package quota

import (
	"testing"
	"time"

	"account-pool-balancer/internal/newapiconsole"
)

// The console's three cards are exactly these derivations. Verified against a
// live altapi.ai account on 2026-11-15: a fresh account showed $10.00 / $0.00
// / 0, i.e. quota=5000000, used_quota=0, request_count=0 with
// quota_per_unit=500000.
func TestALTAPIBalanceDerivation(t *testing.T) {
	s := &Snapshot{ALTAPI: &ALTAPIState{
		Quota:        5000000,
		UsedQuota:    0,
		QuotaPerUnit: 500000,
		RequestCount: 0,
		Source:       "session",
		ReadAt:       time.Now().Format(time.RFC3339),
	}}
	// What altapiReadConsole computes:
	s.ALTAPI.BalanceUSD = newapiconsole.BalanceUSD(s.ALTAPI.Quota, s.ALTAPI.QuotaPerUnit)
	s.ALTAPI.UsedUSD = newapiconsole.BalanceUSD(s.ALTAPI.UsedQuota, s.ALTAPI.QuotaPerUnit)

	if s.ALTAPI.BalanceUSD != 10 {
		t.Fatalf("balance %v, want 10", s.ALTAPI.BalanceUSD)
	}
	if s.ALTAPI.UsedUSD != 0 {
		t.Fatalf("used %v, want 0", s.ALTAPI.UsedUSD)
	}
}

// A partially spent account: 4992102 / 500000 = $9.9842, and 7898 / 500000 =
// $0.0158 — the exact pair measured on uid 242.
func TestALTAPIBalanceDerivationAfterSpending(t *testing.T) {
	bal := newapiconsole.BalanceUSD(4992102, 500000)
	used := newapiconsole.BalanceUSD(7898, 500000)
	if bal != 9.9842 {
		t.Fatalf("balance %v, want 9.9842", bal)
	}
	if used != 0.0158 {
		t.Fatalf("used %v, want 0.0158", used)
	}
}

// A zero divisor must not divide by zero: the measured 500000 is used.
func TestALTAPIBalanceFallsBackOnZeroDivisor(t *testing.T) {
	if got := newapiconsole.BalanceUSD(5000000, 0); got != 10 {
		t.Fatalf("balance %v, want 10", got)
	}
}

// The inference key cannot read a balance. When only a key is bound the pool
// must say "unknown", never "0" — 0 would look like an empty account.
func TestALTAPINoSessionIsUnknownNotZero(t *testing.T) {
	s := &Snapshot{ALTAPI: &ALTAPIState{Source: "key", BalanceUSD: -1, UsedUSD: 0.02}}
	d := s.ALTAPIDiagnosis(time.Now())
	if d.Source != "key" {
		t.Fatalf("source %q", d.Source)
	}
	if d.BalanceUSD == 0 {
		t.Fatal("balance reported as 0 for a key-only account")
	}
}

// The poller and the console read share one normalizer now, so a pasted
// DevTools cookie string and a bare value both arrive as the bare value.
func TestALTAPISessionValueIsShared(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"abc123", "abc123"},
		{"session=abc123", "abc123"},
		{"other=x; session=abc123; more=y", "abc123"},
		{"has spaces here", ""},
		{"nope=1; x=2", ""},
	}
	for _, tc := range cases {
		if got := newapiconsole.SessionValue(tc.in); got != tc.want {
			t.Fatalf("SessionValue(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := newapiconsole.CookieHeader("abc123"); got != "session=abc123" {
		t.Fatalf("CookieHeader = %q", got)
	}
}
