package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
	"account-pool-balancer/internal/thregister"
	"account-pool-balancer/internal/thsignupstore"
)

func (s *Server) fetchTHObservation(ctx context.Context, cfg *config.Config, a config.Account) (*quota.TokenHarborState, error) {
	store, err := s.thStore()
	if err != nil {
		return nil, errors.New("原注册资料不可读，无法检测套餐")
	}
	row, err := store.ForAccount(a.ID, a.RuntimeKey)
	if err != nil {
		return nil, errors.New("无唯一原注册会话或Key来源不匹配，套餐未知")
	}
	if !row.Verified {
		return nil, errors.New("原注册账号邮箱未验证")
	}
	// Registration supplies the uniquely linked session, not the pool egress.
	// After admission each side owns its route; observe permissions through the
	// same account proxy as inference, with the usual source/late-write guards.
	flow, err := thregister.New(thregister.Config{BaseURL: thBaseURL(cfg.TokenHarbor.BaseURL), ProxyURL: a.ProxyURL, UserAgent: thRowUserAgent(row), RequestTimeout: 40 * time.Second})
	if err != nil {
		return nil, errors.New("账号固定代理无效")
	}
	defer flow.Close()
	flow.SeedCookies(thStoredResult(row).Cookies)
	observed, observeErr := flow.Observe(ctx)
	latest, err := store.ForAccount(a.ID, a.RuntimeKey)
	if err != nil || latest.UpdatedAt != row.UpdatedAt {
		return nil, errors.New("检测期间注册会话或入池状态变化，丢弃结果")
	}
	fresh := flow.Session()
	if thSessionChanged(thStoredResult(row).Cookies, fresh) {
		_, err = store.Update(row.ID, func(rw *thsignupstore.Row) error {
			current := s.cfg.Get()
			after, ok := current.Account(a.ID)
			if rw.UpdatedAt != row.UpdatedAt || !ok || quota.ResponseBindingSource(current, after) != quota.ResponseBindingSource(cfg, a) {
				return errors.New("会话更新期间账号来源变化")
			}
			result := thStoredResult(*rw)
			result.Cookies = fresh
			thSaveResult(rw, result)
			return nil
		})
		if err != nil {
			return nil, errors.New("官网已更新会话，但保存失败；暂不放行")
		}
	}
	if observeErr != nil {
		return nil, observeErr
	}
	return &quota.TokenHarborState{Owner: s.fingerprint("tokenharbor-owner", "row", row.ID), Plan: observed.Subscription, Usage: observed.Usage, Models: observed.Models, FreeModels: row.FreeModels}, nil
}

// Persist renewed auth cookies only when values change, not an encrypted full
// registration-file rewrite for every periodic observation.
func thSessionChanged(before, after []thregister.NamedCookie) bool {
	extract := func(cookies []thregister.NamedCookie) map[string]string {
		out := map[string]string{}
		for _, c := range cookies {
			name := strings.ToLower(c.Name)
			if strings.Contains(name, "session_data") || strings.Contains(name, "session-data") {
				continue
			} // renewable Better Auth cache, not the durable auth token
			if strings.Contains(name, "session") || strings.Contains(name, "auth") {
				out[c.Name] = c.Value
			}
		}
		return out
	}
	a, b := extract(before), extract(after)
	if len(a) != len(b) {
		return true
	}
	for k, v := range a {
		if b[k] != v {
			return true
		}
	}
	return false
}

// This is a local union of validated snapshots, never a network fan-out.
func (s *Server) thModels(cfg *config.Config, group string) []byte {
	now := time.Now()
	ids := map[string]bool{}
	for _, a := range cfg.Accounts {
		if a.Provider != config.ProviderTokenHarbor || a.Group != group || !a.Enabled {
			continue
		}
		q := s.quota.View(a.ID)
		if q == nil || q.TokenHarbor == nil || q.TokenHarbor.Source != quota.ResponseBindingSource(cfg, a) || !q.TokenHarbor.PlanFresh(now) {
			continue
		}
		for id, enabled := range q.Catalog {
			if enabled {
				ids[id] = true
			}
		}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	data := make([]map[string]string, 0, len(ordered))
	for _, id := range ordered {
		data = append(data, map[string]string{"id": id, "object": "model", "owned_by": "tokenharbor"})
	}
	b, _ := json.Marshal(map[string]any{"object": "list", "data": data})
	return b
}

func (s *Server) apiTHOverview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	cfg, now := s.cfg.Get(), time.Now()
	effective := cfg.ForProvider(config.ProviderTokenHarbor)
	views := s.pool.Views(effective)
	routing := effective.Routing
	seen := map[string]bool{}
	agent, unknown, other, measured := 0, 0, 0, 0
	total, available := 0.0, 0.0
	var oldest *time.Time
	for _, a := range cfg.Accounts {
		if a.Provider != config.ProviderTokenHarbor {
			continue
		}
		q := s.quota.View(a.ID)
		if q == nil || q.TokenHarbor == nil || q.TokenHarbor.Source != quota.ResponseBindingSource(cfg, a) || !q.TokenHarbor.PlanFresh(now) {
			unknown++
			continue
		}
		t := q.TokenHarbor
		if t.Owner == "" || seen[t.Owner] {
			continue
		}
		seen[t.Owner] = true
		if t.Plan.Tier != "plus" {
			other++
			continue
		}
		agent++
		if oldest == nil || t.CheckedAt.Before(*oldest) {
			at := t.CheckedAt
			oldest = &at
		}
		total += 20
		if t.UsageFresh(now) {
			measured++
			v := views[a.ID]
			healthOK := (!routing.HealthFilter && routing.Strategy != "least-requests-1h") || v.Health == nil || *v.Health >= routing.MinimumHealth
			if a.Enabled && v.Status == "ok" && healthOK {
				available += 5 * (1 - *t.Usage.UsedPercent/100)
			}
		}
	}
	var remaining *float64
	if measured > 0 {
		remaining = &available
	}
	var nominal *float64
	if len(seen) > 0 {
		nominal = &total
	}
	writeJSON(w, 200, map[string]any{"agentAccounts": agent, "unknownAccounts": unknown, "otherPlanAccounts": other, "measuredWeeklyAccounts": measured, "totalEquivalentUsd": nominal, "availableWeeklyEquivalentUsd": remaining, "generatedAt": now, "oldestAgentObservationAt": oldest, "basis": "Agent Pass按用户指定2×等效展示口径：20/周期、5/周；不是钱包余额。总额含已禁用的有效Agent；当前可调度周额排除未知/过期/耗尽/禁用/隔离及官网钱包续费开启的账号，独立按每账号重置时间。"})
}
