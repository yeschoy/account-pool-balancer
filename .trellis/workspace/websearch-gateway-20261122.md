# 独立 WebSearch 网关：2026-11-22

用户认可多客户端/多号池独立服务方案并要求开始。当前第一阶段代码在cmd/websearch-gateway、internal/searchgateway、internal/websearch，未修改原Balancer推理/调度代码或NewAPI。启动/合同/限制在docs/websearch-gateway.md；任务11-22-websearch-gateway。

已通过独立模块normal/race/vet/build、严格配置假凭据-check与差异格式检查。真实本地HTTP Mock验证两轮搜索+下一轮历史、另一上游、混合函数、逐帧输出、真实终态与未知usage、各种资源/失败/隐私边界。镜像未构建：本地Docker daemon查询12秒未响应。没有新生产推理/配置/部署。

仍需真实Codex/NewAPI声明、搜索引用显示与记账、跨轮账号连续性验证；高级搜索参数/preview/cached/open/find、跨协议、管理UI与多副本历史不在当前交付。禁止把原型当成已上线或全协议等价的web_search。任务保持in_progress。

本工作区并发Factory窗口多次提交误收新增网关文件；未reset/重写历史，只做后续定向提交。上线前须从明确干净版本审阅所需范围，不能盲目部署混合主线。临时文件.gotmp/websearch-gateway已登记_tmp_artifacts.md，未经用户同意不清理；Trellis日志保留。
