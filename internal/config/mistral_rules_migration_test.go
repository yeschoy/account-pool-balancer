package config

import (
	"reflect"
	"testing"
)

func TestMistralRulesUpgradeOnlyFactoryDefaults(t *testing.T) {
	for _, custom := range []bool{false, true} {
		c := Default()
		c.MistralErrorRules, c.MistralRetryRules = shippedMistralPolicy(t, true)
		if custom {
			c.MistralErrorRules[0].Note = "operator customization"
			c.MistralRetryRules[0].Note = "operator customization"
		}
		before := c.Clone()
		if err := c.Normalize(); err != nil {
			t.Fatal(err)
		}
		if custom {
			if !reflect.DeepEqual(c.MistralErrorRules, before.MistralErrorRules) || !reflect.DeepEqual(c.MistralRetryRules, before.MistralRetryRules) {
				t.Fatal("overwrote administrator rules")
			}
		} else if c.MistralErrorRules[0].ID != "mistral-service-unavailable" || c.MistralRetryRules[0].ID != "mistral-service-unavailable" {
			t.Fatal("unmodified legacy defaults did not gain the service-error policy")
		}
	}
}
