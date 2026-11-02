package routes

import (
	"testing"
	"time"
)

func ids(seats []Seat, order []int) []string {
	out := make([]string, len(order))
	for i, o := range order {
		out[i] = seats[o].ID
	}
	return out
}

func TestSmartSoonestResetFirstAndTiers(t *testing.T) {
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	seats := []Seat{
		{ID: "late", Priority: 50, Known: true, Used: 10, Soon: []time.Time{now.Add(96 * time.Hour)}},
		{ID: "soon", Priority: 50, Known: true, Used: 50, Soon: []time.Time{now.Add(5 * time.Hour)}},
		{ID: "low", Priority: 50, Known: true, Used: 93, Soon: []time.Time{now.Add(time.Hour)}},
		{ID: "unknown", Priority: 50},
		{ID: "wallet", Priority: 50, Known: true, Used: 100, Fallback: 2},
		{ID: "sub", Priority: 50, Known: true, Used: 100, Fallback: 1, Soon: []time.Time{now.Add(240 * time.Hour)}},
		{ID: "spent", Priority: 50, Known: true, Used: 99, Soon: []time.Time{now.Add(time.Hour)}},
	}
	got := ids(seats, Order("smart", "p", seats, Params{LowShare: 90, SpentShare: 98}))
	want := []string{"soon", "late", "unknown", "low", "spent", "sub", "wallet"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("smart order = %v, want %v", got, want)
		}
	}
}

func TestPriorityGroupsFirst(t *testing.T) {
	seats := []Seat{{ID: "b", Priority: 60}, {ID: "a", Priority: 10, Count: 100}}
	got := ids(seats, Order("least-selections", "p", seats, Params{}))
	if got[0] != "a" {
		t.Fatalf("priority 10 must come first, got %v", got)
	}
}

func TestWeightedSmooth(t *testing.T) {
	Forget()
	seats := []Seat{{ID: "a", Priority: 1, Weight: 3}, {ID: "b", Priority: 1, Weight: 1}}
	n := map[string]int{}
	for range 8 {
		n[ids(seats, Order("weighted", "w", seats, Params{}))[0]]++
	}
	if n["a"] != 6 || n["b"] != 2 {
		t.Fatalf("weighted 3:1 over 8 = %v", n)
	}
}

func TestLeastSelectionsSpreads(t *testing.T) {
	seats := []Seat{{ID: "a", Priority: 1, Count: 5}, {ID: "b", Priority: 1, Count: 2}, {ID: "c", Priority: 1, Count: 2, Active: 1}}
	got := ids(seats, Order("least-selections", "p", seats, Params{}))
	if got[0] != "b" || got[2] != "a" {
		t.Fatalf("least-selections = %v", got)
	}
}

// Accounts with no quota snapshot leave every quota term equal; the order
// must still rotate with the cursor instead of repeating the input order.
func TestSmartUnknownAccountsRotate(t *testing.T) {
	seats := []Seat{
		{ID: "m1", Priority: 50},
		{ID: "m2", Priority: 50},
		{ID: "m3", Priority: 50},
		{ID: "m4", Priority: 50},
	}
	first := map[string]int{}
	for cursor := int64(0); cursor < 4; cursor++ {
		got := ids(seats, Order("smart", "modal", seats, Params{Cursor: cursor}))
		first[got[0]]++
		if len(got) != len(seats) {
			t.Fatalf("order lost seats: %v", got)
		}
	}
	for _, id := range []string{"m1", "m2", "m3", "m4"} {
		if first[id] != 1 {
			t.Fatalf("over 4 cursors each account must lead once, got %v", first)
		}
	}
}

// pace has the same blind spot: no pace, no tokens, nothing separates the
// accounts, so the cursor has to.
func TestPaceUnknownAccountsRotate(t *testing.T) {
	seats := []Seat{{ID: "a", Priority: 1}, {ID: "b", Priority: 1}, {ID: "c", Priority: 1}}
	first := map[string]int{}
	for cursor := int64(0); cursor < 3; cursor++ {
		got := ids(seats, Order("pace", "p", seats, Params{Cursor: cursor}))
		first[got[0]]++
	}
	for _, id := range []string{"a", "b", "c"} {
		if first[id] != 1 {
			t.Fatalf("pace must rotate, got %v", first)
		}
	}
}

// A known account still outranks an unknown one: the fix must not turn
// quota-aware ordering into plain round robin.
func TestSmartKnownBeforeUnknown(t *testing.T) {
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	seats := []Seat{
		{ID: "unknown", Priority: 50},
		{ID: "known", Priority: 50, Known: true, Used: 10, Soon: []time.Time{now.Add(time.Hour)}},
	}
	got := ids(seats, Order("smart", "p", seats, Params{}))
	if got[0] != "known" {
		t.Fatalf("a known account must come first, got %v", got)
	}
}
