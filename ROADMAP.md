# Roadmap

CyComAgent-MCP follows one rule: **give AI capabilities, not workflows**.

## v0.3 Full Power — current development build

- stateless direct HTTP MCP
- 42 generic primitives
- persistent background jobs
- explicit durable state
- durable multi-machine target registry
- stateless-per-operation OpenSSH target adapter
- remote copy through scp
- target probing
- stdin for synchronous process execution
- policy engine (`full`, `safe`, `readonly`)
- central structured audit log
- adapter capability scoring
- optional root privilege broker with UID + peer executable validation
- hardened external plugins (timeout/output/process-group)
- desktop capture/input adapters
- direct tunnel deployment
- health/readiness/metrics
- watchdog, smoke, chaos and job recovery tests

## Next — portability

- native macOS service/process/desktop adapters
- native Windows service/process/desktop adapters
- platform-specific process-group abstraction
- release matrix for linux/amd64, linux/arm64, macOS, Windows where supported

## Next — optional transports

- agent-to-agent local transport
- container namespace adapter
- optional WinRM/PowerShell remote adapter
- SSH jump-host/profile discovery helpers without introducing hidden live-session state

## Next — deeper observability and policy

- stable reason/error codes
- Prometheus counters for policy denials, target calls and adapter failures
- opt-in argument field redaction policies
- signed capability plugin manifests
- SBOM + release signing

## Optional developer capabilities

- LSP/AST helpers as plugins
- repository indexing as an optional module
- structured diff/diagnostic modules

These should remain capabilities. No milestone should introduce built-in application workflows when existing primitives can be composed by the AI instead.
