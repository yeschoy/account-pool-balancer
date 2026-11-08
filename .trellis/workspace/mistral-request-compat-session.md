# Mistral 参数兼容新工作区会话（2026-11-08 UTC）

- 用户完整交接并授权方案/实现/部署，要求新工作区、独立拥有、不再启动代理。工作区`/Users/lyh_god/orca/workspaces/account-pool-balancer/mistral-request-compat`，分支同名，Orca默认基线319c751包含8d41516；不重置其他分支、不在原master开发。
- 已读本地及原上级AGENTS、共享workflow/身份tanggod、backend/mistral/错误及质量规范、共享思考指南、完整15fdea0诊断证据。共享脚本被repo部分.trellis遮蔽，按用户允许手工维护最小PRD/design/implement/验收记录，没有重搭系统。
- 实现96eaad7：精确DeepSeek Chat developer角色值转换、思考开关语义、enabled默认high、seed→random_seed、无意义默认/标记窄兼容、store严格保护；原值身份与cache/metadata保留。字段跨度拼接解决连续删除/重复字段/大数工具数据无损；明确本地400在账号/RPM之前，失败不删参数重放。Mistral响应逐帧兼容保持不变。
- 官方浏览器复核Chat schema integer|null random_seed、metadata与prompt_cache_key；不将泛schema或mock当具体DeepSeek支持证据。未生产验证seed确定性；有效但不支持的生成/retention语义不静默丢弃。
- Mistral定向/相关回归2.739s通过，自审小改后新增6组参数测试0.584s通过，gofmt/diff check/build通过；没有全仓/race/vet或主动生产推理。首次fixture字段编译错误已修正。
- fetch保留master=e06a589独立Mistral工具更新，无应用代码重叠，正常合并为9f375ef并原子推送功能分支/master；不运行注册工具、不覆盖并发更新。
- 复用原部署脚本仅内存适配，9f375ef干净archive构建9.25秒；完整一致备份、仅镜像切换，06:00:34.988本机与06:01:15.204公网登录页/health/四资源哈希验收通过并立即反馈。healthy/零重启/无OOM；161账号/186注册记录关键资产、routingSecret、完整config及原运行参数保留。旧退出至首HTTP可达观测约5秒，不冒称精确停机。正式备份、rollback及具体时间见docs/ovh-deployment.md。
- 06:00:34.988–06:04:33.034自然窗口24/24真实流式success，NewAPI 24条ok/done，Mistral无错误事件；cache2119808/input3195540=66.34%，16hit/8miss，无溢出/请求内切号/多attempt。该窗口非完整10分钟，不作受控缓存改善结论。
- **未完成项/需要确认**：渠道180最新读取发现同时服务两种GLM模型，不能按原设想移除全渠道operations。本轮没有写NewAPI数据库/配置，旧规则仍删metadata/cache与绕过store=true保护。仅DeepSeek Chat条件化收敛可保留GLM/其他路径，但运行NewAPI ad556799精确源码对象不可用；须确认方案并核实运行条件支持，不能盲加条件、整行覆盖或先清空规则。任务维持in_progress等待此边界确认，不虚报整体交付完成。
- 临时产物登记当前`_tmp_artifacts.md`（ignore）：公开文档自动snapshot与本地odb构建；未下载生产原始请求/凭据，未经同意不清理。Trellis/正式备份/业务资料保留。后续仅补记录，不重新部署文档提交。

## 用户授权清理、合入master及部署复核

- 用户明确要求“清理吧，合入master，之后部署到ovh中”。按本轮登记清单精确删除当前工作区`odb`（16653938字节）、公开文档自动快照`pi-agent-browser-snapshot-b488fa98c27a25e6.json`（38564字节）及`_tmp_artifacts.md`（686字节），逐项确认不存在。没有清理历史任务产物、Trellis、生产数据、正式备份、发布源码或新旧镜像。
- fetch确认origin/master仍1d746ef，已包含96eaad7与9f375ef；主目录master为e06a589、工作树干净且未分叉，可仅用`--ff-only`同步最新记录，不在主目录开发、不reset/强推。
- OVH只读预检：仍运行`9f375efee5a89e92dcbe5f14dd0385e4519c1501`，原镜像ID未变；Balancer/NewAPI均healthy、零重启、无OOM，healthz200、数据挂载不变，旧e6f063b树干净、磁盘285.6GiB余量。Git核对运行提交与远端master仅差docs/.trellis记录，全部应用代码一致；依据部署约定复用既有上线验收，**无需重新构建/部署或重启**。本轮未产生停机、推理或生产配置变更。
- 清理/合并/部署授权不扩大为条件化NewAPI规则修改；渠道180规则仍未动，原范围确认门保留。仅提交此清理/复核记录，不因文档版本变化再次部署。

## 用户要求监控约10分钟错误率

- 2026-11-08 06:14:51.458–06:24:51.458 UTC完整600秒，完成请求口径（ts+durationMs），不计未完成请求。Balancer245请求/228成功/17失败，错误率6.94%，客户端取消0；全部失败是Modal上游429（17/89，19.10%）。OpenCode Go96/96成功。
- Mistral精确DeepSeek60/60 HTTP200、错误率0%，57流式+3非流式，无新增400/422/流式错误；NewAPI渠道180同窗口60条消费、57条ok/done，无错误记录。Mistral真实input10055835/cached8382592=83.36%，54hit/6miss，无忙溢出/请求内切号/多attempt。Modal42条尝试级429不能混作42个失败请求。
- 38次健康采样无异常，结束时两服务healthy/零重启/无OOM，运行revision9f375ef。5分钟时已反馈Mistral30/30及Modal4次429，10分钟完成即反馈最终结果。采样健康不冒称连续探针，缓存窗口不当受控提升证据；详见validation.md。
- 全程SSH/远端内存只读，递归错误解码只输出状态/loc/type；不发推理、改配置/规则或重启，无新增临时文件/日志下载/凭据副本。只提交观察记录，不重新部署；原NewAPI范围确认门保留。

## 再次要求观察并修复：422诊断补丁及并发部署阻塞

- 06:48 UTC预查发现Mistral上游503/422，暂停空等并分析。503有上游internal_server_error/Service unavailable证据；422错误体恰64KiB、通用reason，无法断言是请求适配导致。
- 已修复确定的Balancer诊断缺口：7b652c9新增有界、隐私安全的mistralValidation loc/type及真实responseBodyTruncated，原转发前缀/状态/重放/健康策略不变。定向Mistral回归3.744s，最终两组诊断1.022s，集成并发master后2.969s，gofmt/diff check/build通过；无额外生产推理。
- 发布guard发现运行revision已被另一个操作改变，构建/停服/切换前安全中止。观察到06:56:27启动无revision latest镜像，后因缺wget健康检查unhealthy（healthz200），又被另一个操作恢复原9f375ef健康镜像；这些切换不是本会话执行的。补丁尚未上线，不能报422已解决。
- 正常保留master并发注册UI、工具及wget修复8ad33b5，合并成65f2680；没有启动其他代理，仅更新当前Orca卡片阻塞说明。发布必须先协调，不能两个窗口反复覆盖。
- 完整日志窗口06:48:29.015–06:58:29.015 UTC：Mistral170完成/143成功/26失败/1取消，错误率15.29%；失败13上游503+13未知422。NewAPI对应26错误/144消费，143ok/done+1ok/eof，不能把消费144当完整成功144。跨独立部署，不是补丁单版本验收；详细证据/归因边界在diagnostic-20261108.md。
- 中止发布的源码release和预建备份目录登记新的_tmp_artifacts.md（ignore），未经本轮清理同意不删；无原始请求/错误正文/凭据下载。待用户协调暂停另一发布后再统一部署及自然流量定位422；原NewAPI范围门保留。

## 后续解决422及继续排查503（217bf64已上线）

- 用户继续要求422解决并追查503。未止步于JSONL/SQL通用错误：从NewAPI Docker stderr有界body preview恢复14条真实`extra_forbidden / body.messages.4.assistant.reasoning`，input均string，确认遗漏的是历史assistant.reasoning别名；只在远端内存解析loc/type/形态，没有输出/下载正文。
- 49e8aa0在精确DeepSeek Chat上将该别名无损映射原生thinking chunks；同值去重、空值让位、冲突/非法/重复本地明确拒绝，保留顺序、原答案、工具/ID和大数，其他模型/路径/角色不变。Mistral定向3.098s、合入最新明文注册/UI后2.574s、gofmt/diff check/build通过。
- 严格保留并发新注册UI/明文存储改动，确认线上资源与合并源码一致，以当前精确image+container ID重新建立guard，不假称旧无revision镜像有可信Git版本。217bf640b0c30e4fd4a1662977c33b02e36858ae已正常推送并上线，发布前TH/Mistral队列无活跃任务，完整一致data备份，161账号/186注册记录/明文注册文件/完整config及运行参数保留。
- 构建45.70秒；07:29:01.871本机、07:29:37.106 UTC真实未登录公网health/四资源SHA验收通过并立即反馈。回退保留发布前明文版本，不用9f375ef抹新数据；时间/镜像/备份见docs/ovh-deployment.md。
- 07:30:15.069–07:40:15.069 UTC完整10分钟：88完成/85成功/无422、2次上游503/code3800（4尝试），1次原600秒首响应等待超时504；取消0，总错误率3.41%。83流式成功；NewAPI84消费/3错误/82ok-done，按边界如实报告，不硬凑85。
- 503的3800/internal_server_error/Service unavailable分布10个有成功的账号；此前31次最终失败均原有两次尝试、第二次无成功样本。两段150秒被动采样17/20条均HTTP200，没抓到失败请求形态，不能臆断某档位/大输出预算必错。官方文档只给服务暂不可用与有界退避建议，未证明3800细根因；503仍未根治，不伪称本地已修好、不盲增尝试或重启。
- 504那条07:28:37开始、600022ms后触发原firstEventTimeoutSec=600，单尝试、无切号；不擅改10分钟上限。36次健康采样均无异常、结束仍217bf64/healthy/零重启/无OOM；成功cache11713152/input14331638=81.73%，绑定73hit/15miss、无忙溢出。
- 仅自然业务，没有主动生产推理、历史请求重放、NewAPI规则/账号/TTL/限流修改。专用浏览器/内存被动采样已关闭；未新增本地构建/敏感日志产物，中止发布清单仍待同意清理，Trellis/正式备份保留。

## 2026-11-08：用户授权未传思考参数时默认 high（本地修改，未部署）

- 用户在确认已有 thinking 开关兼容后，明确要求缺省时传 high。本次是小修改，不新建或归档其他 Trellis 任务。
- 仅 Mistral Chat 精确模型 `deepseek-v4-1-flash-1017` 在 `reasoning_effort` 与两种 thinking 开关均缺失时补 `reasoning_effort:"high"`。显式关闭、原生档位、无开关时的显式 null、现有冲突校验保持不变；不改其他模型/协议/号池、max_tokens、调度或线上配置。high 可能增加思考用量和耗时。
- 新增回归先确认旧实现缺省未补 high，再验证修复；本地 Mock 覆盖缺省/开启/关闭/none/low/high/xhigh/max/null、原始工具/大数/历史/cache/metadata/输出预算保留及单次真实 HTTP 转发。扩充缺省参数的其他模型/协议/号池隔离，调整既有精确字节断言以纳入授权新增字段；Mistral 定向通过（3.122秒）。
- 质量门禁：`go test ./...`、`go test -race ./...`、`go vet ./...`、`go build -o /dev/null ./cmd/odb`、gofmt 与 diff check 均通过；同步 Mistral 规范及使用文档。
- 本次新增代码尚未推送或部署，未发起生产推理。本地构建输出到 /dev/null，没有新增需清理的业务无关临时产物；此前清单不擅自清理，Trellis 记录保留并随修改提交。

## 2026-11-08：用户授权 Chat 的 reasoning.effort 别名兼容（本地修改，未部署）

- 用户明确要求把嵌套 `reasoning.effort` 自动转为正确的原生字段 `reasoning_effort`；沿用精确 Mistral DeepSeek Chat 边界，不转换 Responses/FIM、其他模型或号池，不新建任务。
- 只接受根 `reasoning` 中唯一的 effort 字段，复用原生档位校验及 medium→high；两种写法归一化后相同才合并，不同值（含显式 null 对非 null）本地400。未知结构、summary/budget等额外字段、根或嵌套重复字段均拒绝，不静默丢弃意图，不回显字段值。与 thinking 开关/缺省 high 的既有语义一致，保留 none/low/null，不让默认值盖掉显式选择。
- 先新增回归复现旧实现把嵌套 low 当成缺省、保留未知根 reasoning 的行为，再修复；最终只写一次原生字段，避免追加式 wire setter 产生重复 effort。历史 assistant.reasoning 别名仍独立处理，原始消息/工具/大数/缓存/metadata/输出预算保留。
- Mock 覆盖所有档位/null、相同/归一化/转义别名、开关冲突、无损拼接、缓存补齐及一次上游 HTTP；Responses 含原生 effort+summary 的正文经 HTTP 保持原样，其他模型/协议/号池隔离通过。定向 Mistral 回归通过；全仓 normal（server63.862秒）、race（server79.071秒）、vet、构建到 /dev/null、gofmt 与 diff check 均通过，规范与使用文档同步。
- 本次及此前缺省 high 均尚未推送/部署，不宣称线上已生效；未发生产推理或修改 NewAPI 配置。无新增业务无关临时产物，旧清单不擅自清理，Trellis 记录随代码提交并保留。

## 2026-11-08 12:37 UTC：用户授权推送并部署到OVH（4f8a680）

- 用户要求提交推送部署、不要过多测试。master已正常推送4f8a680，包含4f8d74e额度/路由、efe35ce缺省high和4f8a680嵌套effort兼容；不重复此前通过的normal/race/vet/build，不改NewAPI配置或在线功能开关、不发生产推理。
- 远端仍aaa09a6时完成一次合并预检；git archive传输命令超时后先检查结果，逐一核对471源码文件SHA256才构建，保留旧分叉工作树。复用既有部署脚本仅内存适配精确container/image/revision守卫、TH/Mistral任务空闲和明文注册库资产核对；构建7.21秒，完整一致备份后以线上Compose仅覆盖image切换，未使用裸docker run。
- 12:37:20.727本机验收：运行4f8a680、Docker healthy/重启0、healthcheck与全部运行参数/完整config保留，191账号/186条TH注册记录核心资产/routingSecret/Mistral明文注册库一致。ai-internal两个别名均保留，从new-api访问双healthz都成功。
- 12:37:48.902真实未登录公网登录页、healthz及四静态资源200，?v=a5bb5ebec0与源码SHA256一致；立即告知用户，独立浏览器关闭。正式release、回退镜像及备份/阶段时间详见docs/ovh-deployment.md，未将6.72秒首HTTP观测间隔当成精确停机。
- 两项思考兼容已部署，但未做新的生产推理对照；额度功能也已部署，本轮未做生产管理台预算刷新/保存往返。上方未推送/未部署仅为当时历史状态，以本节为准。无新临时脚本或敏感下载，旧临时产物待同意清理，Trellis保留；后续只补记录，不重新部署。
