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
| `internal/platform` | provider-neutral shared identity/catalog/license authority port and current same-database adapter |
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
  links and cosmetics survive key rotation or revocation.
- Admin APIs require an admin token, a database-issued admin token, or configured OIDC/SSO.
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

Account registration is a separate, optional commerce path: after signing in
with Angel Accounts, the owner proves the existing token once to claim that bot
before purchasing, assigning, or equipping cosmetics. Signing in is one press
of any sign-in control and opens the Accounts flow directly; Arena holds no
customer password and sends no sign-in mail.

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
