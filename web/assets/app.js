import { translate } from './i18n.js';

// These pure helpers are also imported directly by the dependency-free Node tests.
export function decodeStorage(raw, host, userAgent) {
  if (typeof raw !== 'string' || !raw) return null;
  try {
    let decoded = raw;
    if (raw.startsWith('enc::v1::')) {
      const bytes = Uint8Array.from(atob(raw.slice(9)), char => char.charCodeAt(0));
      const salt = new TextEncoder().encode(`cli-proxy-api-webui::secure-storage|${host}|${userAgent}`);
      decoded = new TextDecoder('utf-8', { fatal: true }).decode(bytes.map((byte, i) => byte ^ salt[i % salt.length]));
    }
    try { return JSON.parse(decoded); } catch { return decoded; }
  } catch { return null; }
}
export function readManagementKey(storage, host, userAgent) {
  try {
    const saved = decodeStorage(storage.getItem('cli-proxy-auth'), host, userAgent);
    const key = saved?.state?.managementKey;
    if (typeof key === 'string' && key.trim()) return key.trim();
    const legacy = decodeStorage(storage.getItem('managementKey'), host, userAgent);
    return typeof legacy === 'string' && legacy.trim() ? legacy.trim() : '';
  } catch { return ''; }
}
export function pathToField(path) {
  return String(path ?? '').replace(/\[(\d+)\]/g, '.$1').replace(/^\$\.?/, '').replace(/^\./, '');
}
export function policyPayload(state) {
  return structuredClone(Object.fromEntries(['revision', 'settings', 'families', 'aliases', 'rules'].map(key => [key, state[key]])));
}
export function reconcileAliasRules(rules, oldName, newName = null) {
  const matches = rule => rule.kind === 'alias' && rule.value.trim().toLowerCase() === oldName.trim().toLowerCase();
  return newName === null ? rules.filter(rule => !matches(rule)) : rules.map(rule => matches(rule) ? {...rule,value:newName} : rule);
}
export function diffJSON(before, after) {
  const a = (JSON.stringify(before ?? null, null, 2)).split('\n');
  const b = (JSON.stringify(after ?? null, null, 2)).split('\n');
  // LCS keeps inserted lines from marking the rest of a document as changed.
  // Bound memory for unusually large history entries; positional comparison is safe then.
  if (a.length * b.length > 1_000_000) return {
    before: a.map((text, i) => ({ text, changed: text !== b[i] })),
    after: b.map((text, i) => ({ text, changed: text !== a[i] })),
  };
  const dp = Array.from({ length: a.length + 1 }, () => new Uint32Array(b.length + 1));
  for (let i = a.length - 1; i >= 0; i--) for (let j = b.length - 1; j >= 0; j--) {
    dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
  }
  const left = [], right = [];
  let i = 0, j = 0;
  while (i < a.length || j < b.length) {
    if (i < a.length && j < b.length && a[i] === b[j]) {
      left.push({ text: a[i++], changed: false }); right.push({ text: b[j++], changed: false });
    } else if (i < a.length && (j === b.length || dp[i + 1][j] >= dp[i][j + 1])) {
      left.push({ text: a[i++], changed: true });
    } else right.push({ text: b[j++], changed: true });
  }
  return { before: left, after: right };
}
export function escapeHTML(value) {
  return String(value ?? '').replace(/[&<>"']/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[char]);
}
// Keep browser examples and the mock's family policy checks on the same parser.
export function parseFamilyName(id, family, includePrerelease = false) {
  const template = family.template;
  if (typeof template !== 'string' || template.length > 200 || template.split('{version}').length !== 2 || template.split('{date}').length > 2) return null;
  let name = String(id).trim(), prerelease = false;
  const marker = name.match(/-(preview|beta|alpha|experimental|exp|rc|nightly|dev|test)(?:[.-]?\d+)?$/i);
  if (marker) { prerelease = true; name = name.slice(0, marker.index); }
  if (prerelease && !includePrerelease) return null;
  const datePattern = '\\d{4}-\\d{2}-\\d{2}|\\d{8}|\\d{6}|\\d{4}';
  const literal = value => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const source = template.split(/(\{version\}|\{date\})/).map(part =>
    part === '{version}' ? '(?<version>\\d+(?:[.-]\\d+)*)' :
      part === '{date}' ? `(?<date>${datePattern})` : literal(part)).join('');
  let match;
  if (!template.includes('{date}')) {
    match = new RegExp(`^${source}-(?<date>${datePattern})$`, 'i').exec(name);
    if (match && (!family.allow_dated || (match.groups.date.replaceAll('-', '').length < 8 && template.endsWith('{version}')))) return null;
  }
  match ??= new RegExp(`^${source}$`, 'i').exec(name);
  if (!match) return null;
  const validDate = raw => {
    const compact = raw.replaceAll('-', '');
    const expanded = compact.length === 6 ? `20${compact}` : compact.length === 4 ? `2000${compact}` : compact;
    if (!/^\d{8}$/.test(expanded)) return false;
    const year = +expanded.slice(0, 4), month = +expanded.slice(4, 6), day = +expanded.slice(6);
    const date = new Date(0);
    date.setUTCFullYear(year, month - 1, day);
    return year > 0 && date.getUTCFullYear() === year && date.getUTCMonth() === month - 1 && date.getUTCDate() === day;
  };
  if (match.groups.date && !validDate(match.groups.date)) return null;
  if (match.groups.version.split(/[.-]/).some(part => part.length === 8 && validDate(part))) return null;
  return { version: match.groups.version, date: match.groups.date?.replaceAll('-', '') ?? null, prerelease };
}
export function familyExamples(family, models) {
  return models.filter(model => parseFamilyName(model.id, family) && (!family.provider || model.providers?.includes(family.provider)));
}

function startApp() {
  const $ = selector => document.querySelector(selector);
  const all = selector => [...document.querySelectorAll(selector)];
  const BASE = '/v0/management/plugins/cpa-model-dock';
  const SESSION_KEY = 'cpa-model-dock.tab-key';
  const state = { lang: 'en', key: '', saved: null, draft: null, overview: null, catalog: null, history: [], tab: 'overview', busy: false, errors: [], preview: null, explain: null, modal: null, editor: null, notice: '', more: true, draftSerial: 0, authFailed: false };
  let pendingFocus = null;
  function requestFocus(find) {
    if (state.busy) pendingFocus = find;
    else find()?.focus();
  }
  try { if (localStorage.getItem('cpa-model-dock.lang') === 'ko') state.lang = 'ko'; } catch { /* Storage can be unavailable inside an iframe. */ }
  const t = (key, values) => translate(state.lang, key, values);
  const clean = value => {
    const text = typeof value === 'object' && value !== null ? JSON.stringify(value) : String(value ?? '');
    return state.key ? text.split(state.key).join('••••') : text;
  };
  const E = value => escapeHTML(clean(value));
  const T = key => E(t(key));
  const dirty = () => !!state.draft && JSON.stringify(policyPayload(state.draft)) !== JSON.stringify(policyPayload(state.saved));
  const date = value => value ? E(new Intl.DateTimeFormat(state.lang === 'ko' ? 'ko-KR' : 'en-US', { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))) : T('never');
  const arr = value => Array.isArray(value) ? value : [];
  const ids = items => arr(items).map(item => typeof item === 'string' ? item : item.id ?? item.name ?? '');
  const chips = items => ids(items).length ? `<div class="chips">${ids(items).map(id => `<span class="chip">${E(id)}</span>`).join('')}</div>` : `<p class="empty">${T('none')}</p>`;
  const badge = (text, kind = '') => `<span class="badge ${kind}">${text}</span>`;
  const btn = (action, label, attrs = '', cls = '') => `<button type="button" data-action="${action}" class="${cls}" ${attrs}>${T(label)}</button>`;
  const families = () => [...arr(state.saved?.builtin_families), ...arr(state.draft?.families)];
  const options = (values, selected) => values.map(([value, label]) => `<option value="${E(value)}" ${String(selected) === String(value) ? 'selected' : ''}>${E(label)}</option>`).join('');
  const familyOptions = selected => options([['', t('family')], ...families().map(family => [family.id, family.name])], selected);
  const fieldError = path => state.errors.filter(error => pathToField(error.path) === pathToField(path)).map(error => E(error.message || error.code)).join('<br>');
  function field(path, label, input, help = '') {
    const id = `field-${pathToField(path).replace(/\./g, '-')}`;
    const error = fieldError(path);
    return `<div class="field"><label for="${id}">${T(label)}</label>${input.replace('<input ', `<input id="${id}" `).replace('<select ', `<select id="${id}" `).replace(' data-field=', ` aria-describedby="${id}-help ${id}-error" ${error ? 'aria-invalid="true"' : ''} data-field=`)}<div id="${id}-help" class="help">${help ? T(help) : ''}</div><div class="field-error" id="${id}-error">${error}</div></div>`;
  }
  const textInput = (path, value, extra = '') => `<input data-field="${E(path)}" value="${E(value)}" ${extra}>`;
  const selectInput = (path, value, entries) => `<select data-field="${E(path)}">${options(entries, value)}</select>`;
  function checkbox(path, label, value, help = '') {
    const id = `field-${pathToField(path).replace(/\./g, '-')}`;
    const error = fieldError(path);
    return `<div class="field"><label class="check" for="${id}"><input id="${id}" type="checkbox" data-field="${E(path)}" aria-describedby="${id}-help ${id}-error" ${error ? 'aria-invalid="true"' : ''} ${value ? 'checked' : ''}><span>${T(label)}<span class="help" id="${id}-help">${help ? T(help) : ''}</span></span></label><div class="field-error" id="${id}-error">${error}</div></div>`;
  }
  function reason(value) { return E(typeof value === 'object' ? value?.message || value?.code || t('unknown') : value || t('unknown')); }
  function header() {
    $('#header').innerHTML = `<div class="brand"><div class="brand-mark" aria-hidden="true">≋</div><div><h1>${T('app')}</h1><p class="help">${T('tagline')}</p></div></div><div class="toolbar">${state.saved ? `<span class="badge ${state.authFailed ? 'bad' : 'good'}"><span class="dot"></span>${T(state.authFailed ? 'unavailable' : 'connected')}</span>` : ''}<label for="language" class="sr-only">${T('language')}</label><select id="language"><option value="en" ${state.lang === 'en' ? 'selected' : ''}>English</option><option value="ko" ${state.lang === 'ko' ? 'selected' : ''}>한국어</option></select>${state.saved ? btn('disconnect', 'disconnect') : ''}</div>`;
    $('#tabs').innerHTML = state.saved ? ['overview', 'aliases', 'whitelist', 'history'].map(tab => `<button id="tab-${tab}" type="button" role="tab" aria-controls="panel-${tab}" aria-selected="${state.tab === tab}" tabindex="${state.tab === tab ? '0' : '-1'}" data-tab="${tab}">${T(tab)}</button>`).join('') : '';
    $('#tabs').setAttribute('aria-label', t('app'));
    document.documentElement.lang = state.lang;
    document.title = t('app');
    all('[data-i18n]').forEach(node => { node.textContent = t(node.dataset.i18n); });
    $('#footer').innerHTML = `<span>${T('footer')}</span><span>${state.saved ? T('savedPolicy') + ' · ' + E(t('revision', { revision: state.saved.revision })) : ''}</span>`;
  }
  function renderErrors() {
    $('#errors').innerHTML = state.errors.length ? `<div class="message error"><strong>${T('validationTitle')}</strong><ul>${state.errors.map((error, index) => `<li><button type="button" class="small danger" data-action="error-field" data-index="${index}">${E(error.path)}: ${E(error.message || error.code)}</button></li>`).join('')}</ul></div>` : '';
  }
  function renderBar() {
    $('#savebar').innerHTML = dirty() ? `<strong>${T('unsaved')}</strong><div class="actions">${btn('discard','discard')}${btn('save','save','','primary')}</div>` : '';
  }
  function renderNotice() {
    $('#notice').innerHTML = state.notice ? `<div class="message row between"><span>${E(state.notice)}</span>${btn('dismiss','dismiss','','small')}</div>` : '';
  }
  function render() {
    const activeId = document.activeElement?.id;
    header(); renderNotice(); renderErrors(); renderBar();
    $('#content').innerHTML = state.saved ? `<section id="panel-${state.tab}" role="tabpanel" aria-labelledby="tab-${state.tab}">${({ overview: overviewPage, aliases: aliasesPage, whitelist: whitelistPage, history: historyPage })[state.tab]()}</section>` : authPage();
    setBusy(state.busy);
    if (!state.busy && activeId) document.getElementById(activeId)?.focus();
  }
  function authPage() {
    return `<section class="card auth"><div class="eyebrow">${T('app')}</div><h2>${T('connect')}</h2><p class="muted">${T('keyHelp')}</p><form id="auth-form"><div class="field"><label for="management-key">${T('key')}</label><input id="management-key" name="key" type="password" required autocomplete="off" spellcheck="false" autocapitalize="none"></div><label class="check"><input id="remember" name="remember" type="checkbox">${T('remember')}</label><p class="help">${T('rememberHelp')}</p><button class="primary" type="submit">${T('connect')}</button></form></section>`;
  }
  function heading(title, description, action = '') {
    return `<div class="page-heading"><div><h2>${T(title)}</h2>${description ? `<p>${T(description)}</p>` : ''}</div><div class="actions">${action}</div></div>`;
  }
  function overviewPage() {
    const o = state.overview || {}, c = o.catalog || {}, guard = o.guard || {};
    const stat = (label, value, text = false) => `<article class="card"><div class="stat-label">${T(label)}</div><div class="stat-value ${text ? 'text' : ''}">${value}</div></article>`;
    return `${heading('overview','',btn('refresh-overview','refresh'))}${dirty() ? `<div class="message">${T('draftNotice')}</div>` : ''}
      <div class="stats">${stat('policy', badge(T(o.policy_enabled ? 'active' : 'unavailable'), o.policy_enabled ? 'good' : 'bad'), true)}${stat('whitelist', T(o.whitelist_enabled ? 'on' : 'off'), true)}${stat('publicModels', E(o.counts?.public_models ?? '—'))}${stat('publicAliases', E(o.counts?.public_aliases ?? '—'))}</div>
      ${arr(o.warnings).length ? `<div class="message warning"><strong>${T('warnings')}</strong><ul>${o.warnings.map(w => `<li>${reason(w)}</li>`).join('')}</ul></div>` : ''}
      <div class="grid"><article class="card"><h3 class="card-title">${T('mappings')}</h3>${arr(o.active_mappings).length ? o.active_mappings.map(m => `<div class="mapping"><strong class="model">${E(m.alias ?? m.name)}</strong><span class="model"><span class="arrow">→</span>${E(m.target ?? m.current_target)}</span></div>`).join('') : `<p class="empty">${T('none')}</p>`}</article>
      <article class="card"><div class="row between card-title"><h3>${T('catalog')}</h3>${badge(T(c.ok ? 'healthy' : 'catalogFailed'), c.ok ? 'good' : 'warn')}</div><p class="help">${T('lastRefresh')} · ${date(c.last_refresh_at)}</p>${c.error ? `<p class="field-error break">${reason(c.error)}</p>` : ''}<div class="actions card-title"></div>${btn('refresh-catalog','refreshCatalog')}</article>
      <article class="card"><h3 class="card-title">${T('updates')}</h3>${arr(o.recent_auto_updates).length ? o.recent_auto_updates.map(update => `<div class="timeline-item"><p>${E(update.summary || update.message || update.alias || '')}</p><span class="help">${date(update.at || update.last_checked_at)}</span></div>`).join('') : `<p class="empty">${T('none')}</p>`}</article>
      <article class="card"><div class="row between card-title"><h3>${T('guard')}</h3>${badge(T(guard.enabled ? 'on' : 'off'), guard.enabled ? 'good' : '')}</div><p class="help">${T(guard.verified === true ? 'verified' : 'unverified')}</p><p class="help">${T('guardLimits')}</p>${arr(guard.limitations).length ? `<ul class="help">${guard.limitations.map(item => `<li>${reason(item)}</li>`).join('')}</ul>` : ''}<p class="help">${T('version')}: ${E(o.plugin_version ?? t('unknown'))} · ${T('host')}: ${E(o.host_schema ?? t('unknown'))}</p></article></div>
      <article class="card"><h3 class="card-title">${T('policySettings')}</h3><div class="form-grid">${field('settings.refresh_interval_seconds','interval',textInput('settings.refresh_interval_seconds',state.draft.settings.refresh_interval_seconds,'type="number" min="0" step="1"'),'intervalHelp')}${checkbox('settings.rewrite_response_model','rewriteDefault',state.draft.settings.rewrite_response_model)}</div></article>`;
  }
  function candidateList(candidates) {
    return arr(candidates).length ? `<ul class="candidate-list">${candidates.map(candidate => `<li><span class="model">${E(candidate.id ?? candidate.model ?? candidate.target)} ${candidate.version ? `<span class="muted">(${E(candidate.version)})</span>` : ''}</span><span>${badge(T(candidate.routable ? 'routable' : 'catalogOnly'), candidate.routable ? 'good' : '')}${candidate.chosen ? ' ' + badge(T('selected'),'good') : ''}${candidate.reason ? `<span class="help"> · ${reason(candidate.reason)}</span>` : ''}</span></li>`).join('')}</ul>` : `<p class="empty">${T('none')}</p>`;
  }
  function aliasesPage() {
    return `${heading('aliases','aliasHelp',btn('add-alias','addAlias','','primary'))}<div class="stack">${state.draft.aliases.length ? state.draft.aliases.map((alias,index) => {
      const savedAlias = state.saved.aliases.find(item => item.id && item.id === alias.id);
      const sameRouting = savedAlias && JSON.stringify([alias.target,alias.provider,alias.mode,alias.auto]) === JSON.stringify([savedAlias.target,savedAlias.provider,savedAlias.mode,savedAlias.auto]);
      const status = sameRouting ? state.saved.alias_status?.[alias.id] || {} : {};
      return `<article class="card alias-card"><div class="row between"><div class="row"><h3>${E(alias.name)}</h3>${badge(T(alias.mode),alias.mode === 'auto' ? 'good' : '')}${!alias.enabled ? badge(T('off')) : ''}${status.target_routable === false ? badge(T('unavailable'),'bad') : ''}</div><div class="actions">${btn('edit-alias','edit',`data-index="${index}"`)}${btn('check-alias','checkNow',`data-index="${index}"`)}${btn('delete-alias','remove',`data-index="${index}"`,'danger')}</div></div><div class="mapping"><span class="help">${T(sameRouting ? 'currentTarget' : 'draftTarget')}</span><span class="model">${E(alias.target)}</span></div>${sameRouting ? `<div class="meta"><span>${T('lastChecked')} · ${date(status.last_checked_at)}</span><span>${T('reason')} · ${reason(status.reason || (status.last_result ? t(status.last_result) : null))}</span></div>` : `<p class="help">${T('runtimePending')}${savedAlias ? ` ${T('savedTarget')}: <span class="model">${E(savedAlias.target)}</span>` : ''}</p>`}${state.errors.some(error => pathToField(error.path).startsWith(`aliases.${index}.`)) ? `<p class="field-error">${state.errors.filter(error => pathToField(error.path).startsWith(`aliases.${index}.`)).map(error => E(error.message || error.code)).join('<br>')}</p>` : ''}<details><summary>${T('candidates')}</summary>${candidateList(status.candidates)}</details></article>`;
    }).join('') : `<article class="card"><p class="empty">${T('none')}</p></article>`}</div>
    <article class="card"><div class="row between card-title"><h3>${T('families')}</h3>${btn('add-family','addFamily')}</div>${state.draft.families.length ? state.draft.families.map((family,index) => `<div class="mapping"><div><strong>${E(family.name)}</strong><p class="model muted">${E(family.template)}</p>${state.errors.filter(error => pathToField(error.path).startsWith(`families.${index}.`)).map(error => `<p class="field-error">${E(error.message || error.code)}</p>`).join('')}</div><div class="actions">${btn('edit-family','edit',`data-index="${index}"`)}${btn('delete-family','remove',`data-index="${index}"`,'danger')}</div></div>`).join('') : `<p class="empty">${T('none')}</p>`}</article>`;
  }
  function whitelistPage() {
    const d = state.draft;
    return `${heading('whitelist','whitelistHelp')}<article class="card">${checkbox('settings.whitelist_enabled','whitelistToggle',d.settings.whitelist_enabled)}<p class="help">${T(d.settings.whitelist_enabled && !d.rules.some(rule => rule.enabled) ? 'zeroHelp' : d.settings.whitelist_enabled ? 'patternHelp' : 'whitelistOffHelp')}</p></article>
      <article class="card"><div class="row between card-title"><h3>${T('rules')}</h3>${btn('add-rule','addRule','','primary')}</div>${d.rules.length ? d.rules.map((rule,index) => `<div class="mapping"><div><label class="check"><input type="checkbox" data-field="rules[${index}].enabled" ${rule.enabled ? 'checked' : ''}><span>${E(t(rule.kind))} · <span class="model">${E(rule.value)}</span></span></label>${rule.note ? `<p class="help">${E(rule.note)}</p>` : ''}${state.errors.filter(error => pathToField(error.path).startsWith(`rules.${index}.`)).map(error => `<p class="field-error">${E(error.message || error.code)}</p>`).join('')}<details><summary>${T('matches')}</summary>${state.preview ? chips(state.preview.rules?.find(item => item.id === rule.id)?.matches) : `<p class="help">${T('previewStale')}</p>`}</details></div><div class="actions">${btn('edit-rule','edit',`data-index="${index}"`)}${btn('delete-rule','remove',`data-index="${index}"`,'danger')}</div></div>`).join('') : `<p class="empty">${T('none')}</p>`}</article>
      <div class="grid"><article class="card"><div class="row between card-title"><h3>${T('draftPreview')}</h3>${btn('preview-rules','refresh')}</div>${state.preview ? `<p class="help">${T('publicModels')}</p>${chips(state.preview.public_models)}<p class="help">${T('publicAliases')}</p>${chips(state.preview.public_aliases)}` : `<p class="help">${T('previewStale')}</p>`}</article>
      <article class="card"><h3 class="card-title">${T('testModel')}</h3><form id="explain-form"><label class="sr-only" for="test-model">${T('testModel')}</label><div class="test-form"><input id="test-model" name="model" required placeholder="${T('modelPlaceholder')}" spellcheck="false" autocomplete="off"><button type="submit">${T('test')}</button></div></form><div id="explain-result">${explainResult()}</div></article></div>`;
  }
  function explainResult() {
    const result = state.explain;
    return !result ? '' : `<div class="message ${result.allowed ? '' : 'error'}"><strong>${T(result.allowed ? 'allowed' : 'blocked')}</strong><p>${reason(result.reason || result.code)}</p>${result.resolved_target ? `<p class="model">${T('resolvedTarget')} · ${E(result.resolved_target)}</p>` : ''}<details open><summary>${T('steps')}</summary><ol>${arr(result.steps).map(step => `<li class="break">${reason(step)}</li>`).join('')}</ol></details></div>`;
  }
  function historyPage() {
    return `${heading('history','historyHelp',btn('refresh-history','refresh'))}<article class="card">${state.history.length ? state.history.map((entry,index) => `<div class="timeline-item"><div class="row between"><div class="row">${badge(E(t(`actor_${entry.actor}`)),entry.actor === 'auto' ? 'good' : '')}${badge(E(t(`kind_${entry.kind}`)))}</div><time class="help">${date(entry.at)}</time></div><p>${E(entry.summary)}</p><div class="actions">${btn('view-diff','viewDiff',`data-index="${index}"`)}${btn('restore','restore',`data-index="${index}"`)}</div></div>`).join('') : `<p class="empty">${T('none')}</p>`}${state.more && state.history.length ? `<div class="actions">${btn('more-history','more')}</div>` : ''}</article>`;
  }
  function pickerRows() {
    const query = (state.editor?.search || '').toLowerCase();
    const list = arr(state.catalog?.models).filter(model => model.id.toLowerCase().includes(query));
    return list.length ? list.map(model => `<div class="picker-row"><div><span class="model">${E(model.id)}</span><p class="help">${E(arr(model.providers).join(', '))} · ${T(model.routable ? 'routable' : 'catalogOnly')}</p></div>${btn('pick-model',state.editor.value.target === model.id ? 'selected' : 'choose',`data-model="${E(model.id)}"`,'small')}</div>`).join('') : `<p class="empty">${T('none')}</p>`;
  }
  function aliasEditor() {
    const { value: alias, index } = state.editor;
    const path = key => `aliases[${index}].${key}`;
    return `<form id="editor-form"><div class="form-grid">${field(path('name'),'name',textInput(path('name'),alias.name,'required autocomplete="off" spellcheck="false"'))}${checkbox(path('enabled'),'enabled',alias.enabled)}
      ${field(path('mode'),'mode',selectInput(path('mode'),alias.mode,[['pinned',t('pinned')],['auto',t('auto')]]))}${field(path('provider'),'provider',textInput(path('provider'),alias.provider,'autocomplete="off"'))}
      <div class="span-all">${field(path('target'),'target',textInput(path('target'),alias.target,'required autocomplete="off" spellcheck="false"'))}<label for="model-search">${T('searchModels')}</label><input id="model-search" type="search" value="${E(state.editor.search)}" autocomplete="off"><div id="model-picker" class="picker">${pickerRows()}</div></div>
      ${alias.mode === 'auto' ? `<div class="span-all">${field(path('auto.family'),'family',`<select data-field="${path('auto.family')}" required>${familyOptions(alias.auto.family)}</select>`)}${checkbox(path('auto.include_prerelease'),'includePrerelease',alias.auto.include_prerelease)}</div>` : ''}
      <div class="span-all">${checkbox(path('expose_original'),'expose',alias.expose_original,'exposeHelp')}</div>${field(path('rewrite_response_model'),'rewrite',selectInput(path('rewrite_response_model'),alias.rewrite_response_model === null ? 'null' : String(alias.rewrite_response_model),[['null',t('default')],['true',t('on')],['false',t('off')]]))}</div><div class="actions dialog-actions">${btn('close-modal','cancel')}<button type="submit" class="primary">${T('addToDraft')}</button></div></form>`;
  }
  function familyEditor() {
    const { value: family, index } = state.editor;
    const path = key => `families[${index}].${key}`;
    return `<form id="editor-form">${field(path('name'),'name',textInput(path('name'),family.name,'required'))}${field(path('template'),'template',textInput(path('template'),family.template,'required maxlength="200" autocomplete="off" spellcheck="false"'),'templateHelp')}${field(path('provider'),'provider',textInput(path('provider'),family.provider))}${checkbox(path('allow_dated'),'allowDated',family.allow_dated)}<h3>${T('exampleMatches')}</h3><p class="help">${T('examplesHelp')}</p><div id="family-examples">${chips(familyExamples(family,arr(state.catalog?.models)))}</div><div class="actions dialog-actions">${btn('close-modal','cancel')}<button type="submit" class="primary">${T('addToDraft')}</button></div></form>`;
  }
  function ruleEditor() {
    const { value: rule, index } = state.editor;
    const path = key => `rules[${index}].${key}`;
    const valueInput = rule.kind === 'family' ? `<select data-field="${path('value')}" required>${familyOptions(rule.value)}</select>` : rule.kind === 'alias' ? selectInput(path('value'),rule.value,[['',t('alias')],...state.draft.aliases.map(alias => [alias.name,alias.name])]) : textInput(path('value'),rule.value,'required autocomplete="off" spellcheck="false"');
    return `<form id="editor-form">${field(path('kind'),'kind',selectInput(path('kind'),rule.kind,['exact','family','pattern','alias'].map(kind => [kind,t(kind)])))}${field(path('value'),rule.kind === 'family' ? 'family' : rule.kind === 'alias' ? 'alias' : 'value',valueInput,'patternHelp')}${field(path('note'),'note',textInput(path('note'),rule.note))}${checkbox(path('enabled'),'enabled',rule.enabled)}<div class="actions dialog-actions">${btn('close-modal','cancel')}<button type="submit" class="primary">${T('addToDraft')}</button></div></form>`;
  }
  function diffView(before, after, labels = ['before','after']) {
    const diff = diffJSON(before,after);
    return `<div class="diff">${['before','after'].map((side,index) => `<section><h3>${T(labels[index])}</h3><pre>${diff[side].map(line => `<span class="diff-line ${line.changed ? (index ? 'added' : 'removed') : ''}">${line.changed ? (index ? '+ ' : '− ') : '  '}${E(line.text)}</span>`).join('')}</pre></section>`).join('')}</div>`;
  }
  let returnFocus = null, modalNeedsFocus = false;
  function openModal(modal) {
    if (!$('#dialog').open) returnFocus = document.activeElement;
    state.modal = modal;
    modalNeedsFocus = true;
    renderModal();
    if (!$('#dialog').open) $('#dialog').showModal();
    focusModal();
  }
  function focusModal() {
    const requested = all('#dialog [data-field]').find(input => pathToField(input.dataset.field) === state.modal?.focusPath);
    const el = requested || $('#dialog [aria-invalid=true]') || $('#dialog input:not([type=checkbox])') || $('#dialog button');
    el?.focus();
  }
  function closeModal() {
    $('#dialog').close(); state.modal = null; state.editor = null;
    requestFocus(() => returnFocus?.isConnected ? returnFocus : ($(`[data-tab="${state.tab}"]`) || $('#content')));
  }
  function renderModal() {
    const modal = state.modal;
    if (!modal) return;
    let body = '';
    if (modal.type === 'editor') body = ({ aliases: aliasEditor, families: familyEditor, rules: ruleEditor })[state.editor.collection]();
    if (modal.type === 'auth-error') body = authPage();
    if (modal.type === 'confirm') body = `<p>${T(modal.help)}</p><div class="actions dialog-actions">${btn('close-modal','cancel')}${btn('confirm',modal.label || 'remove','','primary')}</div>`;
    if (modal.type === 'diff') body = `${diffView(modal.entry.before,modal.entry.after)}<div class="actions dialog-actions">${btn('close-modal','close')}${btn('restore','restore',`data-index="${modal.index}"`,'primary')}</div>`;
    if (modal.type === 'check') body = `<p class="help">${T('checkHelp')}</p>${arr(modal.results).map(result => `<p class="model">${E(result.current_target)}</p><p>${reason(result.reason)}</p>${candidateList(result.candidates)}`).join('')}<div class="actions dialog-actions">${btn('close-modal','cancel')}${btn('apply-check','applyUpdate',`data-alias-id="${E(modal.aliasId)}"`,'primary')}</div>`;
    if (modal.type === 'conflict') body = `<p>${T('conflictHelp')}</p><div class="actions dialog-actions">${btn('close-modal','close')}${btn('view-server','viewServer')}${btn('reload-server','reloadLose','','primary')}</div>`;
    if (modal.type === 'server') body = `<p class="help">${T('conflictHelp')}</p>${diffView(policyPayload(state.draft),policyPayload(modal.server),['yourDraft','serverVersion'])}<div class="actions dialog-actions">${btn('close-modal','close')}${btn('reload-server','reloadLose','','primary')}</div>`;
    $('#dialog').innerHTML = `<div class="page-heading"><h2 id="dialog-title">${T(modal.title)}</h2>${btn('close-modal','close','','small')}</div><div class="dialog-body">${body}</div>`;
    setBusy(state.busy);
  }
  function edit(collection,index) {
    const defaults = {
      aliases: { name:'', enabled:true, mode:'pinned', target:'', provider:'', expose_original:false, rewrite_response_model:null, auto:{ family:families()[0]?.id || '', include_prerelease:false, health_check:false } },
      families: { id:`fam_${crypto.randomUUID().replaceAll('-','')}`, name:'', template:'gpt-{version}-sol', provider:'', allow_dated:true },
      rules: { id:`r_${crypto.randomUUID().replaceAll('-','')}`, enabled:true, kind:'exact', value:'', note:'' },
    };
    const adding = index === undefined;
    const value = structuredClone(adding ? defaults[collection] : state.draft[collection][index]);
    if (collection === 'aliases') value.auto = { ...defaults.aliases.auto, ...value.auto };
    state.editor = { collection,index: adding ? state.draft[collection].length : index,value,search:'' };
    openModal({type:'editor',title: collection === 'aliases' ? (adding ? 'addAlias':'editAlias') : collection === 'families' ? (adding ? 'addFamily':'editFamily') : (adding ? 'addRule':'editRule')});
  }
  function setPath(object,path,value) {
    const parts = pathToField(path).split('.');
    let at = object;
    for (const key of parts.slice(0,-1)) at = at[key] ?? (at[key] = {});
    at[parts.at(-1)] = value;
  }
  function changed() {
    state.errors = []; state.preview = null; state.explain = null; state.draftSerial++;
    renderBar(); renderErrors();
  }
  function setBusy(value) {
    state.busy = value;
    document.body.setAttribute('aria-busy',String(value));
    all('button,input,select,textarea').forEach(node => {
      if (value) { if (!Object.hasOwn(node.dataset,'previousDisabled')) node.dataset.previousDisabled = String(node.disabled); node.disabled = true; }
      else if (Object.hasOwn(node.dataset,'previousDisabled')) { node.disabled = node.dataset.previousDisabled === 'true'; delete node.dataset.previousDisabled; }
    });
  }
  async function api(path,method = 'GET',body) {
    let response;
    try {
      response = await fetch(new URL(BASE + path,location.origin),{method,headers:{Authorization:`Bearer ${state.key}`,...(body !== undefined ? {'Content-Type':'application/json'}:{})},...(body !== undefined ? {body:JSON.stringify(body)}:{}),cache:'no-store',redirect:'error',credentials:'omit',signal:AbortSignal.timeout(20000)});
    } catch { throw new Error(t('networkError')); }
    let data;
    try { data = await response.json(); } catch { throw new Error(t('requestError')); }
    if (!response.ok) {
      const error = new Error(response.status === 401 || response.status === 403 ? t('authError') : clean(data?.error?.message || t('requestError')));
      error.status = response.status; error.data = data; throw error;
    }
    return data;
  }
  async function run(action) {
    if (state.busy) return;
    setBusy(true);
    try { await action(); }
    catch (error) {
      if (error.status === 409 && state.draft) {
        let server = error.data?.state;
        if (!server?.settings) {
          try { server = await api('/state'); } catch { server = null; }
        }
        openModal({type:'conflict',title:'conflictTitle',server});
      } else if (error.status === 422) {
        state.errors = error.data?.error?.details || [];
        render(); if (state.modal) renderModal();
        requestFocus(() => $('#errors button'));
      } else {
        state.notice = clean(error.message || t('requestError')); renderNotice();
        if (error.status === 401 || error.status === 403) {
          state.authFailed = true;
          // No automatic retries: CPA may rate-limit incorrect keys.
          try { sessionStorage.removeItem(SESSION_KEY); } catch { /* unavailable */ }
          if (!state.saved) { state.key = ''; render(); requestFocus(() => $('#management-key')); }
          else {header();openModal({type:'auth-error',title:'connect'});}
        }
      }
    } finally {
      setBusy(false);
      if ($('#dialog').open && (modalNeedsFocus || !$('#dialog').contains(document.activeElement))) focusModal();
      else if (!$('#dialog').open && pendingFocus) pendingFocus()?.focus();
      pendingFocus = null; modalNeedsFocus = false;
    }
  }
  async function loadAll() {
    const [saved,overview,catalog] = await Promise.all([api('/state'),api('/overview'),api('/catalog')]);
    state.saved = saved; state.draft = policyPayload(saved); state.overview = overview; state.catalog = catalog;
    state.authFailed = false;
    state.errors = []; state.preview = null; state.explain = null; state.history = []; state.more = true;
    render();
  }
  async function previewRules() {
    const serial = state.draftSerial;
    const data = await api('/rules/preview','POST',{...policyPayload(state.draft)});
    if (serial === state.draftSerial) state.preview = data;
    render();
  }
  async function getHistory(more = false) {
    const query = new URLSearchParams({limit:'30'});
    if (more && state.history.length) query.set('before',state.history.at(-1).id);
    const data = await api(`/history?${query}`);
    state.history = more ? [...state.history,...arr(data.entries)] : arr(data.entries);
    state.more = data.next_before !== null;
    render();
  }
  async function save() {
    const payload = policyPayload(state.draft);
    if (payload.settings.whitelist_enabled && !payload.rules.some(rule => rule.enabled) && !state.zeroApproved) {
      openModal({type:'confirm',title:'zeroTitle',help:'zeroHelp',label:'save',action:async () => {state.zeroApproved = true; await save();}}); return;
    }
    const validated = await api('/validate','POST',payload);
    if (!validated.valid) {
      state.errors = arr(validated.details); render(); requestFocus(() => $('#errors button')); return;
    }
    const saved = await api('/state','PUT',payload);
    state.saved = saved; state.draft = policyPayload(saved); state.errors = []; state.zeroApproved = false;
    state.notice = t('saved'); render();
    // Publish succeeded even if refreshing the overview later fails.
    state.overview = await api('/overview'); state.catalog = await api('/catalog'); render();
  }
  function requireSaved() {
    if (!dirty()) return true;
    state.notice = t('saveFirst'); renderNotice(); return false;
  }
  async function checkAlias(index) {
    if (!requireSaved()) return;
    const alias = state.draft.aliases[index];
    if (!alias.id) {state.notice = t('newAliasCheck'); renderNotice(); return;}
    const result = await api('/aliases/check','POST',{alias_id:alias.id,dry_run:true});
    openModal({type:'check',title:'checkPreview',results:result.results,aliasId:alias.id});
  }
  function confirmDelete(collection,index) {
    if (collection === 'families') {
      const id = state.draft.families[index].id;
      if (state.draft.aliases.some(alias => alias.auto?.family === id) || state.draft.rules.some(rule => rule.kind === 'family' && rule.value === id)) {
        state.notice = t('familyInUse'); renderNotice(); return;
      }
    }
    const aliasName = collection === 'aliases' ? state.draft.aliases[index].name : null;
    const hasAliasRules = aliasName !== null && reconcileAliasRules(state.draft.rules,aliasName).length !== state.draft.rules.length;
    openModal({type:'confirm',title:'deleteTitle',help:hasAliasRules ? 'deleteAliasRulesHelp' : 'deleteHelp',action:() => {
      if (aliasName !== null) state.draft.rules = reconcileAliasRules(state.draft.rules,aliasName);
      state.draft[collection].splice(index,1);changed();render();
    }});
  }
  async function switchTab(tab) {
    state.tab = tab; render(); requestFocus(() => $(`[data-tab="${tab}"]`));
    if (tab === 'whitelist' && !state.preview) await previewRules();
    if (tab === 'history' && !state.history.length) await getHistory();
  }
  function disconnect() {
    state.key = ''; state.saved = null; state.draft = null; state.overview = null; state.catalog = null;
    state.history = []; state.errors = []; state.notice = ''; state.tab = 'overview'; state.authFailed = false;
    try { sessionStorage.removeItem(SESSION_KEY); } catch { /* unavailable */ }
    render(); requestFocus(() => $('#management-key'));
  }
  async function refreshCatalog() {
    state.catalog = await api('/catalog/refresh','POST',{});
    const server = await api('/state');
    const previousPolicy = policyPayload(state.saved), nextPolicy = policyPayload(server);
    delete previousPolicy.revision; delete nextPolicy.revision;
    state.overview = await api('/overview');
    if (JSON.stringify(previousPolicy) !== JSON.stringify(nextPolicy) && dirty()) {
      openModal({type:'conflict',title:'conflictTitle',server}); render(); return;
    }
    const wasDirty = dirty();
    state.saved = server;
    if (wasDirty) state.draft.revision = server.revision;
    else state.draft = policyPayload(server);
    render();
  }
  async function reloadServer() {
    // Fetch afresh: the version first shown in the conflict may already be outdated.
    closeModal(); await loadAll();
  }
  const actions = {
    dismiss: () => { state.notice = ''; renderNotice(); },
    disconnect: () => dirty() ? openModal({type:'confirm',title:'discardTitle',help:'discardHelp',label:'disconnect',action:disconnect}) : disconnect(),
    'refresh-overview': async () => {state.overview = await api('/overview');render();},
    'refresh-catalog': refreshCatalog,
    'add-alias': () => edit('aliases'), 'edit-alias': el => edit('aliases',Number(el.dataset.index)),
    'add-family': () => edit('families'), 'edit-family': el => edit('families',Number(el.dataset.index)),
    'add-rule': () => edit('rules'), 'edit-rule': el => edit('rules',Number(el.dataset.index)),
    'delete-alias': el => confirmDelete('aliases',Number(el.dataset.index)),
    'delete-family': el => confirmDelete('families',Number(el.dataset.index)),
    'delete-rule': el => confirmDelete('rules',Number(el.dataset.index)),
    'close-modal': closeModal,
    confirm: async () => {const action = state.modal.action;closeModal();await action();},
    discard: () => openModal({type:'confirm',title:'discardTitle',help:'discardHelp',label:'discard',action:() => {state.draft = policyPayload(state.saved);changed();render();}}),
    save,
    'preview-rules': previewRules,
    'check-alias': el => checkAlias(Number(el.dataset.index)),
    'pick-model': el => {
      state.editor.value.target = el.dataset.model;
      const input = $('#dialog [data-field$=".target"]'); input.value = el.dataset.model;
      state.modal.focusPath = pathToField(input.dataset.field); modalNeedsFocus = true;
      $('#model-picker').innerHTML = pickerRows(); input.focus();
    },
    'apply-check': async el => {
      if (!requireSaved()) return;
      await api('/aliases/check','POST',{alias_id:el.dataset.aliasId,dry_run:false,revision:state.saved.revision});
      closeModal(); await loadAll();
    },
    'refresh-history': () => getHistory(), 'more-history': () => getHistory(true),
    'view-diff': el => {const index = Number(el.dataset.index);openModal({type:'diff',title:'diff',entry:state.history[index],index});},
    restore: el => {
      if (!requireSaved()) return;
      const entry = state.history[Number(el.dataset.index)];
      openModal({type:'confirm',title:'restoreTitle',help:'restoreHelp',label:'restore',action:async () => {
        const saved = await api('/history/restore','POST',{entry_id:entry.id,revision:state.saved.revision,scope:entry.kind});
        state.saved = saved;state.draft = policyPayload(saved);state.errors = [];render();
        await loadAll();await getHistory();
      }});
    },
    'view-server': async () => {
      const server = state.modal.server || await api('/state');
      openModal({type:'server',title:'serverVersion',server});
    },
    'reload-server': reloadServer,
    'error-field': el => {
      const path = pathToField(state.errors[Number(el.dataset.index)].path);
      const match = /^(aliases|families|rules)\.(\d+)/.exec(path);
      if (match) {state.tab = match[1] === 'rules' ? 'whitelist' : 'aliases';render();edit(match[1],Number(match[2]));}
      else {state.tab = path.includes('whitelist') ? 'whitelist' : 'overview';render();}
      if (state.modal) state.modal.focusPath = path;
      requestFocus(() => all('[data-field]').find(input => pathToField(input.dataset.field) === path));
    },
  };
  document.addEventListener('click',event => {
    const element = event.target.closest('[data-action],[data-tab]');
    if (!element || state.busy) return;
    if (element.dataset.tab) {run(() => switchTab(element.dataset.tab));return;}
    const action = actions[element.dataset.action];
    if (action) run(() => action(element));
  });
  $('#tabs').addEventListener('keydown',event => {
    if (!['ArrowLeft','ArrowRight','Home','End'].includes(event.key) || state.busy) return;
    event.preventDefault();
    const tabs = all('[data-tab]'); let index = tabs.indexOf(document.activeElement);
    index = event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1 : (index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
    run(() => switchTab(tabs[index].dataset.tab));
  });
  document.addEventListener('input',event => {
    if (state.busy) return;
    const input = event.target;
    if (input.id === 'model-search') {state.editor.search = input.value;$('#model-picker').innerHTML = pickerRows();return;}
    if (!input.dataset.field) return;
    let value = input.type === 'checkbox' ? input.checked : input.type === 'number' ? (input.value === '' ? '' : Number(input.value)) : input.value;
    const path = input.dataset.field;
    if (path.endsWith('rewrite_response_model') && input.tagName === 'SELECT') value = value === 'null' ? null : value === 'true';
    if (state.editor && input.closest('#dialog')) {
      setPath(state.editor.value,pathToField(path).split('.').slice(2).join('.'),value);
      if (path.endsWith('.template') || path.endsWith('.allow_dated') || (state.editor.collection === 'families' && path.endsWith('.provider'))) {
        $('#family-examples').innerHTML = chips(familyExamples(state.editor.value,arr(state.catalog?.models)));
      }
      if (path.endsWith('.mode') || path.endsWith('.kind')) {
        if (path.endsWith('.kind')) state.editor.value.value = '';
        renderModal(); $('#dialog [data-field$=".' + path.split('.').at(-1) + '"]')?.focus();
      }
      return;
    }
    if (path === 'settings.whitelist_enabled' && value && !state.draft.rules.some(rule => rule.enabled)) {
      input.checked = false;
      openModal({type:'confirm',title:'zeroTitle',help:'zeroHelp',label:'enableAnyway',action:() => {state.draft.settings.whitelist_enabled = true;state.zeroApproved = true;changed();render();}});
      return;
    }
    setPath(state.draft,path,value);state.zeroApproved = false;changed();
    // Keep text inputs stable while typing; toggles can re-render dependent descriptions.
    if (input.type === 'checkbox') render();
    else {const preview = $('#explain-result');if (preview) preview.innerHTML = '';}
  });
  document.addEventListener('change',event => {
    if (event.target.id !== 'language' || state.busy) return;
    state.lang = event.target.value === 'ko' ? 'ko' : 'en';
    try {localStorage.setItem('cpa-model-dock.lang',state.lang);} catch { /* optional preference */ }
    render();if (state.modal) {renderModal();focusModal();}
  });
  document.addEventListener('submit',event => {
    event.preventDefault();if (state.busy) return;
    const form = event.target;
    if (!form.reportValidity()) return;
    if (form.id === 'auth-form') {
      const data = new FormData(form);state.key = String(data.get('key') || '').trim();
      form.querySelector('input[type=password]').value = '';
      run(async () => {
        // Changing an expired key must not overwrite an existing local draft.
        if (state.saved) {state.overview = await api('/overview');state.authFailed = false;state.notice = '';closeModal();render();}
        else await loadAll();
        try {if (data.get('remember')) sessionStorage.setItem(SESSION_KEY,state.key);else sessionStorage.removeItem(SESSION_KEY);} catch { /* optional tab persistence */ }
      });
    }
    if (form.id === 'editor-form') {
      const {collection,index,value} = state.editor;
      const previousName = collection === 'aliases' ? state.draft.aliases[index]?.name : undefined;
      if (previousName !== undefined && previousName !== value.name) state.draft.rules = reconcileAliasRules(state.draft.rules,previousName,value.name);
      state.draft[collection][index] = structuredClone(value);closeModal();changed();render();
    }
    if (form.id === 'explain-form') {
      const model = String(new FormData(form).get('model') || '').trim();
      run(async () => {state.explain = await api('/explain','POST',{model,draft:policyPayload(state.draft)});$('#explain-result').innerHTML = explainResult();});
    }
  });
  $('#dialog').addEventListener('cancel',event => {event.preventDefault();if (!state.busy) closeModal();});
  window.addEventListener('beforeunload',event => {if (dirty()) {event.preventDefault();event.returnValue = '';}});
  render();
  try {state.key = sessionStorage.getItem(SESSION_KEY) || '';} catch { /* fallback input */ }
  if (!state.key) {
    try {state.key = readManagementKey(localStorage,location.host,navigator.userAgent);} catch { /* fallback input */ }
  }
  if (state.key) run(loadAll);
}
if (typeof document !== 'undefined') startApp();
