package rules

// Conservative defaults based on HTTP semantics; no unsupported payment or
// Deploy-specific organization-paused rule is inferred for 50X.
func DefaultX50ErrorRules() []ErrorRule {
	return []ErrorRule{
		{ID: "x50-auth", Scope: ScopeAccount, Action: ActionCooldown, DownstreamStatus: 502, When: Match{Statuses: []int{401}}, Reset: &Reset{Fallback: "60s"}, Note: "推理Key鉴权失败，短暂冷却；与billing会话失效不同"},
		{ID: "x50-payment", Scope: ScopeAccount, Action: ActionCooldown, DownstreamStatus: 503, When: Match{Statuses: []int{402}}, Reset: &Reset{Fallback: "30m"}, RefreshQuota: true, Note: "计费拒绝，刷新真实余额；不自动充值或开启付费"},
		{ID: "x50-forbidden", Scope: ScopeModel, Action: ActionCooldown, DownstreamStatus: 403, When: Match{Statuses: []int{403}}, Reset: &Reset{Fallback: "5m"}, Note: "权限拒绝不是封号证明"},
		{ID: "x50-rate", Scope: ScopeAccount, Action: ActionCooldown, DownstreamStatus: 429, When: Match{Statuses: []int{429}}, Reset: &Reset{Header: "Retry-After", Format: "retry-after", Fallback: "60s"}, Note: "遵守完整Retry-After；默认不换号绕限流"},
		{ID: "x50-upstream", Scope: ScopeAccount, Action: ActionCooldown, When: Match{Statuses: []int{500, 502, 503, 504}}, Reset: &Reset{Fallback: "5s"}, Note: "上游临时故障"},
	}
}
func DefaultX50RetryRules() []RetryRule {
	return []RetryRule{
		{ID: "x50-request-stop", Decision: DecisionStop, When: Match{Statuses: []int{400, 403, 404, 405, 409, 413, 422}}, Note: "参数/权限/路径错误不遍历账号"},
		{ID: "x50-rate-stop", Decision: DecisionStop, When: Match{Statuses: []int{429}}, Note: "默认停止换号，等待限流恢复"},
	}
}
