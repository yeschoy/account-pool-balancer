# 2026-11-10 DS/GLM当前低缓存深查

用户要求深度排查并核对官方文档，沿既有mistral-cache-order-audit；不新建任务，不改生产/重启/主动推理/续开旧诊断，不提交其他窗口注册/Dockerfile改动。

- 实际线上f1024e9、NewAPI cb5b639，healthy/restart0。当前DS有局部低缓存：10min121成功样本77.59%，但22条零缓存；NewAPI182/user703约21.49%、user17 DS1017约29.80%，不能用全池平均遮蔽。
- 两轮各120秒RAM被动采样23/41请求，所有采样器退出0，无pcap/真实正文落盘。第二轮纯离线探针调用当前生产同版本identity/cache/prepare，136个生产Go源码与f1024e9哈希一致；仅匿名管道RAM，不建Server、不发HTTP，探针退出0。
- 核心新证据：182/user611明确Session、同账号、稳定cache key与X-Affinity、41工具、跨轮完整prepared历史保留，成功61549/60288=97.95%→62039/384=0.62%→64351/61952=96.27%。前条完成后再发后条，与NewAPI流ok和真实上游trace对齐。另user17同号36工具69.1%→0.99%→98.7%；不是TTL/换号/旧消息改写的统一解释。
- 41样本全部cache有效/affinity等于最终cache；31个相邻prepare比较26完整旧前缀、5真实前缀变化、0纯重排。同Session/user69存在20/21工具与两套system/首user交替，说明业务Session不等于同模型prompt，应由客户端分工作流维持稳定前缀；不能拿它解释所有高→低。
- metadata10:15/16/17三完整分钟r/cp/ck/ckr与日志完全相同；已确认当前统计不多加cached。
- GLM当前全渠道最近2h只有13-token检测，没有新版长会话。旧低缓存3条GLM0%、22条Flash60.37%(低组18.76%)不能冒充本次新样本。
- 官方llms.txt找准DS文档slug(deepseek-v4-flash-1017)、缓存/Chat schema/ThinkChunk回放/三模型卡及精确价格；通用10%文案不能覆盖Flash20%或DS约2.33%的精确缓存价。无官方TTL/连接锁KV保证，不用best-effort搪塞具体因果。

边界：pure-prepare是同版本重算，不是在线真实Body/GetBody Reader/TLS；优先怀疑上游内部cache routing/eviction/容量，但没内部节点不能100%定责。最后真实Reader/source/connection/trace诊断需针对当前低Key新窗口及一次短重启授权；GLM需原长会话新自然请求。未主动向Mistral发送业务日志。

正式证据research/ds-glm-deep-20261110.md；临时本地/远端二进制、脚本、公开文档和脱敏回执已登记_tmp_artifacts.md待同意清理，Trellis保留。专用文档浏览器已关闭、10:18所属采样/probe进程0。config最终5673/mtime10:09:49为外部写入，本轮不冒称全程固定配置。无新业务补丁/部署，核心任务继续in_progress。
