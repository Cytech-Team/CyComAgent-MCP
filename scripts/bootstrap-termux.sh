#!/data/data/com.termux/files/usr/bin/sh
set -eu
TMP="${TMPDIR:-${PREFIX:-/data/data/com.termux/files/usr}/tmp}/cycomagent-bootstrap-shared-$$.sh"
trap 'rm -f "$TMP"' EXIT INT TERM
curl -fsSL --retry 3 --connect-timeout 15 https://raw.githubusercontent.com/Cytech-Team/CyComAgent-MCP/main/scripts/bootstrap.sh -o "$TMP"
exec sh "$TMP"
