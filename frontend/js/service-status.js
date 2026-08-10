'use strict';

import { apiPath } from './paths.js?v=20260710a';

let activeController = null;
let pendingStatus = null;

function findDefaultRoot() {
  return document.getElementById('siteBroadcast') ||
    document.getElementById('service-status-banner') ||
    document.querySelector('[data-service-status-root]');
}

function isExpired(notice) {
  if (!notice || !notice.expires_at) return false;
  const expires = Date.parse(notice.expires_at);
  return Number.isFinite(expires) && expires <= Date.now();
}

function visibleNotice(status) {
  if (status?.maintenance && !isExpired(status.maintenance)) {
    return { kind: 'maintenance', notice: status.maintenance };
  }
  if (status?.broadcast && !isExpired(status.broadcast)) {
    return { kind: 'broadcast', notice: status.broadcast };
  }
  return null;
}

function dismissalKey(notice) {
  return `arena-service-status-dismissed:${notice.id}`;
}

function wasDismissed(notice) {
  try { return sessionStorage.getItem(dismissalKey(notice)) === '1'; } catch { return false; }
}

function markDismissed(notice) {
  try { sessionStorage.setItem(dismissalKey(notice), '1'); } catch { /* storage can be disabled */ }
}

// ---------------------------------------------------------------------------
// Maintenance popup. Every maintenance notice (scheduled nightly restart,
// live restart, update) is raised once as a centered popup on top of the
// non-dismissible banner, so a restart warning cannot be missed. Fully
// self-contained (DOM + styles) so desktop and mobile share it with no
// per-page markup or stylesheet changes. Acknowledging the popup keeps the
// banner; each re-issued warning (new notice id) pops again.
// ---------------------------------------------------------------------------

const POPUP_STYLES = `
.service-status-popup {
  position: fixed; inset: 0; z-index: 12000;
  display: flex; align-items: center; justify-content: center;
  padding: 16px;
  background: rgba(3, 7, 12, 0.72);
}
.service-status-popup[hidden] { display: none; }
.service-status-popup-card {
  max-width: 430px; width: 100%;
  background: #0a1622; color: #e8f1fa;
  border: 1px solid rgba(255, 206, 84, 0.55);
  border-radius: 12px;
  box-shadow: 0 18px 60px rgba(0, 0, 0, 0.55);
  padding: 22px 24px;
  font-family: inherit;
}
.service-status-popup-card h2 {
  margin: 0 0 10px; font-size: 1rem;
  letter-spacing: 0.04em; text-transform: uppercase; color: #ffce54;
}
.service-status-popup-card p { margin: 0 0 18px; line-height: 1.5; font-size: 0.95rem; }
.service-status-popup-card button {
  appearance: none; cursor: pointer;
  border: 1px solid rgba(71, 215, 255, 0.6);
  background: rgba(71, 215, 255, 0.12); color: #bfefff;
  border-radius: 8px; padding: 8px 22px; font: inherit;
}
.service-status-popup-card button:hover { background: rgba(71, 215, 255, 0.22); }
`;

// In-memory fallback so a disabled sessionStorage cannot re-raise the same
// popup on every poll.
const ackedPopupIds = new Set();
let popupEl = null;
let popupNotice = null;

function popupAckKey(notice) {
  return `arena-service-status-popup-ack:${notice.id}`;
}

function popupWasAcked(notice) {
  if (ackedPopupIds.has(notice.id)) return true;
  try { return sessionStorage.getItem(popupAckKey(notice)) === '1'; } catch { return false; }
}

function acknowledgePopup() {
  if (popupNotice) {
    ackedPopupIds.add(popupNotice.id);
    try { sessionStorage.setItem(popupAckKey(popupNotice), '1'); } catch { /* storage can be disabled */ }
  }
  popupNotice = null;
  if (popupEl) popupEl.hidden = true;
}

function popupTitle(notice) {
  switch (notice.phase) {
    case 'scheduled': return 'Scheduled maintenance';
    case 'restarting': return 'Arena restarting';
    default: return 'Service notice';
  }
}

function ensurePopup() {
  if (popupEl) return popupEl;
  // Feature-detect the full DOM: headless test harnesses stub only the
  // banner surface, and the popup is an enhancement on top of it.
  if (typeof document === 'undefined' || typeof document.createElement !== 'function' || !document.body) {
    return null;
  }
  const style = document.createElement('style');
  style.textContent = POPUP_STYLES;
  document.head.appendChild(style);
  const overlay = document.createElement('div');
  overlay.className = 'service-status-popup';
  overlay.hidden = true;
  overlay.setAttribute('role', 'alertdialog');
  overlay.setAttribute('aria-modal', 'true');
  overlay.setAttribute('aria-live', 'assertive');
  const card = document.createElement('div');
  card.className = 'service-status-popup-card';
  const title = document.createElement('h2');
  title.dataset.popupTitle = '';
  const message = document.createElement('p');
  message.dataset.popupMessage = '';
  const ack = document.createElement('button');
  ack.type = 'button';
  ack.textContent = 'OK';
  ack.addEventListener('click', acknowledgePopup);
  card.append(title, message, ack);
  overlay.appendChild(card);
  document.body.appendChild(overlay);
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && popupEl && !popupEl.hidden) acknowledgePopup();
  });
  popupEl = overlay;
  return overlay;
}

function syncPopup(visible) {
  if (!visible || visible.kind !== 'maintenance' || popupWasAcked(visible.notice)) {
    popupNotice = null;
    if (popupEl) popupEl.hidden = true;
    return;
  }
  const overlay = ensurePopup();
  if (!overlay) {
    popupNotice = null;
    return;
  }
  const { notice } = visible;
  overlay.querySelector('[data-popup-title]').textContent = popupTitle(notice);
  overlay.querySelector('[data-popup-message]').textContent = notice.message || '';
  overlay.hidden = false;
  const focusAck = popupNotice?.id !== notice.id;
  popupNotice = notice;
  if (focusAck) overlay.querySelector('button')?.focus?.({ preventScroll: true });
}

/**
 * Mount the public service-status banner and begin authoritative REST polling.
 * WebSocket control messages can be applied immediately through handleStatus.
 */
export function initServiceStatus(options = {}) {
  const root = options.root || findDefaultRoot();
  const pollIntervalMs = Number(options.pollIntervalMs) > 0 ? Number(options.pollIntervalMs) : 15000;
  if (activeController && activeController.root === root) return activeController;
  if (activeController) activeController.destroy();

  const messageEl = root?.querySelector('[data-service-status-message], [data-service-status]') || null;
  const dismissButton = root?.querySelector('[data-service-status-dismiss]') || null;
  let current = null;
  let currentVisible = null;
  let timer = null;
  let destroyed = false;

  function render(status) {
    current = status || null;
    currentVisible = visibleNotice(current);
    syncPopup(currentVisible);
    if (!root || !messageEl) return;
    if (!currentVisible || (currentVisible.kind === 'broadcast' && wasDismissed(currentVisible.notice))) {
      root.hidden = true;
      root.removeAttribute('data-kind');
      root.removeAttribute('data-severity');
      messageEl.textContent = '';
      return;
    }

    const { kind, notice } = currentVisible;
    root.dataset.kind = kind;
    root.dataset.severity = notice.severity || 'info';
    messageEl.textContent = notice.message || '';
    root.hidden = false;
    if (dismissButton) dismissButton.hidden = kind === 'maintenance';
  }

  async function refresh() {
    if (destroyed) return null;
    try {
      const response = await fetch(apiPath('/service-status'), {
        headers: { Accept: 'application/json' },
        cache: 'no-store',
      });
      if (!response.ok) return null;
      const status = await response.json();
      handleStatus(status);
      return status;
    } catch {
      // Keep the last notice visible while the server is actually restarting.
      return null;
    }
  }

  function handleStatus(status) {
    if (!status || status.type !== 'service_status') return false;
    if (current && Number(status.revision) < Number(current.revision)) return false;
    render(status);
    return true;
  }

  function dismiss() {
    if (!currentVisible || currentVisible.kind !== 'broadcast') return;
    markDismissed(currentVisible.notice);
    render(current);
  }

  function destroy() {
    destroyed = true;
    if (timer) clearInterval(timer);
    timer = null;
    dismissButton?.removeEventListener('click', dismiss);
    if (activeController === controller) activeController = null;
  }

  const controller = {
    root,
    handleStatus,
    refresh,
    destroy,
    get current() { return current; },
  };
  activeController = controller;
  dismissButton?.addEventListener('click', dismiss);
  if (pendingStatus) {
    handleStatus(pendingStatus);
    pendingStatus = null;
  }
  refresh();
  timer = setInterval(refresh, pollIntervalMs);
  return controller;
}

// Singleton bridge used by the spectator stream. It safely buffers a status
// received before the site/mobile shell has mounted its banner.
export function handleServiceStatus(status) {
  if (activeController) return activeController.handleStatus(status);
  if (status?.type === 'service_status') {
    pendingStatus = status;
    return true;
  }
  return false;
}

function autoInit() {
  if (!activeController && findDefaultRoot()) initServiceStatus();
}

if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', autoInit, { once: true });
} else {
  autoInit();
}
