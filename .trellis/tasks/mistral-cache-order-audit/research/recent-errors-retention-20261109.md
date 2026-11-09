# 最近报错与retention兼容修复（2026-11-09 UTC）

## 授权/口径

用户要求排查近期报错、修复、只跑相关回归后部署OVH、观察完整10分钟，遇可修问题继续处理。复用现有任务与部署/观察流程，不新建调度或部署系统。真实错误原文/凭据/正文只留远端RAM，本地只保存安全分类、计数、既有内部ID。Trellis保留，临时文件登记`_tmp_artifacts.md`待同意清理。

## 上线前证据

- UTC21:04:15：运行c1aebbcc452212bb262a18d3e95528548234344b，Docker healthy/restart0。
- UTC21:05:59（Balancer完成时间ts+durationMs）最近2小时：Mistral184成功、13本地400；OpenCode Go232成功、0失败；OpenDesign515成功、1取消，另3次上游membership-concurrency事件，不能把attempt计为最终失败；TokenHarbor4成功。
- UTC21:06:36 NewAPI READ ONLY对账：180/DeepSeek恰好13条prompt_cache_retention400，与Balancer本地失败数一致。其他近期NewAPI报错包括132的3条错误、89的3条web_search错误和GPT渠道超时/EOF等，属于其他上游；不混成Mistral回归。
- 同时审阅24小时全池计数：Mistral旧reasoning别名400/图片及文本块422大部分早于已部署兼容修复；429需区分尝试和最终失败；官方503/超时不通过删参数、扫账号或延长等待伪造根治。OpenCode旧400/额度耗尽等不能冒充最近两小时仍复现。

## 根因/修复

精确DeepSeek Chat的本地prepare把所有非空retention当硬语义拒绝。客户端常携带OpenAI缓存性能提示，导致本可推理的请求在选号前400。

按本次兼容授权，消费标准24h/in_memory（含JSON转义）和既有null/空串，不转发原生Chat未知字段；保留现有缓存键、身份、完整历史、思考、工具、数字和预算。未知值/错误类型/重复字段仍本地安全拒绝。此策略**不保证Mistral按指定时长保留，也不改变服务商数据保留政策**。Responses/其他模型/协议/池不变。

官方Chat schema仅列prompt_cache_key，未列prompt_cache_retention；缓存文档明确cache key仅提高命中机会、不保证命中，未提供可配置TTL。资料：https://docs.mistral.ai/api/endpoint/chat ，https://docs.mistral.ai/inference/conversations/advanced/prompt-caching 。Responses另有此字段，故不扩大Chat的消费范围。

## 必要回归

- 干净HEAD源码副本仅覆盖本轮两个文件，隔离注册/指纹/Dockerfile未提交改动。
- 先红：新TestMistralCacheRetentionHints原实现精确返回同400，exit1，0.845秒。
- 后绿：本轮retention/参数安全与隔离、原生thought去重、reasoning和JSON/SSE适配20项相关测试通过，0.809秒；无全仓/race/Node/压测或真实推理。gofmt与diff检查通过。Docker构建承担必要发布构建，不重复无关检查。
- 新测试覆盖6类有效值×3种原缓存键原值、真实mock单attempt、缺缓存键时头链路不变、保留全部thought/history/tools/order/大整数；新增非法类型/未知字符串/重复及转义重复拒绝。

## 发布与观察

待本轮部署和600秒自然流量观察；不提前声称线上已修复或零错误。发布除本次retention外含已提交/已相关验证的原生thought+相同legacy去重（170be84），不包含任何注册/指纹/Dockerfile未提交改动。原交错块布局无损恢复及GLM188根因仍独立未完成。
