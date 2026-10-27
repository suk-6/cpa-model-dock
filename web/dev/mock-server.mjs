import { parseFamilyName } from '../assets/app.js';
import http from 'node:http';
import { readFile } from 'node:fs/promises';
import { randomUUID } from 'node:crypto';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { resolve } from 'node:path';
import { isDeepStrictEqual } from 'node:util';

export const API_BASE = '/v0/management/plugins/cpa-model-dock';
export const RESOURCE_BASE = '/v0/resource/plugins/cpa-model-dock/';
const clone = (value) => structuredClone(value);
const object = (value) => value !== null && typeof value === 'object' && !Array.isArray(value);
const key = (value) => String(value ?? '').trim().toLowerCase();
const reason = (code, message, params = {}) => ({ code, params, message });
const detail = (path, code, message, params = {}) => ({ path, ...reason(code, message, { ...params, field: path }) });
const escape = (value) => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

function summarize(before, after) {
  const changes = [];
  const add = (code, message, params) => changes.push(reason(code, message, params));
  for (const section of ['aliases', 'rules']) {
    const oldRows = new Map(before[section].map((row) => [row.id, row]));
    const newRows = new Map(after[section].map((row) => [row.id, row]));
    for (const id of [...new Set([...oldRows.keys(), ...newRows.keys()])].sort()) {
      const old = oldRows.get(id), next = newRows.get(id);
      if (section === 'aliases') {
        const params = { alias_id: id, alias: (next ?? old).name };
        if (!old) add('alias_created', `Created alias ${next.name} → ${next.target}`, { ...params, target: next.target });
        else if (!next) add('alias_deleted', `Deleted alias ${old.name}`, params);
        else {
          if (old.name !== next.name) add('alias_renamed', `Renamed alias ${old.name} to ${next.name}`, { alias_id: id, from: old.name, to: next.name });
          if (old.enabled !== next.enabled) add(next.enabled ? 'alias_enabled' : 'alias_disabled', `${next.enabled ? 'Enabled' : 'Disabled'} alias ${next.name}`, params);
          if (old.target !== next.target) add('alias_target_changed', `Changed alias ${next.name} target from ${old.target} to ${next.target}`, { ...params, from: old.target, to: next.target });
          const fields = ['mode','provider','expose_original','rewrite_response_model','auto.family','auto.include_prerelease','auto.health_check'].flatMap(field => {
            const read = row => field.split('.').reduce((value,key) => value?.[key],row) ?? null;
            const from = read(old), to = read(next);
            return isDeepStrictEqual(from,to) ? [] : [{field,from,to}];
          });
          if (fields.length) add('alias_changed', `Changed alias ${next.name}: ${fields.slice(0,3).map(field => `${field.field}: ${field.from ?? 'default'} → ${field.to ?? 'default'}`).join('; ')}${fields.length > 3 ? `; +${fields.length-3} more` : ''}`, {...params,fields,count:fields.length});
        }
      } else {
        const row = next ?? old, params = { rule_id: id, kind: row.kind, value: row.value };
        const label = `${row.kind} rule ${row.value} (${id})`;
        if (!old) add('rule_added', `Added ${label}`, params);
        else if (!next) add('rule_removed', `Removed ${label}`, params);
        else {
          if (old.enabled !== next.enabled) add(next.enabled ? 'rule_enabled' : 'rule_disabled', `${next.enabled ? 'Enabled' : 'Disabled'} ${label}`, params);
          if (!isDeepStrictEqual({ ...old, enabled: next.enabled }, next)) add('rule_changed', `Changed ${label}`, params);
        }
      }
    }
  }
  if (before.settings.whitelist_enabled !== after.settings.whitelist_enabled) {
    add(after.settings.whitelist_enabled ? 'whitelist_enabled' : 'whitelist_disabled', `${after.settings.whitelist_enabled ? 'Enabled' : 'Disabled'} whitelist`);
  }
  if (!isDeepStrictEqual({ ...before.settings, whitelist_enabled: after.settings.whitelist_enabled }, after.settings)) add('settings_changed', 'Changed settings');
  const orderedFamilies = (rows) => [...rows].sort((a, b) => a.id < b.id ? -1 : a.id > b.id ? 1 : 0);
  if (!isDeepStrictEqual(orderedFamilies(before.families), orderedFamilies(after.families))) add('families_changed', 'Changed model families');
  if (!changes.length) return reason('policy_unchanged', 'Saved policy without changes');
  if (changes.length === 1) return changes[0];
  return reason('changes', `${changes.length} changes: ${changes.map((change) => change.message).join('; ')}`, { count: changes.length, changes });
}
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
  const parsed = parseFamilyName(key(id), family, includePrerelease);
  if (!parsed) return null;
  return { ...parsed, components: parsed.version.split(/[.-]/).map(Number),
    dateWidth: parsed.date?.length ?? null, date: parsed.date ? dateNumber(parsed.date) : null };
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
  let catalogStatus = { last_refresh_at: timestamp(), last_good_at: models.length ? timestamp() : null, ok: models.length > 0, stale:false, warning:models.length ? '' : 'no_models', warning_detail:models.length ? null : reason('no_models','No models are available yet.'), error: null, error_detail: null };
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
    const name = normalize(input), steps = available ? [reason('normalized', `Matched as ${name}`, { model: name })] : [];
    const finish = (allowed, status, code, message, resolvedTarget, params = {}) => ({ allowed, status, code, reason: reason(code, message, params),
      steps: [...steps, reason(code, message, params)], ...(resolvedTarget ? { resolved_target: resolvedTarget } : {}) });
    if (!available) return finish(false, 503, 'policy_unavailable', 'No valid policy is available; save a valid state to recover.');
    const alias = policy.aliases.find((entry) => entry.enabled && normalize(entry.name) === name);
    if (alias) {
      if (policy.settings.whitelist_enabled && !policy.rules.some((rule) => rule.kind === 'alias' && ruleMatches(rule, name, policy))) {
        return finish(false, 403, 'alias_not_published', 'No enabled alias rule publishes this alias.');
      }
      if (!routable(alias)) return finish(false, 503, 'alias_target_unavailable', 'Alias target has no matching enabled credential.');
      return finish(true, 200, 'alias', 'Published alias routes to its exact target.', alias.target, { alias: alias.name, target: alias.target });
    }
    const exact = policy.rules.find((rule) => rule.kind === 'exact' && ruleMatches(rule, name, policy));
    if (!exact && policy.aliases.some((entry) => entry.enabled && !entry.expose_original && normalize(entry.target) === name)) {
      return finish(false, 404, 'hidden_by_alias', 'This name is not public.');
    }
    if (!policy.settings.whitelist_enabled) return finish(true, 200, 'whitelist_disabled', 'Whitelist is disabled.');
    const rule = policy.rules.find((entry) => entry.kind !== 'alias' && ruleMatches(entry, name, policy));
    return rule ? finish(true, 200, 'rule_allowed', 'An enabled allow rule matches.', undefined, { rule_id: rule.id }) :
      finish(false, 403, 'not_whitelisted', 'No enabled allow rule matches.');
  }
  function publicLists(policy = state, available = policyAvailable) {
    return {
      public_models: models.filter((model) => model.routable && decide(model.id, policy, available).allowed).map((model) => model.id).sort(),
      public_aliases: policy.aliases.filter((alias) => alias.enabled && decide(alias.name, policy, available).allowed).map((alias) => alias.name).sort(),
    };
  }
  function catalog() {
    return { refreshed_at: catalogStatus.last_refresh_at, ok: catalogStatus.ok, stale:catalogStatus.stale, warning:catalogStatus.warning, warning_detail:catalogStatus.warning_detail, last_good_at:catalogStatus.last_good_at, error: catalogStatus.error, error_detail: catalogStatus.error_detail,
      models: models.map((model) => { const decision = decide(model.id); return { id:model.id, providers:clone(model.providers ?? []), credential_count:model.credential_count ?? 0, routable:Boolean(model.routable),
        public: Boolean(model.routable && decision.allowed), reason: !model.routable ?
          reason('not_routable', 'Known in catalog, but no enabled credential advertises it.') : decision.reason }; }) };
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
    const add = (path, code, message, params) => details.push(detail(path, code, message, params));
    const output = { schema: 1, settings: {}, families: [], aliases: [], rules: [] };
    if (!object(draft.settings)) add('settings', draft.settings === undefined ? 'required_full_state_field' : 'invalid_type', 'Settings must be an object.', draft.settings === undefined ? {} : { expected: 'object' });
    const settings = object(draft.settings) ? draft.settings : {};
    for (const field of ['whitelist_enabled', 'rewrite_response_model']) {
      if (typeof settings[field] !== 'boolean') add(`settings.${field}`, settings[field] === undefined ? 'required_setting' : 'invalid_type', 'Expected true or false.', settings[field] === undefined ? {} : { expected: 'boolean' });
      output.settings[field] = settings[field];
    }
    if (settings.refresh_interval_seconds === undefined) add('settings.refresh_interval_seconds', 'required_setting', 'Refresh interval is required.');
    else if (!Number.isInteger(settings.refresh_interval_seconds)) add('settings.refresh_interval_seconds', 'invalid_type', 'Expected an integer.', { expected: 'integer' });
    else if (settings.refresh_interval_seconds < 0 || (settings.refresh_interval_seconds > 0 && settings.refresh_interval_seconds < 30)) {
      add('settings.refresh_interval_seconds', 'invalid_refresh_interval', 'Use 0 (host default) or an integer of at least 30.');
    }
    output.settings.refresh_interval_seconds = settings.refresh_interval_seconds;
    const seen = new Set();
    for (const [section, prefix] of [['families', 'fam'], ['aliases', 'al'], ['rules', 'r']]) {
      if (!Array.isArray(draft[section])) { add(section, draft[section] === undefined ? 'required_full_state_field' : 'invalid_type', 'Expected an array of objects.', draft[section] === undefined ? {} : { expected: 'array_of_objects' }); continue; }
      draft[section].forEach((entry, index) => {
        const path = `${section}[${index}]`;
        if (!object(entry)) { add(path, 'invalid_type', 'Expected an object.', { expected: 'object' }); return; }
        const id = entry.id === undefined ? `${prefix}_${randomUUID()}` : entry.id;
        if (typeof id !== 'string') add(`${path}.id`, 'invalid_type', 'Expected ID text.', { expected: 'string' });
        else if (!id.trim()) add(`${path}.id`, 'required_id', 'ID is required.');
        else if (id.trim() !== id || /[\x00-\x1f\x7f]/.test(id)) add(`${path}.id`, 'invalid_id', 'Expected a stable ID.');
        if (section === 'families' && typeof id === 'string' && id.toLowerCase().startsWith('builtin:')) add(`${path}.id`, 'builtin_read_only', 'Built-in families are read-only.');
        if (seen.has(id)) add(`${path}.id`, 'duplicate_id', 'IDs must be unique.');
        seen.add(id);
        const requiredText = (field) => {
          if (entry[field] !== undefined && typeof entry[field] !== 'string') add(`${path}.${field}`, 'invalid_type', 'Expected text.', { expected: 'string' });
          else if (field !== 'template' && !entry[field]?.trim()) {
            const code = `required_${section === 'families' ? 'family' : section === 'aliases' ? 'alias' : 'rule'}_${field}`;
            add(`${path}.${field}`, code, 'Expected nonempty text.');
          }
          return typeof entry[field] === 'string' ? entry[field] : '';
        };
        const bool = (field, fallback) => {
          if (entry[field] !== undefined && typeof entry[field] !== 'boolean') add(`${path}.${field}`, 'invalid_type', 'Expected true or false.', { expected: 'boolean' });
          return entry[field] ?? fallback;
        };
        if (typeof entry.provider === 'string' && (entry.provider.trim() !== entry.provider || /[\x00-\x1f\x7f]/.test(entry.provider))) add(`${path}.provider`, 'invalid_provider', 'Invalid provider.');
        if (entry.provider !== undefined && typeof entry.provider !== 'string') add(`${path}.provider`, 'invalid_type', 'Expected provider text.', { expected: 'string' });
        if (section === 'families') {
          const name = requiredText('name'), template = requiredText('template');
          if (template.trim() !== template || (template.match(/\{version\}/g) ?? []).length !== 1 || (template.match(/\{date\}/g) ?? []).length > 1) {
            add(`${path}.template`, 'invalid_template_tokens', 'Use exactly one {version}, at most one {date}, and no unknown tokens.');
          }
          else if (/[{}*?/()\\\s\x00-\x1f\x7f]/.test(template.replaceAll('{version}', '').replaceAll('{date}', '')) || /\{version\}\{date\}|\{date\}\{version\}/.test(template)) add(`${path}.template`, 'invalid_template_literal', 'Template literals cannot contain wildcards, whitespace, slashes, thinking suffixes, or adjacent tokens.');
          output.families.push({ id, name, template, provider: entry.provider ?? '', allow_dated: bool('allow_dated', true) });
        } else if (section === 'aliases') {
          const name = requiredText('name'), target = requiredText('target');
          if (typeof entry.name === 'string' && entry.name.trim() !== name) add(`${path}.name`, 'invalid_alias_name', 'Whitespace is not allowed.');
          if (typeof entry.target === 'string' && (entry.target.trim() !== target || /[\s()*?\\]/.test(target))) add(`${path}.target`, 'invalid_target', 'Use a model ID.');
          if (/[\s()*?\\]/.test(name)) add(`${path}.name`, 'invalid_alias_name', 'Alias names cannot contain whitespace or thinking suffixes.');
          if (name.includes('/')) add(`${path}.name`, 'invalid_alias_slash', 'Alias names cannot contain a slash.');
          if (models.some((model) => key(model.id) === key(name))) add(`${path}.name`, 'alias_catalog_conflict', 'Alias name conflicts with a catalog model ID.');
          if (output.aliases.some((alias) => key(alias.name) === key(name))) add(`${path}.name`, 'alias_name_conflict', 'Alias names must be unique.');
          if (entry.mode !== undefined && typeof entry.mode !== 'string') add(`${path}.mode`, 'invalid_type', 'Expected mode text.', { expected: 'string' });
          else if (!['auto', 'pinned'].includes(entry.mode)) add(`${path}.mode`, 'invalid_mode', 'Use pinned or auto.');
          if (entry.rewrite_response_model !== undefined && entry.rewrite_response_model !== null && typeof entry.rewrite_response_model !== 'boolean') add(`${path}.rewrite_response_model`, 'invalid_type', 'Use true, false, or null.', { expected: 'boolean' });
          const alias = { auto: {family:'',include_prerelease:false,health_check:false}, id, name, target, mode: entry.mode, enabled: bool('enabled', true), provider: entry.provider ?? '',
            expose_original: bool('expose_original', false), rewrite_response_model: entry.rewrite_response_model ?? null };
          if (entry.auto !== undefined && !object(entry.auto)) add(`${path}.auto`, 'invalid_type', 'Expected an object.', { expected: 'object' });
          if (object(entry.auto)) {
            if (entry.auto.family !== undefined && typeof entry.auto.family !== 'string') add(`${path}.auto.family`, 'invalid_type', 'Expected family text.', { expected: 'string' });
            alias.auto = { family: entry.auto.family ?? '', include_prerelease: entry.auto.include_prerelease ?? false, health_check: entry.auto.health_check ?? false };
            for (const field of ['include_prerelease', 'health_check']) if (entry.auto[field] !== undefined && typeof entry.auto[field] !== 'boolean') add(`${path}.auto.${field}`, 'invalid_type', 'Expected true or false.', { expected: 'boolean' });
          }
          output.aliases.push(alias);
        } else {
          const value = requiredText('value');
          if (entry.kind !== undefined && typeof entry.kind !== 'string') add(`${path}.kind`, 'invalid_type', 'Expected rule kind text.', { expected: 'string' });
          else if (!['exact', 'family', 'pattern', 'alias'].includes(entry.kind)) add(`${path}.kind`, 'invalid_rule_kind', 'Use exact, family, pattern, or alias.');
          if (entry.note !== undefined && typeof entry.note !== 'string') add(`${path}.note`, 'invalid_type', 'Expected note text.', { expected: 'string' });
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
      result.reason = reason('newer_version', 'Selected the highest strictly newer routable version.', { from: alias.target, to: top.id });
      top.chosen = true; top.reason = reason('selected', 'Highest strictly newer routable candidate.');
    }
    result.candidates = candidates.map(({ parsed, ...candidate }) => candidate);
    return result;
  }
  function record(actor, kind, summaryDetail, before, after) {
    history.unshift({ id: `hist_${++historySequence}`, at: timestamp(), actor, kind, summary: summaryDetail.message, summary_detail: clone(summaryDetail), before: clone(before), after: clone(after) });
    history = history.slice(0, 1000);
  }
  function bump() { state.revision += 1; state.updated_at = timestamp(); }
  function error(status, code, message, extra = {}) { return { status, body: { error: { ...reason(code, message), ...extra } } }; }
  const send = (response, status, body) => {
    response.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8', 'Cache-Control': 'no-store' });
    response.end(JSON.stringify(body));
  };
  function requireRevision(request, body) {
    const header = request.headers['if-match'];
    const match = typeof header === 'string' ? /^"(\d+)"$/.exec(header.trim()) : null;
    const revision = Object.hasOwn(body, 'revision') ? body.revision : (match ? Number(match[1]) : undefined);
    if (!Number.isInteger(revision) || revision !== state.revision || (header && (!match || +match[1] !== state.revision))) {
      return { status: 409, body: { error: reason('revision_conflict', 'Fetch current state and retry.'), state: getState() } };
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
        const types = { 'index.html': 'text/html', 'app.js': 'text/javascript', 'app.css': 'text/css', 'i18n.js': 'text/javascript', 'favicon.svg': 'image/svg+xml' };
        const mime = Object.hasOwn(types, file) ? types[file] : undefined;
        if (!mime) return send(response, 404, error(404, 'not_found', 'Resource not found.').body);
        if (!['GET', 'HEAD'].includes(request.method)) { response.setHeader('Allow','GET, HEAD'); return send(response,405,error(405,'method_not_allowed','Method not allowed.').body); }
        try {
          const content = await readFile(resolve(assetsDir, file));
          response.writeHead(200, { 'Content-Type': `${mime}; charset=utf-8`, 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff',
            'Content-Security-Policy': "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'" });
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
      if (route === 'POST /aliases/check' && body.dry_run !== undefined && typeof body.dry_run !== 'boolean') return send(response, 422, error(422, 'validation_failed', 'Invalid dry_run.', {details:[detail('dry_run', 'invalid_type', 'Expected true or false.', { expected: 'boolean' })]}).body);
      const mutating = ['PUT /state', 'POST /history/restore'].includes(route) || (route === 'POST /aliases/check' && body.dry_run !== true);
      if (mutating) { const conflict = requireRevision(request, body); if (conflict) return send(response, conflict.status, conflict.body); }
      let result;
      if (route === 'GET /state') result = getState();
      else if (route === 'GET /catalog') result = catalog();
      else if (route === 'GET /overview') {
        const lists = publicLists();
        const warnings = [reason('priority_unverified', 'Plugin priority relative to other installed plugins must be verified on the host.')];
        if (catalogStatus.warning_detail) warnings.push(clone(catalogStatus.warning_detail));
        if (!policyAvailable) warnings.push(reason('policy_unavailable', 'Policy is unavailable; model requests are denied.'));
        for (const alias of state.aliases) if (alias.enabled && !routable(alias)) {
          warnings.push(reason('alias_target_unavailable', `Alias ${alias.name} target ${alias.target} has no matching enabled credential.`,
            { alias_id: alias.id, alias: alias.name, target: alias.target }));
        }
        result = { plugin_version: '0.1.0-mock', host_schema: 7, policy_enabled: policyAvailable, whitelist_enabled: state.settings.whitelist_enabled,
          counts: { public_models: lists.public_models.length, public_aliases: lists.public_aliases.length },
          active_mappings: state.aliases.filter((alias) => alias.enabled).map((alias) => ({ alias: alias.name, target: alias.target })),
          recent_auto_updates: clone(history.filter((entry) => entry.actor === 'auto' && entry.kind === 'alias_target').slice(0, 5)),
          catalog: clone(catalogStatus), guard: { enabled: true, verified: false,
            covered: [
              reason('http_model_requests_covered', 'Ordinary HTTP model requests, including streaming requests, are covered by model policy hooks.'),
              reason('model_lists_covered', 'Model list endpoints are filtered by model policy hooks.'),
              reason('responses_websocket_initial_covered', 'The initial Responses WebSocket model request is covered by model policy hooks.'),
            ], limitations: [
              reason('realtime_proxy_required', 'Valid built-in API keys and realtime client secrets can bypass the nonexclusive frontend guard on stock CPA; restrict realtime/live at a reverse proxy.'),
              reason('websocket_messages_proxy_required', 'Ongoing Responses WebSocket messages are not intercepted; use reverse-proxy controls.'),
              reason('model_detail_proxy_required', 'Single-model detail endpoints are not intercepted; use reverse-proxy controls.'),
              reason('admin_api_tool_proxy_required', 'The management API call tool is not intercepted; use reverse-proxy controls.'),
            ] }, warnings };
      } else if (route === 'PUT /state' || route === 'POST /validate' || route === 'POST /rules/preview' || route === 'POST /explain') {
        const draft = route === 'POST /explain' ? body.draft ?? {} : body;
        if (!object(draft)) return send(response, 422, error(422, 'validation_failed', 'Invalid draft.', { details: [detail('draft', 'invalid_type', 'Expected an object.', { expected: 'object' })] }).body);
        const checked = validate(draft, route === 'POST /rules/preview' || route === 'POST /explain');
        if (route === 'POST /validate') result = { valid: checked.details.length === 0, details: checked.details,
          preview: checked.details.length ? { public_models: [], public_aliases: [] } : publicLists(checked.policy, true) };
        else if (checked.details.length) return send(response, 422, error(422, 'validation_failed', 'Policy validation failed.', { details: checked.details }).body);
        else if (route === 'PUT /state') {
          const before = savedPolicy(state);
          state = { ...checked.policy, revision: state.revision, updated_at: timestamp() }; bump();
          policyAvailable = true;
          aliasStatus = {};
          record('user', 'state', summarize(before, savedPolicy(state)), before, savedPolicy(state)); result = getState();
        } else if (route === 'POST /rules/preview') result = { rules: checked.policy.rules.map((rule) => ({ id: rule.id,
          matches: (rule.kind === 'alias' ? checked.policy.aliases.map((alias) => alias.name) : models.map((model) => model.id)).filter((name) => ruleMatches(rule, normalize(name), checked.policy)).sort() })), ...publicLists(checked.policy, true) };
        else {
          if (typeof body.model !== 'string' || !body.model.trim()) return send(response, 422, error(422, 'validation_failed', 'A model is required.', { details: [detail('model', typeof body.model === 'string' || body.model === undefined ? 'required_model' : 'invalid_type', 'Expected a model name.', typeof body.model === 'string' || body.model === undefined ? {} : { expected: 'string' })] }).body);
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
        const success = Array.isArray(nextModels) && nextModels.length > 0;
        const failed = !Array.isArray(nextModels);
        const warning = success ? '' : failed ? 'catalog_unavailable_using_last_known' : models.length ? 'no_models_using_last_known' : 'no_models';
        catalogStatus = { last_refresh_at: timestamp(), last_good_at:success ? timestamp() : catalogStatus.last_good_at, ok:success, stale:!success && models.length > 0, warning,
          warning_detail:warning ? reason(warning,failed ? 'Could not refresh models; previously available models are retained' : 'No available models returned; the last available list is retained.') : null,
          error: failed ? 'Mock catalog refresh failed; last good snapshot retained.' : null,
          error_detail: failed ? reason('catalog_unavailable', 'Could not refresh models; previously available models are retained') : null };
        if (success) models = clone(nextModels);
        result = catalog(); finishRefresh();
      } else if (route === 'POST /aliases/check') {
        if (body.dry_run !== undefined && typeof body.dry_run !== 'boolean') return send(response, 422, error(422, 'validation_failed', 'Invalid dry_run.', { details: [detail('dry_run', 'invalid_type', 'Expected true or false.', { expected: 'boolean' })] }).body);
        if (body.alias_id !== undefined && typeof body.alias_id !== 'string') return send(response, 422, error(422, 'validation_failed', 'Invalid alias_id.', {details:[detail('alias_id', 'invalid_type', 'Expected text.', { expected: 'string' })]}).body);
        const aliases = body.alias_id === undefined || body.alias_id === '' ? state.aliases : state.aliases.filter((alias) => alias.id === body.alias_id);
        if (body.alias_id && !aliases.length) return send(response, 404, error(404, 'alias_not_found', 'Alias not found.').body);
        const results = aliases.map(evaluate);
        if (!body.dry_run) {
          for (const item of results) {
            const alias = state.aliases.find((entry) => entry.id === item.alias_id);
            if (item.last_result === 'updated') {
              record('auto', 'alias_target', reason('alias_target_auto_updated', `${alias.name}: ${alias.target} → ${item.current_target}`,
                { alias_id: alias.id, alias: alias.name, from: alias.target, to: item.current_target }),
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
        if (typeof body.entry_id !== 'string' || !body.entry_id.trim()) details.push(detail('entry_id', typeof body.entry_id === 'string' || body.entry_id === undefined ? 'required_entry_id' : 'invalid_type', 'A history entry is required.', typeof body.entry_id === 'string' || body.entry_id === undefined ? {} : { expected: 'string' }));
        if (!['state','alias_target'].includes(body.scope)) details.push(detail('scope', 'invalid_restore_scope', 'Use state or alias_target.'));
        if (Object.hasOwn(body,'pin') && typeof body.pin !== 'boolean') details.push(detail('pin','invalid_type','Expected true or false.',{expected:'boolean'}));
        if (body.pin === true && body.scope !== 'alias_target') details.push(detail('pin','invalid_restore_pin','Pin is only available when restoring an alias target.'));
        if (details.length) return send(response,422,error(422,'validation_failed','Invalid restore.',{details}).body);
        const entry = history.find((item) => item.id === body.entry_id);
        if (!entry) return send(response, 404, error(404, 'entry_not_found', 'History entry not found.').body);
        if (body.scope !== entry.kind) return send(response, 422, error(422, 'validation_failed', 'Scope must match entry kind.', { details: [detail('scope', 'restore_scope_mismatch', `Use ${entry.kind} for this entry.`)] }).body);
        const before = savedPolicy(state);
        if (body.scope === 'state') {
          const checked = validate(entry.before);
          if (checked.details.length) return send(response, 422, error(422, 'validation_failed', 'Historical policy is invalid.', { details: checked.details }).body);
          state = { ...checked.policy, revision: state.revision, updated_at: timestamp() }; aliasStatus = {};
        } else {
          const alias = state.aliases.find((item) => item.id === entry.before.alias_id);
          if (!alias) return send(response, 422, error(422, 'validation_failed', 'Historical alias no longer exists.', { details: [detail('entry_id', 'alias_not_found', 'Restore the containing state first.')] }).body);
          alias.target = entry.before.target; if (body.pin === true) alias.mode = 'pinned'; delete aliasStatus[alias.id];
        }
        bump();
        const alias = body.scope === 'alias_target' ? state.aliases.find(alias => alias.id === entry.before.alias_id) : null;
        const sourceAt = entry.before.updated_at || entry.at;
        const summaryDetail = alias ? reason('alias_target_restored', `Restored alias ${alias.name} target to ${alias.target}`,{alias_id:alias.id,alias:alias.name,target:alias.target,pinned:alias.mode === 'pinned'}) :
          reason('policy_restored',`Restored policy from ${sourceAt}`,{at:sourceAt});
        const oldAlias = alias && before.aliases.find(item => item.id === alias.id);
        const beforeView = alias ? {alias_id:alias.id,target:oldAlias.target} : before;
        const afterView = alias ? {alias_id:alias.id,target:alias.target} : savedPolicy(state);
        if (alias && oldAlias.mode !== alias.mode) { beforeView.mode = oldAlias.mode; afterView.mode = alias.mode; }
        record('user', body.scope, summaryDetail,
          beforeView, afterView);
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
