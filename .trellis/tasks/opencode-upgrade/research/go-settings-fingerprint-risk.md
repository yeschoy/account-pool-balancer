# Go 设置、指纹与风险控制只读审查（2026-11-05）

## 本轮请求

用户要求基于本项目，详细审查 `jasonxu114514/opencode2api` 的 OpenCode Go 设置、指纹、防封策略。这轮授权是分析，不是改生产或实施身份模拟。

## 方法与产出

- 复用已有 `opencode-upgrade` 任务，direct 执行，没有委派子代理。
- 固定参考源码 `58f56676ba1438adb30604b889bf36db2a1542a1`；生产镜像为 `1c7a3b8`，只读核验配置与缓存。
- 当前官方 Go/Harnesses/Terms 文档补充校验，参考实现注释与官方事实分开标注。
- 完整报告：`docs/opencode-go-risk-audit.md`。
- 临时克隆及工具溢出路径登记 `_tmp_artifacts.md`；未经同意不删，Trellis 证据永久保留。

## 关键证据与结论

1. R/`gateway/upstream.go:inferenceClient`：Bun/uTLS 仅用于免费模型的 `/zen` 或 `/inference`，默认 Go `/zen/go` 仍普通 Go client。
2. R/`gateway/warp.go:usesWARP`：Go 排除于 WARP 范围。
3. R/`httpx/client.go`、`gateway/upstream.go:setV2Headers`：Go UA 与会话头由网关声明为官方 CLI 形状；官方当前建议自有 UA + 稳定会话，不能认为必须照抄。
4. 本项目 `proxy.go:copyRequestHeaders/attempt` 仅透传 UA/session；`identity.go` 的 HMAC 只在内部选号，缺失上游会话头不自动补齐。
5. 生产账号并发/RPM 均 0（不设上限），Go 独立路由 nil，继承全局宽超时。
6. `proxy.go:evaluate` 政策 stop 只结束当前请求，没有后续独立请求的政策熔断。
7. 目录 28、可路由 27；缺少 `gemini-4-argon` 的 Google 原生协议，不是 Key 额度问题。27 个协议为 chat 17、responses 5、messages 5。
8. 真实 usage/目录读取已通过；推理样本和计数仍为 0，不能声称推理或防封实测通过。
9. R/`admin/model_quotas.go:fetchQuota` 未复用账号代理 transport；本项目查询/推理固定代理配置一致。
10. 官方限制批量/多账号规避用量等行为；账号池技术能力不等于 Go 订阅授权。没有官方安全 RPM、IP 账号数或永不封禁指纹阈值。

## 建议但未实施

- 真实 UA 与稳定 session 兜底、独立 Go 路由、低并发与本地 RPM、专用政策告警/熔断。
- usage 连续失败降级、工作区/订阅共享限额、显式官方协议 Base 与最小实际推理验收。
- 不伪装官方 CLI，不拷贝官方 prompt/tools，不以随机 session/project、WARP/代理/账号轮转规避政策。

## 用户后续确认与文档收敛

- Gemini/Google 协议暂不适配，保持未知协议不参与路由；不通过猜测 Chat 协议强行增加可路由数。
- 主报告第 10 节已逐项记录六类不建议做法的原因、替代方案及正常使用边界：假官方 UA、官方 prompt/tools 注入、每请求随机 session/project、重启换身份、默认动态代理/WARP、多 Key/账号规避限制。
- 推荐实施顺序、优先级、保守配置值、错误矩阵及计费/隐私边界仍以主报告为准；都是待实施建议，不构成生产修改或付费推理授权。

## 验证

- `go test ./internal/opencode -count=1`：通过。
- `go test ./internal/opencode ./internal/config ./internal/quota ./internal/pool ./internal/rules ./internal/server -run 'OpenCode|FetchUsage' -count=1`：匹配测试通过；opencode 包无匹配，已用上一条补跑完整包。
- `git diff --check`：通过。
- 未修改业务代码、生产设置或凭证；未付费推理、未重部署、未抓包、未测试封禁触发。主任务仍 in_progress，未完成项不能被这轮审查消除。
