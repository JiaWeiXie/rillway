'use strict';
const $ = id => document.getElementById(id);
const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const lines = value => value.split(/[\n,]/).map(s => s.trim()).filter(Boolean);
const bytes = n => { n = Number(n) || 0; const units = ['B','KiB','MiB','GiB','TiB']; let i = 0; while(n >= 1024 && i < units.length-1){ n /= 1024; i++; } return `${n.toFixed(i ? 1 : 0)} ${units[i]}`; };
const rate = n => `${bytes(n)}/s`;
const typeName = t => ({direct:'直接連線',warp:'Cloudflare WARP',wireguard:'WireGuard',tailscale:'Tailscale',tsnet:'Tailscale',socks5:'SOCKS5',http:'HTTP Proxy'}[t] || t);
const stateKey = t => String(t || '').toLowerCase();
const stateName = t => ({ready:'可用',connected:'已連線',running:'執行中',starting:'啟動中',stopped:'已停止',disconnected:'已斷線',disabled:'已停用',error:'發生錯誤',unavailable:'無法使用',needslogin:'等待登入',needsmachineauth:'等待管理員批准',needs_login:'等待登入',needs_auth:'等待登入',unsupported_account:'帳號不支援',unsupported_client:'版本不支援'}[stateKey(t)] || t || '尚未取得狀態');
let token = ''; try { token = sessionStorage.getItem('rillway-token') || ''; } catch (_) {}
let cfg, statuses = [], flows = [], active = false, polling = false, statusPolling = false, outboundIndex = -1, ruleIndex = -1, licenseID = '';

async function api(path, options = {}) {
  const headers = {'Authorization': `Bearer ${token}`, ...options.headers};
  if(options.body !== undefined) headers['Content-Type'] = 'application/json';
  const response = await fetch(`/api/v1${path}`, {...options, headers, credentials:'omit', cache:'no-store', redirect:'error'});
  let data; try { data = await response.json(); } catch (_) { data = {}; }
  if(!response.ok) {
    const error = new Error(data.error || `服務回應 ${response.status}`); error.status = response.status;
    if(response.status === 401 && active) logout();
    throw error;
  }
  return data;
}
function notice(message, failure = false) { $('notice').textContent = message; $('notice').className = `notice${failure ? ' failure' : ''}`; $('notice').hidden = false; }
function errorIn(form, e) { form.querySelector('.form-error').textContent = e.message; }
function clearError(form) { const error = form.querySelector('.form-error'); if(error) error.textContent = ''; }
function connection(online) { $('connection-dot').className = `dot ${online ? 'online' : 'offline'}`; $('connection-label').textContent = online ? '服務已連線' : '暫時無法連線'; }
function cloneConfig() { return structuredClone(cfg); }
function candidates() { return (cfg?.outbounds || []).filter(o => o.enabled && o.public_internet && ['direct','warp','wireguard'].includes(o.type)); }
function options(selected, withAdaptive = false) { return (withAdaptive ? `<option value="@adaptive"${selected === '@adaptive' ? ' selected' : ''}>自適應出口</option>` : '') + (cfg.outbounds || []).map(o => `<option value="${esc(o.id)}"${o.id === selected ? ' selected' : ''}>${esc(o.id)} · ${esc(typeName(o.type))}${o.enabled ? '' : '（停用）'}</option>`).join(''); }
function candidateBoxes(container, selected, prefix) { container.innerHTML = candidates().map(o => `<label><input type="checkbox" name="${prefix}" value="${esc(o.id)}"${selected.includes(o.id) ? ' checked' : ''}>${esc(o.id)}</label>`).join('') || '<p class="hint">先啟用一個可連到公網的出口。</p>'; }
function checked(container) { return [...container.querySelectorAll('input:checked')].map(i => i.value); }

async function load() {
  cfg = await api('/config');
  active = true; $('login').hidden = true; $('app').hidden = false;
  renderConfig(); await poll(); pollStatuses();
}
async function save(next) {
  cfg = await api('/config', {method:'PUT', body:JSON.stringify(next)});
  renderConfig(); notice('設定已儲存。新連線會使用更新後的規則。'); await poll();
}
function renderConfig() {
  $('revision').textContent = `設定版本 ${cfg.revision}`;
  $('adaptive-toggle').checked = !!cfg.adaptive.enabled;
  $('adaptive-description').textContent = cfg.adaptive.enabled ? '依可用性與測試結果選擇新連線出口；手動規則仍優先。' : '關閉時依分流規則及預設出口連線。';
  candidateBoxes($('adaptive-candidates'), cfg.adaptive.candidates || [], 'global-candidate');
  $('default-outbound').innerHTML = options(cfg.default_outbound);
  $('pac-address').value = cfg.pac.proxy_address || '';
  $('pac-domains').value = (cfg.pac.bypass_domains || []).join('\n');
  $('pac-cidrs').value = (cfg.pac.bypass_cidrs || []).join('\n');
  $('pac-hint').textContent = `PAC 服務監聽：${cfg.listeners.pac || '未設定'}。Mac 需使用能連到 Ubuntu 主機的位址。`;
  $('listeners').innerHTML = Object.entries(cfg.listeners).map(([k,v]) => `<dt>${esc({http:'HTTP Proxy',socks5:'SOCKS5',admin:'管理介面',pac:'PAC'}[k] || k)}</dt><dd>${esc(v || '未設定')}</dd>`).join('');
  $('config-json').value = JSON.stringify(cfg, null, 2);
  renderRules(); renderOutbounds();
}
async function poll() {
  if(!active || polling) return;
  polling = true;
  try {
    const snapshot = await api('/stats');
    flows = Array.isArray(snapshot) ? snapshot : snapshot.flows || [];
    $('applied-at').textContent = snapshot.applied_at ? `生效於 ${new Date(snapshot.applied_at).toLocaleString()}` : '';
    if(snapshot.config_revision !== undefined) $('revision').textContent = `生效版本 ${snapshot.config_revision}`;
    renderFlows(); connection(true);
  } catch (e) { connection(false); if(e.status !== 401) notice(e.message, true); }
  finally { polling = false; }
}
async function pollStatuses() {
  if(!active || statusPolling) return;
  statusPolling = true;
  try { const state = await api('/outbounds'); statuses = Array.isArray(state) ? state : state.outbounds || []; renderOutbounds(); }
  catch(e) { if(e.status !== 401) notice(e.message,true); }
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
    const host = f.host || f.domain || f.ip || '未提供';
    const ip = f.ip ? `${f.ip}${f.family ? ` · ${f.family}` : ''}` : '目的 IP 未提供，由上游解析';
    const sum = (Number(f.upload_bytes)||0)+(Number(f.download_bytes)||0);
    const key = String(f.id ?? `${host}:${f.port}:${index}`);
    let row = rows.get(key);
    if(!row) { row = document.createElement('tr'); row.dataset.flowKey = key; for(let i = 0; i < 8; i++) row.append(document.createElement('td')); row.cells[7].innerHTML = '<button class="table-action">設定出口</button>'; }
    rows.delete(key);
    const cells = [`<span class="domain">${esc(host)}${f.port ? `:${esc(f.port)}` : ''}</span><span class="sub">${esc(ip)}</span>`, `<span class="badge">${esc(f.outbound || '未指定')}</span><span class="sub">${esc(f.rule || f.rule_id || '預設規則')}</span>`, rate(f.download_bytes_per_second || f.download_rate), rate(f.upload_bytes_per_second || f.upload_rate), bytes(sum), `${Number(f.connect_ms || 0).toFixed(1)} ms`, `<span class="badge${flowActive(f) ? ' good' : ''}">${flowActive(f) ? '連線中' : '已結束'}</span>`];
    cells.forEach((html,i) => { if(row.cells[i].innerHTML !== html) row.cells[i].innerHTML = html; if(i >= 2 && i <= 5) row.cells[i].className = 'numeric'; });
    row.cells[7].firstElementChild.dataset.flow = index;
    if(body.rows[position] !== row) body.insertBefore(row,body.rows[position] || null);
  });
  rows.forEach(row => row.remove());
}
function renderRules() {
  $('rules').innerHTML = (cfg.rules || []).map((r,index) => {
    const targets = [...(r.domains || []), ...(r.suffixes || []).map(s => `*.${s.replace(/^\./,'')}`), ...(r.cidrs || [])];
    return `<tr><td><strong>${esc(r.id)}</strong><span class="sub">${esc(targets.join(', ') || '未設定比對目標')}</span></td><td><span class="badge">${esc(r.adaptive ? '自適應' : r.outbound)}</span>${r.adaptive ? `<span class="sub">${esc((r.candidates || []).join(', '))}</span>` : ''}</td><td>${esc(r.family || '雙棧')}</td><td><button class="table-action" data-edit-rule="${index}">編輯</button> <button class="table-action" data-up-rule="${index}"${index === 0 ? ' disabled' : ''} aria-label="將 ${esc(r.id)} 往上移">↑</button> <button class="table-action danger" data-delete-rule="${index}">刪除</button></td></tr>`;
  }).join('') || '<tr><td colspan="4" class="muted">尚無規則。新增網域或 IP 規則，指定它的出口。</td></tr>';
}
function renderOutbounds() {
  if(!cfg) return;
  $('outbound-list').innerHTML = cfg.outbounds.map((o,index) => {
    const s = statuses.find(s => s.id === o.id) || {};
    const good = ['ready','connected','running'].includes(stateKey(s.state)), bad = ['error','unavailable','unsupported_account','unsupported_client'].includes(stateKey(s.state));
    const authURL = typeof s.auth_url === 'string' && /^https:\/\//i.test(s.auth_url) ? s.auth_url : '';
    const actionButtons = o.type === 'warp' ? [['connect','連線'],['disconnect','斷線'],['register','註冊'],['verify','驗證出口']] : ['tailscale','tsnet'].includes(o.type) ? [['connect','連線'],['disconnect','斷線'],['login','登入'],['logout','登出']] : o.type === 'wireguard' ? [['connect','連線'],['disconnect','斷線']] : [];
    return `<article class="outbound-card"><div><div class="outbound-heading"><h3>${esc(o.id)}</h3><span class="badge${good ? ' good' : bad ? ' bad' : ' warn'}">${esc(o.enabled ? stateName(s.state) : '已停用')}</span></div><p class="detail">${esc(s.detail || typeName(o.type))}</p><div class="outbound-meta"><span>${esc(typeName(o.type))}</span><span>${o.public_internet ? '可作為公網出口' : '固定／私網出口'}</span>${s.account ? `<span>帳號：${esc(s.account)}</span>` : ''}${s.version ? `<span>版本 ${esc(s.version)}</span>` : ''}${s.mode ? `<span>模式 ${esc(s.mode)}</span>` : ''}${o.type === 'warp' ? `<span>Proxy listener：${s.listener ? '已啟動' : '未就緒'}</span>` : ''}${s.verified_at && !s.verified_at.startsWith('0001-') ? `<span>上次驗證 ${esc(new Date(s.verified_at).toLocaleString())}</span>` : ''}</div>${authURL ? `<p class="hint"><a href="${esc(authURL)}" target="_blank" rel="noopener noreferrer">開啟 Tailscale 登入頁</a></p>` : ''}</div><div class="outbound-actions">${actionButtons.map(([action,label]) => `<button class="quiet" data-action="${action}" data-id="${esc(o.id)}">${label}</button>`).join('')}${o.type === 'warp' ? `<button class="quiet" data-license="${esc(o.id)}">WARP+ 授權</button>` : ''}<button class="quiet" data-edit-outbound="${index}">編輯</button><button class="danger" data-delete-outbound="${index}">刪除</button></div></article>`;
  }).join('') || '<div class="empty"><h3>先建立一個出口</h3><p>可從直接連線開始，再加入 WARP 或 WireGuard。</p></div>';
}

function navigate(page) {
  const names = {overview:['連線總覽','看見流量，決定去向'],outbounds:['出口與 VPN','管理每一條對外連線'],rules:['分流規則','為目的地選擇合適的路'],settings:['連線設定','連接瀏覽器與你的網路']};
  if(!names[page]) page = 'overview';
  for(const el of document.querySelectorAll('.page')) el.hidden = el.id !== `page-${page}`;
  for(const el of document.querySelectorAll('[data-page]')) el.classList.toggle('selected',el.dataset.page === page);
  [$('page-title').textContent,$('page-context').textContent] = names[page];
}
function logout() { active = false; token = ''; try { sessionStorage.removeItem('rillway-token'); } catch (_) {} $('app').hidden = true; $('login').hidden = false; $('token').value = ''; }
function openOutbound(index = -1) {
  outboundIndex = index; const o = index >= 0 ? cfg.outbounds[index] : {type:'wireguard',enabled:true};
  $('outbound-form-title').textContent = index >= 0 ? '編輯出口' : '新增出口';
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

$('login-form').addEventListener('submit',async e => { e.preventDefault(); token = $('token').value.trim(); $('login-error').textContent = ''; try { await load(); sessionStorage.setItem('rillway-token',token); $('token').value = ''; } catch(error) { $('login-error').textContent = error.message; } });
$('logout').addEventListener('click',logout);
$('refresh').addEventListener('click',() => load().catch(e => notice(e.message,true)));
$('flow-search').addEventListener('input',renderFlows);
document.querySelectorAll('[data-page]').forEach(b => b.addEventListener('click',() => { navigate(b.dataset.page); history.replaceState(null,'',`#${b.dataset.page}`); }));
document.querySelectorAll('[data-close]').forEach(b => b.addEventListener('click',() => $(b.dataset.close).close()));
$('license-dialog').addEventListener('close',() => { $('license-value').value = ''; licenseID = ''; });
$('add-outbound').addEventListener('click',() => openOutbound());
$('out-type').addEventListener('change',outboundFields);
$('add-rule').addEventListener('click',() => openRule());
$('rule-outbound').addEventListener('change',() => { $('rule-candidates').hidden = $('rule-outbound').value !== '@adaptive'; });
$('flows').addEventListener('click',e => { const b = e.target.closest('[data-flow]'); if(b) openRule(-1,flows[Number(b.dataset.flow)]); });
$('adaptive-toggle').addEventListener('change',async () => { const next = cloneConfig(); next.adaptive.enabled = $('adaptive-toggle').checked; next.adaptive.candidates = checked($('adaptive-candidates')); try { await save(next); } catch(e) { $('adaptive-toggle').checked = cfg.adaptive.enabled; notice(e.message,true); } });
$('adaptive-candidates').addEventListener('change',async () => { const next = cloneConfig(); next.adaptive.candidates = checked($('adaptive-candidates')); try { await save(next); } catch(e) { renderConfig(); notice(e.message,true); } });
$('save-default').addEventListener('click',async () => { const next = cloneConfig(); next.default_outbound = $('default-outbound').value; try { await save(next); } catch(e) { notice(e.message,true); } });
$('pac-form').addEventListener('submit',async e => { e.preventDefault(); const next = cloneConfig(); next.pac.proxy_address = $('pac-address').value.trim(); next.pac.bypass_domains = lines($('pac-domains').value); next.pac.bypass_cidrs = lines($('pac-cidrs').value); try { await save(next); } catch(error) { notice(error.message,true); } });
$('save-json').addEventListener('click',async () => { try { const next = JSON.parse($('config-json').value); await save(next); } catch(e) { notice(e.message,true); } });
$('reset-json').addEventListener('click',() => load().catch(e => notice(e.message,true)));
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
  if(del) { if(!confirm(`刪除規則「${next.rules[index].id}」？`)) return; next.rules.splice(index,1); }
  try { await save(next); } catch(error) { notice(error.message,true); }
});
$('outbound-list').addEventListener('click',async e => {
  const edit = e.target.closest('[data-edit-outbound]'); if(edit) { openOutbound(Number(edit.dataset.editOutbound)); return; }
  const license = e.target.closest('[data-license]'); if(license) { licenseID = license.dataset.license; $('license-value').value = ''; clearError($('license-form')); $('license-dialog').showModal(); return; }
  const del = e.target.closest('[data-delete-outbound]');
  if(del) { const next = cloneConfig(); const index = Number(del.dataset.deleteOutbound); if(!confirm(`刪除出口「${next.outbounds[index].id}」？請先移除引用它的規則。`)) return; next.outbounds.splice(index,1); try { await save(next); } catch(error) { notice(error.message,true); } return; }
  const action = e.target.closest('[data-action]'); if(!action) return;
  action.disabled = true; try { await api(`/outbounds/${encodeURIComponent(action.dataset.id)}/${action.dataset.action}`,{method:'POST',body:'{}'}); notice('出口操作已完成。'); await pollStatuses(); } catch(error) { notice(error.message,true); } finally { action.disabled = false; }
});
$('license-form').addEventListener('submit',async e => {
  e.preventDefault(); const value = $('license-value').value.trim(); $('license-value').value = '';
  try { await api(`/outbounds/${encodeURIComponent(licenseID)}/license`,{method:'POST',body:JSON.stringify({value})}); $('license-dialog').close(); notice('WARP+ 授權碼已套用。'); await pollStatuses(); } catch(error) { errorIn(e.target,error); }
});
navigate(location.hash.slice(1));
setInterval(poll,1000);
setInterval(pollStatuses,10000);
if(token) load().catch(e => { $('login-error').textContent = e.message; logout(); });
