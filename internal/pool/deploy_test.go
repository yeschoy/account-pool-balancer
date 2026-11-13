package pool

import (
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
)

func TestDeployCatalogGateSourceAndNoForeignFunding(t *testing.T) {
	a := config.Account{ID: "d", Provider: config.ProviderDeploy, Group: "deploy", Enabled: true, RuntimeKey: "opaque_deploy_example"}
	c := config.Default()
	c.Accounts = []config.Account{a}
	now := time.Now()
	s := &quota.Snapshot{CatalogSource: quota.ResponseBindingSource(c, a), CatalogAt: now, Catalog: map[string]bool{"native": true}, PlanAt: now, Eligible: true, WalletUSD: "0", WalletAt: now}
	p := New(func(string) *quota.Snapshot { return s })
	p.mu.Lock()
	defer p.mu.Unlock()
	if why := p.unavailableLocked(c, a, "missing", now); why != "not-entitled" {
		t.Fatal("fresh catalog ignored", why)
	}
	if why := p.unavailableLocked(c, a, "native", now); why != "" {
		t.Fatal("foreign funding blocked Deploy", why)
	}
	if got := p.classLocked(c, "native", now); got != "unknown" {
		t.Fatal("Deploy taught OpenDesign a funding class", got)
	}
	s.CatalogError = "HTTP 503"
	if why := p.unavailableLocked(c, a, "missing", now); why != "" {
		t.Fatal("failed catalog treated as fresh", why)
	}
	s.CatalogError = ""
	s.CatalogAt = now.Add(-time.Hour)
	if why := p.unavailableLocked(c, a, "missing", now); why != "" {
		t.Fatal("expired catalog treated as fresh", why)
	}
	s.CatalogAt = now
	a.RuntimeKey = "changed_deploy_example"
	if why := p.unavailableLocked(c, a, "missing", now); why != "" {
		t.Fatal("wrong-source catalog survived", why)
	}
}
