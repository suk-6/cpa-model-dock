# Packaging verification — 2026-10-27

Built from source commit `08208c6` in the packaging worktree, using Go 1.26.10
and CPA v8.0.44 SDK. These results do not include concurrent integration fixes.
Docker 29.8.2 / OrbStack used a Linux arm64 daemon and Debian cross compilers.

## Produced artifacts

All files below exist under `dist/` (ignored by Git). ZIP naming is
`cpa-model-dock_0.1.0_<os>_<arch>.zip`. The library is at ZIP root.

| Platform | Library path | Library bytes | ZIP bytes | `file` output |
| --- | --- | ---: | ---: | --- |
| darwin/amd64 | `darwin/amd64/cpa-model-dock.dylib` | 5,062,008 | 2,142,628 | Mach-O 64-bit dynamically linked shared library x86_64 |
| darwin/arm64 | `darwin/arm64/cpa-model-dock.dylib` | 4,688,450 | 1,981,095 | Mach-O 64-bit dynamically linked shared library arm64 |
| linux/amd64 | `linux/amd64/cpa-model-dock.so` | 5,520,496 | 2,220,105 | ELF 64-bit LSB shared object, x86-64, version 1 (SYSV), dynamically linked, BuildID[sha1]=d061419a118348455ca3a563b7082f2bc4b28f4b, stripped |
| linux/arm64 | `linux/arm64/cpa-model-dock.so` | 5,131,408 | 2,018,644 | ELF 64-bit LSB shared object, ARM aarch64, version 1 (SYSV), dynamically linked, BuildID[sha1]=60572e956b1b34159cdc697ef27a60b81718a8f3, stripped |
| windows/amd64 | `windows/amd64/cpa-model-dock.dll` | 5,217,280 | 2,193,018 | PE32+ executable (DLL) (console) x86-64 (stripped to external PDB), for MS Windows |

The host build is also `dist/cpa-model-dock.dylib`, 4,688,450 bytes, Mach-O
arm64. Both macOS architectures were built locally using `/usr/bin/clang`;
both Linux architectures and Windows amd64 were actually cross-built in Docker.
Both Mach-O files have deployment target `minos 12.0` and SDK `27.0` per
`otool -l`; native macOS 12 loading has not been exercised.
`readelf --version-info` reports GLIBC_2.34 as the newest required libc symbol
version for both ELF files. Windows imports KERNEL32.dll and msvcrt.dll.

The following manifest was generated and independently checked against the
archive bytes, ZIP member names and registry sizes. Each ZIP contains exactly
one regular root library and passes its CRC integrity check. `SHA256SUMS` and
`checksums.txt` contain identical data; per-archive `.sha256` files also exist.

```text
38fb3282f69617f73d3c11d7b9301a01ed6c51239c0a2796b8cd84cbdc133bff  cpa-model-dock_0.1.0_darwin_amd64.zip
fd54a3456c0191ee73e227ad373da6dc16e64bdaf78c72b37210ed1dc3ce53e0  cpa-model-dock_0.1.0_darwin_arm64.zip
922f56699c2955b776d83e9ee693815d922780d8c26975edc9367544e4be3add  cpa-model-dock_0.1.0_linux_amd64.zip
eaaf5836bf021ecd7fb509bc398a89800eb17db1b8ca7a95d9326372d2249df0  cpa-model-dock_0.1.0_linux_arm64.zip
ba7854ea475e80a45f6b5a8428750aad3de2f6534e200939bf2a1ad867572292  cpa-model-dock_0.1.0_windows_amd64.zip
```

## Executed checks

- `make build`, both `make dist-darwin-*` targets, both Docker Linux targets,
  and `make dist-windows-amd64`: passed.
- `make package` (full rebuild) and `make package PACKAGE_ONLY=1`: five ZIPs, both checksum manifests, and
  schema-2 `registry.json` generated. The registry uses sample example.invalid
  URLs and has not been published or installed through the store.
- `make test`: passed all Go packages.
- `python3 scripts/abi_smoke.py dist/cpa-model-dock.dylib`: passed on macOS arm64.
- The unchanged ABI smoke harness also passed against `dist/linux/arm64/cpa-model-dock.so`
  inside Docker. Both exercise native init/call/free/shutdown, schemas 6/7,
  management saves, policy denials, and the Alpha denial route with fake callbacks.
- `node --test web/dev/tests/*.test.mjs`: 38 passed, zero failed, with loopback
  permission. The first sandboxed attempt was denied local socket access.

## Four examples on a real CPA

`python3 scripts/validate-examples.py --cpa-bin /tmp/cpa-bin --keep` started
an isolated loopback CPA v8.0.44 and loaded the macOS arm64 plugin. Every
example returned `valid:true` from **POST** `/v0/management/plugins/cpa-model-dock/validate`,
then saved successfully via PUT `/state`. The validator read `state.json` to
confirm persisted revisions 1, 2, 3 and 4. `/explain` reported `hidden_by_alias`,
status 404, for `gpt-6.1-sol` in the aliases-only example. No paid probes or
inference requests were sent.

The synthetic configured models were registered but the plugin catalog
reported `credential_count:0`, `routable:false` for all eight. Public previews
were therefore empty; the automatic alias dry-run kept its target with
`no_routable_candidates`. This establishes draft validity and persistence,
**not successful alias routing or automatic upgrades**. The concurrent
integration work must resolve/verify runtime catalog behavior before release.

Local disposable evidence (not portable repository links):

- `/tmp/cpa-model-dock-cross-build.log`
- `/tmp/cpa-model-dock-docker-build.log`
- `/tmp/cpa-model-dock-node-tests.log`
- `/var/folders/3h/24bs7fpd58338bs7dg__b5rh0000gn/T/cpa-model-dock-examples-4bu2vf1v/results.json`

## Remaining integration evidence

No full real-CPA inference harness result is claimed here. amd64 macOS,
amd64 Linux and Windows binaries were inspected but not loaded on their target
architectures/OS. Reverse-proxy snippets are source-informed deployment examples;
they have not been exercised against nginx or Caddy in this worktree. No store
download/install round trip or production credentials were tested.

<!-- E2E: plugin registration, menu and UI browser workflow on real CPA -->
<!-- E2E: pinned alias JSON/SSE, response rewrite and original hiding -->
<!-- E2E: stable numeric/date auto-upgrade, no downgrade, ambiguous candidates -->
<!-- E2E: family whitelist and alias-only inference across supported protocols -->
<!-- E2E: revision conflict, restart persistence and history restore -->
<!-- E2E: realtime/live and WS continuation observations; no universal enforcement claim -->
