# Model Dock development mock

Requires Node.js **24**. It uses only Node built-ins; no install, bundler, dependency
sources, or `node_modules` are needed. From the repository root:

```sh
node web/dev/mock-server.mjs
# Optional alternate loopback port:
PORT=8790 node web/dev/mock-server.mjs

node --test web/dev/tests/*.test.mjs
```

Open <http://127.0.0.1:8787/v0/resource/plugins/cpa-model-dock/index.html> and enter
management key **`test-key`**. The CLI binds only `127.0.0.1`, defaults to port 8787,
and resets all state on restart. Integration tests use ephemeral loopback ports;
an execution sandbox must permit local listeners.

The only public static paths are the four exact filenames `index.html`, `app.js`,
`app.css`, and `i18n.js`, under `/v0/resource/plugins/cpa-model-dock/`. The directory
itself has no implicit index redirect. `GET` and `HEAD` are supported. No source,
state, arbitrary asset, or dependency directories are exposed. Static content is
read directly from `web/assets` without transformation. Everything under the API
base `/v0/management/plugins/cpa-model-dock` requires exactly
`Authorization: Bearer test-key`, including preview and validation requests.

## Browser UI

The four static assets run directly, with no build step. Overview reports the
saved policy; Aliases and Whitelist edit a local draft. Save validates before
writing, and Discard asks before dropping edits. Alias checks preview first and
apply only on confirmation. Renaming aliases updates their alias-rule references;
deleting an alias explicitly confirms deleting dependent allow rules as well. Checks and history restore require a clean draft.
Catalog refresh keeps draft edits and refetches state for the current revision;
a changed server policy opens the same conflict dialog as a stale save.

English is the default. Korean is selectable and only the language preference
is written to localStorage (`cpa-model-dock.lang`). The management center's
`cli-proxy-auth` and legacy `managementKey` storage are read only, using its exact
UTF-8/XOR/base64 decoder. An explicitly remembered per-tab override takes
precedence; it uses only `sessionStorage['cpa-model-dock.tab-key']`. Unchecked,
a manually entered key stays in memory. Keys never appear in URLs or logs, and
known-key text is redacted from rendered API errors/data. The API base always
resolves against `location.origin`; stored `apiBase` never redirects requests.
Storage failures fall back to a password input. An expired key can be replaced
without losing the draft. An invalid key is not automatically retried.

The layout follows `prefers-color-scheme` and fits an iframe or narrow screen.
Tabs support Left/Right/Home/End; dialogs trap focus natively, close with Escape,
and return focus. Validation summary links open the relevant editor and focus
the exact field. Custom-family examples are illustrative whole-name matches;
the server remains authoritative for ambiguous short dates and version ordering.

## Server factory and fixtures

```js
import { createMockServer } from './web/dev/mock-server.mjs';
const server = createMockServer(); // http.Server; not listening yet
server.listen(0, '127.0.0.1');
// Close the server when finished. Each factory call owns isolated state.
```

Imports never autostart. Optional factory test hooks are `assetsDir`, `catalog`
(model array), `state` (trusted initial policy), `now` (function returning a Date
or date-compatible value), `credentialPrefixes` (known credential prefix strings),
`policyAvailable` (default true; false simulates no last-good policy), and
`refreshCatalog` (sync/async function receiving a cloned catalog and returning a
replacement array). The test hook inputs are trusted, not management API inputs.
Exports also include `API_BASE`, `RESOURCE_BASE`, and `SAMPLE_MODELS`.

Fixtures are fictional development data, not claims about live provider models.
They include all nine required built-in families and the exact models
`gpt-6-astra`, `gpt-6-luna`, `claude-sonnet-4-5`, and `gemini-3-pro-preview`, plus
GPT Sol versions 6.1, 6.9, 6.10, a newer prerelease, a catalog-only 6.12, mini
variants, dated Claude models, and Gemini Pro/Flash versions. Routable models
have one simulated credential; catalog-only models have zero. Providers are
`codex`, `openai`, `anthropic`, and `gemini`; known credential prefixes default
to these names plus `p`. No credential secrets are stored or returned.

Initial revision is 1, whitelist is disabled, and the aliases are `gpt-sol`
(automatic, initially `gpt-6.1-sol`, original hidden) and `opus` (pinned,
`claude-opus-4-5-20251101`, original exposed). Seed rules allow the GPT Sol family
and publish `gpt-sol` when whitelist mode is enabled.

## Exact response shapes for backend alignment

The source of truth remains `docs/design/contract.md`. These concrete choices
resolve fields that §5 leaves descriptive or unspecified; backend implementers
should align these shapes or coordinate a UI change.

`GET /overview` returns exactly this structure; timestamps are ISO 8601 UTC:

```js
{
  plugin_version: '0.1.0-mock',
  host_schema: 7,
  policy_enabled: true, // false only in the unavailable-policy test scenario
  whitelist_enabled: false,
  counts: { public_models: 0, public_aliases: 0 }, // actual computed counts
  active_mappings: [{ alias: 'gpt-sol', target: 'gpt-6.1-sol' }],
  recent_auto_updates: [], // up to 5 complete auto/alias_target history entries
  catalog: { last_refresh_at: '...Z', ok: true, error: null },
  guard: { enabled: true, verified: false, limitations: ['...'] },
  warnings: ['...']
}
```

Counts separate real models and aliases. `active_mappings` contains enabled
configured aliases, including an alias whose target is currently unavailable;
it is not a claim that routing was exercised successfully. `recent_auto_updates`
uses the history entry shape described below, rather than a second summary schema.
`warnings` and guard `limitations` are arrays of strings. Catalog errors are
either `null` or a safe string.

`GET /state` and successful `PUT /state` / `POST /history/restore` return:

```js
{
  schema: 1, revision: 1, updated_at: '...Z',
  settings: { whitelist_enabled: false, rewrite_response_model: false,
              refresh_interval_seconds: 300 },
  families: [], builtin_families: [], aliases: [], rules: [],
  alias_status: {
    al_sol: { current_target: 'gpt-6.1-sol', candidates: [],
      last_checked_at: null, last_result: 'kept',
      reason: { code: 'not_checked', message: '...' }, target_routable: true }
  }
}
```

Families, aliases, and rules follow §2. Runtime `builtin_families`, `alias_status`,
per-alias `status`, and unknown fields are discarded when saving. The server
projects only editable fields and supplies its own schema, revision, timestamp,
and built-ins. IDs may be omitted when creating a family, alias, or rule; the
server assigns `fam_<UUID>`, `al_<UUID>`, or `r_<UUID>`. Supplied stable IDs are
accepted and must be unique. Clients creating a family and references in one
request should assign its stable `fam_...` ID themselves. Built-in family IDs
cannot be used for custom families. Missing IDs in read-only drafts are temporary
and do not persist. An unknown/unroutable alias target may be saved so an offline
target remains visible and repairable; requests through that alias return an
explanation with status 503, never a fallback target.

`POST /aliases/check` returns:

```js
{
  results: [{
    alias_id: 'al_sol',
    current_target: 'gpt-6.10-sol',
    candidates: [{ id: 'gpt-6.10-sol', version: '6.10', routable: true,
      chosen: true, reason: { code: 'selected', message: '...' } }],
    last_checked_at: '...Z',
    last_result: 'updated', // updated | unchanged | kept
    reason: { code: 'newer_version', message: '...' }
  }],
  revision: 2
}
```

`alias_id` omission checks every alias, including disabled or pinned aliases
(which are kept with an explicit reason). Unknown IDs return 404. Each reason
inside this response, `alias_status`, or a catalog entry is `{code,message}`.
Candidate `version` is the parsed version text, preserving dots/dashes. Candidates
are whole-family matches and include catalog-only models with `routable:false`;
excluded prereleases are omitted. A dry-run has the same shape: `current_target`
is the **proposed** target and `updated` means **would update**. Its revision
remains unchanged and it never updates saved targets, timestamps, runtime status,
or history. An applying check stores each result in `alias_status[alias_id]`
without `alias_id` and adds `target_routable`. The check response itself does
not add `target_routable`.

Other §5 responses:

| Endpoint | Concrete response |
| --- | --- |
| `GET /catalog`, `POST /catalog/refresh` | `{refreshed_at,ok,error,models:[{id,providers,credential_count,routable,public,reason:{code,message}}]}` |
| `POST /validate` | `{valid,details:[{path,code,message}],preview:{public_models:[string],public_aliases:[string]}}` |
| `POST /rules/preview` | `{rules:[{id,matches:[string]}],public_models:[string],public_aliases:[string]}` |
| `POST /explain` | `{allowed,status,code,reason:string,steps:[{code,message}],resolved_target?:string}` |
| `GET /history` | `{entries:[{id,at,actor,kind,summary,before,after}],next_before:string|null}` |

Public arrays are model/alias **ID strings**, not catalog objects. Rule match
arrays include matching catalog-only or hidden models, even when the final
public arrays exclude them; alias rules match alias names. Disabled rules
have empty match arrays. Explain `status` is the simulated inference status
(200/403/404/503), while the management request itself succeeds with HTTP 200.
`code`/`reason` describe the policy decision (`hidden_by_alias`,
`alias_not_published`, `not_whitelisted`, `rule:<id>`, etc.); these are distinct
from the protocol error codes an actual inference adapter would return.

## Concurrency, drafts, validation, and restore assumptions

- `PUT /state`, applying `POST /aliases/check`, `POST /catalog/refresh`, and
  `POST /history/restore` require the current integer body `revision` or
  `If-Match: "<revision>"`. If both are sent, both must match. Missing/stale
  revisions return HTTP 409 `{error:{code:'revision_conflict',message},state}`
  where `state` is the complete current GET-state response. Each successful
  mutation advances revision exactly once, including unchanged alias checks,
  no-op state saves, and failed catalog refresh attempts that record status.
- Catalog refresh requires revision despite the §5 table's `{}` example.
  Its response follows the catalog shape and has **no revision field**; refetch
  `/state` before the next mutation. A failed refresh returns HTTP 200 with
  `ok:false` and preserves the last good models. Async refresh callbacks recheck
  revision before committing, so concurrent saves cannot be overwritten.
- Preview, validate, explain, and `dry_run:true` need no revision and ignore a
  supplied one. They never change saved state or history. Preview/explain merge
  partial draft settings over saved settings, replace supplied arrays, and
  accept custom `families` in drafts as well as the table's listed fields.
  `/validate` accepts a full PUT-shaped draft.
- The contract's general 422 rule and `/validate`'s `{valid,details,preview}`
  shape overlap: this mock chooses HTTP **200 with `valid:false`** for an invalid
  `/validate` draft, with empty preview arrays. Invalid saves, previews, explains,
  and restores use HTTP 422 `{error:{code:'validation_failed',message,details}}`.
  Errors contain precise paths such as `aliases[0].name`,
  `aliases[0].auto.family`, `families[0].template`, or `rules[0].value`.
  Malformed/non-object JSON returns 400; JSON request bodies are capped at 1 MiB.
- Settings require both boolean settings and a refresh interval of 0 (host
  default) or an integer >=30. Alias/rule `enabled` defaults to true, alias
  `expose_original` to false, response rewrite to null, optional provider to
  empty, auto flags to false, and family `allow_dated` to true. Alias names
  cannot contain whitespace, `/`, or thinking suffixes and cannot collide
  case-insensitively with real catalog IDs or another alias. Missing family or
  alias rule references are validation failures. Disabled references must still
  be valid so re-enabling is safe.
- History is newest first, capped at 1000 entries; `limit` defaults to 100
  (allowed 1–1000). `before` is an **exclusive history entry ID**, not a timestamp
  or revision. `next_before` is the last returned ID if more entries remain,
  otherwise null. Unknown cursors and invalid limits return 400.
- State saves record `actor:'user',kind:'state'`; actual target switches record
  `actor:'auto',kind:'alias_target'`. Unchanged checks and catalog refreshes do
  not add history entries. State history before/after values are editable
  policy snapshots plus schema/revision/timestamp, without runtime fields.
  Alias-target before/after values are exactly `{alias_id,target}`.
- Restore **undoes to the entry's `before` value**, never its `after`. `scope`
  must match the selected entry's kind. State scope restores the complete
  policy; alias-target scope restores only that alias's target in the current
  policy. An absent alias is a 422 failure. Restore advances from the current
  revision and appends a new user history entry of the restored scope; it never
  rolls revision backward or deletes history. State saves/restores clear alias
  runtime status; alias-target restore clears only that alias's runtime status.

## Policy behavior and deliberate limits

The mock uses the same policy decision function for catalog visibility,
preview, and explain. Enabled aliases require alias publication when whitelist
is on; family/exact/pattern rules alone cannot publish aliases. Alias targets
are allowed internally without exposing the original. An enabled exact rule
can expose an otherwise hidden target. Disabled aliases do not hide originals.
Whitelist off allows other names; whitelist on with no enabled rules denies
all. Real public models must additionally be routable. Known credential prefixes,
Gemini `models/`, case, surrounding whitespace, and a trailing thinking suffix
normalize for comparison only. Unknown `x/y` names stay literal.

Whole-family matching prevents `gpt-6.1-sol-mini` from joining GPT Sol. Versions
compare numerically (6.10 > 6.9; 4-5 == 4.5), then calendar-valid dates. Optional
dated suffixes support YYYYMMDD and YYYY-MM-DD. Short YYMMDD or MMDD forms are
accepted only with an explicit `{date}` template token, which removes their
ambiguity with dashed version components. YY uses the 2000–2099 century. MMDD
compares month/day; comparisons against dates that include a year are ambiguous.
Same-version dated/undated candidates, equal dotted/dashed versions, or other
unresolved top ties keep the current target. Family rules exclude prereleases.
Auto aliases opt into the contract's trailing prerelease markers through
`include_prerelease`. Current targets must parse; targets never downgrade.
Provider pins and routability apply before selecting a candidate.

This mock has no CPA process, inference adapter, paid health probe, background
poller, durable state, filesystem history, or live protection. A requested paid
health probe produces `health_check_unverified` and keeps the current target.
The unavailable-policy fixture fails closed until a valid state is saved. The
host's AfterAuth credential-level mappings and protocol-specific inference
error envelopes cannot be verified by a management-only mock.

Guard status is always **`verified:false`**. Realtime/live provider order requires
a real CPA test. WebSocket continuation frames, Gemini model detail, and
management `api-call` remain documented hook limitations; deploy a reverse proxy
to enforce policy where the host cannot intercept requests. No endpoint in this
mock fabricates a successful live-guard verification.

## Verification

- `node --test web/dev/tests/*.test.mjs`: 38 tests (23 API tests, including
  subtests, and 15 pure UI/asset tests), zero dependencies.
- `node --check web/assets/app.js`, `node --check web/assets/i18n.js`, and
  `node --check web/dev/mock-server.mjs` verify syntax.
- Browser sanity checks cover alias create/save, automatic check preview/apply,
  field validation and focus, stale-write conflict with preserved draft,
  server comparison/reload, history diff/restore, whitelist draft preview,
  alias publication and hidden-original explanations, deny-all confirmation,
  custom family examples, Korean persistence, and narrow-screen layout.
- The mock does not establish live CPA guard effectiveness; Overview explicitly
  reports it as unverified. No paid model probes run.
