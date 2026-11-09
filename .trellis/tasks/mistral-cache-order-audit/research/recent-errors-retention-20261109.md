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

`37332be6577f9ced995d2cb7a1f960bac0c29612`已推送master并上线。发布除本次retention外含已提交/已相关验证的原生thought+相同legacy去重（170be84），不包含任何注册/指纹/Dockerfile未提交改动。614个干净提交文件哈希集合核验，旧c1 healthy/restart0、双内网入口正常；构建6.96秒，镜像就绪后干净停止旧进程、完整一致data备份/gzip/SHA256核验，沿实际Compose仅覆盖image、到期诊断变量仍为空。191账号/186TH核心资产/config完整字节/注册库/routingSecret和全部运行保护保留。

- 构建UTC21:10:14.720–21:10:21.681；停止请求21:10:21.774、旧退出21:10:21.876；备份21:10:22.142–21:10:26.811；切换请求21:10:26.811、新启动21:10:27.131；首HTTP21:10:28.252、Docker healthy21:10:57.485；NewAPI双入口21:10:57.630、本机四资源21:10:57.633。
- UTC21:12:20.013真实未登录公网浏览器登录页/healthz/四资源全部200，查询串?v=9d44d5ff6c和SHA256逐项匹配目标Git提交；回执写正式deploy.json，专用浏览器关闭。上线/入口验收后已立即反馈，不等文档提交。
- 固定观察UTC **21:11:35–21:21:35**（北京时间次日05:11:35–05:21:35），完整600秒；21:21:37.114返回window_complete，42次两服务健康采样全为37332be/healthy/restart0/无OOM、配置hash始终不变。2秒边界补采不扩大统计区间，观察进程已退出，无后台采样或主动推理。

| 号池最终完成结果 | 成功 | 失败 | 取消 |
|---|---:|---:|---:|
| Mistral | 6 | 0 | 0 |
| OpenCode Go | 35 | 0 | 0 |
| OpenDesign | 50 | 1（503） | 0 |

Mistral6条均原13次retention失败的同一下游Key、同一DeepSeek模型，6条均单attempt200，没有400/422/429/503/504。NewAPI180的1非流式+5流式成功逐条口径对齐；流式均ok/done/completed。两层input=1008716/cached=643584完全一致，4条有缓存；这不是控制TTL或缓存改善试验，也没有留存成功请求正文来证明每条仍含retention。样本仅6条，不保证所有客户端/未来请求均无错。GLM188无本窗口业务，原交错块布局无损恢复及缓存根因仍独立未完成。

## 观察中其他错误：不能隐瞒或误修

1. OpenDesign `req_7e23e60557eaac28647fd269`：21:12:43首attempt上游400 membership-concurrency，换号后200，最终成功；不是最终失败。
2. OpenDesign `req_232bb23ae205c3e9d31efb2a`：21:19:52首attempt400 membership-concurrency，第二attempt402 insufficient-balance，最终按计费规则映射503、555ms。两次均正常匹配cooldown与换号策略；实际`routing.maxAccountsPerRequest=2`，第二次后达到明确配置上限。workflow仍有其他候选，不能称全池全部余额为零或没有账号；也不能越过用户设置无限重试/开启付费来假装修复。**本项未消除**，需补可用并发/额度，或经确认调整现有尝试上限。未改线上配置、禁用账号或重启。
3. NewAPI166另有2条GPT流式EOF/failed。只读渠道key哈希/入口对账确认166以及132/89/158/162/164等均不指向Balancer、无号池Key匹配；不在本仓服务转发路径，不能靠改此号池修复或混算为Mistral失败。

正式release `/home/ubuntu/project/account-pool-balancer-releases/20261109T210951Z-37332be`；受限完整备份 `/home/ubuntu/backups/account-pool-balancer/20261109T210951Z-retention-compat-37332be/`，data SHA256 `40550a79331a681be5a2fde990d97c819773f7bb3f6f0114f5fdb6d8550c0d8c`；新镜像 `account-pool-balancer:release-37332be-20261109T210951Z`，旧镜像 `account-pool-balancer:rollback-20261109T210951Z`保留。回退先备份/确认后续写入，不自动覆盖data。旧退出至首HTTP约6.38秒仅为观测间隔，不把构建/健康等待称停机。临时目录已登记、未经同意未清理，Trellis/正式回退资料保留。
