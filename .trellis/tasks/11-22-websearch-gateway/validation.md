# 第一阶段本地验收记录（2026-11-22）

## 已完成
- 独立 `cmd/websearch-gateway`、标准库HTTP配置/受控路由、鉴权与模型名单。
- typed Tavily后端、Responses搜索函数循环、JSON/SSE统一公开事件与真实文本、有限内存历史回放、普通工具交还客户端、用量聚合和安全日志。
- 独立Dockerfile/Compose/无密钥配置示例、用户文档与可执行契约。

## 最后一次验证
- `gofmt`；`git diff --check`：通过。
- `go test -count=1 -timeout=30s ./internal/websearch ./internal/searchgateway ./cmd/websearch-gateway`：通过（1.196s / 0.841s / command无测试）。
- `go test -race -count=1 -timeout=60s` 同路径：通过（1.470s / 1.535s / command无测试）。
- `go vet` 同路径：通过。
- `go build -o .gotmp/websearch-gateway ./cmd/websearch-gateway`：通过。
- 以假环境凭据运行示例配置 `-check`：通过、不发送网络请求。
- Docker daemon12秒未响应：未build镜像，未启动容器；不重复轮询。

测试覆盖两轮+第二上游、流式首字在最终生成前可见、单公开生命周期/序号/位置、完整历史与跨scope/TTL/LRU/重启拒绝、普通函数混合不被服务端执行、搜索错误不重试、HTTP/流式截断不伪完成、取消传播、参数/模型/鉴权/body/并发门禁、真usage全覆盖与缺测未知、原生字节与凭据重定向拒绝、内部参数echo不泄露、总输出预算与强制搜索防循环、JSON上游返回的完整文本转真实SSE文本事件。

## 明确未验收
- 未发本轮真实模型推理；此前仅验证过用户指定搜索HTTP接口，不等于网关链路实测。
- 未部署、未修改NewAPI或现有号池、未进行真实Codex Desktop操作。
- 未实现跨多轮原子账号owner/租约；现有粘性不保证固定账号。
- 搜索高级参数/preview/cached-only/open/find、Chat/Messages转换、自动url_citation注解与原生引用UI、管理UI/热更新/持久化多副本历史均未交付。
- 函数后重新出现文本/思考item的流式交错当前明确失败，不静默重排。

## Git并发边界
开始时Factory窗口已有 `proxy.go` / `factory_gemini_compat_test.go` 修改，本任务未触碰。该窗口后续多次提交把本任务的新增文件一起收入Factory提交；保持历史，不reset、不重写。后续只针对独立模块/文档/spec/本任务Trellis定向提交。不能把这些混合历史直接当作隔离的生产发布版本。

## 下一阶段
用受控新测试路由验证真实Codex的工具声明及NewAPI路径/记账/流式历史；解决账号连续性或明确限定直接上游实验；根据真实声明补充搜索参数和引用，再单独申请部署。本任务保持in_progress，不能归档为完整生产交付。
