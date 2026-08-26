#!/data/data/com.termux/files/usr/bin/sh
set -eu

VERSION="0.4.2-termux-dev"
BASE_URL="https://cdn.cytechteam.site/uploads"
ARCHIVE="CyComAgent-MCP-v0.4.2-termux-dev-termux-arm64-41e35874.tar.gz"
EXPECTED_SHA256="41e3587422d76ab0c7d9696e255de08a757c17178b39518d32ee3d2a49b184d5"

say() { printf '%s\n' "$*"; }
fail() { printf 'CyComAgent installer: %s\n' "$*" >&2; exit 1; }

[ -n "${PREFIX:-}" ] || fail "run this command inside Termux"
case "$PREFIX" in
  */com.termux/files/usr) ;;
  *) say "Warning: PREFIX does not look like the standard Termux prefix: $PREFIX" ;;
esac

case "$(uname -m)" in
  aarch64|arm64) ARCH=arm64 ;;
  *) fail "this release supports Android ARM64/aarch64 only" ;;
esac

say "== CyComAgent-MCP $VERSION one-command setup =="
say "Detected: Android/Termux $ARCH"

# Install only OS packages that can be installed non-interactively. Android
# add-on APKs and permission grants still require Android/user approval.
say "Checking Termux packages..."
NEED=""
for cmd in curl tar sha256sum sv svlogd; do
  command -v "$cmd" >/dev/null 2>&1 || NEED=1
done
if [ -n "$NEED" ]; then
  pkg install -y curl tar coreutils ca-certificates termux-services
else
  # Ensure termux-services itself is present even if inherited binaries happen
  # to exist from an older/manual install.
  pkg install -y termux-services >/dev/null 2>&1 || true
fi

if ! command -v termux-battery-status >/dev/null 2>&1; then
  say "Installing Termux:API command package..."
  pkg install -y termux-api || say "Warning: termux-api CLI package could not be installed; phone API tools will be unavailable until installed."
fi

WORK="${TMPDIR:-$PREFIX/tmp}/cycomagent-bootstrap-$$"
mkdir -p "$WORK"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT INT TERM

URL="$BASE_URL/$ARCHIVE"
OUT="$WORK/$ARCHIVE"
say "Downloading release..."
curl -fL --retry 3 --retry-delay 1 --connect-timeout 15 "$URL" -o "$OUT"

ACTUAL_SHA256=$(sha256sum "$OUT" | awk '{print $1}')
if [ "$ACTUAL_SHA256" != "$EXPECTED_SHA256" ]; then
  printf 'Expected: %s\nActual:   %s\n' "$EXPECTED_SHA256" "$ACTUAL_SHA256" >&2
  fail "release SHA-256 verification failed"
fi
say "SHA-256: verified"

tar -xzf "$OUT" -C "$WORK"
ROOT="$WORK/CyComAgent-MCP"
[ -x "$ROOT/scripts/install-termux.sh" ] || fail "release archive is missing install-termux.sh"

say "Installing CyComAgent + tunnel runtime..."
"$ROOT/scripts/install-termux.sh"

say ""
say "Configuring tool permissions and Secure MCP Tunnel..."
"$ROOT/scripts/configure-termux.sh"

WAKE_LOCK=${CYCOM_WAKE_LOCK:-1}
if [ "$WAKE_LOCK" = "0" ]; then
  "$ROOT/scripts/install-termux-boot.sh"
else
  "$ROOT/scripts/install-termux-boot.sh" --wake-lock
  command -v termux-wake-lock >/dev/null 2>&1 && termux-wake-lock >/dev/null 2>&1 || true
fi

# Initialize services in this shell one final time and print a concise status.
if [ -f "$PREFIX/etc/profile.d/start-services.sh" ]; then
  # shellcheck disable=SC1090
  . "$PREFIX/etc/profile.d/start-services.sh" || true
fi
export SVDIR="${SVDIR:-$PREFIX/var/service}"
export LOGDIR="${LOGDIR:-$PREFIX/var/log}"
command -v service-daemon >/dev/null 2>&1 && service-daemon start >/dev/null 2>&1 || true

say ""
say "== Installed =="
if command -v sv >/dev/null 2>&1; then
  sv status cycomagent 2>/dev/null || true
  if [ -s "$HOME/.config/cycomagent/tunnel-id" ]; then
    sv status cycomagent-tunnel 2>/dev/null || true
  fi
fi
if curl -fsS --max-time 3 http://127.0.0.1:7331/health >/dev/null 2>&1; then
  say "CyComAgent: healthy"
else
  say "CyComAgent: service installed; health endpoint not ready yet"
fi
if [ -s "$HOME/.config/cycomagent/tunnel-id" ]; then
  if curl -fsS --max-time 3 http://127.0.0.1:7332/readyz >/dev/null 2>&1; then
    say "Secure MCP Tunnel: ready"
  elif curl -fsS --max-time 3 http://127.0.0.1:7332/healthz >/dev/null 2>&1; then
    say "Secure MCP Tunnel: live, not ready yet"
  else
    say "Secure MCP Tunnel: starting/not connected yet"
  fi
fi

say ""
say "Reconfigure permissions/tunnel anytime: cycomagent-setup"
say "Agent health:  http://127.0.0.1:7331/health"
say "MCP endpoint:   http://127.0.0.1:7331/mcp"
say "Tunnel health:  http://127.0.0.1:7332/healthz"
say "Logs:           $HOME/.local/state/cycomagent/{log,tunnel-log}"
say ""
say "Non-root limitation: Android still requires the matching Termux:Boot and Termux:API add-on apps plus user-approved permissions/battery settings. Root/su/Magisk is not supported or planned."
