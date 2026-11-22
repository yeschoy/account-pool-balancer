package pool

import (
	"time"

	"account-pool-balancer/internal/config"
)

// ownerLeaseLocked is not another scheduler: it uses the same eligibility,
// health, concurrency/RPM reservation and lease lifecycle under Pool.mu. A
// request owner can fail or wait, never silently borrow another account.
func (p *Pool) ownerLeaseLocked(cfg *config.Config, req Request, now time.Time) (*Lease, error) {
	a, ok := cfg.Account(req.PinnedAccountID)
	if !ok || a.Group != req.Group || a.Provider != config.ProviderMistral || req.Excluded[a.ID] || p.unavailableLocked(cfg, a, req.Model, now) != "" || p.healthExcludedLocked(cfg, a.ID, now) {
		return nil, &BindingError{Reason: "request owner unavailable"}
	}
	if ok, by, ra := p.admitLocked(a, now); !ok {
		return nil, &CapacityError{BlockedBy: by, RetryAfter: ra}
	}
	// No mutation/replacement of ordinary session preferences. The outer
	// request's owner is separate from the long-lived best-effort sticky cache.
	return p.leaseLocked(a, Decision{AccountID: a.ID, Reason: "request-owner", BindingResult: "request-owner", Candidates: 1}, true), nil
}
