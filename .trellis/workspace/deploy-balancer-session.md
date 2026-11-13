# Deploy Balancer 独立分支会话（2026-11-13）

## 来源与范围
用户要求另一个分支实现Deploy号池，参考OpenDesign管理台的路由/错误规则/重试规则/概览，并授权使用其提供的Key进行测试。基于9410a52新建feat/deploy-balancer和独立worktree，原master既有未提交改动未迁移或重置。

## 本次交付代码
- provider/保留组/Opaque Secret校验及导入、下游Key隔离。
- 独立路由/轮询并发、原生模型目录和metadata来源保护、价格表、详细日志及错误/重试配置。
- 原生Chat/SSE透明转发和路径白名单、无工具删除、不确定发送/输出后不重放、截断流记失败；405组织暂停/429完整Retry-After。
- 管理台Deploy标签、账号批量/CRUD、路由和规则可视编辑/保存、概览与日志、正确默认模型测试、未知余额说明及Deploy导入示例。
- backend/deploy规格、使用文档及任务research/validation已保留。任务仍in_progress追踪完整onboarding剩余项，不归档为全完成。

## 证据与质量
全量Go普通/竞态、vet/build、node语法、12套前端行为测试（导航11池）通过。真实浏览器以本地Mock验证配置与规则的保存/刷新、API/磁盘一致以及跨池配置不变，最新二进制重启后设置仍保留，退出清除凭据DOM/缓存。

用户授权真实Key：目录两模型200、默认DeepSeek管理测试200；经本地Balancer转发GLM非流式和DeepSeek流式均200、每次1账号，后者保留reasoning并收到DONE。没有支付、注册、上游Key生命周期、生产配置或部署动作。

## 剩余/交付状态
组织余额/账单需要独立portal会话，尚未绑定，明确not-connected/未知且不伪造零；既有注册机没有搬入本分支。完整provider-onboarding未全部满足，不称新号池完整交付。未推送、未合并master、未部署或验收NewAPI生产渠道。

临时产物登记本worktree的_tmp_artifacts.md，测试data/config.json含真实测试Key（0600）且被忽略。提交前扫描候选源码/文档和业务日志，未发现真实测试/下游Key泄漏。只停止本次启动的临时服务与Mock，文件未经用户同意不删除，Trellis永久保留。
