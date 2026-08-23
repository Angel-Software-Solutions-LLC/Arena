import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';

/**
 * Buying moves to the Angel account, and every surface that offers to sell
 * something moves with it.
 *
 * > "the purchases will be done in the accounts/support app instead"
 *
 * Arena has three places a person can start a purchase — the Dashboard's
 * cosmetics panel, the standalone Shop, and the embedded Stripe controller
 * that both of them share — and a fourth that is not a place at all: a browser
 * still running the bundle from before the switch. What is checked here is
 * that all four agree, because the failure that matters is one of them still
 * offering a checkout the server now refuses.
 *
 * The server side of the same contract is in
 * go-arena/internal/api/purchases_via_accounts_test.go: the endpoints answer
 * 409 and name the destination. These are the clients that have to read it.
 */

const read = (path) => readFileSync(new URL(`../${path}`, import.meta.url), 'utf8');
const HANDOFF = 'https://accounts.angel-serv.com/portal/items';

/* ------------------------------------------- the Dashboard cosmetics panel */

const cosmeticsSandbox = {};
vm.runInNewContext(read('frontend/dashboard/account-cosmetics.js'), cosmeticsSandbox, {
  filename: 'account-cosmetics.js',
});
const cosmetics = cosmeticsSandbox.ArenaAccountCosmetics;

const pack = {
  id: 'arena-set-003-ember-vanguard-pack',
  name: 'Ember Vanguard Set',
  category_id: 'sets',
  is_purchasable: true,
  price_cents: 199,
  currency: 'USD',
  items: [{id: 'skin-1', name: 'Ember Chassis', slot: 'bot_skin', asset_key: 'ember'}],
};
const sellingCatalog = {checkout_enabled: true, packs: [pack], subscription_offer: {enabled: true}};
const handoffCatalog = {
  // Exactly what the server sends after the switch: checkout off, destination
  // named. The two are separate fields precisely so this state is expressible.
  checkout_enabled: false,
  purchase_handoff_url: HANDOFF,
  packs: [pack],
  subscription_offer: {enabled: false},
};

assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.checkoutIntent(handoffCatalog, pack.id))),
  {ok: true, kind: 'handoff', url: HANDOFF},
  'buying a pack becomes a handoff rather than a request to Arena',
);
assert.equal(
  cosmetics.checkoutIntent(sellingCatalog, pack.id).kind,
  'checkout',
  'and while Arena still sells, nothing about that path changes',
);

// A pack that is not for sale is still not for sale. Sending somebody to
// another site to buy a thing this Arena has retired would be a worse failure
// than refusing, because the refusal would arrive after they had left.
assert.equal(
  cosmetics.checkoutIntent({...handoffCatalog, packs: [{...pack, is_purchasable: false}]}, pack.id).reason,
  'pack-not-purchasable',
);
assert.equal(cosmetics.checkoutIntent(handoffCatalog, 'no-such-pack').reason, 'pack-not-found');

// The destination is read as an address or not at all.
for (const hostile of ['javascript:alert(1)', '/portal/items', 'http://accounts.angel-serv.com/x', 'https://', 'https://x y']) {
  assert.equal(
    cosmetics.checkoutIntent({...handoffCatalog, purchase_handoff_url: hostile}, pack.id).ok,
    false,
    `a handoff of ${hostile} must not become a navigation`,
  );
}

// All Access moves too — both subscribing and managing an existing one.
assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.subscriptionIntent({enabled: true}, null, HANDOFF))),
  {ok: true, kind: 'handoff', url: HANDOFF},
);
/*
 * And the one thing that deliberately does not move.
 *
 * A subscription started before the switch is live money in Arena's own Stripe
 * account, and Arena's portal is the only place its holder can cancel it. The
 * repository already drew this line for a sales pause; a handoff is the same
 * kind of event. The server agrees — SubscriptionPortal is the one commerce
 * endpoint left ungated.
 */
assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.subscriptionIntent(
    {enabled: true}, {id: 's1', status: 'active', can_manage: true, has_access: true}, HANDOFF))),
  {ok: true, kind: 'portal', path: '/account/cosmetics/subscription/portal'},
  'an existing subscriber keeps the only place they can cancel',
);
const subscriptions = read('go-arena/internal/api/cosmetic_subscriptions.go');
assert.match(
  subscriptions,
  /func \(h \*CosmeticCommerceHandler\) SubscriptionPortal[\s\S]{0,1200}?Deliberately NOT gated on the handoff/,
  'and the server leaves that endpoint open, with the reason written down',
);
assert.match(
  subscriptions,
  /func \(h \*CosmeticCommerceHandler\) SubscriptionCheckout\(w http\.ResponseWriter, r \*http\.Request\) \{[\s\S]{0,300}?refuseWhenPurchasingMoved\(w\)/,
  'while starting a new one is refused',
);
assert.equal(
  cosmetics.subscriptionIntent({enabled: true}, {id: 's1', status: 'active', can_manage: true}).kind,
  'portal',
  'without a handoff the Stripe portal path is untouched',
);

/* ------------------------------------------------ what the panel renders */

const snapshot = cosmetics.normalizeSnapshot({
  account: {id: 'acct-1', email: 'owner@example.com', email_verified: true},
  bots: [], licenses: [],
});
const sellingHTML = cosmetics.renderPanel(snapshot, {catalog: sellingCatalog, pendingPackID: pack.id});
const handoffHTML = cosmetics.renderPanel(snapshot, {catalog: handoffCatalog, pendingPackID: pack.id});

assert.match(handoffHTML, /data-pack-checkout=/, 'the Buy control survives the move');
assert.match(handoffHTML, /Buy \$1\.99 in your Angel account/, 'and says where the buyer is going');
assert.doesNotMatch(sellingHTML, /in your Angel account/, 'while Arena sells, it does not');

/*
 * Arena reads entitlements once, at sign-in, and holds no credential to ask
 * again (see go-arena/internal/api/customer_entitlements.go). So a pack bought
 * a minute ago genuinely is not here yet, and the panel has to say so and
 * offer the one action that fixes it -- otherwise somebody concludes their
 * purchase vanished.
 */
assert.match(handoffHTML, /data-entitlements-refresh/, 'a signed-in panel offers to re-read the account');
assert.match(handoffHTML, /Refresh purchases/);
assert.match(handoffHTML, /Purchases are made and held in your Angel account/);
assert.doesNotMatch(sellingHTML, /data-entitlements-refresh/, 'and offers nothing of the kind before the move');
assert.match(
  cosmetics.renderPanel(snapshot, {catalog: handoffCatalog, entitlementsBusy: true}),
  /Reading your account\.\.\./,
  'the refresh reports itself while it is in flight',
);

// The order ledger stays, and stops claiming to be the whole story.
assert.match(handoffHTML, /Orders Arena took payment for before buying moved/);
assert.match(sellingHTML, /Arena's signed payment ledger/);

/* ----------------------------------------------------- the Dashboard shell */

const dashboard = read('frontend/dashboard/dashboard.js');
assert.match(
  dashboard,
  /if \(intent\.ok && intent\.kind === 'handoff'\) \{\s*\n\s*navigateAccount\(intent\.url\);/,
  'the pack Buy control leaves for the Angel account',
);
assert.match(
  dashboard,
  /subscriptionIntent\(\s*\n?\s*offer, accountSnapshot\?\.subscription, accountCatalog\?\.purchase_handoff_url\)/,
  'the All Access control is told about the handoff too',
);
assert.match(dashboard, /data-entitlements-refresh/, 'the refresh control is wired');
assert.match(
  dashboard,
  /async function refreshAccountEntitlements\(\)[\s\S]{0,900}signInWithAccounts\(/,
  'and a refresh is a sign-in, because that is the only way Arena gets a token',
);

/* --------------------------------------------------------------- the Shop */

const shopSandbox = {window: {location: {pathname: '/shop/'}}};
const shop = read('frontend/js/cosmetics-shop.js');
assert.match(shop, /state\.purchaseHandoff = purchaseHandoffURL\(data\.purchase_handoff_url\)/,
  'the Shop reads the destination from the same catalog field');
assert.match(
  shop,
  /elements\.purchase\.href = !saleReady[\s\S]{0,200}handoff \|\| dashboardPurchasePath/,
  'and the Buy link goes straight there rather than through the Dashboard first',
);
assert.match(shop, /enabled: raw\.enabled === true \|\| Boolean\(handoff\)/,
  'the All Access card is offered again, pointing at the Angel account');
assert.match(shop, /elements\.subscriptionAction\.href = handoff \|\| subscriptionDashboardPath/);
void shopSandbox;

/* ------------------------------------------- the shared Stripe controller */

const embedded = read('frontend/js/embedded-checkout.js');
assert.match(
  embedded,
  /if \(data\?\.mode === 'handoff'\)/,
  'the controller recognises handoff mode',
);
// Ordering, not proximity: `enabled` is deliberately false in handoff mode, so
// a controller that tested it first would report "checkout is off" to a
// browser that could have been sent somewhere useful.
assert.ok(
  embedded.indexOf("data?.mode === 'handoff'") < embedded.indexOf('!data?.enabled'),
  'handoff must be recognised before the enabled test, not after it',
);
assert.match(
  embedded,
  /const config = await configReady;\s*\n\s*if \(config\.handoff\) \{/,
  'and before Stripe’s script is waited on, because there is no form to fill in',
);
assert.match(embedded, /function handoffURL\(value\)/, 'the destination is validated here too');
assert.match(embedded, /Purchases have moved, but this Arena was not told where to/,
  'handoff mode with no destination is reported as the misconfiguration it is');

/* --------------------------- and the client that has not been reloaded yet */

/*
 * The whole reason `enabled` and `purchase_handoff_url` are separate fields.
 *
 * A browser still running yesterday's bundle knows only `enabled`. It reads
 * false, shows "checkout is not available", and stops -- which is exactly
 * right, because the endpoint behind it now answers 409. If the two facts had
 * been collapsed into one truthy flag, that same browser would have opened a
 * Stripe session against an endpoint that refuses.
 */
const server = read('go-arena/internal/api/cosmetics.go');
assert.match(server, /if handoff != "" \{\s*\n\s*checkoutEnabled = false\s*\n\s*\}/,
  'the server turns its own checkout flag off, so a stale client stands down');
assert.match(server, /body\["purchase_handoff_url"\] = handoff/,
  'and publishes the destination as its own separate fact');

console.log('purchases via accounts: ok');
