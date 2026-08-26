# Design merge map

CyComAgent-MCP is an independently written implementation informed by several existing projects.

| Project | Idea studied | CyComAgent direction |
| --- | --- | --- |
| DesktopCommanderMCP | generic terminal/files/process primitives | keep primitive set small and composable |
| Harsh-2002/SSH-MCP | persistent remote access and reconnect concerns | SSH becomes an optional adapter, not local-runtime identity |
| OpenHands | separation of agent reasoning from execution runtime | runtime contains no LLM/agent brain |
| Agent Zero | operating system as a general-purpose tool | preserve universal shell escape hatch |
| Open Interpreter | code/shell unlock general computer capabilities | avoid hard-coded application workflows |
| MCP 2026-07-28 | stateless/sessionless protocol model | explicit handles and disk-backed state |

No source files from these projects are vendored in the CyComAgent core.
