# Mistral再次422：DeepSeek历史空注解兼容（2026-11-11）

## 证据与范围

- 用户要求定位并解决再次出现的Mistral422，按小修复处理，不新建任务。
- UTC12:44:50只读确认线上仍为92cc0d7024c49c6da80c55a1a7b6a1e148b93e39，running/healthy/restarts0；无生产变更。
- UTC12:45:39按完成时间聚合：近1小时810成功200、36最终422、3最终400、1取消；近24小时66最终422。request/error同一请求不叠加计数；不把66条全部归成同因。
- 最近重复422为deepseek-v4-1-flash-1017；安全诊断显示assistant.content.[other].1.text.[other]/extra_forbidden，涉及零基消息5/11/13/15。没有把首个content.str/string_type的union分支误判为整段数组非法。
- 既有目标详情15条匹配0；从NewAPI已有有界stderr body preview在远端RAM恢复明确坐标body.messages.5.assistant.content.[list].1.text.annotations，input为长度0的list。仅输出固定词表坐标、类型/长度，未输出正文、思考、凭据、真实会话或注解内容。注入该空字段的具体客户端/中间层未确定，不据此定责NewAPI。

## 修复

- 复用mistralDeepSeekTextChunks，仅精确DeepSeek Chat assistant文本块移除annotations=null或空数组，兼容数组内空白与转义字段名。null为语义等价的回归扩展，不冒称生产已观测null。
- 真实非空注解/引用、未知类型原样保留给上游验证；重复/转义重复字段在本地400，零上游请求。用户/系统/原生thinking内部字段、其他模型/Responses/FIM/其他池不变。
- 先做窄文本字段处理，再沿原reasoning转换/原生思考去重；不删历史、思考、正文、工具、图片，不改账号、重试/并发/缓存键/TTL/额度或模型。
- 安全loc白名单只增加固定annotations名字，其余未知路径和input/ctx仍不记录。

## 红绿与质量门禁

隔离源码为干净线上92cc0d7加本轮3个Go文件；原工作树注册/指纹/Dockerfile/README修改未带入。已经核对HEAD与92cc0d7的请求/文本/诊断实现基线相同。

- 红：旧实现精确回归失败，空数组/null仍发送、诊断只能显示[other]，exit1（server1.771s）。
- 定向绿：相关文本/空注解/Reasoning/Image/原生思考/前缀/参数/隐私回归exit0（server1.690s）。
- 干净线上基线+补丁全仓go test ./...：exit0（server86.509s）。
- 全仓go test -race ./...：exit0（server112.850s）。
- go vet ./...与go build -o /dev/null ./cmd/odb：exit0；gofmt/diff check通过。
- 新测试包含真实本地HTTP mock最终上传、单attempt、完整晚历史、原生思考闭合元数据/相同legacy不重复、普通legacy保留、工具/图片/缓存/大整数、空槽与标准ephemeral组合、意义值与角色/模型/路径隔离、重复零attempt及诊断隐私。

## Bug Analysis

1. **类别B/D/E：跨层协议契约与覆盖缺口。** 通用客户端文本块允许空annotations，但精确Mistral DeepSeek原生text schema拒绝它。之前修复了cache_control与空text槽，并未覆盖独立annotations字段；同一个HTTP422不代表旧修复失效或额度错误。
2. **防复发：** P0在已有文本边界补最小规则和最终HTTP组合测试；P0为固定annotations加入安全坐标，避免后续只剩[other]；P1在spec记录仅空值可消费、非空不得丢弃。均已完成。
3. **系统性边界：** 不全局删除未知字段，不推广到GLM/其他池；非空引用与未来无损carrier仍须保留，未知422继续取证而不是扫号/重放。

## 交付状态与待办

已本地修复并通过上述质量门禁；本次没有生产推理、真实私密payload重放、配置改动、重启、部署或线上修复验收。生产仍92cc0d7，需用户确认发布/短重启后，按docs/ovh-deployment.md由干净提交构建、完整备份及双内网入口验收；不能直接部署包含未上线ModelStudio的主线HEAD或脏Dockerfile。临时产物登记_tmp_artifacts.md，未经同意不清理，Trellis保留。
