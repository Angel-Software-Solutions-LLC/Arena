package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"arena-server/internal/config"
	"arena-server/internal/game"
)

// The bot-setup reference depends only on config (immutable after startup)
// and weapon balance scales (change at most once per round via auto-balance
// plus live admin edits), yet it was rebuilt — hundreds of nested map
// literals plus a full JSON encode — on every request of a publicly
// advertised endpoint. 60s matches the established weapon-stats staleness
// budget; llms.txt promises "current effective values", so avoid longer TTLs.
const botSetupCacheTTL = time.Minute

var botSetupCache = newResponseCache(botSetupCacheTTL, 10*time.Second, time.Now)

// BotSetup returns a handler for GET /api/v1/bot-setup.
// This is a public endpoint (no auth required) that returns everything an AI
// agent needs to build and connect a bot.
func BotSetup() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		botSetupCache.Serve(w, r, "bot-setup", func(context.Context) ([]byte, error) {
			return json.Marshal(buildBotSetupResponse())
		}, "failed to build bot setup reference", http.StatusInternalServerError)
	}
}

func buildBotSetupResponse() map[string]interface{} {
	{
		c := &config.C

		// ── Weapons (dynamic from game.WeaponConfigs) ──────────────
		weaponNames := game.GetAvailableWeapons()
		sort.Strings(weaponNames)
		weapons := make([]map[string]interface{}, 0, len(weaponNames))
		for _, name := range weaponNames {
			wc := game.GetWeaponConfig(name)
			weapons = append(weapons, map[string]interface{}{
				"name":            wc.Name,
				"damage":          wc.Damage,
				"range_tiles":     wc.GridRange,
				"cooldown_secs":   wc.Cooldown,
				"special_ability": wc.Special,
				"description":     weaponDescription(wc),
			})
		}

		resp := map[string]interface{}{
			"api_base_url":            "https://arena.angel-serv.com",
			"websocket_url":           "wss://arena.angel-serv.com/ws/bot",
			"spectator_websocket_url": "wss://arena.angel-serv.com/ws/spectator",

			// ── Getting Started ─────────────────────────────────
			"getting_started": []map[string]interface{}{
				{"step": 1, "title": "Generate an API Key", "description": "POST an empty JSON object to /api/v1/keys/generate. No account is required. Arena creates the bot and stores only the key hash; the plaintext key is returned once."},
				{"step": 2, "title": "Configure Your Bot (optional)", "description": "PUT to /api/v1/bot/config with X-Arena-Key header to set your bot name, avatar color, and default loadout."},
				{"step": 3, "title": "Claim Your Bot (optional)", "description": "Sign in with a verified email at /dashboard/, then prove the generated key once through POST /api/v1/account/bots. Claiming links the existing bot to your account for cosmetics; it does not replace the key."},
				{"step": 4, "title": "Fetch the Map (optional)", "description": "GET /api/v1/arena/map to pre-fetch terrain via REST. During intermission, features_pending is true: terrain and shape are ready, game_mode is omitted, and feature arrays/overlays are empty until round_start. Fetch again after round_start for pads, hazards, and objectives."},
				{"step": 5, "title": "Connect via WebSocket", "description": "Connect to wss://arena.angel-serv.com/ws/bot?key=YOUR_API_KEY — you will receive a 'connected' message with arena info."},
				{"step": 6, "title": "Select Loadout", "description": "Send a 'select_loadout' message choosing your weapon, stat allocation, and fallback behavior. You have 10 seconds."},
				{"step": 7, "title": "Receive Ticks", "description": "The server sends 'tick' messages at " + fmt.Sprintf("%d", c.TickRate) + " Hz with your state, nearby entities, and safe zone info."},
				{"step": 8, "title": "Send Actions", "description": "Each tick, send an 'action' message with your chosen action (move, attack, dodge, etc). One action per tick."},
			},

			// ── Authentication ──────────────────────────────────
			"authentication": map[string]interface{}{
				"generate_key": map[string]interface{}{
					"method":      "POST",
					"path":        "/api/v1/keys/generate",
					"description": "Create an unowned server-issued API key and bot. No account is required; submit an empty JSON object. The plaintext key is returned once and never stored by Arena.",
					"response_example": map[string]interface{}{
						"api_key":    "arena_abc123...",
						"bot_id":     "uuid-here",
						"created_at": "2026-07-12T12:00:00Z",
					},
				},
				"claim_bot": map[string]interface{}{
					"method":          "POST",
					"path":            "/api/v1/account/bots",
					"description":     "After verified-email sign-in, prove a generated key once to link its existing bot to the account for cosmetics. Requires X-CSRF-Token.",
					"request_example": map[string]string{"api_key": "arena_abc123..."},
				},
				"usage": map[string]interface{}{
					"websocket":  "Connect with ?key=YOUR_API_KEY query parameter",
					"http":       "Include X-Arena-Key: YOUR_API_KEY header",
					"ws_message": "Or send {\"type\": \"auth\", \"api_key\": \"YOUR_API_KEY\"} as first WebSocket message",
				},
			},

			// ── Endpoints ───────────────────────────────────────
			"endpoints": map[string]interface{}{
				"public": []map[string]interface{}{
					{"method": "POST", "path": "/api/v1/keys/generate", "description": "Generate a database-backed API key and bot; no account required, plaintext returned once"},
					{"method": "GET", "path": "/api/v1/health", "description": "Health check — returns status and online bot count"},
					{"method": "GET", "path": "/api/v1/leaderboard", "description": "Get leaderboard (supports ?limit=N&offset=N)"},
					{"method": "GET", "path": "/api/v1/arena/status", "description": "Current arena status: round number, bots alive, safe zone"},
					{"method": "GET", "path": "/api/v1/arena/map", "description": "Current terrain plus active round features. During intermission, features_pending is true and only next-round terrain/shape are final; game_mode is omitted, feature arrays are empty, and overlays are absent until round_start."},
					{"method": "GET", "path": "/api/v1/cosmetics/catalog", "description": "Public no-pay-to-win cosmetic catalog and checkout readiness"},
					{"method": "GET", "path": "/api/v1/bot-setup", "description": "This endpoint — full bot-building reference"},
				},
				"authenticated": []map[string]interface{}{
					{"method": "POST", "path": "/api/v1/account/bots", "description": "Claim an existing generated key and bot for cosmetics", "auth": "verified customer session + X-CSRF-Token"},
					{"method": "GET", "path": "/api/v1/account/keys", "description": "List account-owned API keys without secret material", "auth": "verified customer session"},
					{"method": "POST", "path": "/api/v1/account/keys", "description": "Create one account-owned API key and bot (maximum 5 active)", "auth": "verified customer session + X-CSRF-Token"},
					{"method": "DELETE", "path": "/api/v1/account/keys/{key_id}", "description": "Revoke one account-owned API key", "auth": "verified customer session + X-CSRF-Token"},
					{"method": "PUT", "path": "/api/v1/bot/config", "description": "Update bot name, avatar color, and default loadout", "auth": "X-Arena-Key header"},
					{"method": "GET", "path": "/api/v1/bot/stats", "description": "Get your bot's lifetime stats (kills, deaths, ELO, etc)", "auth": "X-Arena-Key header"},
					{"method": "GET", "path": "/api/v1/bot/live", "description": "Get your bot's real-time in-game state", "auth": "X-Arena-Key header"},
					{"method": "GET", "path": "/api/v1/bot/cosmetics", "description": "List owned, locked, and equipped cosmetics", "auth": "X-Arena-Key header"},
					{"method": "PUT", "path": "/api/v1/bot/cosmetics", "description": "Equip one owned cosmetic by slot", "auth": "X-Arena-Key header"},
					{"method": "DELETE", "path": "/api/v1/keys/revoke", "description": "Revoke your API key (permanent)", "auth": "X-Arena-Key header"},
				},
				"websocket": []map[string]interface{}{
					{"path": "/ws/bot", "description": "Bot game connection — send actions, receive ticks", "auth": "?key=YOUR_API_KEY query param"},
					{"path": "/ws/spectator", "description": "Read-only spectator feed — ordered lobby and arena states with a five-second presentation delay", "auth": "none"},
				},
			},

			// ── WebSocket Protocol ──────────────────────────────
			"websocket_protocol": map[string]interface{}{
				"reconnect": map[string]interface{}{
					"grace_seconds":          c.WSReconnectGraceSecs,
					"active_state_preserved": true,
					"recommended_backoff":    "1s, 2s, 4s, then cap at 30s; honor details.retry_after",
					"note":                   "During grace the bot remains targetable but cannot act. Protocol locks, bans, and admin kicks are not resumable.",
				},
				"message_rate_limit": map[string]interface{}{
					"messages_per_second": c.WSMaxMessagesPerSec,
					"note":                "A short overflow is dropped and reported without a strike; sustained floods escalate to protocol strikes.",
				},
				"connection_flow": []string{
					"1. Connect to wss://arena.angel-serv.com/ws/bot?key=YOUR_API_KEY",
					"2. Receive 'connected' message with arena config and available weapons",
					"3. Send 'select_loadout' message with weapon, stats, and fallback behavior",
					"4. Receive 'loadout_confirmed' message with your derived stats",
					"5. Wait for 'round_start' or 'lobby' message",
					"6. Game loop: receive 'tick' messages, send 'action' messages",
				},
				"inbound_messages": map[string]interface{}{
					"connected": map[string]interface{}{
						"description": "Sent immediately after authentication succeeds",
						"fields":      "bot_id, arena_size, grid_size, cell_size, fog_radius, available_weapons, stat_budget, stat_min, stat_max, timeout_seconds, last_loadout",
					},
					"loadout_confirmed": map[string]interface{}{
						"description": "Confirms your loadout selection with computed derived stats",
						"fields":      "weapon, stats{hp,speed,attack,defense}, computed{max_hp,move_speed,attack_mult,defense_red,attack_range,cooldown_seconds,weapon_damage}, position",
					},
					"lobby": map[string]interface{}{
						"description": "Sent while waiting for enough bots to start a round",
						"fields":      "bots_connected, bots_needed, countdown, players[]",
					},
					"map_init": map[string]interface{}{
						"description": "DEPRECATED — no longer sent over WebSocket. Use GET /api/v1/arena/map instead (pre-generated during intermission, available before round start).",
						"fields":      "width, height, cell_size, terrain (compact string grid), legend",
						"status":      "deprecated",
					},
					"round_start": map[string]interface{}{
						"description": "Sent when a new round begins",
						"fields":      "round_number, round_modifier, round_modifier_label, position, bots_in_round, safe_zone",
					},
					"tick": map[string]interface{}{
						"description": fmt.Sprintf("Sent every tick (%d times per second) with full game state visible to your bot", c.TickRate),
						"fields":      "tick, round_tick, round_modifier, your_state{bot_id,position,hp,max_hp,speed,weapon,cooldown_remaining,weapon_ready,is_alive,kill_streak,round_kills,dodge_cooldown,invuln_ticks,stun_ticks,shield_absorb,recently_disrupted_ticks,brace_ready,bow_charge_ticks,bow_charge_level,charged_shot_ready,hazard_key_active,hazard_key_ticks,relay_battery_active,relay_battery_ticks,effects,last_action_result,hits_received,kill_feed,in_safe_zone,distance_to_zone_edge,zone_radius,zone_center,grapple_charges,grapple_cooldown,bounty_token_bonus}, nearby_entities[{type,id,name,position,hp,max_hp,weapon,is_alive,has_los,attack_range,can_attack,threat_score,recently_disrupted_ticks,brace_ready,bow_charge_level,charged_shot_ready,rear_exposed,near_impact_surface} | {type,pickup_id,pickup_type,position} | {type:'teleport_pad'|'hazard_zone'|'burn_field'|'capture_pad',position,progress_ticks,capture_ticks,owner_id,is_contested,contender_count,on_ticks,off_ticks,damage_per_tick,next_control_pulse_ticks,...}], safe_zone{center,radius,target_center,target_radius}, fog_radius, hints[{hint_type,direction,distance}], nearby_mines",
					},
					"death": map[string]interface{}{
						"description": "Sent when your bot dies",
						"fields":      "killed_by, killer_name, weapon_used, damage, your_kills_this_life, respawn",
					},
					"kill": map[string]interface{}{
						"description": "Sent when your bot kills another bot",
						"fields":      "victim_name, victim_id, weapon_used, damage, your_kill_streak, your_round_kills",
					},
					"round_end": map[string]interface{}{
						"description": "Sent when the round ends",
						"fields":      "round_number, your_stats{kills,deaths,damage}, round_winner, next_round_in",
					},
					"error": map[string]interface{}{
						"description": "Sent on protocol errors, rate limiting, or invalid actions",
						"fields":      "message, code, details",
					},
					"kick": map[string]interface{}{
						"description": "Sent when your bot is kicked (connection will close)",
						"fields":      "reason",
					},
				},
				"outbound_messages": map[string]interface{}{
					"select_loadout": map[string]interface{}{
						"description": "Send after receiving 'connected' to choose your loadout",
						"example": map[string]interface{}{
							"type":              "select_loadout",
							"weapon":            "sword",
							"stats":             map[string]int{"hp": 7, "speed": 5, "attack": 5, "defense": 3},
							"fallback_behavior": "aggressive",
						},
					},
					"action": map[string]interface{}{
						"description": "Send each tick to control your bot",
						"example": map[string]interface{}{
							"type":   "action",
							"tick":   42,
							"action": "attack",
							"target": "target-bot-id",
						},
					},
					"taunt": map[string]interface{}{
						"description": fmt.Sprintf("Optional cosmetic emote shown as a speech bubble to spectators (never to other bots). Does not consume your per-tick action. Cooldown %gs; extra taunts are silently dropped. Valid emotes: %v", c.TauntCooldownSecs, game.TauntEmoteKeys()),
						"enabled":     c.TauntsEnabled,
						"example": map[string]interface{}{
							"type":  "taunt",
							"emote": "gg",
						},
					},
				},
			},

			// ── Actions ─────────────────────────────────────────
			"actions": []map[string]interface{}{
				{"name": "move", "description": "Move in a direction (dx, dy normalized)", "fields": map[string]string{"direction": "[dx, dy] — e.g. [1, 0] for right, [0, -1] for up"}},
				{"name": "move_to", "description": "Pathfind to a target position (server handles A* pathfinding)", "fields": map[string]string{"target_position": "[x, y] in grid coordinates"}},
				{"name": "attack", "description": "Attack using exactly one aim mode: target for a currently visible bot in weapon range, or target_position for a Staff delayed AoE at a specific tile. The public bounty target is the visibility exception. Sending both aim modes is rejected. Bow users may optionally set charged=true to spend stored charge.", "fields": map[string]string{"target": "bot_id of the target", "charged": "optional bow-only flag for charged shots", "target_position": "staff cast location in grid coordinates (instead of target)"}},
				{"name": "dodge", "description": fmt.Sprintf("Dash in a direction with %d ticks of invulnerability (cooldown: %d ticks)", c.DodgeInvulnTicks, c.DodgeCooldownTicks), "fields": map[string]string{"direction": "[dx, dy] — direction to dodge toward"}},
				{"name": "shove", "description": fmt.Sprintf("Push a currently visible nearby bot away (range: %.1f tiles, knockback: %.1f, stun: %d ticks, cooldown: %.1fs)", c.ShoveRange, c.ShoveKnockback, c.ShoveStunTicks, c.ShoveCooldown), "fields": map[string]string{"target": "bot_id of the target"}},
				{"name": "use_item", "description": "Pick up a nearby item (must be within collect radius)", "fields": map[string]string{"item_id": "pickup_id of the item"}},
				{"name": "idle", "description": "Do nothing this tick", "fields": map[string]string{}},
				{"name": "place_mine", "description": "Place a landmine at current position (max 3 per bot, arms after 1 second, invisible to enemies)", "fields": map[string]string{}},
				{"name": "use_gravity_well", "description": "Deploy a gravity well at target position (requires gravity_well pickup charge)", "fields": map[string]string{"target_position": "[x, y] in grid coordinates"}},
				{"name": "grapple", "description": "Universal ability: either yank a currently visible target bot within 12 tiles or anchor-pull yourself to a target_position. The public bounty target is the visibility exception. 2 charges per round, 4s cooldown, 15 damage on enemy pulls, 3-tick stun.", "fields": map[string]string{"target": "bot_id of the target (enemy pull mode)", "target_position": "[x, y] anchor position (self-pull mode)"}},
			},

			// ── Weapons (from game.WeaponConfigs) ───────────────
			"weapons": weapons,

			// ── Stats System ────────────────────────────────────
			"stats": map[string]interface{}{
				"budget":             c.StatBudget,
				"min_per_stat":       c.StatMin,
				"max_per_stat":       c.StatMax,
				"stat_names":         []string{"hp", "speed", "attack", "defense"},
				"default_allocation": map[string]int{"hp": 5, "speed": 5, "attack": 5, "defense": 5},
				"formulas": map[string]interface{}{
					"max_hp":            fmt.Sprintf("%.0f + (hp_points * %.0f) — e.g. 5 points = %.0f HP", c.StatHPBase, c.StatHPPerPoint, c.StatHPBase+5*c.StatHPPerPoint),
					"move_speed":        fmt.Sprintf("%.1f + (speed_points * %.1f) — e.g. 5 points = %.1f speed", c.StatSpeedBase, c.StatSpeedPerPoint, c.StatSpeedBase+5*c.StatSpeedPerPoint),
					"attack_multiplier": fmt.Sprintf("%.1f + (attack_points * %.1f) — e.g. 5 points = %.1fx damage", c.StatAttackBase, c.StatAttackPerPoint, c.StatAttackBase+5*c.StatAttackPerPoint),
					"defense_reduction": fmt.Sprintf("defense_points * %.2f — e.g. 5 points = %.0f%% damage reduction", c.StatDefensePerPoint, 5*c.StatDefensePerPoint*100),
					"damage_formula":    "weapon_damage * attack_multiplier * (1 - target_defense_reduction)",
				},
				"fallback_behaviors":   []string{"aggressive", "defensive", "opportunistic"},
				"fallback_description": "After your bot has acted, the server can run this AI behavior during a brief missed-action safety window; prolonged silence remains subject to the activity timeout.",
			},

			// ── Game Mechanics ───────────────────────────────────
			"game_mechanics": map[string]interface{}{
				"tick_rate":      c.TickRate,
				"arena_size":     []float64{c.ArenaWidth, c.ArenaHeight},
				"grid_cell_size": c.PathfindingCellSize,
				"fog_of_war": map[string]interface{}{
					"radius_tiles": c.FogRadius,
					"description":  "You can only see entities within this radius (in grid tiles). The server sends hints for distant bots when none are visible; hint distance is also measured in grid tiles.",
				},
				"safe_zone": map[string]interface{}{
					"initial_radius":       c.ZoneInitialRadius,
					"min_radius":           c.ZoneMinRadius,
					"shrink_delay_secs":    c.ZoneShrinkDelay,
					"shrink_interval_secs": c.ZoneShrinkInterval,
					"shrink_percent":       c.ZoneShrinkPercent,
					"damage_per_tick":      c.ZoneDamagePerTick,
					"description":          "The safe zone shrinks over time. Bots outside take damage every tick. Stay inside!",
				},
				"pickups": map[string]interface{}{
					"types": []map[string]interface{}{
						{"type": "health_pack", "effect": fmt.Sprintf("Restores %.0f HP", c.PickupHealthAmount)},
						{"type": "speed_boost", "effect": fmt.Sprintf("%.1fx speed for %d ticks", c.PickupSpeedBoostMult, c.PickupSpeedBoostTicks)},
						{"type": "damage_boost", "effect": fmt.Sprintf("%.1fx damage for %d ticks", c.PickupDamageBoostMult, c.PickupDamageBoostTicks)},
						{"type": "shield_bubble", "effect": fmt.Sprintf("Absorbs %.0f damage", c.PickupShieldBubbleHP)},
						{"type": "gravity_well", "effect": "Grants 1 gravity well charge (deploy with use_gravity_well action)"},
						{"type": "cooldown_shard", "effect": fmt.Sprintf("Reduces weapon, dodge, shove, and grapple cooldowns to %.0f%% for %d ticks", c.PickupCooldownShardMult*100, c.PickupCooldownShardTicks)},
						{"type": "bounty_token", "effect": fmt.Sprintf("Stores +%d bonus score on your next kill for %d ticks", c.PickupBountyTokenPoints, c.PickupBountyTokenTicks)},
						{"type": "hazard_key", "effect": fmt.Sprintf("Hazard immunity for %d ticks. Negates hazard zones and burn fields, and doubles capture-pad progress while active", c.PickupHazardKeyTicks)},
						{"type": "overdrive_core", "effect": fmt.Sprintf("%.2fx damage and %.0f%% cooldowns for %d ticks", c.PickupOverdriveDamageMult, c.PickupOverdriveCooldownMult*100, c.PickupOverdriveTicks)},
						{"type": "grapple_charge", "effect": fmt.Sprintf("Grants +%d grapple charge and immediately clears grapple cooldown", c.PickupGrappleChargeAmount)},
						{"type": "relay_battery", "effect": fmt.Sprintf("Adds +%d capture progress per tick for %d ticks while contesting capture pads", c.PickupRelayBatteryBonusProgress, c.PickupRelayBatteryTicks)},
					},
					"collect_radius_tiles": c.PickupCollectRadius,
					"spawn_interval_ticks": c.PickupSpawnIntervalTicks,
					"max_active":           c.PickupMaxActive,
					"pickup_surge_note":    fmt.Sprintf("During Pickup Surge rounds, spawn cadence accelerates to %.0f%% of normal", c.RoundModifierPickupSurgeIntervalMult*100),
				},
				"rounds": map[string]interface{}{
					"duration_secs":     c.RoundDuration,
					"intermission_secs": c.IntermissionTime,
					"lobby_countdown":   c.LobbyCountdown,
					"min_bots_to_start": c.MinBotsToStart,
				},
				"terrain": map[string]interface{}{
					"types":       map[string]string{"V": "void (impassable)", ".": "ground (walkable)", "#": "wall (impassable)", "~": "water (impassable)"},
					"obstacles":   fmt.Sprintf("%d-%d randomly placed per round", c.ObstacleCountMin, c.ObstacleCountMax),
					"pathfinding": "Server provides A* pathfinding via move_to action. Or use move for direct movement.",
					"rest_api":    "GET /api/v1/arena/map — fetch the terrain grid over REST. The map is pre-generated during intermission so bots can analyze it before the next round starts.",
				},
				"combat": map[string]interface{}{
					"dodge":     fmt.Sprintf("Speed x%.1f, %d invulnerability ticks, %d tick cooldown", c.DodgeSpeedMult, c.DodgeInvulnTicks, c.DodgeCooldownTicks),
					"shove":     fmt.Sprintf("Range %.1f tiles, knockback %.1f, stun %d ticks, cooldown %.1fs", c.ShoveRange, c.ShoveKnockback, c.ShoveStunTicks, c.ShoveCooldown),
					"knockback": fmt.Sprintf("Wall collision deals %.0f bonus damage", c.KnockbackWallDamage),
				},
				"new_features": map[string]interface{}{
					"teleport_pads":           "3 linked pairs spawn each round. Nearby pad entities expose is_ready and cooldown_remaining_ticks so bots can avoid locked pads. During teleport_surge rounds, pads re-arm much faster.",
					"capture_pad":             fmt.Sprintf("A neutral objective pad spawns each round. Stand on it uncontested for %d ticks to capture it, gain +%d score, %.0f shield, and %.1fx damage for %d ticks. While the pad is cooling down, the owner can hold it uncontested for a control pulse every %d ticks worth +%d score and %.0f shield. Capture pads expose progress_ticks, capture_ticks, owner_id, is_contested, contender_count, is_ready, cooldown_remaining_ticks, and next_control_pulse_ticks.", c.CapturePadCaptureTicks, c.CapturePadScoreBonus, c.CapturePadShieldBonus, c.CapturePadDamageBoostMult, c.CapturePadEffectTicks, c.CapturePadControlPulseTicks, c.CapturePadControlPulseScore, c.CapturePadControlPulseShield),
					"combat_reads":            "Bots now receive brace_ready, bow_charge_ticks, bow_charge_level, charged_shot_ready, recently_disrupted_ticks, rear_exposed, and near_impact_surface so they can reason about spear braces, charged bow shots, backstabs, shield bashes, and grapple slams.",
					"environmental_hazards":   "6 pulsing damage zones placed around the arena. Nearby hazard entities expose active, on_ticks, off_ticks, tick_counter, and damage_per_tick. During hazard_storm rounds they stay active longer, recover faster, and hit harder.",
					"sudden_death":            "Activates when the safe zone reaches minimum radius. Random tiles become void (instant death). Keep moving!",
					"bounty_system":           "Consecutive round winners build a public bounty board. The live bounty target is exposed in ticks, and the full board is available via GET /api/v1/bounties.",
					"special_round_modifiers": "Occasional rounds roll a modifier and expose it as round_modifier in round_start/tick. fast_zone accelerates the safe zone, pickup_surge spawns pickups faster, double_bounty doubles bounty-target claim rewards, teleport_surge dramatically shortens teleporter re-arm time, and hazard_storm makes hazard zones pulse faster and hit harder.",
					"landmines":               "Use the place_mine action to plant a landmine at your current position. Max 3 per bot. Mines arm after 1 second and punish choke points, teleporter lanes, and retreat paths.",
					"gravity_well":            "Pick up a gravity_well pickup to gain 1 charge. Use the use_gravity_well action to deploy it at a target position. Pulls nearby enemies toward its center for 3 seconds.",
					"grappling_hook":          "Grapple is a universal ability ALL bots get (not just a weapon). Every bot starts with 2 grapple charges per round. Use the 'grapple' action with a target bot_id to yank an enemy within 12 tiles, or use target_position to anchor-pull yourself to a valid landing. Cooldown 4s, 15 damage on enemy pulls, 3-tick stun. The grapple weapon still exists as a separate loadout.",
				},
			},

			// ── SDKs ────────────────────────────────────────────
			"sdks": map[string]interface{}{
				"python": map[string]interface{}{
					"install": "pip install arena-sdk",
					"repo":    "https://github.com/Angel-Software-Solutions-LLC/Arena/tree/main/sdk/python",
				},
				"nodejs": map[string]interface{}{
					"install": "npm install @arena/sdk",
					"repo":    "https://github.com/Angel-Software-Solutions-LLC/Arena/tree/main/sdk/nodejs",
				},
			},

			// ── Example Bot ─────────────────────────────────────
			"example_bot_python": `import asyncio, json, os, websockets

API_BASE = "https://arena.angel-serv.com"
WS_URL = "wss://arena.angel-serv.com/ws/bot"

async def main():
    # Step 1: Use a saved key, or generate one without creating an account.
    import urllib.request
    api_key = os.environ.get("ARENA_API_KEY")
    if not api_key:
        key_req = urllib.request.Request(
            f"{API_BASE}/api/v1/keys/generate",
            data=b"{}",
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        with urllib.request.urlopen(key_req) as resp:
            generated = json.loads(resp.read())
        api_key = generated["api_key"]
        print("Generated one-time API key (copy it now; Arena cannot recover it):")
        print(api_key)
        print("Save this value as ARENA_API_KEY before closing this terminal.")

    # Step 2: Pre-fetch map via REST (optional, available before round_start)
    map_req = urllib.request.Request(f"{API_BASE}/api/v1/arena/map")
    with urllib.request.urlopen(map_req) as resp:
        map_data = json.loads(resp.read())
    if map_data["status"] == "ok":
        print(f"Map loaded: {map_data['width']}x{map_data['height']} grid")
        terrain = map_data["terrain"]  # list of row strings: '.' = ground, '#' = wall
    else:
        print("No map yet (between rounds), retry arena/map after round_start")
        terrain = None

    # Step 3: Connect WebSocket
    async with websockets.connect(f"{WS_URL}?key={api_key}") as ws:
        # Receive connected message
        connected = json.loads(await ws.recv())
        print(f"Connected! Bot ID: {connected['bot_id']}")

        # Step 3: Select loadout
        await ws.send(json.dumps({
            "type": "select_loadout",
            "weapon": "sword",
            "stats": {"hp": 7, "speed": 5, "attack": 5, "defense": 3},
            "fallback_behavior": "aggressive"
        }))
        confirmed = json.loads(await ws.recv())
        print(f"Loadout confirmed: {confirmed['weapon']}")

        # Step 4: Game loop
        while True:
            msg = json.loads(await ws.recv())

            if msg["type"] == "tick":
                state = msg["your_state"]
                entities = msg.get("nearby_entities", [])

                if not state["is_alive"]:
                    continue

                # Simple AI: attack nearest enemy, or move toward zone center
                enemies = [e for e in entities if e.get("type") == "bot" and e.get("is_alive")]
                if enemies and state["weapon_ready"]:
                    target = min(enemies, key=lambda e: dist(state["position"], e["position"]))
                    await ws.send(json.dumps({
                        "type": "action", "tick": msg["tick"],
                        "action": "attack", "target": target["bot_id"]
                    }))
                elif not state["in_safe_zone"]:
                    zc = msg["safe_zone"]["center"]
                    await ws.send(json.dumps({
                        "type": "action", "tick": msg["tick"],
                        "action": "move_to", "target_position": zc
                    }))
                else:
                    await ws.send(json.dumps({
                        "type": "action", "tick": msg["tick"], "action": "idle"
                    }))

            elif msg["type"] == "death":
                print(f"Died! Killed by {msg['killer_name']}")
            elif msg["type"] == "kill":
                print(f"Kill! {msg['victim_name']} (streak: {msg['your_kill_streak']})")

def dist(a, b):
    return ((a[0]-b[0])**2 + (a[1]-b[1])**2) ** 0.5

asyncio.run(main())
`,
		}

		return resp
	}
}

// weaponDescription returns a human-readable description of a weapon's special ability.
func weaponDescription(wc game.WeaponConfig) string {
	switch wc.Special {
	case "cleave":
		return "Melee weapon that hits all adjacent enemies in range (area cleave)"
	case "projectile":
		return "Ranged weapon that fires arrows — can store charge while ready, then spend it on a faster harder-hitting shot"
	case "backstab":
		return "Fast dual-wield melee — gains bonus damage from the rear arc"
	case "bash":
		return fmt.Sprintf("Melee tank weapon with passive %.0f%% damage reduction and bonus bash damage on disrupted targets", wc.Param*100)
	case "knockback":
		return fmt.Sprintf("Extended-reach melee that knocks enemies back %.0f tiles, with a brace bonus after holding ground", wc.Param)
	case "area":
		return fmt.Sprintf("Ranged AoE — impacts a %d-tile radius after a short delay and leaves a lingering burn field", wc.GridParam)
	case "grapple":
		return "Medium-range hook that pulls attacker to target on hit — can slam enemies near walls or edges for bonus impact"
	default:
		return "Standard weapon"
	}
}
