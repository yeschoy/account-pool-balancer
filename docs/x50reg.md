# 50x AI 注册机（ai.50x.energy）

从 iCloud HME 共享邮箱自动完成 50x AI 账号注册、邮箱确认、登录、创建 API Key，并读回欢迎额度。
注册后每个账号自带 **$100 welcome credit**（`starter` 套餐），Key 可直接用于 `/v1/chat/completions`。

## 一、抓包实录（2026-11-15，真实浏览器会话）

浏览器手动注册一次，CDP 全程抓包（`/tmp/50x-cap/capture2.jsonl`），完整链路如下：

| 步骤 | 请求 | 说明 |
|---|---|---|
| 1 | `GET  https://api.ai.50x.energy/api/auth/signup` | 注册开关：`{"open":true,"social":["google","github"],"consent_version":"2026-11-02","consents":[...]}` |
| 2 | `POST https://api.ai.50x.energy/api/auth/register` | body 仅 `{"email":...,"password":...}` → `201 {"status":"verification_sent"}` |
| 3 | 邮件 | 主题 `Confirm your 50x AI account`，正文含 `https://ai.50x.energy/?verify=<token>` |
| 4 | `POST https://api.ai.50x.energy/api/auth/verify` | body `{"token":"<mail token>"}` → `200 {"token":"<session>"}` |
| 5 | `POST https://api.ai.50x.energy/api/auth/login` | body `{"email":...,"password":...}` → `200 {"token":"<session>"}` |
| 6 | `GET  https://api.ai.50x.energy/api/keys` | `Authorization: Token <session>` → Key 列表（只有 `prefix`，无明文） |
| 7 | `POST https://api.ai.50x.energy/api/keys` | body `{"name":"..."}` → 返回一次性明文 `api_key` |
| 8 | `GET  https://api.ai.50x.energy/api/billing` | `balance_usd=100.000000000`，`tier=paid`，`welcome` 流水 |

关键结论：

- **注册接口没有验证码、没有 consent 字段**。`consent_version` / `consents` 只是前端展示，`POST /api/auth/register` 实测只吃 `email` + `password`，多传会被忽略。
- 邮件 token 是 Django 风格签名串（`payload:timestamp:hmac`），不是验证码数字，无需 OCR。
- 会话认证头是 **`Authorization: Token <x>`**（Django REST 风格），不是 `Bearer`；`Bearer` 只用于 `sk-50x-...` API Key 调 `/v1/*`。
- verify 成功即返回可用会话，login 只是兜底。
- 共享邮箱读信：`GET {MAIL_BASE}/api/messages?alias_email=<addr>&token=<share token>&range=all`，返回 `data.emails[].textContent/htmlContent`。

## 二、用法

```sh
go build -o x50reg ./cmd/x50reg

# 单账号
./x50reg -mail "addr@icloud.com----https://icloud.mida.vip/messages/addr%40icloud.com?token=XXXX"

# 批量（mailboxes.txt 每行 email----接码地址）
./x50reg -mailfile mailboxes.txt -proxies proxies.txt -c 4 -out accounts.jsonl

# 只看解析结果，不发请求
./x50reg -mailfile mailboxes.txt -list
./x50reg -mailfile mailboxes.txt -dry-run

# stdin
./x50reg -mail - < mailboxes.txt
```

参数：

| flag | 默认 | 含义 |
|---|---|---|
| `-mailfile` | – | `email----接码地址` 每行一条 |
| `-mail` | – | 单条 / `-` 读 stdin |
| `-proxies` | – | 代理列表文件，每行一个 URL |
| `-proxy` | – | 单个代理 |
| `-c` | 1 | 并发数 |
| `-out` | `accounts.jsonl` | 结果追加写（0600） |
| `-key-name` | `registrar` | 新建 Key 的名字 |
| `-mail-wait` | 240s | 等确认邮件上限 |
| `-mail-poll` | 3s | 收件箱轮询间隔 |
| `-timeout` | 30s | 单请求超时 |
| `-api` / `-origin` / `-mailbase` / `-ua` | 见代码 | 便于指向测试环境 |

## 三、输出

`accounts.jsonl` 每行一个 JSON（`ok=false` 时带 `error`）：

```json
{"email":"...","ok":true,"proxy":"http://***@us-eu.zooproxy.com:5000",
 "account":{"email":"...","password":"...","session_token":"...","api_key":"sk-50x-...",
            "key_id":5329,"key_prefix":"sk-50x-h-0q","plan":"starter","tier":"paid",
            "balance_usd":"100.000000000","created_at":"..."},
 "elapsed":"16.6s","at":"..."}
```

末尾同时打印 `email \t password \t api_key` 三列，方便直接喂号池。

## 四、实测结果

真实 iCloud 邮箱 6 个，`-c 2`，直连，**6/6 成功**，耗时 49s，平均 13–20s/账号：

```
beards-armors.19@icloud.com    sk-50x-WSsn...   100.000000000$
wastes-clefts.0q@icloud.com    sk-50x-67tB...   100.000000000$
revues.95-trip@icloud.com      sk-50x-6D_F...   100.000000000$
ripped_minters19@icloud.com    sk-50x-dUPb...   100.000000000$
oriels.fuller-2z@icloud.com    sk-50x-1OQ_...   100.000000000$
shellac-visitor.3@icloud.com   sk-50x-531n...   100.000000000$
```

6 个 Key 逐个打 `/v1/chat/completions`（`deepseek-ai/DeepSeek-V4.1-Flash-1017`），**全部返回正常补全**，验证 Key 不是占位。

## 五、代理池

`proxies.txt` 每行一个。`zooproxy` 形态 `user-region-Rand-sid-XXXX-t-10:pass@host:5000`：

- 每个账号通过 `WithStickySID` 重写 `sid-` 段，得到独立粘性会话（保留 `region`、`-t-` 超时），避免同一注册的建号与收信走不同出口。
- 代理被网关拒绝（403/407/`not supported`）或连接失败时标记隔离，自动轮换；整池都坏会重置重试，不会卡死。
- **本机实测这 10 个代理全部返回 `403 Forbidden: msg: forbidden ip=222.125.148.62 not supported`** —— 网关对本机出口 IP 做了白名单，需要在 zooproxy 面板把本机 IP 加入允许列表，或换到白名单内机器/固定跳板后再用。这是网关侧问题，不是注册链路问题（同一批邮箱直连是 6/6 成功的）。

## 六、边界

- 不发求解验证码、不伪造浏览器指纹绕过风控；当前上游未对注册接口设挑战，所以纯 HTTP 足够。
- 邮箱确认必须用**真实收件**，邮箱与接码地址必须成对，脚本会等邮件到齐再 verify。
- 每个账号独立 Key，不覆盖已有 Key；`POST /api/keys` 的明文只返回一次，落盘即最佳时机。
- 上游若关闭 `open`，`CheckSignup` 会在任何写操作前直接失败退出。
- `_runs/` 下含真实账号凭据，权限 0600，不要提交到 Git。

---

# 控制台集成：50X AI 号池标签

50X AI 已接入账号池管理台，作为顶部号池标签 + 两条侧栏页面。目前只有注册机，没有推理号池。

## 页面

| 位置 | 内容 |
|---|---|
| 顶部标签 | `50X AI`（`data-pool="x50"`，第 12 个池） |
| 侧栏 | `50X 注册机`（`#page-x50reg`）、`50X 注册设置`（`#page-x50settings`） |
| 资源 | `/assets/x50.js`，与 `deploy.js` / `tokenharbor.js` 同构 |

切换到此池会直接落在注册机页，不会停在通用概览——50X 没有推理号池，概览对它没有意义。

## 注册机页

- **导入**：`邮箱----接码地址` 每行一条。没有 `token=` 的行会被跳过并计入「跳过」。
- **代理池**：每行一个，顺序即轮询顺序。一个账号绑定一个代理，代理可复用。
- **绑定规则**：导入时按池内顺序继续轮询（`x50ProxyCursor` 取已绑定行里的最大池下标 + 1）。第二次导入不会从第一个代理重来。绑定写入账号行后重试沿用同一出口，不会静默换 IP。
- **批量注册**：只挑 `待注册/失败/已取消` 的行；`已完成` 行永不重跑，避免同一账号生成第二个 Key。
- **行操作**：单行注册 / 重试、复制 Key（显式读取）、删除（运行中会先取消）。
- **导出已完成**：`/api/x50/export`，`email \t password \t apiKey` 每行一个，可直接粘进下游导入框。

## 设置页

并发 1–10、等邮件上限、收件箱轮询间隔、单请求超时、Key 名称、密码策略（随机 / 同邮箱）、导入时自动绑定代理、代理池，以及 API 根与接码服务根两个可选覆盖（联调镜像用，留空用官方端点）。

保存时空的代理文本框表示「保持已保存的代理」而不是清空——避免一次误存抹掉整池凭据。

## 凭据边界

- 列表响应不含明文 Key，只给 `sk-50x-` 前缀；完整值只能通过显式的 `/api/x50/candidates/{id}/key` 读取（`Cache-Control: no-store`）。
- 代理在 UI 与 API 里一律投影成去掉凭据的 `scheme://host:port`；设置页的代理框不回显。
- 数据落在 `data/x50-signup.json`（0600，明文，与 Deploy 注册机一致；密码、会话、Key 都需该文件权限保护）。

## 后台任务生命周期

注册任务挂在**服务级 context** 上（`Server.x50BaseCtx()`），不继承发起它的 HTTP 请求的 context。这一点有回归测试锁住：早期实现用了 `r.Context()`，handler 一返回任务就被取消，所有注册瞬间死在「检查注册开关」阶段。关闭时 `stopX50()` 取消根 context、取消每行 context 并 `Wait()` 等任务退出，之后才关日志库。

## 接口

```
GET    /api/x50/candidates                      列表 + 设置 + 状态 + 上限
POST   /api/x50/candidates/bulk                 批量导入（text / proxies / dryRun）
DELETE /api/x50/candidates/{id}
POST   /api/x50/candidates/{id}/register
POST   /api/x50/candidates/{id}/retry
POST   /api/x50/candidates/bulk-register        批量跑所有可重试行
POST   /api/x50/candidates/clear-finished
GET    /api/x50/candidates/{id}/key             显式读取明文 Key
GET    /api/x50/status
GET    /api/x50/logs
GET    /api/x50/settings
PUT    /api/x50/settings
GET    /api/x50/export
```

上限：单次导入 500 行、队列 3000 行、日志环 400 条、并发 1–10。

## 验收记录（本地）

| 项 | 结果 |
|---|---|
| Go 单测 | `go test ./...` 全过；`-race` 覆盖 `server`/`x50reg`/`x50signupstore` |
| `go vet ./...` | 干净 |
| 前端单测 | 13 个 `*_web_test.js` 全过（含新增 `x50_web_test.js`，以及扩到 12 池的 `pool_navigation_web_test.js`） |
| 真实浏览器 | Chrome 走完：切换标签 → 落注册机 → 保存设置 → 导入 → 批量注册 → 完成 → 复制 Key，控制台无报错 |
| 桩上游 e2e | `TestX50RowLifecycleAgainstFakeUpstream`：3 行全走到 `已完成`，只创建 1 次 Key，重跑不再建 Key |
| 真实上游 | **未在此环境复测**：本次验证期间 `api.ai.50x.energy:443` TCP 不通（`ai.50x.energy` 站点正常），与代码无关。此前 CLI 版本对真实 iCloud 邮箱实测 6/6 成功、6 把 Key 均可用 |
