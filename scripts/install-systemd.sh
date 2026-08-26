#!/usr/bin/env bash
set -Eeuo pipefail
USER_NAME="${1:-${SUDO_USER:-$USER}}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [[ $EUID -ne 0 ]]; then
  exec sudo "$0" "$USER_NAME"
fi

id "$USER_NAME" >/dev/null
install -d -m 0755 /usr/local/lib/cycomagent /etc/cycomagent
install -m 0755 "$ROOT/dist/cycomagent" /usr/local/bin/cycomagent
install -m 0755 "$ROOT/dist/cycomagent-root" /usr/local/bin/cycomagent-root
install -m 0755 "$ROOT/scripts/watchdog.sh" /usr/local/lib/cycomagent/watchdog.sh
install -m 0755 "$ROOT/scripts/smoke-mcp.sh" /usr/local/lib/cycomagent/smoke-mcp.sh
install -m 0755 "$ROOT/scripts/doctor.sh" /usr/local/lib/cycomagent/doctor.sh
install -m 0755 "$ROOT/scripts/chaos-test.sh" /usr/local/lib/cycomagent/chaos-test.sh
install -m 0755 "$ROOT/scripts/recovery-test.sh" /usr/local/lib/cycomagent/recovery-test.sh
install -m 0644 "$ROOT/packaging/systemd/cycomagent@.service" /etc/systemd/system/
install -m 0644 "$ROOT/packaging/systemd/cycomagent-root@.service" /etc/systemd/system/
install -m 0644 "$ROOT/packaging/systemd/cycomagent-tunnel@.service" /etc/systemd/system/
install -m 0644 "$ROOT/packaging/systemd/cycomagent-watchdog@.service" /etc/systemd/system/
install -m 0644 "$ROOT/packaging/systemd/cycomagent-watchdog@.timer" /etc/systemd/system/

# Per-user runtime config is optional; create an empty private file if absent.
if [[ ! -e "/etc/cycomagent/${USER_NAME}.env" ]]; then
  install -m 0600 /dev/null "/etc/cycomagent/${USER_NAME}.env"
fi

systemctl daemon-reload
systemctl enable --now "cycomagent-root@${USER_NAME}.service"
systemctl enable --now "cycomagent@${USER_NAME}.service"
systemctl enable --now "cycomagent-watchdog@${USER_NAME}.timer"

echo
systemctl --no-pager --full status "cycomagent@${USER_NAME}.service" || true
echo
curl -fsS http://127.0.0.1:7331/health || true
echo
cat <<MSG
CyComAgent-MCP installed for ${USER_NAME}.

Runtime: http://127.0.0.1:7331/mcp
Health:  http://127.0.0.1:7331/health

To enable the OpenAI tunnel, create:
  /etc/cycomagent/${USER_NAME}-tunnel.env
with CONTROL_PLANE_TUNNEL_ID and CONTROL_PLANE_API_KEY (plus any tunnel settings),
then run:
  systemctl enable --now cycomagent-tunnel@${USER_NAME}.service
MSG
