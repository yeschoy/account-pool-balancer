package server

import (
	"account-pool-balancer/internal/config"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPoolSwitchManagementAndBlocking(t *testing.T) {
	e := newFactoryEnv(t)
	before := e.s.cfg.Get().Clone()
	ctx, cancel := e.s.poolJobContext(context.Background(), config.ProviderFactory)
	defer cancel()
	w := deployAdmin(t, e, "PUT", "/api/pools/factory", map[string]any{"revision": before.Revision, "enabled": false})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("background context not cancelled")
	}
	if got := e.s.cfg.Get(); got.PoolEnabled(config.ProviderFactory) || !reflect.DeepEqual(got.Accounts, before.Accounts) || !reflect.DeepEqual(got.Factory, before.Factory) {
		t.Fatal("toggle mutated accounts/config")
	}
	rsp, out := e.do(t, "/v1/chat/completions", `{"model":"deepseek-v4.1-flash"}`, nil)
	if rsp.StatusCode != 503 || !strings.Contains(out, "pool_disabled") {
		t.Fatal("disabled inference", rsp.StatusCode, out)
	}
	req, _ := http.NewRequest("GET", e.front.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer downstream-ms")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatal("disabled directory")
	}
	for _, path := range []string{"/api/accounts/f1/test", "/api/accounts/f1/refresh-quota"} {
		if w := deployAdmin(t, e, "POST", path, map[string]any{}); w.Code != 409 {
			t.Fatal("disabled admin network", path, w.Code)
		}
	}
	if len(e.snapshot()) != 0 {
		t.Fatal("disabled hit upstream")
	}
	if e.s.pool.Views(e.s.cfg.Get())["f1"].Status != "provider-disabled" {
		t.Fatal("scheduler ignored switch")
	}
	if err := e.s.Reload(); err != nil || e.s.cfg.Get().PoolEnabled(config.ProviderFactory) {
		t.Fatal("switch did not persist", err)
	}
	w = deployAdmin(t, e, "GET", "/api/pools", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"provider":"factory"`) {
		t.Fatal("no reenable entry")
	}
	w = deployAdmin(t, e, "PUT", "/api/pools/factory", map[string]any{"revision": e.s.cfg.Get().Revision, "enabled": true})
	if w.Code != 200 {
		t.Fatal("enable", w.Code)
	}
	if next := e.s.poolWorkContext(config.ProviderFactory); next.Err() != nil || ctx.Err() == nil {
		t.Fatal("old epoch revived/new epoch cancelled")
	}
	rsp, out = e.do(t, "/v1/chat/completions", `{"model":"deepseek-v4.1-flash","messages":[]}`, nil)
	if rsp.StatusCode != 200 {
		t.Fatal("reenable failed", out)
	}
}
func TestPoolSwitchAuthAndRevision(t *testing.T) {
	e := newFactoryEnv(t)
	req := httptest.NewRequest("PUT", "/api/pools/factory", strings.NewReader(`{"enabled":false}`))
	w := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal("unauthenticated switch")
	}
	w = deployAdmin(t, e, "PUT", "/api/pools/factory", map[string]any{"revision": e.s.cfg.Get().Revision + 1, "enabled": false})
	if w.Code != 409 || !e.s.cfg.Get().PoolEnabled(config.ProviderFactory) {
		t.Fatal("stale revision mutation")
	}
	if w = deployAdmin(t, e, "PUT", "/api/pools/typo", map[string]any{"enabled": false}); w.Code != 400 {
		t.Fatal("typo accepted")
	}
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.PoolSwitches = map[string]bool{}
		for _, p := range config.ManagedPools() {
			c.PoolSwitches[p] = false
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/telnyx/candidates/bulk-register", "/api/x50/candidates/id/retry", "/api/deploy/candidates/bulk-register", "/api/perch/registrations", "/api/tokenharbor/registrations"} {
		if w = deployAdmin(t, e, "POST", path, map[string]any{}); w.Code != 409 {
			t.Fatal("registration gate missing", path, w.Code, w.Body.String())
		}
	}
	if w = deployAdmin(t, e, "GET", "/api/pools", nil); w.Code != 200 {
		t.Fatal("all-off locked control plane")
	}
}
func TestPoolSwitchLetsActiveInferenceFinish(t *testing.T) {
	e := newFactoryEnv(t)
	started, release := make(chan struct{}), make(chan struct{})
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"}}]}`)
	}
	e.mu.Unlock()
	done := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest("POST", e.front.URL+"/v1/chat/completions", strings.NewReader(`{"model":"deepseek-v4.1-flash"}`))
		req.Header.Set("Authorization", "Bearer downstream-ms")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- 0
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		done <- resp.StatusCode
	}()
	<-started
	w := deployAdmin(t, e, "PUT", "/api/pools/factory", map[string]any{"revision": e.s.cfg.Get().Revision, "enabled": false})
	if w.Code != 200 {
		t.Fatal("off")
	}
	close(release)
	select {
	case st := <-done:
		if st != 200 {
			t.Fatal("active inference killed", st)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("inflight hung")
	}
}
