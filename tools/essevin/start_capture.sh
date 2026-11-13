#!/bin/bash
# Start mitmproxy + the driven Chrome window for the essevin registration capture.
#
#   ./start_capture.sh          start mitmproxy (:8899) and open Chrome through it
#   ./start_capture.sh stop     stop mitmproxy, keep the captured data
#
# Chrome uses a dedicated --user-data-dir so the capture is not polluted by an
# existing profile and so the mitm root is trusted for this run only.
set -u

HERE="$(cd "$(dirname "$0")" && pwd)"
PY="$HERE/../../_tmp_fingerprint/.venv/bin/mitmdump"
[ -x "$PY" ] || PY="$(command -v mitmdump 2>/dev/null)"
[ -n "$PY" ] && [ -x "$PY" ] || { echo "[FAIL] mitmdump not found"; exit 1; }
DIR="$HERE/run"
PORT=8899
UPSTREAM="${UPSTREAM:-}"
CHROME="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
PROFILE="$HERE/chrome-profile"

if [ "${1:-start}" = "stop" ]; then
  pkill -f "mitmdump .* -p $PORT" 2>/dev/null && echo "[OK] mitmproxy stopped" || echo "[INFO] mitmproxy not running"
  echo "[INFO] capture kept at $DIR/raw.jsonl"
  exit 0
fi

mkdir -p "$DIR" "$PROFILE"
[ "$(wc -c < "$DIR/raw.jsonl" 2>/dev/null || echo 0)" -eq 0 ] && : > "$DIR/raw.jsonl"

pkill -f "mitmdump .* -p $PORT" 2>/dev/null
sleep 0.5

ARGS=(--set confdir="$HERE/mitm-conf" --set http2_zstd=false -p "$PORT" --listen-host 127.0.0.1 -s "$HERE/capture_addon.py" -q)
[ -n "$UPSTREAM" ] && ARGS+=(--mode "upstream:$UPSTREAM")

INJECT_FP="${INJECT_FP:-0}" CAPTURE_DIR="$DIR" nohup "$PY" "${ARGS[@]}" > "$DIR/mitm.log" 2>&1 &
disown 2>/dev/null || true

for i in $(seq 1 20); do
  lsof -nP -iTCP:$PORT -sTCP:LISTEN >/dev/null 2>&1 && break
  sleep 0.5
done
lsof -nP -iTCP:$PORT -sTCP:LISTEN >/dev/null 2>&1 || { echo "[FAIL] mitmproxy did not listen"; tail -5 "$DIR/mitm.log"; exit 1; }
echo "[OK] mitmproxy on 127.0.0.1:$PORT (${UPSTREAM:-direct} upstream) -> $DIR/raw.jsonl"

# Fresh profile: trust the mitm root, keep everything else default.
CERT="$HERE/mitm-conf/mitmproxy-ca-cert.pem"
if [ ! -f "$CERT" ]; then
  sleep 1
fi

"$CHROME" \
  --user-data-dir="$PROFILE" \
  --proxy-server="http://127.0.0.1:$PORT" \
  --proxy-bypass-list="<-loopback>" \
  --use-mock-keychain \
  --no-first-run \
  --no-default-browser-check \
  --no-sandbox \
  --ignore-certificate-errors \
  --remote-debugging-port=9333 \
  "https://essevin.com/" \
  > "$DIR/chrome.log" 2>&1 &
disown 2>/dev/null || true
echo "[OK] Chrome launched (CDP on 127.0.0.1:9333, profile $PROFILE)"
