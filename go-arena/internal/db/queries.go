package db

import (
	"context"
	"fmt"
	"sort"
	"time"

	"arena-server/internal/config"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// weaponBalanceAlgorithmVersion prevents statistically incompatible state
// from an older balancing loop from silently seeding the current controller.
const weaponBalanceAlgorithmVersion = 2

// EnsureCoreSchema creates or repairs the database tables required by the
// runtime. It is intentionally idempotent so a fresh Postgres volume can be
// bootstrapped on first server start without a separate migration step.
func EnsureCoreSchema(ctx context.Context) error {
	if Pool == nil {
		return nil
	}

	tx, err := Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("EnsureCoreSchema begin: %w", err)
	}
	defer tx.Rollback(ctx)

	statements := []string{
		`CREATE TABLE IF NOT EXISTS api_keys (
			id TEXT PRIMARY KEY,
			key_hash TEXT NOT NULL,
			key_prefix TEXT NOT NULL UNIQUE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_seen TIMESTAMPTZ,
			is_active BOOLEAN NOT NULL DEFAULT true,
			ip_created TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_api_keys_active_prefix
			ON api_keys (key_prefix) WHERE is_active = true`,
		`CREATE TABLE IF NOT EXISTS bots (
			id TEXT PRIMARY KEY,
			api_key_id TEXT NOT NULL UNIQUE REFERENCES api_keys(id) ON DELETE CASCADE,
			name TEXT NOT NULL DEFAULT 'Unnamed Bot',
			avatar_color TEXT NOT NULL DEFAULT '#888888',
			default_weapon TEXT NOT NULL DEFAULT 'sword',
			default_stats JSONB NOT NULL DEFAULT '{}'::jsonb,
			default_fallback TEXT NOT NULL DEFAULT 'aggressive',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_bots_api_key_id ON bots (api_key_id)`,
		`CREATE TABLE IF NOT EXISTS bot_stats (
			bot_id TEXT PRIMARY KEY REFERENCES bots(id) ON DELETE CASCADE,
			kills INT NOT NULL DEFAULT 0,
			deaths INT NOT NULL DEFAULT 0,
			assists INT NOT NULL DEFAULT 0,
			damage_dealt BIGINT NOT NULL DEFAULT 0,
			damage_taken BIGINT NOT NULL DEFAULT 0,
			current_streak INT NOT NULL DEFAULT 0,
			best_streak INT NOT NULL DEFAULT 0,
			elo INT NOT NULL DEFAULT 1000,
			time_alive_seconds BIGINT NOT NULL DEFAULT 0,
			longest_life_secs INT NOT NULL DEFAULT 0,
			rounds_played INT NOT NULL DEFAULT 0,
			round_wins INT NOT NULL DEFAULT 0,
			pickups_collected INT NOT NULL DEFAULT 0,
			distance_traveled DOUBLE PRECISION NOT NULL DEFAULT 0,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_bot_stats_elo ON bot_stats (elo DESC)`,
		`CREATE TABLE IF NOT EXISTS rounds (
			id TEXT PRIMARY KEY,
			persisted_order BIGSERIAL NOT NULL,
			round_number INT NOT NULL,
			started_at TIMESTAMPTZ NOT NULL,
			ended_at TIMESTAMPTZ,
			bots_participated INT NOT NULL DEFAULT 0,
			mvp_bot_id TEXT REFERENCES bots(id) ON DELETE SET NULL,
			status TEXT NOT NULL DEFAULT 'active'
		)`,
		`DO $$
		BEGIN
			IF EXISTS (
				SELECT 1
				FROM pg_constraint
				WHERE conrelid = 'rounds'::regclass
				  AND conname = 'rounds_round_number_key'
			) THEN
				ALTER TABLE rounds DROP CONSTRAINT rounds_round_number_key;
			END IF;
		END
		$$`,
		`ALTER TABLE rounds
			ADD COLUMN IF NOT EXISTS persisted_order BIGSERIAL`,
		`UPDATE rounds SET persisted_order = DEFAULT WHERE persisted_order IS NULL`,
		`ALTER TABLE rounds ALTER COLUMN persisted_order SET NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_rounds_persisted_order
			ON rounds (persisted_order)`,
		`CREATE INDEX IF NOT EXISTS idx_rounds_round_number ON rounds (round_number DESC)`,
		// Retention sweeps delete finished rounds by age (see
		// db.PruneLogTables). Without this the predicate falls back to a
		// sequential scan of the whole table on every hourly sweep.
		`CREATE INDEX IF NOT EXISTS idx_rounds_started_at ON rounds (started_at)`,
		`CREATE TABLE IF NOT EXISTS kill_log (
			id TEXT PRIMARY KEY,
			round_id TEXT REFERENCES rounds(id) ON DELETE SET NULL,
			killer_id TEXT NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
			victim_id TEXT NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
			weapon TEXT NOT NULL,
			damage INT NOT NULL DEFAULT 0,
			killer_hp INT NOT NULL DEFAULT 0,
			tick INT NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_kill_log_round_id ON kill_log (round_id)`,
		`CREATE INDEX IF NOT EXISTS idx_kill_log_created_at ON kill_log (created_at DESC)`,
		// Running all-time per-weapon totals, maintained by InsertKillLog, so
		// ListWeaponKillStats never needs a full scan of the unbounded
		// kill_log table. Raw (non-canonicalized) weapon sources are stored;
		// staff_burn/grapple_slam merging stays at read time.
		`CREATE TABLE IF NOT EXISTS weapon_kill_totals (
			weapon TEXT PRIMARY KEY,
			kills BIGINT NOT NULL DEFAULT 0,
			finisher_damage BIGINT NOT NULL DEFAULT 0
		)`,
		// Seed the running totals from pre-existing kill_log rows exactly once
		// (first boot after the aggregate table is introduced); the
		// uncorrelated NOT EXISTS makes this a no-op when any totals exist.
		`INSERT INTO weapon_kill_totals (weapon, kills, finisher_damage)
		 SELECT weapon, COUNT(*), COALESCE(SUM(damage), 0)
		 FROM kill_log
		 WHERE NOT EXISTS (SELECT 1 FROM weapon_kill_totals)
		 GROUP BY weapon`,
		`CREATE TABLE IF NOT EXISTS rate_limits (
			ip_address TEXT PRIMARY KEY,
			keys_generated INT NOT NULL DEFAULT 0,
			window_start TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_rate_limits_window_start
			ON rate_limits (window_start DESC)`,
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS weapon_balance (
			weapon TEXT PRIMARY KEY,
			damage_scale DOUBLE PRECISION NOT NULL DEFAULT 1.0,
			cooldown_scale DOUBLE PRECISION NOT NULL DEFAULT 1.0,
			adjustment_scale DOUBLE PRECISION NOT NULL DEFAULT 0.05,
			rounds_tracked INT NOT NULL DEFAULT 0,
			revision BIGINT NOT NULL DEFAULT 0,
			algorithm_version INT NOT NULL DEFAULT %d,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`, weaponBalanceAlgorithmVersion),
		// Existing rows predate versioned controller state. Mark them as legacy
		// while making all newly inserted rows default to the current version.
		`ALTER TABLE weapon_balance
			ADD COLUMN IF NOT EXISTS algorithm_version INT NOT NULL DEFAULT 1`,
		`ALTER TABLE weapon_balance
			ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 0`,
		`UPDATE weapon_balance SET revision = 0 WHERE revision IS NULL`,
		`ALTER TABLE weapon_balance ALTER COLUMN revision SET DEFAULT 0`,
		`ALTER TABLE weapon_balance ALTER COLUMN revision SET NOT NULL`,
		fmt.Sprintf(`ALTER TABLE weapon_balance
			ALTER COLUMN algorithm_version SET DEFAULT %d`, weaponBalanceAlgorithmVersion),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS weapon_balance_history (
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
			revision BIGINT NOT NULL DEFAULT 0,
			algorithm_version INT NOT NULL DEFAULT %d,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`, weaponBalanceAlgorithmVersion),
		`ALTER TABLE weapon_balance_history
			ADD COLUMN IF NOT EXISTS algorithm_version INT NOT NULL DEFAULT 1`,
		`ALTER TABLE weapon_balance_history
			ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 0`,
		`UPDATE weapon_balance_history SET revision = 0 WHERE revision IS NULL`,
		`ALTER TABLE weapon_balance_history ALTER COLUMN revision SET DEFAULT 0`,
		`ALTER TABLE weapon_balance_history ALTER COLUMN revision SET NOT NULL`,
		fmt.Sprintf(`ALTER TABLE weapon_balance_history
			ALTER COLUMN algorithm_version SET DEFAULT %d`, weaponBalanceAlgorithmVersion),
		`CREATE INDEX IF NOT EXISTS idx_weapon_balance_history_weapon_created
			ON weapon_balance_history (weapon, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_weapon_balance_history_weapon_revision
			ON weapon_balance_history (weapon, revision DESC, id DESC)`,
		`CREATE TABLE IF NOT EXISTS bounty_board (
			bot_id TEXT PRIMARY KEY REFERENCES bots(id) ON DELETE CASCADE,
			name TEXT NOT NULL DEFAULT '',
			avatar_color TEXT NOT NULL DEFAULT '#888888',
			weapon TEXT NOT NULL DEFAULT 'sword',
			win_streak INT NOT NULL DEFAULT 0,
			bounty_points INT NOT NULL DEFAULT 0,
			claims INT NOT NULL DEFAULT 0,
			is_target BOOLEAN NOT NULL DEFAULT false,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
	}

	for _, stmt := range statements {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("EnsureCoreSchema exec: %w", err)
		}
	}

	// A new controller epoch invalidates accumulated evidence and sensitivity,
	// not the effective live balance players already experienced. Promote every
	// older row generically, preserving its damage/cooldown corrections inside
	// the current safety rails while starting fresh counters and adjustment step.
	minDamage, maxDamage := config.WeaponAutoBalanceDamageBounds()
	minCooldown, maxCooldown := config.WeaponAutoBalanceCooldownBounds()
	_, startStep := config.WeaponAutoBalanceStepBounds()
	if _, err := tx.Exec(ctx, `
		UPDATE weapon_balance
		SET damage_scale = LEAST($3, GREATEST($2, damage_scale)),
			cooldown_scale = LEAST($5, GREATEST($4, cooldown_scale)),
			adjustment_scale = $6,
			rounds_tracked = 0,
			revision = 0,
			algorithm_version = $1,
			updated_at = NOW()
		WHERE algorithm_version < $1`,
		weaponBalanceAlgorithmVersion,
		minDamage, maxDamage,
		minCooldown, maxCooldown,
		startStep,
	); err != nil {
		return fmt.Errorf("EnsureCoreSchema migrate weapon balance epoch: %w", err)
	}
	// Revisions were introduced after algorithm v2 was already deployable.
	// Seed any existing v2 rows from their monotonic round counter so their
	// first post-migration snapshot cannot be mistaken for an older write.
	if _, err := tx.Exec(ctx, `
		UPDATE weapon_balance
		SET revision = GREATEST(revision, rounds_tracked::BIGINT)
		WHERE algorithm_version = $1
		  AND revision < rounds_tracked::BIGINT`, weaponBalanceAlgorithmVersion); err != nil {
		return fmt.Errorf("EnsureCoreSchema seed weapon balance revisions: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE weapon_balance_history
		SET revision = GREATEST(revision, rounds_tracked::BIGINT)
		WHERE algorithm_version = $1
		  AND revision < rounds_tracked::BIGINT`, weaponBalanceAlgorithmVersion); err != nil {
		return fmt.Errorf("EnsureCoreSchema seed weapon balance history revisions: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("EnsureCoreSchema commit: %w", err)
	}

	if err := EnsureRoundBotStatsTable(ctx); err != nil {
		return fmt.Errorf("EnsureCoreSchema round_bot_stats: %w", err)
	}
	if err := EnsureAdminTokensTable(ctx); err != nil {
		return fmt.Errorf("EnsureCoreSchema admin_tokens: %w", err)
	}
	if err := EnsureAdminRegistryTables(ctx); err != nil {
		return fmt.Errorf("EnsureCoreSchema admin_registry: %w", err)
	}
	if err := EnsureAdminOverridesSchema(ctx); err != nil {
		return fmt.Errorf("EnsureCoreSchema admin_overrides: %w", err)
	}
	if err := EnsureServiceNoticeEventsTable(ctx); err != nil {
		return fmt.Errorf("EnsureCoreSchema service_notice_events: %w", err)
	}
	if err := EnsureCosmeticsSchema(ctx); err != nil {
		return fmt.Errorf("EnsureCoreSchema cosmetics: %w", err)
	}
	if err := EnsureCosmeticOrdersSchema(ctx); err != nil {
		return fmt.Errorf("EnsureCoreSchema cosmetic orders: %w", err)
	}
	if err := EnsureCosmeticSubscriptionsSchema(ctx); err != nil {
		return fmt.Errorf("EnsureCoreSchema cosmetic subscriptions: %w", err)
	}
	if err := EnsureCosmeticAdminMembershipsSchema(ctx); err != nil {
		return fmt.Errorf("EnsureCoreSchema cosmetic admin memberships: %w", err)
	}
	if err := EnsurePlatformAuthoritySchema(ctx); err != nil {
		return fmt.Errorf("EnsureCoreSchema platform authority: %w", err)
	}
	// Depends on customer_accounts from the cosmetics schema above.
	if err := EnsureCustomerSessionsSchema(ctx); err != nil {
		return fmt.Errorf("EnsureCoreSchema customer sessions: %w", err)
	}
	// Depends on customer_accounts and cosmetic_items from the schemas above.
	if err := EnsureCustomerEntitlementsSchema(ctx); err != nil {
		return fmt.Errorf("EnsureCoreSchema customer entitlements: %w", err)
	}
	if err := EnsureConsentSchema(ctx); err != nil {
		return fmt.Errorf("EnsureCoreSchema consent: %w", err)
	}
	// Chat is off by default (its runtime_settings row seeds from
	// ARENA_CHAT_ENABLED, see EnsureChatSchema), but the schema itself is
	// always created so an admin can turn chat on live from the admin panel
	// without needing to set that env var and restart. It depends on
	// customer_accounts from the cosmetics schema above, so it stays last.
	if err := EnsureChatSchema(ctx); err != nil {
		return fmt.Errorf("EnsureCoreSchema chat: %w", err)
	}

	return nil
}

// ---------- round_bot_stats (per-round per-bot performance for time-based leaderboards) ----------

func EnsureRoundBotStatsTable(ctx context.Context) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	_, err := Pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS round_bot_stats (
			id SERIAL PRIMARY KEY,
			round_id TEXT REFERENCES rounds(id) ON DELETE SET NULL,
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
		)`)
	if err != nil {
		return err
	}
	statements := []string{
		`ALTER TABLE round_bot_stats ADD COLUMN IF NOT EXISTS round_id TEXT`,
		`ALTER TABLE round_bot_stats ADD COLUMN IF NOT EXISTS weapon TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE round_bot_stats ADD COLUMN IF NOT EXISTS longest_life_secs INT NOT NULL DEFAULT 0`,
		`ALTER TABLE round_bot_stats ADD COLUMN IF NOT EXISTS shots_fired INT NOT NULL DEFAULT 0`,
		`ALTER TABLE round_bot_stats ADD COLUMN IF NOT EXISTS shots_hit INT NOT NULL DEFAULT 0`,
		`UPDATE round_bot_stats AS r
			SET weapon = b.default_weapon
			FROM bots AS b
			WHERE r.weapon = ''
			  AND r.bot_id = b.id
			  AND b.default_weapon <> ''`,
		// Old rows did not carry the engine's UUID. A local round number is a
		// durable identity only while it is unique, so duplicate numbers from
		// separate server runs remain deliberately unmapped. Guessing by clocks
		// can silently attach telemetry to the wrong match after skew or rollback.
		`UPDATE round_bot_stats AS stats
			SET round_id = unique_round.id
			FROM (
				SELECT round_number, MIN(id) AS id
				FROM rounds
				GROUP BY round_number
				HAVING COUNT(*) = 1
			) AS unique_round
			WHERE (stats.round_id IS NULL OR stats.round_id = '')
			  AND unique_round.round_number = stats.round_number`,
		// Interim builds could write an arbitrary non-empty round_id before the
		// relationship was enforced. Null those rows so the nullable FK can be
		// added and validated without misrepresenting their identity.
		`UPDATE round_bot_stats AS stats
			SET round_id = NULL
			WHERE stats.round_id IS NOT NULL
			  AND NOT EXISTS (SELECT 1 FROM rounds WHERE rounds.id = stats.round_id)`,
		`DO $$
		BEGIN
			IF NOT EXISTS (
				SELECT 1 FROM pg_constraint
				WHERE conrelid = 'round_bot_stats'::regclass
				  AND conname = 'round_bot_stats_round_id_fkey'
			) THEN
				ALTER TABLE round_bot_stats
					ADD CONSTRAINT round_bot_stats_round_id_fkey
					FOREIGN KEY (round_id) REFERENCES rounds(id) ON DELETE SET NULL NOT VALID;
			END IF;
		END
		$$`,
		`ALTER TABLE round_bot_stats VALIDATE CONSTRAINT round_bot_stats_round_id_fkey`,
		`CREATE INDEX IF NOT EXISTS idx_rbs_created ON round_bot_stats (created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_rbs_bot ON round_bot_stats (bot_id)`,
		`CREATE INDEX IF NOT EXISTS idx_rbs_weapon ON round_bot_stats (weapon)`,
		`CREATE INDEX IF NOT EXISTS idx_rbs_round_id ON round_bot_stats (round_id)`,
		`CREATE INDEX IF NOT EXISTS idx_rbs_round_number ON round_bot_stats (round_number DESC)`,
	}
	for _, stmt := range statements {
		if _, err := Pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("EnsureRoundBotStatsTable: %w", err)
		}
	}
	return nil
}

func InsertRoundBotStats(ctx context.Context, roundID string, roundNumber int, botID, botName, weapon string,
	kills, deaths int, dmgDealt, dmgTaken int64, longestLife, shotsFired, shotsHit, pickups int, distance float64, elo int, won bool) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	if roundID == "" {
		return fmt.Errorf("InsertRoundBotStats: round identity is required")
	}
	elo = config.ClampElo(elo)
	_, err := Pool.Exec(ctx,
		`INSERT INTO round_bot_stats (round_id, round_number, bot_id, bot_name, weapon, kills, deaths, damage_dealt, damage_taken, longest_life_secs, shots_fired, shots_hit, pickups, distance, elo, won)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		roundID, roundNumber, botID, botName, weapon, kills, deaths, dmgDealt, dmgTaken, longestLife, shotsFired, shotsHit, pickups, distance, elo, won)
	return err
}

// RoundBotStatsRow is one bot's per-round stats line for batched insertion.
type RoundBotStatsRow struct {
	BotID           string
	BotName         string
	Weapon          string
	Kills           int
	Deaths          int
	DamageDealt     int64
	DamageTaken     int64
	LongestLifeSecs int
	ShotsFired      int
	ShotsHit        int
	Pickups         int
	Distance        float64
	Elo             int
	Won             bool
}

// InsertRoundBotStatsBatch inserts all bots' round stats in a single
// multi-row statement (unnest over parallel arrays), replacing one Pool.Exec
// round trip per bot at round end with one per round.
func InsertRoundBotStatsBatch(ctx context.Context, roundID string, roundNumber int, rows []RoundBotStatsRow) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	if roundID == "" {
		return fmt.Errorf("InsertRoundBotStatsBatch: round identity is required")
	}
	if len(rows) == 0 {
		return nil
	}
	n := len(rows)
	botIDs := make([]string, n)
	botNames := make([]string, n)
	weapons := make([]string, n)
	kills := make([]int32, n)
	deaths := make([]int32, n)
	dmgDealt := make([]int64, n)
	dmgTaken := make([]int64, n)
	lifeSecs := make([]int32, n)
	shotsFired := make([]int32, n)
	shotsHit := make([]int32, n)
	pickups := make([]int32, n)
	distances := make([]float64, n)
	elos := make([]int32, n)
	wons := make([]bool, n)
	for i, row := range rows {
		botIDs[i] = row.BotID
		botNames[i] = row.BotName
		weapons[i] = row.Weapon
		kills[i] = int32(row.Kills)
		deaths[i] = int32(row.Deaths)
		dmgDealt[i] = row.DamageDealt
		dmgTaken[i] = row.DamageTaken
		lifeSecs[i] = int32(row.LongestLifeSecs)
		shotsFired[i] = int32(row.ShotsFired)
		shotsHit[i] = int32(row.ShotsHit)
		pickups[i] = int32(row.Pickups)
		distances[i] = row.Distance
		elos[i] = int32(config.ClampElo(row.Elo))
		wons[i] = row.Won
	}
	_, err := Pool.Exec(ctx, `
		INSERT INTO round_bot_stats
			(round_id, round_number, bot_id, bot_name, weapon, kills, deaths,
			 damage_dealt, damage_taken, longest_life_secs, shots_fired,
			 shots_hit, pickups, distance, elo, won)
		SELECT $1, $2, u.bot_id, u.bot_name, u.weapon, u.kills, u.deaths,
			 u.damage_dealt, u.damage_taken, u.longest_life_secs, u.shots_fired,
			 u.shots_hit, u.pickups, u.distance, u.elo, u.won
		FROM unnest(
			$3::text[], $4::text[], $5::text[], $6::int[], $7::int[],
			$8::bigint[], $9::bigint[], $10::int[], $11::int[], $12::int[],
			$13::int[], $14::double precision[], $15::int[], $16::boolean[]
		) AS u(bot_id, bot_name, weapon, kills, deaths, damage_dealt,
			 damage_taken, longest_life_secs, shots_fired, shots_hit, pickups,
			 distance, elo, won)`,
		roundID, roundNumber,
		botIDs, botNames, weapons, kills, deaths, dmgDealt, dmgTaken,
		lifeSecs, shotsFired, shotsHit, pickups, distances, elos, wons,
	)
	if err != nil {
		return fmt.Errorf("InsertRoundBotStatsBatch: %w", err)
	}
	return nil
}

// EnsureAdminRegistryTables creates the small admin-managed registries used by
// the Admin Panel. These tables store validated records, not arbitrary files.
func EnsureAdminRegistryTables(ctx context.Context) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS admin_content_blocks (
			key TEXT PRIMARY KEY,
			label TEXT NOT NULL DEFAULT '',
			value TEXT NOT NULL DEFAULT '',
			published BOOLEAN NOT NULL DEFAULT true,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS custom_map_templates (
			name TEXT PRIMARY KEY,
			display_name TEXT NOT NULL,
			base_shape TEXT NOT NULL,
			seed BIGINT NOT NULL DEFAULT 1,
			enabled BOOLEAN NOT NULL DEFAULT true,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
	}
	for _, stmt := range statements {
		if _, err := Pool.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// ListRecentWeaponPerformance returns average per-weapon round score over the last N rounds.
func ListRecentWeaponPerformance(ctx context.Context, roundLimit int) ([]WeaponRecentPerformance, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := Pool.Query(ctx, `
		WITH recent_rounds AS (
			-- Walk rounds backwards by persisted_order (unique index) and keep
			-- the first $1 that have stats, instead of GROUP BY over the whole
			-- round_bot_stats x rounds join: O(limit) index probes vs O(history).
			SELECT r.id AS round_id
			FROM rounds AS r
			WHERE EXISTS (
				SELECT 1 FROM round_bot_stats AS s WHERE s.round_id = r.id
			)
			ORDER BY r.persisted_order DESC
			LIMIT $1
		)
		SELECT
			stats.weapon,
			COUNT(*)::INT AS bots,
			SUM(CASE WHEN stats.won THEN 1 ELSE 0 END)::INT AS wins,
			COUNT(DISTINCT stats.round_id)::INT AS rounds,
			AVG(
				(stats.kills * 30)::DOUBLE PRECISION +
				(stats.damage_dealt * 0.12)::DOUBLE PRECISION +
				(stats.longest_life_secs * 0.35)::DOUBLE PRECISION +
				(CASE WHEN stats.won THEN 60 ELSE 0 END)::DOUBLE PRECISION
			) AS avg_score,
			AVG(stats.kills)::DOUBLE PRECISION AS avg_kills,
			AVG(stats.damage_dealt)::DOUBLE PRECISION AS avg_damage,
			AVG(stats.longest_life_secs)::DOUBLE PRECISION AS avg_life_secs,
			COALESCE(SUM(stats.shots_fired), 0)::INT AS shots_fired,
			COALESCE(SUM(stats.shots_hit), 0)::INT AS shots_hit,
			CASE
				WHEN COALESCE(SUM(stats.shots_fired), 0) > 0
				THEN COALESCE(SUM(stats.shots_hit), 0)::DOUBLE PRECISION / SUM(stats.shots_fired)
				ELSE 0
			END AS hit_rate,
			CASE
				WHEN COALESCE(SUM(stats.shots_fired), 0) > 0
				THEN COALESCE(SUM(stats.damage_dealt), 0)::DOUBLE PRECISION / SUM(stats.shots_fired)
				ELSE 0
			END AS damage_per_shot,
			CASE
				WHEN COALESCE(SUM(stats.shots_hit), 0) > 0
				THEN COALESCE(SUM(stats.damage_dealt), 0)::DOUBLE PRECISION / SUM(stats.shots_hit)
				ELSE 0
			END AS damage_per_hit,
			CASE
				WHEN COALESCE(SUM(stats.longest_life_secs), 0) > 0
				THEN COALESCE(SUM(stats.shots_fired), 0)::DOUBLE PRECISION / SUM(stats.longest_life_secs)
				ELSE 0
			END AS shots_per_life,
			CASE
				WHEN COALESCE(SUM(stats.shots_hit), 0) > 0
				THEN COALESCE(SUM(stats.kills), 0)::DOUBLE PRECISION / SUM(stats.shots_hit)
				ELSE 0
			END AS kills_per_hit
		FROM round_bot_stats AS stats
		JOIN recent_rounds ON recent_rounds.round_id = stats.round_id
		WHERE stats.weapon <> ''
		GROUP BY stats.weapon
		ORDER BY stats.weapon
	`, roundLimit)
	if err != nil {
		return nil, fmt.Errorf("ListRecentWeaponPerformance: %w", err)
	}
	defer rows.Close()

	var items []WeaponRecentPerformance
	for rows.Next() {
		var item WeaponRecentPerformance
		if err := rows.Scan(
			&item.Weapon,
			&item.Bots,
			&item.Wins,
			&item.Rounds,
			&item.AvgScore,
			&item.AvgKills,
			&item.AvgDamage,
			&item.AvgLifeSecs,
			&item.ShotsFired,
			&item.ShotsHit,
			&item.HitRate,
			&item.DamagePerShot,
			&item.DamagePerHit,
			&item.ShotsPerLife,
			&item.KillsPerHit,
		); err != nil {
			return nil, fmt.Errorf("ListRecentWeaponPerformance scan: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListRecentWeaponPerformance rows: %w", err)
	}
	return items, nil
}

// InsertWeaponBalanceHistory stores one balance-decision snapshot.
func InsertWeaponBalanceHistory(ctx context.Context, item *WeaponBalanceHistory) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	_, err := Pool.Exec(ctx,
		`INSERT INTO weapon_balance_history
			(weapon, rounds_tracked, damage_scale, cooldown_scale, adjustment_scale, avg_score, mean_score, diff_pct, damage_delta, cooldown_delta, revision, algorithm_version, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		item.Weapon, item.RoundsTracked, item.DamageScale, item.CooldownScale, item.AdjustmentScale,
		item.AvgScore, item.MeanScore, item.DiffPct, item.DamageDelta, item.CooldownDelta,
		item.Revision, weaponBalanceAlgorithmVersion, item.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("InsertWeaponBalanceHistory: %w", err)
	}
	return nil
}

// PruneWeaponBalanceHistory deletes a weapon's history rows beyond the most
// recent keep entries (retention order: revision DESC, id DESC — the same
// order ListWeaponBalanceHistory ranks by). Nothing else ever deletes from
// weapon_balance_history, so without this the table grows ~2k rows/day and
// the ROW_NUMBER() window in ListWeaponBalanceHistory scans all of it once a
// minute. Served by idx_weapon_balance_history_weapon_revision. When the
// weapon has fewer than keep+1 rows the subquery returns no row and the
// NULL comparison deletes nothing.
func PruneWeaponBalanceHistory(ctx context.Context, weapon string, keep int) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	_, err := Pool.Exec(ctx, `
		DELETE FROM weapon_balance_history
		WHERE weapon = $1
		  AND (revision, id) < (
			SELECT revision, id FROM weapon_balance_history
			WHERE weapon = $1
			ORDER BY revision DESC, id DESC
			OFFSET $2 LIMIT 1
		  )`,
		weapon, keep,
	)
	if err != nil {
		return fmt.Errorf("PruneWeaponBalanceHistory: %w", err)
	}
	return nil
}

// ListWeaponBalanceHistory returns up to N most recent history points per weapon.
func ListWeaponBalanceHistory(ctx context.Context, perWeapon int) ([]WeaponBalanceHistory, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := Pool.Query(ctx, `
		WITH ranked AS (
			SELECT
				weapon, rounds_tracked, damage_scale, cooldown_scale, adjustment_scale,
				avg_score, mean_score, diff_pct, damage_delta, cooldown_delta, revision, created_at,
				ROW_NUMBER() OVER (PARTITION BY weapon ORDER BY revision DESC, id DESC) AS rn
			FROM weapon_balance_history
			WHERE algorithm_version = $2
		)
		SELECT
			weapon, rounds_tracked, damage_scale, cooldown_scale, adjustment_scale,
			avg_score, mean_score, diff_pct, damage_delta, cooldown_delta, revision, created_at
		FROM ranked
		WHERE rn <= $1
		ORDER BY weapon, revision ASC
	`, perWeapon, weaponBalanceAlgorithmVersion)
	if err != nil {
		return nil, fmt.Errorf("ListWeaponBalanceHistory: %w", err)
	}
	defer rows.Close()

	var items []WeaponBalanceHistory
	for rows.Next() {
		var item WeaponBalanceHistory
		if err := rows.Scan(
			&item.Weapon, &item.RoundsTracked, &item.DamageScale, &item.CooldownScale, &item.AdjustmentScale,
			&item.AvgScore, &item.MeanScore, &item.DiffPct, &item.DamageDelta, &item.CooldownDelta, &item.Revision, &item.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("ListWeaponBalanceHistory scan: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListWeaponBalanceHistory rows: %w", err)
	}
	return items, nil
}

// GetTimeBasedLeaderboard returns aggregated stats for bots within a time window.
func GetTimeBasedLeaderboard(ctx context.Context, since time.Time, sortBy string, limit int) ([]map[string]interface{}, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	validSorts := map[string]string{
		"kills":       "SUM(r.kills) DESC",
		"elo":         "MAX(r.elo) DESC",
		"kd_ratio":    "CASE WHEN SUM(r.deaths)=0 THEN SUM(r.kills) ELSE SUM(r.kills)::float/SUM(r.deaths) END DESC",
		"best_streak": "SUM(r.kills) DESC", // approx — no per-round streak tracking
		"wins":        "SUM(CASE WHEN r.won THEN 1 ELSE 0 END) DESC",
		"damage":      "SUM(r.damage_dealt) DESC",
	}
	order, ok := validSorts[sortBy]
	if !ok {
		order = validSorts["elo"]
	}

	query := fmt.Sprintf(`
		SELECT
			r.bot_id,
			MAX(r.bot_name) AS name,
			COALESCE(MAX(b.avatar_color), '#888') AS avatar_color,
			SUM(r.kills) AS kills,
			SUM(r.deaths) AS deaths,
			MAX(r.elo) AS elo,
			SUM(r.damage_dealt) AS damage_dealt,
			COUNT(*) AS rounds_played,
			SUM(CASE WHEN r.won THEN 1 ELSE 0 END) AS round_wins
		FROM round_bot_stats r
		LEFT JOIN bots b ON b.id::text = r.bot_id
		WHERE r.created_at >= $1
		  AND r.bot_name NOT LIKE 'Legacy-%%'
		GROUP BY r.bot_id
		HAVING COUNT(*) > 0
		ORDER BY %s
		LIMIT $2
	`, order)

	rows, err := Pool.Query(ctx, query, since, limit)
	if err != nil {
		return nil, fmt.Errorf("GetTimeBasedLeaderboard: %w", err)
	}
	defer rows.Close()

	var results []map[string]interface{}
	rank := 0
	for rows.Next() {
		rank++
		var botID, name, color string
		var kills, deaths, elo int
		var dmgDealt int64
		var roundsPlayed, roundWins int
		if err := rows.Scan(&botID, &name, &color, &kills, &deaths, &elo, &dmgDealt, &roundsPlayed, &roundWins); err != nil {
			return nil, fmt.Errorf("GetTimeBasedLeaderboard scan: %w", err)
		}
		results = append(results, map[string]interface{}{
			"rank":          rank,
			"bot_id":        botID,
			"name":          name,
			"avatar_color":  color,
			"kills":         kills,
			"deaths":        deaths,
			"elo":           elo,
			"damage_dealt":  dmgDealt,
			"rounds_played": roundsPlayed,
			"round_wins":    roundWins,
		})
	}
	return results, rows.Err()
}

// ---------- admin_tokens ----------

// EnsureAdminTokensTable creates the admin_tokens table if it doesn't exist.
func EnsureAdminTokensTable(ctx context.Context) error {
	if Pool == nil {
		return nil
	}
	_, err := Pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS admin_tokens (
			id TEXT PRIMARY KEY,
			label TEXT NOT NULL DEFAULT 'Admin Token',
			token_hash TEXT NOT NULL,
			token_hint TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`)
	if err != nil {
		return fmt.Errorf("EnsureAdminTokensTable: %w", err)
	}
	return nil
}

// ListAdminTokens returns all admin tokens (without the actual token, just metadata).
func ListAdminTokens(ctx context.Context) ([]map[string]interface{}, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := Pool.Query(ctx,
		`SELECT id, label, token_hint, created_at FROM admin_tokens ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("ListAdminTokens: %w", err)
	}
	defer rows.Close()
	var results []map[string]interface{}
	for rows.Next() {
		var id, label, tokenHint string
		var createdAt time.Time
		if err := rows.Scan(&id, &label, &tokenHint, &createdAt); err != nil {
			return nil, fmt.Errorf("ListAdminTokens scan: %w", err)
		}
		results = append(results, map[string]interface{}{
			"id":         id,
			"label":      label,
			"token_hint": tokenHint,
			"created_at": createdAt,
		})
	}
	return results, rows.Err()
}

// CreateAdminToken inserts a new admin token.
func CreateAdminToken(ctx context.Context, id, label, tokenHash, tokenHint string) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	_, err := Pool.Exec(ctx,
		`INSERT INTO admin_tokens (id, label, token_hash, token_hint) VALUES ($1, $2, $3, $4)`,
		id, label, tokenHash, tokenHint)
	if err != nil {
		return fmt.Errorf("CreateAdminToken: %w", err)
	}
	return nil
}

// DeleteAdminToken deletes an admin token by ID.
func DeleteAdminToken(ctx context.Context, id string) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	ct, err := Pool.Exec(ctx, `DELETE FROM admin_tokens WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("DeleteAdminToken: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("token not found")
	}
	return nil
}

// GetAllAdminTokenHashes returns all token hashes for auth checking.
func GetAllAdminTokenHashes(ctx context.Context) ([]string, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := Pool.Query(ctx, `SELECT token_hash FROM admin_tokens`)
	if err != nil {
		return nil, fmt.Errorf("GetAllAdminTokenHashes: %w", err)
	}
	defer rows.Close()
	var hashes []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, fmt.Errorf("GetAllAdminTokenHashes scan: %w", err)
		}
		hashes = append(hashes, h)
	}
	return hashes, rows.Err()
}

// ---------- api_keys ----------

const getActiveAPIKeyByPrefixSQL = `SELECT id, key_hash, key_prefix, created_at, last_seen, is_active, ip_created
 FROM api_keys WHERE key_prefix = $1 AND is_active = true`

const isAPIKeyActiveSQL = `SELECT is_active FROM api_keys WHERE id = $1`

const getActiveAPIKeyAndBotByPrefixSQL = `SELECT
 k.id, k.key_hash, k.key_prefix, k.created_at, k.last_seen, k.is_active, k.ip_created,
 COALESCE(b.id, ''), COALESCE(b.api_key_id, ''), COALESCE(b.name, ''),
 COALESCE(b.avatar_color, ''), COALESCE(b.default_weapon, ''),
 COALESCE(b.default_stats, '{}'::jsonb), COALESCE(b.default_fallback, ''),
 COALESCE(b.created_at, to_timestamp(0)), COALESCE(b.updated_at, to_timestamp(0))
 FROM api_keys k
 LEFT JOIN bots b ON b.api_key_id = k.id
 WHERE k.key_prefix = $1 AND k.is_active = true`

// GetAPIKeyByPrefix retrieves an active API key by its prefix.
func GetAPIKeyByPrefix(ctx context.Context, prefix string) (*ApiKey, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	k := &ApiKey{}
	err := Pool.QueryRow(ctx, getActiveAPIKeyByPrefixSQL, prefix).
		Scan(&k.ID, &k.KeyHash, &k.KeyPrefix, &k.CreatedAt, &k.LastSeen, &k.IsActive, &k.IPCreated)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("GetAPIKeyByPrefix: %w", err)
	}
	return k, nil
}

// GetAPIKeyAndBotByPrefix retrieves the active API key and its associated bot
// in one database round trip. Authentication still verifies the stored
// credential before returning the bot to a caller.
func GetAPIKeyAndBotByPrefix(ctx context.Context, prefix string) (*ApiKey, *Bot, error) {
	if Pool == nil {
		return nil, nil, ErrNoDatabase
	}

	key := &ApiKey{}
	bot := &Bot{}
	err := Pool.QueryRow(ctx, getActiveAPIKeyAndBotByPrefixSQL, prefix).Scan(
		&key.ID, &key.KeyHash, &key.KeyPrefix, &key.CreatedAt, &key.LastSeen, &key.IsActive, &key.IPCreated,
		&bot.ID, &bot.APIKeyID, &bot.Name, &bot.AvatarColor, &bot.DefaultWeapon,
		&bot.DefaultStats, &bot.DefaultFallback, &bot.CreatedAt, &bot.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("GetAPIKeyAndBotByPrefix: %w", err)
	}
	if bot.ID == "" {
		bot = nil
	}
	return key, bot, nil
}

// IsAPIKeyActive performs an exact-id, fail-closed admission recheck after a
// bot becomes visible to the engine. A missing row is inactive; callers can
// distinguish database failures and reject the admission without guessing.
func IsAPIKeyActive(ctx context.Context, id string) (bool, error) {
	if Pool == nil {
		return false, ErrNoDatabase
	}

	var active bool
	err := Pool.QueryRow(ctx, isAPIKeyActiveSQL, id).Scan(&active)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("IsAPIKeyActive: %w", err)
	}
	return active, nil
}

// CreateAPIKey inserts a new API key row.
func CreateAPIKey(ctx context.Context, id, keyHash, keyPrefix, ipCreated string) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	_, err := Pool.Exec(ctx, insertAPIKeySQL, id, keyHash, keyPrefix, ipCreated)
	if err != nil {
		return fmt.Errorf("CreateAPIKey: %w", err)
	}
	return nil
}

// DeactivateAPIKey sets is_active = false for the given key.
func DeactivateAPIKey(ctx context.Context, id string) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	_, err := Pool.Exec(ctx,
		`UPDATE api_keys SET is_active = false WHERE id = $1`, id,
	)
	if err != nil {
		return fmt.Errorf("DeactivateAPIKey: %w", err)
	}
	return nil
}

// UpdateAPIKeyLastSeen sets last_seen to NOW() for the given key.
func UpdateAPIKeyLastSeen(ctx context.Context, id string) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	_, err := Pool.Exec(ctx,
		`UPDATE api_keys SET last_seen = NOW() WHERE id = $1`, id,
	)
	if err != nil {
		return fmt.Errorf("UpdateAPIKeyLastSeen: %w", err)
	}
	return nil
}

const updateAPIKeyHashAndLastSeenSQL = `UPDATE api_keys
 SET key_hash = $2, last_seen = NOW()
 WHERE id = $1`

type apiKeyAuthExecer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// UpdateAPIKeyHashAndLastSeen atomically upgrades a verified legacy bcrypt
// credential to the rollback-safe composite and records its successful use in
// one database write.
func UpdateAPIKeyHashAndLastSeen(ctx context.Context, id, keyHash string) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	return updateAPIKeyHashAndLastSeen(ctx, Pool, id, keyHash)
}

func updateAPIKeyHashAndLastSeen(ctx context.Context, execer apiKeyAuthExecer, id, keyHash string) error {
	if keyHash == "" {
		return fmt.Errorf("UpdateAPIKeyHashAndLastSeen: replacement hash is required")
	}
	if _, err := execer.Exec(ctx, updateAPIKeyHashAndLastSeenSQL, id, keyHash); err != nil {
		return fmt.Errorf("UpdateAPIKeyHashAndLastSeen: %w", err)
	}
	return nil
}

// ListAllAPIKeys returns all API keys with their associated bot info.
func ListAllAPIKeys(ctx context.Context) ([]map[string]interface{}, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := Pool.Query(ctx,
		`SELECT k.id, k.key_prefix, k.created_at, k.last_seen, k.is_active, k.ip_created,
		        b.id AS bot_id, b.name AS bot_name, b.avatar_color,
		        COALESCE(s.kills, 0) AS kills, COALESCE(s.deaths, 0) AS deaths,
		        COALESCE(s.elo, 1000) AS elo, COALESCE(s.rounds_played, 0) AS rounds_played
		 FROM api_keys k
		 LEFT JOIN bots b ON b.api_key_id = k.id
		 LEFT JOIN bot_stats s ON s.bot_id = b.id
		 ORDER BY k.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("ListAllAPIKeys: %w", err)
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var (
			keyID, keyPrefix                       string
			createdAt                              time.Time
			lastSeen                               *time.Time
			isActive                               bool
			ipCreated, botID, botName, avatarColor *string
			kills, deaths, elo, roundsPlayed       int
		)
		if err := rows.Scan(&keyID, &keyPrefix, &createdAt, &lastSeen, &isActive, &ipCreated,
			&botID, &botName, &avatarColor, &kills, &deaths, &elo, &roundsPlayed); err != nil {
			return nil, fmt.Errorf("ListAllAPIKeys scan: %w", err)
		}
		entry := map[string]interface{}{
			"key_id":        keyID,
			"key_prefix":    keyPrefix,
			"created_at":    createdAt,
			"last_seen":     lastSeen,
			"is_active":     isActive,
			"ip_created":    ipCreated,
			"bot_id":        botID,
			"bot_name":      botName,
			"avatar_color":  avatarColor,
			"kills":         kills,
			"deaths":        deaths,
			"elo":           elo,
			"rounds_played": roundsPlayed,
		}
		results = append(results, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListAllAPIKeys rows: %w", err)
	}
	return results, nil
}

// ---------- bots ----------

// GetBotByAPIKeyID retrieves a bot by its associated API key ID.
func GetBotByAPIKeyID(ctx context.Context, apiKeyID string) (*Bot, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	b := &Bot{}
	err := Pool.QueryRow(ctx,
		`SELECT id, api_key_id, name, avatar_color, default_weapon, default_stats,
		        default_fallback, created_at, updated_at
		 FROM bots WHERE api_key_id = $1`, apiKeyID,
	).Scan(&b.ID, &b.APIKeyID, &b.Name, &b.AvatarColor, &b.DefaultWeapon, &b.DefaultStats,
		&b.DefaultFallback, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("GetBotByAPIKeyID: %w", err)
	}
	return b, nil
}

// UpdateBot updates mutable fields on a bot row.
func UpdateBot(ctx context.Context, bot *Bot) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	_, err := Pool.Exec(ctx,
		`UPDATE bots SET name = $1, avatar_color = $2, default_weapon = $3,
		                 default_stats = $4, default_fallback = $5, updated_at = $6
		 WHERE id = $7`,
		bot.Name, bot.AvatarColor, bot.DefaultWeapon, bot.DefaultStats,
		bot.DefaultFallback, bot.UpdatedAt, bot.ID,
	)
	if err != nil {
		return fmt.Errorf("UpdateBot: %w", err)
	}
	return nil
}

// ---------- bot_stats ----------

// NormalizeEloRatings repairs ratings produced by older asymmetric formulas
// and applies the configured bounds to both current and time-window boards.
// It is safe to run at every startup and becomes a no-op after the first pass.
func NormalizeEloRatings(ctx context.Context, minElo, maxElo int) (int64, error) {
	if Pool == nil {
		return 0, ErrNoDatabase
	}
	if minElo <= 0 || maxElo <= minElo {
		return 0, fmt.Errorf("invalid Elo bounds %d..%d", minElo, maxElo)
	}
	tx, err := Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("NormalizeEloRatings begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var changed int64
	for _, table := range []string{"bot_stats", "round_bot_stats"} {
		tag, updateErr := tx.Exec(ctx, fmt.Sprintf(
			`UPDATE %s SET elo = LEAST($2, GREATEST($1, elo)) WHERE elo < $1 OR elo > $2`, table,
		), minElo, maxElo)
		if updateErr != nil {
			return 0, fmt.Errorf("NormalizeEloRatings %s: %w", table, updateErr)
		}
		changed += tag.RowsAffected()
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("NormalizeEloRatings commit: %w", err)
	}
	return changed, nil
}

// GetBotStats retrieves stats for a given bot.
func GetBotStats(ctx context.Context, botID string) (*BotStats, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	s := &BotStats{}
	err := Pool.QueryRow(ctx,
		`SELECT bot_id, kills, deaths, assists, damage_dealt, damage_taken,
		        current_streak, best_streak, elo, time_alive_seconds, longest_life_secs,
		        rounds_played, round_wins, pickups_collected, distance_traveled, updated_at
		 FROM bot_stats WHERE bot_id = $1`, botID,
	).Scan(&s.BotID, &s.Kills, &s.Deaths, &s.Assists, &s.DamageDealt, &s.DamageTaken,
		&s.CurrentStreak, &s.BestStreak, &s.Elo, &s.TimeAliveSecs, &s.LongestLifeSecs,
		&s.RoundsPlayed, &s.RoundWins, &s.PickupsCollected, &s.DistanceTraveled, &s.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("GetBotStats: %w", err)
	}
	return s, nil
}

// UpsertBotStats inserts or updates bot_stats using ON CONFLICT.
func UpsertBotStats(ctx context.Context, stats *BotStats) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	elo := config.ClampElo(stats.Elo)
	_, err := Pool.Exec(ctx,
		`INSERT INTO bot_stats (bot_id, kills, deaths, assists, damage_dealt, damage_taken,
		                        current_streak, best_streak, elo, time_alive_seconds,
		                        longest_life_secs, rounds_played, round_wins,
		                        pickups_collected, distance_traveled, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		 ON CONFLICT (bot_id) DO UPDATE SET
		   kills = EXCLUDED.kills,
		   deaths = EXCLUDED.deaths,
		   assists = EXCLUDED.assists,
		   damage_dealt = EXCLUDED.damage_dealt,
		   damage_taken = EXCLUDED.damage_taken,
		   current_streak = EXCLUDED.current_streak,
		   best_streak = EXCLUDED.best_streak,
		   elo = EXCLUDED.elo,
		   time_alive_seconds = EXCLUDED.time_alive_seconds,
		   longest_life_secs = EXCLUDED.longest_life_secs,
		   rounds_played = EXCLUDED.rounds_played,
		   round_wins = EXCLUDED.round_wins,
		   pickups_collected = EXCLUDED.pickups_collected,
		   distance_traveled = EXCLUDED.distance_traveled,
		   updated_at = EXCLUDED.updated_at`,
		stats.BotID, stats.Kills, stats.Deaths, stats.Assists, stats.DamageDealt,
		stats.DamageTaken, stats.CurrentStreak, stats.BestStreak, elo,
		stats.TimeAliveSecs, stats.LongestLifeSecs, stats.RoundsPlayed, stats.RoundWins,
		stats.PickupsCollected, stats.DistanceTraveled, stats.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("UpsertBotStats: %w", err)
	}
	return nil
}

const applyBotStatsDeltaSQL = `INSERT INTO bot_stats (bot_id, kills, deaths, assists, damage_dealt, damage_taken,
                        current_streak, best_streak, elo, time_alive_seconds,
                        longest_life_secs, rounds_played, round_wins,
                        pickups_collected, distance_traveled, updated_at)
 VALUES ($1,$2,$3,0,$4,$5,$6,$7,$8,0,$9,$10,$11,$12,$13,$14)
 ON CONFLICT (bot_id) DO UPDATE SET
   kills = bot_stats.kills + EXCLUDED.kills,
   deaths = bot_stats.deaths + EXCLUDED.deaths,
   damage_dealt = bot_stats.damage_dealt + EXCLUDED.damage_dealt,
   damage_taken = bot_stats.damage_taken + EXCLUDED.damage_taken,
   current_streak = CASE
     WHEN EXCLUDED.updated_at >= bot_stats.updated_at THEN EXCLUDED.current_streak
     ELSE bot_stats.current_streak
   END,
   best_streak = GREATEST(bot_stats.best_streak, EXCLUDED.best_streak),
   elo = CASE
     WHEN EXCLUDED.updated_at >= bot_stats.updated_at THEN EXCLUDED.elo
     ELSE bot_stats.elo
   END,
   longest_life_secs = GREATEST(bot_stats.longest_life_secs, EXCLUDED.longest_life_secs),
   rounds_played = bot_stats.rounds_played + EXCLUDED.rounds_played,
   round_wins = bot_stats.round_wins + EXCLUDED.round_wins,
   pickups_collected = bot_stats.pickups_collected + EXCLUDED.pickups_collected,
   distance_traveled = bot_stats.distance_traveled + EXCLUDED.distance_traveled,
   updated_at = GREATEST(bot_stats.updated_at, EXCLUDED.updated_at)`

// ApplyBotStatsDelta atomically adds one captured stats delta. The arithmetic
// happens inside Postgres so overlapping background persists cannot both read
// the same old totals and overwrite each other. Snapshot-time ordering keeps
// non-additive fields from moving backwards when goroutines finish out of
// order.
func ApplyBotStatsDelta(ctx context.Context, delta *BotStatsDelta) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	elo := config.ClampElo(delta.Elo)
	_, err := Pool.Exec(ctx, applyBotStatsDeltaSQL,
		delta.BotID, delta.Kills, delta.Deaths, delta.DamageDealt, delta.DamageTaken,
		delta.CurrentStreak, delta.BestStreak, elo, delta.LongestLifeSecs,
		delta.RoundsPlayed, delta.RoundWins, delta.PickupsCollected,
		delta.DistanceTraveled, delta.CapturedAt,
	)
	if err != nil {
		return fmt.Errorf("ApplyBotStatsDelta: %w", err)
	}
	return nil
}

// ApplyBotStatsDeltas applies all captured deltas in one network round trip
// via a pgx batch. A batch outside an explicit transaction uses a single Sync
// point, making it implicitly transactional: a mid-batch failure rolls back
// statements that already reported success, so callers must treat the flush
// as all-or-nothing and keep every delta pending on error.
func ApplyBotStatsDeltas(ctx context.Context, deltas []BotStatsDelta) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	if len(deltas) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for i := range deltas {
		d := &deltas[i]
		batch.Queue(applyBotStatsDeltaSQL,
			d.BotID, d.Kills, d.Deaths, d.DamageDealt, d.DamageTaken,
			d.CurrentStreak, d.BestStreak, config.ClampElo(d.Elo), d.LongestLifeSecs,
			d.RoundsPlayed, d.RoundWins, d.PickupsCollected,
			d.DistanceTraveled, d.CapturedAt,
		)
	}
	br := Pool.SendBatch(ctx, batch)
	var execErr error
	for range deltas {
		if _, err := br.Exec(); err != nil {
			execErr = err
			break
		}
	}
	closeErr := br.Close()
	if execErr != nil {
		return fmt.Errorf("ApplyBotStatsDeltas: %w", execErr)
	}
	if closeErr != nil {
		return fmt.Errorf("ApplyBotStatsDeltas close: %w", closeErr)
	}
	return nil
}

// ---------- kill_log ----------

// InsertKillLog inserts a new kill log entry.
func InsertKillLog(ctx context.Context, log *KillLog) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	// One statement so the event row and the running aggregate can never
	// drift: either both land or neither does.
	_, err := Pool.Exec(ctx,
		`WITH ins AS (
			INSERT INTO kill_log (id, round_id, killer_id, victim_id, weapon, damage,
			                      killer_hp, tick, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			RETURNING weapon, damage
		 )
		 INSERT INTO weapon_kill_totals (weapon, kills, finisher_damage)
		 SELECT weapon, 1, damage FROM ins
		 ON CONFLICT (weapon) DO UPDATE SET
			kills = weapon_kill_totals.kills + 1,
			finisher_damage = weapon_kill_totals.finisher_damage + EXCLUDED.finisher_damage`,
		log.ID, log.RoundID, log.KillerID, log.VictimID, log.Weapon, log.Damage,
		log.KillerHP, log.Tick, log.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("InsertKillLog: %w", err)
	}
	return nil
}

// PruneKillLog deletes kill_log rows older than the retention window in
// bounded batches (so the first run after months of uptime cannot create one
// giant delete/vacuum spike). Returns total rows deleted. The running
// weapon_kill_totals aggregate preserves all-time stats, and the windowed
// 24h/1h counts only need recent rows, so pruning is invisible to
// ListWeaponKillStats.
func PruneKillLog(ctx context.Context, olderThan time.Time, batchSize int) (int64, error) {
	if Pool == nil {
		return 0, ErrNoDatabase
	}
	if batchSize <= 0 {
		batchSize = 5000
	}
	var total int64
	for {
		tag, err := Pool.Exec(ctx, `
			DELETE FROM kill_log
			WHERE ctid IN (
				SELECT ctid FROM kill_log
				WHERE created_at < $1
				LIMIT $2
			)`,
			olderThan, batchSize,
		)
		if err != nil {
			return total, fmt.Errorf("PruneKillLog: %w", err)
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < int64(batchSize) {
			return total, nil
		}
	}
}

// ---------- rounds ----------

// CreateRound inserts a new round row.
func CreateRound(ctx context.Context, round *Round) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	_, err := Pool.Exec(ctx,
		`INSERT INTO rounds (id, round_number, started_at, ended_at, bots_participated,
		                     mvp_bot_id, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		round.ID, round.RoundNumber, round.StartedAt, round.EndedAt,
		round.BotsParticipated, round.MVPBotID, round.Status,
	)
	if err != nil {
		return fmt.Errorf("CreateRound: %w", err)
	}
	return nil
}

// UpdateRound updates a round's ended_at, status, and mvp_bot_id.
func UpdateRound(ctx context.Context, round *Round) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	tag, err := Pool.Exec(ctx,
		`UPDATE rounds SET ended_at = $1, status = $2, mvp_bot_id = $3 WHERE id = $4`,
		round.EndedAt, round.Status, round.MVPBotID, round.ID,
	)
	if err != nil {
		return fmt.Errorf("UpdateRound: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// A zero-row UPDATE silently loses the round's completion; surface it.
		return fmt.Errorf("UpdateRound: round %s not found (0 rows updated)", round.ID)
	}
	return nil
}

// InterruptActiveRounds marks rounds left active by an earlier server process
// as interrupted. Startup calls this before the new engine can create a round,
// so every matching row is necessarily an orphan from a previous runtime.
// The exact stop time is unknowable after a crash or forced restart; preserve
// ended_at and all partial telemetry instead of fabricating a completed result.
func InterruptActiveRounds(ctx context.Context) (int64, error) {
	if Pool == nil {
		return 0, ErrNoDatabase
	}
	tx, err := Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("InterruptActiveRounds begin: %w", err)
	}
	defer tx.Rollback(ctx)
	// Wait for any already-running INSERT to finish before choosing the complete
	// set of orphaned rows. Normal readers remain available. The process-wide
	// runtime lease prevents a second updated server from starting new inserts.
	if _, err := tx.Exec(ctx, `LOCK TABLE rounds IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return 0, fmt.Errorf("InterruptActiveRounds lock: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE rounds SET status = 'interrupted' WHERE status = 'active'`)
	if err != nil {
		return 0, fmt.Errorf("InterruptActiveRounds: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("InterruptActiveRounds commit: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ---------- weapon_balance ----------

// ListWeaponBalances returns every persisted weapon balance row.
func ListWeaponBalances(ctx context.Context) ([]WeaponBalance, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := Pool.Query(ctx,
		`SELECT weapon, damage_scale, cooldown_scale, adjustment_scale, rounds_tracked, revision, updated_at
		 FROM weapon_balance
		 WHERE algorithm_version = $1
		 ORDER BY weapon`, weaponBalanceAlgorithmVersion)
	if err != nil {
		return nil, fmt.Errorf("ListWeaponBalances: %w", err)
	}
	defer rows.Close()

	var balances []WeaponBalance
	for rows.Next() {
		var wb WeaponBalance
		if err := rows.Scan(
			&wb.Weapon, &wb.DamageScale, &wb.CooldownScale,
			&wb.AdjustmentScale, &wb.RoundsTracked, &wb.Revision, &wb.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("ListWeaponBalances scan: %w", err)
		}
		balances = append(balances, wb)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListWeaponBalances rows: %w", err)
	}
	return balances, nil
}

// UpsertWeaponBalance stores the adaptive balance state for a weapon.
func UpsertWeaponBalance(ctx context.Context, wb *WeaponBalance) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	_, err := Pool.Exec(ctx,
		`INSERT INTO weapon_balance
			(weapon, damage_scale, cooldown_scale, adjustment_scale, rounds_tracked, revision, updated_at, algorithm_version)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (weapon) DO UPDATE SET
			damage_scale = EXCLUDED.damage_scale,
			cooldown_scale = EXCLUDED.cooldown_scale,
			adjustment_scale = EXCLUDED.adjustment_scale,
			rounds_tracked = EXCLUDED.rounds_tracked,
			revision = EXCLUDED.revision,
			updated_at = EXCLUDED.updated_at,
			algorithm_version = EXCLUDED.algorithm_version
		 WHERE weapon_balance.algorithm_version < EXCLUDED.algorithm_version
		    OR (weapon_balance.algorithm_version = EXCLUDED.algorithm_version
		        AND weapon_balance.revision < EXCLUDED.revision)`,
		wb.Weapon, wb.DamageScale, wb.CooldownScale, wb.AdjustmentScale, wb.RoundsTracked, wb.Revision, wb.UpdatedAt,
		weaponBalanceAlgorithmVersion,
	)
	if err != nil {
		return fmt.Errorf("UpsertWeaponBalance: %w", err)
	}
	return nil
}

func canonicalWeaponSource(source string) string {
	switch source {
	case "staff_burn":
		return "staff"
	case "grapple_slam":
		return "grapple"
	default:
		return source
	}
}

func mergeCanonicalWeaponKillStats(raw []WeaponKillStats) []WeaponKillStats {
	if len(raw) == 0 {
		return nil
	}
	byWeapon := make(map[string]WeaponKillStats, len(raw))
	for _, item := range raw {
		weapon := canonicalWeaponSource(item.Weapon)
		merged := byWeapon[weapon]
		merged.Weapon = weapon
		merged.Kills += item.Kills
		merged.Kills24h += item.Kills24h
		merged.Kills1h += item.Kills1h
		merged.FinisherDamage += item.FinisherDamage
		byWeapon[weapon] = merged
	}
	stats := make([]WeaponKillStats, 0, len(byWeapon))
	for _, item := range byWeapon {
		stats = append(stats, item)
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].Weapon < stats[j].Weapon })
	return stats
}

// ListWeaponKillStats returns per-weapon kill totals from the kill log.
func ListWeaponKillStats(ctx context.Context) ([]WeaponKillStats, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	// All-time totals come from the weapon_kill_totals running aggregate
	// (maintained atomically by InsertKillLog); only the 24h/1h windows touch
	// kill_log, as a bounded range scan on idx_kill_log_created_at. This keeps
	// the query O(recent kills) instead of a full scan of an unbounded table.
	rows, err := Pool.Query(ctx, `
		SELECT
			t.weapon,
			t.kills::INT AS kills,
			COALESCE(w.kills_24h, 0)::INT AS kills_24h,
			COALESCE(w.kills_1h, 0)::INT AS kills_1h,
			t.finisher_damage AS finisher_damage
		FROM weapon_kill_totals AS t
		LEFT JOIN (
			SELECT
				weapon,
				COUNT(*)::INT AS kills_24h,
				COUNT(*) FILTER (WHERE created_at >= NOW() - INTERVAL '1 hour')::INT AS kills_1h
			FROM kill_log
			WHERE created_at >= NOW() - INTERVAL '24 hours'
			GROUP BY weapon
		) AS w ON w.weapon = t.weapon
		ORDER BY t.weapon
	`)
	if err != nil {
		return nil, fmt.Errorf("ListWeaponKillStats: %w", err)
	}
	defer rows.Close()

	var raw []WeaponKillStats
	for rows.Next() {
		var item WeaponKillStats
		if err := rows.Scan(
			&item.Weapon,
			&item.Kills,
			&item.Kills24h,
			&item.Kills1h,
			&item.FinisherDamage,
		); err != nil {
			return nil, fmt.Errorf("ListWeaponKillStats scan: %w", err)
		}
		raw = append(raw, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListWeaponKillStats rows: %w", err)
	}
	return mergeCanonicalWeaponKillStats(raw), nil
}

// ---------- leaderboard ----------

// validSortColumns maps allowed sort keys to SQL ORDER BY clauses.
var validSortColumns = map[string]string{
	"kills":       "s.kills DESC",
	"elo":         "s.elo DESC",
	"streak":      "s.best_streak DESC",
	"best_streak": "s.best_streak DESC",
	"wins":        "s.round_wins DESC",
	"damage":      "s.damage_dealt DESC",
	"kd_ratio":    "CASE WHEN s.deaths = 0 THEN s.kills ELSE s.kills::float / s.deaths END DESC",
}

// GetLeaderboard returns a paginated leaderboard with rank, sorted by the given column.
func GetLeaderboard(ctx context.Context, sortBy string, limit, offset int) ([]LeaderboardEntry, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	orderClause, ok := validSortColumns[sortBy]
	if !ok {
		orderClause = validSortColumns["kills"]
	}

	query := fmt.Sprintf(
		`SELECT
		   ROW_NUMBER() OVER (ORDER BY %s) AS rank,
		   b.id, b.name, b.avatar_color,
		   s.kills, s.deaths, s.elo, s.best_streak,
		   s.damage_dealt, s.rounds_played, s.round_wins
		 FROM bot_stats s
		 JOIN bots b ON b.id = s.bot_id
		 WHERE b.name NOT LIKE 'Legacy-%%'
		   AND (s.rounds_played > 0 OR s.kills > 0 OR s.deaths > 0 OR s.damage_dealt > 0)
		 ORDER BY %s
		 LIMIT $1 OFFSET $2`, orderClause, orderClause,
	)

	rows, err := Pool.Query(ctx, query, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("GetLeaderboard: %w", err)
	}
	defer rows.Close()

	var entries []LeaderboardEntry
	for rows.Next() {
		var e LeaderboardEntry
		if err := rows.Scan(
			&e.Rank, &e.BotID, &e.Name, &e.AvatarColor,
			&e.Kills, &e.Deaths, &e.Elo, &e.BestStreak,
			&e.DamageDealt, &e.RoundsPlayed, &e.RoundWins,
		); err != nil {
			return nil, fmt.Errorf("GetLeaderboard scan: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("GetLeaderboard rows: %w", err)
	}
	return entries, nil
}

// GetLeaderboardCount returns the total number of entries in bot_stats.
func GetLeaderboardCount(ctx context.Context) (int, error) {
	if Pool == nil {
		return 0, ErrNoDatabase
	}
	var count int
	err := Pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM bot_stats s
		JOIN bots b ON b.id = s.bot_id
		WHERE b.name NOT LIKE 'Legacy-%'
		  AND (s.rounds_played > 0 OR s.kills > 0 OR s.deaths > 0 OR s.damage_dealt > 0)
	`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("GetLeaderboardCount: %w", err)
	}
	return count, nil
}

// ---------- bounty board ----------

// ListBountyBoardEntries loads the persisted public bounty board.
func ListBountyBoardEntries(ctx context.Context) ([]BountyBoardEntry, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := Pool.Query(ctx, `
		SELECT bot_id, name, avatar_color, weapon, win_streak, bounty_points, claims, is_target, updated_at
		FROM bounty_board
		ORDER BY bounty_points DESC, win_streak DESC, name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("ListBountyBoardEntries: %w", err)
	}
	defer rows.Close()

	var entries []BountyBoardEntry
	for rows.Next() {
		var entry BountyBoardEntry
		if err := rows.Scan(
			&entry.BotID, &entry.Name, &entry.AvatarColor, &entry.Weapon,
			&entry.WinStreak, &entry.BountyPoints, &entry.Claims, &entry.IsTarget, &entry.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("ListBountyBoardEntries scan: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ListBountyBoardEntries rows: %w", err)
	}
	return entries, nil
}

// ReplaceBountyBoardEntries rewrites the persisted bounty board to match the in-memory snapshot.
func ReplaceBountyBoardEntries(ctx context.Context, entries []BountyBoardEntry) error {
	if Pool == nil {
		return ErrNoDatabase
	}
	tx, err := Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("ReplaceBountyBoardEntries begin: %w", err)
	}
	defer tx.Rollback(ctx)
	// The replacement is a multi-statement snapshot write. Serialize it across
	// every server process while still allowing ordinary SELECT readers; two
	// empty-table DELETEs followed by the same INSERT otherwise race on the
	// bounty_board primary key.
	if _, err := tx.Exec(ctx, `LOCK TABLE bounty_board IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("ReplaceBountyBoardEntries lock: %w", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM bounty_board`); err != nil {
		return fmt.Errorf("ReplaceBountyBoardEntries clear: %w", err)
	}

	now := time.Now()
	for _, entry := range entries {
		if _, err := tx.Exec(ctx, `
			INSERT INTO bounty_board
				(bot_id, name, avatar_color, weapon, win_streak, bounty_points, claims, is_target, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		`,
			entry.BotID, entry.Name, entry.AvatarColor, entry.Weapon,
			entry.WinStreak, entry.BountyPoints, entry.Claims, entry.IsTarget, now,
		); err != nil {
			return fmt.Errorf("ReplaceBountyBoardEntries insert: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("ReplaceBountyBoardEntries commit: %w", err)
	}
	return nil
}

// getLatestWinnerBountySeedSQL orders by persisted_order, not round_number:
// round_number resets to 0 every process restart (it is an in-memory
// counter, never restored from the DB - see game.NewGameEngine), and this
// query's whole purpose is reseeding the bounty board after a restart, so
// ordering by it can surface rounds from a much older server run instead of
// the actually-most-recent ones. persisted_order is a DB sequence and is
// monotonic across restarts, matching how ListRecentWeaponPerformance above
// orders "last N rounds".
const getLatestWinnerBountySeedSQL = `
	SELECT r.mvp_bot_id, b.name, b.avatar_color, b.default_weapon
	FROM rounds r
	JOIN bots b ON b.id = r.mvp_bot_id
	WHERE r.status = 'completed'
	  AND r.mvp_bot_id IS NOT NULL
	  AND b.name NOT LIKE 'Legacy-%'
	ORDER BY r.persisted_order DESC
	LIMIT 32
`

// GetLatestWinnerBountySeed reconstructs a single bounty candidate from the most
// recent consecutive completed round winners. It is used to repopulate the
// bounty board after a restart if no persisted board state exists.
func GetLatestWinnerBountySeed(ctx context.Context, threshold, base, step, maxPoints int) (*BountyBoardEntry, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	rows, err := Pool.Query(ctx, getLatestWinnerBountySeedSQL)
	if err != nil {
		return nil, fmt.Errorf("GetLatestWinnerBountySeed: %w", err)
	}
	defer rows.Close()

	type winnerRow struct {
		BotID       string
		Name        string
		AvatarColor string
		Weapon      string
	}

	var winners []winnerRow
	for rows.Next() {
		var row winnerRow
		if err := rows.Scan(&row.BotID, &row.Name, &row.AvatarColor, &row.Weapon); err != nil {
			return nil, fmt.Errorf("GetLatestWinnerBountySeed scan: %w", err)
		}
		winners = append(winners, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("GetLatestWinnerBountySeed rows: %w", err)
	}
	if len(winners) == 0 {
		return nil, nil
	}

	seed := winners[0]
	streak := 0
	for _, row := range winners {
		if row.BotID != seed.BotID {
			break
		}
		streak++
	}
	if streak < threshold {
		return nil, nil
	}

	points := base + (streak-threshold)*step
	if points > maxPoints {
		points = maxPoints
	}

	return &BountyBoardEntry{
		BotID:        seed.BotID,
		Name:         seed.Name,
		AvatarColor:  seed.AvatarColor,
		Weapon:       seed.Weapon,
		WinStreak:    streak,
		BountyPoints: points,
		Claims:       0,
		IsTarget:     true,
		UpdatedAt:    time.Now(),
	}, nil
}

// GetBotRank returns the 1-based rank of a bot for a given sort column.
func GetBotRank(ctx context.Context, botID, sortBy string) (int, error) {
	if Pool == nil {
		return 0, ErrNoDatabase
	}
	orderClause, ok := validSortColumns[sortBy]
	if !ok {
		orderClause = validSortColumns["kills"]
	}

	query := fmt.Sprintf(
		`SELECT rank FROM (
		   SELECT s.bot_id, ROW_NUMBER() OVER (ORDER BY %s) AS rank
		   FROM bot_stats s
		   JOIN bots b ON b.id = s.bot_id
		   WHERE b.name NOT LIKE 'Legacy-%%'
		     AND (s.rounds_played > 0 OR s.kills > 0 OR s.deaths > 0 OR s.damage_dealt > 0)
		 ) ranked WHERE bot_id = $1`, orderClause,
	)

	var rank int
	err := Pool.QueryRow(ctx, query, botID).Scan(&rank)
	if err != nil {
		if err == pgx.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("GetBotRank: %w", err)
	}
	return rank, nil
}

// ---------- rate_limits ----------

// CheckRateLimit atomically consumes one registration slot for the given IP.
// Concurrent requests for the same IP serialize through PostgreSQL's
// ON CONFLICT row lock, so at most maxPerHour requests can be admitted.
func CheckRateLimit(ctx context.Context, ip string, maxPerHour int) (bool, int, error) {
	if Pool == nil {
		return false, 0, ErrNoDatabase
	}
	return consumeRegistrationRateLimit(ctx, Pool, ip, maxPerHour, time.Now())
}
