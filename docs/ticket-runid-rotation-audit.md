# 审计报告：turn ticket 与 runId 的轮换语义

**审计对象**：`/Users/lyh_god/GolandProjects/account-pool-balancer`（HEAD `02f850b`）
**方法**：逐行读取实现 + 跨仓库调用点检索 + 与原版 `perchai-cli@2.4.120` bundle 对照
**范围**：只审计，未改动任何代码

---

## 结论摘要

**轮换规则本身大体正确，但有两处会导致实际错误行为。**

最大的风险是 **C2：runId 注册表与 delta 缓存键都是全局的，不含账号维度**。账号 failover 后，第二个账号会继承第一个账号建立的 runId 与「服务端已持有前缀」的断言，向它声称一段它从未见过的上下文。原版 CLI 一个进程只服务一个账号，从不存在这个场景；本仓库把它移植到多账号池时漏掉了密钥作用域。

第二个是 **B1：`ForgetRun` 无生产调用点**，delta 被拒后只丢快照、不换 runId，下一个请求会用同一个「服务端已否认」的 runId 重试，每次都先失败一次再回退全量。

另有三处属于「与官方偏离但当前无害」或死代码：**A4（`DropTicket` 无调用点）**、**A3/C3（`UseTicket` 无调用点，`callsSent` 永不增长）**、**B2（死代码）**。

值得注意的是：**你给出的结论 1–4 全部成立**，但都不完整——它们描述的是「正常路径」，而缺陷都在异常路径上。

---

## 逐条核实

### A1 — 单飞防双领票：**成立（但 fast path 存在理论上的重复领取）**

**证据**
- `internal/perch/ticket.go:296-299` fast path：缓存票距过期 > 30s 直接返回，不打网络
- `internal/perch/ticket.go:303-320`：`c.tickets.inst[key]` 存在则等待 `fl.done`，不重复发起
- `internal/perch/ticket.go:321-323`：先占位 `inst[key]` 再释放锁，随后执行网络请求

**分析**
单飞是正确的：`inst` 的占位与检查在同一把锁内原子完成，后到者只会等待。并发 N 个请求只发出 1 次领票请求。

但 fast path（`ticket.go:296`）在**锁外**读 `peekTicket`，两个 goroutine 可能同时判定「无票」而都进入慢 path——此时仍被 `inst` 拦住，所以不会双领票。**这一条成立。**

真正的边界在 A5（见下）：fast path 判定「还有 30s」之后到请求真正发出之间，票可能过期。

---

### A2 — 续期失败继续用旧票：**成立，但有一条静默无票的边界**

**证据**
- `internal/perch/ticket.go:359-363`：续期成功 → 返回新票
- `internal/perch/ticket.go:364-368`：`err != nil && !errTicketSoft` → 返回 `(nil, err)`（enforced 失败）
- `internal/perch/ticket.go:372-376`：旧票未过期 → 继续用旧票
- `internal/perch/ticket.go:378-385`：否则重新领取；`errTicketSoft` 转成 `(nil, nil)`

**分析**
与 `LQt` 语义一致：续期失败不致命，旧票未过期就继续用。

**但存在一条会退化成静默无票的路径**：`acquireTicket:364-368` 中，如果续期返回的是 **enforced 失败**，直接 `return nil, err`——此时旧票其实还没过期，却完全没有被使用。原版 `LQt` 在同位置是：

```js
if (hS = null, xQ) throw new g2;   // enforced 才抛
return {};                          // 否则降级
```

原版在 enforced 时也会抛，行为一致，**所以这条不是缺陷**，只是需要注意：enforced 续期失败会作废一张未过期的票。

真正的降级路径（`errTicketSoft`）会跳过 `:372` 的旧票分支吗？不会——`:369` 的条件是 `err != nil && !errors.Is(err, errTicketSoft)`，soft 错误不进入，直接落到 `:372`，旧票未过期则返回旧票。**逻辑正确。**

---

### A3 — `UseTicket` 与 `TicketFor` 值拷贝的竞态：**不成立（因为 `UseTicket` 根本没有生产调用点）**

**证据**
- `internal/perch/ticket.go:564-578` `UseTicket` 实现：重新读缓存并 `tk.callsSent++`
- 全仓库检索 `UseTicket`：**仅 `ticket.go` 自身定义 + `ticket_test.go` 两处测试**，无生产调用点

**分析**
竞态问题**在现有代码里不存在**，因为 `callsSent` 永远不增长。`TicketFor` 返回 `*Ticket` 拷贝时把 `callsSent` 归零（见 `ticket.go:335`、`:352`），而缓存的权威副本无人递增。

这引出一个更值得注意的事实：**配额计数完全没接上**。`ticket.go:26` 的注释说「the client counts callsSent」，`Ticket.Remaining()`（`ticket.go:161-166`）也据此计算，但生产路径上没有任何地方调用它，所以：
- `Remaining()` 永远返回完整预算（40）
- 诊断接口显示的 `ticketUses` 永远是 0

**这不是竞态 bug，而是功能未接线**（详见 C3）。

---

### A4 — `DropTicket` 无生产调用点：**成立，且是真实缺陷**

**证据**
- `internal/perch/ticket.go:590` `DropTicket` 定义
- 全仓库检索：**无任何生产调用点**

**分析**
服务端明确否认一张票时（例如 401 `not_authenticated`、或 400 指向票本身），池子不会丢弃它。下次请求只要票还在 `expiresAt` 内且距过期 > 30s，fast path 直接复用这张坏票，直到自然过期（最长 5 分钟，`ticketDefaultTtl`）。

**影响**：最长 5 分钟内，所有该账号的 CLI 面请求都会带同一张已被拒绝的票。实际后果取决于服务端是否对重复无效票加重判定——**这一点无法确定**，因为我方日志没有区分「票被拒」与「token 被拒」（`gateway.go:refusal` 只按 HTTP 状态分类，而票被拒与认证失败同为 401）。

---

### A5 — 票在 model-call 飞行期间过期：**成立（存在窗口）**

**证据**
- `internal/perch/ticket.go:71` `TicketRenewLead = 30 * time.Second`
- `internal/perch/ticket.go:296` fast path 要求 `now < expiresAt - 30s`
- `internal/perch/gateway.go:295` model-call 的 `timeout` 默认 5 分钟

**分析**
fast path 只保证「取票时至少还有 30 秒」。取票到请求真正发出之间还要经过 envelope 构建、代理选择等步骤；请求发出后还可能跑满 5 分钟。

因此**票几乎必然在长 turn 期间过期**。这本身不是 bug——`x-perch-turn-ticket` 在请求开始时校验一次即可，原版也是如此。但要注意：**续期逻辑（`GQt`）只在「取票时」触发，不会在请求飞行中续期**，这一点与原版一致（原版也是每次 model-call 前取票）。

结论：**成立但不构成缺陷**，与官方一致。

---

### B1 — `ForgetRun` 无调用点 / delta 被拒后不换 runId：**成立，是真实缺陷**

**证据**
- `internal/perch/runid.go:98-106` `ForgetRun` 定义
- 全仓库检索：**无生产调用点**
- `internal/perch/gateway.go:365-370`：被拒分支只调 `c.deltas.dropPrefix(plan.key, plan.digests)`，不动 runId

**分析**
服务端 409 明确表示「这个 run 我不认这个前缀」。当前实现：
1. 丢弃该前缀的快照（`dropPrefix`）
2. 用**同一个 runId** 重发全量

第 2 次全量请求若成功，`SettleDelta` 会在同一 key 下建立新快照（`runid.go` 未参与）。于是后续请求继续用这个 runId 做增量。

**这未必是错的**：409 说的是「前缀对不上」，不是「runId 无效」，换全量后服务端接受同一 runId 是完全合法的。原版 `r9t` 的 `whole()` 也是保留 endpoint+runId、只丢弃 digest 基线：

```js
whole(){ return t9e(t, n.messageDigests), i9t(e, t, n, null); }
```

**因此 `ForgetRun` 无调用点不是 bug**，反而是与原版一致的设计。原版也没有「被拒就换 runId」的概念。

**但注释与实现在此有出入**：`runid.go:104` 的注释说「used when the server rejects the prefix so the next request starts a new run」——这句话描述的**不是**当前行为，属注释失真（归入 P2）。

---

### B2 — `whole := c.PlanDelta(url, "", Envelope{}); _ = whole` 死代码：**成立**

**证据**
- `internal/perch/gateway.go:368-369`

**分析**
`PlanDelta` 在 `runID == ""` 时立即返回空 plan（`delta.go:303-306`），返回值被赋给 `whole` 后立刻 `_ = whole` 丢弃，从未使用。**删除安全**，无任何副作用。

（顺带说明：这行顺带调用了 `PlanDelta`，而它对空 env 的 `digestMessages(nil)` 返回空切片，也不产生副作用。）

---

### B3 — `RunIdTTL > DeltaTTL`：**成立，且行为确实「只是发全量」**

**证据**
- `internal/perch/runid.go:43` `RunIdTTL = 30 * time.Minute`
- `internal/perch/delta.go:53` `DeltaTTL = 9 * time.Minute`
- `internal/perch/delta.go:346`：`find` 跳过 `confirmedAt` 超过 9 分钟的条目

**分析**
对话闲置 9–30 分钟时：runId 仍复用（`runid.go:82`），但所有快照已过期（`delta.go:346`），`find` 返回 nil，`PlanDelta` 落到 `delta.go:312` 返回非 delta plan → 发全量。

**无副作用**：请求是全量的，服务端会丢弃旧前缀重新建立。下一次调用可以重新成为 delta。符合设计意图。

---

### B4 — `maxRunIds` 淘汰：**成立，会淘汰活跃对话，但无实质冲突**

**证据**
- `internal/perch/runid.go:117-137` `evictRunsLocked`
- `internal/perch/runid.go:88-92`：超限时调用

**分析**
淘汰顺序是「先清过期，再按 `lastUsed` 从旧到新」。`RunFor` 每次命中都会刷新 `lastUsed`（`runid.go:84`），所以**活跃对话理论上不会被优先淘汰**——除非同时有 >4096 个对话都活跃，那时被淘汰的是最久未用的那个。

淘汰后同对话会得到新 runId（`runid.go:88`，代际计数保证 id 不同）。此时 delta 缓存里该旧 runId 的条目仍在（`byK` 以 `endpoint+runId` 为键），但因为新 runId 不同，`find` 查不到，**不会冲突**。残留条目会在 `put` 的键数上限（`DeltaMaxKeys = 32`）下被自然挤出。

**结论**：无实质问题。但注意 `DeltaMaxKeys` 只有 32，而 `maxRunIds` 是 4096——**delta 缓存的实际容量远小于 runId 注册表**，大部分对话的 runId 虽然稳定，其 delta 缓存却早已被挤出。这会让 hitRate 偏低，但不是正确性问题。

---

### B5 — 哪些调用方式落到「每次新 runId」：**成立**

**证据**
- `internal/server/perch_proxy.go:59-63`：`if id.Confidence != "explicit" { return "" }`
- `internal/server/identity.go:144-179`：`identity()` 的判定顺序

**分析**
`identity()` 只有在下列来源命中时才返回 `Confidence: "explicit"`：`previous_response_id`（body）、自定义 session header、parent headers、body 字段（`prompt_cache_key` 等）、Claude `metadata.user_id`、以及一批已知 session headers。

以下情况会落到 `message_hmac` fallback 或 `none`，从而**每次新 runId**：
- 客户端不带任何 session 头/Prompt 缓存键
- 且没有 `previous_response_id`
- 或 `cfg.Routing.FallbackHash == false`（此时直接 `none`）

**这类客户端的 delta 命中率为 0**，每次全量。这是刻意的保守选择（`perch_proxy.go:56-61` 有说明：避免两个相同开头的无关对话共用 run）。

---

### C1 — 两侧错误耦合：**不成立**

**证据**
- `internal/server/perch_proxy.go:242-266`（领票）与 `:275-289`（runId 决议）是两个独立块
- 领票用 `acct.ID` 作 key（`perch_proxy.go:215`），runId 用对话指纹

**分析**
两者解耦正确：ticket 按账号、runId 按对话。`perch_proxy.go:283-288` 只在「无显式对话」时回落 `ticket.RunID`，这是唯一的耦合点，且语义合理（无对话可延续时，沿用本次领票的 run）。

---

### C2 — runId 注册表与 delta 缓存全局、不按账号：**成立，是最严重的问题**

**证据**
- `internal/perch/client.go:288-294`：`Client` 持有 `runs runRegistry` 与 `deltas deltaCache`，二者都是**单例字段**
- `internal/server/perch.go:447-452`：`perchClient()` 返回 `s.perch.keeper.Client()`——**全进程一个 Client**
- `internal/perchstore/keeper.go:223`：`client: perch.NewClient(nil)` 单次构造
- `internal/perch/delta.go:307`：`key := endpoint + "\n" + run` —— **不含账号**
- `internal/perch/runid.go:60-95`：`RunFor` 只按指纹索引，**不含账号**
- `internal/server/proxy.go:577`：failover 时 `excluded[acct.ID] = true` 后换到下一个账号，**再次进入 `attemptPerch`**

**分析**

这是真实缺陷，链路如下：

1. 对话 X 首次请求落在账号 A，`RunFor(X)` 生成 runId `R`，账号 A 的 model-call 成功，服务端确认持有前缀 P1，存入 `deltas["https://app...\nR"]`。
2. 账号 A 出错（如额度耗尽），failover 到账号 B。
3. **同一对话 X 的下一轮请求**（或同一请求的重试）再次 `RunFor(X)` → 仍是 `R`；`PlanDelta` 查到 P1 → 判定为 delta。
4. 请求发往账号 B，body 携带 `messagesDelta{baseLength: N, baseDigest: ...}`，**声称服务端（B 的上下文）已持有 N 条消息**。

但账号 B 从未见过这段对话。服务端的正确反应是返回 409 拒绝，我方随即全量重发——**所以最终结果仍然正确**。

**但代价是真实的**：
- 每次跨账号都先浪费一次请求（409）才回退
- 向 B 发送了一个「我没有这段上下文」的错误断言，这本身是异常行为信号
- 计数器 `refusedDeltas` 会持续增长

**更严重的是同一账号内的键冲突**：`key` 不含账号，若两个账号（不同 Perch 身份）在**同一 endpoint 上恰好共用同一 runId**——这在 `RunFor` 用对话指纹派生时不会发生（指纹相同即同一对话），但**如果两个不同对话经 `ConversationRunID` 派生后 24 位十六进制碰撞**（概率极低），或**同一对话被两个不同账号分别服务过**（C2 的主场景），就会命中彼此的缓存。

**结论：成立，P0 级。** 原版 CLI 一个进程只服务一个账号，从不触发；本仓库移植到多账号池时漏了作用域。

---

### C3 — delta 被拒后全量重发是否消耗第二次配额：**不成立（因为配额根本没计数）**

**证据**
- 全仓库 `UseTicket` 无生产调用点（见 A3）
- `gateway.go:369` 的整包重发直接调 `c.callOnce`，不经过任何配额扣减
- `ticket.go:143-145`：`callsSent` 只由 `UseTicket` 递增

**分析**
无论重发几次，`callsSent` 都不会变化，`Remaining()` 恒为 40。**配额计数在整条链路上都没有接线**。

对照原版：`_j(ticket)` 在每次带票请求时执行 `e.callsSent = (e.callsSent ?? 0) + 1`（bundle 中 `_j` 定义），所以原版每次 model-call（含重试）都消耗一次配额。

**因此这是一处偏离**：本仓库不计数。影响仅限诊断可见性（既然客户端的计数从不用于拦截，服务端才是权威），所以归类为 P1 而非 P0。

**但要注意**：如果将来接上 `UseTicket`，delta 拒绝路径的重发**会**消耗第二次配额——需要确认这符合预期，因为原版在被拒后同样会重发（`g9t` 里 `r.deltaRefused` 后再次 `A9t`），原版也会计两次。**即接上后行为与原版一致，无需特殊处理。**

---

### D1 — 用对话指纹覆盖票的 runId：偏离清单

**证据**
- `internal/server/perch_proxy.go:283-288`
- 原版 `DTi`：`p = await Q1t(...) || e.clientRunId?.trim() || g8(r())`（`perch.mjs` run 入口）
- 原版 `ZQe` 返回 `a?.runId`，即票的 runId

**分析**

| 差异 | 原版 | 本仓库 | 可观测影响 |
|---|---|---|---|
| runId 来源 | 票的 `runId` | 对话指纹派生 | 服务端看到的 runId 与 ticketId **不再对应** |
| runId 生命周期 | 一个 turn（一次 `perch run`） | 30 分钟内同一对话复用 | 同一 runId 跨越多个 turn |
| runId 与 ticket 关系 | 1:1 | N:1（多张票共用一个 runId） | 一张票续期后 runId 不变——**原版续期后 runId 也不变**（票对象整体替换但 `runId` 通常在响应中保留），此点无差异 |
| 无对话时 | 用票的 runId | 用票的 runId | 无差异 |

最实质的偏离是**第一项**：票响应里带 `runId` 字段（实测 `"runId":"tkt-cli-1793640730090-02ox0h68"`），而本仓库发的 envelope `runId` 是 `cli-turn-<hex>-<hex>` 形状，**票的 runId 从未被使用**。若服务端校验「票的 runId 必须等于 envelope 的 runId」，本仓库会被拒——**当前无证据表明有此校验**（我方实测中票与 model-call 均成功过），故标注为**潜在风险，无法确定**。

---

### D2 — 原版是否有本仓库漏掉的重新领票时机：**成立，存在差异**

**证据**
- 原版 `Q1t` 调用点两处：`DTi`（`perch run` 入口）与压缩流程（`perch.mjs` 中另一处 `Q1t` 调用）
- 原版 `TQ(runId)`（每次 model-call 前）会在 `expiresAt <= now + 30s` 时走 `LQt` → `_Q ??= kqr()` 续期
- 原版 `wqr` 中，非 enforced 的失败 → `return null` → `TQ` 返回 `{}`（无票继续）

**分析**

原版在两种时机「重新领票」：
1. **续期**（`30s` 提前量）——本仓库有，`acquireTicket:359`
2. **全新领取**（票已过期且续期失败）——本仓库有，`acquireTicket:378`

原版还有第三种：**每个 turn 开始时的 `Q1t`**。这在多账号池里没有对应物，因为本仓库不建模「turn」——每个下游请求都是独立事务。

**因此差异是结构性的**：原版 runId 的生命周期由 `perch run` 界定，本仓库由 `RunIdTTL` 界定。这不是「漏了某个时机」，而是模型不同。

但有一处**确实是遗漏**：原版 `Q1t` 失败时（返回 null）会回落到 `clientRunId` 或新生成（`g8`），**并继续发请求**；本仓库在 enforced 失败时直接终止请求（`perch_proxy.go:253-259`）。这一点与原版 `wqr` 的 enforced 语义一致（enforced 时原版也抛 `g2`），**无差异**。

---

## 风险排序

| 级别 | 项 | 说明 |
|---|---|---|
| **P0** | C2 | runId 注册表与 delta 缓存不含账号；failover 后向新账号声称它没有的前缀。每次跨账号浪费一次 409 |
| **P1** | A4 | `DropTicket` 未接线；坏票最长存活 5 分钟 |
| **P1** | A3/C3 | `UseTicket` 未接线；配额计数与 `Remaining()` 全部失真 |
| **P1** | D1 | 票的 `runId` 从未使用；若服务端校验则会被拒（无法确定） |
| **P2** | B2 | 死代码，删除安全 |
| **P2** | B1 注释 | `runid.go:104` 注释描述的行为不存在 |
| **P2** | B4 | `DeltaMaxKeys=32` 远小于 `maxRunIds=4096`，命中率被低估 |

## 对你原始结论的更正

你的 4 条结论**全部成立**，但需要补充：

- **结论 2 不完整**：fast path 确实不打网络，但它保证的只是「取票那一刻还有 30s」，不保证 model-call 飞行期间不过期（A5）。
- **结论 4 正确但不充分**：`RunIdTTL` 内复用是 delta 触发的前提，但**前提之上还有一层作用域问题**——复用是无条件的、跨账号的（C2），这才是缺陷所在。
- 你未提及但同样重要的：**配额计数（`UseTicket`）整条链路未接线**，所以「配额消耗是否符合官方」这个问题在现状下无法回答，因为根本没有消耗记录。

## 缺失的证据

以下无法从仓库确定，需要真实上游行为或日志才能判定：

1. 服务端是否校验「票的 `runId` == envelope 的 `runId`」（影响 D1 的实际严重性）
2. 服务端对「同一 runId 先后出现在两个账号」的反应（影响 C2 的实际代价）
3. 票被明确拒绝时返回的具体状态码与 errorCode（影响 A4 的触发准确率）
4. 原版在续期后 `runId` 是否保持不变（我方未观测到续期响应）

---

**审计结束。按约定，未改动任何代码，等待你确认后再进入阶段三。**
