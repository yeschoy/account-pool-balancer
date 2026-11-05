# Token Harbor 号池合入本地主分支（2026-11-05）

用户要求「本地合入 main 分支」，并在查看分支后要求继续。仓库没有 main，本地主分支是 master；已说明按主分支 master 合并，不创建 main、不推送。

- 目标工作树：`/Users/lyh_god/GolandProjects/account-pool-balancer`，合并前 master=`e936769`。
- 来源：`tokenharbour-pool`=`43e6f94`，共同基线 `b586406`。
- 初始目标工作树没有已跟踪文件改动；原有未跟踪 `.worktrees/` 保持不动。
- 使用普通非快进合并，不强制覆盖任一分支、不删除来源分支。

## 冲突及语义合并

五个冲突文件：pool.go、admin.go、proxy.go、app.js、stats.go。保留主分支 Go 的 session/缓存偏好、固定出口核验、政策暂停与观测指标，同时接入 Token Harbor 原生协议、强亲和、严格 Responses 续传和支出确认硬停止。

自动合并仍产生的重复 RuntimeBase case、重复 Token Harbor 标签和空提示已去重；Token Harbor 切换默认进入完整推理概览，仍保留已有注册机、设置、实时注册日志及补验证/入池。

缓存写入统一从共享统计入口更新，兼容主分支 cw/CacheWrite 与来源分支 kw/CacheCreation 投影；二者是别名，不累加为两次测量。保留 Go 的测量覆盖、耗时、绑定与缓存偏好计数。概览绑定/策略/key 计数按所选池隔离。

注册设置保存保留新增路由和模型刷新间隔；GET/PUT 响应不输出验证码 Key 或代理认证，留空保留已存凭据，显式 clear 才移除。新增合并回归测试验证这些跨入口合同和缓存别名持久化。

## 验收

- `go test ./...` 全仓通过（含 Go、原生 Token Harbor 和注册机测试）。
- `go test -race ./...` 全仓通过；`go vet ./...` 通过。
- `go build ./cmd/odb`（输出至已登记临时目录）通过。
- app.js / tokenharbor.js / perch.js 的 `node --check` 通过；`git diff --check` 通过。
- 真实本地浏览器：单一 Token Harbor tab，默认 overview，注册导航保留；两次本地 Mock Messages 均 200、同一账号；read=40、cw=kw=60、写入测量次数=2、缓存率=1/3；未知测量率=null。注册设置保存后 sticky 和 60 秒刷新间隔保留。Go 页缓存亲和/政策暂停/出口核验卡片仍存在，注册导航在 Go 页隐藏。
- 只读打开注册机页面，未启动注册、验证码解题或真实模型调用。所有账号/key 与数据均为本地假数据，上游为 127.0.0.1 Mock；未访问生产、未部署。

本轮临时构建、Mock、假数据、日志、PID 和浏览器结果位于 feature 工作树 `.tmp-tokenharbor/merge-main/`，登记于主工作树 `_tmp_artifacts.md`；测试进程停止后文件保留，待用户同意再清理。Trellis 记录保留并随合并提交。

## 用户授权推送与部署（2026-11-05）

业务提交 `9063068` 已推送 origin/master，并从明确提交的独立归档构建
OVH 固定镜像 `account-pool-balancer:release-9063068-20261105T091131Z`。
断网数据副本预检、一致完整备份、目标单服务切换及本机/公网验收均通过。
52 个账号、9 把下游密钥、2 把导入密钥及既有设置/凭证/代理/卷/网络保留，
OpenCode 开启和兼容转换关闭状态不变。构建 7 秒，0.25 秒采样的观测不可用
窗口约 2.253 秒，healthy 确认等待不是停机。公网三个资源哈希均与版本快照
匹配，未登录生产管理员、未主动调用付费推理/注册/验证码。完整记录与回滚
约束见 `docs/ovh-deployment.md`；预检容器和临时副本登记待用户授权清理。
本次后续只补文档，不因记录提交再次重建或重启。
