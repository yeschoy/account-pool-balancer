# OpenDesign 只读诊断与额度展示合同

## 范围与证据
- 仅 `opendesign` 使用新增分项诊断；不替换调度器，不自动修改账号 enabled、冷却、隔离或健康分。`Window.EffectiveRemaining` 的既有调度语义不在此展示改动范围。
- 工作区 `active`、`/v1/models` 成功均不是推理授权证明，不能证明未封禁。只有 403 + `forbidden_api_key` + 精确已知 `api key owner banned` 文案（含已观察到的固定包装）才归类明确封禁；普通403仅权限不足。
- 500 权益检查失败、503 `database_unavailable` / `billing_preflight_unavailable` 是上游故障，不代表封禁、欠费或零额度。402仅标识计费拒绝，不虚构当前余额。
- HTTP错误只保存状态、固定允许列表码与封禁布尔信号，任意 message/body、URL、密钥不得入 Snapshot/API。未认识的码丢弃，未知仍未知。

## 数据流与接口
- `Snapshot.openDesign`：来源哈希、catalog/workspace/plan/wallet/billing 分项 `{state,status?,code?,checkedAt}`，可选 runtimeRestriction。
- 来源涵盖 provider、运行/控制密钥、两个基址及固定代理；配置变化 Forget、读取时核对与写回锁内复核。迟到结果不能复活旧密钥的余额或封禁证据。clone 深拷贝整个诊断及拒绝证据。
- 运行目录与控制面独立，确认工作区后套餐、钱包、账单并行、单项最多10秒且受父上下文约束；工作区失败不复用未确认的旧工作区读套餐/钱包，仍独立读取账号账单。全部只GET，经原账号 HTTPFor/代理，不跟随凭据请求重定向，不发推理或付款。
- 失败保留各自最后成功值与时间；HTTP200缺失 eligible/codingPlan、账单金额或无效钱包金额也不能冒充测量。钱包的有效 preflight 回退保留明确来源。
- 暂时故障不抹掉上次明确运行密钥拒绝证据；仅新目录成功或来源变化清除该展示证据，不自动恢复账号开关。
- `quota.OpenDesignDiagnosis` 统一生成管理端展示：status/label/detail、checkedAt、planFresh/walletFresh/billingFresh/catalogFresh、planStatus/walletStatus。过期窗口不算 fresh，不将 reset 时间推算成实测满额；钱包0与套餐耗尽各自独立。
- `POST /api/accounts/{id}/refresh-quota` 保留管理员会话/CSRF。OpenDesign完成诊断返回200 `{ok,quotaRefreshed,message,diagnosis,checks,nextAt}`；200只表示完成检测，不代表上游成功。ok需要全部成功且套餐读数有效，quotaRefreshed需要本次套餐读取成功且有效。无控制密钥仍能独立检测目录，但额度未知。
- 等待中超时、来源变化或没有本次结果返回409，不能拿以前的成功读数冒充本次完成。其他 provider 保留既有 API 状态语义。
- `GET /api/accounts` 增加可选 diagnosis；OpenDesign quotaFresh 为展示有效性，调度仍读原 Snapshot 合同。后台轮询关闭时不声称存在自动重试。

## 验证
用本地假凭据 Mock 覆盖：明确封禁+active工作区、普通403、401、402、500/503、单项超时/部分成功、实测套餐0与钱包0、无控制密钥、过期窗口、缺失/无效200正文、封禁后暂时失败及恢复、来源变化/迟到结果、clone/metadata往返、等待超时不能重用旧成功、认证/CSRF、不发推理/不跟随重定向/不改配置和隔离。
前端必须验证旧值明确标注、错误码说明、套餐与钱包分开、窗口到期不展示满额、失败不能 toast 刷新成功。真实浏览器仅连接本地 Mock，并核实 API/磁盘/调用计数后置条件。
