'use strict';

import '../js/babylon-runtime.js?v=20260810d';

import {
  MOBILE_SAFE_VIEWPORT_REGIONS,
  observeArenaSafeViewport,
} from '../js/safe-viewport.js?v=20260718b';
import { isSignedOut, signInAvailability, startSignIn, watchSignInState } from '../js/sign-in.js?v=20260825a';

/**
 * Mobile spectator shell — full-viewport 3D stage, floating top bar,
 * action cluster, and a draggable bottom sheet (Players / Kills / Ranks).
 *
 * Reuses the desktop renderer stack unchanged:
 *   ArenaEngine (3D scene), SpectatorSocket (WS + reconnect), Minimap.
 * @module m/mobile
 */

import { ArenaEngine } from '../js/renderer/engine.js?v=20260810e';
import { Minimap } from '../js/renderer/minimap.js?v=20260718c';
import { SpectatorSocket } from '../js/spectator-ws.js';
import { apiPath, appPath, wsURL } from '../js/paths.js?v=20260710a';
import { handleServiceStatus, initServiceStatus } from '../js/service-status.js?v=20260810c';
import { installClientErrorReporting } from '../js/client-errors.js?v=20260810e';

// Install before anything else so failures during startup are reported too.
installClientErrorReporting();

const ARENA_WIDTH = 2000;
const ARENA_HEIGHT = 2000;
const UI_UPDATE_INTERVAL_MS = 200; // DOM updates throttled below tick rate
const KILL_FEED_MAX = 30;
const BASE_CAMERA_RADIUS = 800;    // mirrors BASE_RADIUS in renderer/camera.js
const RANKS_REFRESH_MS = 30000;

// Matches the 3D flag/team palette (renderer + minimap).
const TEAM_COLORS = ['#4a8dff', '#ff4a40', '#4de669', '#ffd933'];
const TEAM_NAMES = ['Blue', 'Red', 'Green', 'Gold'];

const WEAPON_ICONS = {
  sword: '⚔',
  bow: '🏹',
  daggers: '🗡',
  shield: '🛡',
  spear: '🔱',
  staff: '🪄',
  grapple: '⛓',
};

const _ESC_MAP = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
function esc(value) {
  return String(value == null ? '???' : value).replace(/[&<>"']/g, (ch) => _ESC_MAP[ch]);
}

function titleCase(value) {
  return String(value || '')
    .split(/[\s_-]+/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(' ');
}

// The brand lockup doubles as the Explore dropdown trigger, linking across
// the other Angel Software Solutions properties. Modifier/non-left clicks
// fall through to the real href instead of opening the menu.
function setupExploreBrand() {
  const item = document.getElementById('arenaExploreItem');
  const toggle = document.getElementById('tb-brand');
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

const MOBILE_OVERLAY_SELECTOR = '.mobile-overlay';
const PRIORITY_MODAL_ROOT_SELECTOR = 'dialog, [role="alertdialog"][aria-modal="true"]';
const ACTIVE_PRIORITY_MODAL_SELECTOR = 'dialog[open], [role="alertdialog"][aria-modal="true"]';
const MOBILE_FOCUSABLE_SELECTOR = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  'iframe',
  '[contenteditable="true"]',
  '[tabindex]:not([tabindex="-1"])',
].join(',');

let mobileBackgroundInertState = null;
let mobileBackgroundObserver = null;

function isVisible(element) {
  if (!element || element.hidden || element.getAttribute('aria-hidden') === 'true') return false;
  const style = window.getComputedStyle(element);
  return style.display !== 'none' && style.visibility !== 'hidden' && element.getClientRects().length > 0;
}

function visibleFocusable(root) {
  if (!root) return [];
  return Array.from(root.querySelectorAll(MOBILE_FOCUSABLE_SELECTOR)).filter((element) => {
    if (element.closest('[inert], [hidden], [aria-hidden="true"]')) return false;
    return isVisible(element);
  });
}

function isPriorityModalRoot(element) {
  return element?.matches?.(PRIORITY_MODAL_ROOT_SELECTOR);
}

function isActivePriorityModalRoot(element) {
  return element?.matches?.(ACTIVE_PRIORITY_MODAL_SELECTOR) && isVisible(element);
}

function activePriorityModal() {
  const activeRoot = document.activeElement?.closest?.(ACTIVE_PRIORITY_MODAL_SELECTOR);
  if (activeRoot?.parentElement === document.body && isActivePriorityModalRoot(activeRoot)) return activeRoot;
  const roots = Array.from(document.body.children).filter(isActivePriorityModalRoot);
  return roots[roots.length - 1] || null;
}

function syncMobileBackgroundElement(element) {
  if (!mobileBackgroundInertState || element.parentElement !== document.body ||
      element.matches(MOBILE_OVERLAY_SELECTOR)) return;
  if (isPriorityModalRoot(element)) {
    if (mobileBackgroundInertState.has(element)) {
      element.inert = mobileBackgroundInertState.get(element);
      mobileBackgroundInertState.delete(element);
    }
    return;
  }
  if (!mobileBackgroundInertState.has(element)) {
    mobileBackgroundInertState.set(element, element.inert);
  }
  element.inert = true;
}

function applyMobileBackgroundInert() {
  if (!mobileBackgroundInertState) {
    mobileBackgroundInertState = new Map();
    mobileBackgroundObserver = new MutationObserver((records) => {
      const changedRoots = new Set();
      records.forEach((record) => {
        record.addedNodes.forEach((node) => {
          if (node.nodeType === Node.ELEMENT_NODE && node.parentElement === document.body) {
            changedRoots.add(node);
          }
        });
      });
      changedRoots.forEach(syncMobileBackgroundElement);
    });
    mobileBackgroundObserver.observe(document.body, { childList: true });
  }
  Array.from(document.body.children).forEach(syncMobileBackgroundElement);
}

function restoreMobileBackgroundInert() {
  if (!mobileBackgroundInertState) return;
  mobileBackgroundObserver?.disconnect();
  mobileBackgroundObserver = null;
  mobileBackgroundInertState.forEach((wasInert, element) => {
    if (element.isConnected) element.inert = wasInert;
  });
  mobileBackgroundInertState = null;
}

// Wires a single FAB <-> full-screen overlay pair: click toggles the
// overlay's "open" class (chat-panel.js watches #chat-overlay for exactly
// this), backdrop/close-button taps and Escape dismiss it, and opening one
// overlay closes any other so the mobile viewport never stacks two.
function setupMobileOverlay(overlayEl, fabEl, { onOpen, onClose, interceptPress } = {}) {
  if (!overlayEl || !fabEl) return null;
  const panel = overlayEl.querySelector('.mobile-overlay-panel');
  let returnFocus = null;

  overlayEl.inert = true;

  const close = ({ restoreFocus = true, releaseBackground = true } = {}) => {
    if (!overlayEl.classList.contains('open')) return;
    overlayEl.classList.remove('open');
    overlayEl.style.removeProperty('transition-property');
    overlayEl.style.removeProperty('visibility');
    overlayEl.setAttribute('aria-hidden', 'true');
    overlayEl.inert = true;
    fabEl.classList.remove('active');
    fabEl.setAttribute('aria-expanded', 'false');
    onClose?.();

    if (releaseBackground && !document.querySelector(`${MOBILE_OVERLAY_SELECTOR}.open`)) {
      restoreMobileBackgroundInert();
    }

    const target = returnFocus;
    returnFocus = null;
    if (restoreFocus && target?.isConnected) {
      requestAnimationFrame(() => {
        if (!document.querySelector(`${MOBILE_OVERLAY_SELECTOR}.open`) && target.isConnected) {
          target.focus();
        }
      });
    }
  };

  const open = () => {
    if (overlayEl.classList.contains('open')) return;
    const activeElement = document.activeElement;
    returnFocus = activeElement instanceof HTMLElement && !activeElement.closest(MOBILE_OVERLAY_SELECTOR)
      ? activeElement
      : fabEl;

    applyMobileBackgroundInert();
    document.querySelectorAll(`${MOBILE_OVERLAY_SELECTOR}.open`).forEach((other) => {
      if (other !== overlayEl) {
        other._mobileOverlayClose?.({ restoreFocus: false, releaseBackground: false });
      }
    });
    overlayEl.inert = false;
    // The CSS visibility transition otherwise keeps the newly opened panel
    // unfocusable through its first frame, even though opacity is animating.
    overlayEl.style.transitionProperty = 'opacity';
    overlayEl.style.visibility = 'visible';
    overlayEl.classList.add('open');
    overlayEl.setAttribute('aria-hidden', 'false');
    fabEl.classList.add('active');
    fabEl.setAttribute('aria-expanded', 'true');
    onOpen?.();

    requestAnimationFrame(() => {
      overlayEl.style.removeProperty('transition-property');
      if (!overlayEl.classList.contains('open')) return;
      const focusable = visibleFocusable(panel);
      const closeButton = overlayEl.querySelector('.mobile-overlay-close');
      const target = focusable.includes(closeButton) ? closeButton : focusable[0];
      target?.focus();
    });
  };

  const trapFocus = (event) => {
    if (event.key !== 'Tab' || !overlayEl.classList.contains('open')) return;
    const focusRoot = activePriorityModal() || panel;
    const focusable = visibleFocusable(focusRoot);
    if (focusable.length === 0) {
      event.preventDefault();
      return;
    }

    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    const activeElement = document.activeElement;
    if (!focusRoot?.contains(activeElement)) {
      event.preventDefault();
      first.focus();
    } else if (event.shiftKey && activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  };

  overlayEl._mobileOverlayClose = close;
  overlayEl.querySelectorAll('[data-mobile-overlay-close]').forEach((btn) => {
    btn.addEventListener('click', () => close());
  });
  fabEl.addEventListener('click', () => {
    if (overlayEl.classList.contains('open')) {
      close();
      return;
    }
    // A press may mean something else before it means "open this" -- see the
    // Dashboard FAB below, where a signed-out press opens the Accounts window
    // first. The intercept opens the overlay itself when it is done.
    if (interceptPress?.(open)) return;
    open();
  });
  document.addEventListener('keydown', trapFocus);

  return { open, close };
}

// Sets the iframe's src from data-src the first time its overlay opens, so
// an idle mobile visitor never loads it. Shared by the Dashboard and Shop
// overlays, which are otherwise identical lazy-iframe drawers. Returns the
// {open, close} handle so callers can drive it from elsewhere (see the
// window.ArenaOpen*/ArenaCloseOverlay hooks below).
function setupLazyFrameOverlay(overlayId, fabId, { interceptPress } = {}) {
  const overlay = document.getElementById(overlayId);
  const frame = overlay?.querySelector('iframe[data-src]');
  return setupMobileOverlay(overlay, document.getElementById(fabId), {
    interceptPress,
    onOpen: () => {
      if (frame && !frame.getAttribute('src')) {
        frame.setAttribute('src', appPath(frame.dataset.src));
      }
    },
  });
}

/*
 * The mobile half of app.js's openDashboardFromPress, and for the same
 * reason: the Dashboard drawer is how somebody signs in here too, and it used
 * to open on a screen asking which of two ways they wanted. A signed-out
 * press starts the Accounts window itself; the drawer opens after it, whatever
 * the window did, so nothing about the press can dead-end.
 *
 * Returns false -- "not handled, open normally" -- whenever the session is
 * still unknown or sign-in is not configured on this Arena, because a window
 * opened after a fetch is a window the browser blocks.
 */
function interceptDashboardPressForSignIn(open) {
  if (!isSignedOut() || signInAvailability() !== 'available') return false;
  startSignIn().finally(open);
  return true;
}

// Chat FAB opens #chat-overlay, whose markup/ids match ../js/chat-panel.js
// exactly (that module is unmodified and self-wires on DOMContentLoaded).
function setupChatAndDashboard() {
  // Read the session now, so a later press already knows what it means.
  watchSignInState();
  setupMobileOverlay(document.getElementById('chat-overlay'), document.getElementById('fab-chat'));
  const dashboardOverlay = setupLazyFrameOverlay('dashboard-overlay', 'fab-dashboard', {
    interceptPress: interceptDashboardPressForSignIn,
  });
  const shopOverlay = setupLazyFrameOverlay('shop-overlay', 'fab-shop');

  // Mirrors app.js's openDashboardOverlay/window.ArenaOpen*/ArenaCloseOverlay
  // hooks for the mobile shell: the embedded Shop iframe (frontend/shop/)
  // calls these same-origin globals instead of navigating with ?dash_open=1
  // or a plain link, which would otherwise reload this whole mobile page
  // just to switch drawers.
  const dashboardFrame = document.querySelector('#dashboard-overlay iframe[data-src]');
  window.ArenaOpenDashboard = ({ tab = '', plan = '', pack = '' } = {}) => {
    if (dashboardFrame) {
      const extra = [];
      if (tab) extra.push(`tab=${encodeURIComponent(tab)}`);
      if (plan) extra.push(`plan=${encodeURIComponent(plan)}`);
      if (pack) extra.push(`pack=${encodeURIComponent(pack)}`);
      const loaded = Boolean(dashboardFrame.getAttribute('src'));
      if (!loaded) {
        if (extra.length) {
          const base = dashboardFrame.dataset.src || '';
          dashboardFrame.dataset.src = base + (base.includes('?') ? '&' : '?') + extra.join('&');
        }
      } else if (plan || pack) {
        const base = dashboardFrame.dataset.src || '/dashboard/?view=private';
        dashboardFrame.setAttribute('src', appPath(base) + (base.includes('?') ? '&' : '?') + extra.join('&'));
      } else if (tab && typeof dashboardFrame.contentWindow?.activateTab === 'function') {
        dashboardFrame.contentWindow.activateTab(tab);
      }
    }
    dashboardOverlay?.open();
  };
  window.ArenaOpenShop = () => shopOverlay?.open();
  window.ArenaCloseOverlay = (overlayId) => {
    if (overlayId === 'dashboard-overlay') dashboardOverlay?.close();
    else if (overlayId === 'shop-overlay') shopOverlay?.close();
  };

  const priorityEscapeEvents = new WeakSet();
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && activePriorityModal()) priorityEscapeEvents.add(event);
  }, { capture: true });
  document.addEventListener('keydown', (event) => {
    if (event.key !== 'Escape' || priorityEscapeEvents.has(event)) return;
    document.querySelectorAll('.mobile-overlay.open').forEach((overlay) => {
      overlay._mobileOverlayClose?.();
    });
  });
}

document.addEventListener('DOMContentLoaded', async () => {
  initServiceStatus();
  setupExploreBrand();
  setupChatAndDashboard();
  const el = (id) => document.getElementById(id);
  const ui = {
    conn: el('tb-conn'),
    round: el('tb-round'),
    mode: el('tb-mode'),
    shape: el('tb-shape'),
    alive: el('tb-alive'),
    lobbyCard: el('lobby-card'),
    lobbyTitle: el('lobby-title'),
    lobbyMeta: el('lobby-meta'),
    minimapBox: el('minimap-box'),
    fabZoomIn: el('fab-zoom-in'),
    fabZoomOut: el('fab-zoom-out'),
    fabAutoPan: el('fab-autopan'),
    fabMinimap: el('fab-minimap'),
    fabFollow: el('fab-follow-off'),
    fabFollowName: el('fab-follow-name'),
    sheet: el('sheet'),
    sheetGrip: el('sheet-grip'),
    sheetTabs: el('sheet-tabs'),
    panelPlayers: el('panel-players'),
    panelKills: el('panel-kills'),
    panelRanks: el('panel-ranks'),
  };

  // ---------- 3D engine ----------
  const canvas = el('arena-canvas');
  const engine = new ArenaEngine(canvas, {
    arenaWidth: ARENA_WIDTH, arenaHeight: ARENA_HEIGHT,
  });
  try {
    await engine.init();
    console.log('[Mobile] Arena engine initialized');
  } catch (err) {
    console.error('[Mobile] Engine init failed:', err);
  }
  const stopSafeViewport = observeArenaSafeViewport(
    canvas,
    (viewport) => engine.setSafeViewport(viewport),
    MOBILE_SAFE_VIEWPORT_REGIONS,
  );
  window.addEventListener('pagehide', stopSafeViewport, { once: true });

  // Camera state that must survive dynamic arena-size scene rebuilds
  // (ArenaEngine recreates its CameraController; zoom/follow are restored
  // by the engine itself, pinch tuning and auto-pan are ours to reapply).
  let autoPanOn = true;
  let followId = null;
  let followName = '';
  let lastCameraRef = null;

  function tuneCameraIfNew() {
    const controller = engine.camera;
    if (!controller || controller === lastCameraRef) return;
    lastCameraRef = controller;
    const cam = controller.camera;
    if (cam) {
      // ArcRotateCamera pointer input already does one-finger orbit and
      // two-finger pinch; percentage-based pinch keeps zoom speed sane
      // across the 80..1800 radius range without touching camera.js.
      cam.pinchDeltaPercentage = 0.01;
    }
    controller.setAutoPan(autoPanOn && !followId);
    if (followId) controller.followBot(followId);
  }
  tuneCameraIfNew();

  // ---------- Minimap (hidden until toggled) ----------
  const minimap = new Minimap(ui.minimapBox, ARENA_WIDTH, ARENA_HEIGHT);
  let minimapOn = false;

  // ---------- Follow handling ----------
  function setFollow(botId, name) {
    followId = botId || null;
    followName = name || '';
    engine.followBot(followId);
    if (followId) {
      engine.selectBot(followId);
      engine.setAutoPan(false);
    } else {
      engine.setAutoPan(autoPanOn);
    }
    ui.fabFollow.hidden = !followId;
    ui.fabFollowName.textContent = followName;
    rosterCache = ''; // force lobby-roster re-render paths
    // Live roster rows update in place: flip the highlight class now; the
    // per-row signatures (which include the follow flag) re-sync next tick.
    rosterRows.forEach((row, id) => {
      row.el.classList.toggle('following', followId === id);
    });
  }

  ui.fabFollow.addEventListener('click', () => setFollow(null));

  // ---------- Action cluster ----------
  function nudgeZoom(delta) {
    const controller = engine.camera;
    if (!controller || !controller.camera) return;
    // Derive current zoom from the live radius so pinch gestures (which
    // change radius directly) stay in sync with the buttons.
    const current = BASE_CAMERA_RADIUS / controller.camera.radius;
    controller.setZoom(current + delta);
  }
  ui.fabZoomIn.addEventListener('click', () => nudgeZoom(0.25));
  ui.fabZoomOut.addEventListener('click', () => nudgeZoom(-0.25));

  ui.fabAutoPan.classList.toggle('active', autoPanOn);
  ui.fabAutoPan.addEventListener('click', () => {
    autoPanOn = !autoPanOn;
    ui.fabAutoPan.classList.toggle('active', autoPanOn);
    if (autoPanOn && followId) setFollow(null);
    else engine.setAutoPan(autoPanOn);
  });

  ui.fabMinimap.addEventListener('click', () => {
    minimapOn = !minimapOn;
    ui.minimapBox.hidden = !minimapOn;
    ui.fabMinimap.classList.toggle('active', minimapOn);
    if (minimapOn && lastArenaState) minimap.update(lastArenaState);
  });

  // ---------- Bottom sheet ----------
  const sheet = createBottomSheet(ui.sheet, ui.sheetGrip, ui.sheetTabs);

  let activeTab = 'players';
  ui.sheetTabs.addEventListener('click', (event) => {
    const btn = event.target.closest('.sheet-tab');
    if (!btn || sheet.didDrag()) return;
    activeTab = btn.dataset.tab;
    ui.sheetTabs.querySelectorAll('.sheet-tab').forEach((t) => {
      t.classList.toggle('active', t === btn);
    });
    document.querySelectorAll('.sheet-panel').forEach((p) => {
      p.classList.toggle('active', p.id === `panel-${activeTab}`);
    });
    if (sheet.getState() === 'collapsed') sheet.snapTo('half');
    if (activeTab === 'ranks') loadRanks();
    if (activeTab === 'players') rosterCache = '';
    if (lastState) renderPanels(lastState);
  });

  // ---------- Players roster ----------
  let rosterCache = '';
  // Keyed in-place roster rows (bot_id -> {el, refs, sig}) so the 200ms
  // combat updates patch text/width instead of rebuilding the whole list.
  const rosterRows = new Map();
  let rosterOrder = '';
  let rosterLive = false; // true while #panel-players holds the keyed rows

  function resetRosterRows() {
    rosterRows.clear();
    rosterOrder = '';
    rosterLive = false;
  }

  function teamColor(team) {
    return team > 0 ? TEAM_COLORS[(team - 1) % TEAM_COLORS.length] : '';
  }

  // All user-controlled strings land via textContent/dataset/style props —
  // never interpolated into innerHTML.
  function buildRosterRow(botId) {
    const el = document.createElement('button');
    el.type = 'button';
    el.className = 'p-row';
    el.dataset.botId = botId;

    const dot = document.createElement('span');
    dot.className = 'p-dot';

    const main = document.createElement('span');
    main.className = 'p-main';
    const name = document.createElement('span');
    name.className = 'p-name';
    const sub = document.createElement('span');
    sub.className = 'p-sub';
    main.append(name, sub);

    const hp = document.createElement('span');
    hp.className = 'p-hp';
    const track = document.createElement('span');
    track.className = 'p-hp-track';
    const fill = document.createElement('span');
    fill.className = 'p-hp-fill';
    track.append(fill);
    const label = document.createElement('span');
    label.className = 'p-hp-label';
    hp.append(track, label);

    const kills = document.createElement('span');
    kills.className = 'p-kills';

    el.append(dot, main, hp, kills);
    return { el, refs: { dot, name, sub, fill, label, kills }, sig: '' };
  }

  function renderRoster(state) {
    const bots = state.bots || [];
    if (bots.length === 0) {
      const empty = '<p class="panel-empty">No bots in the arena.</p>';
      if (rosterCache !== empty) {
        ui.panelPlayers.innerHTML = empty;
        rosterCache = empty;
        resetRosterRows();
      }
      return;
    }

    // Take the panel over from lobby/empty/placeholder HTML.
    if (!rosterLive) {
      ui.panelPlayers.innerHTML = '';
      rosterCache = '';
      rosterRows.clear();
      rosterOrder = '';
      rosterLive = true;
    }

    const teamMode = state.game_mode === 'team_battle' || state.game_mode === 'ctf';
    const alive = bots.filter((b) => b.is_alive);
    const dead = bots.filter((b) => !b.is_alive);
    const sorted = [...alive, ...dead];

    // Drop rows for bots that left the arena.
    const ids = new Set(sorted.map((b) => b.bot_id));
    for (const [id, row] of rosterRows) {
      if (!ids.has(id)) {
        row.el.remove();
        rosterRows.delete(id);
      }
    }

    for (const b of sorted) {
      const maxHp = b.max_hp || 100;
      const pct = b.is_alive ? Math.max(0, Math.min(100, Math.round((b.hp / maxHp) * 100))) : 0;
      const hpLabel = b.is_alive ? `${Math.round(b.hp)} HP` : 'Out';
      const color = b.avatar_color || '#fff';
      const nameColor = teamMode && b.team > 0 ? teamColor(b.team) : '';
      const name = b.name == null ? '???' : String(b.name);
      const kills = `${Math.round(b.round_kills || 0)}K`;
      const following = followId === b.bot_id;

      let row = rosterRows.get(b.bot_id);
      if (!row) {
        row = buildRosterRow(b.bot_id);
        rosterRows.set(b.bot_id, row);
      }

      const sig = [
        name, color, b.weapon || '?', b.is_alive ? 1 : 0, pct, hpLabel,
        kills, nameColor, following ? 1 : 0,
      ].join('\u0001');
      if (sig === row.sig) continue;
      row.sig = sig;

      row.el.classList.toggle('dead', !b.is_alive);
      row.el.classList.toggle('following', following);
      row.el.dataset.name = name;
      row.refs.dot.style.background = color;
      row.refs.dot.style.color = color;
      row.refs.name.textContent = name;
      row.refs.name.style.color = nameColor;
      row.refs.sub.textContent = b.weapon || '?';
      row.refs.fill.style.width = `${pct}%`;
      row.refs.fill.classList.toggle('low', pct < 35);
      row.refs.label.textContent = hpLabel;
      row.refs.kills.textContent = kills;
    }

    // Reorder (appendChild moves attached nodes) only when the alive/dead
    // sort order changed; new bot ids always change the order key.
    const order = sorted.map((b) => b.bot_id).join('\u0001');
    if (order !== rosterOrder) {
      rosterOrder = order;
      for (const b of sorted) ui.panelPlayers.appendChild(rosterRows.get(b.bot_id).el);
    }
  }

  function renderLobbyRoster(state) {
    const players = state.players || [];
    const cacheKey = players.length === 0
      ? 'empty'
      : players.map((p) => `${p.name}|${p.weapon}|${p.avatar_color}`).join('\n');
    if (cacheKey !== rosterCache) {
      ui.panelPlayers.textContent = '';
      if (players.length === 0) {
        const empty = document.createElement('p');
        empty.className = 'panel-empty';
        empty.textContent = 'No bots connected yet.';
        ui.panelPlayers.appendChild(empty);
      } else {
        players.forEach((p) => {
          const row = document.createElement('div');
          row.className = 'p-row';
          row.style.cursor = 'default';
          
          const dot = document.createElement('span');
          dot.className = 'p-dot';
          const color = p.avatar_color || '#fff';
          dot.style.background = color;
          dot.style.color = color;
          row.appendChild(dot);
          
          const main = document.createElement('span');
          main.className = 'p-main';
          
          const name = document.createElement('span');
          name.className = 'p-name';
          name.textContent = p.name;
          main.appendChild(name);
          
          const sub = document.createElement('span');
          sub.className = 'p-sub';
          sub.textContent = p.weapon || '?';
          main.appendChild(sub);
          
          row.appendChild(main);
          
          const kills = document.createElement('span');
          kills.className = 'p-kills';
          kills.textContent = 'Ready';
          row.appendChild(kills);
          
          ui.panelPlayers.appendChild(row);
        });
      }
      rosterCache = cacheKey;
      resetRosterRows();
    }
  }

  ui.panelPlayers.addEventListener('click', (event) => {
    const row = event.target.closest('.p-row[data-bot-id]');
    if (!row) return;
    const id = row.dataset.botId;
    if (followId === id) setFollow(null);
    else setFollow(id, row.dataset.name);
  });

  // ---------- Kill feed ----------
  const seenKills = new Set();
  let killCount = 0;

  function renderKillFeed(kills) {
    if (!kills || kills.length === 0) return;
    const fresh = [];
    for (const kill of kills) {
      const key = `${kill.killer}-${kill.victim}-${kill.tick}`;
      if (seenKills.has(key)) continue;
      seenKills.add(key);
      fresh.push(kill);
    }
    if (fresh.length === 0) return;
    if (killCount === 0) ui.panelKills.innerHTML = '';
    for (let i = fresh.length - 1; i >= 0; i--) {
      const kill = fresh[i];
      const row = document.createElement('div');
      row.className = 'k-row';
      row.innerHTML = `
        <span class="k-killer">${esc(kill.killer)}</span>
        <span class="k-icon">${WEAPON_ICONS[kill.weapon] || '☠'}</span>
        <span class="k-victim">${esc(kill.victim)}</span>
        <span class="k-weapon">${esc((kill.weapon || 'unknown').replace('_', ' '))}</span>`;
      ui.panelKills.prepend(row);
      killCount++;
    }
    while (killCount > KILL_FEED_MAX && ui.panelKills.lastElementChild) {
      ui.panelKills.lastElementChild.remove();
      killCount--;
    }
  }

  function resetKillFeed() {
    if (killCount === 0 && seenKills.size === 0 && seenTauntIds.size === 0 && pendingTaunts.length === 0) return;
    seenKills.clear();
    seenTauntIds.clear();
    pendingTaunts = [];
    killCount = 0;
    ui.panelKills.innerHTML = '<p class="panel-empty">No kills yet this round.</p>';
  }

  // ---------- Bot taunts (shares the Kills panel, mirrors desktop hud.js) ----------
  const seenTauntIds = new Set();
  let pendingTaunts = [];

  // Called unthrottled from the WS callback: taunt events ride single
  // broadcasts, so sampling them on the 200ms UI timer would miss some.
  function queueTaunts(events) {
    for (const ev of events) {
      if (!ev || ev.type !== 'taunt' || !ev.id || !ev.text) continue;
      if (seenTauntIds.has(ev.id)) continue;
      seenTauntIds.add(ev.id);
      if (seenTauntIds.size > 256) {
        const first = seenTauntIds.values().next();
        if (!first.done) seenTauntIds.delete(first.value);
      }
      pendingTaunts.push(ev);
    }
    while (pendingTaunts.length > 30) pendingTaunts.shift();
  }

  function renderTaunts(state) {
    if (pendingTaunts.length === 0) return;
    const names = new Map();
    for (const bot of state.bots || []) {
      if (bot && bot.bot_id) names.set(bot.bot_id, bot.name);
    }
    const pending = pendingTaunts;
    pendingTaunts = [];
    // Reverse iteration renders a drained batch oldest-at-top, matching the
    // kill rows above.
    for (let i = pending.length - 1; i >= 0; i--) {
      const ev = pending[i];
      // Age gate: drop banter older than ~6s of ticks or from a previous
      // round (tick counter reset), so a backgrounded tab does not flood
      // the panel with stale lines on return.
      if (typeof state.tick === 'number' && typeof ev.tick === 'number' &&
          (ev.tick > state.tick || state.tick - ev.tick > 60)) {
        continue;
      }
      if (killCount === 0) ui.panelKills.innerHTML = '';
      const row = document.createElement('div');
      row.className = 'k-row k-taunt';
      row.innerHTML = `
        <span class="k-killer">${esc(names.get(ev.owner_id) || '???')}</span>
        <span class="k-icon">&#128172;</span>
        <span class="k-taunt-text">${esc(ev.text)}</span>`;
      ui.panelKills.prepend(row);
      killCount++;
    }
    while (killCount > KILL_FEED_MAX && ui.panelKills.lastElementChild) {
      ui.panelKills.lastElementChild.remove();
      killCount--;
    }
  }

  // ---------- Ranks (leaderboard) ----------
  let ranksFetchedAt = 0;
  let ranksLoading = false;

  async function loadRanks() {
    const now = Date.now();
    if (ranksLoading || now - ranksFetchedAt < RANKS_REFRESH_MS) return;
    ranksLoading = true;
    try {
      const resp = await fetch(`${apiPath('/leaderboard')}?sort=elo&limit=20`);
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
      const data = await resp.json();
      // All-time responses use "entries"; period responses use "leaderboard".
      const entries = data.entries || data.leaderboard || [];
      ranksFetchedAt = Date.now();
      if (entries.length === 0) {
        ui.panelRanks.innerHTML = '<p class="panel-empty">No ranked bots yet.</p>';
        return;
      }
      ui.panelRanks.innerHTML = entries.map((e, i) => {
        // Period responses can omit rank — fall back to list position
        // (same as the desktop leaderboard) instead of showing "#undefined".
        const rank = Number(e.rank) || i + 1;
        return `
        <div class="r-row${rank <= 3 ? ' top' : ''}">
          <span class="r-rank">#${rank}</span>
          <span class="p-dot" style="background:${esc(e.avatar_color || '#fff')};color:${esc(e.avatar_color || '#fff')}"></span>
          <span class="r-name">${esc(e.name)}</span>
          <span class="r-stat">${e.kills}/${e.deaths} K/D</span>
          <span class="r-elo">${e.elo}</span>
        </div>`;
      }).join('');
    } catch (err) {
      console.error('[Mobile] Leaderboard fetch failed:', err);
      ui.panelRanks.innerHTML = '<p class="panel-empty">Standings unavailable.</p>';
    } finally {
      ranksLoading = false;
    }
  }

  // ---------- Top bar ----------
  let roundNumber = 0;
  let lastRoundTick = -1;
  let statusFetchedAt = 0;

  async function fetchRoundNumber() {
    const now = Date.now();
    if (now - statusFetchedAt < 5000) return;
    statusFetchedAt = now;
    try {
      const resp = await fetch(apiPath('/arena/status'));
      if (!resp.ok) return;
      const data = await resp.json();
      if (Number.isFinite(data.round_number)) roundNumber = data.round_number;
    } catch { /* topbar falls back to LIVE */ }
  }

  function modeChip(state) {
    const scores = state.team_scores;
    let label = 'FFA';
    if (state.game_mode === 'team_battle') label = 'TEAM';
    else if (state.game_mode === 'ctf') label = 'CTF';
    if (!scores) return esc(label);
    const teams = Object.keys(scores).sort((a, b) => Number(a) - Number(b));
    if (teams.length < 2) return esc(label);
    let scoreText;
    if (teams.length === 2) {
      const [a, b] = teams;
      scoreText = `${TEAM_NAMES[Number(a) - 1] || `T${a}`} ${scores[a]}:${scores[b]} ${TEAM_NAMES[Number(b) - 1] || `T${b}`}`;
    } else {
      scoreText = teams
        .map((t) => `${(TEAM_NAMES[Number(t) - 1] || `T${t}`).charAt(0)} ${scores[t]}`)
        .join(' · ');
    }
    return `${esc(label)} <span class="ts">${esc(scoreText)}</span>`;
  }

  function updateTopbar(state) {
    if (state.type === 'lobby_state') {
      ui.round.textContent = state.countdown ? `Lobby ${state.countdown}s` : 'Lobby';
      ui.mode.innerHTML = 'FFA';
      ui.mode.hidden = true;
      ui.shape.textContent = '';
      ui.alive.textContent = `${state.bots_connected || 0}/${state.bots_needed || 2} bots`;
      return;
    }
    // New round detection: round_tick resets, refresh the round number.
    const roundTick = state.round_tick || 0;
    if (roundTick < lastRoundTick) fetchRoundNumber();
    lastRoundTick = roundTick;

    const secs = Math.floor(roundTick / 10); // 10 ticks/sec default
    const clock = `${String(Math.floor(secs / 60)).padStart(2, '0')}:${String(secs % 60).padStart(2, '0')}`;
    ui.round.textContent = roundNumber ? `R${roundNumber} ${clock}` : `LIVE ${clock}`;

    ui.mode.hidden = false;
    ui.mode.innerHTML = modeChip(state);
    ui.shape.textContent = state.map_shape && state.map_shape !== 'square'
      ? titleCase(state.map_shape) : '';

    const bots = state.bots || [];
    const alive = bots.filter((b) => b.is_alive).length;
    ui.alive.textContent = `${alive}/${bots.length} alive`;
  }

  // ---------- Lobby card ----------
  function updateLobbyCard(state) {
    const isLobby = state.type === 'lobby_state';
    ui.lobbyCard.hidden = !isLobby;
    if (!isLobby) return;
    const count = state.bots_connected || 0;
    const needed = state.bots_needed || 2;
    ui.lobbyTitle.textContent = state.countdown
      ? `Round starts in ${state.countdown}s`
      : 'Waiting for bots…';
    ui.lobbyMeta.textContent = `${count} connected · ${Math.max(0, needed - count)} more needed`;
  }

  // ---------- State plumbing ----------
  let lastState = null;
  let lastArenaState = null;
  let lastUiUpdate = 0;

  function renderPanels(state) {
    if (state.type === 'lobby_state') {
      renderLobbyRoster(state);
      return;
    }
    if (activeTab === 'players') renderRoster(state);
    if (activeTab === 'ranks') loadRanks(); // self rate-limited to 30s
    renderKillFeed(state.kill_feed || []);
    renderTaunts(state);
  }

  const wsUrl = wsURL('/spectator');
  const spectator = new SpectatorSocket(wsUrl,
    (state) => {
      engine.setState(state); // full tick rate for the 3D scene
      tuneCameraIfNew();
      if (state.type === 'arena_state' && Array.isArray(state.events)) {
        queueTaunts(state.events);
      }
      // round_end (intermission show) is consumed entirely by the shared
      // engine above — its banner overlay renders over this shell's canvas
      // too. Keep control messages out of the top bar / sheet DOM lanes.
      if (state.type !== 'arena_state' && state.type !== 'lobby_state') return;
      const now = performance.now();
      if (now - lastUiUpdate < UI_UPDATE_INTERVAL_MS) return;
      lastUiUpdate = now;
      lastState = state;
      if (state.type === 'lobby_state') {
        resetKillFeed();
        if (followId) setFollow(null);
      } else if (state.type === 'arena_state') {
        lastArenaState = state;
        if (minimapOn) minimap.update(state);
        // Clear follow if the bot left the arena entirely.
        if (followId && !(state.bots || []).some((b) => b.bot_id === followId)) {
          setFollow(null);
        }
      }
      updateTopbar(state);
      updateLobbyCard(state);
      renderPanels(state);
    },
    (status) => {
      const connected = status === 'connected';
      ui.conn.classList.toggle('connected', connected);
      ui.conn.title = status;
    },
    handleServiceStatus,
  );
  spectator.connect();

  fetchRoundNumber();
});

/**
 * Plain-touch bottom sheet with collapsed / half / full snap states.
 * Dragging is handled with touch events + CSS transforms only.
 */
function createBottomSheet(sheetEl, gripEl, tabsEl) {
  const STATES = ['collapsed', 'half', 'full'];
  let state = 'collapsed';
  let sheetH = 0;
  let dragging = false;
  let dragMoved = false;
  let startY = 0;
  let startVisible = 0;
  let lastY = 0;
  let lastT = 0;
  let velocity = 0;

  function measure() {
    sheetH = sheetEl.getBoundingClientRect().height;
  }

  function visibleFor(s) {
    const headerH = gripEl.offsetHeight + tabsEl.offsetHeight;
    if (s === 'collapsed') return headerH;
    if (s === 'half') return Math.min(sheetH, Math.round(window.innerHeight * 0.46));
    return sheetH;
  }

  function currentVisible() {
    const transform = getComputedStyle(sheetEl).transform;
    if (!transform || transform === 'none') return visibleFor(state);
    return sheetH - new DOMMatrixReadOnly(transform).m42;
  }

  function applyVisible(px, animate) {
    const clamped = Math.max(visibleFor('collapsed'), Math.min(sheetH, px));
    sheetEl.classList.toggle('snapping', !!animate);
    sheetEl.style.transform = `translateY(${Math.round(sheetH - clamped)}px)`;
  }

  function snapTo(s) {
    state = s;
    applyVisible(visibleFor(s), true);
    sheetEl.dataset.state = s;
  }

  function nearestState(visible, vel) {
    // Strong flick wins regardless of position.
    if (Math.abs(vel) > 0.9) {
      const idx = STATES.indexOf(state);
      const next = vel < 0 ? Math.min(idx + 1, 2) : Math.max(idx - 1, 0);
      return STATES[next];
    }
    let best = STATES[0];
    let bestDist = Infinity;
    for (const s of STATES) {
      const d = Math.abs(visibleFor(s) - visible);
      if (d < bestDist) { bestDist = d; best = s; }
    }
    return best;
  }

  function onTouchStart(event) {
    measure();
    dragging = true;
    dragMoved = false;
    startY = event.touches[0].clientY;
    lastY = startY;
    lastT = performance.now();
    velocity = 0;
    startVisible = currentVisible();
    sheetEl.classList.remove('snapping');
  }

  function onTouchMove(event) {
    if (!dragging) return;
    const y = event.touches[0].clientY;
    const dy = y - startY;
    if (Math.abs(dy) > 8) dragMoved = true;
    if (!dragMoved) return;
    event.preventDefault();
    const now = performance.now();
    const dt = now - lastT;
    if (dt > 0) velocity = (y - lastY) / dt; // px/ms, + = downward
    lastY = y;
    lastT = now;
    applyVisible(startVisible - dy, false);
  }

  function onTouchEnd() {
    if (!dragging) return;
    dragging = false;
    if (!dragMoved) return;
    snapTo(nearestState(currentVisible(), velocity));
  }

  for (const target of [gripEl, tabsEl]) {
    target.addEventListener('touchstart', onTouchStart, { passive: true });
    target.addEventListener('touchmove', onTouchMove, { passive: false });
    target.addEventListener('touchend', onTouchEnd);
    target.addEventListener('touchcancel', onTouchEnd);
  }

  // Tap or keyboard on the grip cycles states.
  const cycle = () => {
    if (dragMoved) return;
    snapTo(STATES[(STATES.indexOf(state) + 1) % STATES.length]);
  };
  gripEl.addEventListener('click', cycle);
  gripEl.addEventListener('keydown', (event) => {
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      cycle();
    }
  });

  window.addEventListener('resize', () => {
    measure();
    snapTo(state);
  });

  measure();
  snapTo('collapsed');

  return {
    snapTo,
    getState: () => state,
    didDrag: () => dragMoved,
  };
}
