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

`state-dir` empty → fallback documented by backend (e.g. `<auth-dir>/../cpa-model-dock-data` if the host
exposes auth-dir, else `$HOME/.cli-proxy-api/cpa-model-dock`). All other user policy lives in the
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
ONLY when `p` is a prefix of a known credential (from catalog). Unknown `x/y` spellings are matched
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
requests M must equal the alias target (or a CPA credential-level mapping of it); for non-alias
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
`host.auth.list`/`get_runtime`. Snapshot: models → {providers[], credential_count, routable bool}.
Never queried per request. On failure keep last good snapshot, record error + timestamp.
Credential details exposed to the UI are limited to provider, label/id hash, status — never tokens,
keys, emails beyond what CPA's own UI shows (prefer omitting).

## 5. Management API (authenticated by CPA management key; exact paths, no path params)

Base: `/v0/management/plugins/cpa-model-dock`. All JSON. All mutating calls require
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
| POST | `/catalog/refresh` | `{}` | catalog |
| POST | `/aliases/check` | `{alias_id?, dry_run?:bool}` | per-alias evaluation results (candidates with version, routable, chosen, reason) ; applies change unless dry_run |
| POST | `/rules/preview` | `{rules?, settings?, aliases?}` (draft; defaults to saved) | `{rules:[{id, matches[]}], public_models[], public_aliases[]}` |
| POST | `/explain` | `{model, draft?}` | `{allowed, status, code, reason, steps[], resolved_target?}` |
| GET | `/history` | query `limit` (default 100), `before` | `{entries:[{id, at, actor:"user"|"auto", kind:"state"|"alias_target", summary, before, after}]}` |
| POST | `/history/restore` | `{entry_id, revision, scope:"state"|"alias_target"}` | saved state |

Resources (public, static only): `/v0/resource/plugins/cpa-model-dock/index.html` (Menu
`Model Dock`), `/app.js`, `/app.css`, `/i18n.js`. No data in resources.

## 6. Guard / uncovered endpoints

- Codex Alpha Search: ModelRouter receives `SourceFormat="codex-alpha-search"`; for a denied model the
  router returns a handled provider target that is not `codex` → host answers 503. Alias names route to codex.
- `/v1/realtime*`, `/v1/live*`: no model hooks. With `guard-uncovered-endpoints` and whitelist on, a
  FrontendAuthProvider denies these paths (only effective if CPA evaluates plugin providers before the
  built-in key provider — must be verified on a real instance; UI/README state verified result).
- Responses WebSocket continuation frames, Gemini `GET /v1beta/models/{id}` detail, management
  `api-call`: not interceptable; documented with reverse-proxy mitigation.

## 7. Persistence & concurrency

- `state.json` written atomically: temp file in same dir → fsync → rename → fsync dir; previous copy
  kept as `state.json.bak` (last good). `history.jsonl` append-only, capped (default 1000 entries,
  compacted atomically).
- Load order: state.json → state.json.bak → if neither exists, defaults; if a file exists but both are
  corrupt → policy_unavailable (deny all) until a valid state is saved via API.
- In-memory: immutable snapshot behind atomic pointer; each request pins one snapshot at BeforeAuth
  by RequestID (released on lifecycle completion / TTL), so a request sees one consistent policy.
- Hooks never return errors for policy denials and recover panics internally → DENY (fail closed).
