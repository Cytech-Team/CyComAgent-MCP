# External tools

External tools keep specialized integrations outside the core.

A plugin is a JSON manifest in `$CYCOM_PLUGIN_DIR`.

```json
{
  "name": "my_tool",
  "description": "Do one specialized operation.",
  "command": "/opt/my-tool/bin/handler",
  "args": ["--mcp"],
  "timeout_seconds": 60,
  "input_schema": {
    "type": "object",
    "properties": {
      "target": {"type": "string"}
    },
    "required": ["target"],
    "additionalProperties": false
  }
}
```

The executable receives the `arguments` JSON object on stdin. If stdout contains valid JSON, that value becomes structured tool output; otherwise stdout is returned as text metadata.

Good plugin candidates:

- SSH remote adapter
- Docker/Kubernetes semantic adapters
- Minecraft/Pelican controls
- serial/USB hardware
- GPU-specific management
- company-internal deployment systems

Core should not absorb these unless they become truly universal computer primitives.
