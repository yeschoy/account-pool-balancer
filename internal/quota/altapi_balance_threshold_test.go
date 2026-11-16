package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func TestALTAPIBalanceThresholdRefreshSourceAndPersistence(t *testing.T) {
	var failed atomic.Bool
	var rawQuota atomic.Int64
	rawQuota.Store(1000000)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"m1"}]}`)
		case "/api/status":
			fmt.Fprint(w, `{"data":{"quota_per_unit":500000}}`)
		case "/api/user/self":
			if r.Header.Get("Cookie") != "session=mock-session" || r.Header.Get("New-Api-User") != "42" {
				t.Error("console credential contract")
			}
			if failed.Load() {
				w.WriteHeader(503)
				fmt.Fprint(w, `{"message":"console temporarily unavailable"}`)
				return
			}
			fmt.Fprintf(w, `{"success":true,"data":{"id":42,"quota":%d,"used_quota":0,"request_count":1}}`, rawQuota.Load())
		default:
			t.Error("unexpected endpoint", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer up.Close()
	c := config.Default()
	c.ALTAPI.BaseURL = up.URL + "/v1"
	c.ALTAPI.LowBalanceDisable, c.ALTAPI.MinBalanceUSD = true, 2.5
	c.Accounts = []config.Account{{ID: "alt", Provider: config.ProviderALTAPI, RuntimeKey: "mock-runtime", ControlKey: "mock-session", UserID: "42", Enabled: true}}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	p := NewPoller(func() *config.Config { return c }, func(string) *http.Client { return up.Client() })
	a := c.Accounts[0]
	if err := p.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	s := p.Get(a.ID)
	if !s.ALTAPILowBalance(c, a, time.Now()) || s.ALTAPI.BalanceUSD != 2 {
		t.Fatal("real console snapshot did not trigger threshold", s)
	}
	// Get must deep-copy ALTAPI too: future reads cannot race with routing.
	s.ALTAPI.BalanceUSD = 9
	if p.Get(a.ID).ALTAPI.BalanceUSD != 2 {
		t.Fatal("mutable ALTAPI snapshot alias")
	}
	raw, err := json.Marshal(p.Get(a.ID))
	if err != nil || strings.Contains(string(raw), "mock-session") || strings.Contains(string(raw), "mock-runtime") {
		t.Fatal("persisted snapshot leaked credentials", err)
	}
	var restored Snapshot
	if json.Unmarshal(raw, &restored) != nil {
		t.Fatal("snapshot round-trip")
	}
	p.Load(map[string]*Snapshot{a.ID: &restored})
	if !p.Get(a.ID).ALTAPILowBalance(c, a, time.Now()) {
		t.Fatal("restart lost threshold evidence")
	}
	failed.Store(true)
	if err := p.RefreshNow(context.Background(), a); err == nil {
		t.Fatal("failed read accepted")
	}
	if s := p.Get(a.ID); s.ALTAPILowBalance(c, a, time.Now()) || s.ALTAPI.BalanceUSD != 2 {
		t.Fatal("read failure gated using stale history or lost history")
	}
	failed.Store(false)
	rawQuota.Store(1500000)
	if err := p.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if p.Get(a.ID).ALTAPILowBalance(c, a, time.Now()) {
		t.Fatal("new higher balance failed to restore")
	}
	old := p.Get(a.ID)
	c = c.Clone()
	c.Accounts[0].ControlKey = "rotated-session"
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	p.Forget(a.ID)
	p.store(a.ID, old)
	if p.Get(a.ID) != nil {
		t.Fatal("old console read published after source rotation")
	}
}
