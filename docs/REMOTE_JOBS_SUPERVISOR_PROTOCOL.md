# Remote Jobs: Fixed Supervisor Protocol (Design Only)

## Status and non-goals

This document specifies a candidate protocol for a future, separately installed fixed supervisor. It does not implement or authorize a listener, socket, service, account, cgroup operation, child process, systemd call, or execution path. The existing `WorkerReceivedExecution` and immutable `WorkerBackendPreparation` remain non-executable. `ExecutionDisabled()` must remain true until a separate Dispatch Gate Review approves a complete implementation and deployment plan.

The supervisor is not the existing `cycomagent-root` shell broker and must never invoke it as a worker bridge. No request field may be interpreted as shell text, a command line, a path, an environment override, an identity override, or a systemd property.

## Trust boundaries and responsibilities

### Coordinator / CyComAgent runtime

- Authenticates the Federation caller and verifies the signed, purpose-bound request.
- Applies receiver-local policy and atomically reserves the request in the existing durable ledger.
- Persists the execution record and artifact registry record before any future supervisor handoff.
- Opens the already-authorized executable and working-directory descriptors from receiver-owned roots.
- Connects only to the fixed supervisor endpoint and retains its own durable state.
- Never executes the remote payload and never chooses supervisor identity, worker UID/GID, cgroup, unit properties, or arbitrary executable paths.

### Fixed supervisor

- Runs as a separately provisioned, narrowly privileged system service under a dedicated service identity.
- Owns the system-manager connection, worker credential transition, per-job containment, child lifecycle, cancellation, and terminal outcome journal.
- Independently loads the durable authorization/artifact record from a supervisor-owned store or a broker-authenticated read-only interface. It does not trust a JSON record supplied by the connecting process as authority.
- Accepts only one fixed protocol version and a fixed operation set (`prepare/start`, `query`, `cancel`); it never accepts arbitrary systemd method names or properties.
- Has no generic shell, command runner, plugin loader, or arbitrary file-open operation exposed through the IPC protocol.

### Worker child

- Runs under a dedicated non-login worker identity, not the runtime or supervisor identity.
- Receives only the exact executable and working-directory objects authorized for its one job.
- Cannot access the runtime user's home, user D-Bus, system-manager control socket, supervisor control socket, identity keys, ledger, artifact registry, or other job state.
- Has no capability to create sibling systemd units, change cgroups, ptrace the supervisor, or regain privilege.

## Local IPC endpoint and peer authentication

The future endpoint must be provisioned by the service installer in a root-owned runtime directory, for example `/run/cycomagent/remote-jobs/`. The exact path and ownership are deployment configuration, not caller input. The listener must not create its parent hierarchy opportunistically.

Admission requirements, all checked for each accepted connection:

1. Validate every ancestor directory using descriptor-relative traversal; reject symlinks, unexpected owners, and group/world-writable components.
2. Validate the socket inode, owner, mode, and type without a check/use race. The supervisor retains the bound socket and does not unlink a path it cannot prove it owns.
3. Read kernel `SO_PEERCRED` and require the configured runtime UID and expected GID policy.
4. Obtain and retain `SO_PEERPIDFD`; fail closed if unavailable. Do not reopen `/proc/<pid>` by PID after an unpinned credential sample.
5. Hash the peer executable through a pinned process reference and compare against a supervisor-owned allowlist. PID, UID, GID, executable digest, and connection lifetime are one admission tuple.
6. Enforce connection count, per-UID rate, frame size, deadline, and one-request lifecycle limits before reading unbounded data.
7. Close the connection and pidfd on every failure. No request is accepted on an unauthenticated or reused connection.

A process identity match is not authorization for a Federation caller. Federation signature/trust verification and receiver-local policy reservation remain upstream gates; the supervisor independently binds the local runtime peer to a receiver-owned authorization record for the exact request.

## Wire protocol

### Transport choice

Prefer a Unix `SOCK_SEQPACKET` endpoint so each request/control message is one bounded record and the two descriptor rights can be attached to exactly one record. If the implementation uses `SOCK_STREAM`, it must define a strict phase machine with one serialized writer, explicit framing, ancillary-data association, and connection teardown after any partial or ambiguous read. The existing one-byte SCM_RIGHTS marker is a tested transfer primitive, not by itself a complete supervisor protocol.

### Start request

The start request is a fixed, versioned structure. It may contain only:

- protocol version and operation (`start`)
- immutable `CallerNodeID`, `RequestID`, `RemoteJobID`, `LocalJobID`
- one-shot `StartAttemptID`
- receiver policy revision and payload hash
- artifact opaque name, SHA-256 digest, byte size, device and inode
- bounded deadline/runtime/output limits already authorized by receiver policy
- a random supervisor challenge/nonce if needed for connection-to-request binding

It must not contain:

- executable or working-directory paths
- argv or environment values unless a future reviewed payload contract explicitly requires them
- UID, GID, supplementary groups, capabilities, or user names
- unit name, cgroup path, systemd property, D-Bus method, namespace flag, mount rule, or syscall filter
- shell text, arbitrary command, stdin/stdout path, socket path, or arbitrary file reference

The supervisor derives any internal job key and systemd unit/cgroup identity from validated opaque IDs and the durable record. A caller-supplied unit name is never accepted as authority.

### Descriptor record

The `start` record must carry exactly two SCM_RIGHTS descriptors in one atomic record:

1. Executable FD: regular file, read-only access mode, close-on-exec, expected immutable mode/owner, exact byte size, SHA-256 digest, device and inode matching the independently loaded durable artifact record.
2. Working-directory FD: directory, close-on-exec, device and inode matching the receiver-owned approved directory identity; its mount/filesystem and ancestry policy must also be checked by the supervisor.

No third descriptor, unknown ancillary message, truncated control data, duplicate FD, aliased FD, extra payload byte, or mismatched descriptor count is accepted. On any parse or validation error, every received FD is closed before returning. On success, ownership transfers to the supervisor's request context and remains there until terminal cleanup. The runtime sender retains its own references until it has an unambiguous protocol acknowledgement and then closes them according to its durable lifecycle.

The supervisor must verify executable contents by reading the received FD itself and compare all durable metadata. It must not reopen the artifact by name or trust the sender's stat result. The working-directory FD is likewise validated from the received object, not by reopening a path supplied in the message.

### Replies

Replies are bounded, versioned records and must identify the exact request and start attempt. Only these outcomes are defined:

- `accepted`: supervisor durably reserved the exact start attempt; this is not proof that a child exists.
- `contained`: supervisor durably observed the child inside the approved containment boundary and all pre-execution restrictions are confirmed; child remains gated.
- `released`: supervisor durably recorded that the gate was released after containment confirmation.
- `running`: supervisor observed the exact child identity in the expected lifecycle boundary.
- `terminal`: durable terminal status and bounded exit/signal/timeout/cancel classification.
- `unknown`: caller cannot determine whether the operation crossed a durable boundary; it must query, never replay.
- `rejected`: request was rejected before any child could execute.

A transport write succeeding is not a semantic acknowledgement. The sender must bind every reply to request ID, local job ID, start attempt ID, supervisor instance identity, and a monotonic journal sequence.

## Supervisor durable state machine

The supervisor maintains a private, fsync-backed, create-only journal keyed by `(CallerNodeID, RequestID)` and separately binds `StartAttemptID`, artifact digest/inode, policy revision, and supervisor instance generation. Journal records are append-only or atomically replaced with integrity checks; no request can reset a terminal or ambiguous attempt to `accepted`.

Allowed forward transitions:

1. `received` — authenticated peer and bounded request decoded; no descriptor accepted yet.
2. `validated` — durable authorization/artifact binding and both descriptors independently verified.
3. `reserved` — one-shot attempt ID durably consumed; duplicate or changed attempts are rejected.
4. `child_gated` — child exists but is blocked before workload instructions; parent/supervisor owns a fail-closed gate.
5. `containment_confirmed` — exact child identity, cgroup membership, worker credentials, namespace and resource restrictions are independently observed.
6. `released` — durable release intent is recorded, then gate is released; crash around this boundary is `unknown` until reconciled.
7. `running` — exact child observed alive in the expected cgroup and identity.
8. `terminal` — child exit and all descendants cleaned, descriptors closed, and terminal outcome durable.

`rejected` is allowed only before `child_gated`. Any failure after a child may exist but before a confirmed terminal outcome becomes `unknown`/reconciliation-required, never a retryable `rejected` result. A duplicate request with the same attempt returns the stored state; a different attempt for the same request is rejected. No ambiguous outcome may be replayed.

## Child gate and containment ordering

A child must not execute one instruction of the remote artifact before all of these are true:

- The supervisor has durably consumed the one-shot attempt.
- The child identity is pinned without a PID-reuse ambiguity (pidfd or equivalent kernel-held reference).
- The child has been placed in the exact per-job lifecycle cgroup, and the supervisor has independently read back membership from the kernel/system manager.
- The worker UID/GID and supplementary groups are fixed by supervisor policy; all privilege transitions are complete and irreversible.
- Mount, user, PID, IPC, network, and other selected namespaces are established according to the approved profile.
- Filesystem restrictions, read-only roots, private temporary storage, descriptor inheritance, capabilities, `no_new_privs`, seccomp and resource limits are applied and verified.
- The executable and CWD descriptor identities are still the exact admitted objects.
- The child has no access to supervisor/runtime control sockets or a manager API that could create out-of-band units.

The gate must fail closed if the supervisor dies, its control channel closes, setup times out, or any restriction cannot be applied. A child must not interpret EOF or malformed gate data as permission to continue. A parent-death signal alone is not sufficient because it has races and does not handle every supervisor failure mode; combine it with a one-way gate whose only success token is emitted after durable containment confirmation.

The child applies its working directory using the retained directory FD (`fchdir` semantics), never a caller path. The executable must be invoked from the retained executable FD using a supported FD-based kernel execution primitive. No `/proc/self/fd/N`, `/dev/fd/N`, path reopen, shell wrapper, or reconstructed command line is accepted as a substitute. The exact FD execution primitive, script interpreter behavior, close-on-exec handling, and supported kernel/filesystem matrix require a separate implementation proof and adversarial tests before approval.

## Systemd/cgroup integration constraints

The supervisor may be system-manager-authorized, but it must not assume that `StartTransientUnit` transports the request's SCM_RIGHTS descriptors into the resulting service. The D-Bus API and the descriptor channel are separate.

A candidate gated-child + systemd Scope design still has unresolved questions:

- Exact Scope property contract for PID attachment and supported `KillMode`/resource controls on supported systemd versions.
- Whether the child can be attached atomically before it is released, and how PIDFD identity is correlated with the PID accepted by systemd.
- Which sandbox controls are applied by the supervisor before release because a Scope does not perform the same `ExecStart` service setup as a transient Service.
- How the child is guaranteed not to execute while D-Bus acknowledgement is ambiguous or the manager restarts.
- How the supervisor and system manager recover if either crashes between child creation, cgroup attachment, and gate release.

Do not implement `AttachProcessesToUnit` as a shortcut until these questions are answered. Do not weaken containment to make the API fit.

## Ownership and cleanup table

| Resource | Before accepted | After accepted | On terminal / rejection |
|---|---|---|---|
| Runtime's executable/CWD FDs | Runtime owns | Runtime retains its copies; supervisor owns received copies | Runtime closes its copies after unambiguous acknowledgement/lifecycle decision |
| Received executable/CWD FDs | Not present | Supervisor request context owns | Supervisor closes both on every terminal/rejected path |
| Peer pidfd | Connection handler owns | Retained for connection/request lifetime | Closed when connection ends |
| Child pidfd | Not present | Supervisor owns from creation | Closed after child and descendants are reaped/confirmed gone |
| Gate pipe/socket | Not present | Supervisor and gated child own one end each | Closed; EOF means abort, never continue |
| Per-job cgroup/scope | Not present | Supervisor/system manager owns lifecycle | Stop/kill exact job boundary, verify empty, then record terminal |
| Journal reservation | None | Supervisor owns durable attempt record | Retained permanently or per audited retention policy; never silently reset |

Every error path must have one owner for each descriptor. Ownership transfer is explicit and occurs only after validation. A failed `Close` is recorded as cleanup failure and must not be silently ignored when it affects security or resource release.

## Crash and ambiguous-outcome handling

- Crash before `reserved`: no child may exist; retry may be allowed only if durable state proves no reservation was consumed.
- Crash after `reserved` but before child creation: attempt remains consumed; query/reconcile; never mint a new attempt automatically.
- Crash after child creation but before cgroup confirmation: gate EOF must cause child exit; supervisor restart must find and clean any orphan before accepting new work.
- Crash after containment confirmation but before release: child remains gated and must abort unless the supervisor can prove durable release intent and safely resume the exact gate protocol.
- Crash during gate release or after release but before reply: outcome is `unknown`; query journal and kernel state; never start a replacement child.
- Crash while running: systemd-owned cgroup/scope policy must kill the entire job boundary or a boot-time reconciler must identify and clean the orphan before accepting new work.
- Crash during cancellation: cancellation is idempotent against the exact journaled job; a lost reply is `unknown`, not permission to target a PID or unit guessed by the caller.
- System manager unavailable or D-Bus owner changes: fail closed; pin and validate manager owner/generation; do not fall back to user manager, shell commands, or direct PID killing.

## Cancellation and query

`query` and `cancel` identify a job only by the durable tuple and are authorized by the same authenticated runtime peer. Cancellation is idempotent and may target only the supervisor-owned cgroup/scope recorded in the journal. It must never accept a PID, process name, unit name, cgroup path, or arbitrary signal from the caller. Query results are read-only and cannot cause a restart or repair action.

## Required verification before implementation approval

- Protocol parser fuzzing: duplicate JSON keys, unknown fields, trailing data, oversized records, truncated ancillary data, excess/missing/duplicate FDs, and malformed operation transitions.
- Peer authentication: wrong UID/GID, executable replacement, PID exit/reuse, pidfd loss, socket replacement, ancestor symlink and permission races.
- Descriptor integrity: executable replacement/unlink after open, digest mismatch, writable FD, wrong owner/mode/size/device/inode, CWD replacement and mount changes.
- Gate adversarial tests: child cannot run before release; EOF, supervisor kill, manager restart, timeout, malformed token, and parent death all abort.
- Cgroup tests: fork, double fork, `setsid`, daemonization, thread/process creation, cgroup migration attempts, cancellation races, and cleanup after supervisor crash.
- Privilege tests: no manager socket, no user D-Bus, no root broker, no ptrace, no capability regain, no namespace escape, no writable agent state.
- Durability tests: power-loss simulation at every journal transition, duplicate concurrent requests, changed attempt ID, replay after terminal, and lost acknowledgements.
- Supported-system matrix: kernel, systemd, filesystem, architecture, and FD-based execution primitive behavior.
- Integration tests run in disposable VMs with no production credentials, no real listener, no installed service, and no persistent account changes.

## Explicit implementation gates

1. **Protocol review:** wire schema, ancillary FD framing, ownership, and peer authentication accepted.
2. **Kernel execution proof:** demonstrate FD-based execution and CWD handling on supported systems without pathname reopen.
3. **Containment proof:** gated child, irreversible privilege drop, namespaces, cgroup lifecycle, and manager failure handling demonstrated.
4. **Durability proof:** one-shot journal and crash/no-replay semantics pass fault injection.
5. **Adversarial review:** independent security review of worker-to-supervisor and worker-to-manager escape paths.
6. **Dispatch Gate Review:** explicit approval before any executable backend, listener, service, account, Dispatch API, Federation Process Launch, or auto-repair wiring is created.

Until all gates pass, this document is a design specification only. No production or local execution backend is enabled by it.

## Kernel FD execution and gated-child proof

The Linux FD execution and gated-child research, including the `execveat(AT_EMPTY_PATH)` interpreter-script/CLOEXEC incompatibility, ELF-only v1 recommendation, fixed argv/environment contract, pidfd handling, Go runtime fork constraint, cgroup-v2 placement candidates, and disposable-VM proof matrix are specified in [`REMOTE_JOBS_KERNEL_EXECUTION_PROOF.md`](REMOTE_JOBS_KERNEL_EXECUTION_PROOF.md). This is research/design only; it does not enable execution.

## Disposable VM proof harness

The future kernel/containment prototype must use the isolation, evidence, crash-injection, and acceptance requirements in [`REMOTE_JOBS_DISPOSABLE_VM_PROOF_HARNESS.md`](REMOTE_JOBS_DISPOSABLE_VM_PROOF_HARNESS.md). Ordinary Go CI, containers, and the existing runtime chaos/recovery scripts do not establish the required kernel or VM isolation proof.
