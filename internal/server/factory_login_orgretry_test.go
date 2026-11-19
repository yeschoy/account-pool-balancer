package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

// Factory refuses a token that is not bound to an organization. The login must
// re-mint it against a known organization instead of failing the approval.
func TestFactoryLoginRetriesWhoamiWithOrganizations(t *testing.T) {
	e := newFactoryEnv(t)
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Accounts = append(c.Accounts, config.Account{ID: "known", Provider: config.ProviderFactory, Group: "factory",
			RuntimeKey: "fk-existing_key_1", FactoryRefreshToken: "r-existing", FactoryWorkOSOrgID: "org-known"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var refreshedOrg atomic.Value
	mux := http.NewServeMux()
	mux.HandleFunc("/user_management/authorize/device", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"device_code": "d", "user_code": "AAAA-BBBB", "verification_uri": "https://auth.example/device", "expires_in": 30, "interval": 1})
	})
	mux.HandleFunc("/user_management/authenticate", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("grant_type") == "refresh_token" {
			refreshedOrg.Store(r.FormValue("organization_id"))
			json.NewEncoder(w).Encode(map[string]any{
				"access_token":           factoryLoginJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "sub": "user-y", "email": "org@example.com", "org_id": "org-known"}),
				"refresh_token":          "r-rotated",
				"active_organization_id": "org-known",
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  factoryLoginJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "sub": "user-y", "email": "org@example.com"}),
			"refresh_token": "r-initial",
		})
	})
	mux.HandleFunc("/user_management/organizations", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "org-known"}}})
	})
	workos := httptest.NewServer(mux)
	defer workos.Close()
	e.s.factoryAuthBase = workos.URL + "/user_management"

	var whoamiCalls atomic.Int32
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/cli/whoami" {
			whoamiCalls.Add(1)
			if r.Header.Get("X-Factory-Org-Id") == "org-known" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"userId":"user-y","orgId":"org-known","email":"org@example.com"}`))
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"detail":"User not affiliated with an organization","status":401}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}
	e.mu.Unlock()

	w := deployAdmin(t, e, "POST", "/api/factory/login", map[string]any{"group": "factory"})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var flow factoryLoginFlow
	_ = json.Unmarshal(w.Body.Bytes(), &flow)
	deadline := time.Now().Add(10 * time.Second)
	for flow.Status == "pending" && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		w = deployAdmin(t, e, "GET", "/api/factory/login/"+flow.ID, nil)
		_ = json.Unmarshal(w.Body.Bytes(), &flow)
	}
	if flow.Status != "approved" {
		t.Fatalf("expected approval after org retry, got %+v", flow)
	}
	if got, _ := refreshedOrg.Load().(string); got != "org-known" {
		t.Fatalf("retried with organization %q", got)
	}
	if whoamiCalls.Load() < 2 {
		t.Fatal("whoami was not retried")
	}
	a, ok := e.s.cfg.Get().Account(flow.AccountID)
	if !ok || a.FactoryWorkOSOrgID != "org-known" || a.FactoryRefreshToken != "r-rotated" {
		t.Fatalf("account not bound to the retried organization: %+v", a)
	}
	if !strings.Contains(flow.Email, "org@example.com") {
		t.Fatalf("email not recorded: %+v", flow)
	}
}
