# CyComAgent-MCP v0.4.2-termux-dev

Termux reliability hotfix focused on Secure MCP Tunnel DNS resolution.

## Fixed

- Fixed OpenAI tunnel-client on Android/Termux ARM64 resolving public hosts via the Go runtime fallback DNS servers `127.0.0.1:53` / `[::1]:53` when the binary is built with `CGO_ENABLED=0`.
- The bundled tunnel runtime now reads Termux `$PREFIX/etc/resolv.conf` and dials those configured DNS servers directly through a custom Go resolver.
- Falls back to public resolvers only when no valid Termux resolver entries are available.
- Keeps the tunnel binary non-root and does not require the Android NDK or a cgo runtime.

## Why

Android does not expose DNS configuration to a pure-Go resolver in the same way as normal Linux. A Termux shell could resolve `api.openai.com` successfully while the bundled Go tunnel runtime repeatedly attempted `[::1]:53` and failed. This release aligns the tunnel runtime with Termux DNS configuration.

## Upgrade

Re-run the stable one-command installer:

```sh
curl -fsSL https://cdn.cytechteam.site/install/cycomagent | sh
```

Existing policy and tunnel credentials can be reused during setup.
