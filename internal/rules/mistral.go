package rules

// Mistral has workspace-wide limits. Several keys from the same workspace
// are not independent quota; this gateway never invents a remaining balance.
func DefaultMistralErrorRules() []ErrorRule {
	return []ErrorRule{
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
	return []RetryRule{{ID: "mistral-request-error", Decision: DecisionStop,
		When: Match{Statuses: []int{400, 403, 404, 405, 409, 413, 422}}, Note: "请求、模型或权限错误停止，不遍历账号"}}
}
