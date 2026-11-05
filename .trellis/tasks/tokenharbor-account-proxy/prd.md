# TokenHarbor 注册机与号池逐账号代理修改

## 目标与已确认决策
管理员可分别在注册机和号池修改每个账号代理。用户最终明确：**两边独立设置，入池时默认使用原注册记录代理**。不做双向同步，不修改真实账号，不部署。

## 背景
- 注册机已有 PUT `/api/tokenharbor/registrations/{id}/proxy`，但 Store/UI 只允许 saved/queued（`internal/thsignupstore/store.go:435`、`internal/server/web/tokenharbor.js:127`）。
- 号池通用编辑可修改/清除代理，已有配置持久化和来源失效机制；尚无清晰的独立快捷入口（`internal/server/web/app.js:674`、`internal/server/server.go:266`）。
- 入池创建账号已复制 `row.Egress(...)`，已有账号按完整 Key 去重且不覆盖其代理（`internal/server/tokenharbor_registration.go:476`）。
- 套餐后台原先要求号池代理等于注册代理，独立设置将被误停调度，需改为使用原注册会话但始终经过号池账号自己的代理（`internal/server/tokenharbor_entitlements.go:27`）。
- 本工作树缺少 Trellis scripts/workflow，按现存目录手工维护规划，不新增管理工具。

## 需求
1. 注册机 saved/registered/unverified/imported/failed/uncertain 空闲记录均可更换或明确清除代理；queued/running 拒绝修改，避免任务已领取旧出口。只写注册库，不修改号池。
2. 号池账号操作菜单提供“修改代理”，使用现有账号更新 API；通用编辑同样保留独立语义。留空不误清除，清除必须显式选择；只写号池配置。
3. 新建入池账号默认复制注册记录当前绑定代理（含明确直连），后续独立修改互不覆盖；重复入池不覆盖已有号池代理。
4. 号池套餐/额度和目录检测按号池代理执行，原记录只提供唯一 accountId + 完整 Key 关联会话。保留 source/UpdatedAt/过期门禁、认证 Cookie 安全保存、失败不直连；注册机自己的检测继续用注册代理。
5. 注册代理变化使该记录套餐观察失效，保留密码、会话、Key、已付链接和入池状态；号池代理变化沿用现有缓存/会话绑定失效和后台重新检测。并发检测或补验证不得写回过期出口结果；支付操作初始化后预留前的来源变化需拒绝。
6. 代理认证信息不回显，现有管理员认证/CSRF/no-store；输入支持既有 HTTP/HTTPS/SOCKS5/SOCKS5H，不引入依赖或新构建系统。

## 验收
- 每种允许/拒绝状态、无效代理、代理更换与直连、加密重开和敏感字段保留均有回归测试。
- 实测注册与号池各自修改不影响另一端及其他账号；入池复制代理，重复入池不覆盖独立设置。
- 本地不同代理 Mock 证明号池观测走号池出口，注册观测走注册出口；错误/迟到结果不放行旧证明，不发生直连回退。
- 前端行为/语法、Go 普通/race/vet/build 和隔离浏览器验收通过。

## 不在范围
批量代理迁移、自动轮换/代理检测、两边同步、实际注册/付款/推理、部署/生产配置修改。临时验证产物登记 `_tmp_artifacts.md`，经用户同意后再清理；Trellis 长期记录与功能代码一并提交 Git。
