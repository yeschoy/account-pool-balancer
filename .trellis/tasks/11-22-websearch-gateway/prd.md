# 独立 WebSearch 兼容网关（第一阶段）

## 目标与授权
用户于2026-11-22认可独立服务、多号池/客户端复用的方案并要求「好开始」。本阶段交付独立 Go 进程/容器、Responses 兼容原型、Tavily HTTP 后端、静态受控路由与本地自动化验收；不修改生产配置、不部署、不改 NewAPI 源码。不是新增号池。

## 已确认事实
- Mistral Responses 实际拒绝 hosted web_search，仅接受客户端执行的 function。原协议转发/无终态不重放契约见 `.trellis/spec/backend/mistral-responses.md`。
- 用户指定 Tavily Hikari 的 POST /api/tavily/search，Bearer 鉴权。一次 basic/max_results=1 的公开文档搜索已返回200与标题/URL/content，约2.7秒；密钥仅在会话内存读取，不能入库。
- 原 Balancer 的代理调度/限流/日志集中在 internal/server/proxy.go，该文件及 factory_gemini_compat_test.go 存在其他工作的未提交修改，本任务不触碰。

## 要求与验收
- R1 独立运行：cmd/websearch-gateway 和独立 Dockerfile/Compose 示例；路由固定绑定管理员配置的 base 与上游凭据，调用方不能指定 URL/凭据目标。仅 Authorization Bearer 认证；配置只引用环境变量名，不提交真实密钥。
- R2 native/compat/disabled 三种明确模式；未带 hosted 搜索且无网关历史的流量原字节透传（JSON/SSE）。路由/模型隔离、鉴权失败零后端调用。
- R3 compat 仅 Responses、显式模型允许名单。消费 web_search 声明/对应 tool_choice/include，变为保留名函数，服务端执行搜索并回填完整函数调用与结果。其他 function 交回客户端，不执行终端/文件/MCP。未知搜索选项明确400，不丢弃。
- R4 流式逐帧推进（不缓冲整段回答）；隐藏内部函数事件，多轮仅一个公开响应ID、全局序号和 output_index；内部 completed 不提前终止。JSON与SSE都支持正常完成、incomplete、取消和真实失败，不伪造终态/结果/用量、不重试已发送请求。
- R5 下一轮搜索历史可还原：只保存有界、短期内存中的搜索函数调用/结果，按路由+调用凭据+会话隔离；过期/重启/跨作用域409，不重新搜索猜测历史，不保存完整提示词。来源只取真实结果和真实回答链接，不造引用。
- R6 每次搜索、轮数、并发、事件/响应/请求/历史缓存大小与总耗时有界；凭据不跟随重定向、不转发至非配置来源，客户端取消传播。摘要日志不含问题、结果、密钥或原始会话。
- R7 累加每轮真实模型用量；任一轮用量缺失不伪造完整总计。搜索用量独立，不混成模型token。
- R8 本地 HTTP Mock 覆盖完整两轮、混合普通函数、第二个上游、路由/会话隔离、历史过期、失败/超时/重定向、逐帧首字、截断、普通透传；normal/race/vet/build。

## 边界
第一阶段不宣称真实 Codex Desktop/NewAPI 已验收，不宣称所有模型/Chat/Messages支持，不宣称打开网页/页内查找已实现。暂用受限静态配置，无管理UI/热更新。经过现有号池的多轮调用各自进入原调度器；跨轮严格账号钉住尚无接口，发布文档必须注明该限制，不能冒称原子请求/固定账号保证。后续接入生产前必须补受控账号连续性验证/必要协议。
