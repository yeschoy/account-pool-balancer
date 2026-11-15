package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
)

func getCachedModels(t *testing.T, s *Server, key string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	s.handleModels(w, r)
	return w
}

func TestModelsResponseCacheAndQuotaIsolation(t *testing.T) {
	e := newEnv(t, []config.Account{{ID: "a"}, {ID: "b"}, {ID: "other", Group: "other"}}, nil)
	e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, _ map[string]any) {
		fmt.Fprint(w, `{"object":"list","data":[{"id":"m1"},{"id":"m2"},{"id":"hidden"}]}`)
	}
	now := time.Now()
	e.srv.quota.Load(map[string]*quota.Snapshot{
		"a": {CatalogAt: now, Catalog: map[string]bool{"m1": true}},
		"b": {CatalogAt: now, Catalog: map[string]bool{"m1": true}},
	})
	first := getCachedModels(t, e.srv, e.key)
	if first.Code != 200 || !strings.Contains(first.Body.String(), `"m1"`) || strings.Contains(first.Body.String(), `"m2"`) {
		t.Fatalf("filtered response: %d %s", first.Code, first.Body.String())
	}
	cfg := e.srv.cfg.Get()
	body := e.srv.publishedModels(t.Context(), cfg, "default")
	if got := e.srv.publishedModels(t.Context(), cfg, "default"); &got[0] != &body[0] {
		t.Fatal("published cache hit rebuilt the response")
	}
	if n := e.up.callCount(); n != 2 {
		t.Fatalf("repeated listing fanned out again: %d calls, want 2", n)
	}
	other := e.srv.publishedModels(t.Context(), cfg, "other")

	// A quota update rebuilds only the local filtered response. It must not
	// drop the raw union or another group's response.
	e.srv.quota.Load(map[string]*quota.Snapshot{"a": {CatalogAt: now, Catalog: map[string]bool{"m1": true, "m2": true}}})
	e.srv.quota.OnUpdate("a")
	updated := getCachedModels(t, e.srv, e.key)
	if !strings.Contains(updated.Body.String(), `"m2"`) || strings.Contains(updated.Body.String(), `"hidden"`) {
		t.Fatalf("quota update did not refresh filtering: %s", updated.Body.String())
	}
	if got := e.srv.publishedModels(t.Context(), cfg, "other"); &got[0] != &other[0] {
		t.Fatal("quota update evicted an unrelated group")
	}
	if n := e.up.callCount(); n != 3 {
		t.Fatalf("quota refresh triggered network fan-out: %d calls, want 3", n)
	}
	if w := getCachedModels(t, e.srv, "invalid"); w.Code != http.StatusUnauthorized || e.up.callCount() != 3 {
		t.Fatal("cache bypassed downstream authentication")
	}

	// Both layers expire together; a refresh still reads all enabled keys,
	// rather than incorrectly publishing only the first account's models.
	e.srv.models.Lock()
	for key, entry := range e.srv.models.entries {
		if key.group == "default" {
			entry.expiresAt = now.Add(-time.Second)
			e.srv.models.entries[key] = entry
		}
	}
	e.srv.models.Unlock()
	if w := getCachedModels(t, e.srv, e.key); w.Code != 200 || e.up.callCount() != 5 {
		t.Fatalf("expired listing was not refreshed: status %d calls %d", w.Code, e.up.callCount())
	}
}

func TestModelsCacheConcurrentMisses(t *testing.T) {
	var s Server
	cfg := config.Default()
	key := modelsKey{provider: config.ProviderModal, group: "modal", published: true}
	started, release := make(chan struct{}), make(chan struct{})
	var loads atomic.Int32
	load := func(context.Context) modelsEntry {
		if loads.Add(1) == 1 {
			close(started)
		}
		<-release
		return modelsEntry{body: []byte("shared"), expiresAt: time.Now().Add(time.Minute)}
	}
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			got := s.cachedModels(t.Context(), cfg, key, load)
			if string(got.body) != "shared" {
				t.Errorf("response = %q", got.body)
			}
		})
	}
	<-started
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := s.cachedModels(ctx, cfg, key, load); got.body != nil {
		t.Fatal("cancelled waiter did not leave the shared read")
	}
	close(release)
	wg.Wait()
	if loads.Load() != 1 {
		t.Fatalf("concurrent misses loaded %d times", loads.Load())
	}
}

func TestModelsCacheResetDetachesInflight(t *testing.T) {
	var s Server
	cfg := config.Default()
	key := modelsKey{provider: config.ProviderModal, group: "modal"}
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		s.cachedModels(t.Context(), cfg, key, func(context.Context) modelsEntry {
			close(started)
			<-release
			return modelsEntry{body: []byte("obsolete"), expiresAt: time.Now().Add(time.Minute)}
		})
	}()
	<-started
	s.resetModels()
	loadNew := func(context.Context) modelsEntry {
		return modelsEntry{body: []byte("new"), expiresAt: time.Now().Add(time.Minute)}
	}
	if got := s.cachedModels(t.Context(), cfg, key, loadNew); string(got.body) != "new" {
		t.Fatal("new request joined obsolete in-flight read")
	}
	close(release)
	<-finished
	if got := s.cachedModels(t.Context(), cfg, key, func(context.Context) modelsEntry {
		t.Fatal("obsolete read removed or replaced the new cache")
		return modelsEntry{}
	}); string(got.body) != "new" {
		t.Fatalf("obsolete read overwrote the new response: %q", got.body)
	}
}

func TestModelsCacheFailureAndRevision(t *testing.T) {
	var s Server
	cfg := config.Default()
	key := modelsKey{provider: config.ProviderModal, group: "modal"}
	var calls int
	load := func(context.Context) modelsEntry {
		calls++
		if calls == 1 {
			return modelsEntry{}
		}
		return modelsEntry{body: []byte("ok"), expiresAt: time.Now().Add(time.Minute)}
	}
	s.cachedModels(t.Context(), cfg, key, load)
	if got := s.cachedModels(t.Context(), cfg, key, load); string(got.body) != "ok" || calls != 2 {
		t.Fatal("failed load was cached")
	}
	cfg.Revision++
	s.cachedModels(t.Context(), cfg, key, load)
	if calls != 3 {
		t.Fatal("configuration revision reused a stale cache")
	}
}

func TestModelsCacheQuotaScope(t *testing.T) {
	e := newEnv(t, []config.Account{{ID: "a"}, {ID: "modal", Provider: config.ProviderModal, Group: "modal", RuntimeKey: "wk-m.ws-1"}}, nil)
	cfg := e.srv.cfg.Get()
	e.srv.models.Lock()
	for _, p := range []string{config.ProviderOpenDesign, config.ProviderModal} {
		group := "default"
		if p == config.ProviderModal {
			group = "modal"
		}
		for _, published := range []bool{false, true} {
			key := modelsKey{provider: p, group: group, published: published}
			if e.srv.models.entries == nil {
				e.srv.models.entries = map[modelsKey]modelsEntry{}
				e.srv.models.inflight = map[modelsKey]*modelsCall{}
			}
			e.srv.models.entries[key] = modelsEntry{body: []byte("cached"), expiresAt: time.Now().Add(time.Minute), revision: cfg.Revision}
			e.srv.models.inflight[key] = &modelsCall{done: make(chan struct{}), revision: cfg.Revision}
		}
	}
	e.srv.models.Unlock()
	e.srv.quota.OnUpdate("a")
	e.srv.models.Lock()
	defer e.srv.models.Unlock()
	for _, p := range []string{config.ProviderOpenDesign, config.ProviderModal} {
		group := "default"
		if p == config.ProviderModal {
			group = "modal"
		}
		for _, published := range []bool{false, true} {
			key := modelsKey{provider: p, group: group, published: published}
			_, cached := e.srv.models.entries[key]
			_, inflight := e.srv.models.inflight[key]
			want := p != config.ProviderOpenDesign || !published
			if cached != want || inflight != want {
				t.Fatalf("quota invalidation scope: %+v cached=%t inflight=%t want=%t", key, cached, inflight, want)
			}
		}
	}
}

func TestModelsCacheFilterExpiry(t *testing.T) {
	for _, provider := range []string{config.ProviderOpenDesign, config.ProviderDeploy} {
		t.Run(provider, func(t *testing.T) {
			e := newEnv(t, []config.Account{{ID: "a", Provider: provider, Group: provider}}, func(c *config.Config) {
				if provider == config.ProviderDeploy {
					q := c.Deploy.EffectiveQuota()
					q.Enabled = false
					c.Deploy.Quota = &q
					c.Deploy.BaseURL = c.Upstream.RuntimeBase
				}
			})
			cfg := e.srv.cfg.Get().ForProvider(provider)
			at := time.Now().Add(-24*time.Hour + time.Minute)
			expiry := at.Add(24 * time.Hour)
			if provider == config.ProviderDeploy {
				stale := time.Duration(cfg.Deploy.EffectiveQuota().StaleSec) * time.Second
				at = time.Now().Add(-stale + time.Minute)
				expiry = at.Add(stale)
			}
			e.srv.quota.Load(map[string]*quota.Snapshot{"a": {CatalogAt: at, CatalogSource: quota.ResponseBindingSource(cfg, cfg.Accounts[0]), Catalog: map[string]bool{"m": true}}})
			entry := e.srv.filterModels(cfg, provider, modelsEntry{body: []byte(`{"data":[{"id":"m"},{"id":"hidden"}]}`), expiresAt: time.Now().Add(time.Hour)})
			if entry.expiresAt.After(expiry) || strings.Contains(string(entry.body), "hidden") {
				t.Fatalf("filtered cache extended catalogue freshness: expiry=%v want<=%v body=%s", entry.expiresAt, expiry, entry.body)
			}
		})
	}
}

func TestModelsCacheConfigChanges(t *testing.T) {
	e := newEnv(t, []config.Account{{ID: "a"}, {ID: "b"}}, nil)
	e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, _ map[string]any) {
		id := "a-model"
		if strings.Contains(key, "-b-") {
			id = "b-model"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": id}}})
	}
	if w := getCachedModels(t, e.srv, e.key); !strings.Contains(w.Body.String(), "b-model") {
		t.Fatal("initial union missing second account")
	}
	_, err := e.srv.cfg.Update(-1, func(c *config.Config) error {
		c.Accounts[1].Enabled = false
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if w := getCachedModels(t, e.srv, e.key); strings.Contains(w.Body.String(), "b-model") || e.up.callCount() != 3 {
		t.Fatalf("disabled account remained cached: %s calls=%d", w.Body.String(), e.up.callCount())
	}
}

func TestTokenHarborModelsCacheExpiryAndInvalidation(t *testing.T) {
	e := newTHEnv(t)
	cfg := e.s.cfg.Get().ForProvider(config.ProviderTokenHarbor)
	first := e.s.publishedModels(t.Context(), cfg, config.TokenHarborGroup)
	second := e.s.publishedModels(t.Context(), cfg, config.TokenHarborGroup)
	if len(first) == 0 || &first[0] != &second[0] {
		t.Fatal("TokenHarbor bypassed the final-response cache")
	}
	q := e.s.quota.Get("th1")
	q.Catalog = map[string]bool{"new-model": true}
	q.TokenHarbor.ValidUntil = time.Now().Add(time.Minute)
	e.s.quota.Load(map[string]*quota.Snapshot{"th1": q})
	e.s.quota.OnUpdate("th1")
	body := e.s.publishedModels(t.Context(), cfg, config.TokenHarborGroup)
	if !strings.Contains(string(body), "new-model") {
		t.Fatal("TokenHarbor entitlement update did not invalidate its local union")
	}
	key := modelsKey{provider: config.ProviderTokenHarbor, group: config.TokenHarborGroup, published: true}
	e.s.models.Lock()
	until := e.s.models.entries[key].expiresAt
	e.s.models.Unlock()
	if until.After(q.TokenHarbor.ValidUntil) {
		t.Fatal("cache outlives TokenHarbor plan proof")
	}
	// Emulate expiry without sleeping or mutating a stored immutable snapshot.
	for _, id := range []string{"th1", "th2"} {
		q := e.s.quota.Get(id)
		q.TokenHarbor.ValidUntil = time.Now().Add(-time.Second)
		e.s.quota.Load(map[string]*quota.Snapshot{id: q})
	}
	e.s.resetModels()
	body = e.s.publishedModels(t.Context(), cfg, config.TokenHarborGroup)
	if strings.Contains(string(body), `"id"`) {
		t.Fatalf("expired TokenHarbor entitlement remained published: %s", body)
	}
	if e.count() != 0 {
		t.Fatal("model listing performed upstream inference")
	}
}

func TestOpenCodeModelsCacheFallbackInvalidationAndExpiry(t *testing.T) {
	e := newOCEnv(t, false)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.OpenCode.ZenFallbackGroup = config.ProviderOpenCodeGo
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Refresh after the source-affecting settings update.
	for _, a := range e.s.cfg.Get().Accounts {
		if err := e.s.quota.RefreshNow(t.Context(), a); err != nil {
			t.Fatal(err)
		}
	}
	cfg := e.s.cfg.Get().ForProvider(config.ProviderOpenCodeZen)
	first := e.s.publishedModels(t.Context(), cfg, config.ProviderOpenCodeZen)
	upCalls := e.upCalls.Load()
	q := e.s.quota.Get("go1")
	q.Catalog["go-only"] = true
	q.OpenCode.Models["go-only"] = q.OpenCode.Models["chat"]
	q.OpenCode.ModelsAt = time.Now().Add(-24*time.Hour + time.Minute)
	e.s.quota.Load(map[string]*quota.Snapshot{"go1": q})
	e.s.quota.OnUpdate("go1")
	updated := e.s.publishedModels(t.Context(), cfg, config.ProviderOpenCodeZen)
	if &updated[0] == &first[0] || !strings.Contains(string(updated), "go-only") {
		t.Fatalf("fallback account did not invalidate the primary listing: %s", updated)
	}
	key := modelsKey{provider: config.ProviderOpenCodeZen, group: config.ProviderOpenCodeZen, published: true}
	e.s.models.Lock()
	until := e.s.models.entries[key].expiresAt
	e.s.models.Unlock()
	if until.After(q.OpenCode.ModelsAt.Add(24 * time.Hour)) {
		t.Fatal("cached listing outlives protocol metadata")
	}
	if e.upCalls.Load() != upCalls {
		t.Fatal("OpenCode listing made network calls")
	}
}

func BenchmarkModelsPublishedCacheHit(b *testing.B) {
	for _, accounts := range []int{1, 1000, 10000} {
		b.Run(fmt.Sprintf("accounts-%d", accounts), func(b *testing.B) {
			s := newTHEnv(b).s
			cfg := s.cfg.Get().ForProvider(config.ProviderTokenHarbor)
			// The cache lookup must remain independent of account count.
			cfg.Accounts = make([]config.Account, accounts)
			for i := range cfg.Accounts {
				cfg.Accounts[i] = config.Account{ID: fmt.Sprintf("mock-%d", i), Provider: config.ProviderTokenHarbor, Group: config.TokenHarborGroup, Enabled: true}
			}
			s.publishedModels(b.Context(), cfg, config.TokenHarborGroup)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				s.publishedModels(b.Context(), cfg, config.TokenHarborGroup)
			}
		})
	}
}
