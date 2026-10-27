GO ?= go
GOTOOLCHAIN ?= go1.26.10
GOCACHE ?= $(TMPDIR)/cpa-model-dock-go-cache
ifeq ($(strip $(TMPDIR)),)
GOCACHE := /tmp/cpa-model-dock-go-cache
endif
VERSION ?= 0.1.0
PLATFORMS ?= linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64
BASE_URL ?= https://example.invalid/cpa-model-dock/$(VERSION)
PACKAGE_ONLY ?= 0
export GO GOTOOLCHAIN GOCACHE VERSION

.PHONY: test build dist package docker-builder cross-linux cross-windows cross-darwin \
        dist-linux-amd64 dist-linux-arm64 dist-darwin-amd64 dist-darwin-arm64 dist-windows-amd64

test:
	CGO_ENABLED=1 CC=$${CC:-$$(if [ "$$(uname -s)" = Darwin ]; then echo /usr/bin/clang; else echo cc; fi)} $(GO) test ./...

build:
	./scripts/build.sh host

# Serial by default; make -j may run independent targets in parallel.
dist:
	@set -e; for platform in $(PLATFORMS); do ./scripts/build.sh "$$platform"; done

dist-linux-amd64:
	./scripts/build.sh linux/amd64
dist-linux-arm64:
	./scripts/build.sh linux/arm64
dist-darwin-amd64:
	./scripts/build.sh darwin/amd64
dist-darwin-arm64:
	./scripts/build.sh darwin/arm64
dist-windows-amd64:
	./scripts/build.sh windows/amd64

cross-linux: dist-linux-amd64 dist-linux-arm64
cross-darwin: dist-darwin-amd64 dist-darwin-arm64
cross-windows: dist-windows-amd64

docker-builder:
	./scripts/build.sh docker-builder

package:
	@if [ "$(PACKAGE_ONLY)" != 1 ]; then $(MAKE) dist; fi
	python3 scripts/package.py --version "$(VERSION)" --base-url "$(BASE_URL)" --platforms $(PLATFORMS)
