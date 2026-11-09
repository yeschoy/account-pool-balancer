# 实施计划（待确认后启动）

## 已完成研究

- [x] 阅读Mistral/Quota/Pool/JSON/SSE/详情/统计/日志/持久化/设置链路，参考NewAPI源码及官方限制文档。
- [x] 账号40/400/4000、命中/冷选择、health开关、1/8CPU本地基准。
- [x] 128/512/2048KiB历史、无system、SSE、脱敏Config视图、Handler详情开关基准与CPU/alloc/锁剖析。
- [x] owner-only隔离overlay验证收益，Pool normal/race通过；生产代码未改。
- [x] 将证据、设计、验收门禁和未验证项落盘；不宣称3亿TPM已通过。

## 开发前门禁

- [ ] 用户确认会话容量语义（本池总容量/全局/每账号）、默认/边界与缩容行为。
- [ ] 确认3亿TPM统计口径、典型输入/输出/持续时间、峰值/持续窗口；允许先按场景矩阵开发资源保护。
- [ ] 审阅prd/design/本计划后开始正式实现。当前task.json保留planning，不归档。

## 顺序

1. 不可变配置索引/source hash缓存；owner-only sticky命中、轻量候选、健康滚动汇总；先证明原有busy/overflow、分组/Key/source失效、严格续传兼容。
2. 输入/响应/Event总字节预算、上传/逐次写deadline、有界等待；取消/未发/已发未知/RPM预约归还测试。
3. 用户确认的绑定容量字段：Config校验与保存→settings API→运行时LRU/计数→metadata导入导出→概览/账号→UI保存/刷新重载。
4. 稳定脱敏缓存、可选详情采样/字节预算、必要分阶段指标；不默认关闭用户现有详情。
5. Stats去重复/按需分钟桶与导出降锁、JSON共享扫描/延迟fallback、常规SSE解析降分配；以回归和差分协议测试为门禁。
6. 连接设置作用域/UI/Transport缓存换代及重载；保持固定代理不回退和现有长连接/隐私边界。
7. 扩大本地开环/持流/故障压测，记录至少多个metadata保存周期；逐层生产只读预检，实际推理测试须有成本/次数硬上限。

## 必要测试

- 绑定：省略/非法/极限配置、隔离/动态缩容、LRU/过期/删除/未发撤销/发送确认、换Key/Base/代理/下游Key、重启和崩溃窗口；若每号限制，预约/计数同步。
- 调度：命中/未命中、owner禁用/不在组/目录失效/低健康/并发RPM满、reject/overflow/wait-overflow、取消、严格owner缺失fail-closed；其他池不变。
- 资源：已知/chunked/慢上传、预算满、2KiB/2MiB/近上限正文、适配额外分配、非流式大响应、合法大SSE事件、慢读下游、满载等待风暴、峰值后释放和队列字节限额。
- 协议：大数、重复/转义键、原字节/消息排列、工具/图片/思考、multiline SSE、Responses首帧终态/EOF和不重放；统计/缓存率reset语义保持。
- UI：真实本地假数据操作设置保存→读回→SIGHUP/重启，跨池隔离及并发revision冲突；Node行为/语法测试。

## 验证命令（正式功能实现后）

```bash
gofmt -w <本次修改Go文件>
go test ./...
go test -race ./...
go vet ./...
go build -o /dev/null ./cmd/odb
node internal/server/mistral_web_test.js
# 加运行实际受影响的Node设置/缓存/日志行为测试及JS语法检查
```

研究仅修改文档/非编译夹具，不声称已执行上述正式实现全门禁；实际执行以validation.md为准。

## 发布与回退

不提交/覆盖初始其他未完成修改。Trellis/研究提交Git；临时profile/二进制/overlay按_tmp_artifacts登记、用户同意后才清理。部署另按既有OVH约定，不在旧工作树裸build；保留一致备份、旧镜像、healthcheck与双内网别名。新增存储字段先明确旧版读取兼容，不能靠恢复旧data抹掉新业务写入。
