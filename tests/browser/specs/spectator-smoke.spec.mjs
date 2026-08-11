import { expect, test } from '@playwright/test';
import { readFile, writeFile } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { arenaState, lobbyState, roundEnd } from '../fixtures/round-cycle.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const browserRoot = resolve(here, '..');
const babylonPath = resolve(browserRoot, 'node_modules', 'babylonjs', 'babylon.js');
const earcutPath = resolve(browserRoot, 'node_modules', 'earcut', 'dist', 'earcut.min.js');

async function installFixtureRoutes(page, diagnostics) {
  let spectatorSocket = null;

  await page.route('https://cdn.jsdelivr.net/npm/babylonjs@9.14.0/babylon.min.js', async (route) => {
    await route.fulfill({
      body: await readFile(babylonPath),
      contentType: 'text/javascript; charset=utf-8',
    });
  });
  await page.route('https://cdn.jsdelivr.net/npm/earcut@2.2.4/dist/earcut.min.js', async (route) => {
    await route.fulfill({
      body: await readFile(earcutPath),
      contentType: 'text/javascript; charset=utf-8',
    });
  });
  await page.route('https://fonts.googleapis.com/**', (route) => route.fulfill({
    body: '', contentType: 'text/css; charset=utf-8',
  }));

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
    } catch {
      // Chromium builds without WebGPU already take the same WebGL path.
    }
  });

  page.on('console', (message) => {
    if (message.type() === 'warning' || message.type() === 'error') {
      diagnostics.console.push({ type: message.type(), text: message.text() });
    }
  });
  page.on('pageerror', (error) => diagnostics.pageErrors.push(String(error)));
  page.on('requestfailed', (request) => diagnostics.requestFailures.push({
    url: request.url(),
    error: request.failure()?.errorText || 'unknown request failure',
  }));
  page.on('response', (response) => {
    if (response.status() >= 400) {
      diagnostics.httpErrors.push({ url: response.url(), status: response.status() });
    }
  });

  return {
    async waitForSocket() {
      await expect.poll(() => spectatorSocket !== null).toBe(true);
    },
    send(message) {
      if (!spectatorSocket) throw new Error('spectator fixture socket is not connected');
      spectatorSocket.send(JSON.stringify(message));
    },
  };
}

async function snapshot(page) {
  return page.evaluate(() => window.__ARENA_TEST__?.diagnostics() || null);
}

async function waitForRound(page, roundNumber) {
  await expect.poll(async () => (await snapshot(page))?.roundNumber).toBe(roundNumber);
  await expect.poll(async () => (await snapshot(page))?.ready).toBe(true);
}

async function projectBotLabel(page, botId) {
  return page.evaluate((id) => {
    const B = window.BABYLON;
    const scene = B?.EngineStore?.LastCreatedScene;
    const mesh = scene?.getMeshByName(`world-hud-name-${id}`);
    const canvas = document.getElementById('arena-canvas');
    if (!B || !scene || !mesh || !canvas) return null;
    const engine = scene.getEngine();
    const viewport = scene.activeCamera.viewport.toGlobal(
      engine.getRenderWidth(), engine.getRenderHeight(),
    );
    const point = B.Vector3.Project(
      mesh.getAbsolutePosition(),
      B.Matrix.Identity(),
      scene.getTransformMatrix(),
      viewport,
    );
    const rect = canvas.getBoundingClientRect();
    return {
      x: point.x * rect.width / engine.getRenderWidth(),
      y: point.y * rect.height / engine.getRenderHeight(),
      canvasWidth: rect.width,
      canvasHeight: rect.height,
    };
  }, botId);
}

test('spectator layout and round lifecycle stay bounded', async ({ page }, testInfo) => {
  const diagnostics = { console: [], pageErrors: [], requestFailures: [], httpErrors: [], lifecycle: [] };
  const fixture = await installFixtureRoutes(page, diagnostics);

  await page.goto('/?arena-test=1', { waitUntil: 'networkidle' });
  await fixture.waitForSocket();
  await expect.poll(async () => (await snapshot(page))?.ready).toBe(true);

  fixture.send(arenaState(7));
  await waitForRound(page, 7);
  await expect(page.locator('#ws-status')).toContainText('Live');

  // The render loop must actually be advancing frames, not merely registered.
  // Babylon re-queues the next frame only after the render callback returns and
  // has no try/catch on that path, so one throwing frame leaves the engine with
  // activeRenderLoops=1, _frameHandler=0 and frameId frozen: a black canvas while
  // the socket, HUD and minimap all keep updating, which is indistinguishable
  // from a healthy page in every assertion above. Poll frameId for movement.
  const frameIdNow = () => page.evaluate(() => {
    const engine = window.BABYLON?.EngineStore?.LastCreatedScene?.getEngine();
    return engine ? engine.frameId : -1;
  });
  const firstFrameId = await frameIdNow();
  expect(firstFrameId).toBeGreaterThanOrEqual(0);
  // Liveness, not speed: this asserts the loop is still advancing at all, so
  // the budget only has to exceed the worst plausible frame interval. The
  // original 5s was the one assertion in this file below the suite's 30s
  // expect timeout, and it flaked on CI's single-core SwiftShader runner,
  // where a full round-lifecycle run takes ~8 minutes against ~50s locally.
  // A dead loop still fails here; it just no longer fails a slow-but-healthy
  // one.
  await expect.poll(frameIdNow, { timeout: 30_000 }).toBeGreaterThan(firstFrameId);
  await expect(page.locator('body')).not.toContainText(/\d+ bots connected\s*\|\s*Round \d+/i);

  const overflow = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
  }));
  expect(overflow.scrollWidth).toBeLessThanOrEqual(overflow.clientWidth + 1);

  if (testInfo.project.name === 'desktop-1440') {
    const [header, live] = await Promise.all([
      page.locator('.site-header').boundingBox(),
      page.locator('#site-live-pill').boundingBox(),
    ]);
    expect(header).not.toBeNull();
    expect(live).not.toBeNull();
    expect(header.x + header.width - (live.x + live.width)).toBeLessThan(350);
  }

  await page.locator('#follow-bot').selectOption('winner');
  // The WebGL smoke job intentionally uses software rendering in CI. Wait
  // for the first fully animated bounty frame instead of sampling between
  // mesh creation and animate(), when the sparkle emitter is still idle.
  await expect.poll(async () => (await snapshot(page))?.bounty.visible).toBe(true);
  await expect.poll(async () => (await snapshot(page))?.bounty.emitRate).toBeGreaterThan(0);
  // Follow-camera movement is intentionally eased. On a phone the minimap's
  // left inset is large enough that the bounty effect can already be live
  // while the label is still crossing into the safe viewport. Synchronize on
  // the actual framing contract instead of that unrelated visual effect.
  await expect.poll(async () => {
    const [current, frame] = await Promise.all([
      snapshot(page),
      projectBotLabel(page, 'winner'),
    ]);
    const safe = current?.safeViewport;
    if (!safe || !frame) return false;
    return frame.x >= safe.left && frame.x <= frame.canvasWidth - safe.right &&
      frame.y >= safe.top && frame.y <= frame.canvasHeight - safe.bottom;
  }).toBe(true);
  const initial = await snapshot(page);
  const projected = await projectBotLabel(page, 'winner');
  expect(projected).not.toBeNull();
  expect(projected.x).toBeGreaterThanOrEqual(initial.safeViewport.left);
  expect(projected.x).toBeLessThanOrEqual(projected.canvasWidth - initial.safeViewport.right);
  expect(projected.y).toBeGreaterThanOrEqual(initial.safeViewport.top);
  expect(projected.y).toBeLessThanOrEqual(projected.canvasHeight - initial.safeViewport.bottom);
  expect(initial.bounty.visible).toBe(true);
  expect(initial.bounty.emitRate).toBeGreaterThan(0);
  diagnostics.lifecycle.push({ phase: 'initial', snapshot: initial });
  await page.screenshot({ path: testInfo.outputPath('initial.png') });

  fixture.send(roundEnd(7));
  await expect.poll(async () => (await snapshot(page))?.roundTransitionActive).toBe(true);
  const winnerAtRoundEnd = (await snapshot(page)).bots.find((entry) => entry.id === 'winner');
  await expect.poll(async () => (await snapshot(page))?.bounty.visible).toBe(false);
  await expect.poll(async () => (await snapshot(page))?.bounty.emitRate).toBe(0);

  fixture.send(arenaState(7, { tickOffset: 1, winnerPosition: [1700, 1700] }));
  await page.waitForTimeout(350);
  const stale = await snapshot(page);
  const staleWinner = stale.bots.find((entry) => entry.id === 'winner');
  expect(staleWinner.x).toBeCloseTo(winnerAtRoundEnd.x, 3);
  expect(staleWinner.z).toBeCloseTo(winnerAtRoundEnd.z, 3);
  diagnostics.lifecycle.push({ phase: 'round-end', snapshot: stale });
  await page.screenshot({ path: testInfo.outputPath('round-end.png') });

  fixture.send(lobbyState(8));
  fixture.send(arenaState(8, { bountyTarget: null }));
  await waitForRound(page, 8);
  await expect.poll(async () => (await snapshot(page))?.roundTransitionActive).toBe(false);
  await expect.poll(async () => (await snapshot(page))?.intermissionActive).toBe(false);
  const warmBaseline = await snapshot(page);
  diagnostics.lifecycle.push({ phase: 'warm-baseline', snapshot: warmBaseline });

  for (let round = 8; round < 11; round += 1) {
    fixture.send(roundEnd(round));
    await expect.poll(async () => (await snapshot(page))?.roundTransitionActive).toBe(true);
    fixture.send(lobbyState(round + 1));
    fixture.send(arenaState(round + 1, { bountyTarget: null }));
    await waitForRound(page, round + 1);
    await expect.poll(async () => (await snapshot(page))?.intermissionActive).toBe(false);
    const settled = await snapshot(page);
    diagnostics.lifecycle.push({ phase: `settled-${round + 1}`, snapshot: settled });
    for (const resource of ['meshes', 'materials', 'textures', 'particleSystems', 'transformNodes', 'activeParticles']) {
      // Ambient emitters keep advancing while the assertion samples the
      // scene, so active particle instances can differ by a handful even
      // when the owning particle-system count is identical. Structural
      // resources stay exact; active particles get a narrow 5% + 8 margin.
      const baseline = warmBaseline.resources[resource];
      const limit = resource === 'activeParticles' ? Math.ceil(baseline * 1.05) + 8 : baseline;
      expect(settled.resources[resource], `${resource} leaked after round ${round + 1}`)
        .toBeLessThanOrEqual(limit);
    }
  }

  const resourceTiming = await page.evaluate(() => performance.getEntriesByType('resource').map((entry) => ({
    name: entry.name,
    duration: entry.duration,
    transferSize: entry.transferSize,
    encodedBodySize: entry.encodedBodySize,
    decodedBodySize: entry.decodedBodySize,
  })));
  diagnostics.resourceTiming = resourceTiming;
  diagnostics.webgl = await page.evaluate(() => {
    const canvas = document.getElementById('arena-canvas');
    const gl = canvas?.getContext('webgl2') || canvas?.getContext('webgl');
    const extension = gl?.getExtension('WEBGL_debug_renderer_info');
    return {
      renderer: extension ? gl.getParameter(extension.UNMASKED_RENDERER_WEBGL) : 'unavailable',
      vendor: extension ? gl.getParameter(extension.UNMASKED_VENDOR_WEBGL) : 'unavailable',
    };
  });
  await page.screenshot({ path: testInfo.outputPath('settled.png') });

  const targetedWarnings = diagnostics.console.filter(({ text }) =>
    /babylon|highlightlayer|stencil|content security policy|\bcsp\b|webgl warning/i.test(text));
  expect(diagnostics.pageErrors).toEqual([]);
  expect(diagnostics.requestFailures).toEqual([]);
  expect(diagnostics.httpErrors).toEqual([]);
  expect(diagnostics.console.filter((entry) => entry.type === 'error')).toEqual([]);
  expect(targetedWarnings).toEqual([]);

  await writeFile(testInfo.outputPath('diagnostics.json'), `${JSON.stringify(diagnostics, null, 2)}\n`);
  await testInfo.attach('browser-diagnostics', {
    body: Buffer.from(JSON.stringify(diagnostics, null, 2)),
    contentType: 'application/json',
  });
});
