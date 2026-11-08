# 2026-11-08 — Mistral 六项会话/订阅/错误/TPS/性能

## 用户范围与授权

完整核对Codex/Claude身份、套餐信息、4096条绑定上限、错误策略、请求日志TPS及性能；沿用用户少做无用测试、改完自动上线要求。使用既有任务目录，不扩大为部署系统建设。无生产推理/压测/请求重放。

## 证据与实现

双跳各5条样本都缺身份，不把所有HMAC归因漏头；当前Codex源码使用session-id/thread-id，Nginx旧下划线仍被默认过滤。CHAT/API订阅均EDU但API whoami为Free，故新增组织订阅而保留Key访问等级。60分钟8个最终503的16次尝试全失败，13次429重试恢复，精确code3800停止换号而保留其他规则。4096上限已有，修复持久化绑定被LRU淘汰未标dirty；缺失严格所有者继续fail-closed。新增Mistral真实usage/最后调用窗口TPS，流式首事件后和非流式全调用平均明确区分。

原生SSE零分配快路径与纯文本数组跳过图片解码；转义键/多行/未知内容保守回退，消息/思考/图片/工具/大整数/协议隔离保持。局部短基准2613→134.5ns/帧、46→0alloc；128条约1KB历史801→274μs、379→104KB分配，不能冒充端到端或模型提速。

## 质量

受影响五包定向normal/race、vet、build到/dev/null、Mistral Node行为与JS语法、gofmt/diff check通过；server normal4.317s/race7.302s。详情见任务validation.md。未全仓重跑。既有工具临时产物未经同意不清理，Trellis保留。

## 待发布/验收

只在NewAPI的两个vhost放行旧下划线头，先正式备份/nginx -t再reload，检查合成新旧头到达NewAPI。应用复用既有干净快照/Compose image覆盖流程，保留完整备份、旧镜像、healthcheck及两个内网别名；发布后立即反馈，再补自然TPS/订阅只读刷新与公开资源证据。尚未宣称生产端到端或所有客户端实测。
