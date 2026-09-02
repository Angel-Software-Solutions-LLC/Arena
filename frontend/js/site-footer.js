'use strict';

/**
 * The footer every Angel site now wears, rendered in Arena's colours.
 *
 * > "Lastly make them pull the legal documents from support so that we don't
 * > have to update them 1000 times."
 *
 * So the list is not written here. It is fetched from the Accounts corpus at
 * runtime, and publishing a document puts it in this menu without anybody
 * touching Arena. The baked list below is a fallback for when that fetch
 * cannot happen — not a second source of truth.
 *
 * Built against `docs/build/footer-integration.md` on the Support repo, which
 * is the contract all four sites implement.
 *
 * What this file will not do
 * --------------------------
 * **It does not open the menu on hover.** CSS does, with `:hover` and
 * `:focus-within`. A script that also opened it would fight the click handler
 * below — pointer arrives, script opens, click closes — and the menu would
 * look broken for the first thing anybody tries. The click handler exists for
 * the phone, which has no hover to open with.
 *
 * **It does not render a Cookie preferences control.** Arena sets no optional
 * cookies: everything it stores is a session or an OAuth state, all strictly
 * necessary, and there is no `document.cookie` anywhere in this frontend. A
 * preferences control here would open a dialog that could change nothing,
 * which implies a choice was collected that never was. The contract says to
 * omit it rather than fake it, so it is omitted.
 *
 * @module site-footer
 */

import { ACCOUNTS_ORIGIN } from './accounts.js?v=20260825a';

/** Where the corpus lives, and where a reader is sent to read one. */
const LEGAL_INDEX_URL = `${ACCOUNTS_ORIGIN}/api/legal/documents`;
const STATUS_URL = `${ACCOUNTS_ORIGIN}/status`;

/** The legal entity, which must match the one party to the documents. */
const COMPANY_NAME = 'Angel Software Solutions LLC';
const COMPANY_FOOTNOTE =
  'Angel Software Solutions LLC · registered in Delaware, United States · ' +
  'Houston, Texas, United States · EU presence: Vienna, Austria';

/**
 * The corpus as it stood when this shipped.
 *
 * Required by the contract, and a fallback only. The fetch above is
 * cross-origin and best-effort: it fails on a blocked network, behind a proxy
 * that strips CORS, offline, and during any Accounts outage — and "the Cookie
 * Policy is reachable from every page" is a compliance property that cannot
 * depend on another host answering.
 *
 * A document added to the corpus reaches a live page through the fetch. It
 * reaches this list when somebody edits it, which is the one manual step the
 * design does not remove.
 */
const FALLBACK_DOCUMENTS = [
  { slug: 'terms', title: 'Terms of Service' },
  { slug: 'privacy', title: 'Privacy Policy' },
  { slug: 'acceptable-use', title: 'Acceptable Use Policy' },
  { slug: 'cookies', title: 'Cookie Policy' },
  { slug: 'dpa', title: 'Data Processing Addendum' },
  { slug: 'subprocessors', title: 'Subprocessors' },
];

const MOBILE_MAX_WIDTH_PX = 767;
const MOBILE_MAX_LANDSCAPE_HEIGHT_PX = 500;

/** The one breakpoint, both clauses. A phone in landscape is still a phone. */
const isMobile = () =>
  window.matchMedia(
    `(max-width: ${MOBILE_MAX_WIDTH_PX}px), (max-height: ${MOBILE_MAX_LANDSCAPE_HEIGHT_PX}px)`,
  ).matches;

const legalHref = (slug) => `${ACCOUNTS_ORIGIN}/legal/${encodeURIComponent(slug)}`;

/**
 * Keep only entries that can be drawn and linked.
 *
 * The response is another service's, so it is treated as input rather than
 * trusted: anything without both a slug and a title is dropped, and a slug is
 * URL-encoded on the way into an href.
 */
function usableDocuments(payload) {
  const list = Array.isArray(payload?.documents) ? payload.documents : [];
  const clean = list
    .map((entry) => ({
      slug: String(entry?.slug ?? '').trim(),
      title: String(entry?.title ?? '').trim(),
    }))
    .filter((entry) => entry.slug && entry.title);
  return clean.length ? clean : null;
}

function renderItems(list, documents) {
  list.textContent = '';
  for (const document_ of documents) {
    const link = document.createElement('a');
    link.className = 'site-footer__item';
    link.href = legalHref(document_.slug);
    link.textContent = document_.title;
    // A real anchor, so ctrl-click and middle-click keep working: reading the
    // contract should never cost somebody the page they were on.
    link.rel = 'noopener';
    list.appendChild(link);
  }
}

/**
 * Build the footer and attach it.
 *
 * @param {HTMLElement} host element to render into.
 */
export function renderSiteFooter(host) {
  if (!host) return;

  const year = new Date().getFullYear();
  host.classList.add('site-footer');
  host.innerHTML = `
    <div class="site-footer__row">
      <p class="site-footer__copyright">© ${year} ${COMPANY_NAME}</p>
      <div class="site-footer__spacer"></div>
      <a class="site-footer__status" href="${STATUS_URL}">System status</a>
      <nav class="site-footer__legal" aria-label="Legal">
        <div class="site-footer__menu" data-open="false">
          <button class="site-footer__trigger" type="button" aria-haspopup="menu" aria-expanded="false">
            Legal <span class="site-footer__caret" aria-hidden="true">▾</span>
          </button>
          <div class="site-footer__panel" role="menu" aria-label="Legal documents">
            <div class="site-footer__scrim" data-footer-close></div>
            <div class="site-footer__sheet">
              <h2 class="site-footer__sheet-title">Legal</h2>
              <div class="site-footer__items"></div>
              <p class="site-footer__footnote">${COMPANY_FOOTNOTE}</p>
              <button class="site-footer__close" type="button" data-footer-close>Close</button>
            </div>
          </div>
        </div>
      </nav>
    </div>
  `;

  const menu = host.querySelector('.site-footer__menu');
  const trigger = host.querySelector('.site-footer__trigger');
  const items = host.querySelector('.site-footer__items');

  // The baked list first, so the menu is never empty while the fetch is in
  // flight — and stays correct if it never lands.
  renderItems(items, FALLBACK_DOCUMENTS);

  const setOpen = (open) => {
    menu.dataset.open = open ? 'true' : 'false';
    trigger.setAttribute('aria-expanded', open ? 'true' : 'false');
  };

  /*
   * Click is for the phone. On a pointer the panel is already open by the time
   * anything is clicked -- CSS did it -- so toggling here would close it on
   * the first press. Guarding on the breakpoint keeps the two from fighting,
   * which is the failure this whole arrangement exists to avoid.
   */
  trigger.addEventListener('click', () => {
    if (!isMobile()) return;
    setOpen(menu.dataset.open !== 'true');
  });

  host.querySelectorAll('[data-footer-close]').forEach((node) => {
    node.addEventListener('click', (event) => {
      event.stopPropagation();
      setOpen(false);
    });
  });

  // Selecting a document closes the sheet: a navigation closes a dialog
  // anyway, and doing it here means Back does not return to a page with a
  // panel open over it.
  items.addEventListener('click', () => setOpen(false));

  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') setOpen(false);
  });

  /*
   * The live list. `no-store` rather than a cache hint: the endpoint sends a
   * long `Cache-Control` with an ETag of its own, and the browser honours that
   * better than anything guessed here would.
   */
  fetch(LEGAL_INDEX_URL, { credentials: 'omit', cache: 'default' })
    .then((response) => (response.ok ? response.json() : null))
    .then((payload) => {
      const documents = usableDocuments(payload);
      if (documents) renderItems(items, documents);
    })
    .catch(() => {
      /* Offline, blocked, or Accounts is down. The baked list is already
         rendered and is exactly what this case is for. */
    });
}

/** Attach to every `[data-site-footer]` on the page. */
export function mountSiteFooters() {
  document.querySelectorAll('[data-site-footer]').forEach(renderSiteFooter);
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', mountSiteFooters, { once: true });
} else {
  mountSiteFooters();
}
