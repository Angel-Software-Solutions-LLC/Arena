package api

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strings"
	"time"

	"arena-server/internal/config"
	"arena-server/internal/game"
	"arena-server/internal/version"
)

// nightlyRestartSource marks maintenance notices published by the scheduler.
// The notices carry the running build as TargetCommit, so the startup
// reconcile in ServiceStatusService.Load clears them as soon as the process
// comes back up.
const nightlyRestartSource = "nightly-restart"

// nextNightlyRestart returns the next wall-clock occurrence of hour:minute
// in loc strictly after now. time.Date normalizes times that do not exist on
// a DST-transition day.
func nextNightlyRestart(now time.Time, hour, minute int, loc *time.Location) time.Time {
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, loc)
	if !next.After(now) {
		next = time.Date(now.Year(), now.Month(), now.Day()+1, hour, minute, 0, 0, loc)
	}
	return next
}

func parseRestartClock(value string) (hour, minute int, err error) {
	t, err := time.Parse("15:04", strings.TrimSpace(value))
	if err != nil {
		return 0, 0, err
	}
	return t.Hour(), t.Minute(), nil
}

// RunNightlyRestartLoop exits the process cleanly at the configured
// wall-clock time each night, after warning every connected client through
// the durable maintenance notice (banner + popup on both frontends, and the
// service_status frames bots/SDKs already handle). The container supervisor
// (docker compose restart: unless-stopped) brings the server back up, which
// also resets in-memory state like the engine tick counter.
//
// Sequence: warn at T-warn and again at T-1m, then at T give an active round
// a bounded grace window to finish before triggering the same graceful
// shutdown path as POST /admin/server/restart.
func RunNightlyRestartLoop(ctx context.Context, engine *game.GameEngine, svc *ServiceStatusService, shutdown func()) {
	hour, minute, err := parseRestartClock(config.C.NightlyRestartTime)
	if err != nil {
		slog.Error("nightly restart disabled: invalid ARENA_NIGHTLY_RESTART_TIME",
			"value", config.C.NightlyRestartTime, "error", err)
		return
	}
	loc, err := time.LoadLocation(config.C.NightlyRestartTZ)
	if err != nil {
		slog.Error("nightly restart disabled: invalid ARENA_NIGHTLY_RESTART_TZ",
			"value", config.C.NightlyRestartTZ, "error", err)
		return
	}

	warn := time.Duration(config.C.NightlyRestartWarnMinutes) * time.Minute
	if warn < time.Minute {
		warn = time.Minute
	}
	// A maintenance notice durably expires after maintenanceFallbackTTL, so a
	// single advance warning cannot usefully be published earlier than that.
	if warn > maintenanceFallbackTTL {
		warn = maintenanceFallbackTTL
	}

	target := nextNightlyRestart(time.Now().In(loc), hour, minute, loc)
	slog.Info("nightly restart scheduled",
		"at", target.Format(time.RFC3339), "warn_minutes", int(warn.Minutes()))

	if !sleepUntil(ctx, target.Add(-warn)) {
		return
	}
	publishRestartWarning(ctx, svc, target)
	if warn > time.Minute {
		if !sleepUntil(ctx, target.Add(-time.Minute)) {
			return
		}
		publishRestartWarning(ctx, svc, target)
	}
	if !sleepUntil(ctx, target) {
		return
	}

	// Give an in-progress round a bounded grace window to finish instead of
	// cutting it off mid-fight. Rounds are bounded (duration + sudden-death
	// overtime), so the default grace covers a full round tail.
	if grace := time.Duration(config.C.NightlyRestartRoundGraceSecs) * time.Second; grace > 0 && engine != nil {
		deadline := time.Now().Add(grace)
		for time.Now().Before(deadline) && engine.GetRoundPhase() == game.PhaseActive {
			if !sleepFor(ctx, 2*time.Second) {
				return
			}
		}
	}

	slog.Warn("nightly restart: shutting down; the container supervisor restarts the server")
	if svc != nil {
		nctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if _, err := svc.SetManualRestart(nctx); err != nil {
			slog.Warn("nightly restart: could not publish durable restart notice", "error", err)
		}
		cancel()
	}
	// Mirror restartServer: give the notice broadcast time to flush, then
	// trigger the graceful shutdown path (which notifies and closes every
	// WebSocket before draining HTTP).
	time.Sleep(500 * time.Millisecond)
	if shutdown != nil {
		shutdown()
		return
	}
	os.Exit(0)
}

func publishRestartWarning(ctx context.Context, svc *ServiceStatusService, target time.Time) {
	if svc == nil {
		return
	}
	mins := int(math.Round(time.Until(target).Minutes()))
	when := "in about a minute"
	if mins > 1 {
		when = fmt.Sprintf("in about %d minutes", mins)
	}
	message := fmt.Sprintf(
		"Scheduled nightly maintenance: the arena restarts %s (at %s). The current round gets a chance to finish, and connections return automatically.",
		when, target.Format("15:04 MST"))
	nctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := svc.SetMaintenance(nctx, version.ResolvedCommit(), "scheduled", message, nightlyRestartSource); err != nil {
		slog.Warn("nightly restart: could not publish warning notice", "error", err)
	}
}

// sleepUntil blocks until t or ctx cancellation; true means t was reached.
// A t in the past returns immediately.
func sleepUntil(ctx context.Context, t time.Time) bool {
	d := time.Until(t)
	if d <= 0 {
		return ctx.Err() == nil
	}
	return sleepFor(ctx, d)
}

func sleepFor(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
