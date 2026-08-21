# Platform Authority Migration

Arena is adopting the shared Angel-Serv identity and cosmetics platform through
an in-process, same-database strangler. The migration keeps the existing
PostgreSQL records authoritative while callers move behind one port. It does
not copy ownership state, dual-write records, or enable checkout.

## W1b.1 boundary

W1b.1 merged to Arena `main` in
[`Angel-Software-Solutions-LLC/Arena#213`](https://github.com/Angel-Software-Solutions-LLC/Arena/pull/213) at merge commit
`f9207348665f48adfc3e112a36515c4573c8c609`.

`go-arena/internal/platform` defines the first authority facets and a
`PostgresAuthority` adapter over Arena's existing transactions. Router wiring
passes the same adapter to:

- verified OIDC customer identity binding;
- public and administrative catalog operations;
- customer inventory;
- account-agent link and unlink;
- license assignment, grant, revocation, and administrator membership
  reconciliation.

Arena's existing `bot_id` is the stable platform `agent_id`; this checkpoint
does not mint replacement identities.

The following state remains private to Arena and is intentionally outside the
authority port:

- bot API credentials and account-key issuance;
- customer cookies and durable sessions;
- gameplay profile data and bot configuration;
- allowlisted local cosmetic asset keys;
- bot cosmetic inventory projections and `bot_cosmetic_loadout` equip state;
- live game-engine visual refreshes.

The adapter currently returns legacy Arena database projections so existing
HTTP responses remain byte-compatible. Later versioned platform handlers will
map those records to the operational cross-game contract rather than exposing
database types as a public service API.

## Preserved recovery paths

`cosmetic_entitlements`, unclaimed legacy licenses, and deterministic claim
logic remain in place. Link and assignment transactions still perform their
existing recovery and Arena loadout cleanup work atomically. Those crossings
are temporary compatibility behavior, not permission to create another copy of
the ownership ledger.

## Required follow-up before service extraction

W1b.1 establishes dependency direction only.

### W1b.2 operational metadata checkpoint

[`Angel-Software-Solutions-LLC/Arena#216`](https://github.com/Angel-Software-Solutions-LLC/Arena/pull/216) delivered the
cap-independent W1b.2 agent/profile, revision, history, and change-feed
foundation. The follow-up account-capacity checkpoint completes W1b.2 while
remaining in-process on the same PostgreSQL database. Together they add:

1. durable account, agent, and game-profile metadata while preserving every
   Arena bot ID;
2. a platform-owned `maximum_agents` value of 10 per account and a
   transactionally computed `current_agents` count;
3. resource revisions, idempotency records, durable link history, and an
   ordered, bounded change feed.

`current_agents` counts current `account_bot_links`; it is deliberately not a
stored counter and does not change when an API key is deactivated. The
platform's 10-agent capacity is independent of Arena's five-active-API-key
credential limit. Account creation installs the platform metadata in the same
transaction. Every real link or unlink increments the account revision and
emits an account change; same-account link replays do neither.

All link writers acquire the customer-account row before checking the derived
count, so concurrent attempts cannot race past 10. Authentic legacy backfill
fails and rolls back if an existing account is already over its durable
capacity; it never silently unlinks an agent or raises the limit. Managed
deployment preflight requires the account metadata columns before the runtime
starts.

The evidence covers authentic legacy backfill, restart, rollback and
reconciliation, concurrent mutations, API-key independence, and large
irrelevant histories with index-compatible bounded reads. It preserves
`cosmetic_entitlements`, deterministic legacy-license recovery, and the current
single writable authority. Migration and reconciliation do not reactivate a
refunded, revoked, chargeback, or expired license. W1b.2 does not add a second
ownership store, dual-write records, expose platform HTTP handlers, or
physically extract a service.

### W1b.3 control-proof checkpoint

Account-agent linking now consumes the Arena API-key control proof inside the
same PostgreSQL transaction as account revision and capacity enforcement,
credential ownership, link/history/change writes, and legacy-license recovery.
The public compatibility route remains `POST /api/v1/account/bots` with its
existing `{api_key}` body, quota-before-proof ordering, response, and error
behavior. It delegates the plaintext proof once to the authority and does not
pre-verify a trusted bot ID.

The authority also exposes an in-process exact command for account ID, agent
ID, control proof, expected account revision, and idempotency key. Exact-command
replay is scoped by account, verifies proof before returning a replay, binds the
proof-derived bot to the requested agent, and stores only a non-secret request
identity and response. A committed replay remains response-stable if the agent
is retired later; retirement rejects only new link commands. Plaintext proof is
never logged, returned, or written to idempotency, history, change, or license
records.

Both entry points lock the verified customer account, platform account
metadata, and credential in that order before invoking one private link-state
core. Credential verification is implemented inside the authority boundary;
production callers cannot substitute a verifier. Missing, inactive, malformed,
mismatched, incorrect, or retired-agent credentials fail closed. The native
account-key creation transaction remains the separate
privileged registration-and-initial-link path; it cannot claim an existing
agent by trusted ID. W1b.3 still does not expose the versioned platform HTTP
adapter or create a second writable authority.

### W1b.3a cross-repository semantic-conformance checkpoint

Arena consumes the byte-identical Kingdom Grid platform-v1 contract fixture at
`go-arena/internal/db/testdata/platform-v1-consumer-contract.json`. Kingdom
Grid derives its authoritative copy from `protocol/platform/openapi.json` and
pins the exact source blob, so either repository's tests fail when revision
boundaries, idempotency limits, or ordered-change wire semantics drift.

This checkpoint corrects the four independently audited W1a consumer gaps:

1. game-profile transitions compare `expected_agent_revision` with the stable
   agent identity revision while incrementing both the agent and profile
   revisions for a real status change;
2. exact platform mutations accept only 8-128 character idempotency keys;
3. a revisioned and idempotent exact unlink command shares the existing Arena
   assignment-cleanup and link-history transaction, while the public
   compatibility route keeps its current behavior; and
4. the ordered projection serializes string change IDs, `occurred_at`,
   `agent_identity`, and only the canonical W1a transition values, without
   rewriting the existing durable change table.

The exact unlink replay record, account revision update, license-assignment and
loadout cleanup, link history, and ordered changes commit atomically. A stale
revision or conflicting idempotency-key reuse makes no ownership change. This
checkpoint remains same-database and in-process; it adds no public platform
HTTP handler, second writable authority, or Kingdom Grid runtime adapter.

### W1b.4 terminal-license lifecycle checkpoint

Arena issue [#223](https://github.com/Angel-Software-Solutions-LLC/Arena/issues/223) tracks the
same-database W1b.4 delivery. The in-process authority now exposes exact
assignment, explicit unassignment, and terminal lifecycle commands. Each exact
command binds an 8-128 character idempotency key to its request, compares the
expected license revision, and returns stable revision and idempotency errors.

One private transactional lifecycle core owns license creation, deterministic
claim, assignment, unassignment, and terminal cleanup. Existing Arena order,
subscription, admin-membership, manual grant/revoke, account-link, and legacy
entitlement handlers remain public compatibility facades over that core. A
real mutation increments the license revision, appends internal durable
history with separate source, reason, provider reference, previous/current
status, and previous/current agent identity, and emits the existing ordered
platform change in the same
transaction. History reads are cursor-bounded and use the
`(license_id,event_id)` index.

`refunded`, `revoked`, `chargeback`, and `expired` are terminal license states
with deterministic precedence (`chargeback > refunded > revoked > expired >
active`). Stronger provider truth may supersede a weaker terminal status; no
transition may reactivate or downgrade the license. Terminal cleanup removes
the exact assignment and Arena loadout without changing gameplay data. A
recoverable subscription billing mismatch therefore creates and maps a new
license generation after valid provider recovery; it never changes the ended
license back to active. Authentic legacy rows are normalized and projected
into history and ordered changes during startup without reactivation.

The PostgreSQL evidence covers exact replay and conflicting-key rejection,
stale revisions, terminal precedence, concurrent terminal races, atomic
upgrade rollback, all four terminal states, assignment/unassignment,
subscription generations, demo regrant generations, authentic backfill,
restart, bounded history, and the existing compatibility journeys.
The actual W1b.3 binary at `1cee4b6` also completes its migration/startup
preflight against the additive W1b.4 schema. Its former subscription
reactivation statement fails closed at the monotonic status guard and leaves
terminal rows unchanged, so rolling the writer back requires the documented
license/subscription maintenance window rather than silently resurrecting
access.
W1b.4 remains in-process over the existing Arena records and adds no public
platform HTTP route, provider-neutral purchase API, second writable authority,
or physical extraction.

### Later W1b checkpoints

Later checkpoints must add and prove the remaining operational contract before
service extraction:

1. provider-neutral fulfillment and subscription paths through the same
   authority;
2. service-bearer authentication and the versioned platform HTTP adapter;
3. checkpoint-specific backfill, restart, rollback, concurrency, and
   large-history verification before any physical database or service split.

The native email verification transaction, Stripe order/subscription
orchestration, and provider purchase creation remain explicit later crossings.
Current Stripe and demo-bot handlers are W1b.4 compatibility callers of the
private lifecycle core; they are not provider-neutral platform APIs.
