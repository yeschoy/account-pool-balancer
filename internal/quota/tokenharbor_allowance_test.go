package quota

import (
	"context"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func TestTokenHarborAllowancePointersAreDetachedAndDoNotChangeGate(t *testing.T) {
	state, _ := fakeTHObservation(context.Background(), nil, config.Account{})
	now := time.Now()
	state.Source = "source"
	state.CheckedAt = now
	state.ValidUntil = now.Add(time.Minute)
	if state.Gate(now) != "" {
		t.Fatal("missing optional money fields changed weekly authorization")
	}
	week, month := 2.5, 10.0
	state.Usage.WeeklyLimitUSD = &week
	state.Usage.MonthlyLimitUSD = &month
	clone := state.clone()
	*clone.Usage.WeeklyLimitUSD = 99
	*clone.Usage.MonthlyLimitUSD = 100
	if *state.Usage.WeeklyLimitUSD != 2.5 || *state.Usage.MonthlyLimitUSD != 10 {
		t.Fatal("allowance pointers leaked between quota snapshots")
	}
	if state.Gate(now) != "" {
		t.Fatal("display-only allowances changed authorization")
	}
}
