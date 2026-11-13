'use strict';
// OpenDesign 账号池管理台。所有渲染都经 esc()，不使用内联脚本或样式属性（CSP）。

const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => Array.from(el.querySelectorAll(s));
let csrf = '';
let accountsCache = [];
const ocSelected = new Set();
let settingsCache = null;
let currentPage = 'overview';
let refreshTimer = null;
// the pool tab on top: every list, overview and log shows one pool only
const POOLS = ['opendesign', 'river', 'ollama', 'modal', 'mistral', 'tokenharbor', 'modelstudio', 'deploy', 'perch', 'opencode-zen', 'opencode-go'];
let pool = POOLS.includes(localStorage.getItem('odb-pool')) ? localStorage.getItem('odb-pool') : 'opendesign';
const POOL_LABEL = { opendesign: 'OpenDesign', river: 'River', ollama: 'Ollama', modal: 'Modal', mistral: 'Mistral', tokenharbor: 'Token Harbor', modelstudio: '百炼 Model Studio', deploy: 'Deploy', perch: 'Perch', 'opencode-zen': 'OpenCode Zen', 'opencode-go': 'OpenCode Go' };
// the default group of each pool, and what its keys look like
const POOL_GROUP = { opendesign: 'default', river: 'river', ollama: 'ollama', modal: 'modal', mistral: 'mistral', tokenharbor: 'tokenharbor', modelstudio: 'modelstudio', deploy: 'deploy', perch: 'perch', 'opencode-zen': 'opencode-zen', 'opencode-go': 'opencode-go' };
const defaultGroup = () => POOL_GROUP[pool] || 'default';
const isOllama = () => pool === 'ollama';
const isOpenDesign = () => pool === 'opendesign';
// Declare opaque-key pools and the managed native-key pool explicitly.
// Token Harbor also has a verifiable thk_ prefix.
const isOpenCode = () => pool === 'opencode-zen' || pool === 'opencode-go';
const isTokenHarbor = () => pool === 'tokenharbor';
const isMistral = () => pool === 'mistral';
const isModelStudio = () => pool === 'modelstudio';
const isDeploy = () => pool === 'deploy';
const managedKeyPool = () => isOpenCode() || isTokenHarbor() || isMistral() || isModelStudio() || isDeploy();
const keyNeedsProvider = () => isOllama() || managedKeyPool();
const RT_HINT = { opendesign: 'sk-amr-…', river: 'rv_…', ollama: 'ollama.com/settings/keys 签发的 key 原样粘贴',
  deploy: 'Deploy Secret Key（原样粘贴，不是 access_key）', mistral: 'Mistral API Key（原样粘贴，支持 mstrl_…）', tokenharbor: 'thk_live_…（Token Harbor API Key）', modelstudio: 'sk-sp-…（Token Plan）或 sk-…（按量付费），完整粘贴', modal: 'wk-<id>.ws-<secret>（Proxy Token 的完整形态，两段都要）', 'opencode-zen': 'OpenCode Zen API Key（原样粘贴）', 'opencode-go': 'OpenCode Go API Key（原样粘贴）' };
const withPool = (path) => path + (path.includes('?') ? '&' : '?') + 'provider=' + pool;

const esc = (v) => String(v ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const num = (v) => (v === null || v === undefined || Number.isNaN(v)) ? '—' : Number(v).toLocaleString('en-US');
const pct = (v) => (v === null || v === undefined) ? '—' : (v * 100).toFixed(1) + '%';
const usd = (s) => { const f = parseFloat(s); return Number.isFinite(f) ? '$' + f.toFixed(2) : '—'; };
const TZ = { timeZone: 'Asia/Shanghai', hour12: false };
function fmtTime(iso, short) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return '—';
  if (short) return d.toLocaleString('zh-CN', { ...TZ, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
  return d.toLocaleString('zh-CN', { ...TZ, year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' });
}
function fmtIn(iso) {
  const ms = new Date(iso).getTime() - Date.now();
  if (!Number.isFinite(ms)) return '';
  const abs = Math.abs(ms), m = Math.round(abs / 60000);
  const txt = m < 60 ? m + ' 分钟' : m < 2880 ? Math.round(m / 60) + ' 小时' : Math.round(m / 1440) + ' 天';
  return ms >= 0 ? txt + '后' : txt + '前';
}
function fmtDur(sec) {
  if (sec % 86400 === 0) return sec / 86400 + ' 天';
  if (sec % 3600 === 0) return sec / 3600 + ' 小时';
  return Math.round(sec / 60) + ' 分钟';
}
// navigator.clipboard only exists on https/localhost; the admin is often served over plain http.
function copyText(text) {
  const ok = () => toast('已复制');
  const legacy = () => {
    const ta = document.createElement('textarea');
    ta.value = text; ta.style.cssText = 'position:fixed;opacity:0';
    document.body.appendChild(ta); ta.select();
    let done = false;
    try { done = document.execCommand('copy'); } catch (e) { /* fall through */ }
    ta.remove();
    done ? ok() : toast('复制失败，请手动选择密钥');
  };
  if (navigator.clipboard && window.isSecureContext) navigator.clipboard.writeText(text).then(ok, legacy); else legacy();
}

// toast shows a short status line. An optional second argument makes a failure
// stay readable: errors keep their text longer than an acknowledgement, so a
// refused request is not missed the way a 2.6s flash would be.
function toast(msg, kind) {
  const t = $('#toast');
  const failed = kind === 'error';
  t.textContent = msg;
  t.classList.toggle('toast-error', failed);
  t.classList.remove('hidden');
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => {
    t.classList.add('hidden');
    t.classList.remove('toast-error');
  }, failed ? 8000 : 2600);
}
function applyWidths(root) {
  $$('[data-w]', root).forEach((el) => { el.style.width = Math.max(0, Math.min(100, parseFloat(el.dataset.w))) + '%'; });
  $$('[data-h]', root).forEach((el) => { el.style.height = Math.max(0, Math.min(100, parseFloat(el.dataset.h))) + '%'; });
}

async function api(method, path, body) {
  const opt = { method, headers: { Accept: 'application/json' }, credentials: 'same-origin' };
  if (method !== 'GET') opt.headers['X-CSRF-Token'] = csrf;
  if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
  const res = await fetch(path, opt);
  let data = null;
  try { data = await res.json(); } catch { data = null; }
  if (res.status === 401 && !path.startsWith('/api/login') && !path.startsWith('/api/password')) { showAuth(); throw new Error('请先登录'); }
  if (!res.ok) throw new Error((data && data.error) || ('HTTP ' + res.status));
  return data;
}

// ---- modal ----
function modal({ title, body, buttons = [{ label: '关闭', value: 'close' }], wide = false, onOpen }) {
  const dlg = $('#modal');
  dlg.classList.toggle('wide', wide);
  $('#modal-title').textContent = title;
  $('#modal-body').innerHTML = body;
  $('#modal-error').textContent = '';
  const actions = $('#modal-actions');
  actions.innerHTML = buttons.map((b, i) => `<button class="btn ${b.primary ? 'primary' : ''} ${b.danger ? 'danger' : ''}" value="${esc(b.value)}" data-i="${i}">${esc(b.label)}</button>`).join('');
  applyWidths(dlg);
  return new Promise((resolve) => {
    const handler = async (ev) => {
      const btn = ev.target.closest('button[data-i]');
      if (!btn) return;
      ev.preventDefault();
      const b = buttons[Number(btn.dataset.i)];
      if (b.action) {
        btn.disabled = true;
        try {
          const r = await b.action();
          if (r === false) { btn.disabled = false; return; }
        } catch (e) {
          $('#modal-error').textContent = e.message;
          btn.disabled = false;
          return;
        }
      }
      cleanup();
      dlg.close();
      resolve(b.value);
    };
    const onClose = () => { cleanup(); resolve('close'); };
    function cleanup() { actions.removeEventListener('click', handler); dlg.removeEventListener('close', onClose); if (dlg._onClose) dlg._onClose(); dlg._onClose = null; }
    actions.addEventListener('click', handler);
    dlg.addEventListener('close', onClose);
    dlg.showModal();
    if (onOpen) onOpen(dlg);
  });
}
$('#modal-form').addEventListener('submit', (e) => e.preventDefault());
const modalErr = (msg) => { $('#modal-error').textContent = msg; };
function showJSON(title, obj) { modal({ title, body: `<pre>${esc(JSON.stringify(obj, null, 2))}</pre>`, wide: true }); }

// Explicit secret read only: no polling/cache/localStorage, no upstream key creation.
// Both pool and registration batches use bounded reads in the captured selection order.
async function showStoredAPIKeys(entries, title = '批量获取 / 复制 API Key') {
  let closed = false, next = 0;
  const results = new Array(entries.length);
  const keys = () => [...new Set(results.filter((r) => r?.key).map((r) => r.key))];
  const render = () => {
    if (closed) return;
    const done = results.filter(Boolean);
    const success = done.filter((r) => r.key).length;
    const failed = done.filter((r) => r.status === 'failed').length;
    const skipped = done.filter((r) => r.status === 'skipped').length;
    $('#stored-keys-progress').textContent = `已处理 ${done.length}/${entries.length} · 成功 ${success} · 失败 ${failed} · 跳过 ${skipped} · 去重后 ${keys().length} 把 Key`;
    $('#stored-keys-output').value = keys().join('\n');
    $('#stored-keys-errors').textContent = results.map((r, i) => r && !r.key ? `${entries[i].name}：${r.status === 'skipped' ? '尚无已保存 Key，已跳过' : '获取失败，请核对账号后手动重试'}` : '').filter(Boolean).join('\n');
    $('#modal-actions button[value="copy"]').disabled = done.length !== entries.length || success === 0;
  };
  const worker = async () => {
    while (!closed && next < entries.length) {
      const i = next++, entry = entries[i];
      let result = { status: 'skipped' };
      if (entry.path) {
        try {
          const out = await api('GET', entry.path);
          if (typeof out?.runtimeKey !== 'string' || !out.runtimeKey.trim() || /[\r\n]/.test(out.runtimeKey)) throw new Error('missing key');
          result = { status: 'success', key: out.runtimeKey };
        } catch { result = { status: 'failed' }; }
      }
      if (closed) return;
      results[i] = result;
      render();
    }
  };
  await modal({ title, wide: true,
    body: '<p class="muted small">只读取已保存的 API Key，不会创建、替换密钥或启动注册。每行一把，按所选顺序去重；失败不自动重试。关闭窗口即清除本页明文（剪贴板内容需自行清除）。</p><p id="stored-keys-progress" role="status"></p><textarea id="stored-keys-output" rows="10" readonly autocomplete="off" spellcheck="false" aria-label="已获取的 API Key"></textarea><pre id="stored-keys-errors" class="muted small"></pre>',
    buttons: [{ label: '关闭', value: 'close' }, { label: '复制全部 API Key', value: 'copy', primary: true, action: () => { if (keys().length) copyText(keys().join('\n')); return false; } }],
    onOpen: (dlg) => {
      dlg._onClose = () => {
        closed = true;
        results.fill(null);
        $('#stored-keys-output').value = '';
        $('#stored-keys-errors').textContent = '';
        $('#modal-body').replaceChildren();
      };
      render();
      void Promise.all(Array.from({ length: Math.min(3, entries.length) }, worker));
    },
  });
}

// ---- auth ----
let authMode = 'login';
async function boot() {
  let s;
  try { s = await api('GET', '/api/session'); } catch (e) { toast(e.message); return; }
  if (s.authenticated) { csrf = s.csrf; showApp(); return; }
  authMode = s.initialized ? 'login' : 'setup';
  showAuth();
}
function showAuth() {
  accountsCache = [];
  $('#acc-table tbody').replaceChildren();
  $('#modal').close();
  $('#modal-body').replaceChildren();
  $('#app').classList.add('hidden');
  $('#auth').classList.remove('hidden');
  const setup = authMode === 'setup';
  $('#auth-hint').textContent = setup ? '首次使用：设置管理员密码（至少 8 位）。' : '请输入管理员密码。';
  $('#auth-password2').classList.toggle('hidden', !setup);
  $('#auth-password2').required = setup;
  $('#auth-password').autocomplete = setup ? 'new-password' : 'current-password';
  $('#auth-submit').textContent = setup ? '设置并进入' : '登录';
  $('#auth-password').focus();
}
$('#auth-form').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  $('#auth-error').textContent = '';
  const pw = $('#auth-password').value;
  if (authMode === 'setup' && pw !== $('#auth-password2').value) { $('#auth-error').textContent = '两次输入不一致'; return; }
  try {
    const r = await api('POST', authMode === 'setup' ? '/api/setup' : '/api/login', { password: pw });
    csrf = r.csrf;
    $('#auth-password').value = $('#auth-password2').value = '';
    showApp();
  } catch (e) { $('#auth-error').textContent = e.message; }
});
$('#btn-logout').addEventListener('click', async () => { await api('POST', '/api/logout').catch(() => {}); csrf = ''; authMode = 'login'; showAuth(); });
$('#btn-password').addEventListener('click', () => {
  modal({
    title: '修改管理员密码',
    body: `<div class="form"><label>原密码</label><input id="pw-old" type="password"><label>新密码</label><input id="pw-new" type="password" minlength="8"></div>`,
    buttons: [{ label: '取消', value: 'c' }, { label: '保存', value: 'ok', primary: true, action: async () => {
      const r = await api('POST', '/api/password', { old: $('#pw-old').value, new: $('#pw-new').value });
      csrf = r.csrf; toast('密码已修改');
    } }],
  });
});

function showApp() {
  $('#auth').classList.add('hidden');
  $('#app').classList.remove('hidden');
  $('#base-url').textContent = location.origin;
  renderPoolTabs();
  go(location.hash.slice(1) || 'overview');
}

// ---- navigation ----
// The Perch pool runs on sessions, not API keys, and has no routing rules of
// its own; its pages live in perch.js. It still needs both console keys —
// a downstream key for new-api and an import key for the signup bot — so
// `pkeys` is Perch's own page for them rather than the proxy pools' `keys`.
const PROXY_PAGES = ['overview', 'accounts', 'keys', 'routing', 'requests', 'errors', 'details'];
const PERCH_PAGES = ['perch', 'paccounts', 'prequests', 'pregister', 'pautosignup', 'pkeys', 'psettings'];
// Token Harbor is a plain key pool with a registration machine of its own;
// its pages live in tokenharbor.js.
const TH_PAGES = ['thoverview', 'thregister', 'thsettings'];
// Mistral registration belongs only to the Mistral pool.
const MR_PAGES = ['mregister'];
// Deploy 号池：注册机与号池管理（deploy.js）
const DR_PAGES = ['dreg', 'dsettings'];
// perch.js loads after this file, so its loaders are looked up at call time
const pages = { overview: loadOverview, accounts: loadAccounts, keys: loadKeys, routing: loadSettings, requests: () => loadLogs('requests', true), errors: () => loadLogs('errors', true), details: () => loadLogs('details', true),
  perch: () => loadPerchOverview(), paccounts: () => loadPerchAccounts(), prequests: () => loadPerchRequests(true), pregister: () => loadPerchRegistrations(), pautosignup: () => loadPerchAutosignup(), pkeys: () => loadPerchKeys(), psettings: () => loadPerchSettings(),
  thoverview: () => loadTHOverview(), thregister: () => loadTHRegistrations(), thsettings: () => loadTHSettings(),
  mregister: () => loadMistralRegistrations(),
  dreg: () => loadDeployRegistrations(), dsettings: () => loadDeploySettings() };
function go(page) {
  if (!pages[page]) page = 'overview';
  if (MR_PAGES.includes(page) && !isMistral()) page = pool === 'perch' ? 'perch' : 'overview';
  if (DR_PAGES.includes(page) && !isDeploy()) page = pool === 'perch' ? 'perch' : 'overview';
  if (DR_PAGES.includes(page) && window.POOL_DEPLOY) window.POOL_DEPLOY.init();
  if (currentPage === 'pregister' && page !== currentPage) stopPerchRegistrationPolling();
  if (currentPage === 'thregister' && page !== currentPage && typeof stopTHPolling === 'function') stopTHPolling();
  if (currentPage === 'mregister' && page !== currentPage) stopMistralPolling();
  if (currentPage === 'dreg' && page !== currentPage) stopDeployPolling();
  currentPage = page;
  history.replaceState(null, '', '#' + page);
  $$('#nav a').forEach((a) => a.classList.toggle('active', a.dataset.page === page));
  $$('.page').forEach((p) => p.classList.toggle('hidden', p.id !== 'page-' + page));
  clearInterval(refreshTimer);
  pages[page]().catch((e) => toast(e.message));
  if (page === 'dreg') startDeployPolling();
  if (page === 'overview' || page === 'accounts' || page === 'thoverview') {
    refreshTimer = setInterval(() => { if (!document.hidden && !$('#modal').open) pages[currentPage]().catch(() => {}); }, 15000);
  }
}
function renderPoolTabs() {
  $$('#pool-tabs .pool-tab').forEach((b) => b.classList.toggle('active', b.dataset.pool === pool));
  const isPerch = pool === 'perch';
  const isTH = pool === 'tokenharbor';
  // the sidebar shows one pool's pages at a time. The `hidden` attribute is
  // what the markup uses, and .side nav a overrides it, so the class is what
  // actually toggles visibility.
  $$('#nav a[data-page]').forEach((a) => {
    const page = a.dataset.page;
    const own = PERCH_PAGES.includes(page) || TH_PAGES.includes(page) || MR_PAGES.includes(page) || DR_PAGES.includes(page);
    const mine = (isPerch && PERCH_PAGES.includes(page)) || (isTH && TH_PAGES.includes(page)) || (isMistral() && MR_PAGES.includes(page)) || (isDeploy() && DR_PAGES.includes(page));
    a.classList.toggle('hidden', own ? !mine : false);
    a.removeAttribute('hidden');
  });
  $$('#nav [data-pool-nav="tokenharbor"]').forEach((el) => {
    el.classList.toggle('hidden', !isTH);
    el.removeAttribute('hidden');
  });
  $$('#nav [data-pool-nav="perch"]').forEach((el) => {
    el.classList.toggle('hidden', !isPerch);
    el.removeAttribute('hidden');
  });
  $$('#nav [data-pool-nav="mistral"]').forEach((el) => {
    el.classList.toggle('hidden', !isMistral());
    el.removeAttribute('hidden');
  });
  $$('#nav [data-pool-nav="deploy"]').forEach((el) => {
    el.classList.toggle('hidden', !isDeploy());
    el.removeAttribute('hidden');
  });
  document.title = POOL_LABEL[pool] + ' 账号池';
  document.body.dataset.pool = pool;
  $('#btn-add-device').classList.toggle('hidden', pool !== 'opendesign');
  $('#oc-batch').classList.toggle('hidden', !managedKeyPool());
  $('#oc-batch [data-oc-batch="proxy"]').classList.toggle('hidden', !isOpenCode());
  $('#oc-batch [data-oc-batch="keys"]').classList.toggle('hidden', !isTokenHarbor());
  // the last two columns of the account table mean different things per pool
  const heads = { windows: { opendesign: 'Design Plan 窗口', river: '额度', ollama: '额度（/api/balance）', modal: '可用 Endpoint', mistral: '套餐额度（Studio / Vibe）', tokenharbor: '周限 / 月限余额', modelstudio: '套餐与额度', deploy: '额度与模型发现', 'opencode-zen': '额度（未知）', 'opencode-go': 'Go 订阅窗口' },
    wallet: { opendesign: '钱包 / 套餐模型额度', river: '24h 用量', ollama: '已购额度 / 24h 用量', modal: '24h 用量', mistral: '24h 用量', tokenharbor: '24h 用量', modelstudio: '24h 用量', deploy: '24h 用量', 'opencode-zen': '24h 用量', 'opencode-go': '24h 用量' } };
  $$('#acc-table th[data-head]').forEach((th) => { th.textContent = heads[th.dataset.head][pool] || ''; });
  $('#acc-empty').textContent = {
    deploy: '添加或批量导入 Deploy Secret Key（不是 access_key）；独立分组，仅原生 Chat Completions，目录成功不代表 Key 已激活或推理授权。',
    mistral: '还没有 Mistral 账号。点「批量导入」粘贴 API Key，每行一个；或「手动添加」。',
    modelstudio: '还没有百炼 Model Studio 账号。点「批量导入」粘贴 API Key（sk-sp-… 为 Token Plan，sk-… 为按量付费），每行一个；或「手动添加」。',
    river: '还没有 River 账号。点「批量导入」粘贴 rv_ 开头的 key，每行一个。',
    ollama: '还没有 Ollama 账号。点「批量导入」一行一个 key（ollama.com/settings/keys 签发，本池的 key 不要求前缀），或「手动添加」。',
    modal: '还没有 Modal 账号。点「批量导入」一行一个 Proxy Token（wk-<id>.ws-<secret>，modal.com/settings/proxy-auth-tokens 创建），或「手动添加」。',
    tokenharbor: '添加或批量导入 thk_ 开头的 Key，也可使用 TokenHarbor 注册机。分组独立，会话尽量固定同一 Key，余额未知；请根据实际出口地区配置代理。',
    'opencode-zen': '添加 Zen API Key；独立分组，不会推断或混入 Go 池。请先在设置启用 OpenCode。',
    'opencode-go': '添加 Go API Key；额度是订阅共享百分比，不对多个 Key 求和。请先在设置启用 OpenCode。',
  }[pool] || '还没有账号。点「设备授权添加」，在已登录该账号的浏览器里批准即可。';
}
$('#pool-tabs').addEventListener('click', (ev) => {
  const b = ev.target.closest('.pool-tab');
  if (!b || b.dataset.pool === pool) return;
  pool = b.dataset.pool;
  localStorage.setItem('odb-pool', pool);
  accountsCache = [];
  ocSelected.clear();
  logState.requests = { cursor: '', items: [] }; logState.errors = { cursor: '', items: [] }; logState.details = { cursor: '', items: [] };
  $('#acc-group-filter').value = '';
  renderPoolTabs();
  if (pool === 'deploy' && window.POOL_DEPLOY) window.POOL_DEPLOY.init();
  // switching pools lands on that pool's first page
  const valid = pool === 'perch' ? PERCH_PAGES : pool === 'tokenharbor' ? [...PROXY_PAGES, ...TH_PAGES] : pool === 'mistral' ? [...PROXY_PAGES, ...MR_PAGES] : pool === 'deploy' ? [...PROXY_PAGES, ...DR_PAGES] : PROXY_PAGES;
  go(valid.includes(currentPage) ? currentPage : valid[0]);
});
$('#nav').addEventListener('click', (ev) => { const a = ev.target.closest('a[data-page]'); if (a) go(a.dataset.page); });

// ---- overview ----
const GO_CACHE_LABEL = { hit: '命中缓存偏好', miss: '暂无缓存偏好', 'session-preferred': '优先原会话', 'busy-fallback': '缓存账号忙，另选', unavailable: '缓存账号不可用', invalidated: '来源变化，偏好失效' };
const GO_EGRESS_LABEL = { 'configured-only': '仅绑定出口，IP 未验证', unverified: '待验证', checking: '验证中', verified: 'IP 校验通过', observed: '已观测，未固定 IP', expired: '校验已过期', mismatch: '出口 IP 不符', failed: 'IP 校验失败' };
function goOverviewCards(o) {
  const g = o.go; if (!g) return [];
  return [
    ['Go 当前并发', num(g.inFlight), `已设限账号容量 ${num(g.capacity)} · ${num(g.unlimitedAccounts)} 个账号未限并发`],
    ['Go 额度数据', `${num(g.quotaFresh)} / ${num(o.accounts)}`, `新鲜 / 总账号 · 未知 ${num(g.quotaUnknown)} · 已耗尽 ${num(g.quotaExhausted)}（共享订阅不相加）`],
    ['Go 出口校验', num(g.egress.verified || 0), Object.entries(g.egress).map(([k,v]) => `${GO_EGRESS_LABEL[k] || k} ${num(v)}`).join(' · ')],
    ['Go 风控暂停', num(g.policyPaused), '政策拒绝后暂停当前账号，不换出口绕过'],
    ['Go 缓存亲和', num(g.bindings.cache), `当前偏好记录 · 24h 亲和命中 ${num(o.stats.total.cah || 0)} 次；不是实际缓存读取`],
    ['Go 会话绑定命中率', o.stats.total.ar ? pct((o.stats.total.bh || 0) / o.stats.total.ar) : '—', `会话 ${num(g.bindings.sessions)} · 响应链 ${num(g.bindings.responses)}；仅统计带亲和信号的请求`],
    ['Go 平均耗时', o.stats.total.dn ? num(Math.round((o.stats.total.dm || 0) / o.stats.total.dn)) + 'ms' : '—', o.stats.total.tn ? `平均首事件 ${num(Math.round(o.stats.total.tm / o.stats.total.tn))}ms · ${num(o.stats.total.tn)} 次测量` : '暂无首事件测量'],
  ];
}
function goQuotaWindows(g) {
  if (!g?.quotas?.length) return '<p class="muted">暂无 Go 账号；额度按订阅共享窗口展示，不求和。</p>';
  return g.quotas.map((a) => `<div class="win"><b>${esc(a.name)}</b> <span class="badge ${a.fresh ? '' : 'warn'}">${a.fresh ? '数据新鲜' : '未知 / 旧数据'}</span>${['rolling','weekly','monthly'].map((k) => {
    const w = a.usage?.[k]; if (!w) return `<div class="acc-sub">${({ rolling:'5 小时', weekly:'周', monthly:'月' })[k]}：未知</div>`;
    return `<div class="acc-sub">${({ rolling:'5 小时', weekly:'周', monthly:'月' })[k]}已用 ${num(w.usedPercent)}% · 剩余 ${num(w.remainingPercent)}% · 重置 ${esc(fmtTime(w.resetsAt,true))}</div>`;
  }).join('')}<div class="acc-sub">更新 ${esc(fmtTime(a.usageAt,true))}；多个 Key 可能共享同一订阅，不可相加。</div></div>`).join('');
}
async function resetCacheRate() {
  const provider = pool;
  const button = $('#btn-reset-cache-rate');
  if (button.disabled) return;
  button.disabled = true;
  try {
    const confirmed = await modal({ title: '清空24h缓存命中率', body: `<p>清空 ${esc(POOL_LABEL[provider])} 号池的缓存命中率统计（含各账号、模型），从现在重新累计，最长保留24小时。</p><p>保留请求数、原始 Token 用量、费用、日志及会话绑定；不会清除上游缓存，不影响其他号池。无法撤销。</p>`, buttons: [{ label: '取消', value: 'cancel' }, { label: '确认清空', value: 'reset', danger: true }] });
    if (confirmed !== 'reset') return;
    await api('POST', '/api/stats/reset-cache-rate?provider=' + encodeURIComponent(provider));
    toast(POOL_LABEL[provider] + ' 缓存命中率已清空');
    if (pool === provider) await Promise.all([loadOverview(), loadAccounts()]);
  } catch (e) { toast(e.message); }
  finally { button.disabled = false; }
}
$('#btn-reset-cache-rate').addEventListener('click', resetCacheRate);

async function loadOverview() {
  const o = await api('GET', withPool('/api/overview'));
  const st = o.stats.total;
  $('#ov-egress-title').textContent = pool === 'opencode-go' ? '各固定出口路由 24 小时成功率（不是实测 IP）' : '各出口 IP 24 小时成功率';
  $('#ov-quota-title').textContent = isDeploy() ? 'Deploy 余额与能力边界' : pool === 'opencode-go' ? 'Go 订阅窗口（逐账号展示，共享额度不相加）' : isMistral() ? 'Mistral 套餐预算（请在账号页查看）' : '套餐窗口（按工作区去重）';
  const okRate = st.r ? (st.s / Math.max(1, st.r - st.c)) : null;
  const ok = o.status.ok || 0;
  const cards = [
    ['账号', `${ok} / ${o.accounts}`, `可调度 / 总数${o.authFailed ? ` · ${o.authFailed} 个控制密钥失效` : ''}`],
    ...(pool !== 'opendesign' ? [] : o.windows).map((w) => [`${fmtDur(w.durationSeconds)}窗口剩余`, num(w.remaining), `共 ${num(w.limit)} · ${w.workspaces} 个工作区 · 已用 ${w.limit ? (w.used / w.limit * 100).toFixed(1) : 0}%`]),
    ...(pool !== 'opendesign' ? [] : [['钱包合计', '$' + o.walletUsd.toFixed(2), `${o.walletAccounts} 个工作区`]]),
    ...(pool === 'ollama' && o.ollamaAccounts ? [['Ollama 额度合计', '$' + o.ollamaCredits.toFixed(2), `${o.ollamaAccounts} 个账号（含赠送额度 + 已购额度，同一团队的 key 会重复计）`]] : []),
    ...(pool === 'modal' && o.modalAccounts ? [['可用 Endpoint', num(o.modalEndpoints), `${o.modalAccounts} 个账号的 token 能访问的 Endpoint 数（同一 Endpoint 会被多个账号各计一次）`]] : []),
    ['24h 请求', num(st.r), `成功率 ${okRate === null ? '—' : pct(okRate)} · 近 1h ${num(o.stats.lastHour.r)}`],
    ['24h ' + (pool === 'opencode-go' ? '价格表估算' : '费用'), costSum(st), pool === 'opencode-go' ? '不是 Go 订阅账单或额外扣费金额' : isTokenHarbor() ? '按手动价格估算；未配价/含缓存写入的不计，不是上游账单' : '按模型价格估算，未配价格的不计'],
    ['24h ' + (pool === 'opencode-go' ? '缓存 Token 读取占比' : '缓存命中率'), hitRate(st) === null ? '—' : pct(hitRate(st)), `已测缓存 ${num(st.ck || 0)} / 提示 ${num(st.cp || 0)} token · 样本 ${num(st.ckr || 0)} 次` + (pool === 'opencode-go' ? `；24h 写入 ${st.cwkr ? num(st.cw || 0) : '未知'}` : isTokenHarbor() ? `；24h 写入 ${num(st.kw || 0)}` : '')],
    ...(pool === 'opencode-go' ? goOverviewCards(o) : []),
    ...(isDeploy() ? [['Deploy 余额', '未知', o.deploy?.quotaReason || '仅绑定推理 Key，未绑定组织控制台会话；不按 Key 累加余额'], ['Deploy 模型目录', `${num(o.deploy?.catalogFresh || 0)} / ${num(o.accounts)}`, '新鲜目录账号 / 总数；目录不是 Key 激活或推理授权证明']] : []),
    [pool === 'opencode-go' ? '24h 已报告 Token' : '24h Token', pool === 'opencode-go' ? (!st.ikr && !st.okr ? '—' : num(st.p + st.o)) : num(st.i + st.o), pool === 'opencode-go' ? `提示（含缓存）${st.ikr ? num(st.p) : '未知'} · 输出 ${st.okr ? num(st.o) : '未知'} · 累计缓存读取 ${num(st.k || 0)}（缺失的上游用量不计）` : `输入 ${num(st.i)} · 输出 ${num(st.o)} · 缓存 ${num(st.k)}`],
    ['会话绑定', num(o.bindings), `${o.bindingLimit ? (o.bindingIndependent ? 'Mistral独立 ' + num(o.bindings) : '共享 ' + num(o.sharedBindings ?? o.globalBindings)) + ' / ' + num(o.bindingLimit) + ' · LRU淘汰 · ' : ''}策略 ${o.strategy}`],
    ...(isMistral() && o.mistralBufferLimitBytes ? [['本地字节预算', (o.mistralBufferedBytes / 1048576).toFixed(1) + ' / ' + (o.mistralBufferLimitBytes / 1048576).toFixed(0) + ' MiB', `日志排队 ${(Number(o.logQueuedBytes || 0) / 1048576).toFixed(1)} MiB · ${num(o.logQueueLength || 0)} 条 · 累计丢弃 ${num(o.logsDropped || 0)}；不等于进程RSS`]] : []),
  ];
  $('#ov-cards').innerHTML = cards.map(([l, v, s]) => `<div class="card"><div class="label">${esc(l)}</div><div class="value">${esc(v)}</div><div class="sub">${esc(s)}</div></div>`).join('');
  const models = Object.entries(o.stats.models || {}).sort((a, b) => b[1].r - a[1].r);
  $('#ov-models tbody').innerHTML = models.map(([name, b]) => {
    const done = b.r - b.c;
    const hr = hitRate(b);
    return `<tr><td>${esc(name)}</td><td class="num">${num(b.r)}</td><td class="num">${done > 0 ? pct(b.s / done) : '—'}</td><td class="num">${pool === 'opencode-go' && !b.ikr ? '—' : num(b.i)}</td><td class="num">${pool === 'opencode-go' && !b.okr ? '—' : num(b.o)}</td><td class="num">${hr === null ? '—' : pct(hr)}</td><td class="num">${esc(costSum(b))}</td></tr>`;
  }).join('') || '<tr><td colspan="7" class="empty">暂无请求</td></tr>';
  const proxies = Object.entries(o.stats.proxies || {}).sort((a, b) => b[1].r - a[1].r);
  $('#ov-proxies tbody').innerHTML = proxies.map(([name, b]) => {
    const done = b.r - b.c;
    return `<tr><td>${name === 'direct' ? '直连' : esc(name)}</td><td class="num">${num(b.r)}</td><td class="num">${done > 0 ? pct(b.s / done) : '—'}</td><td class="num">${num(b.s)}</td><td class="num">${num(b.f)}</td><td class="num">${num(b.c)}</td></tr>`;
  }).join('') || '<tr><td colspan="6" class="empty">暂无请求</td></tr>';
  const hourly = o.stats.hourly;
  const peak = Math.max(1, ...hourly.map((b) => b.r));
  $('#ov-chart').innerHTML = hourly.map((b) => {
    const label = new Date(b.m * 60000).toLocaleString('zh-CN', { ...TZ, hour: '2-digit', minute: '2-digit' });
    return `<div class="col" title="${esc(label)} 请求 ${b.r}，失败 ${b.f}，取消 ${b.c}"><div class="seg ok" data-h="${b.s / peak * 100}"></div><div class="seg bad" data-h="${b.f / peak * 100}"></div><div class="seg cancel" data-h="${b.c / peak * 100}"></div></div>`;
  }).join('');
  applyWidths($('#ov-chart'));
  $('#ov-windows').innerHTML = isDeploy() ? `<p class="muted">${esc(o.deploy?.quotaReason || '余额未知，未接入控制台会话。')}</p><p class="muted small">仅支持原生 Chat Completions（流式/非流式）；不转换 Responses / Messages。官方 405 可能表示组织暂停，429 默认停止轮换并遵守 Retry-After。请求费用是本池独立价格表估算，不是上游账单。</p>` : pool === 'opencode-go' ? goQuotaWindows(o.go) : o.windows.length ? o.windows.map((w) => {
    const used = w.limit ? w.used / w.limit * 100 : 0;
    return `<div class="win"><div class="row"><b>${esc(fmtDur(w.durationSeconds))}</b><span class="muted">剩余 ${num(w.remaining)} / ${num(w.limit)} credits</span></div><div class="bar ${used >= 98 ? 'bad' : used >= 90 ? 'warn' : ''}"><i data-w="${used}"></i></div></div>`;
  }).join('') : `<p class="muted">${isMistral() ? '已接入 Studio / Vibe 套餐额度与组织订阅，请在账号页刷新查看；额度按用量百分比估算，不是钱包余额。读取失败或过期时标为未知并保留历史值。多个 Key 可能共享组织预算，不可直接相加；缓存命中由上游决定。' : pool === 'tokenharbor' ? 'Token Harbor 余额未知；只同步模型目录。默认按会话/缓存标识固定 Key，缓存是否命中仍由上游决定。429 遵守 Retry-After；免费账号/IP 限额可能共享，不能把每把 Key 当成独立账号额度。' : pool === 'river' ? 'River 没有额度接口，额度用完靠上游 402 / 429 错误推断冷却。' : pool === 'ollama' ? '暂无余额数据：Ollama 的 key 直接读 /api/balance，30 秒内不会重复读（该接口每个用户每分钟限 10 次）。' : pool === 'modal' ? 'Modal 没有额度接口：Shared Endpoint 按 token 从工作区余额计费，只能在 Modal 控制台看；本服务只在遇到 402 时冷却账号。' : '暂无额度数据（需要账号配置 vela_ctrl_ 控制密钥）。'}</p>`;
  applyWidths($('#ov-windows'));
  $('#ov-status').innerHTML = Object.entries(o.status).map(([k, v]) => `<span class="badge ${statusClass(k)}">${esc(statusLabel(k))} ${v}</span>`).join('') || '<span class="muted">无账号</span>';
  $('#ov-updated').textContent = '更新于 ' + fmtTime(o.stats.generated);
}

// ---- accounts ----
const STATUS = { 'wallet-overage-enabled': ['官网钱包续费已开启，暂停调度', 'warn'], 'plan-unknown': ['套餐未知/过期', 'warn'], 'quota-unknown': ['周用量未知/过期', 'warn'], 'catalog-pending': ['等待模型同步', 'warn'], 'policy-paused': ['政策暂停', 'warn'], ok: ['可用', 'ok'], disabled: ['已禁用', ''], cooldown: ['冷却中', 'warn'], quarantined: ['已隔离', 'bad'], 'quota-exhausted': ['额度耗尽', 'bad'], 'model-cooldown': ['模型冷却', 'warn'], 'no-key': ['无密钥', 'bad'], 'not-entitled': ['套餐不含该模型', 'warn'], 'funding-cooldown': ['额度耗尽（推断）', 'warn'] };
const statusLabel = (s) => (STATUS[s] || [s])[0];
const statusClass = (s) => (STATUS[s] || ['', ''])[1];

// ollamaQuotaHTML shows an Ollama account's credits: the included allowance
// of the current period (or a legacy plan's session/weekly percentages) and
// the purchased balance next to it.
function ollamaQuotaHTML(a) {
  const q = a.quota;
  const u = a.usage24h || {};
  const usage = `<div class="acc-sub">24h：${num(u.r || 0)} 次 · ${num((u.i || 0) + (u.o || 0))} tokens</div>`;
  if (!q || !q.ollama) return '<span class="muted small">等待首次读取…</span>' + usage;
  const b = q.ollama;
  const parts = [];
  if (q.authFailed) parts.push('<span class="badge bad">API Key 失效，需换 key</span>');
  else if (q.lastError) parts.push(`<div class="acc-sub" title="${esc(q.lastError)}">读取出错：${esc(q.lastError)}</div>`);
  const bar = (used) => `<div class="bar ${used >= 98 ? 'bad' : used >= 90 ? 'warn' : ''}"><i data-w="${used}"></i></div>`;
  if (b.includedUsd !== undefined && b.includedUsd !== '') {
    const left = parseFloat(b.includedUsd), all = parseFloat(b.allowanceUsd);
    const used = Number.isFinite(left) && Number.isFinite(all) && all > 0 ? (all - left) / all * 100 : 0;
    parts.push(`<div class="win"><div class="acc-sub">赠送额度 · 剩余 <b>${usd(b.includedUsd)}</b> / ${usd(b.allowanceUsd)}</div>${bar(used)}`
      + (b.periodUntil ? `<div class="acc-sub">周期至 ${esc(fmtTime(b.periodUntil, true))}（${esc(fmtIn(b.periodUntil))}）</div>` : '') + `</div>`);
  }
  for (const [label, pctLeft, reset] of [['会话额度', b.sessionPercent, b.sessionResetsAt], ['周额度', b.weeklyPercent, b.weeklyResetsAt]]) {
    if (pctLeft === undefined || pctLeft < 0) continue;
    parts.push(`<div class="win"><div class="acc-sub">${label} · 剩余 <b>${num(pctLeft)}%</b></div>${bar(100 - pctLeft)}`
      + (reset ? `<div class="acc-sub">重置 ${esc(fmtTime(reset, true))}（${esc(fmtIn(reset))}）</div>` : '') + `</div>`);
  }
  parts.push(`<div class="acc-sub">已购额度 ${usd(b.purchasedUsd)}</div>`);
  if (!a.quotaFresh) parts.push('<span class="badge warn">数据过期</span>');
  parts.push(usage);
  return parts.join('');
}

// modalEndpointsHTML lists the Endpoints a Modal proxy token can reach. On
// Modal the request's model field IS the Endpoint's hostname, so this list is
// the account's model catalogue — the only quota-like signal there is.
function modalEndpointsHTML(a) {
  const q = a.quota;
  const u = a.usage24h || {};
  const usage = `<div class="acc-sub">24h：${num(u.r || 0)} 次 · ${num((u.i || 0) + (u.o || 0))} tokens</div>`;
  if (!q || !q.catalog) return '<span class="muted small">等待首次读取…</span>' + usage;
  const on = Object.entries(q.catalog).filter(([, en]) => en).map(([m]) => m).sort();
  const parts = [];
  if (q.authFailed || q.catalogError) {
    parts.push(`<span class="badge bad">Token 读取失败</span><div class="acc-sub" title="${esc(q.catalogError || q.lastError || '')}">${esc(q.catalogError || q.lastError || '')}</div>`);
  }
  parts.push(`<div class="acc-sub" title="${esc('该 token 的 /v1/models（即能访问的 Endpoint）：\n' + (on.join('\n') || '（空）'))}">可用 Endpoint <b>${on.length}</b> 个</div>`);
  // the first few hostnames, so the list is readable at a glance
  for (const m of on.slice(0, 4)) parts.push(`<div class="acc-sub"><code>${esc(m)}</code></div>`);
  if (on.length > 4) parts.push(`<div class="acc-sub muted">…另有 ${on.length - 4} 个</div>`);
  if (a.quotaFresh === false) parts.push('<span class="badge warn">数据过期</span>');
  parts.push(usage);
  return parts.join('');
}

function openCodeQuotaHTML(a) {
  const q = a.quota?.opencode;
  const parts = [];
  if (a.provider === 'opencode-zen') parts.push('<span class="badge">Zen 余额未知</span>');
  else if (!q?.usageAt) parts.push('<span class="badge">Go 额度未知</span>');
  else {
    if (!a.quotaFresh) parts.push('<span class="badge warn">额度已过期 / 查询失败</span>');
    for (const name of ['rolling', 'weekly', 'monthly']) {
      const w = q.usage?.[name]; if (!w) continue;
      const expired = new Date(w.resetsAt).getTime() <= Date.now();
      parts.push(`<div class="acc-sub">${esc(name)} ${expired ? '窗口已到期，等待新数据（旧值）' : ''}已用 ${num(w.usedPercent)}% · 剩余 ${num(w.remainingPercent)}%<br>重置 ${esc(fmtTime(w.resetsAt, true))}</div>`);
    }
    parts.push(`<div class="acc-sub">真实数据 · 更新 ${esc(fmtTime(q.usageAt, true))} · 订阅共享，不可相加</div>`);
  }
  for (const err of [q?.usageError, q?.capabilityError]) if (err) parts.push(`<div class="acc-sub">${esc(err)}</div>`);
  return parts.join('');
}

function tokenHarborQuotaHTML(a) {
  const t = a.quota?.tokenHarbor;
  const u = t?.usage || {};
  const observed = !!t && a.quotaFresh;
  const amount = (v) => typeof v === 'number' && Number.isFinite(v) && v > 0;
  const pctKnown = typeof u.usedPercent === 'number' && Number.isFinite(u.usedPercent) && u.usedPercent >= 0 && u.usedPercent <= 100;
  const windowExpired = u.activated && (!u.resetsAt || !Number.isFinite(Date.parse(u.resetsAt)) || Date.parse(u.resetsAt) <= Date.now());
  const fresh = observed && pctKnown && !u.error && !windowExpired;
  const left = pctKnown ? 100 - u.usedPercent : null;
  const agent = t?.plan?.status === 'active' && t.plan.tier === 'plus';
  const weeklyLimit = agent ? 5 : u.weeklyLimitUsd;
  const monthlyLimit = agent ? 20 : u.monthlyLimitUsd;
  let weekly = '未知';
  if (fresh) {
    weekly = amount(weeklyLimit) ? `约 $${(Math.round(weeklyLimit * left) / 100).toFixed(2)} / $${weeklyLimit.toFixed(2)}（剩余 ${num(left)}%）` : `剩余 ${num(left)}%（金额未知）`;
  } else if (pctKnown) {
    weekly += `（上次剩余 ${num(left)}%，不可视为当前余额）`;
  }
  const reset = u.resetsAt ? ' · 重置 ' + esc(fmtTime(u.resetsAt, true)) : !pctKnown ? ' · 周窗口未知' : u.activated ? ' · 重置未知' : ' · 周窗口尚未激活';
  const monthly = '未知（官网未提供月剩余）' + (amount(monthlyLimit) ? ` · ${observed ? '' : '上次'}月${agent ? '等效总额度' : '总额度'} $${monthlyLimit.toFixed(2)}` : '');
  return `<span class="badge ${observed ? 'ok' : 'warn'}">${esc(t?.plan?.name || '套餐未知')}${observed ? '（已检测）' : '（未知/过期）'}</span><div class="acc-sub">周限余额：${esc(weekly)}${reset}</div><div class="acc-sub">月限余额：${esc(monthly)}</div><div class="acc-sub">${esc(t?.error || u.error || (u.overageBilling ? '请在官网关闭套餐用尽后从钱包继续付费，再刷新；本服务不会代改账单设置' : '仅套餐覆盖可用，不借钱包；免费功能不自动开启'))}</div><div class="acc-sub">检测 ${esc(fmtTime(t?.checkedAt, true))} · ${agent ? 'Agent Pass 按 $5/周、$20/月等效基准，非官网原始美元额度' : '官网额度'}，非钱包余额；周金额按百分比估算</div>`;
}

function openDesignStatusHTML(a) {
  const d = a.diagnosis;
  const bad = d && ['banned', 'auth-failed', 'control-auth-failed'].includes(d.status);
  return `<div class="acc-sub"><span class="badge ${bad ? 'bad' : 'warn'}">${esc(d?.label || '尚未检测，当前状态未知')}</span></div>`
    + (d?.detail ? `<div class="acc-sub">${esc(d.detail)}</div>` : '')
    + (d?.checkedAt ? `<div class="acc-sub">检测 ${esc(fmtTime(d.checkedAt))}</div>` : '')
    + '<div class="acc-sub muted">目录/工作区读取成功不代表推理授权，未报封禁不等于确认安全</div>';
}

function openDesignQuotaHTML(a) {
  const q = a.quota, d = a.diagnosis;
  const labels = { available: '套餐窗口有剩余', exhausted: '套餐窗口额度耗尽', 'no-plan': '当前无生效套餐', unknown: '套餐额度当前未知' };
  const parts = [`<span class="badge ${d?.planStatus === 'exhausted' ? 'bad' : d?.planFresh ? '' : 'warn'}">${esc(labels[d?.planStatus] || labels.unknown)}</span>`];
  if (!a.hasControlKey) parts.push('<div class="acc-sub">无控制密钥，无法读取额度；运行密钥仍可独立检测</div>');
  if (q?.planAt) {
    for (const w of q.windows || []) {
      const passed = w.resetsAt && new Date(w.resetsAt).getTime() <= Date.now();
      const used = w.limit > 0 ? Math.max(0, Math.min(100, (w.limit - w.remaining) / w.limit * 100)) : 0;
      parts.push(`<div class="win"><div class="acc-sub">${d?.planFresh ? '' : '上次'}${esc(fmtDur(w.durationSeconds))} · 剩余 <b>${num(w.remaining)}</b> / ${num(w.limit)}</div><div class="bar ${used >= 98 ? 'bad' : used >= 90 ? 'warn' : ''}"><i data-w="${used}"></i></div><div class="acc-sub">${passed ? '窗口已到期，等待新数据；不推算满额' : '重置 ' + esc(fmtTime(w.resetsAt, true))}</div></div>`);
    }
    parts.push(`<div class="acc-sub">套餐上次成功 ${esc(fmtTime(q.planAt))}</div>`);
    if (!d?.planFresh) parts.push('<div class="acc-sub">以上为历史数据，不代表当前额度</div>');
  }
  if (q?.nextAt && d?.autoRefresh) parts.push(`<div class="acc-sub">下次自动尝试 ${esc(fmtTime(q.nextAt))}（${esc(fmtIn(q.nextAt))}）</div>`);
  else if (d && !d.autoRefresh) parts.push('<div class="acc-sub">后台额度轮询已关闭，可手动检测</div>');
  return parts.join('');
}

function openDesignFundingHTML(a) {
  const q = a.quota, d = a.diagnosis;
  const hasWallet = q?.walletUsd !== undefined && q.walletUsd !== '';
  let html = d?.walletFresh && hasWallet ? '钱包 ' + esc(usd(q.walletUsd)) : '钱包当前未知' + (hasWallet ? `（上次 ${esc(usd(q.walletUsd))}）` : '');
  if (d?.walletStatus === 'empty') html += '<div class="acc-sub">钱包为零不等于套餐额度耗尽</div>';
  if (q?.walletSource === 'preflight' && hasWallet) html += '<div class="acc-sub">钱包数据来源 preflight</div>';
  if (q?.walletAt) html += `<div class="acc-sub">钱包上次成功 ${esc(fmtTime(q.walletAt))}</div>`;
  const credits = (v) => { const n = parseFloat(v); return Number.isFinite(n) ? num(n) : '未知'; };
  if (q?.summary) {
    const prefix = d?.billingFresh ? '' : '上次';
    html += `<div class="acc-sub">${prefix}套餐模型余额 ${esc(credits(q.summary.subscriptionCredits))}</div><div class="acc-sub">${prefix}充值余额 ${esc(credits(q.summary.rechargeCredits))}</div>`;
    html += `<div class="acc-sub">账单上次成功 ${esc(fmtTime(q.summaryAt))}${d?.billingFresh ? '' : ' · 当前未知'}</div>`;
  } else html += '<div class="acc-sub">套餐模型/充值余额未知</div>';
  return html;
}

function mistralQuotaHTML(a) {
  const m = a.quota?.mistral, b = m?.budget;
  const type = m?.keyType === 'API' ? 'Studio API' : m?.keyType === 'CHAT' ? 'Vibe' : 'Key 类型未知';
  const fresh = !!a.quotaFresh && !m?.error && !!b && Date.parse(b.resetsAt) > Date.now();
  let text = `<span class="badge ${fresh ? 'ok' : 'warn'}">${esc(type)}${m?.plan ? ' · Key访问等级 ' + esc(m.plan) : ''}</span>`;
  const sub = m?.subscription;
  if (sub?.plan) text += `<div class="acc-sub">${fresh && !sub.error ? '' : '上次'}组织订阅：${esc(sub.plan)}</div>`;
  else text += '<div class="acc-sub">组织订阅未知，刷新额度后读取</div>';
  if (sub?.error) text += `<div class="acc-sub warn">${esc(sub.error)}（不影响已获取的套餐预算）</div>`;
  if (sub?.payg != null || sub?.vibePayg != null) {
    const flag = (v) => v === true ? '开启' : v === false ? '关闭' : '未知';
    text += `<div class="acc-sub">${fresh && !sub.error ? '' : '上次'}组织 PAYG：API ${flag(sub.payg)} / Vibe ${flag(sub.vibePayg)}（只读）</div>`;
  }
  if (b) {
    const money = (v) => esc(b.currency) + ' ' + Number(v).toFixed(2);
    text += `<div class="acc-sub">${fresh ? '' : '上次'}约剩余 <b>${money(b.remaining)}</b> / ${money(b.total)}</div><div class="acc-sub">已用 ${esc(num(b.usedPercent))}% · 重置 ${esc(fmtTime(b.resetsAt, true))}</div>`;
    if (!fresh) text += '<div class="acc-sub">当前额度未知/过期，以上历史值不可视为当前余额</div>';
    text += `<div class="acc-sub">PAYG ${b.payg === true ? '官网已开启' : b.payg === false ? '官网未开启' : '状态未知'}（本服务不代改）</div>`;
  } else text += '<div class="acc-sub">套餐额度未知，点击刷新额度读取</div>';
  if (m?.error) text += `<div class="acc-sub">${esc(m.error)}</div>`;
  if (m?.checkedAt) text += `<div class="acc-sub">检测 ${esc(fmtTime(m.checkedAt))}${m.at ? ' · 上次成功 ' + esc(fmtTime(m.at)) : ''}</div>`;
  return text + '<div class="acc-sub">按百分比估算，非钱包余额；同组织同类预算共享，不按 Key 重复累加</div>';
}

function modelStudioQuotaHTML(a) {
  // The upstream has no balance endpoint for an inference key: Credits live
  // only on the console page behind a login session. The backend reports that
  // state explicitly, so this shows "unknown / not connected" and never 0.
  const plan = a.modelStudioPlan === 'tokenplan' ? 'Token Plan（月度 Credits）' : '按量付费（按 token 计费）';
  const state = a.modelStudioQuotaState || 'not-connected';
  const connected = state !== 'not-connected';
  const cat = a.quota?.catalog ? Object.keys(a.quota.catalog).length : null;
  let text = `<span class="badge ${connected ? 'ok' : 'warn'}">${esc(plan)}</span>`;
  text += `<div class="acc-sub">余额：<b>${connected ? '已接入' : '未知（未接入控制台）'}</b></div>`;
  if (!connected) text += `<div class="acc-sub">${esc(a.modelStudioQuotaNote || '百炼 Credits 只能在控制台「我的订阅」查看，API Key 无法反查所属账号。')}</div>`;
  if (cat !== null) text += `<div class="acc-sub">模型目录 ${num(cat)} 个${a.quotaFresh ? '' : '（上次读取）'}</div>`;
  if (a.quota?.catalogError) text += `<div class="acc-sub warn">目录读取失败：${esc(a.quota.catalogError)}</div>`;
  return text;
}

function deployQuotaHTML(a) {
  const d = a.deploy || {};
  const labels = { ok: '目录读取成功', failed: '目录读取失败', stale: '目录已过期', unknown: '目录未读取' };
  return `<span class="badge warn">余额未知（未接入控制台）</span><div class="acc-sub">${esc(d.quotaReason || '仅绑定推理 secret；不读取或猜测组织余额。')}</div><div class="acc-sub">${esc(labels[d.catalogState] || labels.unknown)} · ${num(d.models)} 个模型${d.catalogAt ? ' · ' + esc(fmtTime(d.catalogAt, true)) : ''}</div>${d.catalogError ? `<div class="acc-sub warn">${esc(d.catalogError)}</div>` : ''}<div class="acc-sub">模型发现不是 Key 激活或推理授权证明，请单独执行真实请求。</div>`;
}

function windowsHTML(a) {
  if (a.provider === 'deploy') return deployQuotaHTML(a);
  if (a.provider === 'opendesign') return openDesignQuotaHTML(a);
  if (a.provider === 'modelstudio') return modelStudioQuotaHTML(a);
  if (a.provider === 'mistral') return mistralQuotaHTML(a);
  if (a.provider === 'tokenharbor') return tokenHarborQuotaHTML(a);
  if (a.provider === 'opencode-zen' || a.provider === 'opencode-go') return openCodeQuotaHTML(a);
  if (a.provider === 'ollama') return ollamaQuotaHTML(a);
  if (a.provider === 'modal') return modalEndpointsHTML(a);
  const q = a.quota;
  if (!a.hasControlKey) {
    const u = a.usage24h || {};
    return `<span class="badge">仅 API Key</span><div class="acc-sub" title="没有控制密钥读不到剩余额度；额度用完时按上游的余额不足 / 窗口用尽错误冷却，冷却逐次翻倍，成功一次清零。可用「重新设备授权」补上控制密钥">额度未知，按错误推断</div>` +
      `<div class="acc-sub">24h：${num(u.r || 0)} 次 · ${num((u.i || 0) + (u.o || 0))} tokens</div>`;
  }
  if (!q || (!q.planAt && !q.lastError)) return '<span class="muted small">等待首次读取…</span>';
  const parts = [];
  if (q.authFailed) parts.push('<span class="badge bad">控制密钥失效，需重新授权</span>');
  else if (q.lastError) parts.push(`<div class="acc-sub" title="${esc(q.lastError)}">读取出错：${esc(q.lastError)}</div>`);
  if (q.planAt) {
    if (!q.eligible || !q.windows.length) parts.push('<span class="badge">无生效套餐，按钱包计</span>');
    for (const w of q.windows) {
      const passed = w.resetsAt && new Date(w.resetsAt) <= new Date();
      const rem = passed ? w.limit : w.remaining;
      const used = w.limit ? (w.limit - rem) / w.limit * 100 : 100;
      parts.push(`<div class="win"><div class="acc-sub">${esc(fmtDur(w.durationSeconds))} · 剩余 <b>${num(rem)}</b> / ${num(w.limit)}</div><div class="bar ${used >= 98 ? 'bad' : used >= 90 ? 'warn' : ''}"><i data-w="${used}"></i></div><div class="acc-sub">重置 ${esc(fmtTime(w.resetsAt, true))}（${esc(fmtIn(w.resetsAt))}）</div></div>`);
    }
    if (!a.quotaFresh) parts.push('<span class="badge warn">数据过期</span>');
  }
  return parts.join('');
}

const effLimit = (own, learned) => { const v = [own, learned].filter((x) => x > 0); return v.length ? Math.min(...v) : '∞'; };

function accountCredentialHTML(a) {
  return [['runtimeKey', 'Key'], ['controlKey', '控制 Key'], ['proxyUrl', '代理']]
    .filter(([field]) => a[field])
    .map(([field, label]) => `<div class="acc-sub account-credential">${label}：<code>${esc(a[field])}</code> <button type="button" class="btn small" data-account-copy="${field}">复制</button></div>`).join('');
}
function copyAccountField(id, field) {
  if (!['runtimeKey', 'controlKey', 'proxyUrl'].includes(field)) return;
  const value = accountsCache.find((a) => a.id === id)?.[field];
  if (value) copyText(value); else toast('没有可复制的值');
}
function accountRow(a) {
  const r = a.runtime;
  const st = r.state || {};
  const models = Object.entries(st.models || {});
  let status = `<span class="badge ${statusClass(r.status)}">${a.provider === 'opendesign' ? '调度：' : ''}${esc(statusLabel(r.status))}</span>`;
  if (a.provider === 'opendesign') status += openDesignStatusHTML(a);
  if (a.tokenIncomplete) status = '<span class="badge bad">Token 不完整</span><div class="acc-sub" title="只填了 wk-<id> 那段。Modal 要的是 wk-<id>.ws-<secret>：到 modal.com/settings/proxy-auth-tokens 复制完整的 Proxy Token；旧 token 的 secret 只在创建时显示一次，看不到就新建一个，然后在这里改运行密钥">只有 wk-&lt;id&gt;，缺少 .ws-&lt;secret&gt;</div>';
  if (a.proxyHealth) status += `<div class="acc-sub">出口 ${esc(a.proxyHealth.status)}${a.proxyHealth.reason ? ' · ' + esc(a.proxyHealth.reason) : ''}</div>`;
  if (a.egress) status += `<div class="acc-sub" title="${esc('上次校验 ' + fmtTime(a.egress.checkedAt) + '；校验只能发现漂移，固定 IP 需由静态网络保障')}">${esc(GO_EGRESS_LABEL[a.egress.status] || a.egress.status)}${a.egress.observedIp ? ' · ' + esc(a.egress.observedIp) : ''}</div>`;
  if (st.cooldownUntil) status += `<div class="acc-sub">至 ${esc(fmtTime(st.cooldownUntil, true))}</div>`;
  if (st.ruleId) status += `<div class="acc-sub">规则 ${esc(st.ruleId)}${st.statusCode ? ' · ' + st.statusCode : ''}</div>`;
  if (st.learnedConcurrency || st.learnedRpm) status += `<div class="acc-sub" title="网关 tier_limit_exceeded 告知的套餐上限；点「恢复」清除">学到上限：${st.learnedConcurrency ? '并发 ' + st.learnedConcurrency : ''}${st.learnedRpm ? ' RPM ' + st.learnedRpm : ''}</div>`;
  const classes = Object.entries(st.classes || {});
  if (classes.length) status += classes.map(([c, x]) => `<div class="acc-sub" title="${esc(x.ruleId)}">${c === 'package' ? '套餐模型' : esc(c)}额度耗尽，至 ${esc(fmtTime(x.cooldownUntil, true))}</div>`).join('');
  const strikes = Object.entries(st.strikes || {}).filter(([, n]) => n > 1);
  if (strikes.length) status += `<div class="acc-sub" title="连续余额不足次数，冷却按次数翻倍，成功一次清零">连续失败：${strikes.map(([k, n]) => esc(k) + '×' + n).join(' ')}</div>`;
  if (models.length) status += `<div class="acc-sub" title="${esc(models.map(([m, s]) => m + ' 至 ' + fmtTime(s.cooldownUntil, true) + ' (' + s.ruleId + ')').join('\n'))}">${models.length} 个模型冷却中</div>`;
  const q = a.quota;
  const credits = (v) => { const f = parseFloat(v); return Number.isFinite(f) ? num(f) : '—'; };
  // Ollama keeps its own three amounts: included, purchased, and what this
  // pool sent in the last 24h; Modal has neither wallet nor package credits
  const ollama = a.provider === 'ollama';
  const modal = a.provider === 'modal';
  const openCode = a.provider === 'opencode-zen' || a.provider === 'opencode-go';
  const ou = a.usage24h || {};
  const usage24 = `<div class="acc-sub">24h ${num(ou.r || 0)} 次 · ${num((ou.i || 0) + (ou.o || 0))} tokens</div>`;
  const wallet = (modal || openCode || a.provider === 'tokenharbor' || a.provider === 'mistral' || a.provider === 'deploy')
    ? (ou.r ? num(ou.r) + usage24 : '—')
    : ollama
      ? (q && q.ollama && q.ollama.purchasedUsd !== undefined && q.ollama.purchasedUsd !== ''
        ? usd(q.ollama.purchasedUsd) + usage24 : '—')
      : (q && q.walletUsd !== undefined && q.walletUsd !== '' ? usd(q.walletUsd) + (q.walletSource === 'preflight' ? '<div class="acc-sub">来源 preflight</div>' : '') : '—');
  const pkg = !ollama && !modal && !openCode && a.provider !== 'mistral' && q && q.summary ? `<div class="acc-sub" title="套餐模型额度：Design Plan 窗口用尽后、以及调用套餐模型（Claude Sonnet、GPT-6 Sol、Gemini 等）时扣这里，按计费周期重置不结转">套餐模型 ${credits(q.summary.subscriptionCredits)}</div><div class="acc-sub">充值 ${credits(q.summary.rechargeCredits)}</div>` : '';
  const cov = !ollama && !modal && !openCode && q && q.coverage ? Object.entries(q.coverage) : [];
  const cat = q && q.catalog ? Object.entries(q.catalog) : [];
  const catOn = cat.filter(([, en]) => en).map(([m]) => m).sort();
  const catTitle = a.provider === 'deploy' ? 'Deploy 原生模型目录（不是激活/授权/余额证明）：\n' : a.provider === 'opendesign' ? '上次成功读取的模型目录（不证明推理授权）：\n' : a.provider === 'tokenharbor' ? '本账号已确认套餐覆盖且原生目录支持的模型（不代表周剩余额度）：\n' : openCode ? '本账号上游目录（不证明推理授权）；未知/不支持协议不计可路由数：\n' : ollama ? '该 key 的 /v1/models（Ollama 云端目录对所有 key 相同）：\n' : modal ? '该 token 的 /v1/models（即它能访问的 Endpoint，模型名就是 Endpoint 的 hostname）：\n' : '该 API Key 的模型列表（/v1/models），决定套餐能调用哪些模型：\n';
  const catHTML = modal ? '' : cat.length ? `<div class="acc-sub" title="${esc(catTitle + catOn.join('\n'))}">${openCode ? '可路由 ' + num(a.routableModels) + ' / 目录 ' : a.provider === 'tokenharbor' ? '上次确认覆盖 ' : a.provider === 'opendesign' ? (a.diagnosis?.catalogFresh ? '模型目录 ' : '上次模型目录（当前未知）') : '可用模型 '}${catOn.length}${a.provider === 'tokenharbor' ? ' / 目录 ' + Object.keys(q?.tokenHarbor?.directory || {}).length : cat.length > catOn.length ? ' / ' + cat.length : ''} 个</div>` : (q && q.catalogError ? `<div class="acc-sub">模型列表读取失败：${esc(q.catalogError)}</div>` : '');
  status += catHTML;
  const covHTML = cov.length ? `<div class="acc-sub" title="${esc(cov.map(([m, c]) => m + '：' + (c.covered === true ? 'Design Plan 模型' : c.covered === false ? '套餐模型' : '未知')).join('\n'))}">已识别 ${cov.length} 个模型的类别</div>` : '';
  const sum = !ollama && !modal && !openCode && q && q.summary ? `<div class="acc-sub" title="累计消费 ${esc(q.summary.totalConsumedUsd)} · 累计充值 ${esc(q.summary.totalRechargedUsd)} · 调用 ${esc(q.summary.usageCount)} 次">今日 ${esc(usd(q.summary.todayConsumedUsd))}</div>` : '';
  const tier = q && (q.tier || q.membershipTier) ? `<span class="badge accent">${esc(q.tier || q.membershipTier)}</span>` : '';
  const shared = a.sharedWorkspaceWith && a.sharedWorkspaceWith.length ? `<span class="badge warn" title="与其他账号同一工作区，额度共享">共享工作区</span>` : '';
  return `<tr data-id="${esc(a.id)}">
    <td><div class="acc-name">${managedKeyPool() ? `<input type="checkbox" data-oc-select="${esc(a.id)}" aria-label="选择 ${esc(a.name)}" ${ocSelected.has(a.id) ? 'checked' : ''}> ` : ''}${esc(a.name)}</div><div class="acc-sub">${esc(a.email || a.id)}</div>${accountCredentialHTML(a)}
      <div class="chips"><span class="badge">${esc(a.group)}</span>${tier}${a.hasProxy ? '<span class="badge" title="' + esc(a.proxyUrl) + '">代理</span>' : ''}${a.priority !== 50 ? '<span class="badge">优先级 ' + a.priority + '</span>' : ''}${shared}</div></td>
    <td>${status}</td>
    <td>${windowsHTML(a)}</td>
    <td class="num">${a.provider === 'opendesign' ? openDesignFundingHTML(a) : wallet + pkg + sum + covHTML}</td>
    <td class="num">${accountCacheRateHTML(ou)}</td>
    <td class="num">${r.active} / ${effLimit(a.maxConcurrent, st.learnedConcurrency)}</td>
    <td class="num">${r.rpm} / ${effLimit(a.maxRpm, st.learnedRpm)}</td>
    <td class="num">${r.health === null || r.health === undefined ? '<span class="muted">—</span>' : pct(r.health)}<div class="acc-sub">${r.samples} 样本</div></td>
    <td class="num">${num(r.selections)} / ${num(r.bindings)}${pool === 'opencode-go' ? `<div class="acc-sub" title="当前分钟及前59分钟的网关已发送推理次数，包含会话命中；不含额度/诊断。分钟级窗口，新版本升级前的历史不回填。">近1h 已发 ${num(r.recentRequests1h ?? 0)} 次 · 待发 ${num(r.pendingRequests ?? 0)}</div>` : ''}</td>
    <td>${a.provider === 'tokenharbor' ? `<button class="btn small" data-account-copy-key="${esc(a.id)}">复制 Key</button>` : ''}<div class="menu"><button class="btn small" data-menu>操作 ▾</button></div></td>
  </tr>`;
}

$('#acc-table').addEventListener('click', (ev) => {
  const direct = ev.target.closest('[data-account-copy]');
  if (direct) {
    copyAccountField(direct.closest('tr').dataset.id, direct.dataset.accountCopy);
    return;
  }
  const button = ev.target.closest('[data-account-copy-key]');
  if (!button) return;
  const account = accountsCache.find((a) => a.id === button.dataset.accountCopyKey && a.provider === 'tokenharbor');
  if (account) accountAction(account, 'copy-key');
});
$('#acc-table').addEventListener('change', (ev) => {
  const id = ev.target.dataset.ocSelect; if (!id) return;
  if (ev.target.checked) ocSelected.add(id); else ocSelected.delete(id);
});
$('#oc-batch').addEventListener('click', async (ev) => {
  const button = ev.target.closest('[data-oc-batch]'); if (!button || !managedKeyPool()) return;
  const action = button.dataset.ocBatch;
  if (action === 'select' || action === 'clear') {
    $$('#acc-table [data-oc-select]').forEach((input) => { input.checked = action === 'select'; if (input.checked) ocSelected.add(input.dataset.ocSelect); else ocSelected.delete(input.dataset.ocSelect); }); return;
  }
  const group = $('#acc-group-filter').value;
  const selected = accountsCache.filter((a) => a.provider === pool && (!group || a.group === group) && ocSelected.has(a.id));
  if (!selected.length) { toast('请先选择账号'); return; }
  if (action === 'keys') {
    if (!isTokenHarbor() || button.disabled) return;
    button.disabled = true;
    try { await showStoredAPIKeys(selected.map((a) => ({ name: a.name || a.id, path: `/api/accounts/${encodeURIComponent(a.id)}/keys` }))); }
    finally { button.disabled = false; }
    return;
  }
  if (action === 'delete' && await modal({ title: '批量删除', body: `<p>删除选中的 ${selected.length} 个账号及其凭证？不能撤销；状态续传将失效。</p>`, buttons: [{ label: '取消', value: 'cancel' }, { label: '删除', value: 'delete', danger: true }] }) !== 'delete') return;
  $$('#oc-batch button').forEach((b) => { b.disabled = true; });
  const errors = []; let completed = 0;
  try {
    for (const a of selected) {
      try {
        const path = `/api/accounts/${encodeURIComponent(a.id)}`;
        if (action === 'enable' || action === 'disable') await api('PUT', path, { enabled: action === 'enable' });
        else if (action === 'delete') await api('DELETE', path);
        else { const result = await api('POST', path + '/' + ({ refresh: 'refresh-quota', proxy: 'test-proxy', recover: 'recover' }[action])); if (action === 'proxy' && !result.ok) throw new Error(result.error); if (action === 'refresh' && result.ok === false) throw new Error(result.message || '额度读取失败'); }
        completed++;
      } catch (e) { errors.push(a.name + ': ' + e.message); }
    }
    $('#oc-batch-result').textContent = `完成 ${completed}/${selected.length}` + (errors.length ? '；' + errors.join('；') : '');
    if (action === 'delete') ocSelected.clear();
    await loadAccounts();
  } finally { $$('#oc-batch button').forEach((b) => { b.disabled = false; }); }
});

async function loadAccounts() {
  const d = await api('GET', withPool('/api/accounts'));
  accountsCache = d.accounts;
  const groups = [...new Set(d.accounts.map((a) => a.group))].sort();
  const sel = $('#acc-group-filter');
  const cur = sel.value;
  sel.innerHTML = '<option value="">全部分组</option>' + groups.map((g) => `<option ${g === cur ? 'selected' : ''}>${esc(g)}</option>`).join('');
  const rows = d.accounts.filter((a) => !cur || a.group === cur);
  const visible = new Set(rows.map((a) => a.id));
  for (const id of ocSelected) if (!visible.has(id)) ocSelected.delete(id);
  $('#acc-table tbody').innerHTML = rows.map(accountRow).join('');
  $('#acc-empty').classList.toggle('hidden', d.accounts.length > 0);
  applyWidths($('#acc-table'));
  await loadStaging();
}

// ---- staging: open-endpoint imports that collide with an account ----
const REASON = { runtimeKey: '运行密钥相同', controlKey: '控制密钥相同', name: '名称相同' };
async function loadStaging() {
  const d = await api('GET', withPool('/api/staging'));
  const items = d.items || [];
  const cnt = $('#stg-count');
  cnt.textContent = items.length + ' 条待处理';
  cnt.classList.toggle('hidden', items.length === 0);
  $('#stg-empty').classList.toggle('hidden', items.length > 0);
  $('#stg-table tbody').innerHTML = items.map((it) => {
    const confs = it.conflicts.length ? it.conflicts.map((c) => `<div class="conf"><b>${esc(c.name)}</b> <span class="badge">${esc(c.group)}</span> <span class="muted small">${esc(REASON[c.reason] || c.reason)}</span></div>`).join('') : '<span class="muted">现已无重复</span>';
    return `<tr data-id="${esc(it.id)}">
      <td><div class="acc-name">${esc(it.name || '（未命名）')}</div><div class="chips"><span class="badge">${esc(it.group)}</span>${it.proxyUrl ? '<span class="badge" title="' + esc(it.proxyUrl) + '">代理</span>' : ''}</div>${it.note ? '<div class="acc-sub">' + esc(it.note) + '</div>' : ''}</td>
      <td><code>${esc(it.runtimeKey)}</code>${it.controlKey ? '<div class="acc-sub"><code>' + esc(it.controlKey) + '</code></div>' : '<div class="acc-sub">无控制密钥</div>'}</td>
      <td class="num">${esc(fmtTime(it.importedAt))}<div class="acc-sub">密钥 ${esc(it.importKey)}</div>${it.updatedAt !== it.importedAt ? '<div class="acc-sub">再次提交 ' + esc(fmtTime(it.updatedAt, true)) + '</div>' : ''}</td>
      <td>${confs}</td>
      <td class="actions">${it.conflicts.length ? '<button class="btn small primary" data-s="overwrite">覆盖</button>' : ''}${it.canAdd ? '<button class="btn small" data-s="add">作为新账号添加</button>' : ''}<button class="btn small danger" data-s="discard">丢弃</button></td>
    </tr>`;
  }).join('');
  $$('#stg-table [data-s]').forEach((b) => b.addEventListener('click', () => stagingAction(items.find((x) => x.id === b.closest('tr').dataset.id), b.dataset.s)));
}
async function stagingAction(it, act) {
  if (!it) return;
  try {
    if (act === 'discard') {
      if (await modal({ title: '丢弃暂存账号', body: `<p>丢弃 <b>${esc(it.name || it.runtimeKey)}</b>？它的密钥不会写入任何账号。</p>`, buttons: [{ label: '取消', value: 'c' }, { label: '丢弃', value: 'd', danger: true }] }) !== 'd') return;
      await api('DELETE', `/api/staging/${it.id}`); toast('已丢弃');
    } else if (act === 'add') {
      await api('POST', `/api/staging/${it.id}/apply`, { action: 'add' }); toast('已作为新账号添加');
    } else {
      const opts = it.conflicts.map((c, i) => `<option value="${esc(c.accountId)}" ${i === 0 ? 'selected' : ''}>${esc(c.name)}（${esc(c.group)}，${esc(REASON[c.reason] || c.reason)}）</option>`).join('');
      const r = await modal({ title: '覆盖现有账号',
        body: `<p>把 <b>${esc(it.name || '（未命名）')}</b> 的运行密钥${it.controlKey ? '、控制密钥' : ''}${it.name ? '和名称' : ''}写入所选账号，该账号的冷却/隔离会被清除，绑定到它的会话继续有效。</p>
          <div class="form"><label>覆盖账号</label><select id="stg-target">${opts}</select></div>`,
        buttons: [{ label: '取消', value: 'c' }, { label: '覆盖', value: 'ok', primary: true, action: () => api('POST', `/api/staging/${it.id}/apply`, { action: 'overwrite', target: $('#stg-target').value }) }] });
      if (r !== 'ok') return;
      toast('已覆盖');
    }
    await loadAccounts();
  } catch (e) { toast(e.message); }
}
$('#acc-group-filter').addEventListener('change', () => { ocSelected.clear(); loadAccounts().catch((e) => toast(e.message)); });
$('#btn-refresh-acc').addEventListener('click', () => loadAccounts().catch((e) => toast(e.message)));

document.addEventListener('click', (ev) => {
  const open = $('.menu-list');
  if (open && !ev.target.closest('.menu-list')) open.remove();
  if (open && ev.target.closest('[data-menu]')) return; // a second click on 操作 just closes it
  const btn = ev.target.closest('[data-menu]');
  if (!btn) return;
  const id = btn.closest('tr').dataset.id;
  const a = accountsCache.find((x) => x.id === id);
  if (!a) return;
  const items = [['edit', '编辑'], ...(a.provider === 'tokenharbor' ? [['th-proxy', '修改代理']] : []), ['test', '测试连通'], ...((a.provider === 'opencode-zen' || a.provider === 'opencode-go') ? [['proxy-test', '检测固定出口']] : []), ['refresh', a.provider === 'deploy' ? '刷新模型目录（余额未知）' : a.provider === 'opendesign' ? '检测状态并刷新额度（只读）' : a.provider === 'tokenharbor' ? '刷新模型/套餐/周用量' : a.provider === 'ollama' ? '刷新余额' : '刷新额度'], ['recover', '恢复（清除冷却/隔离）'], ['reset-health', '重置健康分'], ...(a.provider === 'opendesign' ? [['reauth', '重新设备授权']] : []), ...(a.provider === 'tokenharbor' ? [['th-return', '退回注册机']] : []), ['toggle', a.enabled ? '禁用' : '启用'], ['delete', '删除', 'danger']];
  const list = document.createElement('div');
  list.className = 'menu-list';
  list.innerHTML = items.map(([k, l, c]) => `<button data-act="${k}" class="${c || ''}">${esc(l)}</button>`).join('');
  // fixed to the viewport so the table's scroll container can't clip it
  document.body.appendChild(list);
  const rect = btn.getBoundingClientRect();
  list.style.right = Math.max(8, window.innerWidth - rect.right) + 'px';
  const below = rect.bottom + 4;
  list.style.top = (below + list.offsetHeight > window.innerHeight ? Math.max(8, rect.top - list.offsetHeight - 4) : below) + 'px';
  list.addEventListener('click', (e) => { const b = e.target.closest('[data-act]'); if (b) { list.remove(); accountAction(a, b.dataset.act); } });
});

window.addEventListener('scroll', () => { const m = $('.menu-list'); if (m) m.remove(); }, true);

async function accountAction(a, act) {
  try {
    switch (act) {
      case 'copy-key':
        if (a.provider !== 'tokenharbor' || $('#modal').open) return;
        return await showStoredAPIKeys([{ name: a.name || a.id, path: `/api/accounts/${encodeURIComponent(a.id)}/keys` }], '复制 API Key');
      case 'edit': return editAccount(a);
      case 'test': return testAccount(a);
      case 'proxy-test': { const r = await api('POST', `/api/accounts/${a.id}/test-proxy`); toast(r.ok ? ('出口可达 · ' + (GO_EGRESS_LABEL[r.egress?.status] || 'IP 未校验') + '（不代表推理授权）') : r.error); break; }
      case 'refresh': {
        const result = await api('POST', `/api/accounts/${a.id}/refresh-quota`);
        if (a.provider === 'opendesign' || a.provider === 'mistral' || a.provider === 'deploy') toast(result.message || '检测完成，请查看分项结果', result.ok ? undefined : 'error');
        else toast(a.provider === 'tokenharbor' ? '套餐、覆盖模型与周用量已刷新' : '额度已刷新');
        break;
      }
      case 'recover': await api('POST', `/api/accounts/${a.id}/recover`); toast('已恢复'); break;
      case 'reset-health': await api('POST', `/api/accounts/${a.id}/reset-health`); toast('健康分已重置'); break;
      case 'reauth': return deviceAuth({ accountId: a.id });
      case 'th-proxy': await thPoolProxy(a); break;
      case 'th-return': return await thReturnToRegistration(a.id, a.name);
      case 'toggle': await api('PUT', `/api/accounts/${a.id}`, { enabled: !a.enabled }); break;
      case 'delete':
        if (await modal({ title: '删除账号', body: `<p>确定删除 <b>${esc(a.name)}</b>？它的密钥会从配置中移除，绑定到它的会话会改派。</p>`, buttons: [{ label: '取消', value: 'c' }, { label: '删除', value: 'del', danger: true }] }) !== 'del') return;
        await api('DELETE', `/api/accounts/${a.id}`); toast('已删除'); break;
    }
    await loadAccounts();
  } catch (e) { toast(e.message, 'error'); }
}

function accountForm(a) {
  const v = (k, d = '') => esc(a ? (a[k] ?? d) : d);
  const defGroup = defaultGroup();
  return `<div class="form">
    <label>名称</label><input id="f-name" value="${v('name')}" maxlength="50">
    <label>邮箱</label><input id="f-email" value="${v('email')}">
    <label>分组</label><input id="f-group" value="${v('group', defGroup)}" placeholder="${defGroup}">
    <span class="hint">下游密钥只调度同分组的账号</span>
    <label>运行密钥</label><input id="f-rt" value="${v('runtimeKey')}" placeholder="${a ? '留空不变' : RT_HINT[pool] || RT_HINT.opendesign}" autocomplete="off">
    ${!isOpenDesign() ? `<span></span><span class="hint">${isDeploy() ? 'Deploy 使用原生 Chat Completions；Secret 原样保存，不是 access_key。目录成功不代表 Key 已 active 或有推理授权，余额需独立控制台会话，当前未知。不会自动注册、创建 Key、绑卡或充值。' : isMistral() ? 'Mistral 独立号池，使用现有调度器；识别 Codex / Claude / pi 会话头，不代表已完成各客户端验收。支持原生 Chat / 无状态 Responses，保留思考与工具，自动补会话缓存 key。DeepSeek V4.1 Flash 1017、GLM 5.3 / Flash 支持 low / high / max，未传默认 high。可刷新 Studio / Vibe 套餐额度与组织订阅，不是钱包余额；无控制密钥，管理员可直接查看和复制 Key。' : isTokenHarbor() ? '原生 Chat / Responses / Messages，保留缓存标记，会话默认不溢出。管理员可直接查看和复制 Key；需通过原注册记录关联会话才能自动检测套餐，单独导入 Key 无法确认套餐时暂停。未知模型不放行，不借钱包，免费功能不自动开启。' : isOpenCode() ? 'OpenCode 套餐必须明确选择；仅 Go 具有已知的 usage 查询契约，Zen 余额显示未知。管理员可直接查看和复制密钥。' : isModal()
      ? 'Modal 只有 Proxy Token 一把钥匙：它既能推理，也能列出该 token 可见的 Endpoint；Modal 没有额度接口，没有控制密钥。'
      : isOllama() 
        ? 'Ollama 的 key 直接读 /api/balance，没有单独的控制密钥；自建服务填任意值（如 <code>ollama</code>）即可。'
        : 'River 直接使用 API Key（rv_…）推理，没有额度接口，没有控制密钥。'}</span>`
      : `<label>控制密钥</label><input id="f-ctrl" value="${v('controlKey')}" placeholder="vela_ctrl_…（留空不变）" autocomplete="off">
    ${a && a.hasControlKey ? '<span></span><label class="check"><input type="checkbox" id="f-clear-ctrl">移除控制密钥</label>' : ''}`}
    ${a ? `<span></span><span class="keycopy"><button type="button" class="btn small" data-copy="runtimeKey">复制运行密钥</button>${a.hasControlKey && isOpenDesign() ? '<button type="button" class="btn small" data-copy="controlKey">复制控制密钥</button>' : ''}${a.hasProxy ? '<button type="button" class="btn small" data-copy="proxyUrl">复制代理</button>' : ''}</span>` : ''}
    ${!a ? `<span></span><span class="hint">新账号进 ${POOL_LABEL[pool]} 号池${keyNeedsProvider() ? '（provider=' + pool + '）' : ''}</span>` : ''}
    <label>最大并发</label><input id="f-conc" type="number" min="0" value="${v('maxConcurrent', pool === 'opencode-go' ? 1 : 0)}">
    <span class="hint">0 表示不限</span>
    <label>每分钟请求</label><input id="f-rpm" type="number" min="0" value="${v('maxRpm', 0)}">
    <label>权重</label><input id="f-weight" type="number" min="1" max="100" value="${v('weight', 1)}">
    <label>优先级</label><input id="f-prio" type="number" min="1" max="100" value="${v('priority', 50)}">
    <span class="hint">数字越小越优先，同优先级内按策略排序</span>
    <label>出站代理</label><input id="f-proxy" type="text" autocomplete="off" value="${v('proxyUrl')}" placeholder="http:// 或 socks5://（留空不变）">
    ${pool === 'tokenharbor' ? '<span></span><span class="hint">号池与注册机代理独立设置，此处修改不影响原注册记录；新建入池时默认沿用注册代理。换出口后旧套餐/周用量失效，重新检测通过前暂停调度。</span>' : ''}
  ${pool === 'opencode-go' ? '<span></span><span class="hint">支持 HTTP/HTTPS、SOCKS5/SOCKS5H 的 IPv6 代理，例如 socks5h://[2001:db8::10]:1080（示例地址须替换）。IPv6 主机须带方括号；代理入口地址不代表它的实际出口。</span>' : ''}
    ${a && a.hasProxy ? '<span></span><label class="check"><input type="checkbox" id="f-clear-proxy">移除代理（直连）</label>' : ''}
    ${pool === 'opencode-go' ? `<label>预期固定出口 IP</label><input id="f-egress-ip" value="${v('egressIp')}" placeholder="IPv4/IPv6，不带方括号；可共享同一固定 IP"><span class="hint">先在 Go 设置配置 IP 校验 URL。留空仅绑定出口，不宣称 IP 已固定；设置后校验失败会阻止推理及额度/目录查询。</span>` : ''}
    <label>备注</label><input id="f-note" value="${v('note')}" maxlength="500">
    <label>启用</label><label class="check"><input type="checkbox" id="f-enabled" ${!a || a.enabled ? 'checked' : ''}>参与调度</label>
  </div>`;
}
function readAccountForm() {
  const n = (id) => Number($(id).value || 0);
  const out = { name: $('#f-name').value, email: $('#f-email').value, group: $('#f-group').value, note: $('#f-note').value,
    maxConcurrent: n('#f-conc'), maxRpm: n('#f-rpm'), weight: n('#f-weight'), priority: n('#f-prio'), enabled: $('#f-enabled').checked };
  if ($('#f-egress-ip')) out.egressIp = $('#f-egress-ip').value.trim();
  if ($('#f-rt').value.trim()) out.runtimeKey = $('#f-rt').value.trim();
  if ($('#f-ctrl') && $('#f-ctrl').value.trim()) out.controlKey = $('#f-ctrl').value.trim();
  if (keyNeedsProvider()) out.provider = pool; // an Ollama key has no prefix to read this from
  if ($('#f-proxy').value.trim()) out.proxyUrl = $('#f-proxy').value.trim();
  if ($('#f-clear-ctrl') && $('#f-clear-ctrl').checked) out.clearControlKey = true;
  if ($('#f-clear-proxy') && $('#f-clear-proxy').checked) out.clearProxy = true;
  return out;
}
let editingAccountId = null;
document.addEventListener('click', async (e) => {
  const b = e.target.closest('[data-copy]');
  if (!b || !editingAccountId) return;
  copyAccountField(editingAccountId, b.dataset.copy);
});
async function editAccount(a) {
  editingAccountId = a.id;
  await modal({ title: '编辑账号', body: accountForm(a), buttons: [{ label: '取消', value: 'c' }, { label: '保存', value: 'ok', primary: true, action: async () => {
    await api('PUT', `/api/accounts/${a.id}`, readAccountForm()); toast('已保存');
  } }] });
  editingAccountId = null;
  loadAccounts().catch(() => {});
}
$('#btn-add-manual').addEventListener('click', async () => {
  await modal({ title: '手动添加账号', body: accountForm(null), buttons: [{ label: '取消', value: 'c' }, { label: '添加', value: 'ok', primary: true, action: async () => {
    const f = readAccountForm();
    if (!f.runtimeKey) throw new Error('运行密钥必填');
    await api('POST', '/api/accounts', f); toast('已添加');
  } }] });
  loadAccounts().catch(() => {});
});
$('#btn-import').addEventListener('click', async () => {
  const help = isDeploy()
    ? '<p class="muted small">每行 <code>Secret Key[,名称[,代理URL]]</code>；不是 access_key，不加前缀。固定 provider=deploy，重复 Key 跳过；分组、客户端及导入密钥与其他池隔离。</p>'
    : isMistral()
    ? '<p class="muted small">每行 <code>API Key[,名称[,代理URL]]</code>，原样粘贴 Mistral 密钥；独立分组，无控制密钥。相同 Key 去重。</p>'
    : isTokenHarbor()
    ? '<p class="muted small">每行 <code>thk_live_…[,名称[,代理URL]]</code>；导入到 Token Harbor 独立分组，无控制密钥。</p>'
    : isOpenCode()
    ? `<p class="muted small">每行 <code>API Key[,名称[,固定代理URL]]</code>。导入到 ${esc(POOL_LABEL[pool])}；相同 Key 去重。不要将不同套餐放入同一分组。</p>`
    : isModal()
    ? `<p class="muted small">每行一个账号：<code>运行密钥[,名称[,代理URL]]</code>。Modal 的 Proxy Token 形如 <code>wk-&lt;id&gt;.ws-&lt;secret&gt;</code>，<b>两段都要</b>（只有 <code>wk-</code> 那段上游会回 401）。Modal 没有控制密钥。逗号、空格或制表符分隔，<code>#</code> 开头为注释。</p>`
    : isOllama()
      ? `<p class="muted small">每行一个账号：<code>运行密钥[,名称[,代理URL]]</code>。Ollama 的 key 没有固定前缀，导入时整行按本池默认的 <code>provider=ollama</code> 处理；也可以在行首写 <code>provider:ollama</code> 明确指定。逗号、空格或制表符分隔，<code>#</code> 开头为注释。</p>`
      : `<p class="muted small">每行一个账号：<code>运行密钥[,控制密钥[,名称[,代理URL]]]</code>（<code>rv_</code> 开头的自动识别为 River 账号、<code>wk-</code> 开头的是 Modal 账号，都没有控制密钥），逗号、空格或制表符分隔，<code>#</code> 开头为注释。行首可写 <code>provider:ollama</code> / <code>provider:modal</code> 指定号池。</p>`;
  const ph = { opendesign: 'sk-amr-xxx,vela_ctrl_xxx,账号A\nrv_xxx,River-1\nwk-xxx.ws-yyy,Modal-1', river: 'rv_xxx,River-1',
    deploy: 'DEPLOY_SECRET_EXAMPLE,Deploy-1', mistral: 'mstrl_example_key,Mistral-1', tokenharbor: 'thk_live_example,TokenHarbor-1', modelstudio: 'sk-sp-example_key,百炼-1', ollama: 'xxxxxxxxxxxxxxxxxxxxxxxx,ollama-1', modal: 'wk-xxxxxxxx.ws-yyyyyyyy,Modal-1', 'opencode-zen': 'ZEN_API_KEY,zen-1', 'opencode-go': 'GO_API_KEY,go-1' }[pool];
  await modal({ title: '批量导入', wide: true,
    body: `${help}
      <textarea id="imp-text" class="code" rows="10" placeholder="${esc(ph)}"></textarea>
      <div class="row"><label class="muted">分组</label><input id="imp-group" placeholder="${defaultGroup()}"></div>`,
    buttons: [{ label: '取消', value: 'c' }, { label: '导入', value: 'ok', primary: true, action: async () => {
      const r = await api('POST', '/api/accounts/import', { text: $('#imp-text').value, group: $('#imp-group').value, provider: keyNeedsProvider() ? pool : '' });
      toast(`已导入 ${r.added} 个账号${r.unchanged ? ` · 跳过 ${r.unchanged} 个重复项` : ''}`);
    } }] });
  loadAccounts().catch(() => {});
});

async function testAccount(a) {
  let lastCatalog = [];
  const run = async (inference) => {
    $('#test-out').textContent = '测试中…';
    const r = await api('POST', `/api/accounts/${a.id}/test`, { inference, model: $('#test-model').value.trim() });
    if (Array.isArray(r && r.runtime && r.runtime.modelIds) && r.runtime.modelIds.length) lastCatalog = r.runtime.modelIds;
    $('#test-out').textContent = JSON.stringify(r, null, 2);
  };
  await modal({ title: '测试 ' + a.name, wide: true,
    body: `<p class="muted small">「免费检查」只读取${(a.provider === 'opencode-zen' || a.provider === 'opencode-go') ? '账号模型列表、协议能力和 Go 额度（若支持）' : a.provider === 'deploy' ? '模型目录（不校验 Key 激活状态，不代表推理授权；余额未知）' : a.provider === 'mistral' ? '模型列表（套餐额度请点刷新额度）' : a.provider === 'tokenharbor' ? '模型列表（余额未知）' : a.provider === 'ollama' ? '模型列表和 /api/balance 余额' : a.provider === 'modal' ? '该 token 能访问的 Endpoint 列表' : '模型列表和工作区'}，不消耗额度。「真实请求」会发一条 max_tokens=${a.provider === 'deploy' ? '256（包含 reasoning；上限，不是必然用量）' : '1'} 的推理，消耗少量 ${a.provider === 'modal' ? 'token（按量计费）' : 'credits'}。</p>
      <div class="row"><input id="test-model" placeholder="真实请求模型，默认 ${esc(a.provider === 'ollama' ? 'gpt-oss:20b' : a.provider === 'river' ? 'deepseek-ai/DeepSeek-V4.1-Flash' : a.provider === 'deploy' ? 'deepseek-ai/deepseek-v4.1-flash-1017' : a.provider === 'mistral' ? 'deepseek-v4-1-flash-1017' : (a.provider === 'modal' || a.provider === 'tokenharbor' || a.provider === 'opencode-zen' || a.provider === 'opencode-go') ? '该账号模型列表中的首个模型（按原生协议）' : 'deepseek-v4.1-flash')}" size="34"></div>
      ${a.provider === 'deploy' ? `<div class="row muted small">每个账号目录可能只放行部分模型：先点「免费检查」读到模型列表，再点「用目录里的模型」填入。<button type="button" class="btn small ghost" id="test-model-fill">用目录里的模型</button></div>` : ''}
      <pre id="test-out" class="mt">点击下方按钮开始</pre>`,
    onOpen: () => {
      const fill = $('#test-model-fill');
      if (fill) fill.onclick = () => {
        if (!lastCatalog.length) { toast('先点「免费检查」读到该账号的模型列表'); return; }
        $('#test-model').value = lastCatalog[0];
        toast('已填入 ' + lastCatalog[0]);
      };
    },
    buttons: [{ label: '关闭', value: 'c' }, { label: '真实请求', value: 'inf', action: async () => { await run(true); return false; } },
      { label: '免费检查', value: 'free', primary: true, action: async () => { await run(false); return false; } }] });
}

// ---- device authorization ----
async function deviceAuth(opts = {}) {
  let flow = null, timer = null;
  const groups = [...new Set(accountsCache.map((a) => a.group).concat([defaultGroup()]))].sort();
  const step1 = !opts.accountId ? `<div class="form"><label>分组</label><select id="da-group">${groups.map((g) => `<option>${esc(g)}</option>`).join('')}</select>
      <label>出站代理</label><input id="da-proxy" placeholder="可选，签发与后续请求都走它"></div>` : '';
  const target = opts.accountId ? accountsCache.find((a) => a.id === opts.accountId) : null;
  const render = () => {
    if (!flow) return;
    const st = { pending: '等待批准…', approved: '已批准', expired: '已过期', failed: '失败', cancelled: '已取消' }[flow.status] || flow.status;
    $('#da-flow').innerHTML = `<p>在<b>已登录目标账号</b>的浏览器中打开激活页，确认验证码后批准：</p>
      <div class="code-big">${esc(flow.userCode)}</div>
      <p><a href="${esc(flow.activationUrl)}" target="_blank" rel="noopener noreferrer" class="btn primary">打开激活页</a>
      <button type="button" class="btn ghost small" id="da-copy">复制链接</button></p>
      <p>状态：<span class="badge ${flow.status === 'approved' ? 'ok' : flow.status === 'pending' ? 'accent' : 'bad'}">${esc(st)}</span>
      ${flow.email ? ' · ' + esc(flow.email) : ''}${flow.error ? ' · ' + esc(flow.error) : ''}</p>
      <p class="muted small">有效期至 ${esc(fmtTime(flow.expiresAt))}。批准使用的是该浏览器会话，请确认登录的是正确账号。</p>`;
    const copy = $('#da-copy');
    if (copy) copy.onclick = () => navigator.clipboard.writeText(flow.activationUrl).then(() => toast('已复制'));
  };
  const poll = async () => {
    try {
      flow = await api('GET', `/api/device-auth/${flow.id}`);
      render();
      if (flow.status !== 'pending') {
        clearInterval(timer);
        if (flow.status === 'approved') { toast(flow.updated ? '已更新账号密钥：' + flow.email : '已添加账号：' + flow.email); loadAccounts().catch(() => {}); }
      }
    } catch (e) { modalErr(e.message); }
  };
  await modal({
    title: target ? '重新授权 ' + target.name : '设备授权添加账号',
    body: `${step1}<div id="da-flow" class="mt"><p class="muted">点「开始」向 OpenDesign 申请一个设备授权码。</p></div>`,
    onOpen: (dlg) => { dlg._onClose = () => { clearInterval(timer); if (flow && flow.status === 'pending') api('DELETE', `/api/device-auth/${flow.id}`).catch(() => {}); }; },
    buttons: [{ label: '关闭', value: 'c' }, { label: '开始', value: 'start', primary: true, action: async () => {
      if (flow && flow.status === 'pending') return false;
      const body = opts.accountId ? { accountId: opts.accountId } : { group: $('#da-group').value, proxyUrl: $('#da-proxy').value.trim() };
      flow = await api('POST', '/api/device-auth', body);
      render();
      clearInterval(timer);
      timer = setInterval(poll, 2000);
      return false;
    } }],
  });
}
$('#btn-add-device').addEventListener('click', () => deviceAuth());

// ---- client keys ----
async function loadKeys() {
  const d = await api('GET', withPool('/api/client-keys'));
  $('#key-table tbody').innerHTML = d.keys.map((k) => `<tr data-id="${esc(k.id)}">
    <td>${esc(k.name || '—')}</td><td><span class="badge">${esc(k.group)}</span></td><td><code>${esc(k.keyPrefix)}</code></td>
    <td><span class="badge ${k.enabled ? 'ok' : ''}">${k.enabled ? '启用' : '停用'}</span></td><td class="num">${esc(fmtTime(k.createdAt, true))}</td>
    <td class="actions"><button class="btn small" data-k="copy"${k.key ? '' : ' disabled title="该密钥创建于支持复制之前，无法找回，请新建"'}>复制</button><button class="btn small" data-k="toggle">${k.enabled ? '停用' : '启用'}</button><button class="btn small" data-k="edit">编辑</button><button class="btn small danger" data-k="del">删除</button></td></tr>`).join('');
  $('#key-empty').classList.toggle('hidden', d.keys.length > 0);
  await loadImportKeys();
  $$('#key-table [data-k]').forEach((b) => b.addEventListener('click', async () => {
    const id = b.closest('tr').dataset.id;
    const k = d.keys.find((x) => x.id === id);
    try {
      if (b.dataset.k === 'copy') return copyText(k.key);
      if (b.dataset.k === 'toggle') await api('PUT', `/api/client-keys/${id}`, { enabled: !k.enabled });
      if (b.dataset.k === 'del') {
        if (await modal({ title: '删除下游密钥', body: `<p>删除后使用 <code>${esc(k.keyPrefix)}</code> 的调用方会立即 401。</p>`, buttons: [{ label: '取消', value: 'c' }, { label: '删除', value: 'd', danger: true }] }) !== 'd') return;
        await api('DELETE', `/api/client-keys/${id}`);
      }
      if (b.dataset.k === 'edit') {
        await modal({ title: '编辑密钥', body: `<div class="form"><label>名称</label><input id="k-name" value="${esc(k.name)}"><label>分组</label><input id="k-group" value="${esc(k.group)}"></div>`,
          buttons: [{ label: '取消', value: 'c' }, { label: '保存', value: 'ok', primary: true, action: () => api('PUT', `/api/client-keys/${id}`, { name: $('#k-name').value, group: $('#k-group').value }) }] });
      }
      loadKeys();
    } catch (e) { toast(e.message); }
  }));
}
$('#btn-new-key').addEventListener('click', async () => {
  let created = null;
  await modal({ title: '新建下游密钥',
    body: `<div class="form"><label>名称</label><input id="k-name" placeholder="new-api"><label>分组</label><input id="k-group" placeholder="${defaultGroup()}" value="${defaultGroup()}"><span class="hint">只调度该分组的账号；这把密钥归 ${POOL_LABEL[pool]} 号池，不会调度其他池的账号</span></div><div id="k-out"></div>`,
    buttons: [{ label: '关闭', value: 'c' }, { label: '创建', value: 'ok', primary: true, action: async () => {
      if (created) return false;
      created = await api('POST', '/api/client-keys', { name: $('#k-name').value, group: $('#k-group').value, provider: pool });
      $('#k-out').innerHTML = `<p class="mt"><b>密钥已创建，可随时在列表中复制：</b></p><div class="keybox"><code>${esc(created.key)}</code><button type="button" class="btn small" id="k-copy">复制</button></div>`;
      $('#k-copy').onclick = () => copyText(created.key);
      return false;
    } }] });
  loadKeys().catch(() => {});
});

// ---- import keys (open account-import endpoint) ----
async function loadImportKeys() {
  const d = await api('GET', withPool('/api/import-keys'));
  $('#import-url').textContent = location.origin + '/open/v1/accounts/import';
  const example = isDeploy() ? { provider: 'deploy', runtimeKey: 'DEPLOY_SECRET_EXAMPLE', name: '账号名', group: 'deploy' } : { runtimeKey: 'sk-amr-…', controlKey: 'vela_ctrl_…', name: '账号名', group: '可选', note: '可选' };
  $('#import-example').textContent = JSON.stringify(example);
  $('#channel-protocol-guide').textContent = isDeploy() ? 'Deploy 渠道类型选 OpenAI，仅 Chat Completions；Responses / Anthropic Messages 本地拒绝，不做转换。' : '渠道类型选 OpenAI；号池支持 /v1/messages 时可选 Anthropic Claude（请先核对本池协议范围）。';
  $('#import-curl').textContent = `curl -X POST ${location.origin}/open/v1/accounts/import \\\n  -H 'Authorization: Bearer sk-odbimp-…' -H 'Content-Type: application/json' \\\n  -d '{"runtimeKey":"sk-amr-…","controlKey":"vela_ctrl_…","name":"账号A"}'`;
  if (isDeploy()) $('#import-curl').textContent = `curl -X POST ${location.origin}/open/v1/accounts/import -H 'Authorization: Bearer sk-odbimp-…' -H 'Content-Type: application/json' -d '${JSON.stringify(example)}'`;
  $('#ik-table tbody').innerHTML = d.keys.map((k) => `<tr data-id="${esc(k.id)}">
    <td>${esc(k.name || '—')}</td><td><span class="badge">${esc(k.group)}</span></td><td><code>${esc(k.keyPrefix)}</code></td>
    <td><span class="badge ${k.enabled ? 'ok' : ''}">${k.enabled ? '启用' : '停用'}</span></td><td class="num">${esc(fmtTime(k.createdAt, true))}</td>
    <td class="actions"><button class="btn small" data-k="copy"${k.key ? '' : ' disabled title="该密钥创建于支持复制之前，无法找回，请新建"'}>复制</button><button class="btn small" data-k="toggle">${k.enabled ? '停用' : '启用'}</button><button class="btn small" data-k="edit">编辑</button><button class="btn small danger" data-k="del">删除</button></td></tr>`).join('');
  $('#ik-empty').classList.toggle('hidden', d.keys.length > 0);
  $$('#ik-table [data-k]').forEach((b) => b.addEventListener('click', async () => {
    const id = b.closest('tr').dataset.id;
    const k = d.keys.find((x) => x.id === id);
    try {
      if (b.dataset.k === 'copy') return copyText(k.key);
      if (b.dataset.k === 'toggle') await api('PUT', `/api/import-keys/${id}`, { enabled: !k.enabled });
      if (b.dataset.k === 'del') {
        if (await modal({ title: '删除导入密钥', body: `<p>删除后使用 <code>${esc(k.keyPrefix)}</code> 的自动上号脚本会立即 401。已导入的账号不受影响。</p>`, buttons: [{ label: '取消', value: 'c' }, { label: '删除', value: 'd', danger: true }] }) !== 'd') return;
        await api('DELETE', `/api/import-keys/${id}`);
      }
      if (b.dataset.k === 'edit') {
        await modal({ title: '编辑导入密钥', body: `<div class="form"><label>名称</label><input id="ik-name" value="${esc(k.name)}"><label>默认分组</label><input id="ik-group" value="${esc(k.group)}"></div>`,
          buttons: [{ label: '取消', value: 'c' }, { label: '保存', value: 'ok', primary: true, action: () => api('PUT', `/api/import-keys/${id}`, { name: $('#ik-name').value, group: $('#ik-group').value }) }] });
      }
      loadImportKeys();
    } catch (e) { toast(e.message); }
  }));
  await loadImportLog();
}
async function loadImportLog() {
  // the console shows one pool at a time: each tab lists only its own imports
  const d = await api('GET', '/api/import-log?provider=' + encodeURIComponent(pool));
  const items = d.items || [];
  const n = (v) => v ? num(v) : '<span class="muted">0</span>';
  $('#ilog-table tbody').innerHTML = items.map((it) => `<tr>
    <td class="num">${esc(fmtTime(it.at))}</td>
    <td>${it.source === 'console' ? '管理台批量导入' : '开放接口'}${it.importKey ? '<div class="acc-sub">密钥 ' + esc(it.importKey) + '</div>' : ''}${it.group ? '<div class="acc-sub">分组 ' + esc(it.group) + '</div>' : ''}</td>
    <td><code>${esc(it.ip)}</code>${it.peer ? '<div class="acc-sub" title="TCP 对端（代理）">经 ' + esc(it.peer) + '</div>' : ''}</td>
    <td class="num">${n(it.total)}</td><td class="num">${n(it.added)}</td><td class="num">${n(it.staged)}</td><td class="num">${n(it.unchanged)}</td><td class="num">${n(it.failed)}</td>
    <td><span class="badge ${it.status === 200 ? 'ok' : it.status < 300 ? 'warn' : 'bad'}">${esc(it.status)}</span>${it.error ? '<div class="acc-sub">' + esc(it.error) + '</div>' : ''}</td>
  </tr>`).join('');
  $('#ilog-empty').classList.toggle('hidden', items.length > 0);
}
$('#btn-refresh-ilog').addEventListener('click', () => loadImportLog().catch((e) => toast(e.message)));
$('#btn-new-import-key').addEventListener('click', async () => {
  let created = null;
  await modal({ title: '新建导入密钥',
    body: `<div class="form"><label>名称</label><input id="ik-name" placeholder="signup-bot"><span class="hint">会写进导入账号的备注，便于追溯来源</span><label>默认分组</label><input id="ik-group" placeholder="${defaultGroup()}" value="${defaultGroup()}"><span class="hint">请求不带 group、provider 时导入到该分组，并进 ${POOL_LABEL[pool]} 号池</span></div><div id="ik-out"></div>`,
    buttons: [{ label: '关闭', value: 'c' }, { label: '创建', value: 'ok', primary: true, action: async () => {
      if (created) return false;
      created = await api('POST', '/api/import-keys', { name: $('#ik-name').value, group: $('#ik-group').value, provider: pool });
      $('#ik-out').innerHTML = `<p class="mt"><b>密钥已创建，可随时在列表中复制：</b></p><div class="keybox"><code>${esc(created.key)}</code><button type="button" class="btn small" id="ik-copy">复制</button></div>`;
      $('#ik-copy').onclick = () => copyText(created.key);
      return false;
    } }] });
  loadImportKeys().catch(() => {});
});

// ---- settings ----
const STRATEGY_HINTS = {
  smart: '智能（默认，参考 magpie Smart）：余量充足的账号里，窗口最早重置的先用，避免额度在重置时浪费；余量低于阈值的排后，钱包付费的最后',
  pace: '周额度节奏：剩余比例 ÷ 距重置小时数，越高越先用',
  'least-used': '用量最少优先：按窗口已用比例，其次按近期 token',
  'least-requests-1h': '近1小时请求最少：先剔除不可用、明确额度耗尽及低于最低健康分的账号（强制健康过滤），再在同优先级内比较近60个分钟桶的已发推理次数＋待发预约；保留会话/缓存亲和，额度查询不计入',
  weighted: '按权重平滑轮询（NGINX 平滑加权）',
  roundrobin: '轮询：最久未被选中者优先',
  'least-selections': '选中次数最少优先，平局按在途数',
  'least-connections': '在途请求最少优先',
  ordered: '按配置顺序：前一个用尽或失败才用下一个',
};
const ROUTING_FIELDS = [
  ['strategy', '选号策略', 'strategy'],
  ['sticky', '会话粘性', 'bool', '同一会话固定到首次应答的账号，命中上游缓存'],
  ['onBindingBusy', '绑定账号忙时', 'select', ['overflow', 'wait-overflow', 'reject'], 'overflow 临时借用其他账号但不改绑；wait-overflow 先等待再借；reject 直接 429'],
  ['explicitTtlSec', '显式会话 TTL（秒）', 'int', '有会话头/字段时，滑动过期'],
  ['fallbackHash', '消息哈希弱粘性', 'bool', '没有会话标识时，按首条 system 与 user 消息生成指纹'],
  ['fallbackTtlSec', '弱粘性 TTL（秒）', 'int'],
  ['sessionHeaders', '额外会话头', 'list', '逗号分隔，优先于内置会话头，例如 X-User-Session'],
  ['concurrencyWaitMs', '满载等待（毫秒）', 'int', '所有账号并发/RPM 满时最多等待，超时返回 429'],
  ['maxAccountsPerRequest', '单请求最多账号', 'int', '仅在首包前、账号被冷却/隔离/401 时换号'],
  ['walletFallback', '允许钱包付费', 'bool', '付费顺序：Design Plan 窗口（仅 Design Plan 模型）→ 套餐模型额度 → 钱包/充值。关闭后只剩钱包能付的账号不调度'],
  ['ignoreCatalog', '忽略套餐模型权限', 'bool', '默认按每把 API Key 自己的 /v1/models 判断套餐能调哪些模型，套餐外的模型不派给该账号；开启后不做此检查'],
  ['lowShare', '已用多少 % 开始靠后', 'float', '按已用百分比比较（不是剩余）：账号用得最满的额度窗口已用达到该值，就排到余量充足的账号之后。默认 90'],
  ['spentShare', '已用多少 % 视为耗尽', 'float', '已用达到该值，再往后排（仍可被选中，不会被排除）。须不小于上一项，默认 98'],
  ['healthFilter', '健康过滤', 'bool', '24h 成功率低于最低健康分的账号不参与调度；近1小时请求最少策略强制过滤，不受此开关影响。其他策略按此开关；样本少于 20 为未知，暂不剔除'],
  ['minimumHealth', '最低健康分', 'float'],
  ['firstEventTimeoutSec', '流式首包超时（秒）', 'int'],
  ['idleTimeoutSec', '流式空闲超时（秒）', 'int'],
  ['heartbeatSec', '心跳间隔（秒）', 'int'],
  ['requestTimeoutSec', '非流式超时（秒）', 'int'],
  ['maxBodyMB', '请求体上限（MB）', 'int'],
  ['modelsCacheSec', '模型列表缓存（秒）', 'int'],
];
const OPENCODE_FIELDS = [
  ['enabled', '启用 OpenCode 接入', 'bool'],
  ['compatibility', '受限协议转换（缓冲流）', 'bool'],
  ['zenBase', 'Zen API Base（包含 /v1）', 'text'], ['goBase', 'Go API Base（包含 /v1）', 'text'],
  ['capabilitiesUrl', '公开能力目录 URL', 'text'], ['refreshSec', '模型刷新间隔（秒）', 'int'],
  ['zenDocsUrl', 'Zen 端点文档 URL（可选）', 'text'], ['goDocsUrl', 'Go 端点文档 URL（可选）', 'text'],
  ['zenFallbackGroup', 'Zen 回退的 Go 分组（默认不回退）', 'text'], ['goFallbackGroup', 'Go 回退的 Zen 分组（默认不回退）', 'text'],
  ['goEgressCheckUrl', 'Go 出口 IP 校验 URL（可选）', 'text', '返回纯 IP 或 {"ip":"…"}；请求不带凭证，走账号固定出口；空值不联系第三方'],
  ['goEgressCheckSec', 'Go IP 校验新鲜度（秒）', 'int', '5–3600；不能让动态代理变成静态出口'],
  ['goPolicyCooldownSec', 'Go 政策拒绝暂停（秒）', 'int', '1–86400；不换号/出口，不清空订阅额度，可人工恢复'],
];
const openCodeFields = () => OPENCODE_FIELDS.filter(([k]) => pool === 'opencode-go' ? !k.startsWith('zen') && k !== 'goFallbackGroup' : !['goEgressCheckUrl','goEgressCheckSec','goPolicyCooldownSec'].includes(k));
const MISTRAL_PERFORMANCE_FIELDS = [
  ['maxBufferedMB', '字节预算（MiB）', 'int', '1–65536；缺省2048，并收紧至Go软内存上限的1/3；满额明确拒绝，不无界排队'],
  ['maxWaitingRequests', '每组最多等待请求数', 'int', '0不等待；1–100000，缺省1024。满载等待毫秒仍决定等待时长，不是并发数'],
  ['uploadTimeoutSec', '上传读取超时（秒）', 'int', '1–3600，缺省60；只约束读入，不截断正常长流'],
  ['writeTimeoutSec', '单次流写超时（秒）', 'int', '1–600，缺省30；每次刷新续期，防止慢读客户端长期占号'],
  ['detailSamplePercent', '正文详情采样（%）', 'int', '0–100，缺省100保持原全量；须开启本池详情开关，同一请求两端一致采样'],
];
const MISTRAL_CONNECTION_FIELDS = [
  ['directMaxConns', '直连每Host连接上限', 'int', '1–100000，本池独立Transport实际生效；HTTP2连接数不等于流并发'],
  ['proxyMaxConns', '固定代理每Host连接上限', 'int', '1–10000，仍走账号固定代理；变更只关闭旧空闲连接，不中断在途'],
];
const routingFields = () => isDeploy() ? ROUTING_FIELDS.filter(([k]) => !['walletFallback','lowShare','spentShare'].includes(k)).map((f) => f[0] === 'ignoreCatalog' ? ['ignoreCatalog', '忽略目录模型过滤', 'bool', '目录只是模型发现，不证明本 Key 推理授权；关闭时按新鲜目录排除未列出的模型'] : f[0] === 'sticky' ? ['sticky', '会话粘性', 'bool', '尽量复用同一账号，不保证上游缓存命中'] : f).concat(MISTRAL_CONNECTION_FIELDS) : pool === 'opencode-go' ? ROUTING_FIELDS.filter(([k]) => !['onBindingBusy','walletFallback','ignoreCatalog'].includes(k)) : isTokenHarbor() ? ROUTING_FIELDS.filter(([k]) => !['walletFallback','ignoreCatalog'].includes(k)) : isMistral() ? ROUTING_FIELDS.filter(([k]) => k !== 'walletFallback').concat(MISTRAL_CONNECTION_FIELDS) : ROUTING_FIELDS;
const QUOTA_FIELDS = [
  ['enabled', '启用额度轮询', 'bool'],
  ['intervalSec', '轮询间隔（秒）', 'int', '建议 30–60 秒'],
  ['concurrency', '并发读取数', 'int'],
  ['staleSec', '快照过期（秒）', 'int', '过期后按未知额度处理'],
];
const LOG_FIELDS = [
  ['retentionDays', '日志保留天数（默认7天）', 'int'], ['maxRequests', '请求日志条数', 'int'], ['maxErrors', '错误日志条数', 'int'], ['maxTotalMB', '总容量（MB）', 'int'],
  ['detail', '详细日志', 'bool', '记录请求头/体、响应头/体（凭据已脱敏），排查完请关闭'], ['maxDetails', '详细日志条数', 'int'], ['detailBodyKB', '单个体上限（KB）', 'int', '超出时保留头尾各一半、省略中间；内存中的详细日志总量上限 256MB，超出自动淘汰最旧的'],
];
const logFields = () => (isTokenHarbor() || isMistral() || isDeploy()) ? LOG_FIELDS.filter(([k]) => k !== 'detail') : LOG_FIELDS;
function renderForm(el, fields, obj, prefix) {
  el.innerHTML = fields.map(([k, label, type, extra, hint]) => {
    const id = `${prefix}-${k}`;
    const v = obj[k];
    let input;
    if (type === 'bool') input = `<label class="check"><input type="checkbox" id="${id}" ${v ? 'checked' : ''}>开启</label>`;
    else if (type === 'strategy') input = `<select id="${id}">${settingsCache.strategies.map((s) => `<option ${s === v ? 'selected' : ''}>${esc(s)}</option>`).join('')}</select>`;
    else if (type === 'select') input = `<select id="${id}">${extra.map((s) => `<option ${s === v ? 'selected' : ''}>${esc(s)}</option>`).join('')}</select>`;
    else if (type === 'text') input = `<input id="${id}" value="${esc(v || '')}" autocomplete="off">`;
    else if (type === 'list') input = `<input id="${id}" value="${esc((v || []).join(', '))}">`;
    else input = `<input id="${id}" type="number" step="${type === 'float' ? 'any' : '1'}" value="${esc(v)}">`;
    const h = type === 'select' ? hint : extra;
    const hintHTML = type === 'strategy' ? `<span class="hint" id="${id}-hint">${esc(STRATEGY_HINTS[v] || '')}</span>` : (h && typeof h === 'string' ? `<span class="hint">${esc(h)}</span>` : '');
    return `<label for="${id}">${esc(label)}</label>${input}${hintHTML}`;
  }).join('');
}
function readForm(fields, prefix, base) {
  const out = { ...base };
  for (const [k, , type] of fields) {
    const el = $(`#${prefix}-${k}`);
    if (type === 'bool') out[k] = el.checked;
    else if (type === 'int') out[k] = parseInt(el.value || '0', 10);
    else if (type === 'float') out[k] = parseFloat(el.value || '0');
    else if (type === 'list') out[k] = el.value.split(',').map((s) => s.trim()).filter(Boolean);
    else out[k] = el.value;
  }
  return out;
}
// ---- error / retry rules (visual editor) ----
const clone = (v) => JSON.parse(JSON.stringify(v));
let errRules = [], retRules = [];
const SCOPES = { account: '整个账号', model: '账号 × 模型', funding: '同一付费来源的所有模型' };
const ACTIONS = { ignore: '忽略（不扣分、不换号）', degrade: '仅降级（扣健康分）', cooldown: '冷却', 'hard-quarantine': '隔离（需人工处理）' };
const RESET_FORMATS = { '': '不读响应头', 'retry-after': 'Retry-After（秒数或日期）', 'unix-seconds': 'Unix 秒时间戳', 'unix-milliseconds': 'Unix 毫秒时间戳', duration: '时长（如 30s）' };
const lines = (t) => String(t || '').split('\n').map((x) => x.trim()).filter(Boolean);
const matchSummary = (w) => {
  const parts = [];
  if ((w.statuses || []).length) parts.push(`状态码 ${w.statuses.join(' / ')}`);
  if ((w.bodyContains || []).length) parts.push(`正文含 ${w.bodyContains.map((x) => '“' + x + '”').join(' 或 ')}`);
  if ((w.bodyCodes || []).length) parts.push(`错误码 ${w.bodyCodes.join(' / ')}（精确匹配）`);
  Object.entries(w.headers || {}).forEach(([k, v]) => parts.push(v ? `响应头 ${k} 含 “${v}”` : `带响应头 ${k}`));
  return parts.length ? parts.map((x) => `<div>${esc(x)}</div>`).join('') : '<span class="muted">（无条件，不会命中）</span>';
};
function errActionHTML(r) {
  const act = { ignore: ['忽略', ''], degrade: ['仅降级', 'warn'], cooldown: ['冷却', 'warn'], 'hard-quarantine': ['隔离', 'bad'] }[r.action] || [r.action, ''];
  let sub = SCOPES[r.scope] || r.scope;
  if (r.action === 'cooldown' && r.reset) sub += `，默认 ${r.reset.fallback}${r.reset.max ? '，最长 ' + r.reset.max : ''}${r.reset.escalate ? '，连续命中翻倍' : ''}`;
  if (r.refreshQuota) sub += '，并刷新额度';
  if (r.downstreamStatus) sub += `，回给下游 ${r.downstreamStatus}`;
  return `<span class="badge ${act[1]}">${esc(act[0])}</span><div class="acc-sub">${esc(sub)}</div>`;
}
function renderRules() {
  const btns = (i, n) => `<button class="btn small" data-r="up" ${i === 0 ? 'disabled' : ''} title="上移">↑</button><button class="btn small" data-r="down" ${i === n - 1 ? 'disabled' : ''} title="下移">↓</button><button class="btn small" data-r="toggle">`;
  $('#rules-error tbody').innerHTML = errRules.map((r, i) => `<tr data-i="${i}" class="${r.disabled ? 'off' : ''}">
    <td class="num">${i + 1}</td><td><b>${esc(r.id)}</b>${r.disabled ? ' <span class="badge">已停用</span>' : ''}<div class="acc-sub">${esc(r.note || '')}</div></td>
    <td>${matchSummary(r.when || {})}</td><td>${errActionHTML(r)}</td>
    <td class="actions">${btns(i, errRules.length)}${r.disabled ? '启用' : '停用'}</button><button class="btn small" data-r="edit">编辑</button><button class="btn small danger" data-r="del">删除</button></td></tr>`).join('');
  $('#rules-retry tbody').innerHTML = retRules.map((r, i) => `<tr data-i="${i}" class="${r.disabled ? 'off' : ''}">
    <td class="num">${i + 1}</td><td><b>${esc(r.id)}</b>${r.disabled ? ' <span class="badge">已停用</span>' : ''}<div class="acc-sub">${esc(r.note || '')}</div></td>
    <td>${matchSummary(r.when || {})}</td>
    <td class="actions">${btns(i, retRules.length)}${r.disabled ? '启用' : '停用'}</button><button class="btn small" data-r="edit">编辑</button><button class="btn small danger" data-r="del">删除</button></td></tr>`).join('');
  $('#rules-error-empty').classList.toggle('hidden', errRules.length > 0);
  $('#rules-retry-empty').classList.toggle('hidden', retRules.length > 0);
}
function bindRuleTable(sel, list, kind) {
  $(sel).addEventListener('click', async (ev) => {
    const b = ev.target.closest('button[data-r]');
    if (!b) return;
    const arr = list();
    const i = Number(b.closest('tr').dataset.i);
    const act = b.dataset.r;
    if (act === 'up' || act === 'down') {
      const j = act === 'up' ? i - 1 : i + 1;
      [arr[i], arr[j]] = [arr[j], arr[i]];
    } else if (act === 'toggle') {
      arr[i].disabled = !arr[i].disabled || undefined;
    } else if (act === 'del') {
      if (await modal({ title: '删除规则', body: `<p>删除规则 <b>${esc(arr[i].id)}</b>？保存设置后生效。</p>`, buttons: [{ label: '取消', value: 'c' }, { label: '删除', value: 'd', danger: true }] }) !== 'd') return;
      arr.splice(i, 1);
    } else if (act === 'edit') {
      await editRule(kind, arr, i);
    }
    renderRules();
  });
}
bindRuleTable('#rules-error', () => errRules, 'error');
bindRuleTable('#rules-retry', () => retRules, 'retry');
$('#btn-add-error-rule').addEventListener('click', async () => { await editRule('error', errRules, -1); renderRules(); });
$('#btn-add-retry-rule').addEventListener('click', async () => { await editRule('retry', retRules, -1); renderRules(); });

async function editRule(kind, arr, idx) {
  const isErr = kind === 'error';
  const r = idx >= 0 ? clone(arr[idx]) : (isErr ? { id: '', scope: 'account', action: 'cooldown', when: {}, reset: { fallback: '30s' } } : { id: '', decision: 'stop', when: {} });
  const w = r.when || {};
  const rs = r.reset || {};
  const opts = (m, v) => Object.entries(m).map(([k, t]) => `<option value="${esc(k)}" ${k === v ? 'selected' : ''}>${esc(t)}</option>`).join('');
  const hdrs = Object.entries(w.headers || {}).map(([k, v]) => v ? `${k}: ${v}` : k).join('\n');
  const body = `<div class="form">
    <label>规则名称</label><input id="r-id" value="${esc(r.id)}" placeholder="如 rate-limited（字母数字 _ . -）">
    <label>备注</label><input id="r-note" value="${esc(r.note || '')}">
    <div class="sect">命中条件（多项同时满足；每项内部任一即可）</div>
    <label>状态码</label><input id="r-st" value="${esc((w.statuses || []).join(', '))}" placeholder="如 429, 503，逗号分隔"><span class="hint">留空表示不限状态码</span>
    <label>正文包含</label><textarea id="r-body" rows="3" placeholder="每行一个关键字，不区分大小写">${esc((w.bodyContains || []).join('\n'))}</textarea><span class="hint">响应正文含其中任意一个即命中</span>
    <label>错误码（精确匹配）</label><textarea id="r-code" rows="2" placeholder="每行一个，如 3800">${esc((w.bodyCodes || []).join('\n'))}</textarea><span class="hint">匹配正文顶层 code / errorCode，和其他条件同时满足；不读取任意正文路径</span>
    <label>响应头</label><textarea id="r-hdr" rows="2" placeholder="每行一个：Header: 子串（子串可省略）">${esc(hdrs)}</textarea>
    ${isErr ? `<div class="sect">处理方式</div>
    <label>作用范围</label><select id="r-scope">${opts(isDeploy() ? { account: SCOPES.account, model: SCOPES.model } : SCOPES, r.scope)}</select>
    <label>动作</label><select id="r-action">${opts(ACTIONS, r.action)}</select>
    <div id="r-reset" class="form" style="grid-column:1/-1;grid-template-columns:120px 1fr">
      <label>默认冷却</label><input id="r-fb" value="${esc(rs.fallback || '')}" placeholder="如 30s、10m、24h"><span class="hint">读不到恢复时间时使用</span>
      <label>最长冷却</label><input id="r-max" value="${esc(rs.max || '')}" placeholder="如 8d，可留空"><span class="hint">支持 s / m / h / d</span>
      <label>读取响应头</label><input id="r-rh" value="${esc(rs.header || '')}" placeholder="如 Retry-After，可留空">
      <label>响应头格式</label><select id="r-rf">${opts(RESET_FORMATS, rs.format || '')}</select>
      <label>正文时间前缀</label><textarea id="r-ba" rows="2" placeholder="每行一个，如 try again after">${esc((rs.bodyAfter || []).join('\n'))}</textarea><span class="hint">正文中这些词后面跟的 RFC3339 时间作为恢复时间</span>
      <span></span><label class="check"><input type="checkbox" id="r-esc" ${rs.escalate ? 'checked' : ''}>连续命中时冷却翻倍（成功一次清零）</label>
    </div>
    <span></span><label class="check"><input type="checkbox" id="r-rq" ${r.refreshQuota ? 'checked' : ''}>${isDeploy() ? '同时刷新模型目录（不是余额）' : '同时立即刷新该账号额度'}</label>
    <label>回给下游状态码</label><input id="r-ds" type="number" value="${r.downstreamStatus || ''}" placeholder="默认"><span class="hint">最后一次失败时返回给下游的状态码（400–599），留空用默认映射</span>` : ''}
    <span></span><label class="check"><input type="checkbox" id="r-off" ${r.disabled ? 'checked' : ''}>停用此规则</label>
  </div>`;
  await modal({ title: (idx >= 0 ? '编辑' : '添加') + (isErr ? '错误规则' : '重试规则'), body, wide: true,
    onOpen: isErr ? () => { const sync = () => { $('#r-reset').style.display = $('#r-action').value === 'cooldown' ? '' : 'none'; }; $('#r-action').addEventListener('change', sync); sync(); } : undefined,
    buttons: [{ label: '取消', value: 'c' }, { label: '确定', value: 'ok', primary: true, action: () => {
      const out = { ...r, id: $('#r-id').value.trim(), note: $('#r-note').value.trim() || undefined, disabled: $('#r-off').checked || undefined };
      if (!out.id) throw new Error('规则名称必填');
      if (arr.some((x, j) => j !== idx && x.id === out.id)) throw new Error('规则名称重复');
      const statuses = $('#r-st').value.split(/[,，\s]+/).filter(Boolean).map(Number);
      if (statuses.some((n) => !Number.isInteger(n) || n < 100 || n > 599)) throw new Error('状态码必须是 100–599 的整数');
      const headers = {};
      lines($('#r-hdr').value).forEach((l) => { const k = l.indexOf(':'); if (k < 0) headers[l] = ''; else headers[l.slice(0, k).trim()] = l.slice(k + 1).trim(); });
      out.when = { statuses: statuses.length ? statuses : undefined, bodyContains: lines($('#r-body').value).length ? lines($('#r-body').value) : undefined, bodyCodes: lines($('#r-code').value).length ? lines($('#r-code').value) : undefined, headers: Object.keys(headers).length ? headers : undefined };
      if (!statuses.length && !out.when.bodyContains && !out.when.bodyCodes && !out.when.headers) throw new Error('至少设置一个命中条件');
      if (isErr) {
        out.scope = $('#r-scope').value; out.action = $('#r-action').value;
        out.refreshQuota = $('#r-rq').checked || undefined;
        const ds = parseInt($('#r-ds').value || '0', 10);
        if (ds && (ds < 400 || ds > 599)) throw new Error('下游状态码必须在 400–599');
        out.downstreamStatus = ds || undefined;
        if (out.action === 'cooldown') {
          if (!$('#r-fb').value.trim()) throw new Error('冷却动作必须填默认冷却时长');
          const rh = $('#r-rh').value.trim(), rf = $('#r-rf').value;
          if (rh && !rf) throw new Error('填了响应头就要选响应头格式');
          out.reset = { header: rh || undefined, format: rf || undefined, bodyAfter: lines($('#r-ba').value).length ? lines($('#r-ba').value) : undefined,
            fallback: $('#r-fb').value.trim(), max: $('#r-max').value.trim() || undefined, escalate: $('#r-esc').checked || undefined };
        } else delete out.reset;
      }
      if (idx >= 0) arr[idx] = out; else arr.push(out);
    } }] });
}

// ---- model prices ----
let priceRows = [];
const PRICE_COLS = [['model', 'text', 22], ['currency', 'cur'], ['input', 'n'], ['cachedInput', 'n'], ['output', 'n'], ['peakMultiplier', 'n'], ['from', 'text', 10],
  ['longThreshold', 'n'], ['longInput', 'n'], ['longCachedInput', 'n'], ['longOutput', 'n'], ['note', 'text', 24]];
function renderPrices() {
  $('#price-table tbody').innerHTML = priceRows.map((e, i) => '<tr>' + PRICE_COLS.map(([k, t, w]) => {
    const v = e[k] === undefined || e[k] === 0 && t === 'n' ? '' : e[k];
    if (t === 'cur') return `<td><select data-i="${i}" data-f="${k}">${['CNY', 'USD'].map((c) => `<option ${c === e[k] ? 'selected' : ''}>${c}</option>`).join('')}</select></td>`;
    return `<td><input data-i="${i}" data-f="${k}" value="${esc(v)}" ${t === 'n' ? 'type="number" step="any" min="0" size="7"' : `size="${w}"`}></td>`;
  }).join('') + `<td><button class="btn ghost small" data-del="${i}">删除</button></td></tr>`).join('');
}
function readPricing() {
  const num0 = (v) => (v === undefined || v === '' ? 0 : Number(v));
  const models = priceRows.map((e) => {
    const o = { model: String(e.model || '').trim(), currency: e.currency || 'USD' };
    for (const k of ['input', 'cachedInput', 'output', 'peakMultiplier', 'longThreshold', 'longInput', 'longCachedInput', 'longOutput']) o[k] = num0(e[k]);
    if (e.from) o.from = String(e.from).trim();
    if (e.note) o.note = e.note;
    return o;
  });
  const holidays = $('#price-holidays').value.split(/[\s,，]+/).filter(Boolean);
  return { models, holidays };
}
$('#price-table').addEventListener('input', (ev) => { const t = ev.target; if (t.dataset.f) priceRows[+t.dataset.i][t.dataset.f] = t.value; });
$('#price-table').addEventListener('click', (ev) => { const b = ev.target.closest('button[data-del]'); if (b) { priceRows.splice(+b.dataset.del, 1); renderPrices(); } });
$('#btn-add-price').addEventListener('click', () => { priceRows.unshift({ model: '', currency: 'USD', input: 0, cachedInput: 0, output: 0 }); renderPrices(); });
$('#btn-default-pricing').addEventListener('click', () => { priceRows = clone(settingsCache.defaults.pricing); renderPrices(); toast('已填入默认价格，保存后生效'); });

async function loadSettings() {
  settingsCache = await api('GET', withPool('/api/settings'));
  const s = settingsCache.settings;
  if (isDeploy()) settingsCache.strategies = settingsCache.strategies.filter((x) => !['smart','pace','least-used'].includes(x) || x === s.routing.strategy);
  $('#deploy-settings').classList.toggle('hidden', !isDeploy());
  $('#deploy-base').value = s.deploy?.baseUrl || 'https://api.deploy.com/api/v1';
  $('#deploy-sync').value = s.deploy?.modelSyncSec ?? 600;
  $('#deploy-detail-log').checked = s.deploy?.detailLog === true;
  $('#deploy-quota-note').textContent = s.deploy?.quotaReason || '余额未知；仅绑定推理 Key，未绑定组织控制台会话。';
  $('#quota-settings-title').textContent = isDeploy() ? '模型目录周期检查' : '额度轮询';
  $('#rules-retry-note').textContent = isDeploy() ? '命中后停止换号，即使错误规则已冷却本号；停用/删除停止规则可允许符合安全条件的换号。未知发送结果或已输出流不能重放。每池独立保存。' : '命中后停止换号；账号冷却/隔离规则优先，已发送或流式输出的安全门不允许重放。错误/重试规则每池独立，上方标签切换';
  $('#mistral-settings').classList.toggle('hidden', !isMistral());
  $('#modelstudio-settings').classList.toggle('hidden', !isModelStudio());
  const ms = s.modelStudio || {};
  $('#ms-region').value = ms.region || 'ap-southeast-1';
  $('#ms-sync').value = ms.modelSyncSec ?? 600;
  $('#ms-detail-log').checked = !!ms.detailLog;
  $('#ms-console-endpoint').value = ms.console?.endpoint || '';
  $('#ms-console-state').textContent = (ms.console?.state === 'not-connected' ? '状态：未接入控制台。' : '状态：' + (ms.console?.state || '未知') + '。') + (ms.console?.reason || '') + (ms.console?.subscriptionPath ? ' 预留接口：' + ms.console.subscriptionPath : '');
  $('#mistral-base').value = s.mistral?.baseUrl || 'https://api.mistral.ai/v1';
  $('#mistral-sync').value = s.mistral?.modelSyncSec ?? 600;
  $('#mistral-detail-log').checked = s.mistral?.detailLog ?? s.logs.detail;
  $('#mistral-binding-capacity').value = s.mistral?.bindingCapacity ?? 0;
  renderForm($('#form-mistral-performance'), MISTRAL_PERFORMANCE_FIELDS, s.mistral?.performance ?? settingsCache.defaults.mistralPerformance, 'mp');
  $('#tokenharbor-settings').classList.toggle('hidden', !isTokenHarbor());
  $('#th-base').value = s.tokenHarbor?.baseUrl || 'https://tokenharbor.ai/v1';
  $('#th-sync').value = s.tokenHarbor?.modelSyncSec ?? 1800;
  $('#th-account-sync').value = s.tokenHarbor?.accountSyncSec ?? 300;
  $('#th-detail-log').checked = s.tokenHarbor?.detailLog === true;
  $('#opencode-settings').classList.toggle('hidden', !isOpenCode());
  renderForm($('#form-opencode'), openCodeFields(), s.opencode || {}, 'oc');
  $('#oc-protocols').value = JSON.stringify((pool === 'opencode-go' ? s.opencode?.goProtocols : s.opencode?.zenProtocols) || {}, null, 2);
  $('#oc-bases').value = JSON.stringify((pool === 'opencode-go' ? s.opencode?.goProtocolBases : s.opencode?.zenProtocolBases) || {}, null, 2);
  renderForm($('#form-routing'), routingFields(), s.routing, 'rt');
  if (pool === 'opencode-go') $('#form-routing').insertAdjacentHTML('beforeend', '<span></span><span class="hint">Go 已绑定会话满载时有界等待后返回 429，不临时借号；最大并发/RPM 在账号编辑中配置。缓存亲和为尽量复用，不保证上游缓存命中。</span>');
  renderForm($('#form-quota'), QUOTA_FIELDS, s.quota, 'qt');
  renderForm($('#form-logs'), logFields(), s.logs, 'lg');
  $$('#form-logs input, #form-logs select').forEach((el) => { el.disabled = isDeploy(); });
  $('#shared-log-note').classList.toggle('hidden', !isDeploy());
  if (isDeploy()) {
    $('#qt-enabled').parentElement.lastChild.textContent = '开启模型目录周期检查';
    $('label[for="qt-intervalSec"]').textContent = '调度检查间隔（秒）';
    $('label[for="qt-staleSec"]').textContent = '目录新鲜度（秒）';
    $('#form-quota .hint').textContent = '本池独立；实际请求同时受上方目录刷新间隔约束，0关闭周期目录读取。手动刷新始终可用；不是余额轮询。';
  }
  $('#rt-strategy').addEventListener('change', (e) => { $('#rt-strategy-hint').textContent = STRATEGY_HINTS[e.target.value] || ''; });
  errRules = clone(s.errorRules || []);
  retRules = clone(s.retryRules || []);
  $('#rules-pool').textContent = POOL_LABEL[pool] + ' 号池';
  renderRules();
  priceRows = clone(s.pricing.models || []);
  $('#price-holidays').value = (s.pricing.holidays || []).join(', ');
  renderPrices();
  $('#set-rev').textContent = '配置版本 ' + s.revision;
  const ov = await api('GET', withPool('/api/overview'));
  $('#pv-group').innerHTML = (ov.groups.length ? ov.groups : [defaultGroup()]).map((g) => `<option>${esc(g)}</option>`).join('');
}
$('#btn-save-settings').addEventListener('click', async () => {
  const s = settingsCache.settings;
  const errorRules = errRules, retryRules = retRules;
  const body = { revision: s.revision, upstream: s.upstream, routing: readForm(routingFields(), 'rt', s.routing), quota: readForm(QUOTA_FIELDS, 'qt', s.quota), logs: readForm(logFields(), 'lg', s.logs), pricing: readPricing(), errorRules, retryRules };
  if (isDeploy()) body.deploy = { baseUrl: $('#deploy-base').value.trim(), modelSyncSec: Number($('#deploy-sync').value), detailLog: $('#deploy-detail-log').checked };
  if (isModelStudio()) body.modelStudio = { region: $('#ms-region').value, modelSyncSec: Number($('#ms-sync').value), detailLog: $('#ms-detail-log').checked, planOverrides: (s.modelStudio && s.modelStudio.planOverrides) || {} };
  if (isMistral()) body.mistral = { baseUrl: $('#mistral-base').value.trim(), modelSyncSec: Number($('#mistral-sync').value), detailLog: $('#mistral-detail-log').checked,
    bindingCapacity: Number($('#mistral-binding-capacity').value), performance: readForm(MISTRAL_PERFORMANCE_FIELDS, 'mp', s.mistral?.performance ?? settingsCache.defaults.mistralPerformance) };
  if (isTokenHarbor()) body.tokenHarbor = { baseUrl: $('#th-base').value.trim(), modelSyncSec: Number($('#th-sync').value), accountSyncSec: Number($('#th-account-sync').value), detailLog: $('#th-detail-log').checked };
  if (isOpenCode()) {
    body.opencode = readForm(openCodeFields(), 'oc', s.opencode || {});
    try {
      body.opencode[pool === 'opencode-go' ? 'goProtocols' : 'zenProtocols'] = JSON.parse($('#oc-protocols').value || '{}');
      body.opencode[pool === 'opencode-go' ? 'goProtocolBases' : 'zenProtocolBases'] = JSON.parse($('#oc-bases').value || '{}');
    }
    catch { toast('模型协议与协议 Base 覆盖必须是 JSON 对象'); return; }
  }
  try { await api('PUT', withPool('/api/settings'), body); toast('已保存'); loadSettings(); } catch (e) { toast(e.message); }
});
$('#btn-default-rules').addEventListener('click', () => {
  errRules = clone(settingsCache.defaults.errorRules);
  retRules = clone(settingsCache.defaults.retryRules);
  renderRules();
  toast('已填入默认规则，保存后生效');
});
$('#btn-preview').addEventListener('click', async () => {
  try {
    const r = await api('POST', '/api/workflow/preview', { group: $('#pv-group').value, model: $('#pv-model').value.trim() });
    const accs = await api('GET', withPool('/api/accounts'));
    const name = (id) => (accs.accounts.find((a) => a.id === id) || { name: id }).name;
    const ex = Object.entries(r.excluded);
    $('#pv-result').innerHTML = `<div><b>候选顺序：</b>${r.order.length ? r.order.map((id, i) => `${i + 1}. ${esc(name(id))}`).join('　') : '无'}</div>` +
      (ex.length ? `<div class="mt"><b>被排除：</b>${ex.map(([id, why]) => `${esc(name(id))}（${esc(statusLabel(why))}）`).join('、')}</div>` : '') +
      '<div class="mt">绑定命中的会话不走此顺序。预览不占用并发、不改计数。</div>';
  } catch (e) { toast(e.message); }
});
$('#btn-reset-counts').addEventListener('click', async () => { try { await api('POST', '/api/workflow/reset-counts'); toast('已清零'); } catch (e) { toast(e.message); } });

// ---- logs ----
const logState = { requests: { cursor: '', items: [] }, errors: { cursor: '', items: [] }, details: { cursor: '', items: [] } };
const LOG_PREFIX = { requests: 'rq', errors: 'er', details: 'dt' };
function accountOptions(sel) {
  const cur = sel.value;
  sel.innerHTML = '<option value="">全部账号</option>' + accountsCache.map((a) => `<option value="${esc(a.id)}" ${a.id === cur ? 'selected' : ''}>${esc(a.name)}</option>`).join('');
}
function statusBadge(st, result) {
  const committedFailure = pool === 'opencode-go' && result === 'failed' && st >= 200 && st < 300;
  const cls = committedFailure ? 'bad' : result === 'success' || (st >= 200 && st < 300) ? 'ok' : result === 'client_cancelled' || st === 499 ? '' : st === 429 ? 'warn' : 'bad';
  return `<span class="badge ${cls}">${esc(st)}</span>`;
}
const BIND = { hit: '命中', miss: '新绑定', provisional: '待确认', invalidated: '失效改派', 'temporary-overflow': '临时溢出', 'not-applicable': '—' };
function cacheRateCell(r) {
  let v = r.cacheRate;
  if (v === undefined && r.inputTokens != null && r.cachedTokens != null) { // logged before cacheRate existed
    const total = r.inputTokens + (r.inboundProtocol === 'anthropic' ? r.cachedTokens : 0);
    v = total > 0 ? Math.min(r.cachedTokens / total, 1) : null;
  }
  if (v === null || v === undefined) return '<span class="acc-sub">—</span>';
  return `<span class="badge ${v >= 0.8 ? 'ok' : v >= 0.3 ? '' : 'warn'}">${(v * 100).toFixed(1)}%</span>`;
}
const CUR = { CNY: '¥', USD: '$' };
function money(v, cur) { return (CUR[cur] || '') + (v >= 1 ? v.toFixed(2) : v >= 0.01 ? v.toFixed(4) : v.toFixed(6)); }
function costSum(b) { return [b.cc ? money(b.cc, 'CNY') : '', b.cu ? money(b.cu, 'USD') : ''].filter(Boolean).join(' + ') || '—'; }
// Only paired, measured cache/prompt samples contribute. These counters can
// be reset independently of the original 24h token usage (b.p/b.k).
function hitRate(b) {
  return b.ckr && b.cp > 0 ? Math.min((b.ck || 0) / b.cp, 1) : null;
}
function accountCacheRateHTML(b) {
  const rate = hitRate(b);
  return `<span title="缓存读取 Token / 同批已测提示 Token；不是命中请求占比。清空后重新累计，最长24小时。">${rate === null ? '—' : pct(rate)}</span><div class="acc-sub">已测缓存 ${num(b.ck || 0)} / 提示 ${num(b.cp || 0)}</div><div class="acc-sub">样本 ${num(b.ckr || 0)} 次</div>`;
}
function costCell(r) { return r.cost === undefined || r.cost === null ? '—' : esc(money(r.cost, r.currency)); }
function upstreamTPSCell(r) {
  const average = typeof r.averageTps === 'number' && Number.isFinite(r.averageTps)
    ? `<span class="acc-sub" title="真实输出 token / useTimeSec；按NewAPI相同的Unix整秒差公式，含本网关排队、首响应等待和重试。两层计时起点不同，不保证数值完全相等。">全程平均 ${num(Math.round(r.averageTps))} tok/s</span>`
    : '<span class="acc-sub" title="旧日志、无真实输出、失败或整秒耗时为0时未知">全程平均 TPS —</span>';
  if (typeof r.upstreamTps !== 'number' || !Number.isFinite(r.upstreamTps)) return average + '<br><span class="acc-sub">上游 TPS —</span>';
  const streaming = r.upstreamTpsBasis === 'post-first-event';
  const basis = streaming ? '流式首事件后平均输出速率' : '非流式上游调用平均速率（含首响应等待）';
  return average + `<br><span class="acc-sub" title="${basis}；仅最后实际上游尝试，使用真实输出用量，非服务端内部解码速度">${streaming ? '首事件后' : '上游均速'} ${num(r.upstreamTps)} tok/s</span>`;
}
function keyCell(r) { return esc(r.clientKeyName || r.clientKeyId || '—'); }
function showMistralCacheAudit(r) {
  const a = r.cacheAudit;
  const supported = a?.version === 1;
  const explanation = '临时诊断，仅记录脱敏HMAC、结构计数与HTTP读取/连接观测，不保存正文。指纹只在同一runId内可比较；不同账号、源配置或并发请求不能直接当作已预热缓存对照。读取完整不等于上游已收到全部字节，须结合写入状态、服务商追踪ID和真实用量。连接标识不是模型缓存节点，也不覆盖其他Key的连接使用。';
  modal({ title:'缓存链路诊断 · ' + r.requestId, wide:true, body:`<p class="acc-sub">${esc(explanation)}</p>${supported ? `<p>采样 ${esc(a.sample)} / ${esc(a.sampleLimit ?? '—')} · 本次进程 ${esc(a.runId)} · 新请求采集截止 ${esc(fmtTime(a.expiresAt))}</p><p class="acc-sub">client → prepared 检查适配；previousClient / previousPrepared / attempts[].previous 对比前序观测；sequenceComplete/toolsComplete=false 或缺失的比较字段表示未知。previousSequenceRetained 表示旧消息JSON字节前缀保留，不保证提供商缓存命中；sequenceReordered/toolsReordered 只识别字节相同元素的纯排列变化。</p><pre>${esc(JSON.stringify(a, null, 2))}</pre>` : '<p class="acc-sub">未采集或诊断格式不受支持；旧日志不能补算完整指纹。</p>'}` });
}
function showClientRequestHeaders(r) {
  const states = { valid:'有效候选（不代表最终选中）', empty:'空值', invalid:'无效值（控制字符或超过512字节）' };
  const candidates = Object.entries(r.clientSessionHeaderStatus || {}).map(([name, state]) => `${name}: ${states[state] || state}`).join('\n');
  const captured = r.clientRequestHeaders != null;
  const explanation = captured ? '这是 NewAPI 等客户端实际到达 Balancer 的请求头，不是最终发给 Mistral 的头。会话头、缓存键按收到的原值展示，仍受长度与总量上限限制；密钥、Cookie 等认证凭证继续隐藏。旧日志中已保存的 [REDACTED] 无法恢复，原值展示仅对新版采集的新请求生效。' : '此历史日志未采集客户端请求头，不能据此判断客户端没有发送。';
  const diagnosis = captured ? (candidates || (r.clientRequestHeadersTruncated ? '未在保留部分中看到支持的会话候选头；请求头已截断，不能判断其余部分。' : '未收到支持的会话候选头；透传开启不代表客户端发送了这些头。')) : '未采集';
  modal({ title:'客户端请求头 · ' + r.requestId, wide:true, body:`<p class="acc-sub">${esc(explanation)}</p><p>最终身份来源：${esc(r.sessionSource || '—')} / ${esc(r.affinityKeyType || '—')}（正文身份或更高优先级头可能先被选中）</p><h4>会话候选头检查</h4><pre>${esc(diagnosis)}</pre><h4>客户端请求头${r.clientRequestHeadersTruncated ? '（已截断）' : ''}</h4><pre>${esc(captured ? JSON.stringify(r.clientRequestHeaders, null, 2) : '未采集')}</pre><p class="acc-sub">仅反映本层收到的内容；无法还原被 NewAPI、Nginx 或其他中间层删除的头。正文 metadata.user_id/user 内 JSON 别名不属于 HTTP 请求头。</p>` });
}
function requestRow(r) {
  const tok = [r.inputTokens, r.outputTokens, r.cachedTokens].map((v) => v === null || v === undefined ? '—' : num(v)).join(' / ');
  return `<tr data-rid="${esc(r.requestId)}"><td class="num">${esc(fmtTime(r.ts))}</td><td><span class="acc-sub">${esc(r.protocol || r.inboundProtocol || '—')}</span>${r.clientFamily ? `<div class="acc-sub">${esc(r.clientFamily)}</div>` : ''}</td><td>${esc(r.model)}${r.stream ? ' <span class="badge">流</span>' : ''}</td>
    <td>${keyCell(r)}</td>
    <td>${esc(r.accountName || '—')}${r.switched ? ' <span class="badge warn">换号</span>' : ''}${r.overflow ? ' <span class="badge warn">溢出</span>' : ''}</td>
    <td>${statusBadge(r.status, r.result)}${r.upstreamStatus && r.upstreamStatus !== r.status ? `<div class="acc-sub">上游 ${esc(r.upstreamStatus)}</div>` : ''}${r.errorCategory ? `<div class="acc-sub">${esc(r.errorCategory)}${r.blockedBy ? '/' + esc(r.blockedBy) : ''}</div>` : ''}</td>
    <td><span class="acc-sub">${esc(BIND[r.bindingResult] || r.bindingResult)}</span><div class="acc-sub">${esc(r.affinityKeyType)}</div>${r.provider === 'mistral' ? `<div class="acc-sub">来源 ${esc(r.sessionSource || '—')}</div>` : ''}${r.cacheAffinityResult ? `<div class="acc-sub">缓存亲和 ${esc(GO_CACHE_LABEL[r.cacheAffinityResult] || r.cacheAffinityResult)} · ${esc(r.cacheHintSource)}</div>` : ''}</td>
    <td class="num">${r.ttftMs === null || r.ttftMs === undefined ? '—' : num(r.ttftMs) + 'ms'}</td><td class="num">${num(r.durationMs)}ms<div>${upstreamTPSCell(r)}</div><div class="acc-sub">${costCell(r)}</div></td>
    <td class="num">${tok}<div>${cacheRateCell(r)}</div>${r.cacheCreationTokens != null ? `<div class="acc-sub">缓存写入 ${num(r.cacheCreationTokens)}</div>` : ''}${r.egressStatus ? `<div class="acc-sub">${esc(r.proxy)}${r.egressIp ? ' / ' + esc(r.egressIp) : ''} · ${esc(GO_EGRESS_LABEL[r.egressStatus] || r.egressStatus)}</div>` : ''}</td><td class="num">${r.attempts ? r.attempts.length : 0}</td>
    <td class="act">${r.provider === 'mistral' && r.cacheAudit ? `<button type="button" class="btn small ghost rq-audit" data-rid="${esc(r.requestId)}">缓存诊断</button>` : ''}${r.provider === 'mistral' ? `<button type="button" class="btn small ghost rq-headers" data-rid="${esc(r.requestId)}">客户端头</button>` : ''}<button type="button" class="btn small ghost rq-del" data-rid="${esc(r.requestId)}" title="删除这条日志">删除</button></td></tr>`;
}
function detailRow(d) {
  const sz = (n, cut) => num(n) + ' B' + (cut ? ' <span class="badge warn">截断</span>' : '');
  return `<tr><td class="num">${esc(fmtTime(d.ts))}</td><td><span class="acc-sub">${esc(d.protocol)}</span></td><td>${esc(d.model)}</td><td>${keyCell(d)}</td><td>${statusBadge(d.status, d.result)}${d.context === 'failed_attempt' ? '<div class="acc-sub">失败尝试 #' + esc(d.attemptIndex) + '</div>' : ''}</td>
    <td class="num">${sz(d.requestBodyBytes, d.requestBodyTruncated)}</td><td class="num">${sz(d.responseBodyBytes, d.responseBodyTruncated)}</td><td class="small">${esc(d.requestId)}</td></tr>`;
}
function showDetail(d) {
  const pretty = (t) => { try { return JSON.stringify(JSON.parse(t), null, 2); } catch { return t; } };
  const sec = (title, text) => `<h4>${esc(title)}</h4><pre>${esc(text)}</pre>`;
  const block = (name, m) => sec(name + '头', JSON.stringify(m.headers || {}, null, 2)) + sec(name + '体（' + num(m.bodyBytes || 0) + ' B' + (m.bodyTruncated ? '，已截断/未完整读取' : '') + '）', pretty(m.body || ''));
  const legacyFailure = d.context === 'failed_attempt';
  let body = block(legacyFailure ? '失败尝试请求（旧格式）' : '客户端原始请求', { headers:d.requestHeaders, body:d.requestBody, bodyBytes:d.requestBodyBytes, bodyTruncated:d.requestBodyTruncated });
  if (d.wireVersion === 1) {
    for (const a of d.upstreamAttempts || []) {
      body += `<h3>上游尝试 #${esc(Number(a.attemptIndex) + 1)} · ${esc(a.method)} ${esc(a.path)} · HTTP ${esc(a.status || '未收到响应')}</h3><p class="acc-sub">${a.sent ? '已发出请求头' : '未确认发出请求头'} · ${esc(a.terminal || '')}</p>`;
      body += block('实际发给上游的请求', a.request || {});
      body += a.response ? block('上游原生响应（适配前）', a.response) : '<p class="acc-sub">没有收到上游HTTP响应，不伪造响应头/体。</p>';
    }
    if (!d.upstreamAttempts?.length) body += '<p class="acc-sub">本次未发起上游HTTP请求。</p>';
  } else if (d.provider === 'mistral') body += '<p class="acc-sub">旧记录未采集上游原生交换，不从客户端内容推造。</p>';
  body += block(legacyFailure ? '上游错误响应（旧格式）' : '返回客户端的响应（适配后）', { headers:d.responseHeaders, body:d.responseBody, bodyBytes:d.responseBodyBytes, bodyTruncated:d.responseBodyTruncated });
  modal({ title: `${legacyFailure ? '失败尝试 #' + d.attemptIndex + ' · ' : ''}${d.method} ${d.path} → ${d.status}${d.clientKeyName || d.clientKeyId ? '（密钥 ' + (d.clientKeyName || d.clientKeyId) + '）' : ''}`, wide:true, body });
}
function errorRow(e) {
  return `<tr><td class="num">${esc(fmtTime(e.ts))}</td><td>${keyCell(e)}</td><td>${esc(e.accountName)}</td><td>${esc(e.model)}</td>
    <td>${statusBadge(e.status)}${e.upstreamStatus && e.upstreamStatus !== e.status ? `<div class="acc-sub">上游 ${esc(e.upstreamStatus)}</div>` : ''}</td>
    <td>${esc(e.category)}<div class="acc-sub">${esc(e.terminal)}${e.origin ? ' · ' + esc(e.origin) : ''}</div>${e.egress ? `<div class="acc-sub">${esc(e.egress)}${e.egressIp ? ' / ' + esc(e.egressIp) : ''}</div>` : ''}</td>
    <td>${e.ruleId ? `${esc(e.ruleId)}<div class="acc-sub">${esc(e.ruleAction)}${e.switched ? ' · 换号' : ''}</div>` : `<span class="muted">${esc(e.matchedBy)}</span>`}${e.retryRuleId ? `<div class="acc-sub">停止重试：${esc(e.retryRuleId)}</div>` : ''}</td>
    <td class="small">${esc(String(e.reason).slice(0, 160))}${e.provider === 'tokenharbor' || e.provider === 'mistral' ? `<div><button type="button" class="btn small er-detail" data-rid="${esc(e.requestId)}" data-attempt="${esc(e.attemptIndex ?? 0)}">详细请求</button></div>` : ''}</td></tr>`;
}
async function openErrorDetails(requestId, attemptIndex) {
  const d = await api('GET', withPool('/api/logs/details?requestId=' + encodeURIComponent(requestId) + (pool === 'mistral' ? '' : '&attemptIndex=' + encodeURIComponent(attemptIndex))));
  if (d.items?.length) showDetail(d.items.find(x => x.context === 'failed_attempt') || d.items[0]);
  else toast('没有该次尝试的详细记录：旧版未采集、记录已过期或尚未生成。');
}
// deleteRequestLog removes one record and re-renders the page from the start.
async function deleteRequestLog(requestId) {
  if (!requestId) return;
  if (await modal({ title: '删除日志', body: `<p>确定删除请求 <code>${esc(requestId)}</code> 的日志？只删这一条，同一次的详细日志不受影响。</p>`, buttons: [{ label: '取消', value: 'c' }, { label: '删除', value: 'x', danger: true }] }) !== 'x') return;
  await api('DELETE', '/api/logs/requests/log/' + encodeURIComponent(requestId));
  toast('已删除该条日志');
  await loadLogs('requests', true);
}

async function loadLogs(kind, reset) {
  $('#rq-client').classList.toggle('hidden', pool !== 'opencode-go');
  $('#er-origin').classList.toggle('hidden', pool !== 'opencode-go');
  $('#rq-first-label').textContent = pool === 'opencode-go' ? '首事件' : '首字';
  $('#rq-cost-label').textContent = pool === 'opencode-go' ? '耗时 / 计价估算' : '耗时 / 费用';
  if (!accountsCache.length) { try { accountsCache = (await api('GET', withPool('/api/accounts'))).accounts; } catch { /* ignore */ } }
  const p = LOG_PREFIX[kind];
  if (kind !== 'details') accountOptions($(`#${p}-account`));
  const st = logState[kind];
  if (reset) { st.cursor = ''; st.items = []; }
  const q = new URLSearchParams({ limit: '50', provider: pool });
  if (st.cursor) q.set('cursor', st.cursor);
  const add = (k, v) => { if (v) q.set(k, v); };
  add('accountId', $(`#${p}-account`).value);
  add('status', $(`#${p}-status`).value.trim());
  if (kind === 'requests') { add('result', $('#rq-result').value); add('protocol', $('#rq-protocol').value);
    const af = $('#rq-affinity').value;
    if (af.startsWith('c:')) add('affinityConfidence', af.slice(2)); else add('affinityKeyType', af);
    if (pool === 'opencode-go') add('clientFamily', $('#rq-client').value);
    add('model', $('#rq-model').value.trim()); } else if (kind === 'details') add('requestId', $('#dt-rid').value.trim()); else { add('ruleId', $('#er-rule').value.trim()); add('origin', pool === 'opencode-go' ? $('#er-origin').value : ''); }
  const d = await api('GET', `/api/logs/${kind}?${q}`);
  st.items = st.items.concat(d.items);
  st.cursor = d.nextCursor || '';
  $(`#${p}-table tbody`).innerHTML = st.items.map({ requests: requestRow, errors: errorRow, details: detailRow }[kind]).join('') || `<tr><td colspan="9" class="empty">暂无记录</td></tr>`;
  $(`#${p}-more`).classList.toggle('hidden', !st.cursor);
}
for (const [kind, p] of Object.entries(LOG_PREFIX)) {
  $(`#btn-${p}-search`).addEventListener('click', () => loadLogs(kind, true).catch((e) => toast(e.message)));
  $(`#${p}-more`).addEventListener('click', () => loadLogs(kind, false).catch((e) => toast(e.message)));
  $(`#btn-${p}-clear`).addEventListener('click', async () => {
    if (await modal({ title: '清空日志', body: `<p>确定清空全部${{ requests: '请求', errors: '错误', details: '详细' }[kind]}日志？</p>`, buttons: [{ label: '取消', value: 'c' }, { label: '清空', value: 'x', danger: true }] }) !== 'x') return;
    try { await api('DELETE', `/api/logs/${kind}`); loadLogs(kind, true); } catch (e) { toast(e.message); }
  });
  $(`#${p}-table tbody`).addEventListener('click', (ev) => {
    const audit = ev.target.closest('.rq-audit');
    if (audit) { const item = logState.requests.items.find(r => r.requestId === audit.dataset.rid); if (item) showMistralCacheAudit(item); return; }
    const headers = ev.target.closest('.rq-headers');
    if (headers) { const item = logState.requests.items.find(r => r.requestId === headers.dataset.rid); if (item) showClientRequestHeaders(item); return; }
    const detail = ev.target.closest('.er-detail');
    if (detail) { openErrorDetails(detail.dataset.rid, detail.dataset.attempt).catch((e) => toast(e.message)); return; }
    const del = ev.target.closest('.rq-del');
    if (del) { deleteRequestLog(del.dataset.rid).catch((e) => toast(e.message)); return; }
    const tr = ev.target.closest('tr');
    if (!tr || !tr.parentElement) return;
    const idx = Array.from(tr.parentElement.children).indexOf(tr);
    const item = logState[kind].items[idx];
    if (item && kind === 'details') showDetail(item);
    else if (item) showJSON(kind === 'requests' ? '请求 ' + item.requestId : '错误 ' + item.requestId + ' #' + item.attemptIndex, item);
  });
}

boot();


// ===================================================================
// Mistral 注册机
// ===================================================================
let mistralPollTimer = null;

const MR_STATUS_LABEL = {
  queued: '排队中',
  running: '注册中',
  registered: '已注册',
  ready: '就绪',
  failed: '失败',
  cancelled: '已取消',
};

function stopMistralPolling() {
  if (mistralPollTimer) { clearInterval(mistralPollTimer); mistralPollTimer = null; }
}

function mistralKeySummary(keys) {
  if (!keys) return '<span class="muted">—</span>';
  const rows = [];
  for (const [label, v] of [['API', keys.api], ['Vibe', keys.vibe],
                            ['Studio', keys.studio], ['CLI', keys.vibe_cli]]) {
    if (!v) continue;
    rows.push(`<div class="mr-key"><span class="mr-key-label">${label}</span>` +
      `<code class="mr-key-val" title="点击复制" data-mr-copy="${escapeHtml(v)}">${escapeHtml(v)}</code></div>`);
  }
  return rows.length ? rows.join('') : '<span class="muted">—</span>';
}

async function loadMistralRegistrations() {
  if (!isMistral() || currentPage !== 'mregister') return;
  await loadMistralStatus();
  await loadMistralSettings();
  await loadMistralCandidates();
  if (!isMistral() || currentPage !== 'mregister') return;
  stopMistralPolling();
  mistralPollTimer = setInterval(() => {
    loadMistralCandidates().catch(() => {});
  }, 5000);
}

async function loadMistralStatus() {
  const el = document.getElementById('mr-worker-text');
  if (!el) return;
  try {
    const d = await api('GET', '/api/mistral/status');
    el.textContent = d.worker_found
      ? `运行环境就绪 · Python ${d.python} · 共 ${d.total} 个候选`
      : `未找到 worker（${d.worker_path}），注册会失败`;
    el.className = d.worker_found ? 'muted' : 'error';
  } catch (e) {
    el.textContent = '状态查询失败：' + e.message;
    el.className = 'error';
  }
}

async function loadMistralCandidates() {
  const tbody = document.getElementById('mr-rows');
  const errEl = document.getElementById('mr-poll-error');
  if (!tbody) return;
  try {
    const d = await api('GET', '/api/mistral/candidates');
    const rows = d.candidates || [];
    if (!rows.length) {
      tbody.innerHTML = '<tr><td colspan="4" class="muted">暂无任务。</td></tr>';
      if (errEl) errEl.textContent = '';
      return;
    }
    tbody.innerHTML = rows.map((c) => {
      const label = MR_STATUS_LABEL[c.status] || c.status;
      const busy = c.status === 'running' || c.status === 'queued';
      const act = busy
        ? `<button type="button" class="btn small" data-mr-cancel="${c.id}">取消</button>`
        : `<button type="button" class="btn small primary" data-mr-run="${c.id}">开始</button>`;
      const err = c.error ? `<div class="error">${escapeHtml(c.error)}</div>` : '';
      return `<tr>
        <td>${escapeHtml(c.email)}${err}</td>
        <td><span class="mr-status mr-${c.status}">${label}</span></td>
        <td class="mr-keys">${mistralKeySummary(c.keys)}</td>
        <td>${c.imported_account_id
          ? '<span class="mr-status mr-ready">已入池</span>'
          : (c.import_to_pool ? '<span class="muted">待入池</span>' : '<span class="muted">—</span>')}</td>
        <td class="actions">
          ${act}
          <button type="button" class="btn small ghost" data-mr-del="${c.id}">删除</button>
        </td>
      </tr>`;
    }).join('');
    if (errEl) errEl.textContent = '';
  } catch (e) {
    if (errEl) errEl.textContent = '刷新失败：' + e.message;
  }
}

function escapeHtml(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g, (ch) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]
  ));
}

document.addEventListener('DOMContentLoaded', () => {
  const form = document.getElementById('mr-form');
  if (form) {
    form.addEventListener('submit', async (ev) => {
      ev.preventDefault();
      const errEl = document.getElementById('mr-form-error');
      errEl.textContent = '';
      const raw = document.getElementById('mr-accounts').value.trim();
      if (!raw) { errEl.textContent = '请填写账号列表'; return; }
      const body = {
        accounts: raw,
        region: document.getElementById('mr-region').value.trim() || 'CA',
        skip_register: document.getElementById('mr-skip-register').checked,
        import_to_pool: document.getElementById('mr-import-pool').checked,
      };
      const resEl = document.getElementById('mr-bulk-result');
      try {
        const d = await api('POST', '/api/mistral/candidates/bulk', body);
        const rej = d.rejected || [];
        resEl.textContent = `已添加 ${d.count} 个` +
          (rej.length ? `，跳过 ${rej.length} 个（${rej.slice(0, 3).map((x) => '第' + x.line + '行:' + x.error).join('；')}${rej.length > 3 ? '…' : ''}）` : '');
        resEl.className = rej.length ? 'error' : 'hint';
        if (d.count) {
          document.getElementById('mr-accounts').value = '';
          toast('已添加 ' + d.count + ' 个任务');
        }
        loadMistralCandidates();
      } catch (e) {
        errEl.textContent = e.message;
      }
    });
  }

  const refresh = document.getElementById('mr-refresh');
  if (refresh) refresh.addEventListener('click', () => loadMistralRegistrations());

  document.addEventListener('click', async (ev) => {
    const t = ev.target;
    if (!(t instanceof HTMLElement)) return;

    if (t.dataset.mrRun) {
      ev.preventDefault();
      try {
        await api('POST', '/api/mistral/candidates/' + t.dataset.mrRun + '/register', {});
        toast('已开始注册');
        loadMistralCandidates();
      } catch (e) { toast(e.message); }
    } else if (t.dataset.mrCancel) {
      ev.preventDefault();
      try {
        await api('POST', '/api/mistral/candidates/' + t.dataset.mrCancel + '/cancel', {});
        toast('已请求取消');
        loadMistralCandidates();
      } catch (e) { toast(e.message); }
    } else if (t.dataset.mrCopy) {
      ev.preventDefault();
      try {
        await navigator.clipboard.writeText(t.dataset.mrCopy);
        toast('已复制');
      } catch { toast('复制失败'); }
    } else if (t.dataset.mrDel) {
      ev.preventDefault();
      if (!confirm('删除这个候选？')) return;
      try {
        await api('DELETE', '/api/mistral/candidates/' + t.dataset.mrDel);
        toast('已删除');
        loadMistralCandidates();
      } catch (e) { toast(e.message); }
    }
  });
});


// ---------------------------------------------------------------- Mistral 设置

async function loadMistralSettings() {
  const form = document.getElementById('mr-settings-form');
  if (!form) return;
  try {
    const d = await api('GET', '/api/mistral/settings');
    const st = d.settings || {};
    document.getElementById('ms-proxy-urls').value = (st.proxyUrls || []).join('\n');
    document.getElementById('ms-mailbox-proxy').value = st.mailboxProxy || '';
    const cardBox = document.getElementById('ms-card-lines');
    if (cardBox) cardBox.value = (st.cardLines || []).join('\n');
    renderCardStatus(d);
    const msg = document.getElementById('mr-settings-msg');
    if (msg) {
      msg.textContent = d.hasProxy
        ? `已配置 ${(st.proxyUrls || []).length} 个代理`
        : '代理未配置，注册会失败';
      msg.className = d.hasProxy ? 'muted' : 'error';
    }
  } catch (e) {
    const msg = document.getElementById('mr-settings-msg');
    if (msg) { msg.textContent = '读取设置失败：' + e.message; msg.className = 'error'; }
  }
}

// renderCardStatus 把银行卡解析结果显示在列表下方。
// d 为 /api/mistral/settings 的响应（含 cardCount / cardErrors）。
function renderCardStatus(d) {
  const out = document.getElementById('ms-card-msg');
  if (!out) return;
  const count = d && typeof d.cardCount === 'number' ? d.cardCount : 0;
  const errs = (d && d.cardErrors) || [];

  if (!count && !errs.length) {
    out.textContent = '银行卡未配置，订阅步骤会跳过';
    out.className = 'muted small';
    return;
  }
  if (!errs.length) {
    out.textContent = `已识别 ${count} 张卡，全部通过校验`;
    out.className = 'muted small';
    return;
  }
  // 有错误：只显示行号与原因，不回显卡号明文
  const detail = errs
    .map((e) => `第 ${e.line} 行：${e.reason}`)
    .slice(0, 5)
    .join('；');
  const more = errs.length > 5 ? ` 等 ${errs.length} 项` : '';
  out.textContent = `已识别 ${count} 张卡，${errs.length} 行无效 —— ${detail}${more}`;
  out.className = 'error small';
}

function parseSids(raw) {
  return raw.split(/[\n,]/).map((x) => x.trim()).filter(Boolean);
}

document.addEventListener('DOMContentLoaded', () => {
  const form = document.getElementById('mr-settings-form');
  if (form) {
    form.addEventListener('submit', async (ev) => {
      ev.preventDefault();
      const msg = document.getElementById('mr-settings-msg');
      msg.textContent = '保存中…';
      msg.className = 'muted';
      try {
        const d = await api('PUT', '/api/mistral/settings', {
          settings: {
            proxyUrls: parseSids(document.getElementById('ms-proxy-urls').value),
            cardLines: document.getElementById('ms-card-lines').value
              .split('\n').map((x) => x.trim()).filter(Boolean),
            mailboxProxy: document.getElementById('ms-mailbox-proxy').value.trim(),
            workerPath: '',
            workerPython: '',
            concurrency: 1,
          },
        });
        renderCardStatus(d);
        const cardErrs = (d.cardErrors || []).length;
        if (cardErrs) {
          msg.textContent = `已保存，但有 ${cardErrs} 行银行卡无效`;
          msg.className = 'error';
        } else {
          msg.textContent = d.hasProxy ? '已保存，代理已配置' : '已保存，但代理还不完整';
          msg.className = d.hasProxy ? 'muted' : 'error';
        }
        toast('设置已保存');
      } catch (e) {
        msg.textContent = '保存失败：' + e.message;
        msg.className = 'error';
      }
    });
  }

  const test = document.getElementById('mr-proxy-test');
  if (test) {
    test.addEventListener('click', async () => {
      const out = document.getElementById('mr-proxy-result');
      out.textContent = '测试中…';
      out.className = 'muted';
      try {
        const d = await api('POST', '/api/mistral/settings/test', {});
        if (d.ok) {
          out.textContent = '出口 ' + (d.response || '') + ' (' + d.sid + ')';
          out.className = 'muted';
        } else {
          out.textContent = '失败：' + (d.error || '') + ' (' + (d.sid || '-') + ')';
          out.className = 'error';
        }
      } catch (e) {
        out.textContent = '测试失败：' + e.message;
        out.className = 'error';
      }
    });
  }
});
