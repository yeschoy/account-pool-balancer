# 独立 WebSearch 兼容网关（已部署隔离测试渠道）

## 状态与边界

2026-11-22：独立 Go 服务、受控路由、Tavily HTTP 后端、Responses JSON/SSE 工具循环已实现；按Codex0.167.0公开源码兼容显式联网选项、合法省略sources的历史回放、函数后文本流、域名限制与真实URL引用。用户明确授权后，**10:55 UTC Balancer与独立网关已部署OVH，11:00 UTC通过NewAPI隔离渠道260的真实搜索、owner续轮与计费验收**。现有业务渠道没有切换。真实Codex Desktop/CLI仍未验收，cached-only仍不支持，不能称为默认Codex零配置完整交付。镜像已在OVH构建，不再受本地Docker daemon不可用限制。

受控真实验证：OVH运行一次opt-in测试二进制，以一个最近成功、无代理的固定Mistral账号直接请求官方Responses，再调用用户指定Tavily Hikari。强制一次搜索并以low上下文限制到`docs.mistral.ai`，两轮流式约4.652秒完成、46事件、一个真实url_citation、累计usage存在；按Codex会省略sources的历史重发后续问成功。整次约5.04秒，3次固定账号模型调用、1次真实搜索，均HTTP200。密钥仅内存/SSH stdin/子进程env，未写入源码/配置/测试/日志；没有重放真实用户历史。这是**网关→真实搜索与固定上游**验证，不是号池多账号连续性或Codex/NewAPI验收。

第三阶段已于2026-11-22 09:19:01–09:19:08 UTC通过**网关→正在运行的Mistral号池→真实Mistral/Tavily**合成验收：使用已有“自测-test”Key、新隔离会话，3次模型调用均为单attempt200，本次实际同一账号且binding为miss→hit→hit；搜索两轮约4138ms、30事件、1真实引用。网关首轮累计input1887/output117，与号池两次368+1519 / 47+70逐项一致；历史续问input1606/output16、cached1536亦匹配。配置hash未变，没有新建/修改渠道、账号、分组或密钥。**测试进程已退出，并非网关常驻上线。**

固定账号保护已随`e7eb119`部署；线上能力接口返回owner v1/Responses/TTL300。NewAPI→独立网关→Mistral号池实际搜索两轮日志为`start→pinned`，同账号、单attempt200，网关首轮input1887/output108与NewAPI计费完全一致；Codex形状历史续问1605in/19out/cached1536亦匹配。此次搜索1次/1credit，搜索+回答10997ms、首次SSE事件403ms（不是正文首字）、续问763ms。忙/错误/限流等保护来自本地真实HTTP→原Pool矩阵，不通过在线制造账号拥塞来验证。

隔离渠道 **260 / WebSearch-Mistral-隔离验收**，仅私有`websearch-qa`组、模型`deepseek-v4.1-flash-1017`映射原生ID。普通用户分组/自动路由名单没有新增该组；管理员测试沿现有推理Key的固定渠道能力单次尝试，不改全局RetryTimes。除新增此渠道外，部署前所有渠道的配置投影一致，号池config逐字节未变。上线/回退细节见`docs/ovh-deployment.md`与任务`validation-phase4.md`。

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

网关只执行专用搜索函数。终端、文件和其他MCP函数仍返回客户端执行。第三阶段仅给原 Balancer 增加显式owner-token接线与原Pool中的限定账号准入分支，继续复用额度/健康/并发/RPM；不新增第二套账号池，不改变无owner-header的原有调度策略。多个路由可配置不同上游与模型允许名单，不限定 Mistral。

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

所有凭据至少16字节，启动时解析到内存；配置只保存环境变量名。修改配置/密钥后重启网关，无热更新或管理UI。示例路由的 `requirePoolOwner:true` 要求上游Balancer已升级owner v1；旧版会在任何推理前明确503，不会悄悄退回普通粘性。示例中的 `0.0.0.0:8791` 是容器监听；本机运行建议改为 `127.0.0.1:8791`。不填listen默认也只监听本机。

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

最小声明：`tools:[{"type":"web_search"}]`，也支持固定版本`web_search_2025_08_26`及对应tool_choice。首次执行后内部续轮改为auto，避免强制无限搜索。支持 `include:["web_search_call.action.sources"]`，来源只取真实结果URL。普通function声明、参数和大整数保留；内部保留名 `apb_web_search` 不能由客户端重复定义。

第二阶段支持：
- `external_web_access:true`（省略也允许联网）；`indexed_web_access:false`；只含text的`search_content_types`。
- `filters.allowed_domains`：最多100个纯DNS域名，映射为Tavily include_domains，并复核返回URL必须落在域名或其子域；越域结果整次明确失败，不把偏好伪装成硬限制。大小写归一，拒绝URL/路径/通配/IP。
- `search_context_size:low|medium|high`是上下文空间提示：low最多3结果/每来源4000字节，medium为配置结果上限/8000字节，high为配置结果上限/12000字节；仍受服务端硬上限约束，不承诺OpenAI内部token量等价。省略保持原12000字节上限。
- `user_location:null`或仅`{"type":"approximate"}`表示不指定位置；非空country/city/region/timezone仍明确拒绝，不以国家boost冒充完整地理定位。

**仍不是OpenAI hosted search的全量替代：**

- cached-only (`external_web_access:false`) 和indexed-only、image、preview、打开网页/页内查找及未知选项仍本地400，不能静默忽略。Codex默认cached会发送false；**因此尚不能承诺默认Codex零配置可用**。Tavily公开文档虽有max_age_hours=-1，当前自定义代理/上游未完成cache-only保证核验，不擅自将其改为实时联网。
- 仅支持完整input历史，拒绝 `previous_response_id` / conversation续传、store=true、background和客户端max_tool_calls；不存完整提示词。
- JSON与逐帧SSE都可执行多轮；上游JSON/终态单包中的完整文本会发对应文本事件。各轮统一公开response ID/事件序号/output_index；内部completed不会提前终止整个请求。
- 普通函数与搜索混用时，执行本服务搜索后将普通函数交还客户端，不在缺少客户端函数结果时继续生成。函数完整item.done后立即分类为公开搜索或普通function并解除顺序屏障，后续文本/思考继续逐帧输出。尚未完成函数前就出现后续item仍明确报 `unfinished_tool_before_output`；不静默重排或整轮缓冲。
- 从实际搜索来源及回答中**真正出现的精确URL**生成标准url_citation（Unicode字符偏移、exclusive end），JSON/逐帧annotation事件/最终output一致；来源不能只作为假URL前缀或嵌套URL的一部分。保留已有注解、不改正文、不为模型未引用的结果造引用。真实链路已观察到一个引用，但仍不保证模型每次引用或真实Codex原生引用UI已验收。
- 普通function的参数流在完整item确认后交还客户端；不把内部function名称/参数事件泄露为需要客户端执行的未知工具。

## 跨轮固定账号协议（已部署，隔离渠道实测）

- 网关路由 `requirePoolOwner:true` 仅用于compat的Responses搜索/历史请求。先以配置的号池下游凭据 `GET /v1/pool-capabilities`，要求 `requestOwner:v1 / protocol:responses` 且 `ttlSec` 覆盖总deadline；不产生模型/RPM/额度请求。旧版不支持时503 `pool_owner_unsupported`，**零推理调用**。该能力探针不是搜索/model健康检查。
- 首轮向号池发 `X-APB-Owner-Token:start`，仍由原调度器正常选号；返回一个AES-GCM加密令牌，不暴露账号ID/原始Key。后续内部模型轮次原样携带该令牌，不能更换/续期；仅整个外部请求内保存，不返回网关客户端、不入历史缓存/正文/Git/日志，也不转发官方上游。
- 令牌固定5分钟，隔离下游Key ID+hash、分组、模型与账号来源（provider/上游Key/Base/固定代理）。每轮重新校验当前配置，再通过**原Pool.mu下的资格/健康/并发/RPM与MarkSent/Release**准入；不是第二套调度，不预占整个搜索等待期的账号并发。
- 续轮账号忙时有限等待或429；失效/隔离/来源变更/过期时409，不能借号或重新选择。首轮/续轮上游失败都最多一次尝试，不换号重放。失败发生在已有SSE之后按response.failed收尾，不伪装改HTTP状态。
- 普通不携带该header的请求、其他号池、长期粘性策略保持原样。该协议目前只实现Mistral Responses；不声称其他号池已具备固定owner。
- 单请求内固定账号不是跨整个会话/下一次外部请求永久固定，也不是保证缓存命中。下一次外部请求重新start，仍优先沿原普通会话策略选号；缓存/公开历史本身保持其原有隔离合同。
- 号池摘要仅记录 `requestOwner:start|pinned`，令牌作为认证敏感header脱敏。相同持久化routingSecret下重启可校验尚未过期令牌，但不恢复lease/RPM；账号/Key/source依旧重验。不同secret不能接受该令牌。
- `requirePoolOwner:false`/省略仅适合直接固定上游或明确接受普通粘性的实验；不能称为多账号安全生产模式。示例对号池默认true，requestTimeoutSec超过300时能力预检会拒绝，不隐式放宽TTL。

## 历史、资源与安全

- 搜索原生function_call/result保存到有界的内存LRU，默认30分钟绝对TTL、1024条、32MiB；只存搜索call、结果与公开item，不存整段对话。公开搜索ID随机生成，按路由/路由凭据/模型/会话HMAC隔离；允许Codex合法省略action.sources与添加null元数据，也允许相同单query/queries表示；ID、status、真实动作、查询不一致、过期、重启或跨作用域时409，不能重新搜索伪造旧结果。
- 优先使用稳定 `X-Session-Id` / `Session-Id` / `X-Codex-Session-Id` / `Thread-Id` / `X-Pi-Session-Id`。缺少会话头时保守按首条原始input形成弱作用域：表示形式或首条输入变化可能409，不是强会话保证。建议接入前确认稳定会话头透传。多副本需额外会话亲和/共享受限历史；本版只建议单实例实验。
- 默认每请求最多3次搜索、最多4轮模型调用；16并发、总deadline180秒、入站/续轮body4MiB、单轮模型响应8MiB、单SSE事件1MiB。来源每条最多12000字节、搜索HTTP响应1MiB、默认5条结果。持续写入受服务端WriteTimeout约束。
- 带凭据请求不跟随重定向，不自动重试任何模型轮次，不向请求方指定的目标转发认证。模型请求和搜索请求的凭据各自独立。搜索查询命中已配置凭据时阻止发送；未知私密内容无法保证由模型自动识别，使用者仍须考虑关键词会交第三方处理。
- 搜索结果是“不可信网页数据”，不是服务端指令；不执行其命令、不自动抓取返回的任意链接。
- 首次失败未提交时返回安全HTTP错误；已提交SSE时发response.failed，错误在response.error内；无合法终态EOF不算成功，不补completed/DONE。客户端取消传播到模型与搜索。网关失败不会直接修改号池账号健康。
- 日志只有路由/模式/路径/耗时、调用计数、安全错误码和测量完整性，不含query/结果/原始会话/凭据。所有轮次用量齐全才返回累计input/output/total，缺测则省略整体usage，不把最后一轮冒充总数。cached/reasoning计数同样按完整覆盖返回。搜索credits单列日志并带完整性标志，不混为模型token。

## 正式接入前的门禁

1. **账号连续性上线已通过**：固定owner已部署，requirePoolOwner=true；NewAPI隔离渠道真实首轮/续轮为start→pinned、同账号/单attempt成功，本地忙/并发/RPM/取消/健康/源变更/TTL矩阵通过。不是跨所有外部会话永久固定或缓存命中率承诺。
2. **真实协议**：从新测试渠道核对真实Codex工具声明、双向流、普通工具/搜索混用、下一轮历史和真实来源展示；本地Mock不能替代。
3. **计费/重试隔离验收已通过**：NewAPI两条消费记录的累计input/output/cache与网关及池日志一致，计费沿现有公式；管理员固定渠道单次尝试，不修改全局重试。搜索credits独立记录（此次1），不自动纳入NewAPI金额；失败轮次可能已收费。将来对普通业务开放前仍需确认该路径不会整单重试。
4. **能力矩阵**：先验证一个Mistral模型，再验证第二个不同上游。Chat/Messages转换、更多搜索操作、分用户配额、管理UI、持久化/多副本历史均未交付。
5. **部署边界**：保留旧渠道地址与回退办法，独立部署并检验容器网络/健康；不替换现有Balancer网络别名，不重建已有号池以安装本服务。

## 本地验证

```sh
go test ./internal/websearch ./internal/searchgateway ./cmd/websearch-gateway
go test -race ./internal/websearch ./internal/searchgateway ./cmd/websearch-gateway
go vet ./internal/websearch ./internal/searchgateway ./cmd/websearch-gateway
go build -o .gotmp/websearch-gateway ./cmd/websearch-gateway
```

HTTP Mock覆盖真实HTTP两轮调用、第二上游、完整搜索历史还原/隔离/过期、混合函数、流式首字在上游完成前可见、单公开终态和全局序号、JSON回包合成文本事件、强制搜索与总输出预算、全部轮次usage合并/缺失或null未知、第二轮失败不冒充前轮用量为总计、搜索失败与超时/重定向/取消/并发/body上限、无搜索/native原字节透传、配置重复键与鉴权/模型零调用拒绝，以及第二阶段的联网选项/域名限制/跨模型历史/字符引用/函数后文本顺序。

`TestLiveResponsesSearch`默认跳过，必须显式提供`SEARCH_GATEWAY_LIVE_BASE`、`SEARCH_GATEWAY_LIVE_MODEL_KEY`、`SEARCH_GATEWAY_LIVE_SEARCH_KEY`和`SEARCH_GATEWAY_LIVE_SEARCH_ENDPOINT`；测试固定一次搜索和至多3个模型请求，不使用现有用户历史。不要在日志或shell参数中展开这些凭据。正式实测记录见任务`validation-phase2.md`。第三阶段使用显式`SEARCH_GATEWAY_LIVE_POOL=1`运行`TestLivePoolResponsesSearch`，只用指定自测下游Key，记录三个号池请求ID后与安全日志投影核对账号和累计usage；没有下载用户正文。该测试如需再验证owner，应额外设置`SEARCH_GATEWAY_LIVE_REQUIRE_OWNER=1`；本次部署则直接请求常驻网关的NewAPI测试渠道，真实验证了同一owner握手与用量，无需再重复消费运行测试二进制；能力预检GET不计入3次模型调用，也不被误算为搜索。第三阶段修改共享准入/代理接线，已执行一次全仓normal/race/vet与两个独立二进制build；没有重复消耗真实搜索以复验后续本地守卫。
