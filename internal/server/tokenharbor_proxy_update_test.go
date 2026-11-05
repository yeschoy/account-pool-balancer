package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
	"account-pool-balancer/internal/thsignupstore"
)

func putTH(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	cookie := savedSession(t, s)
	s.admin.mu.Lock()
	csrf := s.admin.sessions[tokenKey(cookie)].csrf
	s.admin.mu.Unlock()
	req := httptest.NewRequest("PUT", path, strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	req.Header.Set("X-CSRF-Token", csrf)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func TestTHProxyEditsAreIndependentAuthenticatedAndPrivate(t *testing.T) {
	s := newTHTestServer(t)
	before := seedTHImported(t, s)
	store, _ := s.thStore()
	rowPath := "/api/tokenharbor/registrations/" + before.ID + "/proxy"
	poolPath := "/api/accounts/" + before.Account
	for _, path := range []string{rowPath, poolPath} {
		for _, loggedIn := range []bool{false, true} {
			req := httptest.NewRequest("PUT", path, strings.NewReader(`{"proxyUrl":"http://ignored:8080"}`))
			if loggedIn {
				req.AddCookie(&http.Cookie{Name: sessionCookie, Value: savedSession(t, s)})
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, req)
			want := http.StatusUnauthorized
			if loggedIn {
				want = http.StatusForbidden
			}
			if w.Code != want {
				t.Fatal("proxy edit did not require auth/CSRF", w.Code)
			}
		}
	}
	seedTHCapabilities(s)
	original, _ := s.cfg.Get().Account(before.Account)
	regProxy := "http://reg-user:reg-private@127.0.0.1:18001"
	poolProxy := "socks5h://pool-user:pool-private@127.0.0.1:18002"
	for _, edit := range []struct{ path, proxy string }{{rowPath, regProxy}, {poolPath, poolProxy}} {
		body, _ := json.Marshal(map[string]string{"proxyUrl": edit.proxy})
		w := putTH(t, s, edit.path, string(body))
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("proxy edit failed", w.Code, w.Body.String())
		}
		for _, secret := range []string{"reg-user", "reg-private", "pool-user", "pool-private", before.APIKey, before.Password, before.Cookies[0].Value, before.Checkout.URL} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("proxy response leaked credential")
			}
		}
		row, _ := store.Get(before.ID)
		a, _ := s.cfg.Get().Account(before.Account)
		if row.ProxyURL != regProxy {
			t.Fatal("pool update changed registration proxy")
		}
		if edit.path == rowPath && a.ProxyURL != original.ProxyURL {
			t.Fatal("registration update changed pool proxy")
		}
		if edit.path == poolPath && a.ProxyURL != poolProxy {
			t.Fatal("pool proxy not saved")
		}
		want := before
		want.ProxyURL, want.ProxyBound, want.Subscription, want.UpdatedAt = row.ProxyURL, row.ProxyBound, row.Subscription, row.UpdatedAt
		if !reflect.DeepEqual(row, want) {
			t.Fatal("proxy edit changed saved credentials")
		}
		if a.Enabled != original.Enabled {
			t.Fatal("proxy edit enabled account")
		}
	}
	if q := s.quota.View(before.Account); q != nil && q.TokenHarbor != nil {
		t.Fatal("pool proxy edit reused old entitlement")
	}
	for _, path := range []string{rowPath, poolPath} {
		for _, body := range []string{`{"proxyUrl":"socks4://user:private@host:1080"}`, `{"proxyUrl":"http://user:private@host:8080/path"}`} {
			w := putTH(t, s, path, body)
			if w.Code == 200 || strings.Contains(w.Body.String(), "private") {
				t.Fatal("invalid edit accepted or credentials leaked", w.Code)
			}
		}
	}
	if w := putTH(t, s, rowPath, `{}`); w.Code != 400 {
		t.Fatal("missing proxy accepted")
	}
	if w := putTH(t, s, poolPath, `{"proxyUrl":""}`); w.Code != 200 {
		t.Fatal("empty pool edit failed")
	}
	a, _ := s.cfg.Get().Account(before.Account)
	if a.ProxyURL != poolProxy {
		t.Fatal("empty input accidentally cleared pool proxy")
	}
	if w := putTH(t, s, rowPath, `{"proxyUrl":""}`); w.Code != 200 {
		t.Fatal("registration clear failed")
	}
	row, _ := store.Get(before.ID)
	a, _ = s.cfg.Get().Account(before.Account)
	if row.Egress("http://default:8080") != "" || a.ProxyURL != poolProxy {
		t.Fatal("registration clear changed pool or inherited default")
	}
	if w := putTH(t, s, poolPath, `{"clearProxy":true}`); w.Code != 200 {
		t.Fatal("pool clear failed")
	}
	a, _ = s.cfg.Get().Account(before.Account)
	if a.ProxyURL != "" {
		t.Fatal("pool proxy not cleared")
	}
	for _, status := range []thsignupstore.Status{thsignupstore.Queued, thsignupstore.Running} {
		store.Update(before.ID, func(r *thsignupstore.Row) error { r.Status = status; return nil })
		if w := putTH(t, s, rowPath, `{"proxyUrl":"http://busy:8080"}`); w.Code != 409 {
			t.Fatal("busy proxy edit accepted", status, w.Code)
		}
	}
}

func TestTHImportInheritsRegistrationRouteButKeepsPoolOverride(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(fmt.Sprint("direct=", direct), func(t *testing.T) {
			s := newTHTestServer(t)
			proxy := setupTHSubscription(t, s, "plus")
			if direct {
				proxy = ""
			}
			if _, err := s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.ProxyURL = "http://127.0.0.1:1"; return nil }); err != nil {
				t.Fatal(err)
			}
			store, _ := s.thStore()
			rows, _ := store.AddSaved("route", []thsignupstore.Row{{Email: "route@mock.test", APIKey: "thk_live_route_mock", ProxyURL: proxy}})
			row, err := store.Update(rows[0].ID, func(r *thsignupstore.Row) error {
				r.Status, r.Verified = thsignupstore.Registered, true
				r.Cookies = []thsignupstore.Cookie{{Name: "session", Value: "saved"}}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.thImport(context.Background(), row.ID, ""); err != nil {
				t.Fatal(err)
			}
			row, _ = store.Get(row.ID)
			a, _ := s.cfg.Get().Account(row.Account)
			if a.ProxyURL != proxy {
				t.Fatal("admission did not inherit registration route")
			}
			if w := putTH(t, s, "/api/accounts/"+a.ID, `{"proxyUrl":"http://independent:8080"}`); w.Code != 200 {
				t.Fatal("pool override failed")
			}
			if err := s.thImport(context.Background(), row.ID, ""); err != nil {
				t.Fatal(err)
			}
			a, _ = s.cfg.Get().Account(a.ID)
			row, _ = store.Get(row.ID)
			if a.ProxyURL != "http://independent:8080" || row.ProxyURL != proxy || len(s.cfg.Get().Accounts) != 1 {
				t.Fatal("re-import overwrote independent route or duplicated account")
			}
		})
	}
}

func thProxyObservation(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/pricing":
		fmt.Fprint(w, thPricingFixture("plus")+thMappingFixture())
	case "/api/me/free-tier":
		fmt.Fprint(w, `{"ok":true,"on_plan":true,"activated":false,"plan_used_pct":0,"plan_exhausted":false,"overage_billing":false,"plan":{"name":"Agent Pass","window_days":7}}`)
	case "/v1/models":
		fmt.Fprint(w, `{"data":[{"id":"agent-model"}]}`)
	default:
		w.WriteHeader(404)
	}
}

func TestTHIndependentProxyObservationsUseTheirOwnRoutes(t *testing.T) {
	s := newTHTestServer(t)
	row := seedTHImported(t, s)
	var regCalls, poolCalls atomic.Int32
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { regCalls.Add(1); thProxyObservation(w, r) }))
	defer reg.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		poolCalls.Add(1)
		if r.URL.Path != "/v1/models" && r.Header.Get("Cookie") != "session=secret-session" {
			t.Error("missing linked session")
		}
		thProxyObservation(w, r)
	}))
	defer proxy.Close()
	store, _ := s.thStore()
	if _, err := store.SetProxy(row.ID, reg.URL); err != nil {
		t.Fatal(err)
	}
	_, err := s.cfg.Update(-1, func(c *config.Config) error {
		c.TokenHarbor.BaseURL = "http://tokenharbor.mock.invalid/v1"
		c.Accounts[0].ProxyURL = proxy.URL
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.cfg.Get().Account(row.Account)
	if err := s.quota.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	q := s.quota.View(a.ID)
	if regCalls.Load() != 0 || poolCalls.Load() != 3 || q == nil || !q.Catalog["agent-model"] || q.TokenHarbor.Source != quota.ResponseBindingSource(s.cfg.Get(), a) {
		t.Fatal("pool observation did not use independent route")
	}
	if _, err := s.thCheckSubscription(context.Background(), row.ID); err != nil {
		t.Fatal(err)
	}
	if regCalls.Load() != 1 || poolCalls.Load() != 3 {
		t.Fatal("registration observation used pool route")
	}
	// A failed pool route must not reuse registration egress or fall back direct.
	proxy.Close()
	if err := s.quota.RefreshNow(context.Background(), a); err == nil {
		t.Fatal("failed proxy silently fell back")
	}
	if regCalls.Load() != 1 || s.quota.View(a.ID).TokenHarbor.PlanFresh(s.quota.View(a.ID).TokenHarbor.CheckedAt) {
		t.Fatal("proxy failure reused old proof or registration route")
	}
}

func TestTHProxyChangeDiscardsLateObservation(t *testing.T) {
	for _, registration := range []bool{false, true} {
		t.Run(fmt.Sprint("registration=", registration), func(t *testing.T) {
			s := newTHTestServer(t)
			row := seedTHImported(t, s)
			entered, release := make(chan struct{}), make(chan struct{})
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/pricing" {
					close(entered)
					<-release
				}
				thProxyObservation(w, r)
			}))
			defer proxy.Close()
			_, err := s.cfg.Update(-1, func(c *config.Config) error {
				c.TokenHarbor.BaseURL = "http://tokenharbor.mock.invalid/v1"
				c.Accounts[0].ProxyURL = proxy.URL
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			a, _ := s.cfg.Get().Account(row.Account)
			done := make(chan error, 1)
			go func() { done <- s.quota.RefreshNow(context.Background(), a) }()
			<-entered
			if registration {
				store, _ := s.thStore()
				_, err = store.SetProxy(row.ID, "http://new-registration:8080")
			} else {
				_, err = s.cfg.Update(-1, func(c *config.Config) error { c.Accounts[0].ProxyURL = "http://new-pool:8080"; return nil })
			}
			close(release)
			<-done
			if err != nil {
				t.Fatal(err)
			}
			q := s.quota.View(a.ID)
			if q != nil && q.TokenHarbor != nil && q.TokenHarbor.PlanFresh(q.TokenHarbor.CheckedAt) {
				t.Fatal("late observation revived old source")
			}
		})
	}
}
