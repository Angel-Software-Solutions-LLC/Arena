package config

import "testing"

func TestValidateSizeConfig(t *testing.T) {
	good := Config{ChatHistorySize: 50, SpatialCellSize: 100, PathfindingCellSize: 20, DBSSLMode: "disable"}
	if err := ValidateSizeConfig(good); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	cases := map[string]Config{
		"negative chat history": {ChatHistorySize: -1, SpatialCellSize: 100, PathfindingCellSize: 20, DBSSLMode: "disable"},
		"zero chat history":     {ChatHistorySize: 0, SpatialCellSize: 100, PathfindingCellSize: 20, DBSSLMode: "disable"},
		"zero spatial cell":     {ChatHistorySize: 50, SpatialCellSize: 0, PathfindingCellSize: 20, DBSSLMode: "disable"},
		"zero pathfinding cell": {ChatHistorySize: 50, SpatialCellSize: 100, PathfindingCellSize: 0, DBSSLMode: "disable"},
		"unknown sslmode":       {ChatHistorySize: 50, SpatialCellSize: 100, PathfindingCellSize: 20, DBSSLMode: "yes"},
	}
	for name, cfg := range cases {
		if err := ValidateSizeConfig(cfg); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
