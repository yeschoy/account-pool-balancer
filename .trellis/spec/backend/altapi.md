# ALTAPI 会话粘性与设置隔离

## 1. Scope / Trigger

修改 ALTAPI 的路由、身份识别或管理设置时必须遵守。凭据/额度/注册契约见 `docs/altapi-pool.md`；本节不证明服务端 Responses 续接或真实上游缓存命中。

## 2. Signatures

- `Config.ForProvider(config.ProviderALTAPI) *Config`
- `Server.sessionFirstIdentity(cfg, scope, headers, body, hints, claudeSession) Identity`
- `GET|PUT /api/settings?provider=altapi`
- `Pool.Acquire(ctx, cfg.ForProvider("altapi"), Request{Group, Affinity, Explicit, Wait, ...})`

## 3. Contracts

- `ForProvider` 独立选择 `ALTAPI.EffectiveRouting/EffectiveQuota/EffectivePricing`；不能返回全局路由代替本池配置。
- `altapi.routing` 必须进入 `Config.Normalize` 的 provider routing 校验列表；递归校验的副本必须清空包括 ALTAPI 在内的所有嵌套路由，避免递归。
- 管理设置写入只替换本池区块及规则；完整 settings 文档携带的全局路由/额度/日志/价格、Upstream、OpenCode 等不能顺带改写。校验成功并落盘后才发布。
- 身份作用域为 `group + "\x00" + clientKey.ID`；HMAC 原文/指纹不进入普通请求和错误日志。不同 Key 独立绑定不等于独占上游账号。
- 保留 `previous_response_id` 的原有优先级；普通会话先按已有规则读取自定义头、父会话、正文 session/metadata、Claude user session、内建会话头，之后才以正文缓存键 > 缓存头 > 消息 HMAC 兜底。
- ALTAPI 缓存头为 `X-Prompt-Cache-Key` / `Prompt-Cache-Key`。无 session 的缓存键只是尽力兜底；禁止将上游缓存命中和账号粘性命中混为一谈。
- 复用共享 scheduler，不新建第二套绑定表。普通绑定内存化、有 TTL/LRU；不自动迁移或修复旧版本已误写的全局配置。

## 4. Validation & Error Matrix

| 条件 | 结果 |
|---|---|
| ALTAPI busy 策略、TTL、会话头非法 | 保存 400，当前指针/磁盘配置不变 |
| 同 Key/session，缓存键变化 | 保持真实会话 owner |
| 不同 Key，相同 group/session | 绑定独立，不能复用对方绑定 |
| owner 忙，reject | 按预算等待或 429，不临时跳号 |
| owner 忙，overflow / wait-overflow | 按既有等待语义临时分流，不覆盖原 owner |
| TTL 到期 / LRU 淘汰 / 重启 | 普通绑定重建，不能宣称永久粘性 |
| owner 不可用 | 普通绑定可重选，重试仍受规则/已输出边界约束 |

## 5. Good / Base / Bad Cases

- Good：同一真实 session 的缓存键更换后仍命中原 owner，父子会话按父 session 聚合。
- Base：没有真实 session 时缓存键或消息 HMAC 尽力兜底；`sticky=false` 不建立普通绑定。
- Bad：只存 `altapi.routing` 而请求仍读全局，或依赖保存时污染全局“让设置看似生效”。

## 6. Tests Required

- `internal/config/altapi_test.go`：默认/覆盖独立，非法路由拒绝，缺省字段归一化。
- `internal/server/altapi_sticky_test.go`：HTTP → 真实 Pool → 本地 Mock 链路；会话/缓存优先级、Key 隔离、父子、TTL、busy 三模式、失败换号后稳定绑定、并发首请求唯一 owner。
- 设置 GET/PUT/磁盘重载、非法保存回滚和完整其他配置 DeepEqual；保存 sticky=false/custom header 后实际请求生效。
- 保留其他池身份优先级；共享提取改动须跑 Mistral 等既有回归及全量 normal/race/vet/build。不得将本地 Mock 当成生产/真实上游证明。

## 7. Wrong vs Correct

Wrong：`cfg.Routing` 当 ALTAPI 配置；缓存键优先于 session；只用 group 做指纹作用域；临时 overflow 把原绑定改成溢出账号。

Correct：独立 provider 视图 + 校验/落盘发布；真实会话优先 + 下游 Key 作用域；在既有原子调度器内维护滑动 TTL、临时 overflow 和恢复重绑。
