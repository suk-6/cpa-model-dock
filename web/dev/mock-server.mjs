import http from 'node:http';
import { readFile } from 'node:fs/promises';
import { randomUUID } from 'node:crypto';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { resolve } from 'node:path';

export const API_BASE = '/v0/management/plugins/cpa-model-dock';
export const RESOURCE_BASE = '/v0/resource/plugins/cpa-model-dock/';
const clone = (value) => structuredClone(value);
const object = (value) => value !== null && typeof value === 'object' && !Array.isArray(value);
const key = (value) => String(value ?? '').trim().toLowerCase();
const reason = (code, message) => ({ code, message });
const escape = (value) => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const builtins = [
  ['gpt-sol', 'GPT Sol', 'gpt-{version}-sol'],
  ['gpt-astra', 'GPT Astra', 'gpt-{version}-astra'],
  ['gpt-luna', 'GPT Luna', 'gpt-{version}-luna'],
  ['claude-opus', 'Claude Opus', 'claude-opus-{version}'],
  ['claude-sonnet', 'Claude Sonnet', 'claude-sonnet-{version}'],
  ['claude-haiku', 'Claude Haiku', 'claude-haiku-{version}'],
  ['claude-fable', 'Claude Fable', 'claude-fable-{version}'],
  ['gemini-pro', 'Gemini Pro', 'gemini-{version}-pro'],
  ['gemini-flash', 'Gemini Flash', 'gemini-{version}-flash'],
].map(([id, name, template]) => ({ id: `builtin:${id}`, name, template, provider: '', allow_dated: true }));

// Fictional fixtures exercise policy edge cases; these are not live provider claims.
export const SAMPLE_MODELS = [
  ['gpt-6.1-sol', 'codex'], ['gpt-6.9-sol', 'codex'], ['gpt-6.10-sol', 'codex'],
  ['gpt-6.11-sol-preview', 'codex'], ['gpt-6.12-sol', 'codex', false],
  ['gpt-6.1-sol-mini', 'codex'], ['gpt-6.10-sol-mini', 'codex'],
  ['gpt-6-astra', 'openai'], ['gpt-6.1-astra', 'openai'], ['gpt-6.2-astra', 'openai'],
  ['gpt-6-luna', 'openai'], ['gpt-6.1-luna', 'openai'], ['gpt-6.2-luna', 'openai'],
  ['claude-opus-4-5-20251101', 'anthropic'], ['claude-opus-4-6', 'anthropic'],
  ['claude-sonnet-4-5', 'anthropic'], ['claude-sonnet-4-5-20250929', 'anthropic'], ['claude-sonnet-4-6', 'anthropic'],
  ['claude-haiku-4-5-20251001', 'anthropic'], ['claude-fable-4-5', 'anthropic'],
  ['gemini-2.5-pro', 'gemini'], ['gemini-3.0-pro', 'gemini'],
  ['gemini-3-pro-preview', 'gemini'], ['gemini-3.1-pro-preview', 'gemini'], ['gemini-2.5-flash', 'gemini'],
  ['gemini-3.0-flash', 'gemini'], ['gemini-3.1-flash', 'gemini', false],
].map(([id, provider, routable = true]) => ({ id, providers: [provider], credential_count: routable ? 1 : 0, routable }));

function defaultState() {
  return {
    schema: 1, revision: 1,
    settings: { whitelist_enabled: false, rewrite_response_model: false, refresh_interval_seconds: 300 },
    families: [],
    aliases: [
      { id: 'al_sol', name: 'gpt-sol', enabled: true, mode: 'auto', target: 'gpt-6.1-sol', provider: '',
        expose_original: false, rewrite_response_model: null,
        auto: { family: 'builtin:gpt-sol', include_prerelease: false, health_check: false } },
      { id: 'al_opus', name: 'opus', enabled: true, mode: 'pinned', target: 'claude-opus-4-5-20251101', provider: '',
        expose_original: true, rewrite_response_model: null, auto: {family:'',include_prerelease:false,health_check:false} },
    ],
    rules: [
      { id: 'r_sol', enabled: true, kind: 'family', value: 'builtin:gpt-sol', note: 'Whole GPT Sol family' },
      { id: 'r_alias', enabled: true, kind: 'alias', value: 'gpt-sol', note: 'Publish stable alias' },
    ],
  };
}

function dateNumber(raw) {
  const compact = raw.replaceAll('-', '');
  const expanded = compact.length === 6 ? `20${compact}` : compact.length === 4 ? `2000${compact}` : compact;
  if (!/^\d{8}$/.test(expanded)) return null;
  const yyyy = +expanded.slice(0, 4), mm = +expanded.slice(4, 6), dd = +expanded.slice(6);
  const date = new Date(Date.UTC(yyyy, mm - 1, dd));
  return date.getUTCFullYear() === yyyy && date.getUTCMonth() === mm - 1 && date.getUTCDate() === dd ? +expanded : null;
}

function parseFamily(id, family, includePrerelease = false) {
  let name = key(id), prerelease = false;
  const marker = name.match(/-(preview|beta|alpha|experimental|exp|rc|nightly|dev|test)(?:[.-]?\d+)?$/);
  if (marker) { prerelease = true; name = name.slice(0, marker.index); }
  if (prerelease && !includePrerelease) return null;
  let trailingDate = null, dateWidth = null;
  if (!family.template.includes('{date}')) {
    const suffix = name.match(/-(\d{8}|\d{4}-\d{2}-\d{2})$/);
    if (suffix && dateNumber(suffix[1]) !== null) {
      if (!family.allow_dated) return null;
      trailingDate = dateNumber(suffix[1]); dateWidth = 8; name = name.slice(0, suffix.index);
    }
  }
  const pattern = family.template.split(/(\{version\}|\{date\})/).map((part) =>
    part === '{version}' ? '(?<version>\\d+(?:[.-]\\d+)*)' :
      part === '{date}' ? '(?<date>\\d{8}|\\d{4}-\\d{2}-\\d{2}|\\d{6}|\\d{4})' : escape(part)).join('');
  const match = new RegExp(`^${pattern}$`, 'i').exec(name);
  if (!match) return null;
  const date = match.groups.date ? dateNumber(match.groups.date) : trailingDate;
  if (match.groups.date && date === null) return null;
  if (match.groups.date) dateWidth = match.groups.date.replaceAll('-', '').length;
  return { version: match.groups.version, components: match.groups.version.split(/[.-]/).map(Number), date, dateWidth, prerelease };
}

function compare(a, b) {
  for (let i = 0; i < Math.max(a.components.length, b.components.length); i++) {
    const difference = (a.components[i] ?? 0) - (b.components[i] ?? 0);
    if (difference) return Math.sign(difference);
  }
  if (a.dateWidth !== b.dateWidth && (a.dateWidth === 4 || b.dateWidth === 4)) return null;
  if (a.date === b.date) return 0;
  if (a.date === null || b.date === null) return null;
  return Math.sign(a.date - b.date);
}

/** Returns an unstarted, isolated Node http.Server. Importing never opens a port. */
export function createMockServer(options = {}) {
  const clock = options.now ?? (() => new Date());
  const timestamp = () => new Date(clock()).toISOString();
  const assetsDir = options.assetsDir ?? fileURLToPath(new URL('../assets/', import.meta.url));
  const credentialPrefixes = options.credentialPrefixes ?? ['p', 'codex', 'openai', 'anthropic', 'gemini'];
  let models = clone(options.catalog ?? SAMPLE_MODELS);
  let state = clone(options.state ?? defaultState());
  state.updated_at = timestamp();
  let policyAvailable = options.policyAvailable ?? true;
  let catalogStatus = { last_refresh_at: timestamp(), ok: true, error: null };
  let aliasStatus = {};
  let history = [];
  let historySequence = 0;
  let refreshQueue = Promise.resolve();

  function normalize(value) {
    let name = key(value).replace(/\s*\([^()]*\)\s*$/, '');
    const prefix = name.split('/')[0];
    if (credentialPrefixes.map(key).includes(prefix) && name.includes('/')) name = name.slice(prefix.length + 1);
    return name.replace(/^models\//, '');
  }
  const catalogModel = (id) => models.find((model) => key(model.id) === normalize(id));
  const familyById = (policy, id) => [...builtins, ...policy.families].find((family) => family.id === id);
  function routable(alias) {
    const model = catalogModel(alias.target);
    return Boolean(model?.routable && (!alias.provider || model.providers.some((provider) => key(provider) === key(alias.provider))));
  }
  function ruleMatches(rule, name, policy) {
    if (!rule.enabled) return false;
    if (rule.kind === 'exact' || rule.kind === 'alias') return normalize(rule.value) === name;
    if (rule.kind === 'pattern') return new RegExp(`^${key(rule.value).split(/([*?])/).map((part) => part === '*' ? '.*' : part === '?' ? '.' : escape(part)).join('')}$`).test(name);
    const family = familyById(policy, rule.value);
    return Boolean(family && parseFamily(name, family) && (!family.provider || catalogModel(name)?.providers.includes(family.provider)));
  }
  function decide(input, policy = state, available = policyAvailable) {
    const name = normalize(input), steps = [{ code: 'normalized', message: `Matched as ${name}` }];
    const finish = (allowed, status, code, message, resolvedTarget) => ({ allowed, status, code, reason: message,
      steps: [...steps, { code, message }], ...(resolvedTarget ? { resolved_target: resolvedTarget } : {}) });
    if (!available) return finish(false, 503, 'policy_unavailable', 'No valid policy is available; save a valid state to recover.');
    const alias = policy.aliases.find((entry) => entry.enabled && normalize(entry.name) === name);
    if (alias) {
      if (policy.settings.whitelist_enabled && !policy.rules.some((rule) => rule.kind === 'alias' && ruleMatches(rule, name, policy))) {
        return finish(false, 403, 'alias_not_published', 'No enabled alias rule publishes this alias.');
      }
      if (!routable(alias)) return finish(false, 503, 'alias_target_unavailable', 'Alias target has no matching enabled credential.');
      return finish(true, 200, 'alias', 'Published alias routes to its exact target.', alias.target);
    }
    const exact = policy.rules.find((rule) => rule.kind === 'exact' && ruleMatches(rule, name, policy));
    if (!exact && policy.aliases.some((entry) => entry.enabled && !entry.expose_original && normalize(entry.target) === name)) {
      return finish(false, 404, 'hidden_by_alias', 'This name is not public.');
    }
    if (!policy.settings.whitelist_enabled) return finish(true, 200, 'whitelist_disabled', 'Whitelist is disabled.');
    const rule = policy.rules.find((entry) => entry.kind !== 'alias' && ruleMatches(entry, name, policy));
    return rule ? finish(true, 200, `rule:${rule.id}`, 'An enabled allow rule matches.') :
      finish(false, 403, 'not_whitelisted', 'No enabled allow rule matches.');
  }
  function publicLists(policy = state, available = policyAvailable) {
    return {
      public_models: models.filter((model) => model.routable && decide(model.id, policy, available).allowed).map((model) => model.id).sort(),
      public_aliases: policy.aliases.filter((alias) => alias.enabled && decide(alias.name, policy, available).allowed).map((alias) => alias.name).sort(),
    };
  }
  function catalog() {
    return { refreshed_at: catalogStatus.last_refresh_at, ok: catalogStatus.ok, error: catalogStatus.error,
      models: models.map((model) => { const decision = decide(model.id); return { id:model.id, providers:clone(model.providers ?? []), credential_count:model.credential_count ?? 0, routable:Boolean(model.routable),
        public: Boolean(model.routable && decision.allowed), reason: !model.routable ?
          reason('not_routable', 'Known in catalog, but no enabled credential advertises it.') : reason(decision.code, decision.reason) }; }) };
  }
  function savedPolicy(policy) {
    return { schema: 1, revision: policy.revision, updated_at: policy.updated_at,
      settings: clone(policy.settings), families: clone(policy.families), aliases: clone(policy.aliases), rules: clone(policy.rules) };
  }
  function getState() {
    return { ...savedPolicy(state), builtin_families: clone(builtins), alias_status: Object.fromEntries(state.aliases.map((alias) =>
      [alias.id, { current_target: alias.target, candidates: [], last_checked_at: null, last_result: 'kept',
        reason: reason('not_checked', 'No automatic check has run.'), ...clone(aliasStatus[alias.id] ?? {}), target_routable: routable(alias) }])) };
  }
  function validate(input, partial = false) {
    const details = [], draft = partial ? { ...savedPolicy(state), ...input,
      settings: input.settings === undefined ? state.settings : object(input.settings) ? { ...state.settings, ...input.settings } : input.settings } : input;
    const add = (path, code, message) => details.push({ path, code, message });
    const output = { schema: 1, settings: {}, families: [], aliases: [], rules: [] };
    if (!object(draft.settings)) add('settings', 'required', 'Settings must be an object.');
    const settings = object(draft.settings) ? draft.settings : {};
    for (const field of ['whitelist_enabled', 'rewrite_response_model']) {
      if (typeof settings[field] !== 'boolean') add(`settings.${field}`, 'invalid_type', 'Expected true or false.');
      output.settings[field] = settings[field];
    }
    if (!Number.isInteger(settings.refresh_interval_seconds) || settings.refresh_interval_seconds < 0 ||
      (settings.refresh_interval_seconds > 0 && settings.refresh_interval_seconds < 30)) {
      add('settings.refresh_interval_seconds', 'invalid_refresh_interval', 'Use 0 (host default) or an integer of at least 30.');
    }
    output.settings.refresh_interval_seconds = settings.refresh_interval_seconds;
    const seen = new Set();
    for (const [section, prefix] of [['families', 'fam'], ['aliases', 'al'], ['rules', 'r']]) {
      if (!Array.isArray(draft[section])) { add(section, 'required', 'Expected an array.'); continue; }
      draft[section].forEach((entry, index) => {
        const path = `${section}[${index}]`;
        if (!object(entry)) { add(path, 'invalid_type', 'Expected an object.'); return; }
        const id = entry.id === undefined ? `${prefix}_${randomUUID()}` : entry.id;
        if (typeof id !== 'string' || !id.trim() || id.trim() !== id || /[\x00-\x1f\x7f]/.test(id) || (section === 'families' && id.toLowerCase().startsWith('builtin:'))) add(`${path}.id`, 'invalid_id', 'Expected a stable, non-built-in ID.');
        if (seen.has(id)) add(`${path}.id`, 'duplicate_id', 'IDs must be unique.');
        seen.add(id);
        const requiredText = (field) => {
          if (typeof entry[field] !== 'string' || !entry[field].trim()) add(`${path}.${field}`, 'required', 'Expected nonempty text.');
          return typeof entry[field] === 'string' ? entry[field] : '';
        };
        const bool = (field, fallback) => {
          if (entry[field] !== undefined && typeof entry[field] !== 'boolean') add(`${path}.${field}`, 'invalid_type', 'Expected true or false.');
          return entry[field] ?? fallback;
        };
        if (typeof entry.provider === 'string' && (entry.provider.trim() !== entry.provider || /[\x00-\x1f\x7f]/.test(entry.provider))) add(`${path}.provider`, 'invalid_provider', 'Invalid provider.');
        if (entry.provider !== undefined && typeof entry.provider !== 'string') add(`${path}.provider`, 'invalid_type', 'Expected provider text.');
        if (section === 'families') {
          const name = requiredText('name'), template = requiredText('template');
          if ((template.match(/\{version\}/g) ?? []).length !== 1 || (template.match(/\{date\}/g) ?? []).length > 1 ||
            /[{}]/.test(template.replaceAll('{version}', '').replaceAll('{date}', '')) || /[/()]/.test(template)) {
            add(`${path}.template`, 'invalid_template', 'Use exactly one {version}, at most one {date}, and no unknown tokens, slash, or thinking suffix.');
          }
          output.families.push({ id, name, template, provider: entry.provider ?? '', allow_dated: bool('allow_dated', true) });
        } else if (section === 'aliases') {
          const name = requiredText('name'), target = requiredText('target');
          if (typeof entry.name === 'string' && entry.name.trim() !== name) add(`${path}.name`, 'invalid_alias_name', 'Whitespace is not allowed.');
          if (typeof entry.target === 'string' && (entry.target.trim() !== target || /[\s()*?\\]/.test(target))) add(`${path}.target`, 'invalid_target', 'Use a model ID.');
          if (/[\s/()*?\\]/.test(name)) add(`${path}.name`, 'invalid_alias_name', 'Alias names cannot contain whitespace, prefixes, or thinking suffixes.');
          if (models.some((model) => key(model.id) === key(name)) || output.aliases.some((alias) => key(alias.name) === key(name))) add(`${path}.name`, 'alias_conflict', 'Alias name must be unique and cannot equal a catalog model ID.');
          if (!['auto', 'pinned'].includes(entry.mode)) add(`${path}.mode`, 'invalid_mode', 'Use pinned or auto.');
          if (entry.rewrite_response_model !== undefined && entry.rewrite_response_model !== null && typeof entry.rewrite_response_model !== 'boolean') add(`${path}.rewrite_response_model`, 'invalid_type', 'Use true, false, or null.');
          const alias = { auto: {family:'',include_prerelease:false,health_check:false}, id, name, target, mode: entry.mode, enabled: bool('enabled', true), provider: entry.provider ?? '',
            expose_original: bool('expose_original', false), rewrite_response_model: entry.rewrite_response_model ?? null };
          if (entry.auto !== undefined && !object(entry.auto)) add(`${path}.auto`, 'invalid_type', 'Expected an object.');
          if (entry.mode === 'auto' && !object(entry.auto)) add(`${path}.auto`, 'required', 'Automatic aliases require a family.');
          if (object(entry.auto)) {
            if (entry.auto.family !== undefined && typeof entry.auto.family !== 'string') add(`${path}.auto.family`, 'invalid_type', 'Expected family text.');
            alias.auto = { family: entry.auto.family ?? '', include_prerelease: entry.auto.include_prerelease ?? false, health_check: entry.auto.health_check ?? false };
            for (const field of ['include_prerelease', 'health_check']) if (entry.auto[field] !== undefined && typeof entry.auto[field] !== 'boolean') add(`${path}.auto.${field}`, 'invalid_type', 'Expected true or false.');
          }
          output.aliases.push(alias);
        } else {
          const value = requiredText('value');
          if (!['exact', 'family', 'pattern', 'alias'].includes(entry.kind)) add(`${path}.kind`, 'invalid_rule_kind', 'Use exact, family, pattern, or alias.');
          if (entry.note !== undefined && typeof entry.note !== 'string') add(`${path}.note`, 'invalid_type', 'Expected note text.');
          output.rules.push({ id, enabled: bool('enabled', true), kind: entry.kind, value, note: entry.note ?? '' });
        }
      });
    }
    output.aliases.forEach((alias, index) => {
      if (output.aliases.some((other) => normalize(other.name) === normalize(alias.target))) add(`aliases[${index}].target`, 'alias_target_alias', 'Alias targets must be real model IDs.');
      const family = familyById(output,alias.auto?.family);
      if (alias.mode === 'auto' && family?.provider && alias.provider && key(family.provider)!==key(alias.provider)) add(`aliases[${index}].provider`, 'provider_conflict', 'Alias and family provider restrictions conflict.');
      if (alias.mode === 'auto' && !family) add(`aliases[${index}].auto.family`, 'unknown_family', 'Family does not exist.');
    });
    output.rules.forEach((rule, index) => {
      if (rule.kind === 'exact' && /[*?]/.test(rule.value)) add(`rules[${index}].value`, 'invalid_exact', 'Exact rules cannot contain wildcards.');
      if (rule.kind === 'pattern' && /[\\()]/.test(rule.value)) add(`rules[${index}].value`, 'invalid_pattern', 'Invalid pattern.');
      if (rule.kind === 'family' && !familyById(output, rule.value)) add(`rules[${index}].value`, 'unknown_family', 'Family does not exist.');
      if (rule.kind === 'alias' && !output.aliases.some((alias) => key(alias.name) === key(rule.value))) add(`rules[${index}].value`, 'unknown_alias', 'Alias does not exist.');
    });
    return { policy: output, details };
  }
  function evaluate(alias) {
    const result = { alias_id: alias.id, current_target: alias.target, candidates: [], last_checked_at: timestamp(),
      last_result: 'kept', reason: reason('pinned', 'Pinned aliases are not automatically moved.') };
    if (!policyAvailable) { result.reason=reason('policy_unavailable','Policy is unavailable.'); return result; }
    if (!alias.enabled) { result.reason = reason('disabled', 'Alias is disabled.'); return result; }
    if (alias.mode !== 'auto') return result;
    const family = familyById(state, alias.auto.family);
    const candidates = models.flatMap((model) => {
      const parsed = parseFamily(model.id, family, alias.auto.include_prerelease);
      if (!parsed) return [];
      const canRoute = model.routable && (!alias.provider || model.providers.some((provider) => key(provider) === key(alias.provider))) && (!family.provider || model.providers.some((provider) => key(provider) === key(family.provider)));
      return [{ id: model.id, version: parsed.version, routable: Boolean(canRoute), chosen: false,
        reason: reason(canRoute ? 'candidate' : 'not_routable', canRoute ? 'Matching routable candidate.' : 'No matching enabled credential.'), parsed }];
    });
    candidates.sort((a, b) => -(compare(a.parsed, b.parsed) ?? 0) || a.id.localeCompare(b.id));
    const current = parseFamily(alias.target, family, true);
    const eligible = candidates.filter((candidate) => candidate.routable);
    const top = eligible[0];
    if (!current) result.reason = reason('current_unparseable', 'Current target does not parse under this family.');
    else if (!catalogStatus.ok) result.reason = reason('catalog_unavailable','Catalog refresh failed; keeping current target.');
    else if (!top) result.reason = reason('no_routable_candidates', 'No matching candidate is routable.');
    else if (eligible.slice(1).some((candidate) => { const order = compare(top.parsed, candidate.parsed); return order === null || order === 0; })) result.reason = reason('ambiguous', 'Top candidates are not strictly ordered; keeping current target.');
    else if (compare(top.parsed, current) === null) result.reason = reason('ambiguous', 'Candidate and current target are not strictly ordered.');
    else if (compare(top.parsed, current) <= 0) { result.last_result = 'unchanged'; result.reason = reason('up_to_date', 'No strictly newer routable candidate.'); }
    else if (alias.auto.health_check) result.reason = reason('health_check_unverified', 'Mock cannot perform paid health probes; keeping current target.');
    else {
      result.current_target = top.id; result.last_result = 'updated';
      result.reason = reason('newer_version', 'Selected the highest strictly newer routable version.');
      top.chosen = true; top.reason = reason('selected', 'Highest strictly newer routable candidate.');
    }
    result.candidates = candidates.map(({ parsed, ...candidate }) => candidate);
    return result;
  }
  function record(actor, kind, summary, before, after) {
    history.unshift({ id: `hist_${++historySequence}`, at: timestamp(), actor, kind, summary, before: clone(before), after: clone(after) });
    history = history.slice(0, 1000);
  }
  function bump() { state.revision += 1; state.updated_at = timestamp(); }
  function error(status, code, message, extra = {}) { return { status, body: { error: { code, message, ...extra } } }; }
  const send = (response, status, body) => {
    response.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8', 'Cache-Control': 'no-store' });
    response.end(JSON.stringify(body));
  };
  function requireRevision(request, body) {
    const header = request.headers['if-match'];
    const match = typeof header === 'string' ? /^"(\d+)"$/.exec(header.trim()) : null;
    const revision = Object.hasOwn(body, 'revision') ? body.revision : (match ? Number(match[1]) : undefined);
    if (!Number.isInteger(revision) || revision !== state.revision || (header && (!match || +match[1] !== state.revision))) {
      return { status: 409, body: { error: { code: 'revision_conflict', message: 'Fetch current state and retry.' }, state: getState() } };
    }
  }
  async function readBody(request) {
    const chunks = []; let size = 0;
    for await (const chunk of request) {
      chunks.push(chunk); size += chunk.length;
      if (size > 1024 * 1024) throw Object.assign(new Error('JSON body exceeds 1 MiB.'), { status: 413, code: 'body_too_large' });
    }
    const text = Buffer.concat(chunks).toString('utf8');
    let body;
    try { body = text ? JSON.parse(text) : {}; } catch { throw Object.assign(new Error('Malformed JSON.'), { status: 400, code: 'invalid_json' }); }
    if (!object(body)) throw Object.assign(new Error('JSON body must be an object.'), { status: 400, code: 'invalid_json' });
    return body;
  }
  return http.createServer(async (request, response) => {
    try {
      const url = new URL(request.url, 'http://localhost');
      if (url.pathname.startsWith(RESOURCE_BASE)) {
        const file = url.pathname.slice(RESOURCE_BASE.length);
        const types = { 'index.html': 'text/html', 'app.js': 'text/javascript', 'app.css': 'text/css', 'i18n.js': 'text/javascript' };
        const mime = Object.hasOwn(types, file) ? types[file] : undefined;
        if (!mime) return send(response, 404, error(404, 'not_found', 'Resource not found.').body);
        if (!['GET', 'HEAD'].includes(request.method)) { response.setHeader('Allow','GET, HEAD'); return send(response,405,error(405,'method_not_allowed','Method not allowed.').body); }
        try {
          const content = await readFile(resolve(assetsDir, file));
          response.writeHead(200, { 'Content-Type': `${mime}; charset=utf-8`, 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff' });
          return response.end(request.method === 'HEAD' ? undefined : content);
        } catch (failure) { if (failure.code !== 'ENOENT') throw failure; return send(response, 404, error(404, 'not_found', 'Resource not found.').body); }
      }
      if (!url.pathname.startsWith(`${API_BASE}/`)) return send(response, 404, error(404, 'not_found', 'Route not found.').body);
      if (request.headers.authorization !== 'Bearer test-key') return send(response, 401, error(401, 'unauthorized', 'A valid management Bearer key is required.').body);
      const route = `${request.method} ${url.pathname.slice(API_BASE.length)}`;
      const routes = ['GET /overview', 'GET /state', 'PUT /state', 'POST /validate', 'GET /catalog', 'POST /catalog/refresh',
        'POST /aliases/check', 'POST /rules/preview', 'POST /explain', 'GET /history', 'POST /history/restore'];
      if (!routes.includes(route)) {
        const methods = routes.filter((item) => item.split(' ')[1] === url.pathname.slice(API_BASE.length)).map((item) => item.split(' ')[0]);
        if (methods.length) { response.setHeader('Allow', methods.join(', ')); return send(response, 405, error(405, 'method_not_allowed', 'Method not allowed.').body); }
        return send(response, 404, error(404, 'not_found', 'Route not found.').body);
      }
      const body = ['PUT', 'POST'].includes(request.method) ? await readBody(request) : {};
      if (route === 'POST /aliases/check' && body.dry_run !== undefined && typeof body.dry_run !== 'boolean') return send(response, 422, error(422, 'validation_failed', 'Invalid dry_run.', {details:[{path:'dry_run',code:'invalid_type',message:'Expected true or false.'}]}).body);
      const mutating = ['PUT /state', 'POST /history/restore'].includes(route) || (route === 'POST /aliases/check' && body.dry_run !== true);
      if (mutating) { const conflict = requireRevision(request, body); if (conflict) return send(response, conflict.status, conflict.body); }
      let result;
      if (route === 'GET /state') result = getState();
      else if (route === 'GET /catalog') result = catalog();
      else if (route === 'GET /overview') {
        const lists = publicLists();
        result = { plugin_version: '0.1.0-mock', host_schema: 7, policy_enabled: policyAvailable, whitelist_enabled: state.settings.whitelist_enabled,
          counts: { public_models: lists.public_models.length, public_aliases: lists.public_aliases.length },
          active_mappings: state.aliases.filter((alias) => alias.enabled).map((alias) => ({ alias: alias.name, target: alias.target })),
          recent_auto_updates: clone(history.filter((entry) => entry.actor === 'auto' && entry.kind === 'alias_target').slice(0, 5)),
          catalog: clone(catalogStatus), guard: { enabled: true, verified: false,
            limitations: ['Realtime/live guard order requires verification on a real CPA instance.', 'WebSocket continuation frames, Gemini model detail, and management api-call cannot be intercepted.'] },
          warnings: ['In-memory development mock; no live CPA routing or protection is verified.', 'Use a reverse proxy for endpoints that CPA plugin hooks cannot intercept.'] };
      } else if (route === 'PUT /state' || route === 'POST /validate' || route === 'POST /rules/preview' || route === 'POST /explain') {
        const draft = route === 'POST /explain' ? body.draft ?? {} : body;
        if (!object(draft)) return send(response, 422, error(422, 'validation_failed', 'Invalid draft.', { details: [{ path: 'draft', code: 'invalid_type', message: 'Expected an object.' }] }).body);
        const checked = validate(draft, route === 'POST /rules/preview' || route === 'POST /explain');
        if (route === 'POST /validate') result = { valid: checked.details.length === 0, details: checked.details,
          preview: checked.details.length ? { public_models: [], public_aliases: [] } : publicLists(checked.policy, true) };
        else if (checked.details.length) return send(response, 422, error(422, 'validation_failed', 'Policy validation failed.', { details: checked.details }).body);
        else if (route === 'PUT /state') {
          const before = savedPolicy(state);
          state = { ...checked.policy, revision: state.revision, updated_at: timestamp() }; bump();
          policyAvailable = true;
          aliasStatus = {};
          record('user', 'state', 'Saved policy state', before, savedPolicy(state)); result = getState();
        } else if (route === 'POST /rules/preview') result = { rules: checked.policy.rules.map((rule) => ({ id: rule.id,
          matches: (rule.kind === 'alias' ? checked.policy.aliases.map((alias) => alias.name) : models.map((model) => model.id)).filter((name) => ruleMatches(rule, normalize(name), checked.policy)).sort() })), ...publicLists(checked.policy, true) };
        else {
          if (typeof body.model !== 'string' || !body.model.trim()) return send(response, 422, error(422, 'validation_failed', 'A model is required.', { details: [{ path: 'model', code: 'required', message: 'Expected a model name.' }] }).body);
          result = decide(body.model, checked.policy, body.draft === undefined ? policyAvailable : true);
        }
      } else if (route === 'POST /catalog/refresh') {
        const previousRefresh = refreshQueue;
        let finishRefresh;
        refreshQueue = new Promise((resolve) => { finishRefresh = resolve; });
        await previousRefresh;
        let nextModels;
        try { nextModels = options.refreshCatalog ? await options.refreshCatalog(clone(models)) : clone(models); }
        catch { nextModels = null; }
        catalogStatus = { last_refresh_at: timestamp(), ok: Array.isArray(nextModels), error: Array.isArray(nextModels) ? null : 'Mock catalog refresh failed; last good snapshot retained.' };
        if (Array.isArray(nextModels)) models = clone(nextModels);
        result = catalog(); finishRefresh();
      } else if (route === 'POST /aliases/check') {
        if (body.dry_run !== undefined && typeof body.dry_run !== 'boolean') return send(response, 422, error(422, 'validation_failed', 'Invalid dry_run.', { details: [{ path: 'dry_run', code: 'invalid_type', message: 'Expected true or false.' }] }).body);
        if (body.alias_id !== undefined && typeof body.alias_id !== 'string') return send(response, 422, error(422, 'validation_failed', 'Invalid alias_id.', {details:[{path:'alias_id',code:'invalid_type',message:'Expected text.'}]}).body);
        const aliases = body.alias_id === undefined || body.alias_id === '' ? state.aliases : state.aliases.filter((alias) => alias.id === body.alias_id);
        if (body.alias_id && !aliases.length) return send(response, 404, error(404, 'alias_not_found', 'Alias not found.').body);
        const results = aliases.map(evaluate);
        if (!body.dry_run) {
          for (const item of results) {
            const alias = state.aliases.find((entry) => entry.id === item.alias_id);
            if (item.last_result === 'updated') {
              record('auto', 'alias_target', `${alias.name}: ${alias.target} → ${item.current_target}`,
                { alias_id: alias.id, target: alias.target }, { alias_id: alias.id, target: item.current_target });
              alias.target = item.current_target;
            }
            const { alias_id, ...status } = item; aliasStatus[alias_id] = clone(status);
          }
          if (results.some((item) => item.last_result === 'updated')) bump();
        }
        result = { results, revision: state.revision };
      } else if (route === 'GET /history') {
        const limit = url.searchParams.has('limit') ? Number(url.searchParams.get('limit')) : 100;
        if (url.searchParams.getAll('limit').length > 1 || !/^\d+$/.test(url.searchParams.get('limit') ?? '100') || !Number.isInteger(limit) || limit < 1 || limit > 1000) return send(response, 400, error(400, 'invalid_limit', 'Use an integer limit between 1 and 1000.').body);
        const before = url.searchParams.get('before');
        const index = before ? history.findIndex((entry) => entry.id === before) : -1;
        if (url.searchParams.getAll('before').length > 1 || (before && index < 0)) return send(response, 400, error(400, 'invalid_cursor', 'Unknown history entry cursor.').body);
        const remaining = history.slice(index + 1), entries = remaining.slice(0, limit);
        result = { entries: clone(entries), next_before: remaining.length > limit ? entries.at(-1).id : null };
      } else if (route === 'POST /history/restore') {
        const details=[];
        if (typeof body.entry_id !== 'string' || !body.entry_id.trim()) details.push({path:'entry_id',code:'required',message:'A history entry is required.'});
        if (!['state','alias_target'].includes(body.scope)) details.push({path:'scope',code:'invalid_scope',message:'Use state or alias_target.'});
        if (details.length) return send(response,422,error(422,'validation_failed','Invalid restore.',{details}).body);
        const entry = history.find((item) => item.id === body.entry_id);
        if (!entry) return send(response, 404, error(404, 'entry_not_found', 'History entry not found.').body);
        if (body.scope !== entry.kind) return send(response, 422, error(422, 'validation_failed', 'Scope must match entry kind.', { details: [{ path: 'scope', code: 'invalid_scope', message: `Use ${entry.kind} for this entry.` }] }).body);
        const before = savedPolicy(state);
        if (body.scope === 'state') {
          const checked = validate(entry.before);
          if (checked.details.length) return send(response, 422, error(422, 'validation_failed', 'Historical policy is invalid.', { details: checked.details }).body);
          state = { ...checked.policy, revision: state.revision, updated_at: timestamp() }; aliasStatus = {};
        } else {
          const alias = state.aliases.find((item) => item.id === entry.before.alias_id);
          if (!alias) return send(response, 422, error(422, 'validation_failed', 'Historical alias no longer exists.', { details: [{ path: 'entry_id', code: 'alias_not_found', message: 'Restore the containing state first.' }] }).body);
          alias.target = entry.before.target; delete aliasStatus[alias.id];
        }
        bump();
        record('user', body.scope, `Restored before ${entry.id} (${body.scope})`,
          body.scope === 'state' ? before : { alias_id: entry.before.alias_id, target: before.aliases.find((alias) => alias.id === entry.before.alias_id).target },
          body.scope === 'state' ? savedPolicy(state) : clone(entry.before));
        result = getState();
      }
      send(response, 200, result);
    } catch (failure) {
      send(response, failure.status ?? 500, error(failure.status ?? 500, failure.code ?? 'internal_error', failure.status ? failure.message : 'Mock request failed.').body);
    }
  });
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const port = Number(process.env.PORT ?? 8787);
  const server = createMockServer();
  server.listen(port, '127.0.0.1', () => {
    console.log(`Model Dock mock: http://127.0.0.1:${server.address().port}${RESOURCE_BASE}index.html`);
    console.log('Management API key: test-key (development only; state resets on restart).');
  });
}
