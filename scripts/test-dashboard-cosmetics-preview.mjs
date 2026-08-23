import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

import {
  BABYLON_SCRIPT_URL,
  DashboardCosmeticsPreview,
  loadPinnedBabylon,
} from '../frontend/dashboard/cosmetics-preview.js';

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return {promise, resolve, reject};
}

function element() {
  return {
    hidden: false,
    textContent: '',
    dataset: {},
    setAttribute(name, value) {
      this[name] = String(value);
    },
  };
}

function makeHarness(overrides = {}) {
  const calls = [];
  const instances = [];
  class FakePreview {
    constructor(canvas, options) {
      this.canvas = canvas;
      this.options = options;
      this.disposed = false;
      instances.push(this);
      calls.push(['construct', canvas, options]);
    }
    init() {
      calls.push(['init']);
      return this;
    }
    setCharacter(value) {
      calls.push(['character', structuredClone(value)]);
      return this;
    }
    setLoadout(value) {
      calls.push(['loadout', structuredClone(value)]);
      return this;
    }
    dispose() {
      if (this.disposed) throw new Error('preview disposed twice');
      this.disposed = true;
      calls.push(['dispose']);
    }
  }

  const canvas = element();
  const status = element();
  const controller = new DashboardCosmeticsPreview({
    canvas,
    status,
    loadBabylon: async () => calls.push(['babylon']),
    loadPreviewClass: async () => FakePreview,
    ...overrides,
  });
  return {calls, canvas, controller, instances, status, FakePreview};
}

const BOT_ALPHA = {
  id: 'bot-alpha',
  name: 'Alpha',
  avatar_color: '#12abef',
  default_weapon: 'spear',
};
const CURRENT = {
  bot_skin: 'standard',
  weapon_skin: 'standard',
  attachment: 'none',
  trail: 'standard',
};

assert.equal(
  BABYLON_SCRIPT_URL,
  '../js/babylon-runtime.js?v=20260810d',
  'Dashboard must lazy-load the same local Babylon bridge as the Arena',
);

{
  function fakeScript() {
    const listeners = new Map();
    return {
      dataset: {},
      removed: 0,
      addEventListener(type, handler) {
        listeners.set(type, handler);
      },
      fire(type) {
        listeners.get(type)?.({type});
      },
      remove() {
        this.removed += 1;
      },
    };
  }
  const existing = fakeScript();
  const created = [];
  let queryCount = 0;
  const windowObject = {
    BABYLON: null,
    setTimeout: () => 1,
    clearTimeout: () => {},
  };
  const documentObject = {
    head: {append: () => {}},
    querySelector: () => (queryCount++ === 0 ? existing : null),
    createElement: () => {
      const script = fakeScript();
      created.push(script);
      return script;
    },
  };

  const externalFailure = loadPinnedBabylon({windowObject, documentObject});
  existing.fire('error');
  await assert.rejects(externalFailure, /could not be downloaded/i);
  assert.equal(existing.removed, 0, 'a failed script owned by another surface must not be removed');

  const ownedFailure = loadPinnedBabylon({windowObject, documentObject});
  assert.equal(created[0].dataset.arenaCosmeticsPreview, 'babylon-runtime-local');
  assert.equal(created[0].type, 'module');
  created[0].fire('error');
  await assert.rejects(ownedFailure, /could not be downloaded/i);
  assert.equal(created[0].removed, 1, 'a failed Dashboard-owned script must be removed so retry can reload it');

  const retry = loadPinnedBabylon({windowObject, documentObject});
  assert.notEqual(created[1], created[0], 'retry must create a fresh script instead of waiting on a failed node');
  windowObject.BABYLON = {version: '9.14.0'};
  created[1].fire('load');
  assert.equal((await retry).version, '9.14.0');
}

{
  const {calls, controller, status} = makeHarness();
  await controller.update({active: false, verified: true, bot: BOT_ALPHA, loadout: CURRENT});
  assert.deepEqual(calls, [], 'an inactive Cosmetics tab must not load Babylon or the preview renderer');
  await controller.update({active: true, verified: false, bot: BOT_ALPHA, loadout: CURRENT});
  assert.deepEqual(calls, [], 'an unverified account must not load preview dependencies');
  await controller.update({active: true, verified: true, bot: null, loadout: CURRENT});
  assert.deepEqual(calls, [], 'an account without a linked bot must not load preview dependencies');
  assert.match(status.textContent, /link a bot/i, 'the no-bot fallback must explain how to unlock preview');
}

{
  const {calls, canvas, controller, instances, status} = makeHarness();
  await controller.update({active: true, verified: true, bot: BOT_ALPHA, loadout: CURRENT});
  assert.equal(instances.length, 1, 'the outfitter should create one engine/model');
  assert.equal(calls.filter(([name]) => name === 'babylon').length, 1);
  assert.deepEqual(calls.find(([name]) => name === 'character').slice(1), [{
    avatarColor: '#12abef',
    weapon: 'spear',
  }], 'preview character must use the linked bot color and default weapon exactly');
  assert.deepEqual(calls.find(([name]) => name === 'loadout').slice(1), [CURRENT]);
  assert.equal(canvas.hidden, false);
  assert.equal(status.hidden, true);

  const callCount = calls.length;
  await controller.update({active: true, verified: true, bot: BOT_ALPHA, loadout: {...CURRENT}});
  assert.equal(calls.length, callCount, 'an unchanged signature must not rebuild or reapply the model');

  const staged = {...CURRENT, bot_skin: 'body_giant_chicken', trail: 'neon_rain'};
  await controller.update({active: true, verified: true, bot: BOT_ALPHA, loadout: staged});
  assert.equal(instances.length, 1, 'changing staged cosmetics must reuse the existing preview engine');
  assert.deepEqual(calls.at(-1), ['loadout', staged]);

  await controller.update({active: false, verified: true, bot: BOT_ALPHA, loadout: staged});
  assert.equal(calls.filter(([name]) => name === 'dispose').length, 1, 'leaving Cosmetics should release the preview engine');
  await controller.update({active: false, verified: true, bot: BOT_ALPHA, loadout: staged});
  assert.equal(calls.filter(([name]) => name === 'dispose').length, 1, 'deactivation disposal must be idempotent');
  await controller.update({active: true, verified: true, bot: BOT_ALPHA, loadout: staged});
  assert.equal(instances.length, 2, 'returning to Cosmetics may recreate one fresh preview');
  controller.dispose();
  controller.dispose();
  assert.equal(calls.filter(([name]) => name === 'dispose').length, 2, 'controller disposal must be idempotent');
}

{
  const gate = deferred();
  const {calls, controller, instances, FakePreview} = makeHarness({
    loadBabylon: () => gate.promise,
    loadPreviewClass: async () => FakePreview,
  });
  const first = controller.update({active: true, verified: true, bot: BOT_ALPHA, loadout: CURRENT});
  const botBeta = {...BOT_ALPHA, id: 'bot-beta', avatar_color: '#ff8800', default_weapon: 'staff'};
  const secondLoadout = {...CURRENT, attachment: 'arena_set_004_attachment'};
  const second = controller.update({active: true, verified: true, bot: botBeta, loadout: secondLoadout});
  gate.resolve();
  await Promise.all([first, second]);
  assert.equal(instances.length, 1, 'racing async loads must still create only the latest preview');
  assert.deepEqual(calls.find(([name]) => name === 'character').slice(1), [{
    avatarColor: '#ff8800',
    weapon: 'staff',
  }], 'generation guards must prevent stale bot state from reaching the model');
  assert.deepEqual(calls.at(-1), ['loadout', secondLoadout]);
}

const dashboardHTML = readFileSync(new URL('../frontend/dashboard/index.html', import.meta.url), 'utf8')
  // The dashboard runtime was extracted from the inline <script> to
  // dashboard.js; these probes span both, so read them as one source.
  + readFileSync(new URL('../frontend/dashboard/dashboard.js', import.meta.url), 'utf8');
const outfitterIndex = dashboardHTML.indexOf('id="accountCosmeticsOutfitter"');
const inventoryIndex = dashboardHTML.indexOf('id="accountCosmeticsPanel"');
assert.ok(outfitterIndex >= 0 && inventoryIndex > outfitterIndex,
  'the stable outfitter canvas must live outside and before the dynamically replaced inventory panel');
assert.match(dashboardHTML, /id="accountCosmeticsPreviewCanvas"/);
assert.match(dashboardHTML, /id="accountCosmeticsPreviewBot"/);
assert.match(dashboardHTML, /data-cosmetics-preview-reset/);
assert.match(dashboardHTML, /Current(?:ly equipped)?[\s\S]*Preview/, 'outfitter must distinguish server-equipped and staged visuals');
assert.match(dashboardHTML, /import\('\.\/cosmetics-preview\.js\?v=20260823b'\)/,
  'the heavy preview controller must be loaded lazily');
assert.match(dashboardHTML, /ArenaAccountCosmetics\.previewModel/,
  'Dashboard runtime must revalidate staged license IDs through the pure preview model');
assert.match(dashboardHTML, /ArenaAccountCosmetics\.equippedLoadout/,
  'Dashboard runtime must derive current visuals from server-authoritative equipped licenses');

const previewHandlerStart = dashboardHTML.indexOf('function previewAccountLicense');
const previewHandlerEnd = dashboardHTML.indexOf('async function equipAccountLicense', previewHandlerStart);
assert.ok(previewHandlerStart >= 0 && previewHandlerEnd > previewHandlerStart);
const previewHandler = dashboardHTML.slice(previewHandlerStart, previewHandlerEnd);
assert.doesNotMatch(previewHandler, /accountRequest|fetch\(/,
  'Preview and Reset must never call a mutation endpoint; Equip remains the explicit mutation');
assert.match(dashboardHTML, /getElementById\('tab-cosmetics'\)[\s\S]*addEventListener\('click', handleAccountPanelClick\)/,
  'delegation must be bound to the stable Cosmetics tab, not the replaceable inventory panel');

console.log('Dashboard cosmetics outfitter lifecycle, staging safety, and stable UI contract pass');
