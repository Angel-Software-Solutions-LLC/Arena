import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';

const source = readFileSync(new URL('../frontend/dashboard/dashboard.js', import.meta.url), 'utf8');
const start = source.indexOf('function normalizeEloHistory');
const end = source.indexOf('function trackElo', start);

assert.notEqual(start, -1, 'dashboard should define its Elo history boundary');
assert.ok(end > start, 'Elo history boundary should be independently testable');

const sandbox = {};
vm.runInNewContext(`${source.slice(start, end)}\nthis.normalizeEloHistory = normalizeEloHistory;`, sandbox);
const normalize = sandbox.normalizeEloHistory;

assert.deepEqual(
  JSON.parse(JSON.stringify(normalize([
    {time:'12:00', elo:1200},
    {time:'<img src=x onerror=alert(1)>'.repeat(3), elo:'1250'},
    {time:'bad', elo:'"><script>alert(1)</script>'},
    null,
  ]))),
  [
    {time:'12:00', elo:1200},
    {time:'<img src=x onerror=alert(1)><img', elo:1250},
  ],
  'persisted history must be bounded and contain only finite numeric Elo values',
);
assert.deepEqual(JSON.parse(JSON.stringify(normalize({elo:1200}))), [], 'non-array storage must be rejected');

const renderStart = source.indexOf('function renderEloHistory');
const renderEnd = source.indexOf('// ========== Analysis ==========', renderStart);
const renderSource = source.slice(renderStart, renderEnd);
assert.match(renderSource, /<title>\$\{esc\(e\.time\)\}: \$\{e\.elo\}<\/title>/,
  'history labels must escape the only persisted string before writing SVG markup');
assert.doesNotMatch(renderSource, /<title>\$\{e\.time\}/,
  'persisted labels must never be interpolated raw');
