# 只改Mistral号池：有界逻辑布局恢复（2026-11-11）

## 授权与交付口径

用户在确认原生text→thinking→text经两个scalar字段往返会丢布局后，明确要求修复，并明确「不允许客户端配合，只要求改号池」，随后要求继续。沿已有in_progress任务，不新建任务/客户端carrier，不改NewAPI或用户客户端。开发阶段只本地实现/验证，不包含生产推理、真实正文采集/重放、线上配置修改或部署；用户随后明确「部署」，已按文末追记上线46a9b8f，不能沿用原未部署状态。

## 核心实现

- `internal/server/mistral_layout.go`：独立domain-separated摘要作用域、完整两字符串HMAC匹配、immutable布局描述、LRU/字节/TTL限制、已知歧义明确拒绝、派生checkpoint签名/严格校验/深拷贝、原历史匹配和UTF-8完整分片重建。
- `mistral_layout_capture.go`：JSON/SSE当前原生事件只累计摘要/长度，4choices上限；跨帧续片合为逻辑块，不留正文。转换成功后观察；JSON完整或SSE finish_reason/DONE在终止数据对客户端可见前入状态，避免下一轮发起时还没记录。失败/未完成流不建立完整可恢复布局。
- `proxy.go`：只在三精确推理模型Chat、原identity解析之后恢复；原生thinking请求不经过布局猜测，其他message/root/工具/图片/大数保持。技术头和requestLog只含恢复/未知/原生/捕获计数。已知过期/超限/歧义本地400，零上游/零健康处罚。没有重算/替换路由HMAC，不改选号粘性/缓存/重试。
- `server.go`：可选`metadata.mistralLayouts`，沿原分钟合并和正常退出强刷；signed元数据只含类型/长度/closed/空signature枚举、私有摘要和过期时间，不保存输入/输出/思考/工具文本、原始身份或凭据。写失败沿metaDirty重试，崩溃可能丢末次未刷记录。

## 不能过度承诺的边界

1. 只恢复同scope、完整文本精确匹配、有效且没有已知歧义的已捕获布局。旧/外部/改字或丢思考历史、无可用identity、被有限LRU回收的记录不能逆推，沿旧兼容并显式legacyUnknown；不能说所有旧会话已经无损修好。
2. 无客户端响应标识时，未观察到的同文不同布局在信息上无法检测；scope+全文摘要不是不可伪造的原响应归属证明。同scope内一旦观察到两套布局，拒绝last-wins。
3. SSE恢复逻辑内容顺序、固定元数据；不恢复网络分帧、原JSON属性次序/转义/排版或上游未公开的内部块边界。不能将本地往返成功说成实际token前缀/生产缓存命中保证。
4. 8192条、序列化估算16MiB、单条32KiB/512块和分段、4choice、可恢复24h/命中续期，均不是全进程RSS或永久历史保留认证。到期元数据可能作为不可用标记保留在有界LRU，不承诺24h销毁。
5. 新增派生checkpoint字段不改核心存储版本，但旧strict reader不一定接受。后续发布需完整备份及同UID断网兼容启动；降级仅移除此派生字段，不能覆盖上线后新业务data。

## 红绿与验证

隔离源码为干净线上00ba86a加本轮6个Go文件，`proxy.go/server.go`只apply本轮diff；没有复制主线已提交未上线的ModelStudio，也没有其他窗口注册/指纹/README/Dockerfile脏修改。

- 有效红回归：三模型×JSON/SSE真实本地HTTP往返，旧实现均得到thinking→合并text，server1.776s/exit1。red.log/red-fixture.log为合成客户端decoder误把finish_reason当对象的校准失败，已修fixture，不冒充根因；有效证据red-verified.log。
- 最终定向：`go test ./internal/server -run 'Test(Mistral|.*(Metadata|Checkpoint|Drain|Binding|Identity|Native))' -count=1`，**14.196s/exit0**。
- 最终race：`go test -race ./internal/server -run 'TestMistral(Layout|ContentStreamFlush|ContentResponseAndUnsupported|ConversionUsesOriginal|.*HMAC|NativeThought)' -count=1`，**4.794s/exit0**。
- `go vet ./internal/server`、`go build -o /dev/null ./cmd/odb` exit0；gofmt/diff check通过。不重复无关全仓、Node/UI或生产推理。

新回归覆盖：三个模型JSON/SSE→客户端只回传两个scalar→正式本地HTTP；text/thinking交错/空白/UTF-8/part/closed/null signature，工具/schema顺序/大数保持，增长历史旧恢复字节一致、显式及fallback HMAC/X-Affinity不变；真实Server重建后的HTTP恢复/secret保持/不含原文checkpoint、异密钥/篡改拒绝；24h续期/已知过期、LRU/字节/节点上限、万帧纯文本有界、同文不同布局本地400零attempt、native权威与legacyUnknown、非空引用/未知字段/转义重复不被消费、失败流不建checkpoint/首帧即时flush、12并发capture/get/export。

## 用户授权OVH发布追记（UTC17:03，46a9b8f）

- 主线6894f95已推送；独立Git index从实际线上00ba86a仅合入本轮16文件，6个Go文件逐字节等于前述干净隔离测试源码，release/mistral-layout-20261111的46a9b8f1f27c40e237d4701e0685c8fb3838cc5d已推送上线。没有重复测试/带入ModelStudio或其他窗口脏修改。
- 637文件SHA256集合验证，单次预检healthy0/双入口/Healthcheck别名/241.85GiB/注册空闲，构建UTC17:02:19.570–27.154（7.58秒）。新持久化实际断网预检17:02:27.888–28.592，沿线上UID1000:1000/network none、RAM/stdin→tmpfs配置副本和签名合成记录；health200/config与routingSecret保持/checkpoint签名往返/正常退出0。预检容器apb-layout-preflight-20261111t170102z已stopped/PID0/exit0，tmpfs释放，无推理或宿主凭据文件。
- 旧退出17:02:31.269，完整一致备份17:02:31.623–36.871，新启动37.213、首HTTP38.358、healthy17:03:07.590，NewAPI到两个内网healthz07.731通过，立即反馈。191账号/186TH注册/routingSecret/config字节/Mistral注册库/原13硬隔离与运行参数保持，Compose仅image覆盖且诊断保持空。
- UTC17:03:49.070真实未登录公网登录页/healthz/四资源200和SHA256匹配；专用浏览器已关闭。17:05:06一次自然流量复核23成功、2图片超限400、无新422/布局不可用错误；17可恢复捕获、checkpoint20条（17可恢复/3unknown）签名及字段白名单通过。**restored0，生产真实匹配恢复尚未出现**，不把采集/持久化生效冒充实际生产往返或缓存收益验证。未追加付费推理/等待采样或新配置/重启。
- 正式release、镜像、完整一致备份、SHA256与分阶段时间见docs/ovh-deployment.md当前记录。旧strict reader降级先备份最新data，仅移除metadata.mistralLayouts派生字段，不能覆盖新业务写入。Trellis与正式回退资料保留，临时产物经同意再清理；补记录不再次部署。

## Bug analysis / 防复发

- 类别B/D/E：跨层scalar协议本身丢布局，原无状态helper不可能单独恢复；不能继续靠改变前置顺序或稳定HMAC来声称解决。
- 新增独立spec `backend/mistral-layout.md`，保留旧helper characterization，另以真实HTTP恢复测试验收新有状态层，防止用helper自测掩盖端到端丢序。
- 不引入明文缓存/客户端注解/伪造签名/自动上游重放，记录与日志明确unknown和unavailable，核心缓存任务不归档。临时日志/副本已登记_tmp_artifacts.md，未经同意不清理；Trellis保留。
