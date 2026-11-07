# Mistral 缓存、协议、Key复制与 OVH 错误核对（2026-11-07）

Mistral状态：只读研究完成，缓存/Key复制与协议兼容仍待实施范围确认；未修改相关业务代码、线上配置、渠道或凭据，未重启、部署或发起真实推理。用户明确要求只做必要定向验证，避免全量与重复测试。后续单独要求的TokenHarbor日志开关按小改动直接完成，见下节。

## 基线与隔离

- 当前工作树 master 2217151 有其他窗口 TokenHarbor/TLS 未提交修改，原样保留。
- origin/master=f99fa52，Mistral功能提交8d852b6；对应现有干净工作树 `/Users/lyh_god/orca/workspaces/account-pool-balancer/mistral-account`。只读源代码核对在该树完成，不在旧master上拼接功能或强制同步分叉。
- OVH account-pool-balancer 实际 revision=8d852b65ecaaf91bce17f197065931edbf77073e，Mistral baseUrl=https://api.mistral.ai/v1。

## 官方合同（真实浏览器核对）

1. https://docs.mistral.ai/inference/conversations/advanced/prompt-caching
   - prompt_cache_key用于路由共享前缀的请求，官方建议稳定conversation/session/workflow ID，不放凭据或敏感数据。
   - 不保证缓存命中；前缀必须相同。64-token缓存块，少于64 prompt tokens不命中；cached input按标准输入价10%计费。
2. https://docs.mistral.ai/api/endpoint/chat
   - POST /v1/chat/completions请求明确支持prompt_cache_key:string|null。
   - 缓存用量：usage.prompt_tokens_details.cached_tokens。
3. https://docs.mistral.ai/api/endpoint/responses
   - POST /v1/responses请求明确支持prompt_cache_key:string|null，response示例包含usage.input_tokens_details.cached_tokens。
   - previous_response_id必须省略/null；store必须省略/false/null。
4. https://docs.mistral.ai/inference/conversations/responses
   - Responses为无状态public preview，每次input携带完整历史及先前output。
5. https://docs.mistral.ai/inference/conversations/reasoning
   - Mistral原生思考内容是content中的ThinkChunk；文档要求多轮重放完整assistant消息，不应删除思考轨迹冒充兼容。

## 缓存实现建议（未实施）

- 只在Mistral Chat/Responses生成端点、客户端没有有效prompt_cache_key且现有身份有Fingerprint时补充`apb-mistral-<会话HMAC>`。
- 复用proxy.go中identityScope=group+下游Key ID的现有HMAC，保留选号及分组/下游调用者隔离；不发送原始会话ID，不用每次变化的requestId。
- 客户端已有合法key原样保留；没有稳定身份不随机补key；明确fallback消息HMAC只是弱身份。
- 只增加缓存字段，避免重编码整份原生消息/工具；不影响其他provider、不改现有缓存/粘性优先级、不把命中承诺成保证。
- 现有proxy.go仅识别prompt_cache_key来选号，不会从会话头自动注入它；docs/mistral.md当前说明原生正文不改，实施时需同步更新合同与定向测试。

## Key复制边界（未实施）

- Mistral目前同时被config.WriteOnlyCredentials和前端managedKeyPool禁用了`GET /api/accounts/{id}/keys`及编辑框复制按钮。
- 现有下游Key/导入Key列表已有复制；本次建议补Mistral账号上游API Key显式复制。
- 应只在管理员显式点击时允许单账号读取已保存runtimeKey，响应no-store；普通列表保持掩码、不持久化到浏览器、不放宽OpenCode/TokenHarbor等池边界。
- 不要直接删除WriteOnlyCredentials中Mistral分支：该helper还被导入分组隔离/去重保护复用。复制权限与这些隔离语义应分开。

## OVH错误证据（远端内存投影，无凭据/正文下载）

- 2026-11-07日志中Mistral错误共96条，范围13:02:00.276–13:10:57.275 UTC，全部上游HTTP422，path=/v1/chat/completions，model=deepseek-v4-1-flash-1017。
- 同次读取113条Mistral请求：17条HTTP200、96条422。六个Mistral账号最近成功的64模型目录均包含该DeepSeek ID，不能仅因名字不是Mistral就认定模型错误，也不据此声称推理授权普遍有效。
- 35条可解析的完整错误detail均为`extra_forbidden`，位置`body.messages.<index>.assistant.reasoning_content`，提示`Extra inputs are not permitted`。证据指向客户端/中间层把DeepSeek式reasoning_content消息历史直接传给Mistral Chat schema。
- 其余61条：empty error body=21；non-JSON 46 bytes=14；non-JSON约16410–16413 bytes=26。仅凭现有摘要不能确认这61条完全同因。
- `proxy.go:908–912`先调用thBodyPreview(...,16KiB)，再errorReason判断json.Valid；`tokenharbor_privacy.go:153+`诊断头尾裁剪可使原JSON不再有效，也可返回隐私省略占位文本。因此日志的non-JSON不能直接等同上游返回非JSON；不是另一个已确认的协议故障。
- 不修改凭据、轮换代理、重放确定性422或自动删除reasoning_content。修复请求格式应由客户端保留原生ThinkChunk，或另行授权明确的无损兼容映射；先不扩展第二套协议转换。
- 可考虑将验证错误日志投影为type/loc/msg（去除回显input），先解析再限长，既保留错误位置也减少提示词/思考内容泄露。

## 后续请求：TokenHarbor独立详情开关与NewAPI选项（本地完成/说明）

- 新增持久化`tokenHarbor.detailLog`，新旧配置默认false；GET/PUT推理设置显式投影、缺省保留；注册机设置与其他池保存不改变该值。
- 开关关闭时不缓冲TokenHarbor响应详情、不记录成功/失败详情，继续普通请求/错误摘要，不删除旧记录。开启才记录成功和各失败尝试，保持64KiB/脱敏/去重；其他池共享开关行为保持原样。
- 前端TokenHarbor路由独立设置增加默认未选中的复选框；该池隐藏通用详情开关，保存时仍保留共享值。
- 定向Go配置/服务测试、已有OpenDesign/Go详情兼容及隐私测试、JS语法和diff检查通过；仅一次本地build供真实浏览器验证。没有全仓normal/race/vet或真实上游推理。
- 独立本地服务127.0.0.1:18793、零账号、OpenCode关闭；浏览器实际确认默认off→点击on保存→API/磁盘true→点击off保存→APIfalse，global logs.detail一直false。测试管理员密码纯本地虚构。未部署OVH。
- NewAPI截图选项是`thinking_to_content`。本地NewAPI源码3914bf702的`relay/channel/openai/relay-openai.go:23–100`在返回SSE中把reasoning_content包装为content的<think>文本，清除返回delta字段；不是发送请求历史的归一化。UI文案见`channel-mutate-drawer.tsx:1907`。当地源码并非OVH镜像ad556799的精确版本（本地无该git对象），不冒充已核对线上该版每条路径；官方功能说明也描述的是响应转换。
- 因而开启截图选项不能直接修复已有请求中的`messages[*].reasoning_content`，也不是Mistral原生ThinkChunk映射。可能间接影响未来客户端保存的消息，但取决于客户端/路径，不保证。
- 推荐后续在客户端Mistral原生适配或Mistral专属明确兼容边界中，将外来reasoning_content保留为原生thinking块，再移除非法顶层字段，保留回答/工具调用。尚未实现/开启该转换，也未修改NewAPI渠道。
- 此前本次TokenHarbor代码/文档及Trellis规范尚待用户确认提交，未混入其他窗口TLS/注册改动。随后用户明确授权先提交master当前业务改动再合入本地OpenDesign分支；本次作为现有改动检查点一并保存，未推送/部署，不将保存现有TLS源码等同于其真实上游效果已验证。

## 最小验证计划（Mistral待确认）

只增加/运行新增行为对应的Mistral Mock定向用例（Chat/Responses同会话稳定、已有key保留、跨下游隔离、其他池不变）、管理员复制访问边界及前端复制载荷测试、JS语法检查；不全仓race/vet、不批量真实上游试错。上线需另获用户授权，复用既有OVH流程。
