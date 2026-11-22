'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'web/app.js'), 'utf8');
const cell = { innerHTML: '' };
const context = {
  $: () => cell,
  localStorage: { getItem: () => 'factory' },
  hitRate: () => null,
  costSum: () => '—',
  o: { stats: { models: {} }, factoryUsage: { models: [] } },
};
vm.createContext(context);
vm.runInContext(source.slice(source.indexOf('const POOLS ='), source.indexOf('// navigator.clipboard')), context);
const start = source.indexOf('  const models = Object.entries(o.stats.models');
const end = source.indexOf('  const proxies = ', start);
assert.ok(start >= 0 && end > start);
const render = () => vm.runInContext('(() => {\n' + source.slice(start, end) + '\n})()', context);
const model = 'gemini <test>';
context.o.stats.models[model] = { r: 6, s: 6, c: 0, i: 3, o: 1 };
context.o.factoryUsage.models = [{ model, ttftP50Ms: 123, ttftP95Ms: 456, ttftSamples: 172 }];
render();
assert.match(cell.innerHTML, /123ms \/ 456ms<div class="acc-sub">累计样本 172<\/div>/);
assert.doesNotMatch(cell.innerHTML, /&lt;div|<test>/);
assert.match(cell.innerHTML, /gemini &lt;test&gt;/);
// Values are still escaped individually even though the container markup is not.
context.o.factoryUsage.models[0].ttftP50Ms = '<img src=x onerror=alert(1)>';
context.o.factoryUsage.models[0].ttftP95Ms = '<script>bad</script>';
render();
assert.doesNotMatch(cell.innerHTML, /<img|<script>/);
assert.match(cell.innerHTML, /&lt;img/);
context.o.factoryUsage.models = [];
render();
assert.doesNotMatch(cell.innerHTML, /累计样本|undefined/);
context.o.stats.models = {};
render();
assert.match(cell.innerHTML, /暂无请求/);
console.log('Overview TTFT markup, cumulative samples and escaping tests passed');
