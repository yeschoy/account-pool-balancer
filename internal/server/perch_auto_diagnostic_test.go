package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"account-pool-balancer/internal/perchsignupstore"
)

func TestPerchAutoOutboundDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name            string
		workerErr       error
		invalidSession  bool
		activationErr   error
		registerOutcome string
		activateOutcome string
	}{
		{name: "worker failure", workerErr: errors.New("secret-password secret-refresh secret-proxy secret@example.com"), registerOutcome: "failure"},
		{name: "invalid session", invalidSession: true, registerOutcome: "success"},
		{name: "activation failure", activationErr: errors.New("secret-password secret-refresh secret-proxy secret@example.com"), registerOutcome: "success", activateOutcome: "failure"},
		{name: "success", registerOutcome: "success", activateOutcome: "success"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newPerchOpenEnv(t)
			s := e.srv
			_, err := s.perchAuto.store.Import([]perchsignupstore.Candidate{{Login: "secret@example.com", Password: "secret-password", TOTPSecret: "secret-totp", MailboxURL: "https://mail.invalid/secret-refresh", ProxyURL: "http://user:secret-proxy@proxy.invalid:8080"}})
			if err != nil {
				t.Fatal(err)
			}
			rec, err := s.perchAuto.store.Claim()
			if err != nil {
				t.Fatal(err)
			}
			doc := exportDocAsMap("auto-diagnostic")
			session := doc["session"].(map[string]any)
			session["user"] = map[string]any{"id": "auto-diagnostic"}
			doc["supabase_session"] = session
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if tc.invalidSession {
				raw = []byte(`{}`)
			}
			s.perchRegistrations.worker = func(context.Context, registrationInput) ([]byte, error) { return raw, tc.workerErr }
			s.perchRegistrations.activate = func(context.Context, string, []byte) error { return tc.activationErr }
			s.perchAutoRegister(context.Background(), rec, "perch")
			registered := diagnosticRecords(t, s, "register")
			if len(registered) != 1 || registered[0].Outcome != tc.registerOutcome || registered[0].AccountID != "" || registered[0].ProxySource != "explicit_proxy" || registered[0].Target != "unknown" {
				t.Fatalf("registration: %+v", registered)
			}
			activated := diagnosticRecords(t, s, "activate")
			if tc.activateOutcome == "" {
				if len(activated) != 0 {
					t.Fatalf("unexpected activation: %+v", activated)
				}
			} else if len(activated) != 1 || activated[0].Outcome != tc.activateOutcome || activated[0].AccountID != "" || activated[0].ProxySource != "explicit_proxy" || activated[0].Target != "unknown" {
				t.Fatalf("activation: %+v", activated)
			}
		})
	}
}

func TestPerchAutoReactivationDiagnostic(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	autoImportCandidate(t, s)
	rec, err := s.perchAuto.store.Claim()
	if err != nil {
		t.Fatal(err)
	}
	doc := exportDocAsMap("auto-recheck")
	session := doc["session"].(map[string]any)
	session["user"] = map[string]any{"id": "auto-recheck"}
	doc["supabase_session"] = session
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.perchAuto.store.Update(rec.ID, perchsignupstore.Uncertain, raw, ""); err != nil {
		t.Fatal(err)
	}
	s.perchRegistrations.activate = func(context.Context, string, []byte) error { return context.DeadlineExceeded }
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"confirm":true}`))
	req.SetPathValue("id", rec.ID)
	out := httptest.NewRecorder()
	s.apiPerchAutoActivateCandidate(out, req)
	rows := diagnosticRecords(t, s, "activate")
	if out.Code != http.StatusConflict || len(rows) != 1 || rows[0].Outcome != "failure" || rows[0].ErrorClass != "timeout" || rows[0].AccountID != "" || rows[0].ProxySource != "explicit_proxy" || rows[0].Target != "unknown" {
		t.Fatalf("reactivation status=%d diagnostic=%+v", out.Code, rows)
	}
	if rows := diagnosticRecords(t, s, "register"); len(rows) != 0 {
		t.Fatalf("unexpected registration: %+v", rows)
	}
}

func TestPerchAutoDirectActivationDiagnostic(t *testing.T) {
	e := newPerchOpenEnv(t)
	e.srv.perchRegistrations.activate = func(context.Context, string, []byte) error { return nil }
	if err := e.srv.activateAutoSession(context.Background(), "", nil); err != nil {
		t.Fatal(err)
	}
	rows := diagnosticRecords(t, e.srv, "activate")
	if len(rows) != 1 || rows[0].ProxySource != "direct" || rows[0].Target != "unknown" || rows[0].Outcome != "success" || rows[0].AccountID != "" || rows[0].DurationMs < 0 || rows[0].TS.After(time.Now().Add(time.Second)) {
		t.Fatalf("direct activation: %+v", rows)
	}
}
