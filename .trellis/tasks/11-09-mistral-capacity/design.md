# 技术设计（用户已授权实现，生产验收另行确认）

研究与证据：`research.md`、`docs/mistral-capacity.md`。

## 决策边界

- 先优化同一个Pool，不另建Mistral调度器；全局租约/MarkSent/释放语义不变。
- 3亿TPM是业务目标，不作为配置一个数字即可生效的功能；必须绑定工作负载、上游授权容量和验收窗口。
- 用户选择A：Mistral全池会话容量独立配置，不增加每账号上限；并明确要求按审计落实本仓瓶颈改造。新旧配置兼容和具体范围如下。
- owner-only实验是Go overlay研究产物，不能直接当生产实现；它仍有线性ID查询。

## A. 不可变读索引与热命中

Config归一化/发布时构建账号ID、组内账号、下游Key ID/hash、来源hash和有效provider routing索引；只读随快照发布，不持久化派生索引、不在热路径写map。保持原分组优先与启用Key校验，不改变credentials归属。

Mistral sticky hit：查绑定→校验owner及source/group/client enable→只查owner目录/健康→原子准入→续TTL/移LRU→租约。owner失效、忙时overflow/wait-overflow等保留原路径语义；严格续传不允许静默换号。诊断字段含义须明确，不能伪造“扫描全部候选”。健康滚动总和需与1440槽和过期窗口保持等价。

冷选号先用轻量候选索引，避免反复复制Account大结构；仅对策略所需字段计算Seat，保持优先级/权重/额度/RPM等原有结果。预计算来源hash随配置失效，晚返回快照不能复活旧凭据。

## B. 有界资源

复用TokenHarbor已知长度一次分配、chunked保守预留、读取deadline与原子预算模式，泛化输入预算但保留按池隔离/全局硬预算，避免新Mistral耗尽其他池。适配后新正文与响应/Event队列预算不能漏算；额度用字节而不只用请求条数。

进入读体/等待/上游发送前分别有界准入；满额明确429、Retry-After及分类，零上游发送。等待上限与等待人数上限独立。慢读写每次设置可续期write deadline，超时释放上游/租约/正文；不采用杀死全部正常长流的短全局WriteTimeout。

## C. 可配置会话容量

`Mistral.BindingCapacity *int` / JSON `mistral.bindingCapacity`：nil/0保留共享4096；1–1,000,000为Mistral独立普通绑定LRU容量。接口省略字段保留已配置值；显式0回到旧共享预算。UI说明这不是每号会话或在途并发。

- 独立模式在同一个Pool中维护单独Mistral LRU索引/计数，所有绑定仍统一key查找与原子租约；其他池与严格owner留在共享4096预算，彼此不淘汰。启动先装载容量策略，再恢复严格与Mistral绑定。
- 配置发布/reload在Pool锁内迁移预算、裁剪过量LRU；所有删除统一更新dirty和计数，禁用账号/来源变化不复活。旧快照的在途请求不能把全局策略退回旧版本。高容量按需分配，绝不预分配100万条。
- 显式0只有“旧共享预算”含义，不代表关闭粘性或无限容量。容量建议依据新会话率×TTL并留余量。
- 内存维护绑定owner/池索引和每账号计数（若启用）；创建预约、确认、未发取消、替换、过期、LRU、禁用/删除/来源变化和恢复统一走计数/dirty路径。
- 动态缩容裁剪未来路由偏好，不中断在途。严格续传缺失拒绝，不能变普通偏好。
- metadata仍存opaque hash与绝对expiry，不存原session/提示词/凭据；导入先恢复严格owner，再恢复符合新预算的普通偏好。

## D. JSON、统计、日志与连接

- 严格JSON校验一次，复用根字段offset，懒解码fallback历史；完整内容兼容校验仍保留。多行SSE/转义字段保守回退，普通单data字段尽量零拷贝，仍逐帧Flush。
- Stats按需分钟桶和过期回收、消除相同账号all/provider重复，保持24h总量/缓存率reset/旧metadata兼容。锁内快照后锁外安全序列化，不在锁外读可变binding指针。
- 脱敏缓存基于稳定配置版本（而不是每请求provider view指针），有界失效；全文详情提供明确可选采样，不未经授权关闭现有日志。
- 连接参数需明确共享或独立。若独立，Transport key=(固定代理,有效连接策略/版本)，更新只关旧idle、不杀在途。不能把HTTP2连接数当stream并发。

## E. Mistral性能配置与并行边界

新增可选`mistral.performance`，省略沿用安全默认，旧客户端省略不得清空已保存值。`EffectivePerformance()`返回：`maxBufferedMB=2048`（再按Go软内存上限收紧）、`maxWaitingRequests=1024`（0表示不等待）、`uploadTimeoutSec=60`、`writeTimeoutSec=30`、`detailSamplePercent=100`（0表示不采正文详情、摘要不变）。合法范围分别1–65536、0–100000、1–3600、1–600、0–100。配置结构存在时按完整文档校验，不把无效0悄悄补成默认。前端/API独立保存与重载，说明请求体预算不等于整个进程硬内存上限。

池接口约定：`Request.MaxWaiters int`为按组最多等待数，0对旧调用方表示不额外限制；Mistral若配置maxWaitingRequests=0则由Handler将Wait设0，否则传MaxWaiters。按组释放唤醒；管理恢复可广播所有组。`Pool.ConfigureBindings(cfg)`在启动导入前和配置发布回调调用，策略只由当前Config快照更新，不被旧请求回滚。`Pool.BindingCapacityFor(provider)`返回有效容量与独立标记供概览使用。

实现按多seam划分，详见lane-board.md；主线程拥有配置索引/Pool/会话容量/管理UI与集成，隔离worker分别拥有请求资源/JSON/连接/脱敏，以及Stats/Logstore。每个工作树只有一个writer，不触碰既有其他未提交文件。

## F. 兼容与发布

新的可选Config/metadata字段可能被旧版严格JSON读入器拒绝；上线前一致备份，明确降级只去除新增派生字段的办法，不用旧data覆盖新写入。运行中配置变更保持Revision/快照和源校验。

本轮不部署、不改线上参数；后续按`docs/ovh-deployment.md`执行一次预检→构建→备份切换→一次验收，保留双内网别名。上游提额、付款/PAYG及真实推理压测另行明确授权与预算。

## 方案取舍

优先可验证的单实例owner-only、索引和预算：本地实验已证明大收益。先不上Redis/共享数据库，避免在典型50RPS长输入工作负载下先引入网络状态热路径。确需多实例时先决定账号分片/会话一致路由/共享准入和文件单写所有权，不能共享data目录直接多副本。
