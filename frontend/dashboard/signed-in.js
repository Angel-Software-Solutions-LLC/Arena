'use strict';

/**
 * Tell the window that opened this one that the sign-in finished, then leave.
 *
 * The whole of the popup's job. The session cookie was set by the callback
 * that redirected here, so the opener does not need anything *from* this
 * message except the fact of it — it re-reads the session itself, from the
 * server, which is the only account of what happened worth trusting.
 *
 * Three things this is careful about:
 *
 * **The message names its origin.** `postMessage` is told the exact origin
 * rather than `'*'`, so the notification cannot be read by a window on another
 * origin that happens to have a handle on this one.
 *
 * **A missing opener does not mean this was not a popup.** Accounts serves
 * `Cross-Origin-Opener-Policy: same-origin`, so the moment the popup navigated
 * to `/connect` the browser moved it into a fresh browsing context group and
 * severed `window.opener` for good — it is still null after the redirect back
 * here. Treating that as "not a popup" is what left the window sitting on the
 * dashboard instead of closing. The server only ever redirects here when the
 * sign-in asked for `popup=1`, so arriving at all is the evidence that this is
 * a popup; the opener reference is not.
 *
 * **The opener is told through storage, not only through the window.** A
 * `storage` write is same-origin and is delivered across browsing context
 * groups, so it survives the severance that kills `postMessage`. The opener
 * already listens for exactly this key for its cross-tab sync, so the popup
 * only has to touch it.
 *
 * **The redirect is the last resort, not the first.** `window.close()` is
 * ignored for a tab a script did not open — a restored tab, a middle-click —
 * and only once it has visibly failed is continuing into the dashboard the
 * right thing.
 */

/** The one message the opener listens for. Kept in step with accounts-login.js. */
const SIGNED_IN_MESSAGE = 'arena:accounts-signed-in';

/**
 * The cross-tab session key. Kept in step with account-session.js, which owns
 * it and whose startSessionSync() is what hears this write.
 *
 * Written here rather than imported so this page keeps its one virtue: it
 * loads and runs with no module graph behind it.
 */
const SESSION_TOUCHED_KEY = 'arena_session_touched';

/** How long to give window.close() before deciding it was refused. */
const CLOSE_GRACE_MS = 600;

/** Tell every same-origin window that the session changed. Survives COOP. */
function announceSession() {
  try {
    localStorage.setItem(SESSION_TOUCHED_KEY, String(Date.now()));
  } catch (err) {
    /* Private browsing or a full quota. The opener's slow poll still gets there. */
  }
}

/** Where a non-popup lands instead: the dashboard this sign-in was for. */
const dashboardHref = () => new URL('./', window.location.href).href;

function finish() {
  // First, and regardless of whether the opener reference survived: this is
  // what actually reaches the window the person is looking at.
  announceSession();

  const opener = window.opener;
  if (opener && !opener.closed) {
    try {
      opener.postMessage({ type: SIGNED_IN_MESSAGE }, window.location.origin);
    } catch (err) {
      /* Gone between the check and the send. The storage write already went. */
    }
  }

  window.close();

  /*
   * Close is refused for a window a script did not open, and may be refused
   * for one that crossed origins on the way here. Either way, by now it has
   * either happened or it has not: continue into the dashboard, which is where
   * the person was going, and say something true on the way in case the
   * navigation is slow.
   */
  window.setTimeout(() => {
    const note = document.getElementById('note');
    if (note) note.textContent = 'You are signed in. This window can be closed.';
    window.location.replace(dashboardHref());
  }, CLOSE_GRACE_MS);
}

finish();
