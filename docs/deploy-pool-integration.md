# Deploy 号池：注册机 + 号池管理

> 接入 account-pool-balancer Web 控制台。顶部号池切换到 **「Deploy 号池」** 后，侧边栏出现
> 「Deploy 号池管理」与「Deploy 设置」两页。

---

## 1. 架构

```
浏览器（增加 deploy 标签）
   │  /api/deploy/*
   ▼
internal/server/deploy_registration.go   ← Go 管理器（单 worker 串行）
   │  exec python3 tools/deploy_registrar.py --job-file <tmp.json>
   │  stdout 逐行 JSON 事件 → 回写 store
   ▼
internal/deploysignupstore/store.go      ← 明文 JSON 持久化
   data/deploy-signup.json
   │
   ▼
tools/deploy_registrar.py                ← 注册机（接码 + 指纹 + 代理）
tools/deploy_payment.py                  ← 绑卡 + 充值（未实测）
tools/fp_layer.py                        ← UA 池 / TLS 指纹
tools/data/ua_pool.json                  ← 137 条 UA
```

---

## 2. 完整注册链路（实测打通，单号 ~22 秒）

| 步 | 动作 | 接口 |
|---|---|---|
| 1 | 注册 | `POST https://ajax-portal.deploy.com/api/register` → 201 |
| 2 | 记录接码现状 | `GET https://<hme-host>/api/messages?alias_email=&token=&range=all` |
| 3 | 轮询新邮件 | 同上，比对邮件 id 增量 |
| 4 | 提取 token | 正则 `https://deploy\.com/verify-email\?token=([A-Za-z0-9_\-]+)` |
| 5 | **验证邮箱** | `GET /api/authenticate?token=...` → 200 + `hive-portal-production` cookie |
| 6 | 确认 | `GET /api/profile` → `is_verified: true` |

> 验证邮件是**链接**不是验证码。邮件正文："please verify your email address and update your
> temporary password"，链接 24 小时有效。

---

## 3. 使用

### 3.1 导入账号

号池页粘贴，每行 `邮箱----接码地址`：

```
user1@icloud.com----https://icloud.mida.vip/messages/user1%40icloud.com?token=xxx
user2@icloud.com----https://icloud.mida.vip/messages/user2%40icloud.com?token=yyy
```

也接受 `\t` 分隔。重复邮箱会被跳过并报告。

### 3.2 代理池与代理桥

同一页粘贴，每行一个。**账号数与代理数不必相等**，按 `i % N` 轮询复用：

```
账号[0] → 代理[0]
账号[1] → 代理[1]
账号[2] → 代理[0]   ← 复用
...
```

#### 关键：本机必须经系统代理才能出网

2026-11-13 实测确认（此前误判为「IP 未白名单」）：

- 本机只有在系统代理（Clash `127.0.0.1:10808`）开启时才能出外网
- zooproxy 必须**经系统代理隧道**才能 SOCKS5 握手成功
- 直连 zooproxy 会得到 `HTTP/1.1 403`（因为没有出网通路，不是鉴权失败）

而 **curl_cffi 不支持多级代理**（`PRE_PROXY` 对 SOCKS5 上游无效），
所以引入了**本地代理桥** `tools/proxy_bridge.py`：

```
curl_cffi ──HTTP CONNECT──> 127.0.0.1:1750x
                                │
                                └─CONNECT──> 系统代理 ──> zooproxy(SOCKS5) ──> deploy.com
```

curl_cffi 只看到本地 HTTP 代理，**TLS/JA3 指纹能力完整保留**。

| 设置项 | 默认 | 说明 |
|---|---|---|
| 系统代理 | `127.0.0.1:10808` | 本机 Clash/V2Ray 地址；留空表示不用 |
| 代理桥起始端口 | `17500` | 每个上游代理占一个端口 |

每个上游代理首次使用时自动起一个桥，日志打印
`代理桥 @127.0.0.1:17500 -> us-eu.zooproxy.com:5000`。批次结束自动关闭。

#### 出口 IP 实测

| sid | 出口 IP |
|---|---|
| gbSugm14 | 200.13.205.240 |
| PFzfvhTv | 219.91.52.49 |
| vYbtdvfh | 165.49.3.49 |
| MGZCv7wZ | 14.168.162.116 |
| UBFW1aZ8 | 103.99.2.146（首次 TLS 失败，重试成功）|
| 4K3D6hHW | 首次 TLS 失败，重试成功 |

**每个 sid 都是不同出口 IP**，符合「一号一出口」。
偶发 `TLS connect error` 是上游节点不稳，重试即可（注册机遇错会记为 failed，可重跑）。

### 3.3 注册

- 单条：「注册」按钮
- 批量：「批量注册待处理」→ 处理所有 `queued` / `failed`

**串行执行**，同一时间只跑一个批次。间隔默认 8 秒（实测 429 冷却窗长）。

### 3.4 状态机

```
queued → running → registered（注册成功，等邮件）→ verified（邮箱已验证，有会话）
                 ↘ failed（可重试）
        cancelled
```

`verified` 的记录里保存了：`session_cookie`（`hive-portal-production`）、
`account_id`、`org_id`、UA / 浏览器 / 平台 / JA3。**后续用同一身份操作时必须复用这些。**

---

## 3.5 会话保存与复用（重要）

### 3.5.1 保存了什么

每个账号在 `verified` 后保存：

| 字段 | 用途 |
|---|---|
| `session_cookie` | `hive-portal-production` 的值，**唯一登录凭据** |
| `account_id` / `org_id` | 账号与组织 ID |
| `ua` / `browser` / `platform` / `ja3` | 注册时的浏览器指纹 |
| `proxy_url` | 注册时的出口代理 |

### 3.5.2 cookie 是 URL-encoded 的，不能二次编码

会话值里含 `+` 和 `%2F`，**必须原样放进 `Cookie:` 头**：

```python
headers["Cookie"] = f"hive-portal-production={cookie}"   # ✅ 正确
headers["Cookie"] = f"hive-portal-production={quote(cookie)}"  # ❌ 401
```

实测：原样 → 200；错误编码一次 → `401 Authentication Failed`。

### 3.5.3 复用流程

```
已有 session_cookie ?
   ├─ GET /api/profile → 200  ✅ 复用成功（并保存服务端轮换后的新 cookie）
   └─ 非 200
        ├─ 用保存的密码 POST /api/login?enforce_csrf=true
        │     ├─ 200 → 拿新会话 ✅
        │     └─ 失败 → 回落到重新注册
        └─ 无密码 → 重新注册
```

HTML 里点「校验会话」按钮触发，API 是
`POST /api/deploy/candidates/{id}/resume`。

> **重要：服务端会轮换 cookie。** 每次复用后要把响应里的新值写回，
> 否则下一次复用会用已失效的旧值。

### 3.5.4 复用必须沿用原指纹

实测 deploy **不校验** UA/TLS 与会话的绑定（换成 Firefox UA 依然 200）。
但这不等于可以随意换：服务端不拦截 ≠ 不记录。复用时代码会用
`fp_layer.restore_profile()` 还原注册时的 UA / 浏览器 / ja3，
保证 UA 与 TLS 同族。

---

## 4. 指纹

沿用 `tools/fp_layer.py`（见 `docs/deploy-signup-recon.md` §5.3b）：

- 137 条真实 UA，**每号固定一个**，不逐请求轮换
- Chrome/Edge 注入实测的 Chrome 157 JA3 → JA4 `t13d1517h2_8daaf6152771_*`，与真 Chrome 前两段一致
- Safari 按大版本细分底座（safari26_0 / safari18_0 / safari17_0）
- Firefox 用 firefox133 底座
- UA 与 TLS 同族匹配，不会出现 Firefox UA 配 Chrome TLS

UA / JA3 / 平台会随账号一起落盘，重登录时复用。

---

## 5. 绑卡与充值（2026-11-13 已实测打通，含真实扣款）

`tools/deploy_payment.py`

### 5.1 关键发现：纯 HTTP 生成 card token 已被 Stripe 封死

静态逆向时以为可以直接调 `/v1/tokens`，**实测被拒**：

```json
{"error":{"code":"unsupported_publishable_key_tokenization_surface",
 "message":"This integration surface is unsupported for publishable key tokenization."}}
```

`/v1/payment_methods` 同样被拒，**与卡是否有效无关**（测试卡也拒）。
唯一可行路径是**在浏览器里跑 Stripe.js**。

### 5.2 验证可行的方案：Playwright + Stripe.js

```
① Playwright 打开 https://deploy.com/signup（保证 https origin 正确）
② 注入 https://js.stripe.com/v3/
③ elements().create('card') 挂到 DOM，等 ready
④ 在 Stripe 的 elements-inner-card iframe 里填
     input[name=cardnumber] / exp-date / cvc / postal
⑤ stripe.createToken(card, {address_zip}) → tok_xxx
```

实测：
- Stripe 官方测试卡 → `"您的银行卡被拒绝了。您的请求处于真实模式，但使用的是已知的测试卡。"`（链路通，live 模式拒测试卡）
- 真实卡 → **成功拿到 `tok_xxx`**，不扣款

### 5.3 Portal API 路径（逆向修正）

**必须带 `/api` 前缀**，否则 404：

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/organization/{org_id}/payment_methods` | 绑卡 `{token, set_primary}` |
| GET | `/api/organization/{org_id}/payment_methods` | 列已绑卡 |
| POST | `/api/organization/{org_id}/credit/tax_estimate` | 算税 |
| POST | `/api/organization/{org_id}/credit/buy` | **扣款** |
| GET | `/api/organization/{org_id}/credit` | 充值流水 |
| GET | `/api/organization/{org_id}` | 组织信息 |

（静态逆向时漏了 `api/` 前缀，实测才暴露。）

### 5.4 绑卡的坑

| 现象 | 含义 |
|---|---|
| `400 Duplicate fingerprint found. Cannot add credit card` | **卡已经绑上了**，不是失败。继续用已有 source |
| 绑定响应体不含 source id | 必须回查 `GET payment_methods` 拿 `data.list[].internal_id` |

### 5.5 实测扣款结果

**账号 A（bracken_elegant7q）——成功**

```
tax_estimate: {amount:5, tax_amount:0, total:5}
credit/buy  : {total_charged:5, balance:6.00}
流水:  initial 1.00 + bought 5.00 + purchase_promo 100.00 = 106.00
```

**$100 免费额度确实到账**，条件是先充值 $5。

**账号 B（saints.saver2h）——绑卡成功但充值被拒**

```
credit/buy → HTTP 400 {"message":"Purchase Declined"}
```

> ⚠️ **同一张卡绑第二个账号，充值被拒。**
> 这是本次最重要的风控证据：Stripe/deploy 侧能识别同一张卡，
> 卡本身的 fingerprint 关联比 UA/IP 关联强得多。
> **结论：一号一卡是硬约束，不是建议。**

### 5.5b 一卡多绑保护（代码强制）

`tools/deploy_payment.py` 内置 `assert_card_unused()`：

- 卡指纹台账：`tools/data/card_ledger.json`（卡号 SHA-256 前 32 位 → 已绑 org 列表）
- 绑卡前检查：该卡已属于**其他** org → 抛 `CardReuseError`，`step=card_reuse_blocked`
- 同一 org 重复绑放行（幂等，服务端返回 `Duplicate fingerprint`）
- **dry_run 也会拦截**，防止演练阶段先踩坑
- 绑卡成功立即 `record_card_use()` 登记

实测：

```
1. 同 org 再绑        → ✅ 放行
2. 换到另一个 org     → ✅ 拦截（card_reuse_blocked）
```

**每个账号必须配独立的卡。** 没有例外。

### 5.6 用量

```bash
python3 tools/deploy_payment.py \
  --email a@b.com --session-cookie '<hive-portal-production>' --org-id 1635779438 \
  --card "5378727122402135|11|2028|804|94607|US"
# 默认 dry-run；加 --live 才会真实扣款
```

## 6. 后端 API

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/deploy/candidates` | 列表 + 设置 |
| POST | `/api/deploy/candidates` | 单条添加 `{email, mailbox_url}` |
| POST | `/api/deploy/candidates/bulk` | 批量 `{text}` |
| DELETE | `/api/deploy/candidates/{id}` | 删除 |
| POST | `/api/deploy/candidates/{id}/register` | 注册单条 |
| POST | `/api/deploy/candidates/bulk-register` | 批量注册待处理 |
| POST | `/api/deploy/candidates/{id}/cancel` | 取消 |
| GET | `/api/deploy/logs` | 运行日志（环形，保留 500 条） |
| GET/PUT | `/api/deploy/settings` | 读写设置 |
| GET | `/api/deploy/status` | 计数 + busy |

存储文件：`data/deploy-signup.json`（0600，明文）。

---

## 7. 已知问题 / 待办

| # | 问题 | 状态 |
|---|---|---|
| 1 | ~~zooproxy 403~~ | ✅ **已解决**：实为需经系统代理，代理桥已实现 |
| 2 | ~~绑卡流程未验证~~ | ✅ **已用真实卡跑通**，$5 扣款成功、$100 到账 |
| 3 | ~~`payment_methods` 字段名靠推测~~ | ✅ 实测为 `data.list[].internal_id` |
| 4 | ~~「设置临时密码」流程~~ | ❌ **不存在**。注册时设的密码直接可登录（已实测 3/3）|
| 5 | 无并发，单 worker | 设计如此（429 + 一 IP 一 号） |
| 6 | 上游代理偶发 TLS 失败 | 复测 30/30 全成功，不复现；重试保留 |
| 9 | **同卡绑多号充值被拒** | 设计约束：一号一卡 |
| 7 | 代理桥端口占用导致整批失败 | ✅ 端口自动顺延 50 位 |
| 8 | 进程异常留下僵尸 `running` | ✅ 批次结束强制回落 `failed` |

---

## 8. 验证记录（2026-11-13）

```
[CLI]  beards-armors.19@icloud.com
       注册 201 → 拿 token(153) → authenticate 200 → is_verified=True
       耗时 22s

[Web API] 批量导入 2 条 → 触发注册
       saints.saver2h@icloud.com   verified  acct=1241231612
       errata_71sharer@icloud.com  verified  acct=1173871187
       重启服务后数据完整保留

[代理桥] 经系统代理隧道后 zooproxy 握手成功（直连得 403）
        出口 IP 实测：每个 sid 不同（200.13.205.240 / 219.91.52.49 / ...）
        轮询分配：2 代理 5 账号 → 0→A,1→B,2→A,3→B,4→A

[代理注册] ruddier-hits2i@icloud.com  verified  acct=1449734795
[Web+代理] bracken_elegant7q@icloud.com  verified  acct=1972799542

[会话复用] 3 个账号经代理桥依次 resume，全部「会话复用成功」，未重复注册
          bracken_elegant7q  1972799542
          saints.saver2h     1241231612
          errata_71sharer    1173871187
[会话失效] 故意传坏 cookie → 自动用密码重新登录拿到新会话 ✅
[cookie编码] 原样 200 / 错误 quote 一次 → 401

[绑卡]  dry-run 通过；Luhn / 品牌识别正确
```
