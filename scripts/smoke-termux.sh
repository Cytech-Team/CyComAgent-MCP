#!/data/data/com.termux/files/usr/bin/sh
set -eu

if [ -z "${PREFIX:-}" ] || [ ! -d "$PREFIX" ]; then
  echo "This smoke test must run inside Termux." >&2
  exit 2
fi

BIN=${CYCOM_BIN:-$PREFIX/bin/cycomagent}
command -v curl >/dev/null 2>&1 || { echo "curl is required" >&2; exit 2; }
[ -x "$BIN" ] || { echo "CyComAgent binary not found: $BIN" >&2; exit 2; }

STATE=${CYCOM_STATE_DIR:-$HOME/.local/state/cycomagent-smoke}
PORT=${CYCOM_SMOKE_PORT:-17331}
LOG="$TMPDIR/cycomagent-smoke.log"
rm -rf "$STATE"
mkdir -p "$STATE"

"$BIN" --addr "127.0.0.1:$PORT" --state-dir "$STATE" >"$LOG" 2>&1 &
PID=$!
cleanup() { kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; rm -rf "$STATE"; }
trap cleanup EXIT INT TERM

n=0
while [ $n -lt 50 ]; do
  if curl -fsS "http://127.0.0.1:$PORT/readyz" >/dev/null 2>&1; then break; fi
  n=$((n+1)); sleep 0.1
done

BASE="http://127.0.0.1:$PORT"
PROTO="2026-07-28"
META='"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"termux-smoke","version":"1"}}'
post() {
  method="$1"; name="$2"; body="$3"
  if [ -n "$name" ]; then
    curl -fsS -H 'Content-Type: application/json' -H "MCP-Protocol-Version: $PROTO" -H "Mcp-Method: $method" -H "Mcp-Name: $name" --data "$body" "$BASE/mcp"
  else
    curl -fsS -H 'Content-Type: application/json' -H "MCP-Protocol-Version: $PROTO" -H "Mcp-Method: $method" --data "$body" "$BASE/mcp"
  fi
}

echo '[1/5] health/profile'
curl -fsS "$BASE/health" | tee "$TMPDIR/cycomagent-termux-health.json"
grep -q '"runtime_profile":"mobile_assistant"' "$TMPDIR/cycomagent-termux-health.json"
grep -q '"android_device_root_support":"not_supported"' "$TMPDIR/cycomagent-termux-health.json"

echo '[2/5] assistant tool catalog'
post tools/list '' "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\",\"params\":{${META}}}" > "$TMPDIR/cycomagent-termux-tools.json"
for tool in assistant_device_status assistant_listen assistant_speak assistant_notify assistant_location assistant_sms_send android_api_list; do
  grep -q "\"$tool\"" "$TMPDIR/cycomagent-termux-tools.json"
done

echo '[3/5] local shell works without /bin/sh assumption'
post tools/call process_exec "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"process_exec\",\"arguments\":{\"command\":\"printf termux-shell-ok\"},${META}}}" > "$TMPDIR/cycomagent-termux-exec.json"
grep -q 'termux-shell-ok' "$TMPDIR/cycomagent-termux-exec.json"

echo '[4/5] Android device-root path is blocked'
post tools/call process_exec "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"process_exec\",\"arguments\":{\"command\":\"id\",\"privileged\":true},${META}}}" > "$TMPDIR/cycomagent-termux-root.json" || true
grep -q 'device-root escalation is not supported' "$TMPDIR/cycomagent-termux-root.json"

echo '[5/5] sensitive Android defaults to opt-in'
post tools/call assistant_sms_send "{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"tools/call\",\"params\":{\"name\":\"assistant_sms_send\",\"arguments\":{\"numbers\":[\"000\"],\"text\":\"smoke\"},${META}}}" > "$TMPDIR/cycomagent-termux-sensitive.json" || true
grep -q 'allow_sensitive_android=true' "$TMPDIR/cycomagent-termux-sensitive.json"

printf '%s\n' "Termux mobile_assistant smoke test passed." "Log: $LOG"
