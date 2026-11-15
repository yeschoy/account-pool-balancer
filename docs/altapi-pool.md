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
