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
