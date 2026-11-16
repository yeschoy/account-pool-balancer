# altapi 号池

altapi.ai（TokenPower，new-api 定制分支）的号池管理与注册机。结构对齐 Deploy 号池：
配置段 → 默认错误/重试规则 → 额度读取 → 注册候选存储 → 注册机子进程 → 概览。

## 与 Deploy 的关键差异

**altapi 能读出真实余额，Deploy 不能。** 这是本次接入最重要的一点：

| | Deploy | altapi |
|---|---|---|
| 认证 | 推理 Secret | 推理 Key（Bearer）+ **session cookie** |
| 余额 | 未接入，只能显示未知 | ✅ `quota / quota_per_unit`，与页面 Current balance 完全一致 |
| 注册跳转 | 需验证邮件点链接 | 无跳转，填码即完成 |
| 绑卡 | 有 | 无 |

余额读不到的情况只有一种：账号没绑定 session cookie。这时**只显示未知，绝不写 0** ——
0 会被上层理解成"这个号没额度了"，与"读不到"是完全不同的语义。

## 认证契约（实测 2026-11-15）

new-api 的会话是 **cookie**，不是 JWT：

```
POST /api/user/login?turnstile=
{"username":"x","password":"x"}
→ Set-Cookie: session=<gob>; HttpOnly; Secure; SameSite=Lax; Max-Age=2592000
```

之后每个 `/api/*` 请求都要带两个东西：

```
Cookie: session=<...>
New-Api-User: <uid>
```

**`New-Api-User` 必须是真实 uid**，这个坑踩过：

| 取值 | 结果 |
|---|---|
| 缺失 | 401 `未提供 New-Api-User` |
| `-1` | 401 `New-Api-User 与登录用户不匹配` |
| `241`（真实 uid） | 200 |

所以读余额前必须先知道 uid。注册机在登录响应里拿到 `data.id` 并落盘到候选的
`account_id`，额度查询直接用这个值。

## 余额公式（与页面逐项对应）

```
Current balance    = quota      / quota_per_unit
Consumption        = used_quota / quota_per_unit
Number of Requests = request_count
```

`quota_per_unit` 由站点 `/api/status` 给出，实测 **500000**；配置里的
`altapi.quotaPerUnit` 只是离线兜底，在线时站点值优先。

实测对照（uid=241，登录后只打了 2 次请求）：

```
quota=4999849  used_quota=151  request_count=2  unit=500000
→ balance=$9.9997  used=$0.0003   ✅ 与页面一致
```

新号 `quota=5000000` → **$10.00**，即注册赠送额度。

推理 Key **读不到**余额（`/api/user/self` 返回 `access token 无效`），只能读已花掉的钱：

```
GET /dashboard/billing/usage    Authorization: Bearer <key>
→ {"object":"list","total_usage":1.5796}     # 单位是美分，= $0.0158
```

注意 `/v1/dashboard/billing/subscription` 的 `hard_limit_usd` 是写死的占位值 `1e8`，
**不是余额**，不要拿它算额度。

## 配置

```json
{
  "altapi": {
    "baseUrl": "https://altapi.ai/v1",
    "quotaPerUnit": 500000,
    "modelSyncSec": 600,
    "routing": { "strategy": "least-connections" },
    "quota": { "intervalSec": 600 }
  }
}
```

`baseUrl` 必须以 `/v1` 结尾（控制面 `/api/*` 在同源，由 `ALTAPIOrigin()` 推导）。
`quotaPerUnit` 必填区间 1..1e9。

## 号池账号

一个 altapi 账号需要两把凭据：

| 字段 | 内容 | 用途 |
|---|---|---|
| `runtimeKey` | 48 字符 API Key（`sk-` 可加可不加） | `/v1/*` 推理 |
| `controlKey` | `session` cookie 值 | `/api/*` 读余额 |

`controlKey` 留空也能入池干活，只是余额显示未知、只能看到已用金额。

## 错误规则

altapi 有一半拒绝发生在 **HTTP 200** 上，所以规则主要靠正文匹配，不能只看状态码。

| 上游结果 | 处理 | 说明 |
|---|---|---|
| `Invalid token` / `access token 无效` | 硬隔离 | 实测 HTTP 200 + success:false；Key 自身坏了 |
| 该用户已被封禁 | 硬隔离 | 需人工 |
| `MODEL_NOT_SUPPORT` + `no deployment node` | 本号×模型冷却 30m | 列表里有这模型不代表能调（DeepSeek-V3.2 / GLM-4.6 实测） |
| 模型 ID 不存在 | ignore + 不换号 | 换号必然再失败 |
| `额度不足` / `insufficient_user_quota` | 账号冷却 + 立即刷新余额 | 连续命中翻倍（10m→6h），成功清零 |
| 403 分组无权限 | 本号×模型冷却 5m | 换号继续，不判封禁 |
| 429 | 账号冷却，遵守 Retry-After | 同账号多 Key 共享限额 |
| 429/403 + `CF-Ray` | 短冷却 30s | Cloudflare 同 IP 突发，非上游限流 |
| `上游负载已饱和` | 账号冷却 5s | altapi 自己的渠道故障 |
| 400 参数错误 | ignore | 不扣健康分、不换号 |
| 500/502/503/504 | 短冷却 | 仅在未输出时可重试 |

一个铁律：**余额耗尽冷却的是整个账号**（一个钱包付所有模型），但 config 仍拒绝
`funding` scope —— 该 scope 要求"已证明的共享计费源"，写成 account 更保守，避免以后
有人手改配置时误把整号冷却。

## 重试规则

| 决策 | 条件 | 理由 |
|---|---|---|
| stop | Key 失效 | 换号只会拿同一把坏 Key 再撞一次 |
| stop | 400/413/422 参数错误 | 换号必然再失败 |
| stop | 模型 ID 不存在 | 不遍历号池 |
| stop | 429 | 多 Key 共享限额 + 同 IP 共享 CF 限速，换号绕不过 |

## 注册机

`tools/altapi_registrar.py`，由 balancer 以后台子进程方式调用：

```bash
python3 tools/altapi_registrar.py --job-file /tmp/job.json
```

job 格式：

```json
{
  "settings": {
    "base_url": "https://altapi.ai",
    "min_interval_sec": 32,
    "verify_timeout_sec": 180,
    "token_group": "vip",
    "token_name": "newapi",
    "proxy_pool": ["socks5h://user:pass@host:1080"]
  },
  "items": [
    {"index": 0, "email": "a@b.c",
     "mailbox_url": "https://icloud.mida.vip/messages/...?token=..."}
  ]
}
```

**stdout 每行一个 JSON 事件**，后端不解析任何人类可读文本：

```json
{"type":"log","index":0,"email":"...","message":"..."}
{"type":"item_update","index":0,"status":"sent_code","detail":"..."}
{"type":"item_done","index":0,"status":"key_created","api_key":"...","session_cookie":"...","balance_usd":10.0}
```

失败时 `item_done` 带 `status":"failed"` + `error`。子进程异常退出时，仍是
`running`/`sent_code` 的候选会被兜底标成 `failed`，方便重跑。

也可独立 CLI：

```bash
python3 tools/altapi_registrar.py --probe            # 站点状态
python3 tools/altapi_registrar.py --email a@b.c --mailbox 'https://...' --proxy 'socks5h://...'
```

### 注册链路（5 步）

1. `GET /api/status` — 确认 `turnstile_check=false`
2. `GET /api/verification?email=&turnstile=` — 发码（Resend，**按邮箱 30s 限流**）
3. `GET <mida>/api/messages?alias_email=&token=&range=latest` — 取码
4. `POST /api/user/register?turnstile=` — 注册
5. `POST /api/user/login` → `GET /api/user/self` → `POST /api/token/` → `POST /api/token/{id}/key`

### 两个必踩的坑

**验证码是 6 位十六进制，不是 6 位数字。** 实测邮件正文：

```
Your verification code is: 48890d
```

任何 `\d{6}` 的正则都会 100% 失败。

**取明文 Key 必须 POST。**

```
POST /api/token/{id}/key   → {"data":{"key":"1Agx…HAHF"}}
GET  /api/token/{id}/key   → {"error":{"message":"Invalid URL (GET /api/token/212/key)"}}
```

返回 48 字符裸值，直接作 Bearer 用；`sk-` 前缀加不加站点都接受。

## API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/altapi/candidates` | 候选列表 + 概览聚合 + 设置 |
| POST | `/api/altapi/candidates` | 加单个（email / mailboxUrl / proxyUrl） |
| POST | `/api/altapi/candidates/bulk` | 批量，`email----mailbox` 每行 |
| PATCH | `/api/altapi/candidates/{id}` | 改邮箱/接码/代理/session/key |
| DELETE | `/api/altapi/candidates/{id}` | 删除 |
| POST | `/api/altapi/candidates/{id}/register` | 注册单个 |
| POST | `/api/altapi/candidates/bulk-register` | 批量，`{"all":true}` 或 `{"ids":[]}` |
| POST | `/api/altapi/candidates/{id}/cancel` | 取消 |
| POST | `/api/altapi/candidates/{id}/import` | 入池为 config.Account |
| POST | `/api/altapi/candidates/import-all` | 全部入池 |
| POST | `/api/altapi/candidates/{id}/quota` | 查单个余额 |
| POST | `/api/altapi/quota-refresh` | 批量刷新余额 |
| POST | `/api/altapi/proxy-test` | 探测代理出口 IP |
| GET/PUT | `/api/altapi/settings` | 注册机参数 |
| GET | `/api/altapi/logs` | 运行日志 |
| GET | `/api/altapi/status` | **号池概览** |

`/api/altapi/status` 返回可调度账号、每个号的真实余额、余额来源（session/key/none）、
余额未知计数、候选状态分布、规则条数。余额合计只累加真正读到的号。

## 验证

```bash
go build ./...
go test ./internal/config/ ./internal/rules/ ./internal/quota/ \
        ./internal/altapisignupstore/ ./internal/server/
```

真实余额读取（需真 cookie，默认跳过）：

```bash
ALTAPI_SESSION='<session cookie>' ALTAPI_UID=241 \
  go test ./internal/server/ -run TestALTAPILiveConsoleBalance -v
# → balance=$9.9997 used=$0.0003 req=2 unit=500000
```

注册机自测：

```bash
python3 tools/altapi_registrar.py --probe
# → turnstile_check=false, email_verification=true, quota_per_unit=500000
```

## 接进代理时踩到的三个本地坑

这三处不在 altapi 站点侧，是 balancer 自身的假设被 new-api 打破：

1. **`RuntimeBase` 缺分支** — 没写 altapi 时会落到 OpenDesign 的默认基址，
   请求被发到错误主机，表现为 `upstream_account_auth_failed`。
2. **`errorStatus` 认不出 `Invalid token`** — 代理要把"HTTP 200 但内容是错误信封"
   归一成一个状态码喂给规则引擎。原有兜底只匹配 `invalid_api_key` /
   `authentication_error`，`Invalid token` 落空返回 0，规则永远匹配不上，
   于是坏 Key 会被当成成功请求静默放行。已补 `invalid token` / `new_api_error` → 401。
3. **`accountInput` 缺 `userId`** — 无法通过 API 回填 uid，额度读取就卡在 401。

修完之后的行为已验证：

```
坏 Key 账号存在时    → {"error":{"code":"pool_unavailable"}}   # 硬隔离生效
正常账号请求         → claude-sonnet-5 真实返回               # 代理通
```

## 已知边界

- 站点若重开 `turnstile_check`，注册机直接报错退出（不静默降级）。
- Resend 拒绝 `example.com` 等测试域，必须真实可达邮箱。
- 同 IP 高频会触发 Cloudflare 空响应 429；注册机与额度查询都有间隔控制。
- 批量额度查询每号间隔 1.2s，否则登录连击会被 CF 拦（实测第一轮 10 个号挂了 6 个）。
- 价格表只是估算，上游账单才是权威。
- `group`（default/vip）的模型覆盖不同，同一个模型可能一组的号能调另一组不能。

## 会话粘性（本地代码修复）

发现并修复三个接线问题：

1. `Config.ForProvider("altapi")` 原先没有 ALTAPI 分支，实际推理与设置读取走的是全局路由；保存 ALTAPI 设置还会连带改写全局路由、额度、价格、日志和 OpenCode 设置。现在独立读取并保存 `altapi.routing/quota/pricing`，不再覆盖其他池。
2. 通用身份识别把正文 `prompt_cache_key` 放在真实会话前面，导致缓存键变化时同一会话丢绑定、相同缓存键的不同会话挤到一起。ALTAPI 现在先读取真实会话；没有真实会话时才以正文缓存键、`X-Prompt-Cache-Key` / `Prompt-Cache-Key`、消息 HMAC 依次兜底。原有自定义头、父会话、Claude `metadata.user_id` 等识别规则保留。
3. 原先指纹只有分组作用域；现在纳入下游 Key ID。同一分组里不同调用方即使传相同会话 ID，也不会共用绑定；这不表示它们绝不能被调度到同一个账号。

独立路由已接入完整校验，包括忙时策略、自定义会话头和 TTL。显式绑定命中时滑动续期；到期后重新选号。`sticky=false` 仍允许识别会话，但不建立/命中普通粘性绑定。

### 忙时和恢复边界

- `reject`：忙时按配置等待，超出等待预算返回 429，不跳到其他账号。
- `overflow` / `wait-overflow`：允许临时分流，但不把临时账号覆盖为原会话的新 owner。
- owner 冷却/禁用等不可用时，普通会话可以重新选号；一次请求是否重试仍由既有错误/重试规则和已输出边界决定，不因本次修复放宽。
- 普通会话绑定仍是有 TTL、容量限制的内存状态，服务重启或 LRU 淘汰后需要重建。本次没有新增持久化，也没有改 busy 策略的默认值。
- 这只是 Balancer 选号粘性，不是 ALTAPI 上游内部渠道粘性或模型缓存命中的保证；没有真实 session 时的缓存键/消息指纹也不能证明是同一对话。ALTAPI 的服务端 Responses 续接能力未在本轮验收，不能当成严格续接保障。
- 旧版本保存 ALTAPI 设置可能已经改动全局值，本次不会自动猜测、回滚历史配置；如需修正线上值，应单独核对并授权。

回归覆盖：独立默认/参数校验、设置 GET/PUT/磁盘重载与跨池隔离、非法保存回滚、会话优先于缓存、缓存头兜底、父子会话、下游 Key 隔离、滑动 TTL/到期、三种忙时策略、可恢复失败换号及后续绑定、并发首请求唯一 owner。测试使用本地 Mock，不代表已部署或已完成真实上游验收。

```bash
go test ./internal/config ./internal/server -run 'TestALTAPI(IndependentRouting|ValidatesIndependentRouting|Sticky)' -count=1
```

## 跨语言规则一致性

`normalize_uid` 与 `session_value` 在 Go（`internal/newapiconsole`）和 Python
（`tools/altapi_registrar.py`）各有一份实现。这是**有意的重复**：注册机是独立子进程，
部署时直接 `python3 altapi_registrar.py` 跑，import 不到 Go 包。

重复的风险是漂移——只改一边，另一边行为分叉，而且报错看起来像别的问题
（`New-Api-User` 那个坑就是这么来的）。用一张共享用例表锁住：

```
internal/newapiconsole/cases.json      ← 唯一真源，含每条的原因说明
  ├─ cases_test.go (go:embed)          ← Go 侧断言
  └─ tools/test_altapi_registrar.py    ← Python 侧断言
```

```bash
go test ./internal/newapiconsole/            # Go 侧
python3 tools/test_altapi_registrar.py       # Python 侧
```

已验证漂移会被抓到：把 Python 侧改成 `strip()` 不拒绝空白 → 立刻报 3 条失败
（`'241\n'` / `' 241'` / `'241 '`）；把 Go 侧改成接受 `-1` → `TestSessionValueCases` 失败。
恢复后两边都通过。

`balance_usd` 只在表里由 Go 校验：余额换算只发生在 Go 端，Python 没有对应实现，
跳过不是遗漏。

## 前端控制台（2026-11-15 补齐）

之前 altapi 只做了后端（子进程注册机 + 号池读余额），控制台没有入口：`POOLS` 里
没有 `altapi`，`index.html` 没有标签也没有侧栏，`app.js`/`index.html` 里
`altapi` 出现次数是 0。所以顶部只有 12 个标签，看不到 ALTapi。

本次补齐前端，接线分布：

```
internal/server/web/altapi.js       新增，~600 行，与 x50.js / deploy.js 同构
  ├─ 概览（altoverview）   GET /api/altapi/status
  ├─ 注册机（altreg）      /candidates /logs /candidates/bulk /bulk-register
  │                        /{id}/register /{id}/cancel /{id}/import
  │                        /{id}/quota /import-all /quota-refresh
  └─ 设置（altsettings）   GET|PUT /api/altapi/settings, POST /proxy-test

internal/server/web/app.js
  POOLS             += 'altapi'                 （标签顺序：… 50X AI, ALTapi）
  POOL_LABEL         altapi: 'ALTapi'
  POOL_GROUP         altapi: 'altapi'
  isALTapi()         pool === 'altapi'
  ALTAPI_PAGES       ['altoverview','altreg','altsettings']
  pages              三个 loader
  go()               跨池深链纠正 + init + 轮询启停
  renderPoolTabs()   own/mine 归属 + data-pool-nav 显隐

internal/server/web/index.html
  顶部标签           <button data-pool="altapi">ALTapi</button>
  侧栏               nav-sep + 三个 data-page/data-pool-nav="altapi"
  页面区块           三个 <section id="page-alt*">
  脚本               <script src="/assets/altapi.js">
```

### 一并修的后端缺陷

`POST /api/altapi/candidates/bulk` 原来只解码 `lines`，前端传的 `proxies` 被静默丢弃：
页面填了代理池、点导入、提示成功，实际每个候选仍是直连。现在按导入顺序轮转绑定，
空代理不清空已有绑定（改绑走 PATCH）。用 `TestALTAPIBulkImportBindsProxies` 锁住。

### 字段名契约

后端 `Summary` 用 camelCase（`byStatus` / `withKey` / `balanceUnknown`），
不是 snake_case。前端第一版读成了 `by_status`，会让所有计数恒为 0 而页面看起来正常。
`altapi_web_test.js` 现在直接读 `store.go` 的 `json:` tag 做双向断言。

### 验收记录

| 项 | 状态 | 证据 |
|---|---|---|
| 标签渲染 13 个，末位 ALTapi | ✅ 已验证 | 真实 `index.html` grep 出 13 个 `role="tab"` |
| 侧栏三页显隐归属 | ✅ 已验证 | `pool_navigation_web_test.js`（13 池遍历） |
| 三页 loader 隔离 | ✅ 已验证 | 同上，`loadALTStatus/Registrations/Settings` 只在 altapi 下触发 |
| `/assets/altapi.js` 可访问 | ✅ 已验证 | HTTP 200，21173 字节 |
| 四个 GET 接口 | ✅ 已验证 | `status`/`candidates`/`settings`/`logs` 全 200 |
| 批量导入绑代理 | ✅ 已验证 | 实测 p1→p1 代理、p2→p2 代理；Go 测试锁住轮转 |
| 设置保存往返 | ✅ 已验证 | PUT 后 GET 返回 `workers=3 min_interval=45 token_name=mykey aff=ABC123 proxies=[1]` |
| 重启后设置仍在 | ✅ 已验证 | 换 data dir 重启，设置与候选全部保留 |
| 前端单测 | ✅ 14/14 过 | 含新增 `altapi_web_test.js` |
| Go 全量 | ✅ 全过 | `go test ./...` 无 FAIL |

### 前端边界

- 余额 `null` 渲染成「未知」，绝不落成 `$0.00`（没有 session 时余额确实未知）。
- API Key 列表只显示前 8 位；完整值只从行内数据读，需显式点「复制 Key」。
- 注册逻辑仍由 `tools/altapi_registrar.py` 子进程执行，前端不重写注册流程。

### 未做

- 未部署（按约定部署需单独走预检 → 构建 → 备份切换 → 验收，且需你确认）。
- 未提交 git。

## 指纹泄露面分析（2026-11-16 实测）

用「本地回显服务器 + 真实 Chrome 157」建立基准，逐维度对比注册机的实际出网特征。
**结论按危害排序**。

### 1. TLS 指纹：唯一的决定性因素（已修）

同一代理出口、同一时刻的对照实验：

| 客户端 | 结果 |
|---|---|
| Python `urllib` + 完整 Chrome UA/`sec-ch-ua` | **连接被 Cloudflare 直接断开** |
| `curl_cffi` `impersonate=chrome` | **HTTP/3 200** |

决定性证据是「只改 UA 不改 TLS」那组：UA 和 client hints 全部伪造正确，
仍然被断。说明判据在 TLS 层，`User-Agent` 完全不参与。

原因是 Python 用系统 LibreSSL：ClientHello 的 cipher 顺序、支持曲线、扩展集合、
GREASE 全和 Chrome 的 BoringSSL 不同——这就是 JA3/JA4 的原料，是站点第一眼看到的东西。

修复：`Transport` 默认走 `curl_cffi`；`urllib` 只在没装库时降级，且
`tls_is_browserlike=False` 可被上层查询，不再静默降级。

**这同时暴露了一个部署缺陷**：Dockerfile 里 altapi 的 venv 只装了 PySocks，
注释还写着「纯标准库」。OVH 上线后会立刻退回被拦的那条路径。已改为
`curl_cffi PySocks`。

### 2. 头部集合：XHR 形态被伪装成导航（已修）

`curl_cffi` 的 `impersonate` 默认套用**顶层导航**形态：

| 头 | 默认（错） | XHR 实际 |
|---|---|---|
| `Sec-Fetch-Dest` | `document` | `empty` |
| `Sec-Fetch-Mode` | `navigate` | `cors` |
| `Sec-Fetch-Site` | `none` | `same-origin` |
| `Sec-Fetch-User` | `?1` | 不存在 |
| `Upgrade-Insecure-Requests` | `1` | 不存在 |
| `Priority` | `u=0, i` | 不存在 |

带着导航头等于自报「我不是页面内请求」。注意**必须用 `None` 才能摘掉**，
空串会退化成「去掉值但保留头」。

另发现原实现缺 `Accept-Encoding`：urllib 只能发 `identity`，真实 Chrome 发
`gzip, deflate, br, zstd`。这个头不能手写——声称支持 `br` 却解不开会让响应体坏掉，
必须由传输层按实际编码能力决定。

### 3. IP 关联：4 个出口是同一 /24（未修，需换池）

```
51.222.52.200 / .201 / .202 / .203
```

同一 /24、同一 ASN，Cloudflare 侧还落在同一 colo（实测 `cf-ray` 全是 `YYZ`）。
按 IP 轮换对多账号**几乎没有去关联效果**——站点可以按 /24 或 ASN 聚合。

建议：出口按 /24 分散，或至少跨 ASN。这是当前号池最大的关联风险。

### 4. 用户名字符串生成模式（未修）

对比两批账号：

| 特征 | 我生成的(10) | 另一批(13) |
|---|---|---|
| 保留短横 | 6 | 3 |
| 含下划线 | 3 | 6 |
| 首字符为数字 | 0 | 4 |

我的 `username_from_email()` 是「去点号后截断」，另一批是「保留原名」。两套都
可被分离；但单个账号内部看不出异常，只有横向对比全体才显现。若要减弱，应从
邮箱本地部分的样式继承随机性，而不是统一套一个清洗函数。

### 5. 时序：已处理

抓包中人工操作间隔中位 0.97s、1–20s 占 19%；注册机已实现：
- 查信轮询 `POLL_LO..POLL_HI = 3..9s` 随机（原固定 4.0s 是明显机器节拍）
- 核心步骤间 `PACE_STEP 2.0–4.5s`、注册后 `3–6s`、登录后 `1.5–3s`
- **收码后 `9–28s` 长尾**：脚本是「信到就填」，真人要切窗、找信、复制、粘贴，
  这是整条链路最像机器人的瞬间，所以给了明显更长的区间
- `pace_scale` 可从设置页整体调快慢

### 6. 未构成泄露的项

- `New-Api-User: -1` 在未登录阶段是浏览器同样会发的值，不是异常。
- `Origin`/`Referer` 与站点同源，与真实页面一致。
- Cookie 只有 `session`，无额外追踪位。

### 复现方法

```bash
# 回显服务器看服务器侧真实收到的头
python3 -c 'import http.server,json;...' # 见本次分析脚本

# 对照：LibreSSL 被拦 vs curl_cffi 通过（同一代理）
python3 - <<'PY'
# urllib 版本 -> Remote end closed connection without response
# curl_cffi 版本 -> 200 h3
PY
```
