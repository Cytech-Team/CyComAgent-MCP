# CyComAgent-MCP on Termux / Android ARM64

Target: standard non-root Termux on Android ARM64/aarch64. Rooted-device support is intentionally not supported or planned.

## One-command install

Public bootstrap:

```sh
curl -fsSL https://cdn.cytechteam.site/install/cycomagent | sh
```

The bootstrap auto-detects Termux/architecture, installs required Termux packages, verifies the release SHA-256, installs CyComAgent plus the bundled OpenAI tunnel runtime, initializes `termux-services`, opens the permission preset wizard, securely asks for Tunnel ID/API key, enables persistent runit services, and installs the Termux:Boot script with wake-lock by default.

Permission presets:

- `Assistant` — assistant basics, network/status, no generic shell/files, sensitive Android APIs off.
- `Developer` — Assistant plus local files/shell/jobs; sensitive Android APIs off.
- `Power` — all tools except sensitive Android APIs.
- `Everything` — all tools including sensitive Android APIs, subject to Android runtime permissions.
- `Read-only` — read/inspect operations only.
- `Custom` — choose capability groups interactively.

Reconfigure later with:

```sh
cycomagent-setup
```

Services:

- `cycomagent` -> `127.0.0.1:7331/mcp`
- `cycomagent-tunnel` -> Secure MCP Tunnel -> local MCP
- tunnel health -> `127.0.0.1:7332/healthz` and `/readyz`

Secrets are stored separately under `~/.config/cycomagent/` with mode `0600`; the runtime API key is passed to tunnel-client as a file reference rather than a command-line secret.

## Android limitations

The installer cannot silently install Android add-on APKs or grant Android permissions/battery exemptions. For true reboot persistence, install the matching Termux:Boot add-on from the same source/signing family as Termux and launch it once. For assistant APIs, install the matching Termux:API add-on app and grant only the Android permissions you actually want. Set Termux and Termux:Boot battery mode to Unrestricted for the strongest non-root persistence.
