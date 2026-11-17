# CyComAgent Evolution Plan

## Product direction

Evolve CyComAgent into a multi-client personal agent runtime and desktop automation platform while preserving its defining boundary:

- AI clients own reasoning, model selection, and user-facing planning.
- CyComAgent owns capability discovery, controlled execution, durable job state, policy enforcement, and observability.
- Workflows are composable primitives or optional modules; the core must not become coupled to one model vendor or one application.

## Existing architecture to preserve

- MCP HTTP/stdio transports and the current `/mcp` contract.
- Registry-wide policy and audit interceptors.
- Local loopback HTTP default, token requirement for non-loopback binds, and Origin validation.
- Root privilege broker over a peer-credential-checked Unix socket.
- Login-session bridge for GUI/session-sensitive operations.
- Persistent jobs, explicit state namespaces, durable remote target definitions, and external plugin manifests.
- Existing Linux, macOS, Windows, and Termux platform boundaries.
- Audit JSONL format remains append-only and backward-compatible; new fields are optional.

## Current baseline

Already present in the source tree: MCP server, tool registry, policy engine, audit logger, plugin loader, state store, job manager, target manager, desktop runtime capability selection, Linux session bridge, root broker client, browser bridge, health/readiness/metrics endpoints, watchdog and recovery scripts, and Inspector V2.

Known observability limitation: active audit calls were keyed by a deterministic hash of tool + arguments. Concurrent identical calls could overwrite each other. The first evolution slice introduces independent Trace IDs and Call IDs without storing raw arguments.

## Target architecture

```text
AI clients (ChatGPT / Codex / Claude / Cyonara / other MCP clients)
                         |
                MCP / optional API adapters
                         |
                Control & coordination plane
        Mission Control / sessions / approvals / tasks
                         |
          Generic runtime and workflow primitives
       Registry / policy / jobs / state / plugin SDK
                         |
           Platform capability adapters / bridges
      Desktop / browser / SSH / Android / filesystem
                         |
            Local OS and durable remote targets
```

Observability and audit wrap every execution boundary. They must not expose raw arguments or secrets by default.

## Delivery phases

### Phase 0 — Baseline and compatibility guardrails

- Record current source tree and working-tree changes before touching code.
- Run unit tests, race tests, vet, build, MCP smoke, and health checks.
- Preserve all pre-existing modified/untracked user work; no reset/checkout/clean operations.
- Keep current ports, service units, paths, MCP tool names, schemas, and state formats compatible unless a versioned migration is explicitly added.

### Phase 1 — Observability foundation

- Add per-request Trace ID and per-tool Call ID propagation.
- Fix active-call identity collisions for concurrent identical calls.
- Add trace/call IDs to audit events and Inspector detail/export.
- Add stable error codes and safe error classification.
- Add trace lookup and grouped execution timelines after correlation data is proven.
- Preserve old JSONL records and never persist raw argument payloads.

### Phase 2 — Mission Control read model

- Live execution queue and active sessions.
- Task/session explorer with lifecycle states.
- Trace timeline, dependency/call graph, and incident grouping.
- Runtime health, adapter availability, job state, and remote-target status.
- Read-only first; no new execution authority in the dashboard by default.

### Phase 3 — Workflow primitives

- Durable workflow definitions and versioned schemas.
- DAG dependencies, retries with bounded policies, timeouts, cancellation, and checkpoint/resume.
- Idempotency keys and per-step audit correlation.
- Human approval nodes for risky operations.
- AI remains the planner; the runtime executes explicitly supplied workflow graphs.

### Phase 4 — Security and governance

- Named permission profiles and per-tool/per-target grants.
- Approval policies for destructive, privileged, remote, and externally visible actions.
- Policy simulation/dry-run and explainable denial reasons.
- Secret redaction at log/display/export boundaries.
- Emergency stop, concurrency limits, quotas, and execution budgets.
- Clearly document that policy is not an OS sandbox; use OS/container isolation where required.

### Phase 5 — Automation engine

- Durable scheduler with timezone-aware schedules and missed-run behavior.
- Event triggers for service health, filesystem, webhook, and job completion.
- Retry/backoff, deduplication, and delivery status.
- Notifications through optional plugins; no hidden outbound network behavior.

### Phase 6 — Developer ecosystem

- Versioned Plugin SDK and manifest validation.
- Signed/trusted plugin metadata and explicit permissions.
- Capability/API explorer and test harness.
- Optional adapters for Docker/Podman, Kubernetes, Minecraft/Pelican, and GPU tooling.
- Keep domain integrations outside the universal core.

### Phase 7 — Reliability and release engineering

- Cross-platform build/test matrix.
- SBOM, provenance, signed releases, and dependency policy.
- Chaos/recovery tests for runtime, tunnel, bridge, jobs, and storage failures.
- Upgrade/rollback tooling with state backup and compatibility checks.
- Performance benchmarks and bounded resource usage.

## Non-negotiable acceptance criteria

1. Existing MCP clients continue to discover and call the same tools.
2. `/mcp`, `/livez`, `/readyz`, `/health`, `/metrics`, and `/inspector` remain available on the existing configured listener.
3. Existing state and audit data remain readable; migrations are additive and reversible.
4. Concurrent identical calls are independently visible and cancellable.
5. No raw tool arguments, credentials, or environment secrets are written to audit by default.
6. Every destructive/privileged workflow step is policy-checked and auditable.
7. No automatic retry of non-idempotent actions without explicit idempotency semantics.
8. A failed upgrade can restore the previous executable and leave durable state intact.
9. All pre-existing working-tree modifications are preserved.

## Immediate implementation order

1. Complete Trace/Call ID propagation and concurrency regression tests.
2. Add trace-aware Inspector detail/search/export.
3. Add trace query/grouping and stable error taxonomy.
4. Build a read-only Mission Control data model from existing jobs, targets, health, audit, and capabilities.
5. Only then add workflow execution, approval, scheduler, and plugin SDK.

## Baseline portability finding

- Linux build/test is the active deployment baseline.
- macOS arm64 and Android arm64 cross-builds pass for the current source tree.
- A full Windows cross-build currently fails in existing process/job/plugin/target code because it uses POSIX-only `syscall.SysProcAttr` fields and process-group/signal APIs (`Setpgid`, `Setsid`, `Getpgid`, `Kill`, `SIGUSR1`, `SIGUSR2`). This is a pre-existing platform abstraction gap, not introduced by Trace Foundation. Address it in the dedicated process-group/platform-adapter milestone rather than adding scattered OS checks to unrelated packages.

## Reliability audit finding: Job snapshot race

The first repository-wide race-detector pass exposed a pre-existing data race in `internal/jobs.Manager`: `Get` released its read lock before cloning the internal `*Job`, and `Start`/`StartExternal` cloned a returned internal pointer after background monitor goroutines could mutate it. The manager now clones snapshots while holding the appropriate lock and returns only detached snapshots. Existing Job JSON/state formats and public method signatures are unchanged.

## Progress status

- [x] Phase 0 baseline and compatibility guardrails recorded.
- [x] Phase 1 Trace/Call ID foundation, Inspector integration, concurrent-call regression coverage, and backward-compatible audit reads.
- [x] Job Manager snapshot race fixed and verified with repository-wide race detection.
- [x] Phase 1 runtime deployed to the existing local service listener; existing MCP and Inspector routes retained.
- [ ] Phase 2 read-only Mission Control model and trace explorer.
- [ ] Phases 3–7 as listed above.

The current working tree intentionally remains uncommitted. Existing user changes were preserved; no reset, checkout, clean, or commit operation was performed.

## Phase 2 progress: Mission Control read model

- [x] Added a read-only `Runtime.MissionControl()` projection for runtime metadata, counts, tool registry, jobs, targets, loaded plugins, active calls, recent audit events, and desktop adapters.
- [x] Sensitive fields are intentionally excluded: job commands/environments, SSH host/user/identity paths, raw audit errors/reasons, active-call reason text, and display socket values.
- [x] Added `/mission-control`, embedded responsive dashboard assets, and `/mission-control/api/snapshot`.
- [x] Snapshot endpoint enforces the configured CyComAgent token using `X-CyCom-Token` or Bearer auth when a token is configured. The dashboard prompts for the token and retains it only in the current tab's session storage.
- [x] Dashboard is read-only and refreshes every five seconds; it does not add execution authority or modify existing MCP tools.
- [x] Added runtime redaction tests, dashboard asset/method tests, and token validation tests.
- [x] Full repository tests, full race detector, vet, production build, and live HTTP smoke checks passed; deployed to the existing service with pre-deploy binary backups.

Remaining Phase 2 work: trace grouping/drill-down, session lifecycle inventory, and richer adapter health probes. Current desktop adapter information is capability detection, not a live end-to-end input/capture health check.

## Phase 2 progress: Trace Explorer

- [x] Added `GET /mission-control/api/traces` with bounded audit reads, trace ID filtering, tool/call/trace text search, chronological per-trace calls, and success/failure aggregation.
- [x] Legacy audit entries without Trace IDs are isolated by Call ID (or timestamp fallback); they are never merged into a false shared trace.
- [x] Added an expandable Trace Explorer to Mission Control with live refresh and client-side filtering.
- [x] Trace API exposes only call ID, timestamp, tool name, duration, and success state; raw error strings, reasons, and argument hashes are excluded.
- [x] Added tests for grouping, chronological ordering, filtering, legacy events, and outcome counts.
- [x] Full repository tests, race detector, vet, production build, frontend syntax check, and live HTTP/redaction smoke tests passed.
- [x] Deployed with timestamped backups of both installed binaries; service remains active and readiness returns HTTP 200.

Session lifecycle inventory remains deferred: the current runtime has MCP request contexts and persistent Jobs, but no authoritative durable Session Manager. Do not infer sessions from jobs or trace IDs.

## Phase 2 progress: Persistent Session Manager

- [x] Added `internal/sessions` as the authoritative Agent-session registry, separate from desktop login sessions and process Jobs.
- [x] Added generated `ses_` IDs, owner/name metadata, timestamps, lifecycle states (`created`, `active`, `idle`, `expired`, `closed`, `interrupted`), and Parent/Child relationships.
- [x] Added atomic, private (`0600`) JSON persistence under the runtime state directory; corrupt state fails startup rather than silently discarding sessions.
- [x] Added restart recovery: previously `created`/`active` sessions are marked `interrupted`, never silently resumed as active.
- [x] Added lifecycle rules: terminal sessions cannot be reopened; parents cannot close/expire while non-terminal children exist; trace IDs can be attached to sessions.
- [x] Added MCP tools: `session_create`, `session_list`, `session_get`, `session_touch`, `session_transition`, and `session_attach_trace` (existing `session_logout` remains separate).
- [x] Added a redacted Session inventory to Mission Control, including status, age, child count and trace count.
- [x] Tests cover hierarchy rules, lifecycle, recovery, trace attachment, defensive copies, corrupt persistence, and Mission Control projection.
- [x] Full Go tests, race detector, vet, production build and frontend syntax check passed; deployed with timestamped backups and live readiness verification.

Remaining Phase 2 work: automatic propagation of Session ID through MCP request context into Audit/Trace and Job creation; Session Manager UI detail/drill-down; retention/expiry policy and safe pruning. Do not claim automatic trace association until request-context propagation is implemented.

## Phase 2 progress: Session Context Propagation

- [x] Added Session ID to the request-scoped trace context; no global mutable session state.
- [x] MCP `tools/call` accepts optional `_meta.sessionId`, validates it against the authoritative Session Manager, and touches the session before invoking the tool. Invalid/terminal/missing sessions are rejected without running the tool.
- [x] Audit events now persist `session_id` alongside trace/call IDs.
- [x] `process_spawn` persists the originating Session ID on managed service and desktop/user Jobs; legacy job records remain compatible.
- [x] Mission Control job/audit projections and Trace Explorer call details expose the Session ID; trace search can match it.
- [x] Integration tests verify handler context, session activity, audit linkage, invalid-session rejection, and Job persistence.
- [x] Full Go tests, race detector, vet, production build, frontend syntax and diff checks passed.

Protocol note: clients should send `params._meta.sessionId` on `tools/call` to associate work with a Session. Calls without it retain existing behavior and remain unscoped.

## Phase 2 progress: Session Detail Drill-down

- [x] Added authenticated GET `/mission-control/api/sessions/{session_id}` with strict GET-only behavior and no-store responses.
- [x] Added a redacted Session detail projection: parent, child sessions, linked Job summaries, attached Trace IDs, and Session-linked Audit timeline.
- [x] Mission Control Sessions are now selectable and open an inline detail panel; child sessions can be opened recursively.
- [x] The detail projection deliberately excludes Job commands, environment variables, raw audit arguments, hashes, error strings, and reason text.
- [x] Added Runtime tests covering hierarchy and linked Job/Trace/Audit records plus sensitive-data redaction.

## Phase 2 progress: Session Retention & Safe Pruning

- [x] Added `session_prune` MCP tool; `dry_run` defaults to true and actual deletion requires explicit `dry_run=false`.
- [x] Retention age is explicit (`retention_days`, minimum 1 day, maximum 100 years); only terminal sessions older than cutoff are considered.
- [x] Pruning is leaf-only and non-cascading. A parent remains while any child metadata exists; after pruning leaves, a later pass can consider the parent.
- [x] Sessions linked to non-terminal Jobs are protected. Unknown Job statuses are treated as non-terminal (fail-safe); only known exited/terminated states are considered complete.
- [x] Prune rechecks status, age, child references, and protected IDs under the Session Manager lock; persistence failure restores the in-memory snapshot.
- [x] Parent child references are updated when a child is pruned.
- [x] Audit and Trace stores are never modified by Session pruning. Trace IDs and historical Audit records remain in their independent stores.
- [x] Tests cover dry-run non-mutation, terminal leaf-only pruning, hierarchy integrity, and active-work protection.
- [ ] No automatic retention scheduler is enabled. Operators/agents must inspect the dry-run plan and explicitly request deletion.

## Phase 2 progress: Workflow Foundation

- [x] Added durable Workflow DAG model with unique step IDs, explicit dependencies, cycle detection, and a 100-step limit.
- [x] Added MCP tools `workflow_create`, `workflow_list`, `workflow_get`, and `workflow_advance`.
- [x] Workflow creation is declarative and does not execute commands. Execution requires explicit `workflow_advance`.
- [x] Ready steps dispatch as persistent Jobs; every Job inherits the Workflow Session ID.
- [x] Dependency gating: a step dispatches only after every dependency reaches `succeeded`.
- [x] Bounded parallelism (1-16, default 1); workflow steps run in the service execution domain only. No implicit desktop or privileged execution.
- [x] Job reconciliation maps success/failure/termination to step and workflow states.
- [x] Dispatch reservation is persisted before process launch. An ambiguous crash window is recovered as `interrupted`, never silently retried.
- [x] Existing Job IDs prevent duplicate dispatch. No automatic retry or replay is implemented.
- [x] Tests cover DAG execution order, dependency gating, cycle rejection, and recovery without duplicate execution.
- [ ] Future: explicit retry policy with operator approval, cancellation propagation, compensation steps, richer graph visualization, and resumable workflow UI.

## Phase 2 progress: Workflow Recovery & Cancellation

- [x] Added `workflow_cancel`: stops future dispatch, marks pending steps cancelled, sends TERM to active Job process groups, and retains `cancelling` until Jobs become terminal.
- [x] Cancellation is cooperative (TERM); it does not silently escalate to KILL. Operators can inspect Job state and decide on further action.
- [x] Ambiguous `dispatching` steps become `interrupted`; no claim is made that the process definitely did or did not start.
- [x] Added `workflow_retry_step`: explicit per-step retry only for failed/cancelled/interrupted states, only after dependencies succeed, and only when a known linked Job is terminal.
- [x] An interrupted step without a Job ID is not retryable because its dispatch outcome is ambiguous.
- [x] Retry resets one step to pending; execution still requires a separate `workflow_advance` call.
- [x] Added tests for TERM cancellation, pending-step suppression, explicit retry, and ambiguous dispatch protection.
- [ ] Future: cancellation grace period with separately-approved force kill, dependent-step retry planning, compensation/rollback steps, and Mission Control graph controls.

## Phase 2 progress: Mission Control Workflow Visualizer

- [x] Mission Control snapshot now includes a dedicated redacted Workflow DAG projection.
- [x] Workflow cards show status, step progress, dependency edges as prerequisite labels, linked Job IDs, and per-step lifecycle status.
- [x] The projection intentionally excludes shell commands, working directories, shell executable names, and raw error text.
- [x] Existing five-second Mission Control refresh updates Workflow state without a separate dashboard or execution API.
- [x] Visualizer is read-only; lifecycle actions remain explicit MCP tools (`workflow_advance`, `workflow_cancel`, `workflow_retry_step`) so the dashboard does not silently gain execution authority.

## Phase 2 progress: Interactive Workflow Graph & Step Inspector

- [x] Workflow DAG is grouped into dependency stages; nodes are selectable and show prerequisite links.
- [x] Step Inspector shows status, created/updated timestamps, elapsed state window, incoming dependencies, downstream dependents, and linked Job ID.
- [x] Dependency/dependent chips navigate directly to related Steps; selection survives periodic dashboard refreshes.
- [x] Inspector is read-only and continues to omit commands, CWD, shell, and raw errors.
- [x] Added Mission Control projection test proving DAG dependencies are present while execution details remain redacted.
- [ ] Future: true event-sourced per-step transition history and richer visual edge routing for complex DAGs.

## Phase 2 progress: CyCom Fleet Discovery & Version Tracking

- [x] Read-only Tailscale peer discovery via `tailscale status --json` (self, hostname, OS, Tailscale IPs, online/offline, last-seen).
- [x] Strict SSH-only peer probe (`BatchMode`, host-key checking enabled, no password or keyboard-interactive auth) to detect CyComAgent and read its `--version`; no arbitrary remote command execution.
- [x] Authenticated GitHub main-branch `VERSION` and commit SHA lookup via the user's existing `gh` credential store; credentials are not copied to CyCom state or logs.
- [x] Per-device current/behind/version-mismatch/not-installed/offline/unknown status in Mission Control; 45-second refresh cache.
- [x] MCP `fleet_discover` read-only inventory and explicit `fleet_pair` action. Pairing requires `confirm=true`, online peer, successful existing SSH public-key auth, and installed CyComAgent; creates a normal durable SSH target without copying keys or changing SSH trust.
- [x] Fleet version/commit parsing and Tailscale JSON decoding tests.
- [ ] Roll out the Fleet-capable build to Home Server, Server NamKrub and Cyoria-Relay; Relay SSH authorization currently unavailable from this desktop.
- [ ] Add authenticated agent-to-agent RPC and approval-scoped remote tool delegation after the Fleet inventory is stable.

### Fleet deployment verification (2026-11-08)

- [x] Deployed Fleet-capable build to the current NamKrub desktop runtime; `/readyz` returns 200 and Mission Control exposes live Fleet data.
- [x] GitHub `main` currently reports version `0.4.8-isolated-desktop-dev`, commit `6c6a781a4d457521b25d819b879b865f1e45d693`.
- [x] Current desktop is at that commit (local working tree build is marked dirty).
- [x] `namkrub-home-server` and `namkrub-server` both have CyComAgent installed at commit `d797e2354082` and are correctly flagged behind GitHub.
- [x] Paired both servers as `cycom-namkrub-home-server` and `cycom-namkrub-server`; both `target_probe` checks succeeded.
- [x] Verified bidirectional Tailscale visibility: Home Server ↔ Server NamKrub and both ↔ Cyoria-Relay respond to `tailscale ping`.
- [ ] Cyoria-Relay is online in Tailscale but is not yet SSH-authorized from this desktop; strict host-key/auth checks prevent pairing. Android peer `2107113SG` is currently offline.

## Remote Job execution security gate (2026-11-08)

Remote Job execution remains disabled. Federation identity/trust authenticates a peer; it does not grant that peer arbitrary shell execution on the receiver. The existing generic MCP policy (`allow_tools`, `deny_tools`, `allow_remote_targets`, `mode`) is not a sufficient command-level authorization boundary for Remote Jobs.

Before adding a receiver dispatch endpoint, implement and test a separate local execution policy with these mandatory properties:

- Disabled by default; no implicit enablement from a trusted Federation peer or `mode=full`.
- Explicit per-caller NodeID authorization, with revocation and fail-closed behavior.
- Structured executable + argv payload; do not accept a shell command string or invoke `sh -c` for remote requests.
- Exact executable allowlist and constrained working-directory roots; no arbitrary executable path, shell metacharacter interpretation, or unbounded environment injection.
- Explicit timeout, output, process-count and resource limits; privileged/root execution forbidden.
- Receiver-local approval/configuration only; Caller cannot change its own permissions.
- Signed dispatch proof with a distinct purpose domain, binding receiver NodeID, caller NodeID, request ID, immutable payload hash, and policy version.
- Durable idempotency gate before process creation; one stable Remote Job ID mapped to one local Job ID. Any ambiguous dispatch outcome becomes `unknown` and is never automatically replayed.
- Reconcile must compare the exact ledger record and local Job record, verify immutable payload hash and process identity, and only move `unknown` to a terminal/observed state when evidence is authoritative.
- Audit both allowed and denied dispatch attempts, caller identity, policy decision/version, request/job IDs, and outcome; do not log secrets or arbitrary environment values.

Required tests before exposing the endpoint: default-deny, untrusted caller, caller revocation, disallowed executable, argv injection, cwd escape/symlink escape, environment injection, timeout/output caps, duplicate concurrent dispatch, crash before/after spawn, lost response, receiver restart, caller restart, and reconciliation against mismatched/missing Job records. Production nodes must not receive this feature until all gates pass.
