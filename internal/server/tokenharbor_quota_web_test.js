const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'web', 'app.js'), 'utf8');
const begin = source.indexOf('function tokenHarborQuotaHTML(a) {');
const end = source.indexOf('function windowsHTML(a) {', begin);
assert.ok(begin >= 0 && end > begin);
assert.match(source, /tokenharbor: '周限 \/ 月限余额'/);
const context = {
  esc: (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;' })[c]),
  num: (n) => String(n), fmtTime: () => 'reset-time',
};
vm.createContext(context);
vm.runInContext(source.slice(begin, end), context);
const account = (usage = {}, fresh = true) => ({ quotaFresh:fresh, quota:{tokenHarbor:{plan:{status:'active',tier:'plus',name:'Agent Pass'},checkedAt:new Date().toISOString(),usage:{usedPercent:29,weeklyLimitUsd:2.5,monthlyLimitUsd:10,activated:true,resetsAt:new Date(Date.now()+3600000).toISOString(),...usage}}} });
let html = context.tokenHarborQuotaHTML(account());
assert.match(html, /周限余额：约 \$3\.55 \/ \$5\.00（剩余 71%）/);
assert.match(html, /月限余额：未知（官网未提供月剩余） · 月等效总额度 \$20\.00/);
assert.match(html, /非官网原始美元额度/);
assert.doesNotMatch(html, /月限余额：[约\s]*\$/);
assert.doesNotMatch(html, /\$2\.50|\$10\.00/, 'Agent uses the requested $5/$20 display basis without altering upstream measurements');
html = context.tokenHarborQuotaHTML(account({weeklyLimitUsd:undefined,monthlyLimitUsd:undefined}));
assert.match(html, /周限余额：约 \$3\.55 \/ \$5\.00/);
const other = account();
other.quota.tokenHarbor.plan = {status:'active',tier:'pro',name:'Office Pass'};
html = context.tokenHarborQuotaHTML(other);
assert.match(html, /周限余额：约 \$1\.78 \/ \$2\.50/);
assert.doesNotMatch(html, /等效基准/);
other.quota.tokenHarbor.usage.weeklyLimitUsd = undefined;
other.quota.tokenHarbor.usage.monthlyLimitUsd = undefined;
html = context.tokenHarborQuotaHTML(other);
assert.match(html, /剩余 71%（金额未知）/);
assert.doesNotMatch(html, /\$/);
const unknownPlan = account();
unknownPlan.quota.tokenHarbor.plan = {name:'Agent Pass'};
assert.doesNotMatch(context.tokenHarborQuotaHTML(unknownPlan), /等效基准/, 'name alone is not Agent plan proof');
for (const a of [account({},false),account({resetsAt:new Date(Date.now()-1000).toISOString()}),account({error:'读取失败'})]) {
  html = context.tokenHarborQuotaHTML(a);
  assert.match(html, /周限余额：未知/);
  assert.doesNotMatch(html, /周限余额：约 \$/);
}
html = context.tokenHarborQuotaHTML(account({usedPercent:100}));
assert.match(html, /约 \$0\.00/);
html = context.tokenHarborQuotaHTML(account({usedPercent:null}));
assert.match(html, /周限余额：未知/);
html = context.tokenHarborQuotaHTML({quotaFresh:true});
assert.match(html, /周限余额：未知/);
assert.match(html, /月限余额：未知/);
assert.doesNotMatch(html, /已检测/);
const unsafe = account({error:'<img src=x onerror=alert(1)>'});
unsafe.quota.tokenHarbor.plan.name = '<script>plan</script>';
html = context.tokenHarborQuotaHTML(unsafe);
assert.doesNotMatch(html, /<img|<script>/);
assert.match(html, /&lt;img/);
console.log('Token Harbor weekly/monthly allowance display tests passed');
