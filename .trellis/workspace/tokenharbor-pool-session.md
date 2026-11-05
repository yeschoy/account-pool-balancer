# Token Harbor 号池完善（2026-11-05）

基线 b586406。用户要求完善 Token Harbor 号池，按 Codex/Claude 会话和缓存标识尽量复用同 key，按官方协议而非强制兼容，并提供请求/错误日志及概览。

已完成：修复 provider/base 路由缺口；独立 native key 安全与导入/路由/错误规则；会话优先/缓存键后备；Responses key 范围持久绑定、禁止盲切；目录刷新与凭证源校验；管理台独立标签、账号批量、密钥、概览/日志/设置；nullable usage 与 cache read/write 统计；消费确认硬停止。文档 `docs/tokenharbor.md`，契约 `.trellis/spec/backend/tokenharbor.md`，详细验收 `.trellis/tasks/tokenharbor-pool/validation.md`。

全仓 test/race/vet/build、JS 语法与本地浏览器 mock 验证通过。一次旧 Modal 报错文案回归已修复并完整重测。没有使用子代理、真实上游 key、生产访问或付费推理。未实机验收 Codex/Claude Code，不宣称供应商内部缓存必定命中。

本仓库没有 Trellis 脚本，沿用现有轻量本地 task/spec/workspace 记录形式；未改动其他任务或上级工作区状态。临时文件按 `_tmp_artifacts.md` 登记，停止本轮两个本地进程后待用户授权清理；Trellis 记录永久保留。
