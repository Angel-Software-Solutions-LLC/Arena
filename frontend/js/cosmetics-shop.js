'use strict';

import './babylon-runtime.js?v=20260810d';

import { apiPath, appPath } from './paths.js?v=20260710a';
import { CosmeticShopPreview } from './shop-preview.js?v=20260718o';

const PAGE_SIZE = 24;
const SUPPORTED_SLOTS = new Set(['bot_skin', 'weapon_skin', 'attachment', 'trail']);
const SUPPORTED_WEAPONS = new Set(['sword', 'bow', 'spear', 'daggers', 'staff', 'shield', 'grapple']);
const DEFAULT_LOADOUT = Object.freeze({
  bot_skin: 'standard',
  weapon_skin: 'standard',
  attachment: 'none',
  trail: 'standard',
});

const SLOT_LABELS = Object.freeze({
  bot_skin: 'Chassis',
  weapon_skin: 'Weapon finish',
  attachment: 'Attachment',
  trail: 'Trail',
});

export function packItems(pack) {
  return Array.isArray(pack?.items)
    ? pack.items.filter(item => item && typeof item === 'object')
    : [];
}

/** Standalone trail products are separate from coordinated cosmetic sets. */
export function isTrailPack(pack) {
  const items = packItems(pack);
  return pack?.category_id === 'trails'
    || (items.length === 1 && items[0]?.slot === 'trail');
}

/** Singleton products that replace the complete articulated bot silhouette. */
export function isBodyFormPack(pack) {
  const items = packItems(pack);
  return pack?.category_id === 'body-forms'
    || (items.length === 1
      && items[0]?.slot === 'bot_skin'
      && String(items[0]?.asset_key || '').startsWith('body_'));
}

/** Return a sorted copy so catalog order remains the stable featured order. */
export function sortCosmeticPacks(packs, sort = 'featured') {
  const candidates = Array.isArray(packs) ? [...packs] : [];
  const nameOf = pack => String(pack?.name || pack?.id || '');
  const priceOf = pack => Number.isFinite(Number(pack?.price_cents))
    ? Number(pack.price_cents)
    : 0;
  const byName = (left, right) => nameOf(left).localeCompare(nameOf(right), undefined, {
    sensitivity: 'base',
    numeric: true,
  }) || String(left?.id || '').localeCompare(String(right?.id || ''));
  if (sort === 'name') return candidates.sort(byName);
  if (sort === 'price-low') {
    return candidates.sort((left, right) => priceOf(left) - priceOf(right) || byName(left, right));
  }
  if (sort === 'price-high') {
    return candidates.sort((left, right) => priceOf(right) - priceOf(left) || byName(left, right));
  }
  return candidates;
}

export function packPreviewLoadout(pack) {
  const loadout = {...DEFAULT_LOADOUT};
  const populated = new Set();
  for (const item of packItems(pack)) {
    if (!SUPPORTED_SLOTS.has(item.slot) || populated.has(item.slot) || !item.asset_key) continue;
    loadout[item.slot] = item.asset_key;
    populated.add(item.slot);
  }
  return loadout;
}

export function itemPreviewLoadout(item) {
  const loadout = {...DEFAULT_LOADOUT};
  if (item && SUPPORTED_SLOTS.has(item.slot) && item.asset_key) {
    loadout[item.slot] = item.asset_key;
  }
  return loadout;
}

export function dashboardPurchasePath(packID, pathname = window.location.pathname) {
  // The Dashboard opens as a slide-out overlay on the main site rather than
  // a full-page navigation to /dashboard/. dash_pack travels alongside
  // dash_open/dash_tab (see applyDeepLinkedDashboardOpen in js/app.js, which
  // forwards it into the embedded iframe as ?pack=) so the Dashboard lands
  // straight on a one-item purchase card instead of a duplicate shop grid.
  const suffix = packID ? `&dash_pack=${encodeURIComponent(packID)}` : '';
  return appPath('/?dash_open=1&dash_tab=cosmetics' + suffix, pathname);
}

export function subscriptionDashboardPath(pathname = window.location.pathname) {
  return appPath('/?dash_open=1&dash_tab=cosmetics&dash_plan=all-access', pathname);
}

export function catalogPath(pathname = window.location.pathname) {
  return apiPath('/cosmetics/catalog', pathname);
}

function formatPrice(pack) {
  if (pack?.is_free) return 'Free';
  const rawCents = Number(pack?.price_cents || 0);
  const cents = Number.isFinite(rawCents) && rawCents >= 0 ? Math.round(rawCents) : 0;
  const currency = pack?.currency || 'USD';
  try {
    return new Intl.NumberFormat(undefined, {style: 'currency', currency}).format(cents / 100);
  } catch (_) {
    return `$${(cents / 100).toFixed(2)}`;
  }
}

function normalizeSearchText(value) {
  return String(value ?? '').toLowerCase().replace(/[-_\s]+/g, ' ').trim();
}

function searchText(pack) {
  return normalizeSearchText([
    pack?.id,
    pack?.name,
    pack?.description,
    ...packItems(pack).flatMap(item => [item.id, item.name, item.description, item.rarity, item.slot]),
  ].filter(Boolean).join(' '));
}

function swatchStyle(assetKey) {
  const themes = typeof window !== 'undefined' ? window.ArenaCosmeticThemes : globalThis.ArenaCosmeticThemes;
  return themes && typeof themes.swatchStyle === 'function' ? themes.swatchStyle(assetKey || '') : '';
}

function createElement(tag, className, text) {
  const element = document.createElement(tag);
  if (className) element.className = className;
  if (text !== undefined) element.textContent = text;
  return element;
}

async function readJSON(response) {
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(body.error || body.detail || `Request failed (${response.status})`);
  return body;
}

/** Mount the dedicated, pack-first cosmetic showroom. */
export function initCosmeticsShop(root, options = {}) {
  if (!root) return null;

  const pathname = options.pathname || window.location.pathname;
  const fetchImpl = options.fetchImpl || window.fetch.bind(window);
  const previewFactory = options.previewFactory || (canvas => new CosmeticShopPreview(canvas));
  const requestedPackID = options.requestedPackID
    ?? new URLSearchParams(window.location.search).get('pack')
    ?? '';
  const requestedWeapon = options.requestedWeapon
    ?? new URLSearchParams(window.location.search).get('weapon')
    ?? 'sword';

  const elements = {
    canvas: root.querySelector('#shop-preview-canvas'),
    status: root.querySelector('[data-shop-status]'),
    search: root.querySelector('[data-shop-search]'),
    category: root.querySelector('[data-shop-category]'),
    kind: root.querySelector('[data-shop-kind]'),
    sort: root.querySelector('[data-shop-sort]'),
    summary: root.querySelector('[data-shop-results-summary]'),
    showMore: root.querySelector('[data-shop-show-more]'),
    packList: root.querySelector('[data-shop-pack-list]'),
    detail: root.querySelector('[data-shop-pack-detail]'),
    itemList: root.querySelector('[data-shop-item-list]'),
    packName: root.querySelector('[data-shop-pack-name]'),
    packDescription: root.querySelector('[data-shop-pack-description]'),
    packPrice: root.querySelector('[data-shop-pack-price]'),
    packCount: root.querySelector('[data-shop-pack-count]'),
    purchase: root.querySelector('[data-shop-purchase]'),
    previewPack: root.querySelector('[data-shop-preview-pack]'),
    previewLabel: root.querySelector('[data-shop-preview-label]'),
    previewStatus: root.querySelector('[data-shop-preview-status]'),
    rotateLeft: root.querySelector('[data-shop-rotate-left]'),
    rotateRight: root.querySelector('[data-shop-rotate-right]'),
    resetView: root.querySelector('[data-shop-reset-view]'),
    chassisPicker: root.querySelector('[data-shop-chassis-picker]'),
    subscription: root.querySelector('[data-shop-subscription]'),
    subscriptionPrice: root.querySelector('[data-shop-subscription-price]'),
    subscriptionAction: root.querySelector('[data-shop-subscription-action]'),
    subscriptionState: root.querySelector('[data-shop-subscription-state]'),
  };

  if (!elements.canvas || !elements.packList || !elements.detail || !elements.itemList) return null;

  const state = {
    catalog: {categories: [], packs: []},
    checkoutEnabled: false,
    subscriptionOffer: null,
    query: '',
    category: 'all',
    kind: 'all',
    sort: 'featured',
    visible: PAGE_SIZE,
    selectedPackID: '',
    selectedItemID: '',
    weapon: SUPPORTED_WEAPONS.has(requestedWeapon) ? requestedWeapon : 'sword',
    preview: null,
    previewPromise: null,
    previewGeneration: 0,
    destroyed: false,
  };
  const cleanups = [];

  const listen = (target, type, handler, settings) => {
    if (!target) return;
    target.addEventListener(type, handler, settings);
    cleanups.push(() => target.removeEventListener(type, handler, settings));
  };

  const allPacks = () => Array.isArray(state.catalog.packs) ? state.catalog.packs : [];
  const selectedPack = () => allPacks().find(pack => pack.id === state.selectedPackID) || null;
  const selectedItem = () => packItems(selectedPack()).find(item => item.id === state.selectedItemID) || null;
  const filteredPacks = () => {
    const query = normalizeSearchText(state.query);
    const matches = allPacks().filter(pack => {
      if (state.category !== 'all' && pack.category_id !== state.category) return false;
      if (state.kind === 'trails' && !isTrailPack(pack)) return false;
      if (state.kind === 'body-forms' && !isBodyFormPack(pack)) return false;
      if (state.kind === 'sets' && (isTrailPack(pack) || isBodyFormPack(pack))) return false;
      return !query || searchText(pack).includes(query);
    });
    return sortCosmeticPacks(matches, state.sort);
  };

  const setStatus = (message, status = 'ready') => {
    if (!elements.status) return;
    elements.status.textContent = message;
    elements.status.dataset.state = status;
  };

  const renderSubscription = () => {
    const raw = state.subscriptionOffer && typeof state.subscriptionOffer === 'object'
      ? state.subscriptionOffer
      : {};
    const offer = {
      enabled: raw.enabled === true,
      price_cents: Number.isFinite(Number(raw.price_cents)) ? Number(raw.price_cents) : 1999,
      currency: raw.currency || 'USD',
      interval: String(raw.interval || 'month').trim().toLowerCase() || 'month',
      includes_future_sets: raw.includes_future_sets === true,
      max_api_keys: Math.min(5, Math.max(1, Number(raw.max_api_keys) || 5)),
    };
    if (elements.subscriptionPrice) {
      elements.subscriptionPrice.textContent = `${formatPrice(offer)} / ${offer.interval}`;
    }
    if (elements.subscriptionAction) {
      elements.subscriptionAction.href = subscriptionDashboardPath(pathname);
      elements.subscriptionAction.hidden = !offer.enabled;
      elements.subscriptionAction.setAttribute('aria-disabled', String(!offer.enabled));
    }
    if (elements.subscriptionState) {
      elements.subscriptionState.textContent = offer.enabled
        ? `Every current and future set, full-body skin, and trail, up to ${offer.max_api_keys} active API keys.`
        : 'All Access checkout is not open yet.';
    }
    if (elements.subscription) elements.subscription.dataset.state = offer.enabled ? 'available' : 'unavailable';
  };

  const updateURL = packID => {
    if (options.updateURL === false || !window.history?.replaceState) return;
    const url = new URL(window.location.href);
    if (packID) url.searchParams.set('pack', packID);
    else url.searchParams.delete('pack');
    window.history.replaceState(window.history.state, '', `${url.pathname}${url.search}${url.hash}`);
  };

  const ensurePreview = async () => {
    if (state.preview) return state.preview;
    if (state.previewPromise) return state.previewPromise;
    if (elements.previewStatus) {
      elements.previewStatus.textContent = 'Starting showroom renderer...';
      elements.previewStatus.dataset.state = 'loading';
    }
    state.previewPromise = (async () => {
      const preview = previewFactory(elements.canvas);
      await Promise.resolve(preview.init());
      if (state.destroyed) {
        preview.dispose?.();
        return null;
      }
      state.preview = preview;
      return preview;
    })();
    try {
      return await state.previewPromise;
    } finally {
      state.previewPromise = null;
    }
  };

  const applyPreview = async (loadout, label, signature) => {
    const generation = ++state.previewGeneration;
    if (elements.previewLabel) elements.previewLabel.textContent = label;
    elements.canvas.dataset.previewSignature = signature;
    try {
      const preview = await ensurePreview();
      if (!preview || generation !== state.previewGeneration || state.destroyed) return;
      if (typeof preview.setCharacter === 'function') preview.setCharacter({weapon: state.weapon});
      preview.setLoadout(loadout);
      if (elements.previewStatus) {
        elements.previewStatus.textContent = `${label} shown on a running Arena bot.`;
        elements.previewStatus.dataset.state = 'ready';
      }
      elements.canvas.dataset.previewState = 'ready';
    } catch (error) {
      if (generation !== state.previewGeneration || state.destroyed) return;
      if (elements.previewStatus) {
        elements.previewStatus.textContent = `3D preview unavailable. You can still inspect every pack item. ${error.message}`;
        elements.previewStatus.dataset.state = 'error';
      }
      elements.canvas.dataset.previewState = 'unavailable';
    }
  };

  const previewCurrentSelection = () => {
    const pack = selectedPack();
    if (!pack) {
      if (elements.previewLabel) elements.previewLabel.textContent = 'Select a cosmetic pack';
      if (state.preview) {
        if (typeof state.preview.setCharacter === 'function') {
          state.preview.setCharacter({weapon: state.weapon});
        }
        state.preview.setLoadout({...DEFAULT_LOADOUT});
        elements.canvas.dataset.previewSignature = 'standard:no-pack-selected';
        elements.canvas.dataset.previewState = 'ready';
      }
      if (elements.previewStatus) {
        elements.previewStatus.textContent = state.preview
          ? 'Standard Arena bot shown. Adjust the filters to select another pack.'
          : 'Choose a pack to start the bot preview.';
        elements.previewStatus.dataset.state = 'empty';
      }
      return;
    }
    const item = selectedItem();
    if (item) {
      applyPreview(
        itemPreviewLoadout(item),
        `${item.name || item.id || 'Cosmetic'} only`,
        `${pack.id || 'pack'}:item:${item.id || item.asset_key || item.slot}`,
      );
      return;
    }
    applyPreview(
      packPreviewLoadout(pack),
      `${pack.name || pack.id || 'Cosmetic pack'} — full pack`,
      `${pack.id || 'pack'}:full-pack`,
    );
  };

  const createPackButton = pack => {
    const button = createElement('button', 'shop-pack-card');
    button.type = 'button';
    button.dataset.shopPackId = pack.id || '';
    button.setAttribute('aria-pressed', String(pack.id === state.selectedPackID));

    const swatch = createElement('span', 'shop-pack-swatch');
    swatch.setAttribute('aria-hidden', 'true');
    const style = swatchStyle(packItems(pack)[0]?.asset_key);
    if (style) swatch.style.background = style;

    const copy = createElement('span', 'shop-pack-card-copy');
    const productType = isTrailPack(pack) ? 'Trail' : isBodyFormPack(pack) ? 'Full-body skin' : 'Set';
    copy.append(
      createElement('strong', '', pack.name || pack.id || 'Cosmetic pack'),
      createElement('small', 'shop-pack-card-meta', `${productType}, ${packItems(pack).length} item${packItems(pack).length === 1 ? '' : 's'}, ${formatPrice(pack)}`),
    );
    const arrow = createElement('span', 'shop-pack-card-arrow', '↗');
    arrow.setAttribute('aria-hidden', 'true');
    button.append(swatch, copy, arrow);
    button.addEventListener('click', () => selectPack(pack.id, {focusPreview: true}));
    return button;
  };

  const renderPackList = ({revealSelected = false} = {}) => {
    const packs = filteredPacks();
    const selectedIndex = packs.findIndex(pack => pack.id === state.selectedPackID);
    if (revealSelected && selectedIndex >= state.visible) {
      state.visible = selectedIndex + 1;
    }
    elements.packList.setAttribute('aria-busy', 'false');
    elements.packList.replaceChildren();
    if (packs.length === 0) {
      const empty = createElement('div', 'shop-empty-state');
      empty.append(
        createElement('strong', '', 'No packs match'),
        createElement('p', '', 'Try a set name, item, or another collection.'),
      );
      elements.packList.appendChild(empty);
    } else {
      for (const pack of packs.slice(0, state.visible)) {
        elements.packList.appendChild(createPackButton(pack));
      }
    }
    if (elements.summary) {
      const showing = Math.min(state.visible, packs.length);
      elements.summary.textContent = packs.length
        ? `Showing ${showing} of ${packs.length} packs`
        : 'No cosmetic packs found';
    }
    if (elements.showMore) {
      elements.showMore.hidden = state.visible >= packs.length;
      elements.showMore.textContent = `Show ${Math.min(PAGE_SIZE, Math.max(0, packs.length - state.visible))} more packs`;
    }
    if (revealSelected && selectedIndex >= 0) {
      const selectedButton = Array.from(elements.packList.children)
        .find(button => button.dataset?.shopPackId === state.selectedPackID);
      selectedButton?.scrollIntoView?.({block: 'nearest'});
    }
  };

  const updatePackSelection = () => {
    for (const button of elements.packList.children) {
      if (!button.dataset?.shopPackId) continue;
      button.setAttribute('aria-pressed', String(button.dataset.shopPackId === state.selectedPackID));
    }
  };

  const createItemButton = (item, index) => {
    const selected = item.id === state.selectedItemID;
    const button = createElement('button', 'shop-item-card');
    button.type = 'button';
    button.dataset.shopItemId = item.id || String(index);
    button.setAttribute('aria-pressed', String(selected));
    button.setAttribute('aria-label', `Preview ${item.name || item.id || `item ${index + 1}`} on the bot`);

    const swatch = createElement('span', 'shop-item-swatch');
    swatch.setAttribute('aria-hidden', 'true');
    const style = swatchStyle(item.asset_key);
    if (style) swatch.style.background = style;
    const copy = createElement('span', 'shop-item-copy');
    copy.append(
      createElement('small', 'shop-item-slot', SLOT_LABELS[item.slot] || item.slot || 'Cosmetic'),
      createElement('strong', '', item.name || item.id || `Item ${index + 1}`),
      createElement('span', '', item.description || 'Presentation-only Arena cosmetic.'),
    );
    const action = createElement('span', 'shop-item-action', selected ? 'Previewing' : 'Preview item');
    button.append(swatch, copy, action);
    button.addEventListener('click', () => selectItem(item.id));
    return button;
  };

  const renderDetail = () => {
    const pack = selectedPack();
    elements.detail.hidden = !pack;
    elements.itemList.replaceChildren();
    if (!pack) return;

    const items = packItems(pack);
    if (elements.packName) elements.packName.textContent = pack.name || pack.id || 'Cosmetic pack';
    if (elements.packDescription) {
      elements.packDescription.textContent = pack.description || 'A coordinated collection of presentation-only Arena cosmetics.';
    }
    if (elements.packPrice) elements.packPrice.textContent = formatPrice(pack);
    if (elements.packCount) {
      elements.packCount.textContent = `${items.length} included item${items.length === 1 ? '' : 's'}`;
    }
    if (elements.previewPack) {
      elements.previewPack.disabled = items.length === 0;
      elements.previewPack.setAttribute('aria-pressed', String(!state.selectedItemID));
      elements.previewPack.textContent = state.selectedItemID ? 'Preview full pack' : 'Previewing full pack';
    }

    if (items.length === 0) {
      elements.itemList.appendChild(createElement('p', 'shop-empty-state', 'This pack does not have any published items yet.'));
    } else {
      items.forEach((item, index) => elements.itemList.appendChild(createItemButton(item, index)));
    }

    if (elements.purchase) {
      const saleReady = state.checkoutEnabled && pack.is_purchasable === true;
      elements.purchase.href = saleReady ? dashboardPurchasePath(pack.id, pathname) : appPath('/?dash_open=1&dash_tab=cosmetics', pathname);
      elements.purchase.hidden = !saleReady;
      elements.purchase.setAttribute('aria-disabled', String(!saleReady));
      const purchaseKind = isTrailPack(pack) ? 'trail' : isBodyFormPack(pack) ? 'skin' : 'pack';
      elements.purchase.textContent = pack.is_free
        ? 'Claim in Dashboard'
        : `Buy ${purchaseKind} — ${formatPrice(pack)}`;
      elements.purchase.dataset.shopPurchasePack = pack.id || '';
    }
  };

  const updateItemSelection = () => {
    if (elements.previewPack) {
      elements.previewPack.setAttribute('aria-pressed', String(!state.selectedItemID));
      elements.previewPack.textContent = state.selectedItemID ? 'Preview full pack' : 'Previewing full pack';
    }
    for (const button of elements.itemList.children) {
      if (!button.dataset?.shopItemId) continue;
      const selected = button.dataset.shopItemId === state.selectedItemID;
      button.setAttribute('aria-pressed', String(selected));
      const action = button.lastElementChild || button.children?.[button.children.length - 1];
      if (action) action.textContent = selected ? 'Previewing' : 'Preview item';
    }
  };

  function selectPack(packID, {focusPreview = false} = {}) {
    const pack = allPacks().find(candidate => candidate.id === packID);
    if (!pack) return;
    state.selectedPackID = pack.id;
    state.selectedItemID = '';
    updatePackSelection();
    renderDetail();
    previewCurrentSelection();
    updateURL(pack.id);
    if (focusPreview && window.matchMedia?.('(max-width: 768px)').matches) {
      const reducedMotion = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches === true;
      elements.canvas.closest('.shop-preview-panel')?.scrollIntoView({
        behavior: reducedMotion ? 'auto' : 'smooth',
        block: 'start',
      });
    }
  }

  function selectItem(itemID) {
    const item = packItems(selectedPack()).find(candidate => candidate.id === itemID);
    if (!item) return;
    state.selectedItemID = item.id;
    updateItemSelection();
    previewCurrentSelection();
  }

  const previewPack = () => {
    if (!selectedPack()) return;
    state.selectedItemID = '';
    updateItemSelection();
    previewCurrentSelection();
  };

  const selectCharacter = weapon => {
    const normalized = SUPPORTED_WEAPONS.has(weapon) ? weapon : 'sword';
    if (state.weapon === normalized && state.preview) return;
    state.weapon = normalized;
    for (const input of elements.chassisPicker?.querySelectorAll('input[type="radio"]') || []) {
      input.checked = input.value === normalized;
    }
    if (options.updateURL !== false && window.history?.replaceState) {
      const url = new URL(window.location.href);
      if (normalized === 'sword') url.searchParams.delete('weapon');
      else url.searchParams.set('weapon', normalized);
      window.history.replaceState(window.history.state, '', `${url.pathname}${url.search}${url.hash}`);
    }
    previewCurrentSelection();
  };

  const populateCategories = () => {
    if (!elements.category) return;
    const used = new Set(allPacks().map(pack => pack.category_id).filter(Boolean));
    const optionsList = [createElement('option', '', 'All collections')];
    optionsList[0].value = 'all';
    for (const category of state.catalog.categories || []) {
      if (!used.has(category.id)) continue;
      const option = createElement('option', '', category.name || category.id);
      option.value = category.id;
      optionsList.push(option);
    }
    elements.category.replaceChildren(...optionsList);
    elements.category.value = used.has(state.category) ? state.category : 'all';
    state.category = elements.category.value;
  };

  const applyFilter = ({selectFirst = false} = {}) => {
    state.visible = PAGE_SIZE;
    const packs = filteredPacks();
    if (selectFirst || !packs.some(pack => pack.id === state.selectedPackID)) {
      state.selectedPackID = packs[0]?.id || '';
      state.selectedItemID = '';
      renderDetail();
      previewCurrentSelection();
      updateURL(state.selectedPackID);
    }
    renderPackList({revealSelected: true});
  };

  const loadCatalog = async () => {
    setStatus('Loading cosmetic packs...', 'loading');
    elements.packList.setAttribute('aria-busy', 'true');
    try {
      const response = await fetchImpl(catalogPath(pathname), {
        headers: {Accept: 'application/json'},
        cache: 'no-store',
      });
      const data = await readJSON(response);
      state.catalog = {
        categories: Array.isArray(data.categories) ? data.categories : [],
        packs: Array.isArray(data.packs) ? data.packs.filter(pack => pack?.is_active !== false) : [],
      };
      state.checkoutEnabled = data.checkout_enabled === true;
      state.subscriptionOffer = data.subscription_offer && typeof data.subscription_offer === 'object'
        ? data.subscription_offer
        : null;
      renderSubscription();
      populateCategories();
      const matches = filteredPacks();
      const requested = matches.find(pack => pack.id === requestedPackID);
      const initial = requested || matches[0] || null;
      state.selectedPackID = initial?.id || '';
      state.selectedItemID = '';
      renderPackList();
      renderDetail();
      if (initial) {
        previewCurrentSelection();
        updateURL(initial.id);
        setStatus(`${allPacks().length} cosmetic packs ready to preview.`, 'success');
      } else {
        setStatus('No cosmetic packs are published yet.', 'empty');
      }
    } catch (error) {
      state.catalog = {categories: [], packs: []};
      state.subscriptionOffer = null;
      renderSubscription();
      state.selectedPackID = '';
      renderPackList();
      renderDetail();
      previewCurrentSelection();
      setStatus(`Catalog unavailable: ${error.message}`, 'error');
      const retry = createElement('button', 'shop-retry', 'Retry catalog');
      retry.type = 'button';
      retry.addEventListener('click', loadCatalog, {once: true});
      elements.packList.replaceChildren(retry);
    }
  };

  listen(elements.search, 'input', event => {
    state.query = String(event.currentTarget.value || '').trim();
    applyFilter();
  });
  listen(elements.category, 'change', event => {
    state.category = String(event.currentTarget.value || 'all');
    applyFilter();
  });
  listen(elements.kind, 'change', event => {
    state.kind = String(event.currentTarget.value || 'all');
    // Switching product families is an explicit browse action. Clear a stale
    // text query and collection so choosing Trails always reveals its shelf.
    state.query = '';
    state.category = 'all';
    if (elements.search) elements.search.value = '';
    if (elements.category) elements.category.value = 'all';
    applyFilter({selectFirst: true});
  });
  listen(elements.sort, 'change', event => {
    state.sort = String(event.currentTarget.value || 'featured');
    applyFilter({selectFirst: true});
  });
  listen(elements.showMore, 'click', () => {
    state.visible += PAGE_SIZE;
    renderPackList();
  });
  listen(elements.previewPack, 'click', previewPack);
  listen(elements.rotateLeft, 'click', () => state.preview?.rotateBy?.(-Math.PI / 8));
  listen(elements.rotateRight, 'click', () => state.preview?.rotateBy?.(Math.PI / 8));
  listen(elements.resetView, 'click', () => state.preview?.resetRotation?.());
  listen(elements.chassisPicker, 'change', event => {
    if (event.target?.matches?.('input[type="radio"][name="shop-chassis"]')) {
      selectCharacter(event.target.value);
    }
  });

  const dispose = () => {
    if (state.destroyed) return;
    state.destroyed = true;
    state.previewGeneration += 1;
    for (const cleanup of cleanups.splice(0)) cleanup();
    state.preview?.dispose?.();
    state.preview = null;
    state.previewPromise = null;
  };
  listen(window, 'pagehide', event => {
    // A persisted pagehide enters the browser's back/forward cache. The
    // preview suspends and resumes itself; destroying the controller here
    // would leave a blank canvas when the page is restored.
    if (!event.persisted) dispose();
  });

  loadCatalog();
  for (const input of elements.chassisPicker?.querySelectorAll('input[type="radio"]') || []) {
    input.checked = input.value === state.weapon;
  }
  return {
    selectPack,
    selectItem,
    previewPack,
    selectCharacter,
    dispose,
    snapshot: () => ({
      packCount: allPacks().length,
      filteredCount: filteredPacks().length,
      selectedPackID: state.selectedPackID,
      selectedItemID: state.selectedItemID,
      kind: state.kind,
      sort: state.sort,
      weapon: state.weapon,
      previewSignature: elements.canvas.dataset.previewSignature || '',
      subscriptionEnabled: state.subscriptionOffer?.enabled === true,
    }),
  };
}

// Mirrors setupExploreBrand in js/site-shell.js and m/mobile.js: the brand
// lockup doubles as the Explore dropdown trigger everywhere it appears.
// Modifier/non-left clicks fall through to the real href instead of opening
// the menu.
function setupExploreBrand() {
  const item = document.getElementById('arenaExploreItem');
  const toggle = document.getElementById('arenaExploreToggle');
  if (!item || !toggle) return;

  const setOpen = (open) => {
    toggle.setAttribute('aria-expanded', String(open));
    item.classList.toggle('is-open', open);
  };

  toggle.addEventListener('click', (event) => {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    setOpen(!item.classList.contains('is-open'));
  });

  document.addEventListener('click', (event) => {
    if (!item.contains(event.target)) setOpen(false);
  });

  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') setOpen(false);
  });
}

if (typeof document !== 'undefined') {
  const root = document.getElementById('cosmetic-shop');
  if (root) initCosmeticsShop(root);
  setupExploreBrand();
}
