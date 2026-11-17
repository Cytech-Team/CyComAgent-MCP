# Remote Jobs: Linux Process Containment

## Execution boundary

Linux Remote Jobs currently run inside a transient **user systemd scope**. The launcher does not fall back to process-group-only execution when this capability is unavailable.

Each invocation uses:

- `systemd-run --user --scope --collect`
- A unique transient scope name
- `RuntimeMaxSec` equal to the request timeout
- `KillMode=control-group`
- `SendSIGKILL=yes`
- A short `TimeoutStopSec` for cancellation escalation

The scope's cgroup contains the executable and its ordinary descendants. A descendant calling `setsid()` or changing process group membership remains in the same cgroup. Timeout, output-limit cancellation, and abrupt launcher-parent exit are bounded by the scope lifecycle and its runtime limit.

The executable is opened and validated before launch, then passed through an inherited descriptor (`/proc/self/fd/3`). The working directory is resolved from its open directory descriptor. The scoped command is started with validated argv and no shell.

## Environment handling

The `systemd-run` client receives only the session variables needed to contact the user manager (`XDG_RUNTIME_DIR` and `DBUS_SESSION_BUS_ADDRESS`). The workload is then executed through `env -i`, so it receives only keys explicitly present in `ProcessRequest.Environment`. Environment keys are sorted before argv construction, and empty keys are rejected by policy validation.

## Required host capabilities

A Linux host must provide:

- `systemd-run`, `systemctl`, and `env`
- A working user systemd manager and its D-Bus session connection
- A systemd/cgroup configuration that permits transient user scopes and supports `RuntimeMaxSec` and `KillMode=control-group`
- `/proc` descriptor access for the launching user

If these conditions are not met, launch fails closed. There is intentionally no unsafe process-group fallback. On CI hosts without a user systemd manager, execution-dependent tests may skip after a capability probe; policy, persistence, and recovery tests remain runnable.

## Security boundary and remaining escape

This is **process-lifecycle containment, not an adversarial sandbox**.

A workload running as the same Unix user can still access that user's systemd D-Bus socket, even though the launcher strips D-Bus variables from the workload environment. It can explicitly restore the known socket address and ask the user manager to create a second transient scope. That nested scope is outside the original job cgroup and can outlive the original job timeout. A Linux regression test intentionally demonstrates this behavior and cleans up its uniquely named test scope.

Therefore:

- The current backend reliably contains ordinary descendants, including `setsid()` descendants, within the job cgroup.
- It does **not** prevent a malicious same-UID workload from asking the user manager to create a separate unit.
- It must not be treated as a security boundary for arbitrary or hostile code.
- Closing this gap requires a different execution identity or supervisor boundary (for example, a dedicated service account/system manager service with a service-level sandbox that cannot access the user manager bus).

Cgroup containment also does not by itself restrict filesystem access, network access, syscalls, capabilities, CPU, memory, or process count. Those require separate policy and resource-control work.

Non-Linux descriptor-bound execution remains unsupported. Android builds may compile shared Linux code but do not gain a systemd user-scope execution backend.

## Dispatch remains gated

The Remote Jobs MCP surface currently exposes authenticated reservation, status, and read-only reconciliation only. It does not expose process launch. Federation transport dispatch, process launch through the network, and automatic repair remain disabled pending a separate Dispatch Gate Review.

### Privileged broker separation

The existing `cycomagent-root` executable is a general-purpose root shell bridge (`shell -lc` over caller-supplied command text), not a remote-job execution broker. Remote Jobs must not reuse its socket or execution path. A future system-manager broker must be a separate, narrowly scoped implementation that accepts only the signed/authorized structured contract, uses a dedicated non-root worker identity, and owns the systemd unit/cgroup lifecycle. Until that implementation, its installation and security review are complete, no remote-job dispatch listener or launch path may be enabled.

## Future supervisor profile is not yet a complete sandbox

The fixed `WorkerServiceProfile` is currently descriptive data only. It includes a dedicated non-root identity, systemd hardening properties, private network/tmp/devices, filesystem protections, and cgroup lifecycle settings, but it does not yet define all resource limits, a minimal root filesystem, syscall policy, bounded output handling, or the final namespace ordering. It is shaped like a transient Service profile; a systemd Scope does not automatically apply the same `ExecStart` sandbox setup to an already-running gated child. See [`REMOTE_JOBS_KERNEL_EXECUTION_PROOF.md`](REMOTE_JOBS_KERNEL_EXECUTION_PROOF.md) for the gap audit. Do not treat this profile as an approved adversarial sandbox or copy it onto a Scope and assume the restrictions are active.
