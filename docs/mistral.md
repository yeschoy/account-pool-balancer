# Mistral 号池

Mistral 使用现有 OpenDesign 调度器，不引入第二套选号或协议转换。默认上游 `https://api.mistral.ai/v1`。

容量规划见 [3亿TPM完整链路审计](mistral-capacity.md)（2026-11-09）：包含调度/内存/会话容量/连接/日志/入口瓶颈及本地基准。该报告是研究与隔离实验，不表示性能改造、可配置绑定容量或生产3亿TPM已验收。

缓存相关研究见 [Magpie / Mistral 实现深度对比](mistral-magpie-cache-comparison.md)（2026-11-09）。后续已落实普通绑定持久化、真实会话优先与OpenCode缓存别名兼容，定向Mock/构建通过，`2629255`于2026-11-08 19:44 UTC完成部署验收；自然流量已有6条普通绑定落盘，该次部署本身未做生产缓存命中率受控对照。后续自测-test的6+4次公开完整样本对照已记入Trellis，尚未确认具体低命中根因或缓存率修复。

## 管理与接入

1. 管理台选择 **Mistral 号池 → 账号**，手动添加或批量导入 API Key。每行 `API Key[,名称[,代理URL]]`。不需要控制密钥；按用户要求，已登录管理员可在账号列表/编辑窗口直接查看、复制完整 Key 和代理地址。它们沿用 config.json 明文存储，不新增加密或迁移密码/Cookie；接口仍需管理员会话并禁止缓存，普通日志继续脱敏。
2. 在本池创建下游密钥，默认分组 `mistral`。下游使用网关地址和这把密钥，不使用上游 Key。分组与导入密钥均隔离，不能借其他池账号。
3. **路由设置**可修改本池 API Base（必须含 `/v1`）、选号、粘性/TTL/额外会话头、满载等待、单请求账号上限、健康阈值、流式/非流式超时、请求体上限、模型缓存及独立错误/重试规则（含精确错误码匹配）。额度轮询的开关/间隔/并发/快照过期、目录刷新间隔、详情日志也可配置。首次保存前继承原共享路由/轮询/详情日志；Mistral 保存后路由与轮询独立，不改变 OpenDesign。目录周期默认600秒，0关闭周期更新但首次/手动读取仍可用；轮询关闭不影响手动刷新。日志保留/容量仍为全局共享，UI明确提示。

`mstrl_` 开头的 Key 自动识别为 Mistral；无前缀或其他形态的官方 Key 请显式填写 `provider: "mistral"`。本池导入界面会自动携带该字段。重复导入同一 Key 不重复建号。

开放导入使用 **Mistral 池专属导入密钥** 调用现有 `POST /open/v1/accounts/import`：

```json
{"provider":"mistral","runtimeKey":"MISTRAL_API_KEY_PLACEHOLDER","name":"Mistral-1"}
```

### 概览总额度（按工作区去重）

Mistral 概览显示「Mistral 总额度剩余」卡片与「Mistral 套餐总额度（按工作区去重）」面板：每个组织一行，按币种分别合计，不同币种从不相加。组织标识取自 `whoami` 的 `customer_id`（同组织的 API 与 Vibe Key 相同，不同组织不同），由额度刷新写入快照的 `mistral.org`，账号视图镜像为 `workspaceId` 并按既定规则暴露 `sharedWith`。

- 只汇总**读取成功且未过期**（按本池 `quota.staleSec`）且未过重置时间的预算；读取失败、过期、已重置或缺少币种的 Key 计入「未知」，不按 0 加入合计，也不按 Key 重复累加。未知 Key 在刷新额度前不会出现在总计里，页面明确写出该规则。
- 同一工作区多个 Key 若报告不同金额（读取时点差异），该工作区取最大值一次，不叠加；Key 类型不一致会在该行标记。
- 金额仍是由 `usage_percentage` 推算的约剩余，不是钱包余额，不代改 PAYG。首次刷新前所有账号为未知，概览显示「未知」而不是 0。

### 额度请求频率（同工作区复用）

Mistral 预算按工作区计费，但每次预算读取要 3 个上游请求（`whoami`、`billing/budget`、`billing/subscription`）。此前按账号各自读取，多 Key 同组织等于把同一份数据重复拉取，133 个账号在 300 秒间隔下约 11.5 万请求/天。

现按工作区合并：一个 Key 读成功后，**同代理且同 Base** 的其他 Key 复用该结果；「路由设置 → 同工作区额度复用窗口」可配置，默认 600 秒（0 用默认，上限 86400）。规则：

- 只复用**别的 Key 读到的、且比本账号自己上次尝试更新**的结果，账号自己的轮询间隔不会被拉长；手动刷新总是真的重新读取，并刷新该工作区的共享结果。
- 失败不共享：错误归属出错的那把 Key，也不标记工作区已读。
- 同工作区串行：同一工作区的读取不会并发进行；尚不知道自己工作区的 Key 在代理+Base 维度排队，等前一个落地后先尝试复用。
- 代理或 Base 变化后不继承旧工作区的结果；工作区记录随属主账号删除。

节省幅度取决于每个工作区的 Key 数（本次导入的 22 组各 2 把，约减半），跨工作区不会合并。目录刷新（`modelSyncSec`）仍按账号独立，不受此窗口影响。

**本地实测**（20 个 Key 同一工作区、同一代理，32 核本机，Mock 上游）：第一轮预算请求 **60 → 3**，第二轮 **60 → 0**；20 个账号只有一个真正读取。窗口内该工作区的其余 Key 全部复用，热路径下同一工作区的预算请求数从「Key 数 × 3」降为「每窗口 3 次」。跨工作区不合并，133 个账号分别在 119 个工作区时，请求量约为原来的 119/133。间隔仍是每账号独立的 300 秒，Window 只作用于同一工作区内部的去重。

## 会话粘性与调度

- 与 OpenDesign 相同的策略、优先级、权重、最大并发、RPM、健康过滤及故障切换；`smart` 等额度策略在余额未知时使用现有未知额度排序，不伪造额度。
- 识别 Codex 当前官方的 `Session-Id` / `Thread-Id` 及旧 `Session_id` / `Thread_id`、`X-Codex-Session-Id` / `X-Codex-Thread-Id` 等兼容头；Claude 的 `X-Claude-Code-Session-Id` / `X-Claude-Session-Id` / Anthropic 会话头，以及 `metadata.user_id` 或跨协议搬到根 `user` 的 `_session_UUID` / JSON-string `session_id`。普通user标签不是会话。pi 可传的 `X-Session-Id` / `X-Session-Affinity` / `X-Pi-Session-Id` / `X-Pi-Session`，以及现有父会话、OpenCode 等会话头、正文会话字段和 `prompt_cache_key`。任意额外稳定头可在「额外会话头」配置。
- Mistral优先使用真正会话：自定义头、父会话、正文会话字段、Claude会话结构、内置会话头；无真实会话时才回退正文 `prompt_cache_key`、`X-Prompt-Cache-Key` / `Prompt-Cache-Key` 头、首条消息HMAC弱粘性。其他池优先级不变；`previous_response_id`在本池Responses仍不支持。已有缓存键原值不被改写，不能把缓存提示当成必然唯一的会话。不会把每次变化的请求ID或UA当会话，客户端必须实际发送稳定标识。
- 旧下划线头必须先由 Nginx 放行；应用无法恢复已被入口删除的字段。双跳采样若已无任何身份，仍会合理回退message_hmac，不凭空造会话。概览显示当前池绑定数及**全局共享4096条LRU硬上限**，不是每池/每账号4096。淘汰持久化严格续传绑定会同步保存删除，缺失所有者仍拒绝续传，不静默换号。
- 新版请求日志的「客户端头」查看实际到达 Balancer 的请求头、会话候选头是否有效及最终身份来源，不必开启正文详情。按用户最新要求，会话/缓存标识原值展示，认证密钥与Cookie仍隐藏；旧日志已脱敏的值无法恢复。仅新请求会记录，旧日志不能据此判断缺头；超限会标记截断。NewAPI 透传开启不保证客户端确实发送，也不能恢复前级 Nginx 已过滤的下划线头。`metadata.user_id`/`user` 的 JSON 字符串别名属于正文，不是 HTTP 头；正文身份先于普通内置会话头时，来源显示 body 是正常优先级，并非没识别头。此功能须部署包含该改动的版本才会在线生效。
- 会话 HMAC 按分组和下游密钥隔离。TTL 与忙时 `overflow` / `wait-overflow` / `reject` 策略沿用现有配置，临时借号不改原绑定。
- 普通Mistral已发送确认绑定保存在现有 `metadata.pool.mistralBindings`，正常退出强制保存，启动验证后恢复原账号、绝对到期时间及显式/弱身份类型；不持久化提示词/原始会话/凭据或在途/RPM状态。与严格绑定共用4096 LRU，严格绑定先恢复。来源同时校验账号、分组、上游Key/Base/代理及下游Key ID/hash/启用状态；变更、过期、删除、淘汰后不复活。普通新增/续期按既有一分钟节奏合并写盘，崩溃可能丢最近未保存记录，不承诺零丢失。新增可选metadata字段可能被旧版严格JSON读取器拒绝，降版前须备份并仅移除本次派生字段，不能恢复旧data覆盖后续写入。
- Mistral Chat在身份解析前兼容OpenCode的 `promptCacheKey`→`prompt_cache_key`；字符串/null/空串保留，同值双拼写合并且保留蛇形原字节，冲突/非法类型/重复键本地400、零账号尝试。其他池及Responses/FIM等路径不转换；两种拼写在日志中都脱敏。
- **已于2026-11-08 20:26 UTC随be14987部署验收**：Chat / Responses 生成请求缺少顶层 `prompt_cache_key` 时，按 `X-Prompt-Cache-Key` → `Prompt-Cache-Key` → 管理员额外会话头 → 父会话头 → 内置会话头的顺序，取第一个有效值直接填入，不做HMAC、不加前缀。空白、超过512字节或含控制字符的头跳过。客户端已有字段（包括显式null/空串）原样保留；没有有效头时才沿用 `apb-mistral-<现有身份HMAC>` 兜底，没有身份不随机生成。账号粘性和持久化绑定仍按分组与下游密钥做HMAC隔离，但直接发出的原值不做这种隔离；正文、上游详情及错误日志继续脱敏，来源字段只记录缓存头、会话头或HMAC兜底；管理员「客户端头」按最新授权展示收到的会话/缓存头原值。
- `X-Affinity` 是独立HTTP头，不是额外会话机制：下游已传非空值时保留，否则复制最终非空 `prompt_cache_key`。它不反向覆盖正文缓存键，也不默认作为会话头。共享前缀与同一账号有利于缓存命中，但两个提示都不保证命中或锁定同一推理节点；缓存块64 tokens，官方缓存输入按普通输入价10%计费。
- Chat 历史中的 assistant 顶层 `reasoning_content` 进行兼容：非空字符串转为 `content` 内原生 `thinking` 块（`thinking:[{type:"text",text:...}]`），原回答、已有内容块、工具调用/ID保留，再移除不支持的顶层字段；null/空串仅移除空字段。下表三种推理模型的 assistant 字符串 `reasoning` 也作为同义字段处理，同值合并、冲突/重复/不可表示的类型本地400，不丢弃思考或换号重试。Responses历史和其他池不做此转换。NewAPI的“思考转内容”是响应侧`<think>`文本转换，不等价。
- **GLM developer 角色兼容（2026-11-12 11:24 UTC，已随d94da97部署OVH）**：现有DeepSeek的 `developer`→`system` 角色兼容扩展至精确 `zai-glm-5-3` / `zai-glm-5-3-flash` 的Chat，处理完整历史内的首条/后续消息及转义写法；仅换role值，不合并/移动/删除消息，不改指令内容、工具、数字或缓存键。真实Flash两次422已在既有NewAPI容器日志确认 `messages[0]` / `union_tag_invalid` / tag=developer；官方Chat只有system/user/assistant/tool。GLM5.3仅本地HTTP mock验证，不冒称真实推理验收。其他模型/协议/池不变，未知角色仍交上游明确拒绝，重复role本地拒绝。GLM不因此继承DeepSeek专属图片/文本块/seed/store等清理；部署已通过健康/数据保留/双内网入口/公网资源验收，未主动推理或重放原请求，尚未以部署后新的GLM自然恢复样本完成业务复测。详`docs/ovh-deployment.md`。
- 对下表三种推理模型的 Chat，根 `reasoning:{"effort":...}` 兼容为 `reasoning_effort`，只接受含一个 effort 字段的对象；多余字段、重复字段、非法结构本地400，不丢弃 summary 等请求意图。`thinking:{"type":"enabled|disabled"}` / 布尔 `enable_thinking` 仅校验结构后移除，不压低有效档位或产生不支持的 none；未知预算/额外字段仍本地拒绝。
- 按用户2026-11-09最新要求，三种模型的 Chat / Responses 均支持 `low` / `high` / `max`；**未传默认 high，显式 max 保留**，替代旧版DeepSeek max→high策略。null、空值、其他档位（none/medium/xhigh/minimal）或非法档位值类型沿用归一high策略。Chat使用 `reasoning_effort`，Responses使用原生 `reasoning.effort`；兼容两种写法，归一后同值合并、冲突拒绝。Responses的原生reasoning其他字段及完整input历史不丢弃，支持性仍由上游验证；不套用Chat专属思考开关清理。高档位可能增加思考用量和耗时，不自动增大输出预算。其他模型、FIM、input_tokens、其他池不套用该规则。
- 同一精确 DeepSeek Chat 的 user 图片块中，先清除 NewAPI 泄漏的空内部 `MimeType:""/null`（已实测被上游以 `extra_forbidden` 拒绝），再将仅含 `url` 的 `image_url:{"url":"..."}` 对象转成原生字符串 `image_url:"..."`，支持 data URL，保留图片和原有文本/顺序；带 detail 等额外选项的对象保留这些选项，不丢弃语义，非空 MimeType 不擅自删除。其他模型/协议/号池不套用此转换。
- **2026-11-11 13:05 UTC已随00ba86a部署OVH**：精确 DeepSeek Chat 的 assistant 文本块仅清除无内容的 `annotations:[]/null`（数组内空白和转义字段名同样处理）。真实422明确拒绝空注解；真实非空引用/注解、未知类型仍保留交上游校验，不靠丢弃语义换成功。重复字段本地400；完整思考/正文/工具/图片/缓存键保持，其他角色、模型及协议不套用。安全错误坐标可显示固定字段名 `annotations`，不记录引用内容。
- 除上述明确的缓存补齐、思考格式及精确模型参数兼容外，原生字段/工具/模型ID/实际客户端UA保持不变；没有转换的正文保留原字节。精确 DeepSeek Flash 1017、GLM 5.3 和 GLM 5.3 Flash 的 Chat 仅省略不支持的根 `store:false/null`，`store:true`、错误类型和重复字段本地拒绝；不修改缓存键或 `metadata.store`，其他模型/协议不套用该规则。未知字段不静默丢弃，不支持时明确报错。
- 401 隔离失效 Key；402 冷却——其中精确 `402` + 顶层 code `2303`（`billing_api_budget_exhausted`）命中默认规则 `mistral-api-budget-exhausted`，**冷却到该工作区 `api_budget.reset_at` 后自动恢复**，不 hard-quarantine、不扣全局健康、不累加翻倍；上游这份错误不带时间，快照里没有可用重置时间时按最长 24 小时兜底，账号状态行会注明「未读取到重置时间，按最长兜底冷却（估计值）」。其他 402 仍按原规则 10 分钟起逐次翻倍、最长 6 小时。429 尊重完整 `Retry-After`；其他临时 5xx 短冷却。明确的503+顶层code3800视为模型服务不可用，不扣账号健康、不重复换号，并保留上游Retry-After；旧的完整出厂规则自动升级，任何自定义规则/顺序/备注/空列表保留。确定性请求/权限错误不遍历账号。固定代理失败不回退直连；已发送且结果未知或流已输出的请求不重放；凭证请求不跟随重定向。

## DeepSeek 用户历史的 1h 缓存提示 422（2026-11-24，已部署）

北京时间 19:01–19:09，NewAPI 渠道 180 的 16 次上游 422 均确认是 `deepseek-v4-1-flash-1017` Chat 用户文本块的 `cache_control:{"type":"ephemeral","ttl":"1h"}` 被 Mistral 以 `extra_forbidden` 拒绝。既有适配只处理不带 TTL 的 `ephemeral` 提示；换号不能解决请求格式错误。

补丁仅从该模型 Chat 的 `user` 文本块移除这两项组成的 1h 提示，包括历史消息；不删除文本、图片或工具，不修改 `prompt_cache_key` / `X-Affinity`，不提供 1 小时缓存承诺。其他 TTL、额外选项、其他角色的 1h 提示、非文本块、其他模型/协议/号池不做此转换；重复 TTL 本地 400，不发往上游。`f66b67c` 已于 11:30 UTC 部署 OVH，未变更生产配置、未重放线上请求。详见 [部署验收](ovh-deployment.md)。

## DeepSeek 工具历史中的 Gemini 签名 422（2026-11-25，北京时间，已部署）

OVH 北京时间 19:55–23:55 的既有日志记录了 12 次 DeepSeek Chat 上游 422；只读核对的 40 个受影响工具调用均在同一个调用项上携带了相同非空字符串的 `signature` 与 `extra_content.google.thoughtSignature`，两个字段都被 Mistral 判为 `extra_forbidden`。这是 Gemini 专用历史附加信息，不是工具参数或调用结果，也不是此前的 1h 缓存提示。

仅精确 `deepseek-v4-1-flash-1017` Chat 的 assistant 工具调用会省略这对已确认的重复元数据：`extra_content` 必须只含 `google.thoughtSignature`，两值必须是相同的非空字符串。保留工具调用 ID、函数及原始参数、工具结果、原生思考、其他历史和缓存键；不清理未知、单边、不同值或还含其他字段的元数据。遇到适配范围内的歧义重复键本地 400，不换号。GLM、Responses、其他模型和其他池不变。JSON/SSE 本地 HTTP 回归确认旧版 422、新版单次 200。补丁 `44d5dcd` 随 `c0d205c` 部署 OVH；未改 Base URL、未重放线上历史或主动发推理。验收范围见 [部署记录](ovh-deployment.md)。

## 图片会话总量限制（2026-11-23，线上实测）

精确`deepseek-v4-1-flash-1017`的Chat当前上游按**本次请求完整历史内的图片块总数**限制20个。求药使（用户949、渠道180）的4次失败均为上游400 `Total number of images exceeds the maximum allowed of 20`；同渠道另有62次成功，但未采集正文，不能把这些成功均称为图片成功。真实合成测试单图可返回`Red`，20个图片块被接受，21个被拒；历史11个＋当前10个同样被拒。用户原失败没有正文详情，具体数量/客户端是否重复携带仍未知。建议新建会话或减少发出的历史图片，换号/重试不能解除上游限制；号池不自动删图、裁剪历史或换模型。通用Vision FAQ写8张，不覆盖该精确模型实测20张；不能推广到全部模型/协议。详见[完整排查与边界证据](mistral-image-incident-20261123.md)。本轮未改推理代码/生产设置、未部署。

## 温度参数错误排查（2026-11-18，本地修复，未部署）

只读核对生产最近4个请求日志分段中的41次 Mistral DeepSeek 请求：40次成功、1次422。失败发生在 **2026-11-18 17:01:13 UTC**，上游明确返回 `body.temperature / less_than_equal`：客户端温度 **1.62**，上限 **1.5**；不是思考/正文格式转换或账号额度错误。既有 NewAPI 日志在远端内存交叉确认同一数值，未保存用户正文、凭据或身份。

- 仅精确 `deepseek-v4-1-flash-1017` 的 Chat 增加请求前校验：temperature 必须为不超过1.5的数字、null或省略。超上限/类型错误/重复字段（含转义字段名）本地400 `invalid_request`，在选号/RPM获取前拒绝，不换号、不影响账号健康。
- **不会静默将1.62改成1.5**，以免改变用户采样意图。客户端或 NewAPI 的有效请求温度应设置为 **≤1.5**，或省略该参数使用上游默认值；网关升级本身不会使原来的1.62请求成功生成。
- 合法温度原始数值拼写、完整历史、工具/大整数、缓存键、思考内容及流式响应保持；不依据这条错误推断新的下限，不给其他模型或 Responses/FIM/其他号池套用未经验证的上限。其他上游校验错误仍按原策略处理，不靠删除参数或重放掩盖。
- 回归使用合成 HTTP JSON/SSE Mock，覆盖本地零上游尝试/零账号惩罚、合法参数与工具/思考格式转换、模型/协议/供应商隔离。**未修改生产温度、渠道规则或采集设置，未主动生产推理、重启或部署**；本地测试不冒充生产恢复验收。

## 号池端思考/正文顺序恢复（2026-11-11 17:36 UTC，50222f6修复版已部署OVH）

首版46a9b8f生产验收失败（25个恢复请求均400），已确认是closed=true思考流片段被误拆成多个leading ThinkChunk触发上游3051。用户授权修复否则回滚后，50222f6已修复连续思考片段及旧checkpoint合并，文字/真实交错位置不变。新版本上线后约152秒自然样本中，9个恢复请求全部200、恢复16条历史，0新3051/422/本地布局拒绝；因此保留修复版，未执行回滚。这是短窗口真实业务成功，不代表永久或全模型/旧历史/缓存保证；原失败和修复证据均保留在部署记录/任务research中。

不要求客户端或NewAPI配合修改。三个精确推理模型的Chat响应仍是普通`content`/`reasoning_content`，号池只保留有界的块类型、长度、闭合枚举和私有HMAC，不存正文或思考原文。下一轮在相同Key/分组/会话/模型里，完整两字符串匹配后可将`正文A→思考B→正文C`恢复成原逻辑顺序，不再统一前置思考；不修改原HMAC、选号粘性、缓存键、工具或历史。

- 支持JSON与逐帧流式，首帧不等待整流；正常重启可从派生checkpoint恢复。最多8192条、估算16MiB、单条512块/分段，24h命中续期；旧记录被淘汰后不保证仍可恢复。
- 布局记录有冲突、过期或无法完整还原时，按「思考在前、正文在后」继续请求，不再因这一项本地返回400。只对受影响的历史条目兜底，不删消息、思考或工具；原生thinking历史仍是权威。兜底不写为原生记录，仅真实新响应会留下新的顺序；不增加重试或保证缓存命中。
- **部署前旧历史、外部导入、客户端改字/丢思考、未捕获或已被LRU淘汰的记录无法逆推**：沿既有兼容并明确标`legacyUnknown`，不能称这些记录也已无损恢复。上游本来只提供两个字符串时同样未知。
- 号池响应技术头`X-Conversation-Layout`与管理员请求摘要`mistralLayout`显示恢复/未知/原生等计数；兜底历史条数仅记在管理员摘要的`fallback`，不向调用方暴露内部失败原因或原始错误。中转不一定保留技术头，功能不依赖客户端读取它。
- SSE恢复的是逻辑块顺序，跨帧同类型续片会合并；不恢复原网络分帧、JSON属性次序/转义或供应商隐藏块边界，也不承诺上游token前缀完全相同或缓存命中改善。
- 新增可选`metadata.mistralLayouts`派生字段。复用合并保存与正常退出强刷，崩溃可能丢未刷记录；降级严格旧版前先备份当前data，仅去掉该派生字段，不能用旧data覆盖新业务写入。

## 1910套餐资格与3505后端容量精确策略（2026-11-10，已部署OVH）

新版默认规则区分两种不同问题，不改缓存键、历史或重试账号上限：

- **HTTP403 + 顶层code1910**：`mistral-tier-not-allowed` 仅冷却该账号的该模型24小时，不硬隔离整号、不扣全局健康；在原账号尝试上限内可换号，最终失败返回503，避免中转把整个渠道当作认证失败。资格变更后可用既有账号“恢复”清除模型冷却。
- **HTTP429 + 顶层code3505**：`mistral-backend-out-of-capacity` 表示后端容量不足，不是余额或账号RPM不足；不扣账号健康、不重复换号，返回429和独立错误码 `upstream_backend_out_of_capacity`。原始 `Retry-After`（秒数或HTTP日期）保留；上游未给时不编造恢复时间，由客户端自行退避。没有新增跨请求模型停用器，也不保证官方容量恢复。
- 其他403/429、已有503/code3800及其他号池保持原策略。只有nil或两个已发布版本的完整、未修改出厂规则会自动升级；管理员自定义的规则、顺序、备注、禁用项与空列表原样保留。

**已随f1024e9部署，并按用户授权在现有Mistral自定义列表前补入两条精确错误规则及3505重试stop，revision5672。** 原自定义403/429等规则仍在且顺序不变，只对精确1910/3505优先处理；其他配置/凭据保持相等、原13个已隔离账号未恢复。新旧内网入口、健康/版本、真实公网资源及设置保存/读回验收通过，无主动推理或真实错误重放，不宣称已解决GLM缓存低命中。后续代码升级仍不能擅自覆盖其他自定义规则或整表重置。

## 缓存率展示、清空与会话头优先级

账号列表新增「24h 缓存命中率」，概览右上角新增「清空24h缓存命中率」。命中率为**已测缓存读取Token / 同批已测提示Token**，不是命中请求占比；无测量显示 `—`，实际测得零命中才显示0%。清空需确认，只重置当前号池概览、账号及模型的命中率样本，从点击后重新累计、最长24小时；原始24h请求数/Token/费用、单条日志、账号健康及会话绑定均保留，不清除上游缓存。重置立即保存，重启后保留；其他号池不受影响。已于2026-11-08 21:13 UTC随a6d7d38部署OVH；本轮未清空线上统计。

**补入 `prompt_cache_key` 的优先级（Mistral Chat / Responses，取第一个有效值）：**

1. 正文已有 `prompt_cache_key`：原值保留，含显式 `null` / 空串；Chat的 `promptCacheKey` 先按既有规则归一。
2. `X-Prompt-Cache-Key` → `Prompt-Cache-Key`。
3. 管理员配置的「额外会话头」，按配置顺序。
4. 父会话头，按下列固定顺序。
5. 内置会话头，按下列固定顺序。
6. 无有效头时：`apb-mistral-<现有会话身份HMAC>` 兜底；没有身份则不填入。

头值按原值（去首尾空白）直填，不哈希；空白、超过512字节或含控制字符则跳过。头名大小写不敏感，但 `Session-Id` 和 `Session_id` 是两个独立别名。

父会话头固定顺序：

```text
X-Opencode-Parent-Session-Id
X-Parent-Session-Id
X-Parent-Session-Affinity
X-Codex-Parent-Thread-Id
X-Codex-Parent-Session-Id
X-Claude-Code-Parent-Agent-Id
X-Claude-Code-Parent-Session-Id
```

内置会话头固定顺序（从上到下，全部参与上述第5级）：

```text
X-Claude-Code-Session-Id
X-Claude-Session-Id
X-Anthropic-Session-Id
Anthropic-Session-Id
X-Codex-Session-Id
X-Codex-Thread-Id
X-Codex-Conversation-Id
X-Pi-Session-Id
X-Pi-Session
Session-Id
Session_id
X-Session-Id
X-Session-Affinity
Thread-Id
Thread_id
X-Thread-Id
Conversation-Id
Conversation_id
X-Conversation-Id
X-Deepseek-Harness-Session-Id
X-Opencode-Session
X-Opencode-Session-Id
Agent-Session-Id
X-Litellm-Session-Id
X-Http-Session-Id
X-Grok-Conv-Id
X-Task-Id
X-Kilocode-Taskid
```

**账号粘性的身份优先级不同**：额外会话头 → 父会话头 → 正文 `metadata.parent_session_id` → 正文 `session_id` / `conversation_id` / `thread_id` / `metadata.session_id` / `metadata.conversation_id` / `metadata.thread_id` / `metadata.sessionId` → Claude结构（先 `metadata.user_id`，后根 `user`）→ 上述内置头。没有真正会话时，才回退正文 `prompt_cache_key` → 两种缓存头 → 首条系统/用户消息HMAC。Mistral无状态Responses不支持 `previous_response_id` 续传。

Claude结构支持 `_session_UUID`，以及包含会话字段的JSON**字符串**，字段顺序为 `session_id` → `claude_session_id` → `agent_id` → `thread_id` → `conversation_id` → `parent_session_id` → `parent_agent_id` → `parent_thread_id` → `parent_conversation_id`；普通user标签不是会话。正文会话只用于身份解析，无可用HTTP头时通过HMAC兜底补缓存键，不是直接复制正文会话原值。

**管理员「客户端头」原值展示（已随a6d7d38部署OVH）**：按用户明确授权，新采集的Mistral请求摘要保留会话头、父会话头、额外会话头、缓存键及 `X-Affinity` 原值，保留原始空白和多值；仍限制前64个头、每头4个值、每值512字节、总量8KiB，超限会标记截断。`Authorization`、API Key、Cookie等认证凭证及已知密钥继续隐藏，即使被配置为额外会话头也不能绕过。日志API只允许管理员会话访问并设置 `Cache-Control: no-store`，不会通过下游运行Key开放；正文、上游详情、错误日志及其他号池的脱敏规则不变。旧日志中的 `[REDACTED]` 无法还原，并不代表NewAPI屏蔽了头值。识别与转发始终使用原始请求，不因展示规则改变；实现见 `internal/server/identity.go`、`mistral_request.go`、`mistral_client_headers.go`。

## 临时完整前缀与连接诊断（本地已验证，未部署）

新增默认关闭、按精确下游Key ID临时启用的[缓存链路诊断](mistral-cache-diagnostics.md)：30分钟/128请求上限，不必开启正文详情，即可在请求日志查看入站、准备后、实际HTTP正文的HMAC，旧消息完整前缀/消息与工具排列变化、schema属性顺序与连接复用信息。只观测自然流量，不主动发推理或改变缓存/连接/调度策略；不记录正文、原始会话或凭据，不能把诊断交付称作缓存问题已解决。生产启用须另获部署授权。

## 原生接口与额度

支持 `GET /v1/models` 以及原生 `POST /v1/chat/completions`、`/v1/responses`、`/v1/responses/input_tokens`、`/v1/fim/completions`、`/v1/embeddings`。不增加 Anthropic 协议转换，也不因识别 Claude 会话头而宣称 `/v1/messages` 可用。

Mistral Responses 为**无状态预览接口**：每次在 `input` 携带完整历史，`previous_response_id` 必须省略或 null，`store` 必须省略、false 或 null。不建立服务器端 Responses 续传绑定。

Responses SSE识别`event:`和根`data.type`中的终态，首帧即`response.completed` / `response.incomplete`也立即结束，不等待上游关闭连接。合法空output、纯工具调用和incomplete协议终态不因没有文本误判为断流（incomplete仍不表示模型已完整生成）。已输出后遇到`response.failed` / error立即按失败结束；EOF/读错误而没有终态时记录失败，并追加一个明确error事件，不伪造completed/[DONE]/usage，也不重放请求或归咎账号Key。尚未输出任何有效事件的空流返回502 `upstream_response_incomplete`；已提交的HTTP200不会假装改成502。其他池和Mistral Chat保持原有结束策略。

### 模型与思考兼容范围

| 模型 | 上游原生ID | 本项目档位策略 |
|---|---|---|
| DeepSeek V4.1 Flash 1017 | `deepseek-v4-1-flash-1017` | low / high / max，缺省high |
| GLM 5.3 | `zai-glm-5-3` | low / high / max，缺省high |
| GLM 5.3 Flash | `zai-glm-5-3-flash` | low / high / max，缺省high |
| Mistral Large 4 | `mistral-large-4-0` | 原生透传，不套用上述默认/归一策略；官方档位low / medium / high |

NewAPI里的`deepseek-v4.1-flash-1017`、`glm-5.3`等客户端别名应由渠道映射到实际上游ID；本轮不新增模型别名配置，不将Modal的`glm-5-3`短名冒充Mistral原生ID。DeepSeek专属的store/图片/seed等适配不因公共思考控制或developer角色兼容而扩散到GLM/Large；developer角色兼容的精确范围和本地/部署状态见上文。

模型目录可见、协议Mock通过和真实客户端可用是不同验收层级；不能仅凭目录宣称四模型全部获得推理授权。

“刷新额度”与自动轮询同时接入模型目录、本 Key 对应的套餐预算及独立组织订阅；预算沿现有 Snapshot/metadata 保存，页面显示币种、约剩余/总额、已用百分比、重置/检测时间和 PAYG 状态。失败保留上次值但标为历史/未知，403不把仍可推理的Key判失效；目录失败与预算失败分别记录。更换 Key / Base / 代理后失效旧快照，晚返回不能复活旧数据。同工作区多个 Key 可能共享限额，不能累加成独立账号额度。真实且未过期的预算参与 smart/least-used/pace 的余量排序；未知不伪造零，不把 Mistral 预算放进 OpenDesign 付费分类，不代改官网 PAYG 或凭预算假造钱包余额。Mistral `base_model_id` 是模型来源，不当作别名，避免将微调模型改发成基础模型。

组织订阅读取额外以3秒为上限，不跟随重定向。UI明确区分Key访问等级与组织套餐（真实样本API访问等级Free、组织订阅Student）；订阅失败不会使成功预算失效，保留上次订阅并标错误。组织API/Vibe PAYG可空，未知不伪造关闭，也不代用户开启。预算reset_at是额度重置，不冒充账单续费日期。

### API Key 读取套餐额度（2026-11-08 实测）

官方 [Vibe billing 实现](https://github.com/mistralai/mistral-vibe/blob/main/vibe/app_server/_billing.py) 和 [whoami 实现](https://github.com/mistralai/mistral-vibe/blob/main/vibe/setup/auth/whoami.py) 提供了普通 Key 可用的读取路径，均使用 `Authorization: Bearer <API Key>`：

- `GET https://console.mistral.ai/api/vibe/whoami`：按 `plan_type` 区分 Studio/API (`API`) 与 Vibe (`CHAT`)，不要仅由账号名称猜测。
- `GET https://api.mistral.ai/v1/billing/subscription`：读取 `plan_name`、`payg_enabled`、`vibe_payg_enabled` 等套餐信息，不是剩余额度本身。
- `GET https://api.mistral.ai/v1/billing/budget`：返回 `api_budget`、`vibe_budget`，窗口字段含 `initial_budget`、`usage_percentage`、`currency`、`reset_at`、`payg_enabled`。**两项预算对 Studio 和 Vibe Key 都会返回，但号池一律取 `api_budget`**：Mistral 规定第三方工具或网关调用 `/v1/chat/completions` 的用量只计入 `api_budget`（[官方订阅说明](https://docs.mistral.ai/admin/billing-usage/subscriptions#included-monthly-usage)），`vibe_budget` 只供 Mistral 官方产品（Vibe CLI/Web）使用。剩余约为 `initial_budget × max(0, 1 − usage_percentage / 100)`，金额由百分比推算，并非额外测得的钱包余额。

### 号池可用余额是 api_budget（2026-11-27 生产确认）

`kevin-11.26` 的三把 CHAT（Vibe）Key 实测 `GET /v1/billing/budget` 返回 `vibe_budget` 300 USD / 已用 0%，而 `api_budget` 30 USD / 已用 100%，推理请求全部返回 `402 {"type":"billing_api_budget_exhausted","code":"2303"}`。旧实现按「CHAT 取 vibe_budget」显示，于是界面写「剩 300」、选号排序和概览合计也按 300 计算，与实际不符。

现契约：

- 账号页「号池可用（api_budget）约剩余」是唯一代表号池实际可用的余额，CHAT 与 API 两种 Key 相同；它参与 smart / least-used / pace 的余量排序与概览合计。
- `vibe_budget` 仍读取并展示为「Vibe 预算（仅供查看，不计入合计/排序）」，并标注**「仅限 Mistral 官方产品，号池调用不扣此项」**；它不参与排序，也不进入概览「总额度剩余」。上游没返回该项时显示「上游未返回该项（不是 0）」。
- 缺 `api_budget` 即视为读取失败，**不回退**到 `vibe_budget`；`vibe_budget` 缺失或非法只影响这一项展示。
- 同一工作区按 org 去重的规则不变（概览仍是一个工作区一行，取该工作区读到的最大值一次，不按 Key 累加）。
- 快照新增可选字段 `mistral.vibeBudget`，落盘与克隆均按值拷贝；旧快照里的 `budget` 会被视为 api 预算。

本次取 `zxg-01`、`zxg-10` 两组四把真实 Key，经各组固定代理读取 whoami/budget 均为 HTTP200；两类 Key 都能看到所属组织的两项预算，但必须按类型分别归属，不能在每把 Key 上重复累计。同组织相同预算只计一次，币种以上游字段为准，不假定 EUR；样本为 USD，重置时间由 `reset_at` 给出。初始接口采样未覆盖全部账号；后续接入现有自动/手动刷新。2026-11-08 `77e9709`已上线，生产40把Key预算新鲜、组织订阅全部成功持久化（Student6/Pro34，可能共享组织预算），CHAT/API两类各一次真实手动刷新通过。新增显示由Node与生产API投影核验，未做本轮真实生产管理员浏览器保存往返或主动推理；完整部署/验收边界见`docs/ovh-deployment.md`。

官方 CLI 注明 budget 可能需要 `see_org_inference_usage` 权限；403/缺字段/失败应显示未知，不能当作零。企业 `GET /v1/admin/usage` 是另一套 Admin Key 权限（本次普通 Key 为401）；第三方声称的 `/v1/billing/usage` 本次为404，不能据此否定已成功的 budget 接口。探针必须匹配 Go SOCKS 远端解析语义：curl 对 `socks5://` 默认本地解析，本轮曾出现 TLS EOF，改为同一代理的远端 DNS 探测后200；未改线上代理配置、直连回退或绕过权限。

管理台「真实请求」测试 Mistral 时，未填写模型默认使用 `deepseek-v4-1-flash-1017`，仍为 `max_tokens=1`；显式填写的模型保留。「免费检查」只读目录，不发推理请求。

参考：[官方文档](https://docs.mistral.ai/)、[Responses](https://docs.mistral.ai/inference/conversations/responses)、[用量和限制](https://docs.mistral.ai/admin/billing-usage/usage-limits)。

## 首字分段时间观测（2026-11-24，本地实现）

请求日志新增**仅 Mistral** 的 `mistralTiming`：请求级记录入站读取、准备、入池等待与入站字节；每个上游尝试记录连接等待/复用、请求写完、响应头、首事件读到、首事件 Flush、首思考/工具/正文 Flush、末事件/终态 Flush 和单次 Write+Flush 最大值。管理员请求日志行出现「首字分段」按钮，打开后按阶段展示并标注相对起点与缺失值。旧日志、其他池和未到达的阶段不产生字段，也不补算。

字段口径（`internal/server/mistral_timing.go`）：

| 字段 | 起点 | 语义 |
| --- | --- | --- |
| `uploadUs` / `prepareUs` / `admissionUs` | 请求读取开始 / 读取结束 / 每次准入前 | 客户端上传读取（含读 deadline）；JSON 校验、身份、缓存键与兼容适配、布局还原；`Acquire` 租约等待（多尝试累计） |
| `httpStartUs` | handler 入口 | 真正开始 `client.Do` 的时间 |
| `connectionReadyUs` / `connectionWaitUs` | HTTP 开始 / `GetConn` | 连接就绪（含代理握手）；最后一次 `GetConn → GotConn` 的等待；`connectionReused` 表明是否复用 |
| `requestWrittenUs` / `responseHeadersUs` | HTTP 开始 | 请求体写完 / 收到响应头（上游 TTFB 起点） |
| `firstEventReadUs` / `firstEventFlushUs` | handler 入口 | 读到首个可转发上游事件 / 该事件 Write+Flush 成功 |
| `firstThinkingUs` / `firstToolUs` / `firstTextUs` | handler 入口 | 首个思考/工具/正文成功 Flush；只做有界探测，超限时 `outputProbeSkipped` |
| `lastEventFlushUs` / `terminalFlushUs` / `protocolTerminal` | handler 入口 | 末个事件、协议终态（`done`/`responses_terminal`）Flush |
| `maxWriteFlushUs` | — | 单次 Write+Flush 的最大值，含下游背压阻塞，单列而不并入首字 |

语义边界：

- **缺失 ≠ 0**：未观测到（旧日志、未到达、失败在更早处）为 `null`，只是亚微秒观测才写 0。
- `ttftMs` 仍是「首个可转发事件前的时间」，可能是角色/思考/生命周期帧，**不是客户端首正文**；`firstEventFlushUs` 与本网关 `ttftMs` 同为 handler 相对，可直接对照。上游真实生成耗时属于 `responseHeadersUs → firstEventReadUs` 区间。
- 非流式尝试没有首事件字段，只记录到响应头；已发出请求后失败仍保留连接与请求写入事实，不伪造响应头。
- 写阻塞（`maxWriteFlushUs`）、事件队列滞后（`streamMaxLagUs`）与上游模型耗时分开；日志分段不含 NewAPI/Nginx/公网，不能据此判定供应商解码速度或客户端首正文时间。

## 计划键元数据 metadata.call_source（issue #7216，2026-11-27 线上实测通过，未部署）

上游用**请求体** `metadata.call_source` 决定一笔 `/v1/chat/completions` 记到哪个钱包：带该键的请求按 Vibe 客户端处理，记入订阅的 `vibe_budget`；不带则当作普通开发者 API，记入 `api_budget`。`kevin-11.26` 这类订阅号的 `api_budget` 已 100% 用完、`vibe_budget` 仍有余量，因此直连网关一律 `402 {"type":"billing_api_budget_exhausted","code":"2303"}`，而官方 `vibe` CLI、桌面 App 正常——它们每次都带这个键。

依据（`yetone/magpie#7216`，2026-11-19）：

- 同一把 Key、同一 endpoint、只改 body：裸 body 6 次中 5 次 402；`metadata.call_source` 任意值 6 次全 200；只放 `client_name` 无效；`call_source` 放在 body 顶层无效。与 header、UA、cookie、`x-affinity` 无关。
- 官方 CLI 的形状是 `Authorization: Bearer`、`x-affinity == metadata.session_id`，body 带 `metadata.call_source = "vibe_code"`、`client_name = "vibe_programmatic"`。
- issue 评论补充：切换 provider 后历史里带 `reasoning_content` 的 assistant 消息会被上游 `422 extra_forbidden` 拒绝（与本次注入无关，本池既有思考格式兼容已覆盖）。

### 本池实现（默认关闭）

路由设置新增「Chat 计划键元数据」：

| 配置 | 行为 |
|---|---|
| `off`（默认/缺省） | 一个字节都不加，调用方 body 原样转发 |
| `chat-key` | 仅对**本池账号**中工作区额度读取已确认 `keyType=CHAT`（Vibe Key）的账号追加；未读过的 Key 视为未知，不注入 |
| `all-keys` | 本池所有 Key 的 Chat 都追加 |

`call_source` 只接受 `vibe_code`（留空即此值），`client_name` 只接受空或 `vibe_programmatic`。两者都是枚举字面量：本池不提供任意 body 模板，避免把请求稳定记到错误的那本账或伪造计费来源。配置为自由值或未知 mode 时保存直接 400，不落盘。

注入规则（`internal/server/mistral_request.go` 的 `appendMistralPlanKeyMetadata`）：

- 只在根上**完全没有** `metadata` 字段时，在根对象末尾追加一个字节、一个 `"metadata":{"call_source":...}` 字段；原有字节（含数字、转义、空白、尾部换行）位置不变。调用方自己带的 `metadata`（对象或显式 `null`）一律不改——那可能是它自己选择的账本。
- 只作用于 Chat。`/v1/responses`、`/v1/responses/input_tokens`、`/v1/fim/completions`、`/v1/embeddings` 与非 Mistral 池永不追加（报告只证明 Chat 的路由字段）。
- `all-keys` 在**请求准备阶段**（身份、缓存键、审计、详情之后，选号之前）追加，因此重试与换号复用同一份已准备 body，请求日志的入站/准备/实际上行指纹和会话身份都对得上。
- `chat-key` 只能在**选中账号之后**决定，因此在每次上游尝试前追加；它只新增根字段，不改 prompt_cache_key、模型、参数校验结果或内容——`prepareUs`、`X-Affinity`、布局还原与思考/图片/工具兼容全部照旧。
- 该追加不进入账号粘性、会话 HMAC、布局记录和额度排序；不做任何「换号重试直到成功」的新逻辑。

请求日志的模型列出现「计划键」徽标（`mistralPlanKey`），表示这一次实际带上了由号池追加的元数据；调用方自带的 metadata 不会让该标记为真，也不会被记录内容。

### 2026-11-27 线上实测（三把 kevin-11.26 Vibe Key）

在 OVH 上直接读取 `data/config.json` 里 `kevin-11.26-0{1,2,3}-vibe-花钱` 的 Key（只经 TLS 直连 api.mistral.ai 使用，不落盘、不回显、不写日志），对同一 endpoint 做单变量对照。三把 Key 结果完全一致：

| 请求体 | 01 | 02 | 03 |
|---|---|---|---|
| 裸 body（无 metadata） | 402/2303 | 402/2303 | 402/2303 |
| `metadata:{"call_source":"vibe_code"}` | **200** | **200** | **200** |
| `metadata:{"call_source":"vibe_code","client_name":"vibe_programmatic"}` | **200** | **200** | **200** |
| `metadata:{"client_name":"vibe_programmatic"}`（无 call_source） | 402 | 402 | 402 |
| `metadata:{"call_source":"other"}` | **200** | **200** | **200** |
| `metadata:{}` 空对象 | 402 | 402 | 402 |
| 顶层 `call_source`（不在 metadata 内） | 402 | 402 | 402 |

钱包归属直接证据：01 号连续 5 次带 `call_source` 的 Chat 全部 200，前后各读一次 `/v1/billing/budget`，`vibe_budget` 使用率从 `1e-6`% 升到 `6e-6`%（300 USD 下约 0.015 USD），`api_budget` 保持 100% / 30 USD 不动——**真的记到 Vibe 账上了**，不是绕过计费。

路径范围（01 号，其余两把同一现象）：

| 路径 | 裸 body | 带 `metadata.call_source` |
|---|---|---|
| `/v1/chat/completions` | 402 | 200 |
| `/v1/responses` | 402 | **200** |
| `/v1/embeddings` | 402 | **200** |
| `/v1/responses/input_tokens`、`/v1/fim/completions` | 未测 | 未测 |

由此可见：

- 上游**不校验 `call_source` 的值**，`vibe_code`、`other` 都放行——它只判断「metadata 里有没有这个键」。本池仍然只用 `vibe_code`，因为这是官方客户端实测发送的值，没有理由发一个上游可能随时收紧的任意值。
- `client_name` 单独存在**无效**，`metadata:{}` **无效**：注入必须带 `call_source` 才有意义。
- 502/402 的差异与出口无关：02 号配置的 SOCKS 代理当时连不通（`socks hello failed`），同一把 Key 直连即正常，说明问题不在代理也不在 Key 本身。
- 因此**路由范围扩到 `/v1/responses`**（实测同样 200），`/v1/embeddings` 也验证可行但本轮按「只动生成请求」的既定范围**不接入**；`input_tokens`、FIM 无实测证据，保持原字节不动。
- 客户端若自己带了 `metadata.call_source`（哪怕值不对），本池一律不改——那是它自己的计费选择，池子不替它纠正。

### 未做 / 仍属未知

- **尚未部署**：线上仍是 `488aa6b` 镜像，`config.json` 的 `mistral.chatMetadata` 为空（`off`），所以生产流量暂时仍会 402/2303。开关要随新镜像上线才生效。
- 没有经过号池端到端验证：上面的对照是直连上游的请求级验证，不是「部署后走 Balancer 再 200」。上线后需要一次真实调用确认（日志出现「计划键」标记且不再 402）。
- 额度读数的**方向变了**：注入生效后实际消耗记在 `vibe_budget`，界面「号池可用（api_budget）」会停在 100% 用尽不动，而 Vibe 预算缓慢下降。本轮没有擅自改额度展示与排序口径，是否要把 Vibe 预算纳入排序/合计，需要你决定——注意它现在是**真实会被消耗的那本账**了，这跟前面「vibe_budget 仅限官方产品」的结论并不矛盾，而是「网关带上官方客户端标记后，上游把它当官方产品计费」。
- 402/2303 的既有冷却规则不变：开关关着或调用方自带 metadata 时，仍然按 `api_budget.reset_at` 冷却。
- 未验证上游未来是否会加版本/客户端指纹校验，也未验证 Web/CLI 相同的其他字段是否必需（本次证明只 `call_source` 就够）。

### 本地同轮基准（HEAD `64c7b0c` vs 本轮工作树）

Apple M4 / Go 1.26.8，`-cpu 2 -count 5` 取中位；128 KiB / 512 KiB / 2 MiB 合成 Chat 历史（system + 64 轮 user/assistant，带 `reasoning_content`），对象在 HEAD 独立工作树与本树同轮运行：

| 阶段 | 128 KiB 之前 → 现在 | 512 KiB | 2 MiB |
| --- | --- | --- | --- |
| 路由解析 `parseRoutingBodyWithHistory` | 0.71 → 0.71 ms | 2.71 → 2.71 | 10.69 → 10.69 |
| 请求准备 `prepareMistralRequest` | 2.14 → 1.38 ms | 7.22 → 4.60 | 27.29 → 17.27 |
| 布局还原 `restore` | 7.50 → 1.91 ms | 25.77 → 5.40 | 97.70 → 19.31 |
| 准备+还原 | 9.69 → 3.32 ms（B/op 4.85 → 3.09 MB） | 33.37 → 10.07（16.61 → 10.09 MB） | 126.2 → 37.1（63.45 → 38.27 MB） |
| 端到端首字节（真实 Handler + 本地 Mock 上游，1 账号，128 KiB / 512 KiB） | 9.46 → 4.23 ms / 32.11 → 13.30 ms | — | — |
| 端到端分配 | 3.85 → 2.96 MB/请求 | 11.94 → 8.99 MB | — |

端到端基准在 Mock 上游立即返回时测量，反映**网关自身**加上环回 HTTP 的开销，不含真实模型 prefill、NewAPI、Nginx、公网与代理 RTT。

### 本轮改动（不改线上协议语义）

- 正文拼接与布局重建的一次性缓冲改为按原大小+余量 `Grow`：此前溢出一个字节就会翻倍并整份复制历史（`mistralWireObject.render`、`mistralChatMessages`、`restore`）。
- 原生思考块与 content 数组改为手写拼接，**字节与旧 `encoding/json` 输出逐字节相同**（对象键排序一致），由 `mistral_wire_identity_test.go` 固定；仅当已有 content 数组可能含可压缩空白时保留原编码器。
- `restore` 不再把原历史整体解码成 `[]json.RawMessage`（按消息偏移并行遍历配对），只把命中的 content 值就地拼接进一次性全文缓冲，不再「逐消息 render + 根对象再 render」；根 `messages` 重复键的请求直接保持 prepare 结果，不猜哪一处生效。
- 布局纯文本解析改用 wire view（`mistralLayoutPlain`/`mistralHasNativeThinking`），语义与旧的 map 解析等价（重复键取最后、未知键拒绝、null/类型校验不变），省掉每消息的 map 与数组 copy。

未改变：缓存键优先级与 `X-Affinity`、身份 HMAC、绑定/选号/TTL、布局记录内容与歧义兜底、响应适配字节、错误策略与重放边界；所有既有 Mistral 回归（含布局 3051/状态、报文顺序、参数/推理/图片/温度）保持通过。

### 生产现状与下一步

本轮**未部署、未发真实推理**，线上仍无 `mistralTiming`。上一次生产只读基线（14,582 条，2026-11-23）显示：流式成功首事件 P50/P95 1.69/4.21 s，`durationMs − 最后 attempt.ms` P50/P95 8/48 ms；≥200k 输入零缓存请求首事件 P50 10.36 s，而缓存读取占比 ≥90% 的为 2.55 s。即首字主要仍由**上游 prefill 与缓存命中**决定，本网关这几毫秒不是主因；本轮把网关前段压到 128 KiB 3.3 ms、512 KiB 10 ms 量级，主要收益是 CPU/分配与长上下文尾延迟，而不是把 1.7 s 变成几百毫秒。

按此证据，下一步优先级：部署含 `mistralTiming` 的版本后用生产自然流量核对「尝试外/连接/响应头/首事件」占比；再针对 ≥200k 零缓存桶核对绑定命中、身份来源与前缀稳定性（不擅自改缓存键或亲和策略）；之后再考虑单趟 JSON 校验索引复用与 prepare+restore 合并。旧的 3 亿 TPM 容量口径与验收边界不变。

## 长历史请求分配优化（2026-11-25，北京时间，已部署）

同一 Apple M4 / Go 1.26.8、`GOMAXPROCS=2`，以 `44d5dcd` 为基线，用合成历史对基线/补丁交替各跑 6 次，表为中位数。档位是基准标签（实际正文随 JSON 包装变化）；分配是 `B/op`，不是运行中峰值 RSS。

| 基准 | 分配/次 | 用时/次 |
|---|---:|---:|
| 512 KiB 档，路由解析 + 准备 + 布局恢复 | 6.37 → 4.91 MB（−22.9%） | 4.12 → 3.92 ms |
| 2048 KiB 档，同上 | 23.55 → 17.85 MB（−24.2%） | 14.48 → 14.01 ms |
| 512 KiB 档，真实 Handler + Mock SSE | 9.02 → 6.35 MB（−29.5%） | 7.16 → 7.18 ms，首字节基本不变 |
| 无需改写的原生长历史，仅准备阶段 | 259.7 → 104.0 kB（−59.9%） | 171.8 → 158.0 µs |

请求历史未发生改写时复用原切片，不再预分配历史大小的缓冲；需改写时直接将变更的消息写入历史缓冲，正文/思考标量使用一次拼接，保留既定 JSON 转义和顺序。布局匹配遇到只有一段非空文本时直接复用其解码字符串，多段仍按顺序精确拼接。字节回归覆盖 null、显式空串、含空白的既有数组、转义及大整数工具参数；Gemini 签名 422 兼容仍通过。未改变严格校验、真实会话绑定、布局 HMAC/持久化、缓存键、额度、端点或响应流。`c0d205c` 已部署 OVH。上表仍是本地基准，未做生产压测；这轮主要减少网关分配，不承诺真实上游首字、缓存率或容量收益。

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

### 单Key无正文HTTP取证（默认关闭）

既有`ODB_MISTRAL_CACHE_AUDIT_KEY_ID`明确指定一个启用的Mistral下游Key，每进程最多30分钟/128请求。除原入站→prepare→真实Body/GetBody读取指纹/前缀及连接证据外，新增有界脱敏请求/响应头、上游HTTP协议/TLS ALPN与最后完整usage诊断；不依赖详情开关，不记录正文，不改变用量计费/转发/重试。

头名最多64个、每头最多4值；普通技术头可读，未知/会话/亲和值只给随机窗口HMAC。认证/Cookie值始终掩码，最多16个Cookie名称用于诊断，不自动回传Cookie。usage最多8KiB/256节点/8层，完整保留计数/大数/未知数值字段，任意字符串（除standard/priority/USD/EUR）及不安全字段名脱敏，明确sanitized/truncated，不能把脱敏缺失说成上游未返回。启动参数默认空，需经授权发布/重启才能打开；建议独立一次性测试Key，测试结束禁用，旧配置不得因普通部署无授权续采。该功能是取证，不是缓存改善保证。

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
