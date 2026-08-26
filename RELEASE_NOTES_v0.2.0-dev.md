# CyComAgent-MCP v0.2.0-dev

This development release turns the original prototype into a Linux-first, direct-tunnel AI computer runtime.

## Highlights

- 27 built-in generic tools across filesystem, process, persistent jobs, system/service, network, desktop, plugins and capability inspection.
- Direct stateless HTTP MCP runtime on loopback; tunnel-client is a separate long-lived service.
- Modern MCP `2026-07-28` tool-runtime surface plus selected legacy compatibility.
- Legacy GET/SSE probe compatibility so older probes receive an event stream instead of an immediate 404.
- Disk-backed long-running jobs that remain addressable across MCP/tunnel/runtime restarts.
- Linux root privilege broker over a Unix socket with `SO_PEERCRED` UID verification.
- Process-group cancellation so timeout/cancel does not intentionally leave command children orphaned.
- Atomic text patching and binary-safe file reads.
- Desktop capture/input adapter discovery without hard-coding one desktop environment.
- External JSON-over-stdio capability plugins with live reload.
- `/livez`, `/readyz`, `/health`, `/metrics`.
- Streamable HTTP Origin validation for DNS-rebinding defense and constant-time local token comparison.
- systemd runtime/root/tunnel/watchdog units designed to start before graphical login.
- smoke, doctor, chaos, persistent-job recovery tests and CI configuration.
- Static Linux amd64 binaries with no third-party Go runtime dependencies in v0.2 core.

## Release validation performed

- `gofmt`
- `go test ./...`
- `go vet ./...`
- `bash -n scripts/*.sh`
- static `CGO_ENABLED=0` Linux amd64 build
- modern `server/discover`, `tools/list`, `tools/call` live HTTP smoke
- legacy GET/SSE probe live smoke
- live privileged `process_exec` through the Unix root broker
- persistent background job survives hard runtime kill/restart and remains observable
- systemd unit verification with install-time executables staged

## Important limitations

- Linux is the fully implemented host target in v0.2. Portability adapters are roadmap work.
- CyComAgent implements the MCP surface required by this tool runtime; it does not claim to be a full general-purpose MCP SDK. See `docs/MCP_COMPATIBILITY.md`.
- The legacy GET/SSE endpoint is probe compatibility, not a complete old stateful SSE transport.
- If the runtime dies while a recovered background process later exits, the new runtime can detect that it ended and preserve its log, but cannot recover the original OS wait status. Such jobs use exit code `-1` with status `exited-after-recovery`.
- Desktop control depends on a compatible installed adapter and a usable graphical session.
- `process_exec` and the root broker are intentionally powerful. Treat MCP/tunnel credentials and local machine access as administrator-grade authority.

## Deployment model

```text
AI client -> OpenAI tunnel -> tunnel-client.service
                              -> 127.0.0.1:7331/mcp
                              -> cycomagent.service
                              -> local OS
```

`tunnel-client` does not spawn CyComAgent. A tunnel reconnect therefore does not own or erase runtime state.
