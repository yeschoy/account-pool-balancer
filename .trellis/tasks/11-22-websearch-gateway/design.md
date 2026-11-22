# 设计

## 结构
cmd/websearch-gateway 启动独立 net/http；internal/searchgateway 是独立服务边界；internal/websearch 定义搜索请求/结果、Tavily HTTP执行器。复用标准库，不引入第二套账号调度，不改 internal/server。

## 入口/配置
/r/{route}/v1/responses、/r/{route}/v1/models 与有限原生 chat/completions 透传；/healthz 不含配置。JSON配置严格解码，tokenEnv/upstreamKeyEnv/search.apiKeyEnv 只存变量名，启动解析内存；URL须无userinfo/query/fragment，HTTPS搜索后端，允许管理员显式配置HTTP内网推理Base。所有带凭据请求禁重定向、独立构造鉴权，不信任请求方Host/代理头。

## 工具循环
使用 RawMessage 保存原生字段、数字、工具和reasoning历史。compat 默认严格拒绝无法支持的 hosted 参数；保留已有函数工具，不冲突保留名。上游仍为 Responses：input 包含完整原历史+每轮原生output+搜索function_call_output。普通函数和内部搜索混合时先执行搜索，但把普通函数交还客户端，不提前向上游续轮（缺少客户端工具结果）。总搜索与模型轮数有界，无网络/HTTP自动重试。

## SSE
一个会话统一公开response ID/sequence/output_index。文本/思考逐帧流式；函数事件有界缓存到完整item，区分内部搜索与客户端函数。内部搜索变为web_search_call，实际执行时发in_progress/searching/completed。每轮仅在原生 completed/incomplete 终态后进入下一阶段；非最终轮终态不下发为整体终态。EOF不等于完成。完成输出与事件使用同一聚合列表；真实usage逐轮累计，仅全部轮次可测时返回完整总数。

## 搜索历史与来源
LRU+TTL+字节上限的内存缓存，仅存搜索call/output/public item，随机ws_ ID。作用域使用进程随机HMAC，对route、调用凭据和明确session头（不存在时对第一条原始input消息）隔离。回放web_search_call须匹配缓存与完整公开item，未知/过期409。重启明确使缓存失效。不要存整条提示词到缓存、不要将缓存键用于号池调度。引用只由真实结果URL与模型实际输出的链接产生；不保证模型一定引用。

## 失败/运维
兼容服务是新故障域，默认仅本机监听、每路由鉴权、全局并发上限、全局请求deadline。Tavily错误返回安全类别，不透出任意body/Location/密钥。流已提交时response.failed，不补completed；非流式明确HTTP错误。可切渠道回原入口绕过；当前阶段不改线上容器/网络别名。

## 已知限制与发布门禁
网关不是 OpenAI hosted search 的完整等价实现（只搜索，未实现open/find、地域/域名/preview等选项）。历史内存而非跨实例共享；滚动升级/多副本需亲和或后续共享受限存储。现有号池普通粘性不是跨多轮原子租约，服务不能凭空承诺账号不换；上线前验证并按需实现受信任的请求级固定所有者协议。不要因本地Mock通过就启用生产渠道。
