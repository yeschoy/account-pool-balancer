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

本地阶段：实现/相关质量门禁通过后，代码/spec/文档和本记录一起提交为`4d56af5`，当时未推送/未部署、线上仍`edc4c32`。随后用户明确回复「要」，授权最小补丁部署，结果如下，不再沿用先前“未部署”为当前状态。

### 用户授权部署结果（2026-11-12 11:24 UTC）

- `4d56af5b71b9d4395a08fa2c583e24ebc17e8a8f`已推送master；以实际线上`edc4c32`的独立Git index仅合入6文件，发布分支`release/mistral-glm-role-20261112` / `d94da97a73fa380fcfadba9fee5fe32bc707824c`已推送并部署。未带入ModelStudio或其他窗口注册/指纹/Dockerfile/README/Essevin暂存修改。
- Mistral Go基线无漂移、3Go与已通过门禁的本地源码逐字节相同，不重复测试；647文件SHA256核对，复用既有发布引擎，无存储迁移不额外复制敏感数据。单次预检旧实例healthy0/无OOM、Healthcheck/兼容别名/双内网正常、240.53GiB余量且注册空闲。
- 构建UTC11:23:16.055–11:23:23.244（7.19秒），旧退出11:23:23.514，一致完整备份11:23:23.912–11:23:30.530，切换11:23:30.530、新启动11:23:30.762、首HTTP11:23:31.873、Docker healthy11:24:01.108。旧退出至首HTTP观测8.36秒，非精确停机，构建/健康等待不当停机。
- UTC11:24:01.390：新revision、healthy0、NewAPI到新旧两个healthz、四本机资源SHA256通过，191账号/186TH核心注册/config完整字节/routingSecret/Mistral注册库/原13硬隔离及运行边界保持，诊断变量空；随即反馈上线。沿线上Compose只覆盖image，不改配置/恢复账号。
- UTC11:25:02.118：真实未登录公网登录页可见、healthz/四静态资源全200，URL查询串保留且SHA256匹配；正式deploy.json保存回执、专用浏览器已关闭，立即反馈公网验收完成。
- 正式release`/home/ubuntu/project/account-pool-balancer-releases/20261112T112255Z-d94da97`，镜像`account-pool-balancer:release-d94da97-20261112T112255Z`；完整备份`/home/ubuntu/backups/account-pool-balancer/20261112T112255Z-glm-role-d94da97`，data SHA256`524181610ef59d80fcca582323cf7ad02ad02ad8c205d26578bee05f9ef5bcff`，旧edc4c32镜像`account-pool-balancer:rollback-20261112T112255Z`保留。回退先核对最新data，不自动覆盖上线后新写入。
- **以上仅部署/入口/数据验收，尚未用部署后新的GLM自然恢复样本完成角色路径生产业务复测**；没有主动推理、私密payload重放、开启正文/缓存诊断或额外10分钟观察。不把mock/healthy当原请求已线上成功，不宣称GLM缓存已改善。补记录不重新部署。

本修复只解决已确定的角色422原因，不承诺所有GLM参数/模型授权/长会话缓存已修。原核心缓存任务不结案。临时脚本/回执/干净快照均登记`_tmp_artifacts.md`，须用户同意清理，Trellis与正式回退资料保留。
