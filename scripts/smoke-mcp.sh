#!/usr/bin/env bash
set -Eeuo pipefail
BASE="${CYCOM_BASE_URL:-http://127.0.0.1:7331}"
PROTO="2026-07-28"

post() {
  local method="$1" name="${2:-}" body="$3"
  local headers=(-H 'Content-Type: application/json' -H "MCP-Protocol-Version: ${PROTO}" -H "Mcp-Method: ${method}")
  [[ -n "$name" ]] && headers+=(-H "Mcp-Name: ${name}")
  curl -fsS "${headers[@]}" --data "$body" "$BASE/mcp"
}

meta='"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"cycom-smoke","version":"1"}}'

echo '[1/9] readyz'
curl -fsS "$BASE/readyz" | tee /tmp/cycom-smoke-ready.json; echo
grep -q '"status":"ready"' /tmp/cycom-smoke-ready.json
grep -Eq '"tools":4[2-9]|"tools":[5-9][0-9]' /tmp/cycom-smoke-ready.json

echo '[2/9] server/discover'
post server/discover '' "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"server/discover\",\"params\":{${meta}}}" | tee /tmp/cycom-smoke-discover.json; echo
grep -q '2026-07-28' /tmp/cycom-smoke-discover.json

echo '[3/9] tools/list'
post tools/list '' "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\",\"params\":{${meta}}}" | tee /tmp/cycom-smoke-tools.json >/dev/null
grep -q 'target_exec' /tmp/cycom-smoke-tools.json
grep -q 'state_put' /tmp/cycom-smoke-tools.json
grep -q 'audit_tail' /tmp/cycom-smoke-tools.json
echo 'tool catalog contains full-power primitives'

echo '[4/9] process_exec with stdin'
post tools/call process_exec "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"process_exec\",\"arguments\":{\"command\":\"cat\",\"stdin\":\"cycom-stdin-ok\"},${meta}}}" | tee /tmp/cycom-smoke-exec.json; echo
grep -q 'cycom-stdin-ok' /tmp/cycom-smoke-exec.json

echo '[5/9] target_exec local stateless route'
post tools/call target_exec "{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"tools/call\",\"params\":{\"name\":\"target_exec\",\"arguments\":{\"name\":\"local\",\"command\":\"printf cycom-target-ok\"},${meta}}}" | tee /tmp/cycom-smoke-target.json; echo
grep -q 'cycom-target-ok' /tmp/cycom-smoke-target.json

echo '[6/9] durable explicit state round trip'
post tools/call state_put "{\"jsonrpc\":\"2.0\",\"id\":5,\"method\":\"tools/call\",\"params\":{\"name\":\"state_put\",\"arguments\":{\"namespace\":\"smoke\",\"key\":\"roundtrip\",\"value\":{\"ok\":true}},${meta}}}" >/dev/null
post tools/call state_get "{\"jsonrpc\":\"2.0\",\"id\":6,\"method\":\"tools/call\",\"params\":{\"name\":\"state_get\",\"arguments\":{\"namespace\":\"smoke\",\"key\":\"roundtrip\"},${meta}}}" | tee /tmp/cycom-smoke-state.json; echo
grep -q '"ok":true' /tmp/cycom-smoke-state.json

echo '[7/9] policy and audit'
post tools/call policy_get "{\"jsonrpc\":\"2.0\",\"id\":7,\"method\":\"tools/call\",\"params\":{\"name\":\"policy_get\",\"arguments\":{},${meta}}}" | tee /tmp/cycom-smoke-policy.json >/dev/null
grep -q '"mode":"full"' /tmp/cycom-smoke-policy.json
post tools/call audit_tail "{\"jsonrpc\":\"2.0\",\"id\":8,\"method\":\"tools/call\",\"params\":{\"name\":\"audit_tail\",\"arguments\":{\"limit\":20},${meta}}}" | tee /tmp/cycom-smoke-audit.json >/dev/null
grep -q 'process_exec' /tmp/cycom-smoke-audit.json
echo 'policy/audit ok'

echo '[8/9] hostile browser Origin blocked'
code="$(curl -sS -o /tmp/cycom-origin.out -w '%{http_code}' -H 'Origin: https://evil.example' "$BASE/mcp")"
[[ "$code" == 403 ]]
echo 'origin guard ok'

echo '[9/9] legacy GET/SSE probe compatibility'
headers="$(curl -sS -N --max-time 1 -D - "$BASE/mcp" 2>/dev/null | head -n 12 || true)"
grep -qi '200 OK' <<<"$headers"
grep -qi 'text/event-stream' <<<"$headers"
echo "$headers"
echo 'FULL-POWER SMOKE PASS'
