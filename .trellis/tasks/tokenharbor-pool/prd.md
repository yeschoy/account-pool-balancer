# Token Harbor 号池管理

## 目标
让 Codex / Claude Code 等客户端通过独立 Token Harbor key 池调用官方支持的原生接口，尽量复用同一会话的 key，提高缓存复用机会，并在现有管理台查看账号、概览、请求和错误日志。

## 已确认
- 用户要求直接完善实现，其他调度策略参考现有 OpenDesign 池，不强制跨协议兼容。
- 当前基线 b586406 只有 TokenHarbor 配置/注册库/规则，未接通 runtime base、provider 推断、控制台和轮询。
- 官方文档 2026-11-05：Chat / Messages；Codex 文档 wire_api=responses；rate-limits 明列 Responses。原生正文、cache_control、会话及缓存头保留；不自行注入确认消费头。
- Claude 缓存有效期 5 分钟且上游内部路由可变化；本地 key 粘性不能承诺上游命中。
- 免费请求限额按账号/IP而非 key，付费无 RPM 限制；不得把未知套餐硬编码成免费限额。无文档支持的 API key 余额接口，不伪造余额。

## 要求 / 验收
1. 独立 provider/group/client/import keys，默认 runtime https://tokenharbor.ai/v1；旧配置安全归一化，配置与规则校验、保存生效，绝不误发 OpenDesign。
2. 原生 POST /v1/chat/completions、/v1/messages、/v1/responses 与 GET /v1/models；不支持的路径本地拒绝，不做工具剥离或协议转换。
3. 复用调度器：会话/缓存标识 HMAC，key 范围隔离，显式粘性不因忙碌溢出；故障可换号但 server-side previous_response_id 必须命中已知绑定，否则 409。
4. 原样透传缓存标记与客户端协议头、显式消费确认头；不记录原始会话标识到普通请求/错误日志。无凭证重定向。
5. 独立错误规则支持失效 key、余额不足、429 Retry-After、短暂 5xx；请求错误不换号/冷却。
6. 复用模型目录刷新、概览、请求/错误/详细日志与缓存 read/write 统计；Token Harbor 元数据不污染 OpenDesign 计费分类。
7. 管理台提供 Token Harbor 标签，账号增改导入/启停/恢复、下游/导入密钥、专属 upstream 设置与规则；额度明确未知。
8. mock 测试覆盖协议原样、鉴权/头、粘性/忙碌/续传、失败换号、流式、统计日志和管理 API；Go 测试/race/vet/build、JS 检查与本地浏览器烟测。

## 边界
不部署、不调用真实付费推理、不自动注册/购买、不强行支持 WebSocket、Responses 子路由、图像接口或未证实的额度 API。现有注册库不在本轮接入；不回退到其他供应商池。
