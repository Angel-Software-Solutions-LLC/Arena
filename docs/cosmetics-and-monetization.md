# Cosmetics And Fair Monetization

Arena's cosmetics are presentation only, and Arena sells none of them itself.
There is one thing to buy — the monthly **Arena subscription**, bought and held
in Angel Accounts — and it includes every cosmetic in the catalog for every bot
linked to that account. There are no single-item purchases, no orders, no
licences to assign, and no payment provider in this process.

This document is the reference for that model: what unlocks what, how Arena
learns about the subscription, the fair-play boundary, the catalog, the API,
the data model (including what the previous per-item model left behind), and
the operator runbook.

## The rule

A signed-in customer whose Angel account holds an **active** Arena
entitlement has every cosmetic unlocked on every bot linked to that account.
A customer without one can equip the free cosmetics, preview everything, and
sees **"Included with an Arena subscription"** with a link to where the
subscription is sold. Nothing in between exists.

Three consequences follow, and every surface is built around them:

- **Ownership is a flag on the account, not a row per item.** The account is
  either subscribed or it is not. Which bot wears what is Arena's own state
  (`bot_cosmetic_loadout`); whether it may wear it is decided at read time by
  joining that flag.
- **A lapse hides, it does not delete.** When the flag goes off, every paid
  look on the account's bots stops rendering at the next read and the arena
  falls back to standard visuals; the saved loadout is untouched, so a renewed
  subscription brings it straight back with nothing to re-equip.
- **Free cosmetics need nothing.** Items marked `is_free` (the three permanent
  defaults and any free drops) are open to every bot, linked or not.

## How Arena learns about the subscription

Arena holds no credential for the Accounts API. It never stores the access
token from a sign-in and does not ask for `offline_access`, so the only moment
it can read entitlements is inside the sign-in callback, while it is holding
the freshly minted token. `go-arena/internal/api/customer_entitlements.go`
explains the trade in full; the short version is that a durable customer
credential in Arena's database is a table worth stealing, and signing in is
one press.

At that moment Arena calls the Accounts `/v1/entitlements` endpoint
(`go-arena/internal/accounts`), finds the row whose `productSlug` or
`productId` is `arena`, and records Accounts' own `active` flag for it —
`status` alone is not consulted, because Accounts already folds suspension
and expired staff grants into `active`. The result is written to
`customer_accounts.subscription_active` with `subscription_synced_at`, and if
it changed, every connected bot on the account has its presentation
refreshed immediately rather than at its next equip.

The read is best effort and never on the path to a session: an Accounts
outage, a rejected token, or a response with no `entitlements` key leaves the
previous answer standing and logs why. A response that omits the key is
treated as silence, not as "subscribes to nothing", so a bad deploy on the
other side cannot lock a paying customer's cosmetics.

Freshness is the cost. Somebody who subscribes in Accounts and comes straight
back sees it at their next sign-in, and the Dashboard's **Refresh
subscription** control is exactly that: a sign-in, which on a live Accounts
session is one press and a window that closes itself. A push from the
Accounts webhook outbox is the long-term answer and is not built yet.

## What the customer sees

- **Dashboard, Cosmetics tab.** A subscription card at the top says either
  *Everything unlocked* or *Included with an Arena subscription*, when the flag
  was last read, and links to the Accounts shop (`ARENA_ACCOUNTS_SHOP_URL`) to
  subscribe or manage. Below it the whole catalog is the collection, searchable
  and filterable by slot, each card either offering **Equip on <bot>** or
  saying what unlocks it. Preview stages a look on the outfitter without
  touching the server; Equip is the one write.
- **Shop.** Previews every set, full-body skin and trail on a full-size bot.
  Cards say *Free* or *Included with subscription* where a price used to be;
  the banner and the selected pack's action link to the Accounts shop, or to
  the Dashboard when no address is published.
- **Bot API.** `GET /api/v1/bot/cosmetics` returns the catalog with what this
  bot may wear and what it has equipped; `PUT /api/v1/bot/cosmetics` equips,
  and a locked item answers `403` with `code: SUBSCRIPTION_REQUIRED` and a
  `subscription_url`.
- **Admin Panel.** Catalog editing and its audit trail only. The summary
  says what unlocks paid cosmetics and flags a missing shop address. There is
  nothing to grant, no membership to time-box and no order to look up.

## Customer registration and authentication

Signing in is Angel Accounts, and only Angel Accounts. Every sign-in control
in Arena opens the Accounts window directly; Arena holds no customer password,
sends no sign-in mail, and no longer stores email addresses. On return it
binds the verified identity to a `customer_accounts` row, mints the
`arena_customer_session` cookie, and performs the entitlements read above.

A bot created anonymously with `POST /api/v1/keys/generate` is claimed by
submitting its token once to `POST /api/v1/account/bots`; the form clears the
plaintext after the request. Linking never makes the token the subscriber —
the subscription stays with the account, and every linked bot shares it. The
Dashboard can also mint account-owned keys directly (up to five active).

### Configuration

- `ARENA_CUSTOMER_OIDC_*`: the Accounts client, as documented in
  `.env.example`.
- `ARENA_ACCOUNTS_ENTITLEMENTS_URL`: normally unset; the endpoint is taken
  from the Accounts discovery document. Set only for a staging Accounts.
- `ARENA_ACCOUNTS_SHOP_URL`: where to subscribe. Must be an absolute `https`
  URL (startup refuses anything else). Published on the catalog as
  `subscription.url`; when empty the Dashboard and Shop say the operator has
  not published where to subscribe, and the admin summary flags it.
- `ARENA_COSMETICS_ACCOUNT_READ_RPM`: rate limit on the account inventory
  read.

## Fair-play boundary

Cosmetics must never change a bot's HP, movement, attack, defense, weapon,
cooldown, hitbox, collision, visibility to other bots, or protocol data used by
AI decisions.

The implementation enforces this in several places:

- Catalog, account and loadout tables contain presentation data, the
  subscription flag and which bot wears what — nothing else.
- The game engine receives only four allowlisted local asset keys. It never
  receives rarity, subscription, payment, or account data.
- The spectator renderer maps those keys to fixed local visuals; catalog rows
  cannot load arbitrary URLs, scripts, models, or materials.
- Equipping a paid cosmetic checks the subscription flag on the server, at
  write time and again at every read (`botMayWearCosmeticSQL` in
  `go-arena/internal/db/customer_cosmetics.go` is the single rule).
- Unknown, inactive, or retired assets fall back to standard visuals.
- Spectators can disable chassis skins, weapon finishes, attachments, or bot
  movement trails in the existing graphics settings.

## Catalog

The built-in catalog contains exactly 346 items: three permanent defaults and
300 custom cosmetics arranged as 100 coordinated sets (one chassis, one weapon
finish, one attachment each — Neon Signal and Void Orbit are sets 001 and 002,
sets 003-100 span the Elemental, Cosmic, Cyber, Wild, Arcane, Industrial,
Royal, Abyssal and Apex collections), a free Standard Wake trail, 24 movement
trails, and 18 full-body forms (animals, humans, fantasy characters and
constructs on the same articulated skeleton). That is 142 packs beside the
free defaults.

Every one of those 142 packs is included with the subscription. Rarity is
catalog metadata for display and search. Packs and items still carry
`price_cents`, `currency` and `is_purchasable` columns; they are reference
metadata kept by the admin editor (the schema normalises the set and trail
reference prices on repair) and nothing reads them to sell anything. Trail and
body-form packs contain exactly one item and cannot contain set pieces;
coordinated sets cannot contain either.

Set keys use the fixed `arena_set_NNN_slug` contract. A local deterministic
theme mapper gives every set a bounded palette, chassis pattern, weapon finish,
and attachment recipe. Trail keys use a fixed local allowlist. The spectator and
Shop share one procedural renderer capped at 48 ribbon meshes and 24 particle
systems, with one shared material and one shared procedural texture. No catalog
row can load a remote image, model, script, texture, or gameplay behavior.
Storefronts use bounded initial pages plus search/filter/show-more controls
instead of inserting all 346 items into the DOM at once.

## Asset source and intake policy

The starter cosmetics are fixed, local procedural visuals already rendered by
Arena. This catalog/admin work does not import a third-party archive or load
remote art at runtime.

The [`open-hotel/open-hotel-resources`](https://github.com/open-hotel/open-hotel-resources)
distribution is not an Arena asset source. Its own build scripts describe
downloading Habbo game data, while the official
[Habbo Fansite Policy](https://help.habbo.com/hc/en-us/articles/360011512480-Habbo-Fansite-Policy)
is aimed at non-commercial fan work. Arena's paid cosmetics therefore use only
original procedural recipes; the repository may inform generic ideas such as
layered slots or directional sprite atlases, but none of its art is copied or
bundled.

Good CC0 candidates for later reviewed drops include Kenney's
[Rune Pack](https://kenney.nl/assets/rune-pack) for decals,
[Particle Pack](https://kenney.nl/assets/particle-pack) for aura/trail source
art, and [Space Shooter Extension](https://kenney.nl/assets/space-shooter-extension)
for attachment silhouettes. Kenney's [support page](https://kenney.nl/support)
states that game assets on its asset pages are CC0 and may be used commercially
without required attribution. Preserve the exact source page and license record
with every imported batch even when attribution is optional.

Before an external asset becomes a catalog item:

1. Download it from the recorded upstream page and retain the license snapshot.
2. Reject executables, scripts, remote URLs, and files outside the reviewed
   image/model formats.
3. Normalize dimensions, transparency, naming, and texture size offline.
4. Map it to a fixed local `asset_key`; catalog rows must not select arbitrary
   files or URLs.
5. Test GPU memory, draw calls, mobile fidelity, color contrast, and cleanup on
   round/map changes before enabling it.
6. What the subscription includes is the curated cosmetic, not the upstream
   source archive, attribution, authorship, or exclusivity.

## API

Public:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/v1/cosmetics/catalog` | Active categories, packs and items, plus `subscription: {product: "arena", includes_all_cosmetics: true, url}` (`url` only when `ARENA_ACCOUNTS_SHOP_URL` is set). Publishes no checkout fact. |

Bot token (`Authorization: Bearer <api_key>`):

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/v1/bot/cosmetics` | The catalog with `owned` per item (free, or everything once the bot's account subscribes) and the equipped loadout |
| `PUT` | `/api/v1/bot/cosmetics` | `{slot, cosmetic_id}`; equips. A locked item answers `403 {code: "SUBSCRIPTION_REQUIRED", subscription_url}` |

Customer session (`arena_customer_session` cookie, CSRF on mutations):

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/v1/account/session` | Session document |
| `GET` | `/api/v1/account/cosmetics` | Inventory: `account`, `bots`, `subscription: {active, synced_at, url}`, `items` (the whole catalog), `loadouts` (bot → slot → item) |
| `GET` / `POST` / `DELETE` | `/api/v1/account/keys[/{key_id}]` | Account-owned API keys |
| `POST` | `/api/v1/account/bots` | Claim a bot by proving its token once |
| `DELETE` | `/api/v1/account/bots/{bot_id}` | Unlink; paid loadout rows for that bot are removed, the key is untouched |
| `PUT` | `/api/v1/account/bots/{bot_id}/cosmetics` | `{slot, cosmetic_id}`; equips on a linked bot, same `403 SUBSCRIPTION_REQUIRED` for a locked item |
| `PATCH` | `/api/v1/account/profile` | Display name, bio, avatar colour |

Admin (`X-Admin-Token`, or a customer session carrying the `staff` or
`product_admin` claim):

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/v1/admin/cosmetics/catalog` | Every category, pack and item including inactive ones, plus the same `subscription` block |
| `GET` | `/api/v1/admin/cosmetics/audit` | Catalog change audit |
| `PUT` / `DELETE` | `/api/v1/admin/cosmetics/categories/{id}` | Category upsert / delete |
| `PUT` / `DELETE` | `/api/v1/admin/cosmetics/packs/{id}` | Pack upsert / delete |
| `PUT` / `DELETE` | `/api/v1/admin/cosmetics/items/{id}` | Item upsert / delete |

Retired, and answering `404` like any unknown path: `/account/cosmetics/checkout`,
`/account/cosmetics/orders`, `/account/cosmetics/orders/{id}/checkout`,
`/account/cosmetics/subscription/checkout`,
`/account/cosmetics/subscription/portal`, `/account/cosmetic-licenses/*`,
`/cosmetics/webhooks/stripe`, and the admin `/cosmetics/grants`,
`/cosmetics/licenses`, `/cosmetics/memberships`, `/cosmetics/access` and
`/cosmetics/orders` routes. `TestCheckoutRoutesAreGone` in
`go-arena/internal/api/subscription_cosmetics_test.go` walks the router and
holds that line.

## Data model

Live tables:

- `customer_accounts` — the Angel account binding. `subscription_active`
  (boolean, default false) and `subscription_synced_at` are the subscription
  flag and when it was last read.
- `account_bot_links` — which bots an account has claimed.
- `bot_cosmetic_loadout (bot_id, slot, cosmetic_id, updated_at)` — which bot
  wears what. One row per slot; equipping replaces the row.
- `cosmetic_categories`, `cosmetic_packs`, `cosmetic_pack_items`,
  `cosmetic_items`, `cosmetic_catalog_audit` — the catalog.
- `cosmetic_entitlements (bot_id, cosmetic_id)` — bot-scoped complimentary
  grants written only by the admin demo-loadout tool, so a demonstration bot
  can wear paid looks without an account. Honoured by the same read-time rule
  as the subscription.

The single rule, `botMayWearCosmeticSQL`, is: the item is free, **or** the bot
holds a `cosmetic_entitlements` row for it, **or** the bot is linked to an
account with `subscription_active = true`. It is joined at every read, equip
and render; there is no materialised per-item ownership anywhere.

### Migration notes

Schema changes are additive and idempotent (`EnsureCosmeticsSchema` runs
`CREATE ... IF NOT EXISTS` and `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`);
the managed-schema preflight in `cmd/arena-server/main.go` requires the two
subscription columns and `bot_cosmetic_loadout.cosmetic_id` before serving.
Nothing is dropped:

- The per-item era's tables — `cosmetic_licenses`,
  `cosmetic_license_assignments`, `cosmetic_orders`, `cosmetic_order_items`,
  `cosmetic_order_licenses`, `cosmetic_order_refunds`,
  `cosmetic_payment_events`, `cosmetic_subscriptions`,
  `cosmetic_subscription_events`, `cosmetic_subscription_licenses`,
  `cosmetic_admin_memberships`, `cosmetic_admin_membership_licenses`,
  `customer_accounts_grants`, `customer_accounts_grant_licenses` and
  `platform_license_lifecycle_events` — are left in place and are no longer
  read or written. No code references them; an operator may drop them in a
  maintenance window once the previous release is out of rollback reach, or
  keep them as the record of past purchases.
- `bot_cosmetic_loadout` keeps its licence-era columns (`license_id` and
  `account_id`, both nullable) where a database has them; new rows leave them
  `NULL`, and `TestPostgresLicenceEraLoadoutColumnsStayNullAndHarmless` covers
  an upgraded database. A fresh database gets only the minimal table.
- Existing customers hold `subscription_active = false` until their next
  sign-in reads Accounts. Previously purchased per-item licences do not
  convert; the product is subscription-only.

## Runbook

1. Configure the Accounts client (`ARENA_CUSTOMER_OIDC_*`) and confirm it with
   `arena-server check-oidc`.
2. Set `ARENA_ACCOUNTS_SHOP_URL` to the Accounts shop page for the Arena
   product. Startup refuses a non-`https` value.
3. Make sure the Accounts client is provisioned for the `entitlements` scope
   and that the discovery document advertises `entitlements_endpoint`; the
   sign-in log line `accounts entitlements not readable for this client`
   means it is not.
4. Watch `accounts subscription applied` in the logs after a customer signs
   in: it names the account, the flag and how many bots were refreshed.
5. A customer who subscribed and does not see it: ask them to press **Refresh
   subscription** (a sign-in). If it still reads false, check the Arena
   product row in their Accounts entitlements — Arena records exactly its
   `active` flag.
6. Demonstration bots that must wear paid looks without an account use the
   admin demo-loadout tool, which writes `cosmetic_entitlements` for that bot
   only.
