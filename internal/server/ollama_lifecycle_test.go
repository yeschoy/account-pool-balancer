package server

import (
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
)

func TestOllamaQuotaRestartSourceValidation(t *testing.T) {
	for _, changeKey := range []bool{false, true} {
		name := "matching"
		if changeKey {
			name = "changed"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := New(dir, Options{})
			if err != nil {
				t.Fatal(err)
			}
			closeLogs := sync.OnceFunc(s.logs.Close)
			t.Cleanup(closeLogs)
			_, err = s.cfg.Update(-1, func(c *config.Config) error {
				c.Quota.Enabled = false
				c.Accounts = []config.Account{{ID: "o", Provider: config.ProviderOllama, Group: "ollama", RuntimeKey: "opaque-key", Enabled: true}}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			cfg := s.cfg.Get()
			s.quota.Load(map[string]*quota.Snapshot{"o": {CatalogSource: quota.ResponseBindingSource(cfg, cfg.Accounts[0]), BalanceAt: time.Now(), Ollama: &quota.OllamaBalance{IncludedUSD: "42"}}})
			if err := s.saveMetadata(true); err != nil {
				t.Fatal(err)
			}
			if changeKey {
				// Simulate a new saved key followed by a restart before the next
				// metadata tick. The file still contains the old key's quota.
				if _, err := s.cfg.Update(-1, func(c *config.Config) error { c.Accounts[0].RuntimeKey = "new-key"; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			closeLogs()
			restarted, err := New(dir, Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(restarted.logs.Close)
			got := restarted.quota.Get("o")
			if changeKey && got != nil {
				t.Fatal("loaded old-credential quota from metadata")
			}
			if !changeKey && (got == nil || got.Ollama == nil || got.Ollama.IncludedUSD != "42") {
				t.Fatal("lost matching persisted quota")
			}
		})
	}
}

func TestTransportCacheConcurrentInvalidation(t *testing.T) {
	e := newOllamaEnv(t, []config.Account{{ID: "o", Provider: config.ProviderOllama, Group: "ollama", RuntimeKey: "opaque-key"}})
	first := e.srv.transport("")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					_ = e.srv.transport("")
				}
			}
		})
	}
	for range 8 {
		if _, err := e.srv.cfg.Update(-1, func(c *config.Config) error { c.Routing.DirectMaxConns++; return nil }); err != nil {
			t.Error(err)
		}
	}
	close(stop)
	wg.Wait()
	last := e.srv.transport("")
	if last == first || last.MaxConnsPerHost != e.srv.cfg.Get().Routing.DirectMaxConns {
		t.Fatal("cache kept an obsolete connection limit")
	}
	last.CloseIdleConnections()
}

func TestOllamaSSEKeepsNativeStream(t *testing.T) {
	e := newOllamaEnv(t, []config.Account{{ID: "o", Provider: config.ProviderOllama, Group: "ollama", RuntimeKey: "opaque-upstream-key-long-enough"}})
	const wire = "data: {\"id\":\"chunk-1\",\"choices\":[{\"delta\":{\"content\":\"hello\"},\"index\":0}]}\n\ndata: [DONE]\n\n"
	e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, body map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(wire))
		w.(http.Flusher).Flush()
	}
	resp := e.post("/v1/chat/completions", chat(true), map[string]string{"Accept": "text/event-stream"})
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || string(b) != wire {
		t.Fatalf("native SSE changed: status=%d err=%v body=%q", resp.StatusCode, err, b)
	}
}
