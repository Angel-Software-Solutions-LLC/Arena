'use strict';

/**
 * Self-contained public profile popup, opened when a chat handle is
 * clicked. Deliberately independent of the onboarding-overlay drawer system
 * in app.js (same pattern as consent-gate.js): a native <dialog> with its
 * own injected styles, so it works from any page that loads this module
 * without also needing the main site's overlay markup/CSS.
 * @module profile-popup
 */

import { apiPath } from './paths.js?v=20260710a';

const DIALOG_ID = 'arena-profile-popup-dialog';

function injectStyle() {
  if (document.getElementById('arena-profile-popup-style')) return;
  const style = document.createElement('style');
  style.id = 'arena-profile-popup-style';
  style.textContent = `
    #${DIALOG_ID} {
      max-width: 420px;
      width: calc(100vw - 40px);
      padding: 0;
      border: 1px solid rgba(126, 166, 207, 0.28);
      border-radius: 12px;
      background: #0c1825;
      color: #ecf4ff;
      font-family: 'Space Grotesk', system-ui, sans-serif;
      box-shadow: 0 24px 60px rgba(0, 0, 0, 0.55);
    }
    #${DIALOG_ID}::backdrop { background: rgba(4, 9, 16, 0.72); }
    #${DIALOG_ID} .prf-body { padding: 20px 22px 22px; }
    #${DIALOG_ID} .prf-close {
      float: right;
      background: transparent;
      border: none;
      color: #91a6bd;
      font-size: 1.3rem;
      line-height: 1;
      cursor: pointer;
      padding: 2px 4px;
    }
    #${DIALOG_ID} .prf-close:hover { color: #ecf4ff; }
    #${DIALOG_ID} .prf-head {
      display: flex;
      align-items: center;
      gap: 12px;
      margin-bottom: 6px;
    }
    #${DIALOG_ID} .prf-avatar {
      width: 40px;
      height: 40px;
      border-radius: 50%;
      flex: none;
      border: 1px solid rgba(126, 166, 207, 0.28);
    }
    #${DIALOG_ID} .prf-name {
      font-size: 1.05rem;
      font-weight: 700;
      overflow-wrap: anywhere;
    }
    #${DIALOG_ID} .prf-handle {
      font-family: 'IBM Plex Mono', monospace;
      font-size: 0.72rem;
      color: #47d7ff;
    }
    #${DIALOG_ID} .prf-joined {
      font-family: 'IBM Plex Mono', monospace;
      font-size: 0.68rem;
      color: #91a6bd;
      margin: 4px 0 14px;
    }
    #${DIALOG_ID} .prf-bio {
      font-size: 0.85rem;
      line-height: 1.45;
      color: #b9c8d8;
      margin: 0 0 16px;
      white-space: pre-wrap;
      overflow-wrap: anywhere;
    }
    #${DIALOG_ID} .prf-bots-title {
      font-family: 'IBM Plex Mono', monospace;
      font-size: 0.7rem;
      letter-spacing: 0.05em;
      text-transform: uppercase;
      color: #91a6bd;
      margin: 0 0 8px;
    }
    #${DIALOG_ID} .prf-bots { display: flex; flex-direction: column; gap: 6px; }
    #${DIALOG_ID} .prf-bot-row {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 10px;
      padding: 8px 10px;
      background: rgba(10, 18, 30, 0.6);
      border: 1px solid rgba(126, 166, 207, 0.16);
      border-radius: 8px;
      font-size: 0.78rem;
    }
    #${DIALOG_ID} .prf-bot-name { font-weight: 600; overflow-wrap: anywhere; }
    #${DIALOG_ID} .prf-bot-stats {
      font-family: 'IBM Plex Mono', monospace;
      font-size: 0.68rem;
      color: #91a6bd;
      white-space: nowrap;
    }
    #${DIALOG_ID} .prf-empty, #${DIALOG_ID} .prf-loading, #${DIALOG_ID} .prf-error {
      font-size: 0.8rem;
      color: #91a6bd;
      padding: 10px 0;
    }
  `;
  document.head.appendChild(style);
}

function ensureDialog() {
  let dialog = document.getElementById(DIALOG_ID);
  if (dialog) return dialog;
  injectStyle();
  dialog = document.createElement('dialog');
  dialog.id = DIALOG_ID;
  dialog.innerHTML = `
    <div class="prf-body">
      <button type="button" class="prf-close" aria-label="Close profile">&times;</button>
      <div class="prf-content"></div>
    </div>
  `;
  document.body.appendChild(dialog);
  dialog.querySelector('.prf-close').addEventListener('click', () => dialog.close());
  dialog.addEventListener('click', (event) => {
    if (event.target === dialog) dialog.close();
  });
  return dialog;
}

function escapeHTML(value) {
  const el = document.createElement('div');
  el.textContent = String(value ?? '');
  return el.innerHTML;
}

function avatarStyle(color) {
  const bg = color && /^#[0-9a-fA-F]{3,8}$/.test(color) ? color : '#47d7ff';
  return `background:${bg}`;
}

function renderBotRow(bot) {
  return `
    <div class="prf-bot-row">
      <span class="prf-bot-name">${escapeHTML(bot.name || 'Unnamed bot')}</span>
      <span class="prf-bot-stats">${escapeHTML(bot.default_weapon || '')} &middot; ELO ${escapeHTML(bot.elo ?? 0)} &middot; ${escapeHTML(bot.kills ?? 0)}K/${escapeHTML(bot.deaths ?? 0)}D</span>
    </div>`;
}

function renderProfile(container, profile) {
  const joined = profile.joined_at ? new Date(profile.joined_at) : null;
  const joinedText = joined && !Number.isNaN(joined.getTime())
    ? `Joined ${joined.toLocaleDateString([], { year: 'numeric', month: 'short' })}`
    : '';
  const bots = Array.isArray(profile.bots) ? profile.bots : [];
  let botsHTML;
  if (!profile.shows_bots) {
    botsHTML = '<p class="prf-empty">This player keeps their bots private.</p>';
  } else if (bots.length === 0) {
    botsHTML = '<p class="prf-empty">No bots linked yet.</p>';
  } else {
    botsHTML = `<div class="prf-bots">${bots.map(renderBotRow).join('')}</div>`;
  }

  container.innerHTML = `
    <div class="prf-head">
      <div class="prf-avatar" style="${avatarStyle(profile.avatar_color)}"></div>
      <div>
        <div class="prf-name">${escapeHTML(profile.display_name || 'Arena developer')}</div>
        <div class="prf-handle">${escapeHTML(profile.chat_handle || '')}</div>
      </div>
    </div>
    <p class="prf-joined">${escapeHTML(joinedText)}</p>
    ${profile.bio ? `<p class="prf-bio">${escapeHTML(profile.bio)}</p>` : ''}
    <p class="prf-bots-title">Bots</p>
    ${botsHTML}
  `;
}

// Which open is current. Two handles clicked in quick succession are two
// fetches in flight, and the first can answer last; only the latest may draw.
let profileOpenSequence = 0;

/** Fetch and open the profile popup for accountId. No-op for a falsy id (anonymous/dev messages). */
export async function openProfilePopup(accountId) {
  if (!accountId) return;
  const sequence = ++profileOpenSequence;
  const dialog = ensureDialog();
  const content = dialog.querySelector('.prf-content');
  content.innerHTML = '<p class="prf-loading">Loading profile...</p>';
  if (typeof dialog.showModal === 'function' && !dialog.open) {
    dialog.showModal();
  }

  try {
    const resp = await fetch(apiPath(`/profile/${encodeURIComponent(accountId)}`), {
      cache: 'no-store',
      headers: { Accept: 'application/json' },
    });
    if (sequence !== profileOpenSequence) return;
    if (!resp.ok) {
      content.innerHTML = '<p class="prf-error">Profile not found.</p>';
      return;
    }
    const profile = await resp.json();
    if (sequence !== profileOpenSequence) return;
    renderProfile(content, profile);
  } catch (err) {
    if (sequence !== profileOpenSequence) return;
    content.innerHTML = '<p class="prf-error">Could not load this profile right now.</p>';
  }
}
