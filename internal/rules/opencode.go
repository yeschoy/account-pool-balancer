package rules

import "strings"

// OpenCode rules are isolated from OpenDesign's similarly named Go plan.
// A generic forbidden response is not proof of revoked credentials.
func openCodePolicyMatch() Match {
	return Match{Statuses: []int{403}, BodyContains: []string{"freetiererror", "free tier can only", "abusive traffic", "abuse traffic", "abuse of traffic"}}
}

// IsOpenCodePolicyRefusal is a non-overridable safety boundary: editable
// cooldown/retry rules must never turn an explicit access/risk gate into a key walk.
func IsOpenCodePolicyRefusal(status int, body string) bool {
	return openCodePolicyMatch().matches(status, strings.ToLower(body), nil)
}

func DefaultOpenCodeErrorRules() []ErrorRule {
	return []ErrorRule{
		{ID: "opencode-policy-refusal", Scope: ScopeAccount, Action: ActionIgnore, DownstreamStatus: 403, When: openCodePolicyMatch(), Note: "明确门禁/风控拒绝：不换号、不换出口、不改写客户端身份"},
		{ID: "opencode-auth", Scope: ScopeAccount, Action: ActionHardQuarantine, DownstreamStatus: 502, When: Match{Statuses: []int{401}}, Note: "密钥无效；更换凭证后人工恢复"},
		{ID: "opencode-model-permission", Scope: ScopeModel, Action: ActionCooldown, DownstreamStatus: 503, When: Match{Statuses: []int{403, 404}, BodyContains: []string{"model_not_found", "model not found", "model_not_allowed", "model access", "not entitled"}}, Reset: &Reset{Fallback: "10m"}, Note: "仅当前账号的当前模型冷却"},
		{ID: "opencode-balance", Scope: ScopeAccount, Action: ActionCooldown, DownstreamStatus: 503, When: Match{Statuses: []int{402}}, Reset: &Reset{Fallback: "10m", Max: "1h", Escalate: true}, RefreshQuota: true, Note: "上游确认余额不足；不伪造余额"},
		{ID: "opencode-rate", Scope: ScopeAccount, Action: ActionCooldown, When: Match{Statuses: []int{429}}, Reset: &Reset{Header: "Retry-After", Format: "retry-after", Fallback: "30s"}},
		{ID: "opencode-forbidden", Scope: ScopeModel, Action: ActionCooldown, DownstreamStatus: 503, When: Match{Statuses: []int{403}}, Reset: &Reset{Fallback: "5m"}, Note: "权限或供应商拒绝；不伪造官方客户端、不永久隔离整号"},
		{ID: "opencode-temporary", Scope: ScopeModel, Action: ActionCooldown, When: Match{Statuses: []int{500, 502, 503, 504}}, Reset: &Reset{Header: "Retry-After", Format: "retry-after", Fallback: "10s", Max: "5m"}},
	}
}

func DefaultOpenCodeRetryRules() []RetryRule {
	return append([]RetryRule{{ID: "opencode-policy-stop", Decision: DecisionStop, When: openCodePolicyMatch(), Note: "明确供应商政策拒绝，不自动重放"}}, retryRulesFor("opencode-request", "参数错误不换号；明确的账号/模型错误按错误规则处理", 400, 404, 413, 422)...)
}
