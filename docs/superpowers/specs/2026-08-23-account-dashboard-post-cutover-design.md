# Post-Cutover Account Dashboard Recovery Design

**Date:** 2026-08-23
**Status:** Approved

## Goal

Restore the authenticated Dashboard, cosmetic management, and purchase-handoff
experience for Angel Accounts identities whose Arena session deliberately no
longer contains an email address.

## Problem

The post-cutover session contract returns an authenticated, verified account
with a stable account ID and an empty `email` field. The Dashboard still treats
email presence as part of authorization, so it hides account tabs, sign-out,
inventory, and assignment controls for every newly linked account. The merged
purchase-handoff release also changed several directly served frontend files
without changing their cache-bust URLs.

## Design

### Account eligibility

`frontend/dashboard/account-cosmetics.js` will own the shared eligibility
rules:

- A verified account has a non-empty account ID and `email_verified === true`.
- An eligible session is explicitly authenticated and contains a verified
  account.
- Email is optional and never participates in authorization.

The Dashboard will delegate its account-ready check to this shared helper.
Cosmetic assignment validation will use the same verified-account rule, so a
post-cutover account cannot enter the Dashboard but then be rejected by a
second, email-dependent client check.

### Account identity copy

A shared display helper will choose the first non-empty value from:

1. account display name/name;
2. legacy email, for accounts that still have one;
3. `Angel account`.

The Arena shell, Dashboard, and cosmetic-management copy will describe a
verified account rather than a verified email. User actions say to sign in to
an Angel account; session requirements say authenticated Angel account
Dashboard session; ownership copy says verified account. Internal CSS class
names may remain unchanged because they are not a user-visible contract. No
email will be restored or synthesized.

### Cache delivery

Every directly served file changed by the purchase-handoff release will receive
a fresh `?v=20260823b` importer tag:

- `frontend/dashboard/dashboard.css`
- `frontend/dashboard/account-cosmetics.js`
- `frontend/dashboard/dashboard.js`
- `frontend/js/cosmetics-shop.js`
- `frontend/js/embedded-checkout.js` from both the Arena shell and Dashboard

The Dashboard's changed lazy preview controller is also served through the
exact importer identity `./cosmetics-preview.js?v=20260823b`; its focused
static assertion prevents the dynamic import from retaining an older URL.

Unchanged assets, including `embedded-checkout.css`, keep their existing tags.

## Compatibility and security

- The server API, database schema, cookies, and CSRF behavior do not change.
- Explicit authentication, stable account ID, and server-issued verification
  remain required; the fix does not reduce the authorization boundary to a
  display-name check.
- Legacy accounts with an email continue to work and use that address only as
  a display fallback when no display name is present.
- Signed-out, unverified, or ID-less sessions remain unable to use account
  controls.

## Testing

Tests will be written before production changes and will cover:

- an authenticated, verified, ID-bearing session with an empty email is
  eligible;
- signed-out, unverified, and ID-less sessions are ineligible;
- cosmetic assignment accepts an eligible email-less account;
- account labels prefer display name, then legacy email, then the generic
  fallback;
- whitespace-only display names fall back to a non-empty legacy name, while a
  real display name still wins;
- the root Arena shell contains no customer-facing `verify your email` or
  `verified-email` wording;
- the changed Dashboard preview importer uses
  `./cosmetics-preview.js?v=20260823b`;
- a real browser loading `/dashboard/` with the post-cutover session shape
  shows account tabs and sign-out, loads inventory, and renders account-neutral
  identity copy; its API fixture explicitly supplies all allowed Dashboard and
  shell routes and rejects unexpected API paths;
- the changed frontend entry points reference the fresh cache tags.

The affected Node checks, JavaScript syntax checks, browser test, repository
whitespace gate, and full Go suite are the intended validation set. If the Go
toolchain remains unavailable locally, the limitation will be reported and the
fresh required CI run will remain the authoritative backend gate.

## Out of scope

- Reintroducing or synthesizing customer email addresses.
- Changing Angel Accounts or Arena server identity contracts.
- Gameplay, SDK, updater, or unrelated Dashboard refactors found during the
  broader review.
