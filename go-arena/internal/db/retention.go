package db

import (
	"context"
	"fmt"
	"time"
)

// logRetentionTargets lists every log-style table the periodic retention
// sweep clears, with the delete predicate for rows older than the cutoff.
//
// Deliberately absent — data that looks like a log but is not one:
//   - bot_stats, round_bot_stats, bounty_board, weapon_kill_totals: the
//     leaderboard (see resetLeaderboardSQL for the authoritative list).
//     Time-window leaderboards read round_bot_stats.created_at directly, so
//     purging rounds does not affect them (round_id FKs are ON DELETE SET
//     NULL).
//   - the retired Stripe ledger (cosmetic_orders, cosmetic_payment_events,
//     cosmetic_subscription_events, cosmetic_licenses and friends): nothing
//     writes them since commerce moved to Angel Accounts, and an operator
//     who still has them keeps them as a record.
//   - platform_changes, platform_agent_link_events: replay-cursor feed and
//     revision replay guard, not log spam.
//   - consent_acceptances: legal record; the published privacy policy
//     promises retention for the life of the account.
//   - chat_messages, weapon_balance_history: already bounded by stricter
//     row-count caps (50 rows / 200 rows per weapon).
//
// kill_log is swept separately by PruneKillLog (same cutoff, same batching)
// because it predates this sweep and keeps its own aggregate invariants.
var logRetentionTargets = []struct {
	name string
	sql  string
}{
	// Finished match history. Active rounds are never touched; the FK from
	// kill_log and round_bot_stats is ON DELETE SET NULL.
	{"rounds", `
		DELETE FROM rounds
		WHERE ctid IN (
			SELECT ctid FROM rounds
			WHERE started_at < $1 AND status <> 'active'
			LIMIT $2
		)`},
	// Chat moderation audit trail. Live ban state lives on
	// customer_accounts.chat_banned_until, so purging history un-bans nobody.
	{"chat_ban_log", `
		DELETE FROM chat_ban_log
		WHERE ctid IN (
			SELECT ctid FROM chat_ban_log
			WHERE created_at < $1
			LIMIT $2
		)`},
	// Key-generation rate-limit rows; only the last hour is ever read.
	{"rate_limits", `
		DELETE FROM rate_limits
		WHERE ctid IN (
			SELECT ctid FROM rate_limits
			WHERE window_start < $1
			LIMIT $2
		)`},
	// Catalog admin audit log.
	{"cosmetic_catalog_audit", `
		DELETE FROM cosmetic_catalog_audit
		WHERE ctid IN (
			SELECT ctid FROM cosmetic_catalog_audit
			WHERE created_at < $1
			LIMIT $2
		)`},
	// Idempotency records exist to absorb client retries; the retention
	// window is far beyond any realistic retry horizon, and the table ships
	// a created_at index for exactly this purge.
	{"platform_idempotency_records", `
		DELETE FROM platform_idempotency_records
		WHERE ctid IN (
			SELECT ctid FROM platform_idempotency_records
			WHERE created_at < $1
			LIMIT $2
		)`},
	// Service-notice event stream. The newest event per slot IS the live
	// broadcast/maintenance state, so it survives regardless of age.
	{"service_notice_events", `
		DELETE FROM service_notice_events
		WHERE ctid IN (
			SELECT ctid FROM service_notice_events
			WHERE created_at < $1
			  AND id NOT IN (
				SELECT MAX(id) FROM service_notice_events GROUP BY slot
			  )
			LIMIT $2
		)`},
}

// PruneLogTables deletes rows older than the cutoff from every log-style
// table, in bounded batches per table (same rationale as PruneKillLog: the
// first sweep after a long gap must not create one giant delete/vacuum
// spike). Returns per-table deletion counts for the tables that had rows to
// delete. A failure on one table is reported but does not stop the sweep.
func PruneLogTables(ctx context.Context, olderThan time.Time, batchSize int) (map[string]int64, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	if batchSize <= 0 {
		batchSize = 5000
	}
	deleted := make(map[string]int64)
	var firstErr error
	for _, target := range logRetentionTargets {
		for {
			tag, err := Pool.Exec(ctx, target.sql, olderThan, batchSize)
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("prune %s: %w", target.name, err)
				}
				break
			}
			if tag.RowsAffected() > 0 {
				deleted[target.name] += tag.RowsAffected()
			}
			if tag.RowsAffected() < int64(batchSize) {
				break
			}
		}
	}
	return deleted, firstErr
}
