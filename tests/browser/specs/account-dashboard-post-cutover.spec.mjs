import { expect, test } from '@playwright/test';

// This is the post-cutover identity shape, intentionally without an email.
// Account eligibility must be decided by the shared policy rather than the
// Dashboard's former email-field gate.
const ACCOUNT = {
  id: 'acct-post-cutover',
  email: '',
  email_verified: true,
  display_name: 'Arena Pilot',
};

async function installDashboardRoutes(page, requested) {
  await page.route('https://fonts.googleapis.com/**', (route) => route.fulfill({
    body: '', contentType: 'text/css; charset=utf-8',
  }));
  await page.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    requested.add(`${path}${url.search}`);
    const payloads = {
      '/api/v1/account/session': { authenticated: true, csrf_token: 'fixture-csrf', account: ACCOUNT },
      '/api/v1/account/cosmetics': { account: ACCOUNT, bots: [], licenses: [], subscription: null },
      '/api/v1/cosmetics/catalog': {
        categories: [], items: [], packs: [], checkout_enabled: false, subscription_offer: { enabled: false },
      },
      '/api/v1/account/cosmetics/orders': { orders: [] },
      '/api/v1/account/keys': { keys: [] },
      '/api/v1/profile/acct-post-cutover': {
        account_id: ACCOUNT.id,
        display_name: ACCOUNT.display_name,
        chat_handle: 'Arena Pilot#postcutover',
        bio: '',
        avatar_color: '#5edfff',
        shows_bots: false,
        bots: [],
      },
      '/api/v1/content': { blocks: {} },
      '/api/v1/service-status': {
        type: 'service_status', revision: 1, broadcast: null, maintenance: null,
      },
      '/api/v1/chat/config': { enabled: false },
      '/api/v1/version': { commit: 'browser-fixture', build_time: 'fixture' },
    };
    if (!(path in payloads)) throw new Error(`unexpected Dashboard API request: ${path}`);
    const payload = payloads[path];
    await route.fulfill({ json: payload });
  });
}

test('a verified Angel account without an email opens Dashboard account controls', async ({ page }) => {
  const requested = new Set();
  await installDashboardRoutes(page, requested);

  await page.goto('/dashboard/', { waitUntil: 'domcontentloaded' });

  await expect(page.locator('[data-tab="cosmetics"]')).toBeVisible();
  await expect(page.locator('[data-tab="profile"]')).toBeVisible();
  await expect(page.locator('#accountLogoutBtn')).toBeVisible();
  await expect(page.locator('#accountToolbarIdentity')).toContainText('Arena Pilot');
  await expect(page.locator('#accountCosmeticsPanel')).toContainText('Arena Pilot');
  await expect(page.locator('#botSwitcher')).toContainText('Arena Pilot');
  await expect.poll(() => requested.has('/api/v1/account/cosmetics')).toBe(true);

  await page.locator('[data-tab="profile"]').click();
  await expect(page.locator('#accountProfilePanel')).toContainText('Arena Pilot');
  await expect.poll(() => requested.has('/api/v1/profile/acct-post-cutover')).toBe(true);
});
