# essevin 注册协议与实施记录

状态：**研究/暂存**。代码可用，未接入 account-pool-balancer，未提交业务改动。

## 1. 站点结构

| 项 | 值 |
|---|---|
| 官网 | https://essevin.com（Cloudflare） |
| 控制台 | https://console.essevin.com（Vite SPA） |
| 注册入口 | https://console.essevin.com/login?tab=register |
| 推理 API | https://api.essevin.com/v1（OpenAI 兼容，Bearer `sk-...`） |
| 面板 | 自建 airgate 系（`/api/v1/...`，响应包 `{code:0,data,message}`） |

注册页无 Turnstile / reCAPTCHA / 第三方 OAuth 强制项，走邮箱验证码。

## 2. 注册协议（校验码来自真实浏览器抓包）

```
POST /api/v1/auth/send-verify-code   {"email": "...", "lang": "en"}
POST /api/v1/auth/verify-code        {"email": "...", "code": "133274"}
POST /api/v1/auth/register           {"email", "password", "verify_code"}
   -> 200 {"code":0,"data":{"token":"<JWT>","user":{"id":7995,...}}}
GET  /api/v1/users/me                Bearer <JWT>
GET  /api/v1/groups?page=1&page_size=100   Bearer <JWT>   # 有订阅后才有内容
GET  /api/v1/plans                   Bearer <JWT>          # 含 included_groups，新号也能取
POST /api/v1/api-keys                Bearer <JWT> {"name","group_id","expires_at":""}
```

要点：

- `send-verify-code` 有 **60 秒/邮箱**冷却，错误码 `VERIFY_CODE_INVALID` / `VERIFY_CODE_REQUIRED`。
- `register` 请求体只需三个字段；`username`、`source_site`、`invite_code` 均可选。
- **建 Key 必须带 `group_id` 且 `expires_at` 为 `""`**，缺任一字段返回 `400 Invalid request`。
- Key 绑定到模型分组：`Essevin · gpt_text`(31)、`Essevin · claude`(30)、`gpt_image`(32) 等。
  `Essevin Plan · *` 是订阅组，**不是**可用的 Key 分组，必须排除（否则 `403 You can't use this group`）。

## 3. 试用与额度（实测结论）

- 新号会获得 `Essevin Plan · free_trial`：**30,000 点 / 7 天**，不含 Images / Video。
- 是否发放与注册 IP 相关：同一出口 IP 复用注册（7998 与 7996 同 IP）时**不再发放**。
- 首次发放是**即时**的（7996 注册 05:48:32 → 试用 05:49:09）；未发放的账号等 12 小时也不会补发。
- Taster（`plan_group_id: 36`，250,000 点）是 **$4/月 付费套餐**，`plan-change/preview` 返回
  `{"kind":"new_purchase","charge_amount":"4.00"}`；试用组(43)对免费号直接 `400 plan_unavailable`。

## 4. 指纹与出口约束

浏览器抓包实测（见 `capture_addon.py` 采集口径）：

- `User-Agent`: `Chrome/157.0.0.0`（Chrome UA 归约到 major.0.0.0）
- `sec-ch-ua`: `"Not-A.Brand";v="99", "Google Chrome";v="157", "Chromium";v="157"`
- `sec-ch-ua-mobile: ?0`、`sec-ch-ua-platform: "macOS"`
- `accept-language: en`（注册页实际只发 `en`，不是 `en-US,en;q=0.9`）
- TLS: TLSv1.3 / `TLS_AES_256_GCM_SHA384` / ALPN `h2`
- 查询串中的 `tz` 会被服务端记录（如 `?tz=Asia%2FShanghai`）

注册机 `registrar.py` 按上述口径构造 UA + client hints，并要求一个账号绑定一个出口 IP。
TLS/HTTP2 指纹目前是 Python `ssl` 的默认形状，**不是** Chromium 的，属于已知差异。

## 5. 代理链

zooproxy 在 `us-eu.zooproxy.com:5000` 同时开 SOCKS5(用户名/密码) 与 HTTP 代理，
但**对源 IP 有白名单**：本机直连返回 `403 forbidden ip=<本机出口> not supported`。

正确接法是链式：

```
registrar.py --upstream-hop http://127.0.0.1:10808   # sing-box
   CONNECT us-eu.zooproxy.com:5000  (经 hop)
   SOCKS5 用户名/密码认证 -> CONNECT <目标>:443
```

20 条给定代理中 5 条在认证后无响应（已剔除），其余 15 条可用。

## 6. 文件

| 文件 | 用途 |
|---|---|
| `registrar.py` | 注册客户端：HTTP/1.1+TLS、链式代理、iCloud 取码、分组解析、建 Key |
| `run_batch.py` | 批量入口：账号/代理文件、并发、结果 JSONL、`--check` 只测出口 |
| `capture_addon.py` | mitmproxy 抓包 addon（**注意**：注入功能当前会破坏页面，默认关闭） |
| `page_collector.js` | 页面侧指纹采集脚本 |
| `start_capture.sh` | 起 mitmproxy(:8899) + 独立 profile 的 Chrome |

运行时输入（含凭据，**不入库**）放在 `artifacts/essevin/`：

- `accounts.txt`：`email----share_url`
- `proxies.txt`：`socks5://user:pass@host:port`
- `run/raw.jsonl`：原始抓包（含会话 token）

## 7. 未完成

- 接入 account-pool-balancer（`internal/` 下尚无 essevin 池）
- 指纹/IP 泄漏报告（数据已具备，报告未写）
- TLS/HTTP2 指纹与真实 Chromium 的一致性
- 试用发放条件的完整确认（仅确认与出口 IP 相关）
