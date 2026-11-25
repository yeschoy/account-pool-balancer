# account-pool-balancer (`odb`)

<p align="center">
  <img src="docs/logo.svg" width="128" height="128" alt="account-pool-balancer logo" />
</p>

多上游 AI 账号池负载均衡与协议代理服务。面向下游中转网关（`new-api`、`One API`、`Sub2API` 等）与编码/对话客户端（`Claude Code`、`Codex`、`OpenCode`、`pi` 等），提供统一的下游 API 密钥（`sk-odb-…`）和标准 OpenAI / Anthropic 兼容接口，把请求智能分发到多个上游号池，负责**选号调度、会话与缓存粘性、并发与滚动 RPM 控制、多源额度轮询、错误重试与故障隔离、自动注册补号、费用统计与脱敏日志**，并内置完整的现代化 Web 管理台。

主服务以单个 Go 二进制编译运行（`./cmd/odb`），无外部数据库依赖；配置和需持久化的状态保存在数据目录中，部分注册机另需 Python / 浏览器环境。

> **状态提示**：
> - **全局号池启停**：侧栏「号池管理 / 启停」（配置 `poolSwitches`）可在保留账号与日志数据的前提下关闭暂不使用的号池，停止其后台上游工作并拒绝新流量，全部关闭后仍能打开恢复。
> - **Factory AI 号池（本地新增）**：详见 [Factory AI 号池与全局号池启停](docs/factory-pool.md)、[Factory CLI 1.14.0 抓包清单](docs/factory-cli-endpoints.md) 与 [Factory Desktop 协议还原](docs/factory-desktop-protocol.md)。Factory 身份（`whoami`）与额度（`/api/billing/limits`）接口已实测，管理链路、原生路由与有限跨协议桥接已本地验证；出站请求已按 droid 1.14.0 抓包对齐双 User-Agent、请求面、traceparent、模型面 `prem.factory.ai` 与系统提示词固定开头（`internal/factory/droid.go`），但真实模型请求尚未验证通过，不得视为已完成推理或生产验收。

---

## 目录

- [1. 支持的号池与注册机总览](#1-支持的号池与注册机总览)
- [2. 快速开始与部署](#2-快速开始与部署)
  - [2.1 本地编译与运行](#21-本地编译与运行)
  - [2.2 启动参数与环境变量](#22-启动参数与环境变量)
  - [2.3 Docker 部署与生产规范](#23-docker-部署与生产规范)
- [3. 各号池详细接入与设计边界](#3-各号池详细接入与设计边界)
  - [3.1 OpenDesign (`opendesign`)](#31-opendesign-opendesign)
  - [3.2 River (`river`)](#32-river-river)
  - [3.3 Ollama (`ollama`)](#33-ollama-ollama)
  - [3.4 Modal (`modal`)](#34-modal-modal)
  - [3.5 Perch (`perch`) — Supabase 会话池与注册机](#35-perch-perch--supabase-会话池与注册机)
  - [3.6 Token Harbor (`tokenharbor`)](#36-token-harbor-tokenharbor)
  - [3.7 Mistral (`mistral`)](#37-mistral-mistral)
  - [3.8 OpenCode Zen / Go (`opencode-zen` / `opencode-go`)](#38-opencode-zen--go-opencode-zen--opencode-go)
  - [3.9 Model Studio 阿里云百炼 (`modelstudio`)](#39-model-studio-阿里云百炼-modelstudio)
  - [3.10 Deploy (`deploy`)](#310-deploy-deploy)
  - [3.11 50X AI (`x50`)](#311-50x-ai-x50)
  - [3.12 ALTAPI (`altapi`)](#312-altapi-altapi)
  - [3.13 Factory AI (`factory`)](#313-factory-ai-factory)
  - [3.14 Telnyx 注册机 (`telnyx`，无推理号池)](#314-telnyx-注册机-telnyx无推理号池)
- [4. 下游调用接口与开放导入 API](#4-下游调用接口与开放导入-api)
  - [4.1 模型推理与目录接口](#41-模型推理与目录接口)
  - [4.2 开放导入接口（自动上号与暂存区）](#42-开放导入接口自动上号与暂存区)
  - [4.3 返回给下游的状态码转换表](#43-返回给下游的状态码转换表)
- [5. 调度核心与内部机制](#5-调度核心与内部机制)
  - [5.1 选号流程与 9 种调度策略](#51-选号流程与-9-种调度策略)
  - [5.2 会话粘性与两阶段提交绑定](#52-会话粘性与两阶段提交绑定)
  - [5.3 并发控制与 60 秒滚动 RPM 窗口](#53-并发控制与-60-秒滚动-rpm-窗口)
  - [5.4 换号规则、错误规则与重试规则](#54-换号规则错误规则与重试规则)
  - [5.5 流式首包防抖与心跳](#55-流式首包防抖与心跳)
  - [5.6 模型价格表与谷峰/长上下文计费](#56-模型价格表与谷峰长上下文计费)
  - [5.7 日志分段、内存环与脱敏规范](#57-日志分段内存环与脱敏规范)
  - [5.8 全局号池启停 (`poolSwitches`)](#58-全局号池启停-poolswitches)
- [6. 数据目录文件与热加载 (`DATA_DIR`)](#6-数据目录文件与热加载-data_dir)
- [7. 核心配置项字典 (`routing` / `quota` / `logs`)](#7-核心配置项字典-routing--quota--logs)
- [8. 架构限制、回滚与开发验证](#8-架构限制回滚与开发验证)

---

## 1. 支持的号池与注册机总览

当前代码包含 **14 个推理号池标识**（OpenCode Zen / Go 分别计数，Factory 尚未通过真实推理验收）以及 **Telnyx 独立注册机**。各池的分组、下游密钥（`sk-odb-…`）、导入密钥（`sk-odbimp-…`）、路由参数、错误/重试规则和导入日志（每池保留最近 1000 条）独立管理。默认不跨池借号；OpenCode 仅在显式配置时允许 Zen / Go 单跳回退。

| 号池 (`provider`) | 默认分组 | 凭证形态与识别方式 | 支持的下游路径 | 额度感知 / 注册机 | 专项文档 |
|---|---|---|---|---|---|
| **OpenDesign**<br>`opendesign` | `default` | 运行密钥 `sk-amr-…`<br>控制密钥 `vela_ctrl_…`（可选） | `/v1/chat/completions`<br>`/v1/responses`、`/v1/messages`<br>`/v1/*`、`/v1/models` | 控制密钥读工作区窗口、套餐模型额度、钱包与 `billing/preflight`；无控制密钥靠错误推断 | 见 [§3.1](#31-opendesign-opendesign) |
| **River**<br>`river` | `river` | API Key `rv_…`（自动识别） | `/v1/chat/completions`<br>`/v1/responses`、`/v1/models` | 无额度接口，靠 402/429 错误规则分级翻倍冷却；按 Key 同步 `/v1/models` | 见 [§3.2](#32-river-river) |
| **Ollama**<br>`ollama` | `ollama` | API Key（`oll-` 前缀或显式 `provider=ollama`） | `/v1/chat/completions`、`/v1/completions`<br>`/v1/responses`（无状态）<br>`/v1/messages`、`/v1/embeddings`、`/v1/models` | 读取 `GET /api/balance`（赠送额度、`session`/`weekly` 百分比、已购额度），单号最小间隔 30s | [docs/ollama-hardening.md](docs/ollama-hardening.md) |
| **Modal**<br>`modal` | `modal` | Proxy Token `wk-<id>.ws-<secret>` | `/v1/chat/completions`<br>`/v1/responses`、`/v1/messages`、`/v1/models` | 无余额接口，`/v1/models` 免费验活并映射 Endpoint 短名称；402 整号冷却 | [docs/modal-import-runbook.md](docs/modal-import-runbook.md) |
| **Perch**<br>`perch` | `perch` | Supabase 会话 JSON（Access + 旋转 Refresh Token） | `/v1/chat/completions`<br>`/v1/responses`、`/v1/models` | 自动续期会话 + GoTrue 检测 + `usage_meter` RPC（PT 额度）+ **内置 GitHub 注册机与自动补号** | 见 [§3.5](#35-perch-perch--supabase-会话池与注册机) |
| **Token Harbor**<br>`tokenharbor` | `tokenharbor` | API Key `thk_…` + 注册原记录会话 | `/v1/chat/completions`<br>`/v1/responses`、`/v1/messages`、`/v1/models` | 固定代理只读 `/pricing` 与 `/api/me/free-tier` 门禁防钱包透支 + **内置 Go 注册机** | [docs/tokenharbor.md](docs/tokenharbor.md) |
| **Mistral**<br>`mistral` | `mistral` | Studio / Vibe Key（`mstrl_` 或显式声明） | `/v1/chat/completions`、`/v1/responses`（无状态）<br>`/v1/responses/input_tokens`、`/v1/fim/completions`<br>`/v1/embeddings`、`/v1/models` | 接入 Studio/Vibe 套餐与组织订阅刷新 + 思考档位兼容 + **内置混合协议注册机** | [docs/mistral.md](docs/mistral.md)<br>[docs/mistral-capacity.md](docs/mistral-capacity.md) |
| **OpenCode**<br>`opencode-zen`<br>`opencode-go` | `opencode-zen`<br>`opencode-go` | Zen / Go 授权 Key（默认 `opencode.enabled=false`） | `/v1/chat/completions`<br>`/v1/responses`、`/v1/messages`、`/v1/models` | Zen/Go 能力交集、Go 三窗口 usage、Go 固定出口 IP 强校验、可选受限跨协议桥接与单跳回退 | [docs/opencode.md](docs/opencode.md)<br>[docs/opencode-go-gateway.md](docs/opencode-go-gateway.md)<br>[docs/opencode-audit.md](docs/opencode-audit.md) |
| **Model Studio**<br>`modelstudio` | `modelstudio` | 阿里云百炼 Key（按量 `sk-…` / Token Plan `sk-sp-…`） | `/v1/chat/completions`<br>`/v1/messages`、`/v1/models` | 按套餐（`payg`/`tokenplan`）与 4 大地域自动路由 `dashscope` / `token-plan` 接入点 | 见 [§3.9](#39-model-studio-阿里云百炼-modelstudio) |
| **Deploy**<br>`deploy` | `deploy` | 推理 Secret（显式 `provider=deploy`） | `/v1/chat/completions`、`/v1/models` | 独立目录同步与估价表；余额未接入（显示未知）；**内置 Python 注册机与绑卡流程** | [docs/deploy-pool-integration.md](docs/deploy-pool-integration.md)<br>[docs/deploy-balancer.md](docs/deploy-balancer.md)<br>[docs/deploy-registrar-hardening.md](docs/deploy-registrar-hardening.md) |
| **50X AI**<br>`x50` | `x50` | API Key `sk-50x-…` + 可选管理 Token | `/v1/chat/completions`、`/v1/models` | `ControlKey` 读 `/api/billing` 真实余额；**内置 Go 注册机、代理协议自愈、批量入池与批量重置推理代理** | [docs/x50-pool.md](docs/x50-pool.md)<br>[docs/x50reg.md](docs/x50reg.md) |
| **ALTAPI**<br>`altapi` | `altapi` | API Key（显式 `provider=altapi`）+ 可选 session cookie | `/v1/chat/completions`<br>`/v1/responses`、`/v1/messages`<br>`/v1/*`、`/v1/models` | session + `New-Api-User` 读真实余额（低余额停用门控）、固定每小时目录快照 + **内置注册机与二选一自动补号** | [docs/altapi-pool.md](docs/altapi-pool.md) |
| **Factory AI**<br>`factory` | `factory` | API Key `fk-…` 或 WorkOS 设备授权 | `/v1/chat/completions`<br>`/v1/responses`、`/v1/messages`、`/v1beta/models/{model}:generateContent`（Gemini）、`/v1/models`（参考列表） | 实测 `whoami` 与 `/api/billing/limits` 额度 + 按 droid 1.14.0 抓包对齐的客户端身份、Gemini 原生路由与可选受限协议桥接（真实模型推理未验证通过） | [docs/factory-pool.md](docs/factory-pool.md)<br>[docs/factory-cli-endpoints.md](docs/factory-cli-endpoints.md) |
| **Telnyx（仅注册机）**<br>`telnyx` | — | Outlook 邮箱自动化注册（无推理号池） | — | 注册、邮件验证、Portal 会话、创建 API Key 与读取试用额度；历史实测为 $25，不保证当前赠金 | [docs/telnyxreg.md](docs/telnyxreg.md) |

---

## 2. 快速开始与部署

### 2.1 本地编译与运行

```sh
go build -o odb ./cmd/odb
ODB_ADMIN_PASSWORD='请替换为独立强密码' ./odb -addr 127.0.0.1:8790 -data ./data
```

打开 `http://127.0.0.1:8790/`：

1. **添加账号**：
   - **OpenDesign 设备授权**：「账号 → 设备授权添加」，拿到验证码和激活链接，在**已登录目标 OpenDesign 账号**的浏览器中打开并批准，账号连同 `sk-amr-…` 运行密钥和 `vela_ctrl_…` 控制密钥自动入池。
   - **手动添加 / 批量导入**：切换到对应号池标签页，按每行 `运行密钥[,控制密钥[,名称[,代理URL]]]` 导入（无控制密钥的池为 `API Key[,名称[,代理URL]]`；Perch 支持单文件/多选/拖拽/ZIP 导入 JSON；50X / ALTAPI / Deploy / Token Harbor / Mistral 等也可通过内置注册机批量生成与入池；长凭据在账号列表中默认折叠显示）。
2. **创建下游密钥与导入密钥**：「下游密钥 → 新建密钥」生成 `sk-odb-…`（仅显示一次）。如需脚本自动上号，在同一页「新建导入密钥」生成 `sk-odbimp-…`。
3. **对接 new-api**：在 `new-api` 中新建渠道，类型选 OpenAI（或 Anthropic Claude），Base URL 填本服务地址，密钥填 `sk-odb-…`，**务必打开“透传请求头”**。

> 未设置 `ODB_ADMIN_PASSWORD` 时，第一次打开管理台会强制要求设置管理员密码。服务暴露到公网前务必先设好密码。

编译需要 Go 1.26.8 或兼容工具链（见 `go.mod`）。默认监听地址 `:8790` 会绑定所有网卡，上例显式限制为本机访问。

### 2.2 启动参数与环境变量

| 环境变量 | 命令行参数 | 默认值 | 说明 |
|---|---|---|---|
| `ODB_ADDR` | `-addr` | `:8790` | HTTP 监听地址 |
| `ODB_DATA_DIR` | `-data` | `./data` | 数据目录（配置、元数据、加密凭据、日志） |
| `ODB_ADMIN_PASSWORD` | — | 空 | 初始管理员密码（仅在尚未设置密码时生效） |
| `ODB_TH_BUFFER_MB` | `-th-buffer-mb` | `2048` | Token Harbor / Mistral 共享原生输入缓冲预算（单位 MiB，自动不超过 `GOMEMLIMIT` 的 1/3） |
| `ODB_MISTRAL_CACHE_AUDIT_KEY_ID` | `-mistral-cache-audit-key-id` | 空 | 可选：针对指定单个已启用下游 Key ID 开启 Mistral 缓存与连接指纹诊断（限时 30 分钟 / 128 请求，不记录正文） |
| `PERCH_REGISTER_PYTHON` | — | `python3` | Perch 注册机使用的 Python 解释器路径 |
| `PERCH_REGISTER_WORKER` | — | `tools/perch-register/worker.py` | Perch 注册机 `worker.py` 路径 |
| `MISTRAL_WORKER_PYTHON` | — | `python3` | Mistral 注册机使用的 Python 解释器路径 |
| `MISTRAL_WORKER` | — | `tools/mistral/worker.py` | Mistral 注册机 `worker.py` 路径 |
| `MISTRAL_WORKER_ROOT` | — | `tools/mistral/signup` | Mistral 注册机核心包根目录 |
| `MISTRAL_BROWSER_CHANNEL` | — | `chrome` | Mistral 注册机浏览器通道（容器内使用 Xvfb + 官方 Google Chrome Stable） |
| `DEPLOY_PYTHON` | — | `python3` | Deploy 注册机使用的 Python 解释器路径 |
| `DEPLOY_REGISTRAR` | — | `tools/deploy_registrar.py` | Deploy 注册机脚本路径 |
| `ALTAPI_PYTHON` | — | `python3` | ALTAPI 注册机使用的 Python 解释器路径 |
| `ALTAPI_REGISTRAR` | — | `tools/altapi_registrar.py` | ALTAPI 注册机脚本路径 |

> `50X AI` 注册机除内置在控制台外，也可单独编译 CLI 工具：`go build -o x50reg ./cmd/x50reg`（详见 [docs/x50reg.md](docs/x50reg.md)）。

### 2.3 Docker 部署与生产规范

以下适用于新建环境；OVH 现有实例须按本节末尾的专用流程操作。当前 `Dockerfile` 固定安装 amd64 Google Chrome Stable，非 amd64 主机需另行准备兼容构建环境。

```sh
cat > .env <<'X'          # 已在 .gitignore 中，权限建议 0600
ODB_ADMIN_PASSWORD=...     # 只在第一次启动、还没设密码时生效
ODB_UID=1000               # 容器以宿主用户运行，./data 属于该用户
ODB_GID=1000
X
mkdir -p data && chmod 700 data
# ai-internal 为外部网络；尚不存在时先创建
docker network inspect ai-internal >/dev/null 2>&1 || docker network create ai-internal
docker compose up -d --build
```

- **容器内网与兼容别名**：容器加入共享网络 `ai-internal`（与 `new-api` 同一网络），同时具备 `account-pool-balancer` 与兼容别名 `opendesign-balancer`。`new-api` 渠道 Base URL 填 `http://account-pool-balancer:8790`。
- **安全绑定**：管理台仅绑定宿主机 `127.0.0.1:8790`，远程访问请用 SSH 隧道 `ssh -L 8790:127.0.0.1:8790 主机`，或通过反向代理加 HTTPS。
- **优雅停机**：停止时先拒绝新请求入场（返回 `503 server_draining`），等待在途请求最多 30 秒（Compose `stop_grace_period: 35s`），并等待后台额度轮询与注册机任务收尾、刷盘计数与元数据后再关闭日志。
- **OVH 生产环境规范**：当前线上实例从固定版本快照构建镜像，**禁止直接在旧工作树执行 `docker compose up --build`**，也禁止用遗漏网络别名、healthcheck 或资源参数的裸 `docker run` 替代。部署后必须从 `new-api` 容器同时验证 `http://account-pool-balancer:8790/healthz` 与 `http://opendesign-balancer:8790/healthz` 均返回 200。完整流程见 [OVH 部署记录](docs/ovh-deployment.md)。

---

## 3. 各号池详细接入与设计边界

### 3.1 OpenDesign (`opendesign`)

依据官方[模型额度说明](https://open-design.ai/zh/model-credits/)、[价格页](https://open-design.ai/zh/pricing/)与客户端规范：

- **两类模型与扣费顺序**：
  1. **Design Plan 模型**（DeepSeek V4/V4.1、MiMo、GLM-5.3 Flash、Kimi K2.7 Code、Claude Haiku 5.5、GPT-6 Luna 等）：先扣 Design Plan 7 天滚动窗口（Go 档另有 5 小时窗口），用尽后扣**套餐模型额度**（`balances.subscriptionCredits`）。
  2. **套餐模型**（Claude Sonnet/Opus/Fable、GPT-6 Sol/Astra、Gemini、Grok、图片模型等）：不走窗口，直接扣套餐模型额度。
  3. 两者均用尽后请求停止，除非开启了 `walletFallback` 且钱包/充值额度（`rechargeCredits`）有余额。
- **套餐模型目录与类别判定**：
  - 每 10 分钟通过每把 API Key 自身的 `/v1/models` 免费同步可用模型（Go 档 12 个；Plus 档 39 个；Pro/Max 档 42 个）。不在该账号列表里或标 `enabled:false` 的模型不会派给该账号；全组都不支持时直接返回 `503 model_not_entitled`，不打上游（`ignoreCatalog` 可关闭此检查）。
  - 模型类别以 `billing/preflight?modelId=` 的 `modelCovered`（`true` 为 Design Plan 模型）为准，对近期请求过的模型逐账号查询并缓存 6 小时。
- **控制面额度轮询**：
  - 有 `vela_ctrl_…` 控制密钥的账号每 60 秒读取工作区、套餐窗口、钱包和账单摘要，全局并发 2，失败按 `60s × 2ⁿ` 退避（上限 15 分钟）。聊天请求绝不等待额度刷新。
  - 各分项独立保存，不相加、不用 `creditsPerUsd` 换算。读取失败保留上次成功值，绝不写成 0；控制密钥 401/403 时标记「控制密钥失效」并按额度未知处理；快照超过 15 分钟视为过期。
  - **严格排除条件**：只有拿得出新鲜证据证明「该模型可用窗口无余量 + 账单摘要显示套餐模型额度为 0 + 钱包和充值额度为 0（或关闭了 `walletFallback`）」时才排除账号。概览页的窗口剩余按工作区去重求和，多账号共享同一工作区时只算一次并标注「共享工作区」。
- **仅 API Key 模式（无控制密钥）**：
  - 只填 `sk-amr-…` 完全可用：可用模型照样按该 Key 的 `/v1/models` 判断；模型类别优先复用池内其他带控制密钥账号的 preflight 结果，否则使用内置 Design Plan 模型清单。
  - 额度耗尽靠上游错误推断：`insufficient_balance` 出现在 Design Plan 模型上时**整号冷却**；出现在套餐模型上时**仅冷却该账号的全部套餐模型**，Design Plan 模型照常调度。冷却从 10 分钟起连续命中逐次翻倍（上限 6 小时），成功一次清零。

#### OpenDesign 默认错误规则 (`errorRules`)

| 规则 ID | 匹配条件 | 内部动作 | 回下游状态码 |
|---|---|---|---|
| `platform-provider-credentials` | `upstream_provider_unauthenticated/forbidden`（上游网关把供应商 401/403 改写成 500） | 忽略：不扣健康分、不换号 | `502` |
| `account-suspended` | `account_suspended` | 隔离账号 | `503` |
| `runtime-key-invalid` | 401 + `invalid_api_key` / `unauthenticated` / `auth_required` | 隔离账号（重新授权后点“恢复”） | `502` |
| `model-window-limit` | `model_limit_exceeded`（HTTP 400，不扣费） | 该模型冷却到网关给出的重置时刻，换号 | `429` |
| `membership-concurrency` | `tier_limit_exceeded` | 账号短冷却（≤1 分钟）并自动学习并发/RPM 上限，换号 | `429` |
| `usage-limit-exceeded` | `usage_limit_exceeded`（API Key 美元用量上限） | 账号冷却，换号 | `429` |
| `insufficient-balance` | `insufficient_balance`、`余额不足`、`预扣费额度失败` 等 | 按额度来源分级冷却（Design Plan→整号，套餐模型→该号全部套餐模型），10m 起翻倍至 6h；有控制密钥同时触发额度刷新 | `503` |
| `tier-model-not-entitled` | `tier_model_not_entitled` / `tier_request_kind_not_entitled` | 该模型冷却 24 小时 | `503` |
| `rate-limited` | 其他 429 | 账号按 `Retry-After` 冷却（30s～5m） | `429` |
| `forbidden` | 其他 403 | 账号冷却 5 分钟 | `503` |

---

### 3.2 River (`river`)

- 支持 [River](https://docs.river.ai/) API Key（`rv_` 开头自动识别为 `river`，默认进 `river` 组），上游 `https://api.river.ai/v1`（`upstream.riverBase` 可改），鉴权 `Authorization: Bearer rv_…`，无控制密钥。
- **仅支持 OpenAI Chat Completions 与 Responses**（`/v1/chat/completions`、`/v1/responses`、`/v1/models`）。收到 `/v1/messages` 等其他路径时本地直接返回 `404 unsupported_endpoint`，不打上游。
- 每把 Key 的 `/v1/models` 作为可用模型表。
- 额度未知，靠独立规则 `riverErrorRules` / `riverRetryRules` 推断：402（余额不足）逐次翻倍冷却，429 按 `Retry-After` 冷却，401 冷却后换号。

---

### 3.3 Ollama (`ollama`)

- 支持云端 `https://ollama.com/v1`（`upstream.ollamaBase` 可改）或自建 `http://host:11434/v1`。
- **原生三协议直通**（无需协议转换）：`/v1/chat/completions`（含 `/v1/completions`、`/v1/embeddings`）、无状态 `/v1/responses`、`/v1/messages`（漏传 `anthropic-version` 自动补 `2023-06-01`）。
- **凭证识别与鉴权**：管理台在「Ollama 号池」标签下添加或开放导入传 `"provider":"ollama"`（也可使用 `oll-` 前缀自动识别）。对 Ollama 账号**仅发送 `Authorization: Bearer`**（避免 `ollama.com` 的 `/v1/messages` 拒绝单独 `x-api-key`）。
- **额度接口 (`GET /api/balance`)**：读取赠送额度（`included.balance_usd` / `allowance_usd` 与计费周期）、`session` / `weekly` 剩余百分比、已购额度 `purchased.balance_usd`。仅当「赠送额度用完且已购额度为 0」时判为耗尽，同一账号两次读取至少间隔 30 秒。
- 独立错误规则 `ollamaErrorRules` / `ollamaRetryRules`：401 隔离换 Key，403 账号停用隔离，429 按 `Retry-After` 冷却，5xx 短冷却换号。更多细节见 [docs/ollama-hardening.md](docs/ollama-hardening.md)。

---

### 3.4 Modal (`modal`)

支持 [Modal Shared Endpoints](https://modal.com/docs/guide/shared-endpoints)，默认上游 `https://inference.us-west.modal.direct/v1`（`upstream.modalBase` 可改，漏写 `/v1` 自动补齐）。

- **完整 Token 校验**：Proxy Token 完整形态为 `wk-<id>.ws-<secret>`（两段缺一不可）。仅填 `wk-<id>` 时会精准识别并标注为 **「Token 不完整」**。仅发送 `Authorization: Bearer wk-<id>.ws-<secret>`。
- **统一模型短名称映射**：`GET /v1/models` 将池内各账号 Endpoint 按基础模型合并去重，发布统一短名称（如 `glm-5-3`、`glm-5-3-flash`、`deepseek-v4-1-flash-1017`、`kimi-k3`、`qwen3-8-max`）；发往上游时自动替换为选中账号的 Endpoint hostname。
- **三协议支持与缓存亲和**：支持 `/v1/chat/completions`、`/v1/responses`、`/v1/messages`；`/v1/embeddings` 本地直接 404。自动将本地算出的会话 HMAC 指纹写入上游 `Modal-Routing-Affinity-Key` 头，以命中容器前缀缓存。
- **零成本验活与独立规则 (`modalErrorRules` / `modalRetryRules`)**：通过免费的 `/v1/models` 检验 Token 活性；401/403 隔离账号，402 整号翻倍冷却，429 仅冷却该模型，5xx 短冷却，`404 unknown inference model` 立即停止换号。自动上号流程参见 [docs/modal-import-runbook.md](docs/modal-import-runbook.md)。

---

### 3.5 Perch (`perch`) — Supabase 会话池与注册机

管理 [Perch](https://chat.perchai.app) 的 **Supabase 会话池**（凭证为 Access Token + 旋转 Refresh Token，AES-256-GCM 加密保存在 `data/perch-secrets.json`），将 `/v1/chat/completions` 与 `/v1/responses` 翻译为 Perch 的 `model-call` 信封并把事件流翻译回 OpenAI SSE。

- **导入与防倒退**：支持单文件、多选、拖拽、ZIP 及导入前预览；按 `account.supabase_user_id` 去重，旧 Session 文件不覆盖已存新 Session。
- **自动续期与状态检测**：后台按 `refreshTickSec`（默认 30s）检查，剩余不足 `refreshMarginMin`（默认 10 分钟）时进程内按 User ID 单飞续期并立刻原子落盘；GoTrue `/auth/v1/user` 每 300s 校验会话，区分 `正常` / `待验证` / `待选套餐` / `登录失效` / `刷新失败` / `额度耗尽` / `请求限流` / `服务暂时不可用` / `已禁用`。
- **额度读取 (`usage meter`)**：只读 `GET /api/perchai/account` 与 `perch_ai_my_inference_usage_meter` RPC（单位 PT = 千分之一美元），对 `tierSelectionRequired: true` 的账号显式标红为 `待选套餐` 且不参与调度。
- **转发通道、Turn Ticket 与设备指纹隔离**：
  - `cli`（默认）走 `app.perchai.app/api/perch-terminal/model-call`，完整支持 `system` / `tools` / `images` / `response_format`；`web` 走 `chat.perchai.app/api/perchai/chat`（会丢弃 `tools`）。
  - 每次调用前领用 Turn Ticket（遇 `turn_rate_limited` 立即终止，绝不换号规避配额），并支持 Held-Context SHA-256 增量发送（`deltaTtlMin` 默认 30 分钟）。
  - 每账号独立随机 UUID `machine-id` 保存在 `data/perch-device.json`；默认 `telemetryEgress=false` 不外发遥测请求。
- **GitHub 注册机与自动补号**：支持输入 `账号----密码----2FA密钥----接码页` 串行注册、选 Starter 套餐并首条激活；「自动补号」可在目标分组正常账号低于阈值时自动注册入池，入池后自动擦除候选敏感凭据。

---

### 3.6 Token Harbor (`tokenharbor`)

完整文档见 [docs/tokenharbor.md](docs/tokenharbor.md)。

- **原生三协议直通**：支持 `/v1/chat/completions`、`/v1/responses`、`/v1/messages` 与 `/v1/models`，保留原样正文、`tools`、`cache_control`、`prompt_cache_key` 及 Anthropic beta 头。
- **套餐证明与无钱包透支门禁**：通过账号固定代理定期只读 `/pricing`（Pass 档位）和 `/api/me/free-tier`（`plan_used_pct`、周期及 `overage_billing`）；套餐或周用量未知/过期、周额耗尽或开启了 `overage_billing` 时立即暂停调度，防止误扣钱包余额。
- **会话亲和与绑定忙不溢出**：默认绑定忙时不溢出（避免把同一对话历史拆散到另一把 Key）；`previous_response_id` 严格绑定同一账号，失效/跨 Key 返回 409；遇到大额消费确认提示硬停止并原样返回。
- **独立详情日志与内置 Go 注册机**：`tokenHarbor.detailLog` 默认关闭，与全局开关解耦；Go 原生注册机（`internal/thregister`）支持邮箱 / GitHub 持久化会话注册、批量提取 API Key 及入池/退回注册机流转。

---

### 3.7 Mistral (`mistral`)

完整文档见 [docs/mistral.md](docs/mistral.md)、[docs/mistral-capacity.md](docs/mistral-capacity.md) 与 [docs/mistral-cache-diagnostics.md](docs/mistral-cache-diagnostics.md)。

- **支持路径与识别**：默认上游 `https://api.mistral.ai/v1`，支持 `/v1/chat/completions`、无状态 `/v1/responses`、`/v1/responses/input_tokens`、`/v1/fim/completions`、`/v1/embeddings` 与 `/v1/models`。`mstrl_` 前缀自动识别，其他 Key 需显式声明 `provider: "mistral"`。
- **真实会话优先与缓存亲和**：按自定义会话头 → 父会话头 → 正文会话字段 → 内置 28 种客户端会话头 → 缓存键 → 首条消息 HMAC 粘性选号（确认绑定持久化在 `metadata.pool.mistralBindings`）；自动补齐 `prompt_cache_key` 并同步到 `X-Affinity` 请求头。
- **推理模型思考格式与档位兼容**：自动将 Chat 历史中的 `reasoning_content` / `reasoning` 转为原生 `thinking` 块；保留 `low` / `high` / `max` 思考强度档位与完整 Responses 事件流；自动清理 `new-api` 泄漏的空 `MimeType` 等字段。
- **1910 / 3505 / 3800 精确规则**：`403 + 1910` 仅冷却该账号该模型 24h；`429 + 3505`（后端容量不足）与 `503 + 3800` 不扣健康分并立即停止换号；独立统计 24h 缓存命中率；内置 Mistral 混合协议注册机。

---

### 3.8 OpenCode Zen / Go (`opencode-zen` / `opencode-go`)

完整文档见 [docs/opencode.md](docs/opencode.md)、[docs/opencode-go-gateway.md](docs/opencode-go-gateway.md) 与 [docs/opencode-audit.md](docs/opencode-audit.md)。

- **默认关闭 (`opencode.enabled=false`)**：分为 `opencode-zen`（`https://opencode.ai/zen/v1`）与 `opencode-go`（`https://opencode.ai/zen/go/v1`）两个独立号池，支持按协议配置 `zenProtocolBases` / `goProtocolBases`。
- **原生三协议与能力交集**：公开能力目录与各账号 `/models` 取交集；`previous_response_id` 严格绑定并随 metadata checkpoint 持久化。
- **Go 固定出口 IP 强校验 (`ensureGoEgress`)**：推理前通过账号固定代理无凭证探测出口 IP，不一致或代理故障时直接拒绝请求（Fail-Closed），绝不回退直连；遇 `FreeTierError` 等政策拒绝立即暂停保护。
- **可选桥接与跨套餐回退**：开启 `compatibility` 后支持受限缓冲跨协议转换（最大 16 MiB）；开启 `zenFallbackGroup` / `goFallbackGroup` 后支持最多一跳跨套餐回退。

---

### 3.9 Model Studio 阿里云百炼 (`modelstudio`)

- **自动识别套餐 (`payg` vs `tokenplan`)**：`sk-sp-…` 自动识别为 Token Plan / Coding Plan 订阅套餐（`tokenplan`）；`sk-…` 识别为按量付费（`payg`，当配置中已启用 Model Studio 池或显式声明 `provider=modelstudio` 时归入此池，也可在 `planOverrides` 中逐号覆盖）。
- **隔离计费接入点与四大地域**：Token Plan 支持北京（`token-plan.cn-beijing.maas.aliyuncs.com`）与新加坡（默认，`token-plan.ap-southeast-1.maas.aliyuncs.com`）；按量付费支持北京、新加坡、弗吉尼亚、法兰克福四地域的 `/compatible-mode/v1` 与 `/apps/anthropic/v1`。
- **支持协议**：原生 `/v1/chat/completions`、`/v1/messages` 与 `/v1/models`；由于官方明确不支持 OpenAI Responses API，`/v1/responses` 在本地直接拒绝。

---

### 3.10 Deploy (`deploy`)

完整文档见 [docs/deploy-pool-integration.md](docs/deploy-pool-integration.md)、[docs/deploy-balancer.md](docs/deploy-balancer.md) 与 [docs/deploy-registrar-hardening.md](docs/deploy-registrar-hardening.md)。

- **接口与目录**：显式 `provider=deploy`，默认 Base URL 为 `https://api.deploy.com/api/v1`（注意 API 根路径含 `/api/v1`）。仅服务 `POST /v1/chat/completions` 与 `GET /v1/models`（按 `modelSyncSec` 后台同步，其他路径本地拒绝），默认内置官方估算价格表，余额当前未接入（显示未知，不写 0）。
- **独立规则与日志**：独立保存 `deploy.routing`、`deploy.quota`、`deploy.pricing`、`deploy.detailLog` 以及 `deployErrorRules` / `deployRetryRules`。
- **内置注册机与绑卡**：Go 管理器串行调用 `tools/deploy_registrar.py`（配合 `curl_cffi` + Playwright 反检测加固或 AdsPower 环境，见 [docs/adspower-integration.md](docs/adspower-integration.md)），支持接码邮箱注册验证与 Stripe 绑卡流程，数据保存在 `data/deploy-signup.json`。

---

### 3.11 50X AI (`x50`)

完整文档见 [docs/x50-pool.md](docs/x50-pool.md) 与 [docs/x50reg.md](docs/x50reg.md)。

- **接口与默认模型**：`provider=x50`，`sk-50x-` 前缀自动识别并禁止跨池导入。默认 Base URL `https://api.ai.50x.energy/v1`，原生支持 `GET /v1/models` 与 `POST /v1/chat/completions`（含 SSE 与工具调用），默认测试模型 `deepseek-ai/DeepSeek-V4.1-Flash-1017`。
- **真实余额轮询**：`ControlKey` 可选填裸管理会话 Token，后台使用 `Authorization: Token <session>` 请求 `/api/billing` 读取 `balance_usd` 与 `tier`；余额格式异常保留上次同源历史并报错（绝不写成 0），付费/预付费层新鲜余额 `<= 0` 时暂停调度。注意同一上游账号多 Key 共享余额与限流。
- **赠金边界**：注册文档中的 $100 是历史记录，不是当前保证；当前公开说明不再向新账号赠送免费额度，以实际 `/api/billing` 结果为准。
- **独立日志开关与控制台工具**：`x50.requestLog`、`x50.errorLog`、`x50.detailLog` 可分别独立控制。内置 **50X 注册机**（Go 原生实现 `internal/x50reg`，自动探测并纠正误配为 `socks5` 的 HTTP 代理网关，支持注册日志右下角浮层）、**批量入池**（`POST /api/x50/pool/import`，默认隐藏已入池候选）以及**批量重置推理代理**（`POST /api/x50/pool/proxy`）。

---

### 3.12 ALTAPI (`altapi`)

完整文档见 [docs/altapi-pool.md](docs/altapi-pool.md)。

- **协议与每小时目录快照**：面向 `altapi.ai`（TokenPower，`new-api` 定制分支），显式 `provider=altapi`，默认 Base URL `https://altapi.ai/v1`，支持 `/v1/chat/completions`、`/v1/responses`、`/v1/messages`、`/v1/*` 与 `/v1/models`。服务启动后后台每小时固定刷新一次各组目录快照供下游 `/v1/models` 零上游延迟直接读取（冷启动未就绪返回 `502 models_unavailable`）。
- **真实余额与低余额停用门控**：通过 `internal/newapiconsole` 携带 `session` cookie + 真实 `New-Api-User` UID 读取 `/api/user/self` 的 `quota` 与 `/api/status` 的 `quota_per_unit`，与网页端 Current balance 完全一致；未绑定 session 时显示未知（绝不写 0）。提供 `lowBalanceDisable` 与 `minBalanceUsd`（默认预填 `0.50`）低余额调度门控。
- **精确换号与 stop 优先级**：对 `: insufficient_user_quota (request id:` 等特定文案采用 `switch-account` 动作**仅本次请求换号**（不冷却、不隔离、不扣健康分）；同时严格保留 `stop` 重试规则优先级（如 429、无效 Key、Cloudflare challenge 在应用冷却/隔离后立即终止请求）。
- **注册机、自动补号与代理工具**：内置 Python 注册机（`tools/altapi_registrar.py`，基于 `curl_cffi` 模拟浏览器 TLS/HTTP3 指纹，支持 SOCKS5 远端解析、IPv6 出口与批次稀释，用户名保留邮箱原貌），数据保存在 `data/altapi-signup.json`；支持**二选一自动补号**（按最小可用数阈值补号，或按分钟级随机时间范围频率注册，自动补号强制入池），以及候选筛选标签页（默认隐藏已入池）、退回注册机与批量修改代理。

---

### 3.13 Factory AI (`factory`)

完整文档见 [docs/factory-pool.md](docs/factory-pool.md) 与 [docs/factory-desktop-protocol.md](docs/factory-desktop-protocol.md)；出口 IP/网络指纹审计见 [docs/factory-egress-privacy.md](docs/factory-egress-privacy.md)。

- **定位与验证边界**：`provider=factory`，接收完整 `fk-` API Key（禁止跨池混用）。管理/额度源站默认 `https://api.factory.ai`（支持 `https://api.eu.factory.ai`）；推理源站默认 `https://prem.factory.ai`（留空时按源站推导，可显式覆盖）。**目前实测验证了 `GET /api/cli/whoami` 与 `GET /api/billing/limits`，管理链路、额度轮询（Standard / Core 各三窗口）与原生路由已本地验证，但真实模型调用尚未验证通过，不得视为可用或生产验收完成。**
- **每账号代理池 / 出口 IP / UA**：Factory 设置可配代理池与 UA 池，新账号、OAuth 登录、批量导入按池分配未占用代理和 UA；账号自己的代理/UA 优先，「为现有账号分配代理/UA」只补空字段。实际出口 IP 由代理决定，`EgressIP` 只是观察值；默认推理 UA `factory-cli/1.14.0`，管理面 UA `Bun/1.4.4` 可分别覆盖。
- **Factory 授权登录**：账号页「Factory 授权登录」走 WorkOS Device Authorization（`auth.factory.ai/device`），批准后服务端用 `access_token` + `refresh_token` 建号；访问令牌到期自动刷新，推理/额度共用；控制台不显示 Refresh Token，OAuth 账号支持「Factory 重新授权」。旧的 `fk-` Key 账号继续可用，两种账号同池调度。失败时会显示 WorkOS 环境诊断（生产/development client）、令牌有效期与实际出口，便于定位 401。
- **Droid 客户端身份与模型路由**：
  - 支持 `/v1/chat/completions`（映射到 `/api/llm/o/v1/chat/completions`）、`/v1/responses`（`/api/llm/o/v1/responses`）、`/v1/messages`（`/api/llm/a/v1/messages`）、Gemini 原生 `/v1beta/models/{model}:generateContent` / `:streamGenerateContent`（映射到 `/api/llm/g/v1/generate`）、`/v1beta/models/{model}:countTokens`（`/api/llm/g/v1/countTokens`）、`/v1/embeddings`（`/api/llm/o/v1/embeddings`）与 `GET /v1/responses/ws`（`wss://prem.factory.ai/api/llm/o/v1/responses/ws`）；`GET /v1/models` 返回本地配置参考列表（标注 `configured-reference-not-entitlement`，并列出 `auto`/`auto-fast` 虚拟路由）。
  - `auto` / `auto-fast` 由网关解析为同协议第一条已配置路由 / 第一条 `-fast` 路由（droid 客户端原本自己选 auto；这里只服务直接发送 auto 的下游）。已有非空自定义模型列表在重载时自动补入缺失的 Gemini generate 与 embedding 条目，不再需要点「填入抓包参考路由」；显式空数组仍保持清空。countTokens 上游路径和 Embeddings 响应形态按 generate/OpenAI 同族推断，未在抓包中直接命中；WebSocket 升级后不重试，且暂不计入 HTTP 统计。
  - 出站请求在 `internal/factory/droid.go` 中按 droid 1.14.0 抓包复刻客户端身份：推理用 `factory-cli/1.14.0`、管理/额度用 `Bun/1.4.4`；发送 `X-Factory-Client`、`X-Client-Version`、`X-Factory-Org-Id`、可配置的 `X-Factory-Request-Surface`（`cli_tui`/`cli_exec`）、`x-api-provider`、`configured_order`、W3C `traceparent`、会话派生的 `x-session-id` / `x-assistant-message-id`；可选的 `OpenAI-Platform` 仅在配置后发送，不伪造；同时剥离下游客户端指纹头并写入系统提示词固定开头。模型路由缺省已按抓包修正（`bedrock_anthropic`/`databricks`/`baseten`/`gpt-6.2-sol`/`generate+google`），并保留 `anthropic-tools` 的根级 `anyOf/oneOf/allOf` schema 包装/解包与 Fast Mode 参数。Responses WebSocket 已接入下游 `/v1/responses/ws`，HTTP `/v1/responses` 仍保留回退；Gemini 只走原生 generate/countTokens 路径，不参与 Chat/Responses/Messages 转换。
  - `factory.compatibility`（新建配置默认开启）支持在 Chat / Responses / Messages 六个方向做严格缓冲式有限转换（返回 `X-Compatibility-Mode: buffered`，总响应上限 16 MiB）；`factory.standardOnly` 可控制是否仅按 Standard 窗口门禁；命中 `content_policy_rejected` 时执行层强制终止，绝不换号重放。

---

### 3.14 Telnyx 注册机 (`telnyx`，无推理号池)

完整文档见 [docs/telnyxreg.md](docs/telnyxreg.md)。

- 控制台顶部 `Telnyx` 标签提供「Telnyx 注册机」与「Telnyx 注册设置」（仅注册机，暂无推理号池）。
- 后端由 `internal/telnyxreg` 全程 HTTP 驱动：自动从 Outlook 邮箱读信完成 Telnyx 注册、Turnstile 求解、一次性登录链接验证、建立 Portal 会话、创建 API Key 并读取试用额度，记录保存在 `data/telnyx-signup.json`。文档中的 $25 为历史实测记录，不保证当前赠金额度或资格。

---

## 4. 下游调用接口与开放导入 API

### 4.1 模型推理与目录接口

| 方法与路径 | 说明 |
|---|---|
| `POST /v1/chat/completions` | OpenAI Chat Completions（Perch 自动双向翻译，Deploy/50X/其余支持池原生转发，OpenCode/Factory 可选受限桥接） |
| `POST /v1/responses` | OpenAI Responses（OpenDesign / River / Ollama / Modal / Perch / TokenHarbor / Mistral / OpenCode / ALTAPI / Factory） |
| `POST /v1/responses/input_tokens` | Mistral Responses 输入 Token 只读计数接口 |
| `POST /v1/messages` | Anthropic Messages（OpenDesign / Ollama / Modal / TokenHarbor / OpenCode / ModelStudio / ALTAPI / Factory，错误按 Anthropic 格式返回） |
| `POST /v1/fim/completions` | Mistral 代码补全（FIM）接口 |
| `POST /v1/embeddings` | OpenDesign / Ollama / Mistral 向量嵌入接口 |
| `POST /v1/*` | 其他 `/v1/` POST 端点（按各号池 `poolPathOK` 白名单校验） |
| `GET /v1/models` | 当前下游密钥所属分组的可用模型列表（按号池＋分组缓存，默认最多 10 分钟，并发请求单飞合并；Modal 发布短名称；ALTAPI 直接读每小时后台快照；Factory 返回本地参考列表） |
| `GET /healthz` | 存活与健康检查，返回 `200 {"ok":true}` |

- **模型列表缓存**：命中后直接复用最终 JSON，时长由路由配置「模型列表缓存（秒）」（`routing.modelsCacheSec`，默认 `600`）控制（ALTAPI 使用独立每小时后台快照）。上游目录并集与本地权限过滤分层缓存，额度快照更新仅失效受影响分组的本地结果，账号/配置变更会使旧版本缓存失效。
- **鉴权头**：`Authorization: Bearer sk-odb-…` 或 `x-api-key: sk-odb-…`。
- **诊断响应头**：`X-Request-Id`（可在请求/错误/详情日志中关联查询）、`X-Attempts`（本次请求实际尝试的上游账号次数）。

### 4.2 开放导入接口（自动上号与暂存区）

供注册脚本或外部发号器调用，使用「下游密钥 → 导入密钥」新建的 `sk-odbimp-…` 鉴权（支持 `Authorization: Bearer` 或 `X-Import-Key` 头）。导入密钥只能导号，不能调用模型；同一 IP 连续鉴权失败 5 次后 60 秒内返回 `429`。详见 [docs/import-api.md](docs/import-api.md)。

#### (1) 通用号池导入 (`POST /open/v1/accounts/import`)

```sh
# 单条导入（OpenDesign 示例）
curl -X POST https://your-host.example/open/v1/accounts/import \
  -H 'Authorization: Bearer sk-odbimp-…' \
  -H 'Content-Type: application/json' \
  -d '{"runtimeKey":"sk-amr-…","controlKey":"vela_ctrl_…","name":"账号A","group":"default","note":"可选","proxyUrl":"socks5h://127.0.0.1:1080"}'

# 显式指定号池导入（Ollama / Mistral / TokenHarbor / OpenCode / Deploy / ALTAPI / Factory 等）
curl -X POST https://your-host.example/open/v1/accounts/import \
  -H 'Authorization: Bearer sk-odbimp-…' \
  -H 'Content-Type: application/json' \
  -d '{"runtimeKey":"<API_KEY>","provider":"ollama","name":"ollama-1"}'
```

- 支持批量 `{"accounts":[…]}`（单次最多 200 个，请求体上限 4 MiB，拒绝未知字段）；`runtimeKey` 必填（兼容 `apiKey` / `key` 字段名），ALTAPI 等池支持附加 `userId`。
- **无重复（HTTP 200）**：立即入池启用，备注自动写入导入时间与导入密钥名，有控制密钥或目录轮询的立即触发刷新。
- **完全相同（HTTP 200 `unchanged`）**：运行密钥、控制密钥、名称与现有账号完全一致时幂等返回 `unchanged`。
- **存在冲突（HTTP 202 `staged`）**：运行密钥、控制密钥或名称冲突时写入 `data/staging.json` 暂存区（上限 500 条），管理员可在后台选择**覆盖现有账号**、**作为新账号添加**或**丢弃**。

#### (2) Perch 会话导入 (`POST /open/v1/perch/import`)

```sh
curl -X POST https://your-host.example/open/v1/perch/import \
  -H 'Authorization: Bearer sk-odbimp-…' \
  -H 'Content-Type: application/json' \
  -d '{"accounts": [ {导出文档1}, {导出文档2} ], "group": "perch", "tags": ["batch-1"]}'
```

- 支持直接传 CLI 导出 JSON、批量 `accounts` 数组（最多 200 条）、扁平字段（`supabaseUserId` / `accessToken` / `refreshToken` / `expiresAt`）或 `sessionJson` 字符串。绑定到其他号池的导入密钥会被 `403` 拒绝。

### 4.3 返回给下游的状态码转换表

账号层面的局部故障由本服务消化并转换状态码，防止 `new-api` 把整个渠道误判为失效而自动关停：

| 上游或本地情况 | 下游收到的状态码与错误码 |
|---|---|
| 上游 401/403（尝试换号后仍失败） | `502 upstream_account_auth_failed` |
| 模型滚动窗口用尽 `model_limit_exceeded`、套餐并发上限 `tier_limit_exceeded`（上游均为 400） | `429` + 按网关重置时刻计算的精确 `Retry-After` |
| Mistral 后端容量不足（HTTP 429 + code 3505） | `429 upstream_backend_out_of_capacity` + 原始 `Retry-After`（不换号） |
| 余额不足 `insufficient_balance`（402/429）、套餐不含该模型 `tier_model_not_entitled` / Mistral 1910 | `503` |
| 上游网关自身供应商凭据故障 `upstream_provider_*`（上游 500） | `502`（不扣账号健康分、不冷却账号） |
| 其他上游 429 | `429` + `Retry-After` |
| 本地分组内所有账号并发/RPM 满载 | `429 pool_capacity` + 精确 `Retry-After` |
| 本地共享输入缓冲预算满载（`ODB_TH_BUFFER_MB`） | `429 gateway_buffer_limit` + `Retry-After: 1` |
| 分组内账号都在冷却（含该模型冷却） | `429 pool_cooling_down` + 最早恢复时间 |
| 分组内没有可用账号 | `503 pool_unavailable` |
| 目标号池被全局启停关闭（`poolSwitches[provider]=false`） | `503 pool_disabled`（拒绝该池新推理与目录请求，后台轮询停止） |
| 服务正在优雅停机排空 | `503 server_draining` |
| 上游把 400/422 参数错误包装成 5xx | 还原为 `400` / `422`（不冷却账号） |
| 其他 4xx（客户端参数错误等） | 原样透传 |
| 上游超时 / 连接或代理失败 | `504` / `502` |
| 客户端主动断开连接 | `499`（立即中止上游请求，不写错误日志、不扣健康分） |

---

## 5. 调度核心与内部机制

### 5.1 选号流程与 9 种调度策略

每个请求固定使用入口处的一份不可变配置快照。选号流程：**硬资格过滤 → 会话绑定命中 → 健康过滤（可选）→ 按优先级（`priority` 1–100，越小越优先）分组 → 组内按策略排序 → 并发/RPM 准入 → 取得租约**。排序、准入、计数、绑定在同一把锁内原子完成，中间无任何 I/O。

不可调度的账号：已禁用、所在号池被全局关闭、被硬隔离（`hardQuarantined`）、整号冷却中、该模型或该额度类别冷却中、模型不在账号目录中、额度快照证明无可用付费来源或未达余额门控（如 50X 余额 `<= 0`、ALTAPI 低于 `minBalanceUsd`）。

| 策略 (`routing.strategy`) | 排序逻辑 |
|---|---|
| `smart`（默认） | 余量充足的账号里，**最长窗口最早重置的先用**（按小时比较，避免剩余额度在重置时作废）；已用 ≥ `lowShare`（90%）排后，≥ `spentShare`（98%）再排后；再往后是靠套餐模型额度付费的（计费周期先结束的先用），只剩钱包可用的排最后；无额度数据的排在已知账号之后 |
| `pace` | 周窗口剩余比例 ÷ 距重置小时数，越高越先；相差 10% 以内视为同档，按近期 Token 消耗打散 |
| `least-used` | 已用比例最低优先，其次近期 Token 消耗最少 |
| `least-requests-1h` | 强制过滤低于 `minimumHealth` 的账号，同优先级内比较近 60 个分钟桶的已发推理次数＋待发预约，越少越优先（额度查询不计入） |
| `weighted` | 按账号 `weight`（1–100）平滑加权轮询（NGINX Smooth WRR） |
| `roundrobin` | 最久未被选中的账号优先 |
| `least-selections` | 累计选中次数最少优先，平局按当前在途数 |
| `least-connections` | 当前在途请求数最少优先 |
| `ordered` | 严格按配置顺序，前一个不可用或满载才用下一个 |

### 5.2 会话粘性与两阶段提交绑定

`new-api` 打开“透传请求头”后，本服务按以下顺序提取会话标识（Mistral 与 Token Harbor 有针对性微调）：

1. Responses 的 `previous_response_id`（上游按账号保存 response，在内存与 `metadata.json` 中记录对应账号）。
2. `routing.sessionHeaders` 中管理员配置的自定义会话头。
3. 父会话头：`X-Opencode-Parent-Session-Id`、`X-Parent-Session-Id`、`X-Parent-Session-Affinity`、`X-Codex-Parent-Thread-Id`、`X-Codex-Parent-Session-Id`、`X-Claude-Code-Parent-Agent-Id`、`X-Claude-Code-Parent-Session-Id`。
4. 请求体字段：`prompt_cache_key`、`session_id`、`conversation_id`、`thread_id`（含 `metadata.` 下同名字段）、Claude Code `metadata.user_id` / `user` 中的 `_session_UUID` 或 JSON 字符串。
5. 内置会话头：`X-Claude-Code-Session-Id`、`Session-Id`/`Session_id`、`X-Session-Id`、`X-Session-Affinity`、`Thread-Id`/`Thread_id`、`Conversation-Id`、`X-Codex-Session-Id`、`X-Pi-Session-Id`、`X-Opencode-Session-Id` 等。
6. 兜底弱粘性（`fallbackHash=true`）：首条 `system`/`developer` 与首条 `user` 消息文本（各截 4096 字节）的 HMAC。

- **安全与过期**：会话值经 `HMAC-SHA256(routingSecret, …)` 生成指纹并按号池/分组/下游 Key 隔离，原值与指纹均不写普通日志。内存绑定表采用 LRU（全局上限 4096 条），显式会话默认 2 小时（`explicitTtlSec=7200`）滑动过期，弱粘性 15 分钟（`fallbackTtlSec=900`）。
- **两阶段提交与忙时策略 (`onBindingBusy`)**：未命中时先写“待确认（pending）”绑定，请求真正写到上游后才确认；未发出即失败时仅由创建它的请求回滚删除。当绑定账号满载时：`overflow`（默认，TokenHarbor / OpenCode / Factory 除外）临时借用其他账号但不改原绑定；`wait-overflow` 先等待再借；`reject` 直接返回 429。绑定账号被禁用、冷却、隔离、额度耗尽、删除或更换密钥/代理时，绑定自动失效并改派。

### 5.3 并发控制与 60 秒滚动 RPM 窗口

- 每个账号可设 `maxConcurrent` 和 `maxRpm`（0 为不限）。当网关返回 `tier_limit_exceeded` 时自动学习套餐上限（`learnedConcurrency` / `learnedRpm`），与手设值取较小者生效，点「恢复」清除。
- **准入顺序固定**：先检查并发，再检查 RPM；并发满时不消耗 RPM。
- **精确 60 秒滚动窗口**：准入时先预留（reservation），请求真正写到上游后计入时间戳窗口，此后无论上游成败都不退还；请求未发出去就失败时立即退还预留。租约释放幂等。
- 全组账号满载时最多等待 `concurrencyWaitMs`（默认 2000ms），超时返回本地 `429 pool_capacity` 和精确 `Retry-After`，记为 `errorCategory: capacity`，不计入健康样本。

### 5.4 换号规则、错误规则与重试规则

- **换号边界**：每个请求最多尝试 `maxAccountsPerRequest`（默认 2，范围 1–5）个账号，不重复选同一账号。**只在首包之前**、且满足以下任一条件时换号：① 错误规则将该账号/模型标记为 `switch-account`、冷却或隔离（且未被 `stop` 重试规则拦截）；② 上游返回 401；③ 请求根本未发出（连接/代理失败）。普通 5xx 或超时不换号；**首包发出后绝不重放**。
- **`errorRules`（有序，首条命中生效）**：各号池独立保存，匹配 HTTP 状态码、正文关键字（大小写不敏感子串）和响应头；动作为 `ignore` / `switch-account`（仅本次请求跳过该账号，不冷却、不扣健康分）/ `degrade` / `cooldown` / `hard-quarantine`，作用域为整号（`account`）、单模型（`model`）或套餐类别（`class`）。冷却时长优先级：响应头 `Retry-After` → 正文 `bodyAfter` RFC3339 时刻 → `fallback`，不超过 `max`。未命中任何规则时仅记一次降级健康样本，不会自动冷却。
- **`retryRules`（坏请求快速止损）**：默认命中 400/404/413/422 时立即停止换号并原样返回，避免单个坏参数打穿全池账号。OpenDesign 等池若同一响应被 `errorRules` 判定为账号级限额/资格问题则照常换号；ALTAPI 等池则严格保留显式 `stop` 优先级（仍对当前号执行冷却/隔离，但不继续换号）。

### 5.5 流式首包防抖与心跳

流式请求会等待读取**第一个完整的 SSE 事件**并确认不是错误事件后，才开始向下游写入响应头与数据，因此首包前的流内错误仍可无感换号。流式首包超时 `firstEventTimeoutSec` 默认 120 秒，空闲超时 `idleTimeoutSec` 默认 360 秒；开始转发后每 `heartbeatSec`（默认 25 秒）在 SSE 事件边界写一行 `: PING` 心跳。

### 5.6 模型价格表与谷峰/长上下文计费

「路由设置 → 模型价格」按每百万 Token 配置各模型的输入、缓存命中、输出价格（原币种 `USD` / `CNY`，不换算），支持全局及分池独立价格表：
- **DeepSeek 谷峰计费**：基础价格填空闲价，「高峰倍数」填 `2`；北京时间工作日 `09:00–12:00`、`14:00–18:00` 按倍数计费，法定节假日可在「高峰例外日」中排除。
- **调价生效日期与长上下文阶梯价**：同一模型可配置多行按「生效日期」匹配；提示 Token 达到阈值时整笔请求按长上下文单价计算。

### 5.7 日志分段、内存环与脱敏规范

- **请求日志 (`data/logs/requests-*.jsonl`)**：每笔请求一行终态记录（请求 ID、下游密钥、分组、号池、入站/出站协议、模型、选中账号、选号原因、会话来源、绑定结果、状态码、耗时、TTFT、TPS、Token、费用、尝试轨迹 `attempts` 与决策链 `workflow`）。
- **错误日志 (`data/logs/errors-*.jsonl`)**：每次失败的上游尝试一行，通过 `requestId + attemptIndex` 与请求日志关联；错误正文先脱敏再截断至 16 KiB。
- **详细日志 (`data/logs/details-*.jsonl`)**：**默认关闭**（TokenHarbor、Deploy、50X、ALTAPI、Factory 等另有分池独立 `detailLog` 开关；50X 还可独立开关 `requestLog` / `errorLog`）。开启后记录脱敏后的请求/响应头与正文（流式记原始 SSE，单块按 `detailBodyKB` 截断，内存环上限 256 MiB）。
- **异步无阻塞落盘**：单个分段文件 5 MiB，默认保留 7 天（`logs.retentionDays`，上限 365 天）、总磁盘上限 100 MiB。查询读内存环，写入单队列异步刷盘（每秒 `flush`），队列满（默认 64 MiB）时丢弃日志并计数，**模型流量永远不等待磁盘 I/O**。

### 5.8 全局号池启停 (`poolSwitches`)

管理台侧栏「号池管理 / 启停」（接口 `GET /api/pools`、`PUT /api/pools/{provider}`）支持对全部 14 个推理号池及 `telnyx` 注册池独立启停：
- `config.json` 中的 `poolSwitches` 为稀疏映射（缺省视为启用，升级不自动关停既有池），修改带 `revision` 冲突保护并持久化落盘。
- 关闭号池后：控制台隐藏其日常导航标签；新进入的推理与 `/v1/models` 请求立即返回 `503 pool_disabled`；后台额度轮询、Perch 续期/探活、ALTAPI 每小时目录同步与自动补号、以及各池注册任务均通过池级 context 取消（迟到的旧 epoch 结果不会回填）；已在途请求允许自然结束。
- 关闭号池**不删除任何账号、配置或历史日志**，也不改动账号自身的 `enabled` 状态；即使全部号池均被关闭，管理页依然可正常打开并一键恢复。

---

## 6. 数据目录文件与热加载 (`DATA_DIR`)

所有文件权限强制为 `0600`、目录权限为 `0700`，写盘一律采用「写同目录临时文件 → `fsync` → 原子 `rename`」。JSON 损坏时启动直接报错退出并保留现场文件。**整个 `data/` 目录均为核心机密，严禁提交到 Git 或对外共享。**

| 文件 / 路径 | 用途与刷盘策略 |
|---|---|
| `config.json` | 全局开关（`poolSwitches`）、账号（含运行/控制密钥）、下游/导入密钥哈希、全局与各号池独立的路由/额度/日志/错误与重试规则/价格表、管理员密码哈希（PBKDF2-SHA256）。支持编辑后 `kill -HUP <pid>` 或 `POST /api/config/reload` 热加载，在途请求继续使用旧快照 |
| `metadata.json` | 账号运行状态（冷却/硬隔离/学习到的并发与 RPM 上限）、健康样本、各号池额度与目录快照、Responses/Mistral 持久化绑定、24h 分钟桶统计、32 字节 `routingSecret`。冷却/隔离/额度/配置变化 15s 内写盘；仅统计和健康样本变化最多每 60s 写一次 |
| `selection-counters.json` | 各账号累计选中次数、最后选中序号与近期每分钟请求计数，最多每 5 秒写一次 |
| `sessions.json` | Web 管理台登录会话持久化文件 |
| `staging.json` | 开放导入接口检测到冲突的暂存账号（含密钥，上限 500 条），等待管理员覆盖、添加或丢弃 |
| `import-log.json` | 开放导入接口审计记录（调用方 IP、时间、导入密钥 ID、各状态计数，每池独立保留最近 1000 条） |
| `perch-secrets.json` | Perch 号池加密会话（Access / Refresh Token、Supabase URL 与 Key），AES-256-GCM 封装（密钥由 `routingSecret` 派生） |
| `perch-device.json` | Perch 每账号独立加密的设备标识（随机 UUID `machine-id`、首次运行与发消息时间）；**务必与 `metadata.json` 成套备份** |
| `perch-signup.enc` / `perch-autosignup.json` | Perch 自动补号的加密候选账号/代理库存与自动补号任务配置 |
| `tokenharbor-signup.json` | Token Harbor 注册机候选凭据、GitHub/邮箱会话与 API Key 记录（AES-256-GCM 加密封装） |
| `mistral-signup.json` | Mistral 注册机候选凭据、代理库存与状态持久化 |
| `deploy-signup.json` | Deploy 注册机候选凭据、会话、绑卡与代理库存 |
| `x50-signup.json` | 50X AI 注册机候选凭据、API Key 与代理配置 |
| `altapi-signup.json` | ALTAPI 注册机候选凭据、session cookie、代理库存与自动补号配置 |
| `telnyx-signup.json` | Telnyx 注册机候选凭据、Portal 会话、API Key 与 promo credits 记录 |
| `logs/` | `requests-*.jsonl`、`errors-*.jsonl`、`details-*.jsonl`、`perch-*.jsonl`、`tokenharbor-*.jsonl` 等分段日志目录 |

---

## 7. 核心配置项字典 (`routing` / `quota` / `logs`)

### `routing`（全局及各池独立路由配置）

下表为全局基线，分池默认值可能不同。例如 Deploy / 50X / ALTAPI / Factory 默认采用 `least-connections`，TokenHarbor / OpenCode / Factory 的绑定忙时策略默认为 `reject`。

| 字段 | 默认值 | 说明 |
|---|---|---|
| `strategy` | `"smart"` | 选号策略：`smart` / `pace` / `least-used` / `least-requests-1h` / `weighted` / `roundrobin` / `least-selections` / `least-connections` / `ordered` |
| `sticky` | `true` | 是否开启会话粘性 |
| `onBindingBusy` | `"overflow"` | 绑定账号满载时的行为：`overflow`（临时借号不改绑）/ `wait-overflow`（先等待再借）/ `reject`（直接 429） |
| `explicitTtlSec` / `fallbackTtlSec` | `7200` / `900` | 显式会话绑定（2h）与消息哈希弱粘性（15m）的滑动过期秒数 |
| `fallbackHash` | `true` | 无显式会话标识时是否按首条 system/user 消息 HMAC 做弱粘性 |
| `sessionHeaders` | `[]` | 管理员自定义额外会话请求头列表（最高优先级） |
| `concurrencyWaitMs` | `2000` | 分组满载时的本地排队等待毫秒数（`0–30000`） |
| `maxAccountsPerRequest` | `2` | 单次请求首包前最多尝试的账号数（`1–5`） |
| `ignoreCatalog` | `false` | 是否忽略各账号 `/v1/models` 模型目录权限过滤 |
| `walletFallback` | `true` | OpenDesign 窗口与套餐模型额度均用尽后，是否允许调度仅剩钱包/充值余额的账号 |
| `lowShare` / `spentShare` | `90` / `98` | `smart` 策略下已用额度达到多少百分比开始排后 / 视为耗尽 |
| `healthFilter` / `minimumHealth` | `false` / `0.2` | 是否开启 24h 成功率过滤及最低健康阈值（样本数 < 20 视为健康） |
| `firstEventTimeoutSec` / `idleTimeoutSec` / `heartbeatSec` | `120` / `360` / `25` | 流式首包超时、流式空闲超时、`: PING` 心跳间隔（秒） |
| `requestTimeoutSec` | `900` | 非流式请求总超时（秒） |
| `maxBodyMB` | `64` | 下游请求体最大允许大小（MiB） |
| `modelsCacheSec` | `600` | `/v1/models` 响应缓存秒数（ALTAPI 固定每小时后台刷新，不受此项控制） |
| `directMaxConns` / `proxyMaxConns` | `512` / `32` | 直连与单个代理 Transport 的单 Host 最大并发/空闲连接数 |

### `quota`、`logs` 与账号级字段

- **`quota`**：`enabled`（`true`）、`intervalSec`（`60`，轮询间隔秒数；Deploy / 50X / ALTAPI / Factory 等分池默认 `600`）、`concurrency`（`2`，轮询并发数）、`staleSec`（`900`，快照过期秒数）。
- **`logs`**：`retentionDays`（`7`，范围 `1–365`）、`maxRequests`（`50000`）、`maxErrors`（`10000`）、`maxDetails`（`500`）、`detailBodyKB`（`6144`，范围 `1–6144`）、`maxTotalMB`（`100`）、`detail`（`false`）。
- **账号级字段**：`maxConcurrent`、`maxRpm`、`weight`（`1–100`）、`priority`（`1–100`，默认 `50`）、`userId`（ALTAPI 等池真实 UID）、`proxyUrl`（支持 `http://`、`https://`、`socks5://`、`socks5h://` 及 IPv6 出口地址，**配置代理后若代理连接失败绝不回退直连**）、`egressIp`（OpenCode Go 固定出口 IP）。

---

## 8. 架构限制、回滚与开发验证

### 架构限制
- **单进程有状态设计**：并发租约计数、60 秒滚动 RPM 窗口、内存会话绑定表均在单进程内存中维护，不支持多副本挂载同一 `DATA_DIR` 并发读写。
- **OpenDesign 自动充值未知**：控制面接口不返回账号是否开启了自动充值，因此窗口和各项余额均为 0 的账号会被判为耗尽而不调度。
- **Python 注册机运行环境**：Perch / Mistral / Deploy / ALTAPI 注册机依赖 Python 3.10+、`curl_cffi`、Playwright 与 Google Chrome Stable（已在 `Dockerfile` 中完整内置；宿主机直接运行需按需配置对应 Python 虚拟环境）。

### 备份与回滚
- 升级前请完整备份 `data/config.json`、`data/metadata.json` 以及所有 `data/*.enc` / `data/*-signup.json` / `data/perch-*.json` 文件（注意：`perch-secrets.json`、`perch-device.json`、`perch-signup.enc`、`tokenharbor-signup.json` 等均依赖 `metadata.json` 中的 `routingSecret` 解密，**必须成套备份与恢复**）。
- 回滚时停止进程、切回旧版本二进制或旧容器镜像，挂载原数据目录启动即可。若 `selection-counters.json` 损坏可直接删除重建；`config.json` 与 `metadata.json` 不可随意删除。

### 开发与测试

```sh
# 静态检查与全量 Race 竞态测试（全部使用本地临时目录与 mock 上游，不触碰任何真实凭据）
go vet ./... && go test -race ./...

# 编译主服务与 50X 注册机 CLI
go build -o odb ./cmd/odb
go build -o x50reg ./cmd/x50reg
```
