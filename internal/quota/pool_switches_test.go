package quota

import (
	"account-pool-balancer/internal/config"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPoolSwitchCancelsAndDiscardsLateRefresh(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(started)
		<-release
		fmt.Fprint(w, `{"userId":"late","orgId":"late"}`)
	}))
	defer up.Close()
	c := config.Default()
	c.Factory.BaseURL = up.URL
	c.Accounts = []config.Account{{ID: "f", Provider: config.ProviderFactory, RuntimeKey: "fk-mock_factory_key"}}
	var cur atomic.Pointer[config.Config]
	cur.Store(c)
	p := NewPoller(cur.Load, func(string) *http.Client { return up.Client() })
	done := make(chan error, 1)
	go func() { done <- p.RefreshNow(context.Background(), c.Accounts[0]) }()
	<-started
	off := c.Clone()
	off.PoolSwitches = map[string]bool{config.ProviderFactory: false}
	cur.Store(off)
	p.PoolChanged(c, off)
	on := off.Clone()
	on.PoolSwitches[config.ProviderFactory] = true
	cur.Store(on)
	p.PoolChanged(off, on)
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh not cancelled")
	}
	if p.Get("f") != nil {
		t.Fatal("disabled epoch overwrote snapshot")
	}
	cur.Store(off)
	before := calls.Load()
	p.dispatch(context.Background())
	if calls.Load() != before {
		t.Fatal("disabled periodic request")
	}
	if p.RefreshNow(context.Background(), off.Accounts[0]) == nil {
		t.Fatal("manual disabled refresh allowed")
	}
}
func TestPoolSwitchDoesNotEnablePolling(t *testing.T) {
	c := config.Default()
	q := c.Factory.EffectiveQuota()
	q.Enabled = false
	c.Factory.Quota = &q
	c.Accounts = []config.Account{{ID: "f", Provider: config.ProviderFactory, RuntimeKey: "fk-mock_factory_key"}}
	c.PoolSwitches = map[string]bool{config.ProviderFactory: false}
	var cur atomic.Pointer[config.Config]
	cur.Store(c)
	p := NewPoller(cur.Load, func(string) *http.Client { t.Fatal("unexpected network"); return nil })
	on := c.Clone()
	on.PoolSwitches[config.ProviderFactory] = true
	cur.Store(on)
	p.PoolChanged(c, on)
	p.dispatch(context.Background())
	p.workers.Wait()
}
