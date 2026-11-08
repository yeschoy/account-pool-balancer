# 定向验证

- NewAPI线上镜像ad556799：前端completion_tokens/use_time；后端time.Now().Unix()-StartTime.Unix()。新增useTimeSec/averageTps按相同公式，前端只做一次整数四舍五入；不是毫秒耗时取整。确定性用例覆盖跨秒200ms/同秒0/真实输出缺失或零/失败/含等待及重试，不覆盖原upstreamTps。
- 线上a817edf源码363个既有运行文件逐项SHA对照：仅mistral_request.go/proxy.go不同，并新增mistral_affinity_test.go；已审阅并保留X-Affinity修复及回归，未从旧master回退。
- 真实本地HTTP Mock验证客户端原始参数、上游实际适配参数（reasoning/store/cache）、Transport自动Host/Content-Length/User-Agent、适配前数组/适配后文本及大整数、429两次账号/尝试状态关联、64KiB正文边界/截断、关闭分支及网络失败不造响应。没有主动生产推理/重放/压测。
- 原gated首帧用例开启详情后仍能在上游未完成时收到推理帧，同时原生SSE日志保留9007199254740993。mutex保护reader快照，race通过。
- 凭据、自定义Token头、Cookie和响应中新获Cookie值在客户端/上游/下游预览均被掩码；测试曾暴露新Cookie只在上游段掩码的缺口，已修复为请求内传播私有值，同时供错误摘要脱敏。未知敏感JSON字段与X-Affinity也掩码，原生业务字节不改。
- `go test ./internal/server -run 'Test(Mistral|AverageTPS|UpstreamTPS|EnsureMistralXAffinity|.*(Detail|Privacy))' -count=1`通过4.547s；相同定向race通过6.682s。
- Mistral Node行为、app.js语法、server go vet、build到/dev/null、gofmt及diff check通过。未跑重复全仓套件。
- 前端Node验证两TPS名称、旧记录未知、四段/各尝试呈现与HTML转义；未冒充生产管理员浏览器全UI验收。

# 发布与验收

- 0ed42ff已提交推送并上线，保留已部署X-Affinity。496源码文件SHA256一致；7.06秒构建，17:38:44 healthy/零重启/双内网入口与本机资源验收。191账号/186TH核心记录/原config字节/Mistral注册库与运行参数保留。正式备份20261108T173746Z-mistral-wire-tps-0ed42ff，旧镜像/data回退资料保留。
- 按用户明确记录要求，17:40:37通过既有管理员完整设置API仅启用Mistral独立detailLog并验证持久化，revision5509；其他有效参数、账号/密钥/管理员、其他池及全局配置不变，routing/quota原本已独立，无新钉住继承。旧出厂规则按现有有效规范化值持久化，不冒充配置完全没改。正文64KiB、最大详情500不变。
- 17:41:36公网真实未登录浏览器登录页、healthz、四资源200，版本93a90552bf及SHA256与源码一致；浏览器关闭。管理员凭据/Cookie/CSRF仅远端内存，正常logout，无敏感副本。
- 17:41:38自然样本51条：47条有全程平均TPS且公式逐条相符，原上游TPS也47条；15条新wire详情记录已落盘，三段请求/原生响应头/体、各尝试状态及sent/截断标记具备，凭据头掩码通过。只输出计数/字节/布尔，不回显生产正文。
- 本次没有生产推理/重放/压测；真实管理员详情弹窗未交互，UI由Node和生产API投影核验。详情是脱敏限长预览而非逐字节pcap，旧日志不补造；其他池没有增加native capture。上线/业务验收已即时反馈，补记录不再部署。
