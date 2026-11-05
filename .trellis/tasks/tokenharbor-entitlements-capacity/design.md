# 设计（用户已确认严格模式）

## 数据源与边界
- 精确关联号池accountId+完整Key到加密原注册行；不存在/歧义/出口变化则未知。后台用该行会话及固定代理GET官网pricing获取已验证当前套餐和公开models映射。
- 已实测GET `/api/me/free-tier`（名字虽有free，同时包含付费Pass周表）：ok/on_plan/activated/plan_used_pct/plan_exhausted/overage_billing/period_ends_at/reset_at/plan.window_days。真实样本Agent月度有效、周上限2.5原始美元、已用0%、独立重置时间。只读，不POST隐私/付款接口。
- quota.Poller新增TokenHarborFetch回调，server负责会话、quota只保存不含凭据的来源绑定快照。独立accountSyncSec默认300秒；0关闭周期账号同步，手动刷新可用。模型目录按modelSyncSec独立缓存；绝不在推理/下游models热路径访问官网。
- Native目录完整ID与pricing已确认模型组做精确交集，按套餐层级放行，未知/旧别名/未确认变体拒绝。免费模型还须原账号有显式启用证据；本流程不启用免费模型。
- Pool硬门禁独立于ignoreCatalog/walletFallback，来源/套餐/用量过期未知拒绝，周额耗尽或官网钱包续费开启时拒绝，不借钱包、不代改账单设置。Responses绑定不能越过门禁，续传仍按原来源与下游Key隔离。

## 展示与计算
- 账号展示套餐、测量时间、套餐覆盖/目录数、周已用百分比/重置及未知原因；这些均不是钱包余额。
- 概览Agent按用户指定2×等效展示口径20/周期、5/周。总周期名义值统计已确认有效的唯一Agent账号；当前可调度周额仅对已启用且门禁有效账号按5*(1-used_pct/100)求和。非Agent与未知单独计数，不套用20/5。每账号独立重置；过期读数不补成满额。

## 日志、会话、容量
- 保持已有Codex/Claude/OpenCode/通用身份优先级。为pi真实兼容头/无标识情况增加夹具和使用说明；不把普通X-Client-Request-Id当固定会话，不承诺上游缓存必命中。
- TokenHarbor错误自动生成限长脱敏details（requestId+attemptIndex关联，后续成功也保留失败尝试），成功默认不存正文；包含流中错误。会话头/metadata/缓存标识不可泄露；非JSON/SSE正文不能因解析失败输出原身份。保留原始推理字节。
- 模型列表改为不可变缓存读取；目录/会话后台网络不持调度锁。修复响应捕获tail可能因大Write临时扩张的问题；原生输入共享2GiB预算，满额在上游前429，上传60秒deadline随后清除，已知长度单次分配。压测包含大请求/SSE与粘性，报告真实字节、延迟、RSS和错误，不能用虚拟usage冒充3亿真实token。
- 容器采用可覆盖的内存硬限制+GOMEMLIMIT留余量，并有明确容量说明（8GiB硬/6GiB Go/2GiB输入初始参数，并非目标保证）；本轮不应用生产、不推送部署。

## 一致性与兼容
- 元数据来源绑定Key/base/代理，晚到刷新必须在quota互斥锁内再校验当前配置，删除/变化不能复活缓存；新配置/注册行变化使旧观察失效。
- 新快照额外字段不含Cookie/密码/APIKey；旧TokenHarbor目录没有有效套餐观察时不再提供权限。后台同步失效显式显示，不自动登录、换代理或重新注册。
