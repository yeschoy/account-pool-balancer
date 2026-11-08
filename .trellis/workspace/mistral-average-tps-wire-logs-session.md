# 2026-11-09 — 全程平均TPS与Mistral三段日志

用户要求在原首事件后吞吐之外新增NewAPI公式的全程平均TPS，并记录客户端原始请求、上游实际请求、上游原生响应；沿用少量定向验证及自动发布授权。

NewAPI线上ad556799精确源码显示use_time采用两个Unix整秒相减；新增useTimeSec/averageTps及单次整数显示，旧upstreamTps不改，未知/旧记录不回填。Mistral详情加wireVersion/upstreamAttempts，HTTP trace补实际自动头，有界tee在适配前捕获上游响应；保留原客户端请求和适配后下游响应，逐尝试/账号关联，无响应不造数据，64KiB限长且有截断/未完整标记，关闭无捕获。

生产运行a817edf不在可获取Git对象中。对正式release363个现有运行源码SHA对照，仅mistral_request.go/proxy.go和新增affinity测试有差异；已人工审阅并保留客户端优先/最终缓存Key的X-Affinity路由修复，避免发布旧master回退。所有新增详情、Cookie/自定义token/affinity及敏感字段脱敏；响应新Cookie值必须也掩码下游预览/错误摘要，SSE日志用UseNumber保大整数。

定向normal4.547s/race6.682s、Mistral Node/JS语法、server vet/build及格式检查通过，含真实Mock原生/适配差异、429两次尝试、边界/关闭/无响应及gated首帧；没有全仓重跑或生产推理/压测/重放。

0ed42ff已提交推送并部署；496文件哈希核验、构建7.06秒，17:38:44 healthy/零重启/双别名、191账号/186TH核心资产/Mistral注册库/config原字节/运行参数保留。正式备份20261108T173746Z-mistral-wire-tps-0ed42ff与旧镜像保留。

17:40:37按用户要求仅将Mistral独立详情false→true并持久化（revision5509），核验其他有效参数、账号/密钥/管理员、全局及其他池不变，原routing/quota均已独立。17:41:36公网登录/healthz/四资源200和93a90552bf哈希通过，浏览器关闭；正常管理员会话已logout，凭据仅内存。17:41:38自然样本51条中47条平均TPS公式逐条验证、原上游TPS47条；15条三段wire详情字段/状态/字节/截断与凭据掩码通过，无生产推理/重放/压测。真实管理员弹窗未交互，UI由Node和生产API投影验证，不冒充全UI。

上线和业务验收已即时反馈；补记录不再部署。任务手动归档到.trellis/tasks/archive/2026-11/11-09-mistral-wire-logs-average-tps/（项目无task.py/add_session.py），Trellis记录全部保留提交，临时产物未经同意不清理。
