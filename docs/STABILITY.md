# Stability contract

"Never fail" is not physically achievable: power, storage, kernel, hardware, network, upstream services and user configuration can fail.

The engineering goal is stronger and measurable:

> Ordinary failures must not require a human to recreate MCP/SSH session state.

## Required recovery behavior

| Failure | Required behavior |
| --- | --- |
| MCP request/session changes | No loss of runtime-owned state |
| tunnel-client restart | CyComAgent remains alive |
| CyComAgent crash | systemd restarts runtime |
| root broker crash | systemd restarts broker; non-root tools remain usable |
| desktop-session bridge absent/crashed | service/headless execution remains usable; explicit desktop/user execution fails clearly until the login-session bridge returns |
| graphical logout/login | old bridge disappears with the login session; the next desktop autostart creates the new session bridge/socket |
| temporary network outage | local runtime remains ready |
| reboot | runtime/root/tunnel services start without GUI login |
| long job + MCP reconnect | job remains addressable by ID |
| runtime restart during long job | runtime recovers PID metadata; reports unknown exit if process ended while offline |

## Health model

- `/livez`: process is alive.
- `/readyz`: state directory is writable and tools are registered.
- `/health`: structured machine/runtime state.
- `/metrics`: counters and runtime gauges.

The watchdog uses readiness, not just `systemctl is-active`.

## Failure hysteresis

The systemd units restart crashed processes immediately. The periodic watchdog handles processes that remain alive but unhealthy. Tunnel and runtime are restarted independently so a tunnel incident does not erase local work.

## Chaos acceptance test

A release intended for unattended deployment should pass:

1. repeated runtime `SIGKILL`;
2. tunnel `SIGKILL` while runtime remains available;
3. MCP smoke after recovery;
4. reboot without desktop login;
5. temporary network loss and return;
6. concurrent file/process tool calls;
7. command timeouts and large output caps;
8. persistent job recovery;
9. desktop-session bridge routing verifies child cgroup/session identity and preserves Polkit-visible active-session behavior.

`scripts/chaos-test.sh` automates the process-kill subset.

`scripts/recovery-test.sh` verifies that a background job remains observable across a hard runtime kill/restart.
