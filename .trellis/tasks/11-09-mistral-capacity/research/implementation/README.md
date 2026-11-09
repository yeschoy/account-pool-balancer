# 正式实现：本地回执

2026-11-09，Apple M4 / darwin-arm64 / Go1.26.8。主会话执行；无独立agent审阅、生产配置或真实上游推理。

## 完整门禁
- go-test.txt：`go test ./... -count=1 -timeout=180s`，通过。
- go-race.txt：`go test -race ./... -count=1 -timeout=240s`，通过。
- `go vet ./...`、`go build -o <登记的临时目录>/odb ./cmd/odb`、`node --check internal/server/web/app.js`、`git diff --check`，均退出0。
- node-tests.txt：`for test in internal/server/*_test.js; do node "$test"; done`，全部通过。

## 基准
- pool-after.txt：`go test ./internal/pool -run '^$' -bench '^BenchmarkMistralCapacityFlow$' -benchmem -benchtime=200ms -cpu=8 -count=1`。夹具在正式 `internal/pool/capacity_bench_test.go`，包括Normalize、已发布式ConfigureBindings及真实lease周期。
- stats-after.txt：`go test ./internal/stats -run '^$' -bench '^BenchmarkSparseAccounts$' -benchmem -benchtime=300ms -count=1`；每轮4000账号各一条样本，B/op为整轮累计分配，不是RSS。
- components-after.txt / handler-after.txt：把 `internal/server/zz_capacity_research_test.go` overlay到本任务 `research/server_after_bench.go.txt` 的绝对路径（Go JSON Replace映射）；`go test -overlay <映射> ./internal/server -run '^$' -bench '^BenchmarkCapacityResearch(Prepare|SSE|Redact)$' -benchmem -benchtime=200ms -cpu=1 -count=1`；Handler换为 `-bench '^BenchmarkCapacityResearchHandler$' -benchtime=200x -cpu=8`。
- 旧基准在上级research.md及改造前夹具/回执；这些是独立短样本，有调度/GC/环境波动，不是配对统计检验。摊销ns/op不等于逐请求P95/P99，更不是实际TPM。

## 浏览器与边界
真实专属浏览器核对零账号本地settings保存、刷新、配置文件和进程重启后的UI/API；容量50000、预算128MiB、等待16、上传45秒、写20秒、采样25%、连接7/3，OpenDesign根仍512/32且quota=true。本地测试服务/管理员/浏览器均结束。回执及假data仅存登记的临时目录；业务结论永久存本任务validation.md与docs/mistral-capacity.md第8节。

全仓回执来自当时主工作树，保留其他窗口未提交注册/Dockerfile工作但不将其纳入本次提交；仅当前提交的隔离检出构建/定向测试另记会话日志。未推送/部署，300M TPM认证与冷选号/密集统计/大快照持续压测仍待授权和工作负载/SLO/上游限额/费用确认。
