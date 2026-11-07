# Mistral 号池

Mistral 使用现有 OpenDesign 调度器，不引入第二套选号或协议转换。默认上游 `https://api.mistral.ai/v1`。

## 管理与接入

1. 管理台选择 **Mistral 号池 → 账号**，手动添加或批量导入 API Key。每行 `API Key[,名称[,代理URL]]`。不需要控制密钥；密钥仅写入，界面不回显原文。
2. 在本池创建下游密钥，默认分组 `mistral`。下游使用网关地址和这把密钥，不使用上游 Key。分组与导入密钥均隔离，不能借其他池账号。
3. **路由设置**可修改本池 API Base（必须含 `/v1`）、选号、粘性、并发等待与错误/重试规则。未单独保存路由前，继承 OpenDesign 现有路由配置；保存后独立生效，不改变 OpenDesign。

`mstrl_` 开头的 Key 自动识别为 Mistral；无前缀或其他形态的官方 Key 请显式填写 `provider: "mistral"`。本池导入界面会自动携带该字段。重复导入同一 Key 不重复建号。

开放导入使用 **Mistral 池专属导入密钥** 调用现有 `POST /open/v1/accounts/import`：

```json
{"provider":"mistral","runtimeKey":"MISTRAL_API_KEY_PLACEHOLDER","name":"Mistral-1"}
```

## 会话粘性与调度

- 与 OpenDesign 相同的策略、优先级、权重、最大并发、RPM、健康过滤及故障切换；`smart` 等额度策略在余额未知时使用现有未知额度排序，不伪造额度。
- 识别 Codex 的 `Session_id` / `X-Codex-Session-Id` / `X-Codex-Thread-Id`，Claude 的 `X-Claude-Code-Session-Id` 与 `metadata.user_id` 中的会话，pi 可传的 `X-Session-Id` / `X-Session-Affinity` / `X-Pi-Session-Id` / `X-Pi-Session`，以及现有父会话、OpenCode 等会话头、正文会话字段和 `prompt_cache_key`。任意额外稳定头可在「额外会话头」配置。
- 优先级沿用 OpenDesign：`previous_response_id`（本池不支持，Responses 请求会本地拒绝）、自定义头、父会话、正文身份、Claude 元数据、内置会话头、可选首条消息 HMAC 弱粘性。不会把每次变化的请求 ID 当成会话。客户端必须实际发送稳定标识；仅凭客户端名称不能保证粘性。
- 会话 HMAC 按分组和下游密钥隔离。TTL 与忙时 `overflow` / `wait-overflow` / `reject` 策略沿用现有配置，临时借号不改原绑定。
- 上游推理正文、工具、模型 ID、缓存标记和实际客户端 UA 不重写；会话粘性仅固定 Key，不保证上游缓存命中。
- 401 隔离失效 Key；402 冷却；429 尊重完整 `Retry-After`；临时 5xx 短冷却。确定性请求/权限错误不遍历账号。固定代理失败不回退直连；已发送且结果未知或流已输出的请求不重放；凭证请求不跟随重定向。

## 原生接口与额度

支持 `GET /v1/models` 以及原生 `POST /v1/chat/completions`、`/v1/responses`、`/v1/responses/input_tokens`、`/v1/fim/completions`、`/v1/embeddings`。不增加 Anthropic 协议转换，也不因识别 Claude 会话头而宣称 `/v1/messages` 可用。

Mistral Responses 为**无状态预览接口**：每次在 `input` 携带完整历史，`previous_response_id` 必须省略或 null，`store` 必须省略、false 或 null。不建立服务器端 Responses 续传绑定。

普通 API Key 没有本实现可用的余额读取契约；仅同步 `/v1/models`，余额明确显示未知。模型目录失败保留上次成功值与时间；更换 Key / Base / 代理后失效旧快照。同工作区多个 Key 可能共享限额，不能累加成独立账号额度。Mistral `base_model_id` 是模型来源，不当作别名，避免将微调模型改发成基础模型。

参考：[官方文档](https://docs.mistral.ai/)、[Responses](https://docs.mistral.ai/inference/conversations/responses)、[用量和限制](https://docs.mistral.ai/admin/billing-usage/usage-limits)。

## 验证

- `go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./cmd/odb`。
- `node internal/server/mistral_web_test.js`；真实本地浏览器使用假账号与本地 Mock，检查管理、导入、独立设置和后端状态。
- 本次用户提供的测试 Key：真实 `/v1/models` 返回 200 / 64 个模型；以 `ministral-3b-latest` 对 Chat 与 Responses 分别进行一次最多 8 输出 token 的小请求，均为 200，返回输出与 usage。真实凭证只在内存使用，不写入测试文件、日志或 Git；这不是 Codex / Claude / pi 客户端端到端验证或生产压力测试。
