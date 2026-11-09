# 2026-11-09 — Mistral 3亿TPM完整链路与会话容量研究

## 范围

用户要求深度研究整个调用链支持3亿TPM，并自定义绑定会话数。按复杂研究记录任务`.trellis/tasks/11-09-mistral-capacity/`，本地无Trellis脚本，不修建脚手架。保留既有注册机/Dockerfile未提交修改。

## 交付

权威报告`docs/mistral-capacity.md`：客户端/Nginx/NewAPI参考源码→Balancer解析/身份/适配→Pool/固定代理/组织限制→SSE/日志/统计/持久化。区分当前代码事实、Apple M4/Go1.26.8本地测量和未故障注入/未生产核验项。

4000账号smart+health sticky hit仍6.17ms/8.41MB累计分配，CPU/alloc/锁定位全量健康/候选构建；只改`.analysis/`源码副本的owner-only实验48.24µs/3.74KB，Pool normal/race通过，未进入生产源码。实验仍线性查ID且改变Candidates投影，不能直接发布。完整Handler本地短基准全详情约4倍摊销耗时，Stats双固定分钟数组每活跃账号599040B、4000约2.23GiB；长历史重复扫描/无system解码、Config视图使脱敏缓存失效也有量化证据。

设计还覆盖总输入/响应预算、慢客户端逐次写deadline、有界等待/定向唤醒、连接配置作用域、统计稀疏化和绑定恢复/导出成本；不把缓存折扣等同TPM折扣，不将同组织多Key容量相加。典型10K/100K/1M总token分别需500/50/5RPS，持续并发取决于真实时长。

## 状态与边界

任务planning；PRD/design/implement/research/validation与可复现overlay夹具/脱敏文本证据保留。待用户确认本池总绑定容量还是每账号会话数；推荐本池独立容量，每账号上限单独可选。没有正式业务Go/JS改动、自定义容量功能、生产配置/推理/压测/部署/重启。

基线Pool/Server定向测试（缓存命中）、所有合成基准/剖析、实验Pool normal/race、gofmt/JSON/文档围栏/diff check通过；未重复全仓normal/race/vet/build或UI/E2E，不冒称正式交付。初始其他修改不纳入提交。

临时`.analysis/mistral-capacity-20261109/`和公开文档浏览器溢出登记于`_tmp_artifacts.md`，未经同意不清理，Trellis始终保留。
