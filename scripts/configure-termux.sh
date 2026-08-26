#!/data/data/com.termux/files/usr/bin/sh
set -eu

: "${PREFIX:=/data/data/com.termux/files/usr}"
: "${HOME:?HOME is required}"

STATE="$HOME/.local/state/cycomagent"
CONFIG="$HOME/.config/cycomagent"
SERVICE_ROOT="$PREFIX/var/service"
TUNNEL_SERVICE="$SERVICE_ROOT/cycomagent-tunnel"
POLICY_FILE="$STATE/policy.json"
ID_FILE="$CONFIG/tunnel-id"
KEY_FILE="$CONFIG/control-plane-api-key"

mkdir -p "$STATE" "$CONFIG"
chmod 700 "$STATE" "$CONFIG"

TTY=""
if [ -r /dev/tty ] && [ -w /dev/tty ]; then
  TTY=/dev/tty
fi

say() { printf '%s\n' "$*"; }
fail() { printf 'cycomagent-setup: %s\n' "$*" >&2; exit 1; }

ask_line() {
  prompt=$1
  default=${2:-}
  if [ -n "$TTY" ]; then
    if [ -n "$default" ]; then
      printf '%s [%s]: ' "$prompt" "$default" > "$TTY"
    else
      printf '%s: ' "$prompt" > "$TTY"
    fi
    IFS= read -r ANSWER < "$TTY" || ANSWER=""
  else
    ANSWER=""
  fi
  [ -n "$ANSWER" ] || ANSWER=$default
}

ask_yes() {
  prompt=$1
  default=${2:-y}
  ask_line "$prompt (y/n)" "$default"
  case "$ANSWER" in
    y|Y|yes|YES) ANSWER=yes ;;
    *) ANSWER=no ;;
  esac
}

ask_secret() {
  prompt=$1
  if [ -z "$TTY" ]; then
    ANSWER=""
    return
  fi
  printf '%s: ' "$prompt" > "$TTY"
  if stty -echo < "$TTY" 2>/dev/null; then
    IFS= read -r ANSWER < "$TTY" || ANSWER=""
    stty echo < "$TTY" 2>/dev/null || true
    printf '\n' > "$TTY"
  else
    IFS= read -r ANSWER < "$TTY" || ANSWER=""
  fi
}

json_bool() {
  [ "$1" = "yes" ] && printf true || printf false
}

write_policy() {
  mode=$1
  allow_json=$2
  sensitive=$3
  remote=$4
  external=$5
  timeout=$6
  cat > "$POLICY_FILE" <<EOF_POLICY
{
  "mode": "$mode",
  "allow_tools": [$allow_json],
  "deny_tools": [],
  "allow_privileged": false,
  "allow_remote_targets": $remote,
  "allow_external_plugins": $external,
  "allow_sensitive_android": $sensitive,
  "max_exec_timeout_seconds": $timeout
}
EOF_POLICY
  chmod 600 "$POLICY_FILE"
}

BASE='"capabilities_list","policy_get","policy_reload","audit_tail"'
ASSISTANT_TOOLS="$BASE,"'"system_info","system_env","network_request","network_resolve","network_tcp","android_api_list","assistant_device_status","assistant_listen","assistant_speak","assistant_notify","assistant_location","assistant_clipboard_get","assistant_clipboard_set","assistant_vibrate","assistant_torch"'
DEVELOPER_TOOLS="$ASSISTANT_TOOLS,"'"fs_list","fs_read","fs_search","fs_stat","fs_write","fs_move","fs_patch","process_exec","process_inspect","process_spawn","job_get","job_list","job_tail","state_get","state_list","state_put","state_delete"'

PRESET=${CYCOM_PRESET:-}
if [ -z "$PRESET" ]; then
  [ -n "$TTY" ] || fail "interactive terminal unavailable; set CYCOM_PRESET and tunnel environment variables"
  cat > "$TTY" <<'EOF_MENU'

CyComAgent permission preset
  1) Assistant   - phone assistant basics + network/status; no shell/files/sensitive APIs
  2) Developer   - Assistant + files + local shell/jobs; sensitive Android APIs OFF
  3) Power       - all tools; sensitive Android APIs OFF
  4) Everything  - all tools including SMS/calls/camera/mic/contacts
  5) Read-only   - inspect/read only
  6) Custom      - choose capability groups
EOF_MENU
  ask_line "Select preset" "2"
  PRESET=$ANSWER
fi

case "$PRESET" in
  1|assistant|Assistant)
    PRESET=assistant
    write_policy full "$ASSISTANT_TOOLS" false false false 120
    ;;
  2|developer|Developer|balanced|Balanced)
    PRESET=developer
    write_policy full "$DEVELOPER_TOOLS" false false false 900
    ;;
  3|power|Power)
    PRESET=power
    write_policy full "" false true true 3600
    ;;
  4|everything|Everything|full-sensitive)
    PRESET=everything
    write_policy full "" true true true 3600
    ;;
  5|readonly|read-only|Read-only)
    PRESET=readonly
    write_policy readonly "" false false false 120
    ;;
  6|custom|Custom)
    PRESET=custom
    [ -n "$TTY" ] || fail "custom preset requires an interactive terminal"
    ALLOW="$BASE"
    REMOTE=false
    SENSITIVE=false
    EXTERNAL=false
    MAX_TIMEOUT=300

    ask_yes "Allow system/network inspection" y
    [ "$ANSWER" = yes ] && ALLOW="$ALLOW,"'"system_info","system_env","network_request","network_resolve","network_tcp"'

    ask_yes "Allow basic Android assistant tools (voice, notify, location, clipboard, torch)" y
    [ "$ANSWER" = yes ] && ALLOW="$ALLOW,"'"android_api_list","assistant_device_status","assistant_listen","assistant_speak","assistant_notify","assistant_location","assistant_clipboard_get","assistant_clipboard_set","assistant_vibrate","assistant_torch"'

    ask_yes "Allow filesystem READ" y
    [ "$ANSWER" = yes ] && ALLOW="$ALLOW,"'"fs_list","fs_read","fs_search","fs_stat"'

    ask_yes "Allow filesystem WRITE/PATCH/MOVE" n
    [ "$ANSWER" = yes ] && ALLOW="$ALLOW,"'"fs_write","fs_patch","fs_move"'

    ask_yes "Allow filesystem DELETE" n
    [ "$ANSWER" = yes ] && ALLOW="$ALLOW,"'"fs_remove"'

    ask_yes "Allow local shell/process execution" n
    [ "$ANSWER" = yes ] && ALLOW="$ALLOW,"'"process_exec","process_inspect","process_spawn","job_get","job_list","job_tail"'

    ask_yes "Allow process/job signals and service control" n
    [ "$ANSWER" = yes ] && ALLOW="$ALLOW,"'"process_signal","job_signal","job_prune","service_control"'

    ask_yes "Allow persistent state tools" y
    [ "$ANSWER" = yes ] && ALLOW="$ALLOW,"'"state_get","state_list","state_put","state_delete"'

    ask_yes "Allow remote SSH target tools" n
    if [ "$ANSWER" = yes ]; then
      ALLOW="$ALLOW,"'"target_*"'
      REMOTE=true
    fi

    ask_yes "Allow raw allowlisted Termux:API calls" n
    [ "$ANSWER" = yes ] && ALLOW="$ALLOW,"'"android_api_call"'

    ask_yes "Allow SENSITIVE Android APIs (SMS/calls/camera/mic/contacts/call logs)" n
    if [ "$ANSWER" = yes ]; then
      ALLOW="$ALLOW,"'"assistant_sms_send","assistant_phone_call","assistant_camera_photo","assistant_microphone_record","assistant_contacts","assistant_sms_list","assistant_call_log"'
      SENSITIVE=true
    fi

    write_policy full "$ALLOW" "$SENSITIVE" "$REMOTE" "$EXTERNAL" "$MAX_TIMEOUT"
    ;;
  *)
    fail "unknown preset: $PRESET"
    ;;
esac

say "Policy preset: $PRESET"
say "Policy file: $POLICY_FILE"

# Configure or reuse the secure MCP tunnel. Credentials are stored in separate
# mode-0600 files so they never need to appear in the runit command line.
SKIP_TUNNEL=${CYCOM_SKIP_TUNNEL:-0}
TUNNEL_ID=${CYCOM_TUNNEL_ID:-}
TUNNEL_KEY=${CYCOM_TUNNEL_API_KEY:-}

if [ "$SKIP_TUNNEL" != "1" ] && [ -z "$TUNNEL_ID" ] && [ -z "$TUNNEL_KEY" ] && [ -s "$ID_FILE" ] && [ -s "$KEY_FILE" ]; then
  if [ -n "$TTY" ]; then
    ask_yes "Existing tunnel credentials found. Reuse them" y
    if [ "$ANSWER" = yes ]; then
      TUNNEL_ID=$(cat "$ID_FILE")
      TUNNEL_KEY=$(cat "$KEY_FILE")
    fi
  else
    TUNNEL_ID=$(cat "$ID_FILE")
    TUNNEL_KEY=$(cat "$KEY_FILE")
  fi
fi

if [ "$SKIP_TUNNEL" != "1" ] && { [ -z "$TUNNEL_ID" ] || [ -z "$TUNNEL_KEY" ]; }; then
  if [ -z "$TTY" ]; then
    fail "set CYCOM_TUNNEL_ID and CYCOM_TUNNEL_API_KEY, or CYCOM_SKIP_TUNNEL=1"
  fi
  while [ -z "$TUNNEL_ID" ]; do
    ask_line "OpenAI Tunnel ID (tunnel_...) or type skip" ""
    TUNNEL_ID=$ANSWER
    if [ "$TUNNEL_ID" = skip ] || [ "$TUNNEL_ID" = SKIP ]; then
      SKIP_TUNNEL=1
      TUNNEL_ID=""
      break
    fi
    case "$TUNNEL_ID" in
      tunnel_*) ;;
      *) say "Tunnel ID should start with tunnel_."; TUNNEL_ID="" ;;
    esac
  done

  if [ "$SKIP_TUNNEL" != "1" ] && [ -z "$TUNNEL_KEY" ]; then
    while [ -z "$TUNNEL_KEY" ]; do
      ask_secret "OpenAI Tunnel runtime API key (hidden)"
      TUNNEL_KEY=$ANSWER
      [ -n "$TUNNEL_KEY" ] || say "API key cannot be empty."
    done
  fi
fi

if [ "$SKIP_TUNNEL" = "1" ]; then
  : > "$TUNNEL_SERVICE/down" 2>/dev/null || true
  if command -v sv >/dev/null 2>&1; then
    sv down cycomagent-tunnel >/dev/null 2>&1 || true
  fi
  say "Tunnel: skipped (local MCP only)"
else
  printf '%s' "$TUNNEL_ID" > "$ID_FILE"
  printf '%s' "$TUNNEL_KEY" > "$KEY_FILE"
  chmod 600 "$ID_FILE" "$KEY_FILE"
  rm -f "$TUNNEL_SERVICE/down" 2>/dev/null || true
  say "Tunnel credentials saved securely (mode 0600)."
fi

# Initialize termux-services in the current shell; no Termux restart required.
if [ -f "$PREFIX/etc/profile.d/start-services.sh" ]; then
  # shellcheck disable=SC1090
  . "$PREFIX/etc/profile.d/start-services.sh" || true
fi
export SVDIR="${SVDIR:-$SERVICE_ROOT}"
export LOGDIR="${LOGDIR:-$PREFIX/var/log}"
if command -v service-daemon >/dev/null 2>&1; then
  service-daemon start >/dev/null 2>&1 || true
fi

if command -v sv >/dev/null 2>&1; then
  sv up cycomagent >/dev/null 2>&1 || true
  sv restart cycomagent >/dev/null 2>&1 || sv up cycomagent >/dev/null 2>&1 || true
  if [ "$SKIP_TUNNEL" != "1" ] && [ -x "$PREFIX/bin/tunnel-client" ]; then
    command -v sv-enable >/dev/null 2>&1 && sv-enable cycomagent-tunnel >/dev/null 2>&1 || true
    # Credentials are read only when the tunnel process starts. Restart so
    # changing Tunnel ID/API key in this setup takes effect immediately.
    sv restart cycomagent-tunnel >/dev/null 2>&1 || sv up cycomagent-tunnel >/dev/null 2>&1 || true
  fi
fi

# Best-effort local health checks.
AGENT_OK=0
n=0
while [ "$n" -lt 20 ]; do
  if command -v curl >/dev/null 2>&1 && curl -fsS --max-time 2 http://127.0.0.1:7331/health >/dev/null 2>&1; then
    AGENT_OK=1
    break
  fi
  n=$((n + 1))
  sleep 1
done

if [ "$AGENT_OK" -eq 1 ]; then
  say "CyComAgent health: OK"
else
  say "CyComAgent health: not ready yet (check: sv status cycomagent)"
fi

if [ "$SKIP_TUNNEL" != "1" ]; then
  TUNNEL_LIVE=0
  TUNNEL_READY=0
  n=0
  while [ "$n" -lt 20 ]; do
    if command -v curl >/dev/null 2>&1 && curl -fsS --max-time 2 http://127.0.0.1:7332/healthz >/dev/null 2>&1; then
      TUNNEL_LIVE=1
      if curl -fsS --max-time 2 http://127.0.0.1:7332/readyz >/dev/null 2>&1; then
        TUNNEL_READY=1
        break
      fi
    fi
    n=$((n + 1))
    sleep 1
  done
  [ "$TUNNEL_LIVE" -eq 1 ] && say "Tunnel process: LIVE" || say "Tunnel process: not live yet"
  [ "$TUNNEL_READY" -eq 1 ] && say "Tunnel local readiness: READY (this does not prove control-plane polling or ChatGPT connector attachment)" || say "Tunnel local readiness: not ready yet (credentials/network/local MCP may need checking)"
fi

say "Reconfigure anytime: cycomagent-setup"
