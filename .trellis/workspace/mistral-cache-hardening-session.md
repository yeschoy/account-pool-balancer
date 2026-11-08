# 2026-11-09 — Mistral缓存可靠性直接实施

## 用户范围

用户在Magpie源码对比后明确“开始吧”，随后补充“不要建立trellis任务，直接来，做完之后不要做这么多无用的测试”。立即撤掉刚生成、未提交的4个任务骨架文件及空目录，不新建任务、不改其他任务状态；仅保留需提交的规格和本会话记录。直接落实已确认缺口，验证只覆盖本次和受影响的严格绑定边界，不全仓/race/vet/重复UI，不主动推理、不推送/部署、不改线上配置或详情开关。

## 一次有界自然流量核对

- SSH只读确认仍运行`5d15347e55d5b63986f8f79e708223d43e035ef8`，started=2026-11-08T18:43:30.55635019Z，running/restarts0；未重启。
- 2026-11-08 19:25:38.584–19:27:38.599 UTC复用已有内存TCP重组骨架，120秒正常结束。仅输出固定计数/枚举，不存pcap、原始正文、凭据或会话值，不开启详情日志或发模型请求。
- 捕获9个Mistral DeepSeek Chat请求：所检查的标准会话字段、cache key、子agent头、system计费标记均0；两个重复身份/开场分组的system/tools均未变。开场分组不等同真实会话。
- 同期业务日志另只读汇总11条，sessionSource/affinityKeyType全部message_hmac。入口捕获与完成日志计数边界/覆盖不同，未逐条关联，不声称9条与11条一一对应；两处均支持显式身份覆盖仍低。
- 未发现动态计费行，因此不增加删提示词逻辑，也不修改弱身份/父子agent策略。现有标准头识别无法凭空恢复客户端未传/此前已丢失的身份。

## 本地实现

1. Mistral真实session优先：复用原自定义/父会话/正文真实session/Claude结构/内置头顺序，只有不存在真实session时回退body cache key、兼容缓存头、原message_hmac。调用者上游cache key值不改，通用identity/其他池优先级不改；同时存在cache hint与真实session的旧绑定可能有一次重新建立。
2. Mistral Chat的`promptCacheKey`在身份解析前无损归一化。字符串/null/空值保留；同值双拼写合并且保留蛇形原字节；冲突/非法类型/重复及转义重复本地400、零上游。原工具/大数/正文不重编码，Responses等其他路径/池不转换。两种拼写及其私有值进入原隐私遮罩。
3. 普通Mistral租约携带ClientKeyID，来源hash固定账号provider/key/base/proxy、分组、下游Key ID/hash。仅MarkSent确认后的普通绑定导出到现有metadata.pool.mistralBindings，严格responseBindings合同不变。恢复保留绝对期限与explicit/fallback，不恢复并发/RPM；当前更短TTL只缩不延。严格绑定先恢复，共享4096 LRU。
4. 账号/分组/启用、上游凭据/代理/Base、下游Key/hash/启用变更与过期均清理；命中再次校验来源，删除/LRU淘汰置dirty防复活。普通新增/续期使用已有samplesDirty、一分钟合并写入，正常退出强制保存；崩溃可能丢最近未flush记录，不承诺零丢失。

## 必要验证（一次）

- gofmt与diff检查。
- 一次定向`go test ./internal/server ./internal/pool -run '^(TestMistral(SessionPrecedesCacheHint|CamelCacheKey.*|OrdinaryBindingSurvivesServerRestart|CacheStableIsolatedAndCallerKeyPreserved|CachePreservationAndProtocolScope|CodexClaudeSessionIdentity|Binding.*)|TestBindingLimitEvictionMarksPersistedRemoval|TestStrictContinuationNeverOverflowsOrRebinds|TestOpenCodeBindingCheckpointRestoresOnlySameCredential)$' -count=1`：server1.338秒，pool0.420秒，全部通过。
- 范围仅本次会话优先/别名、歧义拒绝与隐私、真实假数据HTTP→metadata→新Server保留m2、显式/弱TTL、发送/取消/晚回调、来源变更矩阵、LRU删除，以及必要严格续传/原缓存字段回归。
- 一次必要`go build -o /dev/null ./cmd/odb`成功，无临时二进制。
- 未运行全仓、race、vet、Node/浏览器或真实推理。Mock不代表生产命中改善；本次没有生产发布。

## 兼容与交付边界

新metadata派生字段可能被旧版严格JSON读取器拒绝。上线需另行授权，保留完整一致备份；降版前保留当前数据并仅移除mistralBindings（如存在），不要覆盖后续写入或改严格responseBindings。首次升级不能恢复旧版本从未保存的普通内存绑定。

本地代码、规格、docs与本Trellis记录一并提交，不push、不部署。临时采样/脱敏汇总/定向日志位于`.analysis/mistral-cache-hardening/`且已登记`_tmp_artifacts.md`，未经用户同意不清理；Trellis记录保留。
