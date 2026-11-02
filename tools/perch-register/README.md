# Perch GitHub OAuth worker

独立单次进程，来源于 `logicc-scripe` 的 `perch_console.sign_in`、`open_design_github`、`logicc_protocol`、`river_chain_proxy` 和 `perch_export`。仅复制本流程所需代码；校验时保留刷新返回的完整会话并核对最初身份。不会调用旧项目或写账号、会话文件，也不会执行套餐激活。


## 安装和运行

需要 Python 3.10+ 和 curl-cffi（版本见 requirements.txt）。在本目录运行：

```sh
python3 -m venv .venv
.venv/bin/pip install -r requirements.txt
.venv/bin/python -B worker.py < INPUT_JSON
```

标准输入为一个 JSON 对象，必填非空字符串 `login`、`password`、`totp_secret`（Base32）和 `mailbox_url`（完整接码页面 URL），可选 `proxy` 为 `http://`、`https://`、`socks5://` 或 `socks5h://` 代理 URL，必须含端口；不提供即直连。账号只通过 stdin 传递，避免出现在命令参数与 shell 历史中。不要使用真实账号执行离线测试。

登录、接码、身份校验均使用同一个显式上游代理，且不使用环境中的系统代理；无 proxy 时均直连。登录和校验为支持认证 SOCKS 会通过本机临时 HTTP CONNECT 代理连接上游，接码请求直接连接上游代理。代理线路的实际出口仍由上游控制。会话校验前若近过期将刷新，刷新可能使旧 refresh token 失效。

成功时 stdout 仅输出一个 JSON 对象及换行，字段为 `schema_version`、`provider`、`auth_provider`、`account`、`session`、`auth_context`；这些字段兼容供号池导入。额外的 `supabase_session` 包含完整 Supabase 会话，且其 `access_token`、`refresh_token`、`expires_at` 与 `session` 一致；如果刷新返回其他字段，也会合并。输出包含凭证，调用方应使用受保护的管道，不要记录或公开。标准输出的 JSON 内容本身不应视为日志。

失败时 stdout 为空，stderr 严格为以下固定行之一（含行末换行），不包含账号、密码、token、代理、异常详情或上游原文。调用方可按完整行进行枚举映射；未知输出应按未知错误处理，不应直接公开：

| stderr | 含义 |
| --- | --- |
| `worker: input_validation` | 输入 JSON、必填字段、账号格式或代理格式无效 |
| `worker: dependency_unavailable` | 加载 worker 所需模块失败 |
| `worker: github_oauth` | GitHub OAuth 登录和授权流程失败 |
| `worker: session_validation` | 提取、刷新、校验或整理会话失败 |
| `worker: internal_error` | 未归类的内部错误 |

非法 JSON 或文本编码返回 2；其他失败返回 1，成功返回 0。分类代表失败阶段，不代表具体根因；网络、上游和清理失败归入发生时所在阶段。

Supabase 项目默认值来自原项目，可通过 `PERCH_SUPABASE_URL` 和 `PERCH_SUPABASE_KEY` 环境变量覆盖。流程依赖 GitHub、Perch、Supabase 和 MailCat 接码页的现有结构，上游变更、多因素验证挑战、风控及网络错误均可能导致失败。无浏览器交互或自动重试整个注册任务。刷新请求的网络重试沿用原实现，响应丢失时令牌可能已轮换。

## 离线测试

```sh
python3 -B -m unittest discover -s . -p 'test_*.py' -v
```

测试使用模拟登录和 HTTP 响应，不访问真实服务、不注册账号。
