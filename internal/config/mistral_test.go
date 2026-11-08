package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestMistralProviderMigrationAndRouting(t *testing.T) {
	c := Default()
	c.Mistral = Mistral{} // old version-1 configuration
	c.Accounts = []Account{{ID: "m1", RuntimeKey: "mstrl_mock_key_1", Enabled: true}, {ID: "m2", Provider: ProviderMistral, RuntimeKey: "sk-opaque-mistral-key", Enabled: true}}
	c.ClientKeys = []ClientKey{{ID: "ck", Group: MistralGroup, KeyHash: HashKey("client"), Enabled: true}}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	for _, a := range c.Accounts {
		if a.Provider != ProviderMistral || a.Group != MistralGroup || c.RuntimeBase(a) != "https://api.mistral.ai/v1" {
			t.Fatal("Mistral routed to another pool")
		}
	}
	if c.ClientKeys[0].Provider != ProviderMistral || !reflect.DeepEqual(c.ForProvider(ProviderMistral).Routing, c.Routing) {
		t.Fatal("default routing differs from OpenDesign")
	}
	r := c.Routing
	r.Strategy, r.OnBindingBusy = "roundrobin", "reject"
	c.Mistral.Routing = &r
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if c.ForProvider(ProviderMistral).Routing.Strategy != "roundrobin" || c.Routing.Strategy != "smart" {
		t.Fatal("routing leaked into OpenDesign")
	}
	clone := c.Clone()
	clone.Mistral.Routing.Strategy = "ordered"
	if c.Mistral.Routing.Strategy != "roundrobin" {
		t.Fatal("mutable routing shared")
	}
	if StrictResponses(ProviderMistral) || !WriteOnlyCredentials(ProviderMistral) {
		t.Fatal("stateless Responses confused with credential secrecy")
	}
	b, _ := json.Marshal(c)
	var restored Config
	if json.Unmarshal(b, &restored) != nil || restored.Normalize() != nil || restored.Mistral.Routing.Strategy != "roundrobin" {
		t.Fatal("round-trip failed")
	}
}

func TestMistralPollingOverrides(t *testing.T) {
	c := Default()
	if c.Mistral.ModelInterval() != 600 || c.Mistral.DetailLog != nil || c.ForProvider(ProviderMistral).Quota != c.Quota {
		t.Fatal("legacy polling/logging defaults changed")
	}
	q := Quota{Enabled: false, IntervalSec: 120, Concurrency: 2, StaleSec: 600}
	c.Mistral.Quota = &q
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if c.ForProvider(ProviderMistral).Quota != q || c.ForProvider(ProviderTokenHarbor).Quota != c.Quota {
		t.Fatal("polling override lost or crossed pools without routing override")
	}
	cloned := c.Clone()
	cloned.Mistral.Quota.IntervalSec = 300
	if c.Mistral.Quota.IntervalSec != 120 {
		t.Fatal("polling override aliases clone")
	}
	for _, n := range []int{-1, 10, 86401} {
		bad := c.Clone()
		bad.Mistral.ModelSyncSec = &n
		if bad.Normalize() == nil {
			t.Fatal("invalid catalog interval accepted")
		}
	}
	bad := c.Clone()
	bad.Mistral.Quota.StaleSec = 1
	if bad.Normalize() == nil {
		t.Fatal("invalid independent quota settings accepted")
	}
}

func TestMistralValidation(t *testing.T) {
	for _, key := range []string{"mstrl_mock-key", "opaque-key-without-prefix", "sk-overlapping-prefix"} {
		if !ValidRuntimeKeyFor(ProviderMistral, key) {
			t.Fatal("valid key rejected")
		}
	}
	for _, key := range []string{"", "short", "key with space", "key\r\ninjection", strings.Repeat("x", 513), "令牌不能当ASCII"} {
		if ValidRuntimeKeyFor(ProviderMistral, key) {
			t.Fatal("invalid key accepted")
		}
	}
	for _, base := range []string{"https://api.mistral.ai", "https://u:secret@host/v1", "https://host/v1?x=y", "https://host/v1?", "https://host/v1#x", "ftp://host/v1"} {
		c := Default()
		c.Mistral.BaseURL = base
		if c.Normalize() == nil {
			t.Fatalf("invalid base accepted: %s", base)
		}
	}
	for _, account := range []Account{
		{RuntimeKey: "mstrl_mock-key", ControlKey: "vela_ctrl_invalid"},
		{RuntimeKey: "mstrl_mock-key", Group: DefaultGroup},
		{RuntimeKey: "sk-amr-other", Group: MistralGroup},
	} {
		c := Default()
		c.Accounts = []Account{account}
		if c.Normalize() == nil {
			t.Fatal("control key or cross-pool group accepted")
		}
	}
	c := Default()
	r := c.Routing
	r.Strategy = "invalid"
	c.Mistral.Routing = &r
	if c.Normalize() == nil {
		t.Fatal("invalid routing accepted")
	}
}
