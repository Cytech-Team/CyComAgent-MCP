# Checkpoint Authority Interface — Security Gap Review

Status: design review only. No interface or runtime behavior is changed by this document. No deployment profile, provider, database, or production adapter is selected.

## 1. Executive finding

The current Go CheckpointAuthority interface is intentionally small:

- LoadCheckpoint(runnerID, runID, runNonce)
- CompareAndAdvance(expected, next)

Its HostJournalCheckpoint value contains runner ID, run ID, nonce, sequence, and journal digest.

The separately implemented signed Checkpoint Acceptance and Checkpoint Read-back receipt models contain additional authority state:
- AuthorityEpoch
- FencingToken
- acceptance RequestID and request digest
- durability classification
- authority issuer identity
- read-back receipt identity and explicit supersession links

These are not currently represented or enforced by CheckpointAuthority. The receipt verifiers validate signed claims, but they do not make the local interface enforce epoch/fencing monotonicity, idempotent request semantics, or durability guarantees.

**Conclusion:** current conformance tests establish only the existing five-field checkpoint API behavior against the in-memory reference. They do not qualify fencing, epoch changes, idempotent request handling, or a provider's declared durability class.

## 2. Evidence in current code

| Concern | Current representation | Gap |
|---|---|---|
| Checkpoint identity/head | HostJournalCheckpoint | No authority epoch or fencing token |
| Atomic compare-and-advance | CheckpointAuthority.CompareAndAdvance(expected, next) | No explicit request ID or idempotency key |
| Read-back | LoadCheckpoint(identity) | Returns only the five-field head; no signed receipt, issuer, epoch, fencing, or durability metadata |
| Acceptance receipt | CheckpointAcceptanceReceiptPayload | Rich signed evidence is verified separately and is not returned by the interface |
| Read-back receipt | CheckpointReadbackPayload | Signed snapshot verifier exists, but no interface method obtains it from a real authority |
| Unknown commit | typed error and read-back guidance | No operation-status lookup by request ID |
| Durability | acceptance receipt carries DurabilityClass | Interface cannot declare or enforce what successful return means |
| Epoch/fencing | receipt verifier compares against accepted values | No live authority API contract enforces monotonic progression |

## 3. Security consequences

1. A caller using only CheckpointAuthority cannot distinguish a head from a new authority epoch if the five checkpoint fields are identical.
2. A stale authority instance or leader cannot be rejected using a fencing token through this interface alone.
3. A successful CompareAndAdvance has no machine-readable durability level. Its comment requires survival across restart, but the interface cannot express the storage/provider boundary that was actually achieved.
4. A lost acknowledgement can be reconciled by loading the head, but the caller cannot query the original operation by request ID or retrieve a signed acceptance receipt through this interface.
5. The in-memory conformance suite cannot test epoch monotonicity or fencing behavior because those fields are absent from its contract.
6. A signed read-back receipt can report epoch/fencing values, but unless the authority implementation itself enforces those values and the verifier has an authenticated source for them, the values remain signed assertions rather than demonstrated monotonic state.

## 4. Required contract capabilities before a production adapter

The next interface revision should be considered only after the owner selects the threat profile. The following capabilities should be evaluated:

### A. Authority head

A richer head projection should bind:
- runner ID, run ID, nonce;
- journal sequence and digest;
- authority epoch;
- fencing token;
- authority issuer / stable authority identity;
- schema/version information where needed.

The comparison contract must define which fields are immutable for one run and which may advance. Epoch changes must be explicit administrative transitions, never inferred from a missing record.

### B. Idempotent advance operation

An advance request should bind:
- globally unique request ID within the authority namespace;
- canonical request digest;
- exact expected and next head;
- authority epoch;
- required policy / observer receipt references.

The authority should reject reuse of one request ID with different request bytes. Repeating the exact same request must resolve to the original result or a clearly reported pending/unknown state, not create a second logical commit.

### C. Fencing semantics

The authority must allocate or validate fencing tokens under its own serialization boundary. A token must never be accepted merely because the caller supplied a larger integer. Stale writers and stale leaders must be rejected at the authority boundary.

The contract must state:
- who allocates tokens;
- whether tokens are global, per runner, per run, or per authority epoch;
- how epoch changes affect token ordering;
- how failover prevents an old leader from writing;
- what read-back proves after a token transition.

### D. Authenticated read-back and operation status

The contract should distinguish:
- current head read;
- signed read-back receipt;
- operation status by request ID;
- audit-range retrieval, if required.

Unknown commit handling should query/read back only; it must not blindly reissue a new request or silently change the expected head.

### E. Durability declaration

The adapter contract must specify what a successful advance means:
- process-restart durable;
- kernel-crash durable;
- power-loss durable under named storage assumptions;
- quorum committed / replicated;
- or another precisely defined level.

The durability label must be assigned by the authority implementation, not accepted as a caller assertion. A receipt may report the level, but qualification evidence must support it.

## 5. Proposed future interface shape (illustrative, not approved API)

A future design may separate semantic operations instead of expanding one generic interface:

- ReadHead(identity) -> authenticated authority head
- Advance(request) -> accepted receipt | conflict | unknown
- ReadOperation(requestID) -> accepted | rejected | pending | unknown
- ReadBack(identity) -> signed current-head receipt
- ReadAudit(identity, cursor) -> authenticated audit range
- privileged, separately authorized epoch/genesis/recovery operations

This is a capability checklist, not a Go API proposal. Do not implement it until the selected profile and authority ownership model are explicit.

## 6. Conformance suite changes required after interface approval

When a revised contract is approved, extend the reusable adapter conformance suite to test:

1. Epoch is initialized only through explicit genesis authorization.
2. Epoch cannot decrease or change implicitly.
3. Fencing token is allocated/validated by the authority and cannot regress.
4. A stale fencing token is rejected even if its expected journal head otherwise matches.
5. Concurrent writers across independent processes receive distinct ordered fencing outcomes as specified.
6. Same request ID + same canonical body resolves to the original operation.
7. Same request ID + different body is rejected.
8. Unknown commit is resolved by request-status/read-back without duplicate logical advancement.
9. Signed read-back issuer, epoch, fencing token, head and supersession links agree with the authority's actual state.
10. Declared durability class is supported by the adapter-specific crash/failover qualification evidence.

Tests must run against the actual adapter and, for remote authority, against the real service test environment. In-memory fixtures alone cannot qualify distributed fencing or durability.

## 7. Profile implications

| Profile | Minimum necessary contract |
|---|---|
| P0 test-only | Current five-field in-memory API is sufficient for unit semantics |
| P1 single-host crash-safe | Current CAS semantics plus explicit local durability contract; fencing may be unnecessary only if one trusted local authority writer is guaranteed and that assumption is documented |
| P2 multi-host service | Authority identity, server-side fencing, idempotent request IDs, authenticated read-back, declared replication durability |
| P3 high-assurance | P2 plus independent monotonic epoch/rollback anchor, separately administered recovery, independently corroborated observations, and evidence of fencing across failover |

P1 must not be used to claim host-root-resistant rollback protection. P2/P3 cannot be achieved by adding fields to a local struct alone; the independent administrative and operational boundaries must exist.

## 8. Current gate and next safe step

No API change is made. No profile is selected. No provider is selected. The current in-memory interface remains a test/reference abstraction only.

The next safe engineering step is a design review of the authority contract after the owner chooses the intended attacker boundary and deployment profile. Until then:
- retain current pure verifiers and in-memory tests;
- keep the Linux File Authority plan as a future P1 qualification plan only;
- do not implement a production file or remote adapter;
- do not enable dispatch, Federation Process Launch, VM execution, or auto-repair.
