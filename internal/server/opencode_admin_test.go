package server

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
	"account-pool-balancer/internal/quota"
)

func TestOpenCodeExistingAdminImportAndWriteOnlyKeys(t *testing.T) {
	e := newOCEnv(t, false)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/accounts/import", strings.NewReader(`{"provider":"opencode-zen","text":"sk-overlapping-token,ZenNew\nsk-overlapping-token,ZenNew"}`))
	e.s.apiImportAccounts(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"added":1`) || !strings.Contains(w.Body.String(), `"unchanged":1`) {
		t.Fatalf("batch dedupe %d %s", w.Code, w.Body)
	}
	ik := config.ImportKey{ID: "ik", Provider: config.ProviderOpenCodeGo, Group: config.ProviderOpenCodeGo}
	result := e.s.importOne(ik, importItem{RuntimeKey: "opaque-import-token", Name: "go-import"}, time.Now())
	if result.Status != "added" {
		t.Fatalf("open import %+v", result)
	}
	again := e.s.importOne(ik, importItem{RuntimeKey: "opaque-import-token", Name: "go-import"}, time.Now())
	if again.Status != "unchanged" {
		t.Fatalf("open dedup %+v", again)
	}
	wrong := e.s.importOne(ik, importItem{RuntimeKey: "opaque-zen-token", Provider: config.ProviderOpenCodeZen}, time.Now())
	if wrong.Status != "error" {
		t.Fatal("import key changed tier")
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/accounts/go1/keys", nil)
	r.SetPathValue("id", "go1")
	e.s.apiAccountKeys(w, r)
	if w.Code != 403 || strings.Contains(w.Body.String(), "opaque-go-secret-key") {
		t.Fatal("upstream key readable in frontend API")
	}
	data, _ := json.Marshal(e.s.accountViews(e.s.cfg.Get()))
	if strings.Contains(string(data), "opaque-go-secret-key") || strings.Contains(string(data), "sk-zen-secret-key") {
		t.Fatal("account view leaks credentials")
	}
}

func TestOpenCodeModelsCatalogIsolationAndRefresh(t *testing.T) {
	e := newOCEnv(t, true)
	cfg := e.s.cfg.Get()
	a, _ := cfg.Account("zen1")
	b, _ := cfg.Account("zen2")
	qa := e.s.quota.Get(a.ID)
	qb := e.s.quota.Get(b.ID)
	qa.Catalog = map[string]bool{"chat": true}
	qb.Catalog = map[string]bool{"responses": true}
	e.s.quota.Load(map[string]*quota.Snapshot{a.ID: qa, b.ID: qb})
	e.s.resetModels()
	for _, model := range []string{"chat", "responses"} {
		status, body, _ := e.post(t, config.ProviderOpenCodeZen, "/v1"+opencode.Path(opencode.Protocol(model)), ocRequest(opencode.Protocol(model), model, false), nil)
		if status != 200 {
			t.Fatalf("catalog route %d %s", status, body)
		}
	}
	e.mu.Lock()
	keys := append([]string(nil), e.keys...)
	e.mu.Unlock()
	if keys[0] != a.RuntimeKey || keys[1] != b.RuntimeKey {
		t.Fatalf("catalog permissions crossed: %v", keys)
	}
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			if c.Accounts[i].ID == a.ID {
				c.Accounts[i].RuntimeKey = "replacement-zen-key"
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if q := e.s.quota.View(a.ID); q != nil && !q.CatalogAt.IsZero() {
		t.Fatal("old key catalog survived hot update")
	}
	if err := e.s.quota.RefreshNow(context.Background(), e.s.cfg.Get().Accounts[0]); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCodeCheckpointAndRestart(t *testing.T) {
	e := newOCEnv(t, false)
	status, b, _ := e.post(t, config.ProviderOpenCodeZen, "/v1/responses", ocRequest(opencode.Responses, "responses", false), nil)
	if status != 200 {
		t.Fatalf("%d %s", status, b)
	}
	if err := e.s.saveMetadata(true); err != nil {
		t.Fatal(err)
	}
	// Restore from a copied checkpoint, never open a second writer on live data.
	dir := t.TempDir()
	for _, name := range []string{"config.json", "metadata.json"} {
		b, err := os.ReadFile(filepath.Join(e.s.dataDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	restored, err := New(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restored.logs.Close)
	a, _ := restored.cfg.Get().Account("go1")
	if q := restored.quota.View(a.ID); q == nil || q.OpenCode == nil || q.OpenCode.Usage["rolling"].UsedPercent != 10 {
		t.Fatal("quota checkpoint lost")
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"responses","previous_response_id":"reply-1","input":"next"}`))
	r.Header.Set("Authorization", "Bearer downstream-opencode-zen")
	w := httptest.NewRecorder()
	restored.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("response chain not restored: %d %s", w.Code, w.Body)
	}
}

func TestOpenCodeFixedProxyFailureAndExplicitRecovery(t *testing.T) {
	e := newOCEnv(t, false)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	proxyURL := "http://" + addr
	saved := e.s.quota.Get("zen1")
	_, err = e.s.cfg.Update(-1, func(c *config.Config) error { c.Accounts[0].ProxyURL = proxyURL; return nil })
	if err != nil {
		t.Fatal(err)
	}
	a, _ := e.s.cfg.Get().Account("zen1")
	saved.OpenCode.Source = quota.OpenCodeSource(e.s.cfg.Get(), a)
	e.s.quota.Load(map[string]*quota.Snapshot{a.ID: saved})
	status, b, _ := e.post(t, config.ProviderOpenCodeZen, "/v1/chat/completions", ocRequest(opencode.Chat, "chat", false), nil)
	if status != 502 || e.count() != 0 {
		t.Fatalf("proxy failed but inference went direct: %d %s calls=%d", status, b, e.count())
	}
	if e.s.openCodeProxyView(proxyURL).Status != "unhealthy" {
		t.Fatal("proxy fault not recorded")
	}
	if v := e.s.pool.Views(e.s.cfg.Get())[a.ID]; v.Samples != 0 || v.State != nil && v.State.HardQuarantined {
		t.Fatal("proxy failure blamed on account")
	}
	ln, err = net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RequestURI = ""
		resp, err := http.DefaultTransport.RoundTrip(r)
		if err != nil {
			w.WriteHeader(502)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			w.Header()[k] = vs
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	proxy.Listener = ln
	proxy.Start()
	t.Cleanup(proxy.Close)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/zen1/test-proxy", nil)
	req.SetPathValue("id", a.ID)
	w := httptest.NewRecorder()
	e.s.apiOpenCodeProxyTest(w, req)
	if !strings.Contains(w.Body.String(), `"ok":true`) || e.s.openCodeProxyView(proxyURL).Status != "healthy" {
		t.Fatalf("proxy recovery failed %s", w.Body)
	}
	status, b, _ = e.post(t, config.ProviderOpenCodeZen, "/v1/chat/completions", ocRequest(opencode.Chat, "chat", false), nil)
	if status != 200 {
		t.Fatalf("recovered proxy unusable %d %s", status, b)
	}
}

func TestOpenCodeSettingsAreIndependentAndInvalidUpdateRetainsOld(t *testing.T) {
	e := newOCEnv(t, false)
	cfg := e.s.cfg.Get()
	doc := settingsDoc{Revision: cfg.Revision, Upstream: cfg.Upstream, Routing: cfg.Routing, Quota: cfg.Quota, Logs: cfg.Logs, Pricing: cfg.Pricing, OpenCode: &cfg.OpenCode}
	doc.ErrorRules, doc.RetryRules = cfg.RulesFor(config.ProviderOpenCodeGo)
	doc.Routing.Strategy = "least-connections"
	b, _ := json.Marshal(doc)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/api/settings?provider=opencode-go", strings.NewReader(string(b)))
	e.s.apiSaveSettings(w, r)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	next := e.s.cfg.Get()
	if next.Routing.Strategy != "ordered" || next.ForProvider(config.ProviderOpenCodeGo).Routing.Strategy != "least-connections" || next.OpenCode.ZenRouting != nil {
		t.Fatal("tier settings leaked")
	}
	before := next
	doc.Revision = next.Revision
	bad := next.OpenCode
	bad.GoProtocols = map[string]string{"m": "guess"}
	doc.OpenCode = &bad
	b, _ = json.Marshal(doc)
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPut, "/api/settings?provider=opencode-go", strings.NewReader(string(b)))
	e.s.apiSaveSettings(w, r)
	if w.Code != 400 || e.s.cfg.Get() != before {
		t.Fatal("invalid configuration became effective")
	}
}
