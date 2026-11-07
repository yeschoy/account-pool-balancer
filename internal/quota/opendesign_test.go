package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

type designReply struct {
	status int
	body   string
	wait   bool
}

type designMock struct {
	mu      sync.Mutex
	replies map[string]designReply
	calls   []string
}

func newDesignMock(t *testing.T) (*designMock, *Poller, *atomic.Pointer[config.Config]) {
	t.Helper()
	f := &designMock{replies: map[string]designReply{
		"/models":                    {200, `{"data":[{"id":"mock-model"}]}`, false},
		"/api/v1/workspaces/current": {200, `{"workspaceId":"ws1","memberStatus":"active","lifecycleState":"active"}`, false},
		"/api/v1/workspaces/ws1/billing/preflight": {200, fmt.Sprintf(`{"balanceUsd":"0","codingPlan":{"eligible":true,"tier":"plus","windows":[{"durationSeconds":604800,"usedCredits":"90","limitCredits":"100","remainingCredits":"10","resetsAt":%q}]}}`, time.Now().Add(time.Hour).Format(time.RFC3339)), false},
		"/api/v1/workspaces/ws1/wallet/balance":    {200, `{"balanceUsd":"0"}`, false},
		"/api/v1/billing/summary":                  {200, `{"balances":{"subscriptionCredits":"100","rechargeCredits":"0"}}`, false},
	}}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		reply, ok := f.replies[r.URL.Path]
		f.mu.Unlock()
		if !ok || r.Method != http.MethodGet {
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if reply.wait {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		fmt.Fprint(w, reply.body)
	}))
	t.Cleanup(up.Close)
	cfg := config.Default()
	cfg.Upstream.RuntimeBase, cfg.Upstream.ControlBase = up.URL, up.URL
	cfg.Accounts = []config.Account{{ID: "od1", Provider: config.ProviderOpenDesign, RuntimeKey: "sk-amr-mock", ControlKey: "vela_ctrl_mock", Enabled: false}}
	var current atomic.Pointer[config.Config]
	current.Store(cfg)
	p := NewPoller(current.Load, func(string) *http.Client { return up.Client() })
	return f, p, &current
}

func (f *designMock) set(path string, r designReply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[path] = r
}

func TestOpenDesignErrorEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, body, state string
		status            int
	}{
		{"ban", `{"error":{"code":"forbidden_api_key","message":"[code=forbidden_api_key] query api key by digest: api key owner banned"}}`, "banned", 403},
		{"forbidden is not ban", `{"error":{"code":"forbidden_api_key","message":"permission denied"}}`, "permission-denied", 403},
		{"wallet scope is not ban", `{"error":"workspace_billing_scope_forbidden"}`, "permission-denied", 403},
		{"untrusted message", `{"error":{"code":"forbidden_api_key","message":"not api key owner banned TOP_SECRET"}}`, "permission-denied", 403},
		{"500 cannot prove ban", `{"error":{"code":"forbidden_api_key","message":"api key owner banned"}}`, "upstream-unavailable", 500},
		{"auth", `{"error":"unauthenticated"}`, "auth-failed", 401},
		{"secret in code", `{"error":"TOP_SECRET"}`, "auth-failed", 401},
		{"billing", `{"error":"database_unavailable"}`, "upstream-unavailable", 503},
		{"rate", `{"error":"rate_limited"}`, "rate-limited", 429},
		{"402 not measured zero", `{"error":"payment_required"}`, "funding-rejected", 402},
		{"html", `<html>TOP_SECRET</html>`, "upstream-unavailable", 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := readHTTPError(tc.status, []byte(tc.body))
			r := readResult(e)
			if r.State != tc.state {
				t.Fatalf("result = %+v", r)
			}
			b, _ := json.Marshal(r)
			if strings.Contains(string(b)+e.Error()+r.Description(), "TOP_SECRET") || strings.Contains(string(b), "message") {
				t.Fatalf("raw error leaked: %s", b)
			}
		})
	}
	if e := readHTTPError(403, []byte(strings.Repeat("x", (1<<20)+1))); e.OwnerBanned || e.Code != "" {
		t.Fatal("oversized body trusted")
	}
}

func TestOpenDesignIndependentReadsAndRecovery(t *testing.T) {
	f, p, current := newDesignMock(t)
	a := current.Load().Accounts[0]
	if err := p.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	good := p.Get(a.ID)
	d := good.OpenDesignDiagnosis(time.Now(), time.Hour)
	if !d.PlanFresh || d.PlanStatus != "available" || d.WalletStatus != "empty" || !d.BillingFresh {
		t.Fatalf("good diagnosis: %+v", d)
	}
	// Plan fails, but wallet and billing independently advance their values.
	f.set("/api/v1/workspaces/ws1/billing/preflight", designReply{503, `{"error":"billing_preflight_unavailable"}`, false})
	f.set("/api/v1/workspaces/ws1/wallet/balance", designReply{200, `{"balanceUsd":"3.5"}`, false})
	f.set("/api/v1/billing/summary", designReply{200, `{"balances":{"subscriptionCredits":"42"}}`, false})
	f.set("/models", designReply{500, `{"error":{"code":"internal_error","message":"TOP_SECRET"}}`, false})
	if p.RefreshNow(context.Background(), a) == nil {
		t.Fatal("partial failure reported success")
	}
	s := p.Get(a.ID)
	d = s.OpenDesignDiagnosis(time.Now(), time.Hour)
	if d.Status != "upstream-unavailable" || d.PlanFresh || d.PlanStatus != "unknown" || !d.WalletFresh || !d.BillingFresh {
		t.Fatalf("partial diagnosis: %+v", d)
	}
	if !s.PlanAt.Equal(good.PlanAt) || s.Windows[0].Remaining != 10 || s.WalletUSD != "3.5" || s.Summary.SubscriptionCredits != "42" || s.AuthFailed {
		t.Fatalf("partial snapshot: %+v", s)
	}
	if s.Failures != 1 || !s.NextAt.After(time.Now()) || current.Load().Accounts[0].Enabled {
		t.Fatal("retry / account switch changed")
	}
	body, _ := json.Marshal(s)
	if strings.Contains(string(body), "TOP_SECRET") || strings.Contains(string(body), a.RuntimeKey) || strings.Contains(string(body), a.ControlKey) {
		t.Fatal("secrets entered snapshot")
	}
	f.set("/api/v1/workspaces/ws1/billing/preflight", designReply{200, fmt.Sprintf(`{"codingPlan":{"eligible":true,"windows":[{"limitCredits":"100","remainingCredits":"0","usedCredits":"100","resetsAt":%q}]}}`, time.Now().Add(time.Hour).Format(time.RFC3339)), false})
	f.set("/models", designReply{200, `{"data":[{"id":"new-model"}]}`, false})
	if err := p.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	s = p.Get(a.ID)
	d = s.OpenDesignDiagnosis(time.Now(), time.Hour)
	if d.PlanStatus != "exhausted" || d.WalletStatus != "positive" || s.Failures != 0 || s.LastError != "" || !s.Catalog["new-model"] {
		t.Fatalf("recovery: %+v %+v", d, s)
	}
}

func TestOpenDesignBanSurvivesActiveWorkspaceAndTransientFailure(t *testing.T) {
	f, p, current := newDesignMock(t)
	a := current.Load().Accounts[0]
	f.set("/models", designReply{403, `{"error":{"code":"forbidden_api_key","message":"api key owner banned"}}`, false})
	_ = p.RefreshNow(context.Background(), a)
	s := p.Get(a.ID)
	if s.OpenDesignDiagnosis(time.Now(), time.Hour).Status != "banned" || s.AuthFailed || s.OpenDesign.Workspace.State != "ok" {
		t.Fatalf("active workspace must not undo runtime ban: %+v", s)
	}
	clone := p.Get(a.ID)
	clone.OpenDesign.RuntimeRestriction.State = "bad-test-mutation"
	clone.OpenDesign.Catalog.State = "bad-test-mutation"
	if p.Get(a.ID).OpenDesign.RuntimeRestriction.State != "banned" {
		t.Fatal("snapshot clone shares mutable diagnostics")
	}
	f.set("/models", designReply{500, `{"error":"internal_error"}`, false})
	_ = p.RefreshNow(context.Background(), a)
	if p.Get(a.ID).OpenDesignDiagnosis(time.Now(), time.Hour).Status != "banned" {
		t.Fatal("transient failure erased confirmed ban")
	}
	f.set("/models", designReply{200, `{"data":[{"id":"m"}]}`, false})
	if err := p.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if p.Get(a.ID).OpenDesign.RuntimeRestriction != nil || current.Load().Accounts[0].Enabled {
		t.Fatal("successful metadata may clear diagnostic, not enable account")
	}
}

func TestOpenDesignTimeoutDoesNotBlockOtherParts(t *testing.T) {
	f, p, current := newDesignMock(t)
	f.set("/api/v1/workspaces/ws1/billing/preflight", designReply{wait: true})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if p.RefreshNow(ctx, current.Load().Accounts[0]) == nil {
		t.Fatal("timeout reported success")
	}
	s := p.Get("od1")
	if s.OpenDesign.Plan.State == "ok" || s.OpenDesign.Wallet.State != "ok" || s.OpenDesign.Billing.State != "ok" || s.WalletUSD != "0" {
		t.Fatalf("independent results lost to timeout: %+v", s)
	}
}

func TestOpenDesignWorkspaceFailureStillReadsBilling(t *testing.T) {
	f, p, current := newDesignMock(t)
	f.set("/api/v1/workspaces/current", designReply{401, `{"error":"unauthenticated"}`, false})
	_ = p.RefreshNow(context.Background(), current.Load().Accounts[0])
	s := p.Get("od1")
	if !s.AuthFailed || s.OpenDesign.Plan.State != "workspace-unconfirmed" || s.OpenDesign.Billing.State != "ok" {
		t.Fatalf("workspace failure: %+v", s)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if strings.Contains(call, "/ws1/") {
			t.Fatalf("unconfirmed workspace reused: %s", call)
		}
	}
}

func TestOpenDesignNoControlAndExpiredWindowsAreUnknown(t *testing.T) {
	_, p, current := newDesignMock(t)
	cfg := current.Load().Clone()
	cfg.Accounts[0].ControlKey = ""
	current.Store(cfg)
	if err := p.RefreshNow(context.Background(), cfg.Accounts[0]); err != nil {
		t.Fatal(err)
	}
	s := p.Get("od1")
	d := s.OpenDesignDiagnosis(time.Now(), time.Hour)
	if d.PlanFresh || d.WalletFresh || d.PlanStatus != "unknown" || s.OpenDesign.Plan.State != "missing-key" || d.CatalogFresh == false {
		t.Fatalf("no control: %+v", d)
	}
	now := time.Now()
	s.OpenDesign.Plan = ReadResult{State: "ok", CheckedAt: now}
	s.PlanAt, s.Eligible = now, true
	s.Windows = []Window{{Limit: 100, Remaining: 0, ResetsAt: now.Add(-time.Second)}}
	if d = s.OpenDesignDiagnosis(now, time.Hour); d.PlanFresh || d.PlanStatus != "unknown" {
		t.Fatal("expired window treated as replenished / current")
	}
	if s.Windows[0].EffectiveRemaining(now) != 100 {
		t.Fatal("display-only feature changed existing scheduler semantics")
	}
	old := &Snapshot{PlanAt: now, Eligible: true}
	if old.OpenDesignDiagnosis(now, time.Hour).PlanFresh {
		t.Fatal("old metadata invented new diagnostic evidence")
	}
}

func TestOpenDesignSourceChangeRejectsLateResults(t *testing.T) {
	_, p, current := newDesignMock(t)
	cfg := current.Load()
	a := cfg.Accounts[0]
	old := &Snapshot{OpenDesign: &OpenDesignState{Source: OpenDesignSource(cfg, a)}, WalletUSD: "7"}
	next := cfg.Clone()
	next.Accounts[0].ControlKey = "vela_ctrl_replaced"
	current.Store(next)
	p.store(a.ID, old)
	if p.Get(a.ID) != nil {
		t.Fatal("stale control-key results resurrected")
	}
	for _, change := range []func(*config.Config){
		func(c *config.Config) { c.Accounts[0].RuntimeKey = "sk-amr-replaced" },
		func(c *config.Config) { c.Accounts[0].ProxyURL = "http://127.0.0.1:1" },
		func(c *config.Config) { c.Upstream.ControlBase = "http://127.0.0.1:2" },
		func(c *config.Config) { c.Upstream.RuntimeBase = "http://127.0.0.1:3" },
	} {
		next = cfg.Clone()
		change(next)
		current.Store(next)
		p.store(a.ID, old)
		if p.Get(a.ID) != nil {
			t.Fatal("stale source accepted")
		}
	}
	current.Store(cfg)
	p.store(a.ID, old)
	data, _ := json.Marshal(p.All())
	var loaded map[string]*Snapshot
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	p.Load(loaded)
	if p.Get(a.ID).OpenDesign.Source != old.OpenDesign.Source || p.Get(a.ID).WalletUSD != "7" {
		t.Fatal("metadata round trip lost diagnostics")
	}
}

func TestOpenDesignMalformedSuccessKeepsPreviousAmounts(t *testing.T) {
	f, p, current := newDesignMock(t)
	a := current.Load().Accounts[0]
	if err := p.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	before := p.Get(a.ID)
	f.set("/api/v1/workspaces/ws1/billing/preflight", designReply{200, `{"codingPlan":{}}`, false})
	f.set("/api/v1/billing/summary", designReply{200, `{}`, false})
	f.set("/api/v1/workspaces/ws1/wallet/balance", designReply{200, `{"balanceUsd":"NaN"}`, false})
	if p.RefreshNow(context.Background(), a) == nil {
		t.Fatal("missing measurements reported successful")
	}
	s := p.Get(a.ID)
	d := s.OpenDesignDiagnosis(time.Now(), time.Hour)
	if d.PlanFresh || d.WalletFresh || d.BillingFresh || d.PlanStatus != "unknown" || d.WalletStatus != "unknown" {
		t.Fatalf("malformed success invented measurements: %+v", d)
	}
	if !s.PlanAt.Equal(before.PlanAt) || !s.WalletAt.Equal(before.WalletAt) || !s.SummaryAt.Equal(before.SummaryAt) || s.WalletUSD != before.WalletUSD {
		t.Fatal("malformed response overwrote last good values/timestamps")
	}
}

func TestOpenDesignDiagnosticRefusesRedirect(t *testing.T) {
	var forwarded atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
	defer foreign.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, foreign.URL, http.StatusFound) }))
	defer up.Close()
	cfg := config.Default()
	cfg.Upstream.RuntimeBase = up.URL
	a := config.Account{ID: "od", Provider: config.ProviderOpenDesign, RuntimeKey: "sk-amr-mock"}
	cfg.Accounts = []config.Account{a}
	p := NewPoller(func() *config.Config { return cfg }, func(string) *http.Client { return up.Client() })
	if p.RefreshNow(context.Background(), a) == nil || forwarded.Load() != 0 {
		t.Fatal("credential-bearing diagnostic followed redirect")
	}
}
