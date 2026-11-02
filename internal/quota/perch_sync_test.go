package quota

import (
	"context"
	"net/http"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func TestPerchSessionRefreshWhileRunning(t *testing.T) {
	cfg := config.Default()
	cfg.Quota.Enabled = false
	cfg.Quota.Concurrency = 1
	a := config.Account{ID: "a", Provider: config.ProviderPerch, RuntimeKey: "key"}
	cfg.Accounts = []config.Account{a}
	p := NewPoller(func() *config.Config { return cfg }, func(string) *http.Client { return http.DefaultClient })
	started := make(chan struct{}, 2)
	release := make(chan struct{}, 2)
	p.PerchFetch = func(ctx context.Context, _ config.Account) (*PerchMeter, error) {
		started <- struct{}{}
		select {
		case <-release:
			return &PerchMeter{Plan: "Starter"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.RefreshSession(a.ID)
	p.dispatch(ctx)
	awaitSignal(t, started)
	p.RefreshSession(a.ID)
	release <- struct{}{}
	awaitSignal(t, p.wake)
	// The initial wake may still be queued, so wait for completion explicitly.
	for {
		p.mu.Lock()
		done := p.running[a.ID]
		p.mu.Unlock()
		if done == nil {
			break
		}
		awaitSignal(t, done)
	}
	p.dispatch(ctx)
	awaitSignal(t, started)
	release <- struct{}{}
	p.mu.Lock()
	done := p.running[a.ID]
	p.mu.Unlock()
	if done != nil {
		awaitSignal(t, done)
	}
	if got := p.Get(a.ID); got == nil || got.Perch == nil || got.Perch.Plan != "Starter" {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestSessionRefreshRespectsConcurrency(t *testing.T) {
	cfg := config.Default()
	cfg.Quota.Enabled = false
	cfg.Quota.Concurrency = 1
	cfg.Accounts = []config.Account{
		{ID: "a", Provider: config.ProviderPerch, RuntimeKey: "key-a"},
		{ID: "b", Provider: config.ProviderPerch, RuntimeKey: "key-b"},
	}
	p := NewPoller(func() *config.Config { return cfg }, func(string) *http.Client { return http.DefaultClient })
	started := make(chan struct{}, 2)
	release := make(chan struct{}, 2)
	p.PerchFetch = func(ctx context.Context, _ config.Account) (*PerchMeter, error) {
		started <- struct{}{}
		select {
		case <-release:
			return &PerchMeter{Plan: "Starter"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	p.RefreshSession("a")
	p.RefreshSession("b")
	p.dispatch(context.Background())
	awaitSignal(t, started)
	p.dispatch(context.Background())
	select {
	case <-started:
		t.Fatal("concurrency limit exceeded")
	default:
	}
	p.mu.Lock()
	done := p.running["a"]
	p.mu.Unlock()
	release <- struct{}{}
	awaitSignal(t, done)
	p.dispatch(context.Background())
	awaitSignal(t, started)
	p.mu.Lock()
	done = p.running["b"]
	p.mu.Unlock()
	release <- struct{}{}
	awaitSignal(t, done)
}

func TestRefreshNowWaitsForBackgroundRead(t *testing.T) {
	cfg := config.Default()
	a := config.Account{ID: "a", Provider: config.ProviderPerch, RuntimeKey: "key"}
	cfg.Accounts = []config.Account{a}
	p := NewPoller(func() *config.Config { return cfg }, func(string) *http.Client { return http.DefaultClient })
	started := make(chan struct{}, 2)
	release := make(chan struct{}, 2)
	p.PerchFetch = func(ctx context.Context, _ config.Account) (*PerchMeter, error) {
		started <- struct{}{}
		select {
		case <-release:
			return &PerchMeter{Plan: "Starter"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	p.dispatch(context.Background())
	awaitSignal(t, started)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := p.RefreshNow(ctx, a); err != context.DeadlineExceeded {
		t.Fatalf("waiting refresh: %v", err)
	}
	release <- struct{}{}
	p.mu.Lock()
	done := p.running[a.ID]
	p.mu.Unlock()
	if done != nil {
		awaitSignal(t, done)
	}
}

func awaitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for synchronization")
	}
}
