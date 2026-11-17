package rules

func DefaultFactoryErrorRules() []ErrorRule {
	return []ErrorRule{
		{ID: "factory-policy", Scope: ScopeAccount, Action: ActionIgnore, DownstreamStatus: 403, When: Match{Statuses: []int{403}, BodyContains: []string{"content_policy_rejected"}}, Note: "使用策略拒绝立即停止；不改写内容、组织、客户端或换号绕过（执行层强制安全门）"},
		{ID: "factory-auth", Scope: ScopeAccount, Action: ActionCooldown, DownstreamStatus: 502, When: Match{Statuses: []int{401}}, Reset: &Reset{Fallback: "60s"}, Note: "本Key鉴权失败，短暂冷却；不推断余额或永久封禁"},
		{ID: "factory-funding", Scope: ScopeAccount, Action: ActionCooldown, DownstreamStatus: 503, When: Match{Statuses: []int{402}}, Reset: &Reset{Fallback: "10m"}, Note: "计费拒绝，停止本请求换号；不自动付款或修改Extra usage"},
		{ID: "factory-forbidden", Scope: ScopeModel, Action: ActionIgnore, DownstreamStatus: 403, When: Match{Statuses: []int{403}}, Note: "权限/组织/模型限制停止换号，不推断封禁"},
		{ID: "factory-rate-limit", Scope: ScopeAccount, Action: ActionCooldown, DownstreamStatus: 429, When: Match{Statuses: []int{429}}, Reset: &Reset{Header: "Retry-After", Format: "retry-after", Fallback: "60s"}, Note: "完整遵守Retry-After；多Key可能共享组织限额，默认不轮换"},
		{ID: "factory-upstream", Scope: ScopeModel, Action: ActionCooldown, When: Match{Statuses: []int{500, 502, 503, 504}}, Reset: &Reset{Header: "Retry-After", Format: "retry-after", Fallback: "5s"}, Note: "明确HTTP暂时故障，未输出且无状态请求可按尝试预算重试"},
	}
}
func DefaultFactoryRetryRules() []RetryRule {
	return []RetryRule{{ID: "factory-stop", Decision: DecisionStop, When: Match{Statuses: []int{400, 402, 403, 404, 405, 409, 413, 422, 429}}, Note: "参数/权限/策略/额度/限流默认停止轮换；策略拒绝安全门不可被自定义规则绕过"}}
}
