# CPAModelDock

CPAModelDock is a native CLIProxyAPI plugin (`cpa-model-dock`) for model aliases,
whitelists, and conservative automatic family upgrades. The Go module owner
`github.com/cpa-model-dock/cpa-model-dock` is a placeholder project namespace.

The backend targets CPA v8.0.44, native ABI 1, and JSON schemas 6–7. It uses the
pinned official `sdk/pluginapi` and `sdk/pluginabi` DTOs. The binding state and
HTTP API are documented in [the contract](docs/design/contract.md).

## Build and test

Go 1.26.10 and cgo are required. On macOS use the system compiler:

```sh
export GOTOOLCHAIN=go1.26.10
export CC=/usr/bin/clang
make test
make build
python3 scripts/abi_smoke.py dist/cpa-model-dock.dylib
```

For a sandbox with an existing module cache and no network:

```sh
GOCACHE=/tmp/cpa-model-dock-go-cache GOFLAGS=-mod=mod GOPROXY=off make test
```

`make build` writes the platform shared library to `dist/`. Cross compilation
requires target C toolchains; the cross-build targets are packaging placeholders.
The Python smoke test calls the actual exported C init/call/free/shutdown table
with fake local host callbacks. It makes no external requests.

Only `web/assets/PLACEHOLDER.txt` is supplied in this backend worktree. Merge the
parallel UI assets before distributing the UI. Missing embedded files return 404.

## Host configuration

Copy the shared library to CPA's plugin directory and configure:

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    cpa-model-dock:
      enabled: true
      priority: 1000
      state-dir: /var/lib/cliproxyapi/cpa-model-dock
      catalog-refresh-interval: 5m
      guard-uncovered-endpoints: true
```

The CPA process needs write access to `state-dir`. An empty value defaults to
`$HOME/.cli-proxy-api/cpa-model-dock`. Back up that directory; it contains
`state.json`, `state.json.bak`, and capped `history.jsonl`. Use one plugin writer
per state directory. There is no cross-process writer lock.

Policy edits use the authenticated API under
`/v0/management/plugins/cpa-model-dock`. A revision or quoted `If-Match` is required
for writes, applying alias checks, and manual refresh. Read-only previews and
dry-run checks do not write. The static UI entry is
`/v0/resource/plugins/cpa-model-dock/index.html`; resources never return state.

Defaults preserve CPA's public model policy. Enabling a whitelist with no allow
rules denies all covered models. An alias can execute its target while hiding
and rejecting direct calls to the original ID. Paid update probes are disabled
by default and run only when explicitly enabled for an auto alias.

## Verified coverage and limits

The Go suite and native ABI smoke test use fake host callbacks. A production CPA
instance has not been exercised. The following behavior is verified in v8.0.44
source and reflected in the contract:

- Policy denials use successful interceptor responses with `Terminate: true`;
  returning hook errors would let CPA continue. Response/stream hooks cannot set
  status or terminate a transport; they return sanitized errors or drop chunks.
- Alpha Search has only ModelRouter coverage. CPA rejects our valid static denial
  executor route with 503. A nonexistent provider sentinel would be ignored.
- The nonexclusive realtime/live guard is **not a mandatory access gate**. On
  stock startup with API keys, config-access is registered first and can accept
  requests before the plugin runs. Realtime client secrets also bypass fallback
  frontend providers. Plugin priority does not change that authentication order.
- In-flight Responses WS create/append rejects an explicit model change by
  requiring replay, but continuation and steer frames do not rerun model hooks.
  Steer control fields are left to upstream validation after payload finalization.
- Gemini model detail and management `api-call` bypass model hooks. Final payload
  rules, other trusted plugins, and disabled/fused/unloaded plugins also lie
  outside this plugin's enforcement boundary.
- Host callbacks expose registered public model IDs, not a complete static catalog
  or exact live eligibility. Missing/disabled/unavailable runtime credentials are
  excluded. Routable models expose only providers with enabled runtime evidence.
  Failed refreshes preserve the last good registration facts and alias targets.
- Stock runtime DTOs expose neither credential prefixes nor CPA alias provenance.
  The plugin never infers trusted prefixes from slashes, and conservatively denies
  an alias whose AfterAuth destination differs from its configured target.

Use reverse-proxy restrictions for realtime/live, Responses WS, Gemini detail,
and management transports when policy must cover those paths. Do not claim
universal model enforcement based on the frontend guard or list filtering.

Before production use, verify CPA loads the library, authenticates management
routes, serves the merged UI with its CSP, executes alias routes for each intended
provider/protocol, handles SSE and WS delivery, and rejects denied Alpha Search
requests on a Codex-only host. Check ordering against other installed plugins.
