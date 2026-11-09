# 实施lane board — 2026-11-09

分类：multi-seam。Pool/配置和UI、请求转发资源、Stats/Logstore有独立源码与验证边界。用户已选择A并授权落实瓶颈；按Trellis主会话dispatch约定采用隔离组件worker，主线程保留集成/审阅/提交/发布决定。

所有lane禁止真实凭据/生产推理/配置变更/部署/清理既有产物，禁止嵌套agent；遇到跨边界改动先contact_supervisor。初始Dockerfile/注册机改动留在主树，不带入组件树、不提交。

| Lane | repo/cwd与隔离 | 独占文件/决定 | 权限 | Gate/交付 | 独立性 |
| --- | --- | --- | --- | --- | --- |
| main-pool-ui | 当前account-pool-balancer主工作树 | internal/config/config.go、opencode.go与新index文件；internal/pool/**；internal/server/admin.go、web/**；集成后server启动调用；会话容量与设置往返 | 主线程唯一writer；可本地提交 | Pool/Config/Node/浏览器、最终全仓门禁；不部署 | 核心会话策略和管理契约为紧耦合seam |
| resource | `.analysis/mistral-capacity-lanes/resource`，从实施计划提交建立的干净独立worktree | internal/config/mistral.go及专属新测试；server.go/proxy.go/identity.go/redact.go/Mistral请求与SSE资源、Transport/详情采样；不能编辑admin.go、web、pool或config.go | 隔离worker唯一writer；提交本lane；不push | Server/Config受影响normal/race、协议和隐私回归、基准；返回commit/差异/接口说明 | 热转发与资源占用可在Mock独立验证，主线程随后接入设置 |
| stats-log | `.analysis/mistral-capacity-lanes/stats-log`，同一计划基线的干净独立worktree | internal/stats/**、internal/logstore/**；按需/去重复统计与有界日志队列 | 隔离worker唯一writer；提交本lane；不push | Stats/Logstore normal/race、旧metadata/reset/retention和新容量基准；返回commit/证据 | 不依赖新路由/会话配置，独立数据层seam |

## 共享接口

- `config.Mistral.BindingCapacity *int`：nil/0共享旧4096，1..1000000独立Mistral普通绑定。resource lane只声明/校验该字段，Pool与UI由main拥有。
- `config.Mistral.Performance *MistralPerformance`与`EffectivePerformance()`：字段/默认与范围以design.md为准；resource lane声明/实现，main接API/UI。
- `pool.Request.MaxWaiters int`由main实现；resource Handler对Mistral配置0时Wait=0，其他情况传MaxWaiters；无需修改pool接口其他字段。
- 主线程新增`Pool.ConfigureBindings(cfg)`于启动导入前/配置回调，resource不重复实现绑定策略。
- Transport独立配置从有效Mistral routing取值，使用兼容的新增helper而保留其他provider/Quota调用行为；不能擅自将root Routing全局值改成Mistral值。
- 日志队列字节预算由logstore自身提供默认64MiB（可在Limits新增可选字段，但不要求改Config），保留原单写者/retention；新队列指标可由主线程接概览。
- Config查找索引由main实现，resource不要另建第二份账号/Key索引。脱敏cache可以基于不可变Accounts backing slice+revision，provider视图共享同一slice。

## 组件集成/审阅门禁

每lane仅提交自己的文件，交付commit和normal/race证据；主线程检查差异后集成，无清空/覆盖其他dirty文件。组件完成后以fresh只读review检查各自提交；最后主线程整体验证，接受实证缺陷再修。工作树/工具产物登记_tmp_artifacts，未经同意保留；Trellis记录提交Git。运行时外部组织/代理/NewAPI/Nginx和真实3亿TPM能力不能由Mock替代。
