(function attachArenaAccountCosmetics(root) {
  'use strict';

  /*
   * The Dashboard's cosmetics model, and nothing else.
   *
   * Arena sells one thing, and does not sell it itself: the Arena
   * subscription, bought and held in Angel Accounts. So there is no per-item
   * ownership here — no licences to assign, no orders to resume, no checkout
   * to open. A signed-in account either holds an active subscription, in
   * which case every cosmetic in the catalog is unlocked for every linked
   * bot, or it does not, in which case every paid cosmetic reads "Included
   * with an Arena subscription" and points at where to get one.
   *
   * Everything in this file is a pure function of server data so it can be
   * exercised without a browser (scripts/test-dashboard-account-cosmetics.mjs).
   */

  function escapeHTML(value) {
    return String(value ?? '')
      .replaceAll('&', '&amp;')
      .replaceAll('<', '&lt;')
      .replaceAll('>', '&gt;')
      .replaceAll('"', '&quot;')
      .replaceAll("'", '&#39;');
  }

  function cleanText(value) {
    return typeof value === 'string' ? value.trim() : '';
  }

  /**
   * A URL this page is willing to navigate somebody to, or ''.
   *
   * Everything that reaches here comes from Arena's own server, so this is not
   * a defence against a hostile catalogue — it is a floor under a
   * configuration mistake. An operator who sets the subscription address to a
   * path, or leaves a placeholder in it, gets a link that is not offered
   * rather than one that navigates somewhere unintended. `javascript:` is
   * refused by the same rule that refuses everything else: only https is an
   * address.
   */
  function httpsURL(value) {
    const raw = cleanText(value);
    if (!raw.startsWith('https://') || raw.length <= 'https://'.length) return '';
    // Whitespace and quotes cannot appear in a URL that was not built wrong,
    // and both are how a value ends up escaping the attribute it is written
    // into. Refuse rather than encode: there is no correct address that needs
    // either, so a value carrying one is a mistake to surface, not to repair.
    return /[\s"'<>]/.test(raw) ? '' : raw;
  }

  const PREVIEW_SLOT_DEFAULTS = Object.freeze({
    bot_skin: 'standard',
    weapon_skin: 'standard',
    attachment: 'none',
    trail: 'standard',
  });
  const PREVIEW_SLOTS = Object.freeze(Object.keys(PREVIEW_SLOT_DEFAULTS));
  const PREVIEW_ASSET_KEY_PATTERN = /^[a-z0-9][a-z0-9_-]{0,79}$/;
  const COLLECTION_PAGE_SIZE = 24;

  function emptyPreviewLoadout() {
    return {...PREVIEW_SLOT_DEFAULTS};
  }

  function previewAssetKey(item) {
    const assetKey = cleanText(item?.asset_key);
    return PREVIEW_ASSET_KEY_PATTERN.test(assetKey) ? assetKey : '';
  }

  function normalizeSession(payload) {
    const source = payload && typeof payload === 'object' ? payload : {};
    const rawAccount = source.account && typeof source.account === 'object'
      ? source.account
      : source;
    const email = cleanText(rawAccount.email).toLowerCase();
    const displayName = cleanText(rawAccount.display_name);
    const legacyName = cleanText(rawAccount.name);
    const emailVerified = rawAccount.email_verified === true || Boolean(cleanText(rawAccount.email_verified_at));
    const authenticated = typeof source.authenticated === 'boolean'
      ? source.authenticated
      : Boolean(rawAccount.id && email);
    return {
      authenticated,
      login_enabled: source.login_enabled === true || authenticated,
      email_login_enabled: source.email_login_enabled === true,
      oidc_login_enabled: source.oidc_login_enabled === true,
      login_url: cleanText(source.login_url),
      logout_url: cleanText(source.logout_url),
      account: {
        id: cleanText(rawAccount.id),
        email,
        email_verified: emailVerified,
        name: displayName || legacyName,
      },
    };
  }

  function isVerifiedAccount(rawAccount) {
    const account = rawAccount && typeof rawAccount === 'object' ? rawAccount : {};
    return Boolean(cleanText(account.id)) && account.email_verified === true;
  }

  function hasVerifiedAccount(rawSession) {
    const source = rawSession && typeof rawSession === 'object' ? rawSession : {};
    const session = normalizeSession(source);
    return source.authenticated === true && isVerifiedAccount(session.account);
  }

  function accountLabel(rawAccount) {
    const account = rawAccount && typeof rawAccount === 'object' ? rawAccount : {};
    const displayName = cleanText(account.display_name);
    const legacyName = cleanText(account.name);
    return displayName || legacyName
      || cleanText(account.email).toLowerCase()
      || 'Angel account';
  }

  function normalizeBot(raw) {
    const bot = raw && typeof raw === 'object' ? raw : {};
    return {
      id: cleanText(bot.id || bot.bot_id),
      name: cleanText(bot.name || bot.bot_name) || 'Unnamed bot',
      key_prefix: cleanText(bot.key_prefix || bot.api_key_prefix),
      key_is_active: bot.key_is_active !== false && bot.is_active !== false,
      avatar_color: cleanText(bot.avatar_color) || '#5edfff',
      default_weapon: cleanText(bot.default_weapon || bot.weapon) || 'sword',
      linked_at: cleanText(bot.linked_at),
    };
  }

  function normalizeItem(raw) {
    const item = raw && typeof raw === 'object' ? raw : {};
    return {
      id: cleanText(item.id || item.cosmetic_id),
      name: cleanText(item.name) || 'Unnamed cosmetic',
      description: cleanText(item.description),
      category_id: cleanText(item.category_id),
      slot: cleanText(item.slot),
      asset_key: cleanText(item.asset_key),
      rarity: cleanText(item.rarity) || 'common',
      is_free: item.is_free === true,
      is_active: item.is_active !== false,
    };
  }

  function normalizeSubscription(payload) {
    const source = payload && typeof payload === 'object' ? payload : {};
    return {
      active: source.active === true,
      synced_at: cleanText(source.synced_at),
      url: httpsURL(source.url),
    };
  }

  function normalizeLoadouts(payload) {
    const source = payload && typeof payload === 'object' && !Array.isArray(payload) ? payload : {};
    const loadouts = {};
    for (const [botID, rawSlots] of Object.entries(source)) {
      const id = cleanText(botID);
      const slots = rawSlots && typeof rawSlots === 'object' ? rawSlots : {};
      if (!id) continue;
      loadouts[id] = {};
      for (const slot of PREVIEW_SLOTS) {
        const itemID = cleanText(slots[slot]);
        if (itemID) loadouts[id][slot] = itemID;
      }
    }
    return loadouts;
  }

  function normalizeSnapshot(payload) {
    const source = payload && typeof payload === 'object' ? payload : {};
    const session = normalizeSession({
      authenticated: true,
      account: source.account || {},
    });
    const bots = Array.isArray(source.bots) ? source.bots.map(normalizeBot).filter(bot => bot.id) : [];
    const items = Array.isArray(source.items) ? source.items.map(normalizeItem).filter(item => item.id) : [];
    return {
      account: session.account,
      bots,
      subscription: normalizeSubscription(source.subscription),
      items,
      loadouts: normalizeLoadouts(source.loadouts),
    };
  }

  function normalizeCatalog(payload) {
    const source = payload && typeof payload === 'object' ? payload : {};
    const subscription = source.subscription && typeof source.subscription === 'object' ? source.subscription : {};
    return {
      categories: Array.isArray(source.categories) ? source.categories : [],
      items: Array.isArray(source.items) ? source.items : [],
      packs: Array.isArray(source.packs) ? source.packs.filter(pack => pack && typeof pack === 'object') : [],
      // Where the subscription is bought. Only ever an absolute https URL
      // from the server, read as such: a relative or javascript: value would
      // be a destination this page then navigates to, so anything that is
      // not plainly an https origin is treated as absent rather than
      // sanitised into something plausible.
      // Also read back the normalised shape, so a catalog that has already
      // been through here (the Dashboard keeps that one) keeps its address.
      subscription_url: httpsURL(subscription.url) || httpsURL(source.subscription_url),
    };
  }

  /** The address to subscribe at, from whichever document carried it. */
  function subscriptionURL(rawSnapshot, rawCatalog) {
    const snapshot = rawSnapshot && typeof rawSnapshot === 'object' ? normalizeSnapshot(rawSnapshot) : null;
    const catalog = rawCatalog && typeof rawCatalog === 'object' ? normalizeCatalog(rawCatalog) : null;
    return snapshot?.subscription.url || catalog?.subscription_url || '';
  }

  /** Whether an item is available to this account: free, or subscribed. */
  function itemUnlocked(snapshot, item) {
    return item.is_active && (item.is_free || snapshot.subscription.active);
  }

  function itemsByID(snapshot) {
    const index = new Map();
    for (const item of snapshot.items) index.set(item.id, item);
    return index;
  }

  function currentPreviewState(snapshot, botID) {
    const loadout = emptyPreviewLoadout();
    const items = {};
    if (!snapshot.bots.some(bot => bot.id === botID)) return {loadout, items};
    const index = itemsByID(snapshot);
    const equipped = snapshot.loadouts[botID] || {};
    for (const slot of PREVIEW_SLOTS) {
      const item = index.get(cleanText(equipped[slot]));
      const assetKey = previewAssetKey(item);
      if (!item || item.slot !== slot || !item.is_active || !assetKey) continue;
      loadout[slot] = assetKey;
      items[slot] = item;
    }
    return {loadout, items};
  }

  // Returns the server-authoritative visual loadout for one linked bot. The
  // server already withheld any paid look the account may not render right
  // now, so what is here is exactly what the arena shows.
  function equippedLoadout(rawSnapshot, rawBotID) {
    const snapshot = normalizeSnapshot(rawSnapshot);
    const botID = cleanText(rawBotID);
    return currentPreviewState(snapshot, botID).loadout;
  }

  // Builds a visual-only staged loadout. stagedBySlot must map a known slot to
  // a catalog item id. Each choice is resolved again from the latest account
  // snapshot, which prevents stale, inactive, wrong-slot, or arbitrary asset
  // values from reaching the renderer or being mistaken for equip authority.
  // Previewing is free for everybody; equipping is what the subscription
  // gates, and canEquip says so per slot.
  function previewModel(rawSnapshot, rawBotID, rawStagedBySlot) {
    const snapshot = normalizeSnapshot(rawSnapshot);
    const botID = cleanText(rawBotID);
    const bot = snapshot.bots.find(entry => entry.id === botID) || null;
    const current = currentPreviewState(snapshot, bot?.id || '');
    const previewLoadout = {...current.loadout};
    const stagedBySlot = {};
    const stagedItems = {};
    const requested = rawStagedBySlot && typeof rawStagedBySlot === 'object' && !Array.isArray(rawStagedBySlot)
      ? rawStagedBySlot
      : {};

    if (bot) {
      const index = itemsByID(snapshot);
      for (const slot of PREVIEW_SLOTS) {
        const itemID = cleanText(requested[slot]);
        if (!itemID) continue;
        const item = index.get(itemID);
        const assetKey = previewAssetKey(item);
        if (!item || !item.is_active || item.slot !== slot || !assetKey) continue;
        stagedBySlot[slot] = item.id;
        stagedItems[slot] = item;
        previewLoadout[slot] = assetKey;
      }
    }

    const slots = {};
    for (const slot of PREVIEW_SLOTS) {
      const stagedItem = stagedItems[slot] || null;
      const currentItem = current.items[slot] || null;
      slots[slot] = {
        currentItem,
        stagedItem,
        assetKey: previewLoadout[slot],
        unlocked: Boolean(stagedItem && itemUnlocked(snapshot, stagedItem)),
        canEquip: Boolean(
          bot?.key_is_active && stagedItem && itemUnlocked(snapshot, stagedItem) &&
          stagedItem.id !== currentItem?.id,
        ),
      };
    }

    return {
      bot,
      currentLoadout: current.loadout,
      previewLoadout,
      currentItems: current.items,
      stagedItems,
      stagedBySlot,
      slots,
      hasStaged: Object.keys(stagedBySlot).length > 0,
      isDirty: PREVIEW_SLOTS.some(slot => previewLoadout[slot] !== current.loadout[slot]),
    };
  }

  function normalizeKeyCollection(payload) {
    const source = payload && typeof payload === 'object' ? payload : {};
    const keys = Array.isArray(source.keys) ? source.keys.map(raw => {
      const key = raw && typeof raw === 'object' ? raw : {};
      return {
        id: cleanText(key.id || key.key_id),
        key_prefix: cleanText(key.key_prefix || key.api_key_prefix),
        bot_id: cleanText(key.bot_id),
        bot_name: cleanText(key.bot_name || key.name) || 'Unnamed bot',
        created_at: cleanText(key.created_at),
        last_used_at: cleanText(key.last_used_at),
        is_active: key.is_active !== false,
      };
    }).filter(key => key.id) : [];
    const rawLimit = Number(source.limit);
    const limit = Number.isFinite(rawLimit) && rawLimit > 0 ? Math.min(5, Math.floor(rawLimit)) : 5;
    const rawCount = Number(source.active_count);
    const activeCount = Number.isFinite(rawCount) && rawCount >= 0
      ? Math.floor(rawCount)
      : keys.filter(key => key.is_active).length;
    return {keys, active_count: activeCount, limit};
  }

  function slotLabel(slot) {
    const labels = {
      bot_skin: 'Bot skins',
      weapon_skin: 'Weapon designs',
      attachment: 'Attachments',
      trail: 'Trails',
    };
    return labels[slot] || (slot ? slot.replaceAll('_', ' ') : 'Cosmetics');
  }

  function accountRoute(name, id) {
    const encoded = id ? encodeURIComponent(String(id)) : '';
    const routes = {
      session: '/account/session',
      cosmetics: '/account/cosmetics',
      keys: '/account/keys',
      key: `/account/keys/${encoded}`,
      bots: '/account/bots',
      bot: `/account/bots/${encoded}`,
      equip: `/account/bots/${encoded}/cosmetics`,
    };
    if (!Object.hasOwn(routes, name)) throw new Error(`unknown account route: ${name}`);
    return routes[name];
  }

  /**
   * Whether this bot may put on this item right now, and the request that
   * does it. Every refusal names a reason the Dashboard can explain; the
   * server makes the same decision again with the account row locked.
   */
  function equipIntent(rawSnapshot, rawBotID, rawItemID) {
    const snapshot = normalizeSnapshot(rawSnapshot);
    if (!isVerifiedAccount(snapshot.account)) {
      return {ok: false, reason: 'verified-account-required'};
    }
    const bot = snapshot.bots.find(entry => entry.id === cleanText(rawBotID));
    if (!bot) return {ok: false, reason: 'bot-not-linked'};
    if (!bot.key_is_active) return {ok: false, reason: 'bot-key-inactive'};
    const item = snapshot.items.find(entry => entry.id === cleanText(rawItemID));
    if (!item) return {ok: false, reason: 'item-not-found'};
    if (!item.is_active || !PREVIEW_SLOTS.includes(item.slot)) return {ok: false, reason: 'item-inactive'};
    if (!itemUnlocked(snapshot, item)) {
      return {ok: false, reason: 'subscription-required', url: snapshot.subscription.url};
    }
    if ((snapshot.loadouts[bot.id] || {})[item.slot] === item.id) {
      return {ok: false, reason: 'already-equipped'};
    }
    return {
      ok: true,
      path: accountRoute('equip', bot.id),
      body: {slot: item.slot, cosmetic_id: item.id},
      bot_id: bot.id,
      slot: item.slot,
    };
  }

  function keyCreateIntent(rawBotName, rawCollection) {
    if (!rawCollection || typeof rawCollection !== 'object') {
      return {ok: false, reason: 'keys-unavailable'};
    }
    const collection = normalizeKeyCollection(rawCollection);
    const botName = cleanText(rawBotName);
    if (!botName) return {ok: false, reason: 'bot-name-required'};
    if (botName.length > 80) return {ok: false, reason: 'bot-name-too-long'};
    if (collection.active_count >= collection.limit) return {ok: false, reason: 'key-limit-reached'};
    return {ok: true, path: accountRoute('keys'), body: {bot_name: botName}};
  }

  function keyRevokeIntent(rawKeyID) {
    const keyID = cleanText(rawKeyID);
    if (!keyID) return {ok: false, reason: 'key-not-found'};
    return {ok: true, path: accountRoute('key', keyID)};
  }

  function requestHeaders(method, csrfToken, hasBody) {
    const normalizedMethod = String(method || 'GET').toUpperCase();
    const headers = {Accept: 'application/json'};
    if (hasBody) headers['Content-Type'] = 'application/json';
    if (normalizedMethod !== 'GET' && normalizedMethod !== 'HEAD' && csrfToken) {
      headers['X-CSRF-Token'] = csrfToken;
    }
    return headers;
  }

  function subscriptionDateLabel(rawTime) {
    const raw = cleanText(rawTime);
    const date = new Date(raw);
    if (!raw || Number.isNaN(date.getTime())) return '';
    return date.toLocaleString(undefined, {
      year: 'numeric', month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit',
    });
  }

  /*
   * The subscription card: the one commerce fact on the page.
   *
   * Arena reads whether the account subscribes at the moment they sign in,
   * and holds no credential to ask again later (see customer_entitlements.go
   * on the server for why that is the deliberate choice). So a subscription
   * started in the Angel account a minute ago is genuinely not here yet, and
   * the honest thing is to say when it was last read and offer the one action
   * that reads it again — a sign-in, which on a live Angel session is one
   * press and a window that closes itself.
   */
  function renderSubscriptionCard(snapshot, view) {
    const subscription = snapshot.subscription;
    const url = subscription.url || httpsURL(view.catalog?.subscription?.url) || subscriptionURL(null, view.catalog);
    const syncedAt = subscriptionDateLabel(subscription.synced_at);
    const refresh = `<button type="button" class="sm" data-subscription-refresh${view.entitlementsBusy ? ' disabled' : ''}>` +
      `${view.entitlementsBusy ? 'Reading your account...' : 'Refresh subscription'}</button>`;
    const status = subscription.active
      ? '<span class="subscription-status is-active">Subscription active</span>'
      : '<span class="subscription-status">Not subscribed</span>';
    const copy = subscription.active
      ? 'Every cosmetic in the catalog is unlocked for every bot linked to this account. Equip anything on any bot; nothing to assign, nothing to buy per item.'
      : 'Paid cosmetics are included with an Arena subscription. Subscribe in your Angel account and every set, full-body skin and trail unlocks for all your linked bots.';
    const manage = url
      ? `<a class="sm subscription-action" href="${escapeHTML(url)}" target="_blank" rel="noopener" data-subscription-link>${subscription.active ? 'Manage in your Angel account' : 'Subscribe in your Angel account'}</a>`
      : `<span class="subscription-unavailable">${subscription.active ? 'Managed in your Angel account.' : 'Ask the Arena operator where to subscribe.'}</span>`;
    const read = syncedAt
      ? `Last read from your Angel account ${escapeHTML(syncedAt)}.`
      : 'Not read from your Angel account yet.';
    return `<section class="subscription-card${subscription.active ? ' is-active' : ''}" aria-labelledby="subscription-title" data-subscription-active="${subscription.active}">
      <div class="subscription-copy">
        <div class="cosmetic-kicker">Arena subscription</div>
        <h2 id="subscription-title">${subscription.active ? 'Everything unlocked' : 'Included with an Arena subscription'}</h2>
        <p>${copy}</p>
        <p class="subscription-read">${read} Just subscribed? <b>Refresh subscription</b> reads your account again.</p>
      </div>
      <div class="subscription-offer">
        ${status}
        ${manage}
        ${refresh}
      </div>
    </section>`;
  }

  function normalizeCollectionFilter(rawFilter) {
    const filter = rawFilter && typeof rawFilter === 'object' ? rawFilter : {};
    const rawVisible = Number(filter.visible);
    return {
      query: cleanText(filter.query).toLowerCase(),
      slot: PREVIEW_SLOTS.includes(cleanText(filter.slot)) ? cleanText(filter.slot) : 'all',
      visible: Number.isFinite(rawVisible) && rawVisible > 0 ? Math.floor(rawVisible) : COLLECTION_PAGE_SIZE,
    };
  }

  function searchText(item) {
    return [item.id, item.name, item.description, item.rarity, item.slot, item.category_id]
      .filter(Boolean).join(' ').toLowerCase().replace(/[-_\s]+/g, ' ');
  }

  /** The items the collection shows for one filter, in catalog order. */
  function collectionItems(rawSnapshot, rawFilter) {
    const snapshot = normalizeSnapshot(rawSnapshot);
    const filter = normalizeCollectionFilter(rawFilter);
    const query = filter.query.replace(/[-_\s]+/g, ' ');
    return snapshot.items.filter(item => {
      if (!item.is_active || !PREVIEW_SLOTS.includes(item.slot)) return false;
      if (filter.slot !== 'all' && item.slot !== filter.slot) return false;
      return !query || searchText(item).includes(query);
    });
  }

  function wearersOf(snapshot, item) {
    return snapshot.bots.filter(bot => (snapshot.loadouts[bot.id] || {})[item.slot] === item.id);
  }

  function renderCosmeticCard(item, snapshot, view, selectedBot) {
    const unlocked = itemUnlocked(snapshot, item);
    const busy = cleanText(view.busyCosmeticID) === item.id;
    const wearers = wearersOf(snapshot, item);
    const wornBySelected = Boolean(selectedBot && wearers.some(bot => bot.id === selectedBot.id));
    const badge = item.is_free
      ? '<span class="ownership-badge">Free</span>'
      : unlocked
        ? '<span class="ownership-badge">Included</span>'
        : '<span class="ownership-badge locked">Subscription</span>';
    const wornCopy = wearers.length
      ? `Equipped on <strong>${escapeHTML(wearers.map(bot => bot.name).join(', '))}</strong>`
      : 'Not equipped on any bot';
    const previewable = snapshot.bots.length > 0;
    let equipControl = '';
    if (!selectedBot) {
      equipControl = '<span class="cosmetic-equip-hint">Link a bot to equip</span>';
    } else if (!unlocked) {
      equipControl = '<span class="cosmetic-equip-hint">Included with an Arena subscription</span>';
    } else if (wornBySelected) {
      equipControl = `<span class="cosmetic-equip-hint">Equipped on ${escapeHTML(selectedBot.name)}</span>`;
    } else if (!selectedBot.key_is_active) {
      equipControl = `<button class="sm cosmetic-equip" type="button" data-cosmetic-equip="${escapeHTML(item.id)}" disabled>Bot key inactive</button>`;
    } else {
      equipControl = `<button class="sm cosmetic-equip" type="button" data-cosmetic-equip="${escapeHTML(item.id)}"${busy ? ' disabled' : ''}>${busy ? 'Saving...' : `Equip on ${escapeHTML(selectedBot.name)}`}</button>`;
    }
    return `<article class="cosmetic-card${unlocked ? '' : ' locked'}" data-cosmetic-id="${escapeHTML(item.id)}">
      <div class="cosmetic-card-head">
        <div>
          <div class="cosmetic-kicker">${escapeHTML(slotLabel(item.slot))} - ${escapeHTML(item.rarity)}</div>
          <h3>${escapeHTML(item.name)}</h3>
        </div>
        ${badge}
      </div>
      <p>${escapeHTML(item.description || 'Visual customization only. No gameplay advantage.')}</p>
      <div class="cosmetic-equip-state${wearers.length ? ' equipped' : ''}">${wornCopy}</div>
      <div class="cosmetic-card-actions">
        <button class="sm cosmetic-preview" type="button" data-cosmetic-preview="${escapeHTML(item.id)}" aria-label="Preview ${escapeHTML(item.name)} on the selected bot"${previewable && !busy ? '' : ' disabled'}>${previewable ? 'Preview' : 'Link a bot to preview'}</button>
        ${equipControl}
      </div>
    </article>`;
  }

  function renderCollection(snapshot, view) {
    const filter = normalizeCollectionFilter(view.filter);
    const matches = collectionItems(snapshot, filter);
    const shown = matches.slice(0, filter.visible);
    const selectedBot = snapshot.bots.find(bot => bot.id === cleanText(view.selectedBotID)) || snapshot.bots[0] || null;
    const groups = new Map();
    for (const item of shown) {
      if (!groups.has(item.slot)) groups.set(item.slot, []);
      groups.get(item.slot).push(item);
    }
    const body = groups.size
      ? [...groups.entries()].map(([slot, items]) => `<section class="cosmetic-group">
          <h3>${escapeHTML(slotLabel(slot))}</h3>
          <div class="cosmetic-card-grid">${items.map(item => renderCosmeticCard(item, snapshot, view, selectedBot)).join('')}</div>
        </section>`).join('')
      : '<div class="cosmetic-empty cosmetic-empty-inventory">No cosmetics match. Try another name or clear the filter.</div>';
    const more = matches.length > shown.length
      ? `<button type="button" class="sm cosmetic-show-more" data-collection-more>Show ${Math.min(COLLECTION_PAGE_SIZE, matches.length - shown.length)} more</button>`
      : '';
    const live = snapshot.items.filter(item => item.is_active && PREVIEW_SLOTS.includes(item.slot));
    const unlockedCount = live.filter(item => itemUnlocked(snapshot, item)).length;
    const summary = snapshot.subscription.active
      ? `${live.length} cosmetics, all unlocked`
      : `${unlockedCount} of ${live.length} unlocked`;
    const slotOptions = ['all', ...PREVIEW_SLOTS].map(slot =>
      `<option value="${escapeHTML(slot)}"${filter.slot === slot ? ' selected' : ''}>${slot === 'all' ? 'All slots' : escapeHTML(slotLabel(slot))}</option>`).join('');
    const target = selectedBot
      ? `Equip puts a cosmetic on <strong>${escapeHTML(selectedBot.name)}</strong> right now; choose another bot in the outfitter above.`
      : 'Link a bot from the Profile tab to equip anything.';
    return `<section class="cosmetic-inventory" data-cosmetic-collection>
      <div class="cosmetic-inventory-head">
        <div><div class="cosmetic-kicker">Your collection</div><h2>Cosmetics</h2></div>
        <span>${escapeHTML(summary)}</span>
      </div>
      <p class="cosmetic-rule">Every set, full-body skin and trail is included with the Arena subscription; free items are always open. ${target}</p>
      <form class="cosmetic-collection-filter" role="search" aria-label="Filter cosmetics">
        <label>Find a cosmetic<input type="search" name="query" data-collection-query value="${escapeHTML(filter.query)}" placeholder="Name, set, rarity" autocomplete="off"></label>
        <label>Slot<select name="slot" data-collection-slot>${slotOptions}</select></label>
        <span class="cosmetic-collection-count" role="status">Showing ${shown.length} of ${matches.length}</span>
      </form>
      ${body}
      ${more}
    </section>`;
  }

  function keyDateLabel(rawTime) {
    const raw = cleanText(rawTime);
    const date = new Date(raw);
    if (!raw || Number.isNaN(date.getTime())) return 'Never used';
    return date.toLocaleDateString(undefined, {year: 'numeric', month: 'short', day: 'numeric'});
  }

  function renderAccountKeys(view) {
    const keysReady = Boolean(view.keys && typeof view.keys === 'object' && !view.keysError);
    const collection = normalizeKeyCollection(view.keys);
    const atLimit = keysReady && collection.active_count >= collection.limit;
    const busyKeyID = cleanText(view.busyKeyID);
    let body = '';
    if (view.keysError) {
      body = `<div class="tip warn" role="alert"><b>API keys unavailable:</b> ${escapeHTML(view.keysError)} <button type="button" class="sm" data-account-keys-retry>Retry</button></div>`;
    } else if (!view.keys) {
      body = '<div class="cosmetic-loading" aria-busy="true">Loading account API keys...</div>';
    } else if (!collection.keys.length) {
      body = '<div class="cosmetic-empty cosmetic-empty-inventory">No API keys yet. Name a bot to create the first one.</div>';
    } else {
      body = `<div class="account-key-list">${collection.keys.map(key => {
        const active = key.is_active;
        const used = key.last_used_at ? `Last used ${keyDateLabel(key.last_used_at)}` : 'Never used';
        return `<article class="account-key-row" data-account-key-id="${escapeHTML(key.id)}">
          <div><strong>${escapeHTML(key.bot_name)}</strong><code>${escapeHTML(key.key_prefix || key.id.slice(0, 10))}...</code></div>
          <div class="account-key-meta"><span>${active ? 'Active' : 'Revoked'}</span><span>${escapeHTML(used)}</span></div>
          ${active ? `<button type="button" class="sm danger" data-account-key-revoke="${escapeHTML(key.id)}"${busyKeyID === key.id ? ' disabled' : ''}>${busyKeyID === key.id ? 'Revoking...' : 'Revoke key'}</button>` : ''}
        </article>`;
      }).join('')}</div>`;
    }
    const generated = view.generatedKey && typeof view.generatedKey === 'object' ? view.generatedKey : null;
    const generatedValue = cleanText(generated?.api_key);
    const generatedPanel = generatedValue
      ? `<div class="account-generated-key" role="status">
          <div><strong>Copy this key now</strong><span>It cannot be recovered after you clear it.</span></div>
          <div class="account-generated-key-field">
            <input id="accountGeneratedKey" type="text" value="${escapeHTML(generatedValue)}" readonly autocomplete="off" spellcheck="false" aria-label="New API key">
            <button type="button" class="sm" data-account-key-copy>Copy key</button>
            <button type="button" class="sm" data-account-key-clear>Clear key</button>
          </div>
        </div>`
      : '';
    return `<section class="account-key-manager" aria-labelledby="account-keys-title">
      <div class="cosmetic-inventory-head">
        <div><div class="cosmetic-kicker">Account credentials</div><h2 id="account-keys-title">API keys</h2></div>
        <span>${collection.active_count} of ${collection.limit} active</span>
      </div>
      <p class="cosmetic-rule">Keys are generated and stored with this verified account. You can keep up to 5 active keys and revoke any one without affecting your subscription or cosmetics.</p>
      ${generatedPanel}
      <form id="accountKeyForm" class="account-key-form">
        <label for="accountKeyBotName">Bot name</label>
        <input id="accountKeyBotName" name="bot_name" maxlength="80" autocomplete="off" placeholder="My Arena bot" required${!keysReady || atLimit ? ' disabled' : ''}>
        <button type="submit" id="accountKeyCreate"${!keysReady || atLimit || view.keyCreateBusy ? ' disabled' : ''}>${view.keyCreateBusy ? 'Creating key...' : 'Create API key'}</button>
        <small>${!keysReady ? 'Wait for account key usage to load before creating a key.' : atLimit ? 'Revoke an active key before creating another.' : 'The full key is shown once. Store it before clearing this screen.'}</small>
      </form>
      ${body}
    </section>`;
  }

  function renderShopLink() {
    return `<section class="cosmetic-shop cosmetic-shop-link" aria-labelledby="cosmetic-shop-link-title">
      <div class="cosmetic-inventory-head">
        <div><div class="cosmetic-kicker">Looking around?</div><h2 id="cosmetic-shop-link-title">Browse the Shop</h2></div>
      </div>
      <p class="cosmetic-rule">Preview every set, full-body skin and trail on a full-size bot before you equip it here.</p>
      <button type="button" class="sm" data-open-shop>Open the Shop</button>
    </section>`;
  }

  // Bot linking lives on the Profile tab (it's account/credential
  // management, not a cosmetics concern), rendered separately by index.html
  // via window.ArenaAccountCosmetics.renderLinkedBots -- kept here because
  // the underlying data (snapshot.bots) is still fetched alongside cosmetic
  // inventory, not through account-profile.js's own data flow.
  function renderLinkedBots(snapshot, view) {
    const owner = accountLabel(snapshot.account);
    const botRows = snapshot.bots.length
      ? snapshot.bots.map(bot => `<li data-linked-bot-id="${escapeHTML(bot.id)}">
          <span><strong>${escapeHTML(bot.name)}</strong>${bot.key_prefix ? `<small>${escapeHTML(bot.key_prefix)}...</small>` : ''}</span>
          <span class="linked-bot-actions"><span class="linked-bot-state${bot.key_is_active ? '' : ' inactive'}">${bot.key_is_active ? 'Linked' : 'Key inactive'}</span><button type="button" class="sm danger" data-bot-unlink="${escapeHTML(bot.id)}">Unlink</button></span>
        </li>`).join('')
      : '<li class="cosmetic-empty">No bots linked yet. Link one by proving its API key below.</li>';
    return `<section class="cosmetic-sidebar" aria-labelledby="linked-bots-title">
      <div class="cosmetic-inventory-head">
        <div><div class="cosmetic-kicker">Your bots</div><h2 id="linked-bots-title">Linked bots</h2></div>
      </div>
      <p class="cosmetic-rule">Claim a bot you started anonymously by proving its server-issued token. Linking does not transfer anything to the token -- the subscription always stays with ${escapeHTML(owner)}, and every linked bot shares it.</p>
      <ul class="linked-bot-list">${botRows}</ul>
      <form id="linkBotForm" class="link-bot-form">
        <label for="linkBotKey">Claim or link an existing bot</label>
        <input type="password" id="linkBotKey" name="api_key" placeholder="arena_..." autocomplete="off" spellcheck="false" required>
        <button type="submit"${view?.linkBusy ? ' disabled' : ''}>${view?.linkBusy ? 'Linking...' : 'Verify & link bot'}</button>
        <small>Paste the token Arena generated for that bot. It is sent once to prove control, then cleared from this form.</small>
      </form>
    </section>`;
  }

  function renderPanel(rawSnapshot, options) {
    const snapshot = normalizeSnapshot(rawSnapshot);
    const view = options && typeof options === 'object' ? options : {};
    const owner = accountLabel(snapshot.account);
    return `<div class="cosmetic-account-summary">
      <div>
        <div class="cosmetic-kicker">Verified account</div>
        <h2>${escapeHTML(owner)}</h2>
        <p>Your Arena subscription, and which linked bot wears what. The subscription stays with this account even if a bot API key is rotated, revoked, or lost. Link a bot and manage API keys from the Profile tab.</p>
      </div>
      <span class="verified-email-badge">Account verified</span>
    </div>
    ${view.error ? `<div class="tip warn" role="alert"><b>Could not update cosmetics:</b> ${escapeHTML(view.error)}</div>` : ''}
    ${view.notice ? `<div class="tip good" role="status"><b>Saved:</b> ${escapeHTML(view.notice)}</div>` : ''}
    ${renderSubscriptionCard(snapshot, view)}
    ${renderCollection(snapshot, view)}
    ${renderShopLink()}`;
  }

  root.ArenaAccountCosmetics = Object.freeze({
    COLLECTION_PAGE_SIZE,
    accountLabel,
    accountRoute,
    collectionItems,
    equipIntent,
    equippedLoadout,
    escapeHTML,
    hasVerifiedAccount,
    isVerifiedAccount,
    keyCreateIntent,
    keyRevokeIntent,
    normalizeCatalog,
    normalizeKeyCollection,
    normalizeSession,
    normalizeSnapshot,
    normalizeSubscription,
    previewModel,
    renderAccountKeys,
    renderLinkedBots,
    renderPanel,
    requestHeaders,
    slotLabel,
    subscriptionURL,
  });
})(typeof globalThis !== 'undefined' ? globalThis : window);
