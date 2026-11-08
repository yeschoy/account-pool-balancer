# 验收记录

## 本地实现
- `go test ./internal/server -run Mistral -count=1`：通过，2.739s；包含现有native/SSE/流式flush/错误重放保护与新参数定向回归。
- 自审移除无用字段/明确思考变量后，仅重新运行6组新增参数测试：通过，0.584s；`go build ./cmd/odb`、gofmt及`git diff --check`通过。
- 初次测试编译发现测试fixture误用了不存在的Config.BaseURL，已改为既有Upstream.RuntimeBase并补其他池账号，未涉及线上。
- 新覆盖：两种思考开关/显式档位/默认/冲突/预算；developer仅role字节替换（保留顺序、重复未知字段、工具和大数）；连续多字段删除首中尾、seed大整数与冲突、metadata/cache值；39组本地拒绝零尝试；其他模型/协议与其他供应商字节相等；上游422不删seed/不换号重放。
- 自审确认strict JSON在前、身份原值读取在前、账号/RPM在后；没有新增调度/TTL/cache hint优先级、生产推理、账号策略或UI修改。
- 仅公开官方Chat schema复核random_seed，不把mock或泛Mistral schema当作特定DeepSeek型号实测证据。该型号random_seed实际支持/确定性尚未知。

## 集成/上线（待补）
fetch发现origin/master=e06a589，新增诊断文档和独立tools/mistral工具；相对当前基线无internal/cmd/go.mod/go.sum/Dockerfile应用差异。原master工作树干净，仅只读核对，保留并发工具更新，不执行注册工具。
