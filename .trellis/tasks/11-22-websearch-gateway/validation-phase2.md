# 第二阶段：兼容修补与真实链路验证（2026-11-22）

## 授权与范围
用户「继续」开发；本轮不部署、不修改生产/NewAPI配置，不重放用户历史。真实测试仅合成公共文档问题，一个固定的最近成功直连Mistral账号；搜索密钥仅当前会话内存→SSH stdin→测试进程env，模型Key仅远端读取现有配置后在内存交给子进程。测试二进制是临时验收工具，不是在线服务。

## 源码证据与修改
研究见`research/codex-0167.md`，本机版本查询0.167.0，未启动Codex子代理。
- Codex显式external_web_access，合法回放可能省略sources；live=true/固定web_search版本/text-only可处理，cached-only/indexed-only仍明确拒绝。
- allowed_domains转Tavily include_domains并复核子域边界；context-size在硬上限内控结果/片段；地理语义不伪造。
- 历史允许合法去掉sources/null元数据，仍检查权威id/status/action/query；增加跨model scope。
- 函数done后释放SSE屏障，后续文本/思考逐帧；原生done/最终output不一致明确失败。
- 只对回答中真实独立来源URL增加url_citation，不改正文；JSON/annotation事件/最终output相同，保留已有引用。
- include_usage=true显式请求搜索credits；usage=null不能算测量0；后轮失败时不拿前轮sum冒充整单。

## 真实合成测试：通过
在OVH运行`TestLiveResponsesSearch`一次，使用固定官方Base `https://api.mistral.ai/v1`、一个最近成功无代理账号，搜索端点为用户指定Tavily Hikari：
1. 模型Responses流HTTP200，强制搜索函数。
2. Tavily搜索HTTP200：`docs.mistral.ai`域名过滤、basic/low/最多3来源。
3. 回填搜索结果后模型Responses流HTTP200；约4652ms完成，46个事件、1个真实url_citation、完整累计usage存在。
4. 按Codex公开模型删除历史action.sources，再发送不搜索的合成续问：模型HTTP200，历史回放成功。

总计**3次固定账号模型请求、1次真实搜索，测试5.04秒**。无自动重试、无用户历史/凭据落盘；未创建测试渠道/账号/密钥，未触碰正在运行的服务配置。真实测试源码`internal/searchgateway/live_test.go`默认skip，只有显式环境凭据才可运行；每次最多3个模型请求。

真实测试后追加了null/失败usage守卫、done/terminal一致性守卫及URL嵌套误匹配保护，这些仅本地Mock/race复验，没有为了相同公共查询重复消耗真实搜索/推理。不要将后续守卫的本地验证说成额外真上游实测。

## 本地验收
- 原有第一阶段矩阵保持通过；新增Codex选项/合法省略sources/跨模型拒绝、域名过滤与返回越域拒绝、Unicode引用/假URL不引用、函数后文本顺序、null和后轮失败用量未知。
- normal/race/vet/build均通过；最终命令结果以会话工具回执为准。
- 未运行真实Codex Desktop/CLI自主任务，没有NewAPI测试渠道或记账验收。
- 未构建/启动新容器，未部署生产；此前本机Docker daemon无响应，本轮未重复轮询。

## 仍然阻塞正式上线
- Codex默认cached将传external_web_access=false；没有经验证的cache-only后端保证时不能宣称默认客户端零配置可用。Tavily文档新增max_age_hours=-1不能单凭200推断用户代理和后端绝不live-fetch。
- 多账号号池跨轮固定owner/租约未实现；本次直接固定账号只能证明单上游链路。
- NewAPI路径/用量计费/自动重试和真实Codex引用UI/工具历史需新受控渠道验收。
- open/find、地理选项、preview/image、其他协议、管理UI、持久化多副本历史仍未实现。

临时产物见_tmp_artifacts.md，未经用户同意不清理；Trellis研究/验收保留。
