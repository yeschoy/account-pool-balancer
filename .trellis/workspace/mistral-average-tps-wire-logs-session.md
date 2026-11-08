# 2026-11-09 — 全程平均TPS与Mistral三段日志

用户要求在原首事件后吞吐之外新增NewAPI公式的全程平均TPS，并记录客户端原始请求、上游实际请求、上游原生响应；沿用少量定向验证及自动发布授权。

NewAPI线上ad556799精确源码显示use_time采用两个Unix整秒相减；新增useTimeSec/averageTps及单次整数显示，旧upstreamTps不改，未知/旧记录不回填。Mistral详情加wireVersion/upstreamAttempts，HTTP trace补实际自动头，有界tee在适配前捕获上游响应；保留原客户端请求和适配后下游响应，逐尝试/账号关联，无响应不造数据，64KiB限长且有截断/未完整标记，关闭无捕获。

生产运行a817edf不在可获取Git对象中。对正式release363个现有运行源码SHA对照，仅mistral_request.go/proxy.go和新增affinity测试有差异；已人工审阅并保留客户端优先/最终缓存Key的X-Affinity路由修复，避免发布旧master回退。所有新增详情、Cookie/自定义token/affinity及敏感字段脱敏；响应新Cookie值必须也掩码下游预览/错误摘要，SSE日志用UseNumber保大整数。

定向normal4.547s/race6.682s、Mistral Node/JS语法、server vet/build及格式检查通过，含真实Mock原生/适配差异、429两次尝试、边界/关闭/无响应及gated首帧；没有全仓重跑或生产推理/压测/重放。

待提交推送/发布：当前Mistral详情开关false，按用户要求仅启用该池，保留其他有效配置；复用Compose仅image、完整备份/旧镜像/healthcheck/双内网别名。自然日志和公网资源验收后立即反馈，补记录不再次部署。新任务记录在.trellis/tasks/11-09-mistral-wire-logs-average-tps/，临时产物沿用登记与同意清理约定。
