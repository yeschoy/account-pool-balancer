# 第四阶段：授权部署及NewAPI隔离渠道真实验收

## 授权与交付状态
用户在第三阶段完成报告后回复「好」，明确允许升级Balancer、部署独立网关、创建一个隔离NewAPI测试渠道；**不切现有业务渠道**。本次不新建部署任务、不改NewAPI源码/全局重试/鉴权，不新建管理员凭据或会话。代码e7eb119已推送并发布；本记录为发布后文档，不要求再次部署。

| 交付项 | 状态 |
|---|---|
| Balancer owner v1 + 独立网关镜像 | OVH已构建并部署 |
| 两服务健康与原内网别名/资源/数据 | 通过 |
| NewAPI渠道260→网关→在线Mistral池→官方+搜索 | 真实合成通过 |
| start→pinned续轮、原调度准入、累计usage和计费 | 真实合成通过 |
| Codex形状省略sources的历史续问 | 经过常驻服务+NewAPI通过 |
| 真实Codex Desktop/CLI和来源UI | 未验收 |
| cached-only / indexed-only / open/find | 未交付，仍明确拒绝 |
| 普通业务渠道切换 | 未执行 |
| 线上刻意忙/RPM/失效等故障注入 | 未执行；本地矩阵已通过 |

## 预检、构建和切换（UTC 2026-11-22）
预检10:52:03：旧镜像release-317cb8a-20261122T072416Z/healthy/0重启/无OOM/User1000，8GiB、正确卷与ai-internal别名；磁盘278GiB。旧工作树05e0044且脏改动，不覆盖、不构建。git fetch+archive **e7eb119ca808766c8ec49f6dd2058c3d712bd766**生成release。

| 阶段 | UTC |
|---|---|
| Balancer构建 |10:52:30.451–10:52:37.931|
| 网关构建 |10:52:37.931–10:52:42.660|
| 请求停旧/确认退出 |10:54:46.148 / 10:54:46.512|
| 一致备份gzip校验完成 |10:54:49.531|
| Compose image-only切换/新启动 |10:54:49.531 / 10:54:49.770|
| Balancer Docker healthy/双内网验收 |10:55:20.098 / 10:55:20.150|
| 网关Docker healthy/核心验收 |10:55:25.486 / 10:55:25.533|
| NewAPI真实搜索+历史验证 |11:00:40–11:00:51|
| 真实公网浏览器healthz+7JS |11:04:25.479|

**退出至新启动约3.26秒**，不是把健康等待或整个部署当停机。服务上线与真实链路通过后均已即时反馈。

发布路径、镜像与回退见docs/ovh-deployment.md。完整一致data.tgz 100672332字节、config原文件/Compose/.env保留；回退只切镜像，不覆盖新数据。服务目录.env0700父目录/0600文件，不入Git；部署脚本只有环境变量名，真实Key仅SSH stdin/远端内存→受控运行环境。首次本地拼接脚本SyntaxError发生在SSH之前，未做远端变更；修正语法一次执行成功，没有重复停服务。

## 隔离NewAPI渠道
- 260 / WebSearch-Mistral-隔离验收 / 私有websearch-qa组，未加入UserUsableGroups、GroupRatio或自动组。
- Base `http://websearch-gateway:8791/r/mistral`，type58无转换，只有/v1/responses与/models，WebSocket关闭。
- 显式模型deepseek-v4.1-flash-1017→原生deepseek-v4-1-flash-1017；所有请求仍校验网关allowedModels。
- 旧式管理员Token正常返回ACCESS_TOKEN_LEGACY_RETIRED；没有伪造cookie、注入会话、改密码或临时关闭保护。由现有本机Postgres运维路径事务新增channel+ability；授权范围内测试配置，不改其他行；无密钥渠道投影备份。
- 使用已有管理员推理Key，通过固定渠道后缀原生能力，PinRetrySingleAttempt。没有新建/替换Key，没有全局RetryTimes变化/重启NewAPI。
- 原有全部渠道的id/name/status/base/models/group/mapping/setting/settings/header_override/auto_ban投影前后一致。Balancer config SHA256字节一致（revision6792、228账号、19Key）。

## 真实模型、搜索和计费证据
隔离合成新会话，强制一次public docs搜索，域名限制docs.mistral.ai；普通用户历史未重放。

| 请求 | 原Pool请求ID | owner | 输入 | 输出 | 缓存 | 状态/尝试 |
|---|---|---|---:|---:|---:|---|
| 搜索规划 |req_edc1d92647a56aa133b7276c|start|368|47|0|200/success/1|
| 搜索回填回答 |req_49724d633f30b234f0d573ba|pinned|1519|61|0|200/success/1|
| 新外部历史续问 |req_d44ac9b7f96afbd64aac1f62|start|1605|19|1536|200/success/1|

三轮均账号acc_c90e20df9cf0b67b6e5b06cd（zxg-13-studio-key），续轮selectionReason=request-owner，无overflow/换号；新外部续问重新start，沿普通会话绑定选中原号，不冒称跨外部请求永久owner。
- 搜索：HTTP200，10997ms、首次SSE事件403ms（非正文TTFT）、26事件，1真实web_search_call+1url_citation；累计1887in/108out/1995total。
- 续问：按Codex省略sources，HTTP200，763ms/首次SSE646ms/13事件、0搜索，1605in/19out/1624total/cached1536。
- 网关安全日志：第一请求modelCalls2/searchCalls1/usageComplete=true/searchCredits1/complete=true；续问modelCalls1/searchCalls0/usageComplete=true。没有返回owner header给网关调用方，未发现真实三类凭据出现在网关日志。
- NewAPI消费日志5006952/request 202611221100401079533178268d9d60mF3Q0R0记录1887in/108out、quota812；5006954/request 202611221100511008222178268d9d6vW9uZTwc记录1605in/19out/cache1536、quota62。两条request_policy仅attempt1/成功，用量与网关/池逐项相等。
- 计费沿既有非高峰公式：首轮(p1887×1+c108×4)/1e6=0.002319 USD/group前；续问(p69×1+cr1536×0.02+c19×4)/1e6=0.00017572 USD/group前。default倍率0.7与quota_per_unit500000后各自取整为812/62，合计874 quota；搜索1credit独立，未混作模型Token或NewAPI自动搜索金额。

## 服务、网络与公网
两服务均目标revision、有Config.Healthcheck、healthy、0重启、OOM=false；Balancer保留8GiB/User1000:1000与卷/双别名，网关256MiB/User1000:1000/只读/capdrop/仅回环端口+ai-internal。从new-api容器分别请求两个Balancer别名与网关healthz均ok。capabilities为v1/protocolresponses/ttl300。

真实未登录公网浏览器打开https://ods.yeschoy.io/，登录页正常；healthz与HTML中的7个版本化JS（app/perch/tokenharbor/deploy/x50/telnyx/altapi，v=495086c684）200，SHA256与发布源码一致。没有登录/挑战绕过，专用浏览器已关闭，证据产物登记_tmp_artifacts.md。

## 后续门禁
真实Codex请求配置/实际UI/普通客户端工具混用仍需验收；cached-only不能静默升级联网。不主动切业务渠道，不自动删除隔离渠道或停已授权服务。只补文档和提交，不再次构建/重启。任务保持in_progress而非完整客户端交付。
