package server

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
)

func chatView(t *testing.T, id, workspace, currency string, apiTotal, apiUsed float64, vibeTotal, vibeUsed float64, fresh bool, resetIn time.Duration) accountView {
	t.Helper()
	now := time.Now()
	m := &quota.MistralState{Org: workspace, KeyType: "CHAT", At: now,
		Budget: &quota.MistralBudget{Total: apiTotal, UsedPercent: apiUsed, Remaining: apiTotal * math.Max(0, 1-apiUsed/100), Currency: currency, ResetsAt: now.Add(resetIn)}}
	if vibeTotal > 0 {
		m.VibeBudget = &quota.MistralBudget{Total: vibeTotal, UsedPercent: vibeUsed, Remaining: vibeTotal * math.Max(0, 1-vibeUsed/100), Currency: currency, ResetsAt: now.Add(resetIn)}
	}
	return accountView{ID: id, Provider: config.ProviderMistral, Quota: &quota.Snapshot{WorkspaceID: workspace, Mistral: m}, QuotaFresh: fresh}
}

// TestMistralOverviewTotalsApiBudgetOnly: the overview's totals are what this
// pool can still spend. A CHAT workspace whose Vibe allowance is untouched
// but whose api_budget is spent must report 0 left, with the Vibe number
// carried separately and marked display-only.
func TestMistralOverviewTotalsApiBudgetOnly(t *testing.T) {
	views := []accountView{
		// the production shape: Vibe keys, api 30 USD fully spent, vibe 300 USD unused
		chatView(t, "v1", "ws-chat", "USD", 30, 100, 300, 0, true, 24*time.Hour),
		chatView(t, "v2", "ws-chat", "USD", 30, 100, 300, 0, true, 24*time.Hour),
		// a Studio key of another workspace with money left
		mistralView(t, "s1", "ws-studio", "API", "Pro", "USD", 30, 22.5, 25, true, 24*time.Hour),
	}
	o := mistralOverviewFor(views, time.Now(), false)
	var chat, studio *mistralWorkspaceView
	for i := range o.Workspaces {
		switch o.Workspaces[i].Workspace {
		case "ws-chat":
			chat = &o.Workspaces[i]
		case "ws-studio":
			studio = &o.Workspaces[i]
		}
	}
	if chat == nil || studio == nil {
		t.Fatalf("workspaces = %+v", o.Workspaces)
	}
	if chat.Total != 30 || chat.Remaining != 0 || chat.UsedPct != 100 {
		t.Fatalf("api budget not used for the CHAT workspace: %+v", chat)
	}
	if chat.Vibe == nil || chat.Vibe.Total != 300 || chat.Vibe.Remaining != 300 || chat.Vibe.Note == "" {
		t.Fatalf("vibe budget missing or not marked display-only: %+v", chat.Vibe)
	}
	if strings.Contains(chat.Vibe.Note, "api") || !strings.Contains(chat.Vibe.Note, "官方产品") {
		t.Fatalf("vibe note = %q", chat.Vibe.Note)
	}
	// The total is the api budgets only: 0 (chat) + 22.5 (studio).
	if len(o.Groups) != 1 || o.Groups[0].Total != 60 || math.Abs(o.Groups[0].Remaining-22.5) > 1e-9 {
		t.Fatalf("totals = %+v (vibe must not be added)", o.Groups)
	}
	// The vibe amount never reaches a sum even when it is huge.
	if o.Groups[0].Remaining >= 300 || o.Groups[0].Total >= 330 {
		t.Fatalf("vibe added to the total: %+v", o.Groups[0])
	}
	if !strings.Contains(o.UnknownRule, "api_budget") || !strings.Contains(o.UnknownRule, "vibe_budget") {
		t.Fatalf("unknown rule must state which budget counts: %q", o.UnknownRule)
	}
}

// TestMistralOverviewOmitsMissingVibe: a workspace with no Vibe plan is not an
// error and shows nothing for it, rather than a zero.
func TestMistralOverviewOmitsMissingVibe(t *testing.T) {
	o := mistralOverviewFor([]accountView{chatView(t, "a", "ws", "USD", 30, 50, 0, 0, true, time.Hour)}, time.Now(), false)
	if len(o.Workspaces) != 1 || o.Workspaces[0].Vibe != nil {
		t.Fatalf("workspace = %+v", o.Workspaces)
	}
}

// TestMistralChatAccountProjectsApiAndVibeBudgets refreshes a CHAT (Vibe) key
// whose workspace has a spent api_budget and an untouched vibe_budget, then
// reads the account API the console uses: the pool balance must be the api
// budget and the vibe budget must be carried as display-only.
func TestMistralChatAccountProjectsApiAndVibeBudgets(t *testing.T) {
	e := newMistralEnv(t)
	reset := time.Now().Add(9 * time.Hour).UTC().Truncate(time.Second)
	e.setHandler(nil)
	e.up.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/vibe/whoami":
			fmt.Fprint(w, `{"plan_type":"CHAT","plan_name":"INDIVIDUAL","customer_id":"8d8f124d-0000-4000-8000-0000000000aa"}`)
		case "/v1/billing/budget":
			fmt.Fprintf(w, `{"api_budget":{"initial_budget":30,"usage_percentage":100,"currency":"USD","reset_at":%q},`+
				`"vibe_budget":{"initial_budget":300,"usage_percentage":0,"currency":"USD","reset_at":%q}}`, reset.Format(time.RFC3339), reset.Format(time.RFC3339))
		case "/v1/billing/subscription":
			fmt.Fprint(w, `{"plan_name":"INDIVIDUAL","payg_enabled":false}`)
		default:
			fmt.Fprint(w, `{"object":"list","data":[{"id":"m"}]}`)
		}
	})
	r := httptest.NewRequest(http.MethodPost, "/api/accounts/m1/refresh-quota", nil)
	r.SetPathValue("id", "m1")
	w := httptest.NewRecorder()
	e.s.apiAccountAction(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("refresh failed: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	e.s.apiAccounts(w, httptest.NewRequest(http.MethodGet, "/api/accounts?provider=mistral", nil))
	var response struct {
		Accounts []struct {
			ID    string          `json:"id"`
			Fresh bool            `json:"quotaFresh"`
			Quota *quota.Snapshot `json:"quota"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	for _, a := range response.Accounts {
		if a.ID != "m1" {
			continue
		}
		if !a.Fresh || a.Quota == nil || a.Quota.Mistral == nil {
			t.Fatalf("quota not projected: %+v", a)
		}
		m := a.Quota.Mistral
		if m.KeyType != "CHAT" || m.Budget == nil || m.Budget.Total != 30 || m.Budget.Remaining != 0 {
			t.Fatalf("pool balance must be the spent api_budget: %+v", m)
		}
		if m.VibeBudget == nil || m.VibeBudget.Total != 300 || m.VibeBudget.Remaining != 300 {
			t.Fatalf("vibe budget must be projected for display: %+v", m.VibeBudget)
		}
		if !m.ApiBudgetReset().Equal(reset) {
			t.Fatalf("api reset = %v, want %v", m.ApiBudgetReset(), reset)
		}
		return
	}
	t.Fatal("m1 missing from the projection")
}

// TestMistral402BudgetExhaustedCoolsToReset drives a real request through the
// gateway: the upstream answers 402 billing_api_budget_exhausted/2303, so the
// account must cool until its measured api_budget reset, switch, and never be
// quarantined.
func TestMistral402BudgetExhaustedCoolsToReset(t *testing.T) {
	e := newMistralEnv(t)
	cfg := e.s.cfg.Get()
	accounts := map[string]config.Account{}
	for _, a := range cfg.Accounts {
		accounts[a.ID] = a
	}
	reset := time.Now().Add(7 * time.Hour).UTC().Truncate(time.Second)
	// m1 has a measured api_budget that is nearly spent; m2 has an untouched
	// one and is the account the request must fall back to.
	snaps := map[string]*quota.Snapshot{}
	for id, used := range map[string]float64{"m1": 95, "m2": 10} {
		a := accounts[id]
		snaps[id] = &quota.Snapshot{CatalogSource: quota.ResponseBindingSource(cfg, a), Mistral: &quota.MistralState{
			Source: quota.ResponseBindingSource(cfg, a), Org: "ws-" + id, KeyType: "CHAT", At: time.Now(),
			Budget: &quota.MistralBudget{Total: 30, UsedPercent: used, Remaining: 30 * (1 - used/100), Currency: "USD", ResetsAt: reset},
		}}
	}
	e.s.quota.Load(snaps)
	var seen []string
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		e.mu.Unlock()
		if strings.HasSuffix(r.Header.Get("Authorization"), "secret_1") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPaymentRequired)
			fmt.Fprint(w, `{"type":"billing_api_budget_exhausted","code":"2303","message":"API budget exhausted"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"reply","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`)
	})
	status, body := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, nil)
	if status != http.StatusOK {
		t.Fatalf("request failed: %d %s", status, body)
	}
	if len(seen) != 2 {
		t.Fatalf("attempts = %d, want the first account then a switch", len(seen))
	}
	st := e.s.pool.Views(e.s.cfg.Get())["m1"].State
	if st == nil || st.HardQuarantined {
		t.Fatalf("a spent api_budget must not quarantine: %+v", st)
	}
	if st.RuleID != "mistral-api-budget-exhausted" {
		t.Fatalf("rule = %q", st.RuleID)
	}
	if !st.CooldownUntil.Equal(reset) {
		t.Fatalf("cooldown until %v, want the api_budget reset %v", st.CooldownUntil, reset)
	}
	if view := e.s.pool.Views(e.s.cfg.Get())["m1"]; view.Health != nil && *view.Health < 1 {
		t.Fatalf("global health penalized: %+v", *view.Health)
	}
	// The other 402 keeps its original behaviour: an escalating generic
	// cooldown, not the reset-based one.
	e2 := newMistralEnv(t)
	e2.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		fmt.Fprint(w, `{"type":"billing_error","code":"1099","message":"payment required"}`)
	})
	status, _ = e2.post(t, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("other 402 status = %d, want 503 as before", status)
	}
	var generic *bool
	for _, id := range []string{"m1", "m2"} {
		if st := e2.s.pool.Views(e2.s.cfg.Get())[id].State; st != nil && st.RuleID == "mistral-insufficient-credits" {
			generic = &st.HardQuarantined
			if d := time.Until(st.CooldownUntil); d > 11*time.Minute || d < 9*time.Minute {
				t.Fatalf("generic 402 cooldown = %v, want the original 10m", d)
			}
		}
	}
	if generic == nil {
		t.Fatal("other 402 no longer uses the generic rule")
	}
}

// TestMistralOverviewUsesPayingWindowInPlanKeyMode: the same production shape
// must be reported as 300 left once the pool sends the Vibe client marker,
// because that is the window a request then spends. The api window stays
// visible as the counterpart so the number is never mistaken for it again.
func TestMistralOverviewUsesPayingWindowInPlanKeyMode(t *testing.T) {
	views := []accountView{chatView(t, "v1", "ws-chat", "USD", 30, 100, 300, 0, true, 24*time.Hour)}

	off := mistralOverviewFor(views, time.Now(), false)
	if len(off.Workspaces) != 1 || len(off.Groups) != 1 {
		t.Fatalf("unexpected shape: %+v", off)
	}
	if w := off.Workspaces[0]; w.Budget != "api" || w.Remaining != 0 || w.Vibe == nil || w.Vibe.Remaining != 300 {
		t.Fatalf("off: workspace = %+v, want 0 api left with vibe shown beside it", w)
	}
	if off.Groups[0].Remaining != 0 {
		t.Fatalf("off total = %v, want 0", off.Groups[0].Remaining)
	}

	on := mistralOverviewFor(views, time.Now(), true)
	w := on.Workspaces[0]
	if w.Budget != "vibe" || w.Remaining != 300 || w.Total != 300 {
		t.Fatalf("on: workspace = %+v, want the Vibe allowance as the balance", w)
	}
	if w.API == nil || w.API.Remaining != 0 {
		t.Fatalf("on: api counterpart = %+v, want the spent api_budget kept for reference", w.API)
	}
	if on.Groups[0].Remaining != 300 {
		t.Fatalf("on total = %v, want 300", on.Groups[0].Remaining)
	}

	// A Studio key is unaffected: nothing about it is billed to vibe_budget.
	studio := []accountView{mistralView(t, "s1", "ws-studio", "API", "Pro", "USD", 30, 22.5, 25, true, 24*time.Hour)}
	if got := mistralOverviewFor(studio, time.Now(), true); got.Workspaces[0].Budget != "api" || got.Workspaces[0].Remaining != 22.5 {
		t.Fatalf("studio in plan-key mode = %+v, want api_budget", got.Workspaces[0])
	}

	// An unread key still contributes nothing rather than a made-up zero.
	unknown := []accountView{chatView(t, "v9", "ws-unknown", "USD", 30, 100, 300, 0, false, 24*time.Hour)}
	if got := mistralOverviewFor(unknown, time.Now(), true); got.UnknownAccounts != 1 || len(got.Workspaces) != 0 {
		t.Fatalf("stale read entered the total: %+v", got)
	}
}

// The account row must read the same window as ranking and the pool view. It
// used to read quota.mistral.budget directly, so a Vibe account with 300 left
// displayed "api_budget 0.00 / 30.00, used 100%" and looked unusable.
func TestMistralAccountViewNamesPayingWindow(t *testing.T) {
	e := newMistralEnv(t)
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Mistral.ChatMetadata = &config.MistralChatMetadata{Mode: config.MistralChatMetadataChatKey, CallSource: config.MistralCallSourceVibeCode}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	e.s.quota.Load(map[string]*quota.Snapshot{"m1": {Mistral: &quota.MistralState{
		KeyType: "CHAT", At: now, CheckedAt: now,
		Budget:     &quota.MistralBudget{Total: 30, UsedPercent: 100, Remaining: 0, Currency: "USD", ResetsAt: now.Add(24 * time.Hour)},
		VibeBudget: &quota.MistralBudget{Total: 300, UsedPercent: 0, Remaining: 300, Currency: "USD", ResetsAt: now.Add(24 * time.Hour)},
	}}})
	views := e.s.accountViews(e.s.cfg.Get())
	var got accountView
	for _, v := range views {
		if v.ID == "m1" {
			got = v
		}
	}
	if got.MistralBudgetWindow != "vibe" {
		t.Fatalf("window = %q, want vibe for a CHAT key with the marker on", got.MistralBudgetWindow)
	}
	// With the marker off the same key bills api_budget and the row says so.
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Mistral.ChatMetadata = &config.MistralChatMetadata{Mode: config.MistralChatMetadataOff}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, v := range e.s.accountViews(e.s.cfg.Get()) {
		if v.ID == "m1" && v.MistralBudgetWindow != "api" {
			t.Fatalf("window = %q, want api with the plan key off", v.MistralBudgetWindow)
		}
	}
	// A Studio key is never labelled vibe, even in plan-key mode.
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Mistral.ChatMetadata = &config.MistralChatMetadata{Mode: config.MistralChatMetadataAllKeys, CallSource: config.MistralCallSourceVibeCode}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.s.quota.Load(map[string]*quota.Snapshot{"m1": {Mistral: &quota.MistralState{
		KeyType: "API", At: now, CheckedAt: now,
		Budget: &quota.MistralBudget{Total: 30, UsedPercent: 40, Remaining: 18, Currency: "USD", ResetsAt: now.Add(24 * time.Hour)},
	}}})
	for _, v := range e.s.accountViews(e.s.cfg.Get()) {
		if v.ID == "m1" && v.MistralBudgetWindow != "api" {
			t.Fatalf("Studio window = %q, want api", v.MistralBudgetWindow)
		}
	}
}
