# Mistral 注册机（任务2）

## 已实测跑通

| 阶段 | 状态 | 证据 |
|---|---|---|
| 代理链（本地→zooproxy→住宅IP） | ✅ | `66.131.78.121` Canada Videotron |
| 指纹伪装（UA+12 Client Hints+时区+语言） | ✅ | `en-CA / America/Winnipeg / macOS` |
| Kratos 注册（邮箱→Signup表单） | ✅ | `ruddier-hits2i` 成功 |
| iCloud HME 接码 | ✅ | 自动取 6 位码 |
| 邮箱验证 | ✅ | `Email successfully verified` |
| admin 登录 + 会话保持 | ✅ | 同 context 内保持 |
| org/workspace UUID 提取 | ✅ | `f9e0f82e...` / `453e2580...` |
| **三把 Key 生成** | ✅ | API/Vibe/Studio 全部拿到明文 |
| 计费表单填写 | ✅ | 地址保存、HST 自动计税 |
| Stripe 卡号填写 | ✅ | `4242 4242 4242 4242` / `12 / 30` / `123` |
| 提交订阅 | ⚠️ | 卡已填、Submit 可点，ToS 勾选+最终确认待完成 |
| 取消订阅接口 | ✅ | `billing.subscriptions.cancelUnsubscription` |

## 成功产出样例

```json
{
  "email": "user@icloud.com",
  "password": "REDACTED",
  "org_uuid": "<org-uuid>",
  "workspace_uuid": "<workspace-uuid>",
  "keys": {
    "API":    "mstrl_XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX_XXXXXX",
    "Vibe":   "mstrl_XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX_XXXXXX",
    "Studio": "mstrl_XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX_XXXXXX"
  },
  "sid": "DrhSdpgE"
}
```

---

## 架构

```
signup/
├── cli.py                    入口 (run / batch / test-proxy / test-mail)
├── runner.py                 主编排（单 context 内串完全流程）
├── accounts.txt              "邮箱----接码链接"
├── data/accounts.json        结果落库
└── core/
    ├── chain_gateway.py      链式代理网关（SOCKS5服务 + 健康检查）
    ├── proxy_pool.py         代理池（粘性会话 + 失败冷却）
    ├── fingerprint.py        设备画像（UA/CH/语言/时区/WebGL/网络）
    ├── browser.py            Playwright 实例（注入指纹 + 拟人交互）
    ├── kratos.py             Kratos HTTP 流程（备用，非主路径）
    ├── mailbox.py            iCloud HME 接码
    ├── http.py               curl_cffi 客户端（备用）
    └── subscribe.py          订阅 + 取消订阅
```

## 关键实现要点

### 1. 链式代理（必须的两层）

```python
from core.chain_gateway import ChainGateway
gw = ChainGateway(
    pre_proxy="socks5h://127.0.0.1:10808",        # 第一跳：本机 sing-box
    target_proxy="http://user:pass@us-eu.zooproxy.com:5000")  # 第二跳：住宅
port = gw.start()      # 本机 SOCKS5 端口
# 之后所有客户端都用 socks5://127.0.0.1:{port}
```

> zooproxy 端点是 **HTTP 代理**，不是 SOCKS5（SCOKS5 会报 `407 Proxy Authentication`）。

### 2. 必须用真实浏览器

实测 `curl` 的 TLS 指纹：
```
ja3 = 375c6162a492dfbf2795909110ce8424
ja4 = t13d4907h2_0d8feac7bc37_7395dae3b2f3
```
与 Chrome 差异巨大，Cloudflare 直接识别。
→ **只改 UA / 只加 header 不够**，必须 Playwright + 真实 Chromium。

### 3. 指纹必须整套自洽

```python
from core.fingerprint import build_profile
p = build_profile("user@icloud.com", "CA")   # 同一邮箱永远同一画像
p.headers()      # 完整 12 个 sec-ch-ua-* + Accept-Language
p.console_js()   # 注入：userAgentData / languages / timezone / screen / WebGL
```

**避免的坑**（实测）：
- ❌ IP 在 CA，浏览器 `zh-CN` → Axept 上报 `countryCode=CA, userLanguage=zh-CN`
- ❌ `downlink=1.45 / rtt=350` → 代理链路特征
- ❌ 账单地址 US 但 IP 在 CA

### 4. 验证码的坑

```python
# 每次点"Send me a code by email"都会【作废旧码】
# 必须先取基线 id 集合，只认之后新到达的邮件
baseline = {e["id"] for e in old_emails}
code = wait_new_email_not_in(baseline, from_domain="mistral")
```

### 5. 会话必须同一 context

```python
# ❌ 错误：注册一个 context，生成 Key 另一个 context -> 302 到登录页
# ✅ 正确：注册 -> 验证 -> 登录 -> 生成 Key 全在同一 context
with SignupBrowser(profile, local_proxy, opts) as sb:
    self._register(acct, sb)
    self._verify(acct, sb)
    self._create_keys(acct, sb)
```

### 6. Key 生成是 REST 不是 tRPC

```http
POST https://admin.mistral.ai/api/billing/api-keys
Content-Type: application/json

{"name":"api-1234","workspace_uuid":"<uuid>",
 "primitive_access_scope":"personal_and_shared","product":"API"}
```
product: `API` | `Vibe` | `Studio`
返回明文 `mstrl_...`，**只此一次**。

### 7. 取消订阅唯一入口

```http
POST /api/local-trpc/billing.subscriptions.cancelUnsubscription
{"json":{"planType":"API"}}
```
planType 合法值：`API` / `CHAT` / `ON_PREMISE` / `MISTRAL_CODE`
无订阅时返回 400 `"No subscription in cancellation"`（幂等可当成功处理）

### 8. Stripe 卡字段

```
iframe[title='Secure payment input frame']
  input[autocomplete='cc-number']   卡号
  input[autocomplete='cc-exp']      有效期 "MM / YY"
  input[autocomplete='cc-csc']      CVC
  input[autocomplete='billing postal-code']  邮编
```
订阅页 **ToS 复选框是隐藏 input，要点 label 文本**。

---

## 用法

```bash
cd tools/mistral/signup

# 测试代理池
python3 cli.py test-proxy

# 测试接码
python3 cli.py test-mail --email X@icloud.com --mailbox "https://icloud.mida.vip/messages/..."

# 单账号全流程
python3 cli.py run --email X@icloud.com --mailbox "https://..." --headless

# 批量
python3 cli.py batch --accounts accounts.txt --limit 5
```

## 批量注意事项

| 项 | 规则 |
|---|---|
| 代理 | 一账号一 sid，用完不换（粘性会话） |
| 指纹 | 由邮箱 hash 派生，天然唯一且可复现 |
| 卡号 | **绝不可跨账号复用**（Stripe 会聚类） |
| cf_clearance | 该 IP 绑定，换 IP 立即失效 |
| 间隔 | 注册与订阅之间 ≥3 分钟（真人节奏） |
| 邮箱格式 | HME 别名占比过高是批量特征，建议混入其它域 |

## 已知限制

1. 代理池健康波动：10 个 sid 同时只有 5-7 个可用，代码已实现健康检查+故障转移
2. Stripe iframe 偶发加载失败（代理抖动），需重试
3. 订阅最终提交（ToS 勾选 → Subscribe）未完整验证，其余步骤均已走通
