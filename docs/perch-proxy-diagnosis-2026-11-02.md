# Perch 代理封号风险诊断

日期：2026-11-02

## 结论

当前证据不能解释“运行约十分钟后封号”的精确触发条件。`perchai-cli@2.4.121` 的本地材料是静态客户端分析，没有 Perch 服务端风控规则、真实封禁响应记录或这批账号的运行时网络证据。因此，不能据此断言是某个请求头、TLS 特征、出口 IP 或请求频率单独导致封禁。

但本项目已不只是兼容一个公开 API：代码明确针对 Perch 的 CLI surface 限制构造请求，并在账号池中处理套餐拒绝、限流和封禁。最严重的风险是绕过产品入口/套餐边界，以及收到限制信号后仍切换其他账号继续同一请求。这比“是否漏了 CLI 的某个字段”更直接，也可能扩大账号损失。不要继续尝试让代理伪装成官方 CLI；应先停止该入口上的池化调用，并向 Perch 确认获准的集成方式。

## 证据与判断

| 优先级 | 本地观察 | 判断边界 |
|---|---|---|
| P0 | [config.go](/Users/lyh_god/GolandProjects/account-pool-balancer/internal/config/config.go:549) 将 CLI surface 设为默认，并说明前置文案用于通过 `perch_surface_required`；[perch_proxy.go](/Users/lyh_god/GolandProjects/account-pool-balancer/internal/server/perch_proxy.go:77) 检查该门槛并构造 CLI 请求。[prefix.go](/Users/lyh_god/GolandProjects/account-pool-balancer/internal/perch/prefix.go:14) 也把它描述为内容门槛。 | 这是明确的访问边界规避风险，不是普通的协议兼容缺口。即使请求成功，也不能证明 Perch 授权了这种代理使用方式。 |
| P0 | [rules.go](/Users/lyh_god/GolandProjects/account-pool-balancer/internal/rules/rules.go:255) 将 `turn_rate_limited` 设为模型冷却；[proxy.go](/Users/lyh_god/GolandProjects/account-pool-balancer/internal/server/proxy.go:710) 冷却或隔离后仍可切换账号，[proxy.go](/Users/lyh_god/GolandProjects/account-pool-balancer/internal/server/proxy.go:557) 会继续执行后续尝试。 | 限流、套餐拒绝或封禁可能从单账号扩散为池内轮换。即使服务端只对单账号发出信号，这种行为也可能被视作规避限制。静态代码不能证明线上实际发生过几次轮换。 |
| P0 | [perch.go](/Users/lyh_god/GolandProjects/account-pool-balancer/internal/server/perch.go:292) 把上游目录里的所有模型标为可用；注释明确表示 `locked` 标记不会阻止代理调用。 | 目录展示和套餐 entitlement 没有保持一致，可能把不属于该账号套餐的模型暴露给下游。应按账号实际授权过滤，并对未授权模型本地拒绝。 |
| P1 | 项目用 Go 将 OpenAI 请求转换成 Perch 请求，见 [perch_proxy.go](/Users/lyh_god/GolandProjects/account-pool-balancer/internal/server/perch_proxy.go:90) 和 [gateway.go](/Users/lyh_god/GolandProjects/account-pool-balancer/internal/perch/gateway.go:274)。本地 CLI 分析记录了更完整的官方客户端生命周期；相关记录注明静态覆盖不完整，未观测线上执行。 | 这不是官方 CLI 的完整运行环境。协议结果相似不等于客户端、授权范围或服务端处理相同；补齐模拟细节不能证明合规，也不能可靠预测风控结果。 |
| P1 | [pool.go](/Users/lyh_god/GolandProjects/account-pool-balancer/internal/pool/pool.go:421) 规定账号 `maxConcurrent` / `maxRpm` 为 `0` 时不设上限；[pool.go](/Users/lyh_god/GolandProjects/account-pool-balancer/internal/pool/pool.go:479) 仅在上限大于零时执行限制。 | 未配置正数上限时，单账号可并发承接池内流量。应在获准用途下设置保守的非零并发与速率上限，并增加整个 Perch 组共享的发送预算。 |
| P1 | [config.go](/Users/lyh_god/GolandProjects/account-pool-balancer/internal/config/config.go:600) 包含客户端身份模拟开关；[device.go](/Users/lyh_god/GolandProjects/account-pool-balancer/internal/perch/device.go:1) 维护对应状态。 | 这是代码里的仿客户端设计，不是服务端检测规则的证据。报告不展开可复用的身份参数或伪装步骤。 |
| P2 | [perchtelemetry/reporter.go](/Users/lyh_god/GolandProjects/account-pool-balancer/internal/perchtelemetry/reporter.go:143) 的可选 `cli-turn` 上报包含 prompt 和回答；[config.go](/Users/lyh_god/GolandProjects/account-pool-balancer/internal/config/config.go:609) 注释记录遥测出站默认关闭。 | 保持遥测出站关闭。不要为模仿客户端而发送额外会话数据；如产品明确授权，仍需单独评估用户同意、数据最小化和保留期限。 |

## CLI 包证据范围

本地包为 `perchai-cli@2.4.121`，相关材料位于 `.analysis/perchai-cli-2.4.121/`。静态分析记录了包身份和哈希，但也明确注明整包解析不完整、动态行为未观测、封禁原因未知。项目的 `go.mod` 没有将该 CLI 声明为运行依赖；仓库中的请求由本项目自己的 Go 实现发出。

本地静态材料说明 CLI 包存在多种模型调用周边流程和错误处理分支；这些只能说明客户端实现，不能反推出服务端的封禁算法。静态包分析没有复现“十分钟封号”，也没有 Perch 风控服务端代码。此前生成的 [pool-management-report.md](/Users/lyh_god/GolandProjects/account-pool-balancer/.analysis/perchai-cli-2.4.121/pool-management-report.md) 应按当前工作树重新核对后再执行：本次复核发现它提到的批量额度 `nil` 错误路径已有 nil-safe helper，刷新逻辑也已使用 session revision 检查，不能直接当作现状缺陷。

## 建议整改顺序

1. 暂停通过受限 CLI surface 进行池化模型调用；关闭自动注册/自动补号功能，直到 Perch 明确授权该用途。不要对限制或封禁响应换另一个账号重试。
2. 对 `banned`、`perch_surface_required`、套餐拒绝和 `429` 设置池级熔断：停止该池后续发送，按服务端 `Retry-After` 等待；只有经确认属于单账号凭据失效的情况才隔离该账号。
3. 将模型目录按每个账号真实 entitlement 过滤，不把 `locked` 或未授权模型作为可调用能力发布。
4. 对获准用途设置非零 `maxConcurrent` / `maxRpm`，并增加组级总预算、全局熔断和限流状态面板。`0` 当前表示无限制，应避免成为生产默认。
5. 保留脱敏诊断：时间戳、上游状态码和结构化错误码、请求 ID、重试次数、账号不可逆摘要、出口类型及限流等待时间。不要记录 Bearer/Refresh Token 或完整 prompt/回答。
6. 向 Perch 支持确认允许的 API、套餐范围和代理集成方式。只有拿到受支持的接口/授权后，再做协议兼容测试；测试使用虚构凭据和本地 mock。

## 对“十分钟后封号”的可验证边界

“十分钟”可能对应服务端策略、账号来源/授权、请求量、多个账号共享的使用方式或其他运行时因素；这些目前都只是待核查假设，不是结论。要进一步归因，至少需要一段脱敏时间线：首次与最后一次成功时间、每次上游状态码/错误码、请求 ID、重试与账号切换次数、封禁状态来自哪个 Perch 页面或接口。不要提交任何 session/token、邮箱密码、完整 prompt 或代理凭据。
