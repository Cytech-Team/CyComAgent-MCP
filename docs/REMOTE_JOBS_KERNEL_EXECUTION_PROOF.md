# Remote Jobs: Kernel FD Execution and Gated-Child Proof Plan

## Status

Research and design only. No executable backend, helper binary, process launch, cgroup mutation, systemd operation, listener, account, or service is implemented or authorized here. `WorkerBackendPreparation.ExecutionDisabled()` remains true. This document records kernel semantics and the proof obligations for a future isolated prototype.

## 1. FD-based execution: Linux `execveat`

Linux `execveat(dirfd, "", argv, envp, AT_EMPTY_PATH)` selects the executable from the already-open file descriptor. It avoids resolving a caller-supplied executable pathname at execution time. The descriptor can refer to an executable file; the kernel manual also documents `O_PATH` descriptors for this purpose. `execveat` is Linux-specific and was added in Linux 3.19.

For this project, the supervisor must retain the already-validated executable FD from SCM_RIGHTS and call the FD-based primitive directly. It must not synthesize `/proc/self/fd/N`, `/dev/fd/N`, a temporary pathname, or a path from the artifact registry. The current transferred descriptor is read-only so the supervisor can hash and inspect it; the exact descriptor access mode and execution behavior must be tested on each supported filesystem and kernel.

### Critical interpreter-script incompatibility

The Linux `execveat(2)` manual documents a specific failure: an interpreter script (`#!...`) executed with `AT_EMPTY_PATH` fails with `ENOENT` when the executable FD has `O_CLOEXEC`, because the interpreter cannot access the script FD after exec. Clearing close-on-exec avoids that error but leaks the descriptor into the interpreter; the documented interpreter argument is a `/dev/fd/N`-style reference. This violates the desired no-path-reopen/no-extra-inherited-FD boundary and creates descriptor-leak and recursive-exec concerns.

**Recommended v1 policy: accept ELF executable artifacts only. Reject scripts and shebang files before reservation.** Do not silently clear `CLOEXEC`, invoke a shell, or add an interpreter path. Supporting scripts later requires a separately reviewed artifact format and interpreter contract, and likely changes the descriptor-count and trust model.

ELF-only validation must be performed against the received FD (magic, class, endianness, machine/architecture, ABI and any policy-specific ELF constraints), not by extension or pathname. ELF magic alone is not proof of safety; the payload remains untrusted and must run inside the full worker containment profile.

## 2. Working directory and argument/environment contract

The child changes directory using the retained directory FD (`fchdir`), then invokes the executable FD. It never resolves the working directory from a string. The directory FD is not passed to the payload unless a future reviewed contract explicitly requires it.

A fixed execution contract is required even when no caller-supplied argv/env is permitted:

- `argv` is constructed by the supervisor, not received from the caller. It contains only a deterministic opaque `argv[0]` label and no caller-controlled arguments.
- `envp` is empty for v1. No inherited environment, PATH lookup, dynamic shell configuration, loader override, locale, HOME, or credential variable is permitted.
- Standard input/output/error are closed or connected to supervisor-created, bounded per-job pipes with explicit quotas and no inherited terminal.
- All descriptors except the intentionally retained standard streams are close-on-exec and closed before the payload image begins. The executable FD may be close-on-exec for ELF execution; this must be verified in the implementation test.
- No `PATH` search, `exec.Command` of the remote artifact, shell, interpreter, or helper wrapper that parses caller data is permitted.

The exact ELF dynamic-loader behavior with an empty environment, architecture-specific ABI, and any required standard descriptors remains part of the proof matrix.

## 3. Gated-child creation and identity pinning

A child must be created in a state where it cannot reach the payload's first instruction. The initial child image is a trusted, fixed gate/helper image or a minimal native child stub; it is not the remote artifact. The child blocks on a one-way gate whose only success token is emitted after all restrictions and containment checks pass. EOF, malformed data, timeout, supervisor death, or setup failure must cause immediate child exit.

The parent obtains a pidfd at creation (`clone3` with `CLONE_PIDFD`) where supported, or opens a pidfd while it still owns an unreaped child and proves that the identity cannot have been recycled. PID values alone are not durable identities. The supervisor retains the pidfd until the process and all descendants are confirmed gone.

A pidfd pins process identity and supports race-resistant process lifecycle operations; it does not itself stop the child, place it in a cgroup, apply a sandbox, or authorize execution. `CLONE_PIDFD` and the gate solve separate problems and both are required in the candidate design.

### Go runtime consideration

Do not implement a post-fork child path by returning into arbitrary Go runtime code. A multithreaded Go process has runtime state and locks that are not safe to use in an ad-hoc fork child. The implementation must either use a proven runtime-supported spawn mechanism for a fixed trusted helper or a separately reviewed minimal native helper. This helper is part of the trusted computing base and must have a tiny, auditable syscall surface. It must not parse shell text, accept arbitrary paths/argv/env, or become a generic command runner.

## 4. Cgroup placement before gate release

Linux `clone3` supports `CLONE_INTO_CGROUP` since kernel 5.7 for cgroup v2. It takes an FD for a cgroup v2 directory and creates the child directly in that cgroup, avoiding a post-creation migration interval. This is a promising option only when the supervisor already owns a correctly delegated and configured target cgroup FD. It does not create a systemd scope, configure namespaces, drop credentials, or apply the complete service sandbox.

A second candidate is:

1. Create a dedicated empty per-job leaf cgroup within a supervisor-owned/delegated cgroup-v2 subtree.
2. Start the fixed child helper in a blocked state and retain its pidfd while keeping it unreaped.
3. Move that exact blocked child into the leaf cgroup using the cgroup v2 interface.
4. Independently verify membership, resource controls, subtree ownership, and that no unrelated process is present.
5. Apply/verify all remaining worker restrictions, then durably record containment confirmation.
6. Release the gate only after the durable release intent is written.

This sequence is a candidate, not an approved implementation. It depends on a systemd delegation contract and cgroup-v2 availability. The supervisor must not write outside its delegated subtree, alter systemd-owned ancestors, or create arbitrary units. The child must remain blocked throughout placement; no untrusted code may run in the interval. The PID-to-pidfd correlation, child-reaping behavior, cgroup migration permissions, and crash cleanup must be demonstrated under stress.

Do not treat `CLONE_INTO_CGROUP` as a universal solution: it is cgroup-v2-only, subject to normal cgroup placement restrictions, and requires the target cgroup FD to exist before child creation. If the system manager's lifecycle model cannot provide that FD safely, fail closed rather than fall back to an unverified scope attach sequence.

## 5. Minimum isolated proof matrix

The proof prototype must run only in disposable VMs with synthetic artifacts and no production credentials. It must not install a persistent service or account and must not create a production listener.

| Test | Required result |
|---|---|
| ELF opened, then renamed/unlinked | FD execution runs the exact opened inode or fails closed; no pathname reopen |
| Executable content/hash mismatch | Rejected before child creation |
| Script/shebang with CLOEXEC FD | Rejected by artifact policy; no interpreter fallback |
| Script/shebang without CLOEXEC | Rejected; no descriptor leak accepted as a workaround |
| Wrong ELF architecture/ABI | Rejected before child creation |
| `noexec` mount / missing execute permission | Kernel failure is reported as a non-replayable start outcome; no fallback |
| Empty environment and fixed argv | Payload sees only the fixed contract; no caller values or inherited variables |
| CWD rename/unlink after FD open | Child uses the admitted directory object or fails closed; no path reopen |
| Gate EOF / malformed token / timeout | Child exits without executing payload |
| Supervisor killed before release | Child exits; no payload instruction runs |
| Supervisor killed at release boundary | Outcome becomes unknown and reconciles; no second child is launched |
| Cgroup placement failure | Gate remains closed and child exits |
| Child forks/double-forks/setsid | All descendants remain in the job boundary and are killed/reaped on cancellation |
| PID churn and concurrent starts | pidfd always refers to the admitted child; no PID-only targeting |
| Kernel/systemd restart | No orphan can execute outside the boundary; startup reconciliation blocks new work until clean |

Record kernel version, systemd version, cgroup mode, filesystem/mount options, architecture, syscall results, and exact helper build digest for every run.

## 6. Decision and open questions

Current recommendation:

- ELF-only payloads for v1.
- `execveat(AT_EMPTY_PATH)` as the candidate FD execution primitive, subject to implementation proof.
- Fixed trusted helper/gate image; never fork back into the Go runtime to perform complex child setup.
- Dedicated cgroup-v2 delegated subtree; evaluate `CLONE_INTO_CGROUP` first, otherwise prove blocked-child placement before gate release.
- No systemd Scope attachment shortcut until its PID/cgroup lifecycle and crash semantics are proven.

Open questions that block approval:

1. Exact systemd delegation configuration and ownership contract for the supervisor's cgroup-v2 subtree.
2. Whether a minimal helper can safely perform credential drop, namespace setup, mount restrictions, `no_new_privs`, seccomp and FD cleanup before gate release on every supported architecture.
3. Exact behavior of `execveat` on supported filesystems, mount flags, LSM configurations and architectures.
4. ELF policy for static vs dynamic binaries and dynamic loader availability without environment variables.
5. How output pipes are bounded and drained without allowing the payload to block cleanup or exhaust supervisor resources.
6. How boot-time reconciliation proves no stale job remains before accepting new requests.

No implementation should begin until these questions are resolved and Dispatch Gate Review explicitly approves the result.

## References

- Linux `execveat(2)`: <https://www.man7.org/linux/man-pages/man2/execveat.2.html> — `AT_EMPTY_PATH`, descriptor execution, and the `O_CLOEXEC` interpreter-script `ENOENT` behavior.
- Linux `execve(2)`: <https://www.man7.org/linux/man-pages/man2/execve.2.html> — interpreter script semantics and argument/environment behavior.
- Linux `clone(2)` / `clone3(2)`: <https://www.man7.org/linux/man-pages/man2/clone.2.html> and <https://man7.org/linux/man-pages/man2/clone3.2.html> — `CLONE_PIDFD` and cgroup-v2-only `CLONE_INTO_CGROUP`.
- Linux `cgroups(7)`: <https://www.man7.org/linux/man-pages/man7/cgroups.7.html> — cgroup v2 process placement and hierarchy constraints.

## 7. Audit of the current descriptive worker profile

The current `BuildWorkerServiceProfile` is a fixed descriptive baseline, not a proven sandbox and not a running service. It currently describes:

- Dedicated non-root `User=` and `Group=`.
- `KillMode=control-group`, `SendSIGKILL=yes`, `TimeoutStopSec=250ms`, and `RuntimeMaxSec=`.
- `NoNewPrivileges=yes`, empty capability bounding/ambient sets, `RestrictSUIDSGID=yes`, and `UMask=0077`.
- `ProtectSystem=strict`, `ProtectHome=yes`, `PrivateTmp=yes`, `PrivateDevices=yes`, and `PrivateNetwork=yes`.
- `ProtectProc=invisible`, `ProcSubset=pid`, `RestrictNamespaces=yes`, `RestrictAddressFamilies=AF_UNIX`, `DevicePolicy=closed`, and kernel/clock/hostname protection properties.
- `UnsetEnvironment=DBUS_SESSION_BUS_ADDRESS XDG_RUNTIME_DIR` and inaccessible paths for `/run/user`, the system bus socket, and the configured root broker socket.

This is useful defense-in-depth data, but it is not yet a complete worker sandbox. In particular, the current profile does not explicitly define:

- `MemoryMax=`, `TasksMax=`, `CPUQuota=`/`CPUWeight=`, `IOReadBandwidthMax=`/`IOWriteBandwidthMax=`, or a bounded `LimitNOFILE=` policy.
- A minimal immutable `RootDirectory=`/`RootImage=` or a reviewed set of bind mounts. `ProtectSystem=strict` makes much of the host filesystem read-only; it does not make every readable host file secret or inaccessible.
- An explicit `PrivateIPC=` / `PrivatePIDs=` decision or a reviewed namespace contract.
- A syscall allowlist/denylist (`SystemCallFilter=`), syscall architecture restriction, `MemoryDenyWriteExecute=`, or filesystem restriction policy.
- Explicit bounded stdout/stderr destinations and a supervisor-enforced output quota.
- A reviewed supplementary-group policy and an explicit statement about whether the worker UID is static, dedicated per host, or dynamically allocated.

These omissions are not permission to append guessed systemd properties. They are review items. Every selected property must be checked against the exact systemd version and the actual launch model, with tests that verify the effective kernel state rather than merely accepting a D-Bus property.

### Scope-versus-Service incompatibility

The existing profile is shaped like a transient **Service** property set (`User=`, `Group=`, `ProtectSystem=`, `PrivateTmp=`, and related service sandbox properties). A systemd **Scope** does not retroactively perform the same `ExecStart` setup for an already-running gated child. Therefore the profile cannot simply be copied onto a Scope and treated as applied.

The design must choose and prove one of these models:

1. A fixed transient Service launched by a trusted helper whose executable and descriptor contract is independently proven; or
2. A dedicated supervisor service with a delegated cgroup-v2 subtree, where the supervisor/helper itself establishes every required namespace, mount, credential, capability, seccomp, resource and descriptor restriction before releasing the child.

The second model must not claim that a cgroup alone applies service sandbox settings. The first model must not assume `StartTransientUnit` can receive the two already-open payload descriptors. Neither model is approved today.

### Namespace ordering constraint

`RestrictNamespaces=yes` is intended to prevent the workload from creating namespaces. If the supervisor/helper needs to create user, mount, PID, IPC or network namespaces as part of the pre-exec setup, it must do so before the restriction becomes effective or use a service-manager-created namespace facility whose effective behavior is verified. Do not remove `RestrictNamespaces=yes` from the worker merely to make setup easier. The payload must never receive authority to create or join namespaces unless a future policy explicitly requires it.

### Decision

Keep `BuildWorkerServiceProfile` as a fixed, non-executable policy description. Do not expand it into an assumed complete sandbox until the service-vs-scope architecture, root filesystem, resource limits, syscall policy, namespace order, output handling and effective-property verification are approved. The existing tests that use synthetic shebang bytes exercise descriptor admission only; they do not establish that scripts are valid executable artifacts. The future execution backend must reject shebang artifacts under the ELF-only v1 proposal above.

## 8. Disposable VM proof harness

The reproducible VM isolation profile, test layers, run phases, evidence provenance contract, expanded test matrix, fault-injection barriers, CI separation, and exit criteria are specified in [`REMOTE_JOBS_DISPOSABLE_VM_PROOF_HARNESS.md`](REMOTE_JOBS_DISPOSABLE_VM_PROOF_HARNESS.md). This is a design specification only. The current development host does not have QEMU/KVM command-line tooling installed; `systemd-nspawn` is present but shares the host kernel and is not accepted as a VM proof environment. No VM was created or started.

The provenance and verification rules for disposable-VM results are specified in [REMOTE_JOBS_VM_EVIDENCE_MANIFEST.md](REMOTE_JOBS_VM_EVIDENCE_MANIFEST.md). Host-observed facts, guest trusted-channel evidence, and guest-untrusted logs must remain distinct; aggregate PASS is recomputed by the host verifier.
