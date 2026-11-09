# Mistral 容量与资源准入契约

## 作用域
`config/pool/server/stats/logstore` 及设置/概览。实现/测量见 `docs/mistral-capacity.md` 第8节；本地优化不是3亿TPM生产认证。

## 配置与往返
- `mistral.bindingCapacity: *int`：省略API字段保留；nil/0延续共享4096；1–1000000为整个Mistral池的独立普通会话LRU，不是每号会话数/并发/无限容量。
- `mistral.performance` 非nil为完整有效对象：`maxBufferedMB=2048`（1–65536）、`maxWaitingRequests=1024`（0–100000）、`uploadTimeoutSec=60`（1–3600）、`writeTimeoutSec=30`（1–600）、`detailSamplePercent=100`（0–100）。nil使用默认，API省略不清空；非法值拒绝整个保存。
- 只有 `?provider=mistral` 的设置可修改Mistral。UI显示默认和实际值，保存含Revision；重载后读文件/运行态/概览一致；连接字段仅在Mistral独立Routing中编辑，不改根或其他provider。

## 绑定与调度
- `Pool.ConfigureBindings` 仅启动和配置发布调用。禁止老请求快照回滚容量，也拒绝更旧Revision的乱序发布。独立/共享各自LRU、统一过期heap及每账号计数；变更容量不终止lease。
- 从独立切回共享时先裁剪Mistral偏好，不用这些偏好挤出已有其他池严格owner。Go可选cache只能淘汰cache。
- Mistral已有绑定先校验owner/source/目录/健康/并发/RPM。只有owner可用时短路；忙、overflow、配置/来源变化回退原选号语义，不从所有者快路径绕过安全检查。快路径Candidates=1表示实际检查一号，不是假称扫描全池。
- 未发送普通绑定不checkpoint；同一provisional偏好的任一真正发送者都可确认，只有创建者可撤销未确认偏好。迟到回调必须校验generation，不能复活已淘汰绑定。
- 分组等待上限及定向唤醒；账号换组后旧lease释放也唤醒当前已发布组，但不唤醒无关组。队列满/禁止排队时需要排队的请求本地429，不靠borrow绕过队列准入。`pool.Request.MaxWaiters=0`对旧调用方保持不增加限制；server把Mistral配置的0映射为禁止排队。
- 延续原MarkSent、source校验、绝对TTL、普通绑定采样合并和退出强刷；不恢复活跃/RPM预约；Mistral Responses仍无状态，不新增previous_response_id服务端续传。

## 不可变快照与统计
- Normalize最后创建账号/组/下游Key/native-source索引；Normalize开始及Store.Update调用可变callback前清除旧索引。请求只读发布快照。Clone不共享可变索引map。
- 健康按分钟缓存聚合；Sample/Reset/Import/Invalidate使缓存失效。
- Stats按观测分钟稀疏分配，单provider账号overall/provider共享ring，同ID跨provider必须拆分；Import可重连相等投影。rolling24h、known/unknown、reset-cache-rate、所有原用量/费用/持久化格式不变。
- 聚合total缓存与稀疏slot同步更新；过期回收、同分钟重置后再记账、冲突provider和旧metadata必须测试。

## 内存、网络与日志
- Mistral输入原字节、重写副本、非流响应和队列内SSE原事件共用原子字节预算；配置/当前发布值取较小值并收紧到Go软内存限额的1/3。这不是RSS硬限制或对整个JSON对象树/运行时的内存认证。
- 上传使用读deadline；超时后关闭HTTP/1复用且不在错误清理中清掉超时deadline，避免net/http为排空未读body再次阻塞。慢客户端逐次Write/Flush续期deadline，不使用全局短WriteTimeout杀长流。
- 输入/改写准入失败在零上游尝试时返回429。上游响应/事件超预算明确失败；流已提交时可报SSE错误，不补DONE/usage，不重放；本地预算/客户端故障不能扣账号健康。
- SSE事件在消费或取消后释放。relay退出先关done/取消上游/关body，等读goroutine退出，再清队列；所有路径预算归零。
- Mistral Transport按固定proxy及独立连接策略分区，采用当前已发布配置防旧请求倒换；只清旧idle连接，不中断活跃请求；绝不代理失败回退直连。HTTP2连接数不等于并发流数。
- JSON保留完整严格校验和原生wire/大数/顺序/未知字段。仅身份fallback确实需要时解码历史；不用路由投影重编码请求。复用最终cache key；保留客户端X-Affinity优先。
- 详情按request-ID确定性采样，0不采正文，100保持原开关效果，客户端/上游共用一次决定；摘要、既有记录、独立cacheAudit不受此采样删除。脱敏缓存按不可变账号切片+Revision，而非临时ForProvider视图指针。
- 日志异步队列保留8192条上限并增加默认64MiB JSON行字节预算；超限不阻塞、计dropped，写出或失败后归还。摘要/详情内存环仍有各自上限，队列预算不是全日志/RSS上限。概览显示queued bytes/条数/dropped与Mistral字节用量，独立和共享绑定占用不能混淆。

## 可执行门禁
`config/mistral_capacity_test.go`、`pool/capacity_test.go`、`server/mistral_capacity_test.go`、`mistral_scan_test.go`、`stats/capacity_test.go`、`logstore/queue_budget_test.go`、`mistral_capacity_web_test.js`；保留既有完整协议、busy/overflow、严格owner、TTL、cache-rate、日志保留测试。全仓normal/race/vet/build、Node、零账号本地浏览器保存/刷新/进程重启读回与跨池隔离。

禁止：以合成usage、短闭环benchmark、单函数提速、连接数或更大LRU宣称3亿TPM。真实验收需明确token/RPS/持流/正文口径、组织授权、全链路资源与费用上限，另行授权生产测试/部署。
