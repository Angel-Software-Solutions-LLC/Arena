package db

import (
	"context"
	"testing"
	"time"
)

func mustExec(t *testing.T, ctx context.Context, sql string, args ...any) {
	t.Helper()
	if _, err := Pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func countRows(t *testing.T, ctx context.Context, table string) int {
	t.Helper()
	var n int
	if err := Pool.QueryRow(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestPruneLogTablesRespectsRetentionAndLeaderboard(t *testing.T) {
	ctx := useFreshPostgresSchema(t)
	if err := EnsureCoreSchema(ctx); err != nil {
		t.Fatalf("EnsureCoreSchema: %v", err)
	}
	if err := EnsureCosmeticsSchema(ctx); err != nil {
		t.Fatalf("EnsureCosmeticsSchema: %v", err)
	}
	if err := EnsureChatSchema(ctx); err != nil {
		t.Fatalf("EnsureChatSchema: %v", err)
	}
	if err := EnsureServiceNoticeEventsTable(ctx); err != nil {
		t.Fatalf("EnsureServiceNoticeEventsTable: %v", err)
	}
	if err := EnsurePlatformAuthoritySchema(ctx); err != nil {
		t.Fatalf("EnsurePlatformAuthoritySchema: %v", err)
	}
	if err := EnsureRoundBotStatsTable(ctx); err != nil {
		t.Fatalf("EnsureRoundBotStatsTable: %v", err)
	}

	old := time.Now().Add(-72 * time.Hour)
	cutoff := time.Now().Add(-48 * time.Hour)

	// rounds: an old finished round is purged; an old but still-active round
	// and a recent round both survive.
	mustExec(t, ctx, `INSERT INTO rounds (id, round_number, started_at, ended_at, status)
		VALUES ('round-old', 1, $1, $1, 'completed'),
		       ('round-stuck', 2, $1, NULL, 'active'),
		       ('round-new', 3, NOW(), NOW(), 'completed')`, old)

	// round_bot_stats is leaderboard data: even ancient rows survive, and
	// deleting their parent round only nulls the FK.
	mustExec(t, ctx, `INSERT INTO round_bot_stats (round_id, round_number, bot_id, bot_name, elo, won, created_at)
		VALUES ('round-old', 1, 'bot-1', 'Vet', 1200, true, $1)`, old)

	mustExec(t, ctx, `INSERT INTO chat_ban_log (account_id, minutes, reason, created_at)
		VALUES ('acct', 10, 'old', $1), ('acct', 10, 'new', NOW())`, old)

	mustExec(t, ctx, `INSERT INTO rate_limits (ip_address, keys_generated, window_start)
		VALUES ('198.51.100.1', 1, $1), ('198.51.100.2', 1, NOW())`, old)

	mustExec(t, ctx, `INSERT INTO cosmetic_catalog_audit (actor, action, entity_type, entity_id, created_at)
		VALUES ('admin', 'create', 'item', 'x', $1), ('admin', 'update', 'item', 'x', NOW())`, old)

	mustExec(t, ctx, `INSERT INTO platform_idempotency_records
		(operation, idempotency_key, request_hash, response, subject_kind, subject_id, revision, created_at)
		VALUES ('op', 'k-old', decode(repeat('ab', 32), 'hex'), '{}', 'account', 'a', 1, $1),
		       ('op', 'k-new', decode(repeat('cd', 32), 'hex'), '{}', 'account', 'a', 1, NOW())`, old)

	// service_notice_events: the newest event per slot is the live state and
	// must survive any age; older superseded events are purged.
	mustExec(t, ctx, `INSERT INTO service_notice_events (slot, active, severity, message, created_at)
		VALUES ('broadcast', true, 'info', 'superseded', $1),
		       ('broadcast', false, 'info', 'current-broadcast', $1),
		       ('maintenance', true, 'warning', 'current-maintenance', $1)`, old)

	// batchSize 1 exercises the batching loop.
	deleted, err := PruneLogTables(ctx, cutoff, 1)
	if err != nil {
		t.Fatalf("PruneLogTables: %v", err)
	}

	want := map[string]int64{
		"rounds":                       1,
		"chat_ban_log":                 1,
		"rate_limits":                  1,
		"cosmetic_catalog_audit":       1,
		"platform_idempotency_records": 1,
		"service_notice_events":        1,
	}
	for table, count := range want {
		if deleted[table] != count {
			t.Errorf("deleted[%s] = %d, want %d (full map: %v)", table, deleted[table], count, deleted)
		}
	}

	if got := countRows(t, ctx, "rounds"); got != 2 {
		t.Errorf("rounds rows = %d, want 2 (active + recent must survive)", got)
	}
	if got := countRows(t, ctx, "round_bot_stats"); got != 1 {
		t.Errorf("round_bot_stats rows = %d, want 1 (leaderboard data must never be pruned)", got)
	}
	var orphanRoundID *string
	if err := Pool.QueryRow(ctx, `SELECT round_id FROM round_bot_stats WHERE bot_id = 'bot-1'`).Scan(&orphanRoundID); err != nil {
		t.Fatalf("read round_bot_stats: %v", err)
	}
	if orphanRoundID != nil {
		t.Errorf("round_bot_stats.round_id = %v, want NULL after parent round purge", *orphanRoundID)
	}
	var survivors []string
	rows, err := Pool.Query(ctx, `SELECT message FROM service_notice_events ORDER BY id`)
	if err != nil {
		t.Fatalf("read service_notice_events: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			t.Fatalf("scan notice: %v", err)
		}
		survivors = append(survivors, m)
	}
	if len(survivors) != 2 || survivors[0] != "current-broadcast" || survivors[1] != "current-maintenance" {
		t.Errorf("service_notice_events survivors = %v, want newest event per slot", survivors)
	}

	// A second sweep is a no-op: nothing else has aged past the cutoff.
	deleted, err = PruneLogTables(ctx, cutoff, 1)
	if err != nil {
		t.Fatalf("PruneLogTables second sweep: %v", err)
	}
	if len(deleted) != 0 {
		t.Errorf("second sweep deleted %v, want nothing", deleted)
	}
}
