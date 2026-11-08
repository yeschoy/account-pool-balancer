# Mistral 接口契约（实测确认）

> 全部结论来自 2026-11-07 真实会话实测，标注 ✅ = 已实测通过，❌ = 实测不存在。

## 域名分工

| 域名 | 角色 | 技术栈 |
|---|---|---|
| `auth.mistral.ai` | Ory Kratos 自托管鉴权 | Go |
| `v2.auth.mistral.ai` | 登录/注册 UI | Next.js (Turbopack) |
| `admin.mistral.ai` | Dashboard（组织/账单/Key） | Next.js + Kong 网关 |
| `chat.mistral.ai` | Le Chat / Vibe | Next.js + tRPC |

---

## 1. 鉴权（Ory Kratos）

### 1.1 邮箱探测 ✅
```http
POST https://auth.mistral.ai/self-service/login?flow={flowId}
Referer: https://v2.auth.mistral.ai/
Content-Type: application/json
Origin: https://v2.auth.mistral.ai

{"csrf_token":"...","identifier":"user@icloud.com","method":"identifier_first","transient_payload":{}}
```
- 账号存在 → `200`
- 账号不存在 → `400` + `ui.nodes[].messages[].id == 4000037`
- **前端靠这个错误码决定跳注册还是登录**

### 1.2 创建流程
```
GET  /self-service/login/browser?return_to=...
GET  /self-service/registration/browser?return_to=...&after_verification_return_to=...
GET  /self-service/verification/browser
```
返回 `{id, expires_at(30min), ui:{action, method, nodes[]}}`，`csrf_token` 在 `ui.nodes` 里，**每次 POST 后轮换**。

### 1.3 注册 ✅
```http
POST https://auth.mistral.ai/self-service/registration?flow={flowId}
{"csrf_token":"...","method":"password","password":"...",
 "traits":{"email":"...","name":{"first":"","last":""}},"transient_payload":{}}
```
成功返回完整 session（含 `identity.id`、`session.id`、`verifiable_addresses[].status="sent"`）。
- session 有效期 **90 天**
- `authenticator_assurance_level: aal1`

### 1.4 邮箱验证 ✅
```http
POST https://auth.mistral.ai/self-service/verification?flow={flowId}
{"code":"123456","csrf_token":"...","method":"code","transient_payload":{}}
```
成功：`{"state":"passed_challenge","ui":{"action":"https://admin.mistral.ai/...","method":"GET"}}`

---

## 2. Dashboard tRPC（admin.mistral.ai）

**双端点**：`/api/local-trpc/`（旧）与 `/api/trpc/`（新）

### 输入编码（关键）
```
GET  ?input=<urlencode(JSON)>
POST body = <JSON>，无 batch 时不要带 trpc-accept: application/jsonl
```
含 `undefined` 字段时必须用 meta 标记：
```json
{"json":{"status":"active",...,"name":null},
 "meta":{"values":{"name":["undefined"],"sort":["undefined"]},"v":1}}
```
> ⚠️ 漏掉 meta.values 会报 `expected object, received undefined`

### 2.1 会话与身份 ✅
```
GET /api/local-trpc/user.meSession
→ {"organization":{"uuid":"..."},"workspace":{"uuid":"..."}}
```

### 2.2 订阅状态 ✅
```
GET  /api/local-trpc/billing.subscriptions.get?input={"json":{"planType":"API"}}
→ {"json": null}  // 无订阅
```

### 2.3 **创建 API Key ✅（非 tRPC！）**
```http
POST https://admin.mistral.ai/api/billing/api-keys
Content-Type: application/json

{"name":"my-key",
 "workspace_uuid":"bf1cedfe-084b-48cb-ab4d-c3787f186735",
 "primitive_access_scope":"personal_and_shared",
 "product":"API"}
```
**响应（含明文 key，仅此一次）**：
```json
{"key_id":"a0ac4bf3-...","key":"mstrl_XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX_XXXXXX",
 "name":"probe-prod-API","hidden_key":"****wY80",
 "created_at":"2026-11-07T21:01:08.970Z","expiration_date":null,
 "primitive_access_scope":"personal_and_shared"}
```

**product 取值 ✅**：`API` / `Vibe` / `Studio`（三者均实测成功）
- `API` → 平台 API Key
- `Vibe` → Vibe/CLI Key
- `Studio` → Studio Key

**primitive_access_scope**：`personal_and_shared` | `shared_only`

### 2.4 API Key 列表 ✅
```
GET /api/billing/api-keys           → {"keys":[{key_id,name,hidden_key,created_at,last_used,can_delete,...}]}
GET /api/local-trpc/albe.apiKeys.list?input={...}
```

### 2.5 删除 Key ✅
```
DELETE /api/billing/api-keys/{key_id}
→ {"detail":"API key deleted successfully"}
```

### 2.6 轮换 Key
```
POST /api/billing/api-keys/{key_id}/rotate
→ 需 body（422: Field required），推测与前次相同结构
```

### 2.7 **取消订阅 ✅（唯一入口，tRPC）**
```http
POST https://admin.mistral.ai/api/local-trpc/billing.subscriptions.cancelUnsubscription
Content-Type: application/json
x-trpc-source: nextjs-react

{"json":{"planType":"API"}}
```
- 无订阅 → `400` `"No subscription in cancellation"`
- ❌ 以下 REST 路径均 404：`/api/billing/subscriptions/cancel`、`/api/billing/subscription/cancel`、`DELETE /api/billing/subscription`、`/api/billing/cancel-subscription`

### 2.8 订阅创建 ✅
```http
POST /api/local-trpc/billing.subscribeLeChat
{"json":{"plan":"INDIVIDUAL","periodicity":"MONTHLY","seats":null},
 "meta":{"values":{"seats":["undefined"]},"v":1}}
→ {"status":"active","subscription_uuid":"9b6c618b-...","action_url":null}
```

### 2.9 支付方式 ✅
```http
POST /api/local-trpc/billing.updateInfo            # 账单地址
POST /api/local-trpc/billing.createPaymentMethodSetupIntent
→ {"intent_secret":"seti_1UZFotJVYnTkVDT6ZhA8DYmp_secret_..."}
```
> Stripe 主账户 ID：`1UZFotJVYnTkVDT6`

---

## 2.10 **接受 AI Studio ToS ✅（弹窗 "Accept and continue"）**

```http
POST https://console.mistral.ai/api-ui/trpc/user.acceptTos?batch=1
Content-Type: application/json
x-trpc-source: nextjs-react
trpc-accept: application/jsonl
x-csrftoken: <csrftoken cookie>
Referer: https://console.mistral.ai/plateforme/studio

{"0":{"json":null,"meta":{"values":["undefined"],"v":1}}}
```

**注意三个容易踩的点**：
1. 域名是 `console.mistral.ai`，**不是** `admin.mistral.ai`
2. 路径前缀是 `/api-ui/trpc/`，**不是** `/api/trpc/` 或 `/api/local-trpc/`
3. 必须 `?batch=1` + batch 对象 body；缺 `x-csrftoken` 直接 **403**

响应是 tRPC jsonl 流式格式：
```
{"json":{"0":[[0],[null,0,0]]}}
{"json":[0,0,[[{"result":0}],["result",0,1]]]}
{"json":[1,0,[[{"data":0}],["data",0,2]]]}
{"json":[2,0,[[null]]]}
```

**幂等**：已接受后弹窗不再出现，重复调用仍返回 200。

**为什么必须做**：AI Studio（Studio）和 Pro 订阅都要求先接受这个 ToS，
否则生成的 Key 无法使用，Studio 页面也会 404。


---

## 2.11 chat.mistral.ai：Le Chat / Vibe 协议

**域名**: `chat.mistral.ai`
**前缀两个**: `/api/trpc/`（常规）、`/api/code-trpc/`（Vibe Code 专用）

### 2.11.1 接受 ToS ✅
```http
POST https://chat.mistral.ai/api/trpc/user.acceptToS?batch=1

{"0":{"json":{"newsletter":false}}}
```
返回 `{"tosAccepted":true,"country":"CA",...}`

> Vibe 页面的 "Accept and continue" 调的就是这个，`newsletter:false`。
> 之前抓到的 `newsletter:true` 是另一处入口。

### 2.11.2 隐私开关 ✅
```http
POST /api/trpc/user.setMessageTrainingUse?batch=1    {"0":{"json":{"enabled":false}}}
POST /api/trpc/user.setKnowledgeEnabled?batch=1      {"0":{"json":{"enabled":false}}}
POST /api/trpc/user.setLocalisationConsent?batch=1   {"0":{"json":{"consent":false}}}
```
全部返回 200。

### 2.11.3 **生成 Vibe CLI Key ✅（有前置条件）**
```http
POST https://chat.mistral.ai/api/code-trpc/apiKey.generateApiKey?batch=1
Content-Type: application/json
x-trpc-source: nextjs-react

{"0":{"json":null,"meta":{"values":["undefined"],"v":1}}}
```

成功返回：
```json
[{"result":{"data":{"json":{
  "vibeApiKey":"mstrl_XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX_XXXXXX",
  "vibeApiKeyId":"d934b2ca-9ad6-4739-9125-a4f0f4394982"}}}}]
```

**两个前置条件**（缺一必 400 `Bad request`）：

| 条件 | 判定方法 |
|---|---|
| ① 已接受 Vibe ToS | 页面出现 "Accept and continue"，或 `user.session.tosAccepted == true` |
| ② **已完成 billing setup** | `admin billing.info.is_billing_setup == true`（有过支付方式） |

> 实测：`is_billing_setup=false` 的账号，ToS 已接受、页面已加载，
> 该接口仍固定返回 `400 Bad request`。

### 2.11.4 user.session ✅
```
GET https://chat.mistral.ai/api/trpc/user.session
→ {"id":"...","user":{"id","email","tosAccepted","memoriesEnabled","knowledgeEnabled"},...}
```

---

## 2.12 各域协议可用性矩阵（实测 2026-11-08）

| 域名 | 纯 HTTP 协议 | 说明 |
|---|---|---|
| `auth.mistral.ai` | ✅ 完全可用 | 注册/发码/验码全部走协议 |
| `admin.mistral.ai` | ✅ 完全可用 | meSession/billing/api-keys/subscriptions 全部走协议 |
| `console.mistral.ai` | ❌ 403 | CF 校验 JA3，curl 无法通过 |
| `chat.mistral.ai` | ❌ 403 | 同上 |

**403 表现**：`Just a moment...`（Cloudflare 挑战页）
即使浏览器先访问并拿到 `__cf_bm`，curl 仍被拦——缺 `cf_clearance` 且 TLS 指纹不符。

**结论**：必须混合。
- 浏览器负责 console / chat 的两个 ToS + Vibe Key 生成
- 其余全部协议


---

## 3. 环境识别点（反指纹重点）

### 3.1 客户端提示头（高熵，34 条请求携带）
```
sec-ch-ua: "Not:A-Brand";v="8", "Chromium";v="156", "Google Chrome";v="156"
sec-ch-ua-full-version: "156.0.8078.80"
sec-ch-ua-full-version-list
sec-ch-ua-platform: "macOS"
sec-ch-ua-platform-version: "15.7.7"      ← OS 版本泄露
sec-ch-ua-arch: "arm"                      ← CPU 架构
sec-ch-ua-bitness: "64"
sec-ch-ua-model: ""
sec-ch-ua-mobile: ?0
sec-ch-ua-wow64: ?0
sec-ch-ua-form-factors: "Desktop"
sec-ch-prefers-color-scheme: light
```
**必须整体伪造一致**：UA 字符串与这 12 个头任意不一致即为强信号。

### 3.2 网络质量头
```
downlink: 1.45   (Mbps)
rtt: 350         (ms)
```
> 实测值偏低（代理链路导致），住宅 IP 正常应 5-20 Mbps / 50-150ms。**异常值可暴露代理**。

### 3.3 风控组件
| 组件 | 域 | 作用 |
|---|---|---|
| Cloudflare Turnstile | `challenges.cloudflare.com` | 人机验证（`0x4AAAAAAAVBu8-ohcZ6vwPe`） |
| Cloudflare Bot Mgmt | `*mistral.ai/cdn-cgi/challenge-platform` | `cf_clearance` / `__cf_bm` / `_cfuvid` |
| Stripe Fingerprint | `js.stripe.com/v3/fingerprinted/*` | 支付指纹 |
| Stripe hCaptcha | `hcaptcha-invisible-*.html` | 不可见人机 |
| Axept 行为检测 | `static.axept.io/behavior-detection.min.js` | 鼠标/键盘/滚动行为 |
| Address Gen | `usaddressgen.com` | 账单地址自动填充（用户在页内生成的） |

### 3.4 Cookie 结构
```
.mistral.ai              ory_session_<project_hash>   主会话，90天
.mistral.ai              cf_clearance / __cf_bm / _cfuvid
.mistral.ai              csrf_token_<hash>
auth.mistral.ai          __Host-ory_kratos_browser_<hash>
admin.mistral.ai         anonymousUser / browser-session-id / csrftoken
```
> `admin` 域是 `anonymousUser`，真实身份靠父域 `ory_session` 传给 tRPC。

### 3.5 Next.js RSC
```
rsc: 1
next-url: /
next-router-state-tree: <编码的 router state>
next-router-prefetch: 1
```
伪造 UA 时如果缺失或格式错乱，会被识别为非真实 Next.js 客户端。

---

## 4. 关键常量

| 项 | 值 |
|---|---|
| Kratos project hash | `1d61ec8f0158ec4868343239ec73dbe1bfebad9908ad860e62f470c767573d0d` |
| Stripe 账户 | `1UZFotJVYnTkVDT6` |
| Turnstile sitekey | `0x4AAAAAAAVBu8-ohcZ6vwPe` |
| Sentry DSN org | `4507101628137472` / project `4509804461097040` |
| Intercom app | `xel0jpx9` |
| Axept project | `6a0b8527e6c59986c40d26de` |
| Kong 网关 | `x-kong-proxy-latency` 响应头 |
