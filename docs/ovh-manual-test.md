# OVH 手动测试指南（AdsPower 跨机 + 额度查询）

> 目标：在 OVH 上手动验证「Mac 跑浏览器、OVH 跑号池」这套架构
> 前提：Mac 在线，AdsPower 客户端在运行

---

## 0. 架构速览

```
┌────────────────────┐         ┌──────────────────────┐
│  Mac（浏览器宿主）   │  SSH    │  OVH（号池/调度）      │
│  AdsPower + profile │◄───────►│  account-pool-balancer│
│  隧道守护 :15026     │  隧道   │  额度查询（纯 HTTP）    │
└────────────────────┘         └──────────────────────┘
```

**关键结论：额度查询不需要 Mac。** 只有注册/验证/绑卡需要走隧道。

---

## 1. Mac 侧准备（一次性）

### 1.1 确认 AdsPower 在跑

```bash
# 应返回 {"code":0,"msg":"success"}
curl -s http://127.0.0.1:50325/status
```

如果没起来：

```bash
open -a "AdsPower Global"
sleep 12

export ADS_API_KEY=<你的 key>
ads start
```

### 1.2 起隧道守护

```bash
cd <项目目录>
nohup python3 tools/ads_tunnel_daemon.py \
  --listen 0.0.0.0:15026 \
  --ssh ovh-server > /tmp/daemon.log 2>&1 &

# 确认
curl -s http://127.0.0.1:15026/health
# {"ok": true, "tunnels": 0, "api_tunnel": true}
```

### 1.3 把端口暴露给 OVH

```bash
ssh -N -f -R 15025:127.0.0.1:50325 ovh-server   # AdsPower API
ssh -N -f -R 15026:127.0.0.1:15026 ovh-server   # 隧道守护
```

> ⚠️ Mac 重启后这两条要重跑。

### 1.4 确认隧道通了

```bash
ssh ovh-server 'curl -s http://127.0.0.1:15025/status; echo; curl -s http://127.0.0.1:15026/health'
```

两条都该有 JSON 返回。

---

## 2. OVH 侧：验证额度查询（最简单）

**这一步完全不需要 Mac 在线跑浏览器**，只要 cookie 已经存进号池。

### 2.1 直接跑 Python（快速验证）

```bash
cd <项目目录>/tools

# 查额度（cookie 从库里取）
python3 deploy_quota.py --cookie "<session_cookie>" --org <ORG_ID>

# 只看会话有效性
python3 deploy_quota.py --cookie "<session_cookie>" --alive
```

预期：

```
账号: user@example-icloud.com (id=<ACCOUNT_ID>, org=<ORG_ID>)
org_id : <ORG_ID>
余额   : $106.00
流水   : 3 条
  2026-11-14T19:11:10     100.00  purchase_promo
  2026-11-14T19:11:08       5.00  bought
  2026-11-14T14:35:24       1.00  initial
```

### 2.2 走 Go 服务的 API

```bash
# 登录拿会话与 CSRF
LOGIN=$(curl -s -c /tmp/ck.txt -H 'Content-Type: application/json' \
  -H 'Origin: http://127.0.0.1:8790' \
  -d '{"password":"<ADMIN_PASSWORD>"}' \
  http://127.0.0.1:8790/api/login)
CSRF=$(echo "$LOGIN" | python3 -c 'import sys,json;print(json.load(sys.stdin)["csrf"])')

# 单账号查额度
curl -s -b /tmp/ck.txt -H "X-CSRF-Token: $CSRF" \
  -H 'Content-Type: application/json' \
  -H 'Origin: http://127.0.0.1:8790' \
  -X POST -d '{}' \
  http://127.0.0.1:8790/api/deploy/candidates/<候选ID>/quota

# 批量刷新
curl -s -b /tmp/ck.txt -H "X-CSRF-Token: $CSRF" \
  -H 'Content-Type: application/json' \
  -H 'Origin: http://127.0.0.1:8790' \
  -X POST -d '{}' \
  http://127.0.0.1:8790/api/deploy/quota-refresh
```

预期（实测过）：

```json
{"ok": true, "org_id": "<ORG_ID>", "balance": 106,
 "rows": [{"charged_on":"2026-11-14T19:11:10Z","amount":"100.00","reason":"purchase_promo"}, ...]}
```

批量：

```json
{"count": 1, "known": 1, "total_credit": 106,
 "items": [{"id":"...","email":"...","ok":true,"balance":106}]}
```

---

## 3. OVH 侧：验证跨机驱动浏览器

这一步**必须** Mac 在线（AdsPower + 守护都在跑）。

### 3.1 列 profile

```bash
cd <项目目录>/tools
python3 ads_remote.py --api-key <KEY> --base http://127.0.0.1:15025 list
```

预期：

```
共 3 个 profile（经隧道 http://127.0.0.1:15025）

  k1j2oae0       no=  20 deploy-002       ip=67.84.113.113    us     bridge=17201
  k1j2oaa5       no=  19 deploy-001       ip=-                       bridge=17200
  k1cet4ua       no=  18                  ip=73.252.209.238   us     bridge=17080
```

### 3.2 启动并连 CDP

```bash
ADS_TUNNEL_DAEMON=http://127.0.0.1:15026 \
python3 ads_remote.py --api-key <KEY> --base http://127.0.0.1:15025 \
  open k1j2oaa5
```

预期：

```
  CDP 隧道: 本机 :16000 -> Mac :57452
profile: k1j2oaa5
Mac 端口: 57452  本机映射: 16000
ws: ws://127.0.0.1:16000/devtools/browser/...
```

### 3.3 用 Python 实际操控

```bash
cd <项目目录>/tools
python3 -c "
import sys; sys.path.insert(0,'.')
from ads_transport import open_remote_profile

p = open_remote_profile(
    '<KEY>', 'k1j2oaa5',
    api_base='http://127.0.0.1:15025',
    tunnel_daemon='http://127.0.0.1:15026',
)
print('UA:', p.eval('navigator.userAgent')[:60])
print('TZ:', p.eval('Intl.DateTimeFormat().resolvedOptions().timeZone'))
print('IP:', p.eval(\"fetch('https://api.ipify.org').then(r=>r.text()).catch(e=>'ERR')\"))
r = p.get('https://ajax-portal.deploy.com/api/csrf')
print('CSRF:', r.status, r.body[:60])
"
```

预期（实测过）：

```
UA: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36
TZ: America/Chicago
IP: 128.92.198.59          ← Mac 代理的 IP，不是 OVH 的
CSRF: 200 {"status_code":200,"message":"success",...
```

**关键点**：IP 是 Mac 代理的，不是 OVH 的。指纹和出口全来自 Mac。

---

## 4. 在 Go 服务里配置 AdsPower

### 4.1 设置项

`PUT /api/deploy/settings`，相关字段：

```json
{
  "transport": "adspower",
  "ads_api_key": "<AdsPower API Key>",
  "ads_api_base": "http://127.0.0.1:15025",
  "ads_tunnel_daemon": "http://127.0.0.1:15026",
  "ads_headless": false
}
```

| 字段 | 说明 |
|---|---|
| `transport` | 设为 `adspower` 才走 profile 路径 |
| `ads_api_key` | AdsPower 本地 API 的 key |
| `ads_api_base` | 本机是 `127.0.0.1:50325`；OVH 上是隧道端口 `15025` |
| `ads_tunnel_daemon` | Mac 侧守护地址（OVH 上是 `127.0.0.1:15026`） |
| `ads_headless` | OVH 上没用（浏览器在 Mac），保持 false |

### 4.2 候选需要绑定 profile

每个候选必须设 `ads_profile_id`：

```json
{"email":"a@b.com", "mailbox_url":"...", "ads_profile_id":"k1j2oaa5"}
```

一号一 profile，不能共用。

### 4.3 注册流程（Go → Python → Mac）

```
POST /api/deploy/candidates/{id}/register
  → 写 job.json（含 ads_profile_id + ads_api_key + ads_api_base）
  → python3 deploy_registrar.py --job
      → 经隧道启动 Mac 上的 profile
      → 页面内 fetch 注册
      → 取信验证（在 profile 内打开链接）
      → 建 API Key
      → 返回 session_cookie 等
  → 落库
```

### 4.4 绑卡流程

```
POST /api/deploy/candidates/{id}/bind-card
  → job 里带 ads_profile_id
  → python3 deploy_payment.py --bind-job
      → **同一个 profile** 里跑 Stripe.js
      → 绑卡 + 充值
```

**绑卡必须用注册时的同一个 profile**，否则 Stripe 看到的是另一台设备。

---

## 5. 数据存放分工

| 数据 | 位置 | 说明 |
|---|---|---|
| profile（Cookie/本地存储/指纹） | **Mac 本地** | 搬到 OVH 会变新设备登录 |
| profile 配置（代理/指纹参数） | AdsPower 云端 | 自动同步 |
| session_cookie | **OVH 数据库** | 查额度用 |
| account_id / org_id | **OVH 数据库** | 号池元数据 |
| access_key / secret_key | **OVH 数据库** | 调模型 API 用 |
| 绑卡信息 / 余额 | **OVH 数据库** | 展示用 |

---

## 6. 常见问题

### Q: 隧道断了怎么办

```bash
# Mac 上重跑
nohup python3 tools/ads_tunnel_daemon.py --listen 0.0.0.0:15026 --ssh ovh-server &
ssh -N -f -R 15025:127.0.0.1:50325 ovh-server
ssh -N -f -R 15026:127.0.0.1:15026 ovh-server
```

### Q: 额度查询返回 401

会话失效。**不代表账号报废** —— cookie 会被服务端轮换。

处理：走隧道重新登录刷新 cookie，不要判账号失败。

```bash
# 在 Mac 上重新提取
python3 tools/deploy_quota.py --extract <KEY> <profile_id>
```

### Q: 跨机启动 profile 超时

检查：

```bash
# Mac 上
curl http://127.0.0.1:15026/health

# OVH 上
curl http://127.0.0.1:15025/status
curl http://127.0.0.1:15026/health
```

守护的 `/tunnels` 端点能看到当前开了哪些隧道。

### Q: 为什么不直接在 OVH 装 AdsPower

两个硬障碍：

1. **profile 数据在本地磁盘**，云端只同步配置
2. **OVH 显卡是 ASPEED**，Chromium 报 `WebGL1 blocklisted`，
   AdsPower 的 WebGL 伪装起不来

详见 `docs/adspower-integration.md` §15。

---

## 7. 快速自检清单

```
□ Mac: AdsPower 客户端在跑（curl :50325/status → 200）
□ Mac: 隧道守护在跑（curl :15026/health → ok:true）
□ Mac→OVH: 两条 -R 隧道已建立
□ OVH: curl :15025/status → 200
□ OVH: curl :15026/health → ok:true
□ OVH: 额度查询返回 balance（纯 HTTP，不需 Mac）
□ OVH: 跨机能连上 CDP（需 Mac 在线）
```
