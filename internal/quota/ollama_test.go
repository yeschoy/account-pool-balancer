package quota

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/egressid"
)

func ollamaPoller(t *testing.T, handler http.HandlerFunc) (*Poller, *config.Config) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.Upstream.OllamaBase = srv.URL + "/v1"
	cfg.Accounts = []config.Account{{ID: "o", Provider: config.ProviderOllama, RuntimeKey: "opaque-key", Group: "ollama", Enabled: true}}
	return NewPoller(func() *config.Config { return cfg }, func(string) *http.Client { return srv.Client() }), cfg
}

func TestOllamaRefreshCoalescesAndThrottles(t *testing.T) {
	for _, status := range []int{200, 401, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var balances, catalogs atomic.Int32
			p, cfg := ollamaPoller(t, func(w http.ResponseWriter, r *http.Request) {
				if r.UserAgent() != egressid.OllamaUserAgent || r.Header.Get("Sec-Ch-Ua") != "" {
					t.Error("metadata used a browser identity")
				}
				if r.URL.Path == "/v1/models" {
					catalogs.Add(1)
					w.Write([]byte(`{"data":[{"id":"m"}]}`))
					return
				}
				balances.Add(1)
				w.WriteHeader(status)
				if status == 200 {
					w.Write([]byte(ollamaIncludedJSON))
				} else {
					w.Write([]byte(`{"error":"rate_limited"}`))
				}
			})
			var wg sync.WaitGroup
			for range 32 {
				wg.Go(func() {
					err := p.RefreshNow(context.Background(), cfg.Accounts[0])
					if (err == nil) != (status == 200) {
						t.Errorf("status %d, refresh error %v", status, err)
					}
				})
			}
			wg.Wait()
			for range 5 {
				_ = p.RefreshNow(context.Background(), cfg.Accounts[0])
			}
			if balances.Load() != 1 || catalogs.Load() != 1 {
				t.Fatalf("duplicate reads: balance=%d models=%d", balances.Load(), catalogs.Load())
			}
			s := p.Get("o")
			if s == nil || s.NextAt.Before(time.Now()) {
				t.Fatal("refresh left an immediate redispatch loop")
			}
			if status == 401 && !s.AuthFailed {
				t.Fatal("lost auth verdict")
			}
		})
	}
}

func TestOllamaRefreshWaiterCancellationDoesNotCancelOwner(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	p, cfg := ollamaPoller(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		close(entered)
		<-release
		w.Write([]byte(ollamaIncludedJSON))
	})
	done := make(chan error, 1)
	go func() { done <- p.RefreshNow(context.Background(), cfg.Accounts[0]) }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.RefreshNow(ctx, cfg.Accounts[0]); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel = %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if p.Get("o").Ollama == nil {
		t.Fatal("waiter cancellation lost owner result")
	}
}

func TestOllamaLateSourceCannotRestoreQuota(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	p, cfg := ollamaPoller(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		close(entered)
		<-release
		w.Write([]byte(ollamaIncludedJSON))
	})
	var current atomic.Pointer[config.Config]
	current.Store(cfg)
	p.Cfg = current.Load
	done := make(chan error, 1)
	go func() { done <- p.RefreshNow(context.Background(), cfg.Accounts[0]) }()
	<-entered
	next := *cfg
	next.Accounts = append([]config.Account(nil), cfg.Accounts...)
	next.Accounts[0].RuntimeKey = "replacement-key"
	current.Store(&next)
	p.Forget("o")
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if p.Get("o") != nil {
		t.Fatal("late old-credential quota was restored")
	}
}

func TestOllamaRefreshFailurePreservesLastGoodAndCadence(t *testing.T) {
	var calls atomic.Int32
	p, cfg := ollamaPoller(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(503)
	})
	old := time.Now().Add(-time.Hour)
	p.Load(map[string]*Snapshot{"o": {CatalogSource: ResponseBindingSource(cfg, cfg.Accounts[0]), Catalog: map[string]bool{"m": true}, CatalogAt: time.Now(), BalanceAt: old, Ollama: &OllamaBalance{IncludedUSD: "42"}}})
	if err := p.RefreshNow(context.Background(), cfg.Accounts[0]); err == nil {
		t.Fatal("expected failure")
	}
	for range 5 {
		_ = p.RefreshNow(context.Background(), cfg.Accounts[0])
	}
	s := p.Get("o")
	if calls.Load() != 1 || s.Ollama.IncludedUSD != "42" || !s.BalanceAt.Equal(old) || s.LastError == "" {
		t.Fatalf("last good/cadence lost: calls=%d snapshot=%+v", calls.Load(), s)
	}
}

func TestOllamaNewModelDoesNotRescheduleBalance(t *testing.T) {
	p, cfg := ollamaPoller(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected upstream request") })
	next := time.Now().Add(time.Hour)
	p.store("o", &Snapshot{CatalogSource: ResponseBindingSource(cfg, cfg.Accounts[0]), NextAt: next})
	p.NoteModel("new-opendesign-model")
	if !p.View("o").NextAt.Equal(next) {
		t.Fatal("another pool's new model triggered Ollama balance refresh")
	}
}

func TestOllamaStaleRefreshCallerDoesNotUseOldKey(t *testing.T) {
	p, cfg := ollamaPoller(t, func(http.ResponseWriter, *http.Request) { t.Error("stale caller contacted upstream") })
	old := cfg.Accounts[0]
	cfg.Accounts[0].RuntimeKey = "new-key"
	if err := p.RefreshNow(context.Background(), old); err == nil {
		t.Fatal("stale account accepted after credential change")
	}
}

func TestOllamaCachedRefreshKeepsPublishedSnapshotImmutable(t *testing.T) {
	p, cfg := ollamaPoller(t, func(http.ResponseWriter, *http.Request) { t.Error("cached refresh contacted upstream") })
	old := &Snapshot{CatalogSource: ResponseBindingSource(cfg, cfg.Accounts[0]), BalanceAt: time.Now(), Ollama: &OllamaBalance{IncludedUSD: "42"}}
	p.store("o", old)
	if err := p.RefreshNow(context.Background(), cfg.Accounts[0]); err != nil {
		t.Fatal(err)
	}
	if !old.NextAt.IsZero() || p.View("o").NextAt.IsZero() {
		t.Fatal("cached refresh mutated a published quota snapshot")
	}
}

func TestOllamaBalanceRefusesRedirect(t *testing.T) {
	var redirected atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/balance" {
			redirected.Add(1)
			return
		}
		http.Redirect(w, r, "/login", 302)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client()}
	if _, err := c.Balance(context.Background(), srv.URL, "secret", "o"); err == nil {
		t.Fatal("redirect accepted as balance")
	}
	if redirected.Load() != 0 {
		t.Fatal("balance followed a credential redirect")
	}
}
