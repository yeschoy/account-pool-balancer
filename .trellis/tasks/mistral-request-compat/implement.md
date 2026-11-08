# 执行与进度

1. [x] 核对新工作区、8d41516祖先、诊断证据、共享工作流与业务规范；无代理。
2. [x] 更新Mistral规范，实现精确参数/消息拼接及本地拒绝。
3. [x] 定向mock与已有Mistral回归，gofmt/diff check/build，独立自审。
4. [ ] 功能分支提交；fetch核对master并发变化后正常合并/推送，不reset/强推。
5. [ ] 一次预检→干净快照构建→一致备份切换→一次验收；通过即反馈。
6. [ ] 渠道180最新读取/备份/CAS式收敛对应operations；自然流量观察422分类与缓存/绑定，记录未知项。
7. [ ] 提交规范/验收/部署记录；不重启文档版本；临时产物征求同意再清理。

验证命令：`go test ./internal/server -run Mistral -count=1`；`go build ./cmd/odb`；`git diff --check`。不做全仓/race/vet或主动生产推理。
