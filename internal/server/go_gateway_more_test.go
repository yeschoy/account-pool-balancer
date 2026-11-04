package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
)

func TestGoBusyCachePreferenceMayFallbackWithoutMovingOwner(t *testing.T) {
	e := newGoGatewayEnv(t)
	body := `{"model":"chat","prompt_cache_key":"shared-prefix","messages":[{"role":"user","content":"hello"}]}`
	h := map[string]string{"X-Session-Id": "owner"}
	if st, _, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", body, h); st != 200 {
		t.Fatal(st)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Session-Id") == "owner" {
			once.Do(func() { close(entered) })
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, ocReply(opencode.Chat))
	}
	done := make(chan int, 1)
	go func() { st, _, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", body, h); done <- st }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("owner not active")
	}
	st, _, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", body, map[string]string{"X-Session-Id": "other"})
	close(release)
	if <-done != 200 || st != 200 {
		t.Fatal("cache preference incorrectly became strict conversation binding")
	}
	st, _, _ = e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", body, map[string]string{"X-Session-Id": "third"})
	if st != 200 {
		t.Fatal(st)
	}
	e.mu.Lock()
	keys := append([]string(nil), e.keys...)
	e.mu.Unlock()
	if len(keys) != 4 || keys[0] == keys[2] || keys[0] != keys[3] {
		t.Fatal("busy cache fallback moved the preferred owner")
	}
	rows := goLogRows(t, e.s, "requests", map[string]string{"provider": config.ProviderOpenCodeGo}, 4)
	found := false
	for _, r := range rows {
		if r["cacheAffinityResult"] == "busy-fallback" {
			found = true
		}
	}
	if !found {
		t.Fatal("cache fallback reason missing")
	}
}

func TestGoEgressProbeAndMetadataUseSameFixedProxy(t *testing.T) {
	e := newGoGatewayEnv(t)
	var proxyChecks, proxyUsage atomic.Int32
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Fixed-Proxy") != "yes" {
			t.Error("IP check bypassed fixed proxy")
		}
		fmt.Fprint(w, "203.0.113.9")
	}))
	defer probe.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == strings.TrimPrefix(probe.URL, "http://") {
			proxyChecks.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "/usage") {
			proxyUsage.Add(1)
		}
		r.RequestURI = ""
		r.Header.Set("X-Test-Fixed-Proxy", "yes")
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
	defer proxy.Close()
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.OpenCode.GoEgressCheckURL = probe.URL
		for i := range c.Accounts {
			if c.Accounts[i].ID == "go1" {
				c.Accounts[i].ProxyURL = proxy.URL
				c.Accounts[i].EgressIP = "203.0.113.9"
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := e.s.cfg.Get().Account("go1")
	if err = e.s.quota.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if proxyChecks.Load() != 1 || proxyUsage.Load() != 1 {
		t.Fatal("quota and probe did not share fixed account transport")
	}
	if st, _, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", ocRequest(opencode.Chat, "chat", false), nil); st != 200 {
		t.Fatal(st)
	}
}

func TestGoPostCommitPolicyErrorLogsAndNeverReplays(t *testing.T) {
	e := newGoGatewayEnv(t)
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		w.(http.Flusher).Flush()
		fmt.Fprint(w, "event: error\ndata: {\"error\":{\"type\":\"permission_error\",\"status\":403,\"message\":\"policy_violation\"}}\n\n")
	}
	st, _, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", ocRequest(opencode.Chat, "chat", true), map[string]string{"X-Session-Id": "stream-session"})
	if st != 200 || e.count() != 1 {
		t.Fatal("committed stream was replayed")
	}
	rows := goLogRows(t, e.s, "errors", map[string]string{"provider": config.ProviderOpenCodeGo, "origin": "upstream"}, 1)
	if rows[0]["category"] != "policy" || rows[0]["upstreamStatus"] != float64(200) {
		t.Fatal("stream policy error not correlated with original HTTP status")
	}
	v := e.s.pool.Views(e.s.cfg.Get())["go1"]
	if v.State == nil || v.State.RuleID != "opencode-go-policy-refusal" || v.Samples != 0 {
		t.Fatal("stream policy did not pause safely")
	}
}

func TestGoLateCachePreferenceCannotReviveOldCredentialAffinity(t *testing.T) {
	e := newGoGatewayEnv(t)
	old := e.s.cfg.Get()
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			if c.Accounts[i].ID == "go1" {
				c.Accounts[i].RuntimeKey = "new-go-credential-key"
				c.Accounts[i].Priority = 90
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := e.s.cfg.Get().Account("go1")
	if err = e.s.quota.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	scope := config.ProviderOpenCodeGo + "\x00" + config.ProviderOpenCodeGo
	cache := e.s.fingerprint(scope, "go-cache:chat", "old-cache-hint")
	e.s.pool.RememberCache(old, cache, "go1", "chat")
	body := `{"model":"chat","prompt_cache_key":"old-cache-hint","messages":[{"role":"user","content":"new conversation"}]}`
	if st, _, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", body, map[string]string{"X-Session-Id": "new-session"}); st != 200 {
		t.Fatal(st)
	}
	e.mu.Lock()
	key := e.keys[0]
	e.mu.Unlock()
	if key != "second-go-secret-key" {
		t.Fatal("old credential result revived cache affinity")
	}
	rows := goLogRows(t, e.s, "requests", map[string]string{"provider": config.ProviderOpenCodeGo}, 1)
	if rows[0]["cacheAffinityResult"] != "invalidated" {
		t.Fatal("source invalidation not logged")
	}
}

func TestGoSharedEgressPolicyIncludesUnpinnedPeersOnSameRoute(t *testing.T) {
	a := config.Account{ProxyURL: "http://static-proxy.example:8080", EgressIP: "203.0.113.7"}
	if !sharesGoEgress(a, config.Account{ProxyURL: a.ProxyURL}) {
		t.Fatal("unpinned peer on the same fixed route escaped pause")
	}
	if !sharesGoEgress(a, config.Account{ProxyURL: "http://other-proxy.example:8080", EgressIP: a.EgressIP}) {
		t.Fatal("shared IP pin escaped pause")
	}
	if sharesGoEgress(a, config.Account{ProxyURL: "http://other-proxy.example:8080", EgressIP: "203.0.113.8"}) {
		t.Fatal("unrelated egress was paused")
	}
	if goPolicyRefusal(403, "model permission denied") || !goPolicyRefusal(403, "account_suspended") {
		t.Fatal("generic model permission and account policy confused")
	}
}
