#!/usr/bin/env bash
set -u
BASE="${CYCOM_BASE_URL:-http://127.0.0.1:7331}"
USER_NAME="${1:-${SUDO_USER:-$USER}}"
fail=0
check(){ local label="$1";shift;if "$@" >/dev/null 2>&1;then printf 'PASS  %s\n' "$label";else printf 'FAIL  %s\n' "$label";fail=1;fi;}
check 'runtime binary' command -v cycomagent
check 'root broker binary' command -v cycomagent-root
check 'runtime service active' systemctl is-active "cycomagent@${USER_NAME}.service"
check 'runtime live' curl -fsS --max-time 3 "$BASE/livez"
check 'runtime ready' curl -fsS --max-time 3 "$BASE/readyz"
printf '\nHealth:\n';curl -sS --max-time 3 "$BASE/health" || true;echo
printf '\nTunnel:\n';systemctl --no-pager --full status "cycomagent-tunnel@${USER_NAME}.service" 2>/dev/null | sed -n '1,18p' || echo 'not installed/enabled'
printf '\nRecent runtime logs:\n';journalctl -u "cycomagent@${USER_NAME}.service" -n 20 --no-pager 2>/dev/null || true
exit "$fail"
