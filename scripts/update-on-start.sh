#!/bin/sh
# Startup updater: GitHub Releases only; fail open and never replace a binary
# until every required artifact has passed SHA-256 verification.
set -u

REPO="${CYCOM_UPSTREAM_REPO:-Cytech-Team/CyComAgent-MCP}"
CHANNEL="${CYCOM_AUTO_UPDATE_CHANNEL:-prerelease}"
PLATFORM="${1:-}"
BIN_DIR="${2:-}"
USER_NAME="${3:-}"
LOG_TAG="cycomagent-updater"
say() { printf '%s: %s\n' "$LOG_TAG" "$*" >&2; }
have() { command -v "$1" >/dev/null 2>&1; }

[ "${CYCOM_AUTO_UPDATE:-1}" = "1" ] || exit 0
[ -n "$PLATFORM" ] && [ -n "$BIN_DIR" ] || exit 0
have curl || { say "curl unavailable; keeping installed version"; exit 0; }
have sha256sum || { say "sha256sum unavailable; keeping installed version"; exit 0; }
have awk || { say "awk unavailable; keeping installed version"; exit 0; }
have install && have mv || { say "install/mv unavailable; keeping installed version"; exit 0; }

TMP_BASE=${TMPDIR:-/tmp}
WORK="$TMP_BASE/cycomagent-update-$$"
(umask 077 && mkdir -p "$WORK") || exit 0
trap 'rm -rf "$WORK"' EXIT HUP INT TERM

API="https://api.github.com/repos/$REPO/releases?per_page=100"
if ! curl -fsSL --connect-timeout 8 --max-time 25 --retry 1 \
  -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2022-11-28' \
  "$API" -o "$WORK/releases.json"; then
  say "GitHub unavailable; keeping installed version"
  exit 0
fi

# GitHub's release JSON places tag_name before prerelease in each release object.
# The channel is explicit: prerelease only, or stable only.
case "$CHANNEL" in
  prerelease)
    TAGS=$(awk '
      /"tag_name"[[:space:]]*:/ { tag=$2; gsub(/[",]/, "", tag) }
      /"prerelease"[[:space:]]*:[[:space:]]*true/ { if (tag != "") print tag; tag="" }
      /"prerelease"[[:space:]]*:[[:space:]]*false/ { tag="" }
    ' "$WORK/releases.json")
    ;;
  stable)
    TAGS=$(awk '
      /"tag_name"[[:space:]]*:/ { tag=$2; gsub(/[",]/, "", tag) }
      /"prerelease"[[:space:]]*:[[:space:]]*false/ { if (tag != "") print tag; tag="" }
      /"prerelease"[[:space:]]*:[[:space:]]*true/ { tag="" }
    ' "$WORK/releases.json")
    ;;
  *)
    say "unknown channel '$CHANNEL'; keeping installed version"
    exit 0
    ;;
esac

case "$PLATFORM" in
  linux-amd64)
    ASSETS="cycomagent-linux-amd64 cycomagent-root-linux-amd64"
    ;;
  termux-arm64)
    ASSETS="cycomagent-android-arm64"
    ;;
  *)
    say "platform '$PLATFORM' is not supported by this updater yet"
    exit 0
    ;;
esac

for tag in $TAGS; do
  case "$tag" in *[!A-Za-z0-9._-]*) continue ;; esac
  BASE="https://github.com/$REPO/releases/download/$tag"
  if ! curl -fsSL --connect-timeout 8 --max-time 20 "$BASE/SHA256SUMS-sync" -o "$WORK/SHA256SUMS"; then
    continue
  fi

  VALID=1
  for asset in $ASSETS; do
    expected=$(awk -v name="$asset" '$2 == name || $2 == "*" name {print $1; exit}' "$WORK/SHA256SUMS")
    case "$expected" in
      ''|*[!0-9a-fA-F]*) VALID=0; break ;;
    esac
    [ "${#expected}" -eq 64 ] || { VALID=0; break; }
    if ! curl -fsSL --connect-timeout 8 --max-time 60 --retry 1 "$BASE/$asset" -o "$WORK/$asset"; then
      VALID=0; break
    fi
    actual=$(sha256sum "$WORK/$asset" | awk '{print $1}')
    [ "$actual" = "$expected" ] || { VALID=0; break; }
  done
  [ "$VALID" -eq 1 ] || { say "asset/checksum validation failed for $tag; trying older prerelease"; continue; }

  CHANGED=0
  ROOT_CHANGED=0
  for asset in $ASSETS; do
    case "$asset" in
      cycomagent-linux-amd64) target="$BIN_DIR/cycomagent" ;;
      cycomagent-root-linux-amd64) target="$BIN_DIR/cycomagent-root" ;;
      cycomagent-android-arm64) target="$BIN_DIR/cycomagent" ;;
    esac
    current=""
    [ -f "$target" ] && current=$(sha256sum "$target" 2>/dev/null | awk '{print $1}')
    incoming=$(sha256sum "$WORK/$asset" | awk '{print $1}')
    [ "$current" = "$incoming" ] && continue
    staged="$target.cycomagent-new.$$"
    if ! install -m 0755 "$WORK/$asset" "$staged" || ! mv -f "$staged" "$target"; then
      rm -f "$staged"
      say "atomic replacement failed for $target; preserving remaining files"
      exit 0
    fi
    CHANGED=1
    [ "$asset" != "cycomagent-root-linux-amd64" ] || ROOT_CHANGED=1
  done
  if [ "$ROOT_CHANGED" -eq 1 ] && [ -n "$USER_NAME" ] && have systemctl; then
    systemctl restart "cycomagent-root@$USER_NAME.service" >/dev/null 2>&1 || say "root broker restart failed; restart it manually"
  fi
  if [ "$CHANGED" -eq 1 ]; then
    say "updated $PLATFORM to $tag"
  else
    say "$PLATFORM already matches $tag"
  fi
  exit 0
done

say "no compatible $CHANNEL release assets found; keeping installed version"
exit 0
