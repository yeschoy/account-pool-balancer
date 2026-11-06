package quota

import (
	"context"
	"errors"
	"maps"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
	"account-pool-balancer/internal/thregister"
)

// No credential/cookie is stored here. Directory is not an entitlement.
type TokenHarborState struct {
	Source      string                  `json:"source"`
	Owner       string                  `json:"owner,omitempty"`
	Plan        thregister.Subscription `json:"plan"`
	Usage       thregister.PassUsage    `json:"usage"`
	FreeModels  bool                    `json:"freeModels"`
	CheckedAt   time.Time               `json:"checkedAt"`
	ValidUntil  time.Time               `json:"validUntil"`
	Error       string                  `json:"error,omitempty"`
	Directory   map[string]bool         `json:"directory,omitempty"`
	DirectoryAt time.Time               `json:"directoryAt,omitzero"`
	Models      map[string]string       `json:"-"`
}

func (s *TokenHarborState) clone() *TokenHarborState {
	if s == nil {
		return nil
	}
	c := *s
	c.Directory = maps.Clone(s.Directory)
	c.Models = maps.Clone(s.Models)
	if s.Usage.OverageBilling != nil {
		v := *s.Usage.OverageBilling
		c.Usage.OverageBilling = &v
	}
	if s.Usage.UsedPercent != nil {
		p := *s.Usage.UsedPercent
		c.Usage.UsedPercent = &p
	}
	return &c
}
func (s *TokenHarborState) PlanFresh(now time.Time) bool {
	return s != nil && s.Source != "" && s.Error == "" && s.Plan.Active() && !s.CheckedAt.IsZero() && !now.Before(s.CheckedAt) && now.Before(s.ValidUntil)
}
func (s *TokenHarborState) UsageFresh(now time.Time) bool {
	return s.PlanFresh(now) && s.Usage.Error == "" && s.Usage.UsedPercent != nil && *s.Usage.UsedPercent >= 0 && *s.Usage.UsedPercent <= 100 && s.Usage.OverageBilling != nil && (!s.Usage.Activated || now.Before(s.Usage.ResetsAt))
}
func (s *TokenHarborState) Gate(now time.Time) string {
	if !s.PlanFresh(now) {
		return "plan-unknown"
	}
	if s.Usage.OverageBilling != nil && *s.Usage.OverageBilling {
		return "wallet-overage-enabled"
	}
	if !s.UsageFresh(now) {
		return "quota-unknown"
	}
	if s.Usage.Exhausted || *s.Usage.UsedPercent >= 100 {
		return "quota-exhausted"
	}
	return ""
}
func passRank(tier string) int {
	switch tier {
	case "plus", "agent":
		return 1
	case "pro", "office":
		return 2
	case "frontier":
		return 3
	}
	return 0
}

func (p *Poller) fetchTokenHarbor(ctx context.Context, cfg *config.Config, a config.Account, force bool) error {
	source := ResponseBindingSource(cfg, a)
	s := p.Get(a.ID)
	if s == nil || s.CatalogSource != source {
		s = &Snapshot{CatalogSource: source}
	}
	now := time.Now()
	previous := s.TokenHarbor
	var observed *TokenHarborState
	var err error
	if p.TokenHarborFetch == nil {
		err = errors.New("没有可用的原注册会话，无法确认套餐")
	} else {
		observed, err = p.TokenHarborFetch(ctx, cfg, a)
	}
	if observed == nil {
		observed = &TokenHarborState{}
	}
	observed.Source, observed.CheckedAt, observed.ValidUntil = source, now, now.Add(10*time.Minute)
	if previous != nil {
		observed.Directory, observed.DirectoryAt = previous.Directory, previous.DirectoryAt
	}
	if err != nil {
		observed.Error = err.Error()
	}
	s.TokenHarbor = observed
	// A manual refresh also refreshes the native directory. Periodic account
	// observations reuse it on its separate cadence, never from inference.
	modelInterval := time.Duration(cfg.TokenHarbor.ModelSyncSec) * time.Second
	if err == nil && (force || observed.DirectoryAt.IsZero() || (modelInterval > 0 && now.Sub(observed.DirectoryAt) >= modelInterval)) {
		cat, readErr := FetchCatalogFull(ctx, opencode.NoRedirect(p.HTTPFor(a.ProxyURL)), cfg.RuntimeBase(a), a.RuntimeKey, a.ID)
		if readErr != nil {
			err = readErr
			if he, ok := readErr.(*HTTPError); ok {
				s.AuthFailed = he.Status == 401
			}
			observed.Error = "原生目录读取失败：" + readErr.Error()
		} else {
			observed.Directory = make(map[string]bool, len(cat.Models))
			for id, m := range cat.Models {
				observed.Directory[id] = m.Enabled
			}
			observed.DirectoryAt = now
		}
	}
	if err == nil && observed.Plan.Active() && now.Sub(observed.DirectoryAt) < 24*time.Hour {
		s.Catalog = map[string]bool{}
		for id, enabled := range observed.Directory {
			minimum, known := observed.Models[id]
			if !enabled || !known {
				continue
			}
			if minimum == "free" {
				if observed.FreeModels {
					s.Catalog[id] = true
				}
			} else if rank := passRank(minimum); rank > 0 && rank <= passRank(observed.Plan.Tier) {
				s.Catalog[id] = true
			}
		}
		s.CatalogAt, s.LastOKAt = now, now
		s.CatalogError, s.LastError, s.AuthFailed, s.Failures = "", "", false, 0
	} else {
		if err == nil {
			err = errors.New("未确认有效付费套餐或目录过期")
		}
		s.CatalogError, s.LastError, s.LastErrorAt = err.Error(), err.Error(), now
		s.Failures++
	}
	interval := time.Duration(cfg.TokenHarbor.AccountInterval()) * time.Second
	if interval == 0 {
		interval = 24 * time.Hour
	}
	s.NextAt = now.Add(interval)
	if err != nil {
		s.NextAt = now.Add(backoff(s.Failures))
	}
	if err == nil && observed.Usage.Error != "" {
		err = errors.New(observed.Usage.Error)
		s.LastError, s.LastErrorAt = err.Error(), now
		s.Failures++
		s.NextAt = now.Add(backoff(s.Failures))
	}
	observed.Models = nil // transient public mapping; only the validated intersection persists
	p.store(a.ID, s)      // rechecks source under the poller mutex
	return err
}
