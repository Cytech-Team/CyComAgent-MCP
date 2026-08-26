# Contributing

CyComAgent-MCP deliberately keeps the core tool surface small.

Before adding a new core tool, ask:

1. Can the same job be composed from existing primitives?
2. Is the proposed capability useful across multiple unrelated workflows?
3. Does it return structured observations that help an AI decide what to do next?
4. Is it an OS capability, or is it an application-specific workflow?

Application-specific integrations should generally become optional adapters or
plugins rather than permanent core tools.

## Development

```bash
go test ./...
go vet ./...
go build -o cycomagent ./cmd/cycomagent
```

Run locally:

```bash
./cycomagent --mode http --addr 127.0.0.1:7331
./scripts/doctor.sh
```
