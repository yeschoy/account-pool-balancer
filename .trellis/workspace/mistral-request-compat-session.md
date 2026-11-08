# Mistral 参数兼容新工作区会话（2026-11-08 UTC）

- 用户完整交接并授权方案/实现/部署，要求新工作区、独立拥有、不再启动代理。工作区`/Users/lyh_god/orca/workspaces/account-pool-balancer/mistral-request-compat`，分支同名，Orca默认基线319c751包含8d41516；不重置其他分支、不在原master开发。
- 已读本地及原上级AGENTS、共享workflow/身份tanggod、backend/mistral/错误及质量规范、共享思考指南、完整15fdea0诊断证据。共享脚本被repo部分.trellis遮蔽，按用户允许手工维护最小PRD/design/implement/验收记录，没有重搭系统。
- 实现96eaad7：精确DeepSeek Chat developer角色值转换、思考开关语义、enabled默认high、seed→random_seed、无意义默认/标记窄兼容、store严格保护；原值身份与cache/metadata保留。字段跨度拼接解决连续删除/重复字段/大数工具数据无损；明确本地400在账号/RPM之前，失败不删参数重放。Mistral响应逐帧兼容保持不变。
- 官方浏览器复核Chat schema integer|null random_seed、metadata与prompt_cache_key；不将泛schema或mock当具体DeepSeek支持证据。未生产验证seed确定性；有效但不支持的生成/retention语义不静默丢弃。
- Mistral定向/相关回归2.739s通过，自审小改后新增6组参数测试0.584s通过，gofmt/diff check/build通过；没有全仓/race/vet或主动生产推理。首次fixture字段编译错误已修正。
- fetch保留master=e06a589独立Mistral工具更新，无应用代码重叠，正常合并为9f375ef并原子推送功能分支/master；不运行注册工具、不覆盖并发更新。
- 复用原部署脚本仅内存适配，9f375ef干净archive构建9.25秒；完整一致备份、仅镜像切换，06:00:34.988本机与06:01:15.204公网登录页/health/四资源哈希验收通过并立即反馈。healthy/零重启/无OOM；161账号/186注册记录关键资产、routingSecret、完整config及原运行参数保留。旧退出至首HTTP可达观测约5秒，不冒称精确停机。正式备份、rollback及具体时间见docs/ovh-deployment.md。
- 06:00:34.988–06:04:33.034自然窗口24/24真实流式success，NewAPI 24条ok/done，Mistral无错误事件；cache2119808/input3195540=66.34%，16hit/8miss，无溢出/请求内切号/多attempt。该窗口非完整10分钟，不作受控缓存改善结论。
- **未完成项/需要确认**：渠道180最新读取发现同时服务两种GLM模型，不能按原设想移除全渠道operations。本轮没有写NewAPI数据库/配置，旧规则仍删metadata/cache与绕过store=true保护。仅DeepSeek Chat条件化收敛可保留GLM/其他路径，但运行NewAPI ad556799精确源码对象不可用；须确认方案并核实运行条件支持，不能盲加条件、整行覆盖或先清空规则。任务维持in_progress等待此边界确认，不虚报整体交付完成。
- 临时产物登记当前`_tmp_artifacts.md`（ignore）：公开文档自动snapshot与本地odb构建；未下载生产原始请求/凭据，未经同意不清理。Trellis/正式备份/业务资料保留。后续仅补记录，不重新部署文档提交。

## 用户授权清理、合入master及部署复核

- 用户明确要求“清理吧，合入master，之后部署到ovh中”。按本轮登记清单精确删除当前工作区`odb`（16653938字节）、公开文档自动快照`pi-agent-browser-snapshot-b488fa98c27a25e6.json`（38564字节）及`_tmp_artifacts.md`（686字节），逐项确认不存在。没有清理历史任务产物、Trellis、生产数据、正式备份、发布源码或新旧镜像。
- fetch确认origin/master仍1d746ef，已包含96eaad7与9f375ef；主目录master为e06a589、工作树干净且未分叉，可仅用`--ff-only`同步最新记录，不在主目录开发、不reset/强推。
- OVH只读预检：仍运行`9f375efee5a89e92dcbe5f14dd0385e4519c1501`，原镜像ID未变；Balancer/NewAPI均healthy、零重启、无OOM，healthz200、数据挂载不变，旧e6f063b树干净、磁盘285.6GiB余量。Git核对运行提交与远端master仅差docs/.trellis记录，全部应用代码一致；依据部署约定复用既有上线验收，**无需重新构建/部署或重启**。本轮未产生停机、推理或生产配置变更。
- 清理/合并/部署授权不扩大为条件化NewAPI规则修改；渠道180规则仍未动，原范围确认门保留。仅提交此清理/复核记录，不因文档版本变化再次部署。

## 用户要求监控约10分钟错误率

- 2026-11-08 06:14:51.458–06:24:51.458 UTC完整600秒，完成请求口径（ts+durationMs），不计未完成请求。Balancer245请求/228成功/17失败，错误率6.94%，客户端取消0；全部失败是Modal上游429（17/89，19.10%）。OpenCode Go96/96成功。
- Mistral精确DeepSeek60/60 HTTP200、错误率0%，57流式+3非流式，无新增400/422/流式错误；NewAPI渠道180同窗口60条消费、57条ok/done，无错误记录。Mistral真实input10055835/cached8382592=83.36%，54hit/6miss，无忙溢出/请求内切号/多attempt。Modal42条尝试级429不能混作42个失败请求。
- 38次健康采样无异常，结束时两服务healthy/零重启/无OOM，运行revision9f375ef。5分钟时已反馈Mistral30/30及Modal4次429，10分钟完成即反馈最终结果。采样健康不冒称连续探针，缓存窗口不当受控提升证据；详见validation.md。
- 全程SSH/远端内存只读，递归错误解码只输出状态/loc/type；不发推理、改配置/规则或重启，无新增临时文件/日志下载/凭据副本。只提交观察记录，不重新部署；原NewAPI范围确认门保留。
