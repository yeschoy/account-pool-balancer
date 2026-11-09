# Mistral 缓存链路临时诊断

这是**取证能力，不是缓存率修复**。不删历史、不改工具顺序、选号、缓存键、重试、连接池或用量统计；不会主动发送推理请求。默认关闭，仅采集一个明确指定的 Mistral 下游 Key 的自然请求。

## 启用与停止

可选启动参数 `-mistral-cache-audit-key-id`，或环境变量 `ODB_MISTRAL_CACHE_AUDIT_KEY_ID`，值为**已存在且启用的下游 Key ID**，不是 Key 名、Bearer 密钥、上游账号 ID。空值关闭；无效/非 Mistral ID 会拒绝启动，不回显传入值。

参数和环境变量均未写入 config.json，没有新增持久化设置或自动启用逻辑。线上使用须另获授权并遵守 [OVH 部署约定](ovh-deployment.md)：保留 Compose、网络别名、healthcheck、资源限制和回退保护，不用裸 docker run 替换服务。本功能交付不代表已经部署或启用。

- 每进程从启动起 **30 分钟**、最多 **128 个有效准备后的目标请求**，先到者停止新采样；本地拒绝/未获取账号的记录可能没有 attempt，不伪造发送。
- 已采样请求可记录其后续实际尝试；每请求最多 8 个诊断 attempt，每 attempt 最多 4 个正文读取实例和 4 个连接事件，超限显式标为截断/未知，不改变原请求的执行次数。
- TTL 到期后由既有 15 秒维护节拍释放比较基线与连接引用；在途回调可能短暂重建有界记录。无额外采样线程/轮询链。
- 移除启动参数/环境变量并正常重启可停用；到期/预算用完也停止新采样。不自动重启续采，不删除既有日志。降级前移除新增参数，日志无需迁移。

## 查看

管理员进入 **Mistral → 请求日志 → 缓存诊断**。即使正文详情开关关闭，也能查看可选 `cacheAudit` 摘要。认证/no-store/保留与既有请求日志一致；运行时 Key 不能读取管理日志。旧日志、非目标 Key、其他号池和排除的路径没有此字段，不从截断正文补算。

只支持精确 `/v1/chat/completions` 和 `/v1/responses`。Chat 比较 messages；Responses 比较数组 input，原生字符串 input 仍有完整正文指纹，但不伪造逐项历史比较。

## 观测边界

1. `client`：完整原始入站正文，别名处理前。
2. `prepared`：Mistral 参数/历史/缓存兼容完成后，获取账号前。
3. `attempts[].expected`：该实际 HTTP attempt 的最终正文；`preparation` 对照 prepared。
4. `attempts[].bodyReads`：HTTP 客户端从原始或 GetBody 重建正文实际读取的字节数与增量 HMAC。`complete` 仅表示读取数量等于预期；必须同时看 `matchesExpected`、`writeSuccesses/writeFailures`、`sent` 与真实响应。**读取完整不等于全部字节已送达上游**，快照之后的异步回调也不能追写既有日志。
5. `connections`：Go HTTP 客户端看到的连接不透明 ID、Reused/WasIdle；TLS 只记录握手事件次数与是否恢复，不记录地址或证书。代理场景不证明代理到上游的连接形态，TLS 事件也可能包含代理握手。连接 ID 不是提供商模型/KV 缓存节点。

已有 MarkSent/不确定发送/重试和原生详情 trace 回调被保留；GetBody/Read/Close 的返回与错误保持原语义。不开启正文详情，不缓冲响应流，不为了诊断等待写入完成或改变首帧 flush。

## 如何比较

所有指纹使用独立、仅进程 RAM 内的随机 HMAC 密钥，附 `runId`；不复用路由 secret，不记录路由指纹原值。重启后不能直接对比指纹。完整 body 指纹不受 64 KiB 详情截断限制。

- `rootOrderTag`、`otherFieldsTag` 分别保留 root 键线缆顺序、除历史和工具外的字段字节与顺序；`toolsTag` 包括工具定义及 schema 属性顺序；`cacheKeyTag` 是缓存字段的原始 JSON 指纹。不会重编码消息、schema 或大整数。
- `previousClient/previousPrepared` 按同一身份、模型、路径对照前序本地观测；`attempts[].previous` 对照前序实际 attempt，并记录前序 requestId/attemptIndex/accountId。它们可能指向本次请求的重试，不假装是新的会话回合。
- `commonSequencePrefix=N` 表示前 N 个消息/输入项原始 JSON 字节相同，后续若有差异，N 也是首个差异的零基索引。`previousSequenceRetained=true` 表示完整保留前一条已观测历史，仅可能尾追加。**不表示提供商一定已处理、缓存或采用同一种 token 化/序列化。**
- `sequenceReordered/toolsReordered=true` 只说明元素字节的多重集合相同、排列不同，不表示请求没有其他变化，也不自动等于缓存下降原因。
- 最多保留 16 个会话基线，每条基线最多 2048 个消息/输入项、256 个工具的内存指纹；最多登记 256 个连接。逐元素上限触发时，完整 body 和已定位数组的指纹仍覆盖全部字节，但逐项比较不完整；root 字段超过 128 个或相关字段重复时，只保证完整 body 指纹，结构信息标未知。`sequenceComplete/toolsComplete=false`、缺失比较字段、`untracked/eventsTruncated/attemptsTruncated` 必须解释为未知，不是没有变化。
- `sample` 是采样预算序号，比较基线顺序是本地观测顺序，不保证抵达/服务商处理顺序。`mayOverlap` 标记前序请求当时可能仍在途，不能按响应完成时间强行认定串行预热。
- `sourceTag` 覆盖账号 ID、凭据、代理配置及目标 origin 的 HMAC；`targetTag/affinityTag` 分别区分完整目标地址和最终 X-Affinity。不同账号/源配置不当同缓存控制组。只观测目标 Key，不能宣称掌握其他 Key 对共享连接的使用。
- 每 attempt 关联现有真实服务商 `upstreamRequestId`，缺失保持未知；匹配到已知凭据/当前请求身份的值省略并标 `traceRedacted`，不替换原生响应头。常规请求摘要的最终 trace 行为未被此诊断改写。

JSON 空白/对象字段顺序或合法兼容适配也可能改变指纹；提供商可能规范化或忽略部分字段。应先定位**哪一层发生变化**，再结合相同身份/账号、原生 trace 和真实 usage 判断，不能把任意 HMAC 变化直接当作模型 prompt 缓存失效。

## 隐私与资源

日志只新增计数、布尔、HMAC、已有请求/账号 ID 和受限服务商 trace，不保存正文、工具/schema/消息文本、原始会话/缓存值、认证值、代理地址或网络地址；逐消息/工具指纹只在有界 RAM 基线内，不整列落日志。既有管理员 clientRequestHeaders 的原值授权不扩大到新诊断。

默认关闭路径不创建指纹/正文 Reader/trace 回调。启用后完整正文扫描/HMAC 会有诊断开销，所以限 Key/时长/请求数，不能拿启用时的时延作为生产优化收益。沿用现有日志存储与轮转，不引入新日志目录或第二个调度器。

## 本地验证

`mistral_cache_audit*_test.go` 用纯本地 mocks 覆盖大正文中段、字节/属性顺序、大整数、历史追加/重排、别名适配、原始/重建/部分 Reader、连接复用与新 TLS、实际 attempt/重试关联、异步快照、TTL/并发预算/内存边界与隐私。另有 Node 和本地假数据浏览器检查管理员入口、JSON 转义、详情关闭仍可读、API no-store 和真实读取指纹往返。

这些检查不需要生产凭据或真实推理，不能证明真实会话低缓存根因已确认或缓存率已改善。
