import { expect, test } from '@playwright/test';

async function installMobileRoutes(page) {
  await page.route('https://fonts.googleapis.com/**', (route) => route.fulfill({
    body: '', contentType: 'text/css; charset=utf-8',
  }));

  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    const payload = path.endsWith('/content') ? { blocks: {} }
      : path.endsWith('/service-status') ? { type: 'service_status', revision: 1, broadcast: null, maintenance: null }
        : path.endsWith('/chat/config') ? { enabled: true }
          : path.endsWith('/account/session') ? { authenticated: false }
            : path.endsWith('/leaderboard') ? { entries: [] }
              : path.endsWith('/version') ? { commit: 'browser-fixture', build_time: 'fixture' }
                : {};
    await route.fulfill({ json: payload });
  });

  await page.route('**/dashboard/?view=private*', (route) => route.fulfill({
    body: '<!doctype html><html><body>Dashboard fixture</body></html>',
    contentType: 'text/html; charset=utf-8',
  }));
  await page.routeWebSocket('**/ws/spectator', (socket) => socket.onMessage(() => {}));
  await page.routeWebSocket('**/ws/chat', (socket) => socket.onMessage(() => {}));

  await page.addInitScript(() => {
    try {
      Object.defineProperty(Navigator.prototype, 'gpu', { configurable: true, get: () => undefined });
    } catch {
      // Chromium builds without WebGPU already take the same WebGL path.
    }
  });
}

const backgroundInertState = (page) => page.locator('body > :not(.mobile-overlay)').evaluateAll(
  (elements) => elements.map((element) => ({ id: element.id, inert: element.inert })),
);

test('mobile overlays isolate the page and preserve keyboard focus through their lifecycle', async ({ page }) => {
  await installMobileRoutes(page);
  await page.goto('/m/', { waitUntil: 'domcontentloaded' });
  await expect.poll(() => page.evaluate(() => typeof window.ArenaOpenDashboard)).toBe('function');

  const chat = page.locator('#chat-overlay');
  const dashboard = page.locator('#dashboard-overlay');
  const shop = page.locator('#shop-overlay');
  const chatFab = page.locator('#fab-chat');
  const dashboardFab = page.locator('#fab-dashboard');

  await expect(chat).toHaveJSProperty('inert', true);
  await expect(dashboard).toHaveJSProperty('inert', true);
  await expect(shop).toHaveJSProperty('inert', true);

  await page.locator('#stage').evaluate((element) => { element.inert = true; });
  const initialBackground = await backgroundInertState(page);

  await chatFab.click();
  await expect(chat).toHaveJSProperty('inert', false);
  await expect(chatFab).toHaveAttribute('aria-expanded', 'true');
  await expect(chat.locator('.mobile-overlay-close')).toBeFocused();
  expect((await backgroundInertState(page)).every(({ inert }) => inert)).toBe(true);

  await page.locator('#chat-input, #chat-send').evaluateAll((elements) => {
    elements.forEach((element) => { element.disabled = false; });
  });
  const chatClose = chat.locator('.mobile-overlay-close');
  const chatSend = page.locator('#chat-send');
  await chatSend.focus();
  await page.keyboard.press('Tab');
  await expect(chatClose).toBeFocused();
  await page.keyboard.press('Shift+Tab');
  await expect(chatSend).toBeFocused();

  await page.evaluate(() => window.ArenaOpenDashboard());
  await expect(chat).toHaveClass(/^(?!.*\bopen\b)/);
  await expect(chat).toHaveJSProperty('inert', true);
  await expect(dashboard).toHaveClass(/\bopen\b/);
  await expect(dashboard).toHaveJSProperty('inert', false);
  expect((await backgroundInertState(page)).every(({ inert }) => inert)).toBe(true);
  await expect(dashboard.locator('.mobile-overlay-close')).toBeFocused();

  await page.keyboard.press('Escape');
  await expect(dashboard).toHaveClass(/^(?!.*\bopen\b)/);
  await expect(dashboard).toHaveJSProperty('inert', true);
  await expect(dashboardFab).toBeFocused();
  expect(await backgroundInertState(page)).toEqual(initialBackground);
});
