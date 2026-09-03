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

/**
 * The size of the window, fixed by cross-product contract.
 *
 * 600 x 800 is what every Angel product that signs somebody in through
 * Accounts opens, so `/connect` renders the same wherever it is reached from
 * and Accounts has one target to lay out against. Arena opened 520 x 680
 * before this, which was too small: `/connect` scrolled.
 *
 * Both are a ceiling, not a promise. A laptop whose work area cannot spare
 * that much gets a window that fits it instead -- see `popupSize`.
 */
const POPUP_WIDTH = 600;
const POPUP_HEIGHT = 800;

/**
 * Room left around the window inside the screen's work area.
 *
 * `availWidth`/`availHeight` describe space the OS will let a window occupy,
 * but the numbers passed to `window.open` size the *viewport* -- the browser
 * adds its own title bar, address bar and borders outside them. Asking for
 * the whole work area therefore produces a window taller than the work area,
 * with its bottom edge under the taskbar. This is the allowance for that
 * chrome.
 */
const POPUP_SCREEN_MARGIN = 80;

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
 * The cross-tab session key `dashboard/signed-in.js` touches when a popup
 * sign-in finishes. Owned by account-session.js; named here because a storage
 * event is the one notification that survives what COOP does to this flow.
 */
const SESSION_TOUCHED_KEY = 'arena_session_touched';

/**
 * Below this, a `closed` reading is the browser, not a person.
 *
 * Accounts serves `Cross-Origin-Opener-Policy: same-origin`, so as soon as the
 * popup reaches `/connect` it moves to a new browsing context group and the
 * handle held here starts reporting `closed === true` while the window is
 * plainly still open. Polling alone could not tell that from somebody shutting
 * the window, and read it as an abandoned sign-in within one poll of opening —
 * which is why the dashboard used to fall back to signed-out while the person
 * was still typing their password. Nobody opens a window and closes it inside
 * a second and a half; that reading is severance, and the flow is still live.
 */
const SEVERANCE_WINDOW_MS = 1500;

/**
 * The contract size, clamped to what this screen can actually show.
 *
 * A window bigger than the work area does not get the space it asked for --
 * it gets cropped by the OS, which is the scrolling this contract exists to
 * remove, reintroduced from the other end. So the ceiling above is a minimum
 * against the room available rather than a fixed number.
 *
 * Exported so the contract can be tested against a screen rather than read
 * off the source: what matters is the number this returns on a small laptop,
 * which no amount of pattern-matching the file can tell you.
 *
 * @returns {{width: number, height: number}}
 */
export function popupSize() {
  const availWidth = screen.availWidth || screen.width || POPUP_WIDTH + POPUP_SCREEN_MARGIN;
  const availHeight = screen.availHeight || screen.height || POPUP_HEIGHT + POPUP_SCREEN_MARGIN;
  return {
    width: Math.round(Math.min(POPUP_WIDTH, availWidth - POPUP_SCREEN_MARGIN)),
    height: Math.round(Math.min(POPUP_HEIGHT, availHeight - POPUP_SCREEN_MARGIN)),
  };
}

/**
 * Centre the popup on the screen the browser window is actually on.
 *
 * `screenX`/`screenY` rather than assuming the primary display, so a second
 * monitor puts the window in front of the person instead of somewhere they
 * have to go looking for it.
 */
function popupFeatures() {
  const { width, height } = popupSize();
  const dualLeft = window.screenLeft ?? window.screenX ?? 0;
  const dualTop = window.screenTop ?? window.screenY ?? 0;
  const openerWidth = window.innerWidth || document.documentElement.clientWidth || screen.width;
  const openerHeight = window.innerHeight || document.documentElement.clientHeight || screen.height;
  const left = Math.max(0, Math.round(dualLeft + (openerWidth - width) / 2));
  const top = Math.max(0, Math.round(dualTop + (openerHeight - height) / 2));
  return `popup=yes,width=${width},height=${height},left=${left},top=${top},` +
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
      window.removeEventListener('storage', onStorage);
      window.removeEventListener('visibilitychange', onVisible);
      clearInterval(closeTimer);
      clearTimeout(giveUpTimer);
      resolve(signedIn);
    };

    /*
     * The storage write from the popup. This is the signal that actually
     * arrives: it is same-origin and crosses browsing context groups, so
     * unlike postMessage it is unaffected by the opener being severed.
     */
    const onStorage = (event) => {
      if (event.key !== SESSION_TOUCHED_KEY) return;
      finish(true);
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
    window.addEventListener('storage', onStorage);

    /*
     * A window closed by hand sends nothing. Polling `closed` is the only way
     * to notice, and resolving false lets the caller stop showing a spinner
     * rather than waiting out the timeout.
     *
     * It resolves false even though the sign-in may in fact have succeeded and
     * the person simply closed the window early — the caller re-reads the
     * session either way, and the server is what decides.
     */
    const openedAt = Date.now();
    let severed = false;

    const closeTimer = setInterval(() => {
      if (!popup.closed) return;
      if (Date.now() - openedAt < SEVERANCE_WINDOW_MS) {
        /*
         * COOP, not a person. The handle is useless from here, so stop asking
         * it: the storage write is what will report the sign-in, and the
         * timeout is what ends an abandoned one.
         */
        severed = true;
        clearInterval(closeTimer);
        window.addEventListener('visibilitychange', onVisible);
        return;
      }
      finish(false);
    }, CLOSE_POLL_MS);

    /*
     * With the handle severed there is no way to see the popup close, so the
     * person coming back to this window is the only other evidence that the
     * flow is over. Resolve false and let the caller re-read the session — the
     * server decides, and a sign-in that did complete still announces itself
     * through the page's own session sync.
     */
    function onVisible() {
      if (severed && document.visibilityState === 'visible') finish(false);
    }

    const giveUpTimer = setTimeout(() => finish(false), WATCH_TIMEOUT_MS);
  });
}
