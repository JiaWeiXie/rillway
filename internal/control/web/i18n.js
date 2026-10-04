'use strict';
// Catalog keys are complete English messages. User-provided values are never
// passed through the translator; callers interpolate them after lookup.
globalThis.RillwayI18n = (() => {
  let locale = 'en';
  try { if(localStorage.getItem('rillway-locale') === 'zh-Hant') locale = 'zh-Hant'; } catch (_) {}
  let catalog = {en:{},'zh-Hant':{}};
  const records = [];
  function t(source, values = {}) {
    const table = catalog[locale] || {};
    const message = Object.hasOwn(table,source) && typeof table[source] === 'string' ? table[source] : source;
    return message.replace(/\{(\w+)\}/g, (match,key) => Object.hasOwn(values,key) ? String(values[key]) : match);
  }
  function capture(root) {
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    while(walker.nextNode()) {
      const node = walker.currentNode;
      if(['SCRIPT','STYLE','TEXTAREA'].includes(node.parentElement?.tagName)) continue;
      const source = node.textContent.trim();
      if(source) records.push({node,source,prefix:node.textContent.match(/^\s*/)[0],suffix:node.textContent.match(/\s*$/)[0]});
    }
    for(const node of root.querySelectorAll('[aria-label],[placeholder],[title]')) {
      for(const attribute of ['aria-label','placeholder','title']) {
        const source = node.getAttribute(attribute);
        if(source) records.push({node,source,attribute});
      }
    }
  }
  function applyStatic() {
    document.documentElement.lang = locale;
    document.title = t('Rillway · Choose your route');
    for(const record of records) {
      if(!record.node.isConnected) continue;
      if(record.attribute) record.node.setAttribute(record.attribute,t(record.source));
      else record.node.textContent = record.prefix + t(record.source) + record.suffix;
    }
    for(const select of document.querySelectorAll('[data-locale]')) select.value = locale;
  }
  // Search static help in both languages, regardless of the display locale.
  // Only captured product copy is included; arbitrary user data is not translated.
  function searchText(root) {
    return records.filter(record => !record.attribute && root.contains(record.node))
      .map(record => record.source + ' ' + (catalog['zh-Hant'][record.source] || record.source)).join(' ');
  }
  function setLocale(value) {
    locale = value === 'zh-Hant' ? 'zh-Hant' : 'en';
    try { localStorage.setItem('rillway-locale',locale); } catch (_) {}
    applyStatic();
  }
  const ready = fetch('/locales.json',{credentials:'omit',cache:'no-store',redirect:'error'})
    .then(response => { if(!response.ok) throw new Error('Language catalog unavailable'); return response.json(); })
    .then(value => { catalog = value; applyStatic(); })
    .catch(() => { locale = 'en'; applyStatic(); });
  return {t,capture,setLocale,applyStatic,searchText,ready,get locale(){return locale;},date(value){return new Date(value).toLocaleString(locale === 'zh-Hant' ? 'zh-TW' : 'en-US');}};
})();
