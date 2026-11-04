package server

import (
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
	"account-pool-balancer/internal/pool"
)

type goQuotaAccount struct {
	AccountID string         `json:"accountId"`
	Name      string         `json:"name"`
	Fresh     bool           `json:"fresh"`
	UsageAt   time.Time      `json:"usageAt,omitzero"`
	Usage     opencode.Usage `json:"usage,omitempty"`
}
type goOverview struct {
	InFlight          int                 `json:"inFlight"`
	LimitedAccounts   int                 `json:"limitedAccounts"`
	UnlimitedAccounts int                 `json:"unlimitedAccounts"`
	Capacity          int                 `json:"capacity"`
	QuotaFresh        int                 `json:"quotaFresh"`
	QuotaUnknown      int                 `json:"quotaUnknown"`
	QuotaExhausted    int                 `json:"quotaExhausted"`
	PolicyPaused      int                 `json:"policyPaused"`
	Egress            map[string]int      `json:"egress"`
	Bindings          pool.BindingSummary `json:"bindings"`
	Quotas            []goQuotaAccount    `json:"quotas"`
}

func (s *Server) goOverview(cfg *config.Config, views []accountView) *goOverview {
	out := &goOverview{Egress: map[string]int{}, Quotas: []goQuotaAccount{}}
	ids := map[string]bool{}
	for _, v := range views {
		if v.Provider != config.ProviderOpenCodeGo {
			continue
		}
		ids[v.ID] = true
		out.InFlight += v.Runtime.Active
		limit := v.MaxConcurrent
		if st := v.Runtime.State; st != nil && st.LearnedConcurrency > 0 && (limit == 0 || st.LearnedConcurrency < limit) {
			limit = st.LearnedConcurrency
		}
		if v.Enabled {
			if limit == 0 {
				out.UnlimitedAccounts++
			} else {
				out.LimitedAccounts++
				out.Capacity += limit
			}
		}
		if v.Egress != nil {
			out.Egress[v.Egress.Status]++
		}
		if st := v.Runtime.State; st != nil && st.RuleID == "opencode-go-policy-refusal" && time.Now().Before(st.CooldownUntil) {
			out.PolicyPaused++
		}
		q := goQuotaAccount{AccountID: v.ID, Name: v.Name, Fresh: v.QuotaFresh}
		if v.Quota != nil && v.Quota.OpenCode != nil {
			q.UsageAt, q.Usage = v.Quota.OpenCode.UsageAt, v.Quota.OpenCode.Usage
			if v.Quota.OpenCode.Exhausted(time.Now(), time.Duration(cfg.Quota.StaleSec)*time.Second) {
				out.QuotaExhausted++
			}
		}
		if q.Fresh {
			out.QuotaFresh++
		} else {
			out.QuotaUnknown++
		}
		out.Quotas = append(out.Quotas, q)
	}
	out.Bindings = s.pool.BindingSummaryFor(ids)
	return out
}
