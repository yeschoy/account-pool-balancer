package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func TestMistralRequestCacheKeyHeaders(t *testing.T) {
	cfg := config.Default()
	cfg.Routing.SessionHeaders = []string{"X-Custom-Session"}
	ident := Identity{Fingerprint: "scope-hmac"}
	for _, name := range append([]string{"X-Prompt-Cache-Key", "Prompt-Cache-Key", "X-Custom-Session"}, parentHeaders...) {
		h := http.Header{strings.ToLower(name): []string{`session-"quoted"\key`}}
		key, source := mistralRequestCacheKey(cfg, h, ident)
		wantSource := "session_header"
		if strings.Contains(name, "Cache-Key") {
			wantSource = "cache_header"
		}
		if key != `session-"quoted"\key` || source != wantSource {
			t.Fatalf("%s was not copied directly with the correct source", name)
		}
	}
	for _, sh := range sessionHeaders {
		h := http.Header{strings.ToLower(sh.name): []string{"session-private"}}
		if key, source := mistralRequestCacheKey(cfg, h, ident); key != "session-private" || source != "session_header" {
			t.Fatalf("%s was not copied directly", sh.name)
		}
	}
	for _, tc := range []struct {
		name, want, source string
		headers            map[string]string
	}{
		{"cache before session", "cache-private", "cache_header", map[string]string{"X-Prompt-Cache-Key": "cache-private", "X-Custom-Session": "custom-private", "Session-Id": "session-private"}},
		{"cache header aliases", "cache-private", "cache_header", map[string]string{"X-Prompt-Cache-Key": "cache-private", "Prompt-Cache-Key": "other-cache"}},
		{"custom before parent", "custom-private", "session_header", map[string]string{"X-Custom-Session": "custom-private", "X-Parent-Session-Id": "parent-private"}},
		{"parent before session", "parent-private", "session_header", map[string]string{"X-Parent-Session-Id": "parent-private", "Session-Id": "session-private"}},
		{"blank cache skipped", "session-private", "session_header", map[string]string{"X-Prompt-Cache-Key": "   ", "Session-Id": "session-private"}},
		{"oversized cache skipped", "session-private", "session_header", map[string]string{"X-Prompt-Cache-Key": strings.Repeat("x", 513), "Session-Id": "session-private"}},
		{"control cache skipped", "session-private", "session_header", map[string]string{"X-Prompt-Cache-Key": "bad\x00cache", "Session-Id": "session-private"}},
		{"unknown header ignored", "apb-mistral-scope-hmac", "session_hmac", map[string]string{"X-Request-Id": "changing-request-id", "X-Affinity": "routing-only"}},
		{"headerless fallback", "apb-mistral-scope-hmac", "session_hmac", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for name, value := range tc.headers {
				h.Set(name, value)
			}
			if key, source := mistralRequestCacheKey(cfg, h, ident); key != tc.want || source != tc.source {
				t.Fatal("cache/session header precedence or fallback changed")
			}
		})
	}
	if key, source := mistralRequestCacheKey(cfg, nil, Identity{}); key != "" || source != "" {
		t.Fatal("identity-free request received a cache key")
	}
}

func TestMistralHeaderCacheKeysOnWire(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		for _, tc := range []struct {
			name, want, source string
			headers            map[string]string
		}{
			{"cache header", "cache-private", "cache_header", map[string]string{"X-Prompt-Cache-Key": "cache-private", "Session-Id": "session-private"}},
			{"cache alias", "alias-private", "cache_header", map[string]string{"Prompt-Cache-Key": "alias-private"}},
			{"session header", `session-"quoted"\key`, "session_header", map[string]string{"Session-Id": `session-"quoted"\key`}},
			{"custom header", "custom-private", "session_header", map[string]string{"X-Custom-Session": "custom-private"}},
			{"supplied affinity", "session-private", "session_header", map[string]string{"Session-Id": "session-private", "X-Affinity": "caller-affinity"}},
			{"fallback", "", "session_hmac", nil},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				e := newMistralEnv(t)
				if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
					c.Routing.SessionHeaders = []string{"X-Custom-Session"}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				body := ` { "model":"m", "input":"question", "messages":[{"role":"user","content":"question"}], "metadata":{"session_id":"different-body-session"}, "tools":[{"n":9007199254740993}] } `
				status, _ := e.post(t, path, body, tc.headers)
				if status != 200 || e.count() != 1 {
					t.Fatal("mock inference failed")
				}
				call := e.call(0)
				var original, sent map[string]json.RawMessage
				_ = json.Unmarshal([]byte(body), &original)
				if json.Unmarshal([]byte(call.body), &sent) != nil || len(sent) != len(original)+1 {
					t.Fatal("cache fill changed unrelated fields")
				}
				for key, raw := range original {
					if !bytes.Equal(sent[key], raw) {
						t.Fatalf("native field changed: %s", key)
					}
				}
				var key string
				_ = json.Unmarshal(sent["prompt_cache_key"], &key)
				if tc.want != "" && key != tc.want || tc.want == "" && !strings.HasPrefix(key, "apb-mistral-") {
					t.Fatal("outbound cache key did not use the direct header or existing fallback")
				}
				wantAffinity := tc.headers["X-Affinity"]
				if wantAffinity == "" {
					wantAffinity = key
				}
				if call.header.Get("X-Affinity") != wantAffinity {
					t.Fatal("final cache key and affinity header diverged")
				}
				logs, _ := e.s.logs.Query("requests", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 10, 0)
				var rec requestLog
				if len(logs.Items) != 1 || json.Unmarshal(logs.Items[0], &rec) != nil || rec.CacheHintSource != tc.source || strings.Contains(string(logs.Items[0]), key) {
					t.Fatal("cache source missing or private key leaked into request log")
				}
			})
		}
	}
}

func TestMistralExistingCacheKeyPrecedesHeaders(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		for _, value := range []string{`"client-cache"`, `null`, `""`} {
			t.Run(path+"/"+value, func(t *testing.T) {
				e := newMistralEnv(t)
				body := ` { "model":"m", "prompt_cache_key":` + value + `,"messages":[],"input":"question" } `
				status, _ := e.post(t, path, body, map[string]string{"X-Prompt-Cache-Key": "header-cache", "Session-Id": "session-private"})
				if status != 200 || e.count() != 1 || e.call(0).body != body {
					t.Fatal("existing cache value or native body bytes were overwritten")
				}
				wantAffinity := ""
				if value == `"client-cache"` {
					wantAffinity = "client-cache"
				}
				if e.call(0).header.Get("X-Affinity") != wantAffinity {
					t.Fatal("affinity did not follow the existing body cache value")
				}
			})
		}
	}
}
