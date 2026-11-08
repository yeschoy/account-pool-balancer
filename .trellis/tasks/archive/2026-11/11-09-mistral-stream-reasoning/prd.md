# Mistral Responses 结束判断、三模型思考档位与兼容回归

## 目标与授权
用户已批准实施前轮建议1～3：修复Responses结束判断；补齐多模型/工具调用回归并仅按证据做最小适配；修正过时UI/文档。最新明确要求覆盖DeepSeek V4.1 Flash 1017、GLM5.3、GLM5.3 Flash的low/high/max，未传档位默认high，显式max不再降为high。本轮不推送、不部署、不主动发真实推理或进行付费操作。

## 已确认背景
- `internal/server/proxy.go:1922,1982` 在首事件及后续EOF分支直接返回成功，首帧终止事件没有及时结束；现有`TestMistralResponsesSSE`仅覆盖正常非空输出。
- `internal/server/mistral_request.go:165,308` 仅为精确DeepSeek Chat适配根reasoning/思考开关，且旧策略将max降为high；DeepSeek其他store/图片/developer/seed兼容有独立生产证据，不应无条件扩散到GLM/Large。
- `docs/ovh-deployment.md`已有Mistral上游ID `zai-glm-5-3` / `zai-glm-5-3-flash`；官方reasoning公开文档核实GLM两型号原生low/high/max，Large4为low/medium/high。
- README及app.js仍有“余额未知/只同步目录”过时介绍；套餐预算、失败历史值和未知钱包实际已有实现。

## 要求与验收
1. Mistral Responses SSE首帧或后续合法completed/incomplete立即结束，不等待上游关连接；空输出和纯工具调用合法。EOF缺少协议终止事件必须记录失败；已提交HTTP保持真实状态，不伪造completed/DONE或usage，不重放/换号。
2. 在现有Mistral原生协议边界内，三模型支持low/high/max并默认high；保留已有不支持档位/null归一high规则及显式冲突/重复字段本地拒绝。根reasoning别名按协议处理，保留工具/正文/metadata/缓存/数值；只将已确认共同的思考控制适配扩展到GLM，DeepSeek专属图片/store/developer/seed规则仍精确限定。Large4和其他模型保持原生参数行为。
3. 模型矩阵覆盖多轮思考历史、工具结果/Deny/apply_patch多行与转义、流式/非流式、缓存字段及跨池/模型/路径隔离；不虚称真实客户端或上游推理验收。
4. 更新UI/README/Mistral指南和规格：预算不等于钱包，失败/过期显示未知及历史值，共享组织额度不可简单累加；明确Mock与真实客户端验收边界。
5. 受影响回归、全仓normal/race/vet/build、Node语法/行为通过，UI文案使用隔离假数据本地浏览器检查；不引入npm产物，临时文件登记后待用户同意清理。

## 不在范围
不新增模型别名配置、逐模型后台设置、新协议转换、调度器、缓存优化、普通绑定持久化；不覆盖线上配置、重放历史请求、注册/付款或生产压测。Trellis业务记录需保留并随代码提交。
