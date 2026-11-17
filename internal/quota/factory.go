package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"account-pool-balancer/internal/config"
)

const FactoryQuotaReason = "Factory fk- Key可做身份验证；billing/limits是否开放取决于凭据权限。401/403表示额度不可读，不代表推理Key失效或余额为0。Standard/Core为共享百分比窗口，多Key不累加；不自动开启Extra usage。"

type FactoryWindow struct {
	Pool        string    `json:"pool"`
	Name        string    `json:"name"`
	UsedPercent float64   `json:"usedPercent"`
	ResetsAt    time.Time `json:"resetsAt,omitzero"`
	Active      bool      `json:"active"`
}

type FactoryState struct {
	Source            string          `json:"source"`
	UserID            string          `json:"userId,omitempty"`
	OrgID             string          `json:"orgId,omitempty"`
	Region            string          `json:"region,omitempty"`
	IdentityAt        time.Time       `json:"identityAt,omitzero"`
	CheckedAt         time.Time       `json:"checkedAt,omitzero"`
	QuotaState        string          `json:"quotaState"`
	QuotaError        string          `json:"quotaError,omitempty"`
	QuotaAt           time.Time       `json:"quotaAt,omitzero"`
	Windows           []FactoryWindow `json:"windows"`
	ExtraBalanceCents *float64        `json:"extraUsageBalanceCents,omitempty"`
	ExtraAllowed      *bool           `json:"extraUsageAllowed,omitempty"`
	QuotaDenied       bool            `json:"quotaDenied,omitempty"`
}

// FactoryView returns the already immutable state; the API never mutates it.
func (s *Snapshot) FactoryView() *FactoryState {
	if s == nil {
		return nil
	}
	return s.Factory
}

func (f *FactoryState) clone() *FactoryState {
	if f == nil {
		return nil
	}
	c := *f
	c.Windows = append([]FactoryWindow(nil), f.Windows...)
	if f.ExtraBalanceCents != nil {
		v := *f.ExtraBalanceCents
		c.ExtraBalanceCents = &v
	}
	if f.ExtraAllowed != nil {
		v := *f.ExtraAllowed
		c.ExtraAllowed = &v
	}
	return &c
}
func (f *FactoryState) Fresh(now time.Time, stale time.Duration) bool {
	return f != nil && f.QuotaState == "ok" && !f.QuotaAt.IsZero() && now.Sub(f.QuotaAt) < stale
}

func (f *FactoryState) Exhausted(cfg *config.Config, model string, now time.Time) bool {
	if !f.Fresh(now, time.Duration(cfg.Factory.EffectiveQuota().StaleSec)*time.Second) {
		return false
	}
	pool := "standard"
	if !cfg.Factory.StandardOnly && cfg.Factory.CoreModel(model) {
		pool = "core"
	}
	if !cfg.Factory.StandardOnly && f.ExtraAllowed != nil && *f.ExtraAllowed && f.ExtraBalanceCents != nil && *f.ExtraBalanceCents > 0 {
		return false
	}
	for _, w := range f.Windows {
		if w.Pool == pool && w.Active && now.Before(w.ResetsAt) && w.UsedPercent >= 100 {
			return true
		}
	}
	return false
}

// The credential is sent only to the configured origin; upstream-provided
// region/private-host values are diagnostic metadata, never redirect targets.
func factoryRead(ctx context.Context, hc *http.Client, cfg *config.Config, a config.Account, path string) ([]byte, int, error) {
	client := *hc
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.RuntimeBase(a)+path, nil)
	if err != nil {
		return nil, 0, errors.New("Factory invalid request")
	}
	req.Header.Set("Authorization", "Bearer "+a.RuntimeKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "account-pool-balancer/factory-gateway")
	if path == "/api/cli/whoami" {
		req.Header.Set("X-Factory-Whoami-Extended", "true")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, errors.New("Factory network request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, resp.StatusCode, fmt.Errorf("Factory %s HTTP %d", path, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return nil, 200, errors.New("Factory response incomplete or too large")
	}
	return b, 200, nil
}

func factoryEnd(raw json.RawMessage) (time.Time, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if t, e := time.Parse(time.RFC3339Nano, s); e == nil {
			return t, nil
		}
		n, e := strconv.ParseInt(s, 10, 64)
		if e == nil && n > 0 {
			return time.UnixMilli(n), nil
		}
	} else {
		var n int64
		if json.Unmarshal(raw, &n) == nil && n > 0 {
			return time.UnixMilli(n), nil
		}
	}
	return time.Time{}, errors.New("Factory invalid windowEnd")
}

func parseFactoryLimits(b []byte) (*FactoryState, error) {
	type window struct {
		Used *float64        `json:"usedPercent"`
		End  json.RawMessage `json:"windowEnd"`
	}
	type windows struct {
		FiveHour *window `json:"fiveHour"`
		Weekly   *window `json:"weekly"`
		Monthly  *window `json:"monthly"`
	}
	var doc struct {
		Limits *struct {
			Standard *windows `json:"standard"`
			Core     *windows `json:"core"`
		} `json:"limits"`
		Extra   *float64 `json:"extraUsageBalanceCents"`
		Allowed *bool    `json:"extraUsageAllowed"`
	}
	if json.Unmarshal(b, &doc) != nil || doc.Limits == nil || doc.Limits.Standard == nil {
		return nil, errors.New("Factory response has no valid Standard limits")
	}
	out := &FactoryState{Windows: []FactoryWindow{}, ExtraBalanceCents: doc.Extra, ExtraAllowed: doc.Allowed}
	if doc.Extra != nil && (math.IsNaN(*doc.Extra) || math.IsInf(*doc.Extra, 0) || *doc.Extra < 0) {
		return nil, errors.New("Factory invalid Extra usage balance")
	}
	for _, p := range []struct {
		name string
		w    *windows
	}{{"standard", doc.Limits.Standard}, {"core", doc.Limits.Core}} {
		if p.w == nil {
			continue
		}
		for _, v := range []struct {
			name string
			w    *window
		}{{"5h", p.w.FiveHour}, {"weekly", p.w.Weekly}, {"monthly", p.w.Monthly}} {
			if v.w == nil {
				continue
			}
			if v.w.Used == nil || math.IsNaN(*v.w.Used) || math.IsInf(*v.w.Used, 0) || *v.w.Used < 0 {
				return nil, errors.New("Factory missing or invalid usedPercent")
			}
			end, e := factoryEnd(v.w.End)
			if e != nil {
				return nil, e
			}
			out.Windows = append(out.Windows, FactoryWindow{Pool: p.name, Name: v.name, UsedPercent: *v.w.Used, ResetsAt: end, Active: !end.IsZero()})
		}
	}
	if len(out.Windows) == 0 {
		return nil, errors.New("Factory reported no usage windows")
	}
	return out, nil
}

func (p *Poller) fetchFactory(ctx context.Context, cfg *config.Config, a config.Account, force bool) error {
	source := ResponseBindingSource(cfg, a)
	current := p.Cfg()
	next, ok := current.Account(a.ID)
	if !ok || next.Provider != config.ProviderFactory || source != ResponseBindingSource(current, next) {
		return errors.New("Factory source changed")
	}
	p.mu.Lock()
	s := p.snaps[a.ID].clone()
	p.mu.Unlock()
	if s == nil || s.CatalogSource != source {
		s = &Snapshot{CatalogSource: source}
	}
	f := s.Factory.clone()
	if f == nil {
		f = &FactoryState{Source: source, QuotaState: "unknown", Windows: []FactoryWindow{}}
	}
	s.Factory = f
	now := time.Now()
	f.CheckedAt = now
	hc := p.HTTPFor(a.ProxyURL)
	if p.FactoryHTTPFor != nil {
		hc = p.FactoryHTTPFor(cfg, a.ProxyURL)
	}
	b, st, err := factoryRead(ctx, hc, cfg, a, "/api/cli/whoami")
	if err == nil {
		var who struct {
			UserID string `json:"userId"`
			OrgID  string `json:"orgId"`
			Region string `json:"region"`
		}
		if json.Unmarshal(b, &who) != nil || who.UserID == "" || who.OrgID == "" {
			err = errors.New("Factory whoami missing identity")
		} else {
			if f.UserID != "" && (f.UserID != who.UserID || f.OrgID != who.OrgID) {
				// The same credential can be re-scoped upstream. Do not show
				// the old organization's limits under the new identity.
				f.Windows = nil
				f.ExtraBalanceCents = nil
				f.ExtraAllowed = nil
				f.QuotaAt = time.Time{}
				f.QuotaState = "unknown"
				f.QuotaError = ""
				f.QuotaDenied = false
			}
			f.UserID, f.OrgID, f.Region, f.IdentityAt = who.UserID, who.OrgID, who.Region, now
		}
	}
	if err != nil {
		f.QuotaState = "failed"
		f.QuotaError = "Factory identity refresh failed; quota freshness unknown"
		s.LastError, s.LastErrorAt = err.Error(), now
		s.AuthFailed = st == 401
		s.Failures++
		s.NextAt = now.Add(backoff(s.Failures))
		p.store(a.ID, s)
		return err
	}
	s.AuthFailed = false
	s.LastError = ""
	s.LastErrorAt = time.Time{}
	s.LastOKAt = now
	s.Failures = 0
	if !f.QuotaDenied || force {
		b, st, err = factoryRead(ctx, hc, cfg, a, "/api/billing/limits")
		if err == nil {
			var q *FactoryState
			q, err = parseFactoryLimits(b)
			if err == nil {
				f.Windows, f.ExtraBalanceCents, f.ExtraAllowed = q.Windows, q.ExtraBalanceCents, q.ExtraAllowed
				f.QuotaAt = now
				f.QuotaState = "ok"
				f.QuotaError = ""
				f.QuotaDenied = false
			}
		}
		if err != nil {
			f.QuotaState = "failed"
			f.QuotaError = err.Error()
			if st == 401 || st == 403 {
				f.QuotaState = "unsupported-credential"
				f.QuotaDenied = true
			}
		}
	}
	s.NextAt = now.Add(time.Duration(cfg.Factory.EffectiveQuota().IntervalSec) * time.Second)
	p.store(a.ID, s)
	return nil // successful identity with unreadable quota is not invalid auth
}
