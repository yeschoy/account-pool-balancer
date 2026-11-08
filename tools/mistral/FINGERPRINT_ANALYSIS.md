# Mistral 环境识别分析（任务1产出）

> 基于 2026-11-07 真实注册+订阅会话（1789 请求）逐层拆解。
> 目标：找出所有"检测批量注册"的信号面，并给出注册机的伪装要求。

---

## 一、识别面清单（按危险度排序）

### 🔴 P0 — 任意一项不一致即判定为机器

#### 1. UA 与 Client Hints 必须整体自洽
浏览器会同时发出 12 个相关字段：

```
User-Agent: Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36
sec-ch-ua: "Not:A-Brand";v="8", "Chromium";v="156", "Google Chrome";v="156"
sec-ch-ua-full-version: "156.0.8078.80"
sec-ch-ua-full-version-list: "Not:A-Brand";v="8.0.0.0", "Chromium";v="156.0.8078.80", "Google Chrome";v="156.0.8078.80"
sec-ch-ua-platform: "macOS"
sec-ch-ua-platform-version: "15.7.7"
sec-ch-ua-arch: "arm"
sec-ch-ua-bitness: "64"
sec-ch-ua-mobile: ?0
sec-ch-ua-model: ""
sec-ch-ua-wow64: ?0
sec-ch-ua-form-factors: "Desktop"
```

**伪造要点**：
- 这是**强绑定组合**。MAC 上说不通 `arch=x86` + `platform-version=10.15.7`（UA 里的 10_15_7 是 mac 的冻结尾号，不是真实系统版本）
- `sec-ch-ua-platform-version: "15.7.7"` 才是真系统版本，必须与 `navigator.userAgentData.platformVersion` 一致
- `sec-ch-ua-model` 在桌面端恒为 `""`，若填值立刻暴露
- `Not:A-Brand` 的版本号（`v="8"`）随 Chrome 版本变化，不能硬编码

**注册机做法**：从真实 Chrome 采集后整体替换，12 个字段 + UA + `navigator.userAgentData` 一次性同步，绝不允许只改 UA。

#### ⚠️ 实测踩坑：Playwright 会泄露 `HeadlessChrome`

Playwright 的 Chromium 默认发出：
```
sec-ch-ua: "Not:A-Brand";v="99", "HeadlessChrome";v="145", "Chromium";v="145"
                          ^^^^^^^^^^^^^^ 品牌直接暴露
                                              v="99" 是非发行版标记
```

更坑的是：`add_init_script` 只能改 **JS 侧** `navigator.userAgentData`，
**改不了真实 HTTP 头**。结果就是 JS 检查通过、但网络层被抓。

**唯一正确做法**：CDP `Network.setExtraHTTPHeaders` 强制覆盖。

```python
cdp = context.new_cdp_session(page)
cdp.send("Network.enable")
cdp.send("Network.setExtraHTTPHeaders", {"headers": {
    "sec-ch-ua": '"Not:A-Brand";v="8", "Chromium";v="156", "Google Chrome";v="156"',
    "sec-ch-ua-platform": '"macOS"',
    "sec-ch-ua-arch": '"arm"',
    # ... 全套 12 个
}})
```

验证方式：`https://httpbin.org/headers` 看实际发出的头。

#### 2. IP 地理位置 vs 浏览器语言 ⚠️ **实测已踩坑**
抓包里 Axept 上报：
```json
"geolocation": {"countryCode":"CA","subdivisionCode":"QC"},
"userLanguage": "zh-CN"          ← 矛盾！
```
代理出口在加拿大，浏览器语言却是简体中文。**这是最容易被抓的点**。

**注册机要求**：
- `Accept-Language` 必须与代理出口国匹配（CA → `en-CA,en;q=0.9`）
- `navigator.language` / `navigator.languages` / `Intl.DateTimeFormat().resolvedOptions().locale` 三者一致
- `Intl.DateTimeFormat().resolvedOptions().timeZone` 必须是出口国时区（CA → `America/Toronto` 或 `America/Vancouver`）
- 时区不能用 UTC 兜底

#### 3. 网络质量头异常
```
downlink: 1.45    ← Mbps
rtt: 350          ← ms
```
真实住宅网络通常 `downlink 5~20`、`rtt 40~150`。
代理/机房链路会把 downlink 压低、rtt 拉高。**这批值本身就偏可疑**。

**注册机要求**：住宅代理尽量选低延迟节点；若无法控制，则接受此风险但不要额外叠加（比如同时开 VPN）。

---

### 🟠 P1 — 行为与设备指纹

#### 4. Axept 行为检测
`static.axept.io/behavior-detection.min.js` — 采集鼠标轨迹、点击、滚动、键盘节奏。
上报到 `api.axept.io/v1/analytics/evts`，事件类型：
`pageview` / `cookies:loaded` / `cookies:open` / `pagevisible` / `pagehide` / `history:pathnamechanged`

**风险**：纯 HTTP 客户端复刻完全不产生这些事件，Axept 会看到"零交互的注册"。
**缓解**：若走浏览器自动化（Playwright/真实 Chrome），需模拟真实鼠标移动轨迹（贝塞尔曲线，非直线瞬移）与有停顿的输入。

#### 5. Cloudflare
- `cf_clearance`：绑定 UA + IP + TLS 指纹的组合。**换 IP 后 clearance 立即失效**。
- Turnstile sitekey：`0x4AAAAAAAVBu8-ohcZ6vwPe`
- 触发点：`POST /cdn-cgi/challenge-platform/h/b/c/<ray>`

**注册机要求**：每个账号必须用**独立 IP + 独立 profile**，不能复用 `cf_clearance`。

#### 6. Stripe 支付指纹
- `js.stripe.com/v3/fingerprinted/js/trusted-types-checker-*`
- `hcaptcha-invisible-*.html`（不可见人机）
- `payment-request-inner-google-pay-*`

**风险**：Stripe 会关联 **同一卡号 + 不同账号**。这是批量订阅的**致命点**——只要卡号复用，账号团伙立即被聚类。

#### 7. Next.js RSC 头
```
rsc: 1
next-url: /
next-router-state-tree: <编码的 router 状态>
next-router-prefetch: 1
```
HTTP 复刻若缺失或格式错误 → 明显不是真实 Next.js 客户端。

---

### 🟡 P2 — 弱信号

| 信号 | 说明 |
|---|---|
| `x-kong-proxy-latency` | Kong 网关，响应头会随地域变化 |
| `mistral-correlation-id` | 每个请求唯一，正常 |
| `baggage` / `sentry-trace` | Sentry 追踪，正常浏览器也有 |
| favicon / woff2 请求 | 真实浏览器会加载，纯 HTTP 客户端不会 |
| `usaddressgen.com` | 用户自己在页面里开的地址生成器，**不是自动行为** |

---

## 二、Cookie 与鉴权模型

```
.mistral.ai              ory_session_<project_hash>      主会话（90天）
.mistral.ai              cf_clearance / __cf_bm / _cfuvid
.mistral.ai              csrf_token_<project_hash>
auth.mistral.ai          __Host-ory_kratos_browser_<hash>
admin.mistral.ai         anonymousUser / browser-session-id / csrftoken
```

**关键**：
- `admin.mistral.ai` 上是 `anonymousUser`，**真实身份不在这个域**
- 身份靠父域 `.mistral.ai` 的 `ory_session` 传给 tRPC
- 所以：**只要能拿到 `ory_session` + `cf_clearance`，纯 HTTP 就能操作 admin 全部接口**（已实测成立）

**这大幅简化注册机**：注册走 HTTP，Key 生成和订阅也走 HTTP，不需要浏览器。唯一的浏览器依赖是 Cloudflare Turnstile（若被触发）。

---

## 三、注册机伪装要求（结论）

| 维度 | 要求 | 固定/随机 |
|---|---|---|
| UA + 12 个 Client Hints | 整体一致，与真实 Chrome 完全匹配 | **固定**（每个 profile 固定一套） |
| Accept-Language | 匹配代理出口国 | 随 IP 变化 |
| 时区 / locale | 匹配出口国 | 随 IP 变化 |
| 屏幕分辨率 / devicePixelRatio | 合理值 | 固定 |
| Canvas / WebGL / Audio 指纹 | 噪声化但稳定 | **固定** |
| 代理 | 每账号独立住宅 IP | 随机 |
| cf_clearance | 每 IP 独立获取 | 不可复用 |
| 银行卡 | **绝不可跨账号复用** | 唯一 |

**核心矛盾**：要"固定指纹"以显得像同一个真实用户，但又要"随机 UA"避免批量特征。
解决办法 —— **每个账号固定一套完整画像**（UA 组 + 指纹 + 时区），账号之间不同，账号内部绝对一致。

---

## 四、实测发现的破绽（本次会话）

1. ✅ IP 在 CA，浏览器语言 `zh-CN` —— **已暴露**
2. ✅ `downlink=1.45 / rtt=350` —— 代理链路特征明显
3. ✅ 账单地址 `Oregon / Medford / 709 N Central Ave` 但 IP 在 `QC Canada` —— **地址国与 IP 国不一致**
4. ✅ 邮箱为 iCloud HME 别名（`@icloud.com` 随机词组合），这是**批量账户的典型特征**

> 第 4 点最难规避：iCloud Hide My Email 生成的别名格式高度规则
> （`形容词-名词.数字@icloud.com`、`单词_单词.数字@icloud.com`），
> 与真人自建邮箱差异明显。Mistral 若做域名+格式统计，这类账号极易聚类。

---

## 五、检测对抗建议

1. **地址必须与 IP 同国**（CA 代理 → CA 地址，不是 US）
2. **语言/时区必须与 IP 同国**
3. **每账号独立 cf_clearance**
4. **卡号不复用**
5. **邮箱格式尽量打散**（不要全是 HME 别名；混合 Gmail/Outlook/自建域，或至少控制 HME 占比）
6. **行为模拟**：浏览器路径需真实鼠标轨迹；纯 HTTP 路径需在关键步骤间加随机延迟（0.8~4s）
7. **注册与订阅之间不要过快**（实测真人间隔约 5 分钟）
