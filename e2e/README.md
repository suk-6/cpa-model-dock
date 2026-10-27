Run from the repository root:

```sh
# Build CPA and verify routing without a plugin binary.
E2E_NO_PLUGIN=1 ./e2e/run.sh

# Run the contract scenarios once the native plugin is available.
CPA_MODEL_DOCK_LIB="$PWD/dist/cpa-model-dock.dylib" ./e2e/run.sh

# Reuse an existing CPA binary; it must have been built with cgo enabled.
CPA_BIN=/absolute/path/to/cpa E2E_NO_PLUGIN=1 ./e2e/run.sh

# Retain the config, CPA log, state/history and machine-readable results.
E2E_KEEP_TMP=1 E2E_NO_PLUGIN=1 ./e2e/run.sh
```

The runner builds CPA with `CC=/usr/bin/clang CGO_ENABLED=1 GOTOOLCHAIN=go1.26.10
go build -o <temp>/cpa ./cmd/server`. It then builds this independent, standard-library-only
Go program. Neither the plugin module nor CPA's source files are edited.
No dependency sources under `node_modules` are accessed.

| Variable | Default / behavior |
| --- | --- |
| `CPA_SOURCE` | `/Users/woosuk/git-files/cpa-model-dock/.ref/CLIProxyAPI` |
| `CPA_BIN` | Build CPA from `CPA_SOURCE`; set to an executable to skip that build |
| `CPA_MODEL_DOCK_LIB` | `../dist/cpa-model-dock.dylib`, resolved **relative to the repository root**, as requested; use an explicit path for a repo-local `dist/` or Linux `.so` |
| `E2E_NO_PLUGIN` | `1` skips plugin loading and reports every plugin scenario as SKIP |
| `E2E_KEEP_TMP` | `1` retains artifacts on success; failures always retain them |
| `GOPROXY` | `off`; dependencies and Go 1.26.10 must already be cached for offline execution |
| `GOFLAGS` | `-mod=readonly`; may explicitly use `GOFLAGS=-mod=mod GOPROXY=off` if needed |
| `GOCACHE` | `${TMPDIR:-/tmp}/cpa-model-dock-go-build`, reusable and writable in this sandbox |

CPA and the fake upstream listen only on random loopback ports. CPA's port is chosen
by binding port zero and releasing it immediately before launch; a concurrent process
can theoretically claim it. Readiness has a 25-second deadline and HTTP calls time out
in 15 seconds. The fake upstream remains alive across CPA restarts. The shell traps
exit/signals, and the Go runner terminates CPA (TERM, then KILL after five seconds)
and closes the upstream. The Go build cache is intentionally reusable.

Each run prints a PASS/FAIL/SKIP table and exits nonzero on FAIL. Retained artifacts
include `results.json`, `cpa.log`, `config.yaml`, `plugins/`, `auth/`, and `state/`.
Failures print the last 60 CPA log lines. The generated JSON config is valid YAML;
CPA may rewrite it as YAML when hashing the plaintext management secret.

The no-plugin mode checks all eight catalog entries, client/management authentication,
direct/prefixed/native-alias/thinking-suffix routing, the unrelated model, chat JSON,
complete SSE chunks and model fields, Responses translation, Claude and Gemini
translation, configured-model removal/reload, and CPA restart with the same upstream
recorder. In this CPA checkout Responses translates to `/v1/chat/completions`; the fake
also implements `/v1/responses` JSON/SSE for hosts that use it. The upstream records
all inference attempts, including attempts at a removed model. Negative policy checks
require both an allowed denial status/code and **no new upstream call**.

Plugin mode follows [`docs/design/contract.md`](../docs/design/contract.md). It checks
registration/menu/static HTML, pinned aliases and response rewriting (JSON and SSE,
global off/on and an alias override), alias-only listing/whitelisting, direct and
normalized-name bypasses, protocol bypasses, numeric auto-update selection, retained
missing targets with 503, stale revisions, atomic validation, and state/history restoration.
Dependent scenarios become SKIP when an earlier prerequisite fails; the prerequisite
remains FAIL. Protocol bypasses become SKIP only if baseline CPA cannot route that
protocol before any policy exists. Both protocols routed on v8.0.44 during development.

The two rows marked `OBSERVATION` report endpoint coverage without making an enforcement
claim. Realtime/live probes compare whitelist-off responses with whitelist-on responses
using a valid client key. Only a policy-coded 401/403 or a new 401/403 relative to
baseline counts as `guard_denied=true`; a native 400/503 does not. Probes cover realtime
GET (WebSocket upgrade), realtime POST, and live POST. They do not establish coverage
of continuation frames, SDP variations, or every subroute. `hidden_detail_leaks=true`
records a successful Gemini detail response including the hidden model ID; this known
host gap does not fail the run. Read these observations before making a security claim.

Plugin-dependent checks have not been executed: no library was available. The harness
asserts the binding contract, so API-shape, status-code, resource/menu, generated-ID,
or response-rewrite differences will surface as failures on first integration.
POST actions include the current `revision` in addition to operation-specific fields,
following the contract's general mutation rule. With only an alias publication rule,
removing its target may make the public model list empty until routability is restored.

CPA's OpenAI-compatible catalog is **configured**, not discovered remotely. The removal
scenario updates the fake `/v1/models` list **and both provider configs**, waits for the
runtime `/v0/management/openai-compatibility` view to change, and refreshes the plugin
catalog until the removed target is absent or unroutable. A remote-list-only removal
would not exercise this contract. `/v8/management/config` reads the on-disk file, so it
is unsuitable as a runtime reload barrier.

Source references below are in CPA v8.0.44 at `$CPA_SOURCE`. Every fixture configuration
field was checked against both the example and `internal/config`.

| Verified fact | Example / config declaration or normalization | Runtime evidence |
| --- | --- | --- |
| v8 layout and `config-version: 8` | `config.example.yaml:10`; `internal/config/config_v8.go:358` | `internal/config/config_v8.go:363` |
| `server.host`, `server.port` | `config.example.yaml:24`; `internal/config/config.go:24`; `internal/config/config_v8.go:76` | loopback readiness |
| Client keys are `access.api-keys`; upstream keys are separate | `config.example.yaml:118`; `internal/config/sdk_config.go:100`; `internal/config/config_v8.go:79` | missing key and client key on management yield 401 |
| `management.allow-remote`, `secret-key`, `disable-control-panel`, `disable-auto-update-panel` | `config.example.yaml:93`; `internal/config/config_types.go:425`; `internal/config/config_v8.go:79` | Bearer auth: `internal/api/handlers/management/handler.go:333`; hashing: `internal/config/config_load.go:243`; no asset download: `internal/managementasset/updater.go:115` |
| `oauth.auth-dir` isolates credentials | `config.example.yaml:946`; `internal/config/config.go:58`; `internal/config/config_v8.go:89` | fresh empty directory |
| `routing.force-model-prefix`, `retry.request-retry`, `retry.max-retry-interval` | `config.example.yaml:197`, `:210`, `:221`; `internal/config/sdk_config.go:88`; `internal/config/config.go:107`; `internal/config/config_v8.go:81` | prefix works; retries/waits explicitly zero |
| `api-keys.openai-compatibility`: `name`, `prefix`, `base-url`, `request-retry`, `keys[].api-key`, `keys[].proxy-url`, `models[].name`, `models[].alias` | `config.example.yaml:891` through `:921`; `internal/config/config_types.go:963`, `:1005`, `:1019`; `internal/config/config_v8.go:406` converts `keys` to `api-key-entries` | native `cpa-native-sol` maps to `gpt-6.1-sol`; `proxy-url: direct` bypasses ambient proxies |
| Model catalog uses configured entries; Responses uses chat upstream | `internal/config/config_types.go:983` | `sdk/cliproxy/service_models.go:853`; `internal/runtime/executor/openai_compat_executor.go:124` |
| Global `plugins.enabled`, `plugins.dir`, per-plugin `enabled` and `priority` are distinct | `config.example.yaml:1225`, `:1260`; `internal/config/config_types.go:28`, `:44` | both switches set in plugin mode |
| Other plugin config keys are opaque to CPA | `config.example.yaml:1264`; `internal/config/config_types.go:49` | `state-dir`, `catalog-refresh-interval`, `guard-uncovered-endpoints` from our contract §1 |
| Root plugin directory supported; library entries must be regular files | `internal/config/plugin_path.go:10` | `internal/pluginhost/platform.go:141`, `:308`; copy to `<plugins.dir>/cpa-model-dock.<platform extension>` |
| Runtime provider view versus disk config view | — | `internal/api/handlers/management/config_lists.go:866`; `internal/api/handlers/management/config_v8.go:20` |
| Embedded catalogs avoid remote catalog updates | — | `cmd/server/main.go:165`; runner passes `-local-model` |

Development validation on macOS, CPA v8.0.44, Go 1.26.10: CPA built with cgo and Apple
clang; no-plugin integration passed; `go vet ./...` passed in this module. The sandbox
allowed cache reads/builds after relocating `GOCACHE`, but rejected loopback `bind`
with `operation not permitted`. Passing integration used elevated sandbox access for
those local listeners. No external service was needed.
