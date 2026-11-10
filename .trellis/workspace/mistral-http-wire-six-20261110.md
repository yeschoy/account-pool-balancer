# 2026-11-10 — 授权HTTP诊断发布及6轮200K Flash真实Reader取证

## 授权与范围

用户先同意上游请求/响应头检查，说明需要最小单Key诊断发布/短重启后再明确回复「可以」。仅本轮新增无正文取证扩展/OVH发布/最多6轮200K合成连续请求；单独临时Key、隔离一个可用账号，结束禁用入口/恢复分组。沿既有任务，不开新任务，不启用全池detail、区域/Priority/PAYG或其他业务功能，不采集业务正文，不混入其他窗口注册/指纹/Dockerfile未提交改动。

## 实现与验证

复用既有cacheAudit默认关闭、精确KeyID/30min/128请求，追加实际HTTP trace所见脱敏头、响应头、protocol/ALPN和有界完整usage；原Reader/GetBody HMAC/前缀/连接/写入、选号/重试/SSE/usage统计保持。未知头值HMAC、认证/Cookie值恒掩码、只暴露有限Cookie名称；usage数值/未知计数/精确大数保留，任意字符串脱敏，8KiB/depth/node界限。JSON/SSE/detail trace组合/HTTP2/首帧/隐私/边界/关闭零分配相关回归通过：Mistralnormal11.671s、相关race8.143s、server vet/cmd构建。隔离干净副本验证，无无关全仓/Node重复。

实现提交推送`92cc0d7024c49c6da80c55a1a7b6a1e148b93e39`。新专用Key `ck_058a943b722e55f52cf86276`/组mistral-http-audit-20261110，最初空组，无业务Key移动。

## 已部署

复用既有引擎，一次预检→629干净源码哈希→构建7.35s→旧进程干净退出→完整一致备份/gzip/SHA256→线上Compose仅镜像+授权诊断变量→Docker healthy/0/无OOM、双内网/公网登录页/healthz/四资源SHA256。新进程UTC14:40:05.765，healthy14:40:36.113；旧退出至首HTTP观测6.61秒，非精确用户停机。191账号/186注册、routingSecret、Mistral注册和运行配置/资源/13硬隔离保留。上线通过后立即告知用户，补文档不重启。

正式release`/home/ubuntu/project/account-pool-balancer-releases/20261110T143926Z-92cc0d7`；备份`/home/ubuntu/backups/account-pool-balancer/20261110T143926Z-http-audit-92cc0d7`，旧镜像rollback-20261110T143926Z保留。详细阶段时间/哈希见docs/ovh-deployment.md，回退不能覆盖新写入且必须清空诊断变量。

## 真实6轮已完成

UTC14:43:31–14:45:56，经正式号池、同Pro CHAT账号acc_78068235bc580c4f6eeb8bdb/代理/session/cache/X-Affinity，新合成会话，不续旧checkpoint。input224081→229637/high/1536；6次全200/自然stop/单attempt/一次成功写入。实际Reader全量HMAC=prepare=client、5组旧消息前缀完整、无并发重叠、source/target/实际亲和稳定，6轮同一HTTP2连接ALPN h2。缓存0/0/0/0/98.8244%/97.8240%，官方usage与SSE/日志一致，总input1360995/cached450432/output2147；官方Vibe实扣USD0.15117091与计价1:1。

原生响应头20个无截断：仅__cf_bm Cookie（值未存），无X-Affinity回显/区域/worker/节点或其他路由Cookie。CF-Cache-Status=DYNAMIC所有轮相同，Cloudflare官方说明是CDN非KV缓存；CF-Ray均YYZ仅边缘位置不代表GPU。__cf_bm为Bot Management/Bot Fight Mode，未回传也能高命中，不支持必需回传解释。X-Max-Retry-Attempts-Reached头存在但未知值指纹化，全6轮相同，不能猜true/内部重试；usage完整而无service_tier字段。

UTC14:46:07专用Key禁用/账号组恢复/管理员logout。14:48:23独立核验exit0/stderr0/无进程、13消息和思考/答案哈希正确/末轮完整历史、healthy0/无OOM/双入口、只采目标6条其他0；与发布一致备份最终配置仅新Key enabled=false/revision变化，13硬隔离相同。复用旧控制器target_key_fields_restored=false是有意关闭专用入口，非恢复失败。初步收尾曾计划保留指向禁用Key的变量到期，但发现启动校验要求Key启用，会导致下次启动失败，因此没有以此作为最终状态；已主动告知并复用原引擎做同镜像不构建的必要关闭切换，完整一致备份、Compose仅清空变量。14:56:54.934新进程，14:57:25.298 healthy/双入口通过，14:58:01最终变量空/专用Key禁用/账号组恢复/13隔离与配置保留、公网healthz200。无额外推理或续采，后续普通发布保持变量空；新备份20261110T145641Z-http-audit-off-92cc0d7/旧镜像保留，当前Compose最后诊断关闭override。该次旧退出至首HTTP约6.63秒，不报成全操作停机。

## 交付和未定问题

完整报告`.trellis/tasks/mistral-cache-order-audit/research/http-wire-six-turn-20261110.md`。已立即告知完成、关键Cookie解释、强Reader证据和停止状态。

本次没有发现转换/实际正文上传/亲和头/账号/外部连接漂移或遗漏必需路由返回字段。更怀疑上游缓存选取/可见性/逐出或服务端内部前缀，但同公网连接不等于同内部GPU；下一步应交官方trace查询内部，不再盲调TTL/开Priority/区域/额外付费。原生任意交错布局/closed元数据及真实NewAPI/OpenCode用户链路仍未验，核心任务in_progress、不归档。

临时产物`.analysis/mistral-http-audit-20261110/`/远端同名tmp登记_tmp_artifacts.md；完整合成思考checkpoint只远端0700/0600，无凭据/思考下载。专用浏览器ovh-mistral-http92已关闭；临时文件清理需用户同意，Trellis/正式发布备份旧镜像保留。其他窗口修改原样保留。
