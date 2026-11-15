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

func TestX50BillingStrict(t *testing.T) {
	for _, raw := range []string{`{}`, `{"balance_usd":null}`, `{"balance_usd":true}`, `{"balance_usd":"NaN"}`, `{"balance_usd":"1e999"}`, `{"balance_usd":""}`, `{"balance_usd":" 1"}`, `{"balance_usd":0,"tier":7}`, `{"balance_usd":{}}`} {
		if _, e := ParseX50Billing([]byte(raw)); e == nil {
			t.Fatal("accepted", raw)
		}
	}
	for _, raw := range []string{`{"balance_usd":"12.345","tier":"paid"}`, `{"balance_usd":0}`, `{"balance_usd":-1}`} {
		x, e := ParseX50Billing([]byte(raw))
		if e != nil || x.BalanceUSD == nil {
			t.Fatal(raw, e)
		}
	}
}
func TestX50RefreshHistorySourceRestartAndDirect(t *testing.T) {
	var failed atomic.Bool
	var models, billing atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			models.Add(1)
			if r.Header.Get("Authorization") != "Bearer sk-50x-mock" {
				t.Error("model auth")
			}
			fmt.Fprint(w, `{"data":[{"id":"native/model"}]}`)
			return
		}
		billing.Add(1)
		if r.URL.Path != "/api/billing" || r.Header.Get("Authorization") != "Token mock-session" {
			t.Error("billing request")
		}
		if failed.Load() {
			w.WriteHeader(401)
			fmt.Fprint(w, `secret mock-session sk-50x-mock`)
			return
		}
		fmt.Fprint(w, `{"balance_usd":"12.345","tier":"paid"}`)
	}))
	defer up.Close()
	c := config.Default()
	c.X50.BaseURL = up.URL + "/v1"
	c.X50.ModelSyncSec = 0
	c.Accounts = []config.Account{{ID: "x", Provider: config.ProviderX50, RuntimeKey: "sk-50x-mock", ControlKey: "mock-session", Enabled: true}}
	if e := c.Normalize(); e != nil {
		t.Fatal(e)
	}
	p := NewPoller(func() *config.Config { return c }, func(proxy string) *http.Client {
		if proxy != "" {
			t.Fatal("unexpected proxy")
		}
		return up.Client()
	})
	a := c.Accounts[0]
	if e := p.RefreshNow(context.Background(), a); e != nil {
		t.Fatal(e)
	}
	q := p.Get(a.ID)
	if !q.FreshX50(time.Now(), time.Hour) || *q.X50.BalanceUSD != 12.345 || q.CatalogSource == "" {
		t.Fatal(q)
	}
	// Immutable Get clones pointers as well as maps.
	*q.X50.BalanceUSD = 9
	if *p.Get(a.ID).X50.BalanceUSD != 12.345 {
		t.Fatal("aliasing")
	}
	if e := p.fetchX50(context.Background(), c, a, false); e != nil {
		t.Fatal(e)
	}
	if models.Load() != 1 || billing.Load() != 2 {
		t.Fatal("sync0 did not disable periodic model HTTP")
	}
	failed.Store(true)
	if e := p.RefreshNow(context.Background(), a); e == nil {
		t.Fatal("401 accepted")
	}
	q = p.Get(a.ID)
	if q.FreshX50(time.Now(), time.Hour) || q.X50.BalanceUSD == nil || *q.X50.BalanceUSD != 12.345 || q.AuthFailed {
		t.Fatal("failed billing lost history or marked inference auth failed")
	}
	raw, _ := json.Marshal(q)
	if strings.Contains(string(raw), "mock-session") {
		t.Fatal("credential leak")
	}
	var restored Snapshot
	json.Unmarshal(raw, &restored)
	p.Load(map[string]*Snapshot{a.ID: &restored})
	if *p.Get(a.ID).X50.BalanceUSD != 12.345 {
		t.Fatal("restart history lost")
	}
	q = p.Get(a.ID)
	c = c.Clone()
	c.Accounts[0].ControlKey = "rotated"
	if e := c.Normalize(); e != nil {
		t.Fatal(e)
	}
	p.Forget(a.ID)
	p.store(a.ID, q)
	if p.Get(a.ID) != nil {
		t.Fatal("restored old source published after session rotation")
	}
}
func TestX50LateReadCannotPublish(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/billing" {
			close(started)
			<-release
			fmt.Fprint(w, `{"balance_usd":55}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"m"}]}`)
	}))
	defer up.Close()
	c := config.Default()
	c.X50.BaseURL = up.URL + "/v1"
	c.Accounts = []config.Account{{ID: "x", Provider: config.ProviderX50, RuntimeKey: "sk-50x-a", ControlKey: "s", Enabled: true}}
	c.Normalize()
	var current atomic.Pointer[config.Config]
	current.Store(c)
	p := NewPoller(current.Load, func(string) *http.Client { return up.Client() })
	done := make(chan error, 1)
	go func() { done <- p.RefreshNow(context.Background(), c.Accounts[0]) }()
	<-started
	next := c.Clone()
	next.Accounts[0].ProxyURL = "http://127.0.0.1:1"
	next.Normalize()
	current.Store(next)
	p.Forget("x")
	close(release)
	<-done
	if p.Get("x") != nil {
		t.Fatal("old source published after invalidation")
	}
}
