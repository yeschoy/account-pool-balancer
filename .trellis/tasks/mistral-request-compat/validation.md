# 验收记录

## 本地实现
- `go test ./internal/server -run Mistral -count=1`：通过，2.739s；包含现有native/SSE/流式flush/错误重放保护与新参数定向回归。
- 自审移除无用字段/明确思考变量后，仅重新运行6组新增参数测试：通过，0.584s；`go build ./cmd/odb`、gofmt及`git diff --check`通过。
- 初次测试编译发现测试fixture误用了不存在的Config.BaseURL，已改为既有Upstream.RuntimeBase并补其他池账号，未涉及线上。
- 新覆盖：两种思考开关/显式档位/默认/冲突/预算；developer仅role字节替换（保留顺序、重复未知字段、工具和大数）；连续多字段删除首中尾、seed大整数与冲突、metadata/cache值；39组本地拒绝零尝试；其他模型/协议与其他供应商字节相等；上游422不删seed/不换号重放。
- 自审确认strict JSON在前、身份原值读取在前、账号/RPM在后；没有新增调度/TTL/cache hint优先级、生产推理、账号策略或UI修改。
- 仅公开官方Chat schema复核random_seed，不把mock或泛Mistral schema当作特定DeepSeek型号实测证据。该型号random_seed实际支持/确定性尚未知。

## 集成/上线
fetch发现origin/master=e06a589，新增诊断文档和独立tools/mistral工具；相对当前基线无internal/cmd/go.mod/go.sum/Dockerfile应用差异。原master工作树干净，仅只读核对，保留并发工具更新，不执行注册工具。

- 功能提交96eaad7；合入origin/master形成9f375efee5a89e92dcbe5f14dd0385e4519c1501后，非强制原子推送master与功能分支。
- 05:59:51.182–06:00:00.435 UTC构建9.25秒；旧进程06:00:00.640退出0；完整一致备份06:00:00.921–04.312；新容器06:00:04.517启动，06:00:05.643首HTTP可达、06:00:34.883 Docker healthy，本机06:00:34.988通过。
- 公网真实未登录浏览器06:01:15.204 UTC登录页/healthz/四资源200，资源保留?v并SHA256与源码逐项匹配；healthy/零重启/无OOM，161账号、186注册记录关键资产、routingSecret、完整config及运行参数保留，通过即已反馈。
- 正式备份`/home/ubuntu/backups/account-pool-balancer/20261108T055905Z-mistral-params-9f375ef/`；完整data SHA256 `1ade70b278d8a756293d09aac6cb87bba9fee93513492612d61f980dcaa60719`，rollback镜像保留8d41516。不恢复旧data覆盖新写入。详细部署记录见docs/ovh-deployment.md。

## NewAPI收敛边界（待用户确认）
上线后最新只读确认渠道180同时包含DeepSeek两个别名及glm-5.3/glm-5.3-flash，对应两个zai模型不在本轮兼容范围。不能直接移除全渠道10项operations而让其他模型/路径回归。**本轮没有任何NewAPI DB/配置写入**，原header通配/路由/模型映射/Key/历史正式备份均保留。

建议仅目标DeepSeek Chat停用对应操作，GLM及其他路径继续原规则；本地NewAPI代码有条件规则，但线上ad556799精确源码对象不可用，尚未验证运行版本能可靠按模型+路径条件匹配，不能把本地静态代码当作线上合同。在确认方案前不盲加条件或全量清空规则。现有规则仍删除metadata/cache key并绕过store=true保护，尚未解除。

## 自然流量（初步有界窗口，不是完整10分钟）
06:00:34.988–06:04:33.034 UTC，约3分58秒，真实DeepSeek完成24/24 success/HTTP200，全为流式；NewAPI渠道180对应24条type=2，stream_status=ok/end_reason=done。Mistral错误事件0，无新增422或本地400，无忙溢出、请求内切号或多attempt；两服务结束时healthy、零重启、无OOM。

真实input3195540/cached2119808，token加权66.34%；绑定16hit/8miss，24次均message_hmac身份与session_hmac cache hint。重启会清内存绑定，miss不能当作同会话一直切号。未恢复NewAPI删掉的客户端metadata/cache，因此不声称客户端原值已端到端恢复，也不把该窗口和先前85.85%当受控对比或承诺缓存提升。无主动生产推理；自然样本没有原始请求体证据，不能声称所有新参数形态、seed或store=true均已线上实测。
