#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${CYCOM_BIN:-$ROOT/dist/cycomagent}"
ADDR="${CYCOM_RECOVERY_ADDR:-127.0.0.1:17334}"
BASE="http://${ADDR}"
PROTO="2026-07-28"
TMP="$(mktemp -d)"
STATE="$TMP/state"
PID=""

cleanup() {
  [[ -n "${PID:-}" ]] && kill "$PID" 2>/dev/null || true
  [[ -n "${PID:-}" ]] && wait "$PID" 2>/dev/null || true
  rm -rf "$TMP"
}
trap cleanup EXIT

start_runtime() {
  "$BIN" \
    --state-dir "$STATE" \
    --plugin-dir "$TMP/plugins" \
    --root-socket "$TMP/root.sock" \
    --addr "$ADDR" >>"$TMP/runtime.log" 2>&1 &
  PID=$!
  for _ in $(seq 1 100); do
    curl -fsS --max-time 1 "$BASE/readyz" >/dev/null 2>&1 && return 0
    sleep .05
  done
  echo 'runtime failed to become ready' >&2
  cat "$TMP/runtime.log" >&2 || true
  return 1
}

call_tool() {
  local name="$1" args="$2" id="$3"
  curl -fsS \
    -H 'Content-Type: application/json' \
    -H "MCP-Protocol-Version: $PROTO" \
    -H 'Mcp-Method: tools/call' \
    -H "Mcp-Name: $name" \
    --data "{\"jsonrpc\":\"2.0\",\"id\":$id,\"method\":\"tools/call\",\"params\":{\"name\":\"$name\",\"arguments\":$args,\"_meta\":{\"io.modelcontextprotocol/protocolVersion\":\"$PROTO\",\"io.modelcontextprotocol/clientCapabilities\":{}}}}" \
    "$BASE/mcp"
}

start_runtime
SPAWN="$(call_tool process_spawn '{"command":"sleep 2; printf recovered-done"}' 1)"
JOB_ID="$(python3 -c 'import json,sys; print(json.loads(sys.stdin.read())["result"]["structuredContent"]["id"])' <<<"$SPAWN")"
echo "spawned $JOB_ID; killing runtime pid=$PID while job stays alive"
kill -KILL "$PID"
wait "$PID" 2>/dev/null || true
PID=""
sleep .15

start_runtime
echo "runtime restarted pid=$PID; waiting for recovered job"
sleep 2.5
GET="$(call_tool job_get "{\"id\":\"$JOB_ID\"}" 2)"
TAIL="$(call_tool job_tail "{\"id\":\"$JOB_ID\",\"lines\":20}" 3)"

python3 - "$GET" "$TAIL" <<'PY'
import json, sys
job = json.loads(sys.argv[1])["result"]["structuredContent"]
tail = json.loads(sys.argv[2])["result"]["structuredContent"]
assert job["status"] == "exited-after-recovery", job
assert "recovered-done" in tail.get("log", ""), tail
print("RECOVERY PASS")
print("job_status=", job["status"], "exit_code=", job["exit_code"], "log=", tail["log"])
PY
