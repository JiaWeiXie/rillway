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
    contains(){return false;}, querySelector(){return null;}, querySelectorAll(){return [];}};
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
    navigate=connection=()=>{};
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
