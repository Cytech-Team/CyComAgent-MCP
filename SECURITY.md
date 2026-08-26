# Security model

CyComAgent-MCP Full Power is intentionally powerful. An authorized MCP client can read/write files, execute programs, manage services, send desktop input, configure SSH targets, transfer files, and optionally execute commands as root.

## Trust model

Treat an MCP client authorized to call a `full` policy runtime as equivalent to a highly privileged operator of the configured OS account. If the root broker is enabled, authorized privileged tool calls can become host root operations.

## HTTP

- Default listen address is loopback only.
- Non-loopback binding without `CYCOM_TOKEN` is refused by default.
- `CYCOM_TOKEN` can be sent as `Authorization: Bearer ...` or `X-CyCom-Token`.
- Browser `Origin` is rejected unless it is syntactically loopback, reducing DNS-rebinding exposure.
- With OpenAI tunnel-client, keep the MCP origin on loopback and let the tunnel provide the remote transport.

## Policy

`$STATE_DIR/policy.json` defaults to `mode: full`. Deployments that should not allow mutation/root/remote operations can use `safe` or `readonly` and optional allow/deny tool globs.

Policy is an application control, not an OS sandbox. A client permitted to use generic shell/file primitives with the runtime user's authority may still be able to alter files that influence future runtime behavior.

## Root broker

The root broker:

- has no TCP listener;
- uses a local Unix socket;
- checks `SO_PEERCRED` UID and PID;
- optionally validates `/proc/PID/exe` against an exact allowed executable;
- runs commands in process groups and kills the group on timeout.

The shipped systemd unit allows the configured UID only when the peer executable is `/usr/local/bin/cycomagent`.

This is stronger than UID-only socket authorization, but it does not magically make Full Power unprivileged. The configured user controls an agent that is intentionally capable of requesting root actions.

## Remote targets

- Passwords are not stored by target definitions.
- Use OpenSSH config, ssh-agent, or a protected identity file.
- Batch mode is enabled for unattended operation.
- Host-key handling defaults to `StrictHostKeyChecking=accept-new`; changed known host keys are not silently accepted.
- Remote privileged commands use `sudo -n` and therefore depend on the remote host's sudo policy. On Termux this does **not** imply Android device root.
- Android device root / `su` / Magisk integration is not supported and not planned. User-space `sudo` inside proot/container environments is treated as an ordinary command and cannot escape Android app permissions by itself.
- `ssh_options` is an advanced Full Power field and can materially change OpenSSH behavior. Only trusted clients should be permitted to modify targets.

## Audit

Tool calls are logged as JSONL with tool name, duration, result and a SHA-256 of the argument document. Raw tool arguments are not copied into the audit log by default because they may contain secrets.

## External plugins

External plugin manifests can execute arbitrary configured programs. Full Power policy allows them by default. Plugins are bounded by output limits, timeouts and process-group termination, but their executable code is trusted code under the runtime user's authority.

## Secrets

`system_env` requires explicit variable names, but generic filesystem/shell capabilities can still access secrets that the configured OS account can access. OS permissions and target credential permissions remain authoritative.

## Android / Termux mobile assistant boundary

Termux mode is non-root by design. Android device root, `su`, Magisk and a local Android privilege broker are not supported and are not planned. Semantic `assistant_*` tools and the allowlisted `android_api_*` bridge execute as the ordinary Termux app user and remain subject to Android permissions.

High-sensitivity capabilities such as SMS send/read, phone calls, contacts, camera and microphone recording require `allow_sensitive_android=true` in the runtime policy in addition to any Android permission. The default is `false`. Remote SSH sudo and sudo inside a non-root proot/container are separate capabilities and do not grant Android device root.
