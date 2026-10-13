'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {createDocument} = require('./browser_dom');
const {loadScripts, web} = require('./shipped_scripts');
const catalog = JSON.parse(fs.readFileSync(path.join(web, 'locales.json'), 'utf8'));
const malicious = '<script>公司😀';
const clone = value => structuredClone(value);
const plain = value => JSON.parse(JSON.stringify(value));
const flush = () => new Promise(setImmediate);
function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return {promise, resolve, reject};
}
function response(body, status = 200) { return {ok: status < 400, status, json: async () => clone(body)}; }
function storage() {
  const values = new Map();
  return {values, getItem: key => values.get(key) ?? null, setItem: (key, value) => values.set(key, String(value)), removeItem: key => values.delete(key)};
}
function initialConfig() {
  return {
    version: 3, revision: 1,
    listeners: {http: '127.0.0.1:17890', socks5: '127.0.0.1:17891', control: '127.0.0.1:17892', pac: '127.0.0.1:17893'},
    outbounds: [{id: 'direct', type: 'direct', enabled: true, public_internet: true},
      {id: 'vpn-a', type: 'warp', enabled: true, public_internet: true}, {id: 'vpn-b', type: 'warp', enabled: true, public_internet: true}],
    outbound_groups: [{id: 'group-a', type: 'fallback', enabled: true, members: ['vpn-a', 'vpn-b']}],
    rule_sets: [{id: 'set-a', name: 'Set A', domains: ['example.com']}],
    rules: [{id: 'target', domains: ['example.com'], outbound: 'group-a', family: 'auto'}, {id: 'untouched', domains: ['other.example'], outbound: 'vpn-b'}],
    default_outbound: 'direct', adaptive: {enabled: false, candidates: ['direct', 'vpn-a']},
    source_access: {rules: []}, connection_limits: {max_connections: 4096, max_connections_per_source: 2048},
    pac: {proxy_address: '127.0.0.1:17890', bypass_domains: [], bypass_cidrs: []}, security: {},
  };
}
const verdict = overrides => ({rule_id: 'target', target_kind: 'group', target_id: 'group-a', resolved_id: 'vpn-a', order: ['vpn-a', 'vpn-b'], private: false, blocked: false, family: 'auto', reason: 'group_fallback', ...overrides});

async function browser() {
  const document = createDocument(fs.readFileSync(path.join(web, 'index.html'), 'utf8'));
  const localStorage = storage(), sessionStorage = storage();
  const requests = [], timers = new Map();
  let timerID = 0;
  const b = {document, localStorage, sessionStorage, requests, server: initialConfig(), pendingPreview: null, pendingPut: null, getConfigError: null};
  const defaults = {rule: {id: 'new-rule', domains: ['example.com'], outbound: 'direct', family: 'auto'},
    group: {id: 'new-group', type: 'fallback', enabled: true, members: ['vpn-a', 'vpn-b']},
    rule_set: {id: 'new-set', name: 'New rule set', domains: ['example.com']}, outbounds: {}};
  const context = vm.createContext({document, localStorage, sessionStorage, console, structuredClone,
    NodeFilter: {SHOW_TEXT: 4}, CSS: {escape: value => String(value)},
    window: {addEventListener() {}}, location: {hostname: 'example.test', hash: ''}, history: {replaceState() {}},
    navigator: {clipboard: {writeText: async () => {}}}, confirm: () => true,
    setTimeout: callback => { const id = ++timerID; timers.set(id, callback); return id; }, clearTimeout: id => timers.delete(id),
    setInterval: () => ++timerID, clearInterval() {},
    fetch: async (url, options = {}) => {
      if (url === '/locales.json') return response(catalog);
      const request = {url, method: options.method || 'GET', body: options.body ? JSON.parse(options.body) : null};
      requests.push(request);
      if (url === '/api/v1/config' && request.method === 'PUT') {
        if (b.pendingPut) await b.pendingPut.promise;
        if (request.body.revision !== b.server.revision) return response({error: 'Configuration changed elsewhere. Your draft is kept.'}, 409);
        b.server = {...clone(request.body), revision: b.server.revision + 1};
        return response(b.server);
      }
      if (url === '/api/v1/config') {
        if (b.getConfigError) return response({error: b.getConfigError}, 503);
        return response(b.server);
      }
      if (url === '/api/v1/defaults') return response(defaults);
      if (url === '/api/v1/stats') return response({config_revision: b.server.revision, flows: []});
      if (url === '/api/v1/info') return response({version: 'test'});
      if (url.startsWith('/api/v1/outbounds')) return response([]);
      if (url === '/api/v1/route/explain') return b.pendingPreview ? b.pendingPreview.promise : response(verdict());
      throw new Error(`Unexpected fixture request: ${request.method} ${url}`);
    },
  });
  b.context = context;
  b.run = source => vm.runInContext(source, context);
  b.get = id => {
    const node = document.getElementById(id);
    assert.ok(node, `Missing shipped control #${id}`);
    return node;
  };
  b.puts = () => requests.filter(request => request.method === 'PUT' && request.url === '/api/v1/config');
  b.submit = async id => { await b.get(id).dispatch('submit'); await flush(); };
  b.change = async (id, value) => { b.get(id).value = value; await b.get(id).dispatch('input'); await b.get(id).dispatch('change'); };
  await loadScripts(context);
  assert.equal(requests.length, 0, 'evaluating full scripts must not request private API data');
  assert.equal(document.events.get('DOMContentLoaded').length, 1);
  for (const id of ['login-language', 'sidebar-language']) {
    assert.equal(b.get(id).tagName, 'SELECT');
    assert.ok(b.get(id).hasAttribute('data-locale'));
    assert.ok(document.querySelector('label[for="' + id + '"]')?.textContent.trim(), 'language controls need visible labels');
    assert.deepEqual(b.get(id).options.map(option => option.value), ['en', 'zh-Hant']);
  }
  b.run('bindUI(); bindUI(); active = true;');
  context.initial = clone(b.server);
  b.run('publishConfig(initial);');
  assert.equal(b.get('rule-form').events.get('submit').length, 1, 'bindUI is idempotent');
  assert.equal(b.get('group-form').events.get('submit').length, 1, 'editor binding is idempotent');
  return b;
}
function editButton(b, container, attribute, id) {
  const button = b.get(container).querySelectorAll(`[${attribute}]`).find(node => node.getAttribute(attribute) === id);
  assert.ok(button, `Missing ${attribute} action for stable ID ${id}`);
  return button;
}
async function openRule(b, id = 'target') {
  await editButton(b, 'rules', 'data-edit-rule', id).click();
  await flush();
  assert.equal(b.get('rule-dialog').open, true);
}
function sourceAdded(b) {
  b.server.revision = 2;
  b.server.source_access.rules.push({id: 'added-elsewhere', name: 'Keep me', enabled: true, action: 'deny', cidrs: ['203.0.113.7/32']});
}

async function ruleConflictReview() {
  const b = await browser();
  await openRule(b);
  await b.change('rule-domains', 'draft.example');
  sourceAdded(b);
  await b.submit('rule-form');
  assert.equal(b.puts().length, 1);
  assert.equal(b.puts()[0].body.revision, 1);
  assert.equal(b.get('rule-dialog').open, true);
  assert.equal(b.get('rule-domains').value, 'draft.example');
  assert.equal(b.get('rule-save').disabled, true, '409 must disable Save until explicit review');
  assert.match(b.get('rule-form').textContent, /Configuration changed elsewhere/);
  await b.submit('rule-form');
  assert.equal(b.puts().length, 1, 'a missing review must never send a second PUT');
  b.getConfigError = 'review unavailable';
  await b.get('rule-review').click();
  assert.equal(b.get('rule-save').disabled, true, 'failed review must not accept a new revision');
  assert.equal(b.get('rule-domains').value, 'draft.example');
  assert.equal(b.puts().length, 1);
  b.getConfigError = null;
  const globalBeforeReview = b.run('JSON.stringify(cfg)');
  await b.get('rule-review').click();
  assert.equal(b.run('JSON.stringify(cfg)'), globalBeforeReview, 'review must not publish its snapshot');
  assert.equal(b.get('rule-review-panel').hidden, false);
  assert.equal(b.get('rule-save').disabled, false);
  assert.equal(b.get('rule-save').textContent.trim(), 'Save my draft');
  assert.equal(b.puts().length, 1, 'review alone is read-only');
  await b.submit('rule-form');
  assert.equal(b.puts().length, 2);
  assert.equal(b.puts()[1].body.revision, 2);
  assert.deepEqual(b.puts()[1].body.source_access.rules, b.server.source_access.rules);
  assert.deepEqual(b.server.source_access.rules.map(rule => rule.id), ['added-elsewhere']);
  assert.deepEqual(b.server.rules.find(rule => rule.id === 'target').domains, ['draft.example']);
  assert.deepEqual(b.server.rules.find(rule => rule.id === 'untouched'), initialConfig().rules[1]);
  assert.deepEqual(b.server.outbound_groups, initialConfig().outbound_groups);
  assert.deepEqual(b.server.rule_sets, initialConfig().rule_sets);
  assert.equal(b.get('rule-dialog').open, false);
}

async function sharedDraftSafety() {
  for (const [collection, id] of [['rules', 'target'], ['outbound_groups', 'group-a'], ['rule_sets', 'set-a']]) {
    const b = await browser();
    // A separate instance exercises the public shared draft API with the real
    // app API/save/publication/session boundaries, without binding another UI.
    b.run(`globalThis.drafts = RillwayEditors.create({api, save, publishConfig, getConfig: () => cfg,
      getSession: () => ({active, generation: sessionGeneration}),
      getObservedRevision: () => Math.max(cfg?.revision ?? 0, latestSnapshot.config_revision ?? 0),
      t, esc, errorText, notice, withBusy, openModal});`);
    const drafts = b.context.drafts;
    let draft = drafts.createDraft(collection, id);
    assert.equal(draft.baseRevision, 1);
    assert.equal(draft.originalID, id);
    const entity = clone(b.server[collection].find(item => item.id === id));
    const original = clone(entity);
    sourceAdded(b);
    await assert.rejects(drafts.saveDraft(draft, entity), error => error.status === 409);
    assert.equal(b.puts().length, 1);
    await assert.rejects(drafts.saveDraft(draft, entity));
    assert.equal(b.puts().length, 1, `${collection}: missing review must not mutate`);
    assert.deepEqual(plain(draft.baseEntity), original);
    b.server[collection] = b.server[collection].filter(item => item.id !== id);
    try { await drafts.reviewDraft(draft); } catch (_) {}
    await assert.rejects(drafts.saveDraft(draft, entity));
    assert.equal(b.puts().length, 1, `${collection}: deleted original ID cannot be silently recreated`);

    const fresh = await browser();
    fresh.run(`globalThis.drafts = RillwayEditors.create({api, save, publishConfig, getConfig: () => cfg,
      getSession: () => ({active, generation: sessionGeneration}), getObservedRevision: () => cfg.revision,
      t, esc, errorText, notice, withBusy, openModal});`);
    draft = fresh.context.drafts.createDraft(collection, null);
    const created = {...original, id: 'occupied'};
    fresh.server.revision = 2;
    fresh.server[collection].push({...clone(created), name: 'Owned by someone else'});
    await assert.rejects(fresh.context.drafts.saveDraft(draft, created), error => error.status === 409);
    try { await fresh.context.drafts.reviewDraft(draft); } catch (_) {}
    await assert.rejects(fresh.context.drafts.saveDraft(draft, created));
    assert.equal(fresh.puts().length, 1, `${collection}: an occupied new ID cannot overwrite another item`);
    assert.equal(fresh.server[collection].find(item => item.id === 'occupied').name, 'Owned by someone else');
  }
}

async function previewRaces() {
  {
    const b = await browser();
    await b.change('rt-host', 'example.com');
    await b.submit('rt-form');
    assert.ok(b.get('rt-result').textContent.includes('vpn-a'));
    await b.run('poll()');
    assert.ok(b.get('rt-result').textContent.includes('vpn-a'), 'an unchanged observed revision must keep the preview current');
    assert.ok(!b.get('rt-result').textContent.includes('Inputs or configuration changed.'));
    ++b.server.revision;
    await b.run('poll()');
    assert.ok(b.get('rt-result').textContent.includes('Inputs or configuration changed.'), 'a newer observed revision must stale the preview');
  }
  for (const [field, changed] of [['rt-host', 'changed.example'], ['rt-port', '8443'], ['rt-source', '203.0.113.8'], ['rt-inbound', 'socks5'], ['rt-family', 'ipv6']]) {
    const b = await browser();
    await b.change('rt-host', 'example.com');
    assert.equal(b.get('rt-port').value, '443');
    assert.equal(b.get('rt-family').value, 'auto');
    b.pendingPreview = deferred();
    const request = b.submit('rt-form');
    await flush();
    assert.equal(b.get('rt-result').getAttribute('aria-busy'), 'true');
    await b.change(field, changed);
    b.pendingPreview.resolve(response(verdict({reason: 'stale-verdict'})));
    await request;
    await flush();
    assert.ok(!b.get('rt-result').textContent.includes('stale-verdict'), `${field}: stale response was rendered`);
    assert.ok(!b.get('rt-result').textContent.includes('vpn-a'), `${field}: stale successful verdict must not remain`);
    b.pendingPreview = null;
    await b.submit('rt-form');
    assert.ok(b.get('rt-result').textContent.includes('vpn-a'), `${field}: a fresh preview must be usable`);
  }
  for (const invalidation of ['revision', 'logout']) {
    const b = await browser();
    await b.change('rt-host', 'example.com');
    b.pendingPreview = deferred();
    const request = b.submit('rt-form');
    await flush();
    if (invalidation === 'revision') { ++b.server.revision; await b.run('poll()'); }
    else await b.get('logout').click();
    b.pendingPreview.resolve(response(verdict({reason: 'stale-verdict'})));
    await request;
    await flush();
    assert.ok(!b.get('rt-result').textContent.includes('stale-verdict'), `${invalidation}: stale response was rendered`);
    assert.ok(!b.get('rt-result').textContent.includes('vpn-a'));
  }
  const b = await browser();
  await b.change('rt-host', 'example.com');
  await b.submit('rt-form');
  assert.ok(b.get('rt-result').textContent.includes('vpn-a'));
  await b.change('rt-port', '8443');
  assert.ok(b.get('rt-result').textContent.includes('Inputs or configuration changed. Run the preview again.'));
  assert.ok(b.get('rt-result').textContent.includes('example.com'), 'stale result must retain its query summary');
  b.pendingPreview = {promise: Promise.resolve(response({error: 'invalid preview'}, 422))};
  await b.submit('rt-form');
  assert.ok(b.get('rt-result').textContent.includes('invalid preview'));
  assert.ok(!b.get('rt-result').textContent.includes('vpn-a'), '422 must clear a previous success verdict');
  b.pendingPreview = null;
  await b.submit('rt-form');
  assert.ok(b.get('rt-result').textContent.includes('vpn-a'));
  b.pendingPreview = deferred();
  const failed = b.submit('rt-form');
  await flush();
  b.pendingPreview.reject(new Error('socket closed'));
  await failed;
  await flush();
  assert.ok(b.get('rt-result').querySelector('.form-error')?.textContent.trim(), 'network failure must expose a readable error');
  assert.ok(!b.get('rt-result').textContent.includes('vpn-a'), 'network failure must clear a previous success verdict');
  assert.equal(b.puts().length, 0, 'route preview is read-only');
}

async function previewVerdictAndTextSafety() {
  const b = await browser();
  await b.change('rt-host', 'example.com');
  b.pendingPreview = {promise: Promise.resolve(response(verdict({blocked: true, reason: 'private_protected'})))};
  await b.submit('rt-form');
  const alert = b.get('rt-result').querySelector('.form-alert.warn');
  assert.ok(alert, 'blocked=true is a visible warning, not an API error');
  assert.ok(alert.textContent.trim());
  assert.ok(b.get('route-test-panel').textContent.includes('This preview does not connect or verify outbound availability.'));
  b.server.rules[0].id = malicious;
  b.server.rule_sets[0].name = malicious;
  b.context.nextConfig = clone(b.server);
  b.run('publishConfig(nextConfig);');
  assert.ok(b.get('rules').textContent.includes(malicious));
  assert.equal(b.get('rules').querySelector('script'), null, 'a rule ID must never become an executable DOM node');
  assert.ok(b.get('rule-set-list').textContent.includes(malicious));
  assert.equal(b.get('rule-set-list').querySelector('script'), null, 'a rule-set name must remain text');
  await openRule(b, malicious);
  await b.change('rule-domains', 'typed.example');
  b.pendingPreview = {promise: Promise.resolve(response(verdict({rule_id: malicious, reason: malicious, target_kind: 'future-kind'})))};
  await b.submit('rt-form');
  assert.ok(b.get('rt-result').textContent.includes(malicious));
  assert.equal(b.get('rt-result').querySelector('script'), null);
  const ids = b.server.rules.map(rule => rule.id);
  b.run("switchLocale('zh-Hant'); renderConfig();");
  assert.equal(b.get('rule-id').value, malicious);
  assert.equal(b.get('rule-domains').value, 'typed.example', 'render/locale must not refill an open draft');
  assert.deepEqual(b.server.rules.map(rule => rule.id), ids);
  assert.ok(b.get('rt-result').textContent.includes(malicious), 'unknown reason must not be translated');
  assert.ok(b.get('rt-result').textContent.includes('future-kind'), 'unknown target kind must remain verbatim');
  b.run("switchLocale('en'); renderConfig();");
  assert.equal(b.get('rule-domains').value, 'typed.example');
  assert.deepEqual([...b.localStorage.values.keys()], ['rillway-locale'], 'no token or draft belongs in localStorage');
}

async function busyAndReset() {
  const b = await browser();
  await openRule(b);
  await b.change('rule-domains', 'busy.example');
  b.pendingPut = deferred();
  const first = b.submit('rule-form');
  await flush();
  assert.equal(b.get('rule-save').getAttribute('aria-busy'), 'true');
  const duplicate = b.submit('rule-form');
  await duplicate;
  assert.equal(b.puts().length, 1, 'busy double-submit must send one mutation');
  b.pendingPut.resolve();
  await first;
  await flush();
  assert.equal(b.get('rule-save').getAttribute('aria-busy'), null);
  assert.equal(b.get('rule-dialog').open, false);
  await openRule(b);
  await b.change('rule-domains', 'discard-me.example');
  b.run('logout();');
  assert.equal(b.get('rule-dialog').open, false);
  assert.equal(b.get('group-dialog').open, false);
  assert.equal(b.get('rule-set-dialog').open, false);
  assert.equal(b.get('group-delete-dialog').open, false);
  b.run('active = true;');
  b.context.nextConfig = clone(b.server);
  b.run('publishConfig(nextConfig);');
  await openRule(b);
  assert.equal(b.get('rule-domains').value, 'busy.example', 'logout must discard the old draft');
  await b.get('rule-dialog').dispatch('cancel');
  assert.equal(b.get('rule-dialog').open, false);
}

async function deletedAndOccupiedRuleUI() {
  const deleted = await browser();
  await openRule(deleted);
  await deleted.change('rule-domains', 'keep-my-input.example');
  deleted.server.revision = 2;
  deleted.server.rules = deleted.server.rules.filter(rule => rule.id !== 'target');
  await deleted.submit('rule-form');
  await deleted.get('rule-review').click();
  await flush();
  assert.equal(deleted.get('rule-save').disabled, true);
  assert.ok(deleted.get('rule-form').textContent.includes('This item was deleted elsewhere. Close this editor to create a new item.'));
  assert.equal(deleted.get('rule-domains').value, 'keep-my-input.example');
  await deleted.submit('rule-form');
  assert.equal(deleted.puts().length, 1);

  const occupied = await browser();
  await occupied.get('add-rule').click();
  await flush();
  await occupied.change('rule-id', 'occupied');
  occupied.server.revision = 2;
  occupied.server.rules.push({id: 'occupied', domains: ['someone-else.example'], outbound: 'vpn-b'});
  await occupied.submit('rule-form');
  await occupied.get('rule-review').click();
  await flush();
  await occupied.submit('rule-form');
  assert.equal(occupied.puts().length, 1, 'review cannot authorize overwriting a newly occupied ID');
  assert.deepEqual(occupied.server.rules.find(rule => rule.id === 'occupied').domains, ['someone-else.example']);
  assert.equal(occupied.get('rule-id').value, 'occupied', 'collision must preserve the draft so its ID can be changed');
}

async function anotherConflictAfterReview() {
  const b = await browser();
  await openRule(b);
  await b.change('rule-domains', 'twice.example');
  sourceAdded(b);
  await b.submit('rule-form');
  await b.get('rule-review').click();
  await flush();
  ++b.server.revision;
  await b.submit('rule-form');
  assert.equal(b.puts().length, 2);
  assert.equal(b.get('rule-save').disabled, true, 'another 409 must invalidate the reviewed base');
  assert.equal(b.get('rule-domains').value, 'twice.example');
  await b.submit('rule-form');
  assert.equal(b.puts().length, 2, 'mutation retries must always require another explicit review');
}

async function otherEditorsBusyLocaleAndReset() {
  for (const spec of [
    {list: 'group-list', action: 'data-edit-group', id: 'group-a', dialog: 'group-dialog', form: 'group-form', save: 'group-save', field: 'group-enabled', idField: 'group-id'},
    {list: 'rule-set-list', action: 'data-edit-set', id: 'set-a', dialog: 'rule-set-dialog', form: 'rule-set-form', save: 'rs-save', field: 'rs-name', idField: 'rs-id'},
  ]) {
    const b = await browser();
    await editButton(b, spec.list, spec.action, spec.id).click();
    await flush();
    assert.equal(b.get(spec.dialog).open, true);
    assert.ok(b.get(spec.idField).disabled || b.get(spec.idField).readOnly, 'existing entity IDs must be locked');
    if (spec.field === 'group-enabled') { b.get(spec.field).checked = false; await b.get(spec.field).dispatch('change'); }
    else await b.change(spec.field, malicious);
    b.run("switchLocale('zh-Hant'); renderConfig();");
    assert.equal(b.get(spec.idField).value, spec.id);
    if (spec.field === 'group-enabled') assert.equal(b.get(spec.field).checked, false);
    else assert.equal(b.get(spec.field).value, malicious);
    b.run("switchLocale('en');");
    b.pendingPut = deferred();
    const first = b.submit(spec.form);
    await flush();
    await b.submit(spec.form);
    assert.equal(b.puts().length, 1, spec.form + ': busy double-submit must send one mutation');
    assert.equal(b.get(spec.save).getAttribute('aria-busy'), 'true');
    b.run('logout();');
    assert.equal(b.get(spec.save).disabled, false);
    assert.equal(b.get(spec.save).getAttribute('aria-busy'), null);
    assert.equal(b.get(spec.dialog).open, false);
    assert.equal(b.get('rt-result').textContent, '');
    const loggedOutConfig = b.run('JSON.stringify(cfg)');
    b.pendingPut.resolve();
    await first;
    await flush();
    assert.equal(b.run('JSON.stringify(cfg)'), loggedOutConfig, 'a response from the previous login must not publish configuration');
    assert.equal(b.get(spec.dialog).open, false, 'a completed previous-session save cannot reopen a dialog');
    assert.equal(b.run('active'), false);
    b.run('active = true;');
    b.context.nextConfig = clone(b.server);
    b.run('publishConfig(nextConfig);');
    await editButton(b, spec.list, spec.action, spec.id).click();
    await flush();
    assert.equal(b.get(spec.dialog).open, true, 'reset must release opening/saving guards');
    assert.equal(b.puts().length, 1, 'logging in again must not replay a prior mutation');
    assert.equal(b.get(spec.save).disabled, false);
  }
}


(async () => {
  await ruleConflictReview();
  await deletedAndOccupiedRuleUI();
  await anotherConflictAfterReview();
  await sharedDraftSafety();
  await previewRaces();
  await previewVerdictAndTextSafety();
  await busyAndReset();
  await otherEditorsBusyLocaleAndReset();
  console.log('Browser editors: explicit conflict review, stable IDs, preview races, text safety, locale, busy and logout contracts passed.');
})().catch(error => { console.error(error); process.exitCode = 1; });
