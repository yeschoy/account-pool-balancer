package config

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMistralCapacityAndResourceConfiguration(t *testing.T) {
	c := Default()
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if c.Mistral.BindingCapacity != nil || c.Mistral.EffectivePerformance().DetailSamplePercent != 100 {
		t.Fatal("legacy defaults changed")
	}
	for _, n := range []int{0, 1, 1000000} {
		cc := c.Clone()
		cc.Mistral.BindingCapacity = &n
		p := cc.Mistral.EffectivePerformance()
		cc.Mistral.Performance = &p
		if err := cc.Normalize(); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(cc)
		restored := &Config{}
		if err := json.Unmarshal(raw, restored); err != nil {
			t.Fatal(err)
		}
		if err := restored.Normalize(); err != nil || *restored.Mistral.BindingCapacity != n || *restored.Mistral.Performance != p {
			t.Fatal("round trip", err)
		}
	}
	for _, n := range []int{-1, 1000001} {
		cc := c.Clone()
		cc.Mistral.BindingCapacity = &n
		if cc.Normalize() == nil {
			t.Fatal("invalid capacity accepted", n)
		}
	}
	for _, change := range []func(*MistralPerformance){func(p *MistralPerformance) { p.MaxBufferedMB = 0 }, func(p *MistralPerformance) { p.MaxWaitingRequests = -1 }, func(p *MistralPerformance) { p.UploadTimeoutSec = 0 }, func(p *MistralPerformance) { p.WriteTimeoutSec = 601 }, func(p *MistralPerformance) { p.DetailSamplePercent = 101 }} {
		cc := c.Clone()
		p := cc.Mistral.EffectivePerformance()
		change(&p)
		cc.Mistral.Performance = &p
		if cc.Normalize() == nil {
			t.Fatal("invalid performance accepted")
		}
	}
}

func TestConfigUpdateInvalidatesMutableLookupIndexes(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Update(-1, func(n *Config) error {
		n.Accounts = []Account{{ID: "a", Provider: ProviderMistral, Group: "old", RuntimeKey: "mstrl_fake-a", Enabled: true}, {ID: "b", Provider: ProviderModal, Group: "destination", RuntimeKey: "wk-fake-b.ws-fake", Enabled: true}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	old := s.Get()
	if _, err = s.Update(-1, func(n *Config) error {
		if n.index != nil {
			return fmt.Errorf("mutable callback retained snapshot index")
		}
		n.Accounts[0].Group = "destination"
		if n.GroupProvider("destination") != ProviderMistral {
			return fmt.Errorf("stale group lookup during move")
		}
		n.Accounts[1].Group = "other"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if s.Get().index == nil || s.Get().GroupProvider("destination") != ProviderMistral || old.GroupProvider("destination") != ProviderModal {
		t.Fatal("new publication/old snapshot changed")
	}
}

func TestConfigDerivedIndexesPreserveSourcesAndClone(t *testing.T) {
	c := Default()
	c.Accounts = []Account{{ID: "m", Provider: ProviderMistral, Group: MistralGroup, RuntimeKey: "mstrl_synthetic", Enabled: true}}
	c.ClientKeys = []ClientKey{{ID: "client", Group: MistralGroup, KeyHash: HashKey("synthetic-client"), Enabled: true}}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	copy := c.Clone()
	if !reflect.DeepEqual(c, copy) || c.index == copy.index {
		t.Fatal("clone shares indexes or changes snapshot")
	}
	if _, ok := c.ClientKey("client"); !ok {
		t.Fatal("missing client")
	}
	a, ok := c.Account("m")
	if !ok {
		t.Fatal("missing account")
	}
	source := c.NativeBindingSource(a)
	if source != HashKey(a.Provider+"\x00"+a.RuntimeKey+"\x00"+a.ProxyURL+"\x00"+c.RuntimeBase(a)) {
		t.Fatal("hash changed")
	}
	copy.Accounts[0].RuntimeKey = "mstrl_new_synthetic"
	a, _ = copy.Account("m")
	if copy.NativeBindingSource(a) == source || c.Accounts[0].RuntimeKey == a.RuntimeKey {
		t.Fatal("stale source/clone alias")
	}
	copy.ClientKeys[0].Enabled = false
	if _, ok := copy.ClientKeyByHash(copy.ClientKeys[0].KeyHash); ok {
		t.Fatal("disabled client indexed as enabled")
	}
	if _, ok := c.ClientKeyByHash(c.ClientKeys[0].KeyHash); !ok {
		t.Fatal("clone mutated published key")
	}
}
