# NEA

NEA is the machine-adaptation layer for CyComAgent.

The rule is simple:

- **Upstream stays upstream.** CyComAgent and third-party projects are fetched from their normal release/tag streams.
- **Machine-specific fixes live in NEA.** Android/Termux, Linux/KWin, and future device-specific compatibility changes are kept outside upstream source trees.
- **Build/install code applies NEA deterministically.** A patch or overlay must be explicit, reviewable, and independently removable when upstream no longer needs it.
- **Auto-upstream remains overridable.** CI and local builds follow the newest compatible upstream by default, while environment variables can pin a known ref for reproducibility.

Current profiles:

| NEA profile | Target | Adaptation |
| --- | --- | --- |
| `termux-arm64/tunnel-client` | OpenAI tunnel-client on Android/Termux ARM64 | Injects the Termux resolver adapter so the pure-Go runtime uses `$PREFIX/etc/resolv.conf` instead of falling back to localhost DNS. |
| `linux-kwin-anyapp` | Codex Desktop Linux helper on KWin/Wayland | Registers the existing native KWin geometry patch and pinned upstream base under the NEA catalog. |

`scripts/nea-apply.sh <profile> <upstream-tree>` applies overlays and `.patch` files for profiles that own source adaptations.

The compatibility copy under `packaging/anyapp/` remains authoritative for the existing Any App build documentation; NEA catalogs it instead of duplicating the patch.
