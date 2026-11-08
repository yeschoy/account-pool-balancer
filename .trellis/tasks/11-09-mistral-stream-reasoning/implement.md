# 执行计划

- [x] 核对公开原生字段和现有路径/模型ID，记录证据；新增定向失败测试，先确认现有缺陷。
- [x] 修复Mistral Responses首帧终态/失败终态/EOF分类，验证不重放、不误记成功、不发假终止帧。
- [x] 抽取三模型公共思考控制，保留max、缺失默认high；保持DeepSeek其他兼容及Large4/其他池原生隔离。
- [x] 增加多模型多轮/工具（Deny、apply_patch换行和转义）/缓存/流与非流回归。
- [x] 更新README、Mistral指南、UI过时文案及对应Node测试；隔离本地浏览器核对。
- [x] 完成gofmt、Go全仓normal/race/vet、build到登记目录或/dev/null、Node行为/语法和全差异复核。
- [ ] 更新workspace，归档任务并提交本轮代码/文档/Trellis；不推送/部署。

## 验证命令
`go test ./internal/server -run 'Mistral|Terminal' -count=1`
`go test ./...`
`go test -race ./...`
`go vet ./...`
`go build -o /dev/null ./cmd/odb`
`node --check internal/server/web/app.js`
`node internal/server/mistral_web_test.js`及其余现有web行为测试。

## 风险与回退
共享proxy.go每个行为改动必须Mistral Responses窄门控；旧SSE路径/其他池应原样。模型思考适配不扩散DeepSeek的store/图片/seed语义。任务开始工作树干净；提交仅本轮修改；源码回退不改线上数据。临时公开浏览器产物/本地假数据服务登记_tmp_artifacts.md，测试结束停止本轮进程，未经同意不清理文件。
