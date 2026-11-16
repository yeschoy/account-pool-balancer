package server

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"account-pool-balancer/internal/config"
)

const altapiModelsInterval = time.Hour

// The group object is an epoch: a changed source gets a new pointer, so an
// older background read can never publish over a newer configuration.
// body is immutable and only read/written under altapiModels.Mutex.
type altapiModelsGroup struct {
	source string
	body   []byte
}

// Reconcile only on config publication, never in /v1/models. Names, balances,
// sessions, limits and unrelated settings are not catalogue sources. Preserve
// unchanged group objects across revisions so routine imports/settings don't
// cause a network fan-out for every group.
func (s *Server) reconcileALTAPIModels(cfg *config.Config) {
	groups := map[string][]string{}
	for _, a := range cfg.Accounts {
		if a.Provider == config.ProviderALTAPI && a.Enabled && a.RuntimeKey != "" {
			groups[a.Group] = append(groups[a.Group], a.ID+"\x00"+cfg.NativeBindingSource(a))
		}
	}
	s.altapiModels.Lock()
	defer s.altapiModels.Unlock()
	if s.altapiModels.wake == nil {
		s.altapiModels.wake = make(chan struct{}, 1)
	}
	// Config callbacks are serialized by Store. Do not regress if a caller
	// presents an older immutable snapshot.
	if cfg.Revision < s.altapiModels.revision {
		return
	}
	s.altapiModels.revision = cfg.Revision
	next := make(map[string]*altapiModelsGroup, len(groups))
	changed := false
	for group, sources := range groups {
		raw, _ := json.Marshal(struct {
			Sources       []string
			IgnoreCatalog bool
		}{sources, cfg.ALTAPI.EffectiveRouting().IgnoreCatalog})
		source := config.HashKey(string(raw))
		if old := s.altapiModels.groups[group]; old != nil && old.source == source {
			next[group] = old
		} else {
			next[group] = &altapiModelsGroup{source: source}
			changed = true
		}
	}
	s.altapiModels.groups = next
	if changed {
		select {
		case s.altapiModels.wake <- struct{}{}:
		default:
		}
	}
}

// All listing requests, even a cold miss or an overdue refresh, are read-only.
// Authentication/group selection happens before this lookup in handleModels.
func (s *Server) readALTAPIModels(cfg *config.Config, group string) []byte {
	s.altapiModels.Lock()
	defer s.altapiModels.Unlock()
	// Store publishes before its change callback. Do not expose old sources
	// during that narrow window; a stale request after a change also fails shut.
	if cfg.Revision != s.altapiModels.revision {
		return nil
	}
	if entry := s.altapiModels.groups[group]; entry != nil {
		return entry.body
	}
	return nil
}

// Only this service-owned loop scans accounts. A failed hourly refresh keeps
// the same source's last successful body; no cache or fake empty list is
// created from failure. Source changes discard the old group immediately.
func (s *Server) runALTAPIModels(ctx context.Context) {
	ticker := time.NewTicker(altapiModelsInterval)
	defer ticker.Stop()
	s.loopALTAPIModels(ctx, ticker.C)
}

func (s *Server) loopALTAPIModels(ctx context.Context, ticks <-chan time.Time) {
	s.altapiModels.Lock()
	wake := s.altapiModels.wake
	s.altapiModels.Unlock()
	select { // The startup pass already covers configuration queued before Run.
	case <-wake:
	default:
	}
	s.refreshALTAPIModels(ctx, false)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			s.refreshALTAPIModels(ctx, false)
		case <-wake:
			s.refreshALTAPIModels(ctx, true)
		}
	}
}

func (s *Server) refreshALTAPIModels(ctx context.Context, missingOnly bool) {
	s.altapiModels.scanMu.Lock()
	defer s.altapiModels.scanMu.Unlock()
	cfg := s.cfg.Get().ForProvider(config.ProviderALTAPI)
	s.altapiModels.Lock()
	targets := make(map[string]*altapiModelsGroup)
	if s.altapiModels.revision == cfg.Revision {
		for group, entry := range s.altapiModels.groups {
			if !missingOnly || entry.body == nil {
				targets[group] = entry
			}
		}
	}
	s.altapiModels.Unlock()
	for group, target := range targets {
		if ctx.Err() != nil {
			return
		}
		body := s.fetchModels(ctx, cfg, group)
		if ctx.Err() != nil {
			return
		}
		if body == nil {
			log.Printf("altapi models: background refresh failed; retaining the last successful catalogue if available")
			continue
		}
		entry := s.filterModels(cfg, group, modelsEntry{body: body})
		s.altapiModels.Lock()
		if s.altapiModels.groups[group] == target && ctx.Err() == nil {
			target.body = entry.body
		}
		s.altapiModels.Unlock()
	}
}
