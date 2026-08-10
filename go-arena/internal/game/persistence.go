package game

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"arena-server/internal/config"
	"arena-server/internal/db"

	"github.com/google/uuid"
)

var (
	botStatsPersistenceMu    sync.Mutex
	botStatsPersistenceEpoch atomic.Uint64
	pendingBotStatsDeltas    = make(map[string]db.BotStatsDelta)
	applyBotStatsDeltas      = db.ApplyBotStatsDeltas
	insertRoundBotStats      = db.InsertRoundBotStatsBatch
)

// PersistBotStatsFromSnapshot saves accumulated round stats using pre-copied
// stat snapshots. This avoids data races because the snapshot values are
// copied under the engine lock before the goroutine starts.
func PersistBotStatsFromSnapshot(ctx context.Context, snaps []BotStatsSnapshot, winnerID string, finalizeRound bool) {
	botStatsPersistenceMu.Lock()
	defer botStatsPersistenceMu.Unlock()

	for _, snap := range snaps {
		if snap.PersistenceEpoch != botStatsPersistenceEpoch.Load() {
			continue
		}
		queueBotStatsDeltaLocked(botStatsDeltaFromSnapshot(snap, winnerID, finalizeRound))
	}
	flushBotStatsDeltasLocked(ctx)
}

func botStatsDeltaFromSnapshot(snap BotStatsSnapshot, winnerID string, finalizeRound bool) db.BotStatsDelta {
	roundsPlayed := 0
	roundWins := 0
	if finalizeRound {
		roundsPlayed = 1
		if snap.BotID == winnerID {
			roundWins = 1
		}
	}
	tickRate := max(1, snap.TickRate)
	lifeSecs := int(math.Round(float64(snap.RoundLongestLife) / float64(tickRate)))
	capturedAt := snap.CapturedAt
	if capturedAt.IsZero() {
		capturedAt = time.Now()
	}
	// takeBotStatsSnapshot clamps Elo before asynchronous persistence. Do not
	// reread mutable runtime config from this goroutine.
	return db.BotStatsDelta{
		BotID:            snap.BotID,
		Kills:            snap.KillsDelta,
		Deaths:           snap.DeathsDelta,
		DamageDealt:      snap.DamageDealtDelta,
		DamageTaken:      snap.DamageTakenDelta,
		CurrentStreak:    snap.KillStreak,
		BestStreak:       snap.BestStreak,
		Elo:              snap.Elo,
		LongestLifeSecs:  lifeSecs,
		RoundsPlayed:     roundsPlayed,
		RoundWins:        roundWins,
		PickupsCollected: snap.PickupsDelta,
		DistanceTraveled: snap.DistanceDelta,
		CapturedAt:       capturedAt,
	}
}

func queueBotStatsDeltaLocked(delta db.BotStatsDelta) {
	pending, exists := pendingBotStatsDeltas[delta.BotID]
	if !exists {
		pendingBotStatsDeltas[delta.BotID] = delta
		return
	}
	pending.Kills += delta.Kills
	pending.Deaths += delta.Deaths
	pending.DamageDealt += delta.DamageDealt
	pending.DamageTaken += delta.DamageTaken
	pending.RoundsPlayed += delta.RoundsPlayed
	pending.RoundWins += delta.RoundWins
	pending.PickupsCollected += delta.PickupsCollected
	pending.DistanceTraveled += delta.DistanceTraveled
	if delta.BestStreak > pending.BestStreak {
		pending.BestStreak = delta.BestStreak
	}
	if delta.LongestLifeSecs > pending.LongestLifeSecs {
		pending.LongestLifeSecs = delta.LongestLifeSecs
	}
	if !delta.CapturedAt.Before(pending.CapturedAt) {
		pending.CurrentStreak = delta.CurrentStreak
		pending.Elo = delta.Elo
		pending.CapturedAt = delta.CapturedAt
	}
	pendingBotStatsDeltas[delta.BotID] = pending
}

func flushBotStatsDeltasLocked(ctx context.Context) {
	if len(pendingBotStatsDeltas) == 0 {
		return
	}
	botIDs := make([]string, 0, len(pendingBotStatsDeltas))
	for botID := range pendingBotStatsDeltas {
		botIDs = append(botIDs, botID)
	}
	sort.Strings(botIDs)
	deltas := make([]db.BotStatsDelta, 0, len(botIDs))
	for _, botID := range botIDs {
		deltas = append(deltas, pendingBotStatsDeltas[botID])
	}
	// One batched round trip instead of one Exec per bot. The batch is
	// implicitly transactional (single Sync point), so treat the flush as
	// all-or-nothing: only clear pending state when the whole batch committed;
	// on any error keep every delta queued — queueBotStatsDeltaLocked merges
	// additively, so the next flush retries the same totals.
	if err := applyBotStatsDeltas(ctx, deltas); err != nil {
		slog.Error("persist: failed to apply bot stats deltas", "bots", len(deltas), "error", err)
		return
	}
	clear(pendingBotStatsDeltas)
}

// PersistSingleBot saves a single bot's stats, typically called on
// disconnect.
func PersistSingleBot(ctx context.Context, snapshot BotStatsSnapshot) {
	PersistBotStatsFromSnapshot(ctx, []BotStatsSnapshot{snapshot}, "", false)
}

// PersistRoundBotStats serializes the time-window source with leaderboard
// resets. An epoch captured before a successful reset is stale even if its
// goroutine was scheduled afterward, so it must not recreate truncated rows.
func PersistRoundBotStats(ctx context.Context, epoch uint64, roundID string, roundNumber int, bots map[string]*BotState, winnerID string) {
	botStatsPersistenceMu.Lock()
	defer botStatsPersistenceMu.Unlock()
	if epoch != botStatsPersistenceEpoch.Load() {
		return
	}
	if roundID == "" {
		slog.Error("persist: refusing round stats without a durable round identity", "round", roundNumber)
		return
	}

	botIDs := make([]string, 0, len(bots))
	for botID := range bots {
		botIDs = append(botIDs, botID)
	}
	sort.Strings(botIDs)
	rows := make([]db.RoundBotStatsRow, 0, len(botIDs))
	for _, botID := range botIDs {
		bot := bots[botID]
		if bot == nil {
			continue
		}
		lifeSecs := int(math.Round(float64(bot.RoundLongestLife) / math.Max(1, float64(config.C.TickRate))))
		rows = append(rows, db.RoundBotStatsRow{
			BotID:           bot.BotID,
			BotName:         bot.Name,
			Weapon:          bot.Weapon,
			Kills:           bot.RoundKills,
			Deaths:          bot.RoundDeaths,
			DamageDealt:     int64(bot.RoundDamageDealt),
			DamageTaken:     int64(bot.RoundDamageTaken),
			LongestLifeSecs: lifeSecs,
			ShotsFired:      bot.RoundShotsFired,
			ShotsHit:        bot.RoundShotsHit,
			Pickups:         bot.RoundPickups,
			Distance:        bot.RoundDistance,
			Elo:             ClampElo(bot.Elo),
			Won:             bot.BotID == winnerID,
		})
	}
	// Single multi-row insert: one DB round trip per round end instead of one
	// per bot, and a shorter botStatsPersistenceMu hold.
	if err := insertRoundBotStats(ctx, roundID, roundNumber, rows); err != nil {
		slog.Error("persist: failed to insert round bot stats", "bots", len(rows), "round_id", roundID, "round", roundNumber, "error", err)
	}
}

// logRetention is the operator-configured window for purging log-style data
// (kill_log plus the tables listed in db.PruneLogTables). Weapon stats only
// need 24h of kill_log rows (all-time totals live in weapon_kill_totals),
// and the widest time-window leaderboard reads round_bot_stats, which the
// sweep never touches.
func logRetention() time.Duration {
	hours := config.C.LogRetentionHours
	if hours <= 0 {
		hours = 48
	}
	return time.Duration(hours) * time.Hour
}

// PruneLogDataOnce deletes log-style rows older than the retention window.
// Called from a background goroutine at startup and hourly; never on the
// tick goroutine.
func PruneLogDataOnce() {
	if db.Pool == nil {
		return
	}
	cutoff := time.Now().Add(-logRetention())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	deleted, err := db.PruneKillLog(ctx, cutoff, 5000)
	if err != nil {
		slog.Warn("persist: kill_log prune failed", "deleted_before_error", deleted, "error", err)
	} else if deleted > 0 {
		slog.Info("persist: kill_log pruned", "deleted", deleted)
	}
	tables, err := db.PruneLogTables(ctx, cutoff, 5000)
	if err != nil {
		slog.Warn("persist: log table prune failed", "deleted_before_error", tables, "error", err)
	} else if len(tables) > 0 {
		slog.Info("persist: log tables pruned", "deleted", tables)
	}
}

// InsertKillLog records a kill event in the database.
func InsertKillLog(ctx context.Context, roundID, killerID, victimID, weapon string, damage float64, killerHP, tick int) {
	var rID *string
	if roundID != "" {
		rID = &roundID
	}

	entry := &db.KillLog{
		ID:        uuid.New().String(),
		RoundID:   rID,
		KillerID:  killerID,
		VictimID:  victimID,
		Weapon:    weapon,
		Damage:    int(damage),
		KillerHP:  killerHP,
		Tick:      tick,
		CreatedAt: time.Now(),
	}

	if err := db.InsertKillLog(ctx, entry); err != nil {
		slog.Error("persist: failed to insert kill log", "error", err)
	}
}
