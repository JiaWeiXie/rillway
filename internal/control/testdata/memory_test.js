'use strict';
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const app=fs.readFileSync('web/app.js','utf8');
const elements=new Map();
function get(id){if(!elements.has(id))elements.set(id,{value:'',textContent:'',innerHTML:'',disabled:false,dataset:{},events:{},querySelector(){return get('error');},addEventListener(event,callback){this.events[event]=callback;}});return elements.get(id);}
const context=vm.createContext({document:{getElementById:get,body:{}},globalThis:{RillwayI18n:{locale:'en',capture(){},t(source,values={}){return source.replace(/\{(\w+)\}/g,(_,key)=>values[key]??key);}}},sessionStorage:{getItem(){return'';}},localStorage:{getItem(){return'';}},location:{hostname:'example.test'},console,structuredClone});
vm.runInContext(app.slice(0,app.indexOf("document.querySelectorAll('[data-locale]').forEach")),context);
vm.runInContext(app.slice(app.indexOf("$('memory-mode').addEventListener"),app.indexOf("$('login-form').addEventListener")),context);
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
 const sent=context.globalThis.sent;assert.equal(sent.mode,'GiB');assert.equal(sent.value,'0.75');assert.equal(sent.revision,'two');assert.equal(vm.runInContext('memoryDirty',context),false);assert.equal(get('memory-fields').disabled,false);
 context.fixture={...s,supported:false,reason:'Service memory limits require a Linux systemd installation.'};vm.runInContext('api=async()=>fixture;',context);await vm.runInContext('loadMemory()',context);assert.equal(get('memory-fields').disabled,true);
 console.log('Memory form: units, bounds, dirty input, conflict revision, save and unsupported platform passed.');
})().catch(err=>{console.error(err);process.exitCode=1;});
