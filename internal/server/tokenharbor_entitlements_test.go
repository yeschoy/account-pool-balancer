package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
	"account-pool-balancer/internal/thregister"
)

func thMappingFixture() string {
	b, _ := json.Marshal(map[string]any{"models": map[string]any{"agent-model": map[string]string{"tier": "agent"}, "office-model": map[string]string{"tier": "office"}, "free-model": map[string]string{"tier": "free"}}, "costs": map[string]int{}})
	c, _ := json.Marshal([]any{1, "b:" + string(b) + "\n"})
	return "<script>self.__next_f.push(" + string(c) + ")</script>"
}
func TestTHAccountAutomaticPlanCoverageUsageAndOverview(t *testing.T) {
	s := newTHTestServer(t)
	row := seedTHImported(t, s)
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" {
			t.Error("observation must be read-only")
		}
		if r.URL.Path != "/v1/models" && r.Header.Get("Cookie") != "session=secret-session" {
			t.Error("wrong saved session")
		}
		switch r.URL.Path {
		case "/pricing":
			fmt.Fprint(w, thPricingFixture("plus")+thMappingFixture())
		case "/api/me/free-tier":
			fmt.Fprintf(w, `{"ok":true,"on_plan":true,"activated":true,"plan_used_pct":25,"plan_exhausted":false,"overage_billing":false,"period_ends_at":%q,"plan":{"name":"Agent Pass","window_days":7}}`, time.Now().Add(36*time.Hour).UTC().Format(time.RFC3339Nano))
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"agent-model"},{"id":"office-model"},{"id":"free-model"},{"id":"legacy-unknown"}]}`)
		default:
			t.Error("unexpected path", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer up.Close()
	_, err := s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = up.URL + "/v1"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.cfg.Get().Account(row.Account)
	if err := s.quota.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	q := s.quota.Get(a.ID)
	if q.TokenHarbor.Plan.Tier != "plus" || !q.Catalog["agent-model"] || len(q.Catalog) != 1 || len(q.TokenHarbor.Directory) != 4 || q.TokenHarbor.Usage.UsedPercent == nil || *q.TokenHarbor.Usage.UsedPercent != 25 || calls != 3 {
		t.Fatalf("wrong coverage/usage %+v calls=%d", q, calls)
	}
	if strings.Contains(string(s.thModels(s.cfg.Get(), a.Group)), "office-model") {
		t.Fatal("higher tier leaked into catalog")
	}
	read := func() map[string]any {
		w := httptest.NewRecorder()
		s.apiTHOverview(w, httptest.NewRequest("GET", "/api/tokenharbor/overview", nil))
		var x map[string]any
		json.Unmarshal(w.Body.Bytes(), &x)
		return x
	}
	got := read()
	if got["totalEquivalentUsd"] != float64(20) || got["availableWeeklyEquivalentUsd"] != float64(0) {
		t.Fatal("disabled account quota counted as currently schedulable", got)
	}
	_, err = s.cfg.Update(-1, func(c *config.Config) error { c.Accounts[0].Enabled = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	got = read()
	if got["availableWeeklyEquivalentUsd"] != 3.75 {
		t.Fatal("weekly fraction or account-specific reset calculation wrong", got)
	}
	*q.TokenHarbor.Usage.OverageBilling = true
	s.quota.Load(map[string]*quota.Snapshot{a.ID: q})
	if s.pool.Views(s.cfg.Get())[a.ID].Status != "wallet-overage-enabled" || read()["availableWeeklyEquivalentUsd"] != float64(0) {
		t.Fatal("wallet continuation bypassed Pass-only policy")
	}
	q = s.quota.Get(a.ID)
	*q.TokenHarbor.Usage.OverageBilling = false
	q.TokenHarbor.Usage.ResetsAt = time.Now().Add(-time.Second)
	s.quota.Load(map[string]*quota.Snapshot{a.ID: q})
	if read()["availableWeeklyEquivalentUsd"] != nil {
		t.Fatal("expired measurement invented a full or zero balance")
	}
}

func TestTHObservationPersistsOnlyChangedSessionCookies(t *testing.T) {
	if thSessionChanged([]thregister.NamedCookie{{Name: "better-auth.session_token", Value: "stable"}, {Name: "better-auth.session_data", Value: "cache1"}}, []thregister.NamedCookie{{Name: "better-auth.session_token", Value: "stable"}, {Name: "better-auth.session_data", Value: "cache2"}}) {
		t.Fatal("renewable session cache triggered full-store rewrite")
	}
	s := newTHTestServer(t)
	row := seedTHImported(t, s)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pricing":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "rotated", Path: "/"})
			fmt.Fprint(w, thPricingFixture("plus")+thMappingFixture())
		case "/api/me/free-tier":
			if c, e := r.Cookie("session"); e != nil || c.Value != "rotated" {
				t.Error("read did not reuse renewed cookie")
			}
			fmt.Fprint(w, `{"ok":true,"on_plan":true,"activated":false,"plan_used_pct":0,"plan_exhausted":false,"overage_billing":false,"plan":{"name":"Agent Pass","window_days":7}}`)
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"agent-model"}]}`)
		default:
			t.Error("unexpected request", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer up.Close()
	_, e := s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = up.URL + "/v1"; return nil })
	if e != nil {
		t.Fatal(e)
	}
	a, _ := s.cfg.Get().Account(row.Account)
	if e = s.quota.RefreshNow(context.Background(), a); e != nil {
		t.Fatal(e)
	}
	store, _ := s.thStore()
	renewed, _ := store.Get(row.ID)
	if len(renewed.Cookies) != 1 || renewed.Cookies[0].Value != "rotated" || renewed.APIKey != row.APIKey {
		t.Fatal("renewed auth cookie not safely persisted")
	}
	if e = s.quota.RefreshNow(context.Background(), a); e != nil {
		t.Fatal(e)
	}
	again, _ := store.Get(row.ID)
	if again.UpdatedAt != renewed.UpdatedAt {
		t.Fatal("unchanged observation rewrote the whole encrypted store")
	}
}

func TestTokenHarborStrictGateCannotBeBypassedAndModelsUseNoNetwork(t *testing.T) {
	e := newTHEnv(t)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		r := c.ForProvider(config.ProviderTokenHarbor).Routing
		r.IgnoreCatalog = true
		r.WalletFallback = true
		c.TokenHarbor.Routing = &r
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	st, _, _ := e.post(t, "/v1/chat/completions", `{"model":"legacy-unknown","messages":[]}`, nil)
	if st != 503 || e.count() != 0 {
		t.Fatal("manual unknown model bypassed strict Pass gate")
	}
	for _, body := range []string{`{"messages":[]}`, `{"model":null}`, `{"model":"m","Model":"frontier-demo"}`, `{"model":"frontier-demo","model":"m"}`, `{"model":"m","mo\u0064el":"frontier-demo"}`, `{"model":"m"}{"model":"frontier-demo"}`} {
		status, _, _ := e.post(t, "/v1/chat/completions", body, nil)
		if status != 400 || e.count() != 0 {
			t.Fatal("ambiguous or missing model bypassed enforcement", status, body)
		}
	}
	e.up.Close() // /models must use the snapshot even with the upstream offline.
	request := httptest.NewRequest("GET", "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer downstream-th")
	w := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(w, request)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"m"`) {
		t.Fatal("models depended on remote fan-out", w.Code, w.Body.String())
	}
	for _, a := range e.s.cfg.Get().Accounts {
		e.s.quota.Forget(a.ID)
	}
	w = httptest.NewRecorder()
	e.s.Handler().ServeHTTP(w, request)
	if strings.Contains(w.Body.String(), `"id":"m"`) {
		t.Fatal("unverified account published unrestricted models")
	}
}

func TestTokenHarborFailedAttemptRetainsDetailAfterSuccessfulFailover(t *testing.T) {
	e := newTHEnv(t)
	setTokenHarborDetail(t, e, true)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.Header.Get("Authorization"), "secret-1") {
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":{"message":"mock failure"}}`)
		} else {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
		}
	})
	status, _, headers := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil)
	if status != 200 || e.count() != 2 {
		t.Fatal("expected a single native failover", status, e.count())
	}
	logs, _ := e.s.logs.Query("details", map[string]string{"requestId": headers.Get("X-Request-Id"), "attemptIndex": "0", "status": "503"}, time.Time{}, time.Time{}, 10, 0)
	if len(logs.Items) != 1 || !bytes.Contains(logs.Items[0], []byte(`"context":"failed_attempt"`)) || !bytes.Contains(logs.Items[0], []byte("mock failure")) {
		t.Fatal("successful fallback lost failed attempt detail")
	}
}

func TestTHInvalidModelDetailsStillMaskAllKnownIdentitiesAndClientKey(t *testing.T) {
	e := newTHEnv(t)
	setTokenHarborDetail(t, e, true)
	status, _, headers := e.post(t, "/v1/chat/completions", `{"model":"m","model":"other","metadata":{"session_id":"body-only-session-456"},"messages":[{"role":"user","content":"downstream-th bad-request-session-123 body-only-session-456"}]}`, map[string]string{"X-Session-Id": "bad-request-session-123"})
	if status != 400 || e.count() != 0 {
		t.Fatal("ambiguous model was forwarded")
	}
	details, _ := e.s.logs.Query("details", map[string]string{"requestId": headers.Get("X-Request-Id")}, time.Time{}, time.Time{}, 10, 0)
	if len(details.Items) != 1 {
		t.Fatal("missing rejected request detail")
	}
	for _, private := range []string{"downstream-th", "bad-request-session-123", "body-only-session-456"} {
		if bytes.Contains(details.Items[0], []byte(private)) {
			t.Fatal("rejected request leaked known credential/identity")
		}
	}
}

func TestTHLargeBodyPreviewKeepsContextWithoutRetainingPrivateMetadata(t *testing.T) {
	body := []byte(`{"session_id":"private-session-12345","messages":[{"role":"user","content":"HEAD-` + strings.Repeat("x", 400000) + `-TAIL"}],"metadata":{"session_id":"private-session-12345"}}`)
	preview, cut := thBodyPreview(body, []string{"private-session-12345"}, 1024)
	if !cut || len(preview) > 1200 || !bytes.Contains(preview, []byte("HEAD-")) || !bytes.Contains(preview, []byte("-TAIL")) || bytes.Contains(preview, []byte("private-session-12345")) {
		t.Fatal("large diagnostic body lost context or exposed identity")
	}
}

func TestTokenHarborFailureDetailsAreBoundedPrivateAndDisableStopsCapture(t *testing.T) {
	e := newTHEnv(t)
	setTokenHarborDetail(t, e, true)
	const identity = "private-session-123456789"
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		fmt.Fprintf(w, `{"error":{"message":%q}}`, "bad input "+identity)
	})
	status, _, headers := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[],"session_id":"`+identity+`"}`, map[string]string{"X-Session-Id": identity, "Cookie": "session=secret-cookie"})
	if status != 400 {
		t.Fatal(status)
	}
	logs, _ := e.s.logs.Query("details", map[string]string{"requestId": headers.Get("X-Request-Id")}, time.Time{}, time.Time{}, 10, 0)
	if len(logs.Items) != 1 {
		t.Fatal("failed request missing details with tokenHarbor.detailLog=true")
	}
	for _, secret := range []string{identity, "secret-cookie", "thk_live_mock-secret", "downstream-th"} {
		if bytes.Contains(logs.Items[0], []byte(secret)) {
			t.Fatal("detail leaked sensitive identity/credential")
		}
	}
	errorsLog, _ := e.s.logs.Query("errors", map[string]string{"requestId": headers.Get("X-Request-Id")}, time.Time{}, time.Time{}, 10, 0)
	for _, item := range errorsLog.Items {
		if bytes.Contains(item, []byte(identity)) {
			t.Fatal("ordinary error reason leaked reflected session")
		}
	}
	setTokenHarborDetail(t, e, false)
	e.setHandler(nil)
	_, _, headers = e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil)
	logs, _ = e.s.logs.Query("details", map[string]string{"requestId": headers.Get("X-Request-Id")}, time.Time{}, time.Time{}, 10, 0)
	if len(logs.Items) != 0 {
		t.Fatal("successful request stored body after disabling details")
	}
	cw := &captureWriter{ResponseWriter: httptest.NewRecorder(), max: 1024}
	cw.Write(bytes.Repeat([]byte("x"), 2<<20))
	if len(cw.tail) > 512 || cap(cw.tail) > 1024 {
		t.Fatal("bounded tail retained a large upstream Write")
	}
}
