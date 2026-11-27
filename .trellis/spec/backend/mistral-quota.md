# Mistral 套餐刷新与路由契约

## 数据链路
`whoami → billing/budget → MistralState → Snapshot/metadata → admin account view → mistralQuotaHTML`，手动与后台轮询必须使用同一个读取器。仅测通接口或显示成功toast不是完成。

- `GET /api/vibe/whoami` 判别 `plan_type=API|CHAT`，只用于标注Key类型（Studio/Vibe）与访问等级；**两种Key的号池可用余额一律取 `api_budget`**。Mistral规定第三方工具或网关调用 `/v1/chat/completions` 的用量只计入工作区 `api_budget`（<https://docs.mistral.ai/admin/billing-usage/subscriptions#included-monthly-usage>）。旧契约「CHAT只取vibe_budget」已作废：实测Vibe Key的 `vibe_budget` 300 USD用0%，`api_budget` 30 USD用100%，推理全部402/2303。
- `MistralState.Budget` 即 api_budget（含重置时间），是唯一参与 smart/least-used/pace 余量排序、概览「总额度剩余」与账户视图的余额；`MistralState.VibeBudget` 只作展示，**一律标注「仅限 Mistral 官方产品，号池调用不扣此项」**，绝不参与排序或任何合计。缺 `api_budget` 即读取失败（不得回退到vibe_budget）；缺/非法 `vibe_budget` 只是该项未知，不影响api预算成功。不能靠账号名猜类型，不能把两项预算相加。
- 官方API Base的whoami固定console.mistral.ai；自定义Base只访问管理员配置来源同一base下的路径，绝不从whoami的domains/tenant字段选择凭据目标。保持固定代理与NoRedirect，网络失败不直连回退。
- total/usedPercent必须存在、有限且非负；currency必须为上游三字母货币；重置时间必须有效且未过期。真正测得0与缺字段不同；超过100%允许显示，remaining=max(0,1-used/100)*total。金额是估算，不是钱包余额。
- 状态仅保留Key类型、已知套餐名、api预算与只读vibe预算、source、成功/检测时间和安全错误，不保存whoami身份/原始响应。
- source沿用ResponseBindingSource（provider/key/base/proxy）。来源变化清除快照，store丢弃晚返回；account API再次校验来源，旧源不能被展示为当前。Snapshot深拷贝包含预算和PAYG指针，metadata持久化保留成功值及错误状态。
- 目录和预算分别记录结果；预算403/无权限不是推理Key失效。失败保留旧金额与成功时间，标为未知/历史；过期不推算补满。手动接口返回ok/quotaRefreshed/message，前端必须尊重，不凭HTTP200弹“已刷新”。

## 配置与调度
- `Mistral.Routing`与错误/重试规则独立；`Mistral.Quota`为nil时继承共享Quota，保存后只更新Mistral轮询配置；模型目录周期ModelSyncSec默认600，0关闭周期但首次/手动仍读。
- 原Poller统一管理并发；Mistral额度开关/间隔/并发/过期均有效，手动可绕过周期停用；设置变化重新排期，不另建service或第二套worker。
- `DetailLog=nil`保留旧共享开关，显式bool仅控制Mistral；日志保留/容量仍共享，必须标注。不改变其他池的详情开关或关闭时删除历史。
- 只有fresh且source匹配的api预算参与smart/least-used/pace的余量排序，lowShare/spentShare仍是排序阈值，不改成新的硬拒绝门。vibe预算不参与排序：同一Key加不加vibe读数必须得到完全相同的Seat。未知不当成0，也不套用OpenDesign Windows、钱包或FundingFor。官网PAYG是只读状态，不自动启用/关闭或宣称已测得钱包余额。

## 402/2303 冷却到 api_budget 重置
- 上游 `402` + 顶层 `code`/`errorCode` 精确 `2303`（`billing_api_budget_exhausted`，正文不带上游给的重置时间）命中出厂规则 `mistral-api-budget-exhausted`：账号级 cooldown 到该工作区快照的 `api_budget.reset_at`，到期自动恢复；**不 hard-quarantine、不扣全局健康、不累加strike**，下游503。命中时立即刷新该账号额度，让界面显示新的百分比。
- 没读到重置时间（从未成功读取/读取失败/已过期）时按规则的兜底值：`max(24h, fallback)`，UI 必须标注该结束时间是估计值（按快照reset与cooldownUntil是否吻合判断并写明）。`Reset.budgetReset` 不得与 `header`/`bodyAfter` 同时使用（校验拒绝）。
- 其他 402 保持原 `mistral-insufficient-credits`（10m起逐次翻倍、最长6h）。只有 `mistral-api-budget-exhausted` 是 Mistral 专属：其他池的规则集中不得出现该ID或 `budgetReset`。
- 规则升级只替换**逐字等于历史出厂列表**的列表（`shippedMistralErrorLists`/`isShippedMistralRetryList` 内联枚举，不再从当前默认值切片推导；从默认值切片会把当前默认自动视为旧版，导致一次列表重排后无法识别上一个出厂版）。管理员自定义的规则、顺序、备注、停用、空列表一律保留。
- UI提供完整已有路由参数、独立轮询/目录/详情、错误和重试编辑器；精确bodyCodes编辑和保存不能丢失；不适用的OpenDesign钱包开关不放出，说明原因。

## 验证
Mock覆盖API/CHAT（**CHAT取api预算、vibe只读展示**）、0与缺字段、过期、币种、403不失效、重定向、持久化/克隆（含apiResetsAt）、来源变化晚返回、轮询开关/并发/目录0、手动刷新与account projection、设置与规则重载/隔离、日志开关、排序（vibe不改变Seat、CHAT耗尽的api预算排在后面）。402/2303覆盖：规则命中与其余402不回归、冷却到reset_at/无reset兜底/到期自动恢复、不隔离不扣健康、跨池隔离、出厂规则升级与自定义保留。浏览器真实操作假数据的刷新/规则保存并验证后端状态和重启读回。真实接口证据不等于生产UI已部署；上线另做版本/数据/资源/渠道路径验收。

## 升级边界
新增config可选字段保持nil时的旧行为；新metadata预算字段及已保存的Mistral独立配置可能不被旧版严格JSON解码器识别。部署须保留一致完整备份并验证存储兼容；上线产生新写入后不能盲目切旧程序或恢复旧data。本契约新增 `mistral.vibeBudget` 可选字段与 `reset.budgetReset` 可选字段，旧版严格解码器不认识；新版会把历史快照的 `budget` 视为api预算（旧版对CHAT写入的是vibe金额），因此回退旧版后CHAT号显示的仍是vibe额度。

官方契约证据：mistralai/mistral-vibe 的 `vibe/app_server/_billing.py`、`vibe/setup/auth/whoami.py`；真实Key只读采样见docs/mistral.md，不在规格中保存凭据。
