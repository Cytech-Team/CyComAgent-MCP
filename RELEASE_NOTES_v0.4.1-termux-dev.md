# CyComAgent-MCP v0.4.1-termux-dev

This development release turns the Termux build into a one-command persistent phone-node installer.

## Highlights

- One-command bootstrap: `curl -fsSL https://cdn.cytechteam.site/install/cycomagent | sh`.
- ARM64/aarch64 Termux auto-detection and prerequisite installation.
- Bundled OpenAI `tunnel-client` runtime (Apache-2.0, pinned upstream commit) for Secure MCP Tunnel.
- Interactive permission presets: Assistant, Developer, Power, Everything, Read-only, and Custom.
- Secure Tunnel ID/runtime API-key setup; secrets are stored mode `0600` and the API key is consumed via a file reference.
- Separate runit services for `cycomagent` and `cycomagent-tunnel`, with independent restart behavior.
- Current-shell `termux-services` initialization, removing the prior need to restart Termux after installation.
- Termux:Boot autostart for both runtime and tunnel, with optional/default one-click wake-lock persistence.
- Local health endpoints for both CyComAgent and tunnel runtime.
- `cycomagent-setup` command for changing permissions or tunnel credentials later.

## Security model

Android device root, `su`, Magisk, and root-broker support remain intentionally unsupported and not planned. Permission presets are CyComAgent policy controls and do not bypass Android runtime permissions. Android add-on APK installation, permission grants, and battery-optimization exemptions still require Android/user approval.
