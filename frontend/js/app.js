'use strict';

import './babylon-runtime.js?v=20260718a';

/**
 * Main application - wires up all modules.
 * @module app
 */

import { ArenaEngine } from './renderer/engine.js?v=20260718m';
import { HudRenderer } from './renderer/hud.js?v=20260711b';
import { Minimap } from './renderer/minimap.js?v=20260718c';
import { SpectatorSocket } from './spectator-ws.js';
import { initLeaderboardWidget } from './leaderboard.js?v=20260710f';
import { initKeyGenerator } from './key-generator.js?v=20260714i';
import { isEnabled, onSettingsChange } from './settings.js';
import { initSettingsPanel } from './settings-panel.js';
import { apiPath, appPath, wsURL } from './paths.js?v=20260710a';
import { handleServiceStatus } from './service-status.js';
import { observeArenaSafeViewport } from './safe-viewport.js?v=20260718b';

const ARENA_WIDTH = 2000;
const ARENA_HEIGHT = 2000;

// These CSS keyframe animations run continuously with no JS trigger point
// (unlike combat-flash/round-sweep/rank-change/killfeed-in, which are only
// ever applied by JS right before they should play). A body class is the
// only way to gate them per-setting instead of all-or-nothing like the
// existing `prefers-reduced-motion` media query.
const CSS_ONLY_EFFECT_CLASSES = [
  ['siteMotion', 'liveHeartbeat', 'no-fx-liveHeartbeat'],
  ['siteMotion', 'auroraBackground', 'no-fx-auroraBackground'],
  ['siteMotion', 'heroChipFloat', 'no-fx-heroChipFloat'],
  ['siteMotion', 'orbitSpins', 'no-fx-orbitSpins'],
  ['killFlash', 'killFeedGlow', 'no-fx-killFeedGlow'],
];

function syncCssOnlyEffectClasses() {
  for (const [section, effect, cls] of CSS_ONLY_EFFECT_CLASSES) {
    document.body.classList.toggle(cls, !isEnabled(section, effect));
  }
}

/** Boot the application when DOM is ready. */
document.addEventListener('DOMContentLoaded', async () => {
  syncCssOnlyEffectClasses();
  onSettingsChange(syncCssOnlyEffectClasses);
  initSettingsPanel();
  mountOverlaySections();
  normalizeOnboardingScrollShells();
  setupRevealAnimations();
  setupOverlays();
  initAboutPanel();

  // Arena renderer
  const canvas = document.getElementById('arena-canvas');
  const arenaEngine = new ArenaEngine(canvas, {
    arenaWidth: ARENA_WIDTH, arenaHeight: ARENA_HEIGHT,
  });

  // HUD
  const hud = new HudRenderer(
    document.getElementById('hud-round'),
    document.getElementById('hud-killfeed'),
    document.getElementById('hud-players'),
    document.getElementById('hud-lobby'),
    document.getElementById('ws-status'),
  );

  // Arena info tabs
  setupArenaTabs();

  // Minimap overlay (bottom-right of the arena canvas). Rescales itself
  // when dynamic arena sizing changes the map dimensions between rounds.
  const minimap = new Minimap(canvas.parentElement, ARENA_WIDTH, ARENA_HEIGHT);

  // Initialize Babylon engine
  try {
    await arenaEngine.init();
    console.log('[App] Arena engine initialized');
  } catch (err) {
    console.error('[App] Engine init failed:', err);
  }
  const stopSafeViewport = observeArenaSafeViewport(
    canvas,
    (viewport) => arenaEngine.setSafeViewport(viewport),
  );
  window.addEventListener('pagehide', stopSafeViewport, { once: true });

  // Keep the browser-smoke contract explicit and inert for normal visitors.
  // The query gate prevents a permanent global debug surface while still
  // letting deterministic WebGL tests read serializable lifecycle counters.
  if (new URLSearchParams(window.location.search).get('arena-test') === '1') {
    Object.defineProperty(window, '__ARENA_TEST__', {
      configurable: true,
      value: Object.freeze({ diagnostics: () => arenaEngine.getLifecycleSnapshot() }),
    });
  }

  // Spectator WebSocket
  const wsUrl = wsURL('/spectator');
  // The 3D scene stays fully smooth off the raw ~10Hz tick, but the HUD/
  // leaderboard/dropdown DOM writes are only human-legible at a few Hz, so
  // they're throttled independently to cut redundant layout/reflow work.
  const UI_UPDATE_INTERVAL_MS = 200;
  let lastUiUpdate = 0;
  // Combat-reactive page pulse: flash the arena shell on each new kill and
  // sweep it on round start, so the whole broadcast frame reacts to the
  // action (readable across a conference hall). Pre-rasterized ::after
  // overlays animating opacity only, so steady-state cost is zero.
  const arenaShell = document.querySelector('.arena-shell');
  let lastKillSig = '';
  let lastPhase = '';
  const pulseTimers = {};
  const pulseShell = (cls) => {
    if (!arenaShell || arenaShell.classList.contains(cls)) return;
    arenaShell.classList.add(cls);
    // animationend bubbles, so a descendant's own animation (e.g. the kill
    // feed's killfeed-in entry animation) reaching this listener would strip
    // the pulse class early, cutting the flash short. A pseudo-element's
    // animationend targets the originating element itself, so filtering on
    // event.target === arenaShell accepts the shell's own ::after animation
    // and rejects everything bubbled up from inside it.
    const onAnimationEnd = (event) => {
      if (event.target !== arenaShell) return;
      arenaShell.removeEventListener('animationend', onAnimationEnd);
      clearTimeout(pulseTimers[cls]);
      arenaShell.classList.remove(cls);
    };
    arenaShell.addEventListener('animationend', onAnimationEnd);
    // If the animation never runs (prefers-reduced-motion sets animation:
    // none, or the shell is display:none), animationend never fires and the
    // latched class would suppress every future pulse. The timer is tracked
    // per class and cleared on re-arm so a stale fallback from a previous
    // pulse can never truncate the next one mid-animation.
    clearTimeout(pulseTimers[cls]);
    pulseTimers[cls] = setTimeout(() => arenaShell.classList.remove(cls), 1500);
  };
  const spectator = new SpectatorSocket(wsUrl,
    (state) => {
      arenaEngine.setState(state);
      // Taunt events ride single broadcasts; queue them unthrottled (cheap
      // array push, no DOM) so the 200ms HUD lane below cannot miss one.
      if (state.type === 'arena_state' && Array.isArray(state.events)) {
        hud.queueTaunts(state.events);
      }
      if (state.type === 'arena_state' && Array.isArray(state.kill_feed) && state.kill_feed.length > 0) {
        const k = state.kill_feed[state.kill_feed.length - 1];
        const sig = `${k.killer}|${k.victim}|${k.tick}`;
        // Rapid multi-kills within one tick coalesce into a single flash.
        if (lastKillSig && sig !== lastKillSig && isEnabled('killFlash', 'fullScreenFlash')) pulseShell('combat-flash');
        lastKillSig = sig;
      }
      if (state.type === 'arena_state' && lastPhase === 'lobby_state' && isEnabled('siteMotion', 'roundSweep')) pulseShell('round-sweep');
      if (state.type === 'arena_state' || state.type === 'lobby_state') lastPhase = state.type;
      // Chrome parks rAF for hidden tabs but keeps delivering WS messages;
      // skip the DOM/canvas half while hidden (the engine still consumes
      // state above for continuity) and snap fresh on return. Control
      // messages like round_end (intermission show, handled entirely inside
      // the engine) carry no HUD payload — keep them out of the DOM lane.
      if (state.type !== 'arena_state' && state.type !== 'lobby_state') return;
      const now = performance.now();
      if (!document.hidden && now - lastUiUpdate >= UI_UPDATE_INTERVAL_MS) {
        lastUiUpdate = now;
        hud.updateState(state);
        if (state.type === 'arena_state') minimap.update(state);
        updateHeroStatus(state);
        updateFollowDropdown(state);
      }
    },
    (status) => hud.setStatus(status),
    handleServiceStatus,
  );
  spectator.connect();

  // Controls
  setupControls(arenaEngine);

  // Leaderboard
  initLeaderboardWidgets();

  const keygenEl = document.getElementById('keygen-card');
  if (keygenEl) {
    initKeyGenerator(keygenEl);
  }

  hud.onSelectBot = (botID) => {
    arenaEngine.selectBot(botID);
  };
  arenaEngine.onSelectBot = (botID) => {
    hud.setSelectedBot(botID);
  };

  // Smooth scroll for CTA. A bare href="#" (used by overlay-trigger links
  // that need a real <a> for keyboard/middle-click semantics but have no
  // scroll target of their own) is not a scroll link -- querySelector('#')
  // throws, so skip it rather than matching every such link here too.
  document.querySelectorAll('a[href^="#"]').forEach((a) => {
    const href = a.getAttribute('href');
    if (href === '#') return;
    a.addEventListener('click', (e) => {
      e.preventDefault();
      const target = document.querySelector(href);
      if (target) target.scrollIntoView({ behavior: 'smooth' });
    });
  });
});

/** @private Setup arena controls. */
function setupControls(engine) {
  const zoomSlider = document.getElementById('zoom-slider');
  const zoomLabel = document.getElementById('zoom-value');
  if (zoomSlider) {
    zoomSlider.addEventListener('input', (e) => {
      engine.setZoom(parseFloat(e.target.value));
    });
    // Sync slider when mouse wheel changes zoom
    if (engine.camera) {
      engine.camera.onZoomChange = (z) => {
        zoomSlider.value = z;
        if (zoomLabel) zoomLabel.textContent = `${z.toFixed(1)}x`;
      };
    }
  }

  const followSelect = document.getElementById('follow-bot');
  if (followSelect) {
    followSelect.addEventListener('change', (e) => {
      engine.followBot(e.target.value || null);
    });
  }

  const autoPanBtn = document.getElementById('auto-pan');
  if (autoPanBtn) {
    autoPanBtn.addEventListener('click', () => {
      const active = autoPanBtn.classList.toggle('active');
      engine.setAutoPan(active);
    });
  }

  // #fullscreen-btn is owned by site-shell.js cinema mode, which captures the
  // click with stopImmediatePropagation. The legacy animated expand/collapse
  // implementation that used to live here was permanently shadowed dead code
  // (and carried a transitionend latch-up bug), so it has been removed.
}

/** @private Populate follow dropdown with current bots. */
let lastBotList = '';
function updateFollowDropdown(state) {
  const select = document.getElementById('follow-bot');
  if (!select || !state.bots) return;
  const names = state.bots.filter((b) => b.is_alive).map((b) => b.name).sort().join(',');
  if (names === lastBotList) return;
  lastBotList = names;
  const current = select.value;
  select.innerHTML = '<option value="">None</option>';
  state.bots.filter((b) => b.is_alive).sort((a, b) => a.name.localeCompare(b.name)).forEach((bot) => {
    const opt = document.createElement('option');
    opt.value = bot.bot_id;
    opt.textContent = bot.name;
    select.appendChild(opt);
  });
  select.value = current;
  if (current && select.value !== current) {
    // The followed bot died or left: its option is gone, so the restore
    // failed and the select shows blank while the camera keeps chasing the
    // corpse. Fall back to free camera through the normal change handler.
    select.value = '';
    select.dispatchEvent(new Event('change'));
  }
}

/** @private Wire up the arena info tab bar. */
function setupArenaTabs() {
  const tabs = document.querySelectorAll('.arena-tab');
  const panels = document.querySelectorAll('.arena-tab-panel');
  tabs.forEach((tab) => {
    tab.addEventListener('click', () => {
      tabs.forEach((t) => t.classList.remove('active'));
      panels.forEach((p) => p.classList.remove('active'));
      tab.classList.add('active');
      const panel = document.getElementById(`tab-${tab.dataset.tab}`);
      if (panel) panel.classList.add('active');
    });
  });
}

function updateHeroStatus(state) {
  if (!state) return;
  if (state.type === 'lobby_state') {
    syncHeroSummary({
      phase: state.countdown ? 'lobby countdown' : 'lobby',
      botsConnected: state.bots_connected || 0,
      botsAlive: 0,
    });
    return;
  }
  if (!state.bots) return;
  syncHeroSummary({
    phase: 'active round',
    botsConnected: state.bots.length,
    botsAlive: state.bots.filter((bot) => bot.is_alive).length,
  });
}

function syncHeroSummary({ phase, botsConnected, botsAlive }) {
  const phaseEl = document.getElementById('hero-phase');
  const botsEl = document.getElementById('hero-bots');
  const aliveEl = document.getElementById('hero-alive');
  if (phaseEl && phase) phaseEl.textContent = titleCase(phase);
  if (botsEl && Number.isFinite(botsConnected)) botsEl.textContent = `${botsConnected}`;
  if (aliveEl && Number.isFinite(botsAlive)) aliveEl.textContent = `${botsAlive}`;
}

function setupRevealAnimations() {
  const nodes = Array.from(document.querySelectorAll('.reveal-on-scroll'));
  if (nodes.length === 0) return;
  if (!('IntersectionObserver' in window) || !isEnabled('siteMotion', 'revealOnScroll')) {
    nodes.forEach((node) => node.classList.add('is-visible'));
    return;
  }

  const observer = new IntersectionObserver((entries) => {
    entries.forEach((entry) => {
      if (!entry.isIntersecting) return;
      entry.target.classList.add('is-visible');
      observer.unobserve(entry.target);
    });
  }, { threshold: 0.18, rootMargin: '0px 0px -60px 0px' });

  nodes.forEach((node) => observer.observe(node));
}

function titleCase(value) {
  return String(value)
    .split(/[\s_-]+/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(' ');
}

function mountOverlaySections() {
  const apiReference = document.getElementById('api-reference');
  const apiSlot = document.getElementById('onboarding-api-slot');
  if (apiReference && apiSlot) {
    apiSlot.appendChild(apiReference);
    apiReference.classList.add('overlay-section-host');
  }

  document.body.classList.add('overlay-content-mounted');
}

function normalizeOnboardingScrollShells() {
  const root = document.getElementById('onboarding-overlay');
  if (!root) return;

  root.querySelectorAll('pre.code-block').forEach((block) => {
    if (block.closest('.drawer-code-scroll-shell')) return;
    const shell = document.createElement('div');
    shell.className = 'drawer-code-scroll-shell';
    const inner = document.createElement('div');
    inner.className = 'drawer-code-scroll-inner';
    block.parentNode.insertBefore(shell, block);
    shell.appendChild(inner);
    inner.appendChild(block);
    bindNestedWheel(shell, inner);
  });

  root.querySelectorAll('.api-table').forEach((table) => {
    if (table.closest('.drawer-table-scroll-shell')) return;
    const shell = document.createElement('div');
    shell.className = 'drawer-table-scroll-shell';
    const inner = document.createElement('div');
    inner.className = 'drawer-table-scroll-inner';
    table.parentNode.insertBefore(shell, table);
    shell.appendChild(inner);
    inner.appendChild(table);
    bindNestedWheel(shell, inner);
  });
}

function bindNestedWheel(shell, inner) {
  const clamp = (value, min, max) => Math.min(max, Math.max(min, value));

  shell.addEventListener('wheel', (event) => {
    const maxTop = Math.max(0, inner.scrollHeight - inner.clientHeight);
    const maxLeft = Math.max(0, inner.scrollWidth - inner.clientWidth);
    const canScrollY = maxTop > 0;
    const canScrollX = maxLeft > 0;
    let consumed = false;

    if (canScrollY && Math.abs(event.deltaY) >= Math.abs(event.deltaX)) {
      const nextTop = clamp(inner.scrollTop + event.deltaY, 0, maxTop);
      if (nextTop !== inner.scrollTop) {
        inner.scrollTop = nextTop;
        consumed = true;
      }
    } else if (canScrollX) {
      const delta = Math.abs(event.deltaX) > Math.abs(event.deltaY) ? event.deltaX : event.deltaY;
      const nextLeft = clamp(inner.scrollLeft + delta, 0, maxLeft);
      if (nextLeft !== inner.scrollLeft) {
        inner.scrollLeft = nextLeft;
        consumed = true;
      }
    }

    if (consumed) {
      event.preventDefault();
      event.stopPropagation();
    }
  }, { passive: false });
}

function initLeaderboardWidgets() {
  const widgets = [
    {
      root: document.getElementById('standings-boards'),
      modeTabsContainer: document.getElementById('standings-mode-tabs'),
      sortTabsContainer: document.getElementById('standings-tabs'),
      podiumEl: document.getElementById('standings-podium'),
      leaderboardBody: document.getElementById('standings-body'),
      bountyBody: document.getElementById('standings-bounty-body'),
      weaponPodiumEl: document.getElementById('standings-weapon-podium'),
      weaponBody: document.getElementById('standings-weapon-body'),
      weaponUpdatedEl: document.getElementById('standings-weapon-updated'),
      limit: 20,
    },
  ];

  widgets.forEach((widget) => {
    if (!widget.root || !widget.modeTabsContainer || !widget.sortTabsContainer || !widget.podiumEl || !widget.leaderboardBody || !widget.bountyBody || !widget.weaponPodiumEl || !widget.weaponBody || !widget.weaponUpdatedEl) {
      return;
    }

    initLeaderboardWidget(widget);
  });
}

/**
 * Populate the About drawer with the live server build identity from
 * GET /api/v1/version (commit hash linked to GitHub, build time, Go version).
 */
function initAboutPanel() {
  const commitEl = document.getElementById('about-commit');
  if (!commitEl) return;
  fetch(apiPath('/version'))
    .then((res) => (res.ok ? res.json() : Promise.reject(new Error(`HTTP ${res.status}`))))
    .then((v) => {
      commitEl.textContent = v.commit_short || 'unknown';
      const link = document.getElementById('about-commit-link');
      if (link && v.commit && v.commit !== 'unknown') {
        link.href = `${v.repo || 'https://github.com/ablac/Arena'}/commit/${v.commit}`;
        link.title = v.commit;
      }
      const buildEl = document.getElementById('about-build-time');
      if (buildEl && v.build_time && v.build_time !== 'unknown') {
        const parsed = new Date(v.build_time);
        buildEl.textContent = Number.isNaN(parsed.getTime()) ? v.build_time : parsed.toLocaleString();
      }
      const goEl = document.getElementById('about-go-version');
      if (goEl && v.go_version) goEl.textContent = v.go_version;
    })
    .catch((err) => {
      console.warn('[About] version fetch failed:', err);
      commitEl.textContent = 'unavailable';
    });
}

function setupOverlays() {
  const backdrop = document.getElementById('overlay-backdrop');
  const openButtons = Array.from(document.querySelectorAll('[data-overlay-open]'));
  const closeButtons = Array.from(document.querySelectorAll('[data-close-overlay]'));
  const overlays = Array.from(document.querySelectorAll('.onboarding-overlay'));
  const openStack = [];

  const syncOverlayState = () => {
    const openEntries = openStack
      .map((id) => document.getElementById(id))
      .filter((overlay) => overlay && overlay.classList.contains('open'));

    const leftOpen = [];
    const rightOpen = [];

    openEntries.forEach((overlay, index) => {
      overlay.classList.remove('overlay-primary', 'overlay-secondary');
      overlay.style.zIndex = `${80 + index}`;
      const side = overlay.dataset.overlaySide === 'left' ? leftOpen : rightOpen;
      side.push(overlay);
    });

    [leftOpen, rightOpen].forEach((group) => {
      group.forEach((overlay, index) => {
        overlay.classList.add(index === 0 ? 'overlay-primary' : 'overlay-secondary');
      });
    });

    const hasOpenOverlays = openEntries.length > 0;
    document.body.classList.toggle('onboarding-open', hasOpenOverlays);
    backdrop?.classList.toggle('active', hasOpenOverlays);
  };

  const removeFromStack = (overlayId) => {
    const idx = openStack.indexOf(overlayId);
    if (idx >= 0) openStack.splice(idx, 1);
  };

  const openOverlay = (overlayId, targetSelector) => {
    const overlay = document.getElementById(overlayId);
    const drawer = overlay?.querySelector('.onboarding-drawer');
    if (!overlay || !drawer) return;
    const scrollRoot = drawer.querySelector('.onboarding-flow-shell-scroll') ||
      drawer.querySelector('.onboarding-drawer-scroll') ||
      drawer;

    if (overlay.classList.contains('open')) {
      removeFromStack(overlayId);
      openStack.push(overlayId);
      syncOverlayState();
    } else {
      overlay.classList.add('open');
      overlay.setAttribute('aria-hidden', 'false');
      openStack.push(overlayId);
      syncOverlayState();
    }

    const lazyFrame = overlay.querySelector('iframe[data-src]');
    if (lazyFrame && !lazyFrame.getAttribute('src')) {
      lazyFrame.setAttribute('src', appPath(lazyFrame.dataset.src));
    }

    if (!targetSelector) {
      scrollRoot.scrollTo({ top: 0, behavior: 'smooth' });
    }

    if (targetSelector) {
      const target = document.querySelector(targetSelector);
      if (target) {
        requestAnimationFrame(() => {
          const targetTop = target.getBoundingClientRect().top - scrollRoot.getBoundingClientRect().top + scrollRoot.scrollTop - 18;
          scrollRoot.scrollTo({ top: Math.max(0, targetTop), behavior: 'smooth' });
        });
      }
    }
  };

  const closeOverlay = (overlayId) => {
    const overlay = document.getElementById(overlayId);
    if (!overlay) return;
    overlay.classList.remove('open', 'overlay-primary', 'overlay-secondary');
    overlay.setAttribute('aria-hidden', 'true');
    removeFromStack(overlayId);
    syncOverlayState();
  };

  // Opens the Dashboard drawer already on the right tab, without a page or
  // iframe reload when it's avoidable. Used both by same-page buttons (see
  // data-dashboard-tab below) and by window.ArenaOpenDashboard, which other
  // same-origin documents that can't reach this page's own overlay JS call
  // directly -- the Shop iframe uses it instead of navigating with
  // ?dash_open=1, which used to reload the whole Arena just to switch tabs.
  const openDashboardOverlay = ({ tab = '', plan = '', pack = '' } = {}) => {
    const frame = document.getElementById('dashboard-frame');
    if (frame) {
      const extra = [];
      if (tab) extra.push(`tab=${encodeURIComponent(tab)}`);
      if (plan) extra.push(`plan=${encodeURIComponent(plan)}`);
      if (pack) extra.push(`pack=${encodeURIComponent(pack)}`);
      const loaded = Boolean(frame.getAttribute('src'));
      if (!loaded) {
        if (extra.length) {
          const base = frame.dataset.src || '';
          frame.dataset.src = base + (base.includes('?') ? '&' : '?') + extra.join('&');
        }
      } else if (plan || pack) {
        // Switching subscription plan or resuming a specific pack purchase
        // needs the dashboard's own bootstrap to re-run (subscription offer
        // resolution / pending-pack catalog lookup) -- reload just this
        // small iframe, never the whole Arena page.
        const base = frame.dataset.src || '/dashboard/?view=private';
        frame.setAttribute('src', appPath(base) + (base.includes('?') ? '&' : '?') + extra.join('&'));
      } else if (tab && typeof frame.contentWindow?.activateTab === 'function') {
        frame.contentWindow.activateTab(tab);
      }
    }
    openOverlay('dashboard-overlay');
  };

  openButtons.forEach((button) => {
    button.addEventListener('click', (event) => {
      event.preventDefault();
      if (button.dataset.overlayOpen === 'dashboard-overlay' && button.dataset.dashboardTab) {
        openDashboardOverlay({ tab: button.dataset.dashboardTab });
        return;
      }
      if (!button.dataset.overlayTarget) {
        const overlay = document.getElementById(button.dataset.overlayOpen);
        if (overlay?.classList.contains('open')) {
          closeOverlay(button.dataset.overlayOpen);
          return;
        }
      }
      openOverlay(button.dataset.overlayOpen, button.dataset.overlayTarget);
    });
  });

  closeButtons.forEach((button) => {
    button.addEventListener('click', () => closeOverlay(button.dataset.closeOverlay));
  });

  overlays.forEach((overlay) => {
    overlay.addEventListener('click', (event) => {
      if (event.target === overlay || event.target.classList.contains('onboarding-scrim')) {
        closeOverlay(overlay.id);
      }
    });
  });

  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') {
      const topOverlayId = openStack[openStack.length - 1];
      if (topOverlayId) closeOverlay(topOverlayId);
    }
  });

  // Same-origin documents this page doesn't control directly (the Shop
  // iframe) call these instead of navigating with ?dash_open=1 or a plain
  // top-level link, either of which used to reload the whole Arena just to
  // switch drawers or go back to the already-running live view underneath.
  window.ArenaOpenDashboard = openDashboardOverlay;
  window.ArenaOpenShop = () => openOverlay('shop-overlay');
  window.ArenaCloseOverlay = closeOverlay;

  applyDeepLinkedDashboardOpen(openDashboardOverlay);
  applyEmailTokenHandoff(openOverlay);
  applyDeepLinkedShopOpen(openOverlay);
}

// Pages that can't reach this page's overlay JS directly (a freshly
// generated key's "claim this bot" link, or the Shop when it's reached as
// its own standalone tab rather than embedded) hand off by navigating here
// with ?dash_open=1 (plus optional dash_tab/dash_plan/dash_pack). This is
// what makes "Dashboard" open in the slide-out drawer everywhere instead of
// sometimes landing on /dashboard/ as a bare full-page navigation.
function applyDeepLinkedDashboardOpen(openDashboardOverlay) {
  const params = new URLSearchParams(window.location.search);
  if (params.get('dash_open') !== '1') return;

  openDashboardOverlay({
    tab: params.get('dash_tab') || '',
    plan: params.get('dash_plan') || '',
    pack: params.get('dash_pack') || '',
  });

  params.delete('dash_open');
  params.delete('dash_tab');
  params.delete('dash_plan');
  params.delete('dash_pack');
  const query = params.toString();
  const cleanURL = window.location.pathname + (query ? `?${query}` : '') + window.location.hash;
  window.history.replaceState(null, '', cleanURL);
}

// The Shop (frontend/shop/) hands off here the same way the Dashboard's own
// deep links do: navigating to ?shop_open=1 rather than reaching into this
// page's overlay JS directly. Used by the Dashboard's "Browse the Shop" link
// so leaving the Cosmetics tab to look for something else reopens the same
// slide-out drawer instead of a bare /shop/ page load.
function applyDeepLinkedShopOpen(openOverlay) {
  const params = new URLSearchParams(window.location.search);
  if (params.get('shop_open') !== '1') return;

  openOverlay('shop-overlay');

  params.delete('shop_open');
  const query = params.toString();
  const cleanURL = window.location.pathname + (query ? `?${query}` : '') + window.location.hash;
  window.history.replaceState(null, '', cleanURL);
}

// A magic-link sign-in email always opens a fresh top-level tab. Landing
// there takes visitors to /dashboard/ first, which immediately hands the
// token back here via a same-origin redirect (see the inline script at the
// top of dashboard/index.html) rather than showing its standalone
// confirm-sign-in screen. This is the other half of that handoff: forward
// the token into the embedded Dashboard drawer's iframe (as a hash fragment,
// same as the original email link, so it never touches a server access log)
// and open the drawer, so sign-in completes on the live arena instead of a
// bare page.
function applyEmailTokenHandoff(openOverlay) {
  const hash = new URLSearchParams(window.location.hash.replace(/^#/, ''));
  const token = hash.get('email_token');
  if (!token) return;

  const frame = document.getElementById('dashboard-frame');
  if (frame) {
    const base = frame.dataset.src || '';
    frame.dataset.src = base + '#email_token=' + encodeURIComponent(token);
  }
  openOverlay('dashboard-overlay');

  hash.delete('email_token');
  const remaining = hash.toString();
  const cleanURL = window.location.pathname + window.location.search + (remaining ? `#${remaining}` : '');
  window.history.replaceState(null, '', cleanURL);
}
