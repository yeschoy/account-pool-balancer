package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTokenHarborConfiguration(t *testing.T) {
	c := Default()
	c.Accounts = []Account{{ID: "th", RuntimeKey: "thk_live_mock-secret", Enabled: true}}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	a := c.Accounts[0]
	if a.Provider != ProviderTokenHarbor || a.Group != TokenHarborGroup || c.RuntimeBase(a) != "https://tokenharbor.ai/v1" {
		t.Fatalf("wrong pool/base: %s %s %s", a.Provider, a.Group, c.RuntimeBase(a))
	}
	c.Routing.Sticky = false
	th := c.ForProvider(ProviderTokenHarbor)
	if !th.Routing.Sticky || th.Routing.OnBindingBusy != "reject" || c.Routing.Sticky {
		t.Fatal("cache defaults changed global routing")
	}
	independent := th.Routing
	independent.Strategy = "ordered"
	c.TokenHarbor.Routing = &independent
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if c.ForProvider(ProviderTokenHarbor).Routing.Strategy != "ordered" {
		t.Fatal("independent routing lost")
	}
	old := Default()
	old.TokenHarbor = TokenHarbor{}
	old.TokenHarborErrorRules, old.TokenHarborRetryRules = nil, nil
	if err := old.Normalize(); err != nil {
		t.Fatal(err)
	}
	if old.TokenHarbor.BaseURL == "" || len(old.TokenHarborErrorRules) == 0 {
		t.Fatal("old config not migrated")
	}
	for _, base := range []string{"https://example.com", "https://user:pass@example.com/v1", "https://example.com/v1?token=x", "https://example.com/v1?", "https://example.com/v1#x"} {
		t.Run(base, func(t *testing.T) {
			bad := c.Clone()
			bad.TokenHarbor.BaseURL = base
			if bad.Normalize() == nil {
				t.Fatal("accepted invalid base")
			}
		})
	}
	bad := c.Clone()
	bad.TokenHarborErrorRules[0].Action = "invalid"
	if bad.Normalize() == nil {
		t.Fatal("accepted invalid rules")
	}
	bad = c.Clone()
	bad.TokenHarbor.Routing.Strategy = "invalid"
	if bad.Normalize() == nil {
		t.Fatal("accepted invalid routing")
	}
	bad = c.Clone()
	bad.Accounts = append(bad.Accounts, Account{ID: "od", RuntimeKey: "sk-amr-mock", Group: TokenHarborGroup})
	if bad.Normalize() == nil {
		t.Fatal("mixed provider group accepted")
	}
	b, _ := json.Marshal(c)
	if !strings.Contains(string(b), `"tokenHarbor"`) {
		t.Fatal("settings not persisted")
	}
}

// The egress region decides each account's timezone, so a value the zone table
// cannot describe must fail loudly rather than be silently dropped: an
// operator who typed "USA" would otherwise keep a whole UTC pool.
func TestTokenHarborEgressRegion(t *testing.T) {
	c := Default()
	c.TokenHarbor.EgressRegion = " us "
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if c.TokenHarbor.EgressRegion != "US" {
		t.Fatalf("region not normalized: %q", c.TokenHarbor.EgressRegion)
	}
	for _, bad := range []string{"USA", "ZZ", "Atlantis", "1"} {
		c := Default()
		c.TokenHarbor.EgressRegion = bad
		if err := c.Normalize(); err == nil {
			t.Fatalf("region %q accepted", bad)
		} else if !strings.Contains(err.Error(), "egressRegion") {
			t.Fatalf("region %q: unhelpful error %v", bad, err)
		}
	}
	// Empty stays empty: no region means no claim, never a guessed one.
	c = Default()
	c.TokenHarbor.EgressRegion = "   "
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if c.TokenHarbor.EgressRegion != "" {
		t.Fatalf("blank region became %q", c.TokenHarbor.EgressRegion)
	}
}
