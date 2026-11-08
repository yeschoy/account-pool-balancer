# Mistral 三段详情日志与 NewAPI 口径全程平均TPS

## 用户要求
- 保留首事件后吞吐，新增与NewAPI请求日志相同公式的全程平均TPS。
- 记录客户端原始请求头/体、每次实际发给上游的头/体、上游原始响应头/体；区分适配后的客户端响应。
- 沿用少量必要定向校验、提交推送及自动OVH发布约定；不生产推理/重放/压测。

## 契约与验收
- averageTps=真实outputTokens/(结束Unix秒-网关起始Unix秒)，同时存useTimeSec；不扣TTFT或前次重试，分层时钟不同不承诺与NewAPI数值完全相等。成功且正输出、正整秒窗口才有值，旧日志未知，不覆盖upstreamTps。
- 本次补齐当前Mistral推理路径：客户端请求/下游响应沿用原详情记录，新增upstreamAttempts按尝试索引关联原生上游交换；不改其他池的独立详情策略。
- 实际请求头包含Transport自动字段（只在详情开启时用HTTP trace观测），上游响应在适配前有界tee捕获；逐帧转发，不整段缓冲，不改变重试/输出/usage。
- 所有凭据、Cookie、自定义会话、缓存/affinity字段脱敏；正文有明确字节数/截断标记，沿用本池详情开关与64KiB上限，关闭时不做捕获。
- 流式读线程与收尾快照必须并发安全；网络失败/本地失败不伪造上游响应。前端明确分段与每次尝试/状态，旧详情兼容。
- 线上a817edf无可获取的Git对象；对正式release的363个既有运行源码逐项SHA比较，仅mistral_request.go和proxy.go有差异，并有mistral_affinity_test.go新增。已人工核对为X-Affinity保留/从最终cache key补齐，须显式保留，不能用旧master覆盖。
