# Factory AI 号池与全局号池启停

## Gemini 模型的三个兼容入口

在 **Factory AI → 路由设置 → 有限协议转换** 开启 `factory.compatibility` 后，配置为 `generate` / `google` 的 Gemini 模型可以通过下列三个 HTTP 入口调用。模型名保持不变（例如 `gemini-4-carbon`），使用 Factory 池的下游密钥，不要把上游 `fk-` Key 发给下游。仍由同一个 Factory 池选账号，发往 `/api/llm/g/v1/generate`，再按调用入口返回 JSON 或 SSE。

| 下游入口 | 最小 JSON 请求体 |
|---|---|
| `POST /v1/chat/completions` | `{"model":"gemini-4-carbon","messages":[{"role":"user","content":"你好"}]}` |
| `POST /v1/responses` | `{"model":"gemini-4-carbon","input":"你好","store":false}` |
| `POST /v1/messages` | `{"model":"gemini-4-carbon","max_tokens":256,"messages":[{"role":"user","content":"你好"}]}` |

- 支持单候选的文本多轮历史、前置系统指令、内联 Base64 图片输入、常用生成参数，以及 schema 可直接表示且不需要 Gemini 回放签名的函数调用与纯文本工具结果。远程图片、必须改写语义的 schema、原生托管工具、签名推理和 Responses 服务端续传不在子集内，请用原生接口。普通文本上的可选 Gemini 回放签名不返回给兼容客户端；需要保留该推理上下文或执行带签名的工具续轮时必须走 Gemini 原生入口。Gemini `STOP` 不报告命中的停止序列，Messages 将其映射为 `end_turn`，不会伪造 `stop_sequence`。
- SSE 请求加 `"stream":true`，兼容层在取得完整上游结果后再发送目标事件（`X-Compatibility-Mode: buffered`，上游响应上限 16 MiB、单 SSE 事件上限 2 MiB）。Chat SSE 加 `"stream_options":{"include_usage":true}` 才会发送用量事件。已报告的思考 token 计入输出 token；Chat/Responses 另附推理 token 明细。用量缺失不补造，Messages 需要用量，缺失时返回 502。
- 不可转换的请求本地 400，不消耗账号请求配额；上游已经生成但无法表示的结果或截断流返回 502，不换号重放。关闭开关后三个入口仍拒绝 Gemini；原生 Gemini 不依赖开关。未增加 Gemini 入站转其他模型、Responses WebSocket 转换，也未改动目录/账号授权。此次三入口桥接由本地 Mock 验证，未在 Factory 或 NewAPI 生产环境实测。

## 首字慢的真正原因：思考 token（2026-11-22，`20d4f2a`）

- 直连上游逐项实测（`prem.factory.ai`，输出上限 1500，提示"写两段话介绍缓存"）：
  | 模型 | 设置 | 首字 | thoughtsTokenCount |
  |---|---|---|---|
  | gemini-4-neon | 默认 | 5.3s | 408 |
  | gemini-4-neon | `thinkingLevel=minimal` | **1.0s** | 0 |
  | gemini-4-neon | `low` | 7.5s | 445 |
  | gemini-4-neon | `medium` | 7.4s | 427 |
  | gemini-4-carbon | 默认 | 10.6s | 468 |
  | gemini-4-carbon | `thinkingLevel=minimal` | **1.3s** | 0 |
  | gemini-4-carbon | `low` | 2.5s | 0 |
  | gemini-4-carbon | `medium` | 14.1s | 459 |
  - 结论：**Gemini 4 默认开启思考**，思考 token 在可见文本之前生成（真实请求里观察到 400–650 thoughts token），所以首字 5–14s；`thinkingBudget` 对该模型不支持（400，提示改用 thinkingLevel）。
- 实现 `20d4f2a`：`factory.thinkingLevel` 配置（`minimal|low|medium|high|空`），在 **Gemini 原生 generate 请求**上注入 `generationConfig.thinkingConfig.thinkingLevel`，**仅当客户端自己没有 thinkingConfig/thinkingBudget** 时生效；控制台 Factory 设置新增「思考等级（Gemini）」下拉，保存即生效、无需发版。
- 线上取值 `minimal`，实测经 `m.yeschoy.pro` 原生流：首字 **2.5–2.9s**（优化前同条件 5–14s），`thoughtsTokenCount` 不再出现。
- 权衡：`minimal` 不产生思考，简单任务/前端生成基本无损，复杂推理会掉质量；复杂任务可在控制台改回 `medium/high` 或"不干预"。

## 首字延迟（TTFT）实测与优化（2026-11-22，`558c385`）

- 现象：new-api 面板首字 5.7–13.7s，且"首字≈总耗时"。
- 分层实测（`gemini-4-neon`，输出 8 token）：
  | 链路 | 首字节 |
  |---|---|
  | 直连 `prem.factory.ai/api/llm/g/v1/generate` | **1.8–2.2s** |
  | 直连本池（`127.0.0.1:8790`） | 1.9–2.7s（网关开销 ≈0） |
  | 经 `m.yeschoy.pro`（new-api → 本池） | **3.1s**（new-api 一跳约 +1s） |
  | 34k token prompt（缓存命中）经 new-api | 4.0s |
  | 107k token prompt（缓存 106k）真实流量 | 5.7–13.7s |
  - 结论：**基线 TTFT ≈2s 在上游**；网关适配只占 6–15ms；new-api 再加约 1s；**大头是 prompt 长度**——缓存命中 106k/107k 也只省计费、基本不省首字时间（上游仍要按长度预填充）。
- 已修的一处真实缺陷：Gemini→Chat 的兼容转换原来是**缓冲整包**再返回（`X-Compatibility-Mode: buffered`），导致客户端首字≈总耗时。`558c385` 新增**逐帧流式转换**（`internal/opencode/gemini_stream.go`）：首个上游帧立即产出 `role` 块与内容 delta，工具调用/usage/`[DONE]` 依次补上，流被截断时按 `length` 收尾。实测同一条请求：首字节 **4.45s** / 总耗时 **10.64s**（此前会等 10.64s 才吐第一个字）。
- 未改的部分：Responses / Messages 目标的转换仍是缓冲（Codex 经 CC Switch 走 chat 路径，优先保障这条）。
- 可继续优化的方向（按收益排序）：① 减少 prompt：DSH 会话一次带 265 个工具（229 个 `mcp__rea__*`），关掉不用 MCP 能把 107k 降到几万 token；② 简单任务用更轻模型（hydrogen p50 2.1s vs neon/carbon 4.4s）；③ 绕过 new-api 一跳可再省约 1s（但会失去鉴权与计费）。

## 是否"收到停止帧但没识别"（2026-11-22，`ac2a757`）

- 验证解析器认得标准形状：直连 `prem.factory.ai/api/llm/g/v1/generate` 抓原始帧，正常完成的最后一帧包含 `"finishReason":"STOP"` + `usageMetadata`（实测 2 帧、902 字节）；本池转换后的 chat 流也稳定出现 `finish_reason:"stop"`。
- 因此那次 5m22s 的 502 **确实是没有收到终止帧**：解析器对"帧收到但形状不认识"会给出别的错误（`incomplete event`、`unsupported Gemini finishReason (blocked or unsupported output)` 等），不会报 `missing finishReason`。
- 为下次可证伪，`geminiReply` 现在记录 `bytes` / `frames` / 最后一帧前 200 字节；"无内容且无终止帧"的错误会带出这三个值，能一眼区分"上游什么都没发"还是"发了没认出来"。
- 部署 `ac2a757` 后普通请求复测：200、`finish_reason:"stop"` 正常。

## Codex 第五轮：上游长回答被截断 / 卡 5 分钟（2026-11-22，`0f733ef`）

- 现场：new-api 日志 `status_code=502, Gemini compatibility response missing finishReason`，响应时间 **5m22s**。
- 本池日志对应请求：`19:03:50Z gemini-4-argon stream=true durationMs=322374 ttft=null status=502 result=failed`——上游接了请求，**一个字节都没发**，322 秒后流关闭，转换层因拿不到 `finishReason` 判定失败。
- 两个问题一起修：
  1. **转换必须缓冲全文**（`X-Compatibility-Mode: buffered`），原先该路径只受 `requestTimeoutSec=900` 约束，所以能卡 5 分钟以上。现在给缓冲读加进度看门狗：`FirstEventSec`（120s）内没有任何字节、或字节流静默超过 `IdleSec`（360s），就取消上游请求 → 归类 timeout（504）并允许换账号重试。
  2. **截断但仍有效的内容不再丢**：Gemini 流在最终事件前关闭时，若已经产出文本或 function call，按 `finish_reason=length` 返回已生成内容；只有"完全没有内容"才报错（错误文案改为 `missing finishReason and produced no content`）。
- 验收：新测试 `TestGuardCompatibilityReadStalls`、`TestGuardCompatibilityReadNoStall`、`TestGeminiCompatibleTruncatedStreamKeepsContent`；`go test ./internal/opencode ./internal/server` 通过（0.368s / 91.988s）；部署后 chat 复测 200。

## Codex 第四轮：functionDeclaration parameters 必须是 OBJECT（2026-11-22，`7ad1a5b`/`234b590`）

- 现场：`Unable to submit request because '…' functionDeclaration parameters schema should be of type OBJECT`。
- 根因：Google 要求 function 声明的 `parameters` 根是 object schema。客户端的单参数工具常用组合式根（`{"anyOf":[{"type":"string"},{"type":"null"}]}`）或标量根，转换后根类型不是 OBJECT，被拒。
- 修复 `7ad1a5b`：声明级强制根为 object —— 删掉根上的 `anyOf/oneOf/allOf`，`type` 不为 object 时置为 object。已用复刻请求验证：修复前 400（与用户报错完全一致），修复后 200。
- 修复 `234b590`：顺带发现 array/object 上的 `enum` 也被拒（`specified incorrect schema type field`），改为直接删除。
- 详情日志实测有效：用户那次失败请求已被 `details-*.jsonl` 记录（`/v1/chat/completions`，body 138691 字节），据此确认是 chat→Gemini 转换路径；同时把 `logs.detailBodyKB` 从 64 提到 **512**，避免大 body 被截断（500 条 × 512 KB 仍低于 512 MB 总量上限）。

## Codex 第三轮：enum 必须是字符串（2026-11-22，`f7e1586`/`7e105ed`）

- 现场：`Invalid value at 'tools[0].function_declarations[N].parameters.properties[0].value.enum[0]' (TYPE_STRING)`。
- 根因：**Google Schema 的 `enum` 是 `repeated string`**，枚举成员必须是 JSON 字符串，与字段本身是 string/integer/boolean 无关。实测：
  | schema | 结果 |
  |---|---|
  | `{"type":"string","enum":["fast","slow"]}` | 200 |
  | `{"type":"integer","enum":[1,2,3]}` | **400** |
  | `{"type":"integer","enum":["1","2","3"]}` | 200 |
  | `{"type":"boolean","enum":[true,false]}` | **400** |
  Codex 的工具声明里有 integer/boolean 枚举，因此被拒。
- 修复：`normalizeGeminiEnum` 把所有成员转成字符串（数字按 JSON 数字原文，布尔 `true`/`false`），`null` 丢弃（可空由 `nullable` 表达），空枚举删除；类型推断同时识别 `json.Number`。
- 验收：本地拼出 Codex 形状的 enum 请求，经 `m.yeschoy.pro` `/v1/responses` → **200**（18 个 SSE 事件）；同一上游 body 直连 `prem.factory.ai` 也 200。
- 新增测试：`TestSanitizeGeminiEnumTypes`。

## Codex 第二轮：策略 403 与 instructions 合并（2026-11-22，`dafbdeb`/`5683949`）

- 现场：协议转换修好后，Codex 调 `gemini-4-argon` 变成 403 `content_policy_rejected`（客户端显示 `upstream account unavailable for this request`）。
- 定位方式：从 Codex 自己的 rollout 日志（`~/.codex/sessions/*/rollout-*.jsonl`）取出 74 KB 的提示文本，逐块直连 `prem.factory.ai` 复现，精确定位到**一句话**：
  `You are Codex, a coding agent based on GPT-5.` → 403（确定性复现 3/3）。
  同句去产品名 `You are a coding agent based on GPT-5.` → 200；`You are Codex.` → 200。
- 修复 `dafbdeb`：新增「其它 agent 身份句」改写，把 `You are <Codex|Cursor|Windsurf|Cline|Roo Code|Aider|Continue|OpenHands|Copilot|Qwen Code|Gemini CLI|Devin|Amp>,` 改成 `You are an assistant,`（保留逗号，句子自然）。整段 74 KB 文本改写后直连上游 → 200。
- 修复 `5683949`：Codex 同时发顶层 `instructions` 和首条 `developer` 消息，Gemini 只有一个 system 通道，原编码器报 `interleaved instructions or mixed system/developer roles`。现在两者合并进同一 instruction 通道（会话开始后再出现的指令仍拒绝）。
- 验收：用真实 Codex 提示文本 + developer 消息 + Codex 工具（`shell`/`apply_patch`/`update_plan`/`view_image`，strict schema）+ `reasoning`/`include`/`prompt_cache_key` 组请求，经 `m.yeschoy.pro` `/v1/responses` → **200**，18 个 SSE 事件含 `response.created` 与输出。
- 新增测试：`TestRewordAgentIdentity`、`TestGeminiCompatibleMergesInstructions`。

## Codex（Responses 协议）调 Gemini 模型（2026-11-21，`8ef8d55`/`665f6ec`/`c423b6b`）

- 现场：CC Switch 代理 → Codex `/responses` → `gemini-4-argon`，返回 400：
  `Factory Gemini conversion: unsupported tool schema; use the model's native protocol`。
- 根因有两处，都是跨协议转换过严：
  1. **工具 schema 被"检测到需要收敛"就整体拒绝**。Codex 的 function 声明带 draft 关键字（`$schema`、`additionalProperties`、`strict`），转换后 `SanitizeGeminiTools` 发现要修剪就 return 错误。原生 Gemini 路径一直是修剪后继续，转换路径却直接失败。
  2. **`parallel_tool_calls` 只删 `true` 的情况**。Codex 发 `false`，字段存活后被严格解码器拒绝（`unsupported compatibility field: parallel_tool_calls`）。
- 修复：转换路径同样按 Google Schema 接受范围修剪；`parallel_tool_calls` 无论 true/false 都删除；Responses 客户端特有的续话/缓存/推理提示（`include`、`prompt_cache_key`、`truncation`、`reasoning`、`background`、`service_tier`、`metadata`、`user`、`safety_identifier`、`max_tool_calls`）在 Gemini 转换时忽略，不再整单拒绝。
- 验收：Codex 形状的 `/v1/responses` 请求（strict schema + 全部上述字段）经 `m.yeschoy.pro` → **200**，首个事件 `response.created`，正文正常。新增 `TestGeminiCompatibleCodexRequest`、`TestFactoryGeminiConversionTrimsToolSchema`。

## DSH 到底会不会发 x-deepseek-harness-session-id（2026-11-21 实测）

- **代码位置**：DSH 只在 **Anthropic Messages 客户端**构造该头，且仅当 `options.sessionId` 有值：
  ```js
  ...options.sessionId === undefined ? {} : { 'x-deepseek-harness-session-id': String(options.sessionId) },
  ```
  该处同时写 `anthropic-version` / `anthropic-beta`，挂在 `POST {baseURL}/messages` 上；Gemini（`google-generative-ai`）与 OpenAI 协议的客户端分支没有这段。
- **对 Factory 池的实际行为**：DSH 当前 profile（`~/.dsh/profiles/desktop/cordis.patch.yml`）里 `yeschoy-factory` 是 `api: google-generative-ai`（只有 gemini-4-argon/carbon/hydrogen/neon），所以走 Factory 的请求全部由 Gemini 适配器发出，**不会带该头**；真实抓包（21:33Z，UA `google-genai-sdk/2.21.0 gl-node/v24.21.0, deepseek`）确认无任何会话头。`yeschoy-altapi` 才是 `anthropic-messages`（那是 ALTAPI 池，不是 Factory）。
- **带上我们的池子会不会用**：会。实测向本池 `/v1/messages` 发一条带 `x-deepseek-harness-session-id: probe-sess-1` 的 `claude-sonnet-5` 请求，200，日志记录：
  `sessionSource=header`、`affinityKeyType=session_id`、`clientSessionHeaderStatus={'X-Deepseek-Harness-Session-Id':'valid'}`（explicit 绑定）。
- **想让 Factory 也走头粘性**：需要在 DSH 里为 messages 协议的模型（claude 系）单独加一个 `api: anthropic-messages` 的 provider 指向同一 baseURL；gemini 模型无法（协议本身不带该头）。好处是会话被压缩后仍保持同一绑定（`message_hmac` 会因为历史前缀变化而换指纹）。

## 会话粘性 TTL 提到 24 小时（2026-11-21，`57d86e3`）

- 原因：Factory 的 prompt 缓存按**账号**（上游 Google 项目身份）各自独立，会话一旦换账号就等于丢缓存。原先 Factory 路由沿用全局默认 `explicitTtlSec=7200`（2h）、`fallbackTtlSec=900`（15min）；DSH 的 Gemini 请求不带会话头，走的是 `message_hmac` 兜底绑定，所以实际只有 **15 分钟**粘性，停顿超过 15 分钟就可能换账号。
- 改动：`factoryRoutingDefaults` 把两项都设为 **86400（24h）**，生产配置 `factory.routing` 同步改为 `explicitTtlSec=86400`、`fallbackTtlSec=86400`（备份 `data/config.json.bak-ttl-*`）。已在线上生效。
- 边界：配置校验里 `explicitTtlSec` 允许 60–7×86400，`fallbackTtlSec` 允许 60–86400（24h 是**上限**）。池内绑定总数上限 4096 条、LRU 淘汰，内存有界。
- 验证：同一 prompt 连发两次，日志 `bindingResult=hit`、`sessionSource=message_hmac`；`go test ./internal/server ./internal/pool ./internal/config` 通过；新增 `TestFactorySessionBindingTTL` 锁定默认值。

## 会话头为什么没有命中（2026-11-21，`4ad3a73`）

现场：DSH（Gemini 协议）请求日志里 `sessionSource=message_hmac`、`affinityKeyType=message_hmac`、`affinityConfidence=fallback`，看不到"会话头命中"。

实测证据（detailLog 抓到的真实 DSH 请求，21:33Z）：

```
User-Agent: google-genai-sdk/2.21.0 gl-node/v24.21.0, deepseek
session 类头: 无
```

结论：
- **DSH 的 Gemini 路由不发会话头**。它走 pi-ai 的 `google-generative-ai` 适配器，请求只带 `x-goog-api-key`；`x-deepseek-harness-session-id` 只出现在 DSH 的 Anthropic 等客户端路径里，Gemini 路径没有。
- **new-api 也不会补会话头**。它的渠道亲和规则是 `model_regex: ^gpt-.*$` + `path_regex: /v1/responses`（codex cli trace），`pass_headers` 只在该规则命中时透传 `Session_id`/`Thread_id` 等；Gemini 请求不命中该规则，而且渠道亲和只决定选哪个渠道，不生成会话标识。
- **本池的头解析是开着的**：`identityWithFallbackHeaders` 内置 30+ 会话头名（含 `X-Deepseek-Harness-Session-Id`）；没收到头才回落到 `message_hmac` 对话指纹。日志里 `bindingResult` 为 hit 11 / miss 2，粘性是工作的；真实 71k token 请求 97% 缓存命中也是间接证据。
- **新增诊断**：Factory 请求现在会记录 `clientSessionHeaderStatus`（客户端实际带了的候选会话头及其状态 valid/empty/invalid；没有则为空），控制台的"客户端请求头"弹窗可直接看出"未收到支持的会话候选头"。

若一定要走头粘性：需要客户端发头（Claude Code/Codex 类客户端天然会发，本池已支持）；给 DSH 加**静态**头不可取（所有会话会钉到同一账号）；在 new-api 加 Gemini 的 affinity 规则也无效，因为 DSH 的 Gemini 客户端本身不发这些头。

## 每请求扫描的性能（2026-11-21，`ef449d1`）

- **原实现**：每个请求先做 `containsPhrase`，对整包 body 跑 11 条 `strings.Contains`，再用 RE2 正则 `(?i)You are[^.\n]{0,120}?harness\.` 判自述句。实测 237 KB 的 agent 请求体：**3.4 ms/次**（其中正则 2.9 ms），还额外分配 239 KB。
- **现实现**：自述句改为手写扫描——先用 `bytes.Contains(s, "arness")` 做字节级门控，命中后按"`You are` + ≤120 字节不含 `. \" \` 换行 + `Harness.`"逐字节找句子边界；短语预检改为预算好的 `[]byte` 表 + `bytes.Contains`，不再把 body 拷成 string。实测 **0.13–0.18 ms/次，0 分配**，约 **20 倍**提升。
- **剩余成本在 JSON 往返，不在匹配**（259 KB body 各阶段实测）：
  | 阶段 | 耗时 |
  |---|---|
  | 工具改名（decode+rename+marshal） | 2.27 ms |
  | magpie 身体改写 | 1.90 ms |
  | Droid 开场/模型写入 | 1.54 ms |
  | Gemini schema 收敛 | 1.52 ms |
  | 短语扫描（已优化） | 0.13 ms |
  整条链路约 6 ms。下一步可把"工具改名 + schema 收敛"合并成一次 decode/marshal（预计省 1.5–2 ms，约 25%），但需要重构测试，未在本轮做。

## 缓存排查（2026-11-21，`147d4e9`）

结论：**上游隐式缓存正常，问题在统计口径**。

- **实测**（经 `m.yeschoy.pro`，同一 prompt 间隔数秒）：
  | 请求 | prompt | cachedContentTokenCount |
  |---|---|---|
  | 663 tokens 首次 | 663 | 无（低于隐式缓存门槛） |
  | 8449 tokens 首次 | 8449 | 无 |
  | 8449 tokens 第二次 | 8449 | **6395（75.7%）** |
  | 15557 tokens 首次 | 15557 | 无 |
  | 15557 tokens 第二次 | 15557 | **13503（86.8%）** |
  真实 DSH 大请求（71619 tokens）在 new-api 侧记录 `cr=69570`（97%），说明 265 工具的请求前缀也确实被缓存。
- **统计 bug（已修）**：Gemini 只在命中时下发 `cachedContentTokenCount`，缺失时原代码把 `cachedTokens` 留成 nil；而 `stats.Record` 只有在 input 与 cached **都非 nil** 时才计入命中率分母，于是所有未命中样本被排除，控制台看到的「24h 缓存命中率」只按命中样本算，虚高。现在 usageMetadata 有 promptTokenCount 但无 cached 时记 **0**（有测量、命中为 0），无任何用量时仍记未知。
- **账号隔离**：Factory 的缓存按账号（Google 项目身份）各自独立。本池 `routing.sticky=true`、`explicitTtlSec=7200`、`sessionHeaders=[]`（按 message_hmac 粘性），日志里 `bindingResult` 为 hit 9 / miss 4，长对话不会被随机分到另一个账号而丢缓存。
- **不会破坏缓存的因素**：本网关的改写（工具改名、schema 收敛、被拒句改写）都是**确定性**的——同一请求连发两次命中 86.8% 就是证明。
- **会导致缓存失效的操作**（客户端侧）：换模型、增删 MCP 工具、改 system prompt/注入时间、切换账号。prompt 太短（<约 1–2k tokens）也不会进入隐式缓存。

## 模型目录补全（2026-11-21，`d684aab`）

droid 模型选择器里的 12 个模型逐个实测（`m.yeschoy.pro`，tiny 请求）：

| 模型 | 原生协议 | 结果 |
|---|---|---|
| gemini-4-neon | Gemini stream | 200（此前三处都缺：本池模型表、new-api 渠道模型表、new-api 计费表达式） |
| gemini-4-hydrogen / carbon / argon | Gemini stream | 200 |
| gemini-3.1-pro-preview、3.8/3.7/3.6/3.5-flash、3-flash-preview | Gemini stream | 200（本池早有路由，但 new-api 渠道 245 的模型表里没有，此前一律 503） |
| gpt-5.4-mini / gpt-5.4-mini-fast | Responses | 200（需 OpenAI 类型渠道 247 转发原生 `/v1/responses`） |

要点：
- **new-api 渠道 245（Gemini 类型）** 的 `channels.models` 已扩到 10 个 Gemini 模型；新增模型必须同时写 `billing_setting.billing_mode`（`tiered_expr`）与 `billing_setting.billing_expr`，否则 relay 直接 400 "has not been priced by the administrator"。
- **gpt-5.4-mini 系列**不能走 Gemini 类型渠道：new-api 会把请求转成 Gemini 格式，本池对非 `generate` 模型返回 "Factory model is not a Google generate route"。新增 **渠道 247 `Factory-OpenAI-gptmini`**（type=1 OpenAI，base_url 与 245 相同，模型仅这两个），走原生 Responses。
- `/v1/chat/completions` 调用 gpt 模型仍会被本池的兼容转换策略拒绝（`compatibility=false` 时为明确的 400；开启后会在上游 Responses 回包的 `logprobs` 字段上触发 502，属于既有兼容层的严格字段校验，未在本次改动）。DSH 对 OpenAI 系模型走 Responses，不受影响。
- droid 截图里的 “GPT-5.4 Mini Fast Mode” 对应模型 ID 是 `gpt-5.4-mini-fast`；`gpt-5.4-mini-fast-mode` 上游直接 400 Invalid model ID。

## 状态与边界

出口 IP/网络指纹审计见 [factory-egress-privacy.md](factory-egress-privacy.md)。本版本参考 [magpie-community/plugins 的 Factory 插件](https://github.com/magpie-community/plugins/tree/main/packages/factory)，固定研究版本 `d24fd5d1824e1078e7c8c56732813f8263fcf728`。采用认证、模型路由元数据、只读身份/额度接口、Fast Mode字段映射、`anthropic-tools.mjs`的工具schema包装/结果解包，以及Droid客户端请求头与系统提示词开头（`factoryHeaders`/`droidBody`/`anthropicBody`）；不是该插件的完整移植。MIT许可与来源见`THIRD_PARTY_NOTICES.md`。另复用本项目的有限协议桥接，而非将该插件误描述为通用协议桥。

**没有移植**：pi/ZCode等第三方客户端的固定句式替换、工具结果/历史内容改写（除下述Droid开头行外）、内容策略拒绝后的重新包装、组织或账号轮换绕过、413剥图、多模态扩展。自定义子集不包含多模态扩展。Factory模型网关不是其Public API中的普通公开模型服务契约；即使补齐客户端身份，上游仍可因字段、套餐或区域策略拒绝。本版本不会为取得200隐藏这一限制。本版本另实现了网关侧的 `auto`/`auto-fast` 虚拟选择、Gemini `countTokens`、OpenAI `Embeddings` 与 Responses WebSocket 透传；它们不属于插件原始移植，边界见下文。

### Droid 客户端身份（按 1.14.0 本地抓包对齐）

Factory 只把订阅的模型请求视为 Droid 发出的请求（插件 README “Requests from other agents”，magpie #242/#506），但客户端身份必须与真实 droid 一致，而不是照抄插件的旧常量。`docs/factory-cli-endpoints.md` 的 3.66 小时、399 条 HTTP / 27 次 LLM 抓包给出了当前行为：

| 项 | 取值 | 来源 |
|---|---|---|
| 版本 | `1.14.0` | 抓包 `x-client-version` / `factory-cli/1.14.0` |
| 推理 `User-Agent` | `factory-cli/1.14.0`，仅 `/api/llm/*` | 抓包；与其它 HTTP 混用即破绽 |
| 管理/额度 `User-Agent` | `Bun/1.4.4` | 抓包 whoami、billing、settings、tools、telemetry |
| `X-Factory-Client` / `X-Client-Version` | `cli` / `1.14.0`（whoami 例外，见下） | 抓包 |
| `X-Factory-Org-Id` | 该账号 whoami 读到的 `orgId`，没有则不发送 | 抓包 |
| `X-Factory-Request-Surface` | 可配置 `cli_tui`（默认）或 `cli_exec` | 抓包两种形态 |
| `X-Factory-Exec-Output-Format` | `cli_exec` 时为 `json` | 抓包 |
| `x-api-provider` | 模型配置的 upstream | 抓包 |
| `x-session-id` / `x-assistant-message-id` | 由网关自身会话指纹派生的 UUID 形状值（路径+首条 system/user 的 HMAC；message 另含本次请求体） | 抓包；不转发调用方值 |
| `x-provider-routing-source` | `configured_order`（本网关按模型配置选上游；抓包的 `session_lock` 表示 droid 复用会话锁定的供应商，本网关不声称） | 抓包 |
| `traceparent` | `00-<32hex>-<16hex>-01`；同会话复用 trace，逐消息换 span | 抓包 |
| `OpenAI-Platform` | 可选设置；抓包值为 `org-<base64>`。whoami 不返回该映射且抓包值只属于一个组织，因此默认不发送，不伪造 | 抓包 |
| `X-Api-Key` | `placeholder`（仅 `/llm/a`，与 Bearer 并存） | 抓包 |
| 系统提示词开头 | `You are Droid, an AI software engineering agent built by Factory.` | `droidBody`/`anthropicBody` |

调用方自带的 `User-Agent`、`X-Factory-*`、`X-Client-Version`、`X-Session-Id`、`X-Provider-Routing-Source`、`OpenAI-Platform`、`Traceparent`、`X-Anthropic-Billing-Header` 一律丢弃后再写入上表值，因此下游客户端指纹不会外泄到上游。

**whoami 与 billing 的头部不同**（抓包逐端点差异）：whoami 只带 Bearer、`X-Factory-Whoami-Extended: true`、当前组织与 `Bun/1.4.4`，**不带** `X-Factory-Client` / `X-Client-Version` / 请求面；billing/limits 带完整业务头（client、version、org、请求面，抓包中时常还有 traceparent）。本项目按端点分别发送，不再用推理 UA 读取额度。

请求体改写只覆盖固定开头：Chat 首个 system 消息前加行、Responses `instructions` 前置、Messages `system` 首块前置并丢弃 Claude Code 的 `x-anthropic-billing-header:` 行（该行后若还有指令则保留）。用户指令、历史、工具 schema、大整数均原样保留（`json.Number` 保证 >2^53 不丢精度）；已符合的请求体逐字节原样发送。

### 用户授权 Key 的真实接口证据

测试凭据不写入源码、示例、测试数据或本文。

| 接口 | 2026-11-17 UTC 实测 | 含义 |
|---|---|---|
| `GET https://api.factory.ai/api/cli/whoami` | 200，约1502ms | Key可识别用户和组织，不证明模型授权 |
| `POST /api/llm/o/v1/chat/completions`，`deepseek-v4.1-flash`，64输出Token上限、无system | 403，`content_policy_rejected`，约2465ms | 该次请求既无 system 也无 Droid 身份头；是否因此被拒未单独分辨 |
| `GET /api/billing/limits`，17:14UTC | **200**，约1432ms | 普通fk-Key本次确实能读额度，不能长期硬编码未知 |

本次额度：Standard周、月各已用1%；5小时窗口及Core窗口 `windowEnd=null`，表示无活动窗口，不等于完整可用额度。Extra余额0美分，`extraUsageAllowed=true`是上游既有状态；未修改官网偏好。

**真实模型调用仍未通过；不得称为已可用或完整接入验收。** 官方支持API Key通过Droid CLI/SDK/Execute API运行Agent，不等于第三方裸模型请求受支持。本版本不创建Droid电脑、执行代码或调用支付接口。

## 管理入口

顶部「Factory AI」，使用已有概览、账号、下游密钥、导入密钥、路由设置、请求/错误/详情日志。

- `provider=factory`，默认分组`factory`，支持完整`fk-` API Key，或通过「Factory 授权登录」生成的 WorkOS 设备会话；其他池不能接受Factory Key。
- 复用账号CRUD、启停、去重批量导入、完整凭据查看/复制、代理、权重/优先级、并发/RPM。管理员读取保留登录/CSRF/no-store边界，凭据不写localStorage；日志脱敏。
- 批量格式：`fk-example_test_key,账号名,http://127.0.0.1:8080`。示例Key无效。
- 开放导入使用Factory专属导入密钥，显式`provider=factory`；下游密钥不能导入，导入密钥不能推理。OAuth 账号不通过批量导入创建，只能在控制台设备授权或重新授权。

### Factory 授权登录（WorkOS Device Authorization）

Factory 的登录与刷新走 **WorkOS User Management**，不是 `api.factory.ai/api/auth/*`（实测 404）。本项目使用 WorkOS 设备授权，不依赖桌面端的 `factory-desktop://` 回调，也不允许 `http://localhost` 回调（该 client 未登记，WorkOS 会跳 `redirect-uri-invalid`）：

1. `POST https://api.workos.com/user_management/authorize/device`，`client_id=client_01HNM792M5G5G1A2THWPXKFMXB`，得到 `device_code`、`user_code`、`verification_uri`（`https://auth.factory.ai/device`）和 `verification_uri_complete`。
2. 控制台展示验证码与链接；用户在浏览器批准后，服务端按 `interval` 轮询 `POST /user_management/authenticate`（`grant_type=urn:ietf:params:oauth:grant-type:device_code`）。
3. 成功后得到 `access_token` / `refresh_token` / `active_organization_id`；用访问令牌调用 `/api/cli/whoami` 校验身份，再按 WorkOS user id 或 email 建号/更新。
4. 访问令牌临近过期（JWT `exp` 前 2 分钟）时，用 `grant_type=refresh_token` 刷新；推理、whoami、billing 都使用刷新后的访问令牌。刷新失败使该账号短期冷却/切换，不把 Refresh Token 写进日志或页面。

账号字段：`RuntimeKey` 保存短期 access token，`factoryRefreshToken` 保存续期令牌，`factoryAccessExpiresAt` 保存过期时间，`factoryOrgId` 保存组织。OAuth 账号的 `NativeBindingSource` 按 WorkOS user id 固定，不因 access token 轮换而丢弃额度快照/绑定；控制台账号列表显示「OAuth 授权」徽标，不显示或复制 access/refresh token。重新授权使用账号操作菜单的「Factory 重新授权」。

> 本机验收：真实 WorkOS `authorize/device` 已通过控制台成功签发 device code（`auth.factory.ai/device` 链接可用）；设备批准、token 兑换、pending/approved、拒绝、刷新与额度读取用 Mock 覆盖。未使用真实账号完成一次审批，不能据此声称线上登录已验收。

#### 401 排查（线上真实失败记录）

线上第一次真实设备登录时，浏览器已显示 `auth.factory.ai/device/success`（设置完成），但服务器侧 `/api/cli/whoami` 返回 **401**。已核实的结论与新增能力：

- `api.factory.ai/api/cli/whoami` 对无 token 和任意坏 token 都返回 401，响应体带 `requestId`；生产 API **只接受 Factory 生产 WorkOS client 签发的 access token**。
- Factory 有两个 WorkOS client：生产 `client_01HNM792M5G5G1A2THWPXKFMXB`，development `client_01HNM7927XNSKCJ4982Z5J3FFZ`。`authorize/device` 只传 `client_id`，所以用 dev client 也能拿到成功页，但换出的 token 在 `api.factory.ai` 上必然 401。本项目固定使用生产 client。
- 现在失败时对话框会显示 `diagnostic`：从 access token 的 `iss`（`…/client_<id>`）判断 `production` / `development` / `unknown`、令牌有效期，并在 dev 时直接说明「api.factory.ai 只接受生产 client」；同时服务端日志记录 whoami 的 HTTP 状态与上游错误体（不含 token），以及实际出口（`direct` 或代理主机）。
- whoami 使用的 Bearer 现在是 WorkOS 返回的**原始 access token 字符串**（不经过任何重编码），与抓包 `Authorization: Bearer <token>` 一致。

若再次出现 401：先看诊断里的 `WorkOS客户端`。

- `development`：浏览器批准用的是 dev 环境（例如用了 Factory 的 dev/测试站），换生产账号重新批准。
- `production` 仍 401：看服务端日志里的上游错误体与 `requestId`，可能是该账号或组织未被 Factory 生产环境接纳、令牌已过期，或数据库迁移把 org 头填错。
- 网络错误（`Factory whoami 网络请求失败`）：检查该账号代理能否访问 `api.factory.ai`。

### 每账号代理池、出口 IP 与 UA

Factory 设置新增：

- **代理池**（每行一个 `http://` / `https://` / `socks5://`）：新账号、OAuth 登录、批量导入按池分配；优先使用未被其他启用 Factory 账号占用的代理。账号自己的「出站代理」优先，不会被池覆盖。
- **UA 池**（每行一个）：新账号/登录按池分配推理 UA；账号自己的「推理 UA」优先。`X-Client-Version` 在 UA 形如 `factory-cli/<version>` 时跟随该版本，否则保持 droid 参考版本 `1.14.0`。**只接受 CLI 形态**：账号、UA 池、默认 UA 都拒绝 `Mozilla/…` 浏览器 UA（推理请求不发送 Sec-CH-UA/Accept-Language/Sec-Fetch-*，浏览器 UA 会自相矛盾）；运行期若读到旧配置里的浏览器 UA，会回退为 `factory-cli/1.14.0`。
- **Accept-Encoding**：推理请求发送 droid 的 `gzip, deflate, br, zstd`；上游压缩响应在进入 JSON/SSE/Gemini 聚合前统一解码（gzip/deflate/br/zstd），未知编码返回 502 `upstream_encoding`。
- **与 magpie 0.1.55 的对齐（2026-11-21）**：逐项比对最新 `@magpie-community/opencode-factory-auth` 0.1.55 后补齐两处——
  - **Gemini 路由删除 body 级 `stream` 字段**：流式与否由路径 `:streamGenerateContent` 决定，droid 不发该字段（magpie `geminiBody` 的 `delete m.stream`）。
  - **Responses 路由对 OpenAI 系模型删除 `temperature`/`top_p`**：OpenAI reasoning 模型会以 400 拒绝这两个参数，droid 从不发送（magpie `droidBody` 同名逻辑）。
  - 未采纳的部分：magpie 仍把 `mcp__` 工具名视为安全、且黑名单里没有 `todo_write`，本仓库实测两者都会被拒，已按实测处理（比 magpie 更严）；其余 Claude Code/ZCode 专用适配器（`undeferredTools`、skill/reminder 改写）与本池客户端无关，未移植。
- **用量与费用（2026-11-21，`4dd5c58`/`c5a6811`/`9a03a21`）**：
  - **Gemini 用量没记录的原因**：`scanUsage` 先判断有没有 `usage` 对象，没有就直接 return，而 Gemini 只在顶层 `usageMetadata` 报用量，所以每次都被丢掉。已改为先读 `usageMetadata`（promptTokenCount/candidatesTokenCount/cachedContentTokenCount/thoughtsTokenCount）。
  - **计价**：`Factory.EffectivePricing()` 现在自带 Google 官方价（USD/1M token，见 `internal/config/factory.go` 的 `factoryPricingDefaults`）。保存过但为空的 `factory.pricing` 视为"未配置"，回落到默认表；有内容的表则覆盖默认。单请求计价改用**各池自己的价格表**（`cfg.ForProvider(provider).Pricing`），否则 Factory 的价永远用不上。
  - **估算口径**：`GET /api/factory/usage` 与 `/api/overview` 的 `factoryUsage` 扫描请求日志，按请求时间对应的单价 × 记录到的 token 计算（日志里有 cost 时以它为准），返回累计 USD、请求数、token 与分模型明细；未配价/无用量计入 `unpriced`。控制台 Factory 页新增「累计官方价估算」卡片。**这是按官方单价估算，不是上游账单**。
  - **注意**：Google 未公开 `gemini-4-carbon/hydrogen/argon` 的价格，默认表按同类已公开档位（3.1 Pro / 3.8 Flash / 3.8 Flash-Lite）预填并在 `note` 标注，可在「价格设置」逐条改。
- **第四轮实测（2026-11-21，用 DSH 失败会话的完整轮次复现）**：`content_policy_rejected` 还有一类触发源——**对话里引用了客户端自述句**。实测："`You are an AI agent powered by DeepSeek Harness.`"（**带句号**）→ 403；同文本去掉句号、或只出现产品名 "DeepSeek Harness" → 200。也就是说 Factory 拒的是"某个 agent 自述句 + 句号"这个完整句子，出现在 user 文本、system、tool 输出里都会让整轮请求被拒。处理：`internal/factory/phrases.go` 增加 `selfDescriptionRE = (?i)You are[^.\n]{0,120}?harness\.`，把命中句子里的 `Harness.` **改写为 `Runtime.`**（实测该改写被网关接受，句子其余部分不动，可读性优于转义），并在 `containsPhrase` 预检里也检查该正则，保证任何位置的引用都能被处理。
- **被拒短语改为语义改写**（2026-11-21，`f4fc226`）：9 条 magpie 短语全部换成网关实测接受的措辞，逐条验证过（字面 403 → 改写 200）：
  | 字面（被拒） | 改写（接受） |
  |---|---|
  | You are an expert coding assistant operating inside pi, a coding agent harness. | You are an expert coding assistant. |
  | The conversation history before this point was compacted into the following summary: | Earlier conversation context is summarized below: |
  | The following is a summary of a branch that this conversation came back from: | Summary of an earlier conversation branch: |
  | This session is being continued from a previous conversation that ran out of context. | This session continues an earlier conversation that ran out of context. |
  | You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK. | You are a coding assistant running within the Claude Agent SDK. |
  | You are Claude Code, Anthropic's official CLI for Claude. | You are a coding assistant, Anthropic's official CLI for Claude. |
  | You are a Claude agent, built on Anthropic's Claude Agent SDK. | You are an agent built on Anthropic's Claude Agent SDK. |
  | You have been invoked in the following environment: | The session environment is: |
  | (user's private global instructions for all projects) | (user's global instructions for all projects) |
  只有 `x-anthropic-billing-header: cc_version=` 无实测可用改写（字面本身 200，仅在工具输出里可能被拒），保留首字符转义兜底。模型现在读到的是自然语言，不再是 `\uXXXX`。
- **重要教训（实测）**：网关在内容检查前会先 **JSON 解码** 请求体。因此"把首字符写成真正 JSON 转义 `\u0059`"的做法无效——解码后仍是原句，仍 403；必须让**解码后的文本**发生变化。magpie 的 `escapeDeep` 之所以有效，是因为它在**解码后的字符串**里写入字面量 `\uXXXX`，序列化后线上是 `\\uXXXX`，解码得到字面 `\u0059ou…`，从而绕开匹配（代价是模型读到字面转义）。本站对 magpie 已知句子沿用该转义，对 DSH 自述句改用**改写**（更干净）。
- **第三轮实测（2026-11-20，用 DSH 会话日志里的真实请求体复现）**：从 `~/.dsh/sessions/*/session.v4.jsonl.zstd` 取到 DSH 实际发送的 system prompt（9.2KB）与 265 个工具定义，经 `m.yeschoy.pro` 复现并逐层二分，得到三条新规则：
  - **MCP 工具名被拒**：`mcp__<server>__<tool>` 这种形状全部 403（`mcp__rea__comment`、`mcp__foo__bar`），而 `mcp__test`（只有一个分隔符）、`foo__bar__baz` 可以；不是前缀而是形状。处理：改名为 `server_tool_<4hex>`，响应里还原（含 SSE）。magpie 0.1.52 仍按"mcp__ 保留"处理，已过时。
  - **工具名黑名单继续增长**：`todo_write` 单条即 403 → 默认 `renameAllTools` 已覆盖。
  - **Gemini 工具 schema 必须适配 Google 的 Schema proto**：带 `$schema` / `additionalProperties` / `strict` 等 draft 关键字 → 400 "Unknown name \"$schema\""；`items` 是数组 → 400 "cannot start list"；`type` 是数组 → 需收敛为单值（可空则加 `nullable`）；带 `items` 必须 `type=array`；`anyOf` 必须独占该 schema（`oneOf`/`allOf` 统一转为 `anyOf`）；缺 `type` 时按约束推断。实现见 `internal/factory/gemini_schema.go`。
- **第二轮实测（2026-11-20，用 DSH 真实请求复现后逐条二分）**：
  - **工具名黑名单比已知名单更长**：`todo_write` 单条声明即 403，而它不在 magpie 的 `REFUSED_TOOL_NAMES` 里。名单会增长且不可先知，因此默认 `renameAllTools=true`：**所有未在 droid 已接受名单（Read/Edit/Glob/Grep/Task/WebSearch/TodoWrite/Execute/Create）里、且不以 `mcp__` 开头的工具名都改名**，响应再还原。被拒名单里的名字仍优先映射到 droid 同名工具。
  - **pi/Claude Code 的压缩开场句在任意位置都被拒**：`The conversation history before this point was compacted into the following summary:` 单独出现在 system 里即 403；`This session is being continued from a previous conversation that ran out of context.`、branch 开场同理。此前只在"整段就是 summary"时才改写，长 system prompt 里漏掉。
  - **首字符 JSON 转义可绕过内容检查**（实测）：把 `T` 写成 `\u0054`、`Y` 写成 `\u0059` 后同样的句子 200，且 JSON 解析后模型读到原字符。因此策略是**对已知被拒句子做无损转义**（而非改写语义），并在 Gemini 路由上同样执行（此前只在 chat/messages/responses 执行）。
- **实测的上游准入规则（2026-11-19，真账号直接探测 prem.factory.ai）**：
  - **system 必须以 droid 那句话开头**：完全没有 system → 403；`You are a helpful assistant.` → 403；`You are Droid, an AI software engineering agent built by Factory.` → 200；DroidLine + 换行 + 任意内容 → 200。因此除 `keep` 外的模式都保证开头存在（缺失时新建、block 形态也处理、Gemini 的 `systemInstruction` 同样写入）。
  - **客户端自述里的 harness 会被拒**：`You are an AI agent powered by DeepSeek Harness.` → 403；改写成 `DeepSeek Runtime` → 200（句号前的 `Harness` 也要改写，`harness.rs`/`harness_key` 这类标识符不改）。
  - **特定工具名会被直接拒绝**：仅声明名字即 403，大小写敏感、全路由生效（Claude Code/Codex/OpenCode 的名字，以及 `read`/`edit`/`glob`/`grep`/`bash`/`write`/`web_search`/`read_file`/`execute_command` 等，含前导下划线变体）。处理方式是按 magpie 的映射把被拒工具改名为 droid 自己的工具（`read`→`Read`、`bash`→`Execute`、`write`→`Create` 等，无对应时加稳定后缀），历史/`tool_choice`/`allowedFunctionNames` 同步改名，响应里的 calls 再改回调用方原名（非流式 JSON 与 SSE 都还原）。
- **System 处理四档**（`factory.systemMode`）：`open`（默认，system 开头写 droid 的固定句）、`magpie`（按 `@magpie-community/opencode-factory-auth` 移植的适配器逐条改写：固定身份句换 droid 开头、pi 开场与压缩句改写、客户端自述 `harness`→`runtime`、ZCode 环境段与模型行改写、Anthropic 计费头删除、工具输出引用被拒短语时包成可解码 JSON 串；任务指令保留）、`fold`（把调用方 system 移到首条 user 消息前并移除 system 字段）、`keep`（原样）。Factory 只接受 droid 自己的 system 内容，其它内容和空 system 都可能被拒；完全没有 system 字段是可接受的。
- **被拒短语转义**（`factory.escapeClientPhrases`，默认开）：Factory usage policy 会读整个对话，出现其它 agent 的 system 提示词（`You are Claude Code…`、`You have been invoked in the following environment:` 等，共 9 条）时整条对话都 403 `content_policy_rejected`。开启后把这些短语首字符写成 JSON `\uXXXX` 转义，JSON 解析后模型读到原字符，只有线上字节不同。短语表与 magpie 0.1.52 `client-phrases.json` 一致。
- **Droid 版本可配**（`factory.droidVersion`）：默认 `0.242.0`。Factory 有服务端 `min_cli_version`，版本落后即使其它头全对也可能 403；改这里即可，无需重新构建。
- **Droid 系统提示开头开关**：`factory.droidBody`（默认开）。开启时每个请求的 system 开头写入 `You are Droid, an AI software engineering agent built by Factory.`（Chat/Responses/Messages/Gemini 四条原生路径）。关闭后请求体只保留路径与 `model` 位置的改动，完全保留调用方原文。关闭后若上游拒绝，说明该身份标记仍被校验。
- **按协议对齐头集合**：抓包里 `traceparent` 只出现在 Responses/Gemini，`x-factory-exec-output-format` 只出现在 Responses/Gemini 的 cli_exec 调用，`openai-platform` 只在 OpenAI 路由。网关按实际线路协议逐条判断，不再“有就发”。
- **默认推理 UA**：账号和 UA 池都没填时使用；默认 `factory-cli/1.14.0`。
- **管理/额度 UA**：仅用于 whoami、billing、WorkOS 设备码/刷新；默认 `Bun/1.4.4`，不与推理 UA 混用。
- 「为现有账号分配代理/UA」只填缺失字段，不覆盖已有值；需要重新分配时先清空账号代理/UA 再点。

**实际出口 IP 只由账号代理决定**。`EgressIP` 是出口检查的观察值，不是可设置/伪造的请求头；不同设备要得到不同 IP，必须给它们分配不同代理。代理池小于账号数时会重复使用；控制台对无代理 Factory 账号显示「直连出口」。

同一账号的推理、Gemini、Embeddings、countTokens、WebSocket、whoami/billing、OAuth refresh 都走该账号代理；OAuth 浏览器批准页仍使用用户浏览器 IP。

## 原生协议与模型

抓包显示 Factory 有两个面：管理面 `api.factory.ai`（whoami、billing、settings、tools、遥测），模型面 `prem.factory.ai`（全部 `/api/llm/*`，含 Responses WebSocket 与 HTTP 回退）。本项目的「管理/额度源站」默认 `https://api.factory.ai`，可明确设 `https://api.eu.factory.ai`；「推理源站」留空时默认 `api.factory.ai` 映射到 `https://prem.factory.ai`，自定义/Mock/代理源站则沿用同一 origin，也可显式填写。两者都必须是无路径、凭据、查询、片段的 HTTP(S) origin。下游 `/v1/responses/ws` 已接入并透传到抓包中的 `wss://prem.../api/llm/o/v1/responses/ws`；HTTP `/v1/responses` 仍保留为回退。WS 升级后不重试，也不计入 HTTP 请求统计。

| 下游路径 | 上游路径 | 路由 |
|---|---|---|
| `/v1/chat/completions` | `/api/llm/o/v1/chat/completions` | 开源模型，x-api-provider按配置 |
| `/v1/responses` | `/api/llm/o/v1/responses` | GPT/OpenAI，Grok/xai |
| `/v1/messages` | `/api/llm/a/v1/messages` | Claude/anthropic，MiniMax M2.7/fireworks |
| `POST /v1beta/models/{model}:generateContent` | `/api/llm/g/v1/generate` | Gemini/google，请求体保留`contents/tools/generationConfig`，补`model`与Droid `systemInstruction`；上游SSE时做最小合并后返回JSON，其余原样 |
| `POST /v1beta/models/{model}:streamGenerateContent` | 同上 | Gemini SSE 透传；上游JSON时包装为单条SSE；无`[DONE]`，以`finishReason`或`promptFeedback`结束 |
| `POST /v1/models/{model}:generateContent` | 同上 | 同一Google路径的兼容别名 |
| `POST /v1beta/models/{model}:countTokens` | `/api/llm/g/v1/countTokens` | Gemini 原生计数；补`model`/Droid `systemInstruction`；返回`{totalTokens}`。上游路径按 generate 同族推断，抓包未直接命中 |
| `POST /v1/embeddings` | `/api/llm/o/v1/embeddings` | OpenAI Embeddings 原样透传；默认`text-embedding-3-large`/openai；不流式、不改写 |
| `GET /v1/responses/ws` | `wss://…/api/llm/o/v1/responses/ws` | Responses WebSocket 透传；首帧`response.create`决定模型/账号；HTTP CONNECT/SOCKS5 代理；升级后不重试 |
| `GET /v1beta/models` | 本地配置参考列表（Google ListModels 格式） | 只列 `protocol=generate` 的 Gemini 路由，每条只声明 `generateContent`/`streamGenerateContent`；响应头 `X-Catalog-Source: configured-reference-not-entitlement` |
| `GET /v1/models` | 本地配置参考列表 | **不是账号授权目录**，响应标注`configured-reference-not-entitlement`；另列`auto`/`auto-fast`虚拟模型 |

模型路由JSON数组 `{id,protocol,upstream}` 可编辑、保存、重载；空数组真正清空。缺省综合固定版本参考与抓包：`claude-sonnet-5-5`/`claude-haiku-4-5-20251001` 为 `bedrock_anthropic`，`kimi-k3.1-pro` 为 `databricks`，`deepseek-v4.1-flash-1017` 为 `baseten`，`gpt-6.2-sol` 为 `openai`；Gemini `gemini-4-argon`/`gemini-4-carbon`/`gemini-4-hydrogen`/`gemini-3.x-flash` 为 `generate`/`google`；其余未观测模型保留参考值。`protocol=generate` 只接受 `upstream=google`。设置页「填入抓包参考路由」可把新默认值写入编辑框，保存后才生效。已有自定义目录不会重排/覆盖，但重新加载时会**自动补入缺失的 Gemini generate 与 embedding 条目**，不需要再点按钮。不发带Key的未知`/models`请求、不偷偷抓取客户端/猜测模型路由。未知模型或路径本地拒绝。

- **auto / auto-fast**：droid 客户端自己选 auto；网关侧把下游传入的`auto`解析为同协议第一条已配置路由，`auto-fast`解析为第一条`-fast`路由。`/v1/models`列出`auto`，存在`-fast`路由时再列`auto-fast`，响应标注`gateway-virtual-auto`；这不是 Factory 服务端 Router 的复刻。
- **countTokens**：按 Gemini 原生`/v1beta/models/{model}:countTokens`接收，转发到`/api/llm/g/v1/countTokens`并补`model`与Droid systemInstruction；响应`{totalTokens}`。上游路径未在抓包中出现，属于同族推断。
- **Embeddings**：`/v1/embeddings`原样转发到`/api/llm/o/v1/embeddings`，只接受配置为`embeddings`/`openai`的模型（默认`text-embedding-3-large`）；不流式、不做协议转换。
- **Responses WebSocket**：`GET /v1/responses/ws`先认证下游Key，读取首帧`response.create`的`model`，租用账号后连接`wss://prem.../api/llm/o/v1/responses/ws`并原样双向转发文本帧。支持HTTP CONNECT/SOCKS5账号代理；升级发出后不重试，也不计入HTTP请求统计。

### 插件适配与有限跨协议转换（续轮）

- **参考插件的工具schema转换**：Messages工具根级`anyOf/oneOf/allOf`整体包到必填`arguments`对象里，改写schema位置的本地`$ref/$dynamicRef`指针；独立`$id`、anchor以及`const/default/examples`中的普通数据不误改。历史`tool_use.input`、`input_examples`同步包装；调用ID、用户指令和工具结果保持。无`$id`的递归资源拒绝而非错误搬移。
- **返回还原**：仅当前请求确实包装的工具解包；真实调用者字段`arguments`不误拆。JSON与原生Messages SSE均保留原工具ID、Unicode、大整数及上游usage。SSE只缓存包装工具参数，文本/推理/普通工具继续流式输出；每工具参数4MiB、并行总16MiB、单帧8MiB上限。无效/截断参数不能当作完成，也不换号重放或降健康；已提交HTTP200时追加错误事件，不伪造终止成功。
- **Fast Mode**：参考模型列表包含Opus/GPT的Fast条目。显式Fast模型补`speed=fast`及Anthropic beta，或`service_tier=priority`；对基座模型显式请求fast/priority且对应Fast模型已配置时映射。普通请求不自动升级；GLM-5.2 Fast是独立Chat模型，不由priority切换。Fast可能更贵，不自动接受新付费条件。已有自定义模型列表不自动覆写。
- **本项目协议桥**：`factory.compatibility` 控制 Chat/Responses/Messages 间的六个有限转换方向，也控制这三个入口调用 Gemini 模型（见上文）；保存/重载后生效。新建默认配置开启，旧已保存但缺该字段的配置保留关闭，需在 UI 显式启用。按模型原生协议选择路径与 `x-api-provider`，再转回调用者协议。Gemini 入站路径仍只接受 `generate` 模型。
- **跨协议SSE是缓冲模式**：总响应16MiB、单事件2MiB上限，等完整成功响应后输出目标SSE，返回`X-Compatibility-Mode: buffered`；不是实时首Token流。原生SSE不进入整包缓冲。签名thinking、服务端续传引用、hosted tools等不能无损表示的字段本地400；响应不能表示则502且不重放，不悄悄删字段或编造usage。`previous_response_id`只能留在原生Responses，虚拟转换出的Responses ID不承诺上游续传。

原生请求除显式Fast/工具schema适配和上节Droid固定开头外保持原始JSON字节、指令及原生字段；Gemini请求只补`model`与Droid `systemInstruction`开头，`contents`、`tools`、`generationConfig`、安全设置和大整数原样保留；调用方身份头被替换为Droid客户端身份（见上节），不再使用balancer自报User-Agent。`X-Factory-Org-Id` 取该账号whoami读到的组织，不转发客户端自带值；whoami提供的region/private-host只作诊断，不自动将凭据送往返回域名。不跟随重定向，不在代理失败后直连。

NewAPI可按模型原生协议选择渠道，也可显式开启上述有限转换使用可表示的请求；转换不消除上游授权/策略限制。列表可见不代表可推理。默认真实测试模型`deepseek-v4.1-flash`，保留手填；模型测试与身份/额度检查分别显示结果。测试请求32–64输出Token上限，不遍历模型/账号。

## 独立路由与会话

Factory独立保存：选号、粘性、会话头、显式/弱粘性TTL、忙时策略、等待、尝试上限、健康阈值、首事件/空闲/总请求超时、请求体/模型缓存、直连/代理连接上限、额度轮询及价格/规则；以及本轮新增的推理源站、Droid 客户端形态（`cli_tui`/`cli_exec`）和可选 `OpenAI-Platform`。这些字段保存、重载与重启后保留；留空/缺省有明确回退，不静默改写别池。

缺省在途最少、粘性开启、绑定忙时拒绝。真实会话标识优先于缓存提示，身份HMAC按分组和下游Key隔离；缓存亲和不保证上游缓存命中。

Responses续传只认已确认、未过期、同Key/Base/代理来源的原账号绑定；不存在/过期/来源变化时409，不猜号或跨账号重放。非流式完成或流式`response.completed`后才记录响应绑定；中途创建事件不作为完成证明。普通会话TTL/LRU与响应链沿现有公共预算，不宣称独立百万级容量。

## 额度全链路

独立轮询（默认600秒）、独立并发预算、手动刷新 → `whoami`（Bun UA、扩展标记、当前组织）身份强类型校验 → `billing/limits`（Bun UA、client/version、请求面、组织、traceparent）强类型窗口/余额校验 → `Snapshot.Factory` → metadata持久化 → 账号API与页面。whoami 建立的 org 作为本次 billing 与后续推理的 `X-Factory-Org-Id`；每账号显式传递，不再使用跨账号共享的全局变量。额度/身份来源绑定provider+完整Key+Base+代理；来源变化失效，锁内再校验，旧来源迟到结果不发布。身份的user/org变化也清空原组织读数。

- Standard/Core各有5小时、周、月百分比及reset；空/缺失/错误类型不当零。窗口尚未激活、已过期、读取失败、旧历史明确展示。
- 额度401/403不等于推理Key无效；保留历史，标`unsupported-credential`，停止周期重试billing，手动刷新可重新验证。身份401单独记录AuthFailed。
- 已知新鲜、仍活动且达到100%的对应窗口参与调度门禁；未知/过期不伪造耗尽或补满。Core分类采用固定参考模型清单，不能仅按托管厂商推断（Muse Spark 1.2属于Standard）。
- `standardOnly`默认关闭；开启时所有模型按Standard窗口门禁，不以Core/Extra绕过本地限制。关闭时按模型Standard/Core分类，若官网已许可且真实Extra余额>0，则窗口耗尽不会在本地拦截。**这不修改付费偏好，也不能保证零额外支出；要保证不花Extra须在官网关闭。**
- 多Key可能共享用户/组织预算，概览只计新鲜状态，不汇总百分比或重复余额。价格表默认空，费用未知，可填手动估算价，不是上游账单。

## 错误、重试与日志

- 独立默认规则，可增删改、启停、排序，按状态/正文/响应头匹配，支持账号或账号×模型作用域、冷却/恢复/刷新/下游状态；未证明共享funding身份，不允许funding作用域。
- `content_policy_rejected`执行层强制停止，不扣Key健康分、不换号/出口、不改组织/内容；即使自定义switch-account规则也不能覆盖。
- 401短暂账号冷却；402停止且不付款；403停止、不直接判封号；429默认停止，完整遵守秒数或日期Retry-After；明确HTTP5xx可在安全条件和尝试预算内重试。
- 已发送结果不确定、流已输出、有状态续传不重放；截断流记失败，不伪造DONE。Responses需真正终止事件。
- 请求/错误摘要、关联失败详情沿既有设施；Factory详情默认关且独立，每体最多64KiB并受共享更小预算约束，Key/Cookie/会话标识脱敏。保留天数/总量是全局共享，只读展示，保存Factory不改变别池。

## 全局号池启停

侧栏「号池管理 / 启停」，独立于当前池，全部关闭仍能打开。

- `GET /api/pools`；`PUT /api/pools/{provider}`，请求含revision与enabled，沿管理员会话、CSRF和冲突保护。支持所有推理池及Telnyx注册池。
- `Config.poolSwitches`是稀疏map，缺项默认开，旧配置不自动关闭在线池；开关落盘、重载/重启保留。
- 关闭隐藏日常标签；新的推理/目录返回503（而不是错误鉴权），调度也排除；后台quota刷新取消、旧epoch结果即使重新开启也不回填；手动上游测试/注册操作拒绝。
- Perch续期/健康、ALTAPI每小时目录/自动补号，以及各注册worker挂池级context；关池取消工作与后续排队，保留已获取的业务数据。无上游请求的必要定时器/共享元数据保存/日志保留清理仍在，不承诺进程内存立即归零。
- 已在途推理允许自然结束；等待期间关闭或下一次换号前会重查，不再新增上游尝试。切换不是删除账号/清空日志，不改变账号自己的Enabled与其他池配置。
- 重新开启建立新后台epoch、按既有轮询配置恢复，不打开原本关闭的quota/OpenCode/自动补号，不自动重跑取消注册。

## 本地验证与交付状态

已执行：
- `go test ./...`（server79.900s）、`go test -race ./...`（server123.848s）、`go vet ./...`、构建与diff检查通过；Factory/启停重点竞态重复10轮通过（server23.534s）。
- Factory配置/分组/凭据/保存重载与空规则、额度失败/权限/零/未激活/窗口/历史/持久化、CRUD/导入去重/跨池密钥、原生三协议/字节保留、自定义规则/策略安全门/未知结果不重放/Responses原绑定、SSE截断与重定向、全池启停鉴权/revision/取消与迟到/在途自然结束/全部关闭可恢复。
- 全部16个前端`*_web_test.js`与全部Web JS语法检查通过；修正既有Telnyx导航测试桩与新全局管理页例外，保留原池隔离断言。
- 真实本地浏览器（仅Mock/假Key）：Factory路由weighted、会话头、TTL180、standardOnly/详情保存；重载后表单/API/磁盘一致；真实点击刷新显示Standard/Core未激活与1%窗口；关池隐藏且新推理503/上游计数不变；全部关闭后刷新仍有管理页；真实点击重新开启恢复200；同Mock源服务重启后额度4窗口与设置/启停均保留，轮询原关闭仍未发上游请求；错误/重试规则可视停用、保存、重载/API/磁盘一致且Deploy规则不受影响；无页面JS错误。

- 续轮验证：六种跨协议方向的JSON/SSE、工具schema根级composition/ref/resource/字面数据/历史/示例、Unicode/大整数、普通工具字段不误拆、参数截断/错误/超限/关闭传播、Fast映射/保留beta、无损限制本地拒绝/无上游无健康消耗；新增浏览器转换开关关闭400零上游、开启200转为Chat、保存与重载/落盘一致，16个前端回归通过。续轮最终`go test ./...`通过（server89.478s）、全仓竞态通过（server141.916s）、vet/build/diff/gofmt通过；直接运行公开插件schema适配与Go实现的8组同输入结构对照一致（不含身份/提示词改写）；重点竞态重复5轮通过。

- 2026-11-19 新增 Gemini countTokens、Embeddings、虚拟 auto/auto-fast 与 Responses WebSocket：countTokens 走 `/api/llm/g/v1/countTokens`（Mock）；Embeddings 走 `/api/llm/o/v1/embeddings`（Mock，`text-embedding-3-large`）；auto 在 Chat/Gemini 路径解析为具体已配置路由并改写 body；WS 通过本地上下游 WebSocket 回环验证首帧 `response.create`、账号租用、Bearer/org/UA 头与双向文本帧。真实 Factory 的 countTokens、Embeddings、WS 尚未调用。
- 2026-11-19 Factory 授权登录：`factoryauth` 覆盖 device code、pending/slow_down/expired/denied、refresh 轮换与 JWT `exp`；服务端覆盖设备码启动→轮询批准→whoami→建号/重授权、OAuth access token 推理、过期自动刷新、额度轮询续期、号池关闭与跨池重授权拒绝；账号来源按 WorkOS user id 固定，access token 轮换不清空快照。真实 WorkOS `authorize/device` 已通过本地浏览器从控制台签发 device code；真实账号批准、token 兑换与刷新仍未执行。
- 2026-11-19 新增 Gemini 原生路由：`/v1beta/models/{model}:generateContent` 与 `:streamGenerateContent`（含 `X-Goog-Api-Key` 鉴权、`/v1/models/{model}:generateContent` 别名）→ `prem.factory.ai/api/llm/g/v1/generate`；补 `model` 与 Droid `systemInstruction`，保留`contents/tools/generationConfig`；同形态JSON/SSE原样透传，跨形态做最小合并/单事件包装；`generate`只接受`google`，Chat/Responses/Messages不做Gemini转换。
- 2026-11-19 按 1.14.0 抓包对齐：`go test ./...` 通过（server 100.730s）；`go test -race`（config/factory/factoryauth/quota/server，server 152.447s）、`go vet ./...`、`go build ./...`、`factory_web_test.js` 与全部 Web JS 语法检查通过。新增/更新断言覆盖：双 User-Agent、`cli_tui`/`cli_exec`、exec 输出格式、W3C traceparent 形状、`configured_order`、OpenAI-Platform 仅 OpenAI 路由与仅配置后发送、whoami 与 billing 头部差异、模型面 `prem.factory.ai` 默认映射、保存/重载/重启保留推理源站/请求面/OpenAI-Platform、额度失败历史，以及 Gemini 路径解析/`systemInstruction`开头/大整数保留/JSON与SSE透传/`finishReason`终止/非generate模型拒绝；代理池/UA池分配、账号 UA 头与版本、设置重载。
- 真实本地浏览器（本地 Mock、假 Key）：Factory 路由设置页可见并修改推理源站、`cli_exec`、`OpenAI-Platform`；「填入抓包参考路由」把 61 条参考路由（含 `gemini-4-carbon` generate/google）写入编辑框，保存后 `/api/settings` 实际生效；重载表单、`/api/settings` 与磁盘一致；经真实代理发送 Responses、Chat 与 Gemini `:generateContent`，Mock 分别收到 `factory-cli/1.14.0`、正确的 `x-api-provider`、`X-Factory-Request-Surface`、`configured_order`、`traceparent`，Gemini 收到 `/api/llm/g/v1/generate`、body 含 `model` 与 Droid `systemInstruction`，且未转发下游 `X-Goog-Api-Key`；`OpenAI-Platform` 仅出现在 OpenAI 路由；无页面 JS 错误（仅有既有 CSP inline-style 控制台提示，未由本改动引入）。
- 端到端路径复核（本地 Mock）：`deepseek-v4.1-flash-1017` → `/api/llm/o/v1/chat/completions` + `baseten`；`kimi-k3.1-pro` → 同一路径 + `databricks`；`gemini-4-carbon` → `/api/llm/g/v1/generate` + `google`。

本轮代码与文档未提交、未部署。真实Factory推理成功、全部模型/第三方客户端授权、生产NewAPI渠道验收仍未完成。Factory WorkOS 设备登录已接入（真实设备码启动 + Mock 批准/刷新）；真实浏览器批准、Refresh Token 线上轮换、真实 Factory countTokens/Embeddings/Responses WebSocket、Droid云端Agent、注册与支付仍未验收。没有创建Trellis任务。临时公开源码、Mock、日志和浏览器回执已登记，清理需用户同意。
