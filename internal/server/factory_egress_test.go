package server

import (
	"encoding/json"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
)

func TestFactoryProxyAndUAPoolAssignmentOnCreate(t *testing.T) {
	e := newFactoryEnv(t)
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Factory.ProxyPool = []string{"http://p1.example:8080", "http://p2.example:8080"}
		c.Factory.UserAgents = []string{"factory-cli/1.15.0", "factory-cli/1.16.0"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i, key := range []string{"fk-pool_one", "fk-pool_two"} {
		w := deployAdmin(t, e, "POST", "/api/accounts", map[string]any{"provider": "factory", "runtimeKey": key, "name": "pool-" + key})
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var out struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.ID == "" {
			t.Fatal(w.Body.String())
		}
		a, _ := e.s.cfg.Get().Account(out.ID)
		wantProxy := []string{"http://p1.example:8080", "http://p2.example:8080"}[i]
		wantUA := []string{"factory-cli/1.15.0", "factory-cli/1.16.0"}[i]
		if a.ProxyURL != wantProxy || a.UserAgent != wantUA {
			t.Fatalf("account %d: proxy=%q ua=%q", i, a.ProxyURL, a.UserAgent)
		}
	}
}

func TestFactoryAssignEgressExistingAccountsAndUAHeader(t *testing.T) {
	e := newFactoryEnv(t)
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Factory.ProxyPool = []string{"http://p1.example:8080", "http://p2.example:8080"}
		c.Factory.UserAgents = []string{"factory-cli/1.15.0", "factory-cli/1.16.0"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w := deployAdmin(t, e, "POST", "/api/factory/assign-egress", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"proxyAssigned":2`) || !strings.Contains(w.Body.String(), `"uaAssigned":2`) {
		t.Fatal(w.Code, w.Body.String())
	}
	cfg := e.s.cfg.Get()
	for _, id := range []string{"f1", "f2"} {
		a, _ := cfg.Account(id)
		if a.ProxyURL == "" || a.UserAgent == "" {
			t.Fatalf("%s not assigned: %+v", id, a)
		}
	}
	// A per-account UA drives both the UA header and the matching client
	// version; a non-factory-cli UA keeps droid's captured version.
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			if c.Accounts[i].ID == "f1" {
				c.Accounts[i].UserAgent = "factory-cli/1.15.0"
				c.Accounts[i].ProxyURL = ""
				c.Accounts[i].Enabled = true
			}
			if c.Accounts[i].ID == "f2" {
				c.Accounts[i].Enabled = false
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, out := e.do(t, "/v1/chat/completions", `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`, nil)
	if !strings.Contains(out, "OK") {
		t.Fatal(out)
	}
	h := e.snapshot()[0].header
	if h.Get("User-Agent") != "factory-cli/1.15.0" || h.Get("X-Client-Version") != "1.15.0" {
		t.Fatalf("ua headers: %v", h)
	}
}
