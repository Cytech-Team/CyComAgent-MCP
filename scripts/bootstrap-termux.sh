#!/data/data/com.termux/files/usr/bin/sh
set -eu
TMP="${TMPDIR:-${PREFIX:-/data/data/com.termux/files/usr}/tmp}/cycomagent-bootstrap-shared-$$.sh"
trap 'rm -f "$TMP"' EXIT INT TERM
curl -fsSL https://cdn.cytechteam.site/install/cycomagent -o "$TMP"
exec sh "$TMP"
