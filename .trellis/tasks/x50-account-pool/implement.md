# 实施与检查

1. [x] 读取worktree/继承AGENTS、before-dev、provider/backend/deploy/frontend指南，核对基线与Deploy日志实现。
2. [x] 配置/独立规则/凭据隔离与原生推理接线。
3. [x] quota刷新、来源安全、metadata与池投影。
4. [x] 注册批量入池与代理批量重置API。
5. [x] 通用管理UI、注册选择/隐藏、日志悬浮。
6. [x] config/quota/pool/rules/server/store/Node回归。
7. [x] 本地真实浏览器一次性mock全链路核对API/磁盘。
8. [x] gofmt; go test ./...; go test -race ./...; go vet ./...; go build ./cmd/odb; 15 Node tests及check。
9. [x] trellis-check、文档/日志；本分支提交（不部署推送），hash见git log。
10. [x] 后续用户授权合入远端master并推送2986f74、OVH部署健康/双内网/公网资源与数据保留验收；原master脏工作区未强行更新。
11. [ ] 本轮真实50X鉴权billing/生产推理业务验收：未执行，不冒称完成。

状态 in_progress；用户已明确授权直接实施，无流程许可阻塞。单人实现，不启动agent。
