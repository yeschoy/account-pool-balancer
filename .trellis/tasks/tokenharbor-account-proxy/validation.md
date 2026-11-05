# 验证结果

## 需求完成
- 用户最终确认采用独立设置，而不是最初建议的同步。注册机和号池各有逐账号入口；入池新建时继承注册记录代理，重复入池不覆盖号池独立设置。
- 注册库更换/清除仅影响该记录，清空旧套餐观察，不损失原密码、Cookie、Key、订阅链接和生命周期；queued/running拒绝。
- 号池更换/明确清除只改配置，清理旧来源证明/续传，使用既有后台刷新；不自动启用账号。号池观测仍依赖唯一原记录会话，但按号池自己的代理联网，不要求两边代理相等。
- 补验证写回和支付预留增加来源检查，避免可编辑代理后的过期操作。无新增持久化结构、依赖或构建系统。

## 自动验证
以下全部通过：
- `node --check internal/server/web/app.js`
- `node --check internal/server/web/tokenharbor.js`
- `node internal/server/tokenharbor_web_test.js`
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `go build -o .analysis/tokenharbor-account-proxy/odb ./cmd/odb`
- `git diff --check`、`git diff --cached --check`

新增测试：`internal/thsignupstore/proxy_update_test.go`、`internal/server/tokenharbor_proxy_update_test.go`；扩展 `internal/server/tokenharbor_web_test.js`。覆盖各状态、无效输入、加密重开、认证/CSRF/no-store、双向独立替换/清除、保留敏感资料、入池代理/明确直连继承、重复入池不覆盖、两个HTTP Mock代理的读请求路线、旧来源/迟到结果丢弃、代理失败不借注册代理/直连、前端取消和空输入保护。

工作区中途出现另一窗口的登录会话恢复改动（thregister/auth、SummaryOf、注册恢复分支等）。没有覆盖或纳入本次提交：只暂存本功能hunk，并导出 `.analysis/tokenharbor-account-proxy/staged/` **从暂存区独立重跑全部上述Node、Go普通/race/vet/build检查，通过**。因此本次功能不依赖另一窗口尚未提交的实现。完整普通/race中server分别约91/104秒（两组并行验证），不是生产服务时延/停机指标。

## 真实本地浏览器
独立会话 `th-account-proxy-mock`，服务/官网/两个代理全部绑定127.0.0.1，数据位于已忽略目录，仅假凭据。

1. 登录后进入TokenHarbor注册机；已注册账号“改代理”弹窗显示作用域，输入为空保存被拒，无效socks4被后端拒绝且不回显认证。
2. 将ready记录从本地代理64349改到带假认证的64350；API摘要只见无认证标签，原已入池账号仍64349。该记录套餐清为空；显式检测后恢复active，再点击实际“校验并入池”，新号池账号沿用64350。
3. 从号池账号真实“操作 → 修改代理”更换admitted账号为64350，原注册记录仍64349；无认证信息出现在账号表格。
4. 从注册机“已入池”页清除admitted原记录代理，号池仍64350。随后从号池菜单刷新模型/套餐/周用量，显示Agent Pass、覆盖1/目录1，证明独立代理不再被原相等条件误停。
5. 号池明确清除为直连后，原注册直连保持、ready账号及其原记录仍64350；Key-only账号弹窗取消不保存、不启用。
6. 注册表格存在横向滚动，操作列可能被裁剪；先 `scrollintoview` 再新快照/实际点击，并用API后置状态确认入池。不能把工具click成功当作业务已执行。浮动日志先显式收起，未修改页面业务代码绕过点击。

验收后浏览器关闭、Mock进程71129停止，64348/64349/64350/64351无监听。临时目录/三个快照/一张截图已登记 `_tmp_artifacts.md`，保留待用户同意再清理；Trellis记录长期保留。

## 边界
未使用真实代理/账号注册/验证码/支付/推理，未推送或部署，未更改在线配置。仅验证Mock代理路线及本地业务，不宣称上游接受任意换IP后的原登录会话；若官网拒绝会话仍未知停调度。
