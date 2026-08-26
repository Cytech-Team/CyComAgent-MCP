# Direct OpenAI tunnel deployment

CyComAgent-MCP and tunnel-client should be separate long-lived services.

```text
ChatGPT -> OpenAI tunnel -> tunnel-client -> 127.0.0.1:7331/mcp -> CyComAgent
```

The tunnel service should use:

```text
MCP_SERVER_URL=http://127.0.0.1:7331/mcp
```

Do not make tunnel-client spawn CyComAgent as a stdio child in the persistent host deployment. If the tunnel process is restarted, the runtime should remain alive.

The included `cycomagent-tunnel@.service` follows the same long-running systemd pattern as the official tunnel-client deployment documentation: network-online dependency, separate MCP_SERVER_URL, and automatic restart.

## Optional local MCP token

If `CYCOM_TOKEN` is configured on the runtime, tunnel-client can send the token only to the MCP origin:

```text
MCP_EXTRA_HEADERS=X-CyCom-Token: your-secret
MCP_DISCOVERY_EXTRA_HEADERS=X-CyCom-Token: your-secret
```

The discovery header matters because tunnel-client may perform a separate startup discovery/probe before forwarding normal MCP calls.
