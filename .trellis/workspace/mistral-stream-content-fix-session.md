# Mistral Chat 内容块导致 NewAPI“断流”诊断与本地修复

- 日期：2026-11-07 UTC；用户两次提供截图，要求定位并解决流式中断。
- 小范围兼容修复，未新建 Trellis 任务。项目只含局部 `.trellis/`，无本地 workflow/scripts，按已存在的后端规范及上级 thinking guides 直接执行。

## 只读生产证据

- 20:22 UTC 起 SSH 只读核对：Balancer 为918352d，健康、无重启/无OOM；NewAPI仍为原ad556799镜像。没有改配置、触发推理、重启或部署。
- 截图相符的 Mistral 渠道180使用 Chat 原生路由（type58，converter=none），入口为原有 Docker 内部 Balancer 地址。
- NewAPI日志已收到 `end_reason=done` 和 `response_status=completed`，但单条请求产生数十到数千次 `cannot unmarshal array ... choices.delta.content ... string`。这证明该类请求主要是响应内容数组与NewAPI字符串类型不兼容，不能仅凭UI“断流”归因于非流式连接或缺失DONE。
- 约20:25 UTC的近20分钟汇总：渠道180有29条上述解析错误、2条正常；Modal156有38条正常DONE，OpenCode167有72条正常DONE及2条正常EOF。其他渠道也大量正常，不能声称所有渠道传输都已断开。
- Balancer记录对应Mistral为stream=true/HTTP200/success。原代码逐个完整SSE事件立即Flush；但原生content数组被直接透传，Balancer无法感知下游NewAPI解析失败，因而两边成功口径不同。
- Mistral官方reasoning文档描述：thinking阶段为ThinkChunk数组，过渡帧同时含thinking/text，answer阶段可变为字符串；SDK模型同时允许signed/reference块，不能通过丢弃这些内容假装兼容。
- 诊断程序/凭据只在远端内存/stdin使用，未下载生产配置、Key、请求正文或数据库。日志自动溢出及公开文档产物登记在`_tmp_artifacts.md`。

## 修复边界

- 新增`internal/server/mistral_response.go`；仅Mistral `/v1/chat/completions` 的JSON及SSE将text数组合并为字符串content、unsigned thinking文本合并为reasoning_content。
- 每个完整事件单独转换并立即Flush，不等待整段生成；保留工具/ID/finish_reason/真实usage、其他消息/根字段及大整数原值，保留SSE元数据/注释/CRLF/多行及终止标记。
- 原生字符串/null/usage/error/DONE事件不改字节；Responses/FIM/其他provider不变。
- 不可表示的块/非空签名/引用/未知块元数据显式报错：首帧前502；提交后返回error SSE且记录failed/compatibility，不伪造DONE、不重放、不惩罚账号或泄漏lease。未通过禁用reasoning或删除工具掩盖问题。
- `.trellis/spec/backend/mistral.md`同步上述窄例外，未扩展OpenCode缓冲兼容模式或新建调度器。

## 验证

- `mistral_response_test.go`覆盖thinking/text/混合过渡/空数组/closing marker、既有reasoning及多choice、工具参数/大整数、拒绝未知/signed/reference块、SSE元数据及CRLF多行、JSON/SSE成功及首帧前/提交后失败、零重放/零账号样本惩罚/lease释放、FIM/Responses/其他池隔离。
- 真实本地HTTP门控Mock：上游发送并Flush首个thinking事件后阻塞，客户端已读到规范化首帧才放行上游继续，证明确实逐块输出而非整段缓冲；结束后真实usage及DONE保留。
- 最终`go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./cmd/odb`、gofmt与git diff检查通过。一次串行全量normal+race批次超过工具120秒窗口，正常测试已通过；race随后单独重跑通过，不将工具超时伪称测试失败或忽略未完成的检查。
- 本地Mock日志与二进制仅在`.analysis/mistral-stream-fix/`，已Git忽略；无真实上游推理或生产验收。

## 交付状态

- 修复仅本地提交，尚未push/部署。已询问用户是否授权测试通过后更新OVH Balancer，未擅自修改NewAPI渠道配置。
- 若获授权，按`docs/ovh-deployment.md`已有预检/构建/一致备份切换/验收流程执行；上线后观察自然业务请求的NewAPI解析错误和DONE状态，不能将Mock通过当作生产修复验收。
- 临时产物待用户同意后清理，Trellis规范/本记录保留。

参考：
- https://docs.mistral.ai/inference/conversations/reasoning
- https://raw.githubusercontent.com/mistralai/client-python/main/src/mistralai/client/models/thinkchunk.py
