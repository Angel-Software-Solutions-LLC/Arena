package game

import (
	"reflect"
	"testing"
)

func TestUpdateBotCosmeticsCopiesLoadout(t *testing.T) {
	engine := NewGameEngine()
	engine.Bots["bot-1"] = &BotState{BotID: "bot-1"}

	loadout := map[string]string{"bot_skin": "neon_grid"}
	if !engine.UpdateBotCosmetics("bot-1", loadout) {
		t.Fatal("connected bot was not updated")
	}
	loadout["bot_skin"] = "mutated_after_call"
	if got := engine.Bots["bot-1"].Cosmetics["bot_skin"]; got != "neon_grid" {
		t.Fatalf("engine retained caller-owned map, got %q", got)
	}

	if engine.UpdateBotCosmetics("missing", map[string]string{}) {
		t.Fatal("missing bot unexpectedly reported as updated")
	}
}

func TestConnectedBotIDsIncludesActiveAndWaitingBotsInStableOrder(t *testing.T) {
	engine := NewGameEngine()
	engine.Bots["bot-z"] = &BotState{BotID: "bot-z"}
	engine.Bots["bot-a"] = &BotState{BotID: "bot-a"}
	engine.WaitingBots["bot-m"] = &BotState{BotID: "bot-m"}
	// A reconnect transition can briefly expose the same identity in both maps;
	// admin-driven cosmetic refreshes must still query it only once.
	engine.WaitingBots["bot-a"] = &BotState{BotID: "bot-a"}

	if got, want := engine.ConnectedBotIDs(), []string{"bot-a", "bot-m", "bot-z"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ConnectedBotIDs() = %v, want %v", got, want)
	}
}

func TestBuildSpectatorStateIncludesCosmeticsWithoutChangingMechanics(t *testing.T) {
	arena := NewArenaMap()
	bot := &BotState{
		BotID: "bot-1", Name: "Cosmo", AvatarColor: "#00ff88", Weapon: "sword",
		Position: Vec2{10, 20}, HP: 100, MaxHP: 100, IsAlive: true,
		Cosmetics: map[string]string{
			"bot_skin": "neon_grid", "weapon_skin": "solar_flare", "attachment": "signal_antenna",
			"trail": "ember_sparks",
		},
	}
	state := BuildSpectatorState(
		map[string]*BotState{bot.BotID: bot}, arena, nil, NewKillFeed(10), 10, 0, 1, nil, RoundModifierNone,
	)
	if len(state.Bots) != 1 {
		t.Fatalf("spectator bots = %d, want 1", len(state.Bots))
	}
	view := state.Bots[0]
	got := view.Cosmetics
	if got == nil {
		t.Fatal("cosmetics missing from spectator view")
	}
	if got["attachment"] != "signal_antenna" {
		t.Fatalf("attachment = %q, want signal_antenna", got["attachment"])
	}
	if got["trail"] != "ember_sparks" {
		t.Fatalf("trail = %q, want ember_sparks", got["trail"])
	}
	if view.Weapon != "sword" || view.HP != float64(100) {
		t.Fatalf("cosmetics changed gameplay view: weapon=%v hp=%v", view.Weapon, view.HP)
	}
}

func TestBuildSpectatorStateIncludesLastActionTickForAnimationEdges(t *testing.T) {
	arena := NewArenaMap()
	bot := &BotState{
		BotID: "bot-action", Name: "Action Bot", Position: Vec2{10, 20},
		HP: 100, MaxHP: 100, IsAlive: true, Weapon: "grapple",
		LastActionTick: 42, LastActionResult: &ActionResult{Action: "grapple", Success: true},
	}
	state := BuildSpectatorState(
		map[string]*BotState{bot.BotID: bot}, arena, nil, NewKillFeed(10), 50, 0, 1, nil, RoundModifierNone,
	)

	if got := state.Bots[0].LastActionTick; got != 42 {
		t.Fatalf("last_action_tick = %v, want 42 so persistent action strings do not retrigger visuals", got)
	}
}
