package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func TestMistralClientRequestHeaders(t *testing.T) {
	cfg := config.Default()
	cfg.Routing.SessionHeaders = []string{"X-Custom-Identity"}
	h := http.Header{
		"Authorization": {"Bearer synthetic-credential"}, "Cookie": {"sid=synthetic-cookie"},
		"session_id": {"private-session"}, "X-Claude-Code-Parent-Agent-Id": {"private-parent"},
		"X-Custom-Identity": {"private-custom"}, "Thread-Id": {strings.Repeat("x", 513)},
		"Conversation-Id": {"  "}, "X-Session-Id": {"bad\x00value"},
		"X-Affinity": {"private-affinity"}, "X-Prompt-Cache-Key": {"private-cache"},
		"User-Agent": {"synthetic-client/1"}, "Content-Type": {"application/json"},
	}
	out, states, truncated := mistralClientRequestHeaders(cfg, h)
	if truncated || states["session_id"] != "valid" || states["Thread-Id"] != "invalid" || states["Conversation-Id"] != "empty" || states["X-Session-Id"] != "invalid" {
		t.Fatal("incorrect header presence/validity diagnostics")
	}
	for name := range h {
		if name != "User-Agent" && name != "Content-Type" && out[name][0] != "[REDACTED]" {
			t.Fatalf("private header not redacted: %s", name)
		}
	}
	if out["User-Agent"][0] != "synthetic-client/1" || h.Get("Authorization") != "Bearer synthetic-credential" || h["session_id"][0] != "private-session" {
		t.Fatal("safe header lost or original request mutated")
	}
	large := http.Header{}
	for i := range 100 {
		large[fmt.Sprintf("X-Diagnostic-%03d", i)] = []string{strings.Repeat("界", 400), "extra", "extra", "extra", "extra"}
	}
	out, _, truncated = mistralClientRequestHeaders(cfg, large)
	budget := 0
	for name, values := range out {
		budget += len(name)
		if len(values) > 4 {
			t.Fatal("unbounded header values")
		}
		for _, value := range values {
			budget += len(value)
			if len(value) > 512 {
				t.Fatal("unbounded header value")
			}
		}
	}
	if !truncated || len(out) > 64 || budget > 8192 {
		t.Fatal("missing summary bounds/truncation")
	}
}

func TestMistralClientHeadersInRequestLogWithoutDetails(t *testing.T) {
	e := newMistralEnv(t)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		off := false
		c.Mistral.DetailLog = &off
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, bodyIdentity := range []string{"", `,"session_id":"private-body"`} {
		body := `{"model":"m","messages":[{"role":"user","content":"synthetic"}]` + bodyIdentity + `}`
		status, _ := e.post(t, "/v1/chat/completions", body, map[string]string{"Session_id": "private-session", "X-Client-Proof": "received-from-newapi"})
		if status != http.StatusOK {
			t.Fatal("mock request failed")
		}
	}
	logs, err := e.s.logs.Query("requests", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 10, 0)
	if err != nil || len(logs.Items) != 2 {
		t.Fatal("missing request summaries")
	}
	sources := map[string]bool{}
	for _, raw := range logs.Items {
		var rec requestLog
		if json.Unmarshal(raw, &rec) != nil || headerValue(rec.ClientRequestHeaders, "Session_id") != "[REDACTED]" || rec.ClientRequestHeaders["X-Client-Proof"][0] != "received-from-newapi" || rec.AffinityKeyType != "session_id" {
			t.Fatal("missing correlated inbound headers/identity")
		}
		sources[rec.SessionSource] = true
		for _, secret := range []string{"private-session", "private-body", "mistral-client", "mstrl_mock_secret", "apb-mistral-"} {
			if strings.Contains(string(raw), secret) {
				t.Fatal("request summary leaked a private value")
			}
		}
	}
	if !sources["header"] || !sources["body"] {
		t.Fatal("header/body priority diagnosis lost")
	}
	details, _ := e.s.logs.Query("details", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 10, 0)
	if len(details.Items) != 0 {
		t.Fatal("header summaries enabled body detail capture")
	}
}
