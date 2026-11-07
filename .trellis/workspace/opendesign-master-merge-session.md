# OpenDesign 合入本地 master

## 授权与保存
用户明确目标是 master，并授权先提交主工作区当前改动，再合并本地 opendesign-pro；不推送、不部署，不自动保存或清理敏感诊断资料。

- 原 master：2217151，保留原有3b0e83b/4832552/2217151历史。
- 先完成现有日志/配置/传输开关相关定向普通测试、Node行为/语法与本地build，按文件哈希确认检查期间未被并发改写，将28份现有业务源码/测试/文档/Trellis记录及必要ignore保存为 `e426272`。
- `.tmp_threbalance/`、`_tmp_fingerprint/` 为既有本地诊断目录，可能含账号/会话资料；仅增加ignore防误入库，未读取正文、未提交或删除。保存原有传输源码不等于已验证其真实上游效果，也未更改其默认开关。

## 合并与冲突处理
将 `d329c5f`（opendesign-pro）以普通保留双亲的合并合入 master，连同其已包含的8d852b6/f99fa52 Mistral历史一起保留；没有强推、reset、rebase或覆盖在线配置。

- `internal/server/admin.go`：TokenHarbor保留显式、管理员/no-store的已保存运行Key读取例外；OpenCode/Mistral继续拒绝完整上游Key读取。设置投影同时保留 Mistral 和 tokenHarbor.detailLog。
- `internal/server/web/app.js`：保留 TokenHarbor显式Key复制提示/按钮、独立日志开关和logFields保存隔离；合并 Mistral文案/设置，且不为其新增Key复制权限。OpenDesign诊断与旧值标记保持完整。
- OpenDesign Node测试原先截取至change监听器，主分支新增click监听器后把真实DOM绑定也带入VM；仅将测试边界改为首个账号表监听器，不删除断言或改变生产事件行为。

## 合并后验收
- `go test -mod=readonly ./...`、`go test -mod=readonly -race ./...`、`go vet -mod=readonly ./...`、本地 `go build -mod=readonly ./cmd/odb`（显式输出至忽略目录）全部通过。
- 全部 web/*.js 与 *_test.js 语法检查、6套Node行为测试通过，包含Mistral、OpenDesign、TokenHarbor保存Key复制、支付URL、额度展示及其他TH行为。
- diff空白检查及无未解决冲突检查通过。此次是合并集成检查，没有重复真实浏览器或生产验收，没有启动服务、注册/付款/推理或访问生产系统。
- 构建/测试日志/文件哈希清单在原OpenDesign工作树 `.analysis/opendesign-merge/`，已登记两工作树的 `_tmp_artifacts.md`，待用户同意后定向清理；Trellis记录与合并提交保存。
