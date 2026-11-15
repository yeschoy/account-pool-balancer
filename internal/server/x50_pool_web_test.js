const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname,'web/x50.js'),'utf8');
const elements = new Map();
function el(id) {
  if(!elements.has(id)) {const classes=new Set(['collapsed']); elements.set(id,{value:'',checked:false,innerHTML:'',textContent:'',dataset:{},style:{},listeners:{},addEventListener(k,f){this.listeners[k]=f;},setAttribute(k,v){this[k]=v;},classList:{contains:k=>classes.has(k),toggle(k,on){on?classes.add(k):classes.delete(k);}}});}
  return elements.get(id);
}
let activePool='x50';let pending=null;const calls=[];
const ctx={console,Date,Set,Number,window:{},X50_PAGES:['x50reg','x50settings'],currentPage:'x50reg',isX50:()=>activePool==='x50',document:{getElementById:el,querySelector:()=>el('logs'),activeElement:null},api:async(method,url,body)=>{calls.push({method,url,body});if(pending)return pending;return {rows:[],logs:[],settings:{}};},confirm:()=>true,toast:()=>{},setInterval:()=>1,clearInterval:()=>{},esc:s=>String(s).replaceAll('<','&lt;'),fmtTime:s=>s};
vm.createContext(ctx);vm.runInContext(source,ctx);ctx.initX50Page();
(async()=>{
 const rows=[{id:'a',email:'a',status:'keyed',hasKey:true,inPool:true,proxyBound:false},{id:'b',email:'b',status:'keyed',hasKey:true,inPool:false,proxyBound:false}];
 ctx.renderX50Rows(rows);assert.doesNotMatch(el('x50-rows').innerHTML,/data-x50-select="a"/);assert.match(el('x50-rows').innerHTML,/data-x50-select="b"/);
 el('x50-show-imported').checked=true;ctx.renderX50Rows(rows);assert.match(el('x50-rows').innerHTML,/已入池/);assert.match(el('x50-rows').innerHTML,/data-x50-select="a"/);
 // Open/close/updates are the same dock interaction as Deploy, with unique state.
 await el('x50-log-toggle').listeners.click();assert.equal(el('x50-log-toggle')['aria-expanded'],'true');
 ctx.renderX50Logs([{at:Date.now(),type:'info',email:'fake',message:'<img evil>'}]);assert.match(el('logs').innerHTML,/&lt;img evil&gt;/);assert.equal(el('x50-log-badge').style.display,'none');
 el('x50-log-close').listeners.click();assert.equal(el('x50-log-toggle')['aria-expanded'],'false');
 ctx.renderX50Logs([{at:Date.now()},{at:Date.now()}]);assert.equal(el('x50-log-badge').textContent,'1');
 // A registration read finishing after leaving X50 must not publish rows/logs.
 let release;pending=new Promise(r=>release=r);const loading=ctx.loadX50Registrations();activePool='deploy';ctx.stopX50Polling();el('logs').innerHTML='unchanged';release({rows:[{email:'late'}],logs:[{message:'late'}]});await loading;assert.equal(el('logs').innerHTML,'unchanged');pending=null;
 activePool='x50';ctx.currentPage='x50reg';await ctx.loadX50Registrations();
 vm.runInContext("x50Rows = [{id:'b',status:'keyed',hasKey:true}]; x50Selected.add('b');",ctx);
 const button={disabled:false};await el('x50-pool-import').listeners.click({currentTarget:button});assert.equal(button.disabled,false);assert.equal(calls.find(c=>c.url==='/api/x50/pool/import').body.ids[0],'b');
 assert.match(ctx.x50QuotaHTML({quota:{x50:{balanceUsd:0,at:'time'}},quotaFresh:false}),/历史读数/);
 assert.match(ctx.x50QuotaHTML({quota:null}),/未知/);
 console.log('50X pool UI tests passed (hide/import/dock/update/late source)');
})().catch(e=>{console.error(e);process.exitCode=1;});
