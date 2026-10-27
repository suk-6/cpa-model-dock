# Building and packaging

Use Go **1.26.10**, a C compiler, Make, and Python 3. The module imports CPA
v8.0.44's official SDK. cgo is mandatory: these are native `-buildmode=c-shared`
libraries, with entry point `cliproxy_plugin_init`, not Go `plugin.Open` modules.
The module/repository owner `cpa-model-dock` is a placeholder namespace.

```sh
make test
make build                         # dist/cpa-model-dock.<host extension>
python3 scripts/abi_smoke.py dist/cpa-model-dock.dylib  # macOS host
make dist                          # all five targets; macOS host required
make package                       # rebuild all selected targets, then package
```

On macOS, install Apple Command Line Tools (`xcode-select --install`). Both
`darwin/amd64` and `darwin/arm64` build locally using `/usr/bin/clang` with the
matching `-arch x86_64` / `-arch arm64` in `CGO_CFLAGS` and `CGO_LDFLAGS`.
The default `MACOSX_DEPLOYMENT_TARGET=12.0` matches the Go 1.26 runtime floor
([Go release notes](https://go.dev/doc/go1.26#darwin)); override it explicitly
to target a newer system. Native loading on macOS 12 has not been tested.
Homebrew clang may not find the Apple SDK. Cross-building macOS from Linux is
not implemented: run the macOS targets on a Mac. A successful build of the
other architecture does not prove it runs on that architecture.

Linux and Windows distribution targets use a Linux Docker daemon. On a Mac,
Docker Desktop or OrbStack supplies it. The builder extends the official
`golang:1.26.10-bookworm` Debian image with these C compilers:

| Target | Make target | C compiler | Output below `dist/` |
| --- | --- | --- | --- |
| linux/amd64 | `dist-linux-amd64` | `x86_64-linux-gnu-gcc` in Docker | `linux/amd64/cpa-model-dock.so` |
| linux/arm64 | `dist-linux-arm64` | `aarch64-linux-gnu-gcc` in Docker | `linux/arm64/cpa-model-dock.so` |
| darwin/amd64 | `dist-darwin-amd64` | Apple clang, `-arch x86_64` | `darwin/amd64/cpa-model-dock.dylib` |
| darwin/arm64 | `dist-darwin-arm64` | Apple clang, `-arch arm64` | `darwin/arm64/cpa-model-dock.dylib` |
| windows/amd64 | `dist-windows-amd64` | `x86_64-w64-mingw32-gcc` in Docker | `windows/amd64/cpa-model-dock.dll` |

The Docker builder itself runs on the daemon's default Linux architecture;
target compilers perform the cross compilation without target emulation.
Source is mounted read-only, the selected output directory read/write, and
Go caches live in the Docker volume `cpa-model-dock-go-cache`. Containers run
with the invoking user's UID/GID. The initial image build needs registry and
Debian package access; the first plugin build also downloads Go modules.

```sh
make docker-builder                 # explicitly create/refresh the builder
make dist-linux-amd64
make cross-linux                    # both Linux architectures
make cross-windows
make cross-darwin
# A Linux host can build its subset, then receive the macOS outputs from a Mac.
make dist PLATFORMS='linux/amd64 linux/arm64 windows/amd64'
```

The [official Docker Go listing](https://hub.docker.com/_/golang) lists Go
1.26.10 tags for bookworm, trixie, Alpine 3.23/3.24, and Windows variants.
We verified the bookworm manifest on 2026-10-27, including linux/amd64 and
linux/arm64. Its multi-platform index digest was
`sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af`.
To pin the same base rather than the mutable tag:

```sh
GO_IMAGE='golang:1.26.10-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af' make docker-builder
```

`GO_IMAGE` and `DOCKER_BUILDER_IMAGE` override the base/tag; rebuild the builder
explicitly after changing the base. Debian package repositories are not snapshot
pinned, so matching the Go image alone does not guarantee byte-identical builds.
Linux outputs from this builder require **glibc >=2.34**, observed with
`readelf --version-info`; they are not musl/Alpine binaries. Use a glibc CPA
deployment or supply/retest a musl toolchain. Windows dependency inspection
reported `KERNEL32.dll` and `msvcrt.dll`; native Windows loading remains untested.
See [the recorded verification](verification.md) for actual artifacts and limits.

## Release files

```sh
# Repackage already built libraries, including when collecting separate CI jobs:
make package PACKAGE_ONLY=1 VERSION=0.1.0 \
  BASE_URL=https://plugins.example.com/cpa-model-dock/0.1.0
# A subset registry contains only the explicitly selected platforms:
make package PACKAGE_ONLY=1 PLATFORMS='darwin/arm64'
```

Use the same `VERSION` when building and packaging. `PACKAGE_ONLY=1` does not
change the version embedded in existing binaries. Outputs are:

- `cpa-model-dock_<version>_<goos>_<goarch>.zip`, each containing exactly
  `cpa-model-dock.so`, `cpa-model-dock.dylib`, or `cpa-model-dock.dll` at ZIP root.
- One `<archive>.sha256` per archive, `SHA256SUMS`, and identical
  `checksums.txt` (the latter filename is required by CPA's GitHub release store).
- `registry.json`, a schema-2 direct registry with the actual ZIP SHA-256 and
  byte size for every selected platform. Its default `example.invalid` URLs
  are intentionally non-publishing samples; set `BASE_URL` before hosting.

The generated C header is removed with the temporary build directory. No
runtime sidecar manifest or separate UI deployment is needed; the four static
Web UI resources are embedded at build time. Never ship placeholder/missing UI
assets. Packaging preserves executable library permissions and fixes ZIP
timestamps, so repackaging identical libraries with the same Python/zlib is
deterministic. Manifests describe the selected set only; do not upload stale
ZIPs from an earlier version with a new `checksums.txt`.

CPA source contracts (v8.0.44): [discovery and extensions](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginhost/platform.go#L98),
[ZIP root validation](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginstore/install.go#L319),
[registry schema and artifacts](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginstore/registry.go#L34),
[GitHub checksum download](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginstore/install.go#L125).
Naming and c-shared flags follow the official
[JS Handler release example](https://github.com/router-for-me/cpa-plugin-jshandler/blob/main/.github/workflows/build.yml).

## Development checks

```sh
GOTOOLCHAIN=go1.26.10 CC=/usr/bin/clang make test
node --test web/dev/tests/*.test.mjs
CPA_BIN=/tmp/cpa-bin CPA_MODEL_DOCK_LIB="$PWD/dist/cpa-model-dock.dylib" ./e2e/run.sh
python3 scripts/validate-examples.py --cpa-bin /tmp/cpa-bin --keep
```

The example runner starts a private real CPA with synthetic configured models,
checks `POST /validate`, saves each draft with `PUT /state`, and reads the
persisted revisions. It does not execute inference or paid health probes. The
full harness covers inference; its results are tracked separately by the
integration work. Both runners and the Node mock tests need loopback listeners;
a restricted execution sandbox must allow them. No `node_modules` installation
or dependency-source inspection is needed.

For cached Go modules without network, use `GOPROXY=off make test` or
`GOPROXY=off make build`; the Makefile relocates `GOCACHE` to the temporary
directory. The Docker cache is independent of the host module cache. Do not
set `GOPROXY=off` on a fresh Docker volume unless it has been prepopulated.
