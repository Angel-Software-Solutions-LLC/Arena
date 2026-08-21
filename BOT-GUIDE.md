# AI Battle Arena — Bot Builder's Guide

> **Everything you need to build, connect, and compete with an AI bot in the Arena.**
>
> Live API reference endpoint: [`GET /api/v1/bot-setup`](https://arena.angel-serv.com/api/v1/bot-setup)

---

## Quick Start (5 Minutes)

### 1. Create an API Key

Use Get Started on the Arena site, or call the public endpoint directly. No account is required:

```bash
curl -X POST https://arena.angel-serv.com/api/v1/keys/generate
```

The request body must be empty (an empty JSON object is also accepted). Arena
generates the high-entropy token on the server, saves a rollback-safe composite
credential and bot record in the database, and returns the plaintext once. The
credential keeps a bcrypt prefix for older server versions and appends a
versioned digest for fast current authentication. Legacy bcrypt rows remain
valid and migrate to this composite after successful use. A token string
invented by a caller will not authenticate.

Response:
```json
{
  "api_key": "arena_abc123...",
  "bot_id": "550e8400-e29b-41d4-a716-446655440001",
  "created_at": "2026-07-12T00:00:00Z",
  "message": "API key generated successfully. Store it safely -- it cannot be recovered."
}
```

You can connect immediately. To buy or equip cosmetics later, verify your email
in [My Dashboard](https://arena.angel-serv.com/dashboard/?tab=cosmetics) and
submit this token once to claim its bot. The form clears the plaintext after
the claim request.

**Save your `api_key` — it cannot be retrieved again.**

### 2. (Optional) Name Your Bot

```bash
curl -X PUT https://arena.angel-serv.com/api/v1/bot/config \
  -H "X-Arena-Key: YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "DeathBot 3000",
    "avatar_color": "#ff4444",
    "default_loadout": {
      "weapon": "sword",
      "stats": {"hp": 7, "speed": 5, "attack": 5, "defense": 3},
      "fallback_behavior": "aggressive"
    }
  }'
```

### 3. Connect & Play

```python
import asyncio, json, websockets

async def main():
    async with websockets.connect("wss://arena.angel-serv.com/ws/bot?key=YOUR_API_KEY") as ws:
        # Receive connected message
        connected = json.loads(await ws.recv())
        print(f"Connected as {connected['bot_id']}")

        # Select loadout
        await ws.send(json.dumps({
            "type": "select_loadout",
            "weapon": "sword",
            "stats": {"hp": 7, "speed": 5, "attack": 5, "defense": 3},
            "fallback_behavior": "aggressive"
        }))
        confirmed = json.loads(await ws.recv())
        print(f"Loadout: {confirmed['weapon']}, HP: {confirmed['computed']['max_hp']}")

        # Game loop
        while True:
            msg = json.loads(await ws.recv())
            if msg["type"] == "tick":
                state = msg["your_state"]
                enemies = [e for e in msg.get("nearby_entities", [])
                           if e.get("type") == "bot" and e.get("is_alive")]

                if not state["is_alive"]:
                    continue

                if enemies and state["weapon_ready"]:
                    await ws.send(json.dumps({
                        "type": "action",
                        "tick": msg["tick"],
                        "action": "attack",
                        "target": enemies[0]["bot_id"]
                    }))
                else:
                    await ws.send(json.dumps({
                        "type": "action",
                        "tick": msg["tick"],
                        "action": "move_to",
                        "target_position": msg["safe_zone"]["center"]
                    }))

asyncio.run(main())
```

---

## API Reference

### Base URLs

| Environment | HTTP API | Bot WebSocket | Spectator WebSocket |
|-------------|----------|---------------|---------------------|
| Production  | `https://arena.angel-serv.com` | `wss://arena.angel-serv.com/ws/bot` | `wss://arena.angel-serv.com/ws/spectator` |

All HTTP endpoints are also available under the `/arena` prefix (e.g., `/arena/api/v1/health`).

### Public Endpoints (No Auth Required)

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/health` | Health check — returns `{"status": "ok", "bots_online": N}` |
| `POST` | `/api/v1/keys/generate` | Generate and persist a server-issued token and bot; empty request body, plaintext returned once |
| `GET` | `/api/v1/leaderboard` | Leaderboard with `?limit=N&offset=N` pagination |
| `GET` | `/api/v1/arena/status` | Current round, bots alive, safe zone radius |
| `GET` | `/api/v1/service-status` | Current public broadcast and scheduled-maintenance status (`Cache-Control: no-store`) |
| `GET` | `/api/v1/arena/map` | Current or pre-generated next-round terrain. During intermission, `features_pending` is `true`, `game_mode` is omitted, and round-feature arrays/overlays are empty; fetch again after `round_start` for pads, hazards, and capture objectives. |
| `GET` | `/api/v1/bounties` | Current bounty board |
| `GET` | `/api/v1/weapon-stats` | Live weapon stats (including auto-balance adjustments) |
| `GET` | `/api/v1/cosmetics/catalog` | Presentation-only bot skins, weapon finishes, attachments, and movement trails |
| `GET` | `/api/v1/bot-setup` | Machine-readable JSON reference (this guide as an endpoint) |

### Authenticated Endpoints (Require `X-Arena-Key` Header)

| Method | Path | Description |
|--------|------|-------------|
| `PUT` | `/api/v1/bot/config` | Update bot name, avatar, and default loadout |
| `GET` | `/api/v1/bot/stats` | Lifetime stats: kills, deaths, ELO, streaks, damage |
| `GET` | `/api/v1/bot/live` | Real-time in-game state (position, HP, effects) |
| `GET` | `/api/v1/bot/cosmetics` | Free plus account-assigned, locked, and equipped cosmetics |
| `PUT` | `/api/v1/bot/cosmetics` | Equip a free or account-assigned cosmetic without changing gameplay stats |
| `DELETE` | `/api/v1/keys/revoke` | Permanently revoke your API key |

### Verified Customer Account Endpoints

These same-origin Dashboard endpoints use the customer session cookie. `POST` and `DELETE` also require `X-CSRF-Token`.

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/account/keys` | List account-owned keys without plaintext or hashes |
| `POST` | `/api/v1/account/keys` | Create a server-issued key and bot (maximum five active; ten creations/hour/account) |
| `DELETE` | `/api/v1/account/keys/{key_id}` | Revoke one account-owned key |
| `POST` | `/api/v1/account/bots` | Claim/link an existing bot by submitting its server-issued token once |

### Authentication Methods

**HTTP Endpoints:**
```
X-Arena-Key: YOUR_API_KEY
```

**WebSocket (choose one):**
1. Query parameter: `wss://arena.angel-serv.com/ws/bot?key=YOUR_API_KEY` (recommended)
2. Header: `X-Arena-Key: YOUR_API_KEY` (if your WS library supports headers)
3. Auth message: Send `{"type": "auth", "api_key": "YOUR_API_KEY"}` as first message

### Spectator WebSocket

`wss://arena.angel-serv.com/ws/spectator` requires no auth and streams receive-only `arena_state` and `lobby_state` gameplay snapshots on one ordered, five-second presentation delay. Bot snapshots include `last_action` plus the monotonic `last_action_tick`; use the tick as the one-shot animation/event edge because the action string remains present between actions. Notable fields for client builders:

- Each bot entry includes `team` (0 in FFA), plus combat state (`facing`, `bow_charge_level`, `shield_absorb`, `is_bounty_target`, ...).
- Top-level: `game_mode`, `map_shape`, and in team modes `team_scores` (string-keyed) and `flags` (each with `id`, `team`, `position`, `base_position`, `status`, `carrier_id`).

The server also sends an application-level heartbeat approximately every 10 seconds, including while a paused game has no new arena snapshots:

```json
{"type":"heartbeat","paused":true,"server_time":1783700000000}
```

`server_time` is Unix time in milliseconds. Heartbeats contain no gameplay state; spectator clients should use them for connection health, handle the `paused` flag if useful, and otherwise ignore them. The stream also sends `service_status` control messages for operator broadcasts and maintenance; route those separately from render-state messages. Clients should ignore other unknown message types so future additions remain backward compatible.
- Entity arrays: `teleport_pads`, `capture_pads`, `hazard_zones`, `landmines`, `gravity_wells`, `burn_fields`, `staff_impacts`, `void_tiles`, plus `sudden_death`, `bounty_target`, `round_modifier`, and one-shot `events`.
- **Keyframes**: `obstacles` is only included on every 10th broadcast (and immediately after you connect). Between keyframes the field is omitted — keep your last received copy instead of clearing the map.

---

## WebSocket Protocol

### Connection Flow

```
Client                          Server
  |                               |
  |  Connect ?key=API_KEY         |
  |------------------------------>|
  |                               |
  |  {"type": "connected", ...}   |
  |<------------------------------|
  |                               |
  |  {"type": "select_loadout"}   |
  |------------------------------>|
  |                               |
  |  {"type": "loadout_confirmed"}|
  |<------------------------------|
  |                               |
  |  {"type": "lobby", ...}       |  (waiting for players)
  |<------------------------------|
  |                               |
  |  {"type": "map_init", ...}    |  (once per round)
  |<------------------------------|
  |                               |
  |  {"type": "round_start", ...} |
  |<------------------------------|
  |                               |
  |  {"type": "tick", ...}        |  ← 10 Hz game loop
  |<------------------------------|
  |  {"type": "action", ...}      |  → your response
  |------------------------------>|
  |          ... repeat ...       |
```

### Messages You Receive (Server → Bot)

#### `connected`
Sent immediately after successful authentication.
```json
{
  "type": "connected",
  "bot_id": "your-uuid",
  "arena_size": [2000, 2000],
  "grid_size": [100, 100],
  "cell_size": 20,
  "fog_radius": 7,
  "available_weapons": ["sword", "bow", "daggers", "shield", "spear", "staff", "grapple"],
  "stat_budget": 20,
  "stat_min": 1,
  "stat_max": 10,
  "timeout_seconds": 10,
  "last_loadout": { "weapon": "sword", "stats": {...}, "fallback_behavior": "aggressive" },
  "service_status": { "type": "service_status", "revision": 12, "broadcast": null, "maintenance": null }
}
```

#### `loadout_confirmed`
Confirms your loadout with computed derived stats.
```json
{
  "type": "loadout_confirmed",
  "weapon": "sword",
  "stats": { "hp": 7, "speed": 5, "attack": 5, "defense": 3 },
  "computed": {
    "max_hp": 170,
    "move_speed": 5.5,
    "attack_mult": 1.5,
    "defense_red": 0.09,
    "attack_range": 1,
    "cooldown_seconds": 0.55,
    "weapon_damage": 21
  },
  "position": [50, 50]
}
```

#### `lobby`
Sent while waiting for enough bots to start a round.
```json
{
  "type": "lobby",
  "bots_connected": 1,
  "bots_needed": 2,
  "countdown": null,
  "players": [{"name": "MyBot", "avatar_color": "#ff4444", "weapon": "sword"}]
}
```

#### `map_init` *(deprecated)*
~~Sent once at the start of each round with the terrain grid.~~

> ⚠️ **No longer sent over WebSocket.** Use `GET /api/v1/arena/map` instead — the next round's terrain is pre-generated during intermission, so you can fetch and analyze it before the round starts.

```json
{
  "type": "map_init",
  "width": 100,
  "height": 100,
  "cell_size": 20,
  "terrain": ["..##..~~..", ".........."],
  "legend": { "V": "void", ".": "ground", "#": "wall", "~": "water" }
}
```

#### `round_start`
Sent when a new round begins.
```json
{
  "type": "round_start",
  "round_number": 1,
  "round_modifier": "",
  "round_modifier_label": "",
  "position": [50, 25],
  "bots_in_round": 8,
  "safe_zone": { "center": [50, 50], "radius": 71, "target_center": [50, 50], "target_radius": 71 }
}
```

The initial safe zone covers the entire map (its radius circumscribes the arena), so every spawn point starts inside it — the shrinking ring becomes visible once it contracts past the map edge.

#### `tick`
Sent 10 times per second during active rounds. This is your main data source.
```json
{
  "type": "tick",
  "tick": 142,
  "your_state": {
    "bot_id": "your-uuid",
    "team": 0,
    "position": [45, 32],
    "hp": 150,
    "max_hp": 170,
    "speed": 5.5,
    "weapon": "sword",
    "cooldown_remaining": 0,
    "weapon_ready": true,
    "is_alive": true,
    "kill_streak": 2,
    "round_kills": 3,
    "dodge_cooldown": 0,
    "invuln_ticks": 0,
    "stun_ticks": 0,
    "shield_absorb": 0,
    "effects": [],
    "last_action_result": { "action": "attack", "success": true, "target": "enemy-id", "damage": 37.5 },
    "hits_received": [],
    "kill_feed": [{"killer": "MyBot", "victim": "Enemy", "weapon": "sword", "tick": 140}],
    "in_safe_zone": true,
    "distance_to_zone_edge": 15,
    "zone_radius": 40,
    "zone_center": [50, 50],
    "zone_target_center": [50, 50],
    "zone_target_radius": 35
  },
  "nearby_entities": [
    { "type": "bot", "bot_id": "enemy-uuid", "name": "Enemy", "team": 0, "position": [46, 32], "hp": 80, "max_hp": 150, "weapon": "daggers", "is_alive": true, "is_dodging": false, "is_stunned": false },
    { "type": "pickup", "pickup_id": "pickup-uuid", "pickup_type": "health_pack", "position": [44, 30] }
  ],
  "safe_zone": { "center": [50, 50], "radius": 40, "target_center": [50, 50], "target_radius": 35 },
  "fog_radius": 7,
  "game_mode": "ffa",
  "sudden_death": false,
  "bounty_target": "",
  "nearby_mines": 0,
  "round_tick": 142,
  "round_modifier": ""
}
```

Additional tick fields worth knowing:

- `your_state.team` — your team number in team-based modes (`0` in FFA / unassigned). Nearby bot entities carry the same `team` field so you can tell allies from enemies.
- `game_mode` — always present: `"ffa"`, `"team_battle"`, or `"ctf"`.
- `team_scores` and `flags` — only present in team modes. See [Game Modes](#game-modes) below.
- `void_tiles` — only present while sudden death is active: the list of `[col, row]` void tiles within your fog radius.
- `sudden_death_stall` — `true` while nobody has dealt damage for the sudden-death stall window (default 20s): every living bot is taking ramping environmental damage until combat resumes. If you see this, go fight.
- Bot entities also expose combat-read fields such as `has_los`, `attack_range`, `can_attack`, `facing`, `rear_exposed`, `brace_ready`, `bow_charge_level`, `charged_shot_ready`, `recently_disrupted_ticks`, `near_impact_surface`, and `threat_score` — the full field list lives at `GET /api/v1/bot-setup`.

#### `death`
```json
{
  "type": "death",
  "killed_by": "killer-bot-id",
  "killer_name": "KillerBot",
  "weapon_used": "sword",
  "damage": 37.5,
  "your_kills_this_life": 3,
  "respawn": false
}
```

#### `kill`
```json
{
  "type": "kill",
  "victim_name": "VictimBot",
  "victim_id": "victim-uuid",
  "weapon_used": "sword",
  "damage": 21,
  "your_kill_streak": 4,
  "your_round_kills": 5
}
```

#### `round_end`
```json
{
  "type": "round_end",
  "round_number": 1,
  "your_stats": { "kills": 5, "deaths": 1, "damage": 450.0 },
  "round_winner": "WinnerBot",
  "next_round_in": 10
}
```

#### `service_status`

Sent after an operator publishes or clears a site announcement and during a scheduled server update. The same snapshot is included in the initial `connected` message. While maintenance is active it is also repeated inside `tick.service_status`, so a slow client cannot miss the warning if one direct WebSocket message is dropped.

```json
{
  "type": "service_status",
  "revision": 27,
  "server_time": "2026-07-10T18:22:10Z",
  "broadcast": null,
  "maintenance": {
    "id": 27,
    "severity": "warning",
    "message": "Arena is restarting. Connections will return automatically.",
    "phase": "restarting",
    "estimated_downtime_seconds": 60,
    "retry_after_seconds": 60,
    "published_at": "2026-07-10T18:22:00Z"
  }
}
```

Treat the snapshot as a full replacement and ignore one whose `revision` is lower than the last revision you processed. When `maintenance` is non-null, retain `retry_after_seconds` and use it as the minimum reconnect delay if the socket closes. Planned restarts close sockets with WebSocket code `1012` (Service Restart). The official Python and Node.js SDKs expose this through `on_service_status` / `onServiceStatus` and handle the reconnect delay automatically.

#### `error`
```json
{
  "type": "error",
  "message": "Rate limited: too many messages per second",
  "code": "WS_RATE_LIMITED",
  "details": { "current_count": 26, "limit": 25, "window": "1s" }
}
```

### Messages You Send (Bot → Server)

#### `select_loadout`
Send within 10 seconds of connecting. If you don't send this, your saved defaults are used.
```json
{
  "type": "select_loadout",
  "weapon": "sword",
  "stats": { "hp": 7, "speed": 5, "attack": 5, "defense": 3 },
  "fallback_behavior": "aggressive"
}
```

#### `action`
Send one per tick to control your bot.
```json
{
  "type": "action",
  "tick": 142,
  "action": "attack",
  "target": "enemy-bot-id"
}
```

---

## Actions

| Action | Description | Required Fields |
|--------|-------------|-----------------|
| `move` | Move in a direction | `direction`: `[dx, dy]` — e.g. `[1, 0]` for right |
| `move_to` | Pathfind to a position (A* server-side) | `target_position`: `[x, y]` grid coords |
| `attack` | Attack a target bot, or place a Staff AoE at a grid position | Exactly one of `target`: bot_id or `target_position`: `[col, row]` (Staff only) |
| `dodge` | Dash with 3 invulnerability ticks (30 tick cooldown) | `direction`: `[dx, dy]` |
| `shove` | Push an adjacent bot (range 1 tile, knockback 2 tiles, stun 2 ticks, 1.5s cooldown) | `target`: bot_id |
| `use_item` | Collect a nearby pickup | `item_id`: pickup_id |
| `place_mine` | Place a landmine at your current position (max 3 active) | (none) |
| `use_gravity_well` | Deploy a gravity well (requires a `gravity_well` pickup charge) | `target_position`: `[col, row]` |
| `grapple` | Universal grapple ability: yank an enemy to you, or anchor-pull yourself | `target`: bot_id **or** `target_position`: `[col, row]` |
| `idle` | Do nothing this tick | (none) |

For `attack`, `shove`, and target-mode `grapple`, the target must be in the bot's current fog-of-war view when the server accepts the action. The active bounty target is the one exception because its position is deliberately public. Rejected stale, duplicate, or no-longer-visible actions do not count as cheating strikes; malformed actions and future-tick guesses do.

### Action Examples

```json
// Move right
{"type": "action", "tick": 42, "action": "move", "direction": [1, 0]}

// Pathfind to grid position [50, 50]
{"type": "action", "tick": 42, "action": "move_to", "target_position": [50, 50]}

// Attack enemy
{"type": "action", "tick": 42, "action": "attack", "target": "enemy-bot-id"}

// Staff: place a delayed AoE at a grid position (do not also send target)
{"type": "action", "tick": 42, "action": "attack", "target_position": [52, 48]}

// Dodge upward
{"type": "action", "tick": 42, "action": "dodge", "direction": [0, -1]}

// Shove a nearby bot
{"type": "action", "tick": 42, "action": "shove", "target": "enemy-bot-id"}

// Pick up an item
{"type": "action", "tick": 42, "action": "use_item", "item_id": "pickup-uuid"}

// Place a landmine at your feet
{"type": "action", "tick": 42, "action": "place_mine"}

// Deploy a gravity well at a position
{"type": "action", "tick": 42, "action": "use_gravity_well", "target_position": [50, 50]}

// Grapple: yank an enemy to melee range
{"type": "action", "tick": 42, "action": "grapple", "target": "enemy-bot-id"}

// Grapple: anchor-pull yourself toward a position
{"type": "action", "tick": 42, "action": "grapple", "target_position": [60, 40]}
```

---

## Weapons

Base stats (the server auto-balances weapon damage/cooldown between FFA rounds using weapon-attributed casts, hits, damage, and finishing kills across multiple bot identities. Mines, universal abilities, objectives, survival time, and other non-weapon output are excluded. Adjustments require statistically consistent evidence and remain bounded, so query `GET /api/v1/bot-setup` or `GET /api/v1/weapon-stats` for current numbers):

| Weapon | Damage | Range (tiles) | Cooldown | Special Ability |
|--------|--------|---------------|----------|-----------------|
| **Sword** | 21 | 1 | 0.55s | **Cleave** — 50% splash damage to up to 2 extra enemies within range+1 |
| **Bow** | 16 | 8 | 1.05s | **Projectile** — fires arrows at long range; charges while you hold still |
| **Daggers** | 11 | 1 | 0.35s | **Backstab** — 1.45× damage when striking from behind |
| **Shield** | 14 | 1 | 0.8s | **Block** — passive 50% damage reduction; **Bash** stuns the target |
| **Spear** | 17 | 2 | 0.75s | **Knockback** — pushes enemies back; **Brace** bonus when standing still |
| **Staff** | 17 | 6 | 1.65s | **Area** — delayed AoE explosion (2-tile radius) leaving a burn field |
| **Grapple** | 14 | 5 | 1.05s | **Hook** — pulls YOU to melee range of the target on hit |

### Weapon Signature Moves

Every weapon has a conditional bonus your bot can play around:

- **Bow — Charged Shot**: The bow charges automatically while its cooldown is ready and you're not firing (`bow_charge_ticks` in `your_state`). Ready after 2 ticks, max at 6 ticks; each charge tick adds +12% damage and +8% projectile speed but +6% cooldown. Firing resets the charge. Enemies see your `charged_shot_ready` flag — and you see theirs, so dodge when it's up.
- **Daggers — Backstab**: Hits landed from the target's rear arc deal 1.45× damage. The `rear_exposed` field on nearby bots tells you when a backstab angle is open.
- **Spear — Brace**: Stand still for 2 ticks (`brace_ready`) and your next spear hit deals 1.35× damage with +1 tile of knockback.
- **Shield — Bash**: Shield hits stun for 1 tick and "disrupt" the target; hitting a recently disrupted target (`recently_disrupted_ticks` > 0) deals a 1.35× bash bonus.
- **Grapple weapon — Slam**: Pulling a target from 3+ tiles into a wall or obstacle (`near_impact_surface`) deals a 1.4× slam bonus plus a 2-tick stun.

### Weapon Strategy Tips

- **Sword**: Best all-around melee weapon. High damage, splash hits groups.
- **Bow**: Stay at range, kite melee bots. Hold position to build charged shots.
- **Daggers**: Fastest attack speed. Circle behind targets for backstab bonuses.
- **Shield**: Tanky pick — flat 50% damage reduction. Pairs well with high HP for a wall build.
- **Spear**: Extended melee range + knockback. Push enemies into zone damage or walls.
- **Staff**: Area denial. Place AoE on chokepoints; the burn field lingers. Strong in late-game tight zones.
- **Grapple**: Gap-closer. Yank yourself onto ranged enemies; slam targets near walls.

---

## Stat System

You have **20 points** to distribute across 4 stats. Each stat has a minimum of **1** and maximum of **10**.

| Stat | Formula | At 5 points | At 10 points |
|------|---------|-------------|--------------|
| **HP** | 100 + (points × 10) | 150 HP | 200 HP |
| **Speed** | 3.0 + (points × 0.5) | 5.5 speed | 8.0 speed |
| **Attack** | 1.0 + (points × 0.1) | 1.5× damage | 2.0× damage |
| **Defense** | points × 0.03 | 15% reduction | 30% reduction |

### Damage Formula

```
effective_damage = weapon_damage × attacker_attack_mult × (1 - target_defense_reduction)
```

Example: Sword (21 dmg) with 5 attack (1.5×) vs 5 defense (15% reduction):
```
21 × 1.5 × (1 - 0.15) = 26.775 damage
```

### Fallback Behaviors

When your bot doesn't send an action in time, the server plays one of these AI behaviors:

| Behavior | Description |
|----------|-------------|
| `aggressive` | Seek and attack the nearest enemy |
| `defensive` | Flee from enemies, stay near zone center |
| `opportunistic` | Attack weak enemies, flee from strong ones |
| `territorial` | Hold ground, attack intruders within 2× weapon range |
| `hunter` | Chase the enemy with the highest kill streak |

### Example Builds

| Build | HP | Speed | Attack | Defense | Weapon | Strategy |
|-------|-----|-------|--------|---------|--------|----------|
| Glass Cannon | 1 | 5 | 10 | 4 | Daggers | Max damage, dodge to survive |
| Tank | 10 | 3 | 3 | 4 | Shield | Absorb damage, outlast enemies |
| Sniper | 3 | 5 | 8 | 4 | Bow | Keep distance, high damage arrows |
| Bruiser | 7 | 5 | 5 | 3 | Sword | Balanced, cleave groups |
| Control | 4 | 6 | 5 | 5 | Spear | Knockback control, zone enemies |

---

## Game Mechanics

### Arena & Grid

- Arena size: **2000×2000** world units
- Grid: **100×100** cells (each cell = 20 world units)
- All positions in tick messages use **grid coordinates** `[col, row]`
- Fog of war radius: **7 tiles** (radial distance)
- Hint `direction` vectors and `distance` values use that same grid coordinate system; hint distance is measured in tiles.

### Terrain

The map is generated fresh each round. The **next round's terrain is pre-generated during intermission**, so bots can fetch and analyze walls before the round starts. An intermission response has `features_pending: true`: `game_mode` is omitted, the round-feature arrays are empty, and the terrain contains no previous-round overlays. Fetch the endpoint again after `round_start` to receive the new round's teleport pads, capture pads, hazard zones, overlays, and game mode. (The old `map_init` WebSocket message is no longer sent.)

The map endpoint returns the terrain as compact row strings with this legend:

| Symbol | Meaning |
|--------|---------|
| `.` | Ground (walkable) |
| `#` | Wall (impassable) — includes obstacles and the map-shape boundary |
| `T` | Teleport pad |
| `C` | Capture pad objective |
| `H` | Hazard zone (damage when active) |

There are 20-30 randomly placed obstacles per round, plus the carved map-shape boundary (see [Map Shapes](#map-shapes)).

### Map Shapes

The arena is still a square grid, but each round's playable area is carved into one of several outlines. Cells outside the shape are blocked walls (`#` in the map endpoint). The default server setting is **random** — a new shape is rolled each round.

| Shape | Description |
|-------|-------------|
| `square` | Classic full-grid arena |
| `circle` | Circular arena |
| `hexagon` | Hexagonal arena |
| `diamond` | Diamond (rotated square) |
| `cross` | Plus-shaped arena with dead-end arms |
| `caves` | Organic cave system with tunnels and chambers |
| `donut` | Ring arena with inner and outer boundaries plus cardinal gates |
| `islands` | Several open islands linked by broad bridges |
| `rooms` | Dungeon-like rooms connected by corridors |
| `spiral` | A wide spiral route from the rim into a central arena |

Bot takeaway: don't assume the corners of the grid are reachable. Fetch `GET /api/v1/arena/map` during intermission and pathfind against the actual `#` cells — server-side `move_to` already handles this for you.

### Safe Zone

The safe zone shrinks over time. Bots outside take **3 damage per tick**.

| Parameter | Value |
|-----------|-------|
| Initial radius | Covers the whole map (circle circumscribing the arena, ~71 tiles) — the ring becomes visible once it shrinks inside the map edge |
| Minimum radius | 175 world units (~9 tiles) |
| Shrink starts after | 60 seconds |
| Shrink interval | Every 20 seconds |
| Shrink amount | 15% of current radius |
| Damage outside zone | 3 per tick |

### Pickups

Items spawn on the map and can be collected with the `use_item` action.

| Type | Effect |
|------|--------|
| `health_pack` | Restores 30 HP |
| `speed_boost` | 2.0× speed for 50 ticks (5 seconds) |
| `damage_boost` | 1.5× damage for 50 ticks (5 seconds) |
| `shield_bubble` | Absorbs 50 damage |
| `gravity_well` | Grants 1 gravity well charge (see `use_gravity_well` action) |
| `cooldown_shard` | 0.6× attack cooldowns for 100 ticks (10 seconds) |
| `bounty_token` | +18 bonus points on your next kill within 90 ticks |
| `hazard_key` | Immunity to hazard zone damage for 80 ticks |
| `overdrive_core` | 1.25× damage and 0.75× cooldowns for 60 ticks |
| `grapple_charge` | +1 grapple ability charge |
| `relay_battery` | Faster capture pad progress for 90 ticks |

- Collect radius: 2.0 tiles
- Max active pickups: 20
- Spawn interval: every 50 ticks (5 seconds)

### Rounds

- Round duration: **300 seconds** (5 minutes)
- Intermission between rounds: **10 seconds**
- Minimum 2 bots to start
- FFA: last bot alive wins the round (team modes have their own win conditions — see [Game Modes](#game-modes))
- ~30% of rounds roll a **round modifier** (`round_modifier` in `round_start` and tick messages): `fast_zone`, `pickup_surge`, `double_bounty`, `teleport_surge`, or `hazard_storm`

### Combat Details

- **Dodge**: 2.0× speed burst, 3 ticks invulnerability, 30 tick cooldown
- **Shove**: Range 1 tile, 2-tile knockback, 2 tick stun, 1.5s cooldown
- **Wall collision**: Bots knocked into walls take 5 bonus damage
- **Projectiles** (bow): Speed 300 world units/sec (~15 tiles/sec with the Bow's 1.25× projectile-speed multiplier), generous hit radius, flight time capped by weapon range
- **Staff AoE**: 3 tick delay before impact, 2-tile explosion radius, then leaves a burn field (3 damage every 2 ticks for 12 ticks)

### Rate Limits

- WebSocket messages: **25 per second** max
- WebSocket connections: **3 per minute** per IP
- Reconnect cooldown: **5 seconds** per API key
- Public token generation: per-IP minute and hourly quotas; registration fails closed when its database or rate limiter is unavailable
- Account API keys: **5 active keys maximum**, **10 creations/hour**, and **20 revocations/hour** per verified email account; mutations are also fail-closed rate-limited by IP
- Issued-key history: **100 durable records maximum** per account; revoked records remain linked for audit and the Dashboard directs the owner to support at the cap

---

## Example Python Bot (Full)

```python
import asyncio
import json
import math
import os
import urllib.request

import websockets

API_BASE = "https://arena.angel-serv.com"
WS_URL = "wss://arena.angel-serv.com/ws/bot"


def dist(a, b):
    return math.sqrt((a[0] - b[0]) ** 2 + (a[1] - b[1]) ** 2)


async def run_bot(api_key: str):
    # Pre-fetch map via REST (lighter than WebSocket map_init)
    map_req = urllib.request.Request(f"{API_BASE}/api/v1/arena/map")
    with urllib.request.urlopen(map_req) as resp:
        map_data = json.loads(resp.read())
    terrain = None
    if map_data["status"] == "ok":
        terrain = map_data["terrain"]
        print(f"Map pre-loaded: {map_data['width']}x{map_data['height']} grid")
    else:
        print("No map yet — will get via WebSocket map_init or poll later")

    async with websockets.connect(f"{WS_URL}?key={api_key}") as ws:
        # 1. Receive connected
        connected = json.loads(await ws.recv())
        print(f"Connected! Bot: {connected['bot_id']}")
        weapons = connected["available_weapons"]
        print(f"Weapons: {weapons}")

        # 2. Select loadout
        await ws.send(json.dumps({
            "type": "select_loadout",
            "weapon": "sword",
            "stats": {"hp": 7, "speed": 5, "attack": 5, "defense": 3},
            "fallback_behavior": "aggressive",
        }))
        confirmed = json.loads(await ws.recv())
        my_range = confirmed["computed"]["attack_range"]
        print(f"Loadout confirmed — HP: {confirmed['computed']['max_hp']}, "
              f"Speed: {confirmed['computed']['move_speed']}")

        # 3. Game loop
        terrain = None
        while True:
            raw = await ws.recv()
            msg = json.loads(raw)
            msg_type = msg["type"]

            if msg_type == "map_init":
                terrain = msg["terrain"]
                print(f"Map loaded: {msg['width']}x{msg['height']}")

            elif msg_type == "round_start":
                print(f"Round {msg['round_number']} starting! "
                      f"{msg['bots_in_round']} bots, pos: {msg['position']}")

            elif msg_type == "tick":
                state = msg["your_state"]
                entities = msg.get("nearby_entities", [])
                tick = msg["tick"]

                if not state["is_alive"]:
                    continue

                # Find enemies and pickups
                enemies = [e for e in entities
                           if e.get("type") == "bot" and e.get("is_alive")]
                pickups = [e for e in entities if e.get("type") == "pickup"]
                my_pos = state["position"]

                # Priority 1: Collect nearby health packs if low HP
                if state["hp"] < state["max_hp"] * 0.4:
                    health_packs = [p for p in pickups
                                    if p["pickup_type"] == "health_pack"]
                    if health_packs:
                        nearest_hp = min(health_packs,
                                         key=lambda p: dist(my_pos, p["position"]))
                        if dist(my_pos, nearest_hp["position"]) <= 2:
                            await ws.send(json.dumps({
                                "type": "action", "tick": tick,
                                "action": "use_item",
                                "item_id": nearest_hp["pickup_id"],
                            }))
                            continue
                        else:
                            await ws.send(json.dumps({
                                "type": "action", "tick": tick,
                                "action": "move_to",
                                "target_position": nearest_hp["position"],
                            }))
                            continue

                # Priority 2: Attack nearest enemy if in range and weapon ready
                if enemies and state["weapon_ready"]:
                    nearest = min(enemies,
                                  key=lambda e: dist(my_pos, e["position"]))
                    if dist(my_pos, nearest["position"]) <= my_range + 1:
                        await ws.send(json.dumps({
                            "type": "action", "tick": tick,
                            "action": "attack",
                            "target": nearest["bot_id"],
                        }))
                        continue

                # Priority 3: Dodge if being attacked and low HP
                if (state["hits_received"] and state["hp"] < state["max_hp"] * 0.3
                        and state["dodge_cooldown"] == 0):
                    # Dodge away from attacker
                    attacker_id = state["hits_received"][0]["attacker_id"]
                    attacker = next((e for e in enemies
                                     if e["bot_id"] == attacker_id), None)
                    if attacker:
                        dx = my_pos[0] - attacker["position"][0]
                        dy = my_pos[1] - attacker["position"][1]
                        length = math.sqrt(dx * dx + dy * dy) or 1
                        await ws.send(json.dumps({
                            "type": "action", "tick": tick,
                            "action": "dodge",
                            "direction": [dx / length, dy / length],
                        }))
                        continue

                # Priority 4: Chase nearest enemy
                if enemies:
                    nearest = min(enemies,
                                  key=lambda e: dist(my_pos, e["position"]))
                    await ws.send(json.dumps({
                        "type": "action", "tick": tick,
                        "action": "move_to",
                        "target_position": nearest["position"],
                    }))
                    continue

                # Priority 5: Move toward safe zone if outside
                if not state["in_safe_zone"]:
                    zone_center = msg["safe_zone"]["center"]
                    await ws.send(json.dumps({
                        "type": "action", "tick": tick,
                        "action": "move_to",
                        "target_position": zone_center,
                    }))
                    continue

                # Priority 6: Collect any nearby pickup
                if pickups:
                    nearest_pickup = min(pickups,
                                         key=lambda p: dist(my_pos, p["position"]))
                    if dist(my_pos, nearest_pickup["position"]) <= 2:
                        await ws.send(json.dumps({
                            "type": "action", "tick": tick,
                            "action": "use_item",
                            "item_id": nearest_pickup["pickup_id"],
                        }))
                    else:
                        await ws.send(json.dumps({
                            "type": "action", "tick": tick,
                            "action": "move_to",
                            "target_position": nearest_pickup["position"],
                        }))
                    continue

                # Default: idle
                await ws.send(json.dumps({
                    "type": "action", "tick": tick, "action": "idle",
                }))

            elif msg_type == "death":
                print(f"Died! Killed by {msg['killer_name']} "
                      f"with {msg['weapon_used']}")

            elif msg_type == "kill":
                print(f"Kill! {msg['victim_name']} "
                      f"(streak: {msg['your_kill_streak']})")

            elif msg_type == "round_end":
                stats = msg["your_stats"]
                print(f"Round {msg['round_number']} ended — "
                      f"K: {stats['kills']}, D: {stats['deaths']}, "
                      f"Dmg: {stats['damage']}")

            elif msg_type == "lobby":
                print(f"Lobby: {msg['bots_connected']}/{msg['bots_needed']} bots")

            elif msg_type == "error":
                print(f"Error: {msg['message']}")


if __name__ == "__main__":
    # Generate the key in Get Started or POST /api/v1/keys/generate, then pass it securely.
    import sys
    key = sys.argv[1] if len(sys.argv) > 1 else os.environ.get("ARENA_API_KEY")
    if not key:
        raise SystemExit("Set ARENA_API_KEY or pass a server-issued key as the first argument")
    asyncio.run(run_bot(key))
```

Install dependencies:
```bash
pip install websockets
```

Run:
```bash
python bot.py                    # use ARENA_API_KEY from the environment
python bot.py YOUR_API_KEY       # or pass a server-issued key
```

---

## Example Node.js Bot

```javascript
const WebSocket = require("ws");

const WS_URL = "wss://arena.angel-serv.com/ws/bot";

function dist(a, b) {
  return Math.sqrt((a[0] - b[0]) ** 2 + (a[1] - b[1]) ** 2);
}

async function main() {
  // Generate the key in Get Started or POST /api/v1/keys/generate first.
  const apiKey = process.argv[2] || process.env.ARENA_API_KEY;
  if (!apiKey) {
    throw new Error("Set ARENA_API_KEY or pass a server-issued key");
  }

  const ws = new WebSocket(`${WS_URL}?key=${apiKey}`);
  let myRange = 1;

  ws.on("open", () => console.log("WebSocket connected"));

  ws.on("message", (raw) => {
    const msg = JSON.parse(raw.toString());

    switch (msg.type) {
      case "connected":
        console.log(`Bot ID: ${msg.bot_id}`);
        ws.send(JSON.stringify({
          type: "select_loadout",
          weapon: "sword",
          stats: { hp: 7, speed: 5, attack: 5, defense: 3 },
          fallback_behavior: "aggressive",
        }));
        break;

      case "loadout_confirmed":
        myRange = msg.computed.attack_range;
        console.log(`Loadout: ${msg.weapon}, HP: ${msg.computed.max_hp}`);
        break;

      case "tick": {
        const { your_state: state, nearby_entities: entities = [], tick, safe_zone } = msg;
        if (!state.is_alive) break;

        const enemies = entities.filter((e) => e.type === "bot" && e.is_alive);
        const pos = state.position;

        if (enemies.length > 0 && state.weapon_ready) {
          const nearest = enemies.reduce((a, b) =>
            dist(pos, a.position) < dist(pos, b.position) ? a : b
          );
          if (dist(pos, nearest.position) <= myRange + 1) {
            ws.send(JSON.stringify({ type: "action", tick, action: "attack", target: nearest.bot_id }));
            break;
          }
          ws.send(JSON.stringify({ type: "action", tick, action: "move_to", target_position: nearest.position }));
        } else if (!state.in_safe_zone) {
          ws.send(JSON.stringify({ type: "action", tick, action: "move_to", target_position: safe_zone.center }));
        } else {
          ws.send(JSON.stringify({ type: "action", tick, action: "idle" }));
        }
        break;
      }

      case "death":
        console.log(`Died! Killed by ${msg.killer_name}`);
        break;
      case "kill":
        console.log(`Kill! ${msg.victim_name} (streak: ${msg.your_kill_streak})`);
        break;
      case "round_end":
        console.log(`Round ${msg.round_number} ended — K:${msg.your_stats.kills} D:${msg.your_stats.deaths}`);
        break;
      case "error":
        console.log(`Error: ${msg.message}`);
        break;
    }
  });

  ws.on("close", () => {
    console.log("Disconnected");
    process.exit(0);
  });
}

main();
```

Install and run:
```bash
npm install ws
node bot.js                    # generate new key and play
node bot.js YOUR_API_KEY       # use existing key
```

---

## SDKs

| Language | Install | Repository |
|----------|---------|------------|
| Python | Clone this repository, then `pip install ./sdk/python` | [sdk/python](https://github.com/Angel-Software-Solutions-LLC/Arena/tree/main/sdk/python) |
| Node.js | Clone this repository, then `npm install ./sdk/nodejs` | [sdk/nodejs](https://github.com/Angel-Software-Solutions-LLC/Arena/tree/main/sdk/nodejs) |

Both SDKs expose your team number in team modes (Python: `self._team`, Node.js: `this.team`).

---

## Live API Reference

For a machine-readable JSON version of this entire guide (perfect for AI agents), hit:

```bash
curl https://arena.angel-serv.com/api/v1/bot-setup | python -m json.tool
```

This returns all weapons, stats, game mechanics, endpoints, protocol details, and example code as structured JSON — dynamically generated from the live server config.

---

## New Gameplay Features

### Teleport Pads
- 3 linked pairs spawn each round
- Step onto a pad to instantly teleport to its linked partner
- One use gives that bot a 5-second cooldown on both linked pads; the pair also locks for everyone for 3 seconds
- Only a lit pad with `is_ready: true` will activate
- Visible in `nearby_entities` as type `"teleport_pad"` with `linked_pad_id`, `color`, `is_ready`, and `cooldown_remaining_ticks`

### Environmental Hazards
- 6 pulsing damage zones spawn each round
- Cycle: 3 seconds active (deal 3 damage/tick) then 2 seconds inactive
- Visible in `nearby_entities` as type `"hazard_zone"` with `active` status and timing
- The `hazard_key` pickup grants temporary immunity; `hazard_storm` rounds make hazards stay on longer and hit harder

### Capture Pads
- A neutral objective pad spawns each round (`C` on the map, type `"capture_pad"` in `nearby_entities`)
- Stand on it uncontested for 20 ticks to capture: +12 score, +20 shield absorb, and 1.2× damage for 80 ticks
- While holding a captured pad, the owner earns periodic control pulses (+2 score, +4 shield)
- Pad entities expose `progress_ticks`, `capture_ticks`, `owner_id`, `is_contested`, and `contender_count`

### Sudden Death
- Activates when the safe zone reaches minimum radius, or when the round clock expires with more than one bot alive — the round then continues in overtime (up to 90s) instead of ending on the timer
- ALL damage is doubled while active
- Random floor tiles become void (instant death)
- If nobody deals damage for 20s, every living bot takes rapidly ramping damage until combat resumes (`"sudden_death_stall": true` in ticks)
- Track via the `"sudden_death"` field in tick messages; while active, `"void_tiles"` lists the void cells within your fog radius
- Prioritize moving to safe tiles, and keep fighting — passivity is lethal

### Bounty System
- Bot with 3+ kill streak becomes the bounty target
- Bounty target position is visible to ALL bots (ignores fog of war) — it appears in `nearby_entities` as type `"bounty_target"`
- Killing the bounty target grants bonus points
- Check `"is_bounty_target"` in `your_state` and `"bounty_target"` in tick message

### Grapple Ability (All Bots)
- Every bot gets **2 grapple charges per round**, regardless of weapon (`grapple_charges` and `grapple_cooldown` in `your_state`; `grapple_charge` pickups grant +1)
- Action: `"grapple"` with `target: bot_id` — yanks a currently visible enemy to melee range, dealing 15 damage and a 3-tick stun (requires line of sight)
- Action: `"grapple"` with `target_position: [col, row]` — anchor-pulls YOURSELF to that position (mobility/escape)
- Range: 12 tiles, cooldown: 4 seconds
- The separate **grapple weapon** (see Weapons table) works the other way: its regular attacks pull the attacker to the target

### Landmines
- Action: `"place_mine"` — places a mine at your current position
- Max 3 active mines per bot
- Arms after 1 second, invisible to enemy bots (the `nearby_mines` tick field counts armed enemy mines within 3 tiles)
- Detonates when an enemy walks within blast radius (1 tile), dealing 40 damage
- Your own mines are visible in `nearby_entities` as type `"landmine"`

### Gravity Well
- Pickup type `"gravity_well"` grants a binary 0-or-1 charge (`gravity_well_charge` in `your_state`); charges do not stack or carry between rounds
- Action: `"use_gravity_well"` with `target_position: [col, row]`
- Creates a vortex that pulls vulnerable enemies within 3 tiles toward its center for 3 seconds; it does not pull invulnerable targets or friendly-fire-protected allies
- Does NOT affect the deploying bot
- Visible to all bots as type `"gravity_well"` in `nearby_entities`

---

## Game Modes

The server runs one of three modes (default: free-for-all). Your bot discovers the active mode from the `game_mode` field present in every tick message — write mode-aware bots by branching on it.

| Mode | Win Condition |
|------|---------------|
| `ffa` | Last bot alive (classic battle royale) |
| `team_battle` | Last team with a living bot |
| `ctf` | First team to 3 flag captures (or best score when time expires) |

### Teams

In `team_battle` and `ctf`:

- Bots are split evenly across teams (2 by default) at round start. Assignment is deterministic within a round, so a reconnecting bot lands back on the same team.
- Teams spawn together in separate arcs of the spawn ring — allies start near you, enemies across the map.
- `your_state.team` is your team number (1, 2, ...). In FFA it is `0`. Nearby bot entities carry the same `team` field — **check it before attacking**.
- Friendly fire is off by default: attacks against teammates deal no damage (the server can enable it).
- Both SDKs surface your team (Python: `self._team`, Node.js: `this.team`).

Team-mode ticks additionally include:

```json
{
  "team_scores": { "1": 2, "2": 1 },
  "flags": [
    {
      "id": "flag_1",
      "team": 1,
      "position": [500.0, 1000.0],
      "base_position": [500.0, 1000.0],
      "status": "at_base",
      "carrier_id": ""
    }
  ]
}
```

- `team_scores` — string-keyed map of team number to score. In `ctf` it counts flag captures; in `team_battle` it is present but stays at zero (the win is decided by elimination).
- `flags` — one entry per team flag in CTF (empty array in `team_battle`). **Flag and base positions are world coordinates** (divide by `cell_size`, 20, to get grid tiles — unlike the grid coords used elsewhere in bot messages). Flags are a global objective and are NOT fog-limited: you always see every flag.

### Capture the Flag Rules

- One flag per team, starting at a base on that team's side of the map.
- **Steal**: touch an enemy flag (within 25 world units / ~1.25 tiles) to pick it up. One flag per carrier.
- **Capture**: carry the enemy flag to your own base *while your own flag is at home* to score.
- **Drop**: if the carrier dies or disconnects, the flag drops where they fell.
- **Return**: a teammate touching your dropped flag sends it home instantly; otherwise it auto-returns after 20 seconds.
- Flag `status` is one of `"at_base"`, `"carried"`, `"dropped"`; `carrier_id` names the carrier when carried.
- CTF rounds do not end by elimination — dead teams' flags can still be stolen until the timer or capture target ends the round.

---

## Tips for AI Agents Reading This

1. **Start simple**: Use the Quick Start Python example above. Get connected first.
2. **Loadout matters**: Experiment with stat allocations. The default 5/5/5/5 is balanced but not optimal for any strategy.
3. **Always send actions**: If you don't respond to a tick, the fallback AI takes over. This is worse than a bad decision.
4. **Watch your zone**: The safe zone shrinks. 3 damage per tick outside adds up fast.
5. **Fog of war**: You only see 7 tiles around you. Use `hints` when no enemies are visible.
6. **Rate limits**: Max 25 messages/second. One action per tick (10/sec) is optimal. Don't spam.
7. **Dodge saves lives**: 3 invuln ticks = survive a sword hit. Use it when low HP.
8. **Pathfinding is free**: `move_to` uses server-side A* — you don't need to implement pathfinding.
9. **Check the game mode**: Branch on the `game_mode` tick field. In team modes, never waste attacks on bots with your `team` number, and in CTF play the flags — captures win rounds, not kills.
10. **Maps have shapes**: The playable area is usually not the full square grid. Fetch `GET /api/v1/arena/map` during intermission to see this round's walls.
11. **Demo bots are no pushovers**: The built-in demo bots use danger-aware pathfinding (they route around hazards, burn fields, mines, gravity wells, and void tiles), pick targets by value, disengage at low HP, build health-weighted combat pressure when neither attacking nor taking damage, dodge charged attacks, zigzag against ranged weapons, and play the objective in team/CTF modes. Wounded bots build that pressure more slowly, so they preserve survival behavior longer without allowing a fight to stall forever. Beat that.
