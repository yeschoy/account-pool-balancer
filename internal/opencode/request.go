package opencode

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type object = map[string]any

func obj(v any) object { m, _ := v.(map[string]any); return m }
func arr(v any) []any  { a, _ := v.([]any); return a }
func str(v any) string { s, _ := v.(string); return s }
func unsupported(field string) error {
	return fmt.Errorf("unsupported compatibility field or content: %s; use the model's native protocol", field)
}
func fields(m object, names string) error {
	allowed := map[string]bool{}
	for _, s := range strings.Fields(names) {
		allowed[s] = true
	}
	for k, v := range m {
		if v != nil && !allowed[k] {
			return unsupported(k)
		}
	}
	return nil
}

type part struct {
	kind, text, url, id, name, args string
	value                           any
}
type message struct {
	role  string
	parts []part
}
type function struct {
	name, description string
	schema            any
	strict            any
}
type request struct {
	model                        string
	stream                       bool
	messages                     []message
	tools                        []function
	choice                       string
	named                        string
	max, temperature, topP, stop any
}

// ConvertRequest intentionally supports a strict subset. Same-protocol bytes
// are never decoded/re-encoded. In particular signed thinking, server-side
// references and hosted tools must not be flattened or silently removed.
func ConvertRequest(from, to Protocol, b []byte) ([]byte, error) {
	if from == to {
		return b, nil
	}
	var m object
	if json.Unmarshal(b, &m) != nil || m == nil {
		return nil, errors.New("invalid compatibility request JSON")
	}
	r, err := decodeRequest(from, m)
	if err != nil {
		return nil, err
	}
	out, err := encodeRequest(to, r)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

func decodeRequest(p Protocol, m object) (request, error) {
	r := request{model: str(m["model"]), temperature: m["temperature"], topP: m["top_p"]}
	r.stream, _ = m["stream"].(bool)
	if v, exists := m["stream"]; exists && v != nil {
		if _, ok := v.(bool); !ok {
			return r, unsupported("non-boolean stream")
		}
	}
	if v := m["tools"]; v != nil {
		if _, ok := v.([]any); !ok {
			return r, unsupported("non-array tools")
		}
	}
	if p == Chat || p == Messages {
		if _, ok := m["messages"].([]any); !ok {
			return r, unsupported("non-array messages")
		}
	}
	if p == Responses {
		switch m["input"].(type) {
		case string, []any:
		default:
			return r, unsupported("input")
		}
	}
	common := "model stream temperature top_p tools tool_choice "
	switch p {
	case Chat:
		if err := fields(m, common+"messages max_tokens max_completion_tokens stop stream_options"); err != nil {
			return r, err
		}
		r.max = m["max_tokens"]
		if m["max_completion_tokens"] != nil {
			if r.max != nil {
				return r, unsupported("both max_tokens and max_completion_tokens")
			}
			r.max = m["max_completion_tokens"]
		}
		r.stop = m["stop"]
		if m["stream_options"] != nil && obj(m["stream_options"]) == nil {
			return r, unsupported("stream_options")
		}
		if o := obj(m["stream_options"]); o != nil {
			if err := fields(o, "include_usage"); err != nil {
				return r, err
			}
		}
		for _, v := range arr(m["messages"]) {
			x := obj(v)
			if x == nil {
				return r, errors.New("invalid message")
			}
			if err := fields(x, "role content tool_calls tool_call_id"); err != nil {
				return r, err
			}
			msg := message{role: str(x["role"])}
			if msg.role == "tool" {
				text, ok := x["content"].(string)
				if !ok {
					return r, unsupported("non-text tool result")
				}
				msg.parts = []part{{kind: "result", id: str(x["tool_call_id"]), text: text}}
			} else {
				parts, err := decodeContent(Chat, x["content"])
				if err != nil {
					return r, err
				}
				msg.parts = parts
				if x["tool_calls"] != nil {
					if _, ok := x["tool_calls"].([]any); !ok {
						return r, unsupported("tool_calls")
					}
				}
				for _, t := range arr(x["tool_calls"]) {
					tc := obj(t)
					if err := fields(tc, "id type function"); err != nil {
						return r, err
					}
					f := obj(tc["function"])
					if str(tc["type"]) != "function" || f == nil {
						return r, unsupported("non-function tool")
					}
					if err := fields(f, "name arguments"); err != nil {
						return r, err
					}
					msg.parts = append(msg.parts, part{kind: "call", id: str(tc["id"]), name: str(f["name"]), args: str(f["arguments"])})
				}
			}
			r.messages = append(r.messages, msg)
		}
	case Messages:
		if err := fields(m, common+"messages system max_tokens stop_sequences"); err != nil {
			return r, err
		}
		r.max, r.stop = m["max_tokens"], m["stop_sequences"]
		if m["system"] != nil {
			parts, err := decodeContent(Messages, m["system"])
			if err != nil {
				return r, err
			}
			r.messages = append(r.messages, message{role: "system", parts: parts})
		}
		for _, v := range arr(m["messages"]) {
			x := obj(v)
			if x == nil {
				return r, errors.New("invalid message")
			}
			if err := fields(x, "role content"); err != nil {
				return r, err
			}
			parts, err := decodeContent(Messages, x["content"])
			if err != nil {
				return r, err
			}
			r.messages = append(r.messages, message{role: str(x["role"]), parts: parts})
		}
	case Responses:
		if err := fields(m, common+"input instructions max_output_tokens"); err != nil {
			return r, err
		}
		r.max = m["max_output_tokens"]
		if m["instructions"] != nil {
			text, ok := m["instructions"].(string)
			if !ok {
				return r, unsupported("instructions")
			}
			r.messages = append(r.messages, message{role: "system", parts: []part{{kind: "text", text: text}}})
		}
		if text, ok := m["input"].(string); ok {
			r.messages = append(r.messages, message{role: "user", parts: []part{{kind: "text", text: text}}})
		} else {
			for _, v := range arr(m["input"]) {
				x := obj(v)
				if x == nil {
					return r, errors.New("invalid input item")
				}
				switch str(x["type"]) {
				case "function_call":
					if err := fields(x, "type call_id name arguments"); err != nil {
						return r, err
					}
					r.messages = append(r.messages, message{role: "assistant", parts: []part{{kind: "call", id: str(x["call_id"]), name: str(x["name"]), args: str(x["arguments"])}}})
				case "function_call_output":
					if err := fields(x, "type call_id output"); err != nil {
						return r, err
					}
					text, ok := x["output"].(string)
					if !ok {
						return r, unsupported("non-text function_call_output")
					}
					r.messages = append(r.messages, message{role: "tool", parts: []part{{kind: "result", id: str(x["call_id"]), text: text}}})
				case "", "message":
					if err := fields(x, "type role content"); err != nil {
						return r, err
					}
					parts, err := decodeContent(Responses, x["content"])
					if err != nil {
						return r, err
					}
					r.messages = append(r.messages, message{role: str(x["role"]), parts: parts})
				default:
					return r, unsupported("Responses item " + str(x["type"]))
				}
			}
		}
	default:
		return r, unsupported("source protocol")
	}
	for _, v := range arr(m["tools"]) {
		x := obj(v)
		if x == nil {
			return r, errors.New("invalid tool")
		}
		f := x
		if p == Chat {
			if err := fields(x, "type function"); err != nil {
				return r, err
			}
			if str(x["type"]) != "function" {
				return r, unsupported("hosted tool")
			}
			f = obj(x["function"])
		} else if p == Responses && str(x["type"]) != "function" {
			return r, unsupported("hosted tool")
		}
		schema := f["parameters"]
		allowed := "name description parameters strict"
		if p == Messages {
			allowed = "name description input_schema"
			schema = f["input_schema"]
		} else if p == Responses {
			allowed += " type"
		}
		if err := fields(f, allowed); err != nil {
			return r, err
		}
		if str(f["name"]) == "" || schema == nil {
			return r, errors.New("function requires name and schema")
		}
		r.tools = append(r.tools, function{name: str(f["name"]), description: str(f["description"]), schema: schema, strict: f["strict"]})
	}
	if v := m["tool_choice"]; v != nil {
		if s, ok := v.(string); ok {
			r.choice = s
		} else {
			x := obj(v)
			if x == nil {
				return r, unsupported("tool_choice")
			}
			if p == Messages {
				if err := fields(x, "type name"); err != nil {
					return r, err
				}
				switch str(x["type"]) {
				case "auto":
					r.choice = "auto"
				case "none":
					r.choice = "none"
				case "any":
					r.choice = "required"
				case "tool":
					r.choice = "named"
					r.named = str(x["name"])
				default:
					return r, unsupported("tool_choice")
				}
			} else {
				if err := fields(x, "type name function"); err != nil {
					return r, err
				}
				if str(x["type"]) != "function" {
					return r, unsupported("tool_choice")
				}
				r.choice = "named"
				r.named = str(x["name"])
				if p == Chat {
					if err := fields(obj(x["function"]), "name"); err != nil {
						return r, err
					}
					r.named = str(obj(x["function"])["name"])
				}
			}
		}
		if r.choice != "auto" && r.choice != "none" && r.choice != "required" && r.choice != "named" {
			return r, unsupported("tool_choice")
		}
	}
	if len(r.messages) == 0 {
		return r, errors.New("compatibility request requires messages or input")
	}
	calls := map[string]bool{}
	for _, msg := range r.messages {
		switch msg.role {
		case "system", "developer", "user", "assistant", "tool":
		default:
			return r, unsupported("role " + msg.role)
		}
		for _, b := range msg.parts {
			if b.kind == "call" {
				if msg.role != "assistant" || b.id == "" || b.name == "" || calls[b.id] || !json.Valid([]byte(b.args)) {
					return r, errors.New("invalid or duplicate tool call")
				}
				calls[b.id] = true
			}
			if b.kind == "result" {
				if b.id == "" || !calls[b.id] {
					return r, errors.New("tool result requires its original tool call and matching ID in full history")
				}
				delete(calls, b.id)
			}
		}
	}
	return r, nil
}

func decodeContent(p Protocol, v any) ([]part, error) {
	if v == nil {
		return nil, nil
	}
	if s, ok := v.(string); ok {
		return []part{{kind: "text", text: s}}, nil
	}
	a, ok := v.([]any)
	if !ok {
		return nil, unsupported("content")
	}
	var out []part
	for _, v := range a {
		b := obj(v)
		if b == nil {
			return nil, unsupported("content block")
		}
		switch str(b["type"]) {
		case "text", "input_text", "output_text":
			if err := fields(b, "type text"); err != nil {
				return nil, err
			}
			s, ok := b["text"].(string)
			if !ok {
				return nil, unsupported("text")
			}
			out = append(out, part{kind: "text", text: s})
		case "image_url", "input_image", "image":
			var u string
			switch p {
			case Chat:
				if err := fields(b, "type image_url"); err != nil {
					return nil, err
				}
				im := obj(b["image_url"])
				if err := fields(im, "url detail"); err != nil {
					return nil, err
				}
				if im["detail"] != nil && im["detail"] != "auto" {
					return nil, unsupported("image detail")
				}
				u = str(im["url"])
			case Responses:
				if err := fields(b, "type image_url detail"); err != nil {
					return nil, err
				}
				if b["detail"] != nil && b["detail"] != "auto" {
					return nil, unsupported("image detail")
				}
				u = str(b["image_url"])
			case Messages:
				if err := fields(b, "type source"); err != nil {
					return nil, err
				}
				s := obj(b["source"])
				switch s["type"] {
				case "url":
					if err := fields(s, "type url"); err != nil {
						return nil, err
					}
					u = str(s["url"])
				case "base64":
					if err := fields(s, "type media_type data"); err != nil {
						return nil, err
					}
					u = "data:" + str(s["media_type"]) + ";base64," + str(s["data"])
				default:
					return nil, unsupported("image source")
				}
			}
			if u == "" {
				return nil, unsupported("empty image URL")
			}
			out = append(out, part{kind: "image", url: u})
		case "tool_use":
			if p != Messages {
				return nil, unsupported("tool_use")
			}
			if err := fields(b, "type id name input"); err != nil {
				return nil, err
			}
			args, err := json.Marshal(b["input"])
			if err != nil {
				return nil, err
			}
			out = append(out, part{kind: "call", id: str(b["id"]), name: str(b["name"]), args: string(args)})
		case "tool_result":
			if p != Messages {
				return nil, unsupported("tool_result")
			}
			if err := fields(b, "type tool_use_id content"); err != nil {
				return nil, err
			}
			text, ok := b["content"].(string)
			if !ok {
				return nil, unsupported("non-text tool_result")
			}
			out = append(out, part{kind: "result", id: str(b["tool_use_id"]), text: text})
		default:
			return nil, unsupported("block " + str(b["type"]))
		}
	}
	return out, nil
}

func encodeRequest(p Protocol, r request) (object, error) {
	out := object{"model": r.model, "stream": r.stream}
	if p == Chat && r.stream {
		out["stream_options"] = object{"include_usage": true}
	}
	if r.temperature != nil {
		out["temperature"] = r.temperature
	}
	if r.topP != nil {
		out["top_p"] = r.topP
	}
	if r.max != nil {
		switch p {
		case Chat:
			out["max_tokens"] = r.max
		case Messages:
			out["max_tokens"] = r.max
		case Responses:
			out["max_output_tokens"] = r.max
		}
	}
	if p == Messages && r.max == nil {
		return nil, unsupported("Messages requires explicit max_tokens")
	}
	if r.stop != nil {
		switch p {
		case Responses:
			return nil, unsupported("stop sequences")
		case Chat:
			out["stop"] = r.stop
		case Messages:
			if s, ok := r.stop.(string); ok {
				out["stop_sequences"] = []string{s}
			} else {
				out["stop_sequences"] = r.stop
			}
		}
	}
	items := []any{}
	for _, m := range r.messages {
		if p == Messages && m.role == "developer" {
			return nil, unsupported("developer role (cannot merge into system without losing precedence)")
		}
		if p == Messages && m.role == "system" {
			if len(items) > 0 || out["system"] != nil {
				return nil, unsupported("multiple or interleaved system messages")
			}
			blocks, err := encodeParts(p, m.parts)
			if err != nil {
				return nil, err
			}
			out["system"] = blocks
			continue
		}
		if p == Chat {
			for _, b := range m.parts {
				if b.kind == "result" && len(m.parts) != 1 {
					return nil, unsupported("mixed tool result and content in one message")
				}
			}
			seenCall := false
			for _, b := range m.parts {
				if b.kind == "call" {
					seenCall = true
				} else if seenCall {
					return nil, unsupported("content after tool call")
				}
			}
			msg := object{"role": m.role}
			blocks := []any{}
			calls := []any{}
			for _, b := range m.parts {
				switch b.kind {
				case "call":
					calls = append(calls, object{"id": b.id, "type": "function", "function": object{"name": b.name, "arguments": b.args}})
				case "result":
					items = append(items, object{"role": "tool", "tool_call_id": b.id, "content": b.text})
				default:
					x, err := encodeParts(p, []part{b})
					if err != nil {
						return nil, err
					}
					blocks = append(blocks, x...)
				}
			}
			if len(calls) > 0 {
				msg["tool_calls"] = calls
			}
			if len(blocks) > 0 {
				msg["content"] = blocks
			}
			if len(calls) > 0 || len(blocks) > 0 {
				items = append(items, msg)
			}
		} else if p == Responses {
			pending := []part{}
			flush := func() error {
				if len(pending) > 0 {
					blocks, err := encodeParts(p, pending)
					if err != nil {
						return err
					}
					if m.role == "assistant" {
						for _, b := range blocks {
							if obj(b)["type"] == "input_text" {
								obj(b)["type"] = "output_text"
							}
						}
					}
					items = append(items, object{"type": "message", "role": m.role, "content": blocks})
					pending = nil
				}
				return nil
			}
			for _, b := range m.parts {
				switch b.kind {
				case "call", "result":
					if err := flush(); err != nil {
						return nil, err
					}
					if b.kind == "call" {
						items = append(items, object{"type": "function_call", "call_id": b.id, "name": b.name, "arguments": b.args})
					} else {
						items = append(items, object{"type": "function_call_output", "call_id": b.id, "output": b.text})
					}
				default:
					pending = append(pending, b)
				}
			}
			if err := flush(); err != nil {
				return nil, err
			}
		} else {
			role := m.role
			if role == "tool" {
				role = "user"
			}
			blocks, err := encodeParts(p, m.parts)
			if err != nil {
				return nil, err
			}
			// Adjacent same-role messages are merged without moving content blocks.
			if len(items) > 0 && obj(items[len(items)-1])["role"] == role {
				last := obj(items[len(items)-1])
				last["content"] = append(arr(last["content"]), blocks...)
			} else {
				items = append(items, object{"role": role, "content": blocks})
			}
		}
	}
	if p == Responses {
		out["input"] = items
	} else {
		out["messages"] = items
	}
	if len(r.tools) > 0 {
		tools := []any{}
		for _, f := range r.tools {
			t := object{"name": f.name, "description": f.description}
			if p == Messages {
				if f.strict != nil {
					return nil, unsupported("strict tools")
				}
				t["input_schema"] = f.schema
			} else {
				t["parameters"] = f.schema
				if f.strict != nil {
					t["strict"] = f.strict
				}
				if p == Chat {
					t = object{"type": "function", "function": t}
				} else {
					t["type"] = "function"
				}
			}
			tools = append(tools, t)
		}
		out["tools"] = tools
	}
	if r.choice != "" {
		var choice any = r.choice
		if p == Messages {
			mode := r.choice
			if mode == "required" {
				mode = "any"
			}
			if mode == "named" {
				mode = "tool"
			}
			x := object{"type": mode}
			if mode == "tool" {
				x["name"] = r.named
			}
			choice = x
		} else if r.choice == "named" {
			if p == Chat {
				choice = object{"type": "function", "function": object{"name": r.named}}
			} else {
				choice = object{"type": "function", "name": r.named}
			}
		}
		out["tool_choice"] = choice
	}
	return out, nil
}

func encodeParts(p Protocol, parts []part) ([]any, error) {
	out := []any{}
	for _, b := range parts {
		switch b.kind {
		case "text":
			ty := "text"
			if p == Responses {
				ty = "input_text"
			}
			out = append(out, object{"type": ty, "text": b.text})
		case "image":
			switch p {
			case Chat:
				out = append(out, object{"type": "image_url", "image_url": object{"url": b.url}})
			case Responses:
				out = append(out, object{"type": "input_image", "image_url": b.url})
			case Messages:
				source := object{"type": "url", "url": b.url}
				if strings.HasPrefix(b.url, "data:") {
					prefix, data, ok := strings.Cut(strings.TrimPrefix(b.url, "data:"), ";base64,")
					if !ok {
						return nil, unsupported("non-base64 data URL")
					}
					source = object{"type": "base64", "media_type": prefix, "data": data}
				}
				out = append(out, object{"type": "image", "source": source})
			}
		case "call":
			var args any
			if json.Unmarshal([]byte(b.args), &args) != nil || obj(args) == nil {
				return nil, unsupported("non-object tool arguments")
			}
			out = append(out, object{"type": "tool_use", "id": b.id, "name": b.name, "input": args})
		case "result":
			out = append(out, object{"type": "tool_result", "tool_use_id": b.id, "content": b.text})
		default:
			return nil, unsupported("content")
		}
	}
	return out, nil
}
