# Mistral Responses终态与三模型思考档位（2026-11-09）

## 用户要求与范围
用户批准前轮建议1～3：Responses结束判断，多模型/工具兼容回归，过时文案修正。追加明确DeepSeek V4.1 Flash 1017、GLM5.3、GLM5.3 Flash支持low/high/max、未传high。取代旧max→high策略；没有新增后台配置或模型别名。按复杂任务留存计划/证据，子项目缺脚本，沿用已有本地任务格式手动归档，不改上级项目任务指针。

任务：`.trellis/tasks/archive/2026-11/11-09-mistral-stream-reasoning/`。
业务提交：`3ec9611 fix(mistral): complete Responses streams and preserve reasoning tiers`。

## 实现
- Mistral Responses首帧/后续completed/incomplete，event或data.type终态立即结束；不再等EOF。空output/纯工具调用是合法协议终态；incomplete不承诺内容完整。
- 无终态的EOF/读故障明确失败；未提交前502，已提交HTTP200保留并追加一个error。不生成假completed/DONE/usage、不重放/换号、不归咎Key，失败TPS未知。后续failed/error及时结束；客户端取消仍499。其他池与Mistral Chat的旧终态策略不变。
- 精确上游IDdeepseek-v4-1-flash-1017、zai-glm-5-3、zai-glm-5-3-flash：Chat reasoning_effort与Responses reasoning.effort保留low/high/max，缺省high；既有null/无效档位归一high政策延续，显式冲突/重复仍400。Responses其他reasoning字段/input原样保留交上游验证；三模型Chat兼容assistant reasoning别名及思考开关。
- DeepSeek专属store/图片/developer/seed等不扩散至GLM；Large4保留原生medium且不加默认。工具Deny结果、apply_patch多行/转义/大数、缓存HMAC与X-Affinity保持。
- UI/README/指南同步预算、历史未知、非钱包与共享预算不可累加说明；明确客户端尚未端到端验收。规格补mistral-responses.md并更新思考策略。

## 验证
- 旧代码先复现4个首帧终止等待超时、缺终态EOF被当成功；修复后定向通过。
- 全仓Go normal/race、vet/build、所有Node行为/JS语法通过；最终full race server81.414s，最后夹具校准后的Mistral定向race7.720s。四模型×双协议×流/非流Mock及隔离/本地拒绝/不重放均覆盖。
- 真实本地浏览器查看零账号Mistral概览/添加窗口，三档和预算说明可见；管理员API确认quota=false、0账号、Base为不可达127.0.0.1:1。没添加账号/改线上数据或发推理。
- 公开文档/本地专用浏览器已关闭，本轮临时PID73795停止，53555监听关闭。中间构建/假data/回执/公开资料文件登记_tmp_artifacts.md，未经同意不清理；Trellis永久保留。

## 交付边界
已本地提交与验证；未推送/部署，未改变线上配置、注册/付款或发真实推理。真实Codex/Cherry Studio/pi端到端、实际上游三档效果/授权、生产缓存命中尚未验收。不能把Mock矩阵当生产可用证明。后续若发布仍按docs/ovh-deployment.md固定流程与双内网别名门禁执行，不在旧树裸构建。
