package perch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// accountServer answers /api/perchai/account with a real-shaped payload.
func accountServer(t *testing.T, body map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/perchai/account" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"ok":false,"error":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// entitlement builds one session.entitlements entry.
func entitlement(key string, val map[string]any) map[string]any {
	return map[string]any{"key": key, "value_json": val}
}

// A plan that STATES its cap: the entitlement wins, nothing is guessed.
func TestAccountStatedEntitlement(t *testing.T) {
	srv := accountServer(t, map[string]any{
		"ok": true,
		"session": map[string]any{
			"planCode": "pro", "planName": "Pro",
			"entitlements": []any{
				entitlement("usage.monthly_pt", map[string]any{"limit": 50000}),
				entitlement("usage.manual_monthly_pt", map[string]any{"limit": 12000}),
			},
		},
		"usageMeter":      map[string]any{"monthlyUsd": 12.5, "monthlyManualPt": 2000, "monthlyCalls": 30},
		"creditBalancePt": 4200,
	})
	c := NewClient(srv.Client())
	a, err := c.Account(context.Background(), srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if a.MonthlyLimitPT != 50000 || a.MonthlySource != AllowanceEntitlement {
		t.Fatalf("monthly cap = %d from %s", a.MonthlyLimitPT, a.MonthlySource)
	}
	if a.ManualLimitPT != 12000 || a.ManualSource != AllowanceEntitlement {
		t.Fatalf("manual cap = %d from %s", a.ManualLimitPT, a.ManualSource)
	}
	if a.CreditBalancePT != 4200 {
		t.Fatalf("credit = %d", a.CreditBalancePT)
	}
	if a.PlanLabel != "Pro" || a.PlanCode != "pro" {
		t.Fatalf("plan = %q/%q", a.PlanLabel, a.PlanCode)
	}
}

// A Starter plan that states no cap gets Perch's own 20,000 PT fallback —
// labelled as a fallback, never presented as an entitlement.
func TestAccountStarterFallback(t *testing.T) {
	srv := accountServer(t, map[string]any{
		"ok": true,
		"session": map[string]any{
			"planCode": "pilot", "planName": "pilot",
			"entitlements": []any{entitlement("usage.monthly_pt", map[string]any{"limit": nil})},
		},
		"usageMeter": map[string]any{"monthlyUsd": 3},
	})
	c := NewClient(srv.Client())
	a, err := c.Account(context.Background(), srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if a.MonthlyLimitPT != StarterMonthlyPT || a.MonthlySource != AllowancePlanFallback {
		t.Fatalf("cap = %d from %s, want the Starter fallback", a.MonthlyLimitPT, a.MonthlySource)
	}
	if a.PlanLabel != "Starter" {
		t.Fatalf("pilot should label as Starter, got %q", a.PlanLabel)
	}
}

// A plan that states nothing and is not Starter gets NO cap: the console then
// says the headroom is unknown instead of inventing one.
func TestAccountUnknownStaysUnknown(t *testing.T) {
	srv := accountServer(t, map[string]any{
		"ok":      true,
		"session": map[string]any{"planCode": "weird_tier", "planName": "Weird", "entitlements": []any{}},
	})
	c := NewClient(srv.Client())
	a, err := c.Account(context.Background(), srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if a.MonthlyLimitPT != 0 || a.MonthlySource != AllowanceNone {
		t.Fatalf("an unstated cap must stay unknown, got %d from %s", a.MonthlyLimitPT, a.MonthlySource)
	}
}

// Ultra's manual cap comes from the plan-code fallback.
func TestAccountUltraManualFallback(t *testing.T) {
	srv := accountServer(t, map[string]any{
		"ok":      true,
		"session": map[string]any{"planCode": "ultra", "planName": "Ultra", "entitlements": []any{}},
	})
	c := NewClient(srv.Client())
	a, err := c.Account(context.Background(), srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if a.ManualLimitPT != 225000 || a.ManualSource != AllowancePlanFallback {
		t.Fatalf("manual cap = %d from %s", a.ManualLimitPT, a.ManualSource)
	}
}

// An internal/staff account is unlimited.
func TestAccountUnlimited(t *testing.T) {
	srv := accountServer(t, map[string]any{
		"ok": true,
		"session": map[string]any{
			"planCode": "pro", "membershipRole": "admin",
			"entitlements": []any{entitlement("usage.monthly_pt", map[string]any{"limit": 1000})},
		},
	})
	c := NewClient(srv.Client())
	a, err := c.Account(context.Background(), srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if !a.Unlimited {
		t.Fatal("an admin account is unlimited")
	}
}

// The roost entitlement is surfaced.
func TestAccountRoostUnlimited(t *testing.T) {
	srv := accountServer(t, map[string]any{
		"ok": true,
		"session": map[string]any{"planCode": "ultra",
			"entitlements": []any{entitlement("usage.roost", map[string]any{"unlimited": true})}},
	})
	c := NewClient(srv.Client())
	a, err := c.Account(context.Background(), srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if !a.RoostUnlimited {
		t.Fatal("usage.roost unlimited was not read")
	}
}

// A refused token is an auth error, so the pool renews instead of cooling.
func TestAccountAuthFailure(t *testing.T) {
	srv := accountServer(t, map[string]any{"ok": true})
	c := NewClient(srv.Client())
	_, err := c.Account(context.Background(), srv.URL, "wrong")
	if err == nil || KindOf(err) != KindAuth {
		t.Fatalf("err = %v (%s)", err, KindOf(err))
	}
}

// The account route states PT directly; those figures are exact and win over
// a USD conversion.
func TestAccountExactPTFigures(t *testing.T) {
	srv := accountServer(t, map[string]any{
		"ok": true,
		"session": map[string]any{"planCode": "pro",
			"entitlements": []any{entitlement("usage.monthly_pt", map[string]any{"limit": 75000})}},
		"usageMeter": map[string]any{
			"monthlyUsd": 12.0, "monthlyPt": 12000, "monthlyManualPt": 2000,
			"dailyPt": 500, "weeklyPt": 3000,
		},
	})
	c := NewClient(srv.Client())
	a, err := c.Account(context.Background(), srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if !a.Meter.MonthlyPTKnown || a.Meter.MonthlyPTExact != 12000 {
		t.Fatalf("monthly PT = %d known=%v", a.Meter.MonthlyPTExact, a.Meter.MonthlyPTKnown)
	}
	if !a.Meter.ManualPTKnown || a.Meter.ManualPTExact != 2000 {
		t.Fatalf("manual PT = %d", a.Meter.ManualPTExact)
	}
	if a.Meter.MonthlyUSD != 12.0 {
		t.Fatalf("monthlyUsd = %v", a.Meter.MonthlyUSD)
	}
}

// A brand-new account has not picked a plan: Perch states no plan code and an
// EMPTY entitlement list, and planName is null — which the label logic renders
// as "Starter". Claiming the free-tier allowance for it would dress a guess as
// a fact, so it must stay uncapped and be flagged instead.
func TestAccountAwaitingTierSelectionHasNoCap(t *testing.T) {
	srv := accountServer(t, map[string]any{
		"ok": true,
		"session": map[string]any{
			"planCode": nil, "planName": nil, "membershipRole": nil,
			"tierSelectionRequired": true,
			"entitlements":          []any{},
		},
		"usageMeter": nil,
	})
	c := NewClient(srv.Client())
	a, err := c.Account(context.Background(), srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if !a.TierSelectionRequired {
		t.Fatal("tierSelectionRequired was not read")
	}
	if a.MonthlyLimitPT != 0 || a.MonthlySource != AllowanceNone {
		t.Fatalf("an account with no plan must have no cap, got %d from %s", a.MonthlyLimitPT, a.MonthlySource)
	}
}

// Once the owner picks Starter, Perch states the cap and the flag clears.
func TestAccountTierSelectedUsesStatedCap(t *testing.T) {
	srv := accountServer(t, map[string]any{
		"ok": true,
		"session": map[string]any{
			"planCode": "pilot", "planName": "Pilot",
			"tierSelectionRequired": false,
			"entitlements":          []any{entitlement("usage.monthly_pt", map[string]any{"limit": 20000})},
		},
		"usageMeter": map[string]any{"monthlyUsd": 3},
	})
	c := NewClient(srv.Client())
	a, err := c.Account(context.Background(), srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if a.TierSelectionRequired {
		t.Fatal("a selected plan must not stay flagged")
	}
	if a.MonthlyLimitPT != 20000 || a.MonthlySource != AllowanceEntitlement {
		t.Fatalf("cap = %d from %s, want Perch's stated 20000", a.MonthlyLimitPT, a.MonthlySource)
	}
}

// Perch's own free-tier figure outranks the compiled-in constant, so the
// console follows Perch when the free plan changes.
func TestStarterCapPrefersPerchFigure(t *testing.T) {
	a := Account{PlanCode: "pilot", PlanName: "Pilot"}
	a.StarterMonthlyLimits(25)
	a.ResolveAllowances()
	if a.MonthlyLimitPT != 25_000 || a.MonthlySource != AllowancePlanFallback {
		t.Fatalf("cap = %d from %s, want 25000 (Perch's $25), not the constant", a.MonthlyLimitPT, a.MonthlySource)
	}

	// with nothing read, the published constant is still better than nothing
	b := Account{PlanCode: "pilot", PlanName: "Pilot"}
	b.ResolveAllowances()
	if b.MonthlyLimitPT != StarterMonthlyPT {
		t.Fatalf("cap = %d, want the %d constant", b.MonthlyLimitPT, StarterMonthlyPT)
	}
}

// starter-limits is read with the account's access token (the billing routes
// take a browser cookie instead), and it is what supplies the real figure.
func TestStarterMonthlyUSD(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/perch-terminal/starter-limits" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"ok":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"view":"public","config":{"monthlyUsd":20}}`))
	}))
	defer srv.Close()

	usd, err := NewClient(srv.Client()).StarterMonthlyUSD(context.Background(), srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if usd != 20 {
		t.Fatalf("monthlyUsd = %v, want 20", usd)
	}
	if _, err := NewClient(srv.Client()).StarterMonthlyUSD(context.Background(), srv.URL, "wrong"); err == nil {
		t.Fatal("a refused token must be an error, not a silent zero")
	}
}
