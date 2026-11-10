# OVH 部署与回滚记录

## 后续部署执行约定（用户确认，2026-11-04）

上次整体交付约 20 分钟，但实际构建约 8 秒，离线预检约 2 秒，切换至确认
健康约 33 秒。主要可避免耗时是现场编写多套脚本、重复检查、资源路径试错
以及上线后的文档收尾。不能把这些时间归咎于 OVH 或 Docker 性能。

后续按以下固定顺序执行，不重新展开一套部署系统设计：

1. **一次预检**：合并检查目标容器、运行 revision、工作树/分叉、数据挂载、
   磁盘和健康。先读本文件，已知的缓存历史不要重复做全仓大输出；用限定
   路径或汇总差异确认。若已是目标版本且健康/资源匹配，直接报告无需部署。
2. **构建**：从明确提交的干净快照构建固定标签镜像并设置 revision；保留旧
   镜像。不重复跑同一提交已经通过且不受部署环境影响的完整测试。首次升级
   或有数据兼容风险时保留断网副本启动预检；普通更新不无故复制敏感数据。
   预检容器必须复用线上 `Config.User`（当前1000:1000），不能假定镜像默认
   用户可读原权限副本。注册库核对按 `server.go` 将 routingSecret 作hex解码，
   不猜测成base64；解密仅在远端内存完成。
3. **备份切换**：在新镜像就绪后才进入停机窗口。按变更风险保留一致、完整
   的数据备份；只更新目标服务，保持卷/端口/网络、配置和功能开关不变。
   不自动用旧数据覆盖上线后的新写入，失败时先确认结果再执行既定回退。
4. **一次验收**：合并核对健康、运行镜像 revision、必要的数据/密钥/设置
   保留与新版资源。**必须核对 `ai-internal` 的网络别名，并从 `new-api`
   容器分别请求 `http://account-pool-balancer:8790/healthz` 和
   `http://opendesign-balancer:8790/healthz`，两者都须成功。**宿主机/公网
   healthz=200 不能证明渠道入口可达；保留同名网络但丢掉别名也会造成全部渠道
   DNS 失败。以线上 Compose 为基础只覆盖 image，不能用遗漏别名/healthcheck/
   资源参数的裸 `docker run` 代替；核验 `Config.Healthcheck`，没有健康检查时
   不得宣称 Docker healthy。本地检查走既有接口；静态脚本实际路径是
   `/assets/app.js`，优先从 HTML 读取，不猜 `/app.js`。资源URL带`?v=`时，
   HTTP请求保留查询串，映射源码文件时使用URL解析后的path。公网 Python 客户端
   的既有 403 不应误判为新版本故障；不改 WAF、不伪装身份或轮换出口，
   用正常真实浏览器校验可访问页面，遇到挑战如实报告而非反复尝试。

**反馈与时间预算**：缓存命中、无异常的单服务部署以 2–5 分钟为目标，
不是硬性 SLA；超过 5 分钟主动说明具体卡点。构建、停机切换、健康确认、
公网验收分别记录时间。必要验收通过立即告知用户，再补最小部署记录，
不要让写文档或提交耗时变成用户等待上线结果的时间。只改记录不重新部署。

## 当前部署：Mistral1910模型资格与3505容量精确策略（2026-11-10 09:42 UTC，f1024e9）

- 用户明确要求提交推送、部署OVH，避免无用操作。`f1024e948b31bb3a51dbe7ae28d4ec8706966e62`已推送master并上线；复用已通过的相关normal/race/vet/build，不重复测试、不发生产推理。未提交/打包其他窗口注册、指纹或Dockerfile未提交修改。
- 一次预检确认旧37332be healthy/零重启、双内网入口正常、245.02GiB余量，旧工作树干净但不是构建源。620个干净提交文件SHA256集合核验；复用既有发布控制器，仅内存替换版本/备份名，无新增部署系统。新镜像就绪后干净停止旧进程，完整一致data备份/gzip/SHA256核验，沿线上Compose仅覆盖image，已到期诊断变量继续为空。
- **191账号、186TH核心注册记录、routingSecret、Mistral注册库及用户/环境/卷/端口/网络/资源/Healthcheck保留。**切换阶段config完整字节未变。Docker healthy/restart0/无OOM，ai-internal新旧别名及NewAPI到两个healthz、四本机静态资源均通过，立即反馈上线。
- UTC09:41:07通过正常管理员API仅在Mistral既有规则前增加两条精确错误策略和3505重试stop，revision5672；403/code1910仅模型冷却24h、原尝试上限内换号/最终503、不扣全局健康，429/code3505停止换号、不扣健康、保留429/原Retry-After。完整旧自定义列表原顺序保留，**其余配置/凭据逐字段相等，原13个硬隔离账号未恢复**，GET与落盘逐项一致，管理员正常logout。不把授权设置修改说成部署后config始终字节不变。
- UTC09:42:15.926真实未登录公网浏览器登录页、healthz及四资源均200，`?v=9d44d5ff6c`与四项SHA256逐项匹配f1024e9；回执入正式deploy.json，专用浏览器已关闭。UTC09:43:01最终核对同版本healthy/0重启/无OOM、旧隔离保留，已立即反馈验收。短窗口未出现1910/3505新自然事件，不冒充真实错误重放验收；**不宣称GLM缓存已改善**，未新开采样或追加10分钟等待。

| 阶段 | UTC时间 |
|---|---|
| 构建 | 09:38:05.417–09:38:12.511（7.09秒） |
| 停止请求 / 旧退出 | 09:38:12.608 / 09:38:12.704 |
| 完整一致备份核验 | 09:38:12.983–09:38:18.619 |
| 切换请求 / 新启动 | 09:38:18.619 / 09:38:18.917 |
| 首HTTP / Docker healthy | 09:38:20.033 / 09:38:49.268 |
| 双入口 / 本机验收 | 09:38:49.414 / 09:38:49.418 |
| 规则保存 / 公网验收 | 09:41:07.309 / 09:42:15.926 |

正式源码`/home/ubuntu/project/account-pool-balancer-releases/20261110T093736Z-f1024e9`；镜像`account-pool-balancer:release-f1024e9-20261110T093736Z`（ID`sha256:239708582af8ccb7c68b267b8d260bb05d7cc265d56b094c74937679852f03c1`）。完整受限备份`/home/ubuntu/backups/account-pool-balancer/20261110T093736Z-tier-capacity-f1024e9/`，data SHA256`218ac904aee14eaa8f09a6f6063fb6f2b6f2fa13cb9abf9a343ec9394537d264`，旧镜像`account-pool-balancer:rollback-20261110T093736Z`保留。回退先确认新写入及新策略与旧代码行为，不自动覆盖data或恢复已隔离账号。旧退出至首HTTP约7.33秒为观测间隔、非精确停机；构建/健康等待不算停机。临时回执登记`_tmp_artifacts.md`，经同意再清理，正式回退资料/Trellis保留；仅补记录不再重启。

## 历史部署：Mistral缓存保留提示兼容（2026-11-09 21:12 UTC，37332be）

- 用户要求排查近期报错、修复后只做相关回归、部署OVH并完整观察10分钟，遇可修问题继续处理。`37332be6577f9ced995d2cb7a1f960bac0c29612`已提交推送master并上线。最近2小时Mistral13条最终400与NewAPI180的13条`prompt_cache_retention`错误对齐；精确DeepSeek Chat现消费标准`24h`/`in_memory`缓存性能提示（含转义），不误拒绝。**原缓存键、身份、完整历史/思考/工具/数字/预算保留；不承诺Mistral实际TTL或数据保留政策。**未知/非法/重复值仍拒绝，其他模型/Responses/协议/池不变。
- 同时发布已提交并通过相关回归的`170be84`原生thinking+相同legacy去重，避免重复插入思考；不声称解决交错块无损布局或原188缓存根因。仅20项相关Go回归0.809秒，旧代码精确红0.845秒；无全仓/race/Node/生产推理/重放。Docker完成必要构建；未混入其他窗口注册/指纹/Dockerfile未提交改动。
- 一次预检确认旧c1 healthy/零重启、双入口正常、245.55GiB余量、旧e6工作树干净但不作为构建源码。614个干净提交文件SHA256集合核验，复用既有部署控制器，无新增部署系统。无存储迁移，不多复制敏感数据；新镜像就绪后干净停旧进程，完整一致data备份/gzip/SHA256核验，沿线上Compose仅覆盖image，过期诊断变量继续为空。
- **191账号、186TH核心注册记录、routingSecret、config.json完整字节、Mistral注册库及用户/卷/端口/环境/网络/资源/Healthcheck保留。**Docker healthy/restart0/无OOM、ai-internal新旧别名、NewAPI分别请求两个healthz、本机四资源均通过，已立即反馈上线。
- UTC21:12:20.013真实未登录公网浏览器登录页/healthz/四资源全部200，`?v=9d44d5ff6c`和四项SHA256逐项匹配37332be；回执写正式deploy.json，专用浏览器已关闭。仅部署/入口/资源验收，不冒充真实失败正文重放或特定TTL验证。
- **UTC21:11:35–21:21:35完整600秒自然观察完成**，21:21:37.114返回结束、进程已退出；42次两服务采样均同版本healthy/restart0/无OOM，config hash始终不变。Mistral原失败Key6次全部单attempt200、0失败/取消，无400/422/超时；NewAPI180的1非流式+5流式成功及usage逐项对齐。OpenCode Go35成功无失败；OpenDesign50成功、1最终503（先上游并发满400、后换号余额拒绝402，触及用户配置最多2账号），另1次并发事件已恢复成功，不能报全池零错误。NewAPI166的2次GPT流式失败确认不经过Balancer。**OpenDesign资源/尝试上限问题未消除，未擅自调大重试、开启付费或覆盖配置。**样本6条不等于全部客户端/TTL保证，原188无业务，不冒称缓存根因解决。详细证据见任务research/recent-errors-retention-20261109.md；仅补记录不再次部署。

| 阶段 | UTC时间 |
|---|---|
| 构建 | 21:10:14.720–21:10:21.681（6.96秒） |
| 停止请求 / 旧退出 | 21:10:21.774 / 21:10:21.876 |
| 完整一致备份核验 | 21:10:22.142–21:10:26.811 |
| 切换请求 / 新启动 | 21:10:26.811 / 21:10:27.131 |
| 首HTTP / Docker healthy | 21:10:28.252 / 21:10:57.485 |
| 双入口 / 本机验收 / 公网验收 | 21:10:57.630 / 21:10:57.633 / 21:12:20.013 |

正式源码`/home/ubuntu/project/account-pool-balancer-releases/20261109T210951Z-37332be`；镜像`account-pool-balancer:release-37332be-20261109T210951Z`（ID`sha256:b61e5cb4c36b044d33e328066fded6c3a2a3df9ce256bceeab3735d58003682f`）；完整受限备份`/home/ubuntu/backups/account-pool-balancer/20261109T210951Z-retention-compat-37332be/`，data SHA256`40550a79331a681be5a2fde990d97c819773f7bb3f6f0114f5fdb6d8550c0d8c`，旧镜像`account-pool-balancer:rollback-20261109T210951Z`保留。回退先确认新写入，不自动覆盖data。旧退出至首HTTP约6.38秒为观测间隔、非精确停机，不把构建/健康等待算停机。临时产物登记`_tmp_artifacts.md`待同意清理，正式回退资料/Trellis保留；补记录不重建/重启。

## 历史部署：Mistral容量优化与独立会话容量（2026-11-09 13:07 UTC，c1aebbc）

- 用户授权清理、提交推送部署，要求少做无用测试。容量业务`1c0a410`及验证记录`c1aebbcc452212bb262a18d3e95528548234344b`已推送master并上线。相对实际在线`72d1e42`，协议修复字节相同，新增路径严格等于本次容量提交；未带入其他窗口未提交的注册/指纹/Dockerfile改动。
- 复用已通过的全仓normal/race/vet/build、Node、本地浏览器和干净commit定向回归，本轮不重复测试、不发真实推理。一次预检确认旧72d1e42 healthy/零重启、191账号、双入口正常、246.1GiB可用；旧e6f063b工作树未构建或重置。603个干净发布文件SHA256集合核验，复用既有控制器仅内存适配，无新本地脚本/归档或敏感副本。
- 新版支持独立会话LRU、索引/owner/健康/等待、字节预算/上传与流写deadline、独立连接池、JSON/SSE/脱敏、稀疏统计/日志队列优化。**线上容量字段仍省略，保留共享4096，不擅改路由/详情；用户可在Mistral路由设置选择1–1000000独立容量。**未认证真实3亿TPM。
- 存储导出格式不变，复用已验证兼容性。镜像就绪后干净停旧进程、完整一致data备份/gzip/SHA256核验，沿线上Compose覆盖image，过期诊断变量保持空，绝不重启续采。**191账号、186TH核心注册记录、routingSecret、完整config.json字节、Mistral注册库、原用户/卷/端口/资源/网络/Healthcheck保留**。
- Docker healthy/restart0、ai-internal新旧别名、NewAPI到两个healthz及本机四资源通过；13:07:31.965 UTC真实未登录公网浏览器登录页/healthz/四资源均200，`?v=9d44d5ff6c`和SHA256逐项匹配c1aebbc。已立即反馈上线/验收，专属浏览器关闭，公网回执入正式deploy.json；补记录不重启。这不是生产管理台保存或真实容量压测。
- 用户同意后定向清理两干净工作树、合成基准/profile/构建/假data及6个指定工具文件，约70.2MiB；Trellis、其他窗口产物、Pi托管执行审计、新旧镜像和正式备份均保留。

| 阶段 | UTC时间 |
|---|---|
| 构建 | 13:05:59.654–13:06:06.700（7.05秒） |
| 停止请求 / 旧进程退出 | 13:06:06.810 / 13:06:08.061 |
| 完整一致备份核验 | 13:06:08.333–13:06:13.530 |
| 切换请求 / 新容器启动 | 13:06:13.530 / 13:06:13.765 |
| 首HTTP / Docker healthy | 13:06:14.875 / 13:06:44.110 |
| 双入口 / 本机验收 / 公网验收 | 13:06:44.265 / 13:06:44.268 / 13:07:31.965 |

正式源码`/home/ubuntu/project/account-pool-balancer-releases/20261109T130529Z-c1aebbc`；镜像`account-pool-balancer:release-c1aebbc-20261109T130529Z`（ID`sha256:b0f664ea2c2e80aec19f75afa3d7ee708606804656be84e5d462253d04038332`）。完整受限备份`/home/ubuntu/backups/account-pool-balancer/20261109T130529Z-mistral-capacity-c1aebbc/`含data、原Compose/.env、构建/切换日志及deploy.json，data SHA256`6756319ccddf4ba6d21b227fa2d2dcf229062f98166839758ce230e7307d8501`；旧镜像`account-pool-balancer:rollback-20261109T130529Z`保留。回退先确认新写入，不自动覆盖data。旧退出至首HTTP观测约6.81秒，非精确停机，不把构建/健康等待算停机。无新增待清理本地部署产物，正式备份/Trellis保留。

## 上线后完整10分钟只读观察（2026-11-09 13:14–13:24 UTC，c1aebbc）

用户明确要求观察10min；实际固定窗口UTC13:14:12–13:24:12（北京时间21:14:12–21:24:12），600秒，边界补采不扩大窗口。以当前实际c1aebbc为基线（其请求适配字节与72d1e42相同），43次运行采样均为同一revision、两服务healthy/restart0/无OOM。期间config文件hash有外部/后台写入，未保存原文、不能定位具体字段；本轮无任何配置/功能开关写入，不冒称固定配置实验。

Mistral按完成时刻共93条：**88成功200、1超时504、4客户端取消499；无400/422/最终429/503**。唯一504请求UTC13:07:26在窗口前发起，13:17:26达到600秒响应等待上限，单attempt、无上游HTTP状态/TTFT/usage，未重放。窗口内1次429事件关联最终200、2attempt，不能当最终失败；request/error两行同一504不算两次失败。NewAPI180/182共享Key，36+52成功、4取消、1错误逐项对齐，88成功流均ok/done。GLM188/185/187及183本窗口无流量，不能据此宣称GLM缓存已修复。

其他池仍有错误：OpenCode Go37条最终400（另38成功）、OpenDesign2条HTTP200但业务failed（另48成功）；NewAPI另有渠道71的12条503和162的一条流式失败，均单列不混成Mistral回归。本轮只读自然流量、不主动推理/压测、不改代码/配置/重启，观察已正常结束。详细口径、时间与请求关联见`.trellis/tasks/mistral-cache-order-audit/research/observe-10min-20261109.md`；补记录不触发新部署。

## 历史部署：Mistral思考空白占位与DeepSeek文本块兼容（2026-11-09 13:00 UTC，72d1e42）

- 用户提供精确400并连续要求继续、急需，沿其此前“只做必要回归后直接部署”授权，先上线`b5d0dbb`修复空白占位误判，再上线独立发布提交`72d1e42114f89238d972f53f07899de731622aef`补齐已证实的两个422格式问题。**真实思考/文字/图片/工具/历史不删除**：三精确推理模型的Chat双思考字段仅忽略空白占位，真正非空冲突仍拒绝；DeepSeek Chat补type-only助手空文本槽的text=""，只消费标准单字段ephemeral提示，TTL/未知结构保留原生、不承诺Anthropic分块语义。没有修原GLM188缓存掉零、原生web_search或官方503。
- `b5d0dbb`及主线业务提交`fbee8e9`已推送。其他窗口在主线并发提交容量改造，检查发现相对线上多出35个业务路径后**没有直接部署主线HEAD**；用独立Git index从b5d0dbb仅合入本轮5文件补丁，生成并推送`release/mistral-text-compat-20261109`分支72d1e42。其相对b5d0dbb业务差异仅mistral_request.go和两个文本块实现/测试文件；未部署容量/注册/Dockerfile改动，未重置/改写主线。
- 红/绿回归均在干净提交源码副本：空白占位旧实现精确失败后相关回归0.936秒通过；文本块旧实现失败后相关Reasoning/Image/Cache/Prefix/Text回归1.764秒通过。无全仓/race/压测或主动生产推理、私密历史重放。573/576个发布文件逐项SHA256集合核验；首轮只读校验SSH超时后确认服务仍旧版healthy且未构建/停服，复用同一源码目录、紧凑哈希集合校验继续，不盲目重复发布。
- 两次均镜像就绪后干净停进程、一致完整备份data并gzip/SHA256核验，沿线上Compose仅覆盖image，显式将`ODB_MISTRAL_CACHE_AUDIT_KEY_ID`设空；旧诊断不因重启续采。**191账号、186TH核心注册记录、routingSecret、完整config.json字节、Mistral注册库与原用户/环境（除首次清空诊断变量）/卷/端口/资源/网络/Healthcheck全部保留**；healthy/restart0、ai-internal新旧别名、NewAPI到两个healthz、四本机静态资源均验收通过，随即告知用户。
- 12:20:47.871/13:00:13.510 UTC正常真实未登录公网浏览器登录页/healthz/四资源均200，`?v=8f01a8b1d7`与各发布SHA256匹配，回执写各正式备份deploy.json；两个专用浏览器均关闭。首轮公网open曾超时，后按可见页面状态确认恢复，不改WAF/出口，不重建服务。
- 只读自然流量边界：b5d0dbb上线后截至12:20:19目标渠道180完成121条（102成功200、16上游422、2官方503、1客户端499），无400，422引出第二个补丁。72d1e42上线后截至12:59:50首批完成6条均200，其中1条经历原有429恢复；**不是完整10分钟观察，也未逐条证明原失败私密payload已再次经过**。不把新短窗口或DeepSeek缓存读数当GLM188缓存改善。未主动推理、追加重试、启用正文详情或部署后续容量改造。

| 阶段（UTC） | b5d0dbb 空白占位 | 72d1e42 文本块兼容 |
|---|---|---|
| 构建 | 12:09:36.625–12:09:43.703（7.08秒） | 12:58:34.308–12:58:41.617（7.31秒） |
| 停止请求 / 旧退出 | 12:09:43.811 / 12:09:43.917 | 12:58:41.724 / 12:58:42.987 |
| 完整一致备份 | 12:09:44.167–12:09:49.808 | 12:58:43.342–12:58:48.593 |
| 切换请求 / 新启动 | 12:09:49.808 / 12:09:50.100 | 12:58:48.593 / 12:58:48.845 |
| 首HTTP / Docker healthy | 12:09:51.226 / 12:10:20.459 | 12:58:49.950 / 12:59:19.179 |
| 双入口 / 本机资源 | 12:10:20.608 / 12:10:20.612 | 12:59:19.337 / 12:59:19.340 |
| 公网验收 | 12:20:47.871 | 13:00:13.510 |

正式源码分别位于`/home/ubuntu/project/account-pool-balancer-releases/20261109T120244Z-b5d0dbb`和`20261109T125802Z-72d1e42`；镜像分别为`account-pool-balancer:release-b5d0dbb-20261109T120244Z`（ID`sha256:a9a637cb98e6a71864ef327fb429ea8ca635de572d462d057111d887a8729e71`）和`account-pool-balancer:release-72d1e42-20261109T125802Z`（ID`sha256:b2aa6054e0c35488e655213290e3b1d15eca517da0f716db8dcd5b93cf10df69`）。正式受限备份`/home/ubuntu/backups/account-pool-balancer/20261109T120244Z-reasoning-placeholder-b5d0dbb/`与`20261109T125802Z-text-compat-72d1e42/`含完整data、原Compose/.env、构建/切换日志和含公网回执的deploy.json；data SHA256分别`3af94d6e7ca71da13b0c616b3342543f613d7534e69c05c850a6c93814935957`、`367af6b63d565ff06812d421ef448c51ad227a49046efccaa9a5867b3c03702d`。旧镜像`account-pool-balancer:rollback-20261109T120244Z`及`rollback-20261109T125802Z`均保留；回退先确认新写入，不覆盖data。旧退出至首HTTP观测分别约7.31/6.96秒，非精确停机，不将等待用户/构建/健康/公网检查算停机。临时产物登记`_tmp_artifacts.md`待用户同意清理，正式备份/Trellis保留；后续只补记录不重新部署。

## 历史部署：Mistral渠道188 GLM5.3追踪与限时缓存取证（2026-11-09 08:22 UTC，480dd2d）

- 用户要求深查缓存、参考Mistral接口文档、少跑无关测试并直接部署。`480dd2d17c27cd44c83b2a3c30083d97da1d5fe6`已推送master并上线，含此前已提交的`bdb2a65`服务商trace修复、`7c5e30b`完整出站指纹诊断。**这是诊断上线，不是缓存低命中已根治。**未包含其他窗口未提交的注册/指纹/Dockerfile改动。
- 已确认渠道188将glm-5.3/flash分别映射Mistral的zai-glm-5-3/flash，截图实际Flash。14条同会话/同账号、无重试换号，缓存95.6%→0是真实上报，两层统计一致；历史正文未留存，不伪造相同前缀证明。详见`.trellis/tasks/mistral-cache-order-audit/research/channel188-20261109.md`。
- 仅跑一次相关Go定向回归（2.718秒），复用此前全仓/Node/本地浏览器结果，不重复测试、不主动推理或重放。565个干净提交文件SHA256逐项核验，复用既有部署脚本仅内存适配；镜像就绪后干净停进程、完整一致备份/gzip/SHA256核验，沿线上Compose仅覆盖image及下述明确诊断变量。
- **191账号、186条TH核心注册记录、routingSecret、完整config.json字节、Mistral注册库及用户/原环境/卷/端口/资源/网络/Healthcheck全部保留**。唯一新增运行变量`ODB_MISTRAL_CACHE_AUDIT_KEY_ID=ck_5295bcdb9c215fffaf6e5f59`，仅渠道188的现有Key；每进程30分钟/128请求上限，正文详情仍关闭，只记有界HMAC/结构/连接/trace，不采其他Key。原始环境外新增该变量是有意变更，不能声称环境字节完全不变。
- Docker healthy/零重启且有Healthcheck；ai-internal新旧别名、NewAPI到两个healthz均通过，四资源本机及真实未登录公网浏览器SHA256逐项匹配，`?v=8f01a8b1d7`保留。验收通过立即反馈，专用浏览器已关闭；补记录不重建/重启。
- UTC08:23:11–08:24:41的90秒有界自然观察无目标新请求，采样进程正常退出、主动推理0。不能宣称已取得新真实出站样本或缓存改善。诊断采样窗口约至08:51:01（以启动/128请求先到为准）；**后续普通部署应在最新Compose覆盖中显式将该环境变量设空，不得因旧override继承而无授权重启续采。**到期不会主动重启或删除日志。

| 阶段 | UTC时间 |
|---|---|
| 构建 | 08:20:47.120–08:20:54.376（7.26秒） |
| 停止请求 / 旧进程退出 | 08:20:54.483 / 08:20:54.586 |
| 完整一致备份核验 | 08:20:54.860–08:21:00.882 |
| 切换请求 / 新容器启动 | 08:21:00.883 / 08:21:01.186 |
| 首HTTP可达 / Docker healthy | 08:21:02.297 / 08:21:31.536 |
| 双内网入口 / 本机资源验收 | 08:21:31.686 / 08:21:31.689 |
| 公网验收 | 08:22:14.756 |

正式源码`/home/ubuntu/project/account-pool-balancer-releases/20261109T082030Z-480dd2d`；镜像`account-pool-balancer:release-480dd2d-20261109T082030Z`（ID`sha256:8f117927111949308b52c4dbba90a4b17022d837371b9964bb6cb6c2313696ab`）。受限完整备份`/home/ubuntu/backups/account-pool-balancer/20261109T082030Z-glm188-cache-480dd2d/`含data、原Compose/.env、构建/切换日志及带公网回执的deploy.json，data SHA256`1e90eec6c3812401ef62f5e7de9ba7b669f81b268c8c908e42ed2a9246c01d4e`。旧镜像`account-pool-balancer:rollback-20261109T082030Z`保留；回退先确认新写入并移除新增诊断变量，不自动覆盖data。未采样精确停机，旧退出至首HTTP观测约7.71秒，不将构建/健康等待算停机。临时产物登记`_tmp_artifacts.md`，待用户同意清理；正式备份/Trellis保留。

## 历史部署：账号缓存率、清空按钮与会话头原值展示（2026-11-08 21:13 UTC，a6d7d38）

- 用户明确授权提交推送部署并要求少做无用测试；`a6d7d38fedae48b01f6b03cf073f5a402146c29f`已非强制推送master并上线，包含`f9c3692`逐账号24h缓存率/当前池命中率样本清空及`a6d7d38`管理员客户端会话/缓存头原值展示。认证Key/Cookie仍隐藏，旧REDACTED不可恢复；正文/上游详情/错误日志及其他池原有隐私边界不变。未提交或部署其他窗口的Dockerfile/注册/指纹改动。
- 复用已通过的全仓normal/race/vet/build、全部Node与本地假数据浏览器结果，不重复测试、不发生产推理、不清空线上统计。一次预检确认旧be14987 healthy/restart0、191账号/186条TH注册记录、TH/Mistral任务空闲、247.3GiB余量、NewAPI双入口正常；旧e6f063b工作树干净但不是构建源，未构建/重置。
- 528个干净提交文件逐项SHA256核验；复用现有部署脚本仅内存适配，无新临时部署脚本/归档/敏感副本。存储格式不变，不另复制凭据做离线预检；镜像就绪后干净停旧进程、完整一致备份/gzip/SHA256核验，沿线上Compose仅追加image覆盖。
- **191账号、186条TH核心注册记录、routingSecret、完整config.json字节、Mistral注册库、环境/用户/挂载/端口/资源/网络/Healthcheck均保留**。新容器Docker healthy、零重启，ai-internal保留新旧兼容别名，从new-api分别请求两个healthz成功，四项本机资源SHA256匹配。
- 21:13:27.815 UTC真实未登录公网浏览器确认登录页正常，healthz与四静态资源均200，`?v=b94cdbf8de`及四项SHA256与发布源码匹配；服务验收通过立即反馈，专用浏览器关闭，公网回执补入正式备份deploy.json。**验收是部署/配置与资产/入口/资源核验，不是生产管理台清空操作或真实上游命中率对照**；未擅自开启功能/修改配置。后续仅补记录，不再重建/重启。

| 阶段 | UTC时间 |
|---|---|
| 构建 | 21:12:09.186–21:12:16.016（6.83秒） |
| 停止请求 / 旧进程退出 | 21:12:16.129 / 21:12:16.257 |
| 完整一致备份核验 | 21:12:16.543–21:12:21.468 |
| 切换请求 / 新容器启动 | 21:12:21.468 / 21:12:21.716 |
| 首HTTP可达 / Docker healthy | 21:12:22.827 / 21:12:52.057 |
| 双内网入口 / 本机资源验收 | 21:12:52.110 / 21:12:52.229 |
| 公网验收 | 21:13:27.815 |

正式源码`/home/ubuntu/project/account-pool-balancer-releases/20261108T211153Z-a6d7d38`；镜像`account-pool-balancer:release-a6d7d38-20261108T211153Z`（ID`sha256:bd03e14127965dfae1af8f880675e9bebf8f03579d6429a1c9be6e790a1a566c`）。受限完整备份`/home/ubuntu/backups/account-pool-balancer/20261108T211153Z-cache-rate-headers-a6d7d38/`含data、原Compose/.env、构建/切换日志及deploy.json，data SHA256`4d666d086060d392a3bad7f4a273e77dcc36d90c078f6985dcbc0707c6efd371`。旧镜像`account-pool-balancer:rollback-20261108T211153Z`保留；回退先确认上线后的新写入，不自动覆盖data。未采样精确停机，旧退出至首HTTP观测约6.57秒，不把构建/排空/健康等待算停机。正式发布/备份资料及Trellis保留，无新待清理临时产物，既有临时文件未经同意不清理。

## 历史部署：Mistral 下游会话/缓存头原值补齐（2026-11-08 20:26 UTC，be14987）

- 用户明确授权合入master、提交推送并部署OVH。业务提交`d6caba5`与远端会话别名/客户端头诊断正常合并为`be149876b647db580b8656f807e6bdfd76e311dc`，已非强制推送master；其他窗口未提交的注册相关改动未提交、未打包、未部署。
- Mistral Chat/Responses缺少正文`prompt_cache_key`时，优先取缓存头、再取额外/父/内置会话头的有效原值直接填入，不做HMAC或前缀。正文已有值（含null/空串）保留；无有效头才保留HMAC兜底。账号绑定仍按分组/下游Key做HMAC隔离，X-Affinity和日志脱敏规则不变。保留线上已有JSON会话别名和客户端头诊断，不改其他池。
- 复用业务提交已通过的全仓normal/race/vet/build，合并后补一次Mistral定向回归（5.938秒）和客户端头Node UI测试，均通过；不重复全仓检查，不发生产推理。一次预检确认旧f5b2e60 healthy/restart0、双入口正常、247.6GiB余量及旧e6f063b工作树干净；旧树未构建/重置。
- 522个干净提交文件逐项SHA256核验，复用现有部署脚本仅内存适配。存储格式不变，不另造敏感副本；新镜像就绪且TH/Mistral无queued/running后停旧进程，退出0并完整一致备份/gzip/SHA256核验，以线上Compose仅追加image覆盖切换。
- **191账号、186条TH核心注册记录、routingSecret、完整config.json字节、Mistral注册库及用户/环境/healthcheck/卷/端口/网络/资源参数全部保留**；Docker healthy/restart0、无OOM、Healthcheck存在，ai-internal双别名及NewAPI容器到两个healthz均成功，四项本机资源SHA256匹配。未修改NewAPI渠道、线上配置或功能开关。
- 20:26:01.825 UTC真实未登录公网浏览器登录页正常，healthz及四资源均200，保留`?v=c7f1808904`且SHA256逐项匹配发布源码；验收后已立即反馈，专用浏览器关闭，公网回执写入正式备份deploy.json。**本轮验收为部署/数据/入口/资源验收，不是生产缓存命中率或真实上游推理对照。**补记录不再构建/重启。

| 阶段 | UTC时间 |
|---|---|
| 构建 | 20:24:42.675–20:24:49.805（7.13秒） |
| 停止请求 / 旧进程退出 | 20:24:49.918 / 20:24:50.049 |
| 完整一致备份核验 | 20:24:50.320–20:24:55.072 |
| 切换请求 / 新容器启动 | 20:24:55.072 / 20:24:55.294 |
| 首HTTP可达 / Docker healthy | 20:24:56.396 / 20:25:25.632 |
| 双内网入口及本机验收 / 公网验收 | 20:25:25.799 / 20:26:01.825 |

正式源码`/home/ubuntu/project/account-pool-balancer-releases/20261108T202426Z-be14987`；镜像`account-pool-balancer:release-be14987-20261108T202426Z`（ID`sha256:8097925decb78d0a89d549bd761498c87ccedce9d46e2baa2d2f7e53cd06d3d0`）。受限完整备份`/home/ubuntu/backups/account-pool-balancer/20261108T202426Z-mistral-cache-headers-be14987/`含data、原Compose/.env、构建/切换日志和deploy.json；data SHA256`553dc2b2ab4561645f6cdd013e8405466bc0b9e7cf63bc38ee91556a7b56aab3`。旧镜像`account-pool-balancer:rollback-20261108T202426Z`保留，回退先确认新写入，不自动覆盖旧data。未采样精确停机，旧退出至首HTTP观测约6.35秒，不把构建/排空/健康检查等待算作停机。无新增临时脚本/归档/敏感下载，正式审计/回退资料与Trellis保留。

## 历史部署：Mistral JSON 会话别名与客户端请求头诊断（2026-11-08 19:57 UTC，f5b2e60）

- 按授权在独立工作树合并最新master，保留2629255的绑定持久化、真实会话优先与缓存别名归一；合并提交`f5b2e60e7fc898477549563ab893fe48a37cab22`已快进推送远端master。未修改原工作区或其他窗口的Trellis任务。Mistral新增8个Claude JSON-string会话别名及日志私密值补齐；请求摘要新增有界、脱敏的客户端头、候选有效性和最终身份来源，关闭正文详情仍可用，其他池不变。
- 合并一次定向Go回归通过（0.898秒），复用此前通过的客户端头UI/语法检查，无全仓/race/vet/压测或真实推理。解决身份优先级冲突时，两阶段Mistral解析均使用本池别名解析器；新增两个cache-hint共存用例，其他池公共解析器不变。
- 521个干净源码文件逐项SHA256核验，复用现有部署脚本仅内存适配。线上旧e6f063b工作树保持干净，未在其构建或重置。存储格式与已运行2629255相同，不另造敏感预检副本；镜像就绪后停旧进程，退出0并完整一致备份/gzip/SHA256核验，以线上Compose仅覆盖image切换。
- Docker healthy/restart0且Healthcheck存在，ai-internal双别名及NewAPI到两healthz均成功；191账号、186条TH核心注册记录、routingSecret、完整config.json字节、Mistral注册库及用户/环境/健康/卷/端口/网络/资源参数全部保留。未修改NewAPI渠道、线上配置或功能开关。
- 19:57:36.862 UTC真实未登录公网浏览器登录页、healthz及四资源200，保留`?v=c7f1808904`且SHA256逐项匹配，专用浏览器已关闭。上线验收即反馈；新日志不补写历史，未主动推理或冒称已确认真实客户端会话缺失原因/缓存命中率改善。部署记录提交不再重建或重启。

| 阶段 | UTC时间 |
|---|---|
| 构建 | 19:56:12.200–19:56:19.141（6.94秒） |
| 停止请求 / 旧进程退出 | 19:56:19.256 / 19:56:19.378 |
| 一致备份核验 / 切换请求 / 新容器启动 | 19:56:19.659–19:56:23.940 / 19:56:23.940 / 19:56:24.189 |
| 首HTTP可达 / Docker healthy / 双入口 | 19:56:25.299 / 19:56:54.534 / 19:56:54.691 |
| 本机资源 / 公网验收 | 19:56:54.694 / 19:57:36.862 |

正式源码`/home/ubuntu/project/account-pool-balancer-releases/20261108T195556Z-f5b2e60`；镜像`account-pool-balancer:release-f5b2e60-20261108T195556Z`（ID`sha256:41c49b06e8f6f6746261c7619d0cf812a1f7530441859e9f1feb9b621f1eb028`）。受限完整备份`/home/ubuntu/backups/account-pool-balancer/20261108T195556Z-mistral-session-headers-f5b2e60/`含data、原Compose/.env、构建/切换日志及deploy.json；data SHA256`828e49fb221a5b0730bb17a3df07421bfde2564a76b4ae55b175d3060fab75bf`。旧镜像`account-pool-balancer:rollback-20261108T195556Z`保留，回退先确认新写入，不自动覆盖旧data。未采样精确停机，旧退出至首HTTP观测约5.92秒，不把构建/健康等待算作停机。无新增临时脚本、归档或敏感下载；正式发布/回退资料与Trellis保留。

## 历史部署：Mistral 缓存绑定持久化与会话优先（2026-11-08 19:44 UTC，2629255）

- 用户明确“清理，提交推送部署”，并要求不要无关测试。`2629255e66c3d74597c323ccd63756872727c330`已推送master并上线：普通Mistral绑定跨重启保存、真实会话优先于cache hint、OpenCode Chat缓存别名无损归一与隐私。复用已通过的一次定向回归/必要build，不重复全仓/race/vet/UI业务测试，不发生产推理。
- 一次预检确认旧5d15347 healthy/restart0、191账号、双内网入口、248.1GiB余量；旧e6f063b工作树干净但历史分叉，未构建/重置旧树。干净提交516个源码文件逐项SHA256核验；复用既有部署脚本仅内存适配，没有新建部署系统或Trellis任务。
- 本次首次新增可选`metadata.pool.mistralBindings`：新镜像以线上UID1000:1000、network none运行一次兼容启动，副本显式带新字段，health及退出0通过；临时容器/敏感副本/日志已当场定向清理，原线上data未改。镜像就绪后停止旧进程、退出0并完整一致备份data，gzip/SHA256核验，然后仅追加线上Compose的image覆盖。
- **191账号、186条TH核心注册资产、routingSecret、完整config.json字节、Mistral注册库及用户/环境/healthcheck/卷/端口/网络/资源参数全部保留**。未开启Mistral详情、修改NewAPI渠道或其他开关。Docker healthy/restart0且有Healthcheck，ai-internal保留新旧别名，NewAPI容器请求account-pool-balancer与opendesign-balancer两个healthz均成功，四项本机静态资源SHA256匹配。
- 上线验收后立即反馈；19:44:17.086 UTC真实未登录公网浏览器登录页与healthz200通过，专用浏览器已关闭。19:44:43.682 UTC一次只读核对发现**6条自然流量普通Mistral绑定已持久化，均为弱身份，key/source均64位hash**；未主动推理或追加等待观察。这不是受控缓存命中率对照，也不声称修复了客户端缺少显式session。
- 按授权已清理本轮Magpie浅克隆/合成审计、hardening采样/验证目录及指定公开文档工具产物，共约219.7MiB；正式文档、Trellis记录、共享Go缓存、其他任务文件以及新旧镜像/备份均保留。部署后的记录提交不再重建或重启。

| 阶段 | UTC时间 |
|---|---|
| 构建 | 19:42:45.308–19:42:52.722（7.41秒） |
| 断网兼容启动 / 临时副本清理完成 | 19:42:52.722–19:42:53.658 |
| 停止请求 / 旧进程退出 | 19:42:53.773 / 19:42:58.531 |
| 完整一致备份及核验 | 19:42:58.852–19:43:02.980 |
| 切换请求 / 新容器启动 | 19:43:02.980 / 19:43:03.195 |
| 首HTTP可达 / Docker healthy | 19:43:04.293 / 19:43:33.528 |
| 双内网入口 / 本机验收 | 19:43:33.557 / 19:43:33.677 |
| 公网 / 自然绑定落盘核对 | 19:44:17.086 / 19:44:43.682 |

正式源码`/home/ubuntu/project/account-pool-balancer-releases/20261108T194231Z-2629255`；镜像`account-pool-balancer:release-2629255-20261108T194231Z`（ID`sha256:7754ac7bbf757c4d54600cb01fd7e85a32ef33100c2d8b9245f094cb9577b6c1`）。受限完整备份`/home/ubuntu/backups/account-pool-balancer/20261108T194231Z-mistral-cache-2629255/`含data、原Compose/.env、构建/切换日志、deploy.json及降版说明；data SHA256`e8e1e4d51be49f0915204d5a1a7339deb1353a5214555184b5c683b16d7652e8`，旧镜像`account-pool-balancer:rollback-20261108T194231Z`保留。**降版先停新进程并备份当前data，旧版严格JSON读取器需要时只移除当前metadata的派生mistralBindings，不得覆盖后续写入或改responseBindings。**未采样精确停机，旧退出至首HTTP观测间隔约5.76秒，不把构建/排空/健康检查等待算作停机。

## 历史部署：Mistral Responses终态与三模型思考档位（2026-11-08 18:44 UTC，5d15347）

- 用户明确授权提交推送部署。业务代码`3ec9611`及任务/规格记录`5d15347e55d5b63986f8f79e708223d43e035ef8`已推送master并上线。修复Mistral Responses首帧/后续终态与缺终态EOF误记成功；DeepSeek V4.1 Flash 1017、GLM5.3、GLM5.3 Flash在Chat/Responses支持low/high/max，缺省high，显式max不再降级。保留DeepSeek专属兼容与Large4/其他池原生隔离，更新UI预算/客户端验收边界文案。
- 复用已完成的全仓Go normal/race/vet/build、全部Node行为/语法、本地零账号真实浏览器和最终Mistral定向race结果；本次不重复跑完整测试、不主动生产推理、重放或压测。无持久化字段/存储格式迁移，不额外复制真实凭据做断网副本预检。
- 一次预检：旧0ed42ff healthy/restart0，191账号/186条TH注册记录，TH/Mistral注册无queued/running，磁盘250.7GiB余量，NewAPI双别名入口均正常。旧e6f063b工作树干净但有历史分叉，未构建/重置旧树；以干净git archive压缩管道传输509个源码文件并逐项SHA256核验。复用既有部署脚本仅内存适配目标版本、完整资产/注册忙守卫和双入口验收，不新增临时部署系统。
- 新镜像就绪后停止旧进程、退出0后完整一致备份data并gzip/SHA256核验，以线上Compose仅覆盖image。**191账号、186条TH核心记录、routingSecret、Mistral注册库、完整config.json字节，以及原用户/环境/healthcheck/卷/端口/网络/资源参数全部保留**。预检发现线上Mistral detailLog已是false，按本次实际值保留，不按历史启用记录擅自重新打开；其他功能开关不改。
- Docker healthy/restart0且Config.Healthcheck存在，ai-internal保留account-pool-balancer/opendesign-balancer；从new-api容器分别请求两者healthz均成功。18:44:35.252 UTC真实未登录公网浏览器登录页正常，healthz及四静态资源200，保留`?v=cbee624b83`且SHA256逐项匹配发布源码。服务与公网验收后立即反馈，浏览器已关闭；补记录不再构建/重启。
- **验收边界**：本次确认版本、健康、数据/配置/运行参数保留、双内网入口及公网静态资源，不代表真实上游三模型三档效果、Codex/Cherry Studio/pi端到端或生产缓存命中已验收。没有改NewAPI渠道或发付费推理。

| 阶段 | UTC时间 |
|---|---|
| 构建 | 18:42:46.347–18:43:25.923（39.58秒） |
| 停止请求 / 旧进程退出 | 18:43:26.035 / 18:43:26.191 |
| 完整一致备份及核验 | 18:43:27.249–18:43:30.325 |
| 切换请求 / 新容器启动 | 18:43:30.326 / 18:43:30.556 |
| 首HTTP可达 / Docker healthy | 18:43:31.667 / 18:44:00.905 |
| 双入口 / 本机验收 | 18:44:00.957 / 18:44:01.080 |
| 公网验收 | 18:44:35.252 |

正式源码`/home/ubuntu/project/account-pool-balancer-releases/20261108T184140Z-5d15347`；镜像`account-pool-balancer:release-5d15347-20261108T184140Z`（ID`sha256:328f28d3f7edf33d79cfc4281c621f97df1adf85acad106c4abe0e16120435fc`）。完整受限备份`/home/ubuntu/backups/account-pool-balancer/20261108T184140Z-mistral-stream-reasoning-5d15347/`含原Compose/.env、完整data、build/switch日志与deploy.json，data SHA256`14e7d20f292f74f3ccc5f6e7618ecf140e1872313285022a75c031a190f958cd`；旧镜像`account-pool-balancer:rollback-20261108T184140Z`保留。回退前先确认上线后新写入，不自动覆盖旧data。未采样精确停机；旧退出至首HTTP观测间隔约5.48秒，不把构建或健康等待算作停机。正式回退资料和Trellis保留，无新增临时脚本/压缩包/敏感副本，既有临时产物待同意清理。

## 历史部署：全程平均TPS与Mistral三段详情（2026-11-08 17:41 UTC，0ed42ff）

- `0ed42ff3834d64d152094b07c1d5b8483d551d43`已提交推送并上线。新增useTimeSec/averageTps，按NewAPI的两个Unix整秒差计算，前端一次整数四舍五入；保留首事件后/上游均速，不回填旧日志，不承诺不同层级计时数值相等。
- Mistral详情新增wireVersion=1/upstreamAttempts：原客户端头/体、每次实际上游头/体、适配前原生响应及适配后客户端响应明确分段，关联账号/尝试。HTTP trace补自动头、有界tee不延迟首帧，每段64KiB且标截断/未完整读取。凭据/Cookie/自定义Token/会话/X-Affinity掩码，新获响应Cookie也用于下游及错误摘要脱敏，SSE日志大整数保真。其他池详情策略不变。
- 预检发现实际上一运行版本是a817edf，不是文档中的77e9709。对正式release363个既有运行文件做SHA比对，并审阅两处源码差异及新增测试，保留客户端优先/最终cache key的X-Affinity修复；没有回退线上路由逻辑。496文件干净快照核验后复用既有部署脚本，仅内存适配，Compose仅image覆盖。**191账号、186TH核心记录、routingSecret、Mistral注册库、原config字节及用户/环境/healthcheck/卷/网络/资源参数保留**；healthy/零重启、两个内网别名及NewAPI到两healthz通过。
- 定向normal4.547s/race6.682s，Cookie错误摘要补强后的wire定向race2.193s，Mistral/TH Node、JS语法、vet/build/格式检查通过；没有全仓重跑、生产推理/重放/压测。服务健康后立即反馈，不因补记录再次重启。
- **用户明确要求记录后，17:40:37.705通过正常管理员API仅将Mistral独立detailLog从false启用并持久化**，revision5509。其他有效参数、账号/密钥/管理员、其他池和全局配置核验不变，原独立routing/quota均非继承，无额外钉住继承值。必要的既有出厂规则规范化按当前有效值保存；详情开关修改不冒充部署时config未变。仅布尔/枚举回执存正式备份detail-switch.json，密码/Cookie/CSRF始终只在远端内存，已正常logout。
- 17:41:36.241真实未登录公网登录页、healthz及四资源200，`?v=93a90552bf`且SHA256与发布源码一致，浏览器已关闭。17:41:38.964自然样本51条中47条有averageTps且逐条核对公式正确，原upstreamTps也47条；已生成15条新wire详情，客户端/实际上游请求和原生响应头/体、sent/状态/截断均存在，凭据头掩码通过。样本请求183201→183325字节、1751315→1763820字节体现适配前后区别，响应95210字节明确限长。只输出字节/形态，不下载或回显真实正文。真实管理员弹窗未浏览，展示由Node与生产API投影验证，不冒充全UI验收。

| 阶段 | UTC时间 |
|---|---|
| 构建 | 17:38:00.122–17:38:07.183（7.06秒） |
| 停止请求 / 旧进程退出 | 17:38:07.294 / 17:38:07.433 |
| 完整一致备份及核验 | 17:38:07.691–17:38:13.732 |
| 切换请求 / 新容器启动 | 17:38:13.732 / 17:38:13.999 |
| 首HTTP可达 / Docker healthy | 17:38:15.129 / 17:38:44.362 |
| 双入口 / 本机验收 | 17:38:44.424 / 17:38:44.537 |
| 启用本池详情 / 公网验收 / 自然日志核验 | 17:40:37.705 / 17:41:36.241 / 17:41:38.964 |

源码 `/home/ubuntu/project/account-pool-balancer-releases/20261108T173746Z-0ed42ff`；镜像 `account-pool-balancer:release-0ed42ff-20261108T173746Z`（ID `sha256:bab8dff537612eef63243e431e2617459301715d0ddbfeedc1911493a50c88ea`）。完整备份 `/home/ubuntu/backups/account-pool-balancer/20261108T173746Z-mistral-wire-tps-0ed42ff/`，data SHA256 `b7db9829c6e3b0b0e44fc877f88840a7bedacff75b4c1cf7c9d0578218e10f1c`，回退镜像 `account-pool-balancer:rollback-20261108T173746Z`保留。回退先确认后续数据/开关写入，不自动恢复旧data。未采样精确停机；旧退出至首HTTP观测约7.70秒，不把构建/健康等待算停机。无新增临时脚本/归档/pcap，正式回退资料与Trellis保留，既有临时文件待同意清理。

## 历史部署：Mistral 会话、订阅、TPS 与热路径优化（2026-11-08 15:36 UTC，77e9709）

- 业务提交 `77e9709f2a95f0ef3dec4237a234d338bb70bdfd` 已推送并上线。补Codex当前session-id/thread-id与兼容头、Claude会话结构/root-user兼容及Mistral缓存头fallback，记录客户端族，所有新增身份沿用HMAC/隐私与下游Key隔离；无身份仍合理fallback，不将UA/请求ID/普通user伪造为会话。
- 套餐新增独立组织subscription及可空只读PAYG、失败历史/状态；不混淆API Key访问等级Free与组织Student/Pro，不抹掉成功预算。原有全局4096 LRU在概览显示容量及全局用量，修复持久化严格绑定淘汰未标dirty；缺失所有者仍fail-closed。默认503+精确顶层3800不再无收益换号/扣Key健康，保留Retry-After、429恢复及其他5xx策略；仅旧完整出厂规则运行时升级，自定义列表不覆盖。
- 日志接入最后真实上游尝试的输出用量/TPS/时间窗口；流式post-first-event与非流式request-average明确区分，未知/失败不造值，不是上游内部decode速度。局部短基准SSE2613→134.5ns、46→0alloc；128条文本块历史801→274μs、379→104KB分配。只跳过确定无须转换的解析，不删思考/图片/工具/校验，不能冒充端到端提速。
- 受影响normal/race/vet、Node、gofmt/diff、build通过，没有全仓重复测试或生产推理/重放/压测。一次预检确认原3ed0ad8 healthy/双入口/251.2GiB可用；487个干净源码文件SHA256一致。复用原部署脚本仅内存适配，Compose仅image覆盖，**191账号、186条TH注册核心资产、routingSecret、原config.json字节、Mistral注册库、环境/用户/healthcheck/网络/资源参数完整保留**；原规则仅在内存按精确旧默认兼容升级。新容器healthy/restart0，NewAPI到两别名healthz成功。
- 另对 `/etc/nginx/conf.d/newapi.conf` 的yeschoy.com/nginx.yeschoy.com两个server加 `underscores_in_headers on`，其他校验不改。备份 `/home/ubuntu/backups/account-pool-balancer/20261108T152932Z-newapi-session-headers/`，nginx -t后仅reload一次。首次宽流量探针未取得完整证据，先只读确认active/配置有效/HTTP200，再缩到GET内存形态采样；15:31:05验证合成Session_id和Session-Id都到达NewAPI，receipt.json保留，无pcap。前后配置SHA256分别e116ac5b60c3b99163eeeeedcb24fca092593e2c82fd0e3b4735e2ad43946b3e、88593929d9d8643c6689a9abd394733bad2a604857787b4b20d97be5750eec65。
- 15:36:04.860公网未登录真实浏览器：登录页正常、healthz与四静态资源200，`?v=762b31dc63`及四SHA256与源码完全一致。浏览器已关闭。15:36:09正常管理员API仅对CHAT/API各一把Key刷新额度，两者成功，40预算新鲜；概览global225/currentMistral209/limit4096，503两条规则已生效。15:38:07只读metadata确认**40把Key订阅均成功持久化（Student6、Pro34），零订阅错误**；这些Key可能共享组织预算，不代表40份独立额度。密码/Cookie/CSRF仅远端内存，正常logout，不改账号/渠道/设置或新开功能。
- 上线后最近80条完成的自然请求均记录TPS（76流式、4非流式），不是主动压测。**这80条仍全部message_hmac**：双跳样本在进入NewAPI时已无身份，修复入口/识别不能凭空恢复更上游未传/已删字段；没有宣称全部Codex/Claude生产端到端识别或fallback比例下降。没有真实管理员浏览器保存往返，本轮新增显示由Node数据/UI测试和生产API投影验证，不冒充生产全UI验收。服务上线即反馈，补记录不再次部署。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 15:32:52.576–15:32:59.763（7.19秒） |
| 停止请求 / 旧进程退出 | 15:32:59.864 / 15:33:00.013 |
| 完整一致备份及核验 | 15:33:00.270–15:33:05.968 |
| 切换请求 / 新容器启动 | 15:33:05.968 / 15:33:06.270 |
| 首HTTP可达 / Docker healthy | 15:33:07.377 / 15:33:36.611 |
| 双入口 / 本机验收 | 15:33:36.665 / 15:33:36.763 |
| 公网验收 / 两类真实刷新 | 15:36:04.860 / 15:36:09.624 |

正式源码 `/home/ubuntu/project/account-pool-balancer-releases/20261108T153233Z-77e9709`；镜像 `account-pool-balancer:release-77e9709-20261108T153233Z`，ID `sha256:d7bd6a623d26ad7f02b74f5f47c7c9180e14f70e3533561ebb344a4462e2313e`。完整受限备份 `/home/ubuntu/backups/account-pool-balancer/20261108T153233Z-mistral-session-perf-77e9709/`，data SHA256 `227c373326849df6adad29a2dcb53d1519e9b9594e1f958aec1ced86a02bbf59`；回退镜像 `account-pool-balancer:rollback-20261108T153233Z` 保留。回退先检查新写入/新增可选metadata字段兼容，不自动覆盖旧data。未采样精确停机；旧进程退出至首HTTP观测约7.36秒，不把构建或Docker健康等待算作停机。正式回退资料/Trellis保留，旧临时产物待同意清理。

## 历史部署：Mistral 图片 MimeType 修复与最终思考规则（2026-11-08 13:18 UTC，3ed0ad8）

- 截图北京时间20:58:48/49两条请求 `req_78b430e23d83d38978c2422a` / `req_ee38b187ecbf9b2b68cfe80f` 都是第29条user图片消息携带 `image_url.MimeType:""` 导致422；两条NewAPI预览均含该空字段/PNG data URL。此前合成一像素图校验已证实该字段触发 `extra_forbidden`，去掉后图片schema通过；故不是请求体上限或模型不支持图片。
- `54b5b26` 修复仅精确DeepSeek Chat user图片块的空/null内部MimeType，保留图片、完整历史、detail等有效选项，重复字段本地拒绝，不擅自删除非空MIME。其后用户连续追加要求：`89a4cdc` 默认/非法档位统一high、保留low/high/max；最终 `3ed0ad86d05e27c6174fe31562e0bbe2a0ff299c` **max也映射high，仅low/high保留**。根reasoning_effort和reasoning.effort共用归一化；省略/null/空值/其他字符串或非法档位类型均high，兼容开关不再产生none。额外预算/未知reasoning结构、重复字段及归一化后冲突仍拒绝。其他模型/Responses/FIM/号池不变；不增输出预算、不改调度/重试/账号配置。
- 三次业务提交均推送master。按用户要求，每个新增改动只跑一次相关定向Go回归（1.015秒、1.833秒、1.884秒），以及gofmt/diff check和必要镜像构建；未跑全仓/race/vet/长观察或主动生产推理，不重放用户失败请求。Trellis规格随代码提交。
- 复用既有部署脚本仅内存适配，每次守卫精确旧container/image/revision及注册任务空闲，干净提交快照472文件逐项SHA256核验。首轮未压缩传输超时90秒，确认远端只有部分源码、服务仍旧版健康且未开始构建/停服后，改用同一提交的压缩归档补齐并重新核验；没有盲目在不完整源码上构建，也没有额外废弃release。后两次是用户新增要求后的明确更新，不是记录变更引发重复发布。
- 三次均在镜像就绪后干净停进程、完整一致备份data并gzip校验，沿线上Compose仅覆盖image；**191账号、186条TH注册记录核心资产、routingSecret、完整config、Mistral明文注册库以及用户/环境/健康检查/卷/端口/网络/资源参数全部保留**。Docker healthy/重启0，ai-internal双别名保留，每次都从new-api分别验证account-pool-balancer和opendesign-balancer的healthz成功。没有在旧e6f063b工作树构建，也未抹平分叉、覆盖在线配置或新增功能开关。
- 最终镜像 `account-pool-balancer:release-3ed0ad8-20261108T131646Z`，ID `sha256:0a0905df542b15eb9853884006d6d695e55a64f30702a2fe8af94046ef1efa23`。13:18:24.177 UTC真实未登录公网登录页正常、healthz及四资源200，保留 `?v=a5bb5ebec0` 且四资源SHA256与源码一致。54b5b26先前亦于13:10:13.021公网验收；89a4cdc通过内部验收后被用户追加要求的3ed0ad8替代，未单列其公网验收。上线/健康验收通过即反馈，浏览器已关闭；补记录不再次重启。
- 只读自然流量快照截至13:18:25.500 UTC，按请求起始ts分段且仅计已落盘记录：54b5b26阶段141次200/21次499，89a4cdc阶段32次200/2次499，最终3ed0ad8初步8次200；各段无新Mistral错误事件/422。499单列客户端取消，不冒充服务成功；这是跨版本初步自然样本，不是同一失败请求重放、具体档位生产对照或完整10分钟验收，不承诺第三方503根治。

| 阶段（UTC） | 54b5b26 | 89a4cdc | 3ed0ad8 |
|---|---|---|---|
| 构建 | 13:07:54.546–13:08:01.856（7.31秒） | 13:14:00.608–13:14:07.623（7.01秒） | 13:17:13.051–13:17:19.712（6.66秒） |
| 停止请求 / 旧进程退出 | 13:08:01.946 / 13:08:06.654 | 13:14:07.712 / 13:14:07.858 | 13:17:19.802 / 13:17:19.938 |
| 完整一致备份及核验 | 13:08:06.975–13:08:12.109 | 13:14:08.112–13:14:13.276 | 13:17:20.199–13:17:25.393 |
| 切换请求 / 新容器启动 | 13:08:12.109 / 13:08:12.361 | 13:14:13.276 / 13:14:13.519 | 13:17:25.393 / 13:17:25.622 |
| 首HTTP可达 / Docker healthy | 13:08:13.470 / 13:08:42.701 | 13:14:14.634 / 13:14:43.875 | 13:17:26.720 / 13:17:55.964 |
| 双入口 / 本机验收 | 13:08:42.755 / 13:08:42.845 | 13:14:43.928 / 13:14:44.019 | 13:17:56.017 / 13:17:56.121 |

正式release位于 `/home/ubuntu/project/account-pool-balancer-releases/` 下对应stamp-版本目录；正式备份位于 `/home/ubuntu/backups/account-pool-balancer/`，保留data、SHA256、原Compose/.env、build/switch日志与deploy.json：

- `20261108T130440Z-mistral-mime-54b5b26/`，data SHA256 `fe322e93b7e6be66d0ec2a8ff8abdb4d5d65b7a5f127ec7efde0c782bb3e3994`。
- `20261108T131342Z-mistral-high-89a4cdc/`，data SHA256 `a6af9b742ea0a8a3bf6595de979d33419536344c6473304883b38b894c4f7b8c`。
- `20261108T131646Z-mistral-max-high-3ed0ad8/`，data SHA256 `00ea6857d857acc1c7289c0e7eafcca9b7cb0b01216641d8fcf278427cc3ca97`。

各自 `account-pool-balancer:rollback-<stamp>` 及新镜像均保留；回退前确认后续写入，不自动覆盖旧data。未采样精确停机，三次旧进程退出至首HTTP可达观测间隔均约6.8秒；不把源码传输、构建、请求排空或健康检查等待当停机。无新增临时脚本/压缩包/敏感下载；临时产物规则见 `_tmp_artifacts.md`，Trellis和正式审计/回退资料保留。

## 历史部署：Mistral 思考档位优先级及图片格式兼容（2026-11-08 12:57 UTC，a2fdba1）

- 用户报告本地400思考开关/档位冲突及422，明确要求少做测试、直接自动上线。`a2fdba1b20a5bcf0b710538848f189c3ee1f17b7` 已提交推送并部署：有效显式effort优先于兼容开关，缺失/null才按开关；精确DeepSeek Chat的user图片URL-only对象转原生字符串，保留图片和额外选项，不改模型/Responses/其他池。仅跑一次相关定向回归（1.450秒）及必要build，不跑全仓/race/长观察，不发生产推理。
- 一次预检确认原4f8a680 healthy/零重启及双入口正常；干净472文件快照逐项SHA256核对后复用原脚本，仅内存适配。构建镜像 `account-pool-balancer:release-a2fdba1-20261108T125534Z`，ID `sha256:a42b17de515ef2e78e2fa88ed1ab2da45513f785d8c4690ac4cade6cb142784d`。完整一致备份 `/home/ubuntu/backups/account-pool-balancer/20261108T125534Z-mistral-compat-a2fdba1/` 保留data、Compose/.env、日志及回执，gzip校验通过，SHA256 `69c4cbf168c95bba980ec2ef90576533bfbb04dcdc3febe2b195e7055532dbd0`；回退镜像 `account-pool-balancer:rollback-20261108T125534Z` 保留。
- 以线上Compose仅覆盖image，191账号/186条TH注册记录核心资产、routingSecret、完整config、Mistral明文注册库和运行参数均保留。healthy/零重启/healthcheck存在，ai-internal双别名保留，从new-api访问两个healthz均成功；公网未登录登录页、healthz及四资源200，?v=a5bb5ebec0与源码SHA256一致。上线即反馈，浏览器关闭；不因补记录再次重启。
- **422尚未完全解决**：旧错误和新版12:57:19事件均指向body.messages.28.user.content.str/string_type，恢复的截断input前缀含image_url对象和data URL。官方模型页明确支持文本/图片，不能当作文本模型拒图；但只看到首个union分支，不能据此断言全部数组非法。上线后一次只读自然流量核对（至12:58:39）有13次200、1次422、1次499，无400；这不是同一失败请求重放或完整功能验收。新增URL-only兼容未消除全部422，其他图片对象选项不静默删除。一次25秒有界内存形态采样未捕获到图片请求，已正常结束，无pcap/正文落盘；后续需要剩余失败请求的完整结构证据，不猜测删图或改模型。503仍属独立上游问题，本次未声称根治。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 12:56:43.905–12:56:51.036（7.13秒） |
| 停止请求 / 旧进程退出 | 12:56:51.125 / 12:56:53.507 |
| 一致备份及校验 | 12:56:53.806–12:56:58.891 |
| 切换请求 / 新容器启动 | 12:56:58.891 / 12:56:59.134 |
| 首HTTP可达 / Docker healthy | 12:57:00.247 / 12:57:29.482 |
| 双入口及本机验收 / 公网验收 | 12:57:29.631 / 12:57:58.357 |

未采样精确停机；旧进程退出至首HTTP可达观测约6.74秒，不把构建、停止等待或健康等待当成停机。正式源码/镜像/备份保留，旧临时产物待同意清理，Trellis记录提交保留。

## 历史部署：Mistral 额度与思考参数兼容（2026-11-08 12:37 UTC，4f8a680）

- 用户要求提交、推送并部署到OVH，避免过多测试。master已推送至 `4f8a680750582e9dc0fb59e5bf514407238f07f2`，包含此前额度刷新/独立路由 `4f8d74e`、缺省思考high `efe35ce`、精确DeepSeek Chat的reasoning.effort别名 `4f8a680`；Responses保持原生。复用此前全仓normal/race/vet/build及本地Mock结果，本次不重复运行、不发生产推理，不修改线上配置或功能开关。
- 一次预检确认原aaa09a6 healthy/重启0、旧e6f063b工作树干净且分叉3/141、252.7GiB余量、data挂载及NewAPI双内网入口正常。未在旧树构建或抹平分叉；干净git archive传到 `/home/ubuntu/project/account-pool-balancer-releases/20261108T123421Z-4f8a680`。传输命令曾超时，先确认远端结果，再逐项核对全部471个源码文件SHA256，未盲目重复发布。仅内存适配既有部署脚本，守卫精确旧container/image/revision及TH/Mistral注册任务空闲。
- 镜像 `account-pool-balancer:release-4f8a680-20261108T123421Z`，ID `sha256:9eef7cc00b5c40b94fffda67067c330d47d3afbe2c2efcdf88db61cd57faba63`，OCI revision正确。构建约7.21秒后才停旧进程并一致备份；沿线上Compose仅覆盖image，保留旧镜像 `account-pool-balancer:rollback-20261108T123421Z`。
- 正式备份 `/home/ubuntu/backups/account-pool-balancer/20261108T123421Z-reasoning-4f8a680/` 保留完整data、原Compose/.env、build/switch日志及deploy.json；gzip校验通过，data SHA256 `02d7ed461726226fe713b02b1708ec31e1c2d6f18b5c7e6ef1930f31200429d0`。**191账号、186条TH注册记录核心资产、routingSecret、完整config、Mistral明文注册库及原用户/环境/卷/端口/网络/健康检查/资源参数均保留**；不自动用旧data覆盖新写入。
- 上线验收：Docker healthy、重启0，healthcheck存在；ai-internal保留account-pool-balancer/opendesign-balancer，从new-api容器访问两者healthz均成功。真实未登录公网登录页正常，healthz及四资源均200，保留 `?v=a5bb5ebec0` 且SHA256逐项与发布源码匹配。通过后立即反馈，独立浏览器已关闭。未新增临时脚本、压缩包或敏感下载；正式发布/备份资料保留，旧临时产物未经同意不清理，Trellis随记录提交。
- **验收边界**：这是版本、配置/资产保留、健康及入口/静态资源验收，不是新的生产推理对照或生产管理台预算刷新/保存往返验收；额度功能已部署，不能把此前本地假数据验证冒充本轮生产UI验收。后续只提交记录，不再构建或重启。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 12:36:37.166–12:36:44.374（7.21秒） |
| 停止请求 / 旧进程退出 | 12:36:44.462 / 12:36:44.626 |
| 完整一致备份及校验 | 12:36:44.852–12:36:49.993 |
| 切换请求 / 新容器启动 | 12:36:49.993 / 12:36:50.246 |
| 首HTTP可达 / Docker healthy | 12:36:51.348 / 12:37:20.581 |
| NewAPI双入口 / 本机静态验收 | 12:37:20.634 / 12:37:20.727 |
| 公网验收 | 12:37:48.902 |

未采样精确停机；旧进程退出至首HTTP可达的观测间隔约6.72秒，不将构建、源码传输或Docker健康等待当成停机。

## 历史部署：Mistral 默认测试模型（2026-11-08 10:43 UTC，aaa09a6）

- 用户要求提交、推送并部署此前改动，暂不做 service。`aaa09a661bc1eda246b2938a14b804bde6adca99` 已推送 master：Mistral 管理台真实请求未指定模型时默认 `deepseek-v4-1-flash-1017`，保留显式模型与 `max_tokens=1`，免费检查仍不推理；同步前端提示与回归测试。`docs/mistral.md` 记录普通 API Key 可读 whoami/billing/budget 的真实证据，**本版未接入自动额度展示**。
- 复用既有定向 Go/Mistral/Modal、race、vet、Node 语法/行为和必要构建结果，不重复全仓测试，不主动生产推理。一次预检确认原容器 healthy/重启0、191账号、40个Mistral（30个zxg）、固定data挂载、256GiB余量及NewAPI双别名通路正常；旧工作树e6f063b干净但分叉3/141，未在旧树构建。原镜像无revision，使用精确container/image作切换守卫，不猜其Git版本。
- 干净 `git archive` 源码 `/home/ubuntu/project/account-pool-balancer-releases/20261108T104040Z-aaa09a6`，固定镜像 `account-pool-balancer:release-aaa09a6-20261108T104040Z`，ID `sha256:b5ca2f9e93ecb5edc9bb775155bd928def7399303394af8871c740829e5387c2`；显式设置OCI revision。无存储变更，不额外复制敏感数据做离线预检；构建完成后停旧进程、完整一致备份并gzip核验，仅以线上Compose的image覆盖切换。
- 验收：healthy、重启0，**191账号、186条TokenHarbor注册记录核心资产、routingSecret、完整config、Mistral明文注册库及原用户/环境/健康/卷/端口/网络/资源参数保留**。`ai-internal` 同时保留 `account-pool-balancer` / `opendesign-balancer`，从 `new-api` 容器访问两个名字的healthz均成功。真实未登录公网登录页正常，healthz和四资源200，保留 `?v=dc01f90104`，SHA256逐项匹配且新默认模型提示存在；验收通过即反馈。
- 正式备份 `/home/ubuntu/backups/account-pool-balancer/20261108T104040Z-mistral-test-aaa09a6/` 保留data/校验、原Compose/.env、构建/切换日志与deploy.json；data SHA256 `1c7c7bb2f69b817d3d5e19aa1d30a954c72eef1f72cabfcfabe50bd6c072dc53`。回退镜像 `account-pool-balancer:rollback-20261108T104040Z` 保留，回退前须确认上线后的写入，不自动覆盖旧data。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 10:41:05.801–10:41:52.165（46.36秒） |
| 停止请求 / 旧进程退出 | 10:41:52.247 / 10:41:52.353 |
| 完整一致备份及核验 | 10:41:53.255–10:41:56.692 |
| 切换请求 / 新容器启动 | 10:41:56.692 / 10:41:56.913 |
| 首HTTP可达 / Docker healthy | 10:41:58.021 / 10:42:27.261 |
| 本机及双渠道验收 / 公网验收 | 10:42:27.402 / 10:43:09.106 |

未采样精确停机；旧进程退出至首HTTP可达的观测间隔约5.67秒，不把构建或健康等待当成停机。仅内存适配既有部署脚本，没有新增临时脚本、容器或敏感下载；正式release/镜像/备份保留，既有临时产物仍按 `_tmp_artifacts.md` 待同意清理，Trellis记录保留。后续记录提交仅改文档，不再次部署。

## 线上故障恢复：补回渠道依赖的内网别名（2026-11-08 09:57 UTC）

- 现象：Balancer 宿主机 `/healthz` 为200、进程 running/restart0/无OOM，但
  09:48部署后没有新业务请求；NewAPI文件日志持续报
  `lookup opendesign-balancer on 127.0.0.11:53: server misbehaving`。
- 根因：5d9b62c切换创建的容器在 `ai-internal` 上 `Aliases=null`，丢了旧名
  `opendesign-balancer`。从 `new-api` 请求新名healthz成功、旧名失败；并非全部
  账号失效。仓库及线上原Compose都已声明旧名别名，实际创建容器未保留它。
- 恢复：09:57:36.016开始，保留原IP `172.18.0.8` 重连该容器的网络端点，补上
  `account-pool-balancer` / `opendesign-balancer`；重连操作0.182秒（非精确
  请求停机时长），09:57:36.259从NewAPI验证两个名字healthz均200。
  **同一容器、同一镜像、零进程重启、161账号及config.json SHA256不变**；
  未改渠道、密钥、代理、额度或业务功能，未重放用户失败请求。
- 初步自然流量验收截至09:58:29 UTC共10条全部success/200，NewAPI最新
  日志尾部无修复后的别名解析错误。继续截至09:59:36 UTC共29条完成，
  **OpenCode Go 15条、Mistral 8条、OpenDesign 5条全部success/200**；
  另1条Modal失败，对应2次已到上游的HTTP429，与此次DNS故障不同，未声称
  该上游错误也已修复。其他号池无该窗口样本，不主动推理或冒称全量实测。
- 无凭据的端点前后状态/配置哈希/验收回执保存在
  `/home/ubuntu/backups/account-pool-balancer/20261108T095736Z-network-alias-repair/network-repair.json`
  （目录0700、文件0600），属于正式故障审计资料，不是临时垃圾。
- 同时发现当前容器 `Config.Healthcheck=null`；HTTP健康和Docker healthy不是
  一回事。为避免恢复后再次中断，本次不重建来补监控；下次正常发布必须经
  Compose保留healthcheck并按上述NewAPI双入口检查验收。仅补记录不再次部署。

## 当前部署：Mistral 注册/订阅节奏停顿（2026-11-08 10:15 UTC，1ae58e7）

- 用户要求注册与订阅中间的核心流程放慢，避免过快触发风控。此前注册→登录→
  两处 ToS→三把 Key 在几十秒内完成，属明显机器节奏。
- 提交 `1ae58e7`：新增 `tools/mistral/signup/core/pacing.py`，在阶段边界插入
  带抖动的停顿，**采样用对数空间**而非均匀分布（固定或均匀间隔本身是特征）。
  关键值：注册→登录 20–60s、两把 Key 之间 8–25s、注册后缓冲 6–14s、
  验证后 5–12s；订阅相关步骤已预留（`register_to_subscribe` 180–420s，
  本版未接入订阅流程）。环境变量 `MISTRAL_PACE_SCALE`（0 关闭，上限 10）与
  `MISTRAL_PACE_OFF=1` 可调。预估单账号额外停顿 1.7–2.5 分钟。
- **顺带修复前次部署遗留**：上一版用裸 `docker run` 切换，导致容器丢失
  `Config.Healthcheck`、`TZ` 与 `ODB_ADMIN_PASSWORD`，且 `docker ps` 只显示
  "Up" 无 healthy。本次改用 `docker compose up -d --no-build` 基于线上 Compose
  重建，三项均已恢复。
- 预检：旧容器 healthz=200、别名 `account-pool-balancer`/`opendesign-balancer`
  较在、磁盘充足。本地 `go build`、`go test`、`test_cards.py`(15)、
  `test_pacing.py`(12) 全通过。
- 干净快照 `/home/ubuntu/project/account-pool-balancer-releases/20261108T101303Z-1ae58e7`，
  镜像 `account-pool-balancer:release-1ae58e7-20261108T101303Z`；旧镜像
  （`sha256:13269fb6d6c8…`）打 `rollback-20261108T101303Z` 保留。
- 备份 `/home/ubuntu/backups/account-pool-balancer/20261108T101303Z-mistral-pacing/`，
  data 264M，文件清单与线上逐项一致。
- **切换注意**：首次 `compose up` 因容器名冲突失败（旧容器非 Compose 创建），
  改为 `stop` + `rm` 后 `compose up` 成功，停机 10:15:06.52→06.96 约 0.3 秒。
- 验收：容器 `Up (healthy)`、`Config.Healthcheck` 存在、restarts=0、
  `TZ` 与 `ODB_ADMIN_PASSWORD` 已恢复；网络别名同时含 `account-pool-balancer`
  与 `opendesign-balancer`；**从 new-api 容器分别访问两个别名均返回
  `{"ok":true}`**；宿主机与公网 healthz 均 200；容器内 pacing 20 步、
  hybrid 13 处接线、`off` 模式返回 0；**191 账号**、`cardLines`/`proxyUrls`
  配置保持不变。
- 遗留：容器 `org.opencontainers.image.revision` 标签为空（Compose 未传
  build-arg），不影响运行；如需可读 revision 后续补 label。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 10:13:2x–10:14:0x（42.6 秒） |
| 停止 / 删除旧容器 | 10:15:06.523 / 10:15:06.677 |
| 新容器启动 | 10:15:06.966 |
| healthy 确认 | 10:15:1x |

未采样精确停机；旧容器退出至新容器启动约 0.3 秒。仅补记录不再次部署。

## 历史部署：Mistral 银行卡列表接入 Python worker（2026-11-08 09:48 UTC，5d9b62c）

- 用户要求提交推送并部署。注册机界面此前已能保存 `cardLines`，但 worker 未读取，
  卡列表实际处于「只存不用」。本次将其接到 Python 执行层。
- 提交 `5d9b62c`：新增 `tools/mistral/signup/core/cards.py`（解析
  `卡号----有效期----CVV`，校验 13-19 位与 Luhn，支持 `05/28`/`5/2028`/`05-28`/`0528`）；
  `worker.py` 读 `MISTRAL_CARDS` 并按 `proxy_index` 轮转选卡，结果回传
  `card_masked`/`card_expiry`（脱敏，不含完整卡号与 CVV）；
  `mistral_registration.go` 结果结构补两字段。规则与 Go 侧
  `internal/mistralcards` 一致，由 `test_consistent_with_go` 锁定两端同输入同输出。
  **卡当前只解析并回显，注册流程不使用；订阅步骤后续取用。**
- 预检：容器 healthy/restarts=0、`healthz=200`、磁盘 262GiB 余量、
  卷 `/home/ubuntu/project/account-pool-balancer/data:/data`、端口 `127.0.0.1:8790`
  均未变。线上 `config.json` 已含 `cardLines: []`（上一版已有该字段），
  **无存储格式变更**，兼容零风险。当前运行镜像无 revision 标签，
  以其 image（`sha256:feb81224f486…`）作为回退基线，不假称对应某 Git 版本。
- 本地验证：`go build`、`go test ./internal/mistralcards ./internal/config`、
  `python3 tools/mistral/test_cards.py`（15 passed）、`gofmt -l` 全部通过。
- 干净 `git archive` 快照 `/home/ubuntu/project/account-pool-balancer-releases/20261108T094514Z-5d9b62c`，
  固定镜像 `account-pool-balancer:release-5d9b62c-20261108T094514Z`
  （`sha256:13269fb6d6c814fb91b9bb627a0c6dd650315119b94165fd1293c66e4d0d447e`），
  旧镜像打 `rollback-20261108T094514Z` 保留。
- 备份 `/home/ubuntu/backups/account-pool-balancer/20261108T094514Z-mistral-cards/`，
  data 263M 完整复制，文件清单与线上逐项一致。**核验说明**：首次比对汇总摘要
  不一致，原因是服务运行中持续写 `metadata.json`/`selection-counters.json`/
  请求日志，重复计算摘要每次变化；文件列表与逐文件比对除活跃写文件外一致，
  判定为活跃服务正常现象而非备份损坏，未用旧数据覆盖上线后写入。
- **09:57故障复核更正**：此前“切换只覆盖image、网络/健康参数不变”的记录
  不准确；新容器遗漏了网络别名和healthcheck，导致NewAPI旧名渠道不可达，
  本机/公网200未覆盖该链路。网络别名已按上节修复，healthcheck缺失单独保留
  为后续正常发布检查项。**161账号**、`proxyUrls`、`cardLines`及注册库保留。
- 验收：容器 running、`healthz=200`（首次尝试即通过）、restarts=0、
  revision 恢复为 `5d9b62c…`、日志 `odb listening on :8790`；
  容器内 `/usr/local/share/mistral-signup/signup/core/cards.py` 存在且
  `parse_line("4633433883347213----05/28----913")` 返回
  `463343******7213 05/28 bin=463343`；公网 `https://ods.yeschoy.io/` 与
  `/healthz` 均 200；`app.js?v=18b5011ecd` 含 `ms-card-lines`/`renderCardStatus`。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 09:45:5x–09:46:3x（43.1 秒） |
| 停止请求 / 旧进程退出 | 09:48:01.4 / 09:48:01.452 |
| 备份及核验 | 切换前完成（263M） |
| 新容器启动 | 09:48:01.829 |
| 首 HTTP 可达 | 09:48:13（100% 200） |

未采样精确停机；旧退出至新启动约 0.38 秒。仅补记录不再次部署。

## 历史部署：Mistral DeepSeek assistant reasoning 兼容（2026-11-08 07:29 UTC，217bf64）

- 用户要求解决422并排查持续503。已从NewAPI Docker stderr的有界body preview恢复14条`extra_forbidden / body.messages.4.assistant.reasoning`，input均string；之前Balancer JSONL与NewAPI SQL只剩通用错误，不能据此声称完全无证据。解析仅远端内存，未输出/保存正文或凭据。
- 修复提交`49e8aa0`：仅精确DeepSeek Chat把assistant reasoning字符串/null作为reasoning_content别名，无损保留为原生thinking chunks；相同别名不重复、冲突/非法/重复字段本地拒绝，保留消息顺序、原答案、工具/ID/大数，其他模型/协议/角色不变。包含此前`7b652c9`的安全loc/type与真实截断诊断；不改重试次数/账号/TTL/限流/NewAPI规则。
- 正常合并并保留并发注册UI、Docker wget与用户要求的明文mistral-signup.json，集成版本`217bf640b0c30e4fd4a1662977c33b02e36858ae`已推送master/功能分支；没有用旧AES源码覆盖线上明文存储。定向Mistral回归3.098s、集成后2.574s，以及gofmt/diff check/必要build通过，不跑重复全仓/race/vet，不主动生产推理。
- 预检将无revision的当前运行镜像作为**精确image+container**回退基线（image`sha256:9f476a18df96b6ab2957ad7830ae8295fb6ee0ffab9a6b9a4c807e410d41e833`，不假称它对应某Git版本）；核对线上四资源SHA256与已合并源码一致、healthy/零重启、磁盘276.8GiB余量、注册无queued/running后才发布。先前因并发切换中止的7b652c9发布没有被冒称成功。
- 干净git archive源码`/home/ubuntu/project/account-pool-balancer-releases/20261108T072646Z-217bf64`，固定镜像`account-pool-balancer:release-217bf64-20261108T072646Z`，ID`sha256:67e3ac3a5511572e52cdbd06f6cd7b17daca093add57ee1e7f32a4a53175f499`。复用既有脚本仅内存适配基线guard/明文注册库摘要；无存储格式变化，不额外复制敏感预检数据。完整一致备份后仅image覆盖切换，**161账号、186注册记录核心资产、routingSecret、完整config、Mistral明文注册库及原用户/环境/卷/端口/网络/健康/资源参数保留**。
- 07:29:01.871本机验收，07:29:37.106 UTC真实未登录公网登录页/healthz/四资源200、保留?v=030230089e且SHA256逐项匹配，通过即反馈。healthy/零重启/无OOM，运行revision恢复可追溯。
- **上线后完整10分钟观察**：07:30:15.069546–07:40:15.069546 UTC（北京时间15:30:15–15:40:15），按完成时间88请求、85成功、3失败，错误率3.41%、取消0；**无422/本地400**。失败为2次官方Mistral503/code3800（4条上游尝试事件）及1次首响应等待达到原600秒上限的504（单次、未换号重放）。83条流式成功；NewAPI同窗口84消费/3错误、82ok/done，完成时间与NewAPI记账时间边界不同，不强行凑齐85成功。成功input14331638/cached11713152=81.73%，绑定73hit/15miss，无忙溢出；只有2次503沿既有流程切号/两attempt。36次健康采样无异常，结束仍217bf64。历史错误请求未重放，不把无新422等同于原14条原样实测完成。
- **503仍未根治**：已证实是api.mistral.ai返回3800/internal_server_error/Service unavailable，遍布10账号且各账号也有成功；此前31个最终503的第二次尝试没有成功样本。两段150秒被动形态采样分别17/20条全200，未捕获503请求形态，不能臆断具体预算/档位触发原因或以重启、加重试、静默丢参数宣称解决。仍需上游服务侧排查，不声称Balancer能保证消除第三方503。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 07:27:40.942–07:28:26.644（45.70秒） |
| 停止请求 / 旧进程退出 | 07:28:26.731 / 07:28:26.847 |
| 完整一致备份及核验 | 07:28:27.784–07:28:31.222 |
| 切换请求 / 新容器启动 | 07:28:31.222 / 07:28:31.434 |
| 首HTTP可达 / Docker healthy | 07:28:32.539 / 07:29:01.772 |
| 本机验收 / 公网验收 | 07:29:01.871 / 07:29:37.106 |

未采样精确停机；旧退出至首HTTP可达观测间隔约5.7秒，不把构建/健康等待当停机。受限正式备份`/home/ubuntu/backups/account-pool-balancer/20261108T072646Z-mistral-reasoning-217bf64/`保留完整data/原Compose与.env/日志/回执，data SHA256`2da52fa9275f1a13dbaa10f377fa3068401baf84002bfd0fa116edc70f73963d`；rollback镜像`account-pool-balancer:rollback-20261108T072646Z`保留发布前明文存储版本，不用9f375ef覆盖新注册数据。正式备份/源码/镜像及Trellis保留；中止发布的临时产物仍按_tmp_artifacts.md待同意清理。仅补记录不再次部署。

## 历史部署：Mistral 注册机改用明文存储（2026-11-08 15:25 UTC，1b253bf）

## 历史部署：Mistral 代理列表统一为完整 URL（2026-11-08 17:33 UTC，9227c0b）

- 用户要求去掉 zooproxy 模板模式：代理只有一种格式，一行一个
  `socks5://用户:密码@IP:端口`（zooproxy 同格式，不再区分）。
  `config.MistralSignup` 只剩 `proxyUrls`/`cardLines`/`mailboxProxy`/
  `workerPath`/`workerPython`/`concurrency`；`Proxy` 只持 `raw_url`，
  移除 `from_env` 与模板拼接；UI 删除「高级」折叠里的模板表单。
- **发布事故与回退**：首次部署（9ab3c64）容器进入 restarting，日志
  `startup: /data/config.json is corrupt: json: unknown field "proxyUserTemplate"`。
  原因是删除了字段但线上 `config.json` 仍带旧键，而 `fsutil.ReadJSON` 使用
  `DisallowUnknownFields`。已立即回退到 `release-fix-20261108T082340Z`
  恢复 healthy（healthz 200），再修复后重新发布。
- 修复（9227c0b）：旧字段改为 `Deprecated*` 保留在结构体中，读入后由
  `normalizeMistralSignup` 清空，下次写盘消失；新增回归测试
  `TestReadLegacyMistralSignupFields` 覆盖旧配置读取。同时备份并清理了
  线上 `config.json` 的旧键（备份
  `/home/ubuntu/backups/account-pool-balancer/20261108T093131Z-pre-mistral-9ab3c64/`）。
- 验收：容器 running/healthy、restarts=0、日志 `listening on :8790`；
  页面不再含 `ms-user-tpl`/`ms-sids`/`mr-advanced`（计数 0），
  保留 `ms-proxy-urls` 与「zooproxy 也用这个格式」提示，资源
  `app.js?v=18b5011ecd`；`PUT settings` 后 `revision=5459`、settings 只剩
  `proxyUrls`；`POST settings/test` 返回 `{"ip":"15.235.122.152"}`。
- 镜像 `account-pool-balancer:release-9227c0b-20261108T093154Z`。
  回退镜像 `release-fix-20261108T082340Z` 保留。

## 历史部署：Mistral 批量账号 + 代理轮转 + 自动入池（2026-11-08 16:22 UTC，a225c5c）

- 用户三项要求已实现：
  1. **代理列表一行一个，账号按顺序分配，代理可重复使用**：
     `PickProxyByIndex(n)` / `ProxyPool.by_index(n)` 取 `n%len`；候选记录
     `proxy_index`，worker 用该序号经本地网关连上游。
  2. **iCloud 账号列表批量导入**，每行 `账号----接码地址`：
     新增 `POST /api/mistral/candidates/bulk`，逐行校验并回报被拒行
     （缺分隔符/空值/邮箱格式/重复）。
  3. **注册成功后自动加入 Mistral 账号池**：
     候选带 `import_to_pool`；成功后 `importMistralCandidate` 写入
     `config.Accounts`（Provider=mistral、Group=mistral），拒绝重复 Key；
     列表新增「池」列显示 已入池/待入池/—。
- 前端表单由单账号改为批量 textarea，新增入池勾选；代理提示说明轮转规则。
- 验收：批量导入 3 条成功、坏行被拒；`proxy_index` 0/1/2 正确落盘并返回
  （`omitempty` 导致 idx=0 被省略，已修）；容器内 3 个代理按
  `0→a,1→b,2→c,3→a,4→b` 轮转；测试候选已清理为 `[]`；
  池内现有 mistral 账号 10 个、总计 161。
- 镜像 `account-pool-balancer:release-fix-20261108T081928Z`；仅
  `--force-recreate` Balancer。仍未验证：容器内真实注册与真实入池。

## 当前部署：Mistral 支持完整代理 URL（2026-11-08 15:55 UTC，2934598）

- 用户要求支持标准格式 `socks5://用户:密码@IP:端口`，不再局限于 zooproxy 模板。
- `config.MistralSignup` 新增 `proxyUrls`：填了即优先使用，忽略模板字段；
  `PickProxy`/`AllProxies` 统一两种来源。支持 socks5/socks5h/http/https。
- worker 注入 `MISTRAL_PROXY_URLS`（换行分隔）；`Proxy.from_url()` 直接持有
  完整 URL；`chain_gateway` 按上游 scheme 选择 SOCKS5 握手或 HTTP CONNECT
  （此前只实现了 HTTP CONNECT）。
- 前端：「代理列表」textarea 置顶优先，模板方式折叠进 `<details>`（高级）。
- 验收：`PUT` 保存 `proxyUrls` 后 `revision=5456`、`hasProxy=true`；
  `POST settings/test` 返回 `{"ip":"15.235.122.152"}`，与配置的出口一致；
  页面含 `ms-proxy-urls` 与格式说明，资源 `app.js?v=9c5ce31094`；
  容器 running/healthy。本机网关直连该 SOCKS5 出口亦为 `15.235.122.152`。
- 镜像 `account-pool-balancer:release-2934598-20261108T075416Z`；仅
  `--force-recreate` Balancer，卷/端口/网络未变。
- 仍未验证：容器内真实注册全流程；订阅扣款。

## 当前部署：Mistral 代理设置移到界面（2026-11-08 15:44 UTC，cfbc02a）

- 用户要求代理凭据在界面配置，而不是写进容器环境。新增
  `config.MistralSignup`（用户名模板/密码/SID 列表/入口/地区/接码代理），
  持久化在 `config.json` 的 `mistralSignup` 键；与 `mistral.go` 里 API 池的
  `Mistral`（BaseURL/routing）命名区分，未改动后者。
- worker 不再依赖容器环境变量：`runMistralWorker` 每次执行时把界面里的设置
  注入 `ZOOPROXY_*` / `MIDA_PROXY` 子进程环境。
- 新增 `GET/PUT /api/mistral/settings` 与 `POST /api/mistral/settings/test`
  （后者用配置的首个 SID 走一次 ipify，只验代理不跑注册）。
- 前端新增「代理与运行设置」表单 + 「测试代理」按钮。
- 验收：`PUT` 保存后 `revision=5454`、`hasProxy=true`；`POST settings/test`
  返回 `{"ip":"64.66.229.122"}`（sid q4h1WBwP）；`/data/config.json` 中
  `mistralSignup.proxySids` 3 条、`proxyPassword` 已写入；页面含
  `mr-settings-form`/`ms-user-tpl`/`代理与运行设置`，资源
  `app.js?v=a1370297d1`；容器 running/healthy。
- 镜像 `account-pool-balancer:release-cfbc02a-20261108T074202Z`；仅
  `--force-recreate` Balancer，卷/端口/网络未变。
- 仍未验证：容器内真实注册全流程；订阅扣款。

## 当前部署：Mistral 注册机改用明文存储（2026-11-08 15:25 UTC，1b253bf）

- 用户明确要求去掉加密存储。`mistralsignupstore` 由 AES-GCM 改为明文 JSON：
  文件 `/data/mistral-signup.json`，权限 0600，`cat` 可直接读出邮箱、密码与
  API Key。`Open(path, secret)` 保留 secret 参数位置以兼容调用方，实际不再
  使用；删除 `encrypt/decrypt/gcm` 与 `Public()` 遮罩方法。
- 前端候选列表直接显示完整 Key 并可点击复制；新增 `.mr-*` 样式。
- 验收：登录后 `POST /api/mistral/candidates` 返回 201 结构，落盘文件
  `-rw------- odb odb`、`rows` 内含明文 password；`DELETE` 后 `rows: []`。
  容器 running/healthy、restarts=0、healthz 200。
- 镜像 `account-pool-balancer:release-1b253bf-20261108T072132Z`，构建复用
  缓存；仅 `--force-recreate` Balancer 单服务，卷/端口/网络/配置未变。
- 回退镜像沿用 `account-pool-balancer:rollback-20261108T065425Z`（9f375ef）。
- 仍未验证：容器内真实注册（未配置 ZOOPROXY_* 代理池环境变量）；订阅扣款。

## 当前部署：Mistral 注册机界面骨架（2026-11-08 15:05 UTC，8ad33b5）

- 用户要求先做「界面壳子」：把命令行注册机变成管理页面可操作的形态。新增
  `mistralsignupstore`（AES-GCM 加密存储，与 perchsignupstore 同构）、
  `mistral_registration.go`（6 个 admin API + 串行 worker 调度）、
  `tools/mistral/worker.py`（stdin/stdout JSON 契约）、前端「Mistral 注册机」
  页面（候选列表 / 状态 / 开始 / 取消 / 删除）。**未接入订阅扣款**，就绪状态
  仅代表三把 API Key 生成成功。
- 基础镜像由 alpine 改为 debian:bookworm-slim：Playwright 官方 Chromium 为
  glibc 编译，musl 下无法运行。镜像由约 60MB 增至 2.01GB。首次切换因
  Debian 未预装 `wget` 导致 compose healthcheck 失败（`executable file not
  found`），服务本身正常；已回退旧镜像恢复，补 `wget` 后重建通过。
- 构建 1m7s（首次）/ 复用缓存后重建；一次预检原 9f375ef 健康、余量 287GiB。
  源码为干净 git clone：`20261108T065139Z-8b701f0`，镜像
  `account-pool-balancer:release-8ad33b5-20261108T070000Z`。
- 切换仅 `--force-recreate` Balancer 单服务；卷/端口/网络/配置/功能开关不变。
  数据目录挂载 `data:/data` 保留，9 个既有 JSON 配置在，未改动。
- 验收：容器 running/healthy、restarts=0、healthz 200；登录后
  `/api/mistral/status` 返回 `worker_found=true`，
  `python=/opt/mistral-signup/bin/python`；`/api/mistral/candidates` 空列表；
  `index.html` 含 `page-mregister`、`app.js` 含 Mistral 逻辑；资源版本
  `app.js?v=f4bb4a3c6e`。
- 未登录访问 `/api/mistral/*` 与 `/api/perch/*` 均为 401，行为一致。
- 回退镜像 `account-pool-balancer:rollback-20261108T065425Z`（原 9f375ef）。
  备份 `/home/ubuntu/backups/account-pool-balancer/20261108T065425Z-mistral-ui-8b701f0/`
  （0700）保留 compose 与原 .env。
- 管理页面仍只监听 `127.0.0.1:8790`，需 SSH 隧道访问，未新增公网入口。
- Playwright Chromium 与系统库已装进镜像（`/opt/ms-playwright`），但容器内
  尚未实跑注册（未配置代理池环境变量）；真实注册验证仍缺。

## 当前部署：Mistral DeepSeek Chat 参数定向兼容（2026-11-08 06:01 UTC，9f375ef）

- 用户明确批准在新工作区独立实现、修复后部署且不再启动代理。功能提交`96eaad7`，集成版本`9f375efee5a89e92dcbe5f14dd0385e4519c1501`；从Orca默认基线319c751（包含8d41516）开发，fetch后正常合入master的e06a589独立工具/诊断更新，再非强制原子推送功能分支与master。没有在原master开发、reset分叉或运行注册工具。
- 仅Mistral Chat精确`deepseek-v4-1-flash-1017`新增developer→system、thinking/enable_thinking到原生effort（enabled无档位/null默认high、disabled为none，冲突/预算/未知结构本地拒绝）、seed整数无损映射random_seed、空默认窄省略及兼容标记处理。保留medium→high、reasoning_content、逐事件流式内容数组及store无状态保护，不改其他模型/Responses/FIM/供应商的新增参数行为、identity优先级、Nginx/调度/TTL/并发/限流/账号策略。
- root user/safety_identifier仅string/null兼容标记，不提供上游追踪/安全语义，不用于会话身份；有效但无法保真的logit_bias/verbosity/retention本地明确拒绝。prompt_cache_key/metadata原值保留。官方Chat schema支持random_seed，但本DeepSeek型号的支持及确定性仍未生产验证；不以mock冒充证据、不在失败时删seed重放。
- 全部Mistral定向测试及相关既有回归通过（2.739s），小幅自审后新增6组参数测试通过（0.584s），gofmt/diff check/必要build通过；未重复全仓/race/vet或主动生产推理。构建源码为干净git archive：`/home/ubuntu/project/account-pool-balancer-releases/20261108T055905Z-9f375ef`，镜像`account-pool-balancer:release-9f375ef-20261108T055905Z`，ID`sha256:6716715f22121e95eb877f96d930a509f7c3afd6490275e5cee2d0404c40e39d`。
- 一次预检8d41516健康/零重启、旧e6f063b工作树干净、磁盘287GiB余量；复用既有`.analysis/th-maintenance/deploy-checkout.py`仅内存适配版本/备份名，不新建部署脚本或复制敏感预检数据。本轮无存储格式变化。新镜像就绪且无queued/running注册任务才停旧进程，退出0后完整一致备份data，gzip/SHA256核验后只追加image覆盖切换Balancer。**161账号、186注册记录核心资产及凭据/Key/代理/支付/备注、routingSecret、完整config与原用户/环境/挂载/端口/网络/资源/健康策略均保留。**
- 本机健康/版本/四资源通过，healthy、零重启、无OOM；真实未登录公网登录页正常，healthz与四资源均200，保留`?v=e02f22dc28`且SHA256逐项匹配。06:01:15.204 UTC公网验收通过并立即反馈；不把服务/资源验收当成真实推理验证。
- **初步自然业务观察（非完整10分钟）**：06:00:34.988–06:04:33.034 UTC约3分58秒，真实DeepSeek完成24/24 success/HTTP200，全流式；NewAPI对应24条消费，全部stream_status=ok/end_reason=done。Mistral错误事件0、无新422/本地400、无忙溢出/请求内切号/多attempt。真实input3195540/cached2119808，token加权66.34%；绑定16hit/8miss，均message_hmac身份/session_hmac cache hint。重启清内存绑定，不能把miss当成同会话一直换号；未恢复客户端原始身份字段，不能声称缓存已改善或所有新参数均已生产实测。
- **用户要求的完整10分钟错误率观察**：06:14:51.458–06:24:51.458 UTC（北京时间14:14:51–14:24:51），按完成时间统计245请求，228成功/17失败，错误率6.94%，客户端取消0。其中Mistral精确DeepSeek **60/60成功、错误率0%**（57流式+3非流式），无新增400/422或流式错误；NewAPI渠道180一致60条消费、57条stream_status=ok/end_reason=done、无错误记录。OpenCode Go96/96成功；全部17次最终失败为Modal上游429（89请求中19.10%），尝试级错误事件42条不能当42个失败请求。Mistral真实input10055835/cached8382592，token加权83.36%，绑定54hit/6miss，无忙溢出/请求内切号/多attempt；不据此承诺缓存改善。38次容器健康采样均无异常，结束时两服务healthy/零重启/无OOM，仍运行9f375ef。仅自然流量只读监控，无主动推理、配置/规则写入或重启；详细口径见`.trellis/tasks/mistral-request-compat/validation.md`。
- **NewAPI渠道180规则暂未修改**：上线后读取最新配置发现它同时服务两个DeepSeek别名与`glm-5.3`/`glm-5.3-flash`（映射zai-glm-5-3/zai-glm-5-3-flash）。直接删除10项全渠道operations会影响未适配的GLM及其他路径，超出精确范围。先保留原规则/header通配/路由/Key与正式历史备份；待确认并核实运行版本可可靠按模型+Chat条件收敛后，才备份最新值、逐项停用目标范围的对应操作，不整行覆盖或清空规则。现有规则仍会删cache/metadata及绕过store=true保护，此限制尚未解除。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 05:59:51.182–06:00:00.435（9.25秒） |
| 请求停止 / 旧进程退出 | 06:00:00.526 / 06:00:00.640 |
| 完整一致备份及核验 | 06:00:00.921–06:00:04.312 |
| 切换请求 / 新容器启动 | 06:00:04.312 / 06:00:04.517 |
| 首次HTTP可达 / Docker healthy | 06:00:05.643 / 06:00:34.883 |
| 本机验收 / 公网验收 | 06:00:34.988 / 06:01:15.204 |

未采样精确停机；旧退出至首HTTP可达观测间隔约5.0秒，不将整体执行或健康等待误报为停机。受限正式备份`/home/ubuntu/backups/account-pool-balancer/20261108T055905Z-mistral-params-9f375ef/`保留完整data、原Compose/.env、构建/切换日志与deploy.json；data SHA256`1ade70b278d8a756293d09aac6cb87bba9fee93513492612d61f980dcaa60719`。回退镜像`account-pool-balancer:rollback-20261108T055905Z`保留8d41516；回退前核查新写入，不用旧data覆盖在线变化。临时产物登记当前工作区`_tmp_artifacts.md`，未经同意不清理；Trellis/正式备份保留。仅补记录不再次部署。

## 历史部署：Mistral DeepSeek 无状态 store 兼容（2026-11-07 21:23 UTC，8d41516）

- 用户要求观察中发现错误则修复、部署并重新观察10分钟。02c61a8窗口内已有真实流式DONE/completed且缓存44416/44816（99.11%），同时另一请求出现上游422 `extra_forbidden / body.store`。`8d41516407abe2f560b5d0f832e058019c7b9877`已正常推送master：仅精确DeepSeek Chat模型省略root store=false/null，true/非法/重复store本地拒绝，保留nested metadata.store；不假装支持持久化，不承诺上游平台的数据留存政策。保留此前medium→high及逐帧内容块兼容，不改账号调度或NewAPI配置。
- 只跑Mistral定向Mock测试、gofmt和必要build，未重复全仓/race/vet或主动生产推理。一次预检原02c61a8健康，旧e6f063b工作树干净、余量286.5GiB；源码为干净git archive：`/home/ubuntu/project/account-pool-balancer-releases/20261107T212114Z-8d41516`，镜像`account-pool-balancer:release-8d41516-20261107T212114Z`，ID`sha256:66ddacf96e36ce5ce1b68ed340b6606eb65fac084e65811f3ca418541ca088ef`。
- 复用既有脚本，仅内存适配；无存储格式变化，不额外复制敏感数据预检。新镜像就绪且无queued/running注册任务才停止旧服务，退出0后完整一致备份data并gzip/SHA256核验，仅image覆盖更新Balancer。**161账号、186注册记录核心资产及凭据/Key/代理/支付/备注、routingSecret、完整config和原用户/环境/挂载/端口/网络/资源/健康策略均保留**，未改在线功能开关。
- 本机healthz/版本/四资源通过，healthy/零重启/无OOM；真实未登录公网登录页正常，healthz/四资源均200，保留`?v=e02f22dc28`资源SHA256逐项匹配；21:23:09.909 UTC公网验收通过。上线后已立即反馈；专用浏览器关闭，最终10分钟自然业务观察重新计时为21:23:09.909–21:33:09.909 UTC，不将中断的旧观察冒充完整窗口。
- **完整10分钟观察完成**：约每15秒轮询（40次），两服务始终healthy/零重启/无OOM；NewAPI与Balancer一致确认Mistral10次成功，其中6次流式正常DONE/completed、4次非流式，零Mistral错误，未再见content数组解析/medium/store错误。结束后只读SQL复核计数一致，没有额外推理或再次部署。
- 缓存为真实input912330/cached124288，token加权约13.62%，2/10请求有缓存、3/10绑定命中。此前99.11%是单次请求，不是全池命中率；**整体缓存仍偏低，不能宣称已整体改善或保证命中**。缺少稳定显式会话ID、新绑定/不同前缀/上游缓存路由仍需区分，不改调度或删除提示词来虚增数字。
- 同窗口Modal有11次最终上游429（28条上游429尝试）、15次成功；OpenCode25次成功（NewAPI19条流式正常DONE/6条非流式）。另有独立cctoken渠道164的一条response.failed及cline-pass-console渠道71两条错误，不经过本Balancer；不将它们掩盖成零错误或通过无关重启/放宽限流处理。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 21:21:40.460–21:21:47.290（6.83秒） |
| 请求停止 / 旧进程退出 | 21:21:47.391 / 21:21:47.494 |
| 完整一致备份及核验 | 21:21:47.762–21:21:51.098 |
| 切换请求 / 新容器启动 | 21:21:51.098 / 21:21:51.416 |
| 首次HTTP可达 / Docker healthy | 21:21:52.556 / 21:22:21.801 |
| 本机验收 / 公网验收 | 21:22:21.911 / 21:23:09.909 |

未采样精确停机；旧退出至首HTTP可达观测间隔约5.1秒。受限正式备份`/home/ubuntu/backups/account-pool-balancer/20261107T212114Z-stateless-8d41516/`（0700/0600）保留data、原Compose/.env、构建/切换日志及回执，data SHA256`d6e2a6fe600c5dd36cb56cf8ee717be5d5cd3ad963b3cfc6a3a47ee448e40c74`。回退镜像`account-pool-balancer:rollback-20261107T212114Z`保留原02c61a8，回退前先核查新写入，不用旧data覆盖在线变化。无新增临时部署脚本/预检副本/容器或凭据下载；只补记录不再次部署。

## 历史部署：Mistral DeepSeek medium 档位兼容（2026-11-07 21:16 UTC，02c61a8）

- 用户授权解决后部署、再观察10分钟；`02c61a80f6a699e432f0a541581a98d81bed3532`已正常推送master。确定性错误为DeepSeek Chat上游400/3051拒绝medium：仅精确模型`deepseek-v4-1-flash-1017`将medium映射到high，已披露可能增加思考token；不降级/关闭思考，不改其他模型/档位/协议，不声称能保证缓存命中。保留a2ffae2流式内容块兼容。
- 只复用/运行Mistral定向Mock测试及必要build，不重复全仓/race/vet、不主动生产推理。一次预检原a2ffae2健康，旧e6f063b工作树干净，磁盘余量286.7GiB。从干净git archive构建源码`/home/ubuntu/project/account-pool-balancer-releases/20261107T211431Z-02c61a8`；镜像`account-pool-balancer:release-02c61a8-20261107T211431Z`，ID`sha256:28004aa53424a316e8339d29db3218b4f6fb850bbc963f5605538c872676cdd4`。
- 复用既有脚本，仅内存适配；无存储格式变化，不额外复制敏感数据预检。新镜像就绪且无queued/running注册任务后停止旧服务，退出0后完整一致备份、gzip/SHA256核验，只追加image覆盖更新Balancer。**161账号、186注册记录核心资产及凭据/Key/代理/支付/备注、routingSecret保留，完整config逐字节不变，原用户/环境/挂载/端口/网络/资源及健康策略不变。** 未改NewAPI配置/在线功能开关。
- 本机健康/版本/四资源通过，healthy/零重启/无OOM；真实未登录公网浏览器登录页正常，healthz/四资源均200，`?v=e02f22dc28`资源SHA256逐项匹配；21:16:07.061 UTC验收通过，已立即反馈并开始新10分钟自然业务观察。专用浏览器关闭，观察结果另记下方及Trellis会话；不将Mock/静态验收当推理验证。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 21:14:54.949–21:15:01.847（6.90秒） |
| 请求停止 / 旧进程退出 | 21:15:01.948 / 21:15:02.120 |
| 完整一致备份及核验 | 21:15:02.403–21:15:05.753 |
| 切换请求 / 新容器启动 | 21:15:05.753 / 21:15:05.972 |
| 首次HTTP可达 / Docker healthy | 21:15:07.088 / 21:15:36.329 |
| 本机验收 / 公网验收 | 21:15:36.436 / 21:16:07.061 |

未采样精确停机；旧进程退出至首次HTTP可达观测间隔约5.0秒。受限正式备份`/home/ubuntu/backups/account-pool-balancer/20261107T211431Z-effort-02c61a8/`（0700/0600）保留data、原Compose/.env、构建/切换日志及回执，data SHA256`8acff9f88965271c1bec8ae21d0a5ba3598a0c99087d47e7500cdfe55d1d2414`。回退镜像`account-pool-balancer:rollback-20261107T211431Z`保留原a2ffae2，回退前先核查新写入，不用旧data覆盖在线变化。无新增临时部署脚本/预检副本/容器或凭据下载；只补记录不再次部署。

## 历史部署：Mistral Chat 流式内容块兼容（2026-11-07 20:43 UTC，a2ffae2）

- 用户授权部署且要求不做过多测试；`a2ffae2fd8555122af89d356787844d1d4a809c6`已正常推送`origin/master`。复用本提交已通过的Go normal/race/vet/build及逐帧Flush回归，不重复全量测试、不发起生产推理。只更新Balancer，不改NewAPI渠道或在线配置/功能开关。
- 一次预检确认原918352d健康、restartCount=0、无OOM，旧工作树仍e6f063b/干净，磁盘充足。从干净`git archive`构建到`/home/ubuntu/project/account-pool-balancer-releases/20261107T204139Z-a2ffae2`，未在旧树构建/reset。镜像`account-pool-balancer:release-a2ffae2-20261107T204139Z`，ID`sha256:fdeecdc05694a4b14f9e2871dba3de6ee6353b0a425758b06a66256b8ce37a69`。
- 复用既有部署脚本，仅内存适配固定版本及备份名称。无存储格式变化，不额外复制敏感数据做断网预检；新镜像就绪并确认无queued/running注册任务后才停止旧服务，退出0后完整一致备份data，通过gzip/SHA256核验，仅追加image覆盖更新目标服务。**157个账号、186条注册记录及凭据/Key/代理/支付记录/备注、routingSecret保留，config逐字节不变，原UID1000:1000/环境/卷/端口/网络/资源及健康策略不变。**
- healthy、restartCount=0、无OOM、运行revision正确；本机healthz和四资源通过。真实未登录公网浏览器`https://ods.yeschoy.io/`登录页正常，healthz及四资源HTTP200，保留`?v=e02f22dc28`查询串且SHA256逐项匹配目标源码；20:43:22.287 UTC验收通过并立即反馈。验收时切换后尚无新的渠道180自然业务记录，不将服务验收宣称为真实Mistral推理修复验收；未主动测试推理或回改旧错误日志。专用浏览器已关闭。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 20:41:58.198–20:42:06.169（7.97秒） |
| 请求停止 / 旧进程退出 | 20:42:06.268 / 20:42:06.370 |
| 完整一致备份及核验 | 20:42:06.621–20:42:09.996 |
| 切换请求 / 新容器启动 | 20:42:09.997 / 20:42:10.215 |
| 首次HTTP可达 / Docker healthy | 20:42:11.315 / 20:42:40.548 |
| 本机验收 / 公网验收 | 20:42:40.646 / 20:43:22.287 |

未采样精确不可用窗口；旧进程退出至首次HTTP可达观测间隔约4.9秒，不将健康等待或整体操作时长当停机。受限正式备份`/home/ubuntu/backups/account-pool-balancer/20261107T204139Z-mistral-stream-a2ffae2/`（目录0700/文件0600）保留data、原Compose/.env、构建/切换日志及验收回执；data SHA256`d2e42292236abf70cc11b74cd206c8578dddd5a9d9e6efc1e49ab78985d4ab0c`。回退镜像`account-pool-balancer:rollback-20261107T204139Z`保留原918352d；回退前先核查新写入，不自动覆盖在线数据。本轮无新增临时脚本/数据副本/容器或凭据下载；只补部署记录，不重新构建或重启。

## 历史部署：master 合并版（2026-11-07 20:19 UTC，918352d）

- 用户授权推送并部署；`918352d5d91b661699ffdb09ad7f5401910b998b` 已正常推送 `origin/master`，从干净 `git archive` 构建。包含 Mistral reasoning_content 原生兼容、Chat/Responses 会话缓存key、管理员完整Key/代理展示复制、TokenHarbor独立默认关闭的详情日志开关，以及master已合入的OpenDesign/TLS/注册功能。
- 复用既有部署脚本，仅内存适配；本提交定向验证已通过，不重复全量测试。相对线上8d852b6没有注册库存储格式变化，不额外复制敏感数据做离线预检；未修改远端e6f063b旧分叉工作树，未新建Trellis任务。
- 源码 `/home/ubuntu/project/account-pool-balancer-releases/20261107T201720Z-918352d`；镜像 `account-pool-balancer:release-918352d-20261107T201720Z`，ID `sha256:4b7c902e5eb2098428d9d4ed550da3c7f6c05d73288f409ae0094c97bb5dccc3`。
- 新镜像就绪且确认无queued/running注册任务后才停止旧服务；退出0后完整一致备份data并通过gzip/SHA256核验，仅追加image覆盖更新目标服务。157个账号、186条注册记录及凭据/Key/代理/支付记录/备注、routingSecret完整保留；config逐字节一致，原用户/环境/卷/端口/网络/健康/资源参数一致，不改在线配置或功能开关，不触发注册、付款或真实推理。
- healthy、restartCount=0、未OOM，运行revision正确；本机healthz/四资源通过。真实未登录公网浏览器 `https://ods.yeschoy.io/` 登录页正常，healthz及四资源均200，保留`?v=e02f22dc28`查询串，SHA256逐项匹配目标源码；20:19:03.419 UTC验收通过并立即反馈。只补记录，不重建/重启。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 20:17:45.682–20:17:54.122（8.44秒） |
| 请求停止 / 旧进程退出 | 20:17:54.224 / 20:17:54.331 |
| 完整一致备份及核验 | 20:17:54.585–20:17:57.922 |
| 切换请求 / 新容器启动 | 20:17:57.922 / 20:17:58.125 |
| 首次HTTP可达 / Docker healthy | 20:17:59.231 / 20:18:28.469 |
| 本机验收 / 公网验收 | 20:18:28.565 / 20:19:03.419 |

未采样精确不可用窗口；旧进程退出至首次HTTP可达观测间隔约4.9秒，不把Docker健康等待或整体操作时长当作停机。受限备份 `/home/ubuntu/backups/account-pool-balancer/20261107T201720Z-master-918352d/` 保留data、校验、原Compose/.env、构建/切换日志及deploy.json，目录0700、文件0600；data SHA256 `6fa59b8a01a2ef2faf08fc47bdc2acdef42a1f0d08af67865369e35593244ac1`。回退镜像 `account-pool-balancer:rollback-20261107T201720Z`（原8d852b6）保留，回退前核查新写入，不自动覆盖在线数据。本轮无新增临时脚本/预检副本/容器或凭据下载，既有产物未经同意不清理。

## Mistral 号池部署（2026-11-07 12:38 UTC，8d852b6）

- 用户授权提交、推送并部署。业务提交 `8d852b65ecaaf91bce17f197065931edbf77073e` 已正常快进推送 `origin/master`，从该提交的干净 `git archive` 构建；未新建 Trellis 任务，不改远端旧分叉工作树。
- 从运行中的 `e9a3669` 升级，发布源码 `/home/ubuntu/project/account-pool-balancer-releases/20261107T123452Z-8d852b6`；镜像 `account-pool-balancer:release-8d852b6-20261107T123452Z`，ID `sha256:71fb3c1571394f3ecd75a3545b5a36fadc59452a67396cd4c052dfdcee8181ea`。构建约6.79秒，复用本提交已通过的全仓 normal/race/vet/build、JS 和本地浏览器验收；本轮只有原生 Key 号池增量，无注册库存储格式变更，不额外复制敏感数据做预检。
- 切换前远端内存解密核实无 queued/running 注册任务；新镜像就绪后才停止旧进程，退出0后完整一致备份 data，gzip完整性与SHA256通过，再复用原Compose文件链追加仅image覆盖，更新目标服务。
- **151账号、10把下游密钥、2把导入密钥、186条注册记录及其凭据/Key/固定代理/支付记录、保存代理池、5份任务备注及routingSecret保留**。配置文件逐字节一致；允许Cookie/套餐观察/更新时间正常刷新，注册核心资产在远端内存HMAC核对。UID1000:1000、环境、挂载、端口、网络及别名、健康/重启策略和资源参数均保持原状（Memory仍0）。Mistral账号仍0，不将用户测试Key入池，不修改在线设置或自动启用功能，不主动注册、付款或生产推理。
- 容器 `2770641a532b` healthy、restartCount=0、未OOM，运行image/revision正确。本机healthz及四静态资源通过；真实未登录公网浏览器 `https://ods.yeschoy.io/` 的healthz与四资源均200，保留 `?v=86a7b466e3` 查询串，SHA256与目标源码逐项相同；HTML已包含Mistral标签和独立设置。12:38:25 UTC完成公网核验，已立即反馈上线，本文档随后补记，不再次部署。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 12:35:45.712–12:35:52.501 |
| 请求停止 / 旧进程退出 | 12:37:14.560 / 12:37:14.678 |
| 完整一致备份及核验 | 12:37:15.060–12:37:18.333 |
| 切换请求 / 新容器启动 | 12:37:18.333 / 12:37:18.587 |
| 首次HTTP健康可达 / Docker healthy确认 | 12:37:18.963 / 12:37:49.271 |
| 本机验收 / 公网验收 | 12:37:49.407 / 12:38:25.026 |

未采样精确不可用窗口；旧进程退出至首次HTTP健康可达间隔约4.3秒，不将Docker健康等待或整个交付时长误报为停机。
受限正式备份 `/home/ubuntu/backups/account-pool-balancer/20261107T123452Z-mistral-8d852b6/` 保留完整data、校验、原Compose/.env、构建/切换日志和deploy.json，目录0700、文件0600。data SHA256：`3c152404c3859cde0979171d36dc409036e729fee5f5d77f870e22b2dd8df545`。
回退镜像 `account-pool-balancer:rollback-20261107T123452Z` 保留（原e9a3669）；之后若导入Mistral账号，旧版不识别新provider，回退前必须核查新写入及配置兼容，不用旧整份data覆盖在线变化。正式备份、源码、新旧镜像保留；临时产物登记 `_tmp_artifacts.md`，未经同意不清理。

## 邮箱验证等待调整（2026-11-06 13:30 UTC）

- 按用户要求，通过正常管理员设置API（CSRF + expectedRevision）将线上
  `tokenHarbor.mailTimeoutSec` 从0（引擎回退3分钟）设为120秒；API及磁盘均确认生效，
  revision由5217变为5218。轮询仍是每次邮箱查询完成后等待5秒；新启动任务使用2分钟，
  已启动任务不打断、不重放。
- 设置接口同时按既有固定单并发规则将存储的registerConcurrency从0规范化为1，
  实际并发前后均为1；除这项规范化、等待时长及revision外，完整配置一致。
- 未构建、部署或重启服务，healthz HTTP200、容器healthy、restartCount=0。
  未触发注册、验证码、创建Key、付款、入池或推理；操作会话已正常退出。
- 修改前config及change.json保留于受限业务备份
  `/home/ubuntu/backups/account-pool-balancer/20261106T133030Z-th-mail-120/`，
  目录0700、文件0600，不下载、不入Git，不作为临时垃圾清理。回退只通过设置API恢复
  mailTimeoutSec，不能用旧整份config覆盖后续在线变更。13:36:51 UTC复核通过。
  本节仅补记录，不再次部署。

## 当前部署：远端最新 master（2026-11-07 10:08 UTC，e9a3669）

- 用户授权部署 OVH 最新远端 `master`；fetch 及构建前核对锁定
  `e9a3669ddbfa3a87d0221edba396e6ac608d7273`。从干净 `git archive` 构建，
  不夹带本地其他窗口的未提交改动，不修改/reset 远端 e6f063b 旧分叉工作树，
  不新建 Trellis 任务。复用该提交已通过的完整 Go/Node/假数据浏览器验收，未重复跑全量测试。
- 上线稳定浏览器身份配置、可配置 1–5 注册并发（全局上限5）、注册完成顺序及任务排序。
  完整线上配置逐字节不变，原注册默认并发1、邮箱等待120秒均保留，未主动创建任务、
  注册、发信、解验证码、创建 Key/支付链接、付款、入池或推理。
- 干净源码 `/home/ubuntu/project/account-pool-balancer-releases/20261107T100552Z-e9a3669/`；
  固定镜像 `account-pool-balancer:release-e9a3669-20261107T100552Z`，镜像 ID
  `sha256:723b9b011c293623193a367be8dcacd07dcf27b17e4a5885fe91291579ef197a`。
  仅在原 Compose 叠加 image override 切换目标服务；用户、环境、挂载、端口、网络别名、
  健康/重启策略和实际资源限制一致，未应用源码 Compose 的其他默认值。
- 新注册可选字段首次升级，按线上 UID1000:1000、`--network none` 使用数据副本预检；
  healthz、正常管理员登录后的186行注册列表核验通过，正常退出0，不访问任何外部上游。
  新镜像就绪后才停止旧服务；旧进程退出0后完成完整一致 data 备份及 gzip/SHA-256 核验。
  **151个账号、186条注册记录的凭据/Key/固定代理/支付记录/备注、保存代理池、
  routingSecret 及完整配置均保留**；敏感比对仅在远端内存执行，允许正常 Cookie/套餐观察更新时间刷新。
- 新容器 healthy、restartCount=0、revision 正确，本机 healthz 及 HTML 中四项资源验收通过。
  真实未登录公网浏览器 `https://ods.yeschoy.io/` 登录页正常，healthz/四资源均HTTP200，
  保留 `?v=8411c592f3` 的资源哈希与目标源码逐一相符；10:08:14.709 UTC 完成公网验收并立即反馈。
  未登录生产管理员做业务写入，不把健康/资源验收当作上游真实注册或付款验收。只补记录，不再次重启。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 10:07:15.378–10:07:22.138（6.760秒） |
| 断网副本预检 | 10:07:22.138–10:07:25.100（2.962秒） |
| 请求停止 / 旧进程退出 | 10:07:25.224 / 10:07:25.343 |
| 完整一致备份及校验 | 10:07:25.634–10:07:28.924（3.290秒） |
| 切换请求 / 新容器启动 | 10:07:28.924 / 10:07:29.135 |
| 首次HTTP健康可达 / Docker healthy | 10:07:30.235 / 10:07:59.475 |
| 本机验收 / 公网验收 | 10:07:59.602 / 10:08:14.709 |

未采样精确不可用窗口，不将约30秒Docker健康等待或整个操作时长误报为停机。
受限备份 `/home/ubuntu/backups/account-pool-balancer/20261107T100552Z-master-e9a3669/`
含完整 data.tar.gz/校验、原 Compose/.env、构建/切换日志和 deploy.json；目录0700、
敏感文件0600，不下载、不入Git。data SHA-256：
`05c9a462c11d89532a34b32901b6b4c89e4b69f876330dd5228f27fde3688ce4`。
回退镜像 `account-pool-balancer:rollback-20261107T100552Z`（原a8902bc）保留；
新版可选注册字段可能被旧版丢弃，降级前先核查新写入和兼容方案，不用旧整份data覆盖在线变化。
已停止的预检容器 `account-pool-balancer-preflight-20261107t100552z`、备份内
`preflight-data/` 和 `preflight.log` 已登记 `_tmp_artifacts.md`，待用户同意再定向清理；
正式备份/源码/新旧镜像及 Trellis 记录保留。公网独立浏览器已关闭。

### 上线后 NewAPI 只读排查（2026-11-07 10:24 UTC）

- 用户要求检查 NewAPI 报错、若由新版引起则修复。合并核对近2小时 NewAPI Docker日志、
  只读 PostgreSQL 使用/错误记录、渠道路由及号池 JSONL 业务日志，未发起真实推理或写入配置。
  NewAPI 自11月3日起运行原镜像，本次只升级号池；两服务均healthy、restartCount=0、未OOM。
  按正常域名 `Host: yeschoy.com` 访问本机 NewAPI `/api/status` HTTP200/success=true；
  无该Host的404是现有域名路由行为，不作为服务故障。
- Modal渠道156和OpenCode渠道167均有上游429，升级前就存在；升级后15分钟分别有37/44条
  NewAPI成功消费记录，也分别有19/8条限流错误。客户端取消对应client_gone/499，
  固定客户端的Invalid token同样早于升级，不据此修改凭据、限流或调度保护。
- 10:16:29 UTC渠道134的单条503，在号池错误记录中确认为OpenDesign上游
  `workspace_billing_scope_unavailable`（账单服务内部请求超时）；未发现本地panic或持久化失败。
  渠道158的两条零token计费告警来自独立外部上游，不经过号池；旧TokenHarbor认证错误
  集中在升级前08:41–09:12 UTC，渠道170核查时已是停用状态，本轮未更改。
- 排查期间10:23起渠道174–179出现422；这些是直连Mistral官方的渠道，不经过号池，
  不能归因本次e9a3669。另有一条OpenCode400明确拒绝file内容块，此类请求须使用其支持的内容格式，
  不在后台丢弃附件来掩盖错误；本轮未修改这些渠道或用户请求。
- 切换窗口10:07:26–10:07:28 UTC存在访问旧兼容别名的Docker DNS错误，启动后截至10:24:21 UTC
  未见继续发生。这是切换期间请求失败证据，不代表持续故障，也不是精确停机采样。
- 当前没有证据表明e9a3669引入上述持续错误，未盲目修代码、回滚、重启、覆盖配置或调整计费。
  本轮探针/查询均在内存执行，无新增临时脚本、日志下载、凭据副本或浏览器产物。

## 历史部署：Stripe 链接路径兼容（2026-11-06 21:37 UTC，a8902bc）

- 用户同意上线安全诊断并用一个未尝试账号验证。先上线`9f6ab11`取得真实响应结构，再修复前后端均写死`/c/pay/`的问题，最终运行`a8902bc6069b4795c2c08d7864e3638ee9e0aef9`。Stripe官方[Session.url合同](https://docs.stripe.com/api/checkout/sessions/object#checkout_session_object-url)不保证路径前缀；现在只接受HTTPS、精确checkout.stripe.com、无userinfo/显式端口/空白且非空根地址，路径/query/fragment按不透明值处理。不扩展自定义域名或invoice_url，不更改报价、单次月付、禁止扣款/升级、pending保护及保存链接优先复用规则。
- 干净git archive源码：`/home/ubuntu/project/account-pool-balancer-releases/20261106T213621Z-a8902bc`；未夹带两份原有HTML/JS测试草稿或未跟踪目录。旧工作树仍e6f063b/干净/分叉，未在旧树构建、reset或覆盖配置。
- 镜像`account-pool-balancer:release-a8902bc-20261106T213621Z`，ID`sha256:73c0301dd57e2a59ed45ee95a5ffda0bdb3f288701b7f8d40dc4a9759c844f26`。复用下节刚完成的同格式断网预检，不重复复制敏感数据；构建就绪后才停服务并做完整一致data备份。仅通过release内image override切换原Compose目标服务，UID1000:1000、环境、卷/端口/网络别名、健康/重启策略和实际资源保持原状；线上Memory限制原本为0，不套用本地新Compose的8GiB。
- **78账号、92注册记录及其凭据/Key/固定代理/支付记录/备注、保存代理池、routingSecret和完整配置均保留**。允许后台正常更新Cookie/套餐观察/更新时间，其他资产用远端内存HMAC比对；配置逐字节一致，日志保留仍30天，未擅自启用功能。
- 全量Go normal/race/vet/build、三套Node行为测试及本地假数据浏览器通过：模拟非`/c/pay/`链接一次创建、加密保存、显式读取和前端展示成功，mock预检1/创建1，另一pending不变、未入池、核账框未勾选。**以上是本地验收。随后用户再次明确授权单账号实测：21:45:55 UTC使用无既有checkout的已验证Free账号`fac***@icloud.com`（`thr_b24e4eab89cf71a7070b6953`，任务`that_ace0a9355f17cf11dbd84322`），在OVH运行a8902bc的正常管理员/CSRF接口上通过原固定代理创建一次，2.542秒返回HTTP200。非`/c/pay/`的HTTPS checkout.stripe.com链接已加密保存，显式GET读回一致；账号仍registered、无pending/failure，原30份checkout记录及完整配置不变。未重试旧订单、提交付款或启用自动续费；管理员正常退出。验证范围是生成、保存及读取，不包含Stripe页面渲染或付款完成。**
- Docker healthy、restartCount=0，healthz及四静态资源本机校验通过。真实未登录公网浏览器`https://ods.yeschoy.io/`的healthz/四资源均200，保留?v查询串，SHA256与a8902bc逐一相符；21:38:11 UTC完成核对。上线及限制已立即反馈，本文档随后补记，不再次重启。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 21:36:33.410–21:36:40.174（6.764秒） |
| 请求停止 / 旧进程退出 | 21:36:40.275 / 21:36:40.370 |
| 完整一致备份及gzip/SHA256核验 | 21:36:40.639–21:36:41.782 |
| 切换请求 / 新容器启动 | 21:36:41.782 / 21:36:41.972 |
| 首次HTTP健康可达 / Docker healthy | 21:36:43.078 / 21:37:12.309 |
| 本机验收 / 公网验收 | 21:37:12.411 / 21:38:11 |

未采样精确不可用窗口，不把约30秒Docker健康等待、开发诊断时间或整个交付时长当作停机。
受限备份`/home/ubuntu/backups/account-pool-balancer/20261106T213621Z-checkout-a8902bc/`含data.tar.gz、校验、原Compose/.env、构建/切换日志及deploy.json，目录0700、敏感文件0600。data SHA256：`8f21d56682876de98f215db884d7834f5538c7c29810113cdd4321a0f54f3710`。回退镜像`account-pool-balancer:rollback-20261106T213621Z`保留；降级前核查新写入，不用旧整份data覆盖在线变化。正式备份/镜像/源码保留，临时预检副本/容器及本地验收文件按`_tmp_artifacts.md`待用户同意清理。

## 支付响应诊断部署与一次实测（2026-11-06 21:09 UTC，9f6ab11）

- 首次从3058850升级至`9f6ab1151363d2d70778853946ac64ec0133e5ae`，含此前已完成的任务备注、默认7天记录级日志保留（保留线上显式30天）、真实周额度/月余额未知展示及只读支付预检。新增诊断只保留固定字段名/JSON类型/总字段数和URL分类，不回显URL标识符、任意键值、Cookie或凭据；HTTP200业务错误也可识别。
- 源码`/home/ubuntu/project/account-pool-balancer-releases/20261106T210853Z-9f6ab11`；镜像`account-pool-balancer:release-9f6ab11-20261106T210853Z`，ID`sha256:ee7162341de53c541db6f7bffec5ff41617eab6fa5e5933f5fe637810e0aa3ff`。
- 新可选元数据首次升级按原UID1000:1000运行断网副本，health及鉴权注册列表92行核验通过；预检容器`account-pool-balancer-checkout-preflight-20261106t210853z`已停止，副本保留在备份内preflight-data。原服务退出0后完整一致备份，再仅切换原Compose镜像；78账号/92记录关键资产、完整配置及运行参数均保留。
- 21:10:30 UTC，按用户授权只选一个无checkout标记的已验证Free账号`ter***@icloud.com`（`thr_d01e72e8b54c5e505082607b`、任务`that_ace0a9355f17cf11dbd84322`），通过正常管理员API/CSRF及其原固定代理提交一次链接创建；报价预检通过。3.948秒后本地API502，上游HTTP200，仅`checkout_url`字符串，HTTPS/checkout.stripe.com、无userinfo/端口/空白，但路径分类other-path，被旧固定前缀校验拒收。**未保留原始URL，不能断言该地址完整有效或收银台已验证；此次至少证明了固定路径造成的拒收点。**
- 创建前pending已持久化，结束后registered、无保存URL，有安全failure诊断；此前全部29份checkout记录保持原样。该账号只读ledger总数0，不代表没有待处理订单。**没有再次创建、换更多账号、核账代确认、地区确认、付款或自动续费操作**；管理员会话正常退出。旧不确定标记均保留，不能盲目重试。
- 构建21:09:04.768–21:09:11.407；停止请求/退出21:09:12.307/12.411；完整备份21:09:12.677–13.815；切换/启动21:09:13.815/14.033；HTTP可达21:09:15.143，Docker healthy21:09:44.388，本机验收21:09:44.503。公网health/四资源200及版本哈希核验亦通过。
- 备份`/home/ubuntu/backups/account-pool-balancer/20261106T210853Z-checkout-9f6ab11/`，data SHA256`897da473e91f86c8b19e7f70f2844cff6e083958ee05955722113bade38609d8`；回退镜像`account-pool-balancer:rollback-20261106T210853Z`（原3058850）保留。凭据仅远端内存/stdin，不下载或写入脚本/日志/Git，未新建Trellis任务。

## 历史部署（2026-11-06 UTC，远程 master：3058850）

- 按用户要求快速部署最新分支；fetch 后本地/远程 `master` 均为
  `3058850ba2aab0dd24dd1256bbd08191530666d6`，从干净 `git archive` 构建，
  未混入未跟踪目录，未修改远端旧分叉工作树，未新建 Trellis 任务。
- 上线 GitHub 五段输入、JSON 接码、授权响应核对及补发验证邮件功能；
  不修改在线配置或功能开关，未主动注册、补发邮件、解验证码、付款或推理。
  注册库存储格式无变化，未重复全量测试或创建敏感预检副本。
- 源码 `/home/ubuntu/project/account-pool-balancer-releases/20261106T154432Z-3058850`；
  固定镜像 `account-pool-balancer:release-3058850-20261106T154432Z`，镜像 ID
  `sha256:1867f7b454aeb8b3a91cc0a0fa7bb33c61a75948bdc72fd41d9e25fd1d783948`。
- 切换前确认无 queued/running 注册任务，旧进程退出0后完整备份 data 并校验 gzip/
  SHA-256；只更新目标服务，用户、环境、挂载、端口、网络别名、健康检查及资源参数不变。
  **78账号、完整配置和密钥、routingSecret、94条注册记录关键资产及保存代理池全部保留**。
  敏感比对仅在远端内存执行，无凭据输出、下载或入 Git。
- 新容器 `ddd956ac316a` healthy、重启0、未OOM，revision 与镜像 ID 正确。
  本机及真实未登录公网浏览器的 healthz、首页、四项资源均200，资源 SHA-256
  与目标源码一致（`?v=375042c90d`）。验收通过已立即反馈，再补记录，不再次重启。

| 阶段 | UTC 时间 / 时长 |
|---|---|
| 构建 | 15:45:54.180–15:46:01.002，6.822秒 |
| 请求停止 / 旧进程退出 | 15:46:01.103 / 15:46:01.191 |
| 完整一致备份及校验 | 15:46:01.508–15:46:02.489，0.982秒 |
| 切换请求 / 新容器启动 | 15:46:02.514 / 15:46:02.801 |
| Docker健康确认 / 本机验收 | 15:46:33.403 / 15:46:33.658 |
| 公网验收 | 15:47:01.868 |

未采样精确不可用窗口，不将约31秒健康等待或整个操作时长视为停机。
受限备份目录 `/home/ubuntu/backups/account-pool-balancer/20261106T154432Z-pre-3058850/`
保留完整 data/校验、Compose/.env、build/switch日志及deploy.json；data SHA-256：
`2c2c32012027fdfa5373070b573b4aeb0f81d7f7e32c2810e91da8674ea73f0e`。
旧镜像 `account-pool-balancer:rollback-20261106T154432Z`（0d32578）保留；
回退前先核查上线后新写入，不能用旧整份 data 覆盖在线业务数据。
本轮直接管道归档、内存执行，无新增临时脚本/压缩包/诊断容器；既有临时产物未经同意不清理。

## 历史部署（2026-11-06 UTC，远程 master：0d32578）

- 按用户要求以 GitHub 远程 `master` 最新提交为准：fetch 后及切换前再次核对均为
  `0d32578d347a5f43dcaf6590905b1e3a6dcf914f`。从该提交干净 `git archive` 构建，
  不包含本地未跟踪目录，不修改/reset远程旧分叉工作树，不新建Trellis任务。
- 上线取消本地60秒注册提交间隔、保留固定单并发，以及客户端身份一致性修复。
  不修改在线配置或功能开关，`egressRegion` 继续留空，不擅自选择出口地区。
  无注册库存储格式变化，复用已通过的测试，不额外复制敏感数据做断网预检。
- 源码 `/home/ubuntu/project/account-pool-balancer-releases/20261106T120400Z-0d32578`；
  固定镜像 `account-pool-balancer:release-0d32578-20261106T120400Z`，镜像ID
  `sha256:6a98a27c7ab3c6d18ae9f7e88f37f95a7c34290f0b41ad910257f24a1872b088`。
  镜像同时设置 `org.opencontainers.image.revision` 和 `revision` 标签。
- 切换前确认无queued/running注册任务；旧进程正常退出0后完整备份data，通过gzip及
  SHA-256校验，再复用原Compose仅切换目标服务。用户、环境、挂载、端口、网络别名、
  健康检查、重启策略及资源参数核对保持一致。
- **78账号、完整配置和密钥、routingSecret、93条注册记录关键资产及保存代理池均保留**。
  注册库仅在远端内存解密比对，允许Cookie/套餐观察/更新时间正常刷新；不输出或下载凭据。
- 新容器 `45acd8f4f7bf` healthy、restartCount=0、未OOM，运行revision和镜像ID正确。
  本机及真实未登录公网浏览器healthz均HTTP200，HTML四项资源均200且SHA-256与源码一致
  （`?v=cc3f2e3eb3`）；取消间隔文案及地区控件已确认。初次验收脚本误用不存在的
  `th-set-egress-region`，按目标源码实际 `th-set-region` 修正后通过，未再次构建/重启。
  验收通过立即反馈上线，再补本文档；未主动注册、解验证码、付款或推理。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 12:03:16.653–12:03:23.165，6.512秒 |
| 请求停止 / 旧进程退出 | 12:04:33.963 / 12:04:34.311 |
| 完整一致备份及校验 | 12:04:34.383–12:04:36.720，2.337秒 |
| 切换请求 / 新容器启动 | 12:04:36.751 / 12:04:36.965 |
| Docker健康确认 | 12:05:07.553 |
| 公网验收 | 12:06:42.000 |
| 最终数据与本机验收登记 | 12:07:29.224 |

整体超过5分钟已说明预检/远程版本核对及验收卡点；构建仅6.512秒。
未采样精确不可用窗口，不将约31秒健康等待或整个交付时长当作停机。

### 备份、回退与临时产物

- 受限目录 `/home/ubuntu/backups/account-pool-balancer/20261106T120400Z-pre-0d32578`，
  含完整data/校验、Compose/.env、build/switch日志及deploy.json；目录0700、敏感文件0600，
  不下载、不入Git。data SHA-256：
  `3cdb4c4694cc6a3626dc9365045389bf3bf0fe55c2a0c92b0aa6e72a1cbae6a6`。
- 旧镜像 `account-pool-balancer:rollback-20261106T120400Z`（c88bc2a）保留。
  回退前核查上线后的配置/账号/注册记录新写入；不整体恢复旧data覆盖在线业务数据。
- 本轮无新增临时部署脚本、源码压缩包、诊断容器或敏感预检副本；独立公网浏览器已关闭。
  正式备份、源码、新旧镜像及部署业务记录保留；既有临时产物未经同意不清理。

## 历史部署（2026-11-06 UTC，master：c88bc2a）

- 用户授权推送并部署；`master` 已推送 `c88bc2ac443ae0eef1e65b0fa9e27f2c2c2c69b2`。
- 本次上线两个提交：`6c69145`（登录不匹配恢复与 OAuth 失败说明）与
  `c88bc2a`（每账号独立一致的客户端身份）。后者新增 `internal/egressid`，
  修复号池与注册机多处未设置 User-Agent、从而以 `Go-http-client/1.1` 出网的问题，
  并让注册机的 UA 按账号确定性随机、同时补齐与之自洽的 `sec-ch-ua*` 与
  `accept-language`；邮箱与验证码读取也复用同一账号种子。
- 从干净 `git archive` 快照构建，源码目录
  `/home/ubuntu/project/account-pool-balancer-releases/20261106T094136Z-c88bc2a`，
  快照 SHA-256 `e1d5e66fda925bb7daf44880879a886dc476eff58da8be59651a7a331e8d7739`；
  不含本地未跟踪产物。
- 固定镜像 `account-pool-balancer:release-c88bc2a-20261106T094136Z`，镜像ID
  `sha256:3fa0ae5611d68761d65453c76091742f5705054e594302298b0832c397380f79`。
  构建一次耗时 7.27 秒，未重复运行本提交已通过的本地全量测试。
- 因注册存储与身份逻辑变更做断网副本预检：复用线上 UID/GID 1000:1000、
  `--network none`、仅挂载副本；容器健康、healthz 返回 `{"ok":true}`、
  无解密或 panic 报错，随后清理预检容器与副本。
- 切换前以 25 秒×8 次采样确认注册库 mtime 与大小稳定（17:38:25 / 283848 字节），
  判定无排队或运行中注册任务。旧进程停止前完整备份 data（gzip 校验通过，
  SHA-256 `2f9e31ebda5375118f89a7fd60b2a4259a1e117684705672f4f0e22098aeecea`），
  再复用原 Compose 仅切换本服务；配置、用户、挂载、端口、网络别名、健康检查与
  资源参数保持不变。
- 新容器 healthy、restartCount=0、未 OOM，运行镜像 ID 与目标一致。
  `/healthz` 返回 200，首页四项资源（`app.css`/`app.js`/`perch.js`/`tokenharbor.js`，
  `?v=363aafdcf3`）HTTP 200，本地与远端的 SHA-256 逐一相同。
  `config.json`（82688 字节）与 `tokenharbor-signup.json`（283848 字节）保持原样。
- **本次验收不包含上游真实注册或推理**：`egressid` 的实际效果需要等下一次
  TokenHarbor 请求发生后才可在其面板上观察，本轮未主动注册、未解验证码、
  未付款、未推理。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 09:46:27.611–09:46:34.881，7.27 秒 |
| 备份 | 09:50:49.123–09:50:50.830，1.71 秒 |
| 切换请求 | 09:51:24.194 |
| Docker 健康确认 | 09:51:57.472，约 33 秒 |
| 验收完成 | 09:53:26 |

### 备份、回退与临时产物

- 受限目录 `/home/ubuntu/backups/account-pool-balancer/20261106T094136Z-pre-c88bc2a`，
  含 `data.tar.gz`、`docker-compose.yml`、`.env`；目录 0700、文件 0600，
  不下载、不入 Git。
- 保留旧镜像 `account-pool-balancer:rollback-20261106T094136Z`（`b7e751723905`，
  对应 `111ba2c`）。若需降级，先确认新版是否已写入旧版无法识别的记录，
  不能直接用旧 data 覆盖线上新写入。
- 临时产物：`/tmp/apb-c88bc2a.tar.gz`、`/tmp/preflight-c88bc2a/`（副本已删）、
  `/tmp/build-c88bc2a.log`，已登记，待用户同意后清理。

## 当前部署（2026-11-06 UTC，master：111ba2c）

- 用户授权推送部署；`master` 已推送业务版本
  `111ba2cac0944770b4714993ab4376de91e75122`。本节仅补记录，不再次构建/重启。
- 上线 GitHub OAuth 四段输入、分阶段固定代理、动态 finish、会话检查点和安全诊断，
  包含官方 `auth.tokenharbor.ai` 自定义鉴权域名白名单修复。新增 GitHub 行按用户选择
  明文0600保存，既有邮箱行保持原加密；本次线上 GitHub 新行数为0。
  **真实 GitHub 完整登录/邮箱验证此前仍未通过，本次健康验收不是该流程验收。**
- 从干净 git archive 构建，源码目录
  `/home/ubuntu/project/account-pool-balancer-releases/20261106T074610Z-111ba2c`；
  不含本地 `.private/` 会话、测试账号、诊断目录及未跟踪工作树。
  远程旧分叉工作树保持 e6f063b、干净，不 reset/rebase 或在旧源码上构建。
- 固定镜像 `account-pool-balancer:release-111ba2c-20261106T074610Z`，镜像ID
  `sha256:b7e75172390589abae0fc4ea5da0e30f8ad1574310cfdf4f5a8a3d41f5483324`。
  构建一次耗时7.187秒，未重复运行本次提交已通过的定向测试。
- 因注册存储扩展做断网副本预检。首个隔离容器默认USER与生产1000:1000不一致，
  只因副本0600权限退出1，未动线上；同镜像/副本复用线上UID/GID后启动健康并退出0。
  两个容器均network none、仅挂载副本；不改生产权限，不重建镜像。
- 切换前确认无queued/running注册任务，旧进程正常退出0后完整备份data并通过gzip/
  SHA-256校验，再复用原Compose仅切换本服务。环境、用户、挂载、端口、网络别名、
  重启/健康检查和资源配置保持一致，不覆盖在线配置或开启功能。
- **78账号、10把下游密钥、2把导入密钥、完整配置、routingSecret、81条注册记录的
  凭据/Key/代理/关联/支付链接及保存代理池全部保留**；远端内存HMAC核对，允许后台
  正常更新Cookie/套餐观察/更新时间。未上传或导入本地测试会话，未主动注册、解验证码、
  付款或推理。
- 新容器healthy、restartCount=0、未OOM，revision正确。本机和真实未登录公网浏览器
  healthz均HTTP200，HTML中的四项资源均200且SHA-256与目标源码相同。
  初次资源校验将`?v=`计入文件名，修正URL映射后通过，未再次重启；公网确认新代理控件
  已存在，未登录生产管理员。验收通过已立即反馈，再补本文档。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 07:50:20.283–07:50:27.470，7.187秒 |
| 同生产UID的断网启动校验 | 07:54:26.897–07:54:27.440，0.543秒 |
| 请求停止 / 旧进程退出 | 07:54:27.530 / 07:54:27.792 |
| 完整一致备份及校验 | 07:54:27.865–07:54:28.689，0.823秒 |
| 镜像切换请求 / 新容器启动 | 07:54:28.689 / 07:54:28.909 |
| Docker健康确认 | 07:54:59.494 |
| 本机完整验收 | 07:56:34.711 |
| 公网验收登记 | 07:58:37.862 |

整体超过5分钟已说明卡点，主要是预检/验收核对（含UID修正及资源查询串映射），
不是构建耗时或持续停机。未采样精确不可用窗口，不把约30秒健康检查等待当作停机。

### 备份、回退与临时产物

- 受限目录 `/home/ubuntu/backups/account-pool-balancer/20261106T074610Z-pre-111ba2c`，
  含完整data/校验、Compose/.env、build/switch日志及deploy.json；目录0700、敏感文件0600，
  不下载、不入Git。data SHA-256：
  `227fd8b325f6b721bc1ca62023e9870e39f7fe8a4dc465eecb6c33a05c54f305`。
- 保留旧镜像 `account-pool-balancer:rollback-20261106T074610Z`（5223edf）。
  **上线后若产生新GitHub行，旧版可能忽略并在写文件时丢弃这些行**；降级前必须检查
  新写入并明确保留/迁移方案，不能直接降级写注册库或恢复整份旧data覆盖新会话/账号。
- 两个已退出的断网容器 `account-pool-balancer-preflight-20261106t074610z`（exit1）和
  `account-pool-balancer-preflight-20261106t074610z-uid1000`（exit0），以及上述目录的
  `preflight-data/`、`preflight.log` 为临时预检产物，已登记，待用户同意再定向清理。
  正式备份/源码/新旧镜像、原有Trellis与本地业务会话不清理。公网验收浏览器已关闭。

## 历史部署（2026-11-06 UTC，master：5223edf）

- 用户授权重新部署 OVH，本次锁定本地已提交版本
  `5223edfa5e9c1589c644233d17aff02a686e0355`，从干净 `git archive` 快照构建；
  不混入未跟踪产物，不修改/reset远程旧分叉工作树。此提交部署时尚未推送 GitHub。
- 上线 TokenHarbor 注册限制仅结束当前账号、不再传播全局冷却的修复；保留固定单并发、
  新注册提交至少间隔60秒、不自动重试及真实UA。未主动注册、解验证码、付款或推理。
- 固定镜像 `account-pool-balancer:release-5223edf-20261106T061658Z`，镜像ID
  `sha256:0c6b150877b669f8354a912258b3321dd15c5d865532f05f769b97037ac54839`；
  源码目录 `/home/ubuntu/project/account-pool-balancer-releases/20261106T061658Z-5223edf`。
  无存储格式变化，复用已通过的Go普通/race/vet/build及JS测试，不复制敏感数据做额外预检。
- 复用原Compose，仅切换目标服务，配置、环境变量、用户、数据卷、端口、网络别名、
  健康检查、重启及资源参数保留。构建前及停机前均确认无排队/运行中注册任务，旧进程
  退出0后完整备份data并校验gzip与SHA-256，才切换新镜像。
- **78账号、10把下游密钥、2把导入密钥、完整配置、routingSecret、81条注册记录的
  密码/Key/代理/关联/支付链接等关键资产及代理池**全部保留；凭据仅在远端内存比对。
- 容器healthy、restartCount=0、未OOM，运行revision正确。本机及真实未登录公网浏览器
  healthz均HTTP200，首页正常，HTML中的四项资源均HTTP200、哈希与目标源码一致。
  验收脚本最初将环境变量顺序、空command和继承镜像entrypoint视为差异；按Docker/Compose
  语义核对后确认无实际变化，未重复构建/重启或恢复数据。整体超过5分钟已说明验收卡点，
  验收通过立即反馈；补本文档不再部署。这不是上游真实注册或付费推理验收。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 06:19:07.031–06:19:13.662，6.63秒 |
| 请求停止 / 旧进程退出 | 06:19:13.762 / 06:19:13.867 |
| 完整一致备份及校验 | 06:19:14.083–06:19:14.914，0.83秒 |
| 镜像切换 / 新容器启动 | 06:19:15.013 / 06:19:15.206 |
| 首次Docker健康检查通过 | 06:19:45.311 |
| 数据/本机资源验收 | 06:21:38.939 |
| 配置语义核对及公网验收登记 | 06:22:43.519 |

未采样精确不可用窗口，不把约30秒健康检查等待、验收排查或整体交付时长误报为停机。

### 备份与回退

- 受限目录 `/home/ubuntu/backups/account-pool-balancer/20261106T061658Z-pre-5223edf`，
  含完整data/校验、Compose/.env、build/switch.log及deploy.json，目录0700、文件0600，
  不下载、不入Git。data SHA-256：
  `48f54db7901645c06ec7a3c1296ec8489019cf1f0deb489a18526b5b346da6ce`。
- 旧镜像 `account-pool-balancer:rollback-20261106T061658Z`（54f28aa）保留。回退会恢复旧版
  跨账号注册暂停行为，须确认无注册任务并检查新写入；不整体恢复旧data覆盖上线后写入。
- 无新增临时部署脚本、诊断容器或敏感预检副本；公网独立浏览器验收后关闭。
  正式备份、源码、新旧镜像和业务记录保留，既有临时产物未经同意不清理。

## 历史部署（2026-11-05 UTC，master：54f28aa）

- 用户授权“先推送再部署”，并要求上线后观察几分钟。`master` 已推送并运行
  `54f28aaa5b0f77d5713e5feb1fca8bbb74d21a81`，业务提交为`082c03c`；不包含随机冒充
  Claude/Codex/pi等客户端的改动。真实下游UA保留，通用/缺省UA使用本项目真实标识；
  注册固定单并发、提交至少间隔60秒，遇限制暂停并完整遵守Retry-After，不自动重试。
- 固定镜像`account-pool-balancer:release-54f28aa-20261105T221442Z`，镜像ID
  `sha256:acccd04533506c9e13ecbf045f3310a61750343dc07de6e95e248ac764e64bc2`；
  干净源码归档位于`/home/ubuntu/project/account-pool-balancer-releases/20261105T221442Z-54f28aa`。
  未修改/reset旧分叉工作树，未在旧源码目录构建。
- 复用线上原Compose仅切换目标服务，环境、用户、数据挂载、端口、网络别名、重启及
  资源参数逐项相同，不应用源码Compose中的新资源默认值，不覆盖在线配置或开启功能。
  无存储格式变化，复用已通过的完整测试，不复制额外敏感预检数据。
- 构建后再次确认无排队/执行中的注册任务，旧服务退出后完整备份data、验证gzip及
  SHA-256后切换。**68账号、10把下游密钥、2把导入密钥、完整配置、routingSecret、
  81条注册记录关键资产及代理池**全部保留，后台Cookie/套餐观察更新不误判为数据丢失。
- 本机健康及四项静态资源验收通过；真实未登录公网浏览器healthz/资源均HTTP200，
  资源哈希与提交源码一致，固定并发1控件已确认。`tokenharbor.js` SHA-256为
  `1c951d4887579c2e1d47d48163efe56f5480b003396d127f22865b846c059acc`。
- 按用户要求连续观察约3分钟：每15秒采样，共13次，全部healthy/HTTP200，运行revision
  正确，restartCount始终0、未OOM。CPU观测0.01%–0.84%，内存185.7–236.2MiB（包含启动
  缓存预热，不是容量/峰值保证）。启动后截至日志核验时9条既有业务请求均200（OpenDesign3、
  Modal6），无新增错误日志、panic/fatal/持久化失败信号；TokenHarbor无真实推理样本。
  未主动注册、解验证码、创建订单、付款或推理，不把健康验收当作付费推理/官网UA显示实测。
  上线/观察通过已立即反馈，补记录不再重建或重启。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 22:14:42.994–22:14:49.724，6.73秒 |
| 请求停止 / 旧进程退出 | 22:14:49.835 / 22:14:49.931 |
| 完整一致备份及校验 | 22:14:50.113–22:14:50.917，0.80秒 |
| 新容器启动 | 22:14:51.125 |
| Compose healthy确认 | 22:15:21.718 |
| 本机/配置/资源验收 | 22:15:21.830 |
| 连续观察 | 22:15:54.257–22:18:55.274，13次采样/约181秒 |
| 公网验收登记 | 22:20:39.044 |

未采样精确不可用窗口，不将约31秒启动至healthy等待、用户要求的观察时间或整个操作时长
误报成停机。整体超过5分钟已说明构建仅6.7秒，包含主动连续观察与公网核验。

### 备份与回退

- 受限目录`/home/ubuntu/backups/account-pool-balancer/20261105T221442Z-pre-54f28aa`，
  含完整data/校验、Compose/.env、build/switch.log、deploy.json及observation.json，
  目录0700、文件0600，凭据不输出或下载。data SHA-256：
  `9afbc20a67c0417d604f728df7e72edef4499a0321d94bc773c6f429141edf8f`。
- 旧镜像`account-pool-balancer:rollback-20261105T221442Z`（0ed063c）保留。
  回退不整体恢复旧data，必须保留上线后的账号/会话/订单写入；旧版不具备本次单并发、
  暂停与真实UA兜底保护，降级前需明确影响并确认无注册任务。此次未执行回退。
- 无新增临时部署脚本或敏感预检副本，公网独立浏览器已关闭；正式备份、源码快照、
  新旧镜像和观察/部署业务记录不清理。既有本地Mock/测试产物仍待用户同意定向清理。

## 历史部署（2026-11-05 UTC，master：0ed063c）

- 用户授权清理本次测试产物并部署最新 master。预检确认本地/远端 master 均为
  `0ed063c1f04ddb01a1279607652ed82a150528d7`，从此提交的干净归档构建，
  不包含其他窗口尚未提交的注册节流/UA修改，不修改远程旧分叉工作树。
- 上线 Agent Pass 预检失败可重试、已保存链接复用、旧不确定结果需逐次人工
  核对账单确认后恢复的修复；保留报价/Free/固定出口和不自动付款保护。
- 镜像 `account-pool-balancer:release-0ed063c-20261105T220002Z`，镜像ID
  `sha256:9d32e1220aadf7353fc892cdec5a8e90b4db9fe769ea9619c155bd194e14e2b9`；
  源码目录 `/home/ubuntu/project/account-pool-balancer-releases/20261105T220002Z-0ed063c`。
  只切换原Compose的目标服务，卷、端口、网络别名、环境、用户、重启及资源参数
  逐项一致；不覆盖配置、不启用额外功能。无存储格式变化，不再复制敏感数据做离线预检。
- 构建后确认无排队/执行中注册记录，再停旧进程，完整备份data并通过gzip及
  SHA-256校验后切换。**68个账号、10把下游密钥、2把导入密钥、完整配置、
  routingSecret、81条注册记录的密码/Key/代理/关联/支付链接等关键资产及代理池**均保留；
  会话/更新时间允许后台正常刷新，凭据只在远端内存比对，不输出或下载。
- healthy、restartCount=0、运行revision正确；本机及公网真实未登录浏览器
  healthz均HTTP200，首页及从HTML读取的四项静态资源均200，哈希与目标源码一致。
  公网 `tokenharbor.js` 的恢复控件已确认，SHA-256为
  `8761c7e6e2ad726f3d36ec5b54e615ebc2b21a27a484ce64c59218671fc3dda3`。
  未登录生产管理员、创建真实订单、注册、解验证码、付款或推理；验收通过后立即反馈上线。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 22:00:02.304–22:00:09.629，7.33秒 |
| 请求停止 / 旧进程退出 | 22:00:09.701 / 22:00:09.791 |
| 完整一致备份及校验 | 22:00:09.991–22:00:10.737，0.75秒 |
| 新容器启动 | 22:00:10.932 |
| Compose healthy确认 | 22:00:41.508 |
| 本机健康/配置/资源验收 | 22:00:41.577 |
| 公网真实浏览器验收登记 | 22:01:30.371 |

未采样精确不可用窗口，不把约31秒启动至healthy等待或整体交付时长当作停机。

### 备份、回退与清理

- 受限备份 `/home/ubuntu/backups/account-pool-balancer/20261105T220002Z-pre-0ed063c`，
  含完整data/校验、Compose/.env、build/switch.log及deploy.json；敏感文件0600，不下载。
  data SHA-256：`a25c0d3c68605c8001a8205ee5ff940fe288b23dfd89420ebe7f00fab5c6c460`。
- 旧镜像 `account-pool-balancer:rollback-20261105T220002Z`（5bdfc24）保留。
  回退不自动恢复旧data，须保留上线后的账号、Cookie和订单写入，避免清除不确定订单标记。
- 用户已同意并完成本轮 `.analysis/tokenharbor-checkout-recovery/` 及登记的四个
  本地浏览器快照的定向清理，所属Mock已停止、端口关闭。其他任务产物及原始Trellis保留。
  本次部署未生成临时脚本或敏感预检副本；正式备份/源码快照/新旧镜像和部署记录不清理。
  公网验收浏览器已关闭。本节为记录更新，不因此再次重建/重启服务。

## 历史部署（2026-11-05，master：5bdfc24）

- 用户授权部署最新 master；预检期间另一窗口完成登录恢复提交，本次锁定业务版本
  `5bdfc2419cb0127d2332d8dce4cfc51145c99337`。后续 master 的 `cf46b9d`
  仅补验收文档，业务/构建路径无差异，不再次构建或重启。
- 固定镜像 `account-pool-balancer:release-5bdfc24-20261105T213227Z`，镜像ID
  `sha256:c70a2dbae6f766bd1b9f741ab12f17b2a644bfb207ac69d211bd19e5e08f66ae`。
  从明确提交干净归档到
  `/home/ubuntu/project/account-pool-balancer-releases/20261105T213227Z-5bdfc24`；
  不混入未提交修改，不修改/reset远程旧分叉工作树。
- 包含套餐模型/周额度门禁、会话及错误详情、逐账号独立代理和安全登录恢复。
  复用线上原Compose仅切目标服务，挂载、端口、环境、用户、网络别名、重启及
  资源限制均保持一致；不覆盖线上配置，不额外启用功能，不应用源码Compose新增
  容量参数。严格门禁下缺少有效来源/套餐观察的账号可能停调度，不绕过该策略。
- 新镜像使用断网数据副本启动成功并正常退出0；旧服务退出后完整一致备份data，
  gzip及SHA-256校验通过。**63个账号、10把下游密钥、2把导入密钥及完整配置**
  保持一致，metadata中的routingSecret已与备份逐项核对。
- 首轮发现注册库密文变化，没有回滚覆盖在线数据；仅在远端内存解密核对，确认
  **39条记录的ID、账号密码/Key、代理、入池关联、支付链接等关键字段及代理池均
  保留**，仅一条已入池记录的cookies/updatedAt正常刷新，状态不变。核对使整体验收
  超过5分钟，已主动说明卡点；不是构建或持续停机。
- healthy、restartCount=0、revision正确；本机与公网真实未登录浏览器healthz及首页
  HTTP200，app.js/tokenharbor.js/perch.js/app.css均HTTP200且SHA-256与源码一致。
  不主动发起注册、验证码、付款、修改代理或推理；后台正常只读观察/会话更新不被
  误判为数据丢失。本次不代表真实付费推理验收。完成后立即反馈上线，再补记录。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 21:34:45.341–21:34:54.451，9.11秒 |
| 断网副本启动预检 | 21:34:54.483–21:34:55.220，0.74秒 |
| 请求停止 / 旧进程退出 | 21:34:55.225 / 21:34:55.477 |
| 完整一致备份及校验 | 21:34:55.477–21:34:56.175，0.70秒 |
| 新容器启动 | 21:34:56.376 |
| Compose healthy确认 | 21:35:26.987 |
| 本机健康/配置/资源核验 | 21:35:27.074 |
| 公网与注册库数据验收登记 | 21:36:37.661 |

未采样精确不可用窗口，不把约32秒停止至healthy等待或整体交付时长当作停机。

### 备份、回退及临时产物

- 受限目录 `/home/ubuntu/backups/account-pool-balancer/20261105T213227Z-pre-5bdfc24`，
  包含完整data/校验、Compose/.env、build/switch.log及deploy.json，敏感文件0600，
  不下载、不入Git。完整data备份SHA-256：
  `fc26ceb4cc41ee57c2259f211980e33630e90135d850aca0e19b0a80ca913b69`。
- 旧镜像 `account-pool-balancer:rollback-20261105T213227Z`（2559c94）保留。
  回退前须检查新配置字段、模型/额度观察及上线后的账号/会话写入兼容性；
  不整体恢复旧数据覆盖新写入，不将旧版启动成功等同于新策略仍受保护。
- 用户同意后，于21:39:09 UTC定向删除已退出0的断网预检容器
  `account-pool-balancer-preflight-20261105t213227z` 及本目录preflight-data/preflight.log，
  均已复核不存在。完整备份及配置/构建/切换记录哈希未变，正式源码快照、新旧镜像
  和部署记录保留；线上容器ID、启动时间、重启次数未变，仍healthy、healthz HTTP200。
  清理记录已更新远程deploy.json和 `_tmp_artifacts.md`，不扩大到其他产物或Trellis。
  独立公网浏览器已关闭；不新建Trellis任务、不产生临时部署脚本。

## 历史部署（2026-11-05，TokenHarbor 归档与退回：2559c94）

- 用户授权推送部署，业务提交 `2559c94ab75f495f24e425edc108f8e893aa7e8e`
  已推送并运行；固定镜像 `account-pool-balancer:release-2559c94-20261105T171553Z`，
  镜像ID `sha256:bc4f20097ed93095a3e4974ed041e53c78307740f891e755667f25b8dae20ee0`。
- 源码从该提交干净归档到
  `/home/ubuntu/project/account-pool-balancer-releases/20261105T171553Z-2559c94`；
  不修改/reset旧分叉工作树、不在旧源码目录构建。复用原Compose只切目标服务，
  卷、端口、环境、用户、网络别名和重启策略均保持一致；无schema变化，不重复
  复制敏感数据做断网预检，也不重跑已通过的相关测试。
- 上线待处理/已入池分类及显式退回原注册机，目录文案改为“目录模型”。
  套餐模型映射本轮仅研究，不裁剪下游模型或更改推理路由/在线功能开关。
- 旧服务退出后完整一致备份data并通过gzip/SHA-256校验；本次切换前后的
  **57个账号、10把下游密钥、2把导入密钥、完整配置、routingSecret及加密注册库**
  均保持一致。没有替用户退回真实账号、重新注册、解验证码、付款或推理。
- healthy、restartCount=0、revision正确；本机及公网真实未登录浏览器healthz
  HTTP200。新页签存在，app.js/tokenharbor.js/app.css均HTTP200且哈希匹配；
  退回接口未登录时返回401。公网验收后立即反馈上线，补记录不再重建/重启。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 17:16:36.825–17:16:43.601，6.78秒 |
| 请求停止 / 旧进程退出 | 17:17:38.046 / 17:17:38.378 |
| 完整一致备份及校验 | 17:17:38.378–17:17:39.216，0.84秒 |
| 新容器启动 | 17:17:39.437 |
| Compose healthy确认 | 17:18:10.062 |
| 本机与数据验收 | 17:18:10.182 |
| 公网验收登记 | 17:19:02.955 |

本次未采样精确不可用窗口，不把约32秒停止至healthy等待当作停机时长。

### 备份与回退

- 受限目录 `/home/ubuntu/backups/account-pool-balancer/20261105T171553Z-pre-2559c94`，
  包含完整data/校验、Compose/.env、build/switch.log及deploy.json，敏感文件0600，
  不下载、不入Git。完整data备份SHA-256：
  `905e4fcbe658e8715a3f9c75320cdca3569140980a8cb27fc8e9ceff3ce379fa`。
- 旧镜像 `account-pool-balancer:rollback-20261105T171553Z`（d7b914f）保留。
  无需schema转换，但回退仍须核对上线后入池/退回等新写入，不能用旧备份覆盖
  在线数据或自动复活已移出账号。正式备份/镜像/源码/部署记录不作为临时垃圾清理。
- 本次无新Trellis任务、临时部署脚本或敏感预检副本；独立公网浏览器已关闭。

## 历史部署（2026-11-05，TokenHarbor 日志与批量入池：d7b914f）

- 用户授权推送部署，业务提交 `d7b914f20a31ede3bbb80638086ff0768a3b36eb`
  已推送并运行。固定镜像 `account-pool-balancer:release-d7b914f-20261105T160127Z`，
  镜像 ID `sha256:4f292612dd82dbcdbf116cef48fcb90c4712e2d81755ff04d68e67d3e6437d7c`。
- 从该提交干净归档构建，源码目录
  `/home/ubuntu/project/account-pool-balancer-releases/20261105T160127Z-d7b914f`；
  旧分叉工作树未修改/reset。仅更新目标服务，卷、端口、用户、环境、网络别名、
  重启策略均保持一致。本次仅前端变更，无存储格式修改，不重复复制敏感数据预检。
- 新增右下角日志重新展开入口、当前任务所选账号批量校验入池；进入注册机默认
  展开进行中/最新任务，轮询保持用户选择。入池仍使用原有后端实时套餐校验。
- 旧服务退出后完成完整一致 data 备份，gzip/SHA-256 校验通过。56 个账号、
  9 把下游密钥、2 把导入密钥、完整配置、routingSecret、加密注册库均保留。
  容器 healthy、restartCount=0；未调用真实注册、验证码、付款或推理接口。
- 本机及公网真实未登录浏览器 `/healthz` HTTP200；新版控件存在，
  tokenharbor.js/app.css 均HTTP200且SHA-256与本次源码一致。
  tokenharbor.js：`645629039cc3d604edda3396e9abf519b73f9c3c28205280c610cd152d89519f`。
  验收后立即反馈上线，随后仅补记录，不再次部署。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 16:02:01.796–16:02:08.603，6.81秒 |
| 请求停止 / 旧进程退出 | 16:02:54.442 / 16:02:54.781 |
| 一致完整备份及校验 | 16:02:54.781–16:02:55.633，0.85秒 |
| 新容器启动 | 16:02:55.867 |
| Compose healthy确认 | 16:03:26.453 |
| 本机与数据验收 | 16:03:26.566 |
| 公网验收登记 | 16:04:02.855 |

本次未采样精确不可用窗口，不把约32秒停止至healthy确认时间当作停机时长。

### 备份与回退

- 受限目录 `/home/ubuntu/backups/account-pool-balancer/20261105T160127Z-pre-d7b914f`，
  含完整data、校验、Compose/.env、build/switch.log及deploy.json；敏感文件0600，
  不下载、不入Git。data备份SHA-256：
  `f5e96ff58c5b03d00dfef87b1be3e95d6ac2715339811c70533a5d4ac65c3d04`。
- 旧镜像 `account-pool-balancer:rollback-20261105T160127Z`（ccafad3）保留；
  无需转换注册库schema，回退仍须核对上线后写入，不自动用旧备份覆盖在线数据。
  现有内网防火墙规则未变。正式备份、源码和镜像是必要审计/回退资料，不清理。
- 未新建Trellis任务或临时部署脚本，无新敏感预检副本；独立公网验收浏览器已关闭。

## 历史部署（2026-11-05，Token Harbor 批量操作：ccafad3）

- 用户授权放行20000–29999后推送部署。业务提交
  `ccafad3b22f2d7876a304f0b330c7e3cdb74e6f2` 已推送并运行；镜像
  `account-pool-balancer:release-ccafad3-20261105T153902Z`，镜像ID
  `sha256:76cfe530f784861ef9a056c13268638233470fde307db68947f6417c436e387f`。
- 持久化UFW规则**仅**允许 `br-8f33b6f41627`（ai-internal）上来自
  `172.18.0.0/16` 到本机 `167.114.158.4` 的TCP `20000:29999`。
  不向公网新增开放，不关闭UFW，IPv6规则未改；原规则已备份。
  切换前复用诊断容器/生产网络命名空间，同20007代理GET注册页
  HTTP/2为200/414ms、HTTP/1.1为200/260ms；切换后新容器TCP复查通过。
  证明之前是Docker到宿主机代理端口被拦，不是IPv6或HTTP版本不支持。
- 目标提交归档源码位于
  `/home/ubuntu/project/account-pool-balancer-releases/20261105T153902Z-ccafad3`。
  旧分叉工作树未修改/reset；复用原Compose只切目标服务，卷、端口、用户、
  环境、网络/别名和重启策略均保留。此版无存储schema变化，不重复复制敏感
  数据做离线预检；一致完整备份在旧服务退出后完成，gzip和SHA-256通过。
- 本机/公网healthz及新版资源验收通过；revision正确、healthy、restartCount=0。
  三个批量按钮已进入新版页面；tokenharbor.js SHA-256为
  `5d42b1f6889a2f09c360459e724c2d4393ca14c2cd1329b262d901300f61b534`。
  56账号、9把下游密钥、2把导入密钥、非账号设置、加密注册库和routingSecret
  均保留。未代替用户重试注册、解验证码、创建真实订单、付款或推理。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 内网防火墙放行 | 15:39:02.581–15:39:02.693 |
| 构建 | 15:39:04.108–15:39:10.609，6.50秒 |
| 请求停止 / 旧进程退出 | 15:39:10.644 / 15:39:10.907 |
| 完整一致备份及校验 | 15:39:10.942–15:39:11.597，0.655秒 |
| 新容器启动 | 15:39:11.798 |
| Compose healthy确认 | 15:39:42.380 |
| 本机和数据验收 | 15:39:42.506 |
| 公网验收登记 | 15:41:58.077 |

本次未采样精确不可用窗口，不把约32秒healthy等待当停机时长。公网真实未登录
浏览器确认healthz/资源HTTP200、资源哈希一致后立即反馈上线；公网验收时刻见远程deploy.json。

### 备份与回退

- 受限目录 `/home/ubuntu/backups/account-pool-balancer/20261105T153902Z-pre-ccafad3`，
  包含完整data、校验、Compose/.env、旧UFW规则、构建/切换/防火墙日志和
  proxy-acceptance.jsonl/deploy.json；敏感文件0600，不下载、不入Git。
  data备份SHA-256：`c5d2b403402025c5f7ed18533c66d0eb5d7eba87846e9cfe08e90f9db1b75348`。
- 保留旧镜像 `account-pool-balancer:rollback-20261105T153902Z`（759256c）。
  回退程序不自动撤销本次经授权的内网放行，也不整体恢复旧数据覆盖新写入。
  如要撤销网络规则，应只删除本节这条精确规则，不整份覆盖UFW或关闭防火墙。
- 本轮无新Trellis任务、无新增敏感预检副本；原诊断容器重新运行后正常退出，
  沿用 `_tmp_artifacts.md` 已登记范围，未经同意不删除。正式备份/镜像/源码保留。

## 历史部署（2026-11-05，Token Harbor 注册保存：759256c）

- 用户授权推送并直接部署；`master` 业务提交
  `759256c3293741641272f00276501ecaf2ca2596` 已推送并运行。
  固定镜像 `account-pool-balancer:release-759256c-20261105T144146Z`，
  镜像ID `sha256:b03256171ddef1522c9ed587aa87ba91c0305dc04bfe1a7d037a1f0c7aceaafa`。
- 源码从明确提交归档到
  `/home/ubuntu/project/account-pool-balancer-releases/20261105T144146Z-759256c`；
  未修改/reset远程旧分叉工作树，未在旧源码目录构建。复用原Compose仅切换目标服务，
  挂载、端口、环境、用户、网络别名和重启策略保持一致。
- 新镜像用断网数据副本启动预检通过并正常退出0；旧服务停止后完整备份data、
  验证gzip及SHA-256后切换。原56账号逐项相同，9把下游密钥、2把导入密钥及
  非账号设置保持一致；未发起注册/验证码/付费推理，不自动修改功能开关。
- 本机与公网真实未登录浏览器 `/healthz`、首页和3个资源均HTTP200；
  app.js/tokenharbor.js/app.css哈希与源码一致。页面已包含“仅保存”“保存代理池”
  和随机分配，不再包含自动入池选项。运行revision正确、healthy、restartCount=0。
- 首次远程命令在已确认healthy后回传超时；检查确认新版本正常运行，未重复构建/
  重启或恢复旧数据。补完数据与公网验收后立即告知上线，本节仅补记录，不重新部署。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 14:41:47.418–14:41:54.038，6.62秒 |
| 断网副本预检 | 14:41:54.038–14:41:54.591，通过 |
| 请求停止 / 旧进程退出 | 14:41:54.591 / 14:41:54.835 |
| 完整一致备份及校验 | 14:41:54.835–14:41:55.470，0.635秒 |
| 新容器启动 | 14:41:55.664 |
| Compose healthy确认 | 14:42:26.246 |
| 数据及本机补验收 | 14:46:58.855 |
| 公网验收登记 | 14:48:22.169 |

本次健康采样未取得完整持久化结果，**不报告精确停机时长**；约32秒的停止请求
至healthy等待、远程回传超时和整个交付时长都不等于停机时长。

### 备份与回退

- 受限目录 `/home/ubuntu/backups/account-pool-balancer/20261105T144146Z-pre-759256c`，
  目录0700、敏感文件0600，完整data备份5,812,413字节，SHA-256：
  `00eacea2bba4a7b7919f16b3c46e06aad83fa087a217928d7bd801be4b7e91a9`。
- 旧镜像 `account-pool-balancer:rollback-20261105T144146Z` 对应3cf9107，保留。
  **旧版不认识新注册库saved状态**：回退前必须检查上线后的新写入并制定兼容转换，
  不可直接交给旧程序，也不得整库恢复旧备份覆盖新账号/凭据/代理池。
- 停止的预检容器、preflight-data和preflight.log登记 `_tmp_artifacts.md`，
  未经同意不删除；完整备份、镜像、正式源码和部署记录保留。本轮无新Trellis任务。

## 历史部署（2026-11-05，master：3cf9107）

- 用户授权重新部署最新 master。部署前核对 GitHub/master 为
  `3cf9107ed7dd409b6b6b48322b12c88c2e3ad3b9`，从远端明确提交的干净归档
  构建，不使用旧工作树源码；本节只补记录，不再次部署。
- 固定镜像 `account-pool-balancer:release-3cf9107-20261105T134914Z`，镜像 ID
  `sha256:31ef35f787bc96d5416aa6425bd87f1ccf3924405373a9529df5ff87f48614cc`。
  原工作树仍为 `e6f063b`，未 reset/rebase；新源码位于
  `/home/ubuntu/project/account-pool-balancer-releases/20261105T134914Z-3cf9107`。
- 复用原 Compose，只更新目标服务；卷、端口、环境、用户、网络/别名及重启
  策略均保留。完整数据副本在 `--network none` 容器健康启动、停止退出0；
  旧进程正常退出后完整备份在线 data、验证 gzip 及 SHA-256，再切换镜像。
- OpenCode 保持启用、兼容转换保持关闭；所有非账号设置及9把下游密钥、
  2把导入密钥逐项相同。未主动发起推理、注册、验证码或修改选号策略。
  新策略已经部署，但是否使用仍取决于原有 Go 路由配置，不自动切换。
- 首次验收发现账号数量变化，**没有恢复旧配置覆盖在线写入**：61个原账号
  中6个 Perch 账号被同期在线管理删除，6条 `deleted` 日志逐条吻合；另有
  1个 Perch 新增，登记时共56个。所有仍存在的原账号逐项未变。核对这项
  并发管理变更使整体超过5分钟，已主动说明；并非构建或持续停机。
- 容器 healthy、restartCount=0，运行 revision 正确；本机和真实未登录
  浏览器 `/healthz` HTTP200。公网 app.js/tokenharbor.js/app.css 均HTTP200，
  SHA-256 与源码快照及本机一致；perch.js HTTP200，登录页正常。
  这是部署/数据保留验收，不代表真实客户端付费推理验收。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 13:49:15.087–13:49:21.785，约6.70秒 |
| 断网副本启动预检 | 13:49:21.794–13:49:22.647，通过 |
| 请求停止 / 旧进程退出 | 13:50:28.124 / 13:50:28.400 |
| 一致备份及校验 | 13:50:28.407–13:50:29.041，约0.63秒 |
| 新容器启动 | 13:50:29.259 |
| 首次观测 HTTP 恢复 | 13:50:29.632 |
| Compose healthy 确认 | 13:50:59.858 |
| 并发配置变化核对及最终验收登记 | 13:54:27.439 |

0.25秒采样首次失败13:50:28.379、首次恢复13:50:29.632，观测不可用约
**1.252秒**，存在采样误差。约32秒的停止至healthy等待不是实际停机时长。
上线验收反馈先于本节文档收尾。

### 备份、回退及临时产物

- 受限目录（0700，文件0600）：
  `/home/ubuntu/backups/account-pool-balancer/20261105T134914Z-pre-3cf9107`。
  含完整 data.tar.gz/校验、Compose/.env 备份、build/switch.log、脱敏deploy.json；
  敏感数据不下载到本地、不提交Git。
- 完整备份 SHA-256：
  `d3c815805511deb2587c4dfca787a723d447e724b2ed14b828db1728484b2b0d`。
- 旧镜像 `account-pool-balancer:rollback-20261105T134914Z` 对应9063068。
  旧版不认识 `least-requests-1h`，回退前须先用新版将相关路由改回旧策略，
  并核查新注册状态及上线后的写入兼容性；不得整体恢复旧备份、复活已删除
  账号或丢弃新增账号。此次没有执行回退。
- 停止的预检容器 `account-pool-balancer-preflight-20261105t134914z`、同目录
  preflight-data/ 和 preflight.log 已登记 `_tmp_artifacts.md`，待用户同意再清理。
  完整备份、正式源码快照、新旧镜像及部署业务记录保留；本轮无新Trellis任务。

## 历史部署（2026-11-05，Token Harbor 号池）

- 用户授权提交、推送并部署；`master` 已推送，业务版本为
  `90630682c07e17fd0dcba270f0ff58ab3e68bf1d`。本节补记录不再次部署。
- SSH、公网、容器、卷、端口及网络均不变：`ovh-server`、
  `https://ods.yeschoy.io`、`account-pool-balancer`、
  `/home/ubuntu/project/account-pool-balancer/data:/data`、`127.0.0.1:8790`、
  `ai-internal` 与 `opendesign-balancer` 别名。
- 固定镜像：`account-pool-balancer:release-9063068-20261105T091131Z`；
  `latest` 指向镜像
  `sha256:f733236840745f55fae33c4335d08d25c2a17c6fd655885fb498cb260048762e`。
- 构建源码为明确提交的干净归档：
  `/home/ubuntu/project/account-pool-balancer-releases/20261105T091131Z-9063068`。
  旧工作树仍为 `e6f063b`，仅 fetch 更新远程引用；没有 reset/rebase、覆盖配置
  或在旧目录 `up --build`。
- 新镜像用现有顶层数据文件副本在 `--network none` 容器启动成功、健康检查
  通过、停止退出 0 后才切换。切换前停止旧服务，备份完整 data，验证 gzip
  及 SHA-256；只更新目标服务，不触及其他容器。
- 验收确认 **52 个账号、9 把下游密钥、2 把导入密钥**及账号凭证/代理/
  provider/group、原有设置、环境、卷、端口、网络别名均保留；相关状态 JSON
  可读取。OpenCode 保持启用，兼容转换保持关闭，没有新增功能开关操作。
- 容器 healthy、restartCount=0，镜像 revision 正确；本机和公网 `/healthz`
  均为 200。真实浏览器未登录生产管理员账号，确认仅一个 Token Harbor 标签、
  注册机导航保留，app.js/tokenharbor.js/app.css 均 HTTP 200，哈希与源码快照
  一致（资源版本 `07f5a2e4de`）。未主动发起真实推理、注册或验证码消费；
  本节不代表真实 Codex/Claude Code 或 Token Harbor Key 推理已验收。

### 本次时间（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 09:11:32–09:11:39，7 秒 |
| 断网副本预检 | 09:11:39–09:11:40，通过 |
| 请求停止 / 旧容器退出 | 09:16:40.245 / 09:16:40.372 |
| 一致备份及 gzip 校验 | 1.601 秒，19,757,095 字节 |
| 新容器启动 | 09:16:42.441 |
| 首次观测 HTTP 恢复 | 09:16:42.751 |
| Compose healthy 及本机验收 | 09:17:13 |
| 公网验收登记 | 09:19:03 |

0.25 秒采样观测到不可用窗口约 **2.253 秒**，采样有误差；
不能把约 33 秒的停止请求至 healthy 等待或整体交付时长当成停机。
整体超过 5 分钟，已主动说明时间主要花在切换前的数据保留/回退核对，
并非构建等待。上线验收反馈先于文档收尾。

### 本次备份与回滚

- 受限目录：
  `/home/ubuntu/backups/account-pool-balancer/20261105T091131Z-pre-9063068`
  （目录 0700，敏感文件 0600）；含完整 data.tar.gz、校验、Compose/.env
  副本和脱敏 deploy.json，未下载凭据到本地或提交 Git。
- 备份 SHA-256：
  `bc24fb877a0bb745f65967c9d73d4231db940f63657ba715779eed76591f8b68`。
- 旧镜像：`account-pool-balancer:rollback-20261105T091131Z`，对应原运行
  `1c7a3b8`。**回滚前必须检查配置兼容性和上线后的新写入**：旧版不认识
  新注册配置或新增账号的某些字段，不能直接把新配置交给旧镜像，也不能
  自动覆盖旧数据备份。必要备份、版本快照、镜像和 deploy.json 保留。
- 本次停止的预检容器
  `account-pool-balancer-preflight-20261105t091131z` 和上述备份目录内
  `preflight-data` 临时敏感副本，已登记 `_tmp_artifacts.md`，待用户同意
  后清理；尚未删除。它们不是在线 data，也不等于完整回滚备份。

## 历史部署（2026-11-04）

- SSH：`ovh-server`；公网：`https://ods.yeschoy.io`。
- 容器：`account-pool-balancer`；宿主监听仍为 `127.0.0.1:8790`。
- 已部署业务代码：`1c7a3b810c93e76c2c29f3e8429d8470c2b7f6ce`。
- 固定镜像：`account-pool-balancer:release-1c7a3b8-20261104T062429Z`。
- 镜像 ID：`sha256:7c0164c89d604238d864095b5995dd2c6c26fc72958fc5501bfa3743e0d8b9cc`。
- 线上 Compose 继续使用 `account-pool-balancer:latest`，现指向上述镜像。
- 现有数据绑定不变：`/home/ubuntu/project/account-pool-balancer/data:/data`。
- `ai-internal` 网络和 `opendesign-balancer` 兼容别名保留。
- 部署切换时 OpenCode 总开关、兼容转换开关保持关闭，未调用其推理接口。
- 后续曾按用户要求通过管理 API 导入 6 个 OpenCode Go 账号，当时开关仍关闭，未做真实上游推理验证；数据已包含旧版不认识的新 provider。后续开关状态见下节。

## OpenCode 开关调整（2026-11-04 18:26 UTC）

用户报告新导入 Go Key 显示 `provider-disabled`、额度未知。确认账号本身
已启用，但 `opencode.enabled=false`；没有额度缓存，不代表 Key 已失效。
经用户明确同意，通过现有管理员 API 开启 OpenCode，再对当前唯一的 Go
账号同步模型和额度。配置 revision 为 5084，核对配置差异仅有
`opencode.enabled` 和 `revision`；兼容转换仍关闭，其他账号池设置不变。

- 账号状态变为 `ok`，模型目录 28 个，可路由 27 个。
- 5 小时、周、月额度查询均成功，已用 0%、剩余 100%，数据新鲜。
- `/healthz` 通过；没有重建、重启服务，无部署切换停机。
- 未发起推理请求，账号 24h 请求/Token 计数仍为 0；目录和额度读取成功
  不代表真实推理权限已验收。
- 修改前配置备份位于远程受限目录
  `/home/ubuntu/backups/account-pool-balancer/20261104T182614Z-enable-opencode/config.json`
  （目录 0700、文件 0600）。这是必要的业务回滚备份，不是待清理临时产物；
  未下载到本地或提交 Git。需撤销时应通过设置 API 仅关闭 OpenCode，
  不用整份旧配置覆盖后续在线写入。

## 重要：旧工作树不是当前镜像的构建源码

远程 `/home/ubuntu/project/account-pool-balancer` 的 `master` 保留在
`e6f063b`，有 3 个与 GitHub 分叉的历史提交。它与 GitHub `f10fa53`
的业务代码完全一致，差异仅为旧提交中误加入的 `.gomodcache`。没有
reset/rebase/清理旧工作树或删除缓存。

本次从 GitHub 目标提交执行 `git archive` 得到干净快照，在下列目录
实际构建 Docker 镜像：

```text
/home/ubuntu/project/account-pool-balancer-releases/20261104T062429Z-1c7a3b8
```

**不要直接在旧工作树执行 `docker compose up --build`，否则可能把旧
源码重新部署上线。** 下一次升级应先核对运行镜像的 revision 标签，
保留回滚镜像，fetch 后按明确的新提交构建独立源码快照，再在原目录
执行 `docker compose up --no-build --no-deps --pull never --wait`。
不要强行丢弃远程分叉提交；若要统一历史，另行确认并保留备份。

## 已完成的远程验证

1. 在 OVH 实际构建完整 Dockerfile，包括运行时 Python 依赖。
2. 用生产数据文件副本在 `--network none` 容器中启动，新程序健康检查
   成功并正常退出 0；副本没有挂载真实数据目录，未连接生产网络。
3. 停止旧容器后备份完整 `data/`，校验 gzip 完整性并保存 SHA-256；
   随后只重建目标服务，不操作其他容器。
4. 容器 healthy，restartCount=0；本机及公网 `/healthz` 均通过。
5. 切换时 47 个账号、8 把下游密钥、2 把导入密钥均保留；核对既有
   ID、账号运行/控制凭证、代理、provider、group、下游密钥及其 hash。
   管理员密码 hash、既有 upstream/routing/quota/perch 和各池错误/
   重试规则保持一致，未把凭证明文输出或下载到本地。
6. metadata、selection-counters、perch-secrets、sessions JSON 可读取。
7. 真实浏览器打开公网登录页，脚本 HTTP 200，Zen/Go 标签已进入新
   HTML；公网脚本与版本快照及本机脚本的 SHA-256 一致：
   `b630f50846f733c0fc52db548986a83ad6cc23d4f94f2925ea864265fb98489a`
   （95375 字节）。没有使用生产管理员密码登录或改动设置。

公网静态资源对 OVH 上直接 Python HTTP 客户端的请求在部署前就返回
403；没有更改 WAF 或配置代理/出口轮换，公网资源校验使用正常真实
浏览器。此处只验证部署/启动/数据保留，不代表真实 Zen/Go 和五种客户端
的推理验收，也不代表 Linux arm64 容器已实际运行。

## 回滚入口

受限目录（0700）：

```text
/home/ubuntu/backups/account-pool-balancer/20261104T062429Z-pre-1c7a3b8
```

其中 `data.tar.gz` 是旧进程退出后的完整一致备份（0600），旁有校验文件、
Compose/.env 副本、构建日志和脱敏 `deploy.json`。`.env`、数据及日志留在
远程，不提交 Git。旧镜像标签：

```text
account-pool-balancer:rollback-20261104T062429Z
```

**当前已导入 OpenCode Go 账号，不能直接执行下面的旧镜像降级命令。**
先确认并执行保留新写入数据的迁移/恢复方案，不要把原始在线配置直接交给
旧版解析。以下仅是用户明确要求回滚且已确认配置兼容后的镜像切换方法：

```sh
cd /home/ubuntu/project/account-pool-balancer
docker tag account-pool-balancer:rollback-20261104T062429Z account-pool-balancer:latest
docker compose up -d --no-build --no-deps --pull never --wait --wait-timeout 120 account-pool-balancer
curl -fsS http://127.0.0.1:8790/healthz
```

不要自动覆盖在线 data：升级后产生的账号/续期/密钥/统计会丢失，旧快照
的刷新 Token 也可能已失效。若之后已启用 OpenCode 或导入其账号，旧版
不能理解新 provider，需另行制定迁移/恢复方案。保留备份及回滚镜像，
不要无确认清理。

经用户批准，已删除预检容器 `account-pool-balancer-check-20261104t062429z`、
受限备份目录中的 `preflight-data` 敏感副本和 `preflight.log`。清理结果已记入
远程 `deploy.json`；线上容器未重启且保持 healthy。运行数据、版本源码快照、
回滚镜像、完整数据备份及构建/部署记录均保留，不随临时文件一起删除。
