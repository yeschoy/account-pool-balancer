package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/thregister"
)

func fakeTHObservation(context.Context, *config.Config, config.Account) (*TokenHarborState, error) {
	used := 0.0
	return &TokenHarborState{Plan: thregister.Subscription{Status: "active", Tier: "plus", Name: "Agent Pass"}, Models: map[string]string{"native": "agent"}, Usage: thregister.PassUsage{OverageBilling: new(bool), UsedPercent: &used, Activated: true, ResetsAt: time.Now().Add(time.Hour)}}, nil
}

func TestTokenHarborCatalogOnlyAndStale(t *testing.T) {
	var code atomic.Int32
	code.Store(200)
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer thk_live_mock" {
			t.Error("wrong metadata request")
		}
		w.WriteHeader(int(code.Load()))
		_, _ = w.Write([]byte(`{"data":[{"id":"native","base_model_id":"do-not-alias"}]}`))
	}))
	defer up.Close()
	cfg := config.Default()
	cfg.TokenHarbor.BaseURL = up.URL + "/v1"
	cfg.Accounts = []config.Account{{ID: "th", RuntimeKey: "thk_live_mock", Enabled: true}}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	p := NewPoller(func() *config.Config { return cfg }, func(string) *http.Client { return up.Client() })
	p.TokenHarborFetch = fakeTHObservation
	a := cfg.Accounts[0]
	if err := p.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	good := p.Get(a.ID)
	if !good.Catalog["native"] || len(good.Aliases) != 0 || !good.PlanAt.IsZero() || good.WalletUSD != "" || good.CatalogSource == "" {
		t.Fatal("catalog confused with plan/balance")
	}
	if d := good.NextAt.Sub(good.CatalogAt); d != 300*time.Second {
		t.Fatalf("wrong sync interval %s", d)
	}
	for _, status := range []int32{503, 403, 401} {
		code.Store(status)
		if p.RefreshNow(context.Background(), a) == nil {
			t.Fatal("expected refresh failure")
		}
		failed := p.Get(a.ID)
		if !failed.CatalogAt.Equal(good.CatalogAt) || !failed.Catalog["native"] || failed.CatalogError == "" {
			t.Fatal("failure destroyed last good catalog")
		}
		if failed.AuthFailed != (status == 401) {
			t.Fatal("regional failure mistaken for invalid key")
		}
	}
	code.Store(200)
	if err := p.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if p.Get(a.ID).AuthFailed {
		t.Fatal("auth state not recovered")
	}
	cfg.TokenHarbor.ModelSyncSec = 0
	off := 0
	cfg.TokenHarbor.AccountSyncSec = &off
	before := calls.Load()
	p.dispatch(context.Background())
	p.workers.Wait()
	if calls.Load() != before {
		t.Fatal("disabled periodic sync contacted upstream")
	}
	old := p.Get(a.ID)
	cfg.Accounts[0].RuntimeKey = "thk_live_changed-credential"
	p.Forget(a.ID)
	p.store(a.ID, old)
	if p.Get(a.ID) != nil {
		t.Fatal("late stale credential catalog revived")
	}
}

func TestTokenHarborCatalogDoesNotFollowRedirect(t *testing.T) {
	var leaked atomic.Bool
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer sink.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, sink.URL+"/v1/models", 302) }))
	defer up.Close()
	cfg := config.Default()
	cfg.TokenHarbor.BaseURL = up.URL + "/v1"
	cfg.Accounts = []config.Account{{ID: "th", RuntimeKey: "thk_live_mock"}}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	p := NewPoller(func() *config.Config { return cfg }, func(string) *http.Client { return up.Client() })
	p.TokenHarborFetch = fakeTHObservation
	if p.RefreshNow(context.Background(), cfg.Accounts[0]) == nil || leaked.Load() {
		t.Fatal("credential redirect followed")
	}
}
