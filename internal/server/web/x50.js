// 50X AI (ai.50x.energy) 注册机前端。
// 由 app.js 在 50X AI 号池下加载；与 deploy.js / tokenharbor.js 同构。

let x50PollTimer = null;
let x50Rows = [];
let x50Settings = null;

// ---------------------------------------------------------------- helpers

async function x50Api(path, opts = {}) {
  const body = opts.body === undefined ? undefined : JSON.parse(opts.body);
  return api(opts.method || 'GET', '/api/x50' + path, body);
}

const X50_STATUS_CLASS = {
  queued: 'muted',
  running: 'warn',
  waiting: 'warn',
  verified: 'warn',
  keyed: 'ok',
  failed: 'bad',
  cancelled: 'muted',
};

function x50Badge(status, text) {
  const cls = X50_STATUS_CLASS[status] || 'muted';
  return `<span class="badge ${cls}">${x50Esc(text || status || '—')}</span>`;
}

function x50Esc(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g, (c) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));
}

// ---------------------------------------------------------------- 注册机页

async function loadX50Registrations() {
  if (!isX50()) return;
  const errEl = document.getElementById('x50-poll-error');
  if (errEl) errEl.textContent = '';
  try {
    const [list, logs] = await Promise.all([x50Api('/candidates'), x50Api('/logs')]);
    x50Rows = list.rows || [];
    x50Settings = list.settings || null;
    renderX50Rows(x50Rows);
    renderX50Status(list.status || {});
    renderX50Logs(logs.logs || []);
    // 代理池只在用户没编辑过时回填，避免覆盖正在输入的内容
    const px = document.getElementById('x50-proxies');
    if (px && document.activeElement !== px && !px.dataset.dirty) {
      px.value = '';
      px.placeholder = x50Settings && x50Settings.proxyCount
        ? `已保存 ${x50Settings.proxyCount} 个代理（凭据不回显）。粘贴新列表可整体替换。`
        : 'socks5://user:pass@host:port';
    }
  } catch (e) {
    if (errEl) errEl.textContent = e.message;
  }
}

function renderX50Rows(rows) {
  const body = document.getElementById('x50-rows');
  const cnt = document.getElementById('x50-count');
  if (cnt) cnt.textContent = String(rows.length);
  if (!body) return;
  if (!rows.length) {
    body.innerHTML = '<tr><td colspan="7" class="muted">还没有账号。在上方粘贴 <code>邮箱----接码地址</code> 后点「导入」。</td></tr>';
    return;
  }
  body.innerHTML = rows.map((r) => {
    const progress = r.status === 'running' && r.stage
      ? `<span class="muted small">${x50Esc(r.stage)}</span>`
      : r.attempts > 1 ? `<span class="muted small">第 ${r.attempts} 次尝试</span>` : '<span class="muted">—</span>';
    const proxy = r.proxyBound
      ? `<span class="small" title="${x50Esc(r.proxyLabel)}">${x50Esc(r.proxyLabel.split('@').pop())}</span>`
      : '<span class="muted">直连</span>';
    const key = r.hasKey
      ? `<code class="small">${x50Esc(r.keyPrefix)}…</code><br><span class="muted small">${x50Esc(r.balanceUsd || '')} USD</span>`
      : (r.hasSession ? '<span class="muted small">有会话 · 无 Key</span>' : '<span class="muted">—</span>');
    const note = r.error
      ? `<span class="error small">${x50Esc(r.error)}</span>`
      : (r.hasMailbox ? '' : '<span class="error small">缺少接码地址</span>');
    const canRun = r.status === 'queued' || r.status === 'failed' || r.status === 'cancelled';
    const busy = r.status === 'running' || r.status === 'waiting';
    return `<tr>
      <td class="small">${x50Esc(r.email)}</td>
      <td>${x50Badge(r.status, r.statusText)}</td>
      <td>${progress}</td>
      <td>${proxy}</td>
      <td>${key}</td>
      <td>${note}</td>
      <td class="row-actions">
        ${canRun ? `<button type="button" class="btn small" data-x50-run="${x50Esc(r.id)}">${r.status === 'queued' ? '注册' : '重试'}</button>` : ''}
        ${r.hasKey ? `<button type="button" class="btn small" data-x50-key="${x50Esc(r.id)}">复制 Key</button>` : ''}
        ${busy ? `<button type="button" class="btn small" data-x50-del="${x50Esc(r.id)}">取消并删除</button>` : `<button type="button" class="btn small ghost" data-x50-del="${x50Esc(r.id)}">删除</button>`}
      </td>
    </tr>`;
  }).join('');
}

function renderX50Status(s) {
  const el = document.getElementById('x50-status');
  if (!el) return;
  const items = [
    ['总数', s.total, ''],
    ['待注册', s.queued, s.queued ? 'warn' : ''],
    ['注册中', s.running, s.running ? 'warn' : ''],
    ['等邮件', s.waiting, s.waiting ? 'warn' : ''],
    ['已完成', s.keyed, s.keyed ? 'ok' : ''],
    ['失败', s.failed, s.failed ? 'bad' : ''],
    ['有代理', `${s.withProxy || 0}/${s.total || 0}`, ''],
    ['worker', s.busy ? `运行中 ${s.busy}` : '空闲', s.busy ? 'warn' : ''],
  ];
  el.innerHTML = items
    .map(([k, v, cls]) => `<span class="badge ${cls}">${x50Esc(k)} ${x50Esc(v == null ? 0 : v)}</span>`)
    .join('');
}

function renderX50Logs(logs) {
  const body = document.querySelector('#x50-log-table tbody');
  if (!body) return;
  body.innerHTML = logs.length
    ? logs.map((l) => `<tr>
        <td class="small">${new Date(l.at).toLocaleTimeString()}</td>
        <td class="small">${x50Esc(l.type || '')}</td>
        <td class="small">${x50Esc(l.email || '')}</td>
        <td class="small">${x50Esc(l.message || '')}</td>
      </tr>`).join('')
    : '<tr><td colspan="4" class="muted">暂无日志</td></tr>';
}

function startX50Polling() {
  stopX50Polling();
  x50PollTimer = setInterval(() => {
    if (document.hidden) return;
    if (!isX50() || currentPage !== 'x50reg') { stopX50Polling(); return; }
    loadX50Registrations().catch(() => {});
  }, 3000);
}

function stopX50Polling() {
  if (x50PollTimer) { clearInterval(x50PollTimer); x50PollTimer = null; }
}

// ---------------------------------------------------------------- 设置页

async function loadX50Settings() {
  if (!isX50()) return;
  const errEl = document.getElementById('x50s-error');
  if (errEl) errEl.textContent = '';
  try {
    const { settings } = await x50Api('/settings');
    x50Settings = settings;
    const set = (id, v) => { const el = document.getElementById(id); if (el != null) el.value = v; };
    set('x50s-concurrency', settings.concurrency ?? 2);
    set('x50s-mail-timeout', settings.mailTimeoutSec ?? 240);
    set('x50s-mail-poll', settings.mailPollSec ?? 3);
    set('x50s-request-timeout', settings.requestTimeoutSec ?? 30);
    set('x50s-key-name', settings.keyName || 'registrar');
    set('x50s-api-base', settings.apiBase || '');
    set('x50s-mail-base', settings.mailBase || '');
    set('x50s-password-mode', settings.passwordMode || 'random');
    const auto = document.getElementById('x50s-auto-bind');
    if (auto) auto.checked = settings.autoBindProxy !== false;
    // 代理凭据不回显：只提示已保存数量
    const px = document.getElementById('x50s-proxies');
    if (px && document.activeElement !== px) {
      px.value = '';
      px.placeholder = settings.proxyCount
        ? `已保存 ${settings.proxyCount} 个代理（凭据不回显）。粘贴新列表可整体替换，留空保存则保持不变。`
        : 'socks5://user:pass@host:port';
    }
  } catch (e) {
    if (errEl) errEl.textContent = e.message;
  }
}

function x50CollectSettings(includeProxies) {
  const val = (id) => { const el = document.getElementById(id); return el ? el.value : ''; };
  const out = {
    concurrency: Number(val('x50s-concurrency')) || 2,
    mailTimeoutSec: Number(val('x50s-mail-timeout')) || 240,
    mailPollSec: Number(val('x50s-mail-poll')) || 3,
    requestTimeoutSec: Number(val('x50s-request-timeout')) || 30,
    keyName: val('x50s-key-name').trim() || 'registrar',
    apiBase: val('x50s-api-base').trim(),
    mailBase: val('x50s-mail-base').trim(),
    passwordMode: val('x50s-password-mode') || 'random',
    autoBindProxy: document.getElementById('x50s-auto-bind')?.checked !== false,
  };
  if (includeProxies) {
    const el = document.getElementById('x50s-proxies');
    const text = el ? el.value.trim() : '';
    // 留空表示沿用已保存的代理，避免保存设置时清空凭据
    if (text) out.proxies = text.split('\n').map((s) => s.trim()).filter(Boolean);
  }
  return out;
}

// ---------------------------------------------------------------- 绑定事件

function initX50Page() {
  if (window.__x50Init) return;
  window.__x50Init = true;

  // 导入
  document.getElementById('x50-form')?.addEventListener('submit', async (ev) => {
    ev.preventDefault();
    const errEl = document.getElementById('x50-form-error');
    const resEl = document.getElementById('x50-bulk-result');
    errEl.textContent = '';
    resEl.textContent = '';
    const text = document.getElementById('x50-accounts').value;
    if (!text.trim()) { errEl.textContent = '请先粘贴账号'; return; }
    const pxEl = document.getElementById('x50-proxies');
    const payload = { text };
    // 只有用户真的输入了才替换，避免空 textarea 清空已保存代理
    if (pxEl && pxEl.value.trim()) {
      payload.proxies = pxEl.value.split('\n').map((s) => s.trim()).filter(Boolean);
    }
    try {
      const r = await x50Api('/candidates/bulk', { method: 'POST', body: JSON.stringify(payload) });
      resEl.textContent = `导入 ${r.added} 个，跳过 ${r.skipped} 个`
        + (r.errors && r.errors.length ? `；${r.errors.slice(0, 3).join('; ')}` : '');
      document.getElementById('x50-accounts').value = '';
      if (pxEl && payload.proxies) { pxEl.value = ''; delete pxEl.dataset.dirty; }
      await loadX50Registrations();
    } catch (e) {
      errEl.textContent = e.message;
    }
  });

  // 保存代理池（号池页）
  document.getElementById('x50-save-proxies')?.addEventListener('click', async () => {
    const errEl = document.getElementById('x50-form-error');
    const resEl = document.getElementById('x50-bulk-result');
    errEl.textContent = '';
    const pxEl = document.getElementById('x50-proxies');
    const text = pxEl ? pxEl.value.trim() : '';
    if (!text) { errEl.textContent = '代理池为空：不保存，避免误清空'; return; }
    try {
      const payload = x50CollectSettings(true);
      payload.proxies = text.split('\n').map((s) => s.trim()).filter(Boolean);
      await x50Api('/settings', { method: 'PUT', body: JSON.stringify(payload) });
      resEl.textContent = `已保存 ${payload.proxies.length} 个代理`;
      pxEl.value = ''; delete pxEl.dataset.dirty;
      await loadX50Registrations();
    } catch (e) {
      errEl.textContent = e.message;
    }
  });

  const pxEl = document.getElementById('x50-proxies');
  pxEl?.addEventListener('input', () => { pxEl.dataset.dirty = '1'; });

  document.getElementById('x50-refresh')?.addEventListener('click', () => {
    loadX50Registrations().catch((e) => toast(e.message));
  });

  // 批量注册
  document.getElementById('x50-bulk-run')?.addEventListener('click', async (ev) => {
    const btn = ev.currentTarget;
    btn.disabled = true;
    try {
      const r = await x50Api('/candidates/bulk-register', { method: 'POST', body: '{}' });
      toast(r.queued ? `已排队 ${r.queued} 个（并发 ${r.concurrency}）` : (r.message || '没有可注册的账号'));
      startX50Polling();
      await loadX50Registrations();
    } catch (e) {
      toast(e.message);
    } finally {
      btn.disabled = false;
    }
  });

  // 导出
  document.getElementById('x50-export')?.addEventListener('click', () => {
    window.open('/api/x50/export', '_blank');
  });

  // 行内操作（事件委托）
  document.getElementById('x50-rows')?.addEventListener('click', async (ev) => {
    const t = ev.target.closest('button');
    if (!t) return;
    const runId = t.dataset.x50Run;
    const keyId = t.dataset.x50Key;
    const delId = t.dataset.x50Del;
    try {
      if (runId) {
        const row = x50Rows.find((r) => r.id === runId);
        const path = row && row.status === 'queued' ? '/register' : '/retry';
        await x50Api(`/candidates/${encodeURIComponent(runId)}${path}`, { method: 'POST', body: '{}' });
        startX50Polling();
        await loadX50Registrations();
      } else if (keyId) {
        const r = await x50Api(`/candidates/${encodeURIComponent(keyId)}/key`);
        await navigator.clipboard.writeText(r.apiKey);
        toast('已复制 API Key');
      } else if (delId) {
        if (!confirm('确定删除这个账号？已注册账号请先复制 Key。')) return;
        await x50Api(`/candidates/${encodeURIComponent(delId)}`, { method: 'DELETE' });
        await loadX50Registrations();
      }
    } catch (e) {
      toast(e.message);
    }
  });

  // 设置页保存
  document.getElementById('x50s-save')?.addEventListener('click', async () => {
    const err = document.getElementById('x50s-error');
    err.textContent = '';
    try {
      const payload = x50CollectSettings(true);
      const r = await x50Api('/settings', { method: 'PUT', body: JSON.stringify(payload) });
      x50Settings = r.settings;
      const px = document.getElementById('x50s-proxies');
      if (px && payload.proxies) { px.value = ''; px.placeholder = `已保存 ${r.settings.proxyCount} 个代理（凭据不回显）。`; }
      toast('已保存');
    } catch (e) {
      err.textContent = e.message;
    }
  });
}

window.POOL_X50 = {
  pages: X50_PAGES,
  isX50,
  loadX50Registrations,
  loadX50Settings,
  startX50Polling,
  stopX50Polling,
  init: initX50Page,
};
