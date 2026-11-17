# Remote Jobs Checkpoint Authority Threat Model

Status: threat-model proposal for architecture review. This document does not select or deploy an authority, provision keys, create storage, enable dispatch, or authorize VM execution.

## 1. Security objectives

The checkpoint authority has a deliberately narrow objective:

- Preserve the latest accepted head of each host-journal chain.
- Reject replay, truncation, skipped sequence numbers, and forks from an earlier accepted head.
- Ensure competing writers cannot both advance one expected head.
- Make uncertain commits recoverable by read-back without blindly repeating an operation.

It does not, by itself:

- Prove that a journal event happened.
- Prove that the host kernel, runner, collector, clock, or signing key was uncompromised.
- Prove that an artifact came from a particular VM or physical host.
- Prevent an authorized signer from signing a fabricated but internally consistent journal.
- Provide trusted time, key enrollment, key revocation, or operator identity.
- Provide execution isolation, VM containment, or dispatch authorization.

A valid signature plus a monotonic checkpoint establishes continuity of accepted signed statements, not the truth of those statements.

## 2. Assets and security principals

| Asset / principal | Security value | Required protection |
|---|---|---|
| Checkpoint tuple | Accepted chain head for runner/run/nonce | Atomic CAS, durable success, anti-rollback |
| Signed journal bytes | Ordered host observations claimed by a signer | Signature, canonical encoding, protected collection |
| Journal signing key | Authority to make statements about host events | Purpose-scoped custody, rotation, revocation, restricted signing interface |
| Manifest and nonce reservation | Binds a test plan and prevents replay | Independent trust, durable uniqueness, fail-closed recovery |
| Artifact bytes and metadata | Evidence payload and digest binding | Immutable/read-only acquisition, bounded reads, independent provenance |
| Coordinator runtime | Authenticates callers and applies receiver policy | Least privilege, durable request ledger, no payload execution |
| Fixed supervisor | Future lifecycle and containment authority | Separate identity, fixed protocol, no generic shell |
| Checkpoint operator / administrator | Can enroll, restore, revoke, or recover authority | Separation of duties, auditable recovery, no silent genesis reset |

## 3. Threat actors and capability tiers

The tiers are cumulative for analysis. A deployment must state which tiers it intends to resist.

| Tier | Attacker capability | What a local file / embedded DB can provide | Required response |
|---|---|---|---|
| T0: crash / accidental fault | Process crash, restart, torn write, disk full, software bug | Potentially durable atomic updates if implementation and filesystem are qualified | Crash matrix, fresh-process recovery, fail closed on corruption |
| T1: unprivileged local user | Can read/write only their own files and processes; cannot modify protected authority directory/service | Can protect records if ownership, permissions, descriptor pinning, and lock semantics are correct | Private authority identity and directory; cross-process adversarial tests |
| T2: privileged host administrator / root | Can modify host files, processes, local DB, local keys, snapshots, and backups | Cannot provide an independent anti-rollback anchor against the same administrator | Separately administered authority or hardware boundary outside host admin control |
| T3: runner / collector / signing-key compromise | Can cause the trusted host-side component to sign false observations, or use its key | A remote checkpoint still prevents silent rollback/fork, but accepts a consistently fabricated successor if signer is authorized | Independent observation/attestation channel, constrained signing service, key isolation and revocation |
| T4: checkpoint authority administrator compromise | Can alter records, restore backups, change policy, or enroll keys | Depends on separation of duties and external monotonic protection | Independent audit/monotonic anchor, dual control, externally retained recovery epoch |
| T5: network attacker / partition | Can delay, drop, replay, reorder, or interrupt requests and responses | Local DB irrelevant to remote protocol semantics | Authenticated transport, request identity, idempotent read-back reconciliation, no blind retry |
| T6: remote service / quorum failure | Failover, stale replica, split brain, loss of quorum, acknowledged write loss | Depends on service consistency and durability guarantees | Explicit commit durability level, fencing/leader epoch, failover and partition tests |

### Interpretation

- A local file or SQLite/bbolt database may be suitable for T0/T1 within one host trust domain.
- It is not an independent anchor against T2 if the same administrator controls both journal and checkpoint.
- A remote service controlled by a separate administrative domain can reduce T2/T4 exposure, but does not solve T3.
- A compromised authorized signer can produce a valid false chain. To make claims about real VM lifecycle events, observations must be independently constrained or corroborated.
- No design should claim resistance to a tier unless the authority, signing keys, backups, operators, and recovery process are outside that tier's control.

## 4. Trust-boundary model

### Boundary A: coordinator to supervisor

The coordinator authenticates Federation requests, checks receiver policy, and reserves the request. The future supervisor independently binds the local peer to the exact receiver-owned authorization record. Neither side treats caller-supplied paths, credentials, unit names, or command text as authority.

This boundary is described by the Fixed Supervisor Protocol. It remains design-only; no listener or service is authorized.

### Boundary B: host observation to journal signer

The host collector observes lifecycle facts and constructs the canonical journal. The signer signs only the approved journal format and purpose. If the collector, kernel, or signer is compromised, the signature alone cannot distinguish fabricated events from true events.

Required future controls include a narrowly scoped signing interface, key isolation, event-source binding, trusted clock policy, audit of signing requests, rotation/revocation, and independent corroboration for high-assurance claims.

### Boundary C: journal stream to checkpoint authority

The journal bytes and checkpoint state must not share the same rollback domain for the threat tier being claimed. Checkpoint advancement is a full-record CAS. Unknown outcomes require read-back and reconciliation.

A local authority on the same host is a crash-consistency mechanism, not a root-resistant anti-rollback mechanism.

### Boundary D: authority administration and recovery

Genesis provisioning, key enrollment, key revocation, backup restore, and disaster recovery are privileged security operations. They must not be implicit side effects of a missing record or a normal verification request.

Recovery must not lower a checkpoint silently. If a restore is older than the independently retained epoch/head, verification remains blocked until a separately authorized reconciliation is completed and audited.

## 5. Deployment profiles

| Profile | Intended environment | Candidate authority | Claims allowed after qualification |
|---|---|---|---|
| P0: test-only | Unit tests and local development | MemoryCheckpointAuthority | In-process CAS semantics only |
| P1: single-host crash-safe | One host; root/admin trusted; protect against crashes and unprivileged local users | Private local file authority or embedded transactional DB | Process/kernel crash properties only to the tested durability level; no root-resistant rollback claim |
| P2: multi-host service | Multiple runners; separate authority service and admin domain | Remote transactional authority with conditional update | Cross-host CAS and declared service durability; no claim against compromised signer |
| P3: high-assurance evidence | Host administrator and runner compromise are in scope | Independently administered monotonic authority plus independently corroborated host observations | Only the exact tiers demonstrated by key custody, observation, rollback, quorum, and recovery tests |

Provisional recommendation: do not choose a production storage engine until the owner explicitly selects a profile. If the product requirement is merely crash-safe single-host state, P1 is the smallest practical scope. If evidence must remain trustworthy after host-root compromise or snapshot rollback, P1 is categorically insufficient; start with P2/P3 threat boundaries instead of trying to harden a local database into an external trust anchor.

## 6. Required decisions before adapter selection

The architecture owner must explicitly answer:

1. Is the target P1, P2, or P3? P0 remains test-only.
2. Is host root/admin trusted, or must the system resist it?
3. Is the journal signer on the same host as the collector? Can the runner invoke arbitrary signing operations?
4. What independently observes VM start, containment, stop, and teardown?
5. Who controls the checkpoint authority, its backups, and recovery credentials?
6. What is the rollback detection mechanism after a full snapshot restore?
7. Does the deployment require one host, several hosts, or several administrative domains?
8. Which crash boundary must a successful CAS survive: process, kernel, power loss, device-cache loss, or remote failover?
9. What happens when the authority is unavailable? The answer must be fail closed; no local fallback or genesis reset.
10. Who can provision the initial genesis checkpoint, and what dual-control/audit process authorizes it?

No implicit defaults should be used for these security decisions.

## 7. Minimum qualification by profile

### P1 minimum

- Dedicated protected directory and authority identity.
- Descriptor-relative, no-symlink access and strict ownership/mode checks.
- Atomic full-record CAS across independent processes.
- File and parent-directory durability or equivalent database commit semantics.
- Corruption, truncation, permission, disk-full, process-kill, and restart tests.
- Explicit statement that root/admin and same-domain snapshot rollback are out of scope.
- No genesis recreation when a checkpoint is missing or invalid.

### P2 minimum

All P1-relevant semantic tests plus:

- Authenticated mutually authorized client identity.
- Server-side conditional update on the complete expected tuple.
- Transaction/replication durability semantics documented for acknowledged writes.
- Request correlation and reconciliation after timeout or lost acknowledgement.
- Leader fencing and stale replica rejection.
- Multi-client CAS races, partition, failover, and acknowledged-commit-loss tests.
- Authority service and backup administration separated from runner administration.

### P3 minimum

All P2 requirements plus:

- Monotonic rollback epoch or equivalent externally retained state.
- Independent administration and recovery authorization.
- Signing key isolated from general runner process memory and arbitrary signing requests.
- Independent corroboration of host lifecycle observations.
- Key rotation/revocation and compromised-signer incident procedures.
- Auditable dual-control genesis, restore, and checkpoint recovery.
- Tests demonstrating the exact claimed protection against host snapshot rollback and authority restore.

## 8. Fail-closed state machine

The verifier/adapter integration must distinguish:

- CURRENT: authority head was loaded and is valid.
- ADVANCED: CAS returned success at the declared durability level.
- CONFLICT: expected checkpoint no longer matches; caller reloads and re-verifies from the new head.
- UNKNOWN_COMMIT: write may have committed but acknowledgement is uncertain; caller performs read-back only and does not retry the same write blindly.
- UNAVAILABLE: authority cannot be reached; verification cannot advance.
- CORRUPT_OR_ROLLBACK_SUSPECTED: state is missing, malformed, older than external epoch, or inconsistent; stop and require audited recovery.

No state may transition automatically from unavailable/corrupt to genesis. A conflict must not be treated as permission to accept the same journal without re-verification against the newly loaded head.

## 9. Current implementation boundary

The current code provides:

- Pure signature and journal-chain verification.
- An in-memory reference CAS authority for tests.
- An orchestration helper that loads, verifies, and attempts one CAS.
- Tests for concurrent CAS, replay, stale head, and a simulated commit whose acknowledgement is lost.

The current code does not provide:

- Durable checkpoint storage or cross-process authority.
- Independent genesis provisioning.
- Host-root-resistant rollback protection.
- Protected host journal collection or independent event observation.
- Production trust administration, key custody, or recovery.
- Any listener, supervisor service, dispatch, VM execution, or auto-repair.

The lost-ack test is a deterministic in-process simulation, not a process kill, database crash, filesystem fault, or power-loss test.

## 10. Gate decision

No deployment profile has been selected. No production adapter is approved. The next review should decide the attacker tiers and deployment profile first. Only then should a storage engine be selected and an adapter-specific design/test plan be written.

This document does not authorize persistent storage creation, service installation, dispatch, Federation Process Launch, auto-repair, VM execution, or production rollout.
