# 执行与进度

1. [x] 核对新工作区、8d41516祖先、诊断证据、共享工作流与业务规范；无代理。
2. [x] 更新Mistral规范，实现精确参数/消息拼接及本地拒绝。
3. [x] 定向mock与已有Mistral回归，gofmt/diff check/build，独立自审。
4. [x] 功能分支提交96eaad7；合入origin/master并发更新为9f375ef，正常原子推送功能分支/master，不reset/强推。
5. [x] 一次预检→干净快照构建→一致备份切换→一次验收；9f375ef上线、公网通过即反馈。
6. [ ] **等待范围确认**：最新只读发现渠道180还含两种GLM模型，不能全渠道移除规则。尚未写DB或新建渠道备份，原规则/header通配/路由/Key保留。下一步需确认并核实运行版本支持仅DeepSeek Chat条件停用对应规则；自然流量初步观察已完成，见validation.md。
7. [x] 本轮规范/验收/部署记录随代码提交；只改记录不重启。临时产物仍保留待同意清理，Trellis和正式备份保留。

验证命令：`go test ./internal/server -run Mistral -count=1`；`go build ./cmd/odb`；`git diff --check`。不做全仓/race/vet或主动生产推理。

## 用户追加：再观察10分钟，若Mistral错误由Balancer造成则修复

- 06:48 UTC预查最近10分钟：84完成/78成功/6失败（5次上游503、1次422），因此先返回诊断，没有把未完成观察冒充完整10分钟。后续又出现503/422。
- 503已在现有日志确认上游JSON/internal_server_error/Service unavailable，原有两账号尝试，不通过修改账号策略或无关重启掩盖。
- 422响应前缀恰64KiB，日志通用reason无法继续归因；当前截断未显式标记。先修复此Balancer诊断缺口：有界解析已有前缀的allowlisted loc/type与真实截断标志，不扩大请求兼容、不启用详情日志、不保留原始正文。
- [x] 定向mock验证截断/嵌套JSON/隐私/零不确定重放、gofmt/diff check/build；修复7b652c9，集成并发master为65f2680后Mistral回归2.969s通过。
- [ ] **部署协调阻塞**：固定流程在运行revision已变时安全中止，未构建/停服/切换。另一个操作换入无revision的latest镜像（缺wget导致health unhealthy、healthz200），后又恢复原9f375ef健康镜像；不能盲目覆盖并发发布。暂停其他发布并统一可追溯基线后再部署诊断补丁。
- [ ] 稳定目标版本完整10分钟验收仍待补丁上线。本轮已复核06:48:29–06:58:29完整10分钟日志：170完成/143成功/26失败/1取消，13上游503+13未知422，跨并发切换，不冒充单版本验收。详见diagnostic-20261108.md；原NewAPI模型范围门不变。
