# account-pool-balancer (`odb`)

<p align="center">
  <img src="docs/logo.svg" width="128" height="128" alt="account-pool-balancer logo" />
</p>

多上游 AI 账号池负载均衡与协议代理服务。对下游（`new-api`、`One API`、`Sub2API` 等中转网关或 `Claude Code`、`Codex`、`OpenCode`、`pi` 等客户端）提供统一的下游 API 密钥（`sk-odb-…`）和标准 OpenAI / Anthropic 兼容接口，把请求智能分发到多个上游号池中，负责**选号调度、会话与缓存粘性、并发与滚动 RPM 控制、多源额度轮询、错误重试与故障隔离、自动注册补号、统计与脱敏日志**，并内置完整的现代化 Web 管理台。

单个 Go 二进制编译运行，无外部数据库依赖。

---

## 目录

- [1. 支持的 9 大上游号池总览](#1-支持的-9-大上游号池总览)
- [2. 快速开始与部署](#2-快速开始与部署)
  - [2.1 本地编译与运行](#21-本地编译与运行)
  - [2.2 启动参数与环境变量](#22-启动参数与环境变量)
  - [2.3 Docker 部署与生产规范](#23-docker-部署与生产规范)
- [3. 各号池详细接入与设计边界](#3-各号池详细接入与设计边界)
  - [3.1 OpenDesign 号池 (`opendesign`)](#31-opendesign-号池-opendesign)
  - [3.2 River 号池 (`river`)](#32-river-号池-river)
  - [3.3 Ollama 号池 (`ollama`)](#33-ollama-号池-ollama)
  - [3.4 Modal 号池 (`modal`)](#34-modal-号池-modal)
  - [3.5 Perch 号池 (`perch`) — Supabase 会话池与注册机](#35-perch-号池-perch--supabase-会话池与注册机)
  - [3.6 Token Harbor 号池 (`tokenharbor`)](#36-token-harbor-号池-tokenharbor)
  - [3.7 Mistral 号池 (`mistral`)](#37-mistral-号池-mistral)
  - [3.8 OpenCode Zen / Go 号池 (`opencode-zen` / `opencode-go`)](#38-opencode-zen--go-号池-opencode-zen--opencode-go)
  - [3.9 Model Studio 阿里云百炼号池 (`modelstudio`)](#39-model-studio-阿里云百炼号池-modelstudio)
- [4. 下游调用接口与开放导入 API](#4-下游调用接口与开放导入-api)
  - [4.1 模型推理与目录接口](#41-模型推理与目录接口)
  - [4.2 开放导入接口（自动上号与暂存区）](#42-开放导入接口自动上号与暂存区)
  - [4.3 返回给下游的状态码转换表](#43-返回给下游的状态码转换表)
- [5. 调度核心与内部机制](#5-调度核心与内部机制)
  - [5.1 选号流程与 8 种调度策略](#51-选号流程与-8-种调度策略)
  - [5.2 会话粘性与两阶段提交绑定](#52-会话粘性与两阶段提交绑定)
  - [5.3 并发控制与 60 秒滚动 RPM 窗口](#53-并发控制与-60-秒滚动-rpm-窗口)
  - [5.4 换号规则、错误规则与重试规则](#54-换号规则错误规则与重试规则)
  - [5.5 流式首包防抖与心跳](#55-流式首包防抖与心跳)
  - [5.6 模型价格表与谷峰/长上下文计费](#56-模型价格表与谷峰长上下文计费)
  - [5.7 日志分段、内存环与脱敏规范](#57-日志分段内存环与脱敏规范)
- [6. 数据目录文件与热加载 (`DATA_DIR`)](#6-数据目录文件与热加载-data_dir)
- [7. 核心配置项字典 (`routing` / `quota` / `logs`)](#7-核心配置项字典-routing--quota--logs)
- [8. 架构限制、回滚与开发验证](#8-架构限制回滚与开发验证)

---

## 1. 支持的 9 大上游号池总览

每个号池在 **分组归属、下游密钥（`sk-odb-…`）、导入密钥（`sk-odbimp-…`）、路由参数、错误/重试规则、导入日志（每池独立保留 1000 条）** 上完全隔离，绝不跨池借号、绝不混放：

| 号池 (`provider`) | 默认分组 | 凭证形态与自动识别前缀 | 支持的下游路径 | 额度感知与目录同步 | 专项文档 |
|---|---|---|---|---|---|
| **OpenDesign**<br>`opendesign` | `default` | 运行密钥 `sk-amr-…`<br>控制密钥 `vela_ctrl_…`（可选） | `/v1/chat/completions`<br>`/v1/responses`<br>`/v1/messages`<br>`/v1/*`、`/v1/models` | 控制密钥每 60s 读工作区窗口、套餐模型额度、钱包与 `billing/preflight`；每 10m 读 `/v1/models`；无控制密钥靠错误推断 | 见 [§3.1](#31-opendesign-号池-opendesign) |
| **River**<br>`river` | `river` | API Key `rv_…`（无控制密钥） | `/v1/chat/completions`<br>`/v1/responses`<br>`/v1/models` | 无额度接口，靠 402/429 错误规则分级翻倍冷却；每把 Key `/v1/models` 作为可用表 | 见 [§3.2](#32-river-号池-river) |
| **Ollama**<br>`ollama` | `ollama` | API Key（显式传 `provider=ollama` 或 `oll-` 前缀） | `/v1/chat/completions`<br>`/v1/completions`<br>`/v1/responses`（无状态）<br>`/v1/messages`<br>`/v1/embeddings`、`/v1/models` | 同一把 Key 读取 `GET /api/balance`（赠送额度、`session`/`weekly` 百分比、已购额度），单号最小间隔 30s | 见 [§3.3](#33-ollama-号池-ollama) |
| **Modal**<br>`modal` | `modal` | Proxy Token<br>`wk-<id>.ws-<secret>` | `/v1/chat/completions`<br>`/v1/responses`<br>`/v1/messages`、`/v1/models` | 无余额接口，靠 `/v1/models` 免费验活并同步 Endpoint，统一短名称自动映射 Endpoint hostname；402 整号冷却 | [docs/modal-import-runbook.md](docs/modal-import-runbook.md) |
| **Perch**<br>`perch` | `perch` | Supabase 会话 JSON（Access + 旋转 Refresh Token） | `/v1/chat/completions`<br>`/v1/responses`<br>`/v1/models` | 自动续期会话 + GoTrue 状态检测 + `/api/perchai/account` 与 `usage_meter` RPC（PT 额度）+ `/api/perchai/models` | 见 [§3.5](#35-perch-号池-perch--supabase-会话池与注册机) |
| **Token Harbor**<br>`tokenharbor` | `tokenharbor` | API Key `thk_…` + 注册原记录会话证明 | `/v1/chat/completions`<br>`/v1/responses`<br>`/v1/messages`<br>`/v1/models` | 经账号固定代理只读 `/pricing`（Pass 档位）与 `/api/me/free-tier`（`plan_used_pct`、周期与 `overage_billing` 门禁） | [docs/tokenharbor.md](docs/tokenharbor.md) |
| **Mistral**<br>`mistral` | `mistral` | Studio / Vibe API Key（`mstrl_` 或显式声明） | `/v1/chat/completions`<br>`/v1/responses`（无状态）<br>`/v1/responses/input_tokens`<br>`/v1/fim/completions`<br>`/v1/embeddings`、`/v1/models` | 接入 Studio / Vibe 套餐额度与组织订阅刷新（百分比推算约值），模型目录独立同步，内置混合协议注册机 | [docs/mistral.md](docs/mistral.md)<br>[docs/mistral-capacity.md](docs/mistral-capacity.md) |
| **OpenCode**<br>`opencode-zen`<br>`opencode-go` | `opencode-zen`<br>`opencode-go` | Zen / Go 授权 Key（8–512 ASCII，默认关闭） | `/v1/chat/completions`<br>`/v1/responses`<br>`/v1/messages`、`/v1/models` | Zen/Go 独立能力目录与协议 Base、Go 三窗口 usage 轮询、Go 固定出口 IP 强校验、可选受限跨协议桥接与跨套餐回退 | [docs/opencode.md](docs/opencode.md)<br>[docs/opencode-audit.md](docs/opencode-audit.md) |
| **Model Studio**<br>`modelstudio` | `modelstudio` | 阿里云百炼 Key（按量 `sk-…` / Token Plan `sk-sp-…`） | `/v1/chat/completions`<br>`/v1/messages`<br>`/v1/models` | 按套餐（`payg` / `tokenplan`）与 4 大地域自动路由对应 `dashscope` / `token-plan` 接入点并同步模型目录 | 见 [§3.9](#39-model-studio-阿里云百炼号池-modelstudio) |

---

## 2. 快速开始与部署

### 2.1 本地编译与运行

```sh
go build -o odb ./cmd/odb
ODB_ADMIN_PASSWORD='至少8位的密码' ./odb -addr :8790 -data ./data
```

打开 `http://127.0.0.1:8790/`：

1. **添加账号**：
   - **OpenDesign 设备授权**：「账号 → 设备授权添加」，拿到验证码和激活链接，在**已登录目标 OpenDesign 账号**的浏览器中打开并批准，账号连同 `sk-amr-…` 运行密钥和 `vela_ctrl_…` 控制密钥自动入池。
   - **手动添加 / 批量导入**：切换到对应号池标签页，按每行 `运行密钥[,控制密钥[,名称[,代理URL]]]` 导入（无控制密钥的池为 `API Key[,名称[,代理URL]]`；Perch 支持单文件/多选/拖拽/ZIP 导入 JSON）。
2. **创建下游密钥与导入密钥**：「下游密钥 → 新建密钥」生成 `sk-odb-…`（仅显示一次）。如需脚本自动上号，在同一页「新建导入密钥」生成 `sk-odbimp-…`。
3. **对接 new-api**：在 `new-api` 中新建渠道，类型选 OpenAI（或 Anthropic Claude），Base URL 填本服务地址，密钥填 `sk-odb-…`，**务必打开“透传请求头”**。

> 未设置 `ODB_ADMIN_PASSWORD` 时，第一次打开管理台会强制要求设置管理员密码。服务暴露到公网前务必先设好密码。

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

### 2.3 Docker 部署与生产规范

```sh
cat > .env <<'X'          # 已在 .gitignore 中，权限建议 0600
ODB_ADMIN_PASSWORD=...     # 只在第一次启动、还没设密码时生效
ODB_UID=1000               # 容器以宿主用户运行，./data 属于该用户
ODB_GID=1000
X
mkdir -p data && chmod 700 data
docker compose up -d --build
```

- **容器内网与兼容别名**：容器加入共享网络 `ai-internal`（与 `new-api` 同一网络），同时具备 `account-pool-balancer` 与兼容别名 `opendesign-balancer`。`new-api` 渠道 Base URL 填 `http://account-pool-balancer:8790`。
- **安全绑定**：管理台仅绑定宿主机 `127.0.0.1:8790`，远程访问请用 SSH 隧道 `ssh -L 8790:127.0.0.1:8790 主机`，或通过反向代理加 HTTPS。
- **优雅停机**：停止时先拒绝新请求入场（返回 `503 server_draining`），等待在途请求最多 30 秒（Compose `stop_grace_period: 35s`），并等待后台额度轮询与注册机任务收尾、刷盘计数与元数据后再关闭日志。
- **OVH 生产环境例外**：当前线上实例从固定版本快照构建镜像，禁止直接在旧工作树执行 `docker compose up --build`。完整预检、构建、备份切换及双内网入口（`account-pool-balancer:8790/healthz` 与 `opendesign-balancer:8790/healthz`）验收流程见 [OVH 部署记录](docs/ovh-deployment.md)。

---

## 3. 各号池详细接入与设计边界

### 3.1 OpenDesign 号池 (`opendesign`)

依据官方[模型额度说明](https://open-design.ai/zh/model-credits/)、[价格页](https://open-design.ai/zh/pricing/)与客户端规范 `specs/current/coding-plan-client-billing.md`：

- **两类模型与扣费顺序**：
  1. **Design Plan 模型**（DeepSeek V4/V4.1、MiMo、GLM-5.3 Flash、Kimi K2.7 Code、Claude Haiku 5.5、GPT-6 Luna 等）：先扣 Design Plan 7 天滚动窗口（Go 档另有 5 小时窗口），用尽后扣**套餐模型额度**（`balances.subscriptionCredits`）。
  2. **套餐模型**（Claude Sonnet/Opus/Fable、GPT-6 Sol/Astra、Gemini、Grok、图片模型等）：不走窗口，直接扣套餐模型额度。
  3. 两者均用尽后请求停止，除非开启了 `walletFallback` 且钱包/充值额度（`rechargeCredits`）有余额。
- **套餐模型目录与类别判定**：
  - 每 10 分钟通过每把 API Key 自身的 `/v1/models` 免费同步可用模型（Go 档列出 12 个 Design Plan 模型；Plus 档 39 个；Pro/Max 档 42 个，多出 `claude-fable-5.1`、`claude-opus-5.5`、`gpt-6-astra`）。不在该账号列表里或标 `enabled:false` 的模型不会派给该账号；全组都不支持时直接返回 `503 model_not_entitled`，不打上游（`ignoreCatalog` 可关闭此检查）。
  - 模型属于哪一类以 `billing/preflight?modelId=` 的 `modelCovered`（`true` 为 Design Plan 模型）为准，对近期请求过的模型逐账号查询并缓存 6 小时（价格页的限时活动如“GPT-6.1 Sol 加入 Design Plan”仅在应用内生效，API 下 `modelCovered` 实测为 `false`）。
- **控制面额度轮询**：
  - 有 `vela_ctrl_…` 控制密钥的账号每 60 秒读取一次工作区、套餐窗口、钱包和账单摘要，全局并发 2，失败按 `60s × 2ⁿ` 退避（上限 15 分钟）。聊天请求绝不等待额度刷新。
  - 各分项独立保存，不相加、不用 `creditsPerUsd` 换算。读取失败时保留上次成功值，绝不写成 0；控制密钥 401/403 时标记「控制密钥失效」并按额度未知处理；快照超过 15 分钟视为过期。
  - **严格排除条件**：只有拿得出新鲜证据证明「该模型可用窗口无余量 + 账单摘要显示套餐模型额度为 0 + 钱包和充值额度为 0（或关闭了 `walletFallback`）」时才排除账号。概览页的窗口剩余按工作区去重求和，多账号共享同一工作区时只算一次并标注「共享工作区」。
- **仅 API Key 模式（无控制密钥）**：
  - 只填 `sk-amr-…` 完全可用：可用模型照样按该 Key 的 `/v1/models` 判断；模型类别优先复用池内其他带控制密钥账号的 preflight 结果，否则使用内置的 12 个 Design Plan 模型清单。
  - 额度耗尽靠上游错误推断：`insufficient_balance` 出现在 Design Plan 模型上说明窗口、套餐额度、钱包全空，**整号冷却**；出现在套餐模型上，**仅冷却该账号的全部套餐模型**，Design Plan 模型照常调度。冷却从 10 分钟起连续命中逐次翻倍（上限 6 小时），成功一次清零。
  - 选号时额度未知的账号排在已知且有余量的账号之后；界面显示「仅 API Key」、24h 请求数/Token、推断冷却状态与连续失败次数。

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

### 3.2 River 号池 (`river`)

- 支持 [River](https://docs.river.ai/) API Key（`rv_` 开头自动识别为 `river`，默认进 `river` 组），上游 `https://api.river.ai/v1`（`upstream.riverBase` 可改），鉴权 `Authorization: Bearer rv_…`，无控制密钥。
- **仅支持 OpenAI Chat Completions 与 Responses**（`/v1/chat/completions`、`/v1/responses`、`/v1/models`）。收到 `/v1/messages`（Anthropic）等其他路径时本地直接返回 `404 unsupported_endpoint`，不打上游（Claude Code 等 Anthropic 客户端不可接 River 池）。
- 每把 Key 的 `/v1/models` 作为可用模型表（形如 `deepseek-ai/DeepSeek-V4.1-Flash`）。
- 额度未知，靠独立规则 `riverErrorRules` / `riverRetryRules` 推断：402（余额不足）逐次翻倍冷却，429 按 `Retry-After` 冷却，401 冷却后换号。已有实例升级后需在「路由设置 → River 号池」点「恢复默认规则」再保存。

---

### 3.3 Ollama 号池 (`ollama`)

- 支持云端 `https://ollama.com/v1`（`upstream.ollamaBase` 可改）或自建 `http://host:11434/v1`。
- **原生三协议直通**（无需协议转换）：
  - OpenAI Chat Completions：`/v1/chat/completions`（另含 `/v1/completions`、`/v1/embeddings`）原样转发。
  - OpenAI Responses：`/v1/responses` 原样转发（Ollama 云端仅支持无状态版本，不支持 `previous_response_id` / `conversation`）。
  - Anthropic Messages：`/v1/messages` 原样转发（Claude Code 可直接接；客户端漏传 `anthropic-version` 时自动补 `2023-06-01`）。
- **凭证识别与鉴权**：Ollama Key 无固定官方前缀，管理台在「Ollama 号池」标签下添加或开放导入传 `"provider":"ollama"`（也可给 Key 加 `oll-` 前缀自动识别）。由于 `ollama.com` 的 `/v1/messages` 拒绝单独的 `x-api-key`，本服务对 Ollama 账号**仅发送 `Authorization: Bearer`**。
- **额度接口 (`GET /api/balance`)**：同一把 Key 直接读取赠送额度（`included.balance_usd` / `allowance_usd` 与计费周期）、老套餐 `session` / `weekly` 剩余百分比、已购额度 `purchased.balance_usd`。三项分开展示，仅当「赠送额度用完且已购额度为 0」时判为耗尽（读失败或过期绝不当作 0）。该接口限频每用户 10 次/分钟，同一账号两次读取至少间隔 30 秒。
- **模型目录与计费**：云端 `/v1/models` 对所有 Key 相同（不做按账号裁剪）；模型名带 tag（如 `gpt-oss:20b`、`gemma4:31b`、`kimi-k2.7-code`），价格表需按同样名称配置。
- 独立错误规则 `ollamaErrorRules` / `ollamaRetryRules`：401 `invalid credentials` 隔离换 Key，403 账号停用隔离，429 按 `Retry-After` 冷却，500/502/503/504 短冷却换号。

---

### 3.4 Modal 号池 (`modal`)

支持 [Modal Shared Endpoints](https://modal.com/docs/guide/shared-endpoints)，默认上游 `https://inference.us-west.modal.direct/v1`（`upstream.modalBase` 可改，漏写 `/v1` 启动自动补齐）。

| 维度 | 其他号池 | Modal 号池 |
|---|---|---|
| 请求 Base URL | 同一个 Base | 同一个 Base（`inference.<region>.modal.direct/v1`） |
| 下游 `model` 字段 | 模型名（如 `claude-sonnet-5`） | **统一短名称**（如 `glm-5-3`），发往上游时自动替换为选中账号的 Endpoint hostname |
| 可用模型范围 | 服务商目录 | 该 Token 可访问的 Endpoint（由 `GET /v1/models` 列出，每 10 分钟缓存） |
| 鉴权头 | `Authorization: Bearer <key>` | 仅发 `Authorization: Bearer wk-<id>.ws-<secret>`（不发 `x-api-key`） |

- **完整 Token 校验**：Proxy Token 完整形态为 `wk-<id>.ws-<secret>`（两段缺一不可）。若只填 `wk-<id>`，上游所有路径均返回 `401 proxy auth required: … supply your proxy token as Bearer wk-<id>.ws-<secret>`，本服务会将其精准识别并标注为 **「Token 不完整」**，而非当作普通上游故障。
- **统一模型短名称映射**：`GET /v1/models` 将池内各账号的 Endpoint 按基础模型合并去重，发布统一短名称：`glm-5-3`、`glm-5-3-flash`、`deepseek-v4-1-flash-1017`、`kimi-k3`、`qwen3-8-max`（对应 `Qwen/Qwen3.8-Max-VL-Thinking`）；原始 `base_model_id` 和完整 hostname 仍可直接调用。
- **三协议支持与缓存亲和**：支持 `/v1/chat/completions`、`/v1/responses`、`/v1/messages`（自动补 `anthropic-version: 2023-06-01`）；`/v1/embeddings` 本地直接 404。自动将本地算出的**会话 HMAC 指纹**写入上游 `Modal-Routing-Affinity-Key` 头，使同一轮对话在 Modal 侧落到同一容器以命中前缀缓存。
- **零成本验活与独立错误规则 (`modalErrorRules`)**：Modal 无余额接口（概览页显示每把 Token 的可用 Endpoint 数），额度轮询通过免费的 `/v1/models` 检验 Token 死活：

| 规则 ID | 匹配条件 | 内部动作 | 回下游状态码 |
|---|---|---|---|
| `modal-token-invalid` | 401（`invalid proxy auth credentials`） | 隔离账号（更换 Token 后点“恢复”） | `502` |
| `modal-token-forbidden` | 403（无权访问该 Endpoint 或 RBAC 限制） | 隔离账号 | `503` |
| `modal-insufficient-balance` | 402（余额不足 / spend limit） | 整号冷却，10 分钟起逐次翻倍至 6 小时 | `503` |
| `modal-concurrency-limited` | 429 | **仅冷却该模型**（按 `Retry-After`，30s～10m），换号 | `429` |
| `modal-unavailable` | 500/502/503/504（容器替换/抢占/冷启动） | 账号短冷却 | — |

> 请求路径上的 `404 unknown inference model` 命中重试规则 `modal-bad-request`，立即停止换号并原样返回，不冷却账号。自动上号流程参见 [docs/modal-import-runbook.md](docs/modal-import-runbook.md)。

---

### 3.5 Perch 号池 (`perch`) — Supabase 会话池与注册机

管理 [Perch](https://chat.perchai.app) 的 **Supabase 会话池**（账号无 API Key，凭证为 Access Token + 旋转 Refresh Token，加密保存在 `data/perch-secrets.json`），同时作为 OpenAI 兼容代理将 `/v1/chat/completions` 与 `/v1/responses` 翻译为 Perch 的 `model-call` 信封并把事件流翻译回 OpenAI SSE。

#### (1) 导入与防倒退机制
「Perch 号池 → Perch 账号 → 导入」支持单文件、多选、拖拽、ZIP 及导入前预览，兼容 CLI 导出的原始 JSON 格式：
```json
{
  "schema_version": "1.0",
  "provider": "perchai",
  "auth_provider": "github",
  "account": { "supabase_user_id": "...", "email": "...", "github_user_id": null, "registered_at": "..." },
  "session": { "access_token": "...", "refresh_token": "...", "expires_at": 1800000000 },
  "auth_context": { "supabase_url": "...", "workspace_id": null }
}
```
- **唯一性按 `account.supabase_user_id`**：重复导入只补更新的 Session，绝不建第二个账号。
- **旧文件不覆盖新 Session**：按 `expires_at` 反推签发时间记为 `fetchedAt`，写入时比已存 Session 旧的直接跳过（管理员手动点「更新会话」除外）。

#### (2) 自动续期与状态检测
- **自动续期**：后台每 `refreshTickSec`（默认 30 秒）检查，Access Token 剩余不足 `refreshMarginMin`（默认 10 分钟）时自动续期并**立刻原子落盘**新 Access Token + 旋转后的 Refresh Token。进程内按 Supabase User ID **单飞合并**（8 个并发请求只消耗一次 Refresh Token）。网络 5xx/超时/429 按 `retries` + `retryBackoffMs` 重试；仅当 `401` 或 `400 invalid_grant / "Refresh token is not valid"` 时标为「登录失效」并隔离。
- **状态检测**：每 `checkIntervalSec`（默认 300 秒）用 GoTrue `/auth/v1/user` 校验会话，不消耗模型额度。状态枚举：`正常` / `待验证` / `待选套餐` / `登录失效` / `刷新失败` / `额度耗尽` / `请求限流` / `服务暂时不可用` / `已禁用`。

#### (3) 额度读取 (`usage meter`) 与待选套餐 (`tierSelectionRequired`)
只读已记录用量，不产生模型调用，单位 **PT = 千分之一美元**（向上取整）：

| 字段 | 数据来源 | 说明 |
|---|---|---|
| 套餐名称 | `GET /api/perchai/account` 的 `session.planCode` / `planName` | `pilot` / `demo` 显示为 Starter |
| 月度上限（权威） | `session.entitlements` 中 `usage.monthly_pt` 的 `value_json.limit` | 界面标注 `Perch 明确给出` |
| Starter 上限（回退） | `GET /api/perch-terminal/starter-limits` 的 `config.monthlyUsd` | 仅当账号未给上限且为 Starter 时采用（实测 $20 → 20,000 PT），标注 `按套餐估算` |
| 手动调用上限 | `usage.manual_monthly_pt`，否则 `ultra`→225,000 / `pro`→75,000 | — |
| 充值余额 / Roost | `creditBalancePt` / `usage.roost.unlimited` | 计划外额度 / 无限标记 |
| 已用额度 | `perch_ai_my_inference_usage_meter` RPC | 展示本月已用、手动调用、今日/本周、请求数、Token 统计、恢复时间 |

- **待选套餐 (`tierSelectionRequired: true`)**：新注册但尚未选套餐的账号无 `planCode`、无 `entitlements`，状态显式标红为 **`待选套餐`**，剩余额度显示「待选套餐」而非「未知」，不猜测 20,000 PT，且不参与模型调度，直至完成 Starter 激活。
- **Attribution (`userId` + `workspaceId`)**：挂在额度轮询上通过 `POST /api/perch-terminal/cli-context`（body `{"query":"","includeIndex":false}`）同步 `session.workspaceId`；只要缺一个 ID 就整体发 `null`，与官方 CLI 严格一致。

#### (4) 模型转发通道、Turn Ticket 与 Held-Context 增量
- **调用通道 (`surface`)**：
  - **`cli`（默认）**：`POST app.perchai.app/api/perch-terminal/model-call`，完整支持 `system` / **`tools`** / `images` / `temperature` / `max_tokens` / `response_format` / tool 结果回传（带 ≥200 字符 surface gate 前缀）。
  - **`web`（退路）**：`POST chat.perchai.app/api/perchai/chat`，无需前缀，但 `system` 被折入用户消息且 **`tools` 会被丢弃**。
- **Turn Ticket（准入票）**：每次模型调用前先向 `POST /api/perch-terminal/turn-ticket`（`{surface:"cli", profile:"standard"}`）领票，后续请求头带 `x-perch-turn-ticket` 并复用票面 `runId`，距到期 <30 秒自动 `renew:true`。**遇到 `turn_rate_limited`（规则 `perch-turn-rate-limited` = `ActionIgnore`）立即终止请求，绝不换号重放规避配额**。
- **Held-Context 增量缓存**：缓存「账号 + runId」下每条消息的 SHA-256 快照（`deltaTtlMin` 默认 30 分钟，1–60；`deltaMaxKeys` 默认 8192，1–65536），服务端已持有前缀时仅发增量消息。
- **设备指纹与遥测隔离**：每个账号在 `data/perch-device.json` 中独立保存 AES-256-GCM 加密的随机 UUID `machine-id`、线程 ID（`cli-cwd-<sha16>`）、UA（`perchai-cli/<cliVersion>`）与 `cli-turn` / PostHog 报文构造。**默认 `telemetryEgress=false` 不向外发送任何遥测请求**，仅脱敏写入本地 `logs/perch` 供审计。

#### (5) Perch 设置项 (`perch`)

| 字段 | 默认值 | 说明 |
|---|---|---|
| `supabaseKey` / `appUrl` | 空 / `https://app.perchai.app` | 留空用内置公共 key；读额度与目录的 host |
| `refreshMarginMin` / `refreshTickSec` / `refreshConcurrency` | `10` / `30` / `4` | 到期前多少分钟续期、检查间隔（秒）、并发数 |
| `retries` / `retryBackoffMs` | `3` / `2000` | 临时网络错误重试次数与退避（令牌被拒不重试） |
| `checkIntervalSec` / `checkConcurrency` | `300` / `4` | GoTrue 状态检测间隔与并发 |
| `quotaIntervalSec` / `logRetentionDays` | `600` / `30` | 额度与模型目录同步间隔、Perch 事件日志保留天数 |
| `surface` / `lane` / `gatePrefix` | `cli` / `chat` / 空 | 调用通道、通道 lane（`chat`/`code`）、自定义 gate 前缀（留空用内置 652 字符前缀） |
| `turnTicket` / `ticketProfile` | `true` / `standard` | 是否启用 turn ticket 领票与配额档位 |
| `deltaTtlMin` / `deltaMaxKeys` | `30` / `8192` | Held-context 增量信任窗口（分钟）与最大缓存键数 |
| `cliVersion` / `locale` | `2.4.120` / `en` | 声称的 CLI 版本与 `x-perch-locale` |
| `deviceFingerprint` / `cwdThreadId` | `true` / `true` | 每账号固定 `machine-id` 与 `cli-cwd-` 线程 ID |
| `cliTurnReport` / `telemetryEgress` / `telemetryOptOut` | `true` / `false` / `false` | 是否构造 `cli-turn`、是否真实外发遥测、额外 opt-out 开关 |

#### (6) GitHub 注册机与自动补号
- **Perch 注册机**：「Perch 号池 → Perch 注册机」，按行输入 `账号----密码----2FA密钥----接码页`，串行完成 GitHub OAuth 登录、会话校验、Starter 套餐选择与首条 `hi` 消息激活，成功后入池（本地需安装 `tools/perch-register/requirements.txt`）。
- **自动补号**：「Perch 号池 → 自动补号」，预先导入候选账号与代理库存（加密保存在 `data/perch-signup.enc`，任务配置存 `data/perch-autosignup.json`）。当目标分组「正常」账号数低于阈值时按间隔（≥60s）每轮注册激活一个账号并自动入池；入池后自动擦除候选中的 GitHub 密码、2FA 与接码页；遇到网络/限流/待核查状态自动暂停补号。支持在代理库存、候选账号、已入池账号三处独立修改代理。

---

### 3.6 Token Harbor 号池 (`tokenharbor`)

完整文档见 [docs/tokenharbor.md](docs/tokenharbor.md)。

- **原生三协议直通**：支持 `POST /v1/chat/completions`、`POST /v1/responses`（Codex `wire_api="responses"`）、`POST /v1/messages`（Claude Code）与 `GET /v1/models`。保留原样 JSON 正文、`tools`、`cache_control`、`prompt_cache_key` 及 Anthropic beta 头，不做跨协议转换。
- **套餐证明与无钱包透支门禁**：
  - 后台使用注册机唯一原记录保存的完整 Key 与登录会话，通过**账号自身的固定代理**定期只读 `GET /pricing`（获取 Pass 档位 `Agent ⊂ Office ⊂ Frontier`）和 `GET /api/me/free-tier`（获取 `plan_used_pct`、周期结束时间及 `overage_billing` 钱包续费开关）。
  - 模型必须同时存在于该 Key 的 `/v1/models` 和官网已确认的 Pass 映射。**套餐未知/过期、周用量未知/过期、周额耗尽或官网开启了 `overage_billing` 时立即暂停调度**，防止误扣钱包余额。
  - 概览与账号表对 Agent Pass 统一采用 **$5/周、$20/周期的 2× 等效展示口径**（按 `5 × (1 - plan_used_pct/100)` 计算可调度周额并按原注册所有者去重）。
- **会话亲和与绑定忙不溢出**：默认开启会话粘性且**绑定忙时不溢出**（短暂等待后返回 429，不把同一历史拆散到另一把 Key）；`previous_response_id` 严格绑定同一账号，失效/跨 Key 返回 409。
- **大额消费确认硬停止**：遇到上游要求确认大额消费时直接停止并原样返回提示，绝不自动确认或换 Key 绕过（仅透传调用方显式传入的 `x-th-confirm-spend`）。
- **独立详情日志 (`tokenHarbor.detailLog`)**：默认关闭，与全局详细日志开关解耦；内置注册机支持批量获取/复制已保存 API Key。

---

### 3.7 Mistral 号池 (`mistral`)

完整文档见 [docs/mistral.md](docs/mistral.md) 与 [docs/mistral-capacity.md](docs/mistral-capacity.md)。

- **支持路径与识别**：默认上游 `https://api.mistral.ai/v1`，支持 `/v1/chat/completions`、无状态 `/v1/responses`、`/v1/responses/input_tokens`、`/v1/fim/completions`、`/v1/embeddings` 与 `/v1/models`。`mstrl_` 前缀自动识别，其他形态 Key 显式声明 `provider: "mistral"`。
- **真实会话优先与 `prompt_cache_key` / `X-Affinity` 自动补齐**：
  - **账号粘性优先级**：管理员额外会话头 → 父会话头 → 正文 `metadata.parent_session_id` / `session_id` / `conversation_id` / `thread_id` → Claude `metadata.user_id` / `user` 中的 `_session_UUID` 或 JSON 字符串 → 28 种内置会话头（Claude Code / Codex / OpenCode / pi 等）→ 缓存键 → 首条消息 HMAC。已确认的普通绑定持久化在 `metadata.pool.mistralBindings`，重启自动恢复。
  - **`prompt_cache_key` 补齐**：Chat（兼容 `promptCacheKey` 归一）与 Responses 缺少顶层 `prompt_cache_key` 时，按 `X-Prompt-Cache-Key` → `Prompt-Cache-Key` → 额外会话头 → 父会话头 → 内置会话头的顺序取首个有效值原样填入；无有效头时用 `apb-mistral-<会话身份HMAC>` 兜底。同时自动将非空 `prompt_cache_key` 同步到 `X-Affinity` 请求头。
- **推理模型思考格式与档位兼容（DeepSeek V4.1 Flash 1017、GLM 5.3、GLM 5.3 Flash）**：
  - 自动将 Chat 历史中 assistant 顶层的 `reasoning_content` / `reasoning` 字符串无损转换为 `content` 数组内的原生 `thinking` 块（`{type:"text", text:...}`），避免多轮对话丢失思考或报 400。
  - Chat / Responses 均支持 `low` / `high` / `max` 三档思考强度，**未传默认 `high`，显式 `max` 原样保留**，自动兼容 `reasoning:{"effort":...}` 与 `reasoning_effort`。
  - 针对 DeepSeek Chat 自动清理 `new-api` 泄漏的空 `MimeType:""/null` 并将单字段 `image_url:{"url":"..."}` 转为原生字符串。
- **1910 套餐资格与 3505 后端容量精确规则**：
  - `HTTP 403 + code 1910`（`mistral-tier-not-allowed`）：仅冷却该账号的该模型 24 小时，不硬隔离整号、不扣健康分，允许换号，最终失败回 503。
  - `HTTP 429 + code 3505`（`mistral-backend-out-of-capacity`）：视为上游后端容量不足，不扣健康分、**立即停止换号**（避免全池陪跑），原样保留 `Retry-After` 并返回 `429 mistral_backend_out_of_capacity`。
  - `HTTP 503 + code 3800`：模型服务不可用，不扣健康分、不重复换号。
- **24h 缓存命中率与注册机**：概览与账号表独立统计 24h 缓存命中率（`已测缓存读取 Token / 同批已测提示 Token`），支持一键重置样本；内置 Mistral 混合协议注册机（`data/mistral-signup.enc`）。

---

### 3.8 OpenCode Zen / Go 号池 (`opencode-zen` / `opencode-go`)

完整文档见 [docs/opencode.md](docs/opencode.md)、[docs/opencode-go-gateway.md](docs/opencode-go-gateway.md) 与 [docs/opencode-audit.md](docs/opencode-audit.md)。

- **默认关闭 (`opencode.enabled=false`)**：显式开启后生效，分为 `opencode-zen`（默认 `https://opencode.ai/zen/v1`）与 `opencode-go`（默认 `https://opencode.ai/zen/go/v1`）两个独立号池，支持按协议配置 `zenProtocolBases` / `goProtocolBases`（如 `/inference/go/openai/v1`、`/inference/go/anthropic/v1`）。
- **原生三协议与能力交集**：分别读取公开能力目录（`capabilitiesUrl`）与各账号自身的 `/models` 取交集；原生 Chat、Responses（`previous_response_id` 严格绑定并随 metadata checkpoint 持久化）、Messages 正文不改写。
- **Go 固定出口 IP 强校验 (`ensureGoEgress`)**：OpenCode Go 账号可配置期望的 `EgressIP` 与检测地址 `goEgressCheckUrl`。推理前通过该账号的固定代理无凭证探测出口 IP，**不一致或代理故障时直接拒绝发往上游（Fail-Closed），绝不回退直连或切换出口**；遇到 `FreeTierError` 等政策拒绝立即触发 `opencode-go-policy-refusal` 暂停保护。
- **可选跨协议缓冲桥接与显式跨套餐回退**：默认均关闭；开启 `compatibility` 后支持受限缓冲式跨协议转换（最大 16 MiB）；开启 `zenFallbackGroup` / `goFallbackGroup` 后支持最多一跳的跨套餐回退。

---

### 3.9 Model Studio 阿里云百炼号池 (`modelstudio`)

- **自动识别套餐 (`payg` vs `tokenplan`)**：
  - `sk-sp-…` 前缀自动识别为 **Token Plan / Coding Plan 订阅套餐 (`tokenplan`)**。
  - `sk-…` 前缀自动识别为 **按量付费 (`payg`)**（亦可在设置 `planOverrides` 中按账号覆盖）。
- **严格隔离计费接入点与四大地域**：防止订阅 Key 误打到按量付费域名导致 401 或产生计划外扣费：
  - **Token Plan (`sk-sp-…`)**：北京 `https://token-plan.cn-beijing.maas.aliyuncs.com`、新加坡（默认）`https://token-plan.ap-southeast-1.maas.aliyuncs.com`，自动映射 OpenAI 兼容路径 `/compatible-mode/v1` 与 Anthropic 兼容路径 `/apps/anthropic/v1`。
  - **按量付费 (`sk-…`)**：支持北京（`dashscope.aliyuncs.com`）、新加坡（`dashscope-intl.aliyuncs.com`）、弗吉尼亚（`dashscope-us.aliyuncs.com`）、法兰克福（`dashscope-eu.aliyuncs.com`）四地域的 `/compatible-mode/v1` 与 `/apps/anthropic/v1`。
- **支持协议**：原生服务 `/v1/chat/completions`、`/v1/messages` 与 `/v1/models`；由于上游官方不支持 OpenAI Responses API，`/v1/responses` 在本地直接拒绝，不作伪造转换。

---

## 4. 下游调用接口与开放导入 API

### 4.1 模型推理与目录接口

| 方法与路径 | 说明 |
|---|---|
| `POST /v1/chat/completions` | OpenAI Chat Completions（Perch 池自动双向翻译，其余池原生转发） |
| `POST /v1/responses` | OpenAI Responses（OpenDesign / River / Ollama / Modal / Perch / TokenHarbor / Mistral / OpenCode） |
| `POST /v1/responses/input_tokens` | Mistral Responses 输入 Token 只读计数接口 |
| `POST /v1/messages` | Anthropic Messages（OpenDesign / Ollama / Modal / TokenHarbor / OpenCode / ModelStudio，错误按 Anthropic 格式返回） |
| `POST /v1/fim/completions` | Mistral 代码补全（FIM）接口 |
| `POST /v1/embeddings` | OpenDesign / Ollama / Mistral 向量嵌入接口 |
| `POST /v1/*` | 其他 `/v1/` POST 端点（按各号池 `poolPathOK` 白名单校验） |
| `GET /v1/models` | 当前下游密钥所属分组的可用模型去重并集（按分组缓存 10 分钟，并发请求单飞合并；Modal 池发布统一短名称） |
| `GET /healthz` | 存活与健康检查，返回 `200 {"ok":true}` |

- **鉴权头**：`Authorization: Bearer sk-odb-…` 或 `x-api-key: sk-odb-…`。
- **诊断响应头**：`X-Request-Id`（可在请求/错误/详情日志中关联查询）、`X-Attempts`（本次请求实际尝试的上游账号次数）。

### 4.2 开放导入接口（自动上号与暂存区）

供注册脚本或外部发号器调用，使用「下游密钥 → 导入密钥」新建的 `sk-odbimp-…` 鉴权（支持 `Authorization: Bearer` 或 `X-Import-Key` 头）。导入密钥只能导号，不能调用模型；同一 IP 连续鉴权失败 5 次后 60 秒内返回 `429`。详见 [docs/import-api.md](docs/import-api.md)。

#### (1) 通用号池导入 (`POST /open/v1/accounts/import`)

```sh
# 单条导入（OpenDesign 示例）
curl -X POST https://ods.yeschoy.io/open/v1/accounts/import \
  -H 'Authorization: Bearer sk-odbimp-…' \
  -H 'Content-Type: application/json' \
  -d '{"runtimeKey":"sk-amr-…","controlKey":"vela_ctrl_…","name":"账号A","group":"default","note":"可选","proxyUrl":"socks5h://127.0.0.1:1080"}'

# 显式指定号池导入（Ollama / Mistral / TokenHarbor / OpenCode 等无唯一前缀 Key）
curl -X POST https://ods.yeschoy.io/open/v1/accounts/import \
  -H 'Authorization: Bearer sk-odbimp-…' \
  -H 'Content-Type: application/json' \
  -d '{"runtimeKey":"<API_KEY>","provider":"ollama","name":"ollama-1"}'
```

- 支持批量 `{"accounts":[…]}`（单次最多 200 个，请求体上限 4 MiB，拒绝未知字段）；`runtimeKey` 必填（兼容 `apiKey` / `key` 字段名）。
- **无重复（HTTP 200）**：立即入池启用，备注自动写入 `开放接口导入 <时间>（密钥 <导入密钥名>）`，有控制密钥或目录轮询的立即触发刷新。
- **完全相同（HTTP 200 `unchanged`）**：运行密钥、控制密钥、名称与现有账号完全一致时幂等返回 `unchanged`，方便脚本重跑。
- **存在冲突（HTTP 202 `staged`）**：运行密钥、控制密钥或名称与现有账号冲突时，不写 `config.json`，而是写入 `data/staging.json` 暂存区（上限 500 条）并返回冲突详情。管理员可在后台「账号」页底部暂存区选择：**覆盖现有账号**（更新密钥并清除旧冷却/隔离）、**作为新账号添加**（仅重名时）或**丢弃**。

#### (2) Perch 会话导入 (`POST /open/v1/perch/import`)

```sh
curl -X POST https://ods.yeschoy.io/open/v1/perch/import \
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
| Mistral 后端容量不足（HTTP 429 + code 3505） | `429 mistral_backend_out_of_capacity` + 原始 `Retry-After`（不换号） |
| 余额不足 `insufficient_balance`（402/429）、套餐不含该模型 `tier_model_not_entitled` / Mistral 1910 | `503` |
| 上游网关自身供应商凭据故障 `upstream_provider_*`（上游 500） | `502`（不扣账号健康分、不冷却账号） |
| 其他上游 429 | `429` + `Retry-After` |
| 本地分组内所有账号并发/RPM 满载 | `429 pool_capacity` + 精确 `Retry-After` |
| 本地共享输入缓冲预算满载（`ODB_TH_BUFFER_MB`） | `429 gateway_buffer_limit` + `Retry-After: 1` |
| 分组内账号都在冷却（含该模型冷却） | `429 pool_cooling_down` + 最早恢复时间 |
| 分组内没有可用账号 | `503 pool_unavailable` |
| 服务正在优雅停机排空 | `503 server_draining` |
| 上游把 400/422 参数错误包装成 5xx | 还原为 `400` / `422`（不冷却账号） |
| 其他 4xx（客户端参数错误等） | 原样透传 |
| 上游超时 / 连接或代理失败 | `504` / `502` |
| 客户端主动断开连接 | `499`（立即中止上游请求，不写错误日志、不扣健康分） |

---

## 5. 调度核心与内部机制

### 5.1 选号流程与 8 种调度策略

每个请求固定使用入口处的一份不可变配置快照。选号流程：**硬资格过滤 → 会话绑定命中 → 健康过滤（可选）→ 按优先级（`priority` 1–100，越小越优先）分组 → 组内按策略排序 → 并发/RPM 准入 → 取得租约**。排序、准入、计数、绑定在同一把锁内原子完成，中间无任何 I/O。

不可调度的账号：已禁用、被硬隔离（`hardQuarantined`）、整号冷却中、该模型或该额度类别冷却中、模型不在账号目录中、额度快照证明无任何可用付费来源。

| 策略 (`routing.strategy`) | 排序逻辑（额度策略参考 [magpie](https://github.com/yetone/magpie) `internal/routes`） |
|---|---|
| `smart`（默认） | 余量充足的账号里，**最长窗口最早重置的先用**（按小时比较，避免剩余额度在重置时作废）；已用 ≥ `lowShare`（90%）排后，≥ `spentShare`（98%）再排后；再往后是靠套餐模型额度付费的（计费周期先结束的先用），只剩钱包可用的排最后；无额度数据的排在已知账号之后 |
| `pace` | 周窗口剩余比例 ÷ 距重置小时数，越高越先；相差 10% 以内视为同档，按近期 Token 消耗打散 |
| `least-used` | 已用比例最低优先，其次近期 Token 消耗最少 |
| `weighted` | 按账号 `weight`（1–100）平滑加权轮询（NGINX Smooth WRR） |
| `roundrobin` | 最久未被选中的账号优先 |
| `least-selections` | 累计选中次数最少优先，平局按当前在途数 |
| `least-connections` | 当前在途请求数最少优先 |
| `ordered` | 严格按配置顺序，前一个不可用或满载才用下一个 |

### 5.2 会话粘性与两阶段提交绑定

`new-api` 打开“透传请求头”后，本服务按以下顺序提取会话标识（Mistral 与 Token Harbor 有针对性微调，见各自章节）：

1. Responses 的 `previous_response_id`（上游按账号保存 response，必须回到同一账号；本服务在内存与 `metadata.json` 中记录每个 response ID 由哪个账号产生）。
2. `routing.sessionHeaders` 中管理员配置的自定义会话头。
3. 父会话头：`X-Opencode-Parent-Session-Id`、`X-Parent-Session-Id`、`X-Parent-Session-Affinity`、`X-Codex-Parent-Thread-Id`、`X-Codex-Parent-Session-Id`、`X-Claude-Code-Parent-Agent-Id`、`X-Claude-Code-Parent-Session-Id`（保证父子 Agent 落到同一账号）。
4. 请求体字段：`prompt_cache_key`、`session_id`、`conversation_id`、`thread_id`（含 `metadata.` 下同名字段）、Claude Code `metadata.user_id` / `user` 中的 `_session_UUID` 或 JSON 字符串。
5. 内置会话头：`X-Claude-Code-Session-Id`、`Session-Id`/`Session_id`、`X-Session-Id`、`X-Session-Affinity`、`Thread-Id`/`Thread_id`、`Conversation-Id`、`X-Codex-Session-Id`、`X-Pi-Session-Id`、`X-Opencode-Session-Id` 等。
6. 兜底弱粘性（`fallbackHash=true`）：首条 `system`/`developer` 与首条 `user` 消息文本（各截 4096 字节）的 HMAC。

- **安全与过期**：会话值经 `HMAC-SHA256(routingSecret, …)` 生成指纹并按分组/下游 Key 隔离，原值与指纹均不写普通日志。内存绑定表采用 LRU（全局上限 4096 条），显式会话默认 2 小时（`explicitTtlSec=7200`）滑动过期，弱粘性 15 分钟（`fallbackTtlSec=900`）。
- **两阶段提交与忙时策略 (`onBindingBusy`)**：未命中时先写“待确认（pending）”绑定，请求真正写到上游后才确认；未发出即失败时仅由创建它的请求回滚删除。当绑定账号满载时：`overflow`（默认，TokenHarbor/OpenCode 除外）临时借用其他账号但不改原绑定；`wait-overflow` 先等待再借；`reject` 直接返回 429。绑定账号被禁用、冷却、隔离、额度耗尽、删除或更换密钥/代理时，绑定自动失效并改派。

### 5.3 并发控制与 60 秒滚动 RPM 窗口

- 每个账号可设 `maxConcurrent` 和 `maxRpm`（0 为不限）。当网关返回 `tier_limit_exceeded`（如 `membership concurrency limit exceeded 6/5`、`rpm 61/60`）时，会自动记下套餐上限（`learnedConcurrency` / `learnedRpm`），与手设值取较小者生效，点「恢复」清除。
- **准入顺序固定**：先检查并发，再检查 RPM；并发满时不消耗 RPM。
- **精确 60 秒滚动窗口**：准入时先预留（reservation），请求真正写到上游后计入时间戳窗口，此后无论上游成败都不退还；请求未发出去就失败时立即退还预留。租约释放幂等。
- 全组账号满载时最多等待 `concurrencyWaitMs`（默认 2000ms），超时返回本地 `429 pool_capacity` 和精确 `Retry-After`，记为 `errorCategory: capacity`，不计入健康样本。

### 5.4 换号规则、错误规则与重试规则

- **换号边界**：每个请求最多尝试 `maxAccountsPerRequest`（默认 2，范围 1–5）个账号，不重复选同一账号。**只在首包之前**、且满足以下任一条件时换号：① 错误规则将该账号/模型冷却或隔离；② 上游返回 401；③ 请求根本未发出（连接/代理失败）。普通 5xx 或超时不换号；**首包发出后绝不重放**。
- **`errorRules`（有序，首条命中生效）**：匹配 HTTP 状态码、正文关键字（大小写不敏感子串）和响应头；动作为 `ignore` / `degrade` / `cooldown` / `hard-quarantine`，作用域为整号（`account`）、单模型（`model`）或套餐类别（`class`）。冷却时长优先级：响应头（如 `Retry-After`）→ 正文 `bodyAfter` 短语后的 RFC3339 时刻（如 `Try again after 2026-10-23T05:00:00Z`）→ `fallback`，不超过 `max`。未命中任何规则时仅记一次降级健康样本，不会自动冷却。
- **`retryRules`（坏请求快速止损）**：默认命中 400/404/413/422 时立即停止换号并原样返回，避免单个用户的坏参数打穿全池账号。但若同一响应已被 `errorRules` 判定为账号级限额/资格问题（如 OpenDesign 的 400 `model_limit_exceeded` / `tier_limit_exceeded`），则照常换号。

### 5.5 流式首包防抖与心跳

流式请求会等待读取**第一个完整的 SSE 事件**并确认不是错误事件后，才开始向下游写入响应头与数据，因此首包前的流内错误仍可无感换号。流式首包超时 `firstEventTimeoutSec` 默认 120 秒，空闲超时 `idleTimeoutSec` 默认 360 秒；开始转发后每 `heartbeatSec`（默认 25 秒）在 SSE 事件边界写一行 `: PING` 心跳。

### 5.6 模型价格表与谷峰/长上下文计费

「路由设置 → 模型价格」按每百万 Token 配置各模型的输入、缓存命中、输出价格（原币种 `USD` / `CNY`，不换算）：
- **DeepSeek 谷峰计费**：基础价格填空闲价，「高峰倍数」填 `2`；北京时间工作日 `09:00–12:00`、`14:00–18:00` 按倍数计费，法定节假日可在「高峰例外日」中排除。
- **调价生效日期**：同一模型可配置多行，按「生效日期」自动匹配已到期的最新价格行。
- **长上下文阶梯价**：提示 Token 达到阈值时整笔请求按长上下文单价计算。请求日志据此实时计算每笔费用与缓存率，概览页汇总 24h 费用（人民币、美元分开统计）。

### 5.7 日志分段、内存环与脱敏规范

- **请求日志 (`data/logs/requests-*.jsonl`)**：每笔请求一行终态记录（时间、请求 ID、下游密钥、分组、号池、入站/出站协议、模型、选中账号、选号原因、会话来源、绑定结果、状态码、耗时、TTFT、TPS、各类 Token、费用、每次尝试轨迹 `attempts` 与决策链 `workflow`）。
- **错误日志 (`data/logs/errors-*.jsonl`)**：每次失败的上游尝试一行，通过 `requestId + attemptIndex` 与请求日志精确关联；错误正文先经密钥/凭据脱敏再截断至 16 KiB（非 JSON 正文仅记字节数）。
- **详细日志 (`data/logs/details-*.jsonl`)**：**默认关闭**（TokenHarbor 另有独立开关 `tokenHarbor.detailLog`）。开启后记录脱敏后的请求/响应头与正文（流式记原始 SSE，单块默认截断 64 KB，内存环上限 256 MiB）。排查完毕后请及时关闭并清空。
- **异步无阻塞落盘**：单个分段文件 5 MiB，默认保留 30 天、总磁盘上限 100 MiB。查询直接读内存环，写入走单队列异步刷盘（每秒 `flush`），队列满（默认 64 MiB）时丢弃日志并计数，**模型流量永远不等待磁盘 I/O**。

---

## 6. 数据目录文件与热加载 (`DATA_DIR`)

所有文件权限强制为 `0600`、目录权限为 `0700`，写盘一律采用「写同目录临时文件 → `fsync` → 原子 `rename`」。JSON 损坏时启动直接报错退出并保留现场文件。**整个 `data/` 目录均为核心机密，严禁提交到 Git 或对外共享。**

| 文件 / 路径 | 用途与刷盘策略 |
|---|---|
| `config.json` | 账号（含运行/控制密钥）、下游密钥与导入密钥的 SHA-256 哈希、各号池路由/额度/日志设置、各池错误与重试规则、模型价格表、管理员密码哈希（PBKDF2-SHA256）。支持手工编辑后执行 `kill -HUP <pid>` 或调用 `POST /api/config/reload` 热加载，在途请求继续使用旧快照 |
| `metadata.json` | 账号运行状态（冷却/硬隔离/学习到的并发与 RPM 上限）、健康样本、各号池额度与目录快照、Responses/Mistral 持久化绑定、24h 分钟桶统计、32 字节 `routingSecret`。冷却/隔离/额度/配置变化 15 秒内写盘；仅统计和健康样本变化时最多每 60 秒写一次 |
| `selection-counters.json` | 各账号累计选中次数、最后选中序号与近期每分钟请求计数，最多每 5 秒写一次 |
| `sessions.json` | Web 管理台登录会话持久化文件 |
| `staging.json` | 开放导入接口检测到冲突的暂存账号（含密钥，上限 500 条），等待管理员覆盖、添加或丢弃 |
| `import-log.json` | 开放导入接口审计记录（调用方 IP、时间、导入密钥 ID、各状态计数，每池独立保留最近 1000 条） |
| `perch-secrets.json` | Perch 号池加密会话（Access / Refresh Token、Supabase URL 与 Key），AES-256-GCM 封装（密钥由 `routingSecret` 派生）；`config.json` 中仅存占位符 `perch-session:<uid>` |
| `perch-device.json` | Perch 每账号独立加密的设备标识（随机 UUID `machine-id`、首次运行与发消息时间），键为账号 HMAC 摘要；**请务必与 `metadata.json` 一起备份**，丢失会导致所有 Perch 账号变更设备指纹 |
| `perch-signup.enc` / `perch-autosignup.json` | Perch 自动补号的加密候选账号/代理库存与自动补号任务配置 |
| `mistral-signup.enc` | Mistral 注册机的加密候选凭据、代理库存与虚拟卡状态 |
| `tokenharbor-signup.enc` | Token Harbor 注册机的加密候选凭据、GitHub/邮箱会话与 API Key 记录 |
| `logs/` | `requests-*.jsonl`、`errors-*.jsonl`、`details-*.jsonl`、`perch-*.jsonl`、`tokenharbor-*.jsonl` 分段日志目录 |

---

## 7. 核心配置项字典 (`routing` / `quota` / `logs`)

### `routing`（全局及各池独立路由配置）

| 字段 | 默认值 | 说明 |
|---|---|---|
| `strategy` | `"smart"` | 选号策略：`smart` / `pace` / `least-used` / `weighted` / `roundrobin` / `least-selections` / `least-connections` / `ordered` |
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
| `directMaxConns` / `proxyMaxConns` | `128` / `32` | 直连与单个代理 Transport 的单 Host 最大并发/空闲连接数 |

### `quota` 与 `logs`

- **`quota`**：`enabled`（`true`）、`intervalSec`（`60`，轮询间隔秒数）、`concurrency`（`2`，轮询并发数）、`staleSec`（`900`，快照过期秒数）。
- **`logs`**：`retentionDays`（`30`）、`maxRequests`（`50000`）、`maxErrors`（`10000`）、`maxDetails`（`500`）、`maxDetailKB`（`64`）、`maxTotalMB`（`100`）、`detail`（`false`）。
- **账号级字段**：`maxConcurrent`、`maxRpm`、`weight`（`1–100`）、`priority`（`1–100`，默认 `50`）、`proxyUrl`（支持 `http://`、`https://`、`socks5://`、`socks5h://`，**配置代理后若代理连接失败绝不回退直连**）、`egressIp`（OpenCode Go 固定出口 IP）。

---

## 8. 架构限制、回滚与开发验证

### 架构限制
- **单进程有状态设计**：并发租约计数、60 秒滚动 RPM 窗口、内存会话绑定表均在单进程内存中维护，不支持多副本挂载同一 `DATA_DIR` 并发读写。
- **OpenDesign 自动充值未知**：控制面接口不返回账号是否开启了自动充值，因此窗口和各项余额均为 0 的账号会被判为耗尽而不调度。
- **Perch / Mistral 注册机运行环境**：注册机 Worker 依赖 Python 3.10+、`curl-cffi`、Playwright 与 Google Chrome Stable（已在 `Dockerfile` 中完整内置；宿主机直接运行需按需配置对应 Python 虚拟环境）。

### 备份与回滚
- 升级前请完整备份 `data/config.json`、`data/metadata.json` 以及所有 `data/*.enc` / `data/perch-*.json` 文件（注意：`perch-secrets.json`、`perch-device.json`、`*-signup.enc` 均依赖 `metadata.json` 中的 `routingSecret` 解密，**必须成套备份与恢复**）。
- 回滚时停止进程、切回旧版本二进制或旧容器镜像，挂载原数据目录启动即可。若 `selection-counters.json` 损坏可直接删除重建；`config.json` 与 `metadata.json` 不可随意删除。

### 开发与测试

```sh
# 静态检查与全量 Race 竞态测试（全部使用本地临时目录与 mock 上游，不触碰任何真实凭据）
go vet ./... && go test -race ./...

# 编译二进制
go build -o odb ./cmd/odb
```
