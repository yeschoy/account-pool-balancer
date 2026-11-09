# 本轮验证

## 已执行

1. `go test -overlay <baseline> ./internal/pool -run '^$' -bench '^BenchmarkCapacityResearchPool$' -benchmem -benchtime=200ms -cpu=1,8 -count=1`：通过，24组基线。
2. `go test -overlay <baseline> ./internal/server -run '^$' -bench '^BenchmarkCapacityResearch(Prepare|SSE|Redact)$' -benchmem -benchtime=200ms -cpu=1 -count=1`：通过，历史/帧解析/脱敏视图基线。
3. `go test -overlay <baseline> ./internal/server -run '^$' -bench '^BenchmarkCapacityResearchHandler$' -benchmem -benchtime=200x -cpu=8 -count=1`：通过，4组每组200本地请求，status均200，log-dropped=0；不代表长期容量。
4. 4000账号sticky/health CPU、alloc、mutex采样；512KiB准备CPU/alloc采样：通过。完整测量边界和输出见`research.md`。
5. `go test -overlay <owner-only> ./internal/pool -run '^$' -bench '^BenchmarkCapacityResearchPool/accounts=(40|400|4000)/health=true/hit=true$' -benchtime=200ms -cpu=1,8 -count=1`：通过，隔离副本实验。
6. `go test -overlay <owner-only> ./internal/pool`：通过0.527s；`go test -race -overlay <owner-only> ./internal/pool`：通过1.695s。没有将副本patch应用到正式源码。
7. `go test ./internal/pool ./internal/server`：通过（Go测试缓存命中）；夹具gofmt、任务JSON解析、Markdown代码围栏配对、`git diff --check`通过。
8. 官方Usage/limits与prompt caching公开原文核对；未访问账号私有Admin Limits页面或下载凭据。

## 本轮未做

- 没有正式业务Go/JS代码变更，因此未跑全仓normal/race/vet/build、生产/本地UI保存往返或真实客户端E2E；不能把上述定向/缓存结果称作全仓门禁通过。
- 没有真实上游推理、生产压力测试、运行时资源/组织限额采样、配置调整、部署或重启。
- 没有实现可配置会话容量、总buffer预算或正式owner-only/index/统计改造。
- 研究patch尚有线性ID查询和Candidates诊断语义变化，缺正式server/端到端回归及新功能测试，不能上线。

## 质量/安全复核

- 文档区分B/op累计分配与RSS、并行摊销ns/op与单请求P95/P99、累计锁等待与墙钟、合成usage与真实TPM。
- 保留已有原生字节/参数兼容、固定代理、禁重定向/不确定已发不重放、严格owner fail-closed和身份HMAC隔离。
- 初始注册机/Dockerfile等其他修改未覆盖，最终只提交本任务文档/基准证据/Trellis。
- 临时overlay/profile/二进制/浏览器公开文档溢出已登记`_tmp_artifacts.md`，尚未取得清理同意，不清理；Trellis保留。

状态：AC1/AC2研究交付；AC3正式功能待容量语义确认，AC4本轮无生产变更。任务仍planning，不能归档为功能完成。
