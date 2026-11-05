# 执行与验证

1. 扩展 Store.SetProxy 状态/严格校验/观察失效；API 保留认证及安全投影。
2. 号池观测解耦注册代理，补充新可编辑状态相关的迟到结果与支付预留检查；号池更新输入采用同一代理校验。
3. 注册机所有空闲行显示代理入口，号池菜单新增入口；共享弹窗明确作用域和直连选择，通用编辑说明独立语义。
4. 添加 Store、服务器/路由/来源、入池继承与重入不覆盖、JS 行为回归。
5. node internal/server/tokenharbor_web_test.js；node --check 两个业务 JS；go test ./...；go test -race ./...；go vet ./...；go build -o .analysis/tokenharbor-account-proxy/odb ./cmd/odb。
6. 隔离纯本地 Mock 浏览器验证两个入口、更换/取消/清除、导入默认、互不覆盖与错误提示；验证后停止本次进程，临时文件登记并保留待授权清理。
7. 更新 docs/tokenharbor.md 和后端 spec，记录 validation 与 workspace 日志；仅 stage 本任务文件，提交功能和 Trellis。无推送/部署。
