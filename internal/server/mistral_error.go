package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

type mistralRequestRejection struct {
	code, message string
}

// Classify only the fixed Mistral HTTP 400 message, even if the large echoed
// image runs past the read limit. Never copy the message or the image URL.
func mistralBadRequest(body []byte, truncated bool) mistralRequestRejection {
	if mistralHasAnimatedGIFError(body) {
		return mistralRequestRejection{
			"animated_gif_unsupported",
			"Animated GIF images are not supported. Replace the GIF in this request or conversation history with a static PNG or JPEG, or start a new conversation without it.",
		}
	}
	if truncated {
		return mistralRequestRejection{"upstream_error_body_truncated", "The upstream service rejected the request (HTTP 400). Its error body exceeded the size limit. Check the request or contact support with the request ID."}
	}
	return mistralRequestRejection{}
}

func mistralHasAnimatedGIFError(body []byte) bool {
	i := skipSpace(body, 0)
	if i >= len(body) || body[i] != '{' {
		return false
	}
	for i = skipSpace(body, i+1); i < len(body) && body[i] == '"'; {
		ke := skipString(body, i)
		if ke < 0 {
			return false
		}
		colon := skipSpace(body, ke)
		if colon >= len(body) || body[colon] != ':' {
			return false
		}
		vs := skipSpace(body, colon+1)
		if mistralString(body[i:ke]) == "message" {
			// Observed native envelope has a top-level message. Don't search
			// arbitrary details or echoed inputs for this phrase.
			return bytes.HasPrefix(body[vs:], []byte(`"Image is an animated GIF. Image URL: `))
		}
		end := skipValue(body, vs)
		if end < 0 {
			return false
		}
		comma := skipSpace(body, end)
		if comma >= len(body) || body[comma] != ',' {
			return false
		}
		i = skipSpace(body, comma+1)
	}
	return false
}

func mistralPublicRequestMessage(err error) string {
	msg := strings.TrimPrefix(err.Error(), "Mistral ")
	if hasMistralErrorBrand([]byte(msg)) {
		return "The request could not be prepared for this endpoint. Check the parameters or contact support with the request ID."
	}
	return msg
}

// Inspect only failures, not generated content or request data. Also recognize
// JSON string escapes so upstream cannot accidentally disclose an escaped name.
func hasMistralErrorBrand(body []byte) bool {
	const brand = "mistral"
	if bytes.Contains(bytes.ToLower(body), []byte(brand)) {
		return true
	}
	if !bytes.Contains(body, []byte(`\u`)) {
		return false
	}
	for i := bytes.IndexByte(body, '"'); i >= 0; i = bytes.IndexByte(body, '"') {
		end := skipString(body, i)
		if end < 0 {
			return true // an escaped prefix cut mid-string cannot be checked safely
		}
		if raw := body[i:end]; bytes.Contains(raw, []byte(`\u`)) && strings.Contains(strings.ToLower(mistralString(raw)), brand) {
			return true
		}
		body = body[end:]
	}
	return false
}

func mistralPublicUpstreamError(status int) (typ, code, message string) {
	switch {
	case status == http.StatusTooManyRequests:
		return "rate_limit_error", "upstream_rate_limited", "The upstream service is rate limited. Retry later."
	case status >= 400 && status < 500:
		return "invalid_request_error", "upstream_request_rejected", fmt.Sprintf("The upstream service rejected the request (HTTP %d). Check the request or contact support with the request ID.", status)
	default:
		return "api_error", "upstream_request_failed", "The upstream request failed. Contact support with the request ID if the error persists."
	}
}

func mistralPublicErrorEvent(status int) []byte {
	typ, code, msg := mistralPublicUpstreamError(status)
	body, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": typ, "code": code, "message": msg}})
	return append(append([]byte("event: error\ndata: "), body...), '\n', '\n')
}

// Only schema coordinates/types may leave the validation parser. In particular,
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
			case "extra_content":
				field := "[other]"
				if len(parts) == 7 && len(loc) == 6 && loc[0] == "body" && loc[1] == "messages" && loc[3] == "assistant" && loc[4] == "tool_calls" {
					_, messageIndex := loc[2].(int)
					_, callIndex := loc[5].(int)
					if messageIndex && callIndex {
						field = name
					}
				}
				loc = append(loc, field)
			case "body", "messages", "role", "thinking", "enable_thinking", "type", "budget_tokens", "user", "seed", "random_seed", "store", "verbosity", "safety_identifier", "metadata", "prompt_cache_key", "prompt_cache_retention", "reasoning_effort", "reasoning", "reasoning_content", "logit_bias", "content", "tool_calls", "tool_call_id", "arguments", "tools", "function", "name", "tool_choice", "parallel_tool_calls", "max_tokens", "max_completion_tokens", "temperature", "top_p", "stream", "stream_options", "include_usage", "include_obfuscation", "response_format", "stop", "n", "safe_prompt", "prediction", "presence_penalty", "frequency_penalty", "assistant", "system", "developer", "tool", "text", "annotations", "signature", "image_url", "url", "input", "output", "strict":
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
