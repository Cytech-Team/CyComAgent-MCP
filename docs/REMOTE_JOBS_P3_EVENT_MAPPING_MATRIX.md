# P3 Event Mapping and Verification Matrix

Status: design and review artifact only. This matrix describes the current V1 host-evidence behavior and a proposed P3 corroboration policy. It does not implement observer receipts, select an observation provider, or authorize production execution.

## 1. Evidence classes

| Class | Source | What it can establish | What it cannot establish alone |
|---|---|---|---|
| H: host assertion | D1 Host Runner / signed host journal | The runner's signed statement that it recorded an event | That the event actually occurred, that the host was uncompromised, or that the statement is complete |
| O: independent observation | D3 Observer Receipt plus retained source measurement | That the configured observer signed a claim derived from its declared source | That the source itself is trustworthy unless source control and measurement integrity are qualified |
| A: authority acceptance | D2 Checkpoint Acceptance Receipt | That the authority accepted a specific journal digest at a particular epoch/sequence under its declared durability contract | That the journal's events are true or independently observed |
| X: artifact bytes | Exact bytes matching manifest digest | Byte-level integrity relative to the manifest digest | Origin, collection integrity, or semantic truth |

A cryptographically valid H+A bundle is not equivalent to H+O+A. P3 lifecycle claims require all policy-required O evidence, and the observer's source must be qualified independently.

## 2. Current repository behavior (implemented V1)

The current code in internal/vmevidence has these facts:

- HostEventRecord fields are event_id, event_type, host_monotonic_ns, case_id, actor, and result.
- The accepted host event vocabulary is VM_START, VM_PAUSE, VM_RESET, VM_POWER_OFF, VM_DESTROY, OVERLAY_DESTROY, WATCHDOG_TIMEOUT, and HOST_OBSERVATION.
- The journal must exactly match the manifest host_events in order and field values; event IDs must be unique; host monotonic time cannot decrease.
- The journal lifecycle validator requires exactly one VM_START, one VM_DESTROY after VM_START, and one OVERLAY_DESTROY after VM_DESTROY. Other accepted event types may appear between these lifecycle events.
- EvaluateAgainstTrustedPlan additionally requires VM_START, VM_DESTROY, and OVERLAY_DESTROY with actor HOST_RUNNER and result RECORDED.
- Manifest teardown fields assert VM process absent, overlay absent, temporary resources absent, and VerifiedBy HOST_RUNNER. These are host assertions, not D3 observations.
- There is no Observer Receipt type, observer signature-purpose verifier, cross-receipt verifier, or D3 integration in the current Go implementation.

Consequently, current PASS evaluation does not prove independent lifecycle observation. It proves that the signed evidence package satisfies the current host-reported policy.

## 3. Lifecycle mapping matrix

| Logical fact | Existing H event / field | Proposed D3 event | Independent source needed | P3 acceptance rule | Current status |
|---|---|---|---|---|---|
| VM start requested | No dedicated required event; optional HOST_OBSERVATION may describe it | VM_CREATE_REQUESTED (request only) | Authenticated control-plane request/audit record | Treat as intent, never as proof VM exists | Not implemented |
| VM actually started | VM_START | VM_START_OBSERVED | Hypervisor/provider control-plane state transition or independently controlled host/hypervisor monitor | Must bind provider VM identity to run authorization and nonce; request receipt alone is insufficient | H required; O absent |
| VM paused | VM_PAUSE | VM_PAUSE_OBSERVED | Hypervisor state transition/audit | Only required when run policy expects pause; correlate exact state transition | Optional H vocabulary; O absent |
| VM reset | VM_RESET | VM_RESET_OBSERVED | Hypervisor reset operation plus resulting VM identity/state | Distinguish reset request from completed reset; define whether VM identity changes | Optional H vocabulary; O absent |
| VM powered off | VM_POWER_OFF | VM_STOP_OBSERVED | Hypervisor/provider state transition | Must not be treated as destruction; require subsequent destroy if policy requires it | Optional H vocabulary; O absent |
| VM destroyed | VM_DESTROY | VM_DESTROYED | Provider/hypervisor resource inventory or lifecycle audit proving resource no longer exists | Must refer to same immutable VM subject as VM_START_OBSERVED; absence must be measured after a bounded consistency interval | H required; O absent |
| Overlay destroyed | OVERLAY_DESTROY | OVERLAY_DESTROYED | Independent storage/control-plane inventory or externally controlled cleanup observer | Must bind overlay identity and show absence; a host cleanup log is not enough | H required; O absent |
| Watchdog fired | WATCHDOG_TIMEOUT | WATCHDOG_TRIGGERED | Independent watchdog/control-plane record if relied upon for safety claim | Correlate watchdog instance, deadline, run identity, and action; timeout claim alone does not prove process containment | Optional H vocabulary; O absent |
| Generic host observation | HOST_OBSERVATION | No generic D3 mapping | Must define a specific source and semantic event | Generic event must not satisfy a required lifecycle corroboration | Optional H vocabulary; O absent |
| Temporary resources absent | Teardown temporary_resources_absent boolean + digest | RESOURCE_SET_ABSENT | Independent inventory of explicitly enumerated resource set | Require stable resource IDs and complete inventory scope; boolean alone is insufficient | H assertion only |
| VM process absent | Teardown vm_process_absent boolean + digest | HOST_PROCESS_ABSENT (only if observer is outside D1 trust domain) | Independent host/hypervisor process or VM control-plane observation | If D1 host root is in threat scope, same-host process inspection is not independent | H assertion only |

D3 event names above are proposed normalized semantic labels, not current accepted host-journal event types. They must be versioned and tied to a concrete source adapter before implementation.

## 4. Required event correlation tuple

Every O receipt that corroborates an H event must match all applicable values:

- issuer_id and observer key purpose;
- runner_id, run_id, and run_nonce;
- host event_id and normalized event type;
- immutable provider/hypervisor subject ID;
- manifest-authorized VM/overlay identity or a digest binding to it;
- source_id and measurement digest;
- policy digest and mapping version;
- observer stream sequence and previous receipt digest.

Do not correlate solely by timestamp, display name, VM name, hostname, IP address, or caller-supplied event text. If the provider cannot bind a stable subject ID to the run authorization, classify the event as uncorrelated and fail the P3 requirement.

## 5. Event state and ordering rules

The normalized D3 vocabulary should describe observed states, not requests:

- VM_CREATE_REQUESTED is intent and cannot satisfy VM_START_OBSERVED.
- VM_START_OBSERVED means the independent source reports the VM entered the defined running state.
- VM_STOP_OBSERVED means the source reports a stopped/powered-off state; it does not prove resource destruction.
- VM_DESTROYED means the source reports the resource deleted/absent under a defined consistency rule.
- OVERLAY_DESTROYED means the independent source reports the exact overlay resource absent.

For the minimal lifecycle, P3 requires:

1. One VM_START host event and one matching VM_START_OBSERVED receipt.
2. One VM_DESTROY host event and one matching VM_DESTROYED receipt.
3. One OVERLAY_DESTROY host event and one matching OVERLAY_DESTROYED receipt.
4. D3 sequence continuity and receipt-chain validity across the required receipts.
5. D2 acceptance receipt that names the exact observer receipt IDs and accepted journal digest.

The order must be consistent across H and O streams. The observer's measured event order must not contradict the host journal order. Wall-clock timestamps alone are not used to order events across independent clocks; use each stream's monotonic sequence and a documented causal relation. If time intervals overlap within clock uncertainty, the verifier must report ordering as indeterminate unless a source-native sequence establishes it.

## 6. Conflict and missing-evidence matrix

| Condition | Classification | Required result | Forbidden behavior |
|---|---|---|---|
| H says VM_START; O says no matching VM | CONTRADICTION | Quarantine run; no P3 acceptance | Prefer H because it is signed |
| H says VM_DESTROY; O still sees VM running | CONTRADICTION | Quarantine; investigate stale or false teardown | Mark teardown successful |
| H says overlay destroyed; O inventory still contains overlay | CONTRADICTION | Quarantine; preserve both records | Auto-delete resource to make evidence pass |
| O reports event with no matching H event | UNBOUND_OBSERVATION | Preserve receipt; reject as corroboration for this journal | Silently attach to nearest timestamp/event |
| H event has no required O receipt | MISSING_CORROBORATION | Run incomplete / BLOCKED under P3 policy | Downgrade to host-only while retaining P3 label |
| Multiple O receipts claim same event with incompatible measurements | OBSERVER_CONFLICT | Quarantine and require review | Choose newest receipt automatically |
| O stream sequence gap or predecessor mismatch | OBSERVER_CHAIN_INVALID | Reject affected stream and later dependent receipts | Reset sequence or infer missing receipt |
| O source unavailable | OBSERVER_UNAVAILABLE | Pending/incomplete; no P3 PASS | Treat lack of negative evidence as success |
| Provider inventory is eventually consistent | OBSERVATION_PENDING | Wait within a declared bounded policy, then fail closed if unresolved | Claim immediate absence |
| O clock uncertainty makes timestamp ordering ambiguous | ORDER_INDETERMINATE | Use source-native causal sequence or mark unresolved | Compare timestamps as if exact |
| D2 accepted journal but receipt references wrong O IDs | AUTHORITY_RECEIPT_MISMATCH | Reject bundle | Trust D2 receipt without cross-check |
| D2 commit outcome unknown | AUTHORITY_OUTCOME_UNKNOWN | Reconcile exact request ID and body | Submit a new request ID or assume no commit |
| D1 host root compromise suspected | RUNNER_DOMAIN_COMPROMISED | Revoke/suspend D1 identity; quarantine affected runs | Trust D1 signatures as independent proof |
| D3 source/observer compromise suspected | OBSERVER_DOMAIN_COMPROMISED | Revoke observer identity; quarantine affected receipts | Replace evidence silently or keep P3 PASS |

No conflict is resolved by automatically mutating infrastructure. Remediation is a separate, explicitly authorized operational workflow and must not rewrite historical evidence.

## 7. Mapping cardinality and deduplication

- Each required H lifecycle event maps to exactly one accepted O receipt per required observer policy. If a policy requires multiple independent observers, each observer has a distinct issuer and stream.
- One O receipt must not satisfy multiple H event IDs.
- Duplicate receipt IDs, event IDs, or observer sequence numbers are rejected within their defined issuer/run scope.
- Additional non-required observations may be retained, but they cannot replace a missing required receipt.
- If a provider emits multiple raw records for one normalized event, preserve all raw records and define a deterministic aggregation rule in the versioned mapping policy. Never discard conflicting raw records.

## 8. Observer independence qualification checklist

Before an event mapping is enabled, record:

1. Source system and exact API/event feed used.
2. Administrative owner of that source and whether D1 host root can modify it.
3. Authentication and signing-key custody for D3.
4. Stable subject identity and binding to authorized run nonce.
5. Source event retention, retrieval, and immutability characteristics.
6. Replay, duplicate, delayed, reordered, and eventual-consistency behavior.
7. Clock source, uncertainty bound, and source-native sequence/correlation IDs.
8. Failure behavior and how missing data is distinguished from negative evidence.
9. Compromise scenarios and revocation/recovery procedure.
10. Test evidence from a real independent source; synthetic fixtures are not qualification evidence.

A second process, sidecar, container, or log on the same D1 host does not meet the independence requirement when D1 host root compromise is in scope.

## 9. Implementation implications

Before adding Go receipt verification:

- Freeze the normalized event vocabulary and mapping version.
- Select a real D3 source and prove its independent administrative boundary.
- Decide whether one observer or multiple observers are required for VM and overlay events.
- Define the exact source measurement bytes retained for each normalized event.
- Define authority policy for missing, delayed, contradictory, and eventually consistent observations.
- Add distinct OBSERVER_RECEIPT and CHECKPOINT_ACCEPTANCE key purposes; do not alias existing key purposes.
- Add pure verifier tests for matching, missing, duplicate, conflict, replay, wrong subject, wrong run identity, and indeterminate ordering.

The current V1 schemas should remain unchanged until a separately reviewed schema-version migration is approved. This matrix is not approval to implement a real observer adapter.

## 10. Pure Go verification model (implemented reference primitive)

internal/vmevidence/observer_verify.go adds VerifyObserverCorroboration, a deterministic in-memory validator over semantic receipt claims. It checks the configured issuer/run identity, policy digest, receipt and event uniqueness, observer sequence continuity, predecessor digest chain, host-event correlation, normalized event and subject-kind mapping, host/observer order, required receipt presence, and VM subject continuity across VM lifecycle observations.

The function also requires a policy-defined subject kind for every mapped host event (for example, VM_INSTANCE for VM lifecycle events and OVERLAY for overlay destruction), and enforces VM subject continuity across VM lifecycle observations. It accepts only claims whose exact payload digest, signature, issuer authorization, and source measurement have already been verified by a separate layer. It does not parse or sign receipt payloads, verify D3 source truth, compare cross-clock intervals, persist anti-replay state, contact an authority, or authorize execution. The supplied slice must contain the relevant stream receipts in sequence order. This is a pure validation primitive and its unit fixtures are not evidence of a real independent observer.

## 10. Decision record

- Event vocabulary in current code: verified against internal/vmevidence/manifest.go, journal.go, and plan.go.
- D3 provider/source: not selected.
- Observer count/quorum: not selected.
- Clock/causal ordering policy: proposed rules above; implementation parameters not selected.
- P3 production baseline: not selected by this document.
- Production services, credentials, listeners, dispatch, VM execution, and auto-repair: not authorized.
