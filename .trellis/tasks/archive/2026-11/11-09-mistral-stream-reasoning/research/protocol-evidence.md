# 公开协议与仓库证据（2026-11-09）

真实浏览器只读访问官方文档，无登录或推理。公开页面内容不等于生产模型授权或客户端实测。

- https://docs.mistral.ai/inference/conversations/reasoning ：公开原文 `zai-glm-5-3: Supports adjustable reasoning via the reasoning_effort parameter. Supported values are low, high, and max`；`GLM 5.3 Flash (zai-glm-5-3-flash): Supports low, high, and max reasoning effort. none is not supported.` Large4原生ID为mistral-large-4-0，low/medium/high。
- https://docs.mistral.ai/api/endpoint/responses ：请求字段 `reasoning: ResponsesSupportedReasoning|null`，描述“The reasoning effort to use. Choose an effort supported by the model”; `stream`说明SDK每帧事件在data中；store/previous_response_id无状态限制保持。
- DeepSeek V4.1 Flash 1017的low/high/max与未传high为本轮用户明确产品策略，替代此前max→high，不伪称已真实验证该型号上游生成。
- `docs/ovh-deployment.md`已记录NewAPI glm-5.3/glm-5.3-flash映射至zai-glm-5-3/zai-glm-5-3-flash；不要混入Modal短名或擅加模型别名。

## 缺陷复现
新增`mistral_responses_terminal_test.go`先在旧实现运行：四个首帧completed/incomplete/纯工具终态用例均因等EOF超时；空流错误码不明确，created-only/delta-only/[DONE]等EOF被当成功。改造后本地定向回归通过。只改Mistral Responses窄门，不改变其他池/Chat的旧终态策略。
