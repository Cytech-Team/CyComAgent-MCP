# Changelog

## 0.4.5-macos-dev

- Added experimental macOS support to the shared Go core for Darwin arm64 and amd64.
- Added user-level launchd/LaunchAgent installation and `launchctl` service management.
- Added built-in `screencapture` plus AppleScript/optional `cliclick` desktop adapters.
- Restricted the local root broker capability report to Linux, avoiding false privilege support on macOS.
- Extended the shared curl bootstrap and release pipeline to macOS with SHA-256 verification.
- Added macOS CI validation while keeping physical-Mac runtime status explicitly experimental.

## 0.4.4-multiplatform-dev

- Added shared curl bootstrap installation for Linux amd64 and Android/Termux ARM64.
- Added curl + PowerShell bootstrap installation for Windows 10/11.
- Added checksum verification against immutable GitHub Release assets on every supported platform.
- Added Windows safe reinstall/update handling and reproducible Windows release packaging.

## 0.4.3-multiplatform-dev

- Added the Windows-native PowerShell MCP bridge to the main repository.
- Added Windows install/uninstall scripts using a loopback-only SYSTEM Scheduled Task.
- Added Windows CI syntax validation and documented its six native tools/security boundary.
- Kept runtime token/log files outside source control.

## 0.4.2-termux-dev

- Fixed bundled OpenAI tunnel-client DNS resolution on Android/Termux ARM64 when built without cgo.
- Tunnel runtime now reads Termux `$PREFIX/etc/resolv.conf` instead of falling back to localhost DNS (`127.0.0.1` / `::1`).
- Kept the Termux tunnel runtime non-root and Android-NDK-free.

## 0.4.0-termux-dev

- Added first-class Android/Termux arm64 core build.
- Added platform-aware shell/socket paths and Termux runit service control.
- Added Termux install/smoke scripts and CI artifact build.
- Declared Android device root / `su` integration unsupported and not planned, while keeping remote SSH sudo and user-space sudo (for example inside proot) allowed.
- Added the Termux `mobile_assistant` profile with semantic `assistant_*` tools for voice, status, notification, location, clipboard and phone actions.
- Added explicit `allow_sensitive_android` policy gating; sensitive Android capabilities default to disabled.
- Added optional Termux:Boot autostart and wake-lock helper.
- Added allowlisted non-root Termux:API bridge under normal Android permissions.

## v0.3.0-fullpower-dev

- Added durable multi-machine `target_*` primitives with stateless OpenSSH execution and scp copy.
- Added explicit persistent `state_*` primitives.
- Added central policy engine and structured audit log.
- Added capability adapter scoring.
- Added synchronous process stdin.
- Hardened root broker with optional peer executable verification.
- Hardened external plugins with process-group timeout and bounded output.
- Expanded Full Power smoke/release gate.

## 0.2.0-dev

- Removed mandatory external Go SDK dependency from runtime core.
- Added direct stateless MCP HTTP implementation for tool runtime operations.
- Added `server/discover` and modern 2026-07-28 metadata support.
- Added legacy initialize/ping support and GET/SSE probe compatibility.
- Added disk-backed persistent background jobs and log handles.
- Added optional Linux root privilege broker using Unix `SO_PEERCRED`.
- Added binary-aware filesystem reads and atomic patching.
- Added desktop capture/input capability adapters.
- Added external JSON-over-stdio tool plugins.
- Added health/readiness/Prometheus-style metrics endpoints.
- Added HTTP token and non-loopback bind protection.
- Added separate runtime/root/tunnel systemd units and watchdog timer.
- Added smoke, doctor and chaos-test scripts.
- Added protocol/registry/job/filesystem tests and static release builds.
- Added process-group cancellation so command timeouts do not leave child processes orphaned.
- Added live plugin reload and persistent-job restart recovery verification.
- Hardened systemd failure isolation so runtime and tunnel recover independently.
- Added MCP HTTP Origin validation and constant-time token comparison.
