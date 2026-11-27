package rules

import (
	"net/http"
	"testing"
	"time"
)

// The production refusal: HTTP 402 with Mistral's own
// billing_api_budget_exhausted / code 2303. It carries no time, so the rule
// must be the one that cools to the measured api_budget reset instead of the
// generic ten-minute 402 cooldown, and it must not quarantine.
func TestMistralApiBudgetExhaustedCoolsToReset(t *testing.T) {
	rs := DefaultMistralErrorRules()
	if err := ValidateErrorRules(rs); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"type":"billing_api_budget_exhausted","code":"2303","message":"API budget exhausted"}`,
		`{"code":2303}`,
		`{"errorCode":"2303"}`,
	} {
		r := MatchError(rs, 402, body, http.Header{})
		if r == nil || r.ID != "mistral-api-budget-exhausted" {
			t.Fatalf("%s matched %v, want mistral-api-budget-exhausted", body, r)
		}
		if r.Action != ActionCooldown || r.Scope != ScopeAccount {
			t.Fatalf("rule action/scope = %s/%s, want account cooldown (never quarantine)", r.Action, r.Scope)
		}
		if r.Reset == nil || !r.Reset.BudgetReset || r.Reset.Escalate {
			t.Fatalf("reset = %+v, want budgetReset without escalation", r.Reset)
		}
	}
	// Every other 402 keeps the original behaviour, including the generic
	// escalating cooldown, and a nested or textual 2303 is not the code.
	for _, body := range []string{`{"code":"other"}`, `{"message":"billing_api_budget_exhausted 2303"}`, `{"error":{"code":2303}}`, ``} {
		r := MatchError(rs, 402, body, http.Header{})
		if r == nil || r.ID != "mistral-insufficient-credits" {
			t.Fatalf("%q matched %v, want the generic 402 rule", body, r)
		}
		if r.Reset == nil || r.Reset.BudgetReset || !r.Reset.Escalate {
			t.Fatalf("generic 402 rule changed: %+v", r.Reset)
		}
	}
	if r := MatchError(rs, 429, `{"code":2303}`, http.Header{}); r == nil || r.ID != "mistral-rate-limited" {
		t.Fatalf("2303 on another status matched %v", r)
	}
}

func TestCooldownForUsesMeasuredResetOrLongestFallback(t *testing.T) {
	now := time.Now()
	r := &ErrorRule{ID: "x", Action: ActionCooldown, Reset: &Reset{Fallback: "10m", Max: "24h", BudgetReset: true}}
	reset := now.Add(9 * time.Hour)
	if d := CooldownFor(r, http.Header{"Retry-After": {"5"}}, "", now, 1, ResetBudget{Reset: reset}); d != 9*time.Hour {
		t.Fatalf("cooldown = %v, want the measured reset (the header must not shorten it)", d)
	}
	// No usable reset: the allowance may well be out for the whole period,
	// so the longest the rule allows is the honest guess.
	if d := CooldownFor(r, nil, "", now, 1, ResetBudget{}); d != 24*time.Hour {
		t.Fatalf("unknown reset cooldown = %v, want the 24h max", d)
	}
	// An already passed reset says nothing about the next window.
	if d := CooldownFor(r, nil, "", now, 1, ResetBudget{Reset: now.Add(-time.Minute)}); d != 24*time.Hour {
		t.Fatalf("expired reset cooldown = %v, want the 24h max", d)
	}
	// A longer fallback than max keeps the fallback.
	r.Reset.Fallback = "48h"
	if d := CooldownFor(r, nil, "", now, 1, ResetBudget{}); d != 48*time.Hour {
		t.Fatalf("fallback longer than max = %v", d)
	}
	// A rule without the flag is untouched.
	plain := &ErrorRule{Action: ActionCooldown, Reset: &Reset{Fallback: "10m", Max: "6h", Escalate: true}}
	if d := CooldownFor(plain, nil, "", now, 3, ResetBudget{Reset: reset}); d != 40*time.Minute {
		t.Fatalf("ordinary escalating cooldown = %v, want 40m", d)
	}
	if d := CooldownAfter(plain, nil, "", now, 3); d != CooldownFor(plain, nil, "", now, 3, ResetBudget{Reset: reset}) {
		t.Fatal("CooldownFor diverged from CooldownAfter for an ordinary rule")
	}
}

func TestBudgetResetValidation(t *testing.T) {
	base := func() []ErrorRule {
		return []ErrorRule{{ID: "r", Scope: ScopeAccount, Action: ActionCooldown,
			When: Match{Statuses: []int{402}}, Reset: &Reset{Fallback: "10m", Max: "24h", BudgetReset: true}}}
	}
	if err := ValidateErrorRules(base()); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Reset){
		"header":    func(r *Reset) { r.Header, r.Format = "Retry-After", "retry-after" },
		"bodyAfter": func(r *Reset) { r.BodyAfter = []string{"resets at"} },
	} {
		rs := base()
		mut(rs[0].Reset)
		if err := ValidateErrorRules(rs); err == nil {
			t.Fatalf("%s: budgetReset combined with another source was accepted", name)
		}
	}
}
