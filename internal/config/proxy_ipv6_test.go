package config

import (
	"strings"
	"testing"
)

func TestValidateIPv6ProxyURL(t *testing.T) {
	for _, raw := range []string{
		"", "http://proxy.example", "http://127.0.0.1:8080",
		"http://[2001:db8::10]:8080", "https://[2001:db8::10]:8443",
		"socks5://[::1]:1080", "socks5h://[::1]:1080", "http://[::1]",
		"http://fake-user:fake%40password@[2001:db8::10]:8080",
		"socks5h://[fe80::1%25en0]:1080",
	} {
		if err := ValidateProxyURL(raw); err != nil {
			t.Errorf("valid proxy syntax rejected: %v", err)
		}
	}
	for _, raw := range []string{
		"http://2001:db8::10:8080", "socks5h://::1:1080",
		"http://[invalid::ip]:8080", "http://[proxy.example]:8080",
		"http://[127.0.0.1]:8080", "http://:8080",
		"http://fake-user:private-marker@2001:db8::10:8080",
	} {
		err := ValidateProxyURL(raw)
		if err == nil {
			t.Fatal("invalid IPv6 proxy syntax accepted")
		}
		if strings.Contains(err.Error(), "fake-user") || strings.Contains(err.Error(), "private-marker") {
			t.Fatal("proxy validation error exposed credentials")
		}
	}
}

func TestGoIPv6EgressPinCanonicalization(t *testing.T) {
	c := Default()
	c.OpenCode.GoEgressCheckURL = "https://ipv6-check.example/ip"
	c.Accounts = []Account{{ID: "go-v6", Provider: ProviderOpenCodeGo, RuntimeKey: "fake-go-ipv6-key", Enabled: true, ProxyURL: "socks5h://[2001:db8::10]:1080", EgressIP: "2001:0db8:0000:0000:0000:0000:0000:0077"}}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if c.Accounts[0].EgressIP != "2001:db8::77" {
		t.Fatal("IPv6 egress pin was not canonicalized")
	}
	c.Accounts[0].EgressIP = "[2001:db8::77]"
	if c.Normalize() == nil {
		t.Fatal("proxy authority syntax accepted as a bare IP pin")
	}
}
