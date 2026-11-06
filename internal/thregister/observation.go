package thregister

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"account-pool-balancer/internal/egressid"
)

// PassUsage is a read-only observation of the account's own seven-day window.
// It is neither a wallet balance nor consent to use free models.
type PassUsage struct {
	UsedPercent    *float64  `json:"usedPercent,omitempty"`
	Activated      bool      `json:"activated"`
	Exhausted      bool      `json:"exhausted"`
	OverageBilling *bool     `json:"overageBilling,omitempty"`
	ResetsAt       time.Time `json:"resetsAt,omitzero"`
	CheckedAt      time.Time `json:"checkedAt,omitzero"`
	Error          string    `json:"error,omitempty"`
}

type Observation struct {
	Subscription Subscription      `json:"subscription"`
	Models       map[string]string `json:"-"` // exact native ID -> minimum Pass
	Usage        PassUsage         `json:"usage"`
}

func flightRecords(body []byte) []any {
	var flight strings.Builder
	for _, match := range flightChunk.FindAllSubmatch(body, -1) {
		var chunk []json.RawMessage
		if json.Unmarshal(match[1], &chunk) != nil || len(chunk) != 2 || string(chunk[0]) != "1" {
			continue
		}
		var text string
		if json.Unmarshal(chunk[1], &text) == nil {
			flight.WriteString(text)
		}
	}
	var records []any
	for _, line := range strings.Split(flight.String(), "\n") {
		_, raw, ok := strings.Cut(line, ":")
		var node any
		if ok && json.Unmarshal([]byte(raw), &node) == nil {
			records = append(records, node)
		}
	}
	return records
}

// ParsePassModels reads the site's already-computed coverage, not price guesses
// or the unrelated low/mid/high API tier. Ambiguous structures fail closed.
func ParsePassModels(body []byte) (map[string]string, error) {
	var candidates []map[string]any
	var walk func(any, int)
	walk = func(v any, depth int) {
		if depth > 128 {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			if models, ok := x["models"].(map[string]any); ok && x["costs"] != nil {
				candidates = append(candidates, models)
			}
			for _, c := range x {
				walk(c, depth+1)
			}
		case []any:
			for _, c := range x {
				walk(c, depth+1)
			}
		}
	}
	for _, node := range flightRecords(body) {
		walk(node, 0)
	}
	if len(candidates) != 1 || len(candidates[0]) == 0 || len(candidates[0]) > 10000 {
		return nil, errors.New("官网套餐模型映射未知或结构变化")
	}
	out := make(map[string]string, len(candidates[0]))
	for id, value := range candidates[0] {
		m, ok := value.(map[string]any)
		tier, _ := m["tier"].(string)
		if !ok || id == "" || len(id) > 512 || strings.TrimSpace(id) != id || strings.ContainsAny(id, "\r\n\t") || (tier != "free" && tier != "agent" && tier != "office" && tier != "frontier") {
			return nil, errors.New("官网套餐模型映射无效")
		}
		out[id] = tier
	}
	return out, nil
}

func (f *Flow) readAccount(ctx context.Context, path, accept string) ([]byte, error) {
	if len(f.Session()) == 0 {
		return nil, errors.New("没有已保存会话，无法读取账号套餐/周用量")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.cfg.base()+path, nil)
	if err != nil {
		return nil, errors.New("账号元数据地址无效")
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Cache-Control", "no-cache, no-store")
	// A page read is a navigation, an API read is an XHR: Chrome sends a
	// different Sec-Fetch set for each, so the kind follows the path.
	if strings.HasPrefix(path, "/api/") {
		f.applyIdentityKind(req, egressid.SameOriginXHR)
	} else {
		f.applyIdentityKind(req, egressid.Navigate)
	}
	client := *f.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("账号元数据网络失败；未重试或切换出口")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("账号元数据返回 HTTP %d；请检查会话及固定代理", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(b) > maxBody {
		return nil, errors.New("账号元数据不完整或过大")
	}
	return b, nil
}

// Observe uses the same saved session and egress for both independent reads.
// No login, privacy POST, billing mutation or inference takes place.
func (f *Flow) Observe(ctx context.Context) (*Observation, error) {
	body, err := f.readAccount(ctx, "/pricing", "text/html")
	if err != nil {
		return nil, err
	}
	plan, err := parseSubscription(body)
	if err != nil {
		return nil, err
	}
	plan.CheckedAt = time.Now()
	models, err := ParsePassModels(body)
	if err != nil {
		return nil, err
	}
	out := &Observation{Subscription: plan, Models: models}
	out.Usage.CheckedAt = time.Now()
	body, err = f.readAccount(ctx, "/api/me/free-tier", "application/json")
	if err == nil {
		out.Usage, err = parsePassUsage(body, plan.Name)
	}
	if err != nil {
		out.Usage.Error = err.Error()
	}
	return out, nil
}

func parsePassUsage(body []byte, planName string) (PassUsage, error) {
	out := PassUsage{CheckedAt: time.Now()}
	var doc struct {
		OK             *bool    `json:"ok"`
		OnPlan         *bool    `json:"on_plan"`
		Activated      *bool    `json:"activated"`
		PlanUsed       *float64 `json:"plan_used_pct"`
		PlanExhausted  *bool    `json:"plan_exhausted"`
		OverageBilling *bool    `json:"overage_billing"`
		PeriodEnd      string   `json:"period_ends_at"`
		ResetAt        string   `json:"reset_at"`
		Plan           struct {
			Name       string
			WindowDays int `json:"window_days"`
		}
	}
	if json.Unmarshal(body, &doc) != nil || doc.OK == nil || !*doc.OK || doc.OnPlan == nil || !*doc.OnPlan || doc.Activated == nil || doc.PlanUsed == nil || doc.PlanExhausted == nil || doc.OverageBilling == nil || doc.Plan.Name != planName || doc.Plan.WindowDays != 7 {
		return out, errors.New("付费套餐周用量未知、套餐不匹配或结构变化")
	}
	p := *doc.PlanUsed
	if math.IsNaN(p) || math.IsInf(p, 0) || p < 0 || p > 100 || (!*doc.Activated && p != 0) || (*doc.PlanExhausted && p < 100) {
		return out, errors.New("付费套餐周百分比无效")
	}
	out.OverageBilling = doc.OverageBilling
	out.UsedPercent, out.Activated, out.Exhausted = doc.PlanUsed, *doc.Activated, *doc.PlanExhausted || p >= 100
	reset := doc.PeriodEnd
	if reset == "" {
		reset = doc.ResetAt
	}
	if reset != "" {
		var err error
		out.ResetsAt, err = time.Parse(time.RFC3339Nano, reset)
		if err != nil {
			return PassUsage{CheckedAt: out.CheckedAt}, errors.New("周重置时间无效")
		}
	}
	if out.Activated && out.ResetsAt.IsZero() {
		return PassUsage{CheckedAt: out.CheckedAt}, errors.New("已激活周窗口缺少重置时间")
	}
	return out, nil
}
