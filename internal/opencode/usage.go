package opencode

import (
	"context"
	"errors"
	"math"
	"net/http"
	"time"
)

type Window struct {
	Status           string    `json:"status"`
	UsedPercent      float64   `json:"usedPercent"`
	RemainingPercent float64   `json:"remainingPercent"`
	ResetsAt         time.Time `json:"resetsAt"`
}

// Usage is subscription-wide, not a per-key dollar balance. Never sum it.
type Usage map[string]Window

func FetchUsage(ctx context.Context, hc *http.Client, base, key string) (Usage, error) {
	var raw struct {
		Usage map[string]struct {
			Status   string    `json:"status"`
			Percent  *float64  `json:"percent"`
			ResetsAt time.Time `json:"resetsAt"`
		} `json:"usage"`
	}
	if err := ReadJSON(ctx, hc, base+"/usage", key, &raw); err != nil {
		return nil, err
	}
	out := Usage{}
	for _, name := range []string{"rolling", "weekly", "monthly"} {
		w, ok := raw.Usage[name]
		if !ok || w.Percent == nil || math.IsNaN(*w.Percent) || math.IsInf(*w.Percent, 0) || *w.Percent < 0 || *w.Percent > 100 || w.ResetsAt.IsZero() || (w.Status != "ok" && w.Status != "rate-limited") {
			return nil, errors.New("incomplete OpenCode Go usage windows")
		}
		out[name] = Window{Status: w.Status, UsedPercent: *w.Percent, RemainingPercent: 100 - *w.Percent, ResetsAt: w.ResetsAt}
	}
	return out, nil
}

type State struct {
	Source          string           `json:"source"` // hash of credentials/base/fixed proxy, never a credential
	Models          map[string]Model `json:"models,omitempty"`
	ModelsAt        time.Time        `json:"modelsAt,omitzero"`
	CapabilityError string           `json:"capabilityError,omitempty"`
	Usage           Usage            `json:"usage,omitempty"`
	UsageAt         time.Time        `json:"usageAt,omitzero"`
	CheckedAt       time.Time        `json:"checkedAt,omitzero"`
	UsageError      string           `json:"usageError,omitempty"`
}

func (s *State) Clone() *State {
	if s == nil {
		return nil
	}
	c := *s
	c.Models = map[string]Model{}
	c.Usage = Usage{}
	for k, m := range s.Models {
		m.InputModalities = append([]string(nil), m.InputModalities...)
		m.OutputModalities = append([]string(nil), m.OutputModalities...)
		c.Models[k] = m
	}
	for k, w := range s.Usage {
		c.Usage[k] = w
	}
	return &c
}
func (s *State) Fresh(now time.Time, ttl time.Duration) bool {
	return s != nil && s.UsageError == "" && !s.UsageAt.IsZero() && now.Sub(s.UsageAt) < ttl
}
func (s *State) Exhausted(now time.Time, ttl time.Duration) bool {
	if !s.Fresh(now, ttl) {
		return false
	}
	for _, w := range s.Usage {
		if now.Before(w.ResetsAt) && (w.Status == "rate-limited" || w.UsedPercent >= 100) {
			return true
		}
	}
	return false
}
