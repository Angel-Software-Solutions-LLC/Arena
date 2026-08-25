import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';

/**
 * Signing in is one press of one control, and it opens a window.
 *
 * This replaces test-dashboard-email-auth.mjs, which asserted the shape of a
 * feature that no longer exists — Arena's own passwordless email sign-in. That
 * test passing after the feature was retired would have been worse than no
 * test: a green guard around a dead path.
 *
 * What is checked here is the part that is easy to get wrong and impossible to
 * see in a screenshot: that the popup is opened directly by the press, that
 * the message it waits for is checked for origin *and* source, that a blocked
 * popup still signs somebody in, and that nothing sensitive crosses the
 * window boundary.
 */

const dashboard = readFileSync(new URL('../frontend/dashboard/index.html', import.meta.url), 'utf8')
  + readFileSync(new URL('../frontend/dashboard/dashboard.js', import.meta.url), 'utf8');
const login = readFileSync(new URL('../frontend/js/accounts-login.js', import.meta.url), 'utf8');
const landing = readFileSync(new URL('../frontend/dashboard/signed-in.js', import.meta.url), 'utf8');
const landingHTML = readFileSync(new URL('../frontend/dashboard/signed-in.html', import.meta.url), 'utf8');
const cosmeticsSource = readFileSync(new URL('../frontend/dashboard/account-cosmetics.js', import.meta.url), 'utf8');

/* ------------------------------------------------- one way in, and only one */

const sandbox = {};
vm.runInNewContext(cosmeticsSource, sandbox, {filename:'account-cosmetics.js'});
const session = sandbox.ArenaAccountCosmetics.normalizeSession({
  authenticated:false,
  login_enabled:true,
  oidc_login_enabled:true,
  email_login_enabled:false,
});
assert.equal(session.oidc_login_enabled, true);
assert.equal(session.email_login_enabled, false, 'the retired path must normalise to off');

assert.doesNotMatch(dashboard, /id="accountEmailForm"/, 'the email sign-in form is retired');
assert.doesNotMatch(dashboard, /id="accountEmailConfirm"/, 'the magic-link confirm step is retired');
assert.doesNotMatch(dashboard, /Email me a sign-in link/, 'nothing offers to mail a link any more');
assert.doesNotMatch(dashboard, /email_token/, 'no magic-link token is read out of the URL');
assert.doesNotMatch(dashboard, /account\/email\/start|account\/email\/verify/, 'the retired routes are not called');

/* ------------------------------------------------------------- one press */

assert.match(dashboard, /id="accountSignInButton"/, 'there is a sign-in control');
assert.match(dashboard, /Sign in with Angel Accounts/, 'and it names where it takes you');
// The legal gate is not a step in the flow -- it is shown once, ever, before
// anything opens. Asking for agreement inside a window somebody did not expect
// is how a consent gate gets clicked through.
const handler = dashboard.slice(dashboard.indexOf('async function startAccountLogin'));
assert.ok(
  handler.indexOf('ensureConsent') < handler.indexOf('signInWithAccounts'),
  'consent is settled before the window opens, not inside it',
);
assert.match(handler, /if \(!accepted\) return;/, 'and declining opens nothing');
assert.match(
  dashboard,
  /signInWithAccounts\(\{returnTo: accountReturnPath\(\)\}\)/,
  'pressing it starts the flow directly, with nowhere to come back to but where you were',
);
assert.match(
  dashboard,
  /import\('\.\.\/js\/accounts-login\.js/,
  'the dashboard reaches the popup module rather than reimplementing it',
);
// No Arena-side screen in between: the handler must not navigate somewhere to
// ask a question first. The only navigation left in the flow is the fallback
// inside accounts-login.js, when the popup could not be opened at all.
assert.doesNotMatch(
  dashboard.slice(dashboard.indexOf('async function startAccountLogin')),
  /navigateAccount\([^)]*login/,
  'sign-in must not route through an Arena page first',
);

/* ------------------------------------------------------------ the window */

assert.match(login, /window\.open\(/, 'a popup is opened');
assert.match(login, /width=\$\{POPUP_WIDTH\}/, 'at a fixed size');
assert.match(login, /left=\$\{left\},top=\$\{top\}/, 'centred on the window it came from');
assert.match(login, /popup=1/, 'and the server is told this is a popup so it lands on the right page');

assert.match(
  login,
  /if \(!popup\) \{[\s\S]*window\.location\.assign\(loginURL\(\{ ?popup: false/,
  'a blocked popup falls back to the same flow in one window',
);

/* --------------------------------------------------- what crosses the gap */

assert.match(
  login,
  /if \(event\.origin !== window\.location\.origin\) return;/,
  'a message from another origin is ignored',
);
assert.match(
  login,
  /if \(event\.source !== popup\) return;/,
  'and so is one from any window other than the popup itself',
);
assert.match(
  landing,
  /opener\.postMessage\(\{ type: SIGNED_IN_MESSAGE \}, window\.location\.origin\)/,
  'the notification is addressed to a specific origin, never "*"',
);
// The session is a cookie the server already set. Anything else crossing the
// window boundary would be a credential in a place it does not need to be.
assert.doesNotMatch(landing, /token|code|secret|id_token|access_token/i,
  'the popup hands over no credential of any kind');

/* ------------------------------------------------ the popup that is not one */

assert.match(
  landing,
  /if \(!opener \|\| opener\.closed\)/,
  'a window with no opener is not a popup and must not dead-end',
);
assert.match(landing, /window\.location\.replace\(dashboardHref\(\)\)/,
  'it continues to the dashboard instead');
assert.match(landingHTML, /<title>Signed in<\/title>/);
assert.match(landingHTML, /name="robots" content="noindex"/, 'this page should not be indexed');

/* ------------------------------------------------------- telling the rest */

assert.match(login, /notifySessionChanged\(\)/,
  'other tabs and the chat panel are told, so they move together');
assert.match(
  dashboard,
  /await initializeAccountMode\(\);/,
  'the opener re-reads the session from the server rather than trusting the message',
);

console.log('dashboard accounts-login: ok');
