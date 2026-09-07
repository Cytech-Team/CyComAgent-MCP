# Linux Any App / KDE Wayland

CyComAgent can expose a separately installed `codex-computer-use-linux` helper
as `anyapp_*` MCP tools. This optional bridge does not launch or modify the
ChatGPT application. The helper's tool schemas, annotations, image content,
and structured responses are forwarded; the generic runtime stays available
when the helper is absent or cannot complete discovery.

## Backend selection

The first executable found is used, in this order:

1. `CYCOM_ANYAPP_BACKEND`, when configured.
2. `/usr/local/lib/cycomagent/computer-use/codex-computer-use-linux`.
3. `/opt/codex-desktop/resources/plugins/openai-bundled/plugins/computer-use/bin/codex-computer-use-linux`.
4. `/usr/lib/chatgpt/resources/plugins/openai-bundled/plugins/computer-use/bin/codex-computer-use-linux`.
5. `codex-computer-use-linux` on `PATH`.

Install only a helper you trust: it executes locally with the runtime user's
permissions. For an explicitly selected standalone installation:

```sh
export CYCOM_ANYAPP_BACKEND=/usr/local/lib/cycomagent/computer-use/codex-computer-use-linux
./dist/cycomagent -mode=stdio
```

Set the variable in your actual service environment for supervised operation.
Restart CyComAgent after first installing the helper so it can discover and
register the tools. After registration, the bridge recreates its helper process
when selected desktop-session variables or the helper file fingerprint change.
Calls are serialized; a canceled or broken call stops the helper rather than
automatically replaying potentially state-changing input.

## KDE Wayland requirements

Use an active desktop session owned by the same user as CyComAgent:

- KWin's `org.kde.KWin` D-Bus scripting service for listing, focusing, and
  (with the patch below) moving/resizing native windows.
- A working XDG desktop portal with RemoteDesktop and screenshot support,
  plus the normal user permission/consent flow.
- The AT-SPI accessibility bus for semantic UI state and actions.

On the verified Plasma Wayland setup, the helper uses the portal for input and
screenshots and KDE clipboard integration for layout-safe literal text.
`xdotool`, `ydotool`, `kdotool`, and permissive `/dev/uinput` access are not
required for that route. Other compositors/backends can have different needs.
Do not weaken device permissions or bypass portal consent to enable automation.

CyComAgent attempts to discover missing session variables when started before
login, including per-user Wayland/runtime paths and readable same-user desktop
process environments. Multiple simultaneous sessions, inaccessible session
buses, or stale inherited variables may still require explicit service settings.
Never commit private session configuration, tokens, or logs to this repository.

## Build the pinned native KWin patch

The geometry fix is implemented in the external helper, not in the Go bridge.
The source patch and upstream MIT license are included under `packaging/anyapp/`.
Its base is the community `ilysenko/codex-desktop-linux` repository at commit
`758b4ce74301c387c6006698921384d4ca1dc47d`.

From a CyComAgent-MCP checkout, with Git, a compatible Rust toolchain, a native
C build toolchain, and the helper's Linux development dependencies installed:

```sh
set -eu
CYCOM_SOURCE=$(pwd)
HELPER_WORK=$(mktemp -d)
HELPER_SOURCE="$HELPER_WORK/codex-desktop-linux"
BASE=$(cat "$CYCOM_SOURCE/packaging/anyapp/UPSTREAM_COMMIT")
PATCH="$CYCOM_SOURCE/packaging/anyapp/kwin-wayland-geometry.patch"

git clone https://github.com/ilysenko/codex-desktop-linux.git "$HELPER_SOURCE"
git -C "$HELPER_SOURCE" checkout --detach "$BASE"
git -C "$HELPER_SOURCE" apply --check "$PATCH"
git -C "$HELPER_SOURCE" apply "$PATCH"

cargo test --locked --manifest-path "$HELPER_SOURCE/Cargo.toml" \
  -p codex-computer-use-linux --lib -j 2
cargo build --locked --manifest-path "$HELPER_SOURCE/Cargo.toml" \
  -p codex-computer-use-linux --bin codex-computer-use-linux --release -j 2
```

This builds only the helper, not the desktop application. Inspect the resulting
binary and keep a backup of any previous helper before installing it. With
appropriate administrator permission, install atomically at the standalone path:

```sh
sudo install -d -m 755 /usr/local/lib/cycomagent/computer-use
sudo install -m 755 \
  "$HELPER_SOURCE/target/release/codex-computer-use-linux" \
  /usr/local/lib/cycomagent/computer-use/codex-computer-use-linux.new
sudo mv /usr/local/lib/cycomagent/computer-use/codex-computer-use-linux.new \
  /usr/local/lib/cycomagent/computer-use/codex-computer-use-linux
sudo install -m 644 "$HELPER_SOURCE/LICENSE" \
  /usr/local/lib/cycomagent/computer-use/LICENSE.codex-desktop-linux
```

The pinned patch clones Qt rectangle values into a writable object before
assigning `frameGeometry`. It supports move/resize, restores maximized windows,
rejects fullscreen or unsupported operations, and waits for a fresh compositor
query to verify the requested result. Window rules or application minimum sizes
may constrain geometry; those cases return an error instead of false success.

The helper is not bundled as a prebuilt binary in this source update. Merely
building the Go runtime does not apply the Rust/KWin patch to an existing helper.

## Verification and limitations

Call `anyapp_doctor`, then `anyapp_get_app_state` and `anyapp_list_windows`.
Use a scratch document to test explicit window focus, clicks, literal English
and Thai text, save shortcuts, screenshot, move, resize, and resize from a
maximized state. Prefer a window ID, or combine PID and title; an application
identifier alone is not enough to distinguish multiple windows of the same app.

The Go bridge preserves uint64 IDs through JSON without float64 rounding.
A separate client that parses JSON into JavaScript numbers can still lose
precision; use selectors such as PID/title when needed.

Verification on KDE Wayland covered exact saved English/Thai content, targeted
click/double-click, screenshots, position and size readback, and unmaximize/resize.
The patched helper passed 284 Rust library tests, including six geometry tests.
These results do not certify all applications or every compositor configuration.

KWrite may expose a focused accessibility node as a non-editable `filler` even
when input lands correctly. Inspect the actual document/UI instead of treating
that warning alone as proof of failure. Some Electron applications expose only
a limited accessibility tree unless accessibility is enabled at launch. This
integration does not alter their application packages or launch configuration.

For bridge checks, run `go test ./...` and `go vet ./...`. The normal GitHub CI
checks the Go core and platform build paths; it does not provide a live KDE
Wayland session or automatically build the separately pinned Rust helper.
