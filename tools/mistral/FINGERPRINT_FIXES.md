# 指纹 / IP 暴露面修复记录（2026-11-09）

> 对应分析：本文只记录**已改的代码**和**实测前后对比**。
> 完整识别面清单见 `FINGERPRINT_ANALYSIS.md`。
> 未修的项在文末「剩余风险」里列明，不要当成已解决。

---

## 一、P0 已修（4 项）

### 1. 指纹注入脚本原本 100% 不执行

`core/fingerprint.py::console_js()` 渲染出来有 4 处 JS 语法错，`add_init_script`
抛 `SyntaxError`，整段脚本一行都没生效：

| 位置 | 错误 |
|---|---|
| `version: "{ch_ua_full_version.split('.')[0]}"` | `ch_ua_full_version` 已含引号 → `version: ""156"` |
| 同上（Google Chrome 行） | 同上 |
| `version: "{ch_ua_full_version}"` | → `""156.0.8078.80""` |
| `"availHeight", { get: () => N }, configurable: true });` | 多一个 `}` |

**修法**
- `ch_ua_full_version` 保留带引号（进 HTTP 头），新增裸值字段 `full_version`（进 JS）
- 模板从 f-string 换成 `%`-格式化，彻底避开 `{{ }}` 转义把 JSON 引号拼坏
- `add_init_script` 之前加 `node --check` 回归测试（`test_fingerprint.py`）

**实测对比**（同一台机器、同一画像）

| | 修前 | 修后 |
|---|---|---|
| `page.on("pageerror")` | `Unexpected number` | 无 |
| `navigator.userAgentData.brands` | `Not:A-Brand 99` / **`HeadlessChrome 145`** / `Chromium 145` | `Not:A-Brand 8` / `Chromium 156` / `Google Chrome 156` |
| HTTP `sec-ch-ua` | `Google Chrome 156`（与 JS 矛盾） | `Google Chrome 156`（与 JS 一致） |
| `navigator.platform` | `MacIntel`（未打补丁，但补丁版会写成 `macOS`） | `MacIntel`（CDP 原生） |
| `screen.availHeight` | `2160`（= height，没有菜单栏） | `2135` |
| `hardwareConcurrency` | 宿主 CPU 数（OVH 上会是 2/4） | 画像值（8/10/12） |

### 2. UA / Client Hints 改成 CDP 原生设置

以前是「JS 打补丁 + `setExtraHTTPHeaders` 改头」两条路各走一半，任何一边拼错就自相矛盾。

现在统一走 `Network.setUserAgentOverride` + `userAgentMetadata`：

```python
cdp.send("Network.setUserAgentOverride", {
    "userAgent": p.ua,
    "acceptLanguage": p.accept_language_plain,   # 裸列表，见下
    "platform": p.nav_platform,                  # "MacIntel" / "Win32"
    "userAgentMetadata": {brands, fullVersionList, platformVersion, arch, ...},
})
```

好处：
- `navigator.userAgentData` 仍是**真正的 `NavigatorUAData` 实例**（`instanceof` 通过、getter 是 native、属性可枚举）
- `sec-ch-ua*` 请求头与 JS 值由同一个来源生成，物理上不可能矛盾
- 高熵 hints（`sec-ch-ua-full-version*` / `arch` / `bitness`）由 Chromium 按 `Accept-CH` 自行决定是否发送，和真浏览器行为一致

**踩坑**：`acceptLanguage` 会被 Chrome 按逗号直接切分写进 `navigator.languages`，
带 q 值就变成 `["fr-CA", "fr;q=0.9", "en;q=0.8"]`（真浏览器不可能）。
所以拆成两层：
- `accept_language_plain`（`fr-CA,fr,en`）→ CDP `acceptLanguage` → `navigator.languages`
- `accept_language`（`fr-CA,fr;q=0.9,en;q=0.8`）→ `Network.setExtraHTTPHeaders` → HTTP 头

实测两者同时生效：头是带 q 值的，`navigator.languages` 是裸列表。

### 3. 删掉 4 个浏览器从不发送的伪头

`DeviceProfile.headers()` 以前会发 `downlink` / `rtt` / `dpr` / `viewport-width`。
这四个是 JS 属性（`navigator.connection.downlink`、`devicePixelRatio`、`innerWidth`），
浏览器从不把它们当 HTTP 头发出去 —— 任何 WAF 看到这四个头，不需要任何启发式就判定非浏览器。

同时把请求头拆成两套：

| 场景 | Accept | sec-fetch-mode | sec-fetch-dest | 其它 |
|---|---|---|---|---|
| 导航（`/self-service/*/browser`） | `text/html,...` | `navigate` | `document` | `sec-fetch-user: ?1`、`Upgrade-Insecure-Requests: 1` |
| XHR / fetch（tRPC、REST） | `application/json` | `cors` | `empty` | `priority: u=1, i` |

### 4. 协议层补 `Origin` / `Referer`，GET 不再带 CSRF

以前 POST 完全没有 `Origin` / `Referer`（`kratos.py` 的 docstring 写着"必须带"，
但代码里从来没加过），GET 反而带着 `x-csrftoken`。

现在：
- `Origin` 按**页面** origin 发，不是按请求自己的 host —— Kratos 的 API 在
  `auth.mistral.ai`，但页面在 `v2.auth.mistral.ai`，跨源时 `Origin` 必须是后者
  （映射表见 `protocol.py::PAGE_ORIGINS`）
- `Referer` 所有请求都发
- `sec-fetch-site` 按实际关系算 `same-origin` / `same-site` / `cross-site`
- `x-csrftoken` 只在写请求上带

**实测（修前 → 修后）**

```
修前 POST:
  Accept: application/json          ← 导航端点却是 JSON
  sec-fetch-dest: empty             ← 导航端点却是 empty
  sec-fetch-mode: cors
  downlink: 10.0 / rtt: 50 / dpr: 1.5 / viewport-width: 3264
  Accept-Encoding: gzip, deflate
  x-csrftoken: tok                  ← GET 也带
  （无 Origin、无 Referer）

修后 导航 GET:
  Accept: text/html,application/xhtml+xml,...
  Upgrade-Insecure-Requests: 1
  sec-fetch-site: none / sec-fetch-mode: navigate / sec-fetch-user: ?1 / sec-fetch-dest: document
  Referer: https://v2.auth.mistral.ai/
  （无 x-csrftoken）

修后 POST:
  Accept: application/json / sec-fetch-mode: cors / sec-fetch-dest: empty
  sec-fetch-site: same-site
  Origin: https://v2.auth.mistral.ai
  Referer: https://v2.auth.mistral.ai/
  x-csrftoken: tok
```

`Accept-Encoding` 改成按 curl 实际能力协商（`curl -V` 探测 brotli/zstd），
能支持就发 `gzip, deflate, br, zstd`（与浏览器一致），不支持才退到 `gzip, deflate`。

---

## 二、IP 侧已修（4 项）

### 5. 出口国改成实测，不再靠人工填的 `region`

以前全链路没有任何地方解析代理出口国，`region` 默认就是字符串 `"CA"`。
代理随机挑，画像固定 CA → 「IP 在美国、语言 `en-CA`、时区 `America/Toronto`、
账单地址 Quebec」，正是 `FINGERPRINT_ANALYSIS.md` 自己写下的最容易被抓的点。

现在 `chain_gateway.probe_exit()` 通过代理打
`https://www.cloudflare.com/cdn-cgi/trace`，解析 `ip=` / `loc=` / `colo=`，
画像和账单地址都按实测 `loc` 生成。

`GEO_PROFILES` 从 2 个国家扩到 10 个（CA/US/GB/DE/FR/NL/IE/AU/JP/SG），
**未知国家直接抛 `UnknownCountry`**，不再静默回落 CA。

### 6. 健康检查不再打目标站

以前 `probe_proxy()` 是：

```python
curl --socks5-hostname ... https://v2.auth.mistral.ai/login
```

UA 是 `curl/8.x`、JA3 是 curl 的、无 cookie，目标就是会被注册的那个域，
最多重试 6 次。在 Mistral 日志里就是「先 curl 后注册」——
注册机的教科书指纹。

现在只打第三方回显，`test_proxy_dedupe.py::test_probe_never_targets_mistral`
用 AST 断言 `chain_gateway` 里不存在任何指向 `mistral.ai` 的字符串字面量。

### 7. 一 IP 一号

以前三处都是 `n % len` 轮转，`config/mistralsignup.go` 的注释直接写
「代理可以重复使用」：10 个代理 40 个号 = 每个 IP 4 个号，串行连续跑。

现在：
- `Candidate` 落库 `exit_ip` / `country`
- Go 侧 `usedExitIPs()` 收集 24 小时内已用的出口 IP，通过 `used_exit_ips` 传给 worker
- worker 按序探测代理，**跳过已占用的出口 IP**；全部占用则失败（fail-closed），
  不复用、不直连
- 显式指定代理时如果 IP 已被占用，直接拒绝

### 8. 单条添加也分配代理序号

`apiMistralCandidateAdd` 以前不设 `ProxyIndex` → 零值 `0` → `by_index(0)` →
**所有手动添加的候选都从第一个代理出口**。现在按 `len(store.List())` 往后排。

---

## 三、顺带修掉的其它 tell

| 项 | 以前 | 现在 |
|---|---|---|
| `window.chrome` | 被写成 `{runtime:{}}`，把真 Chrome 的 `loadTimes`/`csi`/`app` 全删了 | 不再覆盖 |
| `--disable-features=IsolateOrigins,site-per-process` | 会改 `originAgentCluster`/`crossOriginIsolated`，本身就是自动化标志 | 已移除 |
| `viewport` | 等于整屏 → `innerHeight == screen.height`（物理不可能） | `screen.height - 120` |
| `navigator.platform` | 补丁版会写成 `"macOS"` | CDP 原生 `MacIntel` / `Win32` |
| `Intl.DateTimeFormat` | 用 `Proxy` 包了一层（最容易被检测的篡改点），且和 `getTimezoneOffset()` 打架 | 已移除（Playwright `timezone_id` 原生生效） |
| `defineProperty` | 都没带 `enumerable: true` → 属性变成不可枚举 | 全部 `enumerable: true` |
| getter 的 `toString()` | 返回箭头函数源码 | `Function.prototype.toString` 掩码，返回 `function get x() { [native code] }` |
| `navigator.connection` | 返回普通对象（`instanceof NetworkInformation` 失败） | 改 `NetworkInformation.prototype` 上的 getter |
| `hardwareConcurrency` / `deviceMemory` | 泄露宿主 CPU 数 | 按平台画像固定 |
| WebGL renderer 前缀 | `Google Inc. (Intel Inc.)` 配 `ANGLE (Intel, ...)`，厂商标识不一致 | `ANGLE (Intel Inc., ...)` |

---

## 四、验证方式

```bash
cd tools/mistral
python3 test_fingerprint.py      # 12 项：JS 语法 / 伪头 / 导航头 / 国家解析 / 平台自洽
python3 test_proxy_dedupe.py     # 4 项：一 IP 一号 / 出口探测不碰目标站
python3 test_pacing.py           # 12 项
python3 test_cards.py            # 15 项
cd ../.. && go build ./... && go vet ./internal/server/
```

`test_fingerprint.py::test_console_js_parses` 会对
**10 国 × 3 平台 × 2 Chrome 版本 = 60 个画像**逐个渲染 JS 并跑 `node --check`，
这类拼接错误以后不会再静默通过。

---

## 五、运行时改造（2026-11-09 第二批）

### 9. 换真 Google Chrome，不再用 Playwright 自带 Chromium

Playwright Chromium 和真 Chrome 在页面里的区别是硬性的：

| | Playwright Chromium | 真 Google Chrome |
|---|---|---|
| `navigator.plugins.length` | **0** | 5（PDF 三件套 + 2） |
| `window.chrome` | `undefined` | `{loadTimes, csi, app}` |
| `navigator.pdfViewerEnabled` | false | true |

任何 fingerprintjs 级别的库第一眼就读这两项。

现在 `browser.py` 按优先级解析：
1. `MISTRAL_BROWSER_CHANNEL`（`chrome` / `chromium`）
2. 系统装的真 Chrome（macOS `/Applications/...`、Linux `/usr/bin/google-chrome` 等）
3. Playwright Chromium（兜底，会打 warning）

容器镜像也装上了 Google 官方 Chrome stable（`Dockerfile`，带 `CHROME_DEB_VERSION` 构建参数）。

**实测（macOS，真 Chrome 156.0.8078.80）**

```
[browser] binary=/Applications/Google Chrome.app/... version=156.0.8078.80 headless=True real_chrome=True
plugins: 5
chromeKeys: ["loadTimes", "csi", "app"]
```

### 10. 浏览器走 headed + xvfb，WebGL 用真 GPU

新 headless 的 WebGL 固定是 `ANGLE (Google, Vulkan ... SwiftShader)`，
和画像声称的 Apple/NVIDIA GPU 直接矛盾，一条 `getParameter(37446)` 就判死。

两层修改：
- 容器里装 `xvfb`，`worker.py` 启动时 `ensure_display()` 自动拉起 Xvfb 并设置 `DISPLAY`，
  浏览器以 headed 运行（`headless=False`），退出时 `stop_display()` 清理
- `fingerprint.py` 新增 `probe_real_gpu()`：启动时用同一个浏览器二进制探一次真实
  WebGL vendor/renderer，**探测到就照搬真值**（连同"这本来就是真的"一起），
  探测到 SwiftShader 或失败才用画像值兜底

不加 xvfb 也不能用的原因：`headless=False` 且没有 `DISPLAY` 时 Chrome 会直接起不来，
所以 `ensure_display()` 拿不到 Xvfb 时会打 warning 并退到 headless（此时 WebGL 仍会暴露）。

**实测（同一台机器）**

| | 修前（Playwright Chromium headless） | 修后（真 Chrome headless） |
|---|---|---|
| WebGL renderer | `ANGLE (Google, Vulkan ... SwiftShader)` | `ANGLE (Apple, ANGLE Metal Renderer: Apple M4)` |
| plugins / chrome | 0 / `null` | 5 / `{loadTimes, csi, app}` |

> 注意：这里 headless 也能拿到真 GPU 是因为 macOS 上的真 Chrome 没被禁 GPU。
> Linux 容器里 headless 仍然拿不到，必须 xvfb + headed，这也是镜像里装 xvfb 的原因。

### 11. 非真 Chrome 时的 plugins 兜底

`new_context(plugins=[...])` 只在非真 Chrome 时传（真 Chrome 本来就自带，
传了反而会覆盖成假的）。内容就是真 Chrome 那 5 个 PDF 插件。

### 12. 删掉 `--disable-features=IsolateOrigins,site-per-process`

它会改掉 `originAgentCluster` / `crossOriginIsolated` 的行为，本身就是自动化标志。
回归测试 `test_no_isolate_origins_flag` 守着这条。

---

## 六、剩余风险（**未修**，不要当成已解决）

| 优先级 | 项 | 说明 |
|---|---|---|
| ~~P0~~ ✅ | ~~浏览器是 Playwright Chromium 而不是真 Chrome~~ | 已改：resolver 优先真 Chrome，镜像装官方 Chrome stable |
| ~~P0~~ ✅ | ~~headless 下 WebGL 是 SwiftShader~~ | 已改：xvfb + headed + `probe_real_gpu()` 照搬真值 |
| P0 | 协议层 TLS/JA3 仍是 curl | `console`/`chat` 实测 403 就是因为它。需要 `curl_cffi` 或全走 `page.evaluate` |
| P0 | 每请求一个新 curl 进程 | 无连接复用、TLS session resumption 率 0%。需要长连接会话 |
| P1 | 验证码提交太快 | 发码到提交只有 6~15 秒，真人是 20~90 秒 |
| P1 | 零鼠标轨迹 | `human_move()` 定义了但**从未被调用**，所有点击都是瞬移 |
| P1 | 三把 Key 连发 + 命名模式 | `api-1234` / `vibe-5678` / `studio-9012` |
| P1 | UA/CH 只有 6 套组合 | 几百个号共享 6 套 UA，按三元组聚类一抓一个准 |
| P1 | 画像由 email 派生而非 IP | 一个住宅 IP 上出现 N 台不同设备，比"同设备多号"更假 |
| P1 | 邮箱域名 100% `@icloud.com` | 真实用户群 iCloud 占比约 5% |
| P1 | `MISTRAL_PACE_OFF` / `SCALE=0` | 生产误设会让节奏保护归零，且无告警 |
| P2 | `sec-ch-ua-full-version*` 在首个请求就发 | 协议层的问题；浏览器层已由 Chromium 自己按 `Accept-CH` 决定 |
| P1 | Windows 画像在 macOS 宿主上会被 GL 戳穿 | `probe_real_gpu()` 拿真值后，`windows`/`macos-intel` 画像会显示宿主实际 GPU。要在容器里真正模拟其他平台，需要按画像限制平台池 |
| P2 | HTTP/1.1 only | 协议层不说 h2 |
| P2 | 卡按 `n % len` 复用 | 订阅接通即引爆 Stripe 聚类 |
| P2 | 账单 `legal_name` 从未填 | Stripe 卡主姓名与账单姓名不一致 |
| P2 | 账单地址用地标建筑 | `350 Fifth Avenue`（帝国大厦）等在反欺诈共享地址名单里 |
