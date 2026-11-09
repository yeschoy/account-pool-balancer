# 当前Mistral错误的处理建议（方案，未实施）

用户问“该如何处理”。本轮只核对已有错误证据、项目契约及官方函数/搜索工具文档，没有修改业务代码或生产配置、重启、推理测试。工作树另有容量/注册改动，不能直接把当前工作树打包部署。

## 优先级与安全边界

1. **思考字段冲突400（影响最大）**：精确核对原始入站→NewAPI→Balancer的冲突消息序号、两个字符串长度/指纹/相等或包含关系；比较只在RAM，不记录真实思考。当前仅确认两个非空字符串不等，尚不能断言仅空白/包装差异。若是有文档依据、可逆的包装或完整重复，做精确无损归一；真正不同必须追溯客户端/中转来源，统一单一可信字段。不能直接优先reasoning_content、拼接两段或丢弃reasoning，因为都可能改写历史/语义/前缀。现有冲突保护并非无证据可删的bug。
2. **文本块cache_control导致422（最小兼容补丁候选）**：只在已证实的Mistral精确模型/Chat/文本块范围处理标准缓存提示；先确认实际对象形状。若只是允许作为跨协议提示的ephemeral标记，可按明确兼容策略仅消费该字段，所有正文/图片/工具/消息顺序和大整数保持原字节；稳定prompt_cache_key仍沿原链路。不能把Anthropic块级缓存边界/TTL声称已等价映射到Mistral。显式TTL/未知或有语义要求的结构不得静默丢弃。其他号池和不相关字段不动。
3. **GLM Flash web_search导致400（客户端工具能力问题）**：Mistral Chat function调用由客户端真正执行并回传tool结果；原生web_search属于其Agents/Conversations工具能力，不能直接塞到当前Chat接口。优先让客户端使用其已有真实搜索函数/插件（function schema+实际执行+结果回传）；若用户不要联网，可明确关闭该会话的原生搜索。不能仅把type从web_search改function、静默删除工具，或未经验证切换到Agents并声称GLM支持全部新协议。
4. **503/code3800（上游服务问题）**：保留现有不扣账号健康、不无收益轮号的规则及Retry-After。不要增加无边界重试，尤其流已提交或发送结果不确定时。若要业务降级，应经用户确认配置已有可用备用渠道/模型，并明确会话/缓存/模型语义变化；不是改号池代码就能根治官方不可用。
5. **原渠道188缓存下降**：仍独立跟踪，不用以上不同渠道/模型错误冒充缓存根因。诊断窗口已到期，仅1条短探测，无目标相邻长历史。下一次取证需与目标用户真实继续会话同步；不为续采反复重启，不把短请求0缓存当异常，不主动重放私密上下文。

## 交付建议

先完成冲突形态取证和标准cache_control小补丁，客户端搜索能力单独处理；只跑受影响的精确回归，不重复全仓。若后续授权实施部署，必须从明确干净提交快照构建，隔离其他窗口容量/注册未完成代码；沿既有一致备份、Compose镜像覆盖、双内网/版本/健康验收。方案本身不触发部署，也不能把尚未获证的归一逻辑写成已修复。

## 官方参考

- https://docs.mistral.ai/inference/conversations/function-calling
- https://docs.mistral.ai/studio/agents/agent-tools/websearch
- https://docs.mistral.ai/studio/connectors/conversations
- https://docs.mistral.ai/inference/conversations/advanced/prompt-caching
- https://docs.mistral.ai/inference/conversations/reasoning

当前搜索结果明确区分原生Websearch与Chat函数调用；不能由搜索不到cache_control独自证明所有接口不支持。本次文本块不支持的依据是已取得的精确上游extra_forbidden，不是泛化猜测。
