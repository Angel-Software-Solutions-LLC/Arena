import assert from 'node:assert/strict';
import {copyFileSync, mkdtempSync, readFileSync, rmSync, writeFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join, resolve, relative} from 'node:path';
import {pathToFileURL, fileURLToPath} from 'node:url';

/**
 * Signing in is one press, from wherever the press happens.
 *
 * The thing that made it two was never the Accounts flow — that has always
 * opened on a press. It was Arena's own screen in front of it, asking which of
 * two ways you wanted to sign in, back when there were two. This spec guards
 * the properties that stop that screen growing back, and the ones that are
 * easy to break without noticing:
 *
 *  - the popup is opened on the press, with no network round trip in front of
 *    it, because a browser blocks a window opened after one;
 *  - the legal consent gate still runs first, and running it is not a press;
 *  - an Arena with no customer OIDC says so instead of opening nothing;
 *  - one page, one session poll, however many things are watching it.
 *
 * `sign-in.js` is exercised for real, against stub modules written to a temp
 * directory, rather than pattern-matched — ordering bugs are exactly what
 * reading the source cannot catch.
 */

const root = new URL('../', import.meta.url);
const read = (path) => {
  const base = resolve(fileURLToPath(root));
  const target = resolve(fileURLToPath(new URL(path, root)));
  const rel = relative(base, target);
  if (rel.startsWith('..') || resolve(rel) === rel) {
    throw new Error('Invalid path');
  }
  return readFileSync(target, 'utf8');
};

/* ------------------------------------------------- sign-in.js, executed */

const dir = mkdtempSync(join(tmpdir(), 'arena-sign-in-'));

const state = {
  calls: [],
  session: null,
  consent: true,
  popupResolves: true,
  syncs: 0,
  emit: null,
};
globalThis.__arenaSignInProbe = state;

writeFileSync(join(dir, 'account-session.js'), `
const probe = globalThis.__arenaSignInProbe;
export async function fetchAccountSession() {
  probe.calls.push('fetch');
  return probe.session;
}
export function startSessionSync(onChange) {
  probe.syncs += 1;
  probe.emit = onChange;
  onChange(probe.session);
  return () => { probe.stopped = true; };
}
export function notifySessionChanged() {}
`);

writeFileSync(join(dir, 'consent-gate.js'), `
const probe = globalThis.__arenaSignInProbe;
export function ensureConsent() {
  probe.calls.push('consent');
  return Promise.resolve(probe.consent);
}
`);

writeFileSync(join(dir, 'accounts-login.js'), `
const probe = globalThis.__arenaSignInProbe;
export function signInWithAccounts(options) {
  probe.calls.push('open');
  probe.lastOptions = options;
  return Promise.resolve(probe.popupResolves);
}
`);

copyFileSync(new URL('frontend/js/sign-in.js', root), join(dir, 'sign-in.js'));
const moduleHref = pathToFileURL(join(dir, 'sign-in.js')).href;
// A distinct query string is a distinct module URL, which is how each case
// below gets a module with its own cache rather than the previous case's.
const load = (label) => import(`${moduleHref}?case=${label}`);

const reset = (session) => {
  state.calls = [];
  state.session = session;
  state.consent = true;
  state.popupResolves = true;
  state.syncs = 0;
};

const SIGNED_OUT = {authenticated: false, oidc_login_enabled: true};
const NO_OIDC = {authenticated: false, oidc_login_enabled: false};
const SIGNED_IN = {authenticated: true, account: {id: 'acct-1'}, oidc_login_enabled: true};

/* ---------------------------------- nothing is assumed before it is known */

reset(null);
const cold = await load('cold');
assert.equal(cold.signInAvailability(), 'unknown',
  'before the first session read, availability is unknown -- never assumed');
assert.equal(cold.isSignedOut(), false,
  'and "signed out" is a fact, not the absence of one');

/* ------------------------------------------------ one page, one poll */

reset(SIGNED_OUT);
const shared = await load('shared');
const stopA = shared.watchSignInState(() => {});
const stopB = shared.watchSignInState(() => {});
assert.equal(state.syncs, 1,
  'two watchers share one poll: app.js and the chat panel ask the same question');
assert.equal(shared.signInAvailability(), 'available');
assert.equal(shared.isSignedOut(), true);
stopA();
assert.equal(state.stopped, undefined, 'one watcher leaving does not stop the other');
stopB();
assert.equal(state.stopped, true, 'the last one out stops the poll');

/* --------------------------- the press opens the window, and nothing first */

reset(SIGNED_OUT);
const pressed = await load('pressed');
pressed.watchSignInState(() => {});
state.calls = [];
const result = await pressed.startSignIn({returnTo: '/dashboard/'});
assert.equal(result.status, 'signed-in');
assert.deepEqual(
  state.calls.slice(0, 2),
  ['consent', 'open'],
  'consent, then the window -- with no fetch in between, which is what a browser blocks on',
);
assert.equal(state.lastOptions.returnTo, '/dashboard/', 'the caller decides where it lands');

/* ------------------------------------ the gate is honoured, and it is first */

reset(SIGNED_OUT);
const declined = await load('declined');
declined.watchSignInState(() => {});
state.calls = [];
state.consent = false;
const declinedResult = await declined.startSignIn();
assert.equal(declinedResult.status, 'declined');
assert.deepEqual(state.calls, ['consent'],
  'declining the legal gate opens no window at all');

/* -------------------------------------------- a press must never dead-end */

reset(NO_OIDC);
const unconfigured = await load('unconfigured');
unconfigured.watchSignInState(() => {});
assert.equal(unconfigured.signInAvailability(), 'unconfigured');
state.calls = [];
const unconfiguredResult = await unconfigured.startSignIn();
assert.equal(unconfiguredResult.status, 'unconfigured');
assert.equal(unconfiguredResult.message, unconfigured.NOT_CONFIGURED_MESSAGE);
assert.match(unconfiguredResult.message, /not configured on this Arena yet/,
  'and it says so in the words the Dashboard has always used');
assert.ok(!state.calls.includes('open'), 'nothing is opened when there is nowhere to open');

/* --------------------- a press before the first read still reaches Accounts */

reset(SIGNED_OUT);
const uncached = await load('uncached');
state.calls = [];
const uncachedResult = await uncached.startSignIn();
assert.deepEqual(state.calls.slice(0, 3), ['consent', 'fetch', 'open'],
  'with nothing cached it reads the session first rather than guessing');
assert.equal(uncachedResult.status, 'signed-in');

/* ------------------------------ a second press is not a second window */

reset(SIGNED_OUT);
const doublePress = await load('double');
doublePress.watchSignInState(() => {});
state.calls = [];
const [first, second] = await Promise.all([doublePress.startSignIn(), doublePress.startSignIn()]);
assert.equal(state.calls.filter((call) => call === 'open').length, 1,
  'an impatient double press opens one window, not two');
assert.equal(first.status, 'signed-in');
assert.equal(second.status, 'signed-in');

/* ---------------- the server decides, not the window that reported back */

reset(SIGNED_IN);
const closedEarly = await load('closed-early');
closedEarly.watchSignInState(() => {});
state.session = SIGNED_OUT;
state.emit(SIGNED_OUT);
state.popupResolves = false;    // window closed by hand
state.session = SIGNED_IN;      // but the sign-in had already completed
const closedResult = await closedEarly.startSignIn();
assert.equal(closedResult.status, 'signed-in',
  're-reading the session is what settles it, not what the popup managed to say');

reset(SIGNED_OUT);
const abandoned = await load('abandoned');
abandoned.watchSignInState(() => {});
state.popupResolves = false;
const abandonedResult = await abandoned.startSignIn();
assert.equal(abandonedResult.status, 'closed',
  'and a window genuinely closed without signing in reports exactly that');

/* ----------------------------------------- the callers, and what they do */

const app = read('frontend/js/app.js');
const chat = read('frontend/js/chat-panel.js');
const mobile = read('frontend/m/mobile.js');
const dashboardHTML = read('frontend/dashboard/index.html');
const indexHTML = read('frontend/index.html');

// The Dashboard drawer is the entry point every sign-in used to go through.
// A press by a visitor known to be signed out now starts the flow itself.
assert.match(app, /const openDashboardFromPress = /, 'the press has a decision of its own');
assert.match(
  app,
  /if \(!isSignedOut\(\) \|\| signInAvailability\(\) !== 'available'\) \{\s*openDashboardOverlay\(options\);/,
  'unknown or unconfigured means open the drawer, never guess and open a window',
);
assert.match(
  app,
  /startSignIn\(\)\.finally\(\(\) => openDashboardOverlay\(options\)\)/,
  'and whatever the sign-in does, the drawer still opens -- no press does nothing',
);
// Deep links and the Shop iframe are not user gestures. They must keep using
// the plain open, or the window they trigger is a window the browser blocks.
assert.match(app, /applyDeepLinkedDashboardOpen\(openDashboardOverlay\)/,
  'a deep link opens the drawer directly, with no window in front of it');
assert.match(app, /window\.ArenaOpenDashboard = openDashboardOverlay/,
  'and so does the Shop iframe calling in');

assert.match(app, /\[data-arena-signin\]/, 'controls that only sign in are marked, not matched on their text');
assert.match(indexHTML, /data-arena-signin/, 'and at least one exists on the page');
assert.match(app, /control\.hidden = signedIn/, 'an offer to sign in is withdrawn once you are signed in');

// "Sign in to chat" signs you in. It used to press the Dashboard control,
// which opened a drawer, which asked a question.
assert.doesNotMatch(chat, /function openDashboard\(/, 'the chat panel no longer routes through the drawer');
assert.doesNotMatch(chat, /fab-dashboard/, 'nor reaches for the mobile drawer button');
assert.match(chat, /chat-watermark-btn'\)\.addEventListener\('click', async \(\) => \{\s*const result = await startSignIn\(\);/,
  'the watermark press starts the flow directly');
assert.match(chat, /if \(result\.status === 'unconfigured'\) setStatus\(result\.message/,
  'and an Arena without Accounts says so on the line the reader is already watching');
assert.doesNotMatch(chat, /import \{ startSessionSync \}/,
  'the panel shares sign-in.js\'s session watch rather than starting a second poll');

// The mobile shell has the same drawer and needs the same press.
assert.match(mobile, /interceptPress: interceptDashboardPressForSignIn/, 'the mobile Dashboard FAB intercepts too');
assert.match(mobile, /if \(!isSignedOut\(\) \|\| signInAvailability\(\) !== 'available'\) return false;/,
  'with the same refusal to guess');
assert.match(mobile, /startSignIn\(\)\.finally\(open\)/, 'and the same guarantee that the drawer opens after');

/* ------------------------------------- the screen that used to be in the way */

assert.doesNotMatch(dashboardHTML, /class="login-choice secondary"/,
  'the signed-out Dashboard is no longer two equal choices');
assert.doesNotMatch(dashboardHTML, /Recommended</,
  'nothing has to be recommended when there is one of it');
const loginShell = dashboardHTML.slice(dashboardHTML.indexOf('<div id="login">'), dashboardHTML.indexOf('<div id="app">'));
assert.match(loginShell, /<details class="login-secondary"/,
  'the API key path is folded away, not removed');
assert.match(loginShell, /Bot operator\? Open with an API key/,
  'and a bot operator can still find it by name');
assert.match(loginShell, /id="apiKeyInput"/, 'the key field is still there');
assert.doesNotMatch(loginShell, /autofocus/,
  'and no longer takes the focus on a screen that is about signing in');
assert.ok(
  loginShell.indexOf('id="accountSignInButton"') < loginShell.indexOf('login-secondary'),
  'the account control comes first, in the DOM as well as on the screen',
);

/* -------------------------------------------------- getting an account */

const accounts = read('frontend/js/accounts.js');
// Copied to .mjs so Node reads it as the module it is, rather than sniffing a
// .js file against a package.json that does not declare a type.
copyFileSync(new URL('frontend/js/accounts.js', root), join(dir, 'accounts.mjs'));
const {accountsRegisterURL, ACCOUNTS_ORIGIN} = await import(pathToFileURL(join(dir, 'accounts.mjs')).href);
assert.equal(ACCOUNTS_ORIGIN, 'https://accounts.angel-serv.com');
assert.equal(accountsRegisterURL(), 'https://accounts.angel-serv.com/register?product=arena',
  'the cross-product register contract, exactly as specified -- product included');
assert.match(accounts, /a\[data-accounts-register\]/,
  'markup carries the marker; this module carries the URL');
assert.match(loginShell, /data-accounts-register/, 'the signed-out Dashboard invites you to make one');
assert.match(indexHTML, /data-accounts-register/, 'and so does the Arena page');
// One origin for the frontend. A literal written out again is a second source
// of truth for where Angel Accounts lives.
for (const [name, source] of [['app.js', app], ['chat-panel.js', chat], ['index.html', indexHTML],
  ['dashboard/index.html', dashboardHTML]]) {
  assert.doesNotMatch(source, /accounts\.angel-serv\.com\/register/,
    `${name} must not restate the register URL`);
}

/* ------------------------------------- the email flow is gone, all of it */

assert.doesNotMatch(app, /applyEmailTokenHandoff|email_token/,
  'the Arena page no longer forwards a token it can never receive');
assert.doesNotMatch(read('frontend/dashboard/dashboard.js'), /safeCustomerEmailRedirectPath/,
  'and the dashboard no longer validates a redirect nothing produces');
for (const [name, source] of [['dashboard.js', read('frontend/dashboard/dashboard.js')],
  ['account-session.js', read('frontend/js/account-session.js')]]) {
  assert.doesNotMatch(source, /magic-link|magic link/i,
    `${name} must not describe current behaviour in terms of a retired feature`);
}

rmSync(dir, {recursive: true, force: true});

console.log('one-click sign-in: ok');
