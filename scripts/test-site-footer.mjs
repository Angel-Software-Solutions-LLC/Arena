import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

/**
 * The shared footer, held to the contract every Angel site implements.
 *
 * `docs/build/footer-integration.md` on the Support repo is the spec. What is
 * checked here is the part a screenshot cannot show: that the list comes from
 * the corpus at runtime, that a baked fallback exists for when it cannot, that
 * links go to the canonical URLs, and that Arena omits the control it has no
 * cookies to justify.
 *
 * Three of these guard bugs that already happened on a sibling site rather
 * than hypotheticals — the token with no fallback, the popover sized by
 * content, and hover-open written in JavaScript.
 */

const read = (path) => readFileSync(new URL(`../${path}`, import.meta.url), 'utf8');

const css = read('frontend/css/site-footer.css');
const js = read('frontend/js/site-footer.js');
/*
 * Where the shared footer goes, and where it deliberately does not.
 *
 * A footer needs a bottom of a document to sit at. `/shop/` and the legal
 * pages have one. `/` and `/m/` do not: both are fixed, full-viewport app
 * shells whose document height equals the viewport, so an element in the
 * normal flow after them lands at y=0 — on top of the header. The live page's
 * own footer has been rendering at zero height there for as long as it has
 * existed, which is why nobody noticed it was never visible.
 *
 * Putting a 63px bar across a live match view to satisfy "every page" would
 * have been worse than not having it. The exclusion is asserted so it stays a
 * decision rather than becoming an oversight.
 */
const pages = {
  shop: read('frontend/shop/index.html'),
  legal: read('frontend/legal/privacy.html'),
};
const appShells = {
  'the live spectator page': read('frontend/index.html'),
  'the mobile spectator shell': read('frontend/m/index.html'),
};
const router = read('go-arena/internal/api/router.go');
const headers = read('go-arena/internal/api/security_headers.go');

/* ------------------------------------------------------- on every page */

for (const [name, html] of Object.entries(pages)) {
  assert.match(html, /<footer data-site-footer><\/footer>/, `${name} renders the shared footer`);
  assert.match(html, /site-footer\.css/, `${name} loads the footer stylesheet`);
  assert.match(html, /site-footer\.js/, `${name} loads the footer script`);
}

for (const [name, html] of Object.entries(appShells)) {
  assert.doesNotMatch(
    html,
    /data-site-footer/,
    `${name} is a fixed app shell with no document bottom; a flow footer lands on its header`,
  );
}

/* ------------------------------------------------------------ anatomy */

assert.match(js, /© \$\{year\} \$\{COMPANY_NAME\}/, 'the row opens with the year and the legal name');
assert.match(js, /const COMPANY_NAME = 'Angel Software Solutions LLC'/, 'the legal name in full, including LLC');
assert.match(js, /new Date\(\)\.getFullYear\(\)/, 'the year is computed, never hard-coded');
assert.match(js, /const STATUS_URL = `\$\{ACCOUNTS_ORIGIN\}\/status`/, 'System status points at accounts');
assert.match(js, />System status<\/a>/, 'and says so');
assert.match(js, /aria-haspopup="menu"/, 'the Legal control announces its menu');
assert.match(js, /site-footer__caret[^>]*>▾/, 'with the caret the contract specifies');
// Matched piece by piece: the source wraps the sentence across two string
// literals, and what matters is that each fact is stated, not the line breaks.
for (const fact of [
  'Angel Software Solutions LLC',
  'registered in Delaware, United States',
  'Houston, Texas, United States',
  'EU presence: Vienna, Austria',
]) {
  assert.ok(js.includes(fact), `the registered-entity footnote is missing "${fact}"`);
}
assert.match(js, /site-footer__footnote">\$\{COMPANY_FOOTNOTE\}/, 'and it is rendered in the panel');

/* --------------------------------------------- the list, and its fallback */

assert.match(js, /const ACCOUNTS_ORIGIN = 'https:\/\/accounts\.angel-serv\.com'/, 'the corpus lives on accounts');
assert.match(js, /const LEGAL_INDEX_URL = `\$\{ACCOUNTS_ORIGIN\}\/api\/legal\/documents`/, 'the menu is fetched from the corpus index');
assert.match(js, /fetch\(LEGAL_INDEX_URL/, 'at runtime, on every page load');
assert.match(js, /\$\{ACCOUNTS_ORIGIN\}\/legal\/\$\{encodeURIComponent\(slug\)\}/, 'documents link to canonical URLs');
for (const slug of ['terms', 'privacy', 'acceptable-use', 'cookies', 'dpa', 'subprocessors']) {
  assert.ok(js.includes(`slug: '${slug}'`), `the baked fallback lists ${slug}`);
}
// Ordering, not proximity: the fallback must be on screen before the fetch is
// even started, so the menu is never empty and stays right if it never lands.
assert.ok(
  js.indexOf('renderItems(items, FALLBACK_DOCUMENTS)') < js.indexOf('fetch(LEGAL_INDEX_URL'),
  'the fallback must render before the fetch begins, not after it fails',
);
assert.match(js, /credentials: 'omit'/, 'the corpus is public and the fetch sends no credentials');

/* ------------------------------------ the three that bit the sibling site */

// (a) A token with no literal fallback renders as nothing on a page that does
//     not load the palette.
//
//     Comments are stripped first. The header of that stylesheet explains this
//     rule by quoting the bad form, and a check that read its own documentation
//     as a violation would be one nobody could satisfy.
const declarations = css.replace(/\/\*[\s\S]*?\*\//g, '');
const bareTokens = [...declarations.matchAll(/var\((--[a-z-]+)\)/g)].map(([, token]) => token);
assert.deepEqual(bareTokens, [], `every custom property needs a literal fallback; bare: ${bareTokens.join(', ')}`);

// (b) A popover sized by its content changes width with the corpus.
assert.doesNotMatch(css, /\.site-footer__panel[\s\S]{0,400}width:\s*max-content/, 'the panel must not be sized by content');
assert.match(css, /min-width:\s*250px/, 'it has a stated width instead');

// (c) Hover belongs to CSS. A script that also opened on hover would fight the
//     click handler and the menu would look broken on the first press.
assert.match(css, /\.site-footer__menu:hover \.site-footer__panel/, 'CSS opens the panel on hover');
assert.match(css, /:focus-within \.site-footer__panel/, 'and on keyboard focus');
assert.doesNotMatch(js, /mouseenter|mouseover/, 'JavaScript must not open the menu on hover');
assert.match(js, /if \(!isMobile\(\)\) return;/, 'the click handler is for the phone, where there is no hover');

/* ------------------------------------------------------- the breakpoint */

assert.match(css, /@media \(max-width: 767px\), \(max-height: 500px\)/, 'one breakpoint, both clauses');
assert.match(css, /min-height:\s*44px/, '44px rows on a phone');
assert.match(css, /\.site-footer__close/, 'and a visible Close');
assert.match(js, /max-width: \$\{MOBILE_MAX_WIDTH_PX\}px\), \(max-height: \$\{MOBILE_MAX_LANDSCAPE_HEIGHT_PX\}px/,
  'the script agrees with the stylesheet about what a phone is');

/* ------------------------------------- no cookie control, and why not */

// Two halves, and they need different views of the file: the control must not
// be rendered, and the reason it is not must be written down. The explanation
// lives in a comment, so the absence is checked against the code alone.
const footerCode = js.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '');
assert.doesNotMatch(footerCode, /Cookie preferences/, 'Arena sets no optional cookies, so it renders no preferences control');
assert.match(js, /does not render a Cookie preferences control/i, 'and the omission is explained where somebody would look');

/* ---------------------------------------------------- canonical redirects */

assert.match(router, /"\/legal\/terms":\s+accountsLegalBase \+ "\/terms"/, 'Terms redirects to the corpus');
assert.match(router, /"\/legal\/acceptable-use":\s+accountsLegalBase \+ "\/acceptable-use"/, 'so does Acceptable Use');
assert.match(router, /http\.StatusMovedPermanently/, 'permanently, so the two do not compete in search');
assert.doesNotMatch(router, /"\/legal\/privacy":/,
  'Privacy is NOT redirected: the corpus does not describe what Arena processes');

/* ------------------------------------------------------------- the CSP */

assert.match(
  headers,
  /connect-src[^;]*https:\/\/accounts\.angel-serv\.com/,
  'the runtime fetch is allowed by the CSP, or it would be blocked in the browser',
);

console.log('site footer: ok');
