# CPAModelDock — Design & API Contract (v1)

This document is the shared contract between the backend (Go plugin) and the Web UI.
Both sides MUST follow it. If an implementer finds a contradiction with CPA's real
behavior, fix the implementation to match CPA and update this document in the same change.

Research notes with source citations: `.ref/RESEARCH-routing.md`, `.ref/RESEARCH-platform.md`
(local only, not committed). Target host: CLIProxyAPI v8.0.44 (ABI 1, schema 7; negotiate down
to 6 when host advertises 6).

## 1. Packaging

- Plugin ID: `cpa-model-dock`. Library file: `cpa-model-dock.{so,dylib,dll}` (Go `-buildmode=c-shared`, cgo).
- Go module path: `github.com/cpa-model-dock/cpa-model-dock` (placeholder owner; documented as such).
- Display name (Metadata.Name): `CPAModelDock`.
- Host config block (`config.yaml`):

```yaml
plugins:
  configs:
    cpa-model-dock:
      enabled: true
      priority: 1000            # recommended: highest among installed plugins
      state-dir: /var/lib/cliproxyapi/cpa-model-dock   # absolute; where state.json/history live
      catalog-refresh-interval: 5m   # min 30s
      guard-uncovered-endpoints: true  # deny realtime/live/alpha-search for non-allowed models (see §6)
```

`state-dir` empty → `$HOME/.cli-proxy-api/cpa-model-dock`. CPA does not expose its auth-dir or a
plugin data directory in the lifecycle payload. All other user policy lives in the
plugin-owned state file managed through the API below (NOT in config.yaml), so the UI never has to
edit host config.

## 2. Policy model (state.json, schema 1)

```jsonc
{
  "schema": 1,
  "revision": 12,                 // monotonically increasing; optimistic concurrency token
  "updated_at": "2026-10-27T10:00:00Z",
  "settings": {
    "whitelist_enabled": false,   // false = CPA default (everything allowed) except per-alias hiding
    "rewrite_response_model": false,   // default for aliases (alias may override)
    "refresh_interval_seconds": 300    // overrides config when > 0
  },
  "families": [                   // user-defined families; built-ins are provided by backend, read-only
    { "id": "fam_x", "name": "GPT Sol", "template": "gpt-{version}-sol", "provider": "" ,
      "allow_dated": true }
  ],
  "aliases": [
    {
      "id": "al_1",               // server-generated, stable
      "name": "gpt-sol",          // client-facing; unique (case-insensitive) across aliases and must not
                                  // equal any real catalog model id
      "enabled": true,
      "mode": "pinned",           // "pinned" | "auto"
      "target": "gpt-6.1-sol",    // current upstream model id (required in both modes)
      "provider": "",             // optional provider pin (e.g. "codex"); empty = any provider serving target
      "expose_original": false,   // false = target id hidden from lists AND blocked for direct calls
      "rewrite_response_model": null,   // null = use settings default
      "auto": {                   // used when mode == "auto"
        "family": "builtin:gpt-sol",    // family id (builtin:* or user fam_*)
        "include_prerelease": false,    // preview/beta/alpha/exp/experimental/rc
        "health_check": false           // optional paid probe before switching; default off
      }
    }
  ],
  "rules": [                      // whitelist allow rules; evaluated only when whitelist_enabled
    { "id": "r_1", "enabled": true, "kind": "family",  "value": "builtin:gpt-sol", "note": "" },
    { "id": "r_2", "enabled": true, "kind": "exact",   "value": "claude-opus-4-5-20251101" },
    { "id": "r_3", "enabled": true, "kind": "pattern", "value": "claude-opus-*" },  // glob: * and ?
    { "id": "r_4", "enabled": true, "kind": "alias",   "value": "gpt-sol" }         // publish alias
  ]
}
```

New aliases omit `id`; the server assigns it and preserves it on later saves. Family and rule
IDs may be supplied by clients so a new family and its referencing rules/aliases can be saved
together; omitted IDs are generated. IDs are unique across the state.

Runtime-only (not user-editable, returned by API): per-alias `status`
(`current_target`, `candidates[]`, `last_checked_at`, `last_result`: `updated|unchanged|kept`,
`reason` code + message, `target_routable` bool), catalog status, history.

### 2.1 Families & version ordering

Template tokens: `{version}` (numeric dotted or dashed, e.g. `6.1`, `6.10`, `4-5`), optional `{date}`
(YYYYMMDD, YYYY-MM-DD, YYMMDD/MMDD only when unambiguous). Literal text matches case-insensitively.
A candidate matches a family only if the WHOLE id matches the template (optionally followed by
`-{date}` when `allow_dated`), so `gpt-6.1-sol-mini` does NOT match `gpt-{version}-sol`.
Pre-release markers (`preview|beta|alpha|exp|experimental|rc|nightly|dev|test`) appear as an extra
trailing segment and are excluded unless `include_prerelease`.
Thinking suffix `(...)` is never part of a catalog id.

Ordering: compare version numerically per component (6.10 > 6.9; 4-5 == 4.5), then date. If the top
two candidates are not strictly ordered (e.g. same version, one dated and one undated), the result
is `ambiguous` → keep current target, record reason. Never move to a candidate <= current. If the
current target does not parse under the family → keep, reason `current_unparseable`.
Only **routable** candidates (advertised by at least one enabled credential, see §4) may be chosen;
catalog-only candidates are listed with `routable:false`.

Built-in families (read-only, ids `builtin:*`): at least gpt-sol (`gpt-{version}-sol`),
gpt-astra (`gpt-{version}-astra`), gpt-luna (`gpt-{version}-luna`), claude-opus (`claude-opus-{version}`),
claude-sonnet, claude-haiku, claude-fable, gemini-pro (`gemini-{version}-pro`), gemini-flash.
Backend may add more; UI must render whatever the API returns.

## 3. Policy semantics (single engine for listing AND requests)

Name normalization for matching (never for forwarding): trim, case-insensitive compare, strip a
trailing thinking suffix `(...)`, strip Gemini `models/` prefix, strip a credential prefix `p/`
ONLY when `p` is explicitly reported as a credential prefix by the host. **Stock CPA v8.0.44
reports no credential prefix in `HostAuthFileEntry`; its trusted-prefix set is therefore empty.**
The plugin accepts an optional explicit runtime `prefix` extension for future hosts and never
infers a prefix from a slash in a registered model ID. Unknown `x/y` spellings are matched
literally (and therefore normally denied when whitelist is on).

Decision order for a requested name N (first match wins):
1. Policy state invalid/unloadable (no last-good) → DENY all (`policy_unavailable`), lists empty.
2. N is an enabled alias → if whitelist on and no enabled `alias` rule for it → DENY (`alias_not_published`).
   Else route to its target; target executes internally even if the target id itself is not public.
   If target not routable → DENY 503 (`alias_target_unavailable`) — never fall back to another model.
3. N equals (normalized) the target of an alias with `expose_original=false` → DENY (`hidden_by_alias`)
   unless an enabled `exact` rule names N explicitly (explicit exact allow beats hiding).
4. Whitelist off → ALLOW (`whitelist_disabled`).
5. Whitelist on → ALLOW if any enabled exact/family/pattern rule matches N (`rule:<id>`); else DENY
   (`not_whitelisted`). Enabled rules list empty → DENY everything.
After CPA credential/alias resolution (AfterAuth) the resolved model M is checked again: for alias
requests M must equal the alias target. CPA v8.0.44 supplies no credential mapping provenance
through these callbacks/hooks, so a different mapped destination is conservatively denied; it
cannot safely be treated as an approved credential-level mapping. For non-alias
requests M itself must pass steps 3–5. This blocks bypass through CPA oauth-model-alias, prefixes,
and protocol notations.

Exact rules never widen automatically; family rules include new versions matching the template
(respecting include_prerelease=false by default for family rules too).

Listing: an entry is shown iff `decide(entry_id)` is ALLOW and the model is routable; published
aliases are added as entries cloned from their target's entry (id replaced), only if target routable.

Errors (OpenAI-style JSON, Claude/Gemini shapes where the protocol requires):
`403 model_not_allowed`, `404 model_not_found` for hidden names (do not reveal existence),
`503 alias_target_unavailable`, `503 policy_unavailable`. Messages never include secrets.

## 4. Catalog

Background poller (default 5 min, plus on demand) using host callbacks `host.auth.models` and
`host.auth.list`/`get_runtime`. Snapshot: models → {providers[], credential_count, routable bool}. For routable entries, providers
contains only enabled runtime advertisers; catalog-only entries retain their registered providers.
Never queried per request. On failure keep last good snapshot, record a sanitized error + attempt
timestamp. Missing, disabled, or unavailable runtime credentials are not counted as routable.
This is a conservative registration inventory, not a promise of current scheduler eligibility: it
excludes static executor-only registry clients and cannot inspect per-model cooldowns atomically.
Credential details exposed to the UI are limited to provider, label/id hash, status — never tokens,
keys, emails beyond what CPA's own UI shows (prefer omitting).

## 5. Management API (authenticated by CPA management key; exact paths, no path params)

Base: `/v0/management/plugins/cpa-model-dock`. All JSON. All mutating calls (including catalog refresh and an applying alias check) require
`"revision": <n>` in body (or `If-Match: "<n>"`) and return `409 {"error":{"code":"revision_conflict"}}`
with the current state on mismatch. Validation failures: `422 {"error":{"code":"validation_failed",
"details":[{"path":"aliases[0].name","code":"alias_conflict","message":"..."}]}}` and nothing is saved.

| Method | Path | Body | Returns |
|---|---|---|---|
| GET | `/overview` | – | plugin version, host schema, policy_enabled, whitelist_enabled, counts (public models, public aliases), active mappings, recent auto updates (5), catalog status {last_refresh_at, ok, error}, guard status, warnings[] (e.g. priority not highest, uncovered endpoints) |
| GET | `/state` | – | `{revision, settings, families, builtin_families, aliases, rules, alias_status:{id:...}}` |
| PUT | `/state` | full `{revision, settings, families, aliases, rules}` | saved state (new revision) |
| POST | `/validate` | same as PUT body | `{valid, details[], preview:{public_models[], public_aliases[]}}` (no save) |
| GET | `/catalog` | – | `{refreshed_at, ok, error, models:[{id, providers[], routable, public, reason}]}` |
| POST | `/catalog/refresh` | `{revision}` | catalog |
| POST | `/aliases/check` | `{revision?, alias_id?, dry_run?:bool}` (revision required unless dry_run) | per-alias evaluation results (candidates with version, routable, chosen, reason) ; applies change unless dry_run |
| POST | `/rules/preview` | `{rules?, settings?, aliases?}` (draft; defaults to saved) | `{rules:[{id, matches[]}], public_models[], public_aliases[]}` |
| POST | `/explain` | `{model, draft?}` | `{allowed, status, code, reason, steps[], resolved_target?}` |
| GET | `/history` | query `limit` (default 100), `before` | `{entries:[{id, at, actor:"user"|"auto", kind:"state"|"alias_target", summary, before, after}]}` |
| POST | `/history/restore` | `{entry_id, revision, scope:"state"|"alias_target"}` | saved state |

Read-only POSTs `/validate`, `/rules/preview`, `/explain` and dry-run alias checks do not require
a revision. History restore reapplies the selected entry's `after` state as a new revision;
`alias_target` scope reapplies only targets changed in that entry and does not resurrect removed aliases.

Resources (public, static only): `/v0/resource/plugins/cpa-model-dock/index.html` (Menu
`Model Dock`), `/app.js`, `/app.css`, `/i18n.js`. No data in resources.

## 6. Guard / uncovered endpoints

- Codex Alpha Search: ModelRouter receives `SourceFormat="codex-alpha-search"`. Denied names
  route to this plugin's static **denial executor** (`TargetKind="self"`), which declares only the
  `codex-alpha-search` input/output format and always denies execution. CPA validates executor
  readiness, then Alpha Search rejects this non-provider route with 503. A fabricated non-Codex
  provider sentinel is unsafe: CPA discards routes to providers without registered credentials.
  Allowed aliases route to the registered `codex` provider; unavailable or incompatible targets
  receive the denial route when the guard is enabled.
- `/v1/realtime*`, `/v1/live*`: no model hooks. With the guard and whitelist enabled, the
  nonexclusive FrontendAuthProvider denies these endpoints when invoked and returns NotHandled
  elsewhere. **This is not an effective mandatory gate on stock CPA startup with configured API
  keys:** `sdk/cliproxy/builder.go` registers config-access before plugin providers;
  `sdk/access/registry.go` preserves registration order; `sdk/access/manager.go` returns on the
  first successful authentication. Valid built-in keys can therefore bypass the guard. Realtime
  client-secret authentication also bypasses fallback providers (`internal/api/server_middleware.go`).
  Runtime integration remains to be tested; deployments requiring enforcement must block these
  paths in a reverse proxy. The plugin never claims exclusive auth or authenticates requests.
- Responses WebSocket in-flight continuation frames do not rerun ModelRouter/BeforeAuth/AfterAuth.
  Verified in `internal/runtime/executor/codex_websockets_duplex.go`: create/append reads explicit
  `model` and, when it differs from both `req.Model` and the bound requested model, returns
  `NewUpstreamWebsocketReplayRequiredError` before sending upstream. Thus an in-place model
  switch through those frames is not silently accepted. `response.steer` authorizes using the bound
  model but passes control-frame fields through a payload finalizer and leaves unknown/unsupported
  fields to upstream validation; plugin hooks cannot enforce or rewrite a per-frame model there.
  New normal executable turns use the standard hook pipeline. Strict deployments should disable
  Responses WS (`GET /v1/responses` and `/backend-api/codex/responses`) at a reverse proxy.
- Gemini `GET /v1beta/models/{id}` detail and management `api-call` do not invoke these hooks.
  Reverse-proxy restrictions are required to cover them; list filtering alone is not an access gate.
- Request hooks recover panics into successful `Terminate` decisions with protocol-shaped errors.
  ModelRouter has no denial fields; Alpha uses the denial route above. Response and stream DTOs
  have **no `Terminate` or status fields**: recovery returns a sanitized protocol error body/empty
  catalog or drops the chunk. They cannot retroactively change an HTTP status or close a socket.
  Lifecycle completion only frees state and has no client decision to return.
- Hooks pin a policy/catalog snapshot at BeforeAuth by RequestID. The router has no RequestID:
  BeforeAuth may bind the current alias target or the immediately previous alias target generation
  selected by the router, provided the alias remains enabled and published in the current policy.
  Catalog-only refreshes preserve that previous generation without extending its expiry. The bound
  snapshot is reused for all later phases, including requests admitted before a policy revocation. Pins expire after ten
  minutes of inactivity or lifecycle completion. Other plugins, final payload rules, unload/fuse,
  and routes outside this pipeline remain host/deployment trust boundaries.

## 7. Persistence & concurrency

- `state.json` written atomically: temp file in same dir → fsync → rename → fsync dir; previous copy
  kept as `state.json.bak` (last good). `history.jsonl` append-only, capped (default 1000 entries,
  compacted atomically).
- Load order: state.json → state.json.bak → if neither exists, defaults; if a file exists but both are
  corrupt → policy_unavailable (deny all) until a valid state is saved via API.
- In-memory: immutable snapshot behind atomic pointer; each request pins one snapshot at BeforeAuth
  by RequestID (released on lifecycle completion / TTL), so a request sees one consistent policy.
- Hooks never return errors for policy denials and recover panics internally → DENY (fail closed).
