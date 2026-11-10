# 验证与交付状态

## 最新本地修复：1910/3505（2026-11-10，未部署）

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
