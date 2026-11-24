package pool

import (
	"context"
	"errors"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
)

// A spent Factory window is not a cooldown: it comes back at a measured time.
// When every account is out — one cooling down for a model its plan lacks, one
// out of its 5-hour window — the wait reported to the caller must be the
// window reset, not the unrelated long cooldown.
func TestFactoryQuotaExhaustedLooksUpWindowReset(t *testing.T) {
	now := time.Now()
	cfg := testConfig(
		config.Account{ID: "k", Provider: config.ProviderFactory, Group: "factory", RuntimeKey: "fk-0123456789abcdef0123456789abcdef"},
		config.Account{ID: "z", Provider: config.ProviderFactory, Group: "factory", RuntimeKey: "fk-fedcba9876543210fedcba9876543210"},
	)
	model := "gemini-4-neon"
	reset := now.Add(26 * time.Minute)
	snaps := map[string]*quota.Snapshot{
		"z": {
			CatalogSource: quota.ResponseBindingSource(cfg, cfg.Accounts[1]),
			Factory: &quota.FactoryState{
				Source:     quota.ResponseBindingSource(cfg, cfg.Accounts[1]),
				QuotaState: "ok",
				QuotaAt:    now,
				Windows: []quota.FactoryWindow{
					{Pool: "standard", Name: "5h", UsedPercent: 100, ResetsAt: reset, Active: true},
					{Pool: "standard", Name: "weekly", UsedPercent: 60, ResetsAt: now.Add(5 * 24 * time.Hour), Active: true},
				},
			},
		},
	}
	p := New(func(id string) *quota.Snapshot { return snaps[id] })
	p.SetClock(func() time.Time { return now })
	p.Cooldown("k", model, now.Add(20*time.Hour), 400, "factory-model-id-rejected", "factory-model-id-rejected")
	if _, ex := p.Preview(cfg, "factory", model); ex["k"] != "not-entitled" || ex["z"] != "quota-exhausted" {
		t.Fatalf("exclusions: %v", ex)
	}
	_, err := p.Acquire(context.Background(), cfg, Request{Group: "factory", Model: model})
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("want unavailable, got %v", err)
	}
	if ue.RetryAfter < 25*time.Minute || ue.RetryAfter > 27*time.Minute {
		t.Fatalf("want the 5h window reset (~26m), got %v", ue.RetryAfter)
	}
	if ue.Details["quota-exhausted"] != 1 {
		t.Fatalf("details: %v", ue.Details)
	}
	// A stale read must not exclude the account: unknown is not exhausted, so
	// an old 100% snapshot never keeps a model down.
	snaps["z"].Factory.QuotaAt = now.Add(-time.Hour)
	if _, ex := p.Preview(cfg, "factory", model); ex["z"] != "" {
		t.Fatalf("stale snapshot excluded z: %v", ex)
	}
}
