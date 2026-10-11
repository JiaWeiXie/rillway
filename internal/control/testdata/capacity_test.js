'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const app = fs.readFileSync('web/app.js', 'utf8');
const elements = new Map();
function get(id) {
  if (!elements.has(id)) elements.set(id, {
    value: '', textContent: '', innerHTML: '', disabled: false, dataset: {}, events: {},
    querySelector() { return get('error'); },
    contains() { return false; },
    addEventListener(event, callback) { this.events[event] = callback; },
  });
  return elements.get(id);
}
const context = vm.createContext({
  document: { getElementById: get, body: {}, hidden: false, activeElement: null },
  globalThis: { RillwayI18n: { locale: 'en', capture() {}, t(source, values = {}) {
    return source.replace(/\{(\w+)\}/g, (_, key) => values[key] ?? key);
  } } },
  sessionStorage: { getItem() { return ''; } }, localStorage: { getItem() { return ''; } },
  location: { hostname: 'example.test' }, console, structuredClone,
});
vm.runInContext(app.slice(0, app.indexOf("document.querySelectorAll('[data-locale]').forEach")), context);
vm.runInContext(app.slice(app.indexOf('function onCapacityEdit()'), app.indexOf("$('outbound-list').addEventListener")), context);
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
