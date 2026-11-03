package opencode

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

type StreamError struct {
	Payload       []byte
	BeforeContent bool
}

func (e *StreamError) Error() string { return "upstream stream reported an error" }

// ReadStream is a deliberately buffered compatibility mode, not a low-latency
// native relay. Bound total data, validate completion, then emit an ordered
// target stream. Nothing is sent downstream on a partial/unsupported response.
// Native traffic never enters this function.
func ReadStream(p Protocol, rd io.Reader) (*Reply, error) {
	scanner := bufio.NewScanner(io.LimitReader(rd, (16<<20)+1))
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	event := ""
	lines := []string{}
	total := 0
	terminal := false
	contentSeen := false
	chatFinished := false
	chat := object{"choices": []any{object{"message": object{"role": "assistant", "content": ""}, "finish_reason": "stop"}}}
	msg := obj(arr(chat["choices"])[0])["message"].(map[string]any)
	tools := map[int]object{}
	anthropic := object{"type": "message", "role": "assistant", "content": []any{}, "usage": object{}}
	blocks := map[int]object{}
	args := map[int]string{}
	var response object
	consume := func() error {
		data := strings.Join(lines, "\n")
		lines = nil
		if data == "" {
			event = ""
			return nil
		}
		if data == "[DONE]" {
			terminal = true
			return nil
		}
		var m object
		if json.Unmarshal([]byte(data), &m) != nil {
			return errors.New("invalid upstream SSE JSON")
		}
		typ := event
		if typ == "" {
			typ = str(m["type"])
		}
		if typ == "error" || typ == "response.failed" || m["error"] != nil {
			return &StreamError{Payload: []byte(data), BeforeContent: !contentSeen}
		}
		switch p {
		case Chat:
			if id := str(m["id"]); id != "" {
				chat["id"] = id
			}
			if model := str(m["model"]); model != "" {
				chat["model"] = model
			}
			if m["usage"] != nil {
				chat["usage"] = m["usage"]
			}
			cs := arr(m["choices"])
			if len(cs) > 1 {
				return unsupported("multiple stream choices")
			}
			for _, v := range cs {
				c := obj(v)
				if number(c["index"]) != 0 {
					return unsupported("stream choice index")
				}
				delta := obj(c["delta"])
				if err := fields(delta, "role content tool_calls"); err != nil {
					return err
				}
				if s := delta["content"]; s != nil {
					text, ok := s.(string)
					if !ok {
						return unsupported("stream content")
					}
					msg["content"] = str(msg["content"]) + text
					if text != "" {
						contentSeen = true
					}
				}
				for _, v := range arr(delta["tool_calls"]) {
					contentSeen = true
					t := obj(v)
					i := int(number(t["index"]))
					if err := fields(t, "index id type function"); err != nil {
						return err
					}
					f := obj(t["function"])
					if err := fields(f, "name arguments"); err != nil {
						return err
					}
					tc := tools[i]
					if tc == nil {
						tc = object{"type": "function", "function": object{"name": "", "arguments": ""}}
						tools[i] = tc
					}
					if id := str(t["id"]); id != "" {
						if tc["id"] != nil && tc["id"] != id {
							return errors.New("upstream changed tool ID")
						}
						tc["id"] = id
					}
					tf := obj(tc["function"])
					tf["name"] = str(tf["name"]) + str(f["name"])
					tf["arguments"] = str(tf["arguments"]) + str(f["arguments"])
				}
				if c["finish_reason"] != nil {
					chatFinished = true
					obj(arr(chat["choices"])[0])["finish_reason"] = c["finish_reason"]
				}
			}
		case Messages:
			switch typ {
			case "message_start":
				x := obj(m["message"])
				for _, k := range []string{"id", "model", "usage"} {
					if x[k] != nil {
						anthropic[k] = x[k]
					}
				}
			case "content_block_start":
				i := int(number(m["index"]))
				if blocks[i] != nil {
					return errors.New("duplicate content block")
				}
				b := obj(m["content_block"])
				if b == nil {
					return errors.New("missing content block")
				}
				if b["type"] != "text" && b["type"] != "tool_use" {
					return unsupported("stream block " + str(b["type"]))
				}
				blocks[i] = b
				if b["type"] == "tool_use" || str(b["text"]) != "" {
					contentSeen = true
				}
			case "content_block_delta":
				i := int(number(m["index"]))
				b := blocks[i]
				if b == nil {
					return errors.New("content delta before start")
				}
				d := obj(m["delta"])
				contentSeen = true
				switch d["type"] {
				case "text_delta":
					if b["type"] != "text" {
						return errors.New("invalid text delta")
					}
					b["text"] = str(b["text"]) + str(d["text"])
				case "input_json_delta":
					if b["type"] != "tool_use" {
						return errors.New("invalid tool delta")
					}
					args[i] += str(d["partial_json"])
				default:
					return unsupported("stream delta " + str(d["type"]))
				}
			case "message_delta":
				d := obj(m["delta"])
				if d["stop_reason"] != nil {
					anthropic["stop_reason"] = d["stop_reason"]
				}
				if d["stop_sequence"] != nil {
					return unsupported("stop_sequence")
				}
				u := obj(anthropic["usage"])
				for k, v := range obj(m["usage"]) {
					u[k] = v
				}
				anthropic["usage"] = u
			case "message_stop":
				terminal = true
			case "ping", "content_block_stop":
			default:
				return unsupported("stream event " + typ)
			}
		case Responses:
			if strings.HasPrefix(typ, "response.output_") || strings.HasPrefix(typ, "response.function_call_") {
				contentSeen = true
			}
			if typ == "response.completed" || typ == "response.incomplete" {
				response = obj(m["response"])
				terminal = true
			}
		default:
			return unsupported("stream protocol")
		}
		event = ""
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		total += len(line) + 1
		if total > 16<<20 {
			return nil, errors.New("compatibility stream exceeds 16 MiB")
		}
		if line == "" {
			if err := consume(); err != nil {
				return nil, err
			}
			if terminal {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		} else if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, errors.New("upstream stream read failed")
	}
	if !terminal && len(lines) > 0 {
		if err := consume(); err != nil {
			return nil, err
		}
	}
	if !terminal {
		return nil, errors.New("upstream stream ended before terminal event")
	}
	var final object
	switch p {
	case Chat:
		if !chatFinished {
			return nil, errors.New("Chat stream missing finish_reason")
		}
		indices := []int{}
		for i := range tools {
			indices = append(indices, i)
		}
		sort.Ints(indices)
		ts := []any{}
		for _, i := range indices {
			ts = append(ts, tools[i])
		}
		if len(ts) > 0 {
			msg["tool_calls"] = ts
		}
		final = chat
	case Messages:
		indices := []int{}
		for i := range blocks {
			indices = append(indices, i)
		}
		sort.Ints(indices)
		content := []any{}
		for _, i := range indices {
			b := blocks[i]
			if a, ok := args[i]; ok {
				var input any
				if json.Unmarshal([]byte(a), &input) != nil {
					return nil, errors.New("invalid streamed tool arguments")
				}
				b["input"] = input
			}
			content = append(content, b)
		}
		anthropic["content"] = content
		final = anthropic
	case Responses:
		final = response
	}
	b, err := json.Marshal(final)
	if err != nil {
		return nil, err
	}
	return DecodeReply(p, b)
}

// Stream encodes a complete validated reply with deterministic event ordering.
// Tool call_id / tool_use.id values remain exactly those the upstream issued.
func (r *Reply) Stream(p Protocol) ([]byte, error) {
	full, err := r.Encode(p)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	seq := 0
	emit := func(event string, m object) {
		if p == Responses {
			m["sequence_number"] = seq
			seq++
		}
		b, _ := json.Marshal(m)
		if event != "" {
			fmt.Fprintf(&out, "event: %s\n", event)
		}
		fmt.Fprintf(&out, "data: %s\n\n", b)
	}
	switch p {
	case Chat:
		chunk := func(delta object, finish any) {
			emit("", object{"id": r.ID, "object": "chat.completion.chunk", "model": r.Model, "created": full["created"], "choices": []any{object{"index": 0, "delta": delta, "finish_reason": finish}}})
		}
		chunk(object{"role": "assistant", "content": ""}, nil)
		ti := 0
		for _, b := range r.Parts {
			if b.kind == "text" {
				chunk(object{"content": b.text}, nil)
			} else {
				chunk(object{"tool_calls": []any{object{"index": ti, "id": b.id, "type": "function", "function": object{"name": b.name, "arguments": b.args}}}}, nil)
				ti++
			}
		}
		reason := obj(arr(full["choices"])[0])["finish_reason"]
		chunk(object{}, reason)
		emit("", object{"id": r.ID, "object": "chat.completion.chunk", "model": r.Model, "created": full["created"], "choices": []any{}, "usage": full["usage"]})
		out.WriteString("data: [DONE]\n\n")
	case Messages:
		start := object{"id": r.ID, "type": "message", "role": "assistant", "model": r.Model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": r.Usage.Encode(Messages)}
		obj(start["usage"])["output_tokens"] = 0
		emit("message_start", object{"type": "message_start", "message": start})
		for i, b := range r.Parts {
			initial := object{"type": "text", "text": ""}
			delta := object{"type": "text_delta", "text": b.text}
			if b.kind == "call" {
				initial = object{"type": "tool_use", "id": b.id, "name": b.name, "input": object{}}
				delta = object{"type": "input_json_delta", "partial_json": b.args}
			}
			emit("content_block_start", object{"type": "content_block_start", "index": i, "content_block": initial})
			emit("content_block_delta", object{"type": "content_block_delta", "index": i, "delta": delta})
			emit("content_block_stop", object{"type": "content_block_stop", "index": i})
		}
		emit("message_delta", object{"type": "message_delta", "delta": object{"stop_reason": full["stop_reason"], "stop_sequence": nil}, "usage": object{"output_tokens": r.Usage.Output}})
		emit("message_stop", object{"type": "message_stop"})
	case Responses:
		start := object{}
		for k, v := range full {
			start[k] = v
		}
		start["status"] = "in_progress"
		start["output"] = []any{}
		start["usage"] = nil
		emit("response.created", object{"type": "response.created", "response": start})
		emit("response.in_progress", object{"type": "response.in_progress", "response": start})
		for i, v := range arr(full["output"]) {
			item := obj(v)
			initial := object{}
			for k, v := range item {
				initial[k] = v
			}
			initial["status"] = "in_progress"
			if item["type"] == "function_call" {
				initial["arguments"] = ""
			} else {
				initial["content"] = []any{}
			}
			emit("response.output_item.added", object{"type": "response.output_item.added", "output_index": i, "item": initial})
			if item["type"] == "function_call" {
				emit("response.function_call_arguments.delta", object{"type": "response.function_call_arguments.delta", "item_id": item["id"], "output_index": i, "delta": item["arguments"]})
				emit("response.function_call_arguments.done", object{"type": "response.function_call_arguments.done", "item_id": item["id"], "output_index": i, "arguments": item["arguments"]})
			} else {
				block := arr(item["content"])[0]
				emit("response.content_part.added", object{"type": "response.content_part.added", "item_id": item["id"], "output_index": i, "content_index": 0, "part": object{"type": "output_text", "text": "", "annotations": []any{}}})
				emit("response.output_text.delta", object{"type": "response.output_text.delta", "item_id": item["id"], "output_index": i, "content_index": 0, "delta": obj(block)["text"]})
				emit("response.output_text.done", object{"type": "response.output_text.done", "item_id": item["id"], "output_index": i, "content_index": 0, "text": obj(block)["text"]})
				emit("response.content_part.done", object{"type": "response.content_part.done", "item_id": item["id"], "output_index": i, "content_index": 0, "part": block})
			}
			emit("response.output_item.done", object{"type": "response.output_item.done", "output_index": i, "item": item})
		}
		event := "response.completed"
		if r.Stop == "length" {
			event = "response.incomplete"
		}
		emit(event, object{"type": event, "response": full})
	}
	return out.Bytes(), nil
}
