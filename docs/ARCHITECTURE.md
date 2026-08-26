# Architecture

## Boundary

CyComAgent-MCP is an execution/observation runtime. The AI client owns reasoning and planning.

```text
AI: decide what should happen
Runtime: expose capabilities, execute a primitive, report what happened
```

The core avoids application-specific workflows.

## State model

MCP transport state is disposable. Application state is explicit and visible:

- files -> paths
- processes -> OS PIDs
- long-running work -> disk-backed job IDs
- AI/application durable values -> `state_*` namespace/key handles
- remote machines -> durable `target_*` definitions
- plugins -> manifests on disk
- policy -> `$STATE_DIR/policy.json`
- audit -> `$STATE_DIR/audit/*.jsonl`

A tunnel reconnect cannot erase a required `primary` session because target definitions are not live SSH sessions.

## Transport

Preferred local deployment:

```text
OpenAI tunnel
    -> tunnel-client.service
       -> POST http://127.0.0.1:7331/mcp
          -> cycomagent.service
```

Remote machine operations are a separate layer:

```text
AI -> MCP -> target_exec("home") -> system ssh -> remote host
```

The target is persistent configuration; `ssh` is opened per operation. It is an adapter, not a runtime prerequisite.

## Tool pipeline

```text
MCP protocol
   |
registry
   |
policy interceptor
   |
handler
   |
audit interceptor
   |
+-- filesystem
+-- process
+-- jobs
+-- system/network/capabilities
+-- desktop
+-- explicit state
+-- machine targets
+-- external plugins
   |
OS / OpenSSH / external binaries / privilege broker
```

Policy and audit are registry-wide rather than being reimplemented by each tool.

## Capability-driven adapters

`capabilities_list` reports candidate adapters and availability/score. The model can choose among generic capabilities based on the actual machine rather than assuming one OS/toolchain.

Examples:

- remote exec -> OpenSSH
- service control -> systemd
- container CLI -> Docker or Podman
- desktop capture -> grim or Spectacle
- desktop input -> ydotool, wtype, or xdotool

## Privilege boundary

`cycomagent` runs as the configured user. Linux root execution is delegated to `cycomagent-root` over a Unix socket.

The broker verifies Unix peer credentials and can additionally require the peer PID to resolve to an exact executable path. The shipped systemd configuration restricts it to `/usr/local/bin/cycomagent` for the configured UID.

This boundary reduces accidental exposure but is not a sandbox: enabling Full Power intentionally grants broad host authority to the authorized runtime.
