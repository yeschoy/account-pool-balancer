# 执行计划

1. [x] 独立严格配置、路由与凭据解析；Tavily typed后端，固定来源/禁止重定向/响应上限。
2. [x] Responses 请求工具转换、tool_choice/include校验，原生字节透传。
3. [x] 有界工具循环、真实usage合并、普通函数混用、历史缓存及作用域隔离。
4. [x] JSON/SSE 统一输出、逐帧推进、ID/序号映射与终态失败处理；不支持的函数后文本交错明确失败。
5. [x] 独立命令、优雅关闭、容器/Compose/无凭据配置示例、文档（镜像未构建）。
6. [x] HTTP Mock矩阵、race/vet/build、私密值审查；更新spec与任务记录，后续仅定向提交；并发窗口误收本任务文件的历史见validation.md。
7. [ ] 下一阶段：真实Codex/NewAPI工具声明与流式历史、计费核对、跨轮账号连续性、按需高级参数/引用兼容；生产部署未授权未执行。

验证：go test ./internal/websearch ./internal/searchgateway ./cmd/websearch-gateway；go test -race 同路径；go vet 同路径；go build -o .gotmp/websearch-gateway ./cmd/websearch-gateway。业务构建产物登记 _tmp_artifacts.md，未经同意不清理。

本轮不发真实生产推理、不修改NewAPI、不部署、不在日志/脚本/样例中写用户提供的密钥。隔离新增路径，避开已存在的 proxy.go 与 factory_gemini_compat_test.go 脏改动。
