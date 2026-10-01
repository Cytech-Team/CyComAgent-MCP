#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
PUBLISH_DIR=${NEA_PUBLISH_DIR:-}

[ -n "$PUBLISH_DIR" ] || {
  echo "NEA_PUBLISH_DIR is required" >&2
  exit 2
}

mkdir -p "$PUBLISH_DIR"

"$ROOT/scripts/build-termux-tunnel.sh"

META="$ROOT/dist/TUNNEL_CLIENT_UPSTREAM"
BIN="$ROOT/dist/tunnel-client-runtime-termux-arm64"
[ -s "$META" ] || { echo "missing $META" >&2; exit 1; }
[ -x "$BIN" ] || { echo "missing $BIN" >&2; exit 1; }

UPSTREAM_REF=$(sed -n 's/^ref=//p' "$META" | head -n 1)
UPSTREAM_COMMIT=$(sed -n 's/^commit=//p' "$META" | head -n 1)
PROFILE=$(sed -n 's/^nea_profile=//p' "$META" | head -n 1)
SHA256=$(sha256sum "$BIN" | awk '{print $1}')

[ -n "$UPSTREAM_REF" ] || { echo "missing upstream ref" >&2; exit 1; }
[ -n "$UPSTREAM_COMMIT" ] || { echo "missing upstream commit" >&2; exit 1; }
[ -n "$PROFILE" ] || { echo "missing NEA profile" >&2; exit 1; }

SAFE_REF=$(printf '%s' "$UPSTREAM_REF" | tr '/: ' '---')
ASSET="tunnel-client-runtime-termux-arm64-${SAFE_REF}-$(printf '%s' "$UPSTREAM_COMMIT" | cut -c1-8)-$(printf '%s' "$SHA256" | cut -c1-8)"

TMP_ASSET="$PUBLISH_DIR/.${ASSET}.tmp.$$"
cp "$BIN" "$TMP_ASSET"
chmod 0755 "$TMP_ASSET"
mv "$TMP_ASSET" "$PUBLISH_DIR/$ASSET"

TMP_MANIFEST="$PUBLISH_DIR/.current.env.tmp.$$"
cat > "$TMP_MANIFEST" <<EOF_MANIFEST
NEA_COMPONENT=tunnel-client
NEA_PROFILE=$PROFILE
UPSTREAM_REF=$UPSTREAM_REF
UPSTREAM_COMMIT=$UPSTREAM_COMMIT
ASSET=$ASSET
SHA256=$SHA256
EOF_MANIFEST
chmod 0644 "$TMP_MANIFEST"
mv "$TMP_MANIFEST" "$PUBLISH_DIR/current.env"

printf '%s  %s\n' "$SHA256" "$ASSET" > "$PUBLISH_DIR/SHA256SUMS"
echo "Published NEA tunnel runtime: $ASSET"
