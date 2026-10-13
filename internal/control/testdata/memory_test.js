'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const {loadScripts,web}=require('./shipped_scripts');
const catalog=JSON.parse(fs.readFileSync(require('node:path').join(web,'locales.json'),'utf8'));
const elements=new Map();
function get(id) {
  if(!elements.has(id)) elements.set(id,{
    value:'',textContent:'',innerHTML:'',disabled:false,dataset:{},events:{},eventCounts:{},
    attributes:{},setAttribute(name,value){this.attributes[name]=String(value);},
    getAttribute(name){return this.attributes[name]??null;},removeAttribute(name){delete this.attributes[name];},
    querySelectorAll(){return [];},focus(){},close(){},reset(){},
    querySelector(){return get('error');},
    addEventListener(event,callback){
      this.events[event]=callback;
      this.eventCounts[event]=(this.eventCounts[event]||0)+1;
    }
  });
  return elements.get(id);
}
const documentEvents={};
const context=vm.createContext({
  document:{
    readyState:'loading',documentElement:{removeAttribute(){},setAttribute(){}},getElementById:get,body:{querySelectorAll(){return [];}},
    createTreeWalker(){return {nextNode(){return false;}};},
    querySelector(){return null;},
    querySelectorAll(){return [];},
    addEventListener(event,callback){documentEvents[event]=callback;}
  },
  window:{addEventListener(){}},
  NodeFilter:{SHOW_TEXT:4},fetch:async()=>({ok:true,json:async()=>catalog}),setTimeout,clearTimeout,
  sessionStorage:{getItem(){return'';}},localStorage:{getItem(){return'';}},
  location:{hostname:'example.test',hash:''},console,structuredClone
});
loadScripts(context);
assert.equal(typeof documentEvents.DOMContentLoaded,'function');
assert.equal(get('memory-form').events.submit,undefined,'loading must defer listener registration');
vm.runInContext('bindUI(); bindUI();',context);
assert.equal(get('memory-form').eventCounts.submit,1,'binding twice must not duplicate mutations');
const s={supported:true,host_bytes:4*1073741824,minimum_bytes:256*1048576,maximum_bytes:Math.floor(3.6*1024)*1048576,current_bytes:32*1048576,limit_bytes:512*1048576,mode:'MiB',value:'512',revision:'one',minimum_reason:'Direct and WARP safety floor'};
context.fixture=s;vm.runInContext('active=true;api=async()=>structuredClone(fixture);notice=()=>{};',context);
(async()=>{
 await vm.runInContext('loadMemory()',context);
 assert.equal(get('memory-value').value,'512');assert.equal(get('memory-fields').disabled,false);
 assert.equal(get('memory-value').min,'256');assert.match(get('memory-preview').textContent,/512.0 MiB/);
 get('memory-mode').value='GiB';get('memory-mode').events.change();get('memory-value').value='0.75';get('memory-value').events.input();
 context.fixture={...s,revision:'two'};await vm.runInContext('loadMemory()',context);
 assert.equal(get('memory-value').value,'0.75');assert.equal(vm.runInContext('memoryDraftRevision',context),'one');
 // An explicit refresh retains typed values but accepts the new server version.
 await vm.runInContext('loadMemory(true)',context);assert.equal(get('memory-value').value,'0.75');assert.equal(vm.runInContext('memoryDraftRevision',context),'two');
 vm.runInContext('api=async(path,options)=>{globalThis.sent=JSON.parse(options.body);return {...fixture,mode:"GiB",value:"0.75",limit_bytes:805306368,revision:"three"};};',context);
 await get('memory-form').events.submit({preventDefault(){},target:get('memory-form')});
 const sent=context.sent;assert.equal(sent.mode,'GiB');assert.equal(sent.value,'0.75');assert.equal(sent.revision,'two');assert.equal(vm.runInContext('memoryDirty',context),false);assert.equal(get('memory-fields').disabled,false);
 context.fixture={...s,supported:false,reason:'Service memory limits require a Linux systemd installation.'};vm.runInContext('api=async()=>fixture;',context);await vm.runInContext('loadMemory()',context);assert.equal(get('memory-fields').disabled,true);
 console.log('Memory form: units, bounds, dirty input, conflict revision, save and unsupported platform passed.');
})().catch(err=>{console.error(err);process.exitCode=1;});
