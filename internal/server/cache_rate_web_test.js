const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'web/app.js'), 'utf8');
const html = fs.readFileSync(path.join(__dirname, 'web/index.html'), 'utf8');
const button = { disabled: false, addEventListener(_name, fn) { this.handler = fn; } };
const context = {
  $: () => button, localStorage: { getItem: () => 'mistral' },
  statusClass: () => '', statusLabel: () => '正常', windowsHTML: () => '',
  ocSelected: new Set(), fmtTime: () => '', toast() {},
};
vm.createContext(context);
vm.runInContext(source.slice(source.indexOf('const POOLS ='), source.indexOf('const TZ =')), context);
vm.runInContext(source.slice(source.indexOf('function hitRate('), source.indexOf('function costCell(')), context);
vm.runInContext(source.slice(source.indexOf('const effLimit ='), source.indexOf("$('#acc-table').addEventListener")), context);
vm.runInContext(source.slice(source.indexOf('async function resetCacheRate('), source.indexOf('async function loadOverview(')), context);
for (const provider of ['mistral', 'tokenharbor', 'opencode-go', 'opendesign']) {
  vm.runInContext(`pool = '${provider}'`, context);
  assert.equal(context.hitRate({ p: 1000, k: 999 }), null); // missing measurements are not zero or fabricated hits
  assert.equal(context.hitRate({ cp: 100, ck: 80, ckr: 2, p: 1000, k: 999 }), 0.8);
  assert.equal(context.hitRate({ cp: 100, ck: 0, ckr: 1 }), 0);
  assert.equal(context.hitRate({ cp: 0, ck: 0, ckr: 1 }), null);
  assert.equal(context.hitRate({ cp: 100, ck: 101, ckr: 1 }), 1);
}
assert.match(context.accountCacheRateHTML({ cp: 100, ck: 80, ckr: 2 }), /80\.0%.*已测缓存 80 \/ 提示 100.*样本 2 次/s);
assert.match(context.accountCacheRateHTML({}), />—</);
const row = context.accountRow({ id: 'm1', name: '<unsafe>', group: 'mistral', provider: 'mistral', priority: 50, runtime: { state: {}, active: 0, rpm: 0 }, usage24h: { cp: 100, ck: 75, ckr: 1 } });
assert.match(row, /75\.0%/);
assert.doesNotMatch(row, /<unsafe>/);
assert.equal((row.match(/<td/g) || []).length, 10);
assert.match(html, /id="btn-reset-cache-rate"/);
assert.match(html, /24h 缓存命中率<\/th>/);
assert.match(source, /会话头、缓存键按收到的原值展示/);
(async () => {
  const requests = [], refreshes = [], messages = [];
  vm.runInContext('pool = "mistral"', context);
  context.modal = async () => 'cancel';
  context.api = async (...args) => requests.push(args);
  context.loadOverview = async () => refreshes.push('overview');
  context.loadAccounts = async () => refreshes.push('accounts');
  context.toast = text => messages.push(text);
  await button.handler();
  assert.equal(requests.length, 0);
  assert.equal(button.disabled, false);
  context.modal = async options => { assert.match(options.body, /不会清除上游缓存/); return 'reset'; };
  await button.handler();
  assert.deepEqual(requests[0], ['POST', '/api/stats/reset-cache-rate?provider=mistral']);
  assert.deepEqual(refreshes, ['overview', 'accounts']);
  assert.equal(button.disabled, false);
  // Switching tabs during confirmation must not reset the newly selected pool.
  context.modal = async () => { vm.runInContext('pool = "river"', context); return 'reset'; };
  await button.handler();
  assert.equal(requests[1][1], '/api/stats/reset-cache-rate?provider=mistral');
  assert.equal(refreshes.length, 2);
  context.modal = async () => 'reset';
  context.api = async () => { throw new Error('保存失败'); };
  await button.handler();
  assert.equal(messages.at(-1), '保存失败');
  assert.equal(button.disabled, false);
  console.log('cache rate UI tests passed');
})().catch(error => { console.error(error); process.exitCode = 1; });
