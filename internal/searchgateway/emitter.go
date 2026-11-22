package searchgateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
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
	last                    object
	publicFields            object
	modelCalls, searchCalls int
	searchCredits           float64
	creditsComplete         bool
}

func newEmitter(w http.ResponseWriter, stream bool, model string) *emitter {
	return &emitter{w: w, stream: stream, responseID: id("resp_apb_"), model: model, created: time.Now().Unix(), output: []json.RawMessage{}, creditsComplete: true}
}
func (e *emitter) response(status string) object {
	o := clone(e.last)
	if o == nil {
		o = object{}
	}
	o["id"] = marshal(e.responseID)
	o["object"] = marshal("response")
	o["model"] = marshal(e.model)
	o["created_at"] = marshal(e.created)
	o["status"] = marshal(status)
	o["output"] = marshal(e.output)
	// Responses commonly echo tools and options. Never expose the internal
	// search function or a reduced per-round token budget in those echoes.
	for _, key := range []string{"tools", "tool_choice", "include", "max_output_tokens", "store"} {
		delete(o, key)
		if v := e.publicFields[key]; v != nil { o[key] = v }
	}
	// The upstream's last round is not the aggregate request's measured usage.
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
	if o == nil {
		o = object{}
	}
	o["type"] = marshal(typ)
	o["sequence_number"] = marshal(e.seq)
	e.seq++
	if !e.started {
		e.w.Header().Set("Content-Type", "text/event-stream")
		e.w.Header().Set("X-Accel-Buffering", "no")
		e.w.WriteHeader(200)
		e.started = true
	}
	b := marshal(o)
	_, e.writeErr = fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", typ, b)
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
	e       *emitter
	indices map[int]int
	ids     map[string]string
	held    map[int]bool
	done    map[int]bool
}

func newRoundEmitter(e *emitter) *roundEmitter {
	return &roundEmitter{e: e, indices: map[int]int{}, ids: map[string]string{}, held: map[int]bool{}, done: map[int]bool{}}
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
func (r *roundEmitter) frame(typ string, o object) error {
	switch typ {
	case "response.created", "response.in_progress", "response.completed", "response.incomplete":
		return nil
	case "error", "response.failed":
		return upstream("upstream_response_failed")
	}
	rawIndex := o["output_index"]
	var index int
	if rawIndex == nil || json.Unmarshal(rawIndex, &index) != nil || index < 0 || index > 4096 {
		return upstream("unsupported_response_event")
	}
	if typ == "response.output_item.added" {
		item, err := decodeObject(o["item"])
		if err != nil {
			return upstream("invalid_output_item")
		}
		if str(item["type"]) == "function_call" {
			r.held[index] = true
			return nil
		}
		if _, ok := r.indices[index]; ok {
			return upstream("duplicate_output_item")
		}
		r.indices[index] = r.e.add(r.publicItem(item))
		return r.e.writeErr
	}
	if r.held[index] {
		return nil
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
		r.e.done(global, r.publicItem(item))
		r.done[index] = true
		return r.e.writeErr
	}
	// Preserve ordinary text/reasoning events and their content, but remap the
	// aggregate output position and public item identity across internal rounds.
	o = clone(o)
	o["output_index"] = marshal(global)
	if old := str(o["item_id"]); old != "" {
		mapped := r.ids[old]
		if mapped == "" {
			return upstream("unknown_output_item_id")
		}
		o["item_id"] = marshal(mapped)
	}
	r.e.event(typ, o)
	return r.e.writeErr
}
func (r *roundEmitter) finalItem(index int, item object) {
	if global, ok := r.indices[index]; ok {
		if !r.done[index] {
			r.e.done(global, r.publicItem(item))
		}
		return
	}
	p := r.publicItem(item)
	g := r.e.add(p)
	r.e.done(g, p)
	r.indices[index] = g
	r.done[index] = true
}
