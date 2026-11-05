# 验证与交付

## 自动化
- gofmt：通过。
- `go test ./...`：通过（最终修改后重跑）。
- `go test -race ./...`：通过（最终修改后重跑）。
- `go vet ./...`：通过。
- `go build -o .tmp-tokenharbor/odb ./cmd/odb`：通过。
- `node --check internal/server/web/tokenharbor.js`、`git diff --check`：通过。
- 新增测试覆盖：注册会话断点、创建 Key 后回调失败仍返回部分凭据、Resume 不 signup/不重复建 Key、原注册时间邮件筛选、验证 HTTP 500 不算成功、Cookie/Checkout 加密与重启/重试保留、深拷贝、Agent Free 单次报价/固定价格/禁升级与自动续费、错误/重定向/未知结果禁止重放、只读摘要不带链接/原始凭据、两段和三段输入。

## 本地真实浏览器（仅 mock）
独立 th-payment-mock 浏览器、127.0.0.1:18894 应用和18895假上游，无真实业务凭据。完成登录 → Token Harbor 注册机 → 单个假账号 → Agent Pass 弹窗 → 获取并显示假 Stripe 链接。后台断言 hasSession/hasCheckout 均 true、读取的保存链接匹配、轮询没有支付 URL 或原始 Key、两段接码页解析正确。未打开假 Stripe 链接。测试浏览器及两个 mock 进程已关闭，文件待用户授权清理。

## 真实业务（仅一个账号）
- 仅使用用户给出的第一个邮箱，其余四个未注册。注册脚本已限制仅索引0，不做自动重试/出口轮换。
- 同一 socks5h 代理，本地无法连接其 TCP 端口，通过 OVH SSH 转发到同一代理后成功；不改变原代理或出口。自建转发已停止。
- API Key 在创建后立即加密保存；邮件接码页经代理读取 EOF，明确改为独立直连后取得原注册邮件并完成验证，避免把读取失败误报为邮件未到。
- 官网 Pricing 正常 Choose Agent，Auto-renew 未选，实际 Stripe 页面明确 Agent Pass — Monthly (one-time)，US$0.99。只获取链接，未输入付款资料/点击支付，没有验证码服务消费或推理请求。程序自动报价保护分支以 mock 验证，不为测试重复生成真实订单。
- 登录态由浏览器 state save 保存，工具确认文件存在；最终会话/Key/链接/入池ID记录在加密 signup store，本地敏感目录0700、文件0600，Git/Docker忽略。

## OVH（增量数据操作，不是部署）
- 现有管理员 API 登录/CSRF后仅 POST /api/accounts 一次，完成后注销临时管理会话；没有新增导入密钥、覆盖配置、重建或重启。
- 新账号 ID：`acc_d660f07c805eef001a7d0222`，provider/group=tokenharbor，邮箱已验证。尚未付款，enabled=false；未擅自启用调度。
- 原60账号逐项一致，其他设置完整保留，配置revision=5143，healthz正常。
- 备份与业务凭据：`/home/ubuntu/backups/tokenharbor-accounts/20261105-agent-pass/`，受限保存加密记录、密钥、支付链接、导入数据/回执、导入前config。不是临时垃圾，不清理。
- 新注册机代码尚未部署或推送；账号入池不依赖该代码上线。

临时产物清单：工作区 `_tmp_artifacts.md`（已忽略Git）。未经用户同意未删除文件；真实业务会话/密钥/链接及 Trellis 记录永久保留。
