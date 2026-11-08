# 设计

- 扩展 quota/mistral.go：强类型 MistralState（source、keyType、plan、budget、at、checkedAt、error），预算只保留所选类型。浮点指针识别缺字段，拒绝无效数值/货币/重置时间，剩余额按百分比推算；不假设 USD/EUR。
- 使用原 Client.get 与 opencode.NoRedirect、固定账号 HTTP transport。官方 api.mistral.ai Base 的 whoami 固定 console.mistral.ai；自定义 Base 的 whoami 只在配置来源的同一 origin 下读取，绝不采用上游返回的 tenant URL。
- 复用 catalog-only 的目录读取及 source guard，Mistral 包装刷新随后读取套餐；目录错误保留且不阻断可读取的套餐。套餐失败保留上次值和时间并附错误；预算权限错误不改变 AuthFailed。沿用原刷新节奏/失败退避，不新增持久化文件。
- Snapshot 深拷贝预算；accountViews 校验来源并给出 quotaFresh；现有 /refresh-quota 返回套餐结果与安全 message，UI 尊重 ok 并刷新列表。不将币种预算塞进 OpenDesign Windows，不改变资金来源/硬准入策略；根据用户追加要求，fresh预算参与现有余量排序，让lowShare/spentShare真实有效。
- 审计已有独立路由/错误/重试能力，复用原面板/API并加强往返/规则隔离测试；新增Mistral.Quota可选覆盖（nil继承共享）、ModelSyncSec（默认600、0停周期）和DetailLog可选覆盖（nil保留旧共享开关）。原Poller执行本池开关/频率/并发，不创建新worker系统；已有错误匹配bodyCodes补齐可视编辑。
- 新池交付门禁写入AGENTS.md与backend/provider-onboarding.md，后续任务必须覆盖完整能力矩阵；不适用钱包/共用日志容量的边界在UI明确标注。
- 前端只格式化后端已验证预算，历史数据不得当当前余额；按类型标记 Studio/Vibe，并说明同组织同类预算共享。未知不显示0。
