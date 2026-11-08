# 执行与进度

1. [x] 核对新工作区、8d41516祖先、诊断证据、共享工作流与业务规范；无代理。
2. [x] 更新Mistral规范，实现精确参数/消息拼接及本地拒绝。
3. [x] 定向mock与已有Mistral回归，gofmt/diff check/build，独立自审。
4. [x] 功能分支提交96eaad7；合入origin/master并发更新为9f375ef，正常原子推送功能分支/master，不reset/强推。
5. [x] 一次预检→干净快照构建→一致备份切换→一次验收；9f375ef上线、公网通过即反馈。
6. [ ] **等待范围确认**：最新只读发现渠道180还含两种GLM模型，不能全渠道移除规则。尚未写DB或新建渠道备份，原规则/header通配/路由/Key保留。下一步需确认并核实运行版本支持仅DeepSeek Chat条件停用对应规则；自然流量初步观察已完成，见validation.md。
7. [x] 本轮规范/验收/部署记录随代码提交；只改记录不重启。临时产物仍保留待同意清理，Trellis和正式备份保留。

验证命令：`go test ./internal/server -run Mistral -count=1`；`go build ./cmd/odb`；`git diff --check`。不做全仓/race/vet或主动生产推理。
