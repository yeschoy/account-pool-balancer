package server

import (
	"sort"
	"time"

	"account-pool-balancer/internal/config"
)

// mistralOverview summarizes the pool's own budget reads for the overview page.
//
// A Mistral budget belongs to an organization, not to a key: every key in one
// workspace sees the same api_budget/vibe_budget, so summing per-account
// remaining amounts would count one organization many times. Keys are grouped
// by the workspace they reported and every number is a per-workspace total.
// Only values this service already measured go in; a key whose read failed,
// expired, or whose budget reset already passed contributes nothing and is
// counted as unknown instead of as zero.
//
// Every figure here is api_budget: Mistral charges this pool's /v1/chat/
// completions traffic to api_budget for Vibe and Studio keys alike, so
// vibe_budget is the official products' allowance and never enters a total,
// a percentage or a ranking. It is mirrored as display-only per workspace.
type mistralOverview struct {
	// Accounts is the pool's account count these figures cover.
	Accounts int `json:"accounts"`
	// FreshAccounts / UnknownAccounts split accounts by whether their last
	// budget read is still usable right now.
	FreshAccounts   int `json:"freshAccounts"`
	UnknownAccounts int `json:"unknownAccounts"`
	// Workspaces lists the deduplicated organizations that reported a budget.
	Workspaces []mistralWorkspaceView `json:"workspaces"`
	// Groups are the per-currency sums over those workspaces. Different
	// currencies are never added together.
	Groups []mistralCurrencyTotal `json:"groups"`
	// UnknownRule is the reason keys without a usable read stay out of the
	// totals; the page shows it verbatim so no number looks complete.
	UnknownRule string `json:"unknownRule"`
}

// mistralWorkspaceView is one organization, once, however many keys it has.
type mistralWorkspaceView struct {
	Workspace  string    `json:"workspace"`
	KeyType    string    `json:"keyType"` // API (Studio) or CHAT (Vibe)
	Plan       string    `json:"plan,omitempty"`
	Accounts   int       `json:"accounts"` // keys of this pool that saw this workspace
	Total      float64   `json:"total"`
	Remaining  float64   `json:"remaining"`
	UsedPct    float64   `json:"usedPercent"`
	Currency   string    `json:"currency"`
	ResetsAt   time.Time `json:"resetsAt"`
	PAYG       *bool     `json:"payg,omitempty"`
	ReadAt     time.Time `json:"readAt"`     // last successful budget read
	CheckedAt  time.Time `json:"checkedAt"`  // last attempt, successful or not
	MixedTypes bool      `json:"mixedTypes"` // the workspace's keys disagree about key type
	// Vibe is vibe_budget for reference only. Mistral's official products
	// spend it and this pool's traffic does not, so it is never added to
	// Total/Remaining nor used to rank the workspace.
	Vibe *mistralVibeView `json:"vibe,omitempty"`
}

// mistralVibeView is one workspace's vibe_budget, shown as display-only.
type mistralVibeView struct {
	Total      float64   `json:"total"`
	Remaining  float64   `json:"remaining"`
	UsedPct    float64   `json:"usedPercent"`
	Currency   string    `json:"currency"`
	ResetsAt   time.Time `json:"resetsAt"`
	Note       string    `json:"note"`
}

const mistralVibeNote = "仅限 Mistral 官方产品，号池调用不扣此项"

// mistralCurrencyTotal is what can honestly be added up: one currency.
type mistralCurrencyTotal struct {
	Currency   string  `json:"currency"`
	Total      float64 `json:"total"`
	Remaining  float64 `json:"remaining"`
	Used       float64 `json:"used"`
	Workspaces int     `json:"workspaces"`
	Accounts   int     `json:"accounts"`
}

const mistralUnknownRule = "只汇总本服务读取成功且未过期的预算；同一组织多个 Key 共享同一份 api_budget，已按工作区去重。读取失败、过期或重置时间已过的 Key 计为未知，不按 0 计入，也不按 Key 重复累加。合计与排序只用 api_budget：号池调用 /v1/chat/completions 的用量按 Mistral 规定只扣 api_budget，vibe_budget 仅限官方产品，不参与合计。"

func mistralOverviewFor(views []accountView, now time.Time) *mistralOverview {
	out := &mistralOverview{Workspaces: []mistralWorkspaceView{}, Groups: []mistralCurrencyTotal{}, UnknownRule: mistralUnknownRule}
	type acc struct {
		view  accountView
		fresh bool
		ws    string
	}
	var accounts []acc
	// Keys of one organization may report currency or reset time slightly
	// differently within one read; the largest remaining is the one that is
	// true of the whole workspace, and the read keeps a fixed sort order so
	// repeated requests do not disagree.
	for _, v := range views {
		if providerOf(v.Provider) != config.ProviderMistral {
			continue
		}
		out.Accounts++
		// QuotaFresh already applies this pool's staleSec and the reset time;
		// a measured budget also needs a currency and a positive total before
		// it may join a sum.
		ok := v.QuotaFresh && v.Quota != nil && v.Quota.Mistral != nil
		if ok {
			m := v.Quota.Mistral
			b := m.Budget
			ok = m.Error == "" && b != nil && b.Currency != "" && b.Total >= 0 && b.Remaining >= 0
		}
		a := acc{view: v, fresh: ok}
		if ok {
			a.ws = v.Quota.WorkspaceID
			if a.ws == "" {
				// A workspace the upstream has not named yet is still one
				// identity for this key; never merge distinct unnamed keys.
				a.ws = "key:" + v.ID
			}
			out.FreshAccounts++
		} else {
			out.UnknownAccounts++
		}
		accounts = append(accounts, a)
	}
	byWS := map[string]*mistralWorkspaceView{}
	for _, a := range accounts {
		if !a.fresh {
			continue
		}
		m := a.view.Quota.Mistral
		b := m.Budget
		w := byWS[a.ws]
		if w == nil {
			w = &mistralWorkspaceView{Workspace: a.ws, KeyType: m.KeyType, Plan: m.Plan, Currency: b.Currency,
				ResetsAt: b.ResetsAt, PAYG: b.PAYG, ReadAt: m.At, CheckedAt: m.CheckedAt}
			byWS[a.ws] = w
		}
		if v := m.VibeBudget; v != nil && v.Currency != "" {
			// Same rule as the api figures: several keys of one workspace
			// report one allowance, so the largest reading is kept once.
			if w.Vibe == nil {
				w.Vibe = &mistralVibeView{Currency: v.Currency, ResetsAt: v.ResetsAt, Note: mistralVibeNote}
			}
			if v.Total > w.Vibe.Total {
				w.Vibe.Total = v.Total
			}
			if v.Remaining > w.Vibe.Remaining {
				w.Vibe.Remaining = v.Remaining
			}
			if v.UsedPercent > w.Vibe.UsedPct {
				w.Vibe.UsedPct = v.UsedPercent
			}
			if v.ResetsAt.After(w.Vibe.ResetsAt) {
				w.Vibe.ResetsAt = v.ResetsAt
			}
		}
		w.Accounts++
		if m.KeyType != "" && w.KeyType != "" && m.KeyType != w.KeyType {
			w.MixedTypes = true
		}
		if b.Currency > w.Currency { // one currency per workspace; keep a stable, non-empty one
			w.Currency = b.Currency
		}
		if b.Total > w.Total {
			w.Total = b.Total
		}
		if b.Remaining > w.Remaining {
			w.Remaining = b.Remaining
		}
		if b.UsedPercent > w.UsedPct {
			w.UsedPct = b.UsedPercent
		}
		if b.ResetsAt.After(w.ResetsAt) {
			w.ResetsAt = b.ResetsAt
		}
		if m.At.After(w.ReadAt) {
			w.ReadAt = m.At
		}
		if m.CheckedAt.After(w.CheckedAt) {
			w.CheckedAt = m.CheckedAt
		}
	}
	for _, w := range byWS {
		out.Workspaces = append(out.Workspaces, *w)
	}
	sort.Slice(out.Workspaces, func(i, j int) bool {
		a, b := out.Workspaces[i], out.Workspaces[j]
		if a.KeyType != b.KeyType {
			return a.KeyType < b.KeyType
		}
		if a.Currency != b.Currency {
			return a.Currency < b.Currency
		}
		return a.Workspace < b.Workspace
	})
	totals := map[string]*mistralCurrencyTotal{}
	for _, w := range out.Workspaces {
		t := totals[w.Currency]
		if t == nil {
			t = &mistralCurrencyTotal{Currency: w.Currency}
			totals[w.Currency] = t
		}
		t.Total += w.Total
		t.Remaining += w.Remaining
		t.Used += w.Total - w.Remaining
		t.Workspaces++
		t.Accounts += w.Accounts
	}
	for _, t := range totals {
		out.Groups = append(out.Groups, *t)
	}
	sort.Slice(out.Groups, func(i, j int) bool { return out.Groups[i].Currency < out.Groups[j].Currency })
	return out
}
