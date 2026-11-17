# v0.4.8-isolated-desktop-dev

Development release notes for the unified CyComAgent source tree.

- Consolidates Linux, Android/Termux, macOS and Windows bridge packaging behind one source repository and release workflow.
- Adds a `cycom` command for host detection, local platform builds, test/vet and unified release packaging.
- Adds a GitHub Actions workflow that builds the supported release artifacts and publishes them when a `v*` tag is pushed.
- Keeps platform-specific service managers and permission boundaries separate from the shared Core.

This development release does not claim native Android APK or iOS application support. Windows remains the PowerShell-native bridge package; macOS remains experimental.
