package pool

import (
	"account-pool-balancer/internal/config"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func capacityConfig(n int) *config.Config {
	c := mistralBindingConfig()
	c.Accounts[0].MaxConc = 0
	c.Accounts[0].MaxRPM = 0
	c.Mistral.BindingCapacity = &n
	c.Accounts = append(c.Accounts, config.Account{ID: "strict", Provider: config.ProviderOpenCodeGo, Group: "strict", RuntimeKey: "synthetic-strict-key", Enabled: true})
	if err := c.Normalize(); err != nil {
		panic(err)
	}
	return c
}
func sentCapacityBinding(t *testing.T, p *Pool, c *config.Config, key string) *Lease {
	t.Helper()
	l, err := p.Acquire(context.Background(), c.ForProvider(config.ProviderMistral), Request{Group: config.MistralGroup, ClientKeyID: "caller", Affinity: key, Explicit: true, RequestID: key})
	if err != nil {
		t.Fatal(err)
	}
	l.MarkSent()
	return l
}
func TestMistralIndependentCapacityIsolationShrinkAndRestore(t *testing.T) {
	c := capacityConfig(5000)
	p := New(nil)
	p.ConfigureBindings(c)
	for i := 0; i < maxBindings; i++ {
		p.Bind(c, fmt.Sprintf("%064x", 100000+i), "strict")
	}
	p.mu.Lock()
	for i := 0; i < 5001; i++ {
		key := fmt.Sprintf("%064x", i)
		p.putProvisionalLocked(c, Request{Group: config.MistralGroup, ClientKeyID: "caller", Affinity: key, Explicit: true, RequestID: key}, "m1", time.Now())
		p.bindings[key].Value.(*binding).confirmed = true
	}
	p.mu.Unlock()
	if p.SharedBindingCount() != 4096 || p.BindingCount() != 9096 {
		t.Fatal("budgets not independent", p.BindingCount())
	}
	if _, ok := p.BoundAccount(fmt.Sprintf("%064x", 100000)); !ok {
		t.Fatal("Mistral evicted strict owner")
	}
	if _, ok := p.BoundAccount(fmt.Sprintf("%064x", 0)); ok {
		t.Fatal("Mistral LRU not trimmed")
	}
	saved, _ := p.ExportState()
	restored := New(nil)
	restored.ConfigureBindings(c)
	restored.ImportState(saved, map[string]bool{"strict": true, "m1": true})
	restored.ImportMistralBindings(saved.MistralBindings, c)
	if restored.BindingCount() != 9096 {
		t.Fatal("restart lost independent bindings", restored.BindingCount())
	}
	l := sentCapacityBinding(t, restored, c, fmt.Sprintf("%064x", 1)) // touch the oldest entry
	next := c.Clone()
	size := 2
	next.Mistral.BindingCapacity = &size
	restored.ReconcileMistralBindings(next)
	if restored.Views(next)["m1"].Bindings != 2 || restored.SharedBindingCount() != 4096 {
		t.Fatal("shrink changed another pool")
	}
	if restored.Views(next)["m1"].Active != 1 {
		t.Fatal("shrink terminated inflight lease")
	}
	l.Release()
	old := sentCapacityBinding(t, restored, c, fmt.Sprintf("%064x", 2))
	old.Release()
	if limit, independent := restored.BindingCapacityFor(config.ProviderMistral); limit != 2 || !independent {
		t.Fatal("old request reverted capacity")
	}
	zero := 0
	next.Mistral.BindingCapacity = &zero
	restored.ConfigureBindings(next)
	if restored.BindingCount() != 4096 || restored.Views(next)["m1"].Bindings != 0 {
		t.Fatal("legacy migration displaced strict owners")
	}
	if _, ok := restored.BoundAccount(fmt.Sprintf("%064x", 100000)); !ok {
		t.Fatal("strict owner evicted on migration")
	}
	if _, err := restored.Acquire(context.Background(), next, Request{Group: config.MistralGroup, RequireBinding: true, Affinity: "missing"}); err == nil {
		t.Fatal("missing strict owner silently rerouted")
	}
}
func TestMistralCapacityExpiryUnsentAndHeapRecency(t *testing.T) {
	c := capacityConfig(2)
	p := New(nil)
	p.ConfigureBindings(c)
	now := time.Now()
	p.SetClock(func() time.Time { return now })
	a := sentCapacityBinding(t, p, c, "a")
	a.Release()
	now = now.Add(time.Second)
	b := sentCapacityBinding(t, p, c, "b")
	b.Release()
	hit := sentCapacityBinding(t, p, c, "a")
	if hit.Decision.BindingResult != "hit" {
		t.Fatal("no hit")
	}
	hit.Release()
	d := sentCapacityBinding(t, p, c, "d")
	d.Release()
	if _, ok := p.BoundAccount("b"); ok {
		t.Fatal("budget recency not updated")
	}
	unsent, err := p.Acquire(context.Background(), c, Request{Group: config.MistralGroup, ClientKeyID: "caller", Affinity: "unsent", RequestID: "unsent"})
	if err != nil {
		t.Fatal(err)
	}
	unsent.Release()
	unsent.MarkSent()
	if _, ok := p.BoundAccount("unsent"); ok {
		t.Fatal("late callback restored unsent")
	}
	now = now.Add(8 * 24 * time.Hour)
	if p.Views(c)["m1"].Bindings != 0 || len(p.expiry) != 0 || p.BindingCount() != 0 {
		t.Fatal("expiry counters/heap leaked")
	}
	saved, changed := p.ExportState()
	if !changed || len(saved.MistralBindings) != 0 {
		t.Fatal("expiry removal not persisted")
	}
}
func TestBindingPublicationAndMovedAccountWake(t *testing.T) {
	c := capacityConfig(5)
	c.Revision = 10
	p := New(nil)
	p.ConfigureBindings(c)
	l := sentCapacityBinding(t, p, c, "moving")
	next := c.Clone()
	next.Revision = 11
	n := 2
	next.Mistral.BindingCapacity = &n
	next.Accounts[0].Group = "moved"
	if err := next.Normalize(); err != nil {
		t.Fatal(err)
	}
	p.ConfigureBindings(next)
	p.ConfigureBindings(c)
	if cap, _ := p.BindingCapacityFor(config.ProviderMistral); cap != 2 {
		t.Fatal("old publication rolled capacity back")
	}
	current, other := make(chan struct{}), make(chan struct{})
	p.mu.Lock()
	p.waiters["moved"] = &groupWaiters{changed: current, count: 1}
	p.waiters["unrelated"] = &groupWaiters{changed: other, count: 1}
	p.mu.Unlock()
	l.Release()
	select {
	case <-current:
	default:
		t.Fatal("old lease failed to wake account's current group")
	}
	select {
	case <-other:
		t.Fatal("release woke unrelated group")
	default:
	}
}

func TestProvisionalConcurrentSenderConfirmsPreference(t *testing.T) {
	c := capacityConfig(2)
	p := New(nil)
	p.ConfigureBindings(c)
	req := Request{Group: config.MistralGroup, ClientKeyID: "caller", Affinity: "same", Explicit: true, RequestID: "creator"}
	creator, err := p.Acquire(context.Background(), c, req)
	if err != nil {
		t.Fatal(err)
	}
	req.RequestID = "sender"
	sender, err := p.Acquire(context.Background(), c, req)
	if err != nil {
		t.Fatal(err)
	}
	if sender.Decision.BindingResult != "provisional" {
		t.Fatal("not provisional")
	}
	sender.MarkSent()
	sender.Release()
	creator.Release()
	if _, ok := p.BoundAccount("same"); !ok {
		t.Fatal("unsent creator removed another confirmed sender")
	}
	saved, _ := p.ExportState()
	if len(saved.MistralBindings) != 1 {
		t.Fatal("confirmed concurrent sender not checkpointed")
	}
}

func TestBoundedWaitersAndGroupWakeIsolation(t *testing.T) {
	p := New(nil)
	c := testConfig(config.Account{ID: "a", MaxConc: 1}, config.Account{ID: "b", Group: "other", MaxConc: 1})
	held := acquire(t, p, c, Request{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		l, err := p.Acquire(ctx, c, Request{Group: "default", Wait: time.Second, MaxWaiters: 1})
		if l != nil {
			l.Release()
		}
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		p.mu.Lock()
		w := p.waiters["default"]
		ready := w != nil && w.count == 1
		p.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("waiter not registered")
		}
		time.Sleep(time.Millisecond)
	}
	_, err := p.Acquire(context.Background(), c, Request{Group: "default", Wait: time.Second, MaxWaiters: 1})
	var ce *CapacityError
	if !errors.As(err, &ce) || ce.BlockedBy != "wait-queue" {
		t.Fatal("queue length cap ignored", err)
	}
	p.mu.Lock()
	ch := p.waiters["default"].changed
	p.mu.Unlock()
	other := acquire(t, p, c, Request{Group: "other"})
	other.Release()
	select {
	case <-ch:
		t.Fatal("another group broadcast to waiter")
	default:
	}
	cancel()
	if !errors.Is(<-done, context.Canceled) {
		t.Fatal("waiter not cancelled")
	}
	held.Release()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.waiters) != 0 {
		t.Fatal("waiter accounting leaked")
	}
}
