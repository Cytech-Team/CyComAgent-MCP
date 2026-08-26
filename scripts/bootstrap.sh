#!/bin/sh
set -eu

VERSION="0.4.4-multiplatform-dev"
TAG="v${VERSION}"
RELEASE_BASE="https://github.com/Cytech-Team/CyComAgent-MCP/releases/download/${TAG}"

say() { printf '%s\n' "$*"; }
fail() { printf 'CyComAgent installer: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

WORK=""
cleanup() {
  [ -z "${WORK:-}" ] || rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

make_workdir() {
  base=${TMPDIR:-/tmp}
  WORK="$base/cycomagent-bootstrap-$$"
  mkdir -p "$WORK"
}

download_verified() {
  asset=$1
  sums=$2
  out="$WORK/$asset"
  sumfile="$WORK/$sums"

  say "Downloading $asset..."
  curl -fL --retry 3 --retry-delay 1 --connect-timeout 15 "$RELEASE_BASE/$asset" -o "$out"
  curl -fL --retry 3 --retry-delay 1 --connect-timeout 15 "$RELEASE_BASE/$sums" -o "$sumfile"

  expected=$(awk -v name="$asset" '$2 == name || $2 == "*" name { print $1; exit }' "$sumfile")
  [ -n "$expected" ] || fail "checksum for $asset is missing from $sums"
  actual=$(sha256sum "$out" | awk '{print $1}')
  [ "$actual" = "$expected" ] || fail "SHA-256 verification failed for $asset"
  say "SHA-256: verified"
}

install_termux() {
  case "$(uname -m)" in
    aarch64|arm64) ;;
    *) fail "Termux release currently supports ARM64/aarch64 only" ;;
  esac

  say "== CyComAgent-MCP $VERSION =="
  say "Detected: Android / Termux ARM64"

  NEED=""
  for cmd in curl tar sha256sum sv svlogd; do
    have "$cmd" || NEED=1
  done
  if [ -n "$NEED" ]; then
    pkg install -y curl tar coreutils ca-certificates termux-services
  else
    pkg install -y termux-services >/dev/null 2>&1 || true
  fi
  if ! have termux-battery-status; then
    pkg install -y termux-api || say "Warning: termux-api CLI could not be installed; Android API tools will remain unavailable until installed."
  fi

  make_workdir
  asset="CyComAgent-MCP-v${VERSION}-termux-arm64.tar.gz"
  download_verified "$asset" "SHA256SUMS-termux-release"
  tar -xzf "$WORK/$asset" -C "$WORK"
  root="$WORK/CyComAgent-MCP"
  [ -x "$root/scripts/install-termux.sh" ] || fail "release is missing scripts/install-termux.sh"

  "$root/scripts/install-termux.sh"
  "$root/scripts/configure-termux.sh"

  if [ "${CYCOM_WAKE_LOCK:-1}" = "0" ]; then
    "$root/scripts/install-termux-boot.sh"
  else
    "$root/scripts/install-termux-boot.sh" --wake-lock
    have termux-wake-lock && termux-wake-lock >/dev/null 2>&1 || true
  fi

  if [ -f "$PREFIX/etc/profile.d/start-services.sh" ]; then
    . "$PREFIX/etc/profile.d/start-services.sh" || true
  fi
  export SVDIR="${SVDIR:-$PREFIX/var/service}"
  export LOGDIR="${LOGDIR:-$PREFIX/var/log}"
  have service-daemon && service-daemon start >/dev/null 2>&1 || true

  say ""
  say "Installed CyComAgent for Termux."
  curl -fsS --max-time 3 http://127.0.0.1:7331/health >/dev/null 2>&1 && say "Health: OK" || say "Health: service installed, endpoint not ready yet"
  say "MCP: http://127.0.0.1:7331/mcp"
  say "Reconfigure: cycomagent-setup"
}

install_linux() {
  [ "$(uname -s)" = "Linux" ] || fail "this POSIX installer supports Linux and Termux; Windows uses /install/cycomagent.ps1"
  case "$(uname -m)" in
    x86_64|amd64) ;;
    *) fail "Linux release currently supports x86_64/amd64 only" ;;
  esac
  have systemctl || fail "systemd is required by the current Linux installer"
  have sudo || [ "$(id -u)" -eq 0 ] || fail "sudo is required"
  have tar || fail "tar is required"
  have sha256sum || fail "sha256sum is required"

  say "== CyComAgent-MCP $VERSION =="
  say "Detected: Linux amd64"
  make_workdir
  asset="CyComAgent-MCP-v${VERSION}-linux-amd64.tar.gz"
  download_verified "$asset" "SHA256SUMS-linux"
  tar -xzf "$WORK/$asset" -C "$WORK"
  root="$WORK/CyComAgent-MCP"
  [ -x "$root/scripts/install-systemd.sh" ] || fail "release is missing scripts/install-systemd.sh"

  target_user=${CYCOM_USER:-${SUDO_USER:-$(id -un)}}
  say "Installing for user: $target_user"
  "$root/scripts/install-systemd.sh" "$target_user"

  say ""
  say "Installed CyComAgent for Linux."
  curl -fsS --max-time 3 http://127.0.0.1:7331/health >/dev/null 2>&1 && say "Health: OK" || say "Health: service installed, endpoint not ready yet"
  say "MCP: http://127.0.0.1:7331/mcp"
}

case "$(uname -s 2>/dev/null || printf unknown)" in
  Linux)
    if [ -n "${PREFIX:-}" ] && printf '%s' "$PREFIX" | grep -q '/com\.termux/'; then
      install_termux
    else
      install_linux
    fi
    ;;
  Android)
    install_termux
    ;;
  Darwin)
    fail "macOS is not supported yet"
    ;;
  *)
    fail "unsupported platform; on Windows use: curl.exe -fsSL https://cdn.cytechteam.site/install/cycomagent.ps1 | powershell -NoProfile -ExecutionPolicy Bypass -Command -"
    ;;
esac
