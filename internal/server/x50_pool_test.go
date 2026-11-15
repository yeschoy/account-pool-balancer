package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/x50signupstore"
)

func newX50PoolEnv(t *testing.T) *deployTestEnv {
	e := newDeployEnv(t)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		r := c.X50.EffectiveRouting()
		r.Strategy = "ordered"
		r.MaxAccounts = 2
		c.X50.Routing = &r
		q := c.X50.EffectiveQuota()
		q.Enabled = false
		c.X50.Quota = &q
		c.X50.BaseURL = e.up.URL + "/v1"
		c.Accounts = append(c.Accounts, config.Account{ID: "x1", Provider: config.ProviderX50, Group: "x50", RuntimeKey: "sk-50x-mock1", ControlKey: "mock-session1", Enabled: true, Priority: 1}, config.Account{ID: "x2", Provider: config.ProviderX50, Group: "x50", RuntimeKey: "sk-50x-mock2", ControlKey: "mock-session2", Enabled: true, Priority: 2})
		c.ClientKeys = []config.ClientKey{{ID: "xclient", Provider: config.ProviderX50, Group: "x50", KeyHash: config.HashKey("downstream-ms"), Enabled: true}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/billing" {
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Token ") {
				t.Error("billing auth")
			}
			fmt.Fprint(w, `{"balance_usd":"37.25","tier":"paid"}`)
			return
		}
		if r.URL.Path == "/v1/models" {
			fmt.Fprintf(w, `{"data":[{"id":%q}]}`, config.X50DefaultModel)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"mock-ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}
	e.mu.Unlock()
	return e
}
func TestX50PoolImportIdempotentPartialDeleteAndProxyScope(t *testing.T) {
	e := newX50PoolEnv(t)
	st, err := e.s.x50Store()
	if err != nil {
		t.Fatal(err)
	}
	good, _ := st.Add(x50signupstore.Row{Email: "fake@example.test", Status: x50signupstore.Keyed, APIKey: "sk-50x-new", SessionToken: "mock-management", Password: "mock-password", ProxyURL: "http://127.0.0.1:1", MailboxURL: "https://invalid.test/?token=mock"})
	pending, _ := st.Add(x50signupstore.Row{Email: "pending@example.test"})
	saved := st.Snapshot()
	cfgBefore := e.s.cfg.Get()
	foreign := cfgBefore.Accounts[0]
	w := deployAdmin(t, e, "POST", "/api/x50/pool/import", map[string]any{"ids": []string{good.ID, pending.ID, "missing", good.ID}})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Added   int
		Results []struct {
			ID, AccountID, Error string
			Already              bool
		}
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	if result.Added != 1 || len(result.Results) != 3 {
		t.Fatal(w.Body.String())
	}
	target := x50AccountFor(e.s.cfg.Get(), good.APIKey)
	a, _ := e.s.cfg.Get().Account(target)
	if target == "" || a.ControlKey != good.SessionToken || a.ProxyURL != good.ProxyURL {
		t.Fatal("import not durable")
	}
	if strings.Contains(w.Body.String(), good.APIKey) || strings.Contains(w.Body.String(), good.SessionToken) {
		t.Fatal("import leaked credentials")
	}
	repeat := deployAdmin(t, e, "POST", "/api/x50/pool/import", map[string]any{"ids": []string{good.ID}})
	if !strings.Contains(repeat.Body.String(), `"already":true`) {
		t.Fatal(repeat.Body.String())
	}
	w = deployAdmin(t, e, "GET", "/api/x50/candidates", nil)
	if !strings.Contains(w.Body.String(), `"inPool":true`) {
		t.Fatal(w.Body.String())
	}
	// Must reject mixed selections atomically, missing confirmation and malformed proxy.
	revision := e.s.cfg.Get().Revision
	for _, in := range []map[string]any{
		{"scope": "selected", "ids": []string{target, foreign.ID}, "proxyUrl": "", "confirm": "x50"},
		{"scope": "all", "proxyUrl": ""},
		{"scope": "selected", "ids": []string{target}, "proxyUrl": "garbage", "confirm": "x50"},
	} {
		w = deployAdmin(t, e, "POST", "/api/x50/pool/proxy", in)
		if w.Code < 400 || e.s.cfg.Get().Revision != revision {
			t.Fatal("unsafe proxy update", w.Code)
		}
	}
	w = deployAdmin(t, e, "POST", "/api/x50/pool/proxy", map[string]any{"scope": "all", "proxyUrl": "", "confirm": "x50"})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, a := range e.s.cfg.Get().Accounts {
		if a.Provider == config.ProviderX50 && a.ProxyURL != "" {
			t.Fatal("not direct")
		}
	}
	gotForeign, _ := e.s.cfg.Get().Account(foreign.ID)
	if !reflect.DeepEqual(foreign, gotForeign) || !reflect.DeepEqual(saved, st.Snapshot()) {
		t.Fatal("foreign pool or registration damaged")
	}
	// Disk and a newly opened registration store agree, no credentials/history lost.
	raw, _ := os.ReadFile(filepath.Join(e.s.dataDir, "config.json"))
	var disk config.Config
	json.Unmarshal(raw, &disk)
	da, _ := disk.Account(target)
	if da.ProxyURL != "" {
		t.Fatal("disk proxy not direct")
	}
	restored, err := x50signupstore.Open(filepath.Join(e.s.dataDir, "x50-signup.json"))
	if err != nil || !reflect.DeepEqual(restored.Snapshot(), saved) {
		t.Fatal("registration restart differs", err)
	}
	w = deployAdmin(t, e, "DELETE", "/api/accounts/"+target, nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = deployAdmin(t, e, "GET", "/api/x50/candidates", nil)
	if strings.Contains(w.Body.String(), `"inPool":true`) {
		t.Fatal("deleted target still imported")
	}
	w = deployAdmin(t, e, "POST", "/api/x50/pool/import", map[string]any{"ids": []string{good.ID}})
	if !strings.Contains(w.Body.String(), `"added":1`) {
		t.Fatal("reimport failed", w.Body.String())
	}
}
func TestX50SettingsQuotaNativeAndIsolation(t *testing.T) {
	e := newX50PoolEnv(t)
	before := e.s.cfg.Get().Clone()
	w := deployAdmin(t, e, "GET", "/api/settings?provider=x50", nil)
	var doc struct{ Settings settingsDoc }
	json.Unmarshal(w.Body.Bytes(), &doc)
	in := doc.Settings
	in.Routing.Sticky = true
	in.Routing.SessionHeaders = []string{"X-X50-Session"}
	in.Routing.ExplicitTTLSec = 1234
	in.X50.DetailLog = new(bool)
	*in.X50.DetailLog = true
	in.ErrorRules[0].Note = "saved-x50-only"
	w = deployAdmin(t, e, "PUT", "/api/settings?provider=x50", in)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	after := e.s.cfg.Get()
	if !reflect.DeepEqual(before.Deploy, after.Deploy) || !reflect.DeepEqual(before.Routing, after.Routing) || !reflect.DeepEqual(before.Logs, after.Logs) || !reflect.DeepEqual(before.ALTAPI, after.ALTAPI) {
		t.Fatal("settings leaked")
	}
	if after.ForProvider("x50").Routing.ExplicitTTLSec != 1234 || after.X50ErrorRules[0].Note != "saved-x50-only" {
		t.Fatal("not saved")
	}
	raw, _ := os.ReadFile(filepath.Join(e.s.dataDir, "config.json"))
	var disk config.Config
	json.Unmarshal(raw, &disk)
	if disk.X50.Routing.ExplicitTTLSec != 1234 {
		t.Fatal("not on disk")
	}
	w = deployAdmin(t, e, "POST", "/api/accounts/x1/refresh-quota", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"quotaRefreshed":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = deployAdmin(t, e, "GET", "/api/accounts?provider=x50", nil)
	if strings.Contains(w.Body.String(), `"provider":"deploy"`) || !strings.Contains(w.Body.String(), `"balanceUsd":37.25`) {
		t.Fatal("view", w.Body.String())
	}
	body := fmt.Sprintf(`{ "model":%q, "messages":[{"role":"user","content":"ping"}], "tools":[{"type":"function","function":{"name":"tool","parameters":{"type":"object"}}}], "big":9007199254740993 }`, config.X50DefaultModel)
	rsp, _ := e.do(t, "/v1/chat/completions", body, map[string]string{"X-X50-Session": "private-session"})
	if rsp.StatusCode != 200 {
		t.Fatal(rsp.StatusCode)
	}
	found := false
	for _, c := range e.snapshot() {
		if c.path == "/v1/chat/completions" {
			found = true
			if c.body != body || !strings.HasPrefix(c.auth, "Bearer sk-50x-") {
				t.Fatal("native bytes/auth changed")
			}
		}
	}
	if !found {
		t.Fatal("no inference")
	}
	for _, p := range []string{"/v1/responses", "/v1/messages", "/v1/embeddings", "/v1/chat/completions/extra"} {
		rsp, _ := e.do(t, p, body, nil)
		if rsp.StatusCode != 404 {
			t.Fatal("unsupported", p, rsp.StatusCode)
		}
	}
	w = deployAdmin(t, e, "POST", "/api/accounts/x1/test", map[string]any{"inference": true})
	if w.Code != 200 || !strings.Contains(w.Body.String(), config.X50DefaultModel) {
		t.Fatal("default model test", w.Body.String())
	}
	w = deployAdmin(t, e, "POST", "/api/accounts/x1/test", map[string]any{"inference": true, "model": "manual/model"})
	if !strings.Contains(w.Body.String(), "manual/model") {
		t.Fatal("manual model ignored")
	}
	// Unknown send results and truncated output must not be replayed through custom rules.
	e.mu.Lock()
	e.calls = nil
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
	}
	e.mu.Unlock()
	rsp, _ = e.do(t, "/v1/chat/completions", fmt.Sprintf(`{"model":%q,"stream":true,"messages":[]}`, config.X50DefaultModel), nil)
	if len(e.snapshot()) != 1 {
		t.Fatal("truncated stream replayed")
	}
	time.Sleep(10 * time.Millisecond)
	logs := deployAdmin(t, e, "GET", "/api/logs/requests?provider=x50", nil)
	if logs.Code != 200 || !strings.Contains(logs.Body.String(), "incomplete_stream") || !strings.Contains(logs.Body.String(), `"result":"failed"`) {
		t.Fatal("truncated stream counted success", logs.Body.String())
	}
}
func TestX50AdminPermissions(t *testing.T) {
	e := newX50PoolEnv(t)
	for _, p := range []string{"/api/x50/pool/import", "/api/x50/pool/proxy"} {
		r := httptest.NewRequest("POST", p, strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal(p, w.Code)
		}
		login := httptest.NewRecorder()
		e.s.admin.create(login, httptest.NewRequest("POST", "/api/login", nil))
		for _, cookie := range login.Result().Cookies() {
			r.AddCookie(cookie)
		}
		w = httptest.NewRecorder()
		e.s.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("missing CSRF", p, w.Code)
		}
	}
}
