package server

import (
	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/rules"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func newFactoryEnv(t *testing.T) *deployTestEnv {
	e := newDeployEnv(t)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Factory.BaseURL = e.up.URL
		r := c.Factory.EffectiveRouting()
		r.Strategy = "ordered"
		r.MaxAccounts = 2
		c.Factory.Routing = &r
		q := c.Factory.EffectiveQuota()
		q.Enabled = false
		c.Factory.Quota = &q
		c.Accounts = []config.Account{{ID: "f1", Provider: config.ProviderFactory, Group: "factory", RuntimeKey: "fk-mock_factory_key_1", Enabled: true, Priority: 1, MaxConc: 4}, {ID: "f2", Provider: config.ProviderFactory, Group: "factory", RuntimeKey: "fk-mock_factory_key_2", Enabled: true, Priority: 2, MaxConc: 4}}
		c.ClientKeys = []config.ClientKey{{ID: "client", Provider: config.ProviderFactory, Group: "factory", KeyHash: config.HashKey("downstream-ms"), Enabled: true}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.s.stopAltAuto)
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/cli/whoami":
			fmt.Fprint(w, `{"userId":"test-user","orgId":"test-org"}`)
		case "/api/billing/limits":
			w.WriteHeader(403)
			fmt.Fprint(w, `{"detail":"not allowed"}`)
		case "/api/llm/o/v1/responses":
			fmt.Fprint(w, `{"id":"resp_factory_1","object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}]}`)
		case "/api/llm/a/v1/messages":
			fmt.Fprint(w, `{"type":"message","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn"}`)
		default:
			fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
		}
	}
	e.mu.Unlock()
	return e
}
func TestFactoryNativeProtocolsAndNoRewrite(t *testing.T) {
	e := newFactoryEnv(t)
	cases := []struct{ path, model, target, provider string }{{"/v1/chat/completions", "deepseek-v4.1-flash", "/api/llm/o/v1/chat/completions", "fireworks"}, {"/v1/responses", "gpt-5.4", "/api/llm/o/v1/responses", "openai"}, {"/v1/messages", "claude-haiku-4-5-20251001", "/api/llm/a/v1/messages", "anthropic"}}
	for _, v := range cases {
		body := fmt.Sprintf(`{ "model":%q,"messages":[{"role":"system","content":"Original user instructions"},{"role":"user","content":"test"}],"tools":[{"unknown":9007199254740993}] }`, v.model)
		rsp, out := e.do(t, v.path, body, map[string]string{"X-Api-Key": "downstream-secret", "X-Factory-Client": "cli", "X-Client-Version": "spoof", "X-Factory-Org-Id": "other-org"})
		if rsp.StatusCode != 200 {
			t.Fatal(v.path, rsp.StatusCode, out)
		}
		calls := e.snapshot()
		c := calls[len(calls)-1]
		if c.path != v.target || c.body != body || c.auth != "Bearer fk-mock_factory_key_1" || c.apiKey != "" || c.header.Get("X-Api-Provider") != v.provider || c.header.Get("X-Factory-Client") != "" || c.header.Get("X-Factory-Org-Id") != "" {
			t.Fatal("native body/root/auth changed", v.path)
		}
	}
	before := len(e.snapshot())
	for _, path := range []string{"/v1/embeddings", "/v1/messages/extra", "/v1/responses/extra"} {
		rsp, _ := e.do(t, path, `{"model":"deepseek-v4.1-flash"}`, nil)
		if rsp.StatusCode != 404 {
			t.Fatal("unsupported accepted", path)
		}
	}
	rsp, _ := e.do(t, "/v1/chat/completions", `{"model":"gpt-5.4"}`, nil)
	if rsp.StatusCode != 400 || len(e.snapshot()) != before {
		t.Fatal("wrong protocol reached upstream")
	}
}
func TestFactoryPolicyCannotRotateOrChangeHealth(t *testing.T) {
	e := newFactoryEnv(t)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.FactoryErrorRules = []rules.ErrorRule{{ID: "force-switch", Scope: rules.ScopeAccount, Action: rules.ActionSwitchAccount, When: rules.Match{Statuses: []int{403}}}}
		c.FactoryRetryRules = []rules.RetryRule{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		fmt.Fprint(w, `{"code":"content_policy_rejected","detail":"This request was blocked by Factory's usage policy check."}`)
	}
	e.mu.Unlock()
	rsp, out := e.do(t, "/v1/chat/completions", `{"model":"deepseek-v4.1-flash","messages":[]}`, nil)
	if rsp.StatusCode != 403 || len(e.snapshot()) != 1 {
		t.Fatal("policy rotated", rsp.StatusCode, out)
	}
	v := e.s.pool.Views(e.s.cfg.Get())["f1"]
	if v.Status != "ok" || v.Samples != 0 {
		t.Fatal("policy degraded credential", v)
	}
}
func TestFactoryManagementRoundTripAndQuotaPermissions(t *testing.T) {
	e := newFactoryEnv(t)
	before := e.s.cfg.Get().Clone()
	w := deployAdmin(t, e, "GET", "/api/settings?provider=factory", nil)
	var doc struct {
		Settings settingsDoc `json:"settings"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &doc) != nil {
		t.Fatal("settings GET")
	}
	d := doc.Settings
	d.Routing.Strategy = "weighted"
	d.Routing.ExplicitTTLSec = 180
	d.Factory.DetailLog = true
	d.Quota.Enabled = false
	d.ErrorRules = []rules.ErrorRule{}
	d.RetryRules = []rules.RetryRule{}
	w = deployAdmin(t, e, "PUT", "/api/settings?provider=factory", d)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	got := e.s.cfg.Get()
	if got.Factory.EffectiveRouting().Strategy != "weighted" || !got.Factory.DetailLog || len(got.FactoryErrorRules) != 0 {
		t.Fatal("settings not applied")
	}
	if !reflect.DeepEqual(before.Deploy, got.Deploy) || !reflect.DeepEqual(before.Routing, got.Routing) || !reflect.DeepEqual(before.Logs, got.Logs) {
		t.Fatal("cross pool settings")
	}
	if err := e.s.Reload(); err != nil {
		t.Fatal(err)
	}
	if e.s.cfg.Get().Factory.EffectiveRouting().ExplicitTTLSec != 180 {
		t.Fatal("reload lost value")
	}
	w = deployAdmin(t, e, "POST", "/api/accounts/f1/refresh-quota", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"quotaRefreshed":false`) || !strings.Contains(w.Body.String(), `unsupported-credential`) {
		t.Fatal("quota permission hidden", w.Code, w.Body.String())
	}
	w = deployAdmin(t, e, "POST", "/api/accounts/f1/test", map[string]any{"inference": true})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatal("test", w.Code, w.Body.String())
	}
	if err := e.s.saveMetadata(true); err != nil {
		t.Fatal(err)
	}
	loaded, err := New(e.s.dataDir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { loaded.stopAltAuto(); loaded.stopPerch(); loaded.logs.Close() })
	if q := loaded.quota.Get("f1"); q == nil || q.Factory == nil || q.Factory.QuotaState != "unsupported-credential" {
		t.Fatal("persisted quota missing")
	}
}
