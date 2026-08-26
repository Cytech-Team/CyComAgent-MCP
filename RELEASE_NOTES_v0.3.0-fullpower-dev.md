# CyComAgent-MCP v0.3.0-fullpower-dev

This release turns the v0.2 runtime foundation into a Full Power multi-machine execution runtime while preserving the project rule: **give AI capabilities, not workflows**.

## Major additions

- 42 built-in generic MCP tools.
- Durable `target_*` registry with implicit local target and stateless-per-operation OpenSSH remote execution.
- `target_copy` over system `scp`.
- Durable `state_*` key/value primitives for explicit cross-call/application state.
- Central policy engine with `full`, `safe`, and `readonly` modes.
- Central structured audit logging with argument SHA-256 rather than raw argument persistence.
- Capability adapter scoring/diagnostics.
- `process_exec` stdin support.
- Root broker hardening with optional exact peer executable validation in addition to SO_PEERCRED UID/PID.
- External plugin timeout/output/process-group hardening.
- Expanded Full Power MCP smoke test.

## Reliability model

Remote target definitions are configuration, not SSH connection objects. Every SSH operation can reconstruct its own connection from durable target state. MCP/tunnel restart therefore does not require rebuilding a hidden `primary` connection.

## Security notes

The default policy is intentionally `full`. This grants broad authority within the runtime user's OS permissions, and privileged execution when the broker is available. Use loopback binding, the OpenAI tunnel or another trusted transport, HTTP tokens when appropriate, and policy modes for less-trusted deployments.

The systemd root broker unit requires both the configured runtime UID and `/usr/local/bin/cycomagent` as the peer executable.

## Verified in release gate

- `gofmt`
- `go test ./...`
- `go vet ./...`
- static Linux amd64 builds
- modern MCP `2026-07-28` live HTTP requests
- 42-tool catalog
- stdin process execution
- local `target_exec`
- durable state round-trip
- policy and audit tools
- hostile browser Origin rejection
- legacy GET/SSE probe compatibility
- root broker privileged execution with peer executable verification

## Known limitations

Linux is the Full Power local platform for this build. SSH remote targets can reach any host supported by the installed OpenSSH client, but native Windows/macOS local adapters remain future work.
