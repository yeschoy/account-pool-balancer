# 执行计划

1. 安全错误解析与固定分类；新增 OpenDesign 分项观察、来源哈希和展示投影。
2. 接入专用独立只读刷新、旧值保留/退避/来源防护；管理 API 返回结构化诊断。
3. 修改列表、按钮与旧额度/窗口展示；其他 provider 保持原合同。
4. 增加 quota/server/JS 回归，验证不发推理、不改配置/隔离、不会泄露任意正文。
5. gofmt；go test ./...；go test -race ./...；go vet ./...；go build -o .analysis/opendesign-diagnostics/odb ./cmd/odb；全部既有 JS 行为与 node --check；真实隔离浏览器验证刷新动作与后端状态。
6. 更新规格/验收记录，代码及 Trellis 一并提交；只反馈本地完成，不宣称部署/上游修复。临时目录/日志/Mock/浏览器产物登记并待用户同意清理。

风险重点：500不能当作封禁或没钱；钱包0不等于套餐用尽；workspace active不解除封禁；过期窗口不UI推算满额；迟到结果与敏感正文不能入库/回显；超时不阻断独立分项。
