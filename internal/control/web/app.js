'use strict';
const $ = id => document.getElementById(id);
const i18n = globalThis.RillwayI18n;
const t = (source,values) => i18n.t(source,values);
const et = (source,values) => esc(t(source,values));
i18n.capture(document.body);
const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const lines = value => value.split(/[\n,]/).map(s => s.trim()).filter(Boolean);
const bytes = n => { n = Number(n) || 0; const units = ['B','KiB','MiB','GiB','TiB']; let i = 0; while(n >= 1024 && i < units.length-1){ n /= 1024; i++; } return `${n.toFixed(i ? 1 : 0)} ${units[i]}`; };
const rate = n => `${bytes(n)}/s`;
const typeName = value => { const source = ({direct:'Direct',warp:'Cloudflare WARP',wireguard:'WireGuard',tailscale:'Tailscale',tsnet:'Tailscale',socks5:'SOCKS5',http:'HTTP Proxy'}[value]); return source ? t(source) : value; };
const stateKey = t => String(t || '').toLowerCase();
const stateName = value => { const source = ({ready:'Available',connected:'Connected',running:'Running',starting:'Starting',stopped:'Stopped',disconnected:'Disconnected',disabled:'Disabled',error:'Error',unavailable:'Unavailable',needslogin:'Sign-in required',needsmachineauth:'Awaiting admin approval',needs_login:'Sign-in required',needs_auth:'Sign-in required',unsupported_account:'Account unsupported',unsupported_client:'Version unsupported'}[stateKey(value)]); return source ? t(source) : value || t('Status pending'); };
// Protocol names are shown as standard uppercase acronyms; unknown values stay verbatim.
const networkNames = {tcp:'TCP',udp:'UDP'};
const networkName = value => typeof value !== 'string' ? '' : Object.hasOwn(networkNames,value.toLowerCase()) ? networkNames[value.toLowerCase()] : value;
const udpFailureReasons = {
  unsupported:['UDP not supported','This outbound cannot carry UDP, for example WARP Local Proxy. The traffic does not fall back to direct.'],
  unavailable:['Outbound unavailable','The outbound or outbound group is disabled, missing, failed or was replaced, or the group has no usable member.'],
  blocked:['Refused by protection','Private destinations cannot use this public outbound, or the outbound connected back to Rillway.'],
  timeout:['Timed out','The outbound did not open the UDP path in time.'],
  network:['Network error','Another network error occurred while opening the UDP path.']
};
const counter = value => { const n = Number(value); return Number.isFinite(n) && n >= 0 ? String(n) : '0'; };
const pacPresetNotes = {
  'domain.localhost':'This device. Covers localhost and names ending in .localhost.',
  'domain.mdns':'Bonjour and mDNS devices on the current local network.',
  'domain.home':'The standard name space for services on a home network.',
  'domain.tailscale-dns':'Tailscale MagicDNS names. Keeps company and tailnet names on this device.',
  'domain.tailscale-control':'Tailscale sign-in and account pages stay outside the browser Proxy.',
  'domain.docker-host':'Docker Desktop name for reaching services on the host.',
  'domain.docker-gateway':'Docker Desktop name for the Docker VM gateway.',
  'domain.docker-vm':'Docker Desktop name for services in its Linux VM.',
  'domain.kubernetes':'Common Kubernetes cluster suffix. Your cluster may use a different suffix.',
  'cidr.loopback-v4':'IPv4 loopback addresses on this device.',
  'cidr.private-10':'Private IPv4 networks commonly used by companies, VPNs and containers.',
  'cidr.private-172':'Private IPv4 networks, including many Docker bridge networks.',
  'cidr.private-192':'Private IPv4 networks commonly used by home and office LANs.',
  'cidr.link-local-v4':'IPv4 link-local devices that work only on the current network link.',
  'cidr.tailscale':'Tailscale IPv4 addresses and the MagicDNS resolver.',
  'cidr.tailscale-v6':'Tailscale IPv6 addresses. Keep this enabled when another VPN is active.',
  'cidr.loopback-v6':'IPv6 loopback address on this device.',
  'cidr.ula-v6':'Private IPv6 unique local addresses.',
  'cidr.link-local-v6':'IPv6 link-local devices on the current network link.'
};
let memoryStatus = null, memoryDraftRevision = "", memoryDirty = false, memorySaving = false, memoryLoading = false, memoryInputMode = "MiB";
let warpLogStatus = null, warpLogDraftRevision = "", warpLogDirty = false, warpLogSaving = false, warpLogLoading = false, warpLogLoadFailed = false, warpLogGeneration = 0;
let capacityDirty = false, capacityDraftRevision = null, capacitySaving = false;
let dockerBundle = null, deletingOutbound = null, formDefaults = null, outboundType = null, outboundDrafts = {};
let outboundGeneration = 0;
const pendingOutboundActions = new Set();
let token = ''; try { token = sessionStorage.getItem('rillway-token') || ''; } catch (_) {}
let currentPage = 'overview', latestSnapshot = {}, serverInfo = null, lastNotice = null, isOnline = false;
let cfg, statuses = [], flows = [], active = false, polling = false, statusPolling = false, outboundIndex = -1, licenseID = '';
let sessionGeneration = 0, ruleDraft = null, ruleOpening = false;
let sourceClients = [], sourceCapacity = null, sourcePollTimer = null, sourcePollPromise = null, sourceRuleDraft = null, editingSourceRuleID = '', blockSourceContext = null, selectedSourceAddress = '';
let sourceSnapshotGeneratedAt = null;
const refreshChoices = [1,2,5,10,30];
let flowPollTimer, refreshSeconds = 1, openFlowGroups = new Set();
try { const saved = Number(localStorage.getItem('rillway-flow-refresh-seconds')); if(refreshChoices.includes(saved)) refreshSeconds = saved; } catch (_) {}
const outboundRefreshChoices = [0,10,30,60,300];
let outboundRefreshSeconds = 30, outboundPollTimer = null, outboundLastLoadedAt = 0;
try { const saved = localStorage.getItem('rillway-outbound-refresh-seconds'); if(['0','10','30','60','300'].includes(saved)) outboundRefreshSeconds = Number(saved); } catch (_) {}
async function api(path, options = {}) {
  const generation = sessionGeneration;
  const finish = path === '/config' && (!options.method || options.method === 'GET') ? beginLoading(['rules','group-list','rule-set-list']) : null;
  let success = false;
  try {
  const headers = {'Authorization': `Bearer ${token}`, 'Accept-Language': i18n.locale, ...options.headers};
  if(options.body !== undefined) headers['Content-Type'] = 'application/json';
  let response;
  try { response = await fetch(`/api/v1${path}`, {...options, headers, credentials:'omit', cache:'no-store', redirect:'error'}); }
  catch (_) { if(generation !== sessionGeneration) { const error = new Error(); error.stale = true; throw error; } throw sourceError('Could not reach the management service. Try again.'); }
  let data; try { data = await response.json(); } catch (_) { data = {}; }
  if(generation !== sessionGeneration) { const error = new Error(); error.stale = true; throw error; }
  if(!response.ok) {
    const error = new Error(data.error || t('Service returned {status}',{status:response.status})); error.status = response.status;
    if(typeof data.error_source === 'string') error.source = data.error_source;
    if(response.status === 401 && active) logout();
    throw error;
  }
  success = true; return data;
  } finally { finish?.(success); }
}
function sourceError(source) { const error = new Error(t(source)); error.source = source; return error; }
function errorText(error) { return error.source ? t(error.source,error.values) : error.message; }
let noticeTimer = null;
function notice(message, failure = false) {
  if(message?.stale) return;
  clearTimeout(noticeTimer); noticeTimer = null;
  lastNotice = {message,failure};
  $('notice').textContent = typeof message === 'string' ? t(message) : errorText(message);
  $('notice').className = `notice${failure ? ' failure' : ''}`; $('notice').hidden = false;
  if(!failure) noticeTimer = setTimeout(() => { $('notice').hidden = true; lastNotice = null; noticeTimer = null; },6000);
}
function displayError(node,error) {
  if(error.stale) return;
  if(error.source) node.dataset.errorSource = error.source;
  else delete node.dataset.errorSource;
  node.textContent = errorText(error);
}
function errorIn(form,error) { displayError(form.querySelector('.form-error'),error); }
function clearError(form) { const error = form.querySelector('.form-error'); if(error) { error.textContent = ''; delete error.dataset.errorSource; } }
function connection(online) { isOnline = online; $('connection-dot').className = `dot ${online ? 'online' : 'offline'}`; $('connection-label').textContent = t(online ? 'Service connected' : 'Service unavailable'); }
function cloneConfig() { return structuredClone(cfg); }
const selectedRules = new Set(), selectedSourceRules = new Set();
let bulkContext = null, ruleMutationPending = false, rulesNeedReload = false, sourceRulesNeedReload = false;
const loadedCollections = new Set(), loadingCounts = new Map();
function beginLoading(ids) {
  for(const id of ids) {
    loadingCounts.set(id,(loadingCounts.get(id) || 0)+1);
    $(id)?.setAttribute('aria-busy','true');
    if($(id+'-loading')) $(id+'-loading').hidden = loadedCollections.has(id);
  }
  return success => {
    for(const id of ids) {
      if(success) loadedCollections.add(id);
      const count = (loadingCounts.get(id) || 1)-1; loadingCounts.set(id,count);
      if(count === 0) { $(id)?.removeAttribute('aria-busy'); if($(id+'-loading')) $(id+'-loading').hidden = true; }
    }
  };
}
function searchValue(id) { return ($(id)?.value || '').trim().toLowerCase(); }
function renderNoMatch(id,total,visible,searchID) {
  const node = $(id); if(!node) return;
  node.hidden = !total || visible > 0;
  node.querySelector('p').textContent = t('No matches for {query}.',{query:$(searchID).value});
}
function visibleRules() {
  const query = searchValue('rules-search');
  return (cfg?.rules || []).filter(rule => [rule.id,rule.outbound,...['domains','suffixes','cidrs','sources','inbounds','ports','sets'].flatMap(key => rule[key] || [])].join(' ').toLowerCase().includes(query));
}
function renderSelection(kind,visibleIDs) {
  const selected = kind === 'rules' ? selectedRules : selectedSourceRules;
  const collection = kind === 'rules' ? cfg?.rules : cfg?.source_access?.rules;
  for(const id of selected) if(!(collection || []).some(item => item.id === id)) selected.delete(id);
  const all = $(kind+'-select-all'), count = visibleIDs.filter(id => selected.has(id)).length;
  if(all) { all.checked = visibleIDs.length > 0 && count === visibleIDs.length; all.indeterminate = count > 0 && count < visibleIDs.length; all.disabled = visibleIDs.length === 0; }
  const toolbar = $(kind+'-bulk-toolbar'); if(toolbar) toolbar.hidden = selected.size === 0;
  if($(kind+'-selected-count')) $(kind+'-selected-count').textContent = t('{count} selected',{count:selected.size});
  for(const action of ['delete','enable','disable']) { const button = $(kind+'-bulk-'+action); if(button) button.disabled = selected.size === 0 || (kind === 'rules' ? rulesNeedReload || ruleMutationPending : sourceRulesNeedReload); }
}
function clearSelections() { selectedRules.clear(); selectedSourceRules.clear(); }
function renderBulkContext() {
  if(!bulkContext) return;
  const {action,ids} = bulkContext;
  const title = {delete:'Delete {count} items?',enable:'Enable {count} items?',disable:'Disable {count} items?'}[action];
  $('bulk-delete-title').textContent = t(title,{count:ids.length});
  $('bulk-delete-items').innerHTML = ids.map(id => '<li>'+esc(id)+'</li>').join('');
  $('bulk-delete-confirm').textContent = t({delete:'Delete selected',enable:'Enable selected',disable:'Disable selected'}[action]);
  $('bulk-delete-confirm').disabled = !!bulkContext.conflict;
}
function openBulkAction(kind,action,trigger = document.activeElement,ids = null) {
  if(bulkContext || ruleMutationPending || (kind === 'rules' ? rulesNeedReload : sourceRulesNeedReload)) return;
  const visible = kind === 'rules' ? visibleRules().map(rule => rule.id) : (cfg.source_access.rules || []).map(rule => rule.id);
  const selected = kind === 'rules' ? selectedRules : selectedSourceRules;
  const chosen = ids || visible.filter(id => selected.has(id)); if(!chosen.length) return;
  bulkContext = {kind,action,ids:[...chosen],config:cloneConfig(),generation:sessionGeneration,conflict:false};
  clearError($('bulk-delete-form')); $('bulk-reload').hidden = true; renderBulkContext();
  openModal($('bulk-delete-dialog'),trigger,$(kind === 'rules' ? 'add-rule' : 'add-source-rule'));
}
async function reloadCollections() {
  const generation = sessionGeneration, next = await api('/config');
  if(generation !== sessionGeneration) return;
  clearSelections(); rulesNeedReload = false; sourceRulesNeedReload = false;
  $('rules-reload').hidden = true; $('source-rules-reload').hidden = true;
  publishConfig(next);
}
function restoreRuleFocus(id,attribute) {
  const row = [...$('rules').querySelectorAll('[data-rule-id]')].find(node => node.dataset.ruleId === id);
  const requested = row?.querySelector('['+attribute+']');
  const target = requested && !requested.disabled ? requested : row?.querySelector('[data-edit-rule]');
  target?.focus({preventScroll:true});
}
async function reorderRule(id,destination,attribute = 'data-up-rule') {
  if(ruleMutationPending || rulesNeedReload || searchValue('rules-search')) return;
  const next = cloneConfig(), index = next.rules.findIndex(rule => rule.id === id);
  if(index < 0 || destination < 0 || destination >= next.rules.length || index === destination) return;
  next.rules.splice(destination,0,next.rules.splice(index,1)[0]);
  const generation = sessionGeneration; ruleMutationPending = true; renderRules();
  try { await save(next); }
  catch(error) { if(generation === sessionGeneration && !error.stale) { notice(error,true); if(error.status === 409) { rulesNeedReload = true; $('rules-reload').hidden = false; } } }
  finally { if(generation === sessionGeneration) { ruleMutationPending = false; renderRules(); restoreRuleFocus(id,attribute); } }
}
let busyButtons = new Map();
function resetBusyButtons() {
  for(const [button,state] of busyButtons) { button.disabled = state.disabled; button.textContent = state.text; button.removeAttribute('aria-busy'); }
  busyButtons = new Map();
}
async function withBusy(button,action) {
  const locks = busyButtons;
  if(locks.has(button)) return;
  const state = {disabled:button.disabled,text:button.textContent};
  locks.set(button,state);
  button.disabled = true; button.setAttribute('aria-busy','true');
  try { return await action(); }
  finally { if(locks === busyButtons) { locks.delete(button); button.disabled = state.disabled; button.textContent = state.text; button.removeAttribute('aria-busy'); } }
}
const modalOpeners = new WeakMap();
function openModal(dialog,trigger = document.activeElement,fallback = null) {
  if(dialog.open) return;
  const identity = trigger ? {id:trigger.id,data:Object.entries(trigger.dataset || {})} : null;
  modalOpeners.set(dialog,{trigger,identity,fallback});
  if(!dialog.dataset.focusBound) {
    dialog.dataset.focusBound = 'true';
    dialog.addEventListener('close',() => {
      const record = modalOpeners.get(dialog); if(!record || !active) return;
      if(dialog.open) return;
      const usable = element => element?.isConnected && !element.disabled && !element.closest('[hidden]');
      let target = record.trigger;
      if(!usable(target)) {
        target = record.identity?.id ? $(record.identity.id) : null;
        if(!usable(target) && record.identity?.data.length) target = [...document.querySelectorAll('button')].find(button => record.identity.data.every(([key,value]) => button.dataset[key] === value));
      }
      if(!usable(target)) target = record.fallback;
      target?.focus({preventScroll:true}); modalOpeners.delete(dialog);
    });
  }
  dialog.showModal();
  const deletion = dialog.id.includes('delete') || dialog.id === 'block-source-dialog';
  const target = deletion ? dialog.querySelector('[data-close].quiet') : dialog.querySelector('input:not(:disabled),select:not(:disabled),textarea:not(:disabled)');
  target?.focus();
}
const editors = RillwayEditors.create({api,save,publishConfig,getConfig:() => cfg,getSession:() => ({active,generation:sessionGeneration}),getObservedRevision:() => Math.max(cfg?.revision ?? 0,latestSnapshot.config_revision ?? 0),t,esc,errorText,notice,withBusy,openModal});
function publishConfig(next) { cfg = next; clearSelections(); editors.configChanged(); renderConfig(); }

function candidates() { return (cfg?.outbounds || []).filter(o => o.enabled && o.public_internet && ['direct','warp','wireguard'].includes(o.type)); }
function options(selected,withAdaptive = false) {
  const targets = [...(cfg.outbounds || []),...(cfg.outbound_groups || [])];
  return (withAdaptive ? `<option value="@adaptive"${selected === '@adaptive' ? ' selected' : ''}>${et('Adaptive routing')}</option>` : '') + targets.map(o => `<option value="${esc(o.id)}"${o.id === selected ? ' selected' : ''}>${esc(o.id)} · ${esc(({select:t('Select'),fallback:t('Fallback'),'load-balance':t('Load balance')})[o.type] || typeName(o.type))}${o.enabled ? '' : et(' (disabled)')}</option>`).join('');
}
function candidateBoxes(container, selected, prefix) { container.innerHTML = candidates().map(o => `<label><input type="checkbox" name="${prefix}" value="${esc(o.id)}"${selected.includes(o.id) ? ' checked' : ''}>${esc(o.id)}</label>`).join('') || `<p class="hint">${et('Enable an outbound with Internet access first.')}</p>`; }
function checked(container) { return [...container.querySelectorAll('input:checked')].map(i => i.value); }

async function loadInfo() {
  try { return await api('/info'); }
  catch(error) { if(error.status === 404) return null; throw error; }
}
async function load() {
  const generation = sessionGeneration;
  const next = await api('/config');
  const info = await loadInfo();
  if(generation !== sessionGeneration) return;
  serverInfo = info;
  active = true; $('login').hidden = true; $('app').hidden = false;
  clearSelections(); rulesNeedReload = false; sourceRulesNeedReload = false; $('rules-reload').hidden = true; $('source-rules-reload').hidden = true;
  publishConfig(next); await poll();
  if(generation !== sessionGeneration) return;
  pollStatuses(); loadDocker(); loadMemory(); loadWarpLog(); navigate(currentPage);
}
async function save(next) {
  const generation = sessionGeneration;
  const updated = await api('/config',{method:'PUT',body:JSON.stringify(next)});
  if(generation !== sessionGeneration) { const error = new Error(); error.stale = true; throw error; }
  publishConfig(updated); notice('Configuration saved. New connections will use the updated rules.'); await poll();
  if(generation !== sessionGeneration) { const error = new Error(); error.stale = true; throw error; }
}

function renderConfig() {
  renderConfigText();
  $('adaptive-toggle').checked = !!cfg.adaptive.enabled;
  candidateBoxes($('adaptive-candidates'), cfg.adaptive.candidates || [], 'global-candidate');
  $('default-outbound').innerHTML = options(cfg.default_outbound);
  $('pac-address').value = cfg.pac.proxy_address || '';
  renderPACList('domains',cfg.pac.bypass_domains || []);
  renderPACList('cidrs',cfg.pac.bypass_cidrs || []);
  $('config-json').value = JSON.stringify(cfg, null, 2);
  renderRules(); renderOutbounds(); renderSourceAccess(); editors.render();
}
function normalizePACEntry(entry) {
  return typeof entry === 'string' ? {value:entry,enabled:true,note:'',preset:''} : {value:'',enabled:true,note:'',preset:'',...entry};
}
function renderPACList(kind, entries) {
  const container = $(`pac-${kind}`), valueLabel = kind === 'domains' ? 'Domain or suffix' : 'IP address range (CIDR)';
  container.innerHTML = entries.map((raw,index) => {
    const entry = normalizePACEntry(raw), presetSource = pacPresetNotes[entry.preset] || '';
    return `<article class="pac-entry${entry.enabled ? '' : ' disabled'}" data-pac-index="${index}" data-preset="${esc(entry.preset)}" data-preset-value="${esc(entry.value)}"><div class="pac-entry-top"><label class="toggle-control"><input type="checkbox" role="switch" aria-label="${et('Bypass {destination}',{destination:entry.value})}" data-pac-enabled${entry.enabled ? ' checked' : ''}><span class="toggle-track" aria-hidden="true"></span><span class="pac-toggle-state">${et(entry.enabled ? 'Enabled' : 'Disabled')}</span></label><button type="button" class="pac-remove" data-remove-pac aria-label="${et('Remove bypass entry')}">${et('Remove')}</button></div><div class="pac-entry-fields"><label><span>${et(valueLabel)}</span><input data-pac-value value="${esc(entry.value)}" required spellcheck="false" placeholder="${esc(kind === 'domains' ? 'company.example' : '10.0.0.0/8')}"></label><label><span>${et('Note (optional)')}</span><input data-pac-note value="${esc(entry.note)}" maxlength="500" placeholder="${et('Add a note for people managing this setting')}"></label></div>${presetSource ? `<p class="pac-preset-note" data-preset-note="${esc(entry.preset)}"><strong>${et('Built-in note:')}</strong> <span>${et(presetSource)}</span></p>` : ''}</article>`;
  }).join('') || `<div class="pac-list-empty">${et(kind === 'domains' ? 'No domain bypasses. Add one if a local service must stay off the Proxy.' : 'No IP range bypasses. Add one if a private network must stay off the Proxy.')}</div>`;
}
function serializePACList(kind) {
  return [...$(`pac-${kind}`).querySelectorAll('.pac-entry')].map(row => {
    const value = row.querySelector('[data-pac-value]').value.trim();
    const preset = value.toLowerCase() === row.dataset.presetValue.toLowerCase() ? row.dataset.preset : '';
    return {value,enabled:row.querySelector('[data-pac-enabled]').checked,note:row.querySelector('[data-pac-note]').value.trim(),preset};
  }).filter(entry => entry.value);
}
function addPACEntry(kind) {
  const current = serializePACList(kind), value = kind === 'domains' ? 'company.example' : '10.0.0.0/8';
  current.push({value,enabled:true,note:'',preset:''});
  renderPACList(kind,current);
  const input = $(`pac-${kind}`).querySelector('.pac-entry:last-child [data-pac-value]');
  input.focus(); input.select();
}
function updatePACSwitch(input) {
  const row = input.closest('.pac-entry');
  row.classList.toggle('disabled',!input.checked);
  row.querySelector('.pac-toggle-state').textContent = t(input.checked ? 'Enabled' : 'Disabled');
}
function translatePACRows() {
  for(const container of [$('pac-domains'),$('pac-cidrs')]) {
    const empty = container.querySelector('.pac-list-empty');
    if(empty) empty.textContent = t(container.id === 'pac-domains' ? 'No domain bypasses. Add one if a local service must stay off the Proxy.' : 'No IP range bypasses. Add one if a private network must stay off the Proxy.');
  }
  for(const row of document.querySelectorAll('.pac-entry')) {
    const input = row.querySelector('[data-pac-enabled]');
    input.setAttribute('aria-label',t('Bypass {destination}',{destination:row.querySelector('[data-pac-value]').value}));
    row.querySelector('.pac-toggle-state').textContent = t(input.checked ? 'Enabled' : 'Disabled');
    const note = row.querySelector('[data-preset-note]');
    if(note) {
      note.querySelector('strong').textContent = t('Built-in note:');
      note.querySelector('span').textContent = t(pacPresetNotes[note.dataset.presetNote] || '');
    }
    const labels = row.querySelectorAll('.pac-entry-fields label>span');
    if(labels.length === 2) {
      labels[0].textContent = t(row.closest('#pac-domains') ? 'Domain or suffix' : 'IP address range (CIDR)');
      labels[1].textContent = t('Note (optional)');
    }
    const remove = row.querySelector('[data-remove-pac]');
    remove.textContent = t('Remove'); remove.setAttribute('aria-label',t('Remove bypass entry'));
    row.querySelector('[data-pac-note]').placeholder = t('Add a note for people managing this setting');
  }
}
async function poll() {
  if(!active || polling) return;
  const generation = sessionGeneration;
  polling = true;
  const finish = beginLoading(['flow-groups']); let success = false;
  try {
    const snapshot = await api('/stats');
    const info = await loadInfo();
    if(generation !== sessionGeneration || !active) return;
    serverInfo = info;
    const previousRevision = Math.max(cfg?.revision ?? 0,latestSnapshot.config_revision ?? 0);
    latestSnapshot = {...snapshot,config_revision:Math.max(latestSnapshot.config_revision ?? 0,snapshot.config_revision ?? 0)};
    if(Math.max(cfg?.revision ?? 0,latestSnapshot.config_revision ?? 0) > previousRevision) editors.configChanged();
    flows = Array.isArray(snapshot) ? snapshot : snapshot.flows || [];
    renderRevision();
    renderFlows(); renderUDPRelay(); connection(true); success = true;
  } catch (e) { if(generation !== sessionGeneration || e.stale) return; connection(false); if(e.status !== 401) notice(e, true); }
  finally { finish(success); polling = false; }
}
async function pollStatuses(force = false) {
  if(!active || statusPolling) return;
  statusPolling = true;
  const finish = beginLoading(['outbound-list']); let success = false;
  const generation = sessionGeneration;
  const refreshBtn = $('refresh-outbounds');
  stopOutboundPolling();
  if(refreshBtn) { refreshBtn.disabled = true; if(force) refreshBtn.textContent = t('Refreshing outbounds…'); }
  try {
    const state = await api(force ? '/outbounds?refresh=true' : '/outbounds');
    if(generation !== sessionGeneration) return;
    success = true;
    statuses = Array.isArray(state) ? state : state.outbounds || [];
    outboundLastLoadedAt = Date.now();
    renderOutbounds();
    if(force) notice('Outbound statuses refreshed.');
  }
  catch(e) {
    if(generation !== sessionGeneration || e.stale) return;
    if(e.status !== 401) {
      if(force) notice('Could not refresh outbound statuses. Try again.', true);
      else notice(e,true);
    }
  }
  finally {
    finish(success);
    statusPolling = false;
    if(refreshBtn) { refreshBtn.disabled = false; refreshBtn.textContent = t('Refresh now'); }
    scheduleOutboundPolling();
  }
}
function stopOutboundPolling() {
  if(outboundPollTimer) { clearTimeout(outboundPollTimer); outboundPollTimer = null; }
}
function scheduleOutboundPolling(delay = null) {
  stopOutboundPolling();
  if(!active || currentPage !== 'outbounds' || document.hidden || statusPolling || outboundRefreshSeconds === 0) return;
  const ms = delay !== null ? delay : outboundRefreshSeconds * 1000;
  outboundPollTimer = setTimeout(() => {
    if(active && currentPage === 'outbounds' && !document.hidden && outboundRefreshSeconds !== 0) {
      pollStatuses(false);
    }
  }, ms);
}
function flowActive(f) { return !f.closed || f.closed === '0001-01-01T00:00:00Z'; }
function ipLiteral(value) {
  value = String(value || '').replace(/^\[|\]$/g,'');
  return value.includes(':') || /^(?:\d{1,3}\.){3}\d{1,3}$/.test(value);
}
function destinationAddress(f) {
  const host = f.host || f.domain;
  if(host && !ipLiteral(host)) return host;
  const ip = f.ip || host || t('Unknown');
  if(!f.port) return ip;
  return `${ip.includes(':') ? `[${ip}]` : ip}:${f.port}`;
}
function groupFlows(items) {
  const groups = new Map();
  items.forEach(({f,index}) => {
    const host = f.host || f.domain, destination = destinationAddress(f), key = `${host && !ipLiteral(host) ? 'domain' : 'address'}:${destination}`;
    if(!groups.has(key)) groups.set(key,{key,destination,connections:[],active:0,uploadRate:0,downloadRate:0,transferred:0});
    const group = groups.get(key);
    group.connections.push({f,index});
    group.uploadRate += Number(f.upload_bytes_per_second || f.upload_rate) || 0;
    group.downloadRate += Number(f.download_bytes_per_second || f.download_rate) || 0;
    group.transferred += (Number(f.upload_bytes)||0)+(Number(f.download_bytes)||0);
    if(flowActive(f)) group.active++;
  });
  return [...groups.values()].sort((a,b) => b.active-a.active || (b.downloadRate+b.uploadRate)-(a.downloadRate+a.uploadRate) || a.destination.localeCompare(b.destination));
}
function groupRouteSummary(group) {
  const connections = group.connections || [];
  const representative = connections.find(({f}) => flowActive(f)) || connections[0] || {f:{},index:-1};
  const outbounds = new Set(), routes = new Set();
  for(const {f} of connections) {
    const outbound = f.outbound || '';
    const rule = f.rule || f.rule_id || '';
    outbounds.add(outbound);
    routes.add(`${outbound}\u0000${rule}`);
  }
  return {
    index:representative.index,
    flow:representative.f,
    outbound:outbounds.size === 1 ? representative.f.outbound || '' : '',
    rule:routes.size === 1 ? representative.f.rule || representative.f.rule_id || '' : '',
    outboundCount:outbounds.size,
    routeCount:routes.size
  };
}
function flowRow({f,index}) {
  const host = destinationAddress(f);
  const ip = f.ip ? `${f.ip}${f.family ? ` · ${f.family}` : ''}` : t('Destination IP unknown; resolved upstream');
  const sum = (Number(f.upload_bytes)||0)+(Number(f.download_bytes)||0);
  const network = networkName(f.network);
  return `<tr><td><span class="domain">${esc(host)}</span>${network ? ` <span class="badge flow-network" data-network>${esc(network)}</span>` : ''}<span class="sub">${esc(ip)}</span></td><td><span class="badge">${esc(f.outbound || t('Unspecified'))}</span><span class="sub">${esc(f.rule || f.rule_id || t('Default rule'))}</span></td><td class="numeric">${rate(f.download_bytes_per_second || f.download_rate)}</td><td class="numeric">${rate(f.upload_bytes_per_second || f.upload_rate)}</td><td class="numeric">${bytes(sum)}</td><td class="numeric">${Number(f.connect_ms || 0).toFixed(1)} ms</td><td><span class="badge${flowActive(f) ? ' good' : ''}">${et(flowActive(f) ? 'Active' : 'Closed')}</span></td></tr>`;
}
// UDP relay counters come from the stats poll. Outbound IDs are user data and
// unknown reasons are shown verbatim; neither field carries hostnames or IPs.
function renderUDPRelay() {
  if(!Object.hasOwn(latestSnapshot,'udp_dial_failures') && !Object.hasOwn(latestSnapshot,'proxy_udp_drops')) return;
  const drops = latestSnapshot.proxy_udp_drops || {};
  $('udp-drop-target-limit').textContent = counter(drops.target_limit);
  $('udp-drop-recent-failure').textContent = counter(drops.recent_failure);
  $('udp-drop-local-destination').textContent = counter(drops.local_destination);
  const failures = Array.isArray(latestSnapshot.udp_dial_failures) ? latestSnapshot.udp_dial_failures : [];
  $('udp-dial-failures-table').hidden = failures.length === 0;
  $('udp-dial-failures-empty').hidden = failures.length > 0;
  $('udp-dial-failures').innerHTML = failures.map(item => {
    const reason = String(item?.reason ?? ''), known = Object.hasOwn(udpFailureReasons,reason) ? udpFailureReasons[reason] : null;
    const reasonText = known ? `${et(known[0])}<span class="sub">${et(known[1])}</span>` : esc(reason);
    return `<tr data-udp-failure><td data-label="${et('Outbound')}"><strong data-udp-failure-outbound>${esc(item?.outbound ?? '')}</strong></td><td data-label="${et('Reason')}" data-udp-failure-reason="${esc(reason)}"><div>${reasonText}</div></td><td class="numeric" data-label="${et('Count')}" data-udp-failure-count>${counter(item?.count)}</td></tr>`;
  }).join('');
}
function renderFlows() {
  const query = searchValue('flow-search');
  const visible = flows.map((f,index) => ({f,index})).filter(({f}) => [destinationAddress(f),f.host,f.domain,f.ip,f.port,f.outbound,networkName(f.network)].join(' ').toLowerCase().includes(query));
  let up = 0, down = 0, count = 0;
  for(const f of flows) { up += Number(f.upload_bytes_per_second || f.upload_rate) || 0; down += Number(f.download_bytes_per_second || f.download_rate) || 0; if(flowActive(f)) count++; }
  $('upload-rate').textContent = rate(up); $('download-rate').textContent = rate(down); $('active-count').textContent = count;
  $('flows-empty').hidden = flows.length > 0;
  renderNoMatch('flow-no-match',flows.length,visible.length,'flow-search');
  const container = $('flow-groups'), focused = document.activeElement;
  const focusGroup = focused?.closest?.('.flow-group')?.dataset.groupKey;
  const focusGroupAction = focused?.dataset?.groupAction === 'true';
  container.innerHTML = groupFlows(visible).map(group => {
    const open = query || openFlowGroups.has(group.key);
    const key = encodeURIComponent(group.key);
    const route = groupRouteSummary(group);
    const outbound = route.outboundCount > 1 ? t('Multiple outbounds') : route.outbound || t('Unspecified');
    const rule = route.routeCount > 1 ? t('{count} routing paths',{count:route.routeCount}) : route.rule || t('Default rule');
    return `<details class="flow-group" data-group-key="${esc(key)}"${open ? ' open' : ''}><summary><span class="flow-chevron" aria-hidden="true">›</span><span class="flow-destination"><strong>${esc(group.destination)}</strong><small>${et('{count} connections · {active} active',{count:group.connections.length,active:group.active})}</small></span><span class="flow-route"><span class="badge">${esc(outbound)}</span><small>${esc(rule)}</small></span><span class="flow-metric"><small>${et('Download')}</small><strong>${rate(group.downloadRate)}</strong></span><span class="flow-metric"><small>${et('Upload')}</small><strong>${rate(group.uploadRate)}</strong></span><span class="flow-metric flow-total"><small>${et('Transferred')}</small><strong>${bytes(group.transferred)}</strong></span><button type="button" class="table-action flow-action" data-group-action="true" data-flow="${route.index}" data-flow-id="${esc(String(route.flow.id ?? route.index))}">${et('Set outbound')}</button></summary><div class="flow-table"><table><thead><tr><th>${et('Connection')}</th><th>${et('Outbound / rule')}</th><th class="numeric">${et('Download')}</th><th class="numeric">${et('Upload')}</th><th class="numeric">${et('Transferred')}</th><th class="numeric">${et('Connect time')}</th><th>${et('Status')}</th></tr></thead><tbody>${group.connections.sort((a,b) => Number(flowActive(b.f))-Number(flowActive(a.f))).map(flowRow).join('')}</tbody></table></div></details>`;
  }).join('');
  if(focusGroup) {
    const group = [...container.querySelectorAll('.flow-group')].find(node => node.dataset.groupKey === focusGroup);
    const target = focusGroupAction ? group?.querySelector('[data-group-action]') : group?.querySelector('summary');
    target?.focus({preventScroll:true});
  }
}
function restartFlowPolling(runNow = false) {
  clearInterval(flowPollTimer);
  flowPollTimer = setInterval(poll,refreshSeconds*1000);
  if(runNow) poll();
}
function renderRules() {
  const rules = visibleRules(), filtered = !!searchValue('rules-search');
  const focused = document.activeElement, focusedID = focused?.closest?.('[data-rule-id]')?.dataset.ruleId;
  const attribute = ['data-edit-rule','data-up-rule','data-down-rule','data-delete-rule','data-select-rule'].find(key => focused?.hasAttribute?.(key));
  $('rules').innerHTML = rules.map(r => {
    const index = cfg.rules.findIndex(rule => rule.id === r.id), disabled = filtered || ruleMutationPending || rulesNeedReload;
    const targets = [...(r.domains || []),...(r.suffixes || []).map(s => '*.'+s.replace(/^\./,'')),...(r.cidrs || [])];
    const chips = [['Source CIDRs',r.sources],['Inbound types',r.inbounds],['Destination ports',r.ports],['Local rule sets',r.sets]].flatMap(([name,values]) => (values || []).map(value => '<span class="rule-match-chip">'+et(name)+': '+esc(value)+'</span>')).join('');
    return '<tr data-rule-id="'+esc(r.id)+'"><td class="selection-cell" data-label="'+et('Select')+'"><label><input type="checkbox" data-select-rule="'+esc(r.id)+'" aria-label="'+et('Select {name}',{name:r.id})+'"'+(selectedRules.has(r.id) ? ' checked' : '')+'></label></td><td data-label="'+et('Match destination')+'"><strong>'+esc(r.id)+'</strong><span class="sub">'+esc(targets.join(', '))+'</span><div class="rule-match-chips">'+chips+'</div></td><td data-label="'+et('Outbound')+'"><span class="badge">'+esc(r.adaptive ? t('Adaptive routing') : r.outbound)+'</span>'+(r.adaptive ? '<span class="sub">'+esc((r.candidates || []).join(', '))+'</span>' : '')+'</td><td data-label="'+et('IP family')+'">'+esc(r.family || t('Dual stack'))+'</td><td data-label="'+et('Order and actions')+'"><button class="table-action" data-edit-rule="'+esc(r.id)+'">'+et('Edit')+'</button> <button class="table-action" data-up-rule="'+esc(r.id)+'"'+(disabled || index === 0 ? ' disabled' : '')+' aria-label="'+et('Move {name} up',{name:r.id})+'">↑</button> <button class="table-action" data-down-rule="'+esc(r.id)+'"'+(disabled || index === cfg.rules.length-1 ? ' disabled' : '')+' aria-label="'+et('Move {name} down',{name:r.id})+'">↓</button> <button class="table-action danger" data-delete-rule="'+esc(r.id)+'"'+(ruleMutationPending || rulesNeedReload ? ' disabled' : '')+'>'+et('Delete')+'</button></td></tr>';
  }).join('');
  $('rules-empty').hidden = (cfg.rules || []).length > 0;
  $('rules-reorder-hint').hidden = !filtered;
  renderNoMatch('rules-no-match',(cfg.rules || []).length,rules.length,'rules-search');
  renderSelection('rules',rules.map(rule => rule.id));
  if(focusedID && attribute) restoreRuleFocus(focusedID,attribute);
}
function renderCapacity(syncInputs = !capacityDirty) {
  const form = $('source-capacity-form');
  if(!form || !cfg) return;
  const limits = cfg.connection_limits || {};
  const maxConn = Number.isInteger(limits.max_connections) && limits.max_connections > 0
    ? limits.max_connections
    : 256;
  const maxPerSource = Number.isInteger(limits.max_connections_per_source) && limits.max_connections_per_source > 0
    ? limits.max_connections_per_source
    : Math.min(256, maxConn);
  if(syncInputs) {
    const totalInput = $('capacity-max-total');
    const perSourceInput = $('capacity-max-per-source');
    if(totalInput) totalInput.value = String(maxConn);
    if(perSourceInput) perSourceInput.value = String(maxPerSource);
    capacityDraftRevision = cfg.revision;
  }
  const fields = $('source-capacity-fields');
  if(fields) fields.disabled = capacitySaving;
  const reloadBtn = $('capacity-reload');
  if(reloadBtn) reloadBtn.disabled = capacitySaving;
}
function renderSourceAccess() {
  renderCapacity();
  renderSourceClients();
  renderSourceRules();
}
function renderSourceClients() {
  const tbody = $('source-clients-body');
  if(!tbody) return;
  const empty = $('source-clients-empty');
  const timeSpan = $('source-activity-time');
  if(timeSpan) {
    if(sourceSnapshotGeneratedAt) {
      const d = new Date(sourceSnapshotGeneratedAt);
      timeSpan.textContent = t('Activity recorded at {time}', {time: d.toLocaleTimeString()});
    } else {
      timeSpan.textContent = '';
    }
  }
  const capTotal = $('source-capacity-total');
  const capTotalDetail = $('source-capacity-total-detail');
  const capPerSource = $('source-capacity-per-source');
  const capPerSourceDetail = $('source-capacity-per-source-detail');
  const capRejections = $('source-capacity-rejections');
  const capRejectionsDetail = $('source-capacity-rejections-detail');
  const cap = sourceCapacity;
  let effectiveSourceLimit = null;
  if(cap && Number.isInteger(cap.max_connections) && cap.max_connections > 0) {
    if(capTotal) capTotal.textContent = `${cap.active_connections} / ${cap.max_connections}`;
    if(capTotalDetail) capTotalDetail.textContent = t('effective limit');
    effectiveSourceLimit = cap.max_connections_per_source;
    const peakSourceActive = (sourceClients || []).reduce((max, c) => Math.max(max, c.active_connections || 0), 0);
    if(capPerSource) capPerSource.textContent = `${peakSourceActive} / ${effectiveSourceLimit}`;
    if(capPerSourceDetail) capPerSourceDetail.textContent = t('Busiest source / per-source limit');
    if(capRejections) capRejections.textContent = String(cap.source_limit + cap.total_limit);
    if(capRejectionsDetail) {
      capRejectionsDetail.innerHTML = `<span id="total-limit-rejections">${cap.total_limit}</span> ${et('total limit')} · <span id="source-limit-rejections">${cap.source_limit}</span> ${et('source limit')}`;
    }
  } else {
    if(capTotal) capTotal.textContent = t('Unavailable');
    if(capTotalDetail) capTotalDetail.textContent = t('Not available');
    if(capPerSource) capPerSource.textContent = t('Unavailable');
    if(capPerSourceDetail) capPerSourceDetail.textContent = t('Not available');
    if(capRejections) capRejections.textContent = t('Unavailable');
    if(capRejectionsDetail) capRejectionsDetail.textContent = t('Not available');
  }
  const query = searchValue('source-clients-search');
  const visible = (sourceClients || []).filter(client => [client.address,client.rule_id].join(' ').toLowerCase().includes(query));
  renderNoMatch('source-clients-no-match',(sourceClients || []).length,visible.length,'source-clients-search');
  if(!sourceClients || sourceClients.length === 0) {
    tbody.innerHTML = '';
    if(empty) empty.hidden = false;
    return;
  }
  if(empty) empty.hidden = true;
  const focused = document.activeElement;
  const hadSourceFocus = tbody.contains(focused);
  const focusedAddr = focused?.closest?.('[data-client-addr]')?.dataset.clientAddr || selectedSourceAddress;
  tbody.innerHTML = visible.map(c => {
    const isAllow = c.decision === 'allow';
    const badgeClass = isAllow ? 'good' : 'bad';
    const decisionText = isAllow ? t('Allowed') : t('Denied');
    const lastSeenText = c.last_seen ? new Date(c.last_seen).toLocaleTimeString() : t('Never');
    const canBlock = isAllow || c.active_connections > 0;
    const blockBtn = canBlock ? `<button class="table-action danger" data-block-source="${esc(c.address)}">${et('Block and disconnect')}</button>` : `<span class="muted">${et('Blocked')}</span>`;
    const connText = effectiveSourceLimit !== null
      ? `<strong>${c.active_connections}</strong> / ${effectiveSourceLimit} ${et('active')}`
      : `<strong>${c.active_connections}</strong> ${et('active')}`;
    const capRej = c.capacity_rejections || 0;
    const srcRej = c.source_limit_rejections || 0;
    const totRej = c.total_limit_rejections || 0;
    const counterText = `<div><small><span data-counter="accepted">${c.accepted_connections}</span> ${et('accepted')} · <span data-counter="denied">${c.denied_connections}</span> ${et('policy denied')}</small><span class="sub" data-counter="capacity-rejections"><strong data-counter="capacity-total">${capRej}</strong> ${et('capacity rejected')} (<span data-counter="source-limit">${srcRej}</span> ${et('source limit')}, <span data-counter="total-limit">${totRej}</span> ${et('total limit')})</span></div>`;
    return `<tr tabindex="-1" data-client-addr="${esc(c.address)}" class="${selectedSourceAddress === c.address ? 'selected-row' : ''}"><td data-label="${et('Client address')}"><strong>${esc(c.address)}</strong></td><td data-label="${et('Access status')}"><span class="badge ${badgeClass}">${esc(decisionText)}</span></td><td data-label="${et('Matched rule')}">${esc(c.rule_id || t('Default deny'))}</td><td data-label="${et('Connections')}">${connText}</td><td data-label="${et('Counters')}">${counterText}</td><td data-label="${et('Last seen')}">${esc(lastSeenText)}</td><td data-label="${et('Actions')}">${blockBtn}</td></tr>`;
  }).join('');
  if(hadSourceFocus && focusedAddr) focusSource(focusedAddr);
}
function focusSource(address) {
  const row = $('source-clients-body').querySelector(`[data-client-addr="${CSS.escape(address)}"]`);
  (row?.querySelector('[data-block-source]') || row)?.focus({preventScroll:true});
}
function renderSourceRules() {
  const tbody = $('source-rules-body');
  if(!tbody) return;
  const empty = $('source-rules-empty');
  const rules = cfg?.source_access?.rules || [];
  if(rules.length === 0) {
    renderSelection('source-rules',[]);
    tbody.innerHTML = '';
    if(empty) empty.hidden = false;
    return;
  }
  if(empty) empty.hidden = true;
  const focused = document.activeElement, focusedID = focused?.closest?.('[data-source-rule-id]')?.dataset.sourceRuleId;
  const focusAttribute = ['data-edit-source-rule','data-delete-source-rule','data-select-source-rule'].find(key => focused?.hasAttribute?.(key));
  renderSelection('source-rules',rules.map(rule => rule.id));
  tbody.innerHTML = rules.map(r => {
    const isAllow = r.action === 'allow';
    const actionClass = isAllow ? 'good' : 'bad';
    const actionText = isAllow ? t('Allow') : t('Deny');
    const statusText = r.enabled ? t('Enabled') : t('Disabled');
    const statusClass = r.enabled ? '' : 'muted';
    const cidrs = (r.cidrs || []).join(', ');
    return `<tr data-source-rule-id="${esc(r.id)}"><td class="selection-cell" data-label="${et('Select')}"><label><input type="checkbox" data-select-source-rule="${esc(r.id)}" aria-label="${et('Select {name}',{name:r.id})}"${selectedSourceRules.has(r.id) ? ' checked' : ''}></label></td><td data-label="${et('Rule')}"><strong>${esc(r.name || r.id)}</strong><br><small class="sub">${esc(r.id)}</small></td><td data-label="${et('Action')}"><span class="badge ${actionClass}">${esc(actionText)}</span></td><td data-label="${et('Network ranges (CIDR)')}"><code>${esc(cidrs)}</code></td><td data-label="${et('Status')}" class="${statusClass}">${esc(statusText)}</td><td data-label="${et('Note')}"><small>${esc(r.note || '')}</small></td><td data-label="${et('Actions')}"><button class="table-action" data-edit-source-rule="${esc(r.id)}">${et('Edit')}</button> <button class="table-action danger" data-delete-source-rule="${esc(r.id)}"${sourceRulesNeedReload ? ' disabled' : ''}>${et('Delete')}</button></td></tr>`;
  }).join('');
  if(focusedID && focusAttribute) [...tbody.querySelectorAll('[data-source-rule-id]')].find(row => row.dataset.sourceRuleId === focusedID)?.querySelector('['+focusAttribute+']')?.focus({preventScroll:true});
}
async function loadSourceClients(fresh = false) {
  if(!active || currentPage !== 'sources' || document.hidden) return;
  if(sourcePollPromise) {
    await sourcePollPromise;
    return fresh ? loadSourceClients() : undefined;
  }
  sourcePollPromise = (async () => {
    const finish = beginLoading(['source-clients-body']); let success = false;
    try {
      const data = await api('/source-clients');
      if(!active || currentPage !== 'sources' || document.hidden) return;
      sourceClients = data.clients || [];
      sourceSnapshotGeneratedAt = data.generated_at;
      sourceCapacity = data.capacity || null;
      renderSourceClients(); success = true;
    } catch(error) {
      if(error.status !== 401 && currentPage === 'sources' && !document.hidden) notice(error,true);
    } finally { finish(success); }
  })();
  try { await sourcePollPromise; }
  finally { sourcePollPromise = null; }
}
function restartSourcePolling() {
  stopSourcePolling();
  if(active && currentPage === 'sources' && !document.hidden) sourcePollTimer = setInterval(loadSourceClients,refreshSeconds*1000);
}
function stopSourcePolling() {
  if(sourcePollTimer) { clearInterval(sourcePollTimer); sourcePollTimer = null; }
}
function openSourceRule(index = -1) {
  sourceRuleDraft = cloneConfig();
  const isEdit = index >= 0;
  const r = isEdit ? sourceRuleDraft.source_access.rules[index] : null;
  editingSourceRuleID = r?.id || ''; 
  $('source-rule-dialog-title').textContent = t(isEdit ? 'Edit source rule' : 'Add source rule');
  $('src-rule-id').value = r ? r.id : '';
  $('src-rule-id').disabled = isEdit;
  $('src-rule-name').value = r ? r.name : '';
  $('src-rule-action').value = r ? r.action : 'allow';
  $('src-rule-enabled').checked = r ? r.enabled : true;
  $('src-rule-cidrs').value = r ? (r.cidrs || []).join('\n') : '';
  $('src-rule-note').value = r ? r.note || '' : '';
  clearError($('source-rule-form'));
  openModal($('source-rule-dialog'),document.activeElement,$('add-source-rule'));
}
function renderBlockSourceDialog() {
  if(!blockSourceContext) return;
  const {address,count} = blockSourceContext;
  const cidr = address + (address.includes(':') ? '/128' : '/32');
  $('block-source-description').textContent = t('Are you sure you want to block {address} ({cidr})? This will immediately disconnect {count} active connection(s).',{address,cidr,count});
}
function openBlockSourceDialog(address) {
  const client = sourceClients.find(c => c.address === address);
  blockSourceContext = {address,count:client?.active_connections || 0,revision:cfg.revision};
  renderBlockSourceDialog();
  clearError($('block-source-form'));
  openModal($('block-source-dialog'),document.activeElement,$('refresh-sources'));
  $('cancel-block-source').focus();
}
function renderOutbounds() {
  if(!cfg) return;
  const focused = document.activeElement;
  const focusedID = focused?.closest?.('[data-outbound-id]')?.dataset.outboundId;
  const focusAttribute = ['data-action','data-license','data-edit-outbound','data-delete-outbound','data-auth-outbound'].find(key => focused?.hasAttribute?.(key));
  const focusValue = focusAttribute === 'data-action' ? focused.getAttribute(focusAttribute) : '';

  const query = searchValue('outbound-search');
  const visible = cfg.outbounds.map((o,index) => ({o,index})).filter(({o}) => { const status = statuses.find(item => item.id === o.id) || {}; return [o.id,o.type,typeName(o.type),o.enabled ? status.state : 'disabled',o.enabled ? stateName(status.state) : t('Disabled')].join(' ').toLowerCase().includes(query); });
  const groupMatches = (cfg.outbound_groups || []).filter(group => [group.id,group.type,t(({select:'Select',fallback:'Fallback','load-balance':'Load balance'})[group.type] || group.type),...(group.members || [])].join(' ').toLowerCase().includes(query));
  renderNoMatch('outbound-no-match',cfg.outbounds.length+(cfg.outbound_groups || []).length,visible.length+groupMatches.length,'outbound-search');
  $('outbounds-empty').hidden = cfg.outbounds.length > 0;
  $('outbound-list').innerHTML = visible.map(({o,index}) => {
    const s = statuses.find(s => s.id === o.id) || {};
    const good = ['ready','connected','running'].includes(stateKey(s.state)), bad = ['error','unavailable','unsupported_account','unsupported_client'].includes(stateKey(s.state));
    const authURL = typeof s.auth_url === 'string' && /^https:\/\//i.test(s.auth_url) ? s.auth_url : '';
    const actionButtons = o.type === 'warp' ? [['connect','Connect'],['disconnect','Disconnect'],['register','Register'],['verify','Verify outbound']] : ['tailscale','tsnet'].includes(o.type) ? [['connect','Connect'],['disconnect','Disconnect'],['login','Sign in'],['logout','Sign out']] : o.type === 'wireguard' ? [['connect','Connect'],['disconnect','Disconnect']] : [];
    return `<article class="outbound-card" data-outbound-id="${esc(o.id)}"><div><div class="outbound-heading"><h3>${esc(o.id)}</h3><span class="badge${good ? ' good' : bad ? ' bad' : ' warn'}">${esc(o.enabled ? stateName(s.state) : t('Disabled'))}</span></div><p class="detail">${esc(s.detail_source ? t(s.detail_source) : s.detail || typeName(o.type))}</p><div class="outbound-meta"><span>${esc(typeName(o.type))}</span><span>${et(o.public_internet ? 'Internet access' : 'Fixed / private outbound')}</span>${s.account ? `<span>${et('Account: {value}',{value:s.account})}</span>` : ''}${s.version ? `<span>${et('Version {value}',{value:s.version})}</span>` : ''}${s.mode ? `<span>${et('Mode {value}',{value:s.mode})}</span>` : ''}${o.type === 'warp' ? `<span>${et('Proxy listener: {state}',{state:t(s.listener ? 'Listening' : 'Not ready')})}</span>` : ''}${s.verified_at && !s.verified_at.startsWith('0001-') ? `<span>${et('Last verified {date}',{date:i18n.date(s.verified_at)})}</span>` : ''}</div>${!o.enabled ? `<p class="hint">${et('Use Edit to enable this outbound before connecting.')}</p>` : ''}${authURL ? `<p class="hint"><a data-auth-outbound="${esc(o.id)}" href="${esc(authURL)}" target="_blank" rel="noopener noreferrer">${et('Open Tailscale sign-in')}</a></p>` : ''}</div><div class="outbound-actions">${actionButtons.map(([action,label]) => `<button class="quiet" data-action="${action}" data-id="${esc(o.id)}"${!o.enabled || action === 'register' && s.account || pendingOutboundActions.has(`${o.id}/${action}`) ? ' disabled' : ''}>${et(action === 'register' && s.account ? 'Registered' : label)}</button>`).join('')}${o.type === 'warp' ? `<button class="quiet" data-license="${esc(o.id)}"${o.enabled ? '' : ' disabled'}>${et('WARP+ license')}</button>` : ''}${o.id === 'direct' ? `<span class="hint">${et('Built-in outbound; cannot be deleted')}</span>` : `<button class="quiet" data-edit-outbound="${index}">${et('Edit')}</button><button class="danger" data-delete-outbound="${index}">${et('Delete')}</button>`}</div></article>`;
  }).join('');
  if(focusedID && focusAttribute) {
    const card = [...$('outbound-list').querySelectorAll('[data-outbound-id]')].find(node => node.dataset.outboundId === focusedID);
    const target = [...(card?.querySelectorAll(`[${focusAttribute}]`) || [])].find(node => focusAttribute !== 'data-action' || node.getAttribute(focusAttribute) === focusValue);
    if(target && !target.disabled) target.focus({preventScroll:true});
  }
}

async function loadDocker() {
  try {
    dockerBundle = await api('/integrations/docker');
    $('docker-proxy-url').value = dockerBundle.proxy_url;
    $('docker-no-proxy').value = dockerBundle.no_proxy;
    renderDockerExport();
  } catch(error) { displayError($('docker-error'),error); }
}
function renderDockerExport() {
  if(!dockerBundle) return;
  $('docker-config').value = dockerBundle.exports[$('docker-target').value] || '';
  $('docker-loopback-hint').hidden = !dockerBundle.loopback;
  $('docker-loopback-hint').textContent = t('Loopback points to Docker itself inside a container. Use a reachable host or LAN address.');
  $('docker-auth-hint').hidden = !dockerBundle.auth_required;
  $('docker-auth-hint').textContent = t('This Proxy requires authentication. Exports omit credentials; configure them separately.');
}
function openDeleteOutbound(index) {
  const outbound = cfg.outbounds[index];
  if(!outbound || outbound.id === 'direct') return;
  deletingOutbound = {id:outbound.id,config:cloneConfig()};
  $('delete-replacement').value = '';
  renderDeleteOutbound(); clearError($('delete-outbound-form')); openModal($('delete-outbound-dialog'),document.activeElement,$('add-outbound'));
}
function renderDeleteOutbound() {
  if(!deletingOutbound) return;
  const {id,config:c} = deletingOutbound;
  const rules = (c.rules || []).filter(r => r.outbound === id || (r.candidates || []).includes(id));
  const adaptive = (c.adaptive.candidates || []).includes(id) || rules.some(r => (r.candidates || []).includes(id));
  const groups = (c.outbound_groups || []).filter(group => (group.members || []).includes(id) || group.selected === id);
  const referenced = rules.length > 0 || c.default_outbound === id || adaptive || groups.length > 0;
  $('delete-outbound-name').textContent = t('Delete outbound "{name}"?',{name:id});
  const affected = rules.map(r => t('Routing rule: {name}',{name:r.id}));
  if(c.default_outbound === id) affected.push(t('Default outbound'));
  if((c.adaptive.candidates || []).includes(id)) affected.push(t('Adaptive routing'));
  for(const group of groups) affected.push(t('Outbound group: {name}',{name:group.id}));
  $('delete-outbound-references').innerHTML = affected.map(text => `<li>${esc(text)}</li>`).join('');
  $('delete-replacement-section').hidden = !referenced;
  const select = $('delete-replacement'), selected = select.value;
  select.required = referenced;
  const replacementTargets = [...c.outbounds,...(!adaptive && groups.length === 0 ? c.outbound_groups || [] : [])];
  select.innerHTML = `<option value="">${et('Choose a replacement')}</option>` + replacementTargets.filter(o => o.id !== id && o.enabled && (groups.length === 0 || o.type !== 'direct') && (!adaptive || o.public_internet && o.type !== 'tailscale')).map(o => `<option value="${esc(o.id)}">${esc(o.id)}</option>`).join('');
  select.value = selected;
}
function navigate(page) {
  const previousPage = currentPage;
  const names = {
    overview:['Connections','See your traffic. Choose its path.'],
    outbounds:['Outbounds','Manage your available connections.'],
    rules:['Routing rules','Choose how each destination connects.'],
    sources:['Source access','Manage client IP access to the proxy.'],
    settings:['Settings','Connect your browser and network.'],
    glossary:['Glossary','We explain the words used in Rillway.']
  };
  if(!names[page]) page = 'overview'; currentPage = page;
  if(page === 'settings' && active) { loadMemory(); loadWarpLog(); }
  if(page === 'outbounds' && active) { if(previousPage !== page && !document.hidden) pollStatuses(); scheduleOutboundPolling(); } else { stopOutboundPolling(); }
  if(page === 'sources' && active) { loadSourceClients(); restartSourcePolling(); } else { stopSourcePolling(); }
  for(const el of document.querySelectorAll('.page')) el.hidden = el.id !== `page-${page}`;
  for(const el of document.querySelectorAll('[data-page]')) {
    const selected = el.dataset.page === page;
    el.classList.toggle('selected',selected);
    if(selected) el.setAttribute('aria-current','page'); else el.removeAttribute('aria-current');
  }
  [$('page-title').textContent,$('page-context').textContent] = names[page].map(source => t(source));
}
function filterGlossary() {
  const query = $('glossary-search').value.normalize('NFKC').trim().toLowerCase();
  const terms = [...document.querySelectorAll('[data-glossary-term]')];
  let shown = 0;
  for(const term of terms) {
    const words = `${term.textContent} ${i18n.searchText(term)} ${term.dataset.glossaryKeywords || ''}`.normalize('NFKC').toLowerCase();
    term.hidden = query !== '' && !words.includes(query);
    if(!term.hidden) shown++;
  }
  for(const section of document.querySelectorAll('.glossary-section')) {
    section.hidden = ![...section.querySelectorAll('[data-glossary-term]')].some(term => !term.hidden);
  }
  $('glossary-count').textContent = t('{shown} of {total} terms',{shown,total:terms.length});
  $('glossary-empty').hidden = shown !== 0;
}
function clearOutboundSecrets() { outboundGeneration++; $('out-profile').value = ''; $('out-key').value = ''; $('out-upload').value = ''; outboundDrafts = {}; }
function logout() { resetBusyButtons(); sessionGeneration++; active = false; clearTimeout(noticeTimer); noticeTimer = null; lastNotice = null; clearSelections(); bulkContext = null; rulesNeedReload = false; sourceRulesNeedReload = false; ruleMutationPending = false; $('bulk-delete-dialog').close(); $('shortcuts-dialog').close(); loadedCollections.clear(); editors.reset(); ruleDraft = null; ruleOpening = false; $('rule-dialog').close(); $('rule-form').reset(); $('rule-latest').textContent = ''; $('rule-mine').textContent = ''; $('rule-review-panel').hidden = true; memoryStatus = null; memoryDirty = false; memoryDraftRevision = ''; $('memory-fields').disabled = true; warpLogStatus = null; warpLogDirty = false; warpLogDraftRevision = ''; if($('warp-log-fields')) $('warp-log-fields').disabled = true; capacityDirty = false; capacitySaving = false; capacityDraftRevision = null; if($('capacity-fields')) $('capacity-fields').disabled = true; if($('login')) $('login').hidden = false; if($('main')) $('main').hidden = true; cfg = null; clearOutboundSecrets(); }
async function openOutbound(index = -1) {
  try { formDefaults = await api('/defaults'); }
  catch(error) { notice(error,true); return; }
  outboundGeneration++; outboundIndex = index;
  const original = index >= 0 ? cfg.outbounds[index] : null;
  if(original?.id === 'direct') { notice('The built-in direct outbound cannot be deleted.',true); return; }
  const type = original?.type || 'warp';
  const o = {...formDefaults.outbounds[type],...original};
  if(type === 'warp' && !o.warp_binary) o.warp_binary = formDefaults.outbounds.warp.warp_binary;
  outboundDrafts = {}; outboundType = type;
  $('outbound-form-title').textContent = t(index >= 0 ? 'Edit outbound' : 'Add outbound');
  fillOutbound(o);
  clearError($('outbound-form')); openModal($('outbound-dialog'),document.activeElement,$('add-outbound'));
}
function fillOutbound(o) {
  const map = {id:'id',type:'type',address:'proxy_address',file:'config_file',hostname:'hostname',state:'state_dir',auth:'auth_key_file',binary:'warp_binary'};
  for(const [element,key] of Object.entries(map)) $(`out-${element}`).value = o[key] || '';
  $('out-id').disabled = outboundIndex >= 0; $('out-type').disabled = outboundIndex >= 0;
  $('out-profile').value = o.wireguard_config || ''; $('out-key').value = o.tailscale_auth_key || ''; $('out-upload').value = '';
  if(outboundIndex < 0) $('out-file').value = '';
  $('out-enabled').checked = !!o.enabled; $('out-public').checked = !!o.public_internet; $('out-dns').value = (o.dns || []).join('\n');
  outboundFields();
}
function changeOutboundType() {
  const type = $('out-type').value, id = $('out-id').value;
  const draft = {id, type:outboundType, enabled:$('out-enabled').checked, public_internet:$('out-public').checked, dns:lines($('out-dns').value)};
  const map = {address:'proxy_address',file:'config_file',hostname:'hostname',state:'state_dir',auth:'auth_key_file',binary:'warp_binary'};
  for(const [element,key] of Object.entries(map)) draft[key] = $(`out-${element}`).value;
  draft.wireguard_config = $('out-profile').value; draft.tailscale_auth_key = $('out-key').value;
  outboundDrafts[outboundType] = draft;
  const next = {...(outboundDrafts[type] || formDefaults.outbounds[type])};
  if(outboundIndex >= 0 || id !== formDefaults.outbounds[outboundType].id) next.id = id;
  outboundType = type; fillOutbound(next);
}
function outboundFields() {
  const type = $('out-type').value;
  for(const el of document.querySelectorAll('[data-out-field]')) el.hidden = !({proxy:['warp'],wireguard:['wireguard'],tailscale:['tailscale','tsnet'],warp:['warp'],dns:['wireguard','tailscale','tsnet']}[el.dataset.outField] || []).includes(type);
  $('out-public').disabled = ['tailscale','tsnet'].includes(type);
  if($('out-public').disabled) $('out-public').checked = false;
  const hints = {
    warp:'The usual WARP address and command are filled in. Save, then use Register, Connect and Verify outbound. Set WARP+ separately.',
    wireguard:'Import or paste your VPN provider configuration below, then enable and save this outbound. No keys are generated.',
    tailscale:'The node name and private state folder are filled in. Enable this outbound, save, then use its sign-in link. Public Internet access stays off.',
    direct:'Direct uses the server network. The built-in direct already exists; you usually do not need another.'
  };
  $('out-profile').required = type === 'wireguard' && outboundIndex < 0;
  const original = cfg?.outbounds[outboundIndex];
  const existingTail = outboundIndex >= 0 && ['tailscale','tsnet'].includes(type) && (formDefaults.restart_required_outbounds || []).includes(original?.id);
  for(const id of ['out-hostname','out-dns','out-key']) $(id).readOnly = existingTail;
  $('out-restart-hint').hidden = !existingTail;
  $('out-setup-hint').textContent = t(hints[type]);
}
async function openRule(id = null,flow = null,trigger = document.activeElement) {
  if(ruleOpening || $('rule-dialog').open) return;
  ruleOpening = true; const generation = sessionGeneration;
  try {
    const defaults = id === null ? await api('/defaults') : null;
    if(generation !== sessionGeneration || !active) return;
    ruleDraft = editors.createDraft('rules',id); const r = id === null ? defaults.rule : ruleDraft.baseEntity;
    $('rule-id').value = r.id;
    for(const key of ['domains','suffixes','cidrs','sources','ports']) $(`rule-${key}`).value = (r[key] || []).join('\n');
    for(const input of $('rule-inbounds').querySelectorAll('input')) input.checked = (r.inbounds || []).includes(input.value);
    $('rule-sets').innerHTML = (ruleDraft.baseConfig.rule_sets || []).map(set => `<label><input type="checkbox" value="${esc(set.id)}"${(r.sets || []).includes(set.id) ? ' checked' : ''}>${esc(set.id)} · ${esc(set.name)}</label>`).join('') || `<p class="hint">${et('No local rule sets yet')}</p>`;
    $('rule-setup-hint').hidden = id !== null || !!flow;
    if(flow) {
      $('rule-domains').value = ''; $('rule-suffixes').value = ''; $('rule-cidrs').value = '';
      const host = flow.host || flow.domain || flow.ip || '';
      if(host.includes(':') || /^\d+(\.\d+){3}$/.test(host)) $('rule-cidrs').value = `${host}/${host.includes(':') ? '128' : '32'}`;
      else $('rule-domains').value = host;
    }
    $('rule-outbound').innerHTML = options(r.adaptive ? '@adaptive' : (flow?.outbound || r.outbound),true);
    $('rule-family').value = r.family === 'auto' ? '' : r.family || '';
    candidateBoxes($('rule-candidates'),r.candidates || cfg.adaptive.candidates || [],'rule-candidate');
    $('rule-candidates').hidden = $('rule-outbound').value !== '@adaptive';
    clearError($('rule-form')); $('rule-review-panel').hidden = true; renderRuleConflict();
    openModal($('rule-dialog'),trigger,$('add-rule'));
  } catch(error) { if(generation === sessionGeneration) notice(error,true); }
  finally { if(generation === sessionGeneration) ruleOpening = false; }
}
function ruleEntity() {
  const adaptive = $('rule-outbound').value === '@adaptive';
  return {id:$('rule-id').value.trim(),...Object.fromEntries(['domains','suffixes','cidrs','sources','ports'].map(key => [key,lines($(`rule-${key}`).value)])),inbounds:checked($('rule-inbounds')),sets:checked($('rule-sets')),family:$('rule-family').value,adaptive,outbound:adaptive ? '' : $('rule-outbound').value,candidates:adaptive ? checked($('rule-candidates')) : []};
}
function renderRuleConflict() {
  const reviewing = !!ruleDraft?.reviewBase;
  if(!reviewing) $('rule-review-panel').hidden = true;
  $('rule-conflict').hidden = !ruleDraft?.requiresReview && !reviewing && !ruleDraft?.deleted;
  $('rule-conflict-message').textContent = t(ruleDraft?.deleted ? 'This item was deleted elsewhere. Close this editor to create a new item.' : 'Configuration changed elsewhere. Your draft is kept.');
  $('rule-review').hidden = !ruleDraft?.requiresReview || !!ruleDraft?.deleted;
  $('rule-save').disabled = !!ruleDraft?.saving || !!ruleDraft?.deleted || (!!ruleDraft?.requiresReview && !reviewing);
  $('rule-save').textContent = t(reviewing ? 'Save my draft' : 'Save rule');
}
function closeRule() {
  if(ruleDraft?.saving) return;
  if(ruleDraft?.dirty && !confirm(t('Discard unsaved changes?'))) return;
  $('rule-dialog').close();
}

function renderRevision() {
  $('program-version').textContent = serverInfo?.version ? t('Program version: {version}',{version:serverInfo.version}) : t('Program version unavailable');
  const revision = latestSnapshot.config_revision ?? cfg?.revision;
  $('revision').textContent = revision === undefined ? '' : String(revision);
  $('applied-at').textContent = latestSnapshot.applied_at ? i18n.date(latestSnapshot.applied_at) : t('Not available');
}
async function copyText(value) {
  try { await navigator.clipboard.writeText(value); notice('Configuration copied.'); }
  catch (_) { notice('Copy failed. Select the text and copy it manually.',true); }
}
function pacURL(address) {
  if(!address) return '';
  const wildcard = /^(?:0\.0\.0\.0|\[::\]|):(\d+)$/;
  const match = address.match(wildcard);
  if(match) address = `${location.hostname}:${match[1]}`;
  if(match && location.hostname.includes(':') && !location.hostname.startsWith('[')) address = `[${location.hostname}]:${match[1]}`;
  return `http://${address}/proxy.pac`;
}
function renderConfigText() {
  if(!cfg) return;
  renderRevision();
  $('adaptive-description').textContent = t(cfg.adaptive.enabled ? 'Choose outbounds for new connections using availability and probe results. Explicit rules take priority.' : 'When disabled, connections use routing rules and the default outbound.');
  $('pac-url').value = pacURL(cfg.listeners.pac);
  $('pac-copy').disabled = !$('pac-url').value;
  $('pac-hint').textContent = t(cfg.listeners.pac ? 'Use a PAC URL your Mac can reach. A loopback address works only on the Server itself. Saving PAC rules does not change the listener or apply settings to your Mac.' : 'PAC is not configured. Enable its listener in the Server configuration and restart the service.');
  $('listeners').innerHTML = Object.entries(cfg.listeners).map(([k,v]) => `<dt>${esc(t({http:'HTTP Proxy',socks5:'SOCKS5',admin:'Management UI',pac:'PAC'}[k] || k))}</dt><dd>${esc(v || t('Not configured'))}</dd>`).join('');
  const security = cfg.security || {};
  $('server-security').innerHTML = `<dt>${et('Management and PAC allowed networks')}</dt><dd>${esc((security.allowed_clients || []).join(', ') || t('Not configured'))}</dd><dt>${et('Proxy username')}</dt><dd>${esc(security.proxy_username || t('Not configured'))}</dd><dt>${et('Management token and TLS credentials')}</dt><dd>${et('Managed on Server; secret contents are never shown.')}</dd>`;
  const limitsEl = $('connection-limits');
  if(limitsEl) {
    limitsEl.innerHTML = `<dt>${et('Connection capacity')}</dt><dd><a href="#sources">${et('Adjust connection capacity on the Source access page.')}</a></dd>`;
  }
}
async function loadMemory(explicit = false) {
  if(!active || memoryLoading || memorySaving) return;
  memoryLoading = true; $('memory-refresh').disabled = true;
  try {
    const status = await api('/service/memory');
    memoryStatus = status;
    // Only an explicit refresh may advance an edited form's revision.
    if(explicit || !memoryDirty) memoryDraftRevision = status.revision || '';
    renderMemory(!memoryDirty);
    if(explicit) clearError($('memory-form'));
  } catch(error) { errorIn($('memory-form'),error); $('memory-fields').disabled = true; }
  finally { memoryLoading = false; $('memory-refresh').disabled = false; }
}
function renderMemory(fill = false) {
  const s = memoryStatus;
  $('memory-fields').disabled = memorySaving || !s?.supported;
  if(!s) { $('memory-reason').textContent = t('Refresh limits to detect host capacity and the service safety minimum.'); return; }
  $('memory-reason').textContent = s.supported ? t(s.minimum_reason) : t(s.reason);
  $('memory-status').innerHTML = [
    [t('Detected host memory'),bytes(s.host_bytes)],
    [t('Current service usage'),bytes(s.current_bytes)],
    [t('Current memory limit'),s.limit_bytes ? bytes(s.limit_bytes) : t('Unlimited')],
    [t('Allowed memory range'),`${bytes(s.minimum_bytes)} – ${bytes(s.maximum_bytes)}`]
  ].map(([label,value]) => `<dt>${esc(label)}</dt><dd>${esc(value)}</dd>`).join('');
  if(fill) { $('memory-mode').value = s.mode || 'MiB'; memoryInputMode = $('memory-mode').value; $('memory-value').value = s.value || '512'; }
  memoryPreview();
}
function memoryPreview() {
  const s = memoryStatus;
  if(!s?.supported) { $('memory-preview').textContent = ''; return; }
  const mode = $('memory-mode').value, input = $('memory-value'), scale = mode === 'percent' ? s.host_bytes/100 : mode === 'GiB' ? 1073741824 : 1048576;
  input.min = String(Math.ceil(s.minimum_bytes/scale*1000)/1000);
  input.max = mode === 'percent' ? '90' : String(Math.floor(s.maximum_bytes/scale*1000)/1000);
  let budget = Number(input.value)*scale;
  if(mode === 'percent') budget = Math.floor(budget/1048576)*1048576;
  $('memory-preview').textContent = t('Selected limit: {limit}. Allowed: {minimum}–{maximum} {unit}.',{limit:bytes(budget),minimum:input.min,maximum:input.max,unit:mode === 'percent' ? '%' : mode});
}
async function loadWarpLog(explicit = false) {
  if(!active || warpLogLoading || warpLogSaving) return;
  const generation = ++warpLogGeneration;
  warpLogLoadFailed = false;
  warpLogLoading = true; $('warp-log-refresh').disabled = true; renderWarpLog(false);
  try {
    const status = await api('/service/logging/warp');
    if(generation !== warpLogGeneration) return;
    warpLogLoadFailed = false;
    warpLogStatus = status;
    if(explicit || !warpLogDirty) warpLogDraftRevision = status.revision || '';
    renderWarpLog(!warpLogDirty);
    if(explicit) clearError($('warp-log-form'));
  } catch(error) {
    if(generation !== warpLogGeneration) return;
    if(error.status === 501) {
      warpLogStatus = {supported:false, reason:t('WARP system logging requires an upgraded Linux service. Update the binary and run sudo rillway service restart.')};
      renderWarpLog(false);
    } else {
      warpLogLoadFailed = true;
      errorIn($('warp-log-form'),error);
      $('warp-log-fields').disabled = true;
    }
  }
  finally {
    if(generation === warpLogGeneration) {
      warpLogLoading = false;
      $('warp-log-refresh').disabled = false;
      renderWarpLog(false);
    }
  }
}
function renderWarpLog(fill = false) {
  const s = warpLogStatus;
  const form = $('warp-log-form');
  if(!form) return;
  const fields = $('warp-log-fields');
  const statusCard = $('warp-log-status-card');
  const unsupported = $('warp-log-unsupported');
  const unsupportedReason = $('warp-log-unsupported-reason');
  const currentLevel = $('warp-log-current-level');
  const effectiveLevel = $('warp-log-effective-level');
  const levelSelect = $('warp-log-level');
  const levelNames = {
    inherit: 'Use service default (inherit)',
    warning: 'Warning',
    info: 'Info',
    debug: 'Debug'
  };
  if(!s) {
    fields.disabled = true;
    statusCard.hidden = true;
    unsupported.hidden = true;
    return;
  }
  if(!s.supported) {
    fields.disabled = true;
    statusCard.hidden = true;
    unsupported.hidden = false;
    unsupportedReason.textContent = s.reason ? t(s.reason) : t('WARP system logging is not supported in this environment.');
    return;
  }
  unsupported.hidden = true;
  statusCard.hidden = false;
  fields.disabled = warpLogSaving || warpLogLoading || warpLogLoadFailed;
  const configuredLabel = levelNames[s.level] ? t(levelNames[s.level]) : (s.level || '—');
  const effectiveLabel = levelNames[s.effective_level] ? t(levelNames[s.effective_level]) : (s.effective_level || configuredLabel);
  currentLevel.textContent = configuredLabel;
  effectiveLevel.textContent = effectiveLabel;
  if(fill && s.level) {
    levelSelect.value = s.level;
  }
}
function switchLocale(locale) {
  i18n.setLocale(locale);
  filterGlossary();
  renderMemory(false);
  renderWarpLog(false);
  renderDockerExport();
  if($('outbound-dialog').open) outboundFields();
  navigate(currentPage);
  connection(isOnline);
  if(cfg) {
    renderConfigText(); translatePACRows(); renderFlows(); renderUDPRelay(); renderRules(); renderOutbounds(); renderSourceAccess(); renderDeleteOutbound();
    editors.render(); renderRuleConflict();
    for(const id of ['default-outbound','rule-outbound']) {
      const select = $(id), selected = select.value;
      if(select.options.length) { select.innerHTML = options(selected,id === 'rule-outbound'); select.value = selected; }
    }
  }
  $('outbound-form-title').textContent = t(outboundIndex >= 0 ? 'Edit outbound' : 'Add outbound');
  $('source-rule-dialog-title').textContent = t(editingSourceRuleID ? 'Edit source rule' : 'Add source rule');
  renderBlockSourceDialog(); renderBulkContext();
  for(const node of document.querySelectorAll('[data-error-source]')) node.textContent = t(node.dataset.errorSource);
  if(lastNotice) { const {message} = lastNotice; $('notice').textContent = typeof message === 'string' ? t(message) : errorText(message); }
  for(const id of ['adaptive-candidates','rule-candidates']) { const hint = $(id).querySelector('.hint'); if(hint) hint.textContent = t('Enable an outbound with Internet access first.'); }
  if(active) pollStatuses();
}
function onCapacityEdit() {
  if(!capacityDirty) {
    capacityDirty = true;
    capacityDraftRevision = cfg?.revision;
  }
}
let uiBound = false;
function bindUI() {
  if(uiBound) return;
  uiBound = true;
  RillwayTheme.bind();
  editors.bind();
  const searchRenderers = {'flow-search':renderFlows,'outbound-search':() => { renderOutbounds(); editors.render(); },'rules-search':() => { selectedRules.clear(); renderRules(); },'source-clients-search':renderSourceClients,'glossary-search':filterGlossary};
  for(const id of ['outbound-search','rules-search','source-clients-search']) $(id).addEventListener('input',searchRenderers[id]);
  document.querySelectorAll('[data-clear-search]').forEach(button => button.addEventListener('click',() => { const id = button.dataset.clearSearch; $(id).value = ''; searchRenderers[id](); $(id).focus(); }));
  document.querySelector('.skip-link').addEventListener('click',event => { event.preventDefault(); $('main-content').focus(); });
  document.addEventListener('keydown',event => {
    if(!active || document.querySelector('dialog[open]')) return;
    const element = event.target, focusedSearch = Object.keys(searchRenderers).includes(element.id);
    if(event.key === 'Escape' && focusedSearch) { event.preventDefault(); element.value = ''; searchRenderers[element.id](); element.focus(); return; }
    if(element.closest?.('input,textarea,select,[contenteditable]:not([contenteditable="false"])') || event.ctrlKey || event.metaKey || event.altKey) return;
    if(event.key === '/') {
      const id = {overview:'flow-search',outbounds:'outbound-search',rules:'rules-search',sources:'source-clients-search',glossary:'glossary-search'}[currentPage];
      if(id) { event.preventDefault(); $(id).focus(); }
    } else if(event.key === '?') { event.preventDefault(); openModal($('shortcuts-dialog'),element,$('main-content')); $('shortcuts-dialog').querySelector('[data-close]').focus(); }
  });
  for(const [kind,bodyID,attribute,selection] of [['rules','rules','data-select-rule',selectedRules],['source-rules','source-rules-body','data-select-source-rule',selectedSourceRules]]) {
    const visibleIDs = () => kind === 'rules' ? visibleRules().map(rule => rule.id) : (cfg?.source_access?.rules || []).map(rule => rule.id);
    $(bodyID).addEventListener('change',event => { const checkbox = event.target.closest('['+attribute+']'); if(!checkbox) return; const id = checkbox.getAttribute(attribute); if(checkbox.checked) selection.add(id); else selection.delete(id); renderSelection(kind,visibleIDs()); });
    $(kind+'-select-all').addEventListener('change',event => { for(const id of visibleIDs()) { if(event.target.checked) selection.add(id); else selection.delete(id); } if(kind === 'rules') renderRules(); else renderSourceRules(); });
    $(kind+'-bulk-delete').addEventListener('click',event => openBulkAction(kind,'delete',event.currentTarget));
  }
  for(const action of ['enable','disable']) $('source-rules-bulk-'+action).addEventListener('click',event => openBulkAction('source-rules',action,event.currentTarget));
  $('bulk-delete-dialog').addEventListener('close',() => { if(!$('bulk-delete-dialog').open) bulkContext = null; });
  $('bulk-delete-form').addEventListener('submit',event => {
    event.preventDefault(); const context = bulkContext; if(!context || context.conflict || $('bulk-delete-confirm').disabled) return;
    return withBusy($('bulk-delete-confirm'),async () => {
      const next = structuredClone(context.config), ids = new Set(context.ids);
      if(context.kind === 'rules') next.rules = next.rules.filter(rule => !ids.has(rule.id));
      else if(context.action === 'delete') next.source_access.rules = next.source_access.rules.filter(rule => !ids.has(rule.id));
      else for(const rule of next.source_access.rules) if(ids.has(rule.id)) rule.enabled = context.action === 'enable';
      try { await save(next); if(context.generation === sessionGeneration && bulkContext === context) $('bulk-delete-dialog').close(); }
      catch(error) {
        if(context.generation !== sessionGeneration || bulkContext !== context || error.stale) return;
        errorIn(event.target,error);
        if(error.status === 409) { context.conflict = true; $('bulk-reload').hidden = false; if(context.kind === 'rules') { rulesNeedReload = true; $('rules-reload').hidden = false; } else { sourceRulesNeedReload = true; $('source-rules-reload').hidden = false; } renderRules(); renderSourceRules(); }
      }
    }).finally(() => { if(bulkContext === context) renderBulkContext(); });
  });
  $('bulk-reload').addEventListener('click',() => withBusy($('bulk-reload'),async () => { const context = bulkContext; try { await reloadCollections(); if(context?.generation === sessionGeneration && bulkContext === context) $('bulk-delete-dialog').close(); } catch(error) { errorIn($('bulk-delete-form'),error); } }));
  document.querySelectorAll('[data-locale]').forEach(select => select.addEventListener('change',() => switchLocale(select.value)));

  $('memory-mode').addEventListener('change',() => {
    const s = memoryStatus, mode = $('memory-mode').value;
    if(s?.supported) {
      const scale = unit => unit === 'percent' ? s.host_bytes/100 : unit === 'GiB' ? 1073741824 : 1048576;
      const value = Number($('memory-value').value)*scale(memoryInputMode)/scale(mode);
      if(Number.isFinite(value) && value > 0) $('memory-value').value = (Math.ceil(value*1000)/1000).toFixed(3);
    }
    memoryInputMode = mode; memoryDirty = true; memoryPreview();
  });
  $('memory-value').addEventListener('input',() => { memoryDirty = true; memoryPreview(); });
  $('memory-refresh').addEventListener('click',() => loadMemory(true));
  $('memory-form').addEventListener('submit',async e => {
    e.preventDefault(); if(memorySaving || !memoryStatus?.supported) return;
    return withBusy($('memory-save'),async () => {
    const body = {mode:$('memory-mode').value,value:$('memory-value').value.trim(),revision:memoryDraftRevision};
    memorySaving = true; renderMemory(false); clearError(e.target);
    try {
      memoryStatus = await api('/service/memory',{method:'PUT',body:JSON.stringify(body)});
      memoryDraftRevision = memoryStatus.revision; memoryDirty = false;
      renderMemory(true); notice('Memory limit saved. Applied without restarting the service.');
    } catch(error) { errorIn(e.target,error); }
    finally { memorySaving = false; renderMemory(false); }
    }).finally(() => renderMemory(false));
  });
  $('login-form').addEventListener('submit',async event => {
    event.preventDefault(); const generation = ++sessionGeneration; editors.reset(); token = $('token').value.trim(); $('login-error').textContent = ''; delete $('login-error').dataset.errorSource;
    try { await load(); if(generation !== sessionGeneration) return; try { sessionStorage.setItem('rillway-token',token); } catch (_) {} $('token').value = ''; }
    catch(error) { if(generation === sessionGeneration) displayError($('login-error'),error); }
  });
  $('logout').addEventListener('click',logout);
  $('refresh').addEventListener('click',() => load().catch(e => notice(e,true)));
  $('flow-search').addEventListener('input',renderFlows);
  $('flow-refresh').value = String(refreshSeconds);
  $('flow-refresh').addEventListener('change',e => {
    const seconds = Number(e.target.value);
    if(!refreshChoices.includes(seconds)) return;
    refreshSeconds = seconds;
    try { localStorage.setItem('rillway-flow-refresh-seconds',String(seconds)); } catch (_) {}
    restartFlowPolling(true);
    restartSourcePolling();
  });
  if($('outbound-refresh')) {
    $('outbound-refresh').value = String(outboundRefreshSeconds);
    $('outbound-refresh').addEventListener('change',e => {
      const seconds = Number(e.target.value);
      if(!outboundRefreshChoices.includes(seconds)) return;
      outboundRefreshSeconds = seconds;
      try { localStorage.setItem('rillway-outbound-refresh-seconds',String(seconds)); } catch (_) {}
      scheduleOutboundPolling();
    });
  }
  if($('refresh-outbounds')) {
    $('refresh-outbounds').addEventListener('click',() => pollStatuses(true));
  }
  if($('warp-log-level')) {
    $('warp-log-level').addEventListener('change',() => { warpLogDirty = true; });
  }
  if($('warp-log-refresh')) {
    $('warp-log-refresh').addEventListener('click',() => loadWarpLog(true));
  }
  if($('warp-log-form')) {
    $('warp-log-form').addEventListener('submit',async e => {
      e.preventDefault(); if(warpLogSaving || warpLogLoading || !warpLogStatus?.supported) return;
      return withBusy($('warp-log-save'),async () => {
      const level = $('warp-log-level').value;
      const body = {level, revision:warpLogDraftRevision};
      ++warpLogGeneration;
      warpLogSaving = true; renderWarpLog(false); clearError(e.target);
      const saveBtn = $('warp-log-save');
      if(saveBtn) saveBtn.textContent = t('Saving logging level…');
      try {
        warpLogStatus = await api('/service/logging/warp',{method:'PUT',body:JSON.stringify(body)});
        warpLogDraftRevision = warpLogStatus.revision || ''; warpLogDirty = false;
        renderWarpLog(true); notice('WARP logging level applied. WARP service restarted.');
      } catch(error) {
        if(error.status === 409) {
          const draftLevel = level;
          try {
            const latest = await api('/service/logging/warp');
            warpLogStatus = latest;
            warpLogDraftRevision = latest.revision || '';
            renderWarpLog(false);
            $('warp-log-level').value = draftLevel;
            warpLogDirty = true;
          } catch (_) {}
          displayError(e.target.querySelector('.form-error'), sourceError('WARP logging settings changed elsewhere. Refresh settings before saving.'));
          notice('Your selected level has been kept. Refresh status to review the current setting, then save again.', true);
        } else if(error.status === 501) {
          warpLogStatus = {supported:false, reason:t('WARP system logging requires an upgraded Linux service. Update the binary and run sudo rillway service restart.')};
          renderWarpLog(false);
          displayError(e.target.querySelector('.form-error'), sourceError('WARP system logging requires an upgraded Linux service. Update the binary and run sudo rillway service restart.'));
        } else {
          errorIn(e.target,error);
        }
      } finally {
        warpLogSaving = false;
        if(saveBtn) saveBtn.textContent = t('Save logging level');
        renderWarpLog(false);
      }
      }).finally(() => renderWarpLog(false));
    });
  }
  $('flow-groups').addEventListener('toggle',e => {
    if(!e.target.matches('.flow-group')) return;
    const key = decodeURIComponent(e.target.dataset.groupKey);
    if(e.target.open) openFlowGroups.add(key); else openFlowGroups.delete(key);
  },true);
  $('glossary-search').addEventListener('input',filterGlossary);
  $('glossary-clear').addEventListener('click',() => { $('glossary-search').value = ''; filterGlossary(); $('glossary-search').focus(); });
  document.querySelectorAll('[data-page]').forEach(b => b.addEventListener('click',() => { navigate(b.dataset.page); history.replaceState(null,'',`#${b.dataset.page}`); }));
  document.querySelectorAll('[data-close]').forEach(b => b.addEventListener('click',() => $(b.dataset.close).close()));
  $('license-dialog').addEventListener('close',() => { $('license-value').value = ''; licenseID = ''; });
  $('add-outbound').addEventListener('click',() => openOutbound());
  $('out-type').addEventListener('change',changeOutboundType);
  $('add-rule').addEventListener('click',() => openRule());
  $('add-pac-domain').addEventListener('click',() => addPACEntry('domains'));
  $('add-pac-cidr').addEventListener('click',() => addPACEntry('cidrs'));
  for(const kind of ['domains','cidrs']) {
    $(`pac-${kind}`).addEventListener('change',e => { if(e.target.matches('[data-pac-enabled]')) updatePACSwitch(e.target); if(e.target.matches('[data-pac-value]')) { const row = e.target.closest('.pac-entry'); row.querySelector('[data-pac-enabled]').setAttribute('aria-label',t('Bypass {destination}',{destination:e.target.value})); } });
    $(`pac-${kind}`).addEventListener('click',e => {
      const remove = e.target.closest('[data-remove-pac]');
      if(remove) remove.closest('.pac-entry').remove();
    });
  }
  $('rule-outbound').addEventListener('change',() => { $('rule-candidates').hidden = $('rule-outbound').value !== '@adaptive'; });
  $('flow-groups').addEventListener('click',e => { const b = e.target.closest('[data-flow]'); if(b) { e.preventDefault(); e.stopPropagation(); openRule(null,flows[Number(b.dataset.flow)],b); } });
  $('adaptive-toggle').addEventListener('change',async () => { const next = cloneConfig(); next.adaptive.enabled = $('adaptive-toggle').checked; next.adaptive.candidates = checked($('adaptive-candidates')); try { await save(next); } catch(e) { if(e.stale) return; $('adaptive-toggle').checked = cfg.adaptive.enabled; notice(e,true); } });
  $('adaptive-candidates').addEventListener('change',async () => { const next = cloneConfig(); next.adaptive.candidates = checked($('adaptive-candidates')); try { await save(next); } catch(e) { if(e.stale) return; renderConfig(); notice(e,true); } });
  $('save-default').addEventListener('click',async () => { const next = cloneConfig(); next.default_outbound = $('default-outbound').value; try { await save(next); } catch(e) { notice(e,true); } });
  $('pac-form').addEventListener('submit',e => { e.preventDefault(); return withBusy(e.target.querySelector('button.primary'),async () => { const next = cloneConfig(); next.pac.proxy_address = $('pac-address').value.trim(); next.pac.bypass_domains = serializePACList('domains'); next.pac.bypass_cidrs = serializePACList('cidrs'); try { await save(next); } catch(error) { notice(error,true); } }); });
  $('restart-service').addEventListener('click',() => { clearError($('restart-form')); openModal($('restart-dialog'),document.activeElement,$('restart-service')); });
  $('restart-form').addEventListener('submit',async e => {
    e.preventDefault(); const button = $('confirm-restart'); button.disabled = true;
    try {
      await api('/service/restart',{method:'POST',body:'{}'});
      $('restart-dialog').close(); active = false; connection(false);
      notice('Restarting the service. Existing connections will close. Waiting for it to return.');
      let recovered = false;
      for(let attempt = 0; attempt < 30; attempt++) {
        await new Promise(resolve => setTimeout(resolve,1000));
        try { await load(); recovered = true; break; } catch (_) { active = false; }
      }
      if(recovered) notice('Service restarted. The saved configuration is now active.');
      else notice('The service has not returned at this address. If its address or credentials changed, open the new address and sign in again.',true);
    } catch(error) { errorIn(e.target,error); }
    finally { button.disabled = false; }
  });
  $('copy-json').addEventListener('click',() => copyText($('config-json').value));
  $('pac-copy').addEventListener('click',() => copyText($('pac-url').value));
  $('out-upload').addEventListener('change',async () => {
    const file = $('out-upload').files?.[0], generation = outboundGeneration; if(!file) return;
    if(file.size > 1048576) { errorIn($('outbound-form'),sourceError('WireGuard configuration must be at most 1 MiB.')); $('out-upload').value = ''; return; }
    try { const text = await file.text(); if(generation !== outboundGeneration || !$('outbound-dialog').open) return; $('out-profile').value = text; clearError($('outbound-form')); }
    catch (_) { if(generation === outboundGeneration && $('outbound-dialog').open) errorIn($('outbound-form'),sourceError('Could not read the selected configuration file.')); }
  });
  $('outbound-dialog').addEventListener('close',() => { if(!$('outbound-dialog').open) clearOutboundSecrets(); });
  $('reset-json').addEventListener('click',() => load().catch(e => notice(e,true)));
  $('outbound-form').addEventListener('submit',async e => {
    e.preventDefault(); const next = cloneConfig(); const original = outboundIndex >= 0 ? next.outbounds[outboundIndex] : {};
    const generation = sessionGeneration;
    return withBusy(e.target.querySelector('button.primary'),async () => {
    const o = {...original,id:$('out-id').value.trim(),type:$('out-type').value,enabled:$('out-enabled').checked,public_internet:$('out-public').checked,proxy_address:$('out-address').value.trim(),config_file:$('out-file').value.trim(),hostname:$('out-hostname').value.trim(),state_dir:$('out-state').value.trim(),auth_key_file:$('out-auth').value.trim(),warp_binary:$('out-binary').value.trim(),dns:lines($('out-dns').value)};
    if(o.type !== 'warp') { o.proxy_address = ''; o.warp_binary = ''; }
    if(o.type !== 'wireguard' || outboundIndex < 0) o.config_file = '';
    if(!['tailscale','tsnet'].includes(o.type)) { o.hostname = ''; o.state_dir = ''; o.auth_key_file = ''; }
    if(outboundIndex >= 0 && o.type === 'tailscale' && (formDefaults.restart_required_outbounds || []).includes(o.id)) { for(const key of ['hostname','state_dir','auth_key_file','dns']) o[key] = original[key]; }
    try {
      const body = {revision:cfg.revision,create:outboundIndex < 0,outbound:o};
      if(o.type === 'wireguard' && $('out-profile').value.trim()) body.wireguard_config = $('out-profile').value;
      if(o.type === 'tailscale' && $('out-key').value.trim()) body.tailscale_auth_key = $('out-key').value;
      const updated = await api('/outbounds',{method:'PUT',body:JSON.stringify(body)});
      if(generation !== sessionGeneration) return;
      clearOutboundSecrets(); $('outbound-dialog').close(); publishConfig(updated); notice('Configuration saved. New connections will use the updated rules.'); await poll();
    } catch(error) { errorIn(e.target,error); }
    });
  });
  $('rule-form').addEventListener('input',() => { if(ruleDraft) ruleDraft.dirty = true; });
  $('rule-form').addEventListener('change',() => { if(ruleDraft) ruleDraft.dirty = true; });
  $('rule-dialog').addEventListener('cancel',event => { event.preventDefault(); closeRule(); });
  $('rule-dialog').querySelectorAll('[data-rule-close]').forEach(button => button.addEventListener('click',closeRule));
  $('rule-dialog').addEventListener('close',() => { if($('rule-dialog').open) return; ruleDraft = null; $('rule-form').reset(); $('rule-latest').textContent = ''; $('rule-mine').textContent = ''; $('rule-review-panel').hidden = true; });
  $('rule-review').addEventListener('click',() => withBusy($('rule-review'),async () => {
    const draft = ruleDraft; if(!draft) return;
    try {
      const latest = await editors.reviewDraft(draft);
      if(ruleDraft !== draft) return;
      $('rule-latest').textContent = JSON.stringify(latest,null,2); $('rule-mine').textContent = JSON.stringify(ruleEntity(),null,2);
      $('rule-review-panel').hidden = false;
    } catch(error) { if(ruleDraft === draft) errorIn($('rule-form'),error); }
    if(ruleDraft === draft) renderRuleConflict();
  }));
  $('rule-form').addEventListener('submit',event => {
    event.preventDefault(); const draft = ruleDraft; if(!draft || draft.saving || $('rule-save').disabled) return;
    return withBusy($('rule-save'),async () => {
      try { if(await editors.saveDraft(draft,ruleEntity())) $('rule-dialog').close(); }
      catch(error) { if(ruleDraft === draft) errorIn(event.target,error); }
    }).finally(() => { if(ruleDraft === draft) renderRuleConflict(); });
  });
  $('rules').addEventListener('click',event => {
    const edit = event.target.closest('[data-edit-rule]'); if(edit) { openRule(edit.dataset.editRule,null,edit); return; }
    const up = event.target.closest('[data-up-rule]'), down = event.target.closest('[data-down-rule]'), del = event.target.closest('[data-delete-rule]');
    if(del) { openBulkAction('rules','delete',del,[del.dataset.deleteRule]); return; }
    const id = up?.dataset.upRule || down?.dataset.downRule;
    if(id && !(up || down).disabled) reorderRule(id,cfg.rules.findIndex(rule => rule.id === id)+(up ? -1 : 1),up ? 'data-up-rule' : 'data-down-rule');
  });
  $('rules').addEventListener('keydown',event => {
    if(!event.altKey || !['ArrowUp','ArrowDown','Home','End'].includes(event.key)) return;
    const row = event.target.closest('[data-rule-id]'); if(!row) return;
    event.preventDefault(); if(searchValue('rules-search')) return;
    const id = row.dataset.ruleId, index = cfg.rules.findIndex(rule => rule.id === id);
    const attribute = ['data-edit-rule','data-up-rule','data-down-rule','data-delete-rule','data-select-rule'].find(key => event.target.hasAttribute(key)) || 'data-edit-rule';
    reorderRule(id,{ArrowUp:index-1,ArrowDown:index+1,Home:0,End:cfg.rules.length-1}[event.key],attribute);
  });
  for(const id of ['rules-reload','source-rules-reload']) $(id).addEventListener('click',() => withBusy($(id),async () => { try { await reloadCollections(); } catch(error) { notice(error,true); } }));
  $('refresh-sources').addEventListener('click',() => loadSourceClients(true).catch(e => notice(e,true)));
  $('add-source-rule').addEventListener('click',() => openSourceRule());
  $('source-rules-body').addEventListener('click',event => {
    const edit = event.target.closest('[data-edit-source-rule]');
    if(edit) { const index = cfg.source_access.rules.findIndex(rule => rule.id === edit.dataset.editSourceRule); if(index >= 0) openSourceRule(index); return; }
    const del = event.target.closest('[data-delete-source-rule]');
    if(del && !del.disabled) openBulkAction('source-rules','delete',del,[del.dataset.deleteSourceRule]);
  });
  $('source-clients-body').addEventListener('click',e => {
    const row = e.target.closest('[data-client-addr]');
    if(row) selectedSourceAddress = row.dataset.clientAddr;
    const block = e.target.closest('[data-block-source]');
    if(block) { openBlockSourceDialog(block.dataset.blockSource); }
  });
  $('refresh-source-rule').addEventListener('click',async () => {
    const generation = sessionGeneration;
    try {
      const id = editingSourceRuleID;
      const updated = await api('/config');
      if(generation !== sessionGeneration) return;
      const index = id ? (updated.source_access?.rules || []).findIndex(rule => rule.id === id) : -1;
      publishConfig(updated);
      if(id && index < 0) throw sourceError('Source access rule no longer exists. Refresh before editing.');
      openSourceRule(index);
    } catch(error) { errorIn($('source-rule-form'),error); }
  });
  $('source-rule-dialog').addEventListener('close',() => { sourceRuleDraft = null; editingSourceRuleID = ''; });
  $('source-rule-form').addEventListener('submit',async e => {
    e.preventDefault();
    const generation = sessionGeneration;
    if(!sourceRuleDraft) return;
    const button = e.target.querySelector('button[type="submit"]');
    if(button.disabled) return;
    return withBusy(button,async () => {
    try {
      const next = structuredClone(sourceRuleDraft);
      const rule = {id:$('src-rule-id').value.trim(),name:$('src-rule-name').value.trim(),action:$('src-rule-action').value,enabled:$('src-rule-enabled').checked,cidrs:lines($('src-rule-cidrs').value),note:$('src-rule-note').value.trim()};
      if(editingSourceRuleID) {
        const index = next.source_access.rules.findIndex(r => r.id === editingSourceRuleID);
        if(index < 0) throw sourceError('Source access rule no longer exists. Refresh before editing.');
        next.source_access.rules[index] = rule;
      } else { next.source_access.rules.push(rule); }
      await save(next);
      $('source-rule-dialog').close();
    } catch(error) { errorIn(e.target,error); }
    });
  });
  $('block-source-form').addEventListener('submit',async e => {
    e.preventDefault();
    if(!blockSourceContext) return;
    return withBusy($('confirm-block-source'), async () => {
    clearError(e.target);
    const {address,revision} = blockSourceContext;
    try {
      const res = await api('/source-clients/block',{method:'POST',body:JSON.stringify({revision,address})});
      if(generation !== sessionGeneration) return;
      selectedSourceAddress = address;
      $('block-source-dialog').close();
      notice({source:'Blocked {address}; disconnected {count} connection(s).',values:{address,count:res.disconnected}});
      publishConfig(res.config);
      await loadSourceClients(true);
      if(generation !== sessionGeneration) return;
      focusSource(address);
    } catch(error) { errorIn(e.target,error); }
    });
  });
  $('capacity-max-total').addEventListener('input', onCapacityEdit);
  $('capacity-max-per-source').addEventListener('input', onCapacityEdit);
  $('capacity-max-total').addEventListener('change', onCapacityEdit);
  $('capacity-max-per-source').addEventListener('change', onCapacityEdit);
  $('source-capacity-form').addEventListener('submit', async e => {
    e.preventDefault();
    const generation = sessionGeneration;
    if(capacitySaving) return;
    return withBusy($('capacity-save'),async () => {
    clearError(e.target);
    const totalVal = Number($('capacity-max-total').value);
    const perSourceVal = Number($('capacity-max-per-source').value);
    try {
      if(!Number.isInteger(totalVal) || totalVal < 1 || totalVal > 65536) {
        throw sourceError('Total connection limit must be between 1 and 65536.');
      }
      if(!Number.isInteger(perSourceVal) || perSourceVal < 1 || perSourceVal > 65536) {
        throw sourceError('Per-source connection limit must be between 1 and 65536.');
      }
      if(perSourceVal > totalVal) {
        throw sourceError('Per-source connection limit must not exceed total connection limit.');
      }
      capacitySaving = true;
      renderCapacity(false);
      const next = cloneConfig();
      if(capacityDraftRevision !== null && capacityDraftRevision !== undefined) {
        next.revision = capacityDraftRevision;
      }
      next.connection_limits = {
        max_connections: totalVal,
        max_connections_per_source: perSourceVal,
      };
      const updated = await api('/config', {method:'PUT', body:JSON.stringify(next)});
      if(generation !== sessionGeneration) return;
      capacityDirty = false;
      capacityDraftRevision = updated.revision;
      publishConfig(updated);
      await poll();
      if(currentPage === 'sources') await loadSourceClients(true);
      if(generation !== sessionGeneration) return;
      notice('Connection capacity saved. New connections will use the updated limits.');
    } catch(error) {
      errorIn(e.target, error);
    } finally {
      if(generation === sessionGeneration) { capacitySaving = false; renderCapacity(false); }
    }
    }).finally(() => { if(generation === sessionGeneration) renderCapacity(false); });
  });
  $('capacity-reload').addEventListener('click', async () => {
    const generation = sessionGeneration;
    if(capacitySaving) return;
    clearError($('source-capacity-form'));
    try {
      const updated = await api('/config');
      if(generation !== sessionGeneration) return;
      capacityDirty = false;
      capacityDraftRevision = updated.revision;
      publishConfig(updated);
      if(currentPage === 'sources') await loadSourceClients(true);
      if(generation !== sessionGeneration) return;
      notice('Connection limits reloaded.');
    } catch(error) {
      errorIn($('source-capacity-form'), error);
    }
  });
  $('outbound-list').addEventListener('click',async e => {
    const edit = e.target.closest('[data-edit-outbound]'); if(edit) { openOutbound(Number(edit.dataset.editOutbound)); return; }
    const license = e.target.closest('[data-license]'); if(license) { licenseID = license.dataset.license; $('license-value').value = ''; clearError($('license-form')); openModal($('license-dialog'),license,$('add-outbound')); return; }
    const del = e.target.closest('[data-delete-outbound]');
    if(del) { openDeleteOutbound(Number(del.dataset.deleteOutbound)); return; }
    const action = e.target.closest('[data-action]'); if(!action) return;
    const key = `${action.dataset.id}/${action.dataset.action}`;
    if(pendingOutboundActions.has(key)) return;
    pendingOutboundActions.add(key);
    action.disabled = true;
    try { await api(`/outbounds/${encodeURIComponent(action.dataset.id)}/${action.dataset.action}`,{method:'POST',body:'{}'}); notice('Outbound action completed.'); await pollStatuses(); }
    catch(error) { notice(error,true); }
    finally { pendingOutboundActions.delete(key); renderOutbounds(); }
  });
  $('delete-outbound-dialog').addEventListener('close',() => { deletingOutbound = null; });
  $('delete-outbound-form').addEventListener('submit',async e => {
    e.preventDefault(); if(!deletingOutbound) return;
    const generation = sessionGeneration;
    const {id,config:c} = deletingOutbound, button = e.target.querySelector('button.primary');
    return withBusy(button, async () => {
    try {
      const updated = await api(`/outbounds/${encodeURIComponent(id)}`,{method:'DELETE',body:JSON.stringify({revision:c.revision,replacement:$('delete-replacement-section').hidden ? '' : $('delete-replacement').value})});
      if(generation !== sessionGeneration) return;
      $('delete-outbound-dialog').close(); publishConfig(updated); notice('Outbound deleted. Referenced routes now use the replacement you selected.'); await poll();
    } catch(error) { errorIn(e.target,error); }
    });
  });
  $('docker-form').addEventListener('submit',async e => {
    e.preventDefault();
    try {
      dockerBundle = await api('/integrations/docker',{method:'POST',body:JSON.stringify({proxy_url:$('docker-proxy-url').value.trim(),no_proxy:$('docker-no-proxy').value})});
      $('docker-error').textContent = ''; delete $('docker-error').dataset.errorSource;
      renderDockerExport(); notice('Docker settings generated. Apply them in Docker to enable Proxy connections.');
    } catch(error) { displayError($('docker-error'),error); }
  });
  $('docker-target').addEventListener('change',renderDockerExport);
  $('docker-copy').addEventListener('click',async () => {
    if(!$('docker-config').value) return;
    try { await navigator.clipboard.writeText($('docker-config').value); notice('Configuration copied.'); }
    catch(_) { notice(sourceError('Could not copy. Select and copy the configuration manually.'),true); }
  });
  $('docker-download').addEventListener('click',() => {
    if(!$('docker-config').value) return;
    const names = {daemon:'daemon.json',client:'config.json',env:'rillway-docker.env',compose:'compose.yaml'};
    const blob = new Blob([$('docker-config').value],{type:'text/plain;charset=utf-8'});
    const url = URL.createObjectURL(blob), link = document.createElement('a');
    link.href = url; link.download = names[$('docker-target').value]; link.click(); URL.revokeObjectURL(url);
  });
  $('license-form').addEventListener('submit',async e => {
    e.preventDefault(); const value = $('license-value').value.trim(); $('license-value').value = '';
    try { await api(`/outbounds/${encodeURIComponent(licenseID)}/license`,{method:'POST',body:JSON.stringify({value})}); $('license-dialog').close(); notice('WARP+ license key applied.'); await pollStatuses(); } catch(error) { errorIn(e.target,error); }
  });
  window.addEventListener('hashchange',() => navigate(location.hash.slice(1)));
  document.addEventListener('visibilitychange',() => {
    restartSourcePolling();
    if(!document.hidden && currentPage === 'sources') loadSourceClients();
    if(document.hidden) {
      stopOutboundPolling();
    } else if(active && currentPage === 'outbounds' && outboundRefreshSeconds !== 0) {
      const elapsed = Date.now() - outboundLastLoadedAt;
      const intervalMs = outboundRefreshSeconds * 1000;
      if(elapsed >= intervalMs) {
        pollStatuses(false);
      } else {
        scheduleOutboundPolling(intervalMs - elapsed);
      }
    }
  });
}
function startUI() {
  bindUI();
  navigate(location.hash.slice(1));
  i18n.ready.then(() => switchLocale(i18n.locale));
  restartFlowPolling();
  if(token) { const generation = ++sessionGeneration; load().catch(e => { if(generation !== sessionGeneration) return; displayError($('login-error'),e); logout(); }); }
}
if(document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded',startUI,{once:true});
} else {
  startUI();
}
