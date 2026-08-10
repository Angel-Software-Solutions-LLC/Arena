import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';

const html = readFileSync(new URL('../frontend/admin/index.html', import.meta.url), 'utf8');

assert.match(
  html,
  /let _versionInfoRequestGeneration = 0;/,
  'Release Control must track version-load generations so stale requests cannot clear the update target',
);
assert.match(html, /pattern="\[a-z0-9\]\[a-z0-9_\\-\]\*"/, 'cosmetics slug patterns must escape hyphen for modern HTML pattern validation');
assert.match(html, /pattern="\[a-z\]\[a-z0-9_\\-\]\*"/, 'cosmetics rarity pattern must escape hyphen for modern HTML pattern validation');

const start = html.indexOf('// ========== Version & Self-Update ==========');
const end = html.indexOf('function showUpdateOverlay', start);
assert(start >= 0 && end > start, 'Release Control functions must be present');
const releaseControlCode = html.slice(start, end) + '\nglobalThis.__releaseControl = { loadVersionInfo, startUpdate };';

const elements = new Map([
  ['versionInfo', { style: {}, textContent: '', innerHTML: '' }],
  ['updateBtn', { style: {}, textContent: '' }],
  ['updateResult', { style: {}, textContent: '' }],
]);
const pendingVersionLoads = [];
const requests = [];
const context = {
  document: { getElementById: (id) => elements.get(id) ?? null },
  esc: (value) => String(value),
  confirm: () => true,
  api: (path, options) => {
    requests.push({ path, options });
    if (path === '/version') {
      return new Promise((resolve, reject) => pendingVersionLoads.push({ resolve, reject }));
    }
    if (path === '/update') return new Promise(() => {});
    throw new Error(`unexpected API path: ${path}`);
  },
};
vm.runInNewContext(releaseControlCode, context);

const firstLoad = context.__releaseControl.loadVersionInfo();
const secondLoad = context.__releaseControl.loadVersionInfo();
assert.equal(pendingVersionLoads.length, 2, 'test setup must create overlapping version checks');
pendingVersionLoads[1].resolve({
  running: { short: 'old', commit: 'old' },
  latest: { short: 'new', commit: 'new-commit' },
  branch: 'main',
  commitsBehind: 1,
  updaterConfigured: true,
});
await secondLoad;
pendingVersionLoads[0].reject(new Error('stale version probe failed'));
await firstLoad;

assert.equal(elements.get('updateBtn').style.display, '', 'newer successful version response must keep Update visible after an older request fails');
void context.__releaseControl.startUpdate();
const updateRequest = requests.at(-1);
assert.equal(updateRequest.path, '/update', 'Update must POST to the self-update endpoint');
assert.equal(updateRequest.options.method, 'POST', 'Update must use POST');
assert.equal(
  updateRequest.options.body,
  JSON.stringify({ commitSha: 'new-commit' }),
  'Update must POST the latest target after stale version response failure',
);

console.log('admin Release Control race and HTML pattern checks passed');
