'use strict';

/**
 * One click, and you are signed in.
 *
 * > "[sign in should be one click, no intermediate screens] … Also I wouldn't
 * > mind an iFrame popup within the window either… you'll figure something out
 * > that's more elegant."
 *
 * This is that. Pressing Sign in opens a centered window on the Accounts
 * authorization flow immediately — no Arena screen in between asking which way
 * you would like to sign in, because there is only one way now.
 *
 * Not an iframe, and that is not a shortcut
 * -----------------------------------------
 * An iframe was the owner's first suggestion and it cannot work, for two
 * independent reasons, either of which is fatal on its own:
 *
 *  - accounts.angel-serv.com sends `frame-ancestors 'none'`. Every browser
 *    refuses to render it in a frame, and that header is a correct defence
 *    against clickjacking a login form — the fix is not to weaken it.
 *  - Passkeys refuse to operate in a cross-origin frame. Even with the header
 *    relaxed, the strongest way to sign in would be the one that stopped
 *    working.
 *
 * A popup keeps the sign-in on its own origin, in a window whose address bar
 * the person can read — which is the property that makes it safe to type a
 * password into — while leaving the page behind it exactly where it was.
 *
 * The opener never sees a token
 * -----------------------------
 * The popup completes the authorization against Arena's own callback, which
 * sets the session cookie and lands on `dashboard/signed-in.html`. That page
 * posts one bare notification back. No code, no token, and nothing worth
 * intercepting crosses `postMessage` — the opener reacts by asking the server
 * who it is now, which is the only answer that can be trusted anyway.
 *
 * @module accounts-login
 */

import { apiPath } from './paths.js?v=20260710a';
import { notifySessionChanged } from './account-session.js?v=20260823a';

/** The message `dashboard/signed-in.js` sends. Kept in step with that file. */
const SIGNED_IN_MESSAGE = 'arena:accounts-signed-in';

/** Big enough for a passkey prompt and a consent screen without scrolling. */
const POPUP_WIDTH = 520;
const POPUP_HEIGHT = 680;

/**
 * How long to keep watching a popup that was opened but never reported back.
 *
 * Long enough to read a consent screen, find a phone, and use an authenticator;
 * short enough that a window closed by hand does not leave a listener and a
 * timer alive on a page somebody keeps open all day.
 */
const WATCH_TIMEOUT_MS = 5 * 60 * 1000;
const CLOSE_POLL_MS = 400;

/**
 * Centre the popup on the screen the browser window is actually on.
 *
 * `screenX`/`availWidth` rather than `screen.width`, so a second monitor puts
 * the window in front of the person instead of on their primary display.
 */
function popupFeatures() {
  const dualLeft = window.screenLeft ?? window.screenX ?? 0;
  const dualTop = window.screenTop ?? window.screenY ?? 0;
  const width = window.innerWidth || document.documentElement.clientWidth || screen.width;
  const height = window.innerHeight || document.documentElement.clientHeight || screen.height;
  const left = Math.max(0, Math.round(dualLeft + (width - POPUP_WIDTH) / 2));
  const top = Math.max(0, Math.round(dualTop + (height - POPUP_HEIGHT) / 2.4));
  return `popup=yes,width=${POPUP_WIDTH},height=${POPUP_HEIGHT},left=${left},top=${top},` +
    'menubar=no,toolbar=no,location=yes,status=no,resizable=yes,scrollbars=yes';
}

/** The Arena endpoint that starts the flow. `popup=1` decides where it ends. */
function loginURL({ popup, returnTo }) {
  const url = new URL(apiPath('/dashboard/login'), window.location.href);
  if (popup) url.searchParams.set('popup', '1');
  if (returnTo) url.searchParams.set('return_to', returnTo);
  return url.href;
}

/**
 * Start a sign-in.
 *
 * Opens the popup and resolves when it reports back. Falls back to a full-page
 * redirect when the popup cannot be opened — which is not an error state: a
 * blocker firing is ordinary, and the redirect is the same flow taking the
 * long way round. The promise never rejects; a caller only needs to know
 * whether to re-read the session.
 *
 * @param {{returnTo?: string}} [options]
 * @returns {Promise<boolean>} true when a sign-in completed in the popup.
 */
export function signInWithAccounts(options = {}) {
  const returnTo = options.returnTo || '';
  let popup = null;
  try {
    popup = window.open(loginURL({ popup: true, returnTo }), 'arena-accounts-login', popupFeatures());
  } catch (err) {
    popup = null;
  }

  if (!popup) {
    // Blocked, or opened in a browser that will not. Same flow, one window.
    window.location.assign(loginURL({ popup: false, returnTo }));
    return Promise.resolve(false);
  }

  try {
    popup.focus();
  } catch (err) {
    /* Focus is a courtesy; a popup that will not take it still works. */
  }

  return new Promise((resolve) => {
    let settled = false;
    const finish = (signedIn) => {
      if (settled) return;
      settled = true;
      window.removeEventListener('message', onMessage);
      clearInterval(closeTimer);
      clearTimeout(giveUpTimer);
      resolve(signedIn);
    };

    const onMessage = (event) => {
      /*
       * Only this origin, and only this window. Any page can post to any
       * window it has a handle on, so neither check is ceremony: the origin
       * stops another site's message being read as ours, and the source check
       * stops an unrelated frame on our own origin doing the same.
       */
      if (event.origin !== window.location.origin) return;
      if (event.source !== popup) return;
      if (!event.data || event.data.type !== SIGNED_IN_MESSAGE) return;
      // Tell the other tabs and frames before anyone re-reads the session, so
      // the chat panel and the dashboard iframe move together.
      notifySessionChanged();
      finish(true);
    };

    window.addEventListener('message', onMessage);

    /*
     * A window closed by hand sends nothing. Polling `closed` is the only way
     * to notice, and resolving false lets the caller stop showing a spinner
     * rather than waiting out the timeout.
     *
     * It resolves false even though the sign-in may in fact have succeeded and
     * the person simply closed the window early — the caller re-reads the
     * session either way, and the server is what decides.
     */
    const closeTimer = setInterval(() => {
      if (popup.closed) finish(false);
    }, CLOSE_POLL_MS);

    const giveUpTimer = setTimeout(() => finish(false), WATCH_TIMEOUT_MS);
  });
}
