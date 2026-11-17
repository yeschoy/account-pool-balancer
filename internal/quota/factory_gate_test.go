package quota

import (
	"account-pool-balancer/internal/config"
	"testing"
	"time"
)

func TestFactoryQuotaGateStandardCoreAndUnknown(t *testing.T) {
	now := time.Now()
	c := config.Default()
	allowed := true
	balance := 0.0
	f := &FactoryState{QuotaState: "ok", QuotaAt: now, ExtraAllowed: &allowed, ExtraBalanceCents: &balance, Windows: []FactoryWindow{{Pool: "standard", Name: "weekly", UsedPercent: 100, Active: true, ResetsAt: now.Add(time.Hour)}, {Pool: "core", Name: "weekly", UsedPercent: 0, Active: false}}}
	if !f.Exhausted(c, "gpt-5.4", now) || f.Exhausted(c, "deepseek-v4.1-flash", now) {
		t.Fatal("wrong Standard/Core classification")
	}
	if !f.Exhausted(c, "muse-spark-1.2", now) {
		t.Fatal("hosting vendor mistaken for subscription meter")
	}
	c.Factory.StandardOnly = true
	if !f.Exhausted(c, "deepseek-v4.1-flash", now) {
		t.Fatal("standard-only ineffective")
	}
	balance = 100
	if !f.Exhausted(c, "deepseek-v4.1-flash", now) {
		t.Fatal("extra bypassed explicit standard-only")
	}
	c.Factory.StandardOnly = false
	if f.Exhausted(c, "gpt-5.4", now) {
		t.Fatal("existing allowed Extra balance ignored")
	}
	balance = 0
	f.Windows[0].ResetsAt = now.Add(-time.Second)
	if f.Exhausted(c, "gpt-5.4", now) {
		t.Fatal("expired snapshot treated as active window")
	}
	f.Windows[0].ResetsAt = now.Add(time.Hour)
	f.QuotaState = "failed"
	if f.Exhausted(c, "gpt-5.4", now) {
		t.Fatal("failure treated as zero")
	}
}
