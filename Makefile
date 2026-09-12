BINARY := cycomagent
VERSION ?= 0.4.7-session-routing-dev
GO ?= go
LDFLAGS := -s -w -X main.version=$(VERSION)
STAGE := .release/CyComAgent-MCP
TERMUX_STAGE := .release-termux/CyComAgent-MCP
MACOS_STAGE := .release-macos/CyComAgent-MCP
RELEASE_NOTES := RELEASE_NOTES_v$(VERSION).md

.PHONY: all test vet build build-termux build-termux-tunnel build-macos clean release release-termux release-windows release-macos release-all
all: test build

test:
	GOTOOLCHAIN=local $(GO) test ./...

vet:
	GOTOOLCHAIN=local $(GO) vet ./...

build:
	mkdir -p dist
	CGO_ENABLED=0 GOTOOLCHAIN=local $(GO) build -trimpath -ldflags='$(LDFLAGS)' -o dist/cycomagent ./cmd/cycomagent
	CGO_ENABLED=0 GOTOOLCHAIN=local $(GO) build -trimpath -ldflags='$(LDFLAGS)' -o dist/cycomagent-root ./cmd/cycomagent-root

build-termux:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=android GOARCH=arm64 GOTOOLCHAIN=local $(GO) build -trimpath -ldflags='$(LDFLAGS)' -o dist/cycomagent-termux-arm64 ./cmd/cycomagent

build-macos:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 GOTOOLCHAIN=local $(GO) build -trimpath -ldflags='$(LDFLAGS)' -o dist/cycomagent-macos-arm64 ./cmd/cycomagent
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 GOTOOLCHAIN=local $(GO) build -trimpath -ldflags='$(LDFLAGS)' -o dist/cycomagent-macos-amd64 ./cmd/cycomagent

build-termux-tunnel:
	./scripts/build-termux-tunnel.sh

release: test vet build
	rm -rf .release
	mkdir -p $(STAGE)/dist
	cp dist/cycomagent dist/cycomagent-root $(STAGE)/dist/
	cp -a scripts packaging docs examples $(STAGE)/
	cp README.md CHANGELOG.md LICENSE SECURITY.md THIRD_PARTY.md ROADMAP.md $(RELEASE_NOTES) $(STAGE)/
	tar -C .release -czf dist/CyComAgent-MCP-v$(VERSION)-linux-amd64.tar.gz CyComAgent-MCP
	sha256sum dist/cycomagent dist/cycomagent-root dist/CyComAgent-MCP-v$(VERSION)-linux-amd64.tar.gz > dist/SHA256SUMS
	sha256sum dist/CyComAgent-MCP-v$(VERSION)-linux-amd64.tar.gz | sed 's#  dist/#  #' > dist/SHA256SUMS-linux
	rm -rf .release

release-termux: test vet build-termux build-termux-tunnel
	rm -rf .release-termux
	mkdir -p $(TERMUX_STAGE)/dist $(TERMUX_STAGE)/scripts $(TERMUX_STAGE)/packaging
	cp dist/cycomagent-termux-arm64 dist/tunnel-client-runtime-termux-arm64 dist/TUNNEL_CLIENT_COMMIT $(TERMUX_STAGE)/dist/
	cp scripts/install-termux.sh scripts/configure-termux.sh scripts/install-termux-boot.sh scripts/smoke-termux.sh $(TERMUX_STAGE)/scripts/
	cp -a packaging/termux $(TERMUX_STAGE)/packaging/
	cp README.md LICENSE SECURITY.md CHANGELOG.md THIRD_PARTY.md $(RELEASE_NOTES) $(TERMUX_STAGE)/
	tar -C .release-termux -czf dist/CyComAgent-MCP-v$(VERSION)-termux-arm64.tar.gz CyComAgent-MCP
	sha256sum dist/cycomagent-termux-arm64 dist/tunnel-client-runtime-termux-arm64 dist/CyComAgent-MCP-v$(VERSION)-termux-arm64.tar.gz > dist/SHA256SUMS-termux
	sha256sum dist/CyComAgent-MCP-v$(VERSION)-termux-arm64.tar.gz | sed 's#  dist/#  #' > dist/SHA256SUMS-termux-release
	rm -rf .release-termux

release-windows:
	rm -rf .release-windows
	mkdir -p .release-windows/CyComAgent-MCP/platform
	cp -a platform/windows .release-windows/CyComAgent-MCP/platform/
	cp README.md CHANGELOG.md LICENSE SECURITY.md RELEASE_NOTES_v$(VERSION).md .release-windows/CyComAgent-MCP/
	mkdir -p dist
	cd .release-windows && zip -qr ../dist/CyComAgent-MCP-v$(VERSION)-windows.zip CyComAgent-MCP
	sha256sum dist/CyComAgent-MCP-v$(VERSION)-windows.zip | sed 's#  dist/#  #' > dist/SHA256SUMS-windows
	rm -rf .release-windows

release-macos: test vet build-macos
	rm -rf .release-macos
	mkdir -p $(MACOS_STAGE)/dist $(MACOS_STAGE)/scripts
	cp scripts/install-launchd.sh $(MACOS_STAGE)/scripts/
	cp README.md CHANGELOG.md LICENSE SECURITY.md THIRD_PARTY.md ROADMAP.md $(RELEASE_NOTES) $(MACOS_STAGE)/
	for arch in arm64 amd64; do \
		cp dist/cycomagent-macos-$$arch $(MACOS_STAGE)/dist/cycomagent; \
		tar -C .release-macos -czf dist/CyComAgent-MCP-v$(VERSION)-macos-$$arch.tar.gz CyComAgent-MCP; \
	done
	sha256sum dist/CyComAgent-MCP-v$(VERSION)-macos-arm64.tar.gz dist/CyComAgent-MCP-v$(VERSION)-macos-amd64.tar.gz | sed 's#  dist/#  #' > dist/SHA256SUMS-macos
	rm -rf .release-macos

release-all: release release-termux release-windows release-macos

clean:
	rm -rf dist .release .release-termux .release-windows .release-macos
