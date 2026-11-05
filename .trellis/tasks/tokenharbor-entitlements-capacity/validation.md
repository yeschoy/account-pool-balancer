# 本地验收（2026-11-06；未推送、未部署）

## 功能与来源
- 使用保存会话及账号固定出口的只读实测确认付费周用量来自 GET `/api/me/free-tier`，不是钱包接口。Agent样本原始周额2.5，显示20/5仅为用户指定2×等效口径；不声称所有模型双倍促销。没有推理、隐私/付费POST或生产配置修改。
- 最终在业务网络命名空间用新 `Flow.Observe` 读取另一个有关联会话的账号成功：Agent Pass active、周已用0%、有效独立重置时间、overage_billing=false；官网映射29组，原生目录77项，确认Agent覆盖6项。该账号原配置为直连，不是对失败账号换出口。
- 同次检查发现另一账号固定代理端口20004连接拒绝（errno111），官网与目录均无法读取。已向用户说明，未改代理/防火墙/在线配置；该账号新策略下会未知停调度，部署前需核查此运营问题。诊断容器最后exited/0。
- 后台唯一原记录按accountId+完整Key+出口绑定；pricing真实当前套餐和模型分组与原生目录精确求交。未知、过期、钱包续费开启、周额耗尽均不调度；IgnoreCatalog不能绕过，未验证别名不放行。免费功能仍默认不启用。
- 原生请求字节不改写；完整JSON、非空/唯一/小写model校验防歧义。已有三协议、严格续传、忙绑定、来源/下游隔离、不可重放/消费确认测试继续通过。
- pi通用会话头与Codex/Claude/OpenCode字段夹具增加；不凭UA生成会话，不把每请求x-client-request-id泛化成会话。客户端不发送标识时无法承诺亲和。
- 各账号自己的百分比/重置参与汇总、原所有者去重，未测为未知，其他套餐不冒用Agent金额。禁用但仍已确认Agent保留总额，已知停调度状态不算可用周额。
- 错误按requestId+attemptIndex自动保存有界详情，后续成功不丢失败尝试；SSE错误不重放。请求头/体、错误文本反射标识、Cookie/Key脱敏，巨型JSON先扫描删标识再裁剪，不构造完整消息树；超过8MiB或无法安全处理时省略。
- 认证Cookie变更安全加密保存，普通测量/可再生session_data缓存更新不触发全库重写。
- 输入预算默认2GiB且≤Go软内存1/3，已知Content-Length单次分配；chunked保守预留后按实际cap调整，所有错误/完成均释放，满额429且零上游请求；上传deadline60s在开始长响应前清除。

## 质量门禁
通过：
- `node --check internal/server/web/app.js`
- `node --check internal/server/web/tokenharbor.js`
- `node internal/server/tokenharbor_web_test.js`
- `go test ./...`
- `go test -race ./...`
- `go vet ./...`
- `go build -o .analysis/tokenharbor-entitlements-capacity/odb ./cmd/odb`
- `docker compose config --quiet`
- 全量门禁后对最终Cookie写抑制/模型歧义（包括拒绝前身份脱敏）/大正文脱敏/输入预算变化再次执行相关race测试及vet/build，通过。

## 真实浏览器（全部本地假数据）
- 独立 `th-entitlement-mock`，OpenCode关闭，所有原生/网站来源为127.0.0.1 Mock；三账号Agent/Office/无原会话。
- Agent 25%已用→总20、可调度周等效3.75；Office 50%及独立重置时间展示，未知账号明确停调度，不将缺失额度显示为0。
- Agent覆盖1/目录5、Office覆盖2/目录5；下游models仅agent-demo+office-demo，free/frontier/legacy均不公布；手工frontier请求503且未发上游。
- native假上游400后，错误行“详细请求”真实点击打开请求/响应头体；页面可见上下文与错误原因，Authorization/Cookie/X-Session-Id已脱敏，原会话值不在DOM。服务端实际errors只有2条（测试脚本曾同时触发两次加载，前端缓存重复不代表重复推理）。
- Mock已终止，64603/64602无监听；本次浏览器和公开脚本研究浏览器均关闭。

## 本地容量基准
命令：`go test ./internal/server -run '^$' -bench '^BenchmarkTokenHarborGateway$' -benchtime=2s -benchmem`

环境darwin/arm64、Apple M4、GOMAXPROCS10，40并行。请求459304真实字节、每worker稳定会话；1000假账号均有来源绑定的假套餐快照。SSE为10次5ms间隔的合成事件，不是真实模型长流/真实token。

| 账号/响应 | ns/op（总墙钟/N） | p95 ms | p99 ms | B/op | 结束RSS MiB | 观测输入MiB |
|---|---:|---:|---:|---:|---:|---:|
| 2 / JSON | 296196 | 28.08 | 40.95 | 1590145 | 140.5 | 17.08 |
| 2 / 50ms SSE | 1359147 | 55.99 | 68.56 | 1657362 | 141.0 | 17.52 |
| 1000 / JSON | 328591 | 32.10 | 43.76 | 1602102 | 155.4 | 16.64 |
| 1000 / 50ms SSE | 1366792 | 55.26 | 94.20 | 1707939 | 155.6 | 17.52 |

所有请求200/完整读取，无失败，完成后输入保留计数0。RSS由当前测试进程ps测量，包含客户端+Mock+网关及前轮保留堆；不是峰值或生产服务独占RSS。1000账号JSON约3043请求/秒，50ms SSE约732请求/秒，只是本地合成CPU/传输基线。中间未单次分配优化的基线约2.14MB/op，最终JSON约1.60MB/op，约25%分配减少；最终还加入完整JSON权限验证，不能将跨轮延迟解释为全面加速。

## 容量边界/上线提醒
- 3500RPM=58.3请求/秒，3亿TPM平均约8.57万token/请求；并发取决于时长。本地40并行、短合成SSE没有证明3500长流并发或真实3亿TPM。
- Compose8GiB硬/6GiB Go/2GiB输入预算、nofile131072是可覆盖初始参数；未应用在线。保护输入内存不等于给所有巨大非流式输出/网络队列定容。
- 真正上限仍受上游吞吐、出口/共享代理/HTTP2流上限、长流RSS与业务数据规模影响；不把32代理连接当32并发，也不无证据改成3500。
- 旧Key-only或无精确关联的账号/旧模型会暂停，这是用户确认的严格策略。实际运营应确认保存会话与手动刷新状态；不要用钱包绕过未知。
- 本地门禁不会主动借钱包，但缓存观察不是上游原子计费锁；官网在检测间改设置/套餐的竞态不能由本地证明消除，需上游账户限制/资金管理保证绝对禁用钱包，不擅自代改。
- 不启用新功能、不重启线上；需另获推送/部署指令，按docs/ovh-deployment.md一次预检→构建→备份切换→验收。本会话未改变的线上基线为2559c94。
- 临时产物在_tmp_artifacts.md登记且保留待授权；Trellis、业务文档、既有备份/镜像不清理。缺少Trellis脚本/工作流文件，沿用本仓库人工任务记录闭环，不创建工具系统。
