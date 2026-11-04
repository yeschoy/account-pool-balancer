# 验收记录（2026-11-05）

## 结果

- `go test ./...`：全仓通过。
- `go test -race ./...`：全仓通过，无数据竞争报告。
- `go vet ./...`：通过。
- `go build -o .tmp-tokenharbor/odb ./cmd/odb`：通过。
- `node --check internal/server/web/app.js` / `perch.js`：通过。
- `git diff --check`：通过。

中途曾因复用 Modal 测试入口时改写了原有报错文案，触发 `TestModalTestUsesCatalogueModel`；已按 provider 分支保留原 Modal 文案，并完整重跑测试/race 通过，未修改或删除旧测试。

## 新增测试覆盖

- Config：thk_ 推断、独立默认分组/base/routing、旧配置、混池拒绝、URL/规则/路由校验。
- Quota：仅 models、刷新间隔/关闭自动刷新、失败保留目录/时间、403 不误判吊销、401/恢复、源变更晚写拒绝、拒绝重定向。
- Server：三原生协议请求字节、认证/会话/缓存/Anthropic beta/显式消费头；三 SSE 原样；无工具剥离；会话/Claude metadata/cache-key 亲和及来源优先级。
- 同一绑定忙碌不溢出；Responses 续传同号、跨 key/未知状态拒绝、持久化恢复、凭证变更失效；服务端续传失败不换号。
- 401/402/429/503 换号，400/404/413/422 不重试；完整两小时 Retry-After；大额确认先于自定义 402 冷却规则硬停止且保留提示。
- 不跟随推理重定向，不重放不确定已发送请求/已提交 SSE。
- 缓存读取/写入及 Messages 分母、未知 usage=null、请求/错误日志投影、上游 key 脱敏。
- 设置/规则/导入的池隔离、注册 secret 不输出/不覆盖、上游 key 只写、models 列表。

## 真实本地浏览器

使用 `.tmp-tokenharbor/` 假数据，API mock `127.0.0.1:18891`，服务 `127.0.0.1:18890`。无真实服务凭证或付费请求。

1. 登录 → Token Harbor 标签 → 独立设置保存本地 base/刷新间隔，后台读回 sticky=true / reject。
2. 手动添加及重复行批量导入后，后台确认恰好 2 个 Token Harbor 账号，均在独立分组，列表 key 已掩码。
3. UI 创建本池下游 key，仅在浏览器内取该假环境 key 调 mock；请求状态 `[200,200,400]`。
4. 后台概览：请求 3，缓存 read=40、write=60、prompt=120；请求日志 3 条、错误日志 1 条；OpenDesign 请求 0。
5. 请求日志 UI 显示绑定命中、缓存写入 30，不显示原始 smoke 会话值；错误页显示 400/request/停止重试。
6. 最终二进制重启后认证仍有效，统计写入 60 保留。强制新文档导航加载新 hash 资源 `/assets/app.js?v=959fe0fd46`，确认概览已测缓存 40/120；只有未知样本时 hitRate=null，混入未知 input 时已测比率仍为 1/3。

## 边界

未部署、未调用真实 Token Harbor 推理；未实机启动 Codex/Claude Code。注册/自动补号保持未接线。没有凭证支持的余额查询契约，不展示虚构余额。临时文件已登记 `_tmp_artifacts.md`，仅停止本任务启动的进程，未经用户同意不删除文件；Trellis 记录保留。
