# Mistral 缓存、协议、Key复制与 OVH 错误核对（2026-11-07）

## 本地 master 合入记录

- 用户随后明确授权“合入master”；仅本地提交/正常合并，不推送、不部署、不新建Trellis任务。
- 功能分支提交`13971f8`（缓存key、原生reasoning兼容、管理员完整Key/代理展示复制、TH详情开关及规范/测试）；master原有验证记录更新提交`c256df2`，此前master为`e2fc393`，已含OpenDesign诊断与现有TokenHarbor/TLS/注册功能。
- 使用正常no-ff合并保留双方历史，逐处解决8个文件的内容冲突；保留OpenDesign诊断、TokenHarbor注册机单Key接口、既有分组隔离批量复制和Agent等效额度展示。管理员账号API/界面按最新用户授权可读取完整Key/控制Key/代理，注册机API仍只返回已保存API Key，不泄露密码/Cookie/邮箱/支付链接；同步旧测试/规范，未删除有效的鉴权或隐私断言。
- 合并工作树定向Go配置/服务测试通过（0.406/2.603秒），覆盖Mistral、管理员凭据鉴权/禁缓存、TH详情与原保存Key功能、OpenDesign刷新及原Go/OpenDesign详情。Mistral、TH保存Key复制、TH额度展示、TH注册行为、OpenDesign诊断五组Node测试、JS语法和diff检查全部通过。没有全仓/race/vet、真实推理或重复浏览器验收。
- 合并提交见Git日志；保留既有TLS/注册与OpenDesign提交，不reset、不覆盖分叉，不清理任何已登记临时产物。下文“尚未提交”等是合入前阶段的历史记录，以本节为准。

状态更新：用户先要求列清单并停止、禁止创建Trellis任务，随后明确“开始吧”。已在干净Mistral工作树 `/Users/lyh_god/orca/workspaces/account-pool-balancer/mistral-account` 实现管理员完整Key/代理展示复制、Mistral Chat reasoning_content→原生thinking兼容、Chat/Responses缺失缓存key的会话HMAC补齐，并整合本地TokenHarbor独立详情日志开关。主目录既有TLS/注册等并行改动未迁入。未创建Trellis任务、未提交/推送/部署，未改线上配置或NewAPI开关、未发真实推理。用户明确要求只做必要定向验证，避免全量与重复测试。

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

## 本轮实现与验证结果

- Mistral原生生成请求在账号/RPM获取前预处理；全历史assistant的reasoning_content字符串映射为thinking块，保留回答/已有内容块/工具调用，大数与根字段model/tools保持原始字节；null/空思考仅移除空字段，不可表示类型本地400。
- 仅当Chat/Responses生成请求根缓存字段缺失且已有身份时补`apb-mistral-<现有HMAC>`。已有字段（含null/空串）保留，无身份不随机补；生成值加入日志身份脱敏。Responses输入/续传规则与其他池/端点不改。
- 各号池的已登录管理员账号页/编辑框显示完整runtimeKey/controlKey/proxyUrl并精确复制，取消代理密码框；沿用已有明文config，不新增加密、不迁移注册密码/Cookie/会话。导入隔离与日志脱敏保留，匿名/runtime Key不可读管理员凭据，响应no-store，退出清空账号和弹窗DOM。
- 定向Go配置/服务用例通过（Mistral全组、管理员凭据鉴权/禁缓存、TH详情/设置、既有OpenDesign/Go详情；服务用时2.284秒）。Mistral及TH Node行为、JS语法、diff检查通过；没有全仓/race/vet。一份本地build供假账号浏览器验证。
- 本地127.0.0.1:18794、2个禁用假账号、上游均127.0.0.1:1且OpenCode/轮询关闭。实际验证完整展示/编辑值/复制按钮、TH开关off→on→off的API/磁盘保存、共享开关和账号凭据不变，退出后账号/弹窗清空、管理员读取401；复制载荷由Node断言，未宣称系统剪贴板回读。专用浏览器已关闭，服务发送SIGTERM，文件按清单待用户同意清理。
- 实现目录的`.trellis/spec/backend/mistral.md`、相关规范及docs同步。用户未授权Git提交/部署，故本次修改含规范仍未提交；后续仅提交该树本次改动，不混主目录其他窗口改动。
- 此修复针对已证实的reasoning_content形状，不等于已实测生产422全部消失或缓存命中；旧排查还记录过根字段thinking/Chat store被拒绝，未擅自删除其他字段。保留旧研究下的原建议作为历史。

## 原最小验证计划（现已按定向范围完成）

只增加/运行新增行为对应的Mistral Mock定向用例（Chat/Responses同会话稳定、已有key保留、跨下游隔离、其他池不变）、管理员复制访问边界及前端复制载荷测试、JS语法检查；不全仓race/vet、不批量真实上游试错。上线需另获用户授权，复用既有OVH流程。
