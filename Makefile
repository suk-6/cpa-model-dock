GOTOOLCHAIN ?= go1.26.10
export GOTOOLCHAIN
export CGO_ENABLED = 1
ifeq ($(shell uname -s),Darwin)
CC = /usr/bin/clang
endif
export CC
GOOS ?= $(shell go env GOOS)
EXT_linux = so
EXT_freebsd = so
EXT_darwin = dylib
EXT_windows = dll
EXT = $(or $(EXT_$(GOOS)),so)
VERSION ?= 0.1.0

.PHONY: test build cross-linux cross-windows cross-darwin

test:
	go test ./...

build:
	mkdir -p dist
	go build -buildmode=c-shared -ldflags '-X main.pluginVersion=$(VERSION)' -o dist/cpa-model-dock.$(EXT) .

cross-linux cross-windows cross-darwin:
	@echo 'Cross build packaging will supply a target C toolchain and GOOS/GOARCH.'
	@exit 1
