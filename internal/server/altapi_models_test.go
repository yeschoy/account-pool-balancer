package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
)

func altapiModelReply(w http.ResponseWriter, ids ...string) {
	data := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		data = append(data, map[string]string{"id": id, "object": "model"})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func TestALTAPIModelsBackgroundOnlyAndHourlyRefresh(t *testing.T) {
	e := newALTAPIStickyEnv(t)
	var generation atomic.Int32
	e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, _ map[string]any) {
		if r.URL.Path != "/v1/models" {
			t.Error("unexpected endpoint", r.URL.Path)
		}
		if gen := generation.Load(); gen == 2 || (gen == 3 && strings.Contains(key, "alt-a")) {
			w.WriteHeader(503)
			return
		}
		ids := []string{"shared"}
		if strings.Contains(key, "alt-a") {
			ids = append(ids, "a-only")
		} else {
			ids = append(ids, "b-only")
		}
		if generation.Load() == 1 {
			ids = append(ids, "new-model")
		}
		altapiModelReply(w, ids...)
	}
	// Even the first HTTP request must never scan/fetch in the request path.
	if w := getCachedModels(t, e.srv, e.key); w.Code != 502 || e.up.callCount() != 0 {
		t.Fatal("cold request fetched models", w.Code)
	}
	e.srv.refreshALTAPIModels(t.Context(), false)
	if e.up.callCount() != 2 {
		t.Fatal("background did not read the complete group")
	}
	cfg := e.srv.cfg.Get()
	body := e.srv.publishedModels(t.Context(), cfg, "altapi")
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &doc) != nil || len(doc.Data) != 3 {
		t.Fatal("not a deduplicated union", string(body))
	}
	for _, id := range []string{"shared", "a-only", "b-only"} {
		if !strings.Contains(string(body), `"`+id+`"`) {
			t.Fatal("missing", id)
		}
	}
	for range 20 {
		w := getCachedModels(t, e.srv, e.key)
		if w.Code != 200 || w.Body.String() != string(body) {
			t.Fatal("cached HTTP response", w.Code)
		}
		if got := e.srv.publishedModels(t.Context(), cfg, "altapi"); &got[0] != &body[0] {
			t.Fatal("request rebuilt snapshot bytes")
		}
	}
	if w := getCachedModels(t, e.srv, "invalid"); w.Code != 401 {
		t.Fatal("snapshot bypassed client authentication")
	}
	// Balance/catalogue polling and ordinary cache reset do not fan out or
	// re-filter ALTAPI. Per-account inference permissions remain independent.
	e.srv.quota.Load(map[string]*quota.Snapshot{"alt-a": {CatalogAt: time.Now(), Catalog: map[string]bool{"other": true}}})
	e.srv.quota.OnUpdate("alt-a")
	e.srv.resetModels()
	if got := e.srv.publishedModels(t.Context(), cfg, "altapi"); &got[0] != &body[0] || e.up.callCount() != 2 {
		t.Fatal("quota/reset invalidated hourly snapshot")
	}
	generation.Store(1)
	if w := getCachedModels(t, e.srv, e.key); strings.Contains(w.Body.String(), "new-model") || e.up.callCount() != 2 {
		t.Fatal("request performed scheduled work")
	}
	// The new model is now allowed by a refreshed per-account permission
	// snapshot; only the scheduled pass may rebuild the published response.
	e.srv.quota.Load(map[string]*quota.Snapshot{"alt-a": {CatalogAt: time.Now(), Catalog: map[string]bool{"shared": true, "a-only": true, "b-only": true, "new-model": true}}})
	e.srv.refreshALTAPIModels(t.Context(), false)
	updated := e.srv.publishedModels(t.Context(), cfg, "altapi")
	if !strings.Contains(string(updated), "new-model") || e.up.callCount() != 4 {
		t.Fatal("hourly pass did not publish", string(updated))
	}
	generation.Store(2)
	e.srv.refreshALTAPIModels(t.Context(), false)
	if got := e.srv.publishedModels(t.Context(), cfg, "altapi"); &got[0] != &updated[0] || e.up.callCount() != 6 {
		t.Fatal("failed refresh replaced last good body")
	}
	generation.Store(3)
	e.srv.refreshALTAPIModels(t.Context(), false)
	if got := e.srv.publishedModels(t.Context(), cfg, "altapi"); !strings.Contains(string(got), "b-only") || strings.Contains(string(got), "a-only") || e.up.callCount() != 8 {
		t.Fatal("one invalid key blocked the original successful-account union", string(got))
	}
	if altapiModelsInterval != time.Hour {
		t.Fatal("ALTAPI refresh must be fixed at one hour")
	}
}

func TestALTAPIModelsSourceChangesAndGroupIsolation(t *testing.T) {
	e := newALTAPIStickyEnv(t)
	_, err := e.srv.cfg.Update(-1, func(c *config.Config) error {
		c.Accounts = append(c.Accounts, config.Account{ID: "alt-other", Provider: config.ProviderALTAPI, Group: "alt-other", Enabled: true, RuntimeKey: "other-runtime"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e.up.handle = func(w http.ResponseWriter, _ *http.Request, key string, _ map[string]any) { altapiModelReply(w, key) }
	e.srv.refreshALTAPIModels(t.Context(), false)
	cfg := e.srv.cfg.Get()
	main := e.srv.publishedModels(t.Context(), cfg, "altapi")
	other := e.srv.publishedModels(t.Context(), cfg, "alt-other")
	if strings.Contains(string(main), "other-runtime") || !strings.Contains(string(other), "other-runtime") || len(main) == 0 {
		t.Fatal("group catalogue leak")
	}
	// Session/balance/limit/name changes are not model-source changes.
	_, err = e.srv.cfg.Update(-1, func(c *config.Config) error {
		c.Accounts[0].Name = "renamed"
		c.Accounts[0].ControlKey, c.Accounts[0].UserID = "rotated-session", "42"
		c.ALTAPI.LowBalanceDisable, c.ALTAPI.MinBalanceUSD = true, 0.5
		c.Logs.RetentionDays = 8
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg = e.srv.cfg.Get()
	if got := e.srv.publishedModels(t.Context(), cfg, "altapi"); &got[0] != &main[0] {
		t.Fatal("irrelevant configuration invalidated snapshot")
	}
	_, err = e.srv.cfg.Update(-1, func(c *config.Config) error { c.Accounts[0].Enabled = false; return nil })
	if err != nil {
		t.Fatal(err)
	}
	cfg = e.srv.cfg.Get()
	if e.srv.publishedModels(t.Context(), cfg, "altapi") != nil {
		t.Fatal("disabled account's old models remained visible")
	}
	if got := e.srv.publishedModels(t.Context(), cfg, "alt-other"); &got[0] != &other[0] {
		t.Fatal("source change invalidated unrelated group")
	}
	e.srv.refreshALTAPIModels(t.Context(), true)
	if got := e.srv.publishedModels(t.Context(), cfg, "altapi"); strings.Contains(string(got), "alt-a") || !strings.Contains(string(got), "alt-b") || e.up.callCount() != 4 {
		t.Fatal("source-change refill rescanned unrelated groups", string(got))
	}
	// Old request snapshots do not cross the config publication boundary.
	if e.srv.publishedModels(t.Context(), cfg.Clone(), "alt-other") == nil {
		t.Fatal("same revision clone lost catalogue")
	}
	_, err = e.srv.cfg.Update(-1, func(c *config.Config) error { c.Accounts[1].Group = "alt-other"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if e.srv.publishedModels(t.Context(), cfg, "alt-other") != nil {
		t.Fatal("obsolete caller read newer source")
	}
	cfg = e.srv.cfg.Get()
	if e.srv.publishedModels(t.Context(), cfg, "altapi") != nil || e.srv.publishedModels(t.Context(), cfg, "alt-other") != nil {
		t.Fatal("regroup reused old models")
	}
}

func TestALTAPIModelsInflightSourceRotationCannotPublish(t *testing.T) {
	e := newALTAPIStickyEnv(t)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	e.up.handle = func(w http.ResponseWriter, _ *http.Request, _ string, _ map[string]any) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		altapiModelReply(w, "old-source")
	}
	done := make(chan struct{})
	go func() { defer close(done); e.srv.refreshALTAPIModels(t.Context(), false) }()
	<-started
	_, err := e.srv.cfg.Update(-1, func(c *config.Config) error { c.Accounts[0].RuntimeKey = "rotated-runtime"; return nil })
	if err != nil {
		close(release)
		<-done
		t.Fatal(err)
	}
	close(release)
	<-done
	if w := getCachedModels(t, e.srv, e.key); w.Code != 502 {
		t.Fatal("obsolete background read repopulated models", w.Body.String())
	}
	e.up.mu.Lock()
	e.up.handle = func(w http.ResponseWriter, _ *http.Request, _ string, _ map[string]any) {
		altapiModelReply(w, "current-source")
	}
	e.up.mu.Unlock()
	e.srv.refreshALTAPIModels(t.Context(), true)
	if w := getCachedModels(t, e.srv, e.key); w.Code != 200 || !strings.Contains(w.Body.String(), "current-source") || strings.Contains(w.Body.String(), "old-source") {
		t.Fatal("current source did not replace old read", w.Body.String())
	}
}

func TestALTAPIModelsWorkerStartsAndStops(t *testing.T) {
	e := newALTAPIStickyEnv(t)
	started := make(chan struct{})
	var calls atomic.Int32
	e.up.handle = func(w http.ResponseWriter, r *http.Request, _ string, _ map[string]any) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-r.Context().Done()
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); e.srv.runALTAPIModels(ctx) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("startup worker never scanned models")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not cancel catalogue HTTP")
	}
	if calls.Load() != 1 || getCachedModels(t, e.srv, e.key).Code != 502 {
		t.Fatal("cancelled pass published/fetched remaining accounts")
	}
}

func TestALTAPIModelsTimerAndConfigWake(t *testing.T) {
	e := newALTAPIStickyEnv(t)
	var generation atomic.Int32
	e.up.handle = func(w http.ResponseWriter, _ *http.Request, _ string, _ map[string]any) {
		altapiModelReply(w, fmt.Sprintf("generation-%d", generation.Load()))
	}
	ctx, cancel := context.WithCancel(t.Context())
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() { defer close(done); e.srv.loopALTAPIModels(ctx, ticks) }()
	t.Cleanup(func() { cancel(); <-done })
	await := func(want string, calls int) {
		t.Helper()
		deadline := time.NewTimer(3 * time.Second)
		defer deadline.Stop()
		poll := time.NewTicker(time.Millisecond)
		defer poll.Stop()
		for {
			body := e.srv.publishedModels(t.Context(), e.srv.cfg.Get(), "altapi")
			if strings.Contains(string(body), want) && e.up.callCount() == calls {
				return
			}
			select {
			case <-deadline.C:
				t.Fatalf("worker did not publish %s after %d reads: %s", want, calls, body)
			case <-poll.C:
			}
		}
	}
	await("generation-0", 2)
	generation.Store(1)
	ticks <- time.Now() // deterministic clock; production passes a one-hour ticker
	await("generation-1", 4)
	generation.Store(2)
	_, err := e.srv.cfg.Update(-1, func(c *config.Config) error { c.Accounts[0].RuntimeKey = "new-model-source"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	await("generation-2", 6) // source changes wake the service without an HTTP miss
	for range 30 {
		if getCachedModels(t, e.srv, e.key).Code != 200 {
			t.Fatal("warm listing failed")
		}
	}
	if e.up.callCount() != 6 {
		t.Fatal("listing scheduled extra background reads")
	}
}

func TestALTAPIModelsSourceFingerprint(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*config.Config)
	}{
		{"runtime key", func(c *config.Config) { c.Accounts[0].RuntimeKey = "new-runtime" }},
		{"proxy", func(c *config.Config) { c.Accounts[0].ProxyURL = "http://127.0.0.1:1" }},
		{"base", func(c *config.Config) { c.ALTAPI.BaseURL = "http://127.0.0.1:2/v1" }},
		{"enabled", func(c *config.Config) { c.Accounts[0].Enabled = false }},
		{"group", func(c *config.Config) { c.Accounts[0].Group = "alt-second" }},
		{"deleted", func(c *config.Config) { c.Accounts = c.Accounts[1:] }},
		{"filter", func(c *config.Config) {
			r := c.ALTAPI.EffectiveRouting()
			r.IgnoreCatalog = !r.IgnoreCatalog
			c.ALTAPI.Routing = &r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config.Default()
			c.Accounts = []config.Account{{ID: "a", Provider: config.ProviderALTAPI, Enabled: true, Group: "altapi", RuntimeKey: "runtime-a"}}
			if err := c.Normalize(); err != nil {
				t.Fatal(err)
			}
			var s Server
			s.reconcileALTAPIModels(c)
			s.altapiModels.Lock()
			s.altapiModels.groups["altapi"].body = []byte("old")
			s.altapiModels.Unlock()
			next := c.Clone()
			next.Revision++
			tc.change(next)
			if err := next.Normalize(); err != nil {
				t.Fatal(err)
			}
			s.reconcileALTAPIModels(next)
			if s.readALTAPIModels(next, "altapi") != nil {
				t.Fatal("old catalogue survived a source change")
			}
		})
	}
}

func BenchmarkALTAPIModelsSnapshot(b *testing.B) {
	c := config.Default()
	for i := range 10000 {
		c.Accounts = append(c.Accounts, config.Account{ID: fmt.Sprint(i), Provider: config.ProviderALTAPI, Group: "altapi", Enabled: true, RuntimeKey: fmt.Sprintf("fake-runtime-%d", i)})
	}
	if err := c.Normalize(); err != nil {
		b.Fatal(err)
	}
	var s Server
	s.reconcileALTAPIModels(c)
	s.altapiModels.Lock()
	s.altapiModels.groups["altapi"].body = []byte(`{"object":"list","data":[{"id":"fixed-model"}]}`)
	s.altapiModels.Unlock()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if s.publishedModels(b.Context(), c, "altapi") == nil {
			b.Fatal("missing snapshot")
		}
	}
}
