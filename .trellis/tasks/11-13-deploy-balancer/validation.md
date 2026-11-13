# Deploy Balancer 验证与交付状态（2026-11-13）

## 实现与隔离
- 基线9410a52；独立分支feat/deploy-balancer，工作目录account-pool-balancer-deploy。原master既有未提交注册机/Mistral/Essevin等代码未搬迁、未重置、未暂存。
- 新增config.Deploy/ProviderDeploy、独立Routing/Quota/Pricing/目录间隔/详情开关及ErrorRules/RetryRules；复用原Pool、Config.Store、API和原生管理台，没有第二套调度器或前端构建系统。
- 账号/导入/客户端Key和保留组隔离；支持CRUD、启停、重复跳过、代理与并发/RPM/权重/优先级。日志保留/容量仍共享且Deploy页只读，本池保存不会覆盖其他池。

## 自动验证（最终代码均通过）
| 命令 | 结果 |
|---|---|
| gofmt（所有新增/修改Go文件） | 通过 |
| go test ./... | 全量通过 |
| go test -race ./... | 全量通过，含最终新增Deploy目录并发/流截断/不确定发送等回归 |
| go vet ./... | 通过 |
| go build -o .analysis/deploy/odb ./cmd/odb | 通过 |
| node --check internal/server/web/app.js | 通过 |
| 全部 internal/server/*_test.js | 12套通过，导航覆盖11个池 |
| git diff --check | 通过 |

覆盖：Opaque Key显式归属与非法形状、保留分组、配置克隆/磁盘往返、空规则不复活、非法base/sync/funding作用域；HTTP错误/正文/响应头与完整48h Retry-After；轮询独立并发、关闭周期/手动强制、目录失败保留历史、来源变化晚返回、禁止凭据重定向；不使用OpenDesign funding；原生Chat字节/工具/大整数不改、未知协议零上游、规则stop与冷却均生效、尝试上限；导入/CSRF/认证；Key/会话详情脱敏与独立开关；已输出/未知发送结果不重放、截断不造DONE且记失败。

本轮还修正了新增路径中的两处潜在继承问题：通用工具删除后重试分支必须排除Deploy；Deploy采用native隐私预览而非普通详情渲染。均有回归证明。

## 真实本地浏览器（Mock与真实凭据分开）
1. 使用隔离data目录和本地管理员，UI可见「Deploy号池」及完整账号/概览/路由/规则/日志入口。
2. Mock监听127.0.0.1:18792；UI保存weighted、TTL3600、X-Deploy-Session、最多账号3、目录检查120/并发3、连接10/4、详情开关。刷新页面后核对DOM、GET设置及data/config.json完全一致。global仍smart/60秒/detail=false。
3. UI编辑错误规则429冷却为120s，编辑重试规则备注，保存并刷新；API与磁盘确认reset/备注保留。恢复默认规则后再保存，未影响OpenDesign规则。
4. UI添加假Key账号，maxConcurrent2/maxRpm30/weight2；Mock目录新鲜2个模型而余额未知；免费检查只有目录、显式真实请求才多一次Mock Chat。Mock调用计数最终models4/chat1。
5. 切回官方API并关闭详情后才替换为用户授权测试Key；不把真实Key发往Mock。替换后旧来源目录失效，后台读取新目录。
6. 最终二进制重启后真实浏览器复核：目录检查/重试文案正确、本池价格两行、日志参数只读、钱包开关缺席；独立设置保留。概览为1账号、2次网关成功请求，分别是GLM/DeepSeek，其他池账号0。退出后账号缓存/DOM/modal凭据全部清空。

完整临时证据位于忽略的.analysis/deploy，非敏感业务结论保留本文件；不提交测试data/会话/Key。浏览器无生产站点管理动作。

## 用户授权真实接口验证
详见research.md：目录200，两个官方模型；管理端默认DeepSeek推理200。经本地Balancer的GLM非流式200、prompt16/completion68（reasoning65已包含），约1.6秒；DeepSeek流式200、8个data事件、reasoning保留、DONE完整，约1.1秒；均只尝试1账号，无自动重试或压测。客户端测试脚本从忽略配置读取Key，没有写死凭据。

## 分项交付（不能合并宣称“全部完成”）
- 接口证据：官方Quickstart/Errors/Models与真实目录/Chat已验证。
- 后端刷新：目录刷新、来源隔离、失败历史/metadata与手动刷新已接入；**组织金额刷新未接入**。
- 页面：账号管理、独立路由/规则/价格/详情开关、概览与未知余额原因已接入。
- 保存/刷新/重启：已通过UI/API/磁盘和最终服务重启复核。
- 测试：Go普通/竞态/vet/build、12套JS与浏览器通过。
- 提交/推送：功能3c6a87f及测试fixture隔离4c50007已推送feat/deploy-balancer；随后按用户要求合入本地master，保留主线1f0734f注册/支付流水线，合并版本未推送或部署。
- 部署：用户追加授权后，以实际线上b786d7c隔离发布c6ae4b2至OVH，保留配置/数据/双别名，Docker与NewAPI双内网入口及真实公网资源通过。完整10分钟自然观察为43次同版healthy0、44成功/1客户端取消/0最终失败，多个池无自然流量不冒称验收；直连外部渠道的400/429/504另列，详见deployment.md。未自动生产导入测试Key，不冒称生产Deploy推理渠道已验收。
- 完整号池门禁：**未全部通过**，余额/账单控制台会话尚未绑定；既有注册机和上游Key生命周期管理不在本次实现。保留任务未归档用于追踪缺项，不把未知金额当0或伪造完整交付。

临时服务与Mock验收后停止，文件登记_tmp_artifacts.md；未经用户同意不清理，Trellis永久保留。

## master 合并验证
- 合并父版本：master `1f0734f` + feat/deploy-balancer `fb82190`；保留主线注册机/API/存储/支付脚本与分支账号管理/原生Chat/路由/规则。没有删除任一链路，未执行注册、支付或真实上游请求。
- 自动文本合并无冲突，但出现共享classic script重复`isDeploy`/`DR_PAGES`和双Deploy标签；去重后新增同域脚本联合编译与唯一标签回归。注册API复用共享api，确保保存携带CSRF；注册页深链本池隔离并初始化事件。
- 合并工作区通过：`go test ./...`、`go test -race ./...`、`go vet ./...`、`go build`、全部Web JS语法与12套前端测试。Go合并导入保持gofmt。
- 隔离本地空data真实浏览器：Deploy仅一个标签，注册设置保存间隔11秒后刷新仍11；路由保存explicitTtlSec=2345/modelSyncSec=0，刷新后DOM/API/config.json一致。全局仍smart/TTL7200，零上游账号；注册配置与路由配置互不覆盖。其他池打开#dreg回到概览，无注册动作。
- 合并前13个已跟踪文件的未提交修改及10个未跟踪文件以摘要登记；跟踪修改先stash保护，提交合并后apply恢复并逐文件SHA256核对。stash与本地验收产物保留，未经用户同意不清理。
- 仅本地合并，不推送、不部署；原有余额/账单与生产Deploy渠道验收缺项不变。
