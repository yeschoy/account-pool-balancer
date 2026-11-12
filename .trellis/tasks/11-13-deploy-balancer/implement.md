# 实施与验证

1. 读取 backend/provider-onboarding、OpenDesign、frontend 与共享思考规范；独立 worktree 基线确认。
2. 接入 config/provider/group/独立默认 Routing/Quota/rules、校验、API 设置隔离与导入隔离。
3. 接入目录刷新、来源校验、metadata 与账号能力状态、概览；研究真实余额可获取性并如实标注。
4. 接入 Chat/路径白名单、独立详细日志、真实默认模型测试，保留原始字段和重试安全门。
5. 扩展原生管理台池切换、账号、规则、路由、概览；共享设施只读说明，明确重试规则仅支持stop决策，可编辑条件/启停/排序；删除或停用可允许其他安全换号，Deploy stop不被冷却动作覆盖。
6. 添加配置往返/来源晚返回/规则运行/跨池/路径/导入/日志脱敏等 Mock 回归。
7. 本地临时服务 + 真实浏览器保存、刷新、重载，核对 API/磁盘后置条件；用户提供 Key 只做授权的小额真实调用。
8. `gofmt`、`go test ./...`、`go test -race ./...`、`go vet ./...`、`go build -o .analysis/deploy/odb ./cmd/odb`、`node --check internal/server/web/app.js`。
9. 更新 spec、validation 与文档；扫描提交不含真实 Key/临时数据；git diff --check；提交功能/测试/Trellis，区分本地验收和未部署。

## 高风险位置
config.go/opencode.go 的 provider 分支、admin.go 的 settings 保存与导入、quota 来源校验、proxy 协议允许列表、pool 目录/额度隔离、app.js 表单保存。每步限制为 Deploy 可选分支，已有池规则/配置不得被覆盖。

## 临时产物
统一登记 `_tmp_artifacts.md`。无需清理 Trellis。未经用户同意不删除测试数据或 worktree；停止本次启动的临时进程后保留文件。
