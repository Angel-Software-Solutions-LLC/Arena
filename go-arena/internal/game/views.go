package game

import (
	"math"
	"sort"

	"arena-server/internal/config"
)

// round1 rounds a float64 to 1 decimal place, matching Python's round(x, 1).
func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

// botTargetID returns the target_id from the bot's pending action, or empty string.
func botTargetID(bot *BotState) string {
	if bot.PendingAction != nil {
		return bot.PendingAction.TargetID
	}
	return ""
}

func botTargetPosition(bot *BotState) *Vec2 {
	if bot.PendingAction != nil && bot.PendingAction.TargetPosition != nil {
		pos := *bot.PendingAction.TargetPosition
		return &pos
	}
	return nil
}

func bowChargeLevel(bot *BotState) float64 {
	if bot == nil || bot.Weapon != "bow" {
		return 0
	}
	maxTicks := config.C.BowChargeMaxTicks
	if maxTicks <= 0 {
		maxTicks = 6
	}
	ticks := bot.BowChargeTicks
	if ticks < 0 {
		ticks = 0
	}
	if ticks > maxTicks {
		ticks = maxTicks
	}
	return round1(float64(ticks) / float64(maxTicks))
}

func chargedShotReady(bot *BotState) bool {
	if bot == nil || bot.Weapon != "bow" {
		return false
	}
	readyTicks := config.C.BowChargeReadyTicks
	if readyTicks <= 0 {
		readyTicks = 1
	}
	return bot.BowChargeTicks >= readyTicks
}

func isRearExposedToObserver(observerPos Vec2, bot *BotState) bool {
	if bot == nil {
		return false
	}
	return isRearExposed(observerPos, bot.Position, bot.Facing)
}

func isRearExposed(observerPos, targetPos, facing Vec2) bool {
	targetFacing := facing.Normalized()
	if targetFacing.Length() <= 0 {
		return false
	}
	fromTarget := observerPos.Sub(targetPos).Normalized()
	if fromTarget.Length() <= 0 {
		return false
	}
	return targetFacing.X()*fromTarget.X()+targetFacing.Y()*fromTarget.Y() <= config.C.DaggerBackstabDotThreshold
}

// posToGrid converts a Vec2 to grid coordinates [col, row].
// Returns [0, 0] if no terrain grid is active.
func posToGrid(pos Vec2) [2]int {
	if ActiveTerrain != nil {
		return ActiveTerrain.WorldToGrid(pos)
	}
	return [2]int{int(pos.X()), int(pos.Y())}
}

// BotNearbyView is the typed protocol view of a bot as seen by a nearby
// observer. Only HasLOS and RearExposed depend on the observer; every other
// field is observer-independent, so one base view is built per visible bot
// per tick and copied per observer via ObservedBy (see sendBotTickUpdates).
type BotNearbyView struct {
	Type                   string  `json:"type"`
	ID                     string  `json:"id"`
	BotID                  string  `json:"bot_id"`
	Name                   string  `json:"name"`
	Team                   int     `json:"team"`
	Position               [2]int  `json:"position"`
	HP                     float64 `json:"hp"`
	MaxHP                  float64 `json:"max_hp"`
	Weapon                 string  `json:"weapon"`
	IsAlive                bool    `json:"is_alive"`
	AvatarColor            string  `json:"avatar_color"`
	LastAction             *string `json:"last_action"`
	Action                 *string `json:"action"`
	TargetID               string  `json:"target_id"`
	IsDodging              bool    `json:"is_dodging"`
	IsStunned              bool    `json:"is_stunned"`
	Facing                 Vec2    `json:"facing"`
	RecentlyDisruptedTicks int     `json:"recently_disrupted_ticks"`
	BraceReady             bool    `json:"brace_ready"`
	BowChargeTicks         int     `json:"bow_charge_ticks"`
	BowChargeLevel         float64 `json:"bow_charge_level"`
	ChargedShotReady       bool    `json:"charged_shot_ready"`
	HasLOS                 bool    `json:"has_los"`
	AttackRange            int     `json:"attack_range"`
	CanAttack              bool    `json:"can_attack"`
	RearExposed            bool    `json:"rear_exposed"`
	NearImpactSurface      bool    `json:"near_impact_surface"`
	ThreatScore            float64 `json:"threat_score"`

	// worldPos is the bot's world-space position, retained for the
	// observer-dependent LOS/rear checks. Unexported, so not serialized.
	worldPos Vec2
	// worldFacing keeps the raw facing for the rear-exposure dot product.
	worldFacing Vec2
}

// BuildBotNearbyBaseView builds the observer-independent part of a bot's
// nearby view (HasLOS and RearExposed are left false). Position is reported
// as grid coordinates.
func BuildBotNearbyBaseView(bot *BotState) BotNearbyView {
	var lastAction *string
	if bot.LastActionResult != nil {
		action := bot.LastActionResult.Action
		lastAction = &action
	}

	gridPos := posToGrid(bot.Position)

	// Weapon attack range.
	wc := GetWeaponConfig(bot.Weapon)

	// Threat score: (kills * 10 + hp_percent * 5)
	threatScore := round1(float64(bot.RoundKills)*10 + (bot.HP/bot.MaxHP)*500)

	return BotNearbyView{
		Type:                   "bot",
		ID:                     bot.BotID,
		BotID:                  bot.BotID,
		Name:                   bot.Name,
		Team:                   bot.Team,
		Position:               gridPos,
		HP:                     math.Round(bot.HP),
		MaxHP:                  math.Round(bot.MaxHP),
		Weapon:                 bot.Weapon,
		IsAlive:                bot.IsAlive,
		AvatarColor:            bot.AvatarColor,
		LastAction:             lastAction,
		Action:                 lastAction,
		TargetID:               botTargetID(bot),
		IsDodging:              bot.InvulnTicks > 0,
		IsStunned:              bot.StunTicks > 0,
		Facing:                 bot.Facing,
		RecentlyDisruptedTicks: bot.RecentlyDisruptedTicks,
		BraceReady:             bot.Weapon == "spear" && isBraceReady(bot),
		BowChargeTicks:         bot.BowChargeTicks,
		BowChargeLevel:         bowChargeLevel(bot),
		ChargedShotReady:       chargedShotReady(bot),
		AttackRange:            wc.GridRange,
		CanAttack:              bot.CooldownRemaining <= 0,
		NearImpactSurface:      isNearImpactSurface(bot.Position, nil),
		ThreatScore:            threatScore,
		worldPos:               bot.Position,
		worldFacing:            bot.Facing,
	}
}

// ObservedBy returns a copy of the base view with the observer-dependent
// fields (line of sight, rear exposure) filled in. The copy is mandatory:
// each observer's message is marshaled after the engine lock is released, so
// observers must never share one mutable view.
func (v BotNearbyView) ObservedBy(observerPos Vec2) *BotNearbyView {
	view := v
	view.HasLOS = ActiveTerrain != nil && !ActiveTerrain.GridLineBlocked(observerPos, v.worldPos)
	view.RearExposed = isRearExposed(observerPos, v.worldPos, v.worldFacing)
	return &view
}

// BuildBotNearbyView builds the full protocol view for a bot as seen by a
// nearby observer. Position is reported as grid coordinates. observerPos is
// the world-space position of the observing bot (for LOS checks).
func BuildBotNearbyView(bot *BotState, observerPos Vec2) *BotNearbyView {
	return BuildBotNearbyBaseView(bot).ObservedBy(observerPos)
}

// PickupNearbyView is the typed protocol view of a pickup in a bot's
// nearby-entities list.
type PickupNearbyView struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	PickupID   string `json:"pickup_id"`
	PickupType string `json:"pickup_type"`
	Position   [2]int `json:"position"`
}

// BuildPickupNearbyView builds the protocol view for a pickup.
// Position is reported as grid coordinates.
func BuildPickupNearbyView(p Pickup) PickupNearbyView {
	gridPos := posToGrid(p.Position)

	return PickupNearbyView{
		Type:       "pickup",
		ID:         p.ID,
		PickupID:   p.ID,
		PickupType: string(p.Type),
		Position:   gridPos,
	}
}

// BountyTargetView is the typed protocol view of the current bounty target,
// visible to all bots regardless of fog.
type BountyTargetView struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	BotID    string `json:"bot_id"`
	Name     string `json:"name"`
	Position [2]int `json:"position"`
}

// EffectView is the typed protocol view of an active effect in your_state.
type EffectView struct {
	Name  string `json:"name"`
	Ticks int    `json:"ticks"`
}

// HitReceivedView is the typed protocol view of a hit received this tick.
type HitReceivedView struct {
	AttackerID string  `json:"attacker_id"`
	Damage     float64 `json:"damage"`
	Weapon     string  `json:"weapon"`
}

// YourStateView is the typed your_state payload sent to a bot each tick.
// All positions and distances are reported in grid coordinates/tiles.
type YourStateView struct {
	BotID                  string            `json:"bot_id"`
	Team                   int               `json:"team"`
	Position               [2]int            `json:"position"`
	HP                     float64           `json:"hp"`
	MaxHP                  float64           `json:"max_hp"`
	Speed                  float64           `json:"speed"`
	Weapon                 string            `json:"weapon"`
	CooldownRemaining      float64           `json:"cooldown_remaining"`
	WeaponReady            bool              `json:"weapon_ready"`
	IsAlive                bool              `json:"is_alive"`
	KillStreak             int               `json:"kill_streak"`
	RoundKills             int               `json:"round_kills"`
	DodgeCooldown          int               `json:"dodge_cooldown"`
	InvulnTicks            int               `json:"invuln_ticks"`
	StunTicks              int               `json:"stun_ticks"`
	Facing                 Vec2              `json:"facing"`
	RecentlyDisruptedTicks int               `json:"recently_disrupted_ticks"`
	BraceReady             bool              `json:"brace_ready"`
	BowChargeTicks         int               `json:"bow_charge_ticks"`
	BowChargeLevel         float64           `json:"bow_charge_level"`
	ChargedShotReady       bool              `json:"charged_shot_ready"`
	ShieldAbsorb           float64           `json:"shield_absorb"`
	HazardKeyActive        bool              `json:"hazard_key_active"`
	HazardKeyTicks         int               `json:"hazard_key_ticks"`
	RelayBatteryActive     bool              `json:"relay_battery_active"`
	RelayBatteryTicks      int               `json:"relay_battery_ticks"`
	Effects                []EffectView      `json:"effects"`
	LastActionResult       *ActionResult     `json:"last_action_result"`
	HitsReceived           []HitReceivedView `json:"hits_received"`
	KillFeed               []KillFeedEntry   `json:"kill_feed"`
	// Zone info (in grid tiles).
	InSafeZone         bool   `json:"in_safe_zone"`
	DistanceToZoneEdge int    `json:"distance_to_zone_edge"`
	ZoneRadius         int    `json:"zone_radius"`
	ZoneCenter         [2]int `json:"zone_center"`
	ZoneTargetCenter   [2]int `json:"zone_target_center"`
	ZoneTargetRadius   int    `json:"zone_target_radius"`
	// New gameplay state.
	IsBountyTarget    bool    `json:"is_bounty_target"`
	BountyTokenBonus  int     `json:"bounty_token_bonus"`
	MineCount         int     `json:"mine_count"`
	GravityWellCharge int     `json:"gravity_well_charge"`
	GrappleCharges    int     `json:"grapple_charges"`
	GrappleCooldown   float64 `json:"grapple_cooldown"`
}

// BuildYourState builds the full your_state view sent to a bot each tick.
// All positions and distances are reported in grid coordinates/tiles. The
// returned view is a value snapshot: reference-typed bot state (notably
// LastActionResult) is copied here, under the engine lock, so the view can
// be marshaled safely after the lock is released.
func BuildYourState(bot *BotState, arena *ArenaMap, killFeed *KillFeed, tickCount int) *YourStateView {
	// Effective speed (apply speed boost effects).
	effectiveSpeed := bot.Speed
	for _, eff := range bot.ActiveEffects {
		if eff.Name == "speed_boost" {
			effectiveSpeed *= eff.Value
		}
	}

	// Effects list.
	effects := make([]EffectView, 0, len(bot.ActiveEffects))
	for _, eff := range bot.ActiveEffects {
		effects = append(effects, EffectView{
			Name:  eff.Name,
			Ticks: eff.RemainingTicks,
		})
	}

	// Last action result — copied by value so the view does not alias the
	// live *ActionResult, which the next locked tick may replace or clear.
	var lastActionResult *ActionResult
	if bot.LastActionResult != nil {
		resultCopy := *bot.LastActionResult
		lastActionResult = &resultCopy
	}

	// Hits received.
	hitsReceived := make([]HitReceivedView, 0, len(bot.HitsReceived))
	for _, hr := range bot.HitsReceived {
		hitsReceived = append(hitsReceived, HitReceivedView{
			AttackerID: hr.AttackerID,
			Damage:     hr.Damage,
			Weapon:     hr.Weapon,
		})
	}

	// Kill feed (last 5) — cached view shared by every bot's message this tick.
	killFeedEntries := killFeed.RecentViews(5)

	// Zone info in grid coordinates.
	inSafeZone := arena.IsInZone(bot.Position)
	distToEdge := arena.DistanceToZoneEdge(bot.Position)

	gridPos := posToGrid(bot.Position)
	zoneCenter := posToGrid(arena.ZoneCenter)
	zoneTargetCenter := posToGrid(arena.ZoneTargetCenter)

	var cellSize float64 = 20
	if ActiveTerrain != nil {
		cellSize = ActiveTerrain.CellSize
	}
	zoneRadiusTiles := int(math.Round(arena.ZoneRadius / cellSize))
	zoneTargetRadiusTiles := int(math.Round(arena.ZoneTargetRadius / cellSize))
	distToEdgeTiles := int(math.Round(distToEdge / cellSize))

	return &YourStateView{
		BotID:                  bot.BotID,
		Team:                   bot.Team,
		Position:               gridPos,
		HP:                     math.Round(bot.HP),
		MaxHP:                  math.Round(bot.MaxHP),
		Speed:                  round1(effectiveSpeed),
		Weapon:                 bot.Weapon,
		CooldownRemaining:      round1(bot.CooldownRemaining),
		WeaponReady:            bot.CooldownRemaining <= 0,
		IsAlive:                bot.IsAlive,
		KillStreak:             bot.KillStreak,
		RoundKills:             bot.RoundKills,
		DodgeCooldown:          bot.DodgeCooldown,
		InvulnTicks:            bot.InvulnTicks,
		StunTicks:              bot.StunTicks,
		Facing:                 bot.Facing,
		RecentlyDisruptedTicks: bot.RecentlyDisruptedTicks,
		BraceReady:             bot.Weapon == "spear" && isBraceReady(bot),
		BowChargeTicks:         bot.BowChargeTicks,
		BowChargeLevel:         bowChargeLevel(bot),
		ChargedShotReady:       chargedShotReady(bot),
		ShieldAbsorb:           bot.ShieldAbsorb,
		HazardKeyActive:        hasEffectByName(bot.ActiveEffects, "hazard_key"),
		HazardKeyTicks:         effectRemainingTicks(bot.ActiveEffects, "hazard_key"),
		RelayBatteryActive:     hasEffectByName(bot.ActiveEffects, "relay_battery"),
		RelayBatteryTicks:      effectRemainingTicks(bot.ActiveEffects, "relay_battery"),
		Effects:                effects,
		LastActionResult:       lastActionResult,
		HitsReceived:           hitsReceived,
		KillFeed:               killFeedEntries,
		InSafeZone:             inSafeZone,
		DistanceToZoneEdge:     distToEdgeTiles,
		ZoneRadius:             zoneRadiusTiles,
		ZoneCenter:             zoneCenter,
		ZoneTargetCenter:       zoneTargetCenter,
		ZoneTargetRadius:       zoneTargetRadiusTiles,
		IsBountyTarget:         bot.IsBountyTarget,
		BountyTokenBonus:       bot.BountyTokenBonus,
		MineCount:              bot.MineCount,
		GravityWellCharge:      bot.GravityWellCharge,
		GrappleCharges:         bot.GrappleCharges,
		GrappleCooldown:        round1(bot.GrappleCooldown),
	}
}

// BotSpectatorView is the typed spectator-lane view of a bot, mirroring the
// bot-tick lane conversion (BotNearbyView). Spectators get float world
// positions for smooth canvas rendering, not grid coordinates.
//
// Wire-parity notes (vs the previous map payload):
//   - LastAction/Action are pointers so an idle bot still serializes
//     "last_action":null and "action":null; both keys carry the same value,
//     as before.
//   - TargetPosition is null when the pending action has no target position.
//   - Cosmetics has no omitempty: a bot without cosmetics serializes
//     "cosmetics":null exactly like the old map payload did.
// Fields that are zero for most bots on most ticks carry omitempty: a
// steady-state 14-bot snapshot was ~11 KB of which ~97% was the bots array,
// most of it these always-zero keys. This is the SPECTATOR lane only --
// BotNearbyView (the bot protocol) is a separate struct and is untouched, so
// no SDK or third-party bot sees a change. Verified there are no presence or
// strict-equality checks on these keys in frontend/js or frontend/m; absent
// and zero are equivalent to every consumer.
type BotSpectatorView struct {
	Type                   string            `json:"type"`
	ID                     string            `json:"id"`
	BotID                  string            `json:"bot_id"`
	Name                   string            `json:"name"`
	Position               Vec2              `json:"position"`
	HP                     float64           `json:"hp"`
	MaxHP                  float64           `json:"max_hp"`
	Weapon                 string            `json:"weapon"`
	IsAlive                bool              `json:"is_alive"`
	AvatarColor            string            `json:"avatar_color"`
	Cosmetics              map[string]string `json:"cosmetics"`
	LastAction             *string           `json:"last_action"`
	LastActionTick         int               `json:"last_action_tick,omitempty"`
	Action                 *string           `json:"action"`
	TargetID               string            `json:"target_id,omitempty"`
	TargetPosition         *Vec2             `json:"target_position"`
	IsDodging              bool              `json:"is_dodging,omitempty"`
	IsStunned              bool              `json:"is_stunned,omitempty"`
	CooldownRemaining      float64           `json:"cooldown_remaining,omitempty"`
	Facing                 Vec2              `json:"facing"`
	RecentlyDisruptedTicks int               `json:"recently_disrupted_ticks,omitempty"`
	BraceReady             bool              `json:"brace_ready,omitempty"`
	BowChargeTicks         int               `json:"bow_charge_ticks,omitempty"`
	BowChargeLevel         float64           `json:"bow_charge_level,omitempty"`
	ChargedShotReady       bool              `json:"charged_shot_ready,omitempty"`
	KillStreak             int               `json:"kill_streak,omitempty"`
	RoundKills             int               `json:"round_kills,omitempty"`
	ShieldAbsorb           float64           `json:"shield_absorb,omitempty"`
	HazardKeyActive        bool              `json:"hazard_key_active,omitempty"`
	HazardKeyTicks         int               `json:"hazard_key_ticks,omitempty"`
	RelayBatteryActive     bool              `json:"relay_battery_active,omitempty"`
	RelayBatteryTicks      int               `json:"relay_battery_ticks,omitempty"`
	MineCount              int               `json:"mine_count,omitempty"`
	GrappleCharges         int               `json:"grapple_charges,omitempty"`
	GrappleCooldown        float64           `json:"grapple_cooldown,omitempty"`
	GravityWellCharge      int               `json:"gravity_well_charge,omitempty"`
	IsBountyTarget         bool              `json:"is_bounty_target,omitempty"`
	BountyTokenBonus       int               `json:"bounty_token_bonus,omitempty"`
	Team                   int               `json:"team,omitempty"`
}

// BuildBotSpectatorView builds the spectator view of one bot.
func BuildBotSpectatorView(bot *BotState) BotSpectatorView {
	var lastAction *string
	if bot.LastActionResult != nil {
		action := bot.LastActionResult.Action
		lastAction = &action
	}
	return BotSpectatorView{
		Type:                   "bot",
		ID:                     bot.BotID,
		BotID:                  bot.BotID,
		Name:                   bot.Name,
		Position:               Vec2{round1(bot.Position[0]), round1(bot.Position[1])},
		HP:                     math.Round(bot.HP),
		MaxHP:                  math.Round(bot.MaxHP),
		Weapon:                 bot.Weapon,
		IsAlive:                bot.IsAlive,
		AvatarColor:            bot.AvatarColor,
		Cosmetics:              bot.Cosmetics,
		LastAction:             lastAction,
		LastActionTick:         bot.LastActionTick,
		Action:                 lastAction,
		TargetID:               botTargetID(bot),
		TargetPosition:         botTargetPosition(bot),
		IsDodging:              bot.InvulnTicks > 0,
		IsStunned:              bot.StunTicks > 0,
		CooldownRemaining:      round1(bot.CooldownRemaining),
		Facing:                 bot.Facing,
		RecentlyDisruptedTicks: bot.RecentlyDisruptedTicks,
		BraceReady:             bot.Weapon == "spear" && isBraceReady(bot),
		BowChargeTicks:         bot.BowChargeTicks,
		BowChargeLevel:         bowChargeLevel(bot),
		ChargedShotReady:       chargedShotReady(bot),
		KillStreak:             bot.KillStreak,
		RoundKills:             bot.RoundKills,
		ShieldAbsorb:           round1(bot.ShieldAbsorb),
		HazardKeyActive:        hasEffectByName(bot.ActiveEffects, "hazard_key"),
		HazardKeyTicks:         effectRemainingTicks(bot.ActiveEffects, "hazard_key"),
		RelayBatteryActive:     hasEffectByName(bot.ActiveEffects, "relay_battery"),
		RelayBatteryTicks:      effectRemainingTicks(bot.ActiveEffects, "relay_battery"),
		MineCount:              bot.MineCount,
		GrappleCharges:         bot.GrappleCharges,
		GrappleCooldown:        round1(bot.GrappleCooldown),
		GravityWellCharge:      bot.GravityWellCharge,
		IsBountyTarget:         bot.IsBountyTarget,
		BountyTokenBonus:       bot.BountyTokenBonus,
		Team:                   bot.Team,
	}
}

// PickupSpectatorView is the typed spectator-lane view of a pickup, with a
// float world position (the bot-tick lane's PickupNearbyView uses grid cells).
type PickupSpectatorView struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	PickupID   string `json:"pickup_id"`
	PickupType string `json:"pickup_type"`
	Position   Vec2   `json:"position"`
}

// SafeZoneSpectatorView is the typed safe_zone submap of the spectator state,
// in float world coordinates (the bot lane's SafeZoneGridView uses tiles).
type SafeZoneSpectatorView struct {
	Center       Vec2    `json:"center"`
	Radius       float64 `json:"radius"`
	TargetCenter Vec2    `json:"target_center"`
	TargetRadius float64 `json:"target_radius"`
}

// WaitingBotSpectatorView is the typed lobby-tab entry for a bot waiting to
// join the next round. Cosmetics has no omitempty (null when unset, matching
// the previous map payload).
type WaitingBotSpectatorView struct {
	Name        string            `json:"name"`
	AvatarColor string            `json:"avatar_color"`
	Weapon      string            `json:"weapon"`
	Cosmetics   map[string]string `json:"cosmetics"`
}

// BuildSpectatorState builds the full arena snapshot for spectator clients.
// Spectators still receive float positions for smooth rendering.
// roundNumber identifies the round the snapshot describes: the renderer's
// round-transition release compares it against the round_end that entered the
// transition, so leaving it unset would hold the post-round map teardown
// forever (the blank-arena symptom).
func BuildSpectatorState(bots map[string]*BotState, arena *ArenaMap, pickups []Pickup, killFeed *KillFeed, tickCount int, roundStartTick int, roundNumber int, waitingBots map[string]*BotState, roundModifier RoundModifier) SpectatorState {
	botViews := make([]BotSpectatorView, 0, len(bots))
	for _, bot := range bots {
		botViews = append(botViews, BuildBotSpectatorView(bot))
	}
	// Deterministic ordering is still required: the spectator HUD roster and
	// lobby tab render in received order and diff against the previous frame,
	// so raw map iteration order would reshuffle the DOM at 10 Hz. Sorting by
	// struct field is cheap (no map lookups or interface assertions); Name
	// keeps the wire order identical to the previous payloads.
	sort.Slice(botViews, func(i, j int) bool {
		return botViews[i].Name < botViews[j].Name
	})

	pickupViews := make([]PickupSpectatorView, 0, len(pickups))
	for _, p := range pickups {
		pickupViews = append(pickupViews, PickupSpectatorView{
			Type:       "pickup",
			ID:         p.ID,
			PickupID:   p.ID,
			PickupType: string(p.Type),
			Position:   Vec2{round1(p.Position[0]), round1(p.Position[1])},
		})
	}

	// Cached until the feed changes — rebuilt on kills, not every tick.
	killFeedViews := killFeed.AllViews()

	safeZone := SafeZoneSpectatorView{
		Center:       arena.ZoneCenter,
		Radius:       round1(arena.ZoneRadius),
		TargetCenter: arena.ZoneTargetCenter,
		TargetRadius: round1(arena.ZoneTargetRadius),
	}

	// Send collision-accurate obstacles (expanded + grid-snapped). These are
	// static for the whole round, so they're computed once and cached.
	visObstacles := arena.VisualObstacles()

	// Build waiting bots list for the lobby tab during active rounds.
	var waitingViews []WaitingBotSpectatorView
	if len(waitingBots) > 0 {
		waitingViews = make([]WaitingBotSpectatorView, 0, len(waitingBots))
		for _, bot := range waitingBots {
			waitingViews = append(waitingViews, WaitingBotSpectatorView{
				Name:        bot.Name,
				AvatarColor: bot.AvatarColor,
				Weapon:      bot.Weapon,
				Cosmetics:   bot.Cosmetics,
			})
		}
		// Same DOM-stability requirement as the bots list above.
		sort.Slice(waitingViews, func(i, j int) bool {
			return waitingViews[i].Name < waitingViews[j].Name
		})
	}

	return SpectatorState{
		Type:          "arena_state",
		Tick:          tickCount,
		RoundTick:     tickCount - roundStartTick,
		RoundNumber:   roundNumber,
		RoundModifier: string(roundModifier),
		Bots:          botViews,
		SafeZone:      safeZone,
		Pickups:       pickupViews,
		KillFeed:      killFeedViews,
		Obstacles:     visObstacles,
		MaskRects:     arena.MaskRects,
		WaitingBots:   waitingViews,
	}
}
