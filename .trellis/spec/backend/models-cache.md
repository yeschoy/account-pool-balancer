# 模型列表缓存合同

## 1. Scope / Trigger
修改 `GET /v1/models`、上游目录并集、本地权限过滤、额度变更通知或缓存失效时适用。不改变各池推理授权、原生模型 ID、别名、协议或可见性规则。

## 2. Signatures
- `Server.publishedModels(ctx, cfg, group) []byte`：最终下游 JSON。
- `Server.upstreamModels(ctx, cfg, group) modelsEntry`：原始上游/快照目录并集。
- `Server.resetModels()` / `Server.invalidateAccountModels(accountID)`。
- `quota.Poller.OnUpdate`：快照成功存入后通知；`Forget` 删除已有快照也必须通知，且在 Poller 锁外执行。

## 3. Contracts
- 先鉴权再查缓存。键包含 provider/group/原始或最终层；条目和 in-flight 绑定配置 revision，不跨号池、分组或配置版本复用。
- 最终缓存命中只做索引、互斥与时间检查，直接复用不可变 JSON，不遍历账号、不复制快照、不重新解析/序列化 JSON、不发网络请求。
- 复用已有 `routing.modelsCacheSec` 与各池有效 Routing（默认600秒）；网络原始层 TTL 自读取完成计，最终层不得在过滤时续期超过原始层。
- 本地派生目录不得延长来源有效期：TokenHarbor `ValidUntil`、OpenCode 目录/协议元数据24h、Perch目录24h；普通最终过滤受账号目录24h限制，Deploy还受独立 `Quota.StaleSec` 限制。轮询关闭也必须按时间自然过期。
- 额度更新只失效本组最终结果；TokenHarbor/OpenCode/Perch 的原始层也来自快照，须一起失效。OpenCode 显式回退目录同时依赖主组与回退组。不得把余额轮询变成整池 `/models` 网络重读，或清空无关池。
- 网络原始目录保留至 TTL/配置失效；纯上游新增模型不承诺在 TTL 前出现。首次填充和原始层到期仍遍历启用账号取去重并集，不为提速只读第一个账号。
- 同键同 revision 的并发 miss 共享一次构建，等待者可取消，不取消他人的共享读取。失效必须摘除旧 in-flight；新调用不能加入旧任务，旧结果也不能覆写/移除新任务的缓存。用 call 指针身份校验提交。
- 失败（nil）不缓存，不把失败伪装为成功空目录；原有允许的本地空目录语义保留。缓存不落盘，不新增后台刷新、数据库或配置开关。

## 4. Validation / Error Matrix
| 条件 | 结果 |
|---|---|
| 无效下游 Key，缓存已热 | 401，不返回缓存 |
| 同组并发 miss | 一次聚合，其余等待；取消等待者可退出 |
| 额度/快照更新或 Forget | 立即失效相关本地结果；保留无关组和网络原始层 |
| 凭据/代理/Base/启用状态/设置变化 | 旧 revision 不命中；旧 in-flight 不污染新结果 |
| 权限/目录/协议自然过期 | 不因缓存延长；重建沿用原有各池门禁 |
| 上游失败 | 原有502，不长期缓存 nil |

## 5. Good / Base / Bad Cases
Good：额度轮询后只重算本组过滤，已缓存上游目录继续复用；TokenHarbor证明到期后不再发布旧权限。
Base：冷请求仍返回所有启用账号目录并集，Modal短名、OpenCode显式回退等原行为保留。
Bad：把最终缓存时间从每次过滤重新起算、全局额度刷新清空全部网络目录、Forget不通知，或失效后继续等待旧凭据任务。

## 6. Required Tests
`internal/server/models_cache_test.go`：最终字节复用、两层过期、并发合并/取消、失败不缓存、失效与旧任务竞争、revision/禁用账号、分组/跨池失效、目录/Deploy期限、TH权限更新/过期、OpenCode回退依赖/协议期限。保留原有目录并集/别名/过滤及TH严格未知拒绝回归；Ollama源变更测试断言真实条目与in-flight被清空。
`internal/quota/poller_notify_test.go`：Forget通知已删除快照、锁外可读、不重复通知不存在快照。

## 7. ALTAPI 固定目录特例（用户要求）

以上按请求TTL/额度失效规则继续适用于其他池。ALTAPI不再走两层按需构建，使用 `internal/server/altapi_models.go`：

- 服务 `Run(ctx)` 启动后台读取一次，之后固定每小时扫描；按分组读取全部启用账号目录去重并集并过滤一次。不能为提速只读第一个账号，也不能跨组混合。
- `publishedModels` 在鉴权后只读组快照的不可变JSON；冷启动/来源变更尚未就绪返回原502，**请求不得补刷、遍历账号、过滤或序列化**。额度通知及普通 `resetModels` 不删除这个特例快照。
- 配置发布时生成组来源摘要（账号ID/启用/分组/推理Key/代理/Base与目录过滤开关），变更组以新对象替换并唤醒后台；不变组跨revision保留同一对象/字节，session、余额、并发等无关设置不触发目录重读。前台仅接收已协调的当前revision，发布/回调间隙及旧请求不得读到不同来源快照。
- 后台提交校验组对象身份，旧来源任务不能回填新组。小时读取失败（nil）不替换同来源上次成功目录；无历史则继续502。部分账号成功保留原有成功账号目录并集语义，不能因为单个失效Key阻断冷启动列表。固定目录允许过期期间保留旧成功值，但**不改变推理侧账号权限/余额/隔离的实时判断**。
- 生命周期使用服务ctx，关闭等待任务结束，HTTP及账号循环响应取消。只新增ALTAPI专用小时循环，不增加持久缓存、数据库或配置开关；ALTAPI设置页隐藏不生效的 `modelsCacheSec` 并说明固定每小时规则，其他池设置保持。
- `internal/server/altapi_models_test.go`：冷请求零网络、完整并集/去重、字节复用、鉴权/组隔离、额度与无关配置不失效、定时tick/来源变更wake、旧任务隔离、失败保留、服务取消、万账号命中基准。前端验证本池隐藏TTL且其他池原字段不变。
质量门禁：gofmt、全仓normal/race、vet、build。基准只证明本地缓存命中开销，不代表完整HTTP延迟、线上容量或部署验收。
