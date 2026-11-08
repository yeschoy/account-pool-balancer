# 设计

- 总耗时只在finish采样一次，同时更新durationMs与整秒useTimeSec，再基于已采集的真实outputTokens计算averageTps。现有upstreamTps不变，前端两项独立命名。
- 保留线上已审阅的X-Affinity实现/回归，补该私有头的脱敏。不得改变其实际发送值或其他池请求。
- detailLog增加可选upstreamAttempts。仅Mistral已开启详情时创建每尝试capture：最终body预览、header trace、原生response header/status、响应Reader有界tee；append收尾记录，completion统一出具客户端与各次上游数据。
- 复用captureWriter的首尾有界缓存和既有thBodyPreview/thPrivateBody/maskHeaders，不增加第二个日志存储/清理系统。上游读线程用mutex保护快照；关闭分支无capture/trace开销。
- 所有记录只在受保护的现有详情API/弹窗展示，保留原有客户端响应和旧字段。配置/凭据不入Git；生产只按用户已明确请求的Mistral详情开关启用。
- 部署复用既有Compose仅image流程，保留旧镜像/一致备份/healthcheck/两个内网别名。版本若变化先停手检查，禁止覆盖未知更新。
