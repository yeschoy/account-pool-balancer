package rules

func DefaultFactoryErrorRules() []ErrorRule {
	return []ErrorRule{
		{ID: "factory-policy", Scope: ScopeAccount, Action: ActionIgnore, DownstreamStatus: 403, When: Match{Statuses: []int{403}, BodyContains: []string{"content_policy_rejected"}}, Note: "使用策略拒绝立即停止；不改写内容、组织、客户端或换号绕过（执行层强制安全门）"},
		{ID: "factory-auth", Scope: ScopeAccount, Action: ActionCooldown, DownstreamStatus: 502, When: Match{Statuses: []int{401}}, Reset: &Reset{Fallback: "60s"}, Note: "本Key鉴权失败，短暂冷却；不推断余额或永久封禁"},
		{ID: "factory-funding", Scope: ScopeAccount, Action: ActionCooldown, DownstreamStatus: 503, When: Match{Statuses: []int{402}}, Reset: &Reset{Fallback: "10m"}, Note: "计费拒绝，停止本请求换号；不自动付款或修改Extra usage"},
		// Factory validates the body model against the ORG TIER's catalogue, not
		// only against its global catalogue, and answers a model the tier does
		// not carry with the same words it uses for a typo. Measured 2026-11-24
		// against the production pool: a `pro_plus` org answered
		// 400 {"detail":"Invalid model ID in request body"} for every
		// gemini-4-{argon,carbon,hydrogen,neon} request, while a `team` org
		// served the same bodies with 200. The account itself was healthy: the
		// same pro_plus key answered claude-sonnet-5, glm-5.3 and
		// gemini-3.8-flash. So this is an account×model entitlement gap, not a
		// bad request and not an account fault: cool only that pair, switch
		// accounts for the request, and never touch the account's health or its
		// other models.
		{ID: "factory-model-id-rejected", Scope: ScopeModel, Action: ActionCooldown, DownstreamStatus: 503,
			When:  Match{Statuses: []int{400}, BodyContains: []string{"invalid model id in request body"}},
			Reset: &Reset{Fallback: "24h", Max: "7d"},
			Note:  "该账号所属组织套餐不包含此模型 ID（实测 pro_plus 组织调 gemini-4-* 一律 400，team 组织可用）：只冷却本号×模型并换号，账号其它模型照常使用，不扣健康分、不判封禁"},
		{ID: "factory-forbidden", Scope: ScopeModel, Action: ActionIgnore, DownstreamStatus: 403, When: Match{Statuses: []int{403}}, Note: "权限/组织/模型限制停止换号，不推断封禁"},
		{ID: "factory-rate-limit", Scope: ScopeAccount, Action: ActionCooldown, DownstreamStatus: 429, When: Match{Statuses: []int{429}}, Reset: &Reset{Header: "Retry-After", Format: "retry-after", Fallback: "60s"}, Note: "完整遵守Retry-After；多Key可能共享组织限额，默认不轮换"},
		{ID: "factory-upstream", Scope: ScopeModel, Action: ActionCooldown, When: Match{Statuses: []int{500, 502, 503, 504}}, Reset: &Reset{Header: "Retry-After", Format: "retry-after", Fallback: "5s"}, Note: "明确HTTP暂时故障，未输出且无状态请求可按尝试预算重试"},
	}
}
func DefaultFactoryRetryRules() []RetryRule {
	return []RetryRule{{ID: "factory-stop", Decision: DecisionStop, When: Match{Statuses: []int{400, 402, 403, 404, 405, 409, 413, 422, 429}}, Note: "参数/权限/策略/额度/限流默认停止轮换；策略拒绝安全门不可被自定义规则绕过"}}
}
