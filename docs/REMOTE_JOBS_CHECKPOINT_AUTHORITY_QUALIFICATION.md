# Checkpoint Authority Storage Qualification

Status: architecture qualification only. No storage engine is selected or instantiated by this document.

## 1. Purpose and security boundary

The `CheckpointAuthority` contract protects the accepted head of a signed host-journal chain. Its Compare-and-Advance operation must be atomic across the complete checkpoint tuple `(runner_id, run_id, run_nonce, sequence, journal_sha256)`. Two writers presenting the same expected head must not both succeed. A successful return means the new head is durable across the adapter's declared crash boundary. An uncertain return is not success and must be reconciled by reading the authority; callers must never blindly retry.

The authority must be independent of the journal stream and its ordinary backup/restore path. A local database or file protected by the same host administrator and restored from the same snapshot as the journal is not a strong anti-rollback anchor against that administrator. Local durability and independent rollback protection are separate properties.

## 2. Repository facts affecting the choice

- `go.mod` currently has no SQLite, Bolt, or other database driver dependency.
- `internal/vmevidence` is a pure Go package and should not import `internal/federation/remotejobs` merely to reuse its private storage helpers; that would couple evidence policy to the remote-jobs implementation.
- `internal/federation/remotejobs/private_state_store_linux.go` has Linux-only private-directory pinning, descriptor-relative operations, `flock`, file `fsync`, atomic rename/link, and directory `fsync`. Its API is unexported and is not a cross-platform checkpoint adapter.
- `internal/jobs/store_linux.go` uses a pinned Linux directory, per-record `flock`, temp-file write, file `fsync`, rename, and directory `fsync`. Its non-Linux implementation is path-based and has a different security profile. It is a useful implementation reference, not a qualified authority.
- Existing crash callbacks in the jobs store expose logical interruption points, but unit fault injection is not equivalent to power-loss or filesystem durability testing.

## 3. Candidate comparison

| Candidate | Atomic CAS | Durability mechanics | Cross-process / cross-host | Main qualification risks |
|---|---|---|---|---|
| Private file record + OS lock | Implementable with lock + read/compare + atomic replacement | Temp file write, file sync, rename, parent-directory sync | Cross-process on supported local filesystems; not inherently cross-host | Lock semantics and fsync behavior vary by filesystem/OS; stale backup rollback; platform-specific implementation; lock-file and directory hardening |
| SQLite transactional database | Conditional update in a transaction, with a unique identity key | Journal/WAL and synchronous mode; must validate actual VFS/filesystem behavior | Multiple local processes; not a general multi-host authority, especially over network filesystems | Driver/CGO or pure-Go dependency choice, WAL sidecars and backup discipline, busy handling, durability settings, rollback of DB plus backups |
| bbolt-style embedded transactional KV | Single-writer transaction can compare and replace | Database transaction and mmap/page flush behavior; sync policy must be qualified | Multiple local processes subject to file locking; not a distributed multi-host authority | New dependency, single-writer contention, mmap/filesystem assumptions, backup/restore rollback, operational recovery |
| Remote transactional database/service | Conditional update / serializable transaction or equivalent | Server commit and replication durability contract | Suitable for multiple hosts if configured and operated accordingly | Network partitions and unknown commit outcome, service identity/access control, replication failover semantics, operational dependency, backup rollback |
| Independent monotonic anchor / quorum service | Depends on provider's monotonic CAS or quorum protocol | Provider-specific durable acknowledgement | Potentially cross-host and independently administered | Additional infrastructure, enrollment/recovery, availability, quorum and key custody; must verify anti-rollback guarantees rather than assume them |

No candidate is automatically secure merely because it supports transactions or atomic rename. The threat model determines whether a local authority is sufficient or whether the checkpoint must be held by a separately administered service/device.

## 4. Preliminary recommendation

Do not select or implement a production adapter yet.

For a future **single-host, same-kernel threat boundary** prototype, the least-coupled options are a small Linux file-record adapter using a narrowly defined storage primitive, or SQLite with an explicitly selected driver and tested durability settings. The existing `remotejobs` private store should not be imported into `vmevidence`; if its primitives are ever extracted, that must be a separately reviewed shared internal package with platform-specific contracts and independent tests.

If the required threat model includes a compromised host administrator, host snapshot rollback, or several independently administered runners, neither a local file nor a local embedded database is an independent checkpoint authority. A separately administered remote authority or hardware-backed monotonic anchor is then required. This decision must be made from the threat model, not from implementation convenience.

## 5. Mandatory adapter contract

Every adapter proposal must demonstrate all of the following before implementation approval:

1. **Identity binding:** exact runner ID, run ID, and nonce are part of the key and value; no ambiguous key encoding; invalid identities are rejected.
2. **Atomic CAS:** compare all expected fields and update all next fields as one indivisible operation. No last-write-wins behavior.
3. **Monotonicity:** same identity only; `next.sequence == expected.sequence + 1`; sequence overflow rejected; digest is a valid SHA-256 and changes.
4. **Durable success:** success is returned only after the selected durability boundary is met. The boundary must state whether it covers process crash, kernel crash, power loss, device-cache loss, and replicated failover.
5. **Unknown outcomes:** errors after a potentially committed write are classified as uncertain; caller reloads and reconciles. No blind retry and no fallback to genesis.
6. **Fail-closed recovery:** missing, malformed, truncated, inconsistent, or corrupt state never silently initializes a new genesis checkpoint.
7. **Concurrency:** in-process and cross-process competing CAS tests; for a remote service, multi-client and failover tests.
8. **Filesystem/database hardening:** ownership, permissions, symlink/path traversal defenses, bounded record sizes, lock identity, and safe descriptor handling as appropriate to the engine.
9. **Backup/restore:** documented snapshot consistency, restore detection, rollback epoch or external anchor, and tests proving that restoring an old copy cannot silently lower the accepted head.
10. **Operations:** key/authority enrollment, access control, monitoring, retention, backup, recovery, disaster procedure, and audit trail.
11. **Portability:** explicit support matrix. A Linux-only implementation must fail closed on unsupported targets; cross-build success alone does not qualify runtime semantics.
12. **Evidence:** adapter-specific crash/fault tests and documented results. Memory reference tests do not qualify a durable implementation.

## 6. Crash and fault-injection qualification matrix

For each supported adapter, inject interruption at these boundaries and reopen through a fresh process/connection:

| Fault point | Acceptable recovered state | Forbidden result |
|---|---|---|
| Before transaction/lock acquisition | Exact old checkpoint | Missing checkpoint or implicit genesis |
| After lock / transaction acquisition, before compare | Exact old checkpoint | Partial mutation |
| After expected-head read, before write | Exact old checkpoint | CAS accepted without rechecking |
| During write / page update | Exact old or exact new checkpoint, as permitted by engine transaction semantics | Torn/mixed tuple |
| After data write but before durable commit | Exact old checkpoint unless commit is proven durable | Success acknowledgement for non-durable state |
| After durable commit but before acknowledgement | Exact new checkpoint; caller may receive uncertain error and reconcile | Automatic retry, duplicate acceptance, rollback to old head |
| During process termination / restart recovery | Exact old or exact new checkpoint | Silent reset, partial record, fork |
| During concurrent writers | Exactly one winner for one expected head | Two successful successors |
| During backup restore | Restore rejected or independently detected as stale | Old head accepted as current |
| During replica failover (remote) | No acknowledged commit lost under declared durability level | Split-brain successors or stale leader acceptance |

Tests should include deterministic injected errors, subprocess termination, repeated restart, concurrent independent processes, corrupted/truncated state, storage-full and permission failures. Actual power-loss claims require a suitable fault-testing environment; process-kill tests alone cannot prove power-loss durability.

## 7. Selection gate

Before selecting an engine, the owner must approve:

- Whether the authority is local-only or independently administered remotely.
- Which attacker can modify the journal, checkpoint, host, backups, and signing keys.
- Supported OS/filesystem/runtime matrix.
- Required durability level and recovery-time objective.
- Backup/restore anti-rollback mechanism and who controls it.
- Dependency policy and acceptable operational burden.

After that decision, produce a separate adapter design and test plan. Implement only behind the `CheckpointAuthority` interface, retain `MemoryCheckpointAuthority` as test-only, and keep it disconnected from dispatch/VM execution until a separate review gate approves integration.

## 8. Typed adapter outcomes

The package exposes backend-neutral sentinel errors for checkpoint-not-found, CAS conflict, unknown commit outcome, authority unavailable, and corrupt or rollback-suspected state. Adapters should wrap the matching sentinel so callers can classify outcomes with errors.Is without parsing strings. The in-memory reference authority classifies absent records and stale expected heads as not-found and conflict respectively. An unknown commit outcome must never be converted to success by the adapter; callers may read back and reconcile, but must not blindly retry the write.

## 9. Provider-neutral conformance tests

The Go test suite defines a reusable behavioral suite for implementations of CheckpointAuthority in internal/vmevidence/checkpoint_conformance_test.go. It checks exact genesis read, typed not-found, successful exact-head CAS and read-back, stale-CAS conflict without mutation, invalid sequence/digest/cross-run transitions without mutation, concurrent same-head CAS with exactly one winner, and unknown-commit classification followed by read-back (including proving that a retry conflicts after the first write committed).

The suite is currently run against MemoryCheckpointAuthority and an in-memory lost-ack wrapper only. It is not evidence of durable success, process-restart recovery, power-loss safety, filesystem lock correctness, cross-process exclusion, cross-host consistency, independent administration, or rollback resistance. A future adapter must run the same behavioral suite plus adapter-specific crash/restart, multi-process, fault-injection, backup/restore, and (where applicable) replicated failover tests on the actual supported storage and filesystem configuration.

The signed Checkpoint Read-back verifier is a separate evidence-validation primitive. It authenticates a signed snapshot and classifies it relative to a verified acceptance receipt; it does not make the in-memory authority durable and does not itself perform network or storage reads.

## 10. Current decision

**No engine selected. No adapter implemented. No database dependency added. No persistent state path created.** This document records qualification criteria and candidate trade-offs only. Existing in-memory tests establish CAS behavior in one process, not persistence, cross-process locking, crash consistency, or rollback resistance.
