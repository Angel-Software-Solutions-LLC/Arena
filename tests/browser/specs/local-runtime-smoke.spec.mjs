import { expect, test } from '@playwright/test';

async function installLocalRuntimeRoutes(page, diagnostics) {
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

  await page.routeWebSocket('**/ws/spectator', (socket) => socket.onMessage(() => {}));
  await page.routeWebSocket('**/ws/chat', (socket) => socket.onMessage(() => {}));

  await page.addInitScript(() => {
    try {
      Object.defineProperty(Navigator.prototype, 'gpu', { configurable: true, get: () => undefined });
    } catch {
      // Chromium builds without WebGPU already take the same WebGL path.
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
}

test('local modular runtime boots mobile spectator and Shop', { tag: '@phone-only' }, async ({ page }) => {
  const diagnostics = { pageErrors: [], requestFailures: [], httpErrors: [] };
  const requested = [];
  page.on('request', (request) => requested.push(request.url()));
  await installLocalRuntimeRoutes(page, diagnostics);

  for (const route of ['/m/', '/shop/']) {
    await page.goto(route, { waitUntil: 'networkidle' });
    await expect.poll(() => page.evaluate(() => ({
      engine: typeof window.BABYLON?.Engine,
      scene: typeof window.BABYLON?.Scene,
      webgpu: typeof window.BABYLON?.WebGPUEngine,
      earcut: typeof window.earcut,
    }))).toEqual({ engine: 'function', scene: 'function', webgpu: 'function', earcut: 'function' });
  }

  expect(requested.filter((url) =>
    /cdn\.jsdelivr\.net\/npm\/(?:babylonjs|earcut)|cdn\.babylonjs\.com/i.test(url))).toEqual([]);
  expect(diagnostics.pageErrors).toEqual([]);
  expect(diagnostics.requestFailures).toEqual([]);
  expect(diagnostics.httpErrors).toEqual([]);
});
