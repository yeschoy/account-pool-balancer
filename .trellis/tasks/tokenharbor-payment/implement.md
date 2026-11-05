# 实现与验证计划

1. 创建 Orca 工作区，读取 backend/frontend spec（本仓库无 Trellis scripts/workflow/shared guides，不搭建额外系统）。
2. 修复两段邮箱输入；Flow 分阶段保存，Retry/Resume 保留会话和 Key，恢复沿用原注册时间；完善 Cookie 和 Checkout 加密持久化。
3. 限定 Agent Pass 单次托管支付链接，管理员显式 endpoint + 弹窗；保护旧订阅、地区确认、重定向、未知结果与重复提交。
4. 单元测试覆盖报价守卫、两步请求、凭据投影、断点、重启、两段输入、已有链接复用及未知结果禁止重放。
5. 全仓 go test / race / vet / build、JS syntax + 本地浏览器 mock 验收。
6. 真实仅一个账号：注册、保存 Key、验证邮件、官网正常 Choose Agent 获取支付页；浏览器确认单次月度 $0.99，不付款。程序自动创建两步分支以 mock 验证，不为验证它重复生成真实订单。
7. 通过现有 OVH 管理 API 导入该账号（待支付 disabled），备份与凭据归档，核对旧账号/设置及健康；不部署应用。
8. 记录证据并提交业务修改和 Trellis；临时产物待用户授权清理，业务会话/Key/支付链接与 Trellis 保留。
