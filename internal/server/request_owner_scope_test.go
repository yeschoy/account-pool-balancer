package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func TestRequestOwnerAllSourceAndCallerChangesFailClosed(t *testing.T) {
	changes := []struct {
		name   string
		change func(*config.Config, *config.ClientKey)
	}{
		{"base", func(c *config.Config, k *config.ClientKey) { c.Mistral.BaseURL = "https://changed.invalid/v1" }},
		{"proxy", func(c *config.Config, k *config.ClientKey) { c.Accounts[0].ProxyURL = "http://127.0.0.1:1234" }},
		{"runtime-key", func(c *config.Config, k *config.ClientKey) { c.Accounts[0].RuntimeKey = "mstrl_owner_rotated" }},
		{"account-disabled", func(c *config.Config, k *config.ClientKey) { c.Accounts[0].Enabled = false }},
		{"account-removed", func(c *config.Config, k *config.ClientKey) { c.Accounts = c.Accounts[1:] }},
		{"caller-rotated", func(c *config.Config, k *config.ClientKey) {
			k.KeyHash = config.HashKey("changed-client-key")
			c.ClientKeys[0].KeyHash = k.KeyHash
		}},
		{"caller-group", func(c *config.Config, k *config.ClientKey) { k.Group = "other-group"; c.ClientKeys[0].Group = k.Group }},
	}
	for _, tc := range changes {
		t.Run(tc.name, func(t *testing.T) {
			e := newMistralEnv(t)
			old := e.s.cfg.Get()
			key, _ := old.ClientKey("client1")
			account, _ := old.Account("m1")
			now := time.Now()
			token, err := e.s.ownerForLease(old, old, key, "m", account, requestOwner{enabled: true}, now)
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.s.cfg.Update(-1, func(c *config.Config) error { tc.change(c, &key); return nil })
			if err != nil {
				t.Fatal(err)
			}
			current := e.s.cfg.Get()
			h := http.Header{}
			h.Set(requestOwnerHeader, token)
			if _, err = e.s.readRequestOwner(current, key, "m", "/v1/responses", h, now); err == nil {
				t.Fatal("changed token scope accepted")
			}
		})
	}
}
func TestRequestOwnerRejectsChangedConfigAfterSelectionAndRestart(t *testing.T) {
	e := newMistralEnv(t)
	old := e.s.cfg.Get()
	ck, _ := old.ClientKey("client1")
	account, _ := old.Account("m1")
	token, err := e.s.ownerForLease(old, old, ck, "m", account, requestOwner{enabled: true}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.s.cfg.Update(-1, func(c *config.Config) error { c.Mistral.BaseURL = "https://new-source.invalid/v1"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.s.ownerForLease(e.s.cfg.Get(), old, ck, "m", account, requestOwner{enabled: true}, time.Now()); err == nil {
		t.Fatal("late source change sent an old-config request")
	}
	other := &Server{secret: []byte(strings.Repeat("z", 32))}
	h := http.Header{}
	h.Set(requestOwnerHeader, token)
	if _, err = other.readRequestOwner(old, ck, "m", "/v1/responses", h, time.Now()); err == nil {
		t.Fatal("unrelated server secret accepted owner token")
	}
	// Same persisted routing secret may restore unexpired tokens; no lease/RPM is
	// restored and account/caller/source/eligibility are always checked again.
	same := &Server{secret: append([]byte(nil), e.s.secret...)}
	if _, err = same.readRequestOwner(old, ck, "m", "/v1/responses", h, time.Now()); err != nil {
		t.Fatal("same routing secret cannot validate bounded token")
	}
}
func TestRequestOwnerHeaderMultiplicityAndProtocolIsolation(t *testing.T) {
	e := newMistralEnv(t)
	cfg := e.s.cfg.Get()
	ck, _ := cfg.ClientKey("client1")
	h := http.Header{}
	h.Add(requestOwnerHeader, "start")
	h.Add(requestOwnerHeader, "start")
	if _, err := e.s.readRequestOwner(cfg, ck, "m", "/v1/responses", h, time.Now()); err == nil {
		t.Fatal("duplicate owner headers accepted")
	}
	h.Set(requestOwnerHeader, "start")
	if _, err := e.s.readRequestOwner(cfg, ck, "m", "/v1/chat/completions", h, time.Now()); err == nil {
		t.Fatal("owner accepted on another protocol")
	}
	h.Set(requestOwnerHeader, strings.Repeat("x", 4096))
	if _, err := e.s.readRequestOwner(cfg, ck, "m", "/v1/responses", h, time.Now()); err == nil {
		t.Fatal("oversized token accepted")
	}
}
