# Mistral上线观察、缓存证据与effort兼容（2026-11-07 UTC）

## 用户授权与范围

- 用户要求观察10分钟，有可修复错误则修复/重新部署；观察中追加询问缓存低的原因，随后授权解决后部署并重新观察10分钟。沿用小修复/部署流程，不新建Trellis任务，不重复全量测试。
- 未通过禁用思考、删除提示词、全池固定账号、放宽限流或更改上游配置来提高表面成功率/缓存率。

## 第一轮10分钟（20:54:16–21:04:16 UTC）

- 每15秒左右只读核对Docker运行状态、NewAPI PostgreSQL使用/流式状态及Balancer增量JSONL；全过程a2ffae2和NewAPI均healthy、零重启、无OOM。
- NewAPI渠道180共18次成功，全部非流式；Balancer对应18次success。窗口内没有Mistral解析/兼容/网络错误，但不能把非流式样本当作流式复测。
- Modal有10次正常DONE及一次最终429（两条上游429尝试）；OpenDesign有一次上游HTTP500。另有直连外部cctoken渠道153的response.failed（EOF），不经过Balancer；没有证据可用修改Balancer消除该外部失败，不盲目重启/回滚。
- 只读观察脚本和脱敏计数位于`.analysis/mistral-stream-fix/observe-window.py`及`observe10m-*.jsonl`，已登记临时产物。

## 缓存：已知事实与修正初步判断

- 20:42上线后至约21:00的19次Mistral成功请求，input总736950、cached86912，token加权约11.79%；2次有缓存，单次分别92.56%/97.53%。所有19次cached_tokens均有真实读数，并非“缺失当零”；NewAPI的cache_tokens与Balancer一致。NewAPI cache_ratio是计费倍率，不能当命中率。
- 当时19次均message_hmac fallback，18次binding miss、1次hit，分布4账号。但不能仅凭这些聚合统计认定同一会话被频繁换号；其中有不同输入和少于64-token的小探针。
- 渠道180已配置header_override `*`，不能建议盲目重复开启头透传；入口确实没有可识别显式session/cache身份。生产详情日志始终关闭，未开启正文落盘。
- 有界内存线缆形态采样取得4个真实流式请求：同一UA、没有session/cache头、没有prompt_cache_key/metadata；前22→24条消息，首system26298字符、首user91733字符，前缀对比118032字符完全一致。只输出临时HMAC/字段名/长度，不保存正文、pcap、Key或原始会话值。
- 同期日志确认该实际会话连续binding hit、同一账号、未overflow/切换。因此已向用户纠正初步推断：当前真实会话前缀稳定且绑定有效，不能将低缓存全归于会话不稳，不能制造一套新缓存调度器冒充已证实修复。
- 官方缓存合同：共享相同前缀，稳定prompt_cache_key只提高命中机会而非保证；64-token块，小于64无命中。建议客户端提供稳定会话ID，但未声称这会保证缓存或改动客户端正文。

## 确证的新错误与窄兼容

- 21:09:49–21:10:43出现5次Mistral Chat失败，HTTP400/错误3051：`reasoning_effort 'medium' is not supported for this model; supported values: ['none', 'low', 'high', 'xhigh', 'max']`。这些失败不产生可复用的成功缓存结果，不能通过换账号或重试修复。
- 按用户解决错误后部署的授权，仅对Mistral Chat精确模型`deepseek-v4-1-flash-1017`将通用medium映射到high，明确向用户披露可能增加思考token；不降低/关闭思考、不改其他模型/原生档位/Responses/FIM，也不声称这是缓存命中率保证。
- `prepareMistralRequest`仍按原始wire字段位置拼接，并对变更位置排序，保留模型/工具参数/大整数/既有cache key和reasoning历史。
- 定向`go test ./internal/server -run '^TestMistral' -count=1`、gofmt、build及diff检查通过；新增用例覆盖medium字段顺序、cache/工具/大整数/思考保存、一次真实Mock上游请求以及其他模型/档位/协议不变。未重复全仓/race/vet或主动生产推理。

## 第二轮上线与观察中发现

- 02c61a8按既有流程部署，21:16:07.061 UTC公网验收通过；161账号/186注册记录、完整配置及运行参数保留。只做定向Mistral测试/build，正式备份/回退及分阶段时间见部署文档。
- 新观察窗口21:16:07–21:26:07；21:18:06告警触发提前返回给主会话处理：一条真实流式请求正常DONE/completed、input44816/cached44416，命中约99.11%，与NewAPI统计一致。这是实际缓存可工作的生产证据，不是Mock或统计补零。
- 同时21:17:59出现另一个请求的上游422，固定安全诊断指向`extra_forbidden / body.store`，不是medium错误复发、不是网络断流。
- 继续窄兼容：同一精确DeepSeek Chat模型仅省略根store=false/null（原生无状态接口不支持该可选字段）；true/非法/重复store在本地400拒绝，不能默默接受存储要求。其他模型/Responses/FIM不变，嵌套metadata.store不动；这不承诺上游平台的数据留存政策。
- 新增store回归覆盖字段位置/逗号/空白、与effort/reasoning/cache同时修改、工具/大数/嵌套字段保留、拒绝时零上游请求及其他模型/协议原字节。再次只跑Mistral定向测试及build，不重复全仓测试。
- 因新错误需要更新，旧观察窗口不冒充完整10分钟；重新上线后重启10分钟计时。

## 最终兼容部署与重计时

- `8d41516`已推送并从干净快照部署，21:23:09.909 UTC公网验收通过；镜像/完整一致备份/回退/分阶段时间见`docs/ovh-deployment.md`。161账号/186注册记录核心资产、完整配置及运行参数均保留，原02c61a8镜像保留。
- 最终窗口重新计时为21:23:09.909–21:33:09.909 UTC；只读监控同样覆盖Docker健康、NewAPI与Balancer请求/错误及真实缓存计数，不发起额外推理，不回改历史错误，不因文档修改再重启。
- 观察中段两个Mistral请求均成功（一个流式正常DONE），cache1152/input61759约1.87%，二者均新绑定。已明确告知用户：前一个99.11%只是单次请求，不能宣称全池命中率已改善；兼容修复和整体缓存表现必须分开报告。

## 最终完整10分钟结果

- 21:33:09.965 UTC观察完成（窗口21:23:09.909–21:33:09.909，40次轮询）；随后只读SQL复核同一窗口：Mistral10/10成功、6次流式DONE/completed、4次非流式、零type5及stream_status错误，两服务healthy/零重启/无OOM。未再出现content数组解析、medium或store错误，已立即向用户反馈。
- NewAPI/Balancer缓存计数完全一致：input912330、cached124288（约13.62%），2/10有缓存、3/10绑定命中。整体缓存仍低，不能宣称兼容修复已经提升全池缓存率；此前99.11%仅为单个真实请求。不能仅凭fallback/new-binding计数认定所有请求属于同一会话，更不能把冷请求或不同上下文强行合并成会话。
- 其他错误如实独立记录：Modal11次最终上游429/28条上游429尝试，成功15次；OpenCode25次成功。cctoken渠道164的一条response.failed和cline-pass-console渠道71两条错误不经过Balancer。没有以无关重启、凭据轮换、解除限流或更改其他服务来掩盖错误。
- 正式备份内deploy.json追加了公网验收、窗口和脱敏结果；线上运行仍8d41516，后续仅文档提交，不再次构建或重启。所有只读观察/有界内存采样均结束，未安装生产常驻监控或保留原始流量。

## 交付边界

- 已从干净提交构建、保留一致备份/旧镜像、仅切换Balancer并按既有部署约定验收；最终窗口完成，无额外生产推理测试。缓存总体优化尚未被证实，不把可用缓存功能或单次高命中冒充全局目标达成。
- 所有临时产物见`_tmp_artifacts.md`，未经用户同意不清理；本Trellis记录及规范保留并随业务提交。

参考：https://docs.mistral.ai/inference/conversations/advanced/prompt-caching
