package config

import (
	"reflect"
	"testing"

	"account-pool-balancer/internal/rules"
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

// TestMistralApiBudgetRulePreservesOperatorRules: the 2303 rule is an
// addition to the factory list, so only an unmodified list may gain it. Any
// administrator edit — of these six shipped shapes and of the tier/capacity
// list — keeps its rules, order, notes and disabled flags exactly.
func TestMistralApiBudgetRulePreservesOperatorRules(t *testing.T) {
	type variant struct {
		name   string
		errors []rules.ErrorRule
		retry  []rules.RetryRule
	}
	var variants []variant
	base, retries := shippedMistralTierCapacityPolicy(t)
	mutate := func(name string, f func([]rules.ErrorRule) []rules.ErrorRule) {
		variants = append(variants, variant{name, f(append([]rules.ErrorRule(nil), base...)), retries})
	}
	mutate("note", func(rs []rules.ErrorRule) []rules.ErrorRule { rs[4].Note = "operator notes"; return rs })
	mutate("order", func(rs []rules.ErrorRule) []rules.ErrorRule {
		rs[4], rs[5] = rs[5], rs[4]
		return rs
	})
	mutate("disabled-402", func(rs []rules.ErrorRule) []rules.ErrorRule { rs[4].Disabled = true; return rs })
	mutate("custom-402-reset", func(rs []rules.ErrorRule) []rules.ErrorRule {
		rs[4].Reset = &rules.Reset{Fallback: "1h"}
		return rs
	})
	mutate("custom-2303-first", func(rs []rules.ErrorRule) []rules.ErrorRule {
		return append([]rules.ErrorRule{{ID: "operator-2303", Scope: rules.ScopeAccount, Action: rules.ActionHardQuarantine,
			When: rules.Match{Statuses: []int{402}, BodyCodes: []string{"2303"}}}}, rs...)
	})
	mutate("empty", func([]rules.ErrorRule) []rules.ErrorRule { return []rules.ErrorRule{} })
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			c := Default()
			c.MistralErrorRules, c.MistralRetryRules = v.errors, v.retry
			before := c.Clone()
			if err := c.Normalize(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(c.MistralErrorRules, before.MistralErrorRules) {
				t.Fatal("overwrote customized error rules")
			}
			if !reflect.DeepEqual(c.MistralRetryRules, before.MistralRetryRules) {
				t.Fatal("overwrote customized retry rules")
			}
		})
	}
}

// TestMistralApiBudgetRuleDoesNotLeakToOtherPools: the 2303 rule and its
// budgetReset reset are Mistral's only.
func TestMistralApiBudgetRuleDoesNotLeakToOtherPools(t *testing.T) {
	c := Default()
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{ProviderOpenDesign, ProviderRiver, ProviderOllama, ProviderModal, ProviderPerch,
		ProviderTokenHarbor, ProviderOpenCodeZen, ProviderOpenCodeGo, ProviderX50, ProviderFactory, ProviderDeploy, ProviderALTAPI, ProviderModelStudio} {
		errors, retries := c.RulesFor(p)
		for _, r := range errors {
			if r.ID == "mistral-api-budget-exhausted" || r.Reset != nil && r.Reset.BudgetReset {
				t.Fatalf("%s gained the Mistral api_budget rule: %+v", p, r)
			}
		}
		if r := rules.MatchError(errors, 402, `{"code":"2303"}`, nil); r != nil && r.ID == "mistral-api-budget-exhausted" {
			t.Fatalf("%s matched the Mistral rule", p)
		}
		for _, r := range retries {
			if r.ID == "mistral-backend-out-of-capacity" {
				t.Fatalf("%s gained a Mistral retry rule", p)
			}
		}
	}
}
