#!/data/data/com.termux/files/usr/bin/sh
set -eu

: "${PREFIX:=/data/data/com.termux/files/usr}"
: "${HOME:?HOME is required}"

WAKE_LOCK=0
case "${1:-}" in
  --wake-lock) WAKE_LOCK=1 ;;
  "") ;;
  *) echo "usage: $0 [--wake-lock]" >&2; exit 2 ;;
esac

BOOT_DIR="$HOME/.termux/boot"
BOOT_SCRIPT="$BOOT_DIR/20-cycomagent"
CONFIG="$HOME/.config/cycomagent"
mkdir -p "$BOOT_DIR"

{
  echo "#!$PREFIX/bin/sh"
  echo "export PREFIX='$PREFIX'"
  echo "export HOME='$HOME'"
  echo "export SVDIR='$PREFIX/var/service'"
  echo "export LOGDIR='$PREFIX/var/log'"
  if [ "$WAKE_LOCK" -eq 1 ]; then
    echo "command -v termux-wake-lock >/dev/null 2>&1 && termux-wake-lock || true"
  fi
  echo "if [ -f '$PREFIX/etc/profile.d/start-services.sh' ]; then"
  echo "  . '$PREFIX/etc/profile.d/start-services.sh' || true"
  echo "fi"
  echo "command -v service-daemon >/dev/null 2>&1 && service-daemon start >/dev/null 2>&1 || true"
  echo "command -v sv >/dev/null 2>&1 && sv up cycomagent >/dev/null 2>&1 || true"
  echo "if [ -s '$CONFIG/tunnel-id' ] && [ -s '$CONFIG/control-plane-api-key' ] && [ -x '$PREFIX/bin/tunnel-client' ]; then"
  echo "  rm -f '$PREFIX/var/service/cycomagent-tunnel/down' >/dev/null 2>&1 || true"
  echo "  command -v sv >/dev/null 2>&1 && sv up cycomagent-tunnel >/dev/null 2>&1 || true"
  echo "fi"
} > "$BOOT_SCRIPT"
chmod 0700 "$BOOT_SCRIPT"

printf 'Installed Termux:Boot script: %s\n' "$BOOT_SCRIPT"
if [ "$WAKE_LOCK" -eq 1 ]; then
  echo "Wake lock: ON (better background reliability, higher battery use)."
else
  echo "Wake lock: OFF. Re-run with --wake-lock for maximum persistence."
fi

# Best-effort detection only. Android does not allow an ordinary Termux process
# to silently install the Boot add-on or grant battery/permission exemptions.
BOOT_INSTALLED=unknown
if command -v /system/bin/pm >/dev/null 2>&1 || [ -x /system/bin/pm ]; then
  if /system/bin/pm path com.termux.boot >/dev/null 2>&1; then
    BOOT_INSTALLED=yes
  else
    BOOT_INSTALLED=no
  fi
fi

case "$BOOT_INSTALLED" in
  yes)
    echo "Termux:Boot add-on: detected."
    # Launch once when Android permits it; harmless if already initialized.
    if [ -x /system/bin/monkey ]; then
      /system/bin/monkey -p com.termux.boot 1 >/dev/null 2>&1 || true
    fi
    ;;
  no)
    echo "Termux:Boot add-on: NOT detected. Install the matching add-on from the same source/signing family as Termux, then launch it once."
    ;;
  *)
    echo "Termux:Boot add-on: could not be detected automatically. Ensure it is installed and launched once before rebooting."
    ;;
esac

echo "Android battery optimization is OS-controlled; set Termux and Termux:Boot to Unrestricted for best persistence."
