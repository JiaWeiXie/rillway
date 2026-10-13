'use strict';
// Minimal DOM boundary for Node VM contracts. The production renderer owns all
// HTML, state and event logic; this fixture only models nodes and native events.
const decode = text => text.replace(/&(?:amp|lt|gt|quot|#39|#(\d+));/g, (entity, code) => code ? String.fromCodePoint(Number(code)) : ({'&amp;':'&','&lt;':'<','&gt;':'>','&quot;':'"','&#39;':"'"}[entity]));
const dataKey = name => name.substring(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase());
const voidTags = new Set(['AREA','BASE','BR','COL','EMBED','HR','IMG','INPUT','LINK','META','PARAM','SOURCE','TRACK','WBR']);

class Node {
  constructor(document, tagName = '') {
    this.ownerDocument = document;
    this.tagName = tagName.toUpperCase();
    this.children = [];
    this.parentElement = null;
    this.attributes = {};
    this.dataset = {};
    this.style = {};
    this.events = new Map();
    this.value = '';
    this.checked = this.disabled = this.hidden = this.open = this.indeterminate = false;
    this.readOnly = this.required = false;
    this._text = '';
    this.classList = {
      contains: name => this.className.split(/\s+/).includes(name),
      toggle: (name, force) => {
        const classes = new Set(this.className.split(/\s+/).filter(Boolean));
        const add = force ?? !classes.has(name);
        if (add) classes.add(name); else classes.delete(name);
        this.className = [...classes].join(' ');
        return add;
      },
      add: name => this.classList.toggle(name, true),
      remove: name => this.classList.toggle(name, false),
    };
  }
  get id() { return this.attributes.id || ''; }
  get className() { return this.attributes.class || ''; }
  set className(value) { this.attributes.class = String(value); }
  get checked() { return !!this._checked; }
  set checked(value) { this._checked = !!value; }
  get disabled() { return !!this._disabled; }
  set disabled(value) { this._disabled = !!value; if(this._disabled) this.attributes.disabled = ''; else delete this.attributes.disabled; }
  get hidden() { return !!this._hidden; }
  set hidden(value) { this._hidden = !!value; if(this._hidden) this.attributes.hidden = ''; else delete this.attributes.hidden; }
  get open() { return !!this._open; }
  set open(value) { this._open = !!value; if(this._open) this.attributes.open = ''; else delete this.attributes.open; }
  get readOnly() { return !!this._readOnly; }
  set readOnly(value) { this._readOnly = !!value; if(this._readOnly) this.attributes.readonly = ''; else delete this.attributes.readonly; }
  get required() { return !!this._required; }
  set required(value) { this._required = !!value; if(this._required) this.attributes.required = ''; else delete this.attributes.required; }
  get isConnected() { return this === this.ownerDocument.root || !!this.parentElement?.isConnected; }
  get isContentEditable() {
    const value = this.getAttribute('contenteditable');
    return value === null ? !!this.parentElement?.isContentEditable : value !== 'false';
  }
  get options() { return this.querySelectorAll('option'); }
  get selectedOptions() { return this.options.filter(option => option.value === this.value); }
  get textContent() { return this._text + this.children.map(child => child.textContent).join(''); }
  set textContent(value) {
    this.children.forEach(child => { child.parentElement = null; });
    this.children = [];
    this._text = String(value ?? '');
  }
  get innerHTML() { return this._html || ''; }
  set innerHTML(value) {
    this.textContent = '';
    this._html = String(value);
    parseInto(this, this._html);
    if (this.tagName === 'SELECT') this.value = (this.options.find(option => option.hasAttribute('selected')) || this.options[0])?.value || '';
  }
  setAttribute(name, value) {
    this.attributes[name] = String(value);
    if (name.startsWith('data-')) this.dataset[dataKey(name)] = String(value);
    if (name === 'value') this.value = String(value);
    if (['checked','disabled','hidden','open','required','readonly'].includes(name)) this[name === 'readonly' ? 'readOnly' : name] = true;
    if (name === 'type') this.type = String(value);
  }
  getAttribute(name) {
    if (name.startsWith('data-')) return this.dataset[dataKey(name)] ?? null;
    return this.attributes[name] ?? null;
  }
  hasAttribute(name) { return this.getAttribute(name) !== null; }
  removeAttribute(name) {
    delete this.attributes[name];
    if (name.startsWith('data-')) delete this.dataset[dataKey(name)];
    if (['disabled','hidden','open'].includes(name)) this[name] = false;
  }
  append(...nodes) {
    for (const node of nodes) {
      const child = typeof node === 'string' ? Object.assign(new Node(this.ownerDocument), {_text: node}) : node;
      child.remove();
      child.parentElement = this;
      this.children.push(child);
    }
  }
  appendChild(node) { this.append(node); return node; }
  replaceChildren(...nodes) { this.textContent = ''; this.append(...nodes); }
  remove() {
    if (this.parentElement) this.parentElement.children = this.parentElement.children.filter(child => child !== this);
    this.parentElement = null;
  }
  contains(node) { return node === this || this.children.some(child => child.contains(node)); }
  matches(selector) {
    return selector.split(',').some(part => {
      part = part.trim();
      let ok = true;
      part = part.replace(/:not\(([^)]+)\)/g, (_, negative) => { ok &&= !this.matches(negative); return ''; });
      part = part.replace(/:(checked|disabled|enabled|open)/g, (_, state) => { ok &&= state === 'enabled' ? !this.disabled : !!this[state]; return ''; });
      part = part.replace(/\[([^\]=\s]+)(?:=["']?([^\]"']*)["']?)?\]/g, (_, name, value) => {
        ok &&= value === undefined ? this.hasAttribute(name) : this.getAttribute(name) === value;
        return '';
      });
      part = part.replace(/#([\w-]+)/g, (_, id) => { ok &&= this.id === id; return ''; });
      part = part.replace(/\.([\w-]+)/g, (_, name) => { ok &&= this.classList.contains(name); return ''; });
      return ok && (!part || part === '*' || this.tagName === part.toUpperCase());
    });
  }
  closest(selector) { return this.matches(selector) ? this : this.parentElement?.closest(selector) || null; }
  querySelectorAll(selector) {
    if (selector.includes(',')) {
      const nodes = selector.split(',').flatMap(part => this.querySelectorAll(part.trim()));
      return [...new Set(nodes)];
    }
    const result = [];
    const parts = selector.trim().split(/\s+(?=(?:[^"']|"[^"]*"|'[^']*')*$)/);
    const match = node => {
      if (!node.matches(parts[parts.length - 1])) return false;
      let ancestor = node.parentElement;
      for (let i = parts.length - 2; i >= 0; i--) {
        while (ancestor && !ancestor.matches(parts[i])) ancestor = ancestor.parentElement;
        if (!ancestor) return false;
        ancestor = ancestor.parentElement;
      }
      return true;
    };
    const walk = node => { for (const child of node.children) { if (child.tagName && match(child)) result.push(child); walk(child); } };
    walk(this);
    return result;
  }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
  addEventListener(type, callback) {
    if (!this.events.has(type)) this.events.set(type, []);
    this.events.get(type).push(callback);
  }
  reset() {
    for (const node of this.querySelectorAll('input,select,textarea')) {
      node.checked = node.hasAttribute('checked');
      node.value = node.tagName === 'SELECT'
        ? (node.options.find(option => option.hasAttribute('selected')) || node.options[0])?.value || ''
        : node.tagName === 'TEXTAREA' ? node.textContent : node.getAttribute('value') || '';
    }
  }
  focus() { this.ownerDocument.activeElement = this; }
  showModal() { this.open = true; this.setAttribute('open', ''); this.querySelector('input,select,textarea,button')?.focus(); }
  close() { if (!this.open) return; this.open = false; this.removeAttribute('open'); this.dispatch('close', {bubbles: false}); }
  async dispatch(type, properties = {}) {
    const event = {type, target: this, currentTarget: this, defaultPrevented: false, bubbles: true,
      preventDefault() { this.defaultPrevented = true; }, stopPropagation() { this.bubbles = false; }, ...properties};
    const pending = [];
    for (let node = this; node; node = event.bubbles ? node.parentElement : null) {
      event.currentTarget = node;
      for (const callback of node.events.get(type) || []) pending.push(callback(event));
    }
    await Promise.all(pending);
    if (type === 'cancel' && !event.defaultPrevented && this.tagName === 'DIALOG') this.close();
    return event;
  }
  async click() { if (this.disabled) return; await this.dispatch('click'); await new Promise(setImmediate); }
}

function parseInto(root, markup) {
  const stack = [root];
  for (const match of markup.matchAll(/<!--[\s\S]*?-->|<![^>]*>|<\/?[a-zA-Z][^>]*>|[^<]+|</g)) {
    const token = match[0];
    if (token.startsWith('<!')) continue;
    if (token.startsWith('</')) {
      const tag = token.match(/^<\/([\w-]+)/)[1].toUpperCase();
      const index = stack.findLastIndex(node => node.tagName === tag);
      if (index > 0) stack.length = index;
    } else if (/^<[a-zA-Z]/.test(token)) {
      const tag = token.match(/^<([\w-]+)/)[1];
      const node = new Node(root.ownerDocument, tag);
      for (const attr of token.substring(tag.length + 1, token.length - 1).matchAll(/([^\s=/>]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?/g)) node.setAttribute(attr[1], decode(attr[2] ?? attr[3] ?? attr[4] ?? ''));
      stack[stack.length - 1].append(node);
      if (!voidTags.has(node.tagName) && !token.endsWith('/>')) stack.push(node);
    } else {
      const text = new Node(root.ownerDocument);
      text._text = decode(token);
      stack[stack.length - 1].append(text);
    }
  }
  for (const select of root.querySelectorAll('select')) select.value = (select.options.find(option => option.hasAttribute('selected')) || select.options[0])?.value || '';
  for (const textarea of root.querySelectorAll('textarea')) textarea.value = textarea.textContent;
}

function createDocument(markup) {
  const document = {readyState: 'loading', hidden: false, activeElement: null, events: new Map()};
  document.root = new Node(document, 'document');
  // Native events bubble to Document listeners, including keyboard shortcuts.
  document.root.events = document.events;
  parseInto(document.root, markup);
  document.documentElement = document.root.querySelector('html');
  document.body = document.root.querySelector('body');
  document.getElementById = id => document.root.querySelectorAll('[id]').find(node => node.id === id) || null;
  document.querySelector = selector => document.root.querySelector(selector);
  document.querySelectorAll = selector => document.root.querySelectorAll(selector);
  document.createElement = tag => new Node(document, tag);
  document.createTreeWalker = root => {
    const texts = [];
    const visit = node => { if (!node.tagName) texts.push(node); node.children.forEach(visit); };
    visit(root);
    let index = 0;
    return {nextNode() { this.currentNode = texts[index++]; return !!this.currentNode; }};
  };
  document.addEventListener = (type, callback) => {
    if (!document.events.has(type)) document.events.set(type, []);
    document.events.get(type).push(callback);
  };
  return document;
}
module.exports = {createDocument};
