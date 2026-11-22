# 独立 WebSearch 兼容网关（第一阶段，本地原型）

## 状态与边界

2026-11-22：独立 Go 服务、受控路由、Tavily HTTP 后端、Responses JSON/SSE 工具循环与本地 Mock 验证已实现。**没有部署生产、没有修改 NewAPI 渠道、没有以真实 Codex Desktop/真实模型完成端到端验收。** 本地 Docker daemon 查询12秒未响应，因此没有声称容器镜像已构建/运行。

Tavily Hikari 的实际 `POST /api/tavily/search` 此前已用用户授权的凭据做一次公共文档查询，HTTP200、约2.7秒、有标题/URL/content；这不等于下面兼容链路已真实验收。用户凭据未写入源码、配置示例或测试。

## 架构

```text
Codex / 其他 Responses 客户端
                ↓
             NewAPI（可选）
                ↓
     /r/<route>/v1/responses
        WebSearch 兼容网关
          ├── 模型 → 指定号池/模型上游 /v1/responses
          ├── 搜索 → 配置的 Tavily HTTP endpoint
          └── 回填真实搜索结果，继续模型生成
```

网关只执行专用搜索函数。终端、文件和其他MCP函数仍返回客户端执行。原 Balancer 的账号调度/额度/RPM/健康代码没有修改；不新增第二套账号池。多个路由可配置不同上游与模型允许名单，不限定 Mistral。

## 文件

- `cmd/websearch-gateway/`：独立进程、配置校验、healthcheck、信号关闭。
- `internal/searchgateway/`：鉴权/路由、工具转换、执行循环、JSON/SSE、历史缓存与安全日志。
- `internal/websearch/`：独立 typed 搜索后端接口与 Tavily HTTP 客户端。
- `docs/websearch-gateway.example.json`：只含环境变量名称，不含密钥。
- `Dockerfile.websearch-gateway` / `docker-compose.websearch-gateway.yml`：独立容器示例，不替换线上 Balancer Compose。

## 配置和运行

先将示例复制到受控运行目录，修改监听地址、route ID、上游Base、模式、允许的原生模型ID。**不要将真实密钥粘贴进Git中的文件或命令行参数。** 由本地安全环境/密钥管理系统提供：

- `SEARCH_GATEWAY_TAVILY_KEY`：搜索后端凭据。
- `SEARCH_GATEWAY_MISTRAL_TOKEN`：NewAPI/客户端访问这个网关路由的独立凭据。
- `SEARCH_GATEWAY_MISTRAL_UPSTREAM_KEY`：网关访问上游号池的下游Key；不是搜索Key，也不必是官方账号Key。

所有凭据至少16字节，启动时解析到内存；配置只保存环境变量名。修改配置/密钥后重启网关，无热更新或管理UI。示例中的 `0.0.0.0:8791` 是容器监听；本机运行建议改为 `127.0.0.1:8791`。不填listen默认也只监听本机。

```sh
go build -o /受控运行目录/websearch-gateway ./cmd/websearch-gateway
/受控运行目录/websearch-gateway -config /受控运行目录/gateway.json -check
/受控运行目录/websearch-gateway -config /受控运行目录/gateway.json
```

`-check` 不发送网络请求，不打印凭据。`GET /healthz` 只表示该进程可用，不代表搜索额度/模型授权可用。

容器示例使用独立 `websearch-gateway` 服务、1000:1000、只读rootfs、丢弃capabilities、256MiB内存限制、healthcheck、仅宿主回环端口和现有 `ai-internal` 网络。其发布仍须另行授权，不要把该示例直接当成已经验证的生产配置。

### 调用入口

- SDK/Codex 的Base URL：`http://网关地址:8791/r/mistral/v1`。
- 完整请求URL：`POST http://网关地址:8791/r/mistral/v1/responses`。
- 网关入口鉴权：`Authorization: Bearer <网关路由凭据>`。
- NewAPI 若自动追加 `/v1/...`，渠道Base URL应为 `http://websearch-gateway:8791/r/mistral`，**不要盲目再拼一个/v1**；正式接入时必须实际核对最终路径。

`GET /r/<route>/v1/models` 可透传目录；`POST /r/<route>/v1/chat/completions` 仅原生透传。目录可见不等于推理/搜索授权，POST还要通过该路由的模型名单。

## 路由模式

| mode | 行为 |
|---|---|
| native | 不适配搜索，保留请求/响应正文原字节转发给原生支持的上游 |
| compat | 仅在 Responses 有hosted搜索声明或网关搜索历史时启用兼容；否则原字节透传 |
| disabled | 发现hosted搜索或搜索历史时明确400，不静默删工具 |

compat必须显式列出 `allowedModels`。调用方不能指定上游URL、路由凭据来源、模型密钥或搜索endpoint。未知路径/查询参数拒绝；认证、模型、参数校验失败不调用模型或搜索后端。native也限制认证、路径、模型和资源，不是任意反向代理。

## 支持的搜索合同

最小声明：`tools:[{"type":"web_search"}]`。支持对应的 `tool_choice:{"type":"web_search"}`，首次执行后内部续轮改为auto，避免强制无限搜索。支持 `include:["web_search_call.action.sources"]`，来源只取真实结果URL。普通function声明、参数和大整数保留；内部保留名 `apb_web_search` 不能由客户端重复定义。

**第一阶段不是OpenAI hosted search的全量替代：**

- 不支持 `web_search_preview`、打开网页/页内查找、域名过滤、地理位置、搜索上下文大小、cached/live等额外工具选项；无法保持的意图本地400，不能静默忽略。尤其不能把cached-only请求当成live搜索。真实Codex默认声明可能带这些字段，须在生产前检查并补适配或明确配置支持范围。
- 仅支持完整input历史，拒绝 `previous_response_id` / conversation续传、store=true、background和客户端max_tool_calls；不存完整提示词。
- JSON与逐帧SSE都可执行多轮；上游JSON/终态单包中的完整文本会发对应文本事件。各轮统一公开response ID/事件序号/output_index；内部completed不会提前终止整个请求。
- 普通函数与搜索混用时，执行本服务搜索后将普通函数交还客户端，不在缺少客户端函数结果时继续生成。JSON及工具流支持保持原顺序；SSE在尚未完成的函数之后又流出新的文本/思考item，当前明确报 `unsupported_interleaved_tool_stream`，不静默重排或丢失内容。
- 返回真实搜索项与来源，模型被要求引用真实链接；**不保证模型一定引用，也未实现网关自动生成标准url_citation注解/原生搜索引用UI**。普通上游已有注解原样保留。
- 普通function的参数流在完整item确认后交还客户端；不把内部function名称/参数事件泄露为需要客户端执行的未知工具。

## 历史、资源与安全

- 搜索原生function_call/result保存到有界的内存LRU，默认30分钟绝对TTL、1024条、32MiB；只存搜索call、结果与公开item，不存整段对话。公开搜索ID随机生成，按路由/路由凭据/会话HMAC隔离；客户端历史被修改、过期、重启或跨作用域时409，不能重新搜索伪造旧结果。
- 优先使用稳定 `X-Session-Id` / `Session-Id` / `X-Codex-Session-Id` / `Thread-Id` / `X-Pi-Session-Id`。缺少会话头时保守按首条原始input形成弱作用域：表示形式或首条输入变化可能409，不是强会话保证。建议接入前确认稳定会话头透传。多副本需额外会话亲和/共享受限历史；本版只建议单实例实验。
- 默认每请求最多3次搜索、最多4轮模型调用；16并发、总deadline180秒、入站/续轮body4MiB、单轮模型响应8MiB、单SSE事件1MiB。来源每条最多12000字节、搜索HTTP响应1MiB、默认5条结果。持续写入受服务端WriteTimeout约束。
- 带凭据请求不跟随重定向，不自动重试任何模型轮次，不向请求方指定的目标转发认证。模型请求和搜索请求的凭据各自独立。搜索查询命中已配置凭据时阻止发送；未知私密内容无法保证由模型自动识别，使用者仍须考虑关键词会交第三方处理。
- 搜索结果是“不可信网页数据”，不是服务端指令；不执行其命令、不自动抓取返回的任意链接。
- 首次失败未提交时返回安全HTTP错误；已提交SSE时发response.failed，错误在response.error内；无合法终态EOF不算成功，不补completed/DONE。客户端取消传播到模型与搜索。网关失败不会直接修改号池账号健康。
- 日志只有路由/模式/路径/耗时、调用计数、安全错误码和测量完整性，不含query/结果/原始会话/凭据。所有轮次用量齐全才返回累计input/output/total，缺测则省略整体usage，不把最后一轮冒充总数。cached/reasoning计数同样按完整覆盖返回。搜索credits单列日志并带完整性标志，不混为模型token。

## 正式接入前的门禁

1. **账号连续性**：多轮模型调用各自进入原号池调度器，普通粘性不是跨轮原子租约；本版未实现受信任的固定账号协议。必须验证并按需补请求级owner/租约接口，再扩大到生产，不能承诺固定账号/缓存命中。
2. **真实协议**：从新测试渠道核对真实Codex工具声明、双向流、普通工具/搜索混用、下一轮历史和真实来源展示；本地Mock不能替代。
3. **计费/重试**：NewAPI需核对多轮累计usage与自身记账；测试渠道先关闭自动重试，防止网关前的组件将失败整单重放。搜索credits不自动纳入NewAPI金额计费；失败轮次上游可能已经收费。
4. **能力矩阵**：先验证一个Mistral模型，再验证第二个不同上游。Chat/Messages转换、更多搜索操作、分用户配额、管理UI、持久化/多副本历史均未交付。
5. **部署边界**：保留旧渠道地址与回退办法，独立部署并检验容器网络/健康；不替换现有Balancer网络别名，不重建已有号池以安装本服务。

## 本地验证

```sh
go test ./internal/websearch ./internal/searchgateway ./cmd/websearch-gateway
go test -race ./internal/websearch ./internal/searchgateway ./cmd/websearch-gateway
go vet ./internal/websearch ./internal/searchgateway ./cmd/websearch-gateway
go build -o .gotmp/websearch-gateway ./cmd/websearch-gateway
```

HTTP Mock覆盖真实HTTP两轮调用、第二上游、完整搜索历史还原/隔离/过期、混合函数、流式首字在上游完成前可见、单公开终态和全局序号、JSON回包合成文本事件、强制搜索与总输出预算、全部轮次usage合并/缺失未知、搜索失败与超时/重定向/取消/并发/body上限、无搜索/native原字节透传、配置重复键与鉴权/模型零调用拒绝。未跑与本任务无关的号池回归或发送真实生产推理。
