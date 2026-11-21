package opencode

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// GeminiChatStream turns a Gemini SSE stream into Chat completion chunks as the
// frames arrive. Conversion otherwise has to buffer the whole reply, which makes
// the caller's first token arrive only when the answer is finished.
type GeminiChatStream struct {
	id           string
	model        string
	created      int64
	includeUsage bool
	aliases      map[string]string

	started    bool
	textOpen   bool
	toolIndex  int
	finished   bool
	stop       string
	usage      object
	bytesOut   int
	restoreMap map[string]string
}

// NewGeminiChatStream prepares a streamer for one completion. aliases restore
// the caller's own tool names on the way out.
func NewGeminiChatStream(id, model string, created int64, includeUsage bool, aliases map[string]string) *GeminiChatStream {
	return &GeminiChatStream{id: id, model: model, created: created, includeUsage: includeUsage, aliases: aliases, usage: object{}}
}

// ChatID makes a stable chat completion id from the record's request id.
func ChatID(seed string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "chatcmpl-" + seed
	}
	return "chatcmpl-" + hex.EncodeToString(b)
}

func (s *GeminiChatStream) chunk(delta object, finish any, extra object) object {
	m := object{
		"id": s.id, "object": "chat.completion.chunk", "model": s.model, "created": s.created,
		"choices": []any{object{"index": 0, "delta": delta, "finish_reason": finish}},
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func (s *GeminiChatStream) encode(m object) []byte {
	b, _ := json.Marshal(m)
	s.bytesOut += len(b)
	return append(append([]byte("data: "), b...), []byte("\n\n")...)
}

// FeedEvent consumes one Gemini SSE payload and returns the Chat SSE bytes it
// produces. finished reports that a finish reason was seen and the stream is
// complete; the caller still has to send Finish's closing bytes.
func (s *GeminiChatStream) FeedEvent(payload []byte) (out []byte, finished bool, err error) {
	var m object
	if decodeJSON(payload, &m) != nil || m == nil {
		return nil, false, errors.New("invalid Gemini response JSON")
	}
	if m["error"] != nil || m["type"] == "error" {
		return nil, false, &StreamError{Payload: bytes.Clone(payload), BeforeContent: s.bytesOut == 0}
	}
	if feedback := obj(m["promptFeedback"]); str(feedback["blockReason"]) != "" && feedback["blockReason"] != "BLOCK_REASON_UNSPECIFIED" {
		return nil, false, unsupported("Gemini blocked the prompt")
	}
	if raw := m["usageMetadata"]; raw != nil {
		if u := obj(raw); u != nil {
			for _, k := range []string{"promptTokenCount", "candidatesTokenCount", "cachedContentTokenCount", "thoughtsTokenCount", "totalTokenCount", "toolUsePromptTokenCount"} {
				if v := u[k]; v != nil {
					s.usage[k] = v
				}
			}
		}
	}
	if !s.started {
		s.started = true
		out = append(out, s.encode(s.chunk(object{"role": "assistant", "content": ""}, nil, nil))...)
	}
	cs := arr(m["candidates"])
	if len(cs) == 0 {
		return out, false, nil
	}
	if len(cs) > 1 {
		return out, false, unsupported("multiple or invalid Gemini candidates")
	}
	c := obj(cs[0])
	if c == nil || c["index"] != nil && c["index"] != json.Number("0") {
		return out, false, errors.New("invalid or unexpected Gemini candidate")
	}
	if raw := c["content"]; raw != nil {
		content := obj(raw)
		if content == nil {
			return out, false, errors.New("invalid Gemini candidate content")
		}
		for _, item := range arr(content["parts"]) {
			p := obj(item)
			if p == nil || p["thought"] != nil && p["thought"] != false {
				return out, false, unsupported("Gemini thought content")
			}
			if p["text"] != nil {
				text, ok := p["text"].(string)
				if !ok {
					return out, false, unsupported("non-string Gemini text")
				}
				if text == "" {
					continue
				}
				out = append(out, s.encode(s.chunk(object{"content": text}, nil, nil))...)
				continue
			}
			f := obj(p["functionCall"])
			if f == nil {
				return out, false, unsupported("non-text Gemini output")
			}
			name := str(f["name"])
			if alias := s.aliases[name]; alias != "" {
				name = alias
			}
			args := f["args"]
			if args == nil {
				args = object{}
			}
			argsJSON, err := json.Marshal(args)
			if err != nil {
				return out, false, err
			}
			id := str(f["id"])
			if id == "" {
				id = "call_" + s.id + "_" + strconv.Itoa(s.toolIndex)
			}
			call := object{"index": s.toolIndex, "id": id, "type": "function", "function": object{"name": name, "arguments": string(argsJSON)}}
			s.toolIndex++
			out = append(out, s.encode(s.chunk(object{"tool_calls": []any{call}}, nil, nil))...)
		}
	}
	switch c["finishReason"] {
	case nil, "", "FINISH_REASON_UNSPECIFIED":
		return out, false, nil
	case "STOP":
		s.stop = "stop"
	case "MAX_TOKENS":
		s.stop = "length"
	default:
		return out, false, unsupported("Gemini finishReason (blocked or unsupported output)")
	}
	s.finished = true
	out = append(out, s.encode(s.chunk(object{}, s.stop, nil))...)
	if s.includeUsage {
		out = append(out, s.encode(object{"id": s.id, "object": "chat.completion.chunk", "model": s.model, "created": s.created, "choices": []any{}, "usage": geminiChatUsage(s.usage)})...)
	}
	out = append(out, []byte("data: [DONE]\n\n")...)
	return out, true, nil
}

// Finish closes a stream that ended without a finish reason. Content already
// delivered is kept and reported as truncated.
func (s *GeminiChatStream) Finish() []byte {
	if s.finished {
		return nil
	}
	if !s.started {
		return nil
	}
	out := s.encode(s.chunk(object{}, "length", nil))
	if s.includeUsage {
		out = append(out, s.encode(object{"id": s.id, "object": "chat.completion.chunk", "model": s.model, "created": s.created, "choices": []any{}, "usage": geminiChatUsage(s.usage)})...)
	}
	out = append(out, []byte("data: [DONE]\n\n")...)
	s.finished = true
	return out
}

// Usage returns the upstream token counts for logging.
func (s *GeminiChatStream) Usage() (in, out, cached, reasoning int64, known bool) {
	u, err := geminiTokens(s.usage)
	if err != nil || !u.Known {
		return 0, 0, 0, 0, false
	}
	return u.Input, u.Output, u.Cached, u.Reasoning, true
}

// Started reports whether anything was sent downstream.
func (s *GeminiChatStream) Started() bool { return s.started }

func geminiChatUsage(u object) object {
	usage, _ := geminiTokens(u)
	m := object{"prompt_tokens": usage.Input, "completion_tokens": usage.Output, "total_tokens": usage.Input + usage.Output}
	if usage.CachedKnown {
		m["prompt_tokens_details"] = object{"cached_tokens": usage.Cached}
	}
	return m
}

var _ = fmt.Sprintf
