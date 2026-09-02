import assert from 'node:assert/strict';
import {existsSync, readFileSync} from 'node:fs';
import vm from 'node:vm';

/*
 * The one rule of Arena commerce, stated once against the whole front end.
 *
 * A signed-in customer whose Angel account holds an active Arena
 * subscription has every cosmetic unlocked for every linked bot. One without
 * sees "Included with an Arena subscription" and a link to where the
 * subscription is sold. There is no third state: no per-item licence, no
 * checkout, no order, nothing for Arena to sell.
 */

const read = path => readFileSync(new URL(path, import.meta.url), 'utf8');
const sandbox = {};
vm.runInNewContext(read('../frontend/dashboard/account-cosmetics.js'), sandbox, {filename: 'account-cosmetics.js'});
const cosmetics = sandbox.ArenaAccountCosmetics;

const SUBSCRIBE_AT = 'https://accounts.example/shop/arena';
const items = [
  {id:'free-skin', name:'Standard Plus', slot:'bot_skin', asset_key:'arena_set_001_starter', is_free:true},
  {id:'set-skin', name:'Ember Chassis', slot:'bot_skin', asset_key:'arena_set_003_ember', rarity:'rare'},
  {id:'set-weapon', name:'Ember Edge', slot:'weapon_skin', asset_key:'arena_set_003_ember', rarity:'rare'},
  {id:'set-attachment', name:'Ember Fin', slot:'attachment', asset_key:'arena_set_003_ember', rarity:'rare'},
  {id:'trail', name:'Comet Tail', slot:'trail', asset_key:'comet_tail', rarity:'epic'},
];
const paid = items.filter(item => !item.is_free);
const bots = [{id:'bot-1', name:'Alpha', key_prefix:'arena_alpha'}, {id:'bot-2', name:'Beta', key_prefix:'arena_beta'}];
const base = {account:{id:'acct-1', email_verified:true, display_name:'Pilot'}, bots, items, loadouts:{}};
const subscribed = {...base, subscription:{active:true, synced_at:'2026-09-01T12:00:00Z', url:SUBSCRIBE_AT}};
const unsubscribed = {...base, subscription:{active:false, synced_at:'2026-09-01T12:00:00Z', url:SUBSCRIBE_AT}};

// ---- Active subscription: everything, on every bot -----------------------
for (const bot of bots) {
  for (const item of items) {
    const intent = cosmetics.equipIntent(subscribed, bot.id, item.id);
    assert.equal(intent.ok, true, `${item.id} must equip on ${bot.name} with an active subscription`);
    assert.deepEqual(JSON.parse(JSON.stringify(intent.body)), {slot:item.slot, cosmetic_id:item.id});
    assert.equal(cosmetics.previewModel(subscribed, bot.id, {[item.slot]:item.id}).slots[item.slot].canEquip, true);
  }
}
const subscribedHTML = cosmetics.renderPanel(subscribed, {selectedBotID:'bot-2'});
assert.match(subscribedHTML, /Everything unlocked/);
assert.match(subscribedHTML, /5 cosmetics, all unlocked/);
for (const item of paid) {
  assert.match(subscribedHTML, new RegExp(`data-cosmetic-equip="${item.id}"[^>]*>Equip on Beta`), `${item.id} offers Equip`);
}
assert.doesNotMatch(subscribedHTML, /Included with an Arena subscription<\/span>|ownership-badge locked|cosmetic-card locked/,
  'nothing is locked for a subscriber');

// ---- No subscription: locked, said plainly, with the way to fix it -------
for (const bot of bots) {
  for (const item of paid) {
    const intent = cosmetics.equipIntent(unsubscribed, bot.id, item.id);
    assert.equal(intent.ok, false, `${item.id} must not equip without a subscription`);
    assert.equal(intent.reason, 'subscription-required');
    assert.equal(intent.url, SUBSCRIBE_AT, 'the refusal carries the address to subscribe at');
    assert.equal(cosmetics.previewModel(unsubscribed, bot.id, {[item.slot]:item.id}).slots[item.slot].canEquip, false);
    assert.equal(cosmetics.previewModel(unsubscribed, bot.id, {[item.slot]:item.id}).slots[item.slot].stagedItem?.id, item.id,
      'previewing a locked look is still allowed');
  }
  assert.equal(cosmetics.equipIntent(unsubscribed, bot.id, 'free-skin').ok, true, 'free cosmetics never need the subscription');
}
const lockedHTML = cosmetics.renderPanel(unsubscribed, {selectedBotID:'bot-1'});
assert.match(lockedHTML, /<h2 id="subscription-title">Included with an Arena subscription<\/h2>/);
assert.match(lockedHTML, new RegExp(`<a class="sm subscription-action" href="${SUBSCRIBE_AT}" target="_blank" rel="noopener" data-subscription-link>Subscribe in your Angel account</a>`),
  'the locked state links to the Accounts shop');
assert.match(lockedHTML, /1 of 5 unlocked/);
for (const item of paid) {
  assert.match(lockedHTML, new RegExp(`cosmetic-card locked" data-cosmetic-id="${item.id}"`), `${item.id} renders locked`);
  assert.doesNotMatch(lockedHTML, new RegExp(`data-cosmetic-equip="${item.id}"`), `${item.id} offers no Equip`);
}
assert.equal((lockedHTML.match(/Included with an Arena subscription<\/span>/g) || []).length, paid.length,
  'every locked card says what unlocks it');
assert.match(lockedHTML, /data-cosmetic-equip="free-skin"[^>]*>Equip on Alpha/);

// ---- Flipping the flag is the whole change --------------------------------
const lapsed = cosmetics.normalizeSnapshot({...subscribed, subscription:{...subscribed.subscription, active:false}, loadouts:{'bot-1':{bot_skin:'set-skin'}}});
assert.deepEqual(JSON.parse(JSON.stringify(cosmetics.equippedLoadout(lapsed, 'bot-1'))).bot_skin, 'arena_set_003_ember',
  'the saved loadout is still the saved loadout; the server withholds the paid look until the subscription is back');
assert.equal(cosmetics.equipIntent(lapsed, 'bot-1', 'set-weapon').reason, 'subscription-required');
assert.equal(cosmetics.equipIntent({...lapsed, subscription:{active:true}}, 'bot-1', 'set-weapon').ok, true);

// ---- Nothing else is for sale anywhere in the front end -------------------
for (const gone of ['../frontend/js/embedded-checkout.js', '../frontend/css/embedded-checkout.css',
  './test-dashboard-cosmetics-checkout.mjs', './test-embedded-stripe-checkout.mjs', './test-purchases-via-accounts.mjs']) {
  assert.equal(existsSync(new URL(gone, import.meta.url)), false, `${gone} belongs to the retired checkout`);
}
const frontEnd = [
  '../frontend/index.html', '../frontend/dashboard/index.html', '../frontend/dashboard/dashboard.js',
  '../frontend/dashboard/account-cosmetics.js', '../frontend/shop/index.html', '../frontend/js/cosmetics-shop.js',
  '../frontend/admin/index.html', '../frontend/js/app.js', '../frontend/legal/privacy.html',
].map(path => [path, read(path)]);
for (const [path, text] of frontEnd) {
  // price_cents survives only as catalog metadata the admin editor keeps; no page reads it to sell anything.
  assert.doesNotMatch(text, /stripe|embedded-checkout|checkout_enabled|purchase_handoff|subscription_offer|data-pack-checkout|data-order-resume|All Access/i,
    `${path} still carries per-item commerce`);
}
assert.match(read('../.github/workflows/ci.yml'), /test-subscription-unlocks-cosmetics\.mjs/, 'CI runs this');
assert.doesNotMatch(read('../.github/workflows/ci.yml'), /cosmetics-checkout|stripe|purchases-via-accounts/, 'CI runs nothing retired');

console.log('one Arena subscription unlocks every cosmetic; without it the front end says so and points at Accounts');
