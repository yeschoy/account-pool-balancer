const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, 'web/app.js'), 'utf8');
const html = fs.readFileSync(path.join(__dirname, 'web/index.html'), 'utf8');
function element(dataset = {}) {
  const classes = new Set();
  return {
    dataset, value: '', textContent: '', listeners: {},
    addEventListener(event, handler) { this.listeners[event] = handler; },
    removeAttribute() {},
    classList: {
      toggle(name, on) { if (on) classes.add(name); else classes.delete(name); },
      contains(name) { return classes.has(name); },
    },
  };
}
const nodes = new Map();
const node = selector => {
  if (!nodes.has(selector)) nodes.set(selector, element());
  return nodes.get(selector);
};
const anchors = [...html.matchAll(/<a data-page="([^"]+)"([^>]*)>/g)].map(match =>
  element({ page: match[1], poolNav: match[2].match(/data-pool-nav="([^"]+)"/)?.[1] }));
const separators = [...html.matchAll(/<div class="nav-sep[^"]*" data-pool-nav="([^"]+)"/g)].map(match => element({ poolNav: match[1] }));
const sections = anchors.map(anchor => Object.assign(element(), { id: 'page-' + anchor.dataset.page }));
const timers = new Map();
let nextTimer = 1;
const loaded = [];
const context = {
  $: node,
  $$: selector => {
    if (selector === '#nav a' || selector === '#nav a[data-page]') return anchors;
    if (selector === '.page') return sections;
    const owner = selector.match(/^#nav \[data-pool-nav="([^"]+)"\]$/)?.[1];
    return owner ? [...anchors, ...separators].filter(el => el.dataset.poolNav === owner) : [];
  },
  document: { title: '', body: { dataset: {} } },
  history: { replaceState(_state, _unused, hash) { context.hash = hash; } },
  localStorage: { getItem: () => 'opendesign', setItem() {} },
  window: { POOL_DEPLOY: { init() { loaded.push('initDeployPage'); } } },
  currentPage: 'overview', accountsCache: [], ocSelected: new Set(), refreshTimer: null,
  logState: {},
  setInterval(fn) { const id = nextTimer++; timers.set(id, fn); return id; },
  clearInterval(id) { timers.delete(id); },
  stopPerchRegistrationPolling() {}, stopTHPolling() {}, stopMistralPolling() {},
  startX50Polling() {}, stopX50Polling() {}, startTelnyxPolling() {}, stopTelnyxPolling() {},
  startALTPolling() {}, stopALTPolling() {},
  startALTAutoPolling() {}, stopALTAutoPolling() {},
  startDeployPolling() {}, stopDeployPolling() {},
  toast(message) { throw new Error(message); },
};
for (const name of ['loadOverview', 'loadAccounts', 'loadKeys', 'loadSettings', 'loadLogs',
  'loadPerchOverview', 'loadPerchAccounts', 'loadPerchRequests', 'loadPerchRegistrations',
  'loadPerchAutosignup', 'loadPerchKeys', 'loadPerchSettings', 'loadTHOverview',
  'loadTHRegistrations', 'loadTHSettings', 'loadMistralRegistrations',
  'loadDeployRegistrations', 'loadDeploySettings',
  'loadX50Registrations', 'loadX50Settings', 'loadTelnyxRegistrations', 'loadTelnyxSettings', 'loadPools',
  'loadALTStatus', 'loadALTRegistrations', 'loadALTSettings']) {
  context[name] = async () => { loaded.push(name); };
}
vm.createContext(context);
vm.runInContext(source.slice(source.indexOf('const POOLS ='), source.indexOf('const esc =')), context);
vm.runInContext(source.slice(source.indexOf('const PROXY_PAGES ='), source.indexOf('// ---- global pool lifecycle')), context);
const switchPool = provider => node('#pool-tabs').listeners.click({ target: { closest: () => ({ dataset: { pool: provider } }) } });
const registration = anchors.find(el => el.dataset.page === 'mregister');
const mistralSeparator = separators.find(el => el.dataset.poolNav === 'mistral');

for (const provider of vm.runInContext('POOLS', context)) {
  vm.runInContext(`pool = ${JSON.stringify(provider)}; renderPoolTabs();`, context);
  assert.equal(registration.classList.contains('hidden'), provider !== 'mistral', provider + ' registration visibility');
  assert.equal(mistralSeparator.classList.contains('hidden'), provider !== 'mistral', provider + ' separator visibility');
  const before = loaded.length;
  context.go('mregister');
  // 50X 已具有通用推理管理页，跨池专属深链回到概览。
  assert.equal(context.currentPage, provider === 'mistral' ? 'mregister' : provider === 'perch' ? 'perch' : provider === 'telnyx' ? 'telnyxreg' : 'overview');
  assert.equal(context.hash, '#' + context.currentPage);
  assert.equal(sections.find(el => el.id === 'page-mregister').classList.contains('hidden'), provider !== 'mistral');
  assert.equal(loaded.slice(before).includes('loadMistralRegistrations'), provider === 'mistral', provider + ' deep-link isolation');
}
for (const provider of vm.runInContext('POOLS.filter(p => p !== "mistral")', context)) {
  switchPool('mistral');
  context.go('mregister');
  switchPool(provider);
  assert.equal(context.currentPage, provider === 'perch' ? 'perch' : provider === 'telnyx' ? 'telnyxreg' : 'overview');
  assert.equal(registration.classList.contains('hidden'), true);
  assert.equal(mistralSeparator.classList.contains('hidden'), true);
}
// Deploy registration remains alongside the shared management/routing pages.
for (const provider of vm.runInContext('POOLS', context)) {
  vm.runInContext(`pool = ${JSON.stringify(provider)}; renderPoolTabs();`, context);
  for (const [page, loader] of [['dreg', 'loadDeployRegistrations'], ['dsettings', 'loadDeploySettings']]) {
    assert.equal(anchors.find(el => el.dataset.page === page).classList.contains('hidden'), provider !== 'deploy');
    const before = loaded.length;
    context.go(page);
    assert.equal(context.currentPage, provider === 'deploy' ? page : provider === 'perch' ? 'perch' : provider === 'telnyx' ? 'telnyxreg' : 'overview');
    assert.equal(loaded.slice(before).includes(loader), provider === 'deploy', provider + ' Deploy deep-link isolation');
    assert.equal(loaded.slice(before).includes('initDeployPage'), provider === 'deploy', 'Deploy deep links bind handlers');
  }
}
// 50X owns exactly its two pages: the registration machine and its settings.
for (const provider of vm.runInContext('POOLS', context)) {
  vm.runInContext(`pool = ${JSON.stringify(provider)}; renderPoolTabs();`, context);
  for (const [page, loader] of [['x50reg', 'loadX50Registrations'], ['x50settings', 'loadX50Settings']]) {
    assert.equal(anchors.find(el => el.dataset.page === page).classList.contains('hidden'), provider !== 'x50', provider + ' 50X page visibility');
    const before = loaded.length;
    context.go(page);
    assert.equal(loaded.slice(before).includes(loader), provider === 'x50', provider + ' 50X page isolation');
  }
}

// ALTapi 有自己的推理号池：通用管理页（概览/账号/密钥/路由/日志）在它下面也要
// 可见可用，另外还有两个注册机专属页。这里守的是「通用页不被误隐藏」和
// 「专属页只在 altapi 出现」两件事。
for (const provider of vm.runInContext('POOLS', context)) {
  vm.runInContext(`pool = ${JSON.stringify(provider)}; renderPoolTabs();`, context);
  for (const [page, loader] of [['altreg', 'loadALTRegistrations'], ['altsettings', 'loadALTSettings']]) {
    assert.equal(anchors.find(el => el.dataset.page === page).classList.contains('hidden'), provider !== 'altapi', provider + ' ALTapi page visibility');
    const before = loaded.length;
    context.go(page);
    assert.equal(loaded.slice(before).includes(loader), provider === 'altapi', provider + ' ALTapi page isolation');
  }
}
// 通用页在任何池下都不能被隐藏（包括 altapi）。
for (const provider of vm.runInContext('POOLS', context)) {
  vm.runInContext(`pool = ${JSON.stringify(provider)}; renderPoolTabs();`, context);
  for (const page of ['overview', 'accounts', 'keys', 'routing', 'requests', 'errors', 'details']) {
    const el = anchors.find(x => x.dataset.page === page);
    assert.ok(el, '缺少通用页 ' + page);
    assert.equal(el.classList.contains('hidden'), false, provider + ' 下通用页 ' + page + ' 不该隐藏');
  }
}

// Existing provider-owned registration links retain their original scope.
for (const provider of ['perch', 'tokenharbor', 'mistral', 'deploy']) {
  switchPool(provider);
  for (const page of ['pregister', 'thregister', 'mregister', 'dreg', 'dsettings']) {
    const owner = { pregister: 'perch', thregister: 'tokenharbor', mregister: 'mistral', dreg: 'deploy', dsettings: 'deploy' }[page];
    assert.equal(anchors.find(el => el.dataset.page === page).classList.contains('hidden'), provider !== owner);
  }
}
assert.match(html, /<div class="nav-sep hidden" data-pool-nav="mistral" hidden>/);
assert.match(html, /<a data-page="mregister" data-pool-nav="mistral" class="hidden" hidden>/);

// Real registration loader must stop polling on a pool switch, including a
// switch while the initial asynchronous page load is still in flight.
vm.runInContext(source.slice(source.indexOf('let mistralPollTimer ='), source.indexOf('async function loadMistralStatus()')), context);
context.loadMistralStatus = async () => {};
context.loadMistralSettings = async () => {};
context.loadMistralCandidates = async () => {};
(async () => {
  vm.runInContext('pool = "mistral"; currentPage = "mregister";', context);
  await context.loadMistralRegistrations();
  const timer = vm.runInContext('mistralPollTimer', context);
  assert.ok(timers.has(timer));
  switchPool('perch');
  assert.equal(timers.has(timer), false, 'leaving Mistral clears its polling timer');
  assert.equal(vm.runInContext('mistralPollTimer', context), null);

  let resume;
  context.loadMistralStatus = () => new Promise(resolve => { resume = resolve; });
  vm.runInContext('pool = "mistral"; currentPage = "mregister";', context);
  const loading = context.loadMistralRegistrations();
  switchPool('opendesign');
  resume();
  await loading;
  assert.equal(vm.runInContext('mistralPollTimer', context), null, 'late completion cannot restart Mistral polling');
  vm.runInContext('poolStates = { factory: false }; pool = "factory";', context);
  context.go('accounts');
  assert.equal(context.currentPage, 'pools', 'disabled pool lands on always-visible controls');
  vm.runInContext('poolStates = Object.fromEntries(POOLS.map(p => [p, false]));', context);
  context.go('pools');
  assert.equal(context.currentPage, 'pools', 'all-off must retain recovery controls');
  vm.runInContext('poolStates = {}; pool = "opendesign";', context);
  let calls = 0;
  context.loadMistralStatus = async () => { calls++; };
  await context.loadMistralRegistrations();
  assert.equal(calls, 0, 'other pools cannot directly load Mistral registration');
  assert.ok(vm.runInContext('POOLS.includes("deploy")', context));
  assert.match(html, /data-pool="deploy" role="tab">Deploy 号池/);
  console.log(`pool navigation web tests passed (${vm.runInContext('POOLS.length', context)} pools, deep links, switching, polling)`);
})().catch(error => { console.error(error); process.exitCode = 1; });
