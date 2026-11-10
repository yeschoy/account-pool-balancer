# Mistral真实出站Reader、HTTP头及完整usage：6轮200K Flash取证

## 结论

用户明确同意最小诊断发布/短重启及最多6次合成请求。补丁`92cc0d7024c49c6da80c55a1a7b6a1e148b93e39`已提交推送并在OVH运行，健康/双内网/公网验收通过。使用独立临时下游Key、临时隔离一个可用CHAT账号，经**正式号池实际转发路径**完成6轮新合成长会话，input224081→229637，每轮≥200K。

**这次不是离线prepare推断：6/6真实HTTP Body读取完整、HMAC等于prepare和入站body，5/5相邻prepared和attempt旧消息完整保留、未重排，单attempt/单次成功写入，稳定缓存键/实际X-Affinity、同源/目标/账号/代理，且6轮复用同一条HTTP/2连接（ALPN h2）。缓存仍前4轮全部0，第5/6轮98.8244%/97.8240%。** 原生usage、下游SSE、号池计量逐项一致，官方Vibe预算净减USD0.15117091与官方计价1:1。

原始上游头已获取：仅设置Cloudflare `__cf_bm` Cookie，没有观察到亲和回显、GPU/region/worker头或额外路由Cookie，头采集无截断；完整usage中也未返回service_tier/节点信息。**没有发现某个必须回传而号池漏回传的缓存路由字段，也没有发现请求转换破坏前缀。** 不加Cookie也可高命中，不支持“漏回传Cookie导致无法缓存”的必要条件解释。更像上游缓存选取/就绪/逐出或内部请求构造，但不能从外部HTTP连接证明内部GPU/缓存节点一致，根因仍待官方trace定位。

测试已停止，专用Key禁用、账号原分组恢复、管理员logout、控制器exit0/stderr0/进程退出，Docker healthy/restart0/无OOM，双内网入口复验通过。未启用区域/Priority/PAYG/全池正文详情、不继续旧checkpoint、不重放失败、未采其他Key正文。核心缓存任务仍in_progress；诊断上线不等于缓存修复。

## 1. 新诊断和最小发布

### 实现

复用既有`ODB_MISTRAL_CACHE_AUDIT_KEY_ID`，默认空、单Key/每进程30分钟128请求，以及原Body/GetBody增量HMAC/写入/连接/前缀诊断。只为该Key的预算内attempt增加：

- 实际`httptrace.WroteHeaderField`所见请求头（有observed标记），与既有wire detail钩子组合；保存快照防晚回调修改。
- 原生响应头、实际HTTP协议与TLS ALPN。
- 最后原生usage对象；未知数字/bool/null/嵌套计数/精确大数保留，不改普通统计或响应正文。
- 请求/响应最多64头、每头4值、可读值512字节；普通技术头白名单可见，未知/会话/亲和值仅进程随机HMAC，认证/Cookie值恒掩码，最多16个Cookie名称。
- usage最多8KiB/256节点/8层/64对象条目或数组项；未知字符串（除standard/priority/USD/EUR）脱敏、不安全字段名指纹化，sanitized/truncated明确，不保存半截原文或任何正文/思考。
- 无Cookie jar、额外HTTP请求/读取、SSE全程缓存、选号/重试/TTL或缓存键策略变化。

文件：`mistral_cache_audit_http.go`、`mistral_cache_audit_http_test.go`、`mistral_cache_audit_transport.go`和`proxy.go`，文档/规格同步。没有打包其他窗口注册/指纹/Dockerfile未提交改动。

### 本地验证

在干净HEAD隔离副本仅覆盖本轮四源码/测试文件，全部Mistral定向normal11.671秒、cacheAudit/Wire/完整前缀/原生思考相关race8.143秒、server vet和cmd构建通过。覆盖HTTP/JSON/SSE/HTTP2/首帧立即flush、完整usage未知数值/精确大数、detail trace组合、Cookie/认证/未知值隐私、header数量/长度/depth/usage边界、晚回调快照和关闭零分配。初轮失败定位并修正测试fixture自己的分配和mock Host原文泄漏，后完整相关重验通过。不跑无关全仓或Node（没有UI修改）。

### 生产发布

新建临时下游Key `ck_058a943b722e55f52cf86276`，组`mistral-http-audit-20261110`，创建时为空组、无账号，不移动业务或原自测Key。发布仅启动变量指定此Key，保留所有配置/账号/注册/网络/资源及旧镜像和一致备份。运行revision92cc0d7，镜像`account-pool-balancer:release-92cc0d7-20261110T143926Z`，正式来源629文件SHA256集合核验。

- 构建UTC14:39:50.951–14:39:58.300（7.35秒）。
- 停止请求14:39:58.401，旧进程退出14:40:00.272。
- 完整一致备份14:40:00.558–14:40:05.505，gzip及SHA256核验。
- 新进程14:40:05.765，首HTTP14:40:06.882，Docker healthy14:40:36.113。
- NewAPI双内网入口14:40:36.255，公网真实未登录页/healthz/四资源200，SHA256与提交一致，专用浏览器关闭。
- 191账号、186TH注册、routingSecret、Mistral注册资产、原13硬隔离和运行配置保留。未恢复隔离、未改管理员规则。运行环境唯一授权差异为诊断Key变量。

旧退出至首HTTP观测约6.61秒，不将构建/健康等待或全操作时间称为停机时间，也不冒称精确用户可见停机。

正式来源：`/home/ubuntu/project/account-pool-balancer-releases/20261110T143926Z-92cc0d7`；备份`/home/ubuntu/backups/account-pool-balancer/20261110T143926Z-http-audit-92cc0d7`，旧镜像`account-pool-balancer:rollback-20261110T143926Z`保留。备份SHA256 `1c93d7f6ad03ff49370a25c3a06de9a18c11ed430b7e3afe460fbc7c2419ee8c`。回退不得覆盖发布后新写入，且应显式清空诊断变量、不续采。

## 2. 合成实验及实际线缆证据

正式新会话，GLM5.3 Flash/high/1536，先将`acc_78068235bc580c4f6eeb8bdb`临时移入专组，active/pending归零与Vibe预算连续3次稳定后才开始；原自测/业务Key未移动。3200条公开合成系统设计要求，逐轮追加真实回答/思考和10条新要求，无tools/图片，非真实用户历史。新Session/body prompt_cache_key/X-Affinity稳定一致，轮间3秒另含只读预算查询。

| 轮次 | input | cached | 命中率 | 秒 | 官方计价USD |
|---:|---:|---:|---:|---:|---:|
| 1 | 224081 | 0 | 0% | 25.20 | 0.03380915 |
| 2 | 225217 | 0 | 0% | 24.21 | 0.03392555 |
| 3 | 226266 | 0 | 0% | 31.29 | 0.03408540 |
| 4 | 227316 | 0 | 0% | 29.06 | 0.03430140 |
| 5 | 228478 | 225792 | 98.8244% | 7.87 | 0.00737416 |
| 6 | 229637 | 224640 | 97.8240% | 7.49 | 0.00767525 |

6轮全200/SSE DONE/自然stop，同账号/代理；后5次binding hit。总input1360995/cached450432/output2147。原完整13消息checkpoint、全部思考/回答哈希和末轮历史核验通过。

原生usage从真实上游SSE消费的data提取，未脱敏修改任何本次数值、无截断：

```json
{
  "prompt_tokens": 228478,
  "completion_tokens": 395,
  "total_tokens": 228873,
  "prompt_tokens_details": {"cached_tokens": 225792},
  "completion_tokens_details": {"reasoning_tokens": 84}
}
```

以上为第5轮原生usage。所有6轮usage字段集相同，**没有service_tier或节点/路由字段**。没有额外把reasoning_tokens加到completion计费。

实际线缆诊断全部满足：

- client→prepared完整body HMAC相同；prepared→attempt预期HMAC相同；真实Body读取HMAC与预期相同、字节完整，6/6均一次read实例、一次成功WroteRequest、0失败。比上一轮“离线prepare-only”更强，但仍不是证明每个TLS字节都被远端GPU消费。
- 相邻5组previousPrepared与attempt.previous均完整保留全部旧消息，MayOverlap=false，旧工具/root参数/顺序不变。
- cache字段指纹、实际X-Affinity所有值指纹稳定；实际header trace观测到了唯一X-Affinity值，没有被Transport漏掉或重复覆盖。
- source/target指纹各稳定，同账号与固定代理，没有区域/端点或认证来源漂移。
- 6次GotConn均对应同一个连接ID、Reused=true，HTTP/2.0、ALPN h2。它可能也承接同代理的配额读取等流，不代表本次实验专占该连接；不能将一个公网HTTP2连接等同一个内部推理节点。

## 3. 原生HTTP请求头与响应头

### 实际请求头名

```text
:authority :method :path :scheme
Accept Authorization Content-Length Content-Type Host
Session-Id Thread-Id User-Agent X-Affinity
```

Host/authority为`api.mistral.ai`，method POST、路径`/v1/chat/completions`；Authorization已掩码。session/cache/affinity实际值仅HMAC。没有向上游发送Cookie（也未配置Cookie jar）。6轮正文显式prompt_cache_key/high/max_tokens1536，无service_tier或区域参数。

### 实际响应头名（6轮完整集合一致，20个头、无截断）

```text
Access-Control-Allow-Origin
Alt-Svc
Cf-Cache-Status
Cf-Ray
Content-Type
Date
Mistral-Correlation-Id
Server
Set-Cookie
Strict-Transport-Security
X-Content-Type-Options
X-Envoy-Upstream-Service-Time
X-Kong-Proxy-Latency
X-Kong-Request-Id
X-Kong-Upstream-Latency
X-Max-Retry-Attempts-Reached
X-Ratelimit-Limit-Req-Minute
X-Ratelimit-Limit-Tokens-Minute
X-Ratelimit-Remaining-Req-Minute
X-Ratelimit-Remaining-Tokens-Minute
```

关键解读：

1. **唯一Cookie名`__cf_bm`，值未保存。** [Cloudflare官方](https://developers.cloudflare.com/fundamentals/reference/policies-compliances/cloudflare-cookies/)说明用于Bot Management/Bot Fight Mode、机器人评分，并非官方说明的GPU缓存亲和Cookie。本次没有回传该Cookie，仍在第5/6轮高命中，因此它不是本实验“可以命中缓存”的必要条件。未做携带/不携带Cookie的额外A/B，不能声称对概率绝无影响，也不擅自增加Cookie回传/跨号共享。
2. **`CF-Cache-Status: DYNAMIC`六轮均有，包括高命中轮。** [Cloudflare官方](https://developers.cloudflare.com/cache/concepts/cache-responses/)定义为HTTP资源不适合CDN缓存、直接到origin；不是LLM KV cache命中标识。不能把DYNAMIC解读成Mistral关闭prompt cache。
3. `Server=cloudflare`、6个`CF-Ray`后缀均`YYZ`。这是Cloudflare边缘关联信息，不是GPU区域/worker；边缘相同也不证明内部推理路由相同。
4. **没有返回X-Affinity回显、X-Cache/Age/Via、X-Region/worker/节点ID或额外路由Cookie。** 此结论仅针对本次无截断的6个原始响应，不外推所有Mistral接口/时间。
5. **`X-Max-Retry-Attempts-Reached`确实存在**，但最小诊断把非白名单值指纹化。6轮值的同域HMAC完全相同，包含零缓存与高命中轮；没有读取原始值，未找到该Mistral头的可靠公开语义，不能断言它为true、内部重试耗尽或换了worker。保留作为官方支持查询线索，不凭名字实现重试/回传。
6. X-Kong/Envoy延时是真实上游技术头：前4轮X-Kong-Upstream-Latency约17.7–18.2秒，后2轮5.655/3.987秒；与首部/上游处理等待有关，不冒充GPU计算时间。不能将这些响应诊断头盲目回传为下一轮请求参数。
7. 限流头完整名称已保留、值以HMAC记录；没有429/Retry-After，不能把缓存0归因为确定触发限流。

### 官方追踪ID

| 轮次 | Mistral-Correlation-Id / X-Kong-Request-Id |
|---:|---|
| 1 | 01a1c5ea-0a30-7983-a97d-39d30faeca72 |
| 2 | 01a1c5ea-7c1d-74f4-af77-7271a9ab4e38 |
| 3 | 01a1c5ea-e95f-778a-9de1-d36eae37eb33 |
| 4 | 01a1c5eb-724d-723e-b6fd-09f49656e2e4 |
| 5 | 01a1c5eb-f2e6-7aed-a90e-d0def569f5bf |
| 6 | 01a1c5ec-21f8-7d17-9e78-51995cd36886 |

两种头同轮相同，均来自真实上游，不是猜造。可向Mistral支持提交：同Key/显式稳定亲和/完整相同旧前缀、同公网HTTP2连接，为什么连续4轮0、随后恢复，以及未知重试头的精确含义。未主动提交外部工单或上传正文/凭据。

## 4. 扣费、恢复、隔离与停止

- 官方模型价input/cached/output = USD0.15/0.03/0.50每百万token。6轮usage计价USD0.15117091；对应CHAT/Vibe预算净减0.1511709100000020（浮点百分比尾差），至8位小数相同，无现金扣款测量或Priority倍率。
- 预算窗口该账号仅本实验6条，完整诊断采集目标Key6条、其他Key0条，不开启全池detail或其他Key采样。
- UTC14:46:07.669禁用专用Key，14:46:07.692账号原组恢复，14:46:07.710管理员logout，控制器exit0/stderr0。复用控制器旧字段`target_key_fields_restored=false`/`all_config_except_revision_matches_initial=false`来自**有意将新专用Key enabled true→false**，不是遗留凭据/组损坏。独立验证将这两个授权字段复原后相关配置哈希与实验基线相同，账号全部原字段相同。
- UTC14:48:23独立核验进程已退出、healthz/Docker healthy/restart0/无OOM，NewAPI双入口再次通过；检查点13消息/思考哈希、权限0700/0600、无已知认证数据、diagnostic无raw session/凭据/Cookie值。
- 初步收尾曾仅禁用Key，后发现既有`newMistralCacheAudit`启动校验要求目标Key启用，仅留禁用KeyID会导致下次进程重启失败，不能把这种状态作为最终交付。已主动告知用户并复用发布引擎做**必要的诊断关闭切换**：同一镜像不重建、旧进程干净退出/完整一致备份、Compose仅清空该环境变量，未重新采样或追加推理。UTC14:56:54.934新进程启动、14:57:25.298 healthy、双内网14:57:25.435；旧退出至首HTTP观测约6.63秒。14:58:01最终核验变量为空、专用Key禁用/组无账号、所有配置仅本轮Key禁用/revision变化，13硬隔离不变、healthy0重启/无OOM。公网healthz再次200，诊断完全关闭，下次启动不依赖已禁用Key。
- 关闭切换备份`/home/ubuntu/backups/account-pool-balancer/20261110T145641Z-http-audit-off-92cc0d7`（SHA256`ee6cbc5fc435a69a12e5010fabf299c4df20580df341dcb58188bcd0744da819`）及旧镜像rollback-20261110T145641Z保留；同一release新增`deploy-diagnostic-off.override.json`是当前Compose最后覆盖。后续普通发布仍须显式保持诊断变量空，不得重新启用测试Key/无授权续采。如需进一步实验须新授权，不擅自追加超过6次。

## 5. 产物和剩余边界

`.analysis/mistral-http-audit-20261110/`含隔离源码副本、targeted/race/vet-build日志、rollout/run/verify控制器、test-key-created/deploy/公网与最终验收/6轮result/verification/billing-summary/restore-manifest及公开合成dialogue。远端`/tmp/apb-mistral-http-audit-20261110/`另保留0700/0600合成checkpoint，不下载/回显思考或认证。指定无敏感文件用SSH压缩流下载，未落本地归档。已登记`_tmp_artifacts.md`，未经用户同意不清理，Trellis和正式发布/备份/旧镜像保留。

**目前最强结论**：没有观察到转换/上传正文/亲和头/账号或外部HTTP连接漂移，仍存在长前缀缓存未命中；没有发现遗漏的必要缓存路由返回参数。怀疑范围优先上游内部缓存选取/缓存可见性/逐出或服务端内部前缀构造，HTTP证据不足以区分具体机制或保证永不掉零。响应scalar适配对任意原生交错块/closed元数据的保真边界仍未解决，本次普通思考→正文、无tools合成对话不能外推所有真实用户链路。
