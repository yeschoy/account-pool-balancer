# Token Harbor Agent Pass session

- 工作区：`tokenharbor-payment`，从最新 `origin/master` `fe5fd94` 创建；远端无 main，没有强制改动本地分叉主分支。
- 业务提交：`ea012a7`。用户明确只处理第一个账号，只取 Pricing 的 Agent Pass 月度单次链接；其余四个未注册。
- 实现：两段邮箱输入；会话/Key逐阶段加密保存；恢复不重新注册/创建已有Key；邮件时间窗口不前移；免费模型数据授权默认关闭；Agent Pass 报价/托管链接入口、不续费/不付款/不确认升级/不重复未知结果；.private 不进 Docker。
- 质量门通过：全仓 Go tests/race/vet/build、JS syntax、diff check；本地独立浏览器 mock 全流程及 backend 后置断言通过。
- 真实官网 Stripe 收银台已确认 Agent Pass Monthly (one-time) US$0.99，链接先行告知用户；未付款或推理。会话/Key/链接已保存。
- 唯一账号经管理员 API 加入 OVH Token Harbor 池（`acc_d660f07c805eef001a7d0222`），待付款 disabled；原60账号/其他设置不变、healthz正常，无重启。受限业务备份见任务 validation.md。新代码未推送/部署。
- 本轮自建 SSH 转发、两个 mock 进程及 mock 浏览器已停止；真实支付浏览器/业务会话保留。临时文件未删除，已登记 `_tmp_artifacts.md` 待用户同意；业务凭据及 Trellis 保留。
- 当前任务已 completed；仓库缺少 Trellis scripts，采用既有 Markdown/JSON 记录方式，没有额外搭建脚本系统。
