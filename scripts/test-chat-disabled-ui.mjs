import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

class FakeClassList {
  constructor(...names) { this.names = new Set(names); }
  contains(name) { return this.names.has(name); }
  remove(...names) { names.forEach((name) => this.names.delete(name)); }
  toggle(name, force) {
    if (force) this.names.add(name);
    else this.names.delete(name);
  }
}

class FakeElement {
  constructor(...classes) {
    this.attributes = new Map();
    this.classList = new FakeClassList(...classes);
    this.disabled = false;
    this.hidden = false;
    this.title = '';
  }
  setAttribute(name, value) { this.attributes.set(name, String(value)); }
  getAttribute(name) { return this.attributes.get(name); }
}

globalThis.document = {
  addEventListener() {},
};

let source = readFileSync(new URL('../frontend/js/chat-panel.js', import.meta.url), 'utf8');
assert.doesNotMatch(
  source,
  /if\s*\(!cfg\s*\|\|\s*!cfg\.enabled\)\s*return/,
  'an initially disabled chat must still initialize its disabled launcher',
);
const css = readFileSync(new URL('../frontend/css/chat.css', import.meta.url), 'utf8');
assert.match(css, /\.chat-bubble\.is-disabled::after[\s\S]*\.fab-chat\.is-disabled::after/, 'disabled chat launchers need the visual strike');
source = source
  .replace("import { apiPath, wsURL } from './paths.js?v=20260710a';", "const apiPath = (path) => path; const wsURL = (path) => path;")
  .replace("import { openProfilePopup } from './profile-popup.js?v=20260714a';", 'const openProfilePopup = () => {};')
  .replace(
    "import { startSignIn, watchSignInState } from './sign-in.js?v=20260825a';",
    'const startSignIn = async () => ({status: \'closed\'}); const watchSignInState = () => {};',
  );

const { setChatAvailability } = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);
assert.equal(typeof setChatAvailability, 'function', 'chat-panel must expose its availability transition for regression coverage');

const overlay = new FakeElement('open');
overlay.setAttribute('aria-hidden', 'false');
const launcherEl = new FakeElement('is-active', 'active');
launcherEl.setAttribute('aria-label', 'Open chat');
launcherEl.setAttribute('aria-expanded', 'true');
const watermark = new FakeElement();

setChatAvailability({ enabled: false, overlay, launcherEl, watermark });
assert.equal(overlay.classList.contains('open'), false, 'disabling chat must close the visible panel');
assert.equal(overlay.getAttribute('aria-hidden'), 'true');
assert.equal(launcherEl.disabled, true, 'the chat launcher must not reopen a disabled panel');
assert.equal(launcherEl.classList.contains('is-disabled'), true);
assert.equal(launcherEl.classList.contains('is-active'), false);
assert.equal(launcherEl.classList.contains('active'), false);
assert.equal(launcherEl.getAttribute('aria-label'), 'Chat disabled');
assert.equal(launcherEl.getAttribute('aria-expanded'), 'false');
assert.equal(launcherEl.title, 'Chat disabled');
assert.equal(watermark.hidden, true, 'the sign-in watermark must disappear when chat is disabled');

setChatAvailability({ enabled: true, overlay, launcherEl, watermark });
assert.equal(launcherEl.disabled, false, 're-enabling chat must restore the launcher');
assert.equal(launcherEl.classList.contains('is-disabled'), false);
assert.equal(launcherEl.getAttribute('aria-label'), 'Open chat');
assert.equal(launcherEl.title, 'Open chat');

const openOverlay = new FakeElement('open');
const activeLauncher = new FakeElement('is-active', 'active');
activeLauncher.setAttribute('aria-expanded', 'true');
setChatAvailability({ enabled: true, overlay: openOverlay, launcherEl: activeLauncher, watermark });
assert.equal(openOverlay.classList.contains('open'), true, 'an enabled status must not close an already open panel');
assert.equal(activeLauncher.classList.contains('is-active'), true);
assert.equal(activeLauncher.classList.contains('active'), true);
assert.equal(activeLauncher.getAttribute('aria-expanded'), 'true');

console.log('disabled chat closes the panel and leaves a clearly disabled launcher');
