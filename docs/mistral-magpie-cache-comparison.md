# Magpie / Mistral 缓存实现深度对比

调查日期：2026-11-09（北京时间；2026-11-08 UTC）。

后续实施状态：用户明确“开始吧”，并要求不建Trellis任务、只做必要定向验证。已本地实现普通Mistral绑定持久化、真实session优先于cache hint、Chat驼峰缓存别名及脱敏；一次定向回归和必要构建通过；用户随后授权清理、推送并部署，`2629255`已于2026-11-08 19:44 UTC上线且健康/双内网入口/公网/原配置保留验收通过，自然流量6条普通弱绑定已落盘，未做缓存命中率受控对照。本轮临时克隆/采样/验证产物已按授权定向清理，正式研究记录保留。2026-11-08 19:25:38–19:27:38 UTC自然采样9个DeepSeek请求未发现所检查的标准身份/cache key/动态计费行，同期11条业务日志均为message_hmac（两处计数未逐条对齐），因此未增加提示词清洗，也未改弱身份或子agent策略。下文为实施前固定版本对比证据，不据此宣称生产缓存率已改善。

## 结论先行

**值得借鉴 Magpie 的会话工程，但没有发现一个“接上 Mistral 就自动 91%”的特殊缓存开关。** 本项目已经实现稳定 `prompt_cache_key` 补齐、账号粘性、`X-Affinity` 和原生 thinking 往返；重复加头或把 smart 改成轮流，不足以解决问题。

本轮发现的实质改进点：

1. **Claude Code 动态计费行可能破坏整个前缀**。Magpie 在 Claude→非 Claude 的转换链路中精确移除它；本项目不处理。若实际流量含该行，这是优先级最高的候选；尚未确认当前生产流量是否含它。
2. **普通 Mistral 会话绑定未跨重启持久化**，Magpie 有。该缺口已由本地探针和历史发布样本分别证实。
3. **本项目正文 cache key 的优先级高于标准会话头**，会把“缓存路由提示”与“会话身份”混用；具备稳定 Session-Id 也可能因 cache key 变化重新绑定。
4. **弱身份依赖首 system/user 的前 4096 字节**，既容易受动态 system 干扰，也会把开头相同、后续不同的会话合并。应优先提高客户端显式身份覆盖，而非扩大弱推断。
5. **OpenCode camelCase 缓存键没有兼容**；Claude 子 agent 也没有独立 affinity 记录。是否影响当前生产，取决于客户端版本、协议和头字段。
6. 应增加**会话级、实际出站请求级的缓存诊断**，再做受控链路对比，而不是仅凭单次请求的命中百分比调参。

这些都不等于已确定全部生产根因。此前已有“相同账号、相同 1001 条消息，连续三次仅 cached=256”的证据；修复身份和持久化无法充分解释这一类现象。

## 1. 调查边界与基线

- 本地：`account-pool-balancer`，调查开始时 HEAD=`16577f3`；Mistral 业务实现含 `3ec9611`。工作树开始时干净。
- Magpie：公开仓库 [yetone/magpie](https://github.com/yetone/magpie)，固定提交 **`b684308c6181aa2304e4c54ab9aac9f8237fbca5`**，提交时间 2026-11-09 02:47:26 +08:00。不是浮动 main 的二手介绍。
- NewAPI：只补充核对本机相邻源码 `3914bf70209ecae0fe305ba6a7cdc9bad3fa0949` 的 Claude→Chat 转换；不冒充线上精确版本或生产实测。
- 官方：重新读取 [Mistral prompt caching 文档](https://docs.mistral.ai/inference/conversations/advanced/prompt-caching)。
- 实验：本项目 7 个合成探针；Magpie 4 个合成探针及 8 个原有定向测试，全部通过。均为纯本地/HTTP Mock，不是实际缓存命中实验。
- 本轮**未 SSH 生产、未读取新的生产流量、未改业务代码/配置/详情开关、未主动推理、未部署**。引用的生产数字来自已归档历史调查，并非当前最新读数。
- 截图无法确认朋友的 Magpie 版本、模型、Key 数量、客户端、统计窗口和自定义配置，因此无法直接验证其 91% 的归因。

本项目不含可执行的 `.trellis/scripts/`；本次只读研究使用正式报告与 Trellis 会话记录，不新建实现任务、不改其他任务状态。

## 2. 朋友的“轮流 + 整个会话”究竟做了什么

截图中的“会话保持选择整个会话”对应 Magpie 的 `AffinitySession`；“轮流”对应 rotate 路由。这不是每次请求换 Key，而是：

```text
新会话：按路由策略选账号
同一会话后续请求：优先留在原账号
账号不可用/容量策略触发：按约定等待或故障转移
```

`affinity.go:543–707` 中，`AffinitySession` 的分支先于 `rotate` 的新轮次分支，因此整个会话模式不会因为轮流策略而每轮换号。默认 Auto 还会根据上一轮缓存读写量、工具循环和冷却时间决定保留与否；Mistral 没有独立的 Auto 冷缓存时长，走通用 5 分钟。这个 5 分钟是 **Magpie 的客户端路由假设，不是 Mistral 官方 TTL 承诺**。Session 模式也仍受账号可用性等前置条件约束，不等于 Strict。

本项目 `pool.go:841–902` 同样先找有效粘性绑定，再进入 `routes.Order`。所以**已有有效绑定时，smart 并不会每次重新挑一个余额最高的账号**。换成轮流只改变未绑定时的分配，不能直接解释或保证 69%→91%。

截图中的“套一层”也应按位置区分：客户端前面的 Magpie 可能通过客户端配置和协议转换提供稳定身份/正文；已经经过 NewAPI 后再套一层，无法恢复早已丢掉的原始会话标识。若作为普通自定义地址接入本中转，Magpie 的缺失 key 生成路径又可能不同于 Mistral 预设。建议移植已证实有效的窄处理，而不是先增加一个常驻网关并期待命中率自然提升。

## 3. 能力对照

| 维度 | Magpie 固定版本 | 本项目当前实现 | 判断 |
|---|---|---|---|
| Mistral 上游协议 | 预设声明 Chat；Messages/Responses 客户端可转换到 Chat | Chat 和原生无状态 Responses 等；没有本池 Anthropic 转换 | 路径不一定相同，比较时必须控制 |
| 客户端已有 cache key | 解析/转换时保留，Chat builder 写入 | 原字段保留，包括 null/空串 | 核心能力已有 |
| 缺失 cache key 自动补齐 | `ownCacheKey` 排除 vendor preset；Mistral 预设本身不自动生成，可由客户端/extraBody/按地址添加路径提供 | 已识别身份时自动补 HMAC key | 不能声称 Magpie 预设在这点更完整 |
| 特殊 X-Affinity | 在本轮核对的内置 Mistral 请求路径未发现自动注入；用户可设自定义头 | 已从最终非空 cache key 补齐，不覆盖客户端值 | 不应再重复“加这个头” |
| 会话保持 | Session/Turn/Auto/Strict，及路由与故障约束 | sticky + TTL + busy 策略，绑定先于路由 | 原理相同；默认策略不同不代表 Magpie 必然更好 |
| 跨重启普通绑定 | `affinity.json`，保存最近最多 512 个、24h 内的记录；0600、原子替换、合并写入 | Mistral 普通绑定只在内存；持久化只导出满足 source 条件的特定绑定 | 明确可改进 |
| 无显式身份 | 完整首 user 开场，部分 agent 场景继续读到真正用户开场；不依赖首 system | 首 system/developer + 首 user，各截 4096 字节 | 当前弱身份更容易受动态开头干扰 |
| Claude 动态计费行 | 非 Claude 目标的转换路径精确移除 | 保留在实际 Chat 出站正文 | 条件性高影响缺口 |
| OpenCode 配置 | 生成 setCacheKey / supportsPromptCacheKey；兼容 promptCacheKey→prompt_cache_key | 能识别标准头/蛇形 key；不兼容该驼峰别名 | 客户端相关缺口 |
| Claude 子 agent | affinity 记录加 agent-id；额外字段支持 sessionId/subagentId 模板 | 多数情况共享父/主会话身份 | 应区分跟踪与账号亲和，不宜粗暴全拆 |
| 思考历史 | reasoning_content 等转回 Mistral thinking 块 | 已支持原生 thinking 往返及精确模型兼容 | 不是新的主要缓存差距 |
| 用量显示 | 内部 Input 是未缓存部分，CacheRead/Write 分列 | Mistral Input 是含 cached 的完整输入 | 表面数字不同，正确比例可等价 |

### 关于“Magpie 自带缓存键”的重要限定

`gateway.go:8152–8205` 的 `ownCacheKey` 只对指定路径生成缺失键：OpenAI 自有 API、用户按地址添加的 provider 等；明确排除一般 vendor preset。Mistral 的 preset 位于 `provider/presets.go:452–454`，只声明 Chat 地址。

因此以下几种接法不能混为一谈：

- 选择内置 Mistral 预设；
- 将同一地址当普通自定义 provider 添加；
- 客户端自己带 `prompt_cache_key`；
- 用户用 `extraBody` 配置 `${sessionId}`；
- 使用 `!prompt_cache_key` 强制覆盖。

本轮 Magpie 合成探针确认：Mistral preset 的 `ownCacheKey=false`。朋友没有提供配置，不能由其高命中反推出某条代码必然执行。

## 4. 重点改进：动态前缀，而非继续加缓存头

### 4.1 Claude Code 的动态计费行

Magpie `billing_line.go` 处理的是 system 开头这类专用标记：

```text
x-anthropic-billing-header: cc_version=...; cc_entrypoint=...; cch=...;
```

其中 `cch` 随轮次变化。它若成为其他模型提示词的最开头，即使后面十万 token 不变，也可能从开头打断可复用前缀。Magpie 源码注释记录了其他上游上“移除前约 4%、移除后 96–99%”的用户案例；**这是其维护者记录的 DeepSeek/Command Code 案例，不是本轮 Mistral 受控实验**。

Magpie 的处理不是删所有动态信息：

- 只识别明确的计费标记块/起始行；
- Claude 模型走 Anthropic Messages 时保留；
- Claude→其他目标协议/模型的转换路径移除；
- 用户正文中的引用、普通规则、后续内容不全局替换。

本项目 `prepareMistralRequest` 不处理该标记。本机 NewAPI 的 `ClaudeMessagesRequestToOpenAIChat` 对普通目标会拼接 system 文本，没有在该函数里剥除该行；所以它有可能被完整搬进 Chat 的首 system。**这只是静态可行路径，尚未证明当前线上配置/实际流量一定如此。**

本轮本地 HTTP Mock 已证实：

1. 固定 Session-Id，两个请求只改 cch；
2. 最终 `prompt_cache_key` 和 `X-Affinity` 相同；
3. 实际出站首 system 仍不同、计费行仍在。

如果没有显式身份，另一个探针还确认 cch 会改变 `message_hmac`，进而改变生成 cache key：同一个动态行可能同时伤害**前缀稳定性和账号绑定稳定性**。

**建议**：先按真实客户端做有界、脱敏的“是否存在/位置/是否变化”诊断；若命中，最好在 Claude→Chat 的转换边界做精确处理。本项目接收的已是 Chat 时，可评估 Mistral 专属、明确 opt-in 的窄兼容。不能因本报告直接把删 system、删日期、删思考或全局文本清洗当成修复。

### 4.2 显式身份覆盖应作为指标

本项目支持很多标准头，问题不是再写一长串别名就一定解决。此前固定窗口中约 96.94% 请求走 `message_hmac`，说明真实客户端身份没有稳定到达被消费的位置。

Magpie 不只在网关被动识别，还能管理部分客户端配置。例如 OpenCode 生成 `setCacheKey=true`，新版迁移为 `compatibility.supportsPromptCacheKey`。用户明确自设 false 时保留。不同版本并非都读取同一个参数。

建议维护 Codex / Claude 经 NewAPI / pi / OpenCode 的**版本化接入矩阵**，按实际请求验证：客户端发出 → NewAPI 保留 → Balancer 识别 → 最终 cache key 稳定。不得用 UA 名称、随机请求 ID 或给所有用户固定一个头代替会话身份。

Nginx 下划线头问题已在此前发布处理；不能引用旧排查再次宣称它仍未修复。

## 5. 明确的本地工程缺口

### 5.1 普通 Mistral 绑定跨重启保留

本项目 `pool.ExportState` 只导出 `confirmed && source != ""` 的绑定；普通 Mistral 会话不满足 source 条件。本地探针确认内存能查到绑定，导出却没有记录。路由 HMAC secret 已有元数据持久化，不应误说“每次重启 secret 都随机变了”；丢失的是账号映射。

Magpie 保存最后应答者、模型、轮次与缓存信息，重启后按相同身份读回。记录最多保存 512 条、24h 只是记忆边界，是否继续亲和仍要经过相应模式/可用性检查，不是把上游缓存保存到本机。

建议复用现有元数据持久化，而非另造一套存储：

- 只保存已确认的普通 Mistral 绑定，不保存 provisional 或原始提示词；
- 保留分组/下游 Key 的 HMAC 隔离及绝对到期时间；
- 检查账号删除/禁用/换 Key、Base、代理、分组变更后的失效；
- 保留容量上限和 LRU 删除落盘；
- 重启不把旧过期记录重新续期；不恢复在途/RPM lease；
- 先支持同实例重启，不把它包装成已解决多副本一致性。

这是最确定、影响边界最清楚的改进之一，但只减少发布/重启冷启动，不能解释同进程稳定绑定下的低命中。

另一个不应直接照搬的差异是 Magpie 的自适应兼容重试：其部分路径会在上游拒绝可选字段后去掉字段再次请求。那是在提高兼容成功率，并不证明缓存更好；本项目应继续保留“不静默丢失请求意图、不重放结果未知/已开始输出请求”的边界，不能为追求表面成功率或缓存率广泛启用删除字段重试。

### 5.2 会话身份与 cache hint 的优先级

`identity.go:63–72,164–211` 中，body `prompt_cache_key` 在标准 sessionHeaders 之前。探针证明：同一个 Session-Id，正文 key 从 prefix-a 改为 prefix-b，会生成不同绑定身份。

Magpie affinity 优先使用原生会话头；cache hint 与 affinity 并不完全混作同一字段。本项目自己的 `tokenHarborIdentity` 已有“真正 session 优先、cache key 回退”的做法，可借鉴而不改其他池合同。

建议 Mistral 单独评估优先级：明确的自定义/父会话策略 → 真正会话标识 → cache hint → 弱身份。必须保留调用者已有 `prompt_cache_key` 原值；**调整本地选号身份不等于覆盖上游缓存键**。如果客户端本身每轮换上游 key，优先修客户端。

迁移时已有绑定可能产生一次冷切换，应有测试和发布说明；不是无条件修改全局 identity 顺序。

### 5.3 4096 字节弱身份

本轮本地探针确认：

- 只改首 system 的动态内容，会改变 fallback 身份；
- 两段首 user 前 4100 字节一致、只在后缀写不同任务，仍得相同 fallback 身份。

Magpie 在相同合成输入下：改 system 不改变 conversationID，改首 user 的长后缀会改变 ID。但它也不是完美会话恢复：它的部分单消息路径按原始消息 JSON 哈希，客户端重编码/同开场/分叉仍有歧义。

建议顺序是：显式身份 → 缺失告警/接入修复 → 必要时版本化改进弱身份。不能简单把 4096 改成更大就声称解决，也不应该读取本地客户端会话文件去给公共中转站猜身份。

### 5.4 OpenCode 的 promptCacheKey

Magpie `cacheKeyCamel` 将 `promptCacheKey` 改为 `prompt_cache_key`，已有蛇形 key 优先。对应客户端配置也由其写入。

本项目探针确认：驼峰 key 没被路由解析消费；出站正文仍带驼峰字段，同时另补 fallback 生成的蛇形字段。对于严格 schema，这还可能触发未知字段拒绝；本轮未发真实 Mistral 请求验证拒绝。

建议按 Mistral Chat 范围增加受测试的别名兼容，放在身份解析前；明确 null/空值、重复键、转义键和两种拼写冲突策略，保留大数/工具/未知字段；不要把其他 provider 的原生正文一起重写。经 NewAPI 的 DTO 可能先丢掉该别名，修复层级须由端到端形态决定。

### 5.5 子 agent 与父会话

Magpie 的 `stickKey` 以 `session + agent-id` 分开记录主/子会话，但还能按策略让子会话跟随主账号；额外字段可用 `${sessionId}:${subagentId}`。本项目目前同一 Claude session 的子 agent 通常共用绑定，父会话头还有更高优先级。

本轮双方探针分别证实该差异。但本项目没有 Magpie 那套按上一轮缓存量判断 Auto 保持的逻辑，所以**不能照搬它“子会话覆盖主 affinity 导致主会话换号”的全部故障解释**。本项目更直接的风险是不同前缀挤在同一身份上，诊断不清、账号负载集中；同 key 是否争抢上游缓存还要实测。

建议将“逻辑子会话/缓存身份”和“父账号亲和”分开设计，保留默认父子同账号的有效意图；不要简单禁用 agent 或强制每个子 agent 换账号。当前没有此类流量证据时，优先级低于前述项目。

## 6. 为什么不能承诺改完就有 91%

历史 `.trellis/workspace/cache-root-20261108.md` 的固定窗口（2026-11-08 16:32:34.842–17:30:00 UTC，旧 a817edf 进程）记录：

- 658 条成功且 input/cached 真实已知；token 加权 **72.422%**，单请求均值 **48.664%**。
- 未缓存 token 中，新绑定约占 **28.189%**；已绑定且 cached=0 约占 **49.590%**。
- 后续三次相同账号、全部 1001 条消息一致、tools 一致，input=35,808、cached=256，连续三次约 **0.715%**。
- 另有同账号稳定长前缀样本由 0% 到 99.645%，证明上游缓存并非全池失效。
- 发布后普通绑定确实重新建立并改账号，但该样本仍有约 90.90% 缓存；不能把换 Key 简化为必然零缓存。

这是已有生产证据，不是本轮重新测得，也不是朋友那一组 workload。相同模型 ID 亦不证明 Mistral 内部后端/分片始终相同。

官方明确：`prompt_cache_key` 提高命中机会，**不保证命中**；需要兼容的公共前缀。64-token 缓存块，缓存输入按标准输入价的 10% 计费。公开文档没有支持“加 X-Affinity 就锁死真实推理节点”或“Session 模式能指定 Mistral 上游 TTL”的保证。

上述重复消息低命中不能靠去除当前并未变化的计费行来解释。进一步应核对精确出站请求、相关生成参数、最终模型和服务商 trace；分片、驱逐、内部渲染或上报问题仍是待验证假设，不宣布 Mistral 内部存在某个已确认 bug。

## 7. 指标与诊断的改进

### 7.1 统一分母，避免 91% 与 69% 假对比

Mistral Chat `prompt_tokens` 包含 cached。本项目单请求公式是 `cached/input`。

Magpie 内部通常把未缓存 Input、CacheRead 和 CacheWrite 分列，汇总公式为：

```text
CacheRead / (Input + CacheRead + CacheWrite - 未知缓存用量对应的输入)
```

对正常 Mistral 样本，若 Input 已经拆出未缓存部分，两者等价；不能将 Magpie 的 CacheRead 再除以本项目同名但不同含义的 input，也不能把“有任意缓存的请求比例”当作 token 命中率。不要将 affinity 的估计 cacheWrite 当成服务商真实计费写入，它只是路由启发值。

比较至少固定：模型/协议/成功状态、统计窗口、客户端、会话长度、冷启动与热轮次、usage 已知覆盖率。分别报告：

- `Σcached / Σinput`（主要成本相关指标）；
- 请求命中率均值/分布；
- 冷会话首轮与连续热轮次；
- 绑定 hit/miss/overflow 与显式身份占比；
- 失败、取消和未知 usage，不能补零。

### 7.2 不必依赖完整提示词落盘

现有原生交换详情有价值，但有界预览会截断大上下文，且正确地掩码会话字段，不能仅靠它比较整个长前缀。

建议在受限诊断模式中，对**实际发给上游的最终请求**计算分组/调用者隔离的 HMAC 标签与形态统计：

- 会话匿名标签、上一账号与本次账号、重新绑定原因（过期/LRU/发布/不可用/忙溢出）；
- 最终 cache key / X-Affinity 的来源和是否一致，不存原值；
- system、工具 schema、消息前缀的有界指纹，最长相同消息前缀和首个差异位置；
- 计费行是否存在/是否变化、思考/工具集合是否改变、模型/协议；
- 上游可核对的 request ID（严格 allowlist 响应头），input/cached/TTFT。

匿名标签仍具有可关联性，需限权限、限保留/容量，避免与长期原始用户标识关联。标签按最终模型/协议分桶，不把跨池/不同客户端的相似前缀合并。没有服务商 trace 时明确标缺，不拿 Balancer 的 req_* 冒充其查询 ID。

只记录请求 JSON 的空白/键顺序变化不足以证明模型 token 前缀改变；不要盲目对工具数组全局排序或重写正文来“稳定缓存”。

## 8. 建议落地顺序与验收

| 顺序 | 内容 | 验收目标 | 不能承诺的部分 |
|---|---|---|---|
| P0 调查 | 统一口径；核对真实客户端的显式身份和动态前缀；补受限最终请求诊断 | 低命中可分为冷启动/身份变更/前缀变更/绑定稳定仍低命中 | 不改业务即可立刻升到 91% |
| P1 确定性工程 | 普通 Mistral 确认绑定跨重启保留；完善失效/隔离 | 假数据重启后同会话同账号；过期/删号/换凭据正确失效 | 稳定进程内上游低命中全部解决 |
| P1 条件修复 | 真实会话优先于 cache hint；OpenCode 别名及客户端接入配置 | 稳定会话不因 hint 抖动重建；字段往返无损 | 改选号同时改写调用者 cache key |
| P1 有样本才做 | 精确去除 Claude 计费标记的兼容 | 仅 cch 改变时规范化后前缀相同，其余语义/协议不变 | 清洗所有动态提示词或对无该行的请求提效 |
| P2 | 必要的弱身份改进、主/子 agent 的双层身份 | 降低弱身份合并/分裂，保留父账号亲和 | 完美从正文恢复真实会话 |
| P2 受控验证 | Magpie / 当前中转 / 直连上游的小样本对照 | 确定缓存损失发生在哪条边界 | 静态源码或 Mock 代表真实缓存效果 |

受控对比需另获真实推理授权和成本上限：

1. 先确认朋友的精确模型、Magpie 版本、客户端、单 Key/池大小、Session/rotate/extraBody 设置及 91% 的分母。
2. 固定一个允许测试的账号、模型、工具、完整历史、生成参数和稳定会话；先统一 Chat 协议。Magpie Mistral preset 走 Chat，不能拿它与原生 Responses 直接归因比较。
3. 每条路径用独立测试缓存命名空间；冷轮/热轮分开，路径顺序交错，避免一条路径预热后让另一条搭便车。账号、前缀内容和等待间隔控制一致。
4. 先比较保真性，再比较各自热轮次的真实 usage、TTFT 和错误；不重放现有用户私密请求、不做全池刷缓存、不降低思考/丢工具来制造好数字。
5. 只有结果证明当前链路损失，才决定修哪层；若最终请求/身份/账号稳定且直连同样低命中，带服务商 trace 升级给 Mistral。

## 9. 本轮本地验证记录

### 本项目：7 个合成子用例，全部通过

通过 `go test -overlay=... ./internal/server -run '^TestMagpieCacheAudit$' -v -count=1` 加载临时探针，不新增业务源文件。验证当前行为，不表示缺口已修复。

| 合成条件 | 当前可复现行为 |
|---|---|
| Session-Id 固定，只改 body cache key | 身份跟着 cache key 变化 |
| 首 user 固定，只改 system 的 cch | fallback 身份变化；显式 session 可避免身份变化 |
| user 前 4100 字节相同、后缀任务不同 | fallback 身份合并 |
| 同 Claude session，加子 agent-id | 绑定身份不变 |
| 只传 promptCacheKey | 保留驼峰字段，另补 fallback 蛇形 key |
| 已确认普通 Mistral 绑定导出 | 内存可查，持久化 Bindings 中没有 |
| HTTP Mock，固定 Session-Id、只改 cch | cache key/X-Affinity 固定，实际首 system 仍变化 |

### Magpie：4 个合成子用例及 8 个原有测试，全部通过

合成：Mistral preset 不自动补缺失 key、Auto cold=5m；system 变化不改 fallback；4096 字节后的开场差异能区分；主/子 affinity 记录独立。

原有测试：`TestAffinitySurvivesRestart`、`TestAffinityFileBounded`、`TestWithoutBillingLine`、`TestBillingLineLeftOutForVendors`、`TestMistralPartsTranslated`、`TestMistralReasoningAsThinking`、`TestCodexThreadOnChatUpstreamCaches`、`TestOpenCodeCamelCacheKey`。

均只使用 Mock/假数据；没有全仓/race/vet、生产客户端端到端或真实缓存实验。正式研究文档和 Trellis 记录提交；临时克隆/测试/日志登记 `_tmp_artifacts.md`，经用户同意后才清理。

## 10. 可复核源码

以下 Magpie 链接全部固定到调查 revision：

- [Mistral preset](https://github.com/yetone/magpie/blob/b684308c6181aa2304e4c54ab9aac9f8237fbca5/internal/provider/presets.go#L452-L454)
- [缺失 cache key 的生成边界](https://github.com/yetone/magpie/blob/b684308c6181aa2304e4c54ab9aac9f8237fbca5/internal/gateway/gateway.go#L8152-L8205)
- [计费标记处理](https://github.com/yetone/magpie/blob/b684308c6181aa2304e4c54ab9aac9f8237fbca5/internal/gateway/billing_line.go) / [不同目标协议的 Mock 回归](https://github.com/yetone/magpie/blob/b684308c6181aa2304e4c54ab9aac9f8237fbca5/internal/gateway/billing_line_test.go#L119-L175)
- [绑定持久化](https://github.com/yetone/magpie/blob/b684308c6181aa2304e4c54ab9aac9f8237fbca5/internal/gateway/affinity.go#L266-L368) / [重启回归](https://github.com/yetone/magpie/blob/b684308c6181aa2304e4c54ab9aac9f8237fbca5/internal/gateway/affinity_restart_test.go)
- [Session/Auto/Turn 保持判定](https://github.com/yetone/magpie/blob/b684308c6181aa2304e4c54ab9aac9f8237fbca5/internal/gateway/affinity.go#L519-L707)
- [原生会话头与 fallback](https://github.com/yetone/magpie/blob/b684308c6181aa2304e4c54ab9aac9f8237fbca5/internal/gateway/gateway.go#L9361-L9540)
- [OpenCode 驼峰兼容测试](https://github.com/yetone/magpie/blob/b684308c6181aa2304e4c54ab9aac9f8237fbca5/internal/gateway/opencode_cache_key_test.go) / [客户端 setCacheKey 配置](https://github.com/yetone/magpie/blob/b684308c6181aa2304e4c54ab9aac9f8237fbca5/internal/agent/agents.go#L592-L600)
- [额外字段 session/subagent 模板](https://github.com/yetone/magpie/blob/b684308c6181aa2304e4c54ab9aac9f8237fbca5/internal/provider/extrabody.go#L3-L29)
- [多轮前缀保真与缓存用量测试](https://github.com/yetone/magpie/blob/b684308c6181aa2304e4c54ab9aac9f8237fbca5/internal/gateway/prompt_cache_test.go#L43-L85) / [汇总命中率公式](https://github.com/yetone/magpie/blob/b684308c6181aa2304e4c54ab9aac9f8237fbca5/internal/usage/usage.go#L826-L834)

本项目对应位置：`internal/server/identity.go:63–72,164–211,250–294`、`internal/server/mistral_request.go:130–198,373–448`、`internal/server/proxy.go:661–714,1460–1462`、`internal/pool/pool.go:596–668,841–902,1371–1421`、`internal/server/tokenharbor.go:17–37`、`internal/stats/stats.go:215–219`。

历史生产证据：`.trellis/workspace/cache-root-20261108.md`、`.trellis/workspace/mistral-session-performance-session.md`。后者已记录下划线头修复，前者已记录旧 delete 规则不再存在；不要用更早的诊断覆盖这些更新。
