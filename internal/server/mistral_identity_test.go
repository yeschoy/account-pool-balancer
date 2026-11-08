package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
)

func TestMistralCodexClaudeSessionIdentity(t *testing.T) {
	s := &Server{secret: bytes.Repeat([]byte{1}, 32)}
	cfg := config.Default()
	body, _ := parseRequestBody([]byte(`{"messages":[{"role":"user","content":"question"}]}`))
	for _, header := range []string{"Session-Id", "Session_id", "Thread-Id", "Thread_id", "X-Codex-Session-Id", "X-Codex-Thread-Id", "X-Codex-Conversation-Id", "X-Claude-Code-Session-Id", "X-Claude-Session-Id", "X-Anthropic-Session-Id", "Anthropic-Session-Id"} {
		h := http.Header{strings.ToLower(header): []string{"session-private-value"}}
		id := s.mistralIdentity(cfg, "group\x00key1", h, body)
		if id.Source != "header" || id.Confidence != "explicit" || id.Fingerprint == "" || id.Fingerprint == s.mistralIdentity(cfg, "group\x00key2", h, body).Fingerprint {
			t.Fatalf("missing header or caller isolation: %s", header)
		}
	}
	const session = "12345678-abcd-1234-abcd-123456789abc"
	for _, raw := range []map[string]any{
		{"metadata": map[string]any{"user_id": "user_hash_account_org_session_" + session}},
		{"metadata": map[string]any{"user_id": "  {\"session_id\":\"" + session + "\"}"}},
		{"user": "user_hash_account_org_session_" + session},
		{"user": "{\"session_id\":\"" + session + "\"}"},
	} {
		wire, _ := json.Marshal(raw)
		parsed, err := parseRequestBody(wire)
		id := s.mistralIdentity(cfg, "scope", nil, parsed)
		if err != nil || id.KeyType != "claude_user_session" || id.Fingerprint != s.fingerprint("scope", "session", session) {
			t.Fatal("Claude session structure was lost in routing decode")
		}
		private := thPrivateBody(wire, thIdentityValues(nil, parsed, nil))
		if bytes.Contains(private, []byte(session)) {
			t.Fatal("session identity leaked into detail capture")
		}
	}
	h := http.Header{"X-Prompt-Cache-Key": []string{"cache-private-value"}}
	id := s.mistralIdentity(cfg, "scope", h, body)
	if id.Source != "header" || id.KeyType != "prompt_cache_key" || s.identity(cfg, "scope", h, body).Source != "message_hmac" {
		t.Fatal("missing Mistral cache-hint fallback or altered other-provider priority")
	}
	h.Set("Session-Id", "explicit-session")
	if s.mistralIdentity(cfg, "scope", h, body).KeyType != "session_id" {
		t.Fatal("cache hint overrode explicit session")
	}
	body["user"] = "generic-user-label"
	if s.mistralIdentity(cfg, "scope", http.Header{"X-Request-Id": []string{"changing-request-id"}}, body).Source != "message_hmac" {
		t.Fatal("generic user/request ID was incorrectly treated as conversation identity")
	}
}
