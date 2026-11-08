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
- 识别 Codex 的 `Session_id` / `X-Codex-Session-Id` / `X-Codex-Thread-Id`，Claude 的 `X-Claude-Code-Session-Id` 与 `metadata.user_id` 中的会话，pi 可传的 `X-Session-Id` / `X-Session-Affinity` / `X-Pi-Session-Id` / `X-Pi-Session`，以及现有父会话、OpenCode 等会话头、正文会话字段和 `prompt_cache_key`。任意额外稳定头可在「额外会话头」配置。
- 优先级沿用 OpenDesign：`previous_response_id`（本池不支持，Responses 请求会本地拒绝）、自定义头、父会话、正文身份、Claude 元数据、内置会话头、可选首条消息 HMAC 弱粘性。不会把每次变化的请求 ID 当成会话。客户端必须实际发送稳定标识；仅凭客户端名称不能保证粘性。
- 会话 HMAC 按分组和下游密钥隔离。TTL 与忙时 `overflow` / `wait-overflow` / `reject` 策略沿用现有配置，临时借号不改原绑定。
- Chat / Responses 生成请求缺少顶层 `prompt_cache_key` 且已识别会话时，自动填入 `apb-mistral-<现有会话HMAC>`；HMAC按分组与下游密钥隔离，不暴露原始会话或凭据。客户端已有字段（包括显式null/空串）原样保留；没有身份不随机生成。共享前缀与同一账号有利于缓存命中，但不保证命中；缓存块64 tokens，官方缓存输入按普通输入价10%计费。
- 仅 Chat 历史中的 assistant 顶层 `reasoning_content` 进行兼容：非空字符串转为 `content` 内原生 `thinking` 块（`thinking:[{type:"text",text:...}]`），原回答、已有内容块、工具调用/ID保留，再移除不支持的顶层字段；null/空串仅移除空字段。不支持的类型本地400，不丢弃思考或换号重试。Responses和其他池不做此转换。NewAPI的“思考转内容”是响应侧`<think>`文本转换，不等价。
- 仅对 Chat 模型 `deepseek-v4-1-flash-1017`：根 `reasoning:{"effort":...}` 兼容为 `reasoning_effort`，只接受含一个 effort 字段的对象；多余字段、重复字段、非法结构本地400，不丢弃 summary 等请求意图。两种写法按相同规则校验/归一化，同值合并、冲突拒绝（显式 null 与非 null 也视为冲突）；嵌套 effort 为 null 且无开关时保留为原生 null。Responses、其他模型和号池不做此别名转换。
- 仅对同一精确 Chat 模型：两种 effort 写法及 `thinking` / `enable_thinking` 均未传时，默认补 `reasoning_effort:"high"`；显式开启无档位也补 high，显式关闭转 none，已有原生档位保留（medium 兼容为 high），冲突本地拒绝。无开关时显式 `reasoning_effort:null` 仍保持原值。high 可能增加思考用量和耗时，不自动增大 `max_tokens`；其他模型、协议及号池不套用此默认值。
- 除上述明确的缓存补齐、思考格式及精确模型参数兼容外，原生字段/工具/模型ID/实际客户端UA保持不变；没有转换的正文保留原字节。精确 DeepSeek Chat 仅省略不支持的根 `store:false/null`，`store:true` 本地拒绝；其他模型/协议不套用该规则。未知字段不静默丢弃，不支持时明确报错。
- 401 隔离失效 Key；402 冷却；429 尊重完整 `Retry-After`；临时 5xx 短冷却。确定性请求/权限错误不遍历账号。固定代理失败不回退直连；已发送且结果未知或流已输出的请求不重放；凭证请求不跟随重定向。

## 原生接口与额度

支持 `GET /v1/models` 以及原生 `POST /v1/chat/completions`、`/v1/responses`、`/v1/responses/input_tokens`、`/v1/fim/completions`、`/v1/embeddings`。不增加 Anthropic 协议转换，也不因识别 Claude 会话头而宣称 `/v1/messages` 可用。

Mistral Responses 为**无状态预览接口**：每次在 `input` 携带完整历史，`previous_response_id` 必须省略或 null，`store` 必须省略、false 或 null。不建立服务器端 Responses 续传绑定。

“刷新额度”与自动轮询现已同时接入模型目录和本 Key 对应的套餐预算；预算沿现有 Snapshot/metadata 保存，页面显示币种、约剩余/总额、已用百分比、重置/检测时间和 PAYG 状态。失败保留上次值但标为历史/未知，403不把仍可推理的Key判失效；目录失败与预算失败分别记录。更换 Key / Base / 代理后失效旧快照，晚返回不能复活旧数据。同工作区多个 Key 可能共享限额，不能累加成独立账号额度。真实且未过期的预算参与 smart/least-used/pace 的余量排序；未知不伪造零，不把 Mistral 预算放进 OpenDesign 付费分类，不代改官网 PAYG 或凭预算假造钱包余额。Mistral `base_model_id` 是模型来源，不当作别名，避免将微调模型改发成基础模型。

### API Key 读取套餐额度（2026-11-08 实测）

官方 [Vibe billing 实现](https://github.com/mistralai/mistral-vibe/blob/main/vibe/app_server/_billing.py) 和 [whoami 实现](https://github.com/mistralai/mistral-vibe/blob/main/vibe/setup/auth/whoami.py) 提供了普通 Key 可用的读取路径，均使用 `Authorization: Bearer <API Key>`：

- `GET https://console.mistral.ai/api/vibe/whoami`：按 `plan_type` 区分 Studio/API (`API`) 与 Vibe (`CHAT`)，不要仅由账号名称猜测。
- `GET https://api.mistral.ai/v1/billing/subscription`：读取 `plan_name`、`payg_enabled`、`vibe_payg_enabled` 等套餐信息，不是剩余额度本身。
- `GET https://api.mistral.ai/v1/billing/budget`：返回 `api_budget`、`vibe_budget`，窗口字段含 `initial_budget`、`usage_percentage`、`currency`、`reset_at`、`payg_enabled`。Studio 使用 `api_budget`，Vibe 使用 `vibe_budget`；剩余约为 `initial_budget × max(0, 1 − usage_percentage / 100)`，金额由百分比推算，并非额外测得的钱包余额。

本次取 `zxg-01`、`zxg-10` 两组四把真实 Key，经各组固定代理读取 whoami/budget 均为 HTTP200；两类 Key 都能看到所属组织的两项预算，但必须按类型分别归属，不能在每把 Key 上重复累计。同组织相同预算只计一次，币种以上游字段为准，不假定 EUR；样本为 USD，重置时间由 `reset_at` 给出。真实接口采样未覆盖全部账号；随后已接入现有自动/手动刷新。接入代码、失败/持久化及本地假数据浏览器已验证，生产生效仍须部署后另行验收。

官方 CLI 注明 budget 可能需要 `see_org_inference_usage` 权限；403/缺字段/失败应显示未知，不能当作零。企业 `GET /v1/admin/usage` 是另一套 Admin Key 权限（本次普通 Key 为401）；第三方声称的 `/v1/billing/usage` 本次为404，不能据此否定已成功的 budget 接口。探针必须匹配 Go SOCKS 远端解析语义：curl 对 `socks5://` 默认本地解析，本轮曾出现 TLS EOF，改为同一代理的远端 DNS 探测后200；未改线上代理配置、直连回退或绕过权限。

管理台「真实请求」测试 Mistral 时，未填写模型默认使用 `deepseek-v4-1-flash-1017`，仍为 `max_tokens=1`；显式填写的模型保留。「免费检查」只读目录，不发推理请求。

参考：[官方文档](https://docs.mistral.ai/)、[Responses](https://docs.mistral.ai/inference/conversations/responses)、[用量和限制](https://docs.mistral.ai/admin/billing-usage/usage-limits)。

## 验证

2026-11-08补齐额度与路由：全仓Go normal/race/vet与Node行为/语法、必要build通过；新增Mistral定向测试覆盖API/CHAT预算选择、缺字段/过期/403、失败保留、metadata往返、来源变化晚返回、独立轮询开关/并发/目录周期、完整设置/规则保存重载/跨池隔离、详情开关和余量排序。真实本地浏览器使用两把假Key完成批量刷新（Studio USD22.50/30、Vibe EUR270/300）、403显示历史未知、路由/错误码/重试规则保存及进程重启后读回；这是Mock验证，不是生产推理或上线验收。

本轮缓存/思考兼容/凭据展示与TH日志开关只运行受影响Go定向用例、Mistral/TH Node行为与JS语法检查，并用一次本地构建、禁用假账号完成页面核对；未做真实推理或实际缓存命中验证，不重复全仓/race/vet。以下为原始号池接入时的历史验证记录：

- `go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./cmd/odb`。
- `node internal/server/mistral_web_test.js`；真实本地浏览器使用假账号与本地 Mock，检查管理、导入、独立设置和后端状态。
- 本次用户提供的测试 Key：真实 `/v1/models` 返回 200 / 64 个模型；以 `ministral-3b-latest` 对 Chat 与 Responses 分别进行一次最多 8 输出 token 的小请求，均为 200，返回输出与 usage。真实凭证只在内存使用，不写入测试文件、日志或 Git；这不是 Codex / Claude / pi 客户端端到端验证或生产压力测试。
