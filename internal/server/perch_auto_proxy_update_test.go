package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/perchsignupstore"
)

// putProxy edits one inventory entry through the handler the console calls.
func putProxy(s *Server, id, url string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPut, "/api/perch/autosignup/proxies/"+id, bytes.NewBufferString(`{"url":"`+url+`"}`))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	s.apiPerchAutoUpdateProxy(w, r)
	return w
}

func putCandidateProxy(s *Server, id, proxyURL string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{"proxyUrl": proxyURL})
	r := httptest.NewRequest(http.MethodPut, "/api/perch/autosignup/candidates/"+id+"/proxy", bytes.NewReader(body))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	s.apiPerchAutoCandidateProxy(w, r)
	return w
}

// TestPerchAutoUpdateInventoryProxy covers the console's "修改" on an inventory
// row: the entry keeps its ID, the response is redacted, and a candidate bound
// to the old address keeps it.
func TestPerchAutoUpdateInventoryProxy(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	if _, err := s.perchAuto.store.ImportProxies([]string{"http://user:oldsecret@proxy.example:8080"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.perchAuto.store.Import([]perchsignupstore.Candidate{{Login: "bound", Password: "pw", TOTPSecret: "totp", MailboxURL: "mail"}}); err != nil {
		t.Fatal(err)
	}
	// Claim binds the inventory proxy, as a refill would.
	claim, err := s.perchAuto.store.Claim()
	if err != nil || claim.Candidate.ProxyURL != "http://user:oldsecret@proxy.example:8080" {
		t.Fatalf("claim: %+v %v", claim, err)
	}
	inventory := s.perchAuto.store.ListProxies()
	if len(inventory) != 1 {
		t.Fatal(inventory)
	}
	id := inventory[0].ID

	w := putProxy(s, id, "socks5://alice:newsecret@new.example:1080")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "newsecret") || strings.Contains(w.Body.String(), "alice") {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Proxy perchsignupstore.ProxySummary `json:"proxy"`
	}
	if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Proxy.ID != id || out.Proxy.Proxy != "socks5://new.example:1080" {
		t.Fatalf("reply: %s", w.Body.String())
	}
	if rec, _ := s.perchAuto.store.Get(claim.ID); rec.Candidate.ProxyURL != "http://user:oldsecret@proxy.example:8080" {
		t.Fatal("bound candidate was rewritten")
	}

	if w := putProxy(s, id, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("empty url: %d %s", w.Code, w.Body.String())
	}
	if w := putProxy(s, id, "file://user:newsecret@host:80"); w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "newsecret") {
		t.Fatalf("invalid: %d %s", w.Code, w.Body.String())
	}
	if w := putProxy(s, "missing", "http://ok.example:8080"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d %s", w.Code, w.Body.String())
	}
}

func TestPerchAutoUpdateInventoryProxyDuplicate(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	if _, err := s.perchAuto.store.ImportProxies([]string{"http://one.example:8080", "http://two.example:8080"}); err != nil {
		t.Fatal(err)
	}
	list := s.perchAuto.store.ListProxies()
	w := putProxy(s, list[0].ID, "http://someone:creds@two.example:8080")
	if w.Code != http.StatusConflict || strings.Contains(w.Body.String(), "creds") {
		t.Fatalf("duplicate: %d %s", w.Code, w.Body.String())
	}
	if got := s.perchAuto.store.ListProxies(); got[0] != list[0] || got[1] != list[1] {
		t.Fatalf("inventory changed: %+v", got)
	}
}

// TestPerchAutoCandidateProxy covers the candidate row's "修改代理", including
// the direct (empty) choice and the redaction of the list view.
func TestPerchAutoCandidateProxy(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	autoImportCandidate(t, s) // proxy http://proxy.example:8080
	row := s.perchAuto.store.List()[0]

	w := putCandidateProxy(s, row.ID, "socks5://bob:candsecret@cand.example:1080")
	if w.Code != http.StatusOK {
		t.Fatalf("set: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Candidate map[string]any `json:"candidate"`
	}
	if json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("reply: %s", w.Body.String())
	}
	if out.Candidate["proxy"] != "socks5://cand.example:1080" || out.Candidate["hasProxy"] != true {
		t.Fatalf("candidate view: %v", out.Candidate)
	}
	if strings.Contains(w.Body.String(), "candsecret") || strings.Contains(w.Body.String(), "bob") {
		t.Fatalf("leaked credentials: %s", w.Body.String())
	}
	rec, err := s.perchAuto.store.Get(row.ID)
	if err != nil || rec.Candidate.ProxyURL != "socks5://cand.example:1080" {
		t.Fatalf("stored: %+v %v", rec, err)
	}

	// the list carries the new endpoint, still without credentials
	list := httptest.NewRecorder()
	s.apiPerchAuto(list, httptest.NewRequest("GET", "/", nil))
	if list.Code != 200 || !strings.Contains(list.Body.String(), `"canEditProxy":true`) ||
		!strings.Contains(list.Body.String(), "socks5://cand.example:1080") {
		t.Fatalf("list: %s", list.Body.String())
	}
	if strings.Contains(list.Body.String(), "candsecret") {
		t.Fatal("list leaked credentials")
	}

	// an empty value is the direct path, and the row reports it as direct
	if w := putCandidateProxy(s, row.ID, ""); w.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}
	if rec, _ := s.perchAuto.store.Get(row.ID); rec.Candidate.ProxyURL != "" {
		t.Fatal("clear not stored")
	}
	list = httptest.NewRecorder()
	s.apiPerchAuto(list, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(list.Body.String(), `"hasProxy":false`) {
		t.Fatalf("direct row: %s", list.Body.String())
	}

	if w := putCandidateProxy(s, row.ID, "file://user:secret@host:80"); w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("invalid: %d %s", w.Code, w.Body.String())
	}
	if w := putCandidateProxy(s, "missing", "http://ok.example:8080"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d %s", w.Code, w.Body.String())
	}
}

// A candidate the scheduler is working on must not have its proxy swapped under
// it, and neither may one already in the pool.
func TestPerchAutoCandidateProxyRefusedWhileBusyOrCompleted(t *testing.T) {
	e := newPerchOpenEnv(t)
	s := e.srv
	autoImportCandidate(t, s)
	row := s.perchAuto.store.List()[0]

	s.perchAuto.mu.Lock()
	s.perchAuto.workingID = row.ID
	s.perchAuto.mu.Unlock()
	if w := putCandidateProxy(s, row.ID, "http://new.example:8080"); w.Code != http.StatusConflict {
		t.Fatalf("busy: %d %s", w.Code, w.Body.String())
	}
	s.perchAuto.mu.Lock()
	s.perchAuto.workingID = ""
	s.perchAuto.mu.Unlock()

	claim, err := s.perchAuto.store.ClaimByID(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.perchAuto.store.Update(claim.ID, perchsignupstore.Ready, []byte("session"), ""); err != nil {
		t.Fatal(err)
	}
	// ready is editable: the candidate has not been handed to a worker
	if w := putCandidateProxy(s, row.ID, "http://new.example:8080"); w.Code != http.StatusOK {
		t.Fatalf("ready: %d %s", w.Code, w.Body.String())
	}
	if err := s.perchAuto.store.Update(claim.ID, perchsignupstore.Completed, nil, "acct"); err != nil {
		t.Fatal(err)
	}
	if w := putCandidateProxy(s, row.ID, "http://other.example:8080"); w.Code != http.StatusConflict {
		t.Fatalf("completed: %d %s", w.Code, w.Body.String())
	}
	// the list reflects the same rule
	list := httptest.NewRecorder()
	s.apiPerchAuto(list, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(list.Body.String(), `"canEditProxy":false`) {
		t.Fatalf("completed row: %s", list.Body.String())
	}
}

// TestPerchBatchSetProxy covers the pool-side bulk edit.
func TestPerchBatchSetProxy(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("proxy-batch-a")
	e.fake.seed("proxy-batch-b")
	e.importFiles(map[string][]byte{
		"a.json": []byte(exportDoc("proxy-batch-a", e.fake, time.Now().Add(time.Hour).Unix(), 0)),
		"b.json": []byte(exportDoc("proxy-batch-b", e.fake, time.Now().Add(time.Hour).Unix(), 0)),
	})
	ids := []string{}
	for _, a := range e.srv.cfg.Get().Accounts {
		if a.Provider == config.ProviderPerch {
			ids = append(ids, a.ID)
		}
	}
	if len(ids) != 2 {
		t.Fatalf("accounts: %v", ids)
	}
	run := func(body string) (int, string) {
		t.Helper()
		w := httptest.NewRecorder()
		e.srv.apiPerchBatch(w, httptest.NewRequest(http.MethodPost, "/api/perch/batch", bytes.NewBufferString(body)))
		return w.Code, w.Body.String()
	}
	proxy := "socks5://secretuser:secretpass@batch.example:1080"
	code, body := run(`{"action":"proxy","ids":["` + ids[0] + `","` + ids[1] + `"],"proxyUrl":"` + proxy + `"}`)
	if code != http.StatusOK || strings.Contains(body, "secretuser") || strings.Contains(body, "secretpass") {
		t.Fatalf("batch: %d %s", code, body)
	}
	cfg := e.srv.cfg.Get()
	for _, id := range ids {
		if a, ok := cfg.Account(id); !ok || a.ProxyURL != proxy {
			t.Fatalf("account %s proxy = %q", id, a.ProxyURL)
		}
	}
	// an empty string is the direct choice, not a missing field
	if code, body := run(`{"action":"proxy","ids":["` + ids[0] + `"],"proxyUrl":""}`); code != http.StatusOK {
		t.Fatalf("direct: %d %s", code, body)
	}
	if a, _ := e.srv.cfg.Get().Account(ids[0]); a.ProxyURL != "" {
		t.Fatalf("direct not applied: %q", a.ProxyURL)
	}
	// an invalid address fails the whole batch without echoing credentials
	code, body = run(`{"action":"proxy","ids":["` + ids[1] + `"],"proxyUrl":"ftp://secretuser:secretpass@example.com"}`)
	if code != http.StatusBadRequest || strings.Contains(body, "secretuser") || strings.Contains(body, "secretpass") {
		t.Fatalf("invalid: %d %s", code, body)
	}
	if a, _ := e.srv.cfg.Get().Account(ids[1]); a.ProxyURL != proxy {
		t.Fatalf("invalid write applied: %q", a.ProxyURL)
	}
	// a missing field is refused rather than silently selecting direct
	if code, _ := run(`{"action":"proxy","ids":["` + ids[1] + `"]}`); code != http.StatusBadRequest {
		t.Fatalf("missing proxyUrl: %d", code)
	}
	// an id that names no Perch account is a reported failure, not a success
	code, body = run(`{"action":"proxy","ids":["ghost"],"proxyUrl":"http://ok.example:8080"}`)
	if code != http.StatusOK || !strings.Contains(body, `"ok":0`) || !strings.Contains(body, `"failed":1`) {
		t.Fatalf("ghost id: %d %s", code, body)
	}
}
