# OVH 部署与回滚记录

## 当前部署（2026-11-04）

- SSH：`ovh-server`；公网：`https://ods.yeschoy.io`。
- 容器：`account-pool-balancer`；宿主监听仍为 `127.0.0.1:8790`。
- 已部署业务代码：`1c7a3b810c93e76c2c29f3e8429d8470c2b7f6ce`。
- 固定镜像：`account-pool-balancer:release-1c7a3b8-20261104T062429Z`。
- 镜像 ID：`sha256:7c0164c89d604238d864095b5995dd2c6c26fc72958fc5501bfa3743e0d8b9cc`。
- 线上 Compose 继续使用 `account-pool-balancer:latest`，现指向上述镜像。
- 现有数据绑定不变：`/home/ubuntu/project/account-pool-balancer/data:/data`。
- `ai-internal` 网络和 `opendesign-balancer` 兼容别名保留。
- OpenCode 总开关、兼容转换开关保持关闭；没有导入真实 Zen/Go Key 或调用其推理接口。

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

在用户明确要求回滚且确认配置仍兼容时，使用旧镜像而非重建旧工作树：

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

预检容器 `account-pool-balancer-check-20261104t062429z` 已停止；其
`preflight-data` 位于上述受限备份目录，包含敏感数据副本。仅在用户同意后
清理这些中间产物；运行版本快照和回滚备份不随临时文件一起删除。
