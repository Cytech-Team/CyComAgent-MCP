# CyComAgent-MCP v0.4.5-macos-dev

This prerelease adds experimental macOS support without forking the core runtime.

## macOS

- Darwin `arm64` and `amd64` builds from the same Go core as Linux.
- User LaunchAgent install under `~/Library` using label `com.cytech.cycomagent`.
- `service_control` maps to the user `launchd` domain through `launchctl`.
- `desktop_capture` uses built-in `screencapture`.
- Text/key desktop input can use `osascript` + System Events; pointer input can use optional `cliclick`.
- The local privilege/root broker remains Linux-only.
- macOS privacy/TCC permissions remain authoritative for screen and UI automation.

## Install

Linux, Termux and macOS share the POSIX bootstrap:

```sh
curl -fsSL https://cdn.cytechteam.site/install/cycomagent | sh
```

Windows continues to use the curl + PowerShell bootstrap documented in the README. All release archives are verified against published SHA-256 checksum files before installation.

## Validation status

The Darwin binaries are cross-built for both architectures and the repository CI includes a macOS runner for Go tests/build validation. Desktop/TCC behavior and the full LaunchAgent lifecycle have not yet been validated on the project owner's physical Mac hardware, so macOS remains experimental.
