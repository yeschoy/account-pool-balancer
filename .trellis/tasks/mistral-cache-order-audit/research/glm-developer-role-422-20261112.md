# GLM developer角色422最小修复（2026-11-12）

## 请求与边界

用户先要求检查最近三小时Mistral错误/缓存，随后明确要求解决其中两条GLM422。小修复沿现有任务，不新建任务；不改客户端/NewAPI配置，不主动生成或重放业务请求，不改线上配置/日志开关，不自动部署重启。既有注册/指纹/Dockerfile/README和已暂存Essevin文件不混入本轮提交。

## 证据链：补充容器日志后确定原角色，不再停留在猜测

固定UTC2026-11-12 07:56:21.977–10:56:21.977完成时刻窗口，Mistral3703请求为3668成功、28图片总数超20上游400、1GLM容量3505最终429、2GLM422、4客户端取消。成功且input/cached完整的3665条为543921721输入/362883584缓存，Token加权66.716141%；仅描述该窗口，不将图片同code3051混成旧ThinkChunk错误或本修复收益。

两次GLM Flash错误：

- UTC08:38:43.060103212，`req_ee08d8d72799958e7ac550ac`。
- UTC08:38:43.347215995，`req_cf7465922277bbd279324dd4`。
- Balancer安全诊断均为`body.messages[0]` / `union_tag_invalid`，原始reason已省略正文，既有details不存在。
- NewAPI数据库两条type5摘要也没有原角色；**不能仅凭上述坐标推断developer**。
- 用户追加后只读两容器UTC08:38:00–08:39:00的既有日志，Balancer无原始错误，但NewAPI596行中有两条原生`union_tag_invalid`，均明确：**Input tag 'developer' found using 'role'**。只在远端RAM解析，输出固定角色枚举/安全坐标；没有下载原日志、输入/思考/会话/凭据。
- 官方Chat页面`https://docs.mistral.ai/api/endpoint/chat`当前messages为`array<SystemMessage|UserMessage|AssistantMessage|ToolMessage>`，不含DeveloperMessage。专用只读浏览器`mistral-glm-role-docs`已关闭。

因此确定是GLM Chat不接受developer角色，不是账号额度、缓存、annotations或思考块顺序问题。首轮“日志不足以定性”的结论被本次新的容器日志证据补全；不是把猜测改写成历史已知。

## 根因与最小实现

`prepareMistralRequest`已按精确模型计算DeepSeek开关和三模型reasoning开关，但`mistralChatMessages`旧developer布尔同时控制developer→system、DeepSeek图片适配和文本块清理；GLM传false，故developer原样上传并被拒绝。

- 将该形参重命名为`deepSeek`，保留其图片/文本块清理原范围。
- 仅developer→system条件扩展为`deepSeek || reasoningAlias`，即精确`deepseek-v4-1-flash-1017` / `zai-glm-5-3` / `zai-glm-5-3-flash`的Chat。
- 全历史逐消息只替换role值，支持转义字段名/值；不合并/移动/删除消息，不改指令内容、工具、数字、prompt cache key/亲和或身份HMAC。既有assistant思考转换照原契约执行。
- GLM不继承DeepSeek的seed/store/verbosity/logit_bias/user/retention、图片MimeType/URL、文本cache_control/annotations等适配。
- 重复/转义重复role仍本地400、零attempt；未知role仍明确由上游拒绝且不重放。Large4/其他模型、Responses/FIM/input_tokens与其他池不改。
- 没有新增日志字段、原文存储、配置/状态迁移或生产诊断开关。

真实失败样本是GLM Flash；GLM5.3同原生角色schema由本地mock覆盖，不能称本次已有GLM5.3真实角色修复推理验收。

## 验证

在干净HEAD源码快照中仅加入本轮三Go文件，不使用工作区并行注册改动。`edc4c32`实际线上基线与HEAD的两份被修改既有Go文件无差异。

- **旧代码红回归**：新GLM角色原字节测试两模型×三种cache形态全部失败；本地真实HTTP两模型×JSON/SSE均收到同坐标`union_tag_invalid`422，单attempt，精确复现未转换developer。server0.887秒，退出1为预期。
- **补丁绿回归**：相关GLM/developer/参数/思考/模型矩阵/布局/图片/annotations/native-thought/原身份转换测试，server **2.331秒**通过。
- 相同相关范围 **race4.584秒**通过。
- `go vet ./internal/server`、`go build -o /dev/null ./cmd/odb`通过，gofmt和定向diff检查通过。
- 新覆盖实际HTTP JSON/SSE200、原生返回不变/单attempt/真实mock用量100输入64缓存；首条/后续及转义role、完整工具/思考/大数字、原输入不可变、显式cache字符串/null/空串、重复零attempt、未知角色422不重放及model/path/provider隔离。
- 旧GLM参数隔离回归更新为仅允许role别名差异；其他参数/内容仍逐字节一致。没有运行无关全仓测试或生产推理，不把mock422→200冒充原私密payload已在线重过。

## 交付状态与下一步

本地实现/相关质量门禁已通过；代码/spec/文档和本记录一起提交，未推送、未部署。线上仍是`edc4c32`，不会因为本地修复就自动生效。后续获授权部署时应只合入本轮最小补丁，从实际线上基线隔离发布，沿既有预检→构建→一致完整备份/Compose镜像覆盖→双内网别名/健康/必要资源验收流程；不夹带未上线ModelStudio或其他窗口脏修改，不开启诊断续采、不追加未经授权生成。

本修复只解决已确定的角色422原因，不承诺所有GLM参数/模型授权/长会话缓存已修。原核心缓存任务不结案。临时脚本/回执/干净快照均登记`_tmp_artifacts.md`，须用户同意清理，Trellis与正式回退资料保留。
