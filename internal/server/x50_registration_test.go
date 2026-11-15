package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/x50reg"
	"account-pool-balancer/internal/x50signupstore"
)

// newX50Env builds a server with an isolated data dir and no upstream traffic.
func newX50Env(t *testing.T) *x50Env {
	t.Helper()
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.stopPerch(); s.logs.Close() })
	return &x50Env{t: t, s: s}
}

type x50Env struct {
	t *testing.T
	s *Server
}

func (e *x50Env) do(method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	payload := []byte("{}")
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		payload = b
	} else if method == http.MethodGet || method == http.MethodDelete {
		payload = nil
	}
	rec := httptest.NewRecorder()
	cookie, csrf := newX50Session(e.t, e.s)
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	req.Header.Set("X-CSRF-Token", csrf)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	e.s.Handler().ServeHTTP(rec, req)
	return rec
}

// newX50Session creates one admin session and returns its cookie and CSRF token.
func newX50Session(t *testing.T, s *Server) (cookie, csrf string) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.admin.create(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			cookie = c.Value
		}
	}
	if cookie == "" {
		t.Fatal("no session cookie")
	}
	s.admin.mu.Lock()
	if sess := s.admin.sessions[tokenKey(cookie)]; sess != nil {
		csrf = sess.csrf
	}
	s.admin.mu.Unlock()
	return cookie, csrf
}

const x50SampleLine1 = "shellac-visitor.3@icloud.com----https://icloud.mida.vip/messages/shellac-visitor.3%40icloud.com?token=tok-1"
const x50SampleLine2 = "wastes-clefts.0q@icloud.com----https://icloud.mida.vip/messages/wastes-clefts.0q%40icloud.com?token=tok-2"
const x50SampleLine3 = "revues.95-trip@icloud.com----https://icloud.mida.vip/messages/revues.95-trip%40icloud.com?token=tok-3"

func TestX50BulkImportParsesLinesAndBindsProxiesInOrder(t *testing.T) {
	e := newX50Env(t)
	w := e.do(http.MethodPost, "/api/x50/candidates/bulk", map[string]any{
		"text": x50SampleLine1 + "\n" + x50SampleLine2 + "\n" + x50SampleLine3 + "\n" +
			"# 注释行\n" +
			"broken-line-without-separator\n" +
			"no-token@icloud.com----https://icloud.mida.vip/messages/x?notoken=1\n",
		"proxies": []string{"socks5://u:p@a:1", "socks5://u:p@b:2", "socks5://u:p@c:3"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("import %d %s", w.Code, w.Body)
	}
	var resp struct {
		Added   int      `json:"added"`
		Skipped int      `json:"skipped"`
		Errors  []string `json:"errors"`
		Rows    []struct {
			ID         string `json:"id"`
			Email      string `json:"email"`
			ProxyLabel string `json:"proxyLabel"`
			ProxyBound bool   `json:"proxyBound"`
			HasMailbox bool   `json:"hasMailbox"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Added != 3 {
		t.Fatalf("added %d, want 3 (%v)", resp.Added, resp.Errors)
	}
	want := map[string]string{
		"shellac-visitor.3@icloud.com": "socks5://a:1",
		"wastes-clefts.0q@icloud.com":  "socks5://b:2",
		"revues.95-trip@icloud.com":    "socks5://c:3",
	}
	got := map[string]string{}
	for _, r := range resp.Rows {
		if !r.ProxyBound || !r.HasMailbox {
			t.Fatalf("row not bound: %+v", r)
		}
		got[r.Email] = r.ProxyLabel
	}
	for email, wantLabel := range want {
		if got[email] != wantLabel {
			t.Fatalf("%s bound to %q, want %q", email, got[email], wantLabel)
		}
	}
}

func TestX50ProxyAssignmentIsRoundRobinAndReusable(t *testing.T) {
	e := newX50Env(t)
	// 5 accounts over 2 proxies: strict round-robin, proxies reused, no row left
	// unbound while another proxy has fewer bindings.
	lines := []string{
		"a1@icloud.com----https://icloud.mida.vip/messages/a1%40icloud.com?token=t1",
		"a2@icloud.com----https://icloud.mida.vip/messages/a2%40icloud.com?token=t2",
		"a3@icloud.com----https://icloud.mida.vip/messages/a3%40icloud.com?token=t3",
		"a4@icloud.com----https://icloud.mida.vip/messages/a4%40icloud.com?token=t4",
		"a5@icloud.com----https://icloud.mida.vip/messages/a5%40icloud.com?token=t5",
	}
	w := e.do(http.MethodPost, "/api/x50/candidates/bulk", map[string]any{
		"text":    strings.Join(lines, "\n"),
		"proxies": []string{"socks5://u:p@gw1:5000", "socks5://u:p@gw2:5000"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("import %d %s", w.Code, w.Body)
	}
	w = e.do(http.MethodGet, "/api/x50/candidates", nil)
	var list struct {
		Rows []struct {
			Email      string `json:"email"`
			ProxyLabel string `json:"proxyLabel"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Rows) != 5 {
		t.Fatalf("rows %d", len(list.Rows))
	}
	count := map[string]int{}
	for _, r := range list.Rows {
		count[r.ProxyLabel]++
	}
	// 5 rows over 2 proxies: strict alternation gives 3/2 and both are used.
	if len(count) != 2 {
		t.Fatalf("expected both proxies used, got %v", count)
	}
	if diff := count["socks5://gw1:5000"] - count["socks5://gw2:5000"]; diff < -1 || diff > 1 {
		t.Fatalf("round-robin uneven: %v", count)
	}
}

func TestX50AssignProxiesBalancesAcrossSeparateImports(t *testing.T) {
	pool := []string{"socks5://p0", "socks5://p1", "socks5://p2"}
	var existing []x50signupstore.Row
	var targets []x50signupstore.Row
	for i := 0; i < 2; i++ {
		targets = append(targets, x50signupstore.Row{ID: "first" + string(rune('a'+i))})
	}
	got := assignProxies(existing, targets, pool)
	if len(got) != 2 || got["firsta"] != "socks5://p0" || got["firstb"] != "socks5://p1" {
		t.Fatalf("first batch: %v", got)
	}
	// pretend the batch is saved, then import two more
	for id, p := range got {
		existing = append(existing, x50signupstore.Row{ID: id, ProxyURL: p})
	}
	second := []x50signupstore.Row{{ID: "secondA"}, {ID: "secondB"}}
	got2 := assignProxies(existing, second, pool)
	if got2["secondA"] != "socks5://p2" || got2["secondB"] != "socks5://p0" {
		t.Fatalf("second batch must continue round-robin and reuse: %v", got2)
	}
}

func TestX50SettingsNeverReturnProxyCredentials(t *testing.T) {
	e := newX50Env(t)
	if w := e.do(http.MethodPut, "/api/x50/settings", map[string]any{
		"concurrency": 3, "mailTimeoutSec": 120, "mailPollSec": 2,
		"requestTimeoutSec": 20, "keyName": "k", "passwordMode": "random",
		"proxies": []string{"socks5://secretuser:secretpass@gw:5000"},
	}); w.Code != http.StatusOK {
		t.Fatalf("save %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"/api/x50/settings", "/api/x50/candidates"} {
		w := e.do(http.MethodGet, path, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, w.Code)
		}
		body := w.Body.String()
		for _, secret := range []string{"secretuser", "secretpass"} {
			if strings.Contains(body, secret) {
				t.Fatalf("%s leaked %q", path, secret)
			}
		}
		if !strings.Contains(body, "socks5://gw:5000") {
			t.Fatalf("%s lost the proxy label", path)
		}
	}
}

func TestX50SettingsRejectOutOfRangeConcurrency(t *testing.T) {
	e := newX50Env(t)
	for _, n := range []int{-1, x50signupstore.MaxConcurrency + 1} {
		w := e.do(http.MethodPut, "/api/x50/settings", map[string]any{"concurrency": n})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("concurrency %d accepted: %d %s", n, w.Code, w.Body)
		}
	}
	w := e.do(http.MethodPut, "/api/x50/settings", map[string]any{"concurrency": 3})
	if w.Code != http.StatusOK {
		t.Fatalf("valid concurrency rejected: %d %s", w.Code, w.Body)
	}
}

func TestX50RevealKeyIsTheOnlyPlaintextRead(t *testing.T) {
	e := newX50Env(t)
	m, err := e.s.x50Mgr()
	if err != nil {
		t.Fatal(err)
	}
	row, err := m.store.Add(x50signupstore.Row{
		Email: "k@icloud.com", MailboxURL: "https://x/messages/k?token=t",
		Status: x50signupstore.Keyed,
	})
	if err != nil {
		t.Fatal(err)
	}
	const key = "sk-50x-testSECRET0123456789"
	if _, err := m.store.Update(row.ID, func(r *x50signupstore.Row) error {
		r.APIKey, r.KeyPrefix, r.BalanceUSD, r.Status = key, "sk-50x-test", "100.000000000", x50signupstore.Keyed
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	list := e.do(http.MethodGet, "/api/x50/candidates", nil)
	if strings.Contains(list.Body.String(), key) {
		t.Fatal("list response leaked the API key")
	}
	if !strings.Contains(list.Body.String(), "sk-50x-test") {
		t.Fatal("masked key prefix missing from list")
	}
	reveal := e.do(http.MethodGet, "/api/x50/candidates/"+row.ID+"/key", nil)
	if reveal.Code != http.StatusOK || !strings.Contains(reveal.Body.String(), key) {
		t.Fatalf("reveal failed: %d %s", reveal.Code, reveal.Body)
	}
	if reveal.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("revealed key must not be cached")
	}
	// export is an explicit download and carries the key
	exp := e.do(http.MethodGet, "/api/x50/export", nil)
	if exp.Code != http.StatusOK || !strings.Contains(exp.Body.String(), key) {
		t.Fatalf("export failed: %d", exp.Code)
	}
}

func TestX50CompletedRowsNeverReplay(t *testing.T) {
	e := newX50Env(t)
	m, err := e.s.x50Mgr()
	if err != nil {
		t.Fatal(err)
	}
	row, err := m.store.Add(x50signupstore.Row{
		Email: "done@icloud.com", MailboxURL: "https://x/messages/d?token=t",
		Status: x50signupstore.Keyed,
	})
	if err != nil {
		t.Fatal(err)
	}
	// retry must be refused for a completed account
	if w := e.do(http.MethodPost, "/api/x50/candidates/"+row.ID+"/retry", map[string]any{}); w.Code != http.StatusBadRequest {
		t.Fatalf("retry on completed row accepted: %d %s", w.Code, w.Body)
	}
	// bulk start must skip it entirely
	w := e.do(http.MethodPost, "/api/x50/candidates/bulk-register", map[string]any{})
	var resp struct {
		Queued int `json:"queued"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Queued != 0 {
		t.Fatalf("completed row was queued again: %d", resp.Queued)
	}
	after, _ := m.store.Get(row.ID)
	if after.Status != x50signupstore.Keyed || after.Attempts != 0 {
		t.Fatalf("completed row mutated: %+v", after)
	}
}

func TestX50RetryableStatesAreOfferable(t *testing.T) {
	// Gate only: a completed account must never re-enter the machine, while a
	// failed one may. The runner itself is deliberately not started here — it
	// would reach the real upstream.
	for _, tc := range []struct {
		status x50signupstore.Status
		retry  bool
	}{
		{x50signupstore.Queued, true},
		{x50signupstore.Failed, true},
		{x50signupstore.Cancelled, true},
		{x50signupstore.Running, false},
		{x50signupstore.Waiting, false},
		{x50signupstore.Verified, false},
		{x50signupstore.Keyed, false},
	} {
		if got := tc.status.Retryable(); got != tc.retry {
			t.Fatalf("status %s retryable=%v, want %v", tc.status, got, tc.retry)
		}
		if tc.status.Done() != (tc.status == x50signupstore.Keyed) {
			t.Fatalf("status %s Done() mismatch", tc.status)
		}
	}
}

func TestX50DeleteCancelsAndRemoves(t *testing.T) {
	e := newX50Env(t)
	m, err := e.s.x50Mgr()
	if err != nil {
		t.Fatal(err)
	}
	row, err := m.store.Add(x50signupstore.Row{Email: "d@icloud.com", MailboxURL: "https://x/m?token=t"})
	if err != nil {
		t.Fatal(err)
	}
	if w := e.do(http.MethodDelete, "/api/x50/candidates/"+row.ID, nil); w.Code != http.StatusOK {
		t.Fatalf("delete %d %s", w.Code, w.Body)
	}
	if _, err := m.store.Get(row.ID); err == nil {
		t.Fatal("row survived delete")
	}
	if w := e.do(http.MethodDelete, "/api/x50/candidates/"+row.ID, nil); w.Code != http.StatusNotFound {
		t.Fatalf("second delete %d", w.Code)
	}
}

func TestX50BulkImportRejectsEmptyAndOversized(t *testing.T) {
	e := newX50Env(t)
	if w := e.do(http.MethodPost, "/api/x50/candidates/bulk", map[string]any{"text": "nothing useful\n"}); w.Code != http.StatusBadRequest {
		t.Fatalf("empty import accepted: %d", w.Code)
	}
	var b strings.Builder
	for i := 0; i < x50MaxBatch+1; i++ {
		fmt.Fprintf(&b, "user%04d@icloud.com----https://icloud.mida.vip/messages/user%04d%%40icloud.com?token=t%04d\n", i, i, i)
	}
	w := e.do(http.MethodPost, "/api/x50/candidates/bulk", map[string]any{"text": b.String()})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "最多") {
		t.Fatalf("oversized import: %d %s", w.Code, w.Body)
	}
}

func TestX50DuplicateEmailIsRejected(t *testing.T) {
	e := newX50Env(t)
	line := x50SampleLine1
	if w := e.do(http.MethodPost, "/api/x50/candidates/bulk", map[string]any{"text": line}); w.Code != http.StatusOK {
		t.Fatalf("first import %d %s", w.Code, w.Body)
	}
	w := e.do(http.MethodPost, "/api/x50/candidates/bulk", map[string]any{"text": line})
	var resp struct {
		Added   int      `json:"added"`
		Skipped int      `json:"skipped"`
		Errors  []string `json:"errors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Added != 0 || resp.Skipped != 1 || len(resp.Errors) != 1 {
		t.Fatalf("duplicate accepted: %+v", resp)
	}
	if !strings.Contains(resp.Errors[0], "已存在") {
		t.Fatalf("duplicate reason unreadable: %v", resp.Errors)
	}
}

func TestX50DryRunPersistsNothing(t *testing.T) {
	e := newX50Env(t)
	w := e.do(http.MethodPost, "/api/x50/candidates/bulk", map[string]any{"text": x50SampleLine1, "dryRun": true})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"dryRun":true`) {
		t.Fatalf("dry run: %d %s", w.Code, w.Body)
	}
	m, err := e.s.x50Mgr()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.store.List()) != 0 {
		t.Fatal("dry run persisted rows")
	}
}

func TestX50ClearFinishedKeepsUnfinished(t *testing.T) {
	e := newX50Env(t)
	m, err := e.s.x50Mgr()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.store.Add(x50signupstore.Row{Email: "a@icloud.com", Status: x50signupstore.Keyed}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.store.Add(x50signupstore.Row{Email: "b@icloud.com", Status: x50signupstore.Failed}); err != nil {
		t.Fatal(err)
	}
	w := e.do(http.MethodPost, "/api/x50/candidates/clear-finished", map[string]any{})
	var resp struct {
		Removed int `json:"removed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Removed != 1 || len(m.store.List()) != 1 {
		t.Fatalf("clear-finished removed %d, left %d", resp.Removed, len(m.store.List()))
	}
}

func TestX50ProxyLabelHidesCredentials(t *testing.T) {
	cases := map[string]string{
		"":                                   "",
		"socks5://user:pass@gw.example:5000": "socks5://gw.example:5000",
		"http://a:b@10.0.0.1:8080/path?x=1":  "http://10.0.0.1:8080",
		"direct.example:3128":                "direct.example:3128",
	}
	for in, want := range cases {
		if got := x50ProxyLabel(in); got != want {
			t.Fatalf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestX50ParseLinesSkipsUnreadableMailboxes(t *testing.T) {
	got := parseX50Lines(strings.Join([]string{
		"ok@icloud.com----https://icloud.mida.vip/messages/ok%40icloud.com?token=abc",
		"notab@icloud.com----https://x/messages/n?token=def",
		"  ",
		"# comment",
		"missing-token@icloud.com----https://x/messages/m?other=1",
		"tabs@icloud.com\thttps://x/messages/t?token=ghi",
		"dup@icloud.com----https://x/m/a?token=1",
		"DUP@icloud.com----https://x/m/a?token=2",
	}, "\n"))
	if len(got) != 4 {
		t.Fatalf("parsed %d: %+v", len(got), got)
	}
	if got[0].Email != "ok@icloud.com" || got[3].Email != "dup@icloud.com" {
		t.Fatalf("wrong rows: %+v", got)
	}
	for _, g := range got {
		if g.Email == "missing-token@icloud.com" {
			t.Fatal("unreadable mailbox was accepted")
		}
	}
}

func TestX50JobsOutliveTheQueuingRequest(t *testing.T) {
	// Regression: jobs used to inherit r.Context(), which Go cancels the moment
	// the handler returns — every background registration died instantly with
	// "context canceled". Jobs must run on the service context instead.
	e := newX50Env(t)
	m, err := e.s.x50Mgr()
	if err != nil {
		t.Fatal(err)
	}
	if m.base == nil {
		t.Fatal("manager has no base context")
	}
	if err := m.base.Err(); err != nil {
		t.Fatalf("base context already cancelled: %v", err)
	}
	row, err := m.store.Add(x50signupstore.Row{
		Email: "ctx@icloud.com", MailboxURL: "https://x/m?token=t", MailboxToken: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The run will fail against the real network, but it must fail for a
	// network reason and not because its context was cancelled by the request.
	_, err = m.startRows([]string{row.ID}, 1, x50signupstore.Settings{
		KeyName: "k", MailTimeoutSec: 2, MailPollSec: 1, RequestTimeoutSec: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := m.store.Get(row.ID)
		if got.Status == x50signupstore.Failed || got.Status == x50signupstore.Keyed {
			if strings.Contains(got.Error, "context canceled") {
				t.Fatalf("job was cancelled by its request context: %s", got.Error)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("job never reached a terminal state")
}

func TestStopX50CancelsAndWaits(t *testing.T) {
	e := newX50Env(t)
	m, err := e.s.x50Mgr()
	if err != nil {
		t.Fatal(err)
	}
	row, err := m.store.Add(x50signupstore.Row{
		Email: "stop@icloud.com", MailboxURL: "https://x/m?token=t", MailboxToken: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.startRows([]string{row.ID}, 1, x50signupstore.Settings{
		KeyName: "k", MailTimeoutSec: 60, MailPollSec: 1, RequestTimeoutSec: 5,
	}); err != nil {
		t.Fatal(err)
	}
	e.s.stopX50() // must not hang or panic
	if m.base.Err() == nil {
		t.Fatal("base context not cancelled at shutdown")
	}
}

// x50FakeUpstream stands up the API host and the shared-mailbox host the
// registrar talks to, so the whole row lifecycle can be exercised offline.
func x50FakeUpstream(t *testing.T) (apiURL, mailURL string, keys *int32) {
	t.Helper()
	const link = "link-tok:1:abc"
	var keyCount int32
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		switch r.URL.Path {
		case "/api/auth/signup":
			writeJSON(w, 200, map[string]any{"open": true})
		case "/api/auth/register":
			writeJSON(w, http.StatusCreated, map[string]string{"status": "verification_sent"})
		case "/api/auth/verify":
			if body["token"] != link {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid token"})
				return
			}
			writeJSON(w, 200, map[string]string{"token": "session-1"})
		case "/api/auth/me":
			writeJSON(w, 200, map[string]any{"username": "u"})
		case "/api/keys":
			if r.Method == http.MethodGet {
				writeJSON(w, 200, []any{})
				return
			}
			atomic.AddInt32(&keyCount, 1)
			writeJSON(w, http.StatusCreated, map[string]any{"id": 42, "prefix": "sk-50x-stub", "api_key": "sk-50x-stubSECRET"})
		case "/api/billing":
			writeJSON(w, 200, map[string]any{"balance_usd": "100.000000000", "tier": "paid"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	mailSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alias_email") == "" || r.URL.Query().Get("token") == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "invalid share"})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "data": map[string]any{"emails": []map[string]any{{
			"subject": "Confirm your 50x AI account", "fromAddress": "noreply_at_50x_energy@icloud.com",
			"textContent": "Confirm: https://ai.50x.energy/?verify=" + link,
		}}}})
	}))
	t.Cleanup(func() { apiSrv.Close(); mailSrv.Close() })
	return apiSrv.URL, mailSrv.URL, &keyCount
}

func TestX50RowLifecycleAgainstFakeUpstream(t *testing.T) {
	apiURL, mailURL, keyCount := x50FakeUpstream(t)
	e := newX50Env(t)
	if w := e.do(http.MethodPut, "/api/x50/settings", map[string]any{
		"concurrency": 2, "mailTimeoutSec": 15, "mailPollSec": 1, "requestTimeoutSec": 10,
		"keyName": "e2e", "apiBase": apiURL, "mailBase": mailURL,
	}); w.Code != http.StatusOK {
		t.Fatalf("settings %d %s", w.Code, w.Body)
	}
	if w := e.do(http.MethodPost, "/api/x50/candidates/bulk", map[string]any{
		"text": "e2e@icloud.com----https://icloud.mida.vip/messages/e2e%40icloud.com?token=share-1",
	}); w.Code != http.StatusOK {
		t.Fatalf("import %d %s", w.Code, w.Body)
	}
	if w := e.do(http.MethodPost, "/api/x50/candidates/bulk-register", map[string]any{}); w.Code != http.StatusOK {
		t.Fatalf("start %d %s", w.Code, w.Body)
	}

	m, err := e.s.x50Mgr()
	if err != nil {
		t.Fatal(err)
	}
	var row x50signupstore.Row
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		list := m.store.List()
		if len(list) == 1 {
			row = list[0]
			if row.Status == x50signupstore.Keyed || row.Status == x50signupstore.Failed {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if row.Status != x50signupstore.Keyed {
		t.Fatalf("row did not complete: %s / %s", row.Status, row.Error)
	}
	if row.APIKey != "sk-50x-stubSECRET" || row.BalanceUSD != "100.000000000" {
		t.Fatalf("credentials not captured: %+v", row)
	}
	if row.Password == "" || row.SessionToken != "session-1" {
		t.Fatalf("password/session missing: %+v", row)
	}
	if row.Stage != string(x50reg.StageDone) {
		t.Fatalf("stage %q", row.Stage)
	}
	if atomic.LoadInt32(keyCount) != 1 {
		t.Fatalf("expected exactly one key creation, got %d", atomic.LoadInt32(keyCount))
	}
	// Re-running the batch must not mint a second key for the same account.
	if w := e.do(http.MethodPost, "/api/x50/candidates/bulk-register", map[string]any{}); w.Code != http.StatusOK {
		t.Fatalf("re-start %d", w.Code)
	}
	time.Sleep(300 * time.Millisecond)
	if atomic.LoadInt32(keyCount) != 1 {
		t.Fatalf("completed account was registered twice: %d keys", atomic.LoadInt32(keyCount))
	}
	// The console can reveal it explicitly.
	rv := e.do(http.MethodGet, "/api/x50/candidates/"+row.ID+"/key", nil)
	if rv.Code != http.StatusOK || !strings.Contains(rv.Body.String(), "sk-50x-stubSECRET") {
		t.Fatalf("reveal %d %s", rv.Code, rv.Body)
	}
}

func TestX50FailedRowRetriesInPlace(t *testing.T) {
	apiURL, mailURL, _ := x50FakeUpstream(t)
	e := newX50Env(t)
	if w := e.do(http.MethodPut, "/api/x50/settings", map[string]any{
		"concurrency": 1, "mailTimeoutSec": 10, "mailPollSec": 1, "requestTimeoutSec": 10,
		"apiBase": apiURL, "mailBase": mailURL,
	}); w.Code != http.StatusOK {
		t.Fatalf("settings %d %s", w.Code, w.Body)
	}
	// A mailbox share without a token is rejected at import; inject the row
	// directly so the failure happens inside the run instead.
	m, err := e.s.x50Mgr()
	if err != nil {
		t.Fatal(err)
	}
	row, err := m.store.Add(x50signupstore.Row{
		Email: "retry@icloud.com", MailboxURL: "https://x/m?token=", MailboxToken: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if w := e.do(http.MethodPost, "/api/x50/candidates/"+row.ID+"/retry", map[string]any{}); w.Code != http.StatusOK {
		t.Fatalf("retry accepted code %d", w.Code)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := m.store.Get(row.ID)
		if got.Status == x50signupstore.Failed {
			if got.Attempts != 1 {
				t.Fatalf("attempts %d", got.Attempts)
			}
			if got.Error == "" {
				t.Fatal("failure has no reason")
			}
			// retryable again after failing
			if !got.Status.Retryable() {
				t.Fatal("failed row not retryable")
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("row never failed")
}

func TestX50ImportCompletedRecordsKeysWithoutReRegistering(t *testing.T) {
	e := newX50Env(t)
	keyCountBefore := int32(0) // no upstream is reachable from this test at all
	w := e.do(http.MethodPost, "/api/x50/candidates/import-completed", map[string]any{
		"accounts": []map[string]any{
			{
				"email": "done1@icloud.com", "password": "pw1",
				"mailboxUrl": "https://icloud.mida.vip/messages/done1%40icloud.com?token=t1",
				"apiKey":     "sk-50x-importedKEY1", "keyPrefix": "sk-50x-impo",
				"sessionToken": "sess-1", "plan": "starter", "tier": "paid",
				"balanceUsd": "100.000000000",
			},
			{"email": "bad-prefix@icloud.com", "apiKey": "sk-other-SECRET"},
			{"email": "no-key@icloud.com"},
			{"email": "not-an-email", "apiKey": "sk-50x-x"},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("import %d %s", w.Code, w.Body)
	}
	var resp struct {
		Added   int      `json:"added"`
		Skipped int      `json:"skipped"`
		Errors  []string `json:"errors"`
		Rows    []struct {
			Email      string `json:"email"`
			Status     string `json:"status"`
			HasKey     bool   `json:"hasKey"`
			KeyPrefix  string `json:"keyPrefix"`
			BalanceUSD string `json:"balanceUsd"`
			HasMailbox bool   `json:"hasMailbox"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Added != 1 || resp.Skipped != 3 {
		t.Fatalf("added=%d skipped=%d errors=%v", resp.Added, resp.Skipped, resp.Errors)
	}
	r := resp.Rows[0]
	if r.Status != string(x50signupstore.Keyed) || !r.HasKey || !r.HasMailbox {
		t.Fatalf("imported row not completed: %+v", r)
	}
	if r.BalanceUSD != "100.000000000" {
		t.Fatalf("balance lost: %+v", r)
	}
	// The key is stored verbatim and readable only through the explicit read.
	m, _ := e.s.x50Mgr()
	row, err := m.store.Get(resp.Rows[0].Email)
	if err == nil {
		t.Fatalf("store lookup by email should not resolve; got %+v", row)
	}
	list := m.store.List()
	if len(list) != 1 || list[0].APIKey != "sk-50x-importedKEY1" {
		t.Fatalf("stored key mismatch: %+v", list)
	}
	if list[0].SessionToken != "sess-1" || list[0].Password != "pw1" {
		t.Fatalf("credentials lost: %+v", list[0])
	}
	if atomic.LoadInt32(&keyCountBefore) != 0 {
		t.Fatal("unreachable")
	}
	// A completed row must never be re-run by the batch action.
	if w := e.do(http.MethodPost, "/api/x50/candidates/bulk-register", map[string]any{}); w.Code != http.StatusOK {
		t.Fatalf("bulk %d", w.Code)
	}
	var bulk struct {
		Queued int `json:"queued"`
	}
	json.Unmarshal(w.Body.Bytes(), &bulk)
	if bulk.Queued != 0 {
		t.Fatalf("imported completed row was queued: %d", bulk.Queued)
	}
}

func TestX50ImportCompletedNeverOverwritesExistingRow(t *testing.T) {
	e := newX50Env(t)
	m, _ := e.s.x50Mgr()
	orig, err := m.store.Add(x50signupstore.Row{Email: "dup@icloud.com", Status: x50signupstore.Failed, Error: "boom"})
	if err != nil {
		t.Fatal(err)
	}
	w := e.do(http.MethodPost, "/api/x50/candidates/import-completed", map[string]any{
		"accounts": []map[string]any{{"email": "dup@icloud.com", "apiKey": "sk-50x-newKEY"}},
	})
	var resp struct {
		Added   int      `json:"added"`
		Skipped int      `json:"skipped"`
		Errors  []string `json:"errors"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Added != 0 || resp.Skipped != 1 {
		t.Fatalf("existing row overwritten: %+v", resp)
	}
	after, _ := m.store.Get(orig.ID)
	if after.Status != x50signupstore.Failed || after.APIKey != "" {
		t.Fatalf("existing row mutated: %+v", after)
	}
}

func TestX50ImportCompletedDryRunWritesNothing(t *testing.T) {
	e := newX50Env(t)
	w := e.do(http.MethodPost, "/api/x50/candidates/import-completed", map[string]any{
		"dryRun":   true,
		"accounts": []map[string]any{{"email": "dry@icloud.com", "apiKey": "sk-50x-dry"}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("dry run %d %s", w.Code, w.Body)
	}
	m, _ := e.s.x50Mgr()
	if n := len(m.store.List()); n != 0 {
		t.Fatalf("dry run persisted %d rows", n)
	}
}
