package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFactoryConfigIsolationAndRoundTrip(t *testing.T) {
	c := Default()
	c.Routing.Strategy = "ordered"
	c.Quota.Enabled = false
	if !KnownProvider(ProviderFactory) || GroupForProvider(ProviderFactory) != "factory" || !WriteOnlyCredentials(ProviderFactory) {
		t.Fatal("provider registration")
	}
	if got := c.ForProvider(ProviderFactory); got.Routing.Strategy != "least-connections" || got.Routing.OnBindingBusy != "reject" || !got.Quota.Enabled || len(got.Pricing.Models) != 0 {
		t.Fatal("inherited foreign settings")
	}
	c.Accounts = []Account{{ID: "f", Provider: ProviderFactory, RuntimeKey: "fk-mock_test_key_1", Enabled: true}}
	r := c.Factory.EffectiveRouting()
	r.Strategy = "weighted"
	r.SessionHeaders = []string{"X-Factory-Session"}
	c.Factory.Routing = &r
	c.FactoryErrorRules = c.FactoryErrorRules[:0]
	c.FactoryRetryRules = c.FactoryRetryRules[:0]
	c.Factory.DetailLog = true
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(c)
	out := Default()
	if json.Unmarshal(b, out) != nil {
		t.Fatal("decode")
	}
	if e := out.Normalize(); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(c.Factory, out.Factory) || len(out.FactoryErrorRules) != 0 || out.Accounts[0].Group != "factory" {
		t.Fatal("roundtrip")
	}
	clone := out.Clone()
	clone.Factory.Models[0].ID = "changed"
	clone.Factory.Routing.SessionHeaders[0] = "X-Changed"
	if reflect.DeepEqual(out.Factory, clone.Factory) {
		t.Fatal("clone alias")
	}
	if !ValidRuntimeKeyFor(ProviderFactory, "fk-mock_test_key") || ValidRuntimeKeyFor(ProviderDeploy, "fk-mock_test_key") {
		t.Fatal("key boundary")
	}
	for _, g := range []string{"deploy", "default", "mistral"} {
		x := Default()
		x.Accounts = []Account{{ID: "f", Provider: ProviderFactory, Group: g, RuntimeKey: "fk-mock_test_key"}}
		if x.Normalize() == nil {
			t.Fatal("foreign group accepted", g)
		}
	}
	for _, base := range []string{"https://u:p@example.com", "https://example.com/v1", "https://example.com?x=1", "https://example.com?", "ftp://example.com"} {
		x := Default()
		x.Factory.BaseURL = base
		if x.Normalize() == nil {
			t.Fatal("bad base", base)
		}
	}
	x := Default()
	rr := x.Factory.EffectiveRouting()
	rr.MaxAccounts = 6
	x.Factory.Routing = &rr
	if x.Normalize() == nil {
		t.Fatal("unvalidated nested routing")
	}
}
