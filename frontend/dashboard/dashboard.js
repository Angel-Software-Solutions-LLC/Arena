const WEAPON_META = {
  sword:{name:'Sword',badge:'SWD',role:'Balanced frontline weapon.',desc:'Reliable melee with cleave pressure when fights stack up.',build:'hp:5 speed:5 attack:5 defense:5',strongVs:'Daggers, shield',counter:'Bow, staff'},
  bow:{name:'Bow',badge:'BOW',role:'Long-range poke and charge management.',desc:'Stores charge while ready, then cashes it into a faster heavier arrow from clean sightlines.',build:'hp:4 speed:6 attack:6 defense:4',strongVs:'Shield, spear',counter:'Daggers, grapple'},
  daggers:{name:'Daggers',badge:'DAG',role:'Close-range burst assassin.',desc:'Fast repeat pressure that rewards sticking on a target.',build:'hp:4 speed:6 attack:6 defense:4',strongVs:'Bow, staff',counter:'Sword, spear'},
  shield:{name:'Shield',badge:'SHD',role:'Slow attrition tank.',desc:'Defensive brawler built to soak pressure and win longer trades.',build:'hp:7 speed:3 attack:4 defense:6',strongVs:'Daggers',counter:'Staff, bow'},
  spear:{name:'Spear',badge:'SPR',role:'Spacing and brace control.',desc:'Holding ground empowers the next knockback hit and punishes over-commits.',build:'hp:5 speed:5 attack:6 defense:4',strongVs:'Daggers, shield',counter:'Bow, staff'},
  staff:{name:'Staff',badge:'STF',role:'Delayed AoE zone denial.',desc:'Places delayed bursts that leave a lingering burn field on impact.',build:'hp:4 speed:5 attack:7 defense:4',strongVs:'Shield, sword',counter:'Daggers, bow'},
  grapple:{name:'Grapple',badge:'GRP',role:'Wall-slam bruiser.',desc:'Pulls in, then spikes damage when targets are pinned near walls or arena edges.',build:'hp:5 speed:5 attack:6 defense:4',strongVs:'Cornered ranged bots',counter:'Bow, staff'}
};

const TOOLKIT_CACHE_MS = 15000;
const TOOLKIT_STATE = {loading:null,loadedAt:0,weaponStatsById:{},botSetup:null,setupWeaponsById:{},actionsByName:{}};
let toolkitRefreshTimer = null;
let simInitialized = false;

function titleCaseWord(value) {
  return String(value || '').split(/[_-]+/).filter(Boolean).map(part => part.charAt(0).toUpperCase() + part.slice(1)).join(' ');
}
function formatSpecial(value) { return value ? titleCaseWord(value) : 'None'; }
function formatWeaponBadge(weaponId) { return (WEAPON_META[weaponId]?.badge || weaponId.slice(0, 3)).toUpperCase(); }
function weaponIconSvg(weaponId, size=20) {
  const s = Number(size) || 20;
  const svgOpen = `<svg width="${s}" height="${s}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">`;
  switch (weaponId) {
    case 'sword':
      return `${svgOpen}<path d="M12 3l3 3"/><path d="M10.5 7.5l6-6"/><path d="M8 10l6-6"/><path d="M7 11l6 6"/><path d="M6 18l-2 2"/><path d="M10 14l-4 4"/><path d="M9 13l2-2"/><path d="M5 19l-1 1"/></svg>`;
    case 'bow':
      return `${svgOpen}<path d="M7 4c5 3 5 13 0 16"/><path d="M7 12h10"/><path d="M13 9l4 3-4 3"/><path d="M5 6v12"/></svg>`;
    case 'daggers':
      return `${svgOpen}<path d="M7 5l5 5"/><path d="M6 6l2-2"/><path d="M12 10l2-2"/><path d="M17 5l-5 5"/><path d="M18 6l-2-2"/><path d="M12 10l-2-2"/><path d="M10 14l-4 4"/><path d="M14 14l4 4"/></svg>`;
    case 'shield':
      return `${svgOpen}<path d="M12 3l7 3v5c0 5-3 8-7 10-4-2-7-5-7-10V6l7-3z"/><path d="M12 7v10"/><path d="M8.5 10.5H15.5"/></svg>`;
    case 'spear':
      return `${svgOpen}<path d="M5 19L19 5"/><path d="M16 5h3v3"/><path d="M10 14l-5 5"/><path d="M7 20l-3-3"/></svg>`;
    case 'staff':
      return `${svgOpen}<circle cx="15.5" cy="7.5" r="3.2"/><path d="M13 10l-5 11"/><path d="M7 21h3"/><path d="M17.5 4.5l1.3-1.3"/><path d="M19.5 7.5H21"/><path d="M17.5 10.5l1.3 1.3"/></svg>`;
    case 'grapple':
      return `${svgOpen}<path d="M14 4l4 4"/><path d="M8 16l10-10"/><path d="M6 18l4-4"/><path d="M16.5 11.5l2 2c1.6 1.6 1.6 4.1 0 5.7l-.7.7"/><path d="M13.5 14.5l2 2"/><path d="M16.2 19.8l-2.7-2.7"/></svg>`;
    default:
      return `${svgOpen}<circle cx="12" cy="12" r="7"/><path d="M9 12h6"/></svg>`;
  }
}
function getWeaponIds() { const live = Object.keys(TOOLKIT_STATE.weaponStatsById || {}); return live.length ? live : Object.keys(WEAPON_META); }
function getWeaponBundle(weaponId) {
  const live = TOOLKIT_STATE.weaponStatsById[weaponId] || {};
  const setup = TOOLKIT_STATE.setupWeaponsById[weaponId] || {};
  const meta = WEAPON_META[weaponId] || {};
  return {
    id:weaponId,
    name:live.weapon ? titleCaseWord(live.weapon) : setup.name || meta.name || titleCaseWord(weaponId),
    badge:meta.badge || formatWeaponBadge(weaponId),
    role:meta.role || 'General arena weapon.',
    desc:setup.description || meta.desc || 'Live balanced weapon.',
    build:meta.build || 'hp:5 speed:5 attack:5 defense:5',
    strongVs:meta.strongVs || 'Varies by pilot',
    counter:meta.counter || 'Varies by pilot',
    tier:live.tier || '--',
    kills:live.kills || 0,
    recentForm:typeof live.recent_form === 'number' ? live.recent_form : null,
    damage:typeof live.damage_exact === 'number' ? live.damage_exact : (typeof live.damage === 'number' ? live.damage : setup.damage),
    appliedDamage:typeof live.damage === 'number' ? live.damage : setup.damage,
    cooldown:typeof live.cooldown === 'number' ? live.cooldown : setup.cooldown_secs,
    rangeTiles:typeof live.grid_range === 'number' ? live.grid_range : setup.range_tiles,
    special:formatSpecial(live.special || setup.special_ability),
    balance:live.balance_direction || 'steady'
  };
}
async function ensureToolkitData(force=false) {
  const now = Date.now();
  if (!force && TOOLKIT_STATE.loadedAt && (now - TOOLKIT_STATE.loadedAt) < TOOLKIT_CACHE_MS) return TOOLKIT_STATE;
  if (TOOLKIT_STATE.loading) return TOOLKIT_STATE.loading;
  TOOLKIT_STATE.loading = (async () => {
    const [weaponStats, botSetup] = await Promise.all([
      pub('/weapon-stats').catch(() => ({entries:[]})),
      pub('/bot-setup').catch(() => ({}))
    ]);
    TOOLKIT_STATE.weaponStatsById = {};
    (weaponStats.entries || []).forEach(entry => {
      if (entry?.weapon) TOOLKIT_STATE.weaponStatsById[entry.weapon] = entry;
    });
    TOOLKIT_STATE.botSetup = botSetup || {};
    TOOLKIT_STATE.setupWeaponsById = {};
    (botSetup.weapons || []).forEach(entry => {
      if (entry?.name) TOOLKIT_STATE.setupWeaponsById[String(entry.name).toLowerCase()] = entry;
    });
    TOOLKIT_STATE.actionsByName = {};
    (botSetup.actions || []).forEach(entry => {
      if (entry?.name) TOOLKIT_STATE.actionsByName[entry.name] = entry;
    });
    TOOLKIT_STATE.loadedAt = Date.now();
    TOOLKIT_STATE.loading = null;
    return TOOLKIT_STATE;
  })().catch(err => {
    TOOLKIT_STATE.loading = null;
    throw err;
  });
  return TOOLKIT_STATE.loading;
}

// ========== Key Management ==========
function getSavedKeys() { try { return JSON.parse(sessionStorage.getItem('arena_keys')||'[]'); } catch(e) { return []; } }
function saveKey(key, label, name) {
  const keys = getSavedKeys().filter(k => k.key !== key);
  keys.unshift({key, label: label || name || key.slice(0,12), name});
  try { sessionStorage.setItem('arena_keys', JSON.stringify(keys.slice(0,20))); } catch(e) { /* tab storage unavailable */ }
}
function removeKey(key) {
  try { sessionStorage.setItem('arena_keys', JSON.stringify(getSavedKeys().filter(k => k.key !== key))); } catch(e) { /* tab storage unavailable */ }
  renderSavedKeys();
}
function renderSavedKeys() {
  const keys = getSavedKeys();
  document.getElementById('savedKeysList').innerHTML = keys.length ? '<div style="font-size:11px;color:var(--text2);margin-bottom:6px">Keys in this tab:</div>' +
    keys.map(k => `<div class="saved-key" onclick="loginWithKey('${esc(k.key)}')"><span class="name">${esc(k.label||k.name||'Bot')}</span><span class="prefix">${esc(k.key.slice(0,12))}...</span><button class="sm danger" onclick="event.stopPropagation();removeKey('${esc(k.key)}')" style="font-size:9px;padding:1px 5px">x</button></div>`).join('') : '';
}
renderSavedKeys();

// ========== API ==========
let apiKey = '';
let stats = null;
let accountSession = {
  authenticated:false,
  login_enabled:false,
  email_login_enabled:false,
  oidc_login_enabled:false,
  login_url:'',
  logout_url:'',
  account:{id:'',email:'',email_verified:false,name:''},
  csrf_token:'',
};
let accountSnapshot = null;
let accountCatalog = null;
let accountCatalogError = '';
let accountOrders = null;
let accountOrdersError = '';
let accountKeys = null;
let accountKeysError = '';
let accountKeyCreateBusy = false;
let accountBusyKeyID = '';
let accountGeneratedKey = null;
let accountSubscriptionState = {status:'idle',message:''};
let accountBusyLicenseID = '';
let accountBusyOrderID = '';
let accountViewError = '';
let accountViewNotice = '';
let accountRefreshSequence = 0;
let accountPreviewBotID = '';
let accountPreviewStagedBySlot = {};
let accountCosmeticsPreviewController = null;
let accountCosmeticsPreviewModulePromise = null;
let accountCosmeticsPreviewRenderSequence = 0;
let accountProfile = null;
let accountProfileError = '';
let accountProfileLoadSequence = 0;
const DASHBOARD_PARAMS = new URLSearchParams(window.location.search);
let accountPendingPackID = DASHBOARD_PARAMS.get('pack') || '';
const checkoutReturn = String(DASHBOARD_PARAMS.get('checkout') || DASHBOARD_PARAMS.get('checkout_status') || '').toLowerCase();
let accountCheckoutState = checkoutReturn === 'success'
  ? {status:'success',packID:'',message:''}
  : checkoutReturn === 'return'
    ? {status:'reconciling',packID:'',message:"Arena is confirming Stripe's signed payment event before changing your collection."}
    : ['cancel','cancelled','canceled'].includes(checkoutReturn)
      ? {status:'cancelled',packID:'',message:''}
      : {status:'idle',packID:'',message:''};
const DASHBOARD_VIEW = DASHBOARD_PARAMS.get('view') === 'public' ? 'public' : 'private';
// Deep links (e.g. dashboard/?tab=profile from the Cosmetics shop or key
// generator) request an account-auth tab by id. Only honor the request if it
// names a tab that actually requires account auth, otherwise keep whatever
// default the caller passes (cosmetics today, but this generalizes to any
// future account tab without hardcoding its id here).
function resolveAccountLandingTab(fallback) {
  const requested = DASHBOARD_PARAMS.get('tab') || '';
  const matches = requested && [...document.querySelectorAll('.tbtn[data-auth="account"]')]
    .some(button => button.dataset.tab === requested);
  return matches ? requested : fallback;
}
function dashboardAPIBase(pathname, paths) {
  if (paths && typeof paths.apiBase === 'function') {
    return paths.apiBase(pathname);
  }
  const mounted = pathname === '/arena' || pathname.startsWith('/arena/');
  return (mounted ? '/arena' : '') + '/api/v1';
}
function getBase() { return dashboardAPIBase(location.pathname, window.ArenaPaths); }
async function api(path, opts={}) {
  const r = await fetch(getBase()+path, {...opts, headers:{'X-Arena-Key':apiKey,'Content-Type':'application/json',...(opts.headers||{})}});
  if (r.status===401||r.status===403) throw new Error('unauthorized');
  return r.json();
}
async function pub(path) { return (await fetch(getBase()+path)).json(); }

function hasVerifiedAccount() {
  return accountSession.authenticated === true && accountSession.account?.email_verified === true && Boolean(accountSession.account?.email);
}

async function accountRequest(path, opts={}) {
  const method = String(opts.method || 'GET').toUpperCase();
  if (method !== 'GET' && method !== 'HEAD' && !accountSession.csrf_token) {
    throw new Error('Your secure dashboard session is missing CSRF protection. Sign in again before making changes.');
  }
  const headers = {
    ...window.ArenaAccountCosmetics.requestHeaders(method, accountSession.csrf_token, opts.body !== undefined),
    ...(opts.headers||{}),
  };
  const endpoint = path.startsWith('/api/v1/') || path.startsWith('/arena/api/v1/')
    ? path
    : getBase()+path;
  const response = await fetch(endpoint, {...opts,method,headers,credentials:'same-origin',cache:opts.cache||'no-store'});
  let payload = null;
  if (response.status !== 204) {
    const text = await response.text();
    if (text) {
      try { payload = JSON.parse(text); }
      catch (e) { payload = {message:text}; }
    }
  }
  if (opts.allowUnauthorized && (response.status === 401 || response.status === 403)) return null;
  if (!response.ok) {
    const message = payload?.error || payload?.message || `Request failed (${response.status})`;
    throw new Error(message);
  }
  return payload;
}

function accountReturnPath() {
  return location.pathname + location.search;
}

function safeCustomerEmailRedirectPath(raw) {
  const fallback = accountReturnPath();
  if (typeof raw !== 'string') return fallback;
  const candidate = raw.trim();
  if (!candidate.startsWith('/') || candidate.startsWith('//') || candidate.includes('\\')) return fallback;
  try {
    const parsed = new URL(candidate, location.origin);
    const path = parsed.pathname;
    const dashboardPath = path === '/dashboard' || path.startsWith('/dashboard/') ||
      path === '/arena/dashboard' || path.startsWith('/arena/dashboard/');
    if (parsed.origin !== location.origin || parsed.hash || !dashboardPath) return fallback;
    return path + parsed.search;
  } catch (error) {
    return fallback;
  }
}

function navigateAccount(url) {
  if (window.top !== window.self) window.top.location.assign(url);
  else location.assign(url);
}

// Mirrors the "mounted" check dashboardAPIBase() uses for the API prefix, so
// the Shop link lands on the live Arena root that actually contains a
// shop-overlay drawer instead of drifting into an unrelated deployment path.
function siteRootPath() {
  const mounted = location.pathname === '/arena' || location.pathname.startsWith('/arena/');
  return mounted ? '/arena/' : '/';
}

function openArenaShop() {
  // Embedded in the Arena's own Dashboard drawer (desktop or mobile), the
  // parent page can open its Shop drawer directly -- no reload needed. Only
  // fall back to a real navigation when the Dashboard is its own standalone
  // tab (e.g. reached via a bookmarked/emailed link) with no such parent.
  if (window.top !== window.self && typeof window.top.ArenaOpenShop === 'function') {
    window.top.ArenaOpenShop();
    return;
  }
  navigateAccount(siteRootPath() + '?shop_open=1');
}

function notifyOtherTabsSignedIn() {
  import('../js/account-session.js?v=20260714a')
    .then(({notifySessionChanged}) => notifySessionChanged())
    .catch(error => console.warn('Could not notify other Arena tabs of sign-in', error));
}

// The counterpart to notifyOtherTabsSignedIn(): keeps THIS dashboard in sync
// when sign-in (or sign-out) happens somewhere else -- another tab finishing
// a magic-link email, or this same drawer's own confirm step above. Skips
// its own first callback (startSessionSync always fires once immediately
// with the current session, which initializeAccountMode() already just
// fetched) and only reacts to an actual account change after that.
function watchAccountSessionAcrossTabs() {
  import('../js/account-session.js?v=20260714a').then(({startSessionSync}) => {
    let first = true;
    startSessionSync(() => {
      if (first) { first = false; return; }
      initializeAccountMode();
    });
  }).catch(error => console.warn('Cross-tab session sync unavailable', error));
}

async function startAccountLogin() {
  const button = document.getElementById('accountSignInButton');
  if (!accountSession.oidc_login_enabled) {
    document.getElementById('loginError').textContent = 'Angel Accounts sign-in is not configured on this Arena.';
    return;
  }
  // Consent first: the popup is a new window, and asking for agreement inside
  // one the person did not expect is how a consent gate gets clicked through.
  if (window.ArenaConsentGate) {
    const accepted = await window.ArenaConsentGate.ensureConsent();
    if (!accepted) return;
  }
  document.getElementById('loginError').textContent = '';
  const original = button ? button.textContent : '';
  if (button) {
    button.disabled = true;
    button.textContent = 'Waiting for Angel Accounts...';
  }
  try {
    const {signInWithAccounts} = await import('../js/accounts-login.js?v=20260823a');
    await signInWithAccounts({returnTo: accountReturnPath()});
    // Re-read the session whatever the popup reported. It resolves false for a
    // window closed by hand, and that window may still have completed the
    // sign-in on its way out -- the server is what decides, not the popup.
    await initializeAccountMode();
  } catch (error) {
    document.getElementById('loginError').textContent = error?.message || 'Could not start sign-in.';
  } finally {
    if (button) {
      button.disabled = false;
      button.textContent = original || 'Sign in';
    }
  }
}

async function signOutAccount() {
  try {
    const logoutURL = accountSession.logout_url || '/dashboard/logout';
    const separator = logoutURL.includes('?') ? '&' : '?';
    await accountRequest(logoutURL+separator+'return_to='+encodeURIComponent(accountReturnPath()), {method:'POST'});
  } catch (error) {
    accountViewError = error.message || 'Could not sign out of the account session.';
    renderAccountCosmetics();
    return;
  }
  // Signing out only clears the arena_customer_session cookie server-side --
  // there's nothing left to navigate to. Reset the drawer's own state in
  // place instead of reloading the whole Arena page just to land back on
  // /dashboard/, and tell any other open tab to drop its session too
  // (notifyOtherTabsSignedIn is named for the sign-in path but the signal
  // it sends is direction-agnostic: "go re-check your session").
  accountSession = {...accountSession, authenticated:false, account:{id:'',email:'',email_verified:false,name:''}, csrf_token:''};
  accountSnapshot = null;
  accountCatalog = null;
  accountProfile = null;
  notifyOtherTabsSignedIn();
  if (apiKey) {
    buildBotSwitcher();
    updatePrivateAuthUI();
    if (document.querySelector('.tbtn.active')?.dataset.auth === 'account') activateTab('overview');
  } else {
    document.getElementById('app').style.display = 'none';
    document.getElementById('login').style.display = 'flex';
    updateAccountLoginUI();
    updatePrivateAuthUI();
  }
}

// One control, one state to decide: can this Arena reach Angel Accounts. The
// panel used to arbitrate between two sign-in methods and a pending email
// token; there is one method now, so the only question left is whether to
// offer it or explain that it is not configured.
function updateAccountLoginUI() {
  const button = document.getElementById('accountSignInButton');
  if (!button) return;
  delete button.dataset.retrySession;
  const oidcEnabled = accountSession.oidc_login_enabled === true;
  const setup = document.getElementById('accountEmailSetup');
  if (setup) {
    setup.hidden = oidcEnabled;
    setup.textContent = 'Angel Accounts sign-in is not configured on this Arena yet.';
  }
  button.hidden = !oidcEnabled;
  button.disabled = !oidcEnabled;
  button.textContent = 'Sign in with Angel Accounts';
}

function updatePrivateAuthUI() {
  if (DASHBOARD_VIEW !== 'private') return;
  const accountReady = hasVerifiedAccount();
  const botReady = Boolean(apiKey);
  document.querySelectorAll('[data-auth="account"]').forEach(node => { node.style.display = accountReady ? '' : 'none'; });
  document.querySelectorAll('[data-auth="bot"]').forEach(node => { node.style.display = botReady ? '' : 'none'; });
  document.getElementById('statCards').style.display = botReady ? '' : 'none';
  document.getElementById('refreshBtn').style.display = botReady ? '' : 'none';
  document.getElementById('logoutBtn').style.display = botReady ? '' : 'none';
  document.getElementById('logoutBtn').textContent = accountReady ? 'Close bot stats' : 'Logout';
  document.getElementById('accountLogoutBtn').style.display = accountReady ? '' : 'none';
  const identity = document.getElementById('accountToolbarIdentity');
  identity.classList.toggle('visible', accountReady);
  identity.innerHTML = accountReady ? `Owned by <strong>${esc(accountSession.account.email)}</strong>` : '';
  const hasSwitcherChoice = accountReady || getSavedKeys().length > 0;
  document.getElementById('botSwitcher').style.display = hasSwitcherChoice ? '' : 'none';
}

const ACCOUNT_PREVIEW_SLOTS = ['bot_skin','weapon_skin','attachment','trail'];

function accountPreviewAssetName(slot, license, assetKey) {
  if (license?.item?.name) return license.item.name;
  if (slot === 'attachment' && assetKey === 'none') return 'None';
  if (assetKey === 'standard') return slot === 'bot_skin' ? 'Standard bot' : 'Standard';
  return String(assetKey || 'None').replaceAll('_', ' ');
}

function accountCosmeticsTabIsActive() {
  return document.getElementById('tab-cosmetics')?.classList.contains('active') === true;
}

function syncAccountCosmeticsPreviewRenderer(model) {
  const active = accountCosmeticsTabIsActive();
  const verified = hasVerifiedAccount();
  const bot = model?.bot || null;
  const sequence = ++accountCosmeticsPreviewRenderSequence;
  if (!active || !verified || !bot) {
    if (accountCosmeticsPreviewController) {
      void accountCosmeticsPreviewController.update({active:false,verified,bot,loadout:model?.previewLoadout});
    }
    return;
  }

  if (!accountCosmeticsPreviewModulePromise) {
    accountCosmeticsPreviewModulePromise = import('./cosmetics-preview.js?v=20260810d').catch(error => {
      accountCosmeticsPreviewModulePromise = null;
      throw error;
    });
  }
  void accountCosmeticsPreviewModulePromise.then(async module => {
    if (sequence !== accountCosmeticsPreviewRenderSequence || !accountCosmeticsTabIsActive()) return;
    if (!accountCosmeticsPreviewController) {
      accountCosmeticsPreviewController = new module.DashboardCosmeticsPreview({
        canvas:document.getElementById('accountCosmeticsPreviewCanvas'),
        status:document.getElementById('accountCosmeticsPreviewStatus'),
      });
    }
    await accountCosmeticsPreviewController.update({
      active:true,
      verified:hasVerifiedAccount(),
      bot,
      loadout:model.previewLoadout,
    });
  }).catch(() => {
    if (sequence !== accountCosmeticsPreviewRenderSequence) return;
    const canvas = document.getElementById('accountCosmeticsPreviewCanvas');
    const status = document.getElementById('accountCosmeticsPreviewStatus');
    if (canvas) canvas.hidden = true;
    if (status) {
      status.hidden = false;
      status.textContent = '3D preview unavailable. Current and staged cosmetics are still listed.';
    }
  });
}

function renderAccountCosmeticsOutfitter() {
  const selector = document.getElementById('accountCosmeticsPreviewBot');
  const slots = document.getElementById('accountCosmeticsPreviewSlots');
  const name = document.getElementById('accountCosmeticsPreviewName');
  const reset = document.querySelector('[data-cosmetics-preview-reset]');
  if (!selector || !slots || !name || !reset || !window.ArenaAccountCosmetics) return;
  const safe = window.ArenaAccountCosmetics.escapeHTML;

  if (!accountSnapshot) {
    selector.disabled = true;
    selector.innerHTML = '<option value="">Loading linked bots...</option>';
    name.textContent = 'Loading account cosmetics';
    reset.disabled = true;
    slots.innerHTML = '<p>Loading the server-saved loadout...</p>';
    syncAccountCosmeticsPreviewRenderer(null);
    return;
  }

  const bots = Array.isArray(accountSnapshot.bots) ? accountSnapshot.bots : [];
  if (!bots.some(bot => bot.id === accountPreviewBotID)) {
    accountPreviewBotID = bots[0]?.id || '';
    accountPreviewStagedBySlot = {};
  }
  selector.disabled = bots.length === 0;
  selector.innerHTML = bots.length
    ? bots.map(bot => {
        const identity = bot.key_prefix ? ` - ${bot.key_prefix}...` : '';
        return `<option value="${safe(bot.id)}"${bot.id === accountPreviewBotID ? ' selected' : ''}>${safe(bot.name + identity)}</option>`;
      }).join('')
    : '<option value="">Link a bot first</option>';

  if (!accountPreviewBotID) {
    name.textContent = 'No linked bot selected';
    reset.disabled = true;
    slots.innerHTML = '<p>No bots linked yet. <button type="button" class="sm" data-goto-profile>Link one from the Profile tab</button> to compare its current loadout with a staged preview.</p>';
    syncAccountCosmeticsPreviewRenderer(null);
    return;
  }

  const currentLoadout = window.ArenaAccountCosmetics.equippedLoadout(accountSnapshot, accountPreviewBotID);
  const model = window.ArenaAccountCosmetics.previewModel(
    accountSnapshot,
    accountPreviewBotID,
    accountPreviewStagedBySlot,
  );
  accountPreviewStagedBySlot = {...model.stagedBySlot};
  name.textContent = model.bot?.name || 'Linked bot';
  reset.disabled = !model.hasStaged;
  slots.innerHTML = ACCOUNT_PREVIEW_SLOTS.map(slot => {
    const currentLicense = model.currentLicenses[slot] || null;
    const stagedLicense = model.stagedLicenses[slot] || null;
    const currentName = accountPreviewAssetName(slot, currentLicense, currentLoadout[slot]);
    const previewName = accountPreviewAssetName(slot, stagedLicense || currentLicense, model.previewLoadout[slot]);
    const changed = model.previewLoadout[slot] !== currentLoadout[slot];
    return `<article class="cosmetics-preview-slot${changed ? ' is-staged' : ''}" data-preview-slot="${safe(slot)}">
      <span class="cosmetics-preview-slot-name">${safe(window.ArenaAccountCosmetics.slotLabel(slot))}</span>
      <span class="cosmetics-preview-value"><small>Currently equipped</small><strong title="${safe(currentName)}">${safe(currentName)}</strong><em>Saved on Arena</em></span>
      <span class="cosmetics-preview-value"><small>Preview</small><strong title="${safe(previewName)}">${safe(previewName)}</strong><em>${changed ? 'Preview only' : 'Matches current'}</em></span>
    </article>`;
  }).join('');
  syncAccountCosmeticsPreviewRenderer({...model,currentLoadout});
}

function renderAccountCosmetics() {
  const root = document.getElementById('accountCosmeticsPanel');
  if (!root || !window.ArenaAccountCosmetics) return;
  if (!accountSnapshot) {
    if (accountViewError) {
      root.className = '';
      root.innerHTML = `<div class="tip warn" role="alert"><b>Could not load cosmetics:</b> ${esc(accountViewError)} <button type="button" class="sm" data-account-retry>Retry</button></div>`;
    } else {
      root.className = 'cosmetic-loading';
      root.textContent = 'Loading your account-owned cosmetics...';
    }
    renderAccountCosmeticsOutfitter();
    renderAccountBotsAndKeys();
    return;
  }
  root.className = '';
  root.innerHTML = window.ArenaAccountCosmetics.renderPanel(accountSnapshot, {
    busyLicenseID: accountBusyLicenseID,
    error: accountViewError,
    notice: accountViewNotice,
    catalog: accountCatalog,
    catalogError: accountCatalogError,
    pendingPackID: accountPendingPackID,
    checkoutState: accountCheckoutState,
    busyOrderID: accountBusyOrderID,
    orders: accountOrders,
    ordersError: accountOrdersError,
    subscriptionState: accountSubscriptionState,
  });
  renderAccountCosmeticsOutfitter();
  renderAccountBotsAndKeys();
}

// Linked bots and API keys moved to the Profile tab (they're account/bot
// credential management, not cosmetics), but the underlying data still
// comes from the same account-cosmetics fetch as renderAccountCosmetics()
// above, so this renders alongside it rather than needing its own separate
// refresh plumbing threaded through every one of that function's ~40 call
// sites.
function renderAccountBotsAndKeys() {
  const root = document.getElementById('accountBotsPanel');
  if (!root || !window.ArenaAccountCosmetics) return;
  if (!accountSnapshot) {
    if (accountViewError) {
      root.className = '';
      root.innerHTML = `<div class="tip warn" role="alert"><b>Could not load your bots:</b> ${esc(accountViewError)} <button type="button" class="sm" data-account-retry>Retry</button></div>`;
    } else {
      root.className = 'cosmetic-loading';
      root.textContent = 'Loading your linked bots and API keys...';
    }
    return;
  }
  root.className = '';
  const snapshot = window.ArenaAccountCosmetics.normalizeSnapshot(accountSnapshot);
  root.innerHTML = window.ArenaAccountCosmetics.renderLinkedBots(snapshot, {}) +
    window.ArenaAccountCosmetics.renderAccountKeys({
      keys: accountKeys,
      keysError: accountKeysError,
      keyCreateBusy: accountKeyCreateBusy,
      busyKeyID: accountBusyKeyID,
      generatedKey: accountGeneratedKey,
    });
}

async function refreshAccountCosmetics(notice='') {
  const requestID = ++accountRefreshSequence;
  accountOrders = null;
  accountOrdersError = '';
  accountKeys = null;
  accountKeysError = '';
  const ordersRequest = accountRequest(window.ArenaAccountCosmetics.accountRoute('orders')+'?limit=20')
    .then(payload => {
      if (requestID !== accountRefreshSequence) return;
      if (!payload || typeof payload !== 'object' || !Array.isArray(payload.orders)) {
        throw new Error('The purchase history service returned an invalid response.');
      }
      accountOrders = payload.orders.slice(0, 20);
      accountOrdersError = '';
      renderAccountCosmetics();
    })
    .catch(error => {
      if (requestID !== accountRefreshSequence) return;
      accountOrders = null;
      accountOrdersError = error?.message || 'Purchase history could not be loaded.';
      renderAccountCosmetics();
    });
  const keysRequest = accountRequest(window.ArenaAccountCosmetics.accountRoute('keys'))
    .then(payload => {
      if (requestID !== accountRefreshSequence) return;
      if (!payload || typeof payload !== 'object' || !Array.isArray(payload.keys)) {
        throw new Error('The API-key service returned an invalid response.');
      }
      accountKeys = window.ArenaAccountCosmetics.normalizeKeyCollection(payload);
      accountKeysError = '';
      renderAccountCosmetics();
    })
    .catch(error => {
      if (requestID !== accountRefreshSequence) return;
      accountKeys = null;
      accountKeysError = error?.message || 'API keys could not be loaded.';
      renderAccountCosmetics();
    });
  if (accountSnapshot) renderAccountCosmetics();
  try {
    const [inventoryResult, catalogResult] = await Promise.allSettled([
      accountRequest(window.ArenaAccountCosmetics.accountRoute('cosmetics')),
      accountRequest('/cosmetics/catalog'),
    ]);
    if (requestID !== accountRefreshSequence) return;
    if (inventoryResult.status !== 'fulfilled') throw inventoryResult.reason;
    const payload = inventoryResult.value;
    if (!payload || typeof payload !== 'object') throw new Error('The cosmetics service returned an invalid response.');
    if (!payload.account) payload.account = accountSession.account;
    accountSnapshot = window.ArenaAccountCosmetics.normalizeSnapshot(payload);
    if (catalogResult.status === 'fulfilled') {
      accountCatalog = window.ArenaAccountCosmetics.normalizeCatalog(catalogResult.value);
      accountCatalogError = '';
    } else {
      accountCatalogError = catalogResult.reason?.message || 'The set catalog could not be loaded. Your owned cosmetics remain available.';
    }
    accountViewError = '';
    accountViewNotice = notice;
  } catch (e) {
    if (requestID !== accountRefreshSequence) return;
    accountViewError = e.message || 'Could not load this account.';
    accountViewNotice = '';
  }
  renderAccountCosmetics();
  await Promise.all([ordersRequest, keysRequest]);
}

// ========== Profile tab ==========
function renderAccountProfile() {
  const root = document.getElementById('accountProfilePanel');
  if (!root || !window.ArenaAccountProfile) return;
  if (!accountProfile) {
    if (accountProfileError) {
      root.className = '';
      root.innerHTML = `<div class="tip warn" role="alert"><b>Could not load your profile:</b> ${esc(accountProfileError)} <button type="button" class="sm" data-account-profile-retry>Retry</button></div>`;
    } else {
      root.className = 'cosmetic-loading';
      root.textContent = 'Loading your profile...';
    }
    return;
  }
  root.className = '';
  root.innerHTML = window.ArenaAccountProfile.renderPanel(accountProfile);
}

async function refreshAccountProfile() {
  const requestID = ++accountProfileLoadSequence;
  try {
    const accountID = accountSession.account?.id || '';
    if (!accountID) throw new Error('Your account session is missing an account id.');
    const payload = await accountRequest(window.ArenaAccountProfile.accountProfileRoute('profile', accountID));
    if (requestID !== accountProfileLoadSequence) return;
    if (!payload || typeof payload !== 'object') throw new Error('The profile service returned an invalid response.');
    accountProfile = window.ArenaAccountProfile.normalizeProfile(payload);
    accountProfileError = '';
  } catch (error) {
    if (requestID !== accountProfileLoadSequence) return;
    accountProfile = null;
    accountProfileError = error.message || 'Could not load your profile.';
  }
  renderAccountProfile();
}

async function handleAccountProfileSave(event) {
  event.preventDefault();
  const form = document.getElementById('profileForm');
  const nameInput = document.getElementById('profileDisplayNameInput');
  const bioInput = document.getElementById('profileBioInput');
  const colorInput = document.getElementById('profileAvatarColorInput');
  const showBotsInput = document.getElementById('profileShowBotsInput');
  const submit = document.getElementById('profileSaveBtn');
  const status = document.getElementById('profileFormStatus');
  if (!form || !nameInput || !bioInput || !colorInput || !showBotsInput || !submit || !status) return;
  if (!form.reportValidity()) return;
  submit.disabled = true;
  submit.textContent = 'Saving...';
  status.className = 'account-email-status';
  status.textContent = '';
  try {
    const payload = await accountRequest(window.ArenaAccountProfile.accountProfileRoute('update'), {
      method: 'PATCH',
      body: JSON.stringify({
        display_name: nameInput.value.trim(),
        bio: bioInput.value,
        avatar_color: colorInput.value,
        show_bots_public: showBotsInput.checked,
      }),
    });
    if (!payload || typeof payload !== 'object') throw new Error('The profile service returned an invalid response.');
    accountProfile = window.ArenaAccountProfile.normalizeProfile(payload);
    accountProfileError = '';
    renderAccountProfile();
    const savedStatus = document.getElementById('profileFormStatus');
    if (savedStatus) {
      savedStatus.className = 'account-email-status is-success';
      savedStatus.textContent = 'Profile saved.';
    }
  } catch (error) {
    status.className = 'account-email-status is-error';
    status.textContent = error.message || 'Could not save your profile.';
    submit.disabled = false;
    submit.textContent = 'Save profile';
  }
}

function handleAccountProfilePanelInput(event) {
  if (!window.ArenaAccountProfile) return;
  if (event.target.id === 'profileBioInput') {
    const counter = document.getElementById('profileBioCounter');
    if (!counter) return;
    const remaining = window.ArenaAccountProfile.BIO_MAX_LENGTH - event.target.value.length;
    counter.textContent = `${remaining} characters remaining`;
    counter.classList.toggle('is-over', remaining < 0);
    return;
  }
  if (event.target.id === 'profileDisplayNameInput' && accountProfile) {
    const preview = document.getElementById('profileChatHandlePreview');
    if (!preview) return;
    const handle = window.ArenaAccountProfile.previewChatHandle(accountProfile, event.target.value);
    preview.innerHTML = `You'll appear in chat as: <strong>${esc(handle)}</strong>`;
  }
}

function handleAccountProfilePanelClick(event) {
  const retryButton = event.target.closest('[data-account-profile-retry]');
  if (retryButton) {
    accountProfileError = '';
    renderAccountProfile();
    refreshAccountProfile();
    return;
  }
  const swatch = event.target.closest('[data-profile-swatch]');
  if (swatch) {
    const colorInput = document.getElementById('profileAvatarColorInput');
    if (colorInput) colorInput.value = swatch.dataset.profileSwatch;
    document.querySelectorAll('[data-profile-swatch]').forEach(node => {
      node.classList.toggle('is-selected', node === swatch);
    });
  }
}

function bindAccountProfileUI() {
  const panel = document.getElementById('tab-profile');
  if (!panel) return;
  panel.addEventListener('submit', event => {
    if (event.target && event.target.id === 'profileForm') handleAccountProfileSave(event);
  });
  panel.addEventListener('click', handleAccountProfilePanelClick);
  panel.addEventListener('input', handleAccountProfilePanelInput);
}

async function createAccountKey(form) {
  const input = form?.querySelector('#accountKeyBotName');
  const intent = window.ArenaAccountCosmetics.keyCreateIntent(input?.value || '', accountKeys);
  if (!intent.ok) {
    accountKeysError = intent.reason === 'keys-unavailable'
      ? 'Current API key usage is unavailable. Retry before creating a key.'
      : intent.reason === 'key-limit-reached'
        ? 'Five API keys are already active. Revoke one before creating another.'
        : 'Enter a bot name before creating an API key.';
    renderAccountCosmetics();
    return;
  }
  if (accountKeyCreateBusy) return;
  accountKeyCreateBusy = true;
  accountKeysError = '';
  accountViewNotice = '';
  renderAccountCosmetics();
  try {
    const data = await accountRequest(window.ArenaAccountCosmetics.accountRoute('keys'), {
      method:'POST',
      body:JSON.stringify(intent.body),
    });
    const apiKeyValue = String(data?.api_key || '').trim();
    if (!apiKeyValue) throw new Error('The API-key service did not return the one-time key value.');
    accountGeneratedKey = data;
    accountKeyCreateBusy = false;
    await refreshAccountCosmetics('API key created and linked to this verified email account.');
  } catch (error) {
    accountKeyCreateBusy = false;
    accountKeysError = error?.message || 'Could not create an API key.';
    renderAccountCosmetics();
  }
}

async function revokeAccountKey(keyID) {
  const intent = window.ArenaAccountCosmetics.keyRevokeIntent(keyID);
  if (!intent.ok || accountBusyKeyID) return;
  const key = accountKeys?.keys?.find(entry => entry.id === keyID);
  if (!confirm(`Revoke the API key for ${key?.bot_name || 'this bot'}? The bot will no longer be able to connect.`)) return;
  accountBusyKeyID = keyID;
  accountKeysError = '';
  accountViewNotice = '';
  renderAccountCosmetics();
  try {
    await accountRequest(window.ArenaAccountCosmetics.accountRoute('key', keyID), {method:'DELETE'});
    accountBusyKeyID = '';
    await refreshAccountCosmetics('API key revoked. Account purchases and owned cosmetics were not removed.');
  } catch (error) {
    accountBusyKeyID = '';
    accountKeysError = error?.message || 'Could not revoke that API key.';
    renderAccountCosmetics();
  }
}

async function copyGeneratedAccountKey() {
  const input = document.getElementById('accountGeneratedKey');
  if (!input?.value) return;
  try {
    if (navigator.clipboard?.writeText) await navigator.clipboard.writeText(input.value);
    else {
      input.select();
      document.execCommand('copy');
    }
    accountViewNotice = 'API key copied. Store it before clearing this screen.';
  } catch (_) {
    accountViewError = 'The browser could not copy this key. Select it and copy it manually.';
  }
  renderAccountCosmetics();
}

function clearGeneratedAccountKey() {
  const input = document.getElementById('accountGeneratedKey');
  if (input) input.value = '';
  accountGeneratedKey = null;
  renderAccountCosmetics();
}

let accountCheckoutOperationGeneration = 0;
let accountCheckoutRequestController = null;
let accountCheckoutActiveOperation = null;
let accountCheckoutReconciliationGeneration = 0;
let accountCheckoutReconcileTarget = {kind:'',id:'',sessionID:''};

function cancelAccountCheckoutReconciliation() {
  accountCheckoutReconciliationGeneration += 1;
}

function beginAccountCheckoutOperation() {
	cancelAccountCheckoutReconciliation();
	accountCheckoutReconcileTarget = {kind:'',id:'',sessionID:''};
	const previousOperation = accountCheckoutActiveOperation;
	if (previousOperation?.embeddedReservation && window.ArenaEmbeddedCheckout?.abort) {
	  window.ArenaEmbeddedCheckout.abort('Checkout replaced by a newer request.', previousOperation.embeddedReservation);
	}
  accountCheckoutOperationGeneration += 1;
  const previous = accountCheckoutRequestController;
  accountCheckoutRequestController = new AbortController();
	if (previous && !previous.signal.aborted) previous.abort();
  if (accountCheckoutState.status === 'pending') accountCheckoutState = {status:'idle',packID:'',message:''};
  if (accountSubscriptionState.status === 'pending') accountSubscriptionState = {status:'idle',message:''};
  accountBusyOrderID = '';
	const operation = {generation:accountCheckoutOperationGeneration,controller:accountCheckoutRequestController,embeddedReservation:null};
	accountCheckoutActiveOperation = operation;
	return operation;
}

function invalidateAccountCheckoutOperation() {
  accountCheckoutOperationGeneration += 1;
  const controller = accountCheckoutRequestController;
  accountCheckoutRequestController = null;
	accountCheckoutActiveOperation = null;
  if (controller && !controller.signal.aborted) controller.abort();
}

function accountCheckoutOperationCurrent(operation) {
  return Boolean(operation && operation.generation === accountCheckoutOperationGeneration &&
    operation.controller === accountCheckoutRequestController && !operation.controller.signal.aborted);
}

function requireCurrentAccountCheckoutOperation(operation) {
  if (accountCheckoutOperationCurrent(operation)) return;
  const error = new Error('A newer checkout action replaced this request.');
  error.name = 'AbortError';
  throw error;
}

function finishAccountCheckoutOperation(operation) {
  if (accountCheckoutOperationCurrent(operation)) accountCheckoutRequestController = null;
}

function persistedAccountCheckoutPresentation(value) {
  const presentation = String(value || '').trim().toLowerCase();
  return ['embedded','hosted'].includes(presentation) ? presentation : '';
}

function embeddedCheckoutAbortMatchesActiveOperation(event) {
	const requestID = String(event?.detail?.request_id || '').trim();
	const activeRequestID = String(accountCheckoutActiveOperation?.embeddedReservation?.request_id || '').trim();
	return !requestID || requestID === activeRequestID;
}

function embeddedCheckoutCompletionMatchesActiveOperation(event) {
	const requestID = String(event?.detail?.request_id || '').trim();
	const sessionID = String(event?.detail?.session_id || '').trim();
	const activeRequestID = String(accountCheckoutActiveOperation?.embeddedReservation?.request_id || '').trim();
	if (requestID && requestID !== activeRequestID) return false;
	if (sessionID && accountCheckoutReconcileTarget.sessionID && sessionID !== accountCheckoutReconcileTarget.sessionID) return false;
	return true;
}

function completedAccountCheckoutSettled(target) {
	if (!target || !target.kind || !target.id) return false;
	if (target.kind === 'order') {
	  const order = Array.isArray(accountOrders)
		? accountOrders.find(entry => String(entry?.id || '').trim() === target.id)
		: null;
	  if (!order) return false;
	  const status = String(order.status || '').trim().toLowerCase();
	  const fulfilled = Number(order.fulfilled_license_count || 0);
	  return (Number.isFinite(fulfilled) && fulfilled > 0) ||
		['expired','payment_failed','refunded','disputed'].includes(status);
	}
	if (target.kind === 'subscription') {
	  const subscription = accountSnapshot?.subscription;
	  if (!subscription || String(subscription.id || '').trim() !== target.id) return false;
	  const status = String(subscription.status || '').trim().toLowerCase();
	  return subscription.has_access === true || subscription.terminal === true ||
		['active','trialing','canceled','expired','billing_mismatch'].includes(status);
	}
	return false;
}

async function reconcileCompletedAccountCheckout(target, delays=[0,400,900,1800,3200]) {
	const generation = ++accountCheckoutReconciliationGeneration;
	for (const rawDelay of delays) {
	  if (generation !== accountCheckoutReconciliationGeneration || document.visibilityState === 'hidden') return false;
	  const delay = Math.max(0, Number(rawDelay) || 0);
	  if (delay > 0) await new Promise(resolve => setTimeout(resolve, delay));
	  if (generation !== accountCheckoutReconciliationGeneration || document.visibilityState === 'hidden') return false;
	  await refreshAccountCosmetics('Stripe accepted the checkout. Arena is confirming the signed payment event before granting access.');
	  if (generation !== accountCheckoutReconciliationGeneration || document.visibilityState === 'hidden') return false;
	  if (completedAccountCheckoutSettled(target)) return true;
	}
	return false;
}

async function prepareAccountCheckoutPresentation(operation) {
  const controller = window.ArenaEmbeddedCheckout;
  if (controller && typeof controller.prepare === 'function') {
    try {
	  const reservation = await controller.prepare();
	  if (operation) {
		requireCurrentAccountCheckoutOperation(operation);
		operation.embeddedReservation = reservation;
	  }
      return 'embedded';
    } catch (error) {
	  if (operation && !accountCheckoutOperationCurrent(operation)) throw error;
      const useHosted = window.confirm(
        'The on-site payment window could not load. Open Stripe\'s secure hosted checkout instead?'
      );
      if (useHosted) return 'hosted';
      throw error;
    }
  }
  const useHosted = window.confirm(
    'This browser cannot open the on-site payment window. Open Stripe\'s secure hosted checkout instead?'
  );
  if (useHosted) return 'hosted';
  throw new Error('Secure checkout is unavailable in this browser.');
}

async function presentAccountCheckout(data, preparedPresentation='', operation=null) {
  if (operation) requireCurrentAccountCheckoutOperation(operation);
  const presentation = String(data?.presentation || '').trim();
  if (presentation === 'embedded') {
    const sessionID = String(data?.session_id || '').trim();
    const clientSecret = String(data?.client_secret || '').trim();
    if (!sessionID || !clientSecret || String(data?.checkout_url || '').trim()) {
      throw new Error('The checkout service did not return a valid embedded session.');
    }
    if (!window.ArenaEmbeddedCheckout || typeof window.ArenaEmbeddedCheckout.mount !== 'function') {
      throw new Error('The on-site payment window is unavailable.');
    }
    if (preparedPresentation !== 'embedded') {
      if (typeof window.ArenaEmbeddedCheckout.prepare !== 'function') {
        throw new Error('This on-site checkout session cannot be reopened in this browser.');
      }
	  const reservation = await window.ArenaEmbeddedCheckout.prepare();
	  if (operation) operation.embeddedReservation = reservation;
      if (operation) requireCurrentAccountCheckoutOperation(operation);
    }
    if (operation) requireCurrentAccountCheckoutOperation(operation);
	const orderID = String(data?.order_id || '').trim();
	const subscriptionID = String(data?.subscription_id || '').trim();
	accountCheckoutReconcileTarget = orderID
	  ? {kind:'order',id:orderID,sessionID}
	  : (subscriptionID ? {kind:'subscription',id:subscriptionID,sessionID} : {kind:'',id:'',sessionID});
	await window.ArenaEmbeddedCheckout.mount(data, operation?.embeddedReservation);
    if (operation) requireCurrentAccountCheckoutOperation(operation);
    return;
  }
  if (presentation !== 'hosted' || String(data?.client_secret || '').trim()) {
    throw new Error('The checkout service returned an invalid hosted session.');
  }
  const rawCheckoutURL = String(data?.checkout_url || '').trim();
  if (!rawCheckoutURL) throw new Error('The checkout service did not return a hosted redirect.');
  const checkoutURL = new URL(rawCheckoutURL, window.location.href);
  if (!['http:','https:'].includes(checkoutURL.protocol)) throw new Error('The checkout service returned an unsafe redirect.');
  if (operation) requireCurrentAccountCheckoutOperation(operation);
  if (preparedPresentation === 'embedded') {
	abortPreparedAccountCheckout('Continuing in Stripe secure checkout.', operation);
  }
  navigateAccount(checkoutURL.href);
}

function abortPreparedAccountCheckout(message, operation=null) {
  const controller = window.ArenaEmbeddedCheckout;
	if (controller && typeof controller.abort === 'function') {
	  controller.abort(message || 'Secure checkout could not continue.', operation?.embeddedReservation);
	}
}

async function reconcileClosedAccountCheckout(data, packID, operation) {
  if (data?.resumable !== false) return false;
  requireCurrentAccountCheckoutOperation(operation);
  const status = String(data?.checkout_status || '').trim().toLowerCase();
  const message = status === 'expired'
    ? 'That Stripe session expired. Arena is reconciling the signed expiry event before you try again.'
    : 'Stripe reports that checkout finished. Arena is confirming the signed payment event before changing your collection.';
  accountCheckoutState = {status:'reconciling',packID,message};
  renderAccountCosmetics();
  await refreshAccountCosmetics(message);
  requireCurrentAccountCheckoutOperation(operation);
  return true;
}

async function openAccountSubscription() {
  const offer = accountSnapshot?.subscription_offer?.enabled
    ? accountSnapshot.subscription_offer
    : accountCatalog?.subscription_offer;
  const intent = window.ArenaAccountCosmetics.subscriptionIntent(offer, accountSnapshot?.subscription);
  if (!intent.ok || accountSubscriptionState.status === 'pending') return;
  const operation = beginAccountCheckoutOperation();
  accountSubscriptionState = {status:'pending',message:''};
  renderAccountCosmetics();
  let presentation = '';
  try {
    if (intent.kind === 'portal') {
      const data = await accountRequest(intent.path, {method:'POST',signal:operation.controller.signal});
      requireCurrentAccountCheckoutOperation(operation);
      const rawURL = String(data?.portal_url || '').trim();
      if (!rawURL) throw new Error('The subscription portal did not return a redirect.');
      const destination = new URL(rawURL, window.location.href);
      if (!['http:','https:'].includes(destination.protocol)) throw new Error('The subscription service returned an unsafe redirect.');
      requireCurrentAccountCheckoutOperation(operation);
      navigateAccount(destination.href);
      return;
    }
    const subscription = accountSnapshot?.subscription;
    const subscriptionStatus = String(subscription?.status || '').trim().toLowerCase();
    const persistedPresentation = ['created','checkout_pending'].includes(subscriptionStatus)
      ? persistedAccountCheckoutPresentation(subscription?.checkout_presentation)
      : '';
    if (persistedPresentation === 'embedded') {
      if (!window.ArenaEmbeddedCheckout || typeof window.ArenaEmbeddedCheckout.prepare !== 'function') {
        throw new Error('This on-site subscription checkout cannot be reopened in this browser.');
      }
	  operation.embeddedReservation = await window.ArenaEmbeddedCheckout.prepare();
      requireCurrentAccountCheckoutOperation(operation);
      presentation = 'embedded';
    } else if (persistedPresentation === 'hosted') {
      presentation = 'hosted';
    } else {
	  presentation = await prepareAccountCheckoutPresentation(operation);
      requireCurrentAccountCheckoutOperation(operation);
    }
    const data = await accountRequest(intent.path, {
      method:'POST',body:JSON.stringify({presentation}),signal:operation.controller.signal,
    });
    requireCurrentAccountCheckoutOperation(operation);
    await presentAccountCheckout(data, presentation, operation);
    requireCurrentAccountCheckoutOperation(operation);
  } catch (error) {
    if (!accountCheckoutOperationCurrent(operation)) return;
	if (presentation === 'embedded') abortPreparedAccountCheckout(error?.message, operation);
    accountSubscriptionState = {status:'error',message:error?.message || 'Try again in a moment.'};
    renderAccountCosmetics();
    void refreshAccountCosmetics();
  } finally {
    finishAccountCheckoutOperation(operation);
  }
}

async function handleAccountPanelSubmit(event) {
  if (event.target?.id === 'accountKeyForm') {
    event.preventDefault();
    await createAccountKey(event.target);
    return;
  }
  if (event.target?.id !== 'linkBotForm') return;
  event.preventDefault();
  const input = event.target.querySelector('#linkBotKey');
  const submit = event.target.querySelector('button[type="submit"]');
  const rawKey = input?.value.trim() || '';
  if (!rawKey || submit?.disabled) return;
  submit.disabled = true;
  submit.textContent = 'Linking...';
  accountViewError = '';
  accountViewNotice = '';
  input.value = '';
  try {
    await accountRequest(window.ArenaAccountCosmetics.accountRoute('bots'), {method:'POST',body:JSON.stringify({api_key:rawKey})});
    await refreshAccountCosmetics('Bot linked. Cosmetic ownership remains with your email account.');
  } catch (e) {
    accountViewError = e.message || 'Could not link that bot.';
    renderAccountCosmetics();
  } finally {
    const currentSubmit = document.querySelector('#linkBotForm button[type="submit"]');
    if (currentSubmit) {
      currentSubmit.disabled = false;
      currentSubmit.textContent = 'Verify & link bot';
    }
  }
}

async function startCosmeticCheckout(packID) {
  const intent = window.ArenaAccountCosmetics.checkoutIntent(accountCatalog, packID);
  if (!intent.ok) {
    accountCheckoutState = {
      status: intent.reason === 'checkout-disabled' ? 'disabled' : 'error',
      packID,
      message: intent.reason === 'pack-not-purchasable'
        ? 'That set is not currently available for purchase.'
        : 'Checkout is not available for that set.',
    };
    renderAccountCosmetics();
    return;
  }
  if (accountCheckoutState.status === 'pending') return;
  const operation = beginAccountCheckoutOperation();
  accountCheckoutState = {status:'pending',packID,message:''};
  renderAccountCosmetics();
  let presentation = '';
  try {
	presentation = await prepareAccountCheckoutPresentation(operation);
    requireCurrentAccountCheckoutOperation(operation);
    const data = await accountRequest(intent.path, {
      method:'POST',
      body:JSON.stringify({...intent.body,presentation}),
	  signal:operation.controller.signal,
    });
    requireCurrentAccountCheckoutOperation(operation);
    if (await reconcileClosedAccountCheckout(data, packID, operation)) return;
    requireCurrentAccountCheckoutOperation(operation);
    await presentAccountCheckout(data, presentation, operation);
    requireCurrentAccountCheckoutOperation(operation);
  } catch (error) {
    if (!accountCheckoutOperationCurrent(operation)) return;
	if (presentation === 'embedded') abortPreparedAccountCheckout(error?.message, operation);
    accountCheckoutState = {status:'error',packID,message:error.message || 'Try again in a moment.'};
    renderAccountCosmetics();
	void refreshAccountCosmetics();
  } finally {
	finishAccountCheckoutOperation(operation);
  }
}

async function resumeCosmeticCheckout(orderID) {
  orderID = String(orderID || '').trim();
  if (!orderID || accountBusyOrderID) return;
  const order = Array.isArray(accountOrders) ? accountOrders.find(entry => String(entry?.id || '').trim() === orderID) : null;
	if (!order) return;
	const status = String(order.status || '').trim().toLowerCase();
	const checkoutSessionID = String(order.checkout_session_id || '').trim();
	const storedPresentation = persistedAccountCheckoutPresentation(order.checkout_presentation);
	const attachedSession = status === 'checkout_pending' && Boolean(checkoutSessionID);
	const retryableReservation = ['created','payment_failed'].includes(status) && !checkoutSessionID && Boolean(storedPresentation);
	if (!attachedSession && !retryableReservation) return;
  const packID = String(order.pack_id || '').trim();
	const operation = beginAccountCheckoutOperation();
  accountBusyOrderID = orderID;
  accountCheckoutState = {status:'pending',packID,message:''};
  renderAccountCosmetics();
  let embeddedPrepared = false;
  try {
	const data = await accountRequest(window.ArenaAccountCosmetics.accountRoute('orderCheckout', orderID), {
	  method:'POST',signal:operation.controller.signal,
	});
	requireCurrentAccountCheckoutOperation(operation);
	if (await reconcileClosedAccountCheckout(data, packID, operation)) return;
	requireCurrentAccountCheckoutOperation(operation);
    const presentation = String(data?.presentation || '').trim();
    if (presentation === 'embedded') {
      const controller = window.ArenaEmbeddedCheckout;
      if (!controller || typeof controller.prepare !== 'function') {
        throw new Error('This on-site checkout session cannot be reopened in this browser.');
      }
	  operation.embeddedReservation = await controller.prepare();
	  requireCurrentAccountCheckoutOperation(operation);
      embeddedPrepared = true;
    }
	await presentAccountCheckout(data, embeddedPrepared ? 'embedded' : '', operation);
	requireCurrentAccountCheckoutOperation(operation);
  } catch (error) {
	if (!accountCheckoutOperationCurrent(operation)) return;
	if (embeddedPrepared) abortPreparedAccountCheckout(error?.message, operation);
    accountCheckoutState = {status:'error',packID,message:error?.message || 'Could not resume that checkout.'};
  } finally {
	const current = accountCheckoutOperationCurrent(operation);
	finishAccountCheckoutOperation(operation);
	if (current && accountBusyOrderID === orderID) {
	  accountBusyOrderID = '';
	  renderAccountCosmetics();
	}
  }
}

async function assignAccountLicense(licenseID, botID) {
  const intent = window.ArenaAccountCosmetics.assignmentIntent(accountSnapshot, licenseID, botID);
  if (!intent.ok) {
    if (intent.reason !== 'already-assigned') {
      accountViewError = intent.reason === 'license-inactive'
        ? 'That license is no longer active and cannot be assigned.'
        : intent.reason === 'bot-key-inactive'
          ? 'That bot key is inactive. Link an active replacement bot before assigning this cosmetic.'
          : 'Choose a linked bot before assigning this cosmetic.';
      renderAccountCosmetics();
    }
    return;
  }
  accountBusyLicenseID = licenseID;
  accountViewError = '';
  accountViewNotice = '';
  renderAccountCosmetics();
  try {
    await accountRequest(window.ArenaAccountCosmetics.accountRoute('assignment', licenseID), {
      method:'PUT',
      body:JSON.stringify({bot_id:botID}),
    });
    accountBusyLicenseID = '';
    await refreshAccountCosmetics(intent.kind === 'move'
      ? 'Cosmetic moved. The previous bot can no longer use this license; equip it on the new bot when ready.'
      : 'Cosmetic assigned to the selected bot. Equip it when ready.');
  } catch (e) {
    accountBusyLicenseID = '';
    accountViewError = e.message || 'Could not assign that cosmetic.';
    renderAccountCosmetics();
  }
}

function previewAccountLicense(licenseID) {
  const license = accountSnapshot?.licenses.find(entry => entry.id === licenseID);
  if (license?.status !== 'active' || license?.item?.is_active !== true) return;
  if (!ACCOUNT_PREVIEW_SLOTS.includes(license.item.slot)) return;
  if (!accountPreviewBotID || !accountSnapshot?.bots.some(bot => bot.id === accountPreviewBotID)) {
    accountPreviewBotID = accountSnapshot?.bots[0]?.id || '';
  }
  if (!accountPreviewBotID) return;
  accountPreviewStagedBySlot = {
    ...accountPreviewStagedBySlot,
    [license.item.slot]: license.id,
  };
  renderAccountCosmeticsOutfitter();
  const reducedMotion = window.matchMedia?.('(prefers-reduced-motion: reduce)')?.matches === true;
  document.getElementById('accountCosmeticsOutfitter')?.scrollIntoView({
    behavior:reducedMotion ? 'auto' : 'smooth',
    block:'start',
  });
}

function resetAccountCosmeticsPreview() {
  accountPreviewStagedBySlot = {};
  renderAccountCosmeticsOutfitter();
}

async function equipAccountLicense(licenseID) {
  const license = accountSnapshot?.licenses.find(entry => entry.id === licenseID);
  if (license?.status !== 'active') {
    accountViewError = 'That license is no longer active and cannot be equipped.';
    renderAccountCosmetics();
    return;
  }
  if (!license?.assigned_bot_id) {
    accountViewError = 'Assign this cosmetic to a linked bot before equipping it.';
    renderAccountCosmetics();
    return;
  }
  const assignedBot = accountSnapshot?.bots.find(entry => entry.id === license.assigned_bot_id);
  if (!assignedBot?.key_is_active) {
    accountViewError = 'That bot key is inactive. Link an active replacement bot before equipping this cosmetic.';
    renderAccountCosmetics();
    return;
  }
  accountBusyLicenseID = licenseID;
  accountViewError = '';
  accountViewNotice = '';
  renderAccountCosmetics();
  try {
    await accountRequest(window.ArenaAccountCosmetics.accountRoute('equip', license.assigned_bot_id), {
      method:'PUT',
      body:JSON.stringify({license_id:licenseID}),
    });
    accountBusyLicenseID = '';
    await refreshAccountCosmetics('Cosmetic equipped. Any previous cosmetic in that bot slot was replaced explicitly.');
  } catch (e) {
    accountBusyLicenseID = '';
    accountViewError = e.message || 'Could not equip that cosmetic.';
    renderAccountCosmetics();
  }
}

async function unassignAccountLicense(licenseID) {
  accountBusyLicenseID = licenseID;
  accountViewError = '';
  accountViewNotice = '';
  renderAccountCosmetics();
  try {
    await accountRequest(window.ArenaAccountCosmetics.accountRoute('assignment', licenseID), {method:'DELETE'});
    accountBusyLicenseID = '';
    await refreshAccountCosmetics('Cosmetic removed from the bot. It remains owned by your email account.');
  } catch (e) {
    accountBusyLicenseID = '';
    accountViewError = e.message || 'Could not remove that cosmetic.';
    renderAccountCosmetics();
  }
}

async function unlinkAccountBot(botID) {
  const bot = accountSnapshot?.bots.find(entry => entry.id === botID);
  if (!bot || !confirm(`Unlink ${bot.name}? Assigned cosmetics will return to your account, but the API key itself will not be revoked.`)) return;
  accountViewError = '';
  accountViewNotice = '';
  try {
    await accountRequest(window.ArenaAccountCosmetics.accountRoute('bot', botID), {method:'DELETE'});
    await refreshAccountCosmetics('Bot unlinked. Its API key was not revoked, and your cosmetics remain on this account.');
  } catch (e) {
    accountViewError = e.message || 'Could not unlink that bot.';
    renderAccountCosmetics();
  }
}

function handleAccountPanelClick(event) {
  const gotoProfileButton = event.target.closest('[data-goto-profile]');
  if (gotoProfileButton) {
    activateTab('profile');
    return;
  }
  const openShopButton = event.target.closest('[data-open-shop]');
  if (openShopButton) {
    openArenaShop();
    return;
  }
  const previewButton = event.target.closest('[data-license-preview]');
  if (previewButton) {
    previewAccountLicense(previewButton.dataset.licensePreview);
    return;
  }
  const previewResetButton = event.target.closest('[data-cosmetics-preview-reset]');
  if (previewResetButton) {
    resetAccountCosmeticsPreview();
    return;
  }
  const subscriptionButton = event.target.closest('[data-subscription-checkout],[data-subscription-portal]');
  if (subscriptionButton) {
    openAccountSubscription();
    return;
  }
  const copyKeyButton = event.target.closest('[data-account-key-copy]');
  if (copyKeyButton) {
    copyGeneratedAccountKey();
    return;
  }
  const clearKeyButton = event.target.closest('[data-account-key-clear]');
  if (clearKeyButton) {
    clearGeneratedAccountKey();
    return;
  }
  const revokeKeyButton = event.target.closest('[data-account-key-revoke]');
  if (revokeKeyButton) {
    revokeAccountKey(revokeKeyButton.dataset.accountKeyRevoke);
    return;
  }
  const retryKeysButton = event.target.closest('[data-account-keys-retry]');
  if (retryKeysButton) {
    accountKeysError = '';
    refreshAccountCosmetics();
    return;
  }
  const checkoutButton = event.target.closest('[data-pack-checkout]');
  if (checkoutButton) {
    startCosmeticCheckout(checkoutButton.dataset.packCheckout);
    return;
  }
  const resumeOrderButton = event.target.closest('[data-order-resume]');
  if (resumeOrderButton) {
    resumeCosmeticCheckout(resumeOrderButton.dataset.orderResume);
    return;
  }
  const retryButton = event.target.closest('[data-account-retry]');
  if (retryButton) {
    accountViewError = '';
    renderAccountCosmetics();
    refreshAccountCosmetics();
    return;
  }
  const assignButton = event.target.closest('[data-license-assign]');
  if (assignButton) {
    const licenseID = assignButton.dataset.licenseAssign;
    const select = [...document.querySelectorAll('[data-license-target]')]
      .find(node => node.dataset.licenseTarget === licenseID);
    assignAccountLicense(licenseID, select?.value || '');
    return;
  }
  const unassignButton = event.target.closest('[data-license-unassign]');
  if (unassignButton) {
    unassignAccountLicense(unassignButton.dataset.licenseUnassign);
    return;
  }
  const equipButton = event.target.closest('[data-license-equip]');
  if (equipButton) {
    equipAccountLicense(equipButton.dataset.licenseEquip);
    return;
  }
  const unlinkButton = event.target.closest('[data-bot-unlink]');
  if (unlinkButton) unlinkAccountBot(unlinkButton.dataset.botUnlink);
}

function handleAccountPanelChange(event) {
  const previewBot = event.target.closest('#accountCosmeticsPreviewBot');
  if (previewBot) {
    accountPreviewBotID = previewBot.value;
    accountPreviewStagedBySlot = {};
    renderAccountCosmeticsOutfitter();
    return;
  }
  const select = event.target.closest('[data-license-target]');
  if (!select) return;
  const card = select.closest('[data-license-id]');
  const button = card?.querySelector('[data-license-assign]');
  if (button) button.disabled = !select.value || accountBusyLicenseID === card.dataset.licenseId;
}

async function handleEmbeddedCheckoutComplete(event) {
	if (!embeddedCheckoutCompletionMatchesActiveOperation(event)) return;
	invalidateAccountCheckoutOperation();
	const sessionID = String(event?.detail?.session_id || '').trim();
	const target = sessionID && accountCheckoutReconcileTarget.sessionID === sessionID
	  ? {...accountCheckoutReconcileTarget}
	  : {kind:'',id:'',sessionID};
  accountCheckoutState = {status:'success',packID:'',message:''};
  accountSubscriptionState = {status:'idle',message:''};
  renderAccountCosmetics();
	if (target.kind) await reconcileCompletedAccountCheckout(target);
	else await refreshAccountCosmetics('Stripe accepted the checkout. Arena is confirming the signed payment event before granting access.');
}

async function handleEmbeddedCheckoutAbort(event) {
	if (!embeddedCheckoutAbortMatchesActiveOperation(event)) return;
	invalidateAccountCheckoutOperation();
	cancelAccountCheckoutReconciliation();
  const sessionID = String(event?.detail?.session_id || '').trim();
  if (accountSubscriptionState.status === 'pending') {
    accountSubscriptionState = {status:'idle',message:''};
    accountViewNotice = sessionID
      ? 'Subscription checkout closed. Reopen All Access when you are ready; Stripe will reuse the pending session.'
      : '';
  } else {
    accountCheckoutState = sessionID
      ? {
          status:'paused',
          packID:accountCheckoutState.packID || '',
          message:'Resume the same Stripe session from Recent purchases when you are ready.',
        }
      : {status:'idle',packID:'',message:''};
  }
  renderAccountCosmetics();
  if (sessionID) await refreshAccountCosmetics(accountViewNotice || 'Checkout saved. Resume the same Stripe session from Recent purchases when you are ready.');
}

function bindAccountCosmeticsUI() {
  document.getElementById('accountSignInButton').addEventListener('click', startAccountLogin);
  document.getElementById('accountLogoutBtn').addEventListener('click', signOutAccount);
  // Linked bots and API keys render into #tab-profile now (see
  // renderAccountBotsAndKeys), but their submit/click/change handlers below
  // are unchanged -- attach the same delegated listeners to both panels
  // rather than duplicating the handlers themselves.
  for (const panel of [document.getElementById('tab-cosmetics'), document.getElementById('tab-profile')]) {
    if (!panel) continue;
    panel.addEventListener('submit', handleAccountPanelSubmit);
    panel.addEventListener('click', handleAccountPanelClick);
    panel.addEventListener('change', handleAccountPanelChange);
  }
  window.addEventListener('pagehide', event => {
    if (event.persisted) return;
    accountCosmeticsPreviewController?.dispose();
    accountCosmeticsPreviewController = null;
  });
  window.addEventListener('pageshow', event => {
    if (event.persisted) renderAccountCosmeticsOutfitter();
  });
  window.addEventListener('arena:stripe-checkout:complete', handleEmbeddedCheckoutComplete);
  window.addEventListener('arena:stripe-checkout:abort', handleEmbeddedCheckoutAbort);
	document.addEventListener('visibilitychange', () => {
	  if (document.visibilityState === 'hidden') cancelAccountCheckoutReconciliation();
	});
}

async function initializeAccountMode() {
  if (DASHBOARD_VIEW !== 'private') return false;
  try {
    let payload = await accountRequest(window.ArenaAccountCosmetics.accountRoute('session'), {allowUnauthorized:true});
    if (!payload) return false;
    let normalized = window.ArenaAccountCosmetics.normalizeSession(payload);
    accountSession = {
      ...normalized,
      csrf_token: payload.csrf_token || payload.account?.csrf_token || '',
    };
    updateAccountLoginUI();
    document.getElementById('login').style.display='none';
    document.getElementById('app').style.display='block';
    apiKey = '';
    buildBotSwitcher();
    updatePrivateAuthUI();
    activateTab(resolveAccountLandingTab('cosmetics'));
    return true;
  } catch (e) {
    /*
     * The session document could not be read, so whether sign-in works is
     * unknown rather than known-broken. Offer the button anyway: pressing it
     * either works or produces a real error, and both are better than a
     * greyed control that cannot explain itself. Bot performance by API key
     * is unaffected and still available behind it.
     */
    accountSession.login_enabled = false;
    accountSession.oidc_login_enabled = false;
    const setup = document.getElementById('accountEmailSetup');
    if (setup) setup.hidden = true;
    const button = document.getElementById('accountSignInButton');
    button.hidden = false;
    button.disabled = false;
    button.textContent = 'Sign in with Angel Accounts';
    document.getElementById('loginError').textContent = 'Could not check Angel Accounts sign-in. Bot performance by API key is still available.';
    return false;
  }
}

// ========== Auth ==========
function doLogin() {
  const k = document.getElementById('apiKeyInput').value.trim();
  if (!k) return;
  apiKey = k;
  testAuth(document.getElementById('keyLabel').value.trim());
}
function loginWithKey(k) { apiKey = k; testAuth(); }
async function testAuth(label) {
  try {
    stats = await api('/bot/stats');
    saveKey(apiKey, label, stats.name);
    document.getElementById('login').style.display='none';
    document.getElementById('app').style.display='block';
    buildBotSwitcher();
    updatePrivateAuthUI();
    activateTab('overview');
    refreshAll();
  } catch(e) {
    apiKey='';
    if (hasVerifiedAccount()) {
      document.getElementById('login').style.display='none';
      document.getElementById('app').style.display='block';
      buildBotSwitcher();
      updatePrivateAuthUI();
      activateTab(resolveAccountLandingTab('cosmetics'));
      await refreshAccountCosmetics();
      accountViewError = 'That API key could not open bot performance. Your account-owned cosmetics are still available.';
      renderAccountCosmetics();
    } else {
      document.getElementById('loginError').textContent='Invalid API key';
    }
  }
}
function logout() {
  apiKey='';
  stats=null;
  if (hasVerifiedAccount()) {
    buildBotSwitcher();
    updatePrivateAuthUI();
    activateTab('cosmetics');
    return;
  }
  document.getElementById('app').style.display='none';
  document.getElementById('login').style.display='flex';
  updatePrivateAuthUI();
  renderSavedKeys();
}
function switchBot(key) {
  if (!key && hasVerifiedAccount()) {
    logout();
    return;
  }
  if (!key) return;
  apiKey=key;
  buildBotSwitcher();
  updatePrivateAuthUI();
  activateTab('overview');
  refreshAll();
}

function buildBotSwitcher() {
  const keys = getSavedKeys();
  const accountOption = hasVerifiedAccount() ? `<option value="" ${apiKey?'':'selected'}>Cosmetics - ${esc(accountSession.account.email)}</option>` : '';
  document.getElementById('botSwitcher').innerHTML = accountOption + keys.map(k => `<option value="${esc(k.key)}" ${k.key===apiKey?'selected':''}>Stats - ${esc(k.label||k.name||'Bot')}</option>`).join('');
  // Compare selects
  const opts = keys.map(k=>`<option value="${esc(k.key)}">${esc(k.label||k.name)}</option>`).join('');
  document.getElementById('cmpA').innerHTML = '<option value="">Select Bot A</option>'+opts;
  document.getElementById('cmpB').innerHTML = '<option value="">Select Bot B</option>'+opts;
}

// ========== Tabs ==========
function activateTab(tabName) {
  document.querySelectorAll('.tbtn').forEach(t => {
    const active = t.dataset.tab === tabName;
    t.classList.toggle('active', active);
    t.setAttribute('aria-selected', active ? 'true' : 'false');
    t.tabIndex = active ? 0 : -1;
  });
  document.querySelectorAll('.tpanel').forEach(p => {
    const active = p.id === 'tab-'+tabName;
    p.classList.toggle('active', active);
    p.setAttribute('role', 'tabpanel');
    p.setAttribute('aria-hidden', active ? 'false' : 'true');
  });
  const button = document.querySelector(`.tbtn[data-tab="${tabName}"]`);
  const panel = document.getElementById('tab-'+tabName);
  if (!button || !panel || button.style.display === 'none') return;
  renderAccountCosmeticsOutfitter();
  if (tabName==='weapons') renderWeapons();
  if (tabName==='simulator') initSim();
  if (tabName==='skills') renderSkills();
  if (tabName==='allbots') loadAllBots();
  // Linked bots and API keys render on the Profile tab now, fed by the same
  // account-cosmetics fetch as the Cosmetics tab -- landing directly on
  // Profile (e.g. a deep link, or simply clicking it first) must trigger it
  // too, or accountBotsPanel is stuck showing its loading state forever.
  if ((tabName==='cosmetics' || tabName==='profile') && hasVerifiedAccount()) refreshAccountCosmetics();
  if (tabName==='profile' && hasVerifiedAccount()) refreshAccountProfile();
  if (tabName==='strategy') renderStrategy();
  if (tabName==='builds') renderBuilds();
  if (tabName==='mapviewer') loadMapViewer();
  if (tabName==='metareport') loadMetaReport();
}

document.querySelectorAll('.tbtn').forEach(button => {
  button.addEventListener('click', () => activateTab(button.dataset.tab));
  button.addEventListener('keydown', event => {
    if (!['ArrowLeft','ArrowRight','Home','End'].includes(event.key)) return;
    const tabs = [...document.querySelectorAll(`.tbtn[data-view="${DASHBOARD_VIEW}"]`)]
      .filter(tab => tab.style.display !== 'none');
    if (!tabs.length) return;
    const current = Math.max(0, tabs.indexOf(button));
    const next = event.key === 'Home' ? 0
      : event.key === 'End' ? tabs.length - 1
        : (current + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
    event.preventDefault();
    activateTab(tabs[next].dataset.tab);
    tabs[next].focus();
  });
});

function initDashboardMode() {
  const isPublic = DASHBOARD_VIEW === 'public';
  document.body.classList.toggle('mode-public', isPublic);
  document.body.classList.toggle('mode-private', !isPublic);

  document.querySelectorAll('[data-view]').forEach((node) => {
    node.style.display = node.dataset.view === DASHBOARD_VIEW ? '' : 'none';
  });

  if (isPublic) {
    document.getElementById('login').style.display='none';
    document.getElementById('app').style.display='block';
    document.getElementById('botName').textContent='Toolkit';
    document.getElementById('onlineStatus').textContent='Public knowledge';
    document.getElementById('arenaPhase').textContent='No API key required';
    document.getElementById('botSwitcher').style.display='none';
    document.getElementById('refreshBtn').style.display='none';
    document.getElementById('logoutBtn').style.display='none';
    document.getElementById('statCards').style.display='none';
    startToolkitRefreshLoop();
    activateTab('simulator');
    return;
  }

  document.getElementById('login').style.display='flex';
  document.getElementById('app').style.display='none';
  document.getElementById('statCards').style.display='';
  document.getElementById('botSwitcher').style.display='';
  document.getElementById('refreshBtn').style.display='';
  document.getElementById('logoutBtn').style.display='';
  activateTab('overview');
}

// Mirrors setupExploreBrand in js/site-shell.js and m/mobile.js: the brand
// lockup doubles as the Explore dropdown trigger everywhere it appears.
// Modifier/non-left clicks fall through to the real href instead of opening
// the menu. Independent of startDashboard() below so it still works even if
// that async bootstrap fails partway through.
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

async function startDashboard() {
  try {
    window.ArenaPaths = await import('../js/paths.js?v=20260710a');
  } catch (error) {
    // getBase() has a synchronous root-/mount-aware fallback, so a transient
    // module-load failure must not leave the otherwise self-contained
    // dashboard blank.
    console.warn('Dashboard path helper unavailable; using local fallback', error);
  }
  initDashboardMode();
  bindAccountCosmeticsUI();
  bindAccountProfileUI();
  await initializeAccountMode();
  if (DASHBOARD_VIEW === 'private') watchAccountSessionAcrossTabs();
}

startDashboard();
setupExploreBrand();

function getActivePublicTab() {
  return document.querySelector('.tbtn.active[data-view="public"]')?.dataset.tab || 'simulator';
}

function startToolkitRefreshLoop() {
  if (toolkitRefreshTimer) return;
  toolkitRefreshTimer = setInterval(async () => {
    try {
      await ensureToolkitData(true);
      const active = getActivePublicTab();
      if (active === 'weapons') renderWeapons();
      if (active === 'simulator') updateSim();
      if (active === 'skills') renderSkills();
      if (active === 'builds') renderBuilds();
      if (active === 'strategy') renderStrategy();
    } catch (e) {}
  }, 20000);
}

// ========== Refresh ==========
async function refreshAll() {
  try {
    const [s, arena] = await Promise.all([api('/bot/stats'), pub('/arena/status').catch(()=>({}))]);
    stats = s;
    // Track Elo history
    trackElo(s.name, s.elo);

    document.getElementById('botName').textContent = s.name||'Bot';
    const isOnline = arena.bots_connected > 0;
    document.getElementById('onlineStatus').innerHTML = `<span class="online-dot" style="background:${isOnline?'var(--green)':'var(--text2)'}"></span> ${isOnline?'Arena Online':'Arena Offline'}`;
    document.getElementById('arenaPhase').textContent = (arena.status||'--') + ' | ' + (arena.bots_connected||0) + ' bots';

    renderCards(s);
    renderPerf(s);
    renderEloHistory(s.name);
    renderAnalysis(s);
  } catch(e) {}
}

// ========== Cards ==========
function renderCards(s) {
  const kd = s.deaths>0?(s.kills/s.deaths).toFixed(2):s.kills+'.00';
  const wr = s.rounds_played>0?((s.round_wins/s.rounds_played)*100).toFixed(1):'0.0';
  const avgD = s.rounds_played>0?Math.round(s.damage_dealt/s.rounds_played):0;
  const surv = s.rounds_played>0?Math.round(s.time_alive_seconds/s.rounds_played):0;
  const dmgR = s.damage_taken>0?(s.damage_dealt/s.damage_taken).toFixed(1):'--';
  document.getElementById('statCards').innerHTML = `
    <div class="card"><div class="label">Elo</div><div class="value gold">${s.elo||1000}</div><div class="sub">Rank #${s.rank||'--'}</div></div>
    <div class="card"><div class="label">K/D</div><div class="value ${parseFloat(kd)>=1.5?'green':parseFloat(kd)<0.8?'red':''}">${kd}</div><div class="sub">${s.kills}K / ${s.deaths}D</div></div>
    <div class="card"><div class="label">Win Rate</div><div class="value ${parseFloat(wr)>=15?'green':''}">${wr}%</div><div class="sub">${s.round_wins}W / ${s.rounds_played}R</div></div>
    <div class="card"><div class="label">Best Streak</div><div class="value purple">${s.best_streak||0}</div><div class="sub">Current: ${s.current_streak||0}</div></div>
    <div class="card"><div class="label">Avg Dmg/Round</div><div class="value">${avgD}</div><div class="sub">${s.damage_dealt} total</div></div>
    <div class="card"><div class="label">Avg Survival</div><div class="value">${surv}s</div><div class="sub">Best: ${s.longest_life_secs||0}s</div></div>
    <div class="card"><div class="label">Dmg Ratio</div><div class="value ${parseFloat(dmgR)>=1.5?'green':parseFloat(dmgR)<0.8?'red':''}">${dmgR}</div><div class="sub">Dealt/Taken</div></div>
    <div class="card"><div class="label">Pickups</div><div class="value">${s.pickups_collected||0}</div><div class="sub">${s.rounds_played>0?(s.pickups_collected/s.rounds_played).toFixed(1):0}/round</div></div>
  `;
}

// ========== Performance Bars ==========
function renderPerf(s) {
  const kd=s.deaths>0?s.kills/s.deaths:s.kills;
  const dr=s.damage_taken>0?s.damage_dealt/s.damage_taken:s.damage_dealt?99:0;
  const bars=[
    {l:'K/D',v:kd,m:5,c:kd>=1.5?'var(--green)':kd<0.8?'var(--red)':'var(--accent)'},
    {l:'Win %',v:s.rounds_played>0?s.round_wins/s.rounds_played*100:0,m:50,c:'var(--gold)'},
    {l:'Dmg Ratio',v:dr,m:3,c:dr>=1.5?'var(--green)':'var(--orange)'},
    {l:'Kills/Rnd',v:s.rounds_played>0?s.kills/s.rounds_played:0,m:5,c:'var(--accent)'},
    {l:'Pickups/R',v:s.rounds_played>0?s.pickups_collected/s.rounds_played:0,m:5,c:'var(--purple)'},
    {l:'Dist/Rnd',v:s.rounds_played>0?s.distance_traveled/s.rounds_played:0,m:2000,c:'var(--cyan)'},
  ];
  document.getElementById('perfBars').innerHTML = bars.map(b=>`<div class="srow"><span class="lbl">${b.l}</span><div class="bar-w"><div class="bar-f" style="width:${Math.min(100,b.v/b.m*100).toFixed(0)}%;background:${b.c}"></div></div><span class="val">${b.v.toFixed(1)}</span></div>`).join('');
}

// ========== Elo History ==========
function trackElo(name, elo) {
  const key = 'elo_history_'+name;
  let h; try { h=JSON.parse(localStorage.getItem(key)||'[]'); } catch(e) { h=[]; }
  const now = new Date().toLocaleTimeString([], {hour:'2-digit',minute:'2-digit'});
  if (!h.length || h[h.length-1].elo !== elo) h.push({time:now, elo});
  if (h.length > 50) h.shift();
  localStorage.setItem(key, JSON.stringify(h));
}
function renderEloHistory(name) {
  const key = 'elo_history_'+name;
  let h; try { h=JSON.parse(localStorage.getItem(key)||'[]'); } catch(e) { h=[]; }
  if (h.length < 2) { document.getElementById('eloHistory').innerHTML='<span style="color:var(--text2)">Refresh multiple times to build Elo history</span>'; return; }
  const min=Math.min(...h.map(e=>e.elo))-20, max=Math.max(...h.map(e=>e.elo))+20;
  const rng=max-min||1;
  let svg = `<svg width="100%" height="120" viewBox="0 0 ${h.length*20} 120" style="background:var(--bg);border:1px solid var(--border);border-radius:4px">`;
  // Line
  const pts = h.map((e,i)=>`${i*20+10},${110-((e.elo-min)/rng)*100}`).join(' ');
  svg += `<polyline points="${pts}" fill="none" stroke="var(--accent)" stroke-width="2"/>`;
  // Dots + labels
  h.forEach((e,i)=>{
    const y=110-((e.elo-min)/rng)*100;
    svg+=`<circle cx="${i*20+10}" cy="${y}" r="3" fill="var(--accent)"><title>${e.time}: ${e.elo}</title></circle>`;
    if (i===0||i===h.length-1) svg+=`<text x="${i*20+10}" y="${y-8}" fill="var(--text2)" font-size="9" text-anchor="middle">${e.elo}</text>`;
  });
  svg += '</svg>';
  const diff = h[h.length-1].elo - h[0].elo;
  document.getElementById('eloHistory').innerHTML = svg + `<div style="font-size:11px;color:var(--text2);margin-top:4px">Change: <b style="color:${diff>=0?'var(--green)':'var(--red)'}"> ${diff>=0?'+':''}${diff}</b> over ${h.length} samples</div>`;
}

// ========== Analysis ==========
function renderAnalysis(s) {
  const tips=[];
  const kd=s.deaths>0?s.kills/s.deaths:s.kills;
  if (kd<0.5&&s.rounds_played>=3) tips.push({t:'warn',m:'K/D below 0.5 — try more attack stats or a different weapon.'});
  if (kd>=2.0) tips.push({t:'good',m:'Excellent K/D ('+kd.toFixed(1)+')!'});
  if (s.rounds_played>=5&&s.round_wins===0) tips.push({t:'warn',m:'No wins in '+s.rounds_played+' rounds. Focus on survival — last alive wins.'});
  if (s.round_wins>0) tips.push({t:'good',m:'Win rate: '+(s.round_wins/s.rounds_played*100).toFixed(0)+'%'});
  if (s.damage_taken>s.damage_dealt*1.5&&s.rounds_played>=3) tips.push({t:'warn',m:'Taking 1.5x more damage than dealing. More defense/dodge or try shield.'});
  if (s.damage_dealt>s.damage_taken*2) tips.push({t:'good',m:'Great damage efficiency — 2x dealt vs taken.'});
  if (s.pickups_collected<s.rounds_played*0.5&&s.rounds_played>=3) tips.push({t:'warn',m:'Low pickup collection ('+s.pickups_collected+'/'+s.rounds_played+' rounds). Pickups are huge advantages.'});
  if (s.best_streak>=5) tips.push({t:'good',m:'Impressive best streak of '+s.best_streak+'!'});
  if (s.longest_life_secs<30&&s.rounds_played>=3) tips.push({t:'warn',m:'Short average life. Invest in HP/defense or use dodge more.'});
  if (!tips.length) tips.push({t:'tip',m:'Play more rounds for personalized analysis!'});
  document.getElementById('analysis').innerHTML=tips.map(t=>`<div class="tip ${t.t==='warn'?'warn':t.t==='good'?'good':''}">${t.m}</div>`).join('');
}

// ========== Budget Helper (shared by simulator) ==========
function updBudget(pfx) {
  let t=0;['hp','speed','attack','defense'].forEach(s=>{const v=parseInt(document.getElementById(pfx+'_'+s).value);document.getElementById(pfx+'V_'+s).textContent=v;t+=v;});
  document.getElementById(pfx+'Budget').innerHTML=`<b style="color:${t===20?'var(--green)':'var(--red)'}"> ${t}/20</b>${t===20?'':' - must equal 20'}`;
  return t;
}

// ========== Stat Simulator ==========
async function initSim() {
  await ensureToolkitData().catch(() => {});
  const sts=['hp','speed','attack','defense'];
  if (!simInitialized) {
    document.getElementById('simStats').innerHTML=sts.map(st=>`<div class="srow"><span class="lbl">${st}</span><input type="range" min="1" max="10" value="5" id="sim_${st}" oninput="updateSim()" style="flex:1;accent-color:var(--accent)"><span class="val" id="simV_${st}">5</span></div>`).join('');
    document.getElementById('simBudget').innerHTML='';
    simInitialized = true;
  }
  const selected = document.getElementById('simWeapon').value || 'sword';
  document.getElementById('simWeapon').innerHTML = getWeaponIds().map(id => {
    const bundle = getWeaponBundle(id);
    return `<option value="${id}" ${id===selected?'selected':''}>${bundle.name}</option>`;
  }).join('');
  updateSim();
}
async function updateSim() {
  await ensureToolkitData().catch(() => {});
  const t=updBudget('sim');
  const hp=parseInt(document.getElementById('sim_hp').value);
  const spd=parseInt(document.getElementById('sim_speed').value);
  const atk=parseInt(document.getElementById('sim_attack').value);
  const def=parseInt(document.getElementById('sim_defense').value);
  const w=document.getElementById('simWeapon').value;
  const wc=getWeaponBundle(w);

  const maxHP=100+hp*10, moveSpd=3+spd*0.5, atkMult=1+atk*0.1, defRed=def*0.03;
  const baseDamage=Number(wc.damage || wc.appliedDamage || 0);
  const dmgPerHit=baseDamage*atkMult;
  const dps=dmgPerHit/Number(wc.cooldown || 1);
  const ehp=maxHP/(1-defRed); // effective HP accounting for defense

  document.getElementById('simDerived').innerHTML=`
    <div class="srow"><span class="lbl">Max HP</span><div class="bar-w"><div class="bar-f" style="width:${(maxHP/200*100).toFixed(0)}%;background:var(--green)"></div></div><span class="val">${maxHP}</span></div>
    <div class="srow"><span class="lbl">Speed</span><div class="bar-w"><div class="bar-f" style="width:${(moveSpd/8*100).toFixed(0)}%;background:var(--cyan)"></div></div><span class="val">${moveSpd.toFixed(1)}</span></div>
    <div class="srow"><span class="lbl">Atk Mult</span><div class="bar-w"><div class="bar-f" style="width:${(atkMult/2*100).toFixed(0)}%;background:var(--orange)"></div></div><span class="val">${atkMult.toFixed(1)}x</span></div>
    <div class="srow"><span class="lbl">Def Red</span><div class="bar-w"><div class="bar-f" style="width:${(defRed/0.3*100).toFixed(0)}%;background:var(--purple)"></div></div><span class="val">${(defRed*100).toFixed(0)}%</span></div>
    <div class="srow"><span class="lbl">Effective HP</span><div class="bar-w"><div class="bar-f" style="width:${Math.min(100,ehp/300*100).toFixed(0)}%;background:var(--gold)"></div></div><span class="val">${ehp.toFixed(0)}</span></div>
  `;

  // DPS table vs different defense levels
  let dpsHTML='<table><thead><tr><th>Enemy Def</th><th>Dmg/Hit</th><th>DPS</th><th>Hits to Kill (150hp)</th></tr></thead><tbody>';
  [0,3,5,7,10].forEach(d=>{
    const dr=d*0.03;
    const dmg=baseDamage*atkMult*(1-dr);
    const hits=Math.ceil(150/dmg);
    dpsHTML+=`<tr><td>${d}</td><td>${dmg.toFixed(2)}</td><td>${(dmg/Number(wc.cooldown || 1)).toFixed(2)}</td><td>${hits} (${(hits*Number(wc.cooldown || 1)).toFixed(1)}s)</td></tr>`;
  });
  dpsHTML+='</tbody></table>';
  if(w==='shield') dpsHTML+='<div style="font-size:10px;color:var(--text2);margin-top:4px">Note: Shield halves incoming damage passively</div>';
  if(w==='daggers') dpsHTML+='<div style="font-size:10px;color:var(--text2);margin-top:4px">Note: Daggers spike hardest from rear-angle backstabs and stunned targets.</div>';
  if(w==='grapple') dpsHTML+='<div style="font-size:10px;color:var(--text2);margin-top:4px">Note: Grapple also creates value through pulls, spacing, and anchor movement.</div>';
  document.getElementById('simDPS').innerHTML=dpsHTML;

  // Recommendations
  document.getElementById('simRecommend').innerHTML=`
    <div style="font-size:11px"><b style="color:var(--orange)">${wc.name}</b> starter split: ${wc.build}</div>
    <div style="font-size:10px;color:var(--text2);margin-top:5px">Live balance: ${baseDamage.toFixed(2)} damage (${wc.appliedDamage} applied) | ${Number(wc.cooldown || 0).toFixed(2)}s cooldown | ${wc.rangeTiles ?? '--'} tile reach</div>
    <div style="font-size:10px;color:var(--text2);margin-top:5px">Budget status: ${t===20?'legal loadout':'adjust sliders until the budget reaches 20/20'}.</div>
  `;
}

// ========== Weapons ==========
async function renderWeapons() {
  await ensureToolkitData().catch(() => {});
  let html='';
  for(const weaponId of getWeaponIds()){
    const w=getWeaponBundle(weaponId);
    html+=`<div class="weapon-card"><div class="icon">${weaponIconSvg(weaponId, 30)}</div><div style="flex:1">
      <div style="display:flex;justify-content:space-between;gap:10px;align-items:flex-start;flex-wrap:wrap">
        <div>
          <div class="wname">${w.name}</div>
          <div class="wprop">${w.role} ${w.desc}</div>
        </div>
        <div class="pill">${w.tier} tier</div>
      </div>
      <div class="pill-row">
        <span class="pill">Damage ${Number(w.damage || 0).toFixed(2)}</span>
        <span class="pill">Applied ${w.appliedDamage ?? '--'}</span>
        <span class="pill">Cooldown ${Number(w.cooldown || 0).toFixed(2)}s</span>
        <span class="pill">Range ${w.rangeTiles ?? '--'} tiles</span>
        <span class="pill">Special ${w.special}</span>
        <span class="pill">${w.kills} kills</span>
      </div>
      <div style="font-size:11px;margin-top:8px">Basic starter: ${w.build}</div>
      <div style="font-size:10px;color:var(--text2);margin-top:4px">Strong vs <span style="color:var(--green)">${w.strongVs}</span> | Watch for <span style="color:var(--red)">${w.counter}</span>${w.recentForm!=null ? ` | Recent form ${w.recentForm.toFixed(0)}` : ''}</div>
    </div></div>`;
  }
  document.getElementById('weaponGuide').innerHTML=html || '<span style="color:var(--text2)">No weapon data available.</span>';
}

// ========== Leaderboard ==========
async function loadLB() {
  try{
    const period=document.getElementById('lbPeriod').value||'all_time';
    const d=await pub('/leaderboard?sort='+document.getElementById('lbSort').value+'&limit=50&period='+period);
    const e=d.leaderboard||d.entries||[];
    const me=stats?.name||'';
    document.getElementById('lbBody').innerHTML=e.map(r=>{
      const kd=r.deaths>0?(r.kills/r.deaths).toFixed(1):r.kills+'.0';
      const isMe=r.name===me;
      return `<tr style="${isMe?'background:rgba(240,192,64,0.08)':''}"><td style="color:var(--accent)">${r.rank}</td><td>${isMe?'<b style="color:var(--gold)">★ ':''}${esc(r.name)}${isMe?'</b>':''}</td><td>${r.kills}</td><td>${r.deaths}</td><td>${kd}</td><td>${r.elo}</td><td style="color:var(--orange)">${r.best_streak}</td><td>${r.rounds_played||0}</td><td style="color:var(--green)">${r.round_wins||0}</td></tr>`;
    }).join('');
  }catch(e){}
}

// ========== Compare Bots ==========
async function runCompare() {
  const kA=document.getElementById('cmpA').value, kB=document.getElementById('cmpB').value;
  if(!kA||!kB){document.getElementById('compareResult').innerHTML='<span style="color:var(--text2)">Select both bots</span>';return;}
  try{
    const[a,b]=await Promise.all([
      fetch(getBase()+'/bot/stats',{headers:{'X-Arena-Key':kA}}).then(r=>r.json()),
      fetch(getBase()+'/bot/stats',{headers:{'X-Arena-Key':kB}}).then(r=>r.json())
    ]);
    const fields=[
      {l:'Elo',k:'elo',higher:true},{l:'Kills',k:'kills',higher:true},{l:'Deaths',k:'deaths',higher:false},
      {l:'K/D',k:null,fn:s=>s.deaths>0?s.kills/s.deaths:s.kills,higher:true,fmt:v=>v.toFixed(2)},
      {l:'Win Rate',k:null,fn:s=>s.rounds_played>0?s.round_wins/s.rounds_played*100:0,higher:true,fmt:v=>v.toFixed(1)+'%'},
      {l:'Damage Dealt',k:'damage_dealt',higher:true},{l:'Damage Taken',k:'damage_taken',higher:false},
      {l:'Best Streak',k:'best_streak',higher:true},{l:'Rounds',k:'rounds_played',higher:true},
      {l:'Wins',k:'round_wins',higher:true},{l:'Pickups',k:'pickups_collected',higher:true},
    ];
    let html=`<div class="compare-grid"><div class="col" style="text-align:center;color:var(--accent);font-weight:bold">${esc(a.name)}</div><div class="lbl"></div><div class="col" style="text-align:center;color:var(--purple);font-weight:bold">${esc(b.name)}</div>`;
    fields.forEach(f=>{
      const va=f.fn?f.fn(a):(a[f.k]||0);
      const vb=f.fn?f.fn(b):(b[f.k]||0);
      const fmt=f.fmt||(v=>typeof v==='number'?v.toLocaleString():v);
      const aBetter=f.higher?(va>vb):(va<vb);
      const bBetter=f.higher?(vb>va):(vb<va);
      html+=`<div class="col ${aBetter?'better':bBetter?'worse':''}">${fmt(va)}</div><div class="lbl">${f.l}</div><div class="col ${bBetter?'better':aBetter?'worse':''}">${fmt(vb)}</div>`;
    });
    html+='</div>';
    document.getElementById('compareResult').innerHTML=html;
  }catch(e){document.getElementById('compareResult').innerHTML='<span style="color:var(--red)">Failed to load</span>';}
}

// ========== All My Bots ==========
async function loadAllBots() {
  const keys=getSavedKeys();
  const results=[];
  for(const k of keys){
    try{
      const s=await fetch(getBase()+'/bot/stats',{headers:{'X-Arena-Key':k.key}}).then(r=>r.json());
      results.push({...s,_key:k.key,_label:k.label});
    }catch(e){results.push({name:k.label||'?',_key:k.key,_error:true});}
  }
  document.getElementById('allBotsBody').innerHTML=results.map(s=>{
    if(s._error) return `<tr style="opacity:0.4"><td>${esc(s.name)}</td><td colspan="7" style="color:var(--red)">Key invalid</td><td><button class="sm danger" onclick="removeKey('${esc(s._key)}');loadAllBots()">Remove</button></td></tr>`;
    const kd=s.deaths>0?(s.kills/s.deaths).toFixed(1):s.kills+'.0';
    const isActive=s._key===apiKey;
    return `<tr style="${isActive?'background:rgba(88,166,255,0.06)':''}"><td>${isActive?'<b style="color:var(--accent)">':''}${esc(s.name)}${isActive?'</b>':''}</td><td>${s.elo}</td><td>${kd}</td><td>${s.kills}</td><td>${s.deaths}</td><td style="color:var(--green)">${s.round_wins||0}</td><td>${s.rounds_played||0}</td><td style="color:var(--orange)">${s.best_streak||0}</td><td><button class="sm" onclick="switchBot('${esc(s._key)}')">View</button></td></tr>`;
  }).join('');
}

// ========== Code Reference ==========
function renderCodebook() {
  const actions=[
    {name:'move',desc:'Move in a direction',json:`{"type":"action","tick":N,"action":"move","direction":[dx,dy]}`},
    {name:'move_to',desc:'Pathfind to position (avoids obstacles)',json:`{"type":"action","tick":N,"action":"move_to","target_position":[x,y]}`},
    {name:'attack',desc:'Attack a target bot',json:`{"type":"action","tick":N,"action":"attack","target":"bot-uuid"}`},
    {name:'attack (charged bow)',desc:'Spend stored bow charge on a stronger arrow',json:`{"type":"action","tick":N,"action":"attack","target":"bot-uuid","charged":true}`},
    {name:'attack (staff cast)',desc:'Place a delayed staff burst at a grid position',json:`{"type":"action","tick":N,"action":"attack","target_position":[x,y]}`},
    {name:'dodge',desc:'Dodge roll (3 tick invuln, 30 tick cd)',json:`{"type":"action","tick":N,"action":"dodge","direction":[dx,dy]}`},
    {name:'shove',desc:'Knockback + stun (separate cooldown)',json:`{"type":"action","tick":N,"action":"shove","target":"bot-uuid"}`},
    {name:'use_item',desc:'Collect a nearby pickup',json:`{"type":"action","tick":N,"action":"use_item","item_id":"pickup-uuid"}`},
    {name:'idle',desc:'Do nothing this tick',json:`{"type":"action","tick":N,"action":"idle"}`},
  ];
  document.getElementById('codeRef').innerHTML=actions.map(a=>`<div style="margin-bottom:10px"><b style="color:var(--orange)">${a.name}</b> — ${a.desc}<div class="code-block">${esc(a.json)}</div></div>`).join('');

  document.getElementById('codeSnippets').innerHTML=`
<h3>Tactical Reads</h3>
<div class="code-block"># Nearby enemies expose extra combat hints:
# enemy["rear_exposed"]           -> good dagger backstab angle
# enemy["near_impact_surface"]    -> strong grapple slam candidate
# enemy["recently_disrupted_ticks"] -> shield bash bonus window
# state["your_state"]["brace_ready"] -> spear brace is primed
# state["your_state"]["charged_shot_ready"] -> bow can cash out a charged arrow</div>

<h3>Dodge When Hit</h3>
<div class="code-block">hits = state["your_state"].get("hits_received", [])
if hits and state["your_state"]["dodge_cooldown"] <= 0:
    # Dodge perpendicular to attacker
    enemy = next((e for e in nearby if e["id"] == hits[0]["attacker_id"]), None)
    if enemy:
        dx, dy = my_pos[0]-enemy["position"][0], my_pos[1]-enemy["position"][1]
        return {"type":"action","tick":tick,"action":"dodge","direction":[-dy,dx]}</div>

<h3>Kite at Optimal Range</h3>
<div class="code-block">optimal = weapon_range * 0.7
if enemy_dist < optimal * 0.4:
    # Too close — retreat
    return {"type":"action","tick":tick,"action":"move","direction":away_dir}
elif enemy_dist <= weapon_range and weapon_ready:
    return {"type":"action","tick":tick,"action":"attack","target":enemy_id}
elif enemy_dist <= weapon_range:
    # On cooldown — strafe
    return {"type":"action","tick":tick,"action":"move","direction":perp_dir}
else:
    return {"type":"action","tick":tick,"action":"move_to","target_position":enemy_pos}</div>

<h3>Collect Pickups</h3>
<div class="code-block">pickups = [e for e in nearby if e["type"] == "pickup"]
for p in sorted(pickups, key=lambda p: distance(my_pos, p["position"])):
    d = distance(my_pos, p["position"])
    if d < 12:  # Collection range
        return {"type":"action","tick":tick,"action":"use_item","item_id":p["id"]}
    elif d < 20:
        return {"type":"action","tick":tick,"action":"move_to","target_position":p["position"]}</div>

<h3>Use Hints When No Enemies Visible</h3>
<div class="code-block">if not enemies and "hints" in state:
    for hint in state["hints"]:
        if hint["hint_type"] == "bot":
            target = [my_pos[0]+hint["direction"][0]*hint["distance"],
                      my_pos[1]+hint["direction"][1]*hint["distance"]]
            return {"type":"action","tick":tick,"action":"move_to","target_position":target}</div>
  `;
}

// ========== Strategy ==========
function renderStrategy() {
  const weaponTips = getWeaponIds().map((weaponId) => {
    const w = getWeaponBundle(weaponId);
    return `<div class="tip"><b><span class="weapon-inline-icon">${weaponIconSvg(weaponId, 14)}</span>${w.name}:</b> ${w.desc} Role: ${w.role} Starter: ${w.build}. Strong vs ${w.strongVs}. Watch for ${w.counter}.</div>`;
  }).join('');
  document.getElementById('strategyTips').innerHTML=`
<div class="tip"><b>Stat Budget:</b> 20 points across HP, Speed, Attack, Defense (1-10 each). Specialize — don't spread evenly!</div>
<div class="tip"><b>Dodge:</b> 3 ticks invulnerable, 30 tick cooldown. Use reactively (when hit) or offensively (gap close). Dodge perpendicular for unpredictability.</div>
<div class="tip"><b>Shove:</b> Knockback + brief stun. Use when weapon is on cooldown for free damage windows.</div>
<div class="tip"><b>Pickups:</b> Health (+30), Damage Boost (1.5x/5s), Speed Boost (2x/5s), Shield (50 HP absorb), Cooldown Shard (60% cooldowns/10s), Bounty Token (+18 next kill), Hazard Key (hazard immunity + faster pad capture), Overdrive Core (1.25x damage + faster cooldowns), Grapple Charge (+1 grapple, cooldown reset), Relay Battery (+1 capture progress/tick for 9s). Always grab nearby pickups — they're massive advantages.</div>
<div class="tip"><b>Zone:</b> Shrinks over time, 3 HP/tick outside. Use <code>zone_target_center</code> to anticipate where it's going.</div>
<div class="tip"><b>Hints:</b> When no enemies in view radius, server sends directional hints to bots and pickups. Follow them to find fights!</div>
<div class="tip"><b>Cooldown Management:</b> Check <code>weapon_ready</code> before attacking. While on cooldown, strafe perpendicular to enemies — don't waste ticks standing still.</div>
<div class="tip"><b>Target Selection:</b> Focus low HP enemies (<code>hp</code>). Prioritize stunned targets (<code>is_stunned</code>). Skip dodging targets (<code>is_dodging</code> = invulnerable).</div>
<div class="tip"><b>Pathfinding:</b> Use <code>move_to</code> for long-range navigation (server pathfinds around obstacles). Use <code>move</code> only for short strafing/retreating.</div>
<div class="tip"><b>Winning:</b> Last alive wins, not most kills. Sometimes retreating and letting others fight is the best strategy.</div>

<h3 style="margin-top:14px;color:var(--accent)">Weapon Tips</h3>
${weaponTips}
  `;
}

function esc(s){if(s==null)return'';const d=document.createElement('div');d.textContent=String(s);return d.innerHTML;}

// ========== Live Action Radar ==========
let radarInterval = null;
const ACTION_KEYS = ['attack','move','move_to','dodge','shove','use_item','idle'];
const ACTION_LABELS = ['Attack','Move','Move To','Dodge','Shove','Use Item','Idle'];
const ACTION_COLORS = ['#f85149','#58a6ff','#39d353','#d29922','#bc8cff','#3fb950','#8b949e'];

function toggleRadar() {
  if (radarInterval) { clearInterval(radarInterval); radarInterval=null; document.getElementById('radarToggle').textContent='Start'; document.getElementById('radarStatus').textContent='Stopped'; return; }
  document.getElementById('radarToggle').textContent='Stop';
  fetchRadar();
  radarInterval = setInterval(fetchRadar, 1500);
}

async function fetchRadar() {
  try {
    const d = await api('/bot/live');
    if (!d.online) {
      document.getElementById('radarStatus').innerHTML='<span style="color:var(--text2)">Bot is offline — connect your bot to see live actions</span>';
      drawRadar({});
      document.getElementById('radarStats').innerHTML='';
      return;
    }
    drawRadar(d.action_counts || {});
    const alive = d.is_alive;
    document.getElementById('radarStatus').innerHTML=`<span class="online-dot" style="background:${alive?'var(--green)':'var(--red)'}"></span> ${alive?'Alive':'Dead'} | ${d.phase||'--'} | HP: ${Math.round(d.hp||0)}/${Math.round(d.max_hp||0)}`;
    document.getElementById('radarStats').innerHTML=`
      <div style="display:grid;grid-template-columns:1fr 1fr 1fr;gap:4px;font-size:10px">
        <div>K/D: <b>${d.round_kills||0}/${d.round_deaths||0}</b></div>
        <div>Dmg: <b style="color:var(--green)">${Math.round(d.round_damage_dealt||0)}</b>/<b style="color:var(--red)">${Math.round(d.round_damage_taken||0)}</b></div>
        <div>Acc: <b>${(d.accuracy||0).toFixed(0)}%</b></div>
        <div>Streak: <b style="color:var(--orange)">${d.kill_streak||0}</b></div>
        <div>Pickups: <b>${d.round_pickups||0}</b></div>
        <div>Dist: <b>${Math.round(d.round_distance||0)}</b></div>
      </div>
    `;
  } catch(e) {
    document.getElementById('radarStatus').innerHTML='<span style="color:var(--red)">Error fetching live data</span>';
  }
}

function drawRadar(counts) {
  const svg = document.getElementById('radarSvg');
  // Filter to only actions that have been used (or show all for shape)
  const axes = ACTION_KEYS;
  const labels = ACTION_LABELS;
  const colors = ACTION_COLORS;
  const n = axes.length;
  const R = 95;

  // Find max count for scaling
  let maxCount = 1;
  axes.forEach(a => { if ((counts[a]||0) > maxCount) maxCount = counts[a]; });
  const total = axes.reduce((s,a) => s + (counts[a]||0), 0) || 1;

  function pt(i, r) {
    const angle = (Math.PI * 2 * i / n) - Math.PI / 2;
    return [r * Math.cos(angle), r * Math.sin(angle)];
  }

  let html = '';

  // Grid rings with % labels
  [25, 50, 75, 100].forEach(pct => {
    const r = pct / 100 * R;
    const pts = Array.from({length:n}, (_,i) => pt(i,r).join(',')).join(' ');
    html += `<polygon points="${pts}" fill="none" stroke="var(--border)" stroke-width="0.5"/>`;
  });

  // Axis lines + labels
  for (let i = 0; i < n; i++) {
    const [x,y] = pt(i, R);
    html += `<line x1="0" y1="0" x2="${x}" y2="${y}" stroke="var(--border)" stroke-width="0.5"/>`;
    const [lx,ly] = pt(i, R + 20);
    const count = counts[axes[i]] || 0;
    const pct = (count / total * 100).toFixed(0);
    html += `<text x="${lx}" y="${ly}" fill="${colors[i]}" font-size="8" font-weight="bold" text-anchor="middle" dominant-baseline="middle">${labels[i]}</text>`;
    html += `<text x="${lx}" y="${ly+10}" fill="var(--text2)" font-size="7" text-anchor="middle">${count} (${pct}%)</text>`;
  }

  // Data polygon
  const dataPts = axes.map((a,i) => {
    const v = maxCount > 0 ? (counts[a]||0) / maxCount * R : 0;
    return pt(i, v).join(',');
  }).join(' ');
  html += `<polygon points="${dataPts}" fill="rgba(88,166,255,0.12)" stroke="var(--accent)" stroke-width="2"/>`;

  // Data dots
  axes.forEach((a,i) => {
    const v = maxCount > 0 ? (counts[a]||0) / maxCount * R : 0;
    const [x,y] = pt(i, v);
    html += `<circle cx="${x}" cy="${y}" r="3.5" fill="${colors[i]}"><title>${labels[i]}: ${counts[a]||0} (${(((counts[a]||0)/total)*100).toFixed(1)}%)</title></circle>`;
  });

  // Center label — total actions
  html += `<text x="0" y="0" fill="var(--text2)" font-size="9" text-anchor="middle" dominant-baseline="middle">${total} actions</text>`;

  svg.innerHTML = html;
}

drawRadar({});

// ========== Build Templates ==========
const BUILD_TEMPLATES = [
  {name:'Glass Cannon Staff',weapon:'staff',stats:{hp:4,speed:6,attack:9,defense:1},
   style:'AoE kiter — max damage, place area attacks on groups, dodge to survive',
   strong:'Shield, Sword (AoE bypasses melee range)',counter:'Daggers (burst before detonation), fast bots',
   code:`await ws.send(json.dumps({
    "type": "select_loadout",
    "weapon": "staff",
    "stats": {"hp": 4, "speed": 6, "attack": 9, "defense": 1},
    "fallback_behavior": "aggressive"
}))

# Strategy: Keep distance, drop AoE on clusters
if enemies and state["weapon_ready"]:
    # Target the enemy with most nearby allies for splash
    target = min(enemies, key=lambda e: dist(my_pos, e["position"]))
    send_action("attack", target=target["bot_id"])
elif enemies:
    # Kite away while on cooldown
    nearest = min(enemies, key=lambda e: dist(my_pos, e["position"]))
    dx, dy = my_pos[0]-nearest["position"][0], my_pos[1]-nearest["position"][1]
    send_action("move", direction=[dx, dy])`},
  {name:'Tank Shield',weapon:'shield',stats:{hp:8,speed:2,attack:3,defense:7},
   style:'Unkillable wall — 50% block + high defense + massive HP pool = outlast everyone',
   strong:'Daggers (halves their DPS), Sword (block reduces cleave)',counter:'Staff (AoE ignores positioning), Spear (knockback)',
   code:`await ws.send(json.dumps({
    "type": "select_loadout",
    "weapon": "shield",
    "stats": {"hp": 8, "speed": 2, "attack": 3, "defense": 7},
    "fallback_behavior": "defensive"
}))

# Strategy: Stay in zone center, let enemies come to you
if not state["in_safe_zone"]:
    send_action("move_to", target_position=safe_zone["center"])
elif enemies and state["weapon_ready"]:
    nearest = min(enemies, key=lambda e: dist(my_pos, e["position"]))
    if dist(my_pos, nearest["position"]) <= 2:
        send_action("attack", target=nearest["bot_id"])
    else:
        send_action("idle")  # Let them come to you`},
  {name:'Speed Assassin',weapon:'daggers',stats:{hp:3,speed:9,attack:7,defense:1},
   style:'Close gap fast, slip behind targets, and burst with rear-angle backstabs',
   strong:'Bow, Staff (close gap before they react)',counter:'Sword (cleave AoE), Spear (knockback keeps distance)',
   code:`await ws.send(json.dumps({
    "type": "select_loadout",
    "weapon": "daggers",
    "stats": {"hp": 3, "speed": 9, "attack": 7, "defense": 1},
    "fallback_behavior": "aggressive"
}))

# Strategy: Rush lowest HP enemy, dodge if taking hits
low_hp = sorted(enemies, key=lambda e: e["hp"])
if low_hp and state["weapon_ready"]:
    target = low_hp[0]
    if dist(my_pos, target["position"]) <= 2:
        send_action("attack", target=target["bot_id"])
    else:
        send_action("move_to", target_position=target["position"])
elif state["hits_received"] and state["dodge_cooldown"] == 0:
    send_action("dodge", direction=[random.choice([-1,1]), random.choice([-1,1])])`},
  {name:'Sniper Bow',weapon:'bow',stats:{hp:4,speed:6,attack:9,defense:1},
   style:'Long range poke — stay at max range, kite melee bots, high damage arrows',
   strong:'Shield, Spear (outranges both by miles)',counter:'Daggers (too fast, closes gap)',
   code:`await ws.send(json.dumps({
    "type": "select_loadout",
    "weapon": "bow",
    "stats": {"hp": 4, "speed": 6, "attack": 9, "defense": 1},
    "fallback_behavior": "opportunistic"
}))

# Strategy: Keep 5-7 tile distance, fire arrows
optimal_range = 5
if enemies and state["weapon_ready"]:
    target = min(enemies, key=lambda e: e["hp"])  # Focus weak
    d = dist(my_pos, target["position"])
    if d <= 7:
        send_action("attack", target=target["bot_id"])
    else:
        send_action("move_to", target_position=target["position"])
elif enemies:
    nearest = min(enemies, key=lambda e: dist(my_pos, e["position"]))
    if dist(my_pos, nearest["position"]) < 3:
        # Too close — retreat
        dx, dy = my_pos[0]-nearest["position"][0], my_pos[1]-nearest["position"][1]
        send_action("move", direction=[dx, dy])`},
  {name:'Balanced Sword',weapon:'sword',stats:{hp:5,speed:5,attack:6,defense:4},
   style:'Jack of all trades — cleave hits multiple enemies, solid in all situations',
   strong:'Daggers, Shield (cleave bypasses block range)',counter:'Bow, Staff (outranged)',
   code:`await ws.send(json.dumps({
    "type": "select_loadout",
    "weapon": "sword",
    "stats": {"hp": 5, "speed": 5, "attack": 6, "defense": 4},
    "fallback_behavior": "aggressive"
}))

# Strategy: Engage groups for cleave value, collect pickups
if enemies and state["weapon_ready"]:
    # Prefer clusters (cleave hits multiple)
    nearest = min(enemies, key=lambda e: dist(my_pos, e["position"]))
    send_action("attack", target=nearest["bot_id"])
elif pickups:
    nearest_p = min(pickups, key=lambda p: dist(my_pos, p["position"]))
    if dist(my_pos, nearest_p["position"]) <= 2:
        send_action("use_item", item_id=nearest_p["pickup_id"])
    else:
        send_action("move_to", target_position=nearest_p["position"])`},
  {name:'Knockback Spear',weapon:'spear',stats:{hp:5,speed:5,attack:7,defense:3},
   style:'Mid-range control — 2-tile reach + knockback keeps enemies at bay, push into walls for bonus damage',
   strong:'Daggers, Shield (knockback prevents close-in)',counter:'Bow (outranged)',
   code:`await ws.send(json.dumps({
    "type": "select_loadout",
    "weapon": "spear",
    "stats": {"hp": 5, "speed": 5, "attack": 7, "defense": 3},
    "fallback_behavior": "aggressive"
}))

# Strategy: Use knockback to push enemies out of zone or into walls
if enemies and state["weapon_ready"]:
    nearest = min(enemies, key=lambda e: dist(my_pos, e["position"]))
    if dist(my_pos, nearest["position"]) <= 3:
        send_action("attack", target=nearest["bot_id"])
    else:
        send_action("move_to", target_position=nearest["position"])
elif enemies and state.get("shove_cooldown", 1) <= 0:
    nearest = min(enemies, key=lambda e: dist(my_pos, e["position"]))
    if dist(my_pos, nearest["position"]) <= 2:
        send_action("shove", target=nearest["bot_id"])`},
];

const TOOLKIT_BUILD_TEMPLATES = [
  {name:'Basic Sword Starter',weapon:'sword',stats:{hp:5,speed:5,attack:5,defense:5},style:'Simple balanced melee starter. Walk into range, swing when ready, and keep drifting toward the zone.',strong:'General purpose',counter:'Dedicated range pressure',code:`await ws.send(json.dumps({
    "type": "select_loadout",
    "weapon": "sword",
    "stats": {"hp": 5, "speed": 5, "attack": 5, "defense": 5},
    "fallback_behavior": "aggressive"
}))

if not state["in_safe_zone"]:
    send_action("move_to", target_position=safe_zone["center"])
elif enemies and state["weapon_ready"]:
    send_action("attack", target=min(enemies, key=lambda e: dist(my_pos, e["position"]))["bot_id"])
elif enemies:
    send_action("move_to", target_position=min(enemies, key=lambda e: dist(my_pos, e["position"]))["position"])`},
  {name:'Basic Bow Starter',weapon:'bow',stats:{hp:4,speed:6,attack:6,defense:4},style:'Basic ranged starter. Take open shots and back away when melee gets too close.',strong:'Open sightlines',counter:'Fast gap closers',code:`await ws.send(json.dumps({
    "type": "select_loadout",
    "weapon": "bow",
    "stats": {"hp": 4, "speed": 6, "attack": 6, "defense": 4},
    "fallback_behavior": "opportunistic"
}))

if enemies and state["weapon_ready"]:
    send_action("attack", target=min(enemies, key=lambda e: e["hp"])["bot_id"])
elif enemies:
    nearest = min(enemies, key=lambda e: dist(my_pos, e["position"]))
    if dist(my_pos, nearest["position"]) < 3:
        send_action("move", direction=[my_pos[0]-nearest["position"][0], my_pos[1]-nearest["position"][1]])
    else:
        send_action("move_to", target_position=nearest["position"])`},
  {name:'Basic Daggers Starter',weapon:'daggers',stats:{hp:4,speed:6,attack:6,defense:4},style:'Basic rush-down starter. Close space quickly and stay on one target.',strong:'Catching ranged bots',counter:'Knockback and cleave',code:`await ws.send(json.dumps({
    "type": "select_loadout",
    "weapon": "daggers",
    "stats": {"hp": 4, "speed": 6, "attack": 6, "defense": 4},
    "fallback_behavior": "aggressive"
}))

if enemies and state["weapon_ready"]:
    send_action("attack", target=min(enemies, key=lambda e: dist(my_pos, e["position"]))["bot_id"])
elif enemies:
    send_action("move_to", target_position=min(enemies, key=lambda e: dist(my_pos, e["position"]))["position"])`},
  {name:'Basic Shield Starter',weapon:'shield',stats:{hp:7,speed:3,attack:4,defense:6},style:'Basic tank starter. Hold the zone and force nearby brawls.',strong:'Long trades',counter:'Area damage and kiting',code:`await ws.send(json.dumps({
    "type": "select_loadout",
    "weapon": "shield",
    "stats": {"hp": 7, "speed": 3, "attack": 4, "defense": 6},
    "fallback_behavior": "defensive"
}))

if not state["in_safe_zone"]:
    send_action("move_to", target_position=safe_zone["center"])
elif enemies and state["weapon_ready"]:
    send_action("attack", target=min(enemies, key=lambda e: dist(my_pos, e["position"]))["bot_id"])
elif enemies:
    send_action("move_to", target_position=min(enemies, key=lambda e: dist(my_pos, e["position"]))["position"])`},
  {name:'Basic Spear Starter',weapon:'spear',stats:{hp:5,speed:5,attack:6,defense:4},style:'Basic spacing starter. Poke from mid-range and keep moving.',strong:'Spacing control',counter:'Longer-range poke',code:`await ws.send(json.dumps({
    "type": "select_loadout",
    "weapon": "spear",
    "stats": {"hp": 5, "speed": 5, "attack": 6, "defense": 4},
    "fallback_behavior": "aggressive"
}))

if enemies and state["weapon_ready"]:
    send_action("attack", target=min(enemies, key=lambda e: dist(my_pos, e["position"]))["bot_id"])
elif enemies:
    send_action("move_to", target_position=min(enemies, key=lambda e: dist(my_pos, e["position"]))["position"])`},
  {name:'Basic Staff Starter',weapon:'staff',stats:{hp:4,speed:5,attack:6,defense:5},style:'Basic caster starter. Drop damage on the nearest threat and keep a little space.',strong:'Grouped enemies',counter:'Fast divers',code:`await ws.send(json.dumps({
    "type": "select_loadout",
    "weapon": "staff",
    "stats": {"hp": 4, "speed": 5, "attack": 6, "defense": 5},
    "fallback_behavior": "opportunistic"
}))

if enemies and state["weapon_ready"]:
    send_action("attack", target=min(enemies, key=lambda e: dist(my_pos, e["position"]))["bot_id"])
elif enemies:
    nearest = min(enemies, key=lambda e: dist(my_pos, e["position"]))
    send_action("move", direction=[my_pos[0]-nearest["position"][0], my_pos[1]-nearest["position"][1]])`},
  {name:'Basic Grapple Starter',weapon:'grapple',stats:{hp:5,speed:6,attack:5,defense:4},style:'Basic grapple starter. Use pulls for simple engage value and keep chasing.',strong:'Displacing ranged bots',counter:'Heavy bruisers',code:`await ws.send(json.dumps({
    "type": "select_loadout",
    "weapon": "grapple",
    "stats": {"hp": 5, "speed": 6, "attack": 5, "defense": 4},
    "fallback_behavior": "opportunistic"
}))

if enemies and state["weapon_ready"]:
    send_action("attack", target=min(enemies, key=lambda e: dist(my_pos, e["position"]))["bot_id"])
elif enemies and state.get("grapple_charges", 0) > 0:
    send_action("grapple", target=min(enemies, key=lambda e: dist(my_pos, e["position"]))["bot_id"])
elif enemies:
    send_action("move_to", target_position=min(enemies, key=lambda e: dist(my_pos, e["position"]))["position"])`}
];

async function renderBuilds() {
  await ensureToolkitData().catch(() => {});
  document.getElementById('buildTemplates').innerHTML = TOOLKIT_BUILD_TEMPLATES.map((b,i) => {
    const statsStr = Object.entries(b.stats).map(([k,v])=>`${k}:${v}`).join(' ');
    const bundle = getWeaponBundle(b.weapon);
    return `<div style="background:var(--bg);border:1px solid var(--border);border-radius:6px;padding:14px;margin-bottom:10px">
      <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:6px">
        <span style="color:var(--orange);font-weight:bold;font-size:14px">${esc(b.name)}</span>
        <span style="font-size:11px;color:var(--text2)">${esc(b.weapon.toUpperCase())} | ${statsStr}</span>
      </div>
      <div style="font-size:11px;margin-bottom:6px">${esc(b.style)}</div>
      <div style="font-size:10px;color:var(--text2);margin-bottom:6px">Current live tuning: ${Number(bundle.damage || 0).toFixed(2)} damage (${bundle.appliedDamage} applied) | ${Number(bundle.cooldown || 0).toFixed(2)}s cooldown | ${bundle.rangeTiles ?? '--'} tiles</div>
      <div style="font-size:10px;margin-bottom:4px"><span style="color:var(--green)">Strong vs:</span> ${esc(b.strong)}</div>
      <div style="font-size:10px;margin-bottom:8px"><span style="color:var(--red)">Countered by:</span> ${esc(b.counter)}</div>
      <div style="display:flex;align-items:center;gap:8px;margin-bottom:4px">
        <span style="font-size:10px;color:var(--text2)">Python Loadout + Strategy:</span>
        <button class="sm" onclick="copyBuildCode(${i})">Copy Code</button>
        <span id="buildCopy${i}" style="font-size:10px;color:var(--green)"></span>
      </div>
      <div class="build-code-shell">
        <div class="build-code-scroll" id="buildCode${i}">${esc(b.code)}</div>
      </div>
    </div>`;
  }).join('');
}
function copyBuildCode(i) {
  const code = TOOLKIT_BUILD_TEMPLATES[i].code;
  navigator.clipboard.writeText(code).then(() => {
    document.getElementById('buildCopy'+i).textContent = 'Copied!';
    setTimeout(() => { document.getElementById('buildCopy'+i).textContent = ''; }, 2000);
  });
}

// ========== Utility Skills ==========
async function renderSkills() {
  await ensureToolkitData().catch(() => {});
  const actionOrder = ['dodge','shove','place_mine','grapple','use_item','use_gravity_well','move_to'];
  const actions = actionOrder.map(name => TOOLKIT_STATE.actionsByName[name]).filter(Boolean);
    const featureLabels = {
      teleport_pads:'Teleport Pads',
      capture_pad:'Capture Pad',
      environmental_hazards:'Damage Zones',
      sudden_death:'Sudden Death',
      bounty_system:'Bounty Board',
    landmines:'Landmines',
    gravity_well:'Gravity Well'
  };
  const systems = TOOLKIT_STATE.botSetup?.game_mechanics?.new_features || {};

  let html = '<div class="utility-grid">';
  actions.forEach(action => {
    const fieldList = Object.entries(action.fields || {});
    html += `<div class="utility-card">
      <div class="pill">${action.name.replaceAll('_',' ')}</div>
      <h3>${titleCaseWord(action.name)}</h3>
      <div>${esc(action.description || 'Public arena action.')}</div>
      ${fieldList.length ? `<ul>${fieldList.map(([key, value]) => `<li><span class="mono">${esc(key)}</span>: ${esc(value)}</li>`).join('')}</ul>` : '<div class="mono" style="margin-top:8px">No payload fields required.</div>'}
    </div>`;
  });
  html += '</div>';

  html += '<div style="height:14px"></div><div class="utility-grid">';
  Object.entries(featureLabels).forEach(([key, label]) => {
    if (!systems[key]) return;
    html += `<div class="utility-card">
      <div class="pill">Arena System</div>
      <h3>${label}</h3>
      <div>${esc(systems[key])}</div>
    </div>`;
  });
  html += '</div>';

  document.getElementById('skillGuide').innerHTML = html;
}

// ========== Map Viewer ==========
let mapZoomLevel = 1.3;
let mapData = null;
async function loadMapViewer() {
  const status = document.getElementById('mapStatus');
  status.textContent = 'Fetching map...';
  try {
    const d = await pub('/arena/map');
    if (d.status === 'no_map' || !d.terrain) {
      status.innerHTML = '<span style="color:var(--orange)">No map available — the arena may be between rounds. Try again in a few seconds.</span>';
      const c = document.getElementById('mapCanvas');
      c.width = 300; c.height = 40;
      const ctx = c.getContext('2d');
      ctx.fillStyle = '#161b22';
      ctx.fillRect(0,0,300,40);
      ctx.fillStyle = '#8b949e';
      ctx.font = '12px monospace';
      ctx.fillText('No map data', 100, 24);
      return;
    }
    mapData = d;
    status.textContent = `Grid: ${d.width}x${d.height} cells | Cell size: ${d.cell_size}px | Arena: ${d.width*d.cell_size}x${d.height*d.cell_size} world units`
      + (d.map_shape ? ` | Shape: ${d.map_shape}` : '') + (d.game_mode ? ` | Mode: ${d.game_mode}` : '');
    drawMap();
  } catch(e) {
    status.innerHTML = '<span style="color:var(--red)">Failed to load map</span>';
  }
}
function mapZoom(dir) {
  if (dir === 0) mapZoomLevel = 1.3;
  else mapZoomLevel = Math.max(0.5, Math.min(2.5, mapZoomLevel + dir * 0.15));
  if (mapData) drawMap();
}
function drawMap() {
  if (!mapData || !mapData.terrain) return;
  const terrain = mapData.terrain;
  const rows = terrain.length;
  const cols = terrain[0] ? terrain[0].length : 0;
  const viewport = document.getElementById('mapViewport');
  const viewportWidth = Math.max(320, viewport?.clientWidth || 900);
  const viewportHeight = Math.max(320, viewport?.clientHeight || 600);
  const fitCellSize = Math.max(2, Math.floor(Math.min(viewportWidth / cols, viewportHeight / rows)));
  const cs = Math.max(2, Math.floor(fitCellSize * mapZoomLevel));
  const canvas = document.getElementById('mapCanvas');
  canvas.width = cols * cs;
  canvas.height = rows * cs;
  canvas.style.width = `${canvas.width}px`;
  canvas.style.height = `${canvas.height}px`;
  const ctx = canvas.getContext('2d');
  const COLORS = { '.': '#1a2332', '#': '#3d4f5f', '~': '#1a3a5c', 'V': '#0a0a0a' };
  for (let r = 0; r < rows; r++) {
    const row = terrain[r];
    for (let c = 0; c < cols; c++) {
      const ch = row[c] || '.';
      ctx.fillStyle = COLORS[ch] || '#1a2332';
      ctx.fillRect(c * cs, r * cs, cs, cs);
    }
  }
  // Grid lines at larger zoom
  if (cs >= 4) {
    ctx.strokeStyle = 'rgba(48,54,61,0.4)';
    ctx.lineWidth = 0.5;
    for (let r = 0; r <= rows; r++) { ctx.beginPath(); ctx.moveTo(0, r*cs); ctx.lineTo(cols*cs, r*cs); ctx.stroke(); }
    for (let c = 0; c <= cols; c++) { ctx.beginPath(); ctx.moveTo(c*cs, 0); ctx.lineTo(c*cs, rows*cs); ctx.stroke(); }
  }
}
window.addEventListener('resize', () => {
  if (mapData && document.getElementById('tab-mapviewer')?.classList.contains('active')) drawMap();
});

// ========== Meta Report ==========
async function loadMetaReport() {
  const el = document.getElementById('metaReport');
  el.innerHTML = '<span style="color:var(--text2)">Loading leaderboard data...</span>';
  try {
    const d = await pub('/leaderboard?limit=50&sort=elo&period=all_time');
    const entries = d.leaderboard || d.entries || [];
    if (!entries.length) { el.innerHTML = '<span style="color:var(--text2)">No leaderboard data yet</span>'; return; }

    // --- Weapon distribution ---
    // We don't have weapon data in leaderboard, so we use bot/live or show kills/elo analysis
    const top20 = entries.slice(0, 20);
    const top5 = entries.slice(0, 5);

    // Kills distribution as bar chart
    const maxKills = Math.max(...top20.map(e => e.kills)) || 1;
    const maxElo = Math.max(...top20.map(e => e.elo)) || 1;

    let html = '';

    // Top 5 spotlight
    html += '<h3 style="color:var(--accent);margin-bottom:8px">Top 5 Bots</h3>';
    html += '<div style="display:grid;grid-template-columns:repeat(auto-fill,minmax(180px,1fr));gap:8px;margin-bottom:16px">';
    top5.forEach((e,i) => {
      const kd = e.deaths > 0 ? (e.kills/e.deaths).toFixed(1) : e.kills+'.0';
      html += `<div style="background:var(--bg);border:1px solid ${i===0?'var(--gold)':'var(--border)'};border-radius:6px;padding:10px">
        <div style="color:${i===0?'var(--gold)':'var(--accent)'};font-weight:bold;font-size:13px">#${e.rank} ${esc(e.name)}</div>
        <div style="font-size:11px;color:var(--text2);margin-top:4px">Elo: <b style="color:var(--gold)">${e.elo}</b> | K/D: <b>${kd}</b></div>
        <div style="font-size:10px;color:var(--text2)">Kills: ${e.kills} | Wins: ${e.round_wins||0} | Streak: ${e.best_streak}</div>
      </div>`;
    });
    html += '</div>';

    // Elo distribution bar chart
    html += '<h3 style="color:var(--accent);margin-bottom:8px">Elo Distribution (Top 20)</h3>';
    html += '<div style="margin-bottom:16px">';
    top20.forEach(e => {
      const pct = ((e.elo - 800) / (maxElo - 800 || 1) * 100);
      html += `<div style="display:flex;align-items:center;gap:6px;margin-bottom:3px">
        <span style="min-width:100px;font-size:10px;color:var(--text2);text-align:right;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${esc(e.name)}</span>
        <div class="bar-w" style="height:10px"><div class="bar-f" style="width:${Math.max(2,pct).toFixed(0)}%;background:var(--gold)"></div></div>
        <span style="font-size:10px;min-width:35px">${e.elo}</span>
      </div>`;
    });
    html += '</div>';

    // Kill efficiency chart
    html += '<h3 style="color:var(--accent);margin-bottom:8px">Kill Efficiency (Top 20)</h3>';
    html += '<div style="margin-bottom:16px">';
    top20.forEach(e => {
      const kd = e.deaths > 0 ? e.kills/e.deaths : e.kills;
      const pct = Math.min(100, kd / 5 * 100);
      const color = kd >= 2 ? 'var(--green)' : kd >= 1 ? 'var(--accent)' : 'var(--red)';
      html += `<div style="display:flex;align-items:center;gap:6px;margin-bottom:3px">
        <span style="min-width:100px;font-size:10px;color:var(--text2);text-align:right;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${esc(e.name)}</span>
        <div class="bar-w" style="height:10px"><div class="bar-f" style="width:${Math.max(2,pct).toFixed(0)}%;background:${color}"></div></div>
        <span style="font-size:10px;min-width:35px">${kd.toFixed(1)}</span>
      </div>`;
    });
    html += '</div>';

    // Meta tips
    html += '<h3 style="color:var(--accent);margin-bottom:8px">Meta Snapshot</h3>';
    const avgElo = Math.round(top20.reduce((s,e)=>s+e.elo,0)/top20.length);
    const avgKills = Math.round(top20.reduce((s,e)=>s+e.kills,0)/top20.length);
    const bestStreaker = top20.reduce((a,b) => a.best_streak > b.best_streak ? a : b);
    const mostWins = top20.reduce((a,b) => (a.round_wins||0) > (b.round_wins||0) ? a : b);
    html += `<div class="tip"><b>Average top-20 Elo:</b> ${avgElo} | Average kills: ${avgKills}</div>`;
    html += `<div class="tip"><b>Streak king:</b> ${esc(bestStreaker.name)} with ${bestStreaker.best_streak}-kill streak</div>`;
    html += `<div class="tip"><b>Most wins:</b> ${esc(mostWins.name)} with ${mostWins.round_wins||0} round wins</div>`;

    // Tips based on meta
    if (avgKills > 50) html += '<div class="tip warn"><b>High-kill meta:</b> Damage builds are thriving. Consider a tank Shield build to counter.</div>';
    if (avgElo > 1100) html += '<div class="tip"><b>Competitive lobby:</b> Average Elo above 1100 — expect skilled opponents.</div>';
    html += '<div class="tip good"><b>Pro tip:</b> Check the Matchup Matrix in the Strategy tab to find counter-picks for the top bots\' weapons.</div>';

    el.innerHTML = html;
  } catch(e) {
    el.innerHTML = '<span style="color:var(--red)">Failed to load meta data</span>';
  }
}

// ========== Enhanced Strategy — Matchup Matrix ==========
const MATCHUPS = {
  // [attacker][defender] = {rating: -2 to +2, reason}
  // +2 = strong advantage, +1 = slight advantage, 0 = even, -1 = slight disadvantage, -2 = hard counter
  sword: {sword:{r:0,d:'Mirror — cleave vs cleave, comes down to stats'},bow:{r:-1,d:'Bow outranges sword. Must close gap to fight.'},daggers:{r:1,d:'Cleave hits dagger user + nearby allies. AoE advantage.'},shield:{r:1,d:'Cleave bypasses shield positioning, hits from sides.'},spear:{r:0,d:'Similar range. Spear knockback can disrupt, but sword has more DPS.'},staff:{r:-1,d:'Staff outranges and AoE zones sword out. Close gap fast or lose.'}},
  bow: {sword:{r:1,d:'Kite at range, sword can\'t touch you if you stay back.'},bow:{r:0,d:'Mirror — whoever has better aim and positioning wins.'},daggers:{r:-2,d:'Daggers close gap too fast, burst before bow can get shots off.'},shield:{r:1,d:'Outranges shield. Safe free damage from distance.'},spear:{r:1,d:'Outranges spear by 5 tiles. Easy kiting.'},staff:{r:0,d:'Both ranged. Staff has AoE, bow has faster projectiles. Even.'}},
  daggers:{sword:{r:-1,d:'Sword cleave hits daggers hard in group fights.'},bow:{r:2,d:'Close gap with speed, burst before bow can react. Hard counter.'},daggers:{r:0,d:'Mirror — pure speed and reflex contest.'},shield:{r:-1,d:'Shield blocks 50% of dagger hits. Tank outlasts assassin.'},spear:{r:-1,d:'Spear knockback keeps daggers at bay. Hard to stick on target.'},staff:{r:2,d:'Rush into melee range before AoE detonates. Staff is helpless up close.'}},
  shield:{sword:{r:-1,d:'Sword cleave from sides bypasses frontal block positioning.'},bow:{r:-1,d:'Bow kites and chips from range. Shield can\'t close gap.'},daggers:{r:1,d:'Block halves dagger DPS. Tank wins attrition.'},shield:{r:0,d:'Mirror — extremely long, boring fight. Lowest DPS vs lowest damage taken.'},spear:{r:-1,d:'Spear knockback disrupts shield and pushes out of position.'},staff:{r:-2,d:'Staff AoE ignores positioning entirely. Shield\'s block is useless vs area damage.'}},
  spear: {sword:{r:0,d:'Similar melee range. Knockback helps, but sword has cleave DPS.'},bow:{r:-1,d:'Bow outranges by 5 tiles. Spear can\'t reach.'},daggers:{r:1,d:'Knockback prevents daggers from sticking. Range advantage.'},shield:{r:1,d:'Knockback disrupts shield positioning. Push into zone damage.'},spear:{r:0,d:'Mirror — both knockback each other. Stalemately.'},staff:{r:-1,d:'Staff outranges spear. AoE zones out spear user.'}},
  staff: {sword:{r:1,d:'AoE zones out sword. Place impacts between you and them.'},bow:{r:0,d:'Both ranged. Bow has speed, staff has area control. Even matchup.'},daggers:{r:-2,d:'Daggers rush in and burst. AoE has 2-tick delay — too slow vs speed.'},shield:{r:2,d:'AoE bypasses block entirely. Shield has no answer to area damage.'},spear:{r:1,d:'Outranges spear. AoE denies spear\'s mid-range zone.'},staff:{r:0,d:'Mirror — area control contest. Positioning is everything.'}},
};
MATCHUPS.grapple = {
  sword:{r:-1,d:'Sword trades hard once grapple is forced into close range.'},
  bow:{r:2,d:'Grapple can yank ranged targets out of safe spacing.'},
  daggers:{r:0,d:'Both want fast close-range scraps. Positioning decides it.'},
  shield:{r:-1,d:'Shield survives the pull and drags the fight into a brawl.'},
  spear:{r:0,d:'Spear spacing can deny entries, but grapple can punish overextension.'},
  staff:{r:2,d:'Hooking casters before the blast lands is a strong punish.'},
  grapple:{r:0,d:'Mirror - whoever lands the better displacement first wins space.'}
};
MATCHUPS.sword.grapple = {r:1,d:'Sword likes grapple once the hook user is dragged into melee.'};
MATCHUPS.bow.grapple = {r:-2,d:'A landed hook or pull ruins bow spacing instantly.'};
MATCHUPS.daggers.grapple = {r:0,d:'Daggers can burst after a bad pull, but grapple can also peel them off.'};
MATCHUPS.shield.grapple = {r:1,d:'Shield survives the engage and punishes the follow-up fight.'};
MATCHUPS.spear.grapple = {r:0,d:'Spear spacing contests grapple entry, but neither side hard-counters.'};
MATCHUPS.staff.grapple = {r:-2,d:'Grapple can disrupt staff before the delayed hit resolves.'};
const WEAPON_ORDER = ['sword','bow','daggers','shield','spear','staff','grapple'];

function renderMatchupMatrix() {
  const ratingColor = r => r >= 2 ? '#3fb950' : r >= 1 ? '#2ea043' : r === 0 ? '#d29922' : r >= -1 ? '#da3633' : '#f85149';
  const ratingText = r => r >= 2 ? '++' : r >= 1 ? '+' : r === 0 ? '=' : r >= -1 ? '-' : '--';

  let html = '<div style="overflow-x:auto"><table style="border-collapse:collapse;text-align:center;font-size:11px">';
  html += '<thead><tr><th style="padding:6px;border:1px solid var(--border)">ATK \\ DEF</th>';
  WEAPON_ORDER.forEach(w => html += `<th style="padding:6px;border:1px solid var(--border)"><span class="weapon-inline-icon">${weaponIconSvg(w, 12)}</span>${w}</th>`);
  html += '</tr></thead><tbody>';

  WEAPON_ORDER.forEach(atk => {
    html += `<tr><td style="padding:6px;border:1px solid var(--border);font-weight:bold;text-align:left"><span class="weapon-inline-icon">${weaponIconSvg(atk, 12)}</span>${atk}</td>`;
    WEAPON_ORDER.forEach(def => {
      const m = MATCHUPS[atk][def];
      const bg = ratingColor(m.r);
      html += `<td style="padding:6px;border:1px solid var(--border);background:${bg}22;color:${bg};font-weight:bold;cursor:pointer" onclick="showMatchup('${atk}','${def}')">${ratingText(m.r)}</td>`;
    });
    html += '</tr>';
  });
  html += '</tbody></table></div>';
  html += '<div style="font-size:10px;color:var(--text2);margin-top:6px"><span style="color:#3fb950">++</span> strong win &nbsp; <span style="color:#2ea043">+</span> slight edge &nbsp; <span style="color:#d29922">=</span> even &nbsp; <span style="color:#da3633">-</span> slight loss &nbsp; <span style="color:#f85149">--</span> hard counter</div>';
  document.getElementById('matchupMatrix').innerHTML = html;
}
function showMatchup(atk, def) {
  const m = MATCHUPS[atk][def];
  const ratingColor = m.r >= 1 ? 'var(--green)' : m.r <= -1 ? 'var(--red)' : 'var(--orange)';
  document.getElementById('matchupDetail').innerHTML = `<div class="tip" style="border-color:${ratingColor}"><b><span class="weapon-inline-icon">${weaponIconSvg(atk, 12)}</span>${atk} vs <span class="weapon-inline-icon">${weaponIconSvg(def, 12)}</span>${def}:</b> ${m.d}</div>`;
}

// ========== Decision Tree ==========
function renderDecisionTree() {
  const node = (q, yes, no, indent=0) => {
    const pad = indent * 20;
    return `<div style="margin-left:${pad}px;margin-bottom:2px">
      <div style="display:flex;align-items:center;gap:6px">
        <span style="color:var(--accent);font-weight:bold;font-size:12px">${indent > 0 ? '->' : '>'}</span>
        <span style="font-size:12px">${q}</span>
      </div>
      ${yes ? `<div style="margin-left:${pad+20}px;font-size:11px;color:var(--green);margin:2px 0">[Y] YES -> ${yes}</div>` : ''}
      ${no ? `<div style="margin-left:${pad+20}px;font-size:11px;color:var(--red);margin:2px 0">[N] NO -> ${no}</div>` : ''}
    </div>`;
  };

  document.getElementById('decisionTree').innerHTML = `
    <div style="background:var(--bg);border:1px solid var(--border);border-radius:6px;padding:14px;font-family:monospace">
      ${node('Am I alive?', '', '<b>Wait for respawn / next round</b>')}
      ${node('Am I stunned?', '<b style="color:var(--orange)">IDLE</b> (can\'t act while stunned)', 'Continue...', 1)}
      ${node('Am I outside the safe zone?', '<code>move_to</code> zone center (priority!)', 'Continue...', 1)}
      ${node('Am I taking hits AND low HP (&lt;30%) AND dodge off cooldown?', '<code>dodge</code> away from attacker', 'Continue...', 1)}
      ${node('Any health packs nearby AND HP &lt; 50%?', '<code>use_item</code> or <code>move_to</code> health pack', 'Continue...', 1)}
      ${node('Any enemies visible?', 'Check weapon status...', 'Check for pickups...', 1)}
      ${node('Is weapon ready (cooldown = 0)?', '', '', 2)}
      ${node('Is nearest enemy in weapon range?', '<code>attack</code> lowest HP enemy', '<code>move_to</code> nearest enemy', 3)}
      ${node('Weapon on cooldown — shove available?', '<code>shove</code> nearest enemy (free stun)', 'Strafe/dodge perpendicular', 2)}
      ${node('No enemies — any pickups nearby?', '<code>use_item</code> or <code>move_to</code> pickup', '', 1)}
      ${node('Nothing to do?', '<code>idle</code> or patrol toward zone center', '', 1)}
    </div>
    <div style="font-size:10px;color:var(--text2);margin-top:8px">This is a basic priority loop. Advanced bots add: target selection (focus low HP), kiting at optimal range, dodge timing, pickup priority (damage boost &gt; health &gt; shield &gt; speed), zone prediction, and retreat logic.</div>
  `;
}

// Update renderStrategy to also render matrix and tree
const _origRenderStrategy = renderStrategy;
renderStrategy = function() {
  _origRenderStrategy();
  renderMatchupMatrix();
  renderDecisionTree();
};

