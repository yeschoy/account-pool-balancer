package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	if !truncated || states["session_id"] != "valid" || states["Thread-Id"] != "invalid" || states["Conversation-Id"] != "empty" || states["X-Session-Id"] != "invalid" {
		t.Fatal("incorrect header presence/validity diagnostics")
	}
	for name, values := range h {
		want := clip(values[0], 512)
		if isSecretHeader(name) {
			want = "[REDACTED]"
		}
		if out[name][0] != want {
			t.Fatalf("original diagnostic header lost or credential exposed: %s", name)
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
		if json.Unmarshal(raw, &rec) != nil || headerValue(rec.ClientRequestHeaders, "Session_id") != "private-session" || rec.ClientRequestHeaders["X-Client-Proof"][0] != "received-from-newapi" || rec.AffinityKeyType != "session_id" {
			t.Fatal("missing correlated inbound headers/identity")
		}
		sources[rec.SessionSource] = true
		if headerValue(rec.ClientRequestHeaders, "Authorization") != "[REDACTED]" {
			t.Fatal("client credential exposed")
		}
		// Only the explicitly authorized inbound-header projection contains
		// session values; body identities and all other log fields stay private.
		rec.ClientRequestHeaders = nil
		rest, _ := json.Marshal(rec)
		for _, secret := range []string{"private-session", "private-body", "mistral-client", "mstrl_mock_secret", "apb-mistral-"} {
			if strings.Contains(string(rest), secret) {
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

func TestMistralOriginalHeaderLogsRequireAdmin(t *testing.T) {
	e := newMistralEnv(t)
	headers := map[string]string{"Session-Id": "original-session-123", "Thread-Id": "original-thread-456", "Cookie": "sid=synthetic-cookie"}
	if status, _ := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, headers); status != http.StatusOK {
		t.Fatal("mock inference failed")
	}
	login := httptest.NewRecorder()
	e.s.admin.create(login, httptest.NewRequest("POST", "/api/login", nil))
	for _, authenticated := range []bool{false, true} {
		r := httptest.NewRequest("GET", "/api/logs/requests?provider=mistral", nil)
		r.Header.Set("Authorization", "Bearer mistral-client")
		if authenticated {
			for _, cookie := range login.Result().Cookies() {
				r.AddCookie(cookie)
			}
		}
		w := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(w, r)
		if !authenticated {
			if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), "original-session") {
				t.Fatal("runtime client obtained administrator header logs")
			}
			continue
		}
		var page struct {
			Items []requestLog `json:"items"`
		}
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 {
			t.Fatalf("administrator log read failed: %d", w.Code)
		}
		h := page.Items[0].ClientRequestHeaders
		if headerValue(h, "Session-Id") != headers["Session-Id"] || headerValue(h, "Thread-Id") != headers["Thread-Id"] || headerValue(h, "Cookie") != "[REDACTED]" || headerValue(h, "Authorization") != "[REDACTED]" {
			t.Fatal("original session values lost or credentials exposed")
		}
	}
}

func TestMistralOriginalHeadersKeepCredentialGuard(t *testing.T) {
	cfg := config.Default()
	cfg.Routing.SessionHeaders = []string{"Authorization", "Cookie", "X-Custom-Identity"}
	for _, sh := range sessionHeaders {
		h := http.Header{sh.name: {" session-original ", "second-original"}}
		out, _, _ := mistralClientRequestHeaders(cfg, h)
		if len(out[sh.name]) != 2 || out[sh.name][0] != " session-original " || out[sh.name][1] != "second-original" {
			t.Fatalf("original values changed: %s", sh.name)
		}
	}
	h := http.Header{"Authorization": {"Bearer synthetic-secret"}, "Cookie": {"sid=synthetic-cookie"}, "X-Custom-Identity": {"custom-original"}}
	out, _, _ := mistralClientRequestHeaders(cfg, h)
	if out["Authorization"][0] != "[REDACTED]" || out["Cookie"][0] != "[REDACTED]" || out["X-Custom-Identity"][0] != "custom-original" {
		t.Fatal("configured session headers bypassed the credential guard")
	}
}
