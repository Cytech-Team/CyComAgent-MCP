#!/bin/sh
set -eu

[ "$(uname -s 2>/dev/null || true)" = "Darwin" ] || { echo "install-launchd.sh: macOS is required" >&2; exit 1; }

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
: "${HOME:?HOME is required}"

LABEL="com.cytech.cycomagent"
APP_DIR="$HOME/Library/Application Support/CyComAgent"
BIN_DIR="$APP_DIR/bin"
STATE_DIR="$APP_DIR/state"
LOG_DIR="$HOME/Library/Logs/CyComAgent"
PLIST_DIR="$HOME/Library/LaunchAgents"
PLIST="$PLIST_DIR/$LABEL.plist"
BIN="$BIN_DIR/cycomagent"
UID_NUM=$(id -u)
DOMAIN="gui/$UID_NUM"

[ -x "$ROOT/dist/cycomagent" ] || { echo "release is missing dist/cycomagent" >&2; exit 1; }
command -v launchctl >/dev/null 2>&1 || { echo "launchctl is required" >&2; exit 1; }

mkdir -p "$BIN_DIR" "$STATE_DIR" "$LOG_DIR" "$PLIST_DIR"
chmod 700 "$APP_DIR" "$STATE_DIR" 2>/dev/null || true
install -m 0755 "$ROOT/dist/cycomagent" "$BIN"

xml_escape() {
  printf '%s' "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' -e 's/"/\&quot;/g'
}
BIN_XML=$(xml_escape "$BIN")
STATE_XML=$(xml_escape "$STATE_DIR")
HOME_XML=$(xml_escape "$HOME")
OUT_XML=$(xml_escape "$LOG_DIR/stdout.log")
ERR_XML=$(xml_escape "$LOG_DIR/stderr.log")

cat > "$PLIST" <<EOF_PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>$BIN_XML</string>
    <string>--addr</string>
    <string>127.0.0.1:7331</string>
    <string>--state-dir</string>
    <string>$STATE_XML</string>
  </array>
  <key>WorkingDirectory</key>
  <string>$HOME_XML</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ProcessType</key>
  <string>Background</string>
  <key>ThrottleInterval</key>
  <integer>5</integer>
  <key>StandardOutPath</key>
  <string>$OUT_XML</string>
  <key>StandardErrorPath</key>
  <string>$ERR_XML</string>
</dict>
</plist>
EOF_PLIST
chmod 600 "$PLIST"

# Replace an older registration cleanly. bootout may fail if the label is not loaded.
launchctl bootout "$DOMAIN/$LABEL" >/dev/null 2>&1 || true
launchctl enable "$DOMAIN/$LABEL" >/dev/null 2>&1 || true
launchctl bootstrap "$DOMAIN" "$PLIST"

printf '\nCyComAgent macOS experimental runtime installed.\n'
printf 'LaunchAgent: %s\n' "$LABEL"
printf 'MCP:         http://127.0.0.1:7331/mcp\n'
printf 'Health:      http://127.0.0.1:7331/health\n'
printf 'State:       %s\n' "$STATE_DIR"
printf 'Logs:        %s\n' "$LOG_DIR"
printf '\nDesktop capture/input can require Screen Recording, Accessibility, and Automation permissions in macOS Settings.\n'
printf 'Local root-broker execution is not supported on macOS.\n'
