import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';

/*
 * The Dashboard's cosmetics model, exercised without a browser.
 *
 * Arena sells one thing and does not sell it itself: the Arena subscription,
 * held in Angel Accounts. Everything below follows from that. A snapshot has
 * an account, its linked bots, one subscription flag, the catalog items and
 * which bot wears what; the page either unlocks everything or says
 * "Included with an Arena subscription" and points at where to get one.
 */

const source = readFileSync(new URL('../frontend/dashboard/account-cosmetics.js', import.meta.url), 'utf8');
const sandbox = {};
vm.runInNewContext(source, sandbox, {filename: 'account-cosmetics.js'});
const cosmetics = sandbox.ArenaAccountCosmetics;

assert.ok(cosmetics, 'dashboard cosmetics helpers should attach to globalThis');
for (const retired of ['assignmentIntent', 'checkoutIntent', 'subscriptionIntent', 'normalizeSubscriptionOffer', 'normalizeOrder']) {
  assert.equal(cosmetics[retired], undefined, `${retired} belongs to the retired per-item model`);
}
assert.doesNotMatch(source, /stripe|checkout_|license_id|licenses|price_cents|purchase_handoff|subscription_offer|data-license/i,
  'the Dashboard model carries no per-item commerce');

// ---- Session -------------------------------------------------------------
assert.equal(cosmetics.normalizeSession({authenticated:false, account:{id:'acct-1',email:'owner@example.com',email_verified:true}}).authenticated, false,
  'an explicit logged-out response must never be inferred as authenticated');
const verifiedSession = cosmetics.normalizeSession({authenticated:true, login_enabled:true, login_url:'/api/v1/dashboard/login', account:{id:'acct-1',email:'owner@example.com',email_verified_at:'2026-07-10T12:00:00Z'}});
assert.equal(verifiedSession.account.email_verified, true, 'a server verification timestamp should authorize the account UX');
assert.equal(verifiedSession.login_enabled, true);
assert.equal(verifiedSession.login_url, '/api/v1/dashboard/login');
const signedOut = cosmetics.normalizeSession({authenticated:false,login_enabled:true,email_login_enabled:false,oidc_login_enabled:true});
assert.equal(signedOut.oidc_login_enabled, true);
assert.equal(signedOut.email_login_enabled, false);
assert.equal(signedOut.email_start_url, undefined, 'the retired endpoints are gone from the session shape');
const postCutoverSession = {
  authenticated: true,
  account: {id: 'acct-post-cutover', email: '', email_verified: true, display_name: 'Arena Pilot'},
};
const normalizedPostCutoverSession = cosmetics.normalizeSession(postCutoverSession);
assert.equal(normalizedPostCutoverSession.account.id, 'acct-post-cutover');
assert.equal(normalizedPostCutoverSession.account.email, '');
assert.equal(normalizedPostCutoverSession.account.name, 'Arena Pilot');
assert.equal(cosmetics.hasVerifiedAccount(postCutoverSession), true,
  'a signed-in verified Angel account with no email must remain eligible');
assert.equal(cosmetics.hasVerifiedAccount({...postCutoverSession, authenticated:false}), false);
assert.equal(cosmetics.hasVerifiedAccount({account:{id:'acct-legacy-email', email:'legacy@example.com', email_verified:true}}), false,
  'a verified legacy email account without explicit authentication must not be eligible');
assert.equal(cosmetics.hasVerifiedAccount({...postCutoverSession, account:{...postCutoverSession.account, id:''}}), false);
assert.equal(cosmetics.hasVerifiedAccount({...postCutoverSession, account:{...postCutoverSession.account, email_verified:false}}), false);
assert.equal(cosmetics.accountLabel(postCutoverSession.account), 'Arena Pilot');
assert.equal(cosmetics.accountLabel({display_name:'   ', name:'Legacy Name', email:'legacy@example.com'}), 'Legacy Name');
assert.equal(cosmetics.accountLabel({display_name:'Preferred Name', name:'Legacy Name'}), 'Preferred Name');
assert.equal(cosmetics.accountLabel({email:' Legacy@Example.COM '}), 'legacy@example.com');
assert.equal(cosmetics.accountLabel({}), 'Angel account');

// ---- Routes --------------------------------------------------------------
assert.equal(cosmetics.accountRoute('session'), '/account/session');
assert.equal(cosmetics.accountRoute('cosmetics'), '/account/cosmetics');
assert.equal(cosmetics.accountRoute('keys'), '/account/keys');
assert.equal(cosmetics.accountRoute('key', 'key/a'), '/account/keys/key%2Fa');
assert.equal(cosmetics.accountRoute('bots'), '/account/bots');
assert.equal(cosmetics.accountRoute('bot', 'bot/a'), '/account/bots/bot%2Fa');
assert.equal(cosmetics.accountRoute('equip', 'bot/a'), '/account/bots/bot%2Fa/cosmetics');
for (const gone of ['checkout', 'orders', 'orderCheckout', 'subscriptionCheckout', 'subscriptionPortal', 'assignment']) {
  assert.throws(() => cosmetics.accountRoute(gone), /unknown account route/i, `${gone} is not a route any more`);
}
assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.requestHeaders('PUT', 'csrf-value', true))),
  {Accept:'application/json','Content-Type':'application/json','X-CSRF-Token':'csrf-value'},
);
assert.equal(cosmetics.requestHeaders('GET', 'csrf-value', false)['X-CSRF-Token'], undefined, 'CSRF token should only be sent on mutations');

// ---- Snapshot ------------------------------------------------------------
const SUBSCRIBE_AT = 'https://accounts.example/shop/arena';
const items = [
  {id:'skin-free', name:'Standard Plus', slot:'bot_skin', asset_key:'arena_set_001_starter', rarity:'common', is_free:true},
  {id:'skin-neon-grid', name:'Neon <Grid>', slot:'bot_skin', asset_key:'arena_set_003_ember', rarity:'rare', description:'Glows.'},
  {id:'weapon-ember', name:'Ember Edge', slot:'weapon_skin', asset_key:'arena_set_003_ember', rarity:'epic'},
  {id:'trail-comet', name:'Comet Tail', slot:'trail', asset_key:'comet_tail', rarity:'rare', category_id:'trails'},
  {id:'trail-retired', name:'Retired Trail', slot:'trail', asset_key:'old_trail', rarity:'rare', is_active:false},
  {id:'weird-asset', name:'Bad Asset', slot:'attachment', asset_key:'../escape', rarity:'rare'},
];
const rawSnapshot = {
  account: {id: 'acct-1', email: 'Owner@Example.COM ', email_verified: true},
  bots: [
    {id: 'bot-1', name: 'Alpha', key_prefix: 'arena_alpha', avatar_color: '#123456', default_weapon: 'bow'},
    {id: 'bot-2', name: 'Beta', key_prefix: 'arena_beta', avatar_color: '#abcdef', default_weapon: 'staff', key_is_active:false},
  ],
  subscription: {active: true, synced_at: '2026-08-23T10:00:00Z', url: SUBSCRIBE_AT},
  items,
  loadouts: {'bot-1': {bot_skin:'skin-neon-grid', trail:'trail-retired'}, 'bot-2': {bot_skin:'skin-free'}, 'bot-missing': {bot_skin:'skin-neon-grid'}},
};
const snapshot = cosmetics.normalizeSnapshot(rawSnapshot);
assert.equal(snapshot.account.email, 'owner@example.com');
assert.equal(snapshot.bots[0].avatar_color, '#123456', 'linked bots must retain the server-owned preview color');
assert.equal(snapshot.bots[0].default_weapon, 'bow');
assert.equal(snapshot.bots[1].key_is_active, false);
assert.equal(snapshot.subscription.active, true);
assert.equal(snapshot.subscription.url, SUBSCRIBE_AT);
assert.equal(snapshot.items.length, 6, 'the whole catalog travels with the snapshot: there is no owned subset');
assert.equal(snapshot.items[4].is_active, false);
assert.deepEqual(JSON.parse(JSON.stringify(snapshot.loadouts['bot-1'])), {bot_skin:'skin-neon-grid', trail:'trail-retired'});
assert.equal(cosmetics.slotLabel('trail'), 'Trails');
const unsubscribed = cosmetics.normalizeSnapshot({...rawSnapshot, subscription:{active:false, synced_at:'', url:SUBSCRIBE_AT}});
assert.equal(unsubscribed.subscription.active, false);
assert.equal(cosmetics.normalizeSnapshot({...rawSnapshot, subscription:{active:'true'}}).subscription.active, false,
  'only a literal true subscribes; a string is not a flag');
assert.equal(cosmetics.normalizeSnapshot({...rawSnapshot, subscription:{active:true, url:'javascript:alert(1)'}}).subscription.url, '',
  'only an https address is somewhere the page will send anybody');
assert.equal(cosmetics.normalizeSnapshot({...rawSnapshot, subscription:{active:true, url:'/portal'}}).subscription.url, '');
assert.equal(cosmetics.normalizeSnapshot({...rawSnapshot, subscription:undefined}).subscription.active, false,
  'a snapshot with no subscription block reads as not subscribed');

// ---- Catalog and where to subscribe --------------------------------------
const catalog = cosmetics.normalizeCatalog({
  categories: [{id:'sets',name:'Sets'}, {id:'trails',name:'Trails'}],
  packs: [{id:'ember-pack', name:'Ember', items:[items[1], items[2]]}],
  items,
  subscription: {product:'arena', includes_all_cosmetics:true, url:SUBSCRIBE_AT},
});
assert.equal(catalog.subscription_url, SUBSCRIBE_AT);
assert.equal(catalog.checkout_enabled, undefined, 'the catalog publishes no checkout fact');
assert.equal(cosmetics.normalizeCatalog({subscription:{url:'http://accounts.example/'}}).subscription_url, '', 'plain http is not an address');
assert.equal(cosmetics.subscriptionURL(unsubscribed, null), SUBSCRIBE_AT, 'the snapshot carries the address');
assert.equal(cosmetics.subscriptionURL({...rawSnapshot, subscription:{active:false}}, catalog), SUBSCRIBE_AT, 'the catalog is the fallback');
assert.equal(cosmetics.subscriptionURL({...rawSnapshot, subscription:{active:false}}, {subscription:{}}), '');

// ---- Loadout and preview -------------------------------------------------
assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.equippedLoadout(snapshot, 'bot-1'))),
  {bot_skin:'arena_set_003_ember', weapon_skin:'standard', attachment:'none', trail:'standard'},
  'the current loadout is what the server saved, minus anything the catalog has since retired',
);
assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.equippedLoadout(snapshot, 'bot-missing'))),
  {bot_skin:'standard', weapon_skin:'standard', attachment:'none', trail:'standard'},
  'a loadout for a bot that is not linked renders as nothing',
);
const staged = cosmetics.previewModel(snapshot, 'bot-1', {weapon_skin:'weapon-ember', trail:'trail-comet', attachment:'weird-asset', bot_skin:'trail-comet'});
assert.deepEqual(JSON.parse(JSON.stringify(staged.previewLoadout)),
  {bot_skin:'arena_set_003_ember', weapon_skin:'arena_set_003_ember', attachment:'none', trail:'comet_tail'},
  'staging resolves items against the catalog: wrong slot and unsafe asset keys are dropped');
assert.deepEqual(JSON.parse(JSON.stringify(staged.stagedBySlot)), {weapon_skin:'weapon-ember', trail:'trail-comet'});
assert.equal(staged.hasStaged, true);
assert.equal(staged.isDirty, true);
assert.equal(staged.slots.weapon_skin.stagedItem.name, 'Ember Edge');
assert.equal(staged.slots.weapon_skin.unlocked, true);
assert.equal(staged.slots.weapon_skin.canEquip, true, 'subscribed, active key: equip is offered');
assert.equal(staged.slots.bot_skin.currentItem.id, 'skin-neon-grid');
assert.equal(cosmetics.previewModel(snapshot, 'bot-1', {trail:'trail-retired'}).slots.trail.stagedItem, null,
  'a retired item cannot be staged');
assert.equal(cosmetics.previewModel(snapshot, 'bot-2', {weapon_skin:'weapon-ember'}).slots.weapon_skin.canEquip, false,
  'an inactive bot key can preview but not equip');
assert.equal(cosmetics.previewModel(unsubscribed, 'bot-1', {weapon_skin:'weapon-ember'}).slots.weapon_skin.unlocked, false,
  'without the subscription a paid item previews but is locked');
assert.equal(cosmetics.previewModel(unsubscribed, 'bot-1', {weapon_skin:'weapon-ember'}).slots.weapon_skin.canEquip, false);
assert.equal(cosmetics.previewModel(unsubscribed, 'bot-2', {bot_skin:'skin-free'}).slots.bot_skin.unlocked, true,
  'free items are open to everyone');
assert.equal(cosmetics.previewModel(snapshot, 'missing-bot', {trail:'trail-comet'}).bot, null);
assert.deepEqual(JSON.parse(JSON.stringify(cosmetics.previewModel(snapshot, 'missing-bot', {trail:'trail-comet'}).stagedBySlot)), {});

// ---- Equip intent --------------------------------------------------------
assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.equipIntent(snapshot, 'bot-1', 'weapon-ember'))),
  {ok:true, path:'/account/bots/bot-1/cosmetics', body:{slot:'weapon_skin', cosmetic_id:'weapon-ember'}, bot_id:'bot-1', slot:'weapon_skin'},
  'equip is one PUT naming the slot and the item; there is no assignment step',
);
assert.equal(cosmetics.equipIntent(snapshot, 'bot-1', 'skin-neon-grid').reason, 'already-equipped');
assert.equal(cosmetics.equipIntent(snapshot, 'bot-2', 'weapon-ember').reason, 'bot-key-inactive');
assert.equal(cosmetics.equipIntent(snapshot, 'bot-9', 'weapon-ember').reason, 'bot-not-linked');
assert.equal(cosmetics.equipIntent(snapshot, 'bot-1', 'nope').reason, 'item-not-found');
assert.equal(cosmetics.equipIntent(snapshot, 'bot-1', 'trail-retired').reason, 'item-inactive');
assert.equal(cosmetics.equipIntent({...rawSnapshot, account:{id:'acct-1', email_verified:false}}, 'bot-1', 'weapon-ember').reason, 'verified-account-required');
const locked = cosmetics.equipIntent(unsubscribed, 'bot-1', 'weapon-ember');
assert.equal(locked.reason, 'subscription-required');
assert.equal(locked.url, SUBSCRIBE_AT, 'a locked item says where the subscription is');
assert.equal(cosmetics.equipIntent(unsubscribed, 'bot-1', 'skin-free').ok, true, 'free items equip without a subscription');

// ---- Collection ----------------------------------------------------------
assert.deepEqual(Array.from(cosmetics.collectionItems(snapshot, {}), item => item.id),
  ['skin-free', 'skin-neon-grid', 'weapon-ember', 'trail-comet', 'weird-asset'],
  'the collection is the active catalog, in catalog order');
assert.deepEqual(Array.from(cosmetics.collectionItems(snapshot, {slot:'trail'}), item => item.id), ['trail-comet']);
assert.deepEqual(Array.from(cosmetics.collectionItems(snapshot, {query:'NEON  grid'}), item => item.id), ['skin-neon-grid']);
assert.deepEqual(Array.from(cosmetics.collectionItems(snapshot, {query:'epic'}), item => item.id), ['weapon-ember'], 'rarity is searchable');
assert.equal(cosmetics.COLLECTION_PAGE_SIZE, 24);

// ---- Keys ----------------------------------------------------------------
const keyCollection = cosmetics.normalizeKeyCollection({
  keys:[
    {id:'key-1',key_prefix:'arena_alpha',bot_id:'bot-1',bot_name:'Alpha',created_at:'2026-07-12T10:00:00Z',is_active:true},
    {id:'key-2',key_prefix:'arena_beta',bot_id:'bot-2',bot_name:'Beta',created_at:'2026-07-12T11:00:00Z',is_active:true},
  ],
  active_count:2,
  limit:5,
});
assert.equal(keyCollection.active_count, 2);
assert.equal(keyCollection.limit, 5);
assert.equal(keyCollection.keys[0].key_prefix, 'arena_alpha');
assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.keyCreateIntent('  New Bot  ', keyCollection))),
  {ok:true,path:'/account/keys',body:{bot_name:'New Bot'}},
);
assert.equal(cosmetics.keyCreateIntent('Sixth Bot', {...keyCollection,active_count:5}).reason, 'key-limit-reached');
assert.equal(cosmetics.keyCreateIntent('Pending Bot', null).reason, 'keys-unavailable');
assert.deepEqual(JSON.parse(JSON.stringify(cosmetics.keyRevokeIntent('key/1'))), {ok:true,path:'/account/keys/key%2F1'});

const managedKeysHTML = cosmetics.renderAccountKeys({
  keys:keyCollection,
  generatedKey:{api_key:'arena_one_time_secret',bot_id:'bot-new',key:{id:'key-new',key_prefix:'arena_one'}},
});
assert.match(managedKeysHTML, /2 of 5 active/);
assert.match(managedKeysHTML, /id="accountKeyForm"/);
assert.match(managedKeysHTML, /data-account-key-revoke="key-1"/);
assert.match(managedKeysHTML, /value="arena_one_time_secret"/, 'a newly issued key should be shown exactly once inside the authenticated Dashboard');
assert.match(managedKeysHTML, /data-account-key-clear/);
assert.match(managedKeysHTML, /stored with this verified account/);
assert.doesNotMatch(managedKeysHTML, /email account|purchase/i, 'API-key guidance must use account-neutral, subscription-era wording');
assert.match(cosmetics.renderAccountKeys({keys:{...keyCollection,active_count:5}}), /id="accountKeyCreate"[^>]*disabled/,
  'Dashboard must disable key generation at the five-active-key limit');
assert.match(cosmetics.renderAccountKeys({keys:null}), /id="accountKeyCreate"[^>]*disabled/,
  'Dashboard must not create a key before current usage is known');
assert.match(cosmetics.renderAccountKeys({keys:null,keysError:'key service offline'}), /id="accountKeyCreate"[^>]*disabled/,
  'Dashboard must keep generation fail-closed when the key list is unavailable');

// ---- Linked bots ---------------------------------------------------------
const linkedBotsHTML = cosmetics.renderLinkedBots(snapshot, {});
assert.match(linkedBotsHTML, /id="linkBotForm"/);
assert.match(linkedBotsHTML, /data-bot-unlink="bot-1"/);
assert.match(linkedBotsHTML, /Alpha/);
assert.match(linkedBotsHTML, /Key inactive/, 'an inactive key is labelled on its bot');
const postCutoverLinkedBotsHTML = cosmetics.renderLinkedBots(cosmetics.normalizeSnapshot({...rawSnapshot, account:postCutoverSession.account}), {});
assert.match(postCutoverLinkedBotsHTML, /subscription always stays with Arena Pilot/);
assert.doesNotMatch(postCutoverLinkedBotsHTML, /email/i, 'linked-bot guidance must not depend on an email');
assert.match(cosmetics.renderLinkedBots(cosmetics.normalizeSnapshot({account:snapshot.account, bots:[]}), {}), /No bots linked yet/);

// ---- The panel: subscribed -----------------------------------------------
const subscribedHTML = cosmetics.renderPanel(rawSnapshot, {catalog, selectedBotID:'bot-1', filter:{}});
assert.match(subscribedHTML, /Verified account/);
assert.match(subscribedHTML, /Account verified/);
assert.doesNotMatch(subscribedHTML, /verified email|email account|Email verified/i);
assert.match(subscribedHTML, /data-subscription-active="true"/);
assert.match(subscribedHTML, /Everything unlocked/);
assert.match(subscribedHTML, /Subscription active/);
assert.match(subscribedHTML, new RegExp(`href="${SUBSCRIBE_AT}"[^>]*data-subscription-link>Manage in your Angel account`));
assert.match(subscribedHTML, /data-subscription-refresh>Refresh subscription/, 'a subscriber can re-read the flag after changes in Accounts');
assert.match(subscribedHTML, /Last read from your Angel account/);
assert.match(subscribedHTML, /5 cosmetics, all unlocked/, 'retired items are not counted');
assert.match(subscribedHTML, /Neon &lt;Grid&gt;/, 'catalog names are escaped');
assert.match(subscribedHTML, /data-cosmetic-equip="weapon-ember"[^>]*>Equip on Alpha/, 'an unlocked item equips straight onto the selected bot');
assert.match(subscribedHTML, /data-cosmetic-preview="weapon-ember"/);
assert.match(subscribedHTML, /Equipped on Alpha/);
assert.doesNotMatch(subscribedHTML, /data-cosmetic-equip="skin-neon-grid"/, 'an item already worn by the selected bot has nothing to equip');
assert.doesNotMatch(subscribedHTML, /trail-retired/, 'retired items are not listed');
assert.doesNotMatch(subscribedHTML, /Included with an Arena subscription<\/span>/, 'nothing is locked for a subscriber');
assert.doesNotMatch(subscribedHTML, /data-license|data-pack-checkout|data-order-resume|data-subscription-checkout|data-subscription-portal|All Access|\$\d/,
  'no per-item commerce controls survive');
assert.match(subscribedHTML, /data-collection-query/);
assert.match(subscribedHTML, /data-collection-slot/);
assert.match(subscribedHTML, /data-cosmetic-collection/);
assert.match(subscribedHTML, /data-open-shop/);
const inactiveBotHTML = cosmetics.renderPanel(rawSnapshot, {catalog, selectedBotID:'bot-2'});
assert.match(inactiveBotHTML, /data-cosmetic-equip="weapon-ember" disabled>Bot key inactive/);
const busyHTML = cosmetics.renderPanel(rawSnapshot, {catalog, selectedBotID:'bot-1', busyCosmeticID:'weapon-ember'});
assert.match(busyHTML, /data-cosmetic-equip="weapon-ember" disabled>Saving\.\.\./);
assert.match(cosmetics.renderPanel(rawSnapshot, {catalog, entitlementsBusy:true}), /data-subscription-refresh disabled>Reading your account\.\.\./);
assert.match(cosmetics.renderPanel(rawSnapshot, {catalog, error:'server <down>'}), /Could not update cosmetics:<\/b> server &lt;down&gt;/);
assert.match(cosmetics.renderPanel(rawSnapshot, {catalog, notice:'Cosmetic equipped.'}), /Saved:<\/b> Cosmetic equipped\./);

// ---- The panel: not subscribed -------------------------------------------
const lockedHTML = cosmetics.renderPanel({...rawSnapshot, subscription:{active:false, url:SUBSCRIBE_AT}}, {catalog, selectedBotID:'bot-1'});
assert.match(lockedHTML, /data-subscription-active="false"/);
assert.match(lockedHTML, /<h2 id="subscription-title">Included with an Arena subscription<\/h2>/);
assert.match(lockedHTML, /Not subscribed/);
assert.match(lockedHTML, new RegExp(`href="${SUBSCRIBE_AT}"[^>]*data-subscription-link>Subscribe in your Angel account`),
  'the page links to where the subscription is sold');
assert.match(lockedHTML, /Not read from your Angel account yet/);
assert.match(lockedHTML, /1 of 5 unlocked/);
assert.match(lockedHTML, /cosmetic-card locked" data-cosmetic-id="weapon-ember"/);
assert.match(lockedHTML, /<span class="ownership-badge locked">Subscription<\/span>/);
assert.match(lockedHTML, /Included with an Arena subscription<\/span>/, 'a locked card says what unlocks it');
assert.doesNotMatch(lockedHTML, /data-cosmetic-equip="weapon-ember"/, 'a locked item offers no equip control');
assert.match(lockedHTML, /data-cosmetic-preview="weapon-ember"/, 'previewing stays free');
assert.match(lockedHTML, /<span class="ownership-badge">Free<\/span>/);
assert.match(lockedHTML, /data-cosmetic-equip="skin-free"[^>]*>Equip on Alpha/, 'free items equip without a subscription');
const noAddressHTML = cosmetics.renderPanel({...rawSnapshot, subscription:{active:false}}, {catalog:{...catalog, subscription_url:''}});
assert.match(noAddressHTML, /Ask the Arena operator where to subscribe/, 'no configured address is said, not hidden');
assert.doesNotMatch(noAddressHTML, /data-subscription-link/);
assert.match(cosmetics.renderPanel({...rawSnapshot, subscription:{active:false}}, {catalog}), new RegExp(`href="${SUBSCRIBE_AT}"`),
  'the catalog address is used when the snapshot carries none');

// ---- Filtering and paging ------------------------------------------------
const manyItems = Array.from({length: 60}, (_, index) => ({
  id:`item-${String(index).padStart(3, '0')}`, name:`Item ${index}`, slot: index % 2 ? 'weapon_skin' : 'bot_skin',
  asset_key:`arena_set_${String(index).padStart(3, '0')}_x`, rarity: index % 3 ? 'common' : 'legendary',
}));
const manySnapshot = {...rawSnapshot, items: manyItems, loadouts:{}};
const pagedHTML = cosmetics.renderPanel(manySnapshot, {catalog, filter:{}});
assert.equal((pagedHTML.match(/data-cosmetic-id="/g) || []).length, 24, 'the collection renders one bounded page at a time');
assert.match(pagedHTML, /Showing 24 of 60/);
assert.match(pagedHTML, /data-collection-more>Show 24 more/);
const secondPageHTML = cosmetics.renderPanel(manySnapshot, {catalog, filter:{visible:48}});
assert.equal((secondPageHTML.match(/data-cosmetic-id="/g) || []).length, 48);
assert.match(secondPageHTML, /data-collection-more>Show 12 more/);
const filteredHTML = cosmetics.renderPanel(manySnapshot, {catalog, filter:{query:'legendary', slot:'weapon_skin'}});
assert.match(filteredHTML, /Showing 10 of 10/);
assert.match(filteredHTML, /value="legendary"/, 'the typed query survives a redraw');
assert.match(filteredHTML, /<option value="weapon_skin" selected>/);
assert.doesNotMatch(filteredHTML, /data-collection-more/);
assert.match(cosmetics.renderPanel(manySnapshot, {catalog, filter:{query:'no such thing'}}), /No cosmetics match/);

// ---- The Dashboard runtime -----------------------------------------------
const dashboardHTML = readFileSync(new URL('../frontend/dashboard/index.html', import.meta.url), 'utf8')
  // The dashboard runtime and styles live in dashboard.js/dashboard.css;
  // these probes span all three, so read them as one source.
  // accounts-login.js joins them because the sign-in flow itself moved
  // there when it became a popup.
  + readFileSync(new URL('../frontend/dashboard/dashboard.js', import.meta.url), 'utf8')
  + readFileSync(new URL('../frontend/dashboard/dashboard.css', import.meta.url), 'utf8')
  + readFileSync(new URL('../frontend/js/accounts-login.js', import.meta.url), 'utf8');
assert.match(dashboardHTML, /dashboard\/login/, 'sign-in should use the customer dashboard login route');
assert.match(dashboardHTML, /id="accountSignInButton"[^>]*disabled/, 'sign-in stays disabled until session capability is known');
assert.match(dashboardHTML, /method:'POST'/, 'account sign-out should use a CSRF-protected POST');
assert.match(dashboardHTML, /data-account-retry/, 'an initial inventory failure should expose a retry action');
assert.match(dashboardHTML, /Could not check Angel Accounts sign-in\. Bot performance by API key is still available\./,
  'a failed session capability request should say so, not claim sign-in is unconfigured');
assert.doesNotMatch(dashboardHTML, /setup\.hidden = false;[\s\S]{0,200}not configured/);
assert.doesNotMatch(dashboardHTML, /embedded-checkout|stripe|arena:stripe-checkout|accountOrders|accountBusyLicenseID|accountCheckoutState|dash_plan|dash_pack/i,
  'the Dashboard runtime carries no checkout, order or licence machinery');
assert.doesNotMatch(dashboardHTML, /licenses|license_id|All Access|purchases|Refresh purchases/i, 'no per-item commerce copy survives in the Dashboard');
assert.match(dashboardHTML, /data-subscription-refresh/);
assert.match(dashboardHTML, /\[data-cosmetic-equip\]/);
assert.match(dashboardHTML, /\[data-cosmetic-preview\]/);
assert.match(dashboardHTML, /\[data-collection-more\]/);
assert.match(dashboardHTML, /\[data-collection-slot\]/);
assert.match(dashboardHTML, /\[data-collection-query\]/);
assert.match(dashboardHTML, /addEventListener\('input', handleAccountPanelInput\)/, 'typing into the search box must not redraw the input away');
assert.match(dashboardHTML, /accountCosmetics\.js\?v=20260902a|account-cosmetics\.js\?v=20260902a/);
assert.match(dashboardHTML, /dashboard\.js\?v=20260902a/);
assert.match(dashboardHTML, /dashboard\.css\?v=20260902a/);
for (const className of ['subscription-card', 'subscription-status', 'subscription-action', 'cosmetic-card', 'cosmetic-card-grid',
  'cosmetic-collection-filter', 'cosmetic-equip-hint', 'cosmetic-show-more', 'ownership-badge.locked']) {
  assert.match(dashboardHTML, new RegExp(`\\.${className.replace('.', '\\.')}\\b`), `${className} needs styling`);
}

/*
 * The equip handler sends exactly the intent's request and nothing else, and
 * a refused intent never reaches the network.
 */
const equipStart = dashboardHTML.indexOf('async function equipAccountCosmetic');
const equipEnd = dashboardHTML.indexOf('async function unlinkAccountBot', equipStart);
assert.ok(equipStart >= 0 && equipEnd > equipStart);
const equipSource = dashboardHTML.slice(equipStart, equipEnd);
const requests = [];
const renders = [];
const equipSandbox = {
  accountBusyCosmeticID:'',
  accountViewError:'',
  accountViewNotice:'',
  accountPreviewBotID:'bot-1',
  accountPreviewStagedBySlot:{weapon_skin:'weapon-ember', trail:'trail-comet'},
  accountSnapshot:snapshot,
  window:{ArenaAccountCosmetics:cosmetics},
  JSON,
  EQUIP_REFUSALS:{'subscription-required':'Included with an Arena subscription. Subscribe in your Angel account, then refresh your subscription here.'},
  renderAccountCosmetics: () => renders.push({busy:equipSandbox.accountBusyCosmeticID, error:equipSandbox.accountViewError}),
  refreshAccountCosmetics: async notice => { equipSandbox.accountViewNotice = notice; },
  accountRequest: async (path, options) => { requests.push({path, options}); return {}; },
};
vm.runInNewContext(equipSource.replace(/^const EQUIP_REFUSALS[\s\S]*?\};\n/m, ''), equipSandbox, {filename:'dashboard-equip.js'});
await equipSandbox.equipAccountCosmetic('weapon-ember');
assert.deepEqual(JSON.parse(JSON.stringify(requests)), [{path:'/account/bots/bot-1/cosmetics', options:{method:'PUT', body:JSON.stringify({slot:'weapon_skin', cosmetic_id:'weapon-ember'})}}]);
assert.equal(renders[0].busy, 'weapon-ember', 'the card shows Saving... while the request is out');
assert.deepEqual(JSON.parse(JSON.stringify(equipSandbox.accountPreviewStagedBySlot)), {trail:'trail-comet'}, 'the equipped slot leaves the staged preview; the rest stays');
assert.match(equipSandbox.accountViewNotice, /Cosmetic equipped/);
equipSandbox.accountSnapshot = unsubscribed;
await equipSandbox.equipAccountCosmetic('weapon-ember');
assert.equal(requests.length, 1, 'a locked item never reaches the network');
assert.match(equipSandbox.accountViewError, /Included with an Arena subscription/);

console.log('Dashboard cosmetics: one subscription unlocks everything, and its absence is said, not hidden');
