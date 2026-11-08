# 定向验证

- NewAPI线上镜像ad556799：前端completion_tokens/use_time；后端time.Now().Unix()-StartTime.Unix()。新增useTimeSec/averageTps按相同公式，前端只做一次整数四舍五入；不是毫秒耗时取整。确定性用例覆盖跨秒200ms/同秒0/真实输出缺失或零/失败/含等待及重试，不覆盖原upstreamTps。
- 线上a817edf源码363个既有运行文件逐项SHA对照：仅mistral_request.go/proxy.go不同，并新增mistral_affinity_test.go；已审阅并保留X-Affinity修复及回归，未从旧master回退。
- 真实本地HTTP Mock验证客户端原始参数、上游实际适配参数（reasoning/store/cache）、Transport自动Host/Content-Length/User-Agent、适配前数组/适配后文本及大整数、429两次账号/尝试状态关联、64KiB正文边界/截断、关闭分支及网络失败不造响应。没有主动生产推理/重放/压测。
- 原gated首帧用例开启详情后仍能在上游未完成时收到推理帧，同时原生SSE日志保留9007199254740993。mutex保护reader快照，race通过。
- 凭据、自定义Token头、Cookie和响应中新获Cookie值在客户端/上游/下游预览均被掩码；测试曾暴露新Cookie只在上游段掩码的缺口，已修复为请求内传播私有值，同时供错误摘要脱敏。未知敏感JSON字段与X-Affinity也掩码，原生业务字节不改。
- `go test ./internal/server -run 'Test(Mistral|AverageTPS|UpstreamTPS|EnsureMistralXAffinity|.*(Detail|Privacy))' -count=1`通过4.547s；相同定向race通过6.682s。
- Mistral Node行为、app.js语法、server go vet、build到/dev/null、gofmt及diff check通过。未跑重复全仓套件。
- 前端Node验证两TPS名称、旧记录未知、四段/各尝试呈现与HTML转义；未冒充生产管理员浏览器全UI验收。

# 发布边界（待更新）

OVH目前a817edf healthy/零重启；本次会在保留该缓存路由修复的基础上发布。当前共享详情true、Mistral独立false、正文64KiB、最大详情500。按用户明确记录要求，发布后仅启用Mistral独立详情并验证其他有效设置不变；不创建生产推理来制造日志，使用自然流量验收。
