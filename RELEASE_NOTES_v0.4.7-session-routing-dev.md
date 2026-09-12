# v0.4.7-session-routing-dev - Linux Execution Context Routing

This source development update separates **where CyComAgent decides a command should execute** from the long-lived MCP service that requested it.

## What changed

- `process_exec` and `process_spawn` accept `execution_context=auto|service|user|desktop|system`.
- Linux `desktop` and `user` execution routes through `cycomagent --session-bridge`, a per-login-session helper started by the graphical desktop.
- Session-routed children inherit the real login-session cgroup and desktop environment, fixing operations whose authorization depends on active-session identity (for example Polkit power-profile switching).
- `system` execution still requires `privileged=true` and remains behind central policy plus the Linux root broker.
- `auto` stays conservative: it recognizes a small set of well-known session-sensitive commands and otherwise remains in the system-service execution context.
- Desktop/user background jobs remain visible through `job_*` using durable PID/log metadata.
- `system_info` and `capabilities_list` report the desktop-session bridge and its session/cgroup metadata.

## Bridge lifecycle

The systemd installer places `/etc/xdg/autostart/cycomagent-session-bridge.desktop`. Full desktop environments normally launch it on graphical login. Minimal compositors that do not consume XDG autostart entries should start `/usr/local/bin/cycomagent --session-bridge` from their compositor/session autostart.

The bridge refuses to start unless its own cgroup contains a real `session-N.scope`, so copying graphical environment variables into a service cannot impersonate a login session. Its socket is user-private (`0600`), validates the peer UID/PID with Linux `SO_PEERCRED`, and requires the peer executable to resolve to the same CyComAgent binary as the bridge. If no graphical bridge exists, normal service/headless execution is unaffected; explicit desktop/user requests fail clearly instead of silently launching in the wrong cgroup.

## Validation

The test suite covers synchronous session execution, environment/stdin forwarding, process-group timeout cleanup, background log handling, execution-context resolution, and durable external-job persistence. Linux, macOS and Android/Termux builds continue to share the portable core; the actual session bridge is Linux-only in this development version.
