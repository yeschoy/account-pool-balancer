const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, 'web', 'tokenharbor.js'), 'utf8');
const tick = () => new Promise(setImmediate);
function environment(api) {
  const nodes = new Map();
  const timers = new Map();
  let nextTimer = 1;
  const node = (selector) => {
    if (!nodes.has(selector)) {
      const classes = new Set();
      nodes.set(selector, { innerHTML: '', textContent: '', checked: false, value: '2', disabled: false, scrollHeight: 0, scrollTop: 0, clientHeight: 0,
        listeners: {}, attributes: {}, dataset: {}, setAttribute(key, value) { this.attributes[key] = value; }, addEventListener(event, handler) { this.listeners[event] = handler; }, querySelector() { return null; },
        classList: { add(c) { classes.add(c); }, remove(c) { classes.delete(c); }, contains(c) { return classes.has(c); }, toggle(c, yes) { if (yes) classes.add(c); else classes.delete(c); } },
      });
    }
    return nodes.get(selector);
  };
  const context = { $: node, api, currentPage: 'other', URL, console, crypto: require('node:crypto').webcrypto,
    esc: (s) => String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]),
    fmtTime: () => 'time',
    setTimeout(fn) { const id = nextTimer++; timers.set(id, fn); return id; }, clearTimeout(id) { timers.delete(id); },
  };
  vm.createContext(context);
  vm.runInContext(source + '\nglobalThis.state = thState; globalThis.log = thLog;', context);
  return { context, nodes, timers, node };
}

async function testResendVerificationAndVisibleInputs() {
  const calls = [];
  const e = environment(async (method, url) => {
    calls.push({ method, url });
    return { seq: 0, entries: [] };
  });
  const notices = [];
  e.context.toast = (text) => notices.push(text);
  const row = { id: 'mail/one', status: 'unverified', hasSession: true, verified: false };
  assert.match(e.context.thRowActions(row), /重发验证码/);
  assert.doesNotMatch(e.context.thRowActions({ ...row, verified: true }), /重发验证码/);
  assert.doesNotMatch(e.context.thRowActions({ ...row, hasSession: false }), /重发验证码/);
  assert.doesNotMatch(e.context.thRowActions({ ...row, status: 'running' }), /重发验证码/);
  await e.context.thRowAction('resend-verification', row.id);
  const writes = calls.filter((c) => c.method === 'POST');
  assert.equal(writes.length, 1);
  assert.equal(writes[0].url, '/api/tokenharbor/registrations/mail%2Fone/resend-verification');
  assert.match(notices[0], /补验证/);
  const html = fs.readFileSync(path.join(__dirname, 'web', 'index.html'), 'utf8');
  for (const id of ['th-register-accounts', 'th-register-proxies', 'th-register-session-proxies']) {
    const tag = html.match(new RegExp(`<textarea id="${id}"[^>]*>`))?.[0];
    assert.ok(tag, `${id} input exists`);
    assert.doesNotMatch(tag, /pc-secret-input/, `${id} must not render pasted text transparent`);
  }
}

async function testLogSingleFlightAndSequenceDedupe() {
  const pending = [];
  const e = environment(() => new Promise((resolve) => pending.push(resolve)));
  e.context.thLogShow(); e.context.thLogShow(); e.context.thLogShow();
  assert.equal(pending.length, 1, 'show must not create concurrent initial polls');
  assert.equal(e.timers.size, 0, 'only the completed poll schedules the next timer');
  pending.shift()({ seq: 2, entries: [{ seq: 1, detail: 'once' }, { seq: 1, detail: 'duplicate' }, { seq: 2, detail: 'second' }] });
  await tick();
  assert.equal(e.context.log.lines.length, 2);
  assert.equal(e.timers.size, 1);
  e.context.thLogShow();
  assert.equal(pending.length, 0, 'show must reuse the existing scheduled poll');
  const [timer, run] = e.timers.entries().next().value;
  e.timers.delete(timer); run(); e.context.thLogShow();
  assert.equal(pending.length, 1);
  pending.shift()({ seq: 2, entries: [{ seq: 1 }, { seq: 2 }] });
  await tick();
  assert.equal(e.context.log.lines.length, 2, 'repeated server entries must not render twice');
  assert.equal(e.timers.size, 1);
  // A server restart resets its in-memory sequence, not the polling chain.
  const [next, poll] = e.timers.entries().next().value;
  e.timers.delete(next); poll();
  pending.shift()({ seq: 1, entries: [{ seq: 1, detail: 'new server' }] });
  await tick();
  assert.equal(e.context.log.seq, 1);
  assert.equal(e.context.log.lines.length, 3);
  e.context.thLogHide();
  assert.equal(e.node('#th-logbox').classList.contains('hidden'), true);
  assert.equal(e.context.log.visible, false);
  assert.equal(e.node('#th-log-open').classList.contains('hidden'), false, 'collapsed panel must leave a reopen button');
  e.node('#th-log-open').listeners.click();
  assert.equal(e.context.log.visible, true);
  assert.equal(e.node('#th-logbox').classList.contains('hidden'), false);
  assert.equal(e.node('#th-log-open').classList.contains('hidden'), true);
  assert.equal(e.context.log.lines.length, 3, 'reopening retains previous logs');
  assert.equal(e.timers.size, 1, 'reopening must not start another polling chain');
}

async function testBatchBoundedPartialAndNoRetry() {
  let active = 0, peak = 0;
  const waiting = [], calls = [], results = [];
  const e = environment((method, url, body) => {
    calls.push({ method, url, body }); active++; peak = Math.max(peak, active);
    return new Promise((resolve, reject) => waiting.push(() => { active--; url.includes('/row2/') ? reject(new Error('mock failure')) : resolve({ status: 'queued' }); }));
  });
  const rows = Array.from({ length: 8 }, (_, i) => ({ id: 'row' + i, email: 'a' + i + '@mock.test', status: 'saved' }));
  rows.push({ id: 'running', status: 'running' });
  const work = e.context.thBatchRun('retry', rows, (r) => results.push(r));
  assert.equal(calls.length, 3);
  await assert.rejects(e.context.thBatchRun('retry', rows), /已有操作/);
  while (results.length < rows.length) {
    waiting.splice(0).forEach((release) => release());
    await tick();
  }
  const counts = await work;
  assert.equal(peak, 3);
  assert.equal(calls.length, 8, 'one request per eligible row, no replay');
  assert.equal(counts.success, 7); assert.equal(counts.failed, 1); assert.equal(counts.skipped, 1);
  assert.equal(e.context.state.batchBusy, false);
  assert.match(e.context.state.batchMessage, /已排队 7/);
  assert.ok(calls.every((c) => c.method === 'POST' && c.body.concurrency === 2 && c.body.solveCaptcha === false));
}

async function testCheckoutReuseAndUncertainStops() {
  const calls = [];
  const e = environment(async (method, url, body) => {
    calls.push({ method, url, body });
    if (url.includes('/uncertain/')) throw new Error('上次结果不确定');
    return { url: 'https://checkout.stripe.com/c/pay/cs_mock_only' };
  });
  await e.context.thBatchCall('checkout', { id: 'saved', hasCheckout: true });
  await e.context.thBatchCall('checkout', { id: 'new', hasCheckout: false });
  await assert.rejects(e.context.thBatchCall('checkout', { id: 'uncertain', hasCheckout: false }), /不确定/);
  assert.equal(calls.length, 3);
  assert.equal(calls[0].method, 'GET');
  assert.equal(calls[1].body.product, 'starter_pass_monthly');
  assert.equal(calls[1].method, 'POST');
  assert.ok(calls.every((c) => !c.body?.confirmNoPendingOrder), 'batch checkout never confirms uncertain recovery');
  assert.throws(() => e.context.thCheckoutURL('https://evil.test/c/pay/x'), /不可信/);
  assert.throws(() => e.context.thCheckoutURL('javascript:alert(1)'), /不可信/);
  assert.equal(e.context.thBatchEligible('checkout', { status: 'saved', hasSession: false }), false);
  assert.equal(e.context.thBatchEligible('subscription', { status: 'queued', hasSession: true }), false);
  assert.equal(e.context.thBatchEligible('subscription', { status: 'registered', hasSession: true }), true);
  assert.equal(JSON.stringify(e.context.state).includes('stripe'), false, 'URLs must not enter registration state');
}

async function testCheckoutRecoveryRequiresExplicitOneShotConfirmation() {
  const calls = [];
  const e = environment(async (method, url, body) => {
    calls.push({ method, url, body });
    if (calls.length < 3) throw new Error('上次结果不确定，请先核对官网账单');
    return { url: 'https://checkout.stripe.com/c/pay/cs_recovery_mock' };
  });
  e.context.state.rows = [{ id: 'legacy', email: '<legacy>@mock.test', hasCheckout: false }];
  let options;
  e.context.modal = async (input) => { options = input; };
  let displayed = 0;
  e.context.thCheckoutLink = () => { displayed++; };
  await e.context.thRowCheckout('legacy');
  assert.equal(calls.length, 0, 'opening or cancelling a modal cannot create an order');
  assert.match(options.body, /已核对账单/);
  assert.match(options.body, /&lt;legacy&gt;/);
  const create = options.buttons.find((b) => b.value === 'create').action;
  const confirmation = e.node('#th-checkout-confirm-no-order');
  await assert.rejects(create(), /不确定/);
  assert.equal(calls[0].body.confirmNoPendingOrder, false, 'recovery must not be auto-confirmed');
  confirmation.checked = true;
  await assert.rejects(create(), /不确定/);
  assert.equal(calls[1].body.confirmNoPendingOrder, true);
  assert.equal(confirmation.checked, false, 'a failed recovery consumes its confirmation');
  await assert.rejects(create(), /不确定/);
  assert.equal(calls.length, 2, 'a later unchecked click is blocked locally and cannot replay confirmation');
  confirmation.checked = true;
  e.node('#modal-error').textContent = '上次结果不确定';
  await create();
  await create();
  assert.equal(e.node('#modal-error').textContent, '', 'successful recovery clears the previous error');
  assert.equal(calls.length, 3, 'successful creation must not repeat in the same modal');
  assert.equal(displayed, 1);
  assert.equal(JSON.stringify(e.context.state).includes('stripe'), false);
}

async function testOverviewEquivalentAmountsAndUnknown() {
  const allowance = { totalEquivalentUsd: 40, availableWeeklyEquivalentUsd: 3.75, agentAccounts: 2, measuredWeeklyAccounts: 1, unknownAccounts: 1, otherPlanAccounts: 1, basis: '2×Boost等效值，不是钱包余额' };
  const e = environment(async (method, url) => url === '/api/tokenharbor/overview' ? allowance : url.startsWith('/api/accounts') ? { accounts: [] } : { stats: { total: {} } });
  await e.context.loadTHOverview();
  assert.match(e.node('#th-cards').innerHTML, /\$40\.00/);
  assert.match(e.node('#th-cards').innerHTML, /\$3\.75/);
  assert.match(e.node('#th-quota-basis').textContent, /不是钱包余额/);
  allowance.availableWeeklyEquivalentUsd = null;
  await e.context.loadTHOverview();
  assert.match(e.node('#th-cards').innerHTML, /未知/);
  assert.doesNotMatch(e.node('#th-cards').innerHTML, /\$0\.00/);
}

async function testImportedFilterAndReturn() {
  const calls = [];
  const e = environment(async (method, url) => { calls.push({ method, url }); return { attempt: 'job', id: 'paid', status: 'registered' }; });
  const job = { id: 'job', rows: [{ id: 'draft', email: 'draft@mock.test', status: 'saved' },
    { id: 'paid', email: 'paid@mock.test', status: 'imported', accountId: 'acc_mock' }] };
  e.context.state.job = job.id;
  e.context.state.jobs = [job];
  e.context.renderTHJob(job);
  assert.equal(e.context.state.rows.length, 1);
  assert.equal(e.context.state.rows[0].id, 'draft');
  assert.equal(e.node('#th-filter-imported').textContent, '已入池（1）');
  assert.doesNotMatch(e.node('#th-register-rows').innerHTML, /paid@mock.test/);
  e.context.state.selected.add('draft');
  e.node('#th-filter-imported').listeners.click();
  assert.equal(e.context.state.filter, 'imported');
  assert.equal(e.context.state.selected.size, 0);
  assert.equal(e.context.state.rows[0].id, 'paid');
  assert.match(e.node('#th-register-rows').innerHTML, /退回注册机/);
  e.context.state.batchBusy = true;
  e.node('#th-filter-pending').listeners.click();
  assert.equal(e.context.state.filter, 'imported', 'busy batches cannot change selection scope');
  e.context.state.batchBusy = false;
  e.context.modal = async () => 'cancel';
  await e.context.thReturnToRegistration('acc_mock', 'paid@mock.test');
  assert.equal(calls.length, 0, 'cancelled return must not remove a pool account');
  let page;
  e.context.go = (next) => { page = next; };
  e.context.toast = () => {};
  e.context.modal = async () => 'return';
  await e.context.thReturnToRegistration('acc_mock', 'paid@mock.test');
  assert.equal(calls.length, 1);
  assert.equal(calls[0].url, '/api/accounts/acc_mock/return-to-registration');
  assert.equal(e.context.state.filter, 'pending');
  assert.equal(e.context.state.job, 'job');
  assert.equal(page, 'thregister');
  job.rows[1].status = 'registered';
  e.context.renderTHJob(job);
  assert.equal(e.context.state.rows.length, 2, 'returned accounts reappear in the default view');
}

async function testRegistrationDefaultTaskAndRetention() {
  const recent = { id: 'recent', rows: [{ id: 'one', status: 'saved' }] };
  const running = { id: 'running', running: true, rows: [{ id: 'two', status: 'running' }] };
  let jobs = [recent, running];
  const calls = [];
  const e = environment(async (method, url) => { calls.push({ method, url }); return { jobs }; });
  e.context.log.started = true;
  e.node('#th-logbox').classList.add('hidden');
  await e.context.loadTHRegistrations();
  assert.equal(e.context.state.job, 'running', 'initial load expands an active task even when a newer saved task exists');
  assert.equal(e.node('#th-register-detail').classList.contains('hidden'), false);
  e.context.state.job = 'recent';
  e.context.state.selected.add('one');
  await e.context.loadTHRegistrations();
  assert.equal(e.context.state.job, 'recent', 'polling preserves the manually selected task');
  assert.equal(e.context.state.selected.has('one'), true);
  jobs = [running];
  await e.context.loadTHRegistrations();
  assert.equal(e.context.state.job, 'running');
  assert.equal(e.context.state.selected.size, 0, 'switching away from a deleted task clears its selection');
  jobs = [recent];
  await e.context.loadTHRegistrations();
  assert.equal(e.context.state.job, 'recent', 'no active task falls back to the latest task');
  jobs = [];
  await e.context.loadTHRegistrations();
  assert.equal(e.context.state.job, '');
  assert.equal(e.node('#th-register-detail').classList.contains('hidden'), true);
  assert.equal(e.node('#th-logbox').classList.contains('hidden'), true, 'task refresh must not reopen manually hidden logs');
  assert.ok(calls.every((c) => c.method === 'GET'), 'expanding tasks must not register accounts');
}

async function testBatchImportPartialAndScoped() {
  const calls = [];
  const e = environment(async (method, url, body) => {
    calls.push({ method, url, body });
    if (url.includes('/expired/')) throw new Error('套餐已失效');
    return { status: 'imported' };
  });
  const paid = (id) => ({ id, email: id + '@mock.test', status: 'registered', verified: true, subscription: { status: 'active' } });
  const rows = [paid('paid'), paid('expired'), { ...paid('free'), subscription: { status: 'free' } },
    { ...paid('unverified'), verified: false }, { ...paid('imported'), status: 'imported' }, paid('not-selected')];
  e.context.state.rows = rows;
  rows.slice(0, -1).forEach((r) => e.context.state.selected.add(r.id));
  e.node('#th-register-group').value = 'tokenharbor';
  e.context.thUpdateSelection();
  assert.equal(e.node('#th-batch-import').disabled, false);
  let prompt;
  e.context.modal = async (options) => { prompt = options; return 'import'; };
  await e.context.thBatchAction('import');
  assert.match(prompt.body, /重新检测当前套餐/);
  assert.equal(calls.length, 2, 'only selected, verified, last-observed paid rows are submitted once');
  assert.ok(calls.every((c) => c.method === 'POST' && c.url.endsWith('/import') && c.body.group === 'tokenharbor'));
  assert.match(e.context.state.batchMessage, /成功 1 · 失败 1 · 跳过 3/);
  assert.match(e.node('#th-register-import-error').textContent, /expired@mock.test：套餐已失效/);
  e.context.state.selected.clear();
  e.context.state.selected.add('free');
  e.context.thUpdateSelection();
  assert.equal(e.node('#th-batch-import').disabled, true);
}

async function testCancelledBatchDoesNotMutate() {
  let calls = 0;
  const e = environment(async () => { calls++; });
  e.context.state.rows = [{ id: 'one', status: 'saved' }];
  e.context.state.selected.add('one');
  e.context.modal = async () => 'cancel';
  await e.context.thBatchAction('retry');
  await e.context.thBatchAction('import');
  assert.equal(calls, 0);
  assert.equal(e.context.state.batchPrompt, false);
  assert.equal(e.context.state.batchBusy, false);
}

async function testRowGuardAndSelectionRetention() {
  const e = environment(async () => ({}));
  let calls = 0, release;
  const first = e.context.thGuardRow('one', () => { calls++; return new Promise((r) => { release = r; }); });
  await e.context.thGuardRow('one', () => { calls++; });
  assert.equal(calls, 1);
  await assert.rejects(e.context.thBatchRun('retry', []), /已有操作/);
  release(); await first;
  assert.equal(e.context.state.pending.size, 0);
  e.context.state.selected.add('one'); e.context.state.selected.add('deleted');
  const job = { id: 'job', rows: [{ id: 'one', email: '<one>@mock.test', status: 'saved' }] };
  e.context.renderTHJob(job);
  assert.equal(e.context.state.selected.size, 1);
  assert.equal(e.node('#th-select-all').checked, true);
  assert.match(e.node('#th-register-rows').innerHTML, /&lt;one&gt;/);
  assert.doesNotMatch(e.node('#th-register-rows').innerHTML, /<one>/);
  assert.equal(e.node('#th-batch-register').disabled, false);
}

async function testIndependentProxyEditing() {
  const calls = [];
  const e = environment(async (method, url, body) => { calls.push({ method, url, body }); });
  e.context.toast = () => {};
  let dialog;
  e.context.modal = async (options) => { dialog = options; return 'cancel'; };
  const idle = ['saved', 'registered', 'unverified', 'failed', 'uncertain'];
  const job = { id: 'job', rows: [...idle, 'queued', 'running'].map((status) => ({ id: status, status, email: '<mock>@test', proxy: 'http://mock:8080' })) };
  e.context.renderTHJob(job);
  const html = e.node('#th-register-rows').innerHTML;
  for (const id of idle) assert.match(html, new RegExp('th-row-proxy[^>]*data-id="' + id + '"'));
  for (const id of ['queued', 'running']) {
    assert.doesNotMatch(html, new RegExp('th-row-proxy[^>]*data-id="' + id + '"'));
    await assert.rejects(e.context.thRowProxy(id), /不能修改代理/);
  }
  await e.context.thRowProxy('registered');
  assert.equal(calls.length, 0, 'cancel never submits proxy changes');
  assert.match(dialog.body, /不影响已入池账号/);
  assert.match(dialog.body, /&lt;mock&gt;/);
  e.node('#th-new-proxy').value = '';
  await assert.rejects(dialog.buttons[1].action(), /请填写代理/);
  assert.equal(calls.length, 0, 'empty input cannot silently clear a proxy');
  e.node('#th-new-proxy').value = ' socks5h://user:mock-only@127.0.0.1:1080 ';
  await dialog.buttons[1].action();
  assert.equal(calls[0].url, '/api/tokenharbor/registrations/registered/proxy');
  assert.equal(calls[0].body.proxyUrl, 'socks5h://user:mock-only@127.0.0.1:1080');
  e.node('#th-proxy-direct').checked = true;
  await dialog.buttons[1].action();
  assert.equal(calls[1].body.proxyUrl, '');
  e.context.state.filter = 'imported';
  e.context.renderTHJob({ id: 'job', rows: [{ id: 'imported', status: 'imported', accountId: 'pool', email: 'pool@mock.test' }] });
  assert.match(e.node('#th-register-rows').innerHTML, /th-row-proxy/);
  await e.context.thPoolProxy({ id: 'pool/safe', name: '<pool>', proxyUrl: 'http://masked:8080' });
  assert.match(dialog.body, /不影响注册机原记录/);
  assert.match(dialog.body, /&lt;pool&gt;/);
  assert.match(dialog.body, /旧套餐\/周用量及会话绑定将失效/);
  await dialog.buttons[1].action();
  assert.equal(calls[2].url, '/api/accounts/pool%2Fsafe');
  assert.equal(calls[2].body.clearProxy, true);
  e.node('#th-proxy-direct').checked = false;
  await dialog.buttons[1].action();
  assert.equal(calls[3].body.clearProxy, false);
  assert.equal(calls[3].body.proxyUrl, 'socks5h://user:mock-only@127.0.0.1:1080');
  assert.ok(calls.every((c) => c.method === 'PUT'));
  const app = fs.readFileSync(path.join(__dirname, 'web', 'app.js'), 'utf8');
  assert.match(app, /\['th-proxy', '修改代理'\]/);
  assert.match(app, /case 'th-proxy': await thPoolProxy\(a\)/);
}

async function testRegistrationPacingControls() {
  const writes = [];
  const e = environment(async (method, url, body) => {
    if (method === 'PUT') writes.push(body);
    return { tokenHarbor: { registerConcurrency: 4 }, revision: 1 };
  });
  await e.context.loadTHSettings();
  assert.equal(e.node('#th-set-conc').value, '4');
  assert.equal(e.node('#th-register-conc').value, '4', 'saved default must actually reach the registration form');
  e.node('#th-set-conc').value = '5';
  await e.context.thSaveSettings({ preventDefault() {} });
  assert.equal(writes[0].tokenHarbor.registerConcurrency, 5);
  for (const value of ['0', '6', '2.5', '', '-1']) {
    e.node('#th-register-conc').value = value;
    assert.throws(() => e.context.thConcurrency('#th-register-conc'), /1–5/);
  }
  e.node('#th-register-conc').value = '0';
  const result = await e.context.thBatchRun('subscription', [{ id: 'session', status: 'registered', hasSession: true }]);
  assert.equal(result.success, 1, 'registration concurrency validation must not block read-only plan checks');
  e.node('#th-register-conc').value = '5';
  assert.equal(e.context.thConcurrency('#th-register-conc'), 5);
  const html = fs.readFileSync(path.join(__dirname, 'web', 'index.html'), 'utf8');
  for (const id of ['th-register-conc', 'th-set-conc']) {
    const tag = html.match(new RegExp('<input id="' + id + '"[^>]*>'))[0];
    assert.match(tag, /min="1" max="5" step="1"/);
    assert.doesNotMatch(tag, /readonly|disabled/);
  }
  assert.match(html, /所有任务合计最多5个/);
  assert.doesNotMatch(html, /固定单并发/);
  assert.doesNotMatch(html, /(?:注册提交|提交)至少间隔60秒/);
  assert.match(html, /不会自动重试/);
}

async function testJobOrderUsesOnlyCreationTime() {
  const jobs = [
    { id: 'zzz-old', createdAt: '2026-11-05T22:00:00Z', updatedAt: '2027-01-01T00:00:00Z', running: true },
    { id: 'mmm-middle', createdAt: '2026-11-06T22:00:00Z' },
    { id: 'bbb-new', createdAt: '2026-11-07T22:00:00Z' },
    { id: 'aaa-new', createdAt: '2026-11-07T22:00:00Z', note: 'edited' },
  ];
  const e = environment(async (method, url) => url.endsWith('/settings') ? { tokenHarbor: { registerConcurrency: 3 } } : { jobs, seq: 0, entries: [] });
  await e.context.loadTHRegistrations();
  assert.equal(e.node('#th-register-conc').value, '3');
  assert.deepEqual(Array.from(e.context.state.jobs, (j) => j.id), ['aaa-new', 'bbb-new', 'mmm-middle', 'zzz-old']);
  e.context.state.job = 'zzz-old';
  e.context.state.jobs = jobs;
  e.context.renderTHJobs();
  const html = e.node('#th-register-jobs').innerHTML;
  assert.ok(html.indexOf('data-job="aaa-new"') < html.indexOf('data-job="mmm-middle"'));
  assert.ok(html.indexOf('data-job="mmm-middle"') < html.indexOf('data-job="zzz-old"'));
  assert.equal(e.context.state.job, 'zzz-old', 'sorting cannot switch the selected task');
  assert.equal(jobs[0].id, 'zzz-old', 'sorting cannot mutate the response array');
}

async function testPendingCheckoutCanBePreviewedWithoutApprovalOrReplay() {
  const calls = [];
  const e = environment(async (method, url, body) => {
    calls.push({ method, url, body });
    return url.endsWith('/preview') ? { quote: { payNowUsd: 0.99 }, createsOrder: false, provesNoPendingOrder: false } : { url: 'https://checkout.stripe.com/c/pay/cs_readonly_mock' };
  });
  const row = { id: 'pending/one', email: 'pending@mock.test', status: 'registered', hasSession: true, hasCheckout: false, checkoutPending: true, checkoutFailure: { stage: 'create', httpStatus: 502, code: 'stripe_session_failed', message: '<unsafe>provider failure' } };
  e.context.state.rows = [row];
  assert.equal(e.context.thBatchEligible('checkout', row), false, 'batch cannot attempt recovery of uncertain orders');
  assert.match(e.context.thRowActions(row), /检查支付 \/ 恢复链接/);
  await assert.rejects(e.context.thBatchCall('checkout', row), /人工核对/);
  let options;
  e.context.modal = async (input) => { options = input; };
  e.context.thCheckoutLink = () => {};
  await e.context.thRowCheckout(row.id);
  assert.equal(calls.length, 0);
  assert.match(options.body, /th-checkout-form/);
  assert.match(options.body, /&lt;unsafe&gt;/);
  assert.doesNotMatch(options.body, /<unsafe>/);
  const create = options.buttons.find((b) => b.value === 'create').action;
  const preview = options.buttons.find((b) => b.value === 'preview').action;
  const confirmation = e.node('#th-checkout-confirm-no-order');
  await assert.rejects(create(), /先核对官网账单/);
  assert.equal(calls.length, 0, 'known pending marker is explained before another creation request');
  confirmation.checked = true;
  await preview();
  assert.equal(confirmation.checked, true, 'read-only preview must not consume or supply recovery approval');
  assert.equal(calls[0].url, '/api/tokenharbor/registrations/pending%2Fone/checkout/preview');
  assert.equal(calls[0].body.confirmNoPendingOrder, undefined);
  assert.match(e.node('#th-checkout-diagnostic').textContent, /\$0\.99/);
  assert.match(e.node('#th-checkout-diagnostic').textContent, /不代表没有旧待处理订单/);
  confirmation.checked = false;
  await assert.rejects(create(), /先核对官网账单/);
  assert.equal(calls.length, 1, 'successful preview cannot unlock creation');
  confirmation.checked = true;
  await create();
  await create();
  assert.equal(calls.length, 2);
  assert.equal(calls[1].body.confirmNoPendingOrder, true);
  assert.equal(confirmation.checked, false);
  const css = fs.readFileSync(path.join(__dirname, 'web', 'app.css'), 'utf8');
  assert.match(css, /#modal-body\s+\.form\.th-checkout-form\s*\{[^}]*grid-template-columns:\s*minmax\(0, 1fr\)/, 'override must outrank the generic modal two-column form');
}

async function testLiveLogRetentionDropsCachedExpiredLines() {
  let reply = { seq: 2, retentionDays: 7, entries: [{ seq: 1, ts: new Date(Date.now() - 8 * 86400000).toISOString(), detail: 'expired' }, { seq: 2, ts: new Date().toISOString(), detail: 'fresh' }] };
  const e = environment(async () => reply);
  await e.context.thLogPoll();
  assert.equal(e.context.log.lines.length, 1);
  assert.equal(e.context.log.lines[0].detail, 'fresh');
  assert.equal(e.context.log.seq, 2, 'cleanup cannot reset the incremental sequence');
  e.context.log.lines[0].ts = new Date(Date.now() - 2 * 86400000).toISOString();
  reply = { seq: 2, retentionDays: 1, entries: [] };
  await e.context.thLogPoll();
  assert.equal(e.context.log.lines.length, 0, 'config shrink clears cached lines even without new log entries');
  assert.match(e.node('#th-log-body').innerHTML, /暂无日志/);
}

async function testTaskNotesAreEscapedScopedAndExplicitlySaved() {
  const calls = [];
  let fail = true;
  const e = environment(async (method, url, body) => {
    calls.push({ method, url, body });
    if (fail) throw new Error('mock save failed');
    return { id: 'job/one', note: body.note };
  });
  const malicious = '</textarea><img src=x onerror=alert(1)>';
  const job = { id: 'job/one', note: malicious, rows: [{ id: 'row-one', email: 'one@mock.test', status: 'saved' }] };
  const other = { id: 'job-two', note: '另一批次', rows: [] };
  e.context.state.jobs = [job, other];
  e.context.state.job = job.id;
  e.context.state.selected.add('row-one');
  e.context.renderTHJobs();
  assert.match(e.node('#th-register-jobs').innerHTML, /data-job-note="job\/one"/);
  assert.match(e.node('#th-register-jobs').innerHTML, /&lt;\/textarea&gt;&lt;img/);
  assert.doesNotMatch(e.node('#th-register-jobs').innerHTML, /<img/);
  e.context.renderTHJob(job);
  assert.ok(e.node('#th-register-job-title').textContent.includes(malicious), 'detail title is assigned as text, not HTML');

  let dialog, close;
  e.context.modal = (options) => { dialog = options; return new Promise((resolve) => { close = resolve; }); };
  const edit = e.context.thEditJobNote(job.id);
  assert.equal(e.context.state.noteEditing, true);
  assert.match(dialog.body, /&lt;\/textarea&gt;&lt;img/);
  assert.doesNotMatch(dialog.body, /<img/);
  assert.equal(calls.length, 0, 'opening a note must not submit a write');
  const firstDialog = dialog;
  await e.context.thEditJobNote(other.id);
  assert.equal(dialog, firstDialog, 'double clicks cannot replace an active note modal');
  const save = dialog.buttons.find((b) => b.value === 'save').action;
  e.node('#th-job-note').value = ' new note ';
  await assert.rejects(save(), /mock save failed/);
  assert.equal(job.note, malicious, 'failed save cannot update the visible note');
  e.node('#th-job-note').value = '测'.repeat(201);
  await assert.rejects(save(), /最多200/);
  assert.equal(calls.length, 1, 'oversized note is rejected before a request');

  // Polling may replace task objects while the modal is open. Save must target
  // its captured task ID, not the current selection or an obsolete object.
  const polled = { ...job, note: malicious };
  e.context.state.jobs = [polled, other];
  e.context.state.job = other.id;
  fail = false;
  e.node('#th-job-note').value = '  美国代理测试 😀  ';
  await save();
  await save();
  assert.equal(calls.length, 2, 'successful modal saves are not repeated');
  assert.equal(calls[1].method, 'PUT');
  assert.equal(calls[1].url, '/api/tokenharbor/registrations/job%2Fone/note');
  assert.equal(calls[1].body.note, '美国代理测试 😀');
  assert.equal(polled.note, '美国代理测试 😀');
  assert.equal(other.note, '另一批次');
  assert.equal(e.context.state.job, other.id, 'saving must not switch the selected task');
  assert.equal(e.context.state.selected.has('row-one'), true);
  close('save');
  await edit;
  assert.equal(e.context.state.noteEditing, false);

  e.context.modal = async (options) => { dialog = options; return 'cancel'; };
  await e.context.thEditJobNote(job.id);
  assert.equal(calls.length, 2, 'cancel cannot save a note');
  assert.match(dialog.body, /美国代理测试 😀/);
  e.node('#th-job-note').value = '';
  await dialog.buttons.find((b) => b.value === 'save').action();
  assert.equal(calls[2].body.note, '');
  assert.equal(polled.note, '', 'empty explicit save clears the note');
  assert.ok(calls.every((c) => c.method === 'PUT' && c.url.endsWith('/note')), 'notes never start registration or other account work');
}

async function testEmailMailboxFormats() {
  const e = environment(async () => ({}));
  const mailbox = 'https://icloud.mida.vip/messages/alias%40icloud.com?token=mock-token';
  const accounts = e.context.thParseAccounts(`  alias@icloud.com ---- ${mailbox}  \r\n\nother@icloud.com----mock-password----${mailbox}`);
  assert.equal(accounts.length, 2);
  assert.equal(accounts[0].email, 'alias@icloud.com');
  assert.equal(accounts[0].mailboxUrl, mailbox);
  assert.match(accounts[0].password, /^Tq7-[A-Za-z0-9_-]{16}$/);
  assert.notEqual(accounts[0].password, mailbox, 'receive URL must never become the password');
  assert.equal(accounts[1].email, 'other@icloud.com');
  assert.equal(accounts[1].password, 'mock-password');
  assert.equal(accounts[1].mailboxUrl, mailbox);
  const html = fs.readFileSync(path.join(__dirname, 'web', 'index.html'), 'utf8');
  const tag = html.match(/<textarea id="th-register-accounts"[^>]*>/)?.[0];
  assert.ok(tag);
  assert.match(tag, /邮箱----接码页面（自动生成密码）/);
  assert.match(tag, /邮箱----密码----接码页面/);
}

async function testGitHubFormatAndSessionProjection() {
  const e = environment(async () => ({}));
  const accounts = e.context.thParseAccounts('mock-user----mock-password----GEZDGNBVGY3TQOJQ----https://191006.xyz/mailbox/mock-token');
  assert.equal(accounts.length, 1);
  assert.equal(accounts[0].provider, 'github');
  assert.equal(accounts[0].login, 'mock-user');
  assert.equal(accounts[0].totpSecret, 'GEZDGNBVGY3TQOJQ');
  const five = e.context.thParseAccounts('mock-user----mock-password----GEZDGNBVGY3TQOJQ----bound@icloud.com----https://mail.test/api/v1/mailboxes/bound@icloud.com/code?key=private-key');
  assert.equal(five[0].githubEmail, 'bound@icloud.com');
  assert.equal(five[0].mailboxUrl, 'https://mail.test/api/v1/mailboxes/bound@icloud.com/code?key=private-key');
  assert.throws(() => e.context.thParseAccounts('mock-user----pw----GEZDGNBVGY3TQOJQ--------https://mail.test/api/v1/mailboxes/test@icloud.com/code?key=private-key'), /绑定邮箱/);
  e.context.renderTHJob({ id: 'github-job', rows: [{ id: 'gh-row', email: 'github:mock-user', status: 'saved', authProvider: 'github', githubLogin: 'mock-user', githubProxy: 'socks5://ipv4.local:5000', proxy: 'http://ipv6.local:6000', hasGithubSession: true, hasSession: false }] });
  const html = e.node('#th-register-rows').innerHTML;
  assert.match(html, /GitHub 会话 已保存/);
  assert.match(html, /TH 会话 未取得/);
  assert.match(html, /ipv4.local/);
  assert.match(html, /ipv6.local/);
  assert.doesNotMatch(html, /mock-password|GEZDGNBV|private-key/);
}

async function testSavedKeySelectionAndExplicitRead() {
  const e = environment(async () => { throw new Error('key selection must not call an upstream mutation'); });
  const shown = [];
  e.context.showStoredAPIKeys = async (entries) => shown.push(entries);
  const selected = { id: 'row/one', email: '<mock>@test.local', status: 'failed', keyMasked: 'thk_…mask' };
  const missing = { id: 'missing', email: 'missing@test.local', status: 'saved' };
  const hidden = { id: 'hidden', email: 'hidden@test.local', status: 'imported', keyMasked: 'thk_…other' };
  e.context.state.job = 'job';
  e.context.state.jobs = [{id:'job', rows:[selected, missing, hidden]}];
  e.context.state.selected.add(selected.id);
  e.context.state.selected.add(missing.id);
  e.context.state.selected.add(hidden.id);
  e.context.renderTHJob(e.context.state.jobs[0]);
  assert.equal(e.context.state.selected.has(hidden.id), false, 'other tab selection must not leak');
  assert.match(e.node('#th-register-rows').innerHTML, /th-row-keys/);
  assert.equal(e.node('#th-batch-keys').disabled, false);
  await e.context.thBatchAction('keys');
  assert.equal(shown.length, 1);
  assert.equal(shown[0].length, 2);
  assert.equal(shown[0][0].path, '/api/tokenharbor/registrations/row%2Fone/keys');
  assert.equal(shown[0][1].path, null, 'no-key row is skipped, not resumed');
  assert.equal(e.context.state.batchPrompt, false);
  assert.doesNotMatch(JSON.stringify(e.context.state), /runtimeKey|thk_live_/);
  assert.doesNotMatch(e.context.thRowActions(missing), /th-row-keys/);
}

(async () => {
  await testSavedKeySelectionAndExplicitRead();
  await testResendVerificationAndVisibleInputs();
  await testTaskNotesAreEscapedScopedAndExplicitlySaved();
  await testEmailMailboxFormats();
  await testGitHubFormatAndSessionProjection();
  await testRegistrationPacingControls();
  await testJobOrderUsesOnlyCreationTime();
  await testIndependentProxyEditing();
  await testLogSingleFlightAndSequenceDedupe();
  await testBatchBoundedPartialAndNoRetry();
  await testCheckoutReuseAndUncertainStops();
  await testCheckoutRecoveryRequiresExplicitOneShotConfirmation();
  await testPendingCheckoutCanBePreviewedWithoutApprovalOrReplay();
  await testLiveLogRetentionDropsCachedExpiredLines();
  await testRowGuardAndSelectionRetention();
  await testOverviewEquivalentAmountsAndUnknown();
  await testImportedFilterAndReturn();
  await testRegistrationDefaultTaskAndRetention();
  await testBatchImportPartialAndScoped();
  await testCancelledBatchDoesNotMutate();
  console.log('Token Harbor web behavior tests passed');
})().catch((e) => { console.error(e); process.exitCode = 1; });
