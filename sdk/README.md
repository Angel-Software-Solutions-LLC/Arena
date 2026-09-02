# AI Battle Arena -- Bot SDK

Build your own battle bot in Python or Node.js!

## Quick Start

### 1. Get an API Key

Open Get Started at [the Arena](https://arena.angel-serv.com) and generate a
token without signing up, or call:

```bash
curl -X POST https://arena.angel-serv.com/api/v1/keys/generate
```

Arena chooses the token and saves its hash and bot record; the full secret is
shown only once. Do not invent your own token string. To equip cosmetics
later, sign in with your Angel account in [My Dashboard](https://arena.angel-serv.com/dashboard/?tab=cosmetics)
and claim the existing bot with that token; paid cosmetics are included with
the Arena subscription held in that account.

### 2. Install the SDK

**Python:**
```bash
cd sdk/python && pip install -e .
```

**Node.js:**
```bash
cd sdk/nodejs && npm install
```

The SDKs are installed from this repository; similarly named PyPI and npm
packages are not published yet. Python 3.10 or newer is supported.

### 3. Create Your Bot

**Python (`my_bot.py`):**
```python
import asyncio, sys
from arena_sdk import ArenaBot

class MyBot(ArenaBot):
    async def on_tick(self, state, nearby, safe_zone):
        enemy = self.closest_enemy(nearby)
        if enemy:
            return self.attack(enemy["id"])
        return self.move_toward(state["position"], safe_zone["center"])

bot = MyBot(sys.argv[1])
bot.set_loadout("sword", {"hp": 5, "speed": 5, "attack": 5, "defense": 5})
asyncio.run(bot.run())
```

Keep `bot.run()` as the long-lived task. Calling `connect()` by itself only
performs the handshake; if the surrounding coroutine then returns, the bot's
WebSocket closes before it can submit actions.

**Node.js (`my_bot.js`):**
```javascript
import ArenaBot from './sdk/nodejs/src/ArenaBot.js';

class MyBot extends ArenaBot {
  async onTick(state, nearby, safeZone) {
    const enemy = this.closestEnemy(nearby);
    if (enemy) return this.attack(enemy.id);
    return this.moveToward(state.position, safeZone.center);
  }
}

const bot = new MyBot(process.argv[2]);
bot.setLoadout('sword', { hp: 5, speed: 5, attack: 5, defense: 5 });
bot.run();
```

### 4. Run It!

```bash
python my_bot.py YOUR_API_KEY
# or
node my_bot.js YOUR_API_KEY
```

## Python SDK Reference

### ArenaBot Class

| Method | Description |
|--------|-------------|
| `ArenaBot(api_key, server_url='wss://arena.angel-serv.com/ws/bot')` | Constructor. |
| `set_loadout(weapon, stats, fallback='aggressive')` | Configure weapon, stat allocation (must total 20), and fallback AI. |
| `run()` | Connect and run the game loop with auto-reconnect. |

### Event Handlers (override these)

| Handler | When | Args |
|---------|------|------|
| `on_tick(state, nearby, safe_zone)` | Every game tick (~100ms). **Must return an action dict.** | Your state, nearby entities, safe zone info. |
| `on_death(death_info)` | You were killed. | `killed_by`, `weapon_used`, `damage`, `respawn_in_seconds`. |
| `on_respawn(respawn_info)` | You respawned. | `position`, `hp`. |
| `on_round_end(round_info)` | Round ended. | `round_number`, `your_stats`, `round_winner`. |
| `on_service_status(status)` | Public broadcast or maintenance status changed. | Full `service_status` replacement snapshot. |

### Action Helpers

| Method | Returns | Description |
|--------|---------|-------------|
| `move_toward(my_pos, target_pos)` | `{action, direction}` | Move toward a position. |
| `move_away(my_pos, threat_pos)` | `{action, direction}` | Move away from a position. |
| `attack(target_id)` | `{action, target}` | Attack a specific enemy by ID. |
| `shove(target_id)` | `{action, target}` | Shove an enemy — 15-unit knockback + 2-tick stun, no damage. 1.5s cooldown. |
| `dodge(direction)` | `{action, direction}` | Dodge roll in a direction (has cooldown). |
| `use_item(item_id)` | `{action, item_id}` | Use a pickup item. |
| `idle()` | `{action}` | Do nothing this tick. |

### Query Helpers

| Method | Returns | Description |
|--------|---------|-------------|
| `closest_enemy(nearby)` | `dict` or `None` | Nearest enemy bot. |
| `lowest_hp_enemy(nearby)` | `dict` or `None` | Enemy with lowest HP. |
| `nearby_pickups(nearby)` | `list[dict]` | Pickups sorted by distance. |

## Node.js SDK Reference

Same API with JS naming conventions:

| Python | Node.js |
|--------|---------|
| `ArenaBot(api_key)` | `new ArenaBot(apiKey)` |
| `set_loadout(...)` | `setLoadout(...)` |
| `on_tick(state, nearby, safe_zone)` | `onTick(state, nearby, safeZone)` |
| `on_death(info)` | `onDeath(info)` |
| `on_respawn(info)` | `onRespawn(info)` |
| `on_round_end(info)` | `onRoundEnd(info)` |
| `on_service_status(status)` | `onServiceStatus(status)` |
| `move_toward(...)` | `moveToward(...)` |
| `move_away(...)` | `moveAway(...)` |
| `shove(id)` | `shove(id)` |
| `closest_enemy(...)` | `closestEnemy(...)` |
| `lowest_hp_enemy(...)` | `lowestHpEnemy(...)` |
| `nearby_pickups(...)` | `nearbyPickups(...)` |
| `use_item(id)` | `useItem(id)` |

## Game State Reference

### Tick State (`your_state`)

| Field | Type | Description |
|-------|------|-------------|
| `position` | `[x, y]` | Your current coordinates. |
| `hp` | `int` | Current hit points. |
| `max_hp` | `int` | Maximum hit points. |
| `weapon` | `string` | Equipped weapon name. |
| `is_alive` | `bool` | Whether you are alive. |
| `kills` | `int` | Kills this round. |
| `deaths` | `int` | Deaths this round. |

### Nearby Entities

Each entity in `nearby_entities` has:

| Field | Type | Description |
|-------|------|-------------|
| `type` | `string` | `"bot"` or `"pickup"`. |
| `id` | `string` | Entity ID (use for `attack()` / `use_item()`). |
| `position` | `[x, y]` | Entity position. |
| `distance` | `float` | Distance from you. |
| `hp` | `int` | Hit points (bots only). |
| `weapon` | `string` | Weapon name (bots only). |
| `pickup_type` | `string` | `"health"`, `"damage_boost"`, etc. (pickups only). |

### Safe Zone

| Field | Type | Description |
|-------|------|-------------|
| `center` | `[x, y]` | Zone center position. |
| `radius` | `float` | Current zone radius (shrinks over time). |

## Weapons

| Weapon | Damage | Range | Cooldown | Special |
|--------|--------|-------|----------|---------|
| Sword | 25 | 2.0 | 0.5s | Cleave (hits nearby enemies) |
| Bow | 15 | 20.0 | 1.0s | Projectile |
| Daggers | 12 | 1.5 | 0.3s | Double strike (20% chance) |
| Shield | 8 | 1.5 | 0.8s | Block (50% damage reduction) |
| Spear | 20 | 3.0 | 0.7s | Knockback |
| Staff | 18 | 15.0 | 1.2s | Area damage (3.0 radius) |

## Strategy Tips

- Aggressive bots rack up kills but die often -- pair with high attack and fast weapons.
- Defensive bots survive longer but score less -- shield and high HP help.
- Pickups can swing fights -- grab health when low, damage boosts before engaging.
- The safe zone shrinks every round -- do not get caught outside it.
- Dodge has a cooldown -- save it for escaping lethal situations.
- Shove is a free utility action (separate cooldown from your weapon) -- use it to push enemies into the zone edge or off pickups.
- Ranged weapons (bow, staff) reward kiting: stay at max range and retreat when enemies close in.
- Stats must total exactly 20. Min 1, max 10 per stat.

## Example Bots

The `sdk/python/examples/` folder includes ready-to-run bots:

| Bot | File | Strategy |
|-----|------|----------|
| Berserker | `berserker.py` | Charges closest enemy with a sword. Pure aggression. |
| Sniper | `sniper.py` | Kites at bow range, retreats when enemies close in. |
| Turtle | `turtle.py` | Defensive shield build, attacks only nearby threats. |
| Hunter | `hunter.py` | Targets the #1 ranked bot using the leaderboard API. |
| Scavenger | `scavenger.py` | Prioritizes pickups, fights only when necessary. |
| Smart Bot | `smart_bot.py` | State machine: switches between aggressive/defensive/scavenge/zone-aware modes. |

Node.js examples in `sdk/nodejs/examples/`: `berserker.js` and `simple-bot.js`. Read them, remix them, and build your own.
