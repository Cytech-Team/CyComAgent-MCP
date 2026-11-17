# Remote Jobs: Descriptor-Preserving Backend Design

## Decision

The current `WorkerReceivedExecution` and `WorkerBackendPreparation` boundary remains admission/preparation only. No systemd method encoder, process executor, worker listener, service unit, account, or production dispatch path is authorized by this design note.

A received executable FD must remain the object that is executed. Reopening an artifact path, using `/proc/self/fd/N` as an `ExecStart` path, or reconstructing a caller-controlled command is not an acceptable substitute.

## D-Bus boundary findings

`org.freedesktop.systemd1.Manager.StartTransientUnit` accepts a unit name, mode, an `a(sv)` property array, and auxiliary units. It does not accept an arbitrary SCM_RIGHTS descriptor list as method arguments. The manager API separately exposes `AttachProcessesToUnit(unit_name, subcgroup, pids)`, which is a process/cgroup operation, not an executable-FD transfer operation.

The service file-descriptor store is not a generic client-to-manager descriptor import API. `FileDescriptorStoreMax=` accepts descriptors sent by a service process through the `FDSTORE=1` notification protocol; systemd returns stored descriptors to that service's own processes on restart, not to arbitrary D-Bus clients. `OpenFile=` is also not equivalent: systemd opens a configured pathname itself.

Consequently, a `StartTransientUnit` call cannot be treated as if it carries the Worker process's two SCM_RIGHTS descriptors into the manager-created service process. No such assumption may appear in an encoder or backend implementation.

## Candidate A: fixed supervisor service

A future design may use a separately installed, fixed-path supervisor service under a dedicated system-service identity. The supervisor would:

1. Authenticate a narrowly scoped local broker peer using kernel peer credentials and a dedicated protocol.
2. Receive exactly the already-verified executable and working-directory descriptors over a private Unix socket using SCM_RIGHTS.
3. Independently validate the artifact identity and one-shot durable request before accepting ownership.
4. Create a per-job child while retaining a synchronization gate; the child must not execute workload code before containment is confirmed.
5. Place the child in a per-job cgroup/scope, apply the reviewed namespace, credential, syscall, resource, and filesystem restrictions, then release the child to execute the retained descriptor with an FD-based exec primitive.
6. Own timeout, cancellation, descendant cleanup, outcome journaling, and descriptor closure.

This candidate is not implemented. The private socket, supervisor service, account, system-manager unit, and child execution path all require the separate Dispatch Gate Review and deployment authorization. The existing root shell broker is explicitly not an acceptable supervisor.

## Candidate B: externally spawned process attached to a scope

The systemd Manager API exposes `AttachProcessesToUnit`, and Scope units expose process attachment plus runtime/cgroup observations. A theoretical approach is to fork a gated child, attach its PID to a newly created scope, and only then release it to execute the inherited descriptor.

This is not approved as the backend. It requires careful proof of PID identity/lifetime, gate ownership, start/attach ordering, crash behavior, manager permissions, and the race between cgroup attachment and release. More importantly, a Scope does not provide the same service process setup boundary as a transient Service: service execution settings such as `User=`, `ProtectSystem=`, `PrivateTmp=`, and related namespace setup cannot simply be assumed to apply to an already-running process attached to a scope. The worker would need a separately reviewed sandbox setup before release. Same-UID access to a user manager also remains an escape unless the execution identity and manager boundary are changed.

Candidate B therefore remains research-only and must not be implemented by adding an `AttachProcessesToUnit` call to the current protocol.

## Required gate criteria

Before any backend can be enabled, a review must demonstrate:

- The exact executable FD received from the broker is the one passed to the kernel execution primitive; no pathname reopen or substitution is possible.
- The working-directory FD is applied without path re-resolution.
- The child cannot execute workload instructions before identity, cgroup, namespaces, and resource controls are confirmed.
- PID reuse, child exit, broker death, supervisor death, timeout, and ambiguous D-Bus outcomes fail closed and cannot trigger replay.
- A hostile workload cannot access a manager/broker control socket or create an out-of-band unit.
- Every descendant remains inside the per-job lifecycle boundary, including `fork`, `setsid`, and daemonization attempts.
- The worker identity, service manager, unit properties, socket permissions, and artifact ownership are independently verified on supported systemd versions.
- Tests cover descriptor substitution, cgroup attachment races, cancellation races, crash recovery, and cleanup without relying only on mocks.

Until all criteria pass Dispatch Gate Review, `WorkerBackendPreparation.ExecutionDisabled()` must remain true and no execution path may be wired to it.

## Fixed supervisor protocol specification

The detailed candidate wire protocol, peer authentication, descriptor ownership, child gate, journal state machine, cancellation, crash recovery, and implementation gates are specified in [`REMOTE_JOBS_SUPERVISOR_PROTOCOL.md`](REMOTE_JOBS_SUPERVISOR_PROTOCOL.md). That document is design-only and does not authorize a listener, service, account, cgroup operation, or execution backend.

## Kernel execution proof plan

See [`REMOTE_JOBS_KERNEL_EXECUTION_PROOF.md`](REMOTE_JOBS_KERNEL_EXECUTION_PROOF.md) for the Linux `execveat(AT_EMPTY_PATH)` constraints, shebang/CLOEXEC failure mode, ELF-only v1 proposal, gated-child/pidfd model, cgroup-v2 placement candidates, and isolated proof matrix. No implementation or execution path is authorized by that document.
