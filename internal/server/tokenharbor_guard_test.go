package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/thregister"
	"account-pool-balancer/internal/thsignupstore"
)

func thPricingFixture(tier string) string {
	states := []any{}
	for _, p := range []struct{ tier, slug, name string }{
		{"free", "free", "Free"}, {"plus", "starter_pass_monthly", "Agent Pass"}, {"pro", "pro_pass_monthly", "Office Pass"}, {"frontier", "frontier_pass_monthly", "Frontier Pass"},
	} {
		state := "available"
		if tier == p.tier {
			state = "current"
		}
		states = append(states, map[string]any{"plan": map[string]string{"tier": p.tier, "slug": p.slug, "name": p.name}, "state": state})
	}
	var cycle any
	if tier != "free" {
		cycle = "monthly"
	}
	props := map[string]any{"signedIn": true, "free": states[0], "plans": states[1:], "currentCycle": cycle}
	raw, _ := json.Marshal([]any{"$", "$L1", nil, props})
	chunk, _ := json.Marshal([]any{1, "1:" + string(raw) + "\n"})
	return "<script>self.__next_f.push(" + string(chunk) + ")</script>"
}

func setupTHSubscription(t *testing.T, s *Server, tier string) string {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/pricing" || r.Header.Get("Cookie") != "session=saved" {
			t.Error("subscription must use saved session and GET pricing")
		}
		w.Write([]byte(thPricingFixture(tier)))
	}))
	t.Cleanup(up.Close)
	if _, err := s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = up.URL + "/v1"; return nil }); err != nil {
		t.Fatal(err)
	}
	return up.URL
}

func TestTHSaveOnlyBindsProxiesAndNeverContactsUpstream(t *testing.T) {
	s := newTHTestServer(t)
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
	defer up.Close()
	s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = up.URL + "/v1"; return nil })
	store, _ := s.thStore()
	proxies := []string{"http://user:secret@127.0.0.1:18001", "socks5://user:secret@127.0.0.1:18002"}
	if err := store.SaveProxies(proxies); err != nil {
		t.Fatal(err)
	}
	rec := postTH(t, s, "/api/tokenharbor/registrations", `{"saveOnly":true,"solveCaptcha":true,"accounts":[{"email":"a@x.com","password":"pw"},{"email":"b@x.com"},{"email":"c@x.com"}]}`)
	if rec.Code != 201 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	s.thTasks.Wait()
	if calls != 0 || len(s.cfg.Get().Accounts) != 0 {
		t.Fatal("save-only performed external work/import")
	}
	for _, r := range store.List() {
		full, _ := store.Get(r.ID)
		want := proxies[0]
		if r.Email == "b@x.com" {
			want = proxies[1]
		}
		if full.Status != thsignupstore.Saved || full.ProxyURL != want || full.Password == "" {
			t.Fatal("saved row lost credentials or bound/reused proxy")
		}
	}
	for _, secret := range []string{"user:secret", `"password"`, `"cookies"`} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatal("save response leaked credentials")
		}
	}
}

func TestTHProxySelectionExplicitSavedFallbackAndRandomReuse(t *testing.T) {
	for _, mode := range []string{"sequential", "random"} {
		rows := make([]thsignupstore.Row, 50)
		rows[0].ProxyURL = "http://explicit:8080"
		pool := []string{"http://one:8080", "http://two:8080"}
		if err := thAssignProxies(rows, pool, "http://fallback:8080", mode); err != nil {
			t.Fatal(err)
		}
		if rows[0].ProxyURL != "http://explicit:8080" {
			t.Fatal("explicit proxy replaced")
		}
		for i, row := range rows[1:] {
			if row.ProxyURL != pool[0] && row.ProxyURL != pool[1] {
				t.Fatal("selection left candidate pool")
			}
			if mode == "sequential" && row.ProxyURL != pool[(i+1)%2] {
				t.Fatal("sequence mismatch")
			}
		}
	}
	rows := []thsignupstore.Row{{}, {}}
	thAssignProxies(rows, nil, "http://fallback:8080", "")
	if rows[0].ProxyURL != "http://fallback:8080" || rows[1].ProxyURL != rows[0].ProxyURL {
		t.Fatal("default proxy must be reusable")
	}
}

func TestTHResubmissionKeepsSavedCredentialsAndOriginalJob(t *testing.T) {
	s := newTHTestServer(t)
	store, _ := s.thStore()
	rows, err := store.AddSaved("original", []thsignupstore.Row{{Email: "existing@x.com", Password: "original", ProxyURL: "http://original:8080"}})
	if err != nil {
		t.Fatal(err)
	}
	rec := postTH(t, s, "/api/tokenharbor/registrations", `{"accounts":[{"email":"existing@x.com","password":"replacement","proxyUrl":"http://replacement:8080"}]}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"original"`) {
		t.Fatal("resubmission must reopen original job", rec.Body.String())
	}
	s.thTasks.Wait()
	row, _ := store.Get(rows[0].ID)
	if row.Password != "original" || row.ProxyURL != "http://original:8080" || row.Status != thsignupstore.Saved {
		t.Fatal("resubmission changed or started saved credentials")
	}
}

func TestTHSubscriptionDiscardsLateResultAfterConfigurationChange(t *testing.T) {
	s := newTHTestServer(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = "http://127.0.0.1:1/v1"; return nil })
		w.Write([]byte(thPricingFixture("plus")))
	}))
	defer up.Close()
	s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = up.URL + "/v1"; return nil })
	store, _ := s.thStore()
	rows, _ := store.Add("test", []thsignupstore.Row{{Email: "late@x.com"}})
	store.Update(rows[0].ID, func(r *thsignupstore.Row) error {
		r.Status, r.Verified, r.APIKey = thsignupstore.Registered, true, "thk_live_mock_only"
		r.Cookies = []thsignupstore.Cookie{{Name: "session", Value: "saved"}}
		return nil
	})
	if err := s.thImport(context.Background(), rows[0].ID, ""); err == nil {
		t.Fatal("late subscription result must not grant admission")
	}
	row, _ := store.Get(rows[0].ID)
	if row.Subscription.Active() || len(s.cfg.Get().Accounts) != 0 {
		t.Fatal("late result persisted or imported")
	}
}

func TestTHRejectsAutoImportAndInvalidProxyModeBeforeSaving(t *testing.T) {
	s := newTHTestServer(t)
	for _, body := range []string{`{"autoImport":true,"accounts":[{"email":"a@x.com"}]}`, `{"saveOnly":true,"proxyMode":"bad","accounts":[{"email":"a@x.com"}]}`} {
		if rec := postTH(t, s, "/api/tokenharbor/registrations", body); rec.Code != 400 {
			t.Fatal("unsafe request accepted")
		}
	}
	store, _ := s.thStore()
	if len(store.List()) != 0 {
		t.Fatal("invalid request persisted rows")
	}
}

func TestTHImportRechecksSubscriptionInsteadOfCachedPaidOrCheckout(t *testing.T) {
	for _, tier := range []string{"free", "unknown"} {
		t.Run(tier, func(t *testing.T) {
			s := newTHTestServer(t)
			setupTHSubscription(t, s, tier)
			store, _ := s.thStore()
			rows, _ := store.Add("test", []thsignupstore.Row{{Email: "unpaid@x.com"}})
			id := rows[0].ID
			store.Update(id, func(r *thsignupstore.Row) error {
				r.Status, r.Verified, r.APIKey = thsignupstore.Registered, true, "thk_live_testcredential"
				r.Cookies = []thsignupstore.Cookie{{Name: "session", Value: "saved"}}
				r.Subscription = thregister.Subscription{Status: "active", Tier: "plus"}
				r.Checkout = &thsignupstore.Checkout{URL: "https://checkout.stripe.com/c/pay/unpaid"}
				return nil
			})
			if err := s.thImport(context.Background(), id, ""); err == nil {
				t.Fatal("unpaid/unknown subscription imported")
			}
			if len(s.cfg.Get().Accounts) != 0 {
				t.Fatal("failed gate mutated pool")
			}
			row, _ := store.Get(id)
			if row.Subscription.Active() || row.Status != thsignupstore.Registered {
				t.Fatal("failed check retained usable paid status or damaged registered state")
			}
		})
	}
}

func TestTHSavedRowStartsOnlyExplicitlyAndPreservesBinding(t *testing.T) {
	s := newTHTestServer(t)
	s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = "http://127.0.0.1:1/v1"; return nil })
	store, _ := s.thStore()
	rows, _ := store.AddSaved("saved", []thsignupstore.Row{{Email: "start@x.com", Password: "keep-me", ProxyURL: "http://127.0.0.1:1"}})
	path := "/api/tokenharbor/registrations/" + rows[0].ID + "/retry"
	rec := postTH(t, s, path, `{}`)
	if rec.Code != 202 {
		t.Fatalf("start saved: %d %s", rec.Code, rec.Body.String())
	}
	s.thTasks.Wait()
	row, _ := store.Get(rows[0].ID)
	if row.Status == thsignupstore.Saved || row.Password != "keep-me" || row.ProxyURL != "http://127.0.0.1:1" || len(s.cfg.Get().Accounts) != 0 {
		t.Fatal("explicit start lost binding or imported")
	}
}

func TestTHRegistrationCompletionChecksPlanButNeverImports(t *testing.T) {
	for _, tier := range []string{"free", "plus"} {
		t.Run(tier, func(t *testing.T) {
			s := newTHTestServer(t)
			var checks atomic.Int64
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "GET" && r.URL.Path == "/api/keys":
					w.Write([]byte(`{"keys":[]}`))
				case r.Method == "GET" && r.URL.Path == "/pricing":
					checks.Add(1)
					w.Write([]byte(thPricingFixture(tier)))
				default:
					t.Error("resume must not register, create keys, purchase or infer", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer up.Close()
			s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = up.URL + "/v1"; return nil })
			store, _ := s.thStore()
			rows, _ := store.Add("test", []thsignupstore.Row{{Email: "complete@x.com"}})
			store.Update(rows[0].ID, func(r *thsignupstore.Row) error {
				r.APIKey, r.Verified = "thk_live_existing_key", true
				r.Cookies = []thsignupstore.Cookie{{Name: "session", Value: "saved"}}
				return nil
			})
			if err := s.thRegistrar().run(rows[0].ID, false, 1); err != nil {
				t.Fatal(err)
			}
			row, _ := store.Get(rows[0].ID)
			if checks.Load() != 1 || row.Status != thsignupstore.Registered || row.Subscription.Tier != tier || len(s.cfg.Get().Accounts) != 0 {
				t.Fatal("registration must check plan without auto-importing even a paid account")
			}
		})
	}
}

func TestTHProxyAPIProtectsCredentialsAndReusesPool(t *testing.T) {
	s := newTHTestServer(t)
	path := "/api/tokenharbor/proxies"
	unauth := httptest.NewRecorder()
	s.Handler().ServeHTTP(unauth, httptest.NewRequest("GET", path, nil))
	if unauth.Code == 200 {
		t.Fatal("proxy pool must require authentication")
	}
	cookie := savedSession(t, s)
	s.admin.mu.Lock()
	csrf := s.admin.sessions[tokenKey(cookie)].csrf
	s.admin.mu.Unlock()
	request := func(method, body string, withCSRF bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		if withCSRF {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	body := `{"proxies":["http://proxy-user:proxy-secret@127.0.0.1:18001"]}`
	if rec := request("PUT", body, false); rec.Code == 200 {
		t.Fatal("proxy mutations must require CSRF")
	}
	for _, rec := range []*httptest.ResponseRecorder{request("PUT", body, true), request("GET", "", false)} {
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), `"count":1`) {
			t.Fatal("proxy save/view failed", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "proxy-user") || strings.Contains(rec.Body.String(), "proxy-secret") {
			t.Fatal("proxy API leaked authentication")
		}
	}
	for _, input := range []string{
		`{"saveOnly":true,"accounts":[{"email":"saved@x.com"}]}`,
		`{"saveOnly":true,"accounts":[{"email":"override@x.com","proxyUrl":"http://explicit:18002"}],"proxies":["http://task:18003"]}`,
		`{"saveOnly":true,"accounts":[{"email":"task@x.com"}],"proxies":["http://task:18003"]}`,
	} {
		if rec := postTH(t, s, "/api/tokenharbor/registrations", input); rec.Code != 201 {
			t.Fatal("save failed", rec.Body.String())
		}
	}
	store, _ := s.thStore()
	want := map[string]string{"saved@x.com": "http://proxy-user:proxy-secret@127.0.0.1:18001", "override@x.com": "http://explicit:18002", "task@x.com": "http://task:18003"}
	for _, sum := range store.List() {
		row, _ := store.Get(sum.ID)
		if row.ProxyURL != want[row.Email] {
			t.Fatal("proxy priority mismatch")
		}
	}
	if rec := request("PUT", `{}`, true); rec.Code != 400 || len(store.Proxies()) != 1 {
		t.Fatal("missing proxies must not silently clear saved pool")
	}
	if rec := request("PUT", `{"proxies":[]}`, true); rec.Code != 200 || len(store.Proxies()) != 0 {
		t.Fatal("proxy pool clear failed")
	}
	for _, sum := range store.List() {
		row, _ := store.Get(sum.ID)
		if row.ProxyURL != want[row.Email] {
			t.Fatal("clearing pool changed existing bindings")
		}
	}
}
