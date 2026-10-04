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
let token = ''; try { token = sessionStorage.getItem('rillway-token') || ''; } catch (_) {}
let currentPage = 'overview', latestSnapshot = {}, lastNotice = null, isOnline = false;
let cfg, statuses = [], flows = [], active = false, polling = false, statusPolling = false, outboundIndex = -1, ruleIndex = -1, licenseID = '';

async function api(path, options = {}) {
  const headers = {'Authorization': `Bearer ${token}`, 'Accept-Language': i18n.locale, ...options.headers};
  if(options.body !== undefined) headers['Content-Type'] = 'application/json';
  let response;
  try { response = await fetch(`/api/v1${path}`, {...options, headers, credentials:'omit', cache:'no-store', redirect:'error'}); }
  catch (_) { throw sourceError('Could not reach the management service. Try again.'); }
  let data; try { data = await response.json(); } catch (_) { data = {}; }
  if(!response.ok) {
    const error = new Error(data.error || t('Service returned {status}',{status:response.status})); error.status = response.status;
    if(typeof data.error_source === 'string') error.source = data.error_source;
    if(response.status === 401 && active) logout();
    throw error;
  }
  return data;
}
function sourceError(source) { const error = new Error(t(source)); error.source = source; return error; }
function errorText(error) { return error.source ? t(error.source) : error.message; }
function notice(message, failure = false) {
  lastNotice = {message,failure};
  $('notice').textContent = typeof message === 'string' ? t(message) : errorText(message);
  $('notice').className = `notice${failure ? ' failure' : ''}`; $('notice').hidden = false;
}
function displayError(node,error) {
  if(error.source) node.dataset.errorSource = error.source;
  else delete node.dataset.errorSource;
  node.textContent = errorText(error);
}
function errorIn(form,error) { displayError(form.querySelector('.form-error'),error); }
function clearError(form) { const error = form.querySelector('.form-error'); if(error) { error.textContent = ''; delete error.dataset.errorSource; } }
function connection(online) { isOnline = online; $('connection-dot').className = `dot ${online ? 'online' : 'offline'}`; $('connection-label').textContent = t(online ? 'Service connected' : 'Service unavailable'); }
function cloneConfig() { return structuredClone(cfg); }
function candidates() { return (cfg?.outbounds || []).filter(o => o.enabled && o.public_internet && ['direct','warp','wireguard'].includes(o.type)); }
function options(selected, withAdaptive = false) { return (withAdaptive ? `<option value="@adaptive"${selected === '@adaptive' ? ' selected' : ''}>${et('Adaptive routing')}</option>` : '') + (cfg.outbounds || []).map(o => `<option value="${esc(o.id)}"${o.id === selected ? ' selected' : ''}>${esc(o.id)} · ${esc(typeName(o.type))}${o.enabled ? '' : t(' (disabled)')}</option>`).join(''); }
function candidateBoxes(container, selected, prefix) { container.innerHTML = candidates().map(o => `<label><input type="checkbox" name="${prefix}" value="${esc(o.id)}"${selected.includes(o.id) ? ' checked' : ''}>${esc(o.id)}</label>`).join('') || `<p class="hint">${et('Enable an outbound with Internet access first.')}</p>`; }
function checked(container) { return [...container.querySelectorAll('input:checked')].map(i => i.value); }

async function load() {
  cfg = await api('/config');
  active = true; $('login').hidden = true; $('app').hidden = false;
  renderConfig(); await poll(); pollStatuses();
}
async function save(next) {
  cfg = await api('/config', {method:'PUT', body:JSON.stringify(next)});
  renderConfig(); notice('Configuration saved. New connections will use the updated rules.'); await poll();
}
function renderConfig() {
  renderConfigText();
  $('adaptive-toggle').checked = !!cfg.adaptive.enabled;
  candidateBoxes($('adaptive-candidates'), cfg.adaptive.candidates || [], 'global-candidate');
  $('default-outbound').innerHTML = options(cfg.default_outbound);
  $('pac-address').value = cfg.pac.proxy_address || '';
  $('pac-domains').value = (cfg.pac.bypass_domains || []).join('\n');
  $('pac-cidrs').value = (cfg.pac.bypass_cidrs || []).join('\n');
  $('config-json').value = JSON.stringify(cfg, null, 2);
  renderRules(); renderOutbounds();
}
async function poll() {
  if(!active || polling) return;
  polling = true;
  try {
    const snapshot = await api('/stats'); latestSnapshot = snapshot;
    flows = Array.isArray(snapshot) ? snapshot : snapshot.flows || [];
    renderRevision();
    renderFlows(); connection(true);
  } catch (e) { connection(false); if(e.status !== 401) notice(e, true); }
  finally { polling = false; }
}
async function pollStatuses() {
  if(!active || statusPolling) return;
  statusPolling = true;
  try { const state = await api('/outbounds'); statuses = Array.isArray(state) ? state : state.outbounds || []; renderOutbounds(); }
  catch(e) { if(e.status !== 401) notice(e,true); }
  finally { statusPolling = false; }
}
function flowActive(f) { return !f.closed || f.closed === '0001-01-01T00:00:00Z'; }
function renderFlows() {
  const query = $('flow-search').value.toLowerCase();
  const visible = flows.map((f,index) => ({f,index})).filter(({f}) => [f.host,f.domain,f.ip,f.outbound].join(' ').toLowerCase().includes(query));
  let up = 0, down = 0, count = 0;
  for(const f of flows) { up += Number(f.upload_bytes_per_second || f.upload_rate) || 0; down += Number(f.download_bytes_per_second || f.download_rate) || 0; if(flowActive(f)) count++; }
  $('upload-rate').textContent = rate(up); $('download-rate').textContent = rate(down); $('active-count').textContent = count;
  $('flows-empty').hidden = visible.length > 0;
  const body = $('flows');
  const rows = new Map([...body.rows].map(row => [row.dataset.flowKey, row]));
  visible.forEach(({f,index}, position) => {
    const host = f.host || f.domain || f.ip || t('Unknown');
    const ip = f.ip ? `${f.ip}${f.family ? ` · ${f.family}` : ''}` : t('Destination IP unknown; resolved upstream');
    const sum = (Number(f.upload_bytes)||0)+(Number(f.download_bytes)||0);
    const key = String(f.id ?? `${host}:${f.port}:${index}`);
    let row = rows.get(key);
    if(!row) { row = document.createElement('tr'); row.dataset.flowKey = key; for(let i = 0; i < 8; i++) row.append(document.createElement('td')); row.cells[7].innerHTML = '<button class="table-action"></button>'; }
    rows.delete(key); row.cells[7].firstElementChild.textContent = t('Set outbound');
    const cells = [`<span class="domain">${esc(host)}${f.port ? `:${esc(f.port)}` : ''}</span><span class="sub">${esc(ip)}</span>`, `<span class="badge">${esc(f.outbound || t('Unspecified'))}</span><span class="sub">${esc(f.rule || f.rule_id || t('Default rule'))}</span>`, rate(f.download_bytes_per_second || f.download_rate), rate(f.upload_bytes_per_second || f.upload_rate), bytes(sum), `${Number(f.connect_ms || 0).toFixed(1)} ms`, `<span class="badge${flowActive(f) ? ' good' : ''}">${et(flowActive(f) ? 'Active' : 'Closed')}</span>`];
    cells.forEach((html,i) => { if(row.cells[i].innerHTML !== html) row.cells[i].innerHTML = html; if(i >= 2 && i <= 5) row.cells[i].className = 'numeric'; });
    row.cells[7].firstElementChild.dataset.flow = index;
    if(body.rows[position] !== row) body.insertBefore(row,body.rows[position] || null);
  });
  rows.forEach(row => row.remove());
}
function renderRules() {
  $('rules').innerHTML = (cfg.rules || []).map((r,index) => {
    const targets = [...(r.domains || []), ...(r.suffixes || []).map(s => `*.${s.replace(/^\./,'')}`), ...(r.cidrs || [])];
    return `<tr><td><strong>${esc(r.id)}</strong><span class="sub">${esc(targets.join(', ') || t('No matching destinations configured'))}</span></td><td><span class="badge">${esc(r.adaptive ? t('Adaptive routing') : r.outbound)}</span>${r.adaptive ? `<span class="sub">${esc((r.candidates || []).join(', '))}</span>` : ''}</td><td>${esc(r.family || t('Dual stack'))}</td><td><button class="table-action" data-edit-rule="${index}">${et('Edit')}</button> <button class="table-action" data-up-rule="${index}"${index === 0 ? ' disabled' : ''} aria-label="${et('Move {name} up',{name:r.id})}">↑</button> <button class="table-action danger" data-delete-rule="${index}">${et('Delete')}</button></td></tr>`;
  }).join('') || `<tr><td colspan="4" class="muted">${et('No rules yet. Add a domain or IP rule to choose its outbound.')}</td></tr>`;
}
function renderOutbounds() {
  if(!cfg) return;
  $('outbound-list').innerHTML = cfg.outbounds.map((o,index) => {
    const s = statuses.find(s => s.id === o.id) || {};
    const good = ['ready','connected','running'].includes(stateKey(s.state)), bad = ['error','unavailable','unsupported_account','unsupported_client'].includes(stateKey(s.state));
    const authURL = typeof s.auth_url === 'string' && /^https:\/\//i.test(s.auth_url) ? s.auth_url : '';
    const actionButtons = o.type === 'warp' ? [['connect','Connect'],['disconnect','Disconnect'],['register','Register'],['verify','Verify outbound']] : ['tailscale','tsnet'].includes(o.type) ? [['connect','Connect'],['disconnect','Disconnect'],['login','Sign in'],['logout','Sign out']] : o.type === 'wireguard' ? [['connect','Connect'],['disconnect','Disconnect']] : [];
    return `<article class="outbound-card"><div><div class="outbound-heading"><h3>${esc(o.id)}</h3><span class="badge${good ? ' good' : bad ? ' bad' : ' warn'}">${esc(o.enabled ? stateName(s.state) : t('Disabled'))}</span></div><p class="detail">${esc(s.detail_source ? t(s.detail_source) : s.detail || typeName(o.type))}</p><div class="outbound-meta"><span>${esc(typeName(o.type))}</span><span>${et(o.public_internet ? 'Internet access' : 'Fixed / private outbound')}</span>${s.account ? `<span>${et('Account: {value}',{value:s.account})}</span>` : ''}${s.version ? `<span>${et('Version {value}',{value:s.version})}</span>` : ''}${s.mode ? `<span>${et('Mode {value}',{value:s.mode})}</span>` : ''}${o.type === 'warp' ? `<span>${et('Proxy listener: {state}',{state:t(s.listener ? 'Listening' : 'Not ready')})}</span>` : ''}${s.verified_at && !s.verified_at.startsWith('0001-') ? `<span>${et('Last verified {date}',{date:i18n.date(s.verified_at)})}</span>` : ''}</div>${authURL ? `<p class="hint"><a href="${esc(authURL)}" target="_blank" rel="noopener noreferrer">${et('Open Tailscale sign-in')}</a></p>` : ''}</div><div class="outbound-actions">${actionButtons.map(([action,label]) => `<button class="quiet" data-action="${action}" data-id="${esc(o.id)}">${et(label)}</button>`).join('')}${o.type === 'warp' ? `<button class="quiet" data-license="${esc(o.id)}">${et('WARP+ license')}</button>` : ''}<button class="quiet" data-edit-outbound="${index}">${et('Edit')}</button><button class="danger" data-delete-outbound="${index}">${et('Delete')}</button></div></article>`;
  }).join('') || `<div class="empty"><h3>${et('Add your first outbound')}</h3><p>${et('Start with Direct, then add WARP or WireGuard.')}</p></div>`;
}

function navigate(page) {
  const names = {overview:['Connections','See your traffic. Choose its path.'],outbounds:['Outbounds','Manage your available connections.'],rules:['Routing rules','Choose how each destination connects.'],settings:['Settings','Connect your browser and network.']};
  if(!names[page]) page = 'overview'; currentPage = page;
  for(const el of document.querySelectorAll('.page')) el.hidden = el.id !== `page-${page}`;
  for(const el of document.querySelectorAll('[data-page]')) el.classList.toggle('selected',el.dataset.page === page);
  [$('page-title').textContent,$('page-context').textContent] = names[page].map(source => t(source));
}
function logout() { active = false; token = ''; try { sessionStorage.removeItem('rillway-token'); } catch (_) {} $('app').hidden = true; $('login').hidden = false; $('token').value = ''; }
function openOutbound(index = -1) {
  outboundIndex = index; const o = index >= 0 ? cfg.outbounds[index] : {type:'wireguard',enabled:true};
  $('outbound-form-title').textContent = t(index >= 0 ? 'Edit outbound' : 'Add outbound');
  const map = {id:'id',type:'type',address:'proxy_address',file:'config_file',hostname:'hostname',state:'state_dir',auth:'auth_key_file',binary:'warp_binary'};
  for(const [element,key] of Object.entries(map)) $(`out-${element}`).value = o[key] || '';
  $('out-id').disabled = index >= 0; $('out-enabled').checked = o.enabled; $('out-public').checked = !!o.public_internet; $('out-dns').value = (o.dns || []).join('\n');
  clearError($('outbound-form')); outboundFields(); $('outbound-dialog').showModal();
}
function outboundFields() {
  const type = $('out-type').value;
  for(const el of document.querySelectorAll('[data-out-field]')) el.hidden = !({proxy:['warp'],wireguard:['wireguard'],tailscale:['tailscale','tsnet'],warp:['warp'],dns:['wireguard','tailscale','tsnet']}[el.dataset.outField] || []).includes(type);
  if(!['wireguard','tailscale','tsnet'].includes(type)) $('out-dns').value = '';
  $('out-public').disabled = ['tailscale','tsnet'].includes(type);
  if($('out-public').disabled) $('out-public').checked = false;
}
function openRule(index = -1, flow = null) {
  ruleIndex = index; const r = index >= 0 ? cfg.rules[index] : {id:`rule-${Date.now().toString(36)}`,outbound:cfg.default_outbound};
  $('rule-id').value = r.id;
  for(const key of ['domains','suffixes','cidrs']) $(`rule-${key}`).value = (r[key] || []).join('\n');
  if(flow) {
    const host = flow.host || flow.domain || flow.ip || '';
    if(host.includes(':') || /^\d+(\.\d+){3}$/.test(host)) $('rule-cidrs').value = `${host}/${host.includes(':') ? '128' : '32'}`;
    else $('rule-domains').value = host;
  }
  $('rule-outbound').innerHTML = options(r.adaptive ? '@adaptive' : r.outbound, true);
  $('rule-family').value = r.family === 'auto' ? '' : r.family || '';
  candidateBoxes($('rule-candidates'), r.candidates || cfg.adaptive.candidates || [], 'rule-candidate');
  $('rule-candidates').hidden = $('rule-outbound').value !== '@adaptive';
  clearError($('rule-form')); $('rule-dialog').showModal();
}

function renderRevision() {
  const revision = latestSnapshot.config_revision ?? cfg?.revision;
  $('revision').textContent = revision === undefined ? '' : t('Active revision {revision}',{revision});
  $('applied-at').textContent = latestSnapshot.applied_at ? t('Applied {date}',{date:i18n.date(latestSnapshot.applied_at)}) : '';
}
function renderConfigText() {
  if(!cfg) return;
  renderRevision();
  $('adaptive-description').textContent = t(cfg.adaptive.enabled ? 'Choose outbounds for new connections using availability and probe results. Explicit rules take priority.' : 'When disabled, connections use routing rules and the default outbound.');
  $('pac-hint').textContent = t('PAC listener: {address}. Use an address your Mac can reach on the Ubuntu host.',{address:cfg.listeners.pac || t('Not configured')});
  $('listeners').innerHTML = Object.entries(cfg.listeners).map(([k,v]) => `<dt>${esc(t({http:'HTTP Proxy',socks5:'SOCKS5',admin:'Management UI',pac:'PAC'}[k] || k))}</dt><dd>${esc(v || t('Not configured'))}</dd>`).join('');
}
function switchLocale(locale) {
  i18n.setLocale(locale);
  navigate(currentPage);
  connection(isOnline);
  if(cfg) {
    renderConfigText(); renderFlows(); renderRules(); renderOutbounds();
    for(const id of ['default-outbound','rule-outbound']) {
      const select = $(id), selected = select.value;
      if(select.options.length) { select.innerHTML = options(selected,id === 'rule-outbound'); select.value = selected; }
    }
  }
  $('outbound-form-title').textContent = t(outboundIndex >= 0 ? 'Edit outbound' : 'Add outbound');
  for(const node of document.querySelectorAll('[data-error-source]')) node.textContent = t(node.dataset.errorSource);
  if(lastNotice) notice(lastNotice.message,lastNotice.failure);
  for(const id of ['adaptive-candidates','rule-candidates']) { const hint = $(id).querySelector('.hint'); if(hint) hint.textContent = t('Enable an outbound with Internet access first.'); }
  if(active) pollStatuses();
}
document.querySelectorAll('[data-locale]').forEach(select => select.addEventListener('change',() => switchLocale(select.value)));

$('login-form').addEventListener('submit',async e => { e.preventDefault(); token = $('token').value.trim(); $('login-error').textContent = ''; delete $('login-error').dataset.errorSource; try { await load(); sessionStorage.setItem('rillway-token',token); $('token').value = ''; } catch(error) { displayError($('login-error'),error); } });
$('logout').addEventListener('click',logout);
$('refresh').addEventListener('click',() => load().catch(e => notice(e,true)));
$('flow-search').addEventListener('input',renderFlows);
document.querySelectorAll('[data-page]').forEach(b => b.addEventListener('click',() => { navigate(b.dataset.page); history.replaceState(null,'',`#${b.dataset.page}`); }));
document.querySelectorAll('[data-close]').forEach(b => b.addEventListener('click',() => $(b.dataset.close).close()));
$('license-dialog').addEventListener('close',() => { $('license-value').value = ''; licenseID = ''; });
$('add-outbound').addEventListener('click',() => openOutbound());
$('out-type').addEventListener('change',outboundFields);
$('add-rule').addEventListener('click',() => openRule());
$('rule-outbound').addEventListener('change',() => { $('rule-candidates').hidden = $('rule-outbound').value !== '@adaptive'; });
$('flows').addEventListener('click',e => { const b = e.target.closest('[data-flow]'); if(b) openRule(-1,flows[Number(b.dataset.flow)]); });
$('adaptive-toggle').addEventListener('change',async () => { const next = cloneConfig(); next.adaptive.enabled = $('adaptive-toggle').checked; next.adaptive.candidates = checked($('adaptive-candidates')); try { await save(next); } catch(e) { $('adaptive-toggle').checked = cfg.adaptive.enabled; notice(e,true); } });
$('adaptive-candidates').addEventListener('change',async () => { const next = cloneConfig(); next.adaptive.candidates = checked($('adaptive-candidates')); try { await save(next); } catch(e) { renderConfig(); notice(e,true); } });
$('save-default').addEventListener('click',async () => { const next = cloneConfig(); next.default_outbound = $('default-outbound').value; try { await save(next); } catch(e) { notice(e,true); } });
$('pac-form').addEventListener('submit',async e => { e.preventDefault(); const next = cloneConfig(); next.pac.proxy_address = $('pac-address').value.trim(); next.pac.bypass_domains = lines($('pac-domains').value); next.pac.bypass_cidrs = lines($('pac-cidrs').value); try { await save(next); } catch(error) { notice(error,true); } });
$('save-json').addEventListener('click',async () => { try { const next = JSON.parse($('config-json').value); await save(next); } catch(e) { notice(e instanceof SyntaxError ? sourceError('Invalid JSON. Check the configuration syntax.') : e,true); } });
$('reset-json').addEventListener('click',() => load().catch(e => notice(e,true)));
$('outbound-form').addEventListener('submit',async e => {
  e.preventDefault(); const next = cloneConfig(); const original = outboundIndex >= 0 ? next.outbounds[outboundIndex] : {};
  const o = {...original,id:$('out-id').value.trim(),type:$('out-type').value,enabled:$('out-enabled').checked,public_internet:$('out-public').checked,proxy_address:$('out-address').value.trim(),config_file:$('out-file').value.trim(),hostname:$('out-hostname').value.trim(),state_dir:$('out-state').value.trim(),auth_key_file:$('out-auth').value.trim(),warp_binary:$('out-binary').value.trim(),dns:lines($('out-dns').value)};
  if(o.type !== 'warp') { o.proxy_address = ''; o.warp_binary = ''; }
  if(o.type !== 'wireguard') o.config_file = '';
  if(!['tailscale','tsnet'].includes(o.type)) { o.hostname = ''; o.state_dir = ''; o.auth_key_file = ''; }
  if(outboundIndex >= 0) next.outbounds[outboundIndex] = o; else next.outbounds.push(o);
  try { await save(next); $('outbound-dialog').close(); } catch(error) { errorIn(e.target,error); }
});
$('rule-form').addEventListener('submit',async e => {
  e.preventDefault(); const next = cloneConfig(); const adaptive = $('rule-outbound').value === '@adaptive';
  const r = {id:$('rule-id').value.trim(),domains:lines($('rule-domains').value),suffixes:lines($('rule-suffixes').value),cidrs:lines($('rule-cidrs').value),family:$('rule-family').value,adaptive,outbound:adaptive ? '' : $('rule-outbound').value,candidates:adaptive ? checked($('rule-candidates')) : []};
  if(ruleIndex >= 0) next.rules[ruleIndex] = r; else next.rules = [r,...(next.rules || [])];
  try { await save(next); $('rule-dialog').close(); } catch(error) { errorIn(e.target,error); }
});
$('rules').addEventListener('click',async e => {
  const edit = e.target.closest('[data-edit-rule]'); if(edit) { openRule(Number(edit.dataset.editRule)); return; }
  const up = e.target.closest('[data-up-rule]'), del = e.target.closest('[data-delete-rule]'); if(!up && !del) return;
  const next = cloneConfig(); const index = Number(up ? up.dataset.upRule : del.dataset.deleteRule);
  if(up && index > 0) [next.rules[index-1],next.rules[index]] = [next.rules[index],next.rules[index-1]];
  if(del) { if(!confirm(t('Delete rule "{name}"?',{name:next.rules[index].id}))) return; next.rules.splice(index,1); }
  try { await save(next); } catch(error) { notice(error,true); }
});
$('outbound-list').addEventListener('click',async e => {
  const edit = e.target.closest('[data-edit-outbound]'); if(edit) { openOutbound(Number(edit.dataset.editOutbound)); return; }
  const license = e.target.closest('[data-license]'); if(license) { licenseID = license.dataset.license; $('license-value').value = ''; clearError($('license-form')); $('license-dialog').showModal(); return; }
  const del = e.target.closest('[data-delete-outbound]');
  if(del) { const next = cloneConfig(); const index = Number(del.dataset.deleteOutbound); if(!confirm(t('Delete outbound "{name}"? Remove any rules that reference it first.',{name:next.outbounds[index].id}))) return; next.outbounds.splice(index,1); try { await save(next); } catch(error) { notice(error,true); } return; }
  const action = e.target.closest('[data-action]'); if(!action) return;
  action.disabled = true; try { await api(`/outbounds/${encodeURIComponent(action.dataset.id)}/${action.dataset.action}`,{method:'POST',body:'{}'}); notice('Outbound action completed.'); await pollStatuses(); } catch(error) { notice(error,true); } finally { action.disabled = false; }
});
$('license-form').addEventListener('submit',async e => {
  e.preventDefault(); const value = $('license-value').value.trim(); $('license-value').value = '';
  try { await api(`/outbounds/${encodeURIComponent(licenseID)}/license`,{method:'POST',body:JSON.stringify({value})}); $('license-dialog').close(); notice('WARP+ license key applied.'); await pollStatuses(); } catch(error) { errorIn(e.target,error); }
});
navigate(location.hash.slice(1));
i18n.ready.then(() => switchLocale(i18n.locale));
setInterval(poll,1000);
setInterval(pollStatuses,10000);
if(token) load().catch(e => { displayError($('login-error'),e); logout(); });
