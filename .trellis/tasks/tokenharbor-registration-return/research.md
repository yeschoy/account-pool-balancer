# 套餐模型数据研究（2026-11-05）

## 结论
**可以获取按套餐分类的数据，但不能把当前77项 API目录直接当作某套餐的授权列表，也不能只按 API 的 tier 过滤。** 本轮按用户要求做研究，不修改下游 `/v1/models` 或推理调度；先把管理台“可用模型”纠正为“目录模型”。

## 证据

### 1. 原生 Key API
通过生产服务相同网络命名空间、账号已绑定的 SOCKS 出口，对现有两把 TokenHarbor Key 分别只读 GET `/v1/models`。没有推理、注册、付款、重试或出口降级；凭据仅在远端内存，不输出、不下载。

- 两个样本均返回77项。
- 模型字段：`blurb, context_length, created, function_call, id, label, object, owned_by, pricing, supports_prompt_cache, tier, tool_call`。
- `tier` 观察到 `low / mid / high / frontier / null`。
- 没有 `enabled / subscription / allowed / available` 形式的账号套餐授权字段。
- 现有 `quota.FetchCatalogFull` 不保留 tier/pricing，默认缺少 enabled 的项目视为目录启用；这不能证明账号套餐覆盖或有可用余额。
- 两把 Key 的结果相同只证明这两个样本，不扩张为所有套餐的永久契约。

### 2. 官方订阅说明
原文：<https://tokenharbor.ai/docs/billing/subscription>（真实浏览器读取）。

- 套餐覆盖按价格规则加明确名单计算，并非 API `tier` 的简单排序。
- Agent：输入不高于$0.20/百万token且输出不高于$1，加官网指定模型。
- Office：包含Agent，输入不高于$1且输出不高于$4，加官网指定模型。
- Frontier：包含Office及其他有按token价格的模型。
- Free使用 `:free` 路由；购买Pass不自动同意免费模型数据条款。
- 旧代模型随当前代覆盖；多路由模型只有所有服务路由都覆盖时，官网才打勾。覆盖规则和Boost可能随目录更新变化。
- 套餐覆盖、剩余窗口额度、钱包付费能力是三个不同问题。无套餐不等于所有付费模型永远不可调用，有套餐也不等于所有目录模型都被套餐支付。

### 3. 官网公开页面提供更直接的数据
<https://tokenharbor.ai/pricing> 的服务端Flight JSON中可读：

- 当前注册流程已解析的 `free/plans/signedIn/currentCycle`：登录后判断本账号当前套餐。
- 另一个组件有 `models` 映射，模型ID对应 `{label,tier}`，这里的tier为 `free / agent / office / frontier`，与 API 的 low/mid/high 不同。
- 实际样本：`th-rudder → free`、`claude-opus-5.5 → frontier`、`glm-5.3 → office`、`gemini-3.8-flash → office`、`claude-fable-5.1 → frontier`。
- 本次实际取得29组映射：free 3、agent 6、office 12、frontier 8。这些是模型组，不是每种套餐累计可用数；高级套餐还包含低级覆盖。
- 该公开展示映射只包含官网列出的模型组，不能未加验证就覆盖API所有77个旧模型/别名。

<https://tokenharbor.ai/models> 的Flight JSON另外有 `rows/freeRows/otherRows`、`surface`、`tier`、`isFree`、价格和 `speedBuild` 信息，可用于后续精确处理模型组与变体；它仍不是本账号即时推理授权证明。

<https://status.tokenharbor.ai/docs/api/models> 说明一个Universal Key可访问模型目录，调用按token计费；目录不是钱包或套餐剩余额度接口。

## 推荐后续接法
1. 单独读取并缓存官网已计算的覆盖映射，来源和校验时间显式可见；不硬编码会变动的价格/名单。
2. 与本账号最近一次有效套餐观察及原生目录按准确模型ID/已验证别名关联。只对已确认映射显示“套餐覆盖”，其余显示“未确认”，不能补成0或判定永远不能调用。
3. 管理台展示“目录 N / 套餐覆盖已识别 M / 未确认 K”；过期会话、无原注册记录、失败/结构变化保持未知。
4. 若要进一步按套餐裁剪下游模型返回，需单独确认这是“仅限套餐付费模型”模式，并明确钱包付费模型的处理；默认不改变现有路由与原生兼容性。
