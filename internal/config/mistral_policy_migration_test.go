package config

import (
	"encoding/json"
	"reflect"
	"testing"

	"account-pool-balancer/internal/rules"
)

// Freeze the actually shipped factory lists; do not derive old expectations by
// slicing the new defaults, which would let a broken upgrade test itself.
const shippedMistralRules = `{
 "errors":[
  {"id":"mistral-service-unavailable","scope":"model","action":"ignore","when":{"statuses":[503],"bodyCodes":["3800"]},"note":"上游模型服务暂不可用，不扣账号健康；停止无收益换号，由客户端稍后重试"},
  {"id":"mistral-invalid-key","scope":"account","action":"hard-quarantine","downstreamStatus":502,"when":{"statuses":[401]},"note":"Mistral API Key 无效或已撤销，更换后恢复"},
  {"id":"mistral-insufficient-credits","scope":"account","action":"cooldown","downstreamStatus":503,"when":{"statuses":[402]},"reset":{"fallback":"10m","max":"6h","escalate":true},"note":"余额或支出上限不足，冷却后恢复；余额未知"},
  {"id":"mistral-rate-limited","scope":"account","action":"cooldown","when":{"statuses":[429]},"reset":{"header":"Retry-After","format":"retry-after","fallback":"30s"},"note":"遵守完整 Retry-After；同工作区的 Key 可能共享限额"},
  {"id":"mistral-server-error","scope":"account","action":"cooldown","when":{"statuses":[500,502,503,504]},"reset":{"header":"Retry-After","format":"retry-after","fallback":"5s"},"note":"上游暂时故障，短暂冷却后换号"}
 ],
 "retries":[
  {"id":"mistral-service-unavailable","decision":"stop","when":{"statuses":[503],"bodyCodes":["3800"]},"note":"明确的上游服务故障停止换号，避免同一失败重复等待；429保留原策略"},
  {"id":"mistral-request-error","decision":"stop","when":{"statuses":[400,403,404,405,409,413,422]},"note":"请求、模型或权限错误停止，不遍历账号"}
 ]
}`

func shippedMistralPolicy(t *testing.T, beforeServiceRule bool) ([]rules.ErrorRule, []rules.RetryRule) {
	t.Helper()
	var doc struct {
		Errors  []rules.ErrorRule `json:"errors"`
		Retries []rules.RetryRule `json:"retries"`
	}
	if err := json.Unmarshal([]byte(shippedMistralRules), &doc); err != nil {
		t.Fatal(err)
	}
	if beforeServiceRule {
		return doc.Errors[1:], doc.Retries[1:]
	}
	return doc.Errors, doc.Retries
}

func TestMistralTierCapacityFactoryMigration(t *testing.T) {
	for _, version := range []string{"before-service", "shipped", "nil", "current"} {
		t.Run(version, func(t *testing.T) {
			c := Default()
			switch version {
			case "before-service", "shipped":
				c.MistralErrorRules, c.MistralRetryRules = shippedMistralPolicy(t, version == "before-service")
			case "nil":
				c.MistralErrorRules, c.MistralRetryRules = nil, nil
			}
			before := c.Clone()
			if err := c.Normalize(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(c.MistralErrorRules, rules.DefaultMistralErrorRules()) || !reflect.DeepEqual(c.MistralRetryRules, rules.DefaultMistralRetryRules()) {
				t.Fatal("exact factory lists were not upgraded")
			}
			if r := rules.MatchError(c.MistralErrorRules, 403, `{"code":1910}`, nil); r == nil || r.ID != "mistral-tier-not-allowed" {
				t.Fatal("upgrade did not install exact model-tier policy")
			}
			if r := rules.MatchRetry(c.MistralRetryRules, 429, `{"code":3505}`, nil); r == nil || r.ID != "mistral-backend-out-of-capacity" {
				t.Fatal("upgrade did not install capacity stop policy")
			}
			for _, provider := range []string{ProviderOpenDesign, ProviderRiver, ProviderOllama, ProviderModal, ProviderTokenHarbor, ProviderOpenCodeZen, ProviderOpenCodeGo} {
				a, b := before.RulesFor(provider)
				x, y := c.RulesFor(provider)
				if !reflect.DeepEqual(a, x) || !reflect.DeepEqual(b, y) {
					t.Fatalf("Mistral upgrade changed %s", provider)
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
			if !reflect.DeepEqual(c.MistralErrorRules, restored.MistralErrorRules) || !reflect.DeepEqual(c.MistralRetryRules, restored.MistralRetryRules) {
				t.Fatal("default policy changed on serialization/re-normalization")
			}
		})
	}
}

func TestMistralTierCapacityMigrationPreservesOperatorRules(t *testing.T) {
	for _, mutation := range []string{"note", "order", "disabled", "reset", "codes", "status", "prepend403429", "empty", "retry-note", "retry-order", "retry-disabled", "retry-empty"} {
		t.Run(mutation, func(t *testing.T) {
			c := Default()
			c.MistralErrorRules, c.MistralRetryRules = shippedMistralPolicy(t, false)
			errorsChanged, retriesChanged := false, false
			switch mutation {
			case "note":
				c.MistralErrorRules[0].Note = "operator notes"
				errorsChanged = true
			case "order":
				c.MistralErrorRules[0], c.MistralErrorRules[1] = c.MistralErrorRules[1], c.MistralErrorRules[0]
				errorsChanged = true
			case "disabled":
				c.MistralErrorRules[0].Disabled = true
				errorsChanged = true
			case "reset":
				c.MistralErrorRules[3].Reset.Fallback = "5s"
				errorsChanged = true
			case "codes":
				c.MistralErrorRules[0].When.BodyCodes = []string{"3800", "other"}
				errorsChanged = true
			case "status":
				c.MistralErrorRules[0].DownstreamStatus = 502
				errorsChanged = true
			case "prepend403429":
				c.MistralErrorRules = append([]rules.ErrorRule{
					{ID: "403", Scope: rules.ScopeAccount, Action: rules.ActionHardQuarantine, When: rules.Match{Statuses: []int{403}}},
					{ID: "429", Scope: rules.ScopeAccount, Action: rules.ActionCooldown, When: rules.Match{Statuses: []int{429}}, Reset: &rules.Reset{Fallback: "5s"}},
				}, c.MistralErrorRules...)
				errorsChanged = true
			case "empty":
				c.MistralErrorRules = []rules.ErrorRule{}
				errorsChanged = true
			case "retry-note":
				c.MistralRetryRules[0].Note = "operator notes"
				retriesChanged = true
			case "retry-order":
				c.MistralRetryRules[0], c.MistralRetryRules[1] = c.MistralRetryRules[1], c.MistralRetryRules[0]
				retriesChanged = true
			case "retry-disabled":
				c.MistralRetryRules[0].Disabled = true
				retriesChanged = true
			case "retry-empty":
				c.MistralRetryRules = []rules.RetryRule{}
				retriesChanged = true
			}
			before := c.Clone()
			if err := c.Normalize(); err != nil {
				t.Fatal(err)
			}
			if errorsChanged && !reflect.DeepEqual(c.MistralErrorRules, before.MistralErrorRules) {
				t.Fatal("overwrote customized error rules")
			}
			if retriesChanged && !reflect.DeepEqual(c.MistralRetryRules, before.MistralRetryRules) {
				t.Fatal("overwrote customized retry rules")
			}
		})
	}
}
