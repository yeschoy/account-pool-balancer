# Mistral号池端布局恢复契约（2026-11-11）

## 1. Scope / Trigger

用户明确禁止客户端配合，只允许改Mistral号池。仅精确`deepseek-v4-1-flash-1017`、`zai-glm-5-3`、`zai-glm-5-3-flash`的`/v1/chat/completions`，且已解析出原有会话identity；其他模型、Responses/FIM和其他池不启用。本地实现/验证不等于已上线或上游缓存已改善。

## 2. 数据流与接口

`原identity→prepareMistralRequest→mistralLayouts.restore→原attempt/relay→native响应capture→普通双scalar返回→下轮匹配恢复`。

- `mistralLayouts.scope`由独立domain-separated派生密钥HMAC下游Key ID/hash、分组、原identity fingerprint、模型和Mistral base。绝不替换真实路由HMAC/secret、绑定、cache key、X-Affinity、选号、TTL或重试策略。
- `mistralLayoutCapture.observe`只对当前事件内的原生内容做增量完整answer/thought HMAC和长度统计；不留文本缓冲，不等待整流完成才flush。非流响应完整转换成功后记录；SSE观察原生finish_reason或DONE时提交，必须在终止帧对客户端可见之前入状态，避免下一轮竞态。
- `restore`在prepare兼容后，仅对原请求中无原生thinking的assistant历史做精确完整两字符串匹配；重建content，不动其他message/root字段、工具/参数/大数、图片、cache或identity。原生thinking历史为客户端的明确权威布局，不猜/不重排。

## 3. 能保证和不能保证

- 已捕获、同scope、完整文字及空白完全匹配、记录有效且无已知歧义时，恢复text/thinking逻辑交错顺序、分段长度、closed及空signature元数据；所有UTF-8切片必须完整消费两个字符串。不得trim或模糊匹配，不能从记录恢复被客户端删除的思考。
- JSON保存观察到的原生块/嵌套text分段。SSE将相邻同类型跨帧续片合为逻辑块（closed=true是响应片段元数据，不阻止后续连续思考续片；3051证据要求请求至多一个leading ThinkChunk），同帧显式分段保留；这是逻辑内容重建；render还必须把旧checkpoint相邻thinking连续文本合并，不能把流帧当多个request块。此规则只在相邻思考内合并，不跨真实正文、不重排。**不恢复原网络分帧、原JSON属性次序/转义/排版，也无法知道供应商未公开的内部块边界**。不把该功能说成字节级原样重放或token前缀/缓存保证。
- 旧会话、外部历史、客户端改字/删思考、无可靠identity、未捕获/被LRU回收的布局无法逆推出。保留既有兼容并显式`legacyUnknown`，不冒称这些记录已恢复。上游自身只有两个scalar也记unknown；不得自行断言其原来一定thinking在前。没有客户端响应标识，scope+完整文字匹配也不是不可伪造的原响应归属证明：未观察到的同文不同布局无法检测，不能宣称覆盖任意导入历史或绝对无歧义。
- 用户确认（2026-11-20）：匹配记录歧义、过期或无法完整重建时，仅对该条历史保留 `prepareMistralRequest` 的既有兼容字节（扁平思考先于正文），不再因布局重建失败本地400，也不新增重放。其他可精确恢复的条目仍逐条恢复；真正的参数/协议错误仍按原策略拒绝。缺失记录且有扁平思考、或已知unavailable，沿既有兼容并计兜底（保留既有unknown/unrecovered计数）；无思考的未知项和不支持的原生内容保留原路径。原生thinking历史为权威，不套用兜底。
- 同scope同文不同布局仍保留ambiguous标记，不能取后到布局冒充原顺序。兜底只计本次请求，不写入或续期旧布局，不改变原文字/工具/缓存键。后续仅由真实原生响应的现有捕获路径生成新布局；无新响应不造记录。新完整capture仍可替换unavailable。兜底可能改变旧历史的模型缓存前缀，不保证上游成功或命中率。
- 真实非空引用/未知内容不得因重建被删除；非plain内容沿原验证并记unknown。只支持原有可表达的plain text/thinking与固定元数据，供应商真实非空签名仍沿原兼容错误，不伪造。

## 4. 状态、持久化与隐私

- mutex保护单LRU最多8192记录；序列化估算加固定开销最多16MiB；单记录32KiB、512块/分段、4choices/响应，单响应完整文本计数≤512MiB；不是整个进程RSS上限。长纯文本SSE续片不能增长为每token一条描述。
- 保存类型、字节数、分段长度、固定closed/signature枚举、私有key/tag、版本、过期时间；**不保存输入/输出/思考/工具原文、原始会话、凭据或客户端carrier**。作用域摘要与文本摘要都不进业务日志/技术头。并发同键同布局幂等，不同布局标为歧义，不作为原生布局重放。
- 可恢复期限24h，仅在精确重建完成时续期，与账号绑定TTL无关；续期不覆盖并发捕获的新状态。这是使用期限而非原文/元数据销毁承诺；到期记录仍可作为不可用标记留在有限LRU中。有限LRU的缺失状态只说明unknown，不能许诺永久保序或已知所有旧布局；已知过期记录走兼容兜底，不能冒充精确恢复。
- `metadata.mistralLayouts`是可选派生checkpoint，有版本/完整记录HMAC、严格枚举/尺寸/数量校验、深拷贝。复用现有一分钟合并保存、正常退出强刷；写失败保留metaDirty重试，崩溃可能失去未刷记录并成为unknown，不承诺零丢失。篡改/异密钥/重复/超量记录拒绝加载，不输出私有值。
- 旧版metadata严格reader可能拒绝新增字段；上线必须考虑断网启动兼容预检和完整备份。降级先备份当前data，仅移除派生mistralLayouts字段，不恢复旧data覆盖上线后业务写入，也不清理其他绑定。

## 5. 可见性

- 适配请求保留`X-Mistral-Layout`已有兼容计数头；不新增兜底明细、内部错误文本或错误码到对外响应。功能不依赖中间层转发。
- 管理员 `requestLog.mistralLayout.fallback` 只记录按默认顺序处理的历史条数，与 `restored` 分开；`rejected` 只识别旧日志。沿现有admin/no-store权限，不新增正文或公开API。未启用路径/其他池不产生布局字段。

## 6. 必要验证

- `mistral_layout_test.go`：三模型×JSON/SSE真实本地HTTP→原客户端两个scalar→下一轮HTTP，无carrier；text→thinking→text、空白/UTF-8、nested parts/closed/null signature、tools/顺序/图片外历史/大数保持；增长历史中的旧恢复字节稳定、X-Affinity与原HMAC稳定。
- `mistral_layout_3051_test.go`：真实HTTP上游按3051验证至多一个leading ThinkChunk；600帧closed=true只保留一个逻辑块，旧168片段checkpoint不清空即可恢复/新capture等价，无思考文本丢失。
- `mistral_layout_state_test.go`：Key凭据/ID/group/session/model/协议隔离，未识别identity无全局scope；正常Server重建经真实HTTP恢复、checkpoint不含原文/身份、签名/篡改拒绝、深拷贝；TTL/续期、LRU/字节/节点超限、真实 HTTP 中同文不同布局/过期/无效描述/超限/缺失均走既有兜底并单次正常上传，不冒充原始记录；后续真实返回可精确重建；native权威和旧历史unknown不变；流首帧及时flush、失败/未完成流无可用checkpoint、万帧续片有界、并发capture/get/export race。
- 保留Mistral422空注解、legacy/native去重、图片/推理档位、SSE错误/usage/flush、原HMAC/绑定/隐私与metadata回归。不为此追加生产推理或重放用户私密历史。

## 7. 还原实现的字节与配对约束（2026-11-24）

- **位置配对不变**：`restore` 以原请求与 prepare 结果的 `messages` 按索引一一配对；prepare 不增删/重排消息，因此不再把原历史整体解码为 `[]json.RawMessage`，改为 `skipValue` 偏移并行遍历。任何未来在 prepare 中增删消息的改动必须同时改这里，否则配对语义失效。
- **只就地拼 content**：命中后只把重建的 content 值拼回该消息，再与整份正文一次性写入；不得退回「逐消息 render + 根对象 render」的双重复制。命中记录前后字节保持原样，工具/大数/顺序/图片/identity/cache 不变。
- **重复键**：根 `messages` 出现多次时不选其中一处改写，直接返回 prepare 结果（不可判定且极罕见）；单条消息的重复 `content` 仍按原有 `unique` 报错语义处理，不能静默取一个。
- **字节等价**：`mistralLayoutRecord.render` 手写拼接必须与旧 `encoding/json` 数组编码逐字节一致（文本仍走 `json.Marshal` 转义），由 `mistral_wire_identity_test.go::TestMistralLayoutRenderWireBytesUnchanged` 固定；只有确实需要压缩空白时才可用编码器回退，不能顺带改变 key 顺序或转义。
- **解析等价**：`mistralLayoutPlain`/`mistralHasNativeThinking` 改为 wire view 后必须与旧 map 解析等价：重复键取最后、未知键拒绝、`type/text/thinking/closed/signature` 与 null/空 signature 规则不变，非对象/非数组输入仍判不支持并走既有 unknown 兜底。
- **分配边界**：还原缓冲按 `len(prepared)+len/8+1024` 预留；不允许出现「溢出一个字节就翻倍整份正文」的路径。新增解析不得引入每消息 map/数组 copy；`BenchmarkMistralPath` 与 `BenchmarkMistralGatewayStream` 保留同轮前后对比。
