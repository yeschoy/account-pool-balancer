package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
)

// prepareMistralRequest only adapts Mistral generation requests. Unchanged
// fields retain their wire bytes, including tools, model IDs and numeric values.
// It does not implement response conversion or server-side continuation.
func prepareMistralRequest(body []byte, path, fingerprint string) ([]byte, string, error) {
	if path != "/v1/chat/completions" && path != "/v1/responses" {
		return body, "", nil
	}
	type change struct {
		start, end int
		value      []byte
	}
	var changes []change
	var model string
	var effort *change
	cacheSeen := false
	i := skipSpace(body, 0) + 1 // caller already validated the root JSON object
	i = skipSpace(body, i)
	for body[i] != '}' {
		ke := skipString(body, i)
		var key string
		_ = json.Unmarshal(body[i:ke], &key)
		vs := skipSpace(body, skipSpace(body, ke)+1)
		ve := skipValue(body, vs)
		switch key {
		case "model":
			_ = json.Unmarshal(body[vs:ve], &model)
		case "reasoning_effort":
			if bytes.Equal(bytes.TrimSpace(body[vs:ve]), []byte(`"medium"`)) {
				effort = &change{vs, ve, []byte(`"high"`)}
			}
		case "prompt_cache_key":
			cacheSeen = true // preserve even explicit null/empty/invalid client values
		case "messages":
			if path == "/v1/chat/completions" {
				messages, changed, err := mistralReasoningMessages(body[vs:ve])
				if err != nil {
					return nil, "", err
				}
				if changed {
					changes = append(changes, change{vs, ve, messages})
				}
			}
		}
		i = skipSpace(body, ve)
		if body[i] == ',' {
			i = skipSpace(body, i+1)
		}
	}
	// This Mistral-hosted model explicitly rejects the generic OpenAI medium
	// tier (3051). Prefer its supported high tier rather than disabling or
	// lowering reasoning. Do not rewrite other models/protocols or native tiers.
	if path == "/v1/chat/completions" && model == "deepseek-v4-1-flash-1017" && effort != nil {
		changes = append(changes, *effort)
		sort.Slice(changes, func(i, j int) bool { return changes[i].start < changes[j].start })
	}
	generated := ""
	if !cacheSeen && fingerprint != "" {
		generated = "apb-mistral-" + fingerprint
		key, _ := json.Marshal(generated)
		field := append([]byte(`"prompt_cache_key":`), key...)
		if skipSpace(body, skipSpace(body, 0)+1) != i {
			field = append([]byte{','}, field...)
		}
		changes = append(changes, change{i, i, field})
	}
	if len(changes) == 0 {
		return body, "", nil
	}
	var out bytes.Buffer
	last := 0
	for _, c := range changes {
		out.Write(body[last:c.start])
		out.Write(c.value)
		last = c.end
	}
	out.Write(body[last:])
	return out.Bytes(), generated, nil
}

func mistralReasoningMessages(raw []byte) ([]byte, bool, error) {
	var messages []json.RawMessage
	if json.Unmarshal(raw, &messages) != nil {
		return raw, false, nil // native validation owns unrelated malformed messages
	}
	changed := false
	for i, message := range messages {
		var fields map[string]json.RawMessage
		if json.Unmarshal(message, &fields) != nil {
			continue
		}
		var role string
		_ = json.Unmarshal(fields["role"], &role)
		reasoning, exists := fields["reasoning_content"]
		if role != "assistant" || !exists {
			continue
		}
		var text *string
		if json.Unmarshal(reasoning, &text) != nil {
			return nil, false, errors.New("Mistral assistant reasoning_content must be a string or null")
		}
		if text != nil && *text != "" {
			thinking, _ := json.Marshal(map[string]any{
				"type":     "thinking",
				"thinking": []map[string]string{{"type": "text", "text": *text}},
			})
			chunks := []json.RawMessage{thinking}
			content := bytes.TrimSpace(fields["content"])
			switch {
			case len(content) == 0 || bytes.Equal(content, []byte("null")):
			case content[0] == '"':
				var answer string
				_ = json.Unmarshal(content, &answer)
				chunk, _ := json.Marshal(map[string]string{"type": "text", "text": answer})
				chunks = append(chunks, chunk)
			case content[0] == '[':
				var existing []json.RawMessage
				_ = json.Unmarshal(content, &existing)
				chunks = append(chunks, existing...)
			default:
				return nil, false, errors.New("Mistral assistant content must be text, chunks or null to preserve reasoning")
			}
			fields["content"], _ = json.Marshal(chunks)
		}
		delete(fields, "reasoning_content")
		messages[i], _ = json.Marshal(fields)
		changed = true
	}
	if !changed {
		return raw, false, nil
	}
	out, err := json.Marshal(messages)
	return out, true, err
}
