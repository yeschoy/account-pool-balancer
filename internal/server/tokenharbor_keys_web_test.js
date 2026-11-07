const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'web', 'app.js'), 'utf8');
const tick = () => new Promise(setImmediate);

function environment(api) {
  const nodes = new Map();
  const node = (id) => {
    if (!nodes.has(id)) nodes.set(id, { value: '', textContent: '', disabled: false, listeners: {},
      addEventListener(event, fn) { this.listeners[event] = fn; }, replaceChildren() { this.cleared = true; } });
    return nodes.get(id);
  };
  let options, close;
  const dlg = {};
  const copied = [];
  const context = { $: node, api, copyText: (text) => copied.push(text), modal: (opts) => {
    options = opts;
    const wait = new Promise((resolve) => { close = () => { dlg._onClose(); resolve('close'); }; });
    opts.onOpen(dlg);
    return wait;
  } };
  vm.createContext(context);
  const begin = source.indexOf('async function showStoredAPIKeys(');
  const end = source.indexOf('// ---- auth ----', begin);
  assert.ok(begin > 0 && end > begin);
  vm.runInContext(source.slice(begin, end), context);
  return { context, node, nodes, copied, options: () => options, close: () => close() };
}

async function testBoundedOrderedPartialAndCleanup() {
  let active = 0, peak = 0;
  const waiting = [], calls = [];
  const e = environment((method, url) => {
    calls.push({method,url}); active++; peak = Math.max(peak, active);
    return new Promise((resolve, reject) => waiting.push({url, release: () => {
      active--;
      if (url === '/keys/1') reject(new Error('reflected-private-secret'));
      else resolve({runtimeKey: url === '/keys/3' ? 'mock-key-0' : 'mock-key-' + url.split('/').at(-1)});
    }}));
  });
  const entries = Array.from({length:7}, (_, i) => ({name:'account-' + i, path:'/keys/' + i}));
  entries.push({name:'<script>no-key</script>', path:null});
  const work = e.context.showStoredAPIKeys(entries);
  assert.equal(calls.length, 3);
  assert.equal(e.node('#modal-actions button[value="copy"]').disabled, true);
  while (waiting.length) {
    waiting.splice(0).reverse().forEach((r) => r.release());
    await tick();
  }
  assert.equal(peak, 3);
  assert.equal(calls.length, 7, 'one read per keyed row; no retry/create');
  assert.ok(calls.every((r) => r.method === 'GET'));
  assert.match(e.node('#stored-keys-progress').textContent, /成功 6 · 失败 1 · 跳过 1 · 去重后 5/);
  assert.equal(e.node('#stored-keys-output').value, 'mock-key-0\nmock-key-2\nmock-key-4\nmock-key-5\nmock-key-6');
  assert.doesNotMatch(e.node('#stored-keys-errors').textContent, /reflected-private-secret/);
  assert.match(e.node('#stored-keys-errors').textContent, /<script>no-key<\/script>/, 'labels assigned as text, not HTML');
  assert.equal(e.copied.length, 0, 'retrieval alone does not modify clipboard');
  assert.equal(e.node('#modal-actions button[value="copy"]').disabled, false);
  assert.equal(e.options().buttons[1].action(), false);
  assert.equal(e.copied[0], e.node('#stored-keys-output').value);
  e.close(); await work;
  assert.equal(e.node('#stored-keys-output').value, '');
  assert.equal(e.node('#stored-keys-errors').textContent, '');
  assert.equal(e.node('#modal-body').cleared, true);
  e.options().buttons[1].action();
  assert.equal(e.copied.length, 1, 'closed modal retains no secret in handler');
}

async function testCloseBeforeReadCompletes() {
  const pending = [], calls = [];
  const e = environment((method, url) => { calls.push(url); return new Promise((resolve) => pending.push(resolve)); });
  const work = e.context.showStoredAPIKeys(Array.from({length:9}, (_, i) => ({name:String(i),path:'/keys/' + i})));
  e.close(); await work;
  pending.forEach((resolve) => resolve({runtimeKey:'late-secret'}));
  await tick();
  assert.equal(calls.length, 3, 'closing stops scheduling remaining reads');
  assert.equal(e.node('#stored-keys-output').value, '');
  assert.equal(e.copied.length, 0);
}

async function testPoolSelectionScope() {
  const e = environment(async () => { throw new Error('unused'); });
  let shown;
  Object.assign(e.context, {
    managedKeyPool: () => true, isTokenHarbor: () => true, pool:'tokenharbor',
    accountsCache:[{id:'a/1',name:'one',group:'g1',provider:'tokenharbor'}, {id:'other-group',group:'g2',provider:'tokenharbor'}, {id:'zen',group:'g1',provider:'opencode-zen'}],
    ocSelected:new Set(['a/1','other-group','zen','deleted']),
    showStoredAPIKeys: async (entries) => { shown = entries; }, toast: (message) => { throw new Error(message); },
  });
  e.node('#acc-group-filter').value = 'g1';
  const start = source.indexOf("$('#oc-batch').addEventListener('click'");
  const end = source.indexOf('async function loadAccounts()', start);
  vm.runInContext(source.slice(start, end), e.context);
  const button = {dataset:{ocBatch:'keys'},disabled:false};
  await e.node('#oc-batch').listeners.click({target:{closest: () => button}});
  assert.equal(shown.length, 1);
  assert.equal(shown[0].path, '/api/accounts/a%2F1/keys');
  assert.equal(button.disabled, false);
  assert.match(source, /data-account-copy-key/);
  const html = fs.readFileSync(path.join(__dirname, 'web', 'index.html'), 'utf8');
  assert.match(html, /id="th-batch-keys"/);
  assert.match(html, /data-oc-batch="keys"/);
}

(async () => {
  await testBoundedOrderedPartialAndCleanup();
  await testCloseBeforeReadCompletes();
  await testPoolSelectionScope();
  console.log('TokenHarbor explicit API key copy tests passed');
})().catch((error) => { console.error(error); process.exitCode = 1; });
