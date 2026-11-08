# Mistral 会话与高性能完整性

## 目标
完成用户六项要求：Codex/Claude 会话识别链路、套餐信息完整性、有限粘性存储、合理错误规则、请求日志上游TPS及有证据的本地性能优化。

## 已确认事实
- 线上一小时Mistral12839请求中12577为message_hmac（约98%）；header13、body235。Nginx未配置underscores_in_headers，旧式Session_id可能在到NewAPI之前被过滤，不能简单归因Balancer漏头。
- 当前Codex官方源码发session-id/thread-id；现有identity已识别两者及旧Session_id、Claude metadata.user_id的复合/JSON格式；需查双跳真实接收与其他已证实遗漏，不把request-id当会话。
- 40账号均有fresh预算，20API/20CHAT，不能称额度刷新整体未接通；API样本whoami显示Free而billing/subscription返回EDU，现实现未读取subscription。
- Pool已全局maxBindings=4096且LRU淘汰，不重复造新调度器或不必要配置。
- 双跳有界内存采样各5条：NewAPI入口3000与Balancer入口8790均无可识别身份头/正文字段，UA族为other；这批fallback不能仅归因本地漏识别，也不能凭空发明会话。
- 另一60分钟自然窗口14994请求：14969成功、8最终503且均两次尝试都失败；13次429后换号恢复成功。错误优化应针对无收益503而保留429恢复。
- 套餐只读样本：CHAT和API的subscription均返回EDU，API whoami当前显示Free；选定预算分别150USD/15USD，需区分Key访问类型与组织订阅名称。
- 当前requestLog有durationMs/ttftMs/真实usage，无独立上游TPS；网关排队和重试时间不能混入TPS分母。

## 要求与验收
1. 核对Codex/Claude稳定标识来源及入口转发，明确客户端未传/中间层丢失/本地未识别；所有新增身份继续按group+clientKey隔离，日志不含原始值。
2. 补套餐订阅信息来源、独立失败状态、持久化/API/UI往返；按API/CHAT选择预算，不把共享额度重复累计或捏造钱包。
3. 全局粘性条目不超过4096，命中刷新LRU/TTL；严格Responses缺失时fail closed，不悄悄换号。对已存在能力只补必要边界/展示。
4. 依真实错误分布调整确定性请求错误/429/503处理，保留Retry-After、不可重放边界及自定义规则，不靠盲目增加尝试数掩盖上游错误。
5. 新日志记录可解释的上游观测TPS及计时依据，页面可见；未知usage或无有效时间窗不伪造0，旧日志不回填。
6. 至少对实际Mistral热点做简短前后基准；优化不能删除思考/图片/工具、破坏流式逐帧或放松校验。仅相关定向校验与必要构建。

## 范围边界
不生产压测、不主动推理、不更换模型/账号/代理、不改付费或安全策略；不承诺消灭第三方503或让未发送身份的客户端变成100%显式会话。发布与业务验收分别说明，正式备份及Trellis保留。
