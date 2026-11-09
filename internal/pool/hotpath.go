package pool

import (
	"time"

	"account-pool-balancer/internal/config"
)

type healthTotal struct {
	ring              *HealthRing
	minute            int64
	success, degraded int
}

// Only the current owner is needed on a healthy Mistral session hit. The full
// path still owns invalidation, temporary overflow and wait-overflow policy.
func (p *Pool) mistralHitLocked(cfg *config.Config, req Request, now time.Time) (*Lease, error, bool) {
	if !cfg.Routing.Sticky || req.RequireBinding || req.Affinity == "" || req.CacheAffinity != "" {
		return nil, nil, false
	}
	b := p.getBindingLocked(req.Affinity, now)
	if b == nil || !b.mistral || req.Excluded[b.accountID] {
		return nil, nil, false
	}
	a, ok := cfg.Account(b.accountID)
	if !ok || a.Provider != config.ProviderMistral || a.Group != req.Group || !p.validMistralBinding(cfg, b, req.ClientKeyID) || p.unavailableLocked(cfg, a, req.Model, now) != "" || p.healthExcludedLocked(cfg, a.ID, now) {
		return nil, nil, false
	}
	if admitted, by, retry := p.admitLocked(a, now); admitted {
		b.expiresAt = now.Add(ttl(cfg, b.explicit))
		p.touchBindingLocked(b)
		p.moveBindingLocked(b)
		result := "hit"
		if !b.confirmed {
			result = "provisional"
		}
		return p.bindingLeaseLocked(a, Decision{AccountID: a.ID, Reason: "binding", BindingResult: result, Candidates: 1}, b), nil, true
	} else if req.PreserveBinding || cfg.Routing.OnBindingBusy == "reject" {
		return nil, &CapacityError{BlockedBy: by, RetryAfter: retry}, true
	}
	return nil, nil, false
}

func (p *Pool) bindingLeaseLocked(a config.Account, d Decision, b *binding) *Lease {
	l := p.leaseLocked(a, d, false)
	if !b.confirmed {
		l.bindKey, l.bindGen, l.provisional = b.key, b.generation, true
	}
	return l
}

type groupWaiters struct {
	changed chan struct{}
	count   int
}

func (p *Pool) notifyGroupLocked(group string) {
	if w := p.waiters[group]; w != nil {
		close(w.changed)
		w.changed = make(chan struct{})
	}
}
