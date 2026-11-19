# Factory 出口 IP / 网络指纹审计

审计对象：本仓库 `provider=factory` 的 HTTP、Gemini、Embeddings、countTokens、Responses WebSocket、OAuth 登录/刷新、额度轮询、模型测试。证据来自本仓库代码与 `docs/factory-capture/` 的本地抓包；没有打开真实 Factory 模型连接，也没有可用的真实账号做对照。

## 结论

1. **配置了账号代理时，Factory 路径没有直连回退**：HTTP 模型调用、Gemini、Embeddings、countTokens、whoami/billing、OAuth 设备码/刷新、模型测试和 Responses WebSocket 都使用该账号的 `ProxyURL`。代理不可达/格式错误会直接失败，不会悄悄改走本机出口。
2. **没有代理时就是本机出口**：这是配置选择，不是隐藏回退。控制台现在对 Factory 无代理账号显示「直连出口」徽标，登录弹窗也明确提示 WorkOS/Factory 会看到本机出口 IP。
3. **下游客户端地址头不会到达 Factory**：Factory 请求头现在从零重建，不再复制任何下游头；此前常见的 `X-Forwarded-For`、`CF-Connecting-IP`、`X-Real-IP`、`Forwarded` 等已被 `copyRequestHeaders` 丢弃，WebSocket 路径本身也不复制下游头。
4. **客户端 SDK/浏览器指纹不再混入**：`x-stainless-*`、`Sec-*`、`Accept-Language`、`Origin`、`Referer`、`OpenAI-*`、`Anthropic-*`、`X-Goog-*`、trace baggage 等现在全部丢弃，再写入 droid 的固定头。Anthropic 版本固定为 `2023-06-01`。
5. **Factory 模型 HTTP 已固定为 HTTP/1.1**：抓包中 droid 的 `/api/llm/o/v1/responses` 带 `Connection: keep-alive`，不是 HTTP/2；Go 自动 h2 会形成不同的 SETTINGS/ALPN 指纹。Factory 专用 transport 关闭 `ForceAttemptHTTP2`。
6. **仍有残余指纹**：Go TLS ClientHello（JA3/JA4）、HTTP/1.1 头顺序、缺失的 `Accept-Encoding: gzip, deflate, br, zstd`、TCP/IP 栈、代理服务商本身、浏览器批准页 IP、多个账号共用同一出口。这些没有用猜测值“伪装”，见下文。

## 出口路径矩阵

| 路径 | 代理来源 | 目标 DNS | 无代理时 | 直连回退 |
|---|---|---|---|---|
| `/v1/chat/completions`、`/v1/responses`、`/v1/messages` | `Account.ProxyURL` | HTTP CONNECT / SOCKS 由代理解析 | 本机出口 | 无 |
| Gemini `generateContent` / `countTokens` | `Account.ProxyURL` | 同上 | 本机出口 | 无 |
| `/v1/embeddings` | `Account.ProxyURL` | 同上 | 本机出口 | 无 |
| `/v1/responses/ws` | `Account.ProxyURL` | HTTP CONNECT / SOCKS5 由代理解析 | 本机出口 | 无；代理不支持时失败 |
| whoami / billing | `Account.ProxyURL` | 代理 | 本机出口 | 无 |
| WorkOS 设备码 / refresh | 登录时填写的代理；重授权用账号代理 | 代理 | 本机出口 | 无 |
| 模型测试 | `Account.ProxyURL` | 代理 | 本机出口 | 无 |
| `/v1/models` | 本地配置，不访问上游 | — | — | — |

代理 URL 的解析错误会变成 `Proxy` 返回错误，而不是 `nil`（直连）；WebSocket 对不支持的代理 scheme 直接报错。HTTP 代理使用 CONNECT，目标主机名交给代理解析；SOCKS5 同样发送目标主机名，不在本机做目标 DNS。

## 已修复的头部泄漏

Factory 路径不再调用 `copyRequestHeaders` 后保留任何值，而是先清空请求头（仅保留功能性的 `Anthropic-Beta` 列表），再写入 droid 的身份：

- 地址类：`X-Forwarded-For`、`X-Forwarded-Host`、`X-Forwarded-Proto`、`Forwarded`、`X-Real-IP`、`True-Client-IP`、`CF-Connecting-IP`、`X-Client-IP`、`Client-IP`、`X-Originating-IP`、`X-Cluster-Client-IP`、`X-Remote-IP`、`X-Remote-Addr`、`Via`、`X-ProxyUser-IP`、`Fastly-Client-IP`、`X-Akamai-Client-IP`、`X-Bluecoat-Via`、`X-Hermes-Client-IP`、`Proxy-Client-IP`、`WL-Proxy-Client-IP`、`HTTP-Client-IP`、`X-True-IP`、`X-Envoy-External-Address`、`X-Original-Forwarded-For`、`X-Azure-ClientIP`、`X-Azure-SocketIP`、`X-MS-Client-IP` 等。
- 客户端/浏览器：`x-stainless-*`、`Sec-*`、`Accept-Language`、`Origin`、`Referer`、`Priority`、`OpenAI-*`、`x-openai-*`、`Anthropic-Version`/其它 `Anthropic-*`（`Anthropic-Beta` 功能列表除外）、`X-Goog-*`、`Baggage`、`Tracestate`、`Sentry-Trace`、`X-B3-*`、`X-Datadog-*`、`X-Amzn-Trace-Id`、`X-Cloud-Trace-Context`、`X-Request-*`、`X-Correlation-*`、`X-Trace-*`。
- `Anthropic-Beta` 是功能协商，保留调用方列表；Fast Mode 的 droid beta 再追加。它是唯一保留的下游 Anthropic 头，仍可能暴露客户端特性选择。
- 然后只写入：`Authorization`、`User-Agent: factory-cli/1.14.0`、`X-Factory-Client: cli`、`X-Client-Version: 1.14.0`、`X-Factory-Request-Surface`、可选的 `X-Factory-Exec-Output-Format`、`X-Factory-Org-Id`、`x-api-provider`、`x-provider-routing-source: configured_order`、`traceparent`、`x-session-id`、`x-assistant-message-id`、`Accept`、`Content-Type`；Anthropic 路由固定 `anthropic-version: 2023-06-01` 与 `x-api-key: placeholder`；Gemini 使用 `Accept: */*`。

这意味着即使上游反代错误地把下游地址写进某个自定义头，只要它落在上述地址类拼写里，也不会随 Factory 请求发出；即使落在别的客户端头里，也会因为“全部清空后重建”而不存在。

### 抓包里没有的头也不发

前半段解决“多发了不该发的”，这一段解决“发了抓包没有的”。droid 1.14.0 对每个 `prem.factory.ai` 路由发的头集合并不相同，网关按**实际发出的线路协议**逐条对齐：

| 头 | Chat Completions | Messages | Responses | Gemini generate | Responses WS |
|---|---|---|---|---|---|
| `traceparent` | 无 | 无 | **有** | **有** | 无（在首帧 `_factory` 里） |
| `x-factory-exec-output-format` | 无 | 无 | **有**（cli_exec） | **有**（cli_exec） | **有**（cli_exec） |
| `openai-platform` | 无 | 无 | **有**（仅 openai） | 无 | **有**（仅 openai） |
| `x-session-id` / `x-assistant-message-id` | 有 | 有 | 有 | 有 | 仅 session |
| `x-api-provider` / `x-provider-routing-source` / `x-factory-request-surface` | 有 | 有 | 有 | 有 | 有 |

实现：`factory.ApplyProtocolForwardHeaders` 按 `wireProtocol(model.Protocol, plan)` 决定发送集合，跨协议转换后按**转换后的协议**判断（例如 Chat 请求被转成 Responses，就按 Responses 的集合发）。

`x-message-call-type: session-title-generation` 是 droid 自己做会话标题生成时的标记，网关没有对应场景，因此不发——这也是“抓包里没出现的、不属于我们场景的头不发”。

## HTTP/TLS 指纹

- **HTTP/1.1**：Factory 专用 `http.Transport` 设置 `ForceAttemptHTTP2=false`，与抓包中的 `Connection: keep-alive` 一致。WorkOS 设备码/刷新也使用同一 HTTP/1.1 transport，并发送 `User-Agent: Bun/1.4.4`，不再使用 Go 默认 UA。
- **TLS 仍是 Go 默认 ClientHello**。droid CLI 是 Bun/BoringSSL；本仓库没有 droid/Bun 的 TLS ClientHello 实测数据，因此没有用 Chrome/Node 的 uTLS 指纹硬套。用 Chrome 指纹配 `factory-cli` UA 只会制造另一种矛盾。
- **`Accept-Encoding`**：已按 droid 发送 `gzip, deflate, br, zstd`（CLI 的编码列表，不是浏览器的那套），transport 仍保留 `DisableCompression` 以免 Go 再叠加自动 gzip。为兑现这个头，Factory 响应在进入 JSON/SSE/Gemini 聚合前统一解码 gzip / deflate / br / zstd（`internal/server/factory_decode.go`）；未知编码返回 502 `upstream_encoding`，不会把压缩体喂给解析器。
- **UA 形态**：推理 UA 必须是真实 CLI 身份（默认 `factory-cli/1.14.0`）。配置校验现在**拒绝 `Mozilla/…` 浏览器 UA**（账号、UA 池、默认 UA 三处），因为本网关在推理请求上不发送 `Sec-CH-UA`、`Accept-Language`、`Sec-Fetch-*`，浏览器 UA 只会自相矛盾；下游自带的浏览器 UA/Origin/Cookie 也不会被转发。
- **头顺序/大小写**：Go 会规范化头名并按自己的顺序写出；HTTP/1.1 下与 Bun 的顺序不完全一致。

## WebSocket 指纹

- 下游 `/v1/responses/ws` → 上游 `wss://prem.../api/llm/o/v1/responses/ws`，首帧 `response.create` 决定模型，升级后双向文本帧。
- 支持 HTTP CONNECT/SOCKS5 账号代理；无代理时直连，控制台「直连出口」徽标提示。
- 当前 x/net/websocket 客户端会发送 `Origin`，而本地抓包的 droid WS 请求没有 `Origin`；也没有请求 `permessage-deflate`。这两个是 WS 侧残余差异，未用伪造值掩盖。
- WS 不计入 HTTP 请求统计/额度窗口门禁；升级后不重试。

## OAuth 与浏览器 IP

- 服务器侧 WorkOS `authorize/device`、`authenticate`、refresh 都走账号/登录代理；没有代理时走本机出口。
- **设备批准页在用户浏览器打开**，WorkOS/Factory 会看到用户浏览器的真实 IP、时区与语言。服务器代理无法改变这一点；要做完整伪装，浏览器侧也需要与出口地区一致的代理/VPN，并保持语言/时区一致。
- 登录弹窗已提示这一点。

## 共享出口与账号关联

- 多个 Factory 账号配置同一个代理（或都不配代理）时，Factory 看到同一出口 IP，可以把多个账号关联到同一网络；这是号池层面的关联指纹，不是请求头泄漏。
- 现在可以配置代理池自动分配；池大于等于账号数时优先分配未占用代理，池不够时仍会重复。需要严格一账号一住宅 IP 时，仍应手工核对账号代理。代理服务商也能看到源 IP 与目标，属于信任边界。
- 同一请求失败后换号重试时，不同账号可能来自不同出口；若下游内容相同，Factory 仍可能关联，但请求已标记为不同 session/trace。

## 结论性风险表

| 风险 | 状态 |
|---|---|
| 下游 `X-Forwarded-For`/CDN 地址头 | 已阻断（HTTP 全清空 + WebSocket 不复制） |
| 客户端 SDK/浏览器头、语言、Origin | 已阻断（Factory 头全部重建） |
| 代理不可达时直连回退 | 已阻断（明确失败，不 fallback） |
| 无代理直连 | 仍存在；UI 明示，属配置选择 |
| HTTP/2 指纹 | 已改为 HTTP/1.1 |
| Go TLS JA3/JA4 | 仍存在；无 droid TLS 实测，不伪造 |
| `Accept-Encoding` / 头顺序 | 仍存在 |
| 推理 UA 被配成浏览器 UA | 已阻断（配置拒绝，运行期再次回退到 CLI UA） |
| `Accept-Encoding` 缺失 | 已补 droid 的 CLI 编码列表，并配套 gzip/deflate/br/zstd 解码 |
| 未配置 `OpenAI-Platform` 或请求面选错 | 抓包中有该头/`cli_exec`；缺失或错选是身份不一致，不是 IP 泄漏，但可被关联 |
| 下游 `Anthropic-Beta` | 为功能兼容保留；仍暴露客户端特性选择 |
| WebSocket `Origin`/压缩扩展 | 仍存在 |
| 浏览器批准页 IP | 仍存在；需浏览器侧代理 |
| 多账号共享出口 | 仍存在；需运营侧独立代理 |
| WorkOS 侧 Go/默认客户端指纹 | 已改为 HTTP/1.1 + `Bun/1.4.4`；TLS 仍为 Go |
| 代理服务商可见流量 | 信任边界 |

## 验证

- `TestCopyRequestHeadersDropsEveryClientAddressSpelling`：覆盖地址头拼写。
- `TestFactoryStripsClientFingerprintAndAddressHeaders`：地址头、Stainless、Sec-CH、Origin、语言、OpenAI 组织均不到达上游；Anthropic 版本固定。
- `TestFactoryTransportPinsHTTP11`：Factory transport 不使用 HTTP/2。
- 全套 `go test ./...`、`go test -race`、`go vet`、`go build`、前端测试通过（具体耗时见 `docs/factory-pool.md`）。
- 未使用真实 Factory 模型连接；以上为静态/本地 Mock 证据。
