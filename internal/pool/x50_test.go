package pool

import (
	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
	"testing"
	"time"
)

func TestX50CatalogBalanceAndSource(t *testing.T) {
	a := config.Account{ID: "x", Provider: config.ProviderX50, Group: "x50", Enabled: true, RuntimeKey: "sk-50x-a", ControlKey: "s"}
	c := config.Default()
	c.Accounts = []config.Account{a}
	now := time.Now()
	zero := 0.0
	s := &quota.Snapshot{CatalogSource: quota.ResponseBindingSource(c, a), CatalogAt: now, Catalog: map[string]bool{"native": true}, WalletUSD: "0", WalletAt: now, PlanAt: now, Eligible: true, X50: &quota.X50State{At: now, BalanceUSD: &zero, Tier: "paid"}}
	p := New(func(string) *quota.Snapshot { return s })
	p.mu.Lock()
	defer p.mu.Unlock()
	if why := p.unavailableLocked(c, a, "native", now); why != "quota-exhausted" {
		t.Fatal(why)
	}
	s.X50.Tier = "free"
	if why := p.unavailableLocked(c, a, "native", now); why != "" {
		t.Fatal("wallet zero mistaken for free daily exhaustion", why)
	}
	s.X50.Tier = "paid"
	s.X50.Error = "billing401"
	if why := p.unavailableLocked(c, a, "native", now); why != "" {
		t.Fatal("failed history treated as zero", why)
	}
	s.X50.Error = ""
	s.X50.At = now.Add(-48 * time.Hour)
	if why := p.unavailableLocked(c, a, "native", now); why != "" {
		t.Fatal("stale treated as zero", why)
	}
	if why := p.unavailableLocked(c, a, "missing", now); why != "not-entitled" {
		t.Fatal(why)
	}
	if got := p.classLocked(c, "native", now); got != "unknown" {
		t.Fatal("foreign funding", got)
	}
	a.ControlKey = "changed"
	if why := p.unavailableLocked(c, a, "missing", now); why != "" {
		t.Fatal("wrong source", why)
	}
}
