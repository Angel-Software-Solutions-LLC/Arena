import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const html = readFileSync(new URL('../frontend/admin/index.html', import.meta.url), 'utf8');
const match = html.match(/function hasActiveAdminAuth\(tokenValue, session\) \{[^}]+\}/);
assert.ok(match, 'admin auth predicate should remain independently testable');

const hasActiveAdminAuth = new Function(`return (${match[0]});`)();
assert.equal(hasActiveAdminAuth('admin-token', null), true, 'token auth should reconnect');
assert.equal(hasActiveAdminAuth('', { authenticated: true }), true, 'an Angel Accounts desk session should reconnect');
assert.equal(hasActiveAdminAuth('', { authenticated: false }), false);
assert.equal(hasActiveAdminAuth('', null), false);

assert.match(
  html,
  /scheduleSSEReconnect\(controller\)[\s\S]*hasActiveAdminAuth\(token, ssoSession\)/,
  'the reconnect scheduler should use the token-or-desk-session predicate',
);
assert.match(html, /addEventListener\('pagehide', stopSSE\)/, 'navigation must abort the old document stream');
assert.match(html, /event\.persisted[\s\S]*hasActiveAdminAuth\(token, ssoSession\)[\s\S]*startSSE\(\)/, 'BFCache restoration should restart an authenticated stream');

console.log('admin SSE reconnect accepts token and Angel Accounts desk authentication');
