package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
)

func TestFingerprintProxySourceAndRedaction(t *testing.T) {
	for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(name, "env-secret-value")
	}
	store, err := config.Load(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(-1, func(c *config.Config) error {
		c.Accounts = []config.Account{
			{ID: "with-proxy", Name: "One", Provider: config.ProviderPerch, RuntimeKey: config.PerchRuntimeKey("one"), PerchUserID: "one", ProxyURL: "http://proxy-user:proxy-password@proxy.example:8080/private?token=proxy-query"},
			{ID: "without-proxy", Name: "Two", Provider: config.ProviderPerch, RuntimeKey: config.PerchRuntimeKey("two"), PerchUserID: "two"},
		}
		c.Perch.PostHogHost = "https://endpoint.example/path?token=endpoint-secret"
		c.Perch.AppURL = "https://endpoint.example/path?token=app-secret"
		c.Perch.PostHogKey = "posthog-secret"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: store}
	w := httptest.NewRecorder()
	s.apiPerchFingerprint(w, httptest.NewRequest(http.MethodGet, "/api/perch/fingerprint", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var out struct {
		Accounts []struct {
			ID          string `json:"id"`
			ProxySource string `json:"proxySource"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Accounts) != 2 || out.Accounts[0].ProxySource != "account_proxy" || out.Accounts[1].ProxySource != "unconfigured_unknown" {
		t.Fatalf("unexpected proxy sources: %+v", out.Accounts)
	}
	for _, secret := range []string{"proxy-user", "proxy-password", "proxy.example", "proxy-query", "env-secret-value", "endpoint-secret", "app-secret", "posthog-secret"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("fingerprint view exposed %q", secret)
		}
	}
}
