package server

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
)

func TestOpenCodePinnedFallbackCannotExpandPrimaryAttemptBudget(t *testing.T) {
	e := newOCEnv(t, false)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		zen, goRouting := c.Routing, c.Routing
		zen.MaxAccounts = 1
		goRouting.MaxAccounts = 5
		c.OpenCode.ZenRouting = &zen
		c.OpenCode.GoRouting = &goRouting
		c.OpenCode.ZenFallbackGroup = config.ProviderOpenCodeGo
		c.Accounts = append(c.Accounts, config.Account{ID: "go2", Provider: config.ProviderOpenCodeGo, RuntimeKey: "second-go-key-123", Enabled: true})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := e.s.cfg.Get()
	a, _ := cfg.Account("go2")
	if err := e.s.quota.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	fingerprint := e.s.fingerprint(config.ProviderOpenCodeZen+"\x00"+config.ProviderOpenCodeZen, "session", "session-budget")
	e.s.pool.Bind(cfg.ForProvider(config.ProviderOpenCodeGo), fingerprint, "go1")
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") == "Bearer opaque-go-secret-key" {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"error":{"message":"temporary"}}`)
			return
		}
		fmt.Fprint(w, ocReply(opencode.Chat))
	}
	status, b, _ := e.post(t, config.ProviderOpenCodeZen, "/v1/chat/completions", ocRequest(opencode.Chat, "chat", false), map[string]string{"X-Session-Id": "session-budget"})
	if status < 500 || e.count() != 1 {
		t.Fatalf("fallback expanded original attempt budget: %d %s calls=%d", status, b, e.count())
	}
}
