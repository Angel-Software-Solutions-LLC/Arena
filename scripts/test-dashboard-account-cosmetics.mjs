import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';

const source = readFileSync(new URL('../frontend/dashboard/account-cosmetics.js', import.meta.url), 'utf8');
const sandbox = {};
vm.runInNewContext(source, sandbox, {filename: 'account-cosmetics.js'});
const cosmetics = sandbox.ArenaAccountCosmetics;
const recentPurchaseFailures = [];

function recentPurchaseCheck(name, check) {
  try {
    check();
  } catch (error) {
    recentPurchaseFailures.push(`${name}: ${error.message}`);
  }
}

assert.ok(cosmetics, 'dashboard cosmetics helpers should attach to globalThis');
assert.equal(cosmetics.normalizeSession({authenticated:false, account:{id:'acct-1',email:'owner@example.com',email_verified:true}}).authenticated, false, 'an explicit logged-out response must never be inferred as authenticated');
const verifiedSession = cosmetics.normalizeSession({authenticated:true, login_enabled:true, login_url:'/api/v1/dashboard/login', account:{id:'acct-1',email:'owner@example.com',email_verified_at:'2026-07-10T12:00:00Z'}});
assert.equal(verifiedSession.account.email_verified, true, 'a server verification timestamp should authorize the account UX');
assert.equal(verifiedSession.login_enabled, true);
assert.equal(verifiedSession.login_url, '/api/v1/dashboard/login');
// Arena's own email sign-in is retired; the server reports it off and the
// session shape no longer carries its endpoints. What is left to normalise is
// whether Angel Accounts is reachable, which is now the only question.
const signedOut = cosmetics.normalizeSession({authenticated:false,login_enabled:true,email_login_enabled:false,oidc_login_enabled:true});
assert.equal(signedOut.oidc_login_enabled, true);
assert.equal(signedOut.email_login_enabled, false);
assert.equal(signedOut.email_start_url, undefined, 'the retired endpoints are gone from the session shape');
assert.equal(signedOut.email_verify_url, undefined);
const postCutoverSession = {
  authenticated: true,
  account: {
    id: 'acct-post-cutover',
    email: '',
    email_verified: true,
    display_name: 'Arena Pilot',
  },
};
const normalizedPostCutoverSession = cosmetics.normalizeSession(postCutoverSession);
assert.equal(normalizedPostCutoverSession.account.id, 'acct-post-cutover');
assert.equal(normalizedPostCutoverSession.account.email_verified, true);
assert.equal(normalizedPostCutoverSession.account.email, '');
assert.equal(normalizedPostCutoverSession.account.name, 'Arena Pilot');
assert.equal(cosmetics.hasVerifiedAccount(postCutoverSession), true,
  'a signed-in verified Angel account with no email must remain eligible');
assert.equal(cosmetics.hasVerifiedAccount({...postCutoverSession, authenticated:false}), false,
  'an explicit sign-out must not be eligible');
assert.equal(cosmetics.hasVerifiedAccount({account:{
  id:'acct-legacy-email', email:'legacy@example.com', email_verified:true,
}}), false,
  'a verified legacy email account without explicit authentication must not be eligible');
assert.equal(cosmetics.hasVerifiedAccount({...postCutoverSession, account:{...postCutoverSession.account, id:''}}), false,
  'an account ID is required for eligibility');
assert.equal(cosmetics.hasVerifiedAccount({...postCutoverSession, account:{...postCutoverSession.account, email_verified:false}}), false,
  'an unverified account must not be eligible');
assert.equal(cosmetics.accountLabel(postCutoverSession.account), 'Arena Pilot');
const whitespaceDisplayName = {display_name:'   ', name:'Legacy Name', email:'legacy@example.com'};
assert.equal(cosmetics.normalizeSession({authenticated:true, account:whitespaceDisplayName}).account.name, 'Legacy Name',
  'a whitespace-only display name must fall back to a legacy name during normalization');
assert.equal(cosmetics.accountLabel(whitespaceDisplayName), 'Legacy Name',
  'a whitespace-only display name must fall back to a legacy name for account labels');
assert.equal(cosmetics.normalizeSession({authenticated:true, account:{display_name:'Preferred Name', name:'Legacy Name'}}).account.name, 'Preferred Name',
  'a real display name must still win over the legacy name during normalization');
assert.equal(cosmetics.accountLabel({display_name:'Preferred Name', name:'Legacy Name'}), 'Preferred Name',
  'a real display name must still win over the legacy name for account labels');
assert.equal(cosmetics.accountLabel({email:' Legacy@Example.COM '}), 'legacy@example.com');
assert.equal(cosmetics.accountLabel({}), 'Angel account');
assert.equal(cosmetics.accountRoute('session'), '/account/session');
assert.equal(cosmetics.accountRoute('checkout'), '/account/cosmetics/checkout');
assert.equal(cosmetics.accountRoute('subscriptionCheckout'), '/account/cosmetics/subscription/checkout');
assert.equal(cosmetics.accountRoute('subscriptionPortal'), '/account/cosmetics/subscription/portal');
assert.equal(cosmetics.accountRoute('keys'), '/account/keys');
assert.equal(cosmetics.accountRoute('key', 'key/a'), '/account/keys/key%2Fa');
recentPurchaseCheck('orders route', () => {
  assert.equal(cosmetics.accountRoute('orders'), '/account/cosmetics/orders');
  assert.equal(cosmetics.accountRoute('orderCheckout', 'order/a'), '/account/cosmetics/orders/order%2Fa/checkout');
});
assert.equal(cosmetics.accountRoute('assignment', 'license/a'), '/account/cosmetic-licenses/license%2Fa/assignment');
assert.equal(cosmetics.accountRoute('equip', 'bot/a'), '/account/bots/bot%2Fa/cosmetics');
assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.requestHeaders('PUT', 'csrf-value', true))),
  {Accept:'application/json','Content-Type':'application/json','X-CSRF-Token':'csrf-value'},
);
assert.equal(cosmetics.requestHeaders('GET', 'csrf-value', false)['X-CSRF-Token'], undefined, 'CSRF token should only be sent on mutations');

const snapshot = cosmetics.normalizeSnapshot({
  account: {id: 'acct-1', email: 'Owner@Example.COM ', email_verified: true},
  bots: [
    {id: 'bot-1', name: 'Alpha', key_prefix: 'arena_alpha', avatar_color: '#123456', default_weapon: 'bow'},
    {id: 'bot-2', name: 'Beta', key_prefix: 'arena_beta', avatar_color: '#abcdef', default_weapon: 'staff'},
  ],
  licenses: [
    {
      id: 'license-1',
      cosmetic_id: 'skin-neon-grid',
      item: {id: 'skin-neon-grid', name: 'Neon <Grid>', slot: 'bot_skin', rarity: 'rare'},
      assigned_bot_id: 'bot-1',
      equipped: true,
    },
    {
      id: 'license-2',
      cosmetic_id: 'skin-neon-grid',
      item: {id: 'skin-neon-grid', name: 'Neon <Grid>', slot: 'bot_skin', rarity: 'rare'},
      assigned_bot_id: 'bot-2',
    },
  ],
});

assert.equal(snapshot.account.email, 'owner@example.com');
assert.equal(snapshot.licenses.length, 2, 'multiple purchased copies must remain separate licenses');
assert.equal(snapshot.bots[0].avatar_color, '#123456', 'linked bots must retain the server-owned preview color');
assert.equal(snapshot.bots[0].default_weapon, 'bow', 'linked bots must retain the server-owned preview weapon');
assert.equal(cosmetics.slotLabel('trail'), 'Trails');

const postCutoverSnapshot = cosmetics.normalizeSnapshot({
  account: postCutoverSession.account,
  bots: [{id:'bot-post-cutover',name:'Post-cutover Bot',key_is_active:true}],
  licenses: [{
    id:'license-post-cutover', status:'active',
    item:{id:'skin-post-cutover',name:'Post-cutover Skin',slot:'bot_skin'},
  }],
});
assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.assignmentIntent(postCutoverSnapshot, 'license-post-cutover', 'bot-post-cutover'))),
  {ok:true,kind:'assign',license_id:'license-post-cutover',bot_id:'bot-post-cutover',previous_bot_id:null},
  'a verified account without an email may assign a real active license to an active linked bot',
);

const previewSnapshot = cosmetics.normalizeSnapshot({
  account: {id:'acct-preview',email:'preview@example.com',email_verified:true},
  bots: [
    {id:'bot-preview',name:'Preview Bot',avatar_color:'#22ccff',default_weapon:'spear',key_is_active:true},
    {id:'bot-other',name:'Other Bot',avatar_color:'#ff8844',default_weapon:'staff',key_is_active:false},
  ],
  licenses: [
    {
      id:'license-current-skin',status:'active',assigned_bot_id:'bot-preview',equipped:true,
      item:{id:'skin-current',name:'Current Skin',slot:'bot_skin',asset_key:'arena_set_001_current',is_active:true},
    },
    {
      id:'license-current-weapon',status:'active',assigned_bot_id:'bot-preview',equipped:true,
      item:{id:'weapon-current',name:'Current Weapon',slot:'weapon_skin',asset_key:'arena_set_002_weapon',is_active:true},
    },
    {
      id:'license-other-skin',status:'active',assigned_bot_id:'bot-other',equipped:false,
      item:{id:'skin-other',name:'Other Skin',slot:'bot_skin',asset_key:'arena_set_003_other',is_active:true},
    },
    {
      id:'license-preview-attachment',status:'active',equipped:false,
      item:{id:'attachment-preview',name:'Preview Attachment',slot:'attachment',asset_key:'arena_set_004_attachment',is_active:true},
    },
    {
      id:'license-ready-trail',status:'active',assigned_bot_id:'bot-preview',equipped:false,
      item:{id:'trail-ready',name:'Ready Trail',slot:'trail',asset_key:'neon_rain',is_active:true},
    },
    {
      id:'license-other-trail',status:'active',assigned_bot_id:'bot-other',equipped:true,
      item:{id:'trail-other',name:'Other Trail',slot:'trail',asset_key:'ember_sparks',is_active:true},
    },
    {
      id:'license-refunded',status:'refunded',assigned_bot_id:'bot-preview',equipped:true,
      item:{id:'trail-refunded',name:'Refunded Trail',slot:'trail',asset_key:'void_rift',is_active:true},
    },
    {
      id:'license-disabled-item',status:'active',assigned_bot_id:'bot-preview',equipped:true,
      item:{id:'attachment-disabled',name:'Disabled Attachment',slot:'attachment',asset_key:'arena_set_005_disabled',is_active:false},
    },
    {
      id:'license-long-asset',status:'active',equipped:false,
      item:{id:'skin-long',name:'Invalid Asset',slot:'bot_skin',asset_key:'x'.repeat(97),is_active:true},
    },
  ],
});

assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.equippedLoadout(previewSnapshot, 'bot-preview'))),
  {
    bot_skin:'arena_set_001_current',
    weapon_skin:'arena_set_002_weapon',
    attachment:'none',
    trail:'standard',
  },
  'current preview loadout must use only active equipped licenses assigned to that linked bot',
);
assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.equippedLoadout(previewSnapshot, 'bot-other'))),
  {bot_skin:'standard',weapon_skin:'standard',attachment:'none',trail:'ember_sparks'},
  'equipped loadout must stay scoped to one linked bot even when its key is inactive',
);

const stagedPreview = cosmetics.previewModel(previewSnapshot, 'bot-preview', {
  bot_skin:'license-other-skin',
  attachment:'license-preview-attachment',
  weapon_skin:'license-refunded',
  trail:'license-preview-attachment',
});
assert.equal(stagedPreview.bot.id, 'bot-preview');
assert.deepEqual(JSON.parse(JSON.stringify(stagedPreview.currentLoadout)), {
  bot_skin:'arena_set_001_current',weapon_skin:'arena_set_002_weapon',attachment:'none',trail:'standard',
});
assert.deepEqual(JSON.parse(JSON.stringify(stagedPreview.previewLoadout)), {
  bot_skin:'arena_set_003_other',
  weapon_skin:'arena_set_002_weapon',
  attachment:'arena_set_004_attachment',
  trail:'standard',
});
assert.deepEqual(JSON.parse(JSON.stringify(stagedPreview.stagedBySlot)), {
  bot_skin:'license-other-skin',
  attachment:'license-preview-attachment',
}, 'staging must accept only active owned license IDs whose item slot matches the requested slot');
assert.equal(stagedPreview.hasStaged, true);
assert.equal(stagedPreview.isDirty, true);
assert.equal(stagedPreview.slots.bot_skin.canEquip, false,
  'previewing a license assigned to another bot must not imply permission to equip it');
assert.equal(stagedPreview.slots.attachment.canEquip, false,
  'an unassigned preview license must remain a visual-only staged choice');
assert.equal(
  cosmetics.previewModel(previewSnapshot, 'bot-preview', {trail:'license-ready-trail'}).slots.trail.canEquip,
  true,
  'an active preview license assigned to the selected active bot may expose the existing explicit equip path',
);

const arbitraryAssetPreview = cosmetics.previewModel(previewSnapshot, 'bot-preview', {
  bot_skin:'arena_set_999_not_a_license',
  attachment:'license-disabled-item',
  trail:'license-refunded',
});
assert.deepEqual(JSON.parse(JSON.stringify(arbitraryAssetPreview.stagedBySlot)), {},
  'preview state must reject arbitrary asset keys, inactive items, and inactive licenses');
assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.previewModel(previewSnapshot, 'missing-bot', {
    bot_skin:'license-other-skin',
  }).stagedBySlot)),
  {},
  'preview state must reject staging when the selected bot is not linked to the account',
);
assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.previewModel(previewSnapshot, 'bot-preview', {
    bot_skin:'license-long-asset',
  }).stagedBySlot)),
  {},
  'preview state must reject asset keys beyond the renderer contract',
);

const previewHTML = cosmetics.renderPanel(previewSnapshot, {});
assert.match(previewHTML, /data-license-preview="license-current-skin"/,
  'active owned cosmetics must expose a preview action');
assert.match(previewHTML, /data-license-preview="license-other-skin"/,
  'a cosmetic assigned to another linked bot may still be previewed without moving it');
assert.doesNotMatch(previewHTML, /data-license-preview="license-refunded"/,
  'inactive licenses must not expose a preview action');
assert.doesNotMatch(previewHTML, /data-license-preview="license-disabled-item"/,
  'inactive catalog items must not expose a preview action');

const subscriptionOffer = cosmetics.normalizeSubscriptionOffer({
  enabled:true,
  price_cents:1999,
  currency:'USD',
  interval:'month',
  includes_future_sets:true,
  max_api_keys:5,
});
assert.deepEqual(JSON.parse(JSON.stringify(subscriptionOffer)), {
  enabled:true,
  price_cents:1999,
  currency:'USD',
  interval:'month',
  includes_future_sets:true,
  max_api_keys:5,
});
const managedSubscription = cosmetics.normalizeSubscription({
  id:'sub-1',
  status:'active',
  has_access:false,
  cancel_at_period_end:true,
  current_period_end:'2026-08-12T12:00:00Z',
  can_manage:true,
  checkout_presentation:'hosted',
  price_cents:1999,
  currency:'USD',
  interval:'month',
  includes_future_sets:true,
  max_api_keys:5,
});
assert.equal(managedSubscription.has_access, false, 'UI must use the server-authoritative has_access flag instead of inferring access from status');
assert.equal(managedSubscription.cancel_at_period_end, true);
assert.equal(managedSubscription.checkout_presentation, 'hosted');

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
assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.keyRevokeIntent('key/1'))),
  {ok:true,path:'/account/keys/key%2F1'},
);
assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.subscriptionIntent(subscriptionOffer, null))),
  {ok:true,kind:'checkout',path:'/account/cosmetics/subscription/checkout'},
);
assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.subscriptionIntent(subscriptionOffer, {...managedSubscription,can_manage:true}))),
  {ok:true,kind:'portal',path:'/account/cosmetics/subscription/portal'},
);
for (const status of ['created', 'checkout_pending', 'canceled', 'expired']) {
  assert.deepEqual(
    JSON.parse(JSON.stringify(cosmetics.subscriptionIntent(subscriptionOffer, {...managedSubscription,status,can_manage:false}))),
    {ok:true,kind:'checkout',path:'/account/cosmetics/subscription/checkout'},
    `${status} subscriptions must be resumable or replaceable through checkout`,
  );
}
assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.subscriptionIntent(subscriptionOffer, {...managedSubscription,status:'checkout_pending',can_manage:true}))),
  {ok:true,kind:'portal',path:'/account/cosmetics/subscription/portal'},
  'a completed Checkout with a known Stripe customer must expose cancellation while lifecycle webhooks catch up',
);
assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.subscriptionIntent(subscriptionOffer, {...managedSubscription,status:'billing_mismatch',can_manage:true}))),
  {ok:true,kind:'portal',path:'/account/cosmetics/subscription/portal'},
  'billing mismatches must stay manageable while access remains revoked',
);
assert.equal(
  cosmetics.subscriptionIntent(subscriptionOffer, {...managedSubscription,status:'active',can_manage:false}).reason,
  'subscription-unmanageable',
  'an active subscription without provider management must never fall through into duplicate checkout',
);

assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.assignmentIntent(snapshot, 'license-1', 'bot-2'))),
  {ok: true, kind: 'move', license_id: 'license-1', bot_id: 'bot-2', previous_bot_id: 'bot-1'},
);
assert.equal(cosmetics.assignmentIntent(snapshot, 'license-1', 'bot-1').reason, 'already-assigned');
assert.equal(cosmetics.assignmentIntent(snapshot, 'license-2', 'bot-missing').reason, 'bot-not-linked');

const inactiveState = cosmetics.normalizeSnapshot({
  account: {id:'acct-1',email:'owner@example.com',email_verified:true},
  bots: [{id:'bot-inactive',name:'Dormant',key_is_active:false}],
  licenses: [{id:'license-refunded',status:'refunded',item:{id:'skin-neon-grid',name:'Neon Grid',slot:'bot_skin'}}],
});
assert.equal(cosmetics.assignmentIntent(inactiveState, 'license-refunded', 'bot-inactive').reason, 'license-inactive');
inactiveState.licenses[0].status = 'active';
assert.equal(cosmetics.assignmentIntent(inactiveState, 'license-refunded', 'bot-inactive').reason, 'bot-key-inactive');
const inactiveHTML = cosmetics.renderPanel({...inactiveState, licenses:[{...inactiveState.licenses[0],status:'refunded'}]}, {});
assert.match(inactiveHTML, /Refunded/);
assert.doesNotMatch(inactiveHTML, /data-license-equip="license-refunded"/);
assert.match(cosmetics.renderLinkedBots(inactiveState, {}), /Key inactive/);
const inactiveBotHTML = cosmetics.renderPanel({
  ...inactiveState,
  licenses:[{...inactiveState.licenses[0],status:'active',assigned_bot_id:'bot-inactive',equipped:false}],
}, {});
assert.match(inactiveBotHTML, /data-license-equip="license-refunded" disabled>Bot key inactive/);

const catalog = {
  checkout_enabled: true,
  subscription_offer: subscriptionOffer,
  categories: [{id:'sets',name:'Sets'}, {id:'trails',name:'Trails'}],
  packs: [...Array.from({length:100}, (_, index) => {
    const number = String(index + 1).padStart(3, '0');
    const assetKey = `arena_set_${number}_signal_${number}`;
    return {
      id:`set-${number}-pack`, name:`Signal Set ${number}`, description:`Set ${number}`,
      category_id:'sets', is_purchasable:true, price_cents:199, currency:'USD',
      items:['bot_skin','weapon_skin','attachment'].map(slot => ({id:`${slot}-${number}`,slot,asset_key:assetKey,name:`${slot} ${number}`})),
    };
  }), {
    id:'trail-ember-sparks-pack', name:'Ember Sparks', description:'Hot cinders in a fire-red wake.',
    category_id:'trails', is_purchasable:true, price_cents:99, currency:'USD',
    items:[{id:'trail-ember-sparks',slot:'trail',asset_key:'ember_sparks',name:'Ember Sparks'}],
  }],
};
const shopHTML = cosmetics.renderPanel(snapshot, {catalog});
assert.doesNotMatch(shopHTML, /data-shop-pack=/,
  'the Dashboard must not render a browsable catalog -- browsing lives only in the Shop');
assert.doesNotMatch(shopHTML, /data-pack-checkout=/, 'no pending purchase means no checkout button');
assert.match(shopHTML, /data-open-shop/, 'the Dashboard must link out to the Shop for browsing and buying');
assert.match(shopHTML, /All Access/);
assert.match(shopHTML, /\$19\.99[\s\S]*month/);
assert.match(shopHTML, /every current and future cosmetic set, full-body skin, and trail/i);
assert.match(shopHTML, /up to 5 active API keys/i);
assert.match(shopHTML, /subscription cosmetics are removed/i);
assert.match(shopHTML, /data-subscription-checkout/, 'accounts without a managed subscription should be able to start All Access checkout');

const pendingSetHTML = cosmetics.renderPanel(snapshot, {catalog, pendingPackID:'set-001-pack'});
assert.equal((pendingSetHTML.match(/data-shop-pack=/g) || []).length, 1,
  'a deep-linked purchase must show exactly the one pack the customer picked in the Shop, not a browsable grid');
assert.match(pendingSetHTML, /data-pack-checkout="set-001-pack"/, 'enabled sale-ready packs should expose checkout');
assert.match(pendingSetHTML, /\$1\.99/, 'every one-time cosmetic set should display the $1.99 catalog price');
const pendingTrailHTML = cosmetics.renderPanel(snapshot, {catalog, pendingPackID:'trail-ember-sparks-pack'});
assert.match(pendingTrailHTML, /Individual trail/);
assert.match(pendingTrailHTML, /\$0\.99/);
assert.match(pendingTrailHTML, /data-pack-checkout="trail-ember-sparks-pack"/);
const missingPendingHTML = cosmetics.renderPanel(snapshot, {catalog, pendingPackID:'missing-pack'});
assert.match(missingPendingHTML, /no longer available in the Shop/i);
const disabledPendingHTML = cosmetics.renderPanel(snapshot, {catalog:{...catalog,checkout_enabled:false}, pendingPackID:'set-001-pack'});
assert.doesNotMatch(disabledPendingHTML, /data-pack-checkout=/, 'checkout-disabled catalogs must not render purchase buttons');
assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.checkoutIntent(catalog, 'set-001-pack'))),
  {ok:true,kind:'checkout',path:'/account/cosmetics/checkout',body:{pack_id:'set-001-pack',quantity:1}},
);
assert.deepEqual(
  JSON.parse(JSON.stringify(cosmetics.checkoutIntent(catalog, 'trail-ember-sparks-pack'))),
  {ok:true,kind:'checkout',path:'/account/cosmetics/checkout',body:{pack_id:'trail-ember-sparks-pack',quantity:1}},
);

const membershipSnapshot = cosmetics.normalizeSnapshot({
  account:{id:'acct-membership',email:'granted@example.com',email_verified:true},
  bots:[], licenses:[],
  membership:{id:'membership-1',status:'active',granted_at:'2026-07-01T00:00:00Z',expires_at:'2026-08-01T00:00:00Z'},
});
assert.equal(membershipSnapshot.membership?.id, 'membership-1');
const membershipHTML = cosmetics.renderPanel(membershipSnapshot, {catalog});
assert.match(membershipHTML, /Access active/, 'an active admin-granted membership must read as active access, not "Available"');
assert.doesNotMatch(membershipHTML, /class="all-access-status ">Available/, 'a granted membership must not still show the unsubscribed status');
assert.match(membershipHTML, /Granted by Arena staff/i);
const revokedMembershipSnapshot = cosmetics.normalizeSnapshot({
  account:{id:'acct-revoked',email:'revoked@example.com',email_verified:true},
  bots:[], licenses:[],
  membership:{id:'membership-2',status:'revoked',granted_at:'2026-07-01T00:00:00Z',expires_at:'2026-08-01T00:00:00Z'},
});
assert.equal(revokedMembershipSnapshot.membership, null, 'only an active membership should surface in the Dashboard UI');

const baseOrder = {
  id:'order-001',
  pack_id:'set-003-pack',
  pack_name:'Signal Set 003',
  quantity:2,
  expected_subtotal_cents:1000,
  amount_received_cents:900,
  amount_refunded_cents:100,
  currency:'USD',
  status:'refund_review',
  fulfilled_license_count:6,
  created_at:'2026-07-11T12:00:00Z',
};

const loadingOrdersHTML = cosmetics.renderPanel(snapshot, {catalog, orders:null, ordersError:''});
recentPurchaseCheck('loading state', () => {
  assert.match(loadingOrdersHTML, /Recent purchases/);
  assert.match(loadingOrdersHTML, /Loading recent purchases\.\.\./);
});

const failedOrdersHTML = cosmetics.renderPanel(snapshot, {
  catalog,
  orders:null,
  ordersError:'Ledger <offline>',
});
recentPurchaseCheck('independent error state', () => {
  assert.match(failedOrdersHTML, /Recent purchases unavailable/);
  assert.match(failedOrdersHTML, /Ledger &lt;offline&gt;/);
  assert.match(failedOrdersHTML, /data-open-shop/, 'order failure must leave the Shop link visible');
  assert.match(failedOrdersHTML, /data-license-id="license-1"/, 'order failure must leave owned inventory visible');
});

const emptyOrdersHTML = cosmetics.renderPanel(snapshot, {catalog, orders:[]});
recentPurchaseCheck('empty state', () => {
  assert.match(emptyOrdersHTML, /No purchases yet\./);
});

const hostileOrdersHTML = cosmetics.renderPanel(snapshot, {
  catalog,
  orders:[
    {
      ...baseOrder,
      id:'order-<img src=x onerror=alert(1)>',
      pack_name:'Launch <script>alert(1)</script> Set',
    },
    {
      ...baseOrder,
      id:'order-hostile-status',
      status:'<svg onload=alert(1)>',
    },
  ],
});
recentPurchaseCheck('hostile data and complete order facts', () => {
  assert.doesNotMatch(hostileOrdersHTML, /<img src=x onerror=alert\(1\)>/);
  assert.doesNotMatch(hostileOrdersHTML, /<script>alert\(1\)<\/script>/);
  assert.doesNotMatch(hostileOrdersHTML, /<svg onload=alert\(1\)>/);
  assert.match(hostileOrdersHTML, /order-&lt;img src=x onerror=alert\(1\)&gt;/);
  assert.match(hostileOrdersHTML, /Launch &lt;script&gt;alert\(1\)&lt;\/script&gt; Set/);
  assert.match(hostileOrdersHTML, /Quantity 2/);
  assert.match(hostileOrdersHTML, /Expected[\s\S]*\$10\.00/);
  assert.match(hostileOrdersHTML, /Received[\s\S]*\$9\.00/);
  assert.match(hostileOrdersHTML, /Refunded[\s\S]*\$1\.00/);
  assert.match(hostileOrdersHTML, /6 licenses fulfilled/);
  assert.match(hostileOrdersHTML, /<time[^>]*datetime="2026-07-11T12:00:00\.000Z"[^>]*>[^<]*2026[^<]*<\/time>/);
});

const statusCases = [
  ['created', 'Checkout pending'],
  ['checkout_pending', 'Checkout pending'],
  ['processing', 'Processing'],
  ['paid', 'Paid'],
  ['refund_review', 'Refund review'],
  ['refunded', 'Refunded'],
  ['disputed', 'Disputed'],
  ['expired', 'Expired'],
  ['payment_failed', 'Failed'],
  ['failed', 'Failed'],
];
const statusOrdersHTML = cosmetics.renderPanel(snapshot, {
  catalog,
  orders:statusCases.map(([status], index) => ({...baseOrder, id:`status-${index}`, status})),
});
for (const [status, label] of statusCases) {
  recentPurchaseCheck(`truthful ${status} status`, () => {
    assert.match(
      statusOrdersHTML,
      new RegExp(`data-order-status="${status}"[\\s\\S]*?<span class="cosmetic-purchase-status[^"]*">${label}<\\/span>`),
    );
  });
}

const resumableOrderHTML = cosmetics.renderPanel(snapshot, {
  catalog,
  orders:[{...baseOrder, id:'order-resume', status:'checkout_pending', checkout_session_id:'cs_resume', checkout_presentation:'embedded'}],
});
recentPurchaseCheck('attached pending order resume action', () => {
  assert.match(resumableOrderHTML, /data-order-resume="order-resume"[^>]*>Resume checkout<\/button>/);
});
const unattachedPendingOrderHTML = cosmetics.renderPanel(snapshot, {
  catalog,
  orders:[{...baseOrder, id:'order-not-attached', status:'checkout_pending', checkout_session_id:'', checkout_presentation:'embedded'}],
});
recentPurchaseCheck('unattached order cannot resume', () => {
  assert.doesNotMatch(unattachedPendingOrderHTML, /data-order-resume=/);
});
for (const status of ['created', 'payment_failed']) {
  const retryableOrderHTML = cosmetics.renderPanel(snapshot, {
    catalog,
    orders:[{...baseOrder, id:`order-retry-${status}`, status, checkout_session_id:'', checkout_presentation:'hosted'}],
  });
  recentPurchaseCheck(`${status} order exposes reserved checkout retry`, () => {
    assert.match(retryableOrderHTML, new RegExp(`data-order-resume="order-retry-${status}"[^>]*>Retry checkout<\\/button>`));
    assert.match(retryableOrderHTML, /same reserved Stripe checkout/);
  });
}
const unpinnedFailedOrderHTML = cosmetics.renderPanel(snapshot, {
  catalog,
  orders:[{...baseOrder, id:'order-unpinned', status:'payment_failed', checkout_session_id:'', checkout_presentation:''}],
});
recentPurchaseCheck('unattached failure without a persisted mode cannot retry', () => {
  assert.doesNotMatch(unpinnedFailedOrderHTML, /data-order-resume=/);
});
const paidOrderHTML = cosmetics.renderPanel(snapshot, {
  catalog,
  orders:[{...baseOrder, id:'order-paid', status:'paid', checkout_session_id:'cs_paid'}],
});
recentPurchaseCheck('terminal order cannot resume', () => {
  assert.doesNotMatch(paidOrderHTML, /data-order-resume=/);
});

const boundedOrdersHTML = cosmetics.renderPanel(snapshot, {
  catalog,
  orders:Array.from({length:25}, (_, index) => ({...baseOrder, id:`bounded-${index}`})),
});
recentPurchaseCheck('bounded history', () => {
  assert.equal((boundedOrdersHTML.match(/data-purchase-order=/g) || []).length, 20);
});

const unverified = structuredClone(snapshot);
unverified.account.email_verified = false;
assert.equal(cosmetics.assignmentIntent(unverified, 'license-2', 'bot-2').reason, 'verified-account-required');

const html = cosmetics.renderPanel(snapshot, {});
assert.match(html, /Purchases stay with this account even if a bot API key is rotated, revoked, or lost/);
assert.match(html, /assignable to one linked bot at a time/);
assert.match(html, /Equipped on/);
assert.match(html, /Assigned to <strong>Alpha \(arena_alpha\.\.\.\)<\/strong>/);
assert.match(html, /Assigned, not equipped/);
assert.match(html, /data-license-equip="license-2"/);
assert.match(html, /data-license-id="license-1"/);
assert.match(html, /data-license-id="license-2"/);
assert.match(html, /Move to bot/);
assert.match(html, /Alpha - arena_alpha\.\.\./, 'duplicate bot names must remain distinguishable by safe key prefix');
assert.match(html, /Remove from bot/);
assert.doesNotMatch(html, /data-bot-unlink=/, 'linked bots render on the Profile tab now, not inside the Cosmetics panel');
assert.doesNotMatch(html, /id="accountKeyForm"/, 'API keys render on the Profile tab now, not inside the Cosmetics panel');
assert.doesNotMatch(html, /Neon <Grid>/, 'cosmetic names must be escaped');
assert.match(html, /Neon &lt;Grid&gt;/);
assert.doesNotMatch(html, /value="arena_alpha"/, 'rendered account UI must not contain raw API keys');

const postCutoverHTML = cosmetics.renderPanel(postCutoverSnapshot, {});
assert.match(postCutoverHTML, /Arena Pilot/);
assert.match(postCutoverHTML, /Verified account/);
assert.match(postCutoverHTML, /Account verified/);
assert.doesNotMatch(postCutoverHTML, /verified email/i);
assert.doesNotMatch(postCutoverHTML, /email account/i);
assert.doesNotMatch(postCutoverHTML, /Email verified/);

const linkedBotsHTML = cosmetics.renderLinkedBots(snapshot, {});
assert.match(linkedBotsHTML, /id="linkBotForm"/);
assert.match(linkedBotsHTML, /data-bot-unlink="bot-1"/);
assert.match(linkedBotsHTML, /Alpha/);
assert.match(linkedBotsHTML, /Beta/);
const postCutoverLinkedBotsHTML = cosmetics.renderLinkedBots(postCutoverSnapshot, {});
assert.match(postCutoverLinkedBotsHTML, /purchases always stay with Arena Pilot/);
assert.doesNotMatch(postCutoverLinkedBotsHTML, /email/i,
  'linked-bot ownership guidance must not depend on an email');
recentPurchaseCheck('empty linked-bots state', () => {
  const noBotsSnapshot = cosmetics.normalizeSnapshot({account:snapshot.account, bots:[], licenses:[]});
  assert.match(cosmetics.renderLinkedBots(noBotsSnapshot, {}), /No bots linked yet/);
});

const managedHTML = cosmetics.renderPanel({
  ...snapshot,
  subscription:{
    id:'sub-active',status:'active',has_access:true,cancel_at_period_end:true,
    current_period_end:'2026-08-12T12:00:00Z',can_manage:true,price_cents:1999,currency:'USD',interval:'month',
    includes_future_sets:true,max_api_keys:5,
  },
  subscription_offer:subscriptionOffer,
}, {catalog});
assert.match(managedHTML, /All Access/);
assert.match(managedHTML, /Access active/);
assert.match(managedHTML, /data-subscription-portal/, 'an existing provider subscription should expose customer portal management');
assert.doesNotMatch(managedHTML, /data-subscription-checkout/, 'managed subscriptions must not offer a duplicate checkout');

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
assert.doesNotMatch(managedKeysHTML, /email account/i,
  'API-key guidance must use account-neutral wording');

const billingMismatchHTML = cosmetics.renderPanel({
  ...snapshot,
  subscription:{...managedSubscription,status:'billing_mismatch',has_access:false,can_manage:true},
  subscription_offer:subscriptionOffer,
}, {catalog,keys:keyCollection});
assert.match(billingMismatchHTML, /Billing needs attention/, 'a plan mismatch must clearly direct the customer to billing management');
assert.match(billingMismatchHTML, /data-subscription-portal/);

const pendingSubscriptionHTML = cosmetics.renderPanel({
  ...snapshot,
  subscription:{...managedSubscription,status:'checkout_pending',has_access:false,can_manage:false},
  subscription_offer:subscriptionOffer,
}, {catalog,keys:keyCollection});
assert.match(pendingSubscriptionHTML, /Checkout pending/, 'an resumable hosted session must not be described as ended access');
assert.match(pendingSubscriptionHTML, /data-subscription-checkout/);

const atLimitHTML = cosmetics.renderAccountKeys({keys:{...keyCollection,active_count:5}});
assert.match(atLimitHTML, /5 of 5 active/);
assert.match(atLimitHTML, /id="accountKeyCreate"[^>]*disabled/, 'Dashboard must disable key generation at the five-active-key limit');
const loadingKeysHTML = cosmetics.renderAccountKeys({keys:null});
assert.match(loadingKeysHTML, /id="accountKeyCreate"[^>]*disabled/, 'Dashboard must not create a key before current usage is known');
const failedKeysHTML = cosmetics.renderAccountKeys({keys:null,keysError:'key service offline'});
assert.match(failedKeysHTML, /id="accountKeyCreate"[^>]*disabled/, 'Dashboard must keep generation fail-closed when the key list is unavailable');

const dashboardHTML = readFileSync(new URL('../frontend/dashboard/index.html', import.meta.url), 'utf8')
  // The dashboard runtime and styles were extracted from inline blocks to
  // dashboard.js/dashboard.css; these probes span all three, so read them
  // as one source. accounts-login.js joins them because the sign-in flow
  // itself moved there when it became a popup -- the route below is stated
  // once, in the module that opens the window.
  + readFileSync(new URL('../frontend/dashboard/dashboard.js', import.meta.url), 'utf8')
  + readFileSync(new URL('../frontend/dashboard/dashboard.css', import.meta.url), 'utf8')
  + readFileSync(new URL('../frontend/js/accounts-login.js', import.meta.url), 'utf8');
assert.match(dashboardHTML, /dashboard\/login/, 'sign-in should use the customer dashboard login route');
assert.match(dashboardHTML, /id="accountSignInButton"[^>]*disabled/, 'sign-in stays disabled until session capability is known');
assert.match(dashboardHTML, /method:'POST'/, 'account sign-out should use a CSRF-protected POST');
assert.match(dashboardHTML, /data-account-retry/, 'an initial inventory failure should expose a retry action');
/*
 * The claim behind this has not changed, only how it is met. A session
 * document that could not be read used to relabel the button "Retry email
 * sign-in check"; with one sign-in method there is nothing to relabel, so the
 * button is simply offered enabled — pressing it either works or produces a
 * real error, and neither is the "not configured" message that would be a lie.
 */
assert.match(
  dashboardHTML,
  /Could not check Angel Accounts sign-in\. Bot performance by API key is still available\./,
  'a failed session capability request should say so, not claim sign-in is unconfigured',
);
assert.doesNotMatch(
  dashboardHTML,
  /setup\.hidden = false;[\s\S]{0,200}not configured/,
  'an unreadable session must not be reported as an unconfigured Arena',
);
assert.match(dashboardHTML, /\(tabName==='cosmetics' \|\| tabName==='profile'\) && hasVerifiedAccount\(\)\) refreshAccountCosmetics\(\)/, 'opening Cosmetics or Profile must fetch even when API-key login won the startup race -- Profile now needs the same linked-bots/keys data Cosmetics does');
assert.match(dashboardHTML, /sessionStorage\.setItem\('arena_keys'/, 'legacy bot-performance keys should be tab-scoped');
assert.doesNotMatch(dashboardHTML, /localStorage\.setItem\('arena_keys'/, 'bot bearer keys must not persist across browser sessions');
assert.match(dashboardHTML, /input\.value = '';/, 'the one-time link key should be cleared as soon as it is submitted');
const linkHandler = dashboardHTML.slice(
  dashboardHTML.indexOf('async function handleAccountPanelSubmit'),
  dashboardHTML.indexOf('async function assignAccountLicense'),
);
assert.doesNotMatch(linkHandler, /saveKey|localStorage/, 'linking a bot must not persist the proof key in dashboard storage');

recentPurchaseCheck('dashboard script cache version', () => {
  assert.match(dashboardHTML, /dashboard\.css\?v=20260823b/);
  assert.match(dashboardHTML, /account-cosmetics\.js\?v=20260823b/);
  assert.match(dashboardHTML, /dashboard\.js\?v=20260823b/);
  assert.match(dashboardHTML, /href="\.\.\/css\/embedded-checkout\.css\?v=20260713a"/);
  assert.match(dashboardHTML, /src="\.\.\/js\/embedded-checkout\.js\?v=20260823b"/);
});
recentPurchaseCheck('checkout return remains reconciling', () => {
  assert.match(dashboardHTML, /checkoutReturn === 'return'[\s\S]*?status:'reconciling'/);
});
recentPurchaseCheck('closing embedded checkout releases pending state', () => {
  assert.match(dashboardHTML, /function handleEmbeddedCheckoutAbort\(event\)/);
  assert.match(dashboardHTML, /arena:stripe-checkout:abort/);
});
recentPurchaseCheck('hosted checkout and billing portal escape the dashboard iframe', () => {
  const checkoutStart = dashboardHTML.indexOf('async function presentAccountCheckout');
  const checkoutEnd = dashboardHTML.indexOf('function abortPreparedAccountCheckout', checkoutStart);
  const subscriptionStart = dashboardHTML.indexOf('async function openAccountSubscription');
  const subscriptionEnd = dashboardHTML.indexOf('async function handleAccountPanelSubmit', subscriptionStart);
  const checkoutSource = dashboardHTML.slice(checkoutStart, checkoutEnd);
  const subscriptionSource = dashboardHTML.slice(subscriptionStart, subscriptionEnd);
  assert.match(checkoutSource, /navigateAccount\(checkoutURL\.href\)/);
  assert.doesNotMatch(checkoutSource, /window\.location\.assign/);
  assert.match(subscriptionSource, /navigateAccount\(destination\.href\)/);
  assert.doesNotMatch(subscriptionSource, /window\.location\.assign/);
  assert.match(dashboardHTML, /if \(window\.top !== window\.self\) window\.top\.location\.assign\(url\)/);
});
recentPurchaseCheck('pending order resume handler', () => {
  assert.match(dashboardHTML, /async function resumeCosmeticCheckout\(orderID\)/);
  assert.match(dashboardHTML, /accountRoute\('orderCheckout',\s*orderID\)/);
  assert.match(dashboardHTML, /closest\('\[data-order-resume\]'\)/);
});
recentPurchaseCheck('subscription checkout honors the server-persisted presentation', () => {
  const start = dashboardHTML.indexOf('async function openAccountSubscription');
  const end = dashboardHTML.indexOf('async function handleAccountPanelSubmit', start);
  assert.match(dashboardHTML.slice(start, end), /checkout_presentation/);
});
recentPurchaseCheck('long purchase data remains contained', () => {
  assert.match(dashboardHTML, /\.cosmetic-purchase-head>div\{[^}]*min-width:0/);
  assert.match(dashboardHTML, /\.cosmetic-purchase-head h3\{[^}]*overflow-wrap:anywhere/);
});

const refreshStart = dashboardHTML.indexOf('async function refreshAccountCosmetics');
const refreshEnd = dashboardHTML.indexOf('async function handleAccountPanelSubmit', refreshStart);
recentPurchaseCheck('refresh helper source', () => {
  assert.ok(refreshStart >= 0 && refreshEnd > refreshStart);
});

if (refreshStart >= 0 && refreshEnd > refreshStart) {
  const refreshCalls = [];
  const refreshRenders = [];
  let rejectOrders = null;
  const refreshSandbox = {
    accountRefreshSequence:0,
    accountSnapshot:null,
    accountCatalog:null,
    accountCatalogError:'',
    accountOrders:null,
    accountOrdersError:'',
    accountKeys:null,
    accountKeysError:'',
    accountSession:{account:{id:'acct-1',email:'owner@example.com',email_verified:true}},
    accountViewError:'',
    accountViewNotice:'',
    window:{
      ArenaAccountCosmetics:{
        accountRoute:name => ({cosmetics:'/account/cosmetics',orders:'/account/cosmetics/orders',keys:'/account/keys'})[name],
        normalizeSnapshot:value => value,
        normalizeCatalog:value => value,
        normalizeKeyCollection:value => value,
      },
    },
  };
  refreshSandbox.renderAccountCosmetics = () => {
    refreshRenders.push({
      hasInventory:Boolean(refreshSandbox.accountSnapshot),
      orders:refreshSandbox.accountOrders,
      ordersError:refreshSandbox.accountOrdersError,
    });
  };
  refreshSandbox.accountRequest = path => {
    refreshCalls.push(path);
    if (path === '/account/cosmetics/orders?limit=20') {
      return new Promise((_, reject) => {
        rejectOrders = reject;
      });
    }
    if (path === '/account/keys') return Promise.resolve(keyCollection);
    if (path === '/cosmetics/catalog') return Promise.resolve(catalog);
    return Promise.resolve({account:refreshSandbox.accountSession.account,bots:[],licenses:[]});
  };

  vm.runInNewContext(dashboardHTML.slice(refreshStart, refreshEnd), refreshSandbox, {filename:'dashboard-refresh.js'});
  const refreshPromise = refreshSandbox.refreshAccountCosmetics();
  await new Promise(resolve => setImmediate(resolve));
  recentPurchaseCheck('independent bounded fetch', () => {
    assert.ok(refreshCalls.includes('/account/cosmetics/orders?limit=20'));
    assert.ok(refreshCalls.includes('/account/keys'));
    assert.equal(refreshSandbox.accountSnapshot.account.id, 'acct-1');
    assert.equal(refreshSandbox.accountKeys.active_count, 2);
    assert.ok(refreshRenders.some(state => state.hasInventory && state.orders === null),
      'inventory must render while purchase history is still pending');
  });
  if (rejectOrders) rejectOrders(new Error('history offline'));
  await refreshPromise;
  recentPurchaseCheck('history failure leaves inventory and shop data usable', () => {
    assert.equal(refreshSandbox.accountSnapshot.account.id, 'acct-1');
    assert.equal(refreshSandbox.accountCatalog.checkout_enabled, true);
    assert.match(refreshSandbox.accountOrdersError, /history offline/);
    assert.equal(refreshSandbox.accountViewError, '');
  });
}

assert.deepEqual(recentPurchaseFailures, [], `recent purchase behavior failures:\n${recentPurchaseFailures.join('\n')}`);

console.log('account-owned dashboard cosmetics rendering and assignment rules pass');
