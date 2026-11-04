# OpenCode Zen / Go 账号池：本轮交付与操作指南

> 本轮为可独立验证的增量交付，不宣称整个全面升级任务已经完成。尤其：全仓逐行穷尽审计、全能力低延迟跨协议桥接、五种客户端实机验收仍未完成。Mock 不能证明真实上游授权或订阅权限。

源码对照和许可证决策：[opencode-audit.md](opencode-audit.md)。

后续状态：已获用户授权在生产开启 Go 并验证真实模型/额度读取，仍未做真实推理。
Go 设置、指纹与风险控制的专项对照及当前配置见
[opencode-go-risk-audit.md](opencode-go-risk-audit.md)；下文原轮次的“无真实凭证/未验证 usage”记录是历史边界，不代表这次后续核验未进行。

## 1. 实际完成

- 在原有 config/account/group/client-key/import-key/pool 链路中加入 `opencode-zen`、`opencode-go`，不是第二套系统。
- 复用单账号增删改、启用/禁用、冷却恢复、固定代理、权重/优先级、并发/RPM、开放导入、暂存冲突、独立下游密钥。
- 控制台新增两个标签页；复用账号表、设置、概览、日志；增加勾选批量启用/禁用/恢复/删除、同步模型/额度、检测出口。批量操作顺序调用原有账号 API，不引入另一套批处理存储。
- OpenCode 必须明确声明套餐，Key 的 `sk-` 等前缀不会覆盖声明；新账号不能占用其他供应商的保留分组。API Key 接受 8–512 字符的 ASCII token，不接受空白/控制字符。
- Zen / Go 分别读取模型能力，同名模型在两个套餐可使用不同原生协议。账号自己的 `/models` 与套餐能力相交，不把其他账号目录当作本账号权限。
- 可选管理员确认的协议级 Base，支持新版公开目录的 `/inference/{family}/v1`、`/inference/go/{family}/v1` 路由形状，模型/额度查询地址保持独立；不自动信任公开目录中的任意 API URL。
- 原生 Chat、Responses、Messages JSON/SSE 正文不改写；保留原生工具关联、thinking、图片、provider-specific 字段。实际能力依赖上游；网关不因某个路径存在就声称全部支持。
- 可选受限跨协议桥接，默认关闭。支持基础文本、受限图片、函数工具、工具结果、JSON/SSE 输出和已报告用量；无法表达的数据明确报错。
- 显式跨套餐回退，默认关闭。原套餐优先；只有指定另一套餐分组才允许回退。切换时重新编码和选择鉴权，不混合账号池、不突破总尝试次数、不递归回退。
- OpenCode 会话按“原分组 + 下游 Key ID”隔离；支持显式会话、OpenCode 父子会话与原有 Claude/Codex 标识。绑定账号满载时等待/429，不借用另一个账号。
- 原生 Responses 的 `previous_response_id` 严格绑定；未知/过期/已失效绑定返回 409，不随机换号、不删除 previous_response_id 强行重放。已确认的 Responses 绑定随 metadata checkpoint 保存，包含凭证/代理/上游来源校验，重启可恢复。
- 明确 FreeTierError / 已识别的网络滥用等政策拒绝直接停止，不换号、不换出口；此安全边界不能被可编辑重试规则覆盖。
- 独立错误规则：401 认证隔离；402 余额冷却；403 模型级拒绝；429 按 Retry-After（秒数及 HTTP 日期）；5xx 模型级短冷却；普通 400/422 不换号、不隔离整个池。
- HTTP/SSE 提交边界保留；输出有效事件后禁止重放。已写请求头后的不确定网络失败不自动重放，连接前失败可有界重试。异步 HTTP trace 使用原子发送标记，释放后的租约不再扣 RPM。
- Go usage 查询、真实/未知/过期状态、三窗口重置时间及持久化缓存。Zen 无可靠余额接口，明确显示未知。额度读取失败保留上次成功值和时间，不伪装成 0。
- 固定代理健康与账号状态分开；连接失败不扣 OpenCode 账号健康分，30 秒出口冷却，到期可探测，管理员可显式检测恢复。不会因代理故障直连。代理健康本身仅在内存保存，重启变 unknown，不代表额度被重置。
- 校验通过后才换配置；旧设置客户端不带 opencode 字段时保留已存的新配置。独立 Zen/Go 路由设置可继承原全局默认值。凭证/代理/Base/能力源变化使旧目录及相关绑定失效。
- 停机先停止请求入场、取消并排空在途请求，释放租约，再保存状态及关闭日志；额度轮询也等待已取消 worker 退出。修复已有测试未关闭异步日志 writer 导致的 TempDir 清理竞态，没有删除测试。
- Docker 构建上下文排除参考克隆、缓存、临时凭证及测试数据；没有增加数据库或 Go 依赖。

## 2. 仍未完成 / 明确限制

1. 未完成两个仓库的所有文件逐行人工阅读；本轮是核心链路架构审计，范围和证据见审计文档。不能作为全仓安全审计证书。
2. 跨协议 SSE 是**缓冲式**：先完整接收并校验，随后发出按序目标 SSE。因此 TTFT 包含完整上游生成耗时，不是实时 token 桥接。最大 16 MiB，总读取受 requestTimeoutSec 控制；默认关闭。原生 SSE 不缓冲整个响应。
3. 跨协议不支持 signed/encrypted thinking、reasoning 控制/内容、hosted tools、Responses 服务端引用、复杂工具结果/图片工具结果、无法对应的 developer→Messages 优先级、JSON schema 输出模式、audio/video/file、复杂注解、未知参数或多个 choices。明确返回 400（请求不可表示）或 502（上游已生成不可表示响应），不静默丢弃，不重试消费另一账号。
4. Chat/Responses 的必需测量用量缺失时不发明 0；转换到要求 usage 的 Messages 会报错。原生协议始终保留上游自己的 usage。缓存创建与 reasoning token 的已报告值记入请求日志；现有聚合计费仍是模型价格表估算，不是上游账单，也没有逐 TTL 缓存写入计费。
5. `previous_response_id` 仅支持上游原生 Responses。转换到 Chat/Messages 不具备服务端响应存储；请发送完整历史、原始 function_call + function_call_output，不能只发工具结果或伪造上下文。网关不提供响应 GET/DELETE/后台任务端点，只开放三个精确 POST 路径和模型 GET。
6. `metadata`、`store`、prompt-cache 等额外字段在原生请求中完整保留，但大部分不属于本轮跨协议子集；这会限制 Claude Code/Codex 的非原生组合。优先给客户端选匹配的原生协议模型。
7. 模型列表是广告/可见性证据，不保证实际 Key 付费推理权限；403 等真实响应仍决定可调用性。SDK 元数据也可能继承默认协议，需要管理员根据验证结果做套餐级覆盖，不能保证公开目录永远正确。未做自动付费探测、sota/sweet 别名或 WARP。
8. 一个原始 runtimeKey 在主项目仍全局唯一；不支持把同一个 token 重复登记到两个套餐。开放导入继续沿用主项目的全局名称冲突暂存语义；建议套餐名前缀区分账号名。
9. 400/422 默认按参数错误停止，只有经验证且管理员明确配置的供应商限额规则才改变处理，不凭空猜测限额字段。只有明确配置的另一分组能回退，最多一跳；绑定忙、请求本身错误、转换失败和有状态续传均不触发跨套餐回退。总尝试数用尽后也不再回退。
10. 全局 quota.enabled 关闭时周期刷新关闭；管理员手动刷新仍可执行，新增/更换 OpenCode 凭证也会触发一次主动同步。OpenCode enabled=false 则推理、查询及诊断均不联系上游。账号模型目录超过 24 小时会阻塞；能力缓存超过 24 小时仅在显式协议覆盖时可调用。未知额度不阻塞，未知目录会阻塞。
11. 仅原生 Responses 绑定做 checkpoint；一般弱/显式会话绑定不持久化。checkpoint 间隔内崩溃可能失去最近绑定，届时返回 409 而不是把请求交给新账号。上游自身状态到期无法由网关恢复。
12. 未提供删除账号后的回收站；“恢复”沿用原系统语义，清除冷却/隔离，不恢复已经删除的凭证。没有进行大量账号持续压测、动态出口认证、真实跨平台代理连通性、生产带载 Docker 重启或完整指标仪表盘验收。
13. 没有真实上游凭证，没有实际运行 Claude Code、Codex、OpenCode、NewAPI、Sub2API 的端到端会话；不能声称这些客户端均已通过实测。

## 3. 启用与配置

先备份整个 DATA_DIR，再升级可执行文件。进入 OpenCode Zen / Go 的“路由设置”，启用 OpenCode；默认 `enabled=false`，其他池不受影响。添加合法凭证后手动同步模型，创建本分组下游 Key，先做匹配原生协议的小请求验证。

`config.json` 仍为 version 1，新增 `opencode` 对象：

| 字段 | 默认值 / 含义 |
|---|---|
| enabled | false；显式启用新供应商 |
| compatibility | false；受限缓冲转换开关 |
| zenBase | `https://opencode.ai/zen/v1` |
| goBase | `https://opencode.ai/zen/go/v1` |
| capabilitiesUrl | `https://models.opencode.ai/api.json`，16 MiB 上限 |
| zenDocsUrl / goDocsUrl | 空；可选端点表来源，配置后读取失败不会静默当成功 |
| refreshSec | 600；60–86400，模型与能力刷新周期 |
| zenProtocols / goProtocols | `{}`；模型名→`chat`/`responses`/`messages`，分别覆盖 |
| zenProtocolBases / goProtocolBases | `{}`；协议→经管理员确认的推理 Base，包含 `/v1`；不改变模型和 usage 查询 Base |
| zenRouting / goRouting | 缺省继承全局 routing；控制台保存对应套餐设置时建立独立副本 |
| zenFallbackGroup | 空；Zen 不可用时允许回退的 Go 分组 |
| goFallbackGroup | 空；Go 不可用时允许回退的 Zen 分组 |
| zenErrorRules / zenRetryRules | 独立 Zen 默认规则；ID 为 opencode-zen-* |
| goErrorRules / goRetryRules | 独立 Go 默认规则；ID 为 opencode-go-* |

Base 按主项目惯例**包含 `/v1`**。Messages 使用 x-api-key、anthropic-version；Go Messages 附加 beta=true；Chat/Responses 使用 Bearer。不要自行伪造官方 User-Agent、身份、项目 ID、提示词或工具以绕过供应商门禁。免费模型如果拒绝通用 API 请求，按原始拒绝处理。

### 当前公开目录的新路径声明（不等于鉴权实测）

本轮真实读取公开目录，观察到不同 SDK 的 API 已按 family 分开。保留用户给定及参考使用的旧 Base 默认值，但可在管理台“协议 Base 覆盖”中明确配置，例如：

```json
{
  "zenProtocolBases": {
    "chat": "https://opencode.ai/inference/openai/v1",
    "responses": "https://opencode.ai/inference/openai/v1",
    "messages": "https://opencode.ai/inference/anthropic/v1"
  },
  "goProtocolBases": {
    "chat": "https://opencode.ai/inference/go/openai/v1",
    "responses": "https://opencode.ai/inference/go/openai/v1",
    "messages": "https://opencode.ai/inference/go/anthropic/v1"
  }
}
```

这些地址仅由当前公开能力数据声明，未使用真实 Key 调用；旧路径是否继续兼容也未验证。models/usage 继续由 zenBase/goBase 查询，不能把推理 family 地址自动当作控制接口。公开目录中的 Google/Mistral SDK 不在本轮三协议识别表内，不能因 API 包含 openai 就判定支持。

来源字段不允许嵌入 URL 账号密码、query 或 fragment。账号代理仍支持 HTTP/HTTPS/SOCKS5/SOCKS5H 及空字符串 Direct；代理凭证只存本地配置，API 视图脱敏。动态代理地址也只被当作一个固定配置，不会因出口变化清除额度状态。

Go usage 每分钟检查，失败下一分钟重试；模型目录保留上次成功值，最多信任 24 小时。手动刷新强制重读并失效公开能力缓存，且不会扣推理 RPM、选中数或 Token 额度。查询与推理使用同一账号固定代理，禁止 redirect 泄漏凭证。

## 4. 账号导入

控制台选中相应套餐，“批量导入”，一行一个（重复 Key 跳过）：

```text
ZEN_API_KEY_1,zen-01,socks5h://127.0.0.1:1080
ZEN_API_KEY_2,zen-02
```

Go 账号在 Go 标签导入。两种池默认分组分别为 `opencode-zen`、`opencode-go`。可建自定义组，但一个组只能属于一个 provider。

开放接口复用 `POST /open/v1/accounts/import`，每次最多 200 个。先在相应池“下游密钥”页的导入密钥区创建 **独立导入 Key**；它不是推理 Key。以下凭证仅为占位符：

```bash
curl http://127.0.0.1:8790/open/v1/accounts/import \
  -H "Authorization: Bearer $ZEN_IMPORT_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"accounts":[{"provider":"opencode-zen","apiKey":"ZEN_API_KEY_1","name":"zen-01","group":"opencode-zen","proxyUrl":"socks5h://127.0.0.1:1080"}]}'

curl http://127.0.0.1:8790/open/v1/accounts/import \
  -H "Authorization: Bearer $GO_IMPORT_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"accounts":[{"provider":"opencode-go","apiKey":"GO_API_KEY_1","name":"go-01","group":"opencode-go"}]}'
```

`runtimeKey`/`apiKey`/`key` 均沿用原导入别名。OpenCode 导入 Key 不能跨 provider/tier；普通旧导入 Key 也不能越权导入 OpenCode。重复导入相同账号返回 unchanged，名称/凭证冲突仍进入暂存；同一原始 Key 已属其他套餐则拒绝。管理 API `GET /api/accounts/{id}/keys` 对 OpenCode 返回 403，上游凭证仅写入，不回显到前端。

## 5. 下游调用与状态续传

使用对应分组的 **下游推理 Key**，不是上游 OpenCode Key，也不是 import Key：

```bash
curl http://127.0.0.1:8790/v1/models -H "Authorization: Bearer $POOL_KEY"

curl http://127.0.0.1:8790/v1/chat/completions \
  -H "Authorization: Bearer $POOL_KEY" -H 'Content-Type: application/json' \
  -H 'X-Session-Id: your-stable-session' \
  -d '{"model":"ACTUAL_CHAT_MODEL","messages":[{"role":"user","content":"hello"}],"max_tokens":128}'

curl http://127.0.0.1:8790/v1/responses \
  -H "Authorization: Bearer $POOL_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"ACTUAL_RESPONSES_MODEL","input":"hello","max_output_tokens":128}'

curl http://127.0.0.1:8790/v1/messages \
  -H "X-Api-Key: $POOL_KEY" -H 'Anthropic-Version: 2023-06-01' \
  -H 'Content-Type: application/json' \
  -d '{"model":"ACTUAL_MESSAGES_MODEL","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}'
```

`ACTUAL_*` 必须替换为已同步的、允许调用的模型 ID；不要使用示例名做权限判断。`/v1/models` 会附 native_protocol 与模型能力/上下文元数据。启用回退后模型列表包含显式授权的回退组，重名优先主组元数据。

原生 Responses 得到真实 response.id 后，可以按上游协议传 previous_response_id 和对应 call_id 的 function_call_output。绑定不可用返回 409；客户端应明确选择重试等待、重新建立会话或提交完整历史，不能删除字段后假装状态还在。上游必须本来就支持这种有状态续传。

## 6. 客户端接入模板（未实机验收）

### NewAPI

使用该版本支持的 OpenAI-compatible / 自定义渠道，把 API 入口指向此服务，密钥填本分组 POOL_KEY，同步模型。常见版本的渠道 Base 填 `http://account-pool-balancer:8790`，由渠道补 `/v1/...`；若该版本要求完整 API Base，则填末尾带 `/v1` 的地址，必须核对其实际发出的路径，避免双 `/v1`。Messages 模式须选择原生 Messages 模型和支持 Anthropic 转发的渠道类型。现有 Docker 网络别名 `opendesign-balancer` 保留。

### Sub2API

只有所用版本支持“自定义 API Key 上游 / 自定义 Base URL”时，才能填 `http://account-pool-balancer:8790/v1` + POOL_KEY，并选择与 native_protocol 匹配的协议。**不能把 POOL_KEY 当作 OpenAI/Anthropic OAuth token 导入**。如果版本仅接受 OAuth 账号、不能配置通用上游，则本轮没有实现对其的专用适配，不能宣称直接接入成功。

### Claude Code

选原生 Messages 模型，按客户端版本配置自定义 Anthropic endpoint：

```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:8790
export ANTHROPIC_AUTH_TOKEN="$POOL_KEY"
export ANTHROPIC_MODEL=ACTUAL_MESSAGES_MODEL
claude
```

原生模式保留工具、cache_control、thinking 和 beta 请求字段。上游仍可拒绝某个 beta 能力；不要把该错误改写成成功。此环境变量模板不是官方客户端完整能力认证。

### Codex

在支持自定义 provider 的版本中，示例 `~/.codex/config.toml`：

```toml
model_provider = "account_pool"
model = "ACTUAL_RESPONSES_MODEL"

[model_providers.account_pool]
name = "Account Pool"
base_url = "http://127.0.0.1:8790/v1"
env_key = "ACCOUNT_POOL_API_KEY"
wire_api = "responses"
```

启动前设置 `ACCOUNT_POOL_API_KEY` 为 POOL_KEY。必须选原生 Responses，并验证该上游对 store/previous_response_id/工具续传的真实支持。后台响应资源 GET/DELETE 不在本轮范围。

### OpenCode

使用支持自定义 provider 的版本，Chat 模型示例：

```json
{
  "provider": {
    "account-pool": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Account Pool",
      "options": {
        "baseURL": "http://127.0.0.1:8790/v1",
        "apiKey": "{env:ACCOUNT_POOL_API_KEY}"
      },
      "models": { "ACTUAL_CHAT_MODEL": {} }
    }
  }
}
```

原生 Responses 对应 `@ai-sdk/openai`，原生 Messages 对应 `@ai-sdk/anthropic`；分别配置该协议模型，不要把所有模型都交给 openai-compatible SDK。版本语法需以实际客户端为准，网关不会伪装官方 OpenCode 门禁身份。

## 7. 测试与构建记录

本轮在 darwin/arm64、Go 1.26.8 上验证，全部使用本地 Mock Server，不依赖上游凭证。

| 最终验收命令 | 结果 |
|---|---|
| `gofmt -l cmd internal` | 通过，输出为空 |
| `git diff --check` | 通过 |
| `go test ./...` | 通过（server 包 57.656s） |
| `go test -race ./...` | 通过（darwin/arm64，server 包 78.346s） |
| `go vet ./...` | 通过 |
| `go build -o .analysis/odb-upgrade ./cmd/odb` | 通过 |
| `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ...` | 通过，交叉构建，未在 Linux 执行 |
| `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ...` | 通过，交叉构建，未在 Linux 执行 |
| `node --check internal/server/web/app.js` | 通过 |
| `docker compose config --quiet` | 通过 |
| Docker 镜像构建/运行 | 本地 daemon 不可连接；后续已在 OVH 实际构建、隔离预检并上线验证（Linux amd64），见 [部署记录](ovh-deployment.md) |

原始 `.analysis/upgrade-*.log`、研究克隆及构建/冒烟中间产物已获用户批准清理；本节验收结论和 Trellis 记录保留。测试凭证和参考克隆从未提交 Git。
- `node --check internal/server/web/app.js`：语法校验。
- 36 组 server Mock 矩阵（两套餐 × 三客户端协议 × 三原生协议 × JSON/SSE），包含工具调用 ID；另有九组请求转换工具历史测试。
- 401/402/403/429/500、400/422 不误隔离、Retry-After HTTP 日期、SSE 首事件前重试/提交后不重放、请求取消、固定代理断开/显式恢复、未知额度、不污染账号权限、配置校验/热更新、checkpoint 重启与 Responses 绑定恢复、跨套餐协议重编码/粘性、批量导入去重、凭证不回显。
- 100 个并发租约的并发/RPM 一致性、重复 MarkSent/Release、释放后的 late trace、绑定忙不溢出、下游 Key 隔离、父子会话。
- 已有 Perch 两个日志目录清理竞态场景及 OpenCode 测试重复运行三轮通过；不是删除或降低断言。
- 真浏览器在独立本地临时数据目录验证：登录、Zen/Go 标签、Zen 独立 ordered 设置保存而 Go 保持 smart、重复三行导入得到两个账号、批量禁用 2/2、Go 列表为空、API 响应不含上游 Key；重启临时服务后又验证 Go 协议 Base JSON 覆盖保存、Zen 覆盖仍为空。只使用假 Key，接入开关保持关闭，没有联系真实推理或额度接口。
- 另用无凭证真实浏览器读取公开能力目录，验证 SDK/逐模型元数据和 family 路径声明；这不等于授权推理测试。原始 GitHub 文档三链接的 404 也已在浏览器重新确认。
- 后续用户授权的 OVH 部署已验证真实 Docker 构建、断网数据副本启动预检、原 data 挂载上线和公网资源更新；47 个账号、8 把下游 Key、2 把导入 Key 及既有设置保持，OpenCode 仍关闭。
- 无真实 Zen/Go 凭证，未实测其推理/usage；官方 raw 文档链接本次返回 404。未实测五种客户端、付费授权、持续高负载、生产代理或带载推理请求级验收；Linux arm64 仍仅交叉构建。

## 8. 修改文件地图

| 文件 | 作用 |
|---|---|
| `internal/config/config.go`, `opencode.go`, `opencode_test.go` | 两供应商、独立规则/设置/路由/回退组、旧配置兼容与校验测试 |
| `internal/opencode/discovery.go`, `usage.go` | 公共能力、可选端点文档、合并读取缓存、Go usage、未知状态；仅 HTTP 契约 |
| `internal/opencode/request.go`, `response.go`, `stream.go` | 独立编写的严格子集转换、用量语义、缓冲 SSE |
| `internal/opencode/*_test.go` | 协议、工具、图片、未知用量、流截断、发现/额度/redirect 测试 |
| `internal/quota/opencode.go`, `poller.go` | 复用后台 worker 和 Snapshot、固定出口、强制刷新、旧值保留、来源失效与退出等待 |
| `internal/pool/pool.go`, `opencode_test.go` | 保留调度锁与租约；目录过滤、严格续传、checkpoint、幂等性及并发测试 |
| `internal/rules/opencode.go` | 与 OpenDesign Go 无关的独立故障规则 |
| `internal/server/opencode.go`, `opencode_proxy.go` | 接入原 attempt/模型/诊断；代理独立健康和检测 API |
| `internal/server/proxy.go`, `identity.go`, `server.go` | 精确路由、错误/日志、Key scope、原生续传、回退、配置失效和请求排空 |
| `internal/server/admin.go`, `importapi.go` | 复用 CRUD/导入/设置；独立套餐与凭证只写策略 |
| `internal/server/opencode*_test.go` | 管理、重启、代理、回退、取消/停机及矩阵集成测试 |
| `internal/server/proxy_test.go`, `perch_test.go` | 关闭日志 writer 和等待 poller，修复基线测试清理竞态 |
| `internal/perch/delta.go`, `internal/server/perch_auto.go`, `perch_conversation_test.go` | 仅修正基线已有 gofmt 空白/对齐，无业务逻辑修改 |
| `internal/server/web/app.js`, `index.html` | 现有 UI 增量扩展，没有覆盖参考 UI |
| `.dockerignore` | 禁止研究仓库、缓存和临时数据进入构建上下文 |
| `docs/opencode-audit.md`, `docs/opencode.md`, `README.md` | 审计、边界、接入和升级文档 |
| `.trellis/tasks/opencode-upgrade/`, `.trellis/spec/`, `.trellis/workspace/` | 本轮计划、契约与进度，不属于待清理垃圾 |

## 9. 升级与回滚

1. **停服并备份完整 DATA_DIR**（含 config.json、metadata.json、selection-counters.json、logs、所有 Perch secrets/device/registration 数据），备份目录权限应为 0700。不要只备份 config。
2. 替换新构建或在确认是新版本源码的目录执行 `docker compose build && docker compose up -d`。OVH 旧工作树须按 [固定版本快照部署记录](ovh-deployment.md) 操作，不能直接在旧目录重建。本轮不新增依赖/外部数据库、不更改现有卷、端口、网络别名和 Compose 密钥环境变量。
3. 旧账号与下游 Key 继续沿旧路径运行。OpenCode 默认关闭；逐组启用、同步、验证、再接客户端。设置保存与 SIGHUP 都先校验；验证失败保留当前内存配置。
4. 快速逻辑回滚：`opencode.enabled=false`，关闭新调用/查询，原供应商不停用。正在处理的请求保留其已加载配置，不在中途改写。
5. 二进制降级：停止新版，恢复升级前的**完整数据备份**再启动旧版。旧二进制不认识新的 provider，带 OpenCode 账号/Key 的 config 不能直接交给旧版；旧版重写文件也会丢掉新增字段。若需要保留升级后的旧供应商增量，请先离线导出、明确迁移，只删除新 provider 的账号/Key/独立字段，不能盲目覆盖。
6. 新写入的请求日志/统计是否随全量回滚恢复取决于备份时间；“回滚到旧备份”不等于保留备份之后的计数。保留新数据副本以便审计。
7. 新任务及中间产物清单 `_tmp_artifacts.md` 需经用户同意再清理；不删除 Trellis 记录，不动原有分析/图片/生产数据。

## 10. 许可证与发布风险

没有引入 GPL 代码、参考资产或依赖，没有改主项目许可证（基线无 LICENSE 文件）。参考仅用于理解行为契约，独立实现，研究克隆被 Git/Docker 排除。公开发布前仍应由维护者确定主项目授权条款；如以后复制参考实现，需要重新评估 GPL-3.0 对通知、对应源码及分发方式的义务。
