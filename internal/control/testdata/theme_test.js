'use strict';
// Exercise full shipped scripts in their HTML parser/defer order. Only browser
// platform boundaries are faked; theme and application behavior stay real.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const web = path.join(__dirname, '..', 'web');
const markup = fs.readFileSync(path.join(web, 'index.html'), 'utf8');
const catalog = JSON.parse(fs.readFileSync(path.join(web, 'locales.json'), 'utf8'));

function attributes(text) {
  const result = {};
  for(const match of text.matchAll(/([^\s=/>]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?/g)) {
    result[match[1].toLowerCase()] = match[2] ?? match[3] ?? match[4] ?? '';
  }
  return result;
}

function element(tagName, attrs = {}) {
  const listeners = new Map();
  const node = {
    tagName:tagName.toUpperCase(), attributes:{}, dataset:{}, value:'',
    textContent:'', innerHTML:'', hidden:false, disabled:false, checked:false,
    open:false, isConnected:true, classList:{toggle(){}},
    setAttribute(name, value) {
      this.attributes[name] = String(value);
      if(name === 'id') this.id = String(value);
      if(name.startsWith('data-')) {
        const key = name.slice(5).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase());
        this.dataset[key] = String(value);
      }
    },
    getAttribute(name) { return this.attributes[name] ?? null; },
    removeAttribute(name) {
      delete this.attributes[name];
      if(name.startsWith('data-')) {
        const key = name.slice(5).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase());
        delete this.dataset[key];
      }
    },
    addEventListener(name, callback) {
      if(!listeners.has(name)) listeners.set(name, []);
      listeners.get(name).push(callback);
    },
    listenerCount(name) { return listeners.get(name)?.length ?? 0; },
    async dispatch(name) {
      for(const callback of listeners.get(name) ?? []) {
        await callback({target:this, currentTarget:this, preventDefault(){}});
      }
    },
    querySelectorAll() { return []; }, querySelector() { return null; },
    contains() { return false; }, showModal() { this.open = true; },
    close() { this.open = false; }, focus() {}
  };
  for(const [name, value] of Object.entries(attrs)) node.setAttribute(name, value);
  return node;
}

function matches(node, selector) {
  return selector.split(',').some(part => {
    const query = part.trim();
    const tag = query.match(/^[a-z][\w-]*/i)?.[0];
    if(tag && node.tagName !== tag.toUpperCase()) return false;
    const id = query.match(/#([\w-]+)/)?.[1];
    if(id && node.id !== id) return false;
    for(const match of query.matchAll(/\.([\w-]+)/g)) {
      if(!(node.getAttribute('class') ?? '').split(/\s+/).includes(match[1])) return false;
    }
    for(const match of query.matchAll(/\[([^\]=]+)(?:=["']?([^\]"']*)["']?)?\]/g)) {
      const value = node.getAttribute(match[1]);
      if(value === null || (match[2] !== undefined && value !== match[2])) return false;
    }
    return true;
  });
}

function storage(initial = {}, {getThrows = false, setThrows = false} = {}) {
  const values = new Map(Object.entries(initial));
  const writes = [];
  return {
    values, writes,
    getItem(key) {
      if(getThrows) throw new Error('Storage reads denied');
      return values.get(key) ?? null;
    },
    setItem(key, value) {
      writes.push([key, String(value)]);
      if(setThrows) throw new Error('Storage writes denied');
      values.set(key, String(value));
    },
    removeItem(key) { values.delete(key); }
  };
}

async function browser(localStorage = storage()) {
  const nodes = [], requests = [], deferred = [];
  const root = element('html');
  let bindCalls = 0, wrapped = false, beforeBody;
  const document = {
    readyState:'loading', documentElement:root, body:null, hidden:false,
    activeElement:null, title:'', addEventListener(){},
    getElementById(id) { return nodes.find(node => node.id === id) ?? null; },
    querySelectorAll(selector) { return nodes.filter(node => matches(node, selector)); },
    querySelector(selector) { return this.querySelectorAll(selector)[0] ?? null; },
    createTreeWalker() { return {nextNode(){return false;}}; }
  };
  const context = vm.createContext({
    document, localStorage, sessionStorage:storage(),
    window:{addEventListener(){}}, location:{hostname:'example.test', hash:''},
    history:{replaceState(){}}, navigator:{language:'en'}, NodeFilter:{SHOW_TEXT:4},
    structuredClone, setTimeout:() => 1, clearTimeout(){},
    setInterval:() => 1, clearInterval(){},
    fetch:async(url, options) => {
      requests.push({url, options});
      if(url === '/locales.json') return {ok:true, json:async() => catalog};
      return {ok:false, status:401, json:async() => ({
        error_source:'Enter a valid management token to sign in.',
        error:'Enter a valid management token to sign in.'
      })};
    }
  });
  function run(script) {
    const src = script.src;
    assert.match(src, /^\/[\w-]+\.js$/, 'scripts must remain bundled local assets');
    vm.runInContext(fs.readFileSync(path.join(web, src.slice(1)), 'utf8'), context, {filename:src});
    if(context.RillwayTheme && !wrapped) {
      wrapped = true;
      const bind = context.RillwayTheme.bind;
      context.RillwayTheme.bind = (...args) => { bindCalls++; return bind(...args); };
    }
  }
  // Minimal parser for the shipped static markup, not source-code slicing.
  // Head scripts execute before a body exists; deferred scripts execute only
  // after all controls have been parsed, preserving their HTML order.
  for(const token of markup.matchAll(/<([a-z][\w-]*)\b([^>]*)>/gi)) {
    const tag = token[1].toLowerCase(), attrs = attributes(token[2]);
    if(tag === 'body') {
      assert.equal(document.body, null);
      assert.ok(context.RillwayTheme, 'theme must initialize synchronously before body parsing');
      beforeBody = {
        preference:context.RillwayTheme.getPreference(),
        theme:root.getAttribute('data-theme'),
        scheme:document.querySelector('meta[name="color-scheme"]')?.getAttribute('content')
      };
    }
    const node = tag === 'html' ? root : element(tag, attrs);
    nodes.push(node);
    if(tag === 'body') {
      document.body = node;
      node.querySelectorAll = selector => document.querySelectorAll(selector);
    }
    if(tag === 'script') {
      assert.ok(attrs.src, 'the HTML loader expects external bundled scripts');
      assert.equal(Object.hasOwn(attrs, 'async'), false, 'script order must be deterministic');
      if(Object.hasOwn(attrs, 'defer')) deferred.push(attrs);
      else run(attrs);
    }
  }
  assert.ok(beforeBody, 'HTML must contain the body loading boundary');
  for(const script of deferred) run(script);
  await context.RillwayI18n.ready;
  return {
    context, document, root, localStorage, requests, beforeBody,
    get:id => document.getElementById(id),
    get bindCalls() { return bindCalls; }
  };
}

function assertSynced(b, preference) {
  assert.equal(b.context.RillwayTheme.getPreference(), preference);
  assert.equal(b.root.getAttribute('data-theme'), preference === 'system' ? null : preference);
  assert.equal(b.document.querySelector('meta[name="color-scheme"]').getAttribute('content'),
    preference === 'system' ? 'light dark' : preference);
  for(const id of ['login-theme', 'sidebar-theme']) {
    const select = b.get(id);
    assert.ok(select && select.tagName === 'SELECT', `${id} must be an available theme control`);
    assert.ok(b.document.querySelector(`label[for="${id}"]`), `${id} must have a label`);
    assert.equal(select.value, preference, `${id} must follow the current preference`);
  }
}

function bind(b) {
  assert.equal(b.bindCalls, 0, 'app evaluation must not bind before bootstrap');
  vm.runInContext('bindUI(); bindUI();', b.context);
  assert.equal(b.bindCalls, 1, 'application binding must call theme.bind exactly once');
  for(const id of ['login-theme', 'sidebar-theme']) {
    assert.equal(b.get(id).listenerCount('change'), 1, 'each theme control binds once');
  }
  assert.equal(b.get('login-form').listenerCount('submit'), 1, 'theme initialization must not block sign-in');
}

async function assertSignInStillWorks(b) {
  b.get('token').value = 'theme-contract-token';
  await b.get('login-form').dispatch('submit');
  const request = b.requests.find(item => item.url === '/api/v1/config');
  assert.ok(request, 'sign-in must still reach the management API');
  assert.equal(request.options.headers.Authorization, 'Bearer theme-contract-token');
  assert.equal(b.get('login-error').textContent, 'Enter a valid management token to sign in.');
}

(async() => {
  const absent = await browser();
  assert.deepEqual(absent.beforeBody, {preference:'system', theme:null, scheme:'light dark'});
  bind(absent);
  assertSynced(absent, 'system');
  assert.deepEqual(absent.localStorage.writes, [], 'initialization must not write preferences');

  const stored = await browser(storage({'rillway-theme':'dark'}));
  assert.deepEqual(stored.beforeBody, {preference:'dark', theme:'dark', scheme:'dark'},
    'a stored dark choice must apply before a body or controls exist');
  bind(stored);
  assertSynced(stored, 'dark');
  assert.deepEqual(stored.localStorage.writes, [], 'reading a stored preference must not rewrite it');

  for(const [id, preference] of [['login-theme','light'], ['sidebar-theme','dark'], ['login-theme','system']]) {
    stored.get(id).value = preference;
    await stored.get(id).dispatch('change');
    assertSynced(stored, preference);
    assert.equal(stored.localStorage.values.get('rillway-theme'), preference);
    const writes = stored.localStorage.writes.length;
    const reloaded = await browser(stored.localStorage);
    assert.equal(reloaded.beforeBody.preference, preference);
    bind(reloaded);
    assertSynced(reloaded, preference);
    assert.equal(stored.localStorage.writes.length, writes, 'reload must not rewrite the saved choice');
  }
  assert.deepEqual(stored.localStorage.writes, [
    ['rillway-theme','light'], ['rillway-theme','dark'], ['rillway-theme','system']
  ], 'each user change should persist exactly once');

  stored.context.RillwayTheme.bind();
  stored.context.RillwayTheme.bind();
  assert.equal(stored.get('sidebar-theme').listenerCount('change'), 1, 'theme binding itself must be idempotent');
  const writes = stored.localStorage.writes.length;
  stored.context.RillwayTheme.apply('dark');
  assertSynced(stored, 'dark');
  for(const invalid of [undefined, null, 'sepia', 'DARK', ' dark ', {}, 1]) {
    stored.context.RillwayTheme.apply(invalid);
    assertSynced(stored, 'system');
  }
  assert.equal(stored.localStorage.writes.length, writes, 'apply must never persist changes');

  for(const invalid of ['', 'sepia', 'DARK', ' dark ', 'null']) {
    const b = await browser(storage({'rillway-theme':invalid}));
    assert.deepEqual(b.beforeBody, {preference:'system', theme:null, scheme:'light dark'});
    bind(b);
    assertSynced(b, 'system');
    assert.deepEqual(b.localStorage.writes, [], 'invalid stored values must not be repaired by writing storage');
    await assertSignInStillWorks(b);
  }

  for(const options of [{getThrows:true}, {setThrows:true}, {getThrows:true, setThrows:true}]) {
    const b = await browser(storage({'rillway-theme':'dark'}, options));
    assert.equal(b.beforeBody.preference, options.getThrows ? 'system' : 'dark');
    bind(b);
    assertSynced(b, options.getThrows ? 'system' : 'dark');
    assert.deepEqual(b.localStorage.writes, [], 'blocked storage must not trigger initialization writes');
    b.get('sidebar-theme').value = 'light';
    await b.get('sidebar-theme').dispatch('change');
    assertSynced(b, 'light');
    assert.deepEqual(b.localStorage.writes, [['rillway-theme','light']]);
    assert.equal(b.localStorage.values.get('rillway-theme'), options.setThrows ? 'dark' : 'light');
    await assertSignInStillWorks(b);
  }
  console.log('browser theme contracts passed');
})().catch(error => { console.error(error); process.exitCode = 1; });
