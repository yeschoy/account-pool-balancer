# 再次观察及错误归因（2026-11-08 UTC）

用户要求再观察10分钟，有Mistral错误则判断是否当前Balancer造成并修复。不启动代理，不发主动推理，不改账号/重试策略或NewAPI规则。

## 预查（不是新完整观察窗口）

06:38:29.015–06:48:29.015 UTC，按完成时间Mistral84请求/78成功/6失败（5次503、1次422），随后还有同类错误。发现错误后立即返回诊断，没有将中止的观察当作已完成10分钟。

- 503：上游HTTP503、JSON internal_server_error、固定Service unavailable；每最终失败有既有两次账号尝试。证据证明响应来自上游，不是本地生成503，但不据此推断所有503的底层原因或修改限流/账号策略。
- 422：HTTP422、request分类、单次尝试、无账户健康惩罚/重放；responseBytes=65536、通用48字符reason。没有完整loc/type，不能归因thinking、metadata或猜测其他字段。
- 现有Balancer非2xx仅取64KiB，错误体包含大量验证input时变成不完整JSON，通用日志丢诊断且未区分body截断。这是已确认的Balancer诊断缺口，不是已经确认422由本地请求转换引起。

## 定向诊断修复

- 仅Mistral非2xx多读至多1字节判断真实截断，转发body仍原64KiB前缀；不改变状态码、请求内容、重放或健康策略。
- 新增可选mistralValidation（仅allowlisted loc/type）与responseBodyTruncated。解析已有前缀的detail/list及嵌套JSON-string message，支持input截断前已有坐标；深度8、结果8、位置20项和64KiB诊断输入上限。未知/后置/不完整坐标不发明证据。
- 不搜索input/ctx任意子树，不记录input/任意message/身份/凭据；未知loc段统一[other]。不提高原始错误体保存上限、不启用详情日志。
- `go test ./internal/server -run Mistral -count=1`通过3.744s；覆盖嵌套/截断JSON-string、隐私、真实64KiB与超限、单次422及无健康惩罚，gofmt/diff check/build通过。最终加诊断输入64KiB上限后只重跑相关定向测试及必要build。

## 发布边界

fetch发现master另有c63b5ac/5962c86工具更新及8b701f0注册UI/store/API新功能。本轮只发布当前线上应用基线+Mistral诊断修复的干净功能提交，不顺带上线未知注册功能，也不覆盖master并发提交。后续集成保留这些改动。上线后重新完整观察10分钟；若取得422坐标，再按证据定向修复，当前尚不能宣称已解决422。
