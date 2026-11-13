# Deploy 独立号池合同

## 1. Scope / Trigger
修改deploy.com号池的provider/账号导入、独立设置与价格、目录/额度投影、调度、原生Chat、错误重试及管理UI时适用。仍需遵守provider-onboarding与frontend合同；组织余额未绑定不算完整号池门禁全部完成。

## 2. Signatures
- `config.ProviderDeploy="deploy"`; 保留默认分组`deploy`。
- `Config.ForProvider("deploy")`, `RulesFor`, `RuntimeBase`, `NativeBindingSource`；`Deploy.EffectiveRouting/Quota/Pricing`。
- `GET/PUT /api/settings?provider=deploy`：顶层routing/quota/pricing/errorRules/retryRules，deploy对象含baseUrl/modelSyncSec/detailLog。
- 复用`/api/accounts` CRUD/import、`/api/accounts/{id}/test`、`/refresh-quota`，以及client/import Key管理。
- 下游仅`GET /v1/models`、`POST /v1/chat/completions`。其他协议/方法/子路径本地拒绝。
- master同时保留既有`/api/deploy/*`注册链路，注册设置与`/api/settings?provider=deploy`推理路由设置分开；不自动触发注册或支付。前端`isDeploy`与`DR_PAGES`只在app.js声明，deploy.js复用共享api的管理员/CSRF边界。Deploy标签唯一，注册页深链仅允许本池并负责初始化事件，离开本池停止轮询。

## 3. Contracts
### 凭据与配置
Opaque Secret必须显式声明，不是access_key，不因重叠前缀重归其他池。下游/导入密钥、保留分组和Deploy控制台批量导入不能跨池；API根默认`https://api.deploy.com/api/v1`，校验HTTP(S)、无userinfo/query/fragment、以/v1结尾。无凭据重定向、无代理失败转直连。

Deploy独立Routing/Quota/ModelSyncSec/DetailLog/Pricing/ErrorRules/RetryRules。缺失路由/轮询使用缓存的不可变Deploy基线，不继承其他池或在每账号热路径重建全量默认规则。空规则列表是真正清空。全局日志容量/保留只读共享；本池保存不触碰全局Upstream/Logs/OpenCode/Pricing。可配置scope仅account/model，未证实共享付费来源时禁止funding。刷新动作是目录刷新而非余额刷新。

### 目录、额度与调度
目录快照绑定provider/完整Key/Base/代理哈希；来源变化先失效，锁内再次核对写入来源，重启metadata亦核对。失败保留旧目录/时间并标失败；仅来源匹配、最近成功且未过期的目录可过滤模型。不参与OpenDesign funding分类或额度判断。周期检查并发独立；modelSyncSec=0关闭周期目录HTTP，首次/手动仍读。

目录200不证明Key已active或推理授权。当前推理Secret无官方余额查询契约，组织credit需另绑portal会话，本次未绑定。账号`deploy.{quotaState:"not-connected",quotaReason,catalogState,catalogFresh,catalogAt,catalogError,models}`；quotaFresh=false。手动刷新成功返回`{ok:true,catalogRefreshed:true,quotaRefreshed:false,message,diagnosis}`；失败不能toast额度已刷新。不从目录、促销、402/405伪造金额，也不多Key累计共享组织余额。

### 推理、错误与日志
原生Chat/SSE不重编码、不别名化模型、不删除工具、不补假reasoning或缓存读数。默认测试模型`deepseek-ai/deepseek-v4.1-flash-1017`，保留手填；真实测试max_tokens=256含reasoning，content=null不等同鉴权失败。

405精确组织暂停文案独立识别，可能涉及余额/条款，不证明封禁或零余额。429遵守完整秒数/日期Retry-After，冷却和停止换号均生效。Deploy的stop优先于错误规则的换号建议，但错误动作仍执行；其他池维持原语义。未知发送结果/已提交流输出不能被自定义规则绕过；截断流不计成功、不伪造DONE、不重放。

本池详情默认关闭且不受global detail影响；复用有界native隐私预览，Key/Cookie/会话头体/响应回显脱敏。Deploy成功完成与失败尝试统一采用每体独立固定5120KiB采集预算（不受共享`logs.detailBodyKB`旧值压低）（最大5MiB，超限明确标记截断；原始输入超过8MiB仍沿隐私扫描保护整体省略），不扩大其他池的64KiB上限，不修改全局配置或自动开启详情；非2xx上游错误响应仍沿原64KiB读取边界，不为日志额外排空上游。普通摘要与各失败尝试保留；成功重试前的失败可关联详情，末次失败不重复写。管理员完整Key查看沿用既有admin/no-store/CSRF和退出清理，不新增会话迁移。

## 4. Validation & Error Matrix
| 条件 | 结果 |
|---|---|
| 非法base/sync、混池分组、funding scope | 拒绝，旧内存和文件不变 |
| 外池导入Key或Deploy批量行试图跨池 | 拒绝，不创建账号 |
| 目录失败/过期/缺字段/晚返回 | 保留历史并标未知/失败；旧来源不发布 |
| 目录成功、无portal会话 | catalogFresh可真，quotaFresh仍假，余额未知 |
| 401 | 短暂账号冷却，可在总尝试预算内换号，不永久隔离新Key |
| 402/405精确暂停 | 冷却，405默认停止换号；下游503，不自动付款 |
| 403/429 | 错误动作执行且默认停止换号；429完整Retry-After |
| 已发送网络不确定/流输出后失败/截断 | 不重放，不记假成功或造DONE |
| 未登录/缺CSRF | 401/403，零写入/上游动作 |

## 5. Good / Base / Bad Cases
Good：Secret明确加入deploy组，用专属下游Key请求Chat；目录和真实推理分别验证，余额未绑会话明确未知。
Base：旧配置无deploy字段，Deploy使用独立基线且详情默认关，不影响旧池。
Bad：把目录200当Key激活、用注册赠金填余额、把405组织暂停当方法错误、删除工具后偷偷重试，或用funding作用域套OpenDesign计费分类。

## 6. Tests Required
- `internal/config/deploy_test.go`：Key形状/显式归属、独立默认与保存克隆、空规则、保留分组、base/sync/funding校验。
- `internal/rules/deploy_test.go`：401/402/403/405/429/5xx匹配、generic405不误判、完整48h Retry-After。
- `internal/quota/deploy*_test.go`：成功/失败保历史、JSON往返、sync0/手动强制、来源迟到/重定向、独立并发预算。
- `internal/pool/deploy_test.go`：来源/过期/错误目录门禁，无OpenDesign funding污染。
- `internal/server/deploy_test.go`：原生字节/工具/大整数/Key隔离、路径本地拒绝、规则实际换号与stop、配置磁盘与跨池、导入/auth/CSRF、默认模型与未知余额、详情脱敏/流截断/未知结果不重放/凭据不重定向。
- `internal/server/deploy_detail_limit_test.go`：1.37MB成功/失败正文完整保留、5MiB边界/超限标记、响应采集预算、共享限额64/128KiB不压低本池预算、详情关闭、凭据与会话脱敏及TH/Mistral仍64KiB；不得用截断预览冒充完整失败请求。
- `node internal/server/deploy_web_test.js`与真实本地浏览器：入口可见，设置/规则保存刷新核对API/磁盘，未知与错误转义，目录不冒充额度，Mock/真实小额推理严格区分。部署需另行授权。

## 7. Wrong vs Correct
Wrong：错误规则冷却后无视Deploy stop轮换共享组织Key；目录200后显示余额0或Key验证成功。
Correct：执行冷却但停止当前请求换号；余额仍未知，目录只作发现，授权以本次真实推理为证，错误/历史值明确标注。
