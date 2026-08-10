import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

class FakeElement {
  constructor(tagName = 'div') {
    this.tagName = tagName.toUpperCase();
    this.hidden = false;
    this.textContent = '';
    this.dataset = {};
    this.listeners = new Map();
    this.childNodes = [];
  }
  querySelector(selector) {
    for (const candidate of selector.split(',').map((value) => value.trim())) {
      if (this.children?.[candidate]) return this.children[candidate];
      const found = this.findDeep(candidate);
      if (found) return found;
    }
    return null;
  }
  findDeep(selector) {
    for (const child of this.childNodes) {
      const dataMatch = selector.match(/^\[data-([a-z-]+)\]$/);
      const key = dataMatch ? dataMatch[1].replace(/-([a-z])/g, (_, c) => c.toUpperCase()) : null;
      if (key !== null && key in child.dataset) return child;
      if (!dataMatch && child.tagName === selector.toUpperCase()) return child;
      const nested = child.findDeep(selector);
      if (nested) return nested;
    }
    return null;
  }
  append(...nodes) { this.childNodes.push(...nodes); }
  appendChild(node) { this.childNodes.push(node); return node; }
  setAttribute() {}
  focus() {}
  addEventListener(type, fn) { this.listeners.set(type, fn); }
  removeEventListener(type) { this.listeners.delete(type); }
  removeAttribute(name) {
    if (name === 'data-kind') delete this.dataset.kind;
    if (name === 'data-severity') delete this.dataset.severity;
  }
  click() { this.listeners.get('click')?.(); }
}

const message = new FakeElement();
const dismiss = new FakeElement();
const root = new FakeElement();
root.children = {
  '[data-service-status-message]': message,
  '[data-service-status-dismiss]': dismiss,
};

const storage = new Map();
globalThis.window = { location: { pathname: '/', protocol: 'https:', host: 'arena.example' } };
const fakeHead = new FakeElement('head');
const fakeBody = new FakeElement('body');
globalThis.document = {
  getElementById: (id) => id === 'service-status-banner' ? root : null,
  createElement: (tag) => new FakeElement(tag),
  head: fakeHead,
  body: fakeBody,
  addEventListener: () => {},
};
globalThis.sessionStorage = {
  getItem: (key) => storage.get(key) || null,
  setItem: (key, value) => storage.set(key, value),
};
globalThis.fetch = async () => ({
  ok: true,
  json: async () => ({ type: 'service_status', revision: 0, broadcast: null, maintenance: null }),
});

let source = readFileSync(new URL('../frontend/js/service-status.js', import.meta.url), 'utf8');
source = source.replace("import { apiPath } from './paths.js?v=20260710a';", "const apiPath = (path) => '/api/v1' + path;");
const module = await import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);

module.handleServiceStatus({
  type: 'service_status', revision: 2, broadcast: { id: 2, severity: 'info', message: '<b>plain text</b>' }, maintenance: null,
});
const controller = module.initServiceStatus({ root, pollIntervalMs: 60000 });
assert.equal(root.hidden, false);
assert.equal(message.textContent, '<b>plain text</b>', 'notice must be rendered as text, not HTML');
assert.equal(root.dataset.kind, 'broadcast');
assert.equal(dismiss.hidden, false);

dismiss.click();
assert.equal(root.hidden, true, 'manual broadcast should be session-dismissible');

controller.handleStatus({
  type: 'service_status', revision: 3, broadcast: null,
  maintenance: { id: 3, severity: 'warning', message: 'Restarting now', retry_after_seconds: 60 },
});
assert.equal(root.hidden, false);
assert.equal(root.dataset.kind, 'maintenance');
assert.equal(dismiss.hidden, true, 'maintenance must not be dismissible');

const popup = fakeBody.childNodes[0];
assert.ok(popup, 'maintenance notice must raise the popup');
assert.equal(popup.hidden, false);
assert.equal(popup.findDeep('[data-popup-message]').textContent, 'Restarting now');
popup.findDeep('button').click();
assert.equal(popup.hidden, true, 'acknowledging must hide the popup');
assert.equal(root.hidden, false, 'acknowledging the popup must keep the banner');
controller.handleStatus({
  type: 'service_status', revision: 3, broadcast: null,
  maintenance: { id: 3, severity: 'warning', message: 'Restarting now', retry_after_seconds: 60 },
});
assert.equal(popup.hidden, true, 'an acknowledged notice must not pop again');
controller.handleStatus({
  type: 'service_status', revision: 5, broadcast: null,
  maintenance: { id: 5, severity: 'warning', message: 'Restarting in 1 minute', phase: 'scheduled', retry_after_seconds: 60 },
});
assert.equal(popup.hidden, false, 'a re-issued warning (new id) must pop again');
assert.equal(popup.findDeep('[data-popup-title]').textContent, 'Scheduled maintenance');

assert.equal(controller.handleStatus({ type: 'service_status', revision: 2, broadcast: null, maintenance: null }), false, 'older revisions must be ignored');
assert.equal(message.textContent, 'Restarting in 1 minute');
controller.handleStatus({ type: 'service_status', revision: 6, broadcast: null, maintenance: null });
assert.equal(root.hidden, true, 'clear snapshot should hide the banner');
assert.equal(popup.hidden, true, 'clear snapshot should hide the popup');

controller.destroy();
console.log('service status UI checks passed');
