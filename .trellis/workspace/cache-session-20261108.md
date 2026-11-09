# 截图同会话低缓存复查（2026-11-08 UTC）

## 范围与计划

用户要求深度排查截图同一会话缓存低命中。本轮只读：对齐截图请求 → 固定生产版本/实际独立配置 → 检查会话和账号连续性 → 对自然流量做有界前缀/工具/缓存键内存比较 → 核对上游usage与本地统计 → 分清已证实与待证假设。不得改生产配置、日志开关、重启/部署或主动推理。不覆盖其他窗口未提交改动。

临时诊断脚本和脱敏输出登记 `_tmp_artifacts.md`，正文/凭据/原始身份不落盘、不回显。此业务记录保留。

## 初步证据

- 生产 revision be149876b647db580b8656f807e6bdfd76e311dc，启动2026-11-08 20:24:55.294 UTC，restart0；截图时间北京时间11月9日04:44–04:45，即UTC11月8日20:44–20:45，不跨发布。
- 截图目标账号 zxg-07-vibe-key / acc_1de834068bf5988d8988998e，下游自测-test；显式session_id，clientFamily=codex，Session-Id/Thread-Id有效，cacheHintSource=session_header，binding=hit，无overflow/switched。
- 04:44:51 input78118/cached14720；04:45:09 input80444/cached71552；04:45:23 input81721/cached14592；04:45:30 input81819/cached81792。不能由相同账号/身份字段枚举推断真实头值或完整前缀一致，须进一步采样。
- 灰色04:44:36行是newapi下游、zxg-08-vibe-key、321970输入，不能混入目标会话。
- Mistral独立配置sticky=true、smart、explicitTTL1800/fallback600、wait-overflow 2000ms；detailLog=false，保留现状。历史截图原始正文未记录，不承诺能恢复历史payload。

## 1. 截图逐请求对齐（北京时间2026-11-09）

| 时间 | 内部requestId | input | cached | cached/input |
|---|---|---:|---:|---:|
| 04:44:51 | req_13a9ab2f8e644e8112dcb3af | 78,118 | 14,720 | 18.8433% |
| 04:45:09 | req_bcdc71e61b8e4c96e7db51bd | 80,444 | 71,552 | 88.9463% |
| 04:45:23 | req_aec8afae17ca487f476912c3 | 81,721 | 14,592 | 17.8559% |
| 04:45:30 | req_79146abe039863db32d6e289 | 81,819 | 81,792 | 99.9670% |

四条均同下游Key/同账号/同模型/同配置版本/同代理，binding=hit，status/upstreamStatus=200，只有一次attempt，没有overflow或switched。尤其04:45:23→04:45:30仅约7秒，input增加98，而cached增加67,200。这是缓存复用跳变，不是输入分母自然增长可解释的比例下降；但input差不能代替前缀相等证明。

灰色04:44:36行是req_33f76b570d13247a2f16cbd2：下游newapi，账号zxg-08-vibe-key，input321970/cached317568；不归入用户目标组。

目标下游Key+账号从04:43:49至04:48:13共17条已知用量请求：首条binding miss、后16条hit，全部显式header/session_header；0 overflow、0 switched。input1195189/cached733312，token加权61.3553%，6条低于25%，其中4条恰好cached14592。此为Key+账号窗口统计，不以账号相同冒称已逐条证明所有请求原始Session值相同。

以上北京时间04点全部对应UTC前一天20点。04:46:14（17.73%）、04:46:32（97.13%）、04:47:06（17.43%）、04:48:13（99.03%）仍继续高低交错，不能把最高的一条当作持续修复。

## 2. 实际缓存键链路与头截断含义

生产release中四文件SHA256与本地HEAD逐项完全相同：

- internal/server/mistral_request.go：b9d882e75418dc38d28dc6e1199c0df05bef61ddbe87f24663a7a6292aab17fc
- internal/server/proxy.go：2df4e2913194e28b52ec88828898987a167545b051ff023233c38c113c39637f
- internal/server/identity.go：fa2a1b5b4f35494a5f82dd359ed118ae790236dd740ad5d7e31ad3f0a9fc2578
- internal/server/mistral_client_headers.go：e890470eab4a6d6588994732114b3fab985f79ebaaa52c7e7469dca044ac1cf7

运行源码的确定性链路：

1. mistralIdentity先找真实会话；日志明确header/session_id/explicit，不是message_hmac。
2. cacheHintSource=session_header只在prepareMistralRequest实际插入非空键后赋值。不是只检查头存在就记录成功。
3. 四条捕获头共16个、总计1011字节，无配置额外session头、父会话头、缓存头或客户端X-Affinity；有效Session-Id在Thread-Id之前，所以缺失正文缓存键时会直接补Session-Id原值。
4. attempt的ensureMistralXAffinity在发送前使用最终正文prompt_cache_key；客户端未给X-Affinity时自动镜像。此逻辑已在线，不应再以“补这个头”宣称新修复。
5. clientRequestHeadersTruncated=true仅因为X-Codex-Turn-Metadata单值超过512字节。全部16个头名均已记录，远未触及64头或8KiB总限；不是Session头被丢，也不是请求正文截断。此处不输出原始头值。
6. 日志头值已经统一脱敏，不能通过对这些`[REDACTED]`再做hash来证明Session-Id/Thread-Id原值相等；本轮没有作此推断。

当前metadata只存在1条目标Key+账号的普通Mistral偏好，explicit=true，expiry为UTC21:18:13.923（最后04:48:13请求续期30分钟）。这进一步支持该组没有不断失效/重建绑定，但不能独自替代逐请求身份指纹证据。

## 3. 统计与前缀的证据边界

- scanUsage读取usage.prompt_tokens_details.cached_tokens，只有字段实际存在才覆盖。Chat prompt_tokens已包含cached；日志比例为cached/input，不是“cached/(input+cached)”，未发现截图数学错误。
- 81,792、71,552、14,720、14,592均为64的整数倍，符合官方块大小。即使上游按块取整，也不可能单凭最多63个token的块尾损失解释约6.7万token的缓存落差。
- 模型输出适配保留usage，本地没有把账号binding hit伪造为模型缓存命中。
- 历史四条请求的details记录为0；Mistral detailLog=false。不能恢复当时messages/tools完整内容，也不能证明这些历史payload只有尾部追加。
- 同一会话ID只解决选号/路由提示，不证明首system/developer、工具数组、历史assistant思考表示、压缩或插入位置保持前缀一致。

## 4. 有界自然流量内存观察

复用既有只读TCP形态观察器，只输出长度/计数/枚举、每轮独立随机HMAC及内部requestId；正文/凭据/原始会话标识只在远端RAM，不保存pcap或原始响应。不改任何日志开关，不发送合成推理或重放。

- 第一轮UTC20:49:53.406–20:51:53.429，120秒，capture_exit=0。捕获1条newapi自然请求req_5294145b1eca6dd225cb2591，不是目标自测会话；下游原始usage input8429/cached0与完成日志一致。不可把此样本替代截图前缀证明。
- 第二轮UTC20:52:36.240–20:54:36.261，120秒，capture_exit=0。捕获6条入站请求，6条响应关联、5条观察截止前已落完成日志。其中同一自测客户端下游Key出现两组新的显式会话。它们不是原截图目标账号/历史请求重放；其中一组形成如下关键相邻对照。

### 新自然会话的稳定前缀低命中证据

采样临时标签4b4c6984c76444a5，账号始终为zxg-13-studio-key / acc_c90e20df9cf0b67b6e5b06cd，同一模型deepseek-v4-1-flash-1017、同一运行版本；Session-Id与Thread-Id原值的临时HMAC一致且四次不变，均没有正文身份字段。

| 北京时间04:54 | 内部requestId | messages | input | cached | 命中 |
|---|---|---:|---:|---:|---:|
| 02.908 | req_6479c25a156fe24805f2a53e | 3 | 31,461 | 31,104 | 98.8653% |
| 12.049 | req_bc0a33fdaf5cc61a75959d91 | 7 | 34,369 | 31,104 | 90.5002% |
| 17.676 | req_01f1cfc19e965321fd599b95 | 11 | 37,608 | 0 | 0% |
| 28.463 | req_0dfdcd56c1e592fe4238d733 | 15 | 45,378 | 14,592 | 32.1566% |

在远端RAM逐项比较，而非只比较长度或首4096字节：

- 3→7、7→11、11→15时，之前的全部3/7/11条消息完整JSON语义相等；每次仅末尾追加4条消息，没有前面插入/修改。
- 首system/developer文本60,657字符与首user文本2,203字符完整相同。
- 49个tools完整定义/数组顺序相同，规范化JSON长度64,136字节；root除messages外各字段语义相等，包括reasoning_effort=high、thinking、tool_choice等。
- Session/Thread头不变，正文无已有cache key，日志连续session_header。因此按已核验生产代码，生成的prompt_cache_key和缺省X-Affinity稳定；没有解密TLS冒充直接观测到上游原生请求。
- 首条binding miss、后3条hit，全部200、无overflow/switched，失败重试/切号不是解释。
- 四个HTTP/SSE下游原始usage分别为上述input/cached，与日志逐条相等；Mistral适配不重写usage，排除前端或本地统计将高命中误显示为0/14592。
- 四条upstreamRequestId均缺失；内部req_*不能直接充当Mistral客服可查的追踪ID。

这组新会话证明：即使稳定身份、同账号、工具与完整历史前缀保留，仍能出现90.5%→0%→32.16%，而且再次落到与截图相同的14,592缓存平台。不能再把该类现象统一归因于客户端缺Session或上下文压缩。它支持主要问题位于上游缓存复用/上报边界；历史截图payload未留存，仍不冒称已逐字证明原四条历史前缀一致。

第二轮工具/消息比较采用JSON语义相等；未记录对象字段顺序，不能将其写成原始JSON字节相等。第三轮UTC20:56:47.613–20:58:17.653，90秒，增加保留对象字段顺序的比较；capture_exit=0，捕获1条newapi请求、0条目标客户端请求，因此未取得目标的属性顺序对照，不把该项列为已验证。三轮stderr均为空、采样正常退出，无常驻采样/正文文件。

## 5. 分层判断，不把推测当根因

**已确认排除的解释**：截图这几条并非漏识别显式Session、message_hmac漂移、smart随机轮号、忙溢出、请求内失败重试换号、跨发布清空绑定、TTL到期、请求头截断导致Session丢失，或简单比例分母/64token块尾计算错误。运行代码已有正文缓存键补齐与X-Affinity镜像，不重复添加同一逻辑。

**本次新自然会话已实证的边界**：稳定真实会话头、同一账号、工具/历史完整语义前缀保留，仍发生大幅缓存回退；因此“绑定命中但长前缀缓存读数不稳定”不是单纯UI、漏头或不断换账号问题。主要损失已定位于上游缓存复用/上报边界，而不是必须继续修本地选号逻辑。

**还未证明的细节**：

1. 截图历史请求没有正文，无法排除原四条本身还叠加前缀变化；新自然对照不是旧请求重放，不混写两者。
2. Mistral托管DeepSeek的内部缓存可能有路由节点/分片、驱逐/容量或使用量上报差异；当前客户端证据不能区分这些内部机制，也不能据best-effort未命中直接宣称提供商违反保证或已确认bug。
3. 第二轮JSON语义比较尚未涵盖工具对象字段顺序；第三轮未捕获目标相邻请求，因此这一较窄的序列化因素尚未验证。不把语义相等冒充线缆字节完全相等，也不把上游内部具体机制写成已确认。

历史`.trellis/workspace/cache-root-20261108.md`曾用同账号、完整1001消息相等和相同生成键观察到持续256 cached，证明这类低复用在稳定前缀下确实出现过；不是本截图同一会话/同一版本，不冒充本次因果复现。

官方当前合同：prompt_cache_key提高命中机会、不保证；需要共享前缀；prompt_tokens包含cached；64token块。未找到官方保证X-Affinity可锁定此模型内部缓存节点的合同。公开GitHub相关issue也不能证明本截图模型存在已知缺陷。

参考：https://docs.mistral.ai/inference/conversations/advanced/prompt-caching

## 6. 最小下一步与交付状态

已取得稳定语义前缀的自然对照，下一步优先向Mistral升级：提供UTC20:54:02.908/12.049/17.676/28.463、精确模型、受限账号标识、input/cached以及前缀/工具/缓存键一致性证据，询问该模型对prompt_cache_key/X-Affinity的实际支持及缓存复用/驱逐/计数口径。现有日志缺服务商追踪ID是升级诊断缺口，不是缓存低命中的原因；需另获授权后以最小、脱敏的指纹/追踪头采集补齐，不能直接开启全量正文落盘。若某会话前缀确实变化，仅报告变化位置/字段形态，不删除用户上下文或工具来制造高命中。

不盲目改smart、全池固定号、加长TTL、增加未文档化缓存头、反复重启或开启全量正文落盘。受控相同payload直连/代理A/B涉及真实推理与费用，需另行授权，不能把只读观察冒充已做。

本轮无业务代码/生产配置/开关修改，无部署/重启或主动推理；其他窗口未提交改动保持原状。只提交此Trellis业务排查记录，不夹带开发中的注册/缓存UI/头诊断文件。临时产物收尾已登记在项目既有_tmp_artifacts.md；该本机清单按.gitignore第23行忽略，不强制加入Git。并发窗口f9c3692的业务提交不是本轮操作，不改写其历史。诊断脚本内存compile语法检查通过，不产生pycache；git diff --check通过；纯诊断文档不重复跑无关全仓测试。临时产物待用户同意再清理，Trellis记录保留。

**交付状态**：截图请求已定位，排除主要本地选号/会话/计算解释，并取得同客户端新会话稳定语义前缀低命中对照；主要问题定位到上游缓存复用/上报边界。未确认上游内部根因，未修复或部署；原截图逐字节前缀和新样本工具属性序列化顺序未验证，均不冒称已验收。

## 7. 用户指定自测-test详情后的后续审计（UTC21:09起）

已用`.trellis/tasks/mistral-cache-order-audit/`跟踪完整审计/修复。只筛选该Key，固定窗口82条摘要和15条wire详情；原生Mistral-Correlation-Id/X-Kong-Request-Id实际保存在详情，并非提供商没有返回，原摘要只提取X-Request-Id才漏记。这修正了上文仅看摘要时的可追溯性判断；详情仍不补回历史未采集的中段。

15条原生/适配/日志用量逐条相同，可见请求头部和工具尾部除必要兼容变化外字节相同。完整本地HTTP回归新增49工具非字母序schema、连续8轮思考/图片/工具调用和约7MiB历史，工具/metadata/旧prepared消息字节稳定，未发现池端重排/截断。Mistral+OpenCode normal8.575秒、定向race10.909秒、vet/build通过。

本地已修Mistral真实trace漏记并补流式/非流式/隔离回归，未修改缓存/选号策略、未改线上设置/部署/主动推理；**此修复不等于低缓存已解决**。代码/任务/规格随本轮提交，临时产物登记等待同意清理；核心缓存问题仍需完整固定payload的受控直连/号池A/B授权或提供商按已取得的真实trace排查，任务保留in_progress等待证据，不虚假完成归档。

## 8. 用户授权6次固定完整公开样本A/B（UTC21:41）

原历史中段无法恢复，已事先说明采用公开完整合成200599字节/26消息/49工具、每次max_tokens=1，不重放私密正文。首个自测-test号池请求正常选账号，之后固定同账号/代理/完整body/缓存身份交叉共6次：号池0%（冷）→直连99.8463%→直连99.8463%→号池99.8463%→直连99.8463%→号池0%。输入每次34485，合计input206910/cached137728/output6，全部200、号池各1attempt，运行容器/config守卫通过，脚本退出0无后台进程。

该轮没有复现直连低缓存，不能以之前证据宣称号池链路因素已排除；也不能用3个直连/2个号池暖样本认定池端bug。连接池复用/上游节点亲和及上游随机未命中尚待区分。已停在6次上限，不追加真实推理；当时下一步拟最多4次每次全新直连连接对照，等待追加授权。证据见`.trellis/tasks/mistral-cache-order-audit/research/controlled-ab-20261108.md`；未改生产配置/部署，未宣称缓存问题已修复。

## 9. 追加授权4次全新连接（2026-11-09 05:22 UTC）

用户明确继续。隔夜超过7小时原绑定已过期，预检后已说明改为原账号4次全新Transport直连，避免改绑定或重新选号；第一条只当可能冷启动。精确复用上一轮公开fixture/诊断身份，守卫同账号/代理端点、200599字节、每次input34485/max_tokens1，HTTP trace确认各1次新连接/握手/成功写入、无TLS恢复。结果0%→99.8463%→99.8463%→99.8463%；input137940/cached103296/output4，退出0、stderr空、无后台进程，停在4次上限。

因此不能把新TCP/TLS或未复用连接本身当作必然低命中原因，没有依据加“强制新建连接/固定单连接”补丁。也不能把第一条跨夜0误作原暖请求异常或将当前3个暖样本视为已排除共享池/上游随机路由。核心问题仍未修复；应转自测-test自然流量的完整出站指纹/连接标识取证，不再追加付费猜测。证据见`.trellis/tasks/mistral-cache-order-audit/research/fresh-connections-20261109.md`；本轮只提交业务证据，临时探针/脱敏回执继续等待同意清理，不归档未解决任务。

## 10. 自然流量完整指纹与连接诊断（2026-11-09，本地开发）

用户批准继续补齐完整实际出站取证，明确先本地实现/回归，不再主动推理、不部署。已新增可选启动Key ID的Mistral专属诊断：默认关闭、30分钟/128请求，独立进程HMAC/runId，有界历史消息/工具与schema/root顺序指纹、原始入站→准备→实际attempt/Reader比对、GetBody读取和完整/部分/不匹配检测、全值X-Affinity/source/target指纹、原生trace及不透明Go连接/TLS复用观测。保持原转发、MarkSent/重试/选号/usage和流首帧语义，无正文/原始会话/凭据持久化。

新元数据复用admin-only/no-store请求日志，UI仅已采集行出现「缓存诊断」。全仓Go normal/race/vet/build及9套Node行为和web JS语法通过；真实本地浏览器使用1假账号/loopback mock验证关闭详情仍可观察完整174291字节实际读取、旧消息前缀/工具不变、复用连接、假trace和API往返/无注入/无秘密。receipt1005bytes已核验allPassed=true；专用浏览器已关闭，PID81080退出、60065监听关闭，文件经同意再清理。

详见`docs/mistral-cache-diagnostics.md`及本任务validation.md。原始6+4次受控生产调用不再追加；核心低缓存仍未解决。后续需要另获授权部署并只为自测-test临时启用自然采样，不能把诊断代码通过当作缓存率已改善。其他窗口注册/Dockerfile改动保留，Trellis不归档未解决任务。
