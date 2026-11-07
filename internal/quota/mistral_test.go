package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func TestMistralCatalogOnlyRefreshAndSource(t *testing.T) {
	var code atomic.Int32
	code.Store(200)
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer mstrl_mock_secret" {
			t.Error("wrong metadata target/auth")
		}
		w.WriteHeader(int(code.Load()))
		w.Write([]byte(`{"data":[{"id":"ft:custom","base_model_id":"mistral-base"},{"id":"disabled","enabled":false}]}`))
	}))
	defer up.Close()
	cfg := config.Default()
	cfg.Mistral.BaseURL = up.URL + "/v1"
	cfg.Accounts = []config.Account{{ID: "m", RuntimeKey: "mstrl_mock_secret", Enabled: true}}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	p := NewPoller(func() *config.Config { return cfg }, func(string) *http.Client { return up.Client() })
	a := cfg.Accounts[0]
	if err := p.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	good := p.Get(a.ID)
	if !good.Catalog["ft:custom"] || good.Catalog["disabled"] || len(good.Aliases) != 0 || len(good.Bases) != 0 || good.CatalogSource == "" || !good.PlanAt.IsZero() || good.WalletUSD != "" {
		t.Fatal("native IDs or unknown funding lost")
	}
	if err := p.fetch(context.Background(), cfg, a); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("alias-free catalog fetched before TTL")
	}
	for _, st := range []int32{503, 401} {
		code.Store(st)
		if p.RefreshNow(context.Background(), a) == nil {
			t.Fatal("failed refresh reported success")
		}
		q := p.Get(a.ID)
		if !q.CatalogAt.Equal(good.CatalogAt) || !q.Catalog["ft:custom"] || q.LastError == "" || q.AuthFailed != (st == 401) {
			t.Fatal("last good data destroyed or failure unmarked")
		}
	}
	code.Store(200)
	if err := p.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if p.Get(a.ID).AuthFailed {
		t.Fatal("successful refresh did not recover auth")
	}
	old := p.Get(a.ID)
	cfg.Accounts[0].RuntimeKey = "mstrl_replaced_secret"
	p.Forget(a.ID)
	p.store(a.ID, old)
	if p.Get(a.ID) != nil {
		t.Fatal("late source write resurrected catalog")
	}
	if catalogDue(good, time.Now(), true, false) {
		t.Fatal("native IDs incorrectly require alias migration")
	}
}

func TestMistralMetadataNoCredentialRedirect(t *testing.T) {
	var seen atomic.Bool
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen.Store(true) }))
	defer sink.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, sink.URL+"/models", 302) }))
	defer up.Close()
	if _, err := FetchMistralCatalog(context.Background(), up.Client(), up.URL, "mstrl_mock_secret", "test"); err == nil || seen.Load() {
		t.Fatal("redirect followed")
	}
}
