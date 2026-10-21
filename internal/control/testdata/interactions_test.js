'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const {createDocument} = require('./browser_dom');
const {loadScripts, web} = require('./shipped_scripts');
const catalog = JSON.parse(fs.readFileSync(path.join(web, 'locales.json'), 'utf8'));
const clone = value => structuredClone(value);
const flush = () => new Promise(setImmediate);
function deferred() {
  let resolve;
  const promise = new Promise(yes => { resolve = yes; });
  return {promise, resolve};
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
    rules: [{id: 'match-a', domains: ['one.example'], outbound: 'group-a', family: 'auto'},
      {id: 'match-b', domains: ['two.example'], outbound: 'vpn-a', family: 'auto'},
      {id: 'keep', domains: ['keep.invalid'], outbound: 'vpn-b', family: 'auto'}],
    default_outbound: 'direct', adaptive: {enabled: false, candidates: ['direct']},
    source_access: {rules: [{id: 'allow-a', name: 'First', action: 'allow', enabled: false, cidrs: ['192.0.2.0/24']},
      {id: 'allow-b', name: 'Second', action: 'allow', enabled: true, cidrs: ['198.51.100.0/24']},
      {id: 'deny-c', name: 'Third', action: 'deny', enabled: true, cidrs: ['203.0.113.0/24']}]},
    connection_limits: {max_connections: 4096, max_connections_per_source: 2048},
    pac: {proxy_address: '127.0.0.1:17890', bypass_domains: [], bypass_cidrs: []}, security: {},
  };
}
function clock() {
  let now = 0, sequence = 0;
  const jobs = new Map();
  return {
    setTimeout(callback, delay = 0) { const id = ++sequence; jobs.set(id, {at: now + Number(delay), callback}); return id; },
    clearTimeout(id) { jobs.delete(id); },
    async advance(milliseconds) {
      const end = now + milliseconds;
      for (;;) {
        const next = [...jobs.entries()].filter(([, job]) => job.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
        if (!next) break;
        now = next[1].at; jobs.delete(next[0]); await next[1].callback(); await flush();
      }
      now = end;
    },
  };
}
async function browser() {
  const document = createDocument(fs.readFileSync(path.join(web, 'index.html'), 'utf8'));
  const timers = clock(), localStorage = storage(), sessionStorage = storage();
  const requests = [], gates = new Map(), failures = new Map();
  const b = {document, timers, localStorage, sessionStorage, requests, gates, failures, server: initialConfig(), clients: [], flowData: [], statusData: [],
    udpFailures: [], udpDrops: {target_limit: 0, recent_failure: 0, local_destination: 0}};
  const memory = {supported: true, minimum_reason: 'Direct and WARP safety floor', host_bytes: 4 * 1073741824, minimum_bytes: 256 * 1048576, maximum_bytes: 3 * 1073741824,
    current_bytes: 32 * 1048576, limit_bytes: 512 * 1048576, mode: 'MiB', value: '512', revision: 'memory-one'};
  const logging = {supported: true, level: 'info', effective_level: 'info', revision: 'log-one'};
  const defaults = { rule: {id: 'new-rule', domains: ['example.com'], outbound: 'direct', family: 'auto'},
    group: {id: 'new-group', type: 'fallback', enabled: true, members: ['vpn-a', 'vpn-b']},
    rule_set: {id: 'new-set', name: 'New rule set', domains: ['example.com']}, outbounds: {warp: {warp_binary: 'warp-cli'}} };
  let intervalID = 0;
  const context = vm.createContext({document, localStorage, sessionStorage, console, structuredClone,
    NodeFilter: {SHOW_TEXT: 4}, CSS: {escape: value => String(value)},
    window: {addEventListener() {}}, location: {hostname: 'example.test', hash: ''}, history: {replaceState() {}},
    navigator: {clipboard: {writeText: async () => {}}}, confirm: () => true,
    setTimeout: timers.setTimeout, clearTimeout: timers.clearTimeout, setInterval: () => ++intervalID, clearInterval() {},
    fetch: async (url, options = {}) => {
      if (url === '/locales.json') return response(catalog);
      const request = {url, method: options.method || 'GET', body: options.body ? JSON.parse(options.body) : null};
      requests.push(request);
      const key = request.method + ' ' + url;
      if (gates.has(key)) await gates.get(key).promise;
      if (failures.has(key)) return response({error: failures.get(key)}, 503);
      if (url === '/api/v1/config' && request.method === 'PUT') {
        if (request.body.revision !== b.server.revision) return response({error: 'Configuration changed elsewhere. Your draft is kept.'}, 409);
        b.server = {...clone(request.body), revision: b.server.revision + 1};
        return response(b.server);
      }
      if (url === '/api/v1/config') return response(b.server);
      if (url === '/api/v1/defaults') return response(defaults);
      if (url === '/api/v1/stats') return response({config_revision: b.server.revision, flows: b.flowData, udp_dial_failures: b.udpFailures, proxy_udp_drops: b.udpDrops});
      if (url === '/api/v1/info') return response({version: 'test'});
      if (url === '/api/v1/outbounds' && request.method === 'PUT') {
        const outbound = request.body.outbound;
        const next = clone(b.server);
        const index = next.outbounds.findIndex(item => item.id === outbound.id);
        if (index < 0) next.outbounds.push(outbound); else next.outbounds[index] = outbound;
        b.server = {...next, revision: next.revision + 1}; return response(b.server);
      }
      if (url === '/api/v1/outbounds') return response(b.statusData);
      if (url === '/api/v1/source-clients') return response({clients: b.clients, generated_at: '2026-01-01T00:00:00Z'});
      if (url === '/api/v1/service/memory') return response(memory);
      if (url === '/api/v1/service/logging/warp') return response(logging);
      if (url === '/api/v1/integrations/docker') return response({});
      throw new Error(`Unexpected fixture request: ${key}`);
    },
  });
  b.context = context;
  b.run = source => vm.runInContext(source, context);
  b.get = id => { const node = document.getElementById(id); assert.ok(node, `Missing shipped control #${id}`); return node; };
  b.action = (container, attribute, id) => {
    const node = b.get(container).querySelectorAll(`[${attribute}]`).find(item => item.getAttribute(attribute) === id);
    assert.ok(node, `Missing ${attribute} action for ${id}`); return node;
  };
  b.change = async (id, value) => { const node = b.get(id); node.value = value; await node.dispatch('input'); await flush(); };
  b.check = async (node, checked = true) => { node.checked = checked; await node.dispatch('change'); await flush(); };
  b.key = async (node, key, modifiers = {}) => { node.focus(); const event = await node.dispatch('keydown', {key, ...modifiers}); await flush(); return event; };
  b.puts = () => requests.filter(request => request.method === 'PUT' && request.url === '/api/v1/config');
  b.publish = () => { context.next = clone(b.server); b.run('publishConfig(next)'); };
  b.page = page => {
    context.page = page; b.run('currentPage = page');
    for (const node of document.querySelectorAll('.page')) node.hidden = node.id !== `page-${page}`;
  };
  await loadScripts(context);
  assert.equal(requests.length, 0, 'loading full assets must not access authenticated data');
  assert.equal(document.events.get('DOMContentLoaded').length, 1, 'bootstrap must remain deferred');
  b.run('bindUI(); bindUI(); active = true;');
  b.get('app').hidden = false; b.get('login').hidden = true;
  b.publish();
  return b;
}
const rowIDs = (b, container, attribute) => b.get(container).querySelectorAll(`[${attribute}]`).map(node => node.getAttribute(attribute));
const checkedIDs = (b, container, attribute) => b.get(container).querySelectorAll(`[${attribute}]`).filter(node => node.checked).map(node => node.getAttribute(attribute));

async function visibleSelectionAndDeletion() {
  const b = await browser(); b.page('rules');
  await b.change('rules-search', 'match-');
  assert.deepEqual(rowIDs(b, 'rules', 'data-select-rule'), ['match-a', 'match-b']);
  await b.check(b.action('rules', 'data-select-rule', 'match-a'));
  assert.equal(b.get('rules-select-all').checked, false);
  assert.equal(b.get('rules-select-all').indeterminate, true, 'partial visible selection must be indeterminate');
  await b.check(b.get('rules-select-all'));
  assert.equal(b.get('rules-select-all').indeterminate, false);
  assert.deepEqual(checkedIDs(b, 'rules', 'data-select-rule'), ['match-a', 'match-b']);
  await b.change('rules-search', 'keep');
  assert.deepEqual(checkedIDs(b, 'rules', 'data-select-rule'), [], 'changing filter clears the collection selection');
  assert.equal(b.get('rules-select-all').checked, false);
  assert.equal(b.get('rules-select-all').indeterminate, false);
  await b.change('rules-search', 'match-'); await b.check(b.get('rules-select-all'));
  const beforeOutbounds = clone(b.server.outbounds), beforeGroups = clone(b.server.outbound_groups);
  const trigger = b.get('rules-bulk-delete'); trigger.focus(); await trigger.click();
  assert.equal(b.get('bulk-delete-dialog').open, true);
  assert.ok(b.get('bulk-delete-items').textContent.includes('match-a'));
  assert.ok(b.get('bulk-delete-items').textContent.includes('match-b'));
  assert.ok(!b.get('bulk-delete-items').textContent.includes('keep'), 'confirmation lists only selected visible IDs');
  assert.ok(b.document.activeElement.matches('[data-close]'), 'destructive confirmation starts on Cancel');
  await b.get('bulk-delete-form').dispatch('submit'); await flush();
  assert.equal(b.puts().length, 1);
  assert.equal(b.puts()[0].body.revision, 1);
  assert.deepEqual(b.server.rules.map(rule => rule.id), ['keep']);
  assert.deepEqual(b.server.outbounds, beforeOutbounds, 'deleting rules never deletes their outbounds');
  assert.deepEqual(b.server.outbound_groups, beforeGroups, 'deleting rules never deletes groups');
  assert.deepEqual(checkedIDs(b, 'rules', 'data-select-rule'), [], 'successful save clears selection');
}

async function sourceBulkActions() {
  for (const action of ['enable', 'disable', 'delete']) {
    const b = await browser(); b.page('sources');
    await b.check(b.action('source-rules-body', 'data-select-source-rule', 'allow-a'));
    assert.equal(b.get('source-rules-select-all').indeterminate, true);
    await b.check(b.get('source-rules-select-all'));
    assert.deepEqual(checkedIDs(b, 'source-rules-body', 'data-select-source-rule'), ['allow-a', 'allow-b', 'deny-c']);
    await b.check(b.get('source-rules-select-all'), false);
    await b.check(b.action('source-rules-body', 'data-select-source-rule', 'allow-a'));
    await b.check(b.action('source-rules-body', 'data-select-source-rule', 'allow-b'));
    const untouched = clone(b.server.source_access.rules[2]);
    await b.run('loadSourceClients()'); b.run('renderSourceRules()');
    assert.deepEqual(checkedIDs(b, 'source-rules-body', 'data-select-source-rule'), ['allow-a', 'allow-b'], 'background activity refresh preserves rule selection');
    await b.get(`source-rules-bulk-${action}`).click();
    assert.equal(b.get('bulk-delete-dialog').open, true, 'all source mutations require explicit confirmation');
    await b.get('bulk-delete-form').dispatch('submit'); await flush();
    assert.equal(b.puts().length, 1);
    assert.equal(b.puts()[0].body.revision, 1);
    const rules = b.server.source_access.rules;
    assert.deepEqual(rules.find(rule => rule.id === 'deny-c'), untouched);
    if (action === 'delete') assert.deepEqual(rules.map(rule => rule.id), ['deny-c']);
    else for (const id of ['allow-a', 'allow-b']) assert.equal(rules.find(rule => rule.id === id).enabled, action === 'enable');
    assert.deepEqual(checkedIDs(b, 'source-rules-body', 'data-select-source-rule'), []);
  }
}

async function fixedRevisionConflictAndReload() {
  for (const source of [false, true]) {
    const b = await browser(); b.page(source ? 'sources' : 'rules');
    const container = source ? 'source-rules-body' : 'rules', attribute = source ? 'data-select-source-rule' : 'data-select-rule';
    const id = source ? 'allow-a' : 'match-a';
    await b.check(b.action(container, attribute, id));
    await b.get(source ? 'source-rules-bulk-delete' : 'rules-bulk-delete').click();
    b.server.revision = 2;
    b.server.pac.proxy_address = '127.0.0.1:19000';
    const externallyChanged = clone(b.server);
    b.publish();
    await b.get('bulk-delete-form').dispatch('submit'); await flush();
    assert.equal(b.puts().length, 1);
    assert.equal(b.puts()[0].body.revision, 1, 'open confirmation cannot advance its captured revision');
    assert.deepEqual(b.server, externallyChanged, 'conflict leaves external configuration intact');
    assert.equal(b.get('bulk-delete-dialog').open, true);
    assert.ok(b.get('bulk-delete-form').querySelector('.form-error').textContent.trim());
    await b.timers.advance(60000);
    assert.ok(b.get('bulk-delete-form').querySelector('.form-error').textContent.trim(), 'conflict remains available for reading');
    const writes = b.puts().length;
    await b.get('bulk-delete-form').dispatch('submit'); await flush();
    assert.equal(b.puts().length, writes, 'conflicting destructive operations cannot auto-retry');
    b.failures.set('GET /api/v1/config', 'Fixture reload failed');
    await b.get('bulk-reload').click(); await flush();
    assert.equal(b.get('bulk-delete-dialog').open, true, 'failed reload preserves confirmation and its error');
    assert.equal(b.puts().length, writes);
    b.failures.delete('GET /api/v1/config');
    await b.get('bulk-reload').click(); await flush();
    assert.equal(b.puts().length, writes, 'reload cannot replay mutation');
    assert.deepEqual(checkedIDs(b, container, attribute), [], 'explicit reload clears selection');
    assert.equal(b.get(source ? 'source-rules-select-all' : 'rules-select-all').indeterminate, false);
    assert.ok(b.requests.some(request => request.method === 'GET' && request.url === '/api/v1/config'));
  }
}

async function ruleReorderAndFocus() {
  const b = await browser(); b.page('rules');
  await b.change('rules-search', 'match');
  for (const button of b.get('rules').querySelectorAll('[data-up-rule],[data-down-rule]')) assert.equal(button.disabled, true);
  const filtered = b.action('rules', 'data-edit-rule', 'match-b');
  await b.key(filtered, 'ArrowUp', {altKey: true});
  assert.equal(b.puts().length, 0, 'filtered keyboard reorder cannot mutate hidden positions');
  assert.deepEqual(b.server.rules.map(rule => rule.id), ['match-a', 'match-b', 'keep']);
  await b.change('rules-search', '');
  for (const [key, expected] of [['ArrowDown', ['match-a', 'keep', 'match-b']], ['ArrowUp', ['match-a', 'match-b', 'keep']],
    ['Home', ['match-b', 'match-a', 'keep']], ['End', ['match-a', 'keep', 'match-b']]]) {
    await b.key(b.action('rules', 'data-edit-rule', 'match-b'), key, {altKey: true});
    assert.deepEqual(b.server.rules.map(rule => rule.id), expected);
    assert.equal(b.document.activeElement.getAttribute('data-edit-rule'), 'match-b', 'render keeps focus on the same ID and action');
  }
  assert.equal(b.puts().length, 4, 'one config write per keyboard move');
  assert.deepEqual(b.puts().map(request => request.body.revision), [1, 2, 3, 4]);
  const last = b.action('rules', 'data-down-rule', 'match-b'); assert.equal(last.disabled, true);
  await b.key(b.action('rules', 'data-edit-rule', 'match-b'), 'End', {altKey: true});
  assert.equal(b.puts().length, 4, 'boundary move is a no-op');
  await b.action('rules', 'data-up-rule', 'match-b').click();
  assert.equal(b.document.activeElement.getAttribute('data-up-rule'), 'match-b');
  assert.deepEqual(b.server.rules.map(rule => rule.id), ['match-a', 'match-b', 'keep']);
}

async function groupDraftReorder() {
  const b = await browser(); b.page('outbounds');
  await b.action('group-list', 'data-edit-group', 'group-a').click();
  const member = () => b.get('group-members').querySelectorAll('[data-member]').find(node => node.dataset.member === 'vpn-b').querySelector('input');
  await b.key(member(), 'Home', {altKey: true});
  assert.equal(b.document.activeElement.closest('[data-member]').dataset.member, 'vpn-b');
  assert.equal(b.document.activeElement.matches('input'), true);
  assert.deepEqual(rowIDs(b, 'group-members', 'data-member'), ['vpn-b', 'vpn-a']);
  await b.key(member(), 'End', {altKey: true});
  assert.deepEqual(rowIDs(b, 'group-members', 'data-member'), ['vpn-a', 'vpn-b']);
  await b.key(member(), 'ArrowUp', {altKey: true});
  assert.deepEqual(rowIDs(b, 'group-members', 'data-member'), ['vpn-b', 'vpn-a']);
  await b.key(member(), 'ArrowDown', {altKey: true});
  assert.deepEqual(rowIDs(b, 'group-members', 'data-member'), ['vpn-a', 'vpn-b']);
  assert.equal(b.document.activeElement.closest('[data-member]').dataset.member, 'vpn-b');
  assert.equal(b.puts().length, 0, 'member ordering only edits the unsaved draft');
  assert.deepEqual(b.server.outbound_groups[0].members, ['vpn-a', 'vpn-b']);
}

async function busyDoubleSubmits() {
  const cases = [
    ['pac-form', 'PUT /api/v1/config', async b => b.page('settings')],
    ['source-capacity-form', 'PUT /api/v1/config', async b => { b.page('sources'); await b.change('capacity-max-total', '5000'); }],
    ['rule-form', 'PUT /api/v1/config', async b => { b.page('rules'); await b.action('rules', 'data-edit-rule', 'match-a').click(); await b.change('rule-domains', 'draft.example'); }],
    ['group-form', 'PUT /api/v1/config', async b => { b.page('outbounds'); await b.action('group-list', 'data-edit-group', 'group-a').click(); }],
    ['rule-set-form', 'PUT /api/v1/config', async b => { b.page('rules'); await b.action('rule-set-list', 'data-edit-set', 'set-a').click(); await b.change('rs-name', 'Edited'); }],
    ['outbound-form', 'PUT /api/v1/outbounds', async b => { b.page('outbounds'); await b.get('outbound-list').querySelectorAll('[data-outbound-id]').find(node => node.dataset.outboundId === 'vpn-a').querySelector('[data-edit-outbound]').click(); }],
    ['memory-form', 'PUT /api/v1/service/memory', async b => { b.page('settings'); await b.run('loadMemory()'); }],
    ['warp-log-form', 'PUT /api/v1/service/logging/warp', async b => { b.page('settings'); await b.run('loadWarpLog()'); }],
  ];
  for (const [formID, key, prepare] of cases) {
    const b = await browser(); await prepare(b);
    const form = b.get(formID), button = form.querySelector('button.primary');
    assert.ok(button, `${formID} needs a submit control`);
    const originallyDisabled = button.disabled, originalText = button.textContent;
    const gate = deferred(); b.gates.set(key, gate);
    const first = form.dispatch('submit'); await flush();
    assert.equal(button.getAttribute('aria-busy'), 'true', `${formID} exposes busy state`);
    const second = form.dispatch('submit'); await flush();
    assert.equal(b.requests.filter(request => `${request.method} ${request.url}` === key).length, 1, `${formID} double submit sends one mutation`);
    gate.resolve(); await Promise.all([first, second]); await flush();
    assert.notEqual(button.getAttribute('aria-busy'), 'true');
    assert.equal(button.disabled, originallyDisabled, `${formID} restores previous availability`);
    assert.equal(button.textContent, originalText, `${formID} restores its label`);
  }
  const b = await browser(), disabledButton = b.get('save-default'); disabledButton.disabled = true;
  b.context.disabledButton = disabledButton;
  await b.run('withBusy(disabledButton,async () => {})');
  assert.equal(disabledButton.disabled, true, 'busy helper must not enable a previously unavailable control');
}

async function searchUsesLoadedFields() {
  const b = await browser(); b.page('rules');
  Object.assign(b.server.rules[0], {suffixes:['suffix.example'],cidrs:['198.18.0.0/15'],sources:['203.0.113.0/24'],inbounds:['socks5'],ports:['8040-8050'],sets:['set-a']});
  b.publish();
  for (const query of ['one.example', 'suffix.example', '198.18.0.0/15', '203.0.113.0/24', 'socks5', '8040-8050', 'group-a', 'set-a']) {
    await b.change('rules-search', query);
    assert.deepEqual(rowIDs(b, 'rules', 'data-rule-id'), ['match-a'], 'rules search includes all configured condition fields');
  }
  b.page('outbounds');
  b.statusData = [{id:'vpn-a',state:'connected'},{id:'vpn-b',state:'unavailable'}];
  await b.run('pollStatuses()');
  await b.change('outbound-search', 'connected');
  assert.deepEqual(rowIDs(b, 'outbound-list', 'data-outbound-id'), ['vpn-a']);
  await b.change('outbound-search', 'fallback');
  assert.ok(b.get('group-list').textContent.includes('group-a'), 'search includes group type');
  await b.change('outbound-search', 'vpn-b');
  assert.ok(b.get('group-list').textContent.includes('group-a'), 'search includes group members');
  b.page('sources');
  b.clients = [{address:'192.0.2.10',rule_id:'allow-a',allowed:true,active_connections:0,accepted_connections:1,denied_connections:0},
    {address:'198.51.100.10',rule_id:'deny-c',allowed:false,active_connections:0,accepted_connections:0,denied_connections:1}];
  await b.run('loadSourceClients()');
  for (const query of ['192.0.2.10', 'allow-a']) {
    await b.change('source-clients-search', query);
    assert.deepEqual(rowIDs(b, 'source-clients-body', 'data-client-addr'), ['192.0.2.10']);
  }
  assert.equal(b.puts().length, 0, 'searching never saves or evaluates routes');
}

async function emptyAndNoMatchStates() {
  const b = await browser();
  b.flowData = [{id: 1, host: 'one.example', address: 'one.example:443', port: '443', outbound: 'vpn-a', active: true}];
  b.clients = [{address: '192.0.2.10', rule_id: 'allow-a', allowed: true, active_connections: 1, accepted_connections: 1, denied_connections: 0}];
  b.context.clientFixture = clone(b.clients); b.context.flowFixture = clone(b.flowData);
  b.run('flows = flowFixture; sourceClients = clientFixture; renderFlows(); renderSourceClients();');
  for (const [page, search, list, empty, noMatch] of [
    ['overview', 'flow-search', 'flow-groups', 'flows-empty', 'flow-no-match'],
    ['outbounds', 'outbound-search', 'outbound-list', 'outbounds-empty', 'outbound-no-match'],
    ['rules', 'rules-search', 'rules', 'rules-empty', 'rules-no-match'],
    ['sources', 'source-clients-search', 'source-clients-body', 'source-clients-empty', 'source-clients-no-match'],
  ]) {
    b.page(page);
    await b.change(search, 'not-present-<script>公司😀');
    assert.equal(b.get(empty).hidden, true, `${page} cannot show collection-empty for a filtered collection`);
    assert.equal(b.get(noMatch).hidden, false);
    assert.ok(b.get(noMatch).textContent.includes('not-present-<script>公司😀'));
    assert.equal(b.get(noMatch).querySelector('script'), null, 'search data is text, never markup');
    const clear = b.get(noMatch).querySelector(`[data-clear-search="${search}"]`); assert.ok(clear);
    await clear.click();
    assert.equal(b.get(search).value, '');
    assert.equal(b.document.activeElement, b.get(search));
    assert.equal(b.get(noMatch).hidden, true);
    assert.ok(b.get(list).children.length > 0);
  }
  b.server.rules = []; b.publish();
  assert.equal(b.get('rules-empty').hidden, false);
  assert.equal(b.get('rules-no-match').hidden, true);
  b.run('flows = []; sourceClients = []; renderFlows(); renderSourceClients();');
  assert.equal(b.get('flows-empty').hidden, false);
  assert.equal(b.get('source-clients-empty').hidden, false);
  assert.equal(b.get('flow-no-match').hidden, true);
  assert.equal(b.get('source-clients-no-match').hidden, true);
  b.server.outbounds = []; b.server.outbound_groups = []; b.publish();
  assert.equal(b.get('outbounds-empty').hidden, false);
  assert.equal(b.get('outbound-no-match').hidden, true);
  assert.equal(b.get('groups-empty').hidden, false);
  b.server.rule_sets = []; b.publish(); assert.equal(b.get('rule-sets-empty').hidden, false);
}

async function shortcutsAndModalPriority() {
  const b = await browser();
  for (const [page, id] of [['overview', 'flow-search'], ['outbounds', 'outbound-search'], ['rules', 'rules-search'],
    ['sources', 'source-clients-search'], ['glossary', 'glossary-search']]) {
    b.page(page);
    const event = await b.key(b.get('logout'), '/');
    assert.equal(event.defaultPrevented, true);
    assert.equal(b.document.activeElement, b.get(id));
    await b.change(id, 'keep');
    const escape = await b.key(b.get(id), 'Escape');
    assert.equal(escape.defaultPrevented, true); assert.equal(b.get(id).value, '');
    assert.equal(b.document.activeElement, b.get(id));
  }
  b.page('settings');
  const settingsEvent = await b.key(b.get('save-default'), '/'); assert.equal(settingsEvent.defaultPrevented, false);
  for (const node of [b.get('pac-address'), b.get('rule-domains'), b.get('default-outbound')]) {
    for (const key of ['/', '?']) assert.equal((await b.key(node, key)).defaultPrevented, false, 'typing controls keep text shortcuts');
  }
  const editable = b.document.createElement('div'); editable.setAttribute('contenteditable', 'true'); b.document.body.append(editable);
  assert.equal((await b.key(editable, '/')).defaultPrevented, false);
  b.page('rules'); const opener = b.get('add-rule');
  await b.key(opener, '?');
  assert.equal(b.get('shortcuts-dialog').open, true);
  assert.ok(b.get('shortcuts-dialog').querySelector('dl'));
  const focused = b.document.activeElement;
  assert.equal((await b.key(focused, '/')).defaultPrevented, false, 'modal owns keyboard input');
  await b.get('shortcuts-dialog').dispatch('cancel');
  assert.equal(b.get('shortcuts-dialog').open, false);
  assert.equal(b.document.activeElement, opener);
  await b.change('rules-search', 'match');
  await b.action('rules', 'data-edit-rule', 'match-a').click();
  assert.equal(b.get('rule-dialog').open, true);
  await b.key(b.get('rules-search'), 'Escape');
  assert.equal(b.get('rules-search').value, 'match', 'search clearing must not intercept modal Escape');
  await b.get('rule-dialog').dispatch('cancel'); assert.equal(b.get('rule-dialog').open, false);
}

async function noticeLifetime() {
  const b = await browser();
  assert.equal(b.get('notice').getAttribute('role'), 'status');
  b.run("notice('Configuration saved. New connections will use the updated rules.')");
  await b.timers.advance(5999); assert.equal(b.get('notice').hidden, false);
  await b.timers.advance(1); assert.equal(b.get('notice').hidden, true);
  b.run("notice('First success')"); await b.timers.advance(5000);
  b.run("notice('Second success')"); await b.timers.advance(1000);
  assert.equal(b.get('notice').hidden, false, 'new success replaces the old expiry');
  await b.timers.advance(5000); assert.equal(b.get('notice').hidden, true);
  b.run("notice('Success to replace')"); await b.timers.advance(1000);
  b.run("notice(new Error('Persistent failure'),true)");
  await b.timers.advance(60000);
  assert.equal(b.get('notice').hidden, false, 'failures never auto-dismiss');
  assert.equal(b.get('notice').textContent, 'Persistent failure');
  b.run("notice('Recovery')"); await b.timers.advance(6000); assert.equal(b.get('notice').hidden, true);
}

async function scopedLoadingAndFocusReturn() {
  for (const [page, call, endpoint, busyID, focusedID] of [
    ['overview', 'poll()', 'GET /api/v1/stats', 'flow-groups', 'flow-search'],
    ['outbounds', 'pollStatuses()', 'GET /api/v1/outbounds', 'outbound-list', 'outbound-search'],
    ['sources', 'loadSourceClients()', 'GET /api/v1/source-clients', 'source-clients-body', 'source-clients-search'],
  ]) {
    const b = await browser(); b.page(page);
    b.get(focusedID).focus();
    const rows = [...b.get(busyID).children];
    const gate = deferred(); b.gates.set(endpoint, gate);
    const pending = b.run(call); await flush();
    assert.equal(b.get(busyID).getAttribute('aria-busy'), 'true');
    assert.equal(b.get(busyID + '-loading').hidden, false, 'first request presents a Loading status');
    assert.ok(b.get(busyID + '-loading').textContent.trim());
    for (const other of ['flow-groups', 'outbound-list', 'source-clients-body', 'rules', 'group-list', 'rule-set-list'].filter(id => id !== busyID)) {
      assert.notEqual(b.get(other).getAttribute('aria-busy'), 'true', `${call} must not mark unrelated ${other} loading`);
    }
    if (rows.length) assert.deepEqual(b.get(busyID).children, rows, 'background refresh retains current rows');
    gate.resolve(); await pending; await flush();
    assert.notEqual(b.get(busyID).getAttribute('aria-busy'), 'true');
    assert.equal(b.document.activeElement, b.get(focusedID), 'loading cannot steal search focus');
    assert.equal(b.get(busyID + '-loading').hidden, true);
    b.gates.delete(endpoint);
    b.flowData = [{id: 1, host: 'loaded.example', port: '443', outbound: 'vpn-a', active: true}];
    b.clients = [{address: '192.0.2.11', allowed: true, active_connections: 1, accepted_connections: 1, denied_connections: 0}];
    await b.run(call);
    const loadedRows = [...b.get(busyID).children];
    assert.ok(loadedRows.length > 0);
    const refreshGate = deferred(); b.gates.set(endpoint, refreshGate);
    const refreshing = b.run(call); await flush();
    assert.equal(b.get(busyID).getAttribute('aria-busy'), 'true');
    assert.equal(b.get(busyID + '-loading').hidden, true, 'refresh never flashes the first-load status');
    assert.deepEqual(b.get(busyID).children, loadedRows);
    refreshGate.resolve(); await refreshing; await flush();
    b.gates.delete(endpoint); b.failures.set(endpoint, 'Fixture refresh failed');
    await b.run(call); assert.notEqual(b.get(busyID).getAttribute('aria-busy'), 'true', 'failure clears busy in finally');
  }
  const b = await browser(); b.page('rules');
  const gate = deferred(); b.gates.set('GET /api/v1/config', gate);
  const loading = b.run('load()'); await flush();
  for (const id of ['rules', 'group-list', 'rule-set-list']) assert.equal(b.get(id).getAttribute('aria-busy'), 'true', 'only actual config fetch marks config collections busy');
  assert.notEqual(b.get('flow-groups').getAttribute('aria-busy'), 'true');
  gate.resolve(); await loading; await flush();
  for (const id of ['rules', 'group-list', 'rule-set-list']) assert.notEqual(b.get(id).getAttribute('aria-busy'), 'true');
  b.gates.delete('GET /api/v1/config');
  b.failures.set('GET /api/v1/config', 'Fixture configuration load failed');
  await assert.rejects(b.run('load()'));
  for (const id of ['rules', 'group-list', 'rule-set-list']) assert.notEqual(b.get(id).getAttribute('aria-busy'), 'true', 'failed config fetch clears busy');
  b.failures.delete('GET /api/v1/config');
  b.page('rules');
  const opener = b.action('rules', 'data-edit-rule', 'match-a'); opener.focus(); await opener.click();
  assert.equal(b.document.activeElement, b.get('rule-id'), 'editor focuses first available field');
  b.publish();
  await b.get('rule-dialog').dispatch('cancel');
  assert.equal(b.document.activeElement, b.action('rules', 'data-edit-rule', 'match-a'), 'close resolves re-rendered opener by stable ID');
  await b.action('rules', 'data-edit-rule', 'match-a').click();
  b.server.rules = b.server.rules.filter(rule => rule.id !== 'match-a'); b.publish();
  await b.get('rule-dialog').dispatch('cancel'); assert.equal(b.document.activeElement, b.get('add-rule'), 'deleted opener returns focus to Add');
  const selected = b.action('rules', 'data-select-rule', 'match-b'); await b.check(selected);
  await b.get('rules-bulk-delete').click();
  const cancel = b.get('bulk-delete-dialog').querySelector('[data-close].quiet'); assert.equal(b.document.activeElement, cancel);
  await cancel.click(); assert.equal(b.document.activeElement, b.get('rules-bulk-delete'));
  assert.equal(b.puts().length, 0, 'cancel has no mutation');
  const skip = b.document.querySelector('.skip-link'); assert.ok(skip); assert.equal(skip.getAttribute('href'), '#main-content');
  assert.equal(b.get('main-content').getAttribute('tabindex'), '-1');
}

async function udpObservability() {
  const b = await browser(); b.page('overview');
  b.flowData = [{id: 1, host: 'quic.example', port: '443', outbound: 'vpn-a', network: 'udp'},
    {id: 2, host: 'quic.example', port: '443', outbound: 'vpn-a', network: 'tcp'},
    {id: 3, host: 'quic.example', port: '443', outbound: 'vpn-a'},
    {id: 4, host: 'quic.example', port: '443', outbound: 'vpn-a', network: 'future<b>'}];
  b.get('flow-search').focus();
  await b.run('poll()');
  assert.deepEqual(b.get('flow-groups').querySelectorAll('[data-network]').map(node => node.textContent), ['UDP', 'TCP', 'future<b>'],
    'known protocols use uppercase names, missing values render no badge, unknown values stay verbatim text');
  assert.equal(b.get('flow-groups').querySelector('b'), null, 'protocol data is text, never markup');
  assert.equal(b.document.activeElement, b.get('flow-search'), 'stats refresh keeps search focus');
  for (const id of ['udp-drop-target-limit', 'udp-drop-recent-failure', 'udp-drop-local-destination']) assert.equal(b.get(id).textContent, '0');
  assert.equal(b.get('udp-dial-failures-empty').hidden, false, 'no failures shows the empty state');
  assert.equal(b.get('udp-dial-failures-table').hidden, true);
  assert.equal(b.get('udp-dial-failures').querySelectorAll('[data-udp-failure]').length, 0);

  b.udpFailures = [{outbound: 'warp-公司😀', reason: 'unsupported', count: 3},
    {outbound: 'wg-a', reason: 'future_reason', count: 1}, {outbound: 'wg-b', reason: 'toString', count: 2}];
  b.udpDrops = {target_limit: 7, recent_failure: 2, local_destination: 1};
  await b.run('poll()');
  assert.deepEqual(['udp-drop-target-limit', 'udp-drop-recent-failure', 'udp-drop-local-destination'].map(id => b.get(id).textContent), ['7', '2', '1']);
  assert.equal(b.get('udp-dial-failures-empty').hidden, true);
  assert.equal(b.get('udp-dial-failures-table').hidden, false);
  const rows = () => b.get('udp-dial-failures').querySelectorAll('[data-udp-failure]').map(row => ({
    outbound: row.querySelector('[data-udp-failure-outbound]').textContent,
    reason: row.querySelector('[data-udp-failure-reason]').textContent,
    count: row.querySelector('[data-udp-failure-count]').textContent,
  }));
  let rendered = rows();
  assert.deepEqual(rendered.map(row => row.outbound), ['warp-公司😀', 'wg-a', 'wg-b'], 'outbound IDs stay verbatim');
  assert.deepEqual(rendered.map(row => row.count), ['3', '1', '2']);
  assert.ok(rendered[0].reason.includes(catalog.en['UDP not supported']), 'known reasons use the catalog label');
  assert.equal(rendered[1].reason, 'future_reason', 'unknown reasons stay verbatim');
  assert.equal(rendered[2].reason, 'toString', 'inherited object keys are not treated as known reasons');
  b.run("switchLocale('zh-Hant')");
  rendered = rows();
  assert.ok(rendered[0].reason.includes(catalog['zh-Hant']['UDP not supported']), 'known reasons follow the selected locale');
  assert.equal(rendered[0].outbound, 'warp-公司😀');
  assert.equal(rendered[1].reason, 'future_reason');
  assert.equal(b.puts().length, 0, 'observability never writes configuration');
}

// A pending VM Promise alone cannot keep Node alive. Fail instead of silently
// exiting successfully when a renderer or submit handler never completes.
const watchdog = setTimeout(() => { console.error('Browser interaction contracts did not complete.'); process.exitCode = 1; }, 40000);
(async () => {
  for (const test of [visibleSelectionAndDeletion, sourceBulkActions, fixedRevisionConflictAndReload, ruleReorderAndFocus,
    groupDraftReorder, busyDoubleSubmits, searchUsesLoadedFields, emptyAndNoMatchStates, shortcutsAndModalPriority, noticeLifetime, scopedLoadingAndFocusReturn,
    udpObservability]) {
    await test(); console.log(`PASS ${test.name}`);
  }
})().catch(error => { console.error(error); process.exitCode = 1; }).finally(() => clearTimeout(watchdog));
