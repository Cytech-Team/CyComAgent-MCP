# Remote Jobs P3 Reference Architecture

Status: proposed high-assurance reference architecture only. P3 is a design target for review, not a selected deployment profile or production authorization. No service, database, key, listener, or workload is provisioned by this document.

## 1. Security objective and explicit limits

P3 is intended to preserve verifiable evidence continuity when the runner host administrator, runner process, or ordinary host snapshots may be compromised. It separates (a) the component that reports events, (b) the authority that accepts journal heads, (c) the party that controls signing keys, and (d) the party that independently corroborates lifecycle observations.

P3 cannot make a compromised observation source truthful by cryptography alone. The assurance claim is limited to what independent observers actually measure and what the checkpoint service durably accepts. It must not claim proof of guest behavior, guest integrity, or absence of malicious activity unless a separately reviewed measurement mechanism establishes those facts.

## 2. Proposed trust domains

| Domain | Components | Administrative control | Must not control |
|---|---|---|---|
| D1: runner host | Coordinator, disposable VM supervisor, host event collector, local journal spool | Runner operations | Authority administration, trust-root enrollment, independent observer credentials |
| D2: checkpoint service | Remote CAS API, durable transactional store, audit stream, monotonic epoch | Separate authority operators | Runner host root, runner signing key, runner backup restore |
| D3: observation domain | Independent lifecycle observer(s), observation receipts, verifier | Separate observer operators | Runner host root and checkpoint service administration |
| D4: trust/key administration | Purpose-scoped key enrollment, rotation, revocation, policy approval | Security operators with dual control | Routine runner operations and unilateral checkpoint restore |
| D5: evidence consumer | Offline verifier, audit/review tooling, evidence export | Reviewer/operator | Mutating accepted checkpoint state |

The domains should be separate administrative principals and credentials, not merely separate processes or containers on the same host. Separate domains may still share cloud control planes or identity providers; such shared dependencies must be recorded as residual risks.

## 3. Logical architecture

```text
                D4 Trust / Key Administration
                 | enroll, rotate, revoke
                 v
D1 Runner Host  ------------------------------+
+---------------------------+                 |
| Coordinator               |                 |
| Fixed Supervisor          |                 |
| VM lifecycle              |                 |
| Host collector            |                 |
| Local signed journal      |                 |
+-------------+-------------+                 |
              | signed journal + identity     |
              v                               v
       D2 Checkpoint Authority <------- D3 Independent Observer
       +----------------------+          +----------------------+
       | Authenticated API    |<---------| signed observation   |
       | Full-tuple CAS       |          | receipts              |
       | Durable head         |          +----------------------+
       | Monotonic epoch      |
       | Append-only audit    |
       +----------+-----------+
                  |
                  | read-only evidence bundle / proof receipt
                  v
           D5 Evidence Consumer
           +----------------------+
           | Verify signatures    |
           | Verify journal chain |
           | Verify observer data |
           | Verify authority head|
           +----------------------+
```

The diagram is logical. It does not prescribe a cloud provider, database, KMS, HSM, attestation technology, or network topology.

## 4. Event acceptance protocol

A future protocol should use the following ordered stages. Each stage has a distinct durable or cryptographic result.

1. **Run authorization:** D4-approved policy and a trusted manifest bind runner ID, run ID, nonce, test plan digest, expiry, and permitted lifecycle. Nonce reservation is durable and unique.
2. **Run start:** D1 supervisor reports a start request with a unique event/request ID. D3 independently observes the authorized runner/VM lifecycle using an agreed measurement channel.
3. **Journal construction:** D1 collector emits canonical, bounded, purpose-scoped journal bytes. The signing interface only signs the approved journal schema and run identity; it is not a general signing oracle.
4. **Independent observation:** D3 creates a separately signed receipt with observer identity, run identity, event type, observed measurement, observer sequence, and time/clock-quality metadata. Receipt schema and event mapping require a separate review.
5. **Authority acceptance:** D2 authenticates the submitting principal, verifies authorization and signatures/policy, checks observer requirements, loads the current head, and atomically CAS-advances the complete checkpoint tuple. The successful response contains an authority receipt with accepted sequence, journal digest, authority epoch, commit identifier, and server-side acceptance time.
6. **Evidence finalization:** D5 receives journal bytes, observer receipts, manifest, signatures, and D2 acceptance receipt. D5 independently verifies each signature, exact identity binding, event correspondence, chain continuity, and current authority head.
7. **Teardown:** D1 reports VM and overlay teardown; D3 independently observes required teardown facts. D2 accepts the final journal successor only after all policy-required observations exist. A timeout or missing teardown receipt is an incomplete run, not success.

No stage may infer a successful later stage from a local log line or caller-supplied status field.

## 5. Authority API semantics

The eventual remote authority API should expose semantic operations, not generic database access:

- ReadHead(identity) returns the current full checkpoint, authority epoch, and signed/read-authenticated receipt.
- Advance(expected, next, request_id, evidence_digest, observer_receipt_ids) performs one conditional transaction.
- ReadRequest(request_id) resolves a previously submitted operation after an unknown response.
- ReadAudit(identity, from_sequence) returns an authenticated audit range.
- Administrative genesis, epoch bump, key enrollment, restore, and recovery are separate privileged workflows with dual authorization.

Advance must validate:
- exact runner/run/nonce identity binding;
- full expected checkpoint equality;
- next.sequence == expected.sequence + 1 without overflow;
- valid journal digest and immutable request identity;
- authorization, key purpose, validity interval, revocation state, and observer policy;
- unique request ID and no request-ID reuse with different content;
- observer receipt binding to the same run and lifecycle event;
- current authority epoch and fencing token.

A retry with the same request ID and exact same request body must return the original operation result or a resolvable pending/unknown state. A different body under the same request ID is rejected. The service must never accept last-write-wins updates.

## 6. Independent observation requirements

P3 requires more than a second copy of the same runner's journal. The observer must have a materially independent source of information and credentials.

The observer design must specify:

- What it observes (for example, supervisor lifecycle, hypervisor control-plane events, or hardware-backed measurements).
- Which component produces the measurement and who controls that component.
- How the measurement binds to runner ID, run ID, nonce, VM identity, and event sequence.
- How observer clock uncertainty, monotonic counters, replay, and missing events are handled.
- How observer signing keys are protected, rotated, revoked, and audited.
- Whether the observer can be compromised by the same host administrator or cloud administrator.
- How disagreement between runner and observer is represented; disagreement must fail closed, not be resolved by trusting the runner.

A second agent running as root on the same machine is not independent observation. A second log file, sidecar, or container controlled by the same host root is not an independent trust domain.

## 7. Key custody and identity

- Runner journal signing keys must not be exportable to the general coordinator or arbitrary job payload.
- Prefer a constrained signing service or hardware-backed key boundary whose policy accepts only approved canonical journal objects and run identities.
- D2 service authentication credentials must be distinct from D1 runner keys.
- D3 observer keys must be distinct from D1 and D2 keys.
- D4 administrative credentials must require dual control for genesis, epoch reset, restore, and trust-root changes.
- Key purposes must remain separate: test plan, manifest, host journal, observer receipt, authority receipt, and administrative action.
- Rotation and revocation must preserve historical verification while rejecting signatures made after revocation according to a defined policy.
- Key compromise must trigger run suspension, credential revocation, evidence quarantine, and an audited recovery; it must not trigger silent re-enrollment.

No key generation, enrollment, KMS/HSM integration, or secret storage is part of this proposal.

## 8. Anti-rollback and disaster recovery

D2 should maintain a monotonic authority epoch separate from ordinary checkpoint backups. A restore from backup must not lower the epoch or accepted head without a separately controlled recovery ceremony.

Required controls:

- Backups are consistent snapshots and are encrypted/access-controlled separately from runner backups.
- Restore first enters a non-serving recovery mode.
- The restored database's epoch/head is compared with an independently retained monotonic anchor or externally witnessed audit head.
- If the restored state is older, inconsistent, or unverifiable, the authority remains unavailable for advancement.
- Recovery requires dual authorization, explicit reason, evidence of the last accepted head, and an append-only administrative audit event.
- The recovery procedure must define how to reconcile in-flight and unknown-commit request IDs without replaying writes.
- The system must not manufacture a new genesis checkpoint because a record is absent.

A remote database with backups alone is not sufficient to resist compromise of the database administrator and backup administrator when both can restore the same stale state.

## 9. Failure semantics

| Failure | Required behavior |
|---|---|
| D2 unreachable / network partition | No advancement; retain local evidence as untrusted pending evidence; retry only through request reconciliation |
| Lost response after possible commit | Query the exact request ID; verify original body/digest; never blindly resubmit as a new request |
| CAS conflict | Reload head and re-verify the journal against the new head; do not force update |
| D2 database corruption | Stop acceptance; enter recovery state; never reset to genesis |
| D2 failover | Require leader fencing and consistency level that preserves acknowledged commits |
| D3 unavailable | If policy requires observer evidence, run remains incomplete; no runner-only bypass |
| D3 disagrees with D1 | Quarantine evidence and require review; no automatic preference for D1 |
| D1 host-root compromise suspected | Revoke/suspend runner identity and signing credentials; preserve D2/D3 records; quarantine affected runs |
| Signing key revoked | Reject new signatures under revoked key according to policy; preserve historical verification with revocation-time evidence |
| Clock uncertainty | Use monotonic ordering and explicit clock-quality bounds; reject expiry decisions that cannot be trusted |
| Backup rollback detected | Keep authority closed until dual-control recovery and external epoch reconciliation |

## 10. Evidence bundle and offline verification

A completed bundle should contain:

- Exact canonical manifest and detached signature.
- Exact canonical host journal and detached signature.
- Referenced artifact bytes or independently retrievable immutable references.
- Independent observer receipts and signatures.
- D2 checkpoint acceptance receipt and authority epoch proof.
- Trust material and historical revocation/rotation records sufficient for verification.
- Audit references for genesis and any recovery event.
- A machine-readable verification report listing each claim and its source.

Offline verification can prove signature validity, identity binding, chain continuity, receipt consistency, and whether the supplied authority receipt matches a trusted authority record. It cannot independently prove that a cloud provider, hypervisor, observer, or hardware root was uncompromised unless corresponding attestation and trust evidence is included and validated.

## 11. P3 qualification gates

| Gate | Required proof | Blocks |
|---|---|---|
| G1: threat model | Named administrative domains, shared dependencies, attacker tiers, residual risks | Selecting vendors or topology |
| G2: observation | Independent measurement source, event mapping, spoof/replay tests | Claiming real lifecycle evidence |
| G3: key custody | Non-exportability or constrained signing, rotation/revocation and compromise tests | Trusting host journal signer |
| G4: authority consistency | Multi-client CAS, fencing, partition/failover, unknown-commit reconciliation | Production checkpoint acceptance |
| G5: anti-rollback | External epoch, stale backup detection, restore ceremony and tests | Claiming rollback resistance |
| G6: operations | Dual-control admin, incident response, audit, backup and disaster recovery | Operational approval |
| G7: integration | Separate review of coordinator/supervisor integration and explicit dispatch gate | Any production execution |

All gates require recorded evidence. A passing unit test or architecture document alone is not sufficient for a gate.

## 12. Decisions intentionally left open

This proposal does not select:

- Cloud or hosting provider.
- SQL/NoSQL/database engine.
- KMS, HSM, TPM, confidential-computing, or attestation vendor.
- Observer technology or hypervisor integration.
- Network protocol, deployment region, replication topology, or SLA.
- Key enrollment authority or named operators.

These depend on the actual deployment environment and approved threat model. Selecting them without that information would create false assurance.

## 13. Event mapping companion

The current host lifecycle vocabulary and proposed independent observation requirements are specified in docs/REMOTE_JOBS_P3_EVENT_MAPPING_MATRIX.md. It is a review matrix only and does not claim an observer is implemented.

## 13. Receipt schema companion

The provider-neutral payload and cross-object verification proposal for Observer Receipts, Checkpoint Acceptance Receipts, Advance requests, and reconciliation states is in docs/REMOTE_JOBS_P3_RECEIPT_SCHEMA.md. It is a design artifact only; no receipt types or production protocol have been implemented.

## 13. Current implementation and authorization boundary

Current repository work provides pure evidence verification, in-memory CAS reference behavior, typed checkpoint outcomes, and architecture documents. It does not provide durable remote authority, independent observer, production key custody, anti-rollback epoch, or P3 proof.

This proposal does not authorize:
- P3 profile selection as a production requirement;
- service/database/account/key provisioning;
- network listener or production endpoint creation;
- Dispatch API or Federation Process Launch;
- auto-repair, VM runner, payload execution, or production rollout.

The next safe step is a review of G1-G3 requirements and a provider-neutral interface/receipt schema. No production infrastructure is needed for that review.
