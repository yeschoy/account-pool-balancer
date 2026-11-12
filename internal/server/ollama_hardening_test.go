package server

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/egressid"
	"account-pool-balancer/internal/quota"
)

func newOllamaEnv(t *testing.T, accts []config.Account) *env {
	t.Helper()
	e := newEnv(t, accts, func(c *config.Config) {
		c.Upstream.OllamaBase = c.Upstream.RuntimeBase
		c.Routing.Sticky = false
		c.Routing.ConcurrencyWaitMs = 5000
		c.Routing.MaxAccounts = 1
		c.ClientKeys = []config.ClientKey{{ID: "oll-ck", Provider: config.ProviderOllama, Group: "ollama", KeyHash: config.HashKey("sk-odb-oll-test"), Enabled: true}}
	})
	e.key = "sk-odb-oll-test"
	return e
}

func TestOllamaPrivacyAtActualInferenceBoundary(t *testing.T) {
	e := newOllamaEnv(t, []config.Account{{ID: "o", Provider: config.ProviderOllama, Group: "ollama", RuntimeKey: "opaque-upstream-key-long-enough"}})
	seen := make(chan http.Header, 1)
	e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, body map[string]any) {
		seen <- r.Header.Clone()
		writeOK(w, key)
	}
	resp := e.post("/v1/messages", map[string]any{"model": "m", "max_tokens": 1, "messages": []any{map[string]any{"role": "user", "content": "hello"}}}, map[string]string{
		"User-Agent": "Mozilla/5.0 private-device", "Sec-CH-UA-Platform": "private-device",
		"X-Stainless-OS": "private-device", "X-Device-ID": "private-device", "X-Session-ID": "private-session",
		"Traceparent": "private-trace", "Origin": "https://private.example", "Cookie": "private-cookie", "X-Api-Key": "foreign-key",
	})
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("inference status %d", resp.StatusCode)
	}
	h := <-seen
	if h.Get("User-Agent") != egressid.OllamaUserAgent || h.Get("Authorization") != "Bearer opaque-upstream-key-long-enough" || h.Get("Anthropic-Version") == "" {
		t.Fatal("lost honest identity or required authentication/version")
	}
	for k, values := range h {
		if strings.Contains(strings.Join(values, " "), "private") || k == "X-Api-Key" {
			t.Errorf("private header %s reached upstream", k)
		}
	}
}

func TestOllamaConcurrentRequestsKeepAtomicAccountLimits(t *testing.T) {
	accts := make([]config.Account, 4)
	for i := range accts {
		accts[i] = config.Account{ID: fmt.Sprintf("o%d", i), Provider: config.ProviderOllama, Group: "ollama", RuntimeKey: fmt.Sprintf("opaque-upstream-key-long-%d", i), MaxConc: 4}
	}
	e := newOllamaEnv(t, accts)
	var mu sync.Mutex
	active, peak := map[string]int{}, map[string]int{}
	var total, totalPeak int
	e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, body map[string]any) {
		mu.Lock()
		active[key]++
		peak[key] = max(peak[key], active[key])
		total++
		totalPeak = max(totalPeak, total)
		mu.Unlock()
		time.Sleep(2 * time.Millisecond) // controlled upstream work, not test synchronization
		writeOK(w, key)
		mu.Lock()
		active[key]--
		total--
		mu.Unlock()
	}
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			resp := e.post("/v1/chat/completions", chat(false), nil)
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Errorf("concurrent request status %d", resp.StatusCode)
			}
		})
	}
	wg.Wait()
	if e.up.callCount() != 64 || totalPeak < 2 {
		t.Fatalf("not concurrent: calls=%d peak=%d", e.up.callCount(), totalPeak)
	}
	for key, n := range peak {
		if n > 4 {
			t.Errorf("account %s exceeded limit: %d", key, n)
		}
	}
}

func TestOllamaInferenceNeverFollowsRedirect(t *testing.T) {
	e := newOllamaEnv(t, []config.Account{{ID: "o", Provider: config.ProviderOllama, Group: "ollama", RuntimeKey: "opaque-upstream-key-long-enough"}})
	var calls atomic.Int32
	e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, body map[string]any) {
		calls.Add(1)
		http.Redirect(w, r, "/v1/redirected", 307)
	}
	resp := e.post("/v1/chat/completions", chat(false), nil)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if calls.Load() != 1 {
		t.Fatalf("redirect replayed credential request %d times", calls.Load())
	}
}

func TestOllamaSourceChangesInvalidateQuotaAndModelCache(t *testing.T) {
	for _, change := range []string{"key", "proxy", "base"} {
		t.Run(change, func(t *testing.T) {
			e := newOllamaEnv(t, []config.Account{{ID: "o", Provider: config.ProviderOllama, Group: "ollama", RuntimeKey: "opaque-upstream-key-long-enough"}})
			cfg := e.srv.cfg.Get()
			e.srv.quota.Load(map[string]*quota.Snapshot{"o": {CatalogSource: quota.ResponseBindingSource(cfg, cfg.Accounts[0]), Ollama: &quota.OllamaBalance{IncludedUSD: "0", PurchasedUSD: "0"}, BalanceAt: time.Now(), AuthFailed: true}})
			before := e.srv.models.gen
			_, err := e.srv.cfg.Update(-1, func(c *config.Config) error {
				switch change {
				case "key":
					c.Accounts[0].RuntimeKey = "replacement-upstream-key"
				case "proxy":
					c.Accounts[0].ProxyURL = "http://127.0.0.1:1"
				case "base":
					c.Upstream.OllamaBase = "http://127.0.0.1:2/v1"
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if e.srv.quota.Get("o") != nil {
				t.Fatal("old quota/auth verdict survived a source change")
			}
			if e.srv.models.gen <= before {
				t.Fatal("old model cache survived a source change")
			}
		})
	}
}

func TestOllamaProxyFailureNeverFallsBackToDirect(t *testing.T) {
	e := newOllamaEnv(t, []config.Account{{ID: "o", Provider: config.ProviderOllama, Group: "ollama", RuntimeKey: "opaque-upstream-key-long-enough", ProxyURL: "http://127.0.0.1:1"}})
	resp := e.post("/v1/chat/completions", chat(false), nil)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode == 200 || e.up.callCount() != 0 {
		t.Fatal("failed proxy fell back to direct")
	}
}

func TestTransportCacheColdStartIsShared(t *testing.T) {
	s := &Server{}
	cfg := config.Default().Routing
	out := make(chan *http.Transport, 64)
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() { out <- s.transportFor("", cfg, "") })
	}
	wg.Wait()
	close(out)
	var first *http.Transport
	for tr := range out {
		if first == nil {
			first = tr
		}
		if tr != first {
			t.Fatal("concurrent cold start created separate connection pools")
		}
	}
	first.CloseIdleConnections()
}

func BenchmarkOllamaTransportCache(b *testing.B) {
	cfg := config.Default().Routing
	for _, legacy := range []bool{true, false} {
		name := "concurrent-cache"
		if legacy {
			name = "legacy-exclusive"
		}
		b.Run(name, func(b *testing.B) {
			s := &Server{}
			tr := s.transportFor("", cfg, "")
			defer tr.CloseIdleConnections()
			legacyMap := map[string]*http.Transport{"": tr}
			var mu sync.Mutex
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if legacy {
						mu.Lock()
						got := legacyMap[""]
						mu.Unlock()
						if got != tr {
							b.Error("cache miss")
						}
					} else if s.transportFor("", cfg, "") != tr {
						b.Error("cache miss")
					}
				}
			})
		})
	}
}
