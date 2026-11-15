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
  // 通用管理页（与 Deploy 同构）由共用标记提供，ALTapi 不重复声明；
  // 侧栏只保留注册机专属的两页。
  'data-page="altreg"', 'data-page="altsettings"',
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
// ALTapi 有自己的推理号池，所以通用管理页（概览/账号/密钥/路由/日志）全部适用，
// ALTAPI_PAGES = [...PROXY_PAGES, 注册机两页]；专属页单独列出用于跨池拦截。
assert.match(appSource, /const ALTAPI_PAGES = \[\.\.\.PROXY_PAGES, 'altreg', 'altsettings'\]/);
assert.match(appSource, /const ALTAPI_ONLY_PAGES = \['altreg', 'altsettings'\]/);
for (const shared of ['overview', 'accounts', 'keys', 'routing', 'requests', 'errors', 'details']) {
  assert.ok(html.includes(`data-page="${shared}"`), '缺少通用页 ' + shared);
}
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
    && !expr.includes('altBadge') && !expr.includes('actions') && !expr.includes('altCandidateBalance')
    // 下面三类不来自上游文本：时间格式化结果、本地拼好的片段、布尔开关渲染的常量标记。
    && !expr.includes('toLocaleTimeString()') && !expr.includes('note') && !expr.includes('r.enabled')
    // hint 是本地写死的空态提示文案，不含上游内容
    && !expr.includes('hint'));
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

// 9.7) 刷新余额必须读后端真实字段（items/known/skipped），
//     早期读的是 refreshed/failed，提示永远是「已刷新 0 个」。
assert.ok(!/r\.refreshed/.test(altSource), '不能读不存在的 r.refreshed');
assert.match(altSource, /const items = r\.items \|\| \[\]/);
assert.match(altSource, /r\.totalBalanceUsd/);
assert.match(altSource, /r\.skipped/);
// 后端要回 skipped，前端才能区分「没凭据跳过」和「刷新失败」。
const quotaSrc = fs.readFileSync(path.join(__dirname, 'altapi_quota.go'), 'utf8');
assert.match(quotaSrc, /"skipped":\s*skipped/);
assert.match(quotaSrc, /skipped\+\+/);

// 9.8) 每个账号下都要能展示额度：候选行与号池行各有渲染函数，
//      且无凭据时明确说缺什么，不能只显示一个「未知」。
assert.match(altSource, /function altCandidateBalance/);
assert.match(altSource, /缺会话/);
assert.match(altSource, /quota_checked_at/);
assert.match(altSource, /used_quota/);
assert.ok(altSource.includes('altCandidateBalance(r)'), '候选表余额列必须用 altCandidateBalance');
for (const col of ['邮箱','状态','余额','Key / 会话','代理','UID','说明','操作']) {
  assert.ok(html.includes('<th>' + col + '</th>'), '候选表缺少列：' + col);
}

// 9.9) 多选与批量按钮
for (const part of ['id="alt-check-all"', 'id="alt-assign-proxy"', 'id="alt-sel-hint"']) {
  assert.ok(html.includes(part), '缺少 ' + part);
}
assert.ok(html.includes('<th class="col-check">'), '表头缺少勾选列');
assert.ok(altSource.includes('const altSelected = new Set()'));
assert.match(altSource, /function altTargetIDs\(\)/);
assert.match(altSource, /altSelected\.size \? \[\.\.\.altSelected\] : undefined/);
// 四个动作都要带上选中集
for (const act of ['/candidates/assign-proxy', '/quota-refresh', '/candidates/import-all', '/candidates/bulk-register']) {
  const idx = altSource.indexOf(`'${act}'`);
  assert.ok(idx > 0, '前端没调用 ' + act);
  const around = altSource.slice(Math.max(0, idx - 300), idx + 300);
  assert.ok(around.includes('ids'), act + ' 没有带选中 ids');
}
// 分配代理成功后要清空选择
assert.match(altSource, /altSelected\.clear\(\)/);
// CSS 有勾选列
const css = fs.readFileSync(path.join(__dirname, 'web/app.css'), 'utf8');
assert.ok(css.includes('.table .col-check'), 'CSS 缺少勾选列样式');
assert.ok(css.includes('row-selected'), 'CSS 缺少选中行样式');

// 9.10) 已入池状态与退回注册机
assert.ok(altSource.includes("imported: '已入池'"), '缺少「已入池」状态文案');
assert.match(altSource, /r\.imported_account_id \? 'imported' : r\.status/,  '已入池应优先于 key_created 显示');
assert.ok(html.includes('data-oc-batch="alt-return"'), '账号页缺少「退回 ALTapi 注册机」按钮');
assert.ok(appSource.includes("[data-oc-batch=\"alt-return\"]"), '按钮显隐规则缺失');
assert.ok(appSource.includes("classList.toggle('hidden', !isALTapi())"), '按钮未按 altapi 池显隐');
assert.match(appSource, /\/api\/altapi\/return-to-registrar/);
// altapi 也必须有勾选框与批量栏，否则选不中账号就退不回
assert.match(appSource, /const managedKeyPool = \(\) =>[^;]*isALTapi\(\)/);

// 9.11) 候选筛选标签页：默认隐藏已入池
for (const t of ['pending', 'imported', 'all']) {
  assert.ok(html.includes(`data-alt-tab="${t}"`), '缺少标签 ' + t);
}
assert.ok(/data-alt-tab="pending"[^>]*class="alt-tab active"/.test(html) || html.includes('class="alt-tab active" data-alt-tab="pending"'),
  '默认激活的应是「待处理」');
assert.ok(html.includes('data-alt-tab="imported" role="tab" aria-selected="false">已入池'), '缺少「已入池」标签');
assert.match(altSource, /const altIsImported = \(r\) => !!r\.imported_account_id/);
assert.match(altSource, /if \(altTab === 'imported'\) return rows\.filter\(altIsImported\)/);
assert.match(altSource, /if \(altTab === 'all'\) return rows;/);
assert.match(altSource, /return rows\.filter\(\(r\) => !altIsImported\(r\)\)/, 'pending 默认要排除已入池');
// 计数与全选都要基于可见行
assert.match(altSource, /setN\('alt-tab-imported', importedN\)/);
assert.match(altSource, /altVisibleRows\(altRows\)\.forEach/);
assert.ok(altSource.includes("altSwitchTab('imported')"), '导入成功后应跳到已入池');
// CSS 有标签样式
const css2 = fs.readFileSync(path.join(__dirname, 'web/app.css'), 'utf8');
assert.ok(css2.includes('.alt-tab.active'), 'CSS 缺少激活态样式');

// 9.12) 自动补号设置
// 模式二选一：用 radio，不是两个 checkbox
assert.ok(html.includes('name="alta-mode"'), '模式应使用单选');
assert.ok(html.includes('id="alta-mode-threshold"') && html.includes('id="alta-mode-frequency"'));
assert.ok(!html.includes('id="alta-threshold-on"'), '旧的双复选框应已移除');
assert.ok(!html.includes('id="alta-freq-on"'), '旧的双复选框应已移除');
assert.match(altSource, /function syncALTModeBody\(\)/);
assert.match(altSource, /threshold_enabled: !freq/);
assert.match(altSource, /frequency_enabled: freq/);

for (const part of [
  'id="alta-enabled"', 'id="alta-min"',
  'id="alta-freq-min"', 'id="alta-freq-max"',
  'id="alta-max"', 'id="alta-save"', 'id="alta-run"', 'id="alta-status"',
]) {
  assert.ok(html.includes(part), '缺少自动补号设置项 ' + part);
}
assert.match(altSource, /function altCollectAuto\(\)/);
assert.match(altSource, /altApi\('\/auto', \{ method: 'PUT'/);
assert.match(altSource, /altApi\('\/auto\/run', \{ method: 'POST'/);
assert.match(appSource, /if \(page === 'altsettings'\) startALTAutoPolling\(\)/);
assert.match(appSource, /stopALTAutoPolling\(\)/);
// 后端路由与设置字段
const autoSrc = fs.readFileSync(path.join(__dirname, 'altapi_auto.go'), 'utf8');
const regSrc = fs.readFileSync(path.join(__dirname, 'altapi_registration.go'), 'utf8');
assert.ok(regSrc.includes('/api/altapi/auto'), '缺少自动补号路由');
// 字段定义在存储层（跟着注册机设置一起落盘），校验在 server 侧
const storeSrc = fs.readFileSync(path.join(__dirname, '../altapisignupstore/store.go'), 'utf8');
for (const f of ['threshold_enabled', 'min_available', 'frequency_enabled', 'random_min_sec', 'random_max_sec', 'max_per_round']) {
  assert.ok(storeSrc.includes(f), '存储层缺少字段 ' + f);
}
for (const f of ['validAltAutoSettings', 'altAutoReplenish', 'altAutoRegister', 'altAutoAvailability']) {
  assert.ok(autoSrc.includes(f), '服务端缺少 ' + f);
}

// 9.13) 概览卡片必须能脱离「读取中…」
// 之前 altPrefetchStatus 只写了缓存却不重绘，loadOverview 同步拼 cards 时
// 缓存还是 null，卡片永远停在占位文案。
assert.match(altSource, /function syncALTOverviewCards\(\)/);
assert.match(altSource, /placeholder\.insertAdjacentHTML\('afterend', html\)/);
assert.match(altSource, /placeholder\.remove\(\)/);
assert.ok(altSource.includes('await altFetchStatus();'), '预取后必须重绘');
// app.js 在拼完 cards 后要触发预取
assert.match(appSource, /if \(isALTapi\(\) && window\.POOL_ALTAPI\) window\.POOL_ALTAPI\.altPrefetchStatus\(\)/);
// 节流与去重：不能每次概览刷新都真打一次
assert.match(altSource, /async function altFetchStatus\(\{ maxAgeMs = 5000 \} = \{\}\)/);
assert.match(altSource, /if \(altStatusInflight\) return altStatusInflight/);
// 后端 /status 只能读快照，不许在这条路径上打上游
const statusSrc = fs.readFileSync(path.join(__dirname, 'altapi_registration.go'), 'utf8')
  .match(/func \(s \*Server\) apiALTAPIStatus[\s\S]*?\n\}/)[0];
assert.ok(statusSrc.includes('s.quota.Get(a.ID)'), '/status 应读配额快照');
assert.ok(!/altapiFetchQuota/.test(statusSrc), '/status 不能逐账号读上游余额');

// 9.14) 自动补号轮询不能覆盖用户正在编辑的表单
// 曾经 renderALTAuto 同时填表单和刷状态，5 秒轮询把它当状态刷新用，
// 结果参数填一半就被改回已保存的值。
const autoStatusFn = altSource.match(/function renderALTAutoStatus\(auto\)[\s\S]*?\n\}/);
assert.ok(autoStatusFn, '缺少 renderALTAutoStatus');
assert.ok(!/\.value\s*=/.test(autoStatusFn[0]), 'renderALTAutoStatus 不能写 input.value');
assert.ok(!/\.checked\s*=/.test(autoStatusFn[0]), 'renderALTAutoStatus 不能写 input.checked');
// 轮询必须调状态版，不是表单版
const pollFn = altSource.match(/function startALTAutoPolling\(\)[\s\S]*?\n\}/);
assert.ok(pollFn, '缺少 startALTAutoPolling');
assert.ok(pollFn[0].includes('renderALTAutoStatus(r)'), '轮询应只刷状态');
assert.ok(!/renderALTAuto\(r\)/.test(pollFn[0]), '轮询不能调 renderALTAuto');
// 「立即检查」也不能覆盖未保存输入
const runFn = altSource.match(/alta-run'\}[\s\S]*?\n  \}\);/);
if (runFn) assert.ok(!/renderALTAuto\(await/.test(runFn[0]), '立即检查不能覆盖表单');

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
