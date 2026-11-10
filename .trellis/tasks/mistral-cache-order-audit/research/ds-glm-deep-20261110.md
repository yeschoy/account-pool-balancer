# 当前DS/GLM低缓存深查（2026-11-10 UTC10时）

## 1. 授权与方法

用户要求深查当前Mistral的DS/GLM低缓存，允许查询官方文档。沿原任务，不新建任务。全程不改生产代码/配置/账号、不部署/重启、不主动推理或重放，不自动续开旧cacheAudit。只读现有摘要/metadata、NewAPI PostgreSQL READ ONLY，加两次各120秒的被动RAM观测；真实消息/思考/认证/会话值只在远端RAM，不落pcap或下载正文。临时产物登记`_tmp_artifacts.md`，Trellis保留。

UTC10:03:28及10:18:26：Balancer实际f1024e948b31bb3a51dbe7ae28d4ec8706966e62，NewAPI cb5b639标签，两者healthy/restart0。线上cacheAudit仍关闭。本轮没有配置写入；最终config revision5673，mtime=UTC10:09:49.247（前次规则保存5672之后已有外部写入，本轮不冒称全程固定配置实验）；最终10:22核对仍为该mtime/revision，第二次10:14–10:16观测发生在此文件写入之后。两次观察器和离线probe均退出0，10:18核对无所属tcpdump/probe进程。

### 证据层级

1. 既有请求日志与NewAPI用量逐项对账，排除把未知usage计0。
2. 被动HTTP入站原始JSON逐条消息和tools的完整比较，不用首4KB/相同token数量冒充完整前缀。
3. 以已部署f1024e9的**136个生产Go文件SHA256核验相等**的源码构建纯离线prepare探针；被动输入只经远端匿名管道进入独立进程RAM，调用实际`normalizeMistralCacheAlias → parseRoutingBodyWithHistory → mistralIdentity → mistralRequestCacheKey → prepareMistralRequest`及现有指纹函数。读取实际config/routingSecret，但只输出独立随机HMAC/计数/布尔；不启动Server，不发HTTP，不拿生产Key推理。
4. **第3层是相同版本代码重算准备后的结果，不是在线Server真正HTTP Reader/TLS出站抓取。**本轮已显著缩小prepare因果缺口，但仍不能说拿到了实际出站Reader或Mistral内部KV节点证据。

## 2. 最新缓存统计：全池均值不能掩盖局部低命中

UTC10:04:40，按完成时间ts+durationMs，成功且input/cached均已知：

| 窗口 | DS成功已测样本 | input | cached | token加权命中 | 0缓存/低于25% |
|---|---:|---:|---:|---:|---:|
| 5min |45|6338285|4716032|74.405%|10/14|
| 10min |121|15820849|12275712|77.592%|22/30|
| 30min |439|64223407|52864768|82.314%|68/99|

10min的30条失败/取消没有完整usage，排除，不当0。样本量/比例会随窗口变化，以上不是永久常数。

NewAPI UTC10:12:22最近30min type2消费样本，另按channel/user/model拆组（type5失败另列，异常流可能有消费，不能把所有type2都叫成功）：

| channel/user/model | 消费行 | input/cached | token加权 |
|---|---:|---:|---:|
|182/703/DS1017|26|831624/178688|21.487%|
|182/17/DS1017|26|2342452/697984|29.797%|
|182/821/DS别名|12|474616/176768|37.244%|
|182/611/DS1017|11|1316887/776192|58.941%|
|180/67/DS别名|256|35081436/27122816|77.314%|
|182/594/DS1017|55|7729954/6800384|87.974%|

同一用户可能有多种模型别名/不同会话，不合并成受控组。两种DS外部别名最终都映射同精确上游deepseek-v4-1-flash-1017，不能把别名差异本身当根因。

## 3. 新证据A：历史没改、没换号仍从高缓存降至不足1%

第一次固定观察UTC10:06:57.345–10:08:57.416，120s：23请求、23响应ID关联、22完成日志关联、16个原始下游usage记录，无GLM长请求。进程退出0。

渠道182/user17的同一fallback技术组、同acc_a177090beb3615477ad21831、36工具，均binding hit、无overflow/switched：

| UTC开始 | requestId | 消息数 | input/cached | 命中 |
|---|---|---:|---:|---:|
|10:06:58.856|req_0ee1f27dd3badf50c0fcfe1f|29|18560/17280|93.103%|
|10:07:08.361|req_96ce604722f3b017ce216fd9|32|429无usage|未知|
|10:07:16.060|req_c2e5fe192ce5c81ff2a944fe|32|23164/16000|69.073%|
|10:07:34.227|req_421dd914cc4364340b1790d3|35|25900/256|**0.988%**|
|10:07:41.323|req_494d7914ca0d301e140c294b|37|27112/26752|98.672%|
|10:07:47.721|req_1edb54abeb09a3b8f96006a1|39|27824/25216|90.627%|
|10:07:50.690|req_46b676965893a9b9ad53a71b|41|28107/27776|98.822%|

入站每条完整保留上一条所有旧消息的**原始JSON片段**，只尾追加；36工具、根其余字段、system/first user、身份候选均不变。0.988%那条完整保留前32条，非仅语义比较。原始下游usage25900/256与请求日志一致；NewAPI4095279/channel182/user17的25900/256和stream_status=ok再次确认。69.073%的上一条未在第一轮捕到完整usage帧，使用完成日志+NewAPI4095269，不谎称该条三层都有原帧。

该证据排除该组“因为TTL/换号/客户端改旧消息”作为掉缓存解释；不据此说全部会话前缀不变。

## 4. 新证据B：准备后前缀、缓存键也稳定，仍97.95%→0.62%

第二次固定观察UTC10:14:52.604–10:16:52.653，120s：41请求、38响应关联/38完成日志、离线prepare探针41次，全部cachePresent=true、affinityEqualsCache=true。31个相邻准备对照：26个旧前缀完整保留、5个真实入站前缀变化；0个消息集合相同仅重排。探针/采样退出0、requests_sent=0。

### 182/user611：明确会话、原缓存键稳定、思考适配后旧历史仍不变

同acc_ab6045de9afb86934c9a8ed4、header身份、body已有prompt_cache_key，41工具；每个成功请求单attempt、binding hit、无切号：

| UTC开始 | requestId | 消息数 | input/cached | 命中 |
|---|---|---:|---:|---:|
|10:14:55.867|req_ea632043977293ea6446f0e7|37|59508/13568|22.800%|
|10:15:01.161|req_fabdbc2ee037a347f9e04ca4|39|60222/384|0.638%|
|10:15:22.987|req_8ade3fdeecc372f7f5a5a09c|41|61549/60288|**97.951%**|
|10:15:32.319|req_2d9050b5e676e1de584d30e1|43|62039/384|**0.619%**|
|10:16:17.040|req_e28b2967d915e473f058820e|45|64351/61952|96.272%|
|10:16:22.219|req_9ad5fefd38806b2dfe94eb4c|47|65253/61952|94.941%|
|10:16:44.169|req_49bd5bd6ae82bc26c18c3506|51|66419/65536|98.671%|

- 41→43条时，client与相同版本prepare重算均commonSequencePrefix=41、previousSequenceRetained=true；tools、root非历史字段、缓存键HMAC不变，X-Affinity等于最终缓存键。
- 真实生成参数适配确实会改别名/思考表示，但它对旧消息逐条确定性处理，没有本次末轮位置导致旧prepared历史改变的证据。不能把client→prepared合法差异的前5条不同误报为跨轮前缀变化。
- NewAPI4095821/4095832分别61549/60288与62039/384、同channel182/user611且流ok。低缓存上游trace已逐条核对：97.951%为`01a1c4f4-8acd-709e-b9c1-2ed2f340da04`，0.619%为`01a1c4f4-af3b-734d-bc59-222e42cb0c37`，恢复96.272%为`01a1c4f5-5dec-7b6e-89ad-28a9bcacd633`。工单只用真实trace，不从时序猜造。
- 0缓存重试前一条可能429，不拿失败请求当已预热证据；上表97.95%→0.62%的相邻两条均最终成功，前条完成10:15:30、后条开始10:15:32，串行不重叠。

### 182/user69：显式会话也出现93.82%→15.53%→99.09%

同acc_2fa56abdc1c784dbbce43033、20工具、单attempt、稳定session_header缓存键与X-Affinity：
- UTC10:15:34.032 req_7775985d849ab2768ada0394：34消息、57985/54400=93.817%。
- UTC10:16:00.446 req_71d8b835771ff517c4297c67：39消息、65933/10240=15.531%；client及prepare重算完整保留前34条、工具/其余字段不变。
- UTC10:16:08.152 req_23822e0cf3004dd58b28bc4a：41消息、67174/66560=99.086%。

这排除了“只要传Session就一定好了”；Session缺失是风险，但不是本轮两组掉缓存的统一根因。

## 5. 同时发现可处理的输入形态：同Session交替两套前缀

上面的user69会话相邻还穿插一套21工具请求：同实际identity与最终cache key，但system长度23423↔20802、仅前8字符相同，首user也变化，20↔21工具。prepare对照commonSequencePrefix=0、tools不同，变化在到达Balancer时已经存在，不是prepare随机重排。

UTC10:14:55.159的22消息/20工具/63.75%，与10:14:55.396的20消息/21工具/26.605%几乎并发；之后来回交替。它们可能是agent/subagent/不同工作流，同业务Session不等于同模型prompt。**不能把两者当作完整历史被服务端丢掉的证据，也不能保证必须分Key才有缓存。**

可改善方向是客户端/调用编排保持稳定前置system和tools，同类型工作流用稳定且可区分的cache key（如session+agent角色），而非整会话所有不同prompt强行共享一个值。需先确认实际客户端调用语义；不在网关根据tools数量随机改Key，避免破坏既有粘性/命中和隔离。

这解释部分低缓存，但不解释第4节同前缀高→低，二者必须分开。

## 6. 独立统计验证

metadata Mistral provider统计与请求完成日志逐分钟一致：

| UTC分钟 | requests | input(cp) | cached(ck) | 已测样本(ckr) |
|---|---:|---:|---:|---:|
|10:15|23|1751198|1145600|19|
|10:16|21|2315529|2126080|21|
|10:17|21|1076772|634880|14|

三分钟全部逐项相等，不是分母重复加cached或页面漏显。不同窗口/成功口径/含异常流的NewAPI消费口径不混算。

## 7. GLM当前无新的长业务，不能拿DS结论替它验收

UTC10:04/10:17查询Balancer及**全部NewAPI渠道**的最近两小时GLM：Mistral188仅3条各input13/cached0的周期短检测；另155一条13-token检测，不冒充Mistral。无新的GLM5.3长请求或Flash长会话。

近24小时旧长样本仍是GLM5.3三条60491/0=0%；Flash成功22条931222/562176=60.3697%，共享Key14条276314/51840=18.7613%。都早于37332be/f1024e9上线，没有新版GLM长业务验证，不说已修好或本次证实同一因果。旧同号91.4%→0及原生直连单轮掉零证据保留，但不代替新GLM实际出站。

## 8. 本轮Mistral官方文档核对

直接官方llms.txt定位准确DS页面：API模型名deepseek-v4-1-flash-1017，但**文档slug是deepseek-v4-flash-1017**；初始猜测的带v4-1文档URL404已纠正，不能因此声称模型下线。

- [Prompt caching](https://docs.mistral.ai/inference/conversations/advanced/prompt-caching)：缓存依赖相同开头前缀，稳定prompt_cache_key只提高概率；64-token块；prompt_tokens已含cached，不能重复加；没有公开可配置Chat缓存TTL、X-Affinity锁KV节点或命中保证。
- [Chat schema](https://docs.mistral.ai/api/endpoint/chat)：列prompt_cache_key，未列Chat prompt_cache_retention。不能用OpenAI/智谱直连的TTL、显式cache_control语义解释本池；现有兼容消费retention不等于上游接受24h契约。
- [Reasoning](https://docs.mistral.ai/inference/conversations/reasoning)：GLM5.3/Flash low/high/max且不支持none，原生thinking+text；多轮要求完整回放assistant含ThinkChunk。该要求涉及质量和历史保真，**文档没说任意丢布局一定使缓存归零**，不能把结构风险自动当本轮已证根因。
- [DS model](https://docs.mistral.ai/models/deepseek-v4-flash-1017)：Public Preview、1M context、128k输出、text/image、原样托管；输入/缓存输入/输出价格0.30/0.007/1.20 USD每百万token。
- [GLM5.3](https://docs.mistral.ai/models/zai-glm-5-3)：1M，1.40/0.14/4.40；[Flash](https://docs.mistral.ai/models/zai-glm-5-3-flash)：1M，0.15/0.03/0.50。通用“缓存价10%”不能覆盖Flash精确模型0.03/0.15=20%或DS0.007/0.30≈2.33%的价格，也不能把价格折扣误认为命中率保证。
- [Known limitations](https://docs.mistral.ai/resources/known-limitations)：组织级RPS/TPM、流式usage需要include_usage、10min流空闲超时；泛化工具128限制不能取代实际模型行为，本轮137工具组也有成功高缓存，不能擅自删工具。
- [Priority Tier](https://docs.mistral.ai/inference/priority-tier)：需单独资格/配置、收费溢价，不承诺KV缓存固定；本轮不改service_tier/区域/付费选项。

真实浏览器也读取缓存示例、Chat字段、DS卡和ThinkChunk说明，与官方markdown一致；不用检索结果中无关安全公告当缓存依据。

## 9. 结论与最小下一步

### 已确认

- DS有真实的局部低命中，甚至明确Session、固定账号、缓存键/工具/旧prepared前缀均稳定时，仍97.95%→0.62%；统计/页面不是主因。
- 本次已排除这些具体样本的TTL/换号、缓存键漂移、入站旧历史改写/随机消息重排，以及同版本prepare确定性适配破坏旧前缀。不能把它扩大成所有本地链路永远无责。
- 某Session同时混用不同system/tools确实存在，可从调用端做稳定工作流cache key/前缀管理，但不是全部问题。
- GLM当前只有短检测，无新版长会话验收。

### 仍未最终定责

优先怀疑Mistral内部缓存节点选择/逐出/容量调度，原因是相同prepared前缀、账号和key下离散掉低又快速恢复，并且存在backend_out_of_capacity事件。但**本轮没有看到内部节点，3505与某次掉缓存未证因果**；也尚缺在线HTTP真实Reader/source/connection证据，不能100%定责官方。

### 建议

1. 不改TTL、不强制单连接/全新连接、不删思考/工具/历史，不追加付费猜测。
2. 将两组低命中request/上游trace、UTC、input/cached和“prepared前缀保留”的安全证据交给Mistral支持，请核对内部cache routing/eviction；本轮只准备证据，不自动向外部发送任何用户日志。
3. 若需要最后区分在线transport与官方内部问题，沿现成诊断为当前低命中Key ck_85e17680a29c7cf2121740e1补实际Body/GetBody Reader+source+connection+trace；它会涉及一次短重启/新的30min128request窗口，**需明确授权，本轮未执行**。之后只等自然业务，不主动生成。原188诊断不自动复活。
4. GLM请原低命中会话产生新的自然长请求，并精确选择对应Key；没有新样本不能靠DS外推，仍保留无损布局方案的客户端范围决策。

无新业务补丁或部署；本轮交付是更强的实时因果证据与调查范围收敛，不冒称缓存改善，任务保持in_progress。
