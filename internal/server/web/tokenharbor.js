// TokenHarbor 号池管理台。与 app.js 共用 $ / api / esc / modal，加载顺序在 app.js 之后。
// 账号凭证是 thk_live_ Key；页面只显示掩码，从不回显密码、Cookie 或完整 Key。

const TH_STATUS = {
  queued: ['排队中', 'accent'],
  running: ['注册中', 'accent'],
  registered: ['已注册', 'ok'],
  unverified: ['待验证', 'warn'],
  imported: ['已入池', 'ok'],
  failed: ['失败', 'bad'],
  uncertain: ['中断', 'warn'],
};

const thState = { job: '', jobs: [], rows: [], busy: false, timer: null, generation: 0 };
// 右下角注册日志：轮询增量，暂停时只累积不渲染。
const thLog = { seq: 0, lines: [], timer: null, paused: false, visible: false };

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
  const [accounts, overview] = await Promise.all([
    api('GET', '/api/accounts?provider=tokenharbor').catch(() => null),
    api('GET', '/api/overview?provider=tokenharbor').catch(() => null),
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
    ['已启用', enabled, '可调度'],
    ['配了代理', withProxy, withProxy < total ? '其余的直连会被地区拦' : '全部按出口调度'],
    ['24h 请求', overview?.stats?.total?.r || 0, '最近 24 小时'],
  ];
  $('#th-cards').innerHTML = cards.map(([k, v, s]) =>
    `<div class="card"><div class="card-k">${esc(k)}</div><div class="card-v">${esc(String(v))}</div><div class="card-s">${esc(s)}</div></div>`).join('');
}

// ---- 注册机 ----
async function loadTHRegistrations() {
  stopTHPolling();
  thLogShow();
  const generation = ++thState.generation;
  const poll = async () => {
    try {
      const out = await api('GET', '/api/tokenharbor/registrations');
      if (generation !== thState.generation) return;
      thState.jobs = out.jobs || [];
      renderTHJobs();
      if (thState.job) {
        const job = thState.jobs.find((j) => j.id === thState.job);
        if (job) renderTHJob(job);
        else $('#th-register-detail').classList.add('hidden');
      } else {
        $('#th-register-detail').classList.add('hidden');
      }
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
    return `<button type="button" class="pc-register-job ${job.id === thState.job ? 'active' : ''}" data-job="${esc(job.id)}" aria-pressed="${job.id === thState.job}"><span>${esc(job.id)}</span><span class="muted small">${esc(fmtTime(job.createdAt, true))} · ${job.running ? '进行中' : '已结束'} · ${esc(parts || '无账号')}</span></button>`;
  }).join('') || '<p class="muted">暂无任务。</p>';
}

function renderTHJob(job) {
  $('#th-register-detail').classList.remove('hidden');
  $('#th-register-job-title').textContent = '任务 ' + job.id;
  $('#th-register-job-status').textContent = job.running ? '进行中' : '已结束';
  $('#th-register-rows').innerHTML = job.rows.map((r) => `<tr>
    <td>${esc(r.email)}</td>
    <td>${thLabel(r.status)}</td>
    <td><span class="small">${esc(r.proxy || '直连')}</span> ${r.status === 'queued' ? `<button type="button" class="btn small th-row-proxy" data-id="${esc(r.id)}">改代理</button>` : ''}</td>
    <td><span class="small mono">${esc(r.keyMasked || '—')}</span></td>
    <td class="small">${thSafe(r.detail)}${r.verified ? ' · 已验证邮箱' : ''}${r.freeModels ? ' · 免费模型已开' : ''}</td>
    <td>${thRowActions(r)}</td>
  </tr>`).join('') || '<tr><td colspan="6" class="empty">暂无账号</td></tr>';
}

function thRowActions(r) {
  const out = [];
  if (r.status === 'unverified' || (r.status === 'registered' && !r.verified)) {
    out.push(`<button type="button" class="btn small th-row-reverify" data-id="${esc(r.id)}">补验证</button>`);
  }
  if (r.status === 'registered') out.push(`<button type="button" class="btn small primary th-row-import" data-id="${esc(r.id)}">入池</button>`);
  if (r.status === 'failed' || r.status === 'uncertain') out.push(`<button type="button" class="btn small th-row-retry" data-id="${esc(r.id)}">重试</button>`);
  if (r.status !== 'running') out.push(`<button type="button" class="btn small ghost th-row-delete" data-id="${esc(r.id)}">删除</button>`);
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
    if (!email.includes('@')) throw new Error(`邮箱格式有误：${line.slice(0, 40)}`);
    out.push({ email, password: (parts[1] || '').trim() || thRandomPassword(), mailboxUrl: (parts[2] || '').trim() });
  }
  return out;
}

function thParseProxies(text) {
  return text.split(/\r?\n/).map((l) => l.trim()).filter(Boolean).map((line) => {
    const parts = line.split('----');
    if (parts.length > 2 || parts.some((p) => !p.trim())) throw new Error('代理格式有误：' + line.slice(0, 40));
    return parts[parts.length - 1].trim();
  });
}

async function thSubmitRegistration(ev) {
  ev.preventDefault();
  const error = $('#th-register-error');
  error.textContent = '';
  if (thState.busy) return;
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
  thState.busy = true;
  btn.disabled = true;
  try {
    const out = await api('POST', '/api/tokenharbor/registrations', {
      accounts,
      proxies,
      group: $('#th-register-group').value.trim() || 'tokenharbor',
      autoImport: $('#th-register-auto').checked,
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
    btn.disabled = false;
  }
}

async function thRowProxy(id) {
  const row = thState.rows.find((r) => r.id === id);
  modal({
    title: '修改注册代理',
    body: `<div class="form">
      <p class="muted small">${esc(row ? row.email : id)}</p>
      <label for="th-new-proxy">代理地址</label>
      <input id="th-new-proxy" type="password" autocomplete="off" placeholder="http://用户:密码@主机:端口 或 socks5://…">
      <label><input id="th-proxy-direct" type="checkbox"> 清除代理（直连）</label>
    </div>`,
    buttons: [{ label: '取消', value: 'cancel' }, { label: '保存', value: 'save', primary: true, action: async () => {
      const direct = $('#th-proxy-direct').checked;
      const proxy = $('#th-new-proxy').value.trim();
      if (!direct && !proxy) throw new Error('请填写代理地址，或勾选清除代理');
      await api('PUT', '/api/tokenharbor/registrations/' + encodeURIComponent(id) + '/proxy', { proxyUrl: direct ? '' : proxy });
    } }],
  });
  if (currentPage === 'thregister') loadTHRegistrations();
}

async function thRowAction(kind, id) {
  const err = $('#th-register-import-error');
  err.textContent = '';
  if (kind === 'reverify') err.textContent = '';
  try {
    if (kind === 'import') {
      await api('POST', '/api/tokenharbor/registrations/' + encodeURIComponent(id) + '/import', { group: $('#th-register-group').value.trim() || 'tokenharbor' });
    } else if (kind === 'retry') {
      await api('POST', '/api/tokenharbor/registrations/' + encodeURIComponent(id) + '/retry', { solveCaptcha: $('#th-register-solve').checked });
    } else if (kind === 'delete') {
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
  thState.visible = true;
  $('#th-logbox').classList.remove('hidden');
  if (!thLog.timer) {
    thLogPoll();
    thLog.timer = setTimeout(thLogPoll, 1500);
  }
}

function thLogHide() {
  $('#th-logbox').classList.add('hidden');
  // 收起不动轮询状态：再打开时从上次的 seq 继续，不丢中间的行。
}

async function thLogPoll() {
  thLog.timer = null;
  try {
    const out = await api('GET', '/api/tokenharbor/log?after=' + thLog.seq);
    const entries = (out && out.entries) || [];
    if (entries.length) {
      thLog.seq = out.seq || thLog.seq;
      for (const e of entries) thLog.lines.push(e);
      if (thLog.lines.length > 400) thLog.lines = thLog.lines.slice(-400);
      if (!thLog.paused) thLogRender();
    }
    thLogStatus(entries.length);
  } catch (e) {
    $('#th-log-status').textContent = '日志获取失败：' + thSafe(e.message);
  } finally {
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
  on('#th-registration-form', 'submit', thSubmitRegistration);
  on('#th-settings-form', 'submit', thSaveSettings);
  on('#th-register-jobs', 'click', (e) => {
    const b = e.target.closest('[data-job]');
    if (!b) return;
    thState.job = b.dataset.job;
    $('#th-register-import-error').textContent = '';
    loadTHRegistrations();
  });
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
    if (proxy) { thRowProxy(proxy.dataset.id).catch((err) => toast(err.message)); return; }
    for (const kind of ['import', 'retry', 'delete', 'reverify']) {
      const btn = e.target.closest('.th-row-' + kind);
      if (btn) { thRowAction(kind, btn.dataset.id); return; }
    }
  });
}

// 脚本在 body 末尾加载，DOM 已就绪。
thInstall();
