'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const web = path.join(__dirname, '..', 'web');

// Read whole assets, in the same order as the shipped HTML. Never cut app.js
// around a source marker: the deferred bootstrap is part of every contract.
function loadScripts(context) {
  const markup = fs.readFileSync(path.join(web, 'index.html'), 'utf8');
  const sources = [...markup.matchAll(/<script\b[^>]*\bsrc="([^"]+)"[^>]*>/g)].map(match => match[1]);
  assert.deepEqual(sources, ['/theme.js', '/i18n.js', '/editors.js', '/app.js']);
  for (const source of sources) {
    vm.runInContext(fs.readFileSync(path.join(web, source.substring(1)), 'utf8'), context, {filename: source});
  }
  return context.RillwayI18n.ready;
}
module.exports = {loadScripts, web};
