# CPAModelDock — Design & API Contract (v1)

This document is the shared contract between the backend (Go plugin) and the Web UI.
Both sides MUST follow it. If an implementer finds a contradiction with CPA's real
behavior, fix the implementation to match CPA and update this document in the same change.

Research notes with source citations: `.ref/RESEARCH-routing.md`, `.ref/RESEARCH-platform.md`
(local only, not committed). Target host: CLIProxyAPI v8.0.44 (ABI 1, schema 7; negotiate down
to 6 when host advertises 6).

## 1. Packaging

- Plugin ID: `cpa-model-dock`. Library file: `cpa-model-dock.{so,dylib,dll}` (Go `-buildmode=c-shared`, cgo).
- Go module path: `github.com/suk-6/cpa-model-dock` .
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

### 3.1 Response model stream rewriting

Rewriting edits only protocol model fields: top-level `model`, Responses
`response.model`, Claude `message.model`, or Gemini `modelVersion`, including
supported JSON arrays. It never substitutes model-like text in content, tools,
IDs or instructions. Raw JSON events and SSE both support arbitrary transport
fragments, multiple events per chunk and complete events. For CPA scanner-line
delivery, complete `data:` JSON is rewritten and emitted in that same hook call,
even without a final newline or blank-line terminator. Metadata (`event:`, `id:`,
`retry:`, comments and other fields) passes immediately and unchanged; existing
LF/CRLF/CR bytes and absent endings are preserved. Recognized SSE fields at
transport boundaries identify delimiter-free scanner lines, as in CPA's framer.

Incomplete object/array JSON waits for more fragments or SSE multiline data,
subject to the 8 MiB request buffer bound. Malformed object/array JSON, including
an incomplete value terminated by a blank line, fails closed with one sanitized
protocol error and suppresses later chunks. Non-JSON text data, JSON scalars,
empty data and `[DONE]` carry no rewritable model field and pass unchanged.
Incomplete values at stream end are not leaked; completion releases their buffers.
No inference hook performs disk or host I/O.

## 4. Catalog

The last successful non-empty catalog is atomically persisted as `catalog.json` in
`state-dir`, containing only versioned model IDs, providers, credential counts,
routability and the last-good timestamp. Startup loads these facts immediately,
marked `ok:false,stale:true,warning:"using_persisted_catalog"`, while a background
refresh obtains current registration facts. Corrupt/missing caches never block
initialization or live recovery. Cached facts are registration evidence, not a
promise that CPA has already loaded the corresponding executor/credential.

An empty inventory at startup/reconfigure is not authoritative: the poller retries
after 2s, 4s, then every 8s, until the first fresh non-empty
snapshot or the configured normal interval has elapsed. Polling then uses its
normal interval. Retries and host calls never run in inference hooks or block init.
An empty successful callback result reports `ok:false,warning:"no_models"` without
cached facts; after a non-empty result it retains the last-good facts with
`stale:true,warning:"no_models_using_last_known"`. A fresh non-empty inventory
replaces all facts, including removing individual missing models, and clears stale
status. Callback failures retain facts as before. `/catalog` and overview.catalog
expose `stale`, `warning`, `warning_detail:Text|null`, and `last_good_at` separately
from the attempted `refreshed_at` / `last_refresh_at`; cache I/O warnings are safe,
localizable codes. No credential tokens, paths, auth IDs or emails are cached.

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
runtime evidence still wins. Codex API-key config clients have the same omission;
the exact `codex:apikey:<12 lowercase hex>[-positive decimal collision suffix]` ID with provider `codex`, a non-empty
auth index, and registered models supplies the equivalent evidence. No arbitrary
missing OAuth credential is accepted. Removed/disabled config clients lose registrations during CPA
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
text, not stable identifiers; clients branch on `code` and interpolate `params`; messages remain complete English
fallbacks for API consumers. Never derive translation identifiers from message text.

| Method | Path | Body/query | Returns (HTTP 200 unless specified below) |
|---|---|---|---|
| GET | `/overview` | – | `{plugin_version,host_schema,policy_enabled,whitelist_enabled,counts:{public_models,public_aliases},active_mappings:[{alias,target}],recent_auto_updates:HistoryEntry[],catalog:{last_refresh_at,last_good_at,ok,stale,warning,warning_detail:Text|null,error,error_detail:Text|null},guard:{enabled,verified,covered:Text[],limitations:Text[]},warnings:Text[]}` |
| GET | `/state` | – | `StateView` |
| PUT | `/state` | full `{revision?,settings,families,aliases,rules}` | `StateView` with new revision |
| POST | `/validate` | full PUT-shaped draft; revision ignored | `{valid,details:Detail[],preview:{public_models:string[],public_aliases:string[]}}` |
| GET | `/catalog` | – | `{refreshed_at,last_good_at,ok,stale,warning,warning_detail:Text|null,error,error_detail:Text|null,models:[{id,providers:string[],credential_count,routable,public,reason:Reason}]}` |
| POST | `/catalog/refresh` | `{}`; supplied revision ignored | same catalog shape, without revision |
| POST | `/aliases/check` | `{revision?,alias_id?:string,dry_run?:bool}` | `{results:AliasCheckResult[],revision}` |
| POST | `/rules/preview` | `{settings?,families?,aliases?,rules?}` | `{rules:[{id,matches:string[]}],public_models:string[],public_aliases:string[]}` |
| POST | `/explain` | `{model:string,draft?:{settings?,families?,aliases?,rules?}}` | `{allowed,status,code,reason:Reason,steps:Reason[],resolved_target?:string}` |
| GET | `/history` | query `limit` (default 100, integer 1–1000), `before` (exclusive entry ID) | `{entries:HistoryEntry[],next_before:string|null}` |
| POST | `/history/restore` | `{entry_id:string,revision?,scope:"state"|"alias_target",pin?:bool}` | `StateView` with new revision |

`Text` and `Reason` are exactly `{code:string,params:object,message:string}`.
`params` is always an object, including `{}` when no parameters are needed.
`Detail` is exactly `{path:string,code:string,params:object,message:string}`; paths identify editable fields, e.g.
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
entries. Catalog `error` remains `null` on success or a safe English string on failure;
`error_detail` is null on success and Text with code `catalog_unavailable` on
failure. Guard `verified` remains false, and `enabled` describes configuration,
not protection against bypass. `covered` and `limitations` are informational
Text arrays, independent of that flag. Ordinary HTTP model requests (including
streaming), model lists, and the initial Responses WebSocket request are covered.
Realtime/live, ongoing WebSocket messages, single-model detail, and the admin API
tool require reverse-proxy controls. Valid API keys and realtime client secrets
can bypass this policy on standard CPA. This API makes no protective claim for
the nonexclusive frontend guard.

`warnings` contains **only actionable** priority verification, catalog failure,
policy unavailability, and enabled aliases with unavailable targets. Coverage
limitations never populate this attention list. Priority remains unverified
until the installed-plugin order is checked on the host.
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
`alias_not_published`, `not_whitelisted`, `rule_allowed`, etc.), distinct from an
inference adapter's protocol error envelope. `rule_allowed` carries `{rule_id}` in both the reason and final step.
The `normalized` step carries `{model}` with the normalized model ID.
`steps` is an array of `Reason` objects; `resolved_target` is present only when resolved.

Only PUT state, **applying** alias checks, and history restore require the current
integer body `revision` or `If-Match: "<revision>"`. If both are supplied, both
must match the current revision. Missing, malformed, disagreeing, or stale tokens
return HTTP **409** `{error:{code:"revision_conflict",params:{},message},state:StateView}`.
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
and restores return HTTP **422** `{error:{code:"validation_failed",params:{},message,
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
`{id,at,actor,kind,summary,summary_detail:Text,before,after}`. State saves use
`actor:"user",kind:"state"`; target switches use
`actor:"auto",kind:"alias_target"`. State before/after values are policy
snapshots with schema/revision/timestamp but no runtime fields; alias-target
values are `{alias_id,target}`; a restore that also changes mode includes
`mode` in both before/after so the diff shows automatic → pinned. Restoring
such an entry still restores only its target and preserves the current mode
unless `pin:true` is requested. Unchanged checks and refreshes add no
history. Disk history may retain full policy snapshots internally; management
projects target entries into these per-alias pairs.

Restore **undoes to the selected entry's `before` value**, never its `after`.
Scope must match the entry kind. State scope restores the full policy;
alias-target scope restores only that alias's target in the current policy.
An absent alias is a 422 `validation_failed` with detail code `alias_not_found`.
Restore validates before writing, advances from the current revision, and
appends a new user entry of the restored scope without rolling revision back or
deleting history. For `alias_target`, optional `pin:true` changes target and
`mode:"pinned"` atomically in the same revision; it is invalid for `state` scope
(422 `validation_failed`, detail `invalid_restore_pin` at `pin`).
For an automatic alias, the UI explains that later checks can advance a restored
target and offers **Restore and pin to this model** or **Restore only (automatic
updates may move it again)**. Restore summaries use `alias_target_restored`
(alias, target) or `policy_restored` (historical timestamp), with English/Korean
translations, rather than presenting internal history/alias IDs as the summary.

Alias runtime evaluation status is atomically persisted separately from policy.
Unrelated saves preserve status. Created aliases and changes to evaluation inputs
(mode, target, auto settings, provider, enabled flag or referenced family
configuration) invalidate only affected statuses; changed automatic aliases are
reevaluated asynchronously after publication. Restarts restore statuses only for
matching alias IDs and evaluation inputs. Every completed non-dry-run evaluation,
including no-change refresh evaluations, records last_checked_at, candidates and
reason without adding no-op history. Paid probes remain opt-in and bounded.


`summary` remains a complete English string. `summary_detail` describes semantic
before/after changes, including for old records without stored details. It must
not report a generic policy update when a diff is available. Multiple changes
use `{code:"changes",params:{count:N,changes:Text[]},message:"N changes: …"}`;
a single change is returned directly. Array order and policy revision/timestamp
changes are not semantic changes. No-op saves use `policy_unchanged`. New disk
records persist the detail; legacy full snapshots are sufficient to derive it.
A record without usable snapshots uses `history_legacy` and `{summary}`.
Alias edits include changed field names and before/after values in structured
`alias_changed` params, with localized mode, response-name default/on/off, original
visibility and auto-setting labels. Long field lists show a concise prefix plus
the remaining count; the complete structured changes remain available.


Changes are ordered by alias ID, then rule ID, then whitelist, other settings,
and families. For an existing alias, emit rename, enable/disable, target change,
and finally `alias_changed` only when other fields differ. Creation/deletion is
one event. Rule enable/disable is followed by `rule_changed` if kind, value, or
note also differs. Automatic changes use `alias_target_auto_updated`, while user
edits use `alias_target_changed` (including a user changing a pinned target).
New restores use `alias_target_restored` or `policy_restored` with human-readable
alias/model or historical time parameters. Legacy `restored` entries remain
readable. Each projected alias target entry has only that alias's summary, while
its before/after remain compact.

The deterministic text code/parameter inventory is below. Parameter names in
braces denote object keys; parameters are strings unless noted. Unlisted
parameter sets are `{}`. The same code has the same meaning across endpoints;
clients may fall back to `message` for an unknown code.

| Context | Codes | Params |
|---|---|---|
| Attention | `priority_unverified`, `catalog_unavailable`, `policy_unavailable` | `{}` |
| Catalog readiness/cache | `no_models`, `no_models_using_last_known`, `using_persisted_catalog`, `catalog_unavailable_using_last_known`, `catalog_cache_unavailable` | `{}` |
| Attention alias | `alias_target_unavailable` | `{alias_id,alias,target}` in overview; `{}` for a policy decision |
| Covered | `http_model_requests_covered`, `model_lists_covered`, `responses_websocket_initial_covered` | `{}` |
| Proxy controls | `realtime_proxy_required`, `websocket_messages_proxy_required`, `model_detail_proxy_required`, `admin_api_tool_proxy_required` | `{}` |
| Alias history | `alias_created` | `{alias_id,alias,target}` |
| Alias history | `alias_renamed` | `{alias_id,from,to}` (names) |
| Alias history | `alias_deleted`, `alias_enabled`, `alias_disabled` | `{alias_id,alias}` |
| Alias field history | `alias_changed` | `{alias_id,alias,count:number,fields:[{field,from,to}]}` (typed null/bool/string values) |
| Target history | `alias_target_changed`, `alias_target_auto_updated` | `{alias_id,alias,from,to}` (model IDs) |
| Rule history | `rule_added`, `rule_removed`, `rule_enabled`, `rule_disabled`, `rule_changed` | `{rule_id,kind,value}` (removed uses old values; others use new) |
| Policy history | `whitelist_enabled`, `whitelist_disabled`, `settings_changed`, `families_changed`, `policy_unchanged` | `{}` |
| Alias restore history | `alias_target_restored` | `{alias_id,alias,target,pinned:bool}` |
| Policy restore history | `policy_restored` | `{at}` (RFC3339 historical time) |
| Legacy restore history | `restored` | `{entry_id}` |
| Multiple changes | `changes` | `{count:number,changes:Text[]}` |
| Legacy history | `history_legacy` | `{summary}` |
| Explain step | `normalized`, `resolved_model` | `{model}` |
| Policy reason | `rule_allowed` | `{rule_id}` |
| Policy reason | `alias` | `{alias,target}` |
| Policy reasons | `alias_not_published`, `hidden_by_alias`, `whitelist_disabled`, `not_whitelisted`, `policy_unavailable`, `not_routable` | `{}` |
| Alias check/status | `newer_version` | `{from,to}` (previous and proposed/current model IDs) |
| Alias check/status | `not_checked`, `up_to_date`, `ambiguous`, `current_unparseable`, `no_routable_candidates`, `catalog_unavailable`, `pinned`, `disabled`, `policy_unavailable`, `cancelled`, `unknown_family`, `invalid_family`, `health_check_unverified`, `health_check_failed` | `{}` |
| Candidate reason | `candidate`, `selected`, `not_routable`, `alias_conflict` | `{}` |

Every validation detail includes `params.field` equal to its complete `path`,
including nested settings and auto fields. Context-specific validation codes
preserve semantic differences that must not be collapsed into `required` or
`invalid_scope`:

| Validation codes | Meaning / additional params beyond `{field}` |
|---|---|
| `required_id`, `required_family_name`, `required_alias_name`, `required_alias_target`, `required_rule_value`, `required_model`, `required_entry_id` | The named value is required |
| `required_revision`, `required_full_state_field`, `required_setting` | A concurrency token, full replacement section, or full replacement setting is required, respectively |
| `invalid_type` | `{expected}` is `string`, `boolean`, `integer`, `object`, `array_of_objects`, or `non_null` |
| `invalid_revision_body`, `invalid_revision_header`, `revision_disagreement` | Nonnegative integer body token, quoted single header token, or disagreement between tokens; mutation token failures still return the revision conflict envelope |
| `alias_name_conflict`, `alias_catalog_conflict` | Collision with another alias or with a real catalog model, respectively |
| `invalid_alias_slash` | Alias name contains a slash |
| `invalid_template_tokens`, `invalid_template_literal`, `invalid_template_expression` | Invalid token counts, literal/adjacent tokens, or expression, respectively |
| `invalid_restore_scope`, `restore_scope_mismatch`, `invalid_restore_pin` | Invalid scope name or scope not matching the selected entry |
| `unsupported_schema`, `invalid_revision`, `invalid_refresh_interval`, `invalid_id`, `duplicate_id`, `builtin_read_only`, `family_conflict`, `invalid_provider`, `invalid_alias_name`, `invalid_mode`, `invalid_target`, `unknown_target`, `unknown_family`, `provider_conflict`, `alias_target_alias`, `invalid_value`, `invalid_exact`, `invalid_pattern`, `unknown_alias`, `invalid_rule_kind`, `id_unavailable`, `alias_not_found`, `invalid_state` | Domain error identified by code and complete field path |

Error envelopes also include `params:{}`. Parse errors use `invalid_json` with
`{field:"body"}` and oversized bodies use `body_too_large` with
`{field:"body",max_bytes:1048576}`. Validation ordering is deterministic: sections
and array positions follow parsing order; editable keys within each object are
visited lexically. Domain validation retains field-check order.

Resources (public, static only): `/v0/resource/plugins/cpa-model-dock/index.html`
(Menu `Model Dock`), `/app.js`, `/app.css`, `/i18n.js`, `/favicon.svg` (MIME `image/svg+xml`). Only these five exact
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
