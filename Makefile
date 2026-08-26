BINARY := cycomagent
VERSION ?= 0.4.2-termux-dev
GO ?= go
LDFLAGS := -s -w -X main.version=$(VERSION)
STAGE := .release/CyComAgent-MCP
TERMUX_STAGE := .release-termux/CyComAgent-MCP

.PHONY: all test vet build build-termux build-termux-tunnel clean release release-termux
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

build-termux-tunnel:
	./scripts/build-termux-tunnel.sh

release: test vet build
	rm -rf .release
	mkdir -p $(STAGE)/dist
	cp dist/cycomagent dist/cycomagent-root $(STAGE)/dist/
	cp -a scripts packaging docs examples $(STAGE)/
	cp README.md CHANGELOG.md LICENSE SECURITY.md THIRD_PARTY.md ROADMAP.md RELEASE_NOTES_v0.3.0-fullpower-dev.md $(STAGE)/
	tar -C .release -czf dist/CyComAgent-MCP-v$(VERSION)-linux-amd64.tar.gz CyComAgent-MCP
	sha256sum dist/cycomagent dist/cycomagent-root dist/CyComAgent-MCP-v$(VERSION)-linux-amd64.tar.gz > dist/SHA256SUMS
	rm -rf .release

release-termux: test vet build-termux build-termux-tunnel
	rm -rf .release-termux
	mkdir -p $(TERMUX_STAGE)/dist $(TERMUX_STAGE)/scripts $(TERMUX_STAGE)/packaging
	cp dist/cycomagent-termux-arm64 dist/tunnel-client-runtime-termux-arm64 dist/TUNNEL_CLIENT_COMMIT $(TERMUX_STAGE)/dist/
	cp scripts/install-termux.sh scripts/configure-termux.sh scripts/install-termux-boot.sh scripts/smoke-termux.sh $(TERMUX_STAGE)/scripts/
	cp -a packaging/termux $(TERMUX_STAGE)/packaging/
	cp README.md LICENSE SECURITY.md CHANGELOG.md THIRD_PARTY.md RELEASE_NOTES_v0.4.2-termux-dev.md $(TERMUX_STAGE)/
	tar -C .release-termux -czf dist/CyComAgent-MCP-v$(VERSION)-termux-arm64.tar.gz CyComAgent-MCP
	sha256sum dist/cycomagent-termux-arm64 dist/tunnel-client-runtime-termux-arm64 dist/CyComAgent-MCP-v$(VERSION)-termux-arm64.tar.gz > dist/SHA256SUMS-termux
	rm -rf .release-termux

clean:
	rm -rf dist .release .release-termux
