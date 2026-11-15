# Telnyx 注册机（telnyx.com）

**位置**：控制台顶部 `Telnyx` 标签（`pool='telnyx'`），侧栏「Telnyx 注册机」与「Telnyx 注册设置」。
当前只实现注册机，没有推理号池。

**代码**：
- `internal/telnyxreg` — HTTP 流程（registrar / captcharun / mailbox / resolver / egress）
- `internal/telnyxsignupstore` — 存储
- `internal/server/telnyx_registration.go` — runner + API
- `internal/server/web/telnyx.js` — 前端

从 Outlook 邮箱自动完成 Telnyx 账号注册、邮件验证、Portal 会话建立、创建 API Key，并读回试用额度。

**注册后每个账号自带 $25 promo credit**（`pretrial` 等级，30 天窗口），API Key 可直接调 `/v2/*`。

## 一、抓包实录（2026-11-16，真实浏览器 + MITM）

浏览器手动注册一次，CDP + MITM 双通道全程抓包（`_cap/cdp.jsonl`、`_cap/capture.jsonl`）。

| 步骤 | 请求 | 说明 |
|---|---|---|
| 1 | `POST https://telnyx.com/api/validate-email` | body `{"email":...}` → `{"success":true,"data":{"isFreemail":true}}` |
| 2 | Turnstile 求解 | sitekey `0x4AAAAAABkzL3BycQIQz_Ti`，**`data-action="signup"`** |
| 3 | `POST https://api.telnyx.com/v2/unified_signup` | 见下方 body，→ `200 {"success":true,"message":"A one-time sign-in link has been sent..."}` |
| 4 | 邮件 | 主题 `Your Single Use Telnyx Portal sign-in link`，发件人 `portal@telnyx.com` |
| 5 | `POST https://api.telnyx.com/v2/ai_dev_event_sessions` | body `{"portal_redirect_token":"<uuid>","email":"..."}` → `credentials.api_v2_token` |
| 6 | `GET /v2/user_profile` · `/v2/my_account_level` | `Authorization: Bearer <api_v2_token>` |
| 7 | `POST https://api.telnyx.com/v2/api_keys` | body `{"tags":["new"]}` → `201`，**明文 api_key 只在这一响应里** |
| 8 | `GET https://api.telnyx.com/v2/balance/promo-credits` | `remaining_credits: "25"` |

`unified_signup` 请求体：

```json
{
  "email": "addr@outlook.com",
  "terms_of_service": true,
  "terms_and_conditions_url": "https://telnyx.com/legal/terms-and-conditions-of-service/terms-and-conditions-of-service-2023-09-26",
  "privacy_policy_url": "https://telnyx.com/legal/privacy-policy/privacy-policy-2023-11-03",
  "subscription_opt_in": true,
  "cf-turnstile-response": "1.xxxx..."
}
```

邮件链接格式：

```
https://portal.telnyx.com/#/freemium-redirect?token=<uuid>&email=<urlencoded-addr>
```

## 一之二、管理台用法

1. 顶部切到 **Telnyx** 标签 → 侧栏「Telnyx 注册设置」。
2. 填 **打码令牌**（captcha.run）。留空保存表示沿用已保存的，令牌不回显，只显示 `cdd2…692e` 这种掩码。
3. 确认 **siteKey** `0x4AAAAAABkzL3BycQIQz_Ti` 与 **data-action** `signup`。改版时从 `/sign-up` 页的 `.cf-turnstile` 元素同步。
4. 填 **代理池**（每行一个）。建议住宅 IP；一个账号绑定一个代理并写入该行。
5. 回到「Telnyx 注册机」，粘贴账号，点「导入」→「批量注册」。

导入支持两种行：

```
邮箱----邮箱密码----<clientId UUID>----<refreshToken>     # 自动换取件地址
邮箱----邮箱密码----<取件token>                            # #s= 后面那串，直接用
```

进度条会经过 `validate → captcha → signup → waiting → session → key → credits`；「等待验证邮件」是真的会停住的状态（前端每 3 秒轮询）。

操作按钮：`注册/重试`（单行）、`批量注册`（所有可重试行）、`复制 Key`（显式取明文）、`导出已完成`、`清理已完成`、`删除`。

### 后端接口

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/telnyx/candidates` | 行列表 + 设置（Key 只给前缀） |
| POST | `/api/telnyx/candidates/bulk` | 批量导入 |
| POST | `/api/telnyx/candidates/bulk-register` | 批量注册所有可重试行 |
| POST | `/api/telnyx/candidates/{id}/register` | 单行注册 |
| POST | `/api/telnyx/candidates/{id}/retry` | 单行重试 |
| DELETE | `/api/telnyx/candidates/{id}` | 删除 |
| POST | `/api/telnyx/candidates/clear-finished` | 清理 Keyed/Failed/Cancelled |
| POST | `/api/telnyx/candidates/import-completed` | 导入已完成账号（email + key + credits） |
| GET | `/api/telnyx/candidates/{id}/key` | 取完整 Key（增权操作，不在列表里） |
| GET | `/api/telnyx/status` | 队列计数 |
| GET | `/api/telnyx/logs` | 最近 400 条运行日志 |
| GET/PUT | `/api/telnyx/settings` | 设置读写（令牌只回掩码） |
| GET | `/api/telnyx/export` | `email<TAB>key<TAB>credits` 纯文本 |

存储：`<data>/telnyx-signup.json`，0600，明文（与 x50 同策略）。

## 二、关键结论（全部实测）

### 1. Turnstile 的 `action` 是签名的一部分

这是最容易踩的坑。页面 widget：

```html
<div class="cf-turnstile" data-sitekey="0x4AAAAAABkzL3BycQIQz_Ti"
     data-action="signup" data-theme="light">
```

**不带 `action` 的 token 必然被拒**，响应：

```json
{"errors":[{"detail":"That email is not valid, or your browser could not be authenticated. Please try again."}]}
```

带上 `action: "signup"` 后立刻 `200`。`siteReferer` 也要给对（`https://telnyx.com/sign-up`）。

### 2. 不需要浏览器

纯 HTTP 即可，三项都不影响：

- **UA 不必匹配**：打码平台返回 Windows Chrome UA，我们用 Mac UA 提交照样 200
- **不需要 cookies / 浏览器会话**
- **不需要 TLS 指纹伪装**（但 ClientHello 保持浏览器形状更稳）

### 3. 出口 IP 必须全程一致

`portal_redirect_token` 与提交注册时的出口绑定。换 IP 兑换会失败。
每个账号全程锁定同一个代理会话（`-t-30` 这种 sticky 后缀），不要用轮换。

### 4. 邮件 token 单次有效 + 短 TTL

- 重放同一个 token → `401 {"detail":"You must be authenticated..."}`
- 邮件发出后数小时即失效 → `401 {"detail":"Your sign-in link has expired..."}`
- **必须在收到邮件后立刻兑换**，注册机的 `MailWait` 轮询要快

### 5. 免费账号只能有 1 个 API Key

```
POST /v2/api_keys → 403 code 10039
"You have exceeded the limit on API keys. You must have less than 1 keys.
 You currently have 1 keys."
```

已有 Key 的账号重复建 Key 会失败。注册机对此单独报错 `errKeyLimit`。

### 6. API Key 明文只在创建响应中

`POST /v2/api_keys` 的 `data.api_key` 是完整明文（`KEY01..._xxxx`）。
之后 `GET /v2/api_keys` 返回 `"api_key":"<encrypted>"`，**无法再取回**。
必须建 Key 时一次性落盘。

### 7. 同一封邮件里链接出现多次

纯文本部分、HTML 部分、以及带尾随 `]` 的引用会各出现一次。
取最新一封邮件、取最后一个干净链接（去掉 `])},.;`）。

### 8. 重发注册会作废旧 token

对同一邮箱重复 `unified_signup` 会发新邮件，**只有最新那封的 token 有效**。

## 三、邮箱接入（tool.yaoyiyaocoffee.cn）

有两条路，按手上数据的形状选。

### 3.1 MsaArtifacts 格式（推荐，最省事）

手上有 `email----密码----clientId----refreshToken` 这种微软 OAuth 导出时，一次
`mailboxes/resolve` 就能换出取件地址，**不需要事先去网页里点**：

```
POST /api/mailbox-tool/mailboxes/resolve
{"mailboxes":[{"localId":"<uuid v4>","email":"...","clientId":"...","refreshToken":"..."}],
 "includeSharePath": true}
→ {"data":{"results":[{"localId","ok":true,
      "mailbox":{...,"accessToken","refreshToken","state"},"sharePath":"/inbox#s=<token>"}]}}
```

三个坑，全部实测踩过：

1. **`includeSharePath` 必须是 true**，否则响应里根本没有收件地址。
2. **`localId` 必须是 UUID v4**。随便给个字符串会得到
   `{"error":{"code":"VALIDATION_ERROR","message":"输入内容无效 / Invalid request input"}}`。
3. **`refreshToken` 每次调用都会轮换**，响应里返回的是新的那个。旧值立刻失效：
   再拿旧 token 调一次会返回 `ok:false`。必须把新值写回存储。

`refreshToken` 里的 `!` 在 JSON 里要转义成 `\u0021`（Go 的 `encoding/json` 自动处理）。

### 3.2 直接给 shareToken

已经手动取过件、手上有 `#s=` 那串时，直接放第三段即可：

```
LilahMelendez1446@outlook.com----n2AjtHn87C----mDs2l5UJfmyQ0ixBKJX_O4U0KbSBG0R4NAil_UZs1-U
```

**注意**：某些导出里第三段是 `9e5f94bc-e8a4-4e73-b8be-63364c29d753` 这种 UUID，
那是**微软 OAuth 的 client id，所有行完全一样**，不是取件 token。
CLI 遇到 UUID 会当作 clientId 并去 resolve；若第四段缺失会直接报错，
不会白白消耗打码额度。

### 3.3 读信

```
POST /api/mailbox-tool/inbox/info   {"shareToken":"..."} → {"data":{"email":"..."}}
POST /api/mailbox-tool/inbox/read   {"shareToken":"..."}
     → {"data":{"email":"...","messages":[{id,sender,subject,receivedAt,text,html}]}}
```

实测 8 个邮箱一次性全部 resolve 成功，`state` 均为 `READY`。

## 四、代理（住宅 IP）

本机必须链式：`sing-box(127.0.0.1:10808)` → `proxy_bridge` → zooproxy 住宅出口。

```sh
python3 tools/proxy_bridge.py --listen 127.0.0.1:17230 --via 127.0.0.1:10808 \
  --upstream "socks5://USER-region-US-sid-XXXX-t-30:PASS@us-eu.zooproxy.com:5000"
```

实测 10 个 sid 出口各不相同：

| sid | 出口 IP |
|---|---|
| nzHP37YC | 173.30.238.248 |
| RzMaPZkV | 174.62.203.32 |
| swtNj1wi | 24.209.78.192 |
| X8KtJ3qC | 104.51.67.5 |
| eMvAVt9K | 82.40.103.5 |
| eaVDKLW7 | 72.83.144.249 |
| u5iwmSUD | 99.68.65.146 |
| ALsbJNkd | 47.234.212.115 |
| qEARWfuX | 51.146.217.35 |
| PUYq3ZP7 | 47.204.9.171 |

`proxies.txt` 每行写桥接地址（`http://127.0.0.1:17230`），注册机按账号轮询分配，一个账号一个代理。

## 五、打码（captcha.run）

```sh
curl -X POST https://api.captcha.run/v2/tasks \
 -H "Authorization: Bearer <TOKEN>" -H "Content-Type: application/json" \
 -d '{"captchaType":"Turnstile",
      "siteReferer":"https://telnyx.com/sign-up",
      "siteKey":"0x4AAAAAABkzL3BycQIQz_Ti",
      "data":{"action":"signup"}}'
# → {"taskId":"..."}
# GET /v2/tasks/<taskId> → status Working|Success|Fail
```

实测 4–10 秒出 token，单价 `$0.0294`。

## 六、用法

```sh
go build -o telnyxreg ./cmd/telnyxreg

# 先空跑，确认输入解析
./telnyxreg -mailfile mailboxes.txt -list
./telnyxreg -mailfile mailboxes.txt -dry

# 单账号
./telnyxreg -mail "addr@outlook.com----pw----SHARETOKEN" \
  -proxy http://127.0.0.1:17230 \
  -captcha-token $CAPTCHARUN_TOKEN

# 批量，4 并发，自动分配代理
./telnyxreg -mailfile mailboxes.txt -proxies proxies.txt -c 4 -out accounts.jsonl
```

| flag | 默认 | 含义 |
|---|---|---|
| `-mailfile` | – | `email----pw----shareToken` 每行一条 |
| `-mail` | – | 单条，或 `-` 读 stdin |
| `-proxies` | – | 代理列表文件 |
| `-proxy` | – | 单个代理 |
| `-c` | 1 | 并发数 |
| `-out` | `telnyx_accounts.jsonl` | 结果追加写（0600） |
| `-captcha-token` | env `CAPTCHARUN_TOKEN` | 打码令牌 |
| `-sitekey` | 内置 | Turnstile sitekey |
| `-action` | `signup` | Turnstile data-action |
| `-key-tags` | `new` | API Key tags |
| `-mail-wait` | 240s | 等邮件上限 |
| `-mail-poll` | 5s | 收件箱轮询间隔 |
| `-timeout` | 45s | 单请求超时 |
| `-optin` | false | `subscription_opt_in` |
| `-mailbase` | 内置 | 取件工具 base |
| `-resolve` | false | 只把 MsaArtifacts 换成取件地址，不注册 |
| `-list` / `-dry` | – | 只解析不发请求 |

先验证邮箱能不能批量 resolve（不花钱）：

```sh
./telnyxreg -mailfile msa.txt -resolve
```

输出每行 `email  shareToken`，同时会把轮换后的 refreshToken 打印出来，记得存。

## 七、输出

`accounts.jsonl` 每行一个 JSON：

```json
{"email":"...","proxy":"http://***@...","ok":true,
 "account":{"email":"...","session_token":"gAAAAAB...","api_key":"KEY01..._xxx",
            "key_id":"KEY01...","user_id":"...","account_level":"pretrial",
            "credits_usd":"25","egress_ip":"...","created_at":"..."},
 "elapsed":"38.2s","at":"..."}
```

失败：`{"ok":false,"error":"...","elapsed":"...","at":"..."}`

## 八、实测结果（2026-11-16）

**6/6 全部成功，每个 $25 额度，6 个 Key 实测均可调 `/v2/balance` 与 `/v2/phone_numbers`（HTTP 200）。**

| 邮箱 | API Key | 额度 |
|---|---|---|
| TheaJepson5145@outlook.com | `KEY01…<已脱敏>` | $25 |
| HugoRoman2199@outlook.com | `KEY01…<已脱敏>` | $25 |
| FelixMason1548@outlook.com | `KEY01…<已脱敏>` | $25 |
| FelixRubio4290@outlook.com | `KEY01…<已脱敏>` | $25 |
| LilahMelendez1446@outlook.com | `KEY01…<已脱敏>` | $25 |
| ClementineJolley9576@outlook.com | `KEY01…<已脱敏>` | $25 |

单账号端到端 30–53 秒。结果落盘在 `telnyx_accounts.jsonl`（0600）。

### 并发下的 Turnstile 抖动（已修复）

并发 2 跑 4 个账号时，3 个报：

```json
{"errors":[{"detail":"That email is not valid, or your browser could not be authenticated."}]}
```

串行同一账号立刻成功 → 不是代码问题，是打码平台在高并发下返回的 token
质量不稳。已加**换 token 重试**（最多 3 次，每次重新求解），重跑后 2/2 通过。
建议并发 `-c 2`，不要更高。


## 九、限制与风险

- **单账号 1 个 Key**：超出报 10039。若需轮换，先删旧 Key 再建。
- **$25 是 promo credit**，`pretrial` 等级，30 天窗口，部分接口会报 `10038 Feature not permitted`（如 `/v2/organization`）。
- **邮件 token 时效紧**，批量并发不宜过高；建议 `-c 2~4`，每账号配独立住宅 IP。
- **同 IP 高频注册会触发风控**，打码平台文档也提醒频率过高会封 IP。
- 账号为免费 `pretrial` 等级，功能受限；升级需付费信息。

## 十、管理台验收记录（2026-11-16）

### 已实测通过

| 项 | 结果 |
|---|---|
| 服务启动 + 路由注册 | `GET /api/telnyx/settings` 200 |
| 批量导入（两种格式） | 3 行全部识别：msa / share / msa 正确分流 |
| 重复邮箱去重 | 大小写不敏感，重复行 skipped |
| UUID 当 share token | 直接判为 clientId 并走 resolve |
| 设置保存 → 重读 | concurrency / mailTimeoutSec / keyTags / proxies 全部往返一致 |
| 打码令牌掩码 | 列表与设置只给 `cdd2…692e`，不回显明文 |
| 重启后持久化 | 行与设置都在，令牌仍为已保存状态 |
| 文件权限 | `telnyx-signup.json` 0600 |
| **真实端到端注册** | **SadieShepherd9941 全程走通：signup → 收信 → 换会话 → 建 Key → $25** |
| Key 实测可用 | `GET /v2/balance` 200 |
| 导出 | `SadieShepherd9941@outlook.com<TAB>KEY01A1E15340…<TAB>25` |
| 列表不漏 Key | 列表响应里搜不到完整 Key 明文 |
| 前端页面 | Telnyx 标签、侧栏两项、导入表单、状态徽章、表格渲染正常 |
| 设置页往返 | 改 concurrency=1 点保存，后端真的变成 1 |
| 全量测试 | `go test ./internal/...` 全绿（server 93s） |

### 实测中修掉的 bug

**1. 自死锁（最严重）**

`telnyxMgr()` 持 `telnyxMu` 时调用 `telnyxBaseCtx()`，后者又拿同一把锁 —— 第一个 `/api/telnyx/*` 请求就永久挂起，后续请求全部排队。现象是服务进程还在但所有接口超时。

修复：在加锁前先取 `telnyxBaseCtx()`。加了两个回归测试（`TestTelnyxManagerInitDoesNotDeadlock`、`TestTelnyxConcurrentAccess`）锁死这个顺序。

**2. 出口探测失败被静默吞掉**

`CheckEgress` 出错时不写日志，导致排查 401 时看不出是「链接过期」还是「IP 不一致」。现在会打 `[egress] probe failed: … (continuing)`。

**3. 缺 hashchange 监听**

直接改 URL hash 不会切页（只有首次加载读一次）。补上监听，并排除 `go()` 自己写 hash 造成的循环。这条对所有池都有益。

### 实测确认的上游约束

- **Portal 会话与提交注册时的出口 IP 绑定**。同一邮箱换个 IP 重新注册，邮件能收到、`ai_dev_event_sessions` 仍返回 `401 10009`。所以「一个账号一个固定代理」不是优化，是硬要求。
- **单账号 1 个 Key**，重跑必报 `10039`。注册机对已完成的行走 `continue`，不会重复建 Key。
- **mail token 一次性 + 数小时 TTL**，所以「等邮件」是真会卡住的状态，`MailTimeoutSec` 开太大只会让失败来得更晚。
