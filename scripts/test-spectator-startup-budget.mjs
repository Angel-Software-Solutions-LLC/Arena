import assert from 'node:assert/strict';
import { brotliCompressSync, constants as zlibConstants } from 'node:zlib';
import { existsSync, readFileSync, readdirSync } from 'node:fs';

const read = (path) => readFileSync(new URL(path, import.meta.url), 'utf8');
const desktop = read('../frontend/index.html');
const mobile = read('../frontend/m/index.html');
const shop = read('../frontend/shop/index.html');
const app = read('../frontend/js/app.js');
const mobileApp = read('../frontend/m/mobile.js');
const shopApp = read('../frontend/js/cosmetics-shop.js');
const bots = read('../frontend/js/renderer/bots.js');
const botBody = read('../frontend/js/renderer/bot-body.js');
const worldHud = read('../frontend/js/renderer/world-hud.js');

for (const [route, html] of [['desktop', desktop], ['mobile', mobile], ['shop', shop]]) {
  assert.doesNotMatch(html, /babylonjs-gui|babylon\.gui/i,
    `${route} spectator must not eagerly load Babylon GUI`);
  assert.doesNotMatch(html, /cdn\.jsdelivr\.net\/npm\/(?:babylonjs|earcut)|cdn\.babylonjs\.com/i,
    `${route} must not depend on a third-party Babylon runtime CDN`);
}

for (const [route, entry] of [['desktop', app], ['mobile', mobileApp], ['shop', shopApp]]) {
  assert.match(entry, /import ['"](?:\.\/|\.\.\/js\/)babylon-runtime\.js(?:\?v=[^'"]+)?['"];/,
    `${route} entry point must load the local Babylon compatibility bridge first`);
}

const bridge = read('../frontend/js/babylon-runtime.js');
const assetMatch = bridge.match(/\.\.\/assets\/vendor\/(babylon-runtime\.([a-f0-9]{12})\.min\.js)/);
assert.ok(assetMatch, 'bridge must import a content-hashed local Babylon runtime');
for (const [route, html] of [['desktop', desktop], ['mobile', mobile], ['shop', shop]]) {
  assert.match(html, new RegExp(`modulepreload[^>]+${assetMatch[1].replaceAll('.', '\\.')}`),
    `${route} should preload the bridge-referenced Babylon runtime`);
}
const assetURL = new URL(`../frontend/assets/vendor/${assetMatch[1]}`, import.meta.url);
assert.ok(existsSync(assetURL), `checked-in Babylon runtime is missing: ${assetMatch[1]}`);
assert.deepEqual(
  readdirSync(new URL('../frontend/assets/vendor/', import.meta.url))
    .filter(name => /^babylon-runtime\.[a-f0-9]{12}\.min\.js$/.test(name)),
  [assetMatch[1]],
  'only the bridge-referenced Babylon runtime should be shipped',
);
const runtime = readFileSync(assetURL);
const compressed = brotliCompressSync(runtime, {
  params: { [zlibConstants.BROTLI_PARAM_QUALITY]: 11 },
});
// The ceiling rose from 2.30 MB when the required side-effect modules
// (postProcess presentation, shadow/effect-layer/particle scene components,
// Ray) joined the bundle — without them the runtime was smaller but rendered
// a black arena (see vendor-src/babylon-runtime-entry.mjs). It rose again
// from 2.35 MB when @babylonjs/core moved 9.14.0 -> 9.21.2 (2,385,023 raw);
// the Brotli cold-start budget below is the binding constraint and did not
// move — 9.21.2 compresses to ~416 KB, well inside it.
assert.ok(runtime.byteLength <= 2_450_000,
  `Babylon runtime raw size ${runtime.byteLength} exceeds 2.45 MB safety ceiling`);
assert.ok(compressed.byteLength <= 500_000,
  `Babylon runtime Brotli size ${compressed.byteLength} exceeds 500 KB cold-start budget`);
assert.match(runtime.toString('utf8'), /BABYLON/,
  'runtime must preserve the global BABYLON compatibility surface');

assert.doesNotMatch(bots, /BABYLON\.GUI|getGuiTexture|summaryPanel|summaryText/,
  'selected-bot details must not recreate a fullscreen Babylon GUI texture');
assert.doesNotMatch(botBody, /AdvancedDynamicTexture|BABYLON\.GUI|getGuiTexture/,
  'bot construction must remain independent of Babylon GUI');
assert.match(worldHud, /showWorldSelection/);
assert.match(worldHud, /hideWorldSelection/);

console.log(`spectator startup budget: ${runtime.byteLength} raw / ${compressed.byteLength} Brotli bytes`);
