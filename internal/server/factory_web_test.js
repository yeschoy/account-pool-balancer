'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'web/app.js'), 'utf8');
const html = fs.readFileSync(path.join(__dirname, 'web/index.html'), 'utf8');
const nodes = new Map();
function node(id) { if (!nodes.has(id)) nodes.set(id, { value:'', checked:false, innerHTML:'', textContent:'', dataset:{}, classList:{toggle(){}}, handlers:{}, addEventListener(k,f){this.handlers[k]=f;} }); return nodes.get(id); }
const context = { $:node, $$:()=>[], console, localStorage:{getItem:()=> 'factory'}, Date, document:{}, renderRules(){},renderPrices(){},toast(){}, readPricing:()=>({models:[],holidays:[]}), structuredClone };
vm.createContext(context);
vm.runInContext(source.slice(source.indexOf('const POOLS ='),source.indexOf('// navigator.clipboard')),context);
vm.runInContext(source.slice(source.indexOf('function factoryQuotaHTML('),source.indexOf('function deployQuotaHTML(')),context);
assert.equal(vm.runInContext('isFactory() && managedKeyPool() && defaultGroup()==="factory"',context),true);
assert.match(html,/data-pool="factory" role="tab"/);assert.match(html,/id="btn-factory-login"/);assert.match(source,/\/api\/factory\/login/);assert.match(html,/id="factory-proxy-pool"/);assert.match(html,/id="factory-ua-pool"/);assert.match(html,/id="btn-factory-assign-egress"/);assert.match(source,/\/api\/factory\/assign-egress/);
let rendered=context.factoryQuotaHTML({factory:{quotaError:'<img onerror=x>',windows:[],quotaState:'unsupported-credential'}});
assert.match(rendered,/额度未知/);assert.match(rendered,/&lt;img/);assert.doesNotMatch(rendered,/<img|\$0\.00/);
rendered=context.factoryQuotaHTML({quotaFresh:true,factory:{windows:[{pool:'standard',name:'5h',usedPercent:0,active:true,resetsAt:'2099-01-01T00:00:00Z'}],extraUsageBalanceCents:0}});
assert.match(rendered,/已用 0%/);assert.match(rendered,/\$0\.00/);
vm.runInContext(source.slice(source.indexOf('const STRATEGY_HINTS'),source.indexOf('// ---- error / retry rules')),context);
const fields=vm.runInContext('routingFields().map(f=>f[0])',context);
for(const name of ['strategy','sticky','onBindingBusy','sessionHeaders','explicitTtlSec','directMaxConns','proxyMaxConns'])assert.ok(fields.includes(name));
for(const name of ['walletFallback','lowShare','spentShare','ignoreCatalog'])assert.ok(!fields.includes(name));
const settings={revision:3,routing:{strategy:'ordered',sessionHeaders:[],directMaxConns:7,proxyMaxConns:3},quota:{enabled:false},upstream:{},logs:{},pricing:{models:[]},factory:{baseUrl:'http://127.0.0.1:9999',inferenceUrl:'http://127.0.0.1:9998',requestSurface:'cli_exec',openaiPlatform:'org-web',detailLog:false,models:[{id:'test',protocol:'chat',upstream:'fireworks'}]}};
const calls=[];context.api=async(method,url,body)=>{calls.push({method,url,body:body&&structuredClone(body)});if(url.startsWith('/api/overview'))return {groups:['factory']};return {settings:structuredClone(settings),strategies:['ordered'],defaults:{factoryModels:[{id:'gemini-4-carbon',protocol:'generate',upstream:'google'}]}};};context.renderForm=(_el,fs,obj={},prefix)=>{for(const[k,,type]of fs){node('#'+prefix+'-'+k).value=type==='list'?(obj[k]||[]).join(', '):String(obj[k]??'');node('#'+prefix+'-'+k).checked=!!obj[k];}};
context.clone=structuredClone;context.renderPoolTabs=()=>{};
vm.runInContext('let settingsCache=null; let errRules=[],retRules=[],priceRows=[];'+source.slice(source.indexOf('async function loadSettings()'),source.indexOf("$('#btn-default-rules').addEventListener")),context);
(async()=>{
 await context.loadSettings();assert.equal(node('#factory-amp-skip-rewrite').checked,true);assert.equal(node('#factory-base').value,'http://127.0.0.1:9999');assert.equal(node('#factory-inference').value,'http://127.0.0.1:9998');assert.equal(node('#factory-surface').value,'cli_exec');assert.equal(node('#factory-openai-platform').value,'org-web');assert.equal(node('#factory-compatibility').checked,true);assert.match(html,/id="factory-compatibility"/);assert.match(html,/id="factory-inference"/);assert.match(html,/id="factory-surface"/);assert.match(html,/id="factory-openai-platform"/);
 node('#btn-factory-models-default').handlers.click();assert.match(node('#factory-models').value,/"protocol": "generate"/);assert.match(node('#factory-models').value,/gemini-4-carbon/);node('#factory-detail').checked=true;node('#factory-compatibility').checked=false;await node('#btn-save-settings').handlers.click();
 let save=calls.find(x=>x.method==='PUT');assert.equal(save.url,'/api/settings?provider=factory');assert.equal(save.body.factory.detailLog,true);assert.equal(save.body.factory.compatibility,false);assert.equal(save.body.factory.inferenceUrl,'http://127.0.0.1:9998');assert.equal(save.body.factory.requestSurface,'cli_exec');assert.equal(save.body.factory.openaiPlatform,'org-web');assert.equal(save.body.factory.models[0].id,'gemini-4-carbon');assert.ok(!save.body.deploy&&!save.body.mistral&&!save.body.altapi);
 // Amp fast-path switch round-trips; unset (default) is on.
 assert.equal(save.body.factory.ampSkipPromptRewrite,true);assert.match(html,/id="factory-amp-skip-rewrite"/);
 settings.factory.ampSkipPromptRewrite=false;await context.loadSettings();assert.equal(node('#factory-amp-skip-rewrite').checked,false);
 calls.length=0;await node('#btn-save-settings').handlers.click();save=calls.find(x=>x.method==='PUT');assert.equal(save.body.factory.ampSkipPromptRewrite,false);
 calls.length=0;vm.runInContext('pool="opendesign"',context);context.loadSettings=async()=>{};await node('#btn-save-settings').handlers.click();save=calls.find(x=>x.method==='PUT');assert.ok(!save.body.factory);
 assert.match(html,/id="page-pools"/);assert.match(source,/if \(!poolEnabled\(pool\)\) page = 'pools'/);
 console.log('Factory settings/quota/isolation web tests passed');
})().catch(e=>{console.error(e);process.exitCode=1;});
