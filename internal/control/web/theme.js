globalThis.RillwayTheme = (() => {
  'use strict';

  let preference = 'system';
  const boundControls = new WeakSet();

  function getPreference() {
    return preference;
  }

  function apply(value) {
    preference = value === 'light' || value === 'dark' ? value : 'system';
    const root = document.documentElement;
    if(preference === 'system') root.removeAttribute('data-theme');
    else root.setAttribute('data-theme',preference);

    const meta = document.querySelector('meta[name="color-scheme"]');
    if(meta) meta.setAttribute('content',preference === 'system' ? 'light dark' : preference);
    for(const select of document.querySelectorAll('select[data-theme]')) select.value = preference;
  }

  function setPreference(value) {
    apply(value);
    try { localStorage.setItem('rillway-theme',preference); } catch (_) {}
  }

  function bind() {
    for(const select of document.querySelectorAll('select[data-theme]')) {
      if(boundControls.has(select)) continue;
      select.addEventListener('change',() => setPreference(select.value));
      boundControls.add(select);
    }
    apply(preference);
  }

  let stored = 'system';
  try { stored = localStorage.getItem('rillway-theme'); } catch (_) {}
  apply(stored);

  return {getPreference,apply,setPreference,bind};
})();
