package config

import "testing"

func TestGoEgressPinValidationSharingAndSettings(t *testing.T) {
	c := Default()
	c.OpenCode.GoEgressCheckURL = "https://ip-check.example/ip"
	c.Accounts = []Account{
		{ID: "go1", Provider: ProviderOpenCodeGo, RuntimeKey: "go-test-key-one", Enabled: true, EgressIP: "::ffff:203.0.113.7"},
		{ID: "go2", Provider: ProviderOpenCodeGo, RuntimeKey: "go-test-key-two", Enabled: true, EgressIP: "203.0.113.7"},
	}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if c.Accounts[0].EgressIP != "203.0.113.7" {
		t.Fatal("IP pin not canonicalized")
	}
	if c.OpenCode.GoPolicyCooldownSec != 300 || c.OpenCode.GoEgressCheckSec != 60 {
		t.Fatal("Go safety defaults missing")
	}
	c.OpenCode.GoEgressCheckURL = ""
	if c.Normalize() == nil {
		t.Fatal("pin accepted without probe endpoint")
	}
	c.OpenCode.GoEgressCheckURL = "https://user:password@ip-check.example/ip"
	if c.Normalize() == nil {
		t.Fatal("probe endpoint credentials accepted")
	}
	c.OpenCode.GoEgressCheckURL = "https://ip-check.example/ip"
	c.Accounts[0].EgressIP = "not-an-ip"
	if c.Normalize() == nil {
		t.Fatal("invalid IP pin accepted")
	}
	c.Accounts[0].EgressIP = "203.0.113.7"
	c.OpenCode.GoPolicyCooldownSec = -1
	if c.Normalize() == nil {
		t.Fatal("invalid policy cooldown accepted")
	}
}
