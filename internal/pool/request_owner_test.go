package pool

import (
	"context"
	"errors"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func TestRequestOwnerHonorsAdmissionAndNeverOverflows(t *testing.T) {
	cfg := testConfig(config.Account{ID: "a", Provider: config.ProviderMistral, Group: config.MistralGroup, RuntimeKey: "mstrl_owner_a", MaxConc: 1, MaxRPM: 1}, config.Account{ID: "b", Provider: config.ProviderMistral, Group: config.MistralGroup, RuntimeKey: "mstrl_owner_b", MaxConc: 5})
	cfg.Routing.OnBindingBusy = "overflow"
	p := New(nil)
	req := Request{PinnedAccountID: "a", Group: config.MistralGroup, Model: "m", RequestID: "owner"}
	first, e := p.Acquire(context.Background(), cfg, req)
	if e != nil {
		t.Fatal(e)
	}
	if first.Account.ID != "a" || first.Decision.Reason != "request-owner" {
		t.Fatal("wrong owner")
	}
	if other, e := p.Acquire(context.Background(), cfg, req); e == nil {
		other.Release()
		t.Fatal("busy owner overflowed")
	} else {
		var ce *CapacityError
		if !errors.As(e, &ce) || ce.BlockedBy != "concurrency" {
			t.Fatal(e)
		}
	}
	first.MarkSent()
	first.MarkSent()
	first.Release()
	first.Release()
	if other, e := p.Acquire(context.Background(), cfg, req); e == nil {
		other.Release()
		t.Fatal("RPM reservation bypassed")
	} else {
		var ce *CapacityError
		if !errors.As(e, &ce) || ce.BlockedBy != "rpm" {
			t.Fatal(e)
		}
	}
	views := p.Views(cfg)
	if views["a"].Active != 0 || views["a"].RPM != 1 || views["b"].RPM != 0 {
		t.Fatalf("lease lifecycle/other account changed: %+v", views)
	}
}
func TestRequestOwnerWaitCancellationDoesNotOverflow(t *testing.T) {
	cfg := testConfig(config.Account{ID: "a", Provider: config.ProviderMistral, Group: config.MistralGroup, RuntimeKey: "mstrl_owner_a", MaxConc: 1}, config.Account{ID: "b", Provider: config.ProviderMistral, Group: config.MistralGroup, RuntimeKey: "mstrl_owner_b", MaxConc: 5})
	cfg.Routing.OnBindingBusy = "wait-overflow"
	p := New(nil)
	req := Request{PinnedAccountID: "a", Group: config.MistralGroup, Model: "m"}
	lease, err := p.Acquire(context.Background(), cfg, req)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	req.Wait = time.Second
	other, err := p.Acquire(ctx, cfg, req)
	if err == nil {
		other.Release()
		t.Fatal("wait cancellation borrowed another owner")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if p.Views(cfg)["b"].Active != 0 {
		t.Fatal("unrelated account reserved")
	}
}

func TestRequestOwnerCannotEscapeEligibility(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*config.Config, *Pool)
	}{
		{"wrong-group", func(c *config.Config, p *Pool) { c.Accounts[0].Group = "elsewhere" }},
		{"disabled", func(c *config.Config, p *Pool) { c.Accounts[0].Enabled = false }},
		{"wrong-provider", func(c *config.Config, p *Pool) {
			c.Accounts[0].Provider = config.ProviderOpenDesign
			c.Accounts[0].Group = "default"
			c.Accounts[0].RuntimeKey = "sk-amr-owner-test-account"
		}},
		{"quarantined", func(c *config.Config, p *Pool) { p.Quarantine("a", 401, "test", "test") }},
		{"unhealthy", func(c *config.Config, p *Pool) {
			c.Routing.HealthFilter = true
			c.Routing.MinimumHealth = 0.5
			for i := 0; i < MinHealthSamples; i++ {
				p.Sample("a", false)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConfig(config.Account{ID: "a", Provider: config.ProviderMistral, Group: config.MistralGroup, RuntimeKey: "mstrl_owner_a"}, config.Account{ID: "b", Provider: config.ProviderMistral, Group: config.MistralGroup, RuntimeKey: "mstrl_owner_b"})
			p := New(nil)
			tc.change(c, p)
			if e := c.Normalize(); e != nil {
				t.Fatal(e)
			}
			lease, e := p.Acquire(context.Background(), c, Request{PinnedAccountID: "a", Group: config.MistralGroup, Model: "m"})
			if e == nil {
				lease.Release()
				t.Fatal("invalid owner escaped to a usable account")
			}
		})
	}
}
