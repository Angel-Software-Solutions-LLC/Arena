package game

import (
	"encoding/json"
	"testing"
)

// The renderer's round-transition release compares the arena_state's round
// identity against the round_end that entered the transition. A snapshot
// without round_number can never satisfy the round rule, which held the
// post-round map teardown forever and rendered the arena black. Pin both the
// struct field and the wire key so the release signal cannot silently drop
// out of the payload again.
func TestSpectatorStateCarriesRoundIdentityForTransitionRelease(t *testing.T) {
	arena := NewArenaMap()
	state := BuildSpectatorState(nil, arena, nil, NewKillFeed(10), 120, 100, 7, nil, RoundModifierNone)

	if state.RoundNumber != 7 {
		t.Fatalf("RoundNumber = %d, want 7", state.RoundNumber)
	}
	if state.RoundTick != 20 {
		t.Fatalf("RoundTick = %d, want 20", state.RoundTick)
	}

	wire, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal spectator state: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal spectator state: %v", err)
	}
	if got, ok := decoded["round_number"].(float64); !ok || got != 7 {
		t.Fatalf("wire round_number = %v (present=%v), want 7 — the transition release depends on this key", decoded["round_number"], ok)
	}
	if got, ok := decoded["round_tick"].(float64); !ok || got != 20 {
		t.Fatalf("wire round_tick = %v (present=%v), want 20 — the tick-reset release fallback depends on this key", decoded["round_tick"], ok)
	}
}
