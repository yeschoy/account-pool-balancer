# 官方文档核对（2026-11-05）

通过真实浏览器读取 https://tokenharbor.ai/docs 及下列页面：
- /docs/api/curl：Chat Completions / Messages，bearer 或 Messages x-api-key；同一目录模型均可用于两形状，正文 pass-through。
- /docs/integrations/codex：base_url=https://tokenharbor.ai/v1，wire_api=responses。
- /docs/integrations/claude-code：ANTHROPIC_BASE_URL=https://tokenharbor.ai；显式 x-th-confirm-spend:true 可关闭大额调用确认，本服务不自动添加。
- /docs/api/prompt-caching：自带 cache_control 不改写，Chat content block 同样支持；Claude cache creation/read 分开报告，缓存 5 分钟命中续期。上游连接可能变化，本地亲和不等于供应商命中保证。
- /docs/api/rate-limits：列出 /v1/responses；免费账号 60 RPM/1800 RPH（账号所有 keys 合并），IP 100 RPM/3000 RPH；付费不限 RPM/RPH，无并发上限；429 Retry-After；免费 4MB、付费 100MB；413 不应强行截断重试；key limits 为 USD 支出上限。
- /docs/api/models：文档入口重定向公开 /models 页面，目录使用标准 /v1/models；模型目录可见不保证余额或推理授权。

范围取舍：只实现上述三个文本协议及模型目录，不根据 rate-limits 的一行提及推断图像请求/响应完整契约。不新增私有余额 API，不使用注册会话替代 API key 查询余额。
