# 设计

- 注册机新增内存 filter=pending|imported；renderTHJob 先过滤当前任务，再维护 selected/thState.rows，复用所有现有批量锁。页签数量来自未过滤的任务记录。默认 pending，不存 localStorage。
- POST `/api/accounts/{id}/return-to-registration` 复用管理员认证/CSRF/no-store。服务端必须找到唯一 AccountID 匹配的 imported 行，配置中的 provider/Key 必须仍对应同一注册资料。没有注册行的手工 Key 账号拒绝，不按邮箱或 Key 后缀合并。
- 新增 thAdmissionMu，仅串行本地入池/退回/删除注册行的提交，不覆盖网络套餐检测。入池检测后在提交阶段核对原 UpdatedAt，拒绝晚到结果。
- 两文件不宣称原子事务：持有行更新锁时先持久化删除号池配置，再持久化恢复注册行。若第二步失败，账号已停止调度、加密原行仍保留 imported/accountId，用户可在“已入池”里再次退回；不自动用旧配置覆盖线上变化。成功清除 accountId 并保留其他注册字段。既有 config OnChange 负责绑定/目录失效。
- 退回成功定位原任务并切换待处理；菜单和表格共用确认/API/helper。
- 只将 TokenHarbor 的“可用模型”改成“目录模型”，不更改其他 provider 或 catalog/路由语义。套餐模型调研单独记录官方证据和尚未验证边界。
