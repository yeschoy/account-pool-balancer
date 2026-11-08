package pool

import (
	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
	"testing"
	"time"
)

func TestMistralBudgetRankingAndSource(t *testing.T) {
	cfg := testConfig(config.Account{ID: "m", Provider: config.ProviderMistral, Group: config.MistralGroup, RuntimeKey: "mstrl_mock"})
	a := cfg.Accounts[0]
	now := time.Now()
	source := quota.ResponseBindingSource(cfg, a)
	snap := &quota.Snapshot{CatalogSource: source, Mistral: &quota.MistralState{Source: source, At: now, Budget: &quota.MistralBudget{Total: 30, UsedPercent: 75, Remaining: 7.5, Currency: "USD", ResetsAt: now.Add(24 * time.Hour)}}}
	p := New(func(string) *quota.Snapshot { return snap })
	p.mu.Lock()
	defer p.mu.Unlock()
	seat := p.seatLocked(cfg, a, "m", now)
	if !seat.Known || seat.Used != 75 || seat.Pace <= 0 || len(seat.Soon) != 1 {
		t.Fatal("measured budget ignored for routing")
	}
	snap.Mistral.Budget.Total = 0
	if p.seatLocked(cfg, a, "m", now).Known {
		t.Fatal("zero allowance treated as a full plan for ranking")
	}
	snap.Mistral.Budget.Total = 30
	snap.Mistral.Error = "HTTP 403"
	if p.seatLocked(cfg, a, "m", now).Known {
		t.Fatal("failed budget treated as current")
	}
	snap.Mistral.Error = ""
	snap.Mistral.Budget.ResetsAt = now.Add(-time.Second)
	if p.seatLocked(cfg, a, "m", now).Known {
		t.Fatal("expired budget treated as full")
	}
	snap.Mistral.Budget.ResetsAt = now.Add(time.Hour)
	a.ProxyURL = "http://127.0.0.1:8080"
	if p.seatLocked(cfg, a, "m", now).Known {
		t.Fatal("old route budget reused")
	}
}
