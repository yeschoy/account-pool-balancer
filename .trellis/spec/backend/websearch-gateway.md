# 独立 WebSearch 网关（第一阶段）

## Scope / Trigger
变更 `cmd/websearch-gateway`、`internal/searchgateway`、`internal/websearch` 时阅读。本服务独立运行，不是Mistral池内静默删工具功能，也不是新增号池。实现/限制/启动方式的唯一用户文档为 `docs/websearch-gateway.md`。

## Boundary
- 受控 `/r/<route>/v1`：固定管理员配置的上游Base、路由Bearer和独立上游凭据；HTTP请求不能选择凭据目标。Search只调用管理员配置的HTTPS endpoint；真实密钥来自环境变量，样例/源码/Trellis不存密钥。
- native原字节透传；compat只适配Responses hosted web_search或已有网关历史；disabled明确拒绝。POST须经过认证、模型允许名单和资源限制。非搜索/其他进程/原Balancer代码保持隔离。
- RawMessage保留数字、函数参数、reasoning与未知原生字段。重复/转义重复字段拒绝；不吞无法表示的搜索选项、cached-only、preview、open/find、服务端续传或存储语义。
- 服务端只执行保留名搜索函数；其他函数交还客户端。真实搜索后回填原生function_call_output并继续；混合普通函数未有客户端结果时不得继续模型调用。内部响应终态不是整个请求终态。
- 响应要使用一个公开response ID/全局sequence/output_index，隐藏内部函数声明/参数事件及内部续轮budget；真实文本逐帧推进。流式未知/截断/不支持的交错明确失败，不能补completed/DONE、重放或静默重排。JSON/终态单包文本输出为SSE时也必须产生真实文本事件。
- 完整多轮usage才能作为总计；缺失不是零，也不能拿最后一轮充总数。搜索credits另记、有完整性标志，不混token。
- 公开web_search_call只能来自真实调用，sources取真实结果；不伪造url_citation，不承诺模型必然引用。历史仅存搜索call/result/public item，在有界内存LRU+绝对TTL，随机ID和caller/route/session HMAC；过期/重启/改变/跨作用域409，不能重搜冒充旧结果。
- 不新增账号调度；每轮调用原号池自行准入/RPM/计费。普通粘性不等于跨轮固定账号，尚无owner协议时只能列为原型，不能宣布生产连续性验收通过。
- 带凭据HTTP禁止重定向；不自动重试任何已发送模型请求；客户端取消传播。安全日志不可含提示、query、网页结果、原始会话或凭据。超时/次数/并发/事件/body/历史都须有硬边界。

## Tests
`internal/websearch/tavily_test.go`：真实本地HTTP鉴权/参数、结果和UTF8边界、401/429/重定向/超限/坏来源/超时取消、无重试或敏感body回显。
`internal/searchgateway/*_test.go`：受控配置、模型/凭据/路由拒绝、两轮与历史/作用域/TTL/LRU/重启、另一上游、普通函数往返、JSON/SSE真实首字/终态/序号/输出位置、失败不重放、usage覆盖、原生字节、参数/预算/体积/并发/取消、内部echo不泄漏。
normal/race/vet/build只针对独立模块。Mock、搜索接口实测、Codex/NewAPI端到端、已提交、容器构建、部署和线上验收必须分别报告；未授权不改线上渠道或启用功能。
