#!/data/data/com.termux/files/usr/bin/sh
set -eu

: "${PREFIX:=/data/data/com.termux/files/usr}"
: "${HOME:?HOME is required}"
: "${TMPDIR:=$PREFIX/tmp}"

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ARCH=$(uname -m)
case "$ARCH" in
  aarch64|arm64)
    AGENT_SRC="$ROOT/dist/cycomagent-termux-arm64"
    TUNNEL_SRC="$ROOT/dist/tunnel-client-runtime-termux-arm64"
    ;;
  *)
    echo "Unsupported Termux architecture: $ARCH (current release: arm64/aarch64 only)" >&2
    exit 2
    ;;
esac

[ -x "$AGENT_SRC" ] || {
  echo "Missing release binary: $AGENT_SRC" >&2
  exit 2
}

install -m 0755 "$AGENT_SRC" "$PREFIX/bin/cycomagent"
if [ -x "$TUNNEL_SRC" ]; then
  install -m 0755 "$TUNNEL_SRC" "$PREFIX/bin/tunnel-client"
fi

STATE="$HOME/.local/state/cycomagent"
CONFIG="$HOME/.config/cycomagent"
SERVICE_ROOT="$PREFIX/var/service"
AGENT_SERVICE="$SERVICE_ROOT/cycomagent"
TUNNEL_SERVICE="$SERVICE_ROOT/cycomagent-tunnel"

mkdir -p "$STATE" "$STATE/log" "$STATE/tunnel-log" "$CONFIG" "$AGENT_SERVICE/log" "$TUNNEL_SERVICE/log"
chmod 700 "$STATE" "$CONFIG"

cat > "$AGENT_SERVICE/run" <<EOF_AGENT
#!$PREFIX/bin/sh
export PREFIX='$PREFIX'
export HOME='$HOME'
export TMPDIR='$TMPDIR'
export CYCOM_STATE_DIR='$STATE'
# Android device root / su / Magisk is intentionally unsupported and not planned.
unset CYCOM_ROOT_SOCKET
exec '$PREFIX/bin/cycomagent' --addr 127.0.0.1:7331
EOF_AGENT
chmod 0755 "$AGENT_SERVICE/run"

cat > "$AGENT_SERVICE/log/run" <<EOF_LOG
#!$PREFIX/bin/sh
mkdir -p '$STATE/log'
exec '$PREFIX/bin/svlogd' -tt '$STATE/log'
EOF_LOG
chmod 0755 "$AGENT_SERVICE/log/run"

if [ -x "$PREFIX/bin/tunnel-client" ]; then
  cat > "$TUNNEL_SERVICE/run" <<EOF_TUNNEL
#!$PREFIX/bin/sh
set -eu
export PREFIX='$PREFIX'
export HOME='$HOME'
export TMPDIR='$TMPDIR'
CONFIG='$CONFIG'
ID_FILE="\$CONFIG/tunnel-id"
KEY_FILE="\$CONFIG/control-plane-api-key"
[ -s "\$ID_FILE" ] || { echo 'cycomagent-tunnel: missing tunnel-id' >&2; exit 111; }
[ -s "\$KEY_FILE" ] || { echo 'cycomagent-tunnel: missing control-plane-api-key' >&2; exit 111; }
export CONTROL_PLANE_TUNNEL_ID="\$(cat "\$ID_FILE")"
export MCP_SERVER_URL='http://127.0.0.1:7331/mcp'
export MCP_STARTUP_WAIT_TIMEOUT='30s'
exec '$PREFIX/bin/tunnel-client' run \
  --control-plane.api-key "file:\$KEY_FILE" \
  --health.listen-addr 127.0.0.1:7332 \
  --log.level info \
  --log.format json
EOF_TUNNEL
  chmod 0755 "$TUNNEL_SERVICE/run"

  cat > "$TUNNEL_SERVICE/log/run" <<EOF_TLOG
#!$PREFIX/bin/sh
mkdir -p '$STATE/tunnel-log'
exec '$PREFIX/bin/svlogd' -tt '$STATE/tunnel-log'
EOF_TLOG
  chmod 0755 "$TUNNEL_SERVICE/log/run"

  # Keep the tunnel down until credentials are configured by cycomagent-setup.
  if [ ! -s "$CONFIG/tunnel-id" ] || [ ! -s "$CONFIG/control-plane-api-key" ]; then
    : > "$TUNNEL_SERVICE/down"
  fi
fi

# Install the interactive reconfiguration command for later use.
if [ -f "$ROOT/scripts/configure-termux.sh" ]; then
  install -m 0755 "$ROOT/scripts/configure-termux.sh" "$PREFIX/bin/cycomagent-setup"
fi

# termux-services installs its profile hook after the current shell may already
# have started. Initialize it now so a fresh install does not need a restart.
if [ -f "$PREFIX/etc/profile.d/start-services.sh" ]; then
  # shellcheck disable=SC1090
  . "$PREFIX/etc/profile.d/start-services.sh" || true
fi
export SVDIR="${SVDIR:-$SERVICE_ROOT}"
export LOGDIR="${LOGDIR:-$PREFIX/var/log}"
if command -v service-daemon >/dev/null 2>&1; then
  service-daemon start >/dev/null 2>&1 || true
fi

printf '%s\n' \
  "Installed: $PREFIX/bin/cycomagent" \
  "Profile: mobile_assistant (non-root)" \
  "State: $STATE" \
  "Service: $AGENT_SERVICE"

if [ -x "$PREFIX/bin/tunnel-client" ]; then
  echo "Installed: $PREFIX/bin/tunnel-client"
  echo "Tunnel service: $TUNNEL_SERVICE"
fi

if command -v sv-enable >/dev/null 2>&1; then
  sv-enable cycomagent >/dev/null 2>&1 || true
  sv up cycomagent >/dev/null 2>&1 || true
else
  echo "Recommended for supervision/autorestart: pkg install termux-services"
fi

if command -v termux-battery-status >/dev/null 2>&1; then
  echo "Termux:API CLI detected. Android tools remain subject to Android app permissions."
else
  echo "For Android assistant capabilities, install: pkg install termux-api"
  echo "The matching Termux:API add-on app must also be installed from the same source/signing family."
fi

echo "Configure permissions + tunnel: cycomagent-setup"
echo "Boot/autostart helper: ./scripts/install-termux-boot.sh --wake-lock"
echo "Health endpoint: http://127.0.0.1:7331/health"
echo "MCP endpoint:    http://127.0.0.1:7331/mcp"
echo "Tunnel health:   http://127.0.0.1:7332/healthz"
