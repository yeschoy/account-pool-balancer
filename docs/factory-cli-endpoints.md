# Factory CLI 1.14.0 实际调用接口清单

> 只包含**实测调用过的**接口。未调用的一律不列。
> 证据来源：`droid` CLI 交互模式经 mitmproxy 抓包，3.66 小时、399 条 HTTP、27 次 LLM 请求。
> 抓取方式见文末。

## 抓取前置条件（复现必读）

droid 不读系统代理，也不读 macOS 钥匙串，必须同时给两个环境变量：

```bash
NODE_TLS_REJECT_UNAUTHORIZED=0 \
HTTPS_PROXY=http://127.0.0.1:18080 \
ALL_PROXY=http://127.0.0.1:18080 \
NO_PROXY=localhost,127.0.0.1 \
  droid --cwd /tmp
```

- 缺 `HTTPS_PROXY`/`ALL_PROXY` → 直连，代理看不到
- 缺 `NODE_TLS_REJECT_UNAUTHORIZED=0` → TLS 握手被拒（`Client TLS handshake failed`）
- `FACTORY_EXTRA_CA_CERTS` / `NODE_EXTRA_CA_CERTS` / `SSL_CERT_FILE` **均无效**

---

## 一、接口总览（16 个）

| # | 方法 | 主机 | 路径 | 次数 | 协议 |
|---|---|---|---|---|---|
| 1 | POST | telemetry.factory.ai | `/api/ingest` | 242 | JSON |
| 2 | POST | prem.factory.ai | `/api/apps/cloud-sync/sessions/batch` | 47 | JSON |
| 3 | GET | api.factory.ai | `/api/organization/managed-settings` | 37 | JSON |
| 4 | GET | api.factory.ai | `/api/billing/limits` | 17 | JSON (brotli) |
| 5 | POST | prem.factory.ai | `/api/llm/o/v1/chat/completions` | 10 | SSE |
| 6 | POST | prem.factory.ai | `/api/llm/g/v1/generate` | 9 | JSON |
| 7 | GET | api.factory.ai | `/api/feature-flags` | 8 | JSON |
| 8 | GET | prem.factory.ai | `/api/llm/o/v1/responses/ws` | 4 | WebSocket |
| 9 | GET | prem.factory.ai | `/api/internal/user-preferences` | 4 | JSON |
| 10 | POST | prem.factory.ai | `/api/llm/a/v1/messages` | 4 | SSE |
| 11 | POST | api.factory.ai | `/api/automations/sync` | 3 | JSON |
| 12 | GET | api.workos.com | `/sso/jwks/client_01HNM792M5G5G1A2THWPXKFMXB` | 3 | JSON |
| 13 | POST | api.factory.ai | `/api/tools/web-search` | 1 | JSON |
| 14 | POST | api.factory.ai | `/api/tools/get-url-contents` | 1 | JSON |
| 15 | GET | clientservices.googleapis.com | `/chrome-variations/seed` | 8 | 非 Factory |
| 16 | POST | update.googleapis.com | `/service/update2/json` | 1 | 非 Factory |

> 15/16 是 Chromium 自身流量，与 Factory 无关，做反代可忽略。

---

## 二、通用请求头

### 2.1 所有请求必有

```http
Authorization: Bearer <access_token>
User-Agent: <见下>
Accept: */*
Accept-Encoding: gzip, deflate, br, zstd
Host: <对应主机>
Connection: keep-alive
```

### 2.2 User-Agent 分两套（**混用即破绽**）

| 场景 | UA |
|---|---|
| **推理类**（`/api/llm/*`） | `factory-cli/1.14.0` |
| **其它所有 HTTP**（遥测、同步、策略、额度、工具） | `Bun/1.4.4` |
| WorkOS JWKS | `jose/v5.9.6` |

### 2.3 Factory 标识头

| 头 | 值 | 出现范围 |
|---|---|---|
| `X-Factory-Client` | `cli` | 除遥测外的全部 |
| `X-Client-Version` | `1.14.0` | 除遥测外的全部 |
| `X-Factory-Org-Id` | `<org id>` | 除 JWKS 外的全部 |
| `X-Factory-Request-Surface` | `cli_tui`（交互）/ `cli_exec`（exec） | 业务与推理 |
| `traceparent` | `00-<32hex>-<16hex>-01` | 业务与推理（遥测无） |

**各端点的头集合差异：**

```
/api/ingest            只有 Authorization + UA + Content-Type（无 X-Factory-*）
/api/automations/sync  有 X-Factory-Org-Id，但无 X-Factory-Client / X-Client-Version
/api/organization/managed-settings  无 X-Factory-Request-Surface
/api/llm/*             额外有 x-api-provider / x-provider-routing-source / x-session-id
/api/tools/*           额外有 x-session-id / x-client-model
```

> **注意**：头部大小写不统一。`x-factory-org-id`（小写，Bun 发出）与 `X-Factory-Org-Id`（大写）在不同端点混用，HTTP 头不区分大小写，但若做逐字节比对需注意。

---

## 三、逐接口详情

### 3.1 遥测上报 `POST telemetry.factory.ai/api/ingest`（242 次）

**频率**：空闲 **60.0s**（199 次间隔中位 60.00s，标准差 1.39s）；活跃中位 24.4s。3.66 小时共 242 次，平均 66.2 次/小时。

**请求头**

```http
Authorization: Bearer <token>
Content-Type: application/json
User-Agent: Bun/1.4.4
```

**请求体**

```json
{
  "events": [
    {
      "type": "log",
      "level": "info",
      "message": "Using cached system certificates",
      "attributes": {
        "value": "188 certificates, cached 209 minutes ago",
        "tags": {
          "clientType": "cli", "platform": "darwin", "environment": "production",
          "version": "1.14.0", "os": "darwin 24.6.0",
          "terminal": "Orca", "terminalVersion": "1.4.239",
          "droidMode": "...", "sessionId": "<uuid>", "subcommand": "tui",
          "isDroidExec": "false", "isStreamJsonRpcWorker": "false",
          "callingSessionIdPresent": "false", "isInteractiveTty": "true",
          "droidInstallationId": "<uuid>", "factoryTier": "pro_plus",
          "hostId": "<uuid>", "computerId": "<uuid>",
          "traceId": "<32hex>", "spanId": "<16hex>",
          "clientMode": "...", "droidSubMode": "...", "modelId": "...", "isByok": "false"
        }
      },
      "_queuedAt": 1795019293366
    }
  ]
}
```

**tags 字段全集（23 个）**

```
clientType, platform, environment, version, os, terminal, terminalVersion,
droidMode, sessionId, subcommand, isDroidExec, isStreamJsonRpcWorker,
callingSessionIdPresent, isInteractiveTty, droidInstallationId, factoryTier,
hostId, computerId, traceId, spanId, clientMode, droidSubMode, modelId, isByok
```

**载荷规模**

| 指标 | 值 |
|---|---|
| 批次大小 | 2 – 299 条事件 |
| 体积 | 2.1 KB – 312 KB |
| 空闲心跳 | 固定 **3397 B / 3 条** |
| 每小时流量 | 0.5 – 1.3 MB |

**空闲心跳的 3 条固定事件**

```
[metrics_log_daemon_agent_in_process_session_count]    value: 0
[metrics_log_daemon_agent_subprocess_session_count]    value: 1
[metrics_log_daemon_event_loop_delay_p99_ms]           value: 5.816319   ← 每次不同
```

**事件消息样例（出现频次）**

```
43  [tui-startup] Startup phase completed
11  [SettingsService] Hooks and custom models refreshed
 8  [tui-startup] Boundary reached
 6  [Daemon] Settings updated
 5  [metrics_log_cli_startup_phase_ms]
 5  [metrics_log_pre_turn_step_duration_ms]
 4  Cloud user preferences pull finished
 3  [SandboxService] Sandbox not enabled
 3  [McpService] MCP config unchanged
```

**延迟特性**：事件自带 `_queuedAt`，实测有事件在本地队列滞留 **183.6 秒**才上报。遥测不是实时的。

---

### 3.2 会话同步 `POST prem.factory.ai/api/apps/cloud-sync/sessions/batch`（47 次）

**频率**：间隔中位 **10.0 秒**，仅次于遥测的高频请求。

**请求头**

```http
Authorization: Bearer <token>
Content-Type: application/json
X-Client-Version: 1.14.0
X-Factory-Client: cli
X-Factory-Exec-Output-Format: json
X-Factory-Org-Id: <org>
X-Factory-Request-Surface: cli_exec
traceparent: 00-<32hex>-<16hex>-01
User-Agent: Bun/1.4.4
```

**请求体**

```json
{
  "operations": [
    {
      "opId": "0",
      "sessionId": "<uuid>",
      "type": "appendMessages",
      "messages": [ { "id": "<uuid>", "role": "assistant", "content": [...] } ]
    }
  ]
}
```

**operation 类型分布（实测）**

| 类型 | 次数 |
|---|---|
| `appendMessages` | 35 |
| `updateSettings` | 35 |
| `updateDroidStatus` | 29 |
| `updateTitle` | 8 |
| `create` | 4 |
| `updateArtifacts` | 1 |

**`create` 操作含 hostId**

```json
{"sessionId":"<uuid>","type":"create","session":{
  "id":"<uuid>","title":"New Session","isStarted":false,"version":2,
  "machineConnectionType":"tui",
  "hostId":"<uuid>","tags":[...]}}
```

**响应**

```json
{"results":[{"opId":"0","status":"applied","httpStatus":200,
             "previousUpdatedAtMs":1795028604225,"updatedAtMs":1795028608383}]}
```

> **注意**：这里有 conflict 语义——`previousUpdatedAtMs` 用于乐观锁。反代需保留该字段递增逻辑。

---

### 3.3 组织策略 `GET api.factory.ai/api/organization/managed-settings`（37 次）

**频率**：约每 5–6 分钟。

**请求头**：标准业务头，**无** `X-Factory-Request-Surface`。

**响应**

```json
{
  "success": true,
  "settings": {
    "routedModelVisibilityEnabled": false,
    "modelPolicy": {
      "blockedModelIds": ["gpt-6.2-astra-ultrafast","gpt-6-astra-ultrafast","gpt-6.1-sol-ultrafast"],
      "allowAllFactoryModels": true,
      "allowCustomModels": true,
      "isFastModelsAllowed": true,
      "requireExplicitOptInModelIds": ["gpt-6.2-astra-ultrafast","gpt-6-astra-ultrafast","gpt-6.1-sol-ultrafast"]
    }
  },
  "revision": 0,
  "factoryTier": "pro_plus",
  "isUnpaidEnterprise": false,
  "hasStoredSettings": false,
  "evaluationsPolicySupported": true,
  "cmekEnabled": false,
  "cloudSyncDisabledByCompliance": false
}
```

---

### 3.4 额度 `GET api.factory.ai/api/billing/limits`（17 次）

**频率**：约每 12 分钟。

**响应为 brotli 压缩**（`Content-Encoding: br`），解压后 690 B：

```json
{
  "usesTokenRateLimitsBilling": true,
  "hasActiveSubscription": true,
  "limits": {
    "standard": {
      "fiveHour": { "usedPercent": 3, "windowEnd": "2026-11-18T18:32:52.554Z", "secondsRemaining": 7479 },
      "weekly":   { "usedPercent": 1, "windowEnd": "2026-11-24T21:04:14.291Z", "secondsRemaining": 534961 },
      "monthly":  { "usedPercent": 1, "windowEnd": "2026-12-17T21:04:14.291Z", "secondsRemaining": 2522161 }
    },
    "core": {
      "fiveHour": { "usedPercent": 0, "windowEnd": null, "secondsRemaining": null },
      "weekly":   { "usedPercent": 0, "windowEnd": null, "secondsRemaining": null },
      "monthly":  { "usedPercent": 0, "windowEnd": null, "secondsRemaining": null }
    }
  },
  "overagePreference": "droidCore",
  "canManageOverage": true,
  "extraUsageBalanceCents": 0,
  "extraUsageAllowed": true
}
```

**要点**
- 两套池：`standard` / `core`
- 每池三档：`fiveHour` / `weekly` / `monthly`
- **只有百分比，无绝对额度值**
- `extraUsageBalanceCents` 是唯一绝对金额字段

---

### 3.5 功能开关 `GET api.factory.ai/api/feature-flags`（8 次）

**请求头**：标准业务头 + `x-provider-routing-refresh: 1`。

**响应（节选）**

```json
{"flags":{
  "compliance_session_export": false,
  "unified_model_fallbacks": false,
  "anthropic_linked_billing": false,
  "claude_fable_5_2": true,
  "repository_checkout_collision_handling": true,
  "daemon_cgroup_isolation": false,
  "encrypted_relay_transport": false,
  "bitbucket_integration": false,
  "azure_devops_integration": true,
  "workspaces_enabled": false,
  "droid_shield_2.0": false,
  "shared_process_agents": true
}}
```

---

### 3.6 用户偏好 `GET prem.factory.ai/api/internal/user-preferences`（4 次）

**请求头**：标准业务头，`X-Factory-Request-Surface: cli_tui`（小写形式）。

**响应（本账号为空）**

```json
{"schemaVersion": null, "settings": null, "updatedAt": null}
```

---

### 3.7 自动化同步 `POST api.factory.ai/api/automations/sync`（3 次）

**请求头**：仅 `Authorization` + `X-Factory-Org-Id` + `Content-Type` + `Bun/1.4.4`。**无** `X-Factory-Client`。

**请求体**

```json
{
  "automations": [],
  "reportsFieldPresence": true,
  "hostId": "<uuid>",
  "daemonVersion": "1.14.0",
  "snapshot": { "hostId": "<uuid>", "automationIds": [] }
}
```

**响应**

```json
{"success": true, "synced": 0, "collisions": [], "deleted": [], "tombstoned": []}
```

> 这是**第二个携带 hostId 的接口**（另一个是 cloud-sync 的 `create` 操作）。

---

### 3.8 模型推理：四条路径

#### 3.8.1 `POST prem.factory.ai/api/llm/o/v1/chat/completions`（10 次）

**OpenAI Chat 协议**，用于 `deepseek-v4.1-flash-1017`（baseten）、`kimi-k3.1-pro`（databricks）。

**请求头**

```http
Accept: application/json
Authorization: Bearer <token>
Content-Type: application/json
User-Agent: factory-cli/1.14.0
traceparent: 00-<32hex>-<16hex>-01
x-api-provider: baseten | databricks
x-assistant-message-id: <uuid>
x-client-version: 1.14.0
x-factory-client: cli
x-factory-org-id: <org>
x-factory-request-surface: cli_tui
x-provider-routing-source: session_lock | configured_order
x-session-id: <uuid>
```

**请求体（约 58 KB）**

```json
{
  "model": "deepseek-v4.1-flash-1017",
  "messages": [
    {"role":"system","content":"You are Droid, an AI software engineering agent built by Factory..."},
    {"role":"user","content":[{"type":"text","text":"<system-reminder>...环境上下文..."}]},
    {"role":"user","content":"用户真实输入"},
    {"role":"assistant","content":"..."},
    {"role":"user","content":"..."}
  ],
  "tools": [{"type":"function","function":{"name":"Read",...}}, ...],
  "stream": true,
  "stream_options": {...},
  "max_tokens": 131072,
  "temperature": 1,
  "reasoning_effort": "high"
}
```

工具集 15 个：`Read, LS, Execute, Edit, Grep, Glob, Create, ExitSpecMode, AskUser, TodoWrite, TaskOutput, TaskStop, ToolSearch, Skill, Task`

**响应**：`Content-Type: text/event-stream`，SSE 流，末帧含 usage：

```json
{"usage":{"prompt_tokens":13374,"completion_tokens":283,"total_tokens":13657,
          "prompt_tokens_details":{"cached_tokens":1280,...},
          "completion_tokens_details":{"reasoning_tokens":107,...}}}
```

**响应头含限流信息**

```http
X-Ratelimit-Limit-Requests: 1000
X-Ratelimit-Limit-Tokens: 40000000
X-Ratelimit-Remaining-Requests: 500
X-Ratelimit-Remaining-Tokens: 39984147
X-Factory-Stream-Deadline-Ms: 1740000
X-Baseten-Request-Id: ...
Server: Vercel
Via: 1.1 google
```

---

#### 3.8.2 `POST prem.factory.ai/api/llm/g/v1/generate`（9 次）

**Gemini 原生协议**，仅用于 `gemini-4-carbon`（google）。

**请求头**：与 chat/completions 类似，但 `x-api-provider: google`、`Accept: */*`。

**请求体（约 458 KB！）**

```json
{
  "model": "gemini-4-carbon",
  "contents": [ 51 条：user×32 + model×19 ],
  "systemInstruction": { ... },
  "tools": [{"functionDeclarations":[
      "ExitSpecMode","AskUser","Script","WaitForScript","TaskOutput","TaskStop","Task"
  ]}],
  "generationConfig": {
    "maxOutputTokens": 65536,
    "thinkingConfig": { "includeThoughts": true, "thinkingLevel": "LOW" }
  }
}
```

`contents[0].parts` 的 6 个上下文块：

| parts | 大小 | 内容 |
|---|---|---|
| [0] | 3540 字 | 统一工具目录 |
| [1] | 7177 字 | 可用 skills 列表 |
| [2] | 1093 字 | 子代理列表（含自定义 droid 路径） |
| [3] | 566 字 | **环境信息：OS / CPU / 内存 / 磁盘 / pwd** |
| [4] | 100 字 | 当前日期 |
| [5] | 603 字 | **会话启动时的 `ls` 输出** |

环境块实测样例：

```
User system info (darwin 24.6.0)
Resources: 10 CPUs, 24 GB memory
Disk: 80 GB free of 460 GB (working directory, /var/... and home share this filesystem)
Model: Gemini 4 Carbon
User language: en

% pwd
/private/tmp
```

> **这些块是 CLI 每次请求时现场生成的**（同一机器磁盘从 82GB → 80GB），不是静态模板。

---

#### 3.8.3 `GET prem.factory.ai/api/llm/o/v1/responses/ws`（4 次，WebSocket）

**OpenAI Responses 协议 over WebSocket**，用于 `gpt-6.2-sol`（openai）。

**握手请求头**

```http
Host: prem.factory.ai
Connection: Upgrade
Upgrade: websocket
Sec-WebSocket-Version: 13
Sec-WebSocket-Extensions: permessage-deflate; client_max_window_bits
Sec-WebSocket-Key: <随机 16B base64>
Authorization: Bearer <token>
OpenAI-Platform: org-<base64>
User-Agent: factory-cli/1.14.0
X-Client-Version: 1.14.0
X-Factory-Client: cli
X-Factory-Exec-Output-Format: json
X-Factory-Org-Id: <org>
X-Factory-Request-Surface: cli_exec
x-api-provider: openai
x-provider-routing-source: configured_order
x-session-id: <uuid>
```

**客户端帧：`response.create`**

```json
{
  "type": "response.create",
  "model": "gpt-6.2-sol",
  "store": false,
  "tools": [...],
  "instructions": "You are Droid, ...",
  "tool_choice": "auto",
  "parallel_tool_calls": true,
  "include": ["reasoning.encrypted_content"],
  "reasoning": { "effort": "high", "summary": "auto" },
  "text": { "verbosity": "low" },
  "prompt_cache_key": "<sessionId>",
  "prompt_cache_retention": "24h",
  "safety_identifier": "<sessionId>",
  "input": [{"role":"user","content":[{"type":"input_text","text":"..."}]}],
  "_factory": {
    "assistantMessageId": "<uuid>",
    "traceContext": { "traceparent": "00-<32hex>-<16hex>-01" },
    "usageAttribution": {
      "request_surface": "cli_tui",
      "client_type": "cli",
      "client_version": "1.14.0"
    }
  }
}
```

**服务端事件序列**

```
response.created
response.in_progress
response.output_item.added
response.content_part.added
response.output_text.delta      (流式文本)
response.custom_tool_call_input.delta  (工具参数流式)
response.output_text.done
response.content_part.done
response.output_item.done
response.completed              (含 usage)
```

`response.completed` 的 usage：

```json
{"input_tokens":11210,
 "input_tokens_details":{"cached_tokens":0,
   "cache_write_tokens":11207,
   "cache_write_tokens_details":{"ttl_30m_tokens":11207,"ttl_12h_tokens":0}},
 "output_tokens":5}
```

---

#### 3.8.4 `POST prem.factory.ai/api/llm/a/v1/messages`（4 次）

**Anthropic 协议**，用于 `claude-sonnet-5-5` / `claude-haiku-4-5-20251001`（bedrock_anthropic）。

**请求头**

```http
anthropic-version: 2023-06-01
x-api-provider: bedrock_anthropic
x-message-call-type: session-title-generation    ← 副任务标记
x-assistant-message-id: oneshot-assistant-<ts>
x-factory-request-surface: cli_tui
User-Agent: factory-cli/1.14.0
```

**请求体（约 2 KB）**

```json
{"model":"claude-haiku-4-5-20251001","max_tokens":<n>,
 "messages":[{"role":"user","content":"..."}],
 "system":"...","tools":[...],"stream":true}
```

> `x-message-call-type: session-title-generation` 说明这是**生成会话标题**的副任务，
> 与主对话分离。反代若只实现主链路，表现为「有回复但标题不生成」。

---

### 3.9 工具类接口（各 1 次）

#### 3.9.1 `POST api.factory.ai/api/tools/web-search`

服务端代做网页搜索。

**请求头**：标准业务头 + `x-client-model: gemini-4-carbon` + `x-session-id`。

**请求体（71 B）**

```json
{"query":"Los Angeles weather today","knowledge":"core","numResults":5}
```

**响应**

```json
{"results":{"web":[
  {"title":"...","url":"https://...","description":"...","snippets":["..."]}
]}}
```

#### 3.9.2 `POST api.factory.ai/api/tools/get-url-contents`

服务端代抓网页，转 markdown。

**请求体（331 B）**

```json
{"url":"https://api.open-meteo.com/v1/forecast?latitude=34.0522&..."}
```

**响应**

```json
{"data":{"markdown":"```json\n{...}\n```"}}
```

> **关键**：模型的“联网”是服务端代做，客户端不需要外网访问目标站点。

---

### 3.10 WorkOS 公钥 `GET api.workos.com/sso/jwks/client_01HNM792M5G5G1A2THWPXKFMXB`（3 次）

**请求头**：`User-Agent: jose/v5.9.6`，`Accept: */*`。

**用途**：拉 JWKS 验证 access_token 签名。**无 Authorization 头**。

---

## 四、模型 → 供应商映射（实测）

| 模型 | `x-api-provider` | 协议路径 | 次数 |
|---|---|---|---|
| `gemini-4-carbon` | google | `/g/v1/generate` | 9 |
| `deepseek-v4.1-flash-1017` | baseten | `/o/v1/chat/completions` | 5 |
| `kimi-k3.1-pro` | databricks | `/o/v1/chat/completions` | 3 |
| `claude-sonnet-5-5` | bedrock_anthropic | `/a/v1/messages` | 2 |
| `claude-haiku-4-5-20251001` | bedrock_anthropic | `/a/v1/messages` | 2 |
| `gpt-6.2-sol` | openai | `/o/v1/responses/ws` | 4 |

**路由来源**：`session_lock`（会话锁定，不重选）/ `configured_order`（按配置顺序）。

**并发子任务可用不同供应商**：14 秒内 4 次调用，跨 databricks 与 baseten。

---

## 五、频率汇总

| 接口 | 次数 | 节奏 |
|---|---|---|
| `/api/ingest` | 242 | 空闲 **60.0s**（±1.4s），活跃 24.4s |
| `cloud-sync/sessions/batch` | 47 | **10.0s** |
| `organization/managed-settings` | 37 | 约 5–6 分钟 |
| `billing/limits` | 17 | 约 12 分钟 |
| `feature-flags` | 8 | 稀疏 |
| `llm/*` | 23 | 按对话触发 |
| `user-preferences` | 4 | 稀疏 |
| `automations/sync` | 3 | 稀疏 |
| `tools/*` | 2 | 工具调用时 |

---

## 六、设备标识位置

| 标识 | 传输位置 | 说明 |
|---|---|---|
| `droidInstallationId` | 遥测 tags | 独立 UUID，36 位 |
| `hostId` | cloud-sync `create` 操作体、automations/sync | 独立 UUID |
| `computerId` | 遥测 tags | 独立 UUID |
| `sessionId` | 遥测 tags、推理头 `x-session-id`、请求体 | 每会话一个 |
| `traceparent` | 业务/推理请求头、`_factory.traceContext` | W3C 格式 |
| **IP 地址** | ❌ 从不发送 | 仅 TCP 层 |
| **MAC 地址** | ❌ 从不发送 | — |
| **主机名** | ❌ 从不发送 | — |

**环境信息**（写在 prompt 里，非请求头）：

```
OS 版本、CPU 核数、内存大小、磁盘剩余、语言、pwd、目录列表
```

---

## 七、复刻检查表

1. **UA 分两套**：推理用 `factory-cli/1.14.0`，其它用 `Bun/1.4.4`
2. **设备标识在请求体**，不在头里；`hostId` 必须出现在 cloud-sync 与 automations
3. **两个高频节奏**：遥测 60s、会话同步 10s
4. **四条推理路径**，按 `x-api-provider` 区分协议
5. **`_factory.usageAttribution`** 必须匹配真实端：`cli_tui` / `cli_exec` / `desktop`
6. **遥测有延迟**（最长 183s），实时上报反而不像
7. **`x-provider-routing-source`** 二值：`session_lock` / `configured_order`
8. **响应压缩**：`/api/billing/limits` 是 brotli，其余多为 gzip/br

---

## 八、本项目实现映射（2026-11-19）

| 抓包结论 | 本项目状态 |
|---|---|
| 双 User-Agent：推理 `factory-cli/1.14.0`，其它 `Bun/1.4.4` | 已实现：模型请求与管理/额度读取分别发送 |
| `X-Factory-Request-Surface`：`cli_tui` / `cli_exec` | 已实现为号池设置，默认 `cli_tui`；`cli_exec` 同时发 `X-Factory-Exec-Output-Format: json` |
| `traceparent: 00-<32hex>-<16hex>-01` | 已实现：同会话复用 trace，逐消息换 span；额度读取使用随机 trace |
| `x-provider-routing-source`：`configured_order` / `session_lock` | 发送 `configured_order`（本网关按模型配置选上游；不声称 droid 的会话锁定语义） |
| `OpenAI-Platform: org-<base64>` | 可选设置；whoami 不返回映射且抓包值属于单个组织，默认不发送、不伪造 |
| 管理面 `api.factory.ai`、模型面 `prem.factory.ai` | 已实现双源站：管理面读取 whoami/billing，模型面走 `/api/llm/*`；推理源站可显式覆盖 |
| whoami 头部：Bearer + org + `X-Factory-Whoami-Extended` + `Bun/1.4.4` | 已按端点区分；不再用推理 UA 读取额度 |
| 模型→供应商：`bedrock_anthropic`、`databricks`、`baseten`、`openai`、`google` | 默认路由已按抓包修正；`google` 仅用于 `protocol=generate`，不提供聊天路由的无效映射 |
| 推理路径 | **已实现**：Chat、Responses HTTP、Messages、Gemini原生 `/v1beta/models/{model}:generateContent`/`:streamGenerateContent` → `/api/llm/g/v1/generate`、`:countTokens` → `/api/llm/g/v1/countTokens`、`/v1/embeddings` → `/api/llm/o/v1/embeddings`，以及 `/v1/responses/ws` → `wss://prem.../api/llm/o/v1/responses/ws` 文本帧透传 |
| auto / auto-fast | 网关虚拟模型：`auto`选同协议第一条已配置路由，`auto-fast`选第一条`-fast`；droid 原本在客户端选择，不属于 Factory 上游 Router 复刻 |
| Responses WebSocket / 遥测 / 会话同步 / `_factory.usageAttribution` | **WS 已实现下游透传**；遥测、会话同步、usageAttribution 仍未实现/不伪造。WS 升级后不重试，不计入HTTP统计 |

实现说明见 `docs/factory-pool.md`；未部署、未通过真实 Factory 模型推理验收。
