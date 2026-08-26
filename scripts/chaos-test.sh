#!/usr/bin/env bash
set -Eeuo pipefail
USER_NAME="${1:-${SUDO_USER:-$USER}}"
RUNTIME="cycomagent@${USER_NAME}.service"
TUNNEL="cycomagent-tunnel@${USER_NAME}.service"
BASE="${CYCOM_BASE_URL:-http://127.0.0.1:7331}"
ROUNDS="${ROUNDS:-5}"

if [[ $EUID -ne 0 ]]; then exec sudo -E "$0" "$USER_NAME"; fi

wait_ready(){ for _ in $(seq 1 80);do curl -fsS --max-time 2 "$BASE/readyz" >/dev/null 2>&1&&return 0;sleep .25;done;return 1; }
wait_active(){ for _ in $(seq 1 80);do systemctl is-active --quiet "$1"&&return 0;sleep .25;done;return 1; }

printf 'Initial readiness... '; wait_ready; echo PASS

for n in $(seq 1 "$ROUNDS"); do
  pid="$(systemctl show -p MainPID --value "$RUNTIME")"
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] || { echo "invalid runtime pid: $pid"; exit 1; }
  echo "[$n/$ROUNDS] SIGKILL runtime pid=$pid"
  kill -KILL "$pid"
  wait_active "$RUNTIME"
  wait_ready
  echo "[$n/$ROUNDS] runtime recovered"
done

if systemctl is-enabled --quiet "$TUNNEL" 2>/dev/null; then
  echo 'Killing tunnel once; runtime must remain ready.'
  tpid="$(systemctl show -p MainPID --value "$TUNNEL")"
  [[ "$tpid" =~ ^[1-9][0-9]*$ ]] && kill -KILL "$tpid"
  wait_ready
  wait_active "$TUNNEL"
  echo 'Tunnel recovered while runtime stayed healthy.'
else
  echo 'Tunnel service not enabled; skipping tunnel kill test.'
fi

echo 'Running MCP smoke after chaos...'
CYCOM_BASE_URL="$BASE" /usr/local/lib/cycomagent/smoke-mcp.sh 2>/dev/null || \
  "$(cd "$(dirname "$0")" && pwd)/smoke-mcp.sh"

echo 'CHAOS PASS'
