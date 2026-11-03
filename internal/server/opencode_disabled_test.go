package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
)

func TestOpenCodeDisabledMakesNoUpstreamCalls(t *testing.T) {
	e := newOCEnv(t, false)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error { c.OpenCode.Enabled = false; return nil })
	if err != nil {
		t.Fatal(err)
	}
	before := e.upCalls.Load()
	a, _ := e.s.cfg.Get().Account("go1")
	if err := e.s.quota.RefreshNow(context.Background(), a); err == nil {
		t.Fatal("disabled refresh reported success")
	}
	e.s.testOpenCode(context.Background(), e.s.cfg.Get(), a, true, "chat")
	r := httptest.NewRequest(http.MethodPost, "/api/accounts/go1/test-proxy", nil)
	r.SetPathValue("id", a.ID)
	w := httptest.NewRecorder()
	e.s.apiOpenCodeProxyTest(w, r)
	if w.Code != 409 {
		t.Fatal("disabled diagnostic not rejected")
	}
	status, _, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", ocRequest(opencode.Chat, "chat", false), nil)
	if status != 503 {
		t.Fatalf("disabled inference = %d", status)
	}
	if e.upCalls.Load() != before {
		t.Fatal("feature rollback still contacted upstream")
	}
}
