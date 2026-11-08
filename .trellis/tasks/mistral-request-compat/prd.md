# Mistral 参数定向兼容

## 已确认范围
用户已批准方案、独立新工作区实施及修复后部署；不再启动代理。当前分支 `mistral-request-compat` 基线319c751包含8d41516；原master只读。

仅 Mistral Chat `deepseek-v4-1-flash-1017` 新增 developer→system、thinking/enable_thinking、seed、无意义默认及标记参数兼容。保留 medium→high、reasoning_content、逐事件流式兼容与store无状态保护；Responses/FIM/其他模型/供应商不扩展行为。

## 验收
- developer只改role，消息原序、内容、工具/ID及数值无损。
- thinking disabled→none；enabled保留low/high/xhigh/max（medium→high），无档位/null默认high。开关冲突、与档位冲突、未知结构/预算明确本地400、零上游尝试。
- seed按官方Chat random_seed整数schema无损映射；不覆盖显式random_seed，冲突拒绝。不以mock证明本DeepSeek型号支持或确定性。
- 保留prompt_cache_key/metadata原值及现有身份优先级；root user不参与会话身份。
- store false/null省略，true/非法/重复拒绝；无意义默认窄省略，无法兑现的生成/缓存语义明确拒绝。
- 定向mock与相关回归、gofmt、diff check、必要build；不跑重复全仓/race/vet、不批量生产推理。
- 干净提交部署通过后才收敛渠道180对应operations；最新读取、正式备份、仅更新param_override并发保护，不覆盖整行渠道。

## 不做
不改Nginx、smart、TTL、并发/限流/账号策略、不复制生产凭据或原始请求。
