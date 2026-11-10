# 2026-11-10 Mistral1910/3505精确默认规则

## 范围
用户同意近期错误与GLM缓存排查建议后，先本地修复1910模型资格与3505服务容量分类。沿`mistral-cache-order-audit`，不新建任务；核心GLM低缓存/无损布局仍未完成，不归档。不部署、不改生产配置/账号、不主动推理/重放或续开诊断。既有注册/指纹/Dockerfile改动保持原状。

## 实现
- 403+顶层code1910：默认account×model冷却24h、原MaxAccounts上限内换号、最终503、不扣账号全局健康，保留其他模型与既有Recover。
- 429+顶层code3505：默认ignore+stop，不扫账号、不扣健康，保留429和原Retry-After（秒/HTTP日期）；缺失不捏造1秒恢复，独立错误码mistral_backend_out_of_capacity。无全局模型停用或新调度器。
- 响应特例依赖实际Mistral匹配后的request-local标记，防止其他池的同名自定义rule ID触发本池行为。
- 只识别两个完整旧出厂列表/nil进行升级，旧列表JSON冻结在迁移回归；管理员自定义/顺序/备注/禁用/空列表不变。线上已有自定义403/429，发布代码不自动替换或启用新策略，逐项配置变更另须确认。

## 验证
干净d986d4b源码副本，仅覆盖本轮7个实现/测试文件；第一次red.log有测试符号拼写校准错误，修正后red-verified.log为预期行为红：1910最终502不换号、3505换号两次、错误Retry-After/迁移缺失。

最终：internal/rules全包0.675秒、internal/config全包1.083秒；server相关normal1.112秒/race1.906秒；定向vet、cmd构建及gofmt/diff检查通过。覆盖同模型排除/其他模型复用/Recover/上下游503、容量单attempt与下一请求仍可用、秒/日期/缺失hint、普通403/429保留、保存重载自定义策略、其他池与同名rule ID隔离。

开发阶段无全仓重复测试、Node、真实推理或线上配置写入。`.analysis/mistral-policy-20261110/`源码副本/脱敏回执登记`_tmp_artifacts.md`待同意清理，Trellis保留。

## 用户随后授权提交推送部署OVH（UTC09:38–09:43）

f1024e9已推送master并上线；复用既有控制器和已通过回归，620个干净提交文件SHA256集合核验，未混入其他窗口修改。构建09:38:05.417–12.511，旧退出12.704、完整一致备份12.983–18.619，新启动18.917、首HTTP20.033、Docker healthy49.268、NewAPI双内网入口49.414、本机资源49.418。191账号/186TH注册/routingSecret/Mistral注册及运行参数保留，原config字节切换阶段不变。

09:41:07正常admin API仅前置1910/3505错误规则及3505重试stop，revision5672；旧自定义规则原序、其他配置/凭据逐字段相等，原13隔离未恢复，保存/GET/落盘一致，已logout。09:42:15.926真实未登录公网healthz/登录及四资源均200且SHA256匹配；09:43:01最终确认f1024e9 healthy/0重启/无OOM、旧隔离保留，已立即反馈。短窗口无新1910/3505自然错误，不冒充真实重放或缓存改善；不续采、不生成、不额外等10分钟。专用浏览器已关闭。

正式release `/home/ubuntu/project/account-pool-balancer-releases/20261110T093736Z-f1024e9`、受限备份`/home/ubuntu/backups/account-pool-balancer/20261110T093736Z-tier-capacity-f1024e9/`及新旧镜像保留；完整备份SHA256和细节见docs/ovh-deployment.md。回退先确认新写入及策略与旧代码兼容，不自动覆盖data。仅补记录，不再重启。
