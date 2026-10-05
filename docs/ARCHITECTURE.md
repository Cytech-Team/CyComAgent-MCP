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

## Execution-context routing

The Linux MCP runtime remains a system service for boot-time availability and recovery, but not every child process belongs in that service cgroup. Session-sensitive commands can be routed through a small bridge that is itself started by the graphical login session:

```text
cycomagent@USER.service
        |
        | Unix socket, same UID only
        v
cycomagent --session-bridge
(user.slice/.../session-N.scope)
        |
        +--> GUI app / Polkit action / portal / notification / keyring
```

`process_exec` and `process_spawn` expose `execution_context` with `auto`, `service`, `user`, `desktop`, and `system`, plus `desktop_target` with `auto`, `agent_workspace`, and `current`. Explicit `desktop`/Linux `user` contexts retain login-session bridge behavior; `service` retains service execution for non-input commands; `system` requires `privileged=true` and still passes through policy plus the root broker. With `desktop_target=auto`, `execution_context=auto` recognizes the established desktop/session-sensitive command set and routes those commands through the isolated Agent Workspace, returning an error before command execution if the workspace is unavailable. This includes notifications, URL openers, clipboard/compositor tools, and Noctalia, not only raw pointer/keyboard injectors. Recognized pointer/keyboard input commands remain workspace-routed under explicit `execution_context=service`; any executable-token invocation of global `ydotool`/`xdotool` is classified as input even with options or a path-qualified binary name, and is rejected for Agent Workspace because those tools inject on the physical seat. Any executable-token invocation of `whydotool` is also treated as input, including when global options, path qualification, or a wrapper precede its subcommand. Other explicit service commands remain service-local. Agent Workspace process environments point `DBUS_SESSION_BUS_ADDRESS` to `unix:path=/dev/null`, preventing fallback to the physical login bus; operations requiring a real session bus fail closed. `desktop_target=current` or `CYCOM_AGENT_WORKSPACE_AUTO=0` are explicit physical-desktop opt-outs.

The bridge inherits the real graphical environment and, more importantly, the real logind/systemd session cgroup. It does not attempt to manufacture desktop identity by copying environment variables, and startup is rejected when the bridge's own cgroup has no `session-N.scope`. Its Unix socket is mode `0600`; the bridge verifies peer UID/PID with `SO_PEERCRED` and requires the peer executable to resolve to the same CyComAgent binary before accepting a request.

## Capability-driven adapters

`capabilities_list` reports candidate adapters and availability/score. The model can choose among generic capabilities based on the actual machine rather than assuming one OS/toolchain.

Examples:

- remote exec -> OpenSSH
- service control -> systemd (Linux), launchd/launchctl user domain (macOS), or runit (Termux)
- container CLI -> Docker or Podman
- desktop capture -> grim/Spectacle on Linux or screencapture on macOS
- desktop input -> ydotool/wtype/xdotool on Linux; osascript and optional cliclick on macOS

## Privilege boundary

`cycomagent` runs as the configured user. Linux root execution is delegated to `cycomagent-root` over a Unix socket.

The broker verifies Unix peer credentials and can additionally require the peer PID to resolve to an exact executable path. The shipped systemd configuration restricts it to `/usr/local/bin/cycomagent` for the configured UID.

This boundary reduces accidental exposure but is not a sandbox: enabling Full Power intentionally grants broad host authority to the authorized runtime.
