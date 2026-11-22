# 执行计划

1. [ ] 独立严格配置、路由与凭据解析；Tavily typed后端，固定来源/禁止重定向/响应上限。
2. [ ] Responses 请求工具转换、tool_choice/include校验，原生字节透传。
3. [ ] 有界工具循环、真实usage合并、普通函数混用、历史缓存及作用域隔离。
4. [ ] JSON/SSE 统一输出、逐帧推进、ID/序号映射与终态失败处理。
5. [ ] 独立命令、优雅关闭、容器/Compose/无凭据配置示例、文档。
6. [ ] HTTP Mock矩阵、race/vet/build、私密值审查；更新spec与任务记录并定向提交，不提交其他工作修改。

验证：go test ./internal/websearch ./internal/searchgateway ./cmd/websearch-gateway；go test -race 同路径；go vet 同路径；go build -o .gotmp/websearch-gateway ./cmd/websearch-gateway。业务构建产物登记 _tmp_artifacts.md，未经同意不清理。

本轮不发真实生产推理、不修改NewAPI、不部署、不在日志/脚本/样例中写用户提供的密钥。隔离新增路径，避开已存在的 proxy.go 与 factory_gemini_compat_test.go 脏改动。
