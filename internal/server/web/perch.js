// Perch 号池管理台。与 app.js 共用 $ / api / esc / modal，加载顺序在 app.js 之后。
// 该号池只有 Supabase 会话，没有 API Key：页面从不显示任何 Token，只显示脱敏摘要。

const PC_STATUS = {
  normal: ['正常', 'ok'],
  pending: ['待验证', 'warn'],
  'login-expired': ['登录失效', 'bad'],
  'refresh-failed': ['刷新失败', 'bad'],
  restricted: ['套餐受限', 'warn'],
  'quota-exhausted': ['额度耗尽', 'warn'],
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
  const o = await api('GET', '/api/perch/overview');
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
  const out = await api('GET', '/api/perch/accounts?' + qs);
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
    } else if (a.remainingKnown) {
      const src = a.allowanceSource === 'plan_fallback' ? '（按套餐估算，Perch 未明确给出）' : '';
      remaining = `<span class="pc-remain" title="已用 ${esc(usedTxt)} / 上限 ${esc(pcPT(q.allowancePt))}${esc(src)}">${pcPT(a.remainingPt)}</span>`;
    } else {
      remaining = `<span class="pc-remain" title="${esc(a.quotaError || (q.planCode ? 'Perch 没有为该套餐给出月度上限' : '还没有读到额度'))}">未知</span>`;
    }
    const tags = (a.tags || []).map((t) => `<span class="badge">${esc(t)}</span>`).join(' ');
    return `<tr data-id="${esc(a.id)}">
      <td class="pick"><input type="checkbox" class="pc-pick" value="${esc(a.id)}" ${pcState.selected.has(a.id) ? 'checked' : ''}></td>
      <td><div>${esc(a.email || a.name)}</div><div class="muted small">${esc(a.userId)}</div></td>
      <td>${pcBadge(a.status, a.statusDetail)}${a.enabled ? '' : ' <span class="badge off">停用</span>'}</td>
      <td>${esc(a.plan || '未知')}</td>
      <td class="num">${remaining}</td>
      <td class="small">${exp}</td>
      <td class="small">${esc(fetched)}</td>
      <td class="small">${esc(checked)}</td>
      <td class="small">${esc(a.group)} ${tags}</td>
      <td class="actions-cell">
        <select class="pc-actions" data-id="${esc(a.id)}" aria-label="账号操作">
          <option value="">操作…</option>
          <option value="check">检测登录状态</option>
          <option value="refresh">立即续期</option>
          <option value="quota">同步额度</option>
          <option value="session">更新会话</option>
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
        <div class="muted small">Access Token 到期：${esc(s.expiresAt ? fmtTime(s.expiresAt) : '未知')} · 最近刷新：${esc(s.fetchedAt ? fmtTime(s.fetchedAt) : '—')}</div>
        <div class="muted small">Token 尾号：${esc(s.accessSuffix || '—')} · 第 ${esc(s.generation || 0)} 代（仅供区分，不含任何可用凭证）</div>
        ${a.lastError ? `<div class="error">最近错误：${esc(a.lastError)}（${esc(fmtTime(a.lastErrorAt))}）</div>` : ''}
      </div>
      <div>
        <div class="muted small">额度（Perch usage meter，单位 PT = 千分之一美元）</div>
        <div class="muted small">本月已用：${q.usedPt != null ? pcPT(q.usedPt) : '未知'} · 手动调用：${q.manualPt != null ? pcPT(q.manualPt) : '未知'}</div>
        <div class="muted small">月度上限：${a.unlimited ? '不限（内网/管理员账号）' : (q.allowancePt ? pcPT(q.allowancePt) + '（' + ({ entitlement: 'Perch 明确给出', plan_fallback: '按套餐估算' }[a.allowanceSource] || '来源未知') + '）' : '未知')}</div>
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
  const ids = Array.from(pcState.selected);
  if (scope === 'selected' && !ids.length) { toast('先勾选账号'); return; }
  const body = { action, filter: scope === 'selected' ? '' : scope, ids: scope === 'selected' ? ids : [], ...extra };
  const res = await api('POST', '/api/perch/batch', body);
  const rows = (res.results || []).map((r) => `<tr><td class="small">${esc(r.email || r.id)}</td><td>${r.ok ? '<span class="badge ok">成功</span>' : '<span class="badge bad">失败</span>'}</td><td class="small">${esc(r.detail || '')}</td></tr>`).join('');
  modal({ title: `批量${action}：成功 ${res.ok} · 失败 ${res.failed}`, wide: true,
    body: `<div class="table-wrap"><table class="table"><thead><tr><th>账号</th><th>结果</th><th>说明</th></tr></thead><tbody>${rows}</tbody></table></div>` });
  await loadPerchAccounts(true);
}

// ---- 设置 ----
async function loadPerchSettings() {
  const out = await api('GET', '/api/perch/settings');
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
    ['gatePrefix', 'Surface Gate 前缀', 'textarea', 'Perch 要求首条 system 消息以它的 persona 开头，否则一律 403。留空则运行时从本机安装的 perchai-cli 自动派生；本服务不分发 Perch 的文本'],
    ['lane', 'Lane', 'text', 'code 不会注入 Perch 的 operator persona；chat/writer/report 会'],
    ['gateNotice', '挡板说明（前缀之后）', 'textarea', '说明上面那段是无效样板，让模型忽略'],
    ['identityGuard', '身份保护（最后）', 'textarea', '放在调用方指令之后，优先级最高，禁止模型自称 Saffron/Perch'],
    ['neutralSystem', '默认 system（调用方没给时）', 'textarea', ''],
  ], p);
  $('#form-perch-gate').innerHTML += `<label class="row"><input type="checkbox" data-pc-k="exposeReasoning" ${p.exposeReasoning ? 'checked' : ''}> 透传 reasoning_content</label>`;
  $('#form-perch-logs').innerHTML = pcFields([
    ['logRetentionDays', '日志保留天数', 'number', ''],
    ['adminReadOnlyView', '隐藏 Token 尾号', 'checkbox', ''],
    ['alertOnFailure', '异常账号提醒', 'checkbox', ''],
  ], p);
  const st = out.store || {};
  $('#pc-store').innerHTML = `已保存 Session：<b>${st.stored || 0}</b> 个 · 已绑定账号：<b>${st.bound || 0}</b> · 即将需要续期：<b>${st.due || 0}</b><br>
    Session 加密保存在 data/perch-secrets.json（AES-GCM），config.json 与页面都不含 Token。`;
  await loadPerchLogs();
}

function pcFields(list, obj) {
  return list.map(([k, label, type, hint]) => {
    if (type === 'textarea') {
      return `<label>${esc(label)}</label><textarea data-pc-k="${k}" rows="3">${esc(obj[k] ?? '')}</textarea>${hint ? `<span class="hint">${esc(hint)}</span>` : ''}`;
    }
    if (type === 'checkbox') {
      return `<label class="row"><input type="checkbox" data-pc-k="${k}" ${obj[k] ? 'checked' : ''}> ${esc(label)}${hint ? `<span class="hint">${esc(hint)}</span>` : ''}</label>`;
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

async function loadPerchLogs() {
  const out = await api('GET', '/api/logs/perch?limit=100');
  const rows = (out.items || []).map((e) => `<tr><td class="small">${esc(fmtTime(e.ts, true))}</td><td class="small">${esc(e.email || e.accountId || '—')}</td><td>${esc(e.event)}</td><td>${esc(e.outcome || e.status || '')}</td><td class="small">${esc(e.detail || '')}</td></tr>`).join('');
  $('#pc-log-table tbody').innerHTML = rows || '<tr><td colspan="5" class="empty">暂无事件</td></tr>';
}

// ---- 事件绑定 ----
document.addEventListener('DOMContentLoaded', () => {
  const on = (sel, ev, fn) => { const el = $(sel); if (el) el.addEventListener(ev, fn); };
  on('#btn-pc-refresh', 'click', () => loadPerchOverview().catch((e) => toast(e.message)));
  on('#btn-pc-import', 'click', () => pcImport().catch((e) => toast(e.message)));
  on('#btn-pc-import2', 'click', () => pcImport().catch((e) => toast(e.message)));
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
