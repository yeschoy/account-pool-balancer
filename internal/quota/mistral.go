package quota

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
)

// Mistral base_model_id describes a model's ancestry. In particular a
// fine-tuned model must never be silently replaced with its base model.
func FetchMistralCatalog(ctx context.Context, hc *http.Client, base, key, seed string) (*Catalog, error) {
	cat, err := FetchCatalogFull(ctx, opencode.NoRedirect(hc), base, key, seed)
	if err != nil {
		return nil, err
	}
	for id, m := range cat.Models {
		m.Base = ""
		cat.Models[id] = m
	}
	return cat, nil
}

// MistralState records only the budget this key spends, never both organization
// budgets and never identity details returned by whoami.
type MistralState struct {
	Source       string               `json:"source"`
	KeyType      string               `json:"keyType,omitempty"`
	Plan         string               `json:"plan,omitempty"`
	Budget       *MistralBudget       `json:"budget,omitempty"`
	Subscription *MistralSubscription `json:"subscription,omitempty"`
	At           time.Time            `json:"at,omitzero"`
	CheckedAt    time.Time            `json:"checkedAt,omitzero"`
	Error        string               `json:"error,omitempty"`
	Failures     int                  `json:"failures,omitempty"`
}

type MistralBudget struct {
	Total       float64   `json:"total"`
	UsedPercent float64   `json:"usedPercent"`
	Remaining   float64   `json:"remaining"` // estimate derived from the percentage
	Currency    string    `json:"currency"`
	ResetsAt    time.Time `json:"resetsAt"`
	PAYG        *bool     `json:"payg,omitempty"`
}

// Subscription describes the organization's billing plan, which can differ
// from whoami's access plan for this specific API key (e.g. Free versus EDU).
// Its optional read must not invalidate a successfully measured budget.
type MistralSubscription struct {
	Plan      string    `json:"plan,omitempty"`
	PAYG      *bool     `json:"payg,omitempty"`
	VibePAYG  *bool     `json:"vibePayg,omitempty"`
	At        time.Time `json:"at,omitzero"`
	CheckedAt time.Time `json:"checkedAt,omitzero"`
	Error     string    `json:"error,omitempty"`
}

func (m *MistralState) clone() *MistralState {
	if m == nil {
		return nil
	}
	c := *m
	if m.Subscription != nil {
		v := *m.Subscription
		c.Subscription = &v
		if v.PAYG != nil {
			b := *v.PAYG
			c.Subscription.PAYG = &b
		}
		if v.VibePAYG != nil {
			b := *v.VibePAYG
			c.Subscription.VibePAYG = &b
		}
	}
	if m.Budget != nil {
		b := *m.Budget
		c.Budget = &b
		if b.PAYG != nil {
			v := *b.PAYG
			b.PAYG = &v
		}
	}
	return &c
}

func (s *Snapshot) FreshMistral(now time.Time, stale time.Duration) bool {
	return s != nil && s.Mistral != nil && s.Mistral.Error == "" && s.Mistral.Budget != nil &&
		!s.Mistral.At.IsZero() && now.Sub(s.Mistral.At) < stale && now.Before(s.Mistral.Budget.ResetsAt)
}

func FetchMistralBudget(ctx context.Context, hc *http.Client, base, key, seed string) (*MistralState, error) {
	base = strings.TrimRight(base, "/")
	whoBase := strings.TrimSuffix(base, "/v1")
	if base == config.DefaultMistral().BaseURL {
		whoBase = "https://console.mistral.ai"
	}
	client := &Client{Base: whoBase, HTTP: opencode.NoRedirect(hc)}
	var who struct {
		Type string `json:"plan_type"`
		Plan string `json:"plan_name"`
	}
	if err := client.get(ctx, key, "/api/vibe/whoami", "", seed, &who); err != nil {
		return nil, fmt.Errorf("Mistral 账号类型读取失败: %w", err)
	}
	kind := strings.ToUpper(strings.TrimSpace(who.Type))
	if kind != "API" && kind != "CHAT" {
		return nil, errors.New("Mistral Key 类型未知，不能选择套餐预算")
	}
	type detail struct {
		Total    *float64  `json:"initial_budget"`
		Used     *float64  `json:"usage_percentage"`
		Currency string    `json:"currency"`
		Reset    time.Time `json:"reset_at"`
		PAYG     *bool     `json:"payg_enabled"`
	}
	var budgets struct {
		API  *detail `json:"api_budget"`
		Vibe *detail `json:"vibe_budget"`
	}
	client.Base = base
	if err := client.get(ctx, key, "/billing/budget", "", seed, &budgets); err != nil {
		return nil, fmt.Errorf("Mistral 套餐读取失败: %w", err)
	}
	b := budgets.API
	if kind == "CHAT" {
		b = budgets.Vibe
	}
	if b == nil || b.Total == nil || b.Used == nil || math.IsNaN(*b.Total) || math.IsInf(*b.Total, 0) ||
		math.IsNaN(*b.Used) || math.IsInf(*b.Used, 0) || *b.Total < 0 || *b.Used < 0 {
		return nil, errors.New("Mistral 套餐额度字段缺失或无效")
	}
	if len(b.Currency) != 3 || strings.IndexFunc(b.Currency, func(r rune) bool { return r < 'A' || r > 'Z' }) >= 0 {
		return nil, errors.New("Mistral 套餐币种未知")
	}
	if b.Reset.IsZero() || !b.Reset.After(time.Now()) {
		return nil, errors.New("Mistral 套餐重置时间未知或已过期")
	}
	return &MistralState{KeyType: kind, Plan: mistralPlanName(who.Plan), Subscription: fetchMistralSubscription(ctx, client, key, seed), Budget: &MistralBudget{
		Total: *b.Total, UsedPercent: *b.Used, Remaining: *b.Total * math.Max(0, 1-*b.Used/100),
		Currency: b.Currency, ResetsAt: b.Reset, PAYG: b.PAYG,
	}}, nil
}

func mistralPlanName(value string) string {
	value = strings.TrimSpace(value)
	if name := map[string]string{"FREE": "Free", "INDIVIDUAL": "Pro", "EDU": "Student", "TEAM": "Team", "ENTERPRISE": "Enterprise"}[strings.ToUpper(value)]; name != "" {
		return name
	}
	if len(value) <= 64 && strings.IndexFunc(value, func(r rune) bool { return r < 32 || r == 127 }) < 0 {
		return value
	}
	return ""
}

func fetchMistralSubscription(ctx context.Context, client *Client, key, seed string) *MistralSubscription {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	s := &MistralSubscription{CheckedAt: time.Now()}
	var reply struct {
		Plan     string `json:"plan_name"`
		PAYG     *bool  `json:"payg_enabled"`
		VibePAYG *bool  `json:"vibe_payg_enabled"`
	}
	if err := client.get(ctx, key, "/billing/subscription", "", seed, &reply); err != nil {
		s.Error = "Mistral 订阅信息读取失败: " + err.Error()
		return s
	}
	if s.Plan = mistralPlanName(reply.Plan); s.Plan == "" {
		s.Error = "Mistral 订阅名称缺失或无效"
		return s
	}
	s.PAYG, s.VibePAYG, s.At = reply.PAYG, reply.VibePAYG, time.Now()
	return s
}

func (p *Poller) fetchMistral(ctx context.Context, cfg *config.Config, a config.Account, force bool) error {
	cfg = cfg.ForProvider(config.ProviderMistral)
	catalogErr := p.fetchCatalogOnlyForce(ctx, cfg, a, force)
	s := p.Get(a.ID)
	source := ResponseBindingSource(cfg, a)
	if s == nil || s.CatalogSource != source {
		return errors.New("Mistral 账号来源已变化，请重新刷新")
	}
	now := time.Now()
	interval := time.Duration(cfg.Quota.IntervalSec) * time.Second
	schedule := func(next time.Time) time.Time {
		if catalogErr != nil {
			if retry := now.Add(backoff(max(s.Failures, 1))); retry.Before(next) {
				next = retry
			}
		} else if every := cfg.Mistral.ModelInterval(); every > 0 && !s.CatalogAt.IsZero() {
			if due := s.CatalogAt.Add(time.Duration(every) * time.Second); due.Before(next) {
				next = due
			}
		}
		return next
	}
	retryInterval := interval
	if s.Mistral != nil && s.Mistral.Error != "" {
		retryInterval = backoff(s.Mistral.Failures)
	}
	if !force && s.Mistral != nil && s.Mistral.Source == source && now.Sub(s.Mistral.CheckedAt) < retryInterval {
		s.NextAt = schedule(s.Mistral.CheckedAt.Add(retryInterval))
		p.store(a.ID, s)
		return catalogErr
	}
	m, err := FetchMistralBudget(ctx, p.HTTPFor(a.ProxyURL), cfg.RuntimeBase(a), a.RuntimeKey, a.ID)
	if err != nil {
		if s.Mistral == nil {
			s.Mistral = &MistralState{Source: source}
		}
		s.Mistral.CheckedAt, s.Mistral.Error = now, err.Error()
		s.LastError, s.LastErrorAt = err.Error(), now
		s.Mistral.Failures++
		s.Failures = max(s.Failures, s.Mistral.Failures)
		s.NextAt = schedule(now.Add(backoff(s.Mistral.Failures)))
	} else {
		if m.Subscription != nil && m.Subscription.Error != "" && s.Mistral != nil && s.Mistral.Subscription != nil {
			previous := s.Mistral.clone().Subscription
			previous.CheckedAt, previous.Error = m.Subscription.CheckedAt, m.Subscription.Error
			m.Subscription = previous
		}
		m.Source, m.At, m.CheckedAt = source, now, now
		s.Mistral = m
		s.NextAt = schedule(now.Add(interval))
		if catalogErr == nil {
			s.LastError, s.Failures, s.LastOKAt = "", 0, now
		}
	}
	// Billing permission failures do not invalidate an otherwise working API key.
	p.store(a.ID, s)
	return errors.Join(catalogErr, err)
}
