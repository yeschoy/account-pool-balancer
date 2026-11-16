package server

import (
	"context"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
)

// Raw upstream unions and final, entitlement-filtered responses have separate
// lifetimes: a quota update must not trigger another network fan-out.
type modelsKey struct {
	provider  string
	group     string
	published bool
}

type modelsEntry struct {
	body      []byte
	expiresAt time.Time
	revision  int64
}

type modelsCall struct {
	done     chan struct{}
	entry    modelsEntry
	revision int64
}

// resetModels also detaches in-flight reads. Their existing callers can finish,
// but new callers cannot join an obsolete read, nor can it overwrite new data.
func (s *Server) resetModels() {
	s.models.Lock()
	s.models.entries = nil
	s.models.inflight = nil
	s.models.Unlock()
}

func (s *Server) invalidateAccountModels(id string) {
	cfg := s.cfg.Get()
	a, ok := cfg.Account(id)
	if !ok || a.Provider == config.ProviderALTAPI {
		return // ALTAPI listings are refreshed only by their service-owned worker.
	}
	s.models.Lock()
	defer s.models.Unlock()
	invalidate := func(key modelsKey) bool {
		dependent := key.group == a.Group || (config.IsOpenCode(key.provider) && cfg.OpenCode.FallbackGroup(key.provider) == a.Group)
		// These providers build even the raw union from quota snapshots, not
		// a separate upstream /models read. OpenCode fallback is dependent on
		// both groups, but never on unrelated providers.
		local := key.provider == config.ProviderTokenHarbor || key.provider == config.ProviderPerch || config.IsOpenCode(key.provider)
		return dependent && (key.published || local)
	}
	for key := range s.models.entries {
		if invalidate(key) {
			delete(s.models.entries, key)
		}
	}
	for key := range s.models.inflight {
		if invalidate(key) {
			delete(s.models.inflight, key)
		}
	}
}

func (s *Server) publishedModels(ctx context.Context, cfg *config.Config, group string) []byte {
	if cfg.GroupPool(group) == config.ProviderALTAPI {
		return s.readALTAPIModels(cfg, group)
	}
	key := modelsKey{provider: cfg.GroupPool(group), group: group, published: true}
	return s.cachedModels(ctx, cfg, key, func(ctx context.Context) modelsEntry {
		entry := s.upstreamModels(ctx, cfg, group)
		if entry.body == nil {
			return entry
		}
		return s.filterModels(cfg, group, entry)
	}).body
}

func (s *Server) upstreamModels(ctx context.Context, cfg *config.Config, group string) modelsEntry {
	cfg = cfg.ForProvider(cfg.GroupPool(group))
	key := modelsKey{provider: cfg.GroupPool(group), group: group}
	return s.cachedModels(ctx, cfg, key, func(ctx context.Context) modelsEntry {
		// Capture snapshot deadlines before building the union, so it cannot
		// extend their existing permission/protocol freshness window.
		until := s.localModelsExpiry(cfg, group, time.Time{})
		body := s.fetchModels(ctx, cfg, group)
		until = minModelsExpiry(time.Now().Add(time.Duration(cfg.Routing.ModelsCacheSec)*time.Second), until)
		return modelsEntry{body: body, expiresAt: until}
	})
}

// cachedModels coalesces misses for both cache layers. No account iteration,
// snapshot copying or JSON decoding is needed on a published-cache hit.
func (s *Server) cachedModels(ctx context.Context, cfg *config.Config, key modelsKey, load func(context.Context) modelsEntry) modelsEntry {
	s.models.Lock()
	if entry, ok := s.models.entries[key]; ok && entry.revision == cfg.Revision && time.Now().Before(entry.expiresAt) {
		s.models.Unlock()
		return entry
	}
	if call := s.models.inflight[key]; call != nil && call.revision == cfg.Revision {
		s.models.Unlock()
		select {
		case <-call.done:
			return call.entry
		case <-ctx.Done():
			return modelsEntry{}
		}
	}
	call := &modelsCall{done: make(chan struct{}), revision: cfg.Revision}
	if s.models.inflight == nil {
		s.models.inflight = map[modelsKey]*modelsCall{}
	}
	s.models.inflight[key] = call
	s.models.Unlock()

	// A caller leaving must not cancel the shared read for other callers.
	call.entry = load(context.WithoutCancel(ctx))
	call.entry.revision = cfg.Revision
	s.models.Lock()
	if s.models.inflight[key] == call {
		delete(s.models.inflight, key)
		if call.entry.body != nil {
			if s.models.entries == nil {
				s.models.entries = map[modelsKey]modelsEntry{}
			}
			s.models.entries[key] = call.entry
		}
	}
	s.models.Unlock()
	close(call.done)
	return call.entry
}

// A zero deadline means no additional bound.
func minModelsExpiry(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}
	return a
}

// Snapshot-only catalogues must expire even with polling disabled. This runs
// on a miss only, including the opposite tier when OpenCode fallback is on.
func (s *Server) localModelsExpiry(cfg *config.Config, group string, until time.Time) time.Time {
	provider := cfg.GroupPool(group)
	if provider != config.ProviderTokenHarbor && provider != config.ProviderPerch && !config.IsOpenCode(provider) {
		return until
	}
	now := time.Now()
	fallback := ""
	if config.IsOpenCode(provider) {
		fallback = cfg.OpenCode.FallbackGroup(provider)
	}
	for _, a := range cfg.Accounts {
		if !a.Enabled || (a.Group != group && (fallback == "" || a.Group != fallback)) {
			continue
		}
		q := s.quota.View(a.ID)
		if q == nil {
			continue
		}
		switch {
		case provider == config.ProviderTokenHarbor:
			if a.Provider == provider && q.TokenHarbor != nil && q.TokenHarbor.Source == quota.ResponseBindingSource(cfg, a) && q.TokenHarbor.PlanFresh(now) {
				until = minModelsExpiry(until, q.TokenHarbor.ValidUntil)
			}
		case config.IsOpenCode(provider):
			if !config.IsOpenCode(a.Provider) || q.OpenCode == nil || q.OpenCode.Source != quota.OpenCodeSource(cfg, a) {
				continue
			}
			for _, at := range []time.Time{q.CatalogAt, q.OpenCode.ModelsAt} {
				if expiry := at.Add(24 * time.Hour); now.Before(expiry) {
					until = minModelsExpiry(until, expiry)
				}
			}
		case provider == config.ProviderPerch:
			if q.PublicModels(now) != nil {
				until = minModelsExpiry(until, q.CatalogAt.Add(24*time.Hour))
			}
		}
	}
	return until
}
