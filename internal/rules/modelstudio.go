package rules

// Model Studio (Alibaba Cloud Bailian) failures.
//
// The rules below come from the documented error catalogue
// (help.aliyun.com/zh/model-studio/error-code) and the Token Plan FAQ, plus
// what the endpoints were measured to answer on 2026-11-11:
//
//   - a wrong host for the key family answers 401 invalid_api_key
//     ("Incorrect API key provided" on the metered hosts);
//   - a model outside the plan answers 400 InvalidParameter "Model not exist."
//     (Anthropic) or 404 model_not_found (OpenAI-compatible);
//   - the quota is exhausted with 429 insufficient_quota
//     "Your token-plan quota has been exhausted."
//
// DashScope nests its error code inside "error" ({"error":{"code":...}}),
// while Match.BodyCodes reads only a top-level code/errorCode, so these rules
// match on the documented message text (case-insensitive) instead.
//
// Importantly, a limit is per *model* on the pool's dynamic tiers, and the
// account-level TPM/RPM is shared by every key of an Alibaba Cloud account
// ("限流按主账号维度计算，RAM 子账号、业务空间和 API Key 调用量合并"). Cooldown
// therefore lands on the account×model scope wherever the error names a model,
// so one exhausted model does not take the account's whole catalogue down.
func DefaultModelStudioErrorRules() []ErrorRule {
	return []ErrorRule{
		{ID: "modelstudio-quota-exhausted", Scope: ScopeAccount, Action: ActionCooldown, DownstreamStatus: 503, RefreshQuota: true,
			When:  Match{Statuses: []int{429}, BodyContains: []string{"insufficient_quota"}},
			Reset: &Reset{Fallback: "30m", Max: "8d", Escalate: true},
			Note:  "套餐 Credits 用完：本号暂停到周期重置；槽位来自账号共享资源，不伪造余额"},
		{ID: "modelstudio-model-not-entitled", Scope: ScopeModel, Action: ActionCooldown, DownstreamStatus: 404,
			When:  Match{Statuses: []int{400, 403, 404}, BodyContains: []string{"model not exist", "model_not_found", "accessdenied.unpurchased"}},
			Reset: &Reset{Fallback: "24h"},
			Note:  "该套餐/业务空间不包含此模型（Model not exist / AccessDenied.Unpurchased）：仅此号×此模型冷却，不隔离整号"},
		{ID: "modelstudio-invalid-key", Scope: ScopeAccount, Action: ActionHardQuarantine, DownstreamStatus: 502,
			When: Match{Statuses: []int{401}, BodyContains: []string{"invalid_api_key", "incorrect api key provided", "invalid access token"}},
			Note: "API Key 无效、已重置或与 Base URL 套餐不匹配：需人工更换（Token Plan Key 配到按量付费域名同样会 401）"},
		{ID: "modelstudio-concurrency-limit", Scope: ScopeAccount, Action: ActionIgnore,
			When: Match{Statuses: []int{429}, BodyContains: []string{"concurrency allocated quota exceeded"}},
			Note: "并发分配上限（随负载动态变化）：不扣健康、不换号，短退避即可"},
		{ID: "modelstudio-rate-limited", Scope: ScopeModel, Action: ActionCooldown, DownstreamStatus: 429,
			When:  Match{Statuses: []int{429}},
			Reset: &Reset{Header: "Retry-After", Format: "retry-after", Fallback: "60s"},
			Note:  "动态限流（Throttling.*）：按账号×模型冷却，同账号其他 Key 共享同一限额"},
		{ID: "modelstudio-service-unavailable", Scope: ScopeAccount, Action: ActionIgnore,
			When: Match{Statuses: []int{503, 502}, BodyContains: []string{"serviceunavailable"}},
			Note: "平台资源不足或偶发网关异常：与限流额度无关，稍后重试，不换号"},
		{ID: "modelstudio-server-error", Scope: ScopeAccount, Action: ActionCooldown,
			When:  Match{Statuses: []int{500, 504}},
			Reset: &Reset{Header: "Retry-After", Format: "retry-after", Fallback: "5s"},
			Note:  "上游内部错误：短暂冷却后换号"},
	}
}

func DefaultModelStudioRetryRules() []RetryRule {
	return []RetryRule{
		{ID: "modelstudio-model-not-entitled", Decision: DecisionStop,
			When: Match{BodyContains: []string{"model not exist", "model_not_found", "accessdenied.unpurchased"}},
			Note: "模型不在该套餐内：换号也无益，直接返回"},
		{ID: "modelstudio-service-unavailable", Decision: DecisionStop,
			When: Match{Statuses: []int{503, 502}, BodyContains: []string{"serviceunavailable"}},
			Note: "明确的平台不可用停止换号；保留 429 限流的原有换号策略"},
		{ID: "modelstudio-request-error", Decision: DecisionStop,
			When: Match{Statuses: []int{400, 402, 403, 405, 409, 413, 422}},
			Note: "请求或参数错误停止，不遍历账号"},
	}
}
