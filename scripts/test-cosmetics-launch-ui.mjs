import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const desktopHTML = readFileSync(new URL('../frontend/index.html', import.meta.url), 'utf8');
const mobileHTML = readFileSync(new URL('../frontend/m/index.html', import.meta.url), 'utf8');
const shopHTML = readFileSync(new URL('../frontend/shop/index.html', import.meta.url), 'utf8');
const appSource = readFileSync(new URL('../frontend/js/app.js', import.meta.url), 'utf8');
const mobileSource = readFileSync(new URL('../frontend/m/mobile.js', import.meta.url), 'utf8');
const shopSource = readFileSync(new URL('../frontend/js/cosmetics-shop.js', import.meta.url), 'utf8');
const engineSource = readFileSync(new URL('../frontend/js/renderer/engine.js', import.meta.url), 'utf8');
const botsSource = readFileSync(new URL('../frontend/js/renderer/bots.js', import.meta.url), 'utf8');
const botBodySource = readFileSync(new URL('../frontend/js/renderer/bot-body.js', import.meta.url), 'utf8');
const rigSource = readFileSync(new URL('../frontend/js/renderer/character-rig.js', import.meta.url), 'utf8');

assert.ok(desktopHTML.indexOf('js/cosmetic-themes.js') < desktopHTML.indexOf('js/app.js'),
  'desktop must load procedural themes before the live renderer module chain');
assert.ok(mobileHTML.indexOf('../js/cosmetic-themes.js') < mobileHTML.indexOf('mobile.js'),
  'mobile must load procedural themes before its shared renderer');
assert.match(mobileHTML, /id="fab-shop"[^>]+aria-controls="shop-overlay"/,
  'mobile spectator must open the Shop as a slide-out overlay');
assert.match(mobileHTML, /id="shop-overlay"[\s\S]*?data-src="\/shop\/"/,
  'mobile shop overlay must lazy-load the dedicated Shop document');
assert.doesNotMatch(appSource, /initCosmeticsPanel|cosmetics-panel\.js/,
  'the live Arena must not retain the replaced embedded catalog');

assert.match(desktopHTML, /js\/app\.js\?v=20260718o/);
assert.match(mobileHTML, /mobile\.js\?v=20260718o/);
assert.match(shopHTML, /cosmetics-shop\.js\?v=20260718p/);
assert.match(appSource, /renderer\/engine\.js\?v=20260718m/);
assert.match(mobileSource, /renderer\/engine\.js\?v=20260718m/);
assert.match(shopSource, /shop-preview\.js\?v=20260718o/);
assert.match(engineSource, /bots\.js\?v=20260718o/);
assert.match(engineSource, /trails\.js\?v=20260714e/);
assert.match(botsSource, /bot-body\.js\?v=20260718o/);
assert.match(botsSource, /character-rig\.js\?v=20260718o/);
assert.match(botsSource, /cosmetics\.js\?v=20260714e/);
assert.match(botBodySource, /character-rig\.js\?v=20260718o/);
assert.match(rigSource, /forge-weapons\.js\?v=20260718c/);
assert.doesNotMatch(botsSource, /swordsman-anims\.js|animations\.js/,
  'live renderer must not load retired character animators');
assert.doesNotMatch(botBodySource, /swordsman-body\.js|weapons\.js|animations\.js/,
  'live renderer must not load retired character builders');

console.log('live renderers and dedicated Shop share one fresh cosmetic module identity');
