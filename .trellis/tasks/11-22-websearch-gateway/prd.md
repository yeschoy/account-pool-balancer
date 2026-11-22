# 独立 WebSearch 兼容网关（第一至四阶段）

## 目标与授权
用户于2026-11-22认可独立服务、多号池/客户端复用的方案并要求「好开始」。第一阶段交付独立 Go 进程/容器示例、Responses 兼容原型、Tavily HTTP 后端、静态受控路由与本地自动化验收。后续「继续」授权第二阶段源码合同兼容与受控合成真上游验证；第三阶段在用户明确尚未接池后继续，包含实际经在线号池的隔离合成验收和本地固定owner机制实现；第四阶段用户明确授权升级Balancer、部署独立网关、新建一个隔离NewAPI测试渠道。号池原配置/既有业务渠道不变、不改NewAPI源码，不是新增号池。

## 已确认事实
- Mistral Responses 实际拒绝 hosted web_search，仅接受客户端执行的 function。原协议转发/无终态不重放契约见 `.trellis/spec/backend/mistral-responses.md`。
- 用户指定 Tavily Hikari 的 POST /api/tavily/search，Bearer 鉴权。一次 basic/max_results=1 的公开文档搜索已返回200与标题/URL/content，约2.7秒；密钥仅在会话内存读取，不能入库。
- 原 Balancer 的代理调度/限流/日志集中在 internal/server/proxy.go；第一阶段开始时其他窗口有Factory改动，已避开并保留。第三阶段在工作区干净后仅对proxy/server/Pool做固定owner的最小接线，复用原调度/限流，不覆盖其他业务修改。

## 要求与验收
- R1 独立运行：cmd/websearch-gateway 和独立 Dockerfile/Compose 示例；路由固定绑定管理员配置的 base 与上游凭据，调用方不能指定 URL/凭据目标。仅 Authorization Bearer 认证；配置只引用环境变量名，不提交真实密钥。
- R2 native/compat/disabled 三种明确模式；未带 hosted 搜索且无网关历史的流量原字节透传（JSON/SSE）。路由/模型隔离、鉴权失败零后端调用。
- R3 compat 仅 Responses、显式模型允许名单。消费 web_search/固定版本声明及对应 tool_choice/include，变为保留名函数，服务端执行搜索并回填完整函数调用与结果。支持显式live=true/text-only、域名硬限制、上下文空间提示；cached-only/indexed-only/未知语义明确400，不丢弃或偷偷live-fetch。其他 function 交回客户端，不执行终端/文件/MCP。
- R4 流式逐帧推进（不缓冲整段回答）；隐藏内部函数事件，多轮仅一个公开响应ID、全局序号和 output_index；内部 completed 不提前终止。JSON与SSE都支持正常完成、incomplete、取消和真实失败，不伪造终态/结果/用量、不重试已发送请求。
- R5 下一轮搜索历史可还原：只保存有界、短期内存中的搜索函数调用/结果，按路由+调用凭据+模型+会话隔离。允许Codex省略非权威sources/null元数据，权威id/status/action/query仍严格校验；过期/重启/跨作用域409，不重新搜索猜测历史，不保存完整提示词。只对真实来源在真实回答中实际出现的独立URL生成字符级url_citation，保留正文和已有注解，不造来源或承诺模型必引用。
- R6 每次搜索、轮数、并发、事件/响应/请求/历史缓存大小与总耗时有界；凭据不跟随重定向、不转发至非配置来源，客户端取消传播。摘要日志不含问题、结果、密钥或原始会话。
- R7 累加每轮真实模型用量；任一轮用量缺失不伪造完整总计。搜索用量独立，不混成模型token。
- R8 本地 HTTP Mock 覆盖完整两轮、混合普通函数、第二个上游、路由/会话隔离、历史过期、失败/超时/重定向、逐帧首字、截断、普通透传；normal/race/vet/build。
- R9 真正接池：仅指定自测Key/合成新会话，网关访问运行中的Mistral号池而非官方Key，按请求ID核对3轮的真实账号、尝试、状态及累计usage，不修改线上配置/渠道。
- R10 请求内固定owner：显式受控header握手、短期加密令牌按Key/hash/group/model/source隔离；每轮仍通过原Pool资格/健康/并发/RPM，不忙溢出、不失败换号、不重放；旧版能力缺失在零推理时拒绝。无header旧流量策略不变；本地实现与线上部署验收分别报告。
- R11 授权发布后仅创建私有NewAPI测试渠道、不切业务路由；核对NewAPI→常驻网关→号池的真实owner续轮、累计Token/缓存及计费记录，保留回退镜像/一致备份/旧别名。真实客户端与cached-only未验收时继续明确列为门禁。

## 边界
第一阶段不宣称真实 Codex Desktop/NewAPI 已验收，不宣称所有模型/Chat/Messages支持，不宣称打开网页/页内查找已实现。暂用受限静态配置，无管理UI/热更新。经过现有号池的多轮调用各自进入原调度器；第三阶段本地实现owner接口及全仓门禁后，第四阶段明确获授权升级Balancer/部署网关并新增私有NewAPI测试渠道260。新owner已实测start→pinned、同号、usage/计费一致；不冒称持有整个搜索期并发租约/跨外部请求永久同号。真实Codex与cached-only仍待验收/交付，不自动切现有业务渠道。
