# CPAModelDock

[한국어](README.ko.md) · [Operations](docs/operations.md) · [Build guide](docs/building.md) · [Verification evidence](docs/verification.md) · [Binding contract](docs/design/contract.md) · [MIT License](LICENSE)

CPAModelDock is a native [CLIProxyAPI (CPA)](https://github.com/router-for-me/CLIProxyAPI/tree/v8.0.44) plugin for stable model aliases, model allow lists, and conservative automatic upgrades within a model family. Clients can request `gpt-sol` while an operator pins its target to `gpt-6.1-sol` or lets it advance to a strictly newer routable Sol model. Model Dock provides an English/Korean management UI, catalog and policy previews, decision explanations, and persisted change history.

The plugin ID is `cpa-model-dock`; its display name is `CPAModelDock`. Source and release binaries: <https://github.com/suk-6/cpa-model-dock> (Go module `github.com/suk-6/cpa-model-dock`). It is not listed in the official plugin store. Model names in the examples are illustrative fixtures, not provider availability announcements.

## Compatibility and verification

The tested host baseline is **CPA v8.0.44**, native **ABI 1**, with plugin JSON schema **6–7** negotiation. Schema negotiation tests do not establish compatibility with every host using that ABI. Required management/resource APIs are present in locally inspected v8.0.37 source; **v8.0.37 is not a tested minimum version**. See [CPA ABI constants](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/sdk/pluginabi/types.go), [v8.0.37 API declarations](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.37/sdk/pluginapi/types.go), [plugin registration](dispatch.go), and [ABI tests](dispatch_test.go).

Builds passed for Darwin amd64/arm64, Linux amd64/arm64, and Windows amd64. Go/ABI checks, Linux arm64 ABI smoke, and Node tests passed; exact environments, artifact hashes, sizes, and limitations are in [verification](docs/verification.md) and [building](docs/building.md). Linux release outputs require **glibc ≥2.34**; these Debian-built binaries do not support musl-only Alpine. Windows native loading has not been tested.

On 2026-10-27, the full harness on **real CPA v8.0.44 with a fake OpenAI-compatible upstream** completed **29 PASS / 0 FAIL / 0 SKIP**. The upstream recorder verified pinned alias JSON/SSE routing, response-model rewriting, alias-only listing and denials, numeric automatic upgrades, removed-target failure without fallback, revision conflicts, atomic invalid-state rejection, and restart recovery of policy/history. This is not a live-provider credential test. Observation rows also confirmed gaps: valid built-in API keys bypass the realtime/live guard, and Gemini detail GET leaks hidden metadata. Unit-only, source-only and unverified cases are separated in [verification](docs/verification.md).

## Install and open Model Dock

### Manual installation

1. Obtain a trusted ZIP matching CPA's OS and CPU, or build locally. Verify its SHA-256 against the trusted distributor's `SHA256SUMS`.
2. Stop CPA for a predictable manual replacement. Extract the **root-level** library into the configured `plugins.dir`: `cpa-model-dock.dylib` on macOS, `cpa-model-dock.so` on Linux, or `cpa-model-dock.dll` on Windows. Do not rename the plugin ID or install a ZIP as a library. Remove conflicting old copies from the plugin discovery directories.
3. Add the configuration below to the existing CPA configuration; retain your server, management authentication, and provider settings. Create the state directory with write permission for the CPA service account.
4. Start CPA, check the management center's installed plugin list for a loaded/enabled `cpa-model-dock`, then open the plugin menu **Model Dock**.

CPA supports a root plugin directory and platform subdirectories; store installations use versioned library names. Both the global and per-plugin enable switches are required. The loader uses native libraries and a C function table, so OS/architecture, native ABI, supported JSON schema, and compatible C/compiler/runtime dependencies all matter. A shared ABI number alone is insufficient. Sources: [discovery](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginhost/platform.go), [Unix loader](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginhost/loader_unix.go), [Windows loader](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginhost/loader_windows.go).

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

Set a **unique highest priority** among installed plugins for the intended policy ordering; `1000` is only an example, not a reserved maximum. CPA sorts capabilities by descending priority, then ID on ties. Priority cannot turn the plugin into a universal security boundary or fix frontend authentication ordering. See [host configuration](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginhost/config.go) and [capability ordering](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginhost/snapshot.go).

| Setting | Actual behavior/default |
| --- | --- |
| `plugins.enabled` / instance `enabled` | CPA switches; both must be true. Omitted switches are disabled. |
| `plugins.dir` | Default `plugins`; relative paths resolve against CPA’s working directory. The host expands a leading `~`. |
| `plugins.store-sources` | Default `[]`; additional registry URLs. The official registry is always included. |
| `priority` | CPA ordering; omitted value is 0. Choose a unique highest value. |
| `state-dir` | Plugin-owned absolute path. Empty/omitted uses the **CPA process user's** `$HOME/.cli-proxy-api/cpa-model-dock`. Relative paths are rejected. |
| `catalog-refresh-interval` | Go duration, default `5m`, minimum `30s`. |
| `guard-uncovered-endpoints` | Default true; limited best-effort coverage described below. |

All model policy belongs in the plugin's state/API, not additional YAML alias or whitelist keys. `settings.refresh_interval_seconds > 0` overrides the YAML interval; `0` uses YAML. The initial state uses 300, so set it to 0 if you want a different YAML interval to control polling. Sources: [config parser and polling](app.go), [state defaults](internal/policy/policy.go).

### Store installation and self-hosting

In CPA's management center, use the plugin store to install `cpa-model-dock` **only after your chosen registry publishes it**. No public listing is promised. For a self-hosted registry, package artifacts with the correct download base URL and add its URL alongside the other `plugins` settings:

```yaml
plugins:
  enabled: true
  dir: plugins
  store-sources:
    - https://plugins.example.com/model-dock/registry.json
  configs:
    cpa-model-dock:
      enabled: true
      priority: 1000
      state-dir: /var/lib/cliproxyapi/cpa-model-dock
      catalog-refresh-interval: 5m
      guard-uncovered-endpoints: true
```

Replace the illustrative domain. Publish the generated schema-2 direct-install `registry.json` and ZIPs at the configured URLs; it declares platform, URL, SHA-256, and size. The default packaging `BASE_URL` uses `example.invalid` and is deliberately unusable for downloads. The store checks archive integrity, installs the matching platform library, and enables the instance; it **does not enable global `plugins.enabled`**. Keep the store-managed `store` mapping when editing the instance configuration. Checksums are not publisher signatures or a trust sandbox.

For a future GitHub-release store entry, CPA's GitHub installer specifically expects `checksums.txt`; `SHA256SUMS` alone does not satisfy that flow. The packager also generates an identical `checksums.txt`; publish it for that flow, or use the generated direct registry. See [packaging](packaging/README.md), [registry schema](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginstore/registry.go), [archive installer](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginstore/install.go), [GitHub asset selection](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginstore/github.go), and [store management](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/api/handlers/management/plugin_store.go).

### Update, disable, uninstall

Back up `state-dir` before changing versions. Store updates use the same install operation with the desired version; manual updates replace the matching library while CPA is stopped. Keep `state-dir` unchanged to retain policy/history. Confirm loaded version and policy after restart/reload. Windows can require a restart when replacing a loaded same-version artifact.

Disable through the instance switch, or uninstall through CPA's plugin list (`DELETE /v8/management/plugins/cpa-model-dock`); for manual removal stop CPA and remove its library/configuration. **The plugin's state directory is retained** and must be removed separately only if you intend to erase policy/history. Once disabled, uninstalled, unloaded, or fused, the plugin cannot enforce policy. Native Go library images may remain mapped until CPA exits. Sources: [uninstall handler](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/api/handlers/management/plugins.go), [loader lifetime](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginhost/loader_unix.go).

### UI and management key

The menu opens:

```text
/v0/resource/plugins/cpa-model-dock/index.html
```

This exact URL also works directly on the CPA origin. The four embedded static resources (`index.html`, `app.js`, `app.css`, `i18n.js`) are public; they contain no state or credentials. All policy requests go to authenticated `/v0/management/plugins/cpa-model-dock` routes. Use a **CPA management key**, not an inference API key. Public HTML loading does not prove management authentication succeeded.

The UI reads the management center's existing `cli-proxy-auth` or legacy `managementKey` storage without changing it. A manually entered key stays in memory unless **Remember for this tab** is selected; that option stores it in `sessionStorage['cpa-model-dock.tab-key']` and takes precedence over the center's key. Only the language choice is written to localStorage by this UI. Keys are sent in the Authorization header, not URLs or logs; known-key text is redacted from rendered API data/errors. Calls stay on `location.origin`, ignore a stored `apiBase`, and reject redirects. Storage failures fall back to the password input. These are plugin UI properties, not guarantees about all CPA pages, other browser code, or native files. Sources: [UI](web/assets/app.js), [resources and CSP](internal/mgmt/handler.go), [CPA resource dispatch](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/pluginhost/management.go).

Overview shows saved policy. Aliases/Whitelist edit a draft; **Save changes** validates then saves, and **Discard** drops draft edits after confirmation. Checks and history restore require a clean draft. Catalog refresh preserves edits but a concurrent saved policy can cause a revision conflict. Select English or 한국어 in the UI.

Real-CPA evidence: all four public resources carried strict CSP; authenticated management calls succeeded. Separately, headless Chrome against real CPA rendered all four tabs in English and Korean with no CSP violations. The management-center iframe was checked only for resource loading; a real browser session login flow, key replacement and browser conflict handling were not verified. Key/storage logic has unit coverage; stale-write rejection was verified by the harness API checks.

## Policy and automatic upgrades

The binding [contract §3](docs/design/contract.md#3-policy-semantics-single-engine-for-listing-and-requests), [policy engine](internal/policy/policy.go), and [hooks](internal/hooks/hooks.go) define this decision order:

1. Invalid/unloadable state without a last-good policy: deny with `policy_unavailable`; public lists are empty.
2. Enabled alias: with whitelist enabled it needs its own enabled **alias rule**. Otherwise deny `alias_not_published`. A published alias routes internally to its target even if the original is hidden. An unroutable target returns 503 `alias_target_unavailable`; there is no fallback model.
3. Original target of an enabled alias with `expose_original:false`: hide and reject direct requests (`hidden_by_alias`, HTTP 404), **unless an enabled exact rule explicitly permits that name**. A family/pattern rule does not override hiding.
4. Whitelist disabled: allow the remaining names.
5. Whitelist enabled: allow names matching enabled exact/family/pattern rules, otherwise deny. An empty enabled rule set blocks everything. Exact rules do not expand automatically; alias rules publish aliases only.

Lists contain only allowed, routable models and published aliases cloned from routable targets. Filtering a list is not an independent authorization boundary. Typical inference errors are 403 `model_not_allowed`, 404 `model_not_found`, and 503 `alias_target_unavailable`/`policy_unavailable`; explain reason codes describe the more specific policy decision.

Comparison trims whitespace, ignores case, removes a trailing thinking suffix `(...)` and Gemini `models/`. Credential prefixes are stripped **only when explicitly reported by the host**. Stock v8.0.44's runtime DTO reports none, so its trusted-prefix set is empty; arbitrary `p/model` names remain literal. Normalization is for comparisons, not a general rewrite of forwarded names. After CPA's credential selection and native alias/pool mapping, **AfterAuth checks the resolved destination again**: an alias destination must equal its configured target; a non-alias destination must pass hiding/allow rules. CPA provides no mapping provenance here, so a different native alias destination is conservatively denied. Do not assume `oauth-model-alias`, provider pools, or prefixes can safely bypass these checks. Sources: [catalog projection](internal/catalog/catalog.go), [CPA credential model resolution](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/sdk/cliproxy/auth/conductor_models.go), [AfterAuth execution](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/sdk/cliproxy/auth/conductor_execution.go).

Automatic aliases match the **whole model name** against a family template. `gpt-{version}-sol` matches `gpt-6.1-sol`, not `gpt-6.1-sol-mini`. Numeric components determine order (`6.10 > 6.9`; `4-5 == 4.5`), then comparable dates. `{date}` supports full dates and unambiguous short dates; the parser does not invent a year/century to resolve uncertain ordering. Same-version dated/undated candidates, different date precision, tied best candidates, or incomparable prereleases can be ambiguous: **keep the current target**, record the reason. Never downgrade or move to an equal version; an unparseable current target is kept (`current_unparseable`). Prereleases (`preview`, `beta`, `alpha`, `exp`, `experimental`, `rc`, `nightly`, `dev`, `test`) are excluded by default. Family allow rules exclude prereleases as well.

Built-in families are Sol, Astra, Luna, Claude Opus/Sonnet/Haiku/Fable, and Gemini Pro/Flash. Custom families use exactly one `{version}` and optionally one `{date}`. Candidates use enabled runtime credential advertisements. Contract §4 also accepts per-client host model registrations as cached routability evidence for config-synthesized OpenAI-compatible clients omitted from runtime inventory; explicit disabled/unavailable evidence still wins. Other missing credentials and catalog-only entries do not establish routability; a failed refresh keeps the last-good facts and target. This inventory cannot guarantee scheduler eligibility or inspect live per-model cooldowns atomically, and excludes static executor-only registry clients. Health checks are **off by default**; enabling one can make a paid minimal request through CPA before switching. A dry run does not make that paid probe. Sources: [version parser](internal/version/version.go), [auto evaluator](internal/autoupdate/evaluate.go), [catalog](internal/catalog/catalog.go), [probe implementation](app.go).

## Four walkthroughs: UI and curl

Use these on a test instance or review them before replacing an existing policy. Each JSON example is a **full state replacement**, not a patch, and removes unrelated aliases/rules. Unknown or unroutable targets may be saved to keep offline aliases repairable; inference through them fails with 503 and no fallback. Register illustrative targets `gpt-6.1-sol`, `gpt-6-astra`, and `claude-opus-4-5` in your fixture/provider configuration as appropriate. See [example files](examples/README.md).

### Shared curl helper

Requires curl and jq. Set keys in your local shell without putting real credentials into checked-in files. `CPA_MANAGEMENT_KEY` is for management; `CPA_API_KEY` below is a separate inference key.

```sh
export CPA_ORIGIN=http://127.0.0.1:8317
# Set CPA_MANAGEMENT_KEY and CPA_API_KEY privately in your environment.
export DOCK_API="$CPA_ORIGIN/v0/management/plugins/cpa-model-dock"

apply_example() (
  set -eu
  example_file=$1
  # Validation is POST, never PUT. Abort on HTTP failure or valid:false.
  validation=$(curl --fail-with-body -sS -X POST "$DOCK_API/validate" \
    -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" \
    -H 'Content-Type: application/json' --data-binary "@$example_file")
  printf '%s\n' "$validation" | jq .
  printf '%s\n' "$validation" | jq -e '.valid == true' >/dev/null
  # Read the current revision immediately before replacing the state.
  current=$(curl --fail-with-body -sS "$DOCK_API/state" \
    -H "Authorization: Bearer $CPA_MANAGEMENT_KEY")
  revision=$(printf '%s\n' "$current" | jq -er '.revision')
  jq --argjson revision "$revision" '.revision = $revision' "$example_file" |
    curl --fail-with-body -sS -X PUT "$DOCK_API/state" \
      -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" \
      -H 'Content-Type: application/json' --data-binary @-
)

explain_model() {
  jq -n --arg model "$1" '{model:$model}' |
    curl --fail-with-body -sS -X POST "$DOCK_API/explain" \
      -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" \
      -H 'Content-Type: application/json' --data-binary @-
}
```

A 409 means another save/automatic update won the race: GET state, review the change, then revalidate/reapply intentionally. Do not retry blindly. The shipped revision `0` is a placeholder; never reuse it as the live revision. `POST /explain` evaluates policy without making an inference request; its JSON `status` is distinct from its management HTTP status.

### 1. Pin a stable Sol alias

In **Aliases → Add alias**, enter `gpt-sol`, select **Pinned**, target `gpt-6.1-sol`, leave provider empty, enable **Allow the original model name**, and keep response rewriting at its false default. Leave whitelist off and save. Both the alias and the routable original are intended to remain public.

```sh
apply_example examples/01-pinned-sol.json
explain_model gpt-sol
curl --fail-with-body -sS "$CPA_ORIGIN/v1/chat/completions" \
  -H "Authorization: Bearer $CPA_API_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-sol","messages":[{"role":"user","content":"Hello"}]}'
```

The last command is a real inference and may incur provider charges. It requires a routable upstream; saving the example alone does not establish that.

Real-CPA + fake-upstream evidence: the recorder confirmed `gpt-sol` reached `gpt-6.1-sol` for JSON and SSE, with global response rewriting off/on and the per-alias override checked.

### 2. Automatically advance within Sol

Create/edit `gpt-sol` with **Automatic**, initial target `gpt-6.1-sol`, family **GPT Sol**, original allowed and prereleases off. Keep the API-only `auto.health_check` at its false default. Leave whitelist off and save. Use **Check now** to preview candidates, then **Apply check** to reevaluate/apply. Background refresh also evaluates auto aliases; it is not limited to button clicks.

```sh
apply_example examples/02-auto-sol.json
curl --fail-with-body -sS -X POST "$DOCK_API/aliases/check" \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" -H 'Content-Type: application/json' \
  -d '{"dry_run":true}'
revision=$(curl --fail-with-body -sS "$DOCK_API/state" \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" | jq -er '.revision')
jq -n --argjson revision "$revision" '{revision:$revision,dry_run:false}' |
  curl --fail-with-body -sS -X POST "$DOCK_API/aliases/check" \
    -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" -H 'Content-Type: application/json' \
    --data-binary @-
```

A result of `kept` can be correct: no routable candidates, ambiguity, failed health check, or an unparseable current target must not trigger a guessed replacement.

Real-CPA + fake-upstream evidence: numeric selection preferred `6.10` over `6.9`, excluded mini/preview candidates, and JSON/SSE reached `6.10`. Date ordering, ambiguous-candidate retention and no-downgrade edge cases have unit coverage only; they were not exercised by this real-CPA run.

### 3. Allow only Sol, Astra, and Claude Opus families

Clear unrelated draft aliases/rules. In **Whitelist**, add three enabled **Model family** rules: **GPT Sol**, **GPT Astra**, and **Claude Opus**. Enable the whitelist and save. This case uses **only these three families**. It should admit routable `gpt-6.1-sol`, `gpt-6-astra`, and `claude-opus-4-5` and future stable whole-family matches; mini variants and unrelated families remain excluded.

```sh
apply_example examples/03-family-whitelist.json
explain_model gpt-6.1-sol
explain_model gpt-6-astra
explain_model claude-opus-4-5
explain_model gpt-6.1-sol-mini
curl --fail-with-body -sS "$CPA_ORIGIN/v1/models" \
  -H "Authorization: Bearer $CPA_API_KEY"
```

Unit-only evidence for the decision rules used by this three-family policy: whole-family matching, prerelease exclusion, hiding and whitelist decisions. The real-CPA run tested alias-only listing and direct/prefixed/native/thinking/Claude/Gemini denials, not this exact Sol/Astra/Opus policy with successful inference for every family.

### 4. Publish only the alias, hide the original

Create a pinned, enabled `gpt-sol → gpt-6.1-sol`. Turn **Allow the original model name** off (`expose_original:false`), turn the default **Use alias names in responses** on, and leave the alias rewrite override at **Use default**. In **Whitelist**, enable the whitelist, remove other rules, and add exactly one enabled **Alias** rule for `gpt-sol`. Save. Only that alias should be public when its target is routable; internal target execution is allowed while direct target requests are hidden/404. Do not add an exact rule for the target: it would override hiding.

```sh
apply_example examples/04-aliases-only.json
explain_model gpt-sol
explain_model gpt-6.1-sol
curl --fail-with-body -sS "$CPA_ORIGIN/v1/models" \
  -H "Authorization: Bearer $CPA_API_KEY"
# Expected hidden-model denial on a covered inference path:
curl -sS -i "$CPA_ORIGIN/v1/chat/completions" \
  -H "Authorization: Bearer $CPA_API_KEY" -H 'Content-Type: application/json' \
  -d '{"model":"gpt-6.1-sol","messages":[{"role":"user","content":"Hello"}]}'
```

Real-CPA + fake-upstream evidence: `/v1/models` was exactly `[gpt-sol]`; alias JSON/SSE succeeded. Hidden original and thinking-form requests returned 404; prefixed/native/unrelated names returned 403, all with zero upstream calls. Response rewriting off/on and alias override were checked.

## State and management API

The stored schema is **1**. A newly initialized policy has revision 0, empty families/aliases/rules, whitelist false, response rewrite false, and refresh interval 300 seconds. `revision` and `updated_at` are server-managed; GET state also returns read-only built-in families and alias status. Save the complete `settings`, `families`, `aliases`, and `rules` objects/arrays. Unknown runtime view fields are not policy settings.

| Field | Meaning / actual decoding |
| --- | --- |
| `settings.whitelist_enabled` | Boolean, initial false. |
| `settings.rewrite_response_model` | Boolean, initial false; aliases may override. |
| `settings.refresh_interval_seconds` | 0 (host config) or integer ≥30; initial 300. |
| `families[]` | `id`, `name`, `template`, optional `provider`, `allow_dated` defaults true. Built-ins are read-only. |
| `aliases[]` | `id`, `name`, `enabled`, `mode` (`pinned`/`auto`), required `target`, `provider`, `expose_original`, nullable `rewrite_response_model`, `auto`. |
| `aliases[].auto` | `family`, `include_prerelease`, `health_check`; flags default false. |
| `rules[]` | `id`, `enabled`, `kind` (`exact`/`family`/`pattern`/`alias`), `value`, optional `note`. Patterns support `*` and `?`, not regular expressions. |

**Full settings require both booleans and an integer refresh interval.** Omitted alias/rule `enabled` and custom-family `allow_dated` default true; `expose_original` and auto flags default false. The examples send values explicitly to make intent clear. Missing provider is empty; missing/null alias rewrite inherits the global default. A full replacement requires all four top-level policy fields. New aliases may omit `id` for server generation or supply a unique stable ID; retain IDs when editing. Custom family/rule IDs can be supplied to reference them in the same save; all IDs must be unique. Alias names cannot collide case-insensitively with catalog models or other aliases; targets identify upstream models, not other Dock aliases; unknown/unroutable targets remain saveable and fail inference with 503. Sources: [types](internal/policy/types.go), [input decoding](internal/mgmt/input.go), [validation](internal/policy/validate.go), [examples](examples/README.md).

All paths below are relative to `/v0/management/plugins/cpa-model-dock`; all require the management key.

| Method/path | Purpose |
| --- | --- |
| `GET /overview`, `GET /state`, `GET /catalog` | Saved policy, current revision, status and catalog. |
| `PUT /state` | Full validated policy replacement; requires revision. |
| `POST /validate` | Validate full draft and preview, without saving. **PUT is not supported.** |
| `POST /catalog/refresh` | Refresh catalog only; no revision required and no policy/history/alias-status mutation. |
| `POST /aliases/check` | Preview with `dry_run:true`; otherwise apply with revision. Optional `alias_id`. |
| `POST /rules/preview` | Preview a draft's allow rules and public names. |
| `POST /explain` | `{model,draft?}` policy explanation, no inference. |
| `GET /history?limit=100&before=...` | Newest-first history; `before` is an exclusive entry-ID cursor. |
| `POST /history/restore` | `{entry_id,revision,scope:"state"|"alias_target"}`. Undo to the selected entry's **before** value as a new revision. |

Mutations use a current body `revision` or quoted `If-Match: "<n>"`. If both are provided they must agree. Read-only previews/validate/explain and dry-run checks do not require revision. Invalid saves return 422 and change nothing; `/validate` reports policy validation results with HTTP 200 and `valid:true` or `valid:false`. Stale revisions return 409. Refetch GET state after mutations instead of predicting revision increments. Refresh and unchanged checks do not advance policy revision. API runtime status/preview shapes may evolve during backend/UI integration; the binding contract and current backend take precedence over the development mock. Sources: [routes](internal/mgmt/handler.go), [views](internal/mgmt/views.go), [application](app.go).

History restore undoes to the selected entry's `before` value as a new revision. State scope restores the full policy; `alias_target` scope restores only that alias's target in the current policy and fails validation if the alias no longer exists. It never rolls revision backward or deletes history. Persistence uses atomic writes, a last-good `state.json.bak`, and capped `history.jsonl` (1000 entries). Load order is primary → backup → defaults only when neither exists; existing corrupt files without a valid backup yield `policy_unavailable`. Run one writer per state directory; there is no cross-process lock. See [store](internal/store/store.go) and [operations](docs/operations.md).

## Known limitations, enforcement boundaries and privacy

**This is not a universal security guarantee.** Use a unique highest plugin priority, trusted native code, and mandatory proxy restrictions for uncovered paths. Restrict direct access to CPA so clients cannot bypass that proxy. The [operations guide](docs/operations.md) gives concrete nginx/Caddy blocks.

| Boundary | What v8.0.44 actually does |
| --- | --- |
| Realtime/live | No model hooks. The nonexclusive guard can deny when invoked with whitelist on, but stock startup registers config-access first and stops at the first successful authentication: the real-CPA probes with valid built-in client keys observed no guard denial (native 503 `auth_not_found` both off/on). This guard is not protection for these keys. Realtime client secrets also bypass fallback providers. Block `/v1/realtime*` and `/v1/live*` externally. |
| Responses WebSocket | Source-verified only: in-flight continuation frames do not rerun ModelRouter/BeforeAuth/AfterAuth. Explicit create/append model changes require replay rather than being silently accepted; steer uses the bound model for authorization but passes control fields through finalization/upstream validation. Frames cannot be filtered by URL path after upgrade. Strict deployments should block GET `/v1/responses` and GET `/backend-api/codex/responses`. |
| Gemini model detail | Real CPA returned HTTP 200 with hidden model metadata at GET `/v1beta/models/{id}`; it bypasses the filtered model-list hook. Restrict detail GET independently; POST inference is a different path/method. |
| Management raw requests | Source-verified only: POST `/v0/management/api-call` and POST `/v8/management/requests/api-call` do not use the model-policy pipeline. Deny these privileged transports from non-admin networks; CPA management authentication is still required for admins. |
| Video content | GET `/openai/v1/videos/{video_id}/content` runs hooks for its metadata/status lookup, then downloads/copies binary content with a separate HTTP client outside model response/stream hooks. |
| Final payload rules | Provider normalization and configured payload defaults/overrides/filters can run **after AfterAuth**. The plugin does not authorize every final byte sent upstream. |
| Other plugins / load state | Higher-priority routers/interceptors, native executors, disabled/not-loaded/unloaded/fused plugins, and host changes remain trust boundaries. Priority alone cannot guarantee enforcement. |
| In-flight requests | Requests pin policy/catalog snapshots; a later revocation does not retroactively revoke admitted work. Pins expire after completion or ten minutes of inactivity. |

Codex Alpha Search is effectively disabled **with the guard enabled whenever whitelist rules or alias target hiding require final-model authorization**: CPA exposes public IDs without credential mapping provenance, so even exact whitelisted raw, prefixed or CPA-native mapped names are unverifiable. Dock aliases are **always denied on Alpha, even with the guard off**, because OAuth forwards the original body model and API-key mappings resolve after selection without AfterAuth. Both Alpha paths returned the static denial executor's HTTP 503 with zero upstream calls, including published Dock aliases and native/prefix/raw exact allows. An unrestricted default policy passes native Alpha through; turning the guard off removes that conservative destination gate but cannot make Dock aliases work safely. The baseline only reached host `auth_not_found` without Codex credentials. Live Codex OAuth forwarding is source-verified only, not tested with real provider credentials. See [contract §6](docs/design/contract.md#6-guard--uncovered-endpoints).

These concrete examples are for a dedicated gateway. **Proxy syntax/runtime validation remains unverified**; test in your deployment. Replace `10.20.30.0/24` with the admin network and prevent direct CPA access. Admins still need the CPA management key. Caddy `remote_ip` and nginx address restrictions use the connecting peer; review the trust boundary if another proxy sits in front.

nginx exact `=` locations win; otherwise the longest prefix is remembered and the first matching regex in file order wins. A longest matching `^~` prefix skips regex checks **regardless of file position**; do not add bypassing exact/`^~` locations or earlier broad regex proxies. The Gemini regex matches the detail prefix, but its `if` rejects only GET. POST uses that location's own `proxy_pass`, preserving `:generateContent`/`:streamGenerateContent`; exact GET `/v1beta/models` uses the general proxy. Caddy uses `route` order and the combined GET+path matcher. See [official references and deployment checks](docs/operations.md#nginx).

<details>
<summary>nginx / Caddy examples</summary>

```nginx
server {
    listen 127.0.0.1:8080;
    server_name localhost;

    location ~ ^/v1/(realtime|live) {
        return 403;
    }
    location = /v0/management/api-call {
        allow 10.20.30.0/24;
        deny all;
        proxy_pass http://127.0.0.1:8317;
        proxy_http_version 1.1;
    }
    location = /v8/management/requests/api-call {
        allow 10.20.30.0/24;
        deny all;
        proxy_pass http://127.0.0.1:8317;
        proxy_http_version 1.1;
    }
    # Optional strict Alpha Search restrictions.
    location = /v1/alpha/search {
        return 403;
    }
    location = /backend-api/codex/alpha/search {
        return 403;
    }
    location ~ ^/(v1|backend-api/codex)/responses/?$ {
        if ($request_method = GET) { return 403; }
        proxy_pass http://127.0.0.1:8317;
        proxy_http_version 1.1;
        proxy_buffering off;
    }
    location ~ ^/v1beta/models/ {
        if ($request_method = GET) { return 403; }
        proxy_pass http://127.0.0.1:8317;
        proxy_http_version 1.1;
        proxy_buffering off;
    }
    location ~ ^/openai/v1/videos/[^/]+/content/?$ {
        if ($request_method = GET) { return 403; }
        proxy_pass http://127.0.0.1:8317;
        proxy_http_version 1.1;
        proxy_buffering off;
    }
    location / {
        proxy_pass http://127.0.0.1:8317;
        proxy_http_version 1.1;
        proxy_buffering off;
    }
}
```

```caddyfile
http://127.0.0.1:8080 {
    @live path /v1/realtime* /v1/live*
    @rawManagementNonAdmin {
        path /v0/management/api-call /v8/management/requests/api-call
        not remote_ip 10.20.30.0/24
    }
    @alpha path /v1/alpha/search /backend-api/codex/alpha/search
    @responsesWS {
        method GET
        path /v1/responses /v1/responses/ /backend-api/codex/responses /backend-api/codex/responses/
    }
    @geminiDetail {
        method GET
        path /v1beta/models/*
    }
    @videoContent {
        method GET
        path_regexp video ^/openai/v1/videos/[^/]+/content/?$
    }
    route {
        respond @live 403
        respond @rawManagementNonAdmin 403
        respond @alpha 403
        respond @responsesWS 403
        respond @geminiDetail 403
        respond @videoContent 403
        reverse_proxy 127.0.0.1:8317 {
            flush_interval -1
        }
    }
}
```

</details>

Request-hook denials use successful `Terminate` responses; ordinary hook errors would allow CPA to continue. Response/stream DTOs cannot set an HTTP status or terminate an already open socket, so recovery can sanitize a body or drop a chunk but cannot retroactively enforce a transport-level denial.

Source evidence: [route registration](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/api/server_routes.go), [v0 management](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/api/server_management.go), [v8 management](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/api/server_management_v8.go), [frontend provider order](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/sdk/cliproxy/builder.go), [first-success authentication](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/sdk/access/manager.go), [client-secret middleware](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/api/server_middleware.go), [WS continuations](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/runtime/executor/codex_websockets_duplex.go), [video download](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/sdk/api/handlers/openai/openai_videos_handlers.go), [payload rules](https://github.com/router-for-me/CLIProxyAPI/blob/v8.0.44/internal/runtime/executor/openai_compat_executor.go), [plugin hooks](internal/hooks/hooks.go).

An audit of this plugin's Go code and embedded web assets found **no telemetry code or external analytics endpoint**. Catalog polling uses CPA host callbacks; optional paid health probes use CPA model execution. Catalog responses project sanitized model/provider/count/routability facts, not upstream tokens, keys, raw auth files, or credential email fields. The plugin UI/API does not expose credential secrets; its management key is used only for authentication with the scoped browser storage behavior above. This statement applies to this plugin, not CPA's other management APIs, logs, native credential files, or other plugins. Native plugins run inside the CPA process with its privileges and must be trusted. Sources: [catalog projection](internal/catalog/catalog.go), [management](internal/mgmt/handler.go), [UI](web/assets/app.js), [host callbacks/probe](app.go).

## Build, test, troubleshoot

Go **1.26.10**, cgo, and a compatible target C toolchain are required; Python 3 packages artifacts. Node **24** runs the UI mock/tests using built-ins only; there is no npm install or web bundle step. Never read or depend on `node_modules` for this project workflow.

```sh
export GOTOOLCHAIN=go1.26.10
# On macOS:
export CC=/usr/bin/clang
make test
make build
python3 scripts/abi_smoke.py dist/cpa-model-dock.dylib

make dist                       # all five platforms
make dist-darwin-amd64
make dist-darwin-arm64
make dist-linux-amd64
make dist-linux-arm64
make dist-windows-amd64
make package                    # builds all selected targets, then packages
make package PACKAGE_ONLY=1     # packages existing outputs
make package PLATFORMS='darwin/arm64 linux/amd64' \
  BASE_URL=https://plugins.example.com/model-dock/0.1.0

node --test web/dev/tests/*.test.mjs
node web/dev/mock-server.mjs
# Open http://127.0.0.1:8787/v0/resource/plugins/cpa-model-dock/index.html
# Mock management key: test-key

E2E_NO_PLUGIN=1 ./e2e/run.sh
CPA_MODEL_DOCK_LIB="$PWD/dist/cpa-model-dock.dylib" ./e2e/run.sh
```

`make build` provides `dist/cpa-model-dock.<ext>` for the host; platform builds produce `dist/<os>/<arch>/cpa-model-dock.<ext>`. Packaging creates `cpa-model-dock_0.1.0_<os>_<arch>.zip` with the unversioned library at ZIP root, `SHA256SUMS`, its identical `checksums.txt`, and `registry.json`. `PACKAGE_ONLY=1` requires the selected outputs to exist; `PLATFORMS` limits the set. Cross-builds require the target compilers described in [building](docs/building.md), not merely `GOOS`/`GOARCH` changes. The supplied full build uses macOS for Darwin and a Linux Docker daemon for Linux/Windows. A successful cross-build is not proof of native loading on that platform.

For cached/offline modules use `GOCACHE=/tmp/cpa-model-dock-go-cache GOFLAGS=-mod=mod GOPROXY=off make test`. E2E needs local listeners, cached dependencies or appropriate module access, a cgo-enabled CPA binary, and an explicit library path matching your platform. `E2E_NO_PLUGIN=1` skips plugin scenarios. See [E2E harness](e2e/README.md), [mock development](web/dev/README.md), and [verification](docs/verification.md).

| Symptom | Check |
| --- | --- |
| Plugin/menu absent | Both enable switches, plugin filename/dir, OS/arch, glibc/C runtime, loader errors, loaded version, priority. Static HTML alone is not a health check. |
| 401/403 management | Use the management key and host management-access settings; an inference key is insufficient. Replace an expired tab key. |
| 422 save / `valid:false` validate | Use POST validate, full policy fields, required settings, valid target syntax and unique names/IDs. Read `error.details` or validation details. |
| 409 save | Refetch revision and reconcile the draft; an auto update may have saved first. |
| Empty list / 503 alias | Check whitelist rules, alias publication, target availability, provider pin, and catalog credential counts. Registered does not mean routable. |
| Prefix/native alias denied | Stock host supplies no trusted prefix/mapping provenance; inspect the resolved target and AfterAuth policy. |
| Auto keeps target | Check whole-family match, routability, numeric/date ambiguity, prerelease settings, catalog refresh, and health probe result. |
| Restore differs from UI wording | Restore undoes to the selected entry's **Before** value, creating a new revision. |
| Corrupt/unwritable state | Verify service-user permissions; preserve backups and repair through the API. Do not delete state to silence a deny-all condition. |
| Realtime/WS/detail appears unfiltered | Apply and test the proxy controls; these are documented host limitations. |

See [operations](docs/operations.md) for backup/recovery and gateway examples. Report integration results separately from source inspection, mock tests, and management-only validation.
