# CyComAgent-MCP

**Give AI capabilities, not workflows.**

CyComAgent-MCP is an AI-native computer runtime that exposes a real machine — and optional remote machines — as a compact set of composable MCP primitives. The model supplies the reasoning; CyComAgent supplies filesystem, process, persistent job, service, network, desktop, durable state, multi-machine target, policy, audit, plugin, and privileged-execution capabilities.

`v0.4.7-session-routing-dev` adds Linux execution-context routing so GUI/session-sensitive commands can be born inside the active graphical login session instead of inheriting the CyComAgent system-service cgroup. The existing optional Linux Any App bridge, macOS, Termux, and Windows paths remain available. This is the source development version; the curl bootstrap continues to install the latest published release until new release assets are published.

## What “Full Power” means

Full Power does **not** mean hard-coding hundreds of application workflows. It means the runtime gives an AI enough generic primitives to build its own workflow from observations:

```text
AI client
   │
   ▼
OpenAI tunnel / direct MCP
   │
   ▼  stateless HTTP
CyComAgent-MCP
   │
   ├── filesystem
   ├── processes + stdin
   ├── persistent jobs
   ├── service/network/system
   ├── desktop capture/input
   ├── durable explicit state
   ├── local + SSH machine targets
   ├── external capability plugins
   ├── policy + audit
   └── root broker (optional)
   │
   ▼
Real computers
```

There is no required `primary SSH session`. Remote targets are durable configuration; SSH is opened per operation. Restarting an MCP/tunnel process therefore does not erase the machine definition.

## Design contracts

1. **Direct tunnel:** `tunnel-client -> http://127.0.0.1:7331/mcp`.
2. **Boot-ready:** system services can start before graphical login.
3. **Session-independent:** MCP protocol state is not used as application state.
4. **Self-healing:** ordinary runtime/tunnel failures are independently restarted.
5. **Generic primitives:** no built-in `deploy_x`, `fix_y`, or app-specific workflow.
6. **Durable handles:** long-running jobs and explicit state live on disk.
7. **Multi-machine without sticky SSH:** target definitions survive process restarts.
8. **Full-power by policy, not by accident:** default policy is `full`, but `safe` and `readonly` modes are built in.
9. **Observable:** every tool call is audit logged without silently copying its arguments.
10. **Extensible:** JSON-over-stdio plugins can add tools without modifying core.

## MCP compatibility

CyComAgent implements the current MCP `2026-07-28` stateless tool-server shape, including `server/discover`, per-request protocol metadata, deterministic `tools/list`, and `tools/call`. It also retains legacy initialization and a harmless GET/SSE probe compatibility endpoint for older clients/tunnels.

Application state is explicit (`job_*`, `state_*`, `target_*`) rather than hidden in MCP transport sessions.

## 42 built-in generic tools

### Filesystem — 8

`fs_list`, `fs_read`, `fs_write`, `fs_patch`, `fs_move`, `fs_remove`, `fs_search`, `fs_stat`

### Process — 4

`process_exec`, `process_spawn`, `process_inspect`, `process_signal`

`process_exec` is the universal escape hatch and accepts optional UTF-8 `stdin`. Commands run in a process group so timeout/cancellation kills the group instead of leaving descendants behind. On Linux it also accepts `execution_context=auto|service|user|desktop|system`: `desktop`/`user` route through the active login-session bridge, `service` stays in the CyComAgent service, and `system` requires `privileged=true` so the existing policy/root-broker boundary remains authoritative. `auto` is conservative and only routes a small set of known session-sensitive commands automatically; callers should explicitly request `desktop` for arbitrary GUI programs.

### Persistent jobs — 5

`job_list`, `job_get`, `job_tail`, `job_signal`, `job_prune`

`process_spawn` persists job metadata/logs on disk. It accepts the same non-root execution contexts; desktop/user jobs are launched by the login-session bridge while the normal job store still owns their PID, log and signalling metadata. If the runtime dies while a process continues, a new runtime instance rediscovers the PID metadata.

### System / network / capability — 8

`system_info`, `system_env`, `service_control`, `network_request`, `network_tcp`, `network_resolve`, `capabilities_list`, `plugins_reload`

`capabilities_list` includes detected binaries, adapter availability/scores, policy, plugins, root broker, targets, and desktop-session-bridge status.

### Linux desktop-session bridge

The system runtime intentionally remains a boot-time service, but GUI programs, Polkit actions, desktop portals, notifications, keyrings and compositor/clipboard commands sometimes must be *born* in the active graphical login session. Copying `DISPLAY`, `WAYLAND_DISPLAY` or the session D-Bus address into a system service does not change its logind/systemd session cgroup, so it is not sufficient for those cases. The bridge refuses to start unless its own cgroup contains a real `session-N.scope`, preventing a copied environment from being mistaken for session identity.

`cycomagent --session-bridge` is a small per-login-session Unix-socket helper. The system installer places an XDG autostart entry at `/etc/xdg/autostart/cycomagent-session-bridge.desktop`; desktop environments that honor XDG autostart *and preserve the login-session cgroup* can launch it automatically. If an environment delegates XDG autostart applications into `user@UID.service`, or on minimal compositors such as Labwc, start the same command from the compositor/session autostart file instead. The socket is private to the user and verifies the connecting UID/PID with `SO_PEERCRED`, then requires the peer executable to resolve to the same CyComAgent binary as the bridge.

When the bridge is absent, service/headless execution keeps working normally. Explicit `desktop`/`user` execution returns a clear unavailable error instead of silently falling back into the wrong cgroup.

### Desktop — 2

`desktop_capture`, `desktop_input`

Adapters are selected from available host tools such as `grim`, `spectacle`, `ydotool`, `wtype`, or `xdotool` rather than hard-coding a desktop environment.

### Optional Linux Any App tools

When a compatible `codex-computer-use-linux` helper is installed, CyComAgent
registers its MCP tools under `anyapp_*` in addition to the generic tools above.
Use `anyapp_get_app_state` first, then semantic or explicitly targeted actions.
KDE Wayland uses the desktop portal for input/screenshots and KWin for window
control; the patched helper also supports native move/resize without `xdotool`.
No helper means no extra tools; the generic runtime remains available.

See [Linux Any App setup](docs/anyapp-linux.md) for the standalone helper,
permissions, pinned KWin patch, build commands, verification, and limitations.

### Durable explicit state — 4

`state_get`, `state_put`, `state_delete`, `state_list`

This lets the AI/application mint visible durable handles or workspace state without smuggling state into an MCP session.

### Machine targets — 7

`target_list`, `target_get`, `target_upsert`, `target_remove`, `target_probe`, `target_exec`, `target_copy`

The implicit `local` target always exists. Remote targets currently use the system OpenSSH client and `scp`:

```json
{
  "name": "home",
  "transport": "ssh",
  "host": "namkrub-home-server",
  "port": 22,
  "user": "namkrub-home-server",
  "identity_file": "/home/user/.ssh/id_ed25519",
  "enabled": true
}
```

Passwords are intentionally not stored. Authentication comes from OpenSSH config, ssh-agent, or an identity file. Remote privileged execution uses `sudo -n` on the target, so unattended root requires an appropriate remote sudo policy.

### Governance / observability — 4

`audit_tail`, `audit_prune`, `policy_get`, `policy_reload`

Every registry tool call flows through central policy and audit interceptors.

## Policy modes

The runtime creates `$STATE_DIR/policy.json` on first start. Default:

```json
{
  "mode": "full",
  "allow_privileged": true,
  "allow_remote_targets": true,
  "allow_external_plugins": true,
  "allow_sensitive_android": false,
  "max_exec_timeout_seconds": 3600
}
```

Modes:

- `full` — maximum machine capability, subject to OS permissions/root broker.
- `safe` — denies a small set of high-impact primitives.
- `readonly` — denies mutating primitives and non-read HTTP requests.

Optional `allow_tools` / `deny_tools` support exact names and glob-style patterns such as `fs_*`.

Policy changes can be loaded with `policy_reload` without restarting the runtime.

## Structured audit

Audit events are JSONL files under `$STATE_DIR/audit/` and record:

- UTC timestamp
- tool name
- SHA-256 of arguments
- duration
- success/failure
- error text when applicable

Arguments themselves are not copied into audit logs by default, reducing accidental secret persistence.

## Privileged execution

The HTTP/MCP parser runs as the configured normal user. Root is isolated in `cycomagent-root` behind a Unix socket.

```text
CyComAgent (user)
      │
      │ Unix socket
      ▼
cycomagent-root (root)
```

The Linux broker validates:

- Unix peer UID (`SO_PEERCRED`)
- peer PID
- optional exact peer executable path (`/proc/PID/exe`)
- socket ownership/mode

The shipped systemd unit requires the peer executable to be `/usr/local/bin/cycomagent` in addition to the configured UID.

This is intentionally equivalent to granting the configured runtime broad machine authority while the broker is enabled. See `SECURITY.md`.

## External capability plugins

Drop JSON manifests into `$STATE_DIR/plugins.d`. Plugins receive arguments as JSON on stdin and return JSON on stdout. They now run with:

- bounded output
- timeout
- process-group isolation
- whole-group kill on timeout
- policy-controlled loading

This keeps application-specific capabilities outside the generic core.

## Platform status

- Linux: supported (Full Power path, systemd/root broker available when configured).
- Android / Termux arm64: development support as a **`mobile_assistant`** runtime (non-root core + semantic Android assistant tools + runit service adapter). Android device root / `su` is **not supported and not planned**. `sudo` on remote SSH targets or inside a non-root userspace/container such as proot remains allowed where that environment provides it.
- Windows 10/11: development support through the bundled Windows-native PowerShell bridge under `platform/windows` (6 native tools, loopback-only MCP, token auth, optional SYSTEM startup task). This bridge is intentionally smaller than the Linux core and is not yet feature-parity.
- macOS 12+ (experimental): portable Go core builds for Apple Silicon arm64 and Intel amd64, user-level `launchd`/LaunchAgent integration, built-in `screencapture`, and AppleScript/optional `cliclick` desktop input adapters. Local root broker is not supported. This path is CI-validated but has not yet been tested on the project owner's physical Mac hardware.

## One-command install

Linux amd64 (systemd), Android/Termux ARM64, and experimental macOS (Apple Silicon arm64 or Intel amd64) use the same command; the installer detects the platform automatically:

```sh
curl -fsSL https://cdn.cytechteam.site/install/cycomagent | sh
```

Windows 10/11 uses the same curl-based flow but pipes into Windows PowerShell because Windows does not ship `sh` by default:

```powershell
curl.exe -fsSL https://cdn.cytechteam.site/install/cycomagent.ps1 | powershell -NoProfile -ExecutionPolicy Bypass -Command -
```

Each bootstrap downloads an immutable GitHub Release asset and validates its published SHA-256 checksum before installation. Windows may show a UAC prompt because the bridge is installed as a SYSTEM startup task. macOS installs as a user LaunchAgent and may later request normal Screen Recording, Accessibility, or Automation permissions when desktop capabilities are used.

## Build

The core still uses only the Go standard library.

```bash
make test
make release
```

Linux release binaries use `CGO_ENABLED=0`. Build the Termux/Android arm64 runtime with `make build-termux`; build both Darwin architectures with `make build-macos`; package macOS with `make release-macos`. The Windows bridge is PowerShell-native and ships from `platform/windows`.

## Run locally

```bash
./dist/cycomagent --addr 127.0.0.1:7331
```

Endpoints:

```text
POST /mcp
GET  /mcp       legacy SSE probe compatibility
GET  /livez
GET  /readyz
GET  /health
GET  /metrics
```

Run the expanded Full Power smoke test:

```bash
./scripts/smoke-mcp.sh
```

It checks modern MCP, tool catalog, stdin execution, local target routing, durable state, policy, audit, DNS-rebinding Origin protection, and legacy SSE probe compatibility.


## Run on Termux (Android arm64)

From an unpacked Termux release:

```sh
pkg install curl
# Optional but recommended for supervision/autorestart:
pkg install termux-services
./scripts/install-termux.sh
```

The installer binds CyComAgent to `127.0.0.1:7331`, stores state under `$HOME/.local/state/cycomagent`, and installs a runit service under `$PREFIX/var/service/cycomagent`. `service_control` maps to `sv`, `sv-enable`, and `sv-disable` on Termux rather than pretending systemd exists.

Validate on-device with:

```sh
./scripts/smoke-termux.sh
```

Optional reboot autostart uses Termux:Boot:

```sh
./scripts/install-termux-boot.sh
# or keep a wake lock for stronger background reliability (higher battery use)
./scripts/install-termux-boot.sh --wake-lock
```

For the Google-Assistant-like phone layer, install the Termux `termux-api` command package plus the matching Termux:API add-on app. Termux:API exposes Android functionality to command-line clients and remains constrained by Android app permissions. CyComAgent uses semantic `assistant_*` tools first and retains an allowlisted `android_api_*` bridge for advanced capabilities. This does not provide device root.

The mobile assistant surface includes device status, speech-to-text, TTS, notifications, location, clipboard, vibration, torch, SMS, calls, camera, microphone recording, contacts, SMS reading, and call-log access where the OS/device supports them. High-sensitivity operations are policy-gated and default to disabled with `allow_sensitive_android: false`.

## Run on macOS (experimental)

The public POSIX bootstrap auto-detects Darwin:

```sh
curl -fsSL https://cdn.cytechteam.site/install/cycomagent | sh
```

Supported release architectures are Apple Silicon `arm64` and Intel `amd64`. The installer places the runtime under `~/Library/Application Support/CyComAgent`, stores state there, writes logs under `~/Library/Logs/CyComAgent`, and registers the user LaunchAgent `com.cytech.cycomagent`. It does not install or emulate the Linux root broker.

Native macOS adapters currently include:

- `launchctl` / LaunchAgent for `service_control`;
- `screencapture` for `desktop_capture`;
- `osascript` / System Events for text and keyboard input;
- optional `cliclick` for pointer movement and clicking.

macOS privacy controls remain authoritative. Screen capture can require **Screen Recording** permission; keyboard/mouse automation can require **Accessibility** and/or **Automation** permission. The macOS path is experimental and CI/build validated, but physical Mac runtime validation is still pending.

## Root / sudo boundary on Termux

CyComAgent deliberately separates **Android device root** from **sudo inside another environment**:

- Android device root, `su`, Magisk/rooted-device integration, and a Termux root broker: **not supported and not planned**.
- Remote SSH `privileged=true`: supported; this uses `sudo -n` on the remote machine and does not root the Android device.
- `sudo` inside `proot-distro`, containers, chroots, or other user-space environments: allowed as an ordinary command when that environment provides it.
- Native Termux commands always remain bounded by Android/Termux app permissions.

## Install as boot services (Linux/systemd)

```bash
sudo ./scripts/install-systemd.sh "$USER"
```

Installed services:

```text
cycomagent-root@USER.service
cycomagent@USER.service
cycomagent-watchdog@USER.timer
```

Enable the OpenAI tunnel separately after configuring `/etc/cycomagent/USER-tunnel.env`:

```bash
sudo systemctl enable --now cycomagent-tunnel@USER.service
```

The tunnel points at the already-running runtime using:

```text
MCP_SERVER_URL=http://127.0.0.1:7331/mcp
```

Tunnel and runtime restart independently.

## Failure model

CyComAgent cannot make electricity, hardware, the ISP, SSH servers, or upstream tunnel infrastructure incapable of failing. The engineering contract is instead:

> ordinary component failure must not erase unrelated durable state, and recoverable components should restart without manual reconstruction of hidden sessions.

Chaos/recovery scripts are included under `scripts/`.

## Current limitations

- Linux remains the only Full Power/root-broker-supported local runtime. Termux and macOS run the non-root shared core; Windows currently uses its smaller native PowerShell bridge.
- SSH targets require a system `ssh`; copy requires `scp`.
- SSH passwords are not stored or prompted interactively.
- A job recovered after its original parent runtime died can know that it later exited, but the historical wait status may be unavailable; recovered exit code can therefore be `-1`.
- Desktop capability depends on the host session, OS permissions, and an available adapter. macOS additionally enforces TCC Screen Recording/Accessibility/Automation permissions.
- Full Power is broad host authority; expose the HTTP endpoint only through a trusted local/tunnel path and use policy/token controls where appropriate.

## Philosophy

> **Give AI capabilities, not workflows.**

If an AI can solve a task using `fs_* + process_exec + target_exec + state_* + service_control + network_request`, core does not need a workflow-specific tool for that task.

## License

MIT. See `THIRD_PARTY.md` for inspirations/references.
