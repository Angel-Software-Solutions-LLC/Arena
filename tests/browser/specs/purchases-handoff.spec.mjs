import { expect, test } from '@playwright/test';

/**
 * The Shop, in a browser, after buying moved to the Angel account.
 *
 * The node suite (scripts/test-purchases-via-accounts.mjs) checks the pure
 * functions and the source. This checks the thing neither can: that the whole
 * chain — catalog fetch, state, render, href — actually produces a link to
 * accounts.angel-serv.com on a page a person is looking at, in a real engine,
 * at every viewport the Shop ships at.
 *
 * Both states are exercised, because the regression that would hurt is not
 * "the handoff does not work" — it is "the handoff shipped and broke the
 * Stripe flow that is still live".
 */

const HANDOFF = 'https://accounts.angel-serv.com/portal/items';

const PACK = {
  id: 'arena-set-003-ember-vanguard-pack',
  name: 'Ember Vanguard Set',
  description: 'Three presentation-only cosmetics.',
  category_id: 'sets',
  price_cents: 199,
  currency: 'USD',
  is_free: false,
  is_purchasable: true,
  is_active: true,
  items: [
    { id: 'skin-ember', name: 'Ember Chassis', slot: 'bot_skin', asset_key: 'standard', is_active: true },
  ],
};

const CATEGORY = { id: 'sets', name: 'Sets', is_active: true };

async function installShopRoutes(page, { handoff }) {
  await page.route('https://fonts.googleapis.com/**', (route) => route.fulfill({
    body: '', contentType: 'text/css; charset=utf-8',
  }));
  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith('/cosmetics/catalog')) {
      await route.fulfill({
        json: {
          categories: [CATEGORY],
          packs: [PACK],
          items: PACK.items,
          // Exactly what the server sends: its own checkout flag off, the
          // destination published as a separate fact.
          checkout_enabled: !handoff,
          subscription_offer: { enabled: !handoff, price_cents: 1999, currency: 'USD', interval: 'month' },
          ...(handoff ? { purchase_handoff_url: handoff } : {}),
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

test('the Shop sends a buyer to the Angel account once purchasing has moved', async ({ page }) => {
  await installShopRoutes(page, { handoff: HANDOFF });
  await page.goto('/shop/', { waitUntil: 'domcontentloaded' });

  const purchase = page.locator('[data-shop-purchase]');
  await expect(purchase).toHaveAttribute('href', HANDOFF);
  // `hidden` rather than `toBeVisible`: the Shop's detail column is laid out
  // off-screen at phone widths, so visibility would be testing the layout
  // instead of the thing at issue — whether the control is offered at all.
  await expect(purchase).toHaveJSProperty('hidden', false);
  // And it says so before the press, rather than surprising somebody with an
  // address bar that is no longer Arena's.
  await expect(purchase).toContainText('in your Angel account');
  await expect(page.locator('[data-shop-purchase-note]')).toContainText('Buying happens in your Angel account');

  // All Access moves with it, rather than reading as withdrawn.
  const subscribe = page.locator('[data-shop-subscription-action]');
  await expect(subscribe).toHaveAttribute('href', HANDOFF);
  await expect(subscribe).toHaveJSProperty('hidden', false);
});

test('and keeps the Dashboard checkout path while Arena still sells', async ({ page }) => {
  await installShopRoutes(page, { handoff: '' });
  await page.goto('/shop/', { waitUntil: 'domcontentloaded' });

  const purchase = page.locator('[data-shop-purchase]');
  await expect(purchase).toHaveJSProperty('hidden', false);
  await expect(purchase).toHaveAttribute('href', /dash_open=1.*dash_pack=arena-set-003-ember-vanguard-pack/);
  await expect(purchase).not.toContainText('in your Angel account');
  await expect(page.locator('[data-shop-purchase-note]')).toContainText('Checkout opens in your Dashboard');
});
