'use strict';

/**
 * Shared customer-session helpers. The chat panel and the dashboard (loaded
 * in its own iframe) both authenticate against the same
 * arena_customer_session cookie, so signing in on one is, from the server's
 * point of view, already signing in on the other. This module is what makes
 * that show up live in the UI on both sides without a manual reload:
 *  - notifySessionChanged() fires a `storage` event, which the browser
 *    delivers to every OTHER same-origin browsing context sharing this
 *    origin's localStorage -- including a parent page and its same-origin
 *    iframe -- but never back to the writer itself, so this cannot loop.
 *  - startSessionSync() also polls on a slow interval as a fallback for
 *    session changes this module did not itself trigger (e.g. a sign-in
 *    completed in a different tab, a session expiring, or an OIDC redirect
 *    completing inside the dashboard iframe without going through
 *    notifySessionChanged()).
 * @module account-session
 */

import { apiPath } from './paths.js?v=20260710a';

const STORAGE_KEY = 'arena_session_touched';
const POLL_INTERVAL_MS = 20000;

/** Fetch the current customer session from the server. Null on any failure. */
export async function fetchAccountSession() {
  try {
    const resp = await fetch(apiPath('/account/session'), {
      credentials: 'same-origin',
      cache: 'no-store',
      headers: { Accept: 'application/json' },
    });
    if (!resp.ok) return null;
    return await resp.json();
  } catch (err) {
    return null;
  }
}

/** Call after a sign-in or sign-out completes so other tabs/frames notice immediately. */
export function notifySessionChanged() {
  try {
    localStorage.setItem(STORAGE_KEY, String(Date.now()));
  } catch (err) {
    // Storage can be unavailable (private browsing, quota); the slow poll
    // fallback in startSessionSync still catches the change eventually.
  }
}

function sessionSignature(session) {
  if (!session || !session.authenticated) return 'anon';
  return session.account?.id || 'anon';
}

/**
 * Keep `onChange(session)` in sync with the server. Calls it once
 * immediately, then again whenever the signed-in account actually changes
 * (not on every poll -- callers do not need to debounce).
 * @returns {() => void} stop function
 */
export function startSessionSync(onChange) {
  let lastSignature;
  let stopped = false;

  const check = async () => {
    if (stopped) return;
    const session = await fetchAccountSession();
    const signature = sessionSignature(session);
    if (signature !== lastSignature) {
      lastSignature = signature;
      onChange(session);
    }
  };

  const onStorage = (event) => {
    if (event.key === STORAGE_KEY) check();
  };
  const onVisible = () => {
    if (document.visibilityState === 'visible') check();
  };

  window.addEventListener('storage', onStorage);
  document.addEventListener('visibilitychange', onVisible);
  const pollTimer = setInterval(check, POLL_INTERVAL_MS);
  check();

  return () => {
    stopped = true;
    window.removeEventListener('storage', onStorage);
    document.removeEventListener('visibilitychange', onVisible);
    clearInterval(pollTimer);
  };
}
