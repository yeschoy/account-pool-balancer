package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/perchsignupstore"
)

func autoImportCandidate(t *testing.T, s *Server) {
	t.Helper()
	_, err := s.perchAuto.store.Import([]perchsignupstore.Candidate{{Login: "github-user", Password: "secret-password", TOTPSecret: "secret-totp", MailboxURL: "https://mail.example/secret", ProxyURL: "http://proxy.example:8080"}})
	if err != nil {
		t.Fatal(err)
	}
}
func TestPerchAutoSchedulingGates(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		account string
		group   string
		busy    bool
		blocked bool
		want    int
	}{
		{"disabled scheduler", false, "", "", false, false, 0}, {"threshold", true, "normal", "perch", false, false, 0}, {"different group", true, "normal", "other", false, false, 1}, {"disabled account", true, "disabled", "perch", false, false, 1}, {"expired account", true, "expired", "perch", false, false, 1}, {"banned account", true, "banned", "perch", false, false, 1}, {"pending account", true, "pending", "perch", false, false, 0}, {"manual job", true, "", "", true, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newPerchOpenEnv(t)
			s := e.srv
			if tc.account != "" {
				if tc.account == "pending" {
					rec, _ := e.post(map[string]any{"accounts": []any{exportDocAsMap("pending")}, "group": tc.group}, "")
					if rec.Code != 200 {
						t.Fatal(rec.Body.String())
					}
					s.perch.keeper.DropUser("pending")
				} else {
					rec, _ := e.post(map[string]any{"accounts": []any{exportDocAsMap("existing")}, "group": tc.group}, "")
					if rec.Code != 200 {
						t.Fatal(rec.Body.String())
					}
					_, err := s.cfg.Update(-1, func(c *config.Config) error {
						for i := range c.Accounts {
							if c.Accounts[i].PerchUserID == "existing" {
								if tc.account == "disabled" {
									c.Accounts[i].Enabled = false
								}
								if tc.account == "expired" {
									c.Accounts[i].PerchRefreshFailed = true
								}
							}
						}
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
					if tc.account == "banned" {
						for _, a := range s.cfg.Get().Accounts {
							if a.PerchUserID == "existing" {
								s.pool.Quarantine(a.ID, 403, "banned", "perch-banned")
							}
						}
					}
				}
			}
			autoImportCandidate(t, s)
			s.perchAuto.settings.Enabled = tc.enabled
			s.perchAuto.settings.MinAvailable = 1
			s.perchAuto.notBefore = time.Time{}
			s.perchRegistrations.running = tc.busy
			calls := 0
			s.perchRegistrations.worker = func(context.Context, registrationInput) ([]byte, error) { calls++; return nil, errors.New("fake") }
			s.perchAutoCheck(context.Background())
			s.perchAutoCheck(context.Background())
			if calls != tc.want {
				t.Fatalf("worker calls=%d, want %d views=%+v", calls, tc.want, s.perchAccountViews(s.cfg.Get()))
			}
		})
	}
}
func TestPerchAutoOneCandidatePerInterval(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	_, err := s.perchAuto.store.Import([]perchsignupstore.Candidate{{Login: "one", Password: "p", TOTPSecret: "t", MailboxURL: "m", ProxyURL: "http://proxy.example:8080"}, {Login: "two", Password: "p", TOTPSecret: "t", MailboxURL: "m", ProxyURL: "http://proxy.example:8080"}})
	if err != nil {
		t.Fatal(err)
	}
	s.perchAuto.settings.Enabled = true
	s.perchAuto.notBefore = time.Time{}
	calls := 0
	s.perchRegistrations.worker = func(context.Context, registrationInput) ([]byte, error) { calls++; return nil, errors.New("test") }
	s.perchAutoCheck(context.Background())
	s.perchAutoCheck(context.Background())
	list := s.perchAuto.store.List()
	if calls != 1 || list[0].Status != perchsignupstore.Uncertain || list[1].Status != perchsignupstore.Queued {
		t.Fatal("multiple candidates consumed")
	}
}
func TestPerchAutoSettingsAndRedaction(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	if s.perchAuto.settings.Enabled || s.perchAuto.settings.IntervalSec != 60 || s.perchAuto.settings.MinAvailable != 10 || s.perchAuto.settings.Group != "perch" {
		t.Fatal("unexpected defaults")
	}
	if !s.perchAuto.status.LastCheck.IsZero() || !s.perchAuto.notBefore.After(time.Now()) {
		t.Fatal("startup delay incorrectly reported as check")
	}
	autoImportCandidate(t, s)
	rec := httptest.NewRecorder()
	s.apiPerchAuto(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Header().Get("Cache-Control") != "no-store" || strings.Contains(rec.Body.String(), "secret-") {
		t.Fatalf("unsafe response: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	s.apiPerchAutoSettings(rec, httptest.NewRequest("PUT", "/", bytes.NewBufferString(`{"enabled":true,"intervalSec":60,"minAvailable":2,"group":"perch"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("settings: %s", rec.Body.String())
	}
	path := filepath.Join(s.dataDir, "perch-autosignup.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("settings mode: %v %v", info, err)
	}
	b, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(b), "secret") {
		t.Fatalf("settings data: %s %v", b, err)
	}
}
func TestPerchAutoProxyInventory(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	out := httptest.NewRecorder()
	s.apiPerchAutoProxies(out, httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"proxies":[{"url":"http://user:proxysecret@proxy.example:8080"}]}`)))
	if out.Code != 200 || strings.Contains(out.Body.String(), "proxysecret") {
		t.Fatalf("proxy response %d %s", out.Code, out.Body.String())
	}
	out = httptest.NewRecorder()
	s.apiPerchAutoCandidates(out, httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"accounts":[{"login":"x","password":"pw","totpSecret":"otp","mailboxUrl":"mail"}]}`)))
	if out.Code != 200 || strings.Contains(out.Body.String(), "proxysecret") {
		t.Fatalf("candidate response %d %s", out.Code, out.Body.String())
	}
	list := s.perchAuto.store.List()
	record, err := s.perchAuto.store.Get(list[0].ID)
	if err != nil || record.Candidate.ProxyURL != "http://user:proxysecret@proxy.example:8080" {
		t.Fatal("proxy not assigned")
	}
}
func TestPerchAutoNoProxyDoesNotClaim(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	if _, err := s.perchAuto.store.Import([]perchsignupstore.Candidate{{Login: "x", Password: "p", TOTPSecret: "t", MailboxURL: "m"}}); err != nil {
		t.Fatal(err)
	}
	s.perchAuto.settings.Enabled = true
	s.perchAuto.notBefore = time.Time{}
	called := false
	s.perchRegistrations.worker = func(context.Context, registrationInput) ([]byte, error) { called = true; return nil, nil }
	s.perchAutoCheck(context.Background())
	if called || s.perchAuto.store.List()[0].Status != perchsignupstore.Queued || !strings.Contains(s.perchAuto.status.LastError, "代理") {
		t.Fatal("missing proxy must block claim")
	}
}
func TestPerchAutoCannotDeleteWorkingCandidate(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	autoImportCandidate(t, s)
	rec, err := s.perchAuto.store.Claim()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.perchAuto.store.Update(rec.ID, perchsignupstore.Ready, []byte("session"), ""); err != nil {
		t.Fatal(err)
	}
	s.perchAuto.workingID = rec.ID
	req := httptest.NewRequest("DELETE", "/", nil)
	req.SetPathValue("id", rec.ID)
	out := httptest.NewRecorder()
	s.apiPerchAutoDeleteCandidate(out, req)
	if out.Code != http.StatusConflict || len(s.perchAuto.store.List()) != 1 {
		t.Fatal("working session deleted")
	}
}

func TestPerchAutoPersistsBeforeActivation(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	autoImportCandidate(t, s)
	s.perchAuto.settings.Enabled = true
	s.perchAuto.notBefore = time.Time{}
	doc := exportDocAsMap("autosignup")
	session := doc["session"].(map[string]any)
	session["user"] = map[string]any{"id": "autosignup"}
	doc["supabase_session"] = session
	raw, _ := json.Marshal(doc)
	s.perchRegistrations.worker = func(context.Context, registrationInput) ([]byte, error) { return raw, nil }
	s.perchRegistrations.activate = func(ctx context.Context, proxy string, doc []byte) error {
		list := s.perchAuto.store.List()
		record, err := s.perchAuto.store.Get(list[0].ID)
		if err != nil || record.Status != perchsignupstore.Running || !bytes.Equal(record.Session, raw) {
			t.Error("session not persisted before activation")
		}
		return nil
	}
	s.perchAutoCheck(context.Background())
	list := s.perchAuto.store.List()
	if len(list) != 1 || list[0].Status != perchsignupstore.Completed || e.count("autosignup") != 1 {
		t.Fatalf("registration not completed: %+v", list)
	}
}
func TestPerchAutoRevalidateRequiresConfirmation(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	autoImportCandidate(t, s)
	rec, err := s.perchAuto.store.Claim()
	if err != nil {
		t.Fatal(err)
	}
	doc := exportDocAsMap("review-user")
	session := doc["session"].(map[string]any)
	session["user"] = map[string]any{"id": "review-user"}
	doc["supabase_session"] = session
	raw, _ := json.Marshal(doc)
	if err = s.perchAuto.store.Update(rec.ID, perchsignupstore.Uncertain, raw, ""); err != nil {
		t.Fatal(err)
	}
	called := false
	s.perchRegistrations.activate = func(_ context.Context, proxy string, _ []byte) error {
		called = true
		if proxy != "http://proxy.example:8080" {
			t.Error("proxy changed")
		}
		return nil
	}
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"confirm":false}`))
	req.SetPathValue("id", rec.ID)
	out := httptest.NewRecorder()
	s.apiPerchAutoActivateCandidate(out, req)
	if called || out.Code != 400 {
		t.Fatal("confirmation bypassed")
	}
	req = httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"confirm":true}`))
	req.SetPathValue("id", rec.ID)
	out = httptest.NewRecorder()
	s.apiPerchAutoActivateCandidate(out, req)
	if !called || out.Code != 200 || s.perchAuto.store.List()[0].Status != perchsignupstore.Ready {
		t.Fatalf("revalidate %d %s", out.Code, out.Body.String())
	}
}
func TestPerchAutoImportReady(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	autoImportCandidate(t, s)
	rec, err := s.perchAuto.store.Claim()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(exportDocAsMap("ready-user"))
	if err = s.perchAuto.store.Update(rec.ID, perchsignupstore.Ready, raw, ""); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"group":"special"}`))
	req.SetPathValue("id", rec.ID)
	out := httptest.NewRecorder()
	s.apiPerchAutoImportCandidate(out, req)
	if out.Code != 200 || s.perchAuto.store.List()[0].Status != perchsignupstore.Completed || e.count("ready-user") != 1 {
		t.Fatalf("import: %d %s", out.Code, out.Body.String())
	}
}
func TestPerchAutoRejectsMismatchedSession(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	autoImportCandidate(t, s)
	s.perchAuto.settings.Enabled = true
	s.perchAuto.notBefore = time.Time{}
	doc := exportDocAsMap("user-a")
	session := doc["session"].(map[string]any)
	copied := map[string]any{}
	for k, v := range session {
		copied[k] = v
	}
	copied["user"] = map[string]any{"id": "user-b"}
	doc["supabase_session"] = copied
	raw, _ := json.Marshal(doc)
	activated := false
	s.perchRegistrations.worker = func(context.Context, registrationInput) ([]byte, error) { return raw, nil }
	s.perchRegistrations.activate = func(context.Context, string, []byte) error { activated = true; return nil }
	s.perchAutoCheck(context.Background())
	if activated || s.perchAuto.store.List()[0].Status != perchsignupstore.Uncertain || e.count("user-a") != 0 {
		t.Fatal("mismatched identity activated or imported")
	}
}
func TestPerchAutoCancellationIsUncertain(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	autoImportCandidate(t, s)
	s.perchAuto.settings.Enabled = true
	s.perchAuto.notBefore = time.Time{}
	s.perchRegistrations.worker = func(ctx context.Context, _ registrationInput) ([]byte, error) {
		<-ctx.Done()
		return nil, errors.New("sensitive error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.perchAutoCheck(ctx); close(done) }()
	deadline := time.After(3 * time.Second)
	for {
		if s.perchAuto.store.List()[0].Status == perchsignupstore.Running {
			break
		}
		select {
		case <-deadline:
			t.Fatal("not started")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("not cancelled")
	}
	if s.perchAuto.store.List()[0].Status != perchsignupstore.Uncertain {
		t.Fatal("interrupted candidate not uncertain")
	}
	if strings.Contains(s.perchAuto.status.LastError, "sensitive") {
		t.Fatal("error leaked")
	}
}
