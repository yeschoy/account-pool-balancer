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

## 首轮交付状态

首轮已本地修复并通过上述质量门禁；当时没有生产推理、真实私密payload重放、配置改动、重启或部署。以下用户授权发布追记取代原待部署状态。

## 用户授权OVH发布追记

- 用户明确要求解决、避免无用测试并部署OVH。71c1f60已推送master；从实际线上92cc0d7通过独立Git index仅合入本轮6文件，生成/推送release/mistral-empty-annotations-20261111的00ba86a7ccbd427b6973d69de56373a540692567。3个Go文件与前述完整质量门禁的源码逐字节相等，未重复测试。
- 没部署主线已提交但未上线的ModelStudio或工作区注册/指纹/README/Dockerfile未提交改动。631文件SHA256集合验证后复用既有发布引擎，构建UTC13:04:31.052–13:04:43.688，旧进程13:04:43.935干净退出，完整一致备份13:04:44.310–49.768，新启动49.988，首HTTP13:04:51.095，Docker healthy13:05:20.328，双内网入口13:05:20.470通过并立即反馈。
- 191账号、186TH核心注册、routingSecret、config.json字节、Mistral注册库、原13硬隔离和运行参数均保留；诊断变量保持空，无规则/功能开关改动。Compose沿线上仅覆盖image，不在旧工作树构建。
- UTC13:10:31.169真实未登录公网登录页/healthz/四资源全200且SHA256匹配；专用浏览器ovh-mistral-empty-00ba86a已关闭。正式回执补入备份deploy.json。
- UTC13:11:21.151一次只读回顾启动后391秒自然流量：Mistral230成功200、1上游400、0新增422；原失败Key228成功200及该1上游400。400单attempt/upstreamStatus400、invalid_request_error，未归类为annotations，也不隐去或声称全池零错误；不是主动测试或完整10分钟实验，未证明原私密失败payload逐条重过。服务同版本healthy/restart0/无OOM/诊断关闭；无主动生产推理或重放。
- 正式release/镜像/完整备份与分阶段时间、SHA256及回退边界见docs/ovh-deployment.md当前部署记录。部署分支已推送，后续主线仅补文档，不再重启。
- 临时产物登记_tmp_artifacts.md，未经同意不清理；Trellis、新旧正式镜像和回退资料保留。

## 用户要求继续观察：完整10分钟（UTC13:24:55–13:34:55）

- 固定600秒窗口，北京时间2026-11-11 21:24:55–21:34:55。复用已有只读观察器，按完成时间ts+durationMs计最终请求，error按事件时间、attempt另列，不重复计数；窗口末2秒仅接收已在窗口内完成的落盘，不扩大统计边界。
- 43次运行采样始终同版本00ba86a、Balancer与NewAPI均healthy/restarts0/无OOM；config SHA256始终不变。无推理生成、生产重放、配置/账号/规则修改、重启或部署。
- **Mistral305条最终请求：300成功200、4上游400、1客户端取消499；0最终422，0新增422事件/annotations校验错误。**全部来自原失败Key、模型deepseek-v4-1-flash-1017；不能据此覆盖无流量GLM/其他客户端或保证永不复发。
- 4个400的既有错误仅在远端RAM解析，固定公开词表为Total number of images exceeds the maximum of，精确确认图片总数超过上游上限；没有输出图片/正文，未从脱敏片段猜具体上限数字，没有通过删图/删历史或换模型掩盖。此独立限制仍存在，不宣称全池零错误。
- 1次503是中途上游attempt：req_a8fae262322755c25e3b1b15于UTC13:27:19.961触发现有mistral-server-error规则并换号，随后第二attempt200、最终success200，包含于300成功，不当作额外最终失败。没有新增重试规则。
- NewAPI目标渠道180/182/183：300成功消费（16非流式+284流式ok/done/completed），1客户端取消另列；180另4条错误与上述图片超限对齐。缓存/用量观测不作为本次422修复或缓存提升保证。
- UTC13:34:57.253观察器exit0、stderr0；13:35:43独立确认本地PID14867已不存在、服务同版本healthy/restart0/无OOM，503最终结果已核对。已立即反馈完整结果；记录补交不再部署。
