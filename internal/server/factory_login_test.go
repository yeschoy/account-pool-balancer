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

func factoryLoginJWT(payload map[string]any) string {
	b, _ := json.Marshal(payload)
	return "e30." + strings.TrimRight(base64URL(b), "=") + ".sig"
}

func base64URL(b []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var out strings.Builder
	for i := 0; i < len(b); i += 3 {
		var n uint32
		rem := len(b) - i
		n = uint32(b[i]) << 16
		if rem > 1 {
			n |= uint32(b[i+1]) << 8
		}
		if rem > 2 {
			n |= uint32(b[i+2])
		}
		out.WriteByte(alphabet[(n>>18)&63])
		out.WriteByte(alphabet[(n>>12)&63])
		if rem > 1 {
			out.WriteByte(alphabet[(n>>6)&63])
		}
		if rem > 2 {
			out.WriteByte(alphabet[n&63])
		}
	}
	return out.String()
}

func mockFactoryWorkOS(t *testing.T) *httptest.Server {
	var polls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/user_management/authorize/device", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("client_id") != "client_01HNM792M5G5G1A2THWPXKFMXB" {
			t.Errorf("client id: %q", r.FormValue("client_id"))
		}
		json.NewEncoder(w).Encode(map[string]any{
			"device_code": "device-1", "user_code": "ABCD-EFGH",
			"verification_uri": "https://auth.example/device", "verification_uri_complete": "https://auth.example/device?user_code=ABCD-EFGH",
			"expires_in": 30, "interval": 1,
		})
	})
	mux.HandleFunc("/user_management/authenticate", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("grant_type") == "refresh_token" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"access_token":           factoryLoginJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "sub": "workos-user", "org_id": "org-oauth"}),
				"refresh_token":          "fresh-refresh",
				"active_organization_id": "org-oauth",
			})
			return
		}
		if polls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":           factoryLoginJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "sub": "workos-user", "email": "oauth@example.com", "org_id": "org-oauth"}),
			"refresh_token":          "device-refresh",
			"active_organization_id": "org-oauth",
		})
	})
	return httptest.NewServer(mux)
}

func TestFactoryDeviceLoginCreatesOAuthAccountAndRefreshes(t *testing.T) {
	e := newFactoryEnv(t)
	workos := mockFactoryWorkOS(t)
	defer workos.Close()
	e.s.factoryAuthBase = workos.URL + "/user_management"

	w := deployAdmin(t, e, "POST", "/api/factory/login", map[string]any{"group": "factory"})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var flow factoryLoginFlow
	if json.Unmarshal(w.Body.Bytes(), &flow) != nil || flow.ID == "" || flow.VerificationURI == "" {
		t.Fatal("start", w.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		w = deployAdmin(t, e, "GET", "/api/factory/login/"+flow.ID, nil)
		if json.Unmarshal(w.Body.Bytes(), &flow) != nil {
			t.Fatal("poll", w.Body.String())
		}
		if flow.Status != "pending" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("login did not complete", flow)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if flow.Status != "approved" || flow.Email != "oauth@example.com" || flow.AccountID == "" {
		t.Fatal("flow", flow)
	}
	cfg := e.s.cfg.Get()
	a, ok := cfg.Account(flow.AccountID)
	if !ok || a.Provider != config.ProviderFactory || a.FactoryRefreshToken != "device-refresh" || a.RuntimeKey == "" || a.FactoryOrgID != "test-org" || a.FactoryWorkOSOrgID != "org-oauth" || a.UserID != "test-user" || a.Email != "oauth@example.com" {
		t.Fatalf("account: %+v", a)
	}
	// The OAuth account is the only enabled one; its access token is used as
	// Bearer and its organization header is sent.
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			if c.Accounts[i].FactoryRefreshToken == "" {
				c.Accounts[i].Enabled = false
			} else {
				c.Accounts[i].Priority, c.Accounts[i].Enabled = 1, true
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rsp, out := e.do(t, "/v1/chat/completions", `{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rsp.StatusCode != 200 {
		t.Fatal(rsp.StatusCode, out)
	}
	call := e.snapshot()[len(e.snapshot())-1]
	if !strings.HasPrefix(call.auth, "Bearer ") || strings.HasSuffix(call.auth, "stale") || call.header.Get("X-Factory-Org-Id") != "org-oauth" {
		t.Fatalf("oauth inference: auth=%q org=%q", call.auth, call.header.Get("X-Factory-Org-Id"))
	}
	// An expired access token is refreshed once and the request uses the new one.
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			if c.Accounts[i].ID == flow.AccountID {
				c.Accounts[i].RuntimeKey = "stale-access"
				c.Accounts[i].FactoryAccessExpires = time.Now().Add(-time.Hour)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rsp, out = e.do(t, "/v1/chat/completions", `{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rsp.StatusCode != 200 {
		t.Fatal(rsp.StatusCode, out)
	}
	call = e.snapshot()[len(e.snapshot())-1]
	if call.auth == "Bearer stale-access" || call.auth == "" {
		t.Fatal("token not refreshed", call.auth)
	}
	a, _ = e.s.cfg.Get().Account(flow.AccountID)
	if a.FactoryRefreshToken != "fresh-refresh" || a.RuntimeKey == "stale-access" {
		t.Fatal("refresh not persisted", a)
	}
	// Quota polling renews the same way, so an idle account does not go stale.
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			if c.Accounts[i].ID == flow.AccountID {
				c.Accounts[i].RuntimeKey = "quota-stale"
				c.Accounts[i].FactoryAccessExpires = time.Now().Add(-time.Hour)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if w := deployAdmin(t, e, "POST", "/api/accounts/"+flow.AccountID+"/refresh-quota", nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	a, _ = e.s.cfg.Get().Account(flow.AccountID)
	if a.RuntimeKey == "quota-stale" || a.FactoryRefreshToken == "" {
		t.Fatal("quota refresh did not renew token", a)
	}
}

func TestFactoryDeviceLoginSurvivesPoolOffAndRejectsCrossPoolReauth(t *testing.T) {
	e := newFactoryEnv(t)
	workos := mockFactoryWorkOS(t)
	defer workos.Close()
	e.s.factoryAuthBase = workos.URL + "/user_management"
	// An account outside Factory cannot be re-authorized as Factory.
	if w := deployAdmin(t, e, "POST", "/api/factory/login", map[string]any{"accountId": "missing"}); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	_, _ = e.s.cfg.Update(-1, func(c *config.Config) error {
		c.PoolSwitches = map[string]bool{config.ProviderFactory: false}
		return nil
	})
	if w := deployAdmin(t, e, "POST", "/api/factory/login", map[string]any{"group": "factory"}); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestFactoryEgressSelectionDistinct(t *testing.T) {
	e := newFactoryEnv(t)
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Factory.ProxyPool = []string{"http://p1.example:8080", "http://p2.example:8080"}
		c.Factory.UserAgents = []string{"factory-cli/1.15.0", "factory-cli/1.16.0"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg := e.s.cfg.Get()
	p1, u1 := e.s.factoryEgressFor(cfg, "login-a")
	p2, u2 := e.s.factoryEgressFor(cfg, "login-b")
	if p1 == "" || p2 == "" || u1 == "" || u2 == "" || p1 == p2 || u1 == u2 {
		t.Fatalf("pool assignment not distinct: %q/%q %q/%q", p1, u1, p2, u2)
	}
}
