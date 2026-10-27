# Verification evidence — 2026-10-27

## v0.1.2 validation

Validated the v0.1.2 changes with Go 1.26.10 and the same CPA v8.0.44
`/tmp/cpa-bin` described below:

- `CC=/usr/bin/clang CGO_ENABLED=1 GOTOOLCHAIN=go1.26.10 GOFLAGS=-mod=mod
  GOPROXY=off go test -race ./...`: 159 top-level tests and 163 subtests passed
  across nine packages; no race failures.
- `go vet ./...`: passed with the same Go/cgo environment.
- `node --test web/dev/tests/*.test.mjs`: 86 passed, none failed or skipped.
- `go test -C e2e ./...`: passed.
- Rebuilt `dist/cpa-model-dock.dylib` using `go build -buildmode=c-shared`, then
  `CPA_BIN=/tmp/cpa-bin CPA_MODEL_DOCK_LIB="$PWD/dist/cpa-model-dock.dylib"
  ./e2e/run.sh`: **31 passed, zero failed/skipped**. The new native Codex API-key
  fixture proves delimiter-free scanner-line Responses rewriting. Restart checks
  verify alias routing/listing within one second of management readiness and
  retained evaluation status. Restore-and-pin changes target/mode in one revision
  and survives a subsequent automatic check. Partial model removal still passes.
  [Full fake-upstream E2E transcript](evidence/cpa-v8.0.44-v0.1.2-e2e-2026-10-27.txt).
- `make package`: rebuilt Linux amd64/arm64 and Windows amd64 through Docker,
  Darwin amd64/arm64 through Apple clang. Five v0.1.2 ZIPs, checksum manifests
  and a schema-2 registry were generated under `dist/` and verified against bytes,
  sizes, ZIP root member names and CRCs. Nothing was published.
- `python3 scripts/abi_smoke.py` passed against both the host dylib and packaged
  Darwin arm64 dylib. The smoke fixture now matches the existing five resources
  and required full settings object.

The streaming unit fixture is the supplied redacted Codex capture. No real OAuth
credentials were used by the new E2E scenarios. Unit tests also cover bootstrap
retry timing/cancellation, catalog cache sanitization and corruption, empty versus
partial inventory replacement, alias-status fingerprint invalidation and restore
history localization.

## Historical v0.1.0 verification

The historical packaging artifacts below were built from source commit `08208c6` in the packaging worktree, using Go 1.26.10
and CPA v8.0.44 SDK. These artifact hashes describe that earlier build, not a rebuild of the final integration code. The final E2E and unit results below have separate scope.
Docker 29.8.2 / OrbStack used a Linux arm64 daemon and Debian cross compilers.
`go version -m /tmp/cpa-bin` confirms `CGO_ENABLED=1`, `darwin/arm64`,
`vcs.revision=1f917e77c4562623aa49e9804d999524151a2f96` (CPA v8.0.44),
and `vcs.modified=false`.

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
- Earlier packaging check: `make test` passed all Go packages.
- `python3 scripts/abi_smoke.py dist/cpa-model-dock.dylib`: passed on macOS arm64.
- The unchanged ABI smoke harness also passed against `dist/linux/arm64/cpa-model-dock.so`
  inside Docker. Both exercise native init/call/free/shutdown, schemas 6/7,
  management saves, policy denials, and the Alpha denial route with fake callbacks.
- Earlier packaging JS check: 38 passed, zero failed, with loopback permission. The first sandboxed attempt was denied local socket access. Superseded unit counts are recorded below.

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
**not successful alias routing or automatic upgrades**. The later full E2E run below verified routing and upgrades with configured compatible credentials; this earlier management-only result is retained as historical evidence.

Local disposable evidence (not portable repository links):

- `/tmp/cpa-model-dock-cross-build.log`
- `/tmp/cpa-model-dock-docker-build.log`
- `/tmp/cpa-model-dock-node-tests.log`
- `/var/folders/3h/24bs7fpd58338bs7dg__b5rh0000gn/T/cpa-model-dock-examples-4bu2vf1v/results.json`

## Final real-CPA harness (fake upstream)

On **2026-10-27**, the final complete run on **CPA v8.0.44** finished
**29 PASS / 0 FAIL / 0 SKIP**. Read the [full retained log](evidence/cpa-v8.0.44-e2e-2026-10-27.txt),
including both `OBSERVATION` rows. Original local log:
`/tmp/cpa-model-dock-e2e-final.txt`; retained fixture:
`/var/folders/3h/24bs7fpd58338bs7dg__b5rh0000gn/T/cpa-model-dock-e2e.86eEp9`.
The host is real CPA; the upstream is a loopback **fake OpenAI-compatible server**
with a request recorder. These are not live OpenAI/Codex/Claude/Gemini provider tests.
See [harness invocation and fixture behavior](../e2e/README.md); for plugin mode use:

```sh
CPA_BIN=/tmp/cpa-bin CPA_MODEL_DOCK_LIB="$PWD/dist/cpa-model-dock.dylib" E2E_KEEP_TMP=1 ./e2e/run.sh
```

This is a reproduction command, not a claim that the historical packaging binary
matches the final tested integration binary. Rebuild the current library first.

| Evidence area | Verified on real CPA with fake upstream |
| --- | --- |
| Host/auth baseline | Isolated loopback listeners, management authenticated; missing client key and inference key on management rejected; eight fake models, credential prefix and native CPA alias advertised. |
| Baseline routing | Direct, prefixed, native-mapped, thinking-suffixed and unrelated models reached the expected recorded upstream model. Chat SSE included two JSON chunks, model fields, fake text and `[DONE]`. |
| Protocol baseline | Responses translated to fake `/v1/chat/completions`; Claude `/v1/messages` and Gemini `/gemini` translated to the compatible upstream. This does not exercise real provider credentials. |
| Registration and UI resources | Loaded/effective plugin, Model Dock menu, public HTML and all four public assets with strict CSP; authenticated overview/state/catalog/history/validate/preview/explain/refresh/check succeeded. Read-only calls and unchanged applying checks left revision 11 unchanged. |
| Pinned aliases / rewrite | JSON/SSE reached the pinned target; global model rewriting off/on and alias override checked. |
| Alias-only policy | `/v1/models` exactly `[gpt-sol]`; alias JSON/SSE still routed. Hidden direct/thinking names returned 404; prefixed/native/unrelated names returned 403, all with zero upstream calls. Claude notation (including the Fable fixture) and Gemini notation denied with zero upstream calls. |
| Automatic upgrade | Numeric `6.10` beat `6.9`; mini and preview excluded; JSON/SSE reached `6.10`. |
| Removed target | Removed from fake server and both CPA credential configurations; mapping retained, alias returned 503 with zero upstream calls, then fixture restored. |
| Revision / atomic validation | Stale PUT returned 409 `revision_conflict` while old policy remained enforced; invalid mode and invalid whitelist-disable combination rejected atomically without changing revision/state. |
| Restart | Revision 9 and all nine history IDs restored; whitelist and alias continued to enforce. This checks restart recovery, not the history-restore API. |
| Alpha | Unrestricted baseline passed routing but hit host `auth_not_found` without Codex credentials. Under protected policy both `/v1/alpha/search` and `/backend-api/codex/alpha/search` returned HTTP 503 via the static denial executor; zero upstream calls. Published Dock aliases and exact CPA-native/prefixed/raw allows were also denied on both paths. |

The Alpha rule is conditional, not a universal shutdown: with the guard enabled,
whitelist rules or alias target hiding requiring final-model authorization cause
Alpha denial because CPA exposes no mapping provenance. Dock aliases are always
denied on Alpha even with the guard off. An unrestricted default policy passes
native Alpha through. Disabling the guard removes the conservative destination
gate but does not enable safe Dock alias forwarding. See [contract §6](design/contract.md#6-guard--uncovered-endpoints).

### Real-CPA observations of gaps

- **Realtime/live is not protected by this guard with valid built-in keys.**
  GET `/v1/realtime` (upgrade probe), POST `/v1/realtime` and POST `/v1/live`
  each reported `guard_denied=false`; whitelist off/on both returned native
  503 `auth_not_found`. Built-in key auth succeeds before plugin auth; this is
  not a plugin policy denial. The fixture had no Codex credentials, so these
  probes establish neither successful realtime inference nor all subroute behavior.
  Mandatory reverse-proxy restrictions must cover `/v1/realtime*` and `/v1/live*`.
- **Gemini hidden metadata leaks:** GET `/v1beta/models/gpt-6.1-sol` returned
  HTTP 200 including `displayName` and `name: models/gpt-6.1-sol` despite hiding.
  Block/restrict detail GET at the proxy. The PASS row records a known gap,
  not successful enforcement.

### Browser evidence on real CPA

Separately from the 29-row HTTP harness, headless Chrome rendered all four tabs
(Overview, Aliases, Whitelist, History) in EN and KO against real CPA, with strict
CSP and no observed CSP violations. The retained driver `/tmp/uicheck/shot.mjs`
uses CDP, the direct plugin resource URL, a test management-key form, tab clicks,
screenshots and Runtime/Log collection; screenshots were retained in `/tmp/uicheck/`.
This verifies direct-page rendering with the test key, not every edit/save workflow.
The real management-center iframe was verified only for resource loading, not a
real browser session login flow or end-to-end key replacement/conflict workflow.

## Unit-only coverage (at time of writing: 2026-10-27)

The retained race-enabled Go run passed **235 tests/subtests**, zero failures
(`/tmp/cpa-model-dock-go-race.json`); JS passed **46 tests**, zero failed/skipped
(`/tmp/cpa-model-dock-js-tests.txt`, `node --test web/dev/tests/*.test.mjs`).
Counts may change with concurrent implementation work. These are not additional
real-CPA inference scenarios.

Date ordering, ambiguity retention, no-downgrade edge cases, the decision rules used by the three-family
Sol/Astra/Opus policy, and history-restore API semantics have unit coverage only
for claims beyond the harness table. See [version tests](../internal/version/version_test.go),
[auto-update tests](../internal/autoupdate/evaluate_test.go), policy/management tests,
and [JS mock/API tests](../web/dev/tests/mock-api.test.mjs).
Key decoding/storage logic and language-key parity are covered by
[UI logic tests](../web/dev/tests/ui-logic.test.mjs); this does not prove a real
management-center login, key replacement or browser conflict flow.

## Source-verified only

- Responses WS continuation frames do not rerun ModelRouter/BeforeAuth/AfterAuth.
  Create/append explicit model changes require replay; steer uses the bound model
  for authorization while fields pass through finalization/upstream validation.
  No live continuation-frame test was run. Paths cannot filter individual frames
  after upgrade; strict deployments block GET `/v1/responses` and
  `/backend-api/codex/responses` at the proxy.
- Management `/v0/management/api-call` and `/v8/management/requests/api-call`
  bypass the model-hook pipeline; no real-CPA bypass request is claimed here.
- Live Codex OAuth forwarding behavior was read in source, not exercised with real
  provider credentials. Alpha baseline `auth_not_found` is not successful OAuth forwarding.
- Video binary transfer and final payload-rule boundaries are source findings,
  not real-provider end-to-end tests. See [source references and rules](design/contract.md#6-guard--uncovered-endpoints)
  and the [README boundaries](../README.md#known-limitations-enforcement-boundaries-and-privacy).

## Unverified deployment behavior

nginx/Caddy examples are checked against official routing/matcher documentation,
not executed proxy integrations. No installed nginx/Caddy binary was available
for syntax/runtime validation in this worktree. Gateway 403s with valid keys,
admin-network restrictions, POST generation preservation and preventing direct
CPA access still require deployment tests; the real-CPA harness ran without these proxies.
No store download/install round trip, production credentials, real management-center
browser login flow or live Responses WS continuation session was tested.
amd64 macOS, amd64 Linux and Windows binaries were inspected but not loaded on their
target architectures/OS; the older packaging smoke scope remains as recorded above.
