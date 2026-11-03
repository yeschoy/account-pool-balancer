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
	"account-pool-balancer/internal/perch"
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
func TestPerchAutoEmptyInventoryUsesDirect(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	if _, err := s.perchAuto.store.Import([]perchsignupstore.Candidate{{Login: "x", Password: "p", TOTPSecret: "t", MailboxURL: "m"}}); err != nil {
		t.Fatal(err)
	}
	s.perchAuto.settings.Enabled = true
	s.perchAuto.notBefore = time.Time{}
	var got registrationInput
	called := false
	s.perchRegistrations.worker = func(_ context.Context, in registrationInput) ([]byte, error) {
		called, got = true, in
		return nil, errors.New("stopped after claim")
	}
	s.perchRegistrations.activate = func(context.Context, string, []byte) error { return nil }
	s.perchAutoCheck(context.Background())
	if !called || got.ProxyURL != "" {
		t.Fatalf("direct registration not started: %+v", got)
	}
	if s.perchAuto.store.List()[0].Status != perchsignupstore.Uncertain || strings.Contains(s.perchAuto.status.LastError, "代理") {
		t.Fatalf("direct claim blocked: %s", s.perchAuto.status.LastError)
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
	doc := exportDocAsMap("ready-user")
	// A real export carries the envelope; validAutoSession requires it to
	// agree with session field for field.
	session := doc["session"].(map[string]any)
	session["user"] = map[string]any{"id": "ready-user"}
	doc["supabase_session"] = session
	raw, _ := json.Marshal(doc)
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

// A candidate registered ahead of time is imported by the refill, never registered twice.
func TestPerchAutoImportsRegisteredCandidateWithoutRegistering(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	autoImportCandidate(t, s)
	// Register ahead of time: the candidate is already ready with a session.
	rec, err := s.perchAuto.store.Claim()
	if err != nil {
		t.Fatal(err)
	}
	doc := exportDocAsMap("ahead-user")
	session := doc["session"].(map[string]any)
	session["user"] = map[string]any{"id": "ahead-user"}
	doc["supabase_session"] = session
	raw, _ := json.Marshal(doc)
	if err = s.perchAuto.store.Update(rec.ID, perchsignupstore.Ready, raw, ""); err != nil {
		t.Fatal(err)
	}
	registered := false
	s.perchRegistrations.worker = func(context.Context, registrationInput) ([]byte, error) {
		registered = true
		return nil, errors.New("must not register again")
	}
	activated := false
	s.perchRegistrations.activate = func(context.Context, string, []byte) error {
		activated = true
		return nil
	}
	s.perchAuto.settings.Enabled = true
	s.perchAuto.notBefore = time.Time{}
	s.perchAutoCheck(context.Background())
	if registered || activated {
		t.Fatalf("registered=%v activated=%v, want neither", registered, activated)
	}
	if got := s.perchAuto.store.List(); len(got) != 1 || got[0].Status != perchsignupstore.Completed {
		t.Fatalf("candidate not imported: %+v", got)
	}
	if e.count("ahead-user") != 1 {
		t.Fatal("registered account was not imported")
	}
}

// Pending registrations do not stop a refill when a registered one is available.
func TestPerchAutoRegisteredCandidateUnblocksRefill(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	// One queued candidate (not yet registered) and one ready candidate.
	autoImportCandidate(t, s)
	rec, err := s.perchAuto.store.Claim()
	if err != nil {
		t.Fatal(err)
	}
	doc := exportDocAsMap("pool-user")
	session := doc["session"].(map[string]any)
	session["user"] = map[string]any{"id": "pool-user"}
	doc["supabase_session"] = session
	raw, _ := json.Marshal(doc)
	if err = s.perchAuto.store.Update(rec.ID, perchsignupstore.Ready, raw, ""); err != nil {
		t.Fatal(err)
	}
	s.perchAuto.settings.Enabled = true
	s.perchAuto.notBefore = time.Time{}
	registered := false
	s.perchRegistrations.worker = func(context.Context, registrationInput) ([]byte, error) {
		registered = true
		return nil, errors.New("stop")
	}
	s.perchAutoCheck(context.Background())
	if registered {
		t.Fatal("refill registered a candidate while a ready one existed")
	}
	if e.count("pool-user") != 1 {
		t.Fatal("ready candidate was not imported")
	}
}

// The batch endpoint registers only the chosen candidates and leaves them ready.
func TestPerchAutoBatchRegisterSelected(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	for _, login := range []string{"batch-a", "batch-b"} {
		if _, err := s.perchAuto.store.Import([]perchsignupstore.Candidate{{Login: login, Password: "p", TOTPSecret: "t", MailboxURL: "m"}}); err != nil {
			t.Fatal(err)
		}
	}
	list := s.perchAuto.store.List()
	var chosen string
	for _, x := range list {
		if x.Login == "batch-a" {
			chosen = x.ID
		}
	}
	body, _ := json.Marshal(map[string]any{"ids": []string{chosen}, "group": "perch"})
	doc := exportDocAsMap("batch-a")
	session := doc["session"].(map[string]any)
	session["user"] = map[string]any{"id": "batch-a"}
	doc["supabase_session"] = session
	raw, _ := json.Marshal(doc)
	var registered []string
	s.perchRegistrations.worker = func(_ context.Context, in registrationInput) ([]byte, error) {
		registered = append(registered, in.Login)
		return raw, nil
	}
	s.perchRegistrations.activate = func(context.Context, string, []byte) error { return nil }
	out := httptest.NewRecorder()
	s.apiPerchAutoRegisterBatch(out, httptest.NewRequest("POST", "/", bytes.NewReader(body)))
	if out.Code != http.StatusAccepted {
		t.Fatalf("batch %d %s", out.Code, out.Body.String())
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("batch did not finish")
		case <-time.After(10 * time.Millisecond):
		}
		states := map[string]perchsignupstore.Status{}
		for _, x := range s.perchAuto.store.List() {
			states[x.Login] = x.Status
		}
		if states["batch-a"] == perchsignupstore.Ready {
			if states["batch-b"] != perchsignupstore.Queued {
				t.Fatalf("unchosen candidate changed: %v", states)
			}
			break
		}
	}
	if len(registered) != 1 || registered[0] != "batch-a" {
		t.Fatalf("registered %v, want only batch-a", registered)
	}
	// Registering ahead of time must not import: that happens on the next refill.
	if e.count("batch-a") != 0 {
		t.Fatal("batch register imported the account")
	}
}

// A candidate registered weeks ago has an expired access token. Refilling it
// must renew the stored session with its refresh token instead of failing
// forever on the same expired document.
func TestPerchAutoRenewsExpiredReadySessionBeforeImport(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	autoImportCandidate(t, s)
	rec, err := s.perchAuto.store.Claim()
	if err != nil {
		t.Fatal(err)
	}
	doc := exportDocAsMap("aged-user")
	doc["session"].(map[string]any)["expires_at"] = time.Now().Add(-72 * time.Hour).Unix()
	doc["supabase_session"] = doc["session"]
	raw, _ := json.Marshal(doc)
	if err = s.perchAuto.store.Update(rec.ID, perchsignupstore.Ready, raw, ""); err != nil {
		t.Fatal(err)
	}
	renewed, usedProxy := false, "unset"
	renew := func(_ context.Context, creds perch.Creds, token string) (perch.Session, error) {
		renewed, usedProxy = true, creds.ProxyURL
		if token != "refresh-aged-user" {
			t.Errorf("renewed with %q, want the stored refresh token", token)
		}
		return perch.Session{AccessToken: "access-renewed", RefreshToken: "refresh-rotated", ExpiresAt: time.Now().Add(time.Hour).Unix(), UserID: "aged-user"}, nil
	}
	s.perch.renew = renew

	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"group":"special"}`))
	req.SetPathValue("id", rec.ID)
	out := httptest.NewRecorder()
	s.apiPerchAutoImportCandidate(out, req)
	if out.Code != 200 {
		t.Fatalf("import: %d %s", out.Code, out.Body.String())
	}
	if !renewed {
		t.Fatal("expired session was not renewed")
	}
	if usedProxy != "http://proxy.example:8080" {
		t.Fatalf("renewal used proxy %q, want the candidate's proxy", usedProxy)
	}
	if e.count("aged-user") != 1 {
		t.Fatal("renewed candidate was not imported")
	}
}

// A refused refresh token must not be retried forever: it falls back to the
// uncertain state an operator can revalidate.
func TestPerchAutoRefusedRenewalStaysActionable(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	autoImportCandidate(t, s)
	rec, err := s.perchAuto.store.Claim()
	if err != nil {
		t.Fatal(err)
	}
	doc := exportDocAsMap("refused-user")
	doc["session"].(map[string]any)["expires_at"] = time.Now().Add(-time.Hour).Unix()
	raw, _ := json.Marshal(doc)
	if err = s.perchAuto.store.Update(rec.ID, perchsignupstore.Ready, raw, ""); err != nil {
		t.Fatal(err)
	}
	s.perch.renew = func(context.Context, perch.Creds, string) (perch.Session, error) {
		return perch.Session{}, &perch.Error{Kind: perch.KindAuth, Message: "refresh token revoked"}
	}
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"group":"special"}`))
	req.SetPathValue("id", rec.ID)
	out := httptest.NewRecorder()
	s.apiPerchAutoImportCandidate(out, req)
	if out.Code != 409 || !strings.Contains(out.Body.String(), "重新注册") {
		t.Fatalf("refused renewal: %d %s", out.Code, out.Body.String())
	}
	if got := s.perchAuto.store.List(); len(got) != 1 || got[0].Status != perchsignupstore.Ready {
		t.Fatalf("candidate state changed on a refused renewal: %+v", got)
	}
	if e.count("refused-user") != 0 {
		t.Fatal("refused renewal still imported the account")
	}
}

// A cancelled or timed-out refresh must say so. Reporting every transient
// failure as "Perch unavailable" sent one investigation after the proxy when
// the browser had simply gone away.
func TestAutoRefreshFailureReasonNamesTheCause(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"cancelled", &perch.Error{Kind: perch.KindTransient, Message: "request cancelled: context canceled", Err: context.Canceled}, "请求已取消"},
		{"deadline", &perch.Error{Kind: perch.KindTransient, Message: "x", Err: context.DeadlineExceeded}, "请求超时"},
		{"upstream status", &perch.Error{Kind: perch.KindTransient, Status: 502, Message: "bad gateway"}, "HTTP 502 bad gateway"},
		{"status without message", &perch.Error{Kind: perch.KindTransient, Status: 504}, "HTTP 504"},
		{"transport", &perch.Error{Kind: perch.KindTransient, Message: "connection refused"}, "连接失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := autoRefreshFailureReason(tc.err); got != tc.want {
				t.Fatalf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

// The renewal rotates the refresh token before the import reads it, so a
// cancelled request must not surface as a Perch outage.
func TestAutoRefreshTransientMessageCarriesReason(t *testing.T) {
	s := &Server{logs: nil}
	s.perch = &perchRuntime{renew: func(context.Context, perch.Creds, string) (perch.Session, error) {
		return perch.Session{}, &perch.Error{Kind: perch.KindTransient, Status: 503, Message: "service unavailable", Err: errors.New("503")}
	}}
	rec := perchsignupstore.Record{ID: "c1", Candidate: perchsignupstore.Candidate{ProxyURL: "socks5://user:pass@proxy.example:1080"}}
	rec.Session = []byte(`{"account":{"supabase_user_id":"u"},"session":{"access_token":"a","refresh_token":"r","expires_at":1},"supabase_session":{"access_token":"a","refresh_token":"r","expires_at":1,"user":{"id":"u"}},"auth_context":{"supabase_url":"https://project.supabase.co","supabase_anon_key":"k"}}`)
	_, err := s.renewAutoSession(context.Background(), rec)
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("transient failure lost its reason: %v", err)
	}
}

// A failed registration must record WHY, not just that it is uncertain. The
// worker reports one stage label and discards its traceback, so that label is
// the only diagnosis there is; losing it leaves the operator with "check by
// hand" and no idea what to check.
func TestRegisterFailureDetailReachesTheList(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"github stage", &registrationWorkerError{detail: "GitHub 登录或授权阶段失败，请检查账号、代理或上游验证"}, "GitHub 登录或授权阶段失败"},
		{"dependency", &registrationWorkerError{detail: "注册依赖不可用"}, "注册依赖不可用"},
		{"plain process error", errors.New("exit status 1"), "注册进程异常"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{logs: nil}
			s.perch = &perchRuntime{}
			s.perchAuto = &perchAutoManager{settings: perchAutoSettings{IntervalSec: 60, MinAvailable: 1, Group: "perch"}, trigger: make(chan struct{}, 1)}
			st, err := perchsignupstore.Open(filepath.Join(t.TempDir(), "signup.enc"), bytes.Repeat([]byte("k"), 32))
			if err != nil {
				t.Fatal(err)
			}
			s.perchAuto.store = st
			if _, err := st.Import([]perchsignupstore.Candidate{{Login: "user-a", Password: "p", TOTPSecret: "t", MailboxURL: "https://m/"}}); err != nil {
				t.Fatal(err)
			}
			rec, err := st.Claim()
			if err != nil {
				t.Fatal(err)
			}
			s.perchRegistrations.worker = func(context.Context, registrationInput) ([]byte, error) {
				return nil, tc.err
			}
			s.perchAutoRegisterOne(context.Background(), rec)
			got := st.List()[0].Detail
			if !strings.Contains(got, tc.want) {
				t.Fatalf("detail = %q, want it to mention %q", got, tc.want)
			}
			if got == "执行结果不确定，请人工核查" {
				t.Fatal("detail did not carry the reason")
			}
		})
	}
}
