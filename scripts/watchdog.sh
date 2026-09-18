#!/usr/bin/env bash
set -u
USER_NAME="${1:?usage: watchdog.sh USER}"
RUNTIME="cycomagent@${USER_NAME}.service"
TUNNEL_SYSTEM="cycomagent-tunnel@${USER_NAME}.service"
TUNNEL_USER="cycomagent-tunnel.service"
BASE="/var/lib/cycomagent-${USER_NAME}"
STATE="$BASE/self-heal"
LOG="$STATE/events.jsonl"
LOCK="/run/cycomagent-${USER_NAME}/watchdog.lock"
RAM_SOFT=${CYCOM_HEAL_RAM_SOFT:-2500000000}
RAM_HARD=${CYCOM_HEAL_RAM_HARD:-2900000000}
mkdir -p "$STATE"
exec 9>"$LOCK"; flock -n 9 || exit 0
probe(){ curl -fsS --max-time 3 "$1" >/dev/null 2>&1; }
event(){ printf '{"time":"%s","component":"%s","action":"%s","reason":"%s"}\n' "$(date -u +%FT%TZ)" "$1" "$2" "${3//\"/\\\"}" >>"$LOG"; logger -t cycomagent-selfheal "$1: $2: $3"; }
mem_current(){ systemctl show "$RUNTIME" -p MemoryCurrent --value 2>/dev/null | grep -E '^[0-9]+$' || echo 0; }
recheck_env(){
  local bad=0
  probe http://127.0.0.1:7331/livez || bad=1
  probe http://127.0.0.1:7331/readyz || bad=1
  ss -ltn 'sport = :7331' 2>/dev/null | grep -q LISTEN || bad=1
  [[ -d "$BASE" && -w "$BASE" ]] || bad=1
  [[ -S "/run/cycomagent-${USER_NAME}/root.sock" ]] || bad=1
  return "$bad"
}
kill_heavy_children(){
  local main pid rss comm killed=0
  main=$(systemctl show "$RUNTIME" -p MainPID --value 2>/dev/null || echo 0)
  while read -r pid rss comm; do
    [[ "$pid" =~ ^[0-9]+$ && "$rss" =~ ^[0-9]+$ ]] || continue
    [[ "$pid" == "$main" ]] && continue
    # only children in the runtime cgroup; >=384 MiB RSS
    if (( rss >= 393216 )); then
      event memory term "heavy child pid=$pid rss_kib=$rss cmd=$comm"
      kill -TERM "$pid" 2>/dev/null || true; killed=1
    fi
  done < <(systemctl status "$RUNTIME" --no-pager 2>/dev/null | sed -n 's/.*├─\? *\([0-9][0-9]*\) \(.*\)$/\1/p' | while read -r pid rest; do rss=$(awk '/VmRSS:/{print $2}' /proc/$pid/status 2>/dev/null || echo 0); printf '%s %s %s\n' "$pid" "$rss" "$rest"; done)
  (( killed )) && sleep 3
  return 0
}
restart_runtime(){
  event runtime restart "$1"; systemctl restart "$RUNTIME" || { event runtime failed "restart command failed"; return 1; }
  for _ in {1..20}; do sleep 1; recheck_env && { event runtime recovered "$1"; return 0; }; done
  event runtime failed "environment unhealthy after restart"; return 1
}
# Phase 1: RAM pressure -> kill only heavy child processes -> recheck environment.
mem=$(mem_current)
if (( mem >= RAM_SOFT )); then
  event memory pressure "MemoryCurrent=$mem soft=$RAM_SOFT hard=$RAM_HARD"
  kill_heavy_children
  mem=$(mem_current)
  if recheck_env && (( mem < RAM_SOFT )); then
    event memory recovered "child cleanup successful MemoryCurrent=$mem"
  elif (( mem >= RAM_HARD )); then
    restart_runtime "RAM remained above hard limit after child cleanup" || true
  elif ! recheck_env; then
    restart_runtime "environment failed recheck after RAM cleanup" || true
  fi
fi
# Phase 2: runtime health independent of RAM.
if ! systemctl is-active --quiet "$RUNTIME"; then restart_runtime "service inactive" || true
elif ! recheck_env; then restart_runtime "runtime environment check failed" || true
fi
# Phase 3: tunnel health. Clean only a stale tunnel-client; never unrelated port owners.
heal_tunnel(){
  local scope="$1" unit="$2" owner pid unitpid
  local ctl=(systemctl); [[ "$scope" == user ]] && ctl=(runuser -u "$USER_NAME" -- systemctl --user)
  "${ctl[@]}" is-enabled "$unit" >/dev/null 2>&1 || return 0
  if "${ctl[@]}" is-active --quiet "$unit" && probe http://127.0.0.1:7332/; then return 0; fi
  owner=$(ss -ltnp 'sport = :7332' 2>/dev/null | sed -n 's/.*users:(("\([^"]*\)".*/\1/p' | head -1)
  if [[ -n "$owner" && "$owner" != tunnel-client ]]; then event tunnel blocked "7332 owned by $owner; not killing"; return 0; fi
  if [[ "$owner" == tunnel-client ]]; then
    pid=$(ss -ltnp 'sport = :7332' 2>/dev/null | sed -n 's/.*pid=\([0-9]*\).*/\1/p' | head -1)
    unitpid=$("${ctl[@]}" show "$unit" -p MainPID --value 2>/dev/null || echo 0)
    if [[ -n "$pid" && "$pid" != "$unitpid" ]]; then event tunnel cleanup "stale pid=$pid"; kill -TERM "$pid" 2>/dev/null || true; sleep 1; fi
  fi
  event tunnel restart "$scope tunnel unhealthy"; "${ctl[@]}" restart "$unit" || { event tunnel failed "restart failed"; return 0; }
  sleep 2; probe http://127.0.0.1:7332/ && event tunnel recovered "health restored" || event tunnel failed "health still unavailable"
}
if systemctl is-enabled "$TUNNEL_SYSTEM" >/dev/null 2>&1; then heal_tunnel system "$TUNNEL_SYSTEM"; else heal_tunnel user "$TUNNEL_USER"; fi
[[ -f "$LOG" ]] && tail -n 1000 "$LOG" >"$LOG.tmp" && mv "$LOG.tmp" "$LOG"
