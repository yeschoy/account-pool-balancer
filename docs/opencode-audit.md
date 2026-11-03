# OpenCode 升级源码审计与实施边界

## 基线、证据与审计范围

主项目基线 `616defe`；参考 `jasonxu114514/opencode2api` 固定 commit `58f56676ba1438adb30604b889bf36db2a1542a1`（下文 R/ 表示其仓库根目录）。参考仓库只放在被忽略的 `.analysis/`，不参与构建。

这是一份按请求链路展开的架构审计，不是“逐行完整阅读所有文件”的证明。已读取下面列出的关键实现并追踪调用边界；符号索引/文件清单不等于完整审查。Perch 注册工具、全部历史测试、参考 WARP、所有 UI 页面及所有协议边缘分支尚未逐行完整审查，不宣称满足用户要求的全仓穷尽审计。

## 能力对照与复用决策

| 能力 | 主项目证据 | 参考项目证据 | 决策 |
|---|---|---|---|
| 账号/独立组/下游 Key | `internal/config/config.go`: Account, GroupPool, keyProvider, Normalize；`internal/server/admin.go`: accountInput, apiCreateAccount | R/`internal/config/config.go`、R/`internal/gateway/pool.go` 以 ZenKeys/GoKeys 和 upstreamNodes 组织 | 使用主项目实体，不引入第二套 KeyPool；新增两个 provider，保留每组单供应商不变量 |
| 开放/批量导入 | `internal/server/importapi.go`: importOne/importDecide，写锁内重新检查冲突、暂存；`admin.go`: apiImportAccounts | R/`internal/admin/api_keys.go` | 保留开放导入和暂存，OpenCode 明确声明 provider；不按 sk- 推断套餐 |
| 原子并发/RPM | `internal/pool/pool.go`: tryLocked/admitLocked/leaseLocked；Lease.MarkSent/Release，预留→提交→一次释放 | R/`internal/gateway/pool.go` 与 R/`gateway/upstream.go`: doKeyUpstream | 不移植参考池。网络 I/O 不进入主项目调度锁 |
| 八种策略 | `internal/routes/routes.go`: Order；config.Strategies | R/`gateway/pool.go` 的 key/session/proxy 选择 | 保留 smart、pace、weighted、roundrobin、least-used、least-selections、least-connections、ordered |
| 会话 | `internal/server/identity.go`: identity、responseFingerprint；`pool.go`: Bind、getBindingLocked、tryLocked | R/`internal/identity/request.go`: DeriveRequestIDs（带 caller scope）；R/`gateway/upstream.go` | 主项目当前身份只按 group 隔离；OpenCode 增加下游 Key scope。状态续传严格绑定，不能忙时溢出或丢失绑定后随机换号 |
| 原生协议 | `server/proxy.go`: attempt、poolPathOK、relayStream；旧供应商大多按客户端路径原样转发；Perch 独立翻译 | R/`models/catalog.go`: Route.Protocols 是 tier→protocol；R/`models/discovery.go`: protocolForSDK | OpenCode 每个套餐独立能力元数据。其他供应商不增加转换 |
| 转换 | `internal/perch/translate.go` 与 `server/perch_proxy.go` 是 Perch 专用，非通用三协议层 | R/`protocol/bridge.go`: ConvertRequest；request.go/response.go、stream_parser.go、stream_emitter.go | 独立实现受限转换；不复制。不能把存在路径当完整兼容。尤其参考 bridge 明确允许无法表达的 knob 被省略，主项目要求显式拒绝，不能照搬 |
| 错误与重试 | `server/proxy.go`: evaluate/writeFailure；`internal/rules/rules.go`: MatchError、CooldownAfter；SSE 提交后 outDelivered 不再切号 | R/`gateway/upstream.go`: isDeterministicClientResponse、observeKeyResult、doUpstreamTiers；first_event.go | 复用主项目错误规则与先验提交机制；新池独立规则。400/422 不扩散隔离；模型问题仅模型级；认证与代理分开 |
| 模型目录 | `quota/poller.go`: Snapshot.Catalog、NotEntitled、applyCatalog、dispatch；`server/proxy.go`: fetchModels（组内并集） | R/`models/discovery.go`: FetchModels、FetchCapabilities、FetchProtocolDocs；catalog.go: ReplaceWithCapabilities；cache.go | 使用账号级目录，不采用参考的套餐共享权限作为单账号授权证明。公开模型表只提供能力元数据 |
| Go 额度 | 主项目只有 OpenDesign/Ollama/Perch 专属结构；River/Modal 无可靠余额查询 | R/`admin/model_quotas.go`: decodeQuota、fetchQuota、refreshQuota、handleModelQuotas | GET `<Go origin>/v1/usage`，Bearer；usage.rolling/weekly/monthly 含 status、percent、resetsAt。百分比、非金额、可能共享订阅，不跨 Key 求和。失败保留旧值并标记 stale/unknown |
| 代理 | config.ValidateProxyURL 支持 HTTP/HTTPS/SOCKS5/SOCKS5H；server.transport 按 URL 复用且非法配置不直连；账号 ProxyURL 固定 | R/`config/proxy.go`: ParseProxyEntry 支持动态 pool 标记；R/`gateway/runtime.go`、pool.go 管理 proxy health | 优先固定代理，不加入动态出口池、WARP 或把换 IP 当额度恢复。查询也须使用该账号代理 |
| 配置/持久化 | config.Store.Update：Clone→Normalize→原子写文件→交换指针；Reload 先验证。server.metadata 保留 pool/quota/stats；fsutil 原子落盘 | R/`config/persistence.go`、`gateway/runtime.go` | 主项目机制已成熟，增加可选字段，不改版本/文件格式、不引入数据库 |
| 日志/用量 | requestLog/errorLog 严格投影，scanUsage；stats、logstore；redactSecrets、maskBody | R/`telemetry/metrics.go`、logging.go、admin/debug.go | 继续主项目 request/error/details 及 provider 筛选；不另起遥测存储 |
| WebUI | `internal/server/web/app.js`: POOLS、renderPoolTabs、loadAccounts、loadSettings；index.html 和 perch.js | R/webui/js/pages/*、store.js、api.js 是独立后台 | 扩展现有页面和组件，不覆盖整套 UI |

## 不能直接合并的语义

1. 主项目 `RuntimeBase` 已包含 `/v1`；参考的 upstream Zen/Go 是 origin，`wire.Path` 再加 `/v1/...`。本轮配置沿用主项目语义，防止缺失或双重 `/v1`。
2. R/`gateway/upstream.go:upstreamRequest` 对 Messages 使用 x-api-key + anthropic-version，Chat/Responses 用 Bearer；Go Messages 加 beta=true。不是统一 Bearer + Chat 路径。
3. R/`models/discovery.go:protocolForSDK` 将 @ai-sdk/openai 识别为 Responses、openai-compatible 为 Chat、anthropic 为 Messages。不能以“含 openai”都识别 Chat，也不能只用模型名前缀猜协议。
4. 主项目的 OpenDesign Go 是其会员层级，不是 OpenCode Go。不得把 OpenCode Go 百分比塞进 OpenDesign Design Plan credits，也不得触发后者套餐模型过滤。
5. 参考 `bridge.go` 的部分转换合并 developer/system 或省略不可表达参数；主项目要求无损或显式报错，需采用更严格边界。
6. R/`gateway/upstream.go:shapeKeyBody`、opencode_prompt.go、toolshape.go 修改免费模型请求以满足客户端形状门禁；R/`identity/request.go:CanonicalSessionID/RandomProjectID` 的部分逻辑以重启或新 project 重置风控记账为背景。本轮不采用，不伪造官方身份、不注入官方提示词/工具、不删除 reasoning reference 以强行重试、不实现匿名额度规避。
7. 模型列表成功只表明端点响应，不足以证明推理鉴权/订阅权益。无真实凭证不得报告 Zen/Go 实测成功。

## 审计发现的额外兼容性细节

- 主项目 `internal/routes/routes.go:orderGroup` 的 ordered 也调用 tie-break（选中数/在途数/游标），并非严格永远选择配置第一项；本轮保留真实行为，没有为了标签含义重写旧策略。需要确定账号先后时使用不同 Priority。
- `internal/pool/pool.go:classLocked` 原本遍历所有供应商目录来学习 OpenDesign 付费类别；新增 OpenCode 后必须跳过其目录，否则新模型会污染旧供应商的 funding 分类。新池也不调用 OpenDesign NoteModel/preflight。
- Dockerfile 使用 `COPY . .`，原 `.dockerignore` 没排除 `.analysis/`。已补排除参考克隆、测试数据和缓存，避免 GPL 研究材料/临时凭证进入 Docker 构建上下文。
- 参考 R/`gateway/runtime.go:applyLocked` 是“构建新 runtime 后交换”；主项目是 immutable config + 长生命周期 pool，不直接采用 runtime 整体替换。参考 `config/persistence.go:SaveAtomic` 有 .bak/Windows 回退，主项目仍保留 `fsutil` 原子存储契约，未声称已经增加同等 Windows 回退测试。
- 参考 R/`telemetry/metrics.go:Record` 同时考虑 HTTP 状态和终端流结果；主项目同样需保留 requestLog.Result，不能把 HTTP 200 但流失败记成成功。转换失败不做账号健康惩罚。

## 上游证据与不确定性

- 参考源码确实使用 `https://models.opencode.ai/api.json` 发现能力，并从官方 zen.mdx/go.mdx 端点表覆盖 SDK 默认协议；这是必要的补充，因为继承的 SDK 默认值可能不够精确。
- 参考 Go usage 契约见 R/`admin/model_quotas.go:decodeQuota`，严格验证三窗口、百分比 0..100、status 为 ok/rate-limited、时间非零。
- 已用真实浏览器验证公开 `models.opencode.ai/api.json`：当时 Zen 132、Go 37 个模型，默认及逐模型 SDK 字段确实存在；公开 API 声明已是 `/inference/{family}/v1` 和 `/inference/go/{family}/v1`。因此加入管理员显式的 per-protocol Base 覆盖，不让公开元数据自动把上游凭证转发到任意 URL。计数只作本次观察，不写死模型列表。详见 `.trellis/tasks/opencode-upgrade/research/public-contract.md`。
- 本次读取参考指向的官方 raw GitHub go.mdx/zen.mdx/usage.ts 链接均在浏览器可见 `404: Not Found`。usage 仍只有参考固定源码证据；没有实测当前 usage 接口、`/zen` / `/zen/go` 旧推理路径或公开声明的 inference 路径的鉴权调用，部署前必须补验。
- `/v1/models` 的列出不能保证单 Key 的实际模型权限。账号级缓存阻止“账号 A 的列表污染账号 B”，但上游若公开返回全表，真实权限仍只能以实际 403 等信号为准。

## 许可证处理

参考仓库 LICENSE 为 GNU GPL v3。主项目基线没有 LICENSE 文件，不能假设其是 MIT/Apache 或已与 GPL 兼容。未经权利人决定不改变主项目许可，也不复制/链接参考实现、UI、提示词、工具 JSON 或资产。新增代码根据公开协议与审计的行为契约独立编写；参考仓库不提交、不打包。若以后复制有著作权的实现，须先另行确定 GPL 分发、对应源码及通知义务；此处不是法律意见。

## 实施/验收跟踪

详见 `.trellis/tasks/opencode-upgrade/implement.md`；交付与实际测试结果记录在 `docs/opencode.md`。未穷尽审计、完整客户端实机验证及未覆盖能力必须保留为未完成项，不能用 Mock 成功替代真实验证。
