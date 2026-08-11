#!/usr/bin/env node
/**
 * Post-deploy render verification.
 *
 * Loads a live Arena spectator page in a real browser, waits for the renderer
 * to produce frames, and reads back the actual framebuffer. Exits non-zero if
 * the canvas is effectively black.
 *
 * This exists because a total rendering outage (the vendored-Babylon
 * side-effect regression) survived four weeks of green CI: every other signal
 * -- HTTP status, WebSocket traffic, HUD updates, mesh counts, even Babylon's
 * own frameId -- stayed healthy while nothing was drawn. Pixels are the only
 * check that could not be fooled.
 *
 * Usage:
 *   node scripts/verify-deploy-render.mjs https://arena.example.com
 *   node scripts/verify-deploy-render.mjs http://127.0.0.1:8700 --min-lit=0.05
 *
 * Options:
 *   --min-lit=<0..1>   minimum fraction of non-black pixels (default 0.05)
 *   --timeout=<ms>     overall budget (default 90000)
 *   --screenshot=<p>   write a PNG for a human to eyeball on failure
 *   --mobile           check the /m/ mobile spectator instead
 */
import { createRequire } from 'node:module';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const require = createRequire(resolve(here, '..', 'tests', 'browser', 'package.json'));

let chromium;
try {
  ({ chromium } = require('@playwright/test'));
} catch {
  console.error('playwright is not installed; run: npm ci --prefix tests/browser');
  process.exit(2);
}

const args = process.argv.slice(2);
const target = args.find((a) => !a.startsWith('--'));
if (!target) {
  console.error('usage: node scripts/verify-deploy-render.mjs <base-url> [--min-lit=0.05] [--mobile]');
  process.exit(2);
}
const opt = (name, fallback) => {
  const hit = args.find((a) => a.startsWith(`--${name}=`));
  return hit ? hit.slice(name.length + 3) : fallback;
};
const minLit = Number(opt('min-lit', '0.05'));
const budgetMs = Number(opt('timeout', '90000'));
const shotPath = opt('screenshot', '');
const mobile = args.includes('--mobile');
const url = new URL(mobile ? '/m/' : '/', target).toString();

const browser = await chromium.launch({
  // Software rendering keeps this runnable on a headless deploy host with no
  // GPU. PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH points at an already-installed
  // Chromium when the host does not use Playwright's own browser downloads.
  args: ['--use-angle=swiftshader', '--enable-unsafe-swiftshader', '--disable-vulkan'],
  ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
    ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH }
    : {}),
});
const page = await browser.newPage({
  viewport: mobile ? { width: 390, height: 844 } : { width: 1280, height: 800 },
});
const consoleErrors = [];
page.on('pageerror', (e) => consoleErrors.push(String(e).slice(0, 200)));
page.on('console', (m) => { if (m.type() === 'error') consoleErrors.push(m.text().slice(0, 200)); });

let verdict = { ok: false, reason: 'did not run' };
try {
  await page.goto(url, { waitUntil: 'domcontentloaded', timeout: budgetMs });
  // Wait for a Babylon scene that is actually advancing frames.
  await page.waitForFunction(
    () => {
      const scene = window.BABYLON?.EngineStore?.LastCreatedScene;
      return !!scene && scene.getEngine().frameId > 3;
    },
    null,
    { timeout: budgetMs },
  );
  // Read the framebuffer inside onAfterRender, while it is still valid.
  const pixels = await page.evaluate((limitMs) => new Promise((done) => {
    const scene = window.BABYLON.EngineStore.LastCreatedScene;
    const timer = setTimeout(() => done({ error: 'onAfterRender never fired' }), limitMs);
    scene.onAfterRenderObservable.addOnce(() => {
      clearTimeout(timer);
      try {
        const gl = scene.getEngine()._gl;
        const w = gl.drawingBufferWidth, h = gl.drawingBufferHeight;
        const buf = new Uint8Array(w * h * 4);
        gl.readPixels(0, 0, w, h, gl.RGBA, gl.UNSIGNED_BYTE, buf);
        let lit = 0;
        for (let i = 0; i < buf.length; i += 4) {
          if (buf[i] + buf[i + 1] + buf[i + 2] > 30) lit += 1;
        }
        done({ litFraction: lit / (w * h), width: w, height: h });
      } catch (err) {
        done({ error: String(err) });
      }
    });
  }), Math.min(30000, budgetMs));

  if (pixels.error) {
    verdict = { ok: false, reason: pixels.error };
  } else if (pixels.litFraction < minLit) {
    verdict = {
      ok: false,
      reason: `canvas is effectively black: ${(pixels.litFraction * 100).toFixed(2)}% lit ` +
        `(need >= ${(minLit * 100).toFixed(2)}%)`,
      pixels,
    };
  } else {
    verdict = { ok: true, pixels };
  }
} catch (err) {
  verdict = { ok: false, reason: String(err).split('\n')[0].slice(0, 300) };
}

if (shotPath) {
  try { await page.screenshot({ path: shotPath }); } catch { /* best effort */ }
}
await browser.close();

const label = mobile ? 'mobile' : 'desktop';
if (verdict.ok) {
  console.log(`render OK (${label}) ${url} - ${(verdict.pixels.litFraction * 100).toFixed(1)}% lit`);
  process.exit(0);
}
console.error(`RENDER CHECK FAILED (${label}) ${url}: ${verdict.reason}`);
if (consoleErrors.length) {
  console.error('browser errors:');
  for (const e of [...new Set(consoleErrors)].slice(0, 8)) console.error('  ' + e);
}
process.exit(1);
