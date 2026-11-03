# perchai-cli@2.4.120 — 5h / 7d 滚动用量窗口分析

**分析对象**：`/Users/lyh_god/.local/lib/node_modules/perchai-cli/dist/perch.mjs`
**SHA-256**：`fd246c8c63b03281244296edb7f00a6984069f325642896cdef35fb54bf06174`（12,841,681 字节）
**版本**：`package.json` → `perchai-cli@2.4.120`

## 证据等级图例

- **【观测】** — 直接读自 bundle 字节的代码事实，给出偏移
- **【推断】** — 从代码结构/命名/常量推出的结论，不是字面断言
- **【未知】** — 客户端代码中无证据；可能属于服务端 SQL/RLS，不可推测

> **重要范围限制**：本报告只覆盖 **客户端** 行为。`perch_ai_my_inference_usage_meter`
> 返回的数值（`window5hUsd` 等）由服务端计算，其 SQL 定义**不在**这个 bundle 里，属于【未知】。
> REA 语义图分析在 8 GiB 堆上限处耗尽（见 `limitations`），因此主要证据来自字节级定向读取。

---

## 0. 先纠正一个前提：客户端从不信任 `roostRolling` 做闸门

这是本次分析最重要的发现，直接决定你的架构。

**【观测】** 客户端侧只有 **三处** 消费 `roostRolling`，全部是展示/提示，**没有一处**参与准入判断：

| 偏移 | 符号 | 用途 |
|---|---|---|
| `8696947` (L10503) | `Prr()` | 纯解析器：把 snake_case `roost_rolling` 映射成 camelCase，做类型强制 |
| `8701690` (L10503) | `uRt()` | 生成 `/usage` 面板的行数据（`usagePercent`、`nextFreedAt`） |
| `8957937` (L10552) | `Ilr()` | Ultra 周用量 50%/80% 警告横幅 |

**【观测】** 真正的准入闸门 `e6t()`（偏移 `1293774`，L130）走的是**完全不同的一组字段**，由服务端 RPC
重新聚合出来的用量值：`starter5hUsd` / `starterWeeklyUsd` / `roost5hUsd` / `roostWeeklyUsd` /
`manual5hUsd` / `manualWeeklyUsd`，配合上限 `*CapUsd`。

**【观测】** 二者的连接点只有字符串同名。`e6t` 的入参对象 `fe` 在偏移 `1308900`（L130）处构造，
里面**没有** `window5hUsd` / `window5hNextFreedAt` 这类字段。

**结论**：`roostRolling` 是**展示层快照**。服务端真正执行闸门时用的是它自己重新算的数字。
这两者可能一致，但**客户端代码不能证明它们永远一致**。这对你非常关键——见第 6 节的建议。

---

## 1. 窗口语义

### 1.1 整体重置 还是 滑动回血？—— 滑动，逐步回血

**【观测】** 多处文案直接写明"逐渐":

- 偏移 `86082` 附近：`freesUp: "Usage begins freeing {when}"`
- 偏移 `86597`：`weeklyWarning.roost: "You've used {percent}% of this week's Ultra allowance for Roost. It frees up gradually from {date}."`
- 偏移 `81585`：`starterFiveHour.rolling: "…It frees up on a rolling 5-hour window."`
- 偏移 `83700` 附近：`stepsRunning.fiveHour: "Steps still running are using the rest of your 5-hour limit."`

**【观测】** 服务端窗口常量的名字也说明是滚动窗口：
偏移 `1297xxx` 处 `GH=300*60*1e3`（=5 小时毫秒）、`QH=10080*60*1e3`（=7 天毫秒），
配套字段名是 `since5h` / `since7d`（偏移 `1309910`、`1313578` 等），即"从某个起点到现在"的滚动区间。

**【观测】** 关键机制在 `l6t()`（偏移 `1299967`，L130）：它按 `created_at` 升序遍历该窗口内的消费行，
**累加到 `cap - 1e-9` 就停**，然后把那一行的 `created_at + windowMs` 作为释放时刻：

```js
for (const f of rows) {
  u = f.createdAt, c += lUr(band, f, r);
  if (c >= o - aUr) { d = new Date(f.createdAt.getTime() + t.windowMs); break; }   // aUr = 1e-9
}
d ??= u ? new Date(u.getTime() + t.windowMs) : null;
```

**【推断】** 这解释了你观察到的一切：

- 已用额度**不是**到点归零，而是**逐条**到期释放。每笔消费满 5 小时就自然滑出窗口。
- 所以**不会**在 `NextFreedAt` 时刻出现从 1.297 掉到 0 的台阶，而是 5 小时内持续缓慢下降。
- `nextFreedAt` 的真实含义是"**再等多久才刚好腾出足以继续的额度**"，而不是"窗口重置时刻"。

### 1.2 两个时间点相差 ~57 分钟 的含义

你的样本（同一时刻读取）：

```
window5hNextFreedAt : 2026-11-02T19:53:22.913Z
window7dNextFreedAt : 2026-11-08T18:56:42.800Z
```

**【推断】** 按 1.1 的算法，两者本质上是**同一笔（或同一批）最早的消费行** `created_at` 分别加上
5h 和 7d 的结果。因此：

```
7d_next - 5h_next = (created_at + 7d) - (created_at + 5h) = 2 天（常数）
```

算一下你的样本：

- `2026-11-08T18:56:42.800Z` − `2026-11-02T19:53:22.913Z` = **5 天 22 小时 57 分**，不是 2 天。

**【推断】** 这否证了"同一笔消费行"的假设。更自洽的解释是：两个窗口**各自独立**挑到了不同的临界行。

- 5h 窗口的临界行较晚 → `19:53:22`
- 7d 窗口的临界行更早 → 差了约 57 分钟

两者不是同一行，所以差值不是常数，**不能**用固定偏移从一个推算另一个。

**【未知】** 具体是哪两笔消费、以及服务端为何选不同的临界行，需要服务端 SQL 才能确定。

> **实践含义**：**`window7dNextFreedAt` 不能由 `window5hNextFreedAt` 推导**。
> 你的样本里 5h 额度已用到 99.78%（1.297/1.3），7d 才 32%（1.2978/4），
> 说明 5h 是当前真正的瓶颈。判定"什么时候能再用这个号"必须**以 5h 为准**。

### 1.3 单位、精度、比较阈值

**【观测】** 单位是 **USD（浮点美元）**。字段名带 `Usd`，上限来自环境变量
`PERCH_STARTER_5H_CAP_USD` 等（偏移 `1291155` 起，`Xjt()` @ `1290978`）。

**【观测】** 内部精度是**美元的最小单位 0.001**（即 1 PT = $0.001）。证据：

- 偏移 `8700770` 附近 `$Le(e){return Math.round(e*1e3)}` — 展示层把 USD 换成"千分之一美元"
- 偏移 `8697xxx` `Dme(e){return ... Math.ceil(e/.001)}` — 同口径
- 偏移 `1296886` `_we(cap, used){return Math.max(0, Math.floor((cap-used)*1e3))}` — **余量向下取整**

所以 `1.3` 就是 1300 PT，`1.2971128200000002` 是 1297 PT（**注意 `window5hUsd` 本身是未取整的原始浮点**）。

**【观测】** 比较运算符是 **`>=`**，不是 `>`。六个比较点全部确认（见 `e6t` @ `1293774`）：

```js
1293951  starterWeeklyUsd >= o   → ZO("user_starter_weekly")
1294091  starter5hUsd     >= a   → ZO("user_starter_5h")
1294346  roostWeeklyUsd   >= a   → ZO("user_roost_weekly")
1294488  roost5hUsd       >= s   → ZO("user_roost_5h")
1294665  manualWeeklyUsd  >= o   → ZO("user_manual_weekly")
1294801  manual5hUsd      >= a   → ZO("user_manual_5h")
```

**【观测】** 而且检查顺序是 **周限在前、5h 在后**：

```js
// starter 分支
if (starterWeeklyUsd >= starterWeeklyCapUsd) return ZO("user_starter_weekly", ...);
if (starter5hUsd     >= starter5hCapUsd)     return ZO("user_starter_5h", ...);
```

**【推断】** 所以 7d 已满时，即使 5h 还有量，你也只会收到 `weekly` 类的 `limitKind`。
如果你的池子只读 5h，会**误判为可用**。这是一个真实的坑——见第 4 节。

### 1.4 已用额度会不会因"下次释放"立刻下降？

**【观测】** 不会。`window5hUsd` 是服务端对该时刻的求和结果；客户端 `Prr()` 只是原样接收。
客户端**没有任何**在本地预扣/预测释放的代码。

**【推断】** 按 1.1 的逐条释放模型，`window5hUsd` 会在 5 小时内**单调下降**（只要你不继续消费），
每次下降是某笔旧消费滑出窗口的量。它不会在 `nextFreedAt` 处发生突变。

---

## 2. perchai-cli 如何使用

### 2.1 有没有消费？—— 有，但只用于展示

见第 0 节。【观测】消费点为 `Prr()` / `uRt()` / `Ilr()`，全部展示或提示。

### 2.2 客户端侧主动闸门？—— **没有**

**【观测】** 我逐处检查了所有 `roostRolling` 引用（共 15 处命中，全部列出）：没有一处返回值参与
"是否允许发起请求"的判断。`uRt()` 唯一的门槛是 `if (!e?.enabled) return []`（偏移 `8702650` 附近），
即"数据没启用就不画这一行"，不是"超了就拦"。

**【观测】** 客户端**从不**把 `window5hUsd >= window5hCapUsd` 作为本地拦截条件。
它总是把请求发出去，然后解析服务端返回的错误。

**【推断】** 这是一个刻意的架构选择：客户端本地没有可信的用量视图
（`roostRolling` 是快照、可能过期），所以把判定权完全交给服务端。

### 2.3 窗口耗尽时 CLI 的正确反应

**【观测】** 走 `yC` 异常 + `limit` 对象。服务端闸门 `e6t()` 不通过时返回 `ZO()`，
再被抛成带 `limit` 的错误。`limit` 对象的 schema 在 `j3()`（偏移 `90855`，L5）里定义：

```js
{
  errorCode:   "usage_limit_reached" | "promo_overflow_decision" | "metering_unavailable" | ...,
  limitKind:   "five_hour" | "weekly" | "monthly" | "manual_monthly" | "steps_running" | ...,
  resetAt:     string | null,   // ISO 时间戳
  plan:        "starter" | "pro" | ... | null,
  allowancePt / allowanceUsd, offerCredits, offerUltra/upgradePlan,
  heldLimit, includedTiers, upgradeTiers, modelName, customMessage, url, minVersion, command
}
```

**【观测】** `limitKind` 的完整枚举在 `d_r`（偏移 `91800` 附近）与 `XLr()`（偏移 `1292402`）：

```
user_roost_5h    → limitKind "roost_five_hour"     (variant=promo_pin 时为 "promo_five_hour")
user_roost_week  → "roost_weekly"                  (promo_pin → "promo_weekly")
user_starter_5h  → "starter_five_hour"
user_starter_week→ "starter_weekly"
user_manual_5h   → "manual_five_hour"
user_manual_week → "manual_weekly"
user_monthly     → "monthly"
user_monthly_manual → "manual_monthly"
```

**【观测】** `resetAt` 的填充条件在 `Swe()`（偏移 `1293151`）：

```js
resetAt: JLr(e) && t.resetAt ? t.resetAt.toISOString() : null,
// JLr(e) = /_(5h|weekly)$/.test(e)
```

即**只有 5h / weekly 类错误才带 `resetAt`**；monthly 类是 `null`。

**【观测】** `resetAt` 的来源正是第 1.1 节的 `l6t()`：`bUr()`（偏移 `1313190`）把
`windowMs: GH`(5h) 或 `QH`(7d) 传进去，`l6t` 算出 `created_at + windowMs`。
**所以 `resetAt` 在语义上等价于你的 `nextFreedAt`。**

**正确反应分支**（【观测】+【推断】）：

| 情形 | 客户端行为 | 你应该怎么做 |
|---|---|---|
| `limitKind` ∈ {`roost_five_hour`,`starter_five_hour`,`manual_five_hour`} | 带上 `limit.resetAt` 抛错 | 冷却到 `resetAt`，**不要**切换 tier |
| `limitKind` ∈ {`roost_weekly`,`starter_weekly`,`manual_weekly`} | 同上，带 `resetAt` | 冷却到 `resetAt`，通常很久（最长 7d） |
| `limitKind` ∈ {`monthly`,`manual_monthly`} | 带 `allowancePt/allowanceUsd`，`resetAt=null` | **不要**重试；等账单周期（无时间戳可依） |
| `limitKind = "steps_running"` | 带 `heldLimit`，`resetAt=null` | 短退避重试（并发占用，不是配额） |
| `limitKind = "usage_unverified"` | `resetAt=null` | 同 steps_running，短退避 |

**【观测】** `steps_running` / `usage_unverified` 的构造在 `o6t()`（偏移 `1297385`）与
`1313058` 处，二者都**不带** `resetAt`——这是和真配额耗尽的**结构性区别**。

**【观测】** 关于"切换 roost tier"：`Swe()` 里有个 `upgradePlan:"ultra"` 的提示分支，
但条件是 `qqe()`（偏移 `1294xxx`）：`planCode !== "pro"` 直接返回 false，且 `user_monthly*` 类
不参与。它只是**展示一个升级链接**，不是自动切 tier。

**【推断】** 客户端**没有**自动切换额度档位的逻辑。所谓"换 lane"（`billingLane` ∈ roost/manual）
是服务端在请求携带 `lane` 时选择的，客户端不会因为耗尽而自动换。

### 2.4 2.4.120 vs 2.4.121

**【未知】** 本机只有 `2.4.120` 一份：`~/.local/lib/node_modules/perchai-cli` 是唯一副本，
npm cache 里没有其它版本，也没有 `_npx` 副本。**我无法做版本差异对比**，不做推测。

如果你需要这一项，请把 2.4.121 的 tarball 放到本地（不要从网络拉），我再用
`compare_application_versions` 做 digest 级对比。

---

## 3. 调用这个接口的注意事项

### 3.1 轮询频率 / 缓存 TTL

**【观测】** CLI **没有**定时轮询这个 RPC。`Brr()`（偏移 `8706317`）只有一个调用点
（偏移 `8711647`），在 `/usage` slash 命令的一次性处理流程里（`jrr()` 内）。

**【观测】** CLI 的**主路径其实不用这个 RPC**。`getUsageMeter()`（在 `Mrr` 里，偏移 `8699929`）
有一个平台分支：

```js
typeof window < "u"
  ? fetch(`/api/perch-terminal/usage-meter?${params}`)      // 浏览器/桌面
  : e.rpc("perch_ai_my_inference_usage_meter", {...})       // Node CLI
```

**【观测】** 另一条读用量路径是 `GET /api/perchai/account`（偏移 `8707417`、`12643378`、`12645345`），
返回 `{ok, session, usageMeter, creditBalancePt}`，**其中就带 `usageMeter`**。

**【观测】** 这条路径有客户端硬节流：偏移 `12832058` `LNi = 6e4`（**60 秒**），
配合 `_vr` 时间戳在 `COt()` 里做"60 秒内不重复请求"。另外每次请求 `AbortController` 超时 **5 秒**
（偏移 `12642444` 附近 `setTimeout(()=>r.abort(),5e3)`）。

**小结：客户端对齐的取值是 ≥ 60 秒。** 你的 `quotaIntervalSec = 600` 比 CLI 自己的下限
宽松 10 倍。**【推断】** 600 秒对"账号池每号刷一次"是偏保守、安全的；
但对"5h 只剩 3 分钟就要回血"的场景，最多会浪费 10 分钟。见第 6 节建议。

### 3.2 会不会消耗配额？

**【推断】不会。** 证据：

- 它是 PostgREST `rpc()` 调用（`POST .../rpc/<fn>`），不是 model-call 路径
- 对比：真正扣量的是 `perch_ai_reserve_usage_pt` / `perch_ai_settle_usage_reservation`
  （偏移 `1300600` 附近的 `Ewe`），那是**另一组** RPC，带 `p_reserve_pt` 参数
- `my_inference_usage_meter` 的入参只有 `p_day_start` / `p_week_start`，没有任何 reserve 参数

**【未知】** 服务端是否给它写审计日志/触发计费事件，代码里看不到。

### 3.3 `p_day_start` / `p_week_start` 语义与格式

**【观测】** 两个函数的定义就在 bundle 里（偏移 `8697xxx`，紧邻 `Prr`）：

```js
function I_i(){ let e = new Date; e.setHours(0,0,0,0); return e.toISOString(); }         // p_day_start
function R_i(){ let e = new Date; e.setDate(e.getDate()-7); return e.toISOString(); }   // p_week_start
function P_i(){ let e = new Date; e.setDate(e.getDate()-31); return e.toISOString(); }  // monthly 用
```

**关键点**：

- 格式：`Date.toISOString()` → `"2026-11-03T00:00:00.000Z"` 这种 **带毫秒、带 Z** 的 ISO-8601 UTC
- `p_day_start`：**本地时区当天 0 点**，再转成 UTC 字符串（不是"UTC 当天 0 点"！）
- `p_week_start`：`now - 7 天` 的同一时刻，**不是**"7 天前的 0 点"，也**不是**周一

**【推断】** 你的实现用"今天 0 点 UTC"，与 CLI 在非 UTC 时区下**不一致**。
在 `Asia/Shanghai`（UTC+8）下，CLI 会传前一天 `16:00:00.000Z`。差 8 小时。

**【推断】** 对本问题的**实际影响很小**：`roostRolling` 是服务端自己按滚动窗口算的，
很可能根本不依赖这两个参数（它们是给 `dailyPt`/`weeklyPt` 这类日历桶用的）。
但这只是推断——**【未知】** 服务端 `roost_rolling` 是否复用 `p_*` 参数。

> **建议**：既然你要的是 `roostRolling`，就**照 CLI 的算法传**（本地 0 点 / `now-7d`），
> 这样你的数值和官方 `/usage` 面板完全对齐，避免"你看到 1.29，官方显示 1.31"的困惑。

### 3.4 并发上限 / 速率限制

**【观测】** 客户端侧：`LNi = 6e4` 只作用于 `/api/perchai/account` 这条路径，
且是**单进程内存态**（`_vr` 模块级变量），多进程之间**不共享**。
走 `supabase.rpc()` 的路径**没有任何客户端节流**。

**【未知】** 服务端对 `perch_ai_my_inference_usage_meter` 的限流策略不可见。
PostgREST 层面通常只有网关级限制。

**【推断】** 池子里多账号并发调用时，风险来自 Supabase 项目的总请求速率，而非单账号。
建议：单账号并发 ≤ 1，全局加一个令牌桶（见第 6 节）。

### 3.5 Header 要求

**【观测】** 从 `rpc()` 的用法（`e.rpc("perch_ai_my_inference_usage_meter", {...})`）可确认：
这是标准 Supabase `PostgrestClient`，它自动带 `apikey`（来自 client 构造）和
`Authorization: Bearer <access_token>`。

**【观测】** `/api/perchai/account` 路径显式只用：
```js
headers: { Accept: "application/json", Authorization: `Bearer ${t.accessToken}`, ...Zw() }
```

**【推断】** `apikey` 应该用 **publishable key**（`sb_publishable_...`）。
新版 Supabase 里 `anon key` 已改名为 publishable key，二者对 PostgREST 的语义等价——
它只是项目级公开标识，**不是**权限来源。真正的权限来自 `Authorization` 里的用户 JWT。

**【观测】** 缺 `apikey` 时 PostgREST 的行为**不在**这个 bundle 里（是服务端/网关行为）。

- **【未知】** 具体状态码与响应体文本，我无法从客户端代码确定。
- **【推断】** 常规形态是 `401` + `{"message":"No API key found in request"}`，
  但这需要你用无效 header 实测确认。

### 3.6 错误形态区分

**【观测】** `getUsageMeter()` 的错误处理**非常粗糙**（偏移 `8699xxx`）：

```js
if (u.error) {
  console.error("[usage-meter] rpc failed:", _);
  return { ...d, dailyPt: zLe(c,r), weeklyPt: zLe(c,o) };   // 全部字段退化
}
```

注意 `d` 的初值里 `roostRolling: null`。

**【观测】** 也就是说：**认证失败 / 429 / 超时 / 网络错误，客户端全部退化成 `roostRolling: null`**，
不做区分。这是一个真实的客户端缺陷，但对你也是**有用的信号**：

> `roostRolling === null` **不等于**"无限制"，也**不等于**"额度为 0"。
> 它是"读失败/未知"。

**【推断】** 你的 Go 代码必须把 `roostRolling == null` 当作**未知态**处理，
绝不能当成"有额度"或"已耗尽"。这是最容易出的线上事故。

**如何与"配额耗尽"区分**（【观测】+【推断】）：

| 信号 | 来源 | 含义 |
|---|---|---|
| `roostRolling == null` | RPC 失败/字段缺失 | 读数失败，**未知** |
| HTTP 429 + `limit` 有 `limitKind` | model-call 路径 | 真配额耗尽 |
| HTTP 502 + `usage_limit_reached` | 你观测到的形态 | 见第 4 节 |
| `limitKind == "steps_running"` | 并发占用 | **不是**配额，短退避 |
| `limitKind == "metering_unavailable"` | 计量系统故障 | **不是**配额，重试 |

**【观测】** `i9e()`（偏移 `2923897` 附近）是一个纯文本兜底分类器：
```js
t.includes("usage limit") || t.includes("pilot capacity") || t.includes("monthly allowance")
  ? "usage_limit_reached" : "api_error"
```
这说明**有时**客户端只能靠正文文本判断——因为响应体可能没有结构化 `limit`。

---

## 4. 与错误码的关系

### 4.1 `usage_limit_reached` (HTTP 502) 与 `roostRolling` 是否一致？

**【未知】** 无法从客户端证明。理由见第 0 节：闸门 `e6t` 用的是服务端**重新聚合**的
`roost5hUsd` 等字段，不是 `roostRolling.window5hUsd`。

**【推断】** 在稳态下两者应高度接近（同一批消费行），但存在**已知的不一致窗口**：

1. **快照过期**：你读到 `roostRolling` 后，别处（同账号其它进程）又消费了 → 服务端已超，你还在等
2. **周限优先**：`e6t` 先查 7d 再查 5h（偏移 `1293951`/`1294091`），若 7d 已满，
   你会收到 `weekly` 而非 `five_hour`，而 `window5hUsd` 看起来还很有余量
3. **`enabled` 开关**：`Xjt()` 里 `roostRollingEnabled = PERCH_ROOST_RATE_LIMIT_ENABLED !== "false" && o>0 && a>0`。
   若上限配置为 0，`roostRolling` 可能不启用但闸门仍在（或反之）

**【推断】** `502` 这个状态码本身**不是**客户端产生的——客户端把 model-call 的 5xx 原样透传。
`usage_limit_reached` 在客户端是**业务错误码**（`errorCode` 字段），不是 HTTP 状态。

### 4.2 502 响应里有没有 `Retry-After`？

**【观测】** 客户端**从不**从配额错误里读 `Retry-After`。全 bundle 只有 4 处 `Retry-After`/`retry-after`：

| 偏移 | 归属 | 用途 |
|---|---|---|
| `140573` | `supabase-js` 内部 | 通用 5xx 重试 |
| `1319519` | `j6t()` | 传输层瞬时重试 |
| `5364989` | `supabase-js`（重复打包） | 同 `140573` |
| `5456486` | 同 `1319519` 的重复副本 | — |

**【观测】** 关键在 `j6t()` 的**唯一消费者** `e2()`（偏移 `1319700` 附近）：

```js
if (d) {
  if (d.ok || u === r || !H6t(d.status, t.modelOptionId)) return ...;   // 非重试类，直接返回
  let f = j6t(d) ?? n.backoffsMs[u];
  ...
  if (await d.body?.cancel().catch(()=>{}), L6t(...)) return d;        // 注意：body 被丢弃！
```

而 `H6t()`（偏移 `1318996`）：
```js
function H6t(e,t){ return e===429 || e===502 || e===503 || e===504 || e===529 || (t===XI && e===500); }
```

**【观测】** 于是对 502：

- 如果落进**瞬时重试**分支：`Retry-After` 被用于**退避等待**，但**响应体被 `cancel()` 丢弃**，
  所以拿不到 `limit` / `limitKind`
- 如果重试耗尽或不在重试列表：`Sk()` 把 body 读成文本，**`Retry-After` 被读但只存进一个内部 Map**

**【观测】** `Sk()`（偏移 `1320400` 附近）：
```js
let r = await e.text().catch(()=>"");
return O6t(new Error(`${t} returned ${e.status}: ${PH(r).slice(0,400)}`), j6t(e));
```
`O6t` 只是把毫秒数塞进 `D6t`（模块级 `Map`），**不暴露给调用方**。

**回答你的问题**：**【推断】** 502 响应里**不应该**依赖 `Retry-After`。
即使服务端发了，客户端也只在瞬态重试时消费它、且会丢弃 body。
你要的时间信息在**响应体的 `limit.resetAt`** 里（仅 5h/weekly 类），而不是 header。

> 但如果 502 走了瞬态重试分支，body 被丢弃 → 你连 `limit.resetAt` 都拿不到。
> **这正是"客户端侧预先拦截"的最大理由**：等 502 时，你可能只拿到一个裸 502。

### 4.3 全部与窗口相关的错误码

**【观测】** `u_r`（偏移 `91800` 附近）是完整的 `errorCode` 白名单：

```js
["usage_limit_reached", "promo_overflow_decision", "metering_unavailable",
 "starter_model_blocked", "plan_model_blocked", "tier_selection_required",
 "perch_surface_required", "turn_rate_limited", "client_update_required",
 "request_restricted"]
```

**【观测】** 其中与**用量窗口**直接相关的只有 3 个：

| errorCode | 何时出现 | 窗口相关? |
|---|---|---|
| `usage_limit_reached` | 5h/7d/月限触达 | ✅ |
| `promo_overflow_decision` | 促销额度溢出（`variant==="promo_pin"`） | ✅ |
| `metering_unavailable` | 计量服务故障（`userCheckFailed`） | ⚠️ 间接 |

**【观测】** `limitKind` 的完整枚举（`d_r`，紧跟 `u_r`）：
```
["daily","monthly","manual_monthly","starter_five_hour","starter_weekly",
 "roost_five_hour","roost_weekly","promo_five_hour","promo_weekly",
 "manual_five_hour","manual_weekly","steps_running","usage_unverified","metering_unavailable"]
```
**这 14 个值就是你需要处理的全部窗口维度。**

其余错误码（`starter_model_blocked`、`plan_model_blocked`、`turn_rate_limited` 等）
与**模型准入/速率**有关，**不是**窗口额度。

### 4.4 "Steps still running..." 对应什么状态

**【观测】** 文案在偏移 `84083`（英文）：`stepsRunning.fiveHour = "Steps still running are using the rest of your 5-hour limit. Try again when they finish."`

**【观测】** 构造于 `o6t()`（偏移 `1297385`）：
```js
function o6t(e,t){
  return { errorCode:"usage_limit_reached", limitKind:"steps_running",
           resetAt:null, plan: OO(t) ?? Zjt(e), heldLimit: ZLr(e) };
}
```
其中 `ZLr()`（偏移 `1297xxx`）把原始 kind 映射成 `"five_hour"|"weekly"|"monthly"|"manual_monthly"|"usage"`，
存进 `heldLimit`。

**【观测】** 调用来源（偏移 `1310xxx`、`1319xxx`）：这是**预扣（reservation）**机制
（`perch_ai_reserve_usage_pt`）返回 `headroom: insufficient` 时的产物。

**对应 `roostRolling` 的什么状态**：

**【推断】** 对应"**有用量已被在途 turn 预占，但尚未结算**"。

- `window5hUsd` **可能还没到 `window5hCapUsd`**（所以你的读数看起来"没满"）
- 但预占量已把余量吃光 → 服务端拒绝
- 关键识别特征：**`limit.resetAt === null`**，且 **`limit.heldLimit === "five_hour"`**

> 这是"读数未满但请求被拒"的最常见原因。
> 你的池子如果把 `window5hUsd < cap` 当成"一定能用"，就会反复撞这个错。
> **正确做法**：对 `steps_running` 做**短退避**（秒级），而不是把账号冷却 5 小时。

---

## 5. 其他窗口 / 计量体系

### 5.1 除 5h/7d/月度 PT 外还有哪些被强制执行的窗口

**【观测】** 从 `e6t()`（`1293774`）完整可执行的检查项：

| 维度 | 字段 | 是否强制 | 备注 |
|---|---|---|---|
| Starter 周限 (7d) | `starterWeeklyUsd/CapUsd` | ✅ | 先于 5h 检查 |
| Starter 5h | `starter5hUsd/CapUsd` | ✅ | |
| Roost 周限 (7d) | `roostWeeklyUsd/CapUsd` | ✅ | |
| Roost 5h | `roost5hUsd/CapUsd` | ✅ | |
| Manual 周限 (7d) | `manualWeeklyUsd/CapUsd` | ✅ | |
| Manual 5h | `manual5hUsd/CapUsd` | ✅ | |
| Pro/Ultra 月度手工 PT | `manualMonthlyPtLimit` vs `monthlyManualPtUsed` | ✅ | `>=` |
| Starter 月度 PT | `monthlyPtLimit` vs `monthlyPtUsed` | ✅ | `>=` |
| **用户级日消费上限 USD** | `PERCH_PILOT_USER_DAILY_CAP_USD`（默认 **5**） | ✅ | **你漏掉的一维！** |

**【观测】** 最后一行：`Xjt()`（`1290978`）里
```js
let t = WH(process.env.PERCH_PILOT_USER_DAILY_CAP_USD, 5);
return { enabled: e && t > 0, userDailyCapUsd: t, ... }
```
以及 `d_r` 里第一个 `limitKind` 就是 `"daily"`。

> **【推断】** 除了你已知的 5h/7d/月度 PT，还有 **`daily`（每账号每日 $5）** 这一维。
> 它默认启用（`PERCH_PILOT_SPEND_CAP_ENABLED !== "false"`）。这在阈值上通常高于 5h，
> 但在"5h 刚好回血、一天又刷了很多轮"的池子里，可能先撞日限。

**【观测】** 还有 `t6t()`（偏移 `1295555`）——**余量（headroom）**计算，服务于预占：
```js
_we(cap, used) = Math.max(0, Math.floor((cap - used) * 1e3))   // 转成 PT，向下取整
```
它取所有维度里 **`headroomPt` 最小**的那个（`r.reduce(...)`），这就是能被预占的上限。

### 5.2 `manualRolling`、`promoPool`、`monthlyManualPt`

**【观测】** `manualRolling` 与 `roostRolling` **共用同一个解析器** `Prr()`，
字段结构完全相同（`window5hUsd/CapUsd/NextFreedAt`、`window7d*`）。

- 偏移 `8700163`：`roostRolling: Prr(p.roost_rolling), manualRolling: Prr(p.manual_rolling)`
- 偏移 `8703823`：`uRt()` 里 `manualRolling` 用 `"manual"` 前缀、`roostRolling` 用 `"plan"` 前缀
  （对应 `manualWindow5h` / `window5h` 两套行 id）

**【观测】** 何时有值？`bUr()`（`1313190`）显示 `manual` lane 只对以下 kind 生效：
```
user_manual_5h / user_manual_weekly / user_monthly_manual
```
而 `B_i()`（`8702470`）里 `manual` 行**仅在 `eC(planCode)` 为真时渲染**（即 Pro/Ultra 的手工模型额度）。

> **【推断】** `manualRolling` 只在 **Pro/Ultra 且使用 pinned/manual 模型**时才有值。
> 你的 Starter 账号 → 恒为 `null` 是**正常**的，不是 bug。

**【观测】** `promoPool`：**bundle 内 0 处命中**（`grep -c promoPool` = 0）。

> **【未知】** 这个字段名**不在** 2.4.120 的客户端代码里。
> 它可能只存在于服务端 RPC 返回体里而客户端选择了忽略。
> 我**不推测**其语义。客户端侧相近概念是 `variant === "promo_pin"`
> （→ `limitKind` 变成 `promo_five_hour`/`promo_weekly`）和 `promoOverflow`
> （→ `errorCode: "promo_overflow_decision"`）。

**【观测】** `monthlyManualPt`：对应 RPC 字段 `monthly_manual_usd`，经
`M_i(e){ return e==null||e<=0 ? null : Math.ceil(e*1000) }`（偏移 `8698xxx`）转成 PT。
它只在 `B_i()` 的 `manual` 行里用 `qLe(used, limit)` 算百分比。

> **【推断】** 同 `manualRolling`，Starter 下恒为 `null`。

### 5.3 Starter 的 $1.30/5h 和 $4/7d 粒度

**【观测】** bundle 里的**默认常量**是（偏移 `1296xxx`，`Wqe` 模块尾部）：
```js
jLr = 5,     zLr = 20,       // roost 5h = $5,  roost weekly = $20
$Lr = 2.5,   qLr = 7.5,      // starter 5h = $2.5, starter weekly = $7.5
WLr = 15,    GLr = 37.5      // manual 5h = $15, manual weekly = $37.5
```

**【观测】** 但**全部可被环境变量覆盖**（`Xjt()` @ `1290978`）：
```
PERCH_STARTER_5H_CAP_USD       (默认 $Lr = 2.5)
PERCH_STARTER_WEEKLY_CAP_USD   (默认 qLr = 7.5)
PERCH_ROOST_5H_CAP_USD         (默认 jLr = 5)
PERCH_ROOST_WEEKLY_CAP_USD     (默认 zLr = 20)
PERCH_MANUAL_5H_CAP_USD        (默认 WLr = 15)
PERCH_MANUAL_WEEKLY_CAP_USD    (默认 GLr = 37.5)
```

**【推断】** 你观测到的 **$1.3 / $4** **不等于**任何一组默认值。可能是：

1. 服务端配置了不同的环境变量（**最可能**）
2. 你的账号处于 A/B 或促销档位
3. 你读到的是**别的 lane**（例如某个 promo 变体）

**【观测】** 粒度是**按账号（user_id）**，不是按 workspace 或全局：
- `l6t()` 的缓存 key 是 `` `${t.band}:${t.windowMs}:${t.userId}` ``（偏移 `1300xxx`）
- 查询过滤是 `.eq("user_id", t)`（`sumSpendUsdByLane` 系列，偏移 `1299xxx`）
- RPC 名是 `perch_ai_my_inference_usage_meter`（`**my**`）

**【推断】** 所以 `window5hCapUsd` 是**该账号在该 lane 上的上限**。
同账号不同 lane（roost vs manual）有**各自独立**的窗口与上限。
**这意味着一个账号可以同时有 roost 5h 和 manual 5h 两套窗口。**

---

## 6. 给你的建议（重点）

### 6.1 冷却时长应该取什么？

**结论：取 `window5hNextFreedAt` 和 `window7dNextFreedAt` 两者中"更严格"的那个。**

具体规则（【推断】，基于 1.1 的逐条释放模型 + 2.3 的 `resetAt` 语义）：

```
冷却截止 = max(
   window5hUsd >= window5hCapUsd ? window5hNextFreedAt : -∞,
   window7dUsd >= window7dCapUsd ? window7dNextFreedAt : -∞
)
```

理由：

1. **必须查 7d**。`e6t` 先查周限（偏移 `1293951`/`1294091`）。
   只查 5h 会让你把"周限已满"的号当成可用 —— 这是最容易踩的坑。
2. **不要用 `nextFreedAt` 做"整体重置"**。它只是"刚好腾出余量"的时刻，
   不是"额度归零"。把它当作"可以重试的最早时刻"是**正确**用法。
3. **加一点余量**。`l6t` 用了 `1e-9` 容差（`aUr=1e-9`），且服务端结算有延迟。
   建议 `冷却截止 = nextFreedAt + 30s ~ 60s`。
4. **月度不该有 nextFreedAt**（`resetAt=null`）。若撞月度限，
   不要按时间重试，直接把账号标成"本月不可用"。

> ⚠️ **但不要只用本地读数判定**。见 6.2。

### 6.2 是否需要客户端侧预先拦截？

**结论：需要，但只作为"省一次 502"的优化，不能替代服务端判定。**

支持预先拦截的证据：

1. **【观测】** 502 走瞬态重试分支时 body 会被 `cancel()` 丢弃（偏移 `1319700` 附近），
   你可能拿不到 `limit.resetAt`。预先拦截能避免落到这种"信息贫乏"的错误上。
2. **【观测】** 客户端自己**不做**预拦截（第 2.2 节），但它每次请求都带服务端重算的数值，
   所以它不需要。**你没有那个特权** —— 你只有快照，所以你必须更保守。
3. **【观测】** `getUsageMeter` 的错误处理会把一切失败退化成 `roostRolling: null`（3.6 节），
   你必须自己维护"上次成功读数 + 时间戳"，否则无法区分"未知"和"无限"。

**建议的拦截设计**：

```
IF roostRolling == nil          → 视为 UNKNOWN，走探测（低频试一次），不要标记可用
IF now < cooldownUntil          → 跳过，不发请求
IF window5hUsd >= window5hCapUsd → 直接设 cooldownUntil = window5hNextFreedAt + 60s
IF window7dUsd >= window7dCapUsd → 直接设 cooldownUntil = window7dNextFreedAt + 60s
ELSE                            → 允许发起
```

**但必须**：拦截只是优化。真实请求仍可能返回 `usage_limit_reached`，你要仍然处理它，
并用响应里的 `limit.resetAt` / `limit.limitKind` **覆盖**本地推算值（服务端更权威）。

特别地：**对 `limitKind === "steps_running"` 千万不要设长冷却**（4.4 节）。
它是并发占用，`resetAt` 为 `null`。正确做法是秒级退避（如 5s / 15s / 60s 三次）。

### 6.3 轮询频率与缓存建议

| 参数 | CLI 做法（观测） | 你的建议（推断） |
|---|---|---|
| 同一账号读用量最短间隔 | **60 秒**（`LNi=6e4`，仅 `/api/perchai/account`） | **60–120 秒** |
| 单次请求超时 | **5 秒**（`AbortController`） | 5–10 秒 |
| 你的 `quotaIntervalSec` | 不适用（CLI 无此配置） | **600 秒偏长**，见下 |

**为什么 600 秒偏长**：

- 5h 窗口只有 18000 秒，600 秒是 **1/30**。在"最后 10 分钟回血"阶段，
  你会错过多次可用的时机。
- 但同时：5h 用到 99% 时，每 60 秒刷一次也多半还是满的。

**推荐分层策略**（【推断】）：

```
有效刷新间隔 =
  百分比 < 50%              → 600s（省请求）
  50% <= 百分比 < 90%       → 120s
  百分比 >= 90%             → 60s（对齐 CLI 下限）
  now >= nextFreedAt - 120s → 30s（回血临界，但要限次）
```

另外两点：

- **【观测】** `Ilr()` 用的是 `window7dNextFreedAt` 作为警告基准（偏移 `8958094` 附近），
  阈值 `AIi = [80, 50]`（百分比）。这可以作为你"预警"档位的参考。
- **【观测】** 警告去重状态持久化到 `usage-warnings.json`
  （`gwr()` 偏移 `12642500` 附近），按 `userId:lane:threshold` 作 key。
  这也提示你：**告警要按 (账号, lane, 阈值) 去重**，否则会刷屏。

### 6.4 对你的 Go 代码的直接改动清单

1. **解析 `roostRolling`**（以及 `manualRolling` —— 结构相同，别漏）
   ```
   enabled(bool), window5hUsd(f64), window5hCapUsd(*f64 可为 null),
   window5hNextFreedAt(string→time), window7dUsd(f64),
   window7dCapUsd(f64), window7dNextFreedAt(string→time)
   ```
   注意 `window5hCapUsd` **可能为 null**（`Prr` 里显式判了 `=== null`），
   而 `window7dCapUsd` **不判 null**（直接 `Number(...)`，缺失变 `0`）。这个不对称是有意的。

2. **修 `p_day_start` 的时区**：改成"本地 0 点 → `toISOString()`"，
   与 `I_i()`（偏移 `8697xxx`）一致。`p_week_start` 用 `now - 7d`（不是 7 天前 0 点）。

3. **`null` 语义**：`roostRolling == null` → UNKNOWN，**不是** 0，**不是** 无限。

4. **比较用 `>=`**，不是 `>`（六个比较点全用 `>=`）。

5. **先查 7d 再查 5h**，与 `e6t` 顺序一致；两者都满时优先报 `weekly`。

6. **冷却 = max(5h 的 nextFreedAt, 7d 的 nextFreedAt) + 60s**。

7. **`steps_running` / `usage_unverified` 走短退避**，不要拉长冷却。

8. **别忘 `daily` 维度**（$5/天默认）—— 它是 `d_r` 枚举的第一个值。

---

## 7. 未解决的问题（需要服务端证据）

| # | 问题 | 为什么无法回答 |
|---|---|---|
| 1 | `roost_rolling` 的 SQL 实现，是否真用 `p_day_start`/`p_week_start` | RPC 函数体在 Postgres 里 |
| 2 | 2.4.120 → 2.4.121 的差异 | 本机只有 2.4.120，且不允许联网 |
| 3 | `promoPool` 字段语义 | bundle 内 0 处命中 |
| 4 | 服务端对 meter RPC 的 rate limit | 网关/PostgREST 层 |
| 5 | 缺/错 `apikey` 的确切响应 | 服务端行为 |
| 6 | `$1.30`/`$4` 为何不同于默认 `2.5`/`7.5` | 服务端 env 配置 |
| 7 | 你样本里 57 分钟差值的具体来源 | 需两笔消费的 `created_at` 明细 |

---

## 附：关键符号偏移速查（`dist/perch.mjs`）

| 符号 | 字节偏移 | 行 | 作用 |
|---|---|---|---|
| `j3()` | 90,855 | 5 | `limit` 对象解析器（客户端 schema） |
| `u_r` / `d_r` | ~91,800 | 5 | `errorCode` / `limitKind` 白名单 |
| `eSr` | 118,932 | — | 配额错误正则（文本兜底） |
| `Prr()` | 8,696,947 | 10503 | **`roostRolling` / `manualRolling` 解析器** |
| `I_i()`/`R_i()`/`P_i()` | ~8,697,100 | 10503 | `p_day_start` / `p_week_start` / 月度起点 |
| `Mrr().getUsageMeter` | 8,699,929 | 10503 | RPC 调用 + 失败退化 |
| `Ome()` | 8,700,530 | 10503 | 组装 allowance 对象 |
| `uRt()` | 8,701,690 | 10503 | 生成 `/usage` 展示行 |
| `B_i()` | 8,702,470 | 10503 | 按 plan 生成全部行 |
| `$Le()`/`qLe()` | ~8,702,650 | 10503 | USD→PT 取整 / 百分比 |
| `H_i()` | 8,705,791 | 10504 | `/api/perchai/account` 读用量 |
| `Brr()` | 8,706,317 | 10504 | 用量总入口（唯一调用点 8,711,647） |
| `bIi()` | 8,957,788 | 10552 | 7d 用量百分比（供警告） |
| `Ilr()` | 8,957,937 | 10552 | Ultra 周用量 50%/80% 警告 |
| `Mlr()` | 8,958,788 | 10552 | 警告文案渲染（用 `window7dNextFreedAt`） |
| `AIi` | ~8,959,300 | 10552 | 警告阈值 `[80, 50]` |
| `Xjt()` | 1,290,978 | 130 | **各 lane 上限常量 + env 覆盖** |
| `XLr()` | 1,292,402 | 130 | kind → `limitKind` |
| `Swe()` | 1,293,151 | 130 | 构造 `limit`（`resetAt` 仅 5h/weekly） |
| `ZO()` | 1,293,694 | 130 | 拒绝项包装 |
| **`e6t()`** | **1,293,774** | **130** | **服务端准入闸门（`>=`，周限优先）** |
| `t6t()` | 1,295,555 | 130 | 余量 headroom（取最小维度） |
| `_we()` | 1,296,886 | 130 | `(cap-used)*1000` 向下取整 |
| `o6t()` | 1,297,385 | 130 | **`steps_running` 构造（`resetAt:null`）** |
| `GH`/`QH` | ~1,297,700 | 130 | 窗口常量 5h / 7d（毫秒） |
| `jLr`…`GLr` | ~1,297,900 | 130 | 默认上限 5/20/2.5/7.5/15/37.5 |
| `l6t()` | 1,299,967 | 130 | **`nextFreedAt` 算法（逐行累加）** |
| `bUr()` | 1,313,190 | 131 | kind → `{band, since, windowMs, cap}` |
| `j6t()` | 1,319,519 | 131 | **唯一读 `Retry-After` 处** |
| `H6t()` | 1,318,996 | 131 | 可重试状态：429/502/503/504/529 |
| `e2()` | ~1,319,700 | 131 | 瞬态重试（**丢弃 body**） |
| `Mwe()` | 1,317,881 | 131 | 标记错误为"已重试过" |
| `LNi=6e4` | 12,830,658 | — | **`/api/perchai/account` 60 秒节流** |
