# Bug Analysis: 注册成功被误当作可入池资格

## 1. Root Cause Category
- **B / D / E：跨层契约 + 测试缺口 + 隐含假设**。
- 页面默认勾autoImport，registrar直接调用thImport；thImport只检查注册状态/Key，后来补邮箱验证但仍没有付费套餐门槛。单测把已注册直接入池当作正确行为。注册、邮箱、支付链接与有效订阅被混为一个成功状态。

## 2. Why Fixes Failed
- 不是仅取消前端勾选：旧客户端仍能提交autoImport；手动入口仍能导入未付款账号。只改文案或检查有checkout链接无法修复。
- 保存代理绑定又揭示空URL歧义：显式直连与继承默认代理不应共用同一个隐含语义。

## 3. Prevention Mechanisms
| Priority | Mechanism | Action | Status |
|---|---|---|---|
| P0 | 结构 | 删除registrar自动入池调用，后端拒绝旧autoImport=true | DONE |
| P0 | 运行时 | 显式import必须邮箱验证+实时有效付费套餐；失败关闭 | DONE |
| P0 | 测试 | Free/未知/会话失效/checkout/缓存active/late-source拒绝，付费注册也不自动导入 | DONE |
| P1 | 存储 | saved状态与proxyBound区分仅保存/已注册、直连/继承，加密持久化 | DONE |
| P1 | 文档 | Token Harbor可执行契约和回退限制 | DONE |

## 4. Systematic Expansion
- 推理模型目录和余额/paid徽章都不能当订阅证明；本次未把注册会话逻辑塞入推理轮询。
- 普通手动Key导入和旧池账号仍保持管理员原有控制权，不做未经授权的迁移/批量禁用。
- 用独立只读套餐观察描述资格，不把支付链接创建当作付款回执；套餐schema漂移时明确unknown。

## 5. Knowledge Capture
- [x] `.trellis/spec/backend/tokenharbor.md` 补7段可执行契约与测试矩阵。
- [x] `docs/tokenharbor.md` 说明保存/代理复用/检测/入池及回退边界。
- [x] 记录真实只读和Mock验证范围，未宣称真实付款或生产部署。
