// Perch 号池管理台。与 app.js 共用 $ / api / esc / modal，加载顺序在 app.js 之后。
// 该号池只有 Supabase 会话，没有 API Key：页面从不显示任何 Token，只显示脱敏摘要。

const PC_STATUS = {
  normal: ['正常', 'ok'],
  pending: ['待验证', 'warn'],
  'login-expired': ['登录失效', 'bad'],
  'refresh-failed': ['刷新失败', 'bad'],
  restricted: ['套餐受限', 'warn'],
  'quota-exhausted': ['额度耗尽', 'warn'],
  'tier-pending': ['待选套餐', 'bad'],
  'rate-limited': ['请求限流', 'warn'],
  'service-down': ['服务暂时不可用', 'warn'],
  disabled: ['已禁用', 'off'],
};
const pcState = { page: 1, size: 50, total: 0, selected: new Set(), rows: [], lastQuery: '' };

function pcBadge(status, detail) {
  const [label, cls] = PC_STATUS[status] || [status || '未知', ''];
  return `<span class="badge ${cls}" title="${esc(detail || '')}">${esc(label)}</span>`;
}
const pcPT = (v) => (v === null || v === undefined) ? '未知' : Number(v).toLocaleString('en-US') + ' PT';

// ---- 概览 ----
async function loadPerchOverview() {
  const [o, shared] = await Promise.all([
    api('GET', '/api/perch/overview'),
    api('GET', '/api/overview?provider=perch').catch(() => null),
  ]);
  renderPerchStats(shared && shared.stats);
  const c = o.counts || {};
  const g = (k) => c[k] || 0;
  const cards = [
    ['账号总数', o.total, `${o.groups ? Object.keys(o.groups).length : 0} 个分组`],
    ['正常', g('normal'), '可调度'],
    ['登录失效', g('login-expired'), '需要重新登录'],
    ['刷新失败', g('refresh-failed'), '会话错误'],
    ['额度耗尽', g('quota-exhausted'), '本月用尽'],
    ['当前限流', g('rate-limited'), '冷却中'],
    ['禁用', g('disabled'), '手动禁用'],
    ['待验证', g('pending'), '尚未确认'],
    ['最近 24h 导入', o.recentImports, '新入池账号'],
    ['续期成功率', o.refreshRate && o.refreshRate.samples ? o.refreshRate.percent.toFixed(1) + '%' : '—', `${o.refreshRate ? o.refreshRate.samples : 0} 次样本（近 7 天）`],
  ];
  $('#pc-cards').innerHTML = cards.map(([l, v, s]) => `<div class="card"><div class="label">${esc(l)}</div><div class="value">${esc(v)}</div><div class="sub">${esc(s)}</div></div>`).join('');
  const alert = $('#pc-alert');
  if (o.alerting) {
    alert.classList.remove('hidden');
    alert.innerHTML = `<span class="badge bad">需要处理</span> 有 ${g('login-expired') + g('refresh-failed')} 个账号会话失效或刷新失败，请到「Perch 账号」筛选处理。`;
  } else {
    alert.classList.add('hidden');
  }
  // 各套餐账号数量
  const plans = Object.entries(o.plans || {}).sort((a, b) => b[1] - a[1]);
  const trend = (o.trend || []).map((t) => `${esc(t.Day || t.day)}：${Object.entries(t.Seen || t.seen || {}).map(([k, n]) => `${(PC_STATUS[k] || [k])[0]} ${n}`).join(' · ')}`);
  $('#pc-trend').innerHTML = plans.map(([p, n]) => `<span class="badge">${esc(p)}：${n}</span>`).join('') +
    (trend.length ? trend.slice(-7).map((t) => `<div class="muted small">${t}</div>`).join('') : '<div class="muted small">还没有检测记录。</div>');
}

function pcRate(b) {
  if (!b || b.r == null || b.s == null || b.c == null || b.r - b.c <= 0) return '—';
  return pct(b.s / (b.r - b.c));
}
function renderPerchStats(stats) {
  const b = stats && stats.total;
  const n = (v) => v == null ? '—' : num(v);
  const hit = b && b.ckr > 0 && b.cp > 0 ? pct(Math.min((b.ck || 0) / b.cp, 1)) : '—';
  const cards = [
    ['24h 请求', n(b && b.r), `近 1h ${n(stats && stats.lastHour && stats.lastHour.r)}`],
    ['24h 成功率', pcRate(b), '不含客户端取消'],
    ['24h 缓存率', hit, `已知 ${n(b && (b.ckr || 0))} 次 · 缓存 ${n(b && (b.ck || 0))} / 提示 ${n(b && (b.cp || 0))} token`],
    ['24h Token', b && b.i != null && b.o != null ? num(b.i + b.o) : '—', `输入 ${n(b && b.i)} · 输出 ${n(b && b.o)}（仅上游已提供）`],
    ['24h 费用', b ? costSum(b) : '—', '按已知用量估算，可能不完整'],
  ];
  $('#pc-stats-cards').innerHTML = cards.map(([l, v, s]) => `<div class="card"><div class="label">${esc(l)}</div><div class="value">${esc(v)}</div><div class="sub">${esc(s)}</div></div>`).join('');
  $('#pc-stats-updated').textContent = stats && stats.generated ? '更新于 ' + fmtTime(stats.generated) : '统计暂不可用';
  const hourly = stats && stats.hourly || [];
  const peak = Math.max(1, ...hourly.map((v) => v.r || 0));
  $('#pc-requests-chart').innerHTML = hourly.map((v) => {
    const label = new Date(v.m * 60000).toLocaleString('zh-CN', { ...TZ, hour: '2-digit', minute: '2-digit' });
    return `<div class="col" title="${esc(label)} 请求 ${v.r}，失败 ${v.f}，取消 ${v.c}"><div class="seg ok" data-h="${v.s / peak * 100}"></div><div class="seg bad" data-h="${v.f / peak * 100}"></div><div class="seg cancel" data-h="${v.c / peak * 100}"></div></div>`;
  }).join('');
  applyWidths($('#pc-requests-chart'));
  const rows = (map, proxy) => Object.entries(map || {}).sort((a, b) => (b[1].r || 0) - (a[1].r || 0)).map(([name, v]) => {
    const cells = proxy ? [n(v.s), n(v.f), n(v.c)] : [n(v.i), n(v.o), v.ckr > 0 && v.cp > 0 ? pct(Math.min((v.ck || 0) / v.cp, 1)) : '—', costSum(v)];
    return `<tr><td>${esc(name === 'direct' && proxy ? '未配置账号代理（实际路径未知）' : name)}</td><td class="num">${n(v.r)}</td><td class="num">${pcRate(v)}</td>${cells.map((x) => `<td class="num">${esc(x)}</td>`).join('')}</tr>`;
  }).join('') || `<tr><td colspan="${proxy ? 6 : 7}" class="empty">${stats ? '暂无请求' : '统计暂不可用'}</td></tr>`;
  $('#pc-models tbody').innerHTML = rows(stats && stats.models, false);
  $('#pc-proxies tbody').innerHTML = rows(stats && stats.proxies, true);
}

// ---- 账号列表 ----
function pcQuery() {
  const q = new URLSearchParams();
  const set = (k, v) => { if (v) q.set(k, v); };
  set('q', $('#pc-q').value.trim());
  set('status', $('#pc-status').value);
  set('group', $('#pc-group').value);
  set('plan', $('#pc-plan').value);
  set('remaining', $('#pc-remaining').value);
  set('sort', $('#pc-sort').value);
  set('dir', $('#pc-dir').value);
  q.set('page', pcState.page);
  q.set('size', pcState.size);
  return q.toString();
}

async function loadPerchAccounts(keepSelection) {
  const qs = pcQuery();
  pcState.lastQuery = qs;
  const [out, shared] = await Promise.all([
    api('GET', '/api/perch/accounts?' + qs),
    api('GET', '/api/overview?provider=perch').catch(() => null),
  ]);
  const accountStats = shared && shared.stats && shared.stats.accounts;
  pcState.total = out.total;
  pcState.rows = out.accounts || [];
  if (!keepSelection) { pcState.selected.clear(); $('#pc-all').checked = false; }
  const tbody = $('#pc-table tbody');
  tbody.innerHTML = pcState.rows.map((a) => {
    const s = a.session || {};
    const exp = s.expiresAt
      ? `<span class="pc-exp">${fmtTime(s.expiresAt, true)}<span class="muted">${fmtIn(s.expiresAt)}</span></span>`
      : '—';
    const fetched = s.fetchedAt ? fmtTime(s.fetchedAt, true) : '—';
    const checked = a.lastCheckedAt ? fmtTime(a.lastCheckedAt, true) : '—';
    // The cap's provenance decides the wording: a cap Perch stated is a fact,
    // one derived from its plan fallback is an estimate, and no cap at all is
    // "unknown" — never a number this program made up.
    const q = a.quota || {};
    const usedTxt = q.usedPt != null ? pcPT(q.usedPt) : '—';
    let remaining;
    if (a.unlimited) {
      remaining = `<span class="pc-remain" title="Perch 内网/管理员账号，不计量">不限</span>`;
    } else if (a.tierSelectionRequired) {
      // Perch states no plan and no entitlements for this account, so there is
      // no allowance to show. Saying "unknown" would read like a bug; the
      // account simply has not been taken into use yet.
      remaining = `<span class="pc-remain" title="Perch 返回待选套餐且未提供额度；请在 chat.perchai.app 检查该账号的套餐状态。">待选套餐</span>`;
    } else if (a.remainingKnown) {
      const src = a.allowanceSource === 'plan_fallback' ? '（按套餐估算，Perch 未明确给出）' : '';
      remaining = `<span class="pc-remain" title="已用 ${esc(usedTxt)} / 上限 ${esc(pcPT(q.allowancePt))}${esc(src)}">${pcPT(a.remainingPt)}</span>`;
    } else {
      const waiting = !a.quotaAt && !a.quotaError;
      remaining = `<span class="pc-remain" title="${esc(a.quotaError || (q.planCode ? 'Perch 没有为该套餐给出月度上限' : '还没有读到额度'))}">${waiting ? '待同步' : '未知'}</span>`;
    }
    const tags = (a.tags || []).map((t) => `<span class="badge">${esc(t)}</span>`).join(' ');
    return `<tr data-id="${esc(a.id)}">
      <td class="pick"><input type="checkbox" class="pc-pick" value="${esc(a.id)}" ${pcState.selected.has(a.id) ? 'checked' : ''}></td>
      <td><div>${esc(a.email || a.name)}</div><div class="muted small">${esc(a.userId)}</div><div class="muted small">${a.hasProxy ? '显式账号代理' : '未配置账号代理（路径未知）'}</div></td>
      <td>${pcBadge(a.status, a.statusDetail)}${a.enabled ? '' : ' <span class="badge off">停用</span>'}</td>
      <td>${esc(a.plan || (a.tierSelectionRequired ? '待选套餐' : (!a.quotaAt && !a.quotaError ? '待同步' : '未知')))}</td>
      <td class="num">${remaining}</td>
      <td class="small">${exp}</td>
      <td class="small">${esc(fetched)}</td>
      <td class="small">${esc(checked)}</td>
      <td class="small">${esc(a.group)} ${tags}</td>
      <td class="num">${accountStats ? (accountStats[a.id] ? `${num(accountStats[a.id].r)} / ${pcRate(accountStats[a.id])}` : '0 / —') : '—'}</td>
      <td class="actions-cell">
        <select class="pc-actions" data-id="${esc(a.id)}" aria-label="账号操作">
          <option value="">操作…</option>
          <option value="check">检测登录状态</option>
          <option value="refresh">立即续期</option>
          <option value="quota">同步额度</option>
          <option value="session">更新会话</option>
          <option value="proxy">修改代理</option>
          <option value="detail">查看详情/事件</option>
          <option value="${a.enabled ? 'disable' : 'enable'}">${a.enabled ? '禁用账号' : '启用账号'}</option>
          <option value="delete">删除账号</option>
        </select>
      </td></tr>`;
  }).join('');
  $('#pc-empty').classList.toggle('hidden', pcState.total !== 0);
  const pages = Math.max(1, Math.ceil(pcState.total / pcState.size));
  $('#pc-page').textContent = `第 ${pcState.page} / ${pages} 页 · 共 ${pcState.total} 个`;
  $('#pc-count').textContent = `共 ${pcState.total} 个账号 · 已选 ${pcState.selected.size} 个`;
  $('#pc-prev').disabled = pcState.page <= 1;
  $('#pc-next').disabled = pcState.page >= pages;
  fillPcFilters();
}

// 分组与套餐下拉：从当前全量里收集，避免多一次请求
function fillPcFilters() {
  const groups = new Set(), plans = new Set();
  pcState.rows.forEach((a) => { if (a.group) groups.add(a.group); if (a.plan) plans.add(a.plan); });
  const keep = (sel, values, label) => {
    const cur = sel.value;
    sel.innerHTML = `<option value="">${label}</option>` + Array.from(values).sort().map((v) => `<option value="${esc(v)}">${esc(v)}</option>`).join('');
    if (Array.from(values).includes(cur)) sel.value = cur;
  };
  keep($('#pc-group'), groups, '全部分组');
  keep($('#pc-plan'), plans, '全部套餐');
}

async function pcAct(id, act) {
  try {
    if (act === 'check') {
      const r = await api('POST', `/api/perch/accounts/${id}/check`);
      toast('检测结果：' + ((PC_STATUS[r.status] || [r.status])[0]) + (r.detail ? ' — ' + r.detail : ''));
    } else if (act === 'refresh') {
      const r = await api('POST', `/api/perch/accounts/${id}/refresh`);
      toast(r.needLogin ? '刷新令牌已失效，需要重新登录' : r.renewed ? '已续期' : r.skipped ? '无需续期' : ('完成' + (r.error ? '：' + r.error : '')));
    } else if (act === 'quota') {
      const r = await api('POST', `/api/perch/accounts/${id}/quota`);
      toast(r.ok ? '额度已同步' : ('额度读取失败：' + r.error));
    } else if (act === 'session') {
      await pcUpdateSession(id);
    } else if (act === 'proxy') {
      await pcUpdateProxy(id);
    } else if (act === 'detail') {
      await pcShowDetail(id);
      return;
    } else if (act === 'enable' || act === 'disable') {
      await api('PUT', `/api/perch/accounts/${id}`, { enabled: act === 'enable' });
      toast(act === 'enable' ? '已启用' : '已禁用');
    } else if (act === 'delete') {
      const a = pcState.rows.find((x) => x.id === id);
      if (!confirm(`确认删除 ${a ? a.email : id}？其 Session 也会一并移除。`)) return;
      const r = await api('POST', '/api/perch/batch', { action: 'delete', ids: [id] });
      toast(r.failed ? '删除失败：' + (r.results[0] || {}).detail : '已删除');
    }
    await loadPerchAccounts(true);
  } catch (e) {
    toast(e.message);
  }
}

async function pcUpdateProxy(id) {
  let a = pcState.rows.find((x) => x.id === id);
  if (!a) a = (await api('GET', `/api/perch/accounts/${encodeURIComponent(id)}`)).account;
  return modal({
    title: '修改代理 — ' + (a ? a.email : id),
    body: `<div class="form">
      <label>当前</label><span>${a && a.hasProxy ? '已配置账号代理' : '未配置账号代理（路径未知）'}</span>
      <label for="pc-proxy-mode">连接方式</label><select id="pc-proxy-mode"><option value="keep">保持不变</option><option value="direct">清除账号代理</option><option value="replace">替换代理</option></select>
      <label for="pc-proxy-url">完整代理地址</label><input id="pc-proxy-url" type="password" autocomplete="off" placeholder="http:// 或 socks5://">
      <span class="hint">替换时输入完整地址（包括需要的用户名和密码）；原凭据不会回填。清除账号代理后，常规请求仍可能使用环境代理，取决于目标和 NO_PROXY。新设置用于后续请求，已在途的请求不会中断。</span>
    </div>`,
    buttons: [{ label: '取消', value: 'cancel' }, {
      label: '保存', value: 'save', primary: true,
      action: async () => {
        const mode = $('#pc-proxy-mode').value;
        if (mode === 'keep') return;
        const proxyUrl = mode === 'direct' ? '' : $('#pc-proxy-url').value.trim();
        if (mode === 'replace' && !proxyUrl) throw new Error('请输入完整代理地址');
        await api('PUT', `/api/perch/accounts/${id}`, { proxyUrl });
        toast('代理设置已更新');
      },
    }],
  });
}

// 手动更新会话：接受两份 Token，或直接粘贴导出的整份 JSON
function pcUpdateSession(id) {
  const a = pcState.rows.find((x) => x.id === id);
  return modal({
    title: '更新会话 — ' + (a ? a.email : id),
    body: `<div class="form">
      <span class="hint">把账号导出的 JSON 整份粘贴到下面最省事；也可以只填两个 Token。Supabase User ID 必须与本账号一致。</span>
      <label>账号 JSON（可选）</label><textarea id="pc-sess-json" rows="8" placeholder='{"schema_version":"1.0", ...}'></textarea>
      <label>Access Token</label><input id="pc-sess-at" placeholder="eyJ...">
      <label>Refresh Token</label><input id="pc-sess-rt" placeholder="v1...">
    </div>`,
    buttons: [{ label: '取消', value: 'cancel' }, {
      label: '保存', value: 'save', primary: true,
      action: async () => {
        const body = { json: $('#pc-sess-json').value.trim(), accessToken: $('#pc-sess-at').value.trim(), refreshToken: $('#pc-sess-rt').value.trim() };
        await api('PUT', `/api/perch/accounts/${id}/session`, body);
        toast('会话已更新');
      },
    }],
  });
}

async function pcShowDetail(id) {
  const out = await api('GET', `/api/perch/accounts/${id}`);
  const a = out.account || {};
  const s = a.session || {};
  const q = a.quota || {};
  const ev = out.events && out.events.items ? out.events.items : [];
  const rows = ev.map((raw) => {
    const e = raw;
    return `<tr><td class="small">${esc(fmtTime(e.ts, true))}</td><td>${esc(e.event)}</td><td>${esc(e.outcome || '')}</td><td class="small">${esc(e.detail || '')}</td></tr>`;
  }).join('');
  $('#pc-detail-panel').scrollIntoView({ behavior: 'smooth', block: 'nearest' });
  $('#pc-detail').innerHTML = `
    <div class="grid2">
      <div>
        <div><b>${esc(a.email)}</b> ${pcBadge(a.status, a.statusDetail)}</div>
        <div class="muted small">Supabase User ID：${esc(a.userId)}</div>
        <div class="muted small">分组：${esc(a.group)} · 标签：${esc((a.tags || []).join(', ') || '无')}</div>
        <div class="muted small">连接：${a.hasProxy ? '显式账号代理' : '未配置账号代理（路径未知）'}</div>
        <div class="muted small">Access Token 到期：${esc(s.expiresAt ? fmtTime(s.expiresAt) : '未知')} · 最近刷新：${esc(s.fetchedAt ? fmtTime(s.fetchedAt) : '—')}</div>
        <div class="muted small">Token 尾号：${esc(s.accessSuffix || '—')} · 第 ${esc(s.generation || 0)} 代（仅供区分，不含任何可用凭证）</div>
        ${a.lastError ? `<div class="error">最近错误：${esc(a.lastError)}（${esc(fmtTime(a.lastErrorAt))}）</div>` : ''}
      </div>
      <div>
        <div class="muted small">额度（Perch usage meter，单位 PT = 千分之一美元）</div>
        <div class="muted small">本月已用：${q.usedPt != null ? pcPT(q.usedPt) : '未知'} · 手动调用：${q.manualPt != null ? pcPT(q.manualPt) : '未知'}</div>
        <div class="muted small">月度上限：${a.unlimited ? '不限（内网/管理员账号）' : (q.allowancePt ? pcPT(q.allowancePt) + '（' + ({ entitlement: 'Perch 明确给出', plan_fallback: '按套餐估算' }[a.allowanceSource] || '来源未知') + '）' : (a.tierSelectionRequired ? 'Perch 返回待选套餐，额度未知；请在 chat.perchai.app 检查账号套餐' : '未知'))}</div>
        ${q.creditBalancePt ? `<div class="muted small">充值余额：${pcPT(q.creditBalancePt)}</div>` : ''}
        ${a.roostUnlimited ? '<div class="muted small">Roost 额度：不限</div>' : ''}
        ${a.planCode ? `<div class="muted small">套餐代码：${esc(a.planCode)}</div>` : ''}
        <div class="muted small">本月请求：${q.monthlyCalls != null ? q.monthlyCalls : '未知'} · Token：${q.monthlyTokens != null ? q.monthlyTokens : '未知'}</div>
        <div class="muted small">额度恢复：${q.resetsAt ? fmtTime(q.resetsAt, true) : '未提供'}</div>
        <div class="muted small">读取时间：${esc(a.quotaAt ? fmtTime(a.quotaAt) : '—')} ${a.quotaError ? '· 失败：' + esc(a.quotaError) : ''}</div>
      </div>
    </div>
    <div class="table-wrap mt"><table class="table"><thead><tr><th>时间</th><th>事件</th><th>结果</th><th>说明</th></tr></thead><tbody>${rows || '<tr><td colspan="4" class="empty">暂无事件</td></tr>'}</tbody></table></div>`;
}

// ---- 注册 ----
const pcRegistration = { id: '', selected: new Set(), timer: null, generation: 0, busy: false, importing: false };
const pcRegistrationLabels = { running: '运行中', completed: '已完成', queued: '排队中', registering: '注册中', activating: '激活中', activated: '已激活', failed: '失败', imported: '已入池' };
const pcRegistrationLabel = (status) => pcRegistrationLabels[status] || status || '未知';
const pcRegistrationSafe = (detail) => String(detail || '')
  .replace(/\b(?:https?|socks5h?):\/\/[^\s"'<>]+/gi, '[URL 已隐藏]')
  .replace(/\bBearer\s+[^\s,;]+/gi, 'Bearer [已隐藏]')
  .replace(/\b(password|passwd|pwd|totpSecret|totp|secret|token|authorization|proxyUrl|mailboxUrl)\b["'\s]*[:=][\s"']*[^\s,;"']+/gi, '$1=[已隐藏]');

function stopPerchRegistrationPolling() {
  pcRegistration.generation++;
  clearTimeout(pcRegistration.timer);
  pcRegistration.timer = null;
}

function pcRenderRegistration(job) {
  const accounts = Array.isArray(job.accounts) ? job.accounts : [];
  const eligible = new Set(accounts.filter((a) => a.canImport === true).map((a) => a.login));
  pcRegistration.selected.forEach((login) => { if (!eligible.has(login)) pcRegistration.selected.delete(login); });
  $('#pc-register-detail').classList.remove('hidden');
  $('#pc-register-job-title').textContent = '任务 ' + job.id;
  $('#pc-register-job-status').textContent = pcRegistrationLabel(job.status);
  $('#pc-register-rows').innerHTML = accounts.map((a) => `<tr>
    <td class="pick">${a.canImport === true ? `<input type="checkbox" class="pc-register-pick" value="${esc(a.login)}" ${pcRegistration.selected.has(a.login) ? 'checked' : ''} aria-label="选择 ${esc(a.login)}">` : ''}</td>
    <td>${esc(a.login || '—')}</td><td>${esc(a.email || '—')}</td><td><span class="badge ${a.status === 'failed' ? 'bad' : a.status === 'activated' || a.status === 'imported' ? 'ok' : 'accent'}">${esc(pcRegistrationLabel(a.status))}</span></td>
    <td><span class="small">${esc(a.status === 'imported' ? (a.proxy && a.proxy !== '直连' && a.proxy !== '账号已删除' ? '已配置账号代理' : (a.proxy === '账号已删除' ? '账号已删除' : '未配置账号代理（路径未知）')) : (a.proxy && a.proxy !== '直连' ? '已配置注册代理' : '未配置注册代理'))}</span> ${a.status === 'queued' || (a.status === 'activated' && a.canImport) ? `<button type="button" class="btn small pc-register-proxy" data-login="${esc(a.login)}" data-status="${esc(a.status)}">修改</button>` : a.status === 'imported' && a.accountId ? `<button type="button" class="btn small pc-register-imported-proxy" data-id="${esc(a.accountId)}">修改</button>` : ''}</td><td>${esc(pcRegistrationSafe(a.detail))}</td>
  </tr>`).join('') || '<tr><td colspan="6" class="empty">暂无账号状态</td></tr>';
}

async function loadPerchRegistrations() {
  stopPerchRegistrationPolling();
  const generation = pcRegistration.generation;
  const poll = async () => {
    try {
      const result = await api('GET', '/api/perch/registrations');
      if (generation !== pcRegistration.generation) return;
      const jobs = Array.isArray(result.jobs) ? result.jobs : [];
      if (!jobs.some((job) => job.id === pcRegistration.id)) {
        pcRegistration.id = jobs.length ? jobs[0].id : '';
        pcRegistration.selected.clear();
      }
      $('#pc-register-jobs').innerHTML = jobs.map((job) => `<button type="button" class="pc-register-job ${job.id === pcRegistration.id ? 'active' : ''}" data-job="${esc(job.id)}" aria-pressed="${job.id === pcRegistration.id}"><span>${esc(job.id)}</span><span class="muted small">${esc(fmtTime(job.createdAt, true))} · ${esc(pcRegistrationLabel(job.status))} · ${(job.accounts || []).length} 个账号</span></button>`).join('') || '<p class="muted">暂无任务。</p>';
      if (pcRegistration.id) {
        const id = pcRegistration.id;
        const job = await api('GET', '/api/perch/registrations/' + encodeURIComponent(id));
        if (generation !== pcRegistration.generation || id !== pcRegistration.id) return;
        pcRenderRegistration(job);
      } else {
        $('#pc-register-detail').classList.add('hidden');
      }
      $('#pc-register-poll-error').textContent = '';
    } catch (e) {
      if (generation === pcRegistration.generation) $('#pc-register-poll-error').textContent = '任务获取失败：' + pcRegistrationSafe(e.message);
    } finally {
      if (generation === pcRegistration.generation && currentPage === 'pregister') pcRegistration.timer = setTimeout(poll, 3000);
    }
  };
  await poll();
}

async function pcSubmitRegistration(event) {
  event.preventDefault();
  if (pcRegistration.busy) return;
  const error = $('#pc-register-error');
  error.textContent = '';
  try {
    const lines = $('#pc-register-accounts').value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
    if (!lines.length || lines.length > 100) throw new Error('请填写 1-100 个账号');
    const proxies = $('#pc-register-proxies').value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean).map((line, i) => {
      const parts = line.split('----');
      if (parts.length > 2 || parts.some((part) => !part.trim())) throw new Error(`第 ${i + 1} 条代理格式有误`);
      return parts[parts.length - 1].trim();
    });
    const accounts = lines.map((line, i) => {
      const parts = line.split('----').map((part) => part.trim());
      if (parts.length !== 4 || parts.some((part) => !part)) throw new Error(`第 ${i + 1} 条账号格式有误`);
      return { login: parts[0], password: parts[1], totpSecret: parts[2], mailboxUrl: parts[3], proxyUrl: proxies.length ? proxies[i % proxies.length] : '' };
    });
    pcRegistration.busy = true;
    $$('#pc-registration-form textarea, #pc-registration-form input, #pc-register-submit').forEach((el) => { el.disabled = true; });
    const result = await api('POST', '/api/perch/registrations', { accounts, autoImport: $('#pc-register-auto').checked, group: $('#pc-register-group').value.trim() || 'perch' });
    if (!result || !result.id) throw new Error('后台未返回任务 ID，请先检查任务列表后再重试');
    $('#pc-register-accounts').value = '';
    $('#pc-register-proxies').value = '';
    pcRegistration.id = result.id;
    pcRegistration.selected.clear();
    $('#pc-register-import-group').value = $('#pc-register-group').value.trim() || 'perch';
    if (currentPage === 'pregister') loadPerchRegistrations();
  } catch (e) {
    error.textContent = pcRegistrationSafe(e.message) + (pcRegistration.busy ? '。请求结果不确定时，请先刷新任务列表，确认是否已创建后再重试。' : '');
  } finally {
    pcRegistration.busy = false;
    $$('#pc-registration-form textarea, #pc-registration-form input, #pc-register-submit').forEach((el) => { el.disabled = false; });
  }
}

async function pcEditRegistrationProxy(login, status) {
  const id = pcRegistration.id;
  await modal({ title: '修改注册账号代理', body: `<div class="form">
    <p>账号：${esc(login)}</p>
    ${status === 'activated' ? '<p class="muted">该账号已经完成激活。更改后只影响入池后的出口，不会改变已完成的注册和激活请求。</p>' : ''}
    <label for="pc-register-new-proxy">新的代理地址</label>
    <input id="pc-register-new-proxy" type="password" autocomplete="off" placeholder="socks5://用户:密码@主机:端口">
    <label><input id="pc-register-direct" type="checkbox"> 清除注册代理</label>
  </div>`, buttons: [{ label: '取消', value: 'cancel' }, { label: '保存', value: 'save', primary: true, action: async () => {
    const direct = $('#pc-register-direct').checked;
    const proxy = $('#pc-register-new-proxy').value.trim();
    if (!direct && !proxy) throw new Error('请填写完整代理地址，或勾选清除注册代理');
    await api('PUT', '/api/perch/registrations/' + encodeURIComponent(id) + '/proxy', { login, proxyUrl: direct ? '' : proxy });
    $('#pc-register-new-proxy').value = '';
  } }] });
  if (currentPage === 'pregister') await loadPerchRegistrations();
}

async function pcImportRegistration() {
  if (pcRegistration.importing || !pcRegistration.id) return;
  const error = $('#pc-register-import-error');
  error.textContent = '';
  if (!pcRegistration.selected.size) { error.textContent = '请勾选已激活账号'; return; }
  const id = pcRegistration.id, logins = Array.from(pcRegistration.selected);
  pcRegistration.importing = true;
  stopPerchRegistrationPolling();
  $('#pc-register-import').disabled = true;
  try {
    const result = await api('POST', '/api/perch/registrations/' + encodeURIComponent(id) + '/import', { logins, group: $('#pc-register-import-group').value.trim() || 'perch' });
    if (id !== pcRegistration.id) return;
    const rows = Array.isArray(result.accounts) ? result.accounts : [];
    const failed = logins.filter((login) => !rows.some((row) => row.login === login && row.status === 'imported'));
    pcRegistration.selected.clear();
    failed.forEach((login) => pcRegistration.selected.add(login));
    pcRenderRegistration(result);
    if (failed.length) error.textContent = `${failed.length} 个账号入池未完成，请查看详情。`;
    else toast(`已入池 ${logins.length} 个账号`);
  } catch (e) { error.textContent = pcRegistrationSafe(e.message); }
  finally {
    pcRegistration.importing = false;
    $('#pc-register-import').disabled = false;
    if (currentPage === 'pregister') loadPerchRegistrations();
  }
}

// ---- 自动补号 ----
const pcAuto = { loaded: false, busy: false, dirty: false, running: false, enabled: false, generation: 0, rows: [], picked: new Set() };
const pcAutoRegisterable = (row) => row && row.canRegister === true;
function pcAutoUpdateRegisterButtons() {
  const count = pcAuto.rows.filter(pcAutoRegisterable).length;
  $('#pc-auto-register-selected').disabled = pcAuto.busy || pcAuto.picked.size === 0;
  $('#pc-auto-register-all').disabled = pcAuto.busy || count === 0;
  $('#pc-auto-register-selected').textContent = pcAuto.picked.size ? `注册所选（${pcAuto.picked.size}）` : '注册所选';
}
function pcAutoUpdateRunButton() {
  $('#pc-auto-run').disabled = pcAuto.running || pcAuto.busy || !pcAuto.loaded || !pcAuto.enabled;
}
const pcAutoLabels = { queued: '等待中', running: '运行中', ready: '已就绪', completed: '已完成', failed: '失败', uncertain: '结果不确定' };

function pcAutoProxy(value) {
  if (!value) return '直连（未使用代理）';
  try {
    const url = new URL(value);
    return url.protocol + '//' + url.host;
  } catch { return '已配置代理'; }
}

async function loadPerchAutosignup() {
  const generation = ++pcAuto.generation;
  try {
    const data = await api('GET', '/api/perch/autosignup');
    if (generation !== pcAuto.generation) return;
    if (!data || !data.settings || !data.status || !Array.isArray(data.candidates)) throw new Error('后台返回的数据格式不正确');
    const settings = data.settings, status = data.status, rows = data.candidates;
    if (!pcAuto.dirty) {
      $('#pc-auto-enabled').checked = !!settings.enabled;
      $('#pc-auto-interval').value = settings.intervalSec;
      $('#pc-auto-min').value = settings.minAvailable;
      $('#pc-auto-group').value = settings.group || '';
    }
    pcAuto.loaded = true;
    $$('#pc-auto-settings input, #pc-auto-save').forEach((el) => { el.disabled = false; });
    pcAuto.running = !!status.running;
    pcAuto.enabled = settings.enabled === true;
    pcAutoUpdateRunButton();
    const failed = rows.filter((row) => row.status === 'failed').length;
    const cards = [
      ['自动任务', settings.enabled ? '已启用' : '已暂停', status.running ? '正在执行' : '空闲'],
      ['可用账号', num(status.available), '最近一次检查'],
      ['候选账号', num(rows.length), `失败 ${failed} 个`],
      ['最近检查', fmtTime(status.lastCheck), '北京时间'],
    ];
    $('#pc-auto-cards').innerHTML = cards.map(([label, value, sub]) => `<div class="card"><div class="label">${esc(label)}</div><div class="value">${esc(value)}</div><div class="sub">${esc(sub)}</div></div>`).join('');
    // An error stopped the loop; a notice is something it worked through. They
    // are shown apart so a persistent red line keeps meaning "go look".
    $('#pc-auto-last-error').textContent = status.lastError ? '最近错误：' + pcRegistrationSafe(status.lastError) : '';
    $('#pc-auto-notice').textContent = status.notice ? '提示：' + pcRegistrationSafe(status.notice) : '';
    const proxies = Array.isArray(data.proxies) ? data.proxies : [];
    $('#pc-auto-proxy-rows').innerHTML = proxies.map((proxy) => `<tr><td>${esc(pcAutoProxy(proxy.proxy))}</td><td>${proxy.id ? `<button type="button" class="btn small pc-auto-proxy-edit" data-id="${esc(proxy.id)}" data-proxy="${esc(proxy.proxy)}">修改</button> <button type="button" class="btn small danger pc-auto-proxy-delete" data-id="${esc(proxy.id)}">删除</button>` : ''}</td></tr>`).join('') || '<tr><td colspan="2" class="empty">暂无代理库存</td></tr>';
    const eligible = new Set(rows.filter(pcAutoRegisterable).map((row) => row.id));
    pcAuto.rows = rows;
    pcAuto.picked.forEach((id) => { if (!eligible.has(id)) pcAuto.picked.delete(id); });
    pcAutoUpdateRegisterButtons();
    $('#pc-auto-pick-all').checked = eligible.size > 0 && pcAuto.picked.size === eligible.size;
    $('#pc-auto-register-message').textContent = status.registered ? `${num(status.registered)} 个已注册待入池` : '';
    $('#pc-auto-rows').innerHTML = rows.map((row) => `<tr><td class="pick">${pcAutoRegisterable(row) ? `<input type="checkbox" class="pc-auto-pick" value="${esc(row.id)}" ${pcAuto.picked.has(row.id) ? 'checked' : ''} aria-label="选择 ${esc(row.login || '')}">` : ''}</td><td>${esc(row.login || '—')}</td><td><span class="badge ${row.status === 'failed' ? 'bad' : row.status === 'uncertain' ? 'warn' : row.status === 'completed' ? 'ok' : 'accent'}">${esc(pcAutoLabels[row.status] || row.status || '未知')}</span></td><td>${esc(pcAutoProxy(row.proxy))}</td><td>${esc(pcRegistrationSafe(row.detail))}${row.canRetrySave === true ? '<div class="error small">唯一会话仅保存在内存中，服务重启后将丢失。</div>' : ''}</td><td>${row.id && row.canRetrySave === true ? `<button type="button" class="btn small primary pc-auto-retry-save" data-id="${esc(row.id)}">重试保存</button> ` : ''}${row.id && row.status === 'uncertain' && row.canRevalidate === true ? `<button type="button" class="btn small pc-auto-activate" data-id="${esc(row.id)}" data-login="${esc(row.login || '')}">验证激活</button> ` : ''}${row.id && row.canEditProxy === true ? `<button type="button" class="btn small pc-auto-candidate-proxy" data-id="${esc(row.id)}" data-login="${esc(row.login || '')}" data-hasproxy="${row.hasProxy === true ? '1' : '0'}">修改代理</button> ` : ''}${row.id && row.status === 'ready' ? `<button type="button" class="btn small primary pc-auto-import" data-id="${esc(row.id)}" data-login="${esc(row.login || '')}">确认入池</button> ` : ''}${row.id && row.status !== 'running' ? `<button type="button" class="btn small danger pc-auto-delete" data-id="${esc(row.id)}" data-login="${esc(row.login || '')}">删除</button>` : ''}</td></tr>`).join('') || '<tr><td colspan="6" class="empty">暂无候选账号</td></tr>';
    $('#pc-auto-error').textContent = '';
  } catch (e) {
    if (generation === pcAuto.generation) $('#pc-auto-error').textContent = '获取自动补号状态失败：' + pcRegistrationSafe(e.message);
  }
}

// pcAutoRegister registers the chosen candidates now and leaves them ready for a
// later refill; it never imports them.
async function pcAutoRegister(ids) {
  if (!ids || !ids.length) return;
  const group = $('#pc-auto-group').value.trim();
  if (!group) throw new Error('请先填写入池分组并保存设置');
  if (await modal({ title: '注册所选候选', body: `<p>立即注册 ${ids.length} 个候选账号？注册完成后停在「已注册并激活」，补号时直接入池，不会再注册一次。</p>`, buttons: [{ label: '取消', value: 'c' }, { label: '开始注册', value: 'x' }] }) !== 'x') return;
  const res = await api('POST', '/api/perch/autosignup/register', { ids, group });
  pcAuto.picked.clear();
  toast(`已排入注册队列 ${res && res.count ? res.count : ids.length} 个`);
  await loadPerchAutosignup();
}

async function pcAutoSave(event) {
  event.preventDefault();
  if (pcAuto.busy || !pcAuto.loaded) return;
  const intervalSec = Number($('#pc-auto-interval').value), minAvailable = Number($('#pc-auto-min').value);
  const group = $('#pc-auto-group').value.trim();
  if (!Number.isSafeInteger(intervalSec) || intervalSec < 60 || !Number.isSafeInteger(minAvailable) || minAvailable < 1 || !group) {
    $('#pc-auto-error').textContent = '检查间隔至少 60 秒，最低可用账号数至少 1，分组不能为空';
    return;
  }
  pcAuto.busy = true; pcAutoUpdateRunButton();
  $$('#pc-auto-settings input, #pc-auto-save').forEach((el) => { el.disabled = true; });
  $('#pc-auto-settings-message').textContent = '';
  $('#pc-auto-error').textContent = '';
  try {
    await api('PUT', '/api/perch/autosignup/settings', { enabled: $('#pc-auto-enabled').checked, intervalSec, minAvailable, group });
    pcAuto.dirty = false;
    $('#pc-auto-settings-message').textContent = '设置已提交';
    await loadPerchAutosignup();
  } catch (e) { $('#pc-auto-error').textContent = '保存设置失败：' + pcRegistrationSafe(e.message); }
  finally {
    pcAuto.busy = false;
    pcAutoUpdateRunButton();
    $$('#pc-auto-settings input, #pc-auto-save').forEach((el) => { el.disabled = false; });
  }
}

async function pcAutoSubmit(event) {
  event.preventDefault();
  if (pcAuto.busy) return;
  const error = $('#pc-auto-submit-error');
  error.textContent = '';
  $('#pc-auto-submit-message').textContent = '';
  try {
    const lines = $('#pc-auto-accounts').value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
    if (!lines.length || lines.length > 100) throw new Error('请填写 1-100 个账号');
    const proxies = $('#pc-auto-proxies').value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean).map((line, i) => {
      const parts = line.split('----');
      if (parts.length > 2 || parts.some((part) => !part.trim())) throw new Error(`第 ${i + 1} 条代理格式有误`);
      return parts[parts.length - 1].trim();
    });
    const accounts = lines.map((line, i) => {
      const parts = line.split('----').map((part) => part.trim());
      if (parts.length !== 4 || parts.some((part) => !part)) throw new Error(`第 ${i + 1} 条账号格式有误`);
      return { login: parts[0], password: parts[1], totpSecret: parts[2], mailboxUrl: parts[3], proxyUrl: proxies.length ? proxies[i % proxies.length] : '' };
    });
    pcAuto.busy = true; pcAutoUpdateRunButton();
    $$('#pc-auto-candidates textarea, #pc-auto-add').forEach((el) => { el.disabled = true; });
    const result = await api('POST', '/api/perch/autosignup/candidates', { accounts });
    $('#pc-auto-accounts').value = '';
    $('#pc-auto-proxies').value = '';
    if (result && Number.isSafeInteger(result.added) && Number.isSafeInteger(result.duplicate) && result.added >= 0 && result.duplicate >= 0) {
      $('#pc-auto-submit-message').textContent = `新增 ${result.added} 个，重复 ${result.duplicate} 个；重复账号未覆盖。`;
    } else {
      $('#pc-auto-submit-message').textContent = '提交已完成，但后台未返回有效的新增和重复数量，请刷新候选列表确认。';
    }
    await loadPerchAutosignup();
  } catch (e) {
    if (pcAuto.busy) {
      let message = String(e.message || '请求失败');
      for (const line of $('#pc-auto-accounts').value.split(/\r?\n/)) {
        for (const secret of line.split('----').slice(1)) if (secret.trim()) message = message.split(secret.trim()).join('[已隐藏]');
      }
      for (const line of $('#pc-auto-proxies').value.split(/\r?\n/)) {
        const proxy = line.split('----').pop().trim();
        if (proxy) message = message.split(proxy).join('[已隐藏]');
      }
      error.textContent = '提交失败：' + pcRegistrationSafe(message) + '。结果不确定时，请刷新候选列表确认后再重试。';
    } else error.textContent = pcRegistrationSafe(e.message);
  } finally {
    pcAuto.busy = false;
    pcAutoUpdateRunButton();
    $$('#pc-auto-candidates textarea, #pc-auto-add').forEach((el) => { el.disabled = false; });
  }
}

async function pcAutoAddProxies(event) {
  event.preventDefault();
  if (pcAuto.busy) return;
  const error = $('#pc-auto-proxy-error');
  error.textContent = '';
  $('#pc-auto-proxy-message').textContent = '';
  try {
    const lines = $('#pc-auto-proxy-input').value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
    if (!lines.length) throw new Error('请填写代理地址');
    const proxies = lines.map((line, i) => {
      const parts = line.split('----');
      if (parts.length > 2 || parts.some((part) => !part.trim())) throw new Error(`第 ${i + 1} 条代理格式有误`);
      return { url: parts[parts.length - 1].trim() };
    });
    pcAuto.busy = true; pcAutoUpdateRunButton();
    $$('#pc-auto-proxy-form textarea, #pc-auto-proxy-add').forEach((el) => { el.disabled = true; });
    const result = await api('POST', '/api/perch/autosignup/proxies', { proxies });
    $('#pc-auto-proxy-input').value = '';
    $('#pc-auto-proxy-message').textContent = result && Number.isSafeInteger(result.added) && Number.isSafeInteger(result.duplicate) ? `新增 ${result.added} 个，重复 ${result.duplicate} 个。` : '代理提交已完成，请查看库存。';
    await loadPerchAutosignup();
  } catch (e) {
    if (pcAuto.busy) {
      let message = String(e.message || '请求失败');
      for (const line of $('#pc-auto-proxy-input').value.split(/\r?\n/)) {
        const proxy = line.split('----').pop().trim();
        if (proxy) message = message.split(proxy).join('[已隐藏]');
      }
      error.textContent = '代理提交失败：' + pcRegistrationSafe(message) + '。结果不确定时，请刷新库存确认后再重试。';
    } else error.textContent = pcRegistrationSafe(e.message);
  } finally {
    pcAuto.busy = false;
    pcAutoUpdateRunButton();
    $$('#pc-auto-proxy-form textarea, #pc-auto-proxy-add').forEach((el) => { el.disabled = false; });
  }
}

// pcAutoEditProxy rewrites one inventory entry in place. Candidates already
// bound to the old address keep it: their proxy is what their registration ran
// behind, so the dialog says so and points at the candidate row's own button.
async function pcAutoEditProxy(id, current) {
  if (pcAuto.busy) return;
  await modal({ title: '修改库存代理', body: `<div class="form">
    <label>当前</label><span>${esc(pcAutoProxy(current))}</span>
    <label for="pc-auto-proxy-edit-url">新的代理地址</label>
    <input id="pc-auto-proxy-edit-url" type="password" autocomplete="off" placeholder="socks5://用户:密码@主机:端口">
    <span class="hint">只改库存这一条，ID 不变。已经绑定旧地址的候选账号不受影响；要换某个候选的出口，请在该候选行点「修改代理」。新地址会用于之后领取任务的候选账号。</span>
  </div>`, buttons: [{ label: '取消', value: 'cancel' }, { label: '保存', value: 'save', primary: true, action: async () => {
    const url = $('#pc-auto-proxy-edit-url').value.trim();
    if (!url) throw new Error('请填写完整代理地址');
    await api('PUT', '/api/perch/autosignup/proxies/' + encodeURIComponent(id), { url });
    $('#pc-auto-proxy-edit-url').value = '';
  } }] });
  await loadPerchAutosignup();
}

// pcAutoEditCandidateProxy changes one candidate's proxy, including clearing it
// to the direct path.
async function pcAutoEditCandidateProxy(id, login, hasProxy) {
  if (pcAuto.busy) return;
  await modal({ title: '修改候选账号代理' + (login ? ' — ' + login : ''), body: `<div class="form">
    <label>当前</label><span>${hasProxy ? '已配置代理' : '直连（未使用代理）'}</span>
    <label for="pc-auto-candidate-proxy">新的代理地址</label>
    <input id="pc-auto-candidate-proxy" type="password" autocomplete="off" placeholder="socks5://用户:密码@主机:端口">
    <label class="check"><input type="checkbox" id="pc-auto-candidate-direct"> 清除代理，改为直连</label>
    <span class="hint">只影响这个候选之后的注册与激活请求；已经完成的注册和激活不会重放。运行中的候选需要等本轮结束。</span>
  </div>`, buttons: [{ label: '取消', value: 'cancel' }, { label: '保存', value: 'save', primary: true, action: async () => {
    const direct = $('#pc-auto-candidate-direct').checked;
    const proxy = $('#pc-auto-candidate-proxy').value.trim();
    if (!direct && !proxy) throw new Error('请填写完整代理地址，或勾选改为直连');
    await api('PUT', '/api/perch/autosignup/candidates/' + encodeURIComponent(id) + '/proxy', { proxyUrl: direct ? '' : proxy });
    $('#pc-auto-candidate-proxy').value = '';
  } }] });
  await loadPerchAutosignup();
}

async function pcAutoDeleteProxy(id) {
  if (pcAuto.busy || !confirm('确定删除该库存代理？')) return;
  pcAuto.busy = true; pcAutoUpdateRunButton();
  $('#pc-auto-error').textContent = '';
  try {
    await api('DELETE', '/api/perch/autosignup/proxies/' + encodeURIComponent(id));
    toast('库存代理已删除');
    await loadPerchAutosignup();
  } catch (e) { $('#pc-auto-error').textContent = '删除库存代理失败：' + pcRegistrationSafe(e.message); }
  finally { pcAuto.busy = false; pcAutoUpdateRunButton(); }
}

// pcAutoReportFailure surfaces a failed candidate action. The reload that follows
// makes loadPerchAutosignup clear #pc-auto-error, so a write to that element
// alone would be wiped the next line; the toast survives it, and the page
// error is restored afterwards unless the reload supplied a fresher one.
async function pcAutoReportFailure(prefix, err) {
  const message = prefix + (pcRegistrationSafe(err && err.message) || '未知错误');
  toast(message, 'error');
  await loadPerchAutosignup();
  if (!$('#pc-auto-error').textContent) $('#pc-auto-error').textContent = message;
}

async function pcAutoActivate(id, login) {
  if (pcAuto.busy || !confirm(`确定验证候选账号 ${login || id} 的激活状态？此操作可能发送 hi 消息。`)) return;
  pcAuto.busy = true; pcAutoUpdateRunButton();
  $('#pc-auto-error').textContent = '';
  try {
    await api('POST', '/api/perch/autosignup/candidates/' + encodeURIComponent(id) + '/activate', { confirm: true });
    toast('已请求验证激活');
    await loadPerchAutosignup();
  } catch (e) {
    await pcAutoReportFailure('验证激活失败：', e);
  } finally {
    pcAuto.busy = false;
    pcAutoUpdateRunButton();
  }
}

async function pcAutoRetrySave(id) {
  if (pcAuto.busy) return;
  pcAuto.busy = true; pcAutoUpdateRunButton();
  $('#pc-auto-error').textContent = '';
  try {
    await api('POST', '/api/perch/autosignup/candidates/' + encodeURIComponent(id) + '/retry-save');
    toast('已重试保存');
    await loadPerchAutosignup();
  } catch (e) {
    await pcAutoReportFailure('重试保存失败：', e);
  } finally {
    pcAuto.busy = false;
    pcAutoUpdateRunButton();
  }
}

async function pcAutoImport(id, login) {
  if (pcAuto.busy || !pcAuto.loaded) return;
  if (pcAuto.dirty) {
    $('#pc-auto-error').textContent = '请先保存自动补号设置，再确认入池。';
    toast('请先保存自动补号设置，再确认入池', 'error');
    return;
  }
  const group = $('#pc-auto-group').value.trim();
  if (!group) {
    $('#pc-auto-error').textContent = '当前设置的入池分组为空，请先保存设置。';
    toast('入池分组为空，请先保存设置', 'error');
    return;
  }
  if (!confirm(`确定将候选账号 ${login || id} 导入当前设置分组「${group}」？`)) return;
  pcAuto.busy = true; pcAutoUpdateRunButton();
  $('#pc-auto-error').textContent = '';
  try {
    await api('POST', '/api/perch/autosignup/candidates/' + encodeURIComponent(id) + '/import', { group });
    toast('已确认入池');
    await loadPerchAutosignup();
  } catch (e) {
    await pcAutoReportFailure('确认入池失败：', e);
  } finally {
    pcAuto.busy = false;
    pcAutoUpdateRunButton();
  }
}

async function pcAutoDelete(id, login) {
  if (pcAuto.busy || !confirm(`确定删除候选账号 ${login || id}？`)) return;
  pcAuto.busy = true; pcAutoUpdateRunButton();
  $('#pc-auto-error').textContent = '';
  try {
    await api('DELETE', '/api/perch/autosignup/candidates/' + encodeURIComponent(id));
    toast('候选账号已删除');
    await loadPerchAutosignup();
  } catch (e) {
    $('#pc-auto-error').textContent = '删除候选账号失败：' + pcRegistrationSafe(e.message);
    toast('删除候选账号失败', 'error');
  }
  finally { pcAuto.busy = false; pcAutoUpdateRunButton(); }
}

async function pcAutoRun() {
  if (pcAuto.busy || !pcAuto.loaded || !pcAuto.enabled || pcAuto.running) return;
  pcAuto.busy = true; pcAutoUpdateRunButton();
  $('#pc-auto-error').textContent = '';
  try {
    await api('POST', '/api/perch/autosignup/run');
    toast('已触发检查');
    await loadPerchAutosignup();
  } catch (e) {
    $('#pc-auto-error').textContent = '触发检查失败：' + pcRegistrationSafe(e.message);
    toast('触发检查失败', 'error');
  }
  finally { pcAuto.busy = false; pcAutoUpdateRunButton(); }
}

// ---- 导入 ----
function pcImport() {
  return modal({
    title: '导入 Perch 账号',
    wide: true,
    body: `<div class="form">
      <div id="pc-drop" class="drop-zone">把 JSON 文件或 ZIP 拖到这里，或 <label class="link">选择文件<input type="file" id="pc-files" multiple accept=".json,.zip" hidden></label>（可多选）</div>
      <div id="pc-preview" class="mt"></div>
      <label>导入到分组</label><input id="pc-import-group" value="perch" placeholder="perch">
      <span class="hint">同一个 Supabase User ID 视为同一账号；已存在的账号只补 Session，不会覆盖更新的 Session。</span>
    </div>`,
    buttons: [{ label: '关闭', value: 'close' }, {
      label: '开始导入', value: 'import', primary: true,
      action: async () => {
        const st = pcImportState;
        if (!st.files.size) throw new Error('请先选择文件');
        const fd = new FormData();
        for (const [name, file] of st.files) fd.append('files', file, name);
        const group = $('#pc-import-group').value.trim() || 'perch';
        const res = await fetch('/api/perch/import?group=' + encodeURIComponent(group), {
          method: 'POST', headers: { 'X-CSRF-Token': csrf }, credentials: 'same-origin', body: fd,
        });
        const data = await res.json().catch(() => null);
        if (!res.ok) throw new Error((data && data.error) || ('HTTP ' + res.status));
        pcImportState.files.clear();
        await pcShowImportResult(data);
      },
    }],
    onOpen: () => {
      pcImportState.files = new Map();
      const drop = $('#pc-drop');
      const onFiles = async (files) => {
        for (const f of files) pcImportState.files.set(f.name, f);
        await pcPreview();
      };
      drop.addEventListener('dragover', (e) => { e.preventDefault(); drop.classList.add('over'); });
      drop.addEventListener('dragleave', () => drop.classList.remove('over'));
      drop.addEventListener('drop', (e) => { e.preventDefault(); drop.classList.remove('over'); onFiles(e.dataTransfer.files); });
      $('#pc-files').addEventListener('change', (e) => onFiles(e.target.files));
    },
  });
}
const pcImportState = { files: new Map() };

async function pcPreview() {
  if (!pcImportState.files.size) { $('#pc-preview').innerHTML = ''; return; }
  const fd = new FormData();
  for (const [name, file] of pcImportState.files) fd.append('files', file, name);
  const res = await fetch('/api/perch/import/preview', { method: 'POST', headers: { 'X-CSRF-Token': csrf }, credentials: 'same-origin', body: fd });
  const data = await res.json().catch(() => null);
  if (!res.ok) { $('#pc-preview').innerHTML = `<div class="error">${esc((data && data.error) || '预览失败')}</div>`; return; }
  const items = data.items || [];
  const rows = items.map((it) => {
    if (!it.ok) return `<tr><td class="small">${esc(it.file)}</td><td><span class="badge bad">无效</span></td><td colspan="4" class="small">${esc(it.reason)}</td></tr>`;
    const tag = it.duplicateOf === 'stored' ? '<span class="badge warn">已在池中</span>'
      : it.duplicateOf ? '<span class="badge warn">与同批重复</span>' : '<span class="badge ok">新账号</span>';
    return `<tr><td class="small">${esc(it.file)}</td><td>${tag}</td><td>${esc(it.email)}</td><td class="small">${esc(it.userId)}</td><td class="small">${esc(it.expiresAt ? fmtTime(it.expiresAt, true) : '未知')}${it.expired ? ' <span class="badge warn">已过期</span>' : ''}</td><td>${it.hasRefresh ? '<span class="badge ok">可续期</span>' : '<span class="badge bad">无刷新令牌</span>'}</td></tr>`;
  }).join('');
  const ok = items.filter((i) => i.ok).length;
  $('#pc-preview').innerHTML = `<div class="head-row"><h3>预览</h3><span class="muted small">${data.files} 个文件 · 有效 ${ok} · 失败 ${data.failed}</span></div>
    <div class="table-wrap"><table class="table"><thead><tr><th>文件</th><th>判定</th><th>邮箱</th><th>User ID</th><th>到期</th><th>会话</th></tr></thead><tbody>${rows}</tbody></table></div>`;
}

async function pcShowImportResult(data) {
  const s = data.summary || {};
  const rows = (s.outcomes || []).map((o) => `<tr><td class="small">${esc(o.file)}</td><td>${esc(o.email)}</td><td>${esc(o.status)}</td><td class="small">${esc(o.reason || '')}</td></tr>`).join('');
  $('#pc-preview').innerHTML = `<div class="panel">
    <h3>导入完成</h3>
    <p>新增 <b>${s.created}</b> · 更新 <b>${s.updated}</b> · 重复 <b>${s.duplicate}</b> · 跳过 <b>${s.skipped}</b> · 失败 <b>${s.failed}</b></p>
    <div class="table-wrap"><table class="table"><thead><tr><th>文件</th><th>邮箱</th><th>结果</th><th>说明</th></tr></thead><tbody>${rows}</tbody></table></div></div>`;
  await loadPerchAccounts();
}

// ---- 批量 ----
// pcBatchProxyDialog collects one proxy for a whole batch. It resolves to the
// URL to set, an empty string to clear (direct), or null when cancelled — an
// empty string is a real choice here, so it must not read as "cancelled".
function pcBatchProxyDialog() {
  return modal({
    title: '批量修改代理',
    body: `<div class="form">
      <label for="pc-batch-proxy-url">新的代理地址</label>
      <input id="pc-batch-proxy-url" type="password" autocomplete="off" placeholder="http:// 或 socks5://用户:密码@主机:端口">
      <label class="check"><input type="checkbox" id="pc-batch-proxy-direct"> 清除代理，改为直连</label>
      <span class="hint">所选账号会统一使用这一个代理（包括按状态筛选出的账号）。只影响之后的请求，已在途的请求不会中断；原凭据不会回填。</span>
    </div>`,
    buttons: [{ label: '取消', value: 'cancel' }, { label: '执行', value: 'save', primary: true, action: async () => {
      if ($('#pc-batch-proxy-direct').checked) return;
      if (!$('#pc-batch-proxy-url').value.trim()) throw new Error('请填写完整代理地址，或勾选改为直连');
    } }],
  }).then((v) => {
    if (v !== 'save') return null;
    if ($('#pc-batch-proxy-direct').checked) return '';
    return $('#pc-batch-proxy-url').value.trim();
  });
}

async function pcBatch() {
  const action = $('#pc-batch-action').value;
  if (!action) { toast('先选择要执行的批量操作'); return; }
  const scope = $('#pc-batch-scope').value;
  let extra = {};
  if (action === 'delete' && !confirm('确认删除？账号与其 Session 都会移除。')) return;
  if (action === 'group') {
    const g = prompt('移动到分组：', 'perch');
    if (!g) return;
    extra.group = g.trim();
  }
  if (action === 'tags') {
    const t = prompt('设置标签（逗号分隔，留空清除）：', '');
    if (t === null) return;
    extra.tags = t.split(/[,，]/).map((x) => x.trim()).filter(Boolean);
  }
  if (action === 'proxy') {
    const proxy = await pcBatchProxyDialog();
    if (proxy === null) return;
    extra.proxyUrl = proxy;
  }
  const ids = Array.from(pcState.selected);
  if (scope === 'selected' && !ids.length) { toast('先勾选账号'); return; }
  const body = { action, filter: scope === 'selected' ? '' : scope, ids: scope === 'selected' ? ids : [], ...extra };
  const res = await api('POST', '/api/perch/batch', body);
  const label = action === 'proxy' ? '修改代理' : action;
  const rows = (res.results || []).map((r) => `<tr><td class="small">${esc(r.email || r.id)}</td><td>${r.ok ? '<span class="badge ok">成功</span>' : '<span class="badge bad">失败</span>'}</td><td class="small">${esc(r.detail || '')}</td></tr>`).join('');
  modal({ title: `批量${label}：成功 ${res.ok} · 失败 ${res.failed}`, wide: true,
    body: `<div class="table-wrap"><table class="table"><thead><tr><th>账号</th><th>结果</th><th>说明</th></tr></thead><tbody>${rows}</tbody></table></div>` });
  await loadPerchAccounts(true);
}

// ---- 设置 ----
async function loadPerchSettings() {
  const [out, fingerprint] = await Promise.all([
    api('GET', '/api/perch/settings'),
    api('GET', '/api/perch/fingerprint').catch(() => null),
  ]);
  const p = out.perch;
  $('#pc-set-rev').textContent = '';
  $('#form-perch-refresh').innerHTML = pcFields([
    ['supabaseKey', 'Supabase Publishable Key', 'text', '留空则用内置的 perchai 公共 key；账号 JSON 自带的会优先生效'],
    ['appUrl', 'Perch App URL', 'text', 'https://app.perchai.app'],
    ['refreshMarginMin', '提前续期（分钟）', 'number', 'Access Token 到期前多少分钟开始续期'],
    ['refreshTickSec', '续期检查间隔（秒）', 'number', ''],
    ['refreshConcurrency', '续期并发数', 'number', ''],
    ['retries', '临时错误重试次数', 'number', '网络/5xx 才重试，令牌被拒不重试'],
    ['retryBackoffMs', '重试退避（毫秒）', 'number', ''],
  ], p);
  $('#form-perch-monitor').innerHTML = pcFields([
    ['checkIntervalSec', '状态检测间隔（秒）', 'number', '用 GoTrue /user 校验，不消耗模型额度'],
    ['checkConcurrency', '批量检测并发数', 'number', ''],
    ['quotaIntervalSec', '额度同步间隔（秒）', 'number', '读 usage meter，不消耗模型额度'],
  ], p);
  // 模型调用（OpenAI 兼容代理）相关
  $('#form-perch-gate').innerHTML = pcFields([
    ['surface', '调用通道（surface）', 'select', ['cli', 'web'],
      'cli（默认）：走 model-call 信封，支持 system/tools/images，要求首条 system 消息开头 ≥200 字符（留空用自带前缀）；web：走网页 /api/perchai/chat，无需前缀但一条请求只能带一条用户消息，且不支持 tools'],
    ['gatePrefix', 'Surface Gate 前缀（仅 cli 通道）', 'textarea', '留空使用本服务自带的前缀（已实测通过）。自己填时必须 ≥200 字符，且后面接的说明文字也可能触发 403，改完请先跑一次真实调用'],
    ['lane', 'Lane', 'text', 'code 不会注入 Perch 的 operator persona；chat/writer/report 会'],
    ['gateNotice', '挡板说明（前缀之后）', 'textarea', '说明上面那段是无效样板。文案会影响 surface gate：实测某些措辞会让所有请求 403，改完请先验证'],
    ['identityGuard', '身份保护（最后）', 'textarea', '放在调用方指令之后，优先级最高，禁止模型自称 Saffron/Perch'],
    ['neutralSystem', '默认 system（调用方没给时）', 'textarea', ''],
  ], p);
  $('#form-perch-gate').innerHTML += `<label class="row"><input type="checkbox" data-pc-k="exposeReasoning" ${p.exposeReasoning ? 'checked' : ''}> 透传 reasoning_content</label>`;
  $('#form-perch-delta').innerHTML = pcFields([
    ['deltaTtlMin', '前缀信任窗口（分钟）', 'number', '服务端确认「已持有」的消息前缀保持多久仍发增量。0 用默认 9（官方 CLI 的窗口）。调大只对闲置超过窗口的对话有效；超过服务端自己的记忆则会被 409 拒一次再发全量'],
    ['deltaMaxKeys', '缓存对话数上限', 'number', '每个对话前缀约「每条消息一个 SHA-256」，长会话的缓存会明显更大。0 用默认 4096'],
  ], p);
  $('#pc-delta-live').innerHTML = pcDeltaLive(fingerprint);
  $('#form-perch-logs').innerHTML = pcFields([
    ['logRetentionDays', '日志保留天数', 'number', ''],
    ['adminReadOnlyView', '隐藏 Token 尾号', 'checkbox', ''],
    ['alertOnFailure', '异常账号提醒', 'checkbox', ''],
  ], p);
  const st = out.store || {};
  $('#pc-store').innerHTML = `已保存 Session：<b>${st.stored || 0}</b> 个 · 已绑定账号：<b>${st.bound || 0}</b> · 即将需要续期：<b>${st.due || 0}</b><br>
    Session 加密保存在 data/perch-secrets.json（AES-GCM），config.json 与页面都不含 Token。`;
  $('#pc-store').insertAdjacentHTML('beforeend', pcFingerprintProxyReport(fingerprint));
  await loadPerchLogs();
}

// pcDeltaLive renders the held-context delta counters: whether the pool is
// actually sending only what is new, and what it is sized to.
function pcDeltaLive(report) {
  const d = report && report.delta;
  if (!d) return '增量诊断暂不可用。';
  const pct = (v) => `${(Math.round((v || 0) * 1000) / 10).toFixed(1)}%`;
  const lim = d.limits || {};
  const total = (d.deltaRequests || 0) + (d.wholeRequests || 0);
  return `当前生效：信任窗口 <b>${esc(lim.ttlMinutes ?? '?')}</b> 分钟 · 缓存上限 <b>${esc(lim.maxKeys ?? '?')}</b> 个对话<br>
    已缓存对话 <b>${esc(d.cachedKeys ?? 0)}</b> / 快照 <b>${esc(d.cachedSnapshots ?? 0)}</b> · 跟踪中的 runId <b>${esc(d.trackedConversations ?? 0)}</b><br>
    增量 <b>${esc(d.deltaRequests ?? 0)}</b> · 全量 <b>${esc(d.wholeRequests ?? 0)}</b> · 命中率 <b>${pct(d.hitRate)}</b>（共 ${esc(total)} 次）<br>
    被拒 <b>${esc(d.refusedDeltas ?? 0)}</b> · 少发 <b>${esc(d.messagesOmitted ?? 0)}</b> 条消息 / <b>${esc(d.bytesOmitted ?? 0)}</b> 字节<br>
    <span class="hint">被拒持续增长说明服务端不再持有该前缀——调大信任窗口会让这个数字变高，因为它声称了服务端已经丢掉的前缀。</span><br>
    <span class="hint">上面是累计计数，只看得到"发了多少"，看不到"哪一条为什么是全量"。每次模型调用的具体判定在 perch 日志的 <code>deltaSent</code> / <code>deltaReason</code> / <code>deltaHeld</code> / <code>deltaRefused</code> 字段里，按 <code>deltaReason</code> 分组统计即可。</span>`;
}

function pcFingerprintProxyReport(report) {
  if (!report || !Array.isArray(report.accounts)) return '<h2 class="mt">代理来源</h2><p>诊断暂不可用。</p>';
  const label = (source) => ({
    account_proxy: '显式账号代理',
    unconfigured_unknown: '未配置账号代理 / 实际路径未知',
  }[source] || '未知');
  const rows = report.accounts.map((a) => `<tr><td>${esc(a.name || a.id)}</td><td>${esc(label(a.proxySource))}</td></tr>`).join('');
  return `<h2 class="mt">代理来源</h2>
    <p>常规请求无显式账号代理时，默认客户端可能使用进程环境代理；选择取决于目标和 NO_PROXY，实际路径未知。注册和激活使用独立网络配置：其默认实现仅在提供显式代理时使用代理，否则不使用环境代理。此处不代表历史请求或实际出口。</p>
    <div class="table-wrap"><table class="table"><thead><tr><th>账号</th><th>常规请求配置</th></tr></thead><tbody>${rows || '<tr><td colspan="2">暂无账号</td></tr>'}</tbody></table></div>`;
}

function pcFields(list, obj) {
  return list.map(([k, label, type, hint]) => {
    if (type === 'textarea') {
      return `<label>${esc(label)}</label><textarea data-pc-k="${k}" rows="3">${esc(obj[k] ?? '')}</textarea>${hint ? `<span class="hint">${esc(hint)}</span>` : ''}`;
    }
    if (type === 'checkbox') {
      return `<label class="row"><input type="checkbox" data-pc-k="${k}" ${obj[k] ? 'checked' : ''}> ${esc(label)}${hint ? `<span class="hint">${esc(hint)}</span>` : ''}</label>`;
    }
    if (type === 'select') {
      const extra = hint || [];
      const v = obj[k] ?? extra[0];
      return `<label>${esc(label)}</label><select data-pc-k="${k}">${extra.map((s) => `<option ${s === v ? 'selected' : ''}>${esc(s)}</option>`).join('')}</select>`;
    }
    return `<label>${esc(label)}</label><input type="text" inputmode="${type === 'number' ? 'numeric' : 'text'}" data-pc-k="${k}" value="${esc(obj[k] ?? '')}"><span class="hint">${esc(hint || '')}</span>`;
  }).join('');
}

async function savePerchSettings() {
  const p = {};
  $$('[data-pc-k]').forEach((el) => {
    const k = el.dataset.pcK;
    if (el.type === 'checkbox') p[k] = el.checked;
    else if (el.tagName === 'TEXTAREA') p[k] = el.value.trim();
    else if (el.inputMode === 'numeric') p[k] = Number(el.value) || 0;
    else p[k] = el.value.trim();
  });
  await api('PUT', '/api/perch/settings', { perch: p });
  toast('设置已保存');
  await loadPerchSettings();
}

function pcEgressDetail(e) {
  if (e.event !== 'egress') return e.detail || '';
  const stage = { register: '注册', activate: '激活', refresh: '刷新', quota: '额度', models: '模型目录', model: '模型调用' }[e.kind] || '未知阶段';
  const source = { account_proxy: '账号代理', explicit_proxy: '显式代理', direct: '直连', unconfigured_unknown: '未配置 / 路径未知' }[e.proxySource] || '路径未知';
  const target = { 'perch-app': 'Perch App', 'perch-chat': 'Perch Chat', 'supabase-default': '默认 Supabase', unknown: '未知目标' }[e.target] || '未知目标';
  const status = Number.isInteger(e.status) && e.status >= 100 && e.status <= 599 ? `HTTP ${e.status}` : '';
  const duration = Number.isFinite(e.durationMs) && e.durationMs >= 0 ? `${Math.floor(e.durationMs)}ms` : '';
  const errorClass = { auth: '认证', rate_limited: '限流', transient: '暂时性故障', rejected: '请求拒绝', not_found: '未找到', timeout: '超时', cancelled: '已取消', transport: '网络', response: '响应', upstream: '上游', unknown: '未知' }[e.errorClass] || '';
  return [stage, source, target, status, duration, errorClass].filter(Boolean).join(' · ');
}

async function loadPerchLogs() {
  const out = await api('GET', '/api/logs/perch?limit=100');
  const rows = (out.items || []).map((e) => `<tr><td class="small">${esc(fmtTime(e.ts, true))}</td><td class="small">${esc(e.email || e.accountId || '—')}</td><td>${esc(e.event)}</td><td>${esc(e.outcome || e.status || '')}</td><td class="small">${esc(pcEgressDetail(e))}</td></tr>`).join('');
  $('#pc-log-table tbody').innerHTML = rows || '<tr><td colspan="5" class="empty">暂无事件</td></tr>';
}

// ---- Perch 密钥 ----
// The Perch pool needs the same two console objects as every other pool: a
// downstream key (`sk-odb-…`) new-api presents, and an import key
// (`sk-odbimp-…`) a signup bot authenticates POST /open/v1/perch/import with.
// Both are ordinary keys scoped to the `perch` group — only their pages are
// Perch's own.
async function loadPerchKeys() {
  const [down, imp] = await Promise.all([
    api('GET', '/api/client-keys?provider=perch'),
    api('GET', '/api/import-keys?provider=perch'),
  ]);
  const base = location.origin;
  $$('.pkey-base').forEach((el) => { el.textContent = base; });
  const url = $('#pik-url');
  if (url) url.textContent = base + '/open/v1/perch/import';
  const curl = $('#pik-curl');
  if (curl) {
    curl.textContent = `curl -X POST ${base}/open/v1/perch/import \\
  -H 'Authorization: Bearer sk-odbimp-…' -H 'Content-Type: application/json' \\
  -d '{"provider":"perchai","account":{"supabase_user_id":"…","email":"a@b.c"},` +
      `"session":{"access_token":"…","refresh_token":"…","expires_at":1800000000}}'`;
  }

  const row = (k, list) => `<tr data-id="${esc(k.id)}">
    <td>${esc(k.name || '—')}</td><td><span class="badge">${esc(k.group)}</span></td><td><code>${esc(k.keyPrefix)}</code></td>
    <td><span class="badge ${k.enabled ? 'ok' : ''}">${k.enabled ? '启用' : '停用'}</span></td><td class="num">${esc(fmtTime(k.createdAt, true))}</td>
    <td class="actions"><button class="btn small" data-k="copy"${k.key ? '' : ' disabled title="该密钥创建于支持复制之前，无法找回，请新建"'}>复制</button><button class="btn small" data-k="toggle">${k.enabled ? '停用' : '启用'}</button><button class="btn small" data-k="edit">编辑</button><button class="btn small danger" data-k="del">删除</button></td></tr>`;

  $('#pk-table tbody').innerHTML = down.keys.map((k) => row(k)).join('');
  $('#pk-empty').classList.toggle('hidden', down.keys.length > 0);
  $('#pik-table tbody').innerHTML = imp.keys.map((k) => row(k)).join('');
  $('#pik-empty').classList.toggle('hidden', imp.keys.length > 0);

  const bind = (tableSel, path, list, title, groupLabel) => $$(tableSel + ' [data-k]').forEach((b) => b.addEventListener('click', async () => {
    const id = b.closest('tr').dataset.id;
    const k = list.find((x) => x.id === id);
    try {
      if (b.dataset.k === 'copy') return copyText(k.key);
      if (b.dataset.k === 'toggle') await api('PUT', `${path}/${id}`, { enabled: !k.enabled });
      if (b.dataset.k === 'del') {
        if (await modal({ title: `删除${title}`, body: `<p>删除后使用 <code>${esc(k.keyPrefix)}</code> 的调用方会立即 401。已导入的账号与已抓取的 Session 不受影响。</p>`, buttons: [{ label: '取消', value: 'c' }, { label: '删除', value: 'd', danger: true }] }) !== 'd') return;
        await api('DELETE', `${path}/${id}`);
      }
      if (b.dataset.k === 'edit') {
        await modal({ title: `编辑${title}`, body: `<div class="form"><label>名称</label><input id="pk-name" value="${esc(k.name)}"><label>${groupLabel}</label><input id="pk-group" value="${esc(k.group)}"></div>`,
          buttons: [{ label: '取消', value: 'c' }, { label: '保存', value: 'ok', primary: true, action: () => api('PUT', `${path}/${id}`, { name: $('#pk-name').value, group: $('#pk-group').value }) }] });
      }
      loadPerchKeys();
    } catch (e) { toast(e.message); }
  }));
  bind('#pk-table', '/api/client-keys', down.keys, '下游密钥', '分组');
  bind('#pik-table', '/api/import-keys', imp.keys, '导入密钥', '默认分组');
}

// pkCreateKey makes one Perch key of either kind and keeps it on screen until
// the console is closed: the key box is the only place the plaintext shows.
function pkCreateKey(kind) {
  const isImport = kind === 'import';
  const path = isImport ? '/api/import-keys' : '/api/client-keys';
  const title = isImport ? '新建 Perch 导入密钥' : '新建 Perch 下游密钥';
  const groupLabel = isImport ? '默认分组' : '分组';
  const hint = isImport
    ? '自动上号脚本用它鉴权 <code>/open/v1/perch/import</code>。请求不带 group 时，Perch 账号导入到这个分组。'
    : 'new-api 的渠道密钥填它。只调度 <code>perch</code> 分组的账号，不会去借别的池。';
  let created = null;
  return modal({ title,
    body: `<div class="form"><label>名称</label><input id="pk-new-name" placeholder="${isImport ? 'signup-bot' : 'new-api-perch'}">
      <label>${groupLabel}</label><input id="pk-new-group" value="perch" placeholder="perch">
      <span class="hint">${hint}</span></div><div id="pk-out"></div>`,
    buttons: [{ label: '关闭', value: 'c' }, {
      label: '创建', value: 'ok', primary: true,
      action: async () => {
        if (created) return false;
        created = await api('POST', path, { name: $('#pk-new-name').value, group: $('#pk-new-group').value, provider: 'perch' });
        $('#pk-out').innerHTML = `<p class="mt"><b>密钥已创建，可随时在列表中复制：</b></p><div class="keybox"><code>${esc(created.key)}</code><button type="button" class="btn small" id="pk-copy">复制</button></div>`;
        $('#pk-copy').onclick = () => copyText(created.key);
        return false;
      },
    }] });
}

// ---- 请求日志 ----
const pcLog = { cursors: [''], page: 0, next: '', items: [], filters: null, busy: false };
async function loadPerchRequests(reset, target) {
  if (pcLog.busy) return;
  const filters = reset ? {
    accountId: $('#pr-account').value.trim(), model: $('#pr-model').value.trim(),
    status: $('#pr-status').value.trim(), result: $('#pr-result').value, protocol: $('#pr-protocol').value,
  } : pcLog.filters;
  const page = reset ? 0 : target;
  const cursor = reset ? '' : pcLog.cursors[page];
  const q = new URLSearchParams({ provider: 'perch', limit: '50' });
  for (const [k, v] of Object.entries(filters || {})) if (v) q.set(k, v);
  if (cursor) q.set('cursor', cursor);
  pcLog.busy = true;
  $('#pr-prev').disabled = $('#pr-next').disabled = true;
  try {
    const data = await api('GET', '/api/logs/requests?' + q);
    if (reset) pcLog.cursors = [''];
    pcLog.filters = filters;
    pcLog.page = page;
    pcLog.items = data.items || [];
    pcLog.next = data.nextCursor || '';
    pcLog.cursors = pcLog.cursors.slice(0, page + 1);
    if (pcLog.next) pcLog.cursors.push(pcLog.next);
    $('#pr-table tbody').innerHTML = pcLog.items.map(requestRow).join('') || '<tr><td colspan="11" class="empty">暂无记录</td></tr>';
    $('#pr-page').textContent = `第 ${page + 1} 页 · ${pcLog.items.length} 条` + (data.dropped ? ` · 日志丢弃 ${num(data.dropped)} 条（全局）` : '');
  } finally {
    pcLog.busy = false;
    $('#pr-prev').disabled = pcLog.page === 0;
    $('#pr-next').disabled = !pcLog.next;
  }
}

// pcDeleteRequestLog removes one Perch request record and reloads this page.
async function pcDeleteRequestLog(requestId) {
  if (!requestId) return;
  if (await modal({ title: '删除日志', body: `<p>确定删除请求 <code>${esc(requestId)}</code> 的日志？只删这一条。</p>`, buttons: [{ label: '取消', value: 'c' }, { label: '删除', value: 'x', danger: true }] }) !== 'x') return;
  await api('DELETE', '/api/perch/logs/log/' + encodeURIComponent(requestId));
  toast('已删除该条日志');
  await loadPerchRequests(false, pcLog.page);
}

// ---- 事件绑定 ----
document.addEventListener('DOMContentLoaded', () => {
  const on = (sel, ev, fn) => { const el = $(sel); if (el) el.addEventListener(ev, fn); };
  const pcLogLoad = (reset, page) => loadPerchRequests(reset, page).catch((e) => toast(e.message));
  on('#pr-search', 'click', () => pcLogLoad(true));
  on('#pr-refresh', 'click', () => pcLogLoad(true));
  on('#pr-prev', 'click', () => { if (pcLog.page > 0) pcLogLoad(false, pcLog.page - 1); });
  on('#pr-next', 'click', () => { if (pcLog.next) pcLogLoad(false, pcLog.page + 1); });
  on('.pc-log-filters', 'keydown', (e) => { if (e.key === 'Enter') pcLogLoad(true); });
  on('#pr-table tbody', 'click', (e) => {
    const del = e.target.closest('.rq-del');
    if (del) { pcDeleteRequestLog(del.dataset.rid).catch((err) => toast(err.message)); return; }
    const tr = e.target.closest('tr');
    if (!tr) return;
    const item = pcLog.items[Array.from(tr.parentElement.children).indexOf(tr)];
    if (item) showJSON('请求 ' + item.requestId, item);
  });
  on('#pc-auto-pick-all', 'change', (e) => {
    pcAuto.picked.clear();
    if (e.target.checked) pcAuto.rows.filter(pcAutoRegisterable).forEach((row) => pcAuto.picked.add(row.id));
    $$('#pc-auto-rows .pc-auto-pick').forEach((box) => { box.checked = pcAuto.picked.has(box.value); });
    pcAutoUpdateRegisterButtons();
  });
  on('#pc-auto-register-selected', 'click', () => pcAutoRegister([...pcAuto.picked]).catch((e) => { $('#pc-auto-register-message').textContent = e.message; }));
  on('#pc-auto-register-all', 'click', () => pcAutoRegister(pcAuto.rows.filter(pcAutoRegisterable).map((row) => row.id)).catch((e) => { $('#pc-auto-register-message').textContent = e.message; }));
  on('#btn-new-pkey', 'click', () => pkCreateKey('client').then(() => loadPerchKeys()).catch((e) => toast(e.message)));
  on('#btn-new-pik', 'click', () => pkCreateKey('import').then(() => loadPerchKeys()).catch((e) => toast(e.message)));
  on('#btn-pc-refresh', 'click', () => loadPerchOverview().catch((e) => toast(e.message)));
  on('#btn-pc-import', 'click', () => pcImport().catch((e) => toast(e.message)));
  on('#btn-pc-import2', 'click', () => pcImport().catch((e) => toast(e.message)));
  on('#btn-pc-register', 'click', () => go('pregister'));
  on('#btn-pc-register-overview', 'click', () => go('pregister'));
  on('#pc-auto-refresh', 'click', () => loadPerchAutosignup());
  on('#pc-auto-run', 'click', pcAutoRun);
  on('#pc-auto-settings', 'input', () => { pcAuto.dirty = true; $('#pc-auto-settings-message').textContent = '有未保存的修改'; });
  on('#pc-auto-settings', 'submit', pcAutoSave);
  on('#pc-auto-candidates', 'submit', pcAutoSubmit);
  on('#pc-auto-proxy-form', 'submit', pcAutoAddProxies);
  on('#pc-auto-proxy-rows', 'click', (e) => {
    const edit = e.target.closest('.pc-auto-proxy-edit');
    if (edit) { pcAutoEditProxy(edit.dataset.id, edit.dataset.proxy).catch((err) => toast(err.message)); return; }
    const button = e.target.closest('.pc-auto-proxy-delete');
    if (button) pcAutoDeleteProxy(button.dataset.id);
  });
  on('#pc-auto-rows', 'click', (e) => {
    const pick = e.target.closest('.pc-auto-pick');
    if (pick) {
      if (pick.checked) pcAuto.picked.add(pick.value); else pcAuto.picked.delete(pick.value);
      pcAutoUpdateRegisterButtons();
      return;
    }
    const remove = e.target.closest('.pc-auto-delete');
    if (remove) pcAutoDelete(remove.dataset.id, remove.dataset.login);
    const proxy = e.target.closest('.pc-auto-candidate-proxy');
    if (proxy) pcAutoEditCandidateProxy(proxy.dataset.id, proxy.dataset.login, proxy.dataset.hasproxy === '1').catch((err) => toast(err.message));
    const importButton = e.target.closest('.pc-auto-import');
    if (importButton) pcAutoImport(importButton.dataset.id, importButton.dataset.login);
    const retry = e.target.closest('.pc-auto-retry-save');
    if (retry) pcAutoRetrySave(retry.dataset.id);
    const activate = e.target.closest('.pc-auto-activate');
    if (activate) pcAutoActivate(activate.dataset.id, activate.dataset.login);
  });
  on('#pc-registration-form', 'submit', pcSubmitRegistration);
  on('#pc-reg-refresh', 'click', () => loadPerchRegistrations());
  on('#pc-register-import', 'click', pcImportRegistration);
  on('#pc-register-jobs', 'click', (e) => {
    const button = e.target.closest('[data-job]');
    if (!button || button.dataset.job === pcRegistration.id) return;
    pcRegistration.id = button.dataset.job;
    pcRegistration.selected.clear();
    $('#pc-register-import-error').textContent = '';
    loadPerchRegistrations();
  });
  on('#pc-register-rows', 'click', (e) => {
    const button = e.target.closest('.pc-register-proxy');
    if (button) pcEditRegistrationProxy(button.dataset.login, button.dataset.status).catch((err) => toast(err.message));
    const imported = e.target.closest('.pc-register-imported-proxy');
    if (imported) pcUpdateProxy(imported.dataset.id).then(() => loadPerchRegistrations()).catch((err) => toast(err.message));
  });
  on('#pc-register-rows', 'change', (e) => {
    if (!e.target.classList.contains('pc-register-pick')) return;
    if (e.target.checked) pcRegistration.selected.add(e.target.value); else pcRegistration.selected.delete(e.target.value);
  });
  window.addEventListener('beforeunload', (e) => {
    if (!$('#pc-register-accounts').value && !$('#pc-register-proxies').value && !$('#pc-auto-accounts').value && !$('#pc-auto-proxies').value && !$('#pc-auto-proxy-input').value) return;
    e.preventDefault();
    e.returnValue = '';
  });
  on('#btn-pc-search', 'click', () => { pcState.page = 1; loadPerchAccounts().catch((e) => toast(e.message)); });
  on('#pc-q', 'keydown', (e) => { if (e.key === 'Enter') { pcState.page = 1; loadPerchAccounts().catch((err) => toast(err.message)); } });
  on('#pc-prev', 'click', () => { if (pcState.page > 1) { pcState.page--; loadPerchAccounts(true).catch((e) => toast(e.message)); } });
  on('#pc-next', 'click', () => { pcState.page++; loadPerchAccounts(true).catch((e) => toast(e.message)); });
  on('#pc-all', 'change', (e) => {
    pcState.selected.clear();
    if (e.target.checked) pcState.rows.forEach((r) => pcState.selected.add(r.id));
    $$('.pc-pick').forEach((cb) => { cb.checked = e.target.checked; });
    $('#pc-count').textContent = `共 ${pcState.total} 个账号 · 已选 ${pcState.selected.size} 个`;
  });
  on('#pc-table', 'change', (e) => {
    const cb = e.target.closest('.pc-pick');
    if (!cb) return;
    cb.checked ? pcState.selected.add(cb.value) : pcState.selected.delete(cb.value);
    $('#pc-count').textContent = `共 ${pcState.total} 个账号 · 已选 ${pcState.selected.size} 个`;
  });
  on('#pc-table', 'click', (e) => {
    const tr = e.target.closest('tr[data-id]');
    if (tr && !e.target.closest('input') && !e.target.closest('select')) pcShowDetail(tr.dataset.id).catch((err) => toast(err.message));
  });
  on('#pc-table', 'change', (e) => {
    const sel = e.target.closest('.pc-actions');
    if (!sel || !sel.value) return;
    const act = sel.value;
    sel.value = '';
    pcAct(sel.dataset.id, act);
  });
  on('#btn-pc-batch', 'click', () => pcBatch().catch((e) => toast(e.message)));
  on('#btn-pc-export', 'click', () => { location.href = '/api/perch/export'; });
  on('#btn-pc-backup', 'click', () => pcBackup().catch((e) => toast(e.message)));
  on('#btn-pc-restore', 'click', () => pcRestore().catch((e) => toast(e.message)));
  on('#btn-pc-save', 'click', () => savePerchSettings().catch((e) => toast(e.message)));
  on('#btn-pc-clear-logs', 'click', () => {
    const before = $('#pc-log-before').value.trim();
    api('DELETE', '/api/perch/logs' + (before ? '?before=' + encodeURIComponent(before) : ''))
      .then((r) => { toast('已清理'); return loadPerchLogs(); }).catch((e) => toast(e.message));
  });
});

async function pcBackup() {
  const pw = prompt('设置备份口令（至少 8 位，恢复时必须输入同样的口令）：');
  if (!pw) return;
  const data = await api('POST', '/api/perch/backup', { passphrase: pw });
  const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' });
  const a = document.createElement('a');
  a.href = URL.createObjectURL(blob);
  a.download = 'perch-backup-' + new Date().toISOString().slice(0, 10) + '.json';
  a.click();
  URL.revokeObjectURL(a.href);
  toast('备份已下载（加密）');
}

function pcRestore() {
  return modal({
    title: '恢复加密备份',
    body: `<div class="form"><label>备份文件</label><input type="file" id="pc-restore-file" accept=".json">
      <label>备份口令</label><input type="password" id="pc-restore-pw"></div>
      <p id="pc-restore-out" class="muted small"></p>`,
    buttons: [{ label: '关闭', value: 'close' }, {
      label: '恢复', value: 'go', primary: true,
      action: async () => {
        const f = $('#pc-restore-file').files[0];
        const pw = $('#pc-restore-pw').value;
        if (!f || !pw) throw new Error('选择文件并输入口令');
        const backup = JSON.parse(await f.text());
        const r = await api('POST', '/api/perch/restore', { passphrase: pw, backup });
        $('#pc-restore-out').textContent = `新建 ${r.created} · 更新 ${r.updated} · 跳过 ${r.skipped} · 失败 ${r.failed}`;
        await loadPerchAccounts();
      },
    }],
  });
}
