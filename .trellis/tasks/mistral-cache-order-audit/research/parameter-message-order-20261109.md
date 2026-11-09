# 参数上传与消息顺序专项（2026-11-09）

## 用户最新澄清

用户：“我不是怀疑切号这个，而是怀疑是我的参数上传有问题，或者发生了用户消息重排序导致的”。本轮主线改为上传参数、完整消息/工具数组及转换后实际HTTP上传，不继续把选号/TTL当主因。仅限有界自然流量RAM比对、公开合成HTTP mocks和源码审计，不主动推理、重放用户正文、不改生产配置或重启。

原始正文/思考/凭据仅远端RAM，没有pcap/正文文件；只保存计数、布尔、允许字段名/数值、随机HMAC及已有请求ID。既有默认关闭的生产实际出站诊断未擅自开启。

## 实际版本，避免拿本地分叉替代运行源码

- Balancer实际revision c1aebbcc452212bb262a18d3e95528548234344b。
- NewAPI实际镜像calciumion/new-api:latest，OCI标签version v1.0.0-rc.49，revision **48a6c87f9e7757fdc58e5be9bcb4f8281ad0dfd0**，source QuantumNous/new-api。不是当前本地fork HEAD3914bf702，不能用其自定义代码解释现运行版。
- 只读实际commit的OpenAI adaptor：能力规则可能修改输出预算拼写、采样参数或首条role；不是消息数组排序器。结论仍以本次实际两跳观测为准，不把源码无sort当全链路验收。
- 源码：https://github.com/QuantumNous/new-api/blob/48a6c87f9e7757fdc58e5be9bcb4f8281ad0dfd0/relay/channel/openai/adaptor.go

## 1. 两跳生产证据：12组均未发现消息重排

复用既有TCP重组观察器，在同服务器RAM比较NewAPI入站3000与Balancer入站8790。8790严格筛选当前Mistral下游Key；3000只保留允许的对应模型候选，按模型/完整消息核心序列或多重集合、时间窗口唯一匹配；歧义不猜测、不把无匹配当一致。只输出安全结构，不打印原始消息或认证。

| UTC窗口 | 3000候选 / 8790目标 | 唯一配对 | 歧义 | 消息重排 |
|---|---:|---:|---:|---:|
| 14:31:29–14:33:29，120秒 | 22 / 9 | 7 | 0 | 0 |
| 14:36:10–14:37:40，90秒 | 22 / 7 | 5 | 2 | 0 |

两次均capture_exit0、正常结束。**12组messages语义完全相同、tools语义完全相同，模型名按渠道映射变化是唯一根字段值差异**。部分JSON对象的键/空白被重新序列化，不等于消息列表重排。

第一轮89条、91条长历史及132工具，两跳不仅语义相等，消息/工具原始JSON也完全相同。89→91→93的已匹配同组连续请求保留之前全部89/91条消息，没有中段插入/调序。真实input/cache分别116844/111360（95.307%）、118266/111872（94.594%）、119601/117376（98.140%）。该组属180/user67/DeepSeek，不是188的GLM样本。

## 2. 找到上传侧“并非总是历史追加”的具体线索

第一轮另一组，Node UA、渠道182/user821、135工具，4消息：

| UTC开始 | requestId | input / cached | 缓存率 |
|---|---|---:|---:|
| 14:31:53.456 | req_bea6539e49ba134a4b97fbcc | 61826 / 2688 | 4.348% |
| 14:32:36.246 | req_7408212392a4fef546feca00 | 61465 / 0 | 0% |

相同首system/user指纹组、同下游源API Key候选和同上游账号，但两次仍只有4条消息，完整共同前缀仅2条，**零基索引2即第3条消息在进入NewAPI之前已改变**；并非仅在末尾追加。3000与8790分别观察到相同变化，每个请求两跳messages逐项语义相同，没有纯排列变化。

这是上传/更前面构造层的真实变化，不是号池将用户消息重新排序。但未保存消息原文/逐字段指纹，具体哪一字段改变未知；没有真实会话ID，不把相同内容指纹组冒充已证同一业务会话。这也可能是正常的辅助/工具选择调用，不能仅凭这一对定为客户端bug或解释全部缓存异常。

另一次捕获4消息、159工具的输入，在**3000与8790两侧都是 `[user,user,user,system]`，system位于索引3**。输入已经是这个布局，不是NewAPI或号池把system移到最后。是否应调整发送端组包要结合调用目的/模型模板，不能擅自把system移前或给所有用户重新排序；它不是上面135工具那组相同请求的证明。

## 3. 参数实值及官方契约

真实长请求两跳均是：`max_completion_tokens:32768`、`reasoning_effort:"max"`、`stream:true`，没有被上传链路随机改成别的值。上述95%–98%高命中组也用同样参数，所以这组参数不是“必然关闭缓存”的充分原因。

2026-11-09重新实际读取Mistral官方Chat参考：列出max_tokens/prompt_cache_key，未列max_completion_tokens；当前号池对max_completion_tokens原样保留、不改名、不覆盖预算。这是**待核实的模型原生兼容点**，不是已证丢参/缓存根因。不能仅凭文档未列就断言上游忽略该值，更不能未经冲突/语义核验静默改名。

特别是官方通用reasoning_effort枚举也未列max，但模型专属reasoning文档明确GLM5.3/Flash支持low/high/max、不支持none，说明通用schema与模型专属契约不可混用。没有擅自把max降为high/none、调小输出预算或做真实推理探针。

部分文本聊天样本assistant历史没有reasoning别名/native thinking；没有对应原轮完整输出，不能认定是丢失。官方要求完整回放含ThinkChunk的assistant消息，须在发送端进一步核对保存/重放链路，不能无依据合成或丢思考。

参考：
- https://docs.mistral.ai/api/endpoint/chat
- https://docs.mistral.ai/inference/conversations/reasoning

## 4. 转换→真实HTTP mock上传的定向回归

新增`internal/server/mistral_param_order_audit_test.go`，复用既有mistralEnv、独立json.Decoder、HTTP捕获和Reader审计，不复制生产转换器当预期值。

- 三精确模型deepseek-v4-1-flash-1017/zai-glm-5-3/zai-glm-5-3-flash，分别显式Session与纯消息兜底，共6子用例。
- 每组132个逆序工具、非字母序schema、25→29→33完整历史，正文超过64KiB。
- 对照mock服务器实际接收到的全部JSON：用户/系统/工具消息逐索引与源内容一致，assistant的独立唯一回答/思考标签亦逐项对应，防止只比较role漏过初始回答互换；工具调用ID/参数关联不变，已发过的全部消息准备后字节前缀保留。
- model/tools/max_completion_tokens/reasoning_effort/temperature/top_p/parallel_tool_calls/stream/metadata原始字段字节保持，含非标准数字排版0.30e0与9007199254740993，不重排schema。合法思考别名/空白占位按已有契约适配。
- 三轮审计记录数严格为3，每次final attempt与prepared完全相同、实际Body Reader完整匹配、写入成功，无写入失败；不以空记录循环冒充通过。
- max_completion_tokens的断言验证当前“保留字段”的行为，不证明Mistral接受该OpenAI拼写；未来经证实要做别名适配时应同步更改契约/回归。
- 与既有`TestMistralCacheAuditHTTP2TLSAndAdaptation`合跑，覆盖TLS/HTTP2真实mock读取与历史追加。

从**干净部署提交c1aebbc**归档副本仅加该测试，排除其他窗口注册/Dockerfile修改；最终补强assistant逐条对应检查后，两项定向测试1.762秒通过，未重跑全仓或任何付费推理。第一次也是通过，本轮不是bug修复的红绿结果。

## 5. 当前结论与尚缺项

已确认：所配对12组没有NewAPI消息/工具数组重排、关键参数上传没有被中转改值；定向真实HTTP mocks未复现三精确模型在prepare/attempt/Reader层重排或数值损坏。观察到发送端更前面有历史改写及需复核的system末尾布局，值得按实际客户端调用用途排查，而不是继续查轮号。

未确认：真实生产Mistral TLS出站Reader的本窗口指纹仍未开启，不能拿mock/源码当已观察到的生产线缆证明；不能排除其他未采样请求/客户端重排，更不能把180/182 DeepSeek证据冒充原188/GLM根因。原188仍需要原用户新长会话自然请求。当前仅新增回归与业务记录，没有有证据支持的新生产参数/排序补丁，未部署或重启，核心缓存任务保持in_progress。
