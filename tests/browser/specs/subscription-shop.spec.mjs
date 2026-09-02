import { expect, test } from '@playwright/test';

/**
 * The Shop, in a browser, under the subscription model.
 *
 * Arena sells nothing itself: one Arena subscription, bought in the Angel
 * account, unlocks every paid cosmetic. The node suites check the pure
 * functions and the source. This checks the thing neither can: that the whole
 * chain — catalog fetch, state, render, href — actually produces a link to the
 * Angel account on a page a person is looking at, in a real engine, at every
 * viewport the Shop ships at.
 *
 * Both states are exercised, because the address is a deployment setting: a
 * Shop that has not been told where the subscription is sold must lead to the
 * Dashboard, which explains what is missing, rather than to a dead control.
 */

const SUBSCRIBE = 'https://accounts.angel-serv.com/portal/apps';

const PAID_PACK = {
  id: 'arena-set-003-ember-vanguard-pack',
  name: 'Ember Vanguard Set',
  description: 'Three presentation-only cosmetics.',
  category_id: 'sets',
  is_free: false,
  is_active: true,
  items: [
    { id: 'skin-ember', name: 'Ember Chassis', slot: 'bot_skin', asset_key: 'standard', is_active: true },
  ],
};

const FREE_PACK = {
  id: 'arena-set-000-standard-issue',
  name: 'Standard Issue',
  description: 'The chassis every bot starts in.',
  category_id: 'sets',
  is_free: true,
  is_active: true,
  items: [
    { id: 'skin-standard', name: 'Standard Chassis', slot: 'bot_skin', asset_key: 'standard', is_active: true },
  ],
};

const CATEGORY = { id: 'sets', name: 'Sets', is_active: true };

async function installShopRoutes(page, { subscribeURL }) {
  await page.route('https://fonts.googleapis.com/**', (route) => route.fulfill({
    body: '', contentType: 'text/css; charset=utf-8',
  }));
  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith('/cosmetics/catalog')) {
      await route.fulfill({
        json: {
          categories: [CATEGORY],
          packs: [PAID_PACK, FREE_PACK],
          items: [...PAID_PACK.items, ...FREE_PACK.items],
          // Exactly what the server sends: the one commerce fact the catalog
          // publishes, with the address only when the deployment has one.
          subscription: {
            product: 'arena',
            includes_all_cosmetics: true,
            ...(subscribeURL ? { url: subscribeURL } : {}),
          },
        },
      });
      return;
    }
    const payload = path.endsWith('/content') ? { blocks: {} }
      : path.endsWith('/service-status') ? { type: 'service_status', revision: 1, broadcast: null, maintenance: null }
        : path.endsWith('/chat/config') ? { enabled: false }
          : path.endsWith('/account/session') ? { authenticated: false }
            : path.endsWith('/version') ? { commit: 'browser-fixture', build_time: 'fixture' }
              : {};
    await route.fulfill({ json: payload });
  });
  await page.routeWebSocket('**/ws/**', (socket) => socket.onMessage(() => {}));
}

test('the Shop sends a person to the Angel account for the one subscription', async ({ page }) => {
  await installShopRoutes(page, { subscribeURL: SUBSCRIBE });
  await page.goto('/shop/', { waitUntil: 'domcontentloaded' });

  // The banner, where people look for a price.
  const subscribe = page.locator('[data-shop-subscription-action]');
  await expect(subscribe).toHaveAttribute('href', SUBSCRIBE);
  // `hidden` rather than `toBeVisible`: the Shop's detail column is laid out
  // off-screen at phone widths, so visibility would be testing the layout
  // instead of the thing at issue — whether the control is offered at all.
  await expect(subscribe).toHaveJSProperty('hidden', false);
  await expect(subscribe).toContainText('in your Angel account');
  await expect(page.locator('[data-shop-subscription]')).toHaveAttribute('data-state', 'available');

  // A paid pack, selected first, says what unlocks it and leads there too.
  const access = page.locator('[data-shop-access]');
  await expect(access).toHaveAttribute('data-shop-access-pack', PAID_PACK.id);
  await expect(access).toHaveAttribute('href', SUBSCRIBE);
  await expect(access).toHaveJSProperty('hidden', false);
  await expect(access).toContainText('Included with an Arena subscription');
  await expect(page.locator('[data-shop-access-note]')).toContainText('Subscribe in your Angel account');

  // Nothing is bought here: no per-item purchase control exists any more.
  await expect(page.locator('[data-shop-purchase]')).toHaveCount(0);

  // A free pack goes straight to the Dashboard to be equipped.
  await page.locator('[data-shop-pack-list] [data-shop-pack-id="' + FREE_PACK.id + '"]').first().click();
  await expect(access).toHaveAttribute('data-shop-access-pack', FREE_PACK.id);
  await expect(access).toHaveAttribute('href', /dash_open=1.*dash_tab=cosmetics/);
  await expect(access).toContainText('Equip in Dashboard');
});

test('and leads to the Dashboard when no subscription address is published', async ({ page }) => {
  await installShopRoutes(page, { subscribeURL: '' });
  await page.goto('/shop/', { waitUntil: 'domcontentloaded' });

  const subscribe = page.locator('[data-shop-subscription-action]');
  await expect(subscribe).toHaveJSProperty('hidden', false);
  await expect(subscribe).toHaveAttribute('href', /dash_open=1.*dash_tab=cosmetics/);
  await expect(subscribe).toContainText('Open your Dashboard');
  await expect(page.locator('[data-shop-subscription]')).toHaveAttribute('data-state', 'unlinked');

  const access = page.locator('[data-shop-access]');
  await expect(access).toHaveAttribute('href', /dash_open=1.*dash_tab=cosmetics/);
  await expect(access).toContainText('Open Dashboard');
});
