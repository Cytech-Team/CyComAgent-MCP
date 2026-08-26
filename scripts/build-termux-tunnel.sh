#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUT="$ROOT/dist/tunnel-client-runtime-termux-arm64"
REPO="https://github.com/openai/tunnel-client.git"
COMMIT="${TUNNEL_CLIENT_COMMIT:-4d9e440d70563f4a6e3d807dc4b67177b2330fec}"

command -v git >/dev/null 2>&1 || { echo "git is required" >&2; exit 2; }
command -v go >/dev/null 2>&1 || { echo "go is required" >&2; exit 2; }

WORK=$(mktemp -d "${TMPDIR:-/tmp}/cycom-tunnel-build.XXXXXX")
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT INT TERM

git clone -q "$REPO" "$WORK/tunnel-client"
cd "$WORK/tunnel-client"
git checkout -q "$COMMIT"
ACTUAL=$(git rev-parse HEAD)
[ "$ACTUAL" = "$COMMIT" ] || {
  echo "tunnel-client commit mismatch: expected $COMMIT got $ACTUAL" >&2
  exit 1
}

# Go's pure resolver on Android does not use Termux's $PREFIX/etc/resolv.conf.
# With CGO disabled it can fall back to 127.0.0.1/[::1]:53, where ordinary
# Android devices usually have no DNS server listening. Inject a tiny Termux
# resolver shim that reads the Termux resolver file and dials those DNS servers
# directly. This keeps the binary non-root and avoids depending on Android NDK/cgo.
cat > cmd/client-runtime/termux_dns.go <<'EOF_DNS'
package main

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"time"
)

func init() {
	servers := termuxDNSServers()
	if len(servers) == 0 {
		servers = []string{"1.1.1.1:53", "1.0.0.1:53", "8.8.8.8:53"}
	}
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var last error
			for _, server := range servers {
				d := net.Dialer{Timeout: 3 * time.Second}
				conn, err := d.DialContext(ctx, network, server)
				if err == nil {
					return conn, nil
				}
				last = err
			}
			if last == nil {
				last = errors.New("no DNS servers configured")
			}
			return nil, last
		},
	}
}

func termuxDNSServers() []string {
	prefix := strings.TrimSpace(os.Getenv("PREFIX"))
	if prefix == "" {
		prefix = "/data/data/com.termux/files/usr"
	}
	f, err := os.Open(prefix + "/etc/resolv.conf")
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		ip := net.ParseIP(strings.TrimSpace(fields[1]))
		if ip == nil {
			continue
		}
		out = append(out, net.JoinHostPort(ip.String(), "53"))
		if len(out) >= 3 {
			break
		}
	}
	return out
}
EOF_DNS

mkdir -p "$ROOT/dist"
CGO_ENABLED=0 GOOS=android GOARCH=arm64 GOTOOLCHAIN=auto \
  go build -trimpath -ldflags='-s -w' \
  -o "$OUT" ./cmd/client-runtime
chmod 0755 "$OUT"

printf '%s\n' "$COMMIT" > "$ROOT/dist/TUNNEL_CLIENT_COMMIT"
sha256sum "$OUT"
