# 纯协议 / 混合注册机（任务2 补充）

> 目标：尽可能走协议而非 UI，提升速度与稳定性。
> 实测日期：2026-11-08

## 一、域能力矩阵（决定性结论）

| 域名 | 纯协议 | 原因 |
|---|---|---|
| `auth.mistral.ai` | ✅ 可用 | 无严格 CF 挑战 |
| `admin.mistral.ai` | ✅ 可用 | 同上 |
| `console.mistral.ai` | ❌ 403 | CF 校验 JA3 |
| `chat.mistral.ai` | ❌ 403 | CF 校验 JA3 |

**403 现场**：
```html
<title>Just a moment...</title>
```

**已排除的方案**：
- ❌ 浏览器先访问拿 `__cf_bm` 再传给 curl —— 仍 403
- ❌ 换 curl_cffi impersonate=chrome —— 本机 TLS 库冲突；
  且即便能跑，JA3 仍非真 Chrome
- ❌ 加 `sec-ch-ua` 全套头 —— 无效，CF 看的是 TLS 握手

**唯一可行**：console / chat 的动作必须在浏览器里 `page.evaluate` 执行。
→ 混合方案（`core/hybrid.py`）

---

## 二、协议侧完整流程（auth + admin）

### 1. 注册
```
GET  /self-service/registration/browser
     ?return_to=https://admin.mistral.ai/subscriptions/upgrade/plans
     &after_verification_return_to=<同上>
POST /self-service/registration?flow=<id>
     {"csrf_token","method":"password","password",
      "traits":{"email","name":{"first","last"}},"transient_payload":{}}
→ session（90 天）
```

### 2. 发验证码 ⚠️ 三个坑
```
GET  /self-service/verification/browser
     ↑ 会 303，必须跟随重定向，否则拿到空 body
POST /self-service/verification?flow=<id>
     {"csrf_token","email","method":"code","transient_payload":{}}
     ↑ **响应体本身就是可用 flow**
```

**坑位清单**：

| 坑 | 症状 | 正解 |
|---|---|---|
| GET 不跟随 303 | flow 解析失败 | `curl -L`，且 GET 时不加 `-X` |
| POST 后再 GET 一次 | `choose_method`（flow 被重置） | **直接用 POST 响应体** |
| 用旧 csrf | `4010006 Could not find a strategy` | csrf 每次请求都轮换 |

### 3. 提交验证码
```
POST /self-service/verification?flow=<同一个 id>
     {"code":"123456","csrf_token":"<POST 返回的新 csrf>","method":"code",...}
→ {"state":"passed_challenge"}
```

### 4. 登录（拿 admin 会话）
浏览器走一次 `/login`：邮箱 → Continue → 密码 → **Continue with password**
（注意按钮文案，账号已存在时是 "Continue with password"）

### 5. admin 域全部接口
```
GET  /api/local-trpc/user.meSession          → org + workspace UUID
GET  /api/local-trpc/billing.info            → 账单地址 / is_billing_setup
GET  /api/local-trpc/billing.subscriptions.get?input={"json":{"planType":"API"}}
POST /api/local-trpc/billing.updateInfo      → 保存账单地址
POST /api/local-trpc/billing.createPaymentMethodSetupIntent
POST /api/local-trpc/billing.subscribeLeChat
POST /api/local-trpc/billing.subscriptions.cancelUnsubscription
POST /api/billing/api-keys                   → 三把 Key（REST，非 tRPC）
GET  /api/billing/api-keys
DELETE /api/billing/api-keys/{id}
```

**planType 合法值**：`API` / `CHAT` / `ON_PREMISE` / `MISTRAL_CODE`

---

## 三、混合侧（console + chat，必须浏览器）

| 动作 | 域名 | 接口 |
|---|---|---|
| 接受 Studio ToS | console | `POST /api-ui/trpc/user.acceptTos?batch=1` 无参 |
| 接受 Chat/Vibe ToS | chat | `POST /api/trpc/user.acceptToS?batch=1` `{"newsletter":false}` |
| 隐私三开关 | chat | `setMessageTrainingUse` / `setKnowledgeEnabled` / `setLocalisationConsent` |
| Vibe CLI Key | chat | `POST /api/code-trpc/apiKey.generateApiKey?batch=1` |

### Vibe CLI Key 的两个前置条件（重要）
1. 已接受 Vibe ToS
2. **`is_billing_setup == true`**（账号有过支付方式）

> 实测：`is_billing_setup=false` 时，该接口固定 400 `Bad request`，
> 与 ToS、页面路径、请求体写法均无关。
> 这也是为什么它和订阅强耦合——必须先配卡。

---

## 四、aude 前缀总表（最容易记错的部分）

| 前缀 | 用途 | 示例 |
|---|---|---|
| `/api/local-trpc/` | admin 域 tRPC（旧） | `billing.subscriptions.get` |
| `/api/trpc/` | admin + chat 域 tRPC（新） | `user.acceptToS` |
| `/api-ui/trpc/` | console 域 tRPC | `user.acceptTos` |
| `/api/code-trpc/` | chat 域 Vibe Code 专用 | `apiKey.generateApiKey` |
| `/api/billing/` | admin 域纯 REST | `api-keys` |
| `/api/users/`, `/api/workspaces/` | admin 域纯 REST | `principals/{id}` |

---

## 五、代码结构

```
signup/
├── core/protocol.py      纯协议：Kratos + admin + console + chat 封装
├── core/hybrid.py        混合：浏览器过 CF + 协议跑业务
├── protocol_cli.py       纯协议 CLI
├── cli.py                UI 版 CLI（旧，仍可用）
└── runner.py             UI 版编排
```

### 用法

```python
from core.hybrid import HybridRunner

runner = HybridRunner(headless=True).debug(True)
r = runner.run(
    email="user@icloud.com",
    mailbox_url="https://icloud.mida.vip/messages/...",
    password="Passw0rd!",
    first="Mia", last="Anderson",
)
print(r.keys)         # {'API': 'mstrl_...', 'Vibe': 'mstrl_...', 'Studio': 'mstrl_...'}
print(r.vibe_cli_key) # mstrl_...（需 is_billing_setup=true）
```

### 实测耗时
| 路径 | 耗时 |
|---|---|
| 全浏览器 | 95–140s |
| **混合（推荐）** | **55–70s** |
| 全协议 | ~25s（但 console/chat 不可用） |

---

## 六、还需补齐的

1. **订阅的最终提交**：「勾 ToS → Subscribe」这一步未完整验证
   （卡已能填、Submit 能点、ToS checkbox 是隐藏 input 需点 label）
2. **取消订阅成功路径**：接口确认，但没有有效订阅可验证成功返回
3. **Vibe CLI Key 的完整链路**：需要先订阅（`is_billing_setup=true`）才能测
