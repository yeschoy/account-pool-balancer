# OpenDesign 只读状态检测与额度故障分层展示

## 目标
管理员无需从 502 猜测封禁或欠费；只读检测独立呈现运行密钥、控制面、套餐窗口、钱包、账单的实际结果。用户已批准实施此前建议，限定本地代码与测试，不部署。

## 证据与边界
- 运行 `/v1/models` 对两份已停用账号明确返回 403 / forbidden_api_key / api key owner banned；同一账号工作区仍 active，因此 active 不是未封禁证明。
- 另四份账号模型接口 500 权益检查失败，preflight 503 billing_preflight_unavailable，summary 503 database_unavailable；不能归因为余额不足或保证未封禁。
- `internal/quota/client.go` 当前目录错误丢弃错误码；`internal/quota/poller.go` preflight 失败提前返回，阻断 wallet/summary；`internal/server/admin.go` 刷新失败统一 502。
- `internal/server/web/app.js` 使用过期窗口的 limit 代替 remaining，缺少分项成功时间与错误说明。

## 需求与验收
1. R1：仅固定、可审计证据识别封禁；普通 403、workspace active、目录成功均不能证明推理授权。区分密钥失效、权限不足、限流、上游故障及未知；不保存上游任意正文/凭据。
2. R2：OpenDesign 手动刷新强制只读目录与控制面检测；套餐、钱包、账单独立读取并记录结果，分项失败保留旧值及原成功时间。每项超时受限；只 GET，不调用推理/付款/重授权或换出口。
3. R3：沿用刷新 API，HTTP200 表示检测完成，以 ok/quotaRefreshed/diagnosis 明确部分失败；不伪称刷新成功。保存下次自动重试时间；不改变其他 provider 的 API 语义。
4. R4：列表显示明确分类、证据时间、分项错误、上次成功及下次尝试；套餐额度与钱包金额分离。未知/查询失败不归零，窗口过期不在 UI 推算满额；已有调度策略不变。
5. R5：诊断绑定账号凭据/控制与运行基址/代理来源，配置变化与迟到结果不能复活旧证据；诊断元数据兼容旧文件，副本不共享可变指针；不自动启禁用或改变隔离。
6. R6：本地 Mock 覆盖明确封禁、普通403、密钥失效、500/503、耗尽与钱包0、无控制密钥、部分成功、超时、恢复、来源变化、旧数据与敏感信息。通过 Go normal/race/vet/build、JS 行为/语法及真实隔离浏览器验收。

## 不在范围
上游数据库修复、绕过限制、推理授权证明、收费测试、自动充值/账号启禁用、调度策略调整、生产部署。临时产物登记 `_tmp_artifacts.md`，未经同意不清理；Trellis 记录与代码一并提交 Git。
