package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestX50IndependentConfigAndKeys(t *testing.T) {
	c := Default()
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if ProviderForKey("sk-50x-native") != ProviderX50 || ResolveProvider("opendesign", "sk-50x-native") != ProviderX50 || GroupForProvider(ProviderX50) != "x50" {
		t.Fatal("key/group resolution")
	}
	old := c.ForProvider(ProviderX50).Routing
	c.Routing.Strategy = "ordered"
	if !reflect.DeepEqual(c.ForProvider(ProviderX50).Routing, old) {
		t.Fatal("inherited global routing")
	}
	c.Accounts = []Account{{ID: "x", RuntimeKey: "sk-50x-native", ControlKey: "session-for-billing", Enabled: true}}
	if e := c.Normalize(); e != nil {
		t.Fatal(e)
	}
	if c.Accounts[0].Provider != ProviderX50 || c.Accounts[0].Group != "x50" {
		t.Fatal("inference")
	}
	r := c.X50.EffectiveRouting()
	r.SessionHeaders = []string{"X-X50-Session"}
	c.X50.Routing = &r
	c.X50ErrorRules = c.X50ErrorRules[:0]
	c.X50RetryRules = c.X50RetryRules[:0]
	b, _ := json.Marshal(c)
	var loaded Config
	if e := json.Unmarshal(b, &loaded); e != nil {
		t.Fatal(e)
	}
	if e := loaded.Normalize(); e != nil {
		t.Fatal(e)
	}
	if len(loaded.X50ErrorRules) != 0 || len(loaded.X50RetryRules) != 0 || !reflect.DeepEqual(loaded.X50.Routing.SessionHeaders, r.SessionHeaders) {
		t.Fatal("roundtrip")
	}
	source := loaded.NativeBindingSource(loaded.Accounts[0])
	a := loaded.Accounts[0]
	a.ControlKey = "changed"
	if source == loaded.NativeBindingSource(a) {
		t.Fatal("session not in source")
	}
	for _, base := range []string{"https://u:p@bad/v1", "https://bad/api/v1", "https://bad/v1?q=1", "file:///v1"} {
		n := c.Clone()
		n.X50.BaseURL = base
		if n.Normalize() == nil {
			t.Fatal("accepted", base)
		}
	}
	if ValidRuntimeKeyFor(ProviderDeploy, "sk-50x-native") || ValidRuntimeKeyFor(ProviderALTAPI, "sk-50x-native") {
		t.Fatal("known X50 key accepted by another provider")
	}
	n := c.Clone()
	n.Accounts[0].Group = "deploy"
	if n.Normalize() == nil {
		t.Fatal("cross-pool group")
	}
	n = c.Clone()
	n.ClientKeys = []ClientKey{{ID: "k", Group: "x50", Provider: ProviderDeploy, KeyHash: HashKey("test"), Enabled: true}}
	if n.Normalize() == nil {
		t.Fatal("cross-pool client key")
	}
}
