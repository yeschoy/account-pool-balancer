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
- [ ] 定向mock验证截断/嵌套JSON/隐私/零不确定重放、gofmt/diff check/build。
- [ ] 复用固定流程提交部署诊断修复，保留配置/账号/备份；自然新错误取得证据后再定向处理，不猜测422原因。
- [ ] 在稳定目标版本上重新完整观察10分钟并如实记录错误归因；原NewAPI模型范围门不变。
