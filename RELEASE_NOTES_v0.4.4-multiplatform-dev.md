# CyComAgent-MCP v0.4.4-multiplatform-dev

This development release adds one-command curl bootstrap installers across every currently supported platform family.

## Install

Linux (amd64, systemd):

```sh
curl -fsSL https://cdn.cytechteam.site/install/cycomagent | sh
```

Android / Termux (ARM64):

```sh
curl -fsSL https://cdn.cytechteam.site/install/cycomagent | sh
```

Windows 10/11:

```powershell
curl.exe -fsSL https://cdn.cytechteam.site/install/cycomagent.ps1 | powershell -NoProfile -ExecutionPolicy Bypass -Command -
```

## Changes

- Added a shared POSIX bootstrap that detects Linux versus Android/Termux automatically.
- Added a Windows curl bootstrap that downloads the Windows package, verifies SHA-256, and requests UAC elevation only for the installation step.
- All bootstrap installers download immutable GitHub Release assets and verify their published SHA-256 checksum before installation.
- Updated the Windows installer to stop and replace an existing startup task safely during upgrades.
- Added reproducible `release-windows` / `release-all` Makefile targets and platform-specific checksum files.
- Kept the existing Termux bootstrap entry point as a compatibility shim to the shared installer.

macOS remains unsupported.
