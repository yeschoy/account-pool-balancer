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

// 候选上的卡只显示品牌+后四位，绝不回显完整卡号。
function drCardLabel(c) {
  const card = c.card || null;
  if (!card || !card.number) return '<span class="muted">未绑</span>';
  const n = String(card.number).replace(/\D/g, '');
  const tail = n.slice(-4);
  const brand = card.brand || '卡';
  const mark = c.recharged ? ' <span class="badge ok">已充</span>' : '';
  return `${drEsc(brand)} ****${drEsc(tail)}${mark}`;
}

// 代理明文展示：按用户要求显示完整 URL（含账号密码），方便核对与排错。
// 不截断、不脱敏；表格单元格允许换行，长代理不会撑破布局。
function drProxyLabel(url) {
  if (!url) return '<span class="muted">直连</span>';
  return `<span class="dr-proxy">${drEsc(url)}</span>`;
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
    body.innerHTML = '<tr><td colspan="8" class="muted">还没有账号。在上方粘贴 <code>邮箱----接码地址</code> 后点「导入」。</td></tr>';
    return;
  }
  body.innerHTML = rows.map((c) => {
    const acct = c.account_id ? `#${drEsc(c.account_id)}` : '<span class="muted">—</span>';
    const org = c.org_id ? `org ${drEsc(c.org_id)}` : '';
    const fp = c.browser ? `${drEsc(c.browser)}/${drEsc(c.platform || '')}` : '<span class="muted">—</span>';
    let note;
    if (c.error) note = `<span class="error">${drEsc(c.error)}</span>`;
    else if (c.imported_account_id) note = `<span class="badge ok">已入池</span> <span class="muted small">${drEsc(c.imported_account_id)}</span>`;
    else if (c.is_verified) note = `已验证 · ${c.session_cookie ? '有会话' : '无会话'}${c.secret_key ? ' · 有 Key' : ' · 无 Key'}`;
    else note = (c.detail || '');
    const busy = c.status === 'running';
    const canRun = c.status === 'queued' || c.status === 'failed' || c.status === 'cancelled';
    return `<tr>
      <td>${drEsc(c.email)}</td>
      <td>${drBadge(c.status)}</td>
      <td>${acct}<br><span class="muted small">${org}</span></td>
      <td>${fp}</td>
      <td>${drProxyLabel(c.proxy_url)} <button type="button" class="btn small ghost" data-dr-proxy="${drEsc(c.id)}" title="编辑代理">改</button></td>
      <td>${drCardLabel(c)} <button type="button" class="btn small ghost" data-dr-card="${drEsc(c.id)}" title="设置卡号">设卡</button></td>
      <td class="small">${note}</td>
      <td class="row-actions">
        ${canRun ? `<button type="button" class="btn small" data-dr-run="${drEsc(c.id)}">注册</button>` : ''}
        ${(!busy && c.status === 'registered' && !c.is_verified && !c.session_cookie)
          ? `<button type="button" class="btn small" data-dr-resend="${drEsc(c.id)}" title="重新读取接码箱里的验证信并完成验证。不会重发邮件，也不会重新注册。">重新认证</button>` : ''}
        ${(c.session_cookie || c.password) ? `<button type="button" class="btn small" data-dr-resume="${drEsc(c.id)}" title="先校验已保存的会话；会话失效则用保存的密码走 deploy.com/login 登录，不重复注册">${c.session_cookie ? '校验会话/登录' : '去登录'}</button>` : ''}
        ${(c.status === 'verified' && !c.imported_account_id) ? `<button type="button" class="btn small primary" data-dr-import="${drEsc(c.id)}">入池</button>` : ''}
        ${(c.status === 'verified' && !c.recharged && c.session_cookie) ? `<button type="button" class="btn small" data-dr-bind="${drEsc(c.id)}">绑卡</button>` : ''}
        ${busy ? `<button type="button" class="btn small" data-dr-cancel="${drEsc(c.id)}">取消</button>` : ''}
        <button type="button" class="btn small ghost" data-dr-del="${drEsc(c.id)}">删除</button>
      </td>
    </tr>`;
  }).join('');
}

function renderDeployStatus(s) {
  const el = document.getElementById('dr-status');
  if (!el) return;
  const items = [
    ['总数', s.total, ''],
    ['待注册', s.queued, s.queued ? 'warn' : ''],
    ['注册中', s.running, s.running ? 'warn' : ''],
    ['已验证', s.verified, s.verified ? 'ok' : ''],
    ['失败', s.failed, s.failed ? 'bad' : ''],
    ['worker', s.busy ? '运行中' : '空闲', s.busy ? 'warn' : ''],
  ];
  el.innerHTML = items
    .map(([k, v, cls]) => `<span class="badge ${cls}">${drEsc(k)} ${drEsc(v)}</span>`)
    .join('');
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
    const ip = document.getElementById('ds-import-to-pool');
    if (ip) ip.checked = !!settings.import_to_pool;
    // 卡列表在号池页（注册机）而非设置页
    const cd = document.getElementById('dr-cards');
    if (cd && document.activeElement !== cd && !cd.dataset.dirty) {
      cd.value = (settings.cards || []).join('\n');
    }
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
    import_to_pool: !!document.getElementById('ds-import-to-pool')?.checked,
    proxy_pool: px,
  };
}

// readDeployCards 读出号池页的卡列表（只 trim/去空行，校验交给后端）
function readDeployCards() {
  const el = document.getElementById('dr-cards');
  return el ? el.value.split('\n').map((s) => s.trim()).filter(Boolean) : [];
}

// showCardResult 把后端校验结果显示在卡列表下方
function showCardResult(count, errors) {
  const el = document.getElementById('dr-card-msg');
  if (!el) return;
  el.style.whiteSpace = 'pre-wrap';
  if (errors && errors.length) {
    el.className = 'error small';
    el.textContent = `已识别 ${count} 张卡，以下行无效：\n` + errors.join('\n');
  } else if (count) {
    el.className = 'muted small';
    el.textContent = `已识别 ${count} 张卡，全部通过校验`;
  } else {
    el.className = 'muted small';
    el.textContent = '未填写银行卡（不绑卡）';
  }
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
  //
  // 号池页只有「代理池」一个输入框，其余 ds-* 控件都在「Deploy 注册设置」页里
  // 且未必渲染过。以前这里直接提交 collectDeploySettings()，读不到的字段会落到
  // 默认值再把整份设置覆盖回去——代理池因此被清空，python_bin 也会从镜像里的
  // venv 被重置成 python3（容器内没有 curl_cffi，注册直接失败）。
  // 现在只提交代理池，其余字段沿用服务端现值。
  document.getElementById('dr-save-settings')?.addEventListener('click', async () => {
    const errEl = document.getElementById('dr-form-error');
    const resEl = document.getElementById('dr-bulk-result');
    errEl.textContent = '';
    try {
      const cur = await drApi('/settings');
      const next = Object.assign({}, (cur && cur.settings) || {});
      const px = document.getElementById('dr-proxies');
      next.proxy_pool = px ? px.value.split('\n').map((s) => s.trim()).filter(Boolean) : [];
      next.cards = readDeployCards();
      const r = await drApi('/settings', { method: 'PUT', body: JSON.stringify(next) });
      resEl.textContent = `已保存 ${next.proxy_pool.length} 个代理、${r.card_count || 0} 张卡`;
      showCardResult(r.card_count || 0, r.card_errors);
      if (px) delete px.dataset.dirty;
      const cd = document.getElementById('dr-cards');
      if (cd) delete cd.dataset.dirty;
    } catch (e) {
      errEl.textContent = e.message;
    }
  });

  // 分配代理：把代理池按顺序落盘到每条候选记录
  document.getElementById('dr-assign-proxies')?.addEventListener('click', async () => {
    const errEl = document.getElementById('dr-form-error');
    const resEl = document.getElementById('dr-bulk-result');
    errEl.textContent = '';
    const overwrite = window.confirm(
      '把代理池按顺序分配给账号。\n\n' +
      '确定 = 全部重排（覆盖已有代理）\n' +
      '取消 = 只补还没代理的账号'
    );
    try {
      const r = await drApi('/candidates/assign-proxies', {
        method: 'POST',
        body: JSON.stringify({ overwrite }),
      });
      resEl.textContent = `已分配 ${r.assigned} 个，保留 ${r.kept} 个（池内 ${r.pool_size} 个）`;
      await loadDeployRegistrations();
    } catch (e) {
      errEl.textContent = e.message;
    }
  });

  const pxEl = document.getElementById('dr-proxies');
  pxEl?.addEventListener('input', () => { pxEl.dataset.dirty = '1'; });
  const cdEl = document.getElementById('dr-cards');
  cdEl?.addEventListener('input', () => { cdEl.dataset.dirty = '1'; });

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

  // 全部入池
  document.getElementById('dr-bulk-import')?.addEventListener('click', async (ev) => {
    const btn = ev.currentTarget;
    btn.disabled = true;
    try {
      const r = await drApi('/candidates/import-all', { method: 'POST', body: '{}' });
      toast(`入池 ${r.imported} 个，跳过 ${r.skipped} 个` + (r.failed ? `，失败 ${r.failed} 个` : ''));
      if (r.errors && r.errors.length) console.warn('入池失败:', r.errors);
      await loadDeployRegistrations();
    } catch (e) {
      toast(e.message);
    } finally {
      btn.disabled = false;
    }
  });

  // 批量绑卡
  document.getElementById('dr-bulk-bind')?.addEventListener('click', async (ev) => {
    const errEl = document.getElementById('dr-form-error');
    const resEl = document.getElementById('dr-bulk-result');
    errEl.textContent = '';
    if (!window.confirm('给所有已注册且未绑卡的账号依次绑卡并充值 $5？\n\n会真实扣款。卡数量不足时只绑前 N 个。')) return;
    ev.currentTarget.disabled = true;
    try {
      const r = await drApi('/bind-cards', { method: 'POST', body: JSON.stringify({ live: true }) });
      if (r.message) {
        resEl.textContent = r.message;
      } else {
        resEl.textContent = `绑卡成功 ${r.bound} 个，失败 ${r.failed || 0} 个（可用卡 ${r.cards_available} 张，待绑 ${r.total} 个）`;
      }
      await loadDeployRegistrations();
    } catch (e) {
      errEl.textContent = e.message;
    } finally {
      ev.currentTarget.disabled = false;
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
    const resendId = t.dataset.drResend;
    const importId = t.dataset.drImport;
    const proxyId = t.dataset.drProxy;
    const bindId = t.dataset.drBind;
    const cardId = t.dataset.drCard;
    try {
      if (runId) {
        await drApi(`/candidates/${encodeURIComponent(runId)}/register`, { method: 'POST', body: '{}' });
        startDeployPolling();
      } else if (cancelId) {
        await drApi(`/candidates/${encodeURIComponent(cancelId)}/cancel`, { method: 'POST', body: '{}' });
      } else if (cardId) {
        const next = window.prompt('该账号使用的卡：卡号----月/年----CVV\n留空 = 清除', '');
        if (next === null) return;
        try {
          await drApi(`/candidates/${encodeURIComponent(cardId)}/card`, {
            method: 'PATCH',
            body: JSON.stringify({ card: next.trim() }),
          });
          toast(next.trim() ? '已设置卡号' : '已清除卡号');
          await loadDeployRegistrations();
        } catch (e) {
          toast(e.message);
        }
        return;
      } else if (bindId) {
        if (!window.confirm('给这个账号绑卡并充值 $5？\n\n会真实扣款。')) return;
        try {
          const r = await drApi(`/candidates/${encodeURIComponent(bindId)}/bind-card`, {
            method: 'POST',
            body: JSON.stringify({ live: true }),
          });
          toast(r.ok ? '绑卡成功' : `绑卡失败：${r.error || r.step}`);
          await loadDeployRegistrations();
        } catch (e) {
          toast(e.message);
        }
        return;
      } else if (proxyId) {
        // 从当前单元格取现值（明文展示，直接可用）
        const cell = t.closest('td');
        const shown = cell ? cell.querySelector('.dr-proxy') : null;
        const cur = shown ? shown.textContent.trim() : '';
        const next = window.prompt('代理地址（留空 = 直连）\n支持 socks5:// 与 http://', cur);
        if (next === null) return;
        try {
          await drApi(`/candidates/${encodeURIComponent(proxyId)}`, {
            method: 'PATCH',
            body: JSON.stringify({ proxy_url: next.trim() }),
          });
          toast('代理已更新');
          await loadDeployRegistrations();
        } catch (e) {
          toast(e.message);
        }
        return;
      } else if (importId) {
        const r = await drApi(`/candidates/${encodeURIComponent(importId)}/import`, { method: 'POST', body: '{}' });
        toast(r.already ? '已在池中' : `已入池 ${r.account_id}`);
        await loadDeployRegistrations();
        return;
      } else if (resendId) {
        toast('正在重新读取接码箱并验证…');
        const r = await drApi(`/candidates/${encodeURIComponent(resendId)}/resend-verification`, { method: 'POST', body: '{}' });
        toast(r.status === 'verified' ? '认证成功' : (r.error || r.detail || '接码箱里还没有验证信，过几分钟再试'));
        await loadDeployRegistrations();
        return;
      } else if (resumeId) {
        await drApi(`/candidates/${encodeURIComponent(resumeId)}/resume`, { method: 'POST', body: '{}' });
        toast('开始校验会话，失效则用保存的密码登录');
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
    const cardMsg = document.getElementById('ds-card-msg');
    err.textContent = '';
    if (cardMsg) cardMsg.textContent = '';
    try {
      const r = await drApi('/settings', { method: 'PUT', body: JSON.stringify(collectDeploySettings()) });
      toast('已保存');
      if (cardMsg) {
        const n = r.card_count || 0;
        const bad = r.card_errors || [];
        cardMsg.textContent = bad.length
          ? `已识别 ${n} 张卡，以下行无效：\n` + bad.join('\n')
          : (n ? `已识别 ${n} 张卡，全部通过校验` : '未填写银行卡');
        cardMsg.style.whiteSpace = 'pre-wrap';
        cardMsg.className = bad.length ? 'error small' : 'muted small';
      }
    } catch (e) {
      err.textContent = e.message;
    }
  });

  // 测试代理：用系统代理 + 第一个上游代理各发一次请求
  document.getElementById('ds-proxy-test')?.addEventListener('click', async () => {
    const out = document.getElementById('ds-proxy-result');
    if (out) out.textContent = '测试中…';
    try {
      const r = await drApi('/proxy-test', { method: 'POST', body: '{}' });
      const lines = (r.results || []).map((x) =>
        `${x.ok ? '✅' : '❌'} ${x.target}${x.ip ? ' → ' + x.ip : ''}${x.error ? '  ' + x.error : ''}`);
      if (out) {
        out.style.whiteSpace = 'pre-wrap';
        out.textContent = lines.join('\n') || '没有可测试的目标';
      }
    } catch (e) {
      if (out) out.textContent = '测试失败：' + e.message;
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
