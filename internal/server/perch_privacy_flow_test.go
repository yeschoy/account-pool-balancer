package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"account-pool-balancer/internal/perchsignupstore"
)

// privacyRecorder records the order and count of the activation and privacy
// hooks, and whether each was given the same session.
type privacyRecorder struct {
	mu     sync.Mutex
	calls  []string
	tokens []string
}

func (r *privacyRecorder) record(step string, doc []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, step)
	token, _, _ := perchSessionAccess(doc)
	r.tokens = append(r.tokens, token)
}

func (r *privacyRecorder) count(step string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if c == step {
			n++
		}
	}
	return n
}

// The console's own registration closes collection after activation, with the
// session it just activated, and before the import.
func TestRegistrationDisablesDataProgram(t *testing.T) {
	e := newPerchOpenEnv(t)
	doc := exportDocAsMap("privacy-reg")
	doc["supabase_session"] = doc["session"]
	raw, _ := json.Marshal(doc)
	rec := &privacyRecorder{}
	m := &e.srv.perchRegistrations
	m.worker = func(context.Context, registrationInput) ([]byte, error) { return raw, nil }
	m.activate = func(_ context.Context, _ string, d []byte) error { rec.record("activate", d); return nil }
	m.privacy = func(_ context.Context, _ string, d []byte) error { rec.record("privacy", d); return nil }
	m.jobs = map[string]*registrationJob{"job": {ID: "job", Status: "running", Accounts: []registrationRow{{Login: "user"}}}}
	m.running = true
	e.srv.runPerchRegistration("job", []registrationInput{{Login: "user"}}, false, "perch")

	if got := rec.calls; len(got) != 2 || got[0] != "activate" || got[1] != "privacy" {
		t.Fatalf("call order = %v, want [activate privacy]", got)
	}
	if rec.tokens[1] != "access-privacy-reg" {
		t.Fatalf("privacy got %q", rec.tokens[1])
	}
	row := m.jobs["job"].Accounts[0]
	if row.Status != "activated" || !row.CanImport {
		t.Fatalf("row = %+v", row)
	}
	if row.Detail != "Starter 已激活" {
		t.Fatalf("detail = %q", row.Detail)
	}
}

// A refused switch is reported on the row and never stops the account: it
// stays importable, because a usable account with collection on is still the
// operator's to decide about, and a re-run would register a duplicate.
func TestRegistrationSurvivesDataProgramFailure(t *testing.T) {
	e := newPerchOpenEnv(t)
	doc := exportDocAsMap("privacy-fail")
	doc["supabase_session"] = doc["session"]
	raw, _ := json.Marshal(doc)
	m := &e.srv.perchRegistrations
	m.worker = func(context.Context, registrationInput) ([]byte, error) { return raw, nil }
	m.activate = func(context.Context, string, []byte) error { return nil }
	m.privacy = func(context.Context, string, []byte) error { return errors.New("refused") }
	m.jobs = map[string]*registrationJob{"job": {ID: "job", Status: "running", Accounts: []registrationRow{{Login: "user"}}}}
	m.running = true
	e.srv.runPerchRegistration("job", []registrationInput{{Login: "user"}}, true, "perch")

	row := m.jobs["job"].Accounts[0]
	if row.Status != "imported" || e.count("privacy-fail") != 1 {
		t.Fatalf("refused switch must not block the import: %+v, imported=%d", row, e.count("privacy-fail"))
	}
	// importRegistered overwrites the detail with its own success text, so the
	// warning is visible only on the non-auto path; check that one too.
	m.jobs["job"] = &registrationJob{ID: "job", Status: "running", Accounts: []registrationRow{{Login: "user"}}}
	e.srv.runPerchRegistration("job", []registrationInput{{Login: "user"}}, false, "perch")
	row = m.jobs["job"].Accounts[0]
	if row.Status != "activated" || !row.CanImport || !strings.Contains(row.Detail, "隐私收集关闭失败") {
		t.Fatalf("row = %+v", row)
	}
}

// A registration that fails before activation never reaches the switch: there
// is no session to act on and nothing to report.
func TestRegistrationSkipsDataProgramWhenNotActivated(t *testing.T) {
	e := newPerchOpenEnv(t)
	m := &e.srv.perchRegistrations
	m.worker = func(context.Context, registrationInput) ([]byte, error) { return nil, errors.New("boom") }
	m.privacy = func(context.Context, string, []byte) error {
		t.Fatal("privacy ran for a failed registration")
		return nil
	}
	m.jobs = map[string]*registrationJob{"job": {ID: "job", Status: "running", Accounts: []registrationRow{{Login: "user"}}}}
	m.running = true
	e.srv.runPerchRegistration("job", []registrationInput{{Login: "user"}}, false, "perch")
	if row := m.jobs["job"].Accounts[0]; row.Status != "failed" {
		t.Fatalf("row = %+v", row)
	}
}

// The automatic refill does the same, between activation and ready.
func TestAutoRegisterDisablesDataProgram(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	doc := exportDocAsMap("privacy-auto")
	session := doc["session"].(map[string]any)
	session["user"] = map[string]any{"id": "privacy-auto"}
	doc["supabase_session"] = session
	raw, _ := json.Marshal(doc)
	rec := &privacyRecorder{}
	s.perchRegistrations.worker = func(context.Context, registrationInput) ([]byte, error) { return raw, nil }
	s.perchRegistrations.activate = func(_ context.Context, _ string, d []byte) error { rec.record("activate", d); return nil }
	s.perchRegistrations.privacy = func(_ context.Context, _ string, d []byte) error { rec.record("privacy", d); return nil }
	cands, err := s.perchAuto.store.Import([]perchsignupstore.Candidate{{Login: "auto-user", Password: "p", TOTPSecret: "t", MailboxURL: "m"}})
	if err != nil || len(cands) != 1 {
		t.Fatalf("import candidate: %v %v", err, cands)
	}
	claim, err := s.perchAuto.store.Claim()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.perchAutoRegisterOne(context.Background(), claim); !ok {
		t.Fatalf("register failed: %+v", s.perchAutoStatus())
	}
	if got := rec.calls; len(got) != 2 || got[1] != "privacy" {
		t.Fatalf("calls = %v", got)
	}
	if rec.tokens[1] != "access-privacy-auto" {
		t.Fatalf("privacy got %q", rec.tokens[1])
	}
}

// A refused switch does not stop the candidate from reaching ready, so the
// pool refill still imports it instead of registering a second account.
func TestAutoRegisterSurvivesDataProgramFailure(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	doc := exportDocAsMap("privacy-auto-fail")
	session := doc["session"].(map[string]any)
	session["user"] = map[string]any{"id": "privacy-auto-fail"}
	doc["supabase_session"] = session
	raw, _ := json.Marshal(doc)
	s.perchRegistrations.worker = func(context.Context, registrationInput) ([]byte, error) { return raw, nil }
	s.perchRegistrations.activate = func(context.Context, string, []byte) error { return nil }
	s.perchRegistrations.privacy = func(context.Context, string, []byte) error { return errors.New("refused") }
	if _, err := s.perchAuto.store.Import([]perchsignupstore.Candidate{{Login: "auto-fail", Password: "p", TOTPSecret: "t", MailboxURL: "m"}}); err != nil {
		t.Fatal(err)
	}
	claim, err := s.perchAuto.store.Claim()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.perchAutoRegisterOne(context.Background(), claim); !ok {
		t.Fatal("refused switch must not fail the candidate")
	}
	rec, err := s.perchAuto.store.Get(claim.ID)
	if err != nil || rec.Status != perchsignupstore.Ready {
		t.Fatalf("candidate = %+v err = %v", rec, err)
	}
}

func (s *Server) perchAutoStatus() perchAutoStatus {
	a := s.perchAuto
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}
