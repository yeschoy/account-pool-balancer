# 验证与交付状态

## 真实证据

见research/details-evidence.md：仅自测-test，固定窗口82条摘要、15条原生详情。15/15客户端→实际上游可见头尾在必要兼容差异之外相等，原生/适配/日志usage相同。主组14条同技术会话、同账号，仍有0/14592/14720缓存回退。详情中段截断，未宣称恢复全部历史字节。未修改线上开关、配置、账号或请求，未重放/主动推理/部署。

## 完整本地HTTP回归

新增TestMistralHTTPGrowingHistoryKeepsFullWirePrefix：49个故意倒序工具、非字母序schema properties/required、8轮连续追加、原位developer转换、reasoning/reasoning_content交替、tool_calls与大整数、图片URL/MimeType兼容、约7MiB完整历史。断言真实上游接收的工具/metadata原始字节、root顺序、每条旧prepared消息字节、消息角色/工具关联、完整大tool结果、固定账号和缓存键/亲和头。通过，未发现转发随机重排序或日志cap误截请求。

## 先失败再修复

TestMistralProviderTraceIDReachesClientAndSummary在旧代码下，Mistral-Correlation-Id和X-Kong-Request-Id的JSON/SSE共4种情况同时漏掉下游和摘要；X-Request-Id原有2种情况正常。before-fix退出1（1.211秒），失败仅上述trace断言；顺序回归通过。

修复后Mistral按X-Request-Id > Mistral-Correlation-Id > X-Kong-Request-Id选实际有效值，不修改原生头。单测覆盖空/非法/超长跳过、优先级和其他池不启用fallback；原生cached=0仍保持0。OpenCode buffered只显式传legacy provider参数，原行为不变。

## 一次质量验证

- go test ./internal/server -run 'Test(Mistral|OpenCode)' -count=1：通过，8.575秒。
- go test -race ./internal/server -run 'TestMistral(HTTPGrowingHistoryKeepsFullWirePrefix|ProviderTraceIDReachesClientAndSummary|UpstreamRequestIDPrecedenceBoundsAndIsolation|WireDetails)' -count=1：通过，10.909秒。
- go vet ./internal/server：通过。
- go build -o /dev/null ./cmd/odb：通过，无手工二进制。
- gofmt、git diff --check：通过。
- 未重复无关全仓测试，无npm/pnpm安装或新后台进程。

## 交付边界

已实现的是上游trace诊断缺陷修复及完整顺序回归保护；**没有已验证的缓存命中率优化补丁**。尚无账号池造成重排序的证据，不擅自改消息/工具顺序、丢思考、固定整个Key到一个号或改统计制造高命中。

代码/规格/本任务记录提交Git，不推送或部署，其他窗口注册与Dockerfile改动保持原状。原账号池运行be14987，本地修复未上线。低缓存业务问题继续等待完整payload的受控A/B授权或Mistral侧按原生关联ID调查，任务不虚假标完成/归档。临时审计源码/脱敏结果/测试日志留在已登记的.analysis/cache-order-20261108/，经用户同意才清理；Trellis保留。
