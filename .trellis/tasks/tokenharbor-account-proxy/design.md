# 设计

- 不新增代理字段/数据迁移/同步事务。注册代理仍是加密 Row.ProxyURL/ProxyBound；号池代理仍是 config.Account.ProxyURL。
- Store.SetProxy 集中校验，允许空闲状态，拒绝 queued/running；绑定变化后清空该行 Subscription，仅触碰该行代理和观察数据。现有 API 输出 Summary，不含认证。
- 号池快捷弹窗复用注册代理弹窗的输入组件与显式清除规则，提交到既有 PUT `/api/accounts/{id}`（proxyUrl 或 clearProxy），无额外接口。通用编辑增加独立语义说明，后端对 TokenHarbor 新代理复用严格注册代理校验。
- 新建入池原逻辑默认拷贝 row.Egress；完整 Key 去重原逻辑保留，不覆盖已存在号池代理。
- fetchTHObservation 去除注册/号池代理相等条件，保留 verified、唯一关联、UpdatedAt 与 ResponseBindingSource 保护。官网读取和目录请求仍经过号池同一代理；注册机订阅检测仍经 Row.Egress。IP 绑定导致会话不被官网接受时仍未知停调度，不做自动绕行。
- 补验证写回加入 UpdatedAt/base/有效代理来源检查，支付预留加入 UpdatedAt 检查，避免新增可编辑状态暴露读后换出口竞态。现有只读订阅检测已有来源保护。
- 不变：号池代理变更使来源哈希变化，原 Responses 续传及套餐/目录证明失效，重新检测才恢复；不自动开启 enabled，不自动注册/收费/支付，不失败直连。
- 回退：无存储结构变更，旧版可读；只需回退代码，但旧版对独立代理又会按原严格相等逻辑停调度。不得因此覆盖用户新代理。
