package pool

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func TestBindingLimitEvictionMarksPersistedRemoval(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts = []config.Account{{ID: "strict", Provider: config.ProviderTokenHarbor, RuntimeKey: "mock", Enabled: true}}
	p := New(nil)
	key := strings.Repeat("a", 64)
	p.BindWithSession(cfg, key, "strict", "opaque")
	p.mu.Lock()
	p.dirty = false
	for i := 0; i < maxBindings; i++ {
		p.putProvisionalLocked(cfg, Request{Affinity: fmt.Sprintf("%064x", i), RequestID: "test"}, "strict", time.Now())
	}
	dirty := p.dirty
	p.mu.Unlock()
	if p.BindingLimit() != 4096 || p.BindingCount() != 4096 || !dirty {
		t.Fatal("global LRU cap or persisted eviction dirty marker lost")
	}
	if _, ok := p.BoundAccount(key); ok {
		t.Fatal("least-recent binding survived capacity eviction")
	}
	export, _ := p.ExportState()
	for _, b := range export.Bindings {
		if b.Key == key {
			t.Fatal("evicted strict binding would be resurrected after restart")
		}
	}
}
