package thregister

import (
	"fmt"
	"testing"
	"time"
)

func TestPassUsageReadsObservedAllowanceWithoutInventingMonthlyBalance(t *testing.T) {
	body := fmt.Sprintf(`{"ok":true,"on_plan":true,"activated":true,"plan_used_pct":29,"plan_exhausted":false,"overage_billing":false,"period_ends_at":%q,"plan":{"name":"Agent Pass","window_days":7,"window_allowance_usd":2.5,"allowance_usd":10,"windows_per_month":4}}`, time.Now().Add(time.Hour).Format(time.RFC3339Nano))
	usage, err := parsePassUsage([]byte(body), "Agent Pass")
	if err != nil || usage.WeeklyLimitUSD == nil || *usage.WeeklyLimitUSD != 2.5 || usage.MonthlyLimitUSD == nil || *usage.MonthlyLimitUSD != 10 || usage.UsedPercent == nil || *usage.UsedPercent != 29 {
		t.Fatalf("allowance parse failed: %+v %v", usage, err)
	}
}
func TestOptionalAllowanceFailuresDoNotChangeWeeklyGate(t *testing.T) {
	for _, amounts := range []string{"", `,"window_allowance_usd":null,"allowance_usd":null`, `,"window_allowance_usd":-1,"allowance_usd":"not-a-number"`, `,"window_allowance_usd":20,"allowance_usd":10`, `,"window_allowance_usd":1e309,"allowance_usd":0`} {
		body := `{"ok":true,"on_plan":true,"activated":false,"plan_used_pct":0,"plan_exhausted":false,"overage_billing":false,"plan":{"name":"Agent Pass","window_days":7` + amounts + `}}`
		usage, err := parsePassUsage([]byte(body), "Agent Pass")
		if err != nil || usage.UsedPercent == nil || *usage.UsedPercent != 0 || usage.WeeklyLimitUSD != nil || usage.MonthlyLimitUSD != nil {
			t.Fatalf("optional amounts changed weekly proof: %+v %v", usage, err)
		}
	}
}
