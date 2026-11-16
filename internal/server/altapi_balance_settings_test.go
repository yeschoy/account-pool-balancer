package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
)

func TestALTAPILowBalanceSettingsAndScheduling(t *testing.T) {
	e := newALTAPIStickyEnv(t)
	_, err := e.srv.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			c.Accounts[i].ControlKey = "mock-session"
			c.Accounts[i].UserID = fmt.Sprint(41 + i)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before := e.srv.cfg.Get().Clone()
	admin := func(method string, body any) settingsDoc {
		t.Helper()
		w := deployAdmin(t, &deployTestEnv{s: e.srv}, method, "/api/settings?provider=altapi", body)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var out struct {
			Settings settingsDoc `json:"settings"`
		}
		if method == "GET" && json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal("settings decode")
		}
		return out.Settings
	}
	doc := admin("GET", nil)
	if doc.ALTAPI.LowBalanceDisable == nil || *doc.ALTAPI.LowBalanceDisable || doc.ALTAPI.MinBalanceUSD == nil || *doc.ALTAPI.MinBalanceUSD != 0.5 {
		t.Fatal("missing safe defaults")
	}
	*doc.ALTAPI.LowBalanceDisable, *doc.ALTAPI.MinBalanceUSD = true, 0.5
	// Attempted edits outside ALTAPI must not be published.
	doc.Logs.RetentionDays = 31
	doc.OpenCode.Enabled = true
	admin("PUT", doc)
	got := admin("GET", nil)
	if !*got.ALTAPI.LowBalanceDisable || *got.ALTAPI.MinBalanceUSD != 0.5 {
		t.Fatal("threshold GET lost settings")
	}
	loaded, err := config.Load(filepath.Join(e.srv.dataDir, "config.json"))
	if err != nil || loaded.Get().ALTAPI.MinBalanceUSD != 0.5 || !loaded.Get().ALTAPI.LowBalanceDisable {
		t.Fatal("threshold not persisted", err)
	}
	after := e.srv.cfg.Get().Clone()
	after.ALTAPI, after.Revision = before.ALTAPI, before.Revision
	if !reflect.DeepEqual(before, after) {
		t.Fatal("threshold settings changed other pools/configuration")
	}
	// Invalid threshold rejects atomically, preserving memory and disk.
	stable := e.srv.cfg.Get().Clone()
	diskBefore, err := os.ReadFile(filepath.Join(e.srv.dataDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	bad := admin("GET", nil)
	*bad.ALTAPI.MinBalanceUSD = -0.01
	w := deployAdmin(t, &deployTestEnv{s: e.srv}, "PUT", "/api/settings?provider=altapi", bad)
	diskAfter, err := os.ReadFile(filepath.Join(e.srv.dataDir, "config.json"))
	if err != nil || w.Code != 400 || !reflect.DeepEqual(stable, e.srv.cfg.Get()) || string(diskBefore) != string(diskAfter) {
		t.Fatal("invalid threshold changed persisted/current config")
	}

	setBalances := func(balance float64) {
		t.Helper()
		cfg := e.srv.cfg.Get()
		snaps := map[string]*quota.Snapshot{}
		for _, a := range cfg.Accounts {
			if a.Provider != config.ProviderALTAPI {
				continue
			}
			snaps[a.ID] = &quota.Snapshot{ALTAPI: &quota.ALTAPIState{Source: "session", BalanceSource: quota.ResponseBindingSource(cfg, a), BalanceUSD: balance, ReadAt: time.Now().Format(time.RFC3339)}}
		}
		e.srv.quota.Load(snaps)
	}
	setBalances(0.499)
	if available, total := e.srv.altAutoAvailability(e.srv.cfg.Get()); available != 0 || total != 2 {
		t.Fatalf("low balances counted available: %d/%d", available, total)
	}
	w = deployAdmin(t, &deployTestEnv{s: e.srv}, "GET", "/api/altapi/status", nil)
	var status struct {
		Pool struct {
			Accounts []struct {
				Status  string `json:"status"`
				Enabled bool   `json:"enabled"`
			} `json:"accounts"`
		} `json:"pool"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil || len(status.Pool.Accounts) != 2 {
		t.Fatal("status response", w.Body.String())
	}
	for _, a := range status.Pool.Accounts {
		if a.Status != "low-balance" || !a.Enabled {
			t.Fatal("wrong status/manual flag", a)
		}
	}
	resp := e.post("/v1/chat/completions", chat(false), nil)
	_ = readAll(resp)
	if resp.StatusCode != http.StatusServiceUnavailable || e.up.callCount() != 0 {
		t.Fatal("saved threshold did not block inference")
	}
	preview := deployAdmin(t, &deployTestEnv{s: e.srv}, "POST", "/api/workflow/preview", map[string]string{"group": "altapi", "model": "m1"})
	var pv struct {
		Order    []string          `json:"order"`
		Excluded map[string]string `json:"excluded"`
	}
	if preview.Code != 200 || json.Unmarshal(preview.Body.Bytes(), &pv) != nil || len(pv.Order) != 0 || len(pv.Excluded) != 2 {
		t.Fatal("preview did not honor threshold", preview.Body.String())
	}
	for _, why := range pv.Excluded {
		if why != "low-balance" {
			t.Fatal("wrong preview exclusion", why)
		}
	}
	setBalances(0.5)
	altStickyPost(t, e, chat(false), nil, 200)
	if available, _ := e.srv.altAutoAvailability(e.srv.cfg.Get()); available != 2 {
		t.Fatal("refreshed balance did not restore availability")
	}
	setBalances(0.4)
	lower := admin("GET", nil)
	*lower.ALTAPI.MinBalanceUSD = 0.25
	admin("PUT", lower)
	altStickyPost(t, e, chat(false), nil, 200)
	off := admin("GET", nil)
	*off.ALTAPI.LowBalanceDisable = false
	admin("PUT", off)
	altStickyPost(t, e, chat(false), nil, 200)
	if e.up.callCount() != 3 {
		t.Fatal("disabling threshold did not restore scheduling")
	}
}
