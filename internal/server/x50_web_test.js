// 50X AI 号池前端守卫：语法、页面接线、chips 渲染、API 路径与凭据边界。
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const appSource = fs.readFileSync(path.join(__dirname, 'web/app.js'), 'utf8');
const html = fs.readFileSync(path.join(__dirname, 'web/index.html'), 'utf8');
const x50Source = fs.readFileSync(path.join(__dirname, 'web/x50.js'), 'utf8');

// 1) 经典脚本共享全局词法作用域：单文件语法检查会漏掉重复声明冲突。
new vm.Script(appSource + '\n' + x50Source);

// 2) 顶部标签唯一，重启旧页面不能出现两个 50X 入口。
assert.equal([...html.matchAll(/data-pool="x50" role="tab"/g)].length, 1, '重复的 50X 号池标签');
assert.match(html, /data-pool="x50" role="tab">50X AI/);

// 3) 侧栏页面与静态资源都要在。
for (const part of ['data-page="x50reg"', 'data-page="x50settings"', 'id="page-x50reg"',
  'id="page-x50settings"', 'id="x50-accounts"', 'id="x50-proxies"', 'id="x50-rows"',
  'id="x50-status"', 'id="x50-log-table"', '/assets/x50.js']) {
  assert.ok(html.includes(part), `缺少 ${part}`);
}
assert.ok(html.includes('id="x50s-concurrency"'), '缺少并发设置');
assert.ok(html.includes('id="x50s-auto-bind"'), '缺少自动绑定代理开关');

// 4) app.js 必须把新池接进分页、侧栏与标签路由。
assert.match(appSource, /const POOLS = \[[^\]]*'x50'/);
assert.match(appSource, /x50: '50X AI'/);
assert.match(appSource, /const X50_PAGES = \['x50reg', 'x50settings'\]/);
assert.match(appSource, /const isX50 = \(\) => pool === 'x50'/);
assert.match(appSource, /x50reg: \(\) => loadX50Registrations\(\), x50settings: \(\) => loadX50Settings\(\)/);
assert.match(appSource, /if \(page === 'x50reg'\) startX50Polling\(\)/);
assert.match(appSource, /if \(currentPage === 'x50reg' && page !== currentPage && typeof stopX50Polling === 'function'\) stopX50Polling\(\)/);
// 50X 没有推理号池：切过去必须直接落在注册机，且停留在概览时被纠正。
assert.match(appSource, /pool === 'x50' \? X50_PAGES : PROXY_PAGES/);
assert.match(appSource, /if \(isX50\(\) && !X50_PAGES\.includes\(page\)\) page = 'x50reg'/);
assert.match(appSource, /data-pool-nav="x50"/);

// 5) 池切换通过 vm 跑一遍真实函数。
const context = { console, Date, window: {}, localStorage: { getItem() { return 'x50'; } } };
vm.createContext(context);
vm.runInContext(appSource.slice(appSource.indexOf('const POOLS ='), appSource.indexOf('const esc =')), context);

// 6) renderX50Rows 必须转义上游内容，并把「已完成」与「有 Key」区分开。
// renderX50Rows 直接写 DOM，所以用存根元素收它的 innerHTML。
const dom = { rows: { innerHTML: '' }, count: { textContent: '' }, status: { innerHTML: '' } };
context.document = {
  getElementById: (id) => ({ 'x50-rows': dom.rows, 'x50-count': dom.count, 'x50-status': dom.status }[id] || null),
  querySelector: () => null,
};
// x50.js 依赖 app.js 里的 POOLS/X50_PAGES/isX50 等常量；这里补上它们再整体求值。
context.__decls = `var X50_PAGES = ['x50reg', 'x50settings'];`;
vm.runInContext(context.__decls, context);
vm.runInContext(x50Source, context);
context.renderX50Rows([
  {
    id: 'r1', email: '<img src=x onerror=alert(1)>@icloud.com', status: 'keyed', statusText: '已完成',
    hasKey: true, keyPrefix: 'sk-50x-abcd', balanceUsd: '100.000000000',
    proxyBound: true, proxyLabel: 'socks5://gw:5000', hasMailbox: true, attempts: 1,
  },
  {
    id: 'r2', email: 'b@icloud.com', status: 'running', statusText: '注册中', stage: '等待确认邮件',
    proxyBound: false, hasMailbox: true, attempts: 1,
  },
  {
    id: 'r3', email: 'c@icloud.com', status: 'failed', statusText: '失败',
    error: '<script>bad</script>', proxyBound: false, hasMailbox: false, attempts: 2,
  },
]);
const rowsHtml = dom.rows.innerHTML;
assert.ok(rowsHtml.length > 0, 'renderX50Rows 没有产出');
assert.match(rowsHtml, /&lt;img src=x onerror=alert\(1\)&gt;/, '邮箱未转义');
assert.doesNotMatch(rowsHtml, /<img /, '注入的标签逃逸了');
assert.match(rowsHtml, /sk-50x-abcd/, '缺少 Key 前缀');
assert.match(rowsHtml, /100\.000000000 USD/, '缺少 welcome 额度');
assert.match(rowsHtml, /等待确认邮件/, '缺少阶段进度');
assert.match(rowsHtml, /第 2 次尝试/, '缺少尝试次数');
assert.match(rowsHtml, /data-x50-key="r1"/, '已完成账号缺少复制 Key 按钮');
assert.match(rowsHtml, /&lt;script&gt;bad&lt;\/script&gt;/, '错误信息未转义');
assert.doesNotMatch(rowsHtml, /<script>bad/, '错误信息逃逸');
// 已完成账号不得出现「注册/重试」按钮，避免生成第二个 Key
assert.doesNotMatch(rowsHtml.split('<tr>')[1], /data-x50-run="r1"/, '已完成账号仍可重跑');

// 7) 状态 chips 不显示凭据。
dom.status.innerHTML = '';
context.renderX50Status({ total: 3, queued: 0, running: 1, waiting: 1, keyed: 1, failed: 0, withProxy: 1, busy: 1 });
assert.match(dom.status.innerHTML, /已完成 1/);
assert.match(dom.status.innerHTML, /worker 运行中 1/);
assert.match(dom.count.textContent, /3/, '账号计数未更新');

// 8) API 路径与只发送用户真实输入的行为。
(async () => {
  const calls = [];
  context.api = async (...args) => { calls.push(args); return { rows: [], logs: [], settings: { proxyCount: 0 } }; };
  context.isX50 = () => true;
  context.window = {};
  context.document.getElementById = (id) => {
    if (id === 'x50-poll-error') return { textContent: '' };
    if (id === 'x50-proxies') return { value: '', dataset: {}, activeElement: null };
    return null;
  };
  context.document.querySelector = () => null;
  context.document.activeElement = null;
  await context.x50Api('/candidates');
  assert.equal(calls[0][0], 'GET');
  assert.equal(calls[0][1], '/api/x50/candidates');
  assert.equal(calls[0][2], undefined);
  await context.x50Api('/settings', { method: 'PUT', body: JSON.stringify({ concurrency: 3 }) });
  assert.equal(calls[1][0], 'PUT');
  assert.equal(calls[1][1], '/api/x50/settings');
  assert.equal(calls[1][2].concurrency, 3);
  await context.x50Api('/candidates/abc/key');
  assert.equal(calls[2][1], '/api/x50/candidates/abc/key');

  // collectSettings 必须保留已保存代理：空 textarea 不发送 proxies 字段
  const fields = {
    'x50s-concurrency': { value: '4' },
    'x50s-mail-timeout': { value: '180' },
    'x50s-mail-poll': { value: '2' },
    'x50s-request-timeout': { value: '25' },
    'x50s-key-name': { value: 'k1' },
    'x50s-api-base': { value: 'http://127.0.0.1:18801' },
    'x50s-mail-base': { value: ' http://127.0.0.1:18802 ' },
    'x50s-password-mode': { value: 'random' },
    'x50s-proxies': { value: '' },
    'x50s-auto-bind': { checked: true },
  };
  context.document.getElementById = (id) => fields[id] || null;
  const empty = context.x50CollectSettings(true);
  assert.equal(empty.proxies, undefined, '空代理框不应清空已保存代理');
  assert.equal(empty.concurrency, 4);
  assert.equal(empty.apiBase, 'http://127.0.0.1:18801', 'apiBase 未发送');
  assert.equal(empty.mailBase, 'http://127.0.0.1:18802', 'mailBase 未 trim');
  assert.equal(empty.autoBindProxy, true);
  fields['x50s-proxies'].value = 'socks5://a:1\n\nsocks5://b:2';
  const withProxies = context.x50CollectSettings(true);
  // vm 里产出的数组属于另一个 realm，用逐项比较而不是 deepEqual。
  assert.equal(Array.from(withProxies.proxies).join('|'), 'socks5://a:1|socks5://b:2', '代理行解析错误');

  // 边界：关闭自动绑定必须落到请求体
  fields['x50s-auto-bind'].checked = false;
  assert.equal(context.x50CollectSettings(false).autoBindProxy, false);

  console.log('50X web tests passed');
})().catch((error) => { console.error(error); process.exitCode = 1; });
