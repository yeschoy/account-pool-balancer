package pool

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
	"account-pool-balancer/internal/quota"
)

func TestStrictContinuationNeverOverflowsOrRebinds(t *testing.T) {
	cfg := testConfig(config.Account{ID: "a", MaxConc: 1}, config.Account{ID: "b", MaxConc: 1})
	cfg.Routing.OnBindingBusy = "overflow"
	p := New(nil)
	p.Bind(cfg, "stateful", "a")
	l := acquire(t, p, cfg, Request{Affinity: "stateful", RequireBinding: true, RequestID: "r1"})
	_, err := p.Acquire(context.Background(), cfg, Request{Group: "default", Affinity: "stateful", RequireBinding: true, PreserveBinding: true})
	var busy *CapacityError
	if !errors.As(err, &busy) {
		t.Fatalf("busy binding switched account: %v", err)
	}
	l.Release()
	next := acquire(t, p, cfg, Request{Affinity: "stateful", RequireBinding: true, RequestID: "r2"})
	if next.Account.ID != "a" {
		t.Fatal("binding replaced")
	}
	next.Release()
	p.Quarantine("a", 401, "auth", "auth")
	_, err = p.Acquire(context.Background(), cfg, Request{Group: "default", Affinity: "stateful", RequireBinding: true})
	var binding *BindingError
	if !errors.As(err, &binding) {
		t.Fatalf("unavailable stateful chain reselected: %v", err)
	}
	_, err = p.Acquire(context.Background(), cfg, Request{Group: "default", Affinity: "missing", RequireBinding: true})
	if !errors.As(err, &binding) {
		t.Fatal("unknown response chose random account")
	}
}
func TestOpenCodeAccountCatalogAndUnknownQuota(t *testing.T) {
	cfg := config.Default()
	cfg.OpenCode.Enabled = true
	cfg.Accounts = []config.Account{{ID: "a", Provider: config.ProviderOpenCodeGo, RuntimeKey: "go-key-a-123", Enabled: true}, {ID: "b", Provider: config.ProviderOpenCodeGo, RuntimeKey: "go-key-b-123", Enabled: true}}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	snapshots := map[string]*quota.Snapshot{}
	for i, a := range cfg.Accounts {
		model := "only-a"
		if i == 1 {
			model = "only-b"
		}
		snapshots[a.ID] = &quota.Snapshot{Catalog: map[string]bool{model: true}, CatalogAt: time.Now(), OpenCode: &opencode.State{Source: quota.OpenCodeSource(cfg, a)}}
	}
	p := New(func(id string) *quota.Snapshot { return snapshots[id] })
	l, err := p.Acquire(context.Background(), cfg, Request{Group: config.ProviderOpenCodeGo, Model: "only-b"})
	if err != nil {
		t.Fatal(err)
	}
	if l.Account.ID != "b" {
		t.Fatal("account A inherited B permission")
	}
	l.Release()
	cfg.Accounts[1].ProxyURL = "http://127.0.0.1:1234"
	if _, err := p.Acquire(context.Background(), cfg, Request{Group: config.ProviderOpenCodeGo, Model: "only-b"}); err == nil {
		t.Fatal("old proxy's catalog trusted")
	}
}
func TestOpenCodeBindingCheckpointRestoresOnlySameCredential(t *testing.T) {
	cfg := config.Default()
	cfg.OpenCode.Enabled = true
	cfg.Accounts = []config.Account{{ID: "a", Provider: config.ProviderOpenCodeZen, RuntimeKey: "zen-key-123", Enabled: true}}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	snap := &quota.Snapshot{Catalog: map[string]bool{"m": true}, CatalogAt: time.Now(), OpenCode: &opencode.State{Source: quota.OpenCodeSource(cfg, cfg.Accounts[0])}}
	p := New(func(string) *quota.Snapshot { return snap })
	key := strings.Repeat("a", 64)
	p.Bind(cfg, key, "a")
	exp, _ := p.ExportState()
	if len(exp.Bindings) != 1 {
		t.Fatal("Responses binding not checkpointed")
	}
	restored := New(func(string) *quota.Snapshot { return snap })
	restored.ImportState(exp, map[string]bool{"a": true})
	l, err := restored.Acquire(context.Background(), cfg, Request{Group: config.ProviderOpenCodeZen, Model: "m", Affinity: key, RequireBinding: true})
	if err != nil {
		t.Fatal(err)
	}
	l.Release()
	cfg.Accounts[0].RuntimeKey = "new-zen-key"
	snap = snapCopyWithNewSource(snap, cfg)
	if _, err := restored.Acquire(context.Background(), cfg, Request{Group: config.ProviderOpenCodeZen, Model: "m", Affinity: key, RequireBinding: true}); err == nil {
		t.Fatal("response chain attached to replacement key")
	}
}
func snapCopyWithNewSource(s *quota.Snapshot, c *config.Config) *quota.Snapshot {
	v := *s
	v.OpenCode = s.OpenCode.Clone()
	v.OpenCode.Source = quota.OpenCodeSource(c, c.Accounts[0])
	return &v
}
func TestTransportTraceAfterLeaseReleaseDoesNotSpendRPM(t *testing.T) {
	cfg := testConfig(config.Account{ID: "a", MaxRPM: 10})
	p := New(nil)
	l := acquire(t, p, cfg, Request{})
	l.Release()
	l.MarkSent()
	if v := p.Views(cfg)["a"]; v.Active != 0 || v.RPM != 0 {
		t.Fatalf("late trace revived a released lease: %+v", v)
	}
}

func TestOpenCodeConcurrentLeasesHaveSingleRPMCommit(t *testing.T) {
	cfg := testConfig(config.Account{ID: "a", MaxConc: 8, MaxRPM: 1000})
	p := New(nil)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Go(func() {
			l, err := p.Acquire(context.Background(), cfg, Request{Group: "default", Wait: 5 * time.Second})
			if err != nil {
				t.Error(err)
				return
			}
			l.MarkSent()
			l.MarkSent()
			l.Release()
			l.Release()
		})
	}
	wg.Wait()
	views := p.Views(cfg)
	if views["a"].Active != 0 || views["a"].RPM != 100 {
		t.Fatalf("lease/RPM inconsistency %+v", views["a"])
	}
}
