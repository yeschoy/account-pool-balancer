# 当前实施与本地验收（2026-11-09）

## 已落地
- 独立会话LRU（0沿用共享4096，1–1000000独立）、设置/概览/持久化往返；在途lease、跨池/Go cache优先级及恢复/source/TTL保留。
- 不可变账号/组/Key/source索引、owner-only、健康聚合缓存、轻量候选、分组等待/唤醒；可变Update/Normalize阶段废弃旧索引；并发provisional的实际发送者可确认偏好。
- 原始/改写/响应/SSE原事件字节预算、读/逐次写deadline、取消归还/读goroutine退出、响应超限不重放/不扣健康；独立Transport及安全换代；JSON/SSE/脱敏/采样/稀疏Stats/日志队列字节预算。

## 质量门禁
业务代码已提交 `1c0a410`；提交后仅在干净隔离检出该commit，构建及config/pool/logstore/stats/quota/server定向回归（server9.542秒）、全部Node/语法再次通过，见 `research/implementation/clean-commit-tests.txt`。该树干净，未带主树其他窗口注册/Dockerfile等未提交修改。

全仓 `go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、构建及所有Node行为套件/JS语法通过。原始回执和改后基准在本目录 `research/implementation/`；临时构建/假data/浏览器回执在登记的 `.analysis/mistral-capacity-implementation/`。专项覆盖上传/下游慢读真实socket、并发取消归零、队列满/组隔离、账号换组后的释放唤醒/旧Revision防回滚、响应预算、不误重放/不扣健康、LRU迁移/缩容/恢复、Config mutation索引及24h稀疏投影。

## 本地真实浏览器
- 仅127.0.0.1:18793、0账号、独立假data；无真实Key、无上游推理。
- 通过实际设置页保存容量50000、字节128MiB、等待16、上传45秒、写20秒、采样25%、独立连接7/3及本池quota=false。API、UI、配置文件读回一致，正常停止并重启同一data后再次核验全部值。
- OpenDesign根连接仍512/32、根quota=true。概览显示独立0/50000和0/128MiB及日志队列/丢弃；接口实际limit=50000、independent=true，不只是点击成功。
- 管理员已退出，专属mistral-capacity-local浏览器关闭，所属PID47284/55182停止；文件待同意清理，Trellis永久保留。

## 改后测量边界
详见 `docs/mistral-capacity.md` 第8节。4K账号健康开启已确认绑定lease周期836.2ns、1064B；冷选号1.737ms。400账号/详情关闭Mock Handler摊销163174ns、559790B、逐请求P95/P99=2.101/2.718ms。以上为Apple M4本地短测、非生产容量，未包含前置NewAPI/Nginx/真实生成/周期元数据写盘；不能宣称3亿TPM或相同倍数端到端提升。

## 当前仍未完成
未推送/部署/改线上设置/真实推理；未验证300M TPM、真实组织/模型额度、完整NewAPI/Nginx路径、持续开环长流/周期保存及目标SLO。冷选号全量过滤排序、无显式身份fallback、密集24h桶和百万绑定快照仍需按真实负载进一步测量。没有独立agent审阅证据：工具全部启动失败后由用户批准主会话串行。任务保持in_progress，不归档为容量认证完成。

---

# 首轮研究验证（历史快照）

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
