# account-pool-balancer

<p align="center">
  <img src="docs/logo.svg" width="128" height="128" alt="account-pool-balancer logo" />
</p>

多上游 AI 账号池负载均衡代理。对下游（new-api、One API 等中转站或直接调用方）提供一把 API 密钥和 OpenAI / Anthropic 兼容接口，把请求智能分发到一组上游账号上，负责选号调度、会话粘性、并发与 RPM 控制、额度轮询、错误重试与故障隔离、统计日志，并自带现代化 Web 管理台。支持 [OpenDesign](https://opendesign.com/)、[River](https://docs.river.ai/)、[Ollama](https://docs.ollama.com/)（云端或自建）与 [Modal](https://modal.com/)（Shared Endpoint）等多种上游号池，各自独立分组、独立密钥、独立调度规则。

另有一个 **Perch 号池**（[chat.perchai.app](https://chat.perchai.app)）：它管理的是 **Supabase 会话池**而不是 API Key —— 导入账号 JSON 后自动续期、自动检测状态、读额度，不需要手工换 Token。见 [Perch 号池](#perch-号池chatperchaiapp)。

新增 **OpenCode Zen / Go 授权 Key 号池**，在原有后台、分组和调度器上增量接入，默认关闭。支持套餐独立目录/设置、原生三协议、显式回退及 Go usage；受限跨协议转换另行开关，流为缓冲输出，不承诺完整客户端能力。详见 [使用、配置与验收边界](docs/opencode.md) 和 [源码对比审计 / GPL 处理](docs/opencode-audit.md)。真实上游及五种客户端尚未实机验收。

新增 **Token Harbor 号池**：原生 Chat Completions / Responses（Codex）/ Messages（Claude Code），按会话与缓存标识尽量固定同一 key，独立分组/路由/错误规则，支持概览、请求/错误日志和缓存读取/写入统计。不强制跨协议兼容、不保证上游内部缓存命中；余额未知。见 [接入与边界](docs/tokenharbor.md)。

新增 **Mistral 号池**：复用 OpenDesign 调度策略与 Codex / Claude / pi 等会话头粘性，支持账号管理、批量导入、独立密钥/路由/错误规则，以及原生 Chat / 无状态 Responses / FIM / Embeddings。模型目录独立同步，余额明确未知，不做协议转换。见 [Mistral 接入](docs/mistral.md)。

单个 Go 二进制，无外部依赖，无数据库。

## 快速开始

```sh
go build -o odb ./cmd/odb
ODB_ADMIN_PASSWORD='至少8位的密码' ./odb -addr :8790 -data ./data
```

打开 `http://host:8790/`：

1. **账号 → 设备授权添加**：拿到验证码和激活链接，在**已登录该 OpenDesign 账号**的浏览器里打开并批准，账号连同 `sk-amr-` 运行密钥和 `vela_ctrl_` 控制密钥自动入池。也可以手动添加或批量导入（每行 `运行密钥[,控制密钥[,名称[,代理URL]]]`）。
2. **下游密钥 → 新建密钥**，得到 `sk-odb-…`（只显示一次）。要让脚本自动上号，再在同一页 **新建导入密钥** 得到 `sk-odbimp-…`，见“开放导入接口”。
3. 在 new-api 里新建渠道：类型 OpenAI（或 Anthropic Claude），Base URL 填本服务地址，密钥填 `sk-odb-…`，**打开“透传请求头”**。

不设 `ODB_ADMIN_PASSWORD` 时，第一次打开管理台会要求设置密码。服务暴露到公网前务必先设好密码。

| 环境变量 | 参数 | 默认 |
|---|---|---|
| `ODB_ADDR` | `-addr` | `:8790` |
| `ODB_DATA_DIR` | `-data` | `./data` |
| `ODB_ADMIN_PASSWORD` | | 仅在尚未设置密码时生效 |

### Docker 部署

```sh
cat > .env <<'X'          # 已在 .gitignore 中，权限 0600
ODB_ADMIN_PASSWORD=...     # 只在第一次启动、还没设密码时生效
ODB_UID=1000               # 容器以宿主用户运行，./data 属于该用户
ODB_GID=1000
X
mkdir -p data && chmod 700 data
docker compose up -d --build
```

- 容器加入共享网络 `ai-internal`（与 new-api 同一网络）。new-api 渠道的 Base URL 填 `http://account-pool-balancer:8790`。
- 管理台只绑定宿主机 `127.0.0.1:8790`，远程访问用 SSH 隧道 `ssh -L 8790:127.0.0.1:8790 主机`，或通过反向代理加 HTTPS。
- 数据在 `./data`，升级：`git pull && docker compose up -d --build`；回滚：切回旧版本后同样重建。
- 停止时会等待在途请求最多 30 秒（`stop_grace_period: 35s`）。
- **OVH 现有实例例外**：当前从固定版本快照构建镜像，旧工作树因历史缓存提交而保留，不能直接在旧目录 `up --build`。升级入口、部署验收和备份回滚见 [OVH 部署记录](docs/ovh-deployment.md)。

## River 号池（docs.river.ai）

除 OpenDesign 外也支持 [River](https://docs.river.ai/) 的 API Key（`rv_` 开头）。上游 `https://api.river.ai/v1`（设置 `upstream.riverBase` 可改），OpenAI 兼容，`Authorization: Bearer rv_…`。

- 账号的 `provider` 由运行密钥前缀自动决定：`rv_` → `river`，`oll-` → `ollama`，`wk-` → `modal`，`sk-` → `opendesign`；手动添加、批量导入、开放导入接口都认 `rv_`。River 没有控制面，不能填控制密钥。
- **River 只支持 OpenAI Chat Completions 和 Responses**（`/v1/chat/completions`、`/v1/responses`、`/v1/models`）。River 号池收到 `/v1/messages`（Anthropic）等其他路径时，本地直接返回 404 `unsupported_endpoint`，不请求上游；Claude Code 这类 Anthropic 客户端不能接 River 池。
- 额度未知，靠错误推断：River 的 402（余额不足）按“余额不足”规则逐次翻倍冷却，429 按 `Retry-After` 冷却，401 冷却后换号。**已有实例需在“路由设置”点“恢复默认规则”再保存**才有新增的 401/402 规则。
- 每把 key 的 `/v1/models` 作为可用模型表，River 模型名形如 `deepseek-ai/DeepSeek-V4.1-Flash`。
- **号池之间完全分开**：River 账号不指定分组时进 `river` 组、Ollama 账号进 `ollama` 组、Modal 账号进 `modal` 组，OpenDesign 账号进 `default` 组；一个分组只能放同一种账号，混放会被拒绝保存。各池各建下游密钥（「下游密钥」页，分组分别填 `default` 和 `river`），密钥只调度本组账号，互不借号，用哪个池就给下游配哪把密钥（new-api 里建两个渠道）。导入密钥的默认分组为 `default` 时，`rv_` 账号自动进 `river`。
- River 文档没有公布每 key 的并发/RPM 上限，需要时在账号上手设 `maxConcurrent` / `maxRpm`。

## Ollama 号池（ollama.com / 自建 ollama serve）

支持 [Ollama](https://docs.ollama.com/) 的 API Key：云端 `https://ollama.com/v1`（设置 `upstream.ollamaBase` 可改），或自建服务 `http://host:11434/v1`。**三协议都能接**，不用协议转换：

| 客户端协议 | 路径 | 说明 |
|---|---|---|
| OpenAI Chat Completions | `/v1/chat/completions`（另含 `/v1/completions`、`/v1/embeddings`） | 原样转发 |
| OpenAI Responses | `/v1/responses` | 原样转发；Ollama 云端只支持**无状态**版本（没有 `previous_response_id` / `conversation`） |
| Anthropic Messages | `/v1/messages` | 原样转发，Claude Code 可直接接 |

- **Key 没有固定前缀**，所以账号要显式声明号池：管理台在「Ollama 号池」标签下添加 / 批量导入（自动带 `provider=ollama`），开放导入接口传 `"provider":"ollama"`；也可以把 key 写成 `oll-` 开头让它被自动识别。账号的 `provider` 字段在 config.json 里落盘。
- **鉴权只走 `Authorization: Bearer`**：ollama.com 的 `/v1/messages` 明确不接受单独的 `x-api-key`，所以本服务对 Ollama 账号不发 `x-api-key`（OpenDesign 账号仍然两个都发）。客户端漏传 `anthropic-version` 时，代理会补 `2023-06-01`。
- **额度有接口**：Ollama 的同一把 key 就能读 `GET /api/balance`（不需要控制密钥），返回赠送额度（`included.balance_usd` / `allowance_usd` 与计费周期）、老套餐的 `session` / `weekly` 剩余百分比，以及已购额度 `purchased.balance_usd`。本服务把这些分开展示，额度耗尽判定为「赠送额度用完且已购额度为 0」——读失败或数据过期时绝不当作 0。该接口每个用户每分钟限 10 次，所以同一账号两次读取至少间隔 30 秒。
- 冷却规则独立一套（`ollamaErrorRules`）：401 `invalid credentials` 隔离换 key，403 账号停用隔离，429 按 `Retry-After` 冷却，500/502/503/504 短暂冷却换号。已有实例需在「路由设置 → Ollama 号池」点「恢复默认规则」再保存。
- `/v1/models` 原样透传：Ollama 云端的模型目录对所有 key 相同（换个错误 key 也返回 200），不能当套餐依据，所以不做按账号裁剪；自建服务的列表则是本机已 pull 的模型。
- 分组：不指定分组时进 `ollama` 组，各池之间不混放。下游密钥填 `ollama` 分组（new-api 里再建一个 OpenAI/Claude 渠道指向本服务）。
- **每个池在这四件事上完全独立**（OpenDesign / River / Ollama / Modal 各一套），互不借、互不回退：
  - **号池**：`default` / `river` / `ollama` / `modal` 四个分组各属一个池，一个分组只放同一种账号。自定义分组跟着第一把声明了池的密钥走，归属一旦确定就不能再改。
  - **下游密钥**：密钥自己记着 `provider`，所以池里还没有账号时它也不会漂到别的标签下；密钥只能在**自己池的分组**之间移动，想换池请新建密钥（`group "river" 属于 river 号池，这把密钥属于 ollama 号池` 这样的报错就是被拦下了）。
  - **错误规则**：`errorRules` / `retryRules`、`riverErrorRules` / `riverRetryRules`、`ollamaErrorRules` / `ollamaRetryRules`、`modalErrorRules` / `modalRetryRules` 各自独立，设置页只读写当前标签的一套；默认值也各不相同（重试规则 id 分别是 `bad-request`、`river-bad-request`、`ollama-bad-request`、`modal-bad-request`）。改一个池的规则不会动到其他池。
  - **导入记录**：每条记录记着自己导入进哪个池，`GET /api/import-log?provider=` 按池过滤，「下游密钥」页只显示当前标签那个池的记录。记录上限也按池算（每池最近 1000 条），一个池的导入刷屏不会把另一个池的历史挤掉。
- 模型名带 tag，形如 `gpt-oss:20b`、`gemma4:31b`、`kimi-k2.7-code`；价格表按同样写法配置才会算费用。

## Modal 号池（Shared Endpoints）

支持 [Modal](https://modal.com/) 的 **Shared Endpoint**（[文档](https://modal.com/docs/guide/shared-endpoints)，[接入说明](https://modal.com/docs/guide/endpoint-integrations)）。上游 `https://inference.us-west.modal.direct/v1`（设置 `upstream.modalBase` 可改；只填到 `…modal.direct` 时启动会自动补 `/v1`，因为该 host 不带 `/v1` 一律 404）。

**所有模型共用一个 base URL。下游使用统一模型名，本服务选号后自动转换为该账号的 Endpoint hostname。**

| | 其他池 | Modal |
|---|---|---|
| 每个模型的 URL | 同一个 base | 同一个 base（`inference.<region>.modal.direct/v1`） |
| 下游 `model` 字段 | 模型名，如 `claude-sonnet-5` | 统一短名称，如 `glm-5-3`（发往上游时转换为 Endpoint hostname） |
| 能调哪些模型 | 服务商目录 | 该 token 能访问的 Endpoint（`GET /v1/models` 列出） |
| 鉴权 | `Authorization: Bearer <key>` | `Authorization: Bearer wk-<id>.ws-<secret>` |

- **Proxy Token 的完整形态是 `wk-<id>.ws-<secret>`，两段都要**。只贴 `wk-<id>` 那段（省略 `.ws-<secret>`）上游会回 401 `invalid proxy auth credentials`。Token 在 [modal.com/settings/proxy-auth-tokens](https://modal.com/settings/proxy-auth-tokens) 或 `modal workspace proxy-tokens create` 创建。账号按 `wk-` 前缀自动识别为 Modal，没有控制密钥（Modal 没有额度接口）。
- **统一模型名**：下游 `GET /v1/models` 将各账号的 Endpoint 按基础模型合并去重，当前短名称为 `glm-5-3`、`deepseek-v4-1-flash-1017`、`kimi-k3`、`glm-5-3-flash`、`qwen3-8-max`。其中 `qwen3-8-max` 对应 `Qwen/Qwen3.8-Max-VL-Thinking`。请求选号后自动替换为该账号的 hostname；原始 `base_model_id` 和完整 hostname 仍可调用。上游 `curl "$MODAL_BASE/models" -H "Authorization: Bearer wk-…"` 列的是该 token 可访问的 Endpoint。本服务每 10 分钟读一次并缓存，不支持该模型的账号不会接单（`ignoreCatalog` 可关闭目录筛选）。
- **三种协议都能接**：`/v1/chat/completions`、`/v1/responses`、`/v1/messages`（Claude Code 可直接接，anthropic 客户端漏传 `anthropic-version` 时补 `2023-06-01`）。`/v1/embeddings` 等 Shared Endpoint 没有的路径本地直接 404，不打上游。
- **鉴权头只发 `Authorization`**，与 Ollama 同理，不附带 `x-api-key`。
- **会话粘性顺带命中上游缓存**：Shared Endpoint 会把请求路由到「最可能缓存了该提示前缀」的容器，靠 `Modal-Routing-Affinity-Key` 头聚合一轮对话。本服务把**自己算出的会话指纹**（HMAC，不是原始会话值或提示词）塞进这个头，所以同一会话在 Modal 侧也会落到同一容器。该头只在上游发出，不影响下游客户端。
- **额度**：Shared Endpoint 按 token 从工作区余额计费，**没有额度接口**（且套餐额度不能用于 Shared Endpoint）。本服务只在遇到 402 时冷却账号；要限量请用 Modal 的 [spend limits](https://modal.com/docs/guide/budgets#spend-limits)。概览页显示每个 token 的可用 Endpoint 数，不显示余额。
- **最省成本的死活检查**：`/v1/models` 是唯一能免费判定 token 好坏的地方 —— 无效 token 回 401，完整 token 回它能访问的 Endpoint 列表。所以额度轮询把这里的 401 当作「token 失效」标记出来（按失败次数退避重读），而请求路径上的 404 一律当请求方写错模型名，不冷却账号。
- **token 只填了一半的报错**：完整 token 是 `wk-<id>.ws-<secret>`。只填 `wk-<id>` 时 Modal 的每个 host、每条路径都回 401 `proxy auth required: … supply your proxy token as Bearer wk-<id>.ws-<secret>`，本服务会把它识别成「Token 不完整」并在账号列表里直接标出来，而不是当成上游故障。token secret 只在创建时显示一次，看不到就新建一个。
- 实测（2026-10-31，一个 deepseek-v4-1-flash + 一个 kimi-k3 的 Shared Endpoint）：`/v1/chat/completions`、`/v1/responses`、`/v1/messages`、流式全部可用；`/v1/embeddings` 返回 404，所以本池不接。`/v1/models` 返回的每个 `id` 就是 Endpoint hostname，可直接当模型名用。
- 错误规则独立一套（`modalErrorRules` / `modalRetryRules`，默认见下表）：token 失效 401 隔离、无权 403 隔离、402 余额不足整号冷却（10 分钟起翻倍）、429 按 `Retry-After` 冷却**该模型**（Shared Endpoint 的并发上限是按模型在整个工作区共享的，只有这个模型会 429）、5xx 短冷却换号；404 命中重试规则 `modal-bad-request`，停止换号、原样返回。已有实例升级后需在「路由设置 → Modal 号池」点「恢复默认规则」再保存。
- 分组：不指定分组时进 `modal` 组，各池之间不混放；下游密钥填 `modal` 分组（new-api 里再建渠道，Base URL 填本服务地址，模型名填本服务 `/v1/models` 返回的统一短名称）。导入：开放接口传 `"provider":"modal"` 或直接用 `wk-` 开头的 key。

## Perch 号池（chat.perchai.app）

管理 [Perch](https://chat.perchai.app) 的 Supabase 会话池：导入账号 JSON，之后**自动续期、自动检测状态、读额度**，不需要手工维护 Token。参考 [perchai2api](https://github.com/zoeee1125/perchai2api) 的刷新与 usage meter 实现。

**attribution（`userId` + `workspaceId`）**：官方 CLI 只要缺一个 id 就整体发 `null`，本服务照做。`workspaceId` 只能从 `POST /api/perch-terminal/cli-context` 拿到（与领票同 host 同 token，body `{"query":"","includeIndex":false}`，读 `session.workspaceId`），因为 `/api/perchai/account` 不带它、Perch 也没有 OpenDesign 那种控制密钥可查 `/api/v1/workspaces/current`。该读取挂在额度轮询上，**失败不影响额度**；Perch 某次不再声明 workspace 时会**清空**旧值而不是沿用，避免把 turn 归属到已离开的 workspace。刚导入、还没跑过一轮轮询的账号发的是 `null`。

**这个池和别的池不同：账号没有 API Key。** 凭证是一份 Supabase 会话（Access Token + 会轮换的 Refresh Token），由本服务加密保存并自动续期。

**它同时是一个 OpenAI 兼容代理**：`/v1/chat/completions` 与 `/v1/responses` 会被翻译成 Perch 的 `model-call` 信封，Perch 的事件流再翻译回 OpenAI SSE。下游（new-api 等）像接其他池一样接即可，见 [转发模型调用](#转发模型调用openai-兼容)。

### 导入

「Perch 号池 → Perch 账号 → 导入」，支持单文件、多选、拖拽、ZIP，导入前有预览。兼容 CLI 导出的原始格式：

```json
{
  "schema_version": "1.0",
  "provider": "perchai",
  "auth_provider": "github",
  "account": { "supabase_user_id": "...", "email": "...", "github_user_id": null, "registered_at": "..." },
  "session": { "access_token": "...", "refresh_token": "...", "expires_at": 1800000000 },
  "auth_context": { "supabase_url": "...", "workspace_id": null }
}
```

- **唯一性按 `account.supabase_user_id`**：同一 ID 不会建第二个账号。同一次导入里重复的文件算重复；已在池中的账号只补 Session。
- **旧文件不会覆盖新 Session**：每个会话记 `fetchedAt`，写入时比它旧的直接跳过（管理员手动「更新会话」是例外，那是显式的覆盖）。
- 校验失败、损坏的 JSON 不影响同一批里的其他文件，逐个报原因。
- `auth_context.supabase_anon_key` 可选；不带时用内置的 perchai 公共 key，或「Perch 设置」里填的。

### 自动续期（核心）

- 后台每 `refreshTickSec`（默认 30 秒）检查一次，Access Token 剩余不足 `refreshMarginMin`（默认 10 分钟）就刷新。
- 刷新成功后**立刻落盘**新的 Access Token + 轮换后的 Refresh Token + 到期时间。GoTrue 会作废旧 Refresh Token，所以这一步失败会明确报错。
- **同一账号并发刷新会合并成一次**：进程内按 Supabase User ID 单飞，8 个并发请求也只消耗一次 Refresh Token。不同账号互不等待、各自持有自己的凭证，不会串号。
- **网络故障不等于账号失效**：5xx / 超时 / 429 按 `retries` + `retryBackoffMs` 重试，最终仍是「临时错误」，账号保持原状态。
- **只有刷新令牌被拒才算失效**：401，或 Supabase 的 `400 invalid_grant / validation_failed "Refresh token is not valid"`。这时账号标为「登录失效」并隔离出可用列表，等待重新登录。
- 重启后从 `perch-secrets.json` 直接恢复，继续按原计划续期。

### 状态

自动检测（`checkIntervalSec`，默认 300 秒）用 GoTrue `/auth/v1/user` 校验会话，**不消耗模型额度**。状态词汇：`正常` / `待验证` / `登录失效` / `刷新失败` / `额度耗尽` / `请求限流` / `服务暂时不可用` / `已禁用`。每账号独立判定，一个账号异常不影响其他账号；支持手动检测与批量检测。

### 额度（usage meter）

读 Perch 自己的 `perch_ai_my_inference_usage_meter` RPC（就是「Settings → Usage」页用的那个），**只读已记录的用量，不产生任何模型调用**。单位 PT = 千分之一美元（向上取整）。展示本月已用、手动调用、今日/本周、请求次数、Token 统计、额度恢复时间。

额度读两个接口，**上限来自 `GET /api/perchai/account` 的 `session.entitlements`**（Perch 自己客户端用的就是这条），已用量来自 usage meter：

| 字段 | 来源 | 说明 |
|---|---|---|
| 套餐 | `session.planCode` / `planName` | `pilot`/`demo` 显示为 Starter |
| 月度上限 | `usage.monthly_pt` 的 `value_json.limit` | **权威**，Perch 明确给出 |
| 月度上限（回退） | 套餐代码 | Starter→20,000 PT；**仅当 Perch 没给上限且是 Starter 时**，与 Perch 客户端同一逻辑 |
| 手动调用上限 | `usage.manual_monthly_pt`，否则 `ultra`→225,000 / `pro`→75,000 | |
| 充值余额 | `creditBalancePt` | 计划外额度 |
| Roost | `usage.roost` 的 `unlimited` | |
| 内网/管理员 | `membershipRole` / `planCode=internal` | 不计量 |
| Starter 上限 | `GET /api/perch-terminal/starter-limits` 的 `config.monthlyUsd` | Perch 自己给的免费档额度（实测 20 → 20,000 PT），**不是本程序写死的常数**；只有账号自己没给上限时才用它，标注为「按套餐估算」 |

界面上会标注上限的**来源**（`Perch 明确给出` / `按套餐估算`）。**服务端真的没有上限时仍显示「未知」**，不会编数字。额度读取失败只影响额度显示，不影响 Session 管理。

### 待选套餐（`tierSelectionRequired`）

批量注册出来的新号**还没在 Perch 网页上选过套餐**，Perch 会这样回答：

```json
"session": { "planCode": null, "planName": null,
             "tierSelectionRequired": true, "entitlements": [] },
"usageMeter": null
```

这种账号**没有套餐、没有 entitlement、没有用量**，因此：

- 状态显示为 **`待选套餐`**（红标），剩余额度显示「待选套餐」而不是「未知」——「未知」会让人以为读失败了，其实账号只是还没启用。
- **不猜额度**：即使 Perch 会把这种账号当作 Starter，本服务也不给它 20,000 PT，因为它的主人还没选，写个数字就是把猜测当事实。
- 模型调用同样不可用，直到完成选择。

已导入的待选套餐账号仍可在 `chat.perchai.app` 登录并选择 Starter。新注册功能会使用账号自己的会话选择 Starter，并通过网页聊天接口发送首条消息，成功后才允许加入号池；套餐和额度以随后轮询结果为准。此前测试的 `/api/perch-terminal/billing/*` 不接受 Supabase access token，注册流程改用 Perch 的 Starter RPC 和网页会话。


### 转发模型调用（OpenAI 兼容）

Perch 的托管模型不是 OpenAI 接口：客户端把一份**自己的信封** POST 出去，读回**自己的事件流**。这种信封有两条，由「Perch 设置 → 模型调用 → 调用通道」选择：

| 通道 | 端点 | 实测（2026-11-01，Starter/`pilot`） | 能带什么 |
|---|---|---|---|
| **`cli`（默认）** | `POST app.perchai.app/api/perch-terminal/model-call` | **200**（用本服务自带的前缀） | 完整：system / **tools** / images / temperature / max_tokens / response_format / tool 结果回传 |
| `web` | `POST chat.perchai.app/api/perchai/chat` | 200，不需要任何前缀 | 一条用户消息；system 被折进去，**tools 被丢弃** |

默认走 `cli` 是因为只有它支持工具调用：编码 agent、agent 框架都要 tools，`web` 通道给不了。`web` 通道留着作为退路（万一某个账号或时段 `model-call` 被拒）。

> **部署注意**：`config.json` 里已保存的 `gateNotice` / `identityGuard` / `neutralSystem` / `gatePrefix` **不会被新默认值覆盖**。升级到 cli 通道后，请确认这四项不是旧文案（旧的 notice 会让每个请求 403），或在管理台「Perch 设置」里点一次「恢复默认」/ 清空前缀。

### GitHub 注册与入池

「Perch 号池 → Perch 注册机」使用已有 GitHub 账号进行 OAuth 登录，每行填写 `账号----密码----2FA密钥----接码页`。可指定代理与入池分组，勾选自动入池，或在 Starter 激活成功后选择账号入池。注册、会话校验、Starter 选择和首条 `hi` 消息按顺序执行；激活失败不会入池。任务串行执行，未入池结果仅在服务内存中保留，服务重启后消失。发送首条消息可能使用套餐额度；上游策略版本、登录页面或验证方式改变可能导致失败。

本地运行需要 Python 3.10+ 和 `curl-cffi`：`python3 -m pip install -r tools/perch-register/requirements.txt`。启动目录应为项目根目录；也可通过 `PERCH_REGISTER_WORKER` 指定 `worker.py` 绝对路径，`PERCH_REGISTER_PYTHON` 指定 Python 解释器。Docker 镜像包含这些依赖。GitHub 密码、2FA 和接码页只用于当前任务，不存入账号池；入池后正常加密保存 Supabase 会话。新账号入池时沿用注册代理；已有账号的代理不同则暂停入池，需先核对。注册任务可修改排队中和已激活待入池账号的代理；注册或激活进行中不可修改。已入池账号可在「Perch 账号」修改代理（单个或批量）；设置只影响后续请求，已在途请求不会中断。

### 自动补号

「Perch 号池 → 自动补号」可预先导入 GitHub 候选账号和代理库存，设置目标分组、检查间隔（至少 60 秒）和最低可用账号数。任务默认暂停；启用后按间隔检查，每轮最多注册并激活一个账号，再自动入池。每个候选固定使用分配的代理；没有代理时暂停，不自动直连。计数使用该分组状态为「正常」的账号，不代表指定模型一定可用。出现网络、限流或待核查等不确定状态时暂停补号，避免故障期间误消耗候选。

候选凭据、代理库存及处理中会话加密保存在 `data/perch-signup.enc`，密钥由 `data/metadata.json` 的 routingSecret 派生；备份和恢复需同时保留这两个文件。任务设置保存在 `data/perch-autosignup.json`。这不是独立密钥保险库：能同时读取上述文件的进程仍可能解密凭据。成功入池后自动清除候选中的 GitHub 密码、2FA、接码页和临时会话；列表仅展示脱敏代理。

代理可以在三处修改，作用范围各不相同：

- **代理库存** 每行的「修改」就地改写这一条，ID 不变，之后领取任务的候选按序使用新地址。已经绑定旧地址的候选**不会**跟着变——它们的代理就是当次注册实际走的出口，需要单独改；新地址不能与库存中另一条重复（按「协议+主机+端口」判重，不同账号密码算同一条）。
- **候选账号** 每行可「修改代理」或改为直连。运行中的候选必须等本轮结束，已入池的候选不再可改（账号已在号池里，改那边）；待核查、已就绪、失败、排队中都随时可改，密码类凭据不会回填到页面。
- **Perch 账号** 单账号在操作菜单里「修改代理」，多个账号可用「批量操作 → 修改代理…」，也可清空改为直连。批量按所选或按状态筛选的账号统一替换同一个代理；地址非法时整批拒绝，不会改一半。三处的改动都只影响之后的请求，已在途的请求不会中断。

进程中断或激活结果不确定时，候选标为待人工核查，不自动重复注册。若已保存会话，可显式验证激活（可能发送首条消息），再确认入池。磁盘写入失败时会话可能仅保存在内存，页面会提示立即重试保存，进程退出会丢失该内存内容。Perch 明确返回结构化 `banned` 时账号会自动硬隔离，普通 401、限流或网络错误不作为封禁证据；隔离可在账号管理中人工恢复。

### 开放导入接口（给注册机用）

```http
POST /open/v1/perch/import
Authorization: Bearer sk-odbimp-…
Content-Type: application/json
```

Perch 账号没有 API Key，凭证就是一份 Supabase 会话，所以这个接口直接吃**账号导出 JSON**（和 CLI 导出的格式完全一致，也就是「Perch 账号 → 导入」用的那个文件）。

**单条**（导出文档原样放顶层）：

```json
{
  "schema_version": "1.0",
  "provider": "perchai",
  "auth_provider": "github",
  "account": { "supabase_user_id": "...", "email": "a@b.c", "github_user_id": null, "registered_at": "..." },
  "session": { "access_token": "...", "refresh_token": "...", "expires_at": 1800000000 },
  "auth_context": { "supabase_url": "https://xxx.supabase.co", "workspace_id": null }
}
```

**批量**（每次最多 200 条）：

```json
{
  "accounts": [ {导出文档}, {导出文档} ],
  "tags": ["signup-batch-1"],
  "group": "perch",
  "name": "可选：覆盖账号显示名"
}
```

也接受两种省事的写法，注册机自己拼字段时更方便：

- **扁平字段**：`supabaseUserId` / `email` / `accessToken` / `refreshToken` / `expiresAt` / `supabaseUrl` / `supabaseAnonKey` 直接放对象上；
- **`sessionJson`**：把导出的 JSON 原文作为字符串塞进来。

**响应**：

```json
{ "batch":"imp_…", "total":3, "added":2, "updated":0, "duplicate":1, "skipped":0, "failed":0,
  "results":[{"index":0,"status":"added","accountId":"acc_…","userId":"…","email":"…"},
             {"index":1,"status":"unchanged","error":"与已保存的 Session 完全相同"}] }
```

状态：`added`（新建）/ `updated`（补了更新的 Session）/ `unchanged`（重复或已是最新）/ `error`（附原因）。

规则与手动导入完全一致（同一个代码路径）：

- **`supabase_user_id` 是唯一身份**：重复提交返回 `unchanged`，不会建第二个账号。注册机可以放心重试。
- **旧文件不会覆盖新 Session**：导入时按 token 的 `expires_at` 反推签发时间（Access Token 有效期 1 小时）作为 `fetchedAt`，比已存 Session 旧的一律跳过。注册机重放几小时前导出的文件不会把已续期的账号打回原样。
- **单条失败不影响同批**：`accounts` 里某条缺字段 / 不是对象，只影响它自己，其余照常导入，响应里逐条给原因。
- **鉴权**用现有的导入密钥（`sk-odbimp-…`），连续失败同样限流。**绑定到其他号池的导入密钥会被拒绝**（403），避免注册机把 Perch 账号塞进 River 池。
- 导入的账号立即参与续期、状态检测和额度同步。记录在「Perch 设置 → 事件日志」里。

### 下游密钥与导入密钥

Perch 账号没有 API Key，但**下游要调本服务仍然需要一把 `sk-odb-…` 密钥**，自动上号脚本也需要一把 `sk-odbimp-…` 导入密钥。这两把都是普通的密钥，只是分组是 `perch`，在 **「Perch 号池 → Perch 密钥」** 页直接新建即可（和其他号池的「下游密钥」页是同一套后端，`provider=perch`）。

- **下游密钥**：new-api 渠道的密钥填它。只调度 `perch` 分组的账号，不会去借别的池。Perch 翻译 `/v1/chat/completions` 与 `/v1/responses`，`/v1/messages`、`/v1/embeddings` 在本地直接 404。
- **`GET /v1/models` 由本服务回答**：Perch 没有 OpenAI 兼容的模型列表（`app.perchai.app/models` 是一个 307）。本服务从 `/api/perchai/models`（Perch 网页版自己的模型选择器数据源）同步每个账号的目录，把全池并集发布给 `GET /v1/models`，所以 new-api 的「从上游获取模型」能用。目录由额度轮询同步（`quota.intervalSec`），刚启动时可能要等一轮。
- **`locked` 不影响可用性**：Perch 目录里的 `locked` / `requires: pro` 只是网页端的付费墙标记，model-call 代理照样服务这些 id（entitlement 里 `limits.allowed_models` 是 `[*]`）。所以本服务把目录里的所有 id 都算作可调用，不会因为 `locked` 而藏掉模型。
- **导入密钥**：自动上号脚本用它鉴权 `POST /open/v1/perch/import`。属于别的号池的导入密钥会被 403 拒绝。
- 密钥的池写在密钥本身上（`provider: "perch"`），所以即使 `perch` 分组里还没有账号，这把密钥也仍然是 Perch 号池的，不会被误判成 OpenDesign。

### 分组、批量与导出

- 分组默认 `perch`，可批量移动到任意分组；每组的账号数与正常账号数在 `/api/perch/groups`。
- 批量：启用、禁用、删除、检测、续期、同步额度、移动分组、设置标签、更新会话。**单个账号失败不会中断整批**，每个账号单独返回结果与原因。
- 「可用账号」`/api/perch/available` 只返回真正可用的账号（正常状态、启用、会话有效），供授权场景下**人工指定**账号使用 —— 本服务不做轮换规避套餐限额。
- 导出脱敏（无任何 Token）；备份用口令加密（PBKDF2-SHA256 + AES-256-GCM），恢复时已存在且更新的 Session 不会被旧备份覆盖。

### 设置（`perch`）

| 字段 | 默认 | 说明 |
|---|---|---|
| `supabaseKey` | 空 | Publishable key；空则用内置公共 key |
| `appUrl` | `https://app.perchai.app` | 读额度与模型目录的 host |
| `refreshMarginMin` | 10 | 到期前多少分钟续期 |
| `refreshTickSec` | 30 | 续期检查间隔 |
| `refreshConcurrency` | 4 | 同时续期的账号数 |
| `retries` / `retryBackoffMs` | 3 / 2000 | 临时错误重试（令牌被拒不重试） |
| `checkIntervalSec` / `checkConcurrency` | 300 / 4 | 状态检测 |
| `quotaIntervalSec` | 600 | 额度同步 |
| `logRetentionDays` | 30 | Perch 事件日志保留天数 |
| `alertOnFailure` | `true` | 概览页异常提醒 |
| `adminReadOnlyView` | `false` | 连 Token 尾号也不显示 |
| `surface` | `cli` | 模型调用走哪条通道：`cli`（`model-call`，支持 tools/system，需要 ≥200 字符的前缀）或 `web`（网页 `/api/perchai/chat`，无需前缀但**没有 tools**） |
| `gatePrefix` | 空 | **仅 `cli` 通道**的 surface gate 前缀；留空用本服务自带的 `DefaultGatePrefix`（652 字符），也可以填自己的 |
| `lane` | `chat` | **仅 `cli` 通道**：`chat` 体验最稳；`code` 不注入 Perch 的 operator persona |

### turn ticket（每次模型调用前的准入票）

依据 `.analysis/perch-cli-fidelity-report.md` §4A 与 `.analysis/perch-turn-ticket-flow.md`。官方 `perch run` **不是**直接发模型请求：它先向 `/api/perch-terminal/turn-ticket` 领一张限次票，之后该 turn 的每次 `model-call` 都带 `x-perch-turn-ticket`。此前本服务从不领票，这是“长得像 CLI 但不是 CLI”最硬的一条。

| 环节 | 官方 CLI | 本服务 |
|---|---|---|
| 领票 | `POST /api/perch-terminal/turn-ticket`，body `{surface:"cli", profile:"standard"}`，8 秒超时 | 同路径同 body 同超时；每账号一份 |
| 带票 | `x-perch-turn-ticket: <ticket>` | 同 header |
| runId | 取票返回的 `runId`，整个 turn 复用 | 同（`BuildEnvelope` 由票的 runId 覆盖） |
| 续期 | 距 `expiresAt` < 30 秒时 `{renew:true, ticketId}` | 同 |
| 配额 | `standard=40` / `flock=250` 次，客户端**只计数不拦截** | 同：计数供诊断，拦截交给服务端 |
| 失败 | 仅 `enforced` 才让 turn 失败，否则降级为“无票继续” | 同 |

**`turn_rate_limited` 必须终止请求，不得换号重放。** 规则 `perch-turn-rate-limited` 已改为 `ActionIgnore`：这是套餐配额，换号重放同一请求正是配额规避，也正是此前“用十分钟就封号”最可能的放大机制。

新增设置：

| 字段 | 默认 | 说明 |
|---|---|---|
| `turnTicket` | `true` | 是否领票。关掉发出的就是不领票的报文形状，仅在排查时用 |
| `ticketProfile` | `standard` | 票的配额档；本服务只申请 `standard`（没有 CLI 的重提示词语法，不该申请更大的额度） |

诊断接口 `perch/fingerprint` 现在额外返回每个账号的 `hasTicket` / `ticketRunId` / `ticketUses` / `ticketRemaining` / `ticketExpiresAt`，这些是本地缓存的观察，不代表服务端判定。

### held-context 增量的容量（`deltaTtlMin` / `deltaMaxKeys`）

官方 CLI 把这两个数写死在代码里（9 分钟 / 32 个键 / 每键 4 个快照），因为一个进程只服务一个账号。本服务把它们做成设置，因为池子里同时在跑的对话远多于一个。

| 字段 | 默认 | 说明 |
|---|---|---|
| `deltaTtlMin` | `30` | 服务端确认「已持有」的前缀保持多久仍发增量，单位分钟（1–60） |
| `deltaMaxKeys` | `8192` | 缓存的「账号 + runId」前缀条目上限（1–65536） |

**读数前先理解代价。** 快照过期**不会报错**：`find()` 跳过它，请求退回全量，一次请求、不多领票、不多花额度。而信任窗口**长于服务端自己的记忆**才是亏的那种：本服务声称一段服务端早已丢掉的前缀 → 吃一个 409 → 再发全量，**一次请求变成两次**。所以 `refusedDeltas` 持续增长就是窗口太大的直接证据。

同理，每个缓存条目存的是**每条消息一个 SHA-256**：一个 100 轮对话的一个快照约 6 KB，每键 4 个快照。8192 键满载是百 MB 量级，这就是上限存在的理由。

`GET /api/perch/fingerprint` 的 `delta.limits` 回显当前生效的窗口与上限，`pcDeltaLive` 在「Perch 设置 → Held-context 增量」下方展示命中率、被拒次数与少发的消息/字节数。

### 设备标识、指纹与遥测

依据 `perchai-cli@2.4.121` 的静态分析（`.analysis/perchai-cli-2.4.121/analysis.md` §设备、指纹及遥测），官方 CLI 的身份面很小且基本确定，这里逐项对齐，但**一律按账号隔离**：

| 机制 | 官方 CLI | 本服务 |
|---|---|---|
| machine-id | `~/.perch/machine-id`，随机 UUID，首次生成后持久 | `data/perch-device.json` 里**每个账号**一份，AES-256-GCM 加密，0600；账号键是 HMAC 摘要，文件里看不出有哪些账号 |
| 线程 ID | `cli-cwd-` + `SHA256(resolve(cwd))` 前 16 位十六进制 | 调用方没给会话时用同一算法，哈希的是数据目录（不跟随请求内容，也不泄露路径） |
| User-Agent / locale | `perchai-cli/<版本>` + `x-perch-locale` | `cliVersion` / `locale` 可配；web 通道不发 locale（与浏览器一致） |
| PostHog 匿名事件 | 5 个固定事件，`distinct_id` 取 machine-id；`CI` / `PERCH_NO_TELEMETRY` / `DO_NOT_TRACK` 任一为真则禁用 | 同字段同事件；`posthogKey` 为空即不发（2.4.121 编译进去的 key 就是空串） |
| `cli-turn` 会话上报 | `POST /api/perch-terminal/cli-turn`，带 prompt / assistantText / threadId / runId / events；**不检查**上面三个 opt-out 变量 | 同路径同字段；重试预算照抄：流式或无 runId 1 次、有 runId 的终态 3 次，单次 45 秒（流式 12 秒），间隔 1s/2s/4s，只重试 429 与 5xx |

**默认不向外发任何请求。** `telemetryEgress=false` 时，两份上报照样按官方格式构造，然后**脱敏**写入 Perch 事件日志（`event=cli_turn` / `event=telemetry`，prompt 和回答只留摘要与长度），值班时在日志里就能看到“本来会发什么”。打开 `telemetryEgress` 才真的发出。

新增设置：

| 字段 | 默认 | 说明 |
|---|---|---|
| `cliVersion` | `2.4.120` | User-Agent 声称的版本，与已实现的协议面对齐（含 turn ticket）。**改了版本号不等于协议对上了**：只在实现该版本的协议行为后才应调高 |
| `locale` | `en` | `x-perch-locale` |
| `deviceFingerprint` | `true` | 每账号稳定的机器标识；关掉则每次请求都像一台新机器（本身也是信号） |
| `cwdThreadId` | `true` | 没有会话 ID 时用 `cli-cwd-<sha16>`，而不是每次随机 |
| `cliTurnReport` | `true` | 是否上报 `cli-turn`（仍受 `telemetryEgress` 总开关约束） |
| `telemetryEgress` | `false` | 是否真的发送；关 = 只审计 |
| `posthogKey` / `posthogHost` | 空 / `https://us.i.posthog.com` | 匿名事件的 key 与 ingest 地址 |
| `telemetryOptOut` | `false` | 额外的 opt-out；进程环境里的 `CI` / `PERCH_NO_TELEMETRY` / `DO_NOT_TRACK` 同样生效（只影响 PostHog，与 CLI 一致） |

`GET /api/perch/fingerprint` 只读地列出：每个账号的机器标识与两个时间戳、是否出现**跨账号共用**（`shared`，正常永远不该为真）、当前 UA / locale / 线程 ID，以及上报开关的实际状态。

几点必须说清楚：

- 机器标识是**随机 UUID**，不是操作系统 machine-id、MAC、主机名或硬件序列号——官方 CLI 也不采集这些，没有证据就不该补上去。
- `cli-cwd-` 线程 ID 是摘要而不是明文路径，但低熵路径可以被枚举，所以它是假名，不是保密。
- 版本号要跟协议一起升级：把 `cliVersion` 改成比实际协议新的值，只会让请求特征自相矛盾。
- 上游究竟用哪些信号、权重和封禁条件，客户端分析无从得知；**打开上报会真的增加发往 Perch 与第三方（PostHog）的流量**，先看清审计日志再决定。

### 加密与脱敏

- Session 全部加密在 `data/perch-secrets.json`（AES-256-GCM，密钥由 `metadata.json` 的 `routingSecret` 派生，文件 0600）。**`config.json` 里没有任何 Token**，账号只存 `perchUserId` 和占位运行密钥 `perch-session:<supabase_user_id>`。
- 管理台与所有 API 都不返回 Token，只返回脱敏摘要（是否有 Token、到期时间、尾 4 位、代号）。
- 事件日志（`logs/perch`）记导入、续期成功/失败、会话失效、状态变化、额度读取、批量任务与管理员操作；写日志前再过滤一遍 token 形状的字符串。

## 下游接口

| 路径 | 说明 |
|---|---|
| `POST /v1/chat/completions` | OpenAI Chat，原样转发到上游 |
| `POST /v1/responses` | OpenAI Responses，原样转发 |
| `POST /v1/messages` | Anthropic Messages，原样转发（错误按 Anthropic 格式返回） |
| `POST /v1/*` | 其他 POST 同样转发 |
| `GET /v1/models` | 组内账号可用模型的去重列表，按分组缓存 10 分钟，并发请求合并为一次上游读取；Modal 池发布统一短名称 |
| `POST /open/v1/accounts/import` | 开放导入接口（自动上号），用**导入密钥**鉴权，见下文 |

鉴权：`Authorization: Bearer sk-odb-…` 或 `x-api-key`。响应头带 `X-Request-Id`（可在请求日志里查）和 `X-Attempts`。

### 开放导入接口（自动上号）

给注册脚本或其他账号池用的接口。接入说明见 [docs/import-api.md](docs/import-api.md)。在 **下游密钥 → 导入密钥 → 新建** 得到 `sk-odbimp-…`。导入密钥只能导入账号，不能调用模型；下游密钥和管理员会话都不能导入。

```sh
curl -X POST https://ods.yeschoy.io/open/v1/accounts/import \
  -H 'Authorization: Bearer sk-odbimp-…' -H 'Content-Type: application/json' \
  -d '{"runtimeKey":"sk-amr-…","controlKey":"vela_ctrl_…","name":"账号A","group":"可选","note":"可选"}'
```

Ollama 号池的 key 没有固定前缀，导入时显式声明号池（`provider` 省略时由 key 前缀决定）：

```sh
curl -X POST https://ods.yeschoy.io/open/v1/accounts/import \
  -H 'Authorization: Bearer sk-odbimp-…' -H 'Content-Type: application/json' \
  -d '{"runtimeKey":"<ollama API key>","provider":"ollama","name":"ollama-1"}'
```

- 也可以用 `X-Import-Key` 头；批量用 `{"accounts":[…]}`，每次最多 200 个；`runtimeKey` 必填（也接受 `apiKey`/`key` 字段名），`controlKey`、`name`、`group`、`note`、`proxyUrl` 可选。不填 `group` 时用导入密钥的默认分组。
- **没有重复**：立即入池并启用，备注写入 `开放接口导入 <时间>（密钥 <导入密钥名>）` 和调用方给的 note，有控制密钥的马上刷新额度。
- **有重复**（运行密钥、控制密钥或名称与现有账号相同）：不写 `config.json`，而是放进 `data/staging.json` 暂存区，返回 202 和冲突的账号列表。管理台「账号」页底部的暂存区里可以**覆盖**所选的现有账号（换上新密钥和名称，备注追加覆盖时间，清除旧密钥造成的冷却/隔离）、**作为新账号添加**（仅当没有账号持有这把运行密钥，比如只是重名）或**丢弃**。同一把运行密钥再次提交只刷新暂存条目，不重复堆积；暂存区上限 500 条。
- 密钥、控制密钥、名称都和现有账号完全相同时返回 `unchanged`，不改任何东西，方便脚本幂等重跑。
- 响应：`{"added","staged","unchanged","failed","results":[{"index","status","accountId","stageId","conflicts","error"}]}`。全部成功 200，只有暂存的 202，全部失败 400，鉴权失败 401，同一 IP 鉴权连续失败 5 次后 60 秒内 429。请求体上限 4 MiB，未知字段拒绝。
- 日志只记账号 ID、名称、分组和导入密钥 ID，不记任何密钥。

### 返回给下游的状态码

账号层面的问题不会原样回给 new-api，否则 new-api 会把整个渠道判为失效：

| 情况 | 下游收到 |
|---|---|
| 上游 401/403（换号后仍失败） | 502 `upstream_account_auth_failed` |
| 模型滚动窗口用尽 `model_limit_exceeded`、套餐并发上限 `tier_limit_exceeded`（上游都是 **400**） | 429 + 按网关给的重置时间算的 `Retry-After` |
| 余额不足 `insufficient_balance`（402 或 429）、套餐不含该模型 `tier_model_not_entitled` | 503 |
| 网关自身供应商凭据故障 `upstream_provider_*`（上游 500） | 502，不扣账号健康分 |
| 其他上游 429 | 429 + `Retry-After` |
| 本地所有账号并发/RPM 满载 | 429 `pool_capacity` + 精确 `Retry-After` |
| 账号都在冷却（含该模型冷却） | 429 `pool_cooling_down` + 最早恢复时间 |
| 分组内没有可用账号 | 503 `pool_unavailable` |
| 上游把 400/422 包成 5xx | 还原为 400/422，不冷却账号 |
| 其他 4xx（参数错误等） | 原样透传 |
| 超时 / 连接失败 | 504 / 502 |

## 工作原理

### 选号

每个请求固定使用一份配置快照。流程：硬资格过滤 → 会话绑定命中 → 健康过滤（可选）→ 按优先级分组 → 组内按策略排序 → 并发/RPM 准入 → 取得租约。排序、准入、计数、绑定在同一把锁内完成，中间没有 I/O。

不可调度的账号：已禁用、被隔离、冷却中、该模型冷却中、额度快照证明这个模型没有任何来源能付费（见下文“额度”）。

策略（`routing.strategy`），额度相关的几种参考 [magpie](https://github.com/yetone/magpie) 的 `internal/routes`：

| 策略 | 规则 |
|---|---|
| `smart`（默认） | 余量充足的账号里，最长窗口最早重置的先用（按小时比较），因为剩余额度会在重置时作废；已用 ≥ `lowShare`(90%) 的排后，≥ `spentShare`(98%) 的再排后；再往后是靠套餐模型额度付费的（计费周期先结束的先用，因为不结转），只剩钱包可用的最后；无额度数据的排在已知账号之后 |
| `pace` | 周窗口剩余比例 ÷ 距重置小时数，越高越先；相差 10% 以内视为同档，按近期 token 打散 |
| `least-used` | 已用比例最低优先，其次近期 token |
| `weighted` | 按 `weight` 平滑加权轮询（NGINX smooth WRR） |
| `roundrobin` | 最久未被选中优先 |
| `least-selections` | 选中次数最少优先，平局按在途数 |
| `least-connections` | 在途数最少优先 |
| `ordered` | 按配置顺序，前一个不可用才用下一个 |

`priority`（1–100，越小越优先）先于策略生效：只有高优先级的账号都不可用或满载时才用低优先级的。

下游密钥属于一个分组（`group`），只调度同分组的账号，绝不跨组借号。

### 会话粘性

new-api 打开“透传请求头”后，本服务按以下顺序识别会话，取第一个有效值：

1. Responses 的 `previous_response_id`（上游按账号保存 response，必须回到同一账号；本服务会记录每个 response id 由哪个账号产生）
2. `routing.sessionHeaders` 里配置的自定义头
3. 父会话头：`X-Parent-Session-Id`、`X-Codex-Parent-Thread-Id`、`X-Claude-Code-Parent-Agent-Id`（父子 agent 落到同一账号）
4. 请求体：`prompt_cache_key`、`session_id`、`conversation_id`、`thread_id`（含 `metadata.` 下同名字段）、Claude Code 的 `metadata.user_id` 中的 session
5. 会话头：`X-Claude-Code-Session-Id`、`Session-Id`/`Session_id`、`X-Session-Id`、`X-Session-Affinity`、`Thread-Id`、`Conversation-Id`、`X-Conversation-Id`
6. 兜底：首条 system/developer 和首条 user 消息文本（各截 4096 字节）的 HMAC，弱粘性
7. 都没有：普通选号

注意：OpenDesign 网关自己注入 prompt cache，并按服务端目录加权挑选上游供应商密钥（不按用户），所以会话粘性不保证命中供应商缓存；它保证的是同一会话的计费、限额和 `previous_response_id` 落在同一账号。

会话值经 `HMAC-SHA256(routingSecret, …)` 生成指纹并按分组隔离，原值和指纹都不写日志。绑定表在内存中（LRU，上限 4096 条），显式会话 2 小时滑动过期，弱粘性 15 分钟。

绑定采用两阶段提交：未命中时先写“待确认”绑定，请求真正写到上游后才确认；请求未发出就失败时，只由创建它的请求删除。绑定账号忙时（`onBindingBusy`）：`overflow` 临时借用其他账号但不改绑；`wait-overflow` 先等待再借；`reject` 直接 429。绑定账号被禁用、冷却、隔离、额度耗尽、删除或更换密钥/代理时，绑定失效并改派。

### 并发与 RPM

每个账号可设 `maxConcurrent` 和 `maxRpm`（0 为不限）。网关返回 `tier_limit_exceeded`（如 `membership concurrency limit exceeded 6/5`、`rpm 61/60`）时，会自动记下套餐上限（这里是并发 5 / RPM 60），与手设值取较小者，点“恢复”清除。官方价格页写的“任务并发”（Go 10、Plus 20、Pro/Max 30）指 OpenDesign 应用内任务，和 API 并发上限不是一回事，所以不预设，以网关实际告知为准。准入顺序固定：并发 → RPM，并发满时不消耗 RPM。RPM 是精确的 60 秒滚动窗口：准入时预留，请求写到上游后计入窗口，此后无论成败都不退还；没发出去就失败时退还。租约释放幂等。

全部满载时最多等待 `concurrencyWaitMs`，超时返回本地 429 和精确的 `Retry-After`，记为 `errorCategory: capacity`，不计入健康样本。

客户端断开时中止上游请求，记 499，不写错误日志和健康样本。

### 换号与错误规则

每个请求最多尝试 `maxAccountsPerRequest`（默认 2）个账号，不会重复选同一个。只在**首包之前**、满足以下之一时换号：错误规则把账号冷却或隔离、上游 401、请求根本没发出去（连接/代理失败）。普通 5xx、超时不换号；**首包之后绝不重放**。

`errorRules` 有序、首条命中生效，匹配状态码、正文关键字（大小写不敏感，不支持正则）和响应头；动作是 `ignore` / `degrade` / `cooldown` / `hard-quarantine`，作用于账号或账号×模型。冷却时长依次取：响应头（如 `Retry-After`）→ 正文里 `bodyAfter` 短语后面的 RFC3339 时刻（如 `Try again after 2026-10-23T05:00:00Z`）→ `fallback`，不超过 `max`。`downstreamStatus` 指定这是最后一次失败时回给下游的状态码。未命中任何规则只记一次降级样本，**不会自动冷却**。

默认规则按 OpenDesign 官方客户端的错误目录（[nexu-io/open-design](https://github.com/nexu-io/open-design) `specs/current/run-error-catalog.md`、`apps/daemon/src/integrations/vela-errors.ts`）整理：

| 规则 | 匹配 | 动作 | 回下游 |
|---|---|---|---|
| `platform-provider-credentials` | `upstream_provider_unauthenticated/forbidden`（网关把自己的供应商 401/403 改写成 500） | 忽略：不扣健康分、不换号 | 502 |
| `account-suspended` | `account_suspended` | 隔离账号 | 503 |
| `runtime-key-invalid` | 401 + `invalid_api_key` / `unauthenticated` / `auth_required` | 隔离账号（重新授权后点“恢复”） | 502 |
| `model-window-limit` | `model_limit_exceeded`（HTTP 400，不扣费） | 该模型冷却到网关给的时刻，换号 | 429 |
| `membership-concurrency` | `tier_limit_exceeded` | 账号短冷却（≤1 分钟）并学习上限，换号 | 429 |
| `usage-limit-exceeded` | `usage_limit_exceeded`（API Key 美元用量上限） | 账号冷却，换号 | 429 |
| `insufficient-balance` | `insufficient_balance`、`余额不足`、`预扣费额度失败` 等 | 按额度来源冷却（Design Plan 模型→整号，套餐模型→该号全部套餐模型），10 分钟起逐次翻倍至 6 小时，成功清零；有控制密钥的同时刷新额度 | 503 |
| `tier-model-not-entitled` | `tier_model_not_entitled` / `tier_request_kind_not_entitled` | 该模型冷却 24 小时 | 503 |
| `rate-limited` | 其他 429 | 账号冷却（按 Retry-After，30 秒～5 分钟） | 429 |
| `forbidden` | 其他 403 | 账号冷却 5 分钟 | 503 |

已部署的实例升级后，在“路由设置”点“恢复默认规则”再保存，才会换成新规则。

`retryRules` 命中（默认 400/404/413/422）后立即停止换号，原样返回，避免一个用户的坏参数扩散成全池失败。但如果同一个响应已被错误规则判为账号问题（冷却或隔离），说明不是请求的错，照常换号：上游的 `model_limit_exceeded`、`tier_limit_exceeded` 都是 400。
River / Ollama / Modal 三个池各有一套独立规则（`riverErrorRules`、`ollamaErrorRules`、`modalErrorRules` 与对应的 `*RetryRules`），默认值不同。Modal 池的默认错误规则：

| 规则 | 匹配 | 动作 | 回下游 |
|---|---|---|---|
| `modal-token-invalid` | 401（`invalid proxy auth credentials`，token 不完整或已撤销） | 隔离账号（换 token 后点“恢复”） | 502 |
| `modal-token-forbidden` | 403（token 无权访问该 Endpoint，或被环境 RBAC 限制） | 隔离账号 | 503 |
| `modal-insufficient-balance` | 402（余额不足 / spend limit） | 整号冷却，10 分钟起逐次翻倍至 6 小时，成功清零 | 503 |
| `modal-concurrency-limited` | 429 | **该模型**冷却（按 `Retry-After`，30 秒～10 分钟），换号 | 429 |
| `modal-unavailable` | 500/502/503/504（容器被替换、抢占、冷启动） | 账号短冷却 | — |

Modal 的 404 `unknown inference model` 不冷却账号，由重试规则 `modal-bad-request` 停止换号、原样返回：模型名写错在哪个账号上都是错的。

### 流式

等到第一个完整 SSE 事件并确认不是错误事件后才开始向下游写，因此首包前的错误仍可换号。首包超时 120 秒，空闲超时 360 秒，开始转发后每 25 秒在事件边界写一行 `: PING` 心跳。

### 额度

依据官方[模型额度说明](https://open-design.ai/zh/model-credits/)、[价格页](https://open-design.ai/zh/pricing/)和官方客户端规范 `specs/current/coding-plan-client-billing.md`：

- 模型分两类。**Design Plan 模型**（DeepSeek V4/V4.1、MiMo、GLM-5.3 Flash、Kimi K2.7 Code、Claude Haiku 5.5、GPT-6 Luna 等）先扣 Design Plan 7 天窗口（Go 还有 5 小时窗口），用尽后扣**套餐模型额度**。**套餐模型**（Claude Sonnet/Opus/Fable、GPT-6 Sol/Astra、Gemini、Grok、图片模型等）不走窗口，直接扣套餐模型额度。两者都用尽后请求停止，除非开了自动充值或钱包有余额。
- **套餐能调哪些模型**，以每把 API Key 自己的 `/v1/models` 为准：套餐外的模型不在列表里，或标 `enabled:false`，官方客户端也是按它锁模型的。本服务每 10 分钟读一次（免费，没有控制密钥的账号也读），不在某账号列表里的模型不会派给该账号；分组里所有账号都不支持时，直接回 503 `model_not_entitled`，不打上游。Go 档没有套餐模型，也会排除。下游 `/v1/models` 只返回分组内至少一个账号能调用的模型。套餐外的模型实测是直接不出现在列表里：Go 档的 Key 只列出 12 个 Design Plan 模型；Plus 档 39 个；Pro/Max 档 42 个，多出 `claude-fable-5.1`、`claude-opus-5.5`、`gpt-6-astra`，和价格页一致。用 Key 自己的列表判断，套餐调整时不用改代码。设置里的 `ignoreCatalog` 可以关掉这项检查。
- 一个模型属于哪类，以 `billing/preflight?modelId=` 的 `modelCovered` 为准（`true` 为 Design Plan 模型）。本服务对最近被请求过的模型逐账号查询并缓存 6 小时。价格页的限时活动（如“GPT-6.1 Sol 已加入 Design Plan”、DeepSeek 谷价）**只在 OpenDesign 应用内生效，不适用于 API**，实测 API 下 `gpt-6.1-sol` 的 `modelCovered` 为 `false`。
- 有控制密钥的账号每 60 秒读一次工作区、套餐窗口、钱包和账单摘要（`balances.subscriptionCredits` 是套餐模型额度，`rechargeCredits` 是充值额度），全局并发 2，失败按 60s×2ⁿ 退避（上限 15 分钟）。聊天请求不等待额度刷新。各项分开保存，不相加、不用 `creditsPerUsd` 换算。某一项读取失败时保留上次成功值，不写成 0；控制密钥 401/403 时标记“控制密钥失效”，该账号按额度未知处理。快照超过 15 分钟视为过期。
- 只有拿得出证据时才把账号排除：该模型能用的窗口没有余量，且账单摘要（新鲜的）显示套餐模型额度为 0，且钱包和充值额度为 0（或关闭了 `walletFallback`）。官方规范明确说“钱包为 0 不能证明请求无法开始”，摘要读不到时不排除，交给真实的 `insufficient_balance` 错误处理。

概览页的窗口剩余按工作区去重求和，多个账号属于同一工作区时只算一次，账号列表里会标“共享工作区”。

上面这些只适用于 OpenDesign 号池。**Modal 没有额度接口**：Shared Endpoint 按 token 从工作区余额计费（套餐额度不能用于 Shared Endpoint），余额只能在 Modal 控制台看，本服务只在遇到 402 时冷却账号；`/v1/models` 列的是该 token 能访问的 Endpoint，不能当余额依据。**River 没有额度接口**，只能靠 402/429 推断；**Ollama 有 `/api/balance`**：同一把 key 读自己的赠送额度（`included.balance_usd` / `allowance_usd` 与周期）、老套餐的 `session` / `weekly` 剩余百分比和已购额度 `purchased.balance_usd`，三项分开显示、不相加。判定“额度耗尽”的依据是赠送额度为 0（或周期已过）且已购额度为 0——读失败或超过 15 分钟未更新的快照一律按“未知”处理，不会排除账号。该接口每个用户每分钟限 10 次，同一账号两次读取至少间隔 30 秒。

### 只有 API Key 的账号

只填 `sk-amr-` 运行密钥、不填控制密钥完全可用，只是看不到剩余额度：

- **能调哪些模型**：照样按这把 Key 自己的 `/v1/models` 判断（不需要控制密钥）。
- **扣哪份额度**：有任一带控制密钥的账号时按 preflight 判断模型类别，否则用内置的 Design Plan 模型清单（价格页列出的 12 个，也是 Go 档 Key 的完整模型列表），清单外、但出现在某把 Key 模型列表里的算套餐模型。
- **额度用完**：靠上游错误推断。`insufficient_balance`（余额不足）出现在 Design Plan 模型上，说明窗口、套餐模型额度、钱包都空了，整个账号冷却；出现在套餐模型上，只冷却这个账号的全部套餐模型，Design Plan 模型照常用。冷却从 10 分钟起，同一账号连续命中逐次翻倍，最长 6 小时，成功一次清零。网关的余额预检在推理前拒绝，不扣费，所以探测是免费的。`model_limit_exceeded` 自带重置时刻，按它精确冷却。
- **选号**：额度未知的账号排在额度已知、还有余量的账号之后；全是这种账号时按选中次数和在途数平均分配。
- **界面**：显示“仅 API Key”、近 24 小时请求数和 token、推断出的额度耗尽冷却和连续失败次数。要看真实额度，用“重新设备授权”补上控制密钥。

### 模型价格与费用
“路由设置 → 模型价格”按每百万 token 配置各模型的输入、缓存命中、输出价格（厂商原币种，不换算），默认已按 2026-10-23 查到的官方价格填好；备注里写“请核对”的不是从官方页读到的。请求日志据此给每笔请求算费用并显示缓存率，概览显示 24h 费用（人民币、美元分开）。
- DeepSeek 谷峰计费：价格填的是空闲价，“高峰倍数”填 2；北京时间工作日 9-12、14-18 按倍数计，法定节假日在“高峰例外日”里填日期。
- 同一模型可配多行，“生效日期”到了的最新一行生效（用于已公布的调价，如 gemini-4-argon 12-01 起翻倍）。
- 长上下文：提示 token 达到阈值时整个请求按“长”价。缓存写入 token 上游没有给出，不计。
- 未配价格的模型、没有用量的请求不计费用。

### 日志与统计

- 请求日志 `data/logs/requests-*.jsonl`：每个请求一行终态记录（账号、选号原因、绑定结果、状态、上游状态、耗时、首字时间、token、每次尝试）。
- 错误日志 `data/logs/errors-*.jsonl`：每次失败的上游尝试一行，用 `requestId + attemptIndex` 与请求日志关联；原因先脱敏，再截断到 16 KiB，非 JSON 正文只记字节数。
- 5 MiB 分段，单条 ≤ 64 KiB，保留 30 天、合计 ≤ 100 MiB，写入走异步队列，写盘失败时丢弃并计数，不影响模型流量。
- 请求日志的 `protocol` 字段标明入站协议：`openai-chat` / `openai-responses` / `anthropic-messages` / `openai-completions` / `openai-embeddings`，可按协议筛选。
- 详细日志 `data/logs/details-*.jsonl`：**默认关闭**（路由设置 → 日志 → 详细日志）。开启后每个请求记客户端侧的请求头、请求体、响应头、响应体（流式记原始 SSE），每个体截断到“单个体上限”（默认 64 KB），保留条数默认 500。`Authorization`/`x-api-key`/`Cookie` 等头整体打码，其余内容过一遍密钥脱敏，但提示词和模型输出会明文落盘，排查完请关闭并清空。
- 除开启详细日志外，日志不包含：任何密钥、代理 URL、请求头值、请求/响应正文、会话原值或指纹、账号备注。

统计在请求结束时写入分钟桶（24 小时），不从日志推导。

## 数据目录

| 文件 | 内容 |
|---|---|
| `config.json` | 账号（含密钥）、下游密钥与导入密钥的哈希、设置、规则、管理员密码哈希（PBKDF2-SHA256） |
| `staging.json` | 开放导入接口暂存的重复账号（含密钥），等待管理台确认覆盖、添加或丢弃 |
| `metadata.json` | 账号状态（冷却/隔离）、健康样本、额度快照、统计、`routingSecret`。冷却、隔离、额度和配置变化 15 秒内写盘；只有统计和健康样本变化时最多每分钟写一次 |
| `selection-counters.json` | 选号计数，最多每 5 秒写一次 |
| `perch-secrets.json` | Perch 号池的加密会话（Access / Refresh Token、Supabase 项目地址与 key），AES-256-GCM 封装，0600；`config.json` 里不含任何 Token |
| `perch-device.json` | Perch 号池每账号的设备标识（machine-id、首次运行/首次发消息时间），同样 AES-256-GCM 封装、0600；账号键为 HMAC 摘要。丢失等于所有账号“换了设备”，请和 `metadata.json` 一起备份 |
| `logs/` | 请求与错误日志，缓冲写入，每秒刷盘；查询读内存，不受影响 |

所有文件 0600、目录 0700，写盘先写临时文件再 rename。JSON 损坏时启动失败并保留原文件。**整个目录都是机密，不要提交或共享。**

`config.json` 也可以手工编辑，然后 `kill -HUP <pid>` 或管理台调用 `/api/config/reload` 热加载，在途请求继续使用旧快照。

### 主要配置项（`routing`）

| 字段 | 默认 | 说明 |
|---|---|---|
| `strategy` | `smart` | 见上表 |
| `sticky` | `true` | 会话粘性 |
| `onBindingBusy` | `overflow` | `overflow` / `wait-overflow` / `reject` |
| `explicitTtlSec` / `fallbackTtlSec` | 7200 / 900 | 绑定滑动过期 |
| `fallbackHash` | `true` | 无会话标识时按消息哈希弱粘性 |
| `sessionHeaders` | `[]` | 额外会话头 |
| `concurrencyWaitMs` | 2000 | 满载等待，0–30000 |
| `maxAccountsPerRequest` | 2 | 1–5 |
| `ignoreCatalog` | `false` | 不按各 API Key 的模型列表判断套餐权限 |
| `walletFallback` | `true` | 窗口和套餐模型额度都用尽后，是否允许只剩钱包/充值额度的账号付费（真金白银） |
| `lowShare` / `spentShare` | 90 / 98 | 已用达到多少 % 开始靠后 / 视为耗尽（按已用百分比，不是剩余） |
| `healthFilter` / `minimumHealth` | `false` / 0.2 | 24h 成功率过滤，样本少于 20 视为健康 |
| `firstEventTimeoutSec` / `idleTimeoutSec` / `heartbeatSec` | 120 / 360 / 25 | 流式 |
| `requestTimeoutSec` | 900 | 非流式总超时 |
| `maxBodyMB` | 64 | 请求体上限 |

`quota`：`enabled`(true)、`intervalSec`(60)、`concurrency`(2)、`staleSec`(900)。`logs`：`retentionDays`(30)、`maxRequests`(50000)、`maxErrors`(10000)、`maxTotalMB`(100)。账号：`maxConcurrent`、`maxRpm`、`weight`(1–100)、`priority`(1–100，默认 50)、`proxyUrl`（http/https/socks5/socks5h，代理失败不会回退直连）。

## 限制

- **单进程**。并发计数、RPM 窗口、会话绑定都在内存中，不跨进程共享，不要多副本部署同一个数据目录。重启后会话绑定清空（下一轮请求会重新绑定）。
- 设备授权的批准必须在目标账号已登录的浏览器里完成；本服务不接触 cookie。
- 控制面读不到是否开了自动充值。开了自动充值、但窗口和各项额度都为 0 的账号会被判为额度耗尽而不调度。
- `model_limit_exceeded`、`tier_limit_exceeded` 的正文格式来自官方客户端源码，尚未在本账号上真实触发过，见 [docs/upstream-findings.md](docs/upstream-findings.md)。
- **Modal 没有额度接口**：工作区余额只能在 Modal 控制台看，本服务靠 402 推断冷却；`/v1/models` 返回的是 Endpoint 列表，不能当余额依据。
- **Modal 的 404 不等于 token 失效**：请求路径上的 404 `unknown inference model` 只说明模型名不是本 token 可见的 Endpoint，token 好坏以额度轮询读 `/v1/models` 的结果为准。
- Modal 下游使用统一短名称，价格表也按下游请求的模型名配置；上游 Endpoint hostname 由本服务按账号映射。

## 回滚

停掉进程，换回旧二进制，用同一个数据目录启动即可。升级前建议备份 `data/config.json` 和 `data/metadata.json`。`metadata.json` 或 `selection-counters.json` 损坏时可以删除（会丢失冷却状态、健康样本、统计和计数，并生成新的 `routingSecret`，已有会话绑定失效），`config.json` 不能删。

## 开发

```sh
go vet ./... && go test -race ./...
```

集成测试用临时目录和本地 mock 上游，不使用真实凭据。
