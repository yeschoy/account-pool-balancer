package config

import "testing"

func TestLogRetentionDefaultsSevenDaysAndPreservesConfiguredValue(t *testing.T) {
	c := Default()
	if c.Logs.RetentionDays != 7 || c.Perch.LogRetentionDays != 7 {
		t.Fatal("new configurations must default to seven days")
	}
	for _, days := range []int{1, 7, 30, 365} {
		c := Default()
		c.Logs.RetentionDays = days
		if err := c.Normalize(); err != nil || c.Logs.RetentionDays != days {
			t.Fatalf("explicit retention %d was changed: %v", days, err)
		}
	}
	for _, days := range []int{-1, 366} {
		c := Default()
		c.Logs.RetentionDays = days
		if err := c.Normalize(); err == nil {
			t.Fatal("invalid retention accepted", days)
		}
	}
	c = Default()
	c.Logs.RetentionDays = 0
	if err := c.Normalize(); err != nil || c.Logs.RetentionDays != 7 {
		t.Fatal("legacy missing retention did not receive the new default", err)
	}
}
