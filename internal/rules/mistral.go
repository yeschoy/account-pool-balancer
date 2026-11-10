package rules

// Mistral has workspace-wide limits. Several keys from the same workspace
// are not independent quota; this gateway never invents a remaining balance.
func DefaultMistralErrorRules() []ErrorRule {
	return []ErrorRule{
		{ID: "mistral-service-unavailable", Scope: ScopeModel, Action: ActionIgnore,
			When: Match{Statuses: []int{503}, BodyCodes: []string{"3800"}}, Note: "上游模型服务暂不可用，不扣账号健康；停止无收益换号，由客户端稍后重试"},
		{ID: "mistral-tier-not-allowed", Scope: ScopeModel, Action: ActionCooldown, DownstreamStatus: 503,
			When: Match{Statuses: []int{403}, BodyCodes: []string{"1910"}}, Reset: &Reset{Fallback: "24h"}, Note: "套餐不包含此模型：仅该账号×模型冷却24小时后复查，不隔离整号、不扣全局健康；资格变更后可手动恢复"},
		{ID: "mistral-backend-out-of-capacity", Scope: ScopeModel, Action: ActionIgnore,
			When: Match{Statuses: []int{429}, BodyCodes: []string{"3505"}}, Note: "上游后端容量不足，不是账号RPM或余额不足；不扣健康、不换号，保留429及Retry-After，由客户端退避"},
		{ID: "mistral-invalid-key", Scope: ScopeAccount, Action: ActionHardQuarantine, DownstreamStatus: 502,
			When: Match{Statuses: []int{401}}, Note: "Mistral API Key 无效或已撤销，更换后恢复"},
		{ID: "mistral-insufficient-credits", Scope: ScopeAccount, Action: ActionCooldown, DownstreamStatus: 503,
			When: Match{Statuses: []int{402}}, Reset: &Reset{Fallback: "10m", Max: "6h", Escalate: true}, Note: "余额或支出上限不足，冷却后恢复；余额未知"},
		{ID: "mistral-rate-limited", Scope: ScopeAccount, Action: ActionCooldown,
			When: Match{Statuses: []int{429}}, Reset: &Reset{Header: "Retry-After", Format: "retry-after", Fallback: "30s"}, Note: "遵守完整 Retry-After；同工作区的 Key 可能共享限额"},
		{ID: "mistral-server-error", Scope: ScopeAccount, Action: ActionCooldown,
			When: Match{Statuses: []int{500, 502, 503, 504}}, Reset: &Reset{Header: "Retry-After", Format: "retry-after", Fallback: "5s"}, Note: "上游暂时故障，短暂冷却后换号"},
	}
}

func DefaultMistralRetryRules() []RetryRule {
	return []RetryRule{
		{ID: "mistral-service-unavailable", Decision: DecisionStop,
			When: Match{Statuses: []int{503}, BodyCodes: []string{"3800"}}, Note: "明确的上游服务故障停止换号，避免同一失败重复等待；429保留原策略"},
		{ID: "mistral-backend-out-of-capacity", Decision: DecisionStop,
			When: Match{Statuses: []int{429}, BodyCodes: []string{"3505"}}, Note: "明确的后端容量不足停止本次换号；保留其他账号限流429的原有恢复策略"},
		{ID: "mistral-request-error", Decision: DecisionStop,
			When: Match{Statuses: []int{400, 403, 404, 405, 409, 413, 422}}, Note: "请求、模型或权限错误停止，不遍历账号"},
	}
}
