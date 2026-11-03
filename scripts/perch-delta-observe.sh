#!/usr/bin/env bash
#
# perch-delta-observe.sh — 观察 account-pool-balancer 到底有没有在发 held-context 增量请求。
#
# 为什么需要这个脚本：增量有没有生效，无法从"请求成功"看出来。全量请求和增量请求
# 都返回 200，增量被服务端拒绝后也会自动重发全量并成功。唯一诚实的证据是服务端
# 自己维护的计数器（/api/perch/fingerprint 的 delta 段），而这个接口是 adminOnly，
# 需要登录会话 —— 所以脚本先登录、采样、再登出。
#
# 三个子命令：
#   doctor   —— 一次性诊断：逐项判断链路断在哪一环，给出结论而不是一串数字。
#   snapshot —— 采一次样，追加成一行 JSON 到历史文件（给 watch 用，也适合 cron）。
#   watch    —— 持续采样，每次打印相对上一次的变化量。这是回答
#               "之后到底有没有走增量" 的主要手段。
#
# 用法见文件末尾的 examples。
#
# 安全性说明：
#   - 密码只从环境变量 ODB_ADMIN_PASSWORD 读取，绝不出现在命令行参数里
#     （命令行会被 `ps` 看到；环境变量在 /proc/<pid>/environ 里仅有同用户可读）。
#   - 脚本结束时（包括出错和 Ctrl-C）都会调用 /api/logout 让会话失效，避免在服务端
#     留下 24 小时有效的会话。即使登出失败也不影响观察结果，只会打一行警告。
#   - 历史文件只存计数器数值，不存任何 token / cookie / 密码。

set -uo pipefail

# ---------------------------------------------------------------- 默认配置

# 服务监听地址。docker-compose.yml 把端口绑在 127.0.0.1:8790，所以在宿主机上
# 直接访问这个地址即可，不需要经过公网或反代。
BASE_URL="${ODB_BASE_URL:-http://127.0.0.1:8790}"

# 历史文件：每行一个 JSON 采样。放在脚本同目录的 .perch-delta/ 下。
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HISTORY_DIR="${ODB_DELTA_HISTORY_DIR:-$SCRIPT_DIR/.perch-delta}"
HISTORY_FILE="$HISTORY_DIR/samples.jsonl"

# watch 的采样间隔（秒）与总轮数。0 轮表示一直跑。
INTERVAL="${ODB_DELTA_INTERVAL:-60}"
ROUNDS="${ODB_DELTA_ROUNDS:-0}"

# 登录失败重试。apiLogin 对同一 IP 有 5 次/60 秒 的限流，所以这里的退避
# 从 2 秒起、最多 3 次，且只在 429/5xx 时重试（密码错不重试，重试只会加速触顶）。
LOGIN_ATTEMPTS="${ODB_DELTA_LOGIN_ATTEMPTS:-3}"

# ---------------------------------------------------------------- 输出格式

# 只在输出到终端时上色，重定向到文件时保持纯文本（方便 diff 和入日志）。
if [[ -t 1 ]]; then
  C_RED=$'\033[31m'; C_GRN=$'\033[32m'; C_YEL=$'\033[33m'
  C_BLU=$'\033[36m'; C_DIM=$'\033[2m'; C_BLD=$'\033[1m'; C_RST=$'\033[0m'
else
  C_RED=''; C_GRN=''; C_YEL=''; C_BLU=''; C_DIM=''; C_BLD=''; C_RST=''
fi

ok()   { printf '%s  [OK]%s   %s\n'   "$C_GRN" "$C_RST" "$*"; }
warn() { printf '%s  [!!]%s   %s\n'   "$C_YEL" "$C_RST" "$*"; }
bad()  { printf '%s  [XX]%s   %s\n'   "$C_RED" "$C_RST" "$*"; }
info() { printf '%s  [..]%s   %s\n'   "$C_DIM" "$C_RST" "$*"; }
hdr()  { printf '\n%s%s%s\n' "$C_BLD" "$*" "$C_RST"; }
die()  { printf '%s错误：%s%s\n' "$C_RED" "$*" "$C_RST" >&2; exit 1; }

# ---------------------------------------------------------------- 依赖检查

need() {
  command -v "$1" >/dev/null 2>&1 || die "缺少依赖 $1。在 Debian/Ubuntu: apt-get install -y $2"
}

need curl curl
need jq   jq
jq --version >/dev/null 2>&1 || die "jq 不可用（版本过旧？）"

# ---------------------------------------------------------------- 会话管理

COOKIE_JAR=""
# SESSION_COOKIE 存的是完整的 Cookie 头值（"odb_session=<token>"），
# 不是裸 token —— 直接把它塞进 -H "Cookie: $SESSION_COOKIE" 才成立。
# 早期版本只存了值、却当整条头发出去，结果登录 200、随后一切请求 401。
SESSION_COOKIE=""
CSRF_TOKEN=""

# cleanup 无论正常结束、出错还是被中断都会跑。登出是尽力而为：失败只警告，
# 因为会话会在服务端 24 小时后自然过期，而采样结果此时已经拿到了。
cleanup() {
  local code=$?
  if [[ -n "${COOKIE_JAR:-}" && -n "${SESSION_COOKIE:-}" ]]; then
    curl -sS -m 5 -o /dev/null \
      -X POST "$BASE_URL/api/logout" \
      -H "Cookie: $SESSION_COOKIE" \
      -H "Origin: $BASE_URL" 2>/dev/null \
      || printf '%s  [..]%s   登出失败，会话将在服务端自然过期（不影响采样结果）\n' "$C_DIM" "$C_RST" >&2
  fi
  [[ -n "${COOKIE_JAR:-}" && -f "${COOKIE_JAR:-}" ]] && rm -f "$COOKIE_JAR"
  exit $code
}
trap cleanup EXIT INT TERM

# login 用 ODB_ADMIN_PASSWORD 换一个会话。
#
# 注意 sameOrigin：apiLogin 在 Origin 存在时要求它的 host 与 r.Host 一致。
# 我们从 127.0.0.1 直连，所以显式发 Origin: http://127.0.0.1:8790 即可匹配。
login() {
  local pw="${ODB_ADMIN_PASSWORD:-}"
  [[ -n "$pw" ]] || die "未设置 ODB_ADMIN_PASSWORD。
  这个脚本需要管理员密码才能读 /api/perch/fingerprint（该接口是 adminOnly）。
  请不要把它写在命令行里，用环境变量传入：

    export ODB_ADMIN_PASSWORD='...'
    $0 doctor

  在 OVH 主机上，密码通常已经存在于容器环境里，可以直接取用：

    export ODB_ADMIN_PASSWORD=\$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' \\
        account-pool-balancer | sed -n 's/^ODB_ADMIN_PASSWORD=//p')"

  COOKIE_JAR="$(mktemp "${TMPDIR:-/tmp}/odb-delta-cookies.XXXXXX")"
  chmod 600 "$COOKIE_JAR"

  local attempt=1 resp http_code body
  while :; do
    # -w 取 HTTP 状态，body 单独接。临时文件放 jar 旁边，失败时一并清掉。
    local out_file; out_file="$(mktemp "${TMPDIR:-/tmp}/odb-delta-resp.XXXXXX")"
    http_code=$(printf '{"password":%s}' "$(printf '%s' "$pw" | jq -Rs .)" \
      | curl -sS -m 10 -o "$out_file" -w '%{http_code}' \
          -X POST "$BASE_URL/api/login" \
          -H 'Content-Type: application/json' \
          -H "Origin: $BASE_URL" \
          -c "$COOKIE_JAR" \
          --data-binary @- 2>/dev/null) || http_code="000"
    body="$(cat "$out_file" 2>/dev/null)"; rm -f "$out_file"

    case "$http_code" in
      200)
        # cookie 文件里取 odb_session。用 jar 而不是响应头，避免 Set-Cookie 的
        # 多属性解析。
        #
        # 注意：HttpOnly 的 cookie 在 Netscape jar 里整行以 "#HttpOnly_" 开头
        # （前缀直接拼在域名前面），单纯用 awk 按列匹配会因为首字段不是域名而
        # 漏掉 —— 这正是最初版本登录 200、随后 401 的原因。所以这里先剥掉注释
        # 前缀再按列取：$6 = 名字，$7 = 值。
        local _tok
        _tok="$(
          sed 's/^#HttpOnly_//' "$COOKIE_JAR" \
            | awk 'NF>=7 && $6=="odb_session"{v=$7} END{print v}'
        )"
        [[ -n "$_tok" ]] || die "登录返回 200 但没拿到 odb_session cookie"
        SESSION_COOKIE="odb_session=$_tok"
        CSRF_TOKEN="$(printf '%s' "$body" | jq -r '.csrf // empty')"
        return 0
        ;;
      429)
        # 触到限流：等 60 秒以上的窗口过去。
        if (( attempt >= LOGIN_ATTEMPTS )); then
          die "登录被限流（429）且已重试 $attempt 次。apiLogin 限制为每 IP 每 60 秒 5 次。
  等一分钟后重试，或检查是否有别的脚本在用同一个密码反复登录。"
        fi
        info "登录被限流（429），$( ((attempt*20)) ) 秒后重试（$attempt/$LOGIN_ATTEMPTS）"
        sleep $((attempt*20)); ((attempt++)); continue
        ;;
      401)
        # 密码错。不重试：重试只会更快触到限流。
        die "密码错误（401）。检查 ODB_ADMIN_PASSWORD 是否与容器里的 ODB_ADMIN_PASSWORD 一致。
  容器里的实际值：docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' account-pool-balancer | grep ODB_ADMIN_PASSWORD"
        ;;
      000)
        if (( attempt >= LOGIN_ATTEMPTS )); then
          die "连不上 $BASE_URL（curl 返回 000）。
  检查：docker compose ps 看容器是否在跑；或 ODB_BASE_URL 是否设错。
  容器内监听 :8790，compose 把它绑到宿主机 127.0.0.1:8790。"
        fi
        info "连接失败，5 秒后重试（$attempt/$LOGIN_ATTEMPTS）"
        sleep 5; ((attempt++)); continue
        ;;
      *)
        if (( attempt >= LOGIN_ATTEMPTS )); then
          die "登录失败：HTTP $http_code，响应：$body"
        fi
        info "登录返回 HTTP $http_code，5 秒后重试（$attempt/$LOGIN_ATTEMPTS）"
        sleep 5; ((attempt++)); continue
        ;;
    esac
  done
}

# api_get 带着会话读一个 adminOnly 的 GET。输出 body，失败返回非 0。
api_get() {
  local path="$1" out_file http_code
  out_file="$(mktemp "${TMPDIR:-/tmp}/odb-delta-get.XXXXXX")"
  http_code=$(curl -sS -m 20 -o "$out_file" -w '%{http_code}' \
    -H "Cookie: $SESSION_COOKIE" "$BASE_URL$path" 2>/dev/null) || http_code="000"
  if [[ "$http_code" != "200" ]]; then
    local body; body="$(cat "$out_file" 2>/dev/null)"; rm -f "$out_file"
    printf 'HTTP %s: %s\n' "$http_code" "$(printf '%s' "$body" | head -c 300)" >&2
    return 1
  fi
  cat "$out_file"; rm -f "$out_file"
}

# fetch_state 拉 fingerprint 并抽出 delta 段 + 判定所需的设置项。
# 输出一个扁平 JSON 对象，字段名与 /api/perch/fingerprint 的 delta 段一致。
fetch_state() {
  local raw
  raw="$(api_get /api/perch/fingerprint)" || return 1
  printf '%s' "$raw" | jq -c '
    # 采样时间用本地时间，便于和日志对齐
    { ts: (now | todate),
      delta: (.delta // null),
      surface: (.settings.surface // "(未暴露)"),
      lane: (.settings.lane // "(未暴露)") }
  '
}

# ---------------------------------------------------------------- 判定逻辑

# 为什么判定要拆成这么多条：
# "有没有发增量" 这个问题在失败时没有单一答案。链路是
#   下游客户端声明会话 → identity() 认出 explicit → RunFor 给出稳定 runId
#   → PlanDelta 命中快照 → 服务端确认 → 计入 deltaRequests
# 任何一环断了，表现都是 hitRate=0，但修法完全不同。所以 doctor 逐环判定。
#
# 判断依据全部来自 delta 报告自身的字段语义（见 internal/perch/delta.go 的
# DeltaReport 注释），不猜。

verdict_hit_rate() {
  local st="$1"
  local d w delta whole rate
  d=$(jq -r '.delta.deltaRequests     // 0' <<<"$st")
  w=$(jq -r '.delta.wholeRequests     // 0' <<<"$st")
  rate=$(jq -r '.delta.hitRate        // 0' <<<"$st")
  delta=$d; whole=$w

  hdr "① 有没有在发增量"
  if [[ "$d" == "0" && "$w" == "0" ]]; then
    bad "一条模型调用都没有被记录（delta=0, whole=0）。"
    info "可能原因：服务刚重启、计数器刚被 reset，或者根本没有 CLI 通道的流量经过。"
    info "先制造一点真实流量（发一个下游请求），再重新跑一次。"
    return 1
  fi

  local hit_pct; hit_pct=$(awk -v r="$rate" 'BEGIN{printf "%.1f", r*100}')
  info "增量 $d 次 / 全量 $w 次，命中率 $hit_pct%"

  if [[ "$d" == "0" ]]; then
    bad "命中率为 0：每一次调用都走了全量。"
    return 1
  fi
  if awk -v r="$rate" 'BEGIN{exit !(r < 0.05)}'; then
    warn "命中率 $hit_pct% 很低：增量在发生，但大部分请求仍走全量。"
    info "常见于会话大多是单轮（每个请求都是一次新对话），或会话时常超时中断。"
    return 0
  fi
  ok "增量确实在发（命中率 $hit_pct%）。"
  return 0
}

verdict_refused() {
  local st="$1" refused confirmed unconfirmed delta
  refused=$(jq -r '.delta.refusedDeltas       // 0' <<<"$st")
  confirmed=$(jq -r '.delta.confirmedPrefixes  // 0' <<<"$st")
  unconfirmed=$(jq -r '.delta.unconfirmedPrefixes // 0' <<<"$st")
  delta=$(jq -r '.delta.deltaRequests // 0' <<<"$st")

  hdr "② 增量是不是"假装"命中、其实被服务端拒绝"
  info "被拒 $refused 次 / 已确认 $confirmed 次 / 未确认 $unconfirmed 次（共尝试增量 $delta 次）"

  if [[ "$refused" == "0" ]]; then
    ok "没有任何增量被服务端拒绝。"
  else
    # 被拒本身不是灾难：每次被拒会重发全量并成功。真正的信号是比例。
    local deny_pct
    deny_pct=$(awk -v r="$refused" -v d="$delta" 'BEGIN{ if(d>0) printf "%.0f", r/d*100; else print "0" }')
    if (( deny_pct >= 30 )); then
      bad "被拒比例 $deny_pct%：前缀信任窗口明显超过服务端自己的记忆。"
      info "每次被拒都是一次额外往返（409 + 全量重发）。调小 deltaTtlMin。"
      info "当前生效窗口：$(jq -r '.delta.limits.ttlMinutes // "?"' <<<"$st") 分钟"
    else
      warn "被拒比例 $deny_pct%：偶发，可接受。"
      info "若持续增长，说明窗口偏长：$(jq -r '.delta.limits.ttlMinutes // "?"' <<<"$st") 分钟"
    fi
  fi

  # unconfirmed 的语义：声称了前缀但服务端回显对不上。持续非零说明本地 digest
  # 算法或 tools 归一化与服务端不一致 —— 那会让缓存永远学不会基线。
  if [[ "$unconfirmed" != "0" && "$confirmed" == "0" ]]; then
    bad "从未成功确认过任何前缀（confirmed=0, unconfirmed=$unconfirmed）。"
    info "这通常表示服务端回显的 held-length/digest 与本地计算不一致，"
    info "快照因此永远学不到基线，增量也就永远发不出去。这不是配置问题，值得单独排查。"
  fi
}

verdict_chain() {
  local st="$1"
  local unscoped tracked cached_keys cached_snaps surface lane limits_ttl limits_keys

  unscoped=$(jq -r '.delta.unscopedCalls        // 0' <<<"$st")
  tracked=$(jq -r '.delta.trackedConversations  // 0' <<<"$st")
  cached_keys=$(jq -r '.delta.cachedKeys        // 0' <<<"$st")
  cached_snaps=$(jq -r '.delta.cachedSnapshots  // 0' <<<"$st")
  surface=$(jq -r '.surface // "?"' <<<"$st")
  lane=$(jq -r '.lane // "?"' <<<"$st")
  limits_ttl=$(jq -r '.delta.limits.ttlMinutes  // "?"' <<<"$st")
  limits_keys=$(jq -r '.delta.limits.maxKeys    // "?"' <<<"$st")

  hdr "③ 如果没有命中，断在哪一环"
  info "surface=$surface lane=$lane  |  TTL=${limits_ttl}min maxKeys=$limits_keys"
  info "追踪到的会话 $tracked 个 | 缓存 key $cached_keys 个 / 快照 $cached_snaps 个"

  # 环节 A：通道。增量只在 CLI 通道实现（gateway.go 的 !web 分支）。
  if [[ "$surface" == "web" ]]; then
    bad "surface=web：web 通道完全没有增量实现。"
    info "增量是 CLI model-call 的协议（x-perch-call-delta / messagesDelta），"
    info "web lane 的 body 形状不同，结构上无法做增量。改回 surface=cli 才有增量。"
    return 1
  elif [[ "$surface" == "cli" ]]; then
    ok "surface=cli：在增量支持的通道上。"
  else
    warn "surface 未从接口读到（$surface），无法确认通道。"
  fi

  # 环节 B：调用方有没有漏传账号。unscoped>0 是代码层告警：没账号 scope 的调用
  # 永远无法命中缓存，而且完全静默。
  if [[ "$unscoped" != "0" ]]; then
    bad "有 $unscoped 次调用没有携带账号 scope（unscopedCalls>0）。"
    info "这些调用永远发全量且不会报错。这是调用方缺 AccountKey 的 bug，不是配置问题。"
  else
    ok "所有调用都带上了账号 scope。"
  fi

  # 环节 C：会话是否被稳定识别。trackedConversations=0 意味着下游没有声明会话，
  # 每个请求都拿到新的 ticket runId，缓存 key 永远不同。
  if [[ "$tracked" == "0" ]]; then
    bad "没有任何被追踪的会话（trackedConversations=0）。"
    info "下游请求没有携带显式会话标识，每个请求都用了 ticket 的 runId（每次都是新的），"
    info "缓存 key 因此永远对不上，增量无法命中。"
    info ""
    info "要求的会话来源（identity.go 的优先级，且必须是 explicit 才算）："
    info "  · previous_response_id（Responses 协议 body）"
    info "  · X-Claude-Code-Session-Id / Session-Id / Thread-Id / Conversation-Id 等 header"
    info "  · prompt_cache_key / session_id / conversation_id（body 字段）"
    info "  · 父会话 header（X-Parent-Session-Id 等）"
    info ""
    info "只发裸 messages、不带任何标识的客户端命中率恒为 0 —— 这是设计而非故障："
    info "没有显式会话就无法判断两个请求是不是同一段对话的延续。"
  else
    ok "有 $tracked 个会话持有稳定 runId。"
  fi

  # 环节 D：缓存里到底有没有东西。
  if [[ "$cached_snaps" == "0" && "$tracked" != "0" ]]; then
    warn "会话被追踪，但缓存里没有任何已确认快照（cachedSnapshots=0）。"
    info "说明服务端还没确认过任何前缀。首个请求必然是全量，"
    info "它的响应会教会缓存基线；之后的请求才可能成为增量。"
    info "如果长期为 0，配合 ② 的 confirmed/unconfirmed 一起看。"
  elif [[ "$cached_snaps" != "0" ]]; then
    ok "缓存中有 $cached_snaps 个已确认前缀。"
  fi

  return 0
}

verdict_saving() {
  local st="$1" omitted sent bytes_omitted bytes_sent
  omitted=$(jq -r '.delta.messagesOmitted // 0' <<<"$st")
  sent=$(jq -r '.delta.messagesSent       // 0' <<<"$st")
  bytes_omitted=$(jq -r '.delta.bytesOmitted // 0' <<<"$st")
  bytes_sent=$(jq -r '.delta.bytesSent    // 0' <<<"$st")

  if [[ "$omitted" == "0" && "$sent" == "0" ]]; then
    return 0   # 没有增量流量，这一节无话可说
  fi

  hdr "④ 增量实际省下了多少"
  local mb_o mb_s
  mb_o=$(awk -v b="$bytes_omitted" 'BEGIN{printf "%.2f", b/1048576}')
  mb_s=$(awk -v b="$bytes_sent"    'BEGIN{printf "%.2f", b/1048576}')
  info "少发 $omitted 条消息（${mb_o} MB），实发 $sent 条（${mb_s} MB）"
  info "注意：bytesOmitted 统计的是增量请求里被省下的前缀，不含走全量的那些请求，"
  info "所以它衡量的是"增量本身有多省"，不是"总流量省了多少"。"
}

# ---------------------------------------------------------------- 子命令

cmd_doctor() {
  login
  local state
  state="$(fetch_state)" || die "读取 /api/perch/fingerprint 失败（见上面的 HTTP 状态）"

  hdr "Perch 增量诊断 — $BASE_URL"
  info "采样时间 $(jq -r '.ts' <<<"$state")"
  printf '%s（计数器是进程内累计值，服务重启或 reset 后归零）%s\n' "$C_DIM" "$C_RST"

  local rc_hit=0 rc_chain=0
  verdict_hit_rate "$state" || rc_hit=1
  verdict_refused  "$state"
  verdict_chain    "$state" || rc_chain=1
  verdict_saving   "$state"

  hdr "结论"
  if (( rc_hit == 0 )); then
    ok "增量已经在生效。用 'watch' 子命令观察一段时间确认它持续工作。"
  elif (( rc_chain == 1 )); then
    bad "增量没有生效，且原因在链路更早的一环（见 ③）。按上面的提示逐项修。"
  else
    bad "增量没有生效。上面的 ③ 指出断点。"
  fi
  printf '\n提示：如果这是刚重启的服务，先发几个真实下游请求再跑本命令。\n'
  printf '     长期观察请用：%s watch\n\n' "$0"""
}

cmd_snapshot() {
  login
  local state
  state="$(fetch_state)" || die "采样失败"
  mkdir -p "$HISTORY_DIR"
  chmod 700 "$HISTORY_DIR" 2>/dev/null
  printf '%s\n' "$state" >> "$HISTORY_FILE"
  printf '%s %s\n' "$(jq -r '.ts' <<<"$state")" "$(jq -c '.delta' <<<"$state")"
}

cmd_watch() {
  login
  mkdir -p "$HISTORY_DIR"; chmod 700 "$HISTORY_DIR" 2>/dev/null

  hdr "Perch 增量持续观察 — $BASE_URL"
  info "间隔 ${INTERVAL}s，$([[ "$ROUNDS" == "0" ]] && echo "不限轮数（Ctrl-C 结束）" || echo "共 $ROUNDS 轮")"
  info "历史写入 $HISTORY_FILE"
  printf '\n%s%-19s %6s %6s %7s %7s %8s %10s%s\n' \
    "$C_BLD" "时间" "增量" "全量" "命中率" "被拒" "会话" "缓存快照" "$C_RST"

  local prev="" round=0
  while :; do
    ((round++))
    local state
    if ! state="$(fetch_state)"; then
      warn "第 $round 轮采样失败，${INTERVAL}s 后重试"
      sleep "$INTERVAL"; continue
    fi
    printf '%s\n' "$state" >> "$HISTORY_FILE"

    local d w rate refused tracked snaps ts
    d=$(jq -r '.delta.deltaRequests       // 0' <<<"$state")
    w=$(jq -r '.delta.wholeRequests       // 0' <<<"$state")
    rate=$(jq -r '.delta.hitRate          // 0' <<<"$state")
    refused=$(jq -r '.delta.refusedDeltas // 0' <<<"$state")
    tracked=$(jq -r '.delta.trackedConversations // 0' <<<"$state")
    snaps=$(jq -r '.delta.cachedSnapshots // 0' <<<"$state")
    ts=$(jq -r '.ts' <<<"$state" | cut -c12-19)

    local hit_pct; hit_pct=$(awk -v r="$rate" 'BEGIN{printf "%.1f", r*100}')
    # 每轮都打印累计值；相较上一轮有新增时，标出新增量，这样"只是快照"和
    # "确实在动" 一眼可分。
    local trend=""
    if [[ -n "$prev" ]]; then
      local pd pw pr
      pd=$(jq -r '.delta.deltaRequests       // 0' <<<"$prev")
      pw=$(jq -r '.delta.wholeRequests       // 0' <<<"$prev")
      pr=$(jq -r '.delta.refusedDeltas // 0' <<<"$prev")
      local dd=$((d-pd)) dw=$((w-pw)) dr=$((refused-pr))
      if (( dd > 0 || dw > 0 || dr > 0 )); then
        trend="   ${C_GRN}+${dd} 增量${C_RST} ${C_DIM}+${dw} 全量${C_RST}"
        (( dr > 0 )) && trend+=" ${C_YEL}+${dr} 被拒${C_RST}"
      fi
    fi

    printf '%-19s %6s %6s %6.1f%% %7s %8s %10s%s\n' \
      "$ts" "$d" "$w" "$hit_pct" "$refused" "$tracked" "$snaps" "$trend"

    prev="$state"
    [[ "$ROUNDS" != "0" && "$round" -ge "$ROUNDS" ]] && break
    sleep "$INTERVAL"
  done

  printf '\n'
  hdr "观察结束"
  local first last
  first="$(head -1 "$HISTORY_FILE" 2>/dev/null)"
  last="$(tail -1  "$HISTORY_FILE" 2>/dev/null)"
  if [[ -n "$first" && -n "$last" ]]; then
    python3 - "$HISTORY_FILE" <<'PY' 2>/dev/null || info "（python3 不可用，跳过趋势汇总）"
import json,sys
rows=[]
for line in open(sys.argv[1]):
    line=line.strip()
    if not line: continue
    try: rows.append(json.loads(line))
    except Exception: pass
if not rows:
    print("  历史为空。"); raise SystemExit
a,b=rows[0].get("delta") or {},rows[-1].get("delta") or {}
d1,d2=a.get("deltaRequests",0),b.get("deltaRequests",0)
w1,w2=a.get("wholeRequests",0),b.get("wholeRequests",0)
r1,r2=a.get("refusedDeltas",0),b.get("refusedDeltas",0)
tot=(d2+d1*0)+(w2+w1*0)
print(f"  区间内新增：增量 {d2-d1} 次，全量 {w2-w1} 次，被拒 {r2-r1} 次")
if (d2-d1)>0:
    print(f"  → 增量在工作中。")
elif (w2-w1)>0:
    print(f"  → 只有全量在增长：增量没有生效，用 doctor 看断在哪一环。")
else:
    print(f"  → 区间内没有新的模型调用，无法判断。请在有流量时再观察一次。")
PY
  fi
  printf '\n历史文件：%s（每行一个 JSON 采样）\n\n' "$HISTORY_FILE"
}

usage() {
  cat <<'EOF'
perch-delta-observe.sh — 观察 account-pool-balancer 有没有在发 held-context 增量

用法：
  perch-delta-observe.sh doctor              一次性诊断：链路断在哪一环（先跑这个）
  perch-delta-observe.sh snapshot            采一次样，追加到历史文件（适合 cron）
  perch-delta-observe.sh watch               持续采样并显示变化量

必需环境变量：
  ODB_ADMIN_PASSWORD    管理台密码（从容器环境取，别写进命令行）

可选环境变量：
  ODB_BASE_URL              默认 http://127.0.0.1:8790
  ODB_DELTA_INTERVAL        watch 间隔秒数，默认 60
  ODB_DELTA_ROUNDS          watch 轮数，默认 0（不限）
  ODB_DELTA_HISTORY_DIR     历史文件目录，默认 <脚本目录>/.perch-delta
  ODB_DELTA_LOGIN_ATTEMPTS  登录重试次数，默认 3

示例：

  # 1) 在 OVH 主机上，从容器里取密码后诊断
  export ODB_ADMIN_PASSWORD=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' \
      account-pool-balancer | sed -n 's/^ODB_ADMIN_PASSWORD=//p')
  ./perch-delta-observe.sh doctor

  # 2) 持续观察（Ctrl-C 结束）。想让 "之后" 更干净，先在管理台
  #    "Perch 设置 → Held-context 增量" 点一下计数器重置，或调用
  #    POST /api/perch/delta/reset（需会话 + X-CSRF-Token）。
  #    重置只清计数、不清缓存。
  ./perch-delta-observe.sh watch

  # 3) 每 5 分钟采一次样，长期积累（cron）
  */5 * * * * ODB_ADMIN_PASSWORD="$(cat /root/.odb-admin-pw)" \
      /opt/odb/perch-delta-observe.sh snapshot >> /var/log/perch-delta.log 2>&1

退出码：0 = 诊断通过 / 采样成功；1 = 发现问题或执行失败。
EOF
}

case "${1:-doctor}" in
  doctor)   cmd_doctor ;;
  snapshot) cmd_snapshot ;;
  watch)    cmd_watch ;;
  -h|--help|help) usage ;;
  *) die "未知子命令 '$1'（可用：doctor / snapshot / watch / help）" ;;
esac
