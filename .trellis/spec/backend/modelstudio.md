# Model Studio (阿里云百炼) contracts

- `modelstudio` / reserved group `modelstudio`. Keys are DashScope API keys: `sk-sp-…` (Token Plan subscription) or `sk-…` (按量付费). Both are accepted verbatim; there is no control key and no device auth.
- The *plan* decides the host. Measured 2026-11-11 on a live `sk-sp-` key:

  | plan | OpenAI-compatible root | Anthropic root |
  |---|---|---|
  | Token Plan | `https://token-plan.{ap-southeast-1}.maas.aliyuncs.com/compatible-mode/v1` | same origin `/apps/anthropic/v1` |
  | 按量付费 | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` (or the region's metered host) | same origin `/apps/anthropic/v1` |

  Sending a Token Plan key to `dashscope-intl.aliyuncs.com` answers `401 Incorrect API key provided`, and a metered key to a Token Plan host answers `401 invalid access token or token expired`. Neither proves the key is dead, so the pool always resolves the host from the plan and never falls back between them.
- Regions are independent access hosts, key sets and catalogues (`ap-southeast-1`, `cn-beijing`, `us-east-1`, `eu-central-1`). A key is never sent to another region. An unknown region is rejected by the settings API (HTTP 400) rather than silently normalized.
- Protocols: Chat Completions and Anthropic Messages pass through **byte for byte**, no tool stripping, no model aliasing, no cache-mark injection, no protocol conversion. Model names are case-sensitive and are not guessed. **OpenAI Responses is explicitly unsupported upstream** (Coding Plan FAQ: "不支持 OpenAI Responses API") and is refused locally with no upstream request, never translated.
- Auth: Chat uses `Authorization: Bearer <key>`. Messages uses `x-api-key: <key>`; the caller's own `Authorization` header is dropped on that exchange so a foreign credential is never forwarded. `anthropic-version` is supplied when the client omitted it.
- Allowed paths: exactly `/v1/chat/completions`, `/v1/messages`, `/v1/models`. Everything else is refused locally.
- Reasoning: the models put chain-of-thought in `reasoning_content` (OpenAI) / `thinking` blocks (Anthropic). Bytes are passed through untouched, so a client that only renders `content` may show an empty message for a small `max_tokens`. That is the upstream's shape, not a gateway defect; the pool does not rewrite it.

## Quota: unknown, never zero

- Token Plan Credits are shown only on the console page (`modelstudio.console.alibabacloud.com` → 我的订阅). There is no public balance API: `/v1/usage`, `/v1/quota`, `/api/me` and five other candidates were measured 404, and responses carry no `x-ratelimit-*` headers.
- The console's private gateway is `POST /data/api.json?action=zeldaHttp.apikeyMgr./tokenplan/personal/api/v2/subscription&product=bailian`. Measured 2026-11-11 it answers `{"code":"ConsoleNeedLogin"}` without an Alibaba Cloud console session cookie; its sibling `api-keys/getKeyByUid` returns the *console user's own* key. An inference key cannot be mapped back to the account that owns it, so Credits are only reachable by binding a console session to the account that owns the key.
- **User decision (2026-11-11): leave the frame in place and supply the session later.** Until a reader exists, `ModelStudioQuotaState = "not-connected"`, `ModelStudioQuotaNote` carries the reason, and the console column renders 余额未知（未接入控制台）. Enabling `modelstudio.console.enabled` is refused by validation rather than promising a balance that can never be read.
- Never emit `0` for Credits. Zero reads as "exhausted" to both the operator and the scheduler.
- What *is* read: the account's own `/models` catalogue (real, free, and it authenticates the key). The catalogue source is bound to plan + host + key fingerprint, so a plan/region/key change discards a stale catalogue.
- Limits are **per Alibaba Cloud main account**, not per key: "限流按主账号维度计算，RAM 子账号、业务空间和 API Key 调用量合并". Several keys of one account are not additive quota. Dynamic TPM tiers also apply per account × model.

## Error and retry rules (independent)

Axes: DashScope nests the code inside `error` (`{"error":{"code":…}}`), while `rules.Match.BodyCodes` reads only a top-level `code`/`errorCode` (asserted by `TestBodyCodesStringsAndLosslessNumbers`). The pool therefore matches the documented message text (case-insensitive) and keeps `BodyCodes` where the shape really is top-level.

| rule | when | effect |
|---|---|---|
| `modelstudio-quota-exhausted` | 429 + `insufficient_quota` | account cooldown (escalating, ≤8d), 503, refresh quota |
| `modelstudio-concurrency-limit` | 429 + `concurrency allocated quota exceeded` | ignore (no health loss, no switch) |
| `modelstudio-rate-limited` | any other 429 | account×model cooldown, honour `Retry-After`, 429 |
| `modelstudio-model-not-entitled` | 400/403/404 + `Model not exist` / `model_not_found` / `AccessDenied.Unpurchased` | **model-scoped** cooldown 24h, 404 — never quarantines the account, and stops failover |
| `modelstudio-invalid-key` | 401 + `invalid_api_key` / `Incorrect API key` / `invalid access token` | hard quarantine, 502 |
| `modelstudio-service-unavailable` | 502/503 + `ServiceUnavailable` | ignore, stop failover |
| `modelstudio-server-error` | 500/504 | short cooldown, switch |

Rule order matters: the concurrency rule must precede the generic 429 rule.

## Settings isolation

`modelstudio.routing` and `modelstudio.quota` are independent blocks; saving them must not write `routing`/`quota` or any other pool's rules (`TestModelStudioSaveDoesNotTouchOtherPools`). Before the first save the pool inherits the global blocks (plain legacy behaviour), unlike Token Harbor which overrides sticky/busy policy.

## Key ownership ("sk-" is shared with OpenDesign)

`sk-amr-…` stays OpenDesign. `sk-sp-…` is Model Studio on a fresh deployment. A bare `sk-…` is inferred as Model Studio **only** when the deployment already runs the pool (a Model Studio account, group, client key or import key exists); otherwise it stays OpenDesign. An explicitly declared other provider is never overridden. This is deliberately not a silent re-homing rule: see `Config.ModelStudioKeyOwnership`.

## Tests

- `internal/config/modelstudio_test.go` — plan×region hosts, prefix order, ownership, plan precedence, clone round-trip, pool isolation, console refusal, region/override validation.
- `internal/rules/modelstudio_test.go` — every measured/documented error shape, rule ordering, retry stop-vs-failover.
- `internal/server/modelstudio_gateway_test.go` — Chat and Messages byte pass-through, host shape per protocol, `x-api-key` swap with the caller's `Authorization` removed, Responses refused with zero upstream calls, invalid-key failover and quarantine, quota-exhausted 503 after both accounts, catalogue read, unauthorized client key.
- `internal/server/modelstudio_settings_test.go` — settings GET/PUT round trip, disk reload, cross-pool isolation, unknown region 400, invalid interval rejected.

## Intentionally out of scope (2026-11-11)

- **Registration machine**: the user asked to leave it for a later task. There is no `modelstudio` signup flow, store, captcha or mailbox code; accounts are added by manual create or batch import only. Do not add a "register" button that has nothing behind it.
- Coding Plan (legacy `sk-sp-` variant): explicitly excluded by the user; only Token Plan + 按量付费 are modelled. A Coding Plan key would be sent to the Token Plan host and answered `401 invalid access token` — it is reported as an invalid key, not silently retried elsewhere.
- Console Credits reader: reserved, see "Quota: unknown, never zero".
