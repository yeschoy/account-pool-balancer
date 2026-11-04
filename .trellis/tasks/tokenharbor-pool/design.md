# 设计

- 沿用 Config.Store → Pool.Acquire/Lease → 原生 HTTP/SSE → request/error/detail log → stats/overview → 现有 vanilla 控制台，不新增调度器或数据库。
- TokenHarbor.RuntimeBase 取 tokenHarbor.baseUrl；新增可选 routing 覆盖，省略时继承通用路由但默认开启 sticky / reject busy，仍遵守管理员显式覆盖。ForProvider 返回浅拷贝配置，避免影响 OpenDesign。
- 现有 identity 扩展 Codex 会话/缓存头识别，仅 Token Harbor 使用缓存头优先级，并以 group+downstream-key 作用域 HMAC。parent 优先、session 次之、cache-key 再次，缺少稳定标识沿用消息 HMAC。cache_control 是内容缓存边界，不把 ephemeral 当会话 ID。
- Responses 沿用 confirmed response binding/source fingerprint 持久化机制，复用 quota source hash 函数能力需抽象为通用 credential/base/proxy hash，避免旧状态续传到新凭证。HTTP/SSE 已提交不重放。
- Token Harbor 仅 bearer 鉴权，Messages 补默认 anthropic-version；显式 x-th-confirm-spend 原样透传，不默认确认。保留请求字节、提示缓存字段和相关协议头。
- 模型目录独立轮询，无余额读；失败保留旧值，不把 unknown 显示为 0。Token Harbor 目录不参加 OpenDesign ModelClass 学习。
- 设置 API 只投影 baseUrl/modelSyncSec/routing，不输出 captchaKey 等注册凭据；保存以窄字段更新，保留其余注册配置。前端沿用现有标签/日志筛选/概览。
- 旧镜像与在线数据不操作；回滚为 Git 代码版本回退。新增字段均兼容旧配置。
