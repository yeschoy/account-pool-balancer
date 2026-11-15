// ALTapi.ai (TokenPower / new-api 定制分支) 号池与注册机前端。
// 由 app.js 在 ALTapi 号池下加载；与 x50.js / deploy.js 同构。
//
// 后端接口（internal/server/altapi_registration.go / altapi_quota.go）：
//   GET    /api/altapi/candidates                -> {candidates, summary, settings}
//   POST   /api/altapi/candidates                {email, mailboxUrl, proxyUrl, username, password}
//   POST   /api/altapi/candidates/bulk           {lines, proxies?}
//   PATCH  /api/altapi/candidates/{id}
//   DELETE /api/altapi/candidates/{id}
//   POST   /api/altapi/candidates/{id}/register
//   POST   /api/altapi/candidates/bulk-register  {ids?|all}
//   POST   /api/altapi/candidates/{id}/cancel
//   POST   /api/altapi/candidates/{id}/import
//   POST   /api/altapi/candidates/import-all
//   POST   /api/altapi/candidates/{id}/quota
//   POST   /api/altapi/quota-refresh
//   POST   /api/altapi/proxy-test
//   GET    /api/altapi/settings
//   PUT    /api/altapi/settings
//   GET    /api/altapi/logs
//   GET    /api/altapi/status                    -> 号池概览

let altPollTimer = null;
let altRows = [];
let altSettings = null;
// altLogSeen 记录已渲染过的日志条数，用于收起态的数字角标（未读条数）。
let altLogSeen = 0;
// 选中的候选 id。批量按钮只作用于它；为空时按按钮各自的"全部"语义执行。
const altSelected = new Set();

// ---------------------------------------------------------------- helpers

async function altApi(path, opts = {}) {
  const body = opts.body === undefined ? undefined : JSON.parse(opts.body);
  return api(opts.method || 'GET', '/api/altapi' + path, body);
}

const ALT_STATUS_CLASS = {
  queued: 'muted',
  running: 'warn',
  sent_code: 'warn',
  registered: 'warn',
  verified: 'warn',
  key_created: 'ok',
  failed: 'bad',
  cancelled: 'muted',
};

const ALT_STATUS_TEXT = {
  queued: '待注册',
  running: '注册中',
  sent_code: '已发码',
  registered: '已注册',
  verified: '已登录',
  key_created: '已建 Key',
  failed: '失败',
  cancelled: '已取消',
};

function altEsc(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g, (c) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));
}

function altBadge(status, text) {
  const cls = ALT_STATUS_CLASS[status] || 'muted';
  return `<span class="badge ${cls}">${altEsc(text || ALT_STATUS_TEXT[status] || status || '—')}</span>`;
}

// 余额只显示后端读到过的数字。null/缺失 = 未知，绝不显示 0.00。
function altMoney(v) {
  if (v == null || v === '') return '<span class="muted" title="没有会话凭据，读不到余额">未知</span>';
  const n = Number(v);
  if (!Number.isFinite(n)) return '<span class="muted">未知</span>';
  return `<span class="small">$${n.toFixed(2)}</span>`;
}

// 候选行的余额：数字 + 读取时间 + 来源。没有凭据时明确说缺什么。
function altCandidateBalance(r) {
  const money = altMoney(r.balance_usd);
  if (r.balance_usd == null) {
    if (!r.session_cookie) {
      return `${money}<br><span class="muted small" title="余额只在 /api/* 控制面可读，需要登录得到的 session cookie">缺会话</span>`;
    }
    return `${money}<br><span class="muted small">未读取</span>`;
  }
  const at = r.quota_checked_at && !String(r.quota_checked_at).startsWith('0001')
    ? new Date(r.quota_checked_at).toLocaleTimeString()
    : '';
  const used = r.used_quota != null && r.quota_per_unit
    ? ` · 已用 $${(r.used_quota / r.quota_per_unit).toFixed(2)}`
    : '';
  return `${money}${at ? `<br><span class="muted small">${at}${used}</span>` : ''}`;
}

function altProxyCell(r) {
  if (!r.proxy_url) return '<span class="muted">直连</span>';
  const label = String(r.proxy_url).split('@').pop();
  return `<span class="small" title="${altEsc(r.proxy_url)}">${altEsc(label)}</span>`;
}

function altKeyCell(r) {
  if (r.api_key) {
    const head = String(r.api_key).slice(0, 8);
    return `<code class="small">${altEsc(head)}…</code>`
      + (r.balance_usd != null ? `<br>${altMoney(r.balance_usd)}` : '');
  }
  if (r.session_cookie) return '<span class="muted small">有会话 · 无 Key</span>';
  return '<span class="muted">—</span>';
}

// ---------------------------------------------------------------- 概览

// altLastStatus 缓存最近一次 /api/altapi/status，供通用「概览」页同步渲染卡片。
let altLastStatus = null;

// altPrefetchStatus 在进入 altapi 池时预取一次，让「概览」页首屏就有数据。
async function altPrefetchStatus() {
  try {
    altLastStatus = await altApi('/status');
  } catch {
    altLastStatus = null;
  }
}

// altOverviewCards 返回概览页要插的卡片（与 loadOverview 的 cards 同构）。
function altOverviewCards() {
  const s = altLastStatus;
  if (!s) return [['ALTapi 号池', '读取中…', '刷新后可看到余额合计']];
  const c = s.candidates || {};
  const pool = s.pool || {};
  return [
    ['ALTapi 余额合计', `$${(pool.totalBalanceUsd || 0).toFixed(2)}`,
      `已读 ${pool.withBalance || 0} 个 / 共 ${pool.count || 0} 个账号`],
    ['ALTapi 候选', `${c.total || 0} 个`,
      `已建 Key ${c.withKey || 0} · 已入池 ${c.imported || 0} · 失败 ${(c.byStatus || {}).failed || 0}`],
  ];
}

async function loadALTStatus() {
  if (!isALTapi()) return;
  const host = document.getElementById('alt-overview-cards');
  const tb = document.getElementById('alt-overview-rows');
  try {
    const s = await altApi('/status');
    const c = s.candidates || {};
    const pool = s.pool || {};
    const by = c.byStatus || {};
    if (host) {
      const cards = [
        ['号池账号', pool.count || 0, ''],
        ['已知余额', `$${(pool.totalBalanceUsd || 0).toFixed(2)}`, pool.withBalance ? 'ok' : 'muted'],
        ['余额未知', pool.balanceUnknown || 0, pool.balanceUnknown ? 'warn' : ''],
        ['候选总数', c.total || 0, ''],
        ['已发 Key', c.withKey || 0, c.withKey ? 'ok' : ''],
        ['已入池', c.imported || 0, c.imported ? 'ok' : ''],
        ['失败', by.failed || 0, by.failed ? 'bad' : ''],
        ['路由策略', s.routing || '—', ''],
      ];
      host.innerHTML = cards.map(([k, v, cls]) =>
        `<div class="card stat"><span class="muted small">${altEsc(k)}</span><strong class="${cls}">${altEsc(v)}</strong></div>`
      ).join('');
    }
    if (tb) {
      const rows = pool.accounts || [];
      tb.innerHTML = rows.length ? rows.map((r) => `<tr>
        <td class="small">${altEsc(r.name || r.email || r.id)}</td>
        <td>${altEsc(r.group || '—')}</td>
        <td>${r.enabled ? '<span class="badge ok">启用</span>' : '<span class="badge muted">停用</span>'}</td>
        <td>${altMoney(r.balanceUsd)}${r.balanceSource && r.balanceSource !== 'session'
          ? `<br><span class="muted small" title="${altEsc(r.readHint || '')}">${altEsc(r.balanceSource)}</span>` : ''}</td>
        <td class="small">$${(r.usedUsd || 0).toFixed(2)}</td>
        <td class="small">${altEsc(r.requests || 0)}</td>
        <td>${r.lastError ? `<span class="error small">${altEsc(r.lastError)}</span>` : '<span class="muted">—</span>'}</td>
      </tr>`).join('') : '<tr><td colspan="7" class="muted">号池里还没有 ALTapi 账号。先在「ALTapi 注册机」里注册并导入。</td></tr>';
    }
  } catch (e) {
    if (host) host.innerHTML = `<p class="error">${altEsc(e.message)}</p>`;
  }
}

// ---------------------------------------------------------------- 注册机页

async function loadALTRegistrations() {
  if (!isALTapi()) return;
  const errEl = document.getElementById('alt-poll-error');
  if (errEl) errEl.textContent = '';
  try {
    const [list, logs] = await Promise.all([altApi('/candidates'), altApi('/logs')]);
    altRows = list.candidates || [];
    altSettings = list.settings || null;
    renderALTRows(altRows);
    renderALTStatus(list.summary || {});
    renderALTLogs(logs.logs || []);
    const px = document.getElementById('alt-proxies');
    if (px && document.activeElement !== px && !px.dataset.dirty) {
      px.value = '';
      px.placeholder = altSettings && altSettings.proxy_pool && altSettings.proxy_pool.length
        ? `已保存 ${altSettings.proxy_pool.length} 个代理。粘贴新列表可整体替换。`
        : 'socks5://user:pass@host:port';
    }
  } catch (e) {
    if (errEl) errEl.textContent = e.message;
  }
}

function renderALTRows(rows) {
  const body = document.getElementById('alt-rows');
  const cnt = document.getElementById('alt-count');
  if (cnt) cnt.textContent = String(rows.length);
  if (!body) return;
  if (!rows.length) {
    body.innerHTML = '<tr><td colspan="8" class="muted">还没有候选。在上方粘贴 <code>邮箱----接码地址</code> 后点「导入」。</td></tr>';
    return;
  }
  body.innerHTML = rows.map((r) => {
    const busy = r.status === 'running' || r.status === 'sent_code';
    const canRun = r.status === 'queued' || r.status === 'failed' || r.status === 'cancelled';
    const note = r.error
      ? `<span class="error small">${altEsc(r.error)}</span>`
      : (!r.mailbox_url ? '<span class="error small">缺少接码地址</span>' : '<span class="muted">—</span>');
    const actions = [
      canRun ? `<button type="button" class="btn small" data-alt-run="${altEsc(r.id)}">${r.status === 'queued' ? '注册' : '重试'}</button>` : '',
      r.api_key ? `<button type="button" class="btn small" data-alt-key="${altEsc(r.id)}">复制 Key</button>` : '',
      (r.status === 'key_created' && !r.imported_account_id)
        ? `<button type="button" class="btn small" data-alt-import="${altEsc(r.id)}">导入号池</button>` : '',
      (r.session_cookie || r.api_key)
        ? `<button type="button" class="btn small" data-alt-quota="${altEsc(r.id)}">刷余额</button>` : '',
      busy
        ? `<button type="button" class="btn small" data-alt-cancel="${altEsc(r.id)}">取消</button>`
        : `<button type="button" class="btn small ghost" data-alt-del="${altEsc(r.id)}">删除</button>`,
    ].filter(Boolean).join(' ');
    const checked = altSelected.has(r.id);
    return `<tr${checked ? ' class="row-selected"' : ''}>
      <td class="col-check"><input type="checkbox" data-alt-pick="${altEsc(r.id)}"${checked ? ' checked' : ''}></td>
      <td class="small">${altEsc(r.email)}</td>
      <td>${altBadge(r.status)}</td>
      <td>${altCandidateBalance(r)}</td>
      <td>${altKeyCell(r)}</td>
      <td>${altProxyCell(r)}</td>
      <td class="small">${altEsc(r.account_id || '—')}</td>
      <td>${note}</td>
      <td class="row-actions">${actions}</td>
    </tr>`;
  }).join('');
}

// 顶部按钮共用的目标集合：有勾选就只用勾选的，否则传 undefined 让后端走"全部"。
function altTargetIDs() {
  return altSelected.size ? [...altSelected] : undefined;
}

function syncALTSelHint() {
  const el = document.getElementById('alt-sel-hint');
  const all = document.getElementById('alt-check-all');
  if (el) {
    el.textContent = altSelected.size ? `已选 ${altSelected.size} 个` : '';
  }
  if (all) {
    const ids = altRows.map((r) => r.id);
    all.checked = ids.length > 0 && ids.every((id) => altSelected.has(id));
    all.indeterminate = altSelected.size > 0 && !all.checked;
  }
}

function renderALTStatus(s) {
  const el = document.getElementById('alt-status-chips');
  if (!el) return;
  const by = s.byStatus || {};
  const items = [
    ['总数', s.total || 0, ''],
    ['待注册', by.queued || 0, by.queued ? 'warn' : ''],
    ['注册中', by.running || 0, by.running ? 'warn' : ''],
    ['已发 Key', s.withKey || 0, s.withKey ? 'ok' : ''],
    ['已入池', s.imported || 0, s.imported ? 'ok' : ''],
    ['失败', by.failed || 0, by.failed ? 'bad' : ''],
    ['余额未知', s.balanceUnknown || 0, s.balanceUnknown ? 'warn' : ''],
  ];
  el.innerHTML = items
    .map(([k, v, cls]) => `<span class="badge ${cls}">${altEsc(k)} ${altEsc(v)}</span>`)
    .join('');
}

function renderALTLogs(logs) {
  const body = document.querySelector('#alt-log-table tbody');
  if (!body) return;
  const dock = document.getElementById('alt-log-dock');
  const expanded = dock && dock.classList.contains('expanded');
  const badge = document.getElementById('alt-log-badge');
  if (badge) {
    if (expanded) {
      altLogSeen = logs.length;
      badge.textContent = '0';
      badge.style.display = 'none';
    } else {
      const unread = Math.max(0, logs.length - altLogSeen);
      // 收起时才提示；展开着说明用户正在看，数字没有意义
      badge.textContent = unread > 99 ? '99+' : String(unread);
      badge.style.display = unread ? '' : 'none';
    }
  }
  const rows = logs.slice(-200).reverse();
  body.innerHTML = rows.length
    ? rows.map((l) => `<tr>
        <td class="small">${new Date(l.at).toLocaleTimeString()}</td>
        <td class="small">${altEsc(l.type || '')}</td>
        <td class="small">${altEsc(l.email || '')}</td>
        <td class="small">${altEsc(l.message || '')}</td>
      </tr>`).join('')
    : '<tr><td colspan="4" class="muted">暂无日志</td></tr>';
}

function startALTPolling() {
  stopALTPolling();
  altPollTimer = setInterval(() => {
    if (document.hidden) return;
    if (!isALTapi() || currentPage !== 'altreg') { stopALTPolling(); return; }
    loadALTRegistrations().catch(() => {});
  }, 3000);
}

function stopALTPolling() {
  if (altPollTimer) { clearInterval(altPollTimer); altPollTimer = null; }
}

// ---------------------------------------------------------------- 设置页

async function loadALTSettings() {
  if (!isALTapi()) return;
  const errEl = document.getElementById('alts-error');
  if (errEl) errEl.textContent = '';
  try {
    const settings = await altApi('/settings');
    altSettings = settings;
    const val = (id) => document.getElementById(id);
    const set = (id, v) => { const el = val(id); if (el != null) el.value = v; };
    set('alts-workers', settings.workers ?? 2);
    set('alts-min-interval', settings.min_interval_sec ?? 31);
    set('alts-max-retries', settings.max_retries ?? 2);
    set('alts-max-attempts', settings.max_attempts ?? 2);
    set('alts-verify-timeout', settings.verify_timeout_sec ?? 180);
    set('alts-pace', settings.pace_scale ?? 1);
    set('alts-code-delay', settings.code_delay_sec ?? 0);
    set('alts-base-url', settings.base_url || 'https://altapi.ai');
    set('alts-python-bin', settings.python_bin || 'python3');
    set('alts-token-group', settings.token_group || 'default');
    set('alts-token-name', settings.token_name || 'registrar');
    set('alts-aff', settings.aff_code || '');
    const autoVerify = val('alts-auto-verify');
    if (autoVerify) autoVerify.checked = settings.auto_verify !== false;
    const imp = val('alts-import-to-pool');
    if (imp) imp.checked = settings.import_to_pool !== false;
    const px = val('alts-proxies');
    if (px && document.activeElement !== px) {
      px.value = '';
      const n = (settings.proxy_pool || []).length;
      px.placeholder = n
        ? `已保存 ${n} 个代理。粘贴新列表可整体替换，留空保存则保持不变。`
        : 'socks5://user:pass@host:port';
    }
  } catch (e) {
    if (errEl) errEl.textContent = e.message;
  }
}

function altCollectSettings(includeProxies) {
  const v = (id) => { const el = document.getElementById(id); return el ? el.value : ''; };
  const out = {
    workers: Number(v('alts-workers')) || 2,
    min_interval_sec: Number(v('alts-min-interval')) || 31,
    max_retries: Number(v('alts-max-retries')) || 0,
    max_attempts: Number(v('alts-max-attempts')) || 2,
    verify_timeout_sec: Number(v('alts-verify-timeout')) || 180,
    pace_scale: Number(v('alts-pace')) >= 0 ? Number(v('alts-pace')) : 1,
    code_delay_sec: Number(v('alts-code-delay')) >= 0 ? Number(v('alts-code-delay')) : 0,
    base_url: v('alts-base-url').trim() || 'https://altapi.ai',
    python_bin: v('alts-python-bin').trim() || 'python3',
    token_group: v('alts-token-group').trim() || 'default',
    token_name: v('alts-token-name').trim() || 'registrar',
    aff_code: v('alts-aff').trim(),
    auto_verify: document.getElementById('alts-auto-verify')?.checked !== false,
    import_to_pool: document.getElementById('alts-import-to-pool')?.checked !== false,
  };
  if (includeProxies) {
    const el = document.getElementById('alts-proxies');
    const text = el ? el.value.trim() : '';
    // 留空表示沿用已保存的代理，避免保存设置时清空凭据
    if (text) out.proxy_pool = text.split('\n').map((s) => s.trim()).filter(Boolean);
  }
  return out;
}

// ---------------------------------------------------------------- 绑定事件

function initALTPage() {
  if (window.__altInit) return;
  window.__altInit = true;

  // 日志浮层：右下角小方块，点开占右侧（与 Deploy 一致）
  const logDock = document.getElementById('alt-log-dock');
  const setLogExpanded = (on) => {
    if (!logDock) return;
    logDock.classList.toggle('expanded', on);
    logDock.classList.toggle('collapsed', !on);
    const btn = document.getElementById('alt-log-toggle');
    if (btn) btn.setAttribute('aria-expanded', on ? 'true' : 'false');
    if (on) {
      // 展开即视为已读，并把最新日志重新拉一遍
      altLogSeen = Number.MAX_SAFE_INTEGER;
      const badge = document.getElementById('alt-log-badge');
      if (badge) badge.style.display = 'none';
      loadALTRegistrations().catch(() => {});
    }
  };
  document.getElementById('alt-log-toggle')?.addEventListener('click', () => {
    const on = !(logDock && logDock.classList.contains('expanded'));
    // 重新开始计数：收起后再来的日志才算未读
    if (on) altLogSeen = 0;
    setLogExpanded(on);
  });
  document.getElementById('alt-log-close')?.addEventListener('click', () => setLogExpanded(false));

  // 全选 / 反选
  document.getElementById('alt-check-all')?.addEventListener('change', (ev) => {
    const on = ev.currentTarget.checked;
    altSelected.clear();
    if (on) altRows.forEach((r) => altSelected.add(r.id));
    renderALTRows(altRows);
  });

  // 行内勾选（事件委托，渲染后不用重绑）
  document.getElementById('alt-rows')?.addEventListener('change', (ev) => {
    const box = ev.target.closest('input[data-alt-pick]');
    if (!box) return;
    const id = box.dataset.altPick;
    if (box.checked) altSelected.add(id); else altSelected.delete(id);
    syncALTSelHint();
    const tr = box.closest('tr');
    if (tr) tr.classList.toggle('row-selected', box.checked);
  });

  // 分配代理：给未注册且直连的候选补出口
  document.getElementById('alt-assign-proxy')?.addEventListener('click', async (ev) => {
    const btn = ev.currentTarget;
    btn.disabled = true;
    try {
      const ids = altTargetIDs();
      const r = await altApi('/candidates/assign-proxy', {
        method: 'POST', body: JSON.stringify(ids ? { ids } : {}),
      });
      const parts = [`分配 ${r.assigned ?? 0} 个`];
      if (r.kept) parts.push(`已有代理保留 ${r.kept} 个`);
      if (r.skipped) parts.push(`已注册跳过 ${r.skipped} 个`);
      parts.push(`池内 ${r.poolSize ?? 0} 个代理`);
      toast(parts.join('，'));
      altSelected.clear();
      await loadALTRegistrations();
    } catch (e) {
      toast(e.message);
    } finally {
      btn.disabled = false;
    }
  });

  // 概览刷新
  document.getElementById('alt-overview-refresh')?.addEventListener('click', () => {
    loadALTStatus().catch((e) => toast(e.message));
  });

  // 导入候选
  document.getElementById('alt-form')?.addEventListener('submit', async (ev) => {
    ev.preventDefault();
    const errEl = document.getElementById('alt-form-error');
    const resEl = document.getElementById('alt-bulk-result');
    if (errEl) errEl.textContent = '';
    if (resEl) resEl.textContent = '';
    const text = document.getElementById('alt-accounts').value;
    if (!text.trim()) { if (errEl) errEl.textContent = '请先粘贴账号'; return; }
    const pxEl = document.getElementById('alt-proxies');
    const payload = { lines: text };
    if (pxEl && pxEl.value.trim()) {
      payload.proxies = pxEl.value.split('\n').map((s) => s.trim()).filter(Boolean);
    }
    try {
      const r = await altApi('/candidates/bulk', { method: 'POST', body: JSON.stringify(payload) });
      if (resEl) {
        // proxyPool 是后端实际使用的池子大小：没用已存池子时要让用户看见，
        // 否则「导入成功但全直连」只能靠逐行看代理列才发现。
        const poolNote = r.proxyPool
          ? `；代理池 ${r.proxyPool} 个，按顺序轮转分配`
          : '；没有可用代理池，全部直连';
        resEl.textContent = `导入 ${r.added ?? 0} 个，跳过 ${r.skipped ?? 0} 个`
          + poolNote
          + (r.skipped && r.skipped.length ? `；跳过示例：${r.skipped.slice(0, 3).join(', ')}` : '');
      }
      document.getElementById('alt-accounts').value = '';
      if (pxEl && payload.proxies) { pxEl.value = ''; delete pxEl.dataset.dirty; }
      await loadALTRegistrations();
    } catch (e) {
      if (errEl) errEl.textContent = e.message;
    }
  });

  // 保存代理池
  document.getElementById('alt-save-proxies')?.addEventListener('click', async () => {
    const errEl = document.getElementById('alt-form-error');
    const resEl = document.getElementById('alt-bulk-result');
    if (errEl) errEl.textContent = '';
    const pxEl = document.getElementById('alt-proxies');
    const text = pxEl ? pxEl.value.trim() : '';
    if (!text) { if (errEl) errEl.textContent = '代理池为空：不保存，避免误清空'; return; }
    try {
      const payload = altCollectSettings(true);
      payload.proxy_pool = text.split('\n').map((s) => s.trim()).filter(Boolean);
      await altApi('/settings', { method: 'PUT', body: JSON.stringify(payload) });
      if (resEl) resEl.textContent = `已保存 ${payload.proxy_pool.length} 个代理`;
      pxEl.value = ''; delete pxEl.dataset.dirty;
      await loadALTRegistrations();
    } catch (e) {
      if (errEl) errEl.textContent = e.message;
    }
  });

  document.getElementById('alt-proxies')?.addEventListener('input', (ev) => {
    ev.currentTarget.dataset.dirty = '1';
  });

  document.getElementById('alt-refresh')?.addEventListener('click', () => {
    loadALTRegistrations().catch((e) => toast(e.message));
  });

  // 批量注册
  document.getElementById('alt-bulk-run')?.addEventListener('click', async (ev) => {
    const btn = ev.currentTarget;
    btn.disabled = true;
    try {
      const ids = altTargetIDs();
      const r = await altApi('/candidates/bulk-register', {
        method: 'POST', body: JSON.stringify(ids ? { ids } : { all: true }),
      });
      toast(r.queued ? `已排队 ${r.queued} 个` : (r.message || '没有可注册的候选'));
      startALTPolling();
      await loadALTRegistrations();
    } catch (e) {
      toast(e.message);
    } finally {
      btn.disabled = false;
    }
  });

  // 全部导入号池
  document.getElementById('alt-import-all')?.addEventListener('click', async (ev) => {
    const btn = ev.currentTarget;
    btn.disabled = true;
    try {
      const ids = altTargetIDs();
      const r = await altApi('/candidates/import-all', {
        method: 'POST', body: JSON.stringify(ids ? { ids } : {}),
      });
      const failed = r.failed || [];
      toast(`已导入 ${r.imported ?? 0} 个，跳过 ${r.skipped ?? 0} 个`
        + (failed.length ? `，失败 ${failed.length} 个：${failed[0]}` : ''));
      altSelected.clear();
      await loadALTRegistrations();
    } catch (e) {
      toast(e.message);
    } finally {
      btn.disabled = false;
    }
  });

  // 刷新全部余额
  document.getElementById('alt-quota-refresh')?.addEventListener('click', async (ev) => {
    const btn = ev.currentTarget;
    btn.disabled = true;
    try {
      const ids = altTargetIDs();
      const r = await altApi('/quota-refresh', {
        method: 'POST', body: JSON.stringify(ids ? { ids } : {}),
      });
      // 后端返回 items/totalBalanceUsd/known，不是 refreshed/failed。
      const items = r.items || [];
      const ok = items.filter((x) => x.ok).length;
      const bad = items.length - ok;
      const skipped = r.skipped || 0;
      if (!items.length) {
        toast(skipped
          ? `没有可刷新的账号：${skipped} 个候选还没有会话或 Key`
          : '没有可刷新的账号');
      } else {
        const total = r.totalBalanceUsd != null ? `，合计 $${Number(r.totalBalanceUsd).toFixed(2)}` : '';
        toast(`已刷新 ${ok} 个${bad ? `，失败 ${bad} 个` : ''}${total}`
          + (skipped ? `；${skipped} 个无凭据跳过` : ''));
      }
      altSelected.clear();
      await loadALTRegistrations();
    } catch (e) {
      toast(e.message);
    } finally {
      btn.disabled = false;
    }
  });

  // 导出已完成
  document.getElementById('alt-export')?.addEventListener('click', () => {
    const done = altRows.filter((r) => r.api_key);
    if (!done.length) { toast('还没有完成建 Key 的账号'); return; }
    const lines = done.map((r) => [
      r.email, r.api_key, r.account_id || '', r.status,
      r.balance_usd == null ? '' : r.balance_usd,
    ].join('|'));
    const blob = new Blob([lines.join('\n') + '\n'], { type: 'text/plain' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `altapi-accounts-${new Date().toISOString().slice(0, 10)}.txt`;
    a.click();
    URL.revokeObjectURL(a.href);
  });

  // 行内操作
  document.getElementById('alt-rows')?.addEventListener('click', async (ev) => {
    const t = ev.target.closest('button');
    if (!t) return;
    const { altRun, altKey, altDel, altCancel, altImport, altQuota } = t.dataset;
    try {
      if (altRun) {
        await altApi(`/candidates/${encodeURIComponent(altRun)}/register`, { method: 'POST', body: '{}' });
        startALTPolling();
        await loadALTRegistrations();
      } else if (altKey) {
        const row = altRows.find((r) => r.id === altKey);
        if (row && row.api_key) {
          await navigator.clipboard.writeText(row.api_key);
          toast('已复制 API Key');
        }
      } else if (altImport) {
        await altApi(`/candidates/${encodeURIComponent(altImport)}/import`, { method: 'POST', body: '{}' });
        toast('已导入号池');
        await loadALTRegistrations();
      } else if (altQuota) {
        const r = await altApi(`/candidates/${encodeURIComponent(altQuota)}/quota`, { method: 'POST', body: '{}' });
        if (r.ok && r.balanceUsd != null) {
          toast(`余额 $${Number(r.balanceUsd).toFixed(2)}`);
        } else {
          toast(r.error || r.readHint || '读不到余额（需要有效会话）');
        }
        await loadALTRegistrations();
      } else if (altCancel) {
        await altApi(`/candidates/${encodeURIComponent(altCancel)}/cancel`, { method: 'POST', body: '{}' });
        await loadALTRegistrations();
      } else if (altDel) {
        if (!confirm('确定删除这个候选？已注册账号请先复制 Key。')) return;
        await altApi(`/candidates/${encodeURIComponent(altDel)}`, { method: 'DELETE' });
        await loadALTRegistrations();
      }
    } catch (e) {
      toast(e.message);
    }
  });

  // 代理连通性测试
  document.getElementById('alt-proxy-test')?.addEventListener('click', async () => {
    const el = document.getElementById('alt-proxy-list');
    const out = document.getElementById('alt-proxy-result');
    const text = el ? el.value.trim() : '';
    if (!text) { if (out) out.textContent = '请先填入要测试的代理'; return; }
    if (out) out.textContent = '测试中…';
    try {
      const r = await altApi('/proxy-test', {
        method: 'POST',
        body: JSON.stringify({ proxies: text.split('\n').map((s) => s.trim()).filter(Boolean) }),
      });
      const rows = r.results || [];
      if (out) {
        out.innerHTML = rows.length
          ? rows.map((x) => `<div class="small">${x.ok ? '✅' : '❌'} ${altEsc(x.proxy || '')} `
              + `${altEsc(x.ok ? `出口 IP ${x.exitIp || '?'} ${x.country || ''}` : (x.error || ''))}</div>`).join('')
          : '没有结果';
      }
    } catch (e) {
      if (out) out.textContent = e.message;
    }
  });

  // 设置页保存
  document.getElementById('alts-save')?.addEventListener('click', async () => {
    const err = document.getElementById('alts-error');
    if (err) err.textContent = '';
    try {
      const payload = altCollectSettings(true);
      const r = await altApi('/settings', { method: 'PUT', body: JSON.stringify(payload) });
      altSettings = r;
      const px = document.getElementById('alts-proxies');
      if (px && payload.proxy_pool) {
        px.value = '';
        px.placeholder = `已保存 ${payload.proxy_pool.length} 个代理。`;
      }
      toast('已保存');
    } catch (e) {
      if (err) err.textContent = e.message;
    }
  });
}

window.POOL_ALTAPI = {
  pages: ALTAPI_PAGES,
  isALTapi,
  loadALTRegistrations,
  loadALTSettings,
  loadALTStatus,
  altPrefetchStatus,
  loadALTLogs: () => loadALTRegistrations(),
  startALTPolling,
  stopALTPolling,
  init: initALTPage,
};
