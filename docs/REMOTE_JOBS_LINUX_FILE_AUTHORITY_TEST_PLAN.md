# Linux File Checkpoint Authority — Adapter Qualification Test Plan

Status: **test plan only**. This document does not select or implement a storage adapter. No state directory, lock file, service, listener, or persistent record is created by this plan.

## 1. Scope and non-goals

The target under evaluation is a future Linux-only implementation of the vmevidence.CheckpointAuthority interface, backed by one checkpoint record per (runner_id, run_id, run_nonce) and a stable inter-process lock.

The plan qualifies local file semantics only. Passing it would not establish protection against a malicious or compromised host administrator, a host snapshot rollback, a compromised kernel, or coordinated rollback of the journal and authority directory. Those threats require an independently administered monotonic authority or another separately approved anchor.

Out of scope:
- Implementing the adapter or choosing its state path.
- Enabling Dispatch, worker execution, VM launch, auto-repair, or production integration.
- Treating internal/jobs or internal/federation/remotejobs storage code as already-qualified authority code.
- Claiming power-loss durability from ordinary unit tests or process-kill tests.

## 2. Proposed storage protocol to test (not an implementation decision)

A candidate design may use:
- A pinned, owner-controlled directory opened without following symlinks.
- Descriptor-relative operations for all child records and lock files.
- A stable lock inode that is never replaced or unlinked during normal operation.
- A bounded, canonical checkpoint record.
- A same-directory unique temporary file, fully written and synced before atomic replacement.
- A parent-directory sync after replacement.
- Full tuple comparison while holding the same exclusive lock across read, validation, and replacement.

The candidate must specify exact syscall ordering, error handling, lock lifetime, and cleanup behavior before implementation. In particular, it must explain what it returns if replacement succeeds but parent-directory sync fails: this is an uncertain outcome, not success.

A candidate may use flock or another reviewed Linux locking primitive, but the choice must be explicit. Lock correctness must be demonstrated across independent processes, not inferred from goroutine tests. Lock files must not be unlinked/recreated while another process may hold or wait on the old inode.

## 3. Test environment and support declaration

Initial qualification target:
- Linux only, GOOS=linux, native runtime tests.
- Local filesystems explicitly named by the adapter owner (for example, ext4 and XFS only if actually tested).
- Kernel versions and mount options recorded with each result.
- Tests run as an unprivileged dedicated test user in a private temporary directory.
- No network filesystem support unless a separate matrix demonstrates the required locking and durability semantics.

Record filesystem type, mount options, kernel, architecture, storage device/cache assumptions, and test harness version. Unsupported filesystems must fail closed or be clearly rejected; do not silently claim generic Linux support.

Android, macOS, Windows, NFS, SMB, overlay/container filesystems, and removable/network-backed storage are not covered by a Linux local-file pass.

## 4. Behavioral contract tests

Run the reusable CheckpointAuthority conformance suite against the candidate adapter, retaining all existing memory-only tests. Required assertions:

1. Exact identity lookup returns the exact stored tuple.
2. Missing identity returns ErrCheckpointNotFound; it never creates genesis implicitly.
3. A valid next head commits and can be read back from a fresh adapter instance.
4. A stale expected tuple returns ErrCheckpointConflict and leaves the head unchanged.
5. Sequence skip, sequence replay, overflow, cross-run identity, unchanged digest, malformed digest, and invalid identity are rejected without mutation.
6. Two or more clients contending on the same expected tuple produce exactly one successful CAS.
7. Every successful read returns one complete tuple; mixed fields from two writes are impossible.
8. Unknown outcomes are wrapped as ErrCheckpointOutcomeUnknown; the caller can reconcile with a fresh read and must not retry blindly.
9. Corrupt or truncated records return ErrCheckpointCorrupt (or a stricter fail-closed error) and are never interpreted as absent.
10. Permission, ownership, path, and lock failures never trigger a fallback to memory state or a new genesis.

## 5. Path, descriptor, and filesystem hardening

| Test | Setup / action | Required result |
|---|---|---|
| Symlink root | Point configured state path at a symlink | Reject, or resolve only under an explicitly documented safe canonicalization policy; never follow an attacker-swapped child |
| Root replacement race | Rename the opened directory and create a replacement at the old path | All operations remain bound to the pinned directory or fail closed |
| Child symlink | Replace record, temp, or lock name with a symlink | Open must reject without reading/writing the symlink target |
| Wrong file type | Use FIFO, socket, device, directory, or hard-linked unexpected object | Reject before blocking or mutation |
| Ownership/mode | Make root, lock, or record owned by another user or group/world accessible | Reject or safely tighten only where policy explicitly allows; never silently trust it |
| Name traversal | Supply separators, dot components, oversized IDs, or crafted identity strings | Reject before filesystem access |
| Descriptor lifetime | Close adapter and attempt operation; repeatedly open/close under load | Closed adapter fails predictably; no descriptor leak |
| Directory substitution | Replace a child directory while adapter is active | Descriptor-relative operations remain pinned or fail closed |
| Record bounds | Oversized and truncated records | Bounded read; classify corrupt, not absent |
| Temp collision | Pre-create candidate temp names and race creation | Exclusive creation fails safely; no overwrite |
| Lock inode stability | Attempt to replace/unlink lock path while another process holds it | Adapter detects/rejects unsafe state; never split lock domain |

The adapter must document whether it checks effective UID, file type, permissions, link count, and directory identity, and why each check is sufficient for its declared threat boundary.

## 6. Atomic replacement and durability fault points

Instrument deterministic failpoints in a test-only adapter build. Do not expose production fault-injection controls.

| Failpoint | Injected interruption | Acceptable fresh-open result |
|---|---|---|
| F0 | Before lock acquisition | Exact old head |
| F1 | After lock acquisition, before read | Exact old head |
| F2 | After expected-head read, before compare completes | Exact old head; no write |
| F3 | After compare, before temp creation | Exact old head |
| F4 | After temp creation, before payload write | Exact old head; orphan temp may exist but is ignored |
| F5 | During/after partial temp write | Exact old head; partial temp never treated as record |
| F6 | After complete temp write, before file sync | Exact old head |
| F7 | After temp file sync, before close | Exact old head unless durable replacement has not happened |
| F8 | After close, before rename | Exact old head |
| F9 | Immediately after atomic rename, before parent-directory sync | Exact old or exact new head after restart; operation result must be uncertain if durability cannot be established |
| F10 | Parent-directory sync returns an error | Never report durable success; fresh read determines old/new and error remains uncertain |
| F11 | After successful parent-directory sync, before acknowledgement | Exact new head; caller may see unknown outcome and reconcile |
| F12 | After acknowledgement | Exact new head after fresh process open |

For every point, verify that the record is either the complete old tuple or the complete new tuple. A torn tuple, missing record, implicit genesis, or mixed state is a failure.

Process termination at failpoints is useful for recovery testing but does not simulate power loss or device-cache loss. Power-loss claims require a dedicated fault-testing environment and documented hardware/cache assumptions.

## 7. Cross-process locking and CAS tests

Use helper subprocesses, each with its own adapter instance and independently opened descriptors.

- Start at least 32 processes behind a barrier, all attempting the same expected-to-next CAS; exactly one may succeed.
- Repeat under CPU and I/O contention for at least 1,000 rounds in a controlled test environment.
- Verify that a process crash while holding the lock releases kernel lock ownership and a new process can make progress.
- Verify that a paused holder blocks contenders until release; no timeout may silently bypass the lock.
- Verify lock identity is stable across adapter reopen and never split by unlink/recreate.
- Verify lock acquisition errors, interrupted syscalls, and descriptor close errors fail closed.
- Verify a process that read the old head before another process commits cannot later overwrite the new head.

Record winner counts, conflict counts, unexpected errors, hangs, and per-round duration. Race detector results do not replace subprocess tests.

## 8. Restart, corruption, and operational failures

Test fresh process open after:
- Clean close and reopen.
- SIGTERM while idle, waiting for lock, writing temp, after file sync, after rename, and after directory sync.
- SIGKILL at each instrumented failpoint.
- Repeated open/recovery cycles.
- Empty directory where a checkpoint is expected.
- Empty, malformed, duplicate-key, noncanonical, truncated, oversized, and digest-invalid record.
- Orphan temporary files from interrupted writes.
- Read-only directory, read-only filesystem, quota exhaustion, ENOSPC, EDQUOT, EIO, EMFILE, and permission changes.
- Lock file replaced by symlink or a different inode.
- Concurrent open/close and read/write operations.

The adapter must distinguish absence from corruption. It must not delete suspicious state automatically or recreate a genesis record during recovery.

## 9. Backup, restore, and rollback tests

A local file authority cannot independently prove that a restored checkpoint is current if the attacker can restore the authority and journal together.

Test:
1. Capture a consistent old snapshot at sequence N.
2. Advance authority to N+K and verify the new head.
3. Restore only the old authority snapshot.
4. Restore both the old authority and journal snapshots.
5. Attempt to open and read the restored state.
6. Repeat with a missing or stale external rollback anchor, if one is part of a separately approved design.

Without an independent monotonic anchor, the expected security conclusion is **rollback not detectable**. The adapter must not be described as anti-rollback protected. With an approved external anchor, restoration must be detected and fail closed before the old head is accepted.

Backup procedures must define consistency boundaries, inclusion of lock metadata, permissions, retention, encryption, and recovery ownership. Copying a live file without a documented consistency method is not a qualified backup.

## 10. Filesystem-specific qualification

Run the complete suite separately for each claimed filesystem and mount configuration. Do not extrapolate results between ext4, XFS, btrfs, tmpfs, overlayfs, NFS, SMB, or other filesystems.

For each configuration, collect:
- Filesystem and mount options.
- Kernel and VFS version.
- Storage device and write-cache policy.
- Whether barriers/flushes are honored by the storage stack.
- Results for file sync, atomic rename, directory sync, and flock behavior.
- Results after abrupt process termination and any available controlled crash/power-fault test.

If a required primitive is unsupported (for example, directory sync), the adapter must reject that configuration or explicitly weaken its declared durability contract; it must not silently continue.

## 11. Pass criteria and evidence package

A filesystem/configuration passes only if:
- All behavioral and hardening tests pass.
- All subprocess CAS rounds have exactly one winner.
- Every injected interruption recovers to an allowed complete old/new state.
- No successful acknowledgement is followed by loss of the acknowledged head within the declared durability boundary.
- Unknown outcomes remain uncertain and are reconciled by read-back.
- Corruption, permission, storage, and locking errors fail closed.
- Backup/restore rollback behavior is documented honestly, including lack of local anti-rollback protection.
- Test logs, environment metadata, failpoint results, and known limitations are retained as a qualification artifact.

A pass is scoped to the exact tested kernel, filesystem, mount options, and storage assumptions. It is not a general Linux durability certification.

## 12. Decision gate

No Linux File Authority should be implemented or selected until the owner explicitly approves:
- The single-host threat boundary and accepted administrator capabilities.
- Whether local-only durability is sufficient despite no independent anti-rollback protection.
- Supported filesystem/kernel combinations.
- Required crash boundary (process, kernel, power, device cache).
- Locking primitive and storage protocol.
- Backup/restore and external rollback-anchor policy.
- Test environment capable of producing the required evidence.

Until that gate is approved, MemoryCheckpointAuthority remains test/reference only, the signed read-back verifier remains a pure verifier, and no dispatch or VM execution path may consume this authority.
