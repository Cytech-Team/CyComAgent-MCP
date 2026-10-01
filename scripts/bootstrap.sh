#!/bin/sh
set -eu

REPO_SLUG="${CYCOM_UPSTREAM_REPO:-Cytech-Team/CyComAgent-MCP}"
RELEASES_ATOM="https://github.com/$REPO_SLUG/releases.atom"
NEA_TERMUX_BASE="${CYCOM_NEA_TERMUX_BASE:-https://cdn.cytechteam.site/uploads/nea/termux-arm64/tunnel-client}"
AUTO_UPSTREAM="${CYCOM_AUTO_UPSTREAM:-1}"
DRY_RUN="${CYCOM_DRY_RUN:-0}"

say() { printf '%s\n' "$*"; }
fail() { printf 'CyComAgent installer: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

WORK=""
cleanup() {
  [ -z "${WORK:-}" ] || rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

make_workdir() {
  [ -n "$WORK" ] && return 0
  base=${TMPDIR:-/tmp}
  WORK="$base/cycomagent-bootstrap-$$"
  mkdir -p "$WORK"
}

sha256_file() {
  file=$1
  if have sha256sum; then
    sha256sum "$file" | awk '{print $1}'
  elif have shasum; then
    shasum -a 256 "$file" | awk '{print $1}'
  elif have openssl; then
    openssl dgst -sha256 "$file" | awk '{print $NF}'
  else
    fail "SHA-256 tool is required (sha256sum, shasum, or openssl)"
  fi
}

release_tags() {
  if [ -n "${CYCOM_RELEASE_TAG:-}" ]; then
    printf '%s\n' "$CYCOM_RELEASE_TAG"
    return
  fi
  make_workdir
  feed="$WORK/releases.atom"
  if [ ! -s "$feed" ]; then
    curl -fsSL --retry 3 --retry-delay 1 --connect-timeout 15 "$RELEASES_ATOM" -o "$feed"
  fi
  sed -n 's#.*href="https://github.com/[^"]*/releases/tag/\([^"]*\)".*#\1#p' "$feed" | awk '!seen[$0]++'
}

release_base() {
  printf 'https://github.com/%s/releases/download/%s' "$REPO_SLUG" "$1"
}

asset_exists() {
  ae_tag=$1
  ae_asset=$2
  curl -fsIL --retry 2 --connect-timeout 10 --max-time 20 "$(release_base "$ae_tag")/$ae_asset" >/dev/null 2>&1
}

select_packaged_release() {
  sp_suffix=$1
  sp_sums=$2
  SELECTED_TAG=""
  SELECTED_VERSION=""
  SELECTED_ASSET=""
  SELECTED_SUMS="$sp_sums"

  for sp_tag in $(release_tags); do
    sp_version=${sp_tag#v}
    sp_asset="CyComAgent-MCP-${sp_tag}-${sp_suffix}"
    if asset_exists "$sp_tag" "$sp_asset" && asset_exists "$sp_tag" "$sp_sums"; then
      SELECTED_TAG=$sp_tag
      SELECTED_VERSION=$sp_version
      SELECTED_ASSET=$sp_asset
      return 0
    fi
  done
  return 1
}

select_sync_release() {
  ss_asset_a=$1
  ss_asset_b=${2:-}
  SYNC_TAG=""
  for ss_tag in $(release_tags); do
    if asset_exists "$ss_tag" "SHA256SUMS-sync" && asset_exists "$ss_tag" "$ss_asset_a"; then
      if [ -z "$ss_asset_b" ] || asset_exists "$ss_tag" "$ss_asset_b"; then
        SYNC_TAG=$ss_tag
        return 0
      fi
    fi
  done
  return 1
}

download_verified() {
  dv_tag=$1
  dv_asset=$2
  dv_sums=$3
  dv_out="$WORK/$dv_asset"
  dv_sumfile="$WORK/${dv_tag#v}-$dv_sums"
  dv_base=$(release_base "$dv_tag")

  say "Downloading $dv_asset from $dv_tag..."
  curl -fL --retry 3 --retry-delay 1 --connect-timeout 15 "$dv_base/$dv_asset" -o "$dv_out"
  curl -fL --retry 3 --retry-delay 1 --connect-timeout 15 "$dv_base/$dv_sums" -o "$dv_sumfile"

  dv_expected=$(awk -v name="$dv_asset" '$2 == name || $2 == "*" name { print $1; exit }' "$dv_sumfile")
  [ -n "$dv_expected" ] || fail "checksum for $dv_asset is missing from $dv_sums"
  dv_actual=$(sha256_file "$dv_out")
  [ "$dv_actual" = "$dv_expected" ] || fail "SHA-256 verification failed for $dv_asset"
  say "SHA-256: verified"
  DOWNLOADED="$dv_out"
}

sync_termux_core() {
  [ "$AUTO_UPSTREAM" = "1" ] || return 0
  select_sync_release "cycomagent-android-arm64" || {
    say "Auto-upstream: no newer Android sync asset found; keeping packaged core."
    return 0
  }

  download_verified "$SYNC_TAG" "cycomagent-android-arm64" "SHA256SUMS-sync"
  current=""
  [ -x "$PREFIX/bin/cycomagent" ] && current=$(sha256_file "$PREFIX/bin/cycomagent" || true)
  incoming=$(sha256_file "$DOWNLOADED")
  if [ "$current" = "$incoming" ]; then
    say "Auto-upstream core: already current ($SYNC_TAG)."
    return 0
  fi

  if have sv; then sv down cycomagent >/dev/null 2>&1 || true; fi
  install -m 0755 "$DOWNLOADED" "$PREFIX/bin/cycomagent"
  if have sv; then sv up cycomagent >/dev/null 2>&1 || true; fi
  say "Auto-upstream core: $SYNC_TAG"
}

sync_linux_core() {
  user_name=$1
  [ "$AUTO_UPSTREAM" = "1" ] || return 0
  select_sync_release "cycomagent-linux-amd64" "cycomagent-root-linux-amd64" || {
    say "Auto-upstream: no newer Linux sync assets found; keeping packaged core."
    return 0
  }

  download_verified "$SYNC_TAG" "cycomagent-linux-amd64" "SHA256SUMS-sync"
  runtime_bin="$DOWNLOADED"
  runtime_tmp="$WORK/nea-sync-runtime"
  cp "$runtime_bin" "$runtime_tmp"

  download_verified "$SYNC_TAG" "cycomagent-root-linux-amd64" "SHA256SUMS-sync"
  root_tmp="$WORK/nea-sync-root"
  cp "$DOWNLOADED" "$root_tmp"

  sudo systemctl stop "cycomagent@${user_name}.service" "cycomagent-root@${user_name}.service" >/dev/null 2>&1 || true
  sudo install -m 0755 "$runtime_tmp" /usr/local/bin/cycomagent
  sudo install -m 0755 "$root_tmp" /usr/local/bin/cycomagent-root
  sudo systemctl start "cycomagent-root@${user_name}.service" "cycomagent@${user_name}.service" >/dev/null 2>&1 || true
  say "Auto-upstream core/root: $SYNC_TAG"
}

sync_nea_termux_tunnel() {
  [ "$AUTO_UPSTREAM" = "1" ] || return 0
  manifest="$WORK/nea-tunnel-current.env"
  if ! curl -fsSL --retry 3 --retry-delay 1 --connect-timeout 15 "$NEA_TERMUX_BASE/current.env" -o "$manifest"; then
    say "NEA: tunnel manifest unavailable; keeping packaged tunnel runtime."
    return 0
  fi

  asset=$(sed -n 's/^ASSET=//p' "$manifest" | head -n 1)
  expected=$(sed -n 's/^SHA256=//p' "$manifest" | head -n 1)
  upstream_ref=$(sed -n 's/^UPSTREAM_REF=//p' "$manifest" | head -n 1)
  profile=$(sed -n 's/^NEA_PROFILE=//p' "$manifest" | head -n 1)

  case "$asset" in
    tunnel-client-runtime-termux-arm64-*) ;;
    *) say "NEA: invalid tunnel asset name; keeping packaged runtime."; return 0 ;;
  esac
  case "$expected" in
    [0-9a-f][0-9a-f]*) ;;
    *) say "NEA: invalid tunnel checksum; keeping packaged runtime."; return 0 ;;
  esac
  [ "${#expected}" -eq 64 ] || {
    say "NEA: invalid tunnel checksum length; keeping packaged runtime."
    return 0
  }

  out="$WORK/$asset"
  curl -fL --retry 3 --retry-delay 1 --connect-timeout 15 "$NEA_TERMUX_BASE/$asset" -o "$out"
  actual=$(sha256_file "$out")
  [ "$actual" = "$expected" ] || fail "NEA tunnel SHA-256 verification failed"

  current=""
  [ -x "$PREFIX/bin/tunnel-client" ] && current=$(sha256_file "$PREFIX/bin/tunnel-client" || true)
  if [ "$current" != "$actual" ]; then
    if have sv; then sv down cycomagent-tunnel >/dev/null 2>&1 || true; fi
    install -m 0755 "$out" "$PREFIX/bin/tunnel-client"
    if have sv && [ -s "$HOME/.config/cycomagent/tunnel-id" ]; then
      rm -f "$PREFIX/var/service/cycomagent-tunnel/down" >/dev/null 2>&1 || true
      sv up cycomagent-tunnel >/dev/null 2>&1 || true
    fi
  fi
  say "NEA tunnel: ${profile:-termux-arm64} / ${upstream_ref:-current}"
}

dry_run_report() {
  platform=$1
  make_workdir
  case "$platform" in
    termux)
      select_packaged_release "termux-arm64.tar.gz" "SHA256SUMS-termux-release" || fail "no compatible Termux package release found"
      say "NEA dry-run:"
      say "  packaged base: $SELECTED_TAG / $SELECTED_ASSET"
      if select_sync_release "cycomagent-android-arm64"; then say "  auto-upstream core: $SYNC_TAG / cycomagent-android-arm64"; fi
      manifest=$(curl -fsSL "$NEA_TERMUX_BASE/current.env" 2>/dev/null || true)
      [ -z "$manifest" ] || printf '%s\n' "$manifest" | sed 's/^/  nea: /'
      ;;
    linux)
      select_packaged_release "linux-amd64.tar.gz" "SHA256SUMS-linux" || fail "no compatible Linux package release found"
      say "NEA dry-run:"
      say "  packaged base: $SELECTED_TAG / $SELECTED_ASSET"
      if select_sync_release "cycomagent-linux-amd64" "cycomagent-root-linux-amd64"; then say "  auto-upstream core: $SYNC_TAG"; fi
      ;;
    macos-arm64|macos-amd64)
      arch=${platform#macos-}
      select_packaged_release "macos-${arch}.tar.gz" "SHA256SUMS-macos" || fail "no compatible macOS package release found"
      say "NEA dry-run:"
      say "  packaged base: $SELECTED_TAG / $SELECTED_ASSET"
      ;;
  esac
  exit 0
}

install_termux() {
  case "$(uname -m)" in
    aarch64|arm64) ;;
    *) fail "Termux release currently supports ARM64/aarch64 only" ;;
  esac

  make_workdir
  [ "$DRY_RUN" = "1" ] && dry_run_report termux

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

  select_packaged_release "termux-arm64.tar.gz" "SHA256SUMS-termux-release" || fail "no compatible Termux package release found"
  say "== CyComAgent + NEA =="
  say "Detected: Android / Termux ARM64"
  say "Package base: $SELECTED_TAG"

  download_verified "$SELECTED_TAG" "$SELECTED_ASSET" "$SELECTED_SUMS"
  tar -xzf "$DOWNLOADED" -C "$WORK"
  root="$WORK/CyComAgent-MCP"
  [ -x "$root/scripts/install-termux.sh" ] || fail "release is missing scripts/install-termux.sh"

  "$root/scripts/install-termux.sh"
  sync_termux_core
  sync_nea_termux_tunnel
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
  say "Installed CyComAgent + NEA for Termux."
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

  make_workdir
  [ "$DRY_RUN" = "1" ] && dry_run_report linux

  select_packaged_release "linux-amd64.tar.gz" "SHA256SUMS-linux" || fail "no compatible Linux package release found"
  say "== CyComAgent + NEA =="
  say "Detected: Linux amd64"
  say "Package base: $SELECTED_TAG"

  download_verified "$SELECTED_TAG" "$SELECTED_ASSET" "$SELECTED_SUMS"
  tar -xzf "$DOWNLOADED" -C "$WORK"
  root="$WORK/CyComAgent-MCP"
  [ -x "$root/scripts/install-systemd.sh" ] || fail "release is missing scripts/install-systemd.sh"

  target_user=${CYCOM_USER:-${SUDO_USER:-$(id -un)}}
  say "Installing for user: $target_user"
  "$root/scripts/install-systemd.sh" "$target_user"
  sync_linux_core "$target_user"

  say ""
  say "Installed CyComAgent + NEA for Linux."
  curl -fsS --max-time 3 http://127.0.0.1:7331/health >/dev/null 2>&1 && say "Health: OK" || say "Health: service installed, endpoint not ready yet"
  say "MCP: http://127.0.0.1:7331/mcp"
}

install_macos() {
  case "$(uname -m)" in
    arm64|aarch64) arch=arm64 ;;
    x86_64|amd64) arch=amd64 ;;
    *) fail "macOS experimental release supports Apple Silicon arm64 and Intel x86_64 only" ;;
  esac
  have tar || fail "tar is required"
  have curl || fail "curl is required"
  if ! have shasum && ! have sha256sum && ! have openssl; then
    fail "shasum, sha256sum, or openssl is required for checksum verification"
  fi
  have launchctl || fail "launchctl is required"

  make_workdir
  [ "$DRY_RUN" = "1" ] && dry_run_report "macos-$arch"

  select_packaged_release "macos-${arch}.tar.gz" "SHA256SUMS-macos" || fail "no compatible macOS package release found"
  say "== CyComAgent + NEA =="
  say "Detected: macOS $arch (experimental)"
  say "Package base: $SELECTED_TAG"

  download_verified "$SELECTED_TAG" "$SELECTED_ASSET" "$SELECTED_SUMS"
  tar -xzf "$DOWNLOADED" -C "$WORK"
  root="$WORK/CyComAgent-MCP"
  [ -x "$root/scripts/install-launchd.sh" ] || fail "release is missing scripts/install-launchd.sh"

  "$root/scripts/install-launchd.sh"

  say ""
  say "Installed CyComAgent experimental runtime for macOS."
  curl -fsS --max-time 3 http://127.0.0.1:7331/health >/dev/null 2>&1 && say "Health: OK" || say "Health: LaunchAgent installed; endpoint may still be starting or awaiting macOS permissions"
  say "MCP: http://127.0.0.1:7331/mcp"
  say "Note: local root broker is not supported on macOS."
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
    install_macos
    ;;
  *)
    fail "unsupported platform; on Windows use: curl.exe -fsSL https://cdn.cytechteam.site/install/cycomagent.ps1 | powershell -NoProfile -ExecutionPolicy Bypass -Command -"
    ;;
esac
