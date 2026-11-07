# 技术设计

## 数据流与唯一责任
上游 GET → quota.HTTPError 安全解析固定码/明确封禁信号 → Snapshot.openDesign 分项观察 → metadata 原子持久化 → quota.OpenDesignDiagnosis 统一投影 → 管理 API / 原生 JS 渲染。

- HTTPError 扩展固定封禁标志，Error() 不含任意 message；目录非200复用受限 JSON 错误解析。只认可 403 + forbidden_api_key + 已知 api key owner banned 文案为账号封禁，不把普通403/权限scope当封禁。
- OpenDesign 专用轮询路径复用现有 Client、目录解析、HTTPFor/每账号代理与客户端身份。目录与控制面独立；工作区确认后套餐/钱包/账单各自受限并行读取；工作区失败不使用未确认工作区继续读钱包/套餐，账单仍独立读取。现有其他 provider 分支不变。
- Snapshot 新可选 openDesign 保存 source、分项 state/status/code/checkedAt 与上次明确运行密钥拒绝证据。暂时故障不清掉既有封禁证据，新的目录成功才能清除；不自动恢复账号开关或改 pool 状态。缺失旧字段不虚构成功。
- source 为 provider/运行及控制凭据/两种基址/代理哈希；读取与写回核对当前来源，配置变化 Forget。元数据 clone 深拷贝。
- Diagnosis 是展示投影，不进入调度器；分开 planFresh/walletFresh/billingFresh、套餐窗口状态和钱包金额状态。仅最新分项读取成功且在有效期内才代表当前数据，过期窗口未知。保留已有后端 EffectiveRemaining 调度行为，不扩展本次权限。
- 刷新按钮沿用 POST /api/accounts/{id}/refresh-quota、admin/CSRF；OpenDesign 返回完成的诊断（200）以及 ok、quotaRefreshed、message/diagnosis/nextAt。其他 provider 保留现有502与响应合同。来源中途变化返回冲突，不给旧结果打成功标签。

## 前端
按钮改为“检测状态并刷新额度”；列表分别显示账号诊断与套餐/钱包状态，使用现有 esc/textContent。失败保留旧值但标明“上次/当前未知”和成功时间，下次重试可见。显示详细错误固定标签/码，不暴露原始上游 message、密钥或工作区标识。首次没有数据也可显示诊断。复用原 UI，无构建系统或依赖。

## 兼容与风险
新增 optional 元数据，无配置迁移；旧缓存不当作新诊断证据。来源变化取消旧观察，不影响原账号数据、开关和已有冷却/隔离。仅本地假凭据与回环 Mock 验证，生产不作变更。
