# Policy examples

These JSON files are full **management API drafts**, not files to copy directly
over the live `state.json`. New alias IDs are deliberately omitted so the server
can generate them. Saves add server-owned IDs, timestamps, and revisions.
The fixture model names are illustrative; use IDs present in your CPA catalog.

| File | Intent |
| --- | --- |
| `01-pinned-sol.json` | `gpt-sol` stays on `gpt-6.1-sol`; originals stay exposed |
| `02-auto-sol.json` | Same alias follows newer stable `builtin:gpt-sol` versions |
| `03-family-whitelist.json` | Only GPT Sol, GPT Astra, and Claude Opus families |
| `04-aliases-only.json` | Only `gpt-sol` is published; original ID hidden; response rewriting enabled |

All settings, booleans and auto flags are explicit. In the third example,
family rules admit future stable matching versions but do not publish aliases.
In the fourth, add an alias and an enabled `kind: alias` rule for every
additional name you want to publish. No family/exact/pattern rules are needed.

From the repository root, after setting a management key in your shell:

```sh
export CPA_URL=http://127.0.0.1:8317
# Set MANAGEMENT_KEY to your CPA management key without putting it in a URL.
api="$CPA_URL/v0/management/plugins/cpa-model-dock"
draft=examples/01-pinned-sol.json
curl --fail-with-body -sS -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H 'Content-Type: application/json' -X POST --data-binary "@$draft" "$api/validate"
# Require valid:true before continuing. Replace the full policy only after review.
revision=$(curl --fail-with-body -sS -H "Authorization: Bearer $MANAGEMENT_KEY" "$api/state" | jq '.revision')
jq --argjson revision "$revision" '.revision = $revision' "$draft" | \
  curl --fail-with-body -sS -H "Authorization: Bearer $MANAGEMENT_KEY" \
    -H 'Content-Type: application/json' -X PUT --data-binary @- "$api/state"
```

Use `POST /validate` (there is no `PUT /validate`). A successful validation is
HTTP 200 with `valid:true`; check the JSON flag, since invalid drafts also
return HTTP 200 with `valid:false`. State replacement needs the latest revision
or `If-Match: "<revision>"`; a stale revision returns 409. Review and merge existing
policy first: each example replaces all settings/families/aliases/rules.

For repeatable isolated validation on a built host library:

```sh
python3 scripts/validate-examples.py --cpa-bin /tmp/cpa-bin \
  --library dist/cpa-model-dock.dylib --keep
```

All four were validated and saved on a real CPA v8.0.44 instance; see
[verification scope and the fixture's routability caveat](../docs/verification.md).
This check does not establish inference, upgrade, or bypass enforcement.
The [English](../README.md) and [Korean](../README.ko.md) guides include UI steps.
