# Mistral 3亿TPM：完整链路容量审计

日期：2026-11-09。代码基线：`7c5e30b`及当时工作树；注册机/Dockerfile已有其他未提交修改，本轮未覆盖。任务：`.trellis/tasks/11-09-mistral-capacity/`。

**结论：不能通过放大4096、连接数或账号数就宣称支持3亿TPM。现有代码有可量化的调度、统计内存和请求体保护短板。优先改进单实例热路径和资源保护，不预设必须上Redis/多副本。**

**当前状态：正式代码及本地验收完成，已于2026-11-09 13:07 UTC部署c1aebbc并通过健康/双内网入口/公网资源验收；线上配置保留、未发真实推理，尚未认证3亿TPM。** 用户已确认“整个Mistral池保留的会话绑定条数”，不是每账号会话数。

第1–7节保留首轮**改造前审计快照**（当时只有研究/隔离原型，行号亦为旧基线）；正式实现、配置及改后数据见第8节，不能把历史“尚未实现”当作当前状态。

## 1. 容量口径与物理预算

暂按 `总TPM = prompt_tokens（包含cached_tokens）+ completion_tokens` 建模。不能再次加cached_tokens，也不能把缓存计费折扣当作网络字节或TPM折扣；最终口径须由用户确认。

```text
目标 = 300,000,000 tokens/min = 5,000,000 tokens/s
请求率 λ = 5,000,000 / 平均每请求总token
在途数 C ≈ λ × 平均请求时长（Little定律）
新绑定数 B ≈ 每秒新会话数 × 实际滑动TTL；容量另留余量
```

以下请求时长60秒仅为预算假设，不是实测：

| 平均输入 / 输出 | 目标请求率 | 60秒平均时长对应在途 |
| --- | ---: | ---: |
| 8,000 / 2,000 | 500 RPS / 30,000 RPM | 30,000 |
| 98,000 / 2,000 | 50 RPS / 3,000 RPM | 3,000 |
| 998,000 / 2,000 | 5 RPS / 300 RPM | 300 |

- 近似4字节/输入token、输入占比98%时，单跳输入约19.6 MB/s（157 Mbps）；JSON、转义、图片、协议和TLS另算，必须实际测bytes/token。入口→NewAPI→Balancer→固定代理→Mistral各段都承受对应字节，缓存命中仍发送完整历史。
- 同上假设、请求保留60秒，**仅一份输入字节就约1.18 GB**；原始/适配后正文、解析对象、网络缓冲、日志和NewAPI另外占用，不能拿1.18GB直接当容器内存需求。
- 30,000条流的64KiB上游读取buffer，单这一项约1.83GiB；3,000条约187.5MiB。尚不包含goroutine、channel内事件、HTTP2窗口和请求正文。
- 若目标实际指纯输出3亿TPM，则输出5百万token/s；假设单流60token/s，需要约83,333个同时解码的流，完全是另一档上游与网关需求。不能与长输入含缓存TPM混为一谈。
- 示例：50 RPS、10%是新会话、30分钟TTL，需要约9,000条普通绑定；旧4096会在TTL前淘汰。取值应来自新会话率而不是并发数。

## 2. 调用链与证据边界

```text
客户端（稳定Session/完整历史）
 → Nginx/TLS
 → NewAPI：鉴权/渠道选择/预算 → 请求转换/重序列化 → HTTP连接池
 → Balancer handleProxy：下游Key/分组 → 有界单请求读取
 → JSON路由字段/会话HMAC → Mistral参数与缓存键适配
 → Pool.Acquire：可用性/健康/绑定/策略 → 并发与RPM租约
 → transport(proxyURL) → 账号固定代理 → Mistral组织/模型限额
 → 原生SSE逐事件读取、兼容、usage提取、即时Flush
 → 释放租约 → 健康/Token/请求统计 → 日志队列
 → 5秒计数保存、15秒元数据检查、通常1分钟采样合并保存
 → NewAPI流解析/usage和计费 → Nginx → 客户端
```

### 入口与NewAPI

参考相邻NewAPI源码提交 `3914bf70209ecae0fe305ba6a7cdc9bad3fa0949`，**未证明它就是线上镜像源码**；未读取或修改该仓库凭据/工作树文件：

- `../new-api/controller/relay.go:123,159,177`：请求校验、计费准备和渠道重试环。重试预算必须与Balancer账号尝试上限联动，避免故障时多层放大；不能假设Balancer阻止重放就意味着外层也不会重发。
- `../new-api/relay/compatible_handler.go:25,33,96`：普通Chat会DeepCopy DTO；body透传分支仍在此之前做DeepCopy，非透传还会转换/Marshal/参数覆盖。因此开启透传不是“全链路零拷贝”，亦不能不顾模型映射/协议需求直接全局开启。
- `../new-api/common/body_storage.go:47`：有共享内存/磁盘请求体storage；必须同时观测NewAPI buffer、解码历史、出站正文和长流持有对象，不能只看Balancer RSS。
- `../new-api/service/http_client.go:77`：共享Transport、HTTP2、响应头超时已有。`RELAY_MAX_IDLE_CONNS_PER_HOST`是**空闲连接数**，不是同等在途并发硬上限。
- `../new-api/relay/channel/openai/relay-openai.go:105`：流维护响应文本Builder供usage兜底，并逐帧处理工具/终态；不能把此层当透明TCP转发。上游真实usage存在时，可研究更轻的计数路径，但不能破坏计费/工具统计或丢输出。
- `../new-api/relay/helper/stream_scanner.go:28,75`：已有逐次写deadline，属于已完成保护，不应重复声称NewAPI没有；Balancer对应保护仍不足。
- Nginx现行worker_connections、nofile、request/response buffering、下划线会话头、超时、磁盘临时body、NewAPI实际版本/SQL/Redis池和主机资源仍需运行时合并预检，本轮不把历史部署文档当现行实测。SSE响应缓冲不应吞首帧，不能随意关闭所有请求缓冲而放大慢上传风险。

### Balancer核心

| 环节 | 代码锚点 | 现状 |
| --- | --- | --- |
| 入口鉴权/读取/准备 | `internal/server/proxy.go:395,590,613,722` | 一个不可变Config快照；Mistral单请求限体积，但没有总buffer预算 |
| 路由JSON/身份 | `internal/server/identity.go:325,327,397` | 严格完整JSON校验；路由只解码必要字段及前导消息，但找不到system时会继续解码历史 |
| 精确协议适配 | `internal/server/mistral_request.go:33,156,187` | 原字节拼接、保留未知字段/大数；多次扫描相同正文 |
| 选择/租约 | `internal/pool/pool.go:755,988,1064,1100` | 一个进程内全池互斥锁，选号/准入/MarkSent/Release原子 |
| 出站连接 | `internal/server/server.go:411` | 按proxyURL复用Transport、HTTP2；固定代理不降级直连 |
| SSE | `internal/server/proxy.go:1739,1763,1845,1947` | 64KiB Reader、16个事件channel、单事件32MiB上限；逐帧Flush |
| 日志/统计 | `internal/logstore/logstore.go:166,215`，`internal/stats/stats.go:68,135,153` | JSON编码/内存入环在调用协程，磁盘队列8192；统计为固定分钟数组 |
| 元数据 | `internal/server/server.go:130,340`，`internal/pool/pool.go:1384` | 合并持久化已有；绑定/统计导出仍在各自锁内遍历 |

## 3. 本地测量：确定的瓶颈

环境：Apple M4，darwin/arm64，Go1.26.8。无生产凭据，合成账号/正文/usage，所有网络请求仅本地HTTP Mock。基准夹具在任务`research/*.go.txt`，通过Go overlay载入，未进入生产编译路径。完整命令/原始输出见任务`research.md`及`research/`。

### 3.1 命中已有会话仍扫描全池

`tryLocked`先遍历账号、校验目录来源和健康，之后才尝试普通Mistral binding；只有TokenHarbor有owner-only快路径。健康检查每账号扫描1440分钟槽。冷选号进一步构造Seat并排序。

下表是 `Acquire + MarkSent + Release`，GOMAXPROCS=8、8个合成worker、smart、已有新鲜额度、开启健康过滤；B/op是**累计分配量，不是每请求常驻内存/RSS**：

| 同组账号数 | 已确认绑定命中 ns/op | 分配 B/op |
| ---: | ---: | ---: |
| 40 | 64,923（64.9µs） | 78,802 |
| 400 | 567,034（0.567ms） | 629,775 |
| 4,000 | 6,171,776（6.17ms） | 8,408,273 |

4000账号下无绑定的冷选择约8.56ms、10.85MB/op。所有账号都是本池同组；混池时全局扫描仍在，但只有同组走完整候选检查。

- CPU profile（另一次3秒有profile开销运行）`healthLocked`占flat约43.9%；不能把它说成所有场景的固定占比。
- alloc profile约83.4%采样空间来自`tryLocked`候选slice构建/增长（复制较大的Account+Seat），约11.4%累计来自反复计算`ResponseBindingSource`。
- 锁profile约99.9%累计阻塞延迟归在Acquire持锁路径；**这是各goroutine累计等待，不是墙钟时间或停机时间**。
- 增加CPU核数不能解决同一把锁内的全量扫描。

**隔离实验**：只在`.analysis/`副本中，让有效Mistral sticky hit先校验绑定owner，其余路径回退原实现。40/400/4000账号分别约3.82/6.95/48.24µs；4000账号比基线约128倍，分配约3.74KB/op。实验overlay的Pool全部normal/race测试通过。

这只证明owner-only方向有效，**不是已实现/已部署的128倍端到端提升**。实验仍两次线性`Config.Account`查询，并改变候选数诊断投影；尚需不可变ID/分组索引、完整busy/overflow与配置变更测试、server回归和审阅，不能直接当正式补丁发布。

### 3.2 长历史重复扫描与不必要解码

合成128条字符串历史、真实会话头、DeepSeek原生参数，测完整路由解析→别名检查→身份→准备→最终X-Affinity：

| 正文目标尺寸 | 含system的ns/op | 不含system的ns/op |
| ---: | ---: | ---: |
| 128KiB | 1.65ms | 2.57ms |
| 512KiB | 5.08ms | 10.36ms |
| 2MiB | 19.51ms | 35.84ms |

- `leadingItems`需找到system和user才停止；没有system时会解码全部历史，即使明确会话头使message_hmac完全不需要。
- 512KiB含system的profile中，`skipString`约60.5% flat，`json.checkValid`约7.0% flat；可见多次扫描是实开销，不只是推测JSON反序列化慢。
- 建议一次严格校验后复用顶层offset/模型/cache key，并仅在真的需要fallback时解析前导历史；现有兼容校验仍须遍历所有相关messages，不能借性能优化删历史、思考、图片、工具或重复字段校验。
- 原生SSE适配器已有0分配快路径；但连同`sseFields/error/usage`仍约528ns、144B/帧、2分配，尚未包含`readEvent`/Flush/socket。不要拿单个适配函数的0分配宣传整个转发链路0分配。

### 3.3 全量详情与脱敏缓存

合成约134KiB请求，16个普通SSE帧+usage+DONE，8并行、每格200请求，含Handler/真实本地HTTP上游/统计/日志，**不含前端TCP、TLS、NewAPI、Nginx、真实模型耗时和周期元数据保存**：

| 账号 | 详情 | 平均墙钟ns/op | P95 / P99 | 分配B/op |
| ---: | --- | ---: | ---: | ---: |
| 40 | 关 | 0.301ms | 3.21 / 5.86ms | 836,412 |
| 40 | 开 | 1.204ms | 13.53 / 14.82ms | 4,406,070 |
| 400 | 关 | 0.393ms | 4.16 / 6.49ms | 1,430,196 |
| 400 | 开 | 1.639ms | 17.71 / 19.53ms | 6,151,121 |

注意：ns/op为并行吞吐的摊销时间，**不是单请求延迟**；P95/P99才是逐请求本地测量，200条只是短基准，不是稳定尾延迟/持续容量证书。冷统计分配也包含在B/op内。四组日志队列未丢记录，不代表长时间也不会丢。

- `newMistralUpstreamDetail`/`finish`会多次预览和脱敏；预览已限制单段64KiB，且`thBodyPreview`已是扫描器而非整棵JSON树解码，不应重复提议“先实现这些已有保护”。但有界保存不等于扫描/拷贝CPU成本免费。
- `ForProvider`每请求创建新Config视图指针，而`accountReplacer`只按Config指针缓存一次。合成4000账号、同一17KiB文本：稳定视图约126µs/37KB，fresh视图约830µs/1.09MB。建议使用稳定不可变配置版本缓存脱敏器，且有界失效，**不能关闭凭据脱敏**。
- 已有详情开关是用户授权的诊断能力。建议新增可选按Key/时间/失败/比例采样，或经用户同意关闭全量详情；本轮没有擅自关闭线上开关。
- 日志单写队列8192按条而非字节限额；极端详情可占大量排队内存，维护/查询与写锁也可能影响Append。应有字节预算、队列深度/丢弃速率指标和有界采样，不能只调大队列。

### 3.4 统计内存随活跃账号线性膨胀

`Stats`同时写全局scope和provider scope；每个曾产生请求的账号分配两份`[1440]Bucket`。当前`Bucket`为208字节，**每活跃账号599,040字节（约0.571MiB）**，即使只记录过一分钟也分配完整数组：

| 活跃账号 | 仅账号两份统计数组 |
| ---: | ---: |
| 40 | 约22.85MiB |
| 400 | 约228.52MiB |
| 4,000 | 约2.23GiB |
| 10,000 | 约5.58GiB |

这还没有算健康数组、模型/代理scope、绑定、正文、日志及Go运行时。配置了4000账号但仅少数产生请求时，不会立刻为全部4000账号分配Stats数组。

建议稀疏/按需分钟槽、已过24h空闲数据回收，并避免all/provider重复存同一账号桶；保持24h统计、缓存率清空和重启语义。Summary/Export持统计锁扫描全数组，也会停住请求完成记账，应缓存短周期概览并缩短导出持锁。

## 4. 其余必须处理的资源/调度边界

| 优先级 | 问题/证据 | 建议及约束 |
| --- | --- | --- |
| P0 | Mistral在`proxy.go:590`走`io.ReadAll`，只有单请求体积上限；总保留字节/上传/待选队列无限额 | 复用/泛化现有`tokenharbor_buffer.go:31`的预算机制：已知长度一次分配、chunked保守预留、读取deadline、满额本地429且零上游尝试；覆盖原始/适配后正文和响应内存，不只是裸输入。GOMEMLIMIT是软限额不是OOM保护 |
| P0 | `Acquire`等待没有队列长度预算；`Release`每次关闭全池changed channel | 有界准入/等待数，按池/组定向唤醒，保留取消和RPM预约原子性；防止一次释放唤醒全部其他池等待者再全扫描（源码风险，本轮未做等待风暴注入） |
| P0 | Balancer写SSE没有逐次write deadline，入口只有ReadHeaderTimeout | 为每次Write/Flush设置可续期写deadline；阻塞写时同协程idle select不能执行，不能认为取消上游足以释放慢读客户端。禁止全局短WriteTimeout杀正常长流。此风险本轮未以慢读socket复现 |
| P1 | `transport()`只读根`cfg.Routing`，cache只按proxyURL；Mistral独立routing中的连接参数不会影响实际Transport；UI没有连接参数入口 | 明确连接参数是共享还是本池独立；若独立，key纳入有效连接策略、配置变更安全换代/关旧idle池，UI往返验证。默认直连512/代理32不是全局并发目标，也不能将HTTP2连接数等同流并发 |
| P1 | 非流式成功响应`LimitReader(...,512MiB)`后ReadAll，无总响应预算；单SSE事件32MiB、16事件队列按条限 | 响应/事件大小与聚合字节预算、慢下游背压和取消释放；超限明确失败、不截断成成功、不伪造usage/终态。不能直接缩小上限破坏合法工具/多模态负载 |
| P1 | 全局共享4096 LRU；扩大后`Views/Export/Import/Reconcile`线性甚至绑定×账号/Key查找 | 可配置容量与隔离配额、ID/Key索引、每账号计数、过期/LRU指标；锁内复制必要快照，锁外序列化/I/O。不能直接取可变binding指针到锁外读而引入data race |
| P1 | 本地只有每账号并发/RPM，无组织/模型TPM、RPS维度容量表达 | 先核对组织真实授权限额；共享组织的多Key不能相加。有需要再增加明确来源的组织/模型速率预算与平滑，保留未知而不伪造额度；不把金额预算当TPM |
| P2 | 绑定淘汰/删除标dirty会触发较通常一分钟更频繁的metadata保存；大LRU/统计的整快照成本 | 先测元数据序列化/落盘/导出锁，再按证据做分批快照或独立增量日志；不是一开始就新建数据库 |
| P2 | 入口、Transport排队、选号等待、写阻塞混在TTFT/总耗时 | 有界标签的分阶段时延、GetConn/GotConn、HTTP版本/复用、活跃流/等待/字节预算、GC/RSS、日志深度/丢弃、绑定命中/淘汰、上游429分类与尝试放大指标；不能用原始session作metric标签 |

**已有且应保留**：原子不可变Config快照；热选号用`quota.View`而不是每次深拷贝目录；元数据采样合并；原生SSE快路径与逐事件Flush；固定代理、禁凭据重定向；MarkSent后的不确定网络失败不重放；Mistral无状态Responses终态校验；稳定Session/cache key与原字节前缀。不能将这些已完成项重复包装成新优化。

## 5. 自定义绑定会话数：必须先确认语义

现状：`maxBindings=4096`在`pool.go:144`，`BindingLimit()`只读此常量；配置、设置API和UI均没有绑定容量字段。前端展示“全局4096”是事实，不是可配置入口。

以下是两种不同需求，不能混淆，更不等于maxConcurrent：

1. **池保留绑定容量**：例如Mistral最多保留100,000条session→account偏好。到容量后按LRU/过期策略处理；影响TTL内稳定路由、缓存与持久化规模。
2. **每账号可绑定会话数**：例如每账号最多绑定50个不同会话。只约束新绑定，已有会话仍允许回原号；可能让仍有推理并发/RPM余量的账号不能接新会话。

**推荐**：先做Mistral独立可配置总绑定容量（与其他池、严格Responses owner隔离），如确需限制“每号带多少会话”，再加独立默认不限制的每账号字段。字段名/范围/默认值待用户确认，不在本轮偷偷改变现有4096或既有淘汰行为。

实现契约（无论采用哪个方案）：

- Config→Normalize→管理员settings读写/Revision→文件→重载→运行时→概览→UI完整往返；非法值保存失败且不损坏当前配置。
- 旧配置省略字段保持兼容；显式0含义必须清晰，不可同时代表默认/无限/关闭。高容量必须有合理硬上限与内存测量，不提供“无限制”按钮。
- 缩容不终止在途租约、不静默换严格续传owner；普通绑定LRU移除走统一dirty路径，不在下次保存/重启复活。严格owner缺失继续fail-closed。
- 每账号计数（若需要）在新绑定预约时与并发/RPM同锁更新；MarkSent确认、未发送撤销、过期、淘汰、删除/换Key/Base/代理、禁下游Key、重载/恢复都保持一致。不能通过每次扫描全部绑定求计数。
- 会话HMAC仍按分组/下游Key隔离；同值cache提示不必然是唯一会话，稳定显式Session更可靠。
- 既有普通绑定源校验、绝对到期、显式/弱TTL、正常退出flush保持；崩溃仍不承诺最近一分钟零丢失。
- 独立容量必须同步说明全局/本池/每号当前数与上限、过期/淘汰数；不能只改后端常量或加不生效的输入框。

## 6. 分阶段实施与验收

### 第一阶段：低风险收益与安全边界

1. 不可变账号/分组/下游Key/source索引；Mistral绑定owner优先；候选轻量索引；健康滚动总和/短期缓存，保留阈值语义。
2. Mistral总正文/响应预算、上传和逐次写deadline、有界等待/定向唤醒；错误清晰、取消归还、上游零误重放。
3. 用户确认后的绑定容量全链路；稳定配置版本脱敏缓存、可选详情采样；必要指标。

### 第二阶段：大账号/长历史扩展

4. 按需统计分钟桶/去重复存、Summary/Export降锁；共享JSON扫描结果、延迟fallback解析；常规SSE字段零拷贝解析但仍逐事件Flush。
5. 明确并修复连接设置作用域与Transport缓存/重载；实测固定代理和HTTP1.1/HTTP2实际可用流量，不照抄“连接数=并发”。
6. 测绑定恢复、metadata体积/周期写盘/队列/故障路径，必要时再做快照或增量持久化。

### 第三阶段：容量认证（另行确认工作负载/成本）

- 先本地/隔离环境**开环**固定到达率压测，不能仅用闭环8并行短Mock当生产容量：5/50/500 RPS，对应真实正文bytes、短/长输出事件率和60/180秒持流；多模型/图片/工具/无system历史、真实session/new-session混合。
- 账号40/400/4000，绑定4K/10K/100K以及用户实际目标，详情按生产选择；覆盖周期metadata保存、UI刷新、队列满、慢上传/读、取消、代理失败、429/503和下游多层重试。
- 用真实传输字节/SSE帧验证CPU、RSS/heap/GC、P95/P99、排队长度、成功率和已测usage，不通过伪造usage制造3亿TPM成绩。建议稳态至少30分钟并记录GC与多次保存周期，最终SLO/余量由用户确认。
- 再做已授权、有总次数/Token/费用硬上限的真实上游分档测试，区分Mistral原生直连、Balancer、完整NewAPI/Nginx路径开销；不得在生产突然冲到目标或反复重放失败请求。
- 单实例到达资源上限后再考虑多实例。当前租约/RPM/LRU/计数仅进程内，共享同一个data目录多副本不安全；新增实例必须明确账号分片/会话一致路由或共享准入状态和持久化所有权，不能直接`replicas: N`。

## 7. 官方约束与未验证项

本轮通过公开官方文档原文核实：

- [Usage and limits](https://docs.mistral.ai/admin/billing-usage/usage-limits)：Organization按模型有TPM和RPS限制；更高限额需向支持提供目标RPS、TPM/月量和用例。增加同组织Key不自动增加组织容量。
- [Prompt caching](https://docs.mistral.ai/inference/conversations/advanced/prompt-caching)：`prompt_cache_key`只提高命中概率，不保证命中；prompt_tokens包含缓存输入；缓存输入按正常价10%计费、缓存块64token。**该文档没有明确承诺缓存token不计入TPM**，不能依据价格折扣作此假设。X-Affinity在本仓已有行为保留，但本轮未从该官方页面确认其公开容量保证。

尚未核验：线上当前镜像/机器核数和容器实际内存设置、入口带宽/代理吞吐/HTTP2流上限、全部账号的组织归属和真实模型限额、真实客户端输入/输出/时长分布、持续3亿TPM。仓库Compose的8GiB/GOMEMLIMIT6GiB/nofile131072只是模板默认，不能当作线上已生效。

## 8. 已实施与本地验证（部署见第9节）

用户批准直接串行实施；子agent缺失chord依赖后没有独立agent代码/审阅产出。主会话完成实现与检查，不把工具启动失败算作审阅。未改注册机/Dockerfile等其他窗口工作。

### 8.1 自定义会话容量与设置

`mistral.bindingCapacity`：省略/0保持原共享4096；1–1,000,000启用整个Mistral池独立普通会话LRU。设置→保存→配置文件→启动恢复→运行时→概览已打通。缩容淘汰偏好而非取消在途lease；回到共享预算先裁剪Mistral，不挤出其他池的严格owner。保留MarkSent确认、TTL、source校验、采样保存和退出强刷；并发命中未确认偏好时任一真实发送者可确认，未发送创建者不能再撤销已确认绑定。

`mistral.performance` 非nil必须是完整有效对象；API省略保留旧值，旧配置nil采用下列默认：

| 字段 | 默认 | 范围 / 语义 |
| --- | ---: | --- |
| maxBufferedMB | 2048 | 1–65536 MiB；原始/改写正文、非流响应、排队SSE原事件共享；再收紧到Go软内存限额1/3；不是进程RSS硬上限 |
| maxWaitingRequests | 1024 | 每组0–100000；0禁止新增排队；空闲请求仍能准入，需等待时本地429 |
| uploadTimeoutSec | 60 | 1–3600秒；读deadline，错误清理不能重新阻塞排空 |
| writeTimeoutSec | 30 | 1–600秒；逐次Write/Flush续期，不是长流总时长上限 |
| detailSamplePercent | 100 | 0–100；原详情开关内确定性采样；不关闭请求摘要/既有记录/独立cacheAudit |

Mistral独立路由的 `directMaxConns/proxyMaxConns` 已有可保存UI并接入专属Transport；默认512/32不变，配置换代仅关idle，不打断活跃请求，不允许代理失败降级直连。其他号池设置不被Mistral保存覆盖。

### 8.2 调用链改造

- 配置快照增加账号/组/下游Key/native-source索引；可变Update/Normalize先废弃旧索引，发布时重建。已有Mistral owner先过完整可用性/健康/源/并发/RPM检查；owner忙或无效仍走原等待/overflow/选号。
- 健康聚合分钟缓存；轻量账号候选；分组等待上限/定向唤醒；账号换组后旧lease释放同时唤醒当前组，旧Revision发布不回滚容量。普通独立/共享LRU分离，过期heap与账号绑定计数覆盖插删/恢复/迁移。
- 字节预算与真实上传/慢读socket deadline；取消/失败/响应超限释放原始、改写、响应和排队事件。响应超限不重放、不补usage/DONE，也不降级账号健康。SSE退出取消上游并等读goroutine结束后归还队列。
- 字符串扫描快路径、身份历史延迟解码、metadata仅投影身份标量、复用最终cache key、普通单data行SSE借用原字节；保持完整JSON校验及原生字段/大数/顺序/工具/图片/思考兼容。
- Stats改为按观测分钟稀疏槽及total缓存；单provider账号共享overall/provider桶，冲突ID拆分，保留24h/未知值/缓存率重置/持久化格式。日志磁盘队列保留8192条并增加默认64MiB字节预算，超限计dropped而非阻塞；概览显示字节/条数/丢弃。稳定账号切片+Revision复用脱敏器，不再因临时provider视图重建全账号替换器。

### 8.3 改后本地短基准

同一Apple M4，Go1.26.8；Pool cpu=8、200ms，完整lease周期。新夹具Normalize以包含实际发布快照索引，初始化不计时；旧版没有这些索引。Handler相同128KiB正文、16事件、本地HTTP上游、8并行、每格200请求；仍不包括NewAPI/Nginx/入口TCP/TLS/真实生成/周期保存。

| 指标 | 改前 | 改后 |
| --- | ---: | ---: |
| 4000账号/健康开启/已有绑定，摊销ns/op | 6,171,776 | 836.2 |
| 同场景分配B/op | 8,408,273 | 1,064 |
| 4000账号/健康开启/冷选号，摊销ns/op | 约8,560,000 | 1,736,794 |
| 400账号/详情关/Handler摊销ns/op | 393,419 | 163,174 |
| 同Handler P95 / P99 | 4.16 / 6.49ms | 2.101 / 2.718ms |
| 同Handler分配B/op | 1,430,196 | 559,790 |
| 400账号/详情开/Handler摊销ns/op | 1,639,002 | 677,997 |
| 2MiB无system/显式会话头/准备ns/op | 35,836,000 | 5,338,928 |
| 普通SSE字段+error/usage解析 | 528ns、144B、2分配 | 215.1ns、0B、0分配 |
| 4000账号/fresh provider view脱敏 | 830,049ns、1,090,312B | 64,901ns、39,240B |

4000账号各采一分钟的新Stats基准累计约4,023,604B/整轮（3.84MiB，不是RSS）；旧固定两份数组仅此项就约2.23GiB。不能把不同统计口径包装成精确RSS倍数。详情开启仍显著增加分配，不能声称采样解决了所有日志CPU。

**这些是局部或短Mock数据，不是数千倍端到端提升、5000RPS保证或3亿TPM证书。** 冷选号仍全量过滤/排序（4000账号本次约1.74ms）、首次无显式身份的fallback及协议扫描、24h持续稠密统计、超大绑定快照/导出和前置NewAPI仍需持续压测；本轮没有把这些说成已消除。

### 8.4 本地验收与剩余容量门禁

全仓 `go test ./...`、`go test -race ./...`、`go vet ./...`、构建、Node语法/全部行为套件通过。专项覆盖独立/共享LRU切换、缩容不取消lease、跨池/Go cache优先级、TTL/source/MarkSent、队列满/组唤醒、字节拒绝/取消/响应超限、不重放/不扣健康、真实上传与下游慢读socket、Transport换代和协议回归。

真实本地浏览器使用0账号隔离data：保存容量50000、预算128MiB、等待16、上传45秒、流写20秒、采样25%、连接7/3；页面读回、配置文件和进程重启后API/UI一致。OpenDesign仍512/32、根quota.enabled=true；概览明确显示“Mistral独立 0 / 50,000”和预算/日志队列。管理员已退出、专属浏览器与本地进程已停止；无真实模型请求。

**交付状态：接口/运行时/UI/持久化、本地验收及第9节部署验收已完成；未做真实3亿TPM验收。** 真实工作负载口径、上游组织/模型授权、CPU/RSS/GC/前置NewAPI/代理/网络、长流和周期保存、SLO及费用上限仍按第6节第三阶段另行确认。任务保留in_progress；不将功能/部署验收冒充容量认证。

## 9. 授权推送部署与清理

用户明确清理、提交推送部署并要求少做无用测试。已推送并部署`c1aebbc`，保留实际在线72d1e42的协议修复；复用已有测试，不重复全仓或发真实推理。191账号/186TH核心注册记录、完整config字节/Mistral注册库/运行参数保留，Docker healthy/restart0、NewAPI新旧双别名和真实未登录公网healthz/四静态资源SHA256验收通过。线上仍省略容量字段、沿用共享4096，未擅设新数值。构建/备份/切换/健康/公网时间与回退资料见`docs/ovh-deployment.md`当前部署节。

按用户同意清理约70.2MiB本轮合成研究/构建/假data、两个干净工作树和6个指定工具文件；Trellis与正式新旧镜像/备份保留，其他窗口文件未动。后续只补记录不重建/重启。
