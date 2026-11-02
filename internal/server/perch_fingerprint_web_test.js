const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');

const source = fs.readFileSync(require('node:path').join(__dirname, 'web', 'perch.js'), 'utf8');
const context = {
  document: { addEventListener() {} },
  esc: (value) => String(value).replace(/[&<>"']/g, (ch) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[ch]),
};
vm.createContext(context);
vm.runInContext(source, context);
const render = context.pcFingerprintProxyReport;
assert.match(render(null), /诊断暂不可用/);
const html = render({ accounts: [
  { name: '<account>', proxySource: 'account_proxy' },
  { name: 'two', proxySource: 'unconfigured_unknown' },
  { name: 'three', proxySource: 'unexpected' },
] });
assert.match(html, /&lt;account&gt;/);
assert.match(html, /显式账号代理/);
assert.match(html, /未配置账号代理 \/ 实际路径未知/);
assert.match(html, /NO_PROXY/);
assert.match(html, /注册和激活使用独立网络配置/);
assert.match(html, /<td>three<\/td><td>未知<\/td>/);
assert.doesNotMatch(html, /直连|出口 IP/);
const detail = context.pcEgressDetail;
assert.match(detail({event: 'egress', kind: 'model', proxySource: 'account_proxy', target: 'perch-app', status: 429, durationMs: 23, errorClass: 'rate_limited'}), /模型调用 · 账号代理 · Perch App · HTTP 429 · 23ms · 限流/);
assert.doesNotMatch(detail({event: 'egress', kind: '<secret>', proxySource: '<secret>', target: '<secret>', errorClass: '<secret>'}), /<secret>/);
assert.equal(detail({event: 'check', detail: 'existing detail'}), 'existing detail');
