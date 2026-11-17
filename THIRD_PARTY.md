# Third-party notes

CyComAgent-MCP core is independently written and the Full Power Linux core has no third-party Go runtime dependencies.

The project design was informed by public open-source projects and specifications:

- **DesktopCommanderMCP** — MIT. Generic filesystem/terminal/process MCP tool design.
- **Harsh-2002/SSH-MCP** — MIT. Persistent remote/session-manager design and operational lessons.
- **OpenHands** — core runtime MIT (the upstream enterprise directory has separate licensing). Agent/runtime separation.
- **Agent Zero** — MIT. General computer-as-a-tool philosophy.
- **Open Interpreter** — studied only as an architectural reference for general code/shell execution; no source is copied.
- **Model Context Protocol specification** — wire-protocol reference.
- **OpenAI tunnel-client** — Apache-2.0. The Termux ARM64 release bundles a cross-compiled `client-runtime` binary from upstream commit `4d9e440d70563f4a6e3d807dc4b67177b2330fec` so the phone can connect to Secure MCP Tunnel without a separate manual install. Its upstream LICENSE and NOTICE are included under `packaging/termux/third-party/openai-tunnel-client/`. Linux releases may continue to use a separately installed tunnel-client.

Being an architectural influence does not imply endorsement.

## Optional Linux Any App helper

`codex-computer-use-linux` is an external helper from
[ilysenko/codex-desktop-linux](https://github.com/ilysenko/codex-desktop-linux),
MIT, copyright (c) 2025 ilysenko. It is not linked into the Go core or bundled
as a binary by this source update. The KWin adaptation patch contains upstream
context and is distributed with its MIT license in
`packaging/anyapp/LICENSE.codex-desktop-linux`. The exact base commit and build
instructions are in `docs/anyapp-linux.md`. This is a community integration,
not an official OpenAI product or endorsement.
