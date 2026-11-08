# Mistral 额度与完整号池交付（2026-11-08）

用户指出aaa09a6部署后“刷新额度”仍是未知，随后要求Mistral像TokenHarbor一样有完整可配路由，并要求以后新增号池不得遗漏。

## 本轮实现
- 原来只读models+硬编码未知，现接通whoami与budget的API/CHAT选择、强类型预算、快照/metadata、API freshness和实际页面展示；失败、403、过期、来源变化/晚写不伪造当前余额。
- 复用独立路由/错误/重试设置，补Mistral独立Quota、目录刷新、详情日志、真实预算排序及精确错误码编辑。数字3800原先无法匹配，已无损支持JSON数字，规则优先级与重放安全门不变。
- 真实本地假数据浏览器验证同步/403/设置与规则保存、实际进程重启保留；全仓normal/race及最终受影响定向race、vet/build/Node/diff check通过，详见任务validation.md。
- AGENTS.md和backend/provider-onboarding.md成为未来新增号池强制门禁：账户/隔离/额度/全部适用路由参数/错误和重试规则/日志/默认模型/保存重载/部署验收；不适用项要解释，不交付占位或无效参数。

## 边界与下一步
本轮业务与所有Trellis变更需一并提交；不擅自推送或部署。任务11-08-mistral-quota-refresh保持in_progress，等待用户明确生产发布授权。线上仍aaa09a6，不把Mock验证当线上生效。新增metadata/config字段需兼容预检及一致备份后发布，回退先核查新写入。

本地浏览器/Go/mock进程及50267/50266监听已关闭；.analysis/mistral-quota-refresh仅假数据，登记_tmp_artifacts.md待同意清理，所有Trellis记录保留。没有新增生产Key调用、推理、注册、付款或在线配置变更。service仍按用户要求暂不做。
