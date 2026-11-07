package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/thsignupstore"
)

func TestTHKeysExplicitReadOnlyAndPrivate(t *testing.T) {
	s := newTHTestServer(t)
	row := seedTHImported(t, s)
	store, _ := s.thStore()
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
	}))
	defer up.Close()
	if _, err := s.cfg.Update(-1, func(c *config.Config) error {
		c.TokenHarbor.BaseURL = up.URL + "/v1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg := s.cfg.Get().Clone()
	cookie := savedSession(t, s)
	get := func(path string, authenticated bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if authenticated {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}
	for _, path := range []string{"/api/accounts/" + row.Account + "/keys", "/api/tokenharbor/registrations/" + row.ID + "/keys"} {
		if w := get(path, false); w.Code != 401 || strings.Contains(w.Body.String(), row.APIKey) {
			t.Fatal("key read did not require administrator authentication")
		}
		w := get(path, true)
		var out map[string]string
		registrationRead := strings.HasPrefix(path, "/api/tokenharbor/")
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &out) != nil || out["runtimeKey"] != row.APIKey || registrationRead && len(out) != 1 || !registrationRead && len(out) != 3 {
			t.Fatal("administrator read must keep its account/registration credential scope and no-store", w.Code)
		}
		for _, secret := range []string{row.Password, row.Cookies[0].Value, row.Checkout.URL, row.MailboxURL} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("explicit key read leaked another credential")
			}
		}
	}
	for _, path := range []string{"/api/accounts?provider=tokenharbor", "/api/tokenharbor/registrations", "/api/tokenharbor/registrations/" + row.Attempt, "/api/tokenharbor/log"} {
		w := get(path, true)
		accountList := strings.HasPrefix(path, "/api/accounts?")
		if w.Code != 200 || strings.Contains(w.Body.String(), row.APIKey) != accountList || strings.Contains(w.Body.String(), row.Password) || strings.Contains(w.Body.String(), row.Cookies[0].Value) {
			t.Fatal("administrator account listing/registration secrecy scope is wrong", path, w.Code)
		}
	}
	for _, status := range []thsignupstore.Status{thsignupstore.Unverified, thsignupstore.Failed, thsignupstore.Uncertain} {
		before, err := store.Update(row.ID, func(r *thsignupstore.Row) error { r.Status = status; return nil })
		if err != nil {
			t.Fatal(err)
		}
		if w := get("/api/tokenharbor/registrations/"+row.ID+"/keys", true); w.Code != 200 {
			t.Fatal("existing key must remain readable without resuming incomplete registration")
		}
		after, _ := store.Get(row.ID)
		if !reflect.DeepEqual(before, after) {
			t.Fatal("key read changed registration progress")
		}
	}
	if _, err := store.Update(row.ID, func(r *thsignupstore.Row) error { r.APIKey = ""; r.APIKeyCreationAttempted = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if w := get("/api/tokenharbor/registrations/"+row.ID+"/keys", true); w.Code != 409 {
		t.Fatal("missing/uncertain key must not be recreated")
	}
	for _, path := range []string{"/api/tokenharbor/registrations/missing/keys", "/api/accounts/missing/keys"} {
		if w := get(path, true); w.Code != 404 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing key source must fail without caching")
		}
	}
	if calls.Load() != 0 || !reflect.DeepEqual(cfg, s.cfg.Get()) {
		t.Fatal("key read performed upstream work or changed the pool")
	}
}

func TestAdminSavedKeyReadIncludesOpenCode(t *testing.T) {
	s := newTHTestServer(t)
	if _, err := s.cfg.Update(-1, func(c *config.Config) error {
		for _, p := range []string{config.ProviderOpenCodeZen, config.ProviderOpenCodeGo} {
			c.Accounts = append(c.Accounts, config.Account{ID: p, Provider: p, Name: p, RuntimeKey: "mock-private-" + p, Group: p})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{config.ProviderOpenCodeZen, config.ProviderOpenCodeGo} {
		req := httptest.NewRequest("GET", "/api/accounts/"+p+"/keys", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: savedSession(t, s)})
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "mock-private-"+p) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("administrator cannot read the user-authorized OpenCode saved key")
		}
	}
}
