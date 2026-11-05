# 验证结果

## 自动化（仅本轮相关范围）
- gofmt：本轮Go文件。
- `node --check internal/server/web/app.js`、`node --check internal/server/web/tokenharbor.js`：通过。
- `node internal/server/tokenharbor_web_test.js`：通过。覆盖默认分类/归档、切换清空选择、批量期间禁止切换、取消退回零请求、退回定位原任务；保留日志单链/增量去重及既有批量回归。
- `go test -race ./internal/server -run '^TestTH' -count=1`：通过，约5.2秒。新增退回的认证/CSRF、凭据/出口/套餐观察持久化、来源/Key/状态/歧义拒绝、双文件局部失败可重试、网络未持提交锁、晚到入池结果不能撤销退回。
- `go vet ./internal/server`、`go build -o .analysis/tokenharbor-registration-return/odb ./cmd/odb`、`git diff --check`：通过。
- 未跑无关全仓测试；没有真实推理、注册、验证码或付款验收。

## 本地真实浏览器 + 假数据
独立会话 th-return-mock，全上游指向本机Mock：
1. 初始待处理6 / 已入池0，默认展开任务。
2. 检测所选套餐并入池：后端账号1，默认待处理5、已入池1，已入池账号不再出现在默认表格。
3. 切换已入池：只有paid@mock.test，选中项清零，有“退回注册机”入口。
4. 号池真实操作菜单有“退回注册机”；确认后自动返回原任务待处理页。
5. 后端GET账号数0，待处理6 / 已入池0，原记录registered、hasSession/keyMasked/verified均保留。
6. 发现悬浮日志z-index60覆盖菜单50，补充仅TokenHarbor的菜单层级70。浏览器以等价CSSOM层级70验证可点击；最终源码样式加入同一作用域规则并编译通过。没有把因CSP被拦的临时style标签当作成功验证。

## 官方研究与只读生产元数据
见research.md。两把Key各一次API目录读取，均77项；官方pricing给出29组模型的Pass分层。未读取真实密码/会话到本地，不改变在线配置或服务。研究结论不等于实际模型推理权限/额度验收。

## 交付边界
代码及Trellis记录一起本地提交；未推送、未部署。临时产物仅登记、停止所属进程/关闭所属浏览器，未经用户授权不删除；远程诊断容器保留为已退出状态。
