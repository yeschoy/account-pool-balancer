// TokenHarbor 号池管理台。与 app.js 共用 $ / api / esc / modal，加载顺序在 app.js 之后。
// 账号凭证是 thk_live_ Key；页面只显示掩码，从不回显密码、Cookie 或完整 Key。

const TH_STATUS = {
  saved: ['已保存（未注册）', 'warn'],
  queued: ['排队中', 'accent'],
  running: ['注册中', 'accent'],
  registered: ['已注册', 'ok'],
  unverified: ['待验证', 'warn'],
  imported: ['已入池', 'ok'],
  failed: ['失败', 'bad'],
  uncertain: ['中断', 'warn'],
};

const thState = { job: '', filter: 'pending', jobs: [], rows: [], busy: false, timer: null, generation: 0, selected: new Set(), pending: new Set(), batchBusy: false, batchPrompt: false, batchMessage: '' };
// 右下角注册日志：轮询增量，暂停时只累积不渲染。
const thLog = { seq: 0, lines: [], timer: null, inFlight: false, paused: false, visible: false, started: false };

function thLabel(status) {
  const [label, cls] = TH_STATUS[status] || [status || '未知', ''];
  return `<span class="badge ${cls}">${esc(label)}</span>`;
}

// 详情来自服务端，可能是上游错误文本：只按纯文本渲染。
function thSafe(text) {
  return esc(String(text || '')).replace(/\n/g, ' ');
}

// ---- 概览 ----
async function loadTHOverview() {
  const [accounts, overview, allowance] = await Promise.all([
    api('GET', '/api/accounts?provider=tokenharbor').catch(() => null),
    api('GET', '/api/overview?provider=tokenharbor').catch(() => null),
    api('GET', '/api/tokenharbor/overview').catch(() => null),
  ]);
  const list = (accounts && accounts.accounts) || [];
  const total = list.length;
  const enabled = list.filter((a) => a.enabled).length;
  const withProxy = list.filter((a) => a.proxyUrl).length;
  const alerts = [];
  if (total > 0 && withProxy < total) {
    alerts.push(`有 ${total - withProxy} 个账号没有配代理：Token Harbor 按出口地区鉴权，大陆直连会被 403 region_blocked 拒绝。`);
  }
  const alert = $('#th-alert');
  alert.classList.toggle('hidden', alerts.length === 0);
  alert.innerHTML = alerts.map((a) => `<p class="error">${esc(a)}</p>`).join('');
  const cards = [
    ['账号总数', total, 'TokenHarbor'],
    ['已启用', enabled, '还需套餐/周用量有效才能调度'],
    ['Agent 周期等效总额', allowance?.totalEquivalentUsd == null ? '未知' : '$' + allowance.totalEquivalentUsd.toFixed(2), `已确认 Agent ${allowance?.agentAccounts ?? '—'} 个，含已禁用账号`],
    ['当前可调度周等效额度', allowance?.availableWeeklyEquivalentUsd == null ? '未知' : '$' + allowance.availableWeeklyEquivalentUsd.toFixed(2), `周用量已测 ${allowance?.measuredWeeklyAccounts ?? '—'} / ${allowance?.agentAccounts ?? '—'} 个 Agent`],
    ['配了代理', withProxy, withProxy < total ? '其余的直连会被地区拦' : '全部按出口调度'],
    ['24h 请求', overview?.stats?.total?.r || 0, '最近 24 小时'],
  ];
  $('#th-quota-basis').textContent = allowance ? allowance.basis + ` 套餐未知 ${allowance.unknownAccounts} 个，非 Agent ${allowance.otherPlanAccounts} 个（不套用20/5）。Agent 最早检测 ${allowance.oldestAgentObservationAt ? fmtTime(allowance.oldestAgentObservationAt, true) : '未知'}。后台按账号自己的周窗口同步；此处读取缓存，单账号“刷新模型/套餐”可立即检测。` : '套餐/额度读取失败，未按满额或零余额推算。';
  $('#th-cards').innerHTML = cards.map(([k, v, s]) =>
    `<div class="card"><div class="card-k">${esc(k)}</div><div class="card-v">${esc(String(v))}</div><div class="card-s">${esc(s)}</div></div>`).join('');
}

// ---- 注册机 ----
async function loadTHRegistrations() {
  stopTHPolling();
  if (!thLog.started) thLogShow();
  loadTHProxies().catch(() => { $('#th-proxies-status').textContent = '已保存代理池读取失败，请刷新重试'; });
  const generation = ++thState.generation;
  const poll = async () => {
    try {
      const out = await api('GET', '/api/tokenharbor/registrations');
      if (generation !== thState.generation) return;
      thState.jobs = out.jobs || [];
      let job = thState.jobs.find((j) => j.id === thState.job);
      if (!job) {
        job = thState.jobs.find((j) => j.running) || thState.jobs[0];
        thState.job = job?.id || '';
        thState.selected.clear();
        thState.batchMessage = '';
      }
      renderTHJobs();
      if (job) renderTHJob(job);
      else $('#th-register-detail').classList.add('hidden');
      $('#th-register-poll-error').textContent = '';
    } catch (e) {
      if (generation === thState.generation) $('#th-register-poll-error').textContent = '任务获取失败：' + thSafe(e.message);
    } finally {
      if (generation === thState.generation && currentPage === 'thregister') thState.timer = setTimeout(poll, 3000);
    }
  };
  await poll();
}

function stopTHPolling() {
  thState.generation++;
  if (thState.timer) clearTimeout(thState.timer);
  thState.timer = null;
}

function renderTHJobs() {
  const jobs = thState.jobs;
  $('#th-register-jobs').innerHTML = jobs.map((job) => {
    const c = job.counts || {};
    const parts = Object.entries(c).filter(([, n]) => n > 0).map(([k, n]) => `${TH_STATUS[k] ? TH_STATUS[k][0] : k} ${n}`).join(' · ');
    return `<button type="button" ${thState.batchBusy || thState.batchPrompt ? 'disabled' : ''} class="pc-register-job ${job.id === thState.job ? 'active' : ''}" data-job="${esc(job.id)}" aria-pressed="${job.id === thState.job}"><span>${esc(job.id)}</span><span class="muted small">${esc(fmtTime(job.createdAt, true))} · ${job.running ? '进行中' : '已结束'} · ${esc(parts || '无账号')}</span></button>`;
  }).join('') || '<p class="muted">暂无任务。</p>';
}

function renderTHJob(job) {
  const rows = job.rows || [];
  const imported = rows.filter((r) => r.status === 'imported').length;
  for (const [filter, label, count] of [['pending', '待处理', rows.length - imported], ['imported', '已入池', imported]]) {
    const tab = $('#th-filter-' + filter);
    const active = thState.filter === filter;
    tab.textContent = `${label}（${count}）`;
    tab.classList.toggle('primary', active);
    tab.setAttribute('aria-selected', String(active));
    tab.disabled = thState.batchBusy || thState.batchPrompt || thState.pending.size > 0;
  }
  thState.rows = rows.filter((r) => (r.status === 'imported') === (thState.filter === 'imported'));
  const ids = new Set(thState.rows.map((r) => r.id));
  for (const id of thState.selected) if (!ids.has(id)) thState.selected.delete(id);
  $('#th-register-detail').classList.remove('hidden');
  $('#th-register-job-title').textContent = '任务 ' + job.id;
  $('#th-register-job-status').textContent = job.running ? '进行中' : '已结束';
  $('#th-register-rows').innerHTML = thState.rows.map((r) => `<tr>
    <td><input type="checkbox" class="th-row-select" data-id="${esc(r.id)}" aria-label="选择 ${esc(r.email)}" ${thState.selected.has(r.id) ? 'checked' : ''} ${thRowLocked(r.id) || ['queued', 'running'].includes(r.status) ? 'disabled' : ''}></td>
    <td>${esc(r.email)}</td>
    <td>${thLabel(r.status)}</td>
    <td><span class="small">${esc(r.proxy || '直连')}</span> ${!['running', 'queued'].includes(r.status) ? `<button type="button" class="btn small th-row-proxy" data-id="${esc(r.id)}" ${thRowLocked(r.id) ? 'disabled' : ''}>改代理</button>` : ''}</td>
    <td><span class="small mono">${esc(r.keyMasked || '—')}</span></td>
    <td class="small">${thSubscriptionLabel(r)}<br>${thSafe(r.detail)}${r.verified ? ' · 已验证邮箱' : ''}${r.freeModels ? ' · 免费模型已开' : ''}</td>
    <td>${thRowActions(r)}</td>
  </tr>`).join('') || `<tr><td colspan="7" class="empty">${thState.filter === 'imported' ? '本任务暂无已入池账号' : '本任务暂无待处理账号；已入池记录请切换「已入池」查看'}</td></tr>`;
  thUpdateSelection();
}

function thSubscriptionLabel(r) {
  const p = r.subscription || {};
  if (r.status === 'saved') return '<span class="muted">未注册，尚未检测套餐</span>';
  const label = p.status === 'active' ? `${p.name || p.tier}（上次检测有效）` : p.status === 'free' ? 'Free · 未订阅或已到期，待付款' : '套餐未知';
  return `<span class="badge ${p.status === 'active' ? 'ok' : 'warn'}">${esc(label)}</span>${p.checkedAt && !p.checkedAt.startsWith('0001-') ? ` <span class="muted">${esc(fmtTime(p.checkedAt, true))}</span>` : ''}${p.error ? `<br>${thSafe(p.error)}` : ''}`;
}

function thRowActions(r) {
  const out = [];
  const button = (kind, text, primary = false) => `<button type="button" class="btn small ${primary ? 'primary' : ''} th-row-${kind}" data-id="${esc(r.id)}" ${thRowLocked(r.id) ? 'disabled' : ''}>${esc(text)}</button>`;
  if (r.status === 'saved') out.push(button('retry', '开始注册', true));
  if (thBatchEligible('subscription', r)) out.push(button('subscription', '检测套餐'));
  if (r.status === 'unverified' || (r.status === 'registered' && !r.verified)) out.push(button('reverify', '补验证'));
  if (thBatchEligible('checkout', r)) out.push(button('checkout', 'Agent Pass 链接'));
  if (thBatchEligible('import', r)) out.push(button('import', '校验并入池', true));
  if (r.status === 'imported' && r.accountId) out.push(button('return', '退回注册机'));
  if (r.status === 'failed' || r.status === 'uncertain') out.push(button('retry', r.hasSession ? '继续（保留会话）' : '重试'));
  if (!['running', 'queued'].includes(r.status)) out.push(button('delete', '删除'));
  return out.join(' ');
}

const thRandomPassword = () => {
  // 16 位，至少含大小写和数字；不追求可读性，只求不撞规则。
  const alphabet = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789-_';
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  const body = Array.from(bytes, (b) => alphabet[b % alphabet.length]).join('');
  return 'Tq7-' + body;
};

function thParseAccounts(text) {
  const out = [];
  for (const raw of text.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line) continue;
    const parts = line.split('----');
    if (parts.length > 3) throw new Error('每行最多三段：邮箱----密码----接码页');
    const email = (parts[0] || '').trim();
    if (!email.includes('@')) throw new Error(`邮箱格式有误：${email.slice(0, 40)}`);
    const twoPartMailbox = parts.length === 2 && parts[1].trim().startsWith('https://');
    out.push({ email,
      password: (twoPartMailbox ? '' : (parts[1] || '').trim()) || thRandomPassword(),
      mailboxUrl: (twoPartMailbox ? parts[1] : (parts[2] || '')).trim() });
  }
  return out;
}

function thParseProxies(text) {
  return text.split(/\r?\n/).map((l) => l.trim()).filter(Boolean).map((line) => {
    const parts = line.split('----');
    if (parts.length > 2 || parts.some((p) => !p.trim())) throw new Error('代理格式有误：每行一个地址，或 标签----地址');
    return parts[parts.length - 1].trim();
  });
}

async function thSubmitRegistration(ev, saveOnly = false) {
  ev.preventDefault();
  const error = $('#th-register-error');
  error.textContent = '';
  if (thState.busy || thState.batchBusy || thState.batchPrompt) return;
  let accounts, proxies;
  try {
    accounts = thParseAccounts($('#th-register-accounts').value);
    if (!accounts.length || accounts.length > 100) throw new Error('请填写 1-100 个邮箱');
    proxies = thParseProxies($('#th-register-proxies').value);
  } catch (e) {
    error.textContent = e.message;
    return;
  }
  const btn = $('#th-register-submit');
  const saveBtn = $('#th-register-save');
  thState.busy = true;
  btn.disabled = saveBtn.disabled = true;
  try {
    const out = await api('POST', '/api/tokenharbor/registrations', {
      accounts,
      proxies,
      group: $('#th-register-group').value.trim() || 'tokenharbor',
      saveOnly,
      proxyMode: $('#th-register-proxy-mode').value,
      solveCaptcha: $('#th-register-solve').checked,
      concurrency: Number($('#th-register-conc').value) || 0,
    });
    if (!out || !out.id) throw new Error('后台没有返回任务 ID，请先刷新任务列表确认');
    $('#th-register-accounts').value = '';
    $('#th-register-proxies').value = '';
    thState.job = out.id;
    thLogShow();
    if (currentPage === 'thregister') loadTHRegistrations();
  } catch (e) {
    error.textContent = thSafe(e.message) + '。请求结果不确定时，请先刷新任务列表确认任务是否已创建。';
  } finally {
    thState.busy = false;
    btn.disabled = saveBtn.disabled = false;
  }
}

async function loadTHProxies() {
  const out = await api('GET', '/api/tokenharbor/proxies');
  $('#th-proxies-status').textContent = out.count ? `已加密保存 ${out.count} 个代理；输入留空时自动复用。认证信息不回显。` : '尚未保存代理池；输入留空时使用设置中的默认代理。';
}

async function thSaveProxies(clear = false) {
  const status = $('#th-proxies-status');
  const button = $(clear ? '#th-clear-proxies' : '#th-save-proxies');
  button.disabled = true;
  try {
    const proxies = clear ? [] : thParseProxies($('#th-register-proxies').value);
    if (!clear && !proxies.length) throw new Error('请先填写要保存的代理');
    if (clear && !window.confirm('清除已保存代理池？不会修改已有账号绑定的代理。')) return;
    await api('PUT', '/api/tokenharbor/proxies', { proxies });
    $('#th-register-proxies').value = '';
    await loadTHProxies();
  } catch (e) {
    status.textContent = '保存代理池失败：' + e.message;
  } finally {
    button.disabled = false;
  }
}

async function thEditProxy({ title, name, currentProxy, hint, save }) {
  return modal({
    title,
    body: `<div class="form">
      <p class="muted small">${esc(name)}</p>
      <p class="small">当前代理：${esc(currentProxy || '直连')}（认证信息不回显）</p>
      <p class="muted small">${esc(hint)} 代理失败不会自动直连；更换出口可能导致官网会话需要重新验证。</p>
      <label for="th-new-proxy">新代理地址</label>
      <input id="th-new-proxy" type="password" autocomplete="off" placeholder="http(s)://用户:密码@主机:端口 或 socks5(h)://…">
      <label><input id="th-proxy-direct" type="checkbox"> 明确清除代理（直连）</label>
    </div>`,
    buttons: [{ label: '取消', value: 'cancel' }, { label: '保存', value: 'save', primary: true, action: async () => {
      const direct = $('#th-proxy-direct').checked;
      const proxy = $('#th-new-proxy').value.trim();
      if (!direct && !proxy) throw new Error('请填写代理地址，或勾选清除代理');
      await save(direct ? '' : proxy);
    } }],
  });
}

async function thRowProxy(id) {
  const row = thState.rows.find((r) => r.id === id);
  if (!row || ['running', 'queued'].includes(row.status)) throw new Error('当前账号正在执行任务，不能修改代理');
  const choice = await thEditProxy({
    title: '修改注册机账号代理', name: row.email, currentProxy: row.proxy,
    hint: '仅修改此注册记录，不影响已入池账号。新建入池账号时默认沿用这里的代理；修改后需重新检测注册机套餐。',
    save: (proxyUrl) => api('PUT', '/api/tokenharbor/registrations/' + encodeURIComponent(id) + '/proxy', { proxyUrl }),
  });
  if (choice === 'save') toast('注册机代理已保存，号池代理不变');
  if (currentPage === 'thregister') loadTHRegistrations();
}

async function thPoolProxy(account) {
  const choice = await thEditProxy({
    title: '修改号池账号代理', name: account.name || account.id, currentProxy: account.proxyUrl,
    hint: '仅修改此号池账号，不影响注册机原记录。旧套餐/周用量及会话绑定将失效，后台按新代理重新检测，检测通过前暂停调度。',
    save: (proxyUrl) => api('PUT', '/api/accounts/' + encodeURIComponent(account.id), { proxyUrl, clearProxy: proxyUrl === '' }),
  });
  if (choice === 'save') toast('号池代理已保存，注册机代理不变');
}

async function thRowAction(kind, id) {
  const err = $('#th-register-import-error');
  err.textContent = '';
  if (kind === 'reverify') err.textContent = '';
  try {
    if (kind === 'return') {
      const row = thState.rows.find((r) => r.id === id);
      if (!row?.accountId) throw new Error('此记录没有关联的号池账号');
      return await thReturnToRegistration(row.accountId, row.email);
    }
    if (kind === 'import') {
      await api('POST', '/api/tokenharbor/registrations/' + encodeURIComponent(id) + '/import', { group: $('#th-register-group').value.trim() || 'tokenharbor' });
    } else if (kind === 'subscription') {
      err.textContent = '正在检测当前有效套餐…';
      await api('POST', '/api/tokenharbor/registrations/' + encodeURIComponent(id) + '/subscription', {});
      err.textContent = '';
    } else if (kind === 'retry') {
      await api('POST', '/api/tokenharbor/registrations/' + encodeURIComponent(id) + '/retry', { solveCaptcha: $('#th-register-solve').checked, concurrency: Number($('#th-register-conc').value) || 0 });
    } else if (kind === 'delete') {
      if (!window.confirm('确认删除此注册记录及已保存的会话、API Key、支付链接？此操作不删除已入池账号。')) return;
      await api('DELETE', '/api/tokenharbor/registrations/' + encodeURIComponent(id));
    } else if (kind === 'reverify') {
      thLogShow();
      err.textContent = '正在查找验证邮件…';
      await api('POST', '/api/tokenharbor/registrations/' + encodeURIComponent(id) + '/reverify', {});
      err.textContent = '';
    }
  } catch (e) {
    err.textContent = thSafe(e.message);
  }
  if (currentPage === 'thregister') loadTHRegistrations();
}

// 退回只移动现有资料，不重新注册、不创建或撤销上游 Key。
async function thReturnToRegistration(accountID, name) {
  return thGuardRow('return:' + accountID, async () => {
    const choice = await modal({ title: '退回 TokenHarbor 注册机', body: `<p>将 <strong>${esc(name || accountID)}</strong> 移出号池，停止后续调度；原注册会话、Key、接码资料和代理绑定均保留。</p><p>不会重新注册或付款。之后重新入池仍需校验当前有效套餐。仅支持有原注册记录的账号。</p>`, buttons: [{ label: '取消', value: 'cancel' }, { label: '确认退回', value: 'return', primary: true }] });
    if (choice !== 'return') return false;
    const row = await api('POST', '/api/accounts/' + encodeURIComponent(accountID) + '/return-to-registration', {});
    thState.job = row.attempt;
    thState.filter = 'pending';
    thState.selected.clear();
    thState.batchMessage = '';
    toast('已移出号池并退回原注册任务，资料已保留');
    go('thregister');
    return true;
  });
}

// 批量操作只复用既有单行接口，不另外创建订单/注册重试机制。
function thRowLocked(id) {
  return thState.batchBusy || thState.batchPrompt || thState.pending.has(id);
}

function thBatchEligible(kind, row) {
  if (kind === 'import') return row.status === 'registered' && row.verified && row.subscription?.status === 'active';
  if (kind === 'retry') return ['saved', 'failed', 'uncertain'].includes(row.status);
  return !!row.hasSession && !['saved', 'running', 'queued'].includes(row.status);
}

function thRenderCurrentJob() {
  const job = thState.jobs.find((j) => j.id === thState.job);
  if (job) renderTHJob(job);
  renderTHJobs();
}

function thUpdateSelection() {
  const selectable = thState.rows.filter((r) => !['queued', 'running'].includes(r.status));
  const count = selectable.filter((r) => thState.selected.has(r.id)).length;
  const all = $('#th-select-all');
  all.checked = selectable.length > 0 && count === selectable.length;
  all.indeterminate = count > 0 && count < selectable.length;
  all.disabled = thState.batchBusy || thState.batchPrompt || thState.pending.size > 0 || selectable.length === 0;
  $('#th-selected-count').textContent = `已选 ${thState.selected.size} 个（仅本任务）`;
  for (const [kind, id] of [['retry', 'register'], ['subscription', 'subscription'], ['checkout', 'checkout'], ['import', 'import']]) {
    $(`#th-batch-${id}`).disabled = thState.batchBusy || thState.batchPrompt || thState.pending.size > 0 || !thState.rows.some((r) => thState.selected.has(r.id) && thBatchEligible(kind, r));
  }
  $('#th-batch-progress').textContent = thState.batchMessage;
  for (const id of ['#th-register-save', '#th-register-submit']) $(id).disabled = thState.busy || thState.batchBusy || thState.batchPrompt;
}

async function thGuardRow(id, action) {
  if (thRowLocked(id)) return;
  thState.pending.add(id);
  thRenderCurrentJob();
  try {
    return await action();
  } finally {
    thState.pending.delete(id);
    thRenderCurrentJob();
  }
}

async function thBatchCall(kind, row, options) {
  const path = '/api/tokenharbor/registrations/' + encodeURIComponent(row.id);
  if (kind === 'retry') return api('POST', path + '/retry', options);
  if (kind === 'subscription') return api('POST', path + '/subscription', {});
  if (kind === 'import') return api('POST', path + '/import', options);
  if (kind === 'checkout') {
    const out = row.hasCheckout ? await api('GET', path + '/checkout') : await api('POST', path + '/checkout', { kind: 'pass', product: 'starter_pass_monthly' });
    thCheckoutURL(out.url);
    return out;
  }
  throw new Error('未知批量操作');
}

// Requests are bounded, partial failures are final, and none are retried.
async function thBatchRun(kind, rows, onResult = () => {}) {
  if (thState.batchBusy || thState.pending.size) throw new Error('已有操作执行中，请等待完成');
  thState.batchBusy = true;
  const options = kind === 'import'
    ? { group: $('#th-register-group').value.trim() || 'tokenharbor' }
    : { solveCaptcha: $('#th-register-solve').checked, concurrency: Number($('#th-register-conc').value) || 0 };
  const counts = { success: 0, failed: 0, skipped: 0 };
  let next = 0;
  const progress = () => {
    const done = counts.success + counts.failed + counts.skipped;
    thState.batchMessage = `已处理 ${done}/${rows.length} · ${kind === 'retry' ? '已排队' : '成功'} ${counts.success} · 失败 ${counts.failed} · 跳过 ${counts.skipped}${done === rows.length ? '（完成；未自动重试）' : ''}`;
    thUpdateSelection();
  };
  const worker = async () => {
    while (next < rows.length) {
      const row = rows[next++];
      let result;
      if (!thBatchEligible(kind, row)) {
        result = { row, status: 'skipped', error: '当前状态不适用，已跳过' };
      } else {
        try {
          result = { row, status: 'success', value: await thBatchCall(kind, row, options) };
        } catch (e) {
          result = { row, status: 'failed', error: e.message || '请求失败，请核对账号后人工处理' };
        }
      }
      counts[result.status]++;
      onResult(result);
      progress();
    }
  };
  thRenderCurrentJob();
  progress();
  try {
    await Promise.all(Array.from({ length: Math.min(3, rows.length) }, worker));
    return counts;
  } finally {
    thState.batchBusy = false;
    thRenderCurrentJob();
    if (currentPage === 'thregister') loadTHRegistrations();
  }
}

async function thBatchAction(kind) {
  if (thState.batchBusy || thState.batchPrompt || thState.pending.size) return;
  const rows = thState.rows.filter((r) => thState.selected.has(r.id));
  if (!rows.length) return;
  thState.batchPrompt = true;
  thRenderCurrentJob();
  try {
    if (kind === 'checkout') return await thBatchCheckout(rows);
    if (kind === 'import') {
      const eligible = rows.filter((r) => thBatchEligible(kind, r)).length;
      const group = $('#th-register-group').value.trim() || 'tokenharbor';
      const choice = await modal({ title: '校验并入池所选账号', body: `<p>将对 ${eligible} 个邮箱已验证、上次检测套餐有效的账号重新检测当前套餐，通过后加入分组 <strong>${esc(group)}</strong>；其余记录跳过。</p><p>不会注册、付款或自动重试；套餐失效或检测失败的账号不会入池。</p>`, buttons: [{ label: '取消', value: 'cancel' }, { label: '确认校验并入池', value: 'import', primary: true }] });
      if (choice !== 'import') return;
    }
    if (kind === 'retry') {
      const eligible = rows.filter((r) => thBatchEligible(kind, r)).length;
      const choice = await modal({ title: '注册所选账号', body: `<p>将注册/继续 ${eligible} 个账号，其余状态不适用的记录跳过。复用已绑定代理和已有会话，不自动入池。</p><p>${$('#th-register-solve').checked ? '已开启自动解 Turnstile：需要时会产生验证码服务费用。' : '未开启自动解验证码。'}</p>`, buttons: [{ label: '取消', value: 'cancel' }, { label: '确认注册', value: 'start', primary: true }] });
      if (choice !== 'start') return;
    }
    const details = [];
    await thBatchRun(kind, rows, (result) => {
      if (result.status !== 'success') details.push(`${result.row.email}：${result.error}`);
    });
    $('#th-register-import-error').textContent = details.join('\n');
  } finally {
    thState.batchPrompt = false;
    thRenderCurrentJob();
  }
}

async function thBatchCheckout(rows) {
  let started = false;
  let running = false;
  // URLs stay in this explicit modal's closure, never registration state/logs.
  const links = [];
  await modal({ title: '批量获取 Agent Pass 订阅链接（不会付款）', wide: true, body: `<p>已选 ${rows.length} 个账号，仅处理已保存会话的空闲记录；已有链接复用，不确定的订单不重复创建。</p><p class="muted small">只获取月度单次托管链接，不自动续费、不代付，也不会自动打开多个页面。每个账号的实际价格以 Stripe 为准。</p><p id="th-batch-checkout-progress" role="status"></p><div id="th-batch-checkout-results"></div><button type="button" class="btn hidden" id="th-batch-copy-links">复制全部成功链接</button>`, buttons: [{ label: '关闭', value: 'close' }, { label: '确认获取链接', value: 'create', primary: true, action: async () => {
    if (started) return false;
    started = running = true;
    const close = $('#modal-actions button[value="close"]');
    close.disabled = true;
    let done = 0;
    try {
      await thBatchRun('checkout', rows, (result) => {
        const section = document.createElement('section');
        const heading = document.createElement('p');
        heading.textContent = result.row.email;
        section.append(heading);
        const body = document.createElement('div');
        if (result.status === 'success') {
          thCheckoutLink(result.value, body);
          links.push(`${result.row.email}\t${result.value.url}`);
        } else body.textContent = (result.status === 'skipped' ? '跳过：' : '失败：') + result.error;
        section.append(body);
        $('#th-batch-checkout-results').append(section);
        $('#th-batch-checkout-progress').textContent = `已处理 ${++done}/${rows.length}；成功 ${links.length}`;
      });
      const copy = $('#th-batch-copy-links');
      copy.classList.toggle('hidden', links.length === 0);
      copy.onclick = () => copyText(links.join('\n'));
    } finally {
      running = false;
      close.disabled = false;
    }
    return false;
  } }], onOpen: (dlg) => {
    const preventClose = (e) => { if (running) e.preventDefault(); };
    dlg.addEventListener('cancel', preventClose);
    dlg._onClose = () => {
      dlg.removeEventListener('cancel', preventClose);
      links.length = 0;
      $('#th-batch-checkout-results')?.replaceChildren();
      const copy = $('#th-batch-copy-links');
      if (copy) copy.onclick = null;
    };
  } });
}

// 支付链接只在显式打开的弹窗中读取，不进入列表或实时日志。
function thCheckoutURL(raw) {
  let u;
  try { u = new URL(raw); } catch { throw new Error('支付链接不可信'); }
  if (u.protocol !== 'https:' || u.host !== 'checkout.stripe.com' || u.username || u.password || !u.pathname.startsWith('/c/pay/')) throw new Error('支付链接不可信');
  return u;
}

function thCheckoutLink(out, root) {
  const u = thCheckoutURL(out.url);
  root.replaceChildren();
  const note = document.createElement('p');
  note.textContent = '链接已加密保存。未付款；金额、优惠及有效期以 Stripe 收银台显示为准。';
  const link = document.createElement('a');
  link.href = u.href; link.target = '_blank'; link.rel = 'noopener noreferrer'; link.referrerPolicy = 'no-referrer';
  link.textContent = '打开 Stripe 支付页面（需手动付款）';
  const copy = document.createElement('button');
  copy.type = 'button'; copy.className = 'btn small'; copy.textContent = '复制链接';
  copy.onclick = () => copyText(u.href);
  root.append(note, link, document.createTextNode(' '), copy);
}

async function thRowCheckout(id) {
  const row = thState.rows.find((r) => r.id === id);
  const path = '/api/tokenharbor/registrations/' + encodeURIComponent(id) + '/checkout';
  if (row?.hasCheckout) {
    const out = await api('GET', path);
    await modal({ title: '已保存的支付链接', body: '<div id="th-checkout-result"></div>', onOpen: () => thCheckoutLink(out, $('#th-checkout-result')) });
    return;
  }
  let created = false;
  await modal({ title: 'Agent Pass 支付链接（不会付款）', body: `<div class="form">
    <p class="muted small">${esc(row?.email || id)}</p>
    <p>Agent Pass · Monthly (one-time)，官网首月优惠以收银台显示为准。</p>
    <p class="muted small">仅支持 Free 账号新购。不开启自动续费、不记住银行卡；遇到地区确认或订阅升级报价即停止。预检失败可重试；创建结果不确定时须先核对官网账单。</p>
    <label class="muted small"><input id="th-checkout-confirm-no-order" type="checkbox"> 已核对账单：确认未付款且无待处理订单，允许恢复上次不确定的链接获取（无需恢复时不要勾选）</label>
    <div id="th-checkout-result"></div></div>`, buttons: [{ label: '关闭', value: 'close' }, { label: '获取链接', value: 'create', primary: true, action: async () => {
      if (created) return false;
      const confirmation = $('#th-checkout-confirm-no-order');
      const confirmNoPendingOrder = confirmation.checked;
      confirmation.checked = false; // Every uncertain recovery needs a fresh explicit confirmation.
      const out = await api('POST', path, { kind: 'pass', product: 'starter_pass_monthly', confirmNoPendingOrder });
      created = true;
      $('#modal-error').textContent = '';
      thCheckoutLink(out, $('#th-checkout-result'));
      return false;
    } }] });
  if (currentPage === 'thregister') loadTHRegistrations();
}

// ---- 设置 ----
async function loadTHSettings() {
  const out = await api('GET', '/api/tokenharbor/settings');
  const th = out.tokenHarbor || {};
  $('#th-set-base').value = th.baseUrl || '';
  $('#th-set-proxy').value = '';
  $('#th-set-proxy').placeholder = out.proxyLabel && out.proxyLabel !== '直连' ? '已保存：' + out.proxyLabel + '（留空不修改）' : 'http://用户:密码@主机:端口（留空不修改）';
  $('#th-set-captcha').value = '';
  $('#th-set-captcha-hint').textContent = out.hasCaptcha ? '已保存一个 Key，页面不会回显；留空表示不修改。' : '还没有保存 captcha-run Key：注册时无法自动解 Turnstile。';
  $('#th-set-label').value = th.keyLabel || 'pool';
  $('#th-set-group').value = th.group || 'tokenharbor';
  $('#th-set-conc').value = th.registerConcurrency || 2;
  $('#th-set-mail').value = th.mailTimeoutSec || 600;
  const conc = $('#th-register-conc');
  if (conc) conc.value = th.registerConcurrency || 2;
  $('#th-set-save').dataset.revision = String(out.revision || 0);
}

async function thSaveSettings(ev) {
  ev.preventDefault();
  const btn = $('#th-set-save');
  const msg = $('#th-set-message');
  msg.textContent = '';
  const proxyRaw = $('#th-set-proxy').value.trim();
  const body = {
    tokenHarbor: {
      baseUrl: $('#th-set-base').value.trim() || 'https://tokenharbor.ai/v1',
      proxyUrl: proxyRaw,
      captchaKey: $('#th-set-captcha').value.trim(),
      keyLabel: $('#th-set-label').value.trim() || 'pool',
      group: $('#th-set-group').value.trim() || 'tokenharbor',
      registerConcurrency: Number($('#th-set-conc').value) || 2,
      mailTimeoutSec: Number($('#th-set-mail').value) || 600,
    },
    expectedRevision: Number(btn.dataset.revision || 0),
  };
  btn.disabled = true;
  try {
    const out = await api('PUT', '/api/tokenharbor/settings', body);
    msg.textContent = '已保存';
    if (out && out.revision !== undefined) btn.dataset.revision = String(out.revision);
    $('#th-set-captcha').value = '';
    $('#th-set-proxy').value = '';
    loadTHSettings().catch(() => {});
  } catch (e) {
    msg.textContent = thSafe(e.message);
  } finally {
    btn.disabled = false;
  }
}

// ---- 右下角注册日志 ----
function thLogShow() {
  thLog.visible = thLog.started = true;
  $('#th-logbox').classList.remove('hidden');
  $('#th-log-open').classList.add('hidden');
  if (!thLog.timer && !thLog.inFlight) thLogPoll();
}

function thLogHide() {
  thLog.visible = false;
  $('#th-logbox').classList.add('hidden');
  $('#th-log-open').classList.remove('hidden');
  // 收起不动轮询状态：再打开时从上次的 seq 继续，不丢中间的行。
}

async function thLogPoll() {
  if (thLog.inFlight) return;
  if (thLog.timer) clearTimeout(thLog.timer);
  thLog.timer = null;
  thLog.inFlight = true;
  try {
    const out = await api('GET', '/api/tokenharbor/log?after=' + thLog.seq);
    const entries = [];
    // A restarted server has a new in-memory sequence; allow its new log.
    if (Number(out?.seq) < thLog.seq) thLog.seq = 0;
    for (const e of (out && out.entries) || []) {
      if (e.seq > thLog.seq) {
        entries.push(e);
        thLog.seq = e.seq;
      }
    }
    thLog.seq = Math.max(thLog.seq, Number(out?.seq) || 0);
    if (entries.length) {
      for (const e of entries) thLog.lines.push(e);
      if (thLog.lines.length > 400) thLog.lines = thLog.lines.slice(-400);
      if (!thLog.paused) thLogRender();
    }
    thLogStatus(entries.length);
  } catch (e) {
    $('#th-log-status').textContent = '日志获取失败：' + thSafe(e.message);
  } finally {
    thLog.inFlight = false;
    thLog.timer = setTimeout(thLogPoll, 1500);
  }
}

function thLogStatus(newLines) {
  const body = $('#th-log-body');
  if (body.querySelector('.muted')) { /* 首次渲染前占位 */ }
  $('#th-log-status').textContent = thLog.paused ? '已暂停（仍在累积）' : (newLines ? '接收中' : '等待新日志');
  $('#th-log-count').textContent = thLog.lines.length ? `共 ${thLog.lines.length} 行` : '';
}

function thLogRender() {
  const body = $('#th-log-body');
  const atBottom = body.scrollHeight - body.scrollTop - body.clientHeight < 24;
  body.innerHTML = thLog.lines.map((e) => {
    const t = new Date(e.ts);
    const hh = String(t.getHours()).padStart(2, '0');
    const mm = String(t.getMinutes()).padStart(2, '0');
    const ss = String(t.getSeconds()).padStart(2, '0');
    return `<div class="th-log-line ${esc(e.level || 'info')}"><span class="t">${hh}:${mm}:${ss}</span><span class="e" title="${esc(e.email)}">${esc(e.email)}</span><span class="s">${esc(e.stage)}</span><span class="d">${thSafe(e.detail)}</span></div>`;
  }).join('') || '<p class="muted small">暂无日志。</p>';
  if (atBottom) body.scrollTop = body.scrollHeight;
}

// ---- 事件 ----
function thInstall() {
  const on = (sel, ev, fn) => { const el = $(sel); if (el) el.addEventListener(ev, fn); };
  on('#btn-th-refresh', 'click', () => loadTHOverview().catch((e) => toast(e.message)));
  on('#btn-th-goregister', 'click', () => go('thregister'));
  on('#th-reg-refresh', 'click', () => loadTHRegistrations());
  on('#th-registration-form', 'submit', (e) => thSubmitRegistration(e));
  on('#th-register-save', 'click', (e) => thSubmitRegistration(e, true));
  on('#th-save-proxies', 'click', () => thSaveProxies());
  on('#th-clear-proxies', 'click', () => thSaveProxies(true));
  on('#th-settings-form', 'submit', thSaveSettings);
  for (const filter of ['pending', 'imported']) on('#th-filter-' + filter, 'click', () => {
    if (thState.batchBusy || thState.batchPrompt || thState.pending.size || thState.filter === filter) return;
    thState.filter = filter;
    thState.selected.clear();
    thState.batchMessage = '';
    $('#th-register-import-error').textContent = '';
    thRenderCurrentJob();
  });
  on('#th-select-all', 'change', (e) => {
    if (thState.batchBusy || thState.batchPrompt || thState.pending.size) return;
    for (const row of thState.rows) {
      if (['queued', 'running'].includes(row.status)) continue;
      if (e.target.checked) thState.selected.add(row.id); else thState.selected.delete(row.id);
    }
    thRenderCurrentJob();
  });
  on('#th-register-rows', 'change', (e) => {
    const checkbox = e.target.closest('.th-row-select');
    if (!checkbox || thState.batchBusy || thState.batchPrompt || thState.pending.size) return;
    if (checkbox.checked) thState.selected.add(checkbox.dataset.id); else thState.selected.delete(checkbox.dataset.id);
    thUpdateSelection();
  });
  for (const [id, kind] of [['register', 'retry'], ['subscription', 'subscription'], ['checkout', 'checkout'], ['import', 'import']]) {
    on('#th-batch-' + id, 'click', () => thBatchAction(kind).catch((e) => toast(e.message)));
  }
  on('#th-register-jobs', 'click', (e) => {
    const b = e.target.closest('[data-job]');
    if (!b || thState.batchBusy || thState.batchPrompt) return;
    if (thState.job !== b.dataset.job) {
      thState.selected.clear();
      thState.batchMessage = '';
    }
    thState.job = b.dataset.job;
    $('#th-register-import-error').textContent = '';
    loadTHRegistrations();
  });
  on('#th-log-open', 'click', thLogShow);
  on('#th-log-close', 'click', thLogHide);
  on('#th-log-clear', 'click', () => { thLog.lines = []; thLogRender(); thLogStatus(0); });
  on('#th-log-pause', 'click', (e) => {
    thLog.paused = !thLog.paused;
    e.currentTarget.textContent = thLog.paused ? '继续' : '暂停';
    if (!thLog.paused) thLogRender();
    thLogStatus(0);
  });
  on('#th-register-rows', 'click', (e) => {
    const proxy = e.target.closest('.th-row-proxy');
    if (proxy) { thGuardRow(proxy.dataset.id, () => thRowProxy(proxy.dataset.id)).catch((err) => toast(err.message)); return; }
    const checkout = e.target.closest('.th-row-checkout');
    if (checkout) { thGuardRow(checkout.dataset.id, () => thRowCheckout(checkout.dataset.id)).catch((err) => toast(err.message)); return; }
    for (const kind of ['import', 'return', 'retry', 'delete', 'reverify', 'subscription']) {
      const btn = e.target.closest('.th-row-' + kind);
      if (btn) { thGuardRow(btn.dataset.id, () => thRowAction(kind, btn.dataset.id)).catch((err) => toast(err.message)); return; }
    }
  });
}

// 脚本在 body 末尾加载，DOM 已就绪。
thInstall();
