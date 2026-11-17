# Remote Jobs: Dedicated Execution Identity Design

## Decision

Do not treat a per-user systemd scope as a security boundary for untrusted Remote Jobs. The current launcher is useful for ordinary descendant lifecycle control, including descendants that call `setsid()`, but a workload running as the same Unix UID can reconnect to that UID's systemd user manager and request a separate transient unit.

Remote Job execution must remain unavailable to federation callers until a dedicated execution identity and system-manager-owned lifecycle boundary exist.

## Evidence from the current host

The current CyComAgent runtime model runs the main agent as the configured desktop/login user (`User=%i` in `cycomagent@.service`). The Linux launcher invokes `systemd-run --user`, so the launched workload shares the agent's UID and user-manager authority.

The development host has a working system systemd manager and a user systemd manager, but no `cycomagent-worker` account. A non-privileged `systemd-run --system` capability probe did not complete authorization and created no transient unit. This confirms that the agent cannot safely create system services directly as an ordinary user.

The existing `cycomagent-root` broker is not an acceptable execution bridge: its current protocol accepts a shell command and runs it as root. Reusing or extending that unrestricted shell surface for Remote Jobs would turn a containment problem into a root-command injection boundary.

## Target architecture

Separate three identities and responsibilities:

1. **CyComAgent runtime identity** — the configured user account. It authenticates federation peers, validates the caller, applies receiver-local policy, persists ledger/job state, and requests a worker operation. It must not run untrusted Remote Job payloads.
2. **Execution broker identity** — a narrowly scoped privileged system service. It accepts only a versioned, structured Remote Job request over a dedicated Unix socket. It authenticates the peer with `SO_PEERCRED` (UID and PID), verifies the expected CyComAgent executable identity, validates request bounds and receiver-local policy, and owns the lifecycle of one exact system service unit per job.
3. **Worker identity** — a dedicated non-login system account (or a separately allocated DynamicUser per job). It has no access to the CyComAgent user's home, user D-Bus socket, root-broker socket, identity keys, or writable agent state.

The execution broker must not accept a shell, arbitrary systemd properties, arbitrary unit names, arbitrary environment variables, arbitrary UID/GID selection, or arbitrary service directives from the caller.

## Per-job service boundary

For each approved job, the broker creates a uniquely named system-manager service with fixed, broker-owned properties:

- `User=cycomagent-worker` (or a broker-selected DynamicUser)
- `KillMode=control-group`
- `RuntimeMaxSec` from the validated policy timeout
- `SendSIGKILL=yes` and bounded `TimeoutStopSec`
- `NoNewPrivileges=yes`
- `ProtectSystem=strict`
- `ProtectHome=yes`
- `PrivateTmp=yes`
- `PrivateUsers=yes` where supported by the selected workload profile
- Empty capability bounding set and no ambient capabilities
- Explicit syscall/address-family restrictions and resource limits
- No user-manager D-Bus socket, root-broker socket, or writable agent state

Network access must be denied by default and enabled only by a separately reviewed execution profile. Filesystem access must be allowlisted, not inherited from the desktop user's home.

The worker must not be able to call the system manager to create sibling services. It has no system-manager authorization, no user manager, and no access to the runtime user's D-Bus socket. The system manager remains the authority that creates and kills the job cgroup.

## Executable and working-directory integrity

Do not pass user-controlled paths directly to a privileged shell. The broker must independently validate all inputs and bind execution to immutable content:

- Accept only receiver-configured executable identities; never accept an executable allowlist from the caller.
- Resolve and open the executable without following attacker-controlled symlinks; use `openat2(RESOLVE_BENEATH | RESOLVE_NO_MAGICLINKS | RESOLVE_NO_SYMLINKS)` where available, with a fail-closed fallback.
- Verify regular-file type, executable mode, ownership, and an expected digest or trusted package identity.
- Copy or snapshot approved executable content into a root-owned, non-writable per-job staging directory before starting the service. Do not rely on a pathname that the caller can replace after validation.
- Restrict working directories to broker-configured worker roots. If caller-controlled source trees are ever supported, create a validated read-only snapshot rather than exposing the caller's live home directory.
- Pass argv as an argument vector. Never interpolate request values into a shell command or unit property.

The exact file-transfer and stdio mechanism must be designed before implementation. In particular, do not assume `systemd-run --pipe` transfers arbitrary inherited file descriptors into the service. Prefer a broker-owned staging protocol or a small worker helper with a strict, authenticated local protocol.

## Broker protocol and durable state

A future broker request should be a versioned structure containing only:

- protocol version
- immutable local job ID and request ID
- policy revision/digest
- approved executable identity
- argv array
- approved working-directory identity
- allowlisted environment map
- timeout and output byte limits
- bounded input payload reference

The broker independently checks that the job ID is not already active and that the policy revision is current. Unit names are derived by the broker from a validated opaque job ID; callers cannot choose them.

The coordinator writes durable dispatch intent before asking the broker to start the service. If the broker reply is lost, the outcome remains `unknown`; never start a second service for the same request. Reconciliation may inspect the exact broker-owned unit and durable execution record but must not replay an ambiguous job.

Cancellation addresses only the broker-owned unit associated with the immutable job ID. It must never kill by process name, PID pattern, or broad user slice.

## Required tests before a Dispatch Gate Review

- A worker cannot see or connect to the runtime user's D-Bus socket.
- A worker cannot connect to the root broker or execution-broker control socket.
- A worker cannot create a user or system transient unit.
- `setsid()`, double-fork, daemonization, and nested process trees remain inside the job cgroup and die on timeout/cancel.
- A malicious worker cannot modify the executable snapshot, policy, ledger, job state, or another job's staging directory.
- A caller cannot override UID, GID, systemd properties, environment allowlist, unit name, cgroup path, or executable identity.
- Cross-process duplicate requests create at most one system service.
- Broker crash before start, during start, after start, and before reply preserve unknown/no-replay semantics.
- Output limits, stdin handling, cancellation, and service cleanup are bounded.
- Root broker and execution broker sockets reject incorrect UID, PID, executable, malformed JSON, oversized requests, duplicate fields, and stale policy revisions.
- All tests run without installing units, creating accounts, changing live services, or enabling federation dispatch.

## Explicit non-goals for the first implementation

The first secure worker profile should not promise arbitrary desktop-user shell execution, access to the desktop home directory, unrestricted networking, privileged commands, Docker socket access, or arbitrary systemd unit creation. Those are separate capabilities requiring separate threat models and policy.

## Current status

- User-scope cgroup containment: implemented for ordinary descendant lifecycle management.
- Same-UID nested user-manager escape: reproduced by regression test and documented.
- Dedicated worker account: not present on the inspected development host.
- System-manager service creation from the unprivileged agent: unavailable in the capability probe.
- Existing root broker: unrestricted shell protocol; intentionally not reused.
- Dedicated execution broker and per-job system service backend: design only, not implemented or deployed.
- Federation process launch and automatic repair: remain disabled pending implementation, adversarial tests, and a separate Dispatch Gate Review.


## Implemented protocol contract (design-stage only)

The Remote Jobs package now defines `BrokerStartRequest` and strict bounded JSON decoding in `broker_protocol.go`. The contract carries only immutable job/request IDs, policy revision, SHA-256 executable identity, argv, opaque working-directory/input references, allowlisted environment values, timeout, and output cap. It explicitly has no shell string, executable pathname, UID/GID, caller-selected unit name, cgroup path, or systemd property. Unit names are derived deterministically by the broker from validated IDs and do not reveal those IDs. Unknown fields and trailing JSON are rejected; request, argument, environment, timeout, and output limits are enforced.

This is a protocol contract and validation layer only. No socket listener, privileged broker, system service, account, or process-launch integration has been added. The broker must still independently authenticate the peer and resolve every opaque reference against receiver-owned policy before any future service creation.

## Implemented peer-authentication primitive (not wired to a server)

The Remote Jobs package now has a Linux-only `AuthenticateBrokerPeer` primitive. It reads kernel-provided `SO_PEERCRED` UID/PID/GID from an accepted Unix connection, checks the receiver-configured UID, hashes the peer's open `/proc/<pid>/exe` file descriptor, and compares the SHA-256 digest to receiver-owned policy. It obtains a kernel pidfd using `SO_PEERPIDFD` (socket option 77), pins the peer process identity for the caller, and samples `/proc/<pid>/stat` start-time ticks before and after executable inspection. The caller owns the returned pidfd and must close it with `BrokerPeerIdentity.Close()`. Kernels without `SO_PEERPIDFD` fail closed; the implementation deliberately does not fall back to `pidfd_open(SO_PEERCRED.PID)`, which has a PID-reuse race between credential sampling and opening the pidfd. Non-Linux builds return an explicit unsupported error.

Local Unix-socket tests cover the expected peer, UID mismatch, executable digest mismatch, and missing digest policy. This primitive is not a complete broker authentication design: it does not provide socket filesystem authorization, a listener, request framing, policy-revision authorization, or replay protection. The caller must keep the returned pidfd open for the entire authenticated connection/request lifetime and close it on every exit path. In particular, process start-time ticks have kernel clock granularity and must not be treated as a globally unique process handle. A future broker should retain a pidfd where supported and bind authentication to the accepted connection lifecycle.

The broker protocol's environment key validation also now requires POSIX-style ASCII identifiers (`[A-Za-z_][A-Za-z0-9_]*`). This is syntax validation only; the receiver's explicit environment allowlist remains authoritative.

## IPC frame format (implemented primitive; no listener)

The broker IPC framing primitive uses a four-byte unsigned big-endian payload length followed by exactly that many bytes. Empty frames and frames larger than the existing 64 KiB broker request limit are rejected before allocation. Reads use `io.ReadFull`; writes complete short writes and reject zero-progress writers. Multiple frames may be carried on one stream, one after another.

`ReadBrokerFrame` and `WriteBrokerFrame` deliberately do not create sockets, authenticate peers, decode requests, or set deadlines. The future connection owner must set bounded read/write deadlines before invoking them, enforce one serialized writer per connection, authenticate the accepted Unix socket (including retaining its pidfd for the connection lifetime), decode with `DecodeBrokerStartRequest`, and authorize policy revision/replay state before any operation. Framing success is not authorization and does not enable dispatch.

## Bounded connection request helpers (implemented primitive; no server)

`ReadBrokerStartRequest` and `WriteBrokerStartRequest` wrap the frame and strict request decoder/encoder with explicit connection deadlines. The timeout must be positive and no greater than 30 seconds. The helpers clear the respective deadline after the operation and report failure if clearing it fails. Reads accept exactly one frame and then run strict JSON decoding; writes validate the request before serialization.

These helpers do not authenticate a Unix peer, verify socket path ownership/mode, enforce a maximum connection count, bind/retain a pidfd, authorize the policy revision, reserve request IDs, persist replay state, or launch/cancel work. A future server must perform those steps in a defined order and close the connection on any authentication, framing, decoding, deadline, or authorization failure. The connection must not be reused after a partial frame or ambiguous I/O failure.

## Unix socket filesystem validation (implemented primitive; no listener)

Linux `ValidateBrokerSocketPath` performs read-only checks on a pre-existing socket path: the path must be absolute/canonical, the final object must be a Unix socket (not a symlink), its UID must match receiver-owned policy, and group/other permission bits must be absent. Its immediate parent must be a real directory owned by the configured UID and must not be group/world writable. Non-Linux builds fail closed.

This helper does not create/chmod/unlink sockets, walk and validate every ancestor directory, prevent replacement between validation and connect/bind, authenticate the connected peer, or establish a listener. The service installer must provision and audit all ancestor directories; a future server must bind/hold the socket safely and revalidate filesystem state without a check/use race.

## Receiver authorization and one-shot replay reservation (implemented gate; no dispatch)

`AuthorizeAndReserve` composes the receiver-local `PolicyStore` with the cross-process durable remote-job `Ledger`. The caller node ID and `callerTrusted` value are supplied by the authenticated connection/trust layer; they are not accepted from the request JSON. Opaque executable/working-directory references must already have been resolved by receiver-owned code into a `ProcessRequest`.

The gate requires the request policy revision to equal the current receiver policy revision, validates caller trust and allowlists/resource limits, checks that the resolved argv/environment/timeout match the request, hashes the opened executable and compares its SHA-256 digest, and enforces the receiver output cap. It then atomically reserves `(caller node ID, request ID)` in the durable ledger. Exactly one concurrent request can create the record; every identical replay returns `ErrBrokerReplay`, while changed payloads return the ledger conflict. It binds the deterministic local job ID and policy revision while leaving the record in `accepted`.

This is authorization evidence and replay consumption only. It does not call `PrepareDispatch`, change the record to `dispatching`, start a process, or cancel anything. A request ID is consumed even if a later persistence/binding step fails; that is intentionally fail-closed and requires reconciliation rather than retry. The policy revision is checked again after reservation. Any future dispatch gate must re-check the current policy revision and trust state immediately before its own durable dispatch transition. Filesystem path resolution here is still a preliminary check, not the final race-free `openat2`/descriptor-based execution binding required by the dedicated worker design.

## Single-request authenticated connection lifecycle (implemented primitive; no listener)

Linux `ServeAuthenticatedBrokerConnection` owns one already-accepted Unix connection. It authenticates kernel peer credentials and executable identity before reading, retains the returned pidfd for the complete handler call, reads exactly one bounded `BrokerStartRequest`, then closes the pidfd and connection on success or failure. Handler errors are returned unchanged. Non-Linux builds fail closed.

The handler is responsible for obtaining the authenticated Federation caller identity from receiver-owned context/trust state and invoking `AuthorizeAndReserve`; the kernel peer UID/PID is not itself a Federation node identity. This wrapper does not create/bind/listen on a socket, authorize policy, persist replay state by itself, send a response, or dispatch a process. It deliberately closes after one request so partial/ambiguous sessions cannot be reused.

## Federation caller trust context (implemented adapter; transport proof binding remains separate)

The broker authorization gate no longer accepts a caller-controlled `callerTrusted bool`. It requires a `FederationCallerContext` created by `NewFederationCallerContext` against the receiver's live `nodeidentity.Manager` trust store. Unknown, revoked, malformed, or unavailable identities fail closed. The context carries only the locally trusted Federation Node ID; the execution policy independently checks that ID against `AllowedCallers`.

Important boundary: this adapter verifies active local trust membership, not a request signature. Its constructor must only be called by a transport handler after that handler has verified the purpose-bound Federation signature over the exact request bytes. The current broker IPC `BrokerStartRequest` does not yet carry a Federation proof envelope, and `ServeAuthenticatedBrokerConnection` does not manufacture one from kernel SO_PEERCRED. Therefore the complete socket-to-Federation trust binding is NOT implemented and this handler must not be exposed as a production listener yet. Next work must add a separately signed broker request envelope (caller Node ID + purpose-bound signature over canonical request bytes), verify it through `nodeidentity.Manager.VerifyTrustedChallenge`, then construct the context. The local Unix peer identity remains an independent admission check.

## Signed broker request envelope (implemented; still no production listener)

The broker IPC now has a distinct `SignedBrokerStartEnvelope` containing a version, Federation caller Node ID, the strict `BrokerStartRequest`, and a base64url Ed25519 signature. `SignBrokerStartEnvelope` signs the canonical JSON request using the existing `SignRequestForPurpose(..., "reserve", ...)` transcript. The transcript binds the sender Node ID, receiver Node ID, request ID, payload SHA-256, and reserve purpose. `DecodeAndVerifySignedBrokerStartEnvelope` (the only public path that can construct the otherwise package-private caller context) rejects duplicate JSON keys, unknown fields, trailing JSON values, invalid signatures, stale/revoked/untrusted peers, wrong receiver, modified payloads, and invalid request fields before constructing `FederationCallerContext`.

`ReadSignedBrokerStartEnvelope` adds bounded frame/deadline handling. Linux `ServeAuthenticatedSignedBrokerConnection` independently checks the accepted Unix peer with SO_PEERCRED/SO_PEERPIDFD, retains the pidfd for the complete handler, verifies the Federation envelope, and only then invokes a handler with `(kernel peer, trusted Federation caller context, request)`. It still does not create a listener, resolve opaque references, reserve ledger state, transition to dispatching, or launch a process. The handler must call `AuthorizeAndReserve` with the returned caller context after resolving references from receiver-owned state.

The legacy unsigned single-request session remains for low-level tests/compatibility and MUST NOT be wired to any production listener. Production integration must use only the signed session, bind kernel peer UID/executable digest to a receiver-owned local admission policy, enforce connection admission limits, and keep the listener disabled until the separate execution identity/service boundary and dispatch gate review are complete.

## Receiver-side pre-dispatch handler (implemented; no dispatch)

`BrokerReceiverHandler` now composes the receiver-side gates in this order:

1. Requires a complete, already kernel-authenticated peer identity (PID, retained PIDFD, UID, executable digest).
2. Rechecks that the Federation caller remains actively trusted, so a revoke occurring after signature verification but before handler execution is denied.
3. Calls receiver-owned `BrokerPeerAdmission` to bind the local Unix peer (for example an approved runtime UID/executable) to the Federation caller. The request cannot supply or override this mapping.
4. Strictly validates the broker request.
5. Calls receiver-owned `BrokerReferenceResolver` to resolve the working-directory and input references and construct a `ProcessRequest`; the resolver must not launch or mutate durable state.
6. Rechecks Federation trust after potentially slow reference resolution.
7. Calls `AuthorizeAndReserve`, which enforces the current receiver execution policy, executable digest, argv/environment/timeout/output constraints, and durable replay reservation.

`ServeBrokerReceiverConnection` is a Linux convenience entry point that wires this handler into the signed one-request session. It still accepts an already-accepted Unix connection; it does not create a listener. Its successful result is only a durable `accepted` record. No `PrepareDispatch`, `dispatching` transition, launcher, or process execution is reachable through this path.

Admission and reference resolution are explicit interfaces because the current repository does not yet contain a production mapping from local runtime executable identity to Federation caller identity, nor a receiver-owned opaque-reference store. A production service must provide audited implementations; nil dependencies fail closed. Tests use in-memory fakes and temporary identity/state directories only.

## Linux descriptor resolution primitive (implemented; not yet wired to authorization)

`openat2_linux.go` adds `OpenBeneath`, `OpenExecutableBeneath`, and `OpenDirectoryBeneath`. They require an already-open receiver-owned root directory descriptor and a strictly relative path. The kernel enforces `RESOLVE_BENEATH | RESOLVE_NO_MAGICLINKS | RESOLVE_NO_SYMLINKS`; traversal, absolute paths, empty/dot components, symlinks, and procfs magic links are rejected. The implementation uses the Linux `openat2` syscall (number 437) and intentionally has no `openat` or pathname fallback: older kernels return an error.

Tests verify symlink/traversal rejection and that an opened executable descriptor continues to refer to the original inode after its pathname is replaced. The directory helper rejects regular files.

**Security boundary:** this is currently a standalone primitive. `BrokerReferenceResolver` and `AuthorizeAndReserve` still exchange canonical path strings and perform preliminary path-based policy/digest checks; the broker authorization result does not yet retain these descriptors. Therefore this addition does not close the executable/CWD replacement race in the receiver pipeline and must not be described as complete descriptor-bound authorization. The next implementation step is a descriptor-owning resolver result and an authorization API that hashes/validates those exact open file descriptions, retains them through the broker's service-start operation, and never reopens their pathnames.

## Descriptor-bound authorization (implemented; still pre-dispatch)

A separate `BrokerDescriptorReferenceResolver` now returns an owned executable descriptor, an owned working-directory descriptor, and the corresponding receiver-resolved `ProcessRequest`. `HandleDescriptorBound` requires this interface and has no fallback to the legacy pathname-only resolver. The Linux signed receiver IPC entry point `ServeBrokerReceiverConnection` now refuses to run unless the descriptor resolver is configured.

`AuthorizeAndReserveDescriptors`:

- validates the same signed argv/environment/timeout contract and current receiver policy revision;
- runs the existing allowlist policy checks against the receiver-resolved canonical executable and cwd names;
- proves those names still identify the already-open executable and cwd descriptors using `os.SameFile`;
- hashes the executable by reading the pinned descriptor itself;
- retains both descriptors in `BrokerAuthorization` on success, and closes them on every error;
- persists only the `accepted` replay reservation and rechecks policy revision before returning.

`BrokerAuthorization.Close` releases the retained descriptors. The signed IPC convenience function currently closes the authorization after its callback returns because it has no worker-start operation to hand descriptors to. This is intentional: no process is started and no descriptor is reopened by pathname.

Tests verify that a successful authorization keeps the original executable and cwd inodes pinned after the executable pathname is replaced, and that a descriptor for a different executable is rejected without consuming the replay reservation.

**Remaining boundary:** `BrokerReceiverHandler.Handle` and `BrokerReferenceResolver` remain as legacy path-only APIs for existing tests/callers. They must not be wired to production IPC. Production-intended `ServeBrokerReceiverConnection` explicitly requires `DescriptorResolver` and calls `HandleDescriptorBound`. The descriptor-aware resolver implementation itself is still an adapter contract, not a production reference database; it must open descriptors from receiver-owned roots using `OpenExecutableBeneath` / `OpenDirectoryBeneath`, enforce caller/reference ownership, and never accept caller-supplied filesystem paths. A future broker start path must consume the retained descriptors (for example via inherited FDs and descriptor-based cwd) before closing them. Until that exists, the signed connection path only validates/reserves and then closes descriptors; it does not dispatch.

## Concrete receiver-owned reference resolver (Linux; pre-dispatch)

`StaticBrokerDescriptorResolver` now provides a concrete immutable configuration-backed implementation of `BrokerDescriptorReferenceResolver`:

- Executable references are selected only by a receiver-configured lowercase SHA-256 digest. Each entry pins its configured root directory descriptor at resolver construction and contains a receiver-owned relative path.
- Working-directory references are opaque IDs bound to one exact trusted Federation Node ID. A different caller cannot reuse the reference.
- Both paths are opened relative to pinned root descriptors using `OpenExecutableBeneath` / `OpenDirectoryBeneath` (`openat2` with `BENEATH`, `NO_MAGICLINKS`, and `NO_SYMLINKS`). There is no pathname/openat fallback.
- The resolver constructs `ProcessRequest` from the authenticated request's argv/environment/timeout plus receiver-owned absolute path mappings. It does not accept an executable or cwd pathname from the caller.
- Resolver root descriptors remain open for its lifetime. `Close` is idempotent and prevents further resolution; callers must stop resolution before closing it.

The concrete resolver tests cover successful mapping, caller isolation, closed-store rejection, symlink rejection, and unknown executable digest rejection.

**Important limitations remain:** the resolver configuration is currently supplied in-process at construction; no durable administrative reference-store format or management interface exists yet. Root directories must be trusted and non-writable by untrusted callers. The authorization path still compares the configured absolute path with the current path using `os.SameFile`; if the path is replaced between resolution and authorization it fails closed, while the open descriptor itself remains pinned. There is still no descriptor-consuming system service/worker launcher, dedicated worker identity, broker socket listener, or dispatch transition. This resolver is not wired into the running CyComAgent runtime.

## Durable receiver reference configuration (Linux; read-only loader)

Added `BrokerReferenceConfig` and `LoadBrokerReferenceConfig` with a version-1 JSON format containing receiver-owned executable digest/root/relative-path mappings and caller-bound working-directory reference/root/relative-path mappings. `NewBrokerDescriptorResolverFromConfig` loads this file and constructs the pinned-root resolver.

The loader:

- requires an absolute path and opens the final file with `O_NOFOLLOW|O_CLOEXEC`;
- verifies the opened object is a regular file owned by the service UID with no group/other permissions (0600 or stricter);
- bounds input to 1 MiB;
- rejects duplicate JSON keys, unknown fields, trailing data, unsupported versions, and unreasonable entry counts;
- does not create, modify, chmod, or replace the configuration file.

The config is administrator-managed. There is deliberately no runtime CRUD API or management socket in this change. Parent directories and configured root directories must be protected from untrusted writes; final-component `O_NOFOLLOW` does not by itself secure every ancestor. Root path canonicalization at resolver construction is an administrative trust boundary. Resolver roots must be owned by root or the service UID and must not be group/world writable. This loader does not make the worker sandbox or dispatch safe by itself.

## Broker-owned worker service profile (implemented data model; no launcher)

`worker_service_profile_linux.go` now builds a fixed Linux worker-service profile from a broker-selected non-root UID/GID and a policy-bounded timeout. The profile includes cgroup-wide kill semantics, runtime/stop bounds, `NoNewPrivileges`, strict system/home protection, private tmp/devices/network, hidden process metadata (`ProtectProc=invisible`, `ProcSubset=pid`), namespace and address-family restrictions, closed device policy, clock/hostname/kernel protection, empty capability sets, restrictive umask, null stdin, and inaccessible runtime-user/system D-Bus paths. UID/GID are represented separately from the systemd property list and are not fields in `BrokerStartRequest`.

This builder is deliberately side-effect free: it only returns data. It does not resolve account names, create accounts, contact systemd, create units, provision staging directories, configure broker socket access, or launch workers. The profile is a baseline rather than a completed sandbox; executable snapshot transfer, output transport, syscall policy selection, root-broker socket denial for every possible configured path, and host-specific systemd capability verification still require broker-side design and adversarial validation. The installed root-broker template currently uses the per-user path `/run/cycomagent-%i/root.sock` (see `packaging/systemd/cycomagent-root@.service` and `cycomagent@.service`). A fixed `/run/cycomagent/root.sock` deny rule would therefore be incomplete, and systemd path matching must not be assumed to expand `%i` or shell globs in `InaccessiblePaths`. The future privileged broker must derive the exact socket path from trusted local instance configuration and add it as a separate broker-owned inaccessible path before any service start; it must not accept that path from the Federation request. This remains an explicit Dispatch Gate blocker. The worker-profile builder now requires a broker-local `rootBrokerSocketPath` `BuildWorkerExecutionContract` now also requires a broker-owned `BrokerSocketPolicy` and calls `ValidateBrokerSocketPath` before returning a contract. It therefore rejects a missing path, a regular file, a symlink final component, a non-socket object, wrong socket/parent UID, or unsafe socket/parent mode. Tests create a real private Unix socket and exercise successful contract construction, plus rejection when the configured socket is absent. This is still a point-in-time `Lstat` validation: it does not pin the socket inode or eliminate replacement races between validation and a future systemd operation; a production broker must hold a safely opened/connected socket or use a race-resistant directory-fd strategy and audit every ancestor directory. and inserts that exact path into `InaccessiblePaths` alongside the user runtime and session D-Bus paths. It rejects non-canonical paths, whitespace, non-`/run/cycomagent-<instance>/root.sock` layouts, and empty instance names. The profile models systemd `InaccessiblePaths` separately as a typed list of individual absolute paths (not a shell-like space-delimited property string), so a future D-Bus `StartTransientUnit` adapter can encode the property with the correct `as` signature without reparsing caller-controlled text. The parameter is deliberately not part of `BrokerStartRequest`; the future privileged broker must source it from trusted local instance configuration and validate the socket/parent ownership and mode with `ValidateBrokerSocketPath` before using it. The profile builder only constructs data and does not inspect, create, or mutate the socket. In particular, `InaccessiblePaths=/run/user` and the system D-Bus socket restriction are defense-in-depth, not proof of isolation against arbitrary host mounts or a misconfigured service manager.

## Broker-to-worker execution contract (implemented; no dispatch)

`BuildWorkerExecutionContract` now joins the accepted durable authorization evidence, retained executable/cwd descriptors, broker-selected non-root UID/GID, validated request limits, and broker-derived opaque unit name into an in-memory `WorkerExecutionContract`. It rejects missing descriptors, invalid descriptor types, mismatched request/policy revisions, incomplete durable identity, and non-accepted ledger state. Argument and environment data are copied so later mutation of the decoded request cannot mutate the contract.

The contract intentionally contains no descriptor numbers or caller-supplied paths and does not serialize itself onto IPC. The Linux broker-owned `SendBrokerDescriptors` / `ReceiveBrokerDescriptors` primitive now transfers exactly the executable and working-directory descriptors with `SCM_RIGHTS` over an already-authenticated Unix connection. Receive uses `recvmsg(MSG_CMSG_CLOEXEC)` through `SyscallConn`, validates the single marker/control frame and descriptor types, and closes received descriptors on invalid frames. Sender ownership is retained; receiver ownership transfers to the caller on success. This primitive creates no connection/listener and does not execute anything or launch a worker.


## Linux SCM_RIGHTS descriptor transfer (implemented primitive; no listener or dispatch)

`SendBrokerDescriptors` sends exactly two descriptors (regular executable file and working-directory directory) on an already-authenticated `net.UnixConn`. `ReceiveBrokerDescriptors` receives one marker byte and exactly one `SCM_RIGHTS` control message containing exactly two descriptors. It uses `SyscallConn` plus `syscall.Recvmsg(..., MSG_CMSG_CLOEXEC)` so close-on-exec is installed atomically by the kernel, then verifies the flag, executable mode, and directory type. Malformed/truncated frames and invalid descriptor types fail closed and close received descriptors. The sender retains its descriptors; successful receive transfers ownership to the returned `*os.File` values.

The API deliberately does not authenticate or create the connection: callers must authenticate the Unix peer before receiving or sending descriptors. It does not create a socket, listener, account, systemd unit, or worker, and it is not connected to a dispatch path. The focused package tests and race tests cover object identity, sender ownership, close-on-exec, malformed marker handling, and rejection of non-executable descriptors.


### Pinned root-broker socket validation

`ValidateBrokerSocketPathPinned` resolves the configured absolute socket path beneath an already-open filesystem-root descriptor using `openat2` with `RESOLVE_BENEATH`, `RESOLVE_NO_MAGICLINKS`, and `RESOLVE_NO_SYMLINKS`. It retains `O_PATH` descriptors for the immediate parent directory and final Unix socket inode, then checks owner IDs and restrictive modes through `fstat` on those descriptors. `BuildWorkerExecutionContract` now requires this closeable validation object rather than a raw path and a caller-supplied policy. The broker must keep the object alive while relying on the validation and close it on every terminal path.

This pins the objects inspected and blocks symlink traversal at every path component, but it does **not** make a later pathname-based systemd `InaccessiblePaths` application atomic with the check. The future unit adapter must either prove the path still resolves to the pinned inode immediately before applying the unit or move to an fd-based isolation mechanism. Ancestor ownership/mode policy above the immediate parent also remains an installer responsibility. Kernels without `openat2` fail closed.


### Executable content snapshot (pre-dispatch only)

`CreateExecutableSnapshot` copies the already-authorized executable descriptor into a bounded (128 MiB) broker-owned in-memory byte snapshot, checks the authorized SHA-256 digest over the copied bytes, and verifies file identity/size/mtime did not change during the read. `WorkerExecutionContract` retains this snapshot and exposes only digest, size, and independent read-only readers. This prevents later mutation of the original executable inode from changing the bytes represented by the contract.

This is deliberately **not** a root-owned on-disk staging artifact and is not directly executable by systemd. It is a safe content-capture stage only. Before dispatch, a separate privileged adapter must create a root-owned, non-writable per-job artifact relative to a pinned staging-directory descriptor, verify the digest again after writing, fsync file and directory, and define cleanup/recovery for crashes. Until that adapter and descriptor-based execution handoff exist, Dispatch remains disabled.

### Descriptor-relative executable staging (still pre-dispatch)

`StageExecutableSnapshot` accepts only an already-open staging-directory descriptor, requires the directory to be owned by the configured broker UID with no group/other write permission, and rejects a shared broker/worker UID. It creates a cryptographically random opaque basename with `openat2` + `O_CREAT|O_EXCL|O_NOFOLLOW` beneath that pinned directory, writes only the verified in-memory snapshot, applies mode `0555` (worker-readable/executable but not writable), syncs the file, reopens and verifies owner/type/mode/size/SHA-256, then syncs the directory. `StagedExecutable.Close` removes the artifact with `unlinkat` relative to the retained directory FD, so cleanup is not redirected by a renamed/replaced pathname.

This API deliberately does not expose a launch pathname, pass descriptors to systemd, create a system service, or enable dispatch. The staging directory must also be traversable by the worker (for example `0711`) while remaining non-writable and non-listable. `FindAbandonedExecutableStages` performs read-only recovery inspection only for matching `exec-` plus 128-bit-hex names, regular files owned by the broker with the exact sealed mode, and ctime older than 26 hours. The age floor exceeds the 24-hour maximum worker runtime plus recovery margin. Inspection is descriptor-relative and never deletes files. Filename and age are not proof that a worker is inactive; deletion remains blocked until a durable artifact-to-job registry, terminal ledger reconciliation, and systemd unit absence check are available. The caller must obtain the staging directory from trusted installation configuration and enforce the actual privileged broker UID (normally root); tests use a temporary directory owned by the test process. This is a bounded stale-artifact sweep, not a durable job lease or proof that a worker is stopped. An atomic descriptor-to-systemd execution handoff remains an open requirement. The existing root broker shell-execution path is not used.


### Durable executable artifact registry (pre-dispatch only)

`ExecutableArtifactRegistry` stores one create-only record per `(caller node, request)` under the federation state directory. Registration is allowed only after `InspectExecutionConsistency` reports `consistent_prepared`, and rechecks the accepted ledger binding and immutable execution record. It records the opaque staging basename, SHA-256 digest, byte size, broker/worker UIDs, and the staged inode device/inode pair. The registry file is written to a private temporary file, synced, linked into place without replacement, and the registry directory is synced. Repeated identical registration is idempotent; conflicting artifact identity fails closed. After successful registration, the owner must call `StagedExecutable.Retain()` to close descriptors without unlinking; ordinary `Close()` remains an abort/rollback operation that removes an unregistered stage.

`InspectArtifactConsistency` is read-only. It cross-checks artifact metadata against the ledger and execution record and distinguishes prepared, active/unknown, terminal, and missing/mismatched records. A terminal ledger result is explicitly insufficient for deletion: systemd unit absence and a fresh descriptor-relative inode/digest verification are still required. The registry does not perform cleanup, dispatch, process launch, systemd calls, or service/account creation. It is not yet a full multi-store transaction: a crash after staging but before registry creation leaves an unregistered artifact for operator inspection; a crash after registry creation leaves a durable binding that can be reconciled.


### Read-only artifact recovery inspection

`VerifyRegisteredArtifactFile` reopens the registered opaque basename relative to a trusted pinned staging-directory descriptor and compares file type, owner, exact mode, device/inode, size, and SHA-256. It never follows symlinks, deletes, or mutates the artifact. `InspectArtifactRecovery` combines this fresh file evidence with the registry/ledger/execution-record binding and reports missing or mismatched artifacts explicitly.

Recovery intentionally stops at `artifact_verified_unit_identity_unavailable`. The existing structured launcher derives transient scope names from its own PID and current nanosecond timestamp, but does not durably record that unit name against the job/artifact. Therefore a recovery scanner cannot prove the corresponding systemd unit is absent. Do not infer absence from a broad unit-list query or from a terminal ledger status. Before cleanup can be implemented, the future launcher adapter must allocate and durably bind a validated deterministic per-job unit identity before launch, then query the system manager for that exact unit and prove it is unloaded/inactive; unknown manager state remains a hard block.


### Durable launch intent and stable systemd identity

`LaunchIntentJournal` persists a create-only launch intent for each caller/request pair after the artifact registry, accepted ledger entry, and immutable execution record agree. The unit name is derived only by the existing `ExecutionUnitName(localJobID, requestID)` function and is bound to the remote/local job IDs, artifact opaque name, SHA-256, device/inode, payload hash, and policy revision. Repeated preparation is idempotent; a changed binding fails closed.

The journal exposes monotonic evidence transitions (`prepared` to `launching`, then `running` or a terminal/unknown outcome). These methods are persistence primitives only: no launcher calls them yet, no systemd unit is created, and no dispatch path is enabled. The existing command launcher still uses a separate transient scope naming scheme and is not a consumer of this journal. Before integration, the production adapter must atomically coordinate its exact service/scope invocation with the durable `launching` record and reconcile ambiguous systemd outcomes as `unknown`, never retrying an uncertain launch.


### Exact systemd unit reconciliation (read-only)

`SystemdUnitInspector` queries `systemctl --user show` for only the exact deterministic `UnitName` stored in a validated launch intent. It requests `Id`, `LoadState`, `ActiveState`, `SubState`, `InvocationID`, and `ControlGroup`; it rejects mismatched IDs, duplicate/missing/unexpected properties, malformed output, and manager/query errors. It does not enumerate units, stop units, create units, or delete artifacts.

Observations are classified as `not_found`, `inactive`, `active`, or `unknown`. An `inactive` observation is not proof that the unit's cgroup is empty or that an invocation cannot still be reconciled. `not_found` is also not, by itself, authorization to delete: the future recovery gate must additionally prove cgroup absence and reconcile the journal/ledger terminal state. `InvocationID` is recorded as observation only; this version has no expected invocation ID persisted before launch, so it cannot authenticate a particular run. A command failure is `unknown`, never `not_found`. No cleanup decision or launch integration is implemented.

### Cgroup-v2 identity and process-absence observation

`VerifyCgroupAbsence` accepts a pinned cgroup-v2 mount descriptor and the exact `ControlGroup` reported by the systemd inspection for the journal-bound unit. It verifies the filesystem type, rejects root/traversal/relative paths, resolves the cgroup directory with `openat2` beneath the pinned mount (no symlinks or magic links), and reads `cgroup.events` (`populated`) plus `cgroup.procs` twice. It reports `populated`, `empty_observed`, `changing`, or `missing`, along with the pinned directory device/inode and observed PIDs.

This is deliberately an observation, not a deletion gate. A cgroup can acquire members after the final read, and a systemd `not_found` response normally has no ControlGroup path to inspect. No cgroup controls are written (`cgroup.kill`, freeze, or otherwise); no cgroup is removed; no artifact is cleaned. A future cleanup gate needs a stronger serialization/ownership protocol that prevents new processes from entering the exact job cgroup while the absence proof and unit reconciliation are committed. Until then, even `empty_observed` is insufficient for cleanup authorization.

### Serialized terminal recovery observation

`LaunchIntentJournal.InspectSerializedRecoveryGate` performs a read-only evidence pass while holding the per-job launch-intent flock shared with journal `Prepare` and `Transition`. It requires a terminal launch-intent state matching the terminal ledger state and immutable job/payload/policy binding; checks the registered artifact's inode, size and digest; queries the exact deterministic systemd unit; requires `ActiveState=inactive` and a `ControlGroup` whose final component is the journal-bound unit name; and obtains an `empty_observed` cgroup-v2 snapshot. It returns `terminal_artifact_unit_cgroup_absence_observed_not_cleanup_authorized` only when all these observations agree.

This is not a cleanup authorization. The journal flock serializes journal operations only: the existing ledger transitions and legacy launcher do not yet participate in one common lifecycle lock, and cgroup membership can change immediately after observation. A production-safe adapter must make the launch-intent lock, ledger transition, unit creation, and cgroup membership admission one enforceable lifecycle protocol (or use an equivalent system-manager-owned serialization boundary) before any destructive recovery operation. The inspection requires an inactive unit with a nonempty ControlGroup; `not_found` is insufficient because there is no current cgroup path to inspect. No cleanup, dispatch, unit mutation, or cgroup mutation is performed.

### Shared per-job lifecycle lock primitive

`WithJobLifecycleLock(stateDir, callerNodeID, requestID, fn)` provides a cross-process `flock` keyed by the authenticated caller/request identity under `federation/remote-job-lifecycle`. Launch-intent `Prepare`, `Get`, `Transition`, and serialized recovery inspection now use the same lock file. Tests verify that journal access blocks while the shared lifecycle lock is held and that distinct job identities can proceed independently.

This is an incremental coordination primitive, not yet a unified lifecycle transaction. Ledger methods still use their independent ledger lock, and `DispatchCoordinator` / legacy Launcher do not acquire this lifecycle lock across `PrepareDispatch`, Journal transitions, Job Manager launch, and terminal Ledger persistence. Therefore this lock does not prevent those existing paths from racing with recovery. Do not enable cleanup, dispatch, auto-repair, or production execution based on this work. The next integration must make every writer for a job acquire this lock in a documented lock order before taking store-local locks, and must preserve the lock across the irreversible systemd start boundary.

### Coordinator participation in the shared lifecycle lock

`DispatchCoordinator.Dispatch` now acquires the shared per-job lifecycle lock before taking the policy lease or touching store-local locks, and holds it across immutable-intent reconciliation, the Ledger `PrepareDispatch` gate, `LaunchAsJob`, and the terminal/unknown Ledger transition. Launch-intent Journal operations and `InspectSerializedRecoveryGate` acquire the same lock. The intended lock order for these cooperating paths is therefore lifecycle lock first, followed by policy/store-local locks; code must not acquire the lifecycle lock while already holding a store-local lock.

This closes the previous race between the Coordinator dispatch path and serialized recovery inspection, provided both use the same state directory and lock implementation. It does not cover direct calls to public Ledger mutation methods, standalone legacy Launcher calls, Job Manager mutations performed outside this Coordinator, or arbitrary processes able to join the systemd cgroup. No claim of global lifecycle serialization or cleanup authorization is made. In particular, cgroup membership admission remains outside this file-lock protocol and requires a system-manager-owned boundary before any artifact deletion can be considered.

### Ledger mutation enforcement

All public Ledger mutation entry points (`Reserve`, `Transition`, `PrepareDispatch`, `BindExecutionMetadata`, and `BindLocalJob`) now acquire the shared per-job lifecycle lock before the Ledger's own mutex/flock. Their explicit `*UnderLifecycleLock` counterparts are for internal callers that already hold the shared lock; Coordinator uses these to avoid recursive flock acquisition. Read-only `Get` remains store-locked only.

Startup recovery no longer mutates `dispatching` records from inside the initial Ledger-only reload. `Open` first loads a read-only snapshot, then for each candidate acquires the lifecycle lock, reloads under the Ledger lock, and marks the record `unknown` only if it is still `dispatching` and its recorded owner is no longer alive. This preserves crash recovery while preventing startup recovery from racing a cooperating live Coordinator.

This still cannot serialize external legacy Job Manager operations or systemd cgroup membership admission. Public APIs can only coordinate cooperating processes that use the same state directory and lock files. Direct use of `*UnderLifecycleLock` without already holding the lifecycle lock is a caller contract violation. Cleanup remains prohibited.

### Job Manager mutation audit (current boundary)

The remote Coordinator's current `LaunchAsJob` path calls `jobs.Manager.StartStructuredWithID` synchronously while the Coordinator already holds the shared lifecycle lock. The structured path persists `launching`, runs the descriptor-bound launcher synchronously, and persists its final `exited` / `failed` / `launch-unknown` snapshot before returning. It does not spawn a Job Manager monitor goroutine for structured jobs. Therefore the Coordinator's existing lifecycle lock spans the remote Job Manager mutations made by this specific dispatch path.

The generic Job Manager is a separate package and cannot import `remotejobs` without creating an import cycle (`remotejobs` already imports `jobs`). Its other mutation paths are not covered by the per-remote-job lock: `Signal`, `Prune`, generic shell/external-job monitors (`finishJob`, `monitorExternal`), and startup `recover` all mutate Job snapshots using only the Job Manager's per-job lock. The current Job record does not durably bind a local Job ID to `(CallerNodeID, RequestID)`, so those generic paths cannot independently derive the correct remote lifecycle lock key. Do not treat them as serialized with Ledger/Journal recovery.

A safe next design needs an explicit, immutable remote-job ownership binding in the Job record plus a lock-provider contract injected into `jobs.Manager` (to avoid package cycles). Lock acquisition must occur before `Manager.mu` and the per-job flock; Coordinator's already-locked structured launch needs an explicit non-reentrant internal path. Recovery, Signal, and Prune must use the same provider when a Job has a remote ownership binding. Until that design and its cross-process tests exist, generic Job Manager mutations remain a cleanup/repair blocker. No Job Manager mutation guard was added in this audit because a partial guard would either deadlock the Coordinator or leave the generic mutation paths bypassing it.

### Immutable Job ownership and injected lifecycle provider

`jobs.Job` now persists `RemoteCallerNodeID` and `RemoteRequestID` together as an immutable ownership binding for structured remote dispatch. The `jobs` package defines a narrow `LifecycleLockProvider` interface; `remotejobs` supplies the adapter, so the dependency direction does not create an import cycle. The Coordinator configures this provider before dispatch and explicitly reruns Job recovery after configuration.

For existing remote-owned records, `Manager.updateJob` first reads the atomically replaced snapshot to discover the immutable owner, acquires the provider's shared lifecycle lock, then reloads and validates that the owner binding did not change inside the store-locked read-modify-write. If ownership is incomplete or the provider is absent, mutation fails closed. Startup recovery defers remote-owned records until provider installation; Coordinator-triggered `Recover` then processes them through the same lock. The structured remote launch's final update uses the explicit already-locked internal update path because Coordinator holds the lifecycle lock across the runner.

Generic `Manager.Prune` deliberately skips every remote-owned record. It has no remote lifecycle identity transaction and must not delete those records/logs. This is a safety barrier, not cleanup authorization. The provider currently covers `updateJob`-based mutations (including Signal, monitors, and recovery) when a remote owner is present. Prune remains disabled for remote-owned records pending a dedicated lifecycle-aware retention operation. Existing generic Job creation remains unowned. `StartStructuredWithIDOwned` is a caller-held-lock API: callers must hold the matching lifecycle lock for the entire call; the Coordinator is the integrated caller. No production dispatch is enabled by this code change.

### Lifecycle-aware artifact retention assessment (read-only)

The serialized recovery gate now validates the exact journal-bound systemd unit name, requires `LoadState=loaded`, `ActiveState=inactive`, a syntactically valid 128-bit `InvocationID`, and a canonical absolute `ControlGroup` path whose final component is the deterministic unit name. It rejects missing or malformed invocation identity and path components such as `.` or `..` before inspecting cgroup-v2 state.

`AssessArtifactRetention` runs the same cross-store, artifact inode/hash, exact-unit and cgroup observations under the shared per-job lifecycle lock. It applies a 26-hour minimum age and can report `eligible_for_operator_review_not_cleanup_authorized`. This is intentionally an assessment only: cgroup membership can change after observation, and the returned value is not a lease or deletion capability. No API in this path unlinks artifacts, removes registry/journal/ledger records, prunes Job snapshots, or changes systemd state. Actual cleanup remains prohibited until a separate worker execution identity and a race-free kernel-enforced exclusion/termination protocol exist.

### Invocation identity binding and retention fail-closed rule

A syntactically valid `systemd show InvocationID` is not sufficient evidence that the observed invocation is the one associated with a particular Remote Job. `LaunchIntentRecord` therefore has an optional `systemd_invocation_id` field, and the recovery gate now requires an exact match between that durable field and the current systemd observation. A missing, malformed, or mismatched value blocks the gate. The current launcher does not yet capture and durably bind this value, so existing intents cannot pass this stronger check and `AssessArtifactRetention` will report them as ineligible. This is intentional; no backfill or inference from the current unit is permitted.

Retention assessment also rejects a zero or future `CreatedAt` timestamp rather than treating a negative age as meaningful. These checks do not add deletion authorization. A future system-manager execution broker must record the invocation identity as part of its durable start protocol, and cleanup still requires a kernel-enforced exclusion boundary that prevents new processes or sibling scopes from appearing between verification and unlink.

### Immutable Job ownership enforcement

Remote ownership (`RemoteCallerNodeID`, `RemoteRequestID`) is now immutable at the central Job Manager read-modify-write boundary. The invariant applies both to normal mutations that acquire the injected lifecycle provider and to internal callers that already hold the lifecycle lock. A mutation that attempts to add, remove, partially clear, or replace either ownership field is rejected before the durable snapshot is written. This prevents a callback or internal locked path from detaching a remote-owned Job from its lifecycle lock identity.

### Lifecycle provider configuration is immutable

The Job Manager now accepts its lifecycle provider once. Reinstalling the same provider is idempotent, while attempting to replace it with a different provider fails closed. This matters because a provider replacement could otherwise split one Manager's remote-owned Job mutations across different lifecycle-lock domains. Comparable value providers are matched by value; pointer-backed providers are matched by pointer identity. The Coordinator may continue to call the setter on each Dispatch without changing the installed provider.

### One-time systemd InvocationID journal binding

`LaunchIntentJournal.BindSystemdInvocationID` now records a 32-hex systemd InvocationID only while the durable intent is in `launching`. The value is normalized to lowercase, persisted with the journal's temp-file/fsync/rename/directory-fsync sequence, and is immutable thereafter. Repeating the same binding is idempotent; a different ID, malformed ID, or binding outside `launching` is rejected. The method does not query systemd or launch a unit: a future trusted system-manager adapter must obtain the ID from the exact unit and call this method while holding the existing per-job lifecycle lock domain. No backfill is performed for old records.

### Durable metadata directory startup checks

The executable-artifact registry and launch-intent journal now use a shared Linux startup helper that walks the absolute directory path with `Lstat`, rejects symlink and non-directory components, creates missing components with mode `0700`, and requires the final directory to be owned by the effective UID before enforcing `0700`. Non-Linux platforms fail closed for this helper. This reduces accidental or pre-planted symlink redirection at initialization; it is explicitly not a complete defense against a concurrent attacker able to rename writable ancestors. Runtime file access is still path-based in parts of these stores, so descriptor-relative conversion and stronger state-root ownership remain outstanding before hostile same-UID or writable-ancestor threat models can be claimed safe.

### Descriptor-relative artifact and launch metadata I/O

The executable-artifact registry and launch-intent journal now pin their final metadata directories with directory file descriptors and perform record/lock operations relative to those descriptors using `openat2` with beneath/no-symlink resolution. Reads reject symlinks, non-regular files, unexpected ownership, group/world permissions, and records larger than 1 MiB. Lock files are opened without following symlinks and validated before `flock`. Create-only records use a synced temporary file plus descriptor-relative `linkat`; journal updates use a synced temporary file plus descriptor-relative `renameat`, followed by directory `fsync`. The implementation stays compatible with the module's Go 1.23 API baseline and fails closed when `openat2` is unavailable.

The directory descriptor pins the final directory inode if its pathname is later renamed. The startup check also compares the opened inode with the current path entry. This does not make an attacker-writable ancestor safe during the initial path walk, nor does it convert every other state store or the shared lifecycle-lock API to descriptor-relative access. The dedicated execution identity and system-manager broker remain prerequisites for hostile workload isolation.

On Linux, the shared per-job lifecycle lock now uses the same pinned-directory implementation, so Journal and Coordinator participants continue to contend on the same lock inode while avoiding path-based lock-file opens. Android retains its existing file-lock implementation. Registry and Journal expose `Close()` to release their pinned metadata directory descriptors; owners should close these stores during orderly shutdown.

### Ledger and execution-record store descriptor pinning

The remote-job Ledger and receiver-local ExecutionRecordStore now use the same pinned metadata-store primitive as the artifact registry and launch-intent journal. Ledger enumeration opens a fresh directory file description relative to the pinned directory FD; each JSON record is read through bounded, no-follow descriptor-relative access, and the ledger lock is acquired through the same directory FD. Ledger state replacement uses synced temporary files and descriptor-relative `renameat` plus directory `fsync`. Execution records use descriptor-relative per-request locks and bounded reads, with create-only durable writes via `linkat`; existing immutable-intent semantics are preserved. Both stores expose `Close()` for orderly descriptor release.

An explicitly supplied state-directory symlink alias is resolved once by canonicalizing the longest existing path prefix before creating missing store subdirectories. The store then pins and validates the canonical target; no record or lock operation traverses the original alias. On non-Linux platforms these descriptor-relative stores fail closed rather than silently falling back to path-based writes. This completes descriptor pinning for the three core federation stores (Ledger, ExecutionRecordStore, artifact registry) and Launch Intent Journal; it does not claim that every auxiliary coordinator lock or every state directory elsewhere in the application has been migrated.

### Policy and preparation lock pinning

The receiver PolicyStore now shares the pinned federation metadata directory. Policy reads are bounded, no-follow reads; updates use the same descriptor-relative synced replacement protocol as other metadata records. Save, Load, and the long-lived PolicyLease all use one descriptor-relative lock inode, so a policy revision cannot change while a dispatch lease is held. Symlinked policy and lock files fail closed. PolicyStore exposes `Close()`.

The cross-store `PrepareExecutionIntent` serialization lock is now stored as a private lock file in the pinned Ledger federation directory rather than creating and traversing a separate `remote-execution-coordinator` path. The lock key remains a SHA-256 digest of the caller/request identity. The execution-record-first, ledger-binding-second crash boundary is unchanged.

### Existing root broker is explicitly outside the remote-job trust boundary

A source audit of `cmd/cycomagent-root/main_linux.go` confirms that the installed `cycomagent-root` is a general-purpose privileged shell bridge, not a constrained worker supervisor. Its request accepts a shell and command string, executes `shell -lc <command>` as root, permits caller-selected working directory and environment additions, and inherits the root broker's environment. Its timeout cleanup kills only the direct command process group. Peer admission is based on `SO_PEERCRED` UID plus an optional executable pathname check; the pathname check intentionally strips `/proc/PID/exe`'s ` (deleted)` suffix and therefore does not bind the peer to the currently installed executable inode/digest. The listener also unlinks the configured socket path before binding and creates its parent by pathname.

These properties are incompatible with the remote-job contract, which requires fixed receiver-owned identity, structured argv, minimal environment, descriptor-pinned executable/CWD, immutable authorization evidence, and a system-manager-owned cgroup boundary. **Do not connect the remote-job protocol to this root broker, do not expose its socket to a worker, and do not reuse it as a system-manager execution bridge.** Its existing general-purpose host administration role remains separate; this audit did not alter its code or service.

The new worker profile is only a pure-data policy description at present. `BuildWorkerExecutionContract` is also validation/data construction only. There is still no dedicated worker account, system-manager unit installer, system-bus broker listener, authenticated start transaction, InvocationID capture, or launch consumer wired to these types. The current `cycomagent-root@.service` is not that missing broker. Production dispatch therefore remains unavailable by design.

### Job Manager boundary audit (remaining blocker)

The generic `internal/jobs.Manager` has lifecycle-lock enforcement for remote-owned Job mutations, immutable owner binding, remote-owned recovery deferral, and a prune retention barrier. However, its durable Job JSON and log files are still accessed through path-based operations (`CreateTemp`/`Rename`, `OpenFile`, `Remove`, `ReadDir`, and a path-based `.locks` directory). Linux final-component opens use `O_NOFOLLOW`, but the containing `jobs` and `.locks` directories are not pinned by directory FDs, and the initial state-path walk is not protected against a concurrently writable ancestor. This means the Manager has not yet reached the descriptor-relative storage assurance of the federation Ledger and journals. Treat this as an explicit security blocker for hostile same-UID workloads; do not claim the entire application state tree is descriptor-pinned.

`StartStructuredWithIDOwned` also relies on its documented in-process caller contract that the Coordinator already holds the lifecycle lock for the entire runner and terminal snapshot. It does not independently prove possession of that lock. The current production wiring is intentionally not a worker launch path, but a future broker integration must replace this implicit call contract with an unforgeable, broker-owned launch authorization/transaction boundary before dispatch is enabled.

### Worker D-Bus environment defense in depth

The pure-data system-manager WorkerServiceProfile now adds `UnsetEnvironment=DBUS_SESSION_BUS_ADDRESS XDG_RUNTIME_DIR`, in addition to making `/run/user` and the system bus socket inaccessible and creating a private network namespace. This prevents a worker service from inheriting session-bus routing variables from a contaminated system-manager environment. This property is only a profile specification today; no system-manager unit is installed or launched.


### Job Manager descriptor-relative storage (Linux)

The Linux (non-Android) `internal/jobs.Manager` now opens and pins the `jobs` directory and its `.locks` subdirectory at construction. Job record reads, log opens, lock creation/flocking, directory enumeration, temporary-file creation, atomic `renameat`, and removal use those pinned directory descriptors with `openat`/`unlinkat` and `O_NOFOLLOW`. Record and log opens require regular files owned by the Manager effective UID with no group/world permissions; records are capped at 1 MiB. Lock files are owner-checked and normalized to mode 0600. Atomic-write crash hooks now execute at the actual temp-created, written, synced, renamed, and directory-synced boundaries. A regression test replaces the visible `jobs` pathname after Manager construction and verifies reads/writes continue against the pinned directory.

This hardening has explicit limits. Initial directory creation and pinning still begin from a pathname; a concurrently writable ancestor can race the initial path walk. Android and other non-Linux builds retain a compatibility path-based store. The legacy `StartExternal`/`StartExternalWithSession` callback still hands an absolute log pathname to the desktop Session Bridge, which opens that path independently; this is not covered by the pinned-store guarantee and must not be used as the remote worker execution/logging path. Remote structured Jobs use the Manager's descriptor-backed log and record operations. The Manager currently has no orderly `Close` lifecycle for its pinned descriptors; do not add ad-hoc close calls while monitors or jobs may still be active.


### Short-lived Job Manager lifecycle capability

Remote-owned structured Job creation now requires a `jobs.LifecycleAuthorization` capability. `Manager.WithLifecycleAuthorization` obtains the configured shared lifecycle lock, mints a manager-bound capability for the exact caller/request pair, and invalidates it when the callback returns. `StartStructuredWithIDAuthorized` checks manager identity, exact caller/request binding, and active lifetime before persisting the remote-owned Job. The legacy `StartStructuredWithIDOwned` refuses non-empty remote ownership; unowned local structured jobs continue through `StartStructuredWithID`. `LaunchAsJob` now requires both a non-empty valid caller/request identity and the capability; there is no optional request-ID path that can silently create an unowned Job.

This capability proves only that the matching lifecycle lock is held. It is not a signature, peer credential, policy lease, or dispatch approval. The DispatchCoordinator independently verifies the signed dispatch proof, immutable execution intent, and receiver policy before invoking the callback that receives the capability. Do not expose `WithLifecycleAuthorization` as a network/API operation or treat it as an authentication mechanism.


### InvocationID binding evidence boundary

The journal no longer exposes a public API that accepts an arbitrary InvocationID string. Binding is performed through a package-private `systemdStartReceipt` whose fields cannot be constructed by callers outside `remotejobs`. The receipt requires explicit accepted-start evidence and the exact deterministic unit name from the immutable launch intent; the journal rejects missing acceptance evidence, unit mismatch, malformed IDs, non-launching state, and replacement. This is only an evidence boundary for a future trusted systemd adapter: no StartUnit caller or receipt producer is wired yet, and no systemd unit is started by this change.


### Active-unit InvocationID observation gate

InvocationID binding no longer accepts a synthetic in-process receipt or caller-supplied ID. The journal holds the shared per-job lifecycle lock, reads its own immutable intent, and asks the read-only `SystemdUnitInspector` to query that exact unit. Binding requires `LoadState=loaded`, `ActiveState=active`, the exact deterministic unit name, a canonical 32-hex InvocationID, and a canonical ControlGroup ending in that unit name. The binding is immutable. The journal also refuses `running` or `completed` transitions until an InvocationID has been bound. This proves only that the exact unit was observed active at inspection time; it does not prove which actor initiated the start or constitute authorization to launch. If a short-lived job exits before it can be observed active, the result must remain unresolved/unknown rather than inventing an InvocationID.


### Durable start-attempt identity

The launch-intent journal now generates a cryptographically random 128-bit `start_attempt_id` while atomically transitioning `prepared` to `launching`, before any future adapter can issue a systemd start request. The value is persisted under the shared lifecycle lock and survives reads/restarts. `launching`, `running`, and `completed` records without a valid canonical attempt ID are rejected. A future broker Start request must bind this ID together with the exact journal-derived unit name, immutable job/request identity, artifact digest and policy revision. An ambiguous reply must retain this same attempt as `unknown`; it must never mint a new attempt or replay the start. This is only durable correlation metadata: no systemd StartUnit request, service, account, listener, dispatch or worker is created by this change.

### Legacy local scope launcher separation

`Launch()` and `launcher_command_linux.go` remain a separate legacy structured local-launch path. That path uses a PID/time-derived transient `.scope` name under the per-user manager; it is not the durable broker-owned `.service` identity produced by `ExecutionUnitName`, and it is not connected to `LaunchIntentJournal` or the new `systemdStartRequest` description. Do not bind observations from that legacy scope to a remote launch intent, and do not interpret its current same-UID scope containment as the dedicated worker boundary. The new start request description is currently pure in-process data only; no StartUnit/systemd-run producer or consumer is wired.


### One-shot Start request reservation / no-replay boundary

The journal now exposes only an internal `reserveSystemdStartRequest` gate to the future same-package trusted adapter. Under the shared per-job lifecycle lock it requires the exact intent to be `launching`, requires its durable `start_attempt_id`, rejects any prior reservation, persists `start_request_reserved_at`, and only then returns the typed request. The typed request builder rejects records without that durable reservation. This is deliberately at-most-once, not exactly-once delivery: a crash after reservation but before the adapter sends, or after send but before acknowledgement, is indistinguishable and must be reconciled as `unknown`; no caller may reserve or send again. A future adapter must never treat the reservation itself as proof that systemd accepted the request. No StartUnit/systemd-run call or transport consumer is implemented here.


### Correlated systemd Start acknowledgement boundary

A future trusted adapter must return a private, versioned acknowledgement bound to caller/request IDs, remote/local job IDs, the immutable StartAttemptID, exact broker-derived unit name, artifact digest, policy revision, and payload hash. The journal accepts one acknowledgement only, under the shared lifecycle lock. `accepted` is persisted while the intent remains `launching`; only then may the read-only exact-unit inspector bind InvocationID. A definitive `rejected` acknowledgement atomically moves the intent to `failed`; `unknown` atomically moves it to terminal `unknown`. Once a request was reserved, generic transitions cannot manufacture `failed` or `unknown`, and running/completed require accepted acknowledgement plus verified InvocationID. Duplicate, stale, mismatched, and conflicting acknowledgements are rejected. The acknowledgement API is package-private and currently has no transport producer; it is not peer authentication and must only be called by a future trusted broker adapter after validating the actual systemd method reply. No systemd start operation is implemented.


### StartTransientUnit D-Bus reply classification (parser only)

The protocol now includes a pure decoder for a future `StartTransientUnit` method-return envelope. It accepts `accepted` only when the D-Bus reply sender equals the exact unique owner pinned from `GetNameOwner` before the call, the sender is syntactically a D-Bus unique name, the reply is a normal method return (no error name), and the returned job object path is exactly `/org/freedesktop/systemd1/job/<canonical-positive-id>`. Owner changes, method errors, missing/malformed job paths, and all ambiguous outcomes classify as `unknown`; the parser never claims `rejected`. The future transport must source the pinned owner and sender from the actual system bus connection/message header, not request data. No D-Bus connection, method call, systemd start operation, or transport implementation exists in this change; this is only a pure parser and regression suite.


### Crash recovery after one-shot reservation without a reply

A restart may find a `launching` intent with `StartRequestReservedAt` set but no durable systemd acknowledgement. Recovery now has a journal-only operation, `markReservedStartUncertain`, which serializes on the shared per-job lifecycle lock and atomically moves that attempt to terminal `unknown` with a separate `StartRequestUncertainAt` marker. It does not invent `StartRequestOutcome` or `StartRequestAckAt`: no reply was observed, so no acknowledgement exists. The operation is idempotent for an already-marked record, refuses records outside the reserved/no-ack launching state, rejects late acknowledgements, and permanently prevents reservation/replay. This helper is not wired into startup or any production dispatch/recovery loop; callers must not interpret it as proof that systemd did or did not start a unit. It records only that the outcome cannot safely be replayed.


### Dedicated worker-broker wire contract (schema only)

A separate Linux-only `workerBrokerStartRequest` now defines the narrow coordinator-to-worker-broker handoff. It is derived from the reserved `systemdStartRequest` and carries only caller/request/job identities, the durable start-attempt ID, broker-derived deterministic unit name, immutable artifact name/digest/device/inode, policy revision, and payload hash. Strict bounded JSON decoding rejects duplicate keys, unknown fields, trailing data, malformed identities, noncanonical attempt IDs, artifact references outside the opaque-reference grammar, and zero artifact identity values. In particular, the schema has no argv/command, environment, UID/GID, executable or working-directory path, socket/cgroup path, or systemd property fields. The broker must derive all execution details from receiver-owned authorization and pinned descriptors. This is a pure schema/codec with tests only: no socket, listener, request handler, D-Bus transport, or worker launch is wired.

### Worker-broker IPC framing (caller-owned connection only)

Linux-only `readWorkerBrokerStartRequest` and `writeWorkerBrokerStartRequest` now provide bounded length-prefixed transport helpers for the schema above. The frame length is checked against the worker-specific 8 KiB cap before payload allocation; reads and writes require a positive timeout no greater than the existing 30-second broker I/O ceiling. JSON decoding remains strict and validates the complete immutable request identity. Both helpers accept only an already-connected `net.Conn`: they do not dial, listen, accept, authenticate peers, authorize requests, transfer descriptors, reply with an execution result, or launch work. The caller remains responsible for kernel peer authentication and connection lifecycle. Tests use `net.Pipe` and do not create filesystem sockets or services.

### Worker-broker session admission (one accepted connection, no dispatch)

Linux-only `ServeAuthenticatedWorkerBrokerConnection` defines the session boundary for a future externally-created Unix listener. It owns one supplied `*net.UnixConn`, authenticates the kernel peer using the existing SO_PEERCRED + SO_PEERPIDFD + executable-digest check before reading any request, retains the pidfd until the handler returns, reads exactly one bounded Worker Broker frame, and compares every request identity/artifact field against a receiver-supplied `systemdStartRequest`. That expected request must originate from the receiver's durable one-shot reservation; it is not read from or inferred from the connection. Admission rejects incomplete peer identity, UID or executable digest mismatch, invalid expected reservation, and any attempt/unit/artifact/policy/payload substitution. Only after all checks does the supplied callback run. This callback is an extension point, not a start adapter: the session function itself does not reserve, transfer descriptors, invoke D-Bus/systemd, or launch work. Unit tests cover the pure admission predicate with synthetic identity evidence; they do not claim to test kernel SO_PEERPIDFD or a live Unix socket. No listener, socket file, service, account, or production wiring was added.

### Systemd D-Bus owner pin (protocol evidence only)

The Linux protocol now models a systemd owner pin as immutable, connection-generation-scoped evidence: a locally generated 128-bit connection nonce, the exact `org.freedesktop.systemd1` well-known name, the unique owner returned by `GetNameOwner`, and the observation time. The pin is permanently invalidated when a matching `NameOwnerChanged` observation reports any owner change or disappearance; it cannot be refreshed in place or reused after reconnect. A StartTransientUnit method reply is classified as accepted only when its actual Sender matches the still-valid pin, the method returned no D-Bus error, and the job object path is canonical. A reconnect requires a new connection nonce and fresh owner observation. These functions validate evidence supplied by a future D-Bus transport; they do not connect to the system bus, subscribe to signals, issue GetNameOwner, or call StartTransientUnit. No D-Bus transport dependency or production bus interaction has been added.


### Connection-scoped owner session serialization

The owner pin is now held inside a mutex-serialized `systemdBusOwnerSession`; method reply objects no longer carry a copy of the pin that could outlive or bypass invalidation. The session generates its own random connection-generation nonce, validates the actual reply Sender against its private pinned owner, serializes `NameOwnerChanged` invalidation against reply classification, and permanently invalidates on transport close. A transport implementation must deliver D-Bus signals and method replies through one ordered reader/dispatcher and call these session methods in received-message order; it must not race independent signal and reply goroutines. These changes remain pure in-process protocol state: no D-Bus library, connection, signal subscription, method call, listener, or dispatch path was introduced.

### Ordered D-Bus receive dispatcher boundary

A pure in-process `systemdBusMessageDispatcher` now defines the ordering contract for a future trusted D-Bus transport. The transport must use one ordered reader/dispatcher, assign a monotonically increasing receive sequence to every decoded bus message, and forward relevant `NameOwnerChanged` and StartTransientUnit method-return messages with that sequence. Sequence gaps are permitted for unrelated bus messages; zero, duplicate, or regressing sequence values permanently invalidate the connection generation. Owner-change invalidation and method-reply classification are serialized under the dispatcher lock, so a reply delivered after an owner-change event cannot be accepted. Any malformed owner event, ambiguous reply, or transport close fails closed; the start attempt is not replayed.

This is only a protocol boundary and test harness. It does not implement D-Bus wire decoding, a bus socket reader, match-rule installation, GetNameOwner, signal subscription, StartTransientUnit, or any production listener/dispatch. The future transport must assign sequence numbers from its single actual receive loop; independent goroutines must not race signal and reply delivery into the dispatcher. A process restart or reconnect requires a fresh connection generation, owner pin, and dispatcher.

### StartTransientUnit method-reply serial correlation

The ordered dispatcher now requires a single registered Start method-call serial before it will classify any reply. The future connection writer must allocate the D-Bus serial and register that exact nonzero value against the already-reserved durable `systemdStartRequest` before sending. A decoded method return/error must provide `ReplySerial` from its actual D-Bus header; it is accepted for classification only when it matches the registered serial on this same connection generation. The dispatcher derives the acknowledgement identity from its stored request, not from reply-supplied job fields. Missing registration, zero/mismatched serial, duplicate reply, or a reply after owner invalidation closes the generation and yields unknown/no-replay. Only one start method call and one corresponding reply are supported per dispatcher instance.

This does not allocate D-Bus serials, write messages, or contact systemd. A future transport must ensure serial registration and the actual write are serialized on the same connection writer; a write failure after registration is ambiguous and must be persisted as unknown rather than retried. The serial is connection-local and becomes invalid when the connection generation closes.

### Decoded D-Bus message provenance gate

A protocol-only decoded-message validator now sits before the ordered dispatcher. `NameOwnerChanged` is accepted only as a signal from `org.freedesktop.DBus`, at `/org/freedesktop/DBus`, on the `org.freedesktop.DBus` interface, member `NameOwnerChanged`, signature `sss`, with a nonzero message serial and exactly three valid bus-name/unique-owner fields. Method returns/errors require a unique-name sender and nonzero `ReplySerial`; a normal return must have signature `o` and one canonical systemd job object path, while an error must have a valid D-Bus error name and a valid optional string body. Unexpected headers, body shapes, message types, destinations, serial fields, or sender forms fail closed.

The dispatcher consumes only the resulting package-private typed envelopes, rather than loose strings and reply fields. The representation is still only as trustworthy as the future D-Bus decoder that populates it: that decoder must copy header/body values from the actual received wire message, preserve receive order, and assign sequence numbers in its single reader. No D-Bus wire decoder, socket, match rule, bus connection, or method call is implemented here.

### Bounded D-Bus fixed-frame header gate

A pure, allocation-free fixed-header inspector now validates D-Bus byte order, message type, reserved flags, protocol version, nonzero message serial, header-field-array length, body length, 8-byte body alignment, and overflow-safe total frame size before any future transport allocates or reads the variable sections. This coordinator-specific transport profile caps the header-field array at 16 KiB and body at 8 KiB (maximum frame below 25 KiB), which is sufficient for the narrowly scoped owner-change and systemd job-method messages. The returned total size is a bound for a future exact-length read, not proof that the payload is valid.

This is not a complete D-Bus wire decoder: it does not parse variant-encoded header fields, validate the body signature against wire bytes, perform authentication/Hello, or read a socket. A future reader must use the returned bounded frame size, enforce an I/O deadline, parse all header fields with D-Bus alignment/type rules, reject duplicate or unknown required fields, validate body/signature, then construct the private decoded-message envelope. It must never trust lengths before this fixed-header gate.


### Bounded D-Bus wire decoder

A pure frame decoder now consumes only an exact byte slice whose length matches the previously validated fixed header. It parses the D-Bus header-field array with 8-byte struct alignment and per-variant value alignment, accepts only the field codes/types needed by the coordinator, rejects duplicate and unsupported fields, validates string/signature lengths, NUL terminators and UTF-8, and validates object paths. It decodes only the narrow body signatures required here (`sss`, `o`, optional `s`, or empty) and rejects trailing/unparsed bytes. Both byte orders are covered by wire-level fixtures.

The real-wire tests exposed and corrected an earlier protocol assumption: a D-Bus method reply has its own nonzero message `serial` as well as the request-correlating `ReplySerial`. Both are now required. The decoder is still not a transport: it does not authenticate/Hello, read from a socket, handle UNIX_FD ancillary data, install match rules, or invoke systemd. Unsupported D-Bus message/field forms fail closed; a future reader must classify unrelated messages separately while still advancing its receive sequence.


### D-Bus message-type validation and fuzz coverage

The bounded decoder now enforces required header fields by message type: method calls require PATH and MEMBER; method returns require REPLY_SERIAL; errors require ERROR_NAME and REPLY_SERIAL; signals require PATH, INTERFACE and MEMBER. It validates the grammar of object paths, interface/error names, member names, and bus names; nonempty bodies require a signature header, and only the narrow supported signatures are accepted. D-Bus alignment padding (including the header-to-body gap) must be zero.

Two Go fuzz targets cover the fixed 16-byte header inspector and full bounded frame decoder. Short local fuzz campaigns completed without crashes: approximately 590k decoder executions and 530k fixed-header executions, covering both byte orders and malformed/truncated inputs. These are parser robustness checks, not a substitute for interoperability tests against a real D-Bus implementation.


### Ordered decoded-message classification

The protocol dispatcher now exposes one ordered entrypoint for decoded bus messages. Every frame presented by the future single reader advances the same monotonic receive sequence, including unrelated method calls, signals, and replies. Unrelated messages are ignored only after sequence acceptance. A `NameOwnerChanged` signal is acted upon only for `org.freedesktop.systemd1`; owner changes for other well-known names do not invalidate the systemd owner pin. A malformed signal using the exact D-Bus daemon `NameOwnerChanged` envelope fails closed. Method replies are acted upon only when `ReplySerial` matches the one registered start call; a correlated but malformed/wrong-sender reply becomes unknown and closes the connection generation. Duplicate, zero, or regressing sequence values still permanently invalidate the session.

The older focused dispatcher helpers remain for protocol unit tests. A future transport must use the unified `dispatchDecodedMessage` entrypoint exclusively and must not route signals and replies through separate goroutines or separate sequence counters. This remains a pure in-memory protocol boundary: no bus connection, reader, signal subscription, method call, listener, service, or production dispatch has been added.


### Bounded single-reader frame pump (in-memory transport boundary)

A Linux-only single-reader abstraction now consumes an already-provided `io.Reader`; it does not dial, authenticate, own, or configure a socket. It reads the 16-byte fixed header first, validates the bounded header/body lengths before allocating, then reads exactly the remainder of one frame with `io.ReadFull`. Each successfully decoded frame receives a locally assigned monotonically increasing sequence and is sent through the unified ordered dispatcher, including unrelated messages. The reader rejects concurrent read invocations, treats EOF/disconnect and malformed/truncated frames as generation-ending failures, and cannot be reused after failure.

The source reader's blocking behavior and deadlines remain the caller's responsibility; this abstraction does not promise a timeout for an arbitrary `io.Reader`. Tests use only `bytes.Reader` and a gated in-memory reader. A fuzz target covers bounded frame acquisition. There is still no Unix socket, system bus connection, D-Bus authentication/Hello, deadline setup, match-rule subscription, GetNameOwner, StartTransientUnit, listener, service, or production dispatch.

### Reader cancellation and lifecycle closure

The single-reader source contract is now explicitly context-aware and closeable. A future transport adapter must implement `ReadContext(ctx, p)` so cancellation interrupts a blocked read, and `Close()` so explicit shutdown also unblocks it. The reader rejects plain `io.Reader` sources for dispatch, applies the caller's context to every partial read, installs cancellation-triggered closure, and checks cancellation again before ordered dispatch. Explicit `Close`, cancellation, EOF, malformed/truncated input, decode failure, sequence exhaustion, or dispatch failure permanently close the reader generation and invalidate the owner session. Close is idempotent; a fresh connection requires a fresh source, owner pin, and dispatcher. A concurrent second read is rejected without disturbing the active read.

This contract does not itself set socket deadlines or create goroutines to impose timeouts. The future adapter must implement context cancellation and close-unblocks-read correctly, and the owner must supply an appropriate deadline/cancellation policy. Tests use in-memory sources only, including a gated source that proves cancellation interrupts a blocked read. No real connection, socket, D-Bus authentication, bus subscription, systemd method call, listener, or production lifecycle wiring is present.

### D-Bus EXTERNAL authentication and connection handshake state machine

A pure Linux protocol state machine now models the required connection establishment order without opening a connection or writing to a socket. It emits the initial NUL plus a SASL EXTERNAL command whose initial response is the hexadecimal encoding of the decimal effective UID supplied by the future local transport. The server response is accepted only as one bounded CRLF-delimited line containing exactly OK and a 32-hex-character D-Bus GUID. The GUID is retained as connection metadata. REJECTED, ERROR, DATA challenges, malformed GUIDs, embedded delimiters/NUL, and oversized lines fail closed. The only supported mechanism is EXTERNAL; there is no fallback or interactive challenge path.

After authentication, the state machine emits BEGIN and requires an explicit transport write-completion confirmation before accepting the correlated Hello method reply. Hello must be a method return from org.freedesktop.DBus, correlate to the registered nonzero serial, contain exactly one valid unique bus name, and be addressed to that same unique name. The next required step is AddMatch with the exact NameOwnerChanged rule for org.freedesktop.systemd1. Only after a successful correlated empty method return may the transport register GetNameOwner for org.freedesktop.systemd1. Its reply must be from the bus daemon, addressed to the Hello unique name, correlated to the registered serial, and contain exactly one valid unique owner. Only then is the connection-generation-scoped systemd owner session created.

The handshake also requires the future single ordered reader to feed decoded frames through observeHandshakeMessage while AddMatch is installed and GetNameOwner is pending. An exact systemd NameOwnerChanged signal in that interval invalidates the handshake rather than allowing a possibly stale owner query result to be pinned. Signals for unrelated well-known names are ignored; malformed signals using the exact daemon signal envelope fail closed. Once GetNameOwner completes, subsequent owner-change signals must be routed through the regular ordered dispatcher. Close permanently ends the handshake generation; reconnect requires a fresh handshake, GUID observation, Hello unique name, and owner pin.

These methods validate protocol evidence and call ordering only. The caller-supplied UID must be derived from the actual local process identity, not remote request data. A future transport must send the returned EXTERNAL/BEGIN commands and the exact Hello/AddMatch/GetNameOwner calls on the same authenticated connection, register each D-Bus serial before sending, report write completion, and deliver all inbound frames in wire order. This implementation does not perform SASL I/O, D-Bus authentication, Hello, AddMatch, GetNameOwner, socket creation, peer credential inspection, reconnect, listener setup, or production dispatch. Tests use synthetic decoded messages and in-memory state only.

### Unified D-Bus bootstrap coordinator and bounded reader

A single systemdBusBootstrapSession now coordinates the connection-generation protocol. It owns the SASL handshake, serial correlation for Hello/AddMatch/GetNameOwner, the monotonic receive sequence, and the eventual handoff to the existing systemd start-message dispatcher. Every decoded binary message passes through dispatchDecodedMessage, including unrelated traffic during bootstrap. The same sequence continues after GetNameOwner succeeds; there is no second reader or reset sequence at the dispatcher boundary.

The coordinator returns typed outbound call descriptions for Hello, AddMatch, and GetNameOwner. The future writer must allocate a connection-local nonzero serial, register that serial with the coordinator before sending the exact call on the same connection, and treat a write ambiguity as connection failure. AddMatch carries exactly one string argument containing the fixed NameOwnerChanged rule; GetNameOwner carries exactly the fixed systemd well-known name. Start-call registration is refused until the verified owner session and start dispatcher exist.

A systemdBusBootstrapReader consumes one already-open systemdBusContextReadCloser. Before binary mode it reads one CRLF-terminated SASL line into a fixed bounded buffer; after the caller confirms BEGIN was written, it reads fixed D-Bus headers first, validates size before allocation, reads the exact frame, decodes it, and submits it to the single bootstrap coordinator. Both phases use the same source and connection generation. Concurrent reads are rejected. Cancellation closes the source and invalidates the handshake/owner generation; malformed input, EOF, read errors, and dispatch errors also close it. Close is idempotent.

The reader has no writer and does not create, dial, configure, or reconnect a socket. The coordinator and reader tests use in-memory sources and synthetic D-Bus frames only. No real SASL exchange, Hello, AddMatch, GetNameOwner, system-bus connection, StartTransientUnit call, service, listener, or production dispatch has been implemented or enabled. The future transport remains responsible for authenticating the actual local peer, deriving the effective UID from local process credentials, writing all returned commands/calls on the same stream, registering D-Bus serials before writes, and feeding every inbound frame through this one reader in wire order.

### Strict outbound bootstrap frame encoding and serial reservation

The bootstrap protocol now has a narrow outbound D-Bus encoder for exactly three method calls: Hello, AddMatch, and GetNameOwner. It emits canonical little-endian D-Bus method-call frames with bounded header/body sizes, sorted header fields, zero alignment padding, exact destination/path/interface/member values, and only the expected empty or single-string body. AddMatch accepts only the fixed systemd NameOwnerChanged match rule; GetNameOwner accepts only org.freedesktop.systemd1. This is not a general-purpose D-Bus encoder and does not encode StartTransientUnit.

The bootstrap session also owns a connection-local monotonically increasing serial allocator. Serial values are reserved before an encoded frame is returned to the future writer; manually supplied serials used by lower-level protocol tests are also reserved and cannot be reused. Zero and wraparound are forbidden. A prepared call returns its serial, typed call description, and encoded bytes together, while the writer remains responsible for sending the frame on the same authenticated connection. No outbound frame is written by this implementation.

### Single-writer outbound lifecycle and no-replay claims

A protocol-only systemdBusSingleWriter now serializes writes to one already-open context-aware output stream. It accepts only the exact state-machine-produced BEGIN command and prepared bootstrap frames whose typed call description re-encodes byte-for-byte and whose decoded D-Bus header matches the registered serial and call identity. The shared bootstrap session claims BEGIN once and claims each prepared Hello/AddMatch/GetNameOwner serial once, only while that exact transition is pending. A second writer instance cannot bypass these claims because they live in the connection session.

The writer handles short writes by continuing the same frame, but any write error after zero or more bytes, zero progress, invalid write counts, cancellation, or malformed frame permanently closes the stream and handshake/owner generation. It never retries a frame and does not queue asynchronous work. The future transport must ensure reader and writer share the same already-authenticated connection and generation; it must also serialize SASL EXTERNAL before this writer's BEGIN phase. Tests use in-memory sinks only, including short writes, partial-write failure, cancellation, tampering, and duplicate-send attempts.

A StartTransientUnit encoder is intentionally not added at this point. The durable start request intentionally contains no executable path or argv, and the current worker artifact identity is only an opaque registry reference. Encoding ExecStart before the broker has a trusted, descriptor-bound worker executable identity and a complete validated worker service profile would create a path/command injection seam. The existing narrow decoder and reply correlation remain parser-only. A future encoder must be built from broker-owned worker configuration and validated profile properties, never from caller-supplied paths, commands, environment, UID/GID, or arbitrary systemd properties.

### Descriptor-bound executable identity capability

A Linux-only `VerifiedExecutableArtifact` now opens a registered artifact relative to the already-pinned broker staging directory and retains the resulting read-only file descriptor. Creation verifies the durable record's opaque stage name, broker-owned staging directory, regular-file type, exact 0555 mode, broker UID, device, inode, size, and SHA-256 digest. `Verify` repeats metadata and digest checks against the retained descriptor; `DuplicateReadOnlyDescriptor` returns a CLOEXEC duplicate only after revalidation; `Record` returns the immutable registry binding only after revalidation. Capability methods serialize access with an internal mutex.

The descriptor remains bound to the opened inode if the directory entry is renamed or replaced; this is deliberately different from re-resolving a mutable path. The capability exposes no pathname, argv, environment, UID/GID override, systemd properties, or launch method. A duplicated descriptor is not yet a safe systemd `ExecStart` reference: the system manager does not inherit the broker's descriptor table, and passing `/proc/<pid>/fd/...` would introduce lifetime, access-control, and race assumptions. No StartTransientUnit encoding or invocation is enabled by this change. The tests verify retained-inode behavior and replacement rejection using temporary files only.

### Verified executable descriptor handoff

The worker handoff now has explicit Linux-only helpers around the existing two-descriptor SCM_RIGHTS frame. `SendVerifiedWorkerDescriptors` accepts a `VerifiedExecutableArtifact`, obtains a fresh CLOEXEC read-only duplicate only after rechecking its retained inode and digest, and sends that descriptor alongside the already-authorized working-directory descriptor. It closes only its temporary duplicate; the sender's retained capability and directory descriptor remain owned by the sender.

`ReceiveVerifiedWorkerDescriptors` takes ownership of the received descriptors and validates the executable descriptor directly against the receiver's independently loaded durable `ExecutableArtifactRecord`: the open-file access mode must be O_RDONLY; the file must be regular and exactly mode 0555; owner UID, device, inode, size and SHA-256 must match. No path is reopened. Both received descriptors are closed on any validation failure. The receiver still must authenticate the peer and correlate the accompanying job/start request before treating the descriptor as admitted. Tests use a temporary Unix socket pair and temporary staged files, and verify exact inode transfer plus rejection of a substituted digest.

This handoff only proves which bytes were transferred to the receiving process. It does not prove a systemd service manager can access the descriptor, does not create a service unit, and does not launch or execute it. The broker/worker connection is not opened by these helpers; no listener, socket unit, service account, or production dispatch path is configured.

### Worker-side received execution admission

AdmitWorkerReceivedExecution is the Worker-side, non-launching admission boundary. It composes the authenticated kernel peer identity and peer policy, the receiver's durable one-shot systemdStartRequest, the decoded worker request, the independently loaded ExecutableArtifactRecord, and the two already-received descriptors. The artifact record must match every durable job/policy/payload field and the opaque artifact name, digest, device and inode carried by the request. The executable descriptor is then revalidated as read-only against owner/mode/device/inode/size/SHA-256; the working-directory descriptor must still identify a directory.

On success, WorkerReceivedExecution takes ownership of both descriptors and exposes only the immutable artifact record; Close releases both descriptors. On admission failure, ownership remains with the caller. Its accessors and close operation serialize concurrent use. This object intentionally has no method to invoke a process, build an ExecStart, contact systemd, or resolve a caller-supplied path. It is an admission result only, not a runnable job.

The current tests use synthetic peer identity values and temporary descriptors. They do not authenticate a live worker process or prove that a system service manager can consume the descriptor. No worker listener, service account, systemd unit, Dispatch API, Federation Process Launch, or production launch wiring is added.

### Worker backend preparation boundary

WorkerReceivedExecution.PrepareBackend now produces a broker-owned WorkerBackendPreparation only after revalidating the executable descriptor and the fixed WorkerServiceProfile. The profile is reconstructed and compared against the canonical baseline, including numeric non-root worker UID/GID, runtime limit, and the exact hardening properties and inaccessible-path set. The preparation captures the working-directory descriptor's device/inode and copies profile slices so later caller mutation cannot alter the admitted object's internal state.

Revalidate checks the preparation's durable request/artifact binding, fixed profile, runtime limit, executable descriptor identity/digest, and working-directory descriptor identity again. The returned preparation is descriptive only: ExecutionDisabled is always true, and the type contains no executable path, CWD path, argv, environment, descriptor number, or launch callback. It cannot be translated into ExecStart by this package.

This deliberately stops before backend execution. In particular, systemd's service manager cannot be assumed to inherit the Worker process's SCM_RIGHTS descriptors. A future backend needs a separately reviewed, descriptor-capable handoff design and Dispatch Gate Review; this change does not add D-Bus calls, StartTransientUnit encoding, listener/service wiring, account creation, Dispatch API, Federation Process Launch, or production execution.


See also [`REMOTE_JOBS_DESCRIPTOR_BACKEND_DESIGN.md`](REMOTE_JOBS_DESCRIPTOR_BACKEND_DESIGN.md) for the reviewed systemd descriptor-transfer constraints and non-approved backend candidates.
