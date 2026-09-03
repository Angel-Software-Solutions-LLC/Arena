/*
 * The Admin Panel's human sign-in, after the retirement of Arena's own admin
 * SSO application.
 *
 * There is no /admin/login and no arena_admin_session cookie any more. An
 * administrator signs in at Angel Accounts like any other customer and is
 * admitted by the support-desk role on that sign-in, which means the panel is
 * acting on the ordinary customer cookie — so its mutations have to carry the
 * session's CSRF token, and its sign-in has to ask to be returned here.
 *
 * These checks run the panel's own functions rather than reading them, because
 * the failure this guards against is a lockout: a panel that cannot open, or
 * that opens and cannot save anything.
 */
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';

const html = readFileSync(new URL('../frontend/admin/index.html', import.meta.url), 'utf8');

// --- the retired flow leaves nothing behind -------------------------------

for (const retired of ['ssoLoginUrl', 'ssoLogoutUrl', "'/admin/login'", "'/admin/logout'",
  "'/arena/admin/login'", "'/arena/admin/logout'"]) {
  assert.ok(!html.includes(retired),
    `the admin panel still references the retired admin SSO flow: ${retired}`);
}
assert.ok(!/SSO Login/.test(html), 'the sign-in control must name Angel Accounts, not "SSO"');
assert.match(html, /Sign in with Angel Accounts/, 'the panel needs an Accounts sign-in control');

// --- a person signs in through Accounts, and only through Accounts ---------

/*
 * The token form is gone. A token a browser can hold is a token anything that
 * reaches this origin can read, and an admin credential is the worst thing to
 * keep there. Machine access is unaffected — see the X-Admin-Token assertion
 * further down, which is the whole point of removing this and not that.
 */
for (const humanEntry of ['id="tokenInput"', 'function doLogin', 'doLogin()']) {
  assert.ok(!html.includes(humanEntry),
    `the admin panel still offers a human a way to type an admin token: ${humanEntry}`);
}
assert.ok(!/localStorage\.setItem\(\s*'arena_admin_token'/.test(html),
  'the panel must never write an admin token into storage');
assert.match(html, /localStorage\.removeItem\('arena_admin_token'\)/,
  'and must clear one an older version of the panel left behind');

assert.match(
  html,
  /Requires a support-desk role, or an Arena administrator grant/,
  'the sign-in hint names both claims that admit, not just the desk role',
);
assert.match(
  html,
  /id="ssoUnavailable"/,
  'an Arena with no Accounts sign-in says so instead of showing an empty screen',
);

// --- slice out the functions under test -----------------------------------

function slice(startMarker, endMarker) {
  const start = html.indexOf(startMarker);
  assert.notEqual(start, -1, `missing source: ${startMarker}`);
  const end = html.indexOf(endMarker, start);
  assert.notEqual(end, -1, `missing source end: ${endMarker}`);
  return html.slice(start, end);
}

const authSource = slice('// Where this panel lives', 'function getSessionUrl()')
  + slice('function getPublicAPIBase()', 'function deskLoginUrl')
  + slice('async function api(path, opts={})', '// ========== Auth ==========');

function sandbox(overrides = {}) {
  const context = {
    location: { pathname: '/admin/' },
    ssoSession: null,
    deskCsrfToken: '',
    token: '',
    API_BASE: '/api/v1/admin',
    window: { location: { href: '', reload() { context.reloaded = true; } } },
    setLiveFeedStatus() {},
    logout() { context.loggedOut = true; },
    encodeURIComponent,
    console,
    calls: [],
    reloaded: false,
    loggedOut: false,
    ...overrides,
  };
  context.fetch = async (url, init = {}) => {
    context.calls.push({ url: String(url), init });
    return { ok: true, status: 200, async json() { return {}; } };
  };
  runInNewContext(authSource, context);
  return context;
}

// --- signing in returns to the panel it was started from ------------------

{
  const root = sandbox();
  assert.equal(root.adminPanelPath(), '/admin/');
  assert.equal(root.deskLoginUrl(),
    '/api/v1/dashboard/login?return_to=' + encodeURIComponent('/admin/'),
    'sign-in must ask Accounts to return to the admin panel');

  const prefixed = sandbox({ location: { pathname: '/arena/admin/' } });
  assert.equal(prefixed.adminPanelPath(), '/arena/admin/');
  assert.equal(prefixed.deskLoginUrl(),
    '/arena/api/v1/dashboard/login?return_to=' + encodeURIComponent('/arena/admin/'),
    'a prefixed deployment must not send its administrator to the unprefixed app');

  // The server is the authority on where sign-in lives; the computed path is
  // only the fallback for a bootstrap read that never arrived.
  const told = sandbox({ ssoSession: { login_url: '/api/v1/dashboard/login?return_to=%2Fadmin%2F%3Ftab%3Dchatmod' } });
  assert.equal(told.deskLoginUrl(), '/api/v1/dashboard/login?return_to=%2Fadmin%2F%3Ftab%3Dchatmod',
    'the panel must use the login URL the server handed it');
}

// --- a desk session's mutations carry the CSRF token ----------------------

{
  const desk = sandbox({ deskCsrfToken: 'csrf-abc' });
  await desk.api('/chat/enabled', { method: 'PUT', body: '{}' });
  const [call] = desk.calls;
  assert.equal(call.url, '/api/v1/admin/chat/enabled');
  assert.equal(call.init.credentials, 'same-origin',
    'the panel acts on a cookie, so the request must carry it');
  assert.equal(call.init.headers['X-CSRF-Token'], 'csrf-abc',
    'an admin mutation made on the customer cookie is refused without the CSRF token');
  assert.equal(call.init.method, 'PUT', 'the caller\'s method must survive');
}

{
  // A machine, or a human on the break-glass token path: no cookie session, so
  // no CSRF token, and the header must not appear at all.
  const machine = sandbox({ token: 'admin-token' });
  await machine.api('/dashboard/overview');
  const [call] = machine.calls;
  assert.equal(call.init.headers['X-Admin-Token'], 'admin-token',
    'the X-Admin-Token path must be untouched');
  assert.ok(!('X-CSRF-Token' in call.init.headers),
    'a token client must not send a CSRF token it does not have');
}

// --- signing out ends the session, not just the panel --------------------

{
  const desk = sandbox({ deskCsrfToken: 'csrf-abc', ssoSession: { logout_url: '/api/v1/dashboard/logout' } });
  await desk.deskSignOut();
  const [call] = desk.calls;
  assert.equal(call.url, '/api/v1/dashboard/logout');
  assert.equal(call.init.method, 'POST', 'signing out is a mutation');
  assert.equal(call.init.headers['X-CSRF-Token'], 'csrf-abc', 'signing out must carry the CSRF token');
  assert.equal(desk.deskCsrfToken, '', 'signing out must forget the CSRF token');
  assert.equal(desk.reloaded, true, 'signing out must land back on the sign-in screen');
}

// --- a lapsed grant must not strand the administrator ----------------------

{
  const logoutSource = html.slice(html.indexOf('function logout()'), html.indexOf('function showApp()'));
  assert.match(logoutSource, /deskCsrfToken = ''/, 'signing out of the panel must forget the CSRF token');
  assert.match(logoutSource, /if \(ssoEnabled\) document\.getElementById\('ssoSection'\)\.style\.display = ''/,
    'a desk grant that lapses mid-session must leave the sign-in control on screen');
}

// --- the bootstrap read is what decides whether the panel is drawn --------

const init = slice('// Ask the server what this browser', '</script>');
assert.match(init, /getSessionUrl\(\),\s*\{credentials: 'same-origin'/,
  'the bootstrap read must send the session cookie');
assert.match(init, /deskCsrfToken = data\.csrf_token \|\| ''/,
  'the bootstrap must keep the CSRF token the server handed over');
assert.match(init, /data\.authenticated[\s\S]*showApp\(\)/,
  'a desk administrator must land straight in the panel');
assert.match(init, /data\.login_enabled\s*!==\s*false/,
  'a deployment with no customer sign-in must not offer one');

console.log('admin panel desk sign-in checks passed');
