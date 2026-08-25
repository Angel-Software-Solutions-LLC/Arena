import { expect, test } from '@playwright/test';

/**
 * The shared footer, in a real browser, at every viewport this harness runs.
 *
 * `scripts/test-site-footer.mjs` holds the source to the contract — that the
 * list is fetched, that a fallback is baked, that hover lives in CSS. It
 * cannot see a pixel. This is the half that can: whether the panel opens,
 * where it lands, and whether it is on the screen at all on a phone.
 *
 * The corpus is stubbed rather than reached. The fetch is cross-origin to a
 * host this runner cannot see, and the point is to exercise the real code path
 * with a real response — including the CORS header the live endpoint sends —
 * not to depend on the internet.
 */

const CORPUS = 'https://accounts.angel-serv.com/api/legal/documents';

/** Deliberately unlike the baked list, so "the live one won" is provable. */
const STUB = [
  { slug: 'terms', title: 'Terms of Service', url: '/legal/terms' },
  { slug: 'privacy', title: 'Privacy Policy', url: '/legal/privacy' },
  { slug: 'brand-new', title: 'Published After Arena Shipped', url: '/legal/brand-new' },
];

const stubCorpus = (page) => page.route(CORPUS, (route) => route.fulfill({
  status: 200,
  contentType: 'application/json',
  headers: { 'access-control-allow-origin': '*' },
  body: JSON.stringify({ documents: STUB }),
}));

const failCorpus = (page) => page.route(CORPUS, (route) => route.abort('failed'));

/** The contract's breakpoint, both clauses. */
const isPhone = (page) => {
  const size = page.viewportSize();
  return size.width <= 767 || size.height <= 500;
};

test.describe('the shared footer', () => {
  test('states the company, the year and where status lives', async ({ page }) => {
    await stubCorpus(page);
    await page.goto('/shop/');
    const row = page.locator('.site-footer__row');
    await expect(row).toBeVisible();

    await expect(page.locator('.site-footer__copyright'))
      .toHaveText(new RegExp(`© ${new Date().getFullYear()} Angel Software Solutions LLC`));
    await expect(page.locator('.site-footer__status'))
      .toHaveAttribute('href', 'https://accounts.angel-serv.com/status');

    // Arena sets no optional cookies, so it must not offer to manage them.
    await expect(page.locator('.site-footer').getByText('Cookie preferences')).toHaveCount(0);
  });

  test('draws the corpus fetched at runtime, linked canonically', async ({ page }) => {
    await stubCorpus(page);
    await page.goto('/shop/');
    await expect(page.locator('.site-footer__item')).toHaveCount(STUB.length);
    await expect(page.locator('.site-footer__item', { hasText: 'Published After Arena Shipped' })).toHaveCount(1);
    await expect(page.locator('.site-footer__item').first())
      .toHaveAttribute('href', 'https://accounts.angel-serv.com/legal/terms');
  });

  test('falls back to all six documents when Accounts cannot be reached', async ({ page }) => {
    await failCorpus(page);
    await page.goto('/shop/');
    await expect(page.locator('.site-footer__item')).toHaveCount(6);
    // The compliance property that cannot depend on another host answering.
    await expect(page.locator('.site-footer__item', { hasText: 'Cookie Policy' })).toHaveCount(1);
    await expect(page.locator('.site-footer__item').first())
      .toHaveAttribute('href', /^https:\/\/accounts\.angel-serv\.com\/legal\//);
  });

  test('opens the way the viewport calls for, and stays on the screen', async ({ page }) => {
    await stubCorpus(page);
    await page.goto('/shop/');
    await page.locator('.site-footer__row').scrollIntoViewIfNeeded();
    const panel = page.locator('.site-footer__panel');
    await expect(panel).toBeHidden();

    if (isPhone(page)) {
      // No hover to open with; the tap handler is the phone's way in.
      await page.locator('.site-footer__trigger').click();
      await expect(panel).toBeVisible();

      // The bug the centred sheet exists to avoid: an anchored panel measured
      // leftwards from a trigger near the left edge hangs off a 390px screen.
      const sheet = await page.locator('.site-footer__sheet').boundingBox();
      const width = page.viewportSize().width;
      expect(sheet.x).toBeGreaterThanOrEqual(0);
      expect(sheet.x + sheet.width).toBeLessThanOrEqual(width + 1);

      for (const row of await page.locator('.site-footer__item').all()) {
        const rect = await row.boundingBox();
        expect(rect.height).toBeGreaterThanOrEqual(44);
      }

      await page.locator('.site-footer__close').click();
      await expect(panel).toBeHidden();
      return;
    }

    // On a pointer the menu opens on hover, and CSS is what does it.
    await page.locator('.site-footer__menu').hover();
    await expect(panel).toBeVisible();

    const box = await panel.boundingBox();
    const trigger = await page.locator('.site-footer__trigger').boundingBox();
    const width = page.viewportSize().width;
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(width + 1);
    // Above the trigger: this is a footer, so there is nothing below it.
    expect(box.y + box.height).toBeLessThanOrEqual(trigger.y + 2);
    // A stated width, not one that hugs the longest title it happens to hold.
    expect(box.width).toBeGreaterThanOrEqual(250);

    await expect(page.locator('.site-footer__footnote')).toContainText('Delaware');
    await expect(page.locator('.site-footer__footnote')).toContainText('Vienna');
  });

  test('is on the legal pages, which is where a reader arrives from another site', async ({ page }) => {
    await stubCorpus(page);
    await page.goto('/legal/privacy.html');
    await expect(page.locator('.site-footer__row')).toBeVisible();
    await expect(page.locator('.site-footer__copyright')).toContainText('Angel Software Solutions LLC');
  });

  /*
   * The live spectator pages are fixed, full-viewport app shells: the document
   * is exactly the viewport, so anything in the flow after the shell renders
   * at y=0, across the header. Their own footer has been rendering at zero
   * height there for as long as it has existed.
   *
   * This asserts the shared footer stays off them, so the exclusion remains a
   * decision somebody made rather than something that quietly comes back.
   */
  for (const path of ['/', '/m/']) {
    test(`is deliberately absent from the app shell at ${path}`, { tag: '@phone-only' }, async ({ page }) => {
      await stubCorpus(page);
      await page.goto(path);
      await expect(page.locator('.site-footer__row')).toHaveCount(0);
    });
  }
});
