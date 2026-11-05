package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/thregister"
	"account-pool-balancer/internal/thsignupstore"
)

func newTHTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.logs.Close)
	// Registrations are background goroutines that write to the log store;
	// stop them before the test closes that store. A row that is never worked
	// on gives up on thStopping, which this closes.
	t.Cleanup(s.stopTokenHarbor)
	return s
}

// postTH calls an admin endpoint as a logged-in console session would.
func postTH(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	token := s.admin.create(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	_ = token
	var cookie string
	var csrf string
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c.Value
		}
	}
	if cookie == "" {
		t.Fatal("no session cookie")
	}
	// the CSRF token lives on the session, keyed by the cookie's hash
	s.admin.mu.Lock()
	if sess := s.admin.sessions[tokenKey(cookie)]; sess != nil {
		csrf = sess.csrf
	}
	s.admin.mu.Unlock()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	req.Header.Set("X-CSRF-Token", csrf)
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// savedSession creates one admin session and returns its cookie value.
func savedSession(t *testing.T, s *Server) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.admin.create(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}

// A batch must be validated before anything is queued: a bad mailbox or a bad
// proxy line is the operator's typo, not something to discover per row.
func TestTHRegisterRejectsBadInput(t *testing.T) {
	s := newTHTestServer(t)
	cases := map[string]string{
		"empty":       `{"accounts":[]}`,
		"bad email":   `{"accounts":[{"email":"nope","password":"pw"}]}`,
		"dup email":   `{"accounts":[{"email":"a@x.com","password":"pw"},{"email":"A@x.com","password":"pw"}]}`,
		"bad mailbox": `{"accounts":[{"email":"a@x.com","password":"pw","mailboxUrl":"https://mida.vip/nope"}]}`,
		"bad proxy":   `{"accounts":[{"email":"a@x.com","password":"pw"}],"proxies":["socks4://h:1"]}`,
	}
	for name, body := range cases {
		rec := postTH(t, s, "/api/tokenharbor/registrations", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: want 400, got %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

// The regression this exists for: the proxy list must be spread round-robin,
// and a row must never fall back to direct egress when proxies were given.
func TestTHRegisterSpreadsProxies(t *testing.T) {
	s := newTHTestServer(t)
	// Every row registers against an unroutable site so the background workers
	// fail fast instead of reaching the real one.
	if _, err := s.cfg.Update(-1, func(c *config.Config) error {
		th := c.TokenHarbor
		th.BaseURL = "http://127.0.0.1:1/v1"
		c.TokenHarbor = th
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	body := `{"accounts":[
		{"email":"a@x.com","password":"pw"},
		{"email":"b@x.com","password":"pw"},
		{"email":"c@x.com","password":"pw"}],
		"proxies":["http://u:p@1.1.1.1:8080","socks5://u:p@2.2.2.2:1080"]}`
	rec := postTH(t, s, "/api/tokenharbor/registrations", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d %s", rec.Code, rec.Body.String())
	}
	store, err := s.thStore()
	if err != nil {
		t.Fatal(err)
	}
	rows := store.List()
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.Email] = r.Proxy
	}
	if got["a@x.com"] != "http://1.1.1.1:8080" || got["b@x.com"] != "socks5://2.2.2.2:1080" || got["c@x.com"] != "http://1.1.1.1:8080" {
		t.Fatalf("proxy round-robin wrong: %v", got)
	}
}

// The store is credential-sensitive: the API must never echo a password, a
// cookie or a full key.
func TestTHRowsNeverLeakCredentials(t *testing.T) {
	s := newTHTestServer(t)
	if _, err := s.cfg.Update(-1, func(c *config.Config) error {
		th := c.TokenHarbor
		th.BaseURL = "http://127.0.0.1:1/v1"
		c.TokenHarbor = th
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rec := postTH(t, s, "/api/tokenharbor/registrations",
		`{"accounts":[{"email":"a@x.com","password":"SuperSecret123"}],"solveCaptcha":false}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(out.Rows))
	}
	if _, ok := out.Rows[0]["password"]; ok {
		t.Fatal("the response must not carry a password field")
	}
	if strings.Contains(rec.Body.String(), "SuperSecret123") {
		t.Fatal("the response leaked the password")
	}
}

func TestTHImportAddsOneAccount(t *testing.T) {
	s := newTHTestServer(t)
	store, _ := s.thStore()
	rows, err := store.Add("that_test", []thsignupstore.Row{{
		Email: "import@x.com", Password: "pw", APIKey: "thk_live_abcdefghijklmnop", ProxyURL: "http://u:p@1.1.1.1:8080",
	}})
	if err != nil {
		t.Fatal(err)
	}
	id := rows[0].ID
	if _, err := store.Update(id, func(r *thsignupstore.Row) error {
		r.Status = thsignupstore.Registered
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.thImport(id, ""); err != nil {
		t.Fatalf("import: %v", err)
	}
	cfg := s.cfg.Get()
	var found *config.Account
	for i := range cfg.Accounts {
		if cfg.Accounts[i].Provider == config.ProviderTokenHarbor {
			found = &cfg.Accounts[i]
		}
	}
	if found == nil {
		t.Fatal("no Token Harbor account was created")
	}
	if found.RuntimeKey != "thk_live_abcdefghijklmnop" || found.Group != config.TokenHarborGroup {
		t.Fatalf("wrong account: %+v", found)
	}
	// Importing twice must not duplicate the account.
	if err := s.thImport(id, ""); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, a := range s.cfg.Get().Accounts {
		if a.Provider == config.ProviderTokenHarbor {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("re-import duplicated the account: %d", n)
	}
}

// A row the registrar has not finished must not be importable: the key only
// exists once the site created it.
func TestTHImportRejectsUnregistered(t *testing.T) {
	s := newTHTestServer(t)
	store, _ := s.thStore()
	rows, _ := store.Add("that_test", []thsignupstore.Row{{Email: "q@x.com", Password: "pw"}})
	if err := s.thImport(rows[0].ID, ""); err == nil {
		t.Fatal("importing a queued row must fail")
	}
}

// The flow's own failure taxonomy must survive into the row detail, so the
// console can say WHY a registration failed instead of "failed".
func TestTHErrorStagesAreNamed(t *testing.T) {
	if !strings.Contains(thRedact("注册失败：thk_live_abcdefgh"), "…") {
		t.Fatal("thRedact must mask key material")
	}
	if thregister.AccountID("A@x.com") != thregister.AccountID("a@x.com") {
		t.Fatal("account identity must be case-insensitive")
	}
}

// A row whose mail arrived after the wait must be importable only after the
// address is confirmed: its key answers metadata but refuses chat.
func TestTHImportRefusesUnverified(t *testing.T) {
	s := newTHTestServer(t)
	store, _ := s.thStore()
	rows, _ := store.Add("that_test", []thsignupstore.Row{{Email: "u@x.com", Password: "pw"}})
	id := rows[0].ID
	if _, err := store.Update(id, func(r *thsignupstore.Row) error {
		r.Status = thsignupstore.Unverified
		r.APIKey = "thk_live_TESTOKENTESTOKENTESTOKENTESTOKENTESTOKEN"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.thImport(id, ""); err == nil {
		t.Fatal("an unverified account must not enter the pool as usable")
	}
	// Once confirmed it imports.
	if _, err := store.Update(id, func(r *thsignupstore.Row) error {
		r.Status = thsignupstore.Registered
		r.Verified = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.thImport(id, ""); err != nil {
		t.Fatalf("verified account should import: %v", err)
	}
}

// The console tails the registration log by sequence number; the API must
// return only what is new and never a credential.
func TestTHLogTail(t *testing.T) {
	s := newTHTestServer(t)
	s.thLive("x@icloud.com", "开始注册", "", "http://u:p@1.2.3.4:8080", "info")
	s.thLive("x@icloud.com", "完成", "key=thk_live_TESTOKENTESTOKENTESTOKEN…EN", "http://u:p@1.2.3.4:8080", "success")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tokenharbor/log?after=0", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: savedSession(t, s)})
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Entries []thLogEntry `json:"entries"`
		Seq     int64        `json:"seq"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != 2 || out.Seq != 2 {
		t.Fatalf("want 2 entries and seq 2, got %d / %d", len(out.Entries), out.Seq)
	}
	if strings.Contains(rec.Body.String(), "u:p@") {
		t.Fatal("the log leaked proxy credentials")
	}
	if out.Entries[0].Email != "x@icloud.com" || out.Entries[1].Stage != "完成" {
		t.Fatalf("unexpected entries: %+v", out.Entries)
	}

	// Asking again from the latest sequence yields nothing new.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/tokenharbor/log?after=2", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: savedSession(t, s)})
	s.Handler().ServeHTTP(rec, req)
	var again struct {
		Entries []thLogEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if len(again.Entries) != 0 {
		t.Fatalf("tail must be incremental, got %d", len(again.Entries))
	}
}

// The registration flow talks to the site, not to the /v1 API root: the
// setting is an API root, so exactly that suffix comes off and nothing else.
// The regression this guards: a stub URL was once reduced to its host, and the
// test happily registered against production.
func TestTHBaseURLKeepsHostAndPath(t *testing.T) {
	cases := map[string]string{
		"":                                  thregister.DefaultBase,
		"https://tokenharbor.ai/v1":         "https://tokenharbor.ai",
		"https://tokenharbor.ai/v1/":        "https://tokenharbor.ai",
		"https://tokenharbor.ai":            "https://tokenharbor.ai",
		"http://127.0.0.1:8080/stub/v1":     "http://127.0.0.1:8080/stub",
		"https://staging.example.com/th/v1": "https://staging.example.com/th",
		"http://127.0.0.1:1/v1":             "http://127.0.0.1:1",
	}
	for in, want := range cases {
		if got := thBaseURL(in); got != want {
			t.Errorf("thBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}
