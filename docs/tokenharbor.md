# Token Harbor 号池

官方文档：https://tokenharbor.ai/docs 。本实现按 2026-11-05 公开契约接入原生文本 API，不把它转成 OpenDesign，也不做跨协议转换。

## 接入

1. 管理台切换 **Token Harbor 号池**，手动添加或批量导入 `thk_…` API Key。默认分组 `tokenharbor`；自定义分组不能混用其他供应商。
2. 在本池「下游密钥」新建 `sk-odb-…`，分组与账号一致。此密钥只调度该分组，不能借其他池账号。
3. 默认上游 `https://tokenharbor.ai/v1`，可在本池路由设置修改（必须含 `/v1`，无 URL 凭据、查询串或 fragment）。上游 key 在管理 API 只显示掩码，不提供原文读取。
4. 通过中转站时开启会话/缓存头透传。账号可设固定 HTTP/SOCKS 代理、并发/RPM、优先级和权重；代理失败不会自动直连。

### Codex

`~/.codex/config.toml`（将地址替换成实际服务 HTTPS 地址）：

```toml
model = "从本池 /v1/models 选模型 ID"
model_provider = "tokenharbor_pool"

[model_providers.tokenharbor_pool]
name = "Token Harbor Pool"
base_url = "https://pool.example/v1"
wire_api = "responses"
env_key = "TOKENHARBOR_POOL_KEY"
```

环境变量 `TOKENHARBOR_POOL_KEY` 填本服务下游 `sk-odb-…`，不是上游 `thk_…`。

### Claude Code

`~/.claude/settings.json`：

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "https://pool.example",
    "ANTHROPIC_AUTH_TOKEN": "本服务下游 sk-odb-…"
  }
}
```

模型使用 `/v1/models` 中的实际 ID。本池向上游始终发选中账号的 Bearer key；Messages 缺少 `anthropic-version` 时补 `2023-06-01`。

## 支持边界

| 接口 | 行为 |
|---|---|
| POST `/v1/chat/completions` | 原生 JSON / SSE |
| POST `/v1/responses` | 原生 JSON / SSE；Codex 接入文档明确 `wire_api=responses` |
| POST `/v1/messages` | 原生 JSON / SSE；Claude Code |
| GET `/v1/models` | 分组内已启用账号目录合并 |

请求正文原样发送，不删除 tools、不改写提示词、不丢 `cache_control`、`prompt_cache_key`、工具 ID 或供应商字段。保留会话头、缓存头及 Anthropic beta 头。不注入供应商未公布的缓存路由头。

未实现的路径（embeddings、completions、图像、`responses/compact`、`messages/count_tokens` 等）本地 404，不探测上游；不实现 WebSocket。公开限流页提及图像不等于本轮实现了完整图像协议。

## 会话与缓存亲和

沿用原调度器，Token Harbor **默认启用会话粘性，绑定忙碌不溢出**：有短暂准入等待预算，忙时返回 429，而不是为了均衡把同一历史分散到另一把 key。路由与错误规则独立保存；其他选号策略沿用现有池。

识别优先级：

1. `previous_response_id` 的已确认绑定。
2. 管理员额外会话头、父会话头/`metadata.parent_session_id`。
3. 会话字段和头：`Session_id` / `Session-Id`、`X-Codex-Session-Id` / `X-Codex-Thread-Id`、`X-Claude-Code-Session-Id`、thread/conversation 等；Claude Code 的 `metadata.user_id` 支持 JSON 字符串及 `_session_…` 格式。
4. 没有会话标识才用 `X-Prompt-Cache-Key` / `Prompt-Cache-Key` / `X-Cache-Key` 或正文 `prompt_cache_key`。
5. 可选首条 system/user 的 HMAC 弱粘性。

`cache_control: {type:"ephemeral"}` 是缓存边界，不是会话 ID；不会把所有 ephemeral 请求绑在一起。绑定以分组和下游 key 隔离，普通日志仅记录标识类型/来源，不记录原始会话值或 HMAC。

已绑定账号失效/冷却时，携带完整历史的请求可在同组故障切换，并记录换号及跨账号情况。**Responses 服务端续传不能这样换号**：未知、过期、跨下游 key 或凭证/base/代理变更后的 `previous_response_id` 返回 409；绑定忙时 429；失败不换号。已确认 response ID 绑定可随 metadata 恢复，普通会话绑定不跨重启保留。

发送后结果不确定的网络错误、已向下游提交的 SSE 不重放，避免重复执行工具或重复扣费。凭证请求不跟随重定向。

### 不保证上游缓存命中

官方说明 Claude 缓存 5 分钟、命中续期；自带 `cache_control` 不被上游改写，未带时上游可能自动加标记。供应商内部连接仍可能变化。本地 key 亲和只能增加复用机会，不能强制 Token Harbor 的内部模型提供商固定到同一连接。

## 模型、额度与限流

- 只读取文档支持的 `/v1/models`。默认每 1800 秒刷新，`tokenHarbor.modelSyncSec=0` 关闭自动刷新；还需启用通用额度轮询开关。手动刷新始终可用。刷新失败保留上次目录及时间，标记失败/过期；凭证/base/代理改变会丢弃旧目录。
- **余额/套餐未知**。不调用 dashboard 私有接口、不把模型目录当余额、不调用 OpenDesign 控制面。多 key 余额不求和。
- 官方免费限流按账号（所有 keys/模型合并）及 IP 共享；付费无 RPM/RPH 上限且所有套餐无并发上限。不默认假设账号免费，不给每把 key 编造独立额度。
- 401 隔离无效 key；402 余额/支出上限冷却（10 分钟起递增，上限 6 小时）；429 遵守完整 `Retry-After`；临时 5xx 短冷却；400/404/413/422 不截断、不删工具、不换号。全部 key 不可用时通常返回 502/503，避免把上游凭证问题当作下游认证失败。
- **大额消费确认硬停止**，优先于可编辑错误规则。保留原始确认提示，不自动确认、不通过换 key 绕过。仅原样透传调用方明确提供的 `x-th-confirm-spend`。该头会允许单次花掉大部分余额，应由用户自行决定。
- 从基础代码版本升级时，已有非空规则不被覆盖；在「路由设置 → Token Harbor → 恢复默认规则 → 保存」获得新增 429/5xx/余额冷却规则。

## 概览与日志

复用现有概览、账号状态、请求/错误/详细日志，按 provider 过滤。请求日志包括选中账号、会话依据、绑定命中/换号、各次尝试、状态、耗时、首字时间、输入/输出、缓存读取与写入 token。

Messages 的缓存占比为 `cache_read / (input + cache_read + cache_creation)`；Chat / Responses 按上游计入 input 的 cached 部分统计。缺失 usage 显示 `—` / null，不捏造为零。Token Harbor 概览缓存率只使用同时报告 input/cache 的样本，避免把未知样本算作未命中。

费用仅依赖管理员价格表估算，不代表上游账单。价格表尚无缓存写入单价，因此包含缓存写入的 Token Harbor 请求不估价、不计入费用合计，写入 token 仍单独展示。详细日志默认关闭，显式开启后会保存脱敏正文，可能含用户内容/会话字段，按现有保留策略管理。

## 自动导入

使用 **Token Harbor 池自己的导入密钥**：

```http
POST /open/v1/accounts/import
Authorization: Bearer sk-odbimp-…
Content-Type: application/json

{"provider":"tokenharbor","runtimeKey":"thk_live_…","name":"TH-1"}
```

支持已有批量、冲突暂存与导入记录。Token Harbor 导入密钥不跨池使用。合入本地主分支时保留了主分支已有的「TokenHarbor 注册机 / 设置 / 实时注册日志」及补验证、待验证状态、入池流程；没有新增自动补号。普通「概览 / 账号 / 下游密钥 / 路由设置 / 请求日志 / 错误日志」仍用于本池推理管理。注册设置保存不会重置独立路由或模型刷新间隔，验证码 Key 和带认证的代理不回显。

## 注册会话与 Agent Pass 支付链接

注册机支持 `邮箱----接码页` 和 `邮箱----密码----接码页`；未提供密码会随机生成并加密保存。注册成功即保存会话，创建 API Key 后立即保存，再等待邮件；失败后的「继续」复用会话及已有 Key，不重复注册。恢复邮件查询沿用原注册时间。接码页明确使用独立直连 HTTPS，Token Harbor 站点请求继续使用账号固定代理，不做代理失败直连。新账号默认**不自动同意免费模型的数据使用条款**。

已保存会话的注册记录提供「Agent Pass 链接」。仅处理官网 [Pricing](https://tokenharbor.ai/pricing) 的 Agent Pass 月度单次新购，不是余额充值或其他 Pass。先读取报价，只有 Free 账号、monthly、oneTime 的 Agent Pass（已验证价格不超过 $1.99）才创建 Stripe 托管页面；不自动续费、不记住银行卡、不代付。已有订阅升级、地区确认、变化的报价均停止，需在官网人工处理。

管理员接口（现有会话认证及 CSRF）：

```http
POST /api/tokenharbor/registrations/{rowId}/checkout
Content-Type: application/json

{"kind":"pass","product":"starter_pass_monthly"}
```

`GET` 同一路径读取已保存的链接。两接口均 `Cache-Control: no-store`；URL 仅在管理员显式弹窗显示，不出现在任务轮询和实时日志，持久化沿用加密注册库。重复获取返回原链接，不另建订单；未完成或结果不确定的创建记录阻止重放，先核对官网账单。支付金额、优惠和有效期以 Stripe 页面为准；链接过期或结果不确定时不要反复点击创建。

2026-11-05 真实验收只使用了一个邮箱；其余四个未注册。该账号邮箱已验证，官网正常 Choose Agent 得到 **Agent Pass — Monthly (one-time)，US$0.99** 收银台，未提交付款、未进行推理。会话、Key、链接保存在本地及 OVH 受限目录；账号经现有管理员 API 增量入池，待用户付款所以暂不参与调度。程序的报价保护/自动创建分支用 mock 验证，未为验证重复创建真实订单。代码修改未部署；账号入池不需要重启服务。

## 验证范围

本轮覆盖本地 mock 的三原生协议、JSON/SSE 字节保留、头透传、粘性/忙碌、服务端续传/重启恢复、故障切换、消费确认、重定向/不确定请求禁止重放、目录过期/凭证失效、日志与独立设置。浏览器验证了独立标签、添加/去重导入、下游 key、设置保存、请求/错误日志和缓存统计。合并额外验证注册设置与号池设置互不覆盖，保留 Go 会话/缓存/出口观测能力，缓存写入统计兼容既有 `cw` 与 `kw` 投影但不重复计数。**未部署、未使用真实上游 key 推理，未实机验收 Codex / Claude Code**。
