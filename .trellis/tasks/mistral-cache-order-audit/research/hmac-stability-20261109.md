# HMAC多次调用稳定性核对（2026-11-09）

## 用户问题与边界

用户追问“HMAC的计算方式会不会导致多次调用导致不一致”。只审计实际路由/缓存提示计算与诊断指纹区别，用干净线上c1aebbc源码做本地合成计算/重载/并发回归，另只读本机运行拓扑和metadata。没有真实推理、生产重启/配置更改、用户正文重放或新的流量采样；不保存或回显路由密钥、真实HMAC/会话/缓存值。

## 实际公式与生命周期

`internal/server/identity.go`：每次fingerprint都新建`hmac.New(sha256.New,s.secret)`，不是复用上一次的可变hash状态。

```
scope = ClientKey.Group + NUL + ClientKey.ID
fingerprint = lowerhex(HMAC_SHA256(routingSecret, kind + NUL + scope + NUL + value))
```

Mistral真实会话取kind=session、value为按固定优先级选出的会话值。消息兜底取kind=messages，value是固定顺序二元数组的JSON：`[首个非空system/developer文本的前4096字节, 首个非空user文本的前4096字节]`，UTF-8截断不拆码点。不是整段历史或JSON map序列化；JSON转义解码后再用固定数组序列化。

- 不混入时间、随机nonce、请求ID、账号、模型、温度、effort、输出预算、工具；首system/user已确定后，尾部追加的其他历史也不参与。如果原先缺首system/user、随后才补上，则选中输入会变化。这些字段改变仍可能影响模型缓存，不能因HMAC未变就说prompt没变。
- 消息兜底源码虽有map复制，但取值按固定字段/数组次序，不依赖该map的遍历排列；stableConversation也保留根级system/instructions优先于messages/input提取的既有规则。本结论是对相同实际选中值的计算稳定性，不把可能冲突的多个身份来源当成同一输入。
- s.secret在Server.New加载metadata.json.routingSecret，只在缺失/空值时首建随机密钥，成功启动前saveMetadata(true)持久化；普通请求、配置发布、统计持久化不重置它。恢复同一数据和密钥则结果相同，不删除metadata、不用空数据目录或擅自换key。
- 下游Key ID/组、选中会话值或首system/user参与截断部分变化会有意改变输入。首消息选择因重排/压缩变化也可能改key；不能把同一业务用户等同于相同hash输入。

`mistralRequestCacheKey`与prepare：已有正文prompt_cache_key（包括显式null/空）优先保留；无正文key时，先采用有效缓存/会话头**原值**，没有可用头才用`apb-mistral-<fingerprint>`兜底。显式会话头发给上游的key不经过HMAC，本地账号绑定仍另用HMAC隔离。

## 实证1：10000次串行/并发计算不漂移

新增`internal/server/mistral_hmac_stability_test.go`：

- 2000串行+32协程各250次=10000次，使用实际parseRoutingBodyWithHistory(strict=true,history=false)→mistralIdentity→mistralRequestCacheKey路径。
- 混合JSON root/metadata键顺序、等价Unicode/HTML转义、变化的请求ID/UA、不同工具/采样参数与尾追加消息；第一system/user有效输入保持不变。
- 全部身份HMAC与兜底cache-key候选相同，无共享secret字节修改。改变调用方scope或secret则按预期不同。
- 从本地fake配置和已保存metadata重新构造真实Server，密钥/身份结果相同；并未重启生产。
- 干净c1副本仅加本轮测试：normal三项1.104秒、定向race三项2.446秒均通过，无race告警，不跑无关全仓/真实推理。输出只计数/布尔，不打印指纹。

## 实证2：存在“可见文字相同、提取输入不同”的格式边界

当前共享contentText的既有行为：

```
content: "question"                              -> "question"
content: [{"type":"text","text":"question"}]    -> "question\n"
```

短文本表示在字符串/文本块数组间切换，会因附加LF改变兜底HMAC，进而改变缺省生成的prompt_cache_key；这不是HMAC算法随机，而是参与计算的输入不同。多个text块亦带分隔LF；真实空白、换行及Unicode不同形式也不是同字节输入。

回归已复现短文本两种形式指纹不同；同时验证显式Session-Id固定时不受这种内容表示变化影响，超过4096字节的表示差异/后缀不在截断范围内时也不改变HMAC。

**尚未证明目标业务触发该边界**。此前25→29那对全部旧消息原始JSON相同，不能用这一新边界硬解释其0→7.3%。没有调整共享contentText或上线新的指纹规则：这会影响其他池和已持久化绑定，不能没有目标因果证据就全局trim/改分隔/伪造固定key。测试明确标为现有行为characterization，未来经批准规范化时须更新测试/兼容评估，不把该边界当理想不变量。

## 实证3：本机密钥/实例没有发现随机漂移条件

UTC15:59:58只读快照：

- metadata密钥为32字节，间隔读取3次一致；仅在远端RAM以此密钥对公开合成固定输入重复1000次，输出一致。没有打印/传出密钥或hash。这只证明该短观测窗口，不声称已监视全天密钥。
- ai-internal中account-pool-balancer与opendesign-balancer兼容入口均只有同一个account-pool-balancer容器为owner，没有观察到两入口分摊给不同密钥实例。
- Balancer healthy/restart0、revision仍c1aebbc，NewAPI healthy/restart0。没有做真实进程重启测试；跨重载证明来自本地fake文件复建。
- metadata整文件可随统计变化重写，不能拿整文件hash/mtime变化当routingSecret变化。

## 三类HMAC不能混淆

1. **路由/缓存兜底HMAC**：持久化routingSecret、会话/消息身份输入，用于本文实际稳定性判断。
2. **内置cacheAudit指纹**：独立每进程随机诊断key/runId；跨进程不能比较。完整body标签还会因正常尾追加而变化，不能据此说cacheKeyTag变了。诊断key不参与路由或发给Mistral的缓存键。
3. **本次外部RAM观察标签**：每次采样单独随机salt，故不同采样窗口相同文本也会有不同标签；仅限窗口内对照，不是实际prompt_cache_key。

## 对原188的具体限制

既有14条原188/user69 GLM5.3 Flash业务请求已确认sessionSource=header/cacheHintSource=session_header，所选会话头原值逐条一致。按已核对发送代码，该组上游缓存提示是会话头原值，不是消息HMAC生成的apb键，因而上述消息兜底表示边界不能解释这组缓存key的抖动。不要把后续180/182缺会话头的fallback样本或诊断换盐混为原188证据。

## 结论/交付

相同密钥/scope/实际身份输入的重复或并发计算，未发现HMAC本身随机不一致；代码/normal/race/本地保存重载与本机拓扑均支持这一窄结论。发现可复现的表示提取边界，须针对真实低缓存原会话验证是否发生字符串/文本块切换或首消息输入变化后再决定Mistral范围的修复；不伪称已修原缓存故障。

本轮只新增定向回归和Trellis/spec记录，无生产逻辑、配置或部署变化。临时脚本/回执已登记_tmp_artifacts.md，经同意才清理；核心缓存任务保持in_progress。

## 用户追加：转换是否造成乱序/结果变化，再影响HMAC

这个问题不能仅凭“同输入HMAC计算10000次一致”排除，需要查实际时序与共享数据。重新核对c1代码及新增真实本地HTTP回归：

1. `proxy.go:654–677`先统一Chat的promptCacheKey→prompt_cache_key别名。这只替换缓存字段，保留messages原字节，不是消息转换；别名冲突拒绝而非随机选值。
2. `proxy.go:716`用原始（仅已统一别名）的身份投影计算mistralIdentity。
3. `proxy.go:738`再prepareMistralRequest：处理effort、DeepSeek developer→system、思考块、图片形状/标准缓存提示，并填入最终缓存键。
4. `proxy.go:795/887`使用固定ident选路并进入attempt；`1465–1473`Mistral跳过其他provider的upstreamBody改写，直接bytes.NewReader(body)构造HTTP请求；`1523–1530`复用既定缓存键做X-Affinity。**模型内容转换/后续attempt不重新计算该身份HMAC**。

转换器逐项扫描原数组并写新bytes.Buffer，只改当前消息字段，不对messages或tools排序；改变role/type结构不等于移动消息。assistant正文字符串可因思考转为thinking/text数组，但消息兜底只看首system/user，不看assistant；当前号池适配器不会把首条user的文本字符串自动变成text数组，DeepSeek的user处理保持数组外形，仅改图片/提示字段。因此前述短文本LF边界不是“号池自动转换首user后再hash”造成的。

新增`TestMistralConversionUsesOriginalHMACAndImmutableInput`：三精确模型×message fallback/Claude user会话carrier=6组合，每组对同一输入转换10次，核对原byte slice、仍引用原历史的惰性路由投影及从该投影重算身份均不被改写，且60次转换各组输出逐字稳定。验证role（DeepSeek的显式developer→system例外）、user/tool/system文本及tool_call_id所在索引不变；另外原132工具25→29→33消息测试复跑，检查完整旧历史前缀与所有工具/调用关联保留。

每组还做2次真实本地HTTP post、共12次mock接收：实际上传正文逐字等于转换结果、X-Affinity/正文缓存键取转换前身份。测试刻意加入“若错误地对转换后的body重算，就确实得不同HMAC”的可区分样例：新增的prompt_cache_key会成为另一个身份提示；DeepSeek还会删除已提取会话的user兼容标签。实际handler仍上传转换前选定的key，不能将这种反事实手工hash结果当成真实发生的二次hash。原132工具回归另18次mock，共30次本地mock HTTP，均非生产推理。

两项normal合跑1.517秒通过；新增转换链路6组合race2.234秒通过。没有原字节/投影突变、转换重复结果变化、消息重排或转换后重新hash的复现。

**限制仍在**：这些检查说明本地当前转换链路的上述机制未复现缺陷，不是原188真实生产TLS上传已取证。NewAPI/客户端的转换发生在号池HMAC之前，若它改变选中的首消息文字/表示，确实可能改变输入；已有12组双跳只约束那些已观察样本。即使身份HMAC保持不变，转换后完整历史前缀若不稳定，模型缓存仍可能下降，因此不得用HMAC不变宣布转换没有任何问题或原缓存根因已修复。
