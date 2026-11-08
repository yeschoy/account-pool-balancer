package pool

import (
	"encoding/hex"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
)

// PersistedMistralBinding is an ordinary, stateless routing preference, never a
// server-side Responses owner. The key/source are hashes; no prompt or raw
// session/credential is persisted. All providers still share the one LRU cap.
type PersistedMistralBinding struct {
	Key         string    `json:"key"`
	AccountID   string    `json:"accountId"`
	ClientKeyID string    `json:"clientKeyId"`
	Source      string    `json:"source"`
	Explicit    bool      `json:"explicit"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

func mistralBindingSource(cfg *config.Config, a config.Account, clientKeyID string) string {
	if a.Provider != config.ProviderMistral || !a.Enabled || !cfg.ForProvider(config.ProviderMistral).Routing.Sticky || cfg.GroupPool(a.Group) != config.ProviderMistral {
		return ""
	}
	for _, key := range cfg.ClientKeys {
		if key.ID == clientKeyID && key.Enabled && key.Group == a.Group {
			return config.HashKey("mistral-session-v1\x00" + quota.ResponseBindingSource(cfg, a) + "\x00" + a.Group + "\x00" + key.ID + "\x00" + key.KeyHash)
		}
	}
	return ""
}

func (p *Pool) validMistralBinding(cfg *config.Config, b *binding, clientKeyID string) bool {
	a, ok := cfg.Account(b.accountID)
	return ok && b.clientKeyID == clientKeyID && b.source != "" && b.source == mistralBindingSource(cfg, a, clientKeyID)
}

// ImportMistralBindings follows strict-response import, so preferences cannot
// displace restored stateful owners. It preserves absolute deadlines and weak
// identity TTLs, and restores no active request/concurrency/RPM reservations.
func (p *Pool) ImportMistralBindings(saved []PersistedMistralBinding, cfg *config.Config) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for _, v := range saved {
		if len(p.bindings) >= maxBindings {
			break
		}
		if len(v.Key) != 64 || !now.Before(v.ExpiresAt) || p.bindings[v.Key] != nil {
			continue
		}
		if _, err := hex.DecodeString(v.Key); err != nil {
			continue
		}
		b := &binding{key: v.Key, accountID: v.AccountID, clientKeyID: v.ClientKeyID, source: v.Source,
			mistral: true, explicit: v.Explicit, confirmed: true, expiresAt: v.ExpiresAt}
		if !p.validMistralBinding(cfg, b, v.ClientKeyID) {
			continue
		}
		// A shorter TTL configured while stopped may shorten, never renew,
		// the saved deadline. Explicit/fallback type is retained independently.
		limit := now.Add(ttl(cfg.ForProvider(config.ProviderMistral), b.explicit))
		if b.expiresAt.After(limit) {
			b.expiresAt = limit
		}
		p.gen++
		b.generation = p.gen
		p.bindings[b.key] = p.lru.PushBack(b)
	}
}

// ReconcileMistralBindings also handles downstream-key changes, which do not
// necessarily change any account. Removal uses the common dirty/LRU path.
func (p *Pool) ReconcileMistralBindings(cfg *config.Config) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for el := p.lru.Front(); el != nil; {
		next := el.Next()
		b := el.Value.(*binding)
		if b.mistral && (!p.now().Before(b.expiresAt) || !p.validMistralBinding(cfg, b, b.clientKeyID)) {
			p.deleteBindingLocked(b.key)
		}
		el = next
	}
}

func (p *Pool) touchBindingLocked(b *binding) {
	if b.mistral {
		// Ordinary preferences are coalesced with existing sampled metadata,
		// not a synchronous metadata rewrite for every streaming request.
		p.samplesDirty = true
	} else if b.source != "" {
		p.dirty = true
	}
}
