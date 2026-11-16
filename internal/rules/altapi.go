package rules

// altapi.ai (TokenPower) is a customized new-api / one-api fork. Its error
// envelope is OpenAI-shaped but adds a `type:"new_api_error"` and puts the
// reason in `message`; the HTTP status is coarse (400 for most business
// refusals), so body text carries the real meaning.
//
// Measured 2026-11-15:
//
//	200 {"error":{"message":"Invalid token (request id: …)","type":"new_api_error"}}
//	200 {"error":{"message":"Model X not found, no deployment node exists",
//	      "type":"upstream_error","param":"","code":"MODEL_NOT_SUPPORT"}}
//	200 {"message":"无权进行此操作，access token 无效","success":false}
//
// new-api answers 200 for many refusals, so specific business rules match
// body text instead of relying on status alone. Status fallbacks still cover
// documented 429/5xx responses, including empty bodies.
// Official examples: https://altapi.ai/docs (Error Code Reference).
//
// Key facts that shape these rules:
//   - One wallet backs every model (`quota` on /api/user/self). Exhausting it
//     is a whole-account condition, not a per-model one.
//   - The console's "Current balance" is exactly `quota / quota_per_unit`
//     (quota_per_unit = 500000 on altapi.ai), so a 402-class refusal is worth
//     an immediate quota refresh instead of a guess.
//   - Channel groups (`default`, `vip`) have different model coverage; a
//     MODEL_NOT_SUPPORT on one group can answer on another, so it cools the
//     account×model pair rather than hard-quarantining the key.
//   - Several keys per account share the same wallet and the same upstream
//     rate limits; rotating keys cannot escape a 429.

// DefaultALTAPIErrorRules are altapi.ai's failures. Order matters: first match
// wins, so specific wordings must precede the generic status rules.
func DefaultALTAPIErrorRules() []ErrorRule {
	return []ErrorRule{
		// The key itself is rejected. altapi returns this with HTTP 200, so a
		// status-only rule would silently pass it through as a success.
		{ID: "altapi-token-invalid", Scope: ScopeAccount, Action: ActionHardQuarantine,
			When: altapiInvalidTokenMatch(),
			Note: "Key 无效/被禁用/已过期（实测 HTTP 200 + success:false）：硬隔离，需重新生成 Key 或确认账号未封"},

		{ID: "altapi-user-banned", Scope: ScopeAccount, Action: ActionHardQuarantine,
			When: Match{BodyContains: []string{
				"该用户已被封禁", "用户已被禁用", "user has been banned", "account is banned",
				"已被封号",
			}},
			Note: "账号被封禁，需人工处理"},

		// The account is fine; this model has no deployment node in the
		// account's group. Verified: DeepSeek-V3.2 and GLM-4.6 are listed by
		// /api/user/models yet answer MODEL_NOT_SUPPORT.
		//
		// Match.BodyCodes reads only a TOP-LEVEL code, and new-api nests it as
		// error.code, so the code token is matched as body text instead. The
		// measured body is
		//   {"error":{"message":"Model X not found, no deployment node exists",
		//    "type":"upstream_error","param":"","code":"MODEL_NOT_SUPPORT"}}
		// and it arrives with HTTP 400, so no Statuses bound here: the status is
		// not what distinguishes this from a genuine upstream fault.
		{ID: "altapi-model-not-support", Scope: ScopeModel, Action: ActionCooldown, DownstreamStatus: 404,
			When: Match{BodyContains: []string{"MODEL_NOT_SUPPORT", "no deployment node exists",
				"无可用渠道", "没有可用渠道"}},
			Reset: &Reset{Fallback: "30m", Max: "6h"},
			Note:  "该账号当前分组下此模型无部署节点（模型列表里有也不代表能调）：只冷却本号×模型，换号继续；不判封号"},

		// The official example deliberately combines missing model and denied
		// model access. It is not proof that the ID is invalid for every key:
		// keep it model-scoped, ahead of generic invalid_request_error/CF rules.
		// Do not bind it to 403: new-api also wraps business refusals in 200/400.
		{ID: "altapi-model-forbidden", Scope: ScopeModel, Action: ActionCooldown, DownstreamStatus: 403,
			When: Match{BodyContains: []string{"无权限使用该模型", "not allow to use this model", "分组无权限", "model_forbidden",
				"The model does not exist or you do not have access to it"}},
			Reset: &Reset{Fallback: "5m"},
			Note:  "当前 Key/分组无权使用模型或模型不可用：只冷却本号×模型并换号；不推断全池模型不存在或账号封禁"},

		// The caller asked for a model nobody serves. Not an account fault:
		// retrying another account wastes quota on a guaranteed failure.
		{ID: "altapi-model-unknown", Scope: ScopeModel, Action: ActionIgnore, DownstreamStatus: 404,
			When: Match{BodyContains: []string{"is not a valid model", "model_not_found", "模型不存在"}},
			Note: "模型 ID 本身不存在：不扣健康分，也不换号（换号必然再失败）"},

		// The operator-selected balance refusal includes an explicit reason
		// and request id. Match that reason with boundaries, not generic
		// quota/balance wording. Only exclude this account for this request;
		// do not infer that every later request will fail, cool the account,
		// penalize its health or trigger an extra quota refresh.
		{ID: "altapi-user-quota-switch", Scope: ScopeAccount, Action: ActionSwitchAccount, DownstreamStatus: 503,
			When: Match{BodyContains: []string{
				": insufficient_user_quota (request id:", ": insufficient user quota (request id:",
			}},
			Note: "指定余额不足（insufficient user quota + request id）：仅本次请求换号，不冷却、不隔离、不扣健康分、不额外刷新额度"},

		// Other funding refusals retain their existing account cooldown.
		{ID: "altapi-insufficient-quota", Scope: ScopeAccount, Action: ActionCooldown,
			RefreshQuota: true, DownstreamStatus: 503,
			When: Match{BodyContains: []string{
				"额度不足", "余额不足", "insufficient_user_quota", "insufficient quota",
				"预扣费额度失败", "insufficient_balance", "insufficient credits",
				"用户额度不足",
			}},
			Reset: &Reset{Fallback: "10m", Max: "6h", Escalate: true},
			Note:  "钱包余额不够付这次调用：冷却并立即刷新真实余额（quota/quota_per_unit）；连续命中冷却翻倍，成功一次清零"},

		// CF-Ray is also present on ordinary origin responses. Only a positive
		// challenge signal distinguishes the edge from an upstream refusal.
		{ID: "altapi-cf-block", Scope: ScopeAccount, Action: ActionCooldown, DownstreamStatus: 503,
			When:  altapiCFChallengeMatch(),
			Reset: &Reset{Header: "Retry-After", Format: "retry-after", Fallback: "30s", Max: "10m"},
			Note:  "明确 Cf-Mitigated: challenge 的边缘挑战：短冷却并停止本次换号；仅有 CF-Ray 不判封禁"},

		// Catch every documented 429, including an empty body. No automatic
		// replay: the retry rule stops this request after applying the cooldown.
		{ID: "altapi-rate-limit", Scope: ScopeAccount, Action: ActionCooldown, DownstreamStatus: 429,
			When:  Match{Statuses: []int{429}},
			Reset: &Reset{Header: "Retry-After", Format: "retry-after", Fallback: "60s", Max: "15m", Escalate: true},
			Note:  "限流：遵守 Retry-After；无有效提示时连续命中退避翻倍（60秒至15分钟），成功清零；停止本次换号，由客户端稍后重试"},

		// Upstream provider failed on altapi's side. Not this account's fault,
		// but the request cannot be served by it right now.
		// Deliberately does NOT match on the bare type token "upstream_error":
		// new-api reuses it for MODEL_NOT_SUPPORT, which is a per-model
		// condition and must not cool the whole account.
		{ID: "altapi-upstream-error", Scope: ScopeAccount, Action: ActionCooldown, DownstreamStatus: 502,
			When: Match{BodyContains: []string{"当前分组上游负载已饱和", "上游负载已饱和",
				"upstream connect error", "no available channel", "bad response status code"}},
			Reset: &Reset{Header: "Retry-After", Format: "retry-after", Fallback: "5s", Max: "2m"},
			Note:  "altapi 上游故障或渠道饱和：短冷却换号；不是封禁也不是余额耗尽"},

		// The request itself is malformed. Never rotate accounts on this.
		{ID: "altapi-bad-request", Scope: ScopeModel, Action: ActionIgnore, DownstreamStatus: 400,
			When: Match{Statuses: []int{400},
				BodyContains: []string{"invalid_request_error", "messages", "max_tokens",
					"缺少必要参数", "参数错误"}},
			Note: "请求参数错误：不扣健康分、不换号"},

		// Plain 5xx from the origin.
		{ID: "altapi-server-error", Scope: ScopeAccount, Action: ActionCooldown,
			When:  Match{Statuses: []int{500, 502, 503, 504}},
			Reset: &Reset{Header: "Retry-After", Format: "retry-after", Fallback: "5s", Max: "1m"},
			Note:  "服务端错误：短暂冷却，仅在未输出时可重试"},
	}
}

// DefaultALTAPIRetryRules decide when switching accounts is pointless.
func DefaultALTAPIRetryRules() []RetryRule {
	return []RetryRule{
		{ID: "altapi-token-stop", Decision: DecisionStop,
			When: altapiInvalidTokenMatch(),
			Note: "Key 失效：隔离当前账号后停止本次请求，核对或更换 Key；不覆盖显式 stop"},
		// invalid_request_error alone also describes model access errors.
		// Genuine generic 400s already stop via the ignore error rule; only
		// concrete parameter failures belong in this explicit stop matcher.
		{ID: "altapi-bad-request-stop", Decision: DecisionStop,
			When: Match{Statuses: []int{400, 413, 422},
				BodyContains: []string{"messages", "max_tokens", "缺少必要参数", "参数错误", "invalid JSON", "missing required"}},
			Note: "调用方参数错误：换号必然再失败，停止"},
		{ID: "altapi-model-unknown-stop", Decision: DecisionStop,
			When: Match{BodyContains: []string{"is not a valid model", "model_not_found", "模型不存在"}},
			Note: "模型 ID 不存在：停止，不遍历号池"},
		{ID: "altapi-rate-stop", Decision: DecisionStop,
			When: Match{Statuses: []int{429}},
			Note: "同一账号多 Key 共享限额，同 IP 共享 CF 限速：默认不靠换号绕过，客户端按 Retry-After 等待"},
		{ID: "altapi-cf-stop", Decision: DecisionStop,
			When: altapiCFChallengeMatch(),
			Note: "明确的 Cloudflare 挑战停止本次请求，不跨账号或出口重放"},
	}
}

func altapiInvalidTokenMatch() Match {
	return Match{BodyContains: []string{
		"Invalid token", "access token 无效", "无效的令牌", "令牌已过期",
		"Token has expired", "该令牌无权访问",
		"Invalid API key", "invalid_api_key", "authentication_error",
	}}
}

func altapiCFChallengeMatch() Match {
	return Match{Statuses: []int{429, 403}, Headers: map[string]string{"Cf-Mitigated": "challenge"}}
}
