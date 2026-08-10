// Regression gate for the blank-world class of failures: the page can look
// perfectly healthy to every DOM/scene-state assertion (socket live, HUD
// updating, meshes enabled, materials isReady) while the canvas paints
// nothing — exactly what shipped when the vendored Babylon bundle dropped
// required side-effect modules. This spec is the only place that asserts
// actual rendered pixels, read inside onAfterRender while the framebuffer is
// still valid. It also replays the live conditions the round-cycle fixture
// never exercises: a non-integer dynamic arena size (2111.111... for 14
// bots) forcing _rebuildForArenaSize, an hourglass mask, and keyframe
// obstacle payloads.
import { expect, test } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const browserRoot = resolve(here, '..');
const babylonPath = resolve(browserRoot, 'node_modules', 'babylonjs', 'babylon.js');
const earcutPath = resolve(browserRoot, 'node_modules', 'earcut', 'dist', 'earcut.min.js');

const SIZE = 2111.1111111111113;

const bot = (i) => ({
  bot_id: `bot-${i}`,
  name: `Bot${i}`,
  position: [400 + (i % 5) * 300, 500 + Math.floor(i / 5) * 350],
  is_alive: i < 3,
  hp: i < 3 ? 60 : 0,
  max_hp: 100,
  weapon: ['sword', 'bow', 'spear', 'staff', 'daggers'][i % 5],
  avatar_color: '#46d7ff',
  rotation: 0,
  last_action: 'wait',
  action_tick: 0,
  cooldown_remaining: 0,
  round_kills: 0,
  kill_streak: 0,
  shield_absorb: 0,
  mine_count: 0,
  grapple_charges: 0,
  is_bounty_target: false,
});

const maskRects = [
  { x: 0, y: 700, width: 700, height: 700 },
  { x: SIZE - 700, y: 700, width: 700, height: 700 },
];
const obstacles = [
  { x: 900, y: 400, width: 90, height: 90 },
  { x: 1200, y: 1500, width: 120, height: 60 },
  { x: 500, y: 1700, width: 60, height: 140 },
  ...maskRects,
];

const liveState = (tick, keyframe) => ({
  type: 'arena_state',
  tick,
  round_tick: tick,
  round_number: 1,
  round_time_remaining: 210,
  arena_size: [SIZE, SIZE],
  map_shape: 'hourglass',
  game_mode: 'ffa',
  bots: Array.from({ length: 14 }, (_, i) => bot(i)),
  waiting_bots: [],
  obstacles: keyframe ? obstacles : undefined,
  mask_rects: keyframe ? maskRects : undefined,
  pickups: [],
  events: [],
  kill_feed: [],
  safe_zone: {
    center: [SIZE / 2, SIZE / 2],
    radius: 1187,
    target_center: [SIZE / 2, SIZE / 2],
    target_radius: 1009,
  },
  bounty_target: null,
  sudden_death: false,
});

test('the world still paints after a non-integer hourglass rebuild', async ({ page }, testInfo) => {
  test.setTimeout(180_000);
  let spectatorSocket = null;

  await page.route('https://cdn.jsdelivr.net/npm/babylonjs@9.14.0/babylon.min.js', async (route) => {
    await route.fulfill({ body: await readFile(babylonPath), contentType: 'text/javascript; charset=utf-8' });
  });
  await page.route('https://cdn.jsdelivr.net/npm/earcut@2.2.4/dist/earcut.min.js', async (route) => {
    await route.fulfill({ body: await readFile(earcutPath), contentType: 'text/javascript; charset=utf-8' });
  });
  await page.route('https://fonts.googleapis.com/**', (route) => route.fulfill({ body: '', contentType: 'text/css; charset=utf-8' }));
  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    const payload = path.endsWith('/content') ? { blocks: {} }
      : path.endsWith('/service-status') ? { type: 'service_status', revision: 1, broadcast: null, maintenance: null }
        : path.endsWith('/chat/config') ? { enabled: false }
          : path.endsWith('/account/session') ? { authenticated: false }
            : path.endsWith('/leaderboard') ? { entries: [] }
              : path.endsWith('/bounties') ? { entries: [] }
                : path.endsWith('/weapon-stats') ? { weapons: [] }
                  : path.endsWith('/version') ? { commit: 'browser-fixture', build_time: 'fixture' }
                    : {};
    await route.fulfill({ json: payload });
  });
  await page.routeWebSocket('**/ws/spectator', (socket) => {
    spectatorSocket = socket;
    socket.onMessage(() => {});
  });
  await page.routeWebSocket('**/ws/chat', (socket) => socket.onMessage(() => {}));
  await page.addInitScript(() => {
    try {
      Object.defineProperty(Navigator.prototype, 'gpu', { configurable: true, get: () => undefined });
    } catch { /* same WebGL path */ }
  });

  await page.goto('/?arena-test=1', { waitUntil: 'networkidle' });
  await expect.poll(() => spectatorSocket !== null).toBe(true);
  await expect.poll(() => page.evaluate(() => window.__ARENA_TEST__?.diagnostics()?.ready || false)).toBe(true);

  const send = (msg) => spectatorSocket.send(JSON.stringify(msg));

  // The first keyframe carries the non-default size and triggers the rebuild.
  send(liveState(880, true));
  await page.waitForTimeout(1500);
  for (let t = 890; t <= 940; t += 10) {
    send(liveState(t, false));
    await page.waitForTimeout(100);
  }
  send(liveState(950, true));

  // The rebuilt engine must be alive and advancing frames.
  const frameIdNow = () => page.evaluate(() => window.BABYLON?.EngineStore?.LastCreatedScene?.getEngine()?.frameId ?? -1);
  await expect.poll(frameIdNow, { timeout: 60_000 }).toBeGreaterThan(2);
  const seen = await frameIdNow();
  await expect.poll(frameIdNow, { timeout: 30_000 }).toBeGreaterThan(seen);

  // Ground truth: read the framebuffer inside onAfterRender, while it is
  // still valid for readback. A healthy frame paints the floor and skybox
  // across most of the canvas; the blank-world regression reads 0.
  const pixels = await page.evaluate(() => new Promise((done) => {
    const scene = window.BABYLON.EngineStore.LastCreatedScene;
    scene.onAfterRenderObservable.addOnce(() => {
      const gl = scene.getEngine()._gl;
      const w = gl.drawingBufferWidth, h = gl.drawingBufferHeight;
      const buf = new Uint8Array(w * h * 4);
      gl.readPixels(0, 0, w, h, gl.RGBA, gl.UNSIGNED_BYTE, buf);
      let lit = 0;
      for (let i = 0; i < buf.length; i += 4) {
        if (buf[i] + buf[i + 1] + buf[i + 2] > 30) lit += 1;
      }
      done({ litFraction: lit / (w * h), width: w, height: h });
    });
    setTimeout(() => done({ litFraction: -1, timedOut: true }), 30_000);
  }));

  await page.screenshot({ path: testInfo.outputPath('live-conditions.png') });
  expect(pixels.litFraction, `framebuffer readback ${JSON.stringify(pixels)}`).toBeGreaterThan(0.05);
});
