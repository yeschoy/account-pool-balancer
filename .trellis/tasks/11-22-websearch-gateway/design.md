# 设计

## 结构
cmd/websearch-gateway 启动独立 net/http；internal/searchgateway 是独立服务边界；internal/websearch 定义搜索请求/结果、Tavily HTTP执行器。复用标准库，不引入第二套账号调度。第三阶段在internal/server增加显式owner-token握手/校验与原Pool内限定账号准入接线，不把搜索执行放回号池，不改变无header原流量。

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

## 第二阶段合同收敛
依据Codex0.167.0公开hosted_spec/models：显式live=true、text-only、context hint与allowed_domains可表示；域名必须传Tavily并复核来源边界，context hint仅控制既有硬上限内片段/条数。cached-only/indexed-only/精确location/preview/open/find仍不兼容。历史校验只允许省略sources和null元数据，严格保留id/status/action/query与scope，模型也纳入HMAC。实际回答里的独立真实来源URL才生成引用，字符offset，不改正文。函数done后即时分类并解除顺序屏障；仅未完成函数前出现后续item明确失败。源done与terminal.output必须一致。

## 第三阶段固定owner与接池
用已有自测-test Key向运行中的号池发合成请求，以请求ID关联日志核对真实账号/限流路径与usage；不改线上配置。新增owner接口是后续上线安全前提：authenticated GET capabilities→首轮start正常选号→AES-GCM绑定Key/hash/group/model/account-source/5min绝对期限→每轮携原token，经当前配置与选取快照source检查→同Pool.mu限定owner准入。忙/限流可等待或失败，不overflow；错误单attempt、不换号。token不发给官方/搜索/客户端、不持久历史、不续期，摘要只start/pinned。网关requirePoolOwner预检失败时零模型调用；默认示例true。线上旧版未支持，实际合成测试只验证普通粘性样本，不能冒称owner已部署。

## 已知限制与发布门禁
网关不是 OpenAI hosted search 的完整等价实现。Codex默认cached仍被明确拒绝，没有后端cache-only保证就不能承诺客户端零配置。历史内存而非跨实例共享；滚动升级/多副本需亲和或后续共享受限存储。现有线上号池普通粘性不是跨多轮原子租约；固定owner已实现并本地验证，但须生产升级后再验收。已通过固定账号+真实搜索、以及真正经在线号池的3轮合成验证，后者单样本同号/usage匹配不等于忙/错误时owner保证或NewAPI/Codex桌面验收，不能直接启用生产渠道。
