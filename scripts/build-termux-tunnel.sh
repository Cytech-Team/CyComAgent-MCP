#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUT="$ROOT/dist/tunnel-client-runtime-termux-arm64"
REPO="${TUNNEL_CLIENT_REPO:-https://github.com/openai/tunnel-client.git}"
REF="${TUNNEL_CLIENT_REF:-auto}"

command -v git >/dev/null 2>&1 || { echo "git is required" >&2; exit 2; }
command -v go >/dev/null 2>&1 || { echo "go is required" >&2; exit 2; }

WORK=$(mktemp -d "${TMPDIR:-/tmp}/cycom-tunnel-build.XXXXXX")
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT INT TERM

git clone -q --filter=blob:none "$REPO" "$WORK/tunnel-client"
cd "$WORK/tunnel-client"

if [ "$REF" = "auto" ]; then
  # Follow the newest stable semantic-version tag from upstream. Development
  # tags remain opt-in through TUNNEL_CLIENT_REF so a transient upstream branch
  # cannot silently enter a release build.
  REF=$(git tag --list 'v*' --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -n 1 || true)
  [ -n "$REF" ] || {
    echo "unable to resolve a stable tunnel-client upstream tag" >&2
    exit 1
  }
fi

git checkout -q --detach "$REF"
COMMIT=$(git rev-parse HEAD)

"$ROOT/scripts/nea-apply.sh" termux-arm64/tunnel-client "$WORK/tunnel-client"

mkdir -p "$ROOT/dist"
CGO_ENABLED=0 GOOS=android GOARCH=arm64 GOTOOLCHAIN=auto \
  go build -trimpath -ldflags='-s -w' \
  -o "$OUT" ./cmd/client-runtime
chmod 0755 "$OUT"

{
  printf 'repo=%s\n' "$REPO"
  printf 'ref=%s\n' "$REF"
  printf 'commit=%s\n' "$COMMIT"
  printf 'nea_profile=%s\n' 'termux-arm64/tunnel-client'
} > "$ROOT/dist/TUNNEL_CLIENT_UPSTREAM"

# Backward compatibility for older packaging/scripts that expect a commit-only file.
printf '%s\n' "$COMMIT" > "$ROOT/dist/TUNNEL_CLIENT_COMMIT"

echo "tunnel-client upstream: $REF ($COMMIT)"
sha256sum "$OUT"
