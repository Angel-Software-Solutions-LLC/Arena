import { expect, test } from '@playwright/test';

/**
 * One press, in a real browser, and the window that opens is Angel Accounts'.
 *
 * `scripts/test-one-click-sign-in.mjs` executes the decision logic and reads
 * the callers' source. Neither can see whether the press a person actually
 * makes reaches the Accounts flow, or whether an Arena screen is still sitting
 * in front of it. This is the half that can.
 *
 * The Accounts flow itself is stubbed at Arena's own `/dashboard/login`, which
 * is where the popup goes first — the point is which URL the press produces
 * and how many presses it took, not what an identity provider does next.
 */

const SESSION_SIGNED_OUT = {
  authenticated: false,
  login_enabled: true,
  oidc_login_enabled: true,
  email_login_enabled: false,
  login_url: '/dashboard/login',
  logout_url: '/dashboard/logout',
  account: { id: '', email: '', email_verified: false, name: '' },
};

const SESSION_NO_ACCOUNTS = { ...SESSION_SIGNED_OUT, login_enabled: false, oidc_login_enabled: false };

/** Records every URL a popup was opened on, and answers it with a stub page. */
async function installRoutes(page, { session }) {
  const opened = [];
  // On the context, not the page: a popup is its own Page, and page-scoped
  // routes do not follow it -- which is exactly the window under test here.
  const context = page.context();
  await context.route('https://fonts.googleapis.com/**', (route) => route.fulfill({
    body: '', contentType: 'text/css; charset=utf-8',
  }));
  await context.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname;
    const payload = path.endsWith('/account/session') ? session
      : path.endsWith('/content') ? { blocks: {} }
        : path.endsWith('/chat/config') ? { enabled: false }
          : path.endsWith('/service-status')
            ? { type: 'service_status', revision: 1, broadcast: null, maintenance: null }
            : path.endsWith('/version') ? { commit: 'browser-fixture', build_time: 'fixture' }
              : path.endsWith('/consent/accept') ? { ok: true }
                : path.endsWith('/cosmetics/catalog')
                  ? { categories: [], items: [], packs: [], checkout_enabled: false, subscription_offer: { enabled: false } }
                  : path.endsWith('/leaderboard') ? { leaderboard: [] }
                    : {};
    await route.fulfill({ json: payload });
  });
  // Registered last on purpose: Playwright consults the most recently added
  // route first, and the login route lives under the same /api/v1 prefix the
  // catch-all above claims.
  await context.route('**/dashboard/login*', async (route) => {
    opened.push(route.request().url());
    await route.fulfill({
      contentType: 'text/html; charset=utf-8',
      body: '<!doctype html><title>Accounts stub</title><p>accounts</p>',
    });
  });
  return opened;
}

/** The gate is law, not a step: accept it once so the press can be measured. */
async function acceptConsent(page) {
  const accept = page.locator('#arena-consent-gate-dialog .acg-accept');
  await expect(accept).toBeVisible();
  await accept.click();
}

test('the signed-out Dashboard offers one way in, and folds the API key away', async ({ page }) => {
  await installRoutes(page, { session: SESSION_SIGNED_OUT });
  await page.goto('/dashboard/', { waitUntil: 'domcontentloaded' });

  const signIn = page.locator('#accountSignInButton');
  await expect(signIn).toBeVisible();
  await expect(signIn).toHaveText('Sign in with Angel Accounts');

  // The chooser is gone: the key path is present, named, and closed.
  const details = page.locator('#botKeyDetails');
  await expect(details).toBeVisible();
  await expect(details).not.toHaveAttribute('open', /.*/);
  await expect(page.locator('#apiKeyInput')).toBeHidden();
  await details.locator('summary').click();
  await expect(page.locator('#apiKeyInput')).toBeVisible();

  // Getting an account is the fixed cross-product URL, filled in by
  // js/accounts.js rather than written into the markup.
  await expect(page.locator('#login a[data-accounts-register]'))
    .toHaveAttribute('href', 'https://accounts.angel-serv.com/register?product=arena');
});

test('pressing sign in on the Dashboard goes straight to Angel Accounts', async ({ page }) => {
  const opened = await installRoutes(page, { session: SESSION_SIGNED_OUT });
  await page.goto('/dashboard/', { waitUntil: 'domcontentloaded' });

  const before = page.url();
  const popupPromise = page.waitForEvent('popup');
  await page.locator('#accountSignInButton').click();
  await acceptConsent(page);
  const popup = await popupPromise;

  expect(opened.some((url) => url.includes('/dashboard/login') && url.includes('popup=1'))).toBe(true);
  await expect(popup).toHaveTitle('Accounts stub');
  // Nothing of Arena's happened in between: the page underneath never moved.
  expect(page.url()).toBe(before);
  await popup.close();
});

test('an Arena without Angel Accounts says so instead of opening nothing', async ({ page }) => {
  const opened = await installRoutes(page, { session: SESSION_NO_ACCOUNTS });
  await page.goto('/dashboard/', { waitUntil: 'domcontentloaded' });

  await expect(page.locator('#accountEmailSetup'))
    .toHaveText('Angel Accounts sign-in is not configured on this Arena yet.');
  await expect(page.locator('#accountSignInButton')).toBeHidden();
  // And the bot operator's way in is still reachable on exactly this Arena.
  await page.locator('#botKeyDetails summary').click();
  await expect(page.locator('#apiKeyInput')).toBeVisible();
  expect(opened).toEqual([]);
});

test('the Dashboard rail on the live Arena is itself the sign-in press', { tag: '@desktop-only' }, async ({ page }) => {
  const opened = await installRoutes(page, { session: SESSION_SIGNED_OUT });
  await page.goto('/', { waitUntil: 'domcontentloaded' });

  const rail = page.locator('.onboarding-rail[data-overlay-open="dashboard-overlay"]');
  await expect(rail).toBeVisible();
  // Wait for the session read that lets the press decide without a round trip.
  await expect.poll(async () => page.evaluate(() => document.querySelectorAll('[data-arena-signin]').length > 0))
    .toBe(true);

  const popupPromise = page.waitForEvent('popup');
  await rail.click();
  await acceptConsent(page);
  const popup = await popupPromise;

  expect(opened.some((url) => url.includes('/dashboard/login') && url.includes('popup=1'))).toBe(true);
  await popup.close();
  // And the drawer still opens behind it, whatever the window did.
  await expect(page.locator('#dashboard-overlay')).toHaveClass(/open/);

  // The rail is still a toggle: pressing it again closes what it opened,
  // rather than starting another sign-in on top of an open drawer.
  await rail.click();
  await expect(page.locator('#dashboard-overlay')).not.toHaveClass(/open/);
});
