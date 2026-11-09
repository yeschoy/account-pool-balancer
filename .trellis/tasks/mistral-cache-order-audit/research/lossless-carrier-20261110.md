# 无损块布局回传：真实中转版本与客户端能力核对（2026-11-10）

## 授权与本轮结果

用户批准继续核对NewAPI/客户端是否能携带原生块或额外布局信息。沿原任务；本轮只读既有运行版本/目标历史UA品牌，拉取固定公开源码并进行全假数据DTO/SDK探针。未启用生产采样、详情、重启、部署、模型推理或真实正文重放。上一轮170be84重复思考修复仍仅已提交，未部署。

**已找到可用的中转载体，但默认客户端不会自动完成回传。尚未实现完整无损重放，也未验证真实用户客户端。** 下一步实现范围需要确定：是否允许用户端安装/修改provider适配器，还是必须完全不动客户端。

## 1. 先纠正“当前版本”

UTC17:37:42只读Docker实际容器Image ID与镜像名：

- Balancer仍release-c1aebbc-20261109T130529Z，revision c1aebbcc452212bb262a18d3e95528548234344b，healthy/restarts0。
- NewAPI当前镜像为`ghcr.io/yeschoy/new-api:cb5b6399327fab39e2745cd0cd6d8179c55351e6`，Image ID `sha256:aba8969e5fb875b67390e30f47450a289ba05900bbd7bd752c731c9ade7453f9`，healthy/restarts0。没有OCI revision/version标签，故本轮按该不可变镜像的标签对应commit固定拉取源码，未另行校验运行二进制VCS。
- 从公开yeschoy/new-api取得cb5b639完整commit（2026-11-09），而不是修改/使用本地3914bf702工作树。旧官方48a6c87只作为此前观察/对照；不继续冒称它是当前运行版。切换时间未核实，不反推过去采样版本；既有双跳payload比对证据不依赖该源码标签。
- 原188目标Key历史长请求18条，14条原业务UA仅`opencode/latest`，另4条不是可识别版本。不由此推定准确客户端或Bili插件版本。本机安装OpenCode1.2.16，公开对应tag e66d829d1875f5a5fd8b0bfcab69cacb48b11b5d（2026-03-03），仅作可重复的参考，**不冒充原用户版本**。

## 2. 当前NewAPI：原生流数组不兼容，但annotations可传

直接编译执行cb5b639的实际relaykit/dto，使用合成JSON：

| 载体/行为 | 当前源码DTO结果 |
|---|---|
| SSE delta.content原生数组 | 拒绝，Content为*string |
| SSE delta.annotations | json.RawMessage保留 |
| 请求Message.annotations | json.RawMessage保留 |
| reasoning_details额外字段 | 请求/重编码stream均不保留 |
| provider_metadata额外字段 | 请求/重编码stream均不保留 |

定位：`relaykit/dto/openai_response.go:89–99`、`relaykit/dto/openai_request.go:471–486`。

当前`relay/channel/openai/relay-openai.go`即使ForceFormat=false可以转发原始SSE data，仍会通过processTokenData等路径解析ChatCompletionsStreamResponse做统计/状态，因此不能简单关闭Balancer响应转换就断言native流已经端到端可用。原生非流message.Content为any与stream不同，不能把非流成功冒充流兼容。

annotations是已有RawMessage扩展通道；未来若承载APB布局，必须明确标成私有扩展，不伪装标准URL citation，不覆盖现有引用条目，并在发给Mistral之前消费自身条目。此处仅证明DTO往返，不冒称所有channel配置/协议转换均无损。

## 3. 参考SDK真实执行，不只读字段名

取OpenCode1.2.16包清单固定依赖：`@ai-sdk/openai-compatible@1.0.32`、`@ai-sdk/mistral@2.0.27`。npm隔离安装于.analysis下，禁scripts/audit/fund，缓存与lock也留在该忽略目录。所有SDK fetch替换为内存Response、显式合成API Key，未读取/使用真实模型Key。

探针最终通过：

1. openai-compatible拒绝native response.content数组。
2. 给其prompt传`text(A),reasoning(R),text(B)`，下一请求被编码为content=AB、reasoning_content=R，块序不保留。
3. 在reply附annotations/私有布局字段，默认JSON与stream不会将其放入可自动回传的content/providerMetadata。SDK可能保留原始response.body供调试，不能误说全部字节被销毁；它不等于历史自动回传。
4. 参考Mistral SDK非流响应能产生text/reasoning/text parts，但**请求转换会将reasoning文字加进assistant普通content，得到ARB字符串**，并非native ThinkChunk。因此“只切Mistral SDK”不是已验证解决方案。定位`convert-to-mistral-chat-messages.ts:66–101`。
5. 通过显式配置导出的OpenAICompatibleChatLanguageModel的metadataExtractor提取annotations，再明确放到下一assistant的providerOptions.openaiCompatible.annotations，SDK确实发送完整annotations。说明客户端适配器路径技术可行，但本探针是显式提取/回传，不是假称默认SDK或实际OpenCode已经自动支持。
6. 此参考版本createOpenAICompatible factory不向模型传metadataExtractor参数；只在JSON配置加同名选项无效。需要实际适配代码/导出模型类，不是拍脑袋增加一个字段。

参考OpenCode ProviderTransform在interleaved配置下会合并reasoning并移出content；其MessageV2能保留部分part.providerMetadata，但不代表最终SDK会编码为message.annotations。需要完整检查/适配消息转换和流结束metadata传播，尤其纯工具回复、并发、取消和换模型，不得只凭“有providerMetadata字段”就宣布无损。

## 4. 建议路径与选择门禁

推荐在客户端允许适配时走**显式布局载体、无跨请求服务端正文存储**：

```
原生块 → 标准可见文本/思考 + 私有布局/分段/closed信息
       → NewAPI annotations
       → 客户端provider适配器保留并回传
       → 号池校验身份/模型/完整文本后重建原生块
```

后续需设计带版本、认证和调用方/会话/模型隔离的载体，限制布局大小，保留全部文字/空白/块顺序，校验失败明确拒绝而非猜测；SSE、工具回复、并发、取消、重启、历史导入和元数据丢失均需明确行为。annotations只承载布局或安全胶囊，不塞原始思考正文，不泄漏路由HMAC，不破坏现有引用，也不把签名伪造成原生供应商签名。现有客户端默认不改变，能力协商后再启用。

若完全不能改客户端，只能另议号池有状态布局恢复：至少需要有界隔离、持久化/重启、过期、相同文本不同布局歧义、客户端改字/丢思考、迁移旧历史、SSE不完整和资源限制处理。不能默默保存原始用户正文，也不能布局过期就退回猜顺序却声称无损。没有响应身份回传时，同一scope里相同两字符串可能对应不同原始布局，必须承认此不可区分性并拒绝歧义，不能许诺透明覆盖所有情况。

**尚待用户的唯一范围决策：能否调整用户端provider适配器/配置，或必须完全不动客户端。** 这是产品/部署约束，代码无法替用户决定。先做此决策，再在现有任务PRD/design/implement内收敛实施，不凭“继续调查”擅自新增持久化重放服务或改用户客户端配置。

## 5. 本轮交付与产物

- 已完成固定来源读取、实际cb5b639 DTO编译探针和两SDK内存mock探针；最终JSON回执均通过。
- 没有本轮业务代码、客户端配置、线上配置或部署变更；170be84仍未部署，原188缓存根因/改善仍未验证。
- 正式约束与证据更新进Trellis；临时公开源码、npm依赖/缓存/lock、探针源码、合成校准stderr和脱敏回执登记_tmp_artifacts.md，经同意再清理；不清理Trellis/用户全局Go依赖缓存。
