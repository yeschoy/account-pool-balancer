# 2026-11-08 — Mistral 六项会话/订阅/错误/TPS/性能

## 用户范围与授权

完整核对Codex/Claude身份、套餐信息、4096条绑定上限、错误策略、请求日志TPS及性能；沿用用户少做无用测试、改完自动上线要求。使用既有任务目录，不扩大为部署系统建设。无生产推理/压测/请求重放。

## 证据与实现

双跳各5条样本都缺身份，不把所有HMAC归因漏头；当前Codex源码使用session-id/thread-id，Nginx旧下划线仍被默认过滤。CHAT/API订阅均EDU但API whoami为Free，故新增组织订阅而保留Key访问等级。60分钟8个最终503的16次尝试全失败，13次429重试恢复，精确code3800停止换号而保留其他规则。4096上限已有，修复持久化绑定被LRU淘汰未标dirty；缺失严格所有者继续fail-closed。新增Mistral真实usage/最后调用窗口TPS，流式首事件后和非流式全调用平均明确区分。

原生SSE零分配快路径与纯文本数组跳过图片解码；转义键/多行/未知内容保守回退，消息/思考/图片/工具/大整数/协议隔离保持。局部短基准2613→134.5ns/帧、46→0alloc；128条约1KB历史801→274μs、379→104KB分配，不能冒充端到端或模型提速。

## 质量

受影响五包定向normal/race、vet、build到/dev/null、Mistral Node行为与JS语法、gofmt/diff check通过；server normal4.317s/race7.302s。详情见任务validation.md。未全仓重跑。既有工具临时产物未经同意不清理，Trellis保留。

## 正式发布/验收

77e9709已推送并上线。NewAPI两个vhost仅放行旧下划线头，备份20261108T152932Z-newapi-session-headers，nginx -t后只reload一次；首次宽探针不完整，先确认状态再用窄GET探针证明Session_id与Session-Id均达NewAPI。应用487文件哈希一致，构建7.19秒，完整一致备份20261108T153233Z-mistral-session-perf-77e9709；15:33:36 healthy/零重启/双别名，191账号/186TH核心资产/config字节/Mistral注册库与运行参数保留。15:36:04公网登录/healthz/四资源哈希通过，浏览器关闭。

CHAT/API各一把真实刷新成功，40预算新鲜；15:38:07 metadata确认全部40把Key订阅成功持久化（Student6/Pro34、零错误），不能累加共享组织预算。概览global225/currentMistral209/limit4096，两条服务错误规则生效。自然80条均有TPS（76流式/4非流式），也全部message_hmac：没有传身份的请求不能凭空恢复；不能声称全客户端E2E或fallback比例下降。新UI通过Node与生产API投影，不冒充真实生产管理员浏览器保存往返。密码/会话仅内存，已logout；无生产推理/重放/压测。

部署全记录见docs/ovh-deployment.md。本任务手动归档到.trellis/tasks/archive/2026-11/11-08-mistral-session-performance/（本项目无可用task.py/add_session.py），保留全部Trellis记录并提交；旧临时产物未经同意不清理。补记录不重建或重启。
