package quota

import "account-pool-balancer/internal/config"

// PoolChanged cancels reads for disabled pools and suppresses their late
// results even if the pool is enabled again before the old worker returns.
func (p *Poller) PoolChanged(old, cfg *config.Config) {
	p.mu.Lock()
	for id, cancel := range p.cancels {
		a, ok := cfg.Account(id)
		if !ok || !cfg.PoolEnabled(a.Provider) {
			p.discarded[id] = true
			cancel()
			delete(p.urgent, id)
			delete(p.pending, id)
		}
	}
	for _, a := range cfg.Accounts {
		if !cfg.PoolEnabled(a.Provider) {
			delete(p.urgent, a.ID)
			delete(p.pending, a.ID)
			continue
		}
		if old != nil && !old.PoolEnabled(a.Provider) {
			if snap := p.snaps[a.ID]; snap != nil {
				p.rescheduleLocked(a.ID, snap)
			}
		}
	}
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}
