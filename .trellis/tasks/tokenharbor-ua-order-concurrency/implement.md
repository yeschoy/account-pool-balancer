# Execution

1. 已读取项目 backend/frontend/tokenharbor/opencode specs 及共享跨层/复用指南。项目仅携带 specs/tasks/workspace，无本地 workflow/scripts；用上级只读脚本确认 package，运行上下文提示未初始化，因此沿用仓库既有手工任务记录格式，不初始化或改动原工作树。
2. 扩充并测试身份池，补持久化与兼容性测试。
3. 完成阶段重构与两种顺序/中断恢复/不重复发信测试。
4. 并发后端/设置/前端与上下界、实际并行、跨任务全局上限测试。
5. 时间排序、详情一致性、截断前排序、前端轮询测试。
6. gofmt；Go 全量 normal/race/vet/build；Node syntax/behavior；本地实际浏览器假数据流程及后端状态验收。
7. 更新受影响 contracts、validation 与 session；提交代码和全部 Trellis 记录。不合并、不推送、不部署。临时产物登记 _tmp_artifacts.md，等待用户同意清理。
