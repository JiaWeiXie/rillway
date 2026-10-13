'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const {loadScripts, web} = require('./shipped_scripts');
const catalog = JSON.parse(fs.readFileSync(require('node:path').join(web, 'locales.json'), 'utf8'));
const elements = new Map();
function get(id) {
  if (!elements.has(id)) elements.set(id, {
    value: '', textContent: '', innerHTML: '', disabled: false, dataset: {}, events: {}, eventCounts: {},
    attributes: {}, setAttribute(name, value) { this.attributes[name] = String(value); },
    getAttribute(name) { return this.attributes[name] ?? null; }, removeAttribute(name) { delete this.attributes[name]; },
    querySelectorAll() { return []; }, focus() {}, close() {}, reset() {},
    querySelector(selector) { return get(selector === 'button.primary' ? id + '-submit' : id + '-error'); },
    contains() { return false; },
    addEventListener(event, callback) {
      this.events[event] = callback;
      this.eventCounts[event] = (this.eventCounts[event] || 0) + 1;
    },
  });
  return elements.get(id);
}
const documentEvents = {};
const context = vm.createContext({
  document: {
    readyState: 'loading', documentElement: { removeAttribute() {}, setAttribute() {} }, getElementById: get, body: {querySelectorAll() { return []; }}, hidden: false, activeElement: null,
    createTreeWalker() { return {nextNode() { return false; }}; },
    querySelector(selector) { return selector === '.skip-link' ? get('skip-link') : null; },
    querySelectorAll() { return []; },
    addEventListener(event, callback) { documentEvents[event] = callback; },
  },
  window: { addEventListener() {} },
  NodeFilter: {SHOW_TEXT: 4}, fetch: async () => ({ok: true, json: async () => catalog}),
  setTimeout, clearTimeout,
  sessionStorage: { getItem() { return ''; } }, localStorage: { getItem() { return ''; } },
  location: { hostname: 'example.test', hash: '' }, console, structuredClone,
});
loadScripts(context);
assert.equal(typeof documentEvents.DOMContentLoaded, 'function');
assert.equal(get('source-capacity-form').events.submit, undefined, 'loading must defer listener registration');
vm.runInContext('bindUI(); bindUI();', context);
assert.equal(get('source-capacity-form').eventCounts.submit, 1, 'binding twice must not duplicate mutations');
vm.runInContext(`active = true; currentPage = 'sources';
  cfg = {revision: 1, connection_limits: {max_connections: 4, max_connections_per_source: 2}};
  capacityDraftRevision = 1; renderConfig = () => {}; poll = async () => {}; notice = () => {};`, context);
const snapshot = (total, source) => ({ clients: [], capacity: {
  active_connections: 0, max_connections: total, max_connections_per_source: source,
  source_limit: 0, total_limit: 0,
} });
let releaseOld;
let sourceRequests = 0;
let server = { revision: 1, connection_limits: { max_connections: 4, max_connections_per_source: 2 } };
context.apiFixture = async (path, options) => {
  if (path === '/config' && options?.method === 'PUT') {
    server = { ...JSON.parse(options.body), revision: 2 };
    return structuredClone(server);
  }
  if (path === '/source-clients') {
    if (++sourceRequests === 1) return new Promise(resolve => { releaseOld = () => resolve(snapshot(4, 2)); });
    return snapshot(server.connection_limits.max_connections, server.connection_limits.max_connections_per_source);
  }
  throw new Error(`Unexpected request: ${path}`);
};
vm.runInContext('api = apiFixture;', context);
(async () => {
  const oldPoll = vm.runInContext('loadSourceClients()', context);
  get('capacity-max-total').value = '20';
  get('capacity-max-per-source').value = '10';
  const save = get('source-capacity-form').events.submit({ preventDefault() {}, target: get('source-capacity-form') });
  await new Promise(setImmediate);
  assert.equal(get('source-capacity-fields').disabled, true, 'save must wait for an up-to-date capacity snapshot');
  releaseOld();
  await Promise.all([oldPoll, save]);
  assert.equal(get('source-capacity-total').textContent, '0 / 20');
  assert.equal(get('source-capacity-per-source').textContent, '0 / 10');
  assert.equal(get('source-capacity-fields').disabled, false);
  console.log('Capacity save waits for a fresh snapshot after an in-flight older poll.');
})().catch(error => { console.error(error); process.exitCode = 1; });
