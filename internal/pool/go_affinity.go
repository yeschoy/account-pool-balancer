package pool

import (
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
)

// RememberCache records successful Go cache affinity in the existing bounded
// LRU. It is deliberately not server-side response state and is not persisted.
func (p *Pool) RememberCache(cfg *config.Config, key, accountID, model string) {
	if key == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if old := p.getBindingLocked(key, now); old != nil {
		if a, ok := cfg.Account(old.accountID); ok && old.cacheSource == quota.OpenCodeSource(cfg, a) && p.unavailableLocked(cfg, a, model, now) == "" {
			if old.accountID == accountID {
				old.expiresAt = now.Add(ttl(cfg, false))
			}
			return // a busy fallback must not bounce the preferred cache owner
		}
	}
	p.deleteBindingLocked(key)
	if len(p.bindings) >= maxBindings {
		victim := ""
		for el := p.lru.Back(); el != nil; el = el.Prev() {
			if b := el.Value.(*binding); b.cache {
				victim = b.key
				break
			}
		}
		if victim == "" {
			return
		} // optional cache hints never evict response/session state
		p.deleteBindingLocked(victim)
	}
	p.gen++
	a, ok := cfg.Account(accountID)
	if !ok || a.Provider != config.ProviderOpenCodeGo {
		return
	}
	b := &binding{key: key, accountID: accountID, cache: true, cacheSource: quota.OpenCodeSource(cfg, a), confirmed: true, expiresAt: now.Add(ttl(cfg, false)), generation: p.gen}
	p.bindings[key] = p.lru.PushFront(b)
	for p.lru.Len() > maxBindings {
		old := p.lru.Back()
		if old.Value.(*binding).source != "" {
			p.dirty = true
		}
		delete(p.bindings, old.Value.(*binding).key)
		p.lru.Remove(old)
	}
}

func (p *Pool) UpstreamSession(key string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if b := p.getBindingLocked(key, p.now()); b != nil {
		return b.session
	}
	return ""
}

func (p *Pool) PauseUntil(id, ruleID string) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	if st := p.stateLocked(id, p.now()); st != nil && st.RuleID == ruleID && p.now().Before(st.CooldownUntil) {
		return st.CooldownUntil
	}
	return time.Time{}
}

type BindingSummary struct {
	Sessions  int `json:"sessions"`
	Cache     int `json:"cache"`
	Responses int `json:"responses"`
}

// BindingSummaryFor ignores expired and other-provider accounts. It does not
// mutate leases, cursors or exported response-chain state.
func (p *Pool) BindingSummaryFor(accounts map[string]bool) BindingSummary {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out BindingSummary
	for _, el := range p.bindings {
		b := el.Value.(*binding)
		if !accounts[b.accountID] || !p.now().Before(b.expiresAt) {
			continue
		}
		switch {
		case b.cache:
			out.Cache++
		case b.source != "":
			out.Responses++
		default:
			out.Sessions++
		}
	}
	return out
}
