package config

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestOpenCodeMigrationAndIsolation(t *testing.T) {
	old := Default()
	old.OpenCode = OpenCode{}
	old.Accounts = []Account{{ID: "old", RuntimeKey: "sk-amr-old-key", Group: "default", Enabled: true}}
	b, _ := json.Marshal(old)
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if c.OpenCode.Enabled || c.OpenCode.Compatibility || c.OpenCode.GoBase != "https://opencode.ai/zen/go/v1" || c.Accounts[0].Provider != ProviderOpenDesign {
		t.Fatalf("migration changed old semantics: %+v", c.OpenCode)
	}
	for _, p := range []string{ProviderOpenCodeZen, ProviderOpenCodeGo} {
		if ResolveProvider(p, "sk-overlapping-prefix") != p || ResolveProvider(p, "rv_overlapping-prefix") != p {
			t.Fatal("opaque key overridden by prefix")
		}
		if !ValidRuntimeKeyFor(p, "opaque-key") || ValidRuntimeKeyFor(p, "opaque key") {
			t.Fatal("invalid token acceptance")
		}
		c.Accounts = append(c.Accounts, Account{ID: p, Provider: p, RuntimeKey: "sk-" + p, Enabled: true})
		c.ClientKeys = append(c.ClientKeys, ClientKey{ID: p, Group: p, Provider: p, KeyHash: HashKey(p), Enabled: true})
	}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	for _, a := range c.Accounts[1:] {
		if a.Group != a.Provider {
			t.Fatalf("not isolated %+v", a)
		}
	}
	c.Accounts[2].Group = c.Accounts[1].Group
	if c.Normalize() == nil {
		t.Fatal("mixed tier group accepted")
	}
}

func TestOpenCodeValidatedSettingsAndIndependentRouting(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	before := s.Get()
	if _, err := s.Update(-1, func(c *Config) error { c.OpenCode.GoBase = "https://user:secret@example.com/v1"; return nil }); err == nil {
		t.Fatal("credentialed URL accepted")
	}
	if s.Get() != before {
		t.Fatal("invalid update changed config")
	}
	for _, base := range []string{"https://example.com", "https://user:secret@example.com/v1", "https://example.com/v1?key=secret"} {
		if _, err := s.Update(-1, func(c *Config) error { c.OpenCode.GoProtocolBases = map[string]string{"messages": base}; return nil }); err == nil {
			t.Fatal("invalid protocol base accepted")
		}
	}
	_, err = s.Update(-1, func(c *Config) error { r := c.Routing; r.Strategy = "ordered"; c.OpenCode.GoRouting = &r; return nil })
	if err != nil {
		t.Fatal(err)
	}
	c := s.Get()
	if c.Routing.Strategy != "smart" || c.ForProvider(ProviderOpenCodeGo).Routing.Strategy != "ordered" || c.ForProvider(ProviderOpenCodeZen).Routing.Strategy != "smart" {
		t.Fatal("routing leaked")
	}
	if _, err := s.Update(-1, func(c *Config) error { c.OpenCode.GoRouting.Strategy = "not-a-strategy"; return nil }); err == nil {
		t.Fatal("invalid independent routing accepted")
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if s.Get().ForProvider(ProviderOpenCodeGo).Routing.Strategy != "ordered" {
		t.Fatal("routing not persisted")
	}
}
