# OVH 部署与回滚记录

## 后续部署执行约定（用户确认，2026-11-04）

上次整体交付约 20 分钟，但实际构建约 8 秒，离线预检约 2 秒，切换至确认
健康约 33 秒。主要可避免耗时是现场编写多套脚本、重复检查、资源路径试错
以及上线后的文档收尾。不能把这些时间归咎于 OVH 或 Docker 性能。

后续按以下固定顺序执行，不重新展开一套部署系统设计：

1. **一次预检**：合并检查目标容器、运行 revision、工作树/分叉、数据挂载、
   磁盘和健康。先读本文件，已知的缓存历史不要重复做全仓大输出；用限定
   路径或汇总差异确认。若已是目标版本且健康/资源匹配，直接报告无需部署。
2. **构建**：从明确提交的干净快照构建固定标签镜像并设置 revision；保留旧
   镜像。不重复跑同一提交已经通过且不受部署环境影响的完整测试。首次升级
   或有数据兼容风险时保留断网副本启动预检；普通更新不无故复制敏感数据。
   预检容器必须复用线上 `Config.User`（当前1000:1000），不能假定镜像默认
   用户可读原权限副本。注册库核对按 `server.go` 将 routingSecret 作hex解码，
   不猜测成base64；解密仅在远端内存完成。
3. **备份切换**：在新镜像就绪后才进入停机窗口。按变更风险保留一致、完整
   的数据备份；只更新目标服务，保持卷/端口/网络、配置和功能开关不变。
   不自动用旧数据覆盖上线后的新写入，失败时先确认结果再执行既定回退。
4. **一次验收**：合并核对健康、运行镜像 revision、必要的数据/密钥/设置
   保留与新版资源。本地检查走既有接口；静态脚本实际路径是
   `/assets/app.js`，优先从 HTML 读取，不猜 `/app.js`。资源URL带`?v=`时，
   HTTP请求保留查询串，映射源码文件时使用URL解析后的path。公网 Python 客户端
   的既有 403 不应误判为新版本故障；不改 WAF、不伪装身份或轮换出口，
   用正常真实浏览器校验可访问页面，遇到挑战如实报告而非反复尝试。

**反馈与时间预算**：缓存命中、无异常的单服务部署以 2–5 分钟为目标，
不是硬性 SLA；超过 5 分钟主动说明具体卡点。构建、停机切换、健康确认、
公网验收分别记录时间。必要验收通过立即告知用户，再补最小部署记录，
不要让写文档或提交耗时变成用户等待上线结果的时间。只改记录不重新部署。

## 当前部署：Mistral DeepSeek assistant reasoning 兼容（2026-11-08 07:29 UTC，217bf64）

- 用户要求解决422并排查持续503。已从NewAPI Docker stderr的有界body preview恢复14条`extra_forbidden / body.messages.4.assistant.reasoning`，input均string；之前Balancer JSONL与NewAPI SQL只剩通用错误，不能据此声称完全无证据。解析仅远端内存，未输出/保存正文或凭据。
- 修复提交`49e8aa0`：仅精确DeepSeek Chat把assistant reasoning字符串/null作为reasoning_content别名，无损保留为原生thinking chunks；相同别名不重复、冲突/非法/重复字段本地拒绝，保留消息顺序、原答案、工具/ID/大数，其他模型/协议/角色不变。包含此前`7b652c9`的安全loc/type与真实截断诊断；不改重试次数/账号/TTL/限流/NewAPI规则。
- 正常合并并保留并发注册UI、Docker wget与用户要求的明文mistral-signup.json，集成版本`217bf640b0c30e4fd4a1662977c33b02e36858ae`已推送master/功能分支；没有用旧AES源码覆盖线上明文存储。定向Mistral回归3.098s、集成后2.574s，以及gofmt/diff check/必要build通过，不跑重复全仓/race/vet，不主动生产推理。
- 预检将无revision的当前运行镜像作为**精确image+container**回退基线（image`sha256:9f476a18df96b6ab2957ad7830ae8295fb6ee0ffab9a6b9a4c807e410d41e833`，不假称它对应某Git版本）；核对线上四资源SHA256与已合并源码一致、healthy/零重启、磁盘276.8GiB余量、注册无queued/running后才发布。先前因并发切换中止的7b652c9发布没有被冒称成功。
- 干净git archive源码`/home/ubuntu/project/account-pool-balancer-releases/20261108T072646Z-217bf64`，固定镜像`account-pool-balancer:release-217bf64-20261108T072646Z`，ID`sha256:67e3ac3a5511572e52cdbd06f6cd7b17daca093add57ee1e7f32a4a53175f499`。复用既有脚本仅内存适配基线guard/明文注册库摘要；无存储格式变化，不额外复制敏感预检数据。完整一致备份后仅image覆盖切换，**161账号、186注册记录核心资产、routingSecret、完整config、Mistral明文注册库及原用户/环境/卷/端口/网络/健康/资源参数保留**。
- 07:29:01.871本机验收，07:29:37.106 UTC真实未登录公网登录页/healthz/四资源200、保留?v=030230089e且SHA256逐项匹配，通过即反馈。healthy/零重启/无OOM，运行revision恢复可追溯。
- **上线后完整10分钟观察**：07:30:15.069546–07:40:15.069546 UTC（北京时间15:30:15–15:40:15），按完成时间88请求、85成功、3失败，错误率3.41%、取消0；**无422/本地400**。失败为2次官方Mistral503/code3800（4条上游尝试事件）及1次首响应等待达到原600秒上限的504（单次、未换号重放）。83条流式成功；NewAPI同窗口84消费/3错误、82ok/done，完成时间与NewAPI记账时间边界不同，不强行凑齐85成功。成功input14331638/cached11713152=81.73%，绑定73hit/15miss，无忙溢出；只有2次503沿既有流程切号/两attempt。36次健康采样无异常，结束仍217bf64。历史错误请求未重放，不把无新422等同于原14条原样实测完成。
- **503仍未根治**：已证实是api.mistral.ai返回3800/internal_server_error/Service unavailable，遍布10账号且各账号也有成功；此前31个最终503的第二次尝试没有成功样本。两段150秒被动形态采样分别17/20条全200，未捕获503请求形态，不能臆断具体预算/档位触发原因或以重启、加重试、静默丢参数宣称解决。仍需上游服务侧排查，不声称Balancer能保证消除第三方503。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 07:27:40.942–07:28:26.644（45.70秒） |
| 停止请求 / 旧进程退出 | 07:28:26.731 / 07:28:26.847 |
| 完整一致备份及核验 | 07:28:27.784–07:28:31.222 |
| 切换请求 / 新容器启动 | 07:28:31.222 / 07:28:31.434 |
| 首HTTP可达 / Docker healthy | 07:28:32.539 / 07:29:01.772 |
| 本机验收 / 公网验收 | 07:29:01.871 / 07:29:37.106 |

未采样精确停机；旧退出至首HTTP可达观测间隔约5.7秒，不把构建/健康等待当停机。受限正式备份`/home/ubuntu/backups/account-pool-balancer/20261108T072646Z-mistral-reasoning-217bf64/`保留完整data/原Compose与.env/日志/回执，data SHA256`2da52fa9275f1a13dbaa10f377fa3068401baf84002bfd0fa116edc70f73963d`；rollback镜像`account-pool-balancer:rollback-20261108T072646Z`保留发布前明文存储版本，不用9f375ef覆盖新注册数据。正式备份/源码/镜像及Trellis保留；中止发布的临时产物仍按_tmp_artifacts.md待同意清理。仅补记录不再次部署。

## 历史部署：Mistral 注册机改用明文存储（2026-11-08 15:25 UTC，1b253bf）

## 当前部署：Mistral 批量账号 + 代理轮转 + 自动入池（2026-11-08 16:22 UTC，a225c5c）

- 用户三项要求已实现：
  1. **代理列表一行一个，账号按顺序分配，代理可重复使用**：
     `PickProxyByIndex(n)` / `ProxyPool.by_index(n)` 取 `n%len`；候选记录
     `proxy_index`，worker 用该序号经本地网关连上游。
  2. **iCloud 账号列表批量导入**，每行 `账号----接码地址`：
     新增 `POST /api/mistral/candidates/bulk`，逐行校验并回报被拒行
     （缺分隔符/空值/邮箱格式/重复）。
  3. **注册成功后自动加入 Mistral 账号池**：
     候选带 `import_to_pool`；成功后 `importMistralCandidate` 写入
     `config.Accounts`（Provider=mistral、Group=mistral），拒绝重复 Key；
     列表新增「池」列显示 已入池/待入池/—。
- 前端表单由单账号改为批量 textarea，新增入池勾选；代理提示说明轮转规则。
- 验收：批量导入 3 条成功、坏行被拒；`proxy_index` 0/1/2 正确落盘并返回
  （`omitempty` 导致 idx=0 被省略，已修）；容器内 3 个代理按
  `0→a,1→b,2→c,3→a,4→b` 轮转；测试候选已清理为 `[]`；
  池内现有 mistral 账号 10 个、总计 161。
- 镜像 `account-pool-balancer:release-fix-20261108T081928Z`；仅
  `--force-recreate` Balancer。仍未验证：容器内真实注册与真实入池。

## 当前部署：Mistral 支持完整代理 URL（2026-11-08 15:55 UTC，2934598）

- 用户要求支持标准格式 `socks5://用户:密码@IP:端口`，不再局限于 zooproxy 模板。
- `config.MistralSignup` 新增 `proxyUrls`：填了即优先使用，忽略模板字段；
  `PickProxy`/`AllProxies` 统一两种来源。支持 socks5/socks5h/http/https。
- worker 注入 `MISTRAL_PROXY_URLS`（换行分隔）；`Proxy.from_url()` 直接持有
  完整 URL；`chain_gateway` 按上游 scheme 选择 SOCKS5 握手或 HTTP CONNECT
  （此前只实现了 HTTP CONNECT）。
- 前端：「代理列表」textarea 置顶优先，模板方式折叠进 `<details>`（高级）。
- 验收：`PUT` 保存 `proxyUrls` 后 `revision=5456`、`hasProxy=true`；
  `POST settings/test` 返回 `{"ip":"15.235.122.152"}`，与配置的出口一致；
  页面含 `ms-proxy-urls` 与格式说明，资源 `app.js?v=9c5ce31094`；
  容器 running/healthy。本机网关直连该 SOCKS5 出口亦为 `15.235.122.152`。
- 镜像 `account-pool-balancer:release-2934598-20261108T075416Z`；仅
  `--force-recreate` Balancer，卷/端口/网络未变。
- 仍未验证：容器内真实注册全流程；订阅扣款。

## 当前部署：Mistral 代理设置移到界面（2026-11-08 15:44 UTC，cfbc02a）

- 用户要求代理凭据在界面配置，而不是写进容器环境。新增
  `config.MistralSignup`（用户名模板/密码/SID 列表/入口/地区/接码代理），
  持久化在 `config.json` 的 `mistralSignup` 键；与 `mistral.go` 里 API 池的
  `Mistral`（BaseURL/routing）命名区分，未改动后者。
- worker 不再依赖容器环境变量：`runMistralWorker` 每次执行时把界面里的设置
  注入 `ZOOPROXY_*` / `MIDA_PROXY` 子进程环境。
- 新增 `GET/PUT /api/mistral/settings` 与 `POST /api/mistral/settings/test`
  （后者用配置的首个 SID 走一次 ipify，只验代理不跑注册）。
- 前端新增「代理与运行设置」表单 + 「测试代理」按钮。
- 验收：`PUT` 保存后 `revision=5454`、`hasProxy=true`；`POST settings/test`
  返回 `{"ip":"64.66.229.122"}`（sid q4h1WBwP）；`/data/config.json` 中
  `mistralSignup.proxySids` 3 条、`proxyPassword` 已写入；页面含
  `mr-settings-form`/`ms-user-tpl`/`代理与运行设置`，资源
  `app.js?v=a1370297d1`；容器 running/healthy。
- 镜像 `account-pool-balancer:release-cfbc02a-20261108T074202Z`；仅
  `--force-recreate` Balancer，卷/端口/网络未变。
- 仍未验证：容器内真实注册全流程；订阅扣款。

## 当前部署：Mistral 注册机改用明文存储（2026-11-08 15:25 UTC，1b253bf）

- 用户明确要求去掉加密存储。`mistralsignupstore` 由 AES-GCM 改为明文 JSON：
  文件 `/data/mistral-signup.json`，权限 0600，`cat` 可直接读出邮箱、密码与
  API Key。`Open(path, secret)` 保留 secret 参数位置以兼容调用方，实际不再
  使用；删除 `encrypt/decrypt/gcm` 与 `Public()` 遮罩方法。
- 前端候选列表直接显示完整 Key 并可点击复制；新增 `.mr-*` 样式。
- 验收：登录后 `POST /api/mistral/candidates` 返回 201 结构，落盘文件
  `-rw------- odb odb`、`rows` 内含明文 password；`DELETE` 后 `rows: []`。
  容器 running/healthy、restarts=0、healthz 200。
- 镜像 `account-pool-balancer:release-1b253bf-20261108T072132Z`，构建复用
  缓存；仅 `--force-recreate` Balancer 单服务，卷/端口/网络/配置未变。
- 回退镜像沿用 `account-pool-balancer:rollback-20261108T065425Z`（9f375ef）。
- 仍未验证：容器内真实注册（未配置 ZOOPROXY_* 代理池环境变量）；订阅扣款。

## 当前部署：Mistral 注册机界面骨架（2026-11-08 15:05 UTC，8ad33b5）

- 用户要求先做「界面壳子」：把命令行注册机变成管理页面可操作的形态。新增
  `mistralsignupstore`（AES-GCM 加密存储，与 perchsignupstore 同构）、
  `mistral_registration.go`（6 个 admin API + 串行 worker 调度）、
  `tools/mistral/worker.py`（stdin/stdout JSON 契约）、前端「Mistral 注册机」
  页面（候选列表 / 状态 / 开始 / 取消 / 删除）。**未接入订阅扣款**，就绪状态
  仅代表三把 API Key 生成成功。
- 基础镜像由 alpine 改为 debian:bookworm-slim：Playwright 官方 Chromium 为
  glibc 编译，musl 下无法运行。镜像由约 60MB 增至 2.01GB。首次切换因
  Debian 未预装 `wget` 导致 compose healthcheck 失败（`executable file not
  found`），服务本身正常；已回退旧镜像恢复，补 `wget` 后重建通过。
- 构建 1m7s（首次）/ 复用缓存后重建；一次预检原 9f375ef 健康、余量 287GiB。
  源码为干净 git clone：`20261108T065139Z-8b701f0`，镜像
  `account-pool-balancer:release-8ad33b5-20261108T070000Z`。
- 切换仅 `--force-recreate` Balancer 单服务；卷/端口/网络/配置/功能开关不变。
  数据目录挂载 `data:/data` 保留，9 个既有 JSON 配置在，未改动。
- 验收：容器 running/healthy、restarts=0、healthz 200；登录后
  `/api/mistral/status` 返回 `worker_found=true`，
  `python=/opt/mistral-signup/bin/python`；`/api/mistral/candidates` 空列表；
  `index.html` 含 `page-mregister`、`app.js` 含 Mistral 逻辑；资源版本
  `app.js?v=f4bb4a3c6e`。
- 未登录访问 `/api/mistral/*` 与 `/api/perch/*` 均为 401，行为一致。
- 回退镜像 `account-pool-balancer:rollback-20261108T065425Z`（原 9f375ef）。
  备份 `/home/ubuntu/backups/account-pool-balancer/20261108T065425Z-mistral-ui-8b701f0/`
  （0700）保留 compose 与原 .env。
- 管理页面仍只监听 `127.0.0.1:8790`，需 SSH 隧道访问，未新增公网入口。
- Playwright Chromium 与系统库已装进镜像（`/opt/ms-playwright`），但容器内
  尚未实跑注册（未配置代理池环境变量）；真实注册验证仍缺。

## 当前部署：Mistral DeepSeek Chat 参数定向兼容（2026-11-08 06:01 UTC，9f375ef）

- 用户明确批准在新工作区独立实现、修复后部署且不再启动代理。功能提交`96eaad7`，集成版本`9f375efee5a89e92dcbe5f14dd0385e4519c1501`；从Orca默认基线319c751（包含8d41516）开发，fetch后正常合入master的e06a589独立工具/诊断更新，再非强制原子推送功能分支与master。没有在原master开发、reset分叉或运行注册工具。
- 仅Mistral Chat精确`deepseek-v4-1-flash-1017`新增developer→system、thinking/enable_thinking到原生effort（enabled无档位/null默认high、disabled为none，冲突/预算/未知结构本地拒绝）、seed整数无损映射random_seed、空默认窄省略及兼容标记处理。保留medium→high、reasoning_content、逐事件流式内容数组及store无状态保护，不改其他模型/Responses/FIM/供应商的新增参数行为、identity优先级、Nginx/调度/TTL/并发/限流/账号策略。
- root user/safety_identifier仅string/null兼容标记，不提供上游追踪/安全语义，不用于会话身份；有效但无法保真的logit_bias/verbosity/retention本地明确拒绝。prompt_cache_key/metadata原值保留。官方Chat schema支持random_seed，但本DeepSeek型号的支持及确定性仍未生产验证；不以mock冒充证据、不在失败时删seed重放。
- 全部Mistral定向测试及相关既有回归通过（2.739s），小幅自审后新增6组参数测试通过（0.584s），gofmt/diff check/必要build通过；未重复全仓/race/vet或主动生产推理。构建源码为干净git archive：`/home/ubuntu/project/account-pool-balancer-releases/20261108T055905Z-9f375ef`，镜像`account-pool-balancer:release-9f375ef-20261108T055905Z`，ID`sha256:6716715f22121e95eb877f96d930a509f7c3afd6490275e5cee2d0404c40e39d`。
- 一次预检8d41516健康/零重启、旧e6f063b工作树干净、磁盘287GiB余量；复用既有`.analysis/th-maintenance/deploy-checkout.py`仅内存适配版本/备份名，不新建部署脚本或复制敏感预检数据。本轮无存储格式变化。新镜像就绪且无queued/running注册任务才停旧进程，退出0后完整一致备份data，gzip/SHA256核验后只追加image覆盖切换Balancer。**161账号、186注册记录核心资产及凭据/Key/代理/支付/备注、routingSecret、完整config与原用户/环境/挂载/端口/网络/资源/健康策略均保留。**
- 本机健康/版本/四资源通过，healthy、零重启、无OOM；真实未登录公网登录页正常，healthz与四资源均200，保留`?v=e02f22dc28`且SHA256逐项匹配。06:01:15.204 UTC公网验收通过并立即反馈；不把服务/资源验收当成真实推理验证。
- **初步自然业务观察（非完整10分钟）**：06:00:34.988–06:04:33.034 UTC约3分58秒，真实DeepSeek完成24/24 success/HTTP200，全流式；NewAPI对应24条消费，全部stream_status=ok/end_reason=done。Mistral错误事件0、无新422/本地400、无忙溢出/请求内切号/多attempt。真实input3195540/cached2119808，token加权66.34%；绑定16hit/8miss，均message_hmac身份/session_hmac cache hint。重启清内存绑定，不能把miss当成同会话一直换号；未恢复客户端原始身份字段，不能声称缓存已改善或所有新参数均已生产实测。
- **用户要求的完整10分钟错误率观察**：06:14:51.458–06:24:51.458 UTC（北京时间14:14:51–14:24:51），按完成时间统计245请求，228成功/17失败，错误率6.94%，客户端取消0。其中Mistral精确DeepSeek **60/60成功、错误率0%**（57流式+3非流式），无新增400/422或流式错误；NewAPI渠道180一致60条消费、57条stream_status=ok/end_reason=done、无错误记录。OpenCode Go96/96成功；全部17次最终失败为Modal上游429（89请求中19.10%），尝试级错误事件42条不能当42个失败请求。Mistral真实input10055835/cached8382592，token加权83.36%，绑定54hit/6miss，无忙溢出/请求内切号/多attempt；不据此承诺缓存改善。38次容器健康采样均无异常，结束时两服务healthy/零重启/无OOM，仍运行9f375ef。仅自然流量只读监控，无主动推理、配置/规则写入或重启；详细口径见`.trellis/tasks/mistral-request-compat/validation.md`。
- **NewAPI渠道180规则暂未修改**：上线后读取最新配置发现它同时服务两个DeepSeek别名与`glm-5.3`/`glm-5.3-flash`（映射zai-glm-5-3/zai-glm-5-3-flash）。直接删除10项全渠道operations会影响未适配的GLM及其他路径，超出精确范围。先保留原规则/header通配/路由/Key与正式历史备份；待确认并核实运行版本可可靠按模型+Chat条件收敛后，才备份最新值、逐项停用目标范围的对应操作，不整行覆盖或清空规则。现有规则仍会删cache/metadata及绕过store=true保护，此限制尚未解除。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 05:59:51.182–06:00:00.435（9.25秒） |
| 请求停止 / 旧进程退出 | 06:00:00.526 / 06:00:00.640 |
| 完整一致备份及核验 | 06:00:00.921–06:00:04.312 |
| 切换请求 / 新容器启动 | 06:00:04.312 / 06:00:04.517 |
| 首次HTTP可达 / Docker healthy | 06:00:05.643 / 06:00:34.883 |
| 本机验收 / 公网验收 | 06:00:34.988 / 06:01:15.204 |

未采样精确停机；旧退出至首HTTP可达观测间隔约5.0秒，不将整体执行或健康等待误报为停机。受限正式备份`/home/ubuntu/backups/account-pool-balancer/20261108T055905Z-mistral-params-9f375ef/`保留完整data、原Compose/.env、构建/切换日志与deploy.json；data SHA256`1ade70b278d8a756293d09aac6cb87bba9fee93513492612d61f980dcaa60719`。回退镜像`account-pool-balancer:rollback-20261108T055905Z`保留8d41516；回退前核查新写入，不用旧data覆盖在线变化。临时产物登记当前工作区`_tmp_artifacts.md`，未经同意不清理；Trellis/正式备份保留。仅补记录不再次部署。

## 历史部署：Mistral DeepSeek 无状态 store 兼容（2026-11-07 21:23 UTC，8d41516）

- 用户要求观察中发现错误则修复、部署并重新观察10分钟。02c61a8窗口内已有真实流式DONE/completed且缓存44416/44816（99.11%），同时另一请求出现上游422 `extra_forbidden / body.store`。`8d41516407abe2f560b5d0f832e058019c7b9877`已正常推送master：仅精确DeepSeek Chat模型省略root store=false/null，true/非法/重复store本地拒绝，保留nested metadata.store；不假装支持持久化，不承诺上游平台的数据留存政策。保留此前medium→high及逐帧内容块兼容，不改账号调度或NewAPI配置。
- 只跑Mistral定向Mock测试、gofmt和必要build，未重复全仓/race/vet或主动生产推理。一次预检原02c61a8健康，旧e6f063b工作树干净、余量286.5GiB；源码为干净git archive：`/home/ubuntu/project/account-pool-balancer-releases/20261107T212114Z-8d41516`，镜像`account-pool-balancer:release-8d41516-20261107T212114Z`，ID`sha256:66ddacf96e36ce5ce1b68ed340b6606eb65fac084e65811f3ca418541ca088ef`。
- 复用既有脚本，仅内存适配；无存储格式变化，不额外复制敏感数据预检。新镜像就绪且无queued/running注册任务才停止旧服务，退出0后完整一致备份data并gzip/SHA256核验，仅image覆盖更新Balancer。**161账号、186注册记录核心资产及凭据/Key/代理/支付/备注、routingSecret、完整config和原用户/环境/挂载/端口/网络/资源/健康策略均保留**，未改在线功能开关。
- 本机healthz/版本/四资源通过，healthy/零重启/无OOM；真实未登录公网登录页正常，healthz/四资源均200，保留`?v=e02f22dc28`资源SHA256逐项匹配；21:23:09.909 UTC公网验收通过。上线后已立即反馈；专用浏览器关闭，最终10分钟自然业务观察重新计时为21:23:09.909–21:33:09.909 UTC，不将中断的旧观察冒充完整窗口。
- **完整10分钟观察完成**：约每15秒轮询（40次），两服务始终healthy/零重启/无OOM；NewAPI与Balancer一致确认Mistral10次成功，其中6次流式正常DONE/completed、4次非流式，零Mistral错误，未再见content数组解析/medium/store错误。结束后只读SQL复核计数一致，没有额外推理或再次部署。
- 缓存为真实input912330/cached124288，token加权约13.62%，2/10请求有缓存、3/10绑定命中。此前99.11%是单次请求，不是全池命中率；**整体缓存仍偏低，不能宣称已整体改善或保证命中**。缺少稳定显式会话ID、新绑定/不同前缀/上游缓存路由仍需区分，不改调度或删除提示词来虚增数字。
- 同窗口Modal有11次最终上游429（28条上游429尝试）、15次成功；OpenCode25次成功（NewAPI19条流式正常DONE/6条非流式）。另有独立cctoken渠道164的一条response.failed及cline-pass-console渠道71两条错误，不经过本Balancer；不将它们掩盖成零错误或通过无关重启/放宽限流处理。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 21:21:40.460–21:21:47.290（6.83秒） |
| 请求停止 / 旧进程退出 | 21:21:47.391 / 21:21:47.494 |
| 完整一致备份及核验 | 21:21:47.762–21:21:51.098 |
| 切换请求 / 新容器启动 | 21:21:51.098 / 21:21:51.416 |
| 首次HTTP可达 / Docker healthy | 21:21:52.556 / 21:22:21.801 |
| 本机验收 / 公网验收 | 21:22:21.911 / 21:23:09.909 |

未采样精确停机；旧退出至首HTTP可达观测间隔约5.1秒。受限正式备份`/home/ubuntu/backups/account-pool-balancer/20261107T212114Z-stateless-8d41516/`（0700/0600）保留data、原Compose/.env、构建/切换日志及回执，data SHA256`d6e2a6fe600c5dd36cb56cf8ee717be5d5cd3ad963b3cfc6a3a47ee448e40c74`。回退镜像`account-pool-balancer:rollback-20261107T212114Z`保留原02c61a8，回退前先核查新写入，不用旧data覆盖在线变化。无新增临时部署脚本/预检副本/容器或凭据下载；只补记录不再次部署。

## 历史部署：Mistral DeepSeek medium 档位兼容（2026-11-07 21:16 UTC，02c61a8）

- 用户授权解决后部署、再观察10分钟；`02c61a80f6a699e432f0a541581a98d81bed3532`已正常推送master。确定性错误为DeepSeek Chat上游400/3051拒绝medium：仅精确模型`deepseek-v4-1-flash-1017`将medium映射到high，已披露可能增加思考token；不降级/关闭思考，不改其他模型/档位/协议，不声称能保证缓存命中。保留a2ffae2流式内容块兼容。
- 只复用/运行Mistral定向Mock测试及必要build，不重复全仓/race/vet、不主动生产推理。一次预检原a2ffae2健康，旧e6f063b工作树干净，磁盘余量286.7GiB。从干净git archive构建源码`/home/ubuntu/project/account-pool-balancer-releases/20261107T211431Z-02c61a8`；镜像`account-pool-balancer:release-02c61a8-20261107T211431Z`，ID`sha256:28004aa53424a316e8339d29db3218b4f6fb850bbc963f5605538c872676cdd4`。
- 复用既有脚本，仅内存适配；无存储格式变化，不额外复制敏感数据预检。新镜像就绪且无queued/running注册任务后停止旧服务，退出0后完整一致备份、gzip/SHA256核验，只追加image覆盖更新Balancer。**161账号、186注册记录核心资产及凭据/Key/代理/支付/备注、routingSecret保留，完整config逐字节不变，原用户/环境/挂载/端口/网络/资源及健康策略不变。** 未改NewAPI配置/在线功能开关。
- 本机健康/版本/四资源通过，healthy/零重启/无OOM；真实未登录公网浏览器登录页正常，healthz/四资源均200，`?v=e02f22dc28`资源SHA256逐项匹配；21:16:07.061 UTC验收通过，已立即反馈并开始新10分钟自然业务观察。专用浏览器关闭，观察结果另记下方及Trellis会话；不将Mock/静态验收当推理验证。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 21:14:54.949–21:15:01.847（6.90秒） |
| 请求停止 / 旧进程退出 | 21:15:01.948 / 21:15:02.120 |
| 完整一致备份及核验 | 21:15:02.403–21:15:05.753 |
| 切换请求 / 新容器启动 | 21:15:05.753 / 21:15:05.972 |
| 首次HTTP可达 / Docker healthy | 21:15:07.088 / 21:15:36.329 |
| 本机验收 / 公网验收 | 21:15:36.436 / 21:16:07.061 |

未采样精确停机；旧进程退出至首次HTTP可达观测间隔约5.0秒。受限正式备份`/home/ubuntu/backups/account-pool-balancer/20261107T211431Z-effort-02c61a8/`（0700/0600）保留data、原Compose/.env、构建/切换日志及回执，data SHA256`8acff9f88965271c1bec8ae21d0a5ba3598a0c99087d47e7500cdfe55d1d2414`。回退镜像`account-pool-balancer:rollback-20261107T211431Z`保留原a2ffae2，回退前先核查新写入，不用旧data覆盖在线变化。无新增临时部署脚本/预检副本/容器或凭据下载；只补记录不再次部署。

## 历史部署：Mistral Chat 流式内容块兼容（2026-11-07 20:43 UTC，a2ffae2）

- 用户授权部署且要求不做过多测试；`a2ffae2fd8555122af89d356787844d1d4a809c6`已正常推送`origin/master`。复用本提交已通过的Go normal/race/vet/build及逐帧Flush回归，不重复全量测试、不发起生产推理。只更新Balancer，不改NewAPI渠道或在线配置/功能开关。
- 一次预检确认原918352d健康、restartCount=0、无OOM，旧工作树仍e6f063b/干净，磁盘充足。从干净`git archive`构建到`/home/ubuntu/project/account-pool-balancer-releases/20261107T204139Z-a2ffae2`，未在旧树构建/reset。镜像`account-pool-balancer:release-a2ffae2-20261107T204139Z`，ID`sha256:fdeecdc05694a4b14f9e2871dba3de6ee6353b0a425758b06a66256b8ce37a69`。
- 复用既有部署脚本，仅内存适配固定版本及备份名称。无存储格式变化，不额外复制敏感数据做断网预检；新镜像就绪并确认无queued/running注册任务后才停止旧服务，退出0后完整一致备份data，通过gzip/SHA256核验，仅追加image覆盖更新目标服务。**157个账号、186条注册记录及凭据/Key/代理/支付记录/备注、routingSecret保留，config逐字节不变，原UID1000:1000/环境/卷/端口/网络/资源及健康策略不变。**
- healthy、restartCount=0、无OOM、运行revision正确；本机healthz和四资源通过。真实未登录公网浏览器`https://ods.yeschoy.io/`登录页正常，healthz及四资源HTTP200，保留`?v=e02f22dc28`查询串且SHA256逐项匹配目标源码；20:43:22.287 UTC验收通过并立即反馈。验收时切换后尚无新的渠道180自然业务记录，不将服务验收宣称为真实Mistral推理修复验收；未主动测试推理或回改旧错误日志。专用浏览器已关闭。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 20:41:58.198–20:42:06.169（7.97秒） |
| 请求停止 / 旧进程退出 | 20:42:06.268 / 20:42:06.370 |
| 完整一致备份及核验 | 20:42:06.621–20:42:09.996 |
| 切换请求 / 新容器启动 | 20:42:09.997 / 20:42:10.215 |
| 首次HTTP可达 / Docker healthy | 20:42:11.315 / 20:42:40.548 |
| 本机验收 / 公网验收 | 20:42:40.646 / 20:43:22.287 |

未采样精确不可用窗口；旧进程退出至首次HTTP可达观测间隔约4.9秒，不将健康等待或整体操作时长当停机。受限正式备份`/home/ubuntu/backups/account-pool-balancer/20261107T204139Z-mistral-stream-a2ffae2/`（目录0700/文件0600）保留data、原Compose/.env、构建/切换日志及验收回执；data SHA256`d2e42292236abf70cc11b74cd206c8578dddd5a9d9e6efc1e49ab78985d4ab0c`。回退镜像`account-pool-balancer:rollback-20261107T204139Z`保留原918352d；回退前先核查新写入，不自动覆盖在线数据。本轮无新增临时脚本/数据副本/容器或凭据下载；只补部署记录，不重新构建或重启。

## 历史部署：master 合并版（2026-11-07 20:19 UTC，918352d）

- 用户授权推送并部署；`918352d5d91b661699ffdb09ad7f5401910b998b` 已正常推送 `origin/master`，从干净 `git archive` 构建。包含 Mistral reasoning_content 原生兼容、Chat/Responses 会话缓存key、管理员完整Key/代理展示复制、TokenHarbor独立默认关闭的详情日志开关，以及master已合入的OpenDesign/TLS/注册功能。
- 复用既有部署脚本，仅内存适配；本提交定向验证已通过，不重复全量测试。相对线上8d852b6没有注册库存储格式变化，不额外复制敏感数据做离线预检；未修改远端e6f063b旧分叉工作树，未新建Trellis任务。
- 源码 `/home/ubuntu/project/account-pool-balancer-releases/20261107T201720Z-918352d`；镜像 `account-pool-balancer:release-918352d-20261107T201720Z`，ID `sha256:4b7c902e5eb2098428d9d4ed550da3c7f6c05d73288f409ae0094c97bb5dccc3`。
- 新镜像就绪且确认无queued/running注册任务后才停止旧服务；退出0后完整一致备份data并通过gzip/SHA256核验，仅追加image覆盖更新目标服务。157个账号、186条注册记录及凭据/Key/代理/支付记录/备注、routingSecret完整保留；config逐字节一致，原用户/环境/卷/端口/网络/健康/资源参数一致，不改在线配置或功能开关，不触发注册、付款或真实推理。
- healthy、restartCount=0、未OOM，运行revision正确；本机healthz/四资源通过。真实未登录公网浏览器 `https://ods.yeschoy.io/` 登录页正常，healthz及四资源均200，保留`?v=e02f22dc28`查询串，SHA256逐项匹配目标源码；20:19:03.419 UTC验收通过并立即反馈。只补记录，不重建/重启。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 20:17:45.682–20:17:54.122（8.44秒） |
| 请求停止 / 旧进程退出 | 20:17:54.224 / 20:17:54.331 |
| 完整一致备份及核验 | 20:17:54.585–20:17:57.922 |
| 切换请求 / 新容器启动 | 20:17:57.922 / 20:17:58.125 |
| 首次HTTP可达 / Docker healthy | 20:17:59.231 / 20:18:28.469 |
| 本机验收 / 公网验收 | 20:18:28.565 / 20:19:03.419 |

未采样精确不可用窗口；旧进程退出至首次HTTP可达观测间隔约4.9秒，不把Docker健康等待或整体操作时长当作停机。受限备份 `/home/ubuntu/backups/account-pool-balancer/20261107T201720Z-master-918352d/` 保留data、校验、原Compose/.env、构建/切换日志及deploy.json，目录0700、文件0600；data SHA256 `6fa59b8a01a2ef2faf08fc47bdc2acdef42a1f0d08af67865369e35593244ac1`。回退镜像 `account-pool-balancer:rollback-20261107T201720Z`（原8d852b6）保留，回退前核查新写入，不自动覆盖在线数据。本轮无新增临时脚本/预检副本/容器或凭据下载，既有产物未经同意不清理。

## Mistral 号池部署（2026-11-07 12:38 UTC，8d852b6）

- 用户授权提交、推送并部署。业务提交 `8d852b65ecaaf91bce17f197065931edbf77073e` 已正常快进推送 `origin/master`，从该提交的干净 `git archive` 构建；未新建 Trellis 任务，不改远端旧分叉工作树。
- 从运行中的 `e9a3669` 升级，发布源码 `/home/ubuntu/project/account-pool-balancer-releases/20261107T123452Z-8d852b6`；镜像 `account-pool-balancer:release-8d852b6-20261107T123452Z`，ID `sha256:71fb3c1571394f3ecd75a3545b5a36fadc59452a67396cd4c052dfdcee8181ea`。构建约6.79秒，复用本提交已通过的全仓 normal/race/vet/build、JS 和本地浏览器验收；本轮只有原生 Key 号池增量，无注册库存储格式变更，不额外复制敏感数据做预检。
- 切换前远端内存解密核实无 queued/running 注册任务；新镜像就绪后才停止旧进程，退出0后完整一致备份 data，gzip完整性与SHA256通过，再复用原Compose文件链追加仅image覆盖，更新目标服务。
- **151账号、10把下游密钥、2把导入密钥、186条注册记录及其凭据/Key/固定代理/支付记录、保存代理池、5份任务备注及routingSecret保留**。配置文件逐字节一致；允许Cookie/套餐观察/更新时间正常刷新，注册核心资产在远端内存HMAC核对。UID1000:1000、环境、挂载、端口、网络及别名、健康/重启策略和资源参数均保持原状（Memory仍0）。Mistral账号仍0，不将用户测试Key入池，不修改在线设置或自动启用功能，不主动注册、付款或生产推理。
- 容器 `2770641a532b` healthy、restartCount=0、未OOM，运行image/revision正确。本机healthz及四静态资源通过；真实未登录公网浏览器 `https://ods.yeschoy.io/` 的healthz与四资源均200，保留 `?v=86a7b466e3` 查询串，SHA256与目标源码逐项相同；HTML已包含Mistral标签和独立设置。12:38:25 UTC完成公网核验，已立即反馈上线，本文档随后补记，不再次部署。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 12:35:45.712–12:35:52.501 |
| 请求停止 / 旧进程退出 | 12:37:14.560 / 12:37:14.678 |
| 完整一致备份及核验 | 12:37:15.060–12:37:18.333 |
| 切换请求 / 新容器启动 | 12:37:18.333 / 12:37:18.587 |
| 首次HTTP健康可达 / Docker healthy确认 | 12:37:18.963 / 12:37:49.271 |
| 本机验收 / 公网验收 | 12:37:49.407 / 12:38:25.026 |

未采样精确不可用窗口；旧进程退出至首次HTTP健康可达间隔约4.3秒，不将Docker健康等待或整个交付时长误报为停机。
受限正式备份 `/home/ubuntu/backups/account-pool-balancer/20261107T123452Z-mistral-8d852b6/` 保留完整data、校验、原Compose/.env、构建/切换日志和deploy.json，目录0700、文件0600。data SHA256：`3c152404c3859cde0979171d36dc409036e729fee5f5d77f870e22b2dd8df545`。
回退镜像 `account-pool-balancer:rollback-20261107T123452Z` 保留（原e9a3669）；之后若导入Mistral账号，旧版不识别新provider，回退前必须核查新写入及配置兼容，不用旧整份data覆盖在线变化。正式备份、源码、新旧镜像保留；临时产物登记 `_tmp_artifacts.md`，未经同意不清理。

## 邮箱验证等待调整（2026-11-06 13:30 UTC）

- 按用户要求，通过正常管理员设置API（CSRF + expectedRevision）将线上
  `tokenHarbor.mailTimeoutSec` 从0（引擎回退3分钟）设为120秒；API及磁盘均确认生效，
  revision由5217变为5218。轮询仍是每次邮箱查询完成后等待5秒；新启动任务使用2分钟，
  已启动任务不打断、不重放。
- 设置接口同时按既有固定单并发规则将存储的registerConcurrency从0规范化为1，
  实际并发前后均为1；除这项规范化、等待时长及revision外，完整配置一致。
- 未构建、部署或重启服务，healthz HTTP200、容器healthy、restartCount=0。
  未触发注册、验证码、创建Key、付款、入池或推理；操作会话已正常退出。
- 修改前config及change.json保留于受限业务备份
  `/home/ubuntu/backups/account-pool-balancer/20261106T133030Z-th-mail-120/`，
  目录0700、文件0600，不下载、不入Git，不作为临时垃圾清理。回退只通过设置API恢复
  mailTimeoutSec，不能用旧整份config覆盖后续在线变更。13:36:51 UTC复核通过。
  本节仅补记录，不再次部署。

## 当前部署：远端最新 master（2026-11-07 10:08 UTC，e9a3669）

- 用户授权部署 OVH 最新远端 `master`；fetch 及构建前核对锁定
  `e9a3669ddbfa3a87d0221edba396e6ac608d7273`。从干净 `git archive` 构建，
  不夹带本地其他窗口的未提交改动，不修改/reset 远端 e6f063b 旧分叉工作树，
  不新建 Trellis 任务。复用该提交已通过的完整 Go/Node/假数据浏览器验收，未重复跑全量测试。
- 上线稳定浏览器身份配置、可配置 1–5 注册并发（全局上限5）、注册完成顺序及任务排序。
  完整线上配置逐字节不变，原注册默认并发1、邮箱等待120秒均保留，未主动创建任务、
  注册、发信、解验证码、创建 Key/支付链接、付款、入池或推理。
- 干净源码 `/home/ubuntu/project/account-pool-balancer-releases/20261107T100552Z-e9a3669/`；
  固定镜像 `account-pool-balancer:release-e9a3669-20261107T100552Z`，镜像 ID
  `sha256:723b9b011c293623193a367be8dcacd07dcf27b17e4a5885fe91291579ef197a`。
  仅在原 Compose 叠加 image override 切换目标服务；用户、环境、挂载、端口、网络别名、
  健康/重启策略和实际资源限制一致，未应用源码 Compose 的其他默认值。
- 新注册可选字段首次升级，按线上 UID1000:1000、`--network none` 使用数据副本预检；
  healthz、正常管理员登录后的186行注册列表核验通过，正常退出0，不访问任何外部上游。
  新镜像就绪后才停止旧服务；旧进程退出0后完成完整一致 data 备份及 gzip/SHA-256 核验。
  **151个账号、186条注册记录的凭据/Key/固定代理/支付记录/备注、保存代理池、
  routingSecret 及完整配置均保留**；敏感比对仅在远端内存执行，允许正常 Cookie/套餐观察更新时间刷新。
- 新容器 healthy、restartCount=0、revision 正确，本机 healthz 及 HTML 中四项资源验收通过。
  真实未登录公网浏览器 `https://ods.yeschoy.io/` 登录页正常，healthz/四资源均HTTP200，
  保留 `?v=8411c592f3` 的资源哈希与目标源码逐一相符；10:08:14.709 UTC 完成公网验收并立即反馈。
  未登录生产管理员做业务写入，不把健康/资源验收当作上游真实注册或付款验收。只补记录，不再次重启。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 10:07:15.378–10:07:22.138（6.760秒） |
| 断网副本预检 | 10:07:22.138–10:07:25.100（2.962秒） |
| 请求停止 / 旧进程退出 | 10:07:25.224 / 10:07:25.343 |
| 完整一致备份及校验 | 10:07:25.634–10:07:28.924（3.290秒） |
| 切换请求 / 新容器启动 | 10:07:28.924 / 10:07:29.135 |
| 首次HTTP健康可达 / Docker healthy | 10:07:30.235 / 10:07:59.475 |
| 本机验收 / 公网验收 | 10:07:59.602 / 10:08:14.709 |

未采样精确不可用窗口，不将约30秒Docker健康等待或整个操作时长误报为停机。
受限备份 `/home/ubuntu/backups/account-pool-balancer/20261107T100552Z-master-e9a3669/`
含完整 data.tar.gz/校验、原 Compose/.env、构建/切换日志和 deploy.json；目录0700、
敏感文件0600，不下载、不入Git。data SHA-256：
`05c9a462c11d89532a34b32901b6b4c89e4b69f876330dd5228f27fde3688ce4`。
回退镜像 `account-pool-balancer:rollback-20261107T100552Z`（原a8902bc）保留；
新版可选注册字段可能被旧版丢弃，降级前先核查新写入和兼容方案，不用旧整份data覆盖在线变化。
已停止的预检容器 `account-pool-balancer-preflight-20261107t100552z`、备份内
`preflight-data/` 和 `preflight.log` 已登记 `_tmp_artifacts.md`，待用户同意再定向清理；
正式备份/源码/新旧镜像及 Trellis 记录保留。公网独立浏览器已关闭。

### 上线后 NewAPI 只读排查（2026-11-07 10:24 UTC）

- 用户要求检查 NewAPI 报错、若由新版引起则修复。合并核对近2小时 NewAPI Docker日志、
  只读 PostgreSQL 使用/错误记录、渠道路由及号池 JSONL 业务日志，未发起真实推理或写入配置。
  NewAPI 自11月3日起运行原镜像，本次只升级号池；两服务均healthy、restartCount=0、未OOM。
  按正常域名 `Host: yeschoy.com` 访问本机 NewAPI `/api/status` HTTP200/success=true；
  无该Host的404是现有域名路由行为，不作为服务故障。
- Modal渠道156和OpenCode渠道167均有上游429，升级前就存在；升级后15分钟分别有37/44条
  NewAPI成功消费记录，也分别有19/8条限流错误。客户端取消对应client_gone/499，
  固定客户端的Invalid token同样早于升级，不据此修改凭据、限流或调度保护。
- 10:16:29 UTC渠道134的单条503，在号池错误记录中确认为OpenDesign上游
  `workspace_billing_scope_unavailable`（账单服务内部请求超时）；未发现本地panic或持久化失败。
  渠道158的两条零token计费告警来自独立外部上游，不经过号池；旧TokenHarbor认证错误
  集中在升级前08:41–09:12 UTC，渠道170核查时已是停用状态，本轮未更改。
- 排查期间10:23起渠道174–179出现422；这些是直连Mistral官方的渠道，不经过号池，
  不能归因本次e9a3669。另有一条OpenCode400明确拒绝file内容块，此类请求须使用其支持的内容格式，
  不在后台丢弃附件来掩盖错误；本轮未修改这些渠道或用户请求。
- 切换窗口10:07:26–10:07:28 UTC存在访问旧兼容别名的Docker DNS错误，启动后截至10:24:21 UTC
  未见继续发生。这是切换期间请求失败证据，不代表持续故障，也不是精确停机采样。
- 当前没有证据表明e9a3669引入上述持续错误，未盲目修代码、回滚、重启、覆盖配置或调整计费。
  本轮探针/查询均在内存执行，无新增临时脚本、日志下载、凭据副本或浏览器产物。

## 历史部署：Stripe 链接路径兼容（2026-11-06 21:37 UTC，a8902bc）

- 用户同意上线安全诊断并用一个未尝试账号验证。先上线`9f6ab11`取得真实响应结构，再修复前后端均写死`/c/pay/`的问题，最终运行`a8902bc6069b4795c2c08d7864e3638ee9e0aef9`。Stripe官方[Session.url合同](https://docs.stripe.com/api/checkout/sessions/object#checkout_session_object-url)不保证路径前缀；现在只接受HTTPS、精确checkout.stripe.com、无userinfo/显式端口/空白且非空根地址，路径/query/fragment按不透明值处理。不扩展自定义域名或invoice_url，不更改报价、单次月付、禁止扣款/升级、pending保护及保存链接优先复用规则。
- 干净git archive源码：`/home/ubuntu/project/account-pool-balancer-releases/20261106T213621Z-a8902bc`；未夹带两份原有HTML/JS测试草稿或未跟踪目录。旧工作树仍e6f063b/干净/分叉，未在旧树构建、reset或覆盖配置。
- 镜像`account-pool-balancer:release-a8902bc-20261106T213621Z`，ID`sha256:73c0301dd57e2a59ed45ee95a5ffda0bdb3f288701b7f8d40dc4a9759c844f26`。复用下节刚完成的同格式断网预检，不重复复制敏感数据；构建就绪后才停服务并做完整一致data备份。仅通过release内image override切换原Compose目标服务，UID1000:1000、环境、卷/端口/网络别名、健康/重启策略和实际资源保持原状；线上Memory限制原本为0，不套用本地新Compose的8GiB。
- **78账号、92注册记录及其凭据/Key/固定代理/支付记录/备注、保存代理池、routingSecret和完整配置均保留**。允许后台正常更新Cookie/套餐观察/更新时间，其他资产用远端内存HMAC比对；配置逐字节一致，日志保留仍30天，未擅自启用功能。
- 全量Go normal/race/vet/build、三套Node行为测试及本地假数据浏览器通过：模拟非`/c/pay/`链接一次创建、加密保存、显式读取和前端展示成功，mock预检1/创建1，另一pending不变、未入池、核账框未勾选。**以上是本地验收。随后用户再次明确授权单账号实测：21:45:55 UTC使用无既有checkout的已验证Free账号`fac***@icloud.com`（`thr_b24e4eab89cf71a7070b6953`，任务`that_ace0a9355f17cf11dbd84322`），在OVH运行a8902bc的正常管理员/CSRF接口上通过原固定代理创建一次，2.542秒返回HTTP200。非`/c/pay/`的HTTPS checkout.stripe.com链接已加密保存，显式GET读回一致；账号仍registered、无pending/failure，原30份checkout记录及完整配置不变。未重试旧订单、提交付款或启用自动续费；管理员正常退出。验证范围是生成、保存及读取，不包含Stripe页面渲染或付款完成。**
- Docker healthy、restartCount=0，healthz及四静态资源本机校验通过。真实未登录公网浏览器`https://ods.yeschoy.io/`的healthz/四资源均200，保留?v查询串，SHA256与a8902bc逐一相符；21:38:11 UTC完成核对。上线及限制已立即反馈，本文档随后补记，不再次重启。

| 阶段 | UTC 时间 |
|---|---|
| 构建 | 21:36:33.410–21:36:40.174（6.764秒） |
| 请求停止 / 旧进程退出 | 21:36:40.275 / 21:36:40.370 |
| 完整一致备份及gzip/SHA256核验 | 21:36:40.639–21:36:41.782 |
| 切换请求 / 新容器启动 | 21:36:41.782 / 21:36:41.972 |
| 首次HTTP健康可达 / Docker healthy | 21:36:43.078 / 21:37:12.309 |
| 本机验收 / 公网验收 | 21:37:12.411 / 21:38:11 |

未采样精确不可用窗口，不把约30秒Docker健康等待、开发诊断时间或整个交付时长当作停机。
受限备份`/home/ubuntu/backups/account-pool-balancer/20261106T213621Z-checkout-a8902bc/`含data.tar.gz、校验、原Compose/.env、构建/切换日志及deploy.json，目录0700、敏感文件0600。data SHA256：`8f21d56682876de98f215db884d7834f5538c7c29810113cdd4321a0f54f3710`。回退镜像`account-pool-balancer:rollback-20261106T213621Z`保留；降级前核查新写入，不用旧整份data覆盖在线变化。正式备份/镜像/源码保留，临时预检副本/容器及本地验收文件按`_tmp_artifacts.md`待用户同意清理。

## 支付响应诊断部署与一次实测（2026-11-06 21:09 UTC，9f6ab11）

- 首次从3058850升级至`9f6ab1151363d2d70778853946ac64ec0133e5ae`，含此前已完成的任务备注、默认7天记录级日志保留（保留线上显式30天）、真实周额度/月余额未知展示及只读支付预检。新增诊断只保留固定字段名/JSON类型/总字段数和URL分类，不回显URL标识符、任意键值、Cookie或凭据；HTTP200业务错误也可识别。
- 源码`/home/ubuntu/project/account-pool-balancer-releases/20261106T210853Z-9f6ab11`；镜像`account-pool-balancer:release-9f6ab11-20261106T210853Z`，ID`sha256:ee7162341de53c541db6f7bffec5ff41617eab6fa5e5933f5fe637810e0aa3ff`。
- 新可选元数据首次升级按原UID1000:1000运行断网副本，health及鉴权注册列表92行核验通过；预检容器`account-pool-balancer-checkout-preflight-20261106t210853z`已停止，副本保留在备份内preflight-data。原服务退出0后完整一致备份，再仅切换原Compose镜像；78账号/92记录关键资产、完整配置及运行参数均保留。
- 21:10:30 UTC，按用户授权只选一个无checkout标记的已验证Free账号`ter***@icloud.com`（`thr_d01e72e8b54c5e505082607b`、任务`that_ace0a9355f17cf11dbd84322`），通过正常管理员API/CSRF及其原固定代理提交一次链接创建；报价预检通过。3.948秒后本地API502，上游HTTP200，仅`checkout_url`字符串，HTTPS/checkout.stripe.com、无userinfo/端口/空白，但路径分类other-path，被旧固定前缀校验拒收。**未保留原始URL，不能断言该地址完整有效或收银台已验证；此次至少证明了固定路径造成的拒收点。**
- 创建前pending已持久化，结束后registered、无保存URL，有安全failure诊断；此前全部29份checkout记录保持原样。该账号只读ledger总数0，不代表没有待处理订单。**没有再次创建、换更多账号、核账代确认、地区确认、付款或自动续费操作**；管理员会话正常退出。旧不确定标记均保留，不能盲目重试。
- 构建21:09:04.768–21:09:11.407；停止请求/退出21:09:12.307/12.411；完整备份21:09:12.677–13.815；切换/启动21:09:13.815/14.033；HTTP可达21:09:15.143，Docker healthy21:09:44.388，本机验收21:09:44.503。公网health/四资源200及版本哈希核验亦通过。
- 备份`/home/ubuntu/backups/account-pool-balancer/20261106T210853Z-checkout-9f6ab11/`，data SHA256`897da473e91f86c8b19e7f70f2844cff6e083958ee05955722113bade38609d8`；回退镜像`account-pool-balancer:rollback-20261106T210853Z`（原3058850）保留。凭据仅远端内存/stdin，不下载或写入脚本/日志/Git，未新建Trellis任务。

## 历史部署（2026-11-06 UTC，远程 master：3058850）

- 按用户要求快速部署最新分支；fetch 后本地/远程 `master` 均为
  `3058850ba2aab0dd24dd1256bbd08191530666d6`，从干净 `git archive` 构建，
  未混入未跟踪目录，未修改远端旧分叉工作树，未新建 Trellis 任务。
- 上线 GitHub 五段输入、JSON 接码、授权响应核对及补发验证邮件功能；
  不修改在线配置或功能开关，未主动注册、补发邮件、解验证码、付款或推理。
  注册库存储格式无变化，未重复全量测试或创建敏感预检副本。
- 源码 `/home/ubuntu/project/account-pool-balancer-releases/20261106T154432Z-3058850`；
  固定镜像 `account-pool-balancer:release-3058850-20261106T154432Z`，镜像 ID
  `sha256:1867f7b454aeb8b3a91cc0a0fa7bb33c61a75948bdc72fd41d9e25fd1d783948`。
- 切换前确认无 queued/running 注册任务，旧进程退出0后完整备份 data 并校验 gzip/
  SHA-256；只更新目标服务，用户、环境、挂载、端口、网络别名、健康检查及资源参数不变。
  **78账号、完整配置和密钥、routingSecret、94条注册记录关键资产及保存代理池全部保留**。
  敏感比对仅在远端内存执行，无凭据输出、下载或入 Git。
- 新容器 `ddd956ac316a` healthy、重启0、未OOM，revision 与镜像 ID 正确。
  本机及真实未登录公网浏览器的 healthz、首页、四项资源均200，资源 SHA-256
  与目标源码一致（`?v=375042c90d`）。验收通过已立即反馈，再补记录，不再次重启。

| 阶段 | UTC 时间 / 时长 |
|---|---|
| 构建 | 15:45:54.180–15:46:01.002，6.822秒 |
| 请求停止 / 旧进程退出 | 15:46:01.103 / 15:46:01.191 |
| 完整一致备份及校验 | 15:46:01.508–15:46:02.489，0.982秒 |
| 切换请求 / 新容器启动 | 15:46:02.514 / 15:46:02.801 |
| Docker健康确认 / 本机验收 | 15:46:33.403 / 15:46:33.658 |
| 公网验收 | 15:47:01.868 |

未采样精确不可用窗口，不将约31秒健康等待或整个操作时长视为停机。
受限备份目录 `/home/ubuntu/backups/account-pool-balancer/20261106T154432Z-pre-3058850/`
保留完整 data/校验、Compose/.env、build/switch日志及deploy.json；data SHA-256：
`2c2c32012027fdfa5373070b573b4aeb0f81d7f7e32c2810e91da8674ea73f0e`。
旧镜像 `account-pool-balancer:rollback-20261106T154432Z`（0d32578）保留；
回退前先核查上线后新写入，不能用旧整份 data 覆盖在线业务数据。
本轮直接管道归档、内存执行，无新增临时脚本/压缩包/诊断容器；既有临时产物未经同意不清理。

## 历史部署（2026-11-06 UTC，远程 master：0d32578）

- 按用户要求以 GitHub 远程 `master` 最新提交为准：fetch 后及切换前再次核对均为
  `0d32578d347a5f43dcaf6590905b1e3a6dcf914f`。从该提交干净 `git archive` 构建，
  不包含本地未跟踪目录，不修改/reset远程旧分叉工作树，不新建Trellis任务。
- 上线取消本地60秒注册提交间隔、保留固定单并发，以及客户端身份一致性修复。
  不修改在线配置或功能开关，`egressRegion` 继续留空，不擅自选择出口地区。
  无注册库存储格式变化，复用已通过的测试，不额外复制敏感数据做断网预检。
- 源码 `/home/ubuntu/project/account-pool-balancer-releases/20261106T120400Z-0d32578`；
  固定镜像 `account-pool-balancer:release-0d32578-20261106T120400Z`，镜像ID
  `sha256:6a98a27c7ab3c6d18ae9f7e88f37f95a7c34290f0b41ad910257f24a1872b088`。
  镜像同时设置 `org.opencontainers.image.revision` 和 `revision` 标签。
- 切换前确认无queued/running注册任务；旧进程正常退出0后完整备份data，通过gzip及
  SHA-256校验，再复用原Compose仅切换目标服务。用户、环境、挂载、端口、网络别名、
  健康检查、重启策略及资源参数核对保持一致。
- **78账号、完整配置和密钥、routingSecret、93条注册记录关键资产及保存代理池均保留**。
  注册库仅在远端内存解密比对，允许Cookie/套餐观察/更新时间正常刷新；不输出或下载凭据。
- 新容器 `45acd8f4f7bf` healthy、restartCount=0、未OOM，运行revision和镜像ID正确。
  本机及真实未登录公网浏览器healthz均HTTP200，HTML四项资源均200且SHA-256与源码一致
  （`?v=cc3f2e3eb3`）；取消间隔文案及地区控件已确认。初次验收脚本误用不存在的
  `th-set-egress-region`，按目标源码实际 `th-set-region` 修正后通过，未再次构建/重启。
  验收通过立即反馈上线，再补本文档；未主动注册、解验证码、付款或推理。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 12:03:16.653–12:03:23.165，6.512秒 |
| 请求停止 / 旧进程退出 | 12:04:33.963 / 12:04:34.311 |
| 完整一致备份及校验 | 12:04:34.383–12:04:36.720，2.337秒 |
| 切换请求 / 新容器启动 | 12:04:36.751 / 12:04:36.965 |
| Docker健康确认 | 12:05:07.553 |
| 公网验收 | 12:06:42.000 |
| 最终数据与本机验收登记 | 12:07:29.224 |

整体超过5分钟已说明预检/远程版本核对及验收卡点；构建仅6.512秒。
未采样精确不可用窗口，不将约31秒健康等待或整个交付时长当作停机。

### 备份、回退与临时产物

- 受限目录 `/home/ubuntu/backups/account-pool-balancer/20261106T120400Z-pre-0d32578`，
  含完整data/校验、Compose/.env、build/switch日志及deploy.json；目录0700、敏感文件0600，
  不下载、不入Git。data SHA-256：
  `3cdb4c4694cc6a3626dc9365045389bf3bf0fe55c2a0c92b0aa6e72a1cbae6a6`。
- 旧镜像 `account-pool-balancer:rollback-20261106T120400Z`（c88bc2a）保留。
  回退前核查上线后的配置/账号/注册记录新写入；不整体恢复旧data覆盖在线业务数据。
- 本轮无新增临时部署脚本、源码压缩包、诊断容器或敏感预检副本；独立公网浏览器已关闭。
  正式备份、源码、新旧镜像及部署业务记录保留；既有临时产物未经同意不清理。

## 历史部署（2026-11-06 UTC，master：c88bc2a）

- 用户授权推送并部署；`master` 已推送 `c88bc2ac443ae0eef1e65b0fa9e27f2c2c2c69b2`。
- 本次上线两个提交：`6c69145`（登录不匹配恢复与 OAuth 失败说明）与
  `c88bc2a`（每账号独立一致的客户端身份）。后者新增 `internal/egressid`，
  修复号池与注册机多处未设置 User-Agent、从而以 `Go-http-client/1.1` 出网的问题，
  并让注册机的 UA 按账号确定性随机、同时补齐与之自洽的 `sec-ch-ua*` 与
  `accept-language`；邮箱与验证码读取也复用同一账号种子。
- 从干净 `git archive` 快照构建，源码目录
  `/home/ubuntu/project/account-pool-balancer-releases/20261106T094136Z-c88bc2a`，
  快照 SHA-256 `e1d5e66fda925bb7daf44880879a886dc476eff58da8be59651a7a331e8d7739`；
  不含本地未跟踪产物。
- 固定镜像 `account-pool-balancer:release-c88bc2a-20261106T094136Z`，镜像ID
  `sha256:3fa0ae5611d68761d65453c76091742f5705054e594302298b0832c397380f79`。
  构建一次耗时 7.27 秒，未重复运行本提交已通过的本地全量测试。
- 因注册存储与身份逻辑变更做断网副本预检：复用线上 UID/GID 1000:1000、
  `--network none`、仅挂载副本；容器健康、healthz 返回 `{"ok":true}`、
  无解密或 panic 报错，随后清理预检容器与副本。
- 切换前以 25 秒×8 次采样确认注册库 mtime 与大小稳定（17:38:25 / 283848 字节），
  判定无排队或运行中注册任务。旧进程停止前完整备份 data（gzip 校验通过，
  SHA-256 `2f9e31ebda5375118f89a7fd60b2a4259a1e117684705672f4f0e22098aeecea`），
  再复用原 Compose 仅切换本服务；配置、用户、挂载、端口、网络别名、健康检查与
  资源参数保持不变。
- 新容器 healthy、restartCount=0、未 OOM，运行镜像 ID 与目标一致。
  `/healthz` 返回 200，首页四项资源（`app.css`/`app.js`/`perch.js`/`tokenharbor.js`，
  `?v=363aafdcf3`）HTTP 200，本地与远端的 SHA-256 逐一相同。
  `config.json`（82688 字节）与 `tokenharbor-signup.json`（283848 字节）保持原样。
- **本次验收不包含上游真实注册或推理**：`egressid` 的实际效果需要等下一次
  TokenHarbor 请求发生后才可在其面板上观察，本轮未主动注册、未解验证码、
  未付款、未推理。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 09:46:27.611–09:46:34.881，7.27 秒 |
| 备份 | 09:50:49.123–09:50:50.830，1.71 秒 |
| 切换请求 | 09:51:24.194 |
| Docker 健康确认 | 09:51:57.472，约 33 秒 |
| 验收完成 | 09:53:26 |

### 备份、回退与临时产物

- 受限目录 `/home/ubuntu/backups/account-pool-balancer/20261106T094136Z-pre-c88bc2a`，
  含 `data.tar.gz`、`docker-compose.yml`、`.env`；目录 0700、文件 0600，
  不下载、不入 Git。
- 保留旧镜像 `account-pool-balancer:rollback-20261106T094136Z`（`b7e751723905`，
  对应 `111ba2c`）。若需降级，先确认新版是否已写入旧版无法识别的记录，
  不能直接用旧 data 覆盖线上新写入。
- 临时产物：`/tmp/apb-c88bc2a.tar.gz`、`/tmp/preflight-c88bc2a/`（副本已删）、
  `/tmp/build-c88bc2a.log`，已登记，待用户同意后清理。

## 当前部署（2026-11-06 UTC，master：111ba2c）

- 用户授权推送部署；`master` 已推送业务版本
  `111ba2cac0944770b4714993ab4376de91e75122`。本节仅补记录，不再次构建/重启。
- 上线 GitHub OAuth 四段输入、分阶段固定代理、动态 finish、会话检查点和安全诊断，
  包含官方 `auth.tokenharbor.ai` 自定义鉴权域名白名单修复。新增 GitHub 行按用户选择
  明文0600保存，既有邮箱行保持原加密；本次线上 GitHub 新行数为0。
  **真实 GitHub 完整登录/邮箱验证此前仍未通过，本次健康验收不是该流程验收。**
- 从干净 git archive 构建，源码目录
  `/home/ubuntu/project/account-pool-balancer-releases/20261106T074610Z-111ba2c`；
  不含本地 `.private/` 会话、测试账号、诊断目录及未跟踪工作树。
  远程旧分叉工作树保持 e6f063b、干净，不 reset/rebase 或在旧源码上构建。
- 固定镜像 `account-pool-balancer:release-111ba2c-20261106T074610Z`，镜像ID
  `sha256:b7e75172390589abae0fc4ea5da0e30f8ad1574310cfdf4f5a8a3d41f5483324`。
  构建一次耗时7.187秒，未重复运行本次提交已通过的定向测试。
- 因注册存储扩展做断网副本预检。首个隔离容器默认USER与生产1000:1000不一致，
  只因副本0600权限退出1，未动线上；同镜像/副本复用线上UID/GID后启动健康并退出0。
  两个容器均network none、仅挂载副本；不改生产权限，不重建镜像。
- 切换前确认无queued/running注册任务，旧进程正常退出0后完整备份data并通过gzip/
  SHA-256校验，再复用原Compose仅切换本服务。环境、用户、挂载、端口、网络别名、
  重启/健康检查和资源配置保持一致，不覆盖在线配置或开启功能。
- **78账号、10把下游密钥、2把导入密钥、完整配置、routingSecret、81条注册记录的
  凭据/Key/代理/关联/支付链接及保存代理池全部保留**；远端内存HMAC核对，允许后台
  正常更新Cookie/套餐观察/更新时间。未上传或导入本地测试会话，未主动注册、解验证码、
  付款或推理。
- 新容器healthy、restartCount=0、未OOM，revision正确。本机和真实未登录公网浏览器
  healthz均HTTP200，HTML中的四项资源均200且SHA-256与目标源码相同。
  初次资源校验将`?v=`计入文件名，修正URL映射后通过，未再次重启；公网确认新代理控件
  已存在，未登录生产管理员。验收通过已立即反馈，再补本文档。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 07:50:20.283–07:50:27.470，7.187秒 |
| 同生产UID的断网启动校验 | 07:54:26.897–07:54:27.440，0.543秒 |
| 请求停止 / 旧进程退出 | 07:54:27.530 / 07:54:27.792 |
| 完整一致备份及校验 | 07:54:27.865–07:54:28.689，0.823秒 |
| 镜像切换请求 / 新容器启动 | 07:54:28.689 / 07:54:28.909 |
| Docker健康确认 | 07:54:59.494 |
| 本机完整验收 | 07:56:34.711 |
| 公网验收登记 | 07:58:37.862 |

整体超过5分钟已说明卡点，主要是预检/验收核对（含UID修正及资源查询串映射），
不是构建耗时或持续停机。未采样精确不可用窗口，不把约30秒健康检查等待当作停机。

### 备份、回退与临时产物

- 受限目录 `/home/ubuntu/backups/account-pool-balancer/20261106T074610Z-pre-111ba2c`，
  含完整data/校验、Compose/.env、build/switch日志及deploy.json；目录0700、敏感文件0600，
  不下载、不入Git。data SHA-256：
  `227fd8b325f6b721bc1ca62023e9870e39f7fe8a4dc465eecb6c33a05c54f305`。
- 保留旧镜像 `account-pool-balancer:rollback-20261106T074610Z`（5223edf）。
  **上线后若产生新GitHub行，旧版可能忽略并在写文件时丢弃这些行**；降级前必须检查
  新写入并明确保留/迁移方案，不能直接降级写注册库或恢复整份旧data覆盖新会话/账号。
- 两个已退出的断网容器 `account-pool-balancer-preflight-20261106t074610z`（exit1）和
  `account-pool-balancer-preflight-20261106t074610z-uid1000`（exit0），以及上述目录的
  `preflight-data/`、`preflight.log` 为临时预检产物，已登记，待用户同意再定向清理。
  正式备份/源码/新旧镜像、原有Trellis与本地业务会话不清理。公网验收浏览器已关闭。

## 历史部署（2026-11-06 UTC，master：5223edf）

- 用户授权重新部署 OVH，本次锁定本地已提交版本
  `5223edfa5e9c1589c644233d17aff02a686e0355`，从干净 `git archive` 快照构建；
  不混入未跟踪产物，不修改/reset远程旧分叉工作树。此提交部署时尚未推送 GitHub。
- 上线 TokenHarbor 注册限制仅结束当前账号、不再传播全局冷却的修复；保留固定单并发、
  新注册提交至少间隔60秒、不自动重试及真实UA。未主动注册、解验证码、付款或推理。
- 固定镜像 `account-pool-balancer:release-5223edf-20261106T061658Z`，镜像ID
  `sha256:0c6b150877b669f8354a912258b3321dd15c5d865532f05f769b97037ac54839`；
  源码目录 `/home/ubuntu/project/account-pool-balancer-releases/20261106T061658Z-5223edf`。
  无存储格式变化，复用已通过的Go普通/race/vet/build及JS测试，不复制敏感数据做额外预检。
- 复用原Compose，仅切换目标服务，配置、环境变量、用户、数据卷、端口、网络别名、
  健康检查、重启及资源参数保留。构建前及停机前均确认无排队/运行中注册任务，旧进程
  退出0后完整备份data并校验gzip与SHA-256，才切换新镜像。
- **78账号、10把下游密钥、2把导入密钥、完整配置、routingSecret、81条注册记录的
  密码/Key/代理/关联/支付链接等关键资产及代理池**全部保留；凭据仅在远端内存比对。
- 容器healthy、restartCount=0、未OOM，运行revision正确。本机及真实未登录公网浏览器
  healthz均HTTP200，首页正常，HTML中的四项资源均HTTP200、哈希与目标源码一致。
  验收脚本最初将环境变量顺序、空command和继承镜像entrypoint视为差异；按Docker/Compose
  语义核对后确认无实际变化，未重复构建/重启或恢复数据。整体超过5分钟已说明验收卡点，
  验收通过立即反馈；补本文档不再部署。这不是上游真实注册或付费推理验收。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 06:19:07.031–06:19:13.662，6.63秒 |
| 请求停止 / 旧进程退出 | 06:19:13.762 / 06:19:13.867 |
| 完整一致备份及校验 | 06:19:14.083–06:19:14.914，0.83秒 |
| 镜像切换 / 新容器启动 | 06:19:15.013 / 06:19:15.206 |
| 首次Docker健康检查通过 | 06:19:45.311 |
| 数据/本机资源验收 | 06:21:38.939 |
| 配置语义核对及公网验收登记 | 06:22:43.519 |

未采样精确不可用窗口，不把约30秒健康检查等待、验收排查或整体交付时长误报为停机。

### 备份与回退

- 受限目录 `/home/ubuntu/backups/account-pool-balancer/20261106T061658Z-pre-5223edf`，
  含完整data/校验、Compose/.env、build/switch.log及deploy.json，目录0700、文件0600，
  不下载、不入Git。data SHA-256：
  `48f54db7901645c06ec7a3c1296ec8489019cf1f0deb489a18526b5b346da6ce`。
- 旧镜像 `account-pool-balancer:rollback-20261106T061658Z`（54f28aa）保留。回退会恢复旧版
  跨账号注册暂停行为，须确认无注册任务并检查新写入；不整体恢复旧data覆盖上线后写入。
- 无新增临时部署脚本、诊断容器或敏感预检副本；公网独立浏览器验收后关闭。
  正式备份、源码、新旧镜像和业务记录保留，既有临时产物未经同意不清理。

## 历史部署（2026-11-05 UTC，master：54f28aa）

- 用户授权“先推送再部署”，并要求上线后观察几分钟。`master` 已推送并运行
  `54f28aaa5b0f77d5713e5feb1fca8bbb74d21a81`，业务提交为`082c03c`；不包含随机冒充
  Claude/Codex/pi等客户端的改动。真实下游UA保留，通用/缺省UA使用本项目真实标识；
  注册固定单并发、提交至少间隔60秒，遇限制暂停并完整遵守Retry-After，不自动重试。
- 固定镜像`account-pool-balancer:release-54f28aa-20261105T221442Z`，镜像ID
  `sha256:acccd04533506c9e13ecbf045f3310a61750343dc07de6e95e248ac764e64bc2`；
  干净源码归档位于`/home/ubuntu/project/account-pool-balancer-releases/20261105T221442Z-54f28aa`。
  未修改/reset旧分叉工作树，未在旧源码目录构建。
- 复用线上原Compose仅切换目标服务，环境、用户、数据挂载、端口、网络别名、重启及
  资源参数逐项相同，不应用源码Compose中的新资源默认值，不覆盖在线配置或开启功能。
  无存储格式变化，复用已通过的完整测试，不复制额外敏感预检数据。
- 构建后再次确认无排队/执行中的注册任务，旧服务退出后完整备份data、验证gzip及
  SHA-256后切换。**68账号、10把下游密钥、2把导入密钥、完整配置、routingSecret、
  81条注册记录关键资产及代理池**全部保留，后台Cookie/套餐观察更新不误判为数据丢失。
- 本机健康及四项静态资源验收通过；真实未登录公网浏览器healthz/资源均HTTP200，
  资源哈希与提交源码一致，固定并发1控件已确认。`tokenharbor.js` SHA-256为
  `1c951d4887579c2e1d47d48163efe56f5480b003396d127f22865b846c059acc`。
- 按用户要求连续观察约3分钟：每15秒采样，共13次，全部healthy/HTTP200，运行revision
  正确，restartCount始终0、未OOM。CPU观测0.01%–0.84%，内存185.7–236.2MiB（包含启动
  缓存预热，不是容量/峰值保证）。启动后截至日志核验时9条既有业务请求均200（OpenDesign3、
  Modal6），无新增错误日志、panic/fatal/持久化失败信号；TokenHarbor无真实推理样本。
  未主动注册、解验证码、创建订单、付款或推理，不把健康验收当作付费推理/官网UA显示实测。
  上线/观察通过已立即反馈，补记录不再重建或重启。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 22:14:42.994–22:14:49.724，6.73秒 |
| 请求停止 / 旧进程退出 | 22:14:49.835 / 22:14:49.931 |
| 完整一致备份及校验 | 22:14:50.113–22:14:50.917，0.80秒 |
| 新容器启动 | 22:14:51.125 |
| Compose healthy确认 | 22:15:21.718 |
| 本机/配置/资源验收 | 22:15:21.830 |
| 连续观察 | 22:15:54.257–22:18:55.274，13次采样/约181秒 |
| 公网验收登记 | 22:20:39.044 |

未采样精确不可用窗口，不将约31秒启动至healthy等待、用户要求的观察时间或整个操作时长
误报成停机。整体超过5分钟已说明构建仅6.7秒，包含主动连续观察与公网核验。

### 备份与回退

- 受限目录`/home/ubuntu/backups/account-pool-balancer/20261105T221442Z-pre-54f28aa`，
  含完整data/校验、Compose/.env、build/switch.log、deploy.json及observation.json，
  目录0700、文件0600，凭据不输出或下载。data SHA-256：
  `9afbc20a67c0417d604f728df7e72edef4499a0321d94bc773c6f429141edf8f`。
- 旧镜像`account-pool-balancer:rollback-20261105T221442Z`（0ed063c）保留。
  回退不整体恢复旧data，必须保留上线后的账号/会话/订单写入；旧版不具备本次单并发、
  暂停与真实UA兜底保护，降级前需明确影响并确认无注册任务。此次未执行回退。
- 无新增临时部署脚本或敏感预检副本，公网独立浏览器已关闭；正式备份、源码快照、
  新旧镜像和观察/部署业务记录不清理。既有本地Mock/测试产物仍待用户同意定向清理。

## 历史部署（2026-11-05 UTC，master：0ed063c）

- 用户授权清理本次测试产物并部署最新 master。预检确认本地/远端 master 均为
  `0ed063c1f04ddb01a1279607652ed82a150528d7`，从此提交的干净归档构建，
  不包含其他窗口尚未提交的注册节流/UA修改，不修改远程旧分叉工作树。
- 上线 Agent Pass 预检失败可重试、已保存链接复用、旧不确定结果需逐次人工
  核对账单确认后恢复的修复；保留报价/Free/固定出口和不自动付款保护。
- 镜像 `account-pool-balancer:release-0ed063c-20261105T220002Z`，镜像ID
  `sha256:9d32e1220aadf7353fc892cdec5a8e90b4db9fe769ea9619c155bd194e14e2b9`；
  源码目录 `/home/ubuntu/project/account-pool-balancer-releases/20261105T220002Z-0ed063c`。
  只切换原Compose的目标服务，卷、端口、网络别名、环境、用户、重启及资源参数
  逐项一致；不覆盖配置、不启用额外功能。无存储格式变化，不再复制敏感数据做离线预检。
- 构建后确认无排队/执行中注册记录，再停旧进程，完整备份data并通过gzip及
  SHA-256校验后切换。**68个账号、10把下游密钥、2把导入密钥、完整配置、
  routingSecret、81条注册记录的密码/Key/代理/关联/支付链接等关键资产及代理池**均保留；
  会话/更新时间允许后台正常刷新，凭据只在远端内存比对，不输出或下载。
- healthy、restartCount=0、运行revision正确；本机及公网真实未登录浏览器
  healthz均HTTP200，首页及从HTML读取的四项静态资源均200，哈希与目标源码一致。
  公网 `tokenharbor.js` 的恢复控件已确认，SHA-256为
  `8761c7e6e2ad726f3d36ec5b54e615ebc2b21a27a484ce64c59218671fc3dda3`。
  未登录生产管理员、创建真实订单、注册、解验证码、付款或推理；验收通过后立即反馈上线。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 22:00:02.304–22:00:09.629，7.33秒 |
| 请求停止 / 旧进程退出 | 22:00:09.701 / 22:00:09.791 |
| 完整一致备份及校验 | 22:00:09.991–22:00:10.737，0.75秒 |
| 新容器启动 | 22:00:10.932 |
| Compose healthy确认 | 22:00:41.508 |
| 本机健康/配置/资源验收 | 22:00:41.577 |
| 公网真实浏览器验收登记 | 22:01:30.371 |

未采样精确不可用窗口，不把约31秒启动至healthy等待或整体交付时长当作停机。

### 备份、回退与清理

- 受限备份 `/home/ubuntu/backups/account-pool-balancer/20261105T220002Z-pre-0ed063c`，
  含完整data/校验、Compose/.env、build/switch.log及deploy.json；敏感文件0600，不下载。
  data SHA-256：`a25c0d3c68605c8001a8205ee5ff940fe288b23dfd89420ebe7f00fab5c6c460`。
- 旧镜像 `account-pool-balancer:rollback-20261105T220002Z`（5bdfc24）保留。
  回退不自动恢复旧data，须保留上线后的账号、Cookie和订单写入，避免清除不确定订单标记。
- 用户已同意并完成本轮 `.analysis/tokenharbor-checkout-recovery/` 及登记的四个
  本地浏览器快照的定向清理，所属Mock已停止、端口关闭。其他任务产物及原始Trellis保留。
  本次部署未生成临时脚本或敏感预检副本；正式备份/源码快照/新旧镜像和部署记录不清理。
  公网验收浏览器已关闭。本节为记录更新，不因此再次重建/重启服务。

## 历史部署（2026-11-05，master：5bdfc24）

- 用户授权部署最新 master；预检期间另一窗口完成登录恢复提交，本次锁定业务版本
  `5bdfc2419cb0127d2332d8dce4cfc51145c99337`。后续 master 的 `cf46b9d`
  仅补验收文档，业务/构建路径无差异，不再次构建或重启。
- 固定镜像 `account-pool-balancer:release-5bdfc24-20261105T213227Z`，镜像ID
  `sha256:c70a2dbae6f766bd1b9f741ab12f17b2a644bfb207ac69d211bd19e5e08f66ae`。
  从明确提交干净归档到
  `/home/ubuntu/project/account-pool-balancer-releases/20261105T213227Z-5bdfc24`；
  不混入未提交修改，不修改/reset远程旧分叉工作树。
- 包含套餐模型/周额度门禁、会话及错误详情、逐账号独立代理和安全登录恢复。
  复用线上原Compose仅切目标服务，挂载、端口、环境、用户、网络别名、重启及
  资源限制均保持一致；不覆盖线上配置，不额外启用功能，不应用源码Compose新增
  容量参数。严格门禁下缺少有效来源/套餐观察的账号可能停调度，不绕过该策略。
- 新镜像使用断网数据副本启动成功并正常退出0；旧服务退出后完整一致备份data，
  gzip及SHA-256校验通过。**63个账号、10把下游密钥、2把导入密钥及完整配置**
  保持一致，metadata中的routingSecret已与备份逐项核对。
- 首轮发现注册库密文变化，没有回滚覆盖在线数据；仅在远端内存解密核对，确认
  **39条记录的ID、账号密码/Key、代理、入池关联、支付链接等关键字段及代理池均
  保留**，仅一条已入池记录的cookies/updatedAt正常刷新，状态不变。核对使整体验收
  超过5分钟，已主动说明卡点；不是构建或持续停机。
- healthy、restartCount=0、revision正确；本机与公网真实未登录浏览器healthz及首页
  HTTP200，app.js/tokenharbor.js/perch.js/app.css均HTTP200且SHA-256与源码一致。
  不主动发起注册、验证码、付款、修改代理或推理；后台正常只读观察/会话更新不被
  误判为数据丢失。本次不代表真实付费推理验收。完成后立即反馈上线，再补记录。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 21:34:45.341–21:34:54.451，9.11秒 |
| 断网副本启动预检 | 21:34:54.483–21:34:55.220，0.74秒 |
| 请求停止 / 旧进程退出 | 21:34:55.225 / 21:34:55.477 |
| 完整一致备份及校验 | 21:34:55.477–21:34:56.175，0.70秒 |
| 新容器启动 | 21:34:56.376 |
| Compose healthy确认 | 21:35:26.987 |
| 本机健康/配置/资源核验 | 21:35:27.074 |
| 公网与注册库数据验收登记 | 21:36:37.661 |

未采样精确不可用窗口，不把约32秒停止至healthy等待或整体交付时长当作停机。

### 备份、回退及临时产物

- 受限目录 `/home/ubuntu/backups/account-pool-balancer/20261105T213227Z-pre-5bdfc24`，
  包含完整data/校验、Compose/.env、build/switch.log及deploy.json，敏感文件0600，
  不下载、不入Git。完整data备份SHA-256：
  `fc26ceb4cc41ee57c2259f211980e33630e90135d850aca0e19b0a80ca913b69`。
- 旧镜像 `account-pool-balancer:rollback-20261105T213227Z`（2559c94）保留。
  回退前须检查新配置字段、模型/额度观察及上线后的账号/会话写入兼容性；
  不整体恢复旧数据覆盖新写入，不将旧版启动成功等同于新策略仍受保护。
- 用户同意后，于21:39:09 UTC定向删除已退出0的断网预检容器
  `account-pool-balancer-preflight-20261105t213227z` 及本目录preflight-data/preflight.log，
  均已复核不存在。完整备份及配置/构建/切换记录哈希未变，正式源码快照、新旧镜像
  和部署记录保留；线上容器ID、启动时间、重启次数未变，仍healthy、healthz HTTP200。
  清理记录已更新远程deploy.json和 `_tmp_artifacts.md`，不扩大到其他产物或Trellis。
  独立公网浏览器已关闭；不新建Trellis任务、不产生临时部署脚本。

## 历史部署（2026-11-05，TokenHarbor 归档与退回：2559c94）

- 用户授权推送部署，业务提交 `2559c94ab75f495f24e425edc108f8e893aa7e8e`
  已推送并运行；固定镜像 `account-pool-balancer:release-2559c94-20261105T171553Z`，
  镜像ID `sha256:bc4f20097ed93095a3e4974ed041e53c78307740f891e755667f25b8dae20ee0`。
- 源码从该提交干净归档到
  `/home/ubuntu/project/account-pool-balancer-releases/20261105T171553Z-2559c94`；
  不修改/reset旧分叉工作树、不在旧源码目录构建。复用原Compose只切目标服务，
  卷、端口、环境、用户、网络别名和重启策略均保持一致；无schema变化，不重复
  复制敏感数据做断网预检，也不重跑已通过的相关测试。
- 上线待处理/已入池分类及显式退回原注册机，目录文案改为“目录模型”。
  套餐模型映射本轮仅研究，不裁剪下游模型或更改推理路由/在线功能开关。
- 旧服务退出后完整一致备份data并通过gzip/SHA-256校验；本次切换前后的
  **57个账号、10把下游密钥、2把导入密钥、完整配置、routingSecret及加密注册库**
  均保持一致。没有替用户退回真实账号、重新注册、解验证码、付款或推理。
- healthy、restartCount=0、revision正确；本机及公网真实未登录浏览器healthz
  HTTP200。新页签存在，app.js/tokenharbor.js/app.css均HTTP200且哈希匹配；
  退回接口未登录时返回401。公网验收后立即反馈上线，补记录不再重建/重启。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 17:16:36.825–17:16:43.601，6.78秒 |
| 请求停止 / 旧进程退出 | 17:17:38.046 / 17:17:38.378 |
| 完整一致备份及校验 | 17:17:38.378–17:17:39.216，0.84秒 |
| 新容器启动 | 17:17:39.437 |
| Compose healthy确认 | 17:18:10.062 |
| 本机与数据验收 | 17:18:10.182 |
| 公网验收登记 | 17:19:02.955 |

本次未采样精确不可用窗口，不把约32秒停止至healthy等待当作停机时长。

### 备份与回退

- 受限目录 `/home/ubuntu/backups/account-pool-balancer/20261105T171553Z-pre-2559c94`，
  包含完整data/校验、Compose/.env、build/switch.log及deploy.json，敏感文件0600，
  不下载、不入Git。完整data备份SHA-256：
  `905e4fcbe658e8715a3f9c75320cdca3569140980a8cb27fc8e9ceff3ce379fa`。
- 旧镜像 `account-pool-balancer:rollback-20261105T171553Z`（d7b914f）保留。
  无需schema转换，但回退仍须核对上线后入池/退回等新写入，不能用旧备份覆盖
  在线数据或自动复活已移出账号。正式备份/镜像/源码/部署记录不作为临时垃圾清理。
- 本次无新Trellis任务、临时部署脚本或敏感预检副本；独立公网浏览器已关闭。

## 历史部署（2026-11-05，TokenHarbor 日志与批量入池：d7b914f）

- 用户授权推送部署，业务提交 `d7b914f20a31ede3bbb80638086ff0768a3b36eb`
  已推送并运行。固定镜像 `account-pool-balancer:release-d7b914f-20261105T160127Z`，
  镜像 ID `sha256:4f292612dd82dbcdbf116cef48fcb90c4712e2d81755ff04d68e67d3e6437d7c`。
- 从该提交干净归档构建，源码目录
  `/home/ubuntu/project/account-pool-balancer-releases/20261105T160127Z-d7b914f`；
  旧分叉工作树未修改/reset。仅更新目标服务，卷、端口、用户、环境、网络别名、
  重启策略均保持一致。本次仅前端变更，无存储格式修改，不重复复制敏感数据预检。
- 新增右下角日志重新展开入口、当前任务所选账号批量校验入池；进入注册机默认
  展开进行中/最新任务，轮询保持用户选择。入池仍使用原有后端实时套餐校验。
- 旧服务退出后完成完整一致 data 备份，gzip/SHA-256 校验通过。56 个账号、
  9 把下游密钥、2 把导入密钥、完整配置、routingSecret、加密注册库均保留。
  容器 healthy、restartCount=0；未调用真实注册、验证码、付款或推理接口。
- 本机及公网真实未登录浏览器 `/healthz` HTTP200；新版控件存在，
  tokenharbor.js/app.css 均HTTP200且SHA-256与本次源码一致。
  tokenharbor.js：`645629039cc3d604edda3396e9abf519b73f9c3c28205280c610cd152d89519f`。
  验收后立即反馈上线，随后仅补记录，不再次部署。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 16:02:01.796–16:02:08.603，6.81秒 |
| 请求停止 / 旧进程退出 | 16:02:54.442 / 16:02:54.781 |
| 一致完整备份及校验 | 16:02:54.781–16:02:55.633，0.85秒 |
| 新容器启动 | 16:02:55.867 |
| Compose healthy确认 | 16:03:26.453 |
| 本机与数据验收 | 16:03:26.566 |
| 公网验收登记 | 16:04:02.855 |

本次未采样精确不可用窗口，不把约32秒停止至healthy确认时间当作停机时长。

### 备份与回退

- 受限目录 `/home/ubuntu/backups/account-pool-balancer/20261105T160127Z-pre-d7b914f`，
  含完整data、校验、Compose/.env、build/switch.log及deploy.json；敏感文件0600，
  不下载、不入Git。data备份SHA-256：
  `f5e96ff58c5b03d00dfef87b1be3e95d6ac2715339811c70533a5d4ac65c3d04`。
- 旧镜像 `account-pool-balancer:rollback-20261105T160127Z`（ccafad3）保留；
  无需转换注册库schema，回退仍须核对上线后写入，不自动用旧备份覆盖在线数据。
  现有内网防火墙规则未变。正式备份、源码和镜像是必要审计/回退资料，不清理。
- 未新建Trellis任务或临时部署脚本，无新敏感预检副本；独立公网验收浏览器已关闭。

## 历史部署（2026-11-05，Token Harbor 批量操作：ccafad3）

- 用户授权放行20000–29999后推送部署。业务提交
  `ccafad3b22f2d7876a304f0b330c7e3cdb74e6f2` 已推送并运行；镜像
  `account-pool-balancer:release-ccafad3-20261105T153902Z`，镜像ID
  `sha256:76cfe530f784861ef9a056c13268638233470fde307db68947f6417c436e387f`。
- 持久化UFW规则**仅**允许 `br-8f33b6f41627`（ai-internal）上来自
  `172.18.0.0/16` 到本机 `167.114.158.4` 的TCP `20000:29999`。
  不向公网新增开放，不关闭UFW，IPv6规则未改；原规则已备份。
  切换前复用诊断容器/生产网络命名空间，同20007代理GET注册页
  HTTP/2为200/414ms、HTTP/1.1为200/260ms；切换后新容器TCP复查通过。
  证明之前是Docker到宿主机代理端口被拦，不是IPv6或HTTP版本不支持。
- 目标提交归档源码位于
  `/home/ubuntu/project/account-pool-balancer-releases/20261105T153902Z-ccafad3`。
  旧分叉工作树未修改/reset；复用原Compose只切目标服务，卷、端口、用户、
  环境、网络/别名和重启策略均保留。此版无存储schema变化，不重复复制敏感
  数据做离线预检；一致完整备份在旧服务退出后完成，gzip和SHA-256通过。
- 本机/公网healthz及新版资源验收通过；revision正确、healthy、restartCount=0。
  三个批量按钮已进入新版页面；tokenharbor.js SHA-256为
  `5d42b1f6889a2f09c360459e724c2d4393ca14c2cd1329b262d901300f61b534`。
  56账号、9把下游密钥、2把导入密钥、非账号设置、加密注册库和routingSecret
  均保留。未代替用户重试注册、解验证码、创建真实订单、付款或推理。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 内网防火墙放行 | 15:39:02.581–15:39:02.693 |
| 构建 | 15:39:04.108–15:39:10.609，6.50秒 |
| 请求停止 / 旧进程退出 | 15:39:10.644 / 15:39:10.907 |
| 完整一致备份及校验 | 15:39:10.942–15:39:11.597，0.655秒 |
| 新容器启动 | 15:39:11.798 |
| Compose healthy确认 | 15:39:42.380 |
| 本机和数据验收 | 15:39:42.506 |
| 公网验收登记 | 15:41:58.077 |

本次未采样精确不可用窗口，不把约32秒healthy等待当停机时长。公网真实未登录
浏览器确认healthz/资源HTTP200、资源哈希一致后立即反馈上线；公网验收时刻见远程deploy.json。

### 备份与回退

- 受限目录 `/home/ubuntu/backups/account-pool-balancer/20261105T153902Z-pre-ccafad3`，
  包含完整data、校验、Compose/.env、旧UFW规则、构建/切换/防火墙日志和
  proxy-acceptance.jsonl/deploy.json；敏感文件0600，不下载、不入Git。
  data备份SHA-256：`c5d2b403402025c5f7ed18533c66d0eb5d7eba87846e9cfe08e90f9db1b75348`。
- 保留旧镜像 `account-pool-balancer:rollback-20261105T153902Z`（759256c）。
  回退程序不自动撤销本次经授权的内网放行，也不整体恢复旧数据覆盖新写入。
  如要撤销网络规则，应只删除本节这条精确规则，不整份覆盖UFW或关闭防火墙。
- 本轮无新Trellis任务、无新增敏感预检副本；原诊断容器重新运行后正常退出，
  沿用 `_tmp_artifacts.md` 已登记范围，未经同意不删除。正式备份/镜像/源码保留。

## 历史部署（2026-11-05，Token Harbor 注册保存：759256c）

- 用户授权推送并直接部署；`master` 业务提交
  `759256c3293741641272f00276501ecaf2ca2596` 已推送并运行。
  固定镜像 `account-pool-balancer:release-759256c-20261105T144146Z`，
  镜像ID `sha256:b03256171ddef1522c9ed587aa87ba91c0305dc04bfe1a7d037a1f0c7aceaafa`。
- 源码从明确提交归档到
  `/home/ubuntu/project/account-pool-balancer-releases/20261105T144146Z-759256c`；
  未修改/reset远程旧分叉工作树，未在旧源码目录构建。复用原Compose仅切换目标服务，
  挂载、端口、环境、用户、网络别名和重启策略保持一致。
- 新镜像用断网数据副本启动预检通过并正常退出0；旧服务停止后完整备份data、
  验证gzip及SHA-256后切换。原56账号逐项相同，9把下游密钥、2把导入密钥及
  非账号设置保持一致；未发起注册/验证码/付费推理，不自动修改功能开关。
- 本机与公网真实未登录浏览器 `/healthz`、首页和3个资源均HTTP200；
  app.js/tokenharbor.js/app.css哈希与源码一致。页面已包含“仅保存”“保存代理池”
  和随机分配，不再包含自动入池选项。运行revision正确、healthy、restartCount=0。
- 首次远程命令在已确认healthy后回传超时；检查确认新版本正常运行，未重复构建/
  重启或恢复旧数据。补完数据与公网验收后立即告知上线，本节仅补记录，不重新部署。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 14:41:47.418–14:41:54.038，6.62秒 |
| 断网副本预检 | 14:41:54.038–14:41:54.591，通过 |
| 请求停止 / 旧进程退出 | 14:41:54.591 / 14:41:54.835 |
| 完整一致备份及校验 | 14:41:54.835–14:41:55.470，0.635秒 |
| 新容器启动 | 14:41:55.664 |
| Compose healthy确认 | 14:42:26.246 |
| 数据及本机补验收 | 14:46:58.855 |
| 公网验收登记 | 14:48:22.169 |

本次健康采样未取得完整持久化结果，**不报告精确停机时长**；约32秒的停止请求
至healthy等待、远程回传超时和整个交付时长都不等于停机时长。

### 备份与回退

- 受限目录 `/home/ubuntu/backups/account-pool-balancer/20261105T144146Z-pre-759256c`，
  目录0700、敏感文件0600，完整data备份5,812,413字节，SHA-256：
  `00eacea2bba4a7b7919f16b3c46e06aad83fa087a217928d7bd801be4b7e91a9`。
- 旧镜像 `account-pool-balancer:rollback-20261105T144146Z` 对应3cf9107，保留。
  **旧版不认识新注册库saved状态**：回退前必须检查上线后的新写入并制定兼容转换，
  不可直接交给旧程序，也不得整库恢复旧备份覆盖新账号/凭据/代理池。
- 停止的预检容器、preflight-data和preflight.log登记 `_tmp_artifacts.md`，
  未经同意不删除；完整备份、镜像、正式源码和部署记录保留。本轮无新Trellis任务。

## 历史部署（2026-11-05，master：3cf9107）

- 用户授权重新部署最新 master。部署前核对 GitHub/master 为
  `3cf9107ed7dd409b6b6b48322b12c88c2e3ad3b9`，从远端明确提交的干净归档
  构建，不使用旧工作树源码；本节只补记录，不再次部署。
- 固定镜像 `account-pool-balancer:release-3cf9107-20261105T134914Z`，镜像 ID
  `sha256:31ef35f787bc96d5416aa6425bd87f1ccf3924405373a9529df5ff87f48614cc`。
  原工作树仍为 `e6f063b`，未 reset/rebase；新源码位于
  `/home/ubuntu/project/account-pool-balancer-releases/20261105T134914Z-3cf9107`。
- 复用原 Compose，只更新目标服务；卷、端口、环境、用户、网络/别名及重启
  策略均保留。完整数据副本在 `--network none` 容器健康启动、停止退出0；
  旧进程正常退出后完整备份在线 data、验证 gzip 及 SHA-256，再切换镜像。
- OpenCode 保持启用、兼容转换保持关闭；所有非账号设置及9把下游密钥、
  2把导入密钥逐项相同。未主动发起推理、注册、验证码或修改选号策略。
  新策略已经部署，但是否使用仍取决于原有 Go 路由配置，不自动切换。
- 首次验收发现账号数量变化，**没有恢复旧配置覆盖在线写入**：61个原账号
  中6个 Perch 账号被同期在线管理删除，6条 `deleted` 日志逐条吻合；另有
  1个 Perch 新增，登记时共56个。所有仍存在的原账号逐项未变。核对这项
  并发管理变更使整体超过5分钟，已主动说明；并非构建或持续停机。
- 容器 healthy、restartCount=0，运行 revision 正确；本机和真实未登录
  浏览器 `/healthz` HTTP200。公网 app.js/tokenharbor.js/app.css 均HTTP200，
  SHA-256 与源码快照及本机一致；perch.js HTTP200，登录页正常。
  这是部署/数据保留验收，不代表真实客户端付费推理验收。

### 时间记录（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 13:49:15.087–13:49:21.785，约6.70秒 |
| 断网副本启动预检 | 13:49:21.794–13:49:22.647，通过 |
| 请求停止 / 旧进程退出 | 13:50:28.124 / 13:50:28.400 |
| 一致备份及校验 | 13:50:28.407–13:50:29.041，约0.63秒 |
| 新容器启动 | 13:50:29.259 |
| 首次观测 HTTP 恢复 | 13:50:29.632 |
| Compose healthy 确认 | 13:50:59.858 |
| 并发配置变化核对及最终验收登记 | 13:54:27.439 |

0.25秒采样首次失败13:50:28.379、首次恢复13:50:29.632，观测不可用约
**1.252秒**，存在采样误差。约32秒的停止至healthy等待不是实际停机时长。
上线验收反馈先于本节文档收尾。

### 备份、回退及临时产物

- 受限目录（0700，文件0600）：
  `/home/ubuntu/backups/account-pool-balancer/20261105T134914Z-pre-3cf9107`。
  含完整 data.tar.gz/校验、Compose/.env 备份、build/switch.log、脱敏deploy.json；
  敏感数据不下载到本地、不提交Git。
- 完整备份 SHA-256：
  `d3c815805511deb2587c4dfca787a723d447e724b2ed14b828db1728484b2b0d`。
- 旧镜像 `account-pool-balancer:rollback-20261105T134914Z` 对应9063068。
  旧版不认识 `least-requests-1h`，回退前须先用新版将相关路由改回旧策略，
  并核查新注册状态及上线后的写入兼容性；不得整体恢复旧备份、复活已删除
  账号或丢弃新增账号。此次没有执行回退。
- 停止的预检容器 `account-pool-balancer-preflight-20261105t134914z`、同目录
  preflight-data/ 和 preflight.log 已登记 `_tmp_artifacts.md`，待用户同意再清理。
  完整备份、正式源码快照、新旧镜像及部署业务记录保留；本轮无新Trellis任务。

## 历史部署（2026-11-05，Token Harbor 号池）

- 用户授权提交、推送并部署；`master` 已推送，业务版本为
  `90630682c07e17fd0dcba270f0ff58ab3e68bf1d`。本节补记录不再次部署。
- SSH、公网、容器、卷、端口及网络均不变：`ovh-server`、
  `https://ods.yeschoy.io`、`account-pool-balancer`、
  `/home/ubuntu/project/account-pool-balancer/data:/data`、`127.0.0.1:8790`、
  `ai-internal` 与 `opendesign-balancer` 别名。
- 固定镜像：`account-pool-balancer:release-9063068-20261105T091131Z`；
  `latest` 指向镜像
  `sha256:f733236840745f55fae33c4335d08d25c2a17c6fd655885fb498cb260048762e`。
- 构建源码为明确提交的干净归档：
  `/home/ubuntu/project/account-pool-balancer-releases/20261105T091131Z-9063068`。
  旧工作树仍为 `e6f063b`，仅 fetch 更新远程引用；没有 reset/rebase、覆盖配置
  或在旧目录 `up --build`。
- 新镜像用现有顶层数据文件副本在 `--network none` 容器启动成功、健康检查
  通过、停止退出 0 后才切换。切换前停止旧服务，备份完整 data，验证 gzip
  及 SHA-256；只更新目标服务，不触及其他容器。
- 验收确认 **52 个账号、9 把下游密钥、2 把导入密钥**及账号凭证/代理/
  provider/group、原有设置、环境、卷、端口、网络别名均保留；相关状态 JSON
  可读取。OpenCode 保持启用，兼容转换保持关闭，没有新增功能开关操作。
- 容器 healthy、restartCount=0，镜像 revision 正确；本机和公网 `/healthz`
  均为 200。真实浏览器未登录生产管理员账号，确认仅一个 Token Harbor 标签、
  注册机导航保留，app.js/tokenharbor.js/app.css 均 HTTP 200，哈希与源码快照
  一致（资源版本 `07f5a2e4de`）。未主动发起真实推理、注册或验证码消费；
  本节不代表真实 Codex/Claude Code 或 Token Harbor Key 推理已验收。

### 本次时间（UTC）

| 阶段 | 时间 / 时长 |
|---|---|
| 构建 | 09:11:32–09:11:39，7 秒 |
| 断网副本预检 | 09:11:39–09:11:40，通过 |
| 请求停止 / 旧容器退出 | 09:16:40.245 / 09:16:40.372 |
| 一致备份及 gzip 校验 | 1.601 秒，19,757,095 字节 |
| 新容器启动 | 09:16:42.441 |
| 首次观测 HTTP 恢复 | 09:16:42.751 |
| Compose healthy 及本机验收 | 09:17:13 |
| 公网验收登记 | 09:19:03 |

0.25 秒采样观测到不可用窗口约 **2.253 秒**，采样有误差；
不能把约 33 秒的停止请求至 healthy 等待或整体交付时长当成停机。
整体超过 5 分钟，已主动说明时间主要花在切换前的数据保留/回退核对，
并非构建等待。上线验收反馈先于文档收尾。

### 本次备份与回滚

- 受限目录：
  `/home/ubuntu/backups/account-pool-balancer/20261105T091131Z-pre-9063068`
  （目录 0700，敏感文件 0600）；含完整 data.tar.gz、校验、Compose/.env
  副本和脱敏 deploy.json，未下载凭据到本地或提交 Git。
- 备份 SHA-256：
  `bc24fb877a0bb745f65967c9d73d4231db940f63657ba715779eed76591f8b68`。
- 旧镜像：`account-pool-balancer:rollback-20261105T091131Z`，对应原运行
  `1c7a3b8`。**回滚前必须检查配置兼容性和上线后的新写入**：旧版不认识
  新注册配置或新增账号的某些字段，不能直接把新配置交给旧镜像，也不能
  自动覆盖旧数据备份。必要备份、版本快照、镜像和 deploy.json 保留。
- 本次停止的预检容器
  `account-pool-balancer-preflight-20261105t091131z` 和上述备份目录内
  `preflight-data` 临时敏感副本，已登记 `_tmp_artifacts.md`，待用户同意
  后清理；尚未删除。它们不是在线 data，也不等于完整回滚备份。

## 历史部署（2026-11-04）

- SSH：`ovh-server`；公网：`https://ods.yeschoy.io`。
- 容器：`account-pool-balancer`；宿主监听仍为 `127.0.0.1:8790`。
- 已部署业务代码：`1c7a3b810c93e76c2c29f3e8429d8470c2b7f6ce`。
- 固定镜像：`account-pool-balancer:release-1c7a3b8-20261104T062429Z`。
- 镜像 ID：`sha256:7c0164c89d604238d864095b5995dd2c6c26fc72958fc5501bfa3743e0d8b9cc`。
- 线上 Compose 继续使用 `account-pool-balancer:latest`，现指向上述镜像。
- 现有数据绑定不变：`/home/ubuntu/project/account-pool-balancer/data:/data`。
- `ai-internal` 网络和 `opendesign-balancer` 兼容别名保留。
- 部署切换时 OpenCode 总开关、兼容转换开关保持关闭，未调用其推理接口。
- 后续曾按用户要求通过管理 API 导入 6 个 OpenCode Go 账号，当时开关仍关闭，未做真实上游推理验证；数据已包含旧版不认识的新 provider。后续开关状态见下节。

## OpenCode 开关调整（2026-11-04 18:26 UTC）

用户报告新导入 Go Key 显示 `provider-disabled`、额度未知。确认账号本身
已启用，但 `opencode.enabled=false`；没有额度缓存，不代表 Key 已失效。
经用户明确同意，通过现有管理员 API 开启 OpenCode，再对当前唯一的 Go
账号同步模型和额度。配置 revision 为 5084，核对配置差异仅有
`opencode.enabled` 和 `revision`；兼容转换仍关闭，其他账号池设置不变。

- 账号状态变为 `ok`，模型目录 28 个，可路由 27 个。
- 5 小时、周、月额度查询均成功，已用 0%、剩余 100%，数据新鲜。
- `/healthz` 通过；没有重建、重启服务，无部署切换停机。
- 未发起推理请求，账号 24h 请求/Token 计数仍为 0；目录和额度读取成功
  不代表真实推理权限已验收。
- 修改前配置备份位于远程受限目录
  `/home/ubuntu/backups/account-pool-balancer/20261104T182614Z-enable-opencode/config.json`
  （目录 0700、文件 0600）。这是必要的业务回滚备份，不是待清理临时产物；
  未下载到本地或提交 Git。需撤销时应通过设置 API 仅关闭 OpenCode，
  不用整份旧配置覆盖后续在线写入。

## 重要：旧工作树不是当前镜像的构建源码

远程 `/home/ubuntu/project/account-pool-balancer` 的 `master` 保留在
`e6f063b`，有 3 个与 GitHub 分叉的历史提交。它与 GitHub `f10fa53`
的业务代码完全一致，差异仅为旧提交中误加入的 `.gomodcache`。没有
reset/rebase/清理旧工作树或删除缓存。

本次从 GitHub 目标提交执行 `git archive` 得到干净快照，在下列目录
实际构建 Docker 镜像：

```text
/home/ubuntu/project/account-pool-balancer-releases/20261104T062429Z-1c7a3b8
```

**不要直接在旧工作树执行 `docker compose up --build`，否则可能把旧
源码重新部署上线。** 下一次升级应先核对运行镜像的 revision 标签，
保留回滚镜像，fetch 后按明确的新提交构建独立源码快照，再在原目录
执行 `docker compose up --no-build --no-deps --pull never --wait`。
不要强行丢弃远程分叉提交；若要统一历史，另行确认并保留备份。

## 已完成的远程验证

1. 在 OVH 实际构建完整 Dockerfile，包括运行时 Python 依赖。
2. 用生产数据文件副本在 `--network none` 容器中启动，新程序健康检查
   成功并正常退出 0；副本没有挂载真实数据目录，未连接生产网络。
3. 停止旧容器后备份完整 `data/`，校验 gzip 完整性并保存 SHA-256；
   随后只重建目标服务，不操作其他容器。
4. 容器 healthy，restartCount=0；本机及公网 `/healthz` 均通过。
5. 切换时 47 个账号、8 把下游密钥、2 把导入密钥均保留；核对既有
   ID、账号运行/控制凭证、代理、provider、group、下游密钥及其 hash。
   管理员密码 hash、既有 upstream/routing/quota/perch 和各池错误/
   重试规则保持一致，未把凭证明文输出或下载到本地。
6. metadata、selection-counters、perch-secrets、sessions JSON 可读取。
7. 真实浏览器打开公网登录页，脚本 HTTP 200，Zen/Go 标签已进入新
   HTML；公网脚本与版本快照及本机脚本的 SHA-256 一致：
   `b630f50846f733c0fc52db548986a83ad6cc23d4f94f2925ea864265fb98489a`
   （95375 字节）。没有使用生产管理员密码登录或改动设置。

公网静态资源对 OVH 上直接 Python HTTP 客户端的请求在部署前就返回
403；没有更改 WAF 或配置代理/出口轮换，公网资源校验使用正常真实
浏览器。此处只验证部署/启动/数据保留，不代表真实 Zen/Go 和五种客户端
的推理验收，也不代表 Linux arm64 容器已实际运行。

## 回滚入口

受限目录（0700）：

```text
/home/ubuntu/backups/account-pool-balancer/20261104T062429Z-pre-1c7a3b8
```

其中 `data.tar.gz` 是旧进程退出后的完整一致备份（0600），旁有校验文件、
Compose/.env 副本、构建日志和脱敏 `deploy.json`。`.env`、数据及日志留在
远程，不提交 Git。旧镜像标签：

```text
account-pool-balancer:rollback-20261104T062429Z
```

**当前已导入 OpenCode Go 账号，不能直接执行下面的旧镜像降级命令。**
先确认并执行保留新写入数据的迁移/恢复方案，不要把原始在线配置直接交给
旧版解析。以下仅是用户明确要求回滚且已确认配置兼容后的镜像切换方法：

```sh
cd /home/ubuntu/project/account-pool-balancer
docker tag account-pool-balancer:rollback-20261104T062429Z account-pool-balancer:latest
docker compose up -d --no-build --no-deps --pull never --wait --wait-timeout 120 account-pool-balancer
curl -fsS http://127.0.0.1:8790/healthz
```

不要自动覆盖在线 data：升级后产生的账号/续期/密钥/统计会丢失，旧快照
的刷新 Token 也可能已失效。若之后已启用 OpenCode 或导入其账号，旧版
不能理解新 provider，需另行制定迁移/恢复方案。保留备份及回滚镜像，
不要无确认清理。

经用户批准，已删除预检容器 `account-pool-balancer-check-20261104t062429z`、
受限备份目录中的 `preflight-data` 敏感副本和 `preflight.log`。清理结果已记入
远程 `deploy.json`；线上容器未重启且保持 healthy。运行数据、版本源码快照、
回滚镜像、完整数据备份及构建/部署记录均保留，不随临时文件一起删除。
