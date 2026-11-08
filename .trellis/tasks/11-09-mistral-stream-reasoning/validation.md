# 验证与交付边界（2026-11-09）

## 复现与修复
- 新增终态用例先在旧代码运行：首帧空completed/data-only/incomplete/纯工具终态均等待EOF超时；空流错误码不明确；created-only/delta-only/[DONE]等流缺终态仍返回成功。修复后全部通过。
- Responses首帧/后续completed、incomplete及时结束；data.type形式、转义type、字段冲突/重复/错误JSON/嵌套伪终态和客户端取消有覆盖。上游保持连接、正常EOF、实际unexpectedEOF、已提交后failed事件都用真实本地HTTP模拟。
- 缺终态未输出前502；已提交后保持HTTP200并发一个明确error事件，request/attempt/native-detail为failed/incomplete_stream，TPS未知，不重放、不扣账号健康、不泄漏lease；主动客户端取消仍499。
- 三精确模型Chat/Responses档位矩阵覆盖low/high/max、缺省high、null/不支持值归一high、同值别名/冲突/重复/转义、兼容开关与原生Responses reasoning附加字段/input保留。max不再降级。Large4保留原生medium且没有新增默认；其他池/路径不变。
- 四模型Chat/Responses × 流式/非流式矩阵保留多轮思考、工具调用ID、Deny结果、apply_patch多行补丁/引号/反斜杠/大整数、缓存键/亲和头与真实usage。DeepSeek专属developer/store/seed规则保持，不误套GLM；额外GLM图片/字段仅验证原样保留，不宣称这些字段全部获上游支持。工具拒绝不删工具重试。

## 命令结果
- `go test ./...`：通过，server70.053s；其他包部分复用已有缓存。
- `go test -race ./...`：通过；最终运行server81.414s，其余已验证包命中缓存。
- 最终测试夹具进一步按原生字段收敛后，`go test -race ./internal/server -run Mistral -count=1`：通过，7.720s。
- `go vet ./...`、`go build -o /dev/null ./cmd/odb`：通过。
- `node --check internal/server/web/app.js`和所有`internal/server/*_test.js`：通过（包括Mistral/TH/OpenDesign相关行为）；无npm/pnpm安装/产物。
- `gofmt`与`git diff --check`：通过。

## 本地真实浏览器（不是生产/上游推理）
- 临时服务仅127.0.0.1:53555，零账号，quota=false、Mistral目录周期0、OpenCode=false、全部配置上游为不可达127.0.0.1:1。
- 浏览器正常登录假管理员、切Mistral、核对概览预算说明与手动添加窗口：low/high/max缺省high、套餐不是钱包、失败保留历史未知、共享组织预算不可相加、客户端尚未验收提示均可见。未提交账号添加或发推理。
- 通过管理员API读回确认accountCount=0、有效quotaEnabled=false、MistralBase=http://127.0.0.1:1/v1；回执已核验719bytes，位于登记的`.analysis/mistral-stream-reasoning/browser-receipt.json`。
- 公开资料浏览器和本地浏览器均关闭。仅本轮PID73795已TERM停止，53555监听关闭；文件未清理，待用户同意。_tmp_artifacts.md已登记，Trellis保留。

## 交付状态
代码/规格/文档与本地验证完成；无存储迁移、配置字段或模型别名入口。提交状态见task.json/workspace。未推送、未部署、未改线上配置，未发真实模型推理、未注册/付款。Codex/Cherry Studio/pi客户端端到端、实际模型授权/档位效果和生产缓存命中不属于本次已验收范围，后续需单独授权真实测试。
