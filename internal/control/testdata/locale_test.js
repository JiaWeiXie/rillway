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

function element(id = '') {
  return {id, value:'', checked:false, hidden:false, open:false, disabled:false,
    textContent:'', innerHTML:'', dataset:{}, options:[], isConnected:true,
    attributes:{}, classList:{toggle(){}},
    getAttribute(name){return this.attributes[name] ?? null;},
    setAttribute(name,value){this.attributes[name] = value;},
    showModal(){this.open=true;},close(){this.open=false;},contains(){return false;}, querySelector(){return null;}, querySelectorAll(){return [];}};
}
function storage(initial = {}) {
  const values = new Map(Object.entries(initial));
  return {values,getItem(key){return values.get(key) ?? null;},
    setItem(key,value){values.set(key,String(value));},removeItem(key){values.delete(key);}};
}
async function browser(initialLocale) {
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
  const document = {
    documentElement:{lang:'en'}, title:'',
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
  const localStorage=storage(initialLocale ? {'rillway-locale':initialLocale} : {});
  const sessionStorage=storage({'rillway-token':'existing-management-token'});
  const context=vm.createContext({document,localStorage,sessionStorage,
    navigator:{language:'zh-TW'},NodeFilter:{SHOW_TEXT:4},
    fetch:async()=>({ok:true,json:async()=>catalog}),structuredClone});
  vm.runInContext(helper,context);
  await context.RillwayI18n.ready;
  vm.runInContext(app.slice(0,app.indexOf("document.querySelectorAll('[data-locale]').forEach")),context);
  return {context,get,label,userText,localStorage,sessionStorage,document,glossaryTerms,glossarySections};
}

(async()=>{
  const b=await browser();
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
    navigate=connection=renderFlows=()=>{};
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
  assert.equal(b.get('out-file').value,'/daemon/secrets/wireguard.conf');
  assert.equal(b.get('out-enabled').checked,false);
  b.get('out-type').value='tailscale';vm.runInContext('changeOutboundType()',b.context);
  assert.equal(b.get('out-public').disabled,true);
  assert.equal(b.get('out-public').checked,false);
  b.get('out-type').value='warp';vm.runInContext('changeOutboundType();switchLocale(\'en\')',b.context);
  assert.equal(b.get('out-address').value,'127.0.0.1:45555');
  assert.equal(b.get('out-id').value,'公司 🚀');
  await vm.runInContext('openOutbound(1)',b.context);
  assert.equal(b.get('out-address').value,'127.0.0.1:45678','editing must keep existing custom values');
  assert.equal(b.get('out-id').disabled,true);
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
  process.stdout.write('Browser locale contracts passed\n');
})().catch(error=>{console.error(error);process.exitCode=1;});
