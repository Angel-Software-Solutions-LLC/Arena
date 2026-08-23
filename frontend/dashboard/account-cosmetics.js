(function attachArenaAccountCosmetics(root) {
  'use strict';

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
   * configuration mistake. An operator who sets the handoff to a path, or
   * leaves a placeholder in it, gets a Buy control that does nothing rather
   * than one that navigates somewhere unintended. `javascript:` is refused by
   * the same rule that refuses everything else: only https is an address.
   */
  function httpsURL(value) {
    const raw = cleanText(value);
    if (!raw.startsWith('https://') || raw.length <= 'https://'.length) return '';
    // Whitespace and quotes cannot appear in a URL that was not built wrong,
    // and both are how a value ends up escaping the attribute it is written
    // into. Refuse rather than encode: there is no correct handoff that needs
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
        name: cleanText(rawAccount.name || rawAccount.display_name),
      },
    };
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
      slot: cleanText(item.slot),
      asset_key: cleanText(item.asset_key),
      rarity: cleanText(item.rarity) || 'common',
      is_active: item.is_active !== false,
    };
  }

  function normalizeLicense(raw) {
    const license = raw && typeof raw === 'object' ? raw : {};
    const item = normalizeItem(license.item || license.cosmetic || {
      id: license.cosmetic_id,
      name: license.name,
      description: license.description,
      slot: license.slot,
      asset_key: license.asset_key,
      rarity: license.rarity,
    });
    const assignment = license.assignment && typeof license.assignment === 'object'
      ? license.assignment
      : {};
    return {
      id: cleanText(license.id || license.license_id || license.entitlement_id),
      cosmetic_id: cleanText(license.cosmetic_id || item.id),
      item,
      assigned_bot_id: cleanText(license.assigned_bot_id || assignment.bot_id),
      assigned_bot_name: cleanText(license.assigned_bot_name || assignment.bot_name),
      assigned_at: cleanText(license.assigned_at || assignment.assigned_at),
      equipped: license.equipped === true || license.is_equipped === true,
      equipped_bot_id: cleanText(license.equipped_bot_id),
      status: cleanText(license.status).toLowerCase() || 'active',
    };
  }

  function normalizeSnapshot(payload) {
    const source = payload && typeof payload === 'object' ? payload : {};
    const session = normalizeSession({
      authenticated: true,
      account: source.account || {},
    });
    const bots = Array.isArray(source.bots) ? source.bots.map(normalizeBot).filter(bot => bot.id) : [];
    const licensesSource = Array.isArray(source.licenses)
      ? source.licenses
      : (Array.isArray(source.entitlements) ? source.entitlements : []);
    return {
      account: session.account,
      bots,
      licenses: licensesSource.map(normalizeLicense).filter(license => license.id),
      checkout_enabled: source.checkout_enabled === true,
      subscription: source.subscription && typeof source.subscription === 'object'
        ? normalizeSubscription(source.subscription)
        : null,
      subscription_offer: normalizeSubscriptionOffer(source.subscription_offer),
      membership: normalizeMembership(source.membership),
    };
  }

  // An admin-granted "All Access" membership is a distinct entitlement from
  // the Stripe subscription: it grants the same catalog access without a
  // recurring charge. Only "active" is ever surfaced -- expired/revoked
  // memberships have already been synced away from the license list, so
  // showing them here would just contradict the license grid below.
  function normalizeMembership(payload) {
    const source = payload && typeof payload === 'object' ? payload : null;
    if (!source || cleanText(source.status).toLowerCase() !== 'active') return null;
    return {
      id: cleanText(source.id),
      status: 'active',
      granted_at: cleanText(source.granted_at),
      expires_at: cleanText(source.expires_at),
      note: cleanText(source.note),
    };
  }

  function currentPreviewState(snapshot, botID) {
    const loadout = emptyPreviewLoadout();
    const licenses = {};
    if (!snapshot.bots.some(bot => bot.id === botID)) return {loadout, licenses};

    for (const license of snapshot.licenses) {
      const slot = license.item.slot;
      if (!PREVIEW_SLOTS.includes(slot) || licenses[slot]) continue;
      const equippedBotID = cleanText(license.equipped_bot_id || license.assigned_bot_id);
      const assetKey = previewAssetKey(license.item);
      if (license.status !== 'active' || license.item.is_active !== true || license.equipped !== true ||
          equippedBotID !== botID || !assetKey) continue;
      loadout[slot] = assetKey;
      licenses[slot] = license;
    }
    return {loadout, licenses};
  }

  // Returns the server-authoritative visual loadout for one linked bot. A
  // loadout entry can only come from an active, equipped license in the
  // account snapshot; caller-provided asset keys are never accepted here.
  function equippedLoadout(rawSnapshot, rawBotID) {
    const snapshot = normalizeSnapshot(rawSnapshot);
    const botID = cleanText(rawBotID);
    return currentPreviewState(snapshot, botID).loadout;
  }

  // Builds a visual-only staged loadout. stagedBySlot must map a known slot to
  // an owned license ID. Each choice is resolved again from the latest account
  // snapshot, which prevents stale, inactive, wrong-slot, or arbitrary asset
  // values from reaching the renderer or being mistaken for equip authority.
  function previewModel(rawSnapshot, rawBotID, rawStagedBySlot) {
    const snapshot = normalizeSnapshot(rawSnapshot);
    const botID = cleanText(rawBotID);
    const bot = snapshot.bots.find(entry => entry.id === botID) || null;
    const current = currentPreviewState(snapshot, bot?.id || '');
    const previewLoadout = {...current.loadout};
    const stagedBySlot = {};
    const stagedLicenses = {};
    const requested = rawStagedBySlot && typeof rawStagedBySlot === 'object' && !Array.isArray(rawStagedBySlot)
      ? rawStagedBySlot
      : {};

    if (bot) {
      for (const slot of PREVIEW_SLOTS) {
        const licenseID = cleanText(requested[slot]);
        if (!licenseID) continue;
        const license = snapshot.licenses.find(entry => entry.id === licenseID);
        const assetKey = previewAssetKey(license?.item);
        if (!license || license.status !== 'active' || license.item.is_active !== true ||
            license.item.slot !== slot || !assetKey) continue;
        stagedBySlot[slot] = license.id;
        stagedLicenses[slot] = license;
        previewLoadout[slot] = assetKey;
      }
    }

    const slots = {};
    for (const slot of PREVIEW_SLOTS) {
      const stagedLicense = stagedLicenses[slot] || null;
      const currentLicense = current.licenses[slot] || null;
      slots[slot] = {
        currentLicense,
        stagedLicense,
        assetKey: previewLoadout[slot],
        canEquip: Boolean(
          bot?.key_is_active && stagedLicense && stagedLicense.assigned_bot_id === bot.id &&
          !(stagedLicense.equipped && (!stagedLicense.equipped_bot_id || stagedLicense.equipped_bot_id === bot.id)),
        ),
      };
    }

    return {
      bot,
      currentLoadout: current.loadout,
      previewLoadout,
      currentLicenses: current.licenses,
      stagedLicenses,
      stagedBySlot,
      slots,
      hasStaged: Object.keys(stagedBySlot).length > 0,
      isDirty: PREVIEW_SLOTS.some(slot => previewLoadout[slot] !== current.loadout[slot]),
    };
  }

  function normalizeSubscriptionOffer(payload) {
    const source = payload && typeof payload === 'object' ? payload : {};
    const rawPrice = Number(source.price_cents);
    const rawLimit = Number(source.max_api_keys);
    return {
      enabled: source.enabled === true,
      price_cents: Number.isFinite(rawPrice) && rawPrice >= 0 ? Math.round(rawPrice) : 1999,
      currency: cleanText(source.currency).toUpperCase() || 'USD',
      interval: cleanText(source.interval).toLowerCase() || 'month',
      includes_future_sets: source.includes_future_sets === true,
      max_api_keys: Number.isFinite(rawLimit) && rawLimit > 0 ? Math.min(5, Math.floor(rawLimit)) : 5,
    };
  }

  function normalizeSubscription(payload) {
    const source = payload && typeof payload === 'object' ? payload : {};
    const offer = normalizeSubscriptionOffer({...source, enabled: true});
	const checkoutPresentation = cleanText(source.checkout_presentation).toLowerCase();
    return {
      id: cleanText(source.id),
      status: cleanText(source.status).toLowerCase() || 'unknown',
      has_access: source.has_access === true,
	  terminal: source.terminal === true,
      cancel_at_period_end: source.cancel_at_period_end === true,
      current_period_end: cleanText(source.current_period_end),
      can_manage: source.can_manage === true,
		checkout_presentation: ['embedded', 'hosted'].includes(checkoutPresentation) ? checkoutPresentation : '',
      price_cents: offer.price_cents,
      currency: offer.currency,
      interval: offer.interval,
      includes_future_sets: offer.includes_future_sets,
      max_api_keys: offer.max_api_keys,
      created_at: cleanText(source.created_at),
      updated_at: cleanText(source.updated_at),
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

  function normalizeCatalog(payload) {
    const source = payload && typeof payload === 'object' ? payload : {};
    return {
      checkout_enabled: source.checkout_enabled === true,
      /*
       * Where buying happens, when it does not happen here.
       *
       * Only ever an absolute https URL from the server, and read as such: a
       * relative or javascript: value would be a destination this page then
       * navigates to, so anything that is not plainly an https origin is
       * treated as absent rather than sanitised into something plausible.
       */
      purchase_handoff_url: httpsURL(source.purchase_handoff_url),
      categories: Array.isArray(source.categories) ? source.categories : [],
      items: Array.isArray(source.items) ? source.items : [],
      packs: Array.isArray(source.packs) ? source.packs.filter(pack => pack && typeof pack === 'object') : [],
      subscription_offer: normalizeSubscriptionOffer(source.subscription_offer),
    };
  }

  function assignmentIntent(snapshot, licenseID, botID) {
    const state = normalizeSnapshot(snapshot);
    if (!state.account.email || state.account.email_verified !== true) {
      return {ok: false, reason: 'verified-email-required'};
    }
    const license = state.licenses.find(entry => entry.id === licenseID);
    if (!license) return {ok: false, reason: 'license-not-found'};
    if (license.status !== 'active') return {ok: false, reason: 'license-inactive'};
    const bot = state.bots.find(entry => entry.id === botID);
    if (!bot) return {ok: false, reason: 'bot-not-linked'};
    if (!bot.key_is_active) return {ok: false, reason: 'bot-key-inactive'};
    if (license.assigned_bot_id === bot.id) {
      return {ok: false, reason: 'already-assigned'};
    }
    return {
      ok: true,
      kind: license.assigned_bot_id ? 'move' : 'assign',
      license_id: license.id,
      bot_id: bot.id,
      previous_bot_id: license.assigned_bot_id || null,
    };
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
      checkout: '/account/cosmetics/checkout',
      orders: '/account/cosmetics/orders',
      orderCheckout: `/account/cosmetics/orders/${encoded}/checkout`,
      subscriptionCheckout: '/account/cosmetics/subscription/checkout',
      subscriptionPortal: '/account/cosmetics/subscription/portal',
      keys: '/account/keys',
      key: `/account/keys/${encoded}`,
      bots: '/account/bots',
      bot: `/account/bots/${encoded}`,
      equip: `/account/bots/${encoded}/cosmetics`,
      assignment: `/account/cosmetic-licenses/${encoded}/assignment`,
    };
    if (!Object.hasOwn(routes, name)) throw new Error(`unknown account route: ${name}`);
    return routes[name];
  }

  function checkoutIntent(rawCatalog, packID) {
    const catalog = normalizeCatalog(rawCatalog);
    const normalizedID = cleanText(packID);
    /*
     * The pack still has to exist and still has to be for sale, handoff or
     * not. Sending somebody to another site to buy a thing this Arena has
     * retired would be a worse failure than refusing here, because the refusal
     * would arrive after they had left.
     */
    if (!catalog.checkout_enabled && !catalog.purchase_handoff_url) {
      return {ok: false, reason: 'checkout-disabled'};
    }
    if (!normalizedID) return {ok: false, reason: 'pack-not-found'};
    const pack = catalog.packs.find(entry => cleanText(entry.id) === normalizedID);
    if (!pack) return {ok: false, reason: 'pack-not-found'};
    if (pack.is_purchasable !== true) return {ok: false, reason: 'pack-not-purchasable'};
    if (catalog.purchase_handoff_url) {
      return {ok: true, kind: 'handoff', url: catalog.purchase_handoff_url};
    }
    return {
      ok: true,
      kind: 'checkout',
      path: accountRoute('checkout'),
      body: {pack_id: normalizedID, quantity: 1},
    };
  }

  function subscriptionIntent(rawOffer, rawSubscription, rawHandoffURL) {
    const offer = normalizeSubscriptionOffer(rawOffer);
    const subscription = rawSubscription && typeof rawSubscription === 'object'
      ? normalizeSubscription(rawSubscription)
      : null;
    /*
     * Starting All Access moves to the Angel account. Managing one that
     * already exists does not, and the difference matters: a subscription
     * started before the switch is live money in Arena's own Stripe account,
     * and Arena's portal is the only place its holder can cancel it. Sending
     * them to an Angel account that does not hold that subscription would
     * leave a recurring charge nobody can reach.
     */
    const handoff = httpsURL(rawHandoffURL);
    const checkoutStatuses = new Set(['created', 'checkout_pending', 'canceled', 'expired']);
    if (subscription?.can_manage) {
      return {ok: true, kind: 'portal', path: accountRoute('subscriptionPortal')};
    }
    if (handoff) {
      return {ok: true, kind: 'handoff', url: handoff};
    }
    if (subscription && checkoutStatuses.has(subscription.status)) {
      if (!offer.enabled) return {ok: false, reason: 'subscription-disabled'};
      return {ok: true, kind: 'checkout', path: accountRoute('subscriptionCheckout')};
    }
    if (subscription) return {ok: false, reason: 'subscription-unmanageable'};
    if (!offer.enabled) return {ok: false, reason: 'subscription-disabled'};
    return {ok: true, kind: 'checkout', path: accountRoute('subscriptionCheckout')};
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

  function renderBotOption(bot, currentlyAssigned) {
    const disabled = currentlyAssigned || !bot.key_is_active;
    const suffix = currentlyAssigned ? ' (currently assigned)' : (!bot.key_is_active ? ' (key inactive)' : '');
    const identity = bot.key_prefix ? ` - ${bot.key_prefix}...` : ` - ${bot.id.slice(0, 8)}`;
    return `<option value="${escapeHTML(bot.id)}"${disabled ? ' disabled' : ''}>${escapeHTML(bot.name + identity)}${suffix}</option>`;
  }

  function renderLicense(license, snapshot, busyLicenseID) {
    const assignedBot = snapshot.bots.find(bot => bot.id === license.assigned_bot_id);
    const assignedName = assignedBot?.name || license.assigned_bot_name || '';
    const assignedIdentity = assignedBot
      ? `${assignedName} (${assignedBot.key_prefix ? assignedBot.key_prefix + '...' : assignedBot.id.slice(0, 8)})`
      : assignedName;
    const isBusy = busyLicenseID === license.id;
    const activeLicense = license.status === 'active';
    const assignableBots = snapshot.bots.filter(bot => bot.id !== license.assigned_bot_id && bot.key_is_active);
    const options = snapshot.bots.map(bot => renderBotOption(bot, bot.id === license.assigned_bot_id)).join('');
    const assignedCopy = assignedIdentity
      ? `Assigned to <strong>${escapeHTML(assignedIdentity)}</strong>`
      : 'Not assigned to a bot';
    const actionLabel = license.assigned_bot_id ? 'Move to bot' : 'Assign to bot';
    const assignedBotActive = assignedBot?.key_is_active === true;
    const equipped = license.equipped === true && (!license.equipped_bot_id || license.equipped_bot_id === license.assigned_bot_id);
    const previewable = activeLicense && license.item.is_active && snapshot.bots.length > 0;
    const equippedCopy = !activeLicense
      ? `License ${escapeHTML(license.status)}; it cannot be assigned or equipped`
      : equipped
      ? `Equipped on <strong>${escapeHTML(assignedIdentity || 'linked bot')}</strong>`
      : (license.assigned_bot_id ? 'Assigned, not equipped' : 'Assign this license before equipping it');
    const statusLabel = activeLicense ? 'Account owned' : license.status.charAt(0).toUpperCase() + license.status.slice(1);

    return `<article class="cosmetic-license" data-license-id="${escapeHTML(license.id)}">
      <div class="cosmetic-license-head">
        <div>
          <div class="cosmetic-kicker">${escapeHTML(slotLabel(license.item.slot))} - ${escapeHTML(license.item.rarity)}</div>
          <h3>${escapeHTML(license.item.name)}</h3>
        </div>
        <span class="ownership-badge${activeLicense ? '' : ' inactive'}">${escapeHTML(statusLabel)}</span>
      </div>
      <p>${escapeHTML(license.item.description || 'Visual customization only. No gameplay advantage.')}</p>
      <div class="cosmetic-assignment${license.assigned_bot_id ? ' assigned' : ''}">${assignedCopy}</div>
      <div class="cosmetic-equip-state${equipped ? ' equipped' : ''}">${equippedCopy}</div>
      <div class="cosmetic-license-actions">
        ${activeLicense && license.item.is_active ? `<button class="sm cosmetic-preview" type="button" data-license-preview="${escapeHTML(license.id)}" aria-label="Preview ${escapeHTML(license.item.name)} on the selected bot"${previewable && !isBusy ? '' : ' disabled'}>${snapshot.bots.length ? 'Preview' : 'Link a bot to preview'}</button>` : ''}
        <select data-license-target="${escapeHTML(license.id)}" aria-label="Bot for ${escapeHTML(license.item.name)}"${activeLicense && assignableBots.length && !isBusy ? '' : ' disabled'}>
          ${snapshot.bots.length ? `<option value="">${license.assigned_bot_id ? 'Choose another bot...' : 'Choose a linked bot...'}</option>${options}` : '<option value="">Link a bot first</option>'}
        </select>
        <button class="sm" data-license-assign="${escapeHTML(license.id)}" disabled>${isBusy ? 'Saving...' : actionLabel}</button>
        ${activeLicense && license.assigned_bot_id && !equipped ? `<button class="sm cosmetic-equip" data-license-equip="${escapeHTML(license.id)}"${isBusy || !license.item.is_active || !assignedBotActive ? ' disabled' : ''}>${isBusy ? 'Saving...' : (assignedBotActive ? 'Equip on bot' : 'Bot key inactive')}</button>` : ''}
        ${activeLicense && license.assigned_bot_id ? `<button class="sm danger" data-license-unassign="${escapeHTML(license.id)}"${isBusy ? ' disabled' : ''}>Remove from bot</button>` : ''}
      </div>
      <div class="cosmetic-license-id">License ${escapeHTML(license.id)}</div>
    </article>`;
  }

  function formatPrice(item) {
    if (item?.is_free === true) return 'Free';
    const rawCents = Number(item?.price_cents || 0);
    const cents = Number.isFinite(rawCents) && rawCents >= 0 ? Math.round(rawCents) : 0;
    const currency = cleanText(item?.currency) || 'USD';
    try {
      return new Intl.NumberFormat(undefined, {style: 'currency', currency}).format(cents / 100);
    } catch (_) {
      return `$${(cents / 100).toFixed(2)}`;
    }
  }

  function subscriptionPeriodLabel(rawTime) {
    const raw = cleanText(rawTime);
    const date = new Date(raw);
    if (!raw || Number.isNaN(date.getTime())) return '';
    return date.toLocaleDateString(undefined, {year: 'numeric', month: 'short', day: 'numeric'});
  }

  function renderAllAccessPlan(snapshot, view) {
    const catalogOffer = view.catalog ? normalizeCatalog(view.catalog).subscription_offer : null;
    const offer = snapshot.subscription_offer.enabled ? snapshot.subscription_offer : (catalogOffer || snapshot.subscription_offer);
    const subscription = snapshot.subscription;
    const membership = snapshot.membership;
    const intent = subscriptionIntent(offer, subscription, view.catalog?.purchase_handoff_url);
    const periodEnd = subscriptionPeriodLabel(subscription?.current_period_end);
    const membershipEnd = subscriptionPeriodLabel(membership?.expires_at);
    let status = 'Available';
    let statusClass = '';
    let grantNote = '';
    if (subscription?.has_access) {
      status = subscription.cancel_at_period_end
        ? (periodEnd ? `Access active until ${periodEnd}` : 'Access active until period end')
        : 'Access active';
      statusClass = ' is-active';
    } else if (membership) {
      // A staff-granted membership materializes the same per-item licenses a
      // paid subscription would, but it lives in a separate table with no
      // Stripe record -- without this branch the status above would say
      // "Available" even though the account already has every set.
      status = membershipEnd ? `Access active until ${membershipEnd}` : 'Access active';
      statusClass = ' is-active';
      grantNote = 'Granted by Arena staff. Subscribing keeps access going after the grant ends.';
    } else if (subscription) {
      if (subscription.status === 'billing_mismatch') status = 'Billing needs attention';
      else if (['created', 'checkout_pending'].includes(subscription.status)) status = 'Checkout pending';
      else if (['past_due', 'unpaid', 'paused'].includes(subscription.status)) status = 'Access paused';
      else status = 'Access ended';
      statusClass = ' is-warning';
    }
    const action = intent.ok && intent.kind === 'portal'
      ? '<button type="button" class="sm all-access-action" data-subscription-portal>Manage subscription</button>'
      : intent.ok && intent.kind === 'handoff'
        ? `<button type="button" class="sm all-access-action" data-subscription-checkout>${membership ? 'Keep access in your Angel account' : 'Subscribe in your Angel account'}</button>`
        : intent.ok
          ? `<button type="button" class="sm all-access-action" data-subscription-checkout>${membership ? 'Subscribe to keep access' : 'Subscribe for $19.99 / month'}</button>`
          : '<span class="all-access-unavailable">Subscription checkout is not open yet</span>';
    const pending = view.subscriptionState?.status === 'pending';
    const feedback = view.subscriptionState?.status === 'error'
      ? `<p class="all-access-feedback is-error" role="alert">${escapeHTML(view.subscriptionState.message || 'Subscription management is temporarily unavailable.')}</p>`
      : '';
    return `<section class="all-access-plan" aria-labelledby="all-access-title">
      <div class="all-access-copy">
        <div class="cosmetic-kicker">Monthly collection pass</div>
        <h2 id="all-access-title">All Access</h2>
        <p>Every current and future cosmetic set, full-body skin, and trail, with up to 5 active API keys.</p>
        <p class="all-access-removal">Cancellation keeps access through the paid period. When service ends, subscription cosmetics are removed from your account and any bots using them.</p>
        ${grantNote ? `<p class="all-access-grant-note">${escapeHTML(grantNote)}</p>` : ''}
      </div>
      <div class="all-access-offer">
        <span class="all-access-status${statusClass}">${escapeHTML(status)}</span>
        <strong>${escapeHTML(formatPrice(offer))}<small> / ${escapeHTML(offer.interval || 'month')}</small></strong>
        <div${pending ? ' aria-busy="true"' : ''}>${pending ? '<button type="button" class="sm all-access-action" disabled>Opening...</button>' : action}</div>
        ${feedback}
      </div>
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
      <p class="cosmetic-rule">Keys are generated and stored against this verified email account. You can keep up to 5 active keys and revoke any one without affecting purchases.</p>
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

  function shopSwatch(pack) {
    const firstItem = Array.isArray(pack?.items) ? pack.items[0] : null;
    const helper = root.ArenaCosmeticThemes;
    if (!helper || typeof helper.swatchStyle !== 'function') return '';
    return helper.swatchStyle(cleanText(firstItem?.asset_key));
  }

  function renderShopPack(pack, catalog, checkoutState) {
    const packID = cleanText(pack.id);
    const pending = checkoutState?.status === 'pending' && checkoutState.packID === packID;
    const handoff = catalog.purchase_handoff_url;
    const purchasable = (catalog.checkout_enabled || Boolean(handoff)) && pack.is_purchasable === true;
    const isTrail = pack.category_id === 'trails' && pack.items?.length === 1 && pack.items[0]?.slot === 'trail';
    const items = Array.isArray(pack.items) ? pack.items.slice(0, 3) : [];
    const contents = items.length
      ? items.map(item => `<span>${escapeHTML(item.name || item.id || 'Cosmetic')}</span>`).join('')
      : `<span>${isTrail ? 'Individual trail' : 'Coordinated set'}</span>`;
    const swatch = shopSwatch(pack);
    const swatchAttribute = swatch ? ` style="background:${escapeHTML(swatch)}"` : '';
    // Same control either way, because it is the same act. What changes is
    // what it says will happen next, so nobody is surprised by an address bar
    // that is no longer Arena's.
    const buyLabel = pending
      ? (handoff ? 'Opening your Angel account...' : 'Opening checkout...')
      : `Buy ${escapeHTML(formatPrice(pack))}${handoff ? ' in your Angel account' : ''}`;
    const action = purchasable
      ? `<button type="button" class="sm cosmetic-shop-buy" data-pack-checkout="${escapeHTML(packID)}"${pending ? ' disabled' : ''}>${buyLabel}</button>`
      : '<span class="cosmetic-shop-state">Checkout coming soon</span>';
    return `<article class="cosmetic-shop-pack" data-shop-pack="${escapeHTML(packID)}">
      <div class="cosmetic-shop-swatch" aria-hidden="true"${swatchAttribute}></div>
      <div class="cosmetic-shop-pack-copy">
        <div class="cosmetic-kicker">${isTrail ? 'Individual trail' : 'Coordinated set'}</div>
        <h3>${escapeHTML(pack.name || packID || (isTrail ? 'Arena trail' : 'Arena set'))}</h3>
        <p>${escapeHTML(pack.description || (isTrail
          ? 'A presentation-only movement trail with no gameplay advantage.'
          : 'Three presentation-only cosmetics with no gameplay advantage.'))}</p>
        <div class="cosmetic-shop-contents">${contents}</div>
      </div>
      <div class="cosmetic-shop-offer"><strong>${escapeHTML(formatPrice(pack))}</strong>${action}</div>
    </article>`;
  }

  // The Dashboard shows only what an account owns; browsing and buying live
  // in the Shop (frontend/shop/) so there is exactly one place to shop. The
  // one exception is a purchase already in flight: the Shop's "Buy" link
  // hands off here with the chosen pack's id (see dashboardPurchasePath in
  // js/cosmetics-shop.js) so the customer lands on a single ready-to-pay
  // card instead of re-finding the same item in a duplicate catalog.
  function renderPendingPurchase(view) {
    const pendingPackID = cleanText(view.pendingPackID);
    if (!pendingPackID) return '';
    const catalog = view.catalog ? normalizeCatalog(view.catalog) : null;
    const checkoutState = view.checkoutState || {status: 'idle', packID: '', message: ''};
    let feedback = '';
    if (checkoutState.status === 'success') {
      feedback = `<div class="tip" role="status"><b>Checkout returned.</b> Payment is still processing. New licenses appear only after Arena verifies Stripe's signed payment event.</div>`;
    } else if (checkoutState.status === 'reconciling') {
      feedback = `<div class="tip" role="status"><b>Checkout is reconciling.</b> ${escapeHTML(checkoutState.message || "Arena is confirming Stripe's signed payment event before changing your collection.")}</div>`;
    } else if (checkoutState.status === 'paused') {
      feedback = `<div class="tip" role="status"><b>Checkout saved.</b> ${escapeHTML(checkoutState.message || 'Resume the same Stripe session from Recent purchases when you are ready.')}</div>`;
    } else if (checkoutState.status === 'cancelled') {
      feedback = `<div class="tip" role="status"><b>Checkout cancelled.</b> Your collection was not changed.</div>`;
    } else if (checkoutState.status === 'error') {
      feedback = `<div class="tip warn" role="alert"><b>Checkout could not start:</b> ${escapeHTML(checkoutState.message || 'Try again in a moment.')}</div>`;
    } else if (checkoutState.status === 'disabled') {
      feedback = `<div class="tip" role="status"><b>Checkout is not open yet.</b> Return to the Shop and try again once sales are enabled.</div>`;
    }
    const head = `<div class="cosmetic-inventory-head"><div><div class="cosmetic-kicker">From the Shop</div><h2 id="cosmetic-pending-title">Complete your purchase</h2></div></div>`;
    if (view.catalogError) {
      return `<section class="cosmetic-shop cosmetic-pending-purchase" aria-labelledby="cosmetic-pending-title">
        ${head}${feedback}<div class="tip warn" role="alert"><b>Could not load that item:</b> ${escapeHTML(view.catalogError)}</div>
      </section>`;
    }
    if (!catalog) {
      return `<section class="cosmetic-shop cosmetic-pending-purchase" aria-labelledby="cosmetic-pending-title">
        ${head}${feedback}<div class="cosmetic-loading">Loading your selection...</div>
      </section>`;
    }
    const pack = catalog.packs.find(entry => cleanText(entry.id) === pendingPackID);
    if (!pack) {
      return `<section class="cosmetic-shop cosmetic-pending-purchase" aria-labelledby="cosmetic-pending-title">
        ${head}${feedback}<div class="cosmetic-empty cosmetic-empty-inventory">That item is no longer available in the Shop.</div>
      </section>`;
    }
    return `<section class="cosmetic-shop cosmetic-pending-purchase" aria-labelledby="cosmetic-pending-title">
      ${head}
      <p class="cosmetic-rule">Buying a pack grants one license for every included item. Each purchased item copy can be assigned to one bot at a time; items from the same pack can be assigned to different bots.</p>
      ${feedback}
      <div class="cosmetic-shop-grid">${renderShopPack(pack, catalog, checkoutState)}</div>
    </section>`;
  }

  function renderShopLink() {
    return `<section class="cosmetic-shop cosmetic-shop-link" aria-labelledby="cosmetic-shop-link-title">
      <div class="cosmetic-inventory-head">
        <div><div class="cosmetic-kicker">Looking for more?</div><h2 id="cosmetic-shop-link-title">Browse the Shop</h2></div>
      </div>
      <p class="cosmetic-rule">Preview and buy new sets, full-body skins, and trails on your linked bots before you commit.</p>
      <button type="button" class="sm" data-open-shop>Open the Shop</button>
    </section>`;
  }

  function formatOrderUSD(rawCents) {
    const amount = Number(rawCents);
    const cents = Number.isFinite(amount) && amount >= 0 ? Math.round(amount) : 0;
    try {
      return new Intl.NumberFormat('en-US', {style: 'currency', currency: 'USD'}).format(cents / 100);
    } catch (_) {
      return `$${(cents / 100).toFixed(2)}`;
    }
  }

  function orderStatusMeta(rawStatus) {
    const status = cleanText(rawStatus).toLowerCase() || 'created';
    const labels = {
      created: 'Checkout pending',
      checkout_pending: 'Checkout pending',
      processing: 'Processing',
      paid: 'Paid',
      refund_review: 'Refund review',
      refunded: 'Refunded',
      disputed: 'Disputed',
      expired: 'Expired',
      payment_failed: 'Failed',
      failed: 'Failed',
    };
    const warnings = new Set(['refund_review', 'refunded', 'disputed', 'expired', 'payment_failed', 'failed']);
    return {
      status,
      label: labels[status] || 'Unknown',
      className: status === 'paid' ? ' is-paid' : (warnings.has(status) ? ' is-warning' : ''),
    };
  }

  function orderCreatedTime(rawTime) {
    const raw = cleanText(rawTime);
    const date = new Date(raw);
    if (!raw || Number.isNaN(date.getTime())) return {iso: '', label: 'Time unavailable'};
    return {
      iso: date.toISOString(),
      label: date.toLocaleString(undefined, {
        year: 'numeric', month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit',
      }),
    };
  }

  function renderPurchaseOrder(rawOrder, busyOrderID) {
    const order = rawOrder && typeof rawOrder === 'object' ? rawOrder : {};
    const status = orderStatusMeta(order.status);
    const rawQuantity = Number(order.quantity);
    const quantity = Number.isFinite(rawQuantity) && rawQuantity >= 0 ? Math.floor(rawQuantity) : 0;
    const rawFulfilled = Number(order.fulfilled_license_count);
    const fulfilled = Number.isFinite(rawFulfilled) && rawFulfilled >= 0 ? Math.floor(rawFulfilled) : 0;
    const orderID = cleanText(order.id) || 'Unknown order';
	const checkoutSessionID = cleanText(order.checkout_session_id);
	const checkoutPresentation = cleanText(order.checkout_presentation).toLowerCase();
	const attachedSession = status.status === 'checkout_pending' && Boolean(checkoutSessionID);
	const retryableReservation = ['created', 'payment_failed'].includes(status.status) && !checkoutSessionID &&
		['embedded', 'hosted'].includes(checkoutPresentation);
	const resumable = attachedSession || retryableReservation;
    const resumeBusy = resumable && cleanText(busyOrderID) === orderID;
	const actionLabel = retryableReservation ? 'Retry checkout' : 'Resume checkout';
	const busyLabel = retryableReservation ? 'Retrying...' : 'Reopening...';
	const actionCopy = retryableReservation
		? 'Retries this same reserved Stripe checkout.'
		: 'Reopens this same Stripe session.';
    const packName = cleanText(order.pack_name || order.pack_id) || 'Unknown pack';
    const created = orderCreatedTime(order.created_at);
    const createdHTML = created.iso
      ? `<time datetime="${escapeHTML(created.iso)}">${escapeHTML(created.label)}</time>`
      : escapeHTML(created.label);
    return `<article class="cosmetic-purchase" data-purchase-order="${escapeHTML(orderID)}" data-order-status="${escapeHTML(status.status)}">
      <div class="cosmetic-purchase-head">
        <div><div class="cosmetic-kicker">Order <code>${escapeHTML(orderID)}</code></div><h3>${escapeHTML(packName)}</h3></div>
        <span class="cosmetic-purchase-status${status.className}">${escapeHTML(status.label)}</span>
      </div>
      <div class="cosmetic-purchase-facts"><span>Quantity ${quantity}</span><span>${fulfilled} ${fulfilled === 1 ? 'license' : 'licenses'} fulfilled</span>${createdHTML}</div>
      <div class="cosmetic-purchase-money">
        <span>Expected <strong>${escapeHTML(formatOrderUSD(order.expected_subtotal_cents))}</strong></span>
        <span>Received <strong>${escapeHTML(formatOrderUSD(order.amount_received_cents))}</strong></span>
        <span>Refunded <strong>${escapeHTML(formatOrderUSD(order.amount_refunded_cents))}</strong></span>
      </div>
      ${resumable ? `<div class="cosmetic-purchase-actions"><button type="button" class="sm" data-order-resume="${escapeHTML(orderID)}"${resumeBusy ? ' disabled' : ''}>${resumeBusy ? busyLabel : actionLabel}</button><span>${actionCopy}</span></div>` : ''}
    </article>`;
  }

  function renderRecentPurchases(view) {
    let body = '';
    if (view.ordersError) {
      body = `<div class="tip warn" role="alert"><b>Recent purchases unavailable:</b> ${escapeHTML(view.ordersError)} Owned cosmetics and the shop are unaffected.</div>`;
    } else if (!Array.isArray(view.orders)) {
      body = '<div class="cosmetic-loading" aria-busy="true">Loading recent purchases...</div>';
    } else if (!view.orders.length) {
      body = '<div class="cosmetic-empty cosmetic-empty-inventory">No purchases yet.</div>';
    } else {
      body = `<div class="cosmetic-purchase-list">${view.orders.slice(0, 20).map(order => renderPurchaseOrder(order, view.busyOrderID)).join('')}</div>`;
    }
    return `<section class="cosmetic-purchases" aria-labelledby="cosmetic-purchases-title">
      <div class="cosmetic-inventory-head">
        <div><div class="cosmetic-kicker">Account ledger</div><h2 id="cosmetic-purchases-title">Recent purchases</h2></div>
        <span>Latest 20</span>
      </div>
      <p class="cosmetic-rule">${httpsURL(view.catalog?.purchase_handoff_url)
        ? 'Orders Arena took payment for before buying moved to your Angel account. Anything bought since is in your Angel account\'s own receipts.'
        : "Statuses come from Arena's signed payment ledger. Returning from checkout does not mark an order paid."}</p>
      ${body}
    </section>`;
  }

  // Bot linking lives on the Profile tab now (it's account/credential
  // management, not a cosmetics concern), rendered separately by index.html
  // via window.ArenaAccountCosmetics.renderLinkedBots -- kept here because
  // the underlying data (snapshot.bots) is still fetched alongside cosmetic
  // inventory, not through account-profile.js's own data flow.
  function renderLinkedBots(snapshot, view) {
    const email = snapshot.account.email || 'your verified email';
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
      <p class="cosmetic-rule">Claim a bot you started anonymously by proving its server-issued token. Linking does not transfer cosmetic ownership to the token -- purchases always stay with ${escapeHTML(email)}.</p>
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
    const email = snapshot.account.email || 'your verified email';

    const groups = new Map();
    snapshot.licenses.forEach(license => {
      const slot = license.item.slot || 'other';
      if (!groups.has(slot)) groups.set(slot, []);
      groups.get(slot).push(license);
    });
    const inventory = groups.size
      ? [...groups.entries()].map(([slot, licenses]) => `<section class="cosmetic-group">
          <h3>${escapeHTML(slotLabel(slot))}</h3>
          <div class="cosmetic-license-grid">${licenses.map(license => renderLicense(license, snapshot, view.busyLicenseID)).join('')}</div>
        </section>`).join('')
      : '<div class="cosmetic-empty cosmetic-empty-inventory">No cosmetic licenses are on this account yet.</div>';
    const activeCount = snapshot.licenses.filter(license => license.status === 'active').length;
    const inactiveCount = snapshot.licenses.length - activeCount;
    const licenseSummary = `${activeCount} active${inactiveCount ? ` / ${inactiveCount} inactive` : ''}`;

    /*
     * Where purchases live now, and how to go and get the newest one.
     *
     * Arena reads what somebody owns at the moment they sign in, and holds no
     * credential to ask again later (see customer_entitlements.go on the
     * server for why that is the deliberate choice). So a pack bought in the
     * Angel account a minute ago is genuinely not here yet, and the honest
     * thing is to say so and offer the one action that fixes it rather than
     * let somebody conclude their purchase vanished.
     *
     * The action is a sign-in. On a live Angel session that is one press and
     * a window that closes itself, which is why it can be offered as a
     * refresh rather than as "sign in again".
     */
    const handoffURL = httpsURL(view.catalog?.purchase_handoff_url);
    const collectionAction = handoffURL
      ? `<div class="cosmetic-inventory-actions"><span>${escapeHTML(licenseSummary)}</span>` +
        `<button type="button" class="sm" data-entitlements-refresh${view.entitlementsBusy ? ' disabled' : ''}>` +
        `${view.entitlementsBusy ? 'Reading your account...' : 'Refresh purchases'}</button></div>`
      : `<span>${escapeHTML(licenseSummary)}</span>`;
    const collectionRule = handoffURL
      ? 'Purchases are made and held in your Angel account, and read into Arena when you sign in. Bought something just now? <b>Refresh purchases</b> reads your account again. '
      : '';

    return `<div class="cosmetic-account-summary">
      <div>
        <div class="cosmetic-kicker">Verified owner</div>
        <h2>${escapeHTML(email)}</h2>
        <p>What this account owns, and which linked bot wears each item. Purchases stay with this account even if a bot API key is rotated, revoked, or lost. Link a bot and manage API keys from the Profile tab.</p>
      </div>
      <span class="verified-email-badge">Email verified</span>
    </div>
    ${view.error ? `<div class="tip warn" role="alert"><b>Could not update cosmetics:</b> ${escapeHTML(view.error)}</div>` : ''}
    ${view.notice ? `<div class="tip good" role="status"><b>Saved:</b> ${escapeHTML(view.notice)}</div>` : ''}
    ${renderAllAccessPlan(snapshot, view)}
    ${renderPendingPurchase(view)}
    <section class="cosmetic-inventory">
      <div class="cosmetic-inventory-head">
        <div><div class="cosmetic-kicker">Your collection</div><h2>Cosmetic licenses</h2></div>
        ${collectionAction}
      </div>
      <p class="cosmetic-rule">${collectionRule}Every purchased pack item appears here as its own license, assignable to one linked bot at a time. <b>Assign</b> puts a license on a bot so it's available to that bot; <b>Equip</b> makes it the bot's active look in that slot right now.</p>
      ${inventory}
    </section>
    ${renderShopLink()}
    ${renderRecentPurchases(view)}`;
  }

  root.ArenaAccountCosmetics = Object.freeze({
    accountRoute,
    assignmentIntent,
    checkoutIntent,
    equippedLoadout,
    escapeHTML,
    keyCreateIntent,
    keyRevokeIntent,
    normalizeCatalog,
    normalizeKeyCollection,
    normalizeSession,
    normalizeSnapshot,
    normalizeSubscription,
    normalizeSubscriptionOffer,
    previewModel,
    renderAccountKeys,
    renderLinkedBots,
    renderPanel,
    requestHeaders,
    slotLabel,
    subscriptionIntent,
  });
})(typeof globalThis !== 'undefined' ? globalThis : window);
