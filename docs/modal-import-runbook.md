# Modal 号池导入流程（Runbook）

用户说「导入 modal」时，按本文流程执行，不要重新问目标位置。

## 环境事实

| 项 | 值 |
|---|---|
| 目标服务 | 远程 OVH：`ssh ovh-server`（167.114.158.4，port 49555） |
| 部署目录 | `/home/ubuntu/project/opendesign-balancer` |
| 数据目录 | 同上 `data/`（`config.json` 是账号池真实存储） |
| 容器 | `opendesign-balancer`，`127.0.0.1:8790:8790`，健康检查 `/healthz` |
| 公网入口 | `https://ods.yeschoy.io` |
| 导入密钥 | 管理台「下游密钥 → 导入密钥」，`sk-odbimp-…`。当前唯一一把：`杨总`（`ik_d783236e3d71d33e3fa4843a`，默认分组 `default`）。明文在远程 `data/config.json` 的 `importKeys[].key` |
| 归属分组 | 不指定分组时 `wk-` 账号自动进 `modal` 组 |
| 上游 base | `https://inference.us-west.modal.direct/v1` |

## 步骤

### 1. 校验 token 形态

Modal Proxy Token 必须是**完整形态** `wk-<id>.ws-<secret>`，两段都要。

只给 `wk-<id>` 时上游每条路径都回 401 `invalid proxy auth credentials`，最终会被判为「Token 不完整」并隔离，不是上游故障。Token secret 只在创建时显示一次，看不到就让用户新建。

### 2. 直连上游验证（先验证，别把坏号导进池子）

```sh
TOKEN='wk-<id>.ws-<secret>'
curl -s -w '\nHTTP %{http_code}\n' --max-time 30 \
  https://inference.us-west.modal.direct/v1/models \
  -H "Authorization: Bearer $TOKEN"
```

- 200 且 `data[]` 有内容 → token 有效，同时记下该 token 可见的 Endpoint hostname 列表（后面要告诉用户 model 填什么）。
- 400/401/403 → 停下来告诉用户，不要导入。

### 3. 通过开放导入接口入池

```sh
IMPKEY='sk-odbimp-…'            # 从远程 data/config.json 的 importKeys[].key 取，别硬编码进仓库
curl -s -w '\nHTTP %{http_code}\n' --max-time 60 \
  -X POST https://ods.yeschoy.io/open/v1/accounts/import \
  -H "Authorization: Bearer $IMPKEY" \
  -H 'Content-Type: application/json' \
  -d '{"runtimeKey":"'"$TOKEN"'","provider":"modal","name":"Modal-N","note":"手工导入 YYYY-MM-DD"}'
```

- 显式带上 `"provider":"modal"` 更稳（前缀也能推断）。
- `name` ≤ 50 字符；命名沿用 `Modal-1`、`Modal-2`… 递增。
- 只接受导入密钥 `sk-odbimp-…`；下游密钥 `sk-odb-…` 和管理员登录调不通。
- 鉴权也接受请求头 `X-Import-Key: sk-odbimp-…`。

## 响应判定

| status | 处理 |
|---|---|
| `added` | 成功，已入池并启用 |
| `unchanged` | 完全重复提交，无需处理 |
| `staged` | 与现有账号冲突（同名/同 key），**没进池**，需管理员在管理台暂存区处理。要主动告知用户 |
| `error` | 校验失败，看 `error` 字段 |

HTTP 200 表示至少一条 added/unchanged；202 表示只进了暂存；400 表示整单非法。

## 4. 验证落地

```sh
# 账号确实进了 modal 组（不打印完整 key）
ssh ovh-server 'cd ~/project/opendesign-balancer/data && python3 -c "
import json
c=json.load(open(\"config.json\"))
for a in c.get(\"accounts\",[]):
    if a.get(\"provider\")==\"modal\":
        print(a.get(\"id\"), a.get(\"name\"), a.get(\"group\"), a.get(\"enabled\"), (a.get(\"runtimeKey\") or \"\")[:16])
"'

# 服务端审计日志有 open import 记录
ssh ovh-server 'docker logs opendesign-balancer --since 10m 2>&1 | grep -i "open import"'

# 真实推理跑一发
curl -s -w '\nHTTP %{http_code}\n' --max-time 90 \
  https://inference.us-west.modal.direct/v1/chat/completions \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"model":"<endpoint-hostname>","messages":[{"role":"user","content":"reply OK"}],"max_tokens":16}'
```

## 交付话术要点

回复里给用户：账号名、accountId、分组、状态、可选 Endpoint 列表，并提醒：

- 下游 `model` 填 **Endpoint 的 hostname**，不是模型名。
- 只有分组填 `modal` 的下游密钥才会调度到这个号。
- Modal 无额度接口，余额只能在控制台看，本服务只在 402 时冷却。
- 每 10 分钟读一次 `/v1/models` 缓存 catalog，不在列表里的模型不派给该账号。

## 禁止

- 不要把 token / 导入密钥明文写进仓库、commit、issue。
- 不要直接改远程 `config.json` 绕过接口（会破坏 revision 和审计）。先用 `POST /open/v1/accounts/import`；只有在接口不可用时才手工改，且必须先备份 `config.json`。
