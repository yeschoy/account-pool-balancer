package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"account-pool-balancer/internal/config"
)

type X50State struct {
	BalanceUSD *float64  `json:"balanceUsd"`
	Tier       string    `json:"tier,omitempty"`
	At         time.Time `json:"at,omitzero"`
	Error      string    `json:"error,omitempty"`
}

func (s *Snapshot) FreshX50(now time.Time, stale time.Duration) bool {
	return s != nil && s.X50 != nil && s.X50.BalanceUSD != nil && s.X50.Error == "" && !s.X50.At.IsZero() && now.Sub(s.X50.At) < stale
}

var x50Decimal = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

// Missing/null/objects/bools, non-finite and malformed decimal values are not 0.
func ParseX50Billing(raw []byte) (*X50State, error) {
	var b struct {
		Balance json.RawMessage `json:"balance_usd"`
		Tier    string          `json:"tier"`
	}
	if e := json.Unmarshal(raw, &b); e != nil {
		return nil, errors.New("50X billing response is not valid JSON")
	}
	v := strings.TrimSpace(string(b.Balance))
	if strings.HasPrefix(v, "\"") {
		if e := json.Unmarshal(b.Balance, &v); e != nil {
			return nil, errors.New("invalid balance_usd")
		}
	}
	if len(v) > 128 || !x50Decimal.MatchString(v) {
		return nil, errors.New("50X billing missing or invalid balance_usd")
	}
	n, e := strconv.ParseFloat(v, 64)
	if e != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return nil, errors.New("50X billing balance_usd must be finite")
	}
	if len(b.Tier) > 100 {
		return nil, errors.New("50X billing tier too long")
	}
	return &X50State{BalanceUSD: &n, Tier: b.Tier, At: time.Now()}, nil
}
func (p *Poller) fetchX50(ctx context.Context, cfg *config.Config, a config.Account, force bool) error {
	source := ResponseBindingSource(cfg, a)
	cur := p.Cfg()
	n, ok := cur.Account(a.ID)
	if !ok || n.Provider != config.ProviderX50 || ResponseBindingSource(cur, n) != source {
		return errors.New("50X source changed")
	}
	s := p.Get(a.ID)
	if s == nil || s.CatalogSource != source {
		s = &Snapshot{CatalogSource: source}
	}
	if s.X50 == nil {
		s.X50 = &X50State{}
	}
	hc := *p.HTTPFor(a.ProxyURL)
	if p.X50HTTPFor != nil {
		hc = *p.X50HTTPFor(cfg, a.ProxyURL)
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	now := time.Now()
	every := time.Duration(cfg.X50.ModelSyncSec) * time.Second
	if force || s.CatalogAt.IsZero() || every > 0 && now.Sub(s.CatalogAt) >= every {
		cat, e := FetchCatalogFull(ctx, &hc, cfg.RuntimeBase(a), a.RuntimeKey, a.ID)
		if e != nil {
			s.CatalogError = "50X model catalog read failed"
		} else {
			for id, m := range cat.Models {
				m.Base = ""
				cat.Models[id] = m
			}
			applyCatalog(s, cat, time.Now())
		}
	}
	var readErr error
	if a.ControlKey == "" {
		readErr = errors.New("未绑定50X管理会话Token；余额未知，推理Key不能替代管理Token")
	} else {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, config.X50Origin(cfg.X50.BaseURL)+"/api/billing", nil)
		if e == nil {
			req.Header.Set("Authorization", "Token "+a.ControlKey)
			req.Header.Set("Accept", "application/json")
			resp, err := hc.Do(req)
			if err != nil {
				readErr = errors.New("50X billing network error (details omitted)")
			} else {
				raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
				resp.Body.Close()
				switch {
				case resp.StatusCode != 200:
					readErr = fmt.Errorf("50X billing HTTP %d", resp.StatusCode)
				case err != nil || len(raw) > 1<<20:
					readErr = errors.New("50X billing response too large or incomplete")
				default:
					saved, err := ParseX50Billing(raw)
					if err != nil {
						readErr = err
					} else {
						s.X50 = saved
					}
				}
			}
		} else {
			readErr = errors.New("invalid 50X billing endpoint")
		}
	}
	now = time.Now()
	if readErr != nil {
		s.X50.Error = readErr.Error()
		s.LastError = readErr.Error()
		s.LastErrorAt = now
		s.Failures++
	} else {
		s.LastOKAt = now
		s.LastError = ""
		s.Failures = 0
	}
	s.NextAt = now.Add(time.Duration(cfg.X50.EffectiveQuota().IntervalSec) * time.Second)
	// store checks CatalogSource under the publishing lock, including session.
	p.store(a.ID, s)
	return readErr
}
