# Factory AI 号池与全局号池启停

## 状态与边界

本版本参考 [magpie-community/plugins 的 Factory 插件](https://github.com/magpie-community/plugins/tree/main/packages/factory)，固定研究版本 `d24fd5d1824e1078e7c8c56732813f8263fcf728`。采用认证、模型路由元数据、只读身份/额度接口、Fast Mode字段映射，以及`anthropic-tools.mjs`的工具schema包装/结果解包；不是该插件的完整移植。MIT许可与来源见`THIRD_PARTY_NOTICES.md`。另复用本项目的有限协议桥接，而非将该插件误描述为通用协议桥。

**没有移植**：Droid身份/客户端伪装、系统提示词或工具结果改写、内容策略拒绝后的重新包装、组织或账号轮换绕过。Factory模型网关不是其Public API中的普通公开模型服务契约；上游可以拒绝第三方客户端。本版本不会为取得200隐藏这一限制。

### 用户授权 Key 的真实接口证据

测试凭据不写入源码、示例、测试数据或本文。

| 接口 | 2026-11-17 UTC 实测 | 含义 |
|---|---|---|
| `GET https://api.factory.ai/api/cli/whoami` | 200，约1502ms | Key可识别用户和组织，不证明模型授权 |
| `POST /api/llm/o/v1/chat/completions`，`deepseek-v4.1-flash`，64输出Token上限、无system | 403，`content_policy_rejected`，约2465ms | 被Factory使用策略检查拒绝；未取得模型输出，不通过改写/伪装绕过 |
| `GET /api/billing/limits`，17:14UTC | **200**，约1432ms | 普通fk-Key本次确实能读额度，不能长期硬编码未知 |

本次额度：Standard周、月各已用1%；5小时窗口及Core窗口 `windowEnd=null`，表示无活动窗口，不等于完整可用额度。Extra余额0美分，`extraUsageAllowed=true`是上游既有状态；未修改官网偏好。

**真实模型调用仍未通过；不得称为已可用或完整接入验收。** 官方支持API Key通过Droid CLI/SDK/Execute API运行Agent，不等于第三方裸模型请求受支持。本版本不创建Droid电脑、执行代码或调用支付接口。

## 管理入口

顶部「Factory AI」，使用已有概览、账号、下游密钥、导入密钥、路由设置、请求/错误/详情日志。

- `provider=factory`，默认分组`factory`，使用完整`fk-` API Key；其他池不能接受Factory Key。
- 复用账号CRUD、启停、去重批量导入、完整凭据查看/复制、代理、权重/优先级、并发/RPM。管理员读取保留登录/CSRF/no-store边界，凭据不写localStorage；日志脱敏。
- 批量格式：`fk-example_test_key,账号名,http://127.0.0.1:8080`。示例Key无效。
- 开放导入使用Factory专属导入密钥，显式`provider=factory`；下游密钥不能导入，导入密钥不能推理。

## 原生协议与模型

API源站默认`https://api.factory.ai`，也可明确设`https://api.eu.factory.ai`；必须是无路径、凭据、查询、片段的HTTP(S)origin。本地测试可使用HTTP Mock。

| 下游路径 | 上游路径 | 路由 |
|---|---|---|
| `/v1/chat/completions` | `/api/llm/o/v1/chat/completions` | 开源模型，x-api-provider按配置 |
| `/v1/responses` | `/api/llm/o/v1/responses` | GPT/OpenAI，Grok/xai |
| `/v1/messages` | `/api/llm/a/v1/messages` | Claude/anthropic，MiniMax M2.7/fireworks |
| `GET /v1/models` | 本地配置参考列表 | **不是账号授权目录**，响应标注`configured-reference-not-entitlement` |

模型路由JSON数组 `{id,protocol,upstream}` 可编辑、保存、重载；空数组真正清空。缺省来自固定版本参考，不发带Key的未知`/models`请求、不偷偷抓取客户端/猜测模型路由。未知模型或路径本地拒绝。Gemini generate、auto、Embeddings尚未实现。

### 插件适配与有限跨协议转换（续轮）

- **参考插件的工具schema转换**：Messages工具根级`anyOf/oneOf/allOf`整体包到必填`arguments`对象里，改写schema位置的本地`$ref/$dynamicRef`指针；独立`$id`、anchor以及`const/default/examples`中的普通数据不误改。历史`tool_use.input`、`input_examples`同步包装；调用ID、用户指令和工具结果保持。无`$id`的递归资源拒绝而非错误搬移。
- **返回还原**：仅当前请求确实包装的工具解包；真实调用者字段`arguments`不误拆。JSON与原生Messages SSE均保留原工具ID、Unicode、大整数及上游usage。SSE只缓存包装工具参数，文本/推理/普通工具继续流式输出；每工具参数4MiB、并行总16MiB、单帧8MiB上限。无效/截断参数不能当作完成，也不换号重放或降健康；已提交HTTP200时追加错误事件，不伪造终止成功。
- **Fast Mode**：参考模型列表包含Opus/GPT的Fast条目。显式Fast模型补`speed=fast`及Anthropic beta，或`service_tier=priority`；对基座模型显式请求fast/priority且对应Fast模型已配置时映射。普通请求不自动升级；GLM-5.2 Fast是独立Chat模型，不由priority切换。Fast可能更贵，不自动接受新付费条件。已有自定义模型列表不自动覆写。
- **本项目协议桥**：`factory.compatibility`控制Chat/Responses/Messages之间六个方向的有限转换，保存/重载后生效；新建默认配置开启，旧已保存但缺该字段的配置保留关闭，需在UI显式启用。按模型原生协议选择路径/x-api-provider，再转回调用者协议。复用已有工具/图像/完整历史桥，数字解码改用保真number token避免>2^53舍入。
- **跨协议SSE是缓冲模式**：总响应16MiB、单事件2MiB上限，等完整成功响应后输出目标SSE，返回`X-Compatibility-Mode: buffered`；不是实时首Token流。原生SSE不进入整包缓冲。签名thinking、服务端续传引用、hosted tools等不能无损表示的字段本地400；响应不能表示则502且不重放，不悄悄删字段或编造usage。`previous_response_id`只能留在原生Responses，虚拟转换出的Responses ID不承诺上游续传。

原生请求除显式Fast/工具schema适配外保持原始JSON字节、指令及原生字段；不改变调用者身份。仅替换认证、按管理员配置设置x-api-provider，使用真实的balancer User-Agent，不声称Droid CLI。API Key本身已组织绑定，不传客户端的X-Factory-Org-Id，whoami提供的region/private-host只作诊断，不自动将凭据送往返回域名。不跟随重定向，不在代理失败后直连。

NewAPI可按模型原生协议选择渠道，也可显式开启上述有限转换使用可表示的请求；转换不消除上游授权/策略限制。列表可见不代表可推理。默认真实测试模型`deepseek-v4.1-flash`，保留手填；模型测试与身份/额度检查分别显示结果。测试请求32–64输出Token上限，不遍历模型/账号。

## 独立路由与会话

Factory独立保存：选号、粘性、会话头、显式/弱粘性TTL、忙时策略、等待、尝试上限、健康阈值、首事件/空闲/总请求超时、请求体/模型缓存、直连/代理连接上限、额度轮询及价格/规则。

缺省在途最少、粘性开启、绑定忙时拒绝。真实会话标识优先于缓存提示，身份HMAC按分组和下游Key隔离；缓存亲和不保证上游缓存命中。

Responses续传只认已确认、未过期、同Key/Base/代理来源的原账号绑定；不存在/过期/来源变化时409，不猜号或跨账号重放。非流式完成或流式`response.completed`后才记录响应绑定；中途创建事件不作为完成证明。普通会话TTL/LRU与响应链沿现有公共预算，不宣称独立百万级容量。

## 额度全链路

独立轮询（默认600秒）、独立并发预算、手动刷新 → `whoami`身份强类型校验 → `billing/limits`强类型窗口/余额校验 → `Snapshot.Factory` → metadata持久化 → 账号API与页面。额度/身份来源绑定provider+完整Key+Base+代理；来源变化失效，锁内再校验，旧来源迟到结果不发布。身份的user/org变化也清空原组织读数。

- Standard/Core各有5小时、周、月百分比及reset；空/缺失/错误类型不当零。窗口尚未激活、已过期、读取失败、旧历史明确展示。
- 额度401/403不等于推理Key无效；保留历史，标`unsupported-credential`，停止周期重试billing，手动刷新可重新验证。身份401单独记录AuthFailed。
- 已知新鲜、仍活动且达到100%的对应窗口参与调度门禁；未知/过期不伪造耗尽或补满。Core分类采用固定参考模型清单，不能仅按托管厂商推断（Muse Spark 1.2属于Standard）。
- `standardOnly`默认关闭；开启时所有模型按Standard窗口门禁，不以Core/Extra绕过本地限制。关闭时按模型Standard/Core分类，若官网已许可且真实Extra余额>0，则窗口耗尽不会在本地拦截。**这不修改付费偏好，也不能保证零额外支出；要保证不花Extra须在官网关闭。**
- 多Key可能共享用户/组织预算，概览只计新鲜状态，不汇总百分比或重复余额。价格表默认空，费用未知，可填手动估算价，不是上游账单。

## 错误、重试与日志

- 独立默认规则，可增删改、启停、排序，按状态/正文/响应头匹配，支持账号或账号×模型作用域、冷却/恢复/刷新/下游状态；未证明共享funding身份，不允许funding作用域。
- `content_policy_rejected`执行层强制停止，不扣Key健康分、不换号/出口、不改组织/内容；即使自定义switch-account规则也不能覆盖。
- 401短暂账号冷却；402停止且不付款；403停止、不直接判封号；429默认停止，完整遵守秒数或日期Retry-After；明确HTTP5xx可在安全条件和尝试预算内重试。
- 已发送结果不确定、流已输出、有状态续传不重放；截断流记失败，不伪造DONE。Responses需真正终止事件。
- 请求/错误摘要、关联失败详情沿既有设施；Factory详情默认关且独立，每体最多64KiB并受共享更小预算约束，Key/Cookie/会话标识脱敏。保留天数/总量是全局共享，只读展示，保存Factory不改变别池。

## 全局号池启停

侧栏「号池管理 / 启停」，独立于当前池，全部关闭仍能打开。

- `GET /api/pools`；`PUT /api/pools/{provider}`，请求含revision与enabled，沿管理员会话、CSRF和冲突保护。支持所有推理池及Telnyx注册池。
- `Config.poolSwitches`是稀疏map，缺项默认开，旧配置不自动关闭在线池；开关落盘、重载/重启保留。
- 关闭隐藏日常标签；新的推理/目录返回503（而不是错误鉴权），调度也排除；后台quota刷新取消、旧epoch结果即使重新开启也不回填；手动上游测试/注册操作拒绝。
- Perch续期/健康、ALTAPI每小时目录/自动补号，以及各注册worker挂池级context；关池取消工作与后续排队，保留已获取的业务数据。无上游请求的必要定时器/共享元数据保存/日志保留清理仍在，不承诺进程内存立即归零。
- 已在途推理允许自然结束；等待期间关闭或下一次换号前会重查，不再新增上游尝试。切换不是删除账号/清空日志，不改变账号自己的Enabled与其他池配置。
- 重新开启建立新后台epoch、按既有轮询配置恢复，不打开原本关闭的quota/OpenCode/自动补号，不自动重跑取消注册。

## 本地验证与交付状态

已执行：
- `go test ./...`（server79.900s）、`go test -race ./...`（server123.848s）、`go vet ./...`、构建与diff检查通过；Factory/启停重点竞态重复10轮通过（server23.534s）。
- Factory配置/分组/凭据/保存重载与空规则、额度失败/权限/零/未激活/窗口/历史/持久化、CRUD/导入去重/跨池密钥、原生三协议/字节保留、自定义规则/策略安全门/未知结果不重放/Responses原绑定、SSE截断与重定向、全池启停鉴权/revision/取消与迟到/在途自然结束/全部关闭可恢复。
- 全部16个前端`*_web_test.js`与全部Web JS语法检查通过；修正既有Telnyx导航测试桩与新全局管理页例外，保留原池隔离断言。
- 真实本地浏览器（仅Mock/假Key）：Factory路由weighted、会话头、TTL180、standardOnly/详情保存；重载后表单/API/磁盘一致；真实点击刷新显示Standard/Core未激活与1%窗口；关池隐藏且新推理503/上游计数不变；全部关闭后刷新仍有管理页；真实点击重新开启恢复200；同Mock源服务重启后额度4窗口与设置/启停均保留，轮询原关闭仍未发上游请求；错误/重试规则可视停用、保存、重载/API/磁盘一致且Deploy规则不受影响；无页面JS错误。

- 续轮验证：六种跨协议方向的JSON/SSE、工具schema根级composition/ref/resource/字面数据/历史/示例、Unicode/大整数、普通工具字段不误拆、参数截断/错误/超限/关闭传播、Fast映射/保留beta、无损限制本地拒绝/无上游无健康消耗；新增浏览器转换开关关闭400零上游、开启200转为Chat、保存与重载/落盘一致，16个前端回归通过。续轮最终`go test ./...`通过（server89.478s）、全仓竞态通过（server141.916s）、vet/build/diff/gofmt通过；直接运行公开插件schema适配与Go实现的8组同输入结构对照一致（不含身份/提示词改写）；重点竞态重复5轮通过。

本轮代码与文档按用户授权提交并推送；未部署。真实Factory推理成功、全部模型/第三方客户端授权、生产NewAPI渠道验收仍未完成。OAuth登录/续期、Droid云端Agent、注册与支付不在此次实现范围。没有创建Trellis任务。临时公开源码、Mock、日志和浏览器回执已登记，清理需用户同意。
