import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { once } from 'node:events';
import { createMockServer, API_BASE, RESOURCE_BASE } from '../mock-server.mjs';

async function fixture(t, options = {}) {
  const server = createMockServer(options);
  assert.equal(server.listening, false, 'factory must not listen or autostart');
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  t.after(() => new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve())));
  const origin = `http://127.0.0.1:${server.address().port}`;
  async function call(path, { method = 'GET', body, auth = 'Bearer test-key', headers = {} } = {}) {
    const response = await fetch(origin + API_BASE + path, { method,
      headers: { ...(auth ? { Authorization: auth } : {}), ...headers },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
    return { status: response.status, data: await response.json() };
  }
  const state = async () => (await call('/state')).data;
  const save = (body, headers) => call('/state', { method: 'PUT', body, headers });
  const post = (path, body = {}, headers) => call(path, { method: 'POST', body, headers });
  return { server, origin, call, state, save, post };
}

test('all management routes require the exact Bearer key; unknown routes and malformed JSON are safe', async (t) => {
  const { call, origin } = await fixture(t);
  for (const [path, method] of [['/overview', 'GET'], ['/state', 'GET'], ['/state', 'PUT'], ['/validate', 'POST'],
    ['/catalog', 'GET'], ['/catalog/refresh', 'POST'], ['/aliases/check', 'POST'], ['/rules/preview', 'POST'],
    ['/explain', 'POST'], ['/history', 'GET'], ['/history/restore', 'POST']]) {
    assert.equal((await call(path, { method, auth: null })).status, 401, `${method} ${path}`);
    assert.equal((await call(path, { method, auth: 'Bearer wrong' })).status, 401);
  }
  assert.equal((await call('/state/extra')).status, 404);
  assert.equal((await call('/state', { method: 'DELETE' })).status, 404);
  const badJson = await fetch(origin + API_BASE + '/validate', { method: 'POST', headers: { Authorization: 'Bearer test-key' }, body: '{broken' });
  assert.equal(badJson.status, 400);
  assert.equal((await badJson.json()).error.code, 'invalid_json');
  assert.equal((await call('/validate', { method: 'POST', body: [] })).status, 400);
});

test('only the four fixed public resource files are served with correct MIME types', async (t) => {
  const assetsDir = await mkdtemp(join(tmpdir(), 'model-dock-mock-'));
  t.after(() => rm(assetsDir, { recursive: true, force: true }));
  const files = { 'index.html': 'text/html', 'app.js': 'text/javascript', 'app.css': 'text/css', 'i18n.js': 'text/javascript' };
  for (const file of [...Object.keys(files), 'secret.json', 'constructor', 'toString']) await writeFile(join(assetsDir, file), `fixture ${file}`);
  const { origin } = await fixture(t, { assetsDir });
  for (const [file, mime] of Object.entries(files)) {
    const response = await fetch(origin + RESOURCE_BASE + file);
    assert.equal(response.status, 200);
    assert.match(response.headers.get('content-type'), new RegExp(`^${mime}`));
    assert.equal(await response.text(), `fixture ${file}`);
  }
  const head = await fetch(origin + RESOURCE_BASE + 'index.html', { method: 'HEAD' });
  assert.equal(head.status, 200); assert.equal(await head.text(), '');
  for (const suffix of ['', 'secret.json', 'constructor', 'toString', 'mock-server.mjs', '%2e%2e/mock-server.mjs', 'app.js/extra', '%61pp.js']) {
    assert.equal((await fetch(origin + RESOURCE_BASE + suffix)).status, 404, suffix);
  }
  assert.equal((await fetch(origin + RESOURCE_BASE + 'app.js', { method: 'POST' })).status, 404);
  assert.equal((await fetch(origin + '/web/assets/app.js')).status, 404);
});

test('overview, catalog, built-in families, and initial policy agree', async (t) => {
  const { call, state, post } = await fixture(t);
  const current = await state(), overview = (await call('/overview')).data, catalog = (await call('/catalog')).data;
  assert.equal(current.revision, 1);
  for (const id of ['gpt-sol', 'gpt-astra', 'gpt-luna', 'claude-opus', 'claude-sonnet', 'claude-haiku', 'claude-fable', 'gemini-pro', 'gemini-flash']) {
    assert.ok(current.builtin_families.some((family) => family.id === `builtin:${id}`));
  }
  for (const id of ['gpt-6-astra', 'gpt-6-luna', 'claude-sonnet-4-5', 'gemini-3-pro-preview']) {
    assert.ok(catalog.models.some((model) => model.id === id && model.routable), id);
  }
  assert.deepEqual(Object.keys(overview).sort(), ['plugin_version', 'host_schema', 'policy_enabled', 'whitelist_enabled', 'counts', 'active_mappings', 'recent_auto_updates', 'catalog', 'guard', 'warnings'].sort());
  assert.equal(overview.counts.public_models, catalog.models.filter((model) => model.public).length);
  assert.equal(overview.counts.public_aliases, 2);
  assert.equal(overview.guard.verified, false);
  assert.ok(overview.guard.limitations.some((text) => /real CPA/.test(text)));
  assert.deepEqual(overview.active_mappings[0], { alias: 'gpt-sol', target: 'gpt-6.1-sol' });
  const catalogOnly = catalog.models.find((model) => model.id === 'gpt-6.12-sol');
  assert.equal(catalogOnly.routable, false); assert.equal(catalogOnly.public, false);
  assert.equal(catalogOnly.reason.code, 'not_routable');
  assert.equal(catalog.models.find((model) => model.id === 'gpt-6.1-sol').reason.code, 'hidden_by_alias');
  const explanation = (await post('/explain', { model: 'gpt-sol' })).data;
  assert.equal(explanation.allowed, true); assert.equal(explanation.resolved_target, 'gpt-6.1-sol');
});

test('saved state projects editable fields, generates IDs, accepts stable local family references, and rejects stale writes', async (t) => {
  const { state, save, post, call } = await fixture(t);
  const draft = await state();
  draft.settings.whitelist_enabled = true;
  draft.families.push({ id: 'fam_local_reference', name: 'Mini', template: 'gpt-{version}-sol-mini', provider: 'codex', allow_dated: true });
  draft.aliases.push({ name: 'mini', enabled: true, mode: 'auto', target: 'gpt-6.1-sol-mini', provider: '', expose_original: false,
    rewrite_response_model: null, auto: { family: 'fam_local_reference' }, status: { injected: true }, secret: 'never-saved' });
  draft.rules.push({ kind: 'alias', value: 'mini', enabled: true });
  draft.builtin_families = [{ id: 'builtin:injected' }];
  draft.alias_status = { malicious: true };
  draft.runtime = 'discard me';
  const saved = await save(draft);
  assert.equal(saved.status, 200); assert.equal(saved.data.revision, 2);
  const alias = saved.data.aliases.find((entry) => entry.name === 'mini');
  assert.match(alias.id, /^al_/); assert.equal(alias.status, undefined); assert.equal(alias.secret, undefined);
  assert.match(saved.data.rules.at(-1).id, /^r_/);
  assert.equal(saved.data.runtime, undefined); assert.equal(saved.data.builtin_families.length, 9);
  assert.equal(saved.data.alias_status.malicious, undefined);
  const stale = await save(draft);
  assert.equal(stale.status, 409); assert.equal(stale.data.error.code, 'revision_conflict'); assert.equal(stale.data.state.revision, 2);
  const noRevision = { ...saved.data }; delete noRevision.revision;
  assert.equal((await save(noRevision)).status, 409);
  const headerSaved = await save(noRevision, { 'If-Match': '"2"' });
  assert.equal(headerSaved.status, 200); assert.equal(headerSaved.data.revision, 3);
  assert.equal((await save(headerSaved.data, { 'If-Match': '"1"' })).status, 409);
  assert.equal((await post('/catalog/refresh')).status, 409);
  assert.equal((await post('/aliases/check')).status, 409);
  assert.equal((await post('/history/restore', { entry_id: 'x', scope: 'state' })).status, 409);
  const entries = (await call('/history')).data.entries;
  assert.equal(entries.length, 2); assert.equal(entries[0].after.alias_status, undefined);
});

test('validation gives actionable paths and never saves partial or invalid policies', async (t) => {
  const { state, save, post, call } = await fixture(t);
  const original = await state(), invalid = structuredClone(original);
  invalid.settings.refresh_interval_seconds = 10;
  invalid.families.push({ id: 'fam_invalid', name: 'Oops', template: 'no-version' });
  invalid.aliases[0].name = 'GPT-6.10-SOL';
  invalid.aliases[0].auto.family = 'builtin:missing';
  invalid.rules.push({ kind: 'alias', value: 'missing' });
  const validated = await post('/validate', invalid);
  assert.equal(validated.status, 200); assert.equal(validated.data.valid, false);
  const paths = validated.data.details.map((detail) => detail.path);
  for (const path of ['settings.refresh_interval_seconds', 'families[0].template', 'aliases[0].name', 'aliases[0].auto.family', 'rules[2].value']) assert.ok(paths.includes(path), path);
  assert.ok(validated.data.details.some((detail) => detail.code === 'alias_conflict'));
  const rejected = await save(invalid);
  assert.equal(rejected.status, 422); assert.equal(rejected.data.error.code, 'validation_failed');
  assert.ok(rejected.data.error.details.every((detail) => detail.path && detail.code && detail.message));
  assert.deepEqual(await state(), original); assert.deepEqual((await call('/history')).data.entries, []);
  const valid = await post('/validate', original);
  assert.equal(valid.data.valid, true); assert.ok(valid.data.preview.public_aliases.includes('gpt-sol'));
  assert.deepEqual(await state(), original);
  for (const invalidBody of [{}, { settings: null, families: [], aliases: [null], rules: [] }]) {
    assert.equal((await post('/validate', invalidBody)).data.valid, false);
  }
  assert.equal((await post('/explain', { model: 'x', draft: [] })).status, 422);
  assert.equal((await post('/explain', { model: '' })).status, 422);
});

test('policy uses whole-family rules, hidden originals, exact overrides, globs, normalization, and empty deny-all', async (t) => {
  const { state, save, post, call } = await fixture(t);
  const draft = await state(); draft.settings.whitelist_enabled = true;
  assert.equal((await save(draft)).status, 200);
  for (const model of ['GPT-6.10-SOL', ' p/gpt-6.10-sol(high) ']) {
    const decision = (await post('/explain', { model })).data;
    assert.equal(decision.allowed, true); assert.equal(decision.code, 'rule:r_sol');
  }
  assert.equal((await post('/explain', { model: 'gpt-6.10-sol-mini' })).data.allowed, false);
  assert.equal((await post('/explain', { model: 'gpt-6.11-sol-preview' })).data.allowed, false);
  assert.equal((await post('/explain', { model: 'unknown/gpt-6.10-sol' })).data.allowed, false);
  assert.equal((await post('/explain', { model: 'gpt-sol' })).data.allowed, true);
  const hidden = (await post('/explain', { model: 'gpt-6.1-sol' })).data;
  assert.equal(hidden.status, 404); assert.equal(hidden.code, 'hidden_by_alias');
  const exactDraft = { rules: [...draft.rules, { id: 'r_exact', enabled: true, kind: 'exact', value: 'gpt-6.1-sol' }] };
  assert.equal((await post('/explain', { model: 'gpt-6.1-sol', draft: exactDraft })).data.allowed, true);
  const glob = { rules: [{ id: 'glob', enabled: true, kind: 'pattern', value: 'claude-opus-4-?' }] };
  assert.equal((await post('/explain', { model: 'claude-opus-4-6', draft: glob })).data.allowed, true);
  assert.equal((await post('/explain', { model: 'claude-opus-4-5-20251101', draft: glob })).data.allowed, false);
  const gemini = { rules: [{ id: 'g', enabled: true, kind: 'exact', value: 'gemini-3.0-pro' }] };
  assert.equal((await post('/explain', { model: 'models/gemini-3.0-pro', draft: gemini })).data.allowed, true);
  const empty = await state(); empty.rules = []; await save(empty);
  assert.equal((await post('/explain', { model: 'gpt-sol' })).data.code, 'alias_not_published');
  assert.equal((await post('/explain', { model: 'gpt-6.10-sol' })).data.code, 'not_whitelisted');
  assert.equal((await call('/catalog')).data.models.filter((model) => model.public).length, 0);
  assert.deepEqual((await post('/rules/preview')).data.public_aliases, []);
});

test('draft preview/validate/explain do not mutate state, runtime status, revision, or history', async (t) => {
  const { state, post, call } = await fixture(t);
  const before = await state();
  const draft = { settings: { whitelist_enabled: true }, rules: [{ id: 'r_new', enabled: true, kind: 'exact', value: 'gemini-3.0-pro' }] };
  const preview = await post('/rules/preview', draft);
  assert.equal(preview.status, 200);
  assert.deepEqual(preview.data.public_models, ['gemini-3.0-pro']);
  assert.deepEqual(preview.data.public_aliases, []);
  assert.deepEqual(preview.data.rules, [{ id: 'r_new', matches: ['gemini-3.0-pro'] }]);
  const explanation = await post('/explain', { model: 'gpt-sol', draft });
  assert.equal(explanation.data.allowed, false); assert.equal(explanation.data.code, 'alias_not_published');
  await post('/validate', { ...before, revision: 999, ...draft, settings: { ...before.settings, ...draft.settings } });
  assert.deepEqual(await state(), before); assert.deepEqual((await call('/history')).data.entries, []);
});

test('alias dry-run orders 6.10 above 6.9, reports catalog-only candidates, applies once, and restores without rolling revision back', async (t) => {
  const { state, post, call } = await fixture(t);
  const initial = await state();
  const dry = await post('/aliases/check', { alias_id: 'al_sol', dry_run: true });
  assert.equal(dry.status, 200); assert.equal(dry.data.revision, initial.revision);
  const evaluation = dry.data.results[0];
  assert.deepEqual(Object.keys(evaluation).sort(), ['alias_id', 'current_target', 'candidates', 'last_checked_at', 'last_result', 'reason'].sort());
  assert.equal(evaluation.current_target, 'gpt-6.10-sol'); assert.equal(evaluation.last_result, 'updated');
  assert.equal(evaluation.candidates.find((candidate) => candidate.chosen).version, '6.10');
  assert.equal(evaluation.candidates.find((candidate) => candidate.id === 'gpt-6.12-sol').routable, false);
  assert.ok(!evaluation.candidates.some((candidate) => /mini|preview/.test(candidate.id)));
  assert.deepEqual(await state(), initial); assert.deepEqual((await call('/history')).data.entries, []);
  const applied = await post('/aliases/check', { alias_id: 'al_sol', revision: initial.revision });
  assert.equal(applied.data.revision, initial.revision + 1);
  let current = await state(); assert.equal(current.aliases[0].target, 'gpt-6.10-sol');
  assert.equal(current.alias_status.al_sol.target_routable, true);
  assert.equal(current.alias_status.al_sol.last_result, 'updated');
  const entries = (await call('/history')).data.entries;
  assert.equal(entries.length, 1); assert.equal(entries[0].kind, 'alias_target'); assert.equal(entries[0].actor, 'auto');
  assert.deepEqual(entries[0].before, { alias_id: 'al_sol', target: 'gpt-6.1-sol' });
  assert.equal((await call('/overview')).data.recent_auto_updates.length, 1);
  assert.equal((await post('/aliases/check', { revision: initial.revision })).status, 409);
  const unchanged = await post('/aliases/check', { alias_id: 'al_sol', revision: current.revision });
  assert.equal(unchanged.data.results[0].last_result, 'unchanged');
  assert.equal((await call('/history')).data.entries.length, 1);
  current = await state();
  assert.equal((await post('/history/restore', { entry_id: entries[0].id, revision: current.revision, scope: 'state' })).status, 422);
  const restored = await post('/history/restore', { entry_id: entries[0].id, revision: current.revision, scope: 'alias_target' });
  assert.equal(restored.status, 200); assert.equal(restored.data.aliases[0].target, 'gpt-6.1-sol');
  assert.equal(restored.data.revision, current.revision + 1);
  assert.equal((await call('/history')).data.entries[0].actor, 'user');
});

test('alias evaluation handles ambiguity, dates, prerelease opt-in, provider pinning, unparseable targets, and paid checks', async (t) => {
  const cases = [
    { name: 'dated/undated tie', ids: ['gpt-6.9-sol', 'gpt-6.10-sol', 'gpt-6.10-sol-20261001'], expected: 'ambiguous' },
    { name: 'equal dotted/dashed versions', ids: ['gpt-6.9-sol', 'gpt-6.10-sol', 'gpt-6-10-sol'], expected: 'ambiguous' },
    { name: 'newer dates', ids: ['gpt-6.9-sol', 'gpt-6.10-sol-20261001', 'gpt-6.10-sol-20261002'], expected: 'newer_version', target: 'gpt-6.10-sol-20261002' },
    { name: 'unparseable current', target: 'not-a-family-member', ids: ['gpt-6.10-sol'], expected: 'current_unparseable' },
    { name: 'no routable candidates', provider: 'missing', ids: ['gpt-6.9-sol', 'gpt-6.10-sol'], expected: 'no_routable_candidates' },
    { name: 'health checks are not fabricated', health: true, ids: ['gpt-6.9-sol', 'gpt-6.10-sol'], expected: 'health_check_unverified' },
    { name: 'prerelease enabled', prerelease: true, ids: ['gpt-6.9-sol', 'gpt-6.10-sol-preview'], expected: 'newer_version', target: 'gpt-6.10-sol-preview' },
    { name: 'no downgrade', current: 'gpt-6.10-sol', ids: ['gpt-6.9-sol'], expected: 'up_to_date' },
  ];
  for (const item of cases) await t.test(item.name, async (t) => {
    const { state, save, post } = await fixture(t, { catalog: item.ids.map((id) => ({ id, providers: ['codex'], routable: true })) });
    const draft = await state(); draft.aliases = [draft.aliases[0]];
    const alias = draft.aliases[0]; alias.target = item.current ?? (item.expected === 'current_unparseable' ? item.target : 'gpt-6.9-sol');
    alias.provider = item.provider ?? ''; alias.auto.health_check = item.health ?? false; alias.auto.include_prerelease = item.prerelease ?? false;
    const saved = await save(draft); assert.equal(saved.status, 200);
    const result = (await post('/aliases/check', { dry_run: true })).data.results[0];
    assert.equal(result.reason.code, item.expected);
    assert.equal(result.current_target, item.expected === 'newer_version' ? item.target : alias.target);
  });
});

test('unavailable alias targets never fall back; disabled aliases do not hide originals', async (t) => {
  const { state, save, post } = await fixture(t);
  const draft = await state(); draft.aliases[0].target = 'gpt-6.12-sol';
  const saved = await save(draft); assert.equal(saved.status, 200);
  const decision = (await post('/explain', { model: 'gpt-sol' })).data;
  assert.equal(decision.allowed, false); assert.equal(decision.status, 503); assert.equal(decision.code, 'alias_target_unavailable');
  assert.equal(decision.resolved_target, undefined);
  const preview = (await post('/rules/preview')).data;
  assert.ok(!preview.public_aliases.includes('gpt-sol')); assert.ok(!preview.public_models.includes('gpt-6.12-sol'));
  const disabled = await state(); disabled.aliases[0].enabled = false; disabled.aliases[0].target = 'gpt-6.1-sol'; await save(disabled);
  assert.equal((await post('/explain', { model: 'gpt-6.1-sol' })).data.allowed, true);
});

test('custom family references work in the same save; dated matching respects allow_dated and explicit date tokens', async (t) => {
  const { state, save, post } = await fixture(t, { catalog: [
    { id: 'sample-6.9-1026', providers: ['codex'], routable: true },
    { id: 'sample-6.10-1027', providers: ['codex'], routable: true },
    { id: 'sample-6.10-20261027', providers: ['codex'], routable: true },
    { id: 'claude-sonnet-4-5-20250929', providers: ['anthropic'], routable: true },
    { id: 'claude-sonnet-4-5', providers: ['anthropic'], routable: true },
  ] });
  const draft = await state();
  draft.families = [
    { id: 'fam_short', name: 'Short dates', template: 'sample-{version}-{date}', allow_dated: true },
    { id: 'fam_undated', name: 'Undated', template: 'claude-sonnet-{version}', allow_dated: false },
  ];
  draft.aliases = [{ name: 'short', mode: 'auto', target: 'sample-6.9-1026', auto: { family: 'fam_short' } }];
  draft.rules = [{ id: 'undated', enabled: true, kind: 'family', value: 'fam_undated' }];
  const saved = await save(draft); assert.equal(saved.status, 200);
  const checked = (await post('/aliases/check', { dry_run: true })).data.results[0];
  assert.equal(checked.reason.code, 'ambiguous', 'MMDD cannot be ordered against year-bearing dates');
  const preview = (await post('/rules/preview')).data;
  assert.deepEqual(preview.rules[0].matches, ['claude-sonnet-4-5']);
  const current = await state(); current.aliases[0].auto.family = 'fam_short';
  current.families[0].template = 'sample-{version}-{date}';
  assert.equal((await save(current)).status, 200);
  const invalid = await state(); invalid.aliases.push({ ...invalid.aliases[0], id: 'al_dupe', name: 'SHORT' });
  const conflict = await save(invalid);
  assert.equal(conflict.status, 422);
  assert.ok(conflict.data.error.details.some((detail) => detail.path === 'aliases[1].name' && detail.code === 'alias_conflict'));
});

test('unavailable policy fails closed and a valid state save recovers it', async (t) => {
  const { state, save, call, post } = await fixture(t, { policyAvailable: false });
  const blocked = (await post('/explain', { model: 'gpt-sol' })).data;
  assert.equal(blocked.code, 'policy_unavailable'); assert.equal(blocked.status, 503);
  assert.equal((await call('/overview')).data.policy_enabled, false);
  assert.ok((await call('/catalog')).data.models.every((model) => !model.public));
  assert.equal((await save(await state())).status, 200);
  assert.equal((await post('/explain', { model: 'gpt-sol' })).data.allowed, true);
  assert.equal((await call('/overview')).data.policy_enabled, true);
});

test('catalog refresh is revision guarded, replaces snapshots, and retains last good on failure', async (t) => {
  let fail = false;
  const { state, call, post } = await fixture(t, { refreshCatalog: (models) => {
    if (fail) throw new Error('credential token must never leak');
    return [...models, { id: 'gpt-7.0-sol', providers: ['codex'], routable: true }];
  } });
  const before = await state();
  const refreshed = await post('/catalog/refresh', { revision: before.revision });
  assert.equal(refreshed.status, 200); assert.equal(refreshed.data.ok, true);
  assert.ok(refreshed.data.models.some((model) => model.id === 'gpt-7.0-sol'));
  assert.equal((await state()).revision, before.revision + 1);
  fail = true;
  const failed = await post('/catalog/refresh', { revision: (await state()).revision });
  assert.equal(failed.status, 200); assert.equal(failed.data.ok, false);
  assert.deepEqual(failed.data.models, refreshed.data.models);
  assert.ok(!JSON.stringify(failed.data).includes('credential token'));
  assert.equal((await call('/overview')).data.catalog.ok, false);
});

test('concurrent writes and refresh callbacks cannot overwrite a newer revision', async (t) => {
  let release;
  const { state, save, post } = await fixture(t, { refreshCatalog: () => new Promise((resolve) => { release = resolve; }) });
  const draft = await state();
  const results = await Promise.all([save(draft), save(draft)]);
  assert.deepEqual(results.map((result) => result.status).sort(), [200, 409]);
  const current = await state();
  const refresh = post('/catalog/refresh', { revision: current.revision });
  while (!release) await new Promise((resolve) => setImmediate(resolve));
  const saved = await save(current); assert.equal(saved.status, 200);
  release([]);
  assert.equal((await refresh).status, 409);
  assert.equal((await state()).revision, saved.data.revision);
});

test('history pages newest first with an exclusive cursor; state restore undoes before snapshot and stays monotonic', async (t) => {
  const { state, save, call, post } = await fixture(t);
  const first = await state();
  first.settings.whitelist_enabled = true; const saved1 = await save(first);
  const second = saved1.data; second.settings.rewrite_response_model = true; const saved2 = await save(second);
  const third = saved2.data; third.settings.refresh_interval_seconds = 60; const saved3 = await save(third);
  const firstPage = (await call('/history?limit=2')).data;
  assert.equal(firstPage.entries.length, 2); assert.equal(firstPage.next_before, firstPage.entries[1].id);
  const next = (await call(`/history?limit=2&before=${firstPage.next_before}`)).data;
  assert.equal(next.entries.length, 1); assert.equal(next.next_before, null);
  assert.equal(new Set([...firstPage.entries, ...next.entries].map((entry) => entry.id)).size, 3);
  assert.equal((await call('/history?before=missing')).status, 400);
  assert.equal((await call('/history?limit=0')).status, 400);
  assert.equal((await call('/history?limit=1.5')).status, 400);
  const oldest = next.entries[0];
  const restored = await post('/history/restore', { entry_id: oldest.id, scope: 'state', revision: saved3.data.revision });
  assert.equal(restored.status, 200); assert.equal(restored.data.revision, saved3.data.revision + 1);
  assert.deepEqual(restored.data.settings, oldest.before.settings);
  assert.deepEqual(restored.data.aliases, oldest.before.aliases);
  assert.equal((await call('/history')).data.entries.length, 4);
});
