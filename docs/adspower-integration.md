# AdsPower 全流程接入说明

> 实测环境：AdsPower Global 8.9.23（已购付费版）、内核 SunBrowser 153、macOS 宿主机
> 目标：一个账号 = 一个 AdsPower profile，注册→登录→建 Key→绑卡→订阅全程同一

---

## 1. 为什么改用 AdsPower

旧链路有两个结构性缺陷：

| 问题 | 后果 |
|---|---|
| 注册走 `curl_cffi`（Python TLS 栈），绑卡走 Playwright | 对服务端是**两台不同设备**，跨步骤可被关联 |
| UA 池 + ja3 手工拼装 | 出现 "UA 说 135 / TLS 按 136" 这类版本错位（实测 82%） |

AdsPower 把这两件事都解决了：

- **真实 Chromium 内核**（SunBrowser），TLS/HTTP2 指纹原生
- **profile 级绑定**：UA、时区、语言、WebGL、Canvas、字体、代理 IP 全部锁在一个 profile 里
- **会话天然持久**：Cookie 留在 profile，不需要手工搬 `session_cookie`

---

## 2. 启动方式（重要：不要用 CLI 的 open-browser）

CLI 的 `ads open-browser` 在内核已安装时**仍会误报**：

```
SunBrowser 153 is not ready,please to download!
```

但同一时刻直接调 API 就能起来。所以统一走原始 API：

```bash
curl -H "Authorization: Bearer $KEY" \
  "http://local.adspower.net:50325/api/v1/browser/start?user_id=<PROFILE_ID>&cdp_mask=0&headless=0"
```

返回：

```json
{"code":0,"data":{
  "debug_port":"56765",
  "ws":{"puppeteer":"ws://127.0.0.1:56765/devtools/browser/..."},
  "webdriver":".../chromedriver"
}}
```

**`cdp_mask=0` 是关键**——不加的话 CDP 调试口会被屏蔽，接管不了。

---

## 3. 内核版本必须对齐

profile 记录的 `browser_kernel_config.version` 要和本机实际安装的内核一致，
否则启动会失败。本机只装了 `chrome_153`，所以把 profile 固定到 153：

```bash
curl -X POST -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
 -d '{"user_id":"<ID>","fingerprint_config":{"browser_kernel_config":{"version":"153","type":"chrome"}}}' \
 "http://local.adspower.net:50325/api/v1/user/update"
```

**注意**：列表接口 `user/list` **不返回 `fingerprint_config`**，改完从列表看不出来。
要用 `ads get-profile-ua` 或直接启动后实测验证。

---

## 4. 指纹配置（实测踩过的坑）

### 4.1 UA 与 platform 是两个独立字段

只设 `platform: "windows"` 不会改 UA。profile 里存的可能还是 macOS UA，
两者会互相矛盾：

```
ua       = ...Macintosh... Chrome/145      ← 没改
platform = Win32                            ← 改了
```

必须**显式写 `ua`**，且版本与内核一致：

```json
{
  "ua": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Safari/537.36",
  "platform": "windows"
}
```

### 4.2 时区/语言要跟随 IP

```json
{"automatic_timezone":"1", "language_switch":"1", "language":["en-US","en"]}
```

否则会出现：美国住宅 IP + `Asia/Shanghai` + `zh-CN` 的矛盾组合。

### 4.3 WebGL 是"真替换"，不是空壳

实测 AdsPower 能把 WebGL 元数据替换到内部自洽：

```
renderer = ANGLE (Intel, Intel(R) UHD Graphics 630 Direct3D11 vs_5_0 ps_5_0)
maxTex   = 16384        ← 真实 GPU 级别（SwiftShader 只有 8192）
webgl2   = yes
```

这比我最初预估的强——它不只会改字符串。

### 4.4 可用的完整配置

```json
"fingerprint_config": {
  "browser_kernel_config": {"version":"153","type":"chrome"},
  "ua": "<与内核版本一致的完整 UA>",
  "automatic_timezone": "1",
  "language_switch": "1",
  "language": ["en-US","en"],
  "platform": "windows",
  "webgl": "2",
  "webgl_image": "1",
  "webgl_config": {
    "unmasked_vendor": "Google Inc. (Intel)",
    "unmasked_renderer": "ANGLE (Intel, Intel(R) UHD Graphics 630 (0x00003E9B) Direct3D11 vs_5_0 ps_5_0, D3D11)"
  },
  "canvas": "1",
  "audio": "1",
  "hardware_concurrency": "8",
  "device_memory": "8",
  "screen_resolution": "1920_1080"
}
```

---

## 5. 代理：必须先过系统代理

**这是最容易卡住的一步。**

实测：profile 直连 CliProxy 报 `Connection reset by peer`，
因为宿主机本身依赖 Clash 才能出网。

正确做法是用 `tools/proxy_bridge.py` 串联：

```
AdsPower → 127.0.0.1:17080(桥) → 127.0.0.1:10808(Clash) → us2.cliproxy.io → 住宅 IP
```

```bash
python3 tools/proxy_bridge.py --listen 127.0.0.1:17080 \
  --via 127.0.0.1:10808 \
  --upstream "http://user:pass@us2.cliproxy.io:3010"
```

然后把 profile 的代理指向本地桥：

```json
"user_proxy_config": {
  "proxy_soft":"other","proxy_type":"http",
  "proxy_host":"127.0.0.1","proxy_port":"17080",
  "proxy_user":"","proxy_password":""
}
```

实测出口：`174.176.192.76`（US Aberdeen，Comcast 住宅宽带）。

---

## 6. 代码接入

新增 `tools/ads_transport.py`，`deploy_registrar.py` 加 `AdsHttpClient` 适配层
（与 `HttpClient` 同签名，直接替换）。

### 6.1 直接用

```python
from ads_transport import open_profile

p = open_profile(API_KEY, "k1cet4ua")
p.navigate("https://deploy.com/signup?source=landing_login", wait=7)
r = p.post_json("https://ajax-portal.deploy.com/api/register", payload)
print(r.status, r.body)
# 用完不要 stop —— 保持 profile 打开以复用会话
```

### 6.2 走注册机

```bash
python3 tools/deploy_registrar.py --job < job.json
```

```json
{
  "settings": {
    "transport": "adspower",
    "ads_api_key": "<KEY>",
    "ads_headless": false,
    "fast_mode": false
  },
  "items": [
    {"ads_profile_id": "k1cet4ua", "email": "a@yourdomain.com"}
  ]
}
```

或 CLI：

```bash
python3 tools/deploy_registrar.py --transport adspower \
  --ads-profile-id k1cet4ua --ads-api-key <KEY> --file list.txt
```

---

## 7. 实测结果

### 7.1 全流程指纹一致（核心目标）

```
初始  : Chrome/153.0.0.0 | America/New_York | en-US | Win32 | IP=174.176.192.76
注册后: Chrome/153.0.0.0 | America/New_York | en-US | Win32 | IP=174.176.192.76
导航后: Chrome/153.0.0.0 | America/New_York | en-US | Win32 | IP=174.176.192.76

>>> 指纹全程一致: True
>>> IP 全程一致  : True
```

### 7.2 注册接口

```
POST /api/register → 201
{"status_code":201,"message":"Please check your email for verification"}
```

### 7.3 未验证账号

```
GET /api/profile → 401 Cookie and authorization header is missing
```
符合预期：注册后必须先验证邮箱。

---

## 8. 已知限制

| 项 | 状态 | 说明 |
|---|---|---|
| `window.chrome.runtime` | ⚠️ 缺失 | AdsPower 内核特性，真 Chrome 必有；改不了 |
| `outerWidth/Height = 0` | ⚠️ 异常 | 窗口未最大化导致，需在客户端 GUI 手动拉满 |
| `screen 960x540` vs `inner 1920x993` | ⚠️ 矛盾 | Mac Retina 逻辑分辨率（1920÷dpr2），`inner > screen` 不自然 |
| 字体 | ⚠️ 混合 | UA 说 Win32，但能探到 PingFang/SF Pro（宿主是 Mac） |
| 内核 147 | ❌ 未装 | 需在客户端 GUI 下载；153 已可用，暂不阻塞 |

**这些都不是致命问题**（deploy.com 没有指纹 SDK），但在高风控站点会露怯。

---

## 9. 运维要点

**一个 profile 一个号，全程不要换。** 换 profile = 换设备+换 IP，
前面所有伪装作废。

**profile 不要随手 `stop`。** 会话在 Cookie 里，关掉再开会丢。
需要跨天操作时保持 profile 常驻。

**会话复用优先。** 后端已有 `resume_session()` 逻辑，AdsPower 模式下
profile 内 cookie 直接可用，不需要走登录接口。

**并发要节制。** 每个 profile 是一个完整浏览器进程，吃内存。
本机 62G 内存大约能开 10-20 个，OVH 上同理。

---

## 10. 绑卡 + 订阅（已迁移）

### 10.1 结构

`deploy_payment.py` 新增 AdsPower 路径，**不新开浏览器**：

```
AdsProfile（注册用的同一个）
  ├─ 注入 Stripe.js → elements().create('card') → mount
  ├─ OOPIF 填卡（cardnumber / exp-date / cvc / postal）
  ├─ stripe.createToken() → tok_xxx        ← 同设备指纹
  ├─ POST /api/organization/{org}/payment_methods
  ├─ POST .../credit/tax_estimate
  └─ POST .../credit/buy                   ← 实际扣款
```

入口：

```python
from ads_transport import open_profile
import deploy_payment as P

p = open_profile(KEY, "k1cet4ua")
r = P.bind_card_and_charge(
    session_cookie="", org_id=12345, card=card,
    amount=5, ads_profile=p,      # ← 传这个就走 AdsPower 路径
)
```

`bind_card_and_charge(..., ads_profile=p)` 内部转到 `_bind_via_ads()`。
不传 `ads_profile` 则仍走旧的 Playwright 新开浏览器路径（保留兼容）。

### 10.2 三个必须知道的坑

**坑 1：Stripe 的 iframe 是 OOPIF，`Page.getFrameTree` 看不到**

实测：`getFrameTree` 只返回主文档 1 个 frame，而 DOM 里明明有 6 个 iframe。
必须用 `Target.getTargets` 发现，再 `Target.attachToTarget(flatten=True)`：

```python
res = cdp.send("Target.getTargets")
for t in res["targetInfos"]:
    if "elements-inner-card" in t["url"]:
        sid = cdp.send("Target.attachToTarget",
                       {"targetId": t["targetId"], "flatten": True})["sessionId"]
        cdp.send_session(sid, "Runtime.evaluate", {...})
```

**坑 2：Stripe 输入框是受控 React 组件**

直接 `el.value = "4242..."` 不触发 onChange，Stripe 会认为"卡号不完整"。
必须走原生 setter：

```js
const setter = Object.getOwnPropertyDescriptor(
    HTMLInputElement.prototype, 'value').set;
setter.call(el, val);
el.dispatchEvent(new Event('input', {bubbles: true}));
```

**坑 3：`window.__stripe` 会因导航丢失**

先在页面里存的 `window.__stripe`，导航后是 `undefined`。
`create_stripe_token_ads` 在执行 `createToken` 前会检查并重建实例 + 重填。

### 10.3 实测结果

用 Stripe 官方测试卡 `4000000000000002`：

```
ERROR: Your card was declined. Your request was in live mode,
       but used a known test card.
```

**这是成功标志**，不是失败：

- Stripe.js 在 AdsPower profile 内正常加载运行
- 卡号/有效期/CVC 被正确识别（不再是 "incomplete"）
- 请求到达 Stripe 风控层并拿到业务判定
- live 模式拒绝已知测试卡是 Stripe 的预期行为

换真卡即可拿到 `tok_xxx` 并继续绑卡扣款。

### 10.4 重要发现：Stripe 用了 hcaptcha

实测发现 Stripe 在 CardElement 旁边挂了：

```
https://js.stripe.com/v3/hcaptcha-invisible-xxxx
https://newassets.hcaptcha.com/captcha/v1/xxxx
```

**hcaptcha 是 invisible 模式**（用户无感，靠行为特征判定），
不是弹窗挑战。这意味着：

- 它不是"点击验证"那种门槛，但会收集行为信号
- 在真实浏览器里正常填卡通常能静默通过
- **在无头/自动化环境里可能被判定并要求挑战** —— 这是 OVH 上的额外风险

这也解释了为什么 Stripe 那一步**必须**用真浏览器，不能用 HTTP 直连。

### 10.5 批量绑卡

`cmd_bind_job` 支持 AdsPower：

```json
{
  "live": true,
  "amount": 5,
  "ads_api_key": "<KEY>",
  "items": [
    {"id":"a1","email":"a@b.com","org_id":"12345",
     "ads_profile_id":"k1cet4ua",
     "card":"4000000000000002|12|2030|123","zip":"94607","country":"US"}
  ]
}
```

有 `ads_api_key` + `ads_profile_id` 时走 profile 路径，
此时不再需要 `session_cookie`（Cookie 在 profile 里）。

---

## 11. 真卡端到端（已跑通，含真实扣款）

2026-11-15 用真实 Mastercard 完成全链验证。

### 11.1 完整结果

```
① Stripe token      → tok_<TOKEN>
② 绑卡              → 200  internal_id=<PAYMENT_METHOD_ID>
                       brand=MasterCard last4=2826 exp=11/2028
③ tax_estimate      → 200  total=5, tax=0
④ credit/buy        → 200  total_charged=5, balance=6.0
```

余额流水（`GET /api/organization/{org}/credit`）：

```
2026-11-14T14:35:24      1.00  initial          ← 注册赠送
2026-11-14T19:11:08      5.00  bought           ← 本次充值
2026-11-14T19:11:10    100.00  purchase_promo   ← $5 触发 $100 赠送
                          -----
合计                    106.00
```

**$5 确实触发了 $100 赠送**，实际到账 106 美元额度。

### 11.2 绑卡前的环境一致性（关键）

| 项 | 值 |
|---|---|
| 出口 IP | `73.252.209.238`（US Livermore, CA） |
| 浏览器时区 | `America/Los_Angeles` ← **必须与 IP 同城** |
| UA | Windows Chrome/153 |
| 语言 | `en-US, en` |

时区原本是 `America/New_York`，与加州 IP 矛盾，已改为 `America/Los_Angeles`。

**注意**：`automatic_timezone=1`（按 IP 自动）实测**没有生效**，
仍需显式设 `{"automatic_timezone":"0","timezone":"America/Los_Angeles"}`。

### 11.3 踩到的第四个 Stripe 坑

`fill_stripe_frames` 遍历所有含 input 的 Stripe iframe 逐个填，
结果 **postal 被后续 frame 的赋值覆盖成空**，报 `Your postal code is incomplete.`

修复：只填**第一个**真正含 `input[name=cardnumber]` 的 frame，填完即 break。

### 11.4 绑卡流程实拍

```python
import deploy_registrar as R, deploy_payment as P

card = P.Card(number=..., exp_mon=11, exp_year=2028, cvc=..., zip="94550")
p = R.AdsHttpClient(KEY, "k1cet4ua")

tok, err = P.create_stripe_token_ads(p, card)       # ← 同 profile
P.bind_card_ads(p, org, tok)                        # ← 同 profile
p.post_json(f"{P.BASE}/api/organization/{org}/credit/tax_estimate", {...})
p.post_json(f"{P.BASE}/api/organization/{org}/credit/buy",
            {"amount":5,"stripe_source_id":src})    # ← 真实扣款
```

### 11.5 订阅

deploy 走的是"充值额度"模式（credit/buy），不是订阅制。
没有找到 `subscriptions` 端点；`credit/buy` 就是充值入口。

---

## 11.6 待验证

- **多账号并行**：需要多个 profile（一个号一个 profile）
- **绑卡后长期稳定性**：账号是否会被后续风控

---

## 12. 邮件验证：取信独立，验证在浏览器

### 12.1 为什么必须分开

两个动作的技术要求完全不同：

| 步骤 | 正确做法 | 原因 |
|---|---|---|
| **取信** | 独立 HTTP（`MailboxReader`） | 纯 IO，没必要占一个浏览器上下文；轮询几分钟也不该卡住页面 |
| **打开验证链接** | **必须在 profile 内** | 验证响应会下发会话 cookie，只有浏览器自己收到才能写进 profile |

如果验证也用 Python HTTP 去 GET：

```python
# ❌ 错误做法
requests.get("https://ajax-portal.deploy.com/api/authenticate?token=...")
```

cookie 会落在 Python 的内存 jar 里，**profile 依旧是未登录状态**。
后面绑卡时就成了"未登录的设备在操作"，前后完全矛盾。

### 12.2 正确流程

```python
# 1. 取信（独立 HTTP，不占浏览器）
token, mail = reader.wait_for_link(before_ids, timeout=240, since_ts=registered_at)

# 2. 在 profile 内打开验证链接（让 cookie 落到浏览器）
verify_url = f"https://ajax-portal.deploy.com/api/authenticate?{urlencode({'token': token})}"
http.navigate(verify_url, wait=6)          # ← 浏览器自己访问

# 3. 用页面内请求确认 + 拿 profile
r = http.request("GET", PROFILE_URL)
# 200 → 会话已建立，cookie 在 profile 里
```

### 12.3 踩到的四个 bug（都是实测发现的）

**bug 1：token 提取正则丢掉点号（严重）**

deploy 的验证 token 是 JWT，形如 `xxx.yyy.zzz`。原正则：

```python
r"https://deploy\.com/verify-email\?token=([A-Za-z0-9_\-]+)"   # ❌ 不含点
```

会在第一个 `.` 处截断，拿到半个 token，验证必定失败
（`Unable to decrypt token`）。已改为：

```python
r"https://deploy\.com/verify-email\?token=([A-Za-z0-9_.\-%]+)"
```

实测：`eyJhbGciOiJIUzI1NiJ9.eyJpZCI6MTIzfQ.abc-_123` 现在能完整提取。

**bug 2：验证失败仍标记 verified=True**

原代码在 `ar.status != 200` 后继续执行，把 `verified` 置 True。
上层会以为账号可用，等绑卡才发现是未验证号。已改为只在 200 时置 True，
并把失败原因写进 `result["error"]`。

**bug 3（最严重）：`run_one` 里硬编码了 `HttpClient`，AdsPower 完全没参与**

```python
# ❌ 原代码
http = HttpClient(timeout=30, transport=transport, profile=profile, ...)
```

这行直接绕开了 `build_client()`，所以即便设置 `transport=adspower`，
**所有请求仍然是 curl_cffi 发的**。表现极具迷惑性：

- 注册返回 201，"成功"了
- 但浏览器 profile 里**一条 deploy 的 cookie 都没有**
- 日志里没有 `传输: AdsPower profile` 这一行

已改为 `http = build_client(item, settings, profile)`。
**排查技巧**：看日志里有没有 `传输: AdsPower profile（浏览器内请求）`，
没有就说明没走浏览器。

**bug 4：`document.cookie` 读不到会话 cookie**

`hive-portal-production` 是 **HttpOnly**，`document.cookie` 看不到。
用 JS 读会误判成"没有会话"，进而触发不必要的重新登录。
必须走 CDP：

```python
res = cdp.send("Network.getAllCookies")
# 且该 cookie 的 domain 是 ajax-portal.deploy.com，不是 .deploy.com
```

另外 `navigate()` 之后必须**主动刷新 cookie**：验证链接就是靠导航下发
会话的，不刷新拿到的还是导航前的空快照。

### 12.4 验证端点行为（实测）

| 输入 | 响应 |
|---|---|
| 无效 token | `401 {"message":"Unable to decrypt token"}` |
| 有效 token | `200` + 下发会话 cookie |

打开验证链接后 URL 会变成 `https://deploy.com/verify-email`
（token 被前端从地址栏移除，属正常行为）。

### 12.5 真实邮箱端到端结果

用 iCloud HME 真实接码，完整跑通：

```
注册 user@example-icloud.com → 201
拿到验证 token (153 chars)
验证通过 account_id=<ACCOUNT_ID>
profile OK is_verified=True org=<ORG_ID>
```

验证后浏览器内的 cookie（CDP 读取）：

```
csrf                        dom=ajax-portal.deploy.com  httpOnly=True
hive-portal-production      dom=ajax-portal.deploy.com  httpOnly=True   ← 会话在这里
__stripe_mid                dom=.deploy.com             httpOnly=False
```

`GET /api/profile → 200`，说明**会话确实建立在 profile 里**，
后续绑卡可以直接用这个 profile，不需要再登录。

### 12.6 一个重要发现：同邮箱重复注册不发新验证信

实测：同一邮箱第二次调用 `/api/register` 仍返回 201，
但**不会发送新的验证邮件**（收件箱 3 封没有任何新增）。

这意味着：
- 不能靠"重新注册"来重发验证信
- 账号状态以 `/api/profile` 为准，不要看注册返回码
- 批量跑时每个邮箱只能用一次；重试要用新地址

---

## 13. OVH 部署注意

OVH 那台是 Ubuntu 26.04 + ASPEED 板载显卡，实测有问题：

```
ERROR: ContextResult::kFatalFailure: WebGL1 blocklisted
```

**ASPEED 显卡在 Chromium 黑名单里，WebGL 上下文创建失败。**
而 AdsPower 的 WebGL 伪装**依赖上下文存在**——上下文都没有，替换就无从谈起。

这意味着 OVH 上 AdsPower 的价值会大打折扣。建议：

1. 本机（Mac，有真 GPU）跑，验证效果
2. 若要扩容，用**带核显**的机器（Intel UHD / AMD Vega），而不是纯服务器
3. 纯服务器上只能接受 SwiftShader 软件渲染，那是明显的自动化特征


---

## 14. 一号一 profile 批量创建

### 14.1 概念澄清

**profile（环境）不是窗口。**

| | 是什么 |
|---|---|
| profile | 一套**持久身份**：指纹 + UA + 时区 + Cookie + 代理 + 本地存储 |
| 窗口 | profile 运行时的界面，只是临时视图 |

关窗口 ≠ 删 profile。实测验证过：关掉浏览器 + 重启客户端，profile 里的
`hive-portal-production` cookie 依然在，`/api/profile` 仍返回 200。

### 14.2 为什么必须一号一 profile

我早期测试时反复用同一个 profile 跑多个测试账号，结果就是污染：

```
k1cet4ua 里混着：
  - user@example-icloud.com 的会话
  - 多个 test.dev 假邮箱的残留
  - 横跨多个测试的 __stripe_mid
```

如果这些是真实账号，上游能看到「**同一台设备登录了 N 个账号**」。
这是最强的关联信号，比任何指纹伪装都致命。

正确结构：

```
账号 A → profile P1 → 桥 :17200 → 代理 A → IP_A（固定）
账号 B → profile P2 → 桥 :17201 → 代理 B → IP_B（固定）
```

### 14.3 工具：`tools/ads_provision.py`

核心思路：**先探代理，再按实测地理位置生成指纹**。

顺序很重要。反过来（先生成指纹再配代理）就会造出
「加州 IP + 纽约时区」这种自相矛盾的身份。

```bash
# 批量创建（每行一个代理）
python3 tools/ads_provision.py --proxies proxies.txt -n 10

# 干跑，只看会创建什么
python3 tools/ads_provision.py --proxies proxies.txt -n 10 --dry-run

# 列出 profile 与映射关系
python3 tools/ads_provision.py --list --api-key $KEY

# 检查常驻桥状态
python3 tools/ads_provision.py --bridges

# 机器重启后重建所有桥
python3 tools/ads_provision.py --rebuild-bridges
```

### 14.4 两个关键设计

**设计 1：每个代理单独探测**

最初所有探测都走同一座桥，结果两个不同 session 的代理探到**同一个 IP**：

```
[1/2] IP: 47.16.133.117 (US / New York City)
[2/2] IP: 47.16.133.117 (US / New York City)   ← 同一个！
```

因为桥的上游在启动时就固定了。修复：为每个代理临时起一座桥，
探测完即销毁。现在两个代理正确探到不同 IP 和不同时区：

```
deploy-001 → 128.92.198.59 / Waxahachie / America/Chicago
deploy-002 → 67.84.113.113 / New York City / America/New_York
```

**设计 2：每个 profile 一座常驻桥**

AdsPower 的代理字段只能填 `host:port`，填不了 socks5 凭据链；
而本机又必须经 Clash 才能连上 zooproxy。所以：

```
profile → 127.0.0.1:172xx(常驻桥) → Clash(10808) → zooproxy → 出口 IP
```

桥是外部进程，**机器重启后会消失**，此时 profile 无法上网。
用 `--rebuild-bridges` 按映射表重建。

### 14.5 映射表

`tools/data/ads_profiles.json`：

```json
{"profiles": [{
  "profile_id": "k1j2oaa5",
  "serial": "19",
  "name": "deploy-001",
  "proxy": "socks5://...sid-XXX-t-10:...@us-eu.zooproxy.com:5000",
  "bridge_port": 17200,
  "ip": "128.92.198.59",
  "country": "US",
  "city": "Waxahachie",
  "timezone": "America/Chicago",
  "created": "2026-11-15T..."
}]}
```

这张表是**运维的唯一依据**：知道哪个 profile 配哪个代理、该听哪个端口。

### 14.6 实测结果

创建后立即验证指纹与代理是否自洽：

```
=== k1j2oaa5 ===
  IP : 128.92.198.59 Waxahachie America/Chicago
  TZ : America/Chicago          ← 与 IP 同城
  UA : Chrome/153.0.0.0         ← 与内核一致
  plat: Win32 | lang: ["en-US","en"]
  webgl: ANGLE (Intel, UHD Graphics 630 ...)

=== k1j2oae0 ===
  IP : 67.84.113.113 New York City America/New_York
  TZ : America/New_York
  ...（同上）
```

用 `k1j2oaa5` 跑完整注册：**201 成功**，全程 IP 保持 Chicago。

### 14.7 注意事项

- **代理 session 有效期**：`sid-XXX-t-10` 的 `t-10` 是 10 分钟轮换。
  同一 session 内 IP 稳定（实测连查 3 次都是 `73.252.209.238`），
  但过了 `t-10` 会换 IP。**长期跑号要留意这个**，必要时用更长的 `t` 值。
- **套餐环境数上限**：免费版通常 2 个，付费按档位。创建时若报
  `only available in paid subscriptions` 就是满了。
- **创建间隔**：工具内置 1.5 秒间隔，避免触发上游频率限制。


---

## 15. 跨机架构：Mac 跑浏览器，OVH 跑调度

### 15.1 为什么不能直接在 OVH 跑 AdsPower

两个硬障碍：

**障碍 1：AdsPower 是本地客户端架构**

```
profile 数据在本地磁盘：
~/Library/Application Support/adspower_global/cwd_global/source/cache/
    k1cet4ua_h248ag0/     ← Cookie、LocalStorage 都在这
    k1j2oaa5_h248ag0/
```

云端只同步 **profile 配置**（指纹、代理、名称），**会话数据不同步**。
把这个目录拷到 OVH 也没用：目录名里的 `_h248ag0` 是设备 hash，对不上。

**障碍 2：OVH 显卡被 Chromium 拉黑**

实测：

```
ERROR: ContextResult::kFatalFailure: WebGL1 blocklisted
ERROR: SharedImageManager::ProduceSkia: incompatible backing
```

ASPEED 板载显卡在黑名单里，WebGL 上下文建不出来。AdsPower 的 WebGL
伪装依赖上下文存在 —— 上下文都没有，替换无从谈起。

### 15.2 架构

```
┌─────────────────────────┐        ┌──────────────────────────┐
│  Mac（有真 GPU）         │        │  OVH（纯服务器）           │
│                         │  SSH   │                          │
│  AdsPower 客户端         │◄──────►│  account-pool-balancer   │
│  ├ profile + Cookie     │  隧道   │  ├ 账号池调度              │
│  ├ SunBrowser 153       │        │  ├ 接码                    │
│  ├ 常驻代理桥 :172xx     │        │  ├ 数据库                  │
│  └ tunnel_daemon :15026 │        │  └ 号池入库                │
└─────────────────────────┘        └──────────────────────────┘
```

**分工原则：会话数据留在 Mac（不搬），OVH 只存账号元数据并远程驱动。**

搬会话反而有害：profile 的 Cookie 绑定本地设备，搬到 OVH 就是
"新设备登录"，直接触发风控。

### 15.3 隧道设计（关键）

**难点：CDP 端口是动态的**（每次启动 profile 都不同），无法预先写死隧道。

解法：Mac 侧跑一个守护，OVH 通过 HTTP 请求它**按需建隧道**。

```
OVH                              Mac
 │  POST /tunnel?port=57452
 ├─────────────────────────────► tunnel_daemon
 │                                └─ ssh -N -R 16000:127.0.0.1:57452 ovh
 │  ◄── {"ok":true,"local_port":16000}
 │
 └─ 连 ws://127.0.0.1:16000/devtools/browser/...
```

两条固定隧道：

| 隧道 | 方向 | 用途 |
|---|---|---|
| `-R 15025:127.0.0.1:50325` | Mac → OVH | AdsPower API |
| `-R 15026:127.0.0.1:15026` | Mac → OVH | 隧道守护 |

CDP 隧道按需动态建，profile 关闭后守护自动回收。

### 15.4 部署步骤

**Mac 侧（服务端）：**

```bash
# 1. 起守护（同时会建 API 隧道）
python3 tools/ads_tunnel_daemon.py --listen 0.0.0.0:15026 --ssh ovh-server

# 2. 把守护端口暴露给 OVH
ssh -N -f -R 15026:127.0.0.1:15026 ovh-server
```

**OVH 侧（客户端）：**

```bash
# 依赖
sudo apt-get install -y python3-websocket

# 驱动
python3 -c "
import sys; sys.path.insert(0,'.')
from ads_transport import open_remote_profile
p = open_remote_profile(
    KEY, 'k1j2oaa5',
    api_base='http://127.0.0.1:15025',      # Mac 的 API
    tunnel_daemon='http://127.0.0.1:15026', # Mac 的守护
)
print(p.eval('navigator.userAgent'))
"
```

### 15.5 实测结果

从 OVH 实际驱动 Mac 上的浏览器：

```
OVH 读到的浏览器数据: {"ua":"153.0.0.0","tz":"America/Chicago",
                    "plat":"Win32","lang":"en-US"}

CSRF via OVH->Mac: 200 {"status_code":200,"message":"success",...}
出口 IP: 82.40.72.72
```

**关键点：出口 IP 是 Mac 上代理的 IP，不是 OVH 的。** 这说明流量路径是
`OVH → 隧道 → Mac 浏览器 → Mac 的代理 → 目标`，指纹和 IP 全都来自 Mac。

### 15.6 会话与号池的存放

这是你关心的重点，明确分工：

| 数据 | 存哪 | 原因 |
|---|---|---|
| **profile（Cookie/本地存储/指纹）** | **Mac 本地** | 搬到 OVH 会变成新设备登录 |
| **profile 配置（代理/指纹参数）** | AdsPower 云端 + Mac | 云端自动同步 |
| **账号元数据** | **OVH 数据库** | account_id / org_id / access_key / 绑卡信息 / 余额 |
| **映射关系** | `tools/data/ads_profiles.json` | profile_id ↔ 代理 ↔ 端口 |

号池入库只需要**元数据**：`account_id`、`org_id`、`access_key`、
`secret_key`、绑卡 `internal_id`、余额。这些都能从 API 拿到并存进 OVH 的库。

**调模型 API 时不需要浏览器** —— 用 `access_key` + `secret_key` 直连
`api.deploy.com` 即可，走 OVH 自己的出口。

所以真正需要浏览器的只有：注册、验证、登录、绑卡。这三个低频动作走隧道，
高频的模型调用走 OVH 直连。架构上是合理的。

### 15.7 限制与注意

- **Mac 必须在线**。它是浏览器宿主，掉线则所有 profile 不可用。
- **隧道带宽**：CDP 是 JSON over WebSocket，绑卡等操作流量很小，
  但页面加载（图片/JS）走隧道会慢。实测可接受。
- **并发**：每个 profile 一个 CDP 隧道 + 一个浏览器进程。
  Mac 上开 10-20 个实例要留意内存。
- **守护重启**：Mac 重启后隧道全断，需重跑守护 + 重新暴露端口。
- **安全**：隧道只监听 `127.0.0.1`（OVH 侧），不要 `-R 0.0.0.0`。


---

## 16. 号池额度查询（cookie 直连，不需要浏览器）

### 16.1 关键结论

deploy 的会话**就一个 cookie**：

```
hive-portal-production=<SESSION_COOKIE>
```

把它从 AdsPower profile 里取出来，**在任何机器上直连都能用**：

```http
GET https://ajax-portal.deploy.com/api/organization/{org}/credit
Cookie: hive-portal-production=<...>
→ 200 + 流水数组
```

实测从 OVH 用 Mac profile 里提取的 cookie 查询，返回完整数据。

**所以额度查询不需要 CDP 隧道。** 隧道是给绑卡这种「必须真浏览器
（Stripe hcaptcha）」操作用的。查询是纯读，HTTP 足够。

### 16.2 数据流设计

```
Mac (AdsPower)                    OVH (号池)
  │ ① 提取 cookie（一次）           │
  └──────────────────────────────► │ ② 入库存字符串
                                    │
                                    └─ ③ 定时查额度（纯 HTTP，高频低成本）
```

**关键点：cookie 只同步一次，之后 OVH 独立工作。**

| 操作 | 需要 Mac？ | 频率 |
|---|---|---|
| 注册 / 验证 / 绑卡 | ✅ 需要（走隧道） | 低（一次性） |
| 存库时提取 cookie | ✅ 需要（一次） | 低 |
| **查额度 / 查会话有效性** | ❌ **不需要** | 高（可定时） |
| 调模型 API | ❌ 不需要（用 access_key） | 最高 |

### 16.3 工具：`tools/deploy_quota.py`

```bash
# ① 在 Mac 上提取 cookie（输出字符串，用于入库）
python3 tools/deploy_quota.py --extract $API_KEY k1cet4ua

# ② 查额度（自动从 /api/profile 取 org_id）
python3 tools/deploy_quota.py --cookie "<会话>" 

# ③ 只看会话是否还有效
python3 tools/deploy_quota.py --cookie "<会话>" --alive
# → {"alive": true, "reason": "ok"}
# → {"alive": false, "reason": "会话失效（401）"}

# ④ 批量（jsonl，每行含 session_cookie + org_id）
python3 tools/deploy_quota.py --file accounts.jsonl
```

### 16.4 额度口径

`GET /api/organization/{org}/credit` 返回**流水数组**：

```json
{"data": [
  {"charged_on":"2026-11-14T19:11:10Z","amount":"100.00","reason":"purchase_promo"},
  {"charged_on":"2026-11-14T19:11:08Z","amount":"5.00","reason":"bought"},
  {"charged_on":"2026-11-14T14:35:24Z","amount":"1.00","reason":"initial"}
]}
```

**余额 = 各条 amount 之和。**

实测 `reason` 取值：`initial`（注册赠送）/ `bought`（充值）/ `purchase_promo`（满额赠送）。

⚠️ **这是账户余额，不是模型调用配额。** RPM/TPM 这类限速需要另一套接口，
而且要用 `access_key` 而非 cookie。

### 16.5 实测输出

```
账号: user@example-icloud.com (id=<ACCOUNT_ID>, org=<ORG_ID>)
org_id : <ORG_ID>
余额   : $106.00
流水   : 3 条
  2026-11-14T19:11:10     100.00  purchase_promo
  2026-11-14T19:11:08       5.00  bought
  2026-11-14T14:35:24       1.00  initial
```

**这条输出是在 OVH 上跑出来的**，全程没有连 Mac。

### 16.6 入库字段建议

号池要存这些（OVH 数据库）：

```json
{
  "email": "...",
  "account_id": "<ACCOUNT_ID>",
  "org_id": "<ORG_ID>",
  "session_cookie": "<SESSION_COOKIE>",       ← 查额度用
  "ads_profile_id": "k1cet4ua",         ← 回到 Mac 操作时用
  "access_key": "<ACCESS_KEY>",
  "secret_key": "<只在创建时返回一次>",
  "card_last4": "2826",
  "card_internal_id": "<绑卡后返回>",
  "balance": 106.0,
  "quota_checked_at": "2026-11-15T..."
}
```

**注意**：`session_cookie` 会过期（服务端轮换）。同步策略建议：
- 定时用 `--alive` 检测，失效则标记 `session_expired`
- 会话失效**不代表账号失效**，可以走隧道重新登录刷新 cookie
- 不要因为 cookie 失效就判定账号报废

### 16.7 安全

`session_cookie` 等价于账号登录态。存库时：
- 加密存储（与其它凭据同等对待）
- 日志里只打前 8 位
- 不要进 git
