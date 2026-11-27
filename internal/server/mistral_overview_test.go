package server

import (
	"math"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
)

// mistralView builds one account's view the way accountViews would: a
// successful budget read is fresh, anything else stays out of the totals.
func mistralView(t *testing.T, id, workspace, kind, plan, currency string, total, remaining, used float64, fresh bool, resetIn time.Duration) accountView {
	t.Helper()
	now := time.Now()
	// accountViews mirrors whoami's customer_id into WorkspaceID; the helper
	// does the same so the grouping here matches production.
	snap := &quota.Snapshot{WorkspaceID: workspace, Mistral: &quota.MistralState{
		Org: workspace, KeyType: kind, Plan: plan,
		Budget: &quota.MistralBudget{Total: total, Remaining: remaining, UsedPercent: used, Currency: currency, ResetsAt: now.Add(resetIn)},
		At:     now,
	}}
	return accountView{ID: id, Provider: config.ProviderMistral, Quota: snap, QuotaFresh: fresh}
}

func TestMistralOverviewDedupesWorkspaces(t *testing.T) {
	views := []accountView{
		// one organization, two keys (studio + vibe) sharing one budget
		mistralView(t, "a", "ws-1", "API", "Student", "USD", 30, 22.5, 25, true, 24*time.Hour),
		mistralView(t, "b", "ws-1", "API", "Student", "USD", 30, 22.5, 25, true, 24*time.Hour),
		// another organization, other currency
		mistralView(t, "c", "ws-2", "CHAT", "Pro", "EUR", 300, 270, 10, true, 48*time.Hour),
		// an unreadable key must not become zero
		{ID: "d", Provider: config.ProviderMistral, Quota: nil, QuotaFresh: false},
	}
	o := mistralOverviewFor(views, time.Now(), false)
	if o.Accounts != 4 || o.FreshAccounts != 3 || o.UnknownAccounts != 1 {
		t.Fatalf("counts = %d/%d/%d", o.Accounts, o.FreshAccounts, o.UnknownAccounts)
	}
	if len(o.Workspaces) != 2 {
		t.Fatalf("workspaces = %d, want 2", len(o.Workspaces))
	}
	if len(o.Groups) != 2 {
		t.Fatalf("currency groups = %d, want 2 (USD and EUR must never be added)", len(o.Groups))
	}
	byCur := map[string]mistralCurrencyTotal{}
	for _, g := range o.Groups {
		byCur[g.Currency] = g
	}
	if usd := byCur["USD"]; usd.Workspaces != 1 || usd.Accounts != 2 || math.Abs(usd.Total-30) > 1e-9 || math.Abs(usd.Remaining-22.5) > 1e-9 {
		t.Fatalf("USD total = %+v", usd)
	}
	if eur := byCur["EUR"]; eur.Workspaces != 1 || eur.Accounts != 1 || math.Abs(eur.Total-300) > 1e-9 {
		t.Fatalf("EUR total = %+v", eur)
	}
	for _, w := range o.Workspaces {
		if w.Workspace == "ws-1" && (w.Accounts != 2 || w.MixedTypes) {
			t.Fatalf("ws-1 = %+v", w)
		}
	}
	if o.UnknownRule == "" {
		t.Fatal("unknown rule must be stated")
	}
}

func TestMistralOverviewExcludesStaleAndUnknownKeys(t *testing.T) {
	stale := mistralView(t, "a", "ws-1", "API", "", "USD", 100, 100, 0, false, 24*time.Hour)
	nobudget := accountView{ID: "b", Provider: config.ProviderMistral, QuotaFresh: true, Quota: &quota.Snapshot{Mistral: &quota.MistralState{KeyType: "API"}}}
	other := accountView{ID: "c", Provider: config.ProviderModal, QuotaFresh: true}
	o := mistralOverviewFor([]accountView{stale, nobudget, other}, time.Now(), false)
	if o.Accounts != 2 || o.FreshAccounts != 0 || o.UnknownAccounts != 2 {
		t.Fatalf("counts = %+v", o)
	}
	if len(o.Groups) != 0 || len(o.Workspaces) != 0 {
		t.Fatalf("unknown keys must not enter totals: %+v %+v", o.Groups, o.Workspaces)
	}
}

func TestMistralOverviewUnnamedWorkspaceIsNotMerged(t *testing.T) {
	a := mistralView(t, "k1", "", "API", "", "USD", 10, 5, 50, true, time.Hour)
	b := mistralView(t, "k2", "", "API", "", "USD", 20, 20, 0, true, time.Hour)
	o := mistralOverviewFor([]accountView{a, b}, time.Now(), false)
	if len(o.Workspaces) != 2 {
		t.Fatalf("unnamed keys merged into one workspace: %+v", o.Workspaces)
	}
	if len(o.Groups) != 1 || math.Abs(o.Groups[0].Total-30) > 1e-9 || math.Abs(o.Groups[0].Remaining-25) > 1e-9 {
		t.Fatalf("group = %+v", o.Groups)
	}
}

func TestMistralOverviewIsNilForOtherPools(t *testing.T) {
	views := []accountView{mistralView(t, "a", "ws", "API", "", "USD", 1, 1, 0, true, time.Hour)}
	if o := mistralOverviewFor(nil, time.Now(), false); o.Accounts != 0 || o.Groups == nil || o.Workspaces == nil {
		t.Fatalf("empty pool must render as empty, not null: %+v", o)
	}
	_ = views
}
