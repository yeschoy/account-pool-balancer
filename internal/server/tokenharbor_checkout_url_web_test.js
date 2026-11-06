'use strict';
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, 'web/tokenharbor.js'), 'utf8');
const begin = source.indexOf('function thCheckoutURL(raw) {');
const end = source.indexOf('\nfunction thCheckoutLink(', begin);
assert.ok(begin >= 0 && end > begin);
const ctx = vm.createContext({ URL });
vm.runInContext(source.slice(begin, end), ctx);
for (const raw of [
  'https://checkout.stripe.com/c/pay/cs_mock_modern',
  'https://checkout.stripe.com/pay/cs_mock_legacy',
  'https://checkout.stripe.com/checkout/cs_mock_opaque#mock',
  'https://checkout.stripe.com/new-format/opaque?session=mock',
  'https://checkout.stripe.com/?session=mock',
  'https://checkout.stripe.com/#mock-session',
]) assert.equal(ctx.thCheckoutURL(raw).href, raw);
for (const raw of [
  'https://checkout.stripe.com', 'https://checkout.stripe.com/', 'https://checkout.stripe.com/?#',
  'http://checkout.stripe.com/pay/mock', 'https://checkout.stripe.com:443/pay/mock',
  'https://checkout.stripe.com.evil.test/pay/mock', 'https://invoice.stripe.com/i/mock',
  'https://user@checkout.stripe.com/pay/mock', 'https://@checkout.stripe.com/pay/mock',
  'https://checkout.stripe.com/pay/mock\n', 'https://checkout.stripe.com/pay/mock secret',
]) assert.throws(() => ctx.thCheckoutURL(raw), /支付链接不可信/);
console.log('Token Harbor opaque Stripe URL tests passed');
