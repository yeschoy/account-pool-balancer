# 实施与验证

1. 核对官方 API、Claude 缓存、Codex/Claude 接入及限流文档，记录研究。
2. 修复配置 provider/base/normalize、独立 Token Harbor routing/规则与导入。
3. 接通原生转发与身份/续传，复用 binding 和调度，不变更其他池行为。
4. 接通模型目录轮询、错误策略与缓存 read/write 统计。
5. 控制台 Token Harbor 标签、账号/密钥、概览/日志/设置。
6. mock 回归测试与安全/兼容自审。
7. gofmt；go test ./...；go test -race ./...；go vet ./...；go build（输出临时目录）；node --check；本地浏览器烟测。
8. 补业务文档及 Trellis 会话记录，提交代码与 Trellis。临时数据记 _tmp_artifacts.md，未经同意不清理。

风险：proxy.go 全池共享；pool binding source 校验和 quota 状态可能跨重启；admin settings 不能泄露注册 secret 或重置其他池规则。
