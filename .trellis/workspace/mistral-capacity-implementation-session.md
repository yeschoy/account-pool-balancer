# Mistral容量正式改造：本地阶段交付

2026-11-09，收尾核验12:26 UTC。业务提交 `1c0a410`（51文件），基于当时HEAD `b5d0dbb`；没有推送/部署/生产配置/真实上游推理。

## 执行与范围
- 用户确认A：整个Mistral池保留的会话绑定条数；随后明确继续，授权主会话直接串行。Pi子agent因chord依赖缺失全部在启动前失败，没有独立实现/审阅产出，不修全局工具环境。
- config/index/source、pool owner-only/健康/独立LRU/过期heap/等待唤醒、server内存/上传/流写/Transport/JSON/SSE/脱敏/采样、Stats稀疏/去重和日志队列字节预算、完整settings/API/UI/持久化/概览均进入正式源码。
- 旧配置0/省略容量沿用共享4096；1–1000000独立，不是每账号会话数/并发。所有精确边界见 `.trellis/spec/backend/mistral-capacity.md`。
- 回归中修复真实HTTP上传错误清理阻塞、并发provisional实际发送者确认、可变config索引失效、账号换组后旧lease释放唤醒当前组及旧Revision防容量回滚。

## 验证
- 主工作树全仓normal/race（count=1）、vet、build、全部9套Node与JS语法、diff检查通过；原始回执在任务 `research/implementation/`。
- 专属真实浏览器在0账号localhost假data上保存容量50000/预算128MiB/等待16/上传45秒/写20秒/采样25%/连接7和3。刷新、配置文件、停止重启后UI/API一致；根OpenDesign仍512/32和quota=true，概览独立占用/预算/日志队列正确。已退出管理员、关闭专属浏览器与所属测试进程。
- 提交后复用原先干净的 `.analysis/mistral-capacity-lanes/resource`，仅检出 `1c0a410`，构建clean-odb及config/pool/logstore/stats/quota/server定向回归、全部Node/语法再次通过（server9.542秒）。该树仍干净；不依赖主树其他窗口未提交的注册/Dockerfile修改。回执 `clean-commit-tests.txt`。
- resource树HEAD现1c0a410；stats-log树仍62ac872且干净。未移除工作树/产物，未改其他窗口工作。
- 本地短基准4K账号健康开启命中836.2ns/1064B，冷选号1.737ms；400账号详情关闭Handler摊销163174ns，逐请求P95/P99=2.101/2.718ms，559790B。只属Apple M4短Mock，不等同端到端吞吐；完整口径见docs/mistral-capacity.md第8节。

## 保留边界
- 未部署、未推送、未做真实3亿TPM验收/付费推理；没有擅自修改线上容量、连接或详情开关。
- 仍需真实token/RPS/持流/正文/新会话比例/SLO、组织模型授权、费用上限，及完整NewAPI/Nginx/代理/网络/周期保存持续开环测试。冷选号排序、无显式身份fallback、密集24h统计和百万绑定快照没有假称已消除。任务保持in_progress，不归档为容量认证完成。
- 既有Dockerfile、mistralsignupstore、mistral_registration、tools/mistral注册/指纹及本轮收尾期间其他窗口新增的text-block测试/研究保持原样、不夹带提交。
- 临时目录、独立树、4个自动浏览器假数据快照及本地回执已登记 `_tmp_artifacts.md`，未获同意不清理。Trellis规格、研究、回执、日志永久保留；没有npm/pnpm安装产物。
