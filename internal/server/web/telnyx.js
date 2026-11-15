// Telnyx (telnyx.com) 注册机前端。
// 由 app.js 在 Telnyx 号池下加载；与 x50.js / deploy.js 同构。
//
// 与 50X 的差异（都来自实测）：
//   - 输入格式是 MsaArtifacts 导出（email----pw----clientId----refreshToken），
//     clientId 是 UUID 且每行相同，第三段不能当取件 token；
//   - 必须配打码令牌（Turnstile），siteKey 与 action 也要对，action 错一定失败；
//   - 单账号只能有 1 个 API Key，重跑会报 10039；
//   - Portal 链接一次性且几小时过期，所以「等邮件」是个会卡住的真实状态。

let telnyxPollTimer = null;
let telnyxRows = [];
let telnyxSettings = null;

// ---------------------------------------------------------------- helpers

async function telnyxApi(path, opts = {}) {
  const body = opts.body === undefined ? undefined : JSON.parse(opts.body);
  return api(opts.method || 'GET', '/api/telnyx' + path, body);
}

const TELNYX_STATUS_CLASS = {
  queued: 'muted',
  running: 'warn',
  waiting: 'warn',
  linked: 'warn',
  keyed: 'ok',
  failed: 'bad',
  cancelled: 'muted',
};

function telnyxBadge(status, text) {
  const cls = TELNYX_STATUS_CLASS[status] || 'muted';
  return `<span class="badge ${cls}">${telnyxEsc(text || status || '—')}</span>`;
}

function telnyxEsc(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g, (c) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));
}

// ---------------------------------------------------------------- 注册机页

async function loadTelnyxRegistrations() {
  if (!isTelnyx()) return;
  const errEl = document.getElementById('telnyx-poll-error');
  if (errEl) errEl.textContent = '';
  try {
    const [list, logs] = await Promise.all([telnyxApi('/candidates'), telnyxApi('/logs')]);
    telnyxRows = list.candidates || [];
    telnyxSettings = list.settings || null;
    renderTelnyxRows(telnyxRows);
    renderTelnyxStatus(telnyxRows);
    renderTelnyxLogs(logs.lines || []);
    const px = document.getElementById('telnyx-proxies');
    if (px && document.activeElement !== px && !px.dataset.dirty) {
      px.value = '';
      px.placeholder = telnyxSettings && telnyxSettings.proxyPoolCount
        ? `已保存 ${telnyxSettings.proxyPoolCount} 个代理（凭据不回显）。粘贴新列表可整体替换。`
        : 'socks5://user:pass@host:port';
    }
    // 没配打码令牌时给出明确提示，否则每次注册都会在第一步失败
    const warn = document.getElementById('telnyx-captcha-warn');
    if (warn) {
      warn.style.display = telnyxSettings && telnyxSettings.captchaTokenSet ? 'none' : '';
    }
  } catch (e) {
    if (errEl) errEl.textContent = e.message;
  }
}

function renderTelnyxRows(rows) {
  const body = document.getElementById('telnyx-rows');
  const cnt = document.getElementById('telnyx-count');
  if (cnt) cnt.textContent = String(rows.length);
  if (!body) return;
  if (!rows.length) {
    body.innerHTML = '<tr><td colspan="7" class="muted">还没有账号。在上方粘贴 '
      + '<code>邮箱----密码----clientId----refreshToken</code>（或 <code>邮箱----密码----取件token</code>）后点「导入」。</td></tr>';
    return;
  }
  body.innerHTML = rows.map((r) => {
    const stage = r.status === 'running' && r.stage
      ? `<span class="muted small">${telnyxEsc(r.stage)}</span>`
      : r.attempts > 1 ? `<span class="muted small">第 ${r.attempts} 次</span>` : '<span class="muted">—</span>';
    const proxy = r.proxyBound
      ? `<span class="small" title="${telnyxEsc(r.proxyLabel)}">${telnyxEsc(r.proxyLabel.split('@').pop())}</span>`
      : '<span class="muted">直连</span>';
    const key = r.hasKey
      ? `<code class="small">${telnyxEsc(r.keyPrefix)}…</code><br>`
        + `<span class="muted small">${telnyxEsc(r.creditsUsd ? '$' + r.creditsUsd : '')}`
        + `${r.accountLevel ? ' · ' + telnyxEsc(r.accountLevel) : ''}</span>`
      : (r.hasSession ? '<span class="muted small">有会话 · 无 Key</span>' : '<span class="muted">—</span>');
    const mail = r.hasMsa
      ? '<span class="small">Msa</span>'
      : r.hasShare ? `<code class="small">${telnyxEsc(r.shareTokenMasked || '')}</code>` : '<span class="error small">缺凭据</span>';
    const note = r.error
      ? `<span class="error small">${telnyxEsc(r.error)}</span>`
      : (r.egressIp ? `<span class="muted small">${telnyxEsc(r.egressIp)}</span>` : '');
    const canRun = r.status === 'queued' || r.status === 'failed' || r.status === 'cancelled';
    const busy = r.status === 'running';
    return `<tr>
      <td class="small">${telnyxEsc(r.email)}</td>
      <td>${telnyxBadge(r.status, r.statusText)}</td>
      <td>${stage}</td>
      <td>${mail}</td>
      <td>${proxy}</td>
      <td>${key}</td>
      <td>${note}</td>
      <td class="row-actions">
        ${canRun ? `<button type="button" class="btn small" data-telnyx-run="${telnyxEsc(r.id)}">${r.status === 'queued' ? '注册' : '重试'}</button>` : ''}
        ${r.hasKey ? `<button type="button" class="btn small" data-telnyx-key="${telnyxEsc(r.id)}">复制 Key</button>` : ''}
        <button type="button" class="btn small ghost" data-telnyx-del="${telnyxEsc(r.id)}">删除</button>
      </td>
    </tr>`;
  }).join('');
}

function renderTelnyxStatus(rows) {
  const el = document.getElementById('telnyx-status');
  if (!el) return;
  const count = (s) => rows.filter((r) => r.status === s).length;
  const withKey = rows.filter((r) => r.hasKey).length;
  const totalCredits = rows
    .filter((r) => r.hasKey)
    .reduce((n, r) => n + (Number(r.creditsUsd) || 0), 0);
  const items = [
    ['总数', rows.length, ''],
    ['待注册', count('queued'), count('queued') ? 'warn' : ''],
    ['注册中', count('running'), count('running') ? 'warn' : ''],
    ['等邮件', count('waiting'), count('waiting') ? 'warn' : ''],
    ['已完成', count('keyed'), count('keyed') ? 'ok' : ''],
    ['失败', count('failed'), count('failed') ? 'bad' : ''],
    ['Key', `${withKey}/${rows.length}`, ''],
    ['额度合计', `$${totalCredits}`, totalCredits ? 'ok' : ''],
  ];
  el.innerHTML = items
    .map(([k, v, cls]) => `<span class="badge ${cls}">${telnyxEsc(k)} ${telnyxEsc(v == null ? 0 : v)}</span>`)
    .join('');
}

function renderTelnyxLogs(lines) {
  const body = document.querySelector('#telnyx-log-table tbody');
  if (!body) return;
  body.innerHTML = lines.length
    ? lines.slice().reverse().map((l) => `<tr>
        <td class="small">${new Date(l.at).toLocaleTimeString()}</td>
        <td class="small">${telnyxEsc(l.type || '')}</td>
        <td class="small">${telnyxEsc(l.email || '')}</td>
        <td class="small">${telnyxEsc(l.msg || '')}</td>
      </tr>`).join('')
    : '<tr><td colspan="4" class="muted">暂无日志</td></tr>';
}

function startTelnyxPolling() {
  stopTelnyxPolling();
  telnyxPollTimer = setInterval(() => {
    if (document.hidden) return;
    if (!isTelnyx() || currentPage !== 'telnyxreg') { stopTelnyxPolling(); return; }
    loadTelnyxRegistrations().catch(() => {});
  }, 3000);
}

function stopTelnyxPolling() {
  if (telnyxPollTimer) { clearInterval(telnyxPollTimer); telnyxPollTimer = null; }
}

// ---------------------------------------------------------------- 设置页

async function loadTelnyxSettings() {
  if (!isTelnyx()) return;
  const errEl = document.getElementById('telnyxs-error');
  if (errEl) errEl.textContent = '';
  try {
    const settings = await telnyxApi('/settings');
    telnyxSettings = settings;
    const set = (id, v) => { const el = document.getElementById(id); if (el != null) el.value = v; };
    set('telnyxs-concurrency', settings.concurrency ?? 2);
    set('telnyxs-mail-timeout', settings.mailTimeoutSec ?? 240);
    set('telnyxs-mail-poll', settings.mailPollSec ?? 6);
    set('telnyxs-request-timeout', settings.requestTimeoutSec ?? 45);
    set('telnyxs-site-key', settings.siteKey || '');
    set('telnyxs-action', settings.turnstileAction || 'signup');
    set('telnyxs-captcha-attempts', settings.captchaAttempts ?? 3);
    set('telnyxs-key-tags', settings.keyTags || 'new');
    set('telnyxs-site-origin', settings.siteOrigin || '');
    set('telnyxs-api-base', settings.apiBase || '');
    set('telnyxs-portal-base', settings.portalBase || '');
    set('telnyxs-mail-base', settings.mailBase || '');
    const optIn = document.getElementById('telnyxs-optin');
    if (optIn) optIn.checked = settings.optInSubscriptions !== false;
    const auto = document.getElementById('telnyxs-auto-bind');
    if (auto) auto.checked = settings.autoBindProxy !== false;
    // 打码令牌只显示是否已存，不回显
    const tok = document.getElementById('telnyxs-captcha-token');
    if (tok && document.activeElement !== tok) {
      tok.value = '';
      tok.placeholder = settings.captchaTokenSet
        ? `已保存（${settings.captchaTokenMasked}）。留空保存则保持不变。`
        : 'captcha.run 的 API 令牌';
    }
    const px = document.getElementById('telnyxs-proxies');
    if (px && document.activeElement !== px) {
      px.value = '';
      px.placeholder = settings.proxyPoolCount
        ? `已保存 ${settings.proxyPoolCount} 个代理（凭据不回显）。粘贴新列表可整体替换，留空保存则保持不变。`
        : 'socks5://user:pass@host:port';
    }
  } catch (e) {
    if (errEl) errEl.textContent = e.message;
  }
}

function telnyxCollectSettings(includeProxies) {
  const val = (id) => { const el = document.getElementById(id); return el ? el.value : ''; };
  const out = {
    concurrency: Number(val('telnyxs-concurrency')) || 2,
    mailTimeoutSec: Number(val('telnyxs-mail-timeout')) || 240,
    mailPollSec: Number(val('telnyxs-mail-poll')) || 6,
    requestTimeoutSec: Number(val('telnyxs-request-timeout')) || 45,
    siteKey: val('telnyxs-site-key').trim(),
    turnstileAction: val('telnyxs-action').trim() || 'signup',
    captchaAttempts: Number(val('telnyxs-captcha-attempts')) || 3,
    keyTags: val('telnyxs-key-tags').trim() || 'new',
    siteOrigin: val('telnyxs-site-origin').trim(),
    apiBase: val('telnyxs-api-base').trim(),
    portalBase: val('telnyxs-portal-base').trim(),
    mailBase: val('telnyxs-mail-base').trim(),
    optInSubscriptions: document.getElementById('telnyxs-optin')?.checked !== false,
    autoBindProxy: document.getElementById('telnyxs-auto-bind')?.checked !== false,
  };
  // 打码令牌留空表示沿用已保存的，避免保存设置时清空
  const tok = val('telnyxs-captcha-token').trim();
  if (tok) out.captchaToken = tok;
  if (includeProxies) {
    const el = document.getElementById('telnyxs-proxies');
    const text = el ? el.value.trim() : '';
    if (text) out.proxies = text.split('\n').map((s) => s.trim()).filter(Boolean);
  }
  return out;
}

// ---------------------------------------------------------------- 绑定事件

function initTelnyxPage() {
  if (window.__telnyxInit) return;
  window.__telnyxInit = true;

  // 导入
  document.getElementById('telnyx-form')?.addEventListener('submit', async (ev) => {
    ev.preventDefault();
    const errEl = document.getElementById('telnyx-form-error');
    const resEl = document.getElementById('telnyx-bulk-result');
    errEl.textContent = '';
    resEl.textContent = '';
    const text = document.getElementById('telnyx-accounts').value;
    if (!text.trim()) { errEl.textContent = '请先粘贴账号'; return; }
    const pxEl = document.getElementById('telnyx-proxies');
    const payload = { text };
    if (pxEl && pxEl.value.trim()) {
      payload.proxies = pxEl.value.split('\n').map((s) => s.trim()).filter(Boolean);
    }
    try {
      const r = await telnyxApi('/candidates/bulk', { method: 'POST', body: JSON.stringify(payload) });
      resEl.textContent = `导入 ${r.added} 个，跳过 ${r.skipped} 个`
        + (r.errors && r.errors.length ? `；${r.errors.slice(0, 3).join('; ')}` : '');
      document.getElementById('telnyx-accounts').value = '';
      if (pxEl && payload.proxies) { pxEl.value = ''; delete pxEl.dataset.dirty; }
      await loadTelnyxRegistrations();
    } catch (e) {
      errEl.textContent = e.message;
    }
  });

  // 保存代理池（号池页）
  document.getElementById('telnyx-save-proxies')?.addEventListener('click', async () => {
    const errEl = document.getElementById('telnyx-form-error');
    const resEl = document.getElementById('telnyx-bulk-result');
    errEl.textContent = '';
    const pxEl = document.getElementById('telnyx-proxies');
    const text = pxEl ? pxEl.value.trim() : '';
    if (!text) { errEl.textContent = '代理池为空：不保存，避免误清空'; return; }
    try {
      const payload = telnyxCollectSettings(true);
      payload.proxies = text.split('\n').map((s) => s.trim()).filter(Boolean);
      await telnyxApi('/settings', { method: 'PUT', body: JSON.stringify(payload) });
      resEl.textContent = `已保存 ${payload.proxies.length} 个代理`;
      pxEl.value = ''; delete pxEl.dataset.dirty;
      await loadTelnyxRegistrations();
    } catch (e) {
      errEl.textContent = e.message;
    }
  });

  const pxEl = document.getElementById('telnyx-proxies');
  pxEl?.addEventListener('input', () => { pxEl.dataset.dirty = '1'; });

  document.getElementById('telnyx-refresh')?.addEventListener('click', () => {
    loadTelnyxRegistrations().catch((e) => toast(e.message));
  });

  // 批量注册
  document.getElementById('telnyx-bulk-run')?.addEventListener('click', async (ev) => {
    const btn = ev.currentTarget;
    btn.disabled = true;
    try {
      const r = await telnyxApi('/candidates/bulk-register', { method: 'POST', body: '{}' });
      toast(r.started ? `已排队 ${r.started} 个` : '没有可注册的账号');
      startTelnyxPolling();
      await loadTelnyxRegistrations();
    } catch (e) {
      toast(e.message);
    } finally {
      btn.disabled = false;
    }
  });

  // 导出
  document.getElementById('telnyx-export')?.addEventListener('click', () => {
    window.open('/api/telnyx/export', '_blank');
  });

  // 清理已完成
  document.getElementById('telnyx-clear-finished')?.addEventListener('click', async () => {
    if (!confirm('清空已完成/失败/取消的行？已注册账号请先复制 Key。')) return;
    try {
      const r = await telnyxApi('/candidates/clear-finished', { method: 'POST', body: '{}' });
      toast(`已清理 ${r.removed} 行`);
      await loadTelnyxRegistrations();
    } catch (e) {
      toast(e.message);
    }
  });

  // 行内操作（事件委托）
  document.getElementById('telnyx-rows')?.addEventListener('click', async (ev) => {
    const t = ev.target.closest('button');
    if (!t) return;
    const runId = t.dataset.telnyxRun;
    const keyId = t.dataset.telnyxKey;
    const delId = t.dataset.telnyxDel;
    try {
      if (runId) {
        const row = telnyxRows.find((r) => r.id === runId);
        const path = row && row.status === 'queued' ? '/register' : '/retry';
        await telnyxApi(`/candidates/${encodeURIComponent(runId)}${path}`, { method: 'POST', body: '{}' });
        startTelnyxPolling();
        await loadTelnyxRegistrations();
      } else if (keyId) {
        const r = await telnyxApi(`/candidates/${encodeURIComponent(keyId)}/key`);
        await navigator.clipboard.writeText(r.apiKey);
        toast('已复制 API Key');
      } else if (delId) {
        if (!confirm('确定删除这个账号？已注册账号请先复制 Key。')) return;
        await telnyxApi(`/candidates/${encodeURIComponent(delId)}`, { method: 'DELETE' });
        await loadTelnyxRegistrations();
      }
    } catch (e) {
      toast(e.message);
    }
  });

  // 设置页保存
  document.getElementById('telnyxs-save')?.addEventListener('click', async () => {
    const err = document.getElementById('telnyxs-error');
    err.textContent = '';
    try {
      const payload = telnyxCollectSettings(true);
      const r = await telnyxApi('/settings', { method: 'PUT', body: JSON.stringify(payload) });
      telnyxSettings = r;
      const tok = document.getElementById('telnyxs-captcha-token');
      if (tok && payload.captchaToken) { tok.value = ''; tok.placeholder = `已保存（${r.captchaTokenMasked}）。`; }
      const px = document.getElementById('telnyxs-proxies');
      if (px && payload.proxies) {
        px.value = '';
        px.placeholder = `已保存 ${r.proxyPoolCount} 个代理（凭据不回显）。`;
      }
      toast('已保存');
    } catch (e) {
      err.textContent = e.message;
    }
  });
}

window.POOL_TELNYX = {
  pages: TELNYX_PAGES,
  isTelnyx,
  loadTelnyxRegistrations,
  loadTelnyxSettings,
  startTelnyxPolling,
  stopTelnyxPolling,
  init: initTelnyxPage,
};
