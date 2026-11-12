package rules

// Conservative HTTP defaults, not assertions about an account's subscription.
// A catalog read is public metadata and does not disprove pending activation.
func DefaultDeployErrorRules() []ErrorRule {
	return []ErrorRule{
		{ID: "deploy-organization-paused", Scope: ScopeAccount, Action: ActionCooldown, DownstreamStatus: 503,
			When: Match{Statuses: []int{405}, BodyContains: []string{"Your Organization is currently paused"}}, Reset: &Reset{Fallback: "30m"},
			Note: "官方 405 表示组织暂停，可能涉及余额或条款：需人工检查，不等于已测零余额、不自动充值"},
		{ID: "deploy-auth", Scope: ScopeAccount, Action: ActionCooldown, DownstreamStatus: 502,
			When: Match{Statuses: []int{401}}, Reset: &Reset{Fallback: "60s"},
			Note: "鉴权未通过（也可能是新 Key 尚未 active）：短暂冷却并人工核对，不把目录 200 当激活证明"},
		{ID: "deploy-funding", Scope: ScopeAccount, Action: ActionCooldown, DownstreamStatus: 503,
			When: Match{Statuses: []int{402}}, Reset: &Reset{Fallback: "30m", Max: "24h", Escalate: true},
			Note: "计费拒绝：账号冷却，余额仍未知；不自动充值或开启钱包消费"},
		{ID: "deploy-forbidden", Scope: ScopeModel, Action: ActionCooldown, DownstreamStatus: 403,
			When: Match{Statuses: []int{403}}, Reset: &Reset{Fallback: "5m"},
			Note: "权限拒绝不等于封号；仅冷却本号×模型，默认停止换号"},
		{ID: "deploy-rate-limit", Scope: ScopeAccount, Action: ActionCooldown, DownstreamStatus: 429,
			When: Match{Statuses: []int{429}}, Reset: &Reset{Header: "Retry-After", Format: "retry-after", Fallback: "60s"},
			Note: "遵守完整 Retry-After，多个 Key 可能共享组织/IP 限额，不把每把 Key 当独立额度"},
		{ID: "deploy-upstream", Scope: ScopeAccount, Action: ActionCooldown,
			When: Match{Statuses: []int{500, 502, 503, 504}}, Reset: &Reset{Header: "Retry-After", Format: "retry-after", Fallback: "5s"},
			Note: "上游暂时故障，非封禁或余额耗尽证据"},
	}
}
func DefaultDeployRetryRules() []RetryRule {
	return []RetryRule{
		{ID: "deploy-request-stop", Decision: DecisionStop, When: Match{Statuses: []int{400, 403, 404, 405, 409, 413, 422}}, Note: "参数/权限/模型/路径错误停止换号，不遍历账号"},
		{ID: "deploy-rate-stop", Decision: DecisionStop, When: Match{Statuses: []int{429}}, Note: "组织/IP 可能共享限流：默认不轮换 Key 绕过限制，客户端按 Retry-After 等待"},
	}
}
