# 设计

- 入口沿用strict JSON/model验证→原始身份读取→Mistral preparation→账号/RPM admission。新适配仅精确模型/Chat；不改identity优先级，不添加cache header来源。
- 复用skipSpace/skipString/skipValue，按原始字段跨度拼接，不经float64。集中处理连续删除和追加，避免原store删除与新多字段删除重叠。developer只替换role值；reasoning沿用RawMessage内容块逻辑但消息对象不再经map重编码，以防重复字段静默覆盖。
- 精确范围内检查被适配/身份字段重复；reasoning/developer消息拒绝歧义字段。错误不含客户端值，只给字段与可用语义；400在账号尝试之前返回。
- thinking仅接受唯一type=enabled/disabled的对象；enable_thinking仅bool。两者必须一致；none只与禁用相容，其他已实证有效档位只与启用相容。null effort视为未指定。enabled默认high，可能增加思考token。
- 官方渲染Chat schema复核：random_seed integer|null、metadata map|null、prompt_cache_key string|null。seed整数原值映射random_seed，双方存在时不覆盖，非一致整数拒绝。具体DeepSeek支持/确定性未生产验证，保留上游真实错误，不自动删seed重放。
- logit_bias仅null/空对象省略；verbosity仅null/medium默认省略；其他有效生成设置拒绝。user/safety_identifier仅string/null作为不提供上游语义的兼容标记省略，不成为会话ID。prompt_cache_retention仅null/空字符串无要求时省略，显式保留期限拒绝。
- 生产发布遵循docs/ovh-deployment.md；上线后先移除role/store/cache/metadata等已覆盖规则，再按本地拒绝合同收敛其余delete。正式备份与旧镜像保留，不整行回灌。

证据：`git show 15fdea0:.trellis/workspace/mistral-cache-header-deep-session.md`；https://docs.mistral.ai/api/endpoint/chat（2026-11-08浏览器DOM复核）。泛Mistral的minimal不用于本型号。
