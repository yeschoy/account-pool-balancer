# 工作日志

## 2026-11-16
- 独立worktree分支x50-account-pool，初始干净HEAD c5e0c4a（265fb04之上一个已提交ALTapi修改）。确认50X注册c9297fe与Deploy号池均已存在，无基线合并/移植。
- 读取本地/继承AGENTS、provider-onboarding/backend/deploy/frontend、logs/models-cache、公共workflow与thinking guides；公共get_context只读使用，缺失的worktree脚本/流程不补复制，不触碰其他会话指针。
- 用户追加第4项日志浮层，纳入PRD/设计/实现/验收；实际阅读Deploy renderDeployLogs与setLogExpanded、dock HTML/CSS。
- 实现Config.X50独立参数、规则、Bearer原生Chat/SSE、Token billing、严格余额校验/历史/来源保护/metadata、通用CRUD与Key隔离。
- 实现批量选中入池与真实持久化关联投影、默认隐藏/已入池复选框、重复/部分失败/目标删除恢复；推理代理all/selected确认和直连，不写注册历史。
- 前端复用通用页面及Deploy日志dock样式；独立状态、晚返回防护；修复首次startPolling取消当前首屏结果的问题。补注册日志与错误脱敏。
- 当前公开文档浏览器发现新账号无免费额度，所有Key共享账号余额/限流；更新文档与UI历史边界。未使用真实凭据，不做生产费用推理。
- 自动化首轮与最终完整normal/race/vet/build通过，15个Node套件通过；真实浏览器一次性mock完成设置/规则往返、入池隐藏、直连真实HTTP、日志开关/更新/跨池/重启持久化。详见validation。
- Trellis-check内联自审，无委派。复查发现错误规则refresh在关闭周期时应仍能生效，50X改为RefreshSession强制调度；保持别池语义。
- 只读核对原工作区时它已由其他操作者推进至24c92de且干净；本任务未写入/checkout/reset原工作区，不将其当前改动带入本分支。
- 临时文件/进程均登记，停止自建服务与mock但未经用户同意不删除产物。Trellis记录永久保留。

## 后续授权合并部署（UTC 2026-11-15 22:05，本地2026-11-16）
- 用户要求合入master、提交推送并部署。master=24c92de，原工作区出现Telnyx未提交修改，未stash/重置/覆盖；在当前独立worktree合入origin/master无冲突，保留50X代理协议纠正与ALTapi修复。
- 合并提交2986f74再次完整normal/race/vet/build与15Node通过，快进推送origin/master（不强推）；原master工作区本地指针不强行更新以保护脏修改。
- 复用已有rollout引擎，从干净提交归档787文件核验；7.18秒构建；同UID network-none tmpfs真实兼容预检通过；等待自然ALTapi worker空闲再切换，未取消任务/改自动配置。
- 完整一致备份/gzip/SHA256通过，沿线上Compose只覆盖image，保留256账号/注册数据/配置/routingSecret/旧隔离/运行参数/Healthcheck/兼容别名。双NewAPI入口通过，healthy零重启22:05:43立即反馈。
- 真实未登录公网浏览器登录页/healthz/7个版本化资源200且hash逐项匹配，22:07:57登记。一次串行抓取超时只读检查后改有界并行fetch完成，不改WAF、不重启容器。未执行生产50X入池/代理重置或付费推理。正式release/备份/rollback镜像保留，临时产物登记未经同意不删除。

## 上线后错误观察（UTC 2026-11-15 22:16–22:24）
- 用户要求观察并修复错误。只读核对health、双NewAPI入口、日志、metadata与部署前完整备份；未派agent、未写生产配置/账号、不发推理或重注册。
- 发现18个Deploy最终500/36条尝试，正文均Internal Server Error，与部署前21:54已有错误一致，最后22:08:55，后续自然请求成功。不是把所有500藏掉或无限重试的问题，未擅改重试/冷却规则。
- 连续180秒13采样healthy0重启无OOM，17个自然请求全成功（Deploy13、Go2、OpenDesign1、River1），新增错误0。旧OD封禁4、TH会话失效95/来源缺失1均与备份相同；需账号侧处理，不能用代码假修复。生产无50X账号，无法用自然样本证明50X上游业务。
- 未发现本次代码回归，保留当前运行版本，不做无意义重启/部署；观察证据补入validation与部署文档，临时汇总登记后保留。
