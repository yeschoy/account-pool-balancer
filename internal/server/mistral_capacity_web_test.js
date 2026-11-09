const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'web/app.js'), 'utf8');
const html = fs.readFileSync(path.join(__dirname, 'web/index.html'), 'utf8');
const nodes = new Map();
function node(id) {
  if (!nodes.has(id)) nodes.set(id, { value: '', checked: false, innerHTML: '', textContent: '', classList: { toggle() {} }, handlers: {}, addEventListener(k, f) { this.handlers[k] = f; } });
  return nodes.get(id);
}
const perf = { maxBufferedMB: 128, maxWaitingRequests: 16, uploadTimeoutSec: 40, writeTimeoutSec: 20, detailSamplePercent: 25 };
const defaults = { maxBufferedMB: 2048, maxWaitingRequests: 1024, uploadTimeoutSec: 60, writeTimeoutSec: 30, detailSamplePercent: 100 };
const settings = { revision: 3, upstream: {}, routing: { strategy: 'smart', sessionHeaders: [], directMaxConns: 7, proxyMaxConns: 3 }, quota: {}, logs: {}, pricing: { models: [] }, mistral: { baseUrl: 'http://mock.invalid/v1', bindingCapacity: 100000, performance: perf } };
const calls = [];
const rendered = {};
const c = {
  $: node, pool: 'mistral', settingsCache: null, errRules: [], retRules: [], priceRows: [], POOL_LABEL: { mistral: 'Mistral' },
  isMistral() { return c.pool === 'mistral'; }, isTokenHarbor() { return false; }, isOpenCode() { return false; },
  withPool(p) { return p + '?provider=' + c.pool; }, clone: structuredClone, esc: String, renderRules() {}, renderPrices() {}, readPricing() { return { models: [] }; }, toast() {},
  api: async (method, url, body) => {
    calls.push({ method, url, body: body && structuredClone(body) });
    if (method === 'PUT') return { ok: true };
    if (url.startsWith('/api/overview')) return { groups: ['mistral'] };
    return { settings: structuredClone(settings), defaults: { mistralPerformance: defaults }, strategies: ['smart'] };
  },
};
vm.createContext(c);
vm.runInContext(source.slice(source.indexOf('const STRATEGY_HINTS'), source.indexOf('// ---- error / retry rules')) + '\nglobalThis.performanceFields=MISTRAL_PERFORMANCE_FIELDS;globalThis.routeFields=routingFields;', c);
c.renderForm = (_el, fields, obj, prefix) => {
  rendered[prefix] = structuredClone(obj);
  for (const [key,,type] of fields) {
    const n = node('#' + prefix + '-' + key), v = obj[key];
    n.checked = !!v; n.value = type === 'list' ? (v || []).join(', ') : String(v ?? '');
  }
};
vm.runInContext(source.slice(source.indexOf('async function loadSettings()'), source.indexOf("$('#btn-default-rules').addEventListener")), c);
(async () => {
  await c.loadSettings();
  assert.equal(node('#mistral-binding-capacity').value, 100000);
  assert.deepEqual(rendered.mp, perf);
  assert.ok(c.routeFields().some(f => f[0] === 'directMaxConns'));
  assert.ok(c.routeFields().some(f => f[0] === 'proxyMaxConns'));
  node('#mistral-binding-capacity').value = '200000';
  node('#mp-maxWaitingRequests').value = '0';
  node('#mp-detailSamplePercent').value = '0';
  const handler = node('#btn-save-settings').handlers.click;
  await handler();
  let save = calls.find(x => x.method === 'PUT');
  assert.equal(save.url, '/api/settings?provider=mistral');
  assert.equal(save.body.mistral.bindingCapacity, 200000);
  assert.equal(save.body.mistral.performance.maxWaitingRequests, 0);
  assert.equal(save.body.mistral.performance.detailSamplePercent, 0);
  assert.equal(save.body.routing.directMaxConns, 7);
  await c.loadSettings(); // the saved server document, not a stale UI default
  assert.equal(node('#mistral-binding-capacity').value, 100000);
  settings.mistral.performance = undefined;
  settings.mistral.bindingCapacity = undefined;
  await c.loadSettings();
  assert.equal(node('#mistral-binding-capacity').value, 0);
  assert.deepEqual(rendered.mp, defaults);
  c.pool = 'opendesign';
  calls.length = 0;
  c.loadSettings = async () => {};
  await handler();
  save = calls.find(x => x.method === 'PUT');
  assert.ok(!('mistral' in save.body));
  assert.ok(!c.routeFields().some(f => f[0] === 'directMaxConns'));
  assert.match(html, /id="mistral-binding-capacity"[^>]*max="1000000"/);
  assert.match(html, /不是每账号会话数或请求并发/);
  assert.match(html, /id="form-mistral-performance"/);
  assert.match(source, /bindingIndependent/);
  console.log('Mistral capacity/settings web tests passed');
})().catch(e => { console.error(e); process.exitCode = 1; });
