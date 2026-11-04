package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

func goPrivateHeaders(h http.Header) http.Header {
	out := h.Clone()
	for k := range out {
		lower := strings.ToLower(k)
		if strings.Contains(lower, "session") || strings.Contains(lower, "conversation") || strings.Contains(lower, "thread") || strings.Contains(lower, "prompt-cache") || strings.Contains(lower, "prompt_cache") || strings.Contains(lower, "opencode-project") {
			out[k] = []string{"[REDACTED]"}
		}
	}
	return out
}

// Only optional detail capture uses this projection; native request bytes are
// never changed. A malformed body is omitted rather than leaking identifiers.
func goPrivateBody(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil || m == nil {
		return []byte("[Go body omitted: identity redaction unavailable]")
	}
	for _, k := range []string{"prompt_cache_key", "session_id", "conversation_id", "thread_id", "previous_response_id"} {
		if _, ok := m[k]; ok {
			m[k] = "[REDACTED]"
		}
	}
	if meta, ok := m["metadata"].(map[string]any); ok {
		for _, k := range []string{"user_id", "session_id", "sessionId", "conversation_id", "thread_id", "parent_session_id", "project_id"} {
			if _, ok := meta[k]; ok {
				meta[k] = "[REDACTED]"
			}
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return []byte("[Go body omitted]")
	}
	return out
}
