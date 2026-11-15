package newapiconsole

import (
	_ "embed"
	"encoding/json"
	"testing"
)

// casesJSON is the shared table also read by tools/test_altapi_registrar.py.
//
// The uid and cookie rules are implemented twice on purpose — the registrar is
// a standalone Python subprocess and cannot import this package — so the only
// way to keep them from drifting is to assert both languages against one
// literal table. A rule change that updates just one language fails here or
// there, never silently.
//
//go:embed cases.json
var casesJSON []byte

type uidCase struct {
	In  string `json:"in"`
	Out string `json:"out"`
	Why string `json:"why"`
}

type balanceCase struct {
	Quota float64 `json:"quota"`
	Unit  float64 `json:"unit"`
	Out   float64 `json:"out"`
	Why   string  `json:"why"`
}

type caseTable struct {
	NormalizeUID  []uidCase     `json:"normalize_uid"`
	SessionValue  []uidCase     `json:"session_value"`
	BalanceUSD    []balanceCase `json:"balance_usd"`
}

func loadCases(t *testing.T) caseTable {
	t.Helper()
	var c caseTable
	if err := json.Unmarshal(casesJSON, &c); err != nil {
		t.Fatalf("cases.json: %v", err)
	}
	if len(c.NormalizeUID) == 0 || len(c.SessionValue) == 0 || len(c.BalanceUSD) == 0 {
		t.Fatal("cases.json is missing a section")
	}
	return c
}

func TestNormalizeUIDCases(t *testing.T) {
	for _, tc := range loadCases(t).NormalizeUID {
		if got := NormalizeUID(tc.In); got != tc.Out {
			t.Errorf("NormalizeUID(%q) = %q, want %q (%s)", tc.In, got, tc.Out, tc.Why)
		}
	}
}

func TestSessionValueCases(t *testing.T) {
	for _, tc := range loadCases(t).SessionValue {
		if got := SessionValue(tc.In); got != tc.Out {
			t.Errorf("SessionValue(%q) = %q, want %q (%s)", tc.In, got, tc.Out, tc.Why)
		}
	}
}

func TestBalanceUSDCases(t *testing.T) {
	for _, tc := range loadCases(t).BalanceUSD {
		if got := BalanceUSD(tc.Quota, tc.Unit); got != tc.Out {
			t.Errorf("BalanceUSD(%v, %v) = %v, want %v (%s)", tc.Quota, tc.Unit, got, tc.Out, tc.Why)
		}
	}
}
