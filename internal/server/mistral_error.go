package server

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// Only schema coordinates/types may leave the error parser. In particular,
// validation input/ctx and free-form messages may contain an echoed prompt.
type mistralValidation struct {
	Loc  []any  `json:"loc"`
	Type string `json:"type"`
}

func mistralValidationDetails(body []byte) []mistralValidation {
	if len(body) > 64<<10 {
		body = body[:64<<10]
	}
	var out []mistralValidation
	var walk func([]byte, int, bool)
	walk = func(raw []byte, depth int, item bool) {
		raw = bytes.TrimSpace(raw)
		if depth > 8 || len(out) >= 8 || len(raw) == 0 {
			return
		}
		if raw[0] == '"' {
			if decoded := mistralErrorStringPrefix(raw); decoded != nil {
				walk(decoded, depth+1, false)
			}
			return
		}
		if raw[0] == '[' {
			for i, count := skipSpace(raw, 1), 0; i < len(raw) && raw[i] != ']' && count < 32; count++ {
				end := skipValue(raw, i)
				if end < 0 {
					walk(raw[i:], depth+1, true)
					break
				}
				walk(raw[i:end], depth+1, true)
				i = skipSpace(raw, end)
				if i < len(raw) && raw[i] == ',' {
					i = skipSpace(raw, i+1)
				} else {
					break
				}
			}
			return
		}
		if raw[0] != '{' {
			return
		}
		var loc []any
		var typ string
		locSeen, typeSeen, ambiguous := false, false, false
		for i, count := skipSpace(raw, 1), 0; i < len(raw) && raw[i] != '}' && count < 128; count++ {
			if raw[i] != '"' {
				break
			}
			ke := skipString(raw, i)
			if ke < 0 {
				break
			}
			var key string
			if json.Unmarshal(raw[i:ke], &key) != nil {
				break
			}
			colon := skipSpace(raw, ke)
			if colon >= len(raw) || raw[colon] != ':' {
				break
			}
			vs := skipSpace(raw, colon+1)
			end := skipValue(raw, vs)
			valueEnd := end
			if valueEnd < 0 {
				valueEnd = len(raw)
			}
			value := raw[vs:valueEnd]
			switch key {
			case "detail", "errors", "error", "message":
				// Do not search arbitrary subtrees (especially input/ctx).
				if !item {
					walk(value, depth+1, false)
				}
			case "loc":
				if item && end >= 0 {
					ambiguous = ambiguous || locSeen
					locSeen = true
					loc = mistralValidationLocation(value)
				}
			case "type":
				if item && end >= 0 {
					ambiguous = ambiguous || typeSeen
					typeSeen = true
					typ = mistralValidationType(mistralString(value))
				}
			}
			if end < 0 {
				break // retain coordinates parsed BEFORE a truncated echoed input
			}
			i = skipSpace(raw, end)
			if i < len(raw) && raw[i] == ',' {
				i = skipSpace(raw, i+1)
			} else {
				break
			}
		}
		if item && !ambiguous && len(loc) > 0 && typ != "" && len(out) < 8 {
			out = append(out, mistralValidation{Loc: loc, Type: typ})
		}
	}
	walk(body, 0, false)
	return out
}

// A truncated OpenAI message may itself contain an escaped JSON validation
// list. Close only the outer string in memory; discard an incomplete escape.
// The result is still parsed structurally, never logged as message text.
func mistralErrorStringPrefix(raw []byte) []byte {
	if end := skipString(raw, 0); end > 0 {
		var value string
		if json.Unmarshal(raw[:end], &value) == nil {
			return []byte(value)
		}
		return nil
	}
	for cut := 0; cut <= 12 && len(raw)-cut > 1; cut++ {
		candidate := append(append([]byte(nil), raw[:len(raw)-cut]...), '"')
		var value string
		if json.Unmarshal(candidate, &value) == nil {
			return []byte(value)
		}
	}
	return nil
}

func mistralValidationType(value string) string {
	switch value {
	case "extra_forbidden", "union_tag_invalid", "union_tag_not_found", "missing", "string_type", "int_type", "bool_type", "literal_error", "value_error", "list_type", "dict_type", "model_type", "model_attributes_type", "json_invalid", "string_too_long", "string_too_short", "less_than_equal", "greater_than_equal":
		return value
	}
	return ""
}

func mistralValidationLocation(raw []byte) []any {
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 || len(parts) > 20 {
		return nil
	}
	loc := make([]any, 0, len(parts))
	for _, part := range parts {
		if len(part) > 0 && part[0] == '"' {
			name := mistralString(part)
			switch name {
			case "body", "messages", "role", "thinking", "enable_thinking", "type", "budget_tokens", "user", "seed", "random_seed", "store", "verbosity", "safety_identifier", "metadata", "prompt_cache_key", "prompt_cache_retention", "reasoning_effort", "logit_bias", "content", "tool_calls", "tool_call_id", "arguments", "tools", "function", "name", "tool_choice", "parallel_tool_calls", "max_tokens", "max_completion_tokens", "temperature", "top_p", "stream", "stream_options", "include_usage", "include_obfuscation", "response_format", "stop", "n", "safe_prompt", "prediction", "presence_penalty", "frequency_penalty", "assistant", "system", "developer", "tool", "text", "signature", "image_url", "url", "input", "output", "strict":
				loc = append(loc, name)
			default:
				loc = append(loc, "[other]")
			}
		} else if index, err := strconv.Atoi(string(part)); err == nil && index >= 0 && index <= 1000000 {
			loc = append(loc, index)
		} else {
			return nil
		}
	}
	return loc
}
