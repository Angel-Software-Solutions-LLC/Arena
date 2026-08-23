# Post-Cutover Account Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore authenticated Dashboard account features for Angel Accounts sessions whose email is intentionally omitted after the customer-data cutover, while keeping legacy email-bearing sessions working and ensuring browsers receive the corrected assets.

**Architecture:** Make `ArenaAccountCosmetics` the single source of truth for account eligibility and human-readable identity. The Dashboard consumes those helpers instead of re-implementing an email-based gate. Exercise the real browser flow with a post-cutover session fixture, then update the directly served asset identities so deployed HTML cannot reuse pre-fix JavaScript or CSS.

**Tech Stack:** Browser JavaScript, Node.js assertion scripts, Playwright, static HTML, Go repository gate.

**Spec:** `docs/superpowers/specs/2026-08-23-account-dashboard-post-cutover-design.md`

## Global Constraints

- Treat an account as eligible only when the session explicitly says `authenticated: true`, the normalized account has a non-empty stable `id`, and `email_verified === true`.
- An email address is optional. Never use its presence as an authentication, assignment, or rendering gate.
- Display identity in this exact preference order: `display_name`, then `name`, then normalized legacy `email`, then `Angel account`.
- Keep legacy email-bearing accounts compatible and preserve all server/API payload contracts.
- Use account-neutral customer copy. Do not tell post-cutover customers that their ownership or verification is tied to email.
- Use `?v=20260823b` for every directly served file changed by the preceding account cutover: `dashboard.css`, `account-cosmetics.js`, `dashboard.js`, `cosmetics-shop.js`, and `embedded-checkout.js` in both HTML importers. Do not change the unchanged `embedded-checkout.css` identity.
- Use public behavior and real page integration for regression coverage. Static source assertions are acceptable only for the deploy-time asset URL contract.
- Follow red/green TDD for every behavior change: add the failing assertion, run it and inspect the expected failure, make the smallest production change, then rerun it green.
- Preserve unrelated working-tree changes. Do not commit, push, open a pull request, merge, deploy, or publish without separate authorization.

---

## Task 1: Centralize post-cutover account eligibility and identity

**Files:**

- Modify: `scripts/test-dashboard-account-cosmetics.mjs`
- Modify: `frontend/dashboard/account-cosmetics.js`

- [ ] Add post-cutover helper tests to `scripts/test-dashboard-account-cosmetics.mjs` using this session shape:

  ```js
  {
    authenticated: true,
    account: {
      id: 'acct-post-cutover',
      email: '',
      email_verified: true,
      display_name: 'Arena Pilot',
    },
  }
  ```

  Assert that normalization retains the ID, verified flag, empty email, and display name; `hasVerifiedAccount` accepts it; explicit sign-out, missing ID, and an unverified account are rejected; and `accountLabel` returns `Arena Pilot`, a normalized legacy email when no name exists, and `Angel account` when neither exists.

- [ ] Add a post-cutover snapshot assertion proving `assignmentIntent` can assign a real active license to a linked active bot without an email. Change the rejection assertion for an unverified account to expect `verified-account-required`.

- [ ] Add rendered-output assertions proving the summary uses `Arena Pilot`, says `Verified account` and `Account verified`, and contains no customer-facing `verified email`, `email account`, or `Email verified` copy. Assert linked-bot and API-key guidance use account-neutral wording.

- [ ] Run the focused test and confirm it fails for the intended missing helpers/email gate before implementation:

  ```bash
  node scripts/test-dashboard-account-cosmetics.mjs
  ```

- [ ] In `frontend/dashboard/account-cosmetics.js`, add and export:

  ```js
  function isVerifiedAccount(rawAccount) {
    const account = rawAccount && typeof rawAccount === 'object' ? rawAccount : {};
    return Boolean(cleanText(account.id)) && account.email_verified === true;
  }

  function hasVerifiedAccount(rawSession) {
    const session = normalizeSession(rawSession);
    return session.authenticated === true && isVerifiedAccount(session.account);
  }

  function accountLabel(rawAccount) {
    const account = rawAccount && typeof rawAccount === 'object' ? rawAccount : {};
    return cleanText(account.display_name || account.name)
      || cleanText(account.email).toLowerCase()
      || 'Angel account';
  }
  ```

- [ ] Make `normalizeSession` prefer `display_name` over `name` when it produces the normalized `account.name`, so sessions with both fields obey the shared display-order contract.

- [ ] Change `assignmentIntent` to authorize with `isVerifiedAccount(state.account)` and return `verified-account-required` otherwise.

- [ ] Use `accountLabel` in the account summary and linked-bot ownership copy. Change the summary labels to `Verified account` and `Account verified`, and make the API-key, linked-bot, and ownership guidance account-neutral. Keep the existing CSS class name for compatibility.

- [ ] Rerun the focused test and confirm it passes:

  ```bash
  node scripts/test-dashboard-account-cosmetics.mjs
  ```

## Task 2: Integrate the shared policy into the Dashboard and cover the real browser flow

**Files:**

- Create: `tests/browser/specs/account-dashboard-post-cutover.spec.mjs`
- Modify: `frontend/dashboard/dashboard.js`
- Modify: `frontend/dashboard/cosmetics-preview.js`
- Modify as needed for assertions: `scripts/test-dashboard-account-cosmetics.mjs`

- [ ] Add a Playwright test that serves the real `/dashboard/` page, intercepts the account APIs, and returns the exact post-cutover account identity from Task 1. Provide minimal successful payloads for session, cosmetics inventory, catalog, recent orders, keys, and profile requests.

- [ ] Assert through the rendered page that the Cosmetics and Profile tabs are visible, the account sign-out control is visible, the toolbar and account panel show `Arena Pilot`, and the account cosmetics inventory endpoint is requested. These assertions must fail against the current email-gated Dashboard.

- [ ] Install the existing browser-test dependencies if needed, without changing their manifests or lockfile, then run only the new spec and confirm the expected red result:

  ```bash
  npm ci --prefix tests/browser --ignore-scripts
  npm test --prefix tests/browser -- specs/account-dashboard-post-cutover.spec.mjs
  ```

- [ ] Replace the Dashboard-local eligibility logic with delegation to `window.ArenaAccountCosmetics.hasVerifiedAccount(accountSession)`. Add a small local identity-label wrapper that delegates to `accountLabel(accountSession.account)` for toolbar and switcher rendering.

- [ ] Replace email interpolation and email-specific success/status messages in `frontend/dashboard/dashboard.js` with the shared account label and account-neutral copy. Update `frontend/dashboard/cosmetics-preview.js` to prompt users to verify their Angel account rather than their email account.

- [ ] Rerun the new Playwright spec and the focused Node regression until both pass:

  ```bash
  npm test --prefix tests/browser -- specs/account-dashboard-post-cutover.spec.mjs
  node scripts/test-dashboard-account-cosmetics.mjs
  ```

## Task 3: Publish fresh asset identities for all cutover-touched files

**Files:**

- Modify: `scripts/test-dashboard-account-cosmetics.mjs`
- Modify: `scripts/test-cosmetics-launch-ui.mjs`
- Modify: `frontend/dashboard/index.html`
- Modify: `frontend/shop/index.html`
- Modify: `frontend/index.html`

- [ ] Update the static delivery assertions first so they require:

  - `frontend/dashboard/index.html`: `dashboard.css?v=20260823b`, `account-cosmetics.js?v=20260823b`, `dashboard.js?v=20260823b`, and `embedded-checkout.js?v=20260823b`.
  - `frontend/shop/index.html`: `cosmetics-shop.js?v=20260823b`.
  - `frontend/index.html`: `embedded-checkout.js?v=20260823b`.
  - Both existing `embedded-checkout.css?v=20260713a` references remain unchanged.

- [ ] Run the two focused scripts and confirm the expected stale-version failures before editing HTML:

  ```bash
  node scripts/test-dashboard-account-cosmetics.mjs
  node scripts/test-cosmetics-launch-ui.mjs
  ```

- [ ] Update only those importer URLs to the exact identities above. Do not alter unrelated asset versions.

- [ ] Rerun the two focused scripts and confirm they pass:

  ```bash
  node scripts/test-dashboard-account-cosmetics.mjs
  node scripts/test-cosmetics-launch-ui.mjs
  ```

## Task 4: Whole-change verification and review

**Files:** Review all files changed by Tasks 1-3; edit only to address validated review findings.

- [ ] Run all directly affected Node and browser checks:

  ```bash
  node scripts/test-dashboard-account-cosmetics.mjs
  node scripts/test-dashboard-cosmetics-preview.mjs
  node scripts/test-cosmetics-launch-ui.mjs
  node scripts/test-embedded-stripe-checkout.mjs
  node scripts/test-purchases-via-accounts.mjs
  npm test --prefix tests/browser -- specs/account-dashboard-post-cutover.spec.mjs specs/purchases-handoff.spec.mjs
  ```

- [ ] Run the repository-required Go gate. Use the local Go toolchain when available; otherwise use an isolated official Go container against `go-arena` and report any host limitation precisely:

  ```bash
  cd go-arena && go test ./...
  ```

- [ ] Run syntax and patch-integrity gates:

  ```bash
  node --check frontend/dashboard/account-cosmetics.js
  node --check frontend/dashboard/dashboard.js
  node --check frontend/dashboard/cosmetics-preview.js
  git diff --check
  ```

- [ ] Dispatch an independent whole-branch reviewer with the approved spec, plan, test evidence, and full working-tree diff. Address all validated Critical or Important findings and rerun the covering checks.

- [ ] Confirm `git status --short`, list every intended changed file, and ensure no dependency install artifact or unrelated file entered the patch.

## Final post-review hotfix wave

**Files:**

- Modify: `frontend/index.html`
- Modify: `frontend/dashboard/dashboard.js`
- Modify: `frontend/dashboard/account-cosmetics.js`
- Modify: `scripts/test-cosmetics-launch-ui.mjs`
- Modify: `scripts/test-dashboard-cosmetics-preview.mjs`
- Modify: `scripts/test-dashboard-account-cosmetics.mjs`
- Modify: `tests/browser/specs/account-dashboard-post-cutover.spec.mjs`

- [ ] Keep Arena-shell customer copy account-neutral: user actions say “sign
  in to your Angel account,” session requirements say “authenticated Angel
  account Dashboard session,” and ownership says “verified account.” Assert
  the shell contains neither `verify your email` nor `verified-email`.
- [ ] Change the lazy preview importer and its static assertion to the exact
  changed asset identity `./cosmetics-preview.js?v=20260823b`.
- [ ] Normalize `display_name` and `name` independently before choosing the
  first non-empty value, so whitespace display names retain a legacy-name
  fallback while genuine display names remain preferred.
- [ ] Replace the browser fixture's generic API `{}` fallback with explicit
  payloads for session, inventory, catalog, orders, keys, profile, content,
  service status, chat config, and version; throw an error naming every
  unexpected API path. Run the post-cutover browser spec in all configured
  viewport projects after the fixture is fail-closed.
