# CyComAgent-MCP v0.4.0-termux-dev

First Android/Termux development build.

## Added

- Android/arm64 build target for the core CyComAgent runtime.
- Termux host detection without treating every Android host as Termux.
- Platform-aware shell selection so local execution no longer assumes `/bin/sh`.
- Termux-safe runtime/root-socket paths.
- `termux-services`/runit adapter for `service_control`.
- Termux capability detection for runit and selected Termux:API commands.
- `mobile_assistant` runtime profile that tells MCP clients to prefer semantic `assistant_*` phone tools over raw shell/API calls.
- Assistant tools for speech-to-text, TTS, notifications, battery/Wi-Fi/audio status, location, clipboard, vibration, torch, SMS, calls, camera, microphone, contacts, SMS reading and call log.
- Sensitive Android policy gate (`allow_sensitive_android`, default false).
- Optional Termux:Boot autostart helper with opt-in wake lock.
- Termux installer, runit service definition, smoke-test script, CI artifact, and release packaging.

## Deliberately not enabled yet

- Android device root / `su` / Magisk integration. This is not supported and is not planned. Remote SSH sudo and sudo inside non-root user-space environments (for example proot) remain allowed.
- Raw/unrestricted Termux:API command execution. The runtime only exposes an allowlisted bridge and Android still enforces the app permissions required by each command.
- macOS support.

## Known Android constraints

Android may stop Termux child processes under background/phantom-process limits. CyComAgent can be supervised by runit, but the OS remains authoritative over app process lifetime.
