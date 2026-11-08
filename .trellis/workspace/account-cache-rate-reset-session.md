# 账号缓存命中率与24h命中率清空

## 需求与范围

- 根据账号列表截图增加逐账号24h缓存命中率；概览增加当前号池清空按钮。
- 只清除cp/ck/ckr配对测量样本，保留原始请求数、Token、费用、日志、健康及会话绑定。仍用既有Stats mutex和metadata，无新调度器/统计库/配置项。
- 只识别已有Codex/Claude会话规则，不改优先级或实际转发。完整头名单及两套优先级补入docs/mistral.md。
- 截图中的[REDACTED]来自Balancer日志副本脱敏；原始头不改写。UI补说明，不据此断言NewAPI是否改过原值。
- 小范围改动不建任务。当前仓库缺少.trellis/scripts及workflow，使用已有spec并手工记录，不修改共享Trellis环境。其他窗口的Mistral注册/指纹、Dockerfile及其他日志保持原样。

## 实现与边界

- 管理员POST /api/stats/reset-cache-rate?provider=...复用session/CSRF；显式白名单且不含独立Perch。校验失败不清空，持久化失败500明确提示内存已清空。
- provider和overall的全局/账号/模型/代理分钟桶同步更新，仅减去该池同分钟的命中率样本；清空后同分钟新请求立即计入。导出/导入不会用原始Token回填已清空样本。
- 账号/概览/模型统一使用已测ck/cp，缺测/零分母为—，已测零命中为0%。旧版只有原始Token、无配对测量的历史也为—，不伪造覆盖。
- 单条请求日志仍是原始记录，不随清空变更；清空不影响上游缓存。对话框锁定确认前的号池，切换页签不误清新池。

## 验证

- 全仓go test ./...通过；首个串行normal+race命令的120秒外层超时后，单独重跑go test -race ./...全部通过。go vet ./...、go build -o /dev/null ./cmd/odb通过。
- 所有internal/server/*_test.js行为测试、app.js语法和git diff --check通过。既有OpenDesign VM测试补加载新共用缓存率helper。
- 最终定向race覆盖ResetCacheRate/客户端头脱敏/缓存键原值直传，全部通过；新增持久化失败用例通过。
- 本地真实浏览器使用假账号，验证80.0%、未知—；点击确认后账号/概览/模型变—，原始1请求/1000输入/800缓存/$0.5保留，River300/1000不变；后端API及metadata落盘均核验。
- 本地专用浏览器已关闭，PID22874正常停止，62069端口确认关闭。临时目录及自动快照登记于_tmp_artifacts.md，未经同意不清理，Trellis日志保留并提交。

## 交付状态

代码、回归、文档与本地浏览器验收完成；未推送、未部署，未操作生产账号、推理或统计。线上不因本地实现立即生效。

## 用户追加：客户端会话/缓存头不脱敏

- 用户明确要求原值展示，覆盖上一阶段“全部身份头脱敏”的策略，仅调整Mistral管理员请求摘要的clientRequestHeaders。使用既有maskHeaders(cfg,h)保留会话/父会话/自定义/缓存/X-Affinity原值、空白和多值，继续512字节/4值/64头/8KiB边界及截断标记。Authorization/API Key/Cookie和已知密钥继续隐藏；即便认证头配置为会话候选也不绕过。
- 不改请求身份/缓存键生成/转发、其他池、正文/上游详情/错误日志或绑定持久化。旧日志已写入的REDACTED无法恢复，UI和docs明确说明新请求才采集原值。
- 日志API补Cache-Control:no-store；新增管理员可见、运行Key401、凭证不泄露、全部会话头多值/空白保留等回归。既有Mistral隐私用例仅为授权clientRequestHeaders单独断言原值，其余字段仍检查不泄露。
- 全仓normal/race、vet、build及所有Node行为/语法测试通过。定向Mistral+ResetCacheRate race通过。
- 真实本地浏览器使用合成日志验证请求日志→客户端头弹窗：session/thread/cache原值、HTML转义、无脚本节点、凭证隐藏及API no-store；后端真实mock请求的原值采集由Go HTTP回归验证，不把合成UI记录声称为生产证据。回执header-browser-receipt.json已核验；PID40554与52757端口及专用浏览器均停止，临时产物登记在_tmp_artifacts.md待同意清理。
- 此追加仍未推送/部署；原注册/指纹、Dockerfile及其他窗口记录不触碰。
