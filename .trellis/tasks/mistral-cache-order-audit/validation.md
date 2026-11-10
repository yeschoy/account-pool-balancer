# 验证与交付状态

## 最新已部署/真实HTTP取证（2026-11-10 UTC14:40–14:48）

92cc0d7已推送并上线，旧f1024e9→新版本构建7.35秒，629文件哈希核验、完整一致备份、Compose仅image+精确诊断Key变量，191账号/186注册/完整配置与13硬隔离/运行资源和双别名保留，Docker healthy0重启/无OOM、NewAPI双入口、公网页面/healthz/四资源200与提交SHA256通过。

独立临时Key六轮Flash/high/1536合成长聊，input224081→229637，每轮≥200K/200/自然stop/单attempt，实际Reader完整HMAC=prepared=client、5组旧前缀保留/无并发重叠、同账号代理/稳定cache/真实X-Affinity、同一HTTP2连接ALPN h2。缓存0/0/0/0/98.82%/97.82%；原生完整usage与下游及日志usage匹配。响应头20个无截断、仅__cf_bm Cookie且不保存值，无节点/区域/亲和回显；CF DYNAMIC是CDN非KV状态，未知X-Max-Retry-Attempts-Reached只HMAC且6轮相同，不猜值/重试次数，usage没有service_tier。实扣Vibe USD0.15117091与官方价逐笔/总额吻合。

UTC14:46:07专用Key已禁用/账号group恢复/logout，14:48:23独立验证exit0/stderr0/进程退出/healthy0重启/双入口；该Key采样6条、其他Key0。原控制器target_key_fields_restored=false仅因有意禁用新Key，最终对比部署前完整备份配置仅enabled/revision改变、13硬隔离一致。收尾发现启动校验要求Key启用，仅禁用Key却留变量会导致下次启动失败，已告知用户并用同镜像/不构建做必要关闭切换，完整一致备份后Compose仅清变量。14:57:25 healthy/双入口，14:58:01最终变量空/专用Key禁用/组恢复/13隔离不变、配置仅enabled/revision变化，公网healthz200，无追加推理。后续部署继续保持变量空，禁止无授权续采。无正文/凭据/Cookie值输出或全池detail/区域/Priority/PAYG变化。正式research/http-wire-six-turn-20261110.md；本次已实证不是请求prepare/Reader/连接漂移，但上游内部路由/缓存仍未知，核心任务不归档。

## 最小单Key HTTP/usage诊断本地验证（2026-11-10，发布前记录）

用户明确授权发布短重启和最多6次200K合成连续请求。扩展既有cacheAudit，仅受单Key/30min/128保护的attempt添加脱敏请求/响应头、协议/ALPN和有界完整usage，实际正文HMAC/流式/重试/统计不变。隔离HEAD干净副本+本轮四源码/测试文件验证，其他窗口注册/指纹/Dockerfile未带入：全部Mistral定向normal11.671秒、相关cacheAudit/Wire/前缀race8.143秒、server vet及cmd构建通过。覆盖JSON/SSE/full usage未知数值/大数、header trace与旧详情组合、Cookie/认证/未知值掩码、边界/晚回调快照与关闭零分配；初轮两条失败是关闭测试fixture分配及mock Host原文泄漏，已修正并全相关重验。

正常admin API新建专用临时Key ck_058a943b722e55f52cf86276，独立空组mistral-http-audit-20261110，不移动现有业务/自测Key、暂未隔离账号或生成推理，其他配置保留并logout。当前尚未部署新补丁或采集新HTTP，后续状态追加，不冒称缓存修复。

## 最新真实实验：Flash ≥200K连续20轮及官方预算实扣（2026-11-10 UTC13:40–13:46）

用户批准临时隔离可用账号/自测Key并恢复，明确不在乎本次额度。正式测试经f1024e9号池而非直连生成，不经NewAPI；同账号acc_78068235bc580c4f6eeb8bdb/代理/Session/cache/X-Affinity，20轮high/1536，实际input224081→245056且每轮≥200000。20次均HTTP200/自然stop/SSE DONE/单attempt，后19次binding hit，完整41消息与全部思考/答案哈希、末轮历史及20条usage日志对账通过。首1/2轮0，第8→9→10轮98.64%→0%→98.22%，其他17轮96.41%–98.96%；全程加权84.08%、第2–20轮88.31%。

账号排空及三次稳定预算后开始，20轮逐笔读取官方CHAT对应Vibe预算，末尾再次三次稳定：剩余276.299927924→276.066600454，净扣USD0.23332747，官方input/cached/output 0.15/0.03/0.50每百万token计价同为0.23332747，逐笔至8位小数一致。input4681567/cached3936384/output6917；预算窗口账号仅本实验20条，无额外生成/检测。官方另一个API预算变化不归入CHAT费用，不将套餐额度净减冒充现金扣款。完整报告research/glm-flash-200k-20turn-billing-20261110.md。

UTC13:46:12已恢复账号/自测Key原group及全部目标字段并logout，正式成功窗口最终全配置除revision外等于初始；exit0/stderr0、所属进程已结束，13:46:34独立核验healthz200/Docker healthy/restart0/无OOM。此前首选账号150秒未排空已自动恢复且未发推理，另有只读curl DNS预检失败均不算有效轮次。未改其他设置/启动PAYG/重启/部署/正文审计；临时产物经同意清理、思考checkpoint只远端。没有生产代码变更，不跑无关全仓；语法检查及真实独立核验通过。Flash中途掉零已复现，但缺真实在线Reader/上游内部证据，核心任务仍in_progress、不归档。

## 最新真实实验：Flash连续20轮（2026-11-10 UTC12:36–12:40）

用户明确授权后完成新的GLM5.3 Flash连续20轮（不是独立20问/旧结果），经过当前f1024e9号池、high/1536、初始25.4K→末轮47.0K输入，完整累积合成思考/回答、同Session/cache/X-Affinity/账号/代理、每轮1attempt200/自然stop。首两轮0，第3–20轮84.61%–93.34%，保留第2轮的热轮加权88.20%、全20轮85.07%；第4轮40.98s且缓存量少1152，未出现中后段归零。20条SSE与请求日志usage一致，41消息checkpoint全部思考/答案哈希及末轮请求历史验证通过；控制器exit0/stderr0/进程已结束、healthy0重启无OOM。相关路由/账号/测试Key/运行未变，完整config hash有外部变化明确披露，不冒称全配置冻结。估算USD0.0381303，未核对budget净扣。不下载思考/checkpoint，不改生产配置/启停账号/重启，无额外A/B或重放；正式证据research/glm-flash-20turn-20261110.md，业务缓存根因仍不结案。

## 最新深查（2026-11-10 UTC10时，只读未改生产）

见`research/ds-glm-deep-20261110.md`：实际f1024e9/NewAPI cb5 healthy/0，两次各120s被动RAM采样23+41条均capture_exit0，独立离线pure-prepare探针退出0/HTTP请求0，136生产Go文件与f1024e9逐项SHA256相等。41条全部稳定有效cache/affinity，31个相邻prepare比较26完整保留/5前缀真实变化/0纯重排；DS明确Session/同号/41工具/旧prepared前缀完整保留，成功97.95%→0.62%→96.27%，与NewAPI及原始usage对齐。另一Session混20/21工具及不同system/首user不能当同前缀；当前GLM只有3条13-token检测，无新版长会话验收。metadata10:15/16/17三分钟与日志r/cp/ck/ckr逐项一致。官方缓存/模型/ThinkChunk说明及价格已直接核对；未改配置、部署、重启、主动推理/重放或续开cacheAudit；观察/文档浏览器均结束。pure-prepare重算不是在线真实Reader/TLS证据，不能100%定责官方；新单Key真实出站诊断仍待授权，核心任务不归档。

## 最新部署验收（2026-11-10 UTC09:43）

用户要求提交推送部署OVH、避免无用操作，f1024e9已推送master上线。复用相关回归、不重复测试；620文件干净源码哈希集合核验，构建7.09秒，完整一致备份后沿线上Compose仅镜像覆盖；191账号/186TH注册/routingSecret/Mistral注册及运行参数保留。新旧内网别名和NewAPI到两个healthz、Docker healthy/restart0/无OOM及真实公网登录/healthz/四资源200与源码SHA256全部通过。正常管理员API仅前置两条精确错误规则及3505重试stop，revision5672；其他配置/凭据逐字段相等，原13硬隔离保留、旧自定义规则不删不改，保存/GET/落盘一致并logout。UTC09:43最终验收并已反馈用户；短窗口无新1910/3505事件，不冒充真实错误重放或缓存改善。专用浏览器关闭，不续采/推理/追加10min观察；正式回退资料见docs/ovh-deployment.md，临时产物经同意再清理。

## 本地修复：1910/3505（2026-11-10，部署前记录）

用户同意建议后，本地新增精确Mistral默认规则：403+1910仅模型冷却24h/原上限内换号/最终503，不扣全局健康；429+3505不扣健康/停止换号/保留429及原Retry-After（秒/HTTP日期，缺失不伪造），返回明确容量错误码。两版冻结旧出厂列表/nil可升级，自定义规则/空/禁用/顺序/备注原样保留；不会覆盖线上现存403/429。真实本地mock先红后绿，internal/rules全包0.675秒、internal/config全包1.083秒、server相关最终normal1.112秒/race1.906秒，定向vet和cmd构建通过；无全仓/race全仓/Node或生产推理。复用干净d986d4b副本隔离注册/Dockerfile未提交改动。初始red.log含一个测试fixture符号校准错误，修正后red-verified.log仅预期行为断言失败；不拿编译错误当有效红回归。最终补充容量标记的实际Mistral来源隔离，其他池即使保存同名rule ID也不获得Mistral特例；最终server normal/race/vet/build均再验通过。本轮未部署/改生产配置，GLM缓存未验改善。线上精确规则落地仍须确认，核心任务不归档。

## 最新只读复查（2026-11-10，优先于下方历史状态）

实际37332be已部署并包含170be84，healthy/restart0。最近2小时403/1910被放大为793次最终502，后线上已有403隔离规则；429/3505为上游后端容量且当前5s自定义规则遮蔽默认Retry-After策略。24h另有123次超过20张图片的400，可做预检但不能删图。GLM3条长请求0%，Flash22条长请求60.37%，低缓存共享Key14条18.76%；所有长样本均早于当前上线，上线后只有13-token短检测，未验证缓存改善。已READ ONLY对账NewAPI成功/异常流分母，未改生产代码/配置、推理、重启或部署。证据及建议见`research/current-review-20261110.md`；核心任务仍in_progress，下一步新自然长会话/精确出站诊断和无损载体范围决策，不能将调查文档当修复交付。

## 真实证据

见research/details-evidence.md：仅自测-test，固定窗口82条摘要、15条原生详情。15/15客户端→实际上游可见头尾在必要兼容差异之外相等，原生/适配/日志usage相同。主组14条同技术会话、同账号，仍有0/14592/14720缓存回退。详情中段截断，未宣称恢复全部历史字节。未修改线上开关、配置、账号或请求，未重放/主动推理/部署。

## 完整本地HTTP回归

新增TestMistralHTTPGrowingHistoryKeepsFullWirePrefix：49个故意倒序工具、非字母序schema properties/required、8轮连续追加、原位developer转换、reasoning/reasoning_content交替、tool_calls与大整数、图片URL/MimeType兼容、约7MiB完整历史。断言真实上游接收的工具/metadata原始字节、root顺序、每条旧prepared消息字节、消息角色/工具关联、完整大tool结果、固定账号和缓存键/亲和头。通过，未发现转发随机重排序或日志cap误截请求。

## 先失败再修复

TestMistralProviderTraceIDReachesClientAndSummary在旧代码下，Mistral-Correlation-Id和X-Kong-Request-Id的JSON/SSE共4种情况同时漏掉下游和摘要；X-Request-Id原有2种情况正常。before-fix退出1（1.211秒），失败仅上述trace断言；顺序回归通过。

修复后Mistral按X-Request-Id > Mistral-Correlation-Id > X-Kong-Request-Id选实际有效值，不修改原生头。单测覆盖空/非法/超长跳过、优先级和其他池不启用fallback；原生cached=0仍保持0。OpenCode buffered只显式传legacy provider参数，原行为不变。

## 一次质量验证

- go test ./internal/server -run 'Test(Mistral|OpenCode)' -count=1：通过，8.575秒。
- go test -race ./internal/server -run 'TestMistral(HTTPGrowingHistoryKeepsFullWirePrefix|ProviderTraceIDReachesClientAndSummary|UpstreamRequestIDPrecedenceBoundsAndIsolation|WireDetails)' -count=1：通过，10.909秒。
- go vet ./internal/server：通过。
- go build -o /dev/null ./cmd/odb：通过，无手工二进制。
- gofmt、git diff --check：通过。
- 未重复无关全仓测试，无npm/pnpm安装或新后台进程。

## 交付边界

已实现的是上游trace诊断缺陷修复及完整顺序回归保护；**没有已验证的缓存命中率优化补丁**。尚无账号池造成重排序的证据，不擅自改消息/工具顺序、丢思考、固定整个Key到一个号或改统计制造高命中。

代码/规格/本任务记录提交Git，不推送或部署，其他窗口注册与Dockerfile改动保持原状。早期排查基线为be14987，追加A/B最近核实为a6d7d38；本地诊断修改未由本任务部署。本轮未再次核实当前生产版本，不把历史基线冒称实时状态。低缓存业务问题未解决，任务不虚假标完成/归档。用户随后批准的6次公开完整payload A/B已执行，见research/controlled-ab-20261108.md：直连3次99.8463%，号池暖样本一次99.8463%/一次0%，不再把“号池链路因素已排除”当定论；第一阶段停在6次上限；用户随后追加批准4次新连接对照，跨夜旧绑定过期，执行前说明改为原账号4次全新连接直连，不改绑定。HTTP trace确认每次新TCP/TLS且未恢复会话，结果为0%（可能冷启动）→99.8463%→99.8463%→99.8463%，见research/fresh-connections-20261109.md。第二阶段停在4次上限；不支持强制新建/单连接补丁，也未完成原截图因果复现。建议转完整出站指纹/连接标识的自然流量取证，不再追加付费猜测。临时审计源码/脱敏结果/测试日志留在已登记的.analysis/cache-order-20261108/，经用户同意才清理；Trellis保留。

## 自然流量完整指纹实现（2026-11-09，本地已验收，未部署）

- 新增mistral_cache_audit*.go：默认关闭/精确Key ID/30分钟128请求；独立进程HMAC覆盖完整body、消息/工具/root与属性顺序、实际Body/GetBody读取、前序观察/并发和Go连接状态。元数据复用既有请求摘要、admin/no-store和日志保留，不打开正文详情、不产生推理。
- 单元/真实本地HTTP mocks覆盖>6MiB正文中段变更、追加/重排/schema属性序/大整数、原生别名准备、相同长度但Reader字节不同的负控、GetBody/Close/部分读取、所有X-Affinity值、来源变化/原生trace/失败重试/零cache、HTTP2 TLS/keep-alive/新连接、异步快照、SSE首帧门控、TTL/并发预算/各内存上限与隔离。关闭路径begin/attempt均断言零分配；已知凭据/身份伪装native trace会在新增字段中省略，不修改响应头。
- 最终代码全仓 `go test ./...`、`go test -race ./...`、`go vet ./...`、`go build -o /dev/null ./cmd/odb` 全通过；9个Node行为套件及所有web JS语法检查通过；gofmt/diff检查通过。回执在.analysis/cache-natural-audit-20261109/。
- 真实本地浏览器以1个假账号/假Key、loopback mock验证请求日志→缓存诊断按钮。receipt.allPassed=true：sample2/128、完整client174251/实际读取174291字节（缓存键补入后的合法差异）、旧消息前缀保留、工具不变、正文读取hash匹配、连接复用、原生假trace、admin/no-store与API往返、详情/额度轮询/OpenCode关闭、无原始正文/凭据/注入节点。2条请求仅发给本地mock，不是生产推理。
- receipt已读取核验1005bytes；专用mistral-cache-audit-local浏览器已关闭，本轮PID81080正常TERM后退出，60065监听关闭。没有常驻采样/新增npm产物，未触碰其他窗口注册/Dockerfile；临时文件仍待同意清理。
- 本阶段仅实现并验收诊断能力，不推送、不部署/开启生产。需要另获部署授权后只对自测-test自然请求收集因果证据；核心缓存低命中仍未解决，不归档或宣称收益。
