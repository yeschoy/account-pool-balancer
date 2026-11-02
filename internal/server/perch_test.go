package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/perch"
	"account-pool-balancer/internal/perchstore"
)

// perchFake is a Perch identity + usage server: GoTrue's refresh and user
// endpoints, and the usage-meter RPC.
type perchFake struct {
	srv  *httptest.Server
	mu   sync.Mutex
	live map[string]string // userID → current refresh token
	acc  map[string]string // userID → current access token
	seq  map[string]int
	// refreshes counts calls per user.
	refreshes map[string]int
	// broken answers every refresh with 503.
	broken atomic.Bool
	// refuse answers every refresh with 401.
	refuse atomic.Bool
	// usageCalls counts meter reads.
	usageCalls atomic.Int64
	// models is the body /api/perchai/models answers with, and modelCalls
	// records the query each read used (to prove the tier is passed).
	models     string
	modelCalls atomic.Int64
	modelQuery string
	// account is the body /api/perchai/account answers with ("" = the
	// un-selected-plan default), and starterUSD the free-tier figure
	// /api/perch-terminal/starter-limits reports.
	account      string
	accountCalls atomic.Int64
	starterUSD   float64
	starterCalls atomic.Int64
}

// modelsHits describes the catalogue reads made so far.
func (f *perchFake) modelsHits() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.modelQuery == "" {
		return ""
	}
	return f.modelQuery
}

func newPerchFake(t *testing.T) *perchFake {
	f := &perchFake{live: map[string]string{}, acc: map[string]string{}, seq: map[string]int{}, refreshes: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *perchFake) seed(uid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.live[uid] = "refresh-" + uid + "-0"
	f.acc[uid] = "access-" + uid + "-0"
}

func (f *perchFake) currentAccess(uid string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acc[uid]
}

func (f *perchFake) refreshCount(uid string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refreshes[uid]
}

func (f *perchFake) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/rest/v1/rpc/perch_ai_my_inference_usage_meter":
		f.usageCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"monthly_usd": 4.0, "monthly_manual_usd": 1.0, "daily_usd": 0.5,
			"monthly_calls": 42, "monthly_tokens": 9000,
			"period_start": "2026-10-01T00:00:00Z",
		})
	case r.URL.Path == "/auth/v1/user":
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		for uid, a := range f.acc {
			if a == tok {
				f.mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{"id": uid, "email": uid + "@example.com"})
				return
			}
		}
		f.mu.Unlock()
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"msg":"invalid JWT"}`))
	case r.URL.Path == "/auth/v1/token":
		f.handleRefresh(w, r)
	case r.URL.Path == "/api/perchai/models":
		// Perch's hosted-model catalogue (the web app's picker source).
		f.modelCalls.Add(1)
		f.mu.Lock()
		f.modelQuery = r.URL.RawQuery
		body := f.models
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if body == "" {
			_, _ = w.Write([]byte(`{"ok":true,"tiers":[],"models":[]}`))
			return
		}
		_, _ = w.Write([]byte(body))
	case r.URL.Path == "/models":
		// the app host has no OpenAI listing: remember the relay asked
		f.mu.Lock()
		f.modelQuery = "host-models"
		f.mu.Unlock()
		w.WriteHeader(307)
	case r.URL.Path == "/api/perchai/account":
		// The plan/entitlements/usage route. A fake account starts out as an
		// un-selected plan (what a fresh signup looks like), which is what
		// makes the console say "awaiting plan selection".
		f.accountCalls.Add(1)
		f.mu.Lock()
		body := f.account
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if body == "" {
			_, _ = w.Write([]byte(`{"ok":true,"session":{"planCode":null,"planName":null,` +
				`"membershipRole":null,"tierSelectionRequired":true,"entitlements":[]},` +
				`"usageMeter":null,"creditBalancePt":0}`))
			return
		}
		_, _ = w.Write([]byte(body))
	case r.URL.Path == "/api/perch-terminal/starter-limits":
		f.starterCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		f.mu.Lock()
		usd := f.starterUSD
		f.mu.Unlock()
		if usd == 0 {
			usd = 20
		}
		_, _ = fmt.Fprintf(w, `{"ok":true,"view":"public","config":{"monthlyUsd":%g}}`, usd)
	default:
		http.NotFound(w, r)
	}
}

func (f *perchFake) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if f.broken.Load() {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"msg":"upstream hiccup"}`))
		return
	}
	if f.refuse.Load() {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid Refresh Token: Already Used"}`))
		return
	}
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	for uid, live := range f.live {
		if live != body.RefreshToken {
			continue
		}
		f.refreshes[uid]++
		f.seq[uid]++
		n := f.seq[uid]
		f.live[uid] = fmt.Sprintf("refresh-%s-%d", uid, n)
		f.acc[uid] = fmt.Sprintf("access-%s-%d", uid, n)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": f.acc[uid], "refresh_token": f.live[uid], "token_type": "bearer",
			"expires_in": 3600, "expires_at": time.Now().Add(time.Hour).Unix(),
			"user": map[string]any{"id": uid, "email": uid + "@example.com",
				"app_metadata": map[string]any{"provider": "github"}},
		})
		return
	}
	w.WriteHeader(400)
	_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid Refresh Token"}`))
}

// exportDoc is the account JSON in the documented format, pointed at the fake
// Supabase.
func exportDoc(uid string, fake *perchFake, expiresAt int64, fetchSeq int) string {
	fake.mu.Lock()
	rt := fake.live[uid]
	at := fake.acc[uid]
	fake.mu.Unlock()
	if fetchSeq > 0 {
		rt = fmt.Sprintf("refresh-%s-%d", uid, fetchSeq)
		at = fmt.Sprintf("access-%s-%d", uid, fetchSeq)
		fake.mu.Lock()
		fake.seq[uid] = fetchSeq
		fake.live[uid] = rt
		fake.acc[uid] = at
		fake.mu.Unlock()
	}
	return fmt.Sprintf(`{
  "schema_version": "1.0",
  "provider": "perchai",
  "auth_provider": "github",
  "account": {"supabase_user_id": %q, "email": %q, "github_user_id": null, "registered_at": "2026-11-01T03:00:00Z"},
  "session": {"access_token": %q, "refresh_token": %q, "expires_at": %d},
  "auth_context": {"supabase_url": %q, "workspace_id": null}
}`, uid, uid+"@example.com", at, rt, expiresAt, fake.srv.URL)
}

// perchEnv is a running server whose Perch pool points at a fake Perch.
type perchEnv struct {
	t    *testing.T
	srv  *Server
	fake *perchFake
	dir  string
}

func newPerchEnv(t *testing.T) *perchEnv {
	t.Helper()
	fake := newPerchFake(t)
	dir := t.TempDir()
	s, err := New(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// the keeper's HTTP client is the fake's, whatever host an account names
	hc := fake.srv.Client()
	hc.Transport = &rewriteTransport{target: fake.srv.Listener.Addr().String()}
	s.perch.keeper.SetClient(perchClientFor(hc))
	cfg := config.DefaultPerch()
	cfg.SupabaseKey = "sb_publishable_test"
	cfg.RetryBackoffMs = 100
	cfg.Retries = 1
	// a fresh token from the fake lasts an hour, so the margin has to sit
	// well under that or every renewal would be due again immediately
	cfg.RefreshMarginMin = 5
	if _, err := s.cfg.Update(-1, func(c *config.Config) error {
		c.Perch = cfg
		c.Quota.Enabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for !s.logs.Ready() {
		time.Sleep(5 * time.Millisecond)
	}
	return &perchEnv{t: t, srv: s, fake: fake, dir: dir}
}

// rewriteTransport points every request at the fake server.
type rewriteTransport struct{ target string }

func (r *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c := req.Clone(req.Context())
	c.URL.Scheme = "http"
	c.URL.Host = r.target
	return http.DefaultTransport.RoundTrip(c)
}

// upload builds a multipart body with the named files.
func upload(t *testing.T, files map[string][]byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, body := range files {
		fw, err := mw.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(body)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

// importFiles runs the import handler over the files.
func (e *perchEnv) importFiles(files map[string][]byte) map[string]any {
	e.t.Helper()
	body, ctype := upload(e.t, files)
	req := httptest.NewRequest(http.MethodPost, "/api/perch/import", body)
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	e.srv.apiPerchImport(rec, req)
	if rec.Code != 200 {
		e.t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

// ownAccountID is the id of the only account the env imported.
func (e *perchEnv) ownAccountID() string {
	e.t.Helper()
	_, rows := e.accounts("")
	if len(rows) != 1 {
		e.t.Fatalf("accounts = %d, want 1", len(rows))
	}
	id, _ := rows[0]["id"].(string)
	if id == "" {
		e.t.Fatal("account has no id")
	}
	return id
}

func (e *perchEnv) previewFiles(files map[string][]byte) map[string]any {
	e.t.Helper()
	body, ctype := upload(e.t, files)
	req := httptest.NewRequest(http.MethodPost, "/api/perch/import/preview", body)
	req.Header.Set("Content-Type", ctype)
	rec := httptest.NewRecorder()
	e.srv.apiPerchImportPreview(rec, req)
	if rec.Code != 200 {
		e.t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

// accounts lists the pool through the API.
func (e *perchEnv) accounts(query string) (total int, rows []map[string]any) {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/perch/accounts?"+query, nil)
	rec := httptest.NewRecorder()
	e.srv.apiPerchAccounts(rec, req)
	if rec.Code != 200 {
		e.t.Fatalf("accounts: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Total    int              `json:"total"`
		Accounts []map[string]any `json:"accounts"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out.Total, out.Accounts
}

// ---- acceptance -----------------------------------------------------------------

// 1. One JSON shows up as one account.
func TestPerchImportOneAccount(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("u-one")
	doc := exportDoc("u-one", e.fake, time.Now().Add(time.Hour).Unix(), 0)
	sum := e.importFiles(map[string][]byte{"one.json": []byte(doc)})
	s := sum["summary"].(map[string]any)
	if s["created"].(float64) != 1 {
		t.Fatalf("created = %v, want 1", s["created"])
	}
	total, rows := e.accounts("")
	if total != 1 {
		t.Fatalf("total = %d", total)
	}
	row := rows[0]
	if row["userId"] != "u-one" || row["email"] != "u-one@example.com" {
		t.Fatalf("row = %+v", row)
	}
	if row["status"] != "normal" {
		t.Fatalf("a freshly imported live session should be normal, got %v (%v)", row["status"], row["statusDetail"])
	}
	// the JSON's own token must be what is stored
	sec, ok := e.srv.perch.keeper.Get(row["id"].(string))
	if !ok {
		t.Fatal("session not stored")
	}
	if sec.RefreshToken != "refresh-u-one-0" {
		t.Fatalf("stored refresh token = %q", sec.RefreshToken)
	}
}

// 2/3. A hundred accounts are managed independently, and the list pages.
func TestPerchImportHundredAccounts(t *testing.T) {
	e := newPerchEnv(t)
	files := map[string][]byte{}
	for i := 0; i < 100; i++ {
		uid := fmt.Sprintf("user-%03d", i)
		e.fake.seed(uid)
		files[fmt.Sprintf("%s.json", uid)] = []byte(exportDoc(uid, e.fake, time.Now().Add(time.Hour).Unix(), 0))
	}
	sum := e.importFiles(files)
	if got := sum["summary"].(map[string]any)["created"].(float64); got != 100 {
		t.Fatalf("created = %v, want 100", got)
	}
	total, rows := e.accounts("size=50")
	if total != 100 || len(rows) != 50 {
		t.Fatalf("total %d page %d", total, len(rows))
	}
	// every account has its own session and none is another's
	seen := map[string]bool{}
	for _, r := range rows {
		id := r["id"].(string)
		sec, ok := e.srv.perch.keeper.Get(id)
		if !ok {
			t.Fatalf("%s has no session", id)
		}
		if sec.UserID != r["userId"] {
			t.Fatalf("%s: session belongs to %s", id, sec.UserID)
		}
		if seen[sec.AccessToken] {
			t.Fatal("two accounts share an access token")
		}
		seen[sec.AccessToken] = true
	}
	// searching by email narrows it
	total, rows = e.accounts("q=user-042")
	if total != 1 || rows[0]["userId"] != "user-042" {
		t.Fatalf("search: total %d rows %v", total, rows)
	}
}

// 4/5. An expired access token is renewed and the rotated refresh token kept.
func TestPerchAutoRenewKeepsRotatedToken(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("u-rot")
	// the file says the token already ended: it is due the moment it lands
	doc := exportDoc("u-rot", e.fake, time.Now().Add(-time.Minute).Unix(), 0)
	e.importFiles(map[string][]byte{"a.json": []byte(doc)})
	_, rows := e.accounts("")
	id := rows[0]["id"].(string)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res := e.srv.perch.keeper.Refresh(ctx, id, false)
	if res.Err != nil || !res.Renewed {
		t.Fatalf("refresh: %+v", res)
	}
	if e.fake.refreshCount("u-rot") != 1 {
		t.Fatalf("refresh calls = %d", e.fake.refreshCount("u-rot"))
	}
	sec, _ := e.srv.perch.keeper.Get(id)
	if sec.RefreshToken != "refresh-u-rot-1" {
		t.Fatalf("rotated refresh token not stored: %q", sec.RefreshToken)
	}
	if sec.AccessToken != "access-u-rot-1" {
		t.Fatalf("new access token not stored: %q", sec.AccessToken)
	}
	// and the expiry moved forward with it
	if !time.Now().Add(50 * time.Minute).Before(time.Unix(sec.ExpiresAt, 0)) {
		t.Fatalf("expiry not updated: %v", time.Unix(sec.ExpiresAt, 0))
	}
	// it is no longer due, so a second pass does nothing
	again := e.srv.perch.keeper.Refresh(ctx, id, false)
	if !again.Skipped || again.Renewed {
		t.Fatalf("a fresh session should not refresh again: %+v", again)
	}
	if e.fake.refreshCount("u-rot") != 1 {
		t.Fatal("the token was spent twice")
	}
}

// 6. Concurrent refreshes of one account spend its token once.
func TestPerchConcurrentRefreshSameAccount(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("u-race")
	doc := exportDoc("u-race", e.fake, time.Now().Add(-time.Minute).Unix(), 0)
	e.importFiles(map[string][]byte{"a.json": []byte(doc)})
	_, rows := e.accounts("")
	id := rows[0]["id"].(string)

	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res := e.srv.perch.keeper.Refresh(context.Background(), id, true)
			errs[i] = res.Err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	if got := e.fake.refreshCount("u-race"); got != 1 {
		t.Fatalf("the refresh token was spent %d times", got)
	}
}

// 7. Many accounts refreshing at once never swap tokens.
func TestPerchConcurrentRefreshDifferentAccounts(t *testing.T) {
	e := newPerchEnv(t)
	files := map[string][]byte{}
	const n = 30
	for i := 0; i < n; i++ {
		uid := fmt.Sprintf("mix-%02d", i)
		e.fake.seed(uid)
		files[fmt.Sprintf("%s.json", uid)] = []byte(exportDoc(uid, e.fake, time.Now().Add(-time.Minute).Unix(), 0))
	}
	e.importFiles(files)
	_, rows := e.accounts("size=500")
	var wg sync.WaitGroup
	for _, row := range rows {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			res := e.srv.perch.keeper.Refresh(context.Background(), id, true)
			if res.Err != nil {
				t.Errorf("%s: %v", id, res.Err)
			}
		}(row["id"].(string))
	}
	wg.Wait()
	for _, row := range rows {
		uid := row["userId"].(string)
		sec, _ := e.srv.perch.keeper.Get(row["id"].(string))
		if sec.UserID != uid {
			t.Fatalf("%v: stores another account's session (%s)", row["id"], sec.UserID)
		}
		if !strings.HasPrefix(sec.RefreshToken, "refresh-"+uid+"-") {
			t.Fatalf("%s: refresh token %q belongs to someone else", uid, sec.RefreshToken)
		}
		if sec.AccessToken != e.fake.currentAccess(uid) {
			t.Fatalf("%s: stored access %q is not the one Perch issued (%q)", uid, sec.AccessToken, e.fake.currentAccess(uid))
		}
	}
}

// 8. Re-importing the original file cannot undo a renewal.
func TestPerchReimportCannotOverwriteRenewal(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("u-old")
	original := exportDoc("u-old", e.fake, time.Now().Add(-time.Minute).Unix(), 0)
	e.importFiles(map[string][]byte{"a.json": []byte(original)})
	_, rows := e.accounts("")
	id := rows[0]["id"].(string)
	if res := e.srv.perch.keeper.Refresh(context.Background(), id, true); res.Err != nil {
		t.Fatal(res.Err)
	}
	renewed, _ := e.srv.perch.keeper.Get(id)

	sum := e.importFiles(map[string][]byte{"a.json": []byte(original)})
	s := sum["summary"].(map[string]any)
	if s["created"].(float64) != 0 {
		t.Fatal("re-import must not create a second account")
	}
	if s["duplicate"].(float64)+s["skipped"].(float64) < 1 {
		t.Fatalf("re-import should be recognised as a duplicate/skip: %v", s)
	}
	now, _ := e.srv.perch.keeper.Get(id)
	if now.RefreshToken != renewed.RefreshToken || now.AccessToken != renewed.AccessToken {
		t.Fatal("an old JSON overwrote the renewed session")
	}
	// and the pool still has exactly one account
	if total, _ := e.accounts(""); total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}
}

// 9/10. A refused refresh is identified accurately, and only then.
func TestPerchNeedLoginDetected(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("u-dead")
	doc := exportDoc("u-dead", e.fake, time.Now().Add(-time.Minute).Unix(), 0)
	e.importFiles(map[string][]byte{"a.json": []byte(doc)})
	_, rows := e.accounts("")
	id := rows[0]["id"].(string)

	// a network failure first: it must NOT mark the account dead
	e.fake.broken.Store(true)
	res := e.srv.perch.keeper.Refresh(context.Background(), id, true)
	if res.NeedLogin {
		t.Fatal("a 503 was read as a dead account")
	}
	e.fake.broken.Store(false)

	e.fake.refuse.Store(true)
	res = e.srv.perch.keeper.Refresh(context.Background(), id, true)
	if !res.NeedLogin {
		t.Fatalf("a refused refresh token must need login: %+v", res)
	}
	e.srv.onPerchRefresh(id, res)

	a, _ := e.srv.cfg.Get().Account(id)
	if !a.PerchRefreshFailed {
		t.Fatal("the account was not marked")
	}
	// the console says so, and filters find it
	_, abnormal := e.accounts("status=login-expired")
	if len(abnormal) != 1 {
		t.Fatalf("login-expired filter returned %d", len(abnormal))
	}
	if abnormal[0]["status"] != "login-expired" {
		t.Fatalf("status = %v", abnormal[0]["status"])
	}
	// a login-expired account is not offered as usable
	req := httptest.NewRequest(http.MethodGet, "/api/perch/available", nil)
	rec := httptest.NewRecorder()
	e.srv.apiPerchAvailable(rec, req)
	var avail struct {
		Accounts []map[string]any `json:"accounts"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &avail)
	if len(avail.Accounts) != 0 {
		t.Fatalf("a login-expired account was offered as available: %v", avail.Accounts)
	}
	// a fresh sign-in clears it
	e.fake.refuse.Store(false)
	fresh := exportDoc("u-dead", e.fake, time.Now().Add(time.Hour).Unix(), 0)
	body := map[string]any{"json": fresh}
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPut, "/api/perch/accounts/"+id+"/session", bytes.NewReader(raw))
	r.SetPathValue("id", id)
	rec = httptest.NewRecorder()
	e.srv.apiPerchUpdateSession(rec, r)
	if rec.Code != 200 {
		t.Fatalf("update session: %d %s", rec.Code, rec.Body.String())
	}
	a, _ = e.srv.cfg.Get().Account(id)
	if a.PerchRefreshFailed {
		t.Fatal("a fresh session did not clear the failure flag")
	}
}

func TestPerchStaleRefreshResultDoesNotQuarantineNewSession(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("u-stale")
	e.importFiles(map[string][]byte{"a.json": []byte(exportDoc("u-stale", e.fake, time.Now().Add(-time.Minute).Unix(), 0))})
	_, rows := e.accounts("")
	id := rows[0]["id"].(string)
	e.fake.refuse.Store(true)
	res := e.srv.perch.keeper.Refresh(context.Background(), id, true)
	if !res.NeedLogin || res.Revision == 0 {
		t.Fatalf("expected current refusal, got %+v", res)
	}
	fresh := res.Secrets
	fresh.AccessToken = "new-access"
	fresh.RefreshToken = "new-refresh"
	if _, err := e.srv.perch.keeper.Put(fresh, perchstore.PutOptions{BindAs: id, Force: true}); err != nil {
		t.Fatal(err)
	}
	e.srv.onPerchRefresh(id, res)
	a, _ := e.srv.cfg.Get().Account(id)
	if a.PerchRefreshFailed {
		t.Fatal("stale refusal marked the newer session invalid")
	}
}

// 11. The usage meter is shown, and a cap is never invented.
func TestPerchUsageMeter(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("u-use")
	e.importFiles(map[string][]byte{"a.json": []byte(exportDoc("u-use", e.fake, time.Now().Add(time.Hour).Unix(), 0))})
	_, rows := e.accounts("")
	id := rows[0]["id"].(string)
	a, _ := e.srv.cfg.Get().Account(id)
	if err := e.srv.quota.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	_, rows = e.accounts("")
	q, ok := rows[0]["quota"].(map[string]any)
	if !ok {
		t.Fatalf("no quota in the view: %+v", rows[0])
	}
	// monthly_usd 4.00 with 1.00 manual → 3000 PT used, 1000 PT manual
	if got := q["usedPt"].(float64); got != 3000 {
		t.Fatalf("usedPt = %v, want 3000", got)
	}
	if got := q["monthlyCalls"].(float64); got != 42 {
		t.Fatalf("monthlyCalls = %v", got)
	}
	// Perch states no cap, so remaining is unknown — never a Starter default
	if rows[0]["remainingKnown"].(bool) {
		t.Fatal("a cap was invented where Perch stated none")
	}
	if rows[0]["remainingPt"].(float64) != 0 {
		t.Fatal("remaining should be 0 when unknown")
	}
	if e.fake.usageCalls.Load() == 0 {
		t.Fatal("the meter was never read")
	}
}

// The pool's status words cover the states the requirements name.
func TestPerchStatusVocabulary(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("u-s")
	e.importFiles(map[string][]byte{"a.json": []byte(exportDoc("u-s", e.fake, time.Now().Add(time.Hour).Unix(), 0))})
	_, rows := e.accounts("")
	id := rows[0]["id"].(string)
	if rows[0]["status"] != "normal" {
		t.Fatalf("status = %v", rows[0]["status"])
	}
	// disable
	raw, _ := json.Marshal(map[string]any{"enabled": false})
	r := httptest.NewRequest(http.MethodPut, "/api/perch/accounts/"+id, bytes.NewReader(raw))
	r.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	e.srv.apiPerchUpdateAccount(rec, r)
	if rec.Code != 200 {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}
	_, rows = e.accounts("")
	if rows[0]["status"] != "disabled" {
		t.Fatalf("status after disable = %v", rows[0]["status"])
	}
	_, disabled := e.accounts("status=disabled")
	if len(disabled) != 1 {
		t.Fatal("the disabled filter missed it")
	}
}

// 12. A batch never stops because one account fails.
func TestPerchBatchIsolatesFailures(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("good")
	e.importFiles(map[string][]byte{"a.json": []byte(exportDoc("good", e.fake, time.Now().Add(time.Hour).Unix(), 0))})
	_, rows := e.accounts("")
	id := rows[0]["id"].(string)

	raw, _ := json.Marshal(map[string]any{"action": "refresh", "ids": []string{id, "does-not-exist"}})
	r := httptest.NewRequest(http.MethodPost, "/api/perch/batch", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	e.srv.apiPerchBatch(rec, r)
	if rec.Code != 200 {
		t.Fatalf("batch: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		OK      int `json:"ok"`
		Failed  int `json:"failed"`
		Results []struct {
			ID     string `json:"id"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		} `json:"results"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Results) != 2 {
		t.Fatalf("results = %d, want one per id", len(out.Results))
	}
	// the existing account succeeded, the missing one failed — and the
	// failure did not abort the task
	var goodOK, missingOK bool
	for _, res := range out.Results {
		if res.ID == id {
			goodOK = res.OK
		} else {
			missingOK = res.OK
		}
	}
	if !goodOK || missingOK {
		t.Fatalf("batch results = %+v", out.Results)
	}
}

// 13. A restart keeps accounts, credentials and status.
func TestPerchSurvivesRestart(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("u-persist")
	e.importFiles(map[string][]byte{"a.json": []byte(exportDoc("u-persist", e.fake, time.Now().Add(time.Hour).Unix(), 0))})
	_, rows := e.accounts("")
	id := rows[0]["id"].(string)
	before, _ := e.srv.perch.keeper.Get(id)
	if err := e.srv.perch.keeper.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.saveMetadata(true); err != nil {
		t.Fatal(err)
	}

	// the data directory's sessions file must not hold a token in the clear
	raw, err := os.ReadFile(filepath.Join(e.dir, "perch-secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{before.AccessToken, before.RefreshToken} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("perch-secrets.json holds %q in plain text", secret)
		}
	}
	cfgRaw, _ := os.ReadFile(filepath.Join(e.dir, "config.json"))
	if strings.Contains(string(cfgRaw), before.RefreshToken) {
		t.Fatal("config.json holds a refresh token")
	}

	// reopen the same directory
	s2, err := New(e.dir, Options{})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	after, ok := s2.perch.keeper.Get(id)
	if !ok {
		t.Fatal("the session did not survive the restart")
	}
	if after.AccessToken != before.AccessToken || after.RefreshToken != before.RefreshToken {
		t.Fatal("the reopened session differs")
	}
	if _, ok := s2.cfg.Get().Account(id); !ok {
		t.Fatal("the account did not survive the restart")
	}
}

// Preview reports what would happen without writing anything.
func TestPerchImportPreviewWritesNothing(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("p1")
	e.fake.seed("p2")
	files := map[string][]byte{
		"a.json":   []byte(exportDoc("p1", e.fake, time.Now().Add(time.Hour).Unix(), 0)),
		"b.json":   []byte(exportDoc("p1", e.fake, time.Now().Add(time.Hour).Unix(), 0)), // duplicate
		"c.json":   []byte(exportDoc("p2", e.fake, time.Now().Add(time.Hour).Unix(), 0)),
		"bad.json": []byte(`{"account":{}}`),
	}
	out := e.previewFiles(files)
	items := out["items"].([]any)
	if len(items) != 4 {
		t.Fatalf("items = %d", len(items))
	}
	var okN, dupN, badN int
	for _, raw := range items {
		it := raw.(map[string]any)
		switch {
		case !it["ok"].(bool):
			badN++
		case it["duplicateOf"] == "a.json":
			dupN++
		default:
			okN++
		}
	}
	if okN != 2 || dupN != 1 || badN != 1 {
		t.Fatalf("preview: ok %d dup %d bad %d", okN, dupN, badN)
	}
	if total, _ := e.accounts(""); total != 0 {
		t.Fatal("preview wrote accounts")
	}
}

// No API ever returns a token.
func TestPerchAPIsNeverLeakTokens(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("u-secret")
	e.importFiles(map[string][]byte{"a.json": []byte(exportDoc("u-secret", e.fake, time.Now().Add(time.Hour).Unix(), 0))})
	_, rows := e.accounts("")
	id := rows[0]["id"].(string)
	sec, _ := e.srv.perch.keeper.Get(id)

	collect := func(path string, h func(http.ResponseWriter, *http.Request)) string {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		h(rec, r)
		return rec.Body.String()
	}
	bodies := []string{
		collect("/api/perch/accounts", e.srv.apiPerchAccounts),
		collect("/api/perch/accounts/"+id, e.srv.apiPerchAccount),
		collect("/api/perch/overview", e.srv.apiPerchOverview),
		collect("/api/perch/export", e.srv.apiPerchExport),
		collect("/api/perch/settings", e.srv.apiPerchSettings),
	}
	for _, bad := range []string{sec.AccessToken, sec.RefreshToken} {
		for i, b := range bodies {
			if strings.Contains(b, bad) {
				t.Fatalf("response %d leaks a token: %s", i, b)
			}
		}
	}
	for _, kind := range []string{"perch"} {
		page, err := e.srv.logs.Query(kind, nil, time.Time{}, time.Time{}, 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range page.Items {
			for _, bad := range []string{sec.AccessToken, sec.RefreshToken} {
				if strings.Contains(string(raw), bad) {
					t.Fatalf("the event log holds a token: %s", raw)
				}
			}
		}
	}
}

// The Perch pool relays OpenAI Chat Completions and Responses (translated to
// Perch's envelope and back) and nothing else: embeddings, completions and the
// Anthropic Messages shape have no Perch counterpart and are refused locally.
func TestPerchPoolProxiesChatAndResponsesOnly(t *testing.T) {
	// /v1/models too: Perch has no OpenAI listing, but this service answers
	// that path from the catalogue it synced, so it must not be refused here
	for _, p := range []string{"/v1/chat/completions", "/v1/responses", "/v1/models"} {
		if !poolPathOK(config.ProviderPerch, p) {
			t.Fatalf("the perch relay must serve %s", p)
		}
	}
	for _, p := range []string{"/v1/embeddings", "/v1/completions", "/v1/messages"} {
		if poolPathOK(config.ProviderPerch, p) {
			t.Fatalf("the perch relay must refuse %s locally", p)
		}
	}
}

// Group counts and the available list honour status.
func TestPerchGroupsAndAvailable(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("g1")
	e.fake.seed("g2")
	e.importFiles(map[string][]byte{
		"a.json": []byte(exportDoc("g1", e.fake, time.Now().Add(time.Hour).Unix(), 0)),
		"b.json": []byte(exportDoc("g2", e.fake, time.Now().Add(time.Hour).Unix(), 0)),
	})
	// move one to a second group
	ids := []string{}
	for _, a := range e.srv.cfg.Get().Accounts {
		ids = append(ids, a.ID)
	}
	raw, _ := json.Marshal(map[string]any{"action": "group", "ids": ids[:1], "group": "team-b"})
	r := httptest.NewRequest(http.MethodPost, "/api/perch/batch", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	e.srv.apiPerchBatch(rec, r)
	if rec.Code != 200 {
		t.Fatalf("group batch: %d %s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/perch/groups", nil)
	rec = httptest.NewRecorder()
	e.srv.apiPerchGroups(rec, req)
	var out struct {
		Groups []struct {
			Name   string `json:"name"`
			Total  int    `json:"total"`
			Normal int    `json:"normal"`
		} `json:"groups"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Groups) != 2 {
		t.Fatalf("groups = %+v", out.Groups)
	}
	for _, g := range out.Groups {
		if g.Total != 1 || g.Normal != 1 {
			t.Fatalf("group %+v", g)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/api/perch/available", nil)
	rec = httptest.NewRecorder()
	e.srv.apiPerchAvailable(rec, req)
	var avail struct {
		Accounts []map[string]any `json:"accounts"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &avail)
	if len(avail.Accounts) != 2 {
		t.Fatalf("available = %d, want 2", len(avail.Accounts))
	}
	// narrowing by group
	req = httptest.NewRequest(http.MethodGet, "/api/perch/available?group=team-b", nil)
	rec = httptest.NewRecorder()
	e.srv.apiPerchAvailable(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &avail)
	if len(avail.Accounts) != 1 {
		t.Fatalf("group filter returned %d", len(avail.Accounts))
	}
}

// An encrypted backup restores the pool.
func TestPerchBackupRestore(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("bk1")
	e.importFiles(map[string][]byte{"a.json": []byte(exportDoc("bk1", e.fake, time.Now().Add(time.Hour).Unix(), 0))})
	_, rows := e.accounts("")
	id := rows[0]["id"].(string)
	want, _ := e.srv.perch.keeper.Get(id)

	env, err := e.srv.backupPerch("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(env)
	if strings.Contains(string(raw), want.RefreshToken) {
		t.Fatal("the backup holds a refresh token in the clear")
	}
	// the wrong passphrase is refused
	if _, _, _, _, err := e.srv.restorePerch("wrong passphrase", raw); err == nil {
		t.Fatal("a wrong passphrase opened the backup")
	}
	// deleting and restoring brings the session back
	ids := []string{id}
	var results []perchBatchResult
	e.srv.batchDeletePerch(ids, &results)
	if total, _ := e.accounts(""); total != 0 {
		t.Fatal("delete left the account")
	}
	created, _, _, errs, err := e.srv.restorePerch("correct horse battery", raw)
	if err != nil || created != 1 {
		t.Fatalf("restore: created %d errs %v err %v", created, errs, err)
	}
	got, ok := e.srv.perch.keeper.ByUserID("bk1")
	if !ok || got.RefreshToken != want.RefreshToken {
		t.Fatalf("restored session differs: %+v", got)
	}
}

// The settings API round-trips and validates.
func TestPerchSettingsRoundTrip(t *testing.T) {
	e := newPerchEnv(t)
	p := config.DefaultPerch()
	p.RefreshMarginMin = 25
	p.Retries = 4
	p.QuotaIntervalSec = 900
	raw, _ := json.Marshal(map[string]any{"perch": p})
	req := httptest.NewRequest(http.MethodPut, "/api/perch/settings", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	e.srv.apiPerchSaveSettings(rec, req)
	if rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	got := e.srv.cfg.Get().Perch
	if got.RefreshMarginMin != 25 || got.Retries != 4 || got.QuotaIntervalSec != 900 {
		t.Fatalf("settings = %+v", got)
	}
	// an out-of-range value is refused
	p.RefreshMarginMin = 99999
	raw, _ = json.Marshal(map[string]any{"perch": p})
	req = httptest.NewRequest(http.MethodPut, "/api/perch/settings", bytes.NewReader(raw))
	rec = httptest.NewRecorder()
	e.srv.apiPerchSaveSettings(rec, req)
	if rec.Code == 200 {
		t.Fatal("an out-of-range margin was accepted")
	}
}

// The dashboard totals what the pool holds.
func TestPerchOverview(t *testing.T) {
	e := newPerchEnv(t)
	for i := 0; i < 3; i++ {
		uid := fmt.Sprintf("ov%d", i)
		e.fake.seed(uid)
		e.importFiles(map[string][]byte{uid + ".json": []byte(exportDoc(uid, e.fake, time.Now().Add(time.Hour).Unix(), 0))})
	}
	req := httptest.NewRequest(http.MethodGet, "/api/perch/overview", nil)
	rec := httptest.NewRecorder()
	e.srv.apiPerchOverview(rec, req)
	if rec.Code != 200 {
		t.Fatalf("overview: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Total  int            `json:"total"`
		Counts map[string]int `json:"counts"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Total != 3 || out.Counts["normal"] != 3 {
		t.Fatalf("overview = %+v", out)
	}
}

// A ZIP upload imports every member.
func TestPerchZipUpload(t *testing.T) {
	e := newPerchEnv(t)
	var zbuf bytes.Buffer
	zw := newZip(&zbuf)
	for i := 0; i < 5; i++ {
		uid := fmt.Sprintf("zip%d", i)
		e.fake.seed(uid)
		zw.add(t, uid+".json", exportDoc(uid, e.fake, time.Now().Add(time.Hour).Unix(), 0))
	}
	zw.close(t)
	sum := e.importFiles(map[string][]byte{"pack.zip": zbuf.Bytes()})
	if got := sum["summary"].(map[string]any)["created"].(float64); got != 5 {
		t.Fatalf("zip created %v, want 5", got)
	}
}

// ---- small helpers ----------------------------------------------------------------

// testZip builds an archive in memory for the ZIP-upload test.
type testZip struct {
	buf *bytes.Buffer
	zw  *zip.Writer
}

func newZip(buf *bytes.Buffer) *testZip { return &testZip{buf: buf, zw: zip.NewWriter(buf)} }

func (z *testZip) add(t *testing.T, name, body string) {
	t.Helper()
	w, err := z.zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
}

func (z *testZip) close(t *testing.T) {
	t.Helper()
	if err := z.zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// perchClientFor wraps a test HTTP client in perch's client.
func perchClientFor(hc *http.Client) *perch.Client { return &perch.Client{HTTP: hc} }

// The console used to hide the key pages behind the proxy pools' tab, so a
// Perch-only setup had no way to mint the sk-odb- the downstream needs, or the
// sk-odbimp- the signup bot authenticates /open/v1/perch/import with. Both are
// ordinary keys of the `perch` group: create them from the Perch pool and they
// must come back listed under provider=perch, and nowhere else.
func TestPerchPoolOwnsItsKeys(t *testing.T) {
	e := newPerchEnv(t)

	newKey := func(path string) string {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"name": "from-perch", "group": "perch", "provider": "perch"})
		rec := httptest.NewRecorder()
		if path == "/api/client-keys" {
			e.srv.apiCreateClientKey(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw)))
		} else {
			e.srv.apiCreateImportKey(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw)))
		}
		if rec.Code != 200 {
			t.Fatalf("create %s: %d %s", path, rec.Code, rec.Body.String())
		}
		var out struct{ Key string }
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if out.Key == "" {
			t.Fatalf("%s: no key returned", path)
		}
		return out.Key
	}
	newKey("/api/client-keys")
	newKey("/api/import-keys")

	// the keys carry the pool, not just the group: config settles it on save
	cfg := e.srv.cfg.Get()
	if len(cfg.ClientKeys) != 1 || cfg.ClientKeys[0].Provider != config.ProviderPerch {
		t.Fatalf("client keys = %+v", cfg.ClientKeys)
	}
	if len(cfg.ImportKeys) != 1 || cfg.ImportKeys[0].Provider != config.ProviderPerch {
		t.Fatalf("import keys = %+v", cfg.ImportKeys)
	}

	list := func(path string) []any {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path+"?provider=perch", nil)
		rec := httptest.NewRecorder()
		if path == "/api/client-keys" {
			e.srv.apiClientKeys(rec, r)
		} else {
			e.srv.apiImportKeys(rec, r)
		}
		var out struct{ Keys []any }
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Keys
	}
	if n := len(list("/api/client-keys")); n != 1 {
		t.Fatalf("provider=perch listed %d client keys, want 1", n)
	}
	if n := len(list("/api/import-keys")); n != 1 {
		t.Fatalf("provider=perch listed %d import keys, want 1", n)
	}
	// and they stay out of the proxy pools' lists
	for _, p := range []string{"opendesign", "river", "ollama", "modal"} {
		r := httptest.NewRequest(http.MethodGet, "/api/client-keys?provider="+p, nil)
		rec := httptest.NewRecorder()
		e.srv.apiClientKeys(rec, r)
		var out struct{ Keys []any }
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if len(out.Keys) != 0 {
			t.Fatalf("a perch key showed up in the %s pool", p)
		}
	}
}

// GET /v1/models on the Perch pool used to answer 502: the relay had no
// /v1/models path (so a local 404) and fetchModels asked the Perch app host
// for /models, which is a 307 to the web app. Perch's catalogue lives at
// /api/perchai/models, read by the quota poller; the endpoint must publish
// that instead of reading the host.
func TestPerchModelsPathServesSyncedCatalog(t *testing.T) {
	e := newPerchEnv(t)
	const uid = "u-model"
	e.fake.seed(uid)
	// the catalogue the fake Perch answers for /api/perchai/models
	e.fake.models = `{"ok":true,"tiers":[{"id":"standard","locked":false}],
		"models":[{"id":"minimax-m3","label":"MiniMax M3","contextTokens":262000,"locked":true,"requires":"pro"},
		          {"id":"kimi-3","label":"Kimi K3","contextTokens":1000000,"locked":true,"requires":"pro"}]}`

	e.importFiles(map[string][]byte{"a.json": []byte(exportDoc(uid, e.fake, time.Now().Add(time.Hour).Unix(), 0))})
	_, rows := e.accounts("")
	if len(rows) != 1 {
		t.Fatalf("accounts = %v", rows)
	}
	id := rows[0]["id"].(string)
	a, _ := e.srv.cfg.Get().Account(id)

	// a poll fills the account's catalogue from /api/perchai/models
	if err := e.srv.quota.RefreshNow(context.Background(), a); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if got := e.srv.quota.View(id).PublicModels(time.Now()); len(got) != 2 {
		t.Fatalf("synced catalogue = %v, want 2 models", got)
	}
	if !strings.Contains(e.fake.modelsHits(), "tier=") {
		t.Fatalf("tier was not passed to /api/perchai/models: %q", e.fake.modelsHits())
	}

	// and GET /v1/models publishes it rather than failing
	body := e.srv.fetchModels(context.Background(), e.srv.cfg.Get(), "perch")
	if body == nil {
		t.Fatal("the perch pool published no catalogue (the 502 this guards against)")
	}
	var doc struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("catalogue is not JSON: %v", err)
	}
	got := map[string]bool{}
	for _, m := range doc.Data {
		got[m.ID] = true
	}
	for _, want := range []string{"minimax-m3", "kimi-3"} {
		if !got[want] {
			t.Fatalf("catalogue %v is missing %s", got, want)
		}
	}
	// the app host's /models must never be asked
	if strings.Contains(e.fake.modelsHits(), "host-models") {
		t.Fatal("the relay asked the Perch app host for /models")
	}
}

// A freshly signed-up account has picked no plan: Perch states no plan code
// and NO entitlements, so there is nothing to measure. The console must say so
// in every place a cap or a plan name would otherwise appear — "未知" there
// reads like a failed read, when the account simply has not been taken into
// use yet. This is the state every batch-registered account starts in.
func TestPerchTierPendingIsNamedNotUnknown(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("u-tier")
	e.importFiles(map[string][]byte{"a.json": []byte(exportDoc("u-tier", e.fake, time.Now().Add(time.Hour).Unix(), 0))})
	id := e.ownAccountID()

	a, _ := e.srv.cfg.Get().Account(id)
	if err := e.srv.quota.RefreshNow(context.Background(), a); err != nil {
		t.Fatalf("meter poll: %v", err)
	}

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/perch/accounts/"+id, nil)
	r.SetPathValue("id", id)
	e.srv.apiPerchAccount(rec, r)
	if rec.Code != 200 {
		t.Fatalf("account view: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Account map[string]any `json:"account"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	acc := out.Account
	if acc["tierSelectionRequired"] != true {
		t.Fatalf("tierSelectionRequired = %v, want true for an un-selected plan", acc["tierSelectionRequired"])
	}
	if acc["status"] != "tier-pending" {
		t.Fatalf("status = %v, want tier-pending", acc["status"])
	}
	// no cap is invented: Perch stated none, so the view must not carry one
	if v, _ := acc["remainingKnown"].(bool); v {
		t.Fatal("an un-selected plan must not report a known remaining allowance")
	}
	if acc["allowanceSource"] != string(perch.AllowanceNone) && acc["allowanceSource"] != "" {
		t.Fatalf("allowanceSource = %v, want none", acc["allowanceSource"])
	}
	// Perch's own free-tier figure was read (so the console can label an
	// estimate for accounts that DID pick Starter) — but it is not applied as
	// this account's cap
	if e.fake.starterCalls.Load() == 0 {
		t.Fatal("starter-limits was never read")
	}
	if acc["plan"] != nil && acc["plan"] != "" {
		t.Fatalf("plan = %v, want empty for an un-selected plan", acc["plan"])
	}
}

// Once the owner picks Starter, the same route reports the stated cap and the
// flag clears.
func TestPerchTierSelectedReportsCap(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("u-picked")
	e.fake.account = `{"ok":true,"session":{"planCode":"pilot","planName":"Pilot",` +
		`"membershipRole":"member","tierSelectionRequired":false,` +
		`"entitlements":[{"key":"usage.monthly_pt","value_json":{"limit":20000}}]},` +
		`"usageMeter":{"monthlyUsd":3,"dailyUsd":1,"monthlyCalls":7},"creditBalancePt":0}`
	e.importFiles(map[string][]byte{"a.json": []byte(exportDoc("u-picked", e.fake, time.Now().Add(time.Hour).Unix(), 0))})
	id := e.ownAccountID()

	a, _ := e.srv.cfg.Get().Account(id)
	if err := e.srv.quota.RefreshNow(context.Background(), a); err != nil {
		t.Fatalf("meter poll: %v", err)
	}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/perch/accounts/"+id, nil)
	r.SetPathValue("id", id)
	e.srv.apiPerchAccount(rec, r)
	var out struct {
		Account map[string]any `json:"account"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	acc := out.Account
	if acc["tierSelectionRequired"] == true {
		t.Fatal("a selected plan must not stay flagged as pending")
	}
	if acc["status"] == "tier-pending" {
		t.Fatal("status stayed tier-pending after the plan was chosen")
	}
	if !acc["remainingKnown"].(bool) {
		t.Fatal("a stated cap must produce a known remaining allowance")
	}
	if acc["allowanceSource"] != string(perch.AllowanceEntitlement) {
		t.Fatalf("allowanceSource = %v, want entitlement", acc["allowanceSource"])
	}
}
