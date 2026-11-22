# 独立 WebSearch 网关：2026-11-22

用户认可多客户端/多号池独立服务方案并要求开始。当前第一阶段代码在cmd/websearch-gateway、internal/searchgateway、internal/websearch，未修改原Balancer推理/调度代码或NewAPI。启动/合同/限制在docs/websearch-gateway.md；任务11-22-websearch-gateway。

已通过独立模块normal/race/vet/build、严格配置假凭据-check与差异格式检查。真实本地HTTP Mock验证两轮搜索+下一轮历史、另一上游、混合函数、逐帧输出、真实终态与未知usage、各种资源/失败/隐私边界。镜像未构建：本地Docker daemon查询12秒未响应。没有新生产推理/配置/部署。

仍需真实Codex/NewAPI声明、搜索引用显示与记账、跨轮账号连续性验证；高级搜索参数/preview/cached/open/find、跨协议、管理UI与多副本历史不在当前交付。禁止把原型当成已上线或全协议等价的web_search。任务保持in_progress。

本工作区并发Factory窗口多次提交误收新增网关文件；未reset/重写历史，只做后续定向提交。上线前须从明确干净版本审阅所需范围，不能盲目部署混合主线。临时文件.gotmp/websearch-gateway已登记_tmp_artifacts.md，未经用户同意不清理；Trellis日志保留。

## 第二阶段继续

按Codex0.167.0公开源码修复显式联网选项、来源被合法省略的历史回放，并按模型加强scope；支持域名硬限制/context提示和真实URL字符级引用。函数done后放开流式屏障，保持后续text/reasoning实时性；缺失/null/后轮失败用量不伪造总数。

OVH执行一次显式opt-in合成测试，固定一个最近成功的直连Mistral账号；3次模型调用+1次Tavily搜索，搜索/回填两轮约4652ms（46事件/1引用/完整usage），Codex形状历史续问也成功，总5.04秒。没有部署/改NewAPI或号池配置，没有重放用户历史。后续守卫只本地测试，不反复真实消耗。详细验收在validation-phase2.md；真实Codex客户端/NewAPI、多账号owner和cached-only保证仍未验收，不冒充零配置生产可用。

测试二进制在本地.gotmp/websearch-live.test和OVH /home/ubuntu/websearch-qa-20261122/websearch-live.test，均无内置凭据；真实密钥只经内存/SSH stdin/env。已登记临时产物，未经同意不清理。
