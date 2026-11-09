# 2026-11-09 11:36 UTC只读复查：当前错误与缓存分流

用户问“现在又出现了什么问题”。本轮仅远端内存读取Docker状态、已有业务摘要/错误和NewAPI只读事务；不发推理、不重启、不调整配置/诊断开关、不修改其他窗口正在进行的容量/注册代码。无新临时脚本/原始正文/凭据下载。

## 渠道188与诊断

- UTC11:35:51（北京时间19:35:51）两容器healthy、restart0、无OOM。Balancer仍运行480dd2d，启动UTC08:21:01，与本轮授权上线版本一致。
- 渠道188配置/模型映射及目标Key未改变，保持启用。当日21条目标摘要中，原用户长会话仍只有UTC08:05–08:10的14条；后7条均为13输入token的短请求。NewAPI核对这些后续请求为user_id=1，不是原user69长会话，不能因它们缓存0、binding miss或账号不同，声称原会话再次换号/缓存故障。
- 诊断窗口于北京时间16:51:01到期，当前不再新采样。仅采到UTC08:50:03一条13-token/1消息/0工具短请求；实际Body读取228字节完整、matchesExpected=true、写入成功1次、上游200且有真实trace，但没有前序对照，不能解释原长会话缓存下降。未无授权重启续采。

## 当前另行发现的错误

首次滚动30分钟快照（约UTC11:06–11:36）Mistral摘要：219成功200、40失败400、12失败503、2失败422、10失败404、3客户端取消499。NewAPI和号池起止/尝试口径不同，不强行逐项凑数；以下为已精确核对的原因。

### GLM5.3 Flash：web_search工具不兼容

- NewAPI渠道185（自测）1条、187（向总）2条，均映射Mistral的zai-glm-5-3-flash；不是188。参数override均空。
- 精确requestId：req_d7cd801ac041d855c14826e8、req_6301310b868ee572f087ec73、req_c01e7d1d89b448b081de85bf。
- 原生Mistral HTTP400、invalid_request_error；远端仅提取协议词/字段枚举核对，上游指明web_search工具类型不支持，目前只接受function工具。不能把它归因于缓存、密钥耗尽或本地诊断。
- 不擅自删除web_search或伪造function实现；如需修复，须明确客户端改用可执行function工具，或另有授权的真实搜索实现/适配，不用丢功能换200。

### DeepSeek渠道180：两个思考别名冲突

- NewAPI滚动窗口37条精确匹配已部署源码中的静态错误：`Mistral assistant reasoning conflicts with reasoning_content; neither thought was discarded`。
- 对应号池400均无upstream attempt，属于现有无损/冲突保护在本地拒绝，不是官方Mistral400。请求同时携带不相等的两个思考字段，尚未取得二者的完整内容和来源；不能直接认定是哪一层改写或自动丢弃任意一个。
- 渠道180当前param_override为空，不能套用历史渠道规则作为当前致因；本轮没有对冲突保护作静默降级。

### DeepSeek渠道180：cache_control泄漏到文本块

- UTC11:21:32/33，requestId req_7076abd66e25e355a9407e5d、req_5e9decf276774b0712ecb33d，上游HTTP422。
- 在远端仅解析原生错误loc/type，未输出input/正文；首分支为`body.messages.1476.user.content.str/string_type`，另一分支精确为第1476零基user消息的第0个text块下`cache_control/extra_forbidden`。
- 因此此次422有明确的多余字段证据，不应笼统说图片或整个content数组不支持；不能靠删除图片/历史或换号修复。Mistral的缓存提示应遵守其prompt_cache_key接口，而非假定Anthropic块级cache_control通用。

### 503与其他状态

- 12条最终503对应已部署规则mistral-service-unavailable，原生code3800，healthAction=none；为官方模型/服务失败，不当成账号认证失效或改成盲目换号重试。
- 有429尝试级事件，部分后续成功；不能把每个尝试都当最终失败。
- 另有10条本地404与3条499；本轮未进一步定位404路径，不编造原因，499单列为客户端取消。

## 状态

本轮是只读诊断报告，没有修复/推送业务代码或部署。原GLM188低缓存根因仍未证实，不能用这些不同渠道/模型的错误冒充原缓存根因。正在其他窗口修改的容量/注册工作树保持不动；本业务证据单独提交，任务仍in_progress。
