package perchstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/perch"
)

// fakeSupabase is a GoTrue-shaped server. Each user has one live refresh
// token; spending it rotates the answer. It counts calls per user so a test
// can prove two concurrent refreshes of one account spent the token once.
type fakeSupabase struct {
	t     *testing.T
	srv   *httptest.Server
	mu    sync.Mutex
	users map[string]*fakeUser
	// broken makes every refresh answer 503 (a transient failure).
	broken atomic.Bool
	// onBroken counts the 503s served.
	onBroken func()
	// refuse makes every refresh answer 401 invalid_grant (a dead token).
	refuse atomic.Bool
	delay  time.Duration
}

type fakeUser struct {
	id         string
	live       string // the one refresh token that still works
	access     string
	refreshes  atomic.Int64
	issued     int64
	expiresIn  int64
	usageCalls atomic.Int64
}

func newFakeSupabase(t *testing.T) *fakeSupabase {
	f := &fakeSupabase{t: t, users: map[string]*fakeUser{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// seed registers a user with a working refresh token.
func (f *fakeSupabase) seed(userID, email, refresh string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[userID] = &fakeUser{id: userID, live: refresh, access: "access-" + userID + "-0", expiresIn: 3600}
}

func (f *fakeSupabase) refreshCount(userID string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u := f.users[userID]; u != nil {
		return u.refreshes.Load()
	}
	return 0
}

func (f *fakeSupabase) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/rest/v1/rpc/perch_ai_my_inference_usage_meter" {
		f.handleUsage(w, r)
		return
	}
	if r.URL.Path != "/auth/v1/token" || r.URL.Query().Get("grant_type") != "refresh_token" {
		http.NotFound(w, r)
		return
	}
	if got := r.Header.Get("apikey"); got == "" {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"msg":"no apikey"}`))
		return
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.broken.Load() {
		if f.onBroken != nil {
			f.onBroken()
		}
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"msg":"upstream hiccup"}`))
		return
	}
	if f.refuse.Load() {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_code":"refresh_token_already_used","error_description":"Invalid Refresh Token: Already Used"}`))
		return
	}
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.live != body.RefreshToken {
			continue
		}
		u.refreshes.Add(1)
		u.issued++
		// GoTrue rotates: the spent token dies with the answer.
		newRefresh := fmt.Sprintf("refresh-%s-%d", u.id, u.issued)
		u.live = newRefresh
		u.access = fmt.Sprintf("access-%s-%d", u.id, u.issued)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": u.access, "refresh_token": newRefresh, "token_type": "bearer",
			"expires_in": u.expiresIn, "expires_at": time.Now().Add(time.Duration(u.expiresIn) * time.Second).Unix(),
			"user": map[string]any{"id": u.id, "email": u.id + "@example.com", "app_metadata": map[string]any{"provider": "github"}},
		})
		return
	}
	w.WriteHeader(400)
	_, _ = w.Write([]byte(`{"error":"invalid_grant","msg":"Invalid Refresh Token"}`))
}

func (f *fakeSupabase) handleUsage(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	for _, u := range f.users {
		if strings.HasSuffix(strings.TrimPrefix(auth, "Bearer "), u.access) {
			u.usageCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"monthly_usd": 3.25, "monthly_manual_usd": 0.5, "daily_usd": 0.1,
				"daily_calls": 4, "monthly_calls": 90, "monthly_tokens": 12345,
				"period_start": "2026-10-15T00:00:00Z",
			})
			return
		}
	}
	w.WriteHeader(401)
}

func newKeeper(t *testing.T, f *fakeSupabase) *Keeper { return newKeeperCfg(t, f, fastConfig()) }

// fastConfig is DefaultConfig with a backoff short enough to test.
func fastConfig() Config {
	cfg := DefaultConfig()
	cfg.RetryBackoff = time.Millisecond
	return cfg
}

func newKeeperCfg(t *testing.T, f *fakeSupabase, cfg Config) *Keeper {
	t.Helper()
	dir := t.TempDir()
	k, err := Open(filepath.Join(dir, "perch-secrets.json"), []byte("0123456789abcdef0123456789abcdef"), func() Config { return cfg }, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	hc := f.srv.Client()
	hc.Transport = &hostRewrite{t: t, target: f.srv.Listener.Addr().String(), base: http.DefaultTransport}
	k.SetClient(&perch.Client{HTTP: hc})
	return k
}

// hostRewrite sends every request to the fake server whatever host the
// account names, so a test does not need real DNS.
type hostRewrite struct {
	t      *testing.T
	target string
	base   http.RoundTripper
}

func (h *hostRewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.URL.Scheme = "http"
	r2.URL.Host = h.target
	return h.base.RoundTrip(r2)
}

func expiredSession(uid string, fetched time.Time) Secrets {
	return Secrets{
		UserID: uid, Email: uid + "@example.com", SupabaseURL: "https://sb.test",
		AccessToken: "access-" + uid + "-0", RefreshToken: "refresh-" + uid + "-0",
		ExpiresAt: time.Now().Add(-time.Hour).Unix(), FetchedAt: fetched,
	}
}

// ---- the acceptance criteria ---------------------------------------------------

// Two concurrent refreshes of one account must spend its single-use refresh
// token once, and both callers must see the same session.
func TestConcurrentRefreshCollapsesToOneCall(t *testing.T) {
	f := newFakeSupabase(t)
	f.seed("u1", "u1@example.com", "refresh-u1-0")
	f.delay = 30 * time.Millisecond
	k := newKeeper(t, f)
	if _, err := k.Put(expiredSession("u1", time.Now()), PutOptions{BindAs: "acc1", AllowCreate: true}); err != nil {
		t.Fatal(err)
	}

	const n = 8
	var wg sync.WaitGroup
	results := make([]RefreshResult, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = k.Refresh(context.Background(), "acc1", true)
		}(i)
	}
	wg.Wait()

	if got := f.refreshCount("u1"); got != 1 {
		t.Fatalf("refresh token spent %d times, want 1", got)
	}
	sec, _ := k.Get("acc1")
	for i := 0; i < n; i++ {
		if results[i].Err != nil {
			t.Fatalf("caller %d: %v", i, results[i].Err)
		}
		if results[i].Secrets.AccessToken != sec.AccessToken {
			t.Fatalf("caller %d saw access %q, stored %q", i, results[i].Secrets.AccessToken, sec.AccessToken)
		}
	}
	if sec.RefreshToken == "refresh-u1-0" {
		t.Fatal("the rotated refresh token was not stored")
	}
}

// Different accounts refreshing at once must each end up with their own
// tokens — never each other's.
func TestConcurrentRefreshKeepsAccountsApart(t *testing.T) {
	f := newFakeSupabase(t)
	const n = 40
	k := newKeeper(t, f)
	for i := 0; i < n; i++ {
		uid := fmt.Sprintf("user%02d", i)
		f.seed(uid, uid+"@example.com", "refresh-"+uid+"-0")
		if _, err := k.Put(expiredSession(uid, time.Now()), PutOptions{BindAs: "acc" + uid, AllowCreate: true}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			uid := fmt.Sprintf("user%02d", i)
			res := k.Refresh(context.Background(), "acc"+uid, true)
			if res.Err != nil {
				t.Errorf("%s: %v", uid, res.Err)
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		uid := fmt.Sprintf("user%02d", i)
		sec, ok := k.Get("acc" + uid)
		if !ok {
			t.Fatalf("%s: session missing", uid)
		}
		if sec.UserID != uid {
			t.Fatalf("%s: stored session belongs to %s", uid, sec.UserID)
		}
		if !strings.HasPrefix(sec.AccessToken, "access-"+uid+"-") {
			t.Fatalf("%s: got access token %q — another account's token", uid, sec.AccessToken)
		}
		if !strings.HasPrefix(sec.RefreshToken, "refresh-"+uid+"-") {
			t.Fatalf("%s: got refresh token %q — another account's token", uid, sec.RefreshToken)
		}
	}
}

// A refresh token is refused because another client spent it. That needs a
// fresh sign-in and nothing else can produce that verdict.
func TestRefusedRefreshMeansNeedLogin(t *testing.T) {
	f := newFakeSupabase(t)
	f.seed("u1", "u1@example.com", "refresh-u1-0")
	k := newKeeper(t, f)
	if _, err := k.Put(expiredSession("u1", time.Now()), PutOptions{BindAs: "acc1", AllowCreate: true}); err != nil {
		t.Fatal(err)
	}
	before, _ := k.Get("acc1")
	f.refuse.Store(true)
	res := k.Refresh(context.Background(), "acc1", true)
	if !res.NeedLogin {
		t.Fatalf("a refused refresh must mean NeedLogin, got %+v", res)
	}
	if !errors.Is(res.Err, ErrNeedLogin) {
		t.Fatalf("want ErrNeedLogin, got %v", res.Err)
	}
	// a refusal is not retried: it is the definitive answer
	if got := f.refreshCount("u1"); got != 0 {
		t.Fatalf("a 401 should not be retried, saw %d refresh calls", got)
	}
	after, _ := k.Get("acc1")
	if after.AccessToken != before.AccessToken {
		t.Fatal("a refused refresh must leave the session untouched")
	}
}

func TestAmbiguousRefreshRefusalsDoNotNeedLogin(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"invalid api key", 401, `{"msg":"Invalid API key"}`},
		{"proxy forbidden", 403, `<html>Forbidden</html>`},
		{"waf forbidden", 403, `{"error":"access_denied"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			cfg := fastConfig()
			k, err := Open(filepath.Join(t.TempDir(), "perch-secrets.json"), []byte("0123456789abcdef0123456789abcdef"), func() Config { return cfg }, nil)
			if err != nil {
				t.Fatal(err)
			}
			k.SetClient(perch.NewClient(srv.Client()))
			sec := expiredSession("u1", time.Now())
			sec.SupabaseURL = srv.URL
			if _, err := k.Put(sec, PutOptions{BindAs: "acc1", AllowCreate: true}); err != nil {
				t.Fatal(err)
			}
			res := k.Refresh(context.Background(), "acc1", true)
			if res.Err == nil || res.NeedLogin || errors.Is(res.Err, ErrNeedLogin) || perch.KindOf(res.Err) != perch.KindUnknown {
				t.Fatalf("ambiguous refusal: %+v", res)
			}
			after, ok := k.Get("acc1")
			if !ok || after.AccessToken != sec.AccessToken || after.RefreshToken != sec.RefreshToken {
				t.Fatal("ambiguous refusal changed the stored session")
			}
		})
	}
}

// A network blip must never be read as a dead account: it is retried, and
// when the network stays down it is still not NeedLogin.
func TestTransientFailureIsRetriedAndNotFatal(t *testing.T) {
	f := newFakeSupabase(t)
	f.seed("u1", "u1@example.com", "refresh-u1-0")
	cfg := fastConfig()
	cfg.Retries = 2
	k := newKeeperCfg(t, f, cfg)
	if _, err := k.Put(expiredSession("u1", time.Now()), PutOptions{BindAs: "acc1", AllowCreate: true}); err != nil {
		t.Fatal(err)
	}
	f.broken.Store(true)
	var attempts atomic.Int64
	f.onBroken = func() { attempts.Add(1) }
	res := k.Refresh(context.Background(), "acc1", true)
	if res.NeedLogin {
		t.Fatal("a 503 must never mark an account as needing login")
	}
	if res.Err == nil {
		t.Fatal("want an error for a server that keeps failing")
	}
	if len := countRefreshCalls(f, "u1"); len != 0 {
		t.Fatalf("broken server counted %d successful refreshes, want 0", len)
	}
	if perch.KindOf(res.Err) != perch.KindTransient {
		t.Fatalf("want a transient error, got %v (%s)", res.Err, perch.KindOf(res.Err))
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("a 503 should be tried 1+%d times, saw %d", cfg.Retries, got)
	}
	// and once the network recovers, the same token still works
	f.broken.Store(false)
	res = k.Refresh(context.Background(), "acc1", true)
	if res.Err != nil || !res.Renewed {
		t.Fatalf("after recovery: %+v", res)
	}
}

func countRefreshCalls(f *fakeSupabase, uid string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u := f.users[uid]; u != nil {
		return u.refreshes.Load()
	}
	return 0
}

// Re-importing the account's original JSON must not undo a renewal.
func TestOldImportCannotOverwriteRenewedSession(t *testing.T) {
	f := newFakeSupabase(t)
	f.seed("u1", "u1@example.com", "refresh-u1-0")
	k := newKeeper(t, f)
	original := expiredSession("u1", time.Now().Add(-time.Hour))
	if _, err := k.Put(original, PutOptions{BindAs: "acc1", AllowCreate: true}); err != nil {
		t.Fatal(err)
	}
	res := k.Refresh(context.Background(), "acc1", true)
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	renewed, _ := k.Get("acc1")

	again, err := k.Put(original, PutOptions{BindAs: "acc1"})
	if err != nil {
		t.Fatal(err)
	}
	if !again.Skipped {
		t.Fatal("an older import must be skipped, not applied")
	}
	stored, _ := k.Get("acc1")
	if stored.RefreshToken != renewed.RefreshToken || stored.AccessToken != renewed.AccessToken {
		t.Fatal("an old file overwrote the renewed session")
	}
}

// An equal-or-newer file still has to yield to a renewal made after it was
// written: the comparison is on FetchedAt, and a renewal stamps "now".
func TestNewerFileDoesNotBeatNewerRenewal(t *testing.T) {
	f := newFakeSupabase(t)
	f.seed("u1", "u1@example.com", "refresh-u1-0")
	k := newKeeper(t, f)
	fileTime := time.Now().UTC()
	if _, err := k.Put(expiredSession("u1", fileTime), PutOptions{BindAs: "acc1", AllowCreate: true}); err != nil {
		t.Fatal(err)
	}
	if res := k.Refresh(context.Background(), "acc1", true); res.Err != nil {
		t.Fatal(res.Err)
	}
	renewed, _ := k.Get("acc1")
	// the same file, imported again a moment later, carries the same stamp
	if res, _ := k.Put(expiredSession("u1", fileTime), PutOptions{BindAs: "acc1"}); !res.Skipped {
		t.Fatal("a file stamped at or before the renewal must be skipped")
	}
	if now, _ := k.Get("acc1"); now.AccessToken != renewed.AccessToken {
		t.Fatal("the renewal was undone")
	}
	// an explicit force (an admin pasting a session) does replace it
	forced := expiredSession("u1", fileTime)
	forced.AccessToken, forced.RefreshToken = "pasted-access", "pasted-refresh"
	res, err := k.Put(forced, PutOptions{BindAs: "acc1", Force: true})
	if err != nil || !res.Updated {
		t.Fatalf("force should replace: %+v %v", res, err)
	}
	if now, _ := k.Get("acc1"); now.RefreshToken != "pasted-refresh" {
		t.Fatal("a forced update was not stored")
	}
}

// The seal must survive a restart and must not be readable off disk.
func TestSessionsSurviveRestartEncrypted(t *testing.T) {
	f := newFakeSupabase(t)
	f.seed("u1", "u1@example.com", "refresh-u1-0")
	dir := t.TempDir()
	path := filepath.Join(dir, "perch-secrets.json")
	key := []byte("0123456789abcdef0123456789abcdef")
	k, err := Open(path, key, DefaultConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	k.SetClient(&perch.Client{HTTP: &http.Client{Transport: &hostRewrite{target: f.srv.Listener.Addr().String(), base: http.DefaultTransport}}})
	if _, err := k.Put(expiredSession("u1", time.Now()), PutOptions{BindAs: "acc1", AllowCreate: true}); err != nil {
		t.Fatal(err)
	}
	if res := k.Refresh(context.Background(), "acc1", true); res.Err != nil {
		t.Fatal(res.Err)
	}
	want, _ := k.Get("acc1")

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{want.AccessToken, want.RefreshToken, "refresh-u1-0"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("the file holds %q in plain text", secret)
		}
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("file mode %v is too open", info.Mode().Perm())
	}
	// a different key cannot read it
	if k2, err := Open(path, []byte("ffffffffffffffffffffffffffffffff"), DefaultConfig, nil); err == nil && k2.Count() != 0 {
		t.Fatal("another key opened the sealed store")
	}

	// restart with the right key
	k3, err := Open(path, key, DefaultConfig, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := k3.ByUserID("u1")
	if !ok {
		t.Fatal("the session did not survive the restart")
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken {
		t.Fatal("the reopened session differs from the stored one")
	}
	if got.ExpiresAt != want.ExpiresAt {
		t.Fatalf("expiry changed: %d → %d", want.ExpiresAt, got.ExpiresAt)
	}
}

// A due check is the scheduler's only input: 90 s of clock skew must not be
// treated as fresh.
func TestDueHonoursMargin(t *testing.T) {
	now := time.Now()
	s := Secrets{AccessToken: "x", ExpiresAt: now.Add(5 * time.Minute).Unix()}
	if !s.session().Due(now, 10*time.Minute) {
		t.Fatal("5 minutes left with a 10 minute margin is due")
	}
	if s.session().Due(now, time.Minute) {
		t.Fatal("5 minutes left with a 1 minute margin is not due")
	}
	if !(Secrets{AccessToken: "x"}).session().Due(now, time.Minute) {
		t.Fatal("an unknown expiry is due")
	}
}

func TestSummaryNeverCarriesTokens(t *testing.T) {
	s := Secrets{UserID: "u1", AccessToken: "SECRETACCESS", RefreshToken: "SECRETREFRESH",
		ExpiresAt: time.Now().Add(time.Hour).Unix()}
	raw, _ := json.Marshal(Summarize(s))
	for _, bad := range []string{"SECRETACCESS", "SECRETREFRESH"} {
		if strings.Contains(string(raw), bad) {
			t.Fatalf("summary leaks %q: %s", bad, raw)
		}
	}
	if !strings.Contains(string(raw), "SS") {
		t.Fatalf("summary should keep a 2-char suffix for identification: %s", raw)
	}
}
