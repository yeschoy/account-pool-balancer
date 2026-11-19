# Factory Desktop 1.12.1 协议还原（反代 / 客户端复刻用）

分析对象：`/Applications/Factory.app`，Bundle `com.electron.factory`，CFBundleVersion `16963`，
Electron `42.11.17`，内嵌 `@factory/daemon-sdk 1.340.0`，内嵌 CLI `Resources/bin/droid`（Go, arm64）。
签名：`Developer ID Application: The San Francisco AI Factory Inc. (SW6TL4V6Q5)`，hardened runtime，已公证。

证据来源：
- `app.asar` 解包：`.vite/build/index-vvcnYwSf.js`（SDK+UI）、`initLifecycle-BeuHu3vX.js`（daemon 生命周期/落盘）、`index-Cu4a0f0m.js`（Sentry/OTel）。
- 本机落盘身份文件 `~/.factory/{host.json,computer.json,telemetry/droid-installation-id}`。
- 运行期网络观察（TLS SNI / 连接计数），文件 `/tmp/factory-re/capture.ndjson`。

> 标注约定：**【静态】** = 从打包产物直接读出的代码事实；**【运行期】** = 实测到的事实；
> **【推断】** = 由结构推导、未直接观测。

---

## 0. 结论摘要（先看这段）

1. 桌面端不是“直连 Anthropic/OpenAI”，而是统一走 **`https://api.factory.ai`** 的代理网关，
   按供应商前缀分流：`/api/llm/a/...`（Anthropic 协议）、`/api/llm/o/...`（OpenAI 协议）、
   `/api/llm/g/...`（Gemini）。**【静态】** `Dn` 枚举。
2. 鉴权是 **`Authorization: Bearer <token>`**，组织维度额外带 **`X-Factory-Org-Id`**。**【静态】**
   `XKe()`/`gV()`/`Bk()`。
3. 有 **两套遥测**：自建 `POST https://telemetry.factory.ai/api/ingest`（每 2s / ≤100 条批量），
   和 Sentry Electron（含 OTLP traces `POST /api/otlp/traces/ingest`）。**【静态】**
4. 自建遥测**会携带用户消息正文**：`user-message` 事件含 `text`（上限 40000 字符）与 `author`。
   默认开启，仅测试环境禁用。**【静态】** `Ef` 枚举 + `th()`。
5. 设备指纹不是“主板序列号”那类硬件指纹，而是 **两层可导出的身份**：
   - `hostId`：随机 UUIDv4，落盘 `~/.factory/host.json`；
   - 计算机身份：**X25519 密钥对**（`publicKey`/`privateKey`/`keyId`，`schemaVersion:1`），
     落盘 `~/.factory/computer.json`（本机当前该文件只存了 `computerId`+`registeredAt`，
     密钥对路径见 §5 的 `computerIdentityPath`）。**【静态】**
   - 另有 `droid-installation-id`（第三个独立 UUID）。**【运行期】** 实测文件内容。
6. 请求级追踪：注入 W3C **`traceparent`/`tracestate`**，并同时透传 Sentry `traceId`/`spanId` 到遥测。**【静态】**
7. 请求头合并保持**原始大小写**（`BUe` 以 `toLowerCase()` 去重但保留首个原始 key）。**【静态】** ——
   这是反代复刻时报文一致性的关键细节。

---

## 1. 主机与端点清单

### 1.1 环境主机表（**【静态】**，`index-vvcnYwSf.js`）

| 用途 | 主机 |
|---|---|
| 生产 API | `https://api.factory.ai` |
| 生产 API（EU） | `https://api.eu.factory.ai` |
| 生产 App/控制台 | `https://app.factory.ai` |
| 遥测摄入 | `https://telemetry.factory.ai` |
| 遥测（dev） | `https://dev.telemetry.factory.ai` |
| Relay | `https://relay.factory.ai` / `https://relay.eu.factory.ai` |
| Relay（dev） | `https://relay-dev.factory.ai` / `https://relay-dev.eu.factory.ai` |
| Prem（自托管形态） | `prem.factory.ai` / `prem.eu.factory.ai`，`preprod.`/`staging.`/`dev.` 前缀同构 |
| 下载/更新 | `https://downloads.factory.ai/factory-desktop/` |
| staging API | `staging.api.factory.ai` / `staging.api.eu.factory.ai` |
| test API | `test.api.factory.ai` / `test.api.eu.factory.ai` |

外部依赖（**【静态】**，Firebase/Google）：
`identitytoolkit.googleapis.com`、`securetoken.googleapis.com`、`firestore.googleapis.com`、
`generativelanguage.googleapis.com`、`aiplatform.googleapis.com`。

### 1.2 API 路径表（**【静态】**，`Aa` / `Dn` / `y8` / 其他常量）

```
# 鉴权（注意：这些在 api.factory.ai 上实测 404，真实鉴权走 WorkOS，见 §2.4.1）
GET    /api/auth/config          # ← 实测 404（仅自托管/Prem 形态可能启用）
POST   /api/auth/exchange
POST   /api/auth/refresh
GET    /api/auth/whoami          # ← 实测 404
POST   /api/auth/logout

# 鉴权（真实路径，WorkOS User Management）
POST   https://api.workos.com/user_management/authenticate   # refresh_token / PKCE exchange
GET    https://api.workos.com/user_management/...            # 用户/组织信息
GET    https://api.factory.ai/api/cli/whoami                  # 实测 401（带扩展头）

# 身份 / 组织
GET    /api/cli/whoami                    # X-Factory-Whoami-Extended: true
GET    /api/context
GET    /api/feature-flags
GET    /api/organization/usage-limits
GET    /api/organization/usage-limits/global
GET    /api/organization/shared-usage-limits/self
GET    /api/organization/shared-usage-limits/admission
GET    /api/organization/shared-usage-limits/principals
GET    /api/organization/shared-usage-limits/principals/:principalId
GET    /api/organization/managed-settings
GET    /api/internal/managed-settings
GET    /api/internal/managed-settings/history
GET    /api/internal/member-managed-settings
GET    /api/v0/organization/usage/limits/shared
GET    /api/internal/billing/usage/credits
GET    /api/internal/billing/usage/credits/options
GET    /api/external/credits/config

# 推理主链路（网关前缀分流）
POST   /api/llm/a/v1/messages             # Anthropic Messages
POST   /api/llm/v1/messages               # Anthropic 原生直通
POST   /api/llm/o/v1/chat/completions     # OpenAI Chat
POST   /api/llm/o/v1/responses            # OpenAI Responses
WS     /api/llm/o/v1/responses/ws         # OpenAI Responses over WebSocket
POST   /api/llm/o/v1/embeddings
POST   /api/llm/o/v1/images/generations
POST   /api/llm/o/v1/images/edits
POST   /api/llm/g/v1/generate             # Gemini
GET    /api/llm/custom/usage

# 会话 / 数据
GET    /api/sessions/by-ids
GET    /api/internal/session-sidebar-views
GET    /api/internal/session-sidebar-customization
GET    /api/internal/code-review-views
GET    /api/integrations/scm/cloud-apps

# 遥测
POST   /api/ingest                        # telemetry.factory.ai
POST   /api/otlp/traces/ingest            # OTLP traces
```

### 1.3 运行期确认（**【运行期】**）

- Renderer 启动参数实测包含：
  `--factory-backend-origin=https://api.factory.ai`、`--factory-airgap-enabled=false`、
  `--factory-customer-prem=false`。→ **后端 origin 是启动参数注入的，可在启动时覆盖。**
- 观察窗口内 `telemetry.factory.ai` 建连 **104 次**、`api.factory.ai` **10 次**。
  遥测连接频率远高于 API，是识别“真客户端”的显著特征。

---

## 2. 鉴权全流程

### 2.1 Header 常量（**【静态】**，`index-vvcnYwSf.js`）

```js
const yn  = "1.340.0";            // SDK 版本
const Ste = "X-Factory-Client";            // 值：web-desktop | web-app | web-workspace | daemon | cli | sdk | backend
const Bte = "X-Factory-Request-Surface";   // 值见下方 Lt 枚举
const Dte = "X-Client-Version";            // 客户端版本（desktop 取构建 SHA/版本）
const Bm  = "X-Factory-Org-Id";            // 活动组织 ID
const vte = "X-Factory-Whoami-Extended";   // 仅 whoami 用，值 "true"
const kte = "X-Factory-Session-Attribution"; // 会话归因（可选）
const Qte = "X-Factory-Exec-Output-Format";
const wte = "X-Factory-Exec-Input-Format";
```

`X-Factory-Request-Surface` 取值枚举（**【静态】** `Lt`）：

```
web, desktop, cli_tui, cli_exec, cli_acp, slack, jira, linear,
microsoft-teams, google-chat, imessage, sessions_api, api, sdk,
automation, readiness-remediation, readiness-evaluation
```

`X-Factory-Client` 取值枚举（**【静态】** `kn`）：
`web-desktop, web-app, web-workspace, daemon, cli, sdk, backend`

**桌面端的归因默认值（**【静态】** `getRequestAttribution`）**：

```js
clientType === "desktop" → { client: "web-desktop", requestSurface: "desktop" }
clientType === "web"     → { client: "web-app",     requestSurface: "web" }
clientType === "sdk"     → { client: "sdk",         requestSurface: "sdk" }
clientType === "cli"     → { client: "cli" }
```

### 2.2 凭据获取与注入（**【静态】**）

```js
// 凭据读写（keychain 优先）
async function dV(ctx) { const c = await vf(ctx); return c?.token ?? null; }

// 普通请求头
async getHeaders() {
  if (customerDeployment) return await gV(ctx);   // 自托管：Authorization: Bearer <apiKey|token> + X-Factory-Org-Id
  const token = await dV(ctx);
  const h = {};
  if (token) h.Authorization = `Bearer ${token}`;
  return h;
}

// 桌面 whoami
function XKe({credential, activeOrganizationId}) {
  return {
    Authorization: `Bearer ${credential}`,
    ...(activeOrganizationId && { "X-Factory-Org-Id": activeOrganizationId }),
  };
}
fetch(`${baseUrl}/api/cli/whoami`, {
  headers: { ...XKe(...), "X-Factory-Whoami-Extended": "true", /* 可选 X-Factory-Session-Attribution */ }
});
```

凭据落盘：`~/.factory/auth.v2.loginkeychain`（**【运行期】** 存在，`chmod 600`）。
**【推断】** 该文件为“keychain 引用”格式（体积 2222B，非明文 token），真实 token 存于登录钥匙串。

### 2.3 请求头合并顺序（**【静态】**，`rf()` + `BUe()`）

```
最终 headers =  merge(
   原始 Headers（调用方显式设置的）,
   await requestContext.getHeaders(),      // Authorization / X-Factory-Org-Id
   ro.injectContext(ctx),                  // W3C traceparent / tracestate
   调用方 headers 参数
)
```

`BUe` 实现：按 `key.toLowerCase()` 去重，**value 保留首个命中的原始大小写 key**。
→ 反代若要“看起来一样”，需保留 `Authorization`、`X-Factory-Client` 等原始大小写。

### 2.4 追踪注入（**【静态】**）

遥测事件属性会并入：

```js
{
  traceId, spanId,             // OTel 活动 span
  sentryTraceId, sentrySpanId, // Sentry 活动 span（两者同时存在时都带）
}
```

另在 daemon 侧 relayinge 时携带 `_meta: { traceparent, tracestate }`。

### 2.4.1 鉴权服务 = WorkOS User Management（**【静态】**）

> 重要修正：`/api/auth/*` 在 `api.factory.ai` 上返回 **404（Next.js 页面）**，**【运行期】**实测。
> 也就是说登录/刷新**不经过** `api.factory.ai`，而是直接打 WorkOS；`api.factory.ai` 只验证 Bearer。

```js
const dYe = "https://api.workos.com/user_management";   // 默认 base
function xW(ctx) { return ctx?.workosBaseUrl ?? dYe; }

// Client ID 按环境区分
const uYe = "client_01HNM7927XNSKCJ4982Z5J3FFZ";        // 非生产
const IYe = "client_01HNM792M5G5G1A2THWPXKFMXB";        // 生产
function CYe(env) { return env === "production" ? IYe : uYe; }
const hYe = "/welcome/desktop-callback";                 // 浏览器回调落地页
```

**token 刷新（唯一观测到的 WorkOS 调用形态）**：

```http
POST https://api.workos.com/user_management/authenticate
Content-Type: application/x-www-form-urlencoded

grant_type=refresh_token
&refresh_token=<refresh_token>
&client_id=<client_01HNM792M5G5G1A2THWPXKFMXB>
&organization_id=<可选：活动组织>
```

响应 schema（**【静态】** `YW`）：`{ access_token, refresh_token }`

**组织 ID 来源**（**【静态】** `Yp`）：

```js
Yp(resp) => {
  token: resp.access_token,
  activeOrganizationId: EI(resp.active_organization_id),   // ← 即 X-Factory-Org-Id 的值
}
```

即 **`X-Factory-Org-Id` 直接来自 WorkOS 返回的 `active_organization_id`**。调用方若要切组织，
是带 `organization_id` 重新 authenticate，而不是改本地字段。

凭据类型枚举（**【静态】** `hs`）：`prem | api-key | workos`。
- `api-key` 形态：`TE({type:"api-key", token: <apiKey>}, null)` → **org 为 null**（不提 `X-Factory-Org-Id`）。
- `airgap`（离线部署）直接走 api-key，不走 WorkOS。

### 2.4.2 实测端点行为（**【运行期】**）

用 `X-Factory-Client: web-desktop` + `X-Factory-Request-Surface: desktop` + `traceparent`：

| 端点 | 无 token 响应 | 含义 |
|---|---|---|
| `GET /api/llm/a/v1/messages` | `405` | 路由存在，仅接受 POST |
| `GET /api/cli/whoami` | `401` `{"detail":"Missing authorization token in HTTP headers","status":401,"title":"Unauthorized","requestId":"sin1::<id>"}` | 网关会鉴权并返回**结构化错误** |
| `GET /api/auth/config` | `404`（Next.js HTML） | 不在该主机 |
| `GET /api/auth/whoami` | `404`（Next.js HTML） | 不在该主机 |

`requestId` 前缀 `sin1::` 是 POP/机房标识，可用于判断路由区域。

### 2.5 凭据落盘格式（**【静态】** + **【运行期】**）

`~/.factory/auth.v2.loginkeychain`，2222 字节，非 JSON，形如：

```
<iv_b64>:<tag_b64>:<ciphertext_b64>
  Vqhp2GnJGdiOnMhnPmAFjA== : xYhjggg140LnJeh2LG1qHA== : OFNH96zqOA...
```

对应实现（**【静态】**）：

```js
const hA = 32, a8 = 16, exe = 16;   // 密钥 32B / IV 16B / GCM tag 16B
function encrypt(plain, key) {
  const iv  = randomBytes(a8);
  const c   = createCipheriv("aes-256-gcm", key, iv);
  const ct  = Buffer.concat([c.update(plain, "utf8"), c.final()]);
  return `${iv.base64}:${c.getAuthTag().base64}:${ct.base64}`;
}
// 解密前强校验：iv 长度必须 16、tag 长度必须 16，否则 "Invalid IV or auth tag length"
```

**结论（对反代的意义）**：多账号池**不能简单复制这个文件**——AES-256-GCM 密钥在钥匙串里，
换机器/换钥匙串条目即解不开。要做“账号切肤”，两条可行路径：

1. **保存 `access_token` + `refresh_token`（在内存/keyring 中）**，按需调 WorkOS `/authenticate` 刷新；
   反代只需 `Authorization: Bearer <access_token>`（+ 可选 `X-Factory-Org-Id`）。
2. **一账号一 `~/.factory` 目录**（`FACTORY_HOME` 级隔离），让每个 Factory 实例各自持有完整状态，
   互不干扰——这是最贴近真客户端的方式。

`auth.v2.loginkeychain` 的 keychain 引用还说明：**在 CI/容器里没有钥匙串时会降级或失败**，
这也是“伪造客户端”最容易暴露的地方之一（真客户端一定带着可用的系统钥匙串上下文）。

### 2.6 会话/静默登录（**【静态】**，`initLifecycle` 段）

- 桌面支持 **浏览器回调登录**：打开 `authUrl`，回调 `factory-desktop://callback?code=..&state=..`，
  再 `Fw({code, state, callbackUrl})` 交换 token。
- 支持 **relay 免密登录**：`nonce` 生成 → `relay receive/decline` 轮询，带 `pollIntervalMs`/`ackTimeoutMs`。
- `desktopState` 有 `SignedOut / SignedInSameUser / SignedInOtherUser` 三态，用于多账号切换提示。

---

## 3. 推理主链路（写反代的核心）

### 3.1 供应商 → 路径映射（**【静态】**）

```js
const V5e = {
  ANTHROPIC: "/api/llm/a/v1/messages",
  OPENAI:    "/api/llm/o/v1/responses",
  FACTORY:   "/api/llm/factory",     // 值 Hh.factory
  GENERIC_CHAT_COMPLETION_API: Hh.factory,
};
// 其它同族全部映射：
Q.VERTEX_ANTHROPIC / BEDROCK_ANTHROPIC / AZURE_ANTHROPIC / SNOWFLAKE / FIREWORKS
    → Dn.AnthropicMessages  ("/api/llm/a/v1/messages")
Q.BASETEN / TOGETHER / GOOGLE
    → Dn.OpenAIChatCompletions ("/api/llm/o/v1/chat/completions")
Q.OPENAI / AZURE_OPENAI / BEDROCK_OPENAI / DATABRICKS / XAI
    → Dn.OpenAIResponses ("/api/llm/o/v1/responses")
```

### 3.2 Anthropic 请求体的 Factory 扩展（**【静态】**，`OV`/`Wd`/`q1e`）

思考档位 → 协议字段：

```js
// 关闭 / 未开启
{ thinking: { type: "disabled" } }            // 若模型 supportsDisabledThinking
// 开启
{
  thinking: { type: "adaptive", display?: "..." },   // display 透传
  output_config: { effort: "low"|"medium"|"high"|"xhigh"|"max" },
  // 当 display === "updates" 时追加 beta 标志 Y1e
}
// 特定模式（q1e）
{ thinking: { type: "between_tools" }, output_config: { effort: "high" } }
```

> `output_config.effort` 与 `thinking.type: "adaptive"` 是 **Factory 自己的扩展**，不是公开 Anthropic API
> 的字段。反代若照抄公开 SDK，会被判定为非 Factory 客户端。
> `Y1e` 的具体 beta 字符串未在 JS 常量区展开，**【推断】** 为 `anthropic-beta` 列表项。

### 3.3 流式与超时（**【静态】**）

- `jp` 枚举即流式看门狗原因：`connection deadline / idle / connection draining / turn deadline / turn stalled`。
- `transportConfig`：`maxConnectRetries: 0`、`initialRetryDelayMs: 0`、`connectionTimeoutMs: <cfg>` →
  **传输层默认不重试**，重试由上层业务决定（对反代复刻的连接语义很重要）。
- Header 过滤白名单（错误/重放场景）：`["accept", "content-type"]`。

### 3.4 上下文上传（**【静态】**，`/api/context`）

限制值（可作反代校验参考）：

```
XR = 256*1024        单段文本上限
R$ = 64*1024         block 级上限
fC = 256*1024
T$ = 3*1024*1024     总上下文上限
N$ = image/png, image/jpeg, image/gif, image/webp, application/pdf, application/zip
O$ = png/jpeg/gif/webp 10MB, pdf 25MB, zip 50MB
```

`ki = z.uuid()`，`uL = discriminatedUnion("kind", [ space{spaceId,documentId}, local{machineId, path} ])`。

---

## 4. 遥测（“中间做了什么遥测”）

### 4.1 自建遥测客户端（**【静态】**，`TelemetryClient` 类）

```js
FLUSH_INTERVAL_MS      = 2000
MAX_EVENTS_PER_FLUSH   = 100
MAX_DEFERRED_EVENTS    = 1000
endpoint = (process.env.VITE_TELEMETRY_INGEST_BASE_URL || "https://telemetry.factory.ai") + "/api/ingest"
method   = POST
headers  = {
  "Content-Type": "application/json",
  "X-Factory-Client": window.electronAPI ? "web-desktop" : "web-app",
  "X-Client-Version": <sha|version>,      // 可选
  "Authorization": "Bearer <token>",       // 登录后
}
body     = { events: [ { type, name, value, attributes, ... }, ... ] }
```

每条事件注入的属性（**【静态】** `addEvent`）：

```js
attributes = {
  ...event.attributes,
  clientTimestampMs: Date.now(),
  clientPath: window.location.pathname || "unknown",
  isClientMetric: type === METRIC,
  isClientLog:    type === LOG,
  clientPlatform: "desktop" | "web",
  tags: {
    ...event.tags,
    clientType: "web-desktop",
    environment: "production",
    clientVersion: <sha>,          // 若有
    sessionId: <从 pathname 提取>,  // 若有
    orgRegion: <地区>,             // 若有
    traceId / spanId / sentryTraceId / sentrySpanId,
  },
}
```

### 4.2 事件类型（**【静态】**，`Ef`）

```
session-started
droid-message
droid-status
code-channel-diff
turn-failed
pull-request-created
user-message          ← 关键：含正文
```

`user-message` 的结构（**【静态】**）：

```js
{
  kind: "user-message",
  messageId: string(≤256),
  text: string(≤40000),                          // 用户输入原文
  author?: { userId: string(≤256), name?: string(≤256) },
  attachments?: [{ type: "image"|"file", name?, mediaType? }] (≤50)
}
```

`code-channel-diff` 会上传代码 diff 内容（`content` 上限 1e6 字节，带 `baseBranch`/`headBranch`/`repository{name,url}`）。

### 4.3 开关（**【静态】**）

```js
isWebTelemetryDisabled() = webTelemetryDisabledGetter?.() === true || th()
th() = process.env.NODE_ENV === "test" || process.env.VITEST_WORKER_ID != null
```

→ **生产默认开启**，没有 UI 级“彻底关闭”，只有 `webTelemetryDisabledGetter` 注入点（可由宿主关闭）。

### 4.4 Sentry（**【静态】**，`index-Cu4a0f0m.js`）

```
DSN: https://00c38558e24f2d680bcb535f9df08b71@o4508485941854208.ingest.us.sentry.io/4510280784936960
ipcMode: IPCMode.Classic
transport: makeElectronOfflineTransport(makeElectronTransport)
before-send 会删除：request.headers["User-Agent"] 等敏感头
```
**【运行期】** 观察到 renderer 进程对 `o4508485941854208.ingest.us.sentry.io` 的 TLS 建连。
OTLP traces 走 `POST /api/otlp/traces/ingest`（**【静态】**）。

---

## 5. 设备指纹与身份体系（“伪造真实用户”的核心）

### 5.1 落盘位置（**【静态】** + **【运行期】**）

| 文件 | 内容 | 说明 |
|---|---|---|
| `~/.factory/host.json` | `{schemaVersion:1, hostId: UUID, createdAt: ms, computerRegistration?:{computerId, firestoreOrgId, userId, registeredAt}}` | 主机身份 + 设备注册 |
| `~/.factory/computer.json` | `{computerId: UUID, registeredAt: ms}` | 本机：仅注册信息 |
| `~/.factory/<computerIdentityPath>` | `{schemaVersion:1, publicKey: b64, privateKey: b64, keyId, algorithm:"X25519"}` | 计算机密钥对（**【推断】** 实际路径名未在 JS 常量展开） |
| `~/.factory/telemetry/droid-installation-id` | UUIDv4 文本 | CLI 安装标识 |
| `~/.factory/auth.v2.loginkeychain` | keychain 引用 | 鉴权凭据 |
| `~/.factory/org-managed-settings.cache.json` | 组织策略缓存 | 影响可用的模型/权限 |

本机实测值（脱敏）：

```
hostId        = 6e57a4…3093   (UUID, len 36)
computerId    = 011406…b3d9   (UUID, len 36)
firestoreOrgId= l8Ucsy…oWdc   (len 20)
userId        = user_0…PRTH   (len 31)
installation  = b1f4e455-c605-41bf-aad1-2cdac150cb0f
```

### 5.2 身份生成逻辑（**【静态】**，`class Bm`）

```js
generateHostId = default () => crypto.randomUUID()

async getComputerIdentity() {
  const existing = await store.loadComputerIdentity();
  if (existing) return decode(existing);
  return store.withHostConfigLock(async () => {
    const id = await createComputerIdentity();     // X25519 keypair + keyId
    await store.saveComputerIdentity({
      schemaVersion: 1,
      publicKey:  b64(id.publicKey),
      privateKey: b64(id.privateKey),
      keyId:      id.keyId,
      algorithm:  "X25519",
    });
    return id;
  });
}
```

要点：
- **密钥对完全在本地生成、以明文（base64）落盘**，不经服务端下发 → 任何人可自行生成“新设备”。
- `keyId` 由密钥派生；`validateComputerIdentity` 校验 keypair 自洽。
- `computerRegistration` 只在该注册的 `userId` 与 `firestoreOrgId` 与当前登录身份**都匹配**时才有效
  （`getValidComputerRegistration`），否则视为未注册 → 换号会重新注册。
- 主机配置损坏时会被隔离（`quarantineHostConfig("invalid-<ts>")`）并**重新生成 hostId**。

### 5.3 计算机注册流程（**【运行期】** + **【静态】**）

`initLifecycle` 中的自动注册（**【静态】**）：

```js
const { apiBaseUrl } = getEnv();
const { hostId } = await Cc().getHostIdentity(authContext);
fetch(customFetch(Ka.computers.detail(computerId).hosted.path), {
  headers: { Authorization: `Bearer ${token}` }, signal
});
```

即：**先取本地 hostId → 带 Bearer 调 computers 接口 → 保存返回的 computerId 到 `host.json`**。
若无 token 则跳过（`"[computer] No auth token available, skipping auto-registration"`）。

### 5.4 其它“环境指纹”来源（**【静态】**）

- Renderer 启动参数：`--factory-backend-origin`、`--factory-airgap-enabled`、`--factory-customer-prem`、
  `--factory-window-translucency-supported`。服务端若读取，可据此区分客户端形态。
- 浏览器 UA 由 Electron 自生成；代码中**未发现**改写 UA 的逻辑，也无证书固定
  （全量扫描 `certificatePinning`/`pinnedPublicKeys`/`rejectUnauthorized`/`setCertificateVerifyProc` 均 0 命中）。
- `requestAttribution` 结构化上报 `surface / runtime(local|byom|droid-computer|cloud-workspace) /
  version / cliVersion / platform / arch / osVersion`（**【静态】** `aw` schema）→ **这是最强的指纹上报表**。

```js
// 【静态】aw schema
{ surface: "desktop"|"web"|"cli",
  runtime?: "local"|"byom"|"droid-computer"|"cloud-workspace",
  version?, cliVersion?, platform?, arch?, osVersion?, ... }
```

---

## 6. 反代复刻检查表（让请求“看起来像真客户端”）

按重要性排序：

1. **鉴权头**：`Authorization: Bearer <token>`，多组织时必带 `X-Factory-Org-Id`。
2. **客户端标识**：`X-Factory-Client: web-desktop`、`X-Factory-Request-Surface: desktop`、
   `X-Client-Version: <版本>`（**保持原始大小写**）。
3. **推理路径**：必须用 `/api/llm/a/v1/messages` 或 `/api/llm/o/v1/responses` 等网关前缀，
   而不是直连上游 `/v1/messages`。
4. **Anthropic 扩展字段**：`thinking.type="adaptive"` + `output_config.effort`。
5. **追踪头**：`traceparent`（W3C 格式：`00-<32hex traceid>-<16hex spanid>-01`）。
6. **身份三元组**：`hostId` / `computerId` / X25519 密钥对，三者要能对上；换号时要重注册。
7. **遥测**：真客户端启动后会对 `telemetry.factory.ai` 高频建连；反代若完全不发，行为特征即不同。
8. **连接语义**：`maxConnectRetries=0`、2s 遥测批量、流式看门狗。

---

## 7. 未验证/边界

- **未能解出明文请求体**：本机系统信任的根证书（`Local CAPTURE ROOT CA`）**无私钥**（0 valid identities），
  且沙箱不允许安装新的受信根证书（`sudo` 受限、`security add-trusted-cert` 被拒）。
  因此 §3/§4 的报文级细节是**静态还原**，不是抓包实录。要补抓包，见 §8。
- `Y1e`（条件 beta 标志）、`Hh.factory` 的实际路径值、`computerIdentityPath` 实际文件名未展开。
- `Ka.computers.*` 的完整路径表（此处只能看到 `.detail(id).hosted.path` 的调用形态）。
- `/api/auth/*` 在 `api.factory.ai` 上实测 404 —— 真实鉴权走 WorkOS（§2.4.1）。
  但 `auth.exchange` 的 PKCE 交换体未被直接观测到（只有 refresh_token 形态确认）。
- WorkOS 的 `GET /user_management/organizations`、`/users/me` 等读接口未被调用捕获，
  只知道 base 与 client_id。

---

## 8. 补抓明文的最小操作

需要**一个有私钥且被系统信任**的根证书。二选一：

**方案 A（推荐）**：由你执行（沙箱不允许我 sudo）：
```bash
# 1) 让 mitmproxy 生成 CA 并安装为受信根（需弹窗输密码）
openssl x509 -in /tmp/factory-re/mitmconf/mitmproxy-ca-cert.pem -noout -fingerprint -sha256
sudo security add-trusted-cert -d -r trustRoot \
     -k /Library/Keychains/System.keychain /tmp/factory-re/mitmconf/mitmproxy-ca-cert.pem
# 2) 我已经准备好的抓包/切代理脚本
/tmp/factory-re/set-proxy.sh on    # 切到 18080（会链到 Clash 10808）
/tmp/factory-re/run-capture.sh start
# ... 操作 Factory（发一条消息、触发一次登录、等 10s）...
/tmp/factory-re/set-proxy.sh off   # 还原 Clash
```

**方案 B**：用你已有的 Reqable（`~/Library/Application Support/com.reqable.macosx`），
它自带可用的根证书与私钥，导出 HAR 给我解析即可。
注意 Reqable 当前根证书 `Reqable CA (Apr 21, 2026)`（SHA-256 `4C:FD:28:A3:…:60`）**也未在系统信任列表中**，
需先在 Reqable 里点“安装证书”完成系统信任。

抓包后我可以直接把这些补进本文档：每个端点的 request/response 头与体、SSE 帧样例、错误码语义。

---

## 9. 推理链路实测抓包（**【抓包实录】**，2026-11-18）

> 本节是**实际抓到的明文报文**，不再是静态推断。抓取方式：让内嵌 droid CLI 经 mitmproxy 出网，
> 关键开关是 `NODE_TLS_REJECT_UNAUTHORIZED=0`（droid 不读 macOS 钥匙串，必须关证书校验才肯走 MITM）。

### 9.1 抓取命令（可复现）

```bash
# 1) 起 mitmproxy（regular 模式即可）
mitmdump --listen-port 18080 --mode regular \
  --set confdir=/tmp/factory-re/mitmconf --set connection_strategy=lazy \
  -s /tmp/factory-re/wscapture.py

# 2) 让 droid 经代理出网（关键：NODE_TLS_REJECT_UNAUTHORIZED=0）
B=/Applications/Factory.app/Contents/Resources/bin/droid
NODE_TLS_REJECT_UNAUTHORIZED=0 \
HTTPS_PROXY=http://127.0.0.1:18080 ALL_PROXY=http://127.0.0.1:18080 \
  "$B" exec -o json "Reply with exactly: OK"
```

**两个反直觉的点**（都是踩坑得出的）：

1. **droid 默认不走系统代理**。它是 Go/Bun 二进制，`lsof` 实测直连 Vercel IP。
   必须显式给 `HTTPS_PROXY`/`ALL_PROXY` 环境变量。
2. **只给代理变量还不够**。droid 的 TLS 根证书库不读 macOS 钥匙串，
   会拒绝 mitmproxy 的 CA（表现为 `Client TLS handshake failed`）。
   `FACTORY_EXTRA_CA_CERTS` / `NODE_EXTRA_CA_CERTS` / `SSL_CERT_FILE` **都不生效**，
   唯一有效的是 `NODE_TLS_REJECT_UNAUTHORIZED=0`。

### 9.2 真实端点：`prem.factory.ai`（不是 api.factory.ai！）

```
WSS  https://prem.factory.ai/api/llm/o/v1/responses/ws       → 101 Switching Protocols
```

> 这是重要修正：推理走 **prem.factory.ai 的 WebSocket**，而**不是** `api.factory.ai/api/llm/...` 的 HTTP。
> 之前静态分析看到的 `/api/llm/o/v1/responses` 是 HTTP 回退路径，正常情况下用的是 `/ws` 升级。

### 9.3 握手请求头（明文实录）

```http
GET /api/llm/o/v1/responses/ws HTTP/1.1
Host: prem.factory.ai
Connection: Upgrade
Upgrade: websocket
Sec-WebSocket-Version: 13
Sec-WebSocket-Extensions: permessage-deflate; client_max_window_bits
Sec-WebSocket-Key: <随机 16B base64>
Authorization: Bearer <access_token>
OpenAI-Platform: org-bHuLtG1fGmYk5YaOihAAXFBw
User-Agent: factory-cli/1.13.0
X-Client-Version: 1.13.0
X-Factory-Client: cli
X-Factory-Exec-Output-Format: json
X-Factory-Org-Id: <org id>
X-Factory-Request-Surface: cli_exec
x-api-provider: openai
x-provider-routing-source: configured_order
x-session-id: <session uuid>
```

新增的、之前静态分析**没发现**的头：

| 头 | 值 | 含义 |
|---|---|---|
| `OpenAI-Platform` | `org-<base64>` | 把 Factory org 映射成 OpenAI 组织 |
| `x-api-provider` | `openai` | 上游供应商 |
| `x-provider-routing-source` | `configured_order` | 路由策略：按配置顺序 |
| `x-session-id` | UUID | 会话维度 |

`X-Factory-Client: cli` / `X-Factory-Request-Surface: cli_exec` —— **CLI 形态与桌面端的 `web-desktop`/`desktop` 不同**。

### 9.4 客户端帧：`response.create`（48768 字节，完整结构）

用的是 **OpenAI Responses 协议**（`type: response.create`），字段如下：

```json
{
  "type": "response.create",
  "model": "gpt-6.1-sol",
  "store": false,
  "tools": [ /* custom/Script, function/WaitForScript */ ],
  "instructions": "You are Droid, an AI software engineering agent built by Factory.  You are running non-interactively. You are direct, plain-spoken, and precise. Complete the user's request without waiting for further input.  # Harness - Text outside tool calls is shown to the user as GitHub-flavored Markdown. - In ...",
  "tool_choice": "auto",
  "parallel_tool_calls": true,
  "include": ["reasoning.encrypted_content"],
  "reasoning": { "effort": "high", "summary": "auto" },
  "text": { "verbosity": "low" },
  "prompt_cache_key": "<sessionId>",
  "prompt_cache_retention": "24h",
  "safety_identifier": "<sessionId>",
  "input": [
    { "role": "user", "content": [{ "type": "input_text", "text": "<system-reminder>\nUnified tool catalog: ... " }] },
    { "role": "user", "content": [{ "type": "input_text", "text": "<用户输入>" }] }
  ],
  "_factory": {
    "assistantMessageId": "<uuid>",
    "traceContext": { "traceparent": "00-<32hex>-<16hex>-01" },
    "usageAttribution": {
      "request_surface": "cli_exec",
      "exec_output_format": "json",
      "client_type": "cli",
      "client_version": "1.13.0"
    }
  }
}
```

**`_factory` 是 Factory 私有扩展**，包含：
- `assistantMessageId`：服务端用它做幂等/归因
- `traceContext.traceparent`：W3C 追踪，和遥测侧共用
- `usageAttribution`：**计费/统计归因**，声明本次请求来自哪个 surface、什么输出格式、什么客户端版本

> 反代若不发 `_factory.usageAttribution`，服务端会缺归因信息；
> 若发错（比如桌面端发 `cli_exec`），就是明显的伪造痕迹。

### 9.5 服务端帧序列（SSE 等价的事件流，走 WS）

```
response.created            34KB   含 response 元数据（access_programs 等）
response.in_progress        34KB
response.output_item.added  240B   item.type=message, phase=final_answer
response.content_part.added 231B   part.type=output_text
response.output_text.delta  217B   {"delta":"OK"}   ← 流式增量
response.output_text.done   184B   {"text":"OK"}
response.content_part.done  232B
response.output_item.done   302B
response.completed          34KB   含 usage
```

`response.completed` 里的 usage（**实测**）：

```json
{
  "input_tokens": 11210,
  "input_tokens_details": {
    "cached_tokens": 0,
    "cache_write_tokens": 11207,
    "cache_write_tokens_details": { "ttl_30m_tokens": 11207, "ttl_12h_tokens": 0 }
  },
  "output_tokens": 5
}
```

> **11210 输入 token 里 11207 是缓存写入** —— 说明 Droid 的系统提示 + 工具定义非常庞大（约 11K token），
> 每次新会话都要付这笔缓存写入成本。这是反代做额度规划必须知道的量级。

### 9.6 与静态分析的差异更正

| 项 | 之前静态推断 | 实测结果 |
|---|---|---|
| 推理主机 | `api.factory.ai` | **`prem.factory.ai`** |
| 推理传输 | HTTP POST + SSE | **WebSocket**（`/api/llm/o/v1/responses/ws`） |
| 协议 | Anthropic Messages 也有 | 走 OpenAI **Responses** (`response.create`) |
| 私有扩展 | `thinking.adaptive`/`output_config` | OpenAI 侧用 `reasoning.effort`+`_factory` |
| UA | `Factory/1.13.0 ... Electron` | CLI 侧是 **`factory-cli/1.13.0`** |

### 9.7 原始证据文件

已归档到仓库（token 已脱敏）：

- `docs/factory-capture/llm-request.json` —— 完整 `response.create` 请求体（48768B）
- `docs/factory-capture/ws-server-frames.json` —— 服务端 9 个事件帧
- `docs/factory-capture/ws-frames.ndjson` —— 握手 + 全部帧（Authorization/Sec-WebSocket-Key 已脱敏）
- `docs/factory-capture/wscapture.py` —— 抓包 addon

---

## 10. CLI 交互模式实测抓包（**【抓包实录】**，2026-11-18 21:59）

> 本次让 `droid` **交互式 TUI** 经 mitmproxy 出网，完整覆盖启动、多轮对话、工具调用、遥测上报。
> 与 §9 的区别：§9 是 `exec` 单轮；本节是交互模式（`subcommand: tui`），数据更全。
> CLI 版本 **1.14.0**（`~/.local/bin/droid`，curl 安装），模型 `gpt-6.2-sol`。

### 10.1 抓取方式（唯一可行组合）

```bash
NODE_TLS_REJECT_UNAUTHORIZED=0 \
HTTPS_PROXY=http://127.0.0.1:18080 \
ALL_PROXY=http://127.0.0.1:18080 \
NO_PROXY=localhost,127.0.0.1 \
  droid --cwd /tmp
```

三个变量缺一不可：CLI 不读系统代理，且不读 macOS 钥匙串（详见 §9.1）。

### 10.2 实测接口清单（36 条 HTTP + 2 条 WS）

| 次数 | 方法 | 主机 | 路径 | 用途 |
|---|---|---|---|---|
| 8 | POST | prem.factory.ai | `/api/apps/cloud-sync/sessions/batch` | 会话同步（含 hostId） |
| 4 | GET | api.factory.ai | `/api/billing/limits` | 额度查询 |
| 3 | POST | telemetry.factory.ai | `/api/ingest` | **遥测批量上报** |
| 2 | GET | api.factory.ai | `/api/cli/whoami` | 身份校验 |
| 2 | GET | api.factory.ai | `/api/organization/managed-settings` | 组织策略 |
| 2 | GET | api.factory.ai | `/api/feature-flags` | 功能开关 |
| 2 | GET | prem.factory.ai | `/api/llm/o/v1/responses/ws` | **推理 WebSocket（101）** |
| 2 | GET | prem.factory.ai | `/api/internal/user-preferences` | 用户偏好 |
| 1 | POST | api.factory.ai | `/api/connectors/tools/list` | 拉工具目录 |
| 1 | GET | api.factory.ai | `/api/connectors/tools/revision` | 工具版本 |
| 1 | GET | api.factory.ai | `/api/connectors/list` | 连接器列表 |
| 1 | GET | api.factory.ai | `/api/integrations/model-providers` | 模型供应商 |
| 1 | POST | api.factory.ai | `/api/automations/sync` | 自动化同步（含 hostId） |
| 1 | POST | prem.factory.ai | `/api/llm/o/v1/responses` | **推理 HTTP 回退** |
| 1 | POST | prem.factory.ai | `/api/llm/a/v1/messages` | **Anthropic 协议（会话标题）** |
| 1 | POST | prem.factory.ai | `/api/sessions/{id}/attention-snapshot` | 注意力快照 |
| 1 | GET | api.workos.com | `/sso/jwks/client_01HNM…` | 登录公钥 |
| 1 | GET | downloads.factory.ai | `/factory-cli/LATEST` | 版本检查 |
| 1 | GET | clientservices.googleapis.com | `/chrome-variations/seed` | Chromium 变体（环境噪声） |

### 10.3 请求头（四类，各不相同）

**A. 推理 WebSocket 握手**

```http
GET /api/llm/o/v1/responses/ws HTTP/1.1
Host: prem.factory.ai
Upgrade: websocket
Authorization: Bearer <token>
OpenAI-Platform: org-bHuLtG1fGmYk5YaOihAAXFBw
User-Agent: factory-cli/1.14.0
X-Client-Version: 1.14.0
X-Factory-Client: cli
X-Factory-Exec-Output-Format: json
X-Factory-Org-Id: <org>
X-Factory-Request-Surface: cli_exec
x-api-provider: openai
x-provider-routing-source: configured_order
x-session-id: <session uuid>
```

**B. 业务 HTTP（会话同步等）——UA 是 `Bun/1.4.4` 而非 factory-cli**

```http
Authorization: Bearer <token>
Content-Type: application/json
X-Client-Version: 1.14.0
X-Factory-Client: cli
X-Factory-Exec-Output-Format: json
X-Factory-Org-Id: <org>
X-Factory-Request-Surface: cli_exec
traceparent: 00-<32hex>-<16hex>-01
User-Agent: Bun/1.4.4
```

**C. whoami（带扩展标记）**

```http
Authorization: Bearer <token>
X-Factory-Org-Id: <org>
X-Factory-Whoami-Extended: true
User-Agent: Bun/1.4.4
```

**D. Anthropic 协议（会话标题生成，走 Bedrock）**

```http
anthropic-version: 2023-06-01
x-api-provider: bedrock_anthropic
x-assistant-message-id: oneshot-assistant-<ts>
x-message-call-type: session-title-generation
x-factory-request-surface: cli_tui
User-Agent: factory-cli/1.14.0
```

**E. 遥测** —— 最简，只有认证和 UA：

```http
Authorization: Bearer <token>
Content-Type: application/json
User-Agent: Bun/1.4.4
```

### 10.4 新发现的请求头（之前未记录）

| 头 | 值示例 | 出现在 |
|---|---|---|
| `OpenAI-Platform` | `org-bHuLtG1fGmYk5YaOihAAXFBw` | 推理（把 Factory org 映射成 OpenAI org） |
| `x-assistant-message-id` | `<uuid>` / `oneshot-assistant-<ts>` | 推理、Anthropic |
| `x-message-call-type` | `session-title-generation` | 副任务标记（**区分主对话和后台小任务**） |
| `x-provider-routing-source` | `configured_order` | 路由策略 |
| `x-provider-routing-refresh` | `1` | feature-flags 强制刷新 |
| `anthropic-version` | `2023-06-01` | Anthropic 路径 |
| `priority` / `a-im` / `if-none-match` | — | 静态资源/版本检查 |

### 10.5 设备标识：**不在请求头，在请求体**

全量扫描 38 个请求头，**没有任何** `device-id` / `host-id` / `machine-id` / `fingerprint` 类头。设备标识通过三处下发：

**① Host ID —— 放在请求体**

```json
// POST /api/apps/cloud-sync/sessions/batch
{ "operations": [ { "sessionId": "<uuid>", "type": "create",
    "session": { "hostId": "6e57a4db-3d6a-48d7-8ea9-51a111063093",
                 "machineConnectionType": "tui", ... } } ] }

// POST /api/automations/sync
{ "hostId": "6e57a4db-...", "daemonVersion": "1.14.0", "snapshot": { "hostId": "6e57a4db-...", ... } }
```

**② droidInstallationId —— 只在遥测 tags 里**

```json
"tags": { "droidInstallationId": "b1f4e455-c605-41bf-aad1-2cdac150cb0f" }
```

**③ 推理体里是 trace + 会话归因，没有设备 ID**

```json
"_factory": {
  "assistantMessageId": "<uuid>",
  "traceContext": { "traceparent": "00-<32hex>-<16hex>-01" },
  "usageAttribution": {
    "request_surface": "cli_tui",
    "client_type": "cli",
    "client_version": "1.14.0"
  }
}
```

> 注意 `request_surface` 在 TUI 下是 **`cli_tui`**（exec 是 `cli_exec`），
> 但 WS 握手头里仍是 `cli_exec` —— 两者不一致，服务端以谁为准需另行确认。

### 10.6 遥测：一次 POST 打包 257 条事件

```
POST https://telemetry.factory.ai/api/ingest
体大小: 259662 B
体结构: { "events": [ ... ] }
```

单条事件结构：

```json
{
  "type": "log",
  "level": "info",
  "message": "Using cached system certificates",
  "attributes": {
    "value": "188 certificates, cached 209 minutes ago",
    "tags": {
      "clientType": "cli", "platform": "darwin", "environment": "production",
      "version": "1.14.0", "os": "darwin 24.6.0",
      "terminal": "Orca", "terminalVersion": "1.4.239",
      "subcommand": "tui", "isDroidExec": "false",
      "isStreamJsonRpcWorker": "false", "callingSessionIdPresent": "false",
      "isInteractiveTty": "true",
      "droidInstallationId": "b1f4e455-..."
    }
  },
  "_queuedAt": ...
}
```

**实测采集到的 tag 全集**（即它的“设备画像”）：

```
clientType, platform, environment, version, os,
terminal, terminalVersion,          ← 终端程序名和版本（Orca 1.4.239）
subcommand, isDroidExec, isStreamJsonRpcWorker, callingSessionIdPresent,
isInteractiveTty,
droidInstallationId
```

**事件类型分布**：本次 257 条全是 `log`（`type: "log"`）。消息去重后主要在记：

```
43  [tui-startup] Startup phase completed
11  [SettingsService] Hooks and custom models refreshed
 8  [tui-startup] Boundary reached
 6  [Daemon] Settings updated
 5  [metrics_log_cli_startup_phase_ms]        ← 性能指标
 5  [metrics_log_pre_turn_step_duration_ms]
 4  Cloud user preferences pull finished
 3  [SandboxService] Sandbox not enabled
 3  [McpService] MCP config unchanged
 3  [metrics_log_cli_tui_keypress_queue_latency]
```

> 本次**未**捕获到 `/api/otlp/traces/ingest`（链路追踪上报），也未捕获 `user-message` 类正文事件。
> 这只说明本次窗口内没发，不能证明 CLI 永不上报。

### 10.7 WebSocket 帧（497 条）

```
客户端 → 服务端: 2 条 response.create（60999B / 48770B）
服务端 → 客户端: 495 条事件
```

服务端事件分布：

```
426  response.output_text.delta            ← 流式文本
 55  response.custom_tool_call_input.delta ← 工具调用参数流式下发
  2  response.created / response.in_progress
  2  response.output_item.added / output_item.done
  2  response.completed
  1  response.custom_tool_call_input.done
  1  response.content_part.added / done
```

`response.create` 结构（本次 7 个工具）：

```json
{
  "type": "response.create",
  "model": "gpt-6.2-sol",
  "store": false,
  "tools": ["ExitSpecMode","AskUser","Script","WaitForScript",
            "TaskOutput","TaskStop","Task"],
  "tool_choice": "auto",
  "parallel_tool_calls": true,
  "include": ["reasoning.encrypted_content"],
  "reasoning": { "effort": "high", "summary": "auto" },
  "text": { "verbosity": "low" },
  "prompt_cache_key": "<sessionId>",
  "prompt_cache_retention": "24h",
  "safety_identifier": "<sessionId>"
}
```

### 10.8 两条推理路径并存

| 路径 | 协议 | 用途 |
|---|---|---|
| `prem.factory.ai/api/llm/o/v1/responses/ws` | OpenAI Responses over WS | 主对话 |
| `prem.factory.ai/api/llm/o/v1/responses` | 同上，HTTP 回退 | WS 失败时 |
| `prem.factory.ai/api/llm/a/v1/messages` | Anthropic Messages | **会话标题生成**（`x-message-call-type`），模型 `claude-haiku-4-5-20251001`，上游 `bedrock_anthropic` |

> 反代若只实现 OpenAI Responses 一条，会漏掉 Anthropic 副任务；漏掉时表现是“对话正常但标题不生成”。

### 10.9 对反代的关键结论

1. **UA 有两套**：推理用 `factory-cli/<版本>`，业务 HTTP 和遥测用 `Bun/1.4.4`。混用是明显破绽。
2. **设备标识放在请求体**，不在头里 —— 只复制头不够，`cloud-sync/batch` 和 `automations/sync` 必须带正确的 `hostId`。
3. **遥测的 `droidInstallationId` 是独立 UUID**，与 hostId、computerId 都不同。
4. **`_factory.usageAttribution.request_surface`** 必须与真实端匹配：`cli_tui`（交互）/ `cli_exec`（exec）/ `desktop`（桌面端）。
5. **`traceparent` 同时出现在头（业务 HTTP）和体（`_factory.traceContext`）**，格式均为 W3C。
6. **遥测是最高频请求**：3 次 POST 就发了 257 条事件、260KB；反代若完全不上报，行为特征明显不同。

### 10.10 原始证据（已脱敏归档）

- [`docs/factory-capture/cli-tui/endpoints.json`](docs/factory-capture/cli-tui/endpoints.json) —— 40 条请求的方法/主机/路径/状态/完整头
- [`docs/factory-capture/cli-tui/telemetry-sample.json`](docs/factory-capture/cli-tui/telemetry-sample.json) —— 一次遥测 POST 的 257 条事件原文
- [`docs/factory-capture/cli-tui/ws-requests.json`](docs/factory-capture/cli-tui/ws-requests.json) —— 2 次 WS 握手 + 2 条 `response.create`

> `Authorization` 已替换为 `Bearer <REDACTED>`；`hostId`/`sessionId`/`droidInstallationId` 保留原值以便结构核对，如需对外分享请先替换。

### 10.11 遥测发送节奏（实测量化，23 个批次样本）

**两种模式，差异明显：**

| 状态 | 节奏 | 载荷 |
|---|---|---|
| **空闲** | 严格约 60 秒一次 | 固定 3397B / 3 条事件 |
| **活跃** | 事件驱动，0.4s～267s 不等 | 2～299 条事件（中位 7） |

空闲期实测间隔：`59.2s / 59.6s / 59.9s / 60.1s / 61.2s`（连续 5 次）。

**空闲心跳的 3 条事件（内容稳定、数值变化）：**

```
[metrics_log_daemon_agent_in_process_session_count]   value: 0
[metrics_log_daemon_agent_subprocess_session_count]   value: 1
[metrics_log_daemon_event_loop_delay_p99_ms]          value: 5.816319
```

同一批事件三次内容**不完全一致**（`event_loop_delay_p99_ms` 每次不同）。

**关键：遥测是延迟上报的。** 用事件自带 `_queuedAt` 与实际发送时刻对比：

| 发送时刻 | 批次 | 最老事件等待 | 最新事件等待 |
|---|---|---|---|
| 22:04:37 | 16 | **183.6s** | 154.0s |
| 22:04:46 | 7 | **163.1s** | 163.1s |
| 22:04:57 | 299 | 23.3s | 7.1s |
| 22:06:41 | 36 | 21.0s | 2.9s |

有事件在本地队列里滞留约 3 分钟才上报。

**推断机制**（**【推断】**，非直接观测）：发送端存在约 **21 秒**的 flush tick（缓冲空则跳过）；
空闲时每 60 秒生成一批 3 条指标，正好每个 tick 发出一次，故表面呈现 60 秒周期。

> 注意：静态分析中 Web 侧遥测是 `FLUSH_INTERVAL_MS = 2000`，与 CLI 侧节奏**不同**，
> 两者不可混用。21 秒的 tick 是从批次排队时长反推的，不是读到的定时器参数。

**对反代的含义：**

1. 空闲时必有 **60 秒心跳**，载荷恒为 3397B —— 最稳定的识别特征
2. 遥测**有延迟**，最长约 3 分钟；实时上报反而不像真客户端
3. 心跳内容**必须变化**（`event_loop_delay_p99_ms` 每次不同），写死固定值即为伪造痕迹

---

## 11. 交互模式深度实测补充（【抓包实录】2026-11-19 00:28）

### 11.1 遥测频率（65 个批次，双模式确认）

| 状态 | 间隔 | 批次 | 载荷 |
|---|---|---|---|
| **空闲** | **60.0s**（中位，52 个样本，实测 58.5–61.7s） | 3 条事件 | **3397 B** |
| **活跃** | **23.2s**（中位，最小 20.4s，12 个样本） | 15–119 条 | 16–139 KB |

实测连续空闲序列：

```
00:18:56  n=3   3397B
00:19:56  +60.0s  n=3   3397B
00:20:56  +59.9s  n=3   3397B
00:21:56  +60.0s  n=3   3397B
00:22:56  +60.0s  n=3   3397B
00:23:56  +60.0s  n=3   3397B
00:24:55  +58.5s  n=21  28375B   ← 活动插入
00:27:17  +20.4s  n=87  93896B   ← 活跃期最小间隔
00:27:37  +20.7s  n=33  36680B
00:28:32  +20.9s  n=73  87212B
```

**活跃期最小间隔稳定在 20.4–21 秒**，与 §10.11 从排队时长反推的 ~21s flush tick 一致，本次是对 tick 的直接佐证。

### 11.2 额度接口（`GET /api/billing/limits`）—— 响应体完整解出

bro tli 压缩，解压后 690B：

```json
{
  "usesTokenRateLimitsBilling": true,
  "hasActiveSubscription": true,
  "limits": {
    "standard": {
      "fiveHour": { "usedPercent": 3, "windowEnd": "2026-11-18T18:32:52.554Z", "secondsRemaining": 7479 },
      "weekly":   { "usedPercent": 1, "windowEnd": "2026-11-24T21:04:14.291Z", "secondsRemaining": 534961 },
      "monthly":  { "usedPercent": 1, "windowEnd": "2026-12-17T21:04:14.291Z", "secondsRemaining": 2522161 }
    },
    "core": {
      "fiveHour": { "usedPercent": 0, "windowEnd": null, "secondsRemaining": null },
      "weekly":   { "usedPercent": 0, "windowEnd": null, "secondsRemaining": null },
      "monthly":  { "usedPercent": 0, "windowEnd": null, "secondsRemaining": null }
    }
  },
  "overagePreference": "droidCore",
  "canManageOverage": true,
  "extraUsageBalanceCents": 0,
  "extraUsageAllowed": true
}
```

**要点**：
- 两套额度池：`standard` 与 `core`，各有 `fiveHour` / `weekly` / `monthly` 三档
- 每档只有 `usedPercent` + `windowEnd` + `secondsRemaining`，**没有绝对额度数值**
- `core` 池全为 0/null，说明当前订阅只走 `standard`
- 请求头是普通业务头（`Bun/1.4.4` + `X-Factory-Request-Surface: cli_tui`），**无特殊参数**

### 11.3 推理路径：实测四条并存

| 路径 | 上游 | 模型 | 请求体 |
|---|---|---|---|
| `/api/llm/o/v1/responses/ws` | openai | `gpt-6.2-sol` | WS 帧 |
| `/api/llm/o/v1/responses` | openai | `gpt-6.2-sol` | ~45KB |
| `/api/llm/o/v1/chat/completions` | **baseten** | `deepseek-v4.1-flash-1017` | ~58KB |
| `/api/llm/g/v1/generate` | **google** | `gemini-4-carbon` | **~458KB** |
| `/api/llm/a/v1/messages` | **bedrock_anthropic** | `claude-haiku-4-5-20251001` | ~2KB（标题生成） |

### 11.4 请求头对照（三条 HTTP 推理路径）

**chat/completions（Baseten）**

```http
Accept: application/json
Authorization: Bearer <token>
Content-Type: application/json
User-Agent: factory-cli/1.14.0
traceparent: 00-<32hex>-<16hex>-01
x-api-provider: baseten
x-assistant-message-id: <uuid>
x-client-version: 1.14.0
x-factory-client: cli
x-factory-org-id: <org>
x-factory-request-surface: cli_tui
x-provider-routing-source: session_lock
x-session-id: <uuid>
```

**g/v1/generate（Google）**

```http
Accept: */*
Authorization: Bearer <token>
Content-Type: application/json
User-Agent: factory-cli/1.14.0
X-Client-Version: 1.14.0
X-Factory-Client: cli
X-Factory-Org-Id: <org>
X-Factory-Request-Surface: cli_tui
traceparent: 00-<32hex>-<16hex>-01
x-api-provider: google
x-assistant-message-id: <uuid>
x-provider-routing-source: session_lock
x-session-id: <uuid>
```

> 注意 `x-provider-routing-source` 这次是 **`session_lock`**，之前是 `configured_order`。
> 含义推断：会话已锁定到某个供应商后不再重选路由。

### 11.5 请求体结构

**chat/completions（58KB）**

```json
{
  "model": "deepseek-v4.1-flash-1017",
  "messages": [ 5 条: system / user / user / assistant / user ],
  "tools": [15 个: Read, LS, Execute, Edit, Grep, Glob, Create, ExitSpecMode,
            AskUser, TodoWrite, TaskOutput, TaskStop, ToolSearch, Skill, Task],
  "stream": true,
  "stream_options": { ... },
  "max_tokens": 131072,
  "temperature": 1,
  "reasoning_effort": "high"
}
```

消息分层：
- `system`（9945B）—— Droid 系统提示，开头 `You are Droid, an AI software engineering agent built by Factory.`
- `user`（13045B）—— **环境上下文 + 工具目录**（见 11.6）
- `user`（4B）/ `assistant`（31B）/ `user`（12B）—— 真实对话轮次

**g/v1/generate（458KB）**

```json
{
  "model": "gemini-4-carbon",
  "contents": [ 51 条: user×32, model×19 ]   // 311KB
  "systemInstruction": { ... },               // 12854B
  "tools": [ { "functionDeclarations": [7 个: ExitSpecMode, AskUser, Script,
              WaitForScript, TaskOutput, TaskStop, Task] } ],   // 36KB
  "generationConfig": {
    "maxOutputTokens": 65536,
    "thinkingConfig": { "includeThoughts": true, "thinkingLevel": "LOW" }
  }
}
```

> Gemini 侧 `thinkingConfig.includeThoughts: true` —— **明确要求返回思维链**。
> 但 `thinkingLevel: "LOW"`，档位很低。

### 11.6 设备指纹：写进 prompt，不在请求头

`contents[0].parts[0].text` 开头即环境自检：

```
User system info (darwin 24.6.0)
Resources: 10 CPUs, 24 GB memory
Disk: 82 GB free of 460 GB (working directory, /var/... and home share this filesystem)

User language: en

# Session working directory
% pwd
/private/tmp

% ls
<会话启动时 /tmp 的完整文件列表>
```

**采集到的指纹字段**：

| 字段 | 示例 |
|---|---|
| 系统版本 | `darwin 24.6.0` |
| CPU 核数 | `10 CPUs` |
| 内存 | `24 GB memory` |
| 磁盘 | `82 GB free of 460 GB` |
| 语言 | `en` |
| 工作目录 | `pwd` 输出 |
| 目录内容 | `ls` 全量输出（**含所有文件名**） |
| git 状态 | `git status --short --branch` |

> 这是比请求头强得多的指纹：**用户名下的目录结构会随对话一起发给模型**。
> 反代若伪造 `hostId` 但 prompt 里的环境信息与真实机器不符，反而更可疑。

---

## 12. 全量接口普查与总结（【抓包实录】2026-11-19 03:10）

> 抓包窗口 23:31–03:10（约 3.5 小时），累计 **399 条 HTTP**、92 次 TLS 握手、27 次 LLM 请求。
> 本节按主机归类，覆盖此前未记录的接口。

### 12.1 接口总览（按主机）

| 主机 | 次数 | 端点 |
|---|---|---|
| `telemetry.factory.ai` | **242** | `/api/ingest`（遥测批量） |
| `prem.factory.ai` | 78 | 见 12.3 |
| `api.factory.ai` | 67 | 见 12.2 |
| `clientservices.googleapis.com` | 8 | `/chrome-variations/seed`（Chromium 变体，环境噪声） |
| `api.workos.com` | 3 | `/sso/jwks/client_01HNM…`（验签公钥） |
| `update.googleapis.com` | 1 | `/service/update2/json`（Chromium 组件更新） |

> 早期窗口还出现过 `otel-collector-123358924663.us-central1.run.app/v1/metrics`
> 和 `telemetry.factory.ai/api/otlp/traces/ingest`，本窗口未复现。

### 12.2 `api.factory.ai`（67 次）

| 次数 | 方法 | 端点 | 用途 |
|---|---|---|---|
| 37 | GET | `/api/organization/managed-settings` | **组织策略（高频轮询）** |
| 17 | GET | `/api/billing/limits` | 额度百分比 |
| 8 | GET | `/api/feature-flags` | 功能开关 |
| 3 | POST | `/api/automations/sync` | 自动化快照同步 |
| 1 | POST | **`/api/tools/web-search`** | 🆕 **联网搜索工具** |
| 1 | POST | **`/api/tools/get-url-contents`** | 🆕 **网页抓取工具** |

### 12.3 `prem.factory.ai`（78 次）

| 次数 | 方法 | 端点 | 用途 |
|---|---|---|---|
| 47 | POST | `/api/apps/cloud-sync/sessions/batch` | 会话同步（10 秒一次） |
| 10 | POST | `/api/llm/o/v1/chat/completions` | OpenAI Chat 协议 |
| 9 | POST | `/api/llm/g/v1/generate` | Gemini 协议 |
| 4 | GET | `/api/llm/o/v1/responses/ws` | WS 握手 |
| 4 | POST | `/api/llm/a/v1/messages` | Anthropic 协议 |
| 4 | GET | `/api/internal/user-preferences` | 用户偏好 |

### 12.4 新发现的工具类接口（重要）

当模型调用**联网工具**时，CLI 会请求宿主服务端代抓，而不是直接出网：

**网页搜索**

```http
POST api.factory.ai/api/tools/web-search
x-client-model: gemini-4-carbon
x-session-id: <uuid>
traceparent: 00-<32hex>-<16hex>-01
User-Agent: Bun/1.4.4

{"query":"Los Angeles weather today","knowledge":"core","numResults":5}
```

响应返回结构化结果：

```json
{"results":{"web":[{"title":"...","url":"https://www.accuweather.com/...","description":"...","snippets":[...]}]}}
```

**网页抓取**

```http
POST api.factory.ai/api/tools/get-url-contents

{"url":"https://api.open-meteo.com/v1/forecast?latitude=34.0522&..."}
```

响应：

```json
{"data":{"markdown":"```json\n{...}\n```"}}
```

> 注意：`get-url-contents` 把网页转成 markdown 返回。
> **这两个接口意味着：模型的“上网”是服务端代做，客户端不需要外网访问目标站点。**

### 12.5 组织策略（`managed-settings`）实测内容

```json
{
  "success": true,
  "settings": {
    "routedModelVisibilityEnabled": false,
    "modelPolicy": {
      "blockedModelIds": ["gpt-6.2-astra-ultrafast", "gpt-6-astra-ultrafast", "gpt-6.1-sol-ultrafast"],
      "allowAllFactoryModels": true,
      "allowCustomModels": true,
      "isFastModelsAllowed": true,
      "requireExplicitOptInModelIds": ["gpt-6.2-astra-ultrafast", ...]
    }
  },
  "factoryTier": "pro_plus",
  "cmekEnabled": false,
  "cloudSyncDisabledByCompliance": false
}
```

**37 次轮询**说明策略变更会被快速感知；这也是反代最容易忽略的接口之一。

### 12.6 功能开关实测（节选）

```json
{"flags":{
  "unified_model_fallbacks": false,
  "anthropic_linked_billing": false,
  "claude_fable_5_2": true,
  "repository_checkout_collision_handling": true,
  "shared_process_agents": true,
  "azure_devops_integration": true,
  "workspaces_enabled": false,
  "droid_shield_2.0": false,
  ...
}}
```

### 12.7 频率总结（3.5 小时样本）

| 通道 | 次数 | 节奏 |
|---|---|---|
| `/api/ingest` 遥测 | 242 | **空闲 60.0s**（193 次命中 60±5s）；活跃中位 **24.4s**；体积中位 3397B、最大 228KB |
| `cloud-sync/sessions/batch` | 47 | **间隔中位 10.0s** |
| `organization/managed-settings` | 37 | 约每 5–6 分钟 |
| `billing/limits` | 17 | 约每 12 分钟 |
| `feature-flags` | 8 | 较稀疏 |

> **会话同步每 10 秒一次**是仅次于遥测的高频请求，且每次 POST 都携带 `hostId`。
> 反代若不复刻这个 10 秒节奏 + hostId，行为特征差异明显。

### 12.8 模型 → 供应商全量映射（实测）

| 模型 | `x-api-provider` | 协议路径 | 次数 |
|---|---|---|---|
| `gemini-4-carbon` | google | `/g/v1/generate` | 9 |
| `deepseek-v4.1-flash-1017` | baseten | `/o/v1/chat/completions` | 5 |
| `kimi-k3.1-pro` | **databricks** | `/o/v1/chat/completions` | 3 |
| `claude-sonnet-5-5` | bedrock_anthropic | `/a/v1/messages` | 2 |
| `claude-haiku-4-5-20251001` | bedrock_anthropic | `/a/v1/messages` | 2 |
| `gpt-6.2-sol` | openai | `/o/v1/responses/ws` | 4 |

供应商分布：`google 9 / baseten 7 / openai 4 / bedrock_anthropic 4 / databricks 3`

路由来源：`session_lock 16 / configured_order 11`

**并发子任务可用不同供应商**：03:03 窗口内 14 秒发 4 次调用，跨 databricks 与 baseten。
