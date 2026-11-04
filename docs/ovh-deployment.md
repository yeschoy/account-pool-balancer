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

## 当前部署（2026-11-04）

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
