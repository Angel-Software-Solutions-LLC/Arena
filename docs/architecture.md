# Architecture

AI Battle Arena has four main surfaces: the Go game server, the browser spectator UI, bot SDKs, and optional local admin tooling.

## High-Level Flow

```text
Bot SDK / custom bot
  -> REST: create key, fetch map, configure bot
  -> WebSocket: select loadout, receive ticks, send actions

Go server
  -> validates credentials, loadouts, and actions
  -> advances the game loop
  -> persists bot and leaderboard data
  -> broadcasts spectator state

Browser frontend
  -> REST: public status, leaderboard, weapon stats, docs
  -> WebSocket: spectator stream
  -> renders the arena with Babylon.js
```

## Backend

The backend lives in `go-arena/`.

| Package | Responsibility |
| --- | --- |
| `cmd/arena-server` | process entrypoint and service wiring |
| `internal/api` | REST routes, admin routes, security headers, public bot setup metadata |
| `internal/config` | environment-driven configuration |
| `internal/db` | PostgreSQL connection, queries, and models |
| `internal/game` | game state, combat, movement, pickups, map shape, scoring, rounds |
| `internal/platform` | provider-neutral shared identity/catalog authority port (identity, subscription flag, cosmetics) and current same-database adapter |
| `internal/demobots` | built-in demo bot behavior |
| `internal/security` | API key generation/verification, validation, rate limiting |
| `internal/ws` | bot and spectator WebSocket handlers |

## Frontend

The frontend lives in `frontend/` and is served as static files by the Go server. There is no bundler.

- `frontend/index.html` is the desktop spectator and onboarding page.
- `frontend/m/` is the mobile spectator page.
- `frontend/dashboard/` is the public/private bot toolkit surface.
- `frontend/js/renderer/` contains Babylon.js scene modules.
- `frontend/js/settings.js` is the graphics settings source of truth.

Because static files are served directly, HTML, CSS, and JavaScript changes should be syntax-checked and tested in a browser.

## Persistence And Cache

The default Docker Compose stack runs:

- PostgreSQL for bot keys, bot metadata, leaderboard, round history, and related state
- Redis for rate limiting and cache-backed controls
- The Go server bound to localhost by default

For local experiments, `ARENA_DB_OPTIONAL=true` can let the server run in a degraded mode without persistence.

## Security Model

- Public bot tokens are generated only by Arena at `POST /api/v1/keys/generate`.
  The database atomically stores a rollback-safe composite credential, lookup
  prefix, and bot; the plaintext is returned once and arbitrary caller-chosen
  strings are invalid. The composite keeps a bcrypt prefix for old readers and
  appends a versioned digest for fast current authentication. Legacy bcrypt
  rows remain valid and migrate to the composite after successful use.
- Public generation does not require an account. A later signed-in Dashboard
  session -- established through Angel Accounts, the only customer sign-in
  Arena has -- can claim the existing bot by submitting its token once to
  `POST /api/v1/account/bots`; the form clears that proof after the request.
- The Dashboard may also create account-owned keys directly. Durable account
  links, the subscription flag and equipped cosmetics survive key rotation or
  revocation.
- Cosmetics are unlocked by one thing: an active Arena subscription on the
  Angel account, read from the Accounts entitlements endpoint at sign-in and
  written to `customer_accounts.subscription_active`. Arena sells nothing
  itself and holds no payment credential; see
  [cosmetics-and-monetization.md](cosmetics-and-monetization.md).
- Admin APIs separate people from machines, and admit each on its own terms.
- **Humans:** Angel Accounts is the single source of human admin authority,
  through two claims on the verified ID token. The support-desk role arrives
  as `staff: true` (with `staff_role` recorded for audit); a product
  administrator for Arena arrives as `product_admin: true`. Either claim,
  present as the literal `true`, opens the Admin Panel and the admin API. The
  claims are read only from the validated ID token, decided on presence
  rather than truthiness (a string `"true"`, `false` or an absent claim admits
  nobody), and never persisted; see `go-arena/internal/api/platform_admin.go`.
  The audit principal says which claim admitted the actor:
  `accounts-staff:<account_id>[:role]` for the desk role,
  `accounts-product-admin:<account_id>` for a product grant (the desk role
  wins when both are present), and `GET /api/v1/admin/session` reports the
  same `authority`. Authority lapses on `ARENA_OIDC_SESSION_TTL_HOURS` and is
  re-read at the next sign-in, so withdrawing either grant in Accounts revokes
  it with nothing to clean up in Arena. A customer cookie without one of the
  claims is a customer, never an administrator. Because the panel acts on the
  ordinary `arena_customer_session` cookie, its mutations are held to the same
  same-origin and CSRF checks as every other customer mutation.
- **Retired:** Arena's own admin SSO application — its issuer and client
  credentials, its `arena_admin_session` cookie, the `/admin/login`,
  `/admin/callback` and `/admin/logout` routes, and the
  `ARENA_OIDC_ADMIN_EMAILS` allowlist that admitted people to it. Two places
  deciding who administers the platform is one too many, and the allowlist was
  the one nothing revoked when somebody left the desk. Those variables are
  ignored if still set. `ARENA_OIDC_SESSION_TTL_HOURS` survives and now means
  only the grant window above.
- **Machines:** `ARENA_ADMIN_TOKEN`, database-issued admin tokens presented as
  `X-Admin-Token`, and the `ARENA_ADMIN_LOCALHOST_BYPASS` loopback path are
  unchanged. They authenticate automation, not identities, so nothing about
  the desk claim applies to them.
- **Break-glass:** if Angel Accounts is unreachable, those machine paths are
  the way in — an operator on the host uses the loopback bypass, or a holder of
  `ARENA_ADMIN_TOKEN` or a database-issued token uses `X-Admin-Token`. That is
  deliberate: there is no second emergency human login to keep in sync, and
  inventing one would recreate exactly the second source of truth this change
  removed.
- Administrator authority is orthogonal to the subscription: a `product_admin` grant does not subscribe the account, and a subscription grants no administrative access.
- Bot input is schema validated before it affects game state.
- WebSocket and HTTP paths have size and rate controls.
- Production deployments should terminate TLS at a reverse proxy and pass only the needed routes to the server.

## Bot Protocol

Bots interact with the arena through a small loop:

1. Generate a server-issued token from Get Started or `POST /api/v1/keys/generate`, or load an existing token.
2. Connect to `/ws/bot?key=...`.
3. Receive `connected`.
4. Send `select_loadout`.
5. Receive `tick` messages.
6. Send an `action` for the current tick.

Account registration is a separate, optional path: after signing in with
Angel Accounts, the owner proves the existing token once to claim that bot,
and every cosmetic the Arena subscription includes can then be equipped on
it. Signing in is one press of any sign-in control and opens the Accounts flow
directly; Arena holds no customer password and sends no sign-in mail.

The complete public reference is in [BOT-GUIDE.md](../BOT-GUIDE.md) and the machine-readable endpoint `GET /api/v1/bot-setup`.

## Runtime Capacity Signals

The authenticated Admin endpoint `GET /api/v1/admin/debug/metrics` exposes the
signals needed to tune connection capacity without load-testing production:

- lifetime and trailing-one-minute WebSocket attempts, upgrades, admissions,
  failures, average rates, single-second peaks, and admission latency for bot,
  spectator, and chat endpoints
- current game connections and configured tick rate
- PostgreSQL pool capacity, acquired/idle connections, wait counts, and
  cumulative acquire time

These are observed process rates, not a universal WebSocket ceiling. Validate
larger targets through the real TLS proxy/tunnel and database/Redis path in a
staging environment before raising production connection limits.
