package thregister

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Subscription is a credential-free observation, not proof that a checkout
// link was paid. Current means the site's currently effective plan; the page
// does not expose an expiry date, so none is invented here.
type Subscription struct {
	Status    string    `json:"status,omitempty"` // active, free, unknown
	Tier      string    `json:"tier,omitempty"`
	Name      string    `json:"name,omitempty"`
	Cycle     string    `json:"cycle,omitempty"`
	CheckedAt time.Time `json:"checkedAt,omitempty"`
	Error     string    `json:"error,omitempty"`
}

func (s Subscription) Active() bool {
	return s.Status == "active" && (s.Tier == "plus" || s.Tier == "pro" || s.Tier == "frontier")
}

// CheckSubscription only reads the authenticated pricing page. No billing
// mutation, balance spending, model inference or automatic redirect is used.
func (f *Flow) CheckSubscription(ctx context.Context) (Subscription, error) {
	out := Subscription{Status: "unknown", CheckedAt: time.Now()}
	fail := func(err error) (Subscription, error) { out.Error = err.Error(); return out, err }
	if len(f.Session()) == 0 {
		return fail(errors.New("没有已保存的账号会话，无法检测套餐"))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.cfg.base()+"/pricing", nil)
	if err != nil {
		return fail(errors.New("套餐检测地址无效"))
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Cache-Control", "no-cache, no-store")
	req.Header.Set("User-Agent", userAgent)
	client := *f.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return fail(errors.New("套餐检测网络失败；未确认订阅，不允许入池"))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fail(fmt.Errorf("套餐检测返回 HTTP %d；请检查会话及代理，不允许入池", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return fail(errors.New("套餐页面不完整或过大，无法确认订阅"))
	}
	plan, err := parseSubscription(body)
	if err != nil {
		return fail(err)
	}
	plan.CheckedAt = out.CheckedAt
	return plan, nil
}

var flightChunk = regexp.MustCompile(`self\.__next_f\.push\((\[[^<]*?\])\)</script>`)

type pricingPlan struct {
	Plan struct {
		Slug string `json:"slug"`
		Tier string `json:"tier"`
		Name string `json:"name"`
	} `json:"plan"`
	State string `json:"state"`
}

type pricingSubscription struct {
	SignedIn     *bool         `json:"signedIn"`
	Free         pricingPlan   `json:"free"`
	Plans        []pricingPlan `json:"plans"`
	CurrentCycle *string       `json:"currentCycle"`
}

func parseSubscription(body []byte) (Subscription, error) {
	unknown := errors.New("套餐页面结构已变化或状态不明确，无法确认订阅；请在官网核对")
	// Decode the streamed Flight strings, then parse JSON records. Never
	// search arbitrary HTML text for 'paid' or evaluate a server script.
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
	var candidates []pricingSubscription
	var walk func(any, int)
	walk = func(v any, depth int) {
		if depth > 128 {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			_, free := x["free"]
			_, plans := x["plans"]
			_, signedIn := x["signedIn"]
			_, cycle := x["currentCycle"]
			if free && plans && signedIn && cycle {
				raw, _ := json.Marshal(x)
				var p pricingSubscription
				if json.Unmarshal(raw, &p) == nil {
					candidates = append(candidates, p)
				}
			}
			for _, child := range x {
				walk(child, depth+1)
			}
		case []any:
			for _, child := range x {
				walk(child, depth+1)
			}
		}
	}
	for _, line := range strings.Split(flight.String(), "\n") {
		_, raw, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		var node any
		if json.Unmarshal([]byte(raw), &node) == nil {
			walk(node, 0)
		}
	}
	if len(candidates) != 1 {
		return Subscription{}, unknown
	}
	p := candidates[0]
	if p.SignedIn == nil || !*p.SignedIn {
		return Subscription{}, errors.New("账号会话已失效，请重新登录后检测套餐；不会重新注册")
	}
	if p.Free.Plan.Tier != "free" || p.Free.Plan.Slug != "free" || len(p.Plans) != 3 {
		return Subscription{}, unknown
	}
	all := append([]pricingPlan{p.Free}, p.Plans...)
	expected := map[string]string{"free": "free", "plus": "starter_pass_monthly", "pro": "pro_pass_monthly", "frontier": "frontier_pass_monthly"}
	names := map[string]string{"free": "Free", "plus": "Agent Pass", "pro": "Office Pass", "frontier": "Frontier Pass"}
	seen := map[string]bool{}
	var current []pricingPlan
	for _, plan := range all {
		tier := plan.Plan.Tier
		if expected[tier] == "" || expected[tier] != plan.Plan.Slug || names[tier] != plan.Plan.Name || seen[tier] {
			return Subscription{}, unknown
		}
		seen[tier] = true
		if plan.State == "current" {
			current = append(current, plan)
		}
	}
	if len(current) != 1 {
		return Subscription{}, unknown
	}
	c := current[0]
	out := Subscription{Status: "free", Tier: c.Plan.Tier, Name: c.Plan.Name}
	if c.Plan.Tier != "free" {
		if p.CurrentCycle == nil || (*p.CurrentCycle != "monthly" && *p.CurrentCycle != "yearly") {
			return Subscription{}, unknown
		}
		out.Status, out.Cycle = "active", *p.CurrentCycle
	}
	return out, nil
}
