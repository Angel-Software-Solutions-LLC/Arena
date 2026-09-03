'use strict';

/**
 * Signing in, from anywhere on the site, in one press.
 *
 * Arena has exactly one way for a person to sign in: Angel Accounts. It had
 * one before this module existed too — but every control that offered it
 * opened the Dashboard drawer first, and the drawer's signed-out shell asked
 * which of two ways you wanted, so the single way took two presses to reach.
 * This module is the missing middle: a control calls `startSignIn()` and the
 * Accounts window opens, with nothing of Arena's in between.
 *
 * Why the popup must open on the press itself
 * -------------------------------------------
 * Browsers only let `window.open` through while a user gesture is still in
 * effect, and a gesture does not survive a network round trip. So this module
 * never fetches anything on the way to opening the window: it keeps the last
 * known session from `startSessionSync` (which every page already runs) and
 * reads it synchronously at press time. The one fetch here is the fallback
 * for a press that arrives before that first read has landed, which is rare
 * and degrades to `accounts-login.js`'s own same-window redirect rather than
 * to a dead click.
 *
 * Consent is not a step in the flow
 * ---------------------------------
 * The legal gate runs before the window opens, because agreement collected
 * inside a window somebody did not expect is agreement collected badly. It is
 * shown once ever, so for everybody past their first sign-in it costs a
 * microtask and no press. That is why "one click" survives it.
 *
 * Nothing here is allowed to dead-end
 * -----------------------------------
 * An Arena with no customer OIDC configured is a real state — the checked-in
 * config ships that way — and a press in that state must say so. It resolves
 * `unconfigured` with the message the Dashboard has always shown, and the
 * caller puts it where its own visitor is looking.
 *
 * @module sign-in
 */

import { fetchAccountSession, startSessionSync } from './account-session.js?v=20260825a';
import { ensureConsent } from './consent-gate.js?v=20260714a';
import { signInWithAccounts } from './accounts-login.js?v=20260903a';

/** The one sentence Arena says when it cannot reach Angel Accounts at all. */
export const NOT_CONFIGURED_MESSAGE =
  'Angel Accounts sign-in is not configured on this Arena yet.';

/** Last session read from the server, or null before the first read lands. */
let known = null;
/** De-dupes a double press: two windows for one sign-in helps nobody. */
let inFlight = null;

/** Everything on the page that wants to hear about a session change. */
const listeners = new Set();
let stopSync = null;

/**
 * Keep this module's cached session current, and tell the caller about it too.
 *
 * One poll for the whole page, however many callers there are: `app.js` needs
 * the cache to decide what a press means, and the chat panel needs to
 * reconnect its socket, and those are the same question asked twice. A page
 * that already ran `startSessionSync` for its own reasons should call this
 * instead.
 *
 * @param {(session: object|null) => void} [onChange]
 * @returns {() => void} stop function
 */
export function watchSignInState(onChange) {
  if (onChange) listeners.add(onChange);
  if (!stopSync) {
    stopSync = startSessionSync((session) => {
      known = session;
      listeners.forEach((listener) => listener(session));
    });
  } else if (onChange && known !== null) {
    // A late subscriber gets the current answer immediately, the same way
    // startSessionSync itself always fires once on subscribe.
    onChange(known);
  }
  return () => {
    if (onChange) listeners.delete(onChange);
    if (listeners.size === 0 && stopSync) {
      stopSync();
      stopSync = null;
    }
  };
}

/** The last session this module saw, or null if it has not seen one yet. */
export function knownSession() {
  return known;
}

/**
 * Can a press right now open the Accounts window?
 *
 * `'unknown'` until the first session read lands; callers that must decide
 * synchronously (see `app.js`'s Dashboard control) treat unknown as "do the
 * ordinary thing", never as "assume yes".
 *
 * @returns {'available'|'signed-in'|'unconfigured'|'unknown'}
 */
export function signInAvailability() {
  if (!known) return 'unknown';
  if (known.authenticated) return 'signed-in';
  return known.oidc_login_enabled === true ? 'available' : 'unconfigured';
}

/** True only when we positively know this visitor is signed out. */
export function isSignedOut() {
  return Boolean(known) && !known.authenticated;
}

/**
 * Start a sign-in.
 *
 * @param {{returnTo?: string}} [options]
 * @returns {Promise<{status: 'signed-in'|'closed'|'declined'|'unconfigured'|'already-signed-in', message?: string}>}
 */
export function startSignIn(options = {}) {
  if (inFlight) return inFlight;
  inFlight = runSignIn(options).finally(() => { inFlight = null; });
  return inFlight;
}

async function runSignIn({ returnTo = '' } = {}) {
  // Consent first, and before anything opens. `ensureConsent` resolves on a
  // microtask once accepted, so this does not cost the gesture.
  const accepted = await ensureConsent();
  if (!accepted) return { status: 'declined' };

  if (signInAvailability() === 'unknown') {
    known = await fetchAccountSession();
  }
  if (signInAvailability() === 'unconfigured') {
    return { status: 'unconfigured', message: NOT_CONFIGURED_MESSAGE };
  }
  if (signInAvailability() === 'signed-in') {
    return { status: 'already-signed-in' };
  }

  const signedIn = await signInWithAccounts({ returnTo });
  // Whatever the window reported. It resolves false for a window closed by
  // hand, and that window may still have completed the sign-in on its way
  // out — the server is what decides, not the popup.
  known = await fetchAccountSession();
  if (known?.authenticated) return { status: 'signed-in' };
  return { status: signedIn ? 'signed-in' : 'closed' };
}
