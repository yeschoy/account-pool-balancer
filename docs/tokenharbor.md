# Token Harbor 号池

官方文档：https://tokenharbor.ai/docs 。本实现按 2026-11-05 公开契约接入原生文本 API，不把它转成 OpenDesign，也不做跨协议转换。

## 接入

1. 管理台切换 **Token Harbor 号池**。建议由注册机原记录显式校验入池，以关联保存的登录会话；手动/批量导入 `thk_…` Key 仍可保存，但没有唯一原会话时套餐未知、暂停调度。默认分组 `tokenharbor`，自定义分组不能混用其他供应商。
2. 在本池「下游密钥」新建 `sk-odb-…`，分组与账号一致。此密钥只调度该分组，不能借其他池账号。
3. 默认上游 `https://tokenharbor.ai/v1`，可在本池路由设置修改（必须含 `/v1`，无 URL 凭据、查询串或 fragment）。上游 key 在管理 API 只显示掩码，不提供原文读取。
4. 通过中转站时开启会话/缓存头透传。账号可设固定 HTTP/SOCKS 代理、并发/RPM、优先级和权重；代理失败不会自动直连。

### Codex

`~/.codex/config.toml`（将地址替换成实际服务 HTTPS 地址）：

```toml
model = "从本池 /v1/models 选模型 ID"
model_provider = "tokenharbor_pool"

[model_providers.tokenharbor_pool]
name = "Token Harbor Pool"
base_url = "https://pool.example/v1"
wire_api = "responses"
env_key = "TOKENHARBOR_POOL_KEY"
```

环境变量 `TOKENHARBOR_POOL_KEY` 填本服务下游 `sk-odb-…`，不是上游 `thk_…`。

### Claude Code

`~/.claude/settings.json`：

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "https://pool.example",
    "ANTHROPIC_AUTH_TOKEN": "本服务下游 sk-odb-…"
  }
}
```

模型使用 `/v1/models` 中的实际 ID。本池向上游始终发选中账号的 Bearer key；Messages 缺少 `anthropic-version` 时补 `2023-06-01`。

## 支持边界

| 接口 | 行为 |
|---|---|
| POST `/v1/chat/completions` | 原生 JSON / SSE |
| POST `/v1/responses` | 原生 JSON / SSE；Codex 接入文档明确 `wire_api=responses` |
| POST `/v1/messages` | 原生 JSON / SSE；Claude Code |
| GET `/v1/models` | 分组内已启用、套餐已验证账号的覆盖模型并集，只读本地快照 |

推理请求必须显式提供非空、唯一且小写字段名的 `model`，包含 `previous_response_id` 的续传也一样；无效 JSON、重复/大小写歧义 model 本地拒绝，防止绕过套餐门禁。通过校验的请求正文原样发送，不删除 tools、不改写提示词、不丢 `cache_control`、`prompt_cache_key`、工具 ID 或供应商字段。保留会话头、缓存头及 Anthropic beta 头。不注入供应商未公布的缓存路由头。

未实现的路径（embeddings、completions、图像、`responses/compact`、`messages/count_tokens` 等）本地 404，不探测上游；不实现 WebSocket。公开限流页提及图像不等于本轮实现了完整图像协议。

## 会话与缓存亲和

沿用原调度器，Token Harbor **默认启用会话粘性，绑定忙碌不溢出**：有短暂准入等待预算，忙时返回 429，而不是为了均衡把同一历史分散到另一把 key。路由与错误规则独立保存；其他选号策略沿用现有池。

识别优先级：

1. `previous_response_id` 的已确认绑定。
2. 管理员额外会话头、父会话头/`metadata.parent_session_id`。
3. 会话字段和头：`Session_id` / `Session-Id`、`X-Session-Id` / `X-Session-Affinity`、`X-Codex-Session-Id` / `X-Codex-Thread-Id`、`X-Claude-Code-Session-Id`、`X-Opencode-Session-Id` 及 OpenCode 父会话头、thread/conversation 等；Claude Code 的 `metadata.user_id` 支持 JSON 字符串及 `_session_…` 格式。
4. 没有会话标识才用 `X-Prompt-Cache-Key` / `Prompt-Cache-Key` / `X-Cache-Key` 或正文 `prompt_cache_key`。
5. 可选首条 system/user 的 HMAC 弱粘性。

`cache_control: {type:"ephemeral"}` 是缓存边界，不是会话 ID；不会把所有 ephemeral 请求绑在一起。绑定以分组和下游 key 隔离，普通日志仅记录标识类型/来源，不记录原始会话值或 HMAC。

已绑定账号失效/冷却时，携带完整历史的请求可在同组故障切换，并记录换号及跨账号情况。**Responses 服务端续传不能这样换号**：未知、过期、跨下游 key 或凭证/base/代理变更后的 `previous_response_id` 返回 409；绑定忙时 429；失败不换号。已确认 response ID 绑定可随 metadata 恢复，普通会话绑定不跨重启保留。

发送后结果不确定的网络错误、已向下游提交的 SSE 不重放，避免重复执行工具或重复扣费。凭证请求不跟随重定向。

pi 当前 SDK 支持 `x-session-id` / `session_id` / `x-session-affinity`，但自定义供应商是否发送取决于 API/compat 配置（包括 `sendSessionAffinityHeaders`）。客户端必须实际发送稳定标识，中转站不能删除这些头。不能从 User-Agent 生成会话，也不把常见的每请求 `x-client-request-id` 普遍当作会话 ID。此轮验证了字段兼容与本地 Mock，并非所有客户端实机验收。

### 不保证上游缓存命中

官方说明 Claude 缓存 5 分钟、命中续期；自带 `cache_control` 不被上游改写，未带时上游可能自动加标记。供应商内部连接仍可能变化。本地 key 亲和只能增加复用机会，不能强制 Token Harbor 的内部模型提供商固定到同一连接。

## 模型、额度与限流

- 后台使用唯一原注册记录的**完整 Key、会话**，通过**号池账号自己的固定代理**只读 GET `/pricing` 和 `/api/me/free-tier`。前者读取实际当前 Pass 及官方模型分组，后者读取付费 `plan_used_pct`、`period_ends_at`（缺省用 `reset_at`）、是否耗尽及钱包续费开关；接口名带 free 不代表启用免费模型。无重定向、无失败直连、无付款或隐私设置修改。只有认证 Cookie 真正更新才加密保存，普通轮询及可再生 `session_data` 缓存变化不重写整个注册库。
- 账号同步 `tokenHarbor.accountSyncSec` 默认300秒，启用范围60–300，显式0关闭本池周期检测（包括目录），不依赖其他池的额度开关。目录 `modelSyncSec` 默认1800秒，启用范围60–86400，0仅不周期更新目录；首次确认仍需目录。单账号“刷新模型/套餐”会立即检测三份数据。
- 模型必须同时存在于该 Key 的原生 `/v1/models` 和官网已确认的 Pass 映射，Agent ⊂ Office ⊂ Frontier；不猜历史别名、不使用 API tier 代替 Pass、不借钱包，免费模型还必须有原记录显式启用证明。账号表显示覆盖/目录数，`/v1/models` 只读经过过滤的本地并集，不在推理/列表热路径串行联网。
- 套餐未知/过期、周用量未知/过期、周额耗尽及官网开启 `overage_billing` 均暂停调度。开启“忽略目录”也不能绕过这层门禁；官网钱包续费开启时提示用户自行关闭，不代改账单设置。凭证/base/代理变化及迟到结果不能复用旧证明；套餐证明最多10分钟，周窗口已过但未重新读取时标未知，**不会按时间自动补满**。
- 上述是不主动钱包回退的**缓存观察门禁**，不是上游原子计费锁。官网在两次检测间变更套餐/续费设置仍有竞态，周读数也可能滞后；绝对禁止钱包支出还需上游账户限制及资金管理，本服务不代改设置或挪动余额。
- 概览采用用户指定 **Agent Pass 20/周期、5/周的2×等效展示口径**，不是钱包余额或全模型双倍促销承诺。总额包含已禁用但已确认的 Agent；当前可调度周额逐账号计算 `5 × (1 - plan_used_pct/100)`，排除已知停调度状态并按原注册所有者去重。无有效周读数为未知而非0，Office/Frontier单独计数、不擅自套用20/5。实测单个 Agent 的原始周额为2.5，显示5是所需等效口径，不是修改上游额度。
- 官方免费限流按账号（所有 keys/模型合并）及 IP 共享；付费无 RPM/RPH 上限且所有套餐无并发上限。不默认假设账号免费，不给每把 key 编造独立额度。
- 401 隔离无效 key；402 余额/支出上限冷却（10 分钟起递增，上限 6 小时）；429 遵守完整 `Retry-After`；临时 5xx 短冷却；400/404/413/422 不截断、不删工具、不换号。全部 key 不可用时通常返回 502/503，避免把上游凭证问题当作下游认证失败。
- **大额消费确认硬停止**，优先于可编辑错误规则。保留原始确认提示，不自动确认、不通过换 key 绕过。仅原样透传调用方明确提供的 `x-th-confirm-spend`。该头会允许单次花掉大部分余额，应由用户自行决定。
- 从基础代码版本升级时，已有非空规则不被覆盖；在「路由设置 → Token Harbor → 恢复默认规则 → 保存」获得新增 429/5xx/余额冷却规则。

## 概览与日志

复用现有概览、账号状态、请求/错误/详细日志，按 provider 过滤。请求日志包括选中账号、会话依据、绑定命中/换号、各次尝试、状态、耗时、首字时间、输入/输出、缓存读取与写入 token。

Messages 的缓存占比为 `cache_read / (input + cache_read + cache_creation)`；Chat / Responses 按上游计入 input 的 cached 部分统计。缺失 usage 显示 `—` / null，不捏造为零。Token Harbor 概览缓存率只使用同时报告 input/cache 的样本，避免把未知样本算作未命中。

费用仅依赖管理员价格表估算，不代表上游账单。价格表尚无缓存写入单价，因此包含缓存写入的 Token Harbor 请求不估价、不计入费用合计，写入 token 仍单独展示。全量详细日志仍默认关闭；**已认证 TokenHarbor 请求的失败尝试自动留存有界详情**，即使后续换号成功也保留失败尝试，以 requestId + attemptIndex 关联。错误行“详细请求”可打开对应请求/响应头体；SSE提交后出错同样记录但绝不重放。自动详情每个正文最多64KiB头尾，JSON在裁剪前移除会话/响应/缓存标识，扫描超过8MiB、无效JSON或无法安全处理的片段省略。凭据、Cookie、自定义会话头及错误文本反射出的会话值脱敏；仍可能包含用户提示词/代码，沿用现有管理员访问、条数/总容量与保留策略。成功请求不会因此默认全量采集。

## 容量与内存初始参数

3500 RPM约58.3请求/秒，不是3500并发；3亿TPM对应平均约8.57万token/请求。持续并发约为 `RPM/60 × 请求持续秒数`，不能只按RPM指定内存。

- Compose默认硬内存/含swap总量各8GiB（不额外使用swap），Go软内存6GiB，nofile 131072。可用 `ODB_MEMORY_LIMIT` / `ODB_MEMORY_SWAP_LIMIT` / `ODB_GOMEMLIMIT` 覆盖；这不是线上已应用或已验证的容量承诺。
- 原生输入共享缓存预算默认2048MiB，可由 `ODB_TH_BUFFER_MB` 或 `-th-buffer-mb` 设置，并自动不超过 Go 软内存的1/3。已知Content-Length单次分配；chunked上传先保守预留、读完缩到实际容量。保留到本请求结束，满额直接本地429 `gateway_buffer_limit`、Retry-After=1，不接触上游、不排无界队列。上传读取最长60秒，随后清除读取deadline，不影响长SSE。
- 会话命中只校验原账号的最新硬门禁/健康/准入，不每轮扫描其他账号；忙时需要溢出的配置仍走原完整选择逻辑。目录只在后台更新，不因请求增加网站读数。
- 本地 Apple M4、40并行、每请求459304字节、1000假账号/本地HTTP Mock：JSON约3043请求/秒，p95 32.10ms/p99 43.76ms；50ms合成SSE约732请求/秒，p95 55.26ms/p99 94.20ms。观测输入缓存约17.52MiB，复合进程结束RSS约155.6MiB（含测试客户端、上游Mock及前轮保留堆，不是服务独占/峰值）；**这是整个客户端+网关+Mock的合成路径，不是上游推理或真实TPM认证**。输入单次分配使中间基线约2.14MB/op降至最终约1.60MB/op，实际延迟还包含新增的完整JSON权限校验，不宣称全面加速。
- 8/6/2GiB是初始安全预算，长流/超大非流式响应、出口带宽、共享代理连接/HTTP2流上限、真实模型吞吐和Cookie集中续期仍需部署后在批准窗口验证。旧线上代理连接上限32并不等于32并发，不未经实测直接改成3500。未对真实TokenHarbor压测、未修改在线配置。

## 自动导入

使用 **Token Harbor 池自己的导入密钥**：

```http
POST /open/v1/accounts/import
Authorization: Bearer sk-odbimp-…
Content-Type: application/json

{"provider":"tokenharbor","runtimeKey":"thk_live_…","name":"TH-1"}
```

支持已有批量、冲突暂存与导入记录。Token Harbor 导入密钥不跨池使用。合入本地主分支时保留了主分支已有的「TokenHarbor 注册机 / 设置 / 实时注册日志」及补验证、待验证状态、入池流程；没有新增自动补号。普通「概览 / 账号 / 下游密钥 / 路由设置 / 请求日志 / 错误日志」仍用于本池推理管理。注册设置保存不会重置独立路由或模型刷新间隔，验证码 Key 和带认证的代理不回显。

## GitHub OAuth 注册

注册机自动识别四段输入：`GitHub账号----GitHub密码----2FA密钥----邮箱接码地址`；原邮箱两/三段格式仍兼容。支持 `/mailbox/<token>` 的 MailCat 接码页（含 191006.xyz）以及原 iCloud 接码页。接码页必须返回可解析的邮件时间；不使用时间未知的旧邮件。

- GitHub、Supabase、TokenHarbor OAuth 回调和 `/oauth/finish` 全程使用保存时绑定的授权代理。直接连接/连接代理的本机套接字强制 IPv4，**代理本身也必须提供 IPv4 出口**，不能根据代理入口 IP 推断出口。GitHub 密码、TOTP 和授权表单各只提交一次；设备验证、额外权限、风控、拒绝或未知页面停止，不伪装指纹、不绕过验证、不自动重试。
- 官网实际 OAuth 身份服务入口包含 `https://auth.tokenharbor.ai/auth/v1/authorize?provider=github`（Supabase 自定义域名），也兼容原 `*.supabase.co`。只放行确切官方域名/合法 Supabase 域名的 HTTPS 入口，并将后续身份服务跳转固定到返回的原始 origin；不放行相似域名、任意子域、额外端口、用户信息或 fragment。
- finish 从当前页面动态读取 Action ID/Key，提交实际 URL（保留 next 查询串）的 multipart server action；不硬编码捕获的 Cookie、Action ID、Sentry、客户端指纹或部署头。只有官网确认 dashboard 跳转才记完成，HTTP200 不等于成功；不默认同意免费模型的数据条款。
- 「GitHub 授权后 TokenHarbor 代理」可填写独立 IPv6 出口代理，按账号顺序绑定；留空沿用授权代理。仅确认 OAuth/finish 后切换，创建 Key、邮箱验证、套餐检测走 TH 代理；接码服务显式直连。行内“改代理”只改 TH 出口，不改 GitHub 授权出口。两个域的 Cookie 不互相转发，GitHub Cookie 与 OAuth `provider_token`（如有 refresh token 也保留）独立记录，TH Cookie 续期不会擦掉原 GitHub 授权令牌；失败不换路。
- OAuth 完成不代表 TokenHarbor 邮箱已验证；继续原有创建 Key、邮件验证流程，等待超时保留双会话和 Key，之后可补验证。已完成 OAuth 的重试复用 TH 会话，不重复授权/finish/创建已有 Key，仍须已验证邮箱和有效付费套餐才可手动入池。
- **用户明确选择：新增 GitHub 整行记录（密码、2FA、两套 Cookie、API Key 等）明文保存**在业务文件 `data/tokenharbor-signup.json` 的 `githubRows`；文件原子写入、0600，目录新建0700，不入 Git，管理 API/页面/日志不回显。旧邮箱记录及保存代理池继续使用原加密部分，不全库解密迁移。读取该文件即可取得 GitHub 会话，请限制备份/目录访问；回滚旧版前先备份，旧版写文件会丢弃不认识的 GitHub 行。
- API 附加字段：账号 `provider:"github",login,password,totpSecret,mailboxUrl,githubProxyUrl?,sessionProxyUrl?`；请求 `proxies` 为授权代理池、`sessionProxies` 为 TH 后续代理池。`sessionProxyUrl:""` 显式绑定后续直连。仅保存不会发起外部请求；按 GitHub login 去重，不因授权解析出真实邮箱而重复建行。

验证范围：本地 Mock 覆盖 GitHub 密码/TOTP/授权、动态 finish、分离的授权/会话传输、邮箱验证、失败不重放、恢复不重建 Key、明文与旧加密共存及不回显；未将 Mock 当成真实账号注册/代理出口验收。后续单真实账号实测在固定代理入口即遇 HTTP403（`forbidden ip … not supported`），未到 GitHub 登录、未取得会话/Key，不算完整注册验收。SOCKS5 收到 HTTP 响应、CONNECT 拒绝、DNS/超时等现按固定服务阶段和安全错误类别报告，不输出原始 URL/state 或代理认证信息；不自动改协议、换节点或重试。随后第二个账号经用户明确授权以 IPv4 直连，实测修复了官方自定义鉴权域名漏白名单；到达官网/鉴权服务和 GitHub 授权入口，但上下文登录页超时或返回未识别表单，仍未完成登录、finish、邮箱验证或生成 Key。5个 GitHub 中间 Cookie 与1个 TH PKCE 检查点已私下保存，不代表有效会话；未确认密码错误或平台风控。

## 注册记录保存、代理复用与套餐检测

- **仅保存**：保存邮箱、密码、接码页、分组和分配好的代理，不访问官网、不注册、不解验证码、不付款，也不入池。任务显示“已保存（未注册）”，点击该行“开始注册”才执行；重复提交相同邮箱不会覆盖原密码/代理或自动启动。
- **代理分配**：支持按顺序轮流分配或随机选择（有放回），多个账号可以复用一个代理。保存账号时即绑定结果，之后修改代理池不会重分配既有记录。
- **保存代理池**：点击“保存代理池”将代理加密持久化；输入框会清空，不回显认证信息。后续代理框留空自动复用。优先级：账号显式代理 → 本次输入代理池 → 已保存代理池 → 设置默认代理 → 直连。“清除已保存代理池”需要确认，不影响已有账号的绑定。
- **不自动入池**：取消注册后的自动入池；旧客户端提交 `autoImport:true` 也会被后端拒绝。注册成功、验证邮箱、生成 API Key 或支付链接都不代表已付款。
- **检测套餐**：注册完成后进行只读检测，也可在记录中点击“检测套餐”。使用该账号保存的会话与代理读取官网当前有效套餐；不创建订单、不扣款、不调用推理。显示 Free（未订阅或已到期）、当前有效付费套餐或未知及检测时间。官网当前页面未提供精确到期时间，不编造该字段；页面结构变化、会话失效或网络失败均不能放行。
- **手动入池**：只有邮箱已验证且上次检测为付费套餐的记录显示“校验并入池”，点击后后端仍实时复查；Free、过期、未知或检测失败都不入池。入池与运行门禁分开：不擅自删除账号或修改 enabled，通用 Key 导入仍可保存，但已按用户确认的严格策略暂停套餐/周用量未知账号的实际调度。

### 每个账号独立修改代理

- **注册机**：账号行点击“改代理”，支持已保存、已注册、待验证、失败、中断及“已入池”页中的空闲记录。排队/执行中不能修改，避免任务中途换出口。
- **号池**：在“账号 → 操作 → 修改代理”修改指定账号，也可沿用“编辑”中的出站代理字段。
- **两边独立**：注册机改代理不影响已有号池账号，号池改代理也不回写注册记录。新建入池账号时默认复制注册记录当前代理（包括明确直连）；重复入池按完整 Key 去重，不覆盖已有号池的独立代理。
- 支持 HTTP/HTTPS/SOCKS5/SOCKS5H；输入完整新地址，或明确勾选“清除代理（直连）”。留空不能误清除，旧认证信息不回显。保存代理只更新本地配置，不注册、不付款、不自动启用账号，也不自动验证出口可用。
- 注册机套餐检测、补验证、订阅链接仍走注册代理；号池套餐/周用量/目录检测和推理走号池代理。修改注册代理会清除该记录的旧套餐观察，需重新检测；修改号池代理会使旧套餐/额度及 Responses 续传来源失效，按新代理检测通过后才恢复调度。原登录会话仍按唯一账号/完整 Key 关联，若官网因换出口拒绝会话则保持未知，不自动直连或换代理绕过。

```http
PUT /api/tokenharbor/registrations/{rowId}/proxy
{"proxyUrl":"socks5h://user:password@host:1080"}
# 显式 {"proxyUrl":""} 绑定注册机直连

PUT /api/accounts/{accountId}
{"proxyUrl":"http://user:password@host:8080"}
# 号池清除须 {"clearProxy":true}；proxyUrl 留空维持原值
```

### Agent Pass 支付链接重试

- 已保存的链接直接复用，不因重复点击创建新订单；本服务只提供托管收银台链接，不代付款。
- 报价预检失败时尚未提交创建请求，不再留下“已有支付链接”的错误占位，可修复会话/网络问题后重新获取。
- 旧记录或创建请求中断造成的“不确定”结果仍默认拦截。须先到官网核对账单，确认未付款且无待处理订单，再在单账号弹窗勾选“已核对账单”并点击获取链接。勾选仅本次有效，失败后需重新核对；不会绕过 Free 账号、报价或固定出口校验，也不会覆盖已保存链接。
- 批量操作不提供上述确认，不自动重试不确定订单。

### 批量操作

打开一个注册任务后，可勾选账号或“全选本任务”：

- **注册所选**：只处理已保存/失败/中断记录。确认后排队，保留原代理及会话；验证码是否计费由现有开关决定。“已排队”不代表已注册成功，不自动入池。
- **刷新所选套餐**：对已保存会话的空闲账号只读检测，未注册或执行中的记录跳过。
- **获取所选订阅链接**：显式弹窗确认后获取 Agent Pass 月度单次链接；已有链接复用，不确定订单停止。弹窗逐项显示成功/失败/跳过，可手动复制全部成功链接，不自动付款或打开多个窗口。

批量管理员请求最多3个并行，真实注册仍受后台并发预算限制。轮询保留当前勾选，切换任务清空；同一操作防重复点击。部分失败会逐项提示，不自动重试。失败后先核对官网/已保存结果，尤其不要反复创建支付链接。

注册是纯HTTP流程；“打开注册页”只是GET读取动态表单，不启动浏览器。实时日志已限制为单次轮询链并按seq去重，收起后刷新任务不自动重开；屏幕上重复显示不等于真的发起多次注册。

### 注册失败与登录恢复

官网认证动作可能以HTTP200返回业务错误，必须读取Flight中的结构化error，不能凭状态码认定注册成功。`auth-token-code-verifier`只是PKCE中间凭据，不是登录会话；“存在Cookie”也不能证明账号可用。真正的登录token仍须通过`/api/keys`鉴权，401/403不等于Key吊销，更不能直接断定注册风控。

对仅保存中间Cookie的失败记录，显式“重试”只尝试一次原账号/原密码登录，成功后保存新会话再继续原Key和邮件流程；失败时显示官网实际错误，保留原凭据，不自动重新注册、解验证码、换出口或清除记录。官网提示邮箱密码不匹配也不能用于断言账号不存在。原始注册响应未留存时，无法倒推当时具体拒绝原因；重新提交注册需另行确认，先单账号核验，不批量重放。

本次为本地修复契约，尚未推送或部署；真实排查只做正常登录与只读查询，没有重新注册、创建Key、解验证码、付款或推理。

### 正常限速与真实客户端标识

- TokenHarbor 原生推理保留实际下游客户端的 User-Agent；缺失或通用HTTP库标识（如Go-http-client）才使用本项目真实名称 `account-pool-balancer/tokenharbor-gateway`。不冒充浏览器/官方客户端，不伪装设备或轮换出口。官网APP列最终如何识别该名称仍以官网为准，不回改历史日志。
- 注册全局固定单并发，新注册提交至少间隔60秒；旧客户端传更高并发也不能提高实际并发。先等待再读取页面/可能购买验证码，提交前再次校验间隔，登录恢复不重复注册。正常排队最长24小时，停止服务可取消等待。
- 收到注册流程429/503、拒绝响应的Retry-After或官网HTTP200注册过多业务错误时，仅结束当前账号本次尝试，保留原凭据及官网错误。其他排队账号继续按单并发和60秒提交间隔处理，不触发全局冷却，也不因此拒绝其他账号的新注册/重试请求。
- 受限账号提示完整Retry-After秒数/HTTP日期；未给有效时间则提示至少等待5分钟后人工重试该账号。该提示不设全局倒计时，不自动重放失败任务；重新发起不代表官网已解除限制。提交间隔在本进程内维护，验证码仍仅按原有显式开关处理，不以自动解码、改IP或指纹来应对注册限制。
- 本项仅本地Mock/假账号验证，尚未部署；不触发真实注册、验证码、推理或付款，不修改线上配置。

### 同机 IPv6 出口代理排查

IPv4入口的SOCKS代理可以提供IPv6出口，Docker未启用IPv6本身不是失败依据。如果代理就在OVH宿主机上，宿主机curl/Go成功而服务容器TCP连接超时，应先检查Docker网桥到宿主机代理端口的INPUT/UFW规则。测试必须使用同一代理和服务网络命名空间；不得用直连成功冒充代理恢复。只针对内网及必要代理端口的防火墙放行也需要用户明确授权，不擅自修改公网规则或禁用防火墙。

新增管理员接口沿用现有登录/CSRF，响应均 `Cache-Control: no-store`：

```http
POST /api/tokenharbor/registrations
{"accounts":[{"email":"example@icloud.com","mailboxUrl":"接码页"}],"saveOnly":true,"proxyMode":"random"}

PUT /api/tokenharbor/proxies
{"proxies":["http://user:password@host:port"]}

GET /api/tokenharbor/proxies
# 只返回 count 和脱敏 labels；显式 PUT {"proxies":[]} 清空

POST /api/tokenharbor/registrations/{rowId}/subscription
```

代理池与注册凭据共用原有加密文件；旧数据保持可读。新增 `saved` 状态旧版程序不认识，回退前必须备份加密注册库，不要直接让旧程序打开包含该状态的新库。

## 注册会话与 Agent Pass 支付链接

注册机支持 `邮箱----接码页` 和 `邮箱----密码----接码页`；未提供密码会随机生成并加密保存。注册成功即保存会话，创建 API Key 后立即保存，再等待邮件；失败后的「继续」复用会话及已有 Key，不重复注册。恢复邮件查询沿用原注册时间。接码页明确使用独立直连 HTTPS，Token Harbor 站点请求继续使用账号固定代理，不做代理失败直连。新账号默认**不自动同意免费模型的数据使用条款**。

已保存会话的注册记录提供「Agent Pass 链接」。仅处理官网 [Pricing](https://tokenharbor.ai/pricing) 的 Agent Pass 月度单次新购，不是余额充值或其他 Pass。先读取报价，只有 Free 账号、monthly、oneTime 的 Agent Pass（已验证价格不超过 $1.99）才创建 Stripe 托管页面；不自动续费、不记住银行卡、不代付。已有订阅升级、地区确认、变化的报价均停止，需在官网人工处理。

管理员接口（现有会话认证及 CSRF）：

```http
POST /api/tokenharbor/registrations/{rowId}/checkout
Content-Type: application/json

{"kind":"pass","product":"starter_pass_monthly"}
```

`GET` 同一路径读取已保存的链接。两接口均 `Cache-Control: no-store`；URL 仅在管理员显式弹窗显示，不出现在任务轮询和实时日志，持久化沿用加密注册库。重复获取返回原链接，不另建订单；未完成或结果不确定的创建记录阻止重放，先核对官网账单。支付金额、优惠和有效期以 Stripe 页面为准；链接过期或结果不确定时不要反复点击创建。

2026-11-05 真实验收只使用了一个邮箱；其余四个未注册。该账号邮箱已验证，官网正常 Choose Agent 得到 **Agent Pass — Monthly (one-time)，US$0.99** 收银台，未提交付款、未进行推理。会话、Key、链接保存在本地及 OVH 受限目录；账号经现有管理员 API 增量入池，待用户付款所以暂不参与调度。程序的报价保护/自动创建分支用 mock 验证，未为验证重复创建真实订单。代码修改未部署；账号入池不需要重启服务。

## 2026-11-06 套餐/额度/容量增量验收

Node行为/语法、`go test ./...`、`go test -race ./...`、`go vet ./...`、构建及 `docker compose config --quiet` 通过。隔离真实浏览器验证Agent 25%已用得到20/3.75等效值、Office和Agent覆盖数不同、未知账号暂停、免费/Frontier/旧ID不暴露，以及错误详情按钮与脱敏。新同步模块另做真实只读验证：一个Agent账号读到0%已用和独立重置时间，官网映射29组、原生目录77项中精确覆盖6项；另一个固定代理20004连接拒绝，将未知停调度，未改其代理或防火墙。未重新注册、付费或向真实上游发送推理；本轮代码仅本地完成，未推送/部署。

## 早期接入验证范围

本轮覆盖本地 mock 的三原生协议、JSON/SSE 字节保留、头透传、粘性/忙碌、服务端续传/重启恢复、故障切换、消费确认、重定向/不确定请求禁止重放、目录过期/凭证失效、日志与独立设置。浏览器验证了独立标签、添加/去重导入、下游 key、设置保存、请求/错误日志和缓存统计。合并额外验证注册设置与号池设置互不覆盖，保留 Go 会话/缓存/出口观测能力，缓存写入统计兼容既有 `cw` 与 `kw` 投影但不重复计数。**未部署、未使用真实上游 key 推理，未实机验收 Codex / Claude Code**。
