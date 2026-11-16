package pool

import (
	"context"
	"math"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
)

func TestALTAPILowBalanceGate(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	for _, tc := range []struct {
		name   string
		change func(*config.Config, *config.Account, *quota.Snapshot)
		want   string
	}{
		{"below", nil, "low-balance"},
		{"equal", func(c *config.Config, a *config.Account, s *quota.Snapshot) { s.ALTAPI.BalanceUSD = 0.5 }, ""},
		{"above", func(c *config.Config, a *config.Account, s *quota.Snapshot) { s.ALTAPI.BalanceUSD = 0.501 }, ""},
		{"zero known", func(c *config.Config, a *config.Account, s *quota.Snapshot) { s.ALTAPI.BalanceUSD = 0 }, "low-balance"},
		{"zero threshold equal", func(c *config.Config, a *config.Account, s *quota.Snapshot) {
			c.ALTAPI.MinBalanceUSD = 0
			s.ALTAPI.BalanceUSD = 0
		}, ""},
		{"disabled switch", func(c *config.Config, a *config.Account, s *quota.Snapshot) { c.ALTAPI.LowBalanceDisable = false }, ""},
		{"unknown key only", func(c *config.Config, a *config.Account, s *quota.Snapshot) {
			s.ALTAPI.Source = "key"
			s.ALTAPI.BalanceUSD = -1
		}, ""},
		{"unknown missing", func(c *config.Config, a *config.Account, s *quota.Snapshot) { s.ALTAPI = nil }, ""},
		{"stale", func(c *config.Config, a *config.Account, s *quota.Snapshot) {
			s.ALTAPI.ReadAt = now.Add(-time.Duration(c.ALTAPI.EffectiveQuota().StaleSec) * time.Second).Format(time.RFC3339)
		}, ""},
		{"failed", func(c *config.Config, a *config.Account, s *quota.Snapshot) { s.LastError = "console read failed" }, ""},
		{"invalid date", func(c *config.Config, a *config.Account, s *quota.Snapshot) { s.ALTAPI.ReadAt = "invalid" }, ""},
		{"future", func(c *config.Config, a *config.Account, s *quota.Snapshot) {
			s.ALTAPI.ReadAt = now.Add(time.Hour).Format(time.RFC3339)
		}, ""},
		{"nonfinite", func(c *config.Config, a *config.Account, s *quota.Snapshot) { s.ALTAPI.BalanceUSD = math.NaN() }, ""},
		{"legacy source", func(c *config.Config, a *config.Account, s *quota.Snapshot) { s.ALTAPI.BalanceSource = "" }, ""},
		{"changed session", func(c *config.Config, a *config.Account, s *quota.Snapshot) { a.ControlKey = "rotated" }, ""},
		{"changed uid", func(c *config.Config, a *config.Account, s *quota.Snapshot) { a.UserID = "43" }, ""},
		{"changed runtime", func(c *config.Config, a *config.Account, s *quota.Snapshot) { a.RuntimeKey = "changed-runtime" }, ""},
		{"changed base", func(c *config.Config, a *config.Account, s *quota.Snapshot) {
			c.ALTAPI.BaseURL = "https://different.example/v1"
		}, ""},
		{"changed proxy", func(c *config.Config, a *config.Account, s *quota.Snapshot) {
			a.ProxyURL = "http://different.example:8888"
		}, ""},
		{"manual disabled", func(c *config.Config, a *config.Account, s *quota.Snapshot) { a.Enabled = false }, "disabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config.Default()
			c.ALTAPI.LowBalanceDisable, c.ALTAPI.MinBalanceUSD = true, 0.5
			a := config.Account{ID: "a", Provider: config.ProviderALTAPI, Group: "altapi", Enabled: true, RuntimeKey: "runtime-a", ControlKey: "session-a", UserID: "42"}
			s := &quota.Snapshot{ALTAPI: &quota.ALTAPIState{Source: "session", BalanceSource: quota.ResponseBindingSource(c, a), ReadAt: now.Format(time.RFC3339), BalanceUSD: 0.499}}
			if tc.change != nil {
				tc.change(c, &a, s)
			}
			c.Accounts = []config.Account{a}
			p := New(func(string) *quota.Snapshot { return s })
			p.SetClock(func() time.Time { return now })
			p.mu.Lock()
			why := p.unavailableLocked(c, a, "m1", now)
			p.mu.Unlock()
			if why != tc.want {
				t.Fatalf("got %q want %q", why, tc.want)
			}
			view := p.Views(c)[a.ID]
			if tc.want == "low-balance" && (view.Status != "low-balance" || view.State != nil || !c.Accounts[0].Enabled) {
				t.Fatal("gate mutated account state", view)
			}
		})
	}
}

func TestALTAPILowBalanceStickyAndRecovery(t *testing.T) {
	c := config.Default()
	c.ALTAPI.LowBalanceDisable, c.ALTAPI.MinBalanceUSD = true, 2
	c.Accounts = []config.Account{
		{ID: "a", Provider: config.ProviderALTAPI, Group: "altapi", Enabled: true, RuntimeKey: "runtime-a", ControlKey: "session-a", UserID: "41"},
		{ID: "b", Provider: config.ProviderALTAPI, Group: "altapi", Enabled: true, RuntimeKey: "runtime-b", ControlKey: "session-b", UserID: "42"},
	}
	now := time.Now()
	snaps := map[string]*quota.Snapshot{}
	for _, a := range c.Accounts {
		snaps[a.ID] = &quota.Snapshot{ALTAPI: &quota.ALTAPIState{Source: "session", BalanceSource: quota.ResponseBindingSource(c, a), BalanceUSD: 10, ReadAt: now.Format(time.RFC3339)}}
	}
	p := New(func(id string) *quota.Snapshot { return snaps[id] })
	p.SetClock(func() time.Time { return now })
	req := Request{Group: "altapi", Model: "m1", Affinity: "sticky", Explicit: true}
	view := c.ForProvider(config.ProviderALTAPI)
	first, err := p.Acquire(context.Background(), view, req)
	if err != nil {
		t.Fatal(err)
	}
	owner := first.Account.ID
	first.Release()
	snaps[owner].ALTAPI.BalanceUSD = 1
	next, err := p.Acquire(context.Background(), view, req)
	if err != nil {
		t.Fatal(err)
	}
	if next.Account.ID == owner {
		t.Fatal("sticky bypassed low-balance gate")
	}
	next.Release()
	snaps[owner].ALTAPI.BalanceUSD = 2 // equality is eligible, no explicit restore required
	recovered, err := p.Acquire(context.Background(), view, Request{Group: "altapi", Model: "m1", Excluded: map[string]bool{next.Account.ID: true}})
	if err != nil || recovered.Account.ID != owner {
		t.Fatal("balance refresh did not restore account", err)
	}
	recovered.Release()
	p.Quarantine(owner, 403, "manual-check", "manual-quarantine")
	if got := p.Views(c)[owner].Status; got != "quarantined" {
		t.Fatal("balance recovery removed quarantine", got)
	}
	// A different provider with no native funding snapshot must not inherit
	// ALTAPI's configured dollar threshold.
	other := config.Account{ID: "other", Provider: config.ProviderDeploy, Enabled: true, RuntimeKey: "other-runtime"}
	p.mu.Lock()
	why := p.unavailableLocked(c, other, "m1", now)
	p.mu.Unlock()
	if why != "" {
		t.Fatal("ALTAPI threshold leaked to Deploy", why)
	}
}
