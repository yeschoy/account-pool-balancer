// Deploy (deploy.com) 号池管理 + 注册机前端。
// 由 app.js 在 Deploy 号池下加载；与 perch.js / tokenharbor.js 同构。

// Pool identity and owned pages are declared once in app.js.
let drPollTimer = null;

// ---------------------------------------------------------------- helpers

async function drApi(path, opts = {}) {
  const body = opts.body === undefined ? undefined : JSON.parse(opts.body);
  return api(opts.method || 'GET', '/api/deploy' + path, body);
}

const DR_STATUS_LABEL = {
  queued: '待注册',
  running: '注册中',
  registered: '已注册·待验证',
  verified: '已验证',
  failed: '失败',
  cancelled: '已取消',
};

function drBadge(status) {
  const cls = {
    queued: 'muted', running: 'warn', registered: 'warn',
    verified: 'ok', failed: 'bad', cancelled: 'muted',
  }[status] || 'muted';
  return `<span class="badge ${cls}">${DR_STATUS_LABEL[status] || status || '—'}</span>`;
}

function drEsc(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g, (c) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));
}

function drProxyLabel(url) {
  if (!url) return '<span class="muted">直连</span>';
  try {
    const u = new URL(url);
    return `${u.protocol.replace(':', '')}//${u.hostname}:${u.port}`;
  } catch (_) {
    return drEsc(url.slice(0, 40));
  }
}

// ---------------------------------------------------------------- 号池页

async function loadDeployRegistrations() {
  if (!isDeploy()) return;
  const errEl = document.getElementById('dr-poll-error');
  if (errEl) errEl.textContent = '';
  try {
    const [list, status, logs, settings] = await Promise.all([
      drApi('/candidates'),
      drApi('/status'),
      drApi('/logs'),
      drApi('/settings'),
    ]);
    renderDeployRows(list.candidates || []);
    renderDeployStatus(status);
    renderDeployLogs(logs.logs || []);
    // 代理池只在用户没编辑过时回填，避免覆盖正在输入的内容
    const px = document.getElementById('dr-proxies');
    if (px && document.activeElement !== px && !px.dataset.dirty) {
      px.value = (settings.settings && settings.settings.proxy_pool || []).join('\n');
    }
  } catch (e) {
    if (errEl) errEl.textContent = e.message;
  }
}

function renderDeployRows(rows) {
  const body = document.getElementById('dr-rows');
  const cnt = document.getElementById('dr-count');
  if (cnt) cnt.textContent = String(rows.length);
  if (!body) return;
  if (!rows.length) {
    body.innerHTML = '<tr><td colspan="7" class="muted">还没有账号。在上方粘贴 <code>邮箱----接码地址</code> 后点「导入」。</td></tr>';
    return;
  }
  body.innerHTML = rows.map((c) => {
    const acct = c.account_id ? `#${drEsc(c.account_id)}` : '<span class="muted">—</span>';
    const org = c.org_id ? `org ${drEsc(c.org_id)}` : '';
    const fp = c.browser ? `${drEsc(c.browser)}/${drEsc(c.platform || '')}` : '<span class="muted">—</span>';
    const note = c.error
      ? `<span class="error">${drEsc(c.error)}</span>`
      : (c.is_verified ? `已验证 · ${c.session_cookie ? '有会话' : '无会话'}` : (c.detail || ''));
    const busy = c.status === 'running';
    const canRun = c.status === 'queued' || c.status === 'failed' || c.status === 'cancelled';
    return `<tr>
      <td>${drEsc(c.email)}</td>
      <td>${drBadge(c.status)}</td>
      <td>${acct}<br><span class="muted small">${org}</span></td>
      <td>${fp}</td>
      <td>${drProxyLabel(c.proxy_url)}</td>
      <td class="small">${note}</td>
      <td class="row-actions">
        ${canRun ? `<button type="button" class="btn small" data-dr-run="${drEsc(c.id)}">注册</button>` : ''}
        ${c.session_cookie ? `<button type="button" class="btn small" data-dr-resume="${drEsc(c.id)}">校验会话</button>` : ''}
        ${busy ? `<button type="button" class="btn small" data-dr-cancel="${drEsc(c.id)}">取消</button>` : ''}
        <button type="button" class="btn small ghost" data-dr-del="${drEsc(c.id)}">删除</button>
      </td>
    </tr>`;
  }).join('');
}

function renderDeployStatus(s) {
  const el = document.getElementById('dr-status');
  if (!el) return;
  el.innerHTML = [
    ['总数', s.total],
    ['待注册', s.queued],
    ['注册中', s.running],
    ['已验证', s.verified],
    ['失败', s.failed],
    ['worker', s.busy ? '运行中' : '空闲'],
  ].map(([k, v]) => `<div><dt>${k}</dt><dd>${drEsc(v)}</dd></div>`).join('');
}

function renderDeployLogs(logs) {
  const body = document.querySelector('#dr-log-table tbody');
  if (!body) return;
  const rows = logs.slice(-200).reverse();
  body.innerHTML = rows.length
    ? rows.map((l) => `<tr>
        <td class="small">${new Date(l.at).toLocaleTimeString()}</td>
        <td class="small">${drEsc(l.type)}</td>
        <td class="small">${drEsc(l.email || '')}</td>
        <td class="small">${drEsc(l.message || '')}</td>
      </tr>`).join('')
    : '<tr><td colspan="4" class="muted">暂无日志</td></tr>';
}

function startDeployPolling() {
  stopDeployPolling();
  drPollTimer = setInterval(() => {
    if (document.hidden) return;
    if (!isDeploy() || currentPage !== 'dreg') { stopDeployPolling(); return; }
    loadDeployRegistrations().catch(() => {});
  }, 4000);
}

function stopDeployPolling() {
  if (drPollTimer) { clearInterval(drPollTimer); drPollTimer = null; }
}

// ---------------------------------------------------------------- 设置页

async function loadDeploySettings() {
  if (!isDeploy()) return;
  try {
    const { settings } = await drApi('/settings');
    const set = (id, v) => { const el = document.getElementById(id); if (el) el.value = v; };
    set('ds-interval', settings.min_interval_sec ?? 8);
    set('ds-retries', settings.max_retries ?? 6);
    set('ds-verify-timeout', settings.verify_timeout_sec ?? 240);
    set('ds-transport', settings.transport || 'curl_cffi');
    set('ds-python', settings.python_bin || 'python3');
    set('ds-sys-proxy', settings.sys_proxy || '127.0.0.1:10808');
    set('ds-bridge-port', settings.bridge_port || 17500);
    const av = document.getElementById('ds-auto-verify');
    if (av) av.checked = settings.auto_verify !== false;
  } catch (e) {
    const err = document.getElementById('ds-error');
    if (err) err.textContent = e.message;
  }
}

function collectDeploySettings() {
  const val = (id) => { const el = document.getElementById(id); return el ? el.value : ''; };
  const pxRaw = document.getElementById('dr-proxies');
  const px = pxRaw ? pxRaw.value.split('\n').map((s) => s.trim()).filter(Boolean) : [];
  return {
    min_interval_sec: Number(val('ds-interval')) || 8,
    max_retries: Number(val('ds-retries')),
    verify_timeout_sec: Number(val('ds-verify-timeout')) || 240,
    transport: val('ds-transport') || 'curl_cffi',
    python_bin: val('ds-python') || 'python3',
    sys_proxy: val('ds-sys-proxy').trim(),
    bridge_port: Number(val('ds-bridge-port')) || 17500,
    auto_verify: document.getElementById('ds-auto-verify')?.checked !== false,
    proxy_pool: px,
  };
}

// ---------------------------------------------------------------- 绑定事件

function initDeployPage() {
  if (window.__deployInit) return;
  window.__deployInit = true;

  // 导入
  const form = document.getElementById('dr-form');
  form?.addEventListener('submit', async (ev) => {
    ev.preventDefault();
    const errEl = document.getElementById('dr-form-error');
    const resEl = document.getElementById('dr-bulk-result');
    errEl.textContent = '';
    resEl.textContent = '';
    const text = document.getElementById('dr-accounts').value;
    if (!text.trim()) { errEl.textContent = '请先粘贴账号'; return; }
    try {
      const r = await drApi('/candidates/bulk', { method: 'POST', body: JSON.stringify({ text }) });
      resEl.textContent = `导入 ${r.added} 个，跳过 ${r.skipped} 个` +
        (r.errors && r.errors.length ? `；${r.errors.slice(0, 3).join('; ')}` : '');
      document.getElementById('dr-accounts').value = '';
      await loadDeployRegistrations();
    } catch (e) {
      errEl.textContent = e.message;
    }
  });

  // 保存设置（号池页的代理池按钮）
  document.getElementById('dr-save-settings')?.addEventListener('click', async () => {
    const errEl = document.getElementById('dr-form-error');
    const resEl = document.getElementById('dr-bulk-result');
    errEl.textContent = '';
    try {
      await drApi('/settings', { method: 'PUT', body: JSON.stringify(collectDeploySettings()) });
      resEl.textContent = '设置已保存';
      const px = document.getElementById('dr-proxies');
      if (px) delete px.dataset.dirty;
    } catch (e) {
      errEl.textContent = e.message;
    }
  });

  const pxEl = document.getElementById('dr-proxies');
  pxEl?.addEventListener('input', () => { pxEl.dataset.dirty = '1'; });

  // 刷新
  document.getElementById('dr-refresh')?.addEventListener('click', () => {
    loadDeployRegistrations().catch((e) => toast(e.message));
  });

  // 批量注册
  document.getElementById('dr-bulk-run')?.addEventListener('click', async (ev) => {
    const btn = ev.currentTarget;
    btn.disabled = true;
    try {
      const r = await drApi('/candidates/bulk-register', { method: 'POST', body: '{}' });
      toast(`已排队 ${r.queued} 个`);
      startDeployPolling();
      await loadDeployRegistrations();
    } catch (e) {
      toast(e.message);
    } finally {
      btn.disabled = false;
    }
  });

  // 行内操作（事件委托）
  document.getElementById('dr-rows')?.addEventListener('click', async (ev) => {
    const t = ev.target.closest('button');
    if (!t) return;
    const runId = t.dataset.drRun;
    const cancelId = t.dataset.drCancel;
    const delId = t.dataset.drDel;
    const resumeId = t.dataset.drResume;
    try {
      if (runId) {
        await drApi(`/candidates/${encodeURIComponent(runId)}/register`, { method: 'POST', body: '{}' });
        startDeployPolling();
      } else if (cancelId) {
        await drApi(`/candidates/${encodeURIComponent(cancelId)}/cancel`, { method: 'POST', body: '{}' });
      } else if (resumeId) {
        await drApi(`/candidates/${encodeURIComponent(resumeId)}/resume`, { method: 'POST', body: '{}' });
        toast('开始校验会话');
        startDeployPolling();
      } else if (delId) {
        if (!confirm('确定删除这个账号？')) return;
        await drApi(`/candidates/${encodeURIComponent(delId)}`, { method: 'DELETE' });
      }
      await loadDeployRegistrations();
    } catch (e) {
      toast(e.message);
    }
  });

  document.getElementById('dr-log-clear')?.addEventListener('click', () => {
    const body = document.querySelector('#dr-log-table tbody');
    if (body) body.innerHTML = '';
  });

  // 设置页保存
  document.getElementById('ds-save')?.addEventListener('click', async () => {
    const err = document.getElementById('ds-error');
    err.textContent = '';
    try {
      await drApi('/settings', { method: 'PUT', body: JSON.stringify(collectDeploySettings()) });
      toast('已保存');
    } catch (e) {
      err.textContent = e.message;
    }
  });
}

window.POOL_DEPLOY = {
  pages: DR_PAGES,
  isDeploy,
  loadDeployRegistrations,
  loadDeploySettings,
  startDeployPolling,
  stopDeployPolling,
  init: initDeployPage,
};
