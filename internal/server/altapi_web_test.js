// ALTapi 号池前端守卫：语法、页面接线、标签唯一、API 路径与凭据边界。
//
// 重点守三件事：
//   1. 标签/侧栏/页面三处接线必须同时存在，缺一处就会出现「有标签没页面」。
//   2. 余额 null 必须渲染成「未知」，不能渲染成 $0.00——altapi 的余额只在
//      /api/* 控制面可读，没有 session 时确实未知，写 0 会被误读成空账号。
//   3. API Key 只能由「复制 Key」按钮显式读取，列表里只显示前 8 位。
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const appSource = fs.readFileSync(path.join(__dirname, 'web/app.js'), 'utf8');
const html = fs.readFileSync(path.join(__dirname, 'web/index.html'), 'utf8');
const altSource = fs.readFileSync(path.join(__dirname, 'web/altapi.js'), 'utf8');

// 1) 经典脚本共享全局词法作用域：单文件语法检查会漏掉重复声明冲突。
new vm.Script(appSource + '\n' + altSource);

// 2) 顶部标签唯一。
assert.equal([...html.matchAll(/data-pool="altapi" role="tab"/g)].length, 1, '重复的 ALTapi 号池标签');
assert.match(html, /data-pool="altapi" role="tab">ALTapi/);

// 3) 侧栏三页 + 页面区块 + 静态资源都要在。
for (const part of [
  'data-page="altoverview"', 'data-page="altreg"', 'data-page="altsettings"',
  'id="page-altoverview"', 'id="page-altreg"', 'id="page-altsettings"',
  'id="alt-overview-cards"', 'id="alt-overview-rows"',
  'id="alt-accounts"', 'id="alt-proxies"', 'id="alt-rows"', 'id="alt-status-chips"',
  'id="alt-log-dock"', 'id="alt-log-toggle"', 'id="alt-log-badge"',
  'id="alt-log-table"', 'id="alt-log-close"', 'id="alt-proxy-list"', 'id="alt-proxy-result"',
  '/assets/altapi.js',
]) {
  assert.ok(html.includes(part), `缺少 ${part}`);
}

// 4) 设置项必须能看见、能改、能保存（全套参数，不只占位）。
for (const part of [
  'id="alts-workers"', 'id="alts-min-interval"', 'id="alts-max-retries"',
  'id="alts-max-attempts"', 'id="alts-verify-timeout"', 'id="alts-base-url"',
  'id="alts-python-bin"', 'id="alts-token-group"', 'id="alts-token-name"',
  'id="alts-aff"', 'id="alts-auto-verify"', 'id="alts-import-to-pool"',
  'id="alts-proxies"', 'id="alts-save"',
]) {
  assert.ok(html.includes(part), `缺少设置项 ${part}`);
}

// 5) app.js 必须把新池接进分页、侧栏、标签路由与轮询。
assert.match(appSource, /const POOLS = \[[^\]]*'altapi'\]/);
assert.match(appSource, /altapi: 'ALTapi'/);
assert.match(appSource, /const ALTAPI_PAGES = \['altoverview', 'altreg', 'altsettings'\]/);
assert.match(appSource, /const isALTapi = \(\) => pool === 'altapi'/);
assert.match(appSource, /altoverview: \(\) => loadALTStatus\(\), altreg: \(\) => loadALTRegistrations\(\), altsettings: \(\) => loadALTSettings\(\)/);
assert.match(appSource, /if \(page === 'altreg'\) startALTPolling\(\)/);
assert.match(appSource, /if \(currentPage === 'altreg' && page !== currentPage && typeof stopALTPolling === 'function'\) stopALTPolling\(\)/);
assert.match(appSource, /if \(isALTapi\(\) && !ALTAPI_PAGES\.includes\(page\)\) page = 'altreg'/);
assert.match(appSource, /data-pool-nav="altapi"/);

// 6) 每一个后端路由都要被前端用到，且路径拼在 /api/altapi 下。
for (const route of [
  '/status', '/candidates', '/candidates/bulk', '/candidates/bulk-register',
  '/candidates/import-all', '/quota-refresh', '/proxy-test', '/settings', '/logs',
]) {
  assert.ok(altSource.includes(`'${route}'`) || altSource.includes(`\`${route}`), `前端没用 ${route}`);
}
assert.ok(!/\/api\/altapi\/\/api/.test(altSource), '路径重复拼接 /api');
assert.match(altSource, /const body = opts\.body === undefined \? undefined : JSON\.parse\(opts\.body\);/);
assert.match(altSource, /api\(opts\.method \|\| 'GET', '\/api\/altapi' \+ path, body\)/);

// 7) 余额边界：null 必须是「未知」，不能落成 $0.00。
assert.match(altSource, /if \(v == null \|\| v === ''\) return '<span class="muted" title="没有会话凭据，读不到余额">未知<\/span>'/);
assert.ok(!/\$\{\(?r?\.?balance_usd \|\| 0\)?\.toFixed/.test(altSource), '余额不能用 || 0 兜底');

// 8) Key 只显示前缀；完整值只从行内数据读，不渲染进表格。
assert.match(altSource, /const head = String\(r\.api_key\)\.slice\(0, 8\)/);
assert.ok(!/\$\{altEsc\(r\.api_key\)\}<\/code>/.test(altSource), '完整 Key 不能直接渲染进表格');

// 9) 所有渲染进 innerHTML 的上游内容都要过 altEsc。
for (const re of [/r\.email/g, /r\.error/g, /l\.message/g, /r\.proxy_url/g]) {
  const hits = [...altSource.matchAll(re)];
  assert.ok(hits.length > 0, `没找到 ${re}`);
}
const unescaped = [...altSource.matchAll(/<td[^>]*>\$\{([^}]*)\}<\/td>/g)]
  .map((m) => m[1])
  .filter((expr) => !expr.includes('altEsc') && !expr.includes('altMoney') && !expr.includes('altKeyCell') && !expr.includes('altProxyCell')
    && !expr.includes('altBadge') && !expr.includes('actions')
    // 下面三类不来自上游文本：时间格式化结果、本地拼好的片段、布尔开关渲染的常量标记。
    && !expr.includes('toLocaleTimeString()') && !expr.includes('note') && !expr.includes('r.enabled'));
assert.deepEqual(unescaped, [], '存在未转义的单元格插值');

// 9.5) 日志必须是右下角常驻浮窗（与 Deploy 同构），不能是常驻正文面板。
assert.match(html, /<aside id="alt-log-dock" class="dr-log-dock collapsed"/);
assert.ok(!/id="page-altreg"[\s\S]{0,4000}<section class="panel mt">\s*<div class="head-row"><h2>运行日志<\/h2>/.test(html),
  '日志不应再是正文里的常驻面板');
assert.match(altSource, /altLogSeen/);
assert.match(altSource, /alt-log-badge/);
assert.match(altSource, /if \(expanded\) \{[\s\S]{0,200}altLogSeen = logs\.length/);
assert.match(altSource, /alt-log-toggle/);
assert.match(altSource, /alt-log-close/);

// 9.6) 导入时必须回退到已保存的代理池，否则用户在设置页存好代理后导入仍是直连。
assert.ok(altSource.includes('proxyPool'), '导入结果应回报本次使用的代理池大小');

// 10) 池状态 chips：字段名必须和后端 Summary 的 json tag 一致（camelCase）。
//     Summary 用的是 byStatus/withKey/balanceUnknown，写成 snake_case 会让计数永远显示 0。
assert.match(altSource, /s\.byStatus \|\| \{\}/);
assert.match(altSource, /s\.withKey \|\| 0/);
assert.match(altSource, /s\.balanceUnknown \|\| 0/);
assert.match(altSource, /s\.imported \|\| 0/);
const summaryTags = fs.readFileSync(path.join(__dirname, '../altapisignupstore/store.go'), 'utf8')
  .match(/type Summary struct \{[\s\S]*?\n\}/)[0];
for (const tag of ['byStatus', 'withKey', 'imported', 'balanceUnknown']) {
  assert.ok(summaryTags.includes(`json:"${tag}"`), `后端 Summary 缺 ${tag}`);
  assert.ok(altSource.includes(tag), `前端没读 ${tag}`);
}

console.log('ALTapi web tests passed');
