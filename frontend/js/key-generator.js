'use strict';

import { apiPath } from './paths.js?v=20260710a';
import { ensureConsent } from './consent-gate.js?v=20260714a';

/**
 * Public, one-time API token generation for the Get Started drawer.
 * The browser keeps the plaintext only in this result field; Arena persists
 * the server-generated credential as a non-recoverable hash.
 * @module key-generator
 */

/**
 * Initialize the public key generator UI.
 * @param {HTMLElement} container - The key generator card.
 */
export function initKeyGenerator(container) {
  const button = container?.querySelector('.keygen-btn');
  const result = container?.querySelector('.keygen-result');
  if (!button || !result) return;

  button.addEventListener('click', async () => {
    const accepted = await ensureConsent();
    if (!accepted) return;

    button.disabled = true;
    button.textContent = 'Generating token...';

    try {
      const data = await generateKey();
      // Keep the previous one-time credential visible until replacement
      // succeeds; only then zero it before rendering the new response.
      clearGeneratedKey(result);
      showKey(result, data);
      button.textContent = 'Generate another token';
    } catch (error) {
      const message = error?.message || 'Token generation failed';
      const existingKey = result.querySelector('#key-display');
      const warning = result.querySelector('.keygen-warning');
      if (existingKey?.value && warning) {
        warning.textContent = `Previous token kept. New token failed: ${message}`;
        warning.setAttribute?.('role', 'alert');
        button.textContent = 'Try generating another token';
      } else {
        result.innerHTML = `<p class="keygen-error" role="alert">Error: ${escapeHTML(message)}</p>`;
        button.textContent = 'Generate API token';
      }
    } finally {
      button.disabled = false;
    }
  });

  if (typeof window !== 'undefined') {
    window.addEventListener('pagehide', () => clearGeneratedKey(result));
  }
}

/** Request a new server-issued token. The request intentionally has no body. */
async function generateKey() {
  const response = await fetch(apiPath('/keys/generate'), { method: 'POST' });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(payload.detail || payload.error || `Request failed (${response.status})`);
  }
  if (typeof payload.api_key !== 'string' || !payload.api_key) {
    throw new Error('Arena did not return a token');
  }
  return payload;
}

/** Show the plaintext once, with explicit copy and clear controls. */
function showKey(container, data) {
  container.innerHTML = `
    <div class="keygen-success">
      <div class="keygen-result-header">
        <span class="keygen-badge">Server token ready</span>
        <button type="button" class="btn btn-secondary keygen-clear" data-keygen-clear>Clear token</button>
      </div>
      <div class="copy-field keygen-copy-field">
        <input type="text" value="${escapeAttribute(data.api_key)}" readonly id="key-display" autocomplete="off" spellcheck="false" aria-label="New Arena API token">
        <button type="button" data-keygen-copy>Copy</button>
      </div>
      <div class="keygen-meta">
        <p class="keygen-warning">Copy this token now. Arena cannot show it again.</p>
        <p class="keygen-bot-id">Bot ID: <code>${escapeHTML(data.bot_id || '')}</code></p>
      </div>
      <a class="keygen-next-link" href="#" data-keygen-dashboard>Claim this bot in My Dashboard to equip cosmetics</a>
    </div>`;

  const keyField = container.querySelector('#key-display');
  const copyButton = container.querySelector('[data-keygen-copy]');
  const clearButton = container.querySelector('[data-keygen-clear]');
  const dashboardLink = container.querySelector('[data-keygen-dashboard]');
  if (keyField) keyField.value = data.api_key;

  // This result panel is injected after page load, so the click listeners
  // setupOverlays() attaches once at startup never reach it -- open the
  // Dashboard drawer directly via the same hook the Shop iframe uses,
  // instead of a page-reloading ?dash_open=1 link.
  dashboardLink?.addEventListener('click', (event) => {
    event.preventDefault();
    window.ArenaOpenDashboard?.({ tab: 'cosmetics' });
  });

  copyButton?.addEventListener('click', async () => {
    if (!keyField?.value) return;
    try {
      if (globalThis.navigator?.clipboard?.writeText) {
        await globalThis.navigator.clipboard.writeText(keyField.value);
      } else {
        keyField.select();
        document.execCommand('copy');
      }
      copyButton.textContent = 'Copied';
      const resetLabel = setTimeout(() => { copyButton.textContent = 'Copy'; }, 2000);
      resetLabel?.unref?.();
    } catch {
      copyButton.textContent = 'Copy failed';
      keyField.select?.();
    }
  });

  clearButton?.addEventListener('click', () => clearGeneratedKey(container));
}

/** Zero and remove a generated plaintext credential from its result container. */
export function clearGeneratedKey(container) {
  if (!container) return;
  const keyField = container.querySelector('#key-display');
  if (keyField) keyField.value = '';
  container.replaceChildren();
}

function escapeHTML(value) {
  const element = document.createElement('div');
  element.textContent = String(value);
  return element.innerHTML;
}

function escapeAttribute(value) {
  return String(value).replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}
