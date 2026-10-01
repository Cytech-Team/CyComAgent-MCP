#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
PROFILE=${1:-}
TARGET=${2:-}

[ -n "$PROFILE" ] || {
  echo "usage: $0 <profile> <upstream-tree>" >&2
  exit 2
}
[ -n "$TARGET" ] || {
  echo "usage: $0 <profile> <upstream-tree>" >&2
  exit 2
}
[ -d "$TARGET" ] || {
  echo "NEA target tree not found: $TARGET" >&2
  exit 2
}

PROFILE_DIR="$ROOT/nea/$PROFILE"
[ -d "$PROFILE_DIR" ] || {
  echo "NEA profile not found: $PROFILE" >&2
  exit 2
}

OVERLAY="$PROFILE_DIR/overlay"
PATCHES="$PROFILE_DIR/patches"

if [ -d "$OVERLAY" ]; then
  (
    cd "$OVERLAY"
    find . -type f -print
  ) | while IFS= read -r rel; do
    rel=${rel#./}
    dst_rel=$rel
    case "$dst_rel" in
      *.nea) dst_rel=${dst_rel%.nea} ;;
    esac
    dst="$TARGET/$dst_rel"
    mkdir -p "$(dirname -- "$dst")"
    cp "$OVERLAY/$rel" "$dst"
    echo "NEA overlay: $PROFILE -> $dst_rel"
  done
fi

if [ -d "$PATCHES" ]; then
  for patch in "$PATCHES"/*.patch; do
    [ -f "$patch" ] || continue
    git -C "$TARGET" apply --check "$patch"
    git -C "$TARGET" apply "$patch"
    echo "NEA patch: $PROFILE -> $(basename "$patch")"
  done
fi
