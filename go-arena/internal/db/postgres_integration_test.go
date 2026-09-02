package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"arena-server/internal/config"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const postgresIntegrationEnv = "ARENA_TEST_DATABASE_URL"

func useFreshPostgresSchema(t *testing.T) context.Context {
	t.Helper()

	databaseURL := strings.TrimSpace(os.Getenv(postgresIntegrationEnv))
	if databaseURL == "" {
		t.Skipf("set %s to run PostgreSQL integration tests", postgresIntegrationEnv)
	}

	setupCtx, setupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer setupCancel()

	adminPool, err := pgxpool.New(setupCtx, databaseURL)
	if err != nil {
		t.Fatalf("create integration admin pool: %v", err)
	}
	if err := adminPool.Ping(setupCtx); err != nil {
		adminPool.Close()
		t.Fatalf("ping integration Postgres: %v", err)
	}

	schema := "arena_integration_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(setupCtx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatalf("create integration schema: %v", err)
	}

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		adminPool.Close()
		t.Fatalf("parse integration database URL: %v", err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	testPool, err := pgxpool.NewWithConfig(setupCtx, poolConfig)
	if err != nil {
		adminPool.Close()
		t.Fatalf("create schema-scoped pool: %v", err)
	}
	if err := testPool.Ping(setupCtx); err != nil {
		testPool.Close()
		adminPool.Close()
		t.Fatalf("ping schema-scoped pool: %v", err)
	}

	previousPool := Pool
	Pool = testPool
	t.Cleanup(func() {
		Pool = previousPool
		testPool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop integration schema: %v", err)
		}
		adminPool.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestPostgresFreshSchemaCosmeticsAndLeaderboardResetSmoke(t *testing.T) {
	ctx := useFreshPostgresSchema(t)

	// Startup runs schema repair against both empty and existing databases.
	// Running it twice catches non-idempotent DDL and seed behavior.
	for attempt := 1; attempt <= 2; attempt++ {
		if err := EnsureCoreSchema(ctx); err != nil {
			t.Fatalf("EnsureCoreSchema attempt %d: %v", attempt, err)
		}
	}

	const registrationLimit = 5
	type rateResult struct {
		allowed bool
		err     error
	}
	rateResults := make(chan rateResult, registrationLimit*4)
	for i := 0; i < cap(rateResults); i++ {
		go func() {
			allowed, _, err := CheckRateLimit(context.Background(), "198.51.100.25", registrationLimit)
			rateResults <- rateResult{allowed: allowed, err: err}
		}()
	}
	allowedRegistrations := 0
	for i := 0; i < cap(rateResults); i++ {
		result := <-rateResults
		if result.err != nil {
			t.Fatalf("concurrent CheckRateLimit: %v", result.err)
		}
		if result.allowed {
			allowedRegistrations++
		}
	}
	if allowedRegistrations != registrationLimit {
		t.Fatalf("atomic registration admissions = %d, want %d", allowedRegistrations, registrationLimit)
	}
	var consumedSlots int
	if err := Pool.QueryRow(ctx, `
		SELECT keys_generated FROM rate_limits WHERE ip_address = $1`, "198.51.100.25").Scan(&consumedSlots); err != nil {
		t.Fatalf("read registration rate limit: %v", err)
	}
	if consumedSlots != registrationLimit {
		t.Fatalf("registration slots stored = %d, want %d", consumedSlots, registrationLimit)
	}

	now := time.Now()
	bot := &Bot{
		ID:              "integration-bot",
		APIKeyID:        "integration-key",
		Name:            "Integration Bot",
		AvatarColor:     "#123456",
		DefaultWeapon:   "sword",
		DefaultStats:    JSONBStats{"hp": 5, "speed": 5, "attack": 5, "defense": 5},
		DefaultFallback: "aggressive",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := CreateAPIKeyAndBot(ctx, bot.APIKeyID, "integration-hash", "arena_integration_prefix", "127.0.0.1", bot); err != nil {
		t.Fatalf("CreateAPIKeyAndBot: %v", err)
	}
	joinedKey, joinedBot, err := GetAPIKeyAndBotByPrefix(ctx, "arena_integration_prefix")
	if err != nil {
		t.Fatalf("GetAPIKeyAndBotByPrefix: %v", err)
	}
	if joinedKey == nil || joinedKey.ID != bot.APIKeyID || joinedBot == nil || joinedBot.ID != bot.ID {
		t.Fatalf("joined authentication lookup = key %#v bot %#v", joinedKey, joinedBot)
	}
	active, err := IsAPIKeyActive(ctx, bot.APIKeyID)
	if err != nil || !active {
		t.Fatalf("IsAPIKeyActive active row = (%v, %v), want (true, nil)", active, err)
	}
	if err := DeactivateAPIKey(ctx, bot.APIKeyID); err != nil {
		t.Fatalf("DeactivateAPIKey: %v", err)
	}
	active, err = IsAPIKeyActive(ctx, bot.APIKeyID)
	if err != nil || active {
		t.Fatalf("IsAPIKeyActive revoked row = (%v, %v), want (false, nil)", active, err)
	}
	active, err = IsAPIKeyActive(ctx, "missing-integration-key")
	if err != nil || active {
		t.Fatalf("IsAPIKeyActive missing row = (%v, %v), want (false, nil)", active, err)
	}
	if _, err := Pool.Exec(ctx, `UPDATE api_keys SET is_active = true WHERE id = $1`, bot.APIKeyID); err != nil {
		t.Fatalf("reactivate integration API key: %v", err)
	}
	rollbackHash, err := bcrypt.GenerateFromPassword([]byte("integration-key-secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("generate rollback-compatible credential fixture: %v", err)
	}
	migratedHash := string(rollbackHash) + "sha256:v1:" + strings.Repeat("a", 64)
	if err := UpdateAPIKeyHashAndLastSeen(ctx, bot.APIKeyID, migratedHash); err != nil {
		t.Fatalf("UpdateAPIKeyHashAndLastSeen: %v", err)
	}
	joinedKey, joinedBot, err = GetAPIKeyAndBotByPrefix(ctx, "arena_integration_prefix")
	if err != nil {
		t.Fatalf("GetAPIKeyAndBotByPrefix after migration: %v", err)
	}
	if joinedKey == nil || joinedKey.KeyHash != migratedHash || joinedKey.LastSeen == nil || joinedBot == nil || joinedBot.ID != bot.ID {
		t.Fatalf("post-migration authentication lookup = key %#v bot %#v", joinedKey, joinedBot)
	}

	created, err := GrantCosmeticEntitlement(ctx, bot.ID, "skin-neon-grid", "integration", "grant-1")
	if err != nil || !created {
		t.Fatalf("GrantCosmeticEntitlement = (%v, %v), want (true, nil)", created, err)
	}
	item, err := EquipCosmetic(ctx, bot.ID, CosmeticSlotBotSkin, "skin-neon-grid")
	if err != nil {
		t.Fatalf("EquipCosmetic: %v", err)
	}
	if item.ID != "skin-neon-grid" || item.AssetKey != "neon_grid" {
		t.Fatalf("equipped item = %+v", item)
	}
	equipped, err := GetEquippedCosmetics(ctx, bot.ID)
	if err != nil || equipped[CosmeticSlotBotSkin] != "neon_grid" {
		t.Fatalf("equipped cosmetics = (%v, %v), want neon_grid", equipped, err)
	}

	// The grant is what lets the loadout resolve; withdrawing it hides the
	// look at the next read with no loadout cleanup, exactly as a lapsed
	// subscription does for a linked bot.
	if _, err := Pool.Exec(ctx, `DELETE FROM cosmetic_entitlements WHERE bot_id = $1 AND cosmetic_id = $2`, bot.ID, "skin-neon-grid"); err != nil {
		t.Fatalf("withdraw demo entitlement: %v", err)
	}
	equipped, err = GetEquippedCosmetics(ctx, bot.ID)
	if err != nil || equipped[CosmeticSlotBotSkin] != "standard" {
		t.Fatalf("post-withdrawal cosmetics = (%v, %v), want standard", equipped, err)
	}

	if err := ApplyBotStatsDelta(ctx, &BotStatsDelta{
		BotID: bot.ID, Kills: 3, Deaths: 1, DamageDealt: 90, DamageTaken: 30,
		CurrentStreak: 2, BestStreak: 2, Elo: 1040, RoundsPlayed: 1,
		RoundWins: 1, CapturedAt: now,
	}); err != nil {
		t.Fatalf("ApplyBotStatsDelta: %v", err)
	}
	if err := CreateRound(ctx, &Round{
		ID: "integration-round-1", RoundNumber: 1, StartedAt: now, Status: "completed",
	}); err != nil {
		t.Fatalf("CreateRound: %v", err)
	}
	if err := InsertRoundBotStats(ctx, "integration-round-1", 1, bot.ID, bot.Name, "sword", 3, 1, 90, 30, 60, 8, 5, 1, 20, 1040, true); err != nil {
		t.Fatalf("InsertRoundBotStats: %v", err)
	}
	if err := ResetLeaderboard(ctx); err != nil {
		t.Fatalf("ResetLeaderboard: %v", err)
	}

	var allTimeRows, windowRows int
	if err := Pool.QueryRow(ctx, `
		SELECT (SELECT COUNT(*) FROM bot_stats),
		       (SELECT COUNT(*) FROM round_bot_stats)`).Scan(&allTimeRows, &windowRows); err != nil {
		t.Fatalf("count reset leaderboard rows: %v", err)
	}
	if allTimeRows != 0 || windowRows != 0 {
		t.Fatalf("leaderboard reset left rows: all_time=%d windows=%d", allTimeRows, windowRows)
	}
}

func TestReplaceBountyBoardEntriesSerializesConcurrentSnapshots(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	now := time.Now()
	bot := &Bot{
		ID: "bounty-race-bot", APIKeyID: "bounty-race-key", Name: "Bounty Race Bot", AvatarColor: "#123456",
		DefaultWeapon: "sword", DefaultStats: JSONBStats{"hp": 5, "speed": 5, "attack": 5, "defense": 5},
		DefaultFallback: "aggressive", CreatedAt: now, UpdatedAt: now,
	}
	if err := CreateAPIKeyAndBot(ctx, bot.APIKeyID, "bounty-race-hash", "arena_bounty_race", "127.0.0.1", bot); err != nil {
		t.Fatalf("CreateAPIKeyAndBot: %v", err)
	}

	// Hold both empty-table DELETE statements open long enough for their
	// subsequent INSERTs to overlap. Without a transaction-scoped writer lock,
	// one replacement loses the primary-key race for the shared bot snapshot.
	if _, err := Pool.Exec(ctx, `
		CREATE FUNCTION delay_bounty_board_delete() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
			PERFORM pg_sleep(0.25);
			RETURN NULL;
		END
		$$;
		CREATE TRIGGER delay_bounty_board_delete
		AFTER DELETE ON bounty_board
		FOR EACH STATEMENT EXECUTE FUNCTION delay_bounty_board_delete()
	`); err != nil {
		t.Fatalf("install bounty persistence overlap trigger: %v", err)
	}

	entries := []BountyBoardEntry{{
		BotID: bot.ID, Name: bot.Name, AvatarColor: bot.AvatarColor, Weapon: bot.DefaultWeapon,
		WinStreak: 3, BountyPoints: 75, Claims: 1, IsTarget: true,
	}}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < cap(results); i++ {
		go func() {
			<-start
			results <- ReplaceBountyBoardEntries(ctx, entries)
		}()
	}
	close(start)
	for i := 0; i < cap(results); i++ {
		if err := <-results; err != nil {
			t.Fatalf("concurrent ReplaceBountyBoardEntries call %d: %v", i+1, err)
		}
	}

	var rows int
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM bounty_board WHERE bot_id = $1`, bot.ID).Scan(&rows); err != nil {
		t.Fatalf("count persisted bounty rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("persisted bounty rows = %d, want 1", rows)
	}
}

func TestInterruptActiveRoundsReconcilesOnlyPreexistingRounds(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	for i, id := range []string{"interrupted-killer", "interrupted-victim"} {
		keyID := fmt.Sprintf("interrupted-key-%d", i)
		bot := &Bot{
			ID: id, APIKeyID: keyID, Name: id, AvatarColor: "#123456",
			DefaultWeapon: "sword", DefaultStats: JSONBStats{"hp": 5, "speed": 5, "attack": 5, "defense": 5},
			DefaultFallback: "aggressive", CreatedAt: now, UpdatedAt: now,
		}
		if err := CreateAPIKeyAndBot(ctx, keyID, fmt.Sprintf("interrupted-hash-%d", i), fmt.Sprintf("arena_interrupted_%d", i), "127.0.0.1", bot); err != nil {
			t.Fatalf("CreateAPIKeyAndBot(%s): %v", id, err)
		}
	}

	ended := now.Add(-time.Minute)
	for _, round := range []*Round{
		{ID: "stale-active-with-kills", RoundNumber: 102, StartedAt: now.Add(-5 * time.Minute), Status: "active"},
		{ID: "stale-active-empty", RoundNumber: 7, StartedAt: now.Add(-3 * time.Minute), Status: "active"},
		{ID: "already-completed", RoundNumber: 101, StartedAt: now.Add(-10 * time.Minute), EndedAt: &ended, Status: "completed"},
	} {
		if err := CreateRound(ctx, round); err != nil {
			t.Fatalf("CreateRound(%s): %v", round.ID, err)
		}
	}
	roundID := "stale-active-with-kills"
	if err := InsertKillLog(ctx, &KillLog{
		ID: "interrupted-kill", RoundID: &roundID,
		KillerID: "interrupted-killer", VictimID: "interrupted-victim",
		Weapon: "sword", Damage: 25, KillerHP: 50, Tick: 42, CreatedAt: now.Add(-4 * time.Minute),
	}); err != nil {
		t.Fatalf("InsertKillLog: %v", err)
	}

	changed, err := InterruptActiveRounds(ctx)
	if err != nil {
		t.Fatalf("InterruptActiveRounds: %v", err)
	}
	if changed != 2 {
		t.Fatalf("interrupted rows = %d, want 2", changed)
	}

	rows, err := Pool.Query(ctx, `SELECT id, status, ended_at IS NULL FROM rounds ORDER BY id`)
	if err != nil {
		t.Fatalf("read reconciled rounds: %v", err)
	}
	got := map[string]struct {
		status      string
		endedAtNull bool
	}{}
	for rows.Next() {
		var id, status string
		var endedAtNull bool
		if err := rows.Scan(&id, &status, &endedAtNull); err != nil {
			rows.Close()
			t.Fatalf("scan reconciled round: %v", err)
		}
		got[id] = struct {
			status      string
			endedAtNull bool
		}{status: status, endedAtNull: endedAtNull}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("iterate reconciled rounds: %v", err)
	}
	rows.Close()
	if got["stale-active-with-kills"].status != "interrupted" || !got["stale-active-with-kills"].endedAtNull ||
		got["stale-active-empty"].status != "interrupted" || !got["stale-active-empty"].endedAtNull {
		t.Fatalf("reconciled active rounds = %+v", got)
	}
	if got["already-completed"].status != "completed" || got["already-completed"].endedAtNull {
		t.Fatalf("completed round changed = %+v", got["already-completed"])
	}
	var killRows int
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM kill_log WHERE round_id = $1`, roundID).Scan(&killRows); err != nil {
		t.Fatalf("count preserved kill logs: %v", err)
	}
	if killRows != 1 {
		t.Fatalf("preserved kill logs = %d, want 1", killRows)
	}

	changed, err = InterruptActiveRounds(ctx)
	if err != nil || changed != 0 {
		t.Fatalf("idempotent InterruptActiveRounds = (%d, %v), want (0, nil)", changed, err)
	}
	if err := CreateRound(ctx, &Round{
		ID: "new-runtime-round", RoundNumber: 1, StartedAt: now, Status: "active",
	}); err != nil {
		t.Fatalf("CreateRound(new-runtime-round): %v", err)
	}
	var status string
	if err := Pool.QueryRow(ctx, `SELECT status FROM rounds WHERE id = 'new-runtime-round'`).Scan(&status); err != nil {
		t.Fatalf("read new runtime round: %v", err)
	}
	if status != "active" {
		t.Fatalf("new runtime round status = %q, want active", status)
	}
}

func TestInterruptActiveRoundsWaitsForInFlightRoundCreation(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	creating, err := Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin concurrent round creation: %v", err)
	}
	t.Cleanup(func() { _ = creating.Rollback(context.Background()) })
	if _, err := creating.Exec(ctx, `
		INSERT INTO rounds (id, round_number, started_at, status)
		VALUES ('in-flight-round', 1, NOW(), 'active')
	`); err != nil {
		t.Fatalf("insert uncommitted round: %v", err)
	}

	type result struct {
		changed int64
		err     error
	}
	done := make(chan result, 1)
	go func() {
		changed, err := InterruptActiveRounds(context.Background())
		done <- result{changed: changed, err: err}
	}()
	select {
	case got := <-done:
		t.Fatalf("reconciliation returned before round creation committed: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
	if err := creating.Commit(ctx); err != nil {
		t.Fatalf("commit concurrent round creation: %v", err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.changed != 1 {
			t.Fatalf("InterruptActiveRounds after concurrent create = (%d, %v), want (1, nil)", got.changed, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reconciliation remained blocked after round creation committed")
	}
	var status string
	if err := Pool.QueryRow(ctx, `SELECT status FROM rounds WHERE id = 'in-flight-round'`).Scan(&status); err != nil {
		t.Fatalf("read concurrently created round: %v", err)
	}
	if status != "interrupted" {
		t.Fatalf("concurrently created round status = %q, want interrupted", status)
	}
}

func TestRuntimeLeaseEnforcesSingleArenaServer(t *testing.T) {
	ctx := useFreshPostgresSchema(t)

	first, err := AcquireRuntimeLease(ctx)
	if err != nil {
		t.Fatalf("AcquireRuntimeLease(first): %v", err)
	}
	t.Cleanup(func() { _ = first.Close(context.Background()) })
	if _, err := AcquireRuntimeLease(ctx); !errors.Is(err, ErrRuntimeLeaseHeld) {
		t.Fatalf("AcquireRuntimeLease(concurrent) error = %v, want ErrRuntimeLeaseHeld", err)
	}
	if err := first.Close(ctx); err != nil {
		t.Fatalf("close first runtime lease: %v", err)
	}

	replacement, err := AcquireRuntimeLease(ctx)
	if err != nil {
		t.Fatalf("AcquireRuntimeLease(after release): %v", err)
	}
	if err := replacement.Close(ctx); err != nil {
		t.Fatalf("close replacement runtime lease: %v", err)
	}
}

func TestRuntimeLeaseMonitorReportsSessionLoss(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	lease, err := AcquireRuntimeLease(ctx)
	if err != nil {
		t.Fatalf("AcquireRuntimeLease: %v", err)
	}
	t.Cleanup(func() { _ = lease.Close(context.Background()) })
	backendPID := lease.conn.Conn().PgConn().PID()

	monitorCtx, monitorCancel := context.WithCancel(context.Background())
	defer monitorCancel()
	monitorDone := make(chan error, 1)
	go func() { monitorDone <- lease.Monitor(monitorCtx) }()
	var terminated bool
	if err := Pool.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, backendPID).Scan(&terminated); err != nil {
		t.Fatalf("terminate runtime lease backend: %v", err)
	}
	if !terminated {
		t.Fatal("PostgreSQL did not terminate the runtime lease backend")
	}
	select {
	case err := <-monitorDone:
		if err == nil {
			t.Fatal("runtime lease monitor returned nil after session loss")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("runtime lease monitor did not detect session loss")
	}

	replacement, err := AcquireRuntimeLease(ctx)
	if err != nil {
		t.Fatalf("AcquireRuntimeLease after session loss: %v", err)
	}
	if err := replacement.Close(ctx); err != nil {
		t.Fatalf("close replacement runtime lease: %v", err)
	}
}

func TestNormalizeEloRatingsRepairsCurrentAndWindowBoards(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	now := time.Now()
	bot := &Bot{
		ID: "elo-repair-bot", APIKeyID: "elo-repair-key", Name: "Elo Repair Bot", AvatarColor: "#123456",
		DefaultWeapon: "sword", DefaultStats: JSONBStats{"hp": 5, "speed": 5, "attack": 5, "defense": 5},
		DefaultFallback: "aggressive", CreatedAt: now, UpdatedAt: now,
	}
	if err := CreateAPIKeyAndBot(ctx, bot.APIKeyID, "elo-repair-hash", "arena_elo_repair", "127.0.0.1", bot); err != nil {
		t.Fatalf("CreateAPIKeyAndBot: %v", err)
	}
	// Insert impossible legacy values directly: public write helpers now clamp
	// at the persistence boundary, while startup normalization still has to
	// repair rows created by older releases.
	if _, err := Pool.Exec(ctx, `INSERT INTO bot_stats (bot_id, elo, rounds_played, updated_at)
		VALUES ($1, 193480, 1, $2)`, bot.ID, now); err != nil {
		t.Fatalf("insert legacy bot_stats Elo: %v", err)
	}
	if _, err := Pool.Exec(ctx, `INSERT INTO round_bot_stats (round_number, bot_id, bot_name, weapon, elo)
		VALUES (1, $1, $2, 'sword', -50)`, bot.ID, bot.Name); err != nil {
		t.Fatalf("insert legacy round_bot_stats Elo: %v", err)
	}

	changed, err := NormalizeEloRatings(ctx, 100, 3000)
	if err != nil {
		t.Fatalf("NormalizeEloRatings: %v", err)
	}
	if changed != 2 {
		t.Fatalf("changed rows = %d, want 2", changed)
	}
	var current, historical int
	if err := Pool.QueryRow(ctx, `
		SELECT (SELECT elo FROM bot_stats WHERE bot_id = $1),
		       (SELECT elo FROM round_bot_stats WHERE bot_id = $1)`, bot.ID).Scan(&current, &historical); err != nil {
		t.Fatalf("read normalized Elo: %v", err)
	}
	if current != 3000 || historical != 100 {
		t.Fatalf("normalized Elo = %d/%d, want 3000/100", current, historical)
	}

	changed, err = NormalizeEloRatings(ctx, 100, 3000)
	if err != nil || changed != 0 {
		t.Fatalf("idempotent normalize = (%d, %v), want (0, nil)", changed, err)
	}
}

func TestEloPersistenceWritesClampToConfiguredBounds(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	previousConfig := config.C
	config.C.EloMin = 800
	config.C.EloMax = 1200
	config.C.EloStarting = 1000
	t.Cleanup(func() { config.C = previousConfig })

	now := time.Now()
	bot := &Bot{
		ID: "elo-write-bot", APIKeyID: "elo-write-key", Name: "Elo Write Bot", AvatarColor: "#654321",
		DefaultWeapon: "sword", DefaultStats: JSONBStats{"hp": 5, "speed": 5, "attack": 5, "defense": 5},
		DefaultFallback: "aggressive", CreatedAt: now, UpdatedAt: now,
	}
	if err := CreateAPIKeyAndBot(ctx, bot.APIKeyID, "elo-write-hash", "arena_elo_write", "127.0.0.1", bot); err != nil {
		t.Fatalf("CreateAPIKeyAndBot: %v", err)
	}

	if err := ApplyBotStatsDelta(ctx, &BotStatsDelta{BotID: bot.ID, Elo: 9999, CapturedAt: now}); err != nil {
		t.Fatalf("ApplyBotStatsDelta: %v", err)
	}
	var current int
	if err := Pool.QueryRow(ctx, `SELECT elo FROM bot_stats WHERE bot_id = $1`, bot.ID).Scan(&current); err != nil {
		t.Fatalf("read delta Elo: %v", err)
	}
	if current != 1200 {
		t.Fatalf("delta Elo = %d, want upper bound 1200", current)
	}

	if err := UpsertBotStats(ctx, &BotStats{BotID: bot.ID, Elo: -50, UpdatedAt: now.Add(time.Second)}); err != nil {
		t.Fatalf("UpsertBotStats: %v", err)
	}
	if err := Pool.QueryRow(ctx, `SELECT elo FROM bot_stats WHERE bot_id = $1`, bot.ID).Scan(&current); err != nil {
		t.Fatalf("read upsert Elo: %v", err)
	}
	if current != 800 {
		t.Fatalf("upsert Elo = %d, want lower bound 800", current)
	}

	if err := CreateRound(ctx, &Round{
		ID: "elo-round-1", RoundNumber: 1, StartedAt: now, Status: "completed",
	}); err != nil {
		t.Fatalf("CreateRound: %v", err)
	}
	if err := InsertRoundBotStats(ctx, "elo-round-1", 1, bot.ID, bot.Name, "sword", 0, 0, 0, 0, 0, 0, 0, 0, 0, 9999, false); err != nil {
		t.Fatalf("InsertRoundBotStats: %v", err)
	}
	var historical int
	if err := Pool.QueryRow(ctx, `SELECT elo FROM round_bot_stats WHERE bot_id = $1`, bot.ID).Scan(&historical); err != nil {
		t.Fatalf("read round Elo: %v", err)
	}
	if historical != 1200 {
		t.Fatalf("round Elo = %d, want upper bound 1200", historical)
	}
}

func TestWeaponBalancePersistencePromotesLegacyAndRejectsStaleWrites(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	previousConfig := config.C
	config.C.WeaponAutoBalanceMinDamageScale = 0.70
	config.C.WeaponAutoBalanceMaxDamageScale = 1.40
	config.C.WeaponAutoBalanceMinCooldownScale = 0.75
	config.C.WeaponAutoBalanceMaxCooldownScale = 1.35
	config.C.WeaponAutoBalanceMinStep = 0.005
	config.C.WeaponAutoBalanceStartStep = 0.04
	t.Cleanup(func() { config.C = previousConfig })

	// Reproduce the pre-versioning table so startup has to migrate a real
	// persisted state and history, not merely create the latest schema.
	if _, err := Pool.Exec(ctx, `
		CREATE TABLE weapon_balance (
			weapon TEXT PRIMARY KEY,
			damage_scale DOUBLE PRECISION NOT NULL DEFAULT 1.0,
			cooldown_scale DOUBLE PRECISION NOT NULL DEFAULT 1.0,
			adjustment_scale DOUBLE PRECISION NOT NULL DEFAULT 0.05,
			rounds_tracked INT NOT NULL DEFAULT 0,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		t.Fatalf("create legacy balance table: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		INSERT INTO weapon_balance
			(weapon, damage_scale, cooldown_scale, adjustment_scale, rounds_tracked, updated_at)
		VALUES
			('staff', 0.97, 1.19, 0.01, 999, $1),
			('sword', 0.10, 3.00, 0.09, 888, $1)`, now.Add(time.Hour)); err != nil {
		t.Fatalf("insert legacy balance state: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		CREATE TABLE weapon_balance_history (
			id BIGSERIAL PRIMARY KEY,
			weapon TEXT NOT NULL,
			rounds_tracked INT NOT NULL DEFAULT 0,
			damage_scale DOUBLE PRECISION NOT NULL DEFAULT 1.0,
			cooldown_scale DOUBLE PRECISION NOT NULL DEFAULT 1.0,
			adjustment_scale DOUBLE PRECISION NOT NULL DEFAULT 0.05,
			avg_score DOUBLE PRECISION NOT NULL DEFAULT 0,
			mean_score DOUBLE PRECISION NOT NULL DEFAULT 0,
			diff_pct DOUBLE PRECISION NOT NULL DEFAULT 0,
			damage_delta DOUBLE PRECISION NOT NULL DEFAULT 0,
			cooldown_delta DOUBLE PRECISION NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		t.Fatalf("create legacy balance history table: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		INSERT INTO weapon_balance_history
			(weapon, rounds_tracked, damage_scale, cooldown_scale, adjustment_scale, created_at)
		VALUES ('staff', 999, 0.97, 1.19, 0.01, $1)`, now.Add(time.Hour)); err != nil {
		t.Fatalf("insert legacy balance history: %v", err)
	}
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	rows, err := ListWeaponBalances(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("migrated balance read = (%+v, %v), want two current rows", rows, err)
	}
	migrated := make(map[string]WeaponBalance, len(rows))
	for _, row := range rows {
		migrated[row.Weapon] = row
	}
	staff := migrated["staff"]
	if staff.DamageScale != 0.97 || staff.CooldownScale != 1.19 || staff.AdjustmentScale != 0.04 || staff.RoundsTracked != 0 || staff.Revision != 0 {
		t.Fatalf("migrated Staff = %+v, want scales 0.97/1.19 with fresh step/counter", staff)
	}
	sword := migrated["sword"]
	if sword.DamageScale != 0.70 || sword.CooldownScale != 1.35 || sword.AdjustmentScale != 0.04 || sword.RoundsTracked != 0 || sword.Revision != 0 {
		t.Fatalf("clamped migrated sword = %+v, want scales 0.70/1.35 with fresh step/counter", sword)
	}
	if history, err := ListWeaponBalanceHistory(ctx, 10); err != nil || len(history) != 0 {
		t.Fatalf("legacy balance history read = (%+v, %v), want no current rows", history, err)
	}

	current := &WeaponBalance{
		Weapon: "staff", DamageScale: 0.96, CooldownScale: 1.08,
		AdjustmentScale: 0.04, RoundsTracked: 12, Revision: 12, UpdatedAt: staff.UpdatedAt.Add(time.Second),
	}
	if err := UpsertWeaponBalance(ctx, current); err != nil {
		t.Fatalf("write current state: %v", err)
	}
	stale := &WeaponBalance{
		Weapon: "staff", DamageScale: 1.40, CooldownScale: 0.75,
		AdjustmentScale: 0.04, RoundsTracked: 2, Revision: 2, UpdatedAt: staff.UpdatedAt.Add(365 * 24 * time.Hour),
	}
	if err := UpsertWeaponBalance(ctx, stale); err != nil {
		t.Fatalf("stale upsert: %v", err)
	}

	rows, err = ListWeaponBalances(ctx)
	if err != nil {
		t.Fatalf("ListWeaponBalances: %v", err)
	}
	migrated = make(map[string]WeaponBalance, len(rows))
	for _, row := range rows {
		migrated[row.Weapon] = row
	}
	staff = migrated["staff"]
	if staff.DamageScale != current.DamageScale || staff.CooldownScale != current.CooldownScale ||
		staff.RoundsTracked != current.RoundsTracked || staff.Revision != current.Revision || !staff.UpdatedAt.Equal(current.UpdatedAt) {
		t.Fatalf("Staff after stale write = %+v, want %+v", staff, current)
	}
	// A logically newer balance must win even when the application clock moves
	// backward relative to the previously persisted snapshot.
	clockRollbackNewer := &WeaponBalance{
		Weapon: "staff", DamageScale: 0.95, CooldownScale: 1.09,
		AdjustmentScale: 0.04, RoundsTracked: 13, Revision: 13,
		UpdatedAt: staff.UpdatedAt.Add(-365 * 24 * time.Hour),
	}
	if err := UpsertWeaponBalance(ctx, clockRollbackNewer); err != nil {
		t.Fatalf("clock-rollback upsert: %v", err)
	}
	rows, err = ListWeaponBalances(ctx)
	if err != nil {
		t.Fatalf("ListWeaponBalances after clock rollback: %v", err)
	}
	migrated = make(map[string]WeaponBalance, len(rows))
	for _, row := range rows {
		migrated[row.Weapon] = row
	}
	staff = migrated["staff"]
	if staff.DamageScale != clockRollbackNewer.DamageScale || staff.CooldownScale != clockRollbackNewer.CooldownScale ||
		staff.RoundsTracked != clockRollbackNewer.RoundsTracked || staff.Revision != clockRollbackNewer.Revision ||
		!staff.UpdatedAt.Equal(clockRollbackNewer.UpdatedAt) {
		t.Fatalf("Staff after clock rollback = %+v, want %+v", staff, clockRollbackNewer)
	}
	var currentVersions int
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM weapon_balance WHERE algorithm_version = $1`, weaponBalanceAlgorithmVersion).Scan(&currentVersions); err != nil {
		t.Fatalf("count algorithm versions: %v", err)
	}
	if currentVersions != 2 {
		t.Fatalf("current algorithm rows = %d, want 2", currentVersions)
	}
	if err := InsertWeaponBalanceHistory(ctx, &WeaponBalanceHistory{
		Weapon: "staff", RoundsTracked: 12, DamageScale: 0.96, CooldownScale: 1.08,
		AdjustmentScale: 0.05, Revision: 12, CreatedAt: now,
	}); err != nil {
		t.Fatalf("InsertWeaponBalanceHistory: %v", err)
	}
	history, err := ListWeaponBalanceHistory(ctx, 10)
	if err != nil || len(history) != 1 || history[0].RoundsTracked != 12 || history[0].Revision != 12 {
		t.Fatalf("current balance history read = (%+v, %v), want one current row", history, err)
	}
}

func TestRecentWeaponPerformanceUsesPersistedRoundIdentityAcrossRestarts(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	samples := []struct {
		id      string
		round   int
		kills   int
		started time.Time
	}{
		{id: "old-run-98", round: 98, kills: 100, started: now},
		{id: "old-run-99", round: 99, kills: 100, started: now.Add(time.Minute)},
		// Simulate a restart plus a wall clock correction: these are persisted
		// later, but have low local numbers and older timestamps.
		{id: "new-run-1", round: 1, kills: 10, started: now.Add(-365 * 24 * time.Hour)},
		{id: "new-run-2", round: 2, kills: 20, started: now.Add(-365*24*time.Hour + time.Minute)},
	}
	for _, sample := range samples {
		if err := CreateRound(ctx, &Round{
			ID: sample.id, RoundNumber: sample.round, StartedAt: sample.started,
			BotsParticipated: 2, Status: "completed",
		}); err != nil {
			t.Fatalf("CreateRound(%s): %v", sample.id, err)
		}
		for bot := 0; bot < 2; bot++ {
			botID := fmt.Sprintf("%s-bot-%d", sample.id, bot)
			if _, err := Pool.Exec(ctx, `
				INSERT INTO round_bot_stats
					(round_id, round_number, bot_id, bot_name, weapon, kills, created_at)
				VALUES ($1, $2, $3, $3, 'staff', $4, $5)`,
				sample.id, sample.round, botID, sample.kills, sample.started); err != nil {
				t.Fatalf("insert %s stats: %v", sample.id, err)
			}
		}
	}
	var oldOrder, restartedOrder int64
	if err := Pool.QueryRow(ctx, `
		SELECT
			(SELECT persisted_order FROM rounds WHERE id = 'old-run-99'),
			(SELECT persisted_order FROM rounds WHERE id = 'new-run-1')`).Scan(&oldOrder, &restartedOrder); err != nil {
		t.Fatalf("read persisted round order: %v", err)
	}
	if restartedOrder <= oldOrder {
		t.Fatalf("post-restart persisted order = %d, want greater than old run %d", restartedOrder, oldOrder)
	}

	rows, err := ListRecentWeaponPerformance(ctx, 2)
	if err != nil {
		t.Fatalf("ListRecentWeaponPerformance: %v", err)
	}
	if len(rows) != 1 || rows[0].Weapon != "staff" || rows[0].Rounds != 2 || rows[0].Bots != 4 || rows[0].AvgKills != 15 {
		t.Fatalf("recent performance = %+v, want only post-restart rounds 1-2 with four bots and avg kills 15", rows)
	}
}

func TestRoundBotStatsSchemaBackfillsOnlyUnambiguousRoundIdentity(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	now := time.Now().UTC().Truncate(time.Second)
	oldClock := now
	newClock := now.Add(-365 * 24 * time.Hour)

	if _, err := Pool.Exec(ctx, `
		CREATE TABLE rounds (
			id TEXT PRIMARY KEY,
			round_number INT NOT NULL,
			started_at TIMESTAMPTZ NOT NULL,
			ended_at TIMESTAMPTZ,
			bots_participated INT NOT NULL DEFAULT 0,
			mvp_bot_id TEXT,
			status TEXT NOT NULL DEFAULT 'active'
		)`); err != nil {
		t.Fatalf("create legacy rounds: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		INSERT INTO rounds (id, round_number, started_at, ended_at, status)
		VALUES
			('legacy-old', 1, $1, $1, 'completed'),
			('legacy-restarted', 1, $2, NULL, 'active'),
			('legacy-unique', 7, $2, NULL, 'active')`, oldClock, newClock); err != nil {
		t.Fatalf("insert legacy rounds: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		CREATE TABLE round_bot_stats (
			id SERIAL PRIMARY KEY,
			round_number INT NOT NULL,
			bot_id TEXT NOT NULL,
			bot_name TEXT NOT NULL DEFAULT '',
			weapon TEXT NOT NULL DEFAULT '',
			kills INT NOT NULL DEFAULT 0,
			deaths INT NOT NULL DEFAULT 0,
			damage_dealt BIGINT NOT NULL DEFAULT 0,
			damage_taken BIGINT NOT NULL DEFAULT 0,
			longest_life_secs INT NOT NULL DEFAULT 0,
			shots_fired INT NOT NULL DEFAULT 0,
			shots_hit INT NOT NULL DEFAULT 0,
			pickups INT NOT NULL DEFAULT 0,
			distance DOUBLE PRECISION NOT NULL DEFAULT 0,
			elo INT NOT NULL DEFAULT 1000,
			won BOOLEAN NOT NULL DEFAULT false,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		t.Fatalf("create legacy round stats: %v", err)
	}
	if _, err := Pool.Exec(ctx, `
		INSERT INTO round_bot_stats (round_number, bot_id, bot_name, weapon, kills, created_at)
		VALUES
			(1, 'legacy-old-bot', 'old', 'staff', 100, $1),
			-- This row belongs to the restarted round, but its database timestamp
			-- looks closer to the old application clock. It must remain unmapped.
			(1, 'legacy-new-bot', 'new', 'staff', 7, $1),
			-- A single candidate is safe even with clock skew and no ended_at.
			(7, 'legacy-unique-bot', 'unique', 'sword', 3, $1)`, oldClock); err != nil {
		t.Fatalf("insert legacy round stats: %v", err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if err := EnsureCoreSchema(ctx); err != nil {
			t.Fatalf("EnsureCoreSchema migration attempt %d: %v", attempt, err)
		}
	}

	mapped := map[string]string{}
	rows, err := Pool.Query(ctx, `SELECT bot_id, COALESCE(round_id, '') FROM round_bot_stats ORDER BY id`)
	if err != nil {
		t.Fatalf("read migrated round identities: %v", err)
	}
	for rows.Next() {
		var botID, roundID string
		if err := rows.Scan(&botID, &roundID); err != nil {
			t.Fatalf("scan migrated round identity: %v", err)
		}
		mapped[botID] = roundID
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated round identities: %v", err)
	}
	rows.Close()
	if mapped["legacy-old-bot"] != "" || mapped["legacy-new-bot"] != "" ||
		mapped["legacy-unique-bot"] != "legacy-unique" {
		t.Fatalf("migrated round identities = %v", mapped)
	}
	var validated bool
	if err := Pool.QueryRow(ctx, `
		SELECT convalidated
		FROM pg_constraint
		WHERE conrelid = 'round_bot_stats'::regclass
		  AND conname = 'round_bot_stats_round_id_fkey'`).Scan(&validated); err != nil {
		t.Fatalf("read round identity foreign key: %v", err)
	}
	if !validated {
		t.Fatal("round_bot_stats round identity foreign key was not validated")
	}
	if err := InsertRoundBotStats(ctx, "missing-round", 9, "missing-bot", "Missing", "sword",
		0, 0, 0, 0, 0, 0, 0, 0, 0, 1000, false); err == nil {
		t.Fatal("unknown round identity bypassed round_bot_stats foreign key")
	}

	performance, err := ListRecentWeaponPerformance(ctx, 1)
	if err != nil {
		t.Fatalf("ListRecentWeaponPerformance: %v", err)
	}
	if len(performance) != 1 || performance[0].Weapon != "sword" || performance[0].Rounds != 1 ||
		performance[0].Bots != 1 || performance[0].AvgKills != 3 {
		t.Fatalf("post-migration recent performance = %+v, want only confidently mapped unique round", performance)
	}
}

func TestWeaponKillStatsMergeDerivedDamageSources(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}

	now := time.Now().UTC()
	for i, id := range []string{"kill-stats-killer", "kill-stats-victim"} {
		keyID := "kill-stats-key-" + strconv.Itoa(i)
		bot := &Bot{
			ID: id, APIKeyID: keyID, Name: id, AvatarColor: "#123456",
			DefaultWeapon: "staff", DefaultStats: JSONBStats{"hp": 5, "speed": 5, "attack": 5, "defense": 5},
			DefaultFallback: "aggressive", CreatedAt: now, UpdatedAt: now,
		}
		if err := CreateAPIKeyAndBot(ctx, keyID, "kill-stats-hash-"+strconv.Itoa(i), "arena_kill_stats_"+strconv.Itoa(i), "127.0.0.1", bot); err != nil {
			t.Fatalf("CreateAPIKeyAndBot(%s): %v", id, err)
		}
	}

	for i, kill := range []struct {
		weapon string
		damage int
	}{
		{weapon: "staff", damage: 10},
		{weapon: "staff_burn", damage: 15},
		{weapon: "grapple_slam", damage: 20},
	} {
		if err := InsertKillLog(ctx, &KillLog{
			ID: "kill-stats-" + strconv.Itoa(i), KillerID: "kill-stats-killer", VictimID: "kill-stats-victim",
			Weapon: kill.weapon, Damage: kill.damage, CreatedAt: now,
		}); err != nil {
			t.Fatalf("InsertKillLog(%s): %v", kill.weapon, err)
		}
	}

	stats, err := ListWeaponKillStats(ctx)
	if err != nil {
		t.Fatalf("ListWeaponKillStats: %v", err)
	}
	want := []WeaponKillStats{
		{Weapon: "grapple", Kills: 1, Kills24h: 1, Kills1h: 1, FinisherDamage: 20},
		{Weapon: "staff", Kills: 2, Kills24h: 2, Kills1h: 2, FinisherDamage: 25},
	}
	if !reflect.DeepEqual(stats, want) {
		t.Fatalf("weapon kill stats = %#v, want %#v", stats, want)
	}
}
