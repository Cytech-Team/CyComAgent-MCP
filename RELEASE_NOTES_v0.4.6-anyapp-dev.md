# v0.4.6-anyapp-dev - Linux Any App / Wayland

This development source update adds the optional `anyapp_*` Linux MCP bridge.
The standalone helper does not require the ChatGPT application to run.

## Included

- Persistent helper transport, native MCP images and structured results.
- Exact uint64 JSON handling for KWin window identifiers.
- Service-friendly desktop environment discovery and helper/session refresh.
- Native KWin move/resize patch, verified resulting geometry, tests and license.
- Standalone helper setup and reproducible pinned source build instructions.

## Requirements and scope

Any App requires a separately installed, compatible helper and an active local
graphical session. KDE Wayland uses KWin scripting, AT-SPI, and the XDG desktop
portal with normal user consent. The generic tools remain usable without it.
See `docs/anyapp-linux.md` for installation and limitations.

No prebuilt helper, GitHub Release, or replacement bootstrap assets are published
by this source update. `scripts/bootstrap.sh` remains pinned to the existing
published release until matching immutable release assets are available.
