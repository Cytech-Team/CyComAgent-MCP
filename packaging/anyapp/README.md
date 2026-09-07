# Optional Any App helper patch

Source: https://github.com/ilysenko/codex-desktop-linux

Base commit: `758b4ce74301c387c6006698921384d4ca1dc47d` (also in `UPSTREAM_COMMIT`).

`kwin-wayland-geometry.patch` adds native KWin move/resize, verifies the resulting
geometry, handles Qt rectangle value wrappers, updates helper descriptions,
and adds six Rust regression tests. It is a source patch, not an executable.

The accompanying upstream MIT license applies to upstream context in the patch.
See [the setup guide](../../docs/anyapp-linux.md) for application and build steps.
