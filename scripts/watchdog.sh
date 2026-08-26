#!/usr/bin/env bash
set -euo pipefail
USER_NAME="${1:?usage: watchdog.sh USER}"
RUNTIME="cycomagent@${USER_NAME}.service"
TUNNEL="cycomagent-tunnel@${USER_NAME}.service"

probe() { curl -fsS --max-time 3 "$1" >/dev/null 2>&1; }

if ! probe http://127.0.0.1:7331/readyz; then
  logger -t cycomagent-watchdog "runtime readiness failed; restarting ${RUNTIME}"
  systemctl restart "$RUNTIME"
  exit 0
fi

# Tunnel health is optional because a user may intentionally not install it.
if systemctl is-enabled "$TUNNEL" >/dev/null 2>&1; then
  if ! probe http://127.0.0.1:7332/readyz; then
    logger -t cycomagent-watchdog "tunnel readiness failed; restarting ${TUNNEL}"
    systemctl restart "$TUNNEL"
  fi
fi
