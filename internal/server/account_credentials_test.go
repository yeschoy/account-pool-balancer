package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
)

func TestAccountCredentialsRequireAdminAndRemainUncached(t *testing.T) {
	e := newMistralEnv(t)
	const proxy = "http://local-user:local-password@127.0.0.1:9"
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Accounts[0].ProxyURL = proxy
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cookie := httptest.NewRecorder()
	e.s.admin.create(cookie, httptest.NewRequest("POST", "/api/login", nil))
	for _, path := range []string{"/api/accounts?provider=mistral", "/api/accounts/m1/keys"} {
		for _, authenticated := range []bool{false, true} {
			r := httptest.NewRequest("GET", path, nil)
			r.Header.Set("Authorization", "Bearer mistral-client") // runtime key grants no admin access
			if authenticated {
				for _, c := range cookie.Result().Cookies() {
					r.AddCookie(c)
				}
			}
			w := httptest.NewRecorder()
			e.s.Handler().ServeHTTP(w, r)
			if !authenticated {
				if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), "mstrl_mock_secret_1") || strings.Contains(w.Body.String(), proxy) {
					t.Fatal("non-administrator obtained upstream credentials")
				}
				continue
			}
			if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("administrator credential read: %d", w.Code)
			}
			var data any
			if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(data)
			if !strings.Contains(string(b), "mstrl_mock_secret_1") || !strings.Contains(string(b), proxy) {
				t.Fatal("saved Key/proxy was masked instead of displayed")
			}
		}
	}
}
