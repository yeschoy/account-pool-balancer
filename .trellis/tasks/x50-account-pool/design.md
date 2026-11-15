# 设计

基线 c5e0c4a（父265fb04），已包含c9297fe注册机及Deploy完整管理参考链路（不代表Deploy余额门禁已全部完成）。无需移植其他分支；原工作区未触碰。公共Trellis只读参考，手工记录且不修改会话指针。

- X50独立配置块，保留x50分组，sk-50x优先识别。复用通用CRUD/import/client/import keys及原生Chat保护，独立规则不借用Deploy405语义。
- inference Base /v1，billing origin由同base推导，管理Token存Account.ControlKey（不复制密码/邮箱token），代理仅推理账号字段。单独凭据来源哈希覆盖session/base/key/proxy；锁内验证晚返回，metadata同源才用。
- billing GET /api/billing 用Authorization Token；balance_usd必需为有限十进制数（字符串或JSON数字），tier可选字符串，缺失/失败保历史不当零。目录独立时间与错误；quota开关控制周期，手动强制刷新。
- 导入在config.Store.Update原子校验写入、Key去重，候选视图根据当前真实池中key关联判定，无双写标志漂移；不双写target id/布尔标志，避免两文件提交失败产生漂移。删除目标即默认重新显示，重复导入不改用户代理等策略。
- proxy批量接口显式scope=all/selected，确认50X；服务端检查所有selected均属于X50，配置原子更新，空proxy严格直连，不改注册存储。
- x50注册日志复用dr-log-dock样式但唯一DOM id与状态，异步序号/切页门禁阻止晚响应；离页收起并停止轮询。
- 当前公开文档确认多Key共享账号余额/限流，新账号不再保证赠金；旧免费层daily allowance未接入，禁止钱包0推断免费耗尽，只对paid/prepaid且新鲜0作阻断。概览不合计多Key余额。请求/错误/详情采集独立开关，保留天数/容量共享只读。
