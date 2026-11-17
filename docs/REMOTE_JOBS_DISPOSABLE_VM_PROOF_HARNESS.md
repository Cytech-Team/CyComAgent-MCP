# Remote Jobs: Disposable VM Proof Harness

## Status and safety boundary

Design specification only. This document does not create a VM, install a guest image, run a payload, build a helper, change host virtualization settings, or provision any account, service, listener, cgroup, or systemd unit. It is not a dispatch authorization. The current implementation remains non-executable and `WorkerBackendPreparation.ExecutionDisabled()` must remain true.

The harness is intended for a later, separately approved kernel/containment prototype. It must never target the developer workstation, Home Server, Relay VPS, production CyoriaSMP infrastructure, or any machine holding production credentials.

## 1. What qualifies as a proof environment

A qualifying run uses a hardware-virtualized disposable Linux VM with a pinned guest kernel and pinned guest root image. The VM must have an isolated writable overlay that is destroyed after the run. A container (`systemd-nspawn`, Docker, or Podman) is not a substitute: it shares the host kernel and cannot prove guest-kernel crash isolation, host/guest boundary behavior, or kernel-version-specific execution semantics.

Host discovery on the current development machine found:

- `qemu-system-x86_64`: unavailable.
- `qemu-system-aarch64`: unavailable.
- `virt-install` and `virsh`: unavailable.
- Docker and Podman: unavailable.
- `systemd-nspawn`: present, but container-only and therefore not accepted as the kernel proof environment.

No VM was created or started. A future proof run is blocked until an approved disposable VM runner is provisioned separately.

### Required isolation profile

- QEMU/KVM or another reviewed type-1/hardware-virtualized runner; no host-kernel sharing.
- Pinned guest kernel, initramfs/root image, architecture, QEMU version, machine type, and CPU model/features.
- Per-run disposable overlay on a dedicated scratch volume with a hard size limit; destroy overlay and VM state after collection.
- No bridged, NAT, user-mode, or host-only guest network. No guest NIC device at all unless a separately reviewed test explicitly requires one.
- No host home directory, SSH agent, D-Bus socket, Docker socket, systemd socket, credential directory, or writable host filesystem mount passed into the guest.
- No shared clipboard, drag/drop, USB passthrough, GPU passthrough, or host device passthrough.
- No guest agent or general-purpose host command channel. If a result channel is required, use a narrow, documented, one-purpose channel inaccessible to the workload and verify that access control from inside the guest.
- Serial console is diagnostic output only. Treat guest-produced text as untrusted evidence because a workload may be able to forge console output.
- VM lifecycle control remains host-side and limited to start, pause, reset, power-off, and destroy. The harness must not provide a guest-to-host shell or file API.
- CPU, RAM, disk, process count, wall-clock duration, and log size are bounded. Host watchdog destroys the VM on timeout.

A disposable VM is a containment test environment, not proof that the proposed worker is secure on every host. It must be complemented by source review and tests on each explicitly supported kernel/systemd/filesystem/architecture combination.

## 2. Test layers and separation of responsibility

### Layer A — pure policy and artifact parser tests

Run in ordinary Go CI with synthetic byte buffers and temporary files. No process is launched. Verify ELF header parsing, architecture/ABI allowlists, shebang rejection, artifact metadata binding, digest mismatch rejection, and all request/attempt state transitions.

### Layer B — kernel syscall probe guest

Run a small, fixed, reviewed probe inside the disposable guest. It tests only kernel API behavior using inert synthetic ELF fixtures built specifically for the test. It must not accept caller-supplied paths, argv, env, shell text, URLs, or arbitrary binaries. It records syscall return values and errno for:

- `execveat(AT_EMPTY_PATH)` with a verified ELF FD.
- Descriptor close-on-exec behavior and post-exec FD table.
- `fchdir` from an already-open directory FD.
- `pidfd_open` and `clone3(CLONE_PIDFD)` availability and lifecycle behavior.
- `clone3(CLONE_INTO_CGROUP)` availability and documented failure cases, only inside a test-owned delegated cgroup subtree.
- Gate EOF, invalid token, timeout, and parent-death behavior.

This layer is not a worker backend. It must not accept production artifact records or connect to CyComAgent.

### Layer C — containment proof prototype

Only after a separate prototype approval, test the complete trusted-helper setup inside the disposable guest. Use synthetic fixtures and a fixed test policy. Test that no payload instruction executes until the gate is released, and that the payload cannot access supervisor control resources, identity material, or cgroup control.

The prototype must use a one-shot test journal and isolated test-only cgroup subtree. It must not install a persistent systemd service or account. If a systemd manager is required for the experiment, use a fresh guest image with an ephemeral system manager and destroy the VM after each test batch.

### Layer D — crash and adversarial fault injection

The host-side runner injects VM pause/reset/power-off at declared transition barriers. Guest-side fault injection may terminate only the test supervisor/helper process in the disposable VM. Never inject faults into host PID 1, host cgroups, host storage, or any production service.

## 3. Required run phases

1. **Preflight:** verify runner identity, VM image digest, guest kernel digest/version, QEMU version, architecture, cgroup mode, systemd version, filesystem/mount options, and that no network or host mounts are attached.
2. **Fixture preparation:** generate a fresh test-only signing key if signing is part of the prototype; generate minimal inert ELF fixtures for each test architecture; record all hashes. Never copy production keys, payloads, artifacts, or state into the guest.
3. **Snapshot:** create a clean disposable overlay from the pinned read-only base image. Record the base image digest and overlay identity.
4. **Boot:** start the VM with no NIC and no shared writable host directory. Wait only for the fixed guest test runner, not a general guest shell.
5. **Baseline:** capture kernel, systemd, cgroup, mount, LSM, architecture, and capability facts before testing.
6. **Execute test case:** run one case at a time with unique run ID, fixed policy, fixed timeout, and bounded output. Preserve each test's exact inputs and expected result.
7. **Fault injection:** perform only the fault scheduled for that test case. Record the host-side action and monotonic timestamp.
8. **Collect:** gather the host-generated run manifest and guest evidence. Mark guest-generated evidence as untrusted unless its channel is proven inaccessible to the tested workload.
9. **Teardown:** power off and destroy the VM and overlay, verify no VM process remains, and verify no test cgroup or temporary artifact was created on the host.
10. **Review:** compare observed results with expected outcomes; any missing, ambiguous, or contradictory evidence is a failed proof, not a skip or pass.

## 4. Evidence and provenance contract

Each run must produce a machine-readable manifest containing at least:

- Run ID, test case ID, UTC start/end, host monotonic timing, host runner identity and version.
- VM machine type, CPU model/features, memory/vCPU limits, QEMU version, guest image digest, guest kernel release/build ID, architecture.
- Guest systemd version, cgroup v1/v2 mode, mount table/options relevant to executable and CWD FDs, LSM mode, seccomp availability, namespace support.
- Fixture names, ELF class/data/machine/type, file size, SHA-256, descriptor flags, expected argv/env policy.
- Syscall names, arguments after redaction, return values, errno, and whether each operation was attempted or skipped.
- Journal transition sequence, start-attempt ID, child identity evidence, pidfd lifecycle evidence, cgroup membership evidence, gate state, terminal outcome.
- Host-injected pause/reset/power-off events with monotonic timestamps.
- Guest evidence source and trust classification (`host-observed`, `guest-trusted-channel`, or `guest-untrusted-log`).
- Final decision per test: `PASS`, `FAIL`, or `BLOCKED`. There is no implicit pass for missing evidence.

The manifest must not contain secrets, host home paths, production identifiers, private keys, or raw environment values. Hashes and opaque test IDs are preferred.

## 5. Expanded proof matrix

| Area | Cases | Acceptance condition |
|---|---|---|
| VM isolation | no NIC, no host mount, no agent, overlay teardown | Host resources are not reachable from guest; teardown leaves no persistent state |
| FD execution | ELF FD, rename/unlink, CLOEXEC, wrong architecture, malformed ELF | Exact admitted inode is used or execution fails closed; no path reopen or interpreter fallback |
| Descriptor hygiene | enumerate descriptors before/after exec; inject unexpected FD | Only explicitly permitted standard streams remain; no broker, journal, CWD, peer, or gate FD leaks |
| CWD | rename/unlink after open, replace pathname, change mount view | `fchdir` uses admitted directory object or fails closed |
| Gate | EOF, partial token, extra bytes, wrong token, duplicate release, timeout | Every non-exact one-shot release aborts; payload does not execute |
| Parent/supervisor death | death before gate, at durable release intent, after release before ack | Pre-release child aborts; ambiguous post-release state becomes unknown; no replay |
| PID identity | PID churn, concurrent children, pidfd close/reuse, unsupported clone3 | Lifecycle actions target pinned identity; unsupported path fails closed |
| cgroup | wrong FD, non-delegated subtree, migration denied, controller unavailable, cleanup race | No payload release without verified containment; no writes outside delegated subtree |
| Descendants | fork, double fork, setsid, daemonize, thread flood, fork bomb within limits | Every descendant remains in the boundary; resource limits stop exhaustion; cleanup proves subtree empty |
| Privilege | setuid/setgid, capabilities, ptrace, procfs, namespace creation, mount, BPF, keyring | No privilege regain, supervisor access, or policy bypass |
| Manager faults | systemd restart, D-Bus loss, delayed/duplicate reply, manager unavailable | No ambiguous acknowledgement causes replay; reconciliation blocks new starts until safe |
| Resource bounds | memory, CPU, pids, FD count, output flood, disk fill, wall timeout | Each limit is enforced and supervisor remains responsive enough to terminate the job |
| Storage faults | guest overlay full, journal write failure, torn write, reboot at each transition | Durable state is either valid and recoverable or explicitly unknown; no duplicate execution |

A test that depends on a kernel feature not present on a supported target is `BLOCKED` for that target. It cannot be silently skipped while claiming that target is supported.

## 6. Fault injection barriers

The prototype must expose named barriers to the test controller, without a production control API:

- `after_request_reserved`
- `after_child_created_before_identity_pinned`
- `after_identity_pinned_before_cgroup`
- `after_cgroup_placement_before_sandbox`
- `after_sandbox_before_containment_journal`
- `after_containment_journal_before_release_intent`
- `after_release_intent_before_gate_write`
- `after_gate_write_before_running_ack`
- `after_payload_exit_before_terminal_journal`

For each barrier, test abrupt supervisor death, VM reset, journal write failure, and lost acknowledgement where applicable. The expected recovery result must be defined before running the test. A barrier name is not permission to expose an arbitrary kill/PID/signal interface to a remote caller.

## 7. CI integration boundary

Current repository CI runs Go unit tests and vet across Go 1.23–1.25, with Linux static builds and cross-platform builds. It does not provision a pinned nested-virtualization guest, does not test `execveat`/pidfd/cgroup semantics, and cannot be treated as a kernel containment proof.

Recommended future separation:

- Keep parser, state-machine, protocol, and persistence tests in ordinary CI.
- Add a manually approved, isolated VM job only after runner hardening and image pinning are reviewed.
- Require explicit kernel/systemd/architecture matrix entries and attach the evidence manifest to the run.
- Never run the containment prototype as a privileged pull-request job on an untrusted branch.
- Never expose production secrets to the VM job.
- Make a missing runner or missing kernel capability `BLOCKED`, not `PASS`.

Existing `scripts/chaos-test.sh` and `scripts/recovery-test.sh` exercise the main CyComAgent runtime and must not be repurposed for this worker proof: they operate on systemd services or start a local runtime and are not isolated VM harnesses.

## 8. Exit criteria and approval gates

The VM proof is complete only when:

1. VM isolation and teardown are independently reviewed.
2. Every required test case has a reproducible fixture, expected result, and provenance-qualified evidence.
3. FD execution and CWD handling pass on every supported filesystem/kernel/architecture combination.
4. The child cannot execute before verified containment and durable release intent.
5. PIDFD identity, cgroup membership, resource controls, and descendant cleanup are independently evidenced.
6. Every crash point yields a safe terminal or unknown/reconcile state with no replay.
7. No test requires production credentials, host services, persistent accounts, or a network listener.
8. An independent reviewer accepts the test design and evidence.

Completion of this VM proof does not itself authorize production implementation or dispatch. The separate Protocol, Kernel Execution, Containment, Durability, Adversarial Review, and Dispatch Gate Review approvals remain mandatory.

## 9. Machine-readable evidence contract

The strict manifest, host/guest evidence trust classes, detached host signature, anti-replay rules, independent aggregate decision algorithm, and teardown proof requirements are specified in [REMOTE_JOBS_VM_EVIDENCE_MANIFEST.md](REMOTE_JOBS_VM_EVIDENCE_MANIFEST.md). A guest-provided PASS string or guest console log is never sufficient evidence for a passing test.
