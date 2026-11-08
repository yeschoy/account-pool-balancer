const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'web/app.js'), 'utf8');
let dialog;
const context = {
  esc: value => String(value).replace(/[&<>"']/g, c => ({'&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;'}[c])),
  modal: options => { dialog = options; },
};
vm.createContext(context);
vm.runInContext(source.slice(source.indexOf('function showClientRequestHeaders('), source.indexOf('function requestRow(')), context);
context.showClientRequestHeaders({requestId:'synthetic', sessionSource:'body', affinityKeyType:'claude_user_session', clientRequestHeaders:{'User-Agent':['<img onerror=bad>'], 'Session_id':['session-original-123'], 'Thread-Id':['<session&thread>']}, clientSessionHeaderStatus:{Session_id:'valid'}});
assert.match(dialog.body, /实际到达 Balancer/);
assert.match(dialog.body, /有效候选（不代表最终选中）/);
assert.match(dialog.body, /body \/ claude_user_session/);
assert.match(dialog.body, /session-original-123/);
assert.match(dialog.body, /&lt;session&amp;thread&gt;/);
assert.match(dialog.body, /会话头、缓存键按收到的原值展示/);
assert.match(dialog.body, /旧日志中已保存的 \[REDACTED\] 无法恢复/);
assert.match(dialog.body, /认证凭证继续隐藏/);
assert.match(dialog.body, /&lt;img/);
assert.doesNotMatch(dialog.body, /<img/);
context.showClientRequestHeaders({requestId:'legacy'});
assert.match(dialog.body, /历史日志未采集/);
assert.doesNotMatch(dialog.body, /未收到支持的会话候选头/);
context.showClientRequestHeaders({requestId:'no-session', clientRequestHeaders:{Accept:['application/json']}});
assert.match(dialog.body, /未收到支持的会话候选头/);
context.showClientRequestHeaders({requestId:'bounded', clientRequestHeaders:{}, clientRequestHeadersTruncated:true});
assert.match(dialog.body, /已截断/);
assert.match(dialog.body, /不能判断其余部分/);
assert.match(source, /closest\('\.rq-headers'\)/);
console.log('Mistral client-header UI tests passed');
