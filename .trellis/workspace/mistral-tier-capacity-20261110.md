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

无全仓重复测试、Node、真实推理或线上配置写入。`.analysis/mistral-policy-20261110/`源码副本/脱敏回执登记`_tmp_artifacts.md`待同意清理，Trellis保留。实际运行仍以最近只读核验的37332be为准，不把本地验证/提交冒充已上线或缓存改善。
