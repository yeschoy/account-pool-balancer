# Token Harbor 注册保存、代理复用与入池保护

- 用户两轮要求：取消注册自动入池并检测真实套餐；新增仅保存、随机/正序可复用代理、独立持久化代理池和留空自动复用。
- 已完成并归档：`.trellis/tasks/archive/2026-11/tokenharbor-registration-guard/`，PRD/design/implement/validation/bug-analysis保留。项目未提供Trellis脚本，未引入额外工具系统。
- 实现：saved记录加密落盘且无上游调用，明确开始才注册；代理输入/保存池/默认的优先级，有放回随机分配，显式直连通过proxyBound固定；原始认证不回显。重复提交保留原任务和凭据。
- 注册机不再自动入池，旧autoImport=true后端拒绝；套餐只读GET官网pricing，使用保存会话和账号出口，漂移/失败为未知。显式import实时复查，只有邮箱已验证且当前有效付费套餐放行；不把checkout或模型目录当付款证明。
- 最终质量门：全仓Go tests/race/vet/build、node syntax、diff check通过。独立本地浏览器Mock覆盖保存池→留空随机保存3账号（上游计数0）→明确开始其中1账号→Free拒绝→付费仅手动入池，并验证后端状态/掩码。
- 真实业务仅复用既有会话只读核对，当前仍Free。未注册/创建订单/付款/推理，未改生产账号或部署；真实付费分支仅Mock验证。既有会话文件未覆盖。
- 自建Mock进程和两个浏览器已停止/关闭，端口无监听。临时文件登记 `_tmp_artifacts.md`，待用户同意再清理；Trellis日志保留。
- 既有 `.worktrees/` 未跟踪目录属于其他工作，未加入提交、未删除。本次业务代码/契约/任务记录随同提交，未推送。
