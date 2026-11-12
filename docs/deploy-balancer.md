# Deploy 号池 Balancer

## 功能与入口
顶部切换「Deploy 号池」，复用现有概览、账号、下游/导入密钥、路由设置、请求/错误/详细日志。该功能在独立分支 `feat/deploy-balancer` 开发，与原工作区尚未提交的 Deploy 注册机相互独立。

- 账号：手动添加、编辑、启停、批量导入和去重、分组、代理、权重/优先级、并发与RPM。使用 **Secret Key**，不是20位access_key；Key没有可靠前缀，必须显式选择 `provider=deploy`。
- 路由：独立选号/会话头/粘性/TTL/忙时处理、等待、尝试上限、健康阈值、首事件/空闲/请求超时、请求体/模型缓存及直连/代理连接上限。默认在途最少；未知余额不提供余量排序、钱包/套餐阈值或共享付费来源控制。
- 模型：独立目录检查开关、检查间隔/并发/新鲜度，以及实际目录刷新间隔；目录间隔0关闭周期读取，首次/手动读取仍可用。快照来源绑定provider+Key+Base+代理，变化失效，迟到结果不得覆盖新来源。
- 错误/重试：独立默认值、增删改、启停、排序、状态/正文/响应头匹配、账号或账号×模型动作、恢复时间/冷却和下游状态。重试规则是停止换号规则，停用/删除可允许其他符合安全条件的换号；Deploy明确的停止规则不会被冷却动作覆盖。流已输出或未知发送结果始终禁止重放。
- 日志：请求/错误摘要、关联详情入口；详情开关默认关闭且本池独立，脱敏Key/Cookie/会话标识，每体最多64KiB。全局日志保留/容量是共享设施，在Deploy设置页只读；本池保存不改全局或其他池设置。价格表与费用估算则是Deploy独立的。
- 概览：可调度账号、24h请求/成功率/Token/实测缓存、费用估算、会话绑定、模型和出口统计、目录新鲜度及余额能力说明。可调度不等于上游一定授权；费用不是上游账单。

## 接入与协议
上游默认API根：`https://api.deploy.com/api/v1`。官方还提供SF/VA和CDN地址，按官网Quickstart手动选择，不自动切换地区/账号代理。

本地对下游提供：
- `GET /v1/models`
- `POST /v1/chat/completions`（原生非流式/SSE）

NewAPI渠道选OpenAI，Base填Balancer地址（不要填上游`/api/v1`），使用Deploy分组下游密钥。Responses、Messages、Embeddings及未知路径本地拒绝，不隐式转换协议。不重编码原生请求，不删除工具，不改模型ID或伪造reasoning/缓存读数。

批量导入每行：
```text
DEPLOY_SECRET_EXAMPLE,账号名,http://127.0.0.1:8080
```
开放导入使用Deploy专属导入密钥：
```json
{"provider":"deploy","accounts":[{"runtimeKey":"DEPLOY_SECRET_EXAMPLE","name":"示例","group":"deploy"}]}
```
真实凭据只进入管理员配置，不应粘到Git或日志。管理端沿用已授权的完整Key/代理查看与复制、管理员会话/CSRF/no-store边界；退出时清除页面凭据。

## 余额与授权边界
**组织余额读取未接入。** 当前只绑定推理Secret，官方推理文档未提供用该Key读取组织credit的接口；portal余额需要独立登录会话，本次没有绑定。UI/API明确显示 `not-connected` / 未知，而非0。不会使用注册促销金额、模型目录、402/405推算当前余额，也不按多Key累计组织余额。

「刷新模型目录（余额未知）」返回 `catalogRefreshed=true, quotaRefreshed=false`；不会提示额度已刷新。「免费检查」只读取目录，目录200不证明Key已active或推理授权。勾选真实请求才执行少量付费推理，默认模型为 `deepseek-ai/deepseek-v4.1-flash-1017`，允许手填。测试上限256输出Token包含reasoning，短预算下content=null并不一定是失败。

## 默认错误策略
| 上游结果 | 默认处理 |
|---|---|
| 401 | 账号短暂冷却；可能无效/过期/尚未active，不直接永久隔离 |
| 402 | 计费拒绝，账号冷却；不伪造金额、不自动充值 |
| 403 | 本号×模型权限冷却，停止换号；不推断封禁 |
| 405 + 官方Organization currently paused文案 | 组织暂停冷却、下游503，停止换号；人工核对余额/条款 |
| 其他405/400/404/参数错误 | 停止换号，不遍历账号 |
| 429 | 完整遵守秒数/日期Retry-After，账号冷却且默认停止轮换；组织/IP限额可能共享 |
| 500/502/503/504 | 短暂冷却；仅在未输出且明确可重试时按尝试上限处理 |
| 已发送后的网络不确定 / 已输出流 | 不重放；流缺失终止事件记失败，不伪造DONE |

官方错误说明：https://docs.deploy.com/errors-and-limits.md 。官网默认5请求/秒，组织级提额；本服务账号RPM是本地限制，不是上游限额保证。

## 验证与交付状态
详见 `.trellis/tasks/11-13-deploy-balancer/validation.md` 与 `research.md`。
- 已接入并本地验证：管理/路由/错误与重试/目录刷新与持久化/概览、原生Chat与下游目录。
- 已真实验证：用户授权Key的目录、默认DeepSeek推理；经Balancer转发的GLM非流式与DeepSeek流式均200、单账号尝试，后者收到DONE并保留reasoning。
- 未接入：组织余额/账单控制台会话、自动注册与上游Key管理、支付；不宣称新号池完整交付门禁全部通过。
- 不支持：未经验证的Responses/Messages/Embeddings，不放无效开关。
- 已推送功能分支，并从实际线上b786d7c隔离发布Deploy改动至OVH，运行c6ae4b2；主线未上线ModelStudio未夹带。Docker健康、双内网别名、公网资源和数据保留已验收，完整10分钟自然观察记录见[部署证据](../.trellis/tasks/11-13-deploy-balancer/deployment.md)。没有自动导入生产Deploy账号/测试Key，尚不等于生产Deploy推理渠道验收。原工作区未修改，线上原配置完整保留。
