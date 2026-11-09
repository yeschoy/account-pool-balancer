# 最近报错修复、OVH上线与10分钟观察（2026-11-09 UTC）

用户要求排查近期号池报错，修复后只跑相关回归、部署OVH并观察完整10分钟，有可修问题继续处理。沿现有mistral-cache-order-audit，不新建任务；项目本地缺workflow/scripts，直接加载现有task/spec及上级共享指南。其他窗口注册/指纹/Dockerfile改动完整保留，未提交或部署。

- 最近2小时Mistral13条本地400与NewAPI180同13条retention错误对齐；24小时各池旧错误/限流尝试/最终失败及其他直连渠道分开处理。
- `37332be`按本次授权兼容精确DeepSeek Chat标准24h/in_memory缓存提示，不改变cache key/原身份/历史/思考/工具/数字，拒绝未知值和重复；不承诺Mistral TTL或数据保留政策，其他模型/协议/池不变。新HTTP回归先红0.845秒，20项相关回归绿0.809秒，无全仓/race/Node/生产推理/重放。
- 同时发布已验证的170be84原生thought与同值legacy去重，不声称交错布局或原188缓存已修。614个干净Git文件哈希核验，旧c1健康/双入口，构建6.96秒、完整一致备份、沿实际Compose仅切镜像、诊断仍关闭。191账号/186TH核心资产/config原字节/注册库/路由secret/全部运行保护保留，healthy/零重启、双内网入口及真实公网登录页/healthz/四资源200/SHA256匹配。上线验收即反馈，不因记录重启。
- 固定UTC21:11:35–21:21:35完整600秒，21:21:37.114结束，42次两服务采样全健康/同revision/零重启/无OOM，配置hash未变；观察进程和两专用浏览器均结束。
- Mistral原13错误Key6次单attempt200，NewAPI180的1非流式+5流式成功/usage完全对齐，无400/422/超时；仅6条自然样本、无原正文留存，不冒称全部客户端/TTL/原GLM缓存验收。OpenCode35成功零失败；OpenDesign50成功1失败，不能称全池零错误。
- 观察发现OpenDesign503继续追链：req_232bb23ae205c3e9d31efb2a先并发400、再另一账号余额402，触及线上明确maxAccountsPerRequest=2；规则/换号/冷却按设置工作，workflow另有候选，不虚称全池没钱。未擅改用户安全上限/开启付费/伪造成功，本资源与配置问题保留待确认。另一并发事件已恢复200；NewAPI166的2次GPT流式失败经key哈希/入口确认不经过号池。

部署时间、正式release/镜像/完整备份及回退边界见docs/ovh-deployment.md，详证见任务research/recent-errors-retention-20261109.md。本次Mistral兼容已交付；原GLM缓存、交错布局和OpenDesign剩余问题不冒称完成，不归档任务。临时目录/公开快照登记_tmp_artifacts.md，经用户同意再清理；Trellis、正式备份/镜像保留。无npm/pnpm新产物。
