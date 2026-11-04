# OpenCode Go：设置、指纹与风险控制对照审查

审查时间：2026-11-05（北京时间）。本轮只读审查，没有改生产配置，没有发起上游推理。

## 0. 范围与证据等级

- 本项目源码基线：`b5e9eaeb4f5c3e00c6ec73f4ae3cb9c7d7111fec`。工作树已有其他业务的未提交修改；本次没有修改那些代码。
- 生产镜像 revision：`1c7a3b810c93e76c2c29f3e8429d8470c2b7f6ce`，容器 healthy。相关 OpenCode 主链路相对该提交没有新增业务变更；线上开关已在上一轮经用户授权开启。
- 参考仓库：[jasonxu114514/opencode2api](https://github.com/jasonxu114514/opencode2api)，本次浅克隆固定为 `58f56676ba1438adb30604b889bf36db2a1542a1`。以下 R/ 指这个固定快照，不指未来 main。
- 阅读了两个项目中与 Go 配置、请求构建、身份、传输、代理、额度、错误/重试、会话持久化相关的核心文件；没有宣称逐行审计两个仓库的所有源码。
- **运行事实**：生产只读设置及账号视图、已有额度/模型缓存。
- **代码事实**：可沿函数调用链确认的行为；Mock 验证不能证明上游认可。
- **官方事实**：本轮读取的官方 Go、Harnesses、Terms of Use 文档。
- **参考作者的观察**：源码注释关于私有风控、TLS 指纹、session 格式的说明，不等同于官方规则或本次抓包结果。
- **建议**：没有实施；具体并发/RPM 起步值不是官方安全阈值，更不构成“不封号”保证。

## 1. 核心结论

1. 付费 Go 与 Zen 免费模型门禁应分开分析。不能把参考项目的免费模型绕行机制当作 Go 的必需配置。
2. 参考项目确实有 Bun/uTLS 与 HTTP/1.1 请求形态模拟，但 `Gateway.inferenceClient` 仅在免费模型且 origin 为 `/zen` 或 `/inference` 时选择它；默认 Go `/zen/go` 走普通 `net/http`。
3. `Gateway.usesWARP` 也明确排除 Go。启用参考项目的 WARP，不会让 Go 套餐流量自动经过 WARP。
4. 当前官方 Go 文档要求真实编码代理流量、自己的 User-Agent、稳定会话 ID；并不要求第三方客户端自称官方 OpenCode CLI。
5. 本项目不伪造官方指纹，这是合理边界。但当前仅透传 User-Agent/session，不负责补齐缺失的真实客户端身份与会话头；这是比 TLS 伪装更实际的兼容性缺口。
6. 生产 Go 账号本地并发与 RPM 均未设上限。即使传输连接数有上限，也不是账号请求限流。
7. 本项目明确政策拒绝会停止本次请求的换号/换出口，但没有对后续独立请求建立专用政策熔断。因此“本次不重试”不等于“整个渠道不再持续撞上游”。
8. 生产 Go 额度/目录成功，27 个模型已识别原生协议；没有推理样本，不能据此认证 27 个模型真实可用、所有第三方客户端兼容或账号不会被风控。
9. 最优先的改进是合规客户端标识、稳定会话、低并发与有界重试、政策熔断、端点和权限验收；不是随机 IP、随机项目 ID、重启换身份或伪造提示词/工具。

## 2. 生产设置快照

只记录非敏感字段，不保存 Key、管理员密码、Cookie、CSRF 或代理凭证。

| 类别 | 当前值 | 解释 |
|---|---|---|
| OpenCode 总开关 | `true` | 上一轮已获用户授权启用 |
| 兼容转换 | `false` | 不做本地跨协议转换，要求匹配原生路径 |
| Go 控制/默认 API Base | `https://opencode.ai/zen/go/v1` | 目录和额度读取已成功；推理仍未验收 |
| Go 协议 Base 覆盖 | `{}` | 尚未显式使用官方新 family 路径 |
| Go 模型协议覆盖 | `{}` | 来自公开能力数据的 SDK 映射 |
| 能力源 | `https://models.opencode.ai/api.json` | 只取能力；不将其中任意 URL 自动用作带密钥目标 |
| Go 文档补充源 | 空 | 不读取旧 raw 文档地址 |
| 模型/能力刷新周期 | `600` 秒 | 与 Go usage 每分钟读取不同 |
| Go 独立路由 | `null` | 继承全局路由，不是已配置独立 Go 调优 |
| Go→Zen fallback | 未配置 | 不自动跨套餐 |
| 额度轮询 | 开启；并发 `2`；stale `900` 秒 | 是查询 worker 参数，不是推理并发 |
| Go 账号数 | `1` | 账号 enabled，默认 `opencode-go` 分组 |
| 账号 `maxConcurrent` | `0` | 未设置本地并发上限，不是禁止调用 |
| 账号 `maxRpm` | `0` | 未设置本地 RPM 上限 |
| 账号出口 | Direct | 没有固定代理；未评估实际公网 IP 信誉 |
| 继承策略 | `smart`、sticky=true | 单账号时不存在多账号分摊收益 |
| 每请求账号尝试预算 | `2` | 实际只有一个账号，尝试后会排除它，不会凭空得到第二个账号 |
| 绑定忙策略 | 全局 `wait-overflow`；等待 `2000` ms | OpenCode 入池额外 `PreserveBinding=true`，已有绑定满载时不借号溢出 |
| 显式/弱会话 TTL | `3600` / `900` 秒 | 弱会话推断不能替代客户端明确传 session |
| 首事件/空闲超时 | `600` / `600` 秒 | 较宽；长挂起可能长期占用资源 |
| 普通请求超时 | `900` 秒 | 不宜简单理解为所有已建立 SSE 的绝对总时长上限 |
| Direct/Proxy 每主机连接上限 | `512` / `32` | 连接池参数，不是 Go 账号并发/RPM；HTTP/2 还可能多路复用 |
| 默认 Go 分组下游 Key 数 | `0` | 未发现默认分组的下游推理 Key |
| 推理样本、选中数、24h 请求/Token | 均 `0` | 没有真实推理验证依据 |

模型目录 28 个，可路由协议识别 27 个：Chat 17、Responses 5、Messages 5。
未识别的是 `gemini-4-argon`，原因是 Google 原生 SDK/协议未实现，不是 Key 只剩 27 个模型权限。

额度快照 rolling/weekly/monthly 均已用 0%、剩余 100%，且 fresh。它们是订阅窗口百分比，不能直接换算为固定美元余额，也不能与同订阅的另一把 Key 相加。

## 3. “指纹”究竟指什么

| 层次 | 上游可能看到什么 | 本项目状态 |
|---|---|---|
| 账号/计费 | API Key 所属成员、工作区、订阅与额度 | 真实 Key 鉴权；内部 hash 不是新身份 |
| 网络 | 出口 IP、ASN、网络路径、连接行为 | 当前为 OVH Direct；未做 IP 风险测试 |
| TLS | ClientHello、扩展、ALPN 等 | 普通 Go TLS；未抓包测 JA3/JA4 |
| HTTP | UA、HTTP 版本、header 集合与布局 | 真实下游头透传；标准 Go Transport，可尝试 HTTP/2 |
| 会话/项目 | session、parent/child、project ID、跨请求连续性 | 显式头可透传；内部 HMAC 用于选账号，不自动发送上游 |
| 内容/行为 | 编码任务、工具使用、token 规模、请求节奏、失败重试 | 不注入官方 prompt/工具；没有默认账号速率上限 |
| 浏览器/硬件 | Canvas、WebGL、MAC、设备序列号等 | 当前 Go API 请求链没有发现这些采集与伪造逻辑 |

重要区分：

- `config.HashKey` 是标识/查找用途；`Identity.Fingerprint` 是服务端会话路由 HMAC；`OpenCodeSource` 是缓存来源完整性 hash。
- 这些“指纹”不是设备指纹，也不能为真实 Key 创造新的订阅或抹除上游风控记录。
- HTTP 请求体原生透传，不代表 TLS、HTTP header 排列和 HTTP 版本也与原客户端一致。
- 反过来，Go TLS 与官方 Bun 不同，也不能直接推出“因此会封号”。当前没有这种因果证据。

## 4. 参考项目到底做了什么，哪些适用于 Go

### 4.1 UA 与 HTTP 身份

R/`internal/httpx/client.go` 固定了 v2 CLI 版本 `2.1.1`，`V2UserAgent()` 返回 OpenCode CLI 风格的 UA。
R/`internal/gateway/upstream.go:upstreamRequest/setV2Headers` 为默认 Go 请求使用这套 v2 身份，并附加 client/session/project 等头。

它不是“保持真实 Claude Code/Codex/Pi UA”的透明中转，而是由网关声明统一的 OpenCode CLI 身份。

这是实现事实，不代表官方建议。当前官方 Go 文档恰恰建议用自身客户端 UA，例如 `my-coding-agent/1.0`。因此不建议本项目直接照抄其固定官方 UA/版本。

### 4.2 TLS、HTTP/1.1 和 Bun 模拟

R/`internal/httpx/bun.go`、`bun_dial.go` 自定义 RoundTripper：

- uTLS 自定义 ClientHello；源码称参考 Bun 1.4.3 抓取结果。
- 仅 HTTP/1.1 的 ALPN/线缆布局。
- 自定义 header 排列/大小写及 gzip/deflate/br/zstd 解压。
- 自己管理连接池与部分复用连接失败重试。

但使用范围由 R/`internal/gateway/upstream.go:inferenceClient` 决定：

```text
免费模型 AND（origin 是 /zen 或以 /inference 结尾）→ BunTransport
否则 → 普通 http.Client
```

**默认 Go Base 为 `/zen/go`，不进入 BunTransport 分支。** `go.mod` 中有 uTLS 依赖，不等于所有 Go 请求都用了 uTLS。
代码注释的抓包/指纹说明本次没有独立复测，不能将静态模板称为永久可靠的官方指纹。

### 4.3 会话、父子任务与项目

R/`internal/identity/request.go`：

- 按 server key scope 隔离自身派生会话，读取多种显式 header/metadata。
- 对符合其官方 session 判断的客户端标识原样保留。
- 对其他标识重新编码，加入进程级随机盐；重启后派生标识改变。
- 没有显式会话时，用系统指令和第一条用户内容派生；相同开场仍可能碰到同一亲和性。
- 没有项目时生成项目 ID；源码注释将部分重编码/项目变化与免费层风险或额度记账关联。
- `setV2Headers` 将 root/child/parent 会话渲染到上游头。

可借鉴的是**显式会话、父子任务连续性、调用方隔离**。
不应借鉴的是为了重新取得免费额度或摆脱限制而频繁变换会话/项目、用重启重新铸造身份。也没有证据证明这些技巧对付费 Go 封禁有效。

### 4.4 prompt / tools 整形

R/`upstream.go:shapeKeyBody` 只有 `catalog.IsFreeModel` 为真才整形，付费模型保持原正文。
免费模型即使用 Go Key 访问，也可能被：

- 强制 stream；
- 注入捕获的官方系统提示词和环境块；
- 重命名客户端工具并补入官方工具集；
- 在响应中还原工具名。

R/`opencode_prompt.go` 的环境块来自网关工作目录、平台和日期，并不是实际客户端环境。

这会改变 system/developer 语义、工具可用性、token 成本和隐私暴露面，也可能让模型调用下游并不存在的工具。它是针对免费门禁的请求改造，不是通用稳定性功能，更不是付费 Go 的必需步骤。本项目没有移植，建议继续保持。

### 4.5 WARP 与代理

R/`internal/gateway/warp.go:usesWARP` 的条件是免费模型且匿名或 Zen，**Go 不在其中**。
R/`pool.go` 支持静态代理及 `#pool`/`pool+` 动态代理条目；动态条目不做同样的健康跟踪，并关闭 keep-alive、为每次尝试重新建连接。静态 Key 代理可在失败时自动重绑定。

这些是网络路径/可用性功能；频繁换出口并不能恢复同一 Go 订阅额度，也不能证明可解除账号级封禁。当前 Go 自有稳定网络可用时，没有证据支持为了“防封”强行改成动态住宅代理或 WARP。

### 4.6 Key 冷却与重试

参考默认 `retry.max_attempts=3`，**每个认证 tier 独立预算**；Key 失败退避基数 15 秒，连续失败最高到 8 倍，Key 侧保留更长 Retry-After。
但 R/`pool.go:nodeCursor.Next` 在所有 Key 都冷却时仍可取最早结束冷却的 Key，且同一请求可再次访问相同节点。

所以其 cooldown 更接近选择偏好，并非任何情况下都绝对禁止调用；单 Key 时尤其不能把“设置了退避”理解为“一定不会继续打这把 Key”。

普通 Key 路径将 401/403/429 和传输错误等作为可重试；匿名网络滥用有专门的退出/可选熔断，**并非整个 Go 路径都具备同等级政策熔断**。

### 4.7 Go 模型轮转不是防封

参考提供 `sota`/`sweet` 模型组，单模型最多三次单调用尝试，限额/限流时暂停该模型并切换；普通原因失败达到模型计数边界或总超时才结束。
这是模型可用性回退，不是额外订阅权益。统一共享额度耗尽时换模型不应被当作额度恢复；还可能改变模型能力、结果和费用。

### 4.8 参考项目一个值得警惕的出口不一致

R/`internal/admin/model_quotas.go:fetchQuota` 新建默认 `http.Client` 查询 Go `/v1/usage`，没有复用 Key 绑定的 proxy transport；如环境存在全局代理，行为由默认 Transport/环境决定。
因此“推理走账号代理，额度查询也必然同出口”在该实现里不成立。本项目的 `HTTPFor(a.ProxyURL)` 反而明确将查询和推理绑定到同一账号代理配置。

### 4.9 参考 README 不是源码的替代品

这个固定快照的中文 README 部分描述仍提到免费请求从 v2 回退 v1，但 R/`anonymousUpstreams()` 的 v1 调用已注释。审查应以可达代码为准，不按翻译 README 的每句文字当作运行事实。

## 5. 本项目请求链与已有保护

```text
下游 Key 鉴权/分组
  → 解析真实会话信息
  → group + 下游 Key ID 范围内 HMAC
  → 原子账号选择、并发/RPM 预约
  → 账号自己的目录 ∩ 套餐能力
  → 选择原生协议/显式协议 Base
  → 过滤 hop-by-hop、下游凭证、Cookie、转发 IP 头
  → 保留其余客户端头，替换为真实上游 Key
  → 固定账号 Transport，禁止凭证请求 redirect
  → 按已发送/首事件/已提交边界控制错误及重试
  → 写入脱敏请求与错误记录
```

### 已有优势

- 账号、group、下游 Key、导入 Key 分离；OpenCode 凭证管理 API 不回显明文。
- Key/proxy/base/能力源变化，旧权限缓存及相关状态不复用。
- 账号目录隔离，不拿 A 的目录证明 B 的权限。
- Go usage 与推理走同一个 `ProxyURL` 配置，无代理故障静默直连。
- 明确风控拒绝硬性 stop，不受可编辑 cooldown/retry 规则覆盖。
- 参数错误不扩散成整个账号隔离；模型权限问题主要模型级处理。
- 原子并发/RPM 预约、成功写出后计数；取消与释放有测试。
- OpenCode 绑定忙不借号溢出；但普通弱会话在账号不可用时仍可能重新选账号，不是所有会话永久锁号。
- 原生 Responses 的 `previous_response_id` 严格绑定；未知/过期/来源变化返回 409，不删除字段伪装续传；已确认 Responses 绑定可 checkpoint。
- 已有上游请求发出的不确定网络失败不跨号重放；有效 SSE 输出后不重新生成。
- 不注入假官方身份、prompt、工具，不自动信任公共元数据中的任意 API URL。

## 6. 已确认缺口及其优先级

### P0-A：真实客户端 UA 与上游稳定会话缺乏兜底

证据：`internal/server/proxy.go:copyRequestHeaders/attempt`；`internal/server/identity.go:identity/fingerprint`。

- 下游提供的 UA 和 session 头能保留，这对官方 Claude Code/Codex/Pi 很重要。
- 但没有 OpenCode 专门的“非通用 UA”校验/兜底；下游缺省时，上游可能只看到 Go 默认 UA；下游发 SDK UA 也会原样保留。
- 仅存在于 body 的会话以及 fallback HMAC 可以用于本地黏性，却没有自动变成上游 `x-opencode-session`。
- 因此，本地“会话绑定命中”不能证明上游得到了稳定会话标识。

建议：保留真实客户端身份；对缺失/通用库 UA 使用明确的自身网关产品标识或由接入端补齐；在无上游可识别会话头时，按调用方范围产生稳定匿名 ID。不要伪装官方 CLI，也不要按每个 HTTP 请求随机 session。父子任务应同时保留根会话和实际子任务标识。

### P0-B：账号并发与 RPM 默认未限制

证据：生产 `maxConcurrent=0/maxRpm=0`；`internal/pool/pool.go:admitLocked`。

现在 `smart`、健康评分和连接池都不能替代账号限流。`maxAccountsPerRequest=2` 也不是“最多两个并发”。
建议先在单账号上设并发 1；RPM 可从 10–20 的本地保护值起步，再依真实编码工作流、上游反馈和额度调整。这个范围不是官方限额或安全认证。必要时还应按订阅/工作区与下游 Key 限制，而非一把 Key 一个无限独立预算。

### P0-C：政策 stop 只作用于当前请求

证据：`internal/server/proxy.go:evaluate` 的 policy 分支直接返回，没有设置持久化 policy cooldown/breaker。

优点是不会误封账号、不会在一次失败后换号撞门禁；缺点是 new-api/客户端下一次独立请求仍可再次联系上游，页面也可能继续显示账号 ok。
建议增加区分凭证、模型、工作区、出口的政策告警/熔断；明确政策拒绝时停止自动重试并等待人工确认。不能把所有 403 都当账号永久封禁，也不能把所有 403 都当可换号的模型错误。

### P1-D：Go 继承为其他池调过的全局参数

`goRouting=null`，目前实际继承 600 秒首事件、600 秒 idle、900 秒普通请求超时及 smart 等设置。
建议保存独立 Go 路由副本，使 Go 调整不影响其他池。首事件可从 120–180 秒、idle 120–180 秒、普通请求 300–600 秒评估，但长推理模型应以真实数据为准；不能把短超时 + 重放作为“提速”。小规模验收先一请求一次上游尝试，稳定后再给安全失败有限重试。

### P1-E：新旧推理端点尚未实际验收

控制接口 `/zen/go/v1/models` 和 `/usage` 已成功，不证明旧推理路径对全部协议长期兼容。
官方当前文档给出的 Go family 路径是：

| 协议 | Base（本项目须含 `/v1`） | 路径 |
|---|---|---|
| Chat | `https://opencode.ai/inference/go/openai/v1` | `/chat/completions` |
| Responses | 同上 | `/responses` |
| Messages | `https://opencode.ai/inference/go/anthropic/v1` | `/messages` |

本项目可用 `goProtocolBases` **显式**指定经确认地址，同时保持已验证的目录/usage Base 不变。不要未经验证直接将 `goBase` 改成某个 family Base，否则还会改变目录/额度目标及来源 hash。

### P1-F：未知额度放行与共享订阅缺乏更细粒度护栏

`State.Fresh` 在 usage 失败时为 false，`Exhausted` 对非 fresh 返回 false。旧值和更新时间保留是正确的，但实际策略为额度未知时仍可调度，不是自动停止。
建议查询连续失败时提示降级并收紧 admission，而不是伪造 0 或继续无限量运行；同工作区多 Key 不应被视为多份独立额度。

### P1-G：权限/隐私拒绝与账号封禁容易混淆

官方 Go 文档说明工作区 privacy/region 设置会影响模型，目录可见不代表请求获准。当前通用 403 默认仅模型冷却 5 分钟；没有隐私/地域/条款原因的完整结构化分类。
建议在不泄露凭证/对话的前提下保留 error code、request ID、模型、协议、出口类型，并提供人工操作提示。不自动关闭用户隐私保护，不自动改变地域或训练授权。

### P2-H：当前缺少 Google 原生协议支持，但不应胡乱覆盖

`gemini-4-argon` 的 SDK 无法映射到三协议，因此目录 28、可路由 27 是预期保护行为。
官方目前声明文本模型可走其兼容 API；可后续明确选择官方支持的替代协议/端点，并验证工具、多模态、reasoning 和流式边界。不能只把未知 SDK 改成 Chat 就宣称能力完全兼容。

## 7. 错误矩阵：防止误隔离与重试风暴

| 情况 | 当前主项目行为 | 应如何理解 |
|---|---|---|
| 明确 `FreeTierError` / free-tier-only / abuse 等 403 | 硬性 stop；下游 403；不换号/出口，不扣账号健康 | 政策拒绝，不是应该换身份尝试的网络错误；后续请求熔断尚缺 |
| 401 | 整号 hard-quarantine；对下游通常映射 502 | 先检查撤销/错误 Key；不通过重启清掉真实认证问题 |
| 已识别的模型 403/404 | 该账号该模型冷却 10 分钟 | 不等同于整个账号被封 |
| 未识别的普通 403 | 模型冷却 5 分钟，可能按预算换账号 | 需继续区分权限、隐私、地域和其他政策原因 |
| 402 | 账号冷却，默认 10 分钟、连续失败升级、上限 1 小时，并触发额度刷新 | 余额/计费问题；不是改 UA 能解决 |
| 429 | 账号冷却；优先完整 Retry-After，否则 30 秒 | 可能是额度、预算或速率，需要看 error code，不统称封号 |
| 400/413/422 | 默认停止，不隔离整个账号 | 参数/上下文等问题，不换号轰炸 |
| 500/502/503/504 | 模型短冷却，默认 10 秒，设定上限 5 分钟 | 有界可用性恢复，不证明账号异常 |
| 代理连接失败/407 | 独立代理健康，30 秒冷却，无直连回退，不算账号健康失败 | 出口坏不等于 Key 坏 |
| 请求已发出但连接结果不确定 | OpenCode 不跨号网络重放 | 上游可能已经执行/计费 |
| SSE 已输出有效事件后失败 | 不重放；标记流失败 | 避免重复生成和工具执行 |

政策识别目前是 HTTP 403 + 已知关键词匹配，不是通用私有风控检测器；未知文案、其他状态码、HTML 挑战或中途流错误不能都假定已覆盖。

## 8. 额度与计费特别注意

1. rolling、weekly、monthly 三个窗口必须完整；缺失/异常不当成余额 0。
2. 上游报告的是使用百分比，可能有取整；100% 剩余不代表无限请求，也不代表每个模型都有同样的美元额度。
3. 官方模型月度 allowance 数字及换算比率会变化；不能用一个固定金额乘所有窗口百分比，不能将同一订阅的多把 Key 相加。
4. 官方 Go 的 Extra Usage 是控制台余额扣费设置；本项目的全局 `walletFallback` 是其他供应商资金路由语义，不是 Go Extra Usage 开关。
5. 本项目新鲜 Go usage 中任一有效窗口耗尽会本地阻止该账号继续调度。因此即使官方控制台开了 Extra Usage，也不能假定网关会自动继续按量调用；若要支持需另行明确授权与设计，避免意外扣费。
6. Go/Go Plus 是单成员工作区；多人协作应按官方 Teams/成员权益处理。多把 Key 只是凭证数量，不天然增加订阅许可或共享额度。

## 9. 官方政策与“防封”的真实边界

本轮官方 Go 文档 `Where can I use it?` 要求：

- 典型 coding-agent 流量；
- 使用自己的 UA，而非通用 SDK/HTTP 库名称；
- 每个会话稳定的 `x-opencode-session`，优化路由与缓存；部分官方客户端原生 session header 也被识别。

官方 Harnesses 文档还提醒：免费层 chat 模型只能在 OpenCode 内使用；外部 harness 不能因为有一把 Console Key 就推断获准使用受限免费模型。因此目录里的免费模型不应被网关当作“注入官方 prompt 即可合法使用”。付费 Go 与免费模型门禁的具体组合仍应遵循官方授权和真实响应。

官方 Terms of Use（页面标示生效日期 2026-08-15）明确限制：

- 仅自身内部使用，不为第三方利益使用（`What are the basics of using OpenCode?`）。
- 禁止批量创建/维护/使用账号，以及利用多账号规避用量、访问、计费、促销、暂停等限制（`Are there restrictions in how I can use the Services?` 第 4 项）。
- 禁止给服务基础设施施加不合理负载；违反限制可终止访问。
- 未经书面同意不能转让账号及合同权利（`Assignment`）。

因此：技术上能构建账号池，不代表上游授权将单人 Go 订阅变成公共转售服务；使用固定代理本身也不能被简单等同于违约。团队、CI、转售或第三方托管场景应核对适用条款并向服务方取得必要授权。这不是法律意见。

没有官方公开的“安全 RPM”“一 IP 最多几个账号”“哪种 JA3 永不封禁”规则。本报告不编造这类承诺。

## 10. 建议实施顺序（本轮未执行）

### 第一批：先满足官方接口约定与基础护栏

1. Go UA/session 兜底及可观测性：真实 UA 优先、独立产品名兜底、调用方隔离的稳定 ID、父子会话不丢失；只记录存在性/来源类型，不记录原始会话或完整提示词。
2. Go 独立路由副本；单账号并发 1、本地保护 RPM，初验一次上游尝试。
3. 通过 mock 验证 401/403/429、代理失败、首事件边界、无付费自动探测。
4. 经另外授权，选择明确获准的一个模型做最小原生推理；记录实际 UA/session 存在性、目标路径、error code 和上游 request ID，不保存 Key。

### 第二批：防止跨请求反复撞策略

5. 专用 policy circuit breaker、原因分类和告警；禁止以自动换 Key/出口恢复政策拒绝。
6. usage 连续失败的降级 admission，订阅/工作区级共享限制；明确本地 429 与上游 429。
7. 显式官方 protocol Base 与模型覆盖，逐协议验收；不要将三协议全开转换当作首步。

### 第三批：兼容性与运维

8. 原生 Responses 状态续传和重启恢复、父子 agent、多下游 Key 隔离。
9. 观测 P95 TTFT、失败分类、429 比率、缓存命中、实际 token/allowance；不要以模型目录数替代成功率。
10. Gemini/Google 能力支持、流式转换边界、客户端实际工作流；涉及计费与隐私授权时单独确认。

不建议列入改造：假官方 UA、复制官方 prompt/tool 资产、按请求随机 session/project、以 WARP/动态代理/多账号绕过政策或订阅限制。参考项目为 GPLv3，复制实现或资产还需要独立处理许可义务，不能将行为审查理解为可直接拷贝。

## 11. 本轮验证及未验证项

已执行：

```sh
go test ./internal/opencode -count=1
go test ./internal/opencode ./internal/config ./internal/quota ./internal/pool ./internal/rules ./internal/server -run 'OpenCode|FetchUsage' -count=1
git diff --check
```

上述测试通过。第二条对 `internal/opencode` 没有匹配测试，所以另执行第一条完整覆盖该包；不能将第二条说成全仓测试。测试使用本地 Mock，没有联系真实推理上游。

生产只读核验：容器健康、Go 已启用、配置/账号参数、额度和模型缓存、推理计数为 0。本轮未做带 Key 推理、未改开关、未重新部署、未抓包验证 TLS 指纹，也未测试地区/隐私/封禁触发条件。

未验证：

- 所有模型的真实付费权限、实际工具/图片/长上下文能力。
- Claude Code、Codex、Pi、NewAPI 的实际带载端到端流程。
- 官方旧推理路径继续兼容的范围。
- 任意 UA/TLS 模板或出口对私有风控的因果作用。
- 真实长期成功率及封禁概率。

## 12. 核心证据入口

本项目：

- `internal/config/opencode.go`：设置、Base 验证、独立路由与 fallback。
- `internal/server/proxy.go`：`copyRequestHeaders`、`attempt`、`evaluate`、`relayStream`。
- `internal/server/identity.go`：会话来源、HMAC、父子识别。
- `internal/server/server.go:transport`：普通 Go Transport、固定代理、HTTP/2。
- `internal/rules/opencode.go`：政策识别、各类错误与重试规则。
- `internal/quota/opencode.go`、`internal/opencode/usage.go`：来源 hash、读取周期、旧值保留、三窗口/耗尽判断。
- `internal/pool/pool.go`：admission、绑定、quota-aware seat、严格冷却、Responses checkpoint。
- `internal/opencode/discovery.go`：模型 SDK→协议、redirect 禁止、不自动信任元数据 URL。
- `internal/server/opencode_policy_test.go`、`opencode_identity_test.go`、`opencode_admin_test.go`、`opencode_bases_test.go`：对应 Mock 契约。

参考快照：

- [Go 配置](https://github.com/jasonxu114514/opencode2api/blob/58f56676ba1438adb30604b889bf36db2a1542a1/internal/config/config.go)
- [UA 定义](https://github.com/jasonxu114514/opencode2api/blob/58f56676ba1438adb30604b889bf36db2a1542a1/internal/httpx/client.go)
- [Bun TLS](https://github.com/jasonxu114514/opencode2api/blob/58f56676ba1438adb30604b889bf36db2a1542a1/internal/httpx/bun_dial.go)
- [请求构建、Bun 分流、整形、重试](https://github.com/jasonxu114514/opencode2api/blob/58f56676ba1438adb30604b889bf36db2a1542a1/internal/gateway/upstream.go)
- [身份派生](https://github.com/jasonxu114514/opencode2api/blob/58f56676ba1438adb30604b889bf36db2a1542a1/internal/identity/request.go)
- [代理/Key 冷却](https://github.com/jasonxu114514/opencode2api/blob/58f56676ba1438adb30604b889bf36db2a1542a1/internal/gateway/pool.go)
- [WARP 适用范围](https://github.com/jasonxu114514/opencode2api/blob/58f56676ba1438adb30604b889bf36db2a1542a1/internal/gateway/warp.go)
- [额度查询](https://github.com/jasonxu114514/opencode2api/blob/58f56676ba1438adb30604b889bf36db2a1542a1/internal/admin/model_quotas.go)
- [Go 模型轮转](https://github.com/jasonxu114514/opencode2api/blob/58f56676ba1438adb30604b889bf36db2a1542a1/internal/gateway/rotation.go)

官方：

- [Go：客户端约定、套餐、额度、端点、隐私](https://opencode.ai/v2/docs/console/go/)
- [Harnesses：Base URL、session 与免费模型限制](https://opencode.ai/v2/docs/console/harnesses)
- [Terms of Use](https://opencode.ai/legal/terms-of-service)

临时只读源码与工具溢出文件已登记 `_tmp_artifacts.md`，未经用户同意不清理；Trellis 审查记录保留。
