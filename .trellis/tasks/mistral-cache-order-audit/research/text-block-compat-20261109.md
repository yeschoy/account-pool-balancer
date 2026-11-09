# DeepSeek Chat空文本块与跨协议缓存提示422（2026-11-09）

用户在400修复部署过程中连续要求继续、急需。b5d0dbb已于UTC12:09:50上线，12:10:20双内网/健康/资产验收；12:20:47真实公网登录页/healthz/四资源200且SHA256匹配，备份deploy.json已留回执。关闭旧诊断环境变量、191账号/186TH核心注册/完整config及Mistral注册库保留。上线后截至UTC12:20:19渠道180完成121条：102成功200、16上游422、2官方503、1客户端499，**无400**；不能将其他状态冒充全成功。

## 新422证据

UTC12:18后同渠道180的失败原生loc/type：
- body.messages.110.assistant.content.[list].0.text.text / missing。
- body.messages.111.user.content.[list].1.text.cache_control / extra_forbidden。

45秒有界自然形态采样未捕获目标新请求，正常退出，无推理/重放/正文保存。随后仅在远端RAM解析已有精确上游422的input结构，既不输出text也不遍历其他用户正文：
- req_c969bd3c9ddef6f4ddc8b241与req_4c6256c5969f6e6598affdbe：missing的input字典仅有type键，值为text，完全没有text字段，即空槽`{"type":"text"}`。
- 多余cache_control的input精确为单type键的ephemeral对象，没有ttl或未知额外键。
- 当前NewAPI DTO的文本字段为omitempty，可解释空字符串为什么在中转序列化后变为缺字段；未对生产NewAPI完整二进制作源码证明，不把该线索写成已最终追责。

## 最小兼容边界

仅精确deepseek-v4-1-flash-1017的/v1/chat/completions，user/assistant内容数组内text块：
1. 只消费已证实的标准`cache_control:{"type":"ephemeral"}`性能提示；Mistral沿原prompt_cache_key链路缓存，**不声称保留Anthropic分块/TTL语义**。有ttl、未知结构或其他字段的hint保留原生，不静默丢弃；其他号池/模型/协议不变。
2. assistant中仅type=text的空槽补text=""，而非删除块/消息；包含未知字段的不伪造正文，已有text（含空/null）不覆盖。
3. 同一消息/块重复消费字段在本地拒绝；原始字符串、图片、工具、全部历史/顺序、原数字及无关字段按RawMessage线缆拼接保留。不得删图/丢思考/重试重放来掩盖422。
4. 精确红/绿回归后直接部署，沿既有干净提交/一致备份/Compose镜像覆盖/双入口检查；不带其他窗口容量/注册未完成代码，不新增诊断服务，不发生产推理。

## 必要定向验证

- 新增本地回归先在b5d0dbb业务源码上精确复现标准hint未适配，0.341秒；再完成最小适配。
- 干净提交源码隔离副本中一次DeepSeekTextBlock/Reasoning/Image/CacheHeaders/Prefix相关回归通过，1.764秒。覆盖第110/111条迟到消息、完整历史/真实文本/图片/工具/大数保留、空槽补齐、标准hint消费、TTL/未知hint保留、GLM/其他模型/Responses/FIM隔离及本地HTTP mock实际出站；无生产推理、无全仓/race/压测。
- gofmt及本轮精确路径diff检查通过，工作树其他容量/注册文件未参与编译/打包。

## 已提交、隔离发布与验收

- 主线业务提交fbee8e9已推送；其他窗口并发提交容量改造后，当前HEAD相对b5d0dbb多出35个业务路径。没有把主线HEAD直接部署：独立Git index从b5d0dbb仅合入本轮5文件补丁，发布提交72d1e42114f89238d972f53f07899de731622aef已推送release/mistral-text-compat-20261109分支，不重写主线/工作树。发布相对b5d0dbb仅3个业务文件变化，正是隔离回归验证的请求适配/新实现/测试。
- UTC12:58:48.845启动，构建7.31秒；12:59:19 Docker healthy/零重启、双内网入口、576个源码文件SHA256集合与四资源、本地data/config/注册资产验收通过，立即告知用户。191账号/186TH核心记录/完整config与Mistral注册库保留，诊断仍关闭。
- UTC13:00:13.510真实未登录公网登录页/healthz/四资源均200，原?v查询保留且SHA256逐项匹配发布源码，回执写正式备份deploy.json；专用浏览器已关闭。正式release、旧镜像、完整一致备份和精确阶段时间见docs/ovh-deployment.md。
- 截至12:59:50上线后目标渠道首批完成6条自然请求均200，其中1条先429后沿原规则恢复；未主动生成/重放。这不是完整10分钟验收，也未逐条恢复原失败私密payload冒称原样重放已通过。

这修复已证实的两个请求格式问题，不承诺原GLM188缓存掉零、原生web_search或官方503均已解决。
