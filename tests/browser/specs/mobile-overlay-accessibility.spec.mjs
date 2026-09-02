import { expect, test } from '@playwright/test';

async function installMobileRoutes(page) {
  let spectatorSocket = null;
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
  await page.routeWebSocket('**/ws/spectator', (socket) => {
    spectatorSocket = socket;
    socket.onMessage(() => {});
  });
  await page.routeWebSocket('**/ws/chat', (socket) => socket.onMessage(() => {}));

  await page.addInitScript(() => {
    // Register before application modules to prove the mobile Escape handler
    // still yields when a priority modal dismisses itself earlier in dispatch.
    document.addEventListener('keydown', (event) => {
      if (event.key !== 'Escape') return;
      const serviceDialog = document.querySelector('.service-status-popup:not([hidden])');
      serviceDialog?.querySelector('button')?.click();
    });
    try {
      Object.defineProperty(Navigator.prototype, 'gpu', { configurable: true, get: () => undefined });
    } catch {
      // Chromium builds without WebGPU already take the same WebGL path.
    }
  });

  return {
    hasSpectatorSocket: () => spectatorSocket !== null,
    sendSpectatorMessage: (message) => spectatorSocket?.send(JSON.stringify(message)),
  };
}

const backgroundInertState = (page) => page.locator(
  'body > :not(.mobile-overlay):not(dialog):not([role="alertdialog"][aria-modal="true"])',
).evaluateAll(
  (elements) => elements.map((element) => ({ id: element.id, inert: element.inert })),
);

test('mobile overlays isolate the page and preserve keyboard focus through their lifecycle', async ({ page }) => {
  const routes = await installMobileRoutes(page);
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

  await page.evaluate(() => {
    const ordinary = document.createElement('button');
    ordinary.id = 'late-background-control';
    ordinary.textContent = 'Late background control';
    document.body.appendChild(ordinary);

    const alreadyInert = document.createElement('button');
    alreadyInert.id = 'late-inert-background-control';
    alreadyInert.textContent = 'Already inert background control';
    alreadyInert.inert = true;
    document.body.appendChild(alreadyInert);
  });
  await expect(page.locator('#late-background-control')).toHaveJSProperty('inert', true);
  await expect(page.locator('#late-inert-background-control')).toHaveJSProperty('inert', true);

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

  const chatInput = page.locator('#chat-input');
  await chatInput.fill('draft stays in Chat');
  await chatInput.focus();
  await page.keyboard.press('Escape');
  await expect(chatInput).toHaveValue('');
  await expect(chat).toHaveClass(/\bopen\b/);

  await page.evaluate(() => {
    const dialog = document.createElement('dialog');
    dialog.id = 'late-native-dialog';
    const close = document.createElement('button');
    close.type = 'button';
    close.textContent = 'Close native dialog';
    dialog.appendChild(close);
    document.body.appendChild(dialog);
  });
  const nativeDialog = page.locator('#late-native-dialog');
  await expect(nativeDialog).toHaveJSProperty('inert', false);
  await nativeDialog.evaluate((dialog) => dialog.showModal());
  await expect(nativeDialog).toHaveJSProperty('inert', false);
  await nativeDialog.evaluate((dialog) => {
    dialog.close();
    dialog.remove();
  });

  await expect.poll(routes.hasSpectatorSocket).toBe(true);
  routes.sendSpectatorMessage({
    type: 'service_status',
    revision: 2,
    broadcast: null,
    maintenance: {
      id: 'mobile-modal-review',
      message: 'Arena maintenance fixture',
      phase: 'scheduled',
      severity: 'warning',
    },
  });
  const serviceDialog = page.locator('.service-status-popup');
  const acknowledgeService = serviceDialog.getByRole('button', { name: 'OK' });
  await expect(serviceDialog).toBeVisible();
  await expect(serviceDialog).toHaveAttribute('role', 'alertdialog');
  await expect(serviceDialog).toHaveJSProperty('inert', false);
  await expect(acknowledgeService).toBeFocused();

  await chatClose.focus();
  await page.keyboard.press('Tab');
  await expect(acknowledgeService).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(serviceDialog).toBeHidden();
  await expect(serviceDialog).toHaveJSProperty('inert', false);
  await expect(chat).toHaveClass(/\bopen\b/);
  await page.keyboard.press('Tab');
  await expect(chatClose).toBeFocused();

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
  const restoredBackground = await backgroundInertState(page);
  initialBackground.forEach((initial) => {
    expect(restoredBackground.find(({ id }) => id === initial.id)).toEqual(initial);
  });
  await expect(page.locator('#late-background-control')).toHaveJSProperty('inert', false);
  await expect(page.locator('#late-inert-background-control')).toHaveJSProperty('inert', true);
});
