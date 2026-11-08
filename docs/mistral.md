# Mistral 号池

Mistral 使用现有 OpenDesign 调度器，不引入第二套选号或协议转换。默认上游 `https://api.mistral.ai/v1`。

## 管理与接入

1. 管理台选择 **Mistral 号池 → 账号**，手动添加或批量导入 API Key。每行 `API Key[,名称[,代理URL]]`。不需要控制密钥；按用户要求，已登录管理员可在账号列表/编辑窗口直接查看、复制完整 Key 和代理地址。它们沿用 config.json 明文存储，不新增加密或迁移密码/Cookie；接口仍需管理员会话并禁止缓存，普通日志继续脱敏。
2. 在本池创建下游密钥，默认分组 `mistral`。下游使用网关地址和这把密钥，不使用上游 Key。分组与导入密钥均隔离，不能借其他池账号。
3. **路由设置**可修改本池 API Base（必须含 `/v1`）、选号、粘性/TTL/额外会话头、满载等待、单请求账号上限、健康阈值、流式/非流式超时、请求体上限、模型缓存及独立错误/重试规则（含精确错误码匹配）。额度轮询的开关/间隔/并发/快照过期、目录刷新间隔、详情日志也可配置。首次保存前继承原共享路由/轮询/详情日志；Mistral 保存后路由与轮询独立，不改变 OpenDesign。目录周期默认600秒，0关闭周期更新但首次/手动读取仍可用；轮询关闭不影响手动刷新。日志保留/容量仍为全局共享，UI明确提示。

`mstrl_` 开头的 Key 自动识别为 Mistral；无前缀或其他形态的官方 Key 请显式填写 `provider: "mistral"`。本池导入界面会自动携带该字段。重复导入同一 Key 不重复建号。

开放导入使用 **Mistral 池专属导入密钥** 调用现有 `POST /open/v1/accounts/import`：

```json
{"provider":"mistral","runtimeKey":"MISTRAL_API_KEY_PLACEHOLDER","name":"Mistral-1"}
```

## 会话粘性与调度

- 与 OpenDesign 相同的策略、优先级、权重、最大并发、RPM、健康过滤及故障切换；`smart` 等额度策略在余额未知时使用现有未知额度排序，不伪造额度。
- 识别 Codex 当前官方的 `Session-Id` / `Thread-Id` 及旧 `Session_id` / `Thread_id`、`X-Codex-Session-Id` / `X-Codex-Thread-Id` 等兼容头；Claude 的 `X-Claude-Code-Session-Id` / `X-Claude-Session-Id` / Anthropic 会话头，以及 `metadata.user_id` 或跨协议搬到根 `user` 的 `_session_UUID` / JSON-string `session_id`。普通user标签不是会话。pi 可传的 `X-Session-Id` / `X-Session-Affinity` / `X-Pi-Session-Id` / `X-Pi-Session`，以及现有父会话、OpenCode 等会话头、正文会话字段和 `prompt_cache_key`。任意额外稳定头可在「额外会话头」配置。
- 优先级沿用 OpenDesign：`previous_response_id`（本池不支持，Responses 请求会本地拒绝）、自定义头、父会话、正文身份、Claude 会话结构、内置会话头、最后的 `X-Prompt-Cache-Key` / `Prompt-Cache-Key` 头兼容、可选首条消息 HMAC 弱粘性。不会把每次变化的请求 ID 当成会话。客户端必须实际发送稳定标识；仅凭客户端名称不能保证粘性。
- 旧下划线头必须先由 Nginx 放行；应用无法恢复已被入口删除的字段。双跳采样若已无任何身份，仍会合理回退message_hmac，不凭空造会话。概览显示当前池绑定数及**全局共享4096条LRU硬上限**，不是每池/每账号4096。淘汰持久化严格续传绑定会同步保存删除，缺失所有者仍拒绝续传，不静默换号。
- 会话 HMAC 按分组和下游密钥隔离。TTL 与忙时 `overflow` / `wait-overflow` / `reject` 策略沿用现有配置，临时借号不改原绑定。
- Chat / Responses 生成请求缺少顶层 `prompt_cache_key` 且已识别会话时，自动填入 `apb-mistral-<现有会话HMAC>`；HMAC按分组与下游密钥隔离，不暴露原始会话或凭据。客户端已有字段（包括显式null/空串）原样保留；没有身份不随机生成。共享前缀与同一账号有利于缓存命中，但不保证命中；缓存块64 tokens，官方缓存输入按普通输入价10%计费。
- Chat 历史中的 assistant 顶层 `reasoning_content` 进行兼容：非空字符串转为 `content` 内原生 `thinking` 块（`thinking:[{type:"text",text:...}]`），原回答、已有内容块、工具调用/ID保留，再移除不支持的顶层字段；null/空串仅移除空字段。下表三种推理模型的 assistant 字符串 `reasoning` 也作为同义字段处理，同值合并、冲突/重复/不可表示的类型本地400，不丢弃思考或换号重试。Responses历史和其他池不做此转换。NewAPI的“思考转内容”是响应侧`<think>`文本转换，不等价。
- 对下表三种推理模型的 Chat，根 `reasoning:{"effort":...}` 兼容为 `reasoning_effort`，只接受含一个 effort 字段的对象；多余字段、重复字段、非法结构本地400，不丢弃 summary 等请求意图。`thinking:{"type":"enabled|disabled"}` / 布尔 `enable_thinking` 仅校验结构后移除，不压低有效档位或产生不支持的 none；未知预算/额外字段仍本地拒绝。
- 按用户2026-11-09最新要求，三种模型的 Chat / Responses 均支持 `low` / `high` / `max`；**未传默认 high，显式 max 保留**，替代旧版DeepSeek max→high策略。null、空值、其他档位（none/medium/xhigh/minimal）或非法档位值类型沿用归一high策略。Chat使用 `reasoning_effort`，Responses使用原生 `reasoning.effort`；兼容两种写法，归一后同值合并、冲突拒绝。Responses的原生reasoning其他字段及完整input历史不丢弃，支持性仍由上游验证；不套用Chat专属思考开关清理。高档位可能增加思考用量和耗时，不自动增大输出预算。其他模型、FIM、input_tokens、其他池不套用该规则。
- 同一精确 DeepSeek Chat 的 user 图片块中，先清除 NewAPI 泄漏的空内部 `MimeType:""/null`（已实测被上游以 `extra_forbidden` 拒绝），再将仅含 `url` 的 `image_url:{"url":"..."}` 对象转成原生字符串 `image_url:"..."`，支持 data URL，保留图片和原有文本/顺序；带 detail 等额外选项的对象保留这些选项，不丢弃语义，非空 MimeType 不擅自删除。其他模型/协议/号池不套用此转换。
- 除上述明确的缓存补齐、思考格式及精确模型参数兼容外，原生字段/工具/模型ID/实际客户端UA保持不变；没有转换的正文保留原字节。精确 DeepSeek Chat 仅省略不支持的根 `store:false/null`，`store:true` 本地拒绝；其他模型/协议不套用该规则。未知字段不静默丢弃，不支持时明确报错。
- 401 隔离失效 Key；402 冷却；429 尊重完整 `Retry-After`；其他临时 5xx 短冷却。明确的503+顶层code3800视为模型服务不可用，不扣账号健康、不重复换号，并保留上游Retry-After；旧的完整出厂规则自动升级，任何自定义规则/顺序/备注/空列表保留。确定性请求/权限错误不遍历账号。固定代理失败不回退直连；已发送且结果未知或流已输出的请求不重放；凭证请求不跟随重定向。

## 原生接口与额度

支持 `GET /v1/models` 以及原生 `POST /v1/chat/completions`、`/v1/responses`、`/v1/responses/input_tokens`、`/v1/fim/completions`、`/v1/embeddings`。不增加 Anthropic 协议转换，也不因识别 Claude 会话头而宣称 `/v1/messages` 可用。

Mistral Responses 为**无状态预览接口**：每次在 `input` 携带完整历史，`previous_response_id` 必须省略或 null，`store` 必须省略、false 或 null。不建立服务器端 Responses 续传绑定。

Responses SSE识别`event:`和根`data.type`中的终态，首帧即`response.completed` / `response.incomplete`也立即结束，不等待上游关闭连接。合法空output、纯工具调用和incomplete协议终态不因没有文本误判为断流（incomplete仍不表示模型已完整生成）。已输出后遇到`response.failed` / error立即按失败结束；EOF/读错误而没有终态时记录失败，并追加一个明确error事件，不伪造completed/[DONE]/usage，也不重放请求或归咎账号Key。尚未输出任何有效事件的空流返回502 `mistral_response_incomplete`；已提交的HTTP200不会假装改成502。其他池和Mistral Chat保持原有结束策略。

### 模型与思考兼容范围

| 模型 | 上游原生ID | 本项目档位策略 |
|---|---|---|
| DeepSeek V4.1 Flash 1017 | `deepseek-v4-1-flash-1017` | low / high / max，缺省high |
| GLM 5.3 | `zai-glm-5-3` | low / high / max，缺省high |
| GLM 5.3 Flash | `zai-glm-5-3-flash` | low / high / max，缺省high |
| Mistral Large 4 | `mistral-large-4-0` | 原生透传，不套用上述默认/归一策略；官方档位low / medium / high |

NewAPI里的`deepseek-v4.1-flash-1017`、`glm-5.3`等客户端别名应由渠道映射到实际上游ID；本轮不新增模型别名配置，不将Modal的`glm-5-3`短名冒充Mistral原生ID。DeepSeek专属的developer/store/图片/seed等适配不因公共思考控制而扩散到GLM/Large。

模型目录可见、协议Mock通过和真实客户端可用是不同验收层级；不能仅凭目录宣称四模型全部获得推理授权。

“刷新额度”与自动轮询同时接入模型目录、本 Key 对应的套餐预算及独立组织订阅；预算沿现有 Snapshot/metadata 保存，页面显示币种、约剩余/总额、已用百分比、重置/检测时间和 PAYG 状态。失败保留上次值但标为历史/未知，403不把仍可推理的Key判失效；目录失败与预算失败分别记录。更换 Key / Base / 代理后失效旧快照，晚返回不能复活旧数据。同工作区多个 Key 可能共享限额，不能累加成独立账号额度。真实且未过期的预算参与 smart/least-used/pace 的余量排序；未知不伪造零，不把 Mistral 预算放进 OpenDesign 付费分类，不代改官网 PAYG 或凭预算假造钱包余额。Mistral `base_model_id` 是模型来源，不当作别名，避免将微调模型改发成基础模型。

组织订阅读取额外以3秒为上限，不跟随重定向。UI明确区分Key访问等级与组织套餐（真实样本API访问等级Free、组织订阅Student）；订阅失败不会使成功预算失效，保留上次订阅并标错误。组织API/Vibe PAYG可空，未知不伪造关闭，也不代用户开启。预算reset_at是额度重置，不冒充账单续费日期。

### API Key 读取套餐额度（2026-11-08 实测）

官方 [Vibe billing 实现](https://github.com/mistralai/mistral-vibe/blob/main/vibe/app_server/_billing.py) 和 [whoami 实现](https://github.com/mistralai/mistral-vibe/blob/main/vibe/setup/auth/whoami.py) 提供了普通 Key 可用的读取路径，均使用 `Authorization: Bearer <API Key>`：

- `GET https://console.mistral.ai/api/vibe/whoami`：按 `plan_type` 区分 Studio/API (`API`) 与 Vibe (`CHAT`)，不要仅由账号名称猜测。
- `GET https://api.mistral.ai/v1/billing/subscription`：读取 `plan_name`、`payg_enabled`、`vibe_payg_enabled` 等套餐信息，不是剩余额度本身。
- `GET https://api.mistral.ai/v1/billing/budget`：返回 `api_budget`、`vibe_budget`，窗口字段含 `initial_budget`、`usage_percentage`、`currency`、`reset_at`、`payg_enabled`。Studio 使用 `api_budget`，Vibe 使用 `vibe_budget`；剩余约为 `initial_budget × max(0, 1 − usage_percentage / 100)`，金额由百分比推算，并非额外测得的钱包余额。

本次取 `zxg-01`、`zxg-10` 两组四把真实 Key，经各组固定代理读取 whoami/budget 均为 HTTP200；两类 Key 都能看到所属组织的两项预算，但必须按类型分别归属，不能在每把 Key 上重复累计。同组织相同预算只计一次，币种以上游字段为准，不假定 EUR；样本为 USD，重置时间由 `reset_at` 给出。初始接口采样未覆盖全部账号；后续接入现有自动/手动刷新。2026-11-08 `77e9709`已上线，生产40把Key预算新鲜、组织订阅全部成功持久化（Student6/Pro34，可能共享组织预算），CHAT/API两类各一次真实手动刷新通过。新增显示由Node与生产API投影核验，未做本轮真实生产管理员浏览器保存往返或主动推理；完整部署/验收边界见`docs/ovh-deployment.md`。

官方 CLI 注明 budget 可能需要 `see_org_inference_usage` 权限；403/缺字段/失败应显示未知，不能当作零。企业 `GET /v1/admin/usage` 是另一套 Admin Key 权限（本次普通 Key 为401）；第三方声称的 `/v1/billing/usage` 本次为404，不能据此否定已成功的 budget 接口。探针必须匹配 Go SOCKS 远端解析语义：curl 对 `socks5://` 默认本地解析，本轮曾出现 TLS EOF，改为同一代理的远端 DNS 探测后200；未改线上代理配置、直连回退或绕过权限。

管理台「真实请求」测试 Mistral 时，未填写模型默认使用 `deepseek-v4-1-flash-1017`，仍为 `max_tokens=1`；显式填写的模型保留。「免费检查」只读目录，不发推理请求。

参考：[官方文档](https://docs.mistral.ai/)、[Responses](https://docs.mistral.ai/inference/conversations/responses)、[用量和限制](https://docs.mistral.ai/admin/billing-usage/usage-limits)。

## TPS与局部性能

请求日志有两种TPS：

- **全程平均**：真实输出token ÷ `useTimeSec`，其中`useTimeSec = 结束Unix整秒 − 本网关请求开始Unix整秒`，与NewAPI日志相同公式，包含本网关记录区间内的排队、首响应等待及先前重试；显示时与NewAPI一样四舍五入为整数。两层计时起点不同，不能保证数值相等，短请求也有整秒边界误差。
- **首事件后 / 上游均速**：保留原上游TPS，流式为首个有效事件到结束的观测平均，非流式为最后一次上游整次调用平均；排除本地选号/排队和先前重试。这不是服务端内部解码速度，可能受网络和背压影响。

无真实usage、失败、无有效时间窗口或旧日志显示未知，不用历史耗时倒算测量值。全程平均与NewAPI一致，零输出/零整秒窗口也不显示TPS；不会额外累加上游已计入输出的思考token。

### 原生交换详情

2026-11-08 17:41 UTC，`0ed42ff`已上线OVH，按用户要求启用Mistral独立详情。自然流量已验证47条全程平均TPS公式和15条新三段详情，其他池及有效参数保持；旧记录不补造。详见`docs/ovh-deployment.md`。

Mistral详情开关开启后，现有详情记录额外保存每次`upstreamAttempts`，弹窗分开显示：客户端原始请求头/体、实际发给上游的请求头/体（包括参数与缓存适配）、上游原生响应头/体（响应适配前），并保留返回客户端的适配后响应。换号按尝试编号及账号关联，网络失败不伪造上游响应，旧日志没有的数据不补造。

请求头通过HTTP trace补齐Transport自动字段；响应体有界tee捕获，不为日志延迟首帧、读完整个流或重放请求。每段正文最多64KiB首尾预览，明确标记截断/未完整读取；响应字节数是实际已读字节，不冒充未读取的完整长度。凭据、Cookie、API/自定义token头、会话及X-Affinity一律脱敏；无法安全脱敏的破损片段明确省略。因此是原生协议的脱敏诊断预览，不是逐字节TLS抓包。其他池详情开关/行为不变。

同一Apple M4短基准：原生字符串SSE约2613→135ns/帧、46→0次分配；128条约1KB文本块历史准备约801→274μs、379→104KB分配。原生帧和纯文本历史仅跳过无用解码，内容数组/转义键/多行SSE保守回退，不删思考、图片、工具或校验。这是局部基准，不代表模型生成或端到端耗时相同比例提速；无生产压测。

## 验证

### 本轮兼容修复（2026-11-09，已部署5d15347）

新增Mock矩阵覆盖上述四模型的Chat/Responses、流式/非流式、多轮思考与工具结果、Deny/apply_patch原始多行补丁/引号/反斜杠/大数、缓存字段及真实usage保留；三种推理模型额外覆盖low/high/max/缺省high、别名冲突和跨池/路径隔离。工具被上游拒绝时保留错误，不删工具重放。Responses回归先在旧代码复现首帧终态等待及EOF误记成功，再验证修复；完整执行结果见`.trellis/tasks/archive/2026-11/11-09-mistral-stream-reasoning/validation.md`。

2026-11-08 18:44 UTC，5d15347已推送并部署OVH，版本/健康/数据与配置保留、NewAPI双内网入口及公网资源验收通过，见`docs/ovh-deployment.md`。本轮未发真实推理、未修改生产配置。**Codex / Cherry Studio / pi等真实客户端端到端验收尚未进行**；会话头识别和Mock协议形状不等于客户端已验收。历史真实测试仅证明以下记录中明确的接口/模型；Claude原生Messages仍不支持，不能因为识别Claude会话头就宣称Claude Code直连可用。

### 历史验证

2026-11-08补齐额度与路由：全仓Go normal/race/vet与Node行为/语法、必要build通过；新增Mistral定向测试覆盖API/CHAT预算选择、缺字段/过期/403、失败保留、metadata往返、来源变化晚返回、独立轮询开关/并发/目录周期、完整设置/规则保存重载/跨池隔离、详情开关和余量排序。真实本地浏览器使用两把假Key完成批量刷新（Studio USD22.50/30、Vibe EUR270/300）、403显示历史未知、路由/错误码/重试规则保存及进程重启后读回；这是Mock验证，不是生产推理或上线验收。

本轮缓存/思考兼容/凭据展示与TH日志开关只运行受影响Go定向用例、Mistral/TH Node行为与JS语法检查，并用一次本地构建、禁用假账号完成页面核对；未做真实推理或实际缓存命中验证，不重复全仓/race/vet。以下为原始号池接入时的历史验证记录：

- `go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./cmd/odb`。
- `node internal/server/mistral_web_test.js`；真实本地浏览器使用假账号与本地 Mock，检查管理、导入、独立设置和后端状态。
- 本次用户提供的测试 Key：真实 `/v1/models` 返回 200 / 64 个模型；以 `ministral-3b-latest` 对 Chat 与 Responses 分别进行一次最多 8 输出 token 的小请求，均为 200，返回输出与 usage。真实凭证只在内存使用，不写入测试文件、日志或 Git；这不是 Codex / Claude / pi 客户端端到端验证或生产压力测试。
