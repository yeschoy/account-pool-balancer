# Deploy 接口证据（2026-11-13）

## 官方文档
通过真实浏览器读取 docs.deploy.com/llms.txt 和以下同源 Markdown。未采集登录会话或付款页面；临时原文见忽略的 `.analysis/deploy/`。
- https://docs.deploy.com/quickstart.md：Bearer 使用创建时仅显示一次的 Secret Key；API root 为 https://api.deploy.com/api/v1；可手动使用 SF/VA 或 CDN 根。示例 Chat 会返回 reasoning_content 和真实 usage。
- https://docs.deploy.com/models-and-pricing.md：原生 IDs `deepseek-ai/deepseek-v4.1-flash-1017`、`zai-org/glm-5.3-flash`；两者支持工具、推理和提示缓存。USD/百万 Token 的输入/输出/缓存读取价格分别为0.10/0.35/0.003 与0.05/0.17/0.01，不另收美国推理溢价。
- https://docs.deploy.com/errors-and-limits.md：默认5请求/秒，组织级提额。429 Too Many Requests；401 Invalid or expired token；400 Invalid payload；405 Organization currently paused，要求检查余额/条款或联系支持。405不一定是方法不支持，不足以宣称已测零余额。
- 文档完整索引未列出能用推理 Secret 查询组织 credit 余额的接口。原工作区注册调研记录的组织余额控制面使用 portal Cookie，不能拿推理 secret 冒充登录会话。本次只有用户提供的推理 Key，组织余额未接入，不把促销文案或模型目录当成金额。

## 真实验证（用户显式授权，最小请求，无重试）
使用用户在本会话提供的 Key，通过本地表单仅写入被忽略的隔离 data/config.json；本文不含真实凭据。
1. 管理端只读目录：HTTP200，两个原生模型。
2. 管理端默认 DeepSeek 推理：HTTP200，约1.4秒；模型正确，max_tokens=256上限，reasoning可占据输出预算。
3. 下游模型列表：HTTP200，两个原生模型，使用本地生成的 Deploy 分组下游密钥。
4. 经本地 Balancer 调 GLM非流式：HTTP200，单次账号尝试；prompt16、completion68（reasoning65包含在completion内，不额外累加），finish_reason=stop，约1.6秒。
5. 经本地 Balancer 调 DeepSeek流式：HTTP200，单次账号尝试；8个data事件，保留 reasoning_content，收到 `[DONE]`，约1.1秒。

无创建/删除上游 Key，无注册、充值、绑卡、自动续费、部署或压测。目录成功只证明模型发现，真实请求才证明本次推理授权；不承诺永久有效。
