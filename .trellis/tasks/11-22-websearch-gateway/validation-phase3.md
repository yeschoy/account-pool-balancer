# 第三阶段：真实线上号池链路 + 新owner本地安全验收

## 实际经过在线Mistral号池：通过
2026-11-22 **09:19:01.975–09:19:08.378 UTC**，OVH测试进程使用已有`自测-test`下游Key（ID ck_f66b798382a306afd3ec309c）、随机新合成会话访问 `http://127.0.0.1:8790/v1`。不是直连官方，不重放用户历史、不创建/改动渠道或账户。搜索后端为用户指定Tavily Hikari。

| 本次号池请求ID | 状态/尝试 | 实际账号 | 绑定 | 输入 | 输出 | 缓存 |
|---|---|---|---|---:|---:|---:|
| req_ef44d48134358533d221a068 |200/success/1|zxg-11.19-02-vibe|miss|368|47|0|
| req_8015cd002e969a83b20e239f |200/success/1|同上|hit|1519|70|0|
| req_eabe3c32dbc31978b79253ca |200/success/1|同上|hit|1606|16|1536|

- 首两轮搜索+回答 **4138ms**，30个事件、1个真实url_citation；网关累计usage input1887、output117、total2004，与池日志368+1519 / 47+70完全一致。
- 按Codex省略sources的历史格式续问成功：input1606、output16、total1622、cached1536，与第三条池日志一致。
- 总计3次模型调用、1次真实搜索，测试本体5.18秒。配置SHA256前后相等；没有重启、覆盖设置或创建测试Key。
- 真实合成测试本身未启用新owner协议：线上仍旧版，以上仅证明该样本沿普通header粘性实际同号，不能承诺忙/失败时固定。

## 上传与进程状态
首次SCP、其次压缩大二进制传输超时。先核对无QA进程/本次日志、远端SHA不一致，未执行半成品；随后传约几十KB的源码包到独立临时目录，使用远端已有Go1.26.8编译测试，一次真实测试通过。没有重复支付推理/搜索。源码/二进制都不含凭据，残留路径在_tmp_artifacts.md，经用户同意再清理。
2026-11-22 10:04:37 UTC只读复核，QA活跃进程0；线上capabilities返回405，明确不支持owner v1。该复核零模型/搜索调用。

## 请求级固定账号保护：已实现，本地通过，未上线
- 认证GET `/v1/pool-capabilities` 公开协议版本/5分钟TTL/Responses，不调用推理/额度。
- `X-APB-Owner-Token:start` 首轮正常选号，仅一次attempt；加密ow1令牌绑定账号source/下游Key ID+hash/group/model/绝对期限，不暴露账号/凭据。
- 续轮携带原令牌，经最新配置source校验后进入原Pool.mu限定owner准入；资格、健康、并发/RPM、MarkSent/Release均复用。可等待或429/409，不overflow/换号，不新建/改变长期绑定。
- 若选号快照与最新Base/proxy/Key已变化，发送前拒绝；不能用新Base两次hash旧账号假称source相同。
- 网关requirePoolOwner必须先预检能力与TTL，旧池拒绝在推理之前；只接受首个owner及后续完全相同令牌，不向调用方/上游模型/搜索/日志/历史存储泄漏。
- 默认无header的其他流量不变，不是持久服务器Responses续传、不保证下一外部请求永久同号。

本地真实HTTP网关→Server→原Pool→Mock外部服务矩阵：正常两轮同账号/累计usage通过；在搜索阶段占满原账号但让其他号空闲，续轮明确失败且仅首轮发往模型。另测忙/RPM/等待取消/健康/隔离/禁用/错误组/Key/model/expiry/source/restart-secret/重复header/旧池能力/日志隐私，normal和race通过。

## 全仓质量门禁
因为本轮触及共享Pool/server，已实际执行：
- `go test -count=1 -timeout=240s ./...`：全部通过，server103.188s。
- `go test -race -count=1 -timeout=240s ./...`：全部通过，server155.941s。
- `go vet ./...`：通过。
- `go build -o .gotmp/odb-request-owner ./cmd/odb`、`go build -o .gotmp/websearch-gateway ./cmd/websearch-gateway`：通过。
- 后加健康/取消边界测试仅测试文件变化，受影响pool/server定向race复验通过（2.286s / 3.366s），不重复全仓。
- 格式/diff和真实密钥未落盘审查在提交前执行；例子仅环境变量引用。

## 未做 / 上线门禁
- **未部署**新增owner版Balancer或独立网关；线上原服务及NewAPI渠道不变，网关不是常驻生产入口。
- 新owner机制未做线上验收；需用户明确允许生产升级后按现有部署约定执行，不以“继续开发”自行切业务入口。
- 真实Codex Desktop/CLI、新NewAPI测试渠道路径/计费/UI未验收；cached-only仍明确拒绝，默认Codex零配置不保证。
- open/find、preview/image、地理位置、其他协议、管理UI/多副本历史仍不在本阶段。

状态：真实接池链路已验证；固定账号协议代码/本地全仓质量已完成；待授权部署与客户端验收，任务保持in_progress。
