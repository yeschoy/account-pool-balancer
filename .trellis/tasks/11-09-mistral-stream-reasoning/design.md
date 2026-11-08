# 设计

## Responses结束边界
仅对Mistral原生`/v1/responses`启用完整终止判断，不启用严格Responses续传绑定，也不改变其他池。复用现有SSE事件解析/错误判断；首事件与后续事件使用同一终止语义。支持event名称和data.type的completed/incomplete/failed；[DONE]不能替代原生Responses终止事件。错误事件保留原始协议字节并立即结束，不再继续等EOF。已提交后EOF或读错误只输出明确error事件（不得伪造成功完成），更新现有request/attempt/detail分类；不自动重试。合法incomplete是已正常收到的协议终态，不因没有文本而误判为网络失败。

## 三模型思考控制
使用一个精确模型集合：deepseek-v4-1-flash-1017、zai-glm-5-3、zai-glm-5-3-flash。保留原model字符串，不替换上游ID或新建别名。公共思考档位校验/归一只服务这三者；low/high/max原值保留，其他已支持的默认归一high策略延续。Chat将root reasoning.effort转换为reasoning_effort；校验/移除兼容thinking/enable_thinking且不让开关压低明确档位。Responses按已核对的公开原生reasoning.effort补high默认，兼容根reasoning_effort别名但仅同值合并、冲突拒绝；reasoning对象内summary等其他字段及完整input均保留交上游验证，不伪称额外字段被支持。DeepSeek专属store/developer/image/seed/logit_bias等逻辑不复制到GLM；原有Chat reasoning_content通用无损转换保持，assistant reasoning别名只在有证据/明确契约的范围内适配。

## 数据与隔离
继续wire splicing/RawMessage，工具参数含多行JSON字符串和大数不得重编码成损失精度的map。会话HMAC、prompt_cache_key、X-Affinity及记录脱敏不变。Mistral空流失败不降账号健康，不遍历账号，失败TPS未知，详情保留上游原始片段。

## UI与发布
只更新现有vanilla控制台说明，不增加配置/依赖或迁移。预算真实新鲜/历史/未知仍由已有account projection决定；概览不把同组织多Key预算求和。文档清楚区分代码/Mock/客户端/生产验收。本轮无部署，后续发布沿既有OVH安全流程，不重建部署工具。
