'use strict';
// Contract tests for the shipped, dependency-free browser implementation.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const web = path.join(__dirname, '..', 'web');
const catalog = JSON.parse(fs.readFileSync(path.join(web, 'locales.json'), 'utf8'));
const helper = fs.readFileSync(path.join(web, 'i18n.js'), 'utf8');
const app = fs.readFileSync(path.join(web, 'app.js'), 'utf8');
const resourceMessages = [
  ["Resource control is unavailable. Upgrade the installed service to enable it.", "資源控制無法使用；請升級已安裝的服務以啟用此功能。"],
  [
    "Resource control did not respond. Read current settings before retrying.",
    "資源控制未回應。請先讀取目前設定再重試。"
  ],
  [
    "WARP logging control is unavailable. Upgrade the installed service to enable it.",
    "WARP 記錄控制無法使用。請升級已安裝的服務以啟用。"
  ],
  [
    "Could not read WARP logging settings.",
    "無法讀取 WARP 記錄設定。"
  ],
  [
    "Invalid WARP log level. Allowed values: inherit, warning, info, debug.",
    "WARP 記錄層級無效。允許的值為 inherit、warning、info、debug。"
  ],
  [
    "Could not apply WARP logging settings. Refresh settings before retrying.",
    "無法套用 WARP 記錄設定。請重新整理設定後再重試。"
  ],
  [
    "WARP logging control requires a Linux systemd installation.",
    "WARP 記錄控制需要 Linux systemd 安裝環境。"
  ],
  [
    "Invalid WARP log level.",
    "WARP 記錄層級無效。"
  ],
  [
    "Could not read saved WARP log settings. Current settings are unchanged.",
    "無法讀取已儲存的 WARP 記錄設定。目前設定未變更。"
  ],
  [
    "Could not save WARP log settings. Current settings are unchanged.",
    "無法儲存 WARP 記錄設定。目前設定未變更。"
  ],
  [
    "Could not apply WARP log settings. The previous settings were restored.",
    "無法套用 WARP 記錄設定。已還原先前的設定。"
  ],
  [
    "Invalid resource control request.",
    "資源控制請求無效。"
  ],
  [
    "Service resource control requires a Linux systemd installation.",
    "服務資源控制需要 Linux systemd 安裝環境。"
  ],
  [
    "WARP log settings could not be saved or restored. Read current settings before retrying.",
    "無法儲存或還原 WARP 記錄設定。請先讀取目前設定再重試。"
  ]
];
for(const [source,translation] of resourceMessages) {
  assert.equal(catalog.en[source],source);
  assert.equal(catalog["zh-Hant"][source],translation);
}

function element(id = '') {
  const el = {id, value:'', checked:false, hidden:false, open:false, disabled:false,
    textContent:'', innerHTML:'', dataset:{}, options:[], isConnected:true,
    attributes:{}, classList:{toggle(){}}, events:{}, eventCounts:{},
    addEventListener(evt,cb){this.events[evt]=cb;this.eventCounts[evt]=(this.eventCounts[evt]||0)+1;},
    getAttribute(name){return this.attributes[name] ?? null;},
    setAttribute(name,value){this.attributes[name] = value;},
    showModal(){this.open=true;},close(){this.open=false;},contains(){return false;}, querySelector(){return null;}, querySelectorAll(){return [];}};
  return el;
}
function storage(initial = {}) {
  const values = new Map(Object.entries(initial));
  return {values,getItem(key){return values.get(key) ?? null;},
    setItem(key,value){values.set(key,String(value));},removeItem(key){values.delete(key);}};
}
async function browser(initialLocale, preferences = {}) {
  const elements = new Map();
  const get = id => {
    if(!elements.has(id)) elements.set(id,element(id));
    return elements.get(id);
  };
  const label = {textContent:' Management token ',parentElement:{tagName:'LABEL'},isConnected:true};
  const userText = {textContent:'Management token',parentElement:{tagName:'TEXTAREA'},isConnected:true};
  const input = get('token');
  input.value = '公司 🚀 👨‍👩‍👧‍👦 🇹🇼 <script>';
  input.attributes.placeholder = 'Search domain, IP, or outbound';
  const selectors = [get('login-language'),get('sidebar-language')];
  const glossaryTerms = ['DNS', 'Proxy', 'WARP+'].map(title => {
    const term=element(); term.textContent=title; return term;
  });
  glossaryTerms[0].dataset.glossaryKeywords='dns 名稱 查詢 解析 網域';
  glossaryTerms[1].dataset.glossaryKeywords='proxy 代理 瀏覽器 browser';
  glossaryTerms[2].dataset.glossaryKeywords='warp+ license 授權 付費';
  const glossaryCopy={textContent:'Set your browser to use Rillway. Only traffic sent through this proxy appears in Connections.',parentElement:{tagName:'P'},isConnected:true};
  glossaryTerms[1].contains=node=>node===glossaryCopy;
  const glossarySections=glossaryTerms.map(term => ({hidden:false,querySelectorAll:()=>[term]}));
  const documentEvents = {}, documentEventCounts = {}, windowEvents = {};
  const document = {
    readyState:'loading', documentElement:{lang:'en'}, title:'', hidden:false, activeElement:null,
    addEventListener(event,callback){
      documentEvents[event]=callback;
      documentEventCounts[event]=(documentEventCounts[event]||0)+1;
    },
    body:{querySelectorAll(){return [input];}},
    getElementById:get,
    createTreeWalker(){let index=-1; const nodes=[label,userText,glossaryCopy];return {
      nextNode(){index++;this.currentNode=nodes[index];return index<nodes.length;}};},
    querySelectorAll(selector){
      if(selector === '[data-locale]') return selectors;
      if(selector === '[data-glossary-term]') return glossaryTerms;
      if(selector === '.glossary-section') return glossarySections;
      if(selector === '[data-error-source]') return [...elements.values()].filter(node => node.dataset.errorSource);
      return [];
    }
  };
  const localStorage=storage({...preferences,...(initialLocale ? {'rillway-locale':initialLocale} : {})});
  const sessionStorage=storage({'rillway-token':'existing-management-token'});
  let now = 0, nextTimer = 1;
  const timers = new Map();
  const setTimeout = (callback, delay) => { const id = nextTimer++; timers.set(id,{callback,at:now+delay}); return id; };
  const clearTimeout = id => timers.delete(id);
  const clock = {async advance(ms) { now += ms; for(const [id,timer] of [...timers]) { if(timer.at <= now) { timers.delete(id); timer.callback(); } } await new Promise(resolve => setImmediate(resolve)); }};
  const intervals = new Map();
  const setInterval = (callback,delay) => { const id=nextTimer++;intervals.set(id,{callback,delay});return id; };
  const clearInterval = id => intervals.delete(id);
  const requests = [];
  const context=vm.createContext({document,localStorage,sessionStorage,
    window:{addEventListener(event,callback){windowEvents[event]=callback;}},
    location:{hostname:'example.test',hash:''}, history:{replaceState(){}},
    navigator:{language:'zh-TW'},NodeFilter:{SHOW_TEXT:4},
    fetch:async(url)=>{requests.push(url);return {ok:true,json:async()=>catalog};},
    structuredClone,setTimeout,clearTimeout,setInterval,clearInterval,
    Date:class extends Date { static now(){return now;} }});
  vm.runInContext(helper,context,{filename:'i18n.js'});
  await context.RillwayI18n.ready;
  vm.runInContext(app,context,{filename:'app.js'});
  assert.equal(typeof documentEvents.DOMContentLoaded,'function','loading must register the deferred bootstrap');
  assert.equal(documentEventCounts.DOMContentLoaded,1);
  assert.equal(get('login-form').events.submit,undefined,'loading must not bind or sign in yet');
  assert.deepEqual(requests,['/locales.json'],'app evaluation must not request private data');
  assert.equal(timers.size,0,'app evaluation must not start polling');
  assert.equal(intervals.size,0,'app evaluation must not start polling');
  return {context,get,label,userText,localStorage,sessionStorage,document,glossaryTerms,glossarySections,clock,timers,intervals,documentEvents,windowEvents,requests};
}

(async()=>{
  const b=await browser();
  vm.runInContext('bindUI(); bindUI();',b.context);
  for(const id of ['login-form','memory-form','source-capacity-form','warp-log-form','outbound-form']) {
    assert.equal(b.get(id).eventCounts.submit,1,'bindUI must register each form exactly once');
  }
  assert.equal(b.get('sidebar-language').eventCounts.change,1);
  assert.deepEqual(b.requests,['/locales.json'],'binding alone must not load private data');
  assert.equal(b.intervals.size,0,'binding alone must not start polling');
  vm.runInContext("cfg={revision:42}; serverInfo={version:'v9.8.7'}; latestSnapshot={config_revision:43}; renderRevision();",b.context);
  assert.equal(b.get('program-version').textContent,'Program version: v9.8.7');
  assert.equal(b.get('revision').textContent,'43');
  assert.equal(b.get('advanced-info').open,false);
  vm.runInContext("serverInfo=null; renderRevision();",b.context);
  assert.equal(b.get('program-version').textContent,'Program version unavailable');
  vm.runInContext("serverInfo={version:'v9.8.7'}; i18n.setLocale('zh-Hant'); renderRevision();",b.context);
  assert.equal(b.get('program-version').textContent,'程式版本：v9.8.7');
  vm.runInContext("i18n.setLocale('en');",b.context);
  const originalFetch=b.context.fetch;
  for (const status of [404,401,500]) {
    b.context.fetch=async()=>({ok:false,status,json:async()=>({error:'fixture failure'})});
    if(status===404) assert.equal(await vm.runInContext('loadInfo()',b.context),null);
    else await assert.rejects(vm.runInContext('loadInfo()',b.context),error=>error.status===status);
  }

  b.context.fetch=originalFetch;
  const translate=b.context.RillwayI18n;
  assert.equal(translate.locale,'en','browser language must not change the default');
  assert.equal((await browser('zh-Hant')).context.RillwayI18n.locale,'zh-Hant');
  assert.equal((await browser('unsupported')).context.RillwayI18n.locale,'en');
  const tokenValue=b.get('token').value;
  translate.setLocale('zh-Hant');
  assert.equal(b.document.documentElement.lang,'zh-Hant');
  assert.equal(b.document.title,'Rillway · 決定連線的路徑');
  assert.equal(b.label.textContent,' 管理權杖 ');
  assert.equal(b.get('token').attributes.placeholder,catalog['zh-Hant']['Search domain, IP, or outbound']);
  assert.equal(b.userText.textContent,'Management token','textarea contents must not be translated');
  assert.equal(b.get('token').value,tokenValue);
  for(const source of ['constructor','__proto__','toString','公司 😀 <custom upstream status>']) {
    assert.equal(translate.t(source),source,'unknown sources must stay unchanged');
  }
  assert.deepEqual([...b.localStorage.values.keys()],['rillway-locale']);
  assert.equal(b.sessionStorage.getItem('rillway-token'),'existing-management-token');
  assert.equal(vm.runInContext("et('Move {name} up',{name:'<script>公司😀'})",b.context),
    catalog['zh-Hant']['Move {name} up'].replace('{name}','&lt;script&gt;公司😀'));

  vm.runInContext(`
    cfg={outbounds:[{id:'warp',type:'warp',enabled:true,public_internet:true}]};
    statuses=[{id:'warp',state:'connected',account:'Unlimited'}];
    renderOutbounds();
  `,b.context);
  assert.match(b.get('outbound-list').innerHTML,/data-action="register" data-id="warp" disabled>已註冊<\/button>/);
  vm.runInContext("i18n.setLocale('en');renderOutbounds();",b.context);
  assert.match(b.get('outbound-list').innerHTML,/data-action="register" data-id="warp" disabled>Registered<\/button>/);
  vm.runInContext("statuses=[];renderOutbounds();",b.context);
  assert.match(b.get('outbound-list').innerHTML,/data-action="register" data-id="warp">Register<\/button>/);
  vm.runInContext(`
    cfg={outbounds:[{id:'direct',type:'direct',enabled:true,public_internet:true},{id:'warp',type:'warp',enabled:true,public_internet:true}]};
    renderOutbounds();
  `,b.context);
  assert.doesNotMatch(b.get('outbound-list').innerHTML,/data-(?:delete|edit)-outbound="0"/);
  assert.match(b.get('outbound-list').innerHTML,/data-delete-outbound="1"/);

  vm.runInContext(`
    i18n.setLocale('zh-Hant');
    renderPACList('domains',[{value:'company.example',enabled:true},{value:'<custom>',enabled:false}]);
  `,b.context);
  assert.match(b.get('pac-domains').innerHTML,/aria-label="讓 company\.example 略過 Proxy"/,'PAC switches must identify their destination');
  assert.match(b.get('pac-domains').innerHTML,/aria-label="讓 &lt;custom&gt; 略過 Proxy"/,'destination names must be escaped in accessible labels');
  vm.runInContext("i18n.setLocale('en');renderPACList('cidrs',[{value:'10.0.0.0/8',enabled:true}]);",b.context);
  assert.match(b.get('pac-cidrs').innerHTML,/aria-label="Bypass 10\.0\.0\.0\/8"/);

  vm.runInContext('cfg.outbounds[1].enabled=false;renderOutbounds()',b.context);
  assert.match(b.get('outbound-list').innerHTML,/data-action="connect" data-id="warp" disabled/,'disabled outbounds must not offer an unavailable connect action');
  assert.match(b.get('outbound-list').innerHTML,/data-license="warp" disabled/);
  assert.match(b.get('outbound-list').innerHTML,/Use Edit to enable/);
  vm.runInContext('cfg.outbounds[1].enabled=true;renderOutbounds()',b.context);

  let restoredOutboundFocus=0;
  const oldEdit={hasAttribute:key=>key==='data-edit-outbound',closest:()=>({dataset:{outboundId:'warp'}})};
  const newEdit={disabled:false,focus(){restoredOutboundFocus++;}};
  const newCard={dataset:{outboundId:'warp'},querySelectorAll:()=>[newEdit]};
  b.document.activeElement=oldEdit;
  b.get('outbound-list').querySelectorAll=()=>[newCard];
  vm.runInContext('renderOutbounds()',b.context);
  assert.equal(restoredOutboundFocus,1,'outbound status refresh must keep keyboard focus on the same outbound');
  let restoredAuthFocus=0;
  const oldAuth={hasAttribute:key=>key==='data-auth-outbound',closest:()=>({dataset:{outboundId:'tailnet'}})};
  const newAuth={focus(){restoredAuthFocus++;}};
  const authCard={dataset:{outboundId:'tailnet'},querySelectorAll:selector=>selector==='[data-auth-outbound]'?[newAuth]:[]};
  b.document.activeElement=oldAuth;
  b.get('outbound-list').querySelectorAll=()=>[authCard];
  vm.runInContext(`cfg.outbounds.push({id:'tailnet',type:'tailscale',enabled:true});statuses.push({id:'tailnet',auth_url:'https://login.tailscale.com/a/example'});renderOutbounds()`,b.context);
  assert.match(b.get('outbound-list').innerHTML,/data-auth-outbound="tailnet" href="https:\/\/login\.tailscale\.com\/a\/example"/);
  assert.equal(restoredAuthFocus,1,'outbound status refresh must keep keyboard focus on the sign-in link');
  vm.runInContext('cfg.outbounds.pop();statuses.pop()',b.context);
  b.document.activeElement=null;
  vm.runInContext("pendingOutboundActions.add('warp/connect');renderOutbounds();",b.context);
  assert.match(b.get('outbound-list').innerHTML,/data-action="connect" data-id="warp" disabled/,'status refresh must not re-enable a pending action');
  vm.runInContext("pendingOutboundActions.delete('warp/connect');renderOutbounds();",b.context);
  assert.match(b.get('outbound-list').innerHTML,/data-action="connect" data-id="warp">/);

  const grouped=JSON.parse(vm.runInContext(`JSON.stringify(groupFlows([
    {index:0,f:{id:10,host:'github.com',port:'443',outbound:'warp-plus',rule:'rule-ghcr',download_bytes:1024,download_bytes_per_second:100,closed:false}},
    {index:1,f:{id:11,host:'github.com',port:'80',outbound:'direct',rule:'default',upload_bytes:512,upload_bytes_per_second:20,closed:true}},
    {index:2,f:{ip:'2001:db8::1',port:'443',download_bytes:256,closed:false}},
    {index:3,f:{ip:'2001:db8::1',port:'8443',upload_bytes:128,closed:false}}
  ]))`,b.context));
  const github=grouped.find(group=>group.destination==='github.com');
  assert.equal(github.connections.length,2,'domain ports should share one destination group');
  assert.equal(github.active,1);
  assert.equal(github.transferred,1536);
  const githubRoute=JSON.parse(vm.runInContext(`JSON.stringify(groupRouteSummary(${JSON.stringify(github)}))`,b.context));
  assert.equal(githubRoute.outboundCount,2);
  assert.equal(githubRoute.routeCount,2);
  assert.equal(githubRoute.index,0,'the group action should use an active connection as its rule template');
  assert(grouped.some(group=>group.destination==='[2001:db8::1]:443'));
  assert(grouped.some(group=>group.destination==='[2001:db8::1]:8443'),'IP destinations must include the port in their group');
  const hostIP=JSON.parse(vm.runInContext(`JSON.stringify(groupFlows([{index:0,f:{host:'192.0.2.8',ip:'192.0.2.8',port:'443'}}]))`,b.context));
  assert.equal(hostIP[0].destination,'192.0.2.8:443','an IP in the host field must still include its port');
  vm.runInContext(`
    i18n.setLocale('zh-Hant');
    flows=[
      {id:10,host:'github.com',port:'443',outbound:'warp-plus',rule:'rule-ghcr',download_bytes:1024,closed:false},
      {id:11,host:'github.com',port:'80',outbound:'direct',rule:'default',upload_bytes:512,closed:true}
    ];
    renderFlows();
  `,b.context);
  assert.match(b.get('flow-groups').innerHTML,/多個出口/);
  assert.match(b.get('flow-groups').innerHTML,/2 條路由/);
  assert.equal((b.get('flow-groups').innerHTML.match(/data-flow=/g)||[]).length,1,'set-outbound belongs to the group header');
  assert.match(b.get('flow-groups').innerHTML,/data-group-action="true"/,'group action needs a stable focus identity');
  let restoredGroupActionFocus=0;
  const stableGroup={dataset:{groupKey:'domain%3Agithub.com'},querySelectorAll(){return [];},querySelector(selector){
    return selector === '[data-group-action]' ? {focus(){restoredGroupActionFocus++;}} : null;
  }};
  b.get('flow-groups').querySelectorAll=()=>[stableGroup];
  b.document.activeElement={dataset:{groupAction:'true'},closest(selector){return selector === '.flow-group' ? stableGroup : null;}};
  vm.runInContext("flows.unshift({id:12,host:'github.com',port:'443',outbound:'warp-plus',rule:'rule-ghcr',closed:false});renderFlows();",b.context);
  assert.equal(restoredGroupActionFocus,1,'group refresh must restore focus after its representative flow changes');
  b.document.activeElement=null;
  vm.runInContext(`
    deletingOutbound={id:'warp',config:{outbounds:[
      {id:'direct',type:'direct',enabled:true,public_internet:true},
      {id:'warp',type:'warp',enabled:true,public_internet:true},
      {id:'company',type:'tailscale',enabled:true,public_internet:true}],
      rules:[{id:'<script>公司😀',outbound:'warp'}],default_outbound:'warp',adaptive:{candidates:['direct','warp']}}};
    $('delete-replacement').value='direct';renderDeleteOutbound();
  `,b.context);
  assert.equal(b.get('delete-replacement').value,'direct');
  assert.equal(b.get('delete-replacement').required,true);
  assert.match(b.get('delete-outbound-references').innerHTML,/&lt;script&gt;公司😀/);
  assert.doesNotMatch(b.get('delete-replacement').innerHTML,/value="(?:warp|company)"/);
  vm.runInContext("i18n.setLocale('zh-Hant');renderDeleteOutbound();",b.context);
  assert.equal(b.get('delete-replacement').value,'direct');
  assert.match(b.get('delete-outbound-name').textContent,/確定刪除出口「warp」/);
  vm.runInContext("deletingOutbound=null;",b.context);
  vm.runInContext("cfg=undefined;i18n.setLocale('zh-Hant');",b.context);

  // Browser QA covers layout and form preservation. Here only stub visual
  // refresh work, keeping the shipped error and locale-switch behavior intact.
  vm.runInContext(`
    navigate=connection=renderFlows=renderMemory=()=>{};
    pollStatuses=()=>{};
  `,b.context);
  const safeSource='Enter a valid management token to sign in.';
  b.context.apiSource=safeSource;
  b.context.apiLocalized=catalog['zh-Hant'][safeSource];
  vm.runInContext(`
    switchLocale('zh-Hant');
    displayError($('login-error'),{message:apiLocalized,source:apiSource});
    notice({message:apiLocalized,source:apiSource},true);
  `,b.context);
  assert.equal(b.get('login-error').textContent,catalog['zh-Hant'][safeSource]);
  vm.runInContext("switchLocale('en')",b.context);
  assert.equal(b.get('login-error').textContent,safeSource);
  assert.equal(b.get('notice').textContent,safeSource);
  vm.runInContext("displayError($('login-error'),{message:'未知 upstream 😀'});switchLocale('zh-Hant');",b.context);
  assert.equal(b.get('login-error').textContent,'未知 upstream 😀');
  assert.equal(b.get('login-error').dataset.errorSource,undefined);

  // A failed logging refresh must keep a previously supported form disabled;
  // otherwise its stale revision can be submitted after the failure.
  b.context.fetch=async()=>({ok:false,status:503,json:async()=>({error:'Could not read WARP logging settings.'})});
  vm.runInContext("active=true;errorIn=()=>{};warpLogStatus={supported:true,level:'info',effective_level:'info',revision:'stale'};renderWarpLog(true)",b.context);
  await vm.runInContext('loadWarpLog(true)',b.context);
  assert.equal(b.get('warp-log-fields').disabled,true,'failed WARP refresh must keep stale controls disabled');

  // Search should work across languages and full-width input, without
  // changing user text, credentials, or configuration.
  const configBeforeSearch=vm.runInContext('JSON.stringify(cfg)',b.context);
  b.get('glossary-search').value=' ＤＮＳ ';
  vm.runInContext('filterGlossary()',b.context);
  assert.deepEqual(b.glossaryTerms.map(term=>term.hidden),[false,true,true]);
  assert.deepEqual(b.glossarySections.map(section=>section.hidden),[false,true,true]);
  assert.equal(b.get('glossary-empty').hidden,true);
  assert.equal(b.get('glossary-count').textContent, catalog['zh-Hant']['{shown} of {total} terms'].replace('{shown}','1').replace('{total}','3'));
  b.get('glossary-search').value='代理';
  vm.runInContext("switchLocale('en')",b.context);
  assert.equal(b.get('glossary-search').value,'代理');
  assert.deepEqual(b.glossaryTerms.map(term=>term.hidden),[true,false,true]);
  assert.equal(b.get('glossary-count').textContent,'1 of 3 terms');
  b.get('glossary-search').value='把瀏覽器';
  vm.runInContext('filterGlossary()',b.context);
  assert.deepEqual(b.glossaryTerms.map(term=>term.hidden),[true,false,true],'Chinese explanations remain searchable in English');
  b.get('glossary-search').value='<script>🚀';
  vm.runInContext('filterGlossary()',b.context);
  assert.deepEqual(b.glossaryTerms.map(term=>term.hidden),[true,true,true]);
  assert.equal(b.get('glossary-empty').hidden,false);
  b.get('glossary-search').value='';
  vm.runInContext('filterGlossary()',b.context);
  assert.equal(b.get('glossary-count').textContent,'3 of 3 terms');
  assert.deepEqual(b.glossarySections.map(section=>section.hidden),[false,false,false]);
  assert.equal(b.get('glossary-empty').hidden,true);
  assert.equal(vm.runInContext('JSON.stringify(cfg)',b.context),configBeforeSearch);
  assert.equal(b.sessionStorage.getItem('rillway-token'),'existing-management-token');

  // A PAC URL is distinct from the Proxy address embedded in that file.
  b.context.location={hostname:'control.example.net'};
  assert.equal(vm.runInContext("pacURL('192.0.2.20:17893')",b.context),'http://192.0.2.20:17893/proxy.pac');
  assert.equal(vm.runInContext("pacURL('[2001:db8::20]:17893')",b.context),'http://[2001:db8::20]:17893/proxy.pac');
  assert.equal(vm.runInContext("pacURL('0.0.0.0:17893')",b.context),'http://control.example.net:17893/proxy.pac');
  assert.equal(vm.runInContext("pacURL('')",b.context),'');
  b.context.location.hostname='[2001:db8::10]';
  assert.equal(vm.runInContext("pacURL('[::]:17893')",b.context),'http://[2001:db8::10]:17893/proxy.pac');

  // Defaults are real values; switching types and languages preserves edits.
  b.context.defaults={outbounds:{warp:{id:'warp-2',type:'warp',enabled:true,public_internet:true,proxy_address:'127.0.0.1:40000',warp_binary:'warp-cli'},wireguard:{id:'wireguard',type:'wireguard',enabled:false,public_internet:true,config_file:'/daemon/secrets/wireguard.conf'},tailscale:{id:'tailscale',type:'tailscale',enabled:false,public_internet:false,hostname:'rillway-tailscale',state_dir:'/daemon/tailscale/tailscale'}},rule:{id:'rule',domains:['github.com'],outbound:'direct',family:'auto'}};
  b.context.fetch=async()=>({ok:true,json:async()=>b.context.defaults});
  vm.runInContext("cfg={outbounds:[{id:'direct',type:'direct'},{id:'warp',type:'warp',proxy_address:'127.0.0.1:45678'}],rules:[],listeners:{pac:'127.0.0.1:17893'},adaptive:{candidates:[]},default_outbound:'direct'}",b.context);
  await vm.runInContext('openOutbound()',b.context);
  assert.equal(b.get('out-address').value,'127.0.0.1:40000');
  assert.equal(b.get('out-binary').value,'warp-cli');
  assert.equal(b.get('out-id').value,'warp-2');
  b.get('out-address').value='127.0.0.1:45555';
  b.get('out-id').value='公司 🚀';
  b.get('out-type').value='wireguard';vm.runInContext('changeOutboundType()',b.context);
  assert.equal(b.get('out-id').value,'公司 🚀');
  assert.equal(b.get('out-file').value,'','new profiles have no saved server file yet');
  assert.equal(b.get('out-profile').required,true);
  assert.equal(b.get('out-enabled').checked,false);
  b.get('out-profile').value='private draft';
  b.get('out-type').value='tailscale';vm.runInContext('changeOutboundType()',b.context);
  assert.equal(b.get('out-public').disabled,true);
  assert.equal(b.get('out-public').checked,false);
  assert.equal(b.get('out-hostname').readOnly,false);
  b.get('out-type').value='wireguard';vm.runInContext('changeOutboundType()',b.context);
  assert.equal(b.get('out-profile').value,'private draft','type switches keep an unsaved import');
  b.get('out-type').value='warp';vm.runInContext('changeOutboundType();switchLocale(\'en\')',b.context);
  assert.equal(b.get('out-address').value,'127.0.0.1:45555');
  assert.equal(b.get('out-id').value,'公司 🚀');
  await vm.runInContext('openOutbound(1)',b.context);
  assert.equal(b.get('out-address').value,'127.0.0.1:45678','editing must keep existing custom values');
  assert.equal(b.get('out-id').disabled,true);
  vm.runInContext("cfg.outbounds.push({id:'tail',type:'tailscale',enabled:true,state_dir:'/daemon/tail',hostname:'existing-tail',dns:['100.100.20.1']})",b.context);
  b.context.defaults.restart_required_outbounds=['tail'];
  await vm.runInContext('openOutbound(2)',b.context);
  assert.equal(b.get('out-type').disabled,true);
  assert.equal(b.get('out-hostname').readOnly,true);
  assert.equal(b.get('out-dns').readOnly,true);
  assert.equal(b.get('out-key').readOnly,true);
  assert.equal(b.get('out-restart-hint').hidden,false);
  assert.equal(b.get('out-key').value,'','existing credentials are never filled in');
  await vm.runInContext('openRule()',b.context);
  assert.equal(b.get('rule-domains').value,'github.com');
  assert.equal(b.get('rule-family').value,'');
  await vm.runInContext("openRule(-1,{ip:'192.0.2.8',outbound:'warp'})",b.context);
  assert.equal(b.get('rule-domains').value,'','IP rule must not keep the default GitHub domain');
  assert.equal(b.get('rule-cidrs').value,'192.0.2.8/32');
  assert.equal(b.get('rule-setup-hint').hidden,true);

  let request;
  b.context.fetch=async(url,options)=>{request={url,options};return {
    ok:false,status:409,json:async()=>({error:catalog['zh-Hant'][safeSource],error_source:safeSource})};};
  vm.runInContext("switchLocale('zh-Hant')",b.context);
  await assert.rejects(vm.runInContext("api('/config')",b.context),error=>error.source===safeSource);
  assert.equal(request.options.headers['Accept-Language'],'zh-Hant');
  assert.equal(request.options.headers.Authorization,'Bearer existing-management-token');
  assert.equal(request.options.credentials,'omit');
  // Closing dialogs clears secrets through the shipped listeners.
  b.get('out-profile').value='private WireGuard draft';
  b.get('out-key').value='private Tailscale auth key';
  b.get('out-upload').value='private selected profile';
  b.get('outbound-dialog').open=false;
  b.get('outbound-dialog').events.close();
  for(const id of ['out-profile','out-key','out-upload']) assert.equal(b.get(id).value,'');
  assert.equal(vm.runInContext('Object.keys(outboundDrafts).length',b.context),0);
  b.get('license-value').value='private WARP license';
  vm.runInContext("licenseID='warp'",b.context);
  b.get('license-dialog').events.close();
  assert.equal(b.get('license-value').value,'');
  assert.equal(vm.runInContext('licenseID',b.context),'');
  // WARP logging 409 conflict handler test:
  // On 409, GET /service/logging/warp must be called immediately, revision updated,
  // user's draft level preserved, form marked dirty, and conflict/retry guidance displayed.
  vm.runInContext("switchLocale('en')", b.context);
  const warpCalls = [];
  b.context.fetch = async (url, options) => {
    warpCalls.push({ url, method: options?.method || 'GET', body: options?.body });
    if (options?.method === 'PUT') {
      return { ok: false, status: 409, json: async () => ({ error: 'Conflict', error_source: 'WARP logging settings changed elsewhere. Refresh settings before saving.' }) };
    }
    return { ok: true, json: async () => ({ supported: true, level: 'warning', effective_level: 'warning', revision: 'rev-fresh' }) };
  };
  const warpForm = b.get('warp-log-form');
  const warpError = b.get('warp-log-form-error');
  warpForm.querySelector = () => warpError;
  b.get('warp-log-level').value = 'debug';
  vm.runInContext(`
    active = true;
    warpLogStatus = { supported: true, level: 'info', effective_level: 'info', revision: 'rev-stale' };
    warpLogDraftRevision = 'rev-stale';
    warpLogDirty = false;
  `, b.context);
  await warpForm.events.submit({ preventDefault() {}, target: warpForm });
  assert.equal(warpCalls.length, 2, '409 must trigger an immediate GET');
  assert.equal(warpCalls[0].method, 'PUT');
  assert.equal(warpCalls[1].method, 'GET');
  assert.equal(warpCalls[1].url, '/api/v1/service/logging/warp');
  assert.equal(b.get('warp-log-level').value, 'debug', 'selected draft level must be preserved on 409');
  assert.equal(vm.runInContext('warpLogDraftRevision', b.context), 'rev-fresh', 'revision must be updated to fresh revision');
  assert.equal(vm.runInContext('warpLogDirty', b.context), true, 'form must remain dirty');
  assert.equal(warpError.textContent, 'WARP logging settings changed elsewhere. Refresh settings before saving.');
  assert.equal(b.get('notice').textContent, 'Your selected level has been kept. Refresh status to review the current setting, then save again.');

  for(const [raw,expected] of [[undefined,30],['',30],['nonsense',30],['0',0],['10',10]]) {
    const prefs = raw === undefined ? {} : {'rillway-outbound-refresh-seconds':raw};
    const fixture = await browser('en',prefs);
    assert.equal(vm.runInContext('outboundRefreshSeconds',fixture.context),expected);
  }
  const scheduler = await browser('en',{'rillway-outbound-refresh-seconds':'10'});
  vm.runInContext('bindUI()',scheduler.context);
  const requests = [];
  scheduler.context.fetch = async url => { requests.push(url); return {ok:true,json:async()=>[]}; };
  vm.runInContext("active=true; navigate('outbounds')",scheduler.context);
  await scheduler.clock.advance(0);
  assert.deepEqual(requests,['/api/v1/outbounds'],'entering Outbounds loads cached status');
  await scheduler.clock.advance(9999); assert.equal(requests.length,1);
  await scheduler.clock.advance(1); assert.equal(requests.length,2);
  vm.runInContext("navigate('overview')",scheduler.context);
  await scheduler.clock.advance(10000); assert.equal(requests.length,2);
  scheduler.document.hidden=true;
  vm.runInContext("navigate('outbounds')",scheduler.context);
  await scheduler.clock.advance(10000); assert.equal(requests.length,2);
  scheduler.document.hidden=false;
  vm.runInContext('outboundRefreshSeconds=0; scheduleOutboundPolling()',scheduler.context);
  await scheduler.clock.advance(10000); assert.equal(requests.length,2);
  await vm.runInContext('pollStatuses(true)',scheduler.context);
  assert.equal(requests.at(-1),'/api/v1/outbounds?refresh=true');
  b.context.fetch=async()=>({ok:false,status:501,json:async()=>({error:'unsupported'})});
  await warpForm.events.submit({preventDefault(){},target:warpForm});
  assert.equal(b.get('warp-log-fields').disabled,true);
  assert.match(b.get('warp-log-unsupported-reason').textContent,/sudo rillway service restart/);
  b.context.fetch=async()=>({ok:true,json:async()=>({supported:false,reason:'fixture unsupported reason'})});
  await vm.runInContext('loadWarpLog()',b.context);
  assert.equal(b.get('warp-log-unsupported').hidden,false);
  assert.equal(b.get('warp-log-unsupported-reason').textContent,'fixture unsupported reason');
  process.stdout.write('Browser locale contracts passed\n');
})().catch(error=>{console.error(error);process.exitCode=1;});
