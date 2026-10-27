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
Optional calendar suffixes are recognized before parsing numeric versions, including when
`allow_dated:false`; a dashed version cannot absorb a disabled, invalid, or ambiguous date suffix.
A valid eight-digit calendar date is never a version component (including dotted/internal components).
An explicit `{date}` token remains authoritative even when optional dates are disabled. Optional
short dates are rejected when a template ending in `{version}` makes their interpretation ambiguous.
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

Listing: an entry CPA itself supplies is shown iff `decide(entry_id)` is ALLOW, even when the
callback catalog is empty, unknown, stale, or omits that entry; published
aliases are added as entries cloned from their target's entry (id replaced), only if target routable.

Errors (OpenAI-style JSON, Claude/Gemini shapes where the protocol requires):
`403 model_not_allowed`, `404 model_not_found` for hidden names (do not reveal existence),
`503 alias_target_unavailable`, `503 policy_unavailable`. Messages never include secrets.

## 4. Catalog

Background poller (default 5 min, plus on demand) using host callbacks `host.auth.models` and
`host.auth.list`/`get_runtime`. Snapshot: models → {providers[], credential_count, routable bool}. For routable entries, providers
contains only enabled runtime advertisers; catalog-only entries retain their registered providers.
Never queried per request. On failure keep last good snapshot, record a sanitized error + attempt
timestamp. Disabled or unavailable runtime credentials are not counted as routable. Missing
runtime credentials remain unconfirmed, with one source-verified CPA v8.0.44 exception:
config-synthesized OpenAI-compatible clients have IDs `openai-compatibility:<name>:<12 hex>`
and a provider matching CPA's `OpenAICompatibleProviderKey(name)` (normally
`openai-compatible-<name>`, preserving already-prefixed names and `openai-compatibility`), and are omitted by `auth.list/get_runtime`
because they have neither a file path nor `runtime_only`. Their positive per-client model
registrations in `auth.models` count as cached routability evidence; explicit disabled/unavailable
runtime evidence still wins. Removed/disabled config clients lose registrations during CPA
reconciliation. No callback snapshot guarantees request-time health or eligibility. Sources:
`internal/watcher/synthesizer/config.go:synthesizeOpenAICompat`,
`internal/pluginhost/auth_callbacks.go:buildHostAuthFileEntry/buildHostAuthModels`,
`sdk/cliproxy/service_auth.go:applyAuthUpdate` in the CPA checkout.
This is a conservative registration inventory, not a promise of current scheduler eligibility: it
excludes static executor-only registry clients and cannot inspect per-model cooldowns atomically.
Credential details exposed to the UI are limited to provider, label/id hash, status — never tokens,
keys, emails beyond what CPA's own UI shows (prefer omitting).

## 5. Management API (authenticated by CPA management key; exact paths, no path params)

Base: `/v0/management/plugins/cpa-model-dock`. CPA authenticates every management
route with its management key, including previews. Requests and responses are JSON.
The shapes below are the wire contract; internal policy/evaluator structs are not
response DTOs. All arrays are arrays, including when empty; object maps are `{}`
when empty. Timestamps are ISO 8601 UTC. Human-readable messages are safe display
text, not stable identifiers; clients branch on `code`.

| Method | Path | Body/query | Returns (HTTP 200 unless specified below) |
|---|---|---|---|
| GET | `/overview` | – | `{plugin_version,host_schema,policy_enabled,whitelist_enabled,counts:{public_models,public_aliases},active_mappings:[{alias,target}],recent_auto_updates:HistoryEntry[],catalog:{last_refresh_at,ok,error},guard:{enabled,verified,limitations:string[]},warnings:string[]}` |
| GET | `/state` | – | `StateView` |
| PUT | `/state` | full `{revision?,settings,families,aliases,rules}` | `StateView` with new revision |
| POST | `/validate` | full PUT-shaped draft; revision ignored | `{valid,details:Detail[],preview:{public_models:string[],public_aliases:string[]}}` |
| GET | `/catalog` | – | `{refreshed_at,ok,error,models:[{id,providers:string[],credential_count,routable,public,reason:Reason}]}` |
| POST | `/catalog/refresh` | `{}`; supplied revision ignored | same catalog shape, without revision |
| POST | `/aliases/check` | `{revision?,alias_id?:string,dry_run?:bool}` | `{results:AliasCheckResult[],revision}` |
| POST | `/rules/preview` | `{settings?,families?,aliases?,rules?}` | `{rules:[{id,matches:string[]}],public_models:string[],public_aliases:string[]}` |
| POST | `/explain` | `{model:string,draft?:{settings?,families?,aliases?,rules?}}` | `{allowed,status,code,reason:string,steps:Reason[],resolved_target?:string}` |
| GET | `/history` | query `limit` (default 100, integer 1–1000), `before` (exclusive entry ID) | `{entries:HistoryEntry[],next_before:string|null}` |
| POST | `/history/restore` | `{entry_id:string,revision?,scope:"state"|"alias_target"}` | `StateView` with new revision |

`Reason` is exactly `{code:string,message:string}`. `Detail` is exactly
`{path:string,code:string,message:string}`; paths identify editable fields, e.g.
`aliases[0].name`, `aliases[0].auto.family`, `families[0].template`, `rules[0].value`.

`StateView` contains exactly `{schema,revision,updated_at,settings,families,
builtin_families,aliases,rules,alias_status}`. The editable policy follows §2;
`builtin_families` is a read-only array. `alias_status` is keyed by current alias
ID. Each value contains exactly `{current_target,candidates,last_checked_at,
last_result,reason:Reason,target_routable}`. A never-checked alias has
`last_checked_at:null`, `last_result:"kept"`, `reason.code:"not_checked"`, and
empty candidates. Saves/restores return the same complete shape as GET.

`AliasCheckResult` contains exactly `{alias_id,current_target,candidates,
last_checked_at,last_result,reason:Reason}`. Candidates contain exactly
`{id,version,routable,chosen,reason:Reason}`. `version` preserves parsed dots and
dashes. Whole-family candidates include catalog-only models with `routable:false`;
excluded prereleases are omitted. Selection uses `reason.code:"selected"`;
result codes include `newer_version`, `up_to_date`, `ambiguous`,
`current_unparseable`, `no_routable_candidates`, `catalog_unavailable`, `pinned`,
and `disabled`. Omitting `alias_id` (or supplying an empty string) checks all
aliases, including disabled/pinned aliases, which are kept with explicit reasons.
Unknown IDs return 404 `alias_not_found`. Applying checks cache each result
without `alias_id`, adding `target_routable` in state. A dry-run reports the
**proposed** `current_target`; `updated` means **would update**. Dry-run never
persists targets, check timestamps, runtime status, or history. Paid probes cannot
run in a dry-run; an unverified probe keeps the target (`health_check_unverified`).
The development mock cannot perform paid probes even during applying checks.

Overview counts separate public real models and aliases. `active_mappings`
contains all enabled configured aliases, including unpublished or currently
unavailable targets, and is not routing verification. `recent_auto_updates`
contains up to five complete newest `actor:"auto",kind:"alias_target"` history
entries. Catalog errors are `null` on success or a safe string on failure.
Guard `verified` remains false; `limitations` and `warnings` are arrays of strings.
Catalog visibility uses the policy decision and routability; catalog-only models
have `public:false` and `reason.code:"not_routable"`.

Preview/explain merge partial draft settings over saved settings and replace each
supplied array. Custom `families` are accepted. Omitted draft fields use saved
values. An explicit valid draft can preview recovery from unavailable policy;
explain without a draft uses saved policy availability. All public/match arrays
contain **ID strings**, sorted lexically. Individual rule matches include hidden
and catalog-only models even when final public arrays exclude them; alias rules
match configured alias names. Disabled rules have empty match arrays. Explain's
`status` simulates inference (200/403/404/503); the management response remains
HTTP 200. Its `code` describes the policy decision (`hidden_by_alias`,
`alias_not_published`, `not_whitelisted`, `rule:<id>`, etc.), distinct from an
inference adapter's protocol error envelope. `steps` is an array of `Reason`
objects, not strings; `resolved_target` is present only when resolved.

Only PUT state, **applying** alias checks, and history restore require the current
integer body `revision` or `If-Match: "<revision>"`. If both are supplied, both
must match the current revision. Missing, malformed, disagreeing, or stale tokens
return HTTP **409** `{error:{code:"revision_conflict",message},state:StateView}`.
An applying check advances policy revision exactly once **only if at least one
target changes**; unchanged checks may update runtime status but do not change
policy timestamps or history. State saves (including no-op saves) and successful
restores advance revision once. Checks of several aliases share one policy
revision, with a separate target history entry per changed alias.

Catalog refresh, validate, preview, explain, and dry-run require no revision and
ignore supplied body/header tokens. **Refresh never changes policy revision,
policy timestamp, alias targets/runtime status, or history**, even on failure.
A failed refresh returns HTTP 200 `ok:false`, keeps last-good models, and updates
catalog attempt status. Concurrent publication preserves the latest policy;
catalog refreshes cannot overwrite a concurrent state save. Scheduled automatic
alias checks are independent policy mutations and use the applying-check rules.

Malformed/non-object JSON returns 400 `invalid_json`; an empty body is `{}`.
Bodies over 1 MiB return 413 `body_too_large`. Invalid saves, previews, explains,
and restores return HTTP **422** `{error:{code:"validation_failed",message,
details:Detail[]}}`, without saving. `/validate` instead returns HTTP **200**
`valid:false` with details and empty preview arrays. Unknown routes return 404
`not_found`; unsupported methods on known paths return 405 `method_not_allowed`
with `Allow`. Missing history entries return 404 `entry_not_found`. Missing
management dependencies return 503 `policy_unavailable`; operation failures
return sanitized 500 errors.

Save projection discards runtime fields, per-alias `status`, unknown fields,
and client schema/timestamps. The server supplies schema/revision/timestamp and
built-ins. Stable supplied IDs are accepted and must be unique across the policy;
omitted family/alias/rule IDs are generated as `fam_<UUID>`, `al_<UUID>`,
`r_<UUID>`. Read-only draft IDs are temporary. Clients creating a family and its
references together should supply that family's stable ID. Built-in IDs are
reserved for custom families. Full settings require both booleans and an integer refresh interval
of 0 (host default) or >=30. Alias/rule `enabled` defaults true,
`expose_original` false, rewrite override null, provider empty, auto flags false,
and family `allow_dated` true. Alias names cannot contain whitespace, slash,
wildcards, or thinking suffixes and cannot collide case-insensitively with a
catalog ID or another alias. Missing family/alias rule references are invalid,
even when disabled. Unknown/unroutable targets may be saved so offline aliases
remain repairable; requests through them fail 503 without fallback.

History is newest first, capped at 1000 exposed entries. `before` is an exclusive
**entry ID**, not a timestamp or revision; clients treat IDs as opaque strings.
`next_before` is the last returned ID when more entries remain, otherwise null.
Unknown cursors, duplicate cursor/limit parameters, and invalid limits return
400 `invalid_cursor`/`invalid_limit`. `HistoryEntry` is exactly
`{id,at,actor,kind,summary,before,after}`. State saves use
`actor:"user",kind:"state"`; target switches use
`actor:"auto",kind:"alias_target"`. State before/after values are policy
snapshots with schema/revision/timestamp but no runtime fields; alias-target
values are exactly `{alias_id,target}`. Unchanged checks and refreshes add no
history. Disk history may retain full policy snapshots internally; management
projects target entries into these per-alias pairs.

Restore **undoes to the selected entry's `before` value**, never its `after`.
Scope must match the entry kind. State scope restores the full policy;
alias-target scope restores only that alias's target in the current policy.
An absent alias is a 422 `validation_failed` with detail code `alias_not_found`.
Restore validates before writing, advances from the current revision, and
appends a new user entry of the restored scope without rolling revision back or
deleting history. State saves/restores clear all alias runtime status;
alias-target restore clears only the restored alias's status.

Resources (public, static only): `/v0/resource/plugins/cpa-model-dock/index.html`
(Menu `Model Dock`), `/app.js`, `/app.css`, `/i18n.js`. Only these four exact
filenames are served; GET and HEAD are supported. No management data is public.

## 6. Guard / uncovered endpoints

- Codex Alpha Search: ModelRouter receives `SourceFormat="codex-alpha-search"`.
  With the guard enabled, Alpha is denied whenever whitelist rules or alias target hiding
  require final-model authorization. CPA's `auth.models` exposes public IDs without their
  credential mapping provenance, so even an exact whitelisted ID cannot be verified as a raw
  upstream destination. CPAModelDock aliases are always denied on Alpha, including when the
  guard is off: the router changes credential selection only; OAuth sends the original body
  model unchanged, while API-key credentials resolve private mappings after selection without
  AfterAuth (`internal/api/server_routes.go:251-291`, `:464-478`). No plugin callback can safely
  rewrite/authorize that final body. An unrestricted default policy passes native Alpha through.
  Denials route to this plugin's static **denial executor** (`TargetKind="self"`); Alpha rejects
  this unsupported non-provider route with HTTP 503 naming `cpa-model-dock`, before transport.
  The route reason distinguishes `alpha_alias_unsupported`/`alpha_destination_unverifiable`
  internally; CPA's Alpha error DTO does not expose that reason. Ordinary unavailable aliases
  also use this executor route to reach BeforeAuth (otherwise CPA's earlier provider lookup
  would return generic 400); they return 503 `alias_target_unavailable`. The executor declares
  Alpha and the ordinary OpenAI/Responses/Claude/Gemini formats and never forwards a request.
  Both Alpha paths, exact native/prefix/raw rules, and Dock aliases are exercised on real CPA.
- `/v1/realtime*`, `/v1/live*`: no model hooks. With the guard and whitelist enabled, the
  nonexclusive FrontendAuthProvider denies these endpoints when invoked and returns NotHandled
  elsewhere. **This is not an effective mandatory gate on stock CPA startup with configured API
  keys:** `sdk/cliproxy/builder.go` registers config-access before plugin providers;
  `sdk/access/registry.go` preserves registration order; `sdk/access/manager.go` returns on the
  first successful authentication. Valid built-in keys can therefore bypass the guard. Realtime
  client-secret authentication also bypasses fallback providers (`internal/api/server_middleware.go`).
  Real-CPA tests with valid built-in keys observe no guard denial (503 auth_not_found both
  before and after enabling whitelist; no Codex credentials in the fixture). Deployments
  requiring enforcement must block these paths in a reverse proxy. The plugin never claims exclusive auth or authenticates requests.
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
  Real CPA returns HTTP 200 with the hidden model name on Gemini detail GET. Reverse-proxy
  restrictions are required to cover these paths; list filtering alone is not an access gate.
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
  compacted atomically). Primary rename is the commit point: a later directory open/fsync/close
  error cannot roll it back. Memory and history retain the committed revision and the save returns
  success; crash durability is unconfirmed in that case. Pre-replacement failures leave policy intact.
- Load order: state.json → state.json.bak → if neither exists, defaults; if a file exists but both are
  corrupt → policy_unavailable (deny all) until a valid state is saved via API.
- In-memory: immutable snapshot behind atomic pointer; each request pins one snapshot at BeforeAuth
  by RequestID (released on lifecycle completion / TTL), so a request sees one consistent policy.
- Catalog publication has a monotonic generation for completed successful and failed refreshes.
  Delayed delivery cannot overwrite a newer generation. Applying alias checks pin both revision
  and catalog generation, re-check them after probes, and fence publication through the commit.
  A target withdrawn during a probe cannot be persisted from the older snapshot.
- Health probes run outside the policy mutation mutex with a five-second wait bound and lifecycle
  cancellation. Shutdown/reconfigure cancel management checks before taking the runtime write
  lock. Synchronous host callbacks may continue after cancellation; one process-wide pending
  health probe (and a separate inventory slot) bounds abandoned workers across replacements.
  No subsequent probe starts until that slot returns. This does not cancel upstream host I/O.
- Hooks never return errors for policy denials and recover panics internally → DENY (fail closed).
