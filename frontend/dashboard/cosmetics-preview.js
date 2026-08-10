'use strict';

export const BABYLON_SCRIPT_URL = '../js/babylon-runtime.js?v=20260810d';

let babylonLoadPromise = null;

function defaultWindow() {
  return typeof window === 'undefined' ? null : window;
}

function defaultDocument() {
  return typeof document === 'undefined' ? null : document;
}

export function loadPinnedBabylon({windowObject = defaultWindow(), documentObject = defaultDocument()} = {}) {
  if (windowObject?.BABYLON) return Promise.resolve(windowObject.BABYLON);
  if (!windowObject || !documentObject?.head) {
    return Promise.reject(new Error('Babylon.js cannot load outside a browser document'));
  }
  if (babylonLoadPromise) return babylonLoadPromise;

  babylonLoadPromise = new Promise((resolve, reject) => {
    const existing = documentObject.querySelector?.(`script[src="${BABYLON_SCRIPT_URL}"]`);
    const script = existing || documentObject.createElement('script');
    const createdHere = !existing;
    let settled = false;
    const timer = windowObject.setTimeout?.(
      () => finish(new Error('The 3D preview renderer took too long to load')),
      15000,
    );
    const finish = (error) => {
      if (settled) return;
      settled = true;
      if (timer) windowObject.clearTimeout?.(timer);
      if (error || !windowObject.BABYLON) {
        babylonLoadPromise = null;
        if (createdHere && script.dataset?.arenaCosmeticsPreview === 'babylon-runtime-local') script.remove?.();
        reject(error || new Error('Babylon.js loaded without exposing its renderer'));
        return;
      }
      resolve(windowObject.BABYLON);
    };

    script.addEventListener('load', () => finish(), {once: true});
    script.addEventListener('error', () => finish(new Error('The 3D preview renderer could not be downloaded')), {once: true});
    if (createdHere) {
      script.src = BABYLON_SCRIPT_URL;
      script.type = 'module';
      script.async = true;
      script.dataset.arenaCosmeticsPreview = 'babylon-runtime-local';
      documentObject.head.append(script);
    } else if (windowObject.BABYLON) {
      finish();
    }
  });
  return babylonLoadPromise;
}

async function defaultPreviewClassLoader() {
  const module = await import('../js/shop-preview.js?v=20260714e');
  if (typeof module.CosmeticShopPreview !== 'function') {
    throw new Error('The Arena cosmetic preview module is unavailable');
  }
  return module.CosmeticShopPreview;
}

function clean(value, fallback = '') {
  return typeof value === 'string' && value.trim() ? value.trim() : fallback;
}

function previewSignature(bot, loadout) {
  return JSON.stringify({
    botID: clean(bot?.id),
    avatarColor: clean(bot?.avatar_color, '#5edfff'),
    weapon: clean(bot?.default_weapon, 'sword'),
    loadout: {
      bot_skin: clean(loadout?.bot_skin, 'standard'),
      weapon_skin: clean(loadout?.weapon_skin, 'standard'),
      attachment: clean(loadout?.attachment, 'none'),
      trail: clean(loadout?.trail, 'standard'),
    },
  });
}

/**
 * Owns at most one Cosmetics-tab preview. Dependencies are intentionally
 * loaded only after the verified-account, linked-bot, and active-tab gates.
 */
export class DashboardCosmeticsPreview {
  constructor({
    canvas,
    status,
    loadBabylon = loadPinnedBabylon,
    loadPreviewClass = defaultPreviewClassLoader,
  } = {}) {
    if (!canvas) throw new TypeError('DashboardCosmeticsPreview requires a canvas');
    if (!status) throw new TypeError('DashboardCosmeticsPreview requires a status element');
    this.canvas = canvas;
    this.status = status;
    this.loadBabylon = loadBabylon;
    this.loadPreviewClass = loadPreviewClass;
    this.preview = null;
    this.signature = '';
    this.generation = 0;
    this.disposed = false;
    this._showStatus('Select a linked bot to preview cosmetics.');
  }

  _showStatus(message) {
    this.status.textContent = message;
    this.status.hidden = false;
    this.canvas.hidden = true;
    this.canvas.setAttribute?.('aria-busy', message ? 'true' : 'false');
  }

  _showCanvas() {
    this.canvas.hidden = false;
    this.canvas.setAttribute?.('aria-busy', 'false');
    this.status.textContent = '';
    this.status.hidden = true;
  }

  _teardownPreview() {
    const preview = this.preview;
    this.preview = null;
    this.signature = '';
    if (!preview) return;
    try {
      preview.dispose();
    } catch (_) {
      // Disposal is best-effort during tab changes/page teardown. Clearing the
      // owned reference first prevents a broken third-party context looping.
    }
  }

  async update({active = false, verified = false, bot = null, loadout = null} = {}) {
    if (this.disposed) return false;
    const generation = ++this.generation;
    if (!active) {
      this._teardownPreview();
      this._showStatus('Open Cosmetics to preview this bot.');
      return false;
    }
    if (!verified) {
      this._teardownPreview();
      this._showStatus('Verify your email account to preview cosmetics.');
      return false;
    }
    if (!bot?.id) {
      this._teardownPreview();
      this._showStatus('Link a bot to preview your owned cosmetics.');
      return false;
    }

    const normalizedLoadout = {
      bot_skin: clean(loadout?.bot_skin, 'standard'),
      weapon_skin: clean(loadout?.weapon_skin, 'standard'),
      attachment: clean(loadout?.attachment, 'none'),
      trail: clean(loadout?.trail, 'standard'),
    };
    const signature = previewSignature(bot, normalizedLoadout);
    if (this.preview && this.signature === signature) return true;
    if (!this.preview) this._showStatus(`Loading 3D preview for ${clean(bot.name, 'your bot')}...`);

    try {
      await this.loadBabylon();
      if (this.disposed || generation !== this.generation) return false;
      const PreviewClass = await this.loadPreviewClass();
      if (this.disposed || generation !== this.generation) return false;
      if (!this.preview) {
        const candidate = new PreviewClass(this.canvas, {
          avatarColor: clean(bot.avatar_color, '#5edfff'),
          weapon: clean(bot.default_weapon, 'sword'),
          autoRotate: true,
        });
        this.preview = candidate.init() || candidate;
      }
      this.preview.setCharacter({
        avatarColor: clean(bot.avatar_color, '#5edfff'),
        weapon: clean(bot.default_weapon, 'sword'),
      });
      this.preview.setLoadout(normalizedLoadout);
      if (this.disposed || generation !== this.generation) {
        this._teardownPreview();
        return false;
      }
      this.signature = signature;
      this._showCanvas();
      return true;
    } catch (error) {
      if (this.disposed || generation !== this.generation) return false;
      this._teardownPreview();
      this._showStatus('3D preview unavailable. Your equipped and staged cosmetics are still listed here.');
      return false;
    }
  }

  dispose() {
    if (this.disposed) return;
    this.disposed = true;
    this.generation += 1;
    this._teardownPreview();
    this._showStatus('3D preview closed.');
  }
}
