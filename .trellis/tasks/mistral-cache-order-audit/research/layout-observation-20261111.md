# 46a9b8f上线后完整10分钟：业务回归，未验收（2026-11-11）

## 授权与窗口

用户明确「观察10min看看有什么错」，只读自然流量，不授权新推理、配置/账号/规则修改、重启或新发布。本轮固定UTC2026-11-11 **17:10:12–17:20:12**（北京时间11月12日01:10:12–01:20:12）600秒。复用已有观察器，按Balancer请求ts+durationMs完成时刻在[start,end)计最终结果，错误按事件时刻、attempt另计；NewAPI按整秒记账，结束2秒补采只收窗口内落盘，不扩大统计范围。

## 运行与最终计数

43次采样Balancer同46a9b8f及NewAPI均healthy、restarts0、无OOM；config SHA256始终不变。观察器UTC17:20:14.641 exit0/stderr0，所属PID38002已不存在；UTC17:21:38独立只读交叉核对服务仍同版healthy0/无OOM，无驻留进程。

Mistral **110最终请求**：

| 最终结果 | 次数 | 说明 |
|---|---:|---|
| 200 success | 69 | 均无恢复历史 |
| 400 failed / request，上游400 | 25 | restored>0，全数失败 |
| 400 failed / upstream | 8 | 既有图片总数超限规则400-image |
| 400 failed / request，零attempt | 4 | 本地拒绝，无上游状态；不能和25个上游400混计 |
| 499 client_cancelled | 4 | DeepSeek2、GLM5.3的188Key2，另列 |
| 422 | 0 | 无新增422或annotations验证错误 |

失败37/110=33.64%，不以服务healthy/无422声称新版业务验收成功。所有25个恢复请求均为精确DeepSeek下游原失败Key；本窗口没有GLM恢复成功样本，GLM2条均客户端取消。

## 最关键的交叉验证

- `mistralLayout.restored`总数25，含恢复的请求25；按同一固定窗口重扫最终request逐ID对账：**25个全部1attempt、upstreamStatus400、最终failed400，0成功**。
- 无恢复历史的69请求success200；另8图片超限400、4客户端取消、4零attempt本地400。不能把25次内部匹配/重建叫作25次“恢复成功”。
- 恢复范围内上游错误的安全词表/语法骨架为`Expected at most one [unknown] [unknown], got [number]`。只在远端RAM解析，未输出原错误自由文本/input/正文/凭据；当前尚未确定未allowlist词语的确切含义、原生结构与此错误的具体因果。**这是高度直接相关的生产回归，先止损，不能无证据甩锅上游或客户端。**
- 4零attempt请求的布局摘要restored0，legacyUnknown分别169/745；末次checkpoint有unavailable16，可能涉及有界记录处理，但本轮未取到这些本地响应的精确代码，不冒称已经全部定责。

## 布局记录观测，不能充当业务成功

窗口内110请求带摘要：restored25、legacyUnknown10507（历史消息出现次数，非独立会话数）、native0、captured52、captureUnavailable9。结束后现有checkpoint113（recoverable89/unknown8/unavailable16）；只是持久状态快照，不等于窗口内所有成功/失败次数。

原有helper/HTTP mock回归通过只能证明本地仿真下的块顺序/校验/持久化，不证明Mistral官方能接受重建后的原生结构。当前25/25上游失败证据足以判定**生产业务验收失败**；新的方案必须先离线定位和加强真实schema/逻辑块的约束，不可以继续用本地mock绿作为已交付证明。也不为查问题擅自付费重放业务。

## 其他池与NewAPI

- OpenCode Go134成功、无最终失败；Modal5成功，2个429尝试事件另计。
- OpenDesign211成功、7最终429、1最终524、1取消；尝试事件400/402/524按原规则另计，不能把48条400尝试当48最终失败，也不把这些错误归Mistral。
- NewAPI180/182有69成功消费（19非流式+50流式ok/done/completed），目标180/182错误记录共37与Mistral失败对齐；182/188另4取消。其他渠道134/165/198/207错误单列，不冒称号池全部零错。

## 结论与待用户动作

完整窗口结果已立即反馈：原422未复发，但新增布局恢复有明显业务回归，建议**先回退本次布局功能到00ba86a，保留空注解422修复**，再离线修复。用户当前只要求观察，本轮未擅自回退/改配置/部署；线上仍46a9b8f，不隐藏此风险。

回退须先完整备份**当前最新**data，再为旧strict reader仅移除派生metadata.mistralLayouts（保留最新账号/凭据/额度/现有bindings/日志），沿现有Compose仅覆盖保留的旧镜像，核验双内网入口及healthcheck；不能恢复发布前data覆盖新写入。实际回退需明确授权。任务保持in_progress并标记业务验收失败，文档同步，不把观察记录提交触发重启。

临时脚本/脱敏回执登记_tmp_artifacts.md，未经同意不清理，Trellis保留。
