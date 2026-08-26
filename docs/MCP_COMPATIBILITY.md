# MCP compatibility

CyComAgent-MCP implements the MCP surface it needs for a generic computer tool runtime. It is deliberately small and does not claim to be a complete SDK replacement.

## Modern protocol

Preferred protocol: `2026-07-28`.

Supported runtime methods:

| Method | Status | Notes |
| --- | --- | --- |
| `server/discover` | supported | reports tool capability and supported protocol versions |
| `tools/list` | supported | returns built-in and dynamically loaded external tools |
| `tools/call` | supported | structured content, text content, image content, tool errors |

Modern HTTP requests are independent. Runtime state that must outlive a request is explicitly persisted by CyComAgent rather than stored in an MCP transport session.

## Legacy compatibility

Accepted legacy protocol versions:

- `2025-11-25`
- `2025-06-18`
- `2025-03-26`

Compatibility methods/features:

- `initialize`
- `ping`
- JSON-RPC over stdio
- `GET /mcp` legacy SSE **probe compatibility**

The GET endpoint is not a full legacy stateful SSE transport. It exists so older health/discovery probes do not receive an immediate 404 while modern tool traffic remains stateless HTTP POST.

## HTTP routing validation

CyComAgent understands:

- `MCP-Protocol-Version`
- `Mcp-Method`
- `Mcp-Name`
- protocol version in request `_meta`

`CYCOM_STRICT_MCP=true` enforces modern routing headers more strictly. Compatibility mode is the default because tunnel/client versions do not all migrate simultaneously.

## Authentication

When `CYCOM_TOKEN` is configured, both GET and POST `/mcp` require either:

```text
Authorization: Bearer <token>
```

or:

```text
X-CyCom-Token: <token>
```

Health endpoints remain local-operator endpoints and should stay bound to loopback unless protected by another trusted layer.
