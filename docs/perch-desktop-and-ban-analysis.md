# Perch Desktop 逆向与封禁判定分析

**目标**：`Perch Terminal Desktop` v2.5.2（`app.perchai.app` / `www.perchai.app/zh-tw/desktop` 分发）
**来源**：官方 `https://download.perchai.app/desktop/latest/mac-arm64.dmg`（323,390,095 字节）
**artifact 身份**：`CFBundleIdentifier = app.perchai.desktop`，`CFBundleShortVersionString = 2.5.2`
**主载荷**：`Contents/Resources/app.asar`（778,987,220 字节，78,458 条目，56,539 个 JS，283.8 MB）
**分析范围**：`dist-electron/main.js`（5,312,228 字节，Electron 主进程，含全部业务逻辑）、`dist-electron/preload.js`（31,446 字节）、`package.json`
**方法**：直接解析 ASAR 索引后按偏移提取，字节级定向读取；未执行该应用，未登录，未向 Perch 发送任何请求
**日期**：2026-11-03

> 证据等级：【观测】直接读自产物字节并给出位置；【推断】从代码结构得出的结论；【未知】产物内无证据。

---

## 0. 结论摘要

**桌面端与 CLI 发给 Perch 的模型请求，在协议层面没有区别。** 桌面端不存在桌面专属的签名、attestation、token 或 header。`perch_surface_required` 与 `clientSurface` 无关，因此**在客户端伪造"我是官方桌面端"这条路不存在可模仿的对象**。

封禁判定发生在服务端，依据是客户端无法观测的信号。已确认的封禁理由有两条，均直接写在服务端返回文案里：

| 理由 | 我方实测账号数 |
|---|---|
| `using Perch through another app or tool` | 22 |
| `using multiple accounts on the free tier` | 24 |

---

## 1. 分发与载荷结构

【观测】`/zh-tw/desktop` 页面指向 5 个平台产物：

```
https://download.perchai.app/desktop/latest/mac-arm64.dmg
https://download.perchai.app/desktop/latest/mac-x64.dmg
https://download.perchai.app/desktop/latest/windows-x64.exe
https://download.perchai.app/desktop/latest/linux-x64.AppImage
https://download.perchai.app/desktop/latest/linux-x64.deb
```

页面标注 `v 2.5.1 · 2026-10-29`，但实际 DMG 内 `Info.plist` 与 `package.json` 均为 **2.5.2**（【观测】）——分发清单滞后于产物，说明版本号不能作为协议判定依据。

【观测】ASAR 顶层只有三项：

```
/dist-electron      main.js, preload.js, perchBusinessTools/
/node_modules       828 个包
/package.json
```

`package.json`：`"main": "dist-electron/main.js"`，`"bin": { "perch": "./bin/perch" }`（`bin` 在 asar 内不存在，属打包残留）。

【观测】asar 的 unpacked 条目中 `node_modules/@img/sharp-libvips-darwin-arm64/lib/libvips-cpp.8.17.3.dylib` 的声明哈希与伴随文件不一致（声明 `ad39fe2d…`，实测 `39a146cc…`）。这是 macOS 对 dylib 二次签名后的正常现象，不是篡改。

---

## 2. 桌面端与 CLI 的请求完全相同

### 2.1 模型调用的完整 header

【观测】`main.js` `sendModelCallViaServer`：

```javascript
const response = await fetchModelProxyWithRetry(endpoint, {
  method: "POST",
  headers: {
    "Content-Type": "application/json",
    ...upload.headers,
    ...hasStreamingHandler ? { Accept: "text/event-stream" } : {},
    ...modelProxyAuthHeader(opts?.cliServerAccessToken ?? null),
    ...cliUserAgentHeader(opts?.cliServerAccessToken ?? null),
    ...cliLocaleHeader(),
    ...await currentTurnTicketHeader(ticketRunIdFor(opts?.runId))
  },
  body,
  signal: _signal
}, ...);
```

逐项解析：

| header | 来源函数 | 值 |
|---|---|---|
| `Authorization` | `modelProxyAuthHeader` | `Bearer <token>`（显式 token 优先，否则 `PERCH_MODEL_CALL_PROXY_TOKEN`） |
| `User-Agent` | `cliUserAgentHeader` | `perchai-cli/<PERCH_CLI_VERSION>`；**浏览器上下文不发** |
| `x-perch-locale` | `cliLocaleHeader` | 仅 `state.host === "cli"` 时发送 |
| `x-perch-turn-ticket` | `currentTurnTicketHeader` | 票值 |
| `Accept` | 内联 | 仅流式时 `text/event-stream` |

【观测】这与 `perchai-cli@2.4.120` 的 `A9t` 完全对应，包括"浏览器不发 UA"和"仅流式声明 SSE"这两个条件分支。

### 2.2 envelope 结构

【观测】同函数：

```javascript
const body = JSON.stringify({
  ...upload.body(serializableRequest),
  runId: opts?.runId,
  lane: opts?.lane ?? "chat",
  strictManual: opts?.strictManual ?? false,
  raceMode: opts?.raceMode,
  preferredModelId: opts?.preferredModelId ?? null,
  avoidModelIds: opts?.avoidModelIds ?? [],
  attribution: opts?.attribution ?? null,
  clientSurface: opts?.clientSurface ?? (opts?.cliServerAccessToken ? "cli" : null),
  ...
});
```

**关键**：`clientSurface` 只有两种取值——显式传入的值，或 `"cli"`（有 token 时），否则 `null`。全 bundle **不存在** `clientSurface: "desktop"`（【观测】grep 计数为 0）。

### 2.3 端点集合

【观测】`main.js` 中全部 `/api/` 字面量：

```
"/api/perch-terminal/model-call"    × 4
"/api/perch-terminal/turn-ticket"   × 2
"/api/v2/cortex/v1/messages"        × 1   （Snowflake 直连分支，与 Perch 无关）
"/api/auth/session"                 × 1   （浏览器同源会话）
```

与 CLI 一致：模型调用只走 `model-call` 和 `turn-ticket`。

---

## 3. `"desktop"` 的真实用途：本地运行存活判断

【观测】`main.js` `features/perchTerminal/runtime/runLiveness.ts`：

```javascript
function parseRunOwner(value) {
  ...
  const surface = v.surface === "desktop" || v.surface === "cli" ? v.surface : "other";
  return { instanceId, surface, deviceKey, pid };
}

function resolveHost() {
  if (installedHost) return installedHost;
  if (isDesktopRenderer()) {
    installedHost = { surface: "desktop", deviceKey: desktopDeviceKey() };
    return installedHost;
  }
  return { surface: "other", deviceKey: null };
}

function isDesktopRenderer() {
  return typeof window !== "undefined" && Boolean(window.perchDesktop);
}
```

`"desktop"` 的全部 4 次出现都在这里，用于判断"上一个运行本 turn 的进程是否还活着"。`deviceKey` 来自 `localStorage`（`dev_<uuid>`），**仅用于本地比对，不上网**。

`preload.js` 在末尾执行 `contextBridge.exposeInMainWorld("perchDesktop", api)`（【观测】），这就是 `isDesktopRenderer()` 的依据。

---

## 4. 桌面端没有客户端侧的身份证明

【观测】定向搜索下列关键词，全部零命中：

```
appSignature  deviceToken  attestation  AppAttest  desktopToken
"x-perch-desktop-*"  "x-perch-device-*"  "x-desktop-*"
```

`x-perch-*` 系列只有 6 个，与 CLI 完全相同：

```
x-perch-call-delta      x-perch-turn-ticket     x-perch-locale
x-perch-call-held-tools x-perch-call-held-length x-perch-call-held-digest
```

**结论【推断】**：Perch 无法仅凭请求内容区分桌面端与 CLI。因此 `perch_surface_required` 的判定依据不在这些字段里。

---

## 5. 唯一与 CLI 不同的地方：认证载体

【观测】桌面端在浏览器上下文取会话：

```javascript
// lib/auth/browserSession.ts
async function syncBrowserAuthSession() {
  if (typeof window === "undefined") return null;
  const response = await fetch("/api/auth/session", SESSION_FETCH_OPTIONS);
  if (!response.ok) return null;
  const body = await response.json();
  return body.session ?? null;
}

SESSION_FETCH_OPTIONS = { credentials: "same-origin", cache: "no-store" };
```

即**同源 cookie 会话**，而非纯 Bearer。桌面端仍是 Electron 渲染进程，请求带 `Origin`/`Referer`/cookie 体系。

【推断】这是唯一可能被服务端用于区分"官方客户端"与"第三方 HTTP 调用"的客户端侧差异，但**其具体权重与服务端逻辑未知**（服务端代码不在产物内）。

---

## 6. 封禁判定：只有一处会明说，且需要写操作

### 6.1 三个接口对同一被封账号的返回

我方实测（同一 token、同一账号）：

| 接口 | 方法 | 返回 | 是否暴露封禁 |
|---|---|---|---|
| `/api/perchai/account` | GET | **200** | **否** |
| `/api/perch-terminal/turn-ticket` | POST | 401 `not_authenticated` | 否（只说未认证） |
| `/api/perch-terminal/model-call` | POST | 403 `not_authorized` | 否（只说未授权） |
| `/api/perch-terminal/cli-plan` | **GET** | 200 `{"ok":true,"planRequired":true}` | **否** |
| `/api/perch-terminal/cli-plan` | **POST** | 403 `{"errorCode":"banned", …}` | **是** |

**这是本项目此前判断失误的直接原因**：只读探测给出假阴性。账号检测若只看 `account` 接口，会把整池已封账号报成"正常"。

### 6.2 选择套餐请求格式

【观测】`main.js`：

```javascript
// GET  —— 读取闸门状态
fetch(`${appUrl}/api/perch-terminal/cli-plan`, {
  method: "GET",
  headers: { Accept: "application/json", Authorization: `Bearer ${accessToken}` }
});

// POST —— 选择 Starter
fetch(`${appUrl}/api/perch-terminal/cli-plan`, {
  method: "POST",
  headers: { "Content-Type": "application/json", Authorization: `Bearer ${accessToken}` },
  body: JSON.stringify({ plan: "starter", noticeVersion: <来自 GET 的 starter.noticeVersion> })
});
```

`noticeVersion` 必须回传 GET 得到的值（实测为 `"2026-09-13"`），否则服务端返回 `notice_version_mismatch`。

【观测】`main.js` 中该端点的错误码白名单：

```javascript
["not_authenticated", "invalid_request", "notice_version_mismatch",
 "banned", "oauth_required", "capacity_reached", "select_failed", "unavailable"]
```

`banned` 在这个白名单里，是产物内唯一一处把封禁当作独立语义处理的接口。

### 6.3 服务端返回的两种封禁理由

我方对 46 个账号逐个 POST 实测，**无一例外全部封禁**：

```
403 {"ok":false,
     "error":"This account was banned for using Perch through another app or tool, which the terms do not allow.",
     "errorCode":"banned"}                                    ← 22 个

403 {"ok":false,
     "error":"This account was banned for using multiple accounts on the free tier, which the terms do not allow.",
     "errorCode":"banned"}                                    ← 24 个
```

第一种是**第三方工具**，第二种是**免费额度多账号**。

### 6.4 与模型调用的错误码区分

【观测】`model-call` 的 `perch_surface_required` 文案：

```
"Your plan includes Perch-hosted models for use in Perch AI Web, Desktop, and CLI only.
 Direct API access is not included.
 Repeated direct-access attempts may result in account suspension."
```

```
403 perch_surface_required
  limit: { errorCode, limitKind, resetAt: null, plan: "starter" }
  helpUrl: "https://perchai.app/pricing#hosted-model-access"
```

**这不是封禁**，是"该请求不算来自允许的产品入口"。它出现在尚未被封的账号上（我方新建测试账号即为此状态：能领票、能读套餐，但模型调用被拒）。

---

## 7. 时间线（实测）

```
23:32  5h 窗口 $1.2971 / $1.30 打满（99.78%）
00:12  RPC 返回中的 roostRolling（5h/7d 窗口数据）消失
00:23  plan / entitlements 在 Account 接口中变为空
01:10  cli-plan GET 全部返回 planRequired=true
01:41  cli-plan POST 返回 banned；46/46 全部封禁
```

【推断】先用尽短期配额，随后授权被收回，最终整池标记封禁。但**服务端判定顺序无法从客户端证据确定**。

---

## 8. 为什么"伪装"不可行

| 尝试方向 | 是否可行 | 依据 |
|---|---|---|
| 伪造 `clientSurface: "desktop"` | **无效** | 桌面端从不发该值，服务端也无对应语义【观测】 |
| 伪造桌面专属 header | **无对象** | 产物内不存在此类 header【观测】 |
| 伪造 UA 版本号 | **无效** | 桌面端 UA 与 CLI 同为 `perchai-cli/<ver>`【观测】 |
| 携带 cookie 会话 | **部分可行但不足** | 需真实浏览器上下文；且不改判定权重【推断】 |
| 伪造 TLS/JA3 | **无证据有效** | 无指纹差异被服务端引用的证据【未知】 |
| 换 IP | **无证据有效** | 封禁理由是行为/多账号，非地理【观测】 |
| 重新 GitHub 授权 | **无效** | 封禁写死在账号上，重授权后 POST 仍返回 `banned`（实测） |

**根本原因**：桌面端在协议层没有"我用的是官方客户端"的可验证声明。判定只能来自服务端对行为、账号拓扑与网络特征的观察。客户端侧无从模仿。

---

## 9. 覆盖边界

【观测】已完整读取：

- `app.asar` 索引（78,458 条目）
- `dist-electron/main.js` 全文（5.3 MB）
- `dist-electron/preload.js` 全文
- `package.json`
- ASAR unpacked 完整性检查

【未知】：

- 服务端判定逻辑、权重、封禁阈值——不在产物内
- 渲染进程 bundle（asar 内无独立 renderer chunk，业务在主进程；若有远程加载的 web 内容未纳入）
- `node_modules` 内 828 个第三方包的逐行审计（仅按关键词定向检索）
- 其余平台产物（Windows/Linux）未比对，可能含平台差异
- 官方 `perchai-cli@2.4.120` 与桌面端共享的 `turnTicketClient`/`modelProxyTransportHeaders` 模块已比对一致，但未做全量逐行 diff

---

## 10. 可复现步骤

```bash
# 1. 获取官方产物
curl -L -o perch-mac-arm64.dmg \
  https://download.perchai.app/desktop/latest/mac-arm64.dmg

# 2. 只读挂载
hdiutil attach -readonly -nobrowse -mountpoint /tmp/perchdmg perch-mac-arm64.dmg

# 3. 提取 asar（按索引偏移，勿整包解压）
#    header: 16 字节，[4:8] 为 header 长度（本产物 20,502,160 字节）
#    JSON 后剩余字节需 rstrip 空字节后再 raw_decode
#    数据区起点 = 16 + header_len，条目 offset 相对该起点

# 4. 定向检索
grep -n 'clientSurface'           dist-electron/main.js
grep -c 'clientSurface: "desktop"' dist-electron/main.js   # → 0
grep -n 'perch_surface_required'  dist-electron/main.js
```

---

## 11. 建议

1. **停止对 Perch 的伪装尝试。** 客户端侧已确认无可模仿对象；继续尝试只会扩大封禁面。
2. **46 个账号已全部标记封禁并停用**（`metadata.json` 的 `pool.accountStates`，规则 `perch-banned`）。
3. **账号检测已修复**：新增 `internal/perch/plan.go`，`applyCheck` 现在会读套餐闸门，`banned` 与 `no-plan` 不再被误报为正常。
4. **合规路径**：向 Perch 询问是否有官方 API 方案；否则将流量转向已有的 River / Ollama / Modal / OpenDesign 池。

---

## 附：关键位置索引

| 内容 | 位置 |
|---|---|
| 模型调用 header/envelope | `main.js` `sendModelCallViaServer` |
| 票 header 常量 | `main.js` `turnTicketClient.ts`，`TURN_TICKET_HEADER_NAME` |
| UA / locale / auth header | `main.js` `modelProxyTransportHeaders.ts` |
| `"desktop"` surface（本地） | `main.js` `runLiveness.ts`，`resolveHost` / `isDesktopRenderer` |
| 浏览器会话 | `main.js` `lib/auth/browserSession.ts` |
| 套餐闸门 GET/POST | `main.js`，路径常量 `/api/perch-terminal/cli-plan` |
| 封禁错误码白名单 | `main.js` `_2n = ["not_authenticated", …, "banned", …]` |
| preload 桥 | `preload.js` 末行 `contextBridge.exposeInMainWorld("perchDesktop", api)` |
