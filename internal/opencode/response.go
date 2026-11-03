package opencode

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Tokens struct {
	Known                                        bool
	CachedKnown, CacheWriteKnown, ReasoningKnown bool
	Input, Output, Cached, CacheWrite, Reasoning int64
}

// Reply keeps ordered content. Signed/encrypted thinking is not representable
// across these protocols and is rejected rather than replaced by fake text.
type Reply struct {
	ID, Model, Stop string
	Parts           []part
	Usage           Tokens
}

func number(v any) int64 { n, _ := v.(float64); return int64(n) }
func usageOf(p Protocol, m object) Tokens {
	u := Tokens{}
	in, out := "input_tokens", "output_tokens"
	if p == Chat {
		in, out = "prompt_tokens", "completion_tokens"
	}
	_, hasIn := m[in].(float64)
	_, hasOut := m[out].(float64)
	u.Known = hasIn && hasOut
	if p == Messages {
		u.Cached = number(m["cache_read_input_tokens"])
		u.CacheWrite = number(m["cache_creation_input_tokens"])
		_, u.CachedKnown = m["cache_read_input_tokens"].(float64)
		_, u.CacheWriteKnown = m["cache_creation_input_tokens"].(float64)
		u.Input = number(m["input_tokens"]) + u.Cached + u.CacheWrite
		u.Output = number(m["output_tokens"])
	} else {
		u.Input = number(m["input_tokens"])
		u.Output = number(m["output_tokens"])
		detail := obj(m["input_tokens_details"])
		if p == Chat {
			u.Input = number(m["prompt_tokens"])
			u.Output = number(m["completion_tokens"])
			detail = obj(m["prompt_tokens_details"])
		}
		u.Cached = number(detail["cached_tokens"])
		u.CacheWrite = number(detail["cache_creation_tokens"])
		_, u.CachedKnown = detail["cached_tokens"].(float64)
		_, u.CacheWriteKnown = detail["cache_creation_tokens"].(float64)
		od := obj(m["output_tokens_details"])
		if p == Chat {
			od = obj(m["completion_tokens_details"])
		}
		u.Reasoning = number(od["reasoning_tokens"])
		_, u.ReasoningKnown = od["reasoning_tokens"].(float64)
	}
	return u
}
func (u Tokens) Encode(p Protocol) object {
	if !u.Known {
		return nil
	}
	if p == Messages {
		out := object{"input_tokens": max(0, u.Input-u.Cached-u.CacheWrite), "output_tokens": u.Output}
		if u.CachedKnown {
			out["cache_read_input_tokens"] = u.Cached
		}
		if u.CacheWriteKnown {
			out["cache_creation_input_tokens"] = u.CacheWrite
		}
		return out
	}
	in, out, details, od := "input_tokens", "output_tokens", "input_tokens_details", "output_tokens_details"
	if p == Chat {
		in, out, details, od = "prompt_tokens", "completion_tokens", "prompt_tokens_details", "completion_tokens_details"
	}
	result := object{in: u.Input, out: u.Output, "total_tokens": u.Input + u.Output}
	d := object{}
	if u.CachedKnown {
		d["cached_tokens"] = u.Cached
	}
	if u.CacheWriteKnown {
		d["cache_creation_tokens"] = u.CacheWrite
	}
	if len(d) > 0 {
		result[details] = d
	}
	if u.ReasoningKnown {
		result[od] = object{"reasoning_tokens": u.Reasoning}
	}
	return result
}
func DecodeReply(p Protocol, b []byte) (*Reply, error) {
	var m object
	if json.Unmarshal(b, &m) != nil || m == nil {
		return nil, errors.New("invalid upstream response JSON")
	}
	r := &Reply{ID: str(m["id"]), Model: str(m["model"]), Usage: usageOf(p, obj(m["usage"]))}
	switch p {
	case Chat:
		choices := arr(m["choices"])
		if len(choices) != 1 {
			return nil, unsupported("multiple/zero response choices")
		}
		c := obj(choices[0])
		msg := obj(c["message"])
		if msg["refusal"] != nil && msg["refusal"] != "" {
			return nil, unsupported("refusal")
		}
		if msg["audio"] != nil || msg["annotations"] != nil {
			return nil, unsupported("audio or annotations")
		}
		if msg["reasoning_content"] != nil || msg["reasoning"] != nil {
			return nil, unsupported("reasoning response conversion")
		}
		parts, err := decodeContent(Chat, msg["content"])
		if err != nil {
			return nil, err
		}
		r.Parts = parts
		for _, v := range arr(msg["tool_calls"]) {
			x := obj(v)
			f := obj(x["function"])
			if x["type"] != "function" {
				return nil, unsupported("response tool")
			}
			r.Parts = append(r.Parts, part{kind: "call", id: str(x["id"]), name: str(f["name"]), args: str(f["arguments"])})
		}
		switch str(c["finish_reason"]) {
		case "stop":
			r.Stop = "stop"
		case "length":
			r.Stop = "length"
		case "tool_calls":
			r.Stop = "tools"
		default:
			return nil, unsupported("finish_reason " + str(c["finish_reason"]))
		}
	case Messages:
		parts, err := decodeContent(Messages, m["content"])
		if err != nil {
			return nil, err
		}
		r.Parts = parts
		switch str(m["stop_reason"]) {
		case "end_turn":
			r.Stop = "stop"
		case "max_tokens":
			r.Stop = "length"
		case "tool_use":
			r.Stop = "tools"
		default:
			return nil, unsupported("stop_reason " + str(m["stop_reason"]))
		}
	case Responses:
		switch str(m["status"]) {
		case "completed":
			r.Stop = "stop"
		case "incomplete":
			if obj(m["incomplete_details"])["reason"] != "max_output_tokens" {
				return nil, unsupported("incomplete reason")
			}
			r.Stop = "length"
		default:
			return nil, unsupported("response status")
		}
		for _, v := range arr(m["output"]) {
			x := obj(v)
			switch x["type"] {
			case "message":
				for _, b := range arr(x["content"]) {
					if a := obj(b)["annotations"]; len(arr(a)) > 0 {
						return nil, unsupported("annotations")
					}
					y := obj(b)
					if y["type"] != "output_text" {
						return nil, unsupported("output content")
					}
					if err := fields(y, "type text annotations"); err != nil {
						return nil, err
					}
					r.Parts = append(r.Parts, part{kind: "text", text: str(y["text"])})
				}
			case "function_call":
				r.Parts = append(r.Parts, part{kind: "call", id: str(x["call_id"]), name: str(x["name"]), args: str(x["arguments"])})
				if r.Stop == "stop" {
					r.Stop = "tools"
				}
			default:
				return nil, unsupported("output item " + str(x["type"]))
			}
		}
	default:
		return nil, unsupported("response protocol")
	}
	for _, p := range r.Parts {
		if p.kind == "call" && (p.id == "" || p.name == "" || !json.Valid([]byte(p.args))) {
			return nil, errors.New("incomplete upstream tool call")
		}
	}
	return r, nil
}
func (r *Reply) Encode(p Protocol) (object, error) {
	if r.ID == "" {
		return nil, errors.New("upstream response missing ID")
	}
	out := object{"id": r.ID, "model": r.Model}
	if r.Usage.Known {
		out["usage"] = r.Usage.Encode(p)
	}
	switch p {
	case Chat:
		msg := object{"role": "assistant"}
		text := ""
		calls := []any{}
		toolsStarted := false
		for _, b := range r.Parts {
			switch b.kind {
			case "text":
				if toolsStarted {
					return nil, unsupported("text after tools in Chat response")
				}
				text += b.text
			case "call":
				toolsStarted = true
				calls = append(calls, object{"id": b.id, "type": "function", "function": object{"name": b.name, "arguments": b.args}})
			default:
				return nil, unsupported("response content")
			}
		}
		msg["content"] = text
		if len(calls) > 0 {
			msg["tool_calls"] = calls
		}
		reason := "stop"
		if r.Stop == "tools" {
			reason = "tool_calls"
		}
		if r.Stop == "length" {
			reason = "length"
		}
		out["object"] = "chat.completion"
		out["created"] = time.Now().Unix()
		out["choices"] = []any{object{"index": 0, "message": msg, "finish_reason": reason}}
	case Messages:
		if !r.Usage.Known {
			return nil, unsupported("missing usage cannot be represented as a measured Messages token count")
		}
		if r.Usage.Reasoning > 0 {
			return nil, unsupported("reasoning token accounting in Messages conversion")
		}
		blocks, err := encodeParts(p, r.Parts)
		if err != nil {
			return nil, err
		}
		reason := "end_turn"
		if r.Stop == "tools" {
			reason = "tool_use"
		}
		if r.Stop == "length" {
			reason = "max_tokens"
		}
		out["type"] = "message"
		out["role"] = "assistant"
		out["content"] = blocks
		out["stop_reason"] = reason
		out["stop_sequence"] = nil
	case Responses:
		out["object"] = "response"
		out["created_at"] = time.Now().Unix()
		out["status"] = "completed"
		out["error"] = nil
		out["incomplete_details"] = nil
		if r.Stop == "length" {
			out["status"] = "incomplete"
			out["incomplete_details"] = object{"reason": "max_output_tokens"}
		}
		items := []any{}
		for i, b := range r.Parts {
			id := fmt.Sprintf("item_%s_%d", r.ID, i)
			switch b.kind {
			case "text":
				items = append(items, object{"type": "message", "id": id, "role": "assistant", "status": "completed", "content": []any{object{"type": "output_text", "text": b.text, "annotations": []any{}}}})
			case "call":
				items = append(items, object{"type": "function_call", "id": id, "call_id": b.id, "name": b.name, "arguments": b.args, "status": "completed"})
			default:
				return nil, unsupported("response content")
			}
		}
		out["output"] = items
	default:
		return nil, unsupported("response protocol")
	}
	return out, nil
}
