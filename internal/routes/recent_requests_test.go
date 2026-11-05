package routes

import (
	"math"
	"testing"
)

func TestRecentRequestsOrderPriorityPendingAndTies(t *testing.T) {
	seats := []Seat{
		{ID: "busy", Priority: 50, RecentRequests1H: 5, Active: 1},
		{ID: "pending", Priority: 50, RecentRequests1H: 4, PendingRequests: 2},
		{ID: "old", Priority: 50, RecentRequests1H: 5, Count: 9999, LastSelected: 1},
		{ID: "new", Priority: 50, RecentRequests1H: 5, LastSelected: 3},
		{ID: "priority", Priority: 10, RecentRequests1H: 1000},
	}
	got := ids(seats, Order("least-requests-1h", "go", seats, Params{}))
	want := []string{"priority", "old", "new", "busy", "pending"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recent order=%v, want %v", got, want)
		}
	}
	// Lifetime count must not overrule equal recent usage. Equal historical
	// sequence/active/load still rotates rather than pinning input order.
	equal := []Seat{{ID: "a", Count: 99999}, {ID: "b"}, {ID: "c", Count: 7}}
	seen := map[string]int{}
	for cursor := int64(0); cursor < 3; cursor++ {
		seen[ids(equal, Order("least-requests-1h", "go", equal, Params{Cursor: cursor}))[0]]++
	}
	for _, s := range equal {
		if seen[s.ID] != 1 {
			t.Fatalf("recent ties did not rotate: %v", seen)
		}
	}
	extreme := []Seat{{ID: "overflow", RecentRequests1H: math.MaxInt64, PendingRequests: 1}, {ID: "lower", RecentRequests1H: math.MaxInt64}}
	if ids(extreme, Order("least-requests-1h", "go", extreme, Params{}))[0] != "lower" {
		t.Fatal("projected load overflowed")
	}
}
