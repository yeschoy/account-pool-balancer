package config

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"account-pool-balancer/internal/rules"
)

// Frozen from the actually shipped 4cc0593 defaults, not derived from the new
// rule tables. Notes, ordering, disabled flags and explicit empty lists count.
func shippedALTAPIRules(t *testing.T) ([]rules.ErrorRule, []rules.RetryRule) {
	t.Helper()
	return frozenALTAPIRules(t, "v1")
}

func frozenALTAPIRules(t *testing.T, version string) ([]rules.ErrorRule, []rules.RetryRule) {
	t.Helper()
	b, err := os.ReadFile("testdata/altapi_rules_" + version + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Errors  []rules.ErrorRule `json:"errors"`
		Retries []rules.RetryRule `json:"retries"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Errors, doc.Retries
}

func TestALTAPIRulesUpgradeFactoryDefaults(t *testing.T) {
	for _, version := range []string{"shipped", "v2", "nil", "current"} {
		t.Run(version, func(t *testing.T) {
			c := Default()
			if version == "shipped" {
				c.ALTAPIErrorRules, c.ALTAPIRetryRules = shippedALTAPIRules(t)
			}
			if version == "v2" {
				c.ALTAPIErrorRules, c.ALTAPIRetryRules = frozenALTAPIRules(t, "v2")
			}
			if version == "nil" {
				c.ALTAPIErrorRules, c.ALTAPIRetryRules = nil, nil
			}
			before := c.Clone()
			if err := c.Normalize(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(c.ALTAPIErrorRules, rules.DefaultALTAPIErrorRules()) || !reflect.DeepEqual(c.ALTAPIRetryRules, rules.DefaultALTAPIRetryRules()) {
				t.Fatal("legacy factory rules not upgraded")
			}
			if r := rules.MatchError(c.ALTAPIErrorRules, 401, `{"error":{"message":"Invalid API key provided","type":"authentication_error"}}`, nil); r == nil || r.ID != "altapi-token-invalid" {
				t.Fatal("official key error not installed")
			}
			for _, provider := range []string{ProviderOpenDesign, ProviderRiver, ProviderOllama, ProviderModal, ProviderDeploy, ProviderX50, ProviderMistral, ProviderTokenHarbor, ProviderOpenCodeGo, ProviderOpenCodeZen} {
				a, b := before.RulesFor(provider)
				x, y := c.RulesFor(provider)
				if !reflect.DeepEqual(a, x) || !reflect.DeepEqual(b, y) {
					t.Fatalf("changed %s rules", provider)
				}
			}
			wire, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			var restored Config
			if err := json.Unmarshal(wire, &restored); err != nil {
				t.Fatal(err)
			}
			if err := restored.Normalize(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(c.ALTAPIErrorRules, restored.ALTAPIErrorRules) || !reflect.DeepEqual(c.ALTAPIRetryRules, restored.ALTAPIRetryRules) {
				t.Fatal("round-trip changed rules")
			}
		})
	}
}

func TestALTAPIQuotaSwitchPreservesCustomizedV2(t *testing.T) {
	for _, mutation := range []string{"note", "order", "disabled", "reset", "body", "append", "empty"} {
		t.Run(mutation, func(t *testing.T) {
			c := Default()
			c.ALTAPIErrorRules, c.ALTAPIRetryRules = frozenALTAPIRules(t, "v2")
			switch mutation {
			case "note":
				c.ALTAPIErrorRules[5].Note = "operator quota note"
			case "order":
				c.ALTAPIErrorRules[4], c.ALTAPIErrorRules[5] = c.ALTAPIErrorRules[5], c.ALTAPIErrorRules[4]
			case "disabled":
				c.ALTAPIErrorRules[5].Disabled = true
			case "reset":
				c.ALTAPIErrorRules[5].Reset.Fallback = "3m"
			case "body":
				c.ALTAPIErrorRules[5].When.BodyContains = []string{"operator quota"}
			case "append":
				c.ALTAPIErrorRules = append(c.ALTAPIErrorRules, rules.ErrorRule{ID: "operator", Scope: rules.ScopeAccount, Action: rules.ActionIgnore, When: rules.Match{Statuses: []int{418}}})
			case "empty":
				c.ALTAPIErrorRules = []rules.ErrorRule{}
			}
			before := c.Clone()
			if err := c.Normalize(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(c.ALTAPIErrorRules, before.ALTAPIErrorRules) {
				t.Fatal("overwrote customized v2 errors")
			}
		})
	}
}

func TestALTAPIRulesUpgradePreservesCustomLists(t *testing.T) {
	for _, mutation := range []string{"note", "order", "disabled", "reset", "body", "header", "append", "empty", "retry-note", "retry-order", "retry-disabled", "retry-body", "retry-empty"} {
		t.Run(mutation, func(t *testing.T) {
			c := Default()
			c.ALTAPIErrorRules, c.ALTAPIRetryRules = shippedALTAPIRules(t)
			errorsChanged, retriesChanged := true, false
			switch mutation {
			case "note":
				c.ALTAPIErrorRules[0].Note = "operator note"
			case "order":
				c.ALTAPIErrorRules[0], c.ALTAPIErrorRules[1] = c.ALTAPIErrorRules[1], c.ALTAPIErrorRules[0]
			case "disabled":
				c.ALTAPIErrorRules[0].Disabled = true
			case "reset":
				c.ALTAPIErrorRules[6].Reset.Fallback = "3m"
			case "body":
				c.ALTAPIErrorRules[0].When.BodyContains = []string{"operator body"}
			case "header":
				c.ALTAPIErrorRules[7].When.Headers = map[string]string{"X-Operator": "yes"}
			case "append":
				c.ALTAPIErrorRules = append(c.ALTAPIErrorRules, rules.ErrorRule{ID: "operator", Scope: rules.ScopeAccount, Action: rules.ActionIgnore, When: rules.Match{Statuses: []int{418}}})
			case "empty":
				c.ALTAPIErrorRules = []rules.ErrorRule{}
			case "retry-note":
				errorsChanged, retriesChanged = false, true
				c.ALTAPIRetryRules[0].Note = "operator note"
			case "retry-order":
				errorsChanged, retriesChanged = false, true
				c.ALTAPIRetryRules[0], c.ALTAPIRetryRules[1] = c.ALTAPIRetryRules[1], c.ALTAPIRetryRules[0]
			case "retry-disabled":
				errorsChanged, retriesChanged = false, true
				c.ALTAPIRetryRules[0].Disabled = true
			case "retry-body":
				errorsChanged, retriesChanged = false, true
				c.ALTAPIRetryRules[0].When.BodyContains = []string{"operator body"}
			case "retry-empty":
				errorsChanged, retriesChanged = false, true
				c.ALTAPIRetryRules = []rules.RetryRule{}
			}
			before := c.Clone()
			if err := c.Normalize(); err != nil {
				t.Fatal(err)
			}
			if errorsChanged && !reflect.DeepEqual(c.ALTAPIErrorRules, before.ALTAPIErrorRules) {
				t.Fatal("overwrote custom error rules")
			}
			if retriesChanged && !reflect.DeepEqual(c.ALTAPIRetryRules, before.ALTAPIRetryRules) {
				t.Fatal("overwrote custom retry rules")
			}
			if !errorsChanged && !reflect.DeepEqual(c.ALTAPIErrorRules, rules.DefaultALTAPIErrorRules()) {
				t.Fatal("unmodified error list did not upgrade independently")
			}
			if !retriesChanged && !reflect.DeepEqual(c.ALTAPIRetryRules, rules.DefaultALTAPIRetryRules()) {
				t.Fatal("unmodified retry list did not upgrade independently")
			}
		})
	}
}
