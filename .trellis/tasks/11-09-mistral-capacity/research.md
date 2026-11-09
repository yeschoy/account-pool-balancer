# 研究证据与复现

最终审计结论以 [`docs/mistral-capacity.md`](../../../docs/mistral-capacity.md) 为准。本目录只存可复核夹具/原始输出/隔离实验，不把设计冒充正式功能。

## 范围与状态

- 源码HEAD `7c5e30b`。初始工作树另有注册机/Dockerfile未提交修改，未覆盖或纳入本任务提交。
- Apple M4 / darwin arm64 / Go1.26.8；本地假账号和合成正文，只有Handler基准访问本地HTTP Mock。未使用生产Key、未发真实模型推理。
- `research/pool_bench.go.txt` 和 `research/server_bench.go.txt` 经Go overlay加载为各包临时测试文件，不参加正常生产编译。
- `research/owner-only-prototype.patch` **只应用在`.analysis/`源码副本**；生产`internal/pool/pool.go`未改。它验证收益方向，不是已经准备好上线的实现。
- 临时overlay/profile/二进制在`.analysis/mistral-capacity-20261109/`，登记于`_tmp_artifacts.md`，未经同意不清理。Trellis证据保留。

## 基线命令

从仓库根目录执行。overlay映射的绝对路径可按当前checkout重新生成，不能直接照搬另一机器的路径。

```bash
OUT=.analysis/mistral-capacity-20261109
python3 - <<'PY'
import json
from pathlib import Path
root = Path.cwd()
out = root / '.analysis/mistral-capacity-20261109'
out.mkdir(parents=True, exist_ok=True)
research = root / '.trellis/tasks/11-09-mistral-capacity/research'
replacements = {
    str(root / 'internal/pool/zz_capacity_research_test.go'): str(research / 'pool_bench.go.txt'),
    str(root / 'internal/server/zz_capacity_research_test.go'): str(research / 'server_bench.go.txt'),
}
(out / 'overlay.json').write_text(json.dumps({'Replace': replacements}, indent=2))
PY

go test -overlay "$OUT/overlay.json" ./internal/pool -run '^$' \
  -bench '^BenchmarkCapacityResearchPool$' -benchmem -benchtime=200ms -cpu=1,8 -count=1

go test -overlay "$OUT/overlay.json" ./internal/server -run '^$' \
  -bench '^BenchmarkCapacityResearch(Prepare|SSE|Redact)$' -benchmem -benchtime=200ms -cpu=1 -count=1

go test -overlay "$OUT/overlay.json" ./internal/server -run '^$' \
  -bench '^BenchmarkCapacityResearchHandler$' -benchmem -benchtime=200x -cpu=8 -count=1
```

原始输出：`research/pool-baseline.txt`、`research/components-baseline.txt`、`research/handler-baseline.txt`。

Pool夹具：40/400/4000同组Mistral账号，所有账号有新鲜目录/预算（poller.View），smart；health=true时每账号已填有效健康数组，confirmed sticky hit绑定到中间位置账号；每轮Acquire/MarkSent/Release。hit=false是**无affinity的冷选号**，不是每轮创建新会话，未测长流/满载等待。8CPU时8worker，不能冒充几千并发实测。

Prepare夹具：约128/512/2048KiB真实合成正文、128条字符串历史、真实会话头，含/不含system。覆盖生产调用顺序中的路由扫描、别名检查、身份、准备、X-Affinity；不含完整Handler、日志及上游。

Handler夹具：约134KiB正文、每worker固定假会话、16个SSE内容事件+usage+DONE；真实本地HTTP上游；包含Handler、统计和日志，不包含前端TCP/TLS、Nginx/NewAPI、真实生成和周期metadata循环。`ns/op`是8并行摊销时间，P95/P99才是逐请求观测；200样本是短基准不是SLA，合成usage不可换算为真实TPM。

Redact夹具：17KiB安全文本，40/400/4000假Key；比较稳定Config provider view与每次ForProvider新视图指针。缓存由指针标识，反复新视图导致重建；不能据此关闭脱敏。

## CPU、内存和锁剖析

```bash
go test -overlay "$OUT/overlay.json" ./internal/pool -run '^$' \
  -bench '^BenchmarkCapacityResearchPool/accounts=4000/health=true/hit=true$' \
  -benchtime=3s -cpu=8 -count=1 \
  -cpuprofile "$OUT/pool.cpu" -memprofile "$OUT/pool.mem" \
  -mutexprofile "$OUT/pool.mutex" -o "$OUT/pool.test"
go tool pprof -top "$OUT/pool.test" "$OUT/pool.cpu"
go tool pprof -top -alloc_space "$OUT/pool.test" "$OUT/pool.mem"
go tool pprof -top "$OUT/pool.test" "$OUT/pool.mutex"

go test -overlay "$OUT/overlay.json" ./internal/server -run '^$' \
  -bench '^BenchmarkCapacityResearchPrepare/KB=512/system=true$' \
  -benchtime=3s -cpu=1 -count=1 \
  -cpuprofile "$OUT/prepare.cpu" -memprofile "$OUT/prepare.mem" -o "$OUT/server.test"
go tool pprof -top "$OUT/server.test" "$OUT/prepare.cpu"
```

文本证据：`research/pool-cpu-top.txt`、`research/pool-alloc-top.txt`、`research/pool-mutex-top.txt`、`research/prepare-cpu-top.txt`。profile有采样/运行开销，不能混用其ns/op与无profile主表。alloc_space含夹具setup与校准，不是常驻内存；锁延迟是多个goroutine累计，不能当墙钟或停机时间。

## 隔离owner-only实验

复制生产`pool.go`到临时目录，在副本应用`owner-only-prototype.patch`；该证据为zero-context diff，应用工具需支持`--unidiff-zero`，且只能修改临时副本。overlay-fast额外把`internal/pool/pool.go`指向此副本。原主文件始终未改。

```bash
# overlay-fast.json含：pool.go -> 临时副本，zz_capacity_research_test.go -> pool夹具
# 切勿在生产源码上直接应用实验patch。
go test -overlay "$OUT/overlay-fast.json" ./internal/pool -run '^$' \
  -bench '^BenchmarkCapacityResearchPool/accounts=(40|400|4000)/health=true/hit=true$' \
  -benchtime=200ms -cpu=1,8 -count=1
go test -overlay "$OUT/overlay-fast.json" ./internal/pool
go test -race -overlay "$OUT/overlay-fast.json" ./internal/pool
```

结果 `research/pool-owner-only.txt`；Pool normal0.527s、race1.695s通过。它仍有线性Account查找、未覆盖新容量功能、没有做完整server/端到端回归，且Candidates诊断变为1；不得仅凭已有用例通过就上线。

## 官方证据

公开文档由agent_browser read原文核对：

- https://docs.mistral.ai/admin/billing-usage/usage-limits ：Organization、per-model TPM/RPS、提额条件。
- https://docs.mistral.ai/inference/conversations/advanced/prompt-caching ：含缓存的prompt计数、64-token块、10%缓存输入价、命中不保证；没有明确缓存不计TPM的承诺。

未访问任何账号Admin Limits页面，未假设同组织多Key可加总。公开网页工具溢出已登记临时文档，不提交工具缓存或凭据。
