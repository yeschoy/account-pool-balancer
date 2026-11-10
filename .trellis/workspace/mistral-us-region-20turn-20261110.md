# 2026-11-10 UTC20时 — US固定区域GLM Flash 200K连续20轮

用户要求锁定US或EU再实验。先只读两区目录，均200且Flash可见；US21模型不列GLM5.3/DS，EU64模型三者皆有。选择US做本轮20次生成，EU仅目录未测推理，不自动切线上或追加EU。

为避免池级Base影响业务，复用当前正式92cc0d7相同镜像ID运行独立临时account-pool-balancer（非正式8790/非NewAPI/非上游直连）。单账号原代理/原路由策略、新临时下游Key，127.0.0.1:18790私有入口、无生产data挂载或网络别名、CPU1/内存1GiB无swap，配置认证只通过stdin→128MiB /data tmpfs。只该实例启用单Key无正文Reader/HTTP诊断、后台quota/catalog关闭；正式实例诊断保持空、不重建镜像/部署重启、不改Base/业务Key/Priority/PAYG。

正常admin API将同一Pro CHAT账号acc_78068235bc580c4f6eeb8bdb暂移无业务Key空组、active/pending排空及Vibe预算三次稳定。UTC20:26:42–20:30:31完成新合成会话20轮/high/1536，input224081→245555，每轮≥200000/HTTP200/自然stop/单attempt，完整累积思考答案，后19轮binding hit。实际出站Host均api.us.mistral.ai、Reader=prepared=client/19组旧前缀完整/亲和源目标稳定，同HTTP2/ALPN h2。首1/2轮0、后18轮96.10%–99.02%，无中途归零，全程加权89.18%、第2–20轮93.66%、第3–20轮98.63%；第8轮缓存量下降但仍96.10%，不隐瞒。

总input4686606/cached4179456/output7441。中位5.56秒、最大23.35秒。标准价等效USD0.20517668×1.1区域价=USD0.225694348；官方Vibe剩余272.563751148→272.338056800、净扣0.2256943479999990，20笔逐笔至9位吻合。正式账号预算窗口业务请求0，临时实例恰20请求；未测现金扣款/不相加API预算。

20:30:42.569临时容器正常退出0并释放tmpfs认证，20:30:42.588账号全部字段/正式全配置除revision恢复并logout。20:31:06独立核验41消息/20段完整思考答案哈希/末轮历史/原生usage/日志/真实US Host和Reader，正式实例ID/启动时间/全球Base/诊断空均未变、healthy0重启无OOM/NewAPI双入口成功。controller和只读observer进程均不存在/stderr0，无额外生成。停止容器保留待用户同意清理，不留运行测试服务。

较旧global20轮，本次US少一次中段掉零、中位更快，总额即便加价10%仍低约3.27%；但不同时间约7h、不同实例（旧global f1024e9正式/本次92cc0d7隔离）、不同背景流量/资源/新routingSecret、模型后续自然输出不同，不是随机同期A/B，不能确立区域因果或承诺US长期更稳。CF-Ray CMH仅Cloudflare边缘非GPU节点，固定地区非固定单worker。US缺另两目标模型，不建议直接切整池。

正式报告：`.trellis/tasks/mistral-cache-order-audit/research/us-region-20turn-20261110.md`。临时`.analysis/mistral-regional-us-20261110/`和远端同名tmp/run/checkpoint已登记_tmp_artifacts.md；思考checkpoint只远端0700/0600、不下载/回显凭据。容器apb-regional-us-test-20261110已停/tmpfs释放，待同意后定向清理；Trellis/其他窗口文件保留。

本轮仅实验控制器和报告，无业务源码改动，复用控制器SHA256/语法/真实全链路及恢复核验，不跑无关Go/Node全仓。核心缓存任务in_progress、不归档。已立即反馈20轮完成、1.1倍实扣、恢复正常及非严格A/B限制；不自动部署区域路由或继续EU实验。
