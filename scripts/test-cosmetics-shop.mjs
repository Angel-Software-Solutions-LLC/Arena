import assert from 'node:assert/strict';
import {existsSync, readFileSync} from 'node:fs';

const shopHTMLURL = new URL('../frontend/shop/index.html', import.meta.url);
const shopModuleURL = new URL('../frontend/js/cosmetics-shop.js', import.meta.url);
const shopCSSURL = new URL('../frontend/css/shop.css', import.meta.url);

assert.equal(existsSync(shopHTMLURL), true, 'cosmetics need a dedicated /shop/ document');
assert.equal(existsSync(shopModuleURL), true, 'the Shop needs an isolated catalog controller');

const shopHTML = readFileSync(shopHTMLURL, 'utf8');
const shopCSS = readFileSync(shopCSSURL, 'utf8');
const mainHTML = readFileSync(new URL('../frontend/index.html', import.meta.url), 'utf8');
const shopIDs = Array.from(shopHTML.matchAll(/\sid="([^"]+)"/g), match => match[1]);
assert.equal(shopIDs.length, new Set(shopIDs).size, 'Shop document must not contain duplicate IDs');
for (const match of shopHTML.matchAll(/aria-labelledby="([^"]+)"/g)) {
  for (const id of match[1].split(/\s+/)) {
    assert.ok(shopIDs.includes(id), `Shop aria-labelledby must resolve #${id}`);
  }
}

assert.match(shopHTML, /<main[^>]*id="cosmetic-shop"[^>]*tabindex="-1"/,
  'the skip-link target must accept programmatic focus without joining the normal tab order');
assert.match(shopHTML, /<link rel="modulepreload" href="\.\.\/assets\/vendor\/babylon-runtime\.[a-f0-9]{12}\.min\.js">/,
  'Shop should preload the content-hashed local Babylon runtime');
assert.doesNotMatch(shopHTML, /materialsLibrary/, 'Shop must not load the unused Babylon materials add-on');
assert.match(readFileSync(shopModuleURL, 'utf8'), /import ['"]\.\/babylon-runtime\.js\?v=[^'"]+['"];/,
  'Shop entry must initialize the local runtime before constructing its preview');
assert.equal((shopHTML.match(/<canvas\b/g) || []).length, 1, 'Shop must use one shared bot preview canvas');
assert.match(shopHTML, /id="shop-preview-canvas"/, 'the large bot preview canvas needs a stable hook');
assert.match(shopHTML, /data-shop-pack-list/, 'Shop needs a pack browser');
assert.match(shopHTML, /data-shop-kind/, 'Shop needs a product-type filter');
assert.match(shopHTML, /data-shop-kind[\s\S]{0,400}<option value="trails">Trails<\/option>/,
  'Trails must be a first-class filter even when catalog collection metadata changes');
assert.match(shopHTML, /data-shop-kind[\s\S]{0,500}<option value="body-forms">Full-body skins<\/option>/,
  'full-body skins must be a first-class product filter');
assert.match(shopHTML, /data-shop-sort/, 'Shop needs an explicit sort control');
assert.doesNotMatch(shopHTML, /<option value="price-(?:low|high)">/,
  'nothing has a price of its own any more, so there is nothing to sort by price');
assert.match(shopHTML, /data-shop-pack-detail/, 'Shop needs a selected-pack detail region');
assert.match(shopHTML, /data-shop-item-list/, 'pack detail must expose its complete item list');
assert.match(shopHTML, /data-shop-preview-pack/, 'customers need a full-pack preview control');
assert.match(shopHTML, /data-shop-rotate-left/, 'preview must have a non-gesture rotate-left control');
assert.match(shopHTML, /data-shop-rotate-right/, 'preview must have a non-gesture rotate-right control');
assert.match(shopHTML, /data-shop-reset-view/, 'preview must have a reset control');
assert.match(shopHTML, /data-shop-access[^>]*\shidden(?:\s|>)/,
  'the access link must not be keyboard-operable before a pack loads');

/*
 * The subscription model, stated where people look for a price. There is no
 * per-item licence, no All Access tier beside it, nothing to buy from Arena;
 * one subscription in the Angel account unlocks the whole catalog.
 */
assert.match(shopHTML, /data-shop-subscription/, 'Shop needs one prominent subscription banner');
assert.match(shopHTML, /Included with an Arena subscription/);
assert.match(shopHTML, /Nothing is sold separately/i,
  'the Shop still says items cannot be bought one at a time — shorter, but the claim is load-bearing');
assert.match(shopHTML, /data-shop-subscription-action/, 'the banner must lead to where the subscription is sold');
assert.match(shopHTML, /data-shop-subscription-action[^>]*\shidden(?:\s|>)/,
  'the subscription action must not be operable before the catalog says where it points');
assert.match(shopCSS, /\.shop-subscription-offer \[hidden\]\s*\{[^}]*display:\s*none\s*!important/s,
  'author button styles must not override the hidden state');
/*
 * The Shop sells nothing, and none of the retired per-item commerce may come
 * back. `/price/i` used to be on this list because Arena had no price to show
 * at all; it now quotes one — the subscription's, from the Accounts catalog,
 * at runtime. So the guard moves rather than lifts: no hard-coded figure
 * (`/\$\d/` stays, and is the one that matters), no per-item price anywhere,
 * and no purchase control.
 */
for (const retired of [/All Access/, /license/i, /licence/i, /\$\d/, /checkout/i, /stripe/i, /purchas/i, /dash_plan|dash_pack/]) {
  assert.doesNotMatch(shopHTML, retired, `the Shop must not carry per-item commerce copy: ${retired}`);
}
assert.doesNotMatch(shopHTML, /shop-item-price|data-shop-item-price|price-(?:low|high)/i,
  'a price may only ever describe the subscription, never an item or a sort order');
for (const mention of shopHTML.match(/[a-z-]*price[a-z-]*/gi) || []) {
  assert.match(mention, /^(?:data-)?shop-subscription-price$/i,
    `the only price hook in the Shop is the subscription figure, found: ${mention}`);
}
assert.doesNotMatch(shopCSS, /license|all-access|purchase/i, 'retired commerce styles must not linger');
for (const mention of shopCSS.match(/[a-z-]*price[a-z-]*/gi) || []) {
  assert.match(mention, /^(?:data-)?shop-subscription-price$/i,
    `the only price style in the Shop is the subscription figure, found: ${mention}`);
}
assert.match(mainHTML, /data-overlay-open="shop-overlay"[^>]*>[\s\S]*?<span>Shop<\/span>/,
  'the main command dock must open the Shop as a slide-out drawer');
assert.match(mainHTML, /class="mobile-command-actions"[\s\S]*?data-overlay-open="shop-overlay"[^>]*>Shop<\/button>/,
  'mobile quick actions must open the dedicated Shop drawer directly');
assert.match(mainHTML, /id="shop-overlay"[\s\S]*?data-src="\/shop\/[^"]*"/,
  'the Shop drawer must lazy-load the dedicated Shop document');

let source = readFileSync(shopModuleURL, 'utf8');
assert.match(source, /dataset\.shopPackId\s*=/, 'pack hooks must serialize as data-shop-pack-id in real DOM');
assert.match(source, /dataset\.shopItemId\s*=/, 'item hooks must serialize as data-shop-item-id in real DOM');
assert.doesNotMatch(source, /dataset\.shop(?:Pack|Item)ID\s*=/, 'dataset acronyms must not split into data-*-i-d attributes');
assert.doesNotMatch(source, /checkout|stripe|purchase_handoff|subscription_offer/i,
  'the Shop controller must not read checkout facts the catalog no longer publishes');
/*
 * `price_cents` is read again, but only off the subscription block the catalog
 * quotes from Accounts — never off a pack or an item, which is what its being
 * banned outright was protecting against.
 */
for (const line of source.split('\n')) {
  if (!/price_cents/.test(line)) continue;
  assert.match(line, /subscription\?\.price_cents|subscription\.price_cents/,
    `price_cents may only be read from the subscription block, found: ${line.trim()}`);
}
source = source.replace(/import ['"]\.\/babylon-runtime\.js[^'"]*['"];\r?\n/, '');
source = source.replace(/import \{[^}]*\} from '\.\/paths\.js[^']*';\r?\n/, `
  const appPath = (path, pathname = '/') =>
    (pathname === '/arena' || pathname.startsWith('/arena/')) ? '/arena' + path : path;
  const apiPath = (path, pathname = '/') =>
    (pathname === '/arena' || pathname.startsWith('/arena/')) ? '/arena/api/v1' + path : '/api/v1' + path;
`);
source = source.replace(/import \{ CosmeticShopPreview \} from '\.\/shop-preview\.js[^']*';\r?\n/,
  'class CosmeticShopPreview {}\n');
const shop = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);

const pack = {
  id: 'ember-pack',
  items: [
    {id: 'body-first', slot: 'bot_skin', asset_key: 'arena_set_003_ember'},
    {id: 'body-alt', slot: 'bot_skin', asset_key: 'arena_set_004_alt'},
    {id: 'weapon', slot: 'weapon_skin', asset_key: 'arena_set_003_ember'},
    {id: 'attachment', slot: 'attachment', asset_key: 'arena_set_003_ember'},
    {id: 'trail', slot: 'trail', asset_key: 'ember_sparks'},
  ],
};

const trailPack = {
  id: 'trail-only', name: 'Comet Tail', category_id: 'trails',
  items: [{id: 'trail-item', name: 'Comet Tail', slot: 'trail', asset_key: 'comet_tail'}],
};
const bodyFormPack = {
  id: 'giant-chicken-pack', name: 'Giant Chicken', category_id: 'body-forms',
  items: [{id: 'giant-chicken', name: 'Giant Chicken', slot: 'bot_skin', asset_key: 'body_giant_chicken'}],
};
const freePack = {
  id: 'starter-pack', name: 'Starter', category_id: 'season-one', is_free: true,
  items: [{id: 'starter-skin', name: 'Starter', slot: 'bot_skin', asset_key: 'arena_set_001_starter'}],
};
assert.equal(shop.isTrailPack(trailPack), true);
assert.equal(shop.isTrailPack(pack), false, 'a coordinated set containing a trail remains a set');
assert.equal(shop.isBodyFormPack(bodyFormPack), true);
assert.equal(shop.isBodyFormPack(pack), false, 'a coordinated chassis set is not a full-body skin');
assert.deepEqual(
  shop.sortCosmeticPacks([
    {id: 'zulu', name: 'Zulu'},
    {id: 'alpha', name: 'Alpha'},
  ], 'name').map(candidate => candidate.id),
  ['alpha', 'zulu'],
  'name sorting must be deterministic and must not mutate the catalog order',
);
assert.deepEqual(
  shop.sortCosmeticPacks([{id: 'zulu', name: 'Zulu'}, {id: 'alpha', name: 'Alpha'}], 'price-low').map(candidate => candidate.id),
  ['zulu', 'alpha'],
  'a retired price sort must fall back to catalog order, not throw or reorder',
);
assert.equal(shop.accessLabel(freePack), 'Free');
assert.equal(shop.accessLabel(pack), 'Included with subscription');

assert.deepEqual(shop.packPreviewLoadout(pack), {
  bot_skin: 'arena_set_003_ember',
  weapon_skin: 'arena_set_003_ember',
  attachment: 'arena_set_003_ember',
  trail: 'ember_sparks',
}, 'full-pack preview must use the first ordered item in every supported slot');
assert.deepEqual(shop.itemPreviewLoadout(pack.items[2]), {
  bot_skin: 'standard',
  weapon_skin: 'arena_set_003_ember',
  attachment: 'none',
  trail: 'standard',
}, 'individual preview must isolate the chosen item against standard defaults');
assert.deepEqual(shop.itemPreviewLoadout(pack.items[4]), {
  bot_skin: 'standard',
  weapon_skin: 'standard',
  attachment: 'none',
  trail: 'ember_sparks',
}, 'individual trail preview must isolate the selected trail against standard defaults');
assert.deepEqual(shop.packItems(pack).map(item => item.id), ['body-first', 'body-alt', 'weapon', 'attachment', 'trail'],
  'pack detail must preserve every catalog item, including multiple items in one slot');
assert.equal(shop.dashboardCosmeticsPath('/shop/'), '/?dash_open=1&dash_tab=cosmetics');
assert.equal(shop.dashboardCosmeticsPath('/arena/shop/'), '/arena/?dash_open=1&dash_tab=cosmetics');
assert.equal(shop.catalogPath('/arena/shop/'), '/arena/api/v1/cosmetics/catalog');
assert.equal(shop.subscriptionURL('https://accounts.example/shop/arena'), 'https://accounts.example/shop/arena');
for (const unsafe of ['/shop', 'http://accounts.example/', 'javascript:alert(1)', 'https://', 'https://a b', '']) {
  assert.equal(shop.subscriptionURL(unsafe), '', `${JSON.stringify(unsafe)} is not an address the Shop will send anybody to`);
}

class FakeStyle {
  constructor() { this.background = ''; }
}

class FakeElement {
  constructor(tagName = 'div') {
    this.tagName = tagName.toUpperCase();
    this.children = [];
    this.dataset = {};
    this.attributes = new Map();
    this.listeners = new Map();
    this.className = '';
    this.textContent = '';
    this.value = '';
    this.hidden = false;
    this.disabled = false;
    this.href = '';
    this.style = new FakeStyle();
  }
  append(...nodes) { this.children.push(...nodes); }
  appendChild(node) { this.children.push(node); return node; }
  replaceChildren(...nodes) {
    const active = globalThis.document?.activeElement;
    if (this.children.includes(active) && !nodes.includes(active)) {
      globalThis.document.activeElement = globalThis.document.body;
    }
    this.children = [...nodes];
  }
  setAttribute(name, value) { this.attributes.set(name, String(value)); }
  getAttribute(name) { return this.attributes.get(name) ?? null; }
  addEventListener(name, listener) { this.listeners.set(name, listener); }
  removeEventListener(name, listener) {
    if (this.listeners.get(name) === listener) this.listeners.delete(name);
  }
  click() {
    globalThis.document.activeElement = this;
    this.listeners.get('click')?.({currentTarget: this});
  }
  closest() { return null; }
}

class FakeRoot extends FakeElement {
  constructor(elements) { super('main'); this.elements = elements; }
  querySelector(selector) { return this.elements[selector] || null; }
}

const canvas = new FakeElement('canvas');
const status = new FakeElement('p');
const packList = new FakeElement('div');
const detail = new FakeElement('aside');
const itemList = new FakeElement('div');
const search = new FakeElement('input');
const category = new FakeElement('select');
const kind = new FakeElement('select');
const sort = new FakeElement('select');
const summary = new FakeElement('p');
const showMore = new FakeElement('button');
const packName = new FakeElement('h2');
const packDescription = new FakeElement('p');
const packAccess = new FakeElement('strong');
const packCount = new FakeElement('p');
const access = new FakeElement('a');
const accessNote = new FakeElement('p');
const previewPack = new FakeElement('button');
const previewLabel = new FakeElement('p');
const previewStatus = new FakeElement('p');
const rotateLeft = new FakeElement('button');
const rotateRight = new FakeElement('button');
const resetView = new FakeElement('button');
const subscription = new FakeElement('section');
const subscriptionAction = new FakeElement('a');
const subscriptionState = new FakeElement('p');
const root = new FakeRoot({
  '#shop-preview-canvas': canvas,
  '[data-shop-status]': status,
  '[data-shop-search]': search,
  '[data-shop-category]': category,
  '[data-shop-kind]': kind,
  '[data-shop-sort]': sort,
  '[data-shop-results-summary]': summary,
  '[data-shop-show-more]': showMore,
  '[data-shop-pack-list]': packList,
  '[data-shop-pack-detail]': detail,
  '[data-shop-item-list]': itemList,
  '[data-shop-pack-name]': packName,
  '[data-shop-pack-description]': packDescription,
  '[data-shop-pack-access]': packAccess,
  '[data-shop-pack-count]': packCount,
  '[data-shop-access]': access,
  '[data-shop-access-note]': accessNote,
  '[data-shop-preview-pack]': previewPack,
  '[data-shop-preview-label]': previewLabel,
  '[data-shop-preview-status]': previewStatus,
  '[data-shop-rotate-left]': rotateLeft,
  '[data-shop-rotate-right]': rotateRight,
  '[data-shop-reset-view]': resetView,
  '[data-shop-subscription]': subscription,
  '[data-shop-subscription-action]': subscriptionAction,
  '[data-shop-subscription-state]': subscriptionState,
});

globalThis.document = {
  activeElement: null,
  body: new FakeElement('body'),
  createElement: tagName => new FakeElement(tagName),
};
globalThis.window = Object.assign(new FakeElement('window'), {
  location: {pathname: '/shop/', search: '', href: 'https://arena.example/shop/'},
  ArenaCosmeticThemes: {swatchStyle: () => 'linear-gradient(#000, #fff)'},
  matchMedia: () => ({matches: false}),
});

const previewCalls = [];
const fakePreview = {
  init() { previewCalls.push({type: 'init'}); return this; },
  setLoadout(loadout) { previewCalls.push({type: 'loadout', loadout}); },
  rotateBy() {},
  resetRotation() {},
  dispose() {},
};
const bulkPacks = Array.from({length: 99}, (_, index) => {
  const number = String(index + 2).padStart(3, '0');
  const assetKey = `arena_set_${number}_signal_test`;
  const finalSet = number === '100';
  return {
    id: finalSet ? 'arena-set-100-apex-radiance-pack' : `signal-set-${number}`,
    name: finalSet ? 'Apex Radiance Set' : `Signal Set ${number}`,
    description: finalSet ? 'A coordinated three-piece Apex Radiance cosmetic set' : `Coordinated Arena set ${number}`,
    category_id: 'season-one',
    items: [{
      id: `chassis-${number}`,
      name: `Signal ${number} Chassis`,
      slot: 'bot_skin',
      asset_key: assetKey,
    }],
  };
});
const SUBSCRIBE_AT = 'https://accounts.example/shop/arena';
const catalog = {
  subscription: {product: 'arena', includes_all_cosmetics: true, url: SUBSCRIBE_AT},
  categories: [{id: 'season-one', name: 'Season One'}, {id: 'body-forms', name: 'Body Forms'}],
  packs: [
    {...pack, name: 'Ember Pack', category_id: 'season-one'},
    trailPack,
    bodyFormPack,
    freePack,
    ...bulkPacks,
  ],
};
let resolveCatalog;
const catalogReady = new Promise(resolve => { resolveCatalog = resolve; });
const controller = shop.initCosmeticsShop(root, {
  pathname: '/shop/',
  requestedPackID: 'ember-pack',
  updateURL: false,
  previewFactory: () => fakePreview,
  fetchImpl: async () => ({ok: true, json: async () => catalogReady}),
});
search.value = 'Signal Set 099';
search.listeners.get('input')({currentTarget: search});
resolveCatalog(catalog);
await new Promise(resolve => setTimeout(resolve, 0));

assert.equal(controller.snapshot().selectedPackID, 'signal-set-099',
  'a search typed during fetch must determine the selected pack when the response arrives');
assert.equal(packList.children.length, 1);
assert.equal(controller.snapshot().subscriptionURL, SUBSCRIBE_AT);
assert.equal(subscriptionAction.href, SUBSCRIBE_AT, 'the banner leads to where Accounts sells the subscription');
assert.equal(subscriptionAction.hidden, false);
assert.match(subscriptionAction.textContent, /Subscribe in your Angel account/);
assert.match(subscriptionState.textContent, /one subscription/i);
assert.equal(subscription.dataset.state, 'available');

category.value = 'season-one';
category.listeners.get('change')({currentTarget: category});
assert.equal(controller.snapshot().filteredCount, 1, 'collection filters should remain active before switching product families');
kind.value = 'trails';
kind.listeners.get('change')({currentTarget: kind});
assert.equal(controller.snapshot().filteredCount, 1, 'the Trails filter must isolate standalone trail products');
assert.equal(controller.snapshot().selectedPackID, 'trail-only');
assert.equal(packList.children[0].dataset.shopPackId, 'trail-only');
assert.equal(category.value, 'all', 'switching product families must clear an incompatible collection filter');

kind.value = 'body-forms';
kind.listeners.get('change')({currentTarget: kind});
assert.equal(controller.snapshot().filteredCount, 1, 'the full-body filter must isolate body-form products');
assert.equal(controller.snapshot().selectedPackID, 'giant-chicken-pack');
assert.equal(packList.children[0].dataset.shopPackId, 'giant-chicken-pack');
assert.match(packList.children[0].children[1].children[1].textContent, /Full-body skin/);
assert.match(packList.children[0].children[1].children[1].textContent, /included with subscription/,
  'a pack card says what unlocks it instead of a price');

kind.value = 'all';
kind.listeners.get('change')({currentTarget: kind});
sort.value = 'name';
sort.listeners.get('change')({currentTarget: sort});
assert.equal(controller.snapshot().sort, 'name');
assert.equal(packList.children[0].dataset.shopPackId, 'arena-set-100-apex-radiance-pack',
  'changing sort order must visibly reorder the product browser and its active selection');

search.value = 'set 100';
search.listeners.get('input')({currentTarget: search});
assert.equal(controller.snapshot().filteredCount, 1, 'natural spacing must find a hyphenated set number');
assert.equal(controller.snapshot().selectedPackID, 'arena-set-100-apex-radiance-pack');

search.value = '';
search.listeners.get('input')({currentTarget: search});
assert.equal(packList.children.length, 24, 'the 100-pack Shop must keep its initial DOM page bounded');
assert.equal(packList.children[0].dataset.shopPackId, 'arena-set-100-apex-radiance-pack',
  'the selected pack must stay visible when a broader filter is restored');

const emberSortedButton = packList.children.find(button => button.dataset.shopPackId === 'ember-pack');
emberSortedButton.click();
search.value = '';
search.listeners.get('input')({currentTarget: search});
assert.equal(controller.snapshot().selectedPackID, 'ember-pack');
assert.equal(packList.children[0].dataset.shopPackId, 'arena-set-100-apex-radiance-pack',
  'preserving a selection must not move it ahead of the selected name sort');
showMore.listeners.get('click')();
assert.equal(packList.children.length, 48, 'Show more must reveal one bounded page at a time');

const emberButton = packList.children.find(button => button.dataset.shopPackId === 'ember-pack');
emberButton.click();
await new Promise(resolve => setTimeout(resolve, 0));
assert.equal(packList.children.find(button => button.dataset.shopPackId === 'ember-pack'), emberButton,
  'pack selection must update in place so keyboard focus is not discarded');
assert.equal(document.activeElement, emberButton, 'pack selection must retain keyboard focus');
assert.equal(controller.snapshot().selectedPackID, 'ember-pack');
assert.equal(itemList.children.length, 5, 'selecting a pack must render every item, including its trail');
assert.equal(packCount.textContent, '5 included items');
assert.equal(packAccess.textContent, 'Included with subscription');
assert.equal(access.href, SUBSCRIBE_AT, 'a paid pack leads to the subscription, not to a checkout');
assert.equal(access.hidden, false);
assert.match(access.textContent, /Included with an Arena subscription/);
assert.match(accessNote.textContent, /Subscribe in your Angel account/);
assert.doesNotMatch(accessNote.textContent + access.textContent, /Stripe|checkout|Buy/i);
assert.deepEqual(previewCalls.at(-1).loadout, shop.packPreviewLoadout(pack));

search.value = 'Starter';
search.listeners.get('input')({currentTarget: search});
assert.equal(controller.snapshot().selectedPackID, 'starter-pack');
await new Promise(resolve => setTimeout(resolve, 0));
assert.equal(packAccess.textContent, 'Free');
assert.equal(access.href, '/?dash_open=1&dash_tab=cosmetics', 'a free pack goes straight to the Dashboard to be equipped');
assert.match(access.textContent, /Equip in Dashboard/);
search.value = '';
search.listeners.get('input')({currentTarget: search});
packList.children.find(button => button.dataset.shopPackId === 'ember-pack').click();
await new Promise(resolve => setTimeout(resolve, 0));

assert.equal(controller.snapshot().selectedPackID, 'ember-pack');
const weaponButton = itemList.children[2];
weaponButton.click();
await new Promise(resolve => setTimeout(resolve, 0));
assert.equal(controller.snapshot().selectedItemID, 'weapon');
assert.equal(itemList.children[2], weaponButton,
  'item selection must update in place so keyboard focus is not discarded');
assert.equal(document.activeElement, weaponButton, 'item selection must retain keyboard focus');
assert.deepEqual(previewCalls.at(-1).loadout, shop.itemPreviewLoadout(pack.items[2]));
assert.equal(itemList.children[2].getAttribute('aria-pressed'), 'true', 'selected item must publish pressed state');

controller.previewPack();
await new Promise(resolve => setTimeout(resolve, 0));
assert.equal(controller.snapshot().selectedItemID, '');
assert.match(canvas.dataset.previewSignature, /full-pack$/);

search.value = 'no such cosmetic';
search.listeners.get('input')({currentTarget: search});
assert.equal(controller.snapshot().selectedPackID, '');
assert.equal(canvas.dataset.previewSignature, 'standard:no-pack-selected',
  'an empty filter must clear stale pack cosmetics from the preview');
assert.deepEqual(previewCalls.at(-1).loadout, {
  bot_skin: 'standard',
  weapon_skin: 'standard',
  attachment: 'none',
  trail: 'standard',
});
controller.dispose();

/*
 * An operator who has not published where to subscribe. The banner still
 * says what unlocks the catalog, and every control falls back to the
 * Dashboard, which explains what is missing, rather than going dead.
 */
subscriptionAction.hidden = true;
subscriptionAction.attributes.clear();
const unlinkedController = shop.initCosmeticsShop(root, {
  pathname: '/arena/shop/',
  requestedPackID: 'ember-pack',
  updateURL: false,
  previewFactory: () => fakePreview,
  fetchImpl: async () => ({
    ok: true,
    json: async () => ({...catalog, subscription: {product: 'arena', includes_all_cosmetics: true}}),
  }),
});
await new Promise(resolve => setTimeout(resolve, 0));
assert.equal(unlinkedController.snapshot().subscriptionURL, '');
assert.equal(subscriptionAction.hidden, false);
assert.equal(subscriptionAction.href, '/arena/?dash_open=1&dash_tab=cosmetics');
assert.match(subscriptionAction.textContent, /Open your Dashboard/);
assert.match(subscriptionState.textContent, /not published yet/i);
assert.equal(subscription.dataset.state, 'unlinked');
assert.equal(access.href, '/arena/?dash_open=1&dash_tab=cosmetics', 'with no address the paid pack still leads somewhere honest');
assert.match(access.textContent, /Included with an Arena subscription/);
unlinkedController.dispose();


/* ------------------------------------ the price is Accounts', or absent */

/*
 * Arena charges nothing and holds no price. The Shop quotes the figure the
 * catalog carried over from the Accounts product catalog, so the number on
 * this page and the number on the card cannot disagree — and quotes nothing
 * at all when Accounts has not said one, which is the honest answer.
 */
assert.equal(shop.subscriptionPrice({price_cents: 999, currency: 'USD', interval: 'month'}).amount, '$9.99',
  'the figure is whatever Accounts sells the plan for');
assert.equal(shop.subscriptionPrice({price_cents: 999, interval: 'month'}).interval, 'month',
  'and carries the interval it is charged over');
assert.equal(shop.subscriptionPrice({price_cents: 1000, currency: 'USD'}).amount, '$10.00',
  'a round figure still reads as money');

for (const missing of [undefined, null, {}, {price_cents: 0}, {price_cents: -100}, {price_cents: 'free'}]) {
  assert.equal(shop.subscriptionPrice(missing), null,
    `no figure must be quoted for ${JSON.stringify(missing)}`);
}

assert.match(shopHTML, /data-shop-subscription-price/,
  'the Shop needs somewhere to put the price');
assert.match(shopHTML, /<p class="shop-subscription-price" data-shop-subscription-price hidden>/,
  'and it starts hidden, so a Shop that does not know one shows no empty line');
assert.match(shopCSS, /\.shop-subscription-price \{/,
  'the figure is styled as an answer to "how much", not as another line of prose');

/*
 * The block said the same thing three times: a paragraph, a note, and the
 * status line beside the button all restated what one subscription covers,
 * and the packs the page exists for started below the fold. One sentence
 * says it now. What it must not lose is the claim that nothing is sold
 * individually, which is pinned above.
 */
assert.doesNotMatch(shopHTML, /is unlocked by the Arena subscription, for every bot/,
  'the subscription block should not restate its own heading at length');
assert.doesNotMatch(shopHTML, /Cancelling keeps everything through the paid period/,
  'lapse mechanics belong where somebody manages a subscription, not on the shelf');

console.log('dedicated cosmetics Shop previews every pack and points at the one subscription that unlocks them');
