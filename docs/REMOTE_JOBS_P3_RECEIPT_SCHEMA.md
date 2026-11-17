# P3 Observer and Checkpoint Receipt Schema

Status: provider-neutral schema proposal. It is not a wire-protocol commitment, production API, or authorization to provision services. The schema is intentionally separate from the existing V1 manifest and host-journal formats.

## 1. Design rules

1. Every receipt is signed over its exact canonical JSON bytes using a purpose-scoped key.
2. A receipt binds the same runner ID, run ID, and run nonce as the manifest and journal.
3. Digests are lowercase hexadecimal SHA-256 over exact canonical payload bytes, not re-serialized semantic objects.
4. Receipt IDs and request IDs are globally unique within their issuer scope and are immutable once issued.
5. Times are canonical RFC3339 UTC strings. Monotonic sequence values are authoritative for ordering within one issuer; wall-clock timestamps are descriptive and carry an explicit uncertainty bound.
6. Unknown fields, duplicate JSON keys, non-canonical encodings, unsupported versions, invalid bounds, and invalid signatures are rejected.
7. A receipt is a claim by its issuer. Signature validity proves issuer-key possession, not truth of the underlying observation or correctness of the authority's storage.
8. The observer receipt and authority acceptance receipt are distinct evidence types with distinct key purposes. Neither substitutes for the other.

## 2. Shared identity and digest conventions

| Field | Type | Rule |
|---|---|---|
| schema_version | string | Exact version, initially 1.0 |
| receipt_id | string | Non-empty bounded opaque identifier; unique per issuer |
| issuer_id | string | Stable identity from out-of-band trust configuration |
| runner_id | string | Exact manifest host.runner_id |
| run_id | string | Exact manifest run_id |
| run_nonce | string | Lowercase hex, at least 32 bytes |
| issued_at_utc | string | Canonical RFC3339 UTC |
| payload_sha256 | string | Lowercase 64-character SHA-256 |
| signature | detached envelope | Existing Ed25519-SHA256 envelope, purpose-scoped trust lookup |

The detached signature is carried beside the receipt payload, not nested into the bytes whose digest it signs. This avoids self-reference. A transport object may contain both:

- payload: exact canonical receipt JSON bytes;
- signature: SignatureEnvelope with algorithm, key_id, digest, signature.

The digest in the detached signature envelope must equal SHA-256(payload). Subject measurements use their own explicitly named digest fields.

## 3. Observer Receipt payload

The observer receipt records one independently sourced observation. One receipt represents one event, not a batch with ambiguous partial acceptance.

~~~json
{
  "schema_version": "1.0",
  "receipt_id": "obs-opaque-unique-id",
  "issuer_id": "observer-domain-a",
  "runner_id": "runner-01",
  "run_id": "run-opaque-id",
  "run_nonce": "64-or-more-lowercase-hex-characters",
  "observer_sequence": 17,
  "event_id": "event-opaque-id",
  "event_type": "VM_START",
  "subject": {
    "kind": "VM_INSTANCE",
    "id": "opaque-provider-instance-id"
  },
  "measurement": {
    "kind": "HYPERVISOR_CONTROL_PLANE_EVENT",
    "value_sha256": "64-lowercase-hex-characters",
    "source_id": "independent-source-01"
  },
  "observed_at_utc": "2026-11-08T00:00:00Z",
  "clock_uncertainty_ms": 250,
  "previous_receipt_sha256": "64-lowercase-hex-characters",
  "policy_digest_sha256": "64-lowercase-hex-characters"
}
~~~

### Observer field semantics

- observer_sequence starts from an explicitly provisioned observer genesis and increases exactly by one for each receipt in the stream keyed by issuer_id, runner_id, run_id, and run_nonce. It must never be silently reset.
- event_id binds the receipt to one host-journal event. It is not sufficient by itself to prove the event happened.
- event_type is a versioned, allow-listed vocabulary. Initial candidates are VM_CREATE_REQUESTED, VM_START, VM_STOP, VM_DESTROY, and OVERLAY_DESTROY. A mapping document must define whether each is a request, observed state transition, or confirmed terminal state.
- subject identifies the observed resource in the observer's namespace. Provider IDs are opaque and must not be interpreted as globally unique without issuer_id.
- measurement.value_sha256 hashes the exact source record or attestation bytes retained by the observer. The underlying bytes must be included or retrievable by immutable authenticated reference for independent verification.
- observed_at_utc is the observer's wall-clock time. clock_uncertainty_ms is a non-negative upper bound declared by the observer; it is not a proof of clock correctness.
- previous_receipt_sha256 links this observer stream. The first receipt must refer to an explicitly provisioned observer genesis digest; it must not use an implicit zero digest unless a separately approved genesis format defines that exact value.
- policy_digest_sha256 binds the event interpretation and acceptance policy used by the observer.

The observer receipt does not contain a free-form success field. The verifier derives whether the observation satisfies a policy from the event type, measurement, source, and policy digest.

## 4. Checkpoint Acceptance Receipt payload

The authority receipt records one immutable authority decision for one idempotent request.

~~~json
{
  "schema_version": "1.0",
  "receipt_id": "authority-receipt-unique-id",
  "issuer_id": "checkpoint-authority-a",
  "request_id": "client-generated-unique-request-id",
  "request_sha256": "64-lowercase-hex-characters",
  "runner_id": "runner-01",
  "run_id": "run-opaque-id",
  "run_nonce": "64-or-more-lowercase-hex-characters",
  "authority_epoch": 42,
  "commit_sequence": 18,
  "accepted_journal_sha256": "64-lowercase-hex-characters",
  "previous_journal_sha256": "64-lowercase-hex-characters",
  "observer_receipt_ids": [
    "obs-opaque-unique-id"
  ],
  "decision": "ACCEPTED",
  "committed_at_utc": "2026-11-08T00:00:01Z",
  "durability_class": "QUORUM_COMMITTED",
  "fencing_token": 901
}
~~~

### Authority field semantics

- request_id is generated before submission and remains unchanged during reconciliation. It is never reused for a different request body.
- request_sha256 hashes the exact canonical Advance request payload shown in Section 5, including the complete expected and next checkpoint, observer receipt IDs, and policy digest.
- authority_epoch is a monotonic authority generation. It may increase only through a separately authorized administrative transition; it must not decrease on restore.
- commit_sequence and journal digests bind the accepted journal successor to the prior head.
- observer_receipt_ids list the exact receipts evaluated by policy. The verifier must retrieve and verify each one; a list of IDs alone is not proof.
- decision is ACCEPTED only in a success receipt. Rejections and unknown/pending outcomes use a distinct response envelope and must never be represented as accepted receipts.
- committed_at_utc is authority server time and is descriptive; durable commit semantics are defined by the authority implementation and qualification evidence.
- durability_class is a declared implementation guarantee, not a self-authenticating assurance. Its meaning must be documented and tested. QUORUM_COMMITTED must not be used unless acknowledged writes survive the declared failure model.
- fencing_token is monotonically increasing within the authority leadership/epoch scope. Stale leaders and stale tokens are rejected.

## 5. Advance request

The canonical Advance request is a separate signed/authenticated request body:

~~~json
{
  "schema_version": "1.0",
  "request_id": "client-generated-unique-request-id",
  "authority_epoch": 42,
  "runner_id": "runner-01",
  "run_id": "run-opaque-id",
  "run_nonce": "64-or-more-lowercase-hex-characters",
  "expected": {
    "sequence": 17,
    "journal_sha256": "64-lowercase-hex-characters"
  },
  "next": {
    "sequence": 18,
    "journal_sha256": "64-lowercase-hex-characters"
  },
  "observer_receipt_ids": [
    "obs-opaque-unique-id"
  ],
  "policy_digest_sha256": "64-lowercase-hex-characters"
}
~~~

The authenticated client identity is established by the transport and authority policy, not trusted from a caller-controlled JSON field. The authority binds that authenticated principal to runner_id and permitted run scope. It validates full expected-head equality, exact +1 sequence, immutable request ID/body, authority epoch, observer receipts, and policy before committing.

## 6. Reconciliation response states

A transport response is not itself a signed acceptance receipt unless it contains the exact signed receipt payload and signature.

| State | Meaning | Client action |
|---|---|---|
| ACCEPTED | Durable commit at declared durability class | Verify receipt and independently read back head |
| REJECTED_CONFLICT | Expected head differs | Reload and re-verify journal from new head |
| REJECTED_POLICY | Identity, policy, signature, or observer requirements failed | Stop; do not mutate request |
| PENDING | Request is known but not terminal | Poll ReadRequest using same request ID |
| UNKNOWN | Server cannot yet establish terminal outcome | ReadRequest only; do not create a replacement request |
| NOT_FOUND | No request record is visible | Do not infer that no commit occurred unless authority contract guarantees that conclusion |
| UNAVAILABLE | Authority cannot answer | Preserve evidence as pending/untrusted; no advancement |
| CORRUPT_OR_ROLLBACK_SUSPECTED | Authority state or epoch inconsistent | Close acceptance and require audited recovery |

A retry with the same request ID and identical request digest returns the same operation state/result. Reuse of the request ID with a different digest is a hard conflict. If the service cannot prove whether a commit happened, it must report UNKNOWN rather than NOT_FOUND.

## 7. Signature purposes and trust records

The Go reference implementation defines TEST_PLAN, MANIFEST, HOST_JOURNAL, OBSERVER_RECEIPT, and CHECKPOINT_ACCEPTANCE as distinct purposes. AUTHORITY_ADMIN_ACTION remains a future purpose and is not implemented:

- OBSERVER_RECEIPT
- CHECKPOINT_ACCEPTANCE
- AUTHORITY_ADMIN_ACTION

No purpose may be silently aliased to HOST_JOURNAL or MANIFEST. Trust records must bind key ID, purpose, issuer identity, validity interval, revocation status/time, and policy version. Historical verification requires retained, authenticated revocation and rotation history.

## 8. Canonical encoding and validation limits

The Go implementation reuses strict bounded JSON validation, unknown-field rejection, and canonical re-encoding. Current reference limits are: payload at most 1 MiB; source measurement at most 16 MiB; at most 512 observer receipt IDs per advance; request and receipt IDs at most 128 bytes; durability class at most 64 bytes; observer clock uncertainty at most 300,000 ms; observer sequence, authority epoch, commit sequence, and fencing token are uint64 with zero/overflow rejected where they cannot represent a valid successor. Subject and source identifiers are bounded by the shared 4,096-byte JSON string limit. Remaining limits to freeze include:

- maximum payload bytes;
- maximum observer receipt count per journal successor;
- maximum observer ID and source ID lengths;
- maximum event vocabulary and subject identifier lengths;
- maximum clock uncertainty;
- maximum observer sequence and authority epoch;
- maximum request ID length;
- maximum audit range size.

All integer overflow cases must fail closed. Arrays must be deterministically ordered where order is semantically meaningful; otherwise IDs must be sorted before canonical request construction. No floating-point values should be used for timestamps, counters, or digests.

## 9. Cross-object verification rules

A verifier accepts a completed evidence bundle only when all applicable checks pass:

1. Manifest signature and trusted manifest key purpose.
2. Manifest identity matches journal, observer receipts, authority receipt, and run authorization.
3. Host journal signature and canonical bytes.
4. Journal sequence and previous digest match the independently loaded checkpoint.
5. Every required host event has exactly the required observer corroboration according to the approved mapping policy.
6. Observer receipt signatures, issuer authorization, observer stream continuity, event IDs, subject identity, measurement digest, and policy digest all validate.
7. Authority receipt signature and CHECKPOINT_ACCEPTANCE purpose validate.
8. Authority receipt request digest, epoch, sequence, previous digest, accepted digest, and observer receipt IDs match the exact Advance request and journal.
9. An authenticated authority read confirms the accepted head has not been superseded or rolled back, or the verifier explicitly labels the proof as historical-at-epoch rather than current.
10. Artifacts referenced by the manifest are present and their exact bytes match declared digests and bounds.

A valid observer receipt cannot repair an invalid host journal. A valid authority receipt cannot establish that the observer was independent. Each trust claim is verified separately.

## 10. Pure verification model status

Pure Go primitives now exist at internal/vmevidence/observer_receipt.go, internal/vmevidence/observer_verify.go, and internal/vmevidence/checkpoint_receipt.go. The checkpoint receipt verifier validates the exact canonical Advance request digest, successor and predecessor heads, run identity, authority epoch, ordered observer receipt IDs and policy identity, then verifies a CHECKPOINT_ACCEPTANCE signature using issuer-bound trust. It does not prove durable storage, fencing-token monotonicity across authority history, or current-head status; those require authenticated authority read-back and qualified authority guarantees. DecodeObserverReceipt enforces bounded strict JSON, the exact V1 field set, canonical encoding, UTC timestamp form, and clock-uncertainty bounds. VerifyObserverReceipt checks the exact payload signature with the OBSERVER_RECEIPT key purpose, verifies supplied retained source-measurement bytes against the declared SHA-256, and only then projects a semantic claim for corroboration. Observer verification requires an issuer-aware trust store that binds key ID to issuer ID as well as OBSERVER_RECEIPT purpose, validity interval, and revocation status; a generic purpose-only TrustStore is deliberately insufficient. Source bytes are bounded by the existing single-artifact limit. MemoryTrustStore is test/reference-only. These primitives do not establish D3 independence, measurement-source authenticity, durable replay protection, checkpoint acceptance, or provider integration. Unit tests use synthetic keys and measurements only.

## 10. Event mapping companion

The mapping from current V1 host events to proposed independent D3 observations, including conflict and missing-evidence behavior, is documented in docs/REMOTE_JOBS_P3_EVENT_MAPPING_MATRIX.md. Current implementation behavior and proposed P3 corroboration are explicitly separated there.

## 10. Test vectors required before implementation approval

Pure Go tests should cover:

- valid observer receipt and valid authority receipt;
- wrong purpose, issuer, key ID, expired key, revoked key, and signature mutation;
- identity mismatch across runner/run/nonce;
- event ID mismatch and missing required observer;
- duplicate receipt ID, observer sequence gap/replay, wrong predecessor digest;
- observer clock uncertainty overflow and unsupported event type;
- request ID reused with identical body versus changed body;
- wrong expected head, skipped sequence, stale authority epoch, stale fencing token;
- unknown commit followed by exact request reconciliation;
- authority receipt whose accepted digest differs from the journal;
- authority receipt with missing or substituted observer receipt;
- canonical JSON violations, duplicate keys, unknown fields, oversized payload, and integer overflow;
- historical receipt verification after key rotation and revocation.

Tests must be pure and deterministic. They do not prove real observer independence, durable database commits, process-crash consistency, key non-exportability, or anti-rollback.

## 11. Current implementation boundary

The observer and checkpoint acceptance payloads now have pure Go V1 decoders and issuer-bound, purpose-scoped signature verifiers. CanonicalCheckpointAdvanceRequest creates the exact canonical request bytes whose SHA-256 is bound by the acceptance receipt. These primitives do not create keys, provision an authority, or connect to an observer or authority. The current MemoryTrustStore and MemoryCheckpointAuthority remain test/reference-only. A valid acceptance receipt is not proof of durable commit, monotonically increasing fencing history, or current head; authenticated read-back and authority qualification remain mandatory.

Before implementation, review the event mapping and issuer identity model. In particular, decide which independently observable event source is actually available; do not encode a fictional provider event into a schema and later describe it as measured evidence.
