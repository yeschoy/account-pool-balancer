package server

import (
	"encoding/json"
	"strings"
	"unicode"
)

// Canonical session_id wins; aliases are Mistral-only, in this fixed order.
var mistralClaudeSessionKeys = [...]string{
	"session_id", "claude_session_id", "agent_id", "thread_id", "conversation_id",
	"parent_session_id", "parent_agent_id", "parent_thread_id", "parent_conversation_id",
}

func mistralClaudeUserSession(uid string) string {
	uid = strings.TrimSpace(uid)
	if !strings.HasPrefix(uid, "{") {
		return claudeUserSession(uid) // Preserve the legacy compound-string path.
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(uid), &fields) != nil {
		return ""
	}
	for _, key := range mistralClaudeSessionKeys {
		var value string
		if json.Unmarshal(fields[key], &value) != nil || len(value) > 512 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			continue
		}
		if value, ok := validValue(value); ok {
			return value
		}
	}
	return ""
}

// Extend only Mistral's existing request-local redaction list. Collect every
// string candidate, not just the winning/valid identity: upstreams may echo
// ignored aliases too. Include JSON escaping for error/body string echoes.
func mistralClaudeIdentityValues(body map[string]any) []string {
	var out []string
	add := func(value string) {
		if value == "" {
			return
		}
		out = append(out, value)
		encoded, _ := json.Marshal(value)
		out = append(out, string(encoded[1:len(encoded)-1]))
	}
	for _, path := range []string{"metadata.user_id", "user"} {
		uid := lookup(body, path)
		add(uid)
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(uid), &fields) != nil {
			continue
		}
		for _, key := range mistralClaudeSessionKeys {
			var value string
			if json.Unmarshal(fields[key], &value) == nil {
				add(value)
				add(strings.TrimSpace(value))
			}
		}
	}
	return out
}
