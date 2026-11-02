package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/perch"
)

func diagnosticRecords(t *testing.T, s *Server, kind string) []perchDiagnostic {
	t.Helper()
	page, err := s.logs.Query("perch", map[string]string{"event": "egress", "kind": kind}, time.Time{}, time.Time{}, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []perchDiagnostic
	for _, raw := range page.Items {
		for _, secret := range []string{"secret-password", "secret-refresh", "secret-proxy", "secret-prompt", "secret@example.com"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("diagnostic contains sensitive text: %s", raw)
			}
		}
		var rec perchDiagnostic
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatal(err)
		}
		out = append(out, rec)
	}
	return out
}

func TestPerchDiagnosticProjection(t *testing.T) {
	e := newPerchOpenEnv(t)
	err := &perch.ModelError{Status: 429, Code: "secret-password", Err: "secret-refresh secret-proxy"}
	e.srv.logPerchDiagnostic("internal-id", "model", "secret-proxy", "https://secret-password.example/path?token=secret-refresh", time.Now(), err)
	rows := diagnosticRecords(t, e.srv, "model")
	if len(rows) != 1 || rows[0].AccountID != "internal-id" || rows[0].Outcome != "failure" || rows[0].Status != 429 || rows[0].ErrorClass != "rate_limited" || rows[0].ProxySource != "unconfigured_unknown" || rows[0].Target != "unknown" {
		t.Fatalf("unexpected diagnostic: %+v", rows)
	}
	for input, expected := range map[string]string{
		"https://app.perchai.app/path":                    "perch-app",
		"https://chat.perchai.app":                        "perch-chat",
		"https://zlfuvsfjtgsdtqcaykia.supabase.co":        "supabase-default",
		"https://secret-password@app.perchai.app":         "unknown",
		"https://secret-password.example":                 "unknown",
		"https://app.perchai.app/?secret=secret-password": "unknown",
		"not a URL": "unknown",
	} {
		if got := perchDiagnosticTarget(input); got != expected {
			t.Errorf("target %q: got %q want %q", input, got, expected)
		}
	}
}

func TestPerchRegistrationDiagnostics(t *testing.T) {
	e := newPerchOpenEnv(t)
	doc := exportDocAsMap("diagnostic")
	doc["supabase_session"] = doc["session"]
	raw, _ := json.Marshal(doc)
	m := &e.srv.perchRegistrations
	m.worker = func(context.Context, registrationInput) ([]byte, error) { return raw, nil }
	m.activate = func(context.Context, string, []byte) error { return errors.New("secret-password secret-refresh") }
	m.jobs = map[string]*registrationJob{"job": {ID: "job", Accounts: []registrationRow{{Login: "secret@example.com", proxyURL: "http://user:secret-proxy@proxy.invalid"}}}}
	e.srv.runPerchRegistration("job", []registrationInput{{Login: "secret@example.com", Password: "secret-password"}}, false, "")
	registered := diagnosticRecords(t, e.srv, "register")
	activated := diagnosticRecords(t, e.srv, "activate")
	if len(registered) != 1 || registered[0].Outcome != "success" || registered[0].AccountID != "" || registered[0].ProxySource != "explicit_proxy" || registered[0].Target != "unknown" {
		t.Fatalf("registration: %+v", registered)
	}
	if len(activated) != 1 || activated[0].Outcome != "failure" || activated[0].ErrorClass != "unknown" || activated[0].AccountID != "" || activated[0].Target != "unknown" {
		t.Fatalf("activation: %+v", activated)
	}
}

func TestPerchModelDiagnostics(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonstream", true: "stream"}[streaming], func(t *testing.T) {
			fake := newPerchModelFake(t)
			e := withPerchPool(t, fake)
			resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
				"model": "gpt-6-sol", "stream": streaming,
				"messages": []any{map[string]any{"role": "user", "content": "secret-prompt"}},
			})
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d", resp.StatusCode)
			}
			// the streaming broker returns before the server has written its
			// diagnostic row, so wait for the log instead of assuming it lost
			// the race to the client-side EOF
			var rows []perchDiagnostic
			for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(5 * time.Millisecond) {
				rows = diagnosticRecords(t, e.srv, "model")
				if len(rows) == 1 || time.Now().After(deadline) {
					break
				}
			}
			if len(rows) != 1 || rows[0].Outcome != "success" || rows[0].AccountID == "" || rows[0].Target != "unknown" {
				t.Fatalf("model: %+v", rows)
			}
		})
	}
}
