# 转换后的历史是否会变化：原生thinking与legacy思考重复（2026-11-09）

## 用户问题与结论边界

用户要求检查转换是否改写内容、进而导致上游零缓存。继续原任务，不新建任务、不把身份HMAC稳定当作模型缓存稳定。

已通过干净线上c1aebbc源码的本地真实HTTP接收复现一个确定转换缺陷：同一assistant同时有原生content.thinking与完全相同的reasoning/reasoning_content，旧实现无条件再prepend一个thinking。native-only为1块，冗余字段出现后变2块；HMAC/显式会话缓存键不变，出站旧消息却不同。冗余字段在轮次间有无变化时会改写准备后的历史前缀。

**这证明本地转换缺陷，不证明原188/user69实际触发或缓存归零根因已确定**。同一原始输入仍会确定性地得到相同输出；该缺陷只有在冗余表示切换/新消息重放时才增加相应差异，不能硬解释此前25→29全部旧入站消息原字节不变的DeepSeek低缓存对，也不能把新追加后缀的变化当成全部旧前缀被破坏。原14条没有完整入站/出站正文，当前生产仍未取得对应长会话的真实TLS Reader证据。本轮无真实推理、正文重放、生产配置/详情变更、重启或部署；本地假usage不用于声称缓存改善。

## 1. 确切重现

```
输入A：content=[thinking(R),text(A)]
输入B：content=[thinking(R),text(A)] + reasoning_content=R
输入C：content=[thinking(R),text(A)] + reasoning=R
输入D：content=[thinking(R),text(A)] + 两个相同legacy字段=R
```

旧代码`mistralAssistantReasoning`将B/C/D变成`[thinking(R),thinking(R),text(A)]`；A保持一块。三精确模型各4个真实本地HTTP post（共12个）均可复现，原生R含完整前后空白/换行/中文，原生块含closed/signature:null，工具schema与Session-Id保持一致。比对的是mock接收到的最终assistant原字节，不是只测hash辅助函数。

新增`mistral_native_thought_replay_test.go`在未修改c1时先红：三模型的冗余字段三种变体均从1块变2块，另有直接函数same用例失败。red.log保存脱敏合成回执，没有真实会话或推理正文。

## 2. 最小修复

- 仅对`deepseek-v4-1-flash-1017`、`zai-glm-5-3`、`zai-glm-5-3-flash` Chat：如果全部原生plain thinking文本按原顺序串接，与已选legacy思考**逐字完全相等**，只移除冗余legacy字段，保留已有content原字节，不再prepend。
- 保留原生thinking/text相对顺序、文本part边界、closed、空signature及所有原始字节。既不trim也不合并原生块，不删不同的真实思考。
- 空格不同/不同思考、签名或未知思考metadata不作为相同表示折叠，沿用原有完整保留行为；原生thinking及嵌套text的歧义重复消费字段不折叠。其他模型行为不变。
- 抽取既有响应中的plain-thinking解析边界为`mistralThinkingTextParts`，请求匹配与响应适配共用一个严格signed/reference/unknown-metadata边界，避免两套解释；响应拼接文本/空白及错误语义不变。
- 不修改HMAC、路由、账号、TTL、缓存键优先级、工具定义或请求模型，不对用户正文做空白清洗。

修改：`internal/server/mistral_request.go`、`internal/server/mistral_response.go`；新增回归`internal/server/mistral_native_thought_replay_test.go`。

## 3. 另一个已确认但未解决的响应往返边界

同一测试文件增加`TestMistralFlatResponseReplayBoundary`，三精确模型×普通/交错布局×完整JSON/逐delta=12组合：

- 普通`thinking→text`经scalar reasoning_content/content往返，思考与回答文字（含前后空格、换行、缩进、中文）都完整保留，没有随机加空格的复现；请求原生JSON/文本part边界不保证原样，不能把JSON字节变化直接当token变化。
- 原生`text→thinking→text`经两个scalar字段扁平化，再构造成请求，会成为`thinking→text`，文本分段和交错位置无法由两字符串恢复。逐delta时同样如此。**这是块结构层的顺序损失，不是messages数组排序，也未由本次重复思考修复解决。**
- 该边界尚未证明在原188发生，不能据合成结构差异声称必然0缓存；它提示完整原生助手历史的保留需要单独无损方案/客户端承载，不能随手伪造签名、全局重排原生块或用新缓存存储真实用户正文来掩盖。

官方依据：Mistral reasoning文档明确多轮“always replay the full assistant message (including ThinkChunk) back into the message history”；GLM5.3 content始终为chunks。URL https://docs.mistral.ai/inference/conversations/reasoning ，source_check mw9ifwpfe1vwmm 的p-1-2有该原文；已人工审阅，不把工具自动unclear当否定或自动通过。未使用泛Mistral分词器冒充GLM实际服务端模板/缓存规则。

## 4. 必要回归与未验证项

干净c1副本只叠加本轮两业务文件及本轮/既有本地回归：

- 原始red两项失败（0.985秒）→修复后green两项0.950秒通过。
- 扩展后normal1.958秒、race3.176秒通过：原生冗余思考/保守边界/响应往返、现有双思考alias与空白占位/冲突、JSON/SSE响应/原生bytes与未知内容失败、SSE framing、三模型132工具25→29→33完整旧历史/实际Reader、原身份跨转换不可变及HTTP2/TLS本地mock。
- 新HTTP证明同一原生历史在三种冗余字段变体下恢复成与native-only逐字相同的assistant，原生内容、工具及会话缓存键未被改写。
- clean c1副本`go vet ./internal/server`和`go build -o /dev/null ./cmd/odb`通过，无额外二进制产物。
- 不跑无关全仓或生产推理；未部署、未验证原188触发、未测实际GLM token序列/生产缓存改善、未解决任意交错块的原生无损往返。

## Bug Analysis: 同一思考的两种表示被当作两个内容片段

### 1. Root Cause Category
- B Cross-Layer Contract + D Test Coverage Gap + E Implicit Assumption。
- 旧实现假定legacy思考与content已有块互不重叠，直接prepend。此前同输入确定性/纯legacy历史追加测试都能通过，缺少“原生已含思考、legacy只是副本”的组合及等价表示切换测试。

### 2. Why Previous Checks Did Not Catch It
- HMAC仅覆盖选中身份/首文本，不能观测assistant历史；稳定HMAC不能为转换层背书。
- 同一输入重复转换是确定的，但两种逻辑等价输入可能被确定性地转换为不同历史。
- 只检查messages索引/alias别名互相一致，未检查native content与alias是否重复表达。

### 3. Prevention Mechanisms
| Priority | Mechanism | Specific Action | Status |
|---|---|---|---|
| P0 | 编码边界 | 同值native优先保留，冗余scalar不再插入；严格逐字比较、不trim | DONE |
| P0 | HTTP回归 | 三模型native-only/两种alias/双alias，最终原字节等价 | DONE |
| P0 | 保守隔离 | 不同空白/思考、签名/未知metadata/歧义字段、其他模型不折叠 | DONE |
| P1 | 共享解释 | 请求匹配与响应共用plain-thinking文本解析 | DONE |
| P1 | 往返边界 | 交错块不能经两字符串无损重放，单独记录不冒充已修 | OPEN |

### 4. Systematic Expansion
- 未来缓存调查必须同时验证“同输入重复稳定”和“冗余/等价表示不改已发送历史”，并检查响应→客户端历史→请求整个往返。
- 上游缓存基于token共同前缀，不是整个JSON相等；转换变化位置、块语义和实际服务端序列决定影响，不能任意一个空格/字段变化就宣布全部归零。

### 5. Knowledge Capture
- 更新backend/mistral.md原生/legacy重复表达及往返边界契约；测试是可执行证据。
- 更新现有任务和workspace；保留Trellis记录。临时red/green/targeted/race日志登记_tmp_artifacts.md，经用户同意才清理。
