package config

import (
	"encoding/json"
	"testing"
)

func TestPoolSwitchesBackwardsCompatibility(t *testing.T) {
	c := Default()
	for _, p := range ManagedPools() {
		if !c.PoolEnabled(p) {
			t.Fatal("legacy pool disabled", p)
		}
	}
	c.PoolSwitches = map[string]bool{ProviderFactory: false, ProviderDeploy: false, "telnyx": false}
	if e := c.Normalize(); e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(c)
	out := Default()
	if json.Unmarshal(b, out) != nil || out.Normalize() != nil || out.PoolEnabled(ProviderFactory) || !out.PoolEnabled(ProviderMistral) {
		t.Fatal("roundtrip/default")
	}
	copy := out.Clone()
	copy.PoolSwitches[ProviderFactory] = true
	if out.PoolEnabled(ProviderFactory) {
		t.Fatal("clone shared map")
	}
	out.PoolSwitches["typo"] = false
	if out.Normalize() == nil {
		t.Fatal("unknown pool accepted")
	}
}
