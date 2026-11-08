package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

var testMistralClaudeKeys = []string{
	"session_id", "claude_session_id", "agent_id", "thread_id", "conversation_id",
	"parent_session_id", "parent_agent_id", "parent_thread_id", "parent_conversation_id",
}

func mistralAliasBody(t *testing.T, path string, fields any) ([]byte, map[string]any) {
	t.Helper()
	uid, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"model": "m", "messages": []any{map[string]any{"role": "user", "content": "synthetic question"}}}
	if path == "user" {
		body["user"] = string(uid)
	} else {
		body["metadata"] = map[string]any{"user_id": string(uid)}
	}
	wire, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseRequestBody(wire)
	if err != nil {
		t.Fatal(err)
	}
	return wire, parsed
}

func TestMistralClaudeJSONAliases(t *testing.T) {
	s := &Server{secret: bytes.Repeat([]byte{1}, 32)}
	cfg := config.Default()
	for _, path := range []string{"metadata.user_id", "user"} {
		for _, key := range testMistralClaudeKeys {
			t.Run(path+"/"+key, func(t *testing.T) {
				_, body := mistralAliasBody(t, path, map[string]any{key: "synthetic-session"})
				id := s.mistralIdentity(cfg, "group\x00key1", nil, body)
				if id.KeyType != "claude_user_session" || id.Fingerprint != s.fingerprint("group\x00key1", "session", "synthetic-session") {
					t.Fatal("alias not recognized")
				}
				if id != s.mistralIdentity(cfg, "group\x00key1", nil, body) || id.Fingerprint == s.mistralIdentity(cfg, "group\x00key2", nil, body).Fingerprint || id.Fingerprint == s.mistralIdentity(cfg, "other\x00key1", nil, body).Fingerprint {
					t.Fatal("unstable identity or lost caller/group isolation")
				}
				for _, provider := range []string{config.ProviderOpenDesign, config.ProviderOpenCodeZen, config.ProviderOpenCodeGo, config.ProviderTokenHarbor} {
					other := s.identity(cfg.ForProvider(provider), "group\x00key1", nil, body)
					if (key == "session_id") != (other.KeyType == "claude_user_session") {
						t.Fatalf("changed shared identity behavior for %s", provider)
					}
				}
			})
		}
	}
}

func TestMistralClaudeJSONAliasPriority(t *testing.T) {
	fields := map[string]any{}
	for _, key := range testMistralClaudeKeys {
		fields[key] = "synthetic-" + key
	}
	// Repeated suffixes prove the fixed order, independently of map iteration.
	for _, key := range testMistralClaudeKeys {
		uid, _ := json.Marshal(fields)
		if got := mistralClaudeUserSession(string(uid)); got != fields[key] {
			t.Fatalf("wrong priority at %s", key)
		}
		delete(fields, key)
	}
	for _, invalid := range []any{"", "   ", nil, 42, true, []any{"not-string"}, map[string]any{}, "bad\x00id", "\ntrimmed-control", strings.Repeat("x", 513), strings.Repeat("界", 171)} {
		uid, _ := json.Marshal(map[string]any{"session_id": invalid, "claude_session_id": invalid, "agent_id": "valid-fallback"})
		if mistralClaudeUserSession(string(uid)) != "valid-fallback" {
			t.Fatal("invalid candidate blocked the next valid alias")
		}
	}
	for _, uid := range []string{`{"agent_id":null}`, `{"agent_id":42}`, `{"agent_id":`, `ordinary-user`} {
		if mistralClaudeUserSession(uid) != "" {
			t.Fatal("non-session value became identity")
		}
	}
	s := &Server{secret: []byte("synthetic-secret")}
	cfg := config.Default()
	_, body := mistralAliasBody(t, "metadata.user_id", map[string]any{"agent_id": "json-session"})
	h := http.Header{"X-Session-Id": {"header-session"}, "X-Prompt-Cache-Key": {"header-cache"}}
	if s.mistralIdentity(cfg, "scope", h, body).Fingerprint != s.fingerprint("scope", "session", "json-session") {
		t.Fatal("session header displaced the existing body structure priority")
	}
	body["session_id"] = "body-session"
	if s.mistralIdentity(cfg, "scope", h, body).KeyType != "session_id" {
		t.Fatal("alias displaced explicit body identity")
	}
	h.Set("X-Parent-Session-Id", "parent-session")
	if s.mistralIdentity(cfg, "scope", h, body).KeyType != "parent_session" {
		t.Fatal("alias displaced parent header")
	}
	body["metadata"] = map[string]any{"user_id": map[string]any{"agent_id": "not-a-string-contract"}}
	delete(body, "session_id")
	if s.mistralIdentity(cfg, "scope", nil, body).KeyType == "claude_user_session" {
		t.Fatal("expanded the JSON-string carrier contract")
	}
}

func TestMistralClaudeJSONAliasPrivacyAndCacheMock(t *testing.T) {
	e := newMistralEnv(t)
	enableMistralWireDetails(t, e)
	fields := map[string]any{}
	var secrets []string
	for i, key := range testMistralClaudeKeys {
		value := fmt.Sprintf("s%d", i) // Short non-winning aliases must be private too.
		if key == "agent_id" {
			value = "escaped-\"private\nvalue"
		}
		fields[key] = value
		secrets = append(secrets, value)
	}
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": strings.Join(secrets, "|")}})
	})
	for i, path := range []string{"metadata.user_id", "user"} {
		wire, _ := mistralAliasBody(t, path, fields)
		status, _ := e.post(t, "/v1/chat/completions", string(wire), nil)
		if status != http.StatusBadRequest || e.count() != i+1 {
			t.Fatal("unexpected mock outcome or replay")
		}
		call := e.call(i)
		var sent map[string]json.RawMessage
		var original map[string]json.RawMessage
		_ = json.Unmarshal([]byte(call.body), &sent)
		_ = json.Unmarshal(wire, &original)
		var cache string
		_ = json.Unmarshal(sent["prompt_cache_key"], &cache)
		if !strings.HasPrefix(cache, "apb-mistral-") || call.header.Get("X-Affinity") != cache {
			t.Fatal("missing actual outbound cache/affinity hint")
		}
		for key, value := range original {
			if !bytes.Equal(value, sent[key]) {
				t.Fatalf("changed native request field %s", key)
			}
		}
	}
	for _, kind := range []string{"details", "errors"} {
		logs, err := e.s.logs.Query(kind, map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 10, 0)
		if err != nil || len(logs.Items) == 0 {
			t.Fatal("missing mock privacy evidence")
		}
		for _, item := range logs.Items {
			for _, secret := range secrets {
				encoded, _ := json.Marshal(secret)
				if bytes.Contains(item, []byte(secret)) || bytes.Contains(item, encoded[1:len(encoded)-1]) || bytes.Contains(item, []byte("escaped-")) {
					t.Fatal("alias leaked into detail/error logs")
				}
			}
		}
	}
}
