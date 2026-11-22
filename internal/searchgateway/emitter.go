package searchgateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"account-pool-balancer/internal/websearch"
)

type emitter struct {
	w                       http.ResponseWriter
	stream, started         bool
	responseID, model       string
	created                 int64
	seq                     int
	output                  []json.RawMessage
	writeErr                error
	usage                   json.RawMessage
	last, publicFields      object
	modelCalls, searchCalls int
	searchCredits           float64
	creditsComplete         bool
	sources                 []websearch.Source
	privateValues           []string
}

func newEmitter(w http.ResponseWriter, stream bool, model string) *emitter {
	return &emitter{w: w, stream: stream, responseID: id("resp_apb_"), model: model, created: time.Now().Unix(), output: []json.RawMessage{}, creditsComplete: true}
}
func (e *emitter) response(status string) object {
	o := clone(e.last)
	o["id"] = marshal(e.responseID)
	o["object"] = marshal("response")
	o["model"] = marshal(e.model)
	o["created_at"] = marshal(e.created)
	o["status"] = marshal(status)
	o["output"] = marshal(e.output)
	for _, k := range []string{"tools", "tool_choice", "include", "max_output_tokens", "store"} {
		delete(o, k)
		if v := e.publicFields[k]; v != nil {
			o[k] = v
		}
	}
	delete(o, "usage")
	if e.usage != nil {
		o["usage"] = e.usage
	}
	return o
}
func (e *emitter) event(typ string, o object) {
	if !e.stream || e.writeErr != nil {
		return
	}
	o = clone(o)
	o["type"] = marshal(typ)
	o["sequence_number"] = marshal(e.seq)
	e.seq++
	if !e.started {
		e.w.Header().Set("Content-Type", "text/event-stream")
		e.w.Header().Set("X-Accel-Buffering", "no")
		e.w.WriteHeader(200)
		e.started = true
	}
	_, e.writeErr = fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", typ, marshal(o))
	if f, ok := e.w.(http.Flusher); ok {
		f.Flush()
	}
}
func (e *emitter) begin() {
	if e.stream && !e.started {
		e.event("response.created", object{"response": marshal(e.response("in_progress"))})
		e.event("response.in_progress", object{"response": marshal(e.response("in_progress"))})
	}
}
func (e *emitter) add(item object) int {
	index := len(e.output)
	e.output = append(e.output, marshal(item))
	e.event("response.output_item.added", object{"output_index": marshal(index), "item": marshal(item)})
	return index
}
func (e *emitter) done(index int, item object) {
	e.output[index] = marshal(item)
	e.event("response.output_item.done", object{"output_index": marshal(index), "item": marshal(item)})
}
func (e *emitter) finish(status string) error {
	if e.stream {
		typ := "response.completed"
		if status == "incomplete" {
			typ = "response.incomplete"
		}
		e.event(typ, object{"response": marshal(e.response(status))})
		return e.writeErr
	}
	e.w.Header().Set("Content-Type", "application/json")
	e.w.WriteHeader(200)
	e.started = true
	return json.NewEncoder(e.w).Encode(e.response(status))
}
func (e *emitter) fail(err error) {
	s := safeError(err)
	response := e.response("failed")
	response["error"] = marshal(map[string]string{"code": s.Code, "message": s.Message})
	e.event("response.failed", object{"response": marshal(response)})
}

type roundEmitter struct {
	e           *emitter
	indices     map[int]int
	ids         map[string]string
	held, done  map[int]bool
	itemIDs     map[int]string
	searchItems map[int]object
	parts       map[int]map[int]object
	nativeDone  map[int]json.RawMessage
}

func newRoundEmitter(e *emitter) *roundEmitter {
	return &roundEmitter{e: e, indices: map[int]int{}, ids: map[string]string{}, held: map[int]bool{}, done: map[int]bool{}, itemIDs: map[int]string{}, searchItems: map[int]object{}, parts: map[int]map[int]object{}, nativeDone: map[int]json.RawMessage{}}
}
func (r *roundEmitter) publicItem(item object) object {
	item = clone(item)
	old := str(item["id"])
	if old != "" {
		key := r.ids[old]
		if key == "" {
			key = id("item_apb_")
			r.ids[old] = key
		}
		item["id"] = marshal(key)
	}
	return item
}
func (r *roundEmitter) publicID(index int) string { return r.itemIDs[index] }
func (r *roundEmitter) frame(typ string, o object) error {
	switch typ {
	case "response.created", "response.in_progress", "response.completed", "response.incomplete":
		return nil
	case "error", "response.failed":
		return upstream("upstream_response_failed")
	}
	var index int
	if o["output_index"] == nil || json.Unmarshal(o["output_index"], &index) != nil || index < 0 || index > 4096 {
		return upstream("unsupported_response_event")
	}
	if typ == "response.output_item.added" {
		if _, exists := r.indices[index]; exists || r.held[index] {
			return upstream("duplicate_output_item")
		}
		for heldIndex := range r.held {
			if heldIndex < index {
				return upstream("unfinished_tool_before_output")
			}
		}
		item, err := decodeObject(o["item"])
		if err != nil {
			return upstream("invalid_output_item")
		}
		if str(item["type"]) == "function_call" {
			r.held[index] = true
			return nil
		}
		p := r.publicItem(item)
		r.itemIDs[index] = str(p["id"])
		r.indices[index] = r.e.add(p)
		return r.e.writeErr
	}
	if r.held[index] {
		if typ != "response.output_item.done" {
			return nil
		}
		item, err := decodeObject(o["item"])
		if err != nil {
			return upstream("invalid_output_item")
		}
		delete(r.held, index)
		r.nativeDone[index] = marshal(item)
		if str(item["type"]) != "function_call" {
			return upstream("conflicting_tool_item")
		}
		if str(item["name"]) == searchFunction {
			query, err := searchQuery(item, r.e.privateValues)
			if err != nil {
				return err
			}
			public := object{"id": marshal(id("ws_apb_")), "type": marshal("web_search_call"), "status": marshal("in_progress"), "action": marshal(map[string]any{"type": "search", "query": query})}
			r.searchItems[index] = public
			r.indices[index] = r.e.add(public)
			r.itemIDs[index] = str(public["id"])
		} else {
			r.finalItem(index, item)
		}
		return r.e.writeErr
	}
	global, ok := r.indices[index]
	if !ok {
		return upstream("missing_output_item")
	}
	if typ == "response.output_item.done" {
		item, err := decodeObject(o["item"])
		if err != nil {
			return upstream("invalid_output_item")
		}
		// Annotation events were emitted at content_part.done; final item must carry
		// the identical citation set in JSON, SSE and completed.output.
		r.nativeDone[index] = marshal(item)
		r.annotateItem(index, item)
		r.e.done(global, r.e.citedItem(r.publicItem(item)))
		r.done[index] = true
		return r.e.writeErr
	}
	o = clone(o)
	o["output_index"] = marshal(global)
	if old := str(o["item_id"]); old != "" {
		mapped := r.ids[old]
		if mapped == "" {
			return upstream("unknown_output_item_id")
		}
		o["item_id"] = marshal(mapped)
	}
	var ci int
	if typ == "response.content_part.done" && json.Unmarshal(o["content_index"], &ci) == nil {
		part, err := decodeObject(o["part"])
		if err != nil {
			return upstream("invalid_content_part")
		}
		o["part"] = marshal(r.annotatePart(index, ci, part))
	}
	r.e.event(typ, o)
	return r.e.writeErr
}
func (r *roundEmitter) finalItem(index int, item object) {
	if global, ok := r.indices[index]; ok {
		if !r.done[index] {
			r.annotateItem(index, item)
			r.e.done(global, r.e.citedItem(r.publicItem(item)))
			r.done[index] = true
		}
		return
	}
	p := r.e.citedItem(r.publicItem(item))
	initial := p
	parts, _ := array(p["content"])
	if r.e.stream && str(p["type"]) == "message" && len(parts) > 0 {
		initial = clone(p)
		initial["status"] = marshal("in_progress")
		initial["content"] = marshal([]any{})
	}
	if r.e.stream && str(p["type"]) == "function_call" {
		initial = clone(p)
		initial["status"] = marshal("in_progress")
		initial["arguments"] = marshal("")
	}
	global := r.e.add(initial)
	r.indices[index] = global
	r.itemIDs[index] = str(p["id"])
	if r.e.stream && str(p["type"]) == "function_call" {
		r.e.event("response.function_call_arguments.delta", object{"output_index": marshal(global), "item_id": p["id"], "delta": p["arguments"]})
		r.e.event("response.function_call_arguments.done", object{"output_index": marshal(global), "item_id": p["id"], "arguments": p["arguments"]})
	}
	if r.e.stream && str(p["type"]) == "message" {
		for ci, raw := range parts {
			part, err := decodeObject(raw)
			if err != nil || str(part["type"]) != "output_text" {
				continue
			}
			fields := object{"output_index": marshal(global), "item_id": p["id"], "content_index": marshal(ci)}
			blank := clone(part)
			blank["text"] = marshal("")
			blank["annotations"] = marshal([]any{})
			added := clone(fields)
			added["part"] = marshal(blank)
			r.e.event("response.content_part.added", added)
			text := clone(fields)
			text["delta"] = part["text"]
			r.e.event("response.output_text.delta", text)
			annotations, _ := array(part["annotations"])
			for ai, a := range annotations {
				event := clone(fields)
				event["annotation_index"] = marshal(ai)
				event["annotation"] = a
				r.e.event("response.output_text.annotation.added", event)
			}
			done := clone(fields)
			done["text"] = part["text"]
			r.e.event("response.output_text.done", done)
			done = clone(fields)
			done["part"] = raw
			r.e.event("response.content_part.done", done)
		}
	}
	r.e.done(global, p)
	r.done[index] = true
}
