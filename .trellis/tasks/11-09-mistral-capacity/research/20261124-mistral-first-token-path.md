# Mistral 首字路径：分段观测与请求准备优化（2026-11-24）

基线：本仓 HEAD `64c7b0c`，工作树另有上一轮未提交的 `mistralTiming` 观测骨架。本轮=补齐观测（测试/UI/文档）+ 已测热路径优化。**未部署、未发真实生产推理**。

## 1. 生产证据边界

只读列出 OVH 容器 `/data/logs`：`requests-*.jsonl` 仅剩当前分段（45 条 Mistral，约 6.5 分钟窗口），此前的 24h 分段已被裁剪。因此本轮没有重做 11-23 的 14,582 条基线，直接引用
`20261123-mistral-end-to-end-baseline.md`：流式成功首事件 P50/P95 1.69/4.21 s，`durationMs − 最后 attempt.ms` P50/P95 8/48 ms；≥200k 输入零缓存首事件 P50 10.36 s，缓存读取≥90% 为 2.55 s。结论仍是上游 prefill/缓存主导首字，网关为毫秒级。

## 2. 本地分段（同轮，HEAD 工作树 vs 本树）

Apple M4 / Go 1.26.8，`-cpu 2 -count 5` 中位；合成 128 KiB / 512 KiB / 2 MiB Chat 历史（system + 64 轮 user/assistant，带 `reasoning_content`），`BenchmarkMistralPath`：

| 阶段 | 128 KiB | 512 KiB | 2 MiB |
| --- | --- | --- | --- |
| parse | 0.71 → 0.71 ms | 2.71 → 2.71 | 10.69 → 10.69 |
| prepare | 2.14 → 1.38 | 7.22 → 4.60 | 27.29 → 17.27 |
| restore | 7.50 → 1.91 | 25.77 → 5.40 | 97.70 → 19.31 |
| prepare+restore | 9.69 → 3.32 ms（4.85 → 3.09 MB/op） | 33.37 → 10.07（16.61 → 10.09 MB） | 126.2 → 37.1（63.45 → 38.27 MB） |

`BenchmarkMistralGatewayStream`（真实 Handler + 本地 Mock 上游，1 账号，首帧即返回）：
128 KiB first-byte 9.46 → 4.23 ms，B/op 3.85 → 2.96 MB；512 KiB 32.11 → 13.30 ms，11.94 → 8.99 MB。

## 3. 改动

- 一次性缓冲按原大小+余量 Grow（`mistralWireObject.render`、`mistralChatMessages`、`restore`）：此前溢出一个字节即翻倍并整份复制历史。
- 原生思考块/content 手写拼接，与旧 `encoding/json` 输出逐字节一致（`TestMistralAssistantReasoningWireBytesUnchanged`、`TestMistralLayoutRenderWireBytesUnchanged` 固定）。
- `restore` 不再把原历史解码成 `[]json.RawMessage`：按 `skipValue` 偏移并行配对，命中时只把 content 值就地拼进一次性全文缓冲（去掉逐消息 render + 根 render）；根 `messages` 重复键不猜，保持 prepare 结果。
- 布局纯文本解析改 wire view（`mistralLayoutPlain`/`mistralHasNativeThinking`/`mistralTextChunkView`/`mistralThinkingPartsView`），用 `TestMistralLayoutPlainMatchesResponseParser` 对齐旧 map 解析（重复键取最后、未知键拒绝、null/类型规则不变）。
- `requestLog.mistralTiming`/`attemptLog.mistralTiming` + 管理台「首字分段」（`mistral_timing.go`、`mistral_timing_test.go`、`mistral_web_test.js`）。

## 4. 未做

单趟 JSON 校验索引复用（parse 仍是两趟）、prepare+restore 合并成一次遍历、≥200k 零缓存桶的绑定/身份/前缀诊断（需部署后真实分段数据）、任何缓存键或亲和策略调整。3 亿 TPM 容量口径与验收状态不变。
