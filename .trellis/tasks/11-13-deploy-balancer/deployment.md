# OVH 部署与10分钟自然观察（2026-11-13北京时间）

## 授权与发布范围
用户补充要求：提交推送后部署OVH，然后完整观察10分钟；若发现本次代码问题继续修复。
- 功能分支feat/deploy-balancer已推送：业务3c6a87f，测试隔离4c50007。
- 实际线上为b786d7c；主线另有未上线ModelStudio，未直接发布功能分支全量树。自b786d7c整理release/deploy-pool-20261113的c6ae4b2b50fa3a8295715a9c144c3dfb90a2907d并推送，只保留Deploy改动；测试fixture不再依赖未上线provider。原master未改动。
- 发布基线不同，追加相关normal（server16.698秒）/race（server4.391秒）/vet/build、Deploy与导航JS验证；不重复完整测试或发生产推理。

## 预检与保护
线上192账号、186条TH注册、18个既有硬隔离；healthy0/无OOM，双内网别名与Healthcheck保留，约239.7GiB余量。668个Git文件SHA256核验。
首次预检因Mistral一条queued候选中止，服务未停、未构建。只读确认该候选最后更新时间20:22，距检查超过1小时；容器只有odb进程。源码明确Add仅保存，需显式register才启动，worker上限15min。因此只将旧检查收窄为：running必阻断；未知/近期queued必阻断；超过20min且无任何其他进程的仅保存queued可保留。未更改/取消/删除候选，复用同一已校验源码和空备份目录，不重复建立发布树。Docker top仅comm在该daemon不支持，改用已验证的pid,comm列；仍在停服前失败保护内。

新镜像构建7.24秒；以线上UID1000:1000、network none、data tmpfs执行必要断网旧配置/metadata启动与签名布局checkpoint往返，health200/正常退出0，config/routingSecret不变，tmpfs已释放，不发推理。旧服务正常退出后做完整一致data.tar.gz，gzip/SHA256通过；沿线上Compose只覆盖镜像，诊断变量仍为空，保留环境/资源/用户/卷/端口/Healthcheck/网络别名。

## 切换与入口验收
| 阶段 | UTC 2026-11-12 |
|---|---|
| 构建 | 21:35:41.819–21:35:49.055 |
| 断网兼容预检 | 21:35:49.886–21:35:50.610 |
| 停止请求 / 旧退出 | 21:35:50.734 / 21:35:50.843 |
| 完整一致备份 | 21:35:51.213–21:35:55.635 |
| 切换请求 / 新启动 | 21:35:55.635 / 21:35:55.872 |
| 首HTTP / Docker healthy | 21:35:56.976 / 21:36:26.211 |
| 双内网入口 / 本机资源 | 21:36:26.379 / 21:36:26.486 |
| 公网真实浏览器 | 21:38:33.092 |

旧退出至首HTTP观测约6.13秒，非精确停机；构建/健康等待/总耗时不算停机。上线验收后立即反馈用户，不等文档或观察结束再告知。

Docker healthy/0重启/无OOM，NewAPI分别请求account-pool-balancer和opendesign-balancer两个healthz成功；config.json完整字节、192账号、186TH核心注册、routingSecret、Mistral注册文件、18个既有硬隔离均保留。公网ods.yeschoy.io真实未登录页、healthz/四资源200，保留?v=1d8bfed439并逐项SHA256匹配release，Deploy标签存在；回执已写正式deploy.json，专属浏览器已关闭。

正式source：/home/ubuntu/project/account-pool-balancer-releases/20261112T213203Z-c6ae4b2。
镜像：account-pool-balancer:release-c6ae4b2-20261112T213203Z，ID sha256:676dc320a09c0de9b52f91bed5b83fedf15c34e8ee556926a88b16409254cc7a。
一致备份：/home/ubuntu/backups/account-pool-balancer/20261112T213203Z-deploy-pool-c6ae4b2，data SHA256 58e150cb2cae5dee4d631962f88829a9a9748226f210e85a196efcba6b27fe80。
回退镜像：account-pool-balancer:rollback-20261112T213203Z。回退先保护上线后新写入，不自动还原旧data。

## 观察
固定窗口UTC21:37:47–21:47:47（北京时间11月13日05:37:47–05:47:47），完整600秒。观察器21:47:49.146退出0、stderr0，独立核对进程已退出；43次采样两服务均healthy/0重启/无OOM，始终同c6ae4b2，config hash始终未变。复用只读观察器，不发推理、不改设置、不采正文。按完成时刻ts+durationMs统计Balancer请求，错误事件按事件时刻分列；NewAPI以只读SQL交叉核对，不把attempt错误与最终请求失败重复相加。

| Balancer号池 | 成功200 | 最终失败 | 客户端取消 |
|---|---:|---:|---:|
| OpenCode Go | 35 | 0 | 1（499） |
| River | 7 | 0 | 0 |
| Modal | 2 | 0 | 0 |
| OpenDesign / Mistral / Ollama / TokenHarbor / Perch / OpenCode Zen / Deploy | 无自然流量 | 未据此验收 | 未据此验收 |
| 合计 | 44 | 0 | 1 |

窗口内Balancer错误事件0。NewAPI另外13条type5错误经只读渠道路由核对：215直连api.deploy.com为4条400；158/162配置指向cctoken.store，共7条429信号；217配置指向ai.furry.vg，为2条504信号。它们不经过本次Balancer，不能归因本次发布，也不把NewAPI同请求的type2取消与type5重复算成Balancer失败。167指向opendesign-balancer的1条为客户端取消，与Go499对应。未擅自修改这些直接渠道的参数或重放私密请求。

21:48:49独立最终核验：仍c6ae4b2 healthy0/无OOM，NewAPI双入口均成功；192账号、config完整字节与发布一致备份相同、routingSecret/Mistral注册文件及原18硬隔离保留。已立即反馈观察完成；当前没有可归因本次改动的其他池回归，不触发修复/回退。样本有限且多个池无流量，不声称所有客户端或长期零错误。

## 范围边界
上线的是空的Deploy管理入口和代码，没有自动向生产导入测试Key/账号或创建上游资源。Deploy自身真实推理已在本地授权验证，不能冒称生产Deploy渠道验收。组织余额/账单会话仍未绑定，完整新号池门禁未全部通过。临时文件见_tmp_artifacts.md，未经同意不清理；Trellis/正式release/备份保留。
