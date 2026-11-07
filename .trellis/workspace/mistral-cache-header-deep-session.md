# 缓存头、双跳转发与选号深度只读排查（2026-11-07 UTC）

## 请求与边界

用户要求深入排查是否漏缓存头、导致不断切号。本轮只读生产诊断，不改Nginx/NewAPI/Balancer配置、功能开关、业务代码，不重启或部署，不发起模型推理。临时脚本/脱敏证据登记`_tmp_artifacts.md`，生产流量仅内存解析、不保存pcap/正文/Key。

## 1. 已实证：Nginx会丢弃Session_id

- `nginx -T`确认入口配置位于`/etc/nginx/conf.d/newapi.conf`，全配置未设置underscores_in_headers或ignore_invalid_headers覆盖。官方默认分别off/on：含下划线头被标记无效并忽略。
- 21:55:24 UTC在正常公开未登录浏览器对`https://yeschoy.com/api/status`发起一次只读GET，使用虚构固定标识同时发送Session_id、Session-Id、X-Session-Id、X-Prompt-Cache-Key。HTTP200；NewAPI入口TCP形态实测：只有Session_id消失，其余三种到达。
- 21:55:56 UTC通过证书正常验证/SNI=域名的本机Nginx HTTPS，同样对公开status路由发送相同虚构头；HTTP200，入口再次仅Session_id消失。因不经过Cloudflare，这证明Nginx本身足以造成丢失，不能将其归给NewAPI/Balancer。
- 未关闭TLS验证、改WAF、伪装身份、使用API Key或触发推理；探针虚构值不是用户会话或凭据。
- 因此，使用Codex式Session_id的客户端会被迫退回fallback；但本轮没有客户端发送端的真实头快照，不能断言所有低命中请求原先都发送过该头。

## 2. 已确认的Balancer识别缺口

- `internal/server/identity.go`识别Session_id/Session-Id/X-Session-Id等，并识别正文prompt_cache_key；Mistral使用通用identity。
- Mistral默认不消费`X-Prompt-Cache-Key`/`Prompt-Cache-Key`作为缓存/会话身份；这两个头仅在`internal/server/go_identity.go`的Go专用路径被读取。线上routing.sessionHeaders为空，未配置额外补救。
- `copyRequestHeaders`不会过滤上述会话/缓存头；因此“转发了这个HTTP头”不等于“Mistral选号读取了它”。这是客户端只通过这两个头给出cache hint时的兼容缺口，不是已证明当前所有请求的原因。
- 现有Mistral `prepareMistralRequest`在缺失body prompt_cache_key时补充会话HMAC，并尊重客户端已有值。后续attempt不对Mistral重做body模型映射，生成字段没有在本地后续流程被删掉。
- 官方Mistral缓存合同使用正文prompt_cache_key，不是要求添加HTTP Cache-Control或某个未文档化的必需缓存头。线上本轮36/36请求记录cacheHintSource=session_hmac，说明生成路径被执行；真实高命中也证明缓存未整体失效。

## 3. NewAPI与自然流量双跳

- 渠道180 header_override含`*`，param_override为空，Chat路由converter=none；没有必要重复打开通配透传。pass_through_body_enabled=false，不能把converter=none等同于字节透传。
- 本地NewAPI源码显示头通配过滤凭据/逐跳等不安全头，不过滤标准会话头；Chat DTO保留prompt_cache_key/metadata，但不含根session_id/thread_id/conversation_id。线上ad556799精确源码对象不可用，因此将源码信息标记为静态兼容风险，不冒充已逐行验证线上版本。
- 21:55:18–21:57:48 UTC有界观察NewAPI入口3000与Balancer入口8790。Mistral自然请求的两组可关联样本（21:56:09、21:56:58）都在NewAPI入口已无可识别显式会话/cache头或body身份字段；到Balancer时前缀HMAC、工具HMAC及字段形态一致，只出现预期模型名映射，未发现这两组样本中NewAPI进一步丢掉已有缓存/会话字段。
- 同一Mistral短会话的21:56:09/21:57:47样本前缀临时HMAC一致，日志均绑定命中同一账号；另一个2消息/8工具请求前缀不同，是新绑定，不能当作同一会话换号。
- 采样也包含其他DeepSeek别名/Modal请求；不能把所有DeepSeek样本都归给Mistral。any网卡可能产生重复TCP观测，样本观测条数不作为实际业务请求计数，实际计数以业务JSONL/SQL为准。

## 4. 真实选号与缓存证据（截至21:59:22 UTC）

- 当前版本8d41516启动后Mistral36次请求：35 success、1 client_cancelled；23次binding hit、13次miss，全部message_hmac；0忙溢出、0请求内部故障切换、0多attempt。
- 这些flags只能排除相应类型的切换，不能把请求内switched=false解释为跨请求绝不换号，也不能把13次miss都解释为同一会话被切了13次。
- 超过20万input token的14次成功请求全部落在同一账号；最近7条cacheRate约96.5%–99.4%，最新98.82%。当前35次成功累计input4744200、cached3024128，token加权约63.74%，较之前窗口继续上升；不同时间窗口/业务组合不能当成受控实验。
- 线上sticky=true、smart、wait-overflow、等待2秒；fallback TTL900秒，显式身份TTL3600秒且命中时滑动续期。普通会话绑定只在内存，服务重启会重新建绑定；本轮未重启。漏掉显式身份会失去更强身份语义及1小时TTL，首次消息/前缀变化也可能被当成新会话。

## 5. 建议的最小修复（未实施，待确认）

1. 优先让可控客户端使用`X-Session-Id`或`Session-Id`，同一会话固定值，不用每次变化的请求ID；若需兼容既有Session_id客户端，在明确入口vhost范围允许underscores_in_headers，保持ignore_invalid_headers保护，不全局放开无效头。先nginx -t与非推理头探针，再经授权reload。
2. Mistral增加X-Prompt-Cache-Key/Prompt-Cache-Key兼容读取或到body prompt_cache_key的窄映射。保留已有body值/显式会话优先级、下游Key+模型隔离及日志脱敏；不要把波动cache hint无条件提升为会话ID，也不要全池用常量Key。
3. 不改smart调度、不全池固定一个账号、不移除提示词/思考或擅自延长所有池TTL。先用同一稳定身份的自然业务对比绑定与cached_tokens。

## 状态

只完成只读排查与证据归档，尚未改Nginx或应用代码，未提交配置变更、部署、重启或开启详情日志。150秒采样已正常结束，专用公开浏览器关闭；临时文件未经用户同意不清理。

参考：
- https://nginx.org/en/docs/http/ngx_http_core_module.html#underscores_in_headers
- https://docs.mistral.ai/inference/conversations/advanced/prompt-caching
