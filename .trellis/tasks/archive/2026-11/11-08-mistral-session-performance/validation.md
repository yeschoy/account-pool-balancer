# 验证与发布边界

## 实现 / 定向验收

- 新旧Codex头、Claude兼容头与metadata/root-user两种结构、缓存头fallback、下游Key隔离、隐私脱敏和无身份时HMAC均有定向用例；不会将普通user/UA/request-id当会话。
- 组织订阅与Key访问等级独立；可空PAYG显式false保留；403/302不破坏预算新鲜度，保留上次订阅并注明错误，深拷贝与JSON往返通过。禁止跟随携带凭据的重定向。
- 原有4096全局LRU不另建配置/调度器；新增可见上限/全局数量，修复淘汰持久化绑定未标dirty的漏洞；缺失的严格Responses绑定仍fail-closed。
- 精确503+顶层code3800停止无收益换号/不扣账号健康，429及其他5xx原策略保留；只有旧的完整出厂规则自动迁移，任何自定义规则/空列表不覆盖。
- 上游TPS仅使用最后实际请求的真实输出usage；流式post-first-event与非流式request-average有区别，失败/未知/零窗口不造值，旧日志显示未知。读事件时打点，不把最终下游flush计算进去；不是上游内部decode计时。
- normal定向：`go test ./internal/server ./internal/quota ./internal/pool ./internal/config ./internal/rules -run 'Test(Mistral|UpstreamTPS|BindingLimit|.*Identity|BodyCodes)' -count=1`，全部通过（server4.317s，其余<=2.535s）。
- race定向含TokenHarbor native/continuation/identity：相同包，`-run 'Test(Mistral|UpstreamTPS|BindingLimit|.*Identity|BodyCodes|TokenHarbor.*(Native|Continuation|Identity))'`，全部通过（server7.302s，其余<=4.055s）。
- 受影响五包go vet、build到/dev/null、gofmt、diff --check、Node Mistral行为和app.js语法均通过。没有全仓重跑，没有生产推理/重放/压测。

## 100ms短基准（Apple M4，单轮前后，相同fixture）

| fixture | 之前 | 之后 | 分配变化 |
|---|---|---|---|
| 原生字符串SSE帧 | 2613ns/op | 134.5ns/op | 2400B/46alloc → 0B/0alloc |
| 128条、每条约1KB文本块历史准备 | 801084ns/op | 273925ns/op | 379370B/3844alloc → 104033B/1558alloc |

仅代表网关局部样例，不是上游TPS/模型性能或端到端提速倍数。转义/空白/多行SSE/内容数组保守回退，已有推理/图片/工具/数字/协议隔离用例保持通过。

## 生产只读证据与限制

- 双跳内存形态采样各5条在3000/8790端口均无可识别头/正文字段，不能声称全部fallback来自Balancer漏头。
- 官方Codex当前发送session-id/thread-id；官方Vibe读取subscription plan_name及nullable vibe_payg_enabled。真实CHAT/API只读订阅均EDU，API whoami却Free，说明两种等级不可混用。
- 60分钟14994请求中13次429后换号恢复，8次最终503在16个服务错误尝试中均未恢复；仅优化已知code3800，其他结构不扩张规则匹配。
- Nginx仅两个NewAPI vhost加入underscores_in_headers on，备份20261108T152932Z-newapi-session-headers，nginx -t/reload一次；15:31:05窄GET合成探针证实Session_id与Session-Id均到NewAPI。首次宽探针不完整后先确认配置/active/HTTP200，再收窄采样，未重复修改/reload。
- 77e9709已推送并正式发布，487源码SHA256一致；healthy/restart0、原healthcheck/用户/环境/资源/两个别名保留，从new-api双healthz通过；191账号/186TH核心记录/config原字节/Mistral注册库不变。7.19秒构建，15:33:36本机验收；15:36:04公网登录/healthz/四资源200及SHA256匹配。正式备份20261108T153233Z-mistral-session-perf-77e9709保留。
- 15:36:09正常管理员API各刷新CHAT/API一把，均成功，40预算新鲜；概览global225/currentMistral209/limit4096，两条3800规则生效。15:38:07 metadata只读确认40把Key的订阅全部成功持久化：Student6/Pro34、零错误；同组织Key不当作独立额度累加。管理员密码/Cookie/CSRF未回显/落盘，已logout。
- 自然最新80条完成请求全部TPS有值（76 post-first-event、4 request-average），也全部message_hmac；不能声称fallback比例下降或凭空恢复未传身份。新显示通过Node数据/UI与生产API投影核验，未做真实生产管理员浏览器保存往返、Codex/Claude全客户端E2E或生产推理/重放/压测。公网未登录浏览器已关闭；上述边界不隐藏。
