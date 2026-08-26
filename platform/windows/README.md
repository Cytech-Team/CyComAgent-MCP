# CyComAgent Windows Bridge

Windows-native MCP bridge for CyComAgent. It runs on Windows PowerShell 5.1+, binds to loopback only, creates a random local bearer token, and can run as `SYSTEM` through Task Scheduler.

## Tools

- `windows_system_info` — Windows identity, OS, PowerShell and elevation state.
- `windows_exec` — execute PowerShell with bounded timeout/output.
- `windows_file_read` — read UTF-8 or binary files.
- `windows_file_list` — list local files/directories.
- `windows_processes` — list local processes.
- `windows_services` — list/start/stop/restart/query services.

## Install

Open an elevated PowerShell prompt from this directory:

```powershell
Set-ExecutionPolicy -Scope Process Bypass
.\Install-CyComAgent-Windows-Bridge.ps1
```

Default endpoint:

```text
http://127.0.0.1:7332/mcp
```

The installer registers `CyComAgent Windows Bridge` as an AtStartup Scheduled Task under `SYSTEM`, with restart-on-failure enabled. Runtime data is stored under:

```text
C:\ProgramData\CyComAgent\WindowsBridge
```

`bridge.token` and `bridge.log` are runtime files and must never be committed.

## Authentication

All control endpoints except `/health` require either:

```text
X-CyCom-Token: <token>
```

or:

```text
Authorization: Bearer <token>
```

The bridge intentionally accepts only loopback bind addresses (`127.0.0.1`, `localhost`, `::1`). Use a trusted tunnel/transport if remote MCP access is needed; do not expose the bridge directly to a LAN or the public Internet.

## Uninstall

Remove only the scheduled task and keep runtime data:

```powershell
.\Uninstall-CyComAgent-Windows-Bridge.ps1
```

Remove the task plus token/log/install directory:

```powershell
.\Uninstall-CyComAgent-Windows-Bridge.ps1 -RemoveData
```

## Security boundary

If installed as `SYSTEM`, `windows_exec` and service-control capabilities have machine-level authority. Treat possession of `bridge.token` as equivalent to privileged local control. Keep the endpoint loopback-only and keep the token private.
