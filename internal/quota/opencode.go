package quota

import (
	"context"
	"errors"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
)

func OpenCodeSource(cfg *config.Config, a config.Account) string {
	bases := cfg.OpenCode.ProtocolBases(a.Provider)
	return config.HashKey(bases["chat"] + "\x00" + bases["responses"] + "\x00" + bases["messages"] + "\x00" + a.Provider + "\x00" + a.RuntimeKey + "\x00" + a.ProxyURL + "\x00" + cfg.RuntimeBase(a) + "\x00" + cfg.OpenCode.CapabilitiesURL + "\x00" + cfg.OpenCode.ZenDocsURL + "\x00" + cfg.OpenCode.GoDocsURL)
}

func (p *Poller) fetchOpenCode(ctx context.Context, cfg *config.Config, a config.Account) error {
	return p.fetchOpenCodeForce(ctx, cfg, a, false)
}

func (p *Poller) fetchOpenCodeForce(ctx context.Context, cfg *config.Config, a config.Account, force bool) error {
	if !cfg.OpenCode.Enabled {
		if force {
			return errors.New("OpenCode is disabled; no upstream was contacted")
		}
		return nil
	}
	now := time.Now()
	s := p.Get(a.ID)
	source := OpenCodeSource(cfg, a)
	if s == nil || s.OpenCode == nil || s.OpenCode.Source != source {
		s = &Snapshot{OpenCode: &opencode.State{Source: source}}
	}
	state := s.OpenCode
	client := opencode.NoRedirect(p.HTTPFor(a.ProxyURL))
	ttl := time.Duration(cfg.OpenCode.RefreshSec) * time.Second
	var partErr error
	if force || s.CatalogAt.IsZero() || now.Sub(s.CatalogAt) >= ttl {
		c, err := FetchCatalogFull(ctx, client, cfg.RuntimeBase(a), a.RuntimeKey)
		if err != nil {
			s.CatalogError = err.Error()
			partErr = err
		} else {
			applyCatalog(s, c, now)
		}
	}
	if force || state.ModelsAt.IsZero() || now.Sub(state.ModelsAt) >= ttl {
		if force {
			p.discovery.Invalidate(a.ProxyURL, cfg.OpenCode.CapabilitiesURL, cfg.OpenCode.ZenDocsURL, cfg.OpenCode.GoDocsURL)
		}
		all, err := p.discovery.Get(ctx, client, a.ProxyURL, cfg.OpenCode.CapabilitiesURL, cfg.OpenCode.ZenDocsURL, cfg.OpenCode.GoDocsURL, ttl)
		if err != nil {
			state.CapabilityError = err.Error()
			partErr = err
		} else {
			tier := "zen"
			if a.Provider == config.ProviderOpenCodeGo {
				tier = "go"
			}
			state.Models = map[string]opencode.Model{}
			// No account can inherit another key's listing. Metadata is only joined
			// onto this account's own advertised model IDs.
			for id, en := range s.Catalog {
				if en {
					if m, ok := all[tier][id]; ok {
						state.Models[id] = m
					}
				}
			}
			state.ModelsAt, state.CapabilityError = now, ""
		}
	}
	state.CheckedAt = now
	if a.Provider == config.ProviderOpenCodeGo && (force || state.UsageAt.IsZero() || now.Sub(state.UsageAt) >= time.Minute) {
		u, err := opencode.FetchUsage(ctx, client, cfg.RuntimeBase(a), a.RuntimeKey)
		if err != nil {
			state.UsageError = err.Error()
			partErr = err
		} else {
			state.Usage, state.UsageAt, state.UsageError = u, now, ""
		}
	}
	if partErr != nil {
		s.LastError, s.LastErrorAt = partErr.Error(), now
		s.Failures++
		s.NextAt = now.Add(time.Minute)
	} else {
		s.LastError = ""
		s.Failures = 0
		s.LastOKAt = now
		interval := ttl
		if a.Provider == config.ProviderOpenCodeGo {
			interval = time.Minute
		}
		s.NextAt = now.Add(interval)
	}
	// A late result for an old key/proxy/base must never revive its permissions.
	current := p.Cfg()
	if next, ok := current.Account(a.ID); ok && current.Revision == cfg.Revision && OpenCodeSource(current, next) == source {
		p.store(a.ID, s)
	}
	return partErr
}
