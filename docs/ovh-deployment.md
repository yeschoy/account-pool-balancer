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
3. **备份切换**：在新镜像就绪后才进入停机窗口。按变更风险保留一致、完整
   的数据备份；只更新目标服务，保持卷/端口/网络、配置和功能开关不变。
   不自动用旧数据覆盖上线后的新写入，失败时先确认结果再执行既定回退。
4. **一次验收**：合并核对健康、运行镜像 revision、必要的数据/密钥/设置
   保留与新版资源。本地检查走既有接口；静态脚本实际路径是
   `/assets/app.js`，优先从 HTML 读取，不猜 `/app.js`。公网 Python 客户端
   的既有 403 不应误判为新版本故障；不改 WAF、不伪装身份或轮换出口，
   用正常真实浏览器校验可访问页面，遇到挑战如实报告而非反复尝试。

**反馈与时间预算**：缓存命中、无异常的单服务部署以 2–5 分钟为目标，
不是硬性 SLA；超过 5 分钟主动说明具体卡点。构建、停机切换、健康确认、
公网验收分别记录时间。必要验收通过立即告知用户，再补最小部署记录，
不要让写文档或提交耗时变成用户等待上线结果的时间。只改记录不重新部署。

## 当前部署（2026-11-05，TokenHarbor 日志与批量入池：d7b914f）

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
