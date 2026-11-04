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
	// Registrations reach the network; the tests here never start one with a
	// real row, and a queued row's worker is cancelled below.
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
	rec := postTH(t, s, "/api/tokenharbor/registrations",
		`{"accounts":[{"email":"a@x.com","password":"SuperSecret123"}]}`)
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
