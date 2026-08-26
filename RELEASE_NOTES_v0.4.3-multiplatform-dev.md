# CyComAgent-MCP v0.4.3-multiplatform-dev

This development release consolidates the public CyComAgent source tree across Linux, Android/Termux, and Windows.

## Windows

The Windows-native bridge that powers the live Desktop integration is now included under `platform/windows`.

It provides six native MCP tools:

- `windows_system_info`
- `windows_exec`
- `windows_file_read`
- `windows_file_list`
- `windows_processes`
- `windows_services`

The bridge is PowerShell-native, loopback-only, token-authenticated, and can be installed as a boot-started `SYSTEM` Scheduled Task. Installer and uninstaller scripts are included.

Windows support is development-stage and does not yet have feature parity with the Linux core.

## Linux

The Linux Full Power runtime remains unchanged in capability: filesystem/process/job/service/network/state/SSH target/policy/audit/plugin primitives plus the optional root broker.

## Android / Termux

The non-root `mobile_assistant` runtime remains supported on Termux arm64 with Termux:API semantic assistant actions. Rooted Android remains unsupported and not planned.

## Validation

- `go test ./...`
- `go vet ./...`
- Linux static build in GitHub Actions
- Termux arm64 build in GitHub Actions
- Windows PowerShell parser validation on `windows-latest`

Runtime secrets such as Windows `bridge.token`, logs, tunnel credentials, and local state are excluded from source control.
