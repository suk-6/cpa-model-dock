# Changelog

## 0.1.2 — 2026-10-27

- Fix Responses streaming with response-name rewriting for Codex scanner lines,
  fragmented JSON and mixed SSE chunks; preserve metadata and line endings.
- Retry empty startup inventories and persist the sanitized last-known catalog;
  retain it with a visible warning on entirely empty refreshes. Recognize native
  Codex API-key config registrations omitted from CPA's runtime inventory.
- Persist alias evaluation times, candidates and reasons across unrelated edits
  and restarts; reevaluate changed automatic settings asynchronously.
- Show changed alias fields and before/after values in English/Korean history.
- Show human-readable restore summaries and offer restore-and-pin for automatic
  aliases, with a revisioned `pin:true` restore option.

## 0.1.1 — 2026-10-27

- Fix the Web UI failing with `crypto.randomUUID is not a function` when adding
  rules or families while CPA is served over plain HTTP from a non-localhost host.

## 0.1.0 — 2026-10-27

- Native CLIProxyAPI plugin for pinned model aliases, conservative per-alias
  family upgrades, and model whitelist rules.
- Shared policy for supported model listings and request hooks, with an
  AfterAuth check of the resolved destination and optional response model rewrite.
- English/Korean Web UI, authenticated revision-based management API, catalog
  previews, explanations, atomic policy storage, backup, and bounded history.
- Five platform build targets, plugin-store ZIPs and SHA-256 manifests,
  self-hosted direct registry generation, and four runnable policy examples.
- Target CPA v8.0.44, ABI 1, schemas 6–7. See the README for endpoint coverage
  limits and the distinction between source evidence and real integration tests.
