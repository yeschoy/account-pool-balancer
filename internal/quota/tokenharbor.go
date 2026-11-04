package quota

import (
	"context"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
)

// Only the documented API-key model catalog is read. A catalog is not a balance
// or evidence of inference entitlement; no dashboard/session endpoint is used.
func (p *Poller) fetchTokenHarbor(ctx context.Context, cfg *config.Config, a config.Account) error {
	source := ResponseBindingSource(cfg, a)
	s := p.Get(a.ID)
	if s == nil || s.CatalogSource != source {
		s = &Snapshot{CatalogSource: source}
	}
	now := time.Now()
	cat, err := FetchCatalogFull(ctx, opencode.NoRedirect(p.HTTPFor(a.ProxyURL)), cfg.RuntimeBase(a), a.RuntimeKey)
	if err == nil {
		s.Catalog = make(map[string]bool, len(cat.Models))
		for id, m := range cat.Models {
			s.Catalog[id] = m.Enabled
		}
		s.CatalogAt, s.LastOKAt = now, now
		s.CatalogError, s.LastError = "", ""
		s.AuthFailed, s.Failures = false, 0
		// Zero disables periodic sync, but manual refresh still works.
		interval := time.Duration(cfg.TokenHarbor.ModelSyncSec) * time.Second
		if interval == 0 {
			interval = 24 * time.Hour
		}
		s.NextAt = now.Add(interval)
	} else {
		s.CatalogError, s.LastError, s.LastErrorAt = err.Error(), err.Error(), now
		// A regional 403 is not proof that the API key was revoked.
		if he, ok := err.(*HTTPError); ok && he.Status == 401 {
			s.AuthFailed = true
		}
		s.Failures++
		s.NextAt = now.Add(backoff(s.Failures))
	}
	current := p.Cfg()
	if next, ok := current.Account(a.ID); ok && ResponseBindingSource(current, next) == source {
		p.store(a.ID, s)
	}
	return err
}
