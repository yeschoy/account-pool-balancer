package pool

import (
	"reflect"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
	"account-pool-balancer/internal/routes"
	"account-pool-balancer/internal/rules"
)

// mistralChatSeat is a CHAT (Vibe) account whose own budget is a spent
// api_budget next to an untouched vibe_budget: exactly the 2303 case.
func mistralChatSeat(t *testing.T, cfg *config.Config, a config.Account, apiUsed, vibeUsed float64) *quota.Snapshot {
	t.Helper()
	now := time.Now()
	source := quota.ResponseBindingSource(cfg, a)
	return &quota.Snapshot{CatalogSource: source, Mistral: &quota.MistralState{
		Source: source, Org: "ws-" + a.ID, KeyType: "CHAT", At: now,
		Budget:     &quota.MistralBudget{Total: 30, UsedPercent: apiUsed, Remaining: 30 * (1 - apiUsed/100), Currency: "USD", ResetsAt: now.Add(12 * time.Hour)},
		VibeBudget: &quota.MistralBudget{Total: 300, UsedPercent: vibeUsed, Remaining: 300 * (1 - vibeUsed/100), Currency: "USD", ResetsAt: now.Add(12 * time.Hour)},
	}}
}

// TestMistralChatKeyRanksByApiBudget: the vibe budget is display-only, so it
// must not make a CHAT account look richer than the api budget says — neither
// for used share nor for pace, and it must not change what least-used/smart
// choose.
func TestMistralChatKeyRanksByApiBudget(t *testing.T) {
	cfg := testConfig(config.Account{ID: "vibe", Provider: config.ProviderMistral, Group: config.MistralGroup, RuntimeKey: "mstrl_vibe"})
	a := cfg.Accounts[0]
	now := time.Now()
	withVibe := &quota.Snapshot{CatalogSource: quota.ResponseBindingSource(cfg, a), Mistral: &quota.MistralState{
		Source: quota.ResponseBindingSource(cfg, a), At: now,
		Budget:     &quota.MistralBudget{Total: 30, UsedPercent: 100, Remaining: 0, Currency: "USD", ResetsAt: now.Add(12 * time.Hour)},
		VibeBudget: &quota.MistralBudget{Total: 300, UsedPercent: 0, Remaining: 300, Currency: "USD", ResetsAt: now.Add(12 * time.Hour)},
	}}
	// The same workspace read, except that it reported no vibe_budget at all.
	apiOnly := &quota.Snapshot{CatalogSource: withVibe.CatalogSource, Mistral: &quota.MistralState{
		Source: withVibe.Mistral.Source, At: now, KeyType: "CHAT",
		Budget: &quota.MistralBudget{Total: 30, UsedPercent: 100, Remaining: 0, Currency: "USD", ResetsAt: now.Add(12 * time.Hour)},
	}}

	p := New(func(string) *quota.Snapshot { return withVibe })
	got := p.SeatForTest(cfg, a, "m", now)
	if !got.Known || got.Used != 100 {
		t.Fatalf("seat = %+v, want known and 100%% used from api_budget", got)
	}
	if got.Pace != 0 {
		t.Fatalf("pace = %v, want 0: the untouched vibe budget must not add pace", got.Pace)
	}
	p2 := New(func(string) *quota.Snapshot { return apiOnly })
	without := p2.SeatForTest(cfg, a, "m", now)
	if !reflect.DeepEqual(got, without) {
		t.Fatalf("vibe budget changed the ranking: with %+v, without %+v", got, without)
	}

	// A CHAT key at 100% api usage must be picked after a sibling whose api
	// budget is nearly untouched, whatever the vibe numbers say.
	cfg = testConfig(
		config.Account{ID: "vibe", Provider: config.ProviderMistral, Group: config.MistralGroup, RuntimeKey: "mstrl_vibe"},
		config.Account{ID: "studio", Provider: config.ProviderMistral, Group: config.MistralGroup, RuntimeKey: "mstrl_studio"},
	)
	vibe, studio := cfg.Accounts[0], cfg.Accounts[1]
	snaps := map[string]*quota.Snapshot{
		"vibe": mistralChatSeat(t, cfg, vibe, 100, 0),
		"studio": {CatalogSource: quota.ResponseBindingSource(cfg, studio), Mistral: &quota.MistralState{
			Source: quota.ResponseBindingSource(cfg, studio), Org: "ws-studio", KeyType: "API", At: now,
			Budget: &quota.MistralBudget{Total: 30, UsedPercent: 20, Remaining: 24, Currency: "USD", ResetsAt: now.Add(12 * time.Hour)}}},
	}
	for _, strategy := range []string{"least-used", "smart"} {
		cfg.Routing.Strategy = strategy
		p3 := New(func(id string) *quota.Snapshot { return snaps[id] })
		cands, excluded := p3.Preview(cfg, config.MistralGroup, "m")
		if len(cands) != 2 || cands[0] != "studio" {
			t.Fatalf("%s order = %v (excluded %v), want the untouched api_budget first", strategy, cands, excluded)
		}
	}
}

// TestMistralApiBudgetCooldownEndsAtReset drives the shipped 2303 rule into
// the pool the way proxy.go does and checks the account is out only until the
// measured api_budget reset, with no quarantine and no health penalty.
func TestMistralApiBudgetCooldownEndsAtReset(t *testing.T) {
	cfg := testConfig(config.Account{ID: "vibe", Provider: config.ProviderMistral, Group: config.MistralGroup, RuntimeKey: "mstrl_vibe"})
	a := cfg.Accounts[0]
	now := time.Now()
	reset := now.Add(9 * time.Hour)
	snap := mistralChatSeat(t, cfg, a, 100, 0)
	// The helper dates its read at the real clock; keep it inside the window.
	snap.Mistral.At = now
	snap.Mistral.Budget.ResetsAt = reset
	snap.Mistral.VibeBudget.ResetsAt = reset
	p := New(func(string) *quota.Snapshot { return snap })
	p.SetClock(func() time.Time { return now })

	errRules, retries := cfg.RulesFor(config.ProviderMistral)
	body := `{"type":"billing_api_budget_exhausted","code":"2303","message":"API budget exhausted"}`
	rule := rules.MatchError(errRules, 402, body, nil)
	if rule == nil || rule.ID != "mistral-api-budget-exhausted" {
		t.Fatalf("shipped rule not matched: %v", rule)
	}
	if rules.MatchRetry(retries, 402, body, nil) != nil {
		t.Fatal("a 402/2303 must not stop account switching")
	}
	d := rules.CooldownFor(rule, nil, body, now, 1, rules.ResetBudget{Reset: snap.Mistral.ApiBudgetReset()})
	if d != 9*time.Hour {
		t.Fatalf("cooldown = %v, want the api_budget reset in 9h", d)
	}
	p.Cooldown(a.ID, "", now.Add(d), 402, rule.ID, rule.ID)

	p.mu.Lock()
	st := p.states[a.ID]
	p.mu.Unlock()
	if st.HardQuarantined {
		t.Fatal("a spent api_budget must never hard-quarantine the account")
	}
	if st.RuleID != rule.ID || !st.CooldownUntil.Equal(reset) {
		t.Fatalf("state = %+v", st)
	}
	// No sample was recorded for the account: a cooldown, not a degrade, is
	// what a spent allowance means, so its health must stay untouched.
	view := p.Views(cfg)[a.ID]
	if view.Health != nil && *view.Health < 1 {
		t.Fatalf("health penalized: %+v", *view.Health)
	}
	if view.Status != "cooldown" {
		t.Fatalf("status = %q, want cooldown (not quarantined)", view.Status)
	}
	// Just before the reset it is still unavailable, right after it is back.
	if why := p.unavailableLocked(cfg, a, "m", reset.Add(-time.Minute)); why == "" {
		t.Fatal("account available before its api_budget reset")
	}
	if why := p.unavailableLocked(cfg, a, "m", reset.Add(time.Second)); why != "" {
		t.Fatalf("account did not recover after the api_budget reset: %s", why)
	}
	// A replenished budget then ranks normally again. The read is the one a
	// refresh after the reset returns: a new window with its own reset time.
	next := reset.Add(30 * 24 * time.Hour)
	snap.Mistral.Budget.UsedPercent, snap.Mistral.Budget.Remaining = 0, 30
	snap.Mistral.Budget.ResetsAt = next
	snap.Mistral.At = reset
	p.mu.Lock()
	seat := p.seatLocked(cfg, a, "m", reset.Add(time.Second))
	p.mu.Unlock()
	if !seat.Known || seat.Used != 0 {
		t.Fatalf("seat after reset = %+v", seat)
	}
}

// TestMistralApiBudgetCooldownWithoutReset: with no usable read the rule still
// cools the account, for its longest fallback, and says so by rule id only —
// the account comes back when that passes.
func TestMistralApiBudgetCooldownWithoutReset(t *testing.T) {
	cfg := testConfig(config.Account{ID: "vibe", Provider: config.ProviderMistral, Group: config.MistralGroup, RuntimeKey: "mstrl_vibe"})
	a := cfg.Accounts[0]
	now := time.Now()
	p := New(func(string) *quota.Snapshot { return nil })
	errRules, _ := cfg.RulesFor(config.ProviderMistral)
	rule := rules.MatchError(errRules, 402, `{"code":2303}`, nil)
	d := rules.CooldownFor(rule, nil, "", now, 1, rules.ResetBudget{})
	if d != 24*time.Hour {
		t.Fatalf("fallback cooldown = %v, want the 24h longest", d)
	}
	p.Cooldown(a.ID, "", now.Add(d), 402, rule.ID, rule.ID)
	if why := p.unavailableLocked(cfg, a, "m", now.Add(23*time.Hour)); why == "" {
		t.Fatal("account available before the fallback cooldown ended")
	}
	if why := p.unavailableLocked(cfg, a, "m", now.Add(24*time.Hour+time.Second)); why != "" {
		t.Fatalf("account did not recover after the fallback cooldown: %s", why)
	}
}

// TestMistralApiBudgetCooldownIsPoolScoped: the cooldown is this pool's
// state, and neither the 2303 rule nor the account's Mistral snapshot may
// leak into another pool's accounts or rules.
func TestMistralApiBudgetCooldownIsPoolScoped(t *testing.T) {
	cfg := testConfig(
		config.Account{ID: "vibe", Provider: config.ProviderMistral, Group: config.MistralGroup, RuntimeKey: "mstrl_vibe"},
		config.Account{ID: "od", Provider: config.ProviderOpenDesign, Group: "default", RuntimeKey: "sk-amr-od"},
		config.Account{ID: "th", Provider: config.ProviderTokenHarbor, Group: config.ProviderTokenHarbor, RuntimeKey: "thk_live_x"},
	)
	now := time.Now()
	var snap *quota.Snapshot
	p := New(func(id string) *quota.Snapshot {
		if id == "vibe" {
			return snap
		}
		return nil
	})
	snap = mistralChatSeat(t, cfg, cfg.Accounts[0], 100, 0)
	snap.Mistral.Budget.ResetsAt = now.Add(time.Hour)
	rule := rules.MatchError(cfg.MistralErrorRules, 402, `{"code":2303}`, nil)
	p.Cooldown("vibe", "", now.Add(time.Hour), 402, rule.ID, rule.ID)

	// The other pools' rules do not know the Mistral rule, and their accounts
	// keep their own state.
	for _, provider := range []string{config.ProviderOpenDesign, config.ProviderTokenHarbor} {
		errors, _ := cfg.RulesFor(provider)
		if r := rules.MatchError(errors, 402, `{"code":2303}`, nil); r != nil && r.ID == "mistral-api-budget-expired" {
			t.Fatalf("%s matched the Mistral rule", provider)
		}
		for _, r := range errors {
			if r.Reset != nil && r.Reset.BudgetReset {
				t.Fatalf("%s has a budgetReset rule: %s", provider, r.ID)
			}
		}
	}
	od, _ := cfg.Account("od")
	th, _ := cfg.Account("th")
	for _, other := range []config.Account{od, th} {
		// unavailableLocked reports each pool's own reasons (e.g. plan-unknown
		// for a pool without a read snapshot); only the Mistral cooldown
		// reason would show a leak.
		why := p.unavailableLocked(cfg, other, "m", now)
		if why == "cooldown" || why == "quarantined" {
			t.Fatalf("%s/%s excluded by a Mistral cooldown: %s", other.Provider, other.ID, why)
		}
		if _, ok := p.states[other.ID]; ok {
			t.Fatalf("%s/%s carries Mistral state", other.Provider, other.ID)
		}
		if snap := p.StatesSnapshotForTest()[other.ID]; snap != nil && snap.RuleID == rule.ID {
			t.Fatalf("%s/%s carries the Mistral rule", other.Provider, other.ID)
		}
	}
	// And the Mistral account of another group is still not served for this
	// one: the default group sees only its own account.
	if cands, excluded := p.Preview(cfg, "default", "m"); len(cands) != 1 || cands[0] != "od" {
		t.Fatalf("default group preview = %v %v", cands, excluded)
	}
	// Within the Mistral group the cooling account is the reason it is out,
	// and only there.
	if _, excluded := p.Preview(cfg, config.MistralGroup, "m"); excluded["vibe"] != "cooldown" {
		t.Fatalf("Mistral group exclusions = %v, want vibe excluded by cooldown", excluded)
	}
	if !p.StatesSnapshotForTest()["vibe"].CooldownUntil.Equal(now.Add(time.Hour)) {
		t.Fatal("Mistral cooldown missing from its own pool")
	}
}

// StatesSnapshotForTest copies the per-account state map for pool-internal
// tests.
func (p *Pool) StatesSnapshotForTest() map[string]*AccountState {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]*AccountState, len(p.states))
	for id, st := range p.states {
		c := *st
		out[id] = &c
	}
	return out
}

// SeatForTest exposes the ranking seat for pool-internal tests.
func (p *Pool) SeatForTest(cfg *config.Config, a config.Account, model string, now time.Time) routes.Seat {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seatLocked(cfg, a, model, now)
}
