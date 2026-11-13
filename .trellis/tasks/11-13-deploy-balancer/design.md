# 技术设计

## 边界与复用
新增 config.ProviderDeploy 与 Deploy 配置块、独立 ErrorRules/RetryRules，集成既有 Config.Normalize/ForProvider/RulesFor/RuntimeBase、组隔离、管理 API、选号器与原生 HTML/JS。不复制注册机，不引入前端构建系统。

## 数据流
账号/设置 API → Config.Store.Update(克隆/校验/原子保存) → ForProvider 选择独立路由/轮询 → 现有 Pool/Proxy → `deploy.baseUrl + /chat/completions`。部署默认 API root 含 `/api/v1`；非 Chat 请求在本地拒绝。请求体与响应 SSE 透明通过，复用已有鉴权脱敏/重试安全门。Deploy停号规则优先于错误动作的换号建议，但仍执行冷却；不沿用通用工具删除后重试。Chat输出后不重放，缺失终止的截断流记失败，不伪造DONE。

目录刷新 → HTTPFor(账号代理、禁止凭据重定向) → 强类型 Catalog 解析 → Snapshot(来源哈希、时间、失败状态) → metadata → Account API/概览/UI。目录不等于余额、不等于推理授权；手动刷新必须返回准确说明，不声称额度已刷新。来源变更先失效；存储锁内再次核对来源，晚返回丢弃。

余额能力先调查官方文档和只读接口。仅有推理 Key 时不向 Cookie 控制面伪造授权，不自动创建上游资源；如无可靠可读取金额，UI/API 返回能力状态和原因，不设无效余额刷新开关，记录未接入范围。

## 配置隔离
Deploy 拥有独立 Routing/Quota/ModelSyncSec/DetailLog/Pricing/规则。默认独立价格来自实测读取的官方文档，仅估算而非账单。新旧配置都通过默认 Deploy 参数初始化，避免未来全局路由变化隐式影响本池。钱包与 Design Plan 阈值不适用，不出现在 Deploy 表单。日志保留/容量仍为全局设施，Deploy 页只读且保存不修改；价格表和详细采集开关独立。并发/RPM 在账号上，轮询并发在 Deploy 上；请求连接池参数若共用必须明确标注/不伪装独立。

## 凭据与兼容
Deploy Key 无可靠前缀，必须显式声明 provider；不能被 sk-/其他池推断覆盖。复用管理员授权展示/复制与 no-store 边界，日志继续脱敏。测试 Key 仅通过本地表单写入忽略的测试 data/config.json，不写脚本/测试/任务文档。

## 风险/回退
新增配置字段兼容旧配置，不覆盖现有账号。仅提交独立分支；无线上变更。若余额接口/Responses/Messages 未得到证据，明确不支持或未接入，而非透传未知协议或虚构余额。
