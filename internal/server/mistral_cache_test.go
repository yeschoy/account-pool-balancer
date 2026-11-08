package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
)

func TestMistralSessionPrecedesCacheHint(t *testing.T) {
	s := &Server{secret: bytes.Repeat([]byte{1}, 32)}
	cfg := config.Default()
	for _, header := range []string{"Session-Id", "X-Claude-Code-Session-Id", "X-Pi-Session-Id"} {
		h := http.Header{}
		h.Set(header, "test-private-session")
		var first string
		for _, cache := range []string{"test-private-prefix-a", "test-private-prefix-b"} {
			body := map[string]any{"prompt_cache_key": cache, "messages": []any{map[string]any{"role": "user", "content": "hello"}}}
			id := s.mistralIdentity(cfg, "group\x00caller", h, body)
			if id.Source != "header" || id.KeyType != "session_id" || first != "" && first != id.Fingerprint {
				t.Fatalf("%s: cache hint overrode session identity", header)
			}
			first = id.Fingerprint
			if s.identity(cfg, "group\x00caller", h, body).KeyType != "prompt_cache_key" {
				t.Fatal("other providers' priority changed")
			}
		}
	}
	for _, body := range []map[string]any{
		{"session_id": "test-private-session", "prompt_cache_key": "cache"},
		{"metadata": map[string]any{"user_id": "{\"session_id\":\"test-private-session\"}"}, "prompt_cache_key": "cache"},
		{"metadata": map[string]any{"user_id": "{\"parent_agent_id\":\"test-private-session\"}"}, "prompt_cache_key": "cache"},
		{"user": "{\"agent_id\":\"test-private-session\"}", "prompt_cache_key": "cache"},
	} {
		if id := s.mistralIdentity(cfg, "scope", nil, body); id.KeyType == "prompt_cache_key" {
			t.Fatal("cache hint overrode structured body session")
		}
	}
	body := map[string]any{"prompt_cache_key": "caller-cache"}
	h := http.Header{}
	h.Set("X-Prompt-Cache-Key", "header-cache")
	if id := s.mistralIdentity(cfg, "scope", h, body); id.Source != "body" || id.KeyType != "prompt_cache_key" {
		t.Fatal("body cache key should precede fallback cache header")
	}
}

func TestMistralCamelCacheKeyOnWire(t *testing.T) {
	for _, value := range []string{`"test-private-session"`, `null`, `""`} {
		t.Run(value, func(t *testing.T) {
			e := newMistralEnv(t)
			body := ` { "model":"m", "promptCacheKey":` + value + `,"tools":[{"number":9007199254740993}],"messages":[{"role":"user","content":"<keep> & same"}]} `
			status, reply := e.post(t, "/v1/chat/completions", body, nil)
			if status != 200 {
				t.Fatalf("%d %s", status, reply)
			}
			var sent map[string]json.RawMessage
			json.Unmarshal([]byte(e.call(0).body), &sent)
			if sent["promptCacheKey"] != nil || string(sent["prompt_cache_key"]) != value || !bytes.Contains(sent["tools"], []byte("9007199254740993")) {
				t.Fatal("cache alias was not normalized losslessly")
			}
		})
	}
}

func TestMistralCamelCacheKeyRejectsAmbiguityBeforeAdmission(t *testing.T) {
	for _, fields := range []string{
		`"promptCacheKey":123`,
		`"promptCacheKey":{}`,
		`"promptCacheKey":[]`,
		`"promptCacheKey":"a","promptCacheKey":"a"`,
		`"promptCacheKey":"a","promptCache\u004bey":"a"`,
		`"promptCacheKey":"a","prompt_cache_key":"b"`,
		`"promptCacheKey":"a","prompt_cache_key":null`,
		`"promptCacheKey":null,"prompt_cache_key":""`,
		`"promptCacheKey":"a","prompt_cache_key":"a","prompt_cache_key":"a"`,
	} {
		t.Run(fields, func(t *testing.T) {
			e := newMistralEnv(t)
			status, _ := e.post(t, "/v1/chat/completions", `{"model":"m",`+fields+`,"messages":[]}`, nil)
			if status != 400 || e.count() != 0 {
				t.Fatal("ambiguous cache alias acquired an account or contacted upstream")
			}
		})
	}
}

func TestMistralCamelCacheKeyEqualAliasesAndPrivacy(t *testing.T) {
	e := newMistralEnv(t)
	body := `{"model":"m","promptCacheKey":"private-session-123","prompt_cache_key":"private-session-\u003123","messages":[{"role":"user","content":"private-session-123"}]}`
	if status, reply := e.post(t, "/v1/chat/completions", body, nil); status != 200 {
		t.Fatalf("%d %s", status, reply)
	}
	if strings.Contains(e.call(0).body, "promptCacheKey") || !strings.Contains(e.call(0).body, `"prompt_cache_key":"private-session-\u003123"`) {
		t.Fatal("equal aliases did not preserve canonical wire value")
	}
	for _, preview := range [][]byte{
		thPrivateBody([]byte(body), nil),
		func() []byte { b, _ := thBodyPreview([]byte(body), nil, 4096); return b }(),
	} {
		var obj map[string]any
		if json.Unmarshal(preview, &obj) != nil || obj["promptCacheKey"] != "[REDACTED]" {
			t.Fatal("camelCase identity leaked into diagnostic body")
		}
	}
}

func TestMistralOrdinaryBindingSurvivesServerRestart(t *testing.T) {
	e := newMistralEnv(t)
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error { c.Accounts[0].Enabled = false; return nil }); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"m","messages":[{"role":"user","content":"synthetic restart question"}]}`
	h := map[string]string{"Session-Id": "synthetic-restart-session"}
	if status, _ := e.post(t, "/v1/chat/completions", body, h); status != 200 || e.count() != 1 {
		t.Fatal("mock request failed")
	}
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error { c.Accounts[0].Enabled = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := e.s.saveMetadata(true); err != nil {
		t.Fatal(err)
	}
	// Restart from a fake-data snapshot in another directory, with no two log
	// writers sharing a file and without touching the original test process.
	dir := t.TempDir()
	for _, name := range []string{"config.json", "metadata.json"} {
		b, err := os.ReadFile(filepath.Join(e.s.dataDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	restarted, err := New(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restarted.stopPerch(); restarted.logs.Close() })
	headers := http.Header{}
	headers.Set("Session-Id", h["Session-Id"])
	obj, _ := parseRequestBody([]byte(body))
	identity := restarted.mistralIdentity(restarted.cfg.Get(), config.MistralGroup+"\x00client1", headers, obj)
	if account, ok := restarted.pool.BoundAccount(identity.Fingerprint); !ok || account != "m2" {
		t.Fatal("restart lost ordinary Mistral binding to m2")
	}
}
