# 50X验收记录（2026-11-16，本地）

## 证据分级

| 层级 | 状态 |
|---|---|
| 当前公开接口文档 | 真实本地浏览器打开quickstart/api-reference/balance-usage/pricing-credits，确认精确模型、Bearer、Chat/SSE/tools/models、余额/限流按账号共享、当前新账号无免费额度；公开价格需登录 |
| 历史鉴权接口证据 | docs/x50reg.md记载Token billing、6个真实账号及Chat；仅历史，不是本轮验证 |
| 本轮带凭据真实上游 / 付费推理 | 未执行，无可用业务凭据，不消耗生产费用 |
| 后端刷新/持久化/展示 | 已实现，本地mock验证成功/失败/强校验/旧值/来源变化/晚返回/重启 |
| UI保存重载 | 已完成真实浏览器操作并复核API/磁盘及服务重启 |
| Git提交 | 业务cd0a57c、合并最新master后2986f74已推送远端master；原master脏工作区未强制更新 |
| 部署 / 线上验收 | 后续用户明确授权，2986f74已部署OVH，Docker healthy/双NewAPI入口/公网7资源/数据保留验收通过；真实50X上游业务未复验，见docs/ovh-deployment.md |

## 自动化门禁

- gofmt（仅Go文件）；git diff --check。
- `go test ./...`：通过。
- `go test -race ./...`：通过。
- `go vet ./...`：通过。
- `go build ./cmd/odb`：通过。
- `node --check internal/server/web/app.js`、`node --check internal/server/web/x50.js`：通过。
- 全部15个 `internal/server/*web_test.js`：通过，含x50_pool_web_test。
- 具体最终命令结果在 `.tmp/x50/final-gate-summary.txt`（临时文件已登记，不提交）。

### 回归覆盖

config：Key推断/拒绝跨池、分组/client Key隔离、独立默认、配置JSON往返、空规则、非法base、session进来源哈希。
quota：billing有限十进制字符串/数字、缺字段/null/bool/对象/NaN/溢出/错误tier、Bearer vs Token、sync0/手动、失败历史、深拷贝、序列化重启、会话/代理换源迟到拒绝。
pool：paid/prepaid新鲜0阻断、免费层0不误判、过期/失败/旧来源不作耗尽、目录门禁、无外池funding。
rules/server：默认401/402/403/429/5xx、48h Retry-After；自定义状态+正文+响应头、stop执行/停用后有限换号与冷却；原生工具/大整数、精确默认模型与手填、不支持路径；已发送未知结果/截断SSE不重放不假成功；请求/错误/详情开关、凭据与会话脱敏。
registration/store：真实config写盘成功派生标志、重复幂等/混合失败、非法代理候选单项失败、写盘失败不误标、删除目标恢复可见再入池；注册存储重新打开与代理/密码/会话历史不变。
proxy：all/selected明确范围/确认、混池/非法代理原子拒绝、直连Transport.Proxy=nil不吃环境代理、其他账号/候选不变。
permissions：未登录401、缺CSRF403、管理员no-store沿用现有边界。
frontend：隐藏/勾选/选择批量、浮层开关/更新/角标、转义、切换后旧响应不发布、通用导航与独立参数保存。

## 真实本地浏览器（一次性数据 + 127.0.0.1 mock）

1. 服务127.0.0.1:18850，mock18851，均仅本地。OpenCode禁用，外池样例停用，无生产数据/真实Key；注册候选2个假Key，初始推理代理故意不可达。
2. 真实Chrome管理员初始化 → 50X注册机，默认未勾“已入池”。选中2个已完成候选 → 点击批量入池 → 原生confirm确认。
   - API后置：2个inPool=true、推理账号2个；页面默认只显示未入池候选。
   - 勾“已入池”显示已入池标识；重复导入added=0；删除一个目标后inPool=false、默认重新可见。
3. 账号页代理弹窗：scope=all（包含停用）、直连、确认修改50X。
   - API两账号direct=true，随后两次真实HTTP至mock刷新quotaRefreshed/catalogRefreshed=true，37.25余额。
   - 磁盘：50X推理代理空；Deploy外池代理保持127.0.0.1:9；两个注册候选代理仍127.0.0.1:9，凭据和历史未改。
4. 路由界面填写ExplicitTTL=1800、尝试2、开启本池详情 → 保存；错误规则UI编辑note，重试规则UI停用 → 保存。
   - GET与DOM复核TTL1800、详情true、note=browser-saved-x50-rule、retryDisabled=true；磁盘同值；停止/重启本地服务后相同，余额metadata恢复37.25。
5. 注册日志按钮：右下角展开/收起；用一次性候选导入产生真实本地服务日志，展开刷新显示“导入1个”，不用合成DOM日志。切Deploy注册页，Deploy“暂无日志”，50X页隐藏/浮层收起/轮询停止；切回后自己的日志仍独立。
   - 截图 `.tmp/x50/registration-log.png` 已经浏览器工具验证保存。
6. 账号真实测试接口指向mock：默认精确模型与手填模型推理均成功；创建一次性50X客户端Key通过真正/v1/chat/completions网关请求，status200、请求日志1条/详情1条且不泄漏下游Key，随后删除一次性客户端Key。无真实生产推理费用。
7. 浏览器截图、输出、mock脚本/数据/日志全部登记 `_tmp_artifacts.md`；服务/mock已SIGTERM停止，lsof确认18850/18851无监听，文件未经同意不删除。

## 上线后只读观察（用户追加授权）

- UTC 2026-11-15 22:20:28–22:23:28 连续180秒、15秒间隔13次采样：运行2986f74，全部healthy、零重启/无OOM，Healthcheck与兼容别名正常；结束双NewAPI入口均ok。
- 观察窗17个自然请求全部200成功：Deploy13、OpenCode Go2、OpenDesign1、River1；新增错误0，日志无非法JSON。
- 上线以来至22:23:28共147个完成请求：127成功、18个Deploy上游500最终失败、2个OpenCode Go客户端取消499；36条错误尝试均属上述18个Deploy失败（每请求2尝试，不能计成36次最终失败）。错误集中22:08:10–22:08:55，上游正文为53字节JSON `Internal Server Error`；相同模型/正文错误在部署前21:54:32已存在，非上线后才出现。观察窗Deploy自然成功13个，未主动发生产推理或重放用户请求。
- 部署前完整备份与当前metadata对照：4个OpenDesign上游明确封禁、95个TokenHarbor管理会话失效、1个TokenHarbor原注册会话/Key来源缺失均是原有状态。未清冷却/隔离、未重注册、未改代理/额度或伪造健康；会话/封禁需有效会话或上游处理，代码修补不能代替。
- 容器日志无panic/fatal/error/timeout；NewAPI近期日志无panic/DNS解析或连接拒绝，存在与上游错误对应的500及少量外部超时，不把其他渠道错误归给本服务。
- 生产50X推理账号仍0，无50X自然推理/billing样本，不冒称该池生产推理已实测。未发现可归因于本次实现的代码缺陷，未修改业务代码、在线配置或重新部署。
- 临时观察脚本和脱敏汇总在 `.tmp/x50-observe/`，已登记 `_tmp_artifacts.md`，无后台常驻观察进程，清理需同意。

## 已知限制（不能称为线上完整验收）

- 本轮没有真实50X管理会话/Key的billing与推理验收；历史成功不证明当前账号授权，公开目录也不证明。
- 免费层daily allowance无可靠可读合同，明确未知；不支持的协议已拒绝，非无效开关。
- 多Key共享额度，尚无稳定上游账号ID对账合同，保守不汇总、不声称跨Key独立额度或限流容量。
- 全局日志保留容量只读共享，50X请求/错误/详情采集可分别控制；注册环形日志内存400条，与Deploy行为一致，不声称重启保留它。
- 初始开发阶段未推送/部署/合并；后续用户授权后已完成远端master合并推送与OVH部署入口验收。仍未真实业务入池/重置生产代理/调用50X付费推理；不能把上线healthy冒充上游业务复验。
