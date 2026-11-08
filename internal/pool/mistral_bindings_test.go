package pool

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func mistralBindingConfig() *config.Config {
	cfg := testConfig(config.Account{ID: "m1", Provider: config.ProviderMistral, Group: config.MistralGroup, RuntimeKey: "mstrl_synthetic_key", MaxConc: 2, MaxRPM: 10})
	cfg.ClientKeys = []config.ClientKey{{ID: "caller", Group: config.MistralGroup, Provider: config.ProviderMistral, Enabled: true, KeyHash: config.HashKey("synthetic-downstream-key")}}
	return cfg
}

func TestMistralBindingCheckpointLifecycle(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			cfg := mistralBindingConfig()
			now := time.Now().UTC()
			p := New(nil)
			p.SetClock(func() time.Time { return now })
			key := strings.Repeat("a", 64)
			req := Request{Group: config.MistralGroup, ClientKeyID: "caller", Affinity: key, Explicit: explicit, RequestID: "request-1"}
			lease, err := p.Acquire(context.Background(), cfg, req)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := p.ExportState()
			if len(before.MistralBindings) != 0 {
				t.Fatal("unsent binding persisted")
			}
			lease.MarkSent()
			lease.MarkSent()
			lease.Release()
			states, samples := p.Dirty()
			if states || !samples {
				t.Fatal("ordinary binding must use coalesced metadata writes")
			}
			saved, _ := p.ExportState()
			if len(saved.MistralBindings) != 1 || len(saved.Bindings) != 0 {
				t.Fatal("ordinary binding mixed into strict response owners")
			}
			v := saved.MistralBindings[0]
			deadline := now.Add(ttl(cfg, explicit))
			if v.Explicit != explicit || !v.ExpiresAt.Equal(deadline) || len(v.Source) != 64 || strings.Contains(v.Source, "synthetic") {
				t.Fatal("checkpoint lost type/deadline or contains raw source")
			}
			now = now.Add(3 * time.Second)
			restored := New(nil)
			restored.SetClock(func() time.Time { return now })
			restored.ImportState(saved, map[string]bool{"m1": true})
			restored.ImportMistralBindings(saved.MistralBindings, cfg)
			if account, ok := restored.BoundAccount(key); !ok || account != "m1" {
				t.Fatal("confirmed preference not restored")
			}
			view := restored.Views(cfg)["m1"]
			if view.Active != 0 || view.RPM != 0 {
				t.Fatal("inflight/RPM state restored")
			}
			check, _ := restored.ExportState()
			if !check.MistralBindings[0].ExpiresAt.Equal(deadline) || check.MistralBindings[0].Explicit != explicit {
				t.Fatal("restart renewed TTL or promoted weak identity")
			}
			next, err := restored.Acquire(context.Background(), cfg, req)
			if err != nil || next.Decision.BindingResult != "hit" {
				t.Fatal("restored request did not use binding", err)
			}
			next.Release()
			// No sent callback after cancellation may create a new checkpoint.
			cancelled, err := p.Acquire(context.Background(), cfg, Request{Group: config.MistralGroup, ClientKeyID: "caller", Affinity: strings.Repeat("b", 64), RequestID: "cancelled"})
			if err != nil {
				t.Fatal(err)
			}
			cancelled.Release()
			cancelled.MarkSent()
			check, _ = p.ExportState()
			if len(check.MistralBindings) != 1 {
				t.Fatal("late trace revived cancelled preference")
			}
		})
	}
}

func TestMistralBindingSourceAndExpiryInvalidation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*config.Config)
	}{
		{"upstream_key", func(c *config.Config) { c.Accounts[0].RuntimeKey = "new-synthetic-key" }},
		{"base", func(c *config.Config) { c.Mistral.BaseURL = "https://different.invalid/v1" }},
		{"proxy", func(c *config.Config) { c.Accounts[0].ProxyURL = "http://127.0.0.1:8080" }},
		{"group", func(c *config.Config) { c.Accounts[0].Group = "different-group" }},
		{"account_disabled", func(c *config.Config) { c.Accounts[0].Enabled = false }},
		{"account_removed", func(c *config.Config) { c.Accounts = nil }},
		{"provider", func(c *config.Config) { c.Accounts[0].Provider = config.ProviderModal }},
		{"caller_rotated", func(c *config.Config) { c.ClientKeys[0].KeyHash = config.HashKey("new-downstream-key") }},
		{"caller_group", func(c *config.Config) { c.ClientKeys[0].Group = "other" }},
		{"caller_disabled", func(c *config.Config) { c.ClientKeys[0].Enabled = false }},
		{"caller_removed", func(c *config.Config) { c.ClientKeys = nil }},
		{"sticky_off", func(c *config.Config) { c.Routing.Sticky = false }},
		{"expired", func(c *config.Config) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := mistralBindingConfig()
			now := time.Now().UTC()
			p := New(nil)
			p.SetClock(func() time.Time { return now })
			key := strings.Repeat("a", 64)
			lease, err := p.Acquire(context.Background(), cfg, Request{Group: config.MistralGroup, ClientKeyID: "caller", Affinity: key, RequestID: "sent"})
			if err != nil {
				t.Fatal(err)
			}
			lease.MarkSent()
			lease.Release()
			saved, _ := p.ExportState()
			if len(saved.MistralBindings) != 1 {
				t.Fatal("test checkpoint missing")
			}
			tc.change(cfg)
			if tc.name == "expired" {
				now = saved.MistralBindings[0].ExpiresAt
			}
			p.ReconcileMistralBindings(cfg)
			if _, ok := p.BoundAccount(key); ok {
				t.Fatal("invalid live preference retained")
			}
			if dirty, _ := p.Dirty(); !dirty {
				t.Fatal("persisted removal not marked dirty")
			}
			restored := New(nil)
			restored.SetClock(func() time.Time { return now })
			restored.ImportMistralBindings(saved.MistralBindings, cfg)
			if _, ok := restored.BoundAccount(key); ok {
				t.Fatal("stale checkpoint accepted after restart")
			}
		})
	}
}

func TestMistralBindingEvictionAndAdmissionSourceCheck(t *testing.T) {
	cfg := mistralBindingConfig()
	p := New(nil)
	key := strings.Repeat("f", 64)
	req := Request{Group: config.MistralGroup, ClientKeyID: "caller", Affinity: key, RequestID: "sent"}
	lease, err := p.Acquire(context.Background(), cfg, req)
	if err != nil {
		t.Fatal(err)
	}
	lease.MarkSent()
	lease.Release()
	p.ExportState()
	// A snapshot change not yet reconciled still cannot hit the old source.
	cfg.ClientKeys[0].KeyHash = config.HashKey("rotated-key")
	lease, err = p.Acquire(context.Background(), cfg, req)
	if err != nil || lease.Decision.BindingResult != "invalidated" {
		t.Fatal("admission trusted stale downstream source", err)
	}
	lease.MarkSent()
	lease.Release()
	p.ExportState()
	p.mu.Lock()
	for i := 0; i < maxBindings; i++ {
		p.putProvisionalLocked(cfg, Request{Affinity: fmt.Sprintf("%064x", i), RequestID: "unsent"}, "m1", time.Now())
	}
	p.mu.Unlock()
	if p.BindingCount() != maxBindings {
		t.Fatal("global LRU cap changed")
	}
	if _, ok := p.BoundAccount(key); ok {
		t.Fatal("LRU failed to evict old preference")
	}
	if dirty, _ := p.Dirty(); !dirty {
		t.Fatal("eviction can resurrect saved binding")
	}
	saved, _ := p.ExportState()
	if len(saved.MistralBindings) != 0 {
		t.Fatal("unsent/evicted records persisted")
	}
}
