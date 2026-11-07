const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'web/app.js'), 'utf8');
const context = { console, Date, localStorage: { getItem() { return 'opendesign'; } } };
vm.createContext(context);
vm.runInContext(source.slice(source.indexOf('const POOLS ='), source.indexOf('// navigator.clipboard')), context);
vm.runInContext(source.slice(source.indexOf('const STATUS ='), source.indexOf('// ollamaQuotaHTML')), context);
vm.runInContext(source.slice(source.indexOf('function openDesignStatusHTML('), source.indexOf("$('#acc-table').addEventListener(", source.indexOf('function openDesignStatusHTML('))), context);
vm.runInContext(source.slice(source.indexOf('async function accountAction('), source.indexOf('function accountForm(')), context);
const account = {
  id: 'od', name: '<img onerror=bad>', provider: 'opendesign', hasControlKey: true,
  runtime: { status: 'ok', state: {}, active: 0, rpm: 0, selections: 0, bindings: 0 },
  diagnosis: { status: 'upstream-unavailable', label: '上游故障', detail: '<script>secret</script>', planFresh: false, walletFresh: false, billingFresh: false, planStatus: 'unknown', walletStatus: 'unknown', autoRefresh: true },
  quota: {
    planAt: '2026-01-01T01:00:00Z', nextAt: '2099-01-01T01:00:00Z',
    windows: [{ durationSeconds: 604800, limit: 100, remaining: 0, resetsAt: '2026-01-02T01:00:00Z' }],
    walletUsd: '3.50', walletAt: '2026-01-01T01:00:00Z',
    summary: { subscriptionCredits: '42', rechargeCredits: '0' }, summaryAt: '2026-01-01T01:00:00Z',
    catalog: { 'mock-model': true },
  },
};
let html = context.windowsHTML(account);
assert.match(html, /套餐额度当前未知/);
assert.match(html, /上次7 天/);
assert.match(html, /剩余 <b>0<\/b> \/ 100/);
assert.doesNotMatch(html, /剩余 <b>100<\/b>/);
assert.match(html, /窗口已到期，等待新数据；不推算满额/);
assert.match(html, /套餐上次成功/);
assert.match(html, /下次自动尝试/);
html = context.openDesignFundingHTML(account);
assert.match(html, /钱包当前未知（上次 \$3.50）/);
assert.match(html, /上次套餐模型余额 42/);
assert.match(html, /账单上次成功/);
html = context.accountRow(account);
assert.match(html, /调度：可用/);
assert.match(html, /上次模型目录（当前未知）/);
assert.match(html, /&lt;script&gt;secret&lt;\/script&gt;/);
assert.doesNotMatch(html, /<script>|<img onerror/);
assert.match(html, /不等于确认安全/);
const empty = { ...account, quota: null, hasControlKey: false, diagnosis: { planStatus: 'unknown', autoRefresh: false } };
assert.match(context.windowsHTML(empty), /无控制密钥/);
assert.match(context.windowsHTML(empty), /后台额度轮询已关闭/);
assert.match(context.openDesignFundingHTML(empty), /钱包当前未知/);
assert.doesNotMatch(context.openDesignFundingHTML(empty), /\$0.00/);
const measured = { ...account, diagnosis: { planStatus: 'exhausted', planFresh: true, walletFresh: true, walletStatus: 'empty', billingFresh: true }, quota: { ...account.quota, walletUsd: '0', windows: [{ durationSeconds: 18000, remaining: 0, limit: 100, resetsAt: '2099-01-01T01:00:00Z' }] } };
assert.match(context.windowsHTML(measured), /套餐窗口额度耗尽/);
assert.match(context.openDesignFundingHTML(measured), /钱包 \$0.00/);
assert.match(context.openDesignFundingHTML(measured), /钱包为零不等于套餐额度耗尽/);
assert.match(context.openDesignStatusHTML({ diagnosis: { status: 'banned', label: '上游明确账号已封禁' } }), /badge bad/);
assert.match(source, /检测状态并刷新额度（只读）/);
(async () => {
  const calls = [], toasts = [];
  let reloads = 0;
  context.api = async (...args) => { calls.push(args); return { ok: false, quotaRefreshed: false, message: '账单服务不可用，额度未知' }; };
  context.toast = (...args) => toasts.push(args);
  context.loadAccounts = async () => { reloads++; };
  await context.accountAction(account, 'refresh');
  assert.deepEqual(calls, [['POST', '/api/accounts/od/refresh-quota']]);
  assert.deepEqual(toasts, [['账单服务不可用，额度未知', 'error']]);
  assert.equal(reloads, 1);
  assert.ok(!toasts.some(([message]) => message === '额度已刷新'));
  context.api = async () => ({ ok: true, quotaRefreshed: true, message: '套餐、钱包与账单已刷新；目录检测不证明推理授权' });
  await context.accountAction(account, 'refresh');
  assert.equal(toasts.at(-1)[1], undefined);
  context.api = async () => { throw new Error('账号来源已变化，请重新检测'); };
  await context.accountAction(account, 'refresh');
  assert.equal(toasts.at(-1)[1], 'error');
  console.log('OpenDesign diagnostic web tests passed');
})().catch(error => { console.error(error); process.exitCode = 1; });
