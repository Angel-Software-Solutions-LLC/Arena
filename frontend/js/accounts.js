'use strict';

/**
 * Where Angel Accounts lives, and the one place that says so.
 *
 * Arena links to Accounts from several unrelated corners — the footer's legal
 * corpus, the status page, and now every invitation to get an account. Each of
 * those used to be free to write the origin out again, which is how a product
 * ends up with one of them still pointing at a host that moved.
 *
 * The register URL is a contract, not a preference
 * ------------------------------------------------
 * `/register?product=arena` is fixed across every Angel product. The `product`
 * parameter does nothing on the Accounts side today; it is carried anyway,
 * exactly as written, because the side that will consume it cannot be changed
 * retroactively for links people have already bookmarked or shared.
 *
 * @module accounts
 */

/** The Accounts origin. Every other Accounts URL in the frontend derives from this. */
export const ACCOUNTS_ORIGIN = 'https://accounts.angel-serv.com';

/**
 * Where a visitor without an Angel account is sent to make one.
 *
 * @param {string} [product] the Angel product asking. Arena, here, always.
 * @returns {string}
 */
export function accountsRegisterURL(product = 'arena') {
  return `${ACCOUNTS_ORIGIN}/register?product=${encodeURIComponent(product)}`;
}

/**
 * Point every `[data-accounts-register]` anchor at the register URL.
 *
 * The markup carries no href of its own on purpose: a literal in the HTML is
 * a second source of truth, and the whole point of this module is that there
 * is one. Called once on load, and again by anything that injects markup
 * later.
 *
 * @param {ParentNode} [root=document]
 */
export function applyAccountsLinks(root = document) {
  const href = accountsRegisterURL();
  root.querySelectorAll('a[data-accounts-register]').forEach((anchor) => {
    anchor.href = href;
    anchor.target = '_blank';
    anchor.rel = 'noopener';
  });
}

if (typeof document !== 'undefined') {
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', () => applyAccountsLinks());
  } else {
    applyAccountsLinks();
  }
}

// Compatibility surface for classic (non-module) scripts — the Dashboard's
// dashboard.js is one, and it renders account panels after this module has
// already run. Module consumers should use the named exports above.
if (typeof window !== 'undefined') {
  window.ArenaAccounts = Object.freeze({
    ACCOUNTS_ORIGIN,
    accountsRegisterURL,
    applyAccountsLinks,
  });
}
