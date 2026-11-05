# 研究进度（规划阶段）

## 官网套餐模型与额度口径
真实浏览器读取 https://tokenharbor.ai/pricing 的Flight JSON，未登录、未创建订单。

- 公共模型组映射29项：free3、agent6、office12、frontier8；高级套餐包含低级的付费覆盖，但免费路线仍需用户单独同意，不能因覆盖列表而启用。
- 原始套餐 allowanceUsd：Agent10、Office35、Frontier180；Agent的20/周5是用户指定的2×Boost等效统计口径，并非钱包美元余额，也不代表所有模型都能2×。
- 本次公开promos仅展示deepseek-v4.1-flash-1017的multiple=2。不能把“总额20”描述成官网所有Agent模型一律可花20现金。
- 原生目录77项及low/mid/high tier不能直接做Pass授权；需精确关联官网组、实际别名和变体。不能验证的模型默认拒绝是推荐规则，需用户确认对既有调用的影响。
- 账号当前周用量、剩余百分比、重置时间尚未取得可验证数据源。不会按注册日期/统一周一推算，也不会以缺失百分比当0%使用。

## 会话身份已有实现与pi源码证据
- `internal/server/identity.go` 已识别Codex/Claude/OpenCode、父会话和通用session/thread/conversation字段；TokenHarbor专用逻辑使会话优先于共享prompt_cache_key。
- `internal/server/tokenharbor.go` 复用严格Responses来源/下游Key隔离；不能因负载压力绕过续传绑定。账号忙碌不能无声溢出，完整历史故障切换与服务端续传必须区分。
- 本地安装pi-ai 1.0.0源码路径：`.../node_modules/@earendil-works/pi-ai/dist/api/`：
  - openai-responses.js:207–266：按compat发送x-session-id或session_id，同时可能发送x-client-request-id；正文prompt_cache_key来自sessionId，但cacheRetention=none时不发送。
  - openai-completions.js:516–564：会话头受sendSessionAffinityHeaders控制；自定义base不一定有prompt_cache_key。
  - anthropic-messages.js:800–818：sendSessionAffinityHeaders开启时发送x-session-id或x-session-affinity。
- 现有服务端已支持x-session-id、session_id、x-session-affinity、prompt_cache_key；不能承诺客户端没发送任何身份时仍能精确会话命中。x-client-request-id不能无条件当会话，因为其他客户端通常每个请求都不同。后续加真实格式夹具和pi兼容配置说明，不凭UA伪造身份。

## 日志缺口
- `internal/server/proxy.go:383–465`：只有logs.detail=true才创建captureWriter、保存details；线上目前false，所以失败不自动有独立详细正文。
- 普通错误日志已有16KiB有界JSON/SSE原因；非JSON上游错误只保留类型/长度（redact.go:errorReason）。
- 完整detail目前只有OpenCode Go专门删除会话/缓存字段；TokenHarbor新增错误详情必须同步保护原始会话值，不只遮Authorization/Cookie。
- 推荐TokenHarbor失败请求自动保存有界脱敏详情并按requestId关联，成功默认不留正文；流中失败即使HTTP200也要有失败标识。避免全量捕获/无界复制和逐请求磁盘fsync。

## 生产只读基线（非压测）
一次SSH只读检查，无配置/容器修改，无真实推理。

- 主机16逻辑CPU、约62.4GiB内存，检查时MemAvailable约51GiB；还有其他业务，不能把所有内存分配给本服务。
- 容器未设置memory/memorySwap/nanoCPU上限；检查时约232.6MiB、CPU0.70%、22PIDs，不能用空闲占用推算高负载需要。
- routing.directMaxConns=512、proxyMaxConns=32、maxBodyMB=100、requestTimeoutSec=900。
- MaxConns是连接数，不是HTTP/2并发流数；需要基准确认是否限制共享代理场景，不盲目加到3500。
- logs.detail=false、detailBodyKB=64、maxDetails=500、maxTotalMB=512、maxErrors=10000；不要把源码默认6MiB正文上限误报为当前线上设置。
- 当前2个TokenHarbor号池账号；部署/测试不自动新增或启用账号。

## 代码路径上的容量风险（尚未基准定量）
- `proxy.go:fetchModels` 的普通分支在缓存miss时逐账号串行GET上游目录；TokenHarbor需要改成后台已验证快照合并，不能在模型列表或每次推理热路径读取官网。
- 3500RPM约58.3请求/秒；按3亿TPM，平均约8.57万token/请求。并发取决于平均持续时间：30秒约1750、60秒约3500、120秒约7000。
- 必须基准实际请求字节、流式持续时间、连接复用、分配/GC、P95/P99、失败率与日志模式；Mock的usage token字段不能冒充真实承载3亿token。
- 先按真实大上下文请求模型做本地负载，依据结果建议容器硬内存上限及GOMEMLIMIT安全余量；本轮尚未给生产应用任何参数。

## 当前待确认的一项关键产品规则
严格套餐模式下，无法确认套餐/模型归属的账号或模型，推荐标记未知并拒绝调度，不猜Agent，不通过钱包放行。该规则可能暂时减少手工Key-only账号或旧模型的可用性，需确认后作为验收契约。
