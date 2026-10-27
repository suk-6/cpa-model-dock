import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile, readdir } from 'node:fs/promises';
import { decodeStorage, readManagementKey, pathToField, policyPayload, reconcileAliasRules, diffJSON, escapeHTML, familyExamples } from '../../assets/app.js';
import { messages, translate } from '../../assets/i18n.js';

const host = 'localhost:8787', userAgent = 'Management Center / 한국어';
function encode(value) {
  const bytes = new TextEncoder().encode(value);
  const salt = new TextEncoder().encode(`cli-proxy-api-webui::secure-storage|${host}|${userAgent}`);
  return 'enc::v1::' + Buffer.from(bytes.map((byte,i) => byte ^ salt[i % salt.length])).toString('base64');
}
const storage = values => ({ getItem(key) {return values[key] ?? null;} });

test('decodes the exact management center XOR format including UTF-8', () => {
  const value = {state:{managementKey:'fixture-key-한글',rememberPassword:true},version:0};
  assert.deepEqual(decodeStorage(encode(JSON.stringify(value)),host,userAgent),value);
});
test('decodes plaintext JSON, legacy scalar JSON and raw strings', () => {
  assert.deepEqual(decodeStorage('{"state":{}}',host,userAgent),{state:{}});
  assert.equal(decodeStorage('"legacy-key"',host,userAgent),'legacy-key');
  assert.equal(decodeStorage('legacy-key',host,userAgent),'legacy-key');
  assert.equal(decodeStorage(encode('legacy-key'),host,userAgent),'legacy-key');
});
test('malformed and absent storage safely fall back', () => {
  assert.equal(decodeStorage(null,host,userAgent),null);
  assert.equal(decodeStorage('enc::v1::%%%%',host,userAgent),null);
  assert.equal(readManagementKey({getItem(){throw new Error('blocked');}},host,userAgent),'');
  assert.equal(readManagementKey(storage({'cli-proxy-auth':encode('{broken')}),host,userAgent),'');
});
test('key bridge reads persisted state first, then legacy; never writes storage', () => {
  const encoded = encode(JSON.stringify({state:{managementKey:'current-key'}}));
  assert.equal(readManagementKey(storage({'cli-proxy-auth':encoded,managementKey:'legacy-key'}),host,userAgent),'current-key');
  assert.equal(readManagementKey(storage({'cli-proxy-auth':'{"state":{"rememberPassword":false}}',managementKey:encode('"legacy-key"')}),host,userAgent),'legacy-key');
  assert.equal(readManagementKey(storage({'cli-proxy-auth':'{"state":{"managementKey":42}}'}),host,userAgent),'');
});
test('field paths support indexed arrays, nested fields and optional JSON root', () => {
  assert.equal(pathToField('aliases[0].name'),'aliases.0.name');
  assert.equal(pathToField('$.aliases[12].auto.family'),'aliases.12.auto.family');
  assert.equal(pathToField('settings.whitelist_enabled'),'settings.whitelist_enabled');
  assert.equal(pathToField('rules.1.value'),'rules.1.value');
  assert.equal(pathToField(undefined),'');
});
test('save projection drops runtime fields and isolates nested draft objects', () => {
  const state = {revision:4,settings:{whitelist_enabled:false},families:[],aliases:[{name:'sol',auto:{family:'builtin:gpt-sol'}}],rules:[],alias_status:{},builtin_families:[],schema:1,updated_at:'now'};
  const draft = policyPayload(state);
  assert.deepEqual(Object.keys(draft),['revision','settings','families','aliases','rules']);
  draft.aliases[0].auto.family = 'custom';
  assert.equal(state.aliases[0].auto.family,'builtin:gpt-sol');
});
test('diff finds changed values without highlighting the whole object', () => {
  const diff = diffJSON({name:'sol',target:'6.9'},{name:'sol',target:'6.10'});
  assert.equal(diff.before.filter(line => line.changed).length,1);
  assert.equal(diff.after.filter(line => line.changed).length,1);
  assert.match(diff.before.find(line => line.changed).text,/6\.9/);
});
test('diff insertion preserves later unchanged lines', () => {
  const diff = diffJSON({a:1,z:3},{a:1,b:2,z:3});
  assert.equal(diff.before.some(line => line.changed),false);
  assert.equal(diff.after.filter(line => line.changed).length,1);
  assert.match(diff.after.find(line => line.changed).text,/"b"/);
});
test('diff handles identical, missing and large documents', () => {
  assert.ok(diffJSON({a:1},{a:1}).before.every(line => !line.changed));
  assert.equal(diffJSON(undefined,null).after[0].changed,false);
  const diff = diffJSON(Array(1100).fill('a'),Array(1100).fill('b'));
  assert.ok(diff.before.some(line => line.changed));
});
test('all API strings can be escaped before HTML rendering', () => {
  assert.equal(escapeHTML('<script a="x">&\'</script>'),'&lt;script a=&quot;x&quot;&gt;&amp;&#39;&lt;/script&gt;');
});
test('family examples use whole-name matching, dated option and provider pins', () => {
  const models = ['gpt-6.1-sol','gpt-6.10-sol','gpt-6.9-sol','gpt-6.1-sol-mini','gpt-6.10-sol-preview','gpt-6.10-sol-20261027'].map(id => ({id,providers:['codex']}));
  const family = {template:'gpt-{version}-sol',allow_dated:false};
  assert.deepEqual(familyExamples(family,models).map(m => m.id),models.slice(0,3).map(m => m.id));
  assert.equal(familyExamples({...family,allow_dated:true},models).length,4);
  assert.equal(familyExamples({...family,provider:'other'},models).length,0);
});
test('literal template punctuation does not become a regular expression', () => {
  const models = [{id:'model.6.1+'},{id:'modelx6.1+'}];
  assert.deepEqual(familyExamples({template:'model.{version}+'},models),[models[0]]);
  assert.deepEqual(familyExamples({template:'invalid'},models),[]);
});
test('every translation key exists in both languages, with the same placeholders', () => {
  assert.deepEqual(Object.keys(messages.en).sort(),Object.keys(messages.ko).sort());
  for (const key of Object.keys(messages.en)) {
    assert.ok(messages.en[key]?.trim(),`English ${key}`);
    assert.ok(messages.ko[key]?.trim(),`Korean ${key}`);
    assert.deepEqual(messages.en[key].match(/\{\w+\}/g),messages.ko[key].match(/\{\w+\}/g),key);
  }
  assert.equal(translate('en','revision',{revision:12}),'Revision 12');
  assert.equal(translate('ko','revision',{revision:12}),'버전 12');
  assert.throws(() => translate('en','missing_key'));
});
test('the embed has exactly four served files and no inline code or styles', async () => {
  assert.deepEqual((await readdir(new URL('../../assets/',import.meta.url))).sort(),['app.css','app.js','i18n.js','index.html']);
  const html = await readFile(new URL('../../assets/index.html',import.meta.url),'utf8');
  assert.doesNotMatch(html,/<style\b|\sstyle=|\son\w+=|<script(?![^>]*\bsrc=)/i);
  const code = await readFile(new URL('../../assets/app.js',import.meta.url),'utf8');
  assert.doesNotMatch(code,/console\.|\beval\(|new Function\(/);
  const refs = [...code.matchAll(/\bt\('([^']+)'/g),...code.matchAll(/\bT\('([^']+)'/g)];
  for (const [,key] of refs) assert.ok(Object.hasOwn(messages.en,key),key);
});

test('alias rename and removal preserve rule integrity without widening other rules', () => {
  const rules = [
    {id:'r1',kind:'alias',value:'GPT-SOL',enabled:true},
    {id:'r2',kind:'exact',value:'gpt-sol',enabled:true},
    {id:'r3',kind:'alias',value:'other',enabled:false},
  ];
  const renamed = reconcileAliasRules(rules,'gpt-sol','stable-sol');
  assert.equal(renamed[0].value,'stable-sol');
  assert.equal(renamed[1].value,'gpt-sol');
  assert.equal(rules[0].value,'GPT-SOL');
  assert.deepEqual(reconcileAliasRules(rules,'gpt-sol'),rules.slice(1));
});

test('family examples never absorb calendar dates into numeric versions', () => {
  const ids = ['claude-opus-4-5', 'claude-opus-4-5-20251101', 'claude-opus-4-5-2025-11-01',
    'claude-opus-4-5-20251301', 'claude-opus-4-5-251101', 'claude-opus-4-5-1101',
    'claude-opus-4.5.20251101', 'claude-opus-4-20251101-5', 'claude-opus-999999999999999999999'];
  const models = ids.map(id => ({id}));
  const family = {template:'claude-opus-{version}',allow_dated:false};
  assert.deepEqual(familyExamples(family,models).map(m => m.id),[ids[0],ids[8]]);
  assert.deepEqual(familyExamples({...family,allow_dated:true},models).map(m => m.id),[ids[0],ids[1],ids[2],ids[8]]);
  assert.deepEqual(familyExamples({template:'claude-opus-{version}-{date}',allow_dated:false},[models[4]]),[models[4]]);
});
