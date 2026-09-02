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
 * **It does not assume it is a popup.** A popup blocker, a middle-click, or
 * somebody restoring the tab later all land here with no opener. Then this is
 * an ordinary page and the honest thing is to continue into the dashboard
 * rather than sit on a dead end telling somebody to close a window they did
 * not open.
 *
 * **It closes only what it opened.** `window.close()` is ignored by browsers
 * for a tab a script did not open, which is exactly the case above — so the
 * redirect is not a fallback for the close failing, it is the path for a
 * window that was never a popup.
 */

/** The one message the opener listens for. Kept in step with accounts-login.js. */
const SIGNED_IN_MESSAGE = 'arena:accounts-signed-in';

/** Where a non-popup lands instead: the dashboard this sign-in was for. */
const dashboardHref = () => new URL('./', window.location.href).href;

function finish() {
  const opener = window.opener;
  if (!opener || opener.closed) {
    /*
     * Not a popup. Go where the person was trying to get to. Replace rather
     * than assign, so Back does not walk them into this page again.
     */
    window.location.replace(dashboardHref());
    return;
  }

  try {
    opener.postMessage({ type: SIGNED_IN_MESSAGE }, window.location.origin);
  } catch (err) {
    /*
     * The opener may be gone between the check and the send. Nothing to
     * recover: its own session poll notices the cookie within a few seconds,
     * which is what that poll is for.
     */
  }
  window.close();

  /*
   * If the close was refused — some browsers decline for a window that has
   * navigated across origins on the way here — say something true rather than
   * leaving "You can close this window" above a window that will not close.
   */
  window.setTimeout(() => {
    const note = document.getElementById('note');
    if (note) note.textContent = 'You are signed in. This window can be closed.';
  }, 400);
}

finish();
