package game

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"arena-server/internal/config"
	"arena-server/internal/db"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// safeGo runs fn in its own goroutine, recovering any panic so a bug (or a
// database call unexpectedly panicking, e.g. an unguarded nil db.Pool
// access) in fire-and-forget persistence work can never crash the whole
// server process the way an unrecovered goroutine panic would.
func safeGo(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("recovered panic in background goroutine", "panic", r)
			}
		}()
		fn()
	}()
}

var createRoundRecord = db.CreateRound

var roundCreateTimeout = 10 * time.Second

var errRoundCreateIncomplete = errors.New("round record creation did not complete")

// roundPersistenceResult is the completion result for one round's durable
// INSERT. Closing done publishes err to every dependent persistence goroutine.
type roundPersistenceResult struct {
	done        chan struct{}
	err         error
	skipLogOnce sync.Once
}

func newRoundPersistenceResult() *roundPersistenceResult {
	return &roundPersistenceResult{
		done: make(chan struct{}),
		err:  errRoundCreateIncomplete,
	}
}

func (result *roundPersistenceResult) wait() error {
	if result == nil {
		return errRoundCreateIncomplete
	}
	<-result.done
	return result.err
}

// enqueueRoundCreate chains round INSERTs in engine start order without
// blocking the tick goroutine. PostgreSQL therefore allocates persisted_order
// in match-start order even if an earlier database call is delayed.
//
// The engine lock serializes calls to this helper in production.
func (e *GameEngine) enqueueRoundCreate(round *db.Round) *roundPersistenceResult {
	result := newRoundPersistenceResult()
	previous := e.roundCreateTail
	e.roundCreateTail = result.done
	create := createRoundRecord

	safeGo(func() {
		// Always release this round and the next queued round. The non-nil
		// default error prevents dependents from running if create panics.
		defer close(result.done)
		if previous != nil {
			<-previous
		}
		ctx, cancel := context.WithTimeout(context.Background(), roundCreateTimeout)
		defer cancel()
		result.err = create(ctx, round)
		if result.err != nil {
			slog.Error("failed to create round record",
				"round_id", round.ID,
				"round", round.RoundNumber,
				"error", result.err,
			)
		}
	})
	return result
}

func afterRoundCreated(result *roundPersistenceResult, operation, roundID string, fn func()) {
	if err := result.wait(); err != nil {
		if result != nil {
			result.skipLogOnce.Do(func() {
				slog.Warn("skipping dependent round persistence after create failure",
					"operation", operation,
					"round_id", roundID,
					"error", err,
				)
			})
		}
		return
	}
	fn()
}

// GameEngine is the central coordinator for the arena game loop.
type GameEngine struct {
	mu sync.RWMutex

	// State
	Bots           map[string]*BotState
	WaitingBots    map[string]*BotState
	Pickups        []Pickup
	Projectiles    []Projectile
	StaffImpacts   []StaffImpact
	BurnFields     []BurnField
	Round          RoundState
	Arena          *ArenaMap
	Grid           *SpatialGrid
	NavGrid        *NavGrid
	Terrain        *TerrainGrid
	NextTerrain    *TerrainGrid // Pre-generated terrain for next round (available during intermission)
	NextObstacles  []Obstacle   // Pre-generated obstacles for next round
	NextNavGrid    *NavGrid     // Pre-generated nav grid for next round
	NextMapShape   MapShape     // Pre-generated map shape for next round
	NextMaskRects  []Obstacle   // Pre-generated boundary rectangles for the next round's shape
	NextZoneTarget *Vec2        // Pre-picked zone drift target for next round (round_end preview parity, issue #192)
	KillFeed       *KillFeed

	// Anti-teaming
	AntiTeam *AntiTeamTracker

	// New gameplay systems
	TeleportPads []TeleportPad
	CapturePads  []CapturePad
	HazardZones  []HazardZone
	SuddenDeath  *SuddenDeathSystem
	Bounty       *BountySystem
	Landmines    []Landmine
	GravityWells []GravityWell

	// Game modes (groundwork)
	ModeRules  ModeRules
	Flags      []*CTFFlag
	TeamScores map[int]int

	// Spectators
	Spectators   []*SpectatorConn
	spectatorsMu sync.RWMutex

	// Tick tracking
	TickCount int
	Running   bool
	Paused    bool

	// Server start time for uptime tracking.
	StartTime time.Time

	// Public operator announcements and scheduled-maintenance state. Kept on a
	// separate lock so REST/status delivery never contends with simulation ticks.
	serviceStatus   ServiceStatus
	serviceStatusMu sync.RWMutex

	// Ban list: API key IDs that are banned from reconnecting.
	bannedKeys   map[string]bool
	bannedKeysMu sync.RWMutex

	// IP ban list.
	bannedIPs   map[string]bool
	bannedIPsMu sync.RWMutex

	// Events (buffered, drained after each tick)
	DeathEvents  []DeathEvent
	KillEvents   []KillEvent
	RecentEvents []ArenaEvent
	// forceKeyframe requests that the next spectator broadcast include the
	// static round data regardless of the keyframe interval (set on join).
	// Guarded by spectatorsMu.
	forceKeyframe bool

	// Last lobby broadcast frame (engine lock). Kept so byte-identical lobby
	// frames share one slice identity and per-bot dedup can skip re-sends.
	lastLobbyPayload []byte
	// Persistence tracking
	lastPersistTick      int
	skipLeaderboardRound int // active round that straddled a leaderboard reset
	// roundDBReady publishes the current round's CreateRound result. Dependent
	// updates, stats, and kill logs run only after a successful insert.
	// roundCreateTail chains inserts in match-start order so the database's
	// persisted_order sequence cannot be reordered by asynchronous completion.
	// Both fields are written only by the tick goroutine in startRound.
	roundDBReady    *roundPersistenceResult
	roundCreateTail <-chan struct{}
	// Bounty snapshots are generated from the tick goroutine but persisted in
	// background goroutines. Serialize writes and discard a queued snapshot when
	// a newer generation already exists, preventing old state from winning due
	// to goroutine or database scheduling order.
	bountyPersistMu         sync.Mutex
	bountyPersistGeneration atomic.Uint64

	// outbox stages the payloads built during the locked tick phase; the tick
	// goroutine marshals and delivers them after releasing e.mu. Touched only
	// by the tick goroutine (see outbox.go).
	outbox tickOutbox
}

// GameEventHook is a callback for emitting game events to the dashboard.
// Set by main to avoid circular imports.
var GameEventHook func(eventName string, data map[string]interface{})

// SpectatorMessage carries both the inspectable JSON payload and Gorilla's
// cached wire representation. One message is shared by every spectator for a
// broadcast, so compression is performed once per connection configuration.
type SpectatorMessage struct {
	Payload  []byte
	Prepared *websocket.PreparedMessage
}

// SpectatorRoundEndBatch keeps the newest final arena snapshot and its
// lifecycle boundary atomic at the engine-to-writer handoff.
type SpectatorRoundEndBatch struct {
	FinalState *SpectatorMessage
	RoundEnd   *SpectatorMessage
}

// SpectatorSendBufferSize is the bounded handoff between the simulation's
// non-blocking broadcasts and each spectator writer.
const SpectatorSendBufferSize = 32

// SpectatorConn wraps a WebSocket connection for a spectator client.
type SpectatorConn struct {
	Conn         *websocket.Conn
	SendChan     chan *SpectatorMessage
	StateChan    chan *SpectatorMessage
	RoundEndChan chan SpectatorRoundEndBatch
	Done         chan struct{}
	IP           string
	ConnectedAt  time.Time

	closeDoneOnce sync.Once
}

// CloseDone closes the Done channel exactly once. Both the connection
// handler's own disconnect cleanup and an admin-triggered KickSpectator can
// race to close Done for the same spectator; without this guard the second
// close panics with "close of closed channel".
func (s *SpectatorConn) CloseDone() {
	s.closeDoneOnce.Do(func() {
		close(s.Done)
	})
}

// NewGameEngine initialises all fields and returns a ready-to-run engine.
func NewGameEngine() *GameEngine {
	e := &GameEngine{
		Bots:        make(map[string]*BotState),
		WaitingBots: make(map[string]*BotState),
		Arena:       NewArenaMap(),
		Grid:        NewSpatialGrid(config.C.SpatialCellSize),
		KillFeed:    NewKillFeed(config.C.KillFeedSize),
		AntiTeam:    NewAntiTeamTracker(),
		SuddenDeath: NewSuddenDeathSystem(),
		Bounty:      NewBountySystem(),
		StartTime:   time.Now(),
		bannedKeys:  make(map[string]bool),
		bannedIPs:   make(map[string]bool),
		Round: RoundState{
			Phase: PhaseLobby,
		},
	}
	// Expose the sudden-death system to package-level damage helpers.
	ActiveSuddenDeath = e.SuddenDeath
	return e
}

// Run starts the main game loop. It ticks at the configured TickRate and
// stops when ctx is cancelled.
func (e *GameEngine) Run(ctx context.Context) {
	interval := time.Duration(float64(time.Second) / float64(config.C.TickRate))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	e.Running = true
	slog.Info("game engine started", "tick_rate", config.C.TickRate)

	// Log retention: once at startup, then hourly, always off the tick
	// goroutine. Sweeps kill_log plus the other log-style tables past
	// ARENA_LOG_RETENTION_HOURS; leaderboard aggregates (bot_stats,
	// round_bot_stats, weapon_kill_totals, bounty_board) are never touched.
	// Hourly cadence keeps the effective window within ~1h of the configured
	// one instead of drifting a full day past it.
	safeGo(func() {
		PruneLogDataOnce()
		pruneTicker := time.NewTicker(time.Hour)
		defer pruneTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-pruneTicker.C:
				PruneLogDataOnce()
			}
		}
	})

	for {
		select {
		case <-ctx.Done():
			e.Running = false
			slog.Info("game engine stopped")
			return
		case <-ticker.C:
			e.tick()
		}
	}
}

// tick runs one full engine step in two phases. The locked phase advances
// the simulation and stages every outbound payload as a self-contained value
// snapshot in e.outbox; the unlocked phase then marshals those snapshots to
// JSON and performs the non-blocking channel handoffs, still on the single
// tick goroutine. The engine write lock is therefore held for simulation and
// view building only — serialization no longer stalls action submissions or
// REST readers.
func (e *GameEngine) tick() {
	e.tickLocked()
	e.flushTickOutbox()
}

// tickLocked is the simulation/staging phase. It holds e.mu for its whole
// duration; nothing inside it may perform JSON marshaling for the per-bot,
// spectator, lobby, or death/kill paths — those are staged in e.outbox.
func (e *GameEngine) tickLocked() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.Paused {
		return
	}

	e.TickCount++
	c := &config.C
	dt := 1.0 / float64(c.TickRate)

	switch e.Round.Phase {
	case PhaseLobby:
		e.tickLobby(c)

	case PhaseIntermission:
		e.tickIntermission()

	case PhaseActive:
		e.tickActive(c, dt)
	}

	// Broadcast to spectators in ALL phases (lobby/intermission included).
	if e.Round.Phase != PhaseActive {
		if e.TickCount%c.SpectatorBroadcastInterval == 0 {
			e.sendLobbyStateUpdate()
		}
	}
}

// --------------------------------------------------------------------------
// Phase handlers
// --------------------------------------------------------------------------

func (e *GameEngine) tickLobby(c *config.Config) {
	e.Round.TimeRemaining = 0

	// Merge waiting bots into active bots.
	for id, bot := range e.WaitingBots {
		e.Bots[id] = bot
	}
	e.WaitingBots = make(map[string]*BotState)

	// Detached active-round sessions are never lobby participants. End-of-round
	// cleanup removes them, and this connected count keeps a defensive stale
	// entry from satisfying the next match's minimum-player gate.
	connected := e.connectedBotCountLocked()

	// Start or continue lobby countdown.
	if connected >= c.MinBotsToStart && e.Round.LobbyCountdownTicks == 0 {
		e.Round.LobbyCountdownTicks = int(c.LobbyCountdown * float64(c.TickRate))
	}

	if e.Round.LobbyCountdownTicks > 0 {
		e.Round.TimeRemaining = float64(e.Round.LobbyCountdownTicks) / float64(config.C.TickRate)
		e.Round.LobbyCountdownTicks--
		if e.Round.LobbyCountdownTicks <= 0 {
			e.startRound()
			return
		}
	}

	// Reset countdown if we drop below minimum.
	if connected < c.MinBotsToStart {
		e.Round.LobbyCountdownTicks = 0
	}

	// Stage lobby updates every 2 ticks. The payload inputs (player list,
	// countdown) are captured under the lock; marshal, dedup, and delivery
	// happen in flushTickOutbox after the lock is released.
	if e.TickCount%2 == 0 {
		var countdown *int
		if e.Round.LobbyCountdownTicks > 0 {
			secs := e.Round.LobbyCountdownTicks / config.C.TickRate
			if secs < 1 {
				secs = 1
			}
			countdown = &secs
		}
		recipients := make([]*BotState, 0, len(e.Bots))
		for _, bot := range e.Bots {
			recipients = append(recipients, bot)
		}
		e.outbox.lobbyUpdate = &outboundLobbyUpdate{
			connected:  connected,
			minBots:    config.C.MinBotsToStart,
			countdown:  countdown,
			players:    buildLobbyPlayers(e.Bots),
			recipients: recipients,
		}
	}
}

func (e *GameEngine) tickIntermission() {
	e.Round.TimeRemaining = float64(e.Round.IntermissionTicks) / float64(config.C.TickRate)
	e.Round.IntermissionTicks--
	if e.Round.IntermissionTicks <= 0 {
		e.Round.Phase = PhaseLobby
		e.Round.LobbyCountdownTicks = 0
		e.Round.TimeRemaining = 0
	}
}

func (e *GameEngine) tickActive(c *config.Config, dt float64) {
	remainingTicks := int(c.RoundDuration*float64(c.TickRate)) - (e.TickCount - e.Round.StartTick)
	if remainingTicks < 0 {
		remainingTicks = 0
	}
	e.Round.TimeRemaining = float64(remainingTicks) / float64(c.TickRate)
	zoneDelayTicks, zoneIntervalTicks, zoneShrinkPercent, roundTotalTicks := effectiveZoneProfile(e.Round.Modifier)

	// Apply fallback AI actions for bots without pending actions.
	e.applyFallbacks()

	// Process USE_ITEM actions.
	e.processUseItems()

	// Movement.
	ProcessMovement(e.Bots, e.Arena.Obstacles, e.Grid, e.NavGrid, dt)

	// Anti-stuck: if a bot hasn't moved for 10+ ticks, nudge it to a
	// random adjacent passable cell so it doesn't stay glued to a wall.
	if ActiveTerrain != nil {
		for _, bot := range e.Bots {
			if !bot.IsAlive {
				continue
			}
			cell := ActiveTerrain.WorldToGrid(bot.Position)
			updateStuckDetection(bot, cell)
			// Only nudge bots that are actually trying to move: shuffling a
			// deliberately-idle bot (territorial guards, braced spears) makes
			// it twitch in place every second for no reason.
			wantsMove := bot.PendingAction != nil &&
				(bot.PendingAction.Type == ActionMove ||
					bot.PendingAction.Type == ActionMoveTo ||
					bot.PendingAction.Type == ActionDodge)
			if bot.StuckTicks >= 10 && wantsMove {
				// Try each direction randomly until we find a passable cell
				for _, d := range directions {
					if !ActiveTerrain.IsMoveBlocked(cell[0], cell[1], d.dx, d.dy) {
						nc := [2]int{cell[0] + d.dx, cell[1] + d.dy}
						bot.Position = ActiveTerrain.GridToWorld(nc)
						bot.LastValidPosition = bot.Position
						bot.StuckTicks = 0
						bot.StillTicks = 0
						e.Grid.Update(bot.BotID, bot.Position)
						break
					}
				}
			}
		}
	}

	// Shoves (before combat so shoved bots can't attack this tick).
	ProcessShoves(e.Bots, e.Arena.Obstacles, e.TickCount)

	// Combat.
	ProcessCombat(e.Bots, e.Arena.Obstacles, &e.Projectiles, &e.StaffImpacts, &e.RecentEvents, e.Grid, e.TickCount, dt)

	// Only a combat action that passed validation and committed successfully
	// counts as fighting. Merely naming a nearby bot in an invalid request must
	// not reset the anti-team proximity clock.
	e.recordSuccessfulCombatCommitments()

	// Projectiles.
	UpdateProjectiles(&e.Projectiles, e.Bots, e.Arena.Obstacles, &e.RecentEvents, e.TickCount, dt)

	// Staff area impacts.
	e.appendArenaEvents(ProcessStaffImpacts(&e.StaffImpacts, &e.BurnFields, e.Bots, e.AntiTeam, e.TickCount)...)
	ProcessBurnFields(&e.BurnFields, e.Bots, e.AntiTeam, e.TickCount)

	// Zone shrink.
	e.Arena.UpdateZoneProfile(e.TickCount, e.Round.StartTick, zoneDelayTicks, zoneIntervalTicks, zoneShrinkPercent, roundTotalTicks)

	// Zone damage.
	e.applyZoneDamage()

	// Teleport pads.
	e.appendArenaEvents(ProcessTeleports(e.Bots, e.TeleportPads, e.Grid, e.TickCount, e.Round.Modifier)...)

	// Capture pad objective.
	e.appendArenaEvents(UpdateCapturePads(e.CapturePads, e.Bots, e.TickCount)...)

	// CTF flags (team modes only).
	if len(e.Flags) > 0 {
		e.appendArenaEvents(UpdateCTFFlags(e.Flags, e.Bots, e.TeamScores, e.TickCount)...)
	}

	// Environmental hazards.
	UpdateHazards(e.HazardZones, e.Bots, e.TickCount, e.Round.Modifier)

	// Landmines.
	e.appendArenaEvents(UpdateMines(&e.Landmines, e.Bots, e.TickCount)...)

	// Gravity wells.
	UpdateGravityWells(&e.GravityWells, e.Bots, e.Grid)

	// Sudden death: activate when zone reaches minimum, remove floor tiles.
	// (Runs before death checks so void-tile damage registers this tick;
	// the bounty target is recalculated after deaths instead.)
	if e.SuddenDeath.CheckActivation(e.Arena, e.TickCount-e.Round.StartTick, roundTotalTicks) {
		e.appendArenaEvents(ArenaEvent{
			ID:       fmt.Sprintf("sudden-death:%d", e.TickCount),
			Type:     "sudden_death",
			Tick:     e.TickCount,
			Position: e.Arena.ZoneCenter,
			Color:    "#ff3355",
			Radius:   e.Arena.ZoneRadius,
		})
	}
	e.SuddenDeath.Update(e.Bots, e.Arena)
	e.SuddenDeath.UpdateStall(e.Bots)

	// Anti-teaming: penalise bots that stay near each other without fighting.
	penalised := e.AntiTeam.Update(e.Bots, e.Grid)
	for _, botID := range penalised {
		if bot, ok := e.Bots[botID]; ok && bot.IsAlive {
			bot.HP -= config.C.AntiTeamDamagePerTick * SuddenDeathDamageMultiplier()
		}
	}

	// Bot separation.
	SeparateBots(e.Bots, e.Arena.Obstacles, e.Grid)

	// HARD WALL ENFORCEMENT: Every tick, validate every bot's position.
	// If a bot is in a blocked cell, revert to LastValidPosition.
	// Then update LastValidPosition for next tick.
	if ActiveTerrain != nil {
		for _, bot := range e.Bots {
			if !bot.IsAlive {
				continue
			}
			cell := ActiveTerrain.WorldToGrid(bot.Position)
			if ActiveTerrain.IsBlocked(cell[0], cell[1]) {
				// Revert to last known valid position.
				prevCell := ActiveTerrain.WorldToGrid(bot.LastValidPosition)
				if !ActiveTerrain.IsBlocked(prevCell[0], prevCell[1]) {
					bot.Position = bot.LastValidPosition
				} else {
					// Last valid position is also blocked (deep-wall spawn or a
					// map swap under the bot). Take the nearest open cell —
					// scanning full rings, not 8 fixed rays, so thick caves
					// walls can't strand the bot inside terrain forever.
					if nc, ok := ActiveTerrain.NearestOpenCell(cell, 0); ok {
						bot.Position = ActiveTerrain.GridToWorld(nc)
					} else {
						// Entire grid blocked (cannot happen with valid maps):
						// keep LastValidPosition unset so next tick retries.
						e.Grid.Update(bot.BotID, bot.Position)
						continue
					}
				}
				e.Grid.Update(bot.BotID, bot.Position)
			}
			// Record this tick's valid position for next tick's enforcement.
			bot.LastValidPosition = bot.Position
		}
	} else {
		for _, bot := range e.Bots {
			if bot.IsAlive {
				EnforceObstacleBounds(bot, e.Arena.Obstacles, config.C.BotRadius)
			}
		}
	}

	// Check deaths.
	deaths := CheckDeaths(e.Bots, e.Grid, e.TickCount)

	// Handle kill credits before buffering death events so the enriched source
	// and damage metadata is included in the messages sent to bots.
	e.handleKillCredits(deaths)
	e.DeathEvents = append(e.DeathEvents, deaths...)

	// No respawns — dead bots stay dead until next round.

	// Bounty system: recalculate the live target now that deaths are resolved.
	e.Bounty.Update(e.Bots)

	// Pickups (include gravity_well type).
	MaybeSpawnPickupAtInterval(&e.Pickups, e.Arena, e.TickCount, effectivePickupSpawnInterval(e.Round.Modifier))
	CheckAutoCollect(e.Bots, &e.Pickups)

	// Tick effects and timers for all bots.
	for _, bot := range e.Bots {
		TickEffects(bot)
		TickTimers(bot, dt)
	}

	// Update longest life tracking.
	for _, bot := range e.Bots {
		if bot.IsAlive {
			lifeLen := e.TickCount - bot.RoundLifeStartTick
			if lifeLen > bot.RoundLongestLife {
				bot.RoundLongestLife = lifeLen
			}
		}
	}

	// Check AFK bots.
	e.checkAFK()

	// Send per-bot tick updates.
	e.sendBotTickUpdates()

	// Spectator broadcasts at configured interval.
	if e.TickCount%c.SpectatorBroadcastInterval == 0 {
		e.sendSpectatorUpdate()
	}

	// Send buffered event messages.
	e.sendEventMessages()

	// Periodic persistence.
	persistInterval := int(c.PersistIntervalSecs * float64(c.TickRate))
	if persistInterval > 0 && e.TickCount-e.lastPersistTick >= persistInterval {
		e.lastPersistTick = e.TickCount
		snaps := e.snapshotBotStats()
		safeGo(func() { PersistBotStatsFromSnapshot(context.Background(), snaps, "", false) })
	}

	// Check round end.
	if ShouldEndRound(e.Bots, &e.Round, e.TickCount, e.TeamScores, e.SuddenDeath) {
		e.endRound()
	}

	// Clear per-tick feedback.
	for _, bot := range e.Bots {
		bot.ClearTickFeedback()
	}
}

// --------------------------------------------------------------------------
// Round lifecycle
// --------------------------------------------------------------------------

func (e *GameEngine) startRound() {
	c := &config.C
	e.Round.RoundNumber++

	// Use pre-generated terrain from intermission if available, otherwise generate fresh.
	var obstacles []Obstacle
	var maskRects []Obstacle
	if e.NextTerrain != nil {
		obstacles = e.NextObstacles
		e.NavGrid = e.NextNavGrid
		e.Terrain = e.NextTerrain
		ActiveTerrain = e.Terrain
		ActiveMapShape = e.NextMapShape
		maskRects = e.NextMaskRects
		// Clear pre-gen fields.
		e.NextTerrain = nil
		e.NextObstacles = nil
		e.NextNavGrid = nil
		e.NextMaskRects = nil
		e.NextMapShape = ShapeSquare
	} else {
		// First round or no pre-gen available — generate fresh.
		var shape MapShape
		obstacles, e.NavGrid, e.Terrain, shape, maskRects = generateRoundTerrain(len(e.Bots))
		ActiveTerrain = e.Terrain
		ActiveMapShape = shape
	}
	// Use the zone drift target pre-picked at endRound (announced to
	// spectators in round_end.next_map.safe_zone) so the round opens with
	// exactly the placement the intermission show previewed.
	e.Arena.ResetWithZoneTarget(obstacles, e.NextZoneTarget)
	e.NextZoneTarget = nil
	e.Arena.MaskRects = maskRects

	// Clear transient state.
	e.Pickups = nil
	e.Projectiles = nil
	e.StaffImpacts = nil
	e.BurnFields = nil
	e.DeathEvents = nil
	e.KillEvents = nil
	e.Landmines = nil
	e.GravityWells = nil
	// Spectator events (incl. taunts) buffered after the previous round's
	// last broadcast would otherwise ghost into the new round's first frame.
	e.RecentEvents = nil
	e.Grid.Clear()
	e.KillFeed.Clear()
	e.AntiTeam.Clear()
	e.SuddenDeath.Clear()
	e.Bounty.ResetRoundState(e.Bots)

	// Spawn new gameplay systems.
	e.TeleportPads = SpawnTeleportPads(e.Arena, config.C.TeleportPadPairs)
	e.CapturePads = SpawnCapturePads(e.Arena, config.C.CapturePadCount)
	e.HazardZones = SpawnHazardZones(e.Arena, config.C.HazardZoneCount)

	// Set round state.
	e.Round.Phase = PhaseActive
	e.Round.StartTick = e.TickCount
	e.Round.Modifier = rollRoundModifier()
	e.Round.TimeRemaining = c.RoundDuration
	e.Round.RoundID = uuid.New().String()
	e.Bounty.RewardMultiplier = effectiveBountyRewardMultiplier(e.Round.Modifier)

	// Resolve the game mode for this round and set up teams/objectives.
	e.ModeRules = CurrentModeRules()
	ActiveModeRules = e.ModeRules
	e.Round.Mode = e.ModeRules.Mode
	e.TeamScores = make(map[int]int)
	e.Flags = nil
	if e.ModeRules.HasTeams() {
		AssignTeams(e.Bots, e.ModeRules.TeamCount)
		if e.ModeRules.UsesFlags {
			e.Flags = SpawnCTFFlags(e.Arena, e.ModeRules.TeamCount)
		}
	} else {
		AssignTeams(e.Bots, 0)
	}

	if db.Pool != nil {
		round := &db.Round{
			ID:               e.Round.RoundID,
			RoundNumber:      e.Round.RoundNumber,
			StartedAt:        time.Now(),
			BotsParticipated: len(e.Bots),
			Status:           "active",
		}
		// Persist off the tick goroutine. enqueueRoundCreate serializes only
		// the background INSERTs, preserving match-start order without ever
		// making the simulation wait on PostgreSQL.
		e.roundDBReady = e.enqueueRoundCreate(round)
	} else {
		// A dynamically unavailable database must never inherit the previous
		// round's successful result.
		e.roundDBReady = nil
	}

	// Spawn bots evenly around the zone perimeter. In team modes each team
	// gets its own arc of the spawn ring so allies start together.
	botList := make([]*BotState, 0, len(e.Bots))
	for _, bot := range e.Bots {
		botList = append(botList, bot)
	}
	var spawnPoints []Vec2
	if e.ModeRules.HasTeams() {
		teamSizes := make(map[int]int)
		for _, bot := range botList {
			teamSizes[bot.Team]++
		}
		memberIdx := make(map[int]int)
		spawnPoints = make([]Vec2, len(botList))
		for i, bot := range botList {
			spawnPoints[i] = e.Arena.TeamSpawnPoint(bot.Team, memberIdx[bot.Team], e.ModeRules.TeamCount, teamSizes[bot.Team])
			memberIdx[bot.Team]++
		}
	} else {
		spawnPoints = e.Arena.GetSpawnPoints(len(botList))
	}
	for i, bot := range botList {
		bot.ResetRoundStats()
		SpawnBotAt(bot, spawnPoints[i], e.Grid, e.TickCount)
		bot.KillStreak = 0
		bot.LastActionTick = 0 // Reset AFK timer so bots aren't kicked at round start
		bot.ReconnectActionGraceUntilTick = 0
		bot.StillTicks = 0
		bot.BowChargeTicks = 0
		setFacingToward(bot, e.Arena.ZoneCenter)
	}

	// Send round_start to every bot.
	// Note: map_init is no longer sent over WebSocket — bots should use
	// GET /api/v1/arena/map instead (available during intermission with
	// next round's pre-generated terrain).
	for _, bot := range e.Bots {
		SendRoundStart(bot, e.Round, e.Bots, e.Arena)
	}

	// Force the next spectator broadcast to be a keyframe so clients swap to
	// this round's walls immediately. Without it, renderers keep the previous
	// round's obstacles for up to a full keyframe interval while bots already
	// stand at new-map positions — on shape changes they appear embedded in
	// walls ("stuck in the ground") until the keyframe lands.
	e.spectatorsMu.Lock()
	e.forceKeyframe = true
	e.spectatorsMu.Unlock()

	slog.Info("round started",
		"round", e.Round.RoundNumber,
		"bots", len(e.Bots),
		"modifier", e.Round.Modifier,
		"obstacles", len(obstacles),
		"map_shape", ActiveMapShape,
		"mode", e.Round.Mode,
	)

	if GameEventHook != nil {
		GameEventHook("round_start", map[string]interface{}{
			"round_number": e.Round.RoundNumber,
			"modifier":     string(e.Round.Modifier),
			"bots":         len(e.Bots),
			"obstacles":    len(obstacles),
			"map_shape":    string(ActiveMapShape),
			"mode":         string(e.Round.Mode),
		})
	}
}

func (e *GameEngine) endRound() {
	winnerID, winnerName := DetermineWinner(e.Bots, e.TeamScores)
	awards := CalculateAwards(e.Bots)

	// Capture the winner's avatar color for the spectator round_end broadcast
	// now, before the reconnect-pending prune below can remove the session.
	winnerColor := ""
	if winner := e.Bots[winnerID]; winner != nil {
		winnerColor = winner.AvatarColor
	}

	info := RoundEndInfo{
		RoundNumber: e.Round.RoundNumber,
		WinnerID:    winnerID,
		WinnerName:  winnerName,
		Awards:      awards,
	}

	nextRoundIn := config.C.IntermissionTime

	// Discard any bot tick snapshots staged earlier in this tick: they show
	// alive state from the round that just ended, and flushing them after the
	// round_end control messages below would deliver exactly the stale
	// round_end-then-tick sequence that SendRoundEnd's queue discard prevents.
	e.outbox.botTicks = e.outbox.botTicks[:0]

	for _, bot := range e.Bots {
		SendRoundEnd(bot, info, nextRoundIn)
	}

	e.Bounty.OnRoundEnd(e.Bots, winnerID)
	e.persistBountyBoardAsync()
	AutoBalanceWeapons(context.Background(), e.Bots, winnerID)

	if db.Pool != nil {
		endedAt := time.Now()
		var winnerIDPtr *string
		if winnerID != "" {
			winnerIDPtr = &winnerID
		}
		round := &db.Round{
			ID:       e.Round.RoundID,
			EndedAt:  &endedAt,
			MVPBotID: winnerIDPtr,
			Status:   "completed",
		}
		roundNumber := e.Round.RoundNumber
		// Persist off the tick goroutine, same as the other persistence paths.
		// Wait for this round's CreateRound insert first: a plain UPDATE that
		// races ahead of the INSERT matches zero rows and the round would be
		// recorded as permanently 'active'.
		ready := e.roundDBReady
		safeGo(func() {
			afterRoundCreated(ready, "round completion", round.ID, func() {
				if err := db.UpdateRound(context.Background(), round); err != nil {
					slog.Error("failed to update round record", "round_id", round.ID, "round", roundNumber, "error", err)
				}
			})
		})
	}

	// A leaderboard reset during an active round must not rewrite pre-reset
	// round history, but it also must not alter the live match's score. Keep
	// post-reset cumulative deltas while omitting that straddling round from
	// rounds-played/wins and the time-window table.
	roundNum := e.Round.RoundNumber
	roundID := e.Round.RoundID
	recordCompletedRound := e.skipLeaderboardRound != roundNum
	finalSnaps := e.snapshotBotStats()
	safeGo(func() {
		PersistBotStatsFromSnapshot(context.Background(), finalSnaps, winnerID, recordCompletedRound)
	})

	// Record per-round per-bot stats for time-based leaderboards.
	if recordCompletedRound {
		roundBots := e.copyBotsForPersist()
		roundStatsEpoch := botStatsPersistenceEpoch.Load()
		roundReady := e.roundDBReady
		safeGo(func() {
			afterRoundCreated(roundReady, "round bot stats", roundID, func() {
				PersistRoundBotStats(context.Background(), roundStatsEpoch, roundID, roundNum, roundBots, winnerID)
			})
		})
	} else {
		e.skipLeaderboardRound = 0
	}

	// A reconnect grace period is meaningful only while its round is active.
	// Keep detached participants through winner/award/final persistence capture,
	// then remove their transport placeholders before entering intermission so
	// they cannot consume capacity or start a phantom next round.
	for botID, bot := range e.Bots {
		if !bot.ReconnectPending {
			continue
		}
		delete(e.Bots, botID)
		e.Grid.Remove(botID)
	}
	e.Round.Phase = PhaseIntermission
	e.Round.Modifier = RoundModifierNone
	e.Round.IntermissionTicks = int(config.C.IntermissionTime * float64(config.C.TickRate))
	e.Round.TimeRemaining = config.C.IntermissionTime
	e.Bounty.RewardMultiplier = 1

	// Pre-generate next round's terrain so bots can GET /api/v1/arena/map during intermission.
	// Size the next round's map for everyone expected to play it: current
	// bots plus the waiting lobby that joins at round start.
	e.NextObstacles, e.NextNavGrid, e.NextTerrain, e.NextMapShape, e.NextMaskRects = generateRoundTerrain(len(e.Bots) + len(e.WaitingBots))
	ActiveTerrain = e.NextTerrain
	ActiveMapShape = e.NextMapShape

	// Pre-pick the next round's zone drift target on the pre-generated
	// terrain (ActiveTerrain above), so the round_end preview below can
	// announce the exact safe-zone placement the round will open with.
	// startRound passes the same value into ResetWithZoneTarget (issue #192
	// parity — the show's gold-ring glide must land where the first keyframe
	// puts the ring).
	nextZoneTarget := pickZoneTargetCenter(config.C.ArenaWidth, config.C.ArenaHeight, e.Arena.MinRadius)
	e.NextZoneTarget = &nextZoneTarget
	nextZoneCenter := NewVec2(config.C.ZoneCenterX, config.C.ZoneCenterY)

	// Stage the typed round_end spectator broadcast (issue #189): winner
	// announcement plus a preview of the next round's pre-generated terrain,
	// so clients can run the intermission show and pre-build the next map.
	// generateRoundTerrain above already applied dynamic sizing for the NEXT
	// round, so config.C.Arena{Width,Height} here are exactly the dimensions
	// the next round's first keyframe will report (ArenaMap.Reset copies
	// them) — the client must see identical values or it would tear the
	// scene down twice. Marshal + delivery happen in flushTickOutbox after
	// the engine lock is released, and only when spectators are connected.
	roundEndMsg := &RoundEndSpectatorMessage{
		Type:             "round_end",
		RoundNumber:      e.Round.RoundNumber,
		IntermissionSecs: config.C.IntermissionTime,
		NextMap: &NextMapView{
			Shape:     string(e.NextMapShape),
			ArenaSize: [2]float64{config.C.ArenaWidth, config.C.ArenaHeight},
			Obstacles: ExpandObstaclesForClient(e.NextObstacles, e.NextMaskRects, e.NextTerrain.CellSize),
			MaskRects: e.NextMaskRects,
			// Byte parity with the first keyframe's safe_zone: startRound's
			// ResetWithZoneTarget copies these exact values (center/radius are
			// deterministic from the post-resize config, the target is the
			// pre-pick above), and BuildSpectatorState applies the same round1
			// rounding to the radii.
			SafeZone: &SafeZoneSpectatorView{
				Center:       nextZoneCenter,
				Radius:       round1(initialZoneRadius(&config.C, nextZoneCenter, config.C.ArenaWidth, config.C.ArenaHeight)),
				TargetCenter: nextZoneTarget,
				TargetRadius: round1(e.Arena.MinRadius),
			},
		},
	}
	if winnerID != "" {
		roundEndMsg.Winner = &RoundWinnerView{ID: winnerID, Name: winnerName, Color: winnerColor}
	}
	e.outbox.spectatorEvent = roundEndMsg

	slog.Info("round ended",
		"round", e.Round.RoundNumber,
		"winner", winnerName,
		"next_map_pregenerated", true,
	)

	if GameEventHook != nil {
		GameEventHook("round_end", map[string]interface{}{
			"round_number": e.Round.RoundNumber,
			"winner_id":    winnerID,
			"winner_name":  winnerName,
		})
	}
}

// --------------------------------------------------------------------------
// Bot management
// --------------------------------------------------------------------------

// AddBot adds a bot to the engine. If the game is in lobby phase the bot is
// added directly; otherwise it goes into the waiting list for the next round.
// Returns false if the server is at capacity (MaxBots).
func (e *GameEngine) AddBot(bot *BotState) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	if existing := e.Bots[bot.BotID]; existing != nil {
		if e.Round.Phase == PhaseActive {
			// A reconnect during an active round replaces only the transport
			// session. Preserve the authoritative match state so reconnecting cannot
			// reset HP, deaths, cooldowns, position, or the round-locked loadout.
			newConn := bot.Conn
			newSendChan := bot.SendChan
			newTickChan := bot.TickChan
			newConnectedAt := bot.ConnectedAt
			newTransportCloseCause := bot.TransportCloseCause
			*bot = *existing
			bot.Conn = newConn
			bot.SendChan = newSendChan
			bot.TickChan = newTickChan
			bot.ConnectedAt = newConnectedAt
			bot.TransportCloseCause = newTransportCloseCause
			bot.ReconnectPending = false
			bot.DisconnectedAtTick = 0
			graceTicks := config.C.AFKTimeoutTicks
			if graceTicks < 1 {
				graceTicks = 1
			}
			bot.ReconnectActionGraceUntilTick = e.TickCount + graceTicks
			e.Grid.Update(bot.BotID, bot.Position)
		} else {
			// Between rounds the newly validated loadout is authoritative. Carry
			// only live cross-round progression that may not have reached the DB yet;
			// combat state and resources must not leak into the next match.
			if existing.Elo > 0 {
				bot.Elo = existing.Elo
			}
			bot.RoundWinStreak = existing.RoundWinStreak
			e.Grid.Remove(bot.BotID)
		}
		e.Bots[bot.BotID] = bot
		existing.SignalTransportClose(BotTransportCloseCause{
			Source: "session_replaced", CloseCode: websocket.CloseNormalClosure, CloseReason: "session replaced by reconnect",
		})
		if existing.Conn != nil {
			conn := existing.Conn
			safeGo(func() {
				_ = conn.WriteControl(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session replaced by reconnect"),
					time.Now().Add(time.Second),
				)
				_ = conn.Close()
			})
		}
		return true
	}
	if existing := e.WaitingBots[bot.BotID]; existing != nil {
		e.WaitingBots[bot.BotID] = bot
		existing.SignalTransportClose(BotTransportCloseCause{
			Source: "session_replaced", CloseCode: websocket.CloseNormalClosure, CloseReason: "session replaced by reconnect",
		})
		if existing.Conn != nil {
			conn := existing.Conn
			safeGo(func() {
				_ = conn.WriteControl(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session replaced by reconnect"),
					time.Now().Add(time.Second),
				)
				_ = conn.Close()
			})
		}
		return true
	}

	// Existing sessions do not consume an additional slot, so capacity is
	// checked only after the reconnect cases above. Detached transports are not
	// connected admissions and are pruned at the current round boundary.
	if e.connectedBotCountLocked() >= config.C.MaxBots {
		return false
	}

	if e.Round.Phase == PhaseLobby {
		e.Bots[bot.BotID] = bot
	} else {
		e.WaitingBots[bot.BotID] = bot
	}
	return true
}

// BuildLoadoutConfirmationForSession snapshots one admitted session while the
// engine lock protects its position and round-locked loadout. Serializing the
// returned map is safe after the lock is released.
func (e *GameEngine) BuildLoadoutConfirmationForSession(botID string, expected *BotState) (map[string]interface{}, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	bot := e.Bots[botID]
	if bot == nil {
		bot = e.WaitingBots[botID]
	}
	if bot == nil || bot != expected {
		return nil, false
	}
	derived := ComputeDerivedStats(bot.Stats, bot.Weapon)
	return BuildLoadoutConfirmed(bot, derived), true
}

// DisconnectBotSessionForKey closes the exact active/waiting transport that
// currently owns botID and apiKeyID. Cleanup remains pointer-checked in
// RemoveBot, so closing an older connection cannot remove a newer session.
func (e *GameEngine) DisconnectBotSessionForKey(botID, apiKeyID string) bool {
	e.mu.RLock()
	bot := e.Bots[botID]
	if bot == nil {
		bot = e.WaitingBots[botID]
	}
	if bot == nil || bot.APIKeyID != apiKeyID || bot.Conn == nil {
		e.mu.RUnlock()
		return false
	}
	conn := bot.Conn
	bot.SignalTransportClose(BotTransportCloseCause{
		Source: "server_policy", CloseCode: websocket.ClosePolicyViolation, CloseReason: "temporary protocol lock",
	})
	e.mu.RUnlock()
	// Protocol locks are punitive, not ordinary transport failures. Remove the
	// authoritative session first so handler cleanup cannot place a locked bot
	// into transient reconnect grace.
	e.RemoveBot(botID, bot)
	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "temporary protocol lock"),
		time.Now().Add(time.Second),
	)
	_ = conn.Close()
	return true
}

// DetachBotSession preserves an active-round bot across a short, ordinary
// transport interruption. The bot remains targetable but cannot act while its
// connection is absent. A matching reconnect keeps the authoritative combat
// state; checkAFK expires it after the configured grace window.
func (e *GameEngine) DetachBotSession(botID string, expected *BotState) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.Round.Phase != PhaseActive || expected == nil {
		return false
	}
	current := e.Bots[botID]
	if current == nil || current != expected {
		return false
	}
	current.Conn = nil
	current.SendChan = nil
	current.TickChan = nil
	current.PendingAction = nil
	current.ReconnectPending = true
	current.DisconnectedAtTick = e.TickCount
	return true
}

// HasBotSessionForKey reports whether this key already owns an active or
// waiting session. This distinguishes a bounded reconnect from a new admission
// without exposing mutable session state outside the engine lock.
func (e *GameEngine) HasBotSessionForKey(botID, apiKeyID string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	bot := e.Bots[botID]
	if bot == nil {
		bot = e.WaitingBots[botID]
	}
	return bot != nil && bot.APIKeyID == apiKeyID
}

// RemoveBot removes a specific bot instance from both active and waiting maps
// and persists its stats. If a reconnect has already replaced this bot in the
// engine maps, the newer instance is left alone.
func (e *GameEngine) RemoveBot(botID string, expected *BotState) {
	e.mu.Lock()
	var bot *BotState
	var statsSnapshot *BotStatsSnapshot
	if current := e.Bots[botID]; current != nil && current == expected {
		bot = current
		delete(e.Bots, botID)
		e.Grid.Remove(botID)
	}
	if current := e.WaitingBots[botID]; current != nil && current == expected {
		bot = current
		delete(e.WaitingBots, botID)
	}
	if bot != nil && len(e.Landmines) > 0 {
		// Drop this bot's armed-but-undetonated mines so they don't keep
		// damaging other bots (and crediting kills to a departed player)
		// for the rest of the round.
		live := e.Landmines[:0]
		for _, m := range e.Landmines {
			if m.OwnerID != botID {
				live = append(live, m)
			}
		}
		e.Landmines = live
	}
	if bot != nil {
		snapshot := takeBotStatsSnapshot(bot)
		statsSnapshot = &snapshot
	}
	e.mu.Unlock()

	if statsSnapshot != nil {
		safeGo(func() { PersistSingleBot(context.Background(), *statsSnapshot) })
	}
}

var (
	ErrActionBotNotFound      = errors.New("bot is not active")
	ErrActionNil              = errors.New("action is required")
	ErrActionTickFuture       = errors.New("action tick is ahead of the server")
	ErrActionTickStale        = errors.New("action tick is stale")
	ErrActionTickDuplicate    = errors.New("action tick was already submitted")
	ErrActionServerTickUsed   = errors.New("an action was already accepted this server tick")
	ErrActionTargetNotVisible = errors.New("target is outside current visibility")
	ErrActionSessionReplaced  = errors.New("bot session was replaced")
	ErrActionRoundNotActive   = errors.New("round is not active")
	ErrActionBotNotAlive      = errors.New("bot is not alive")
)

// SubmitBotAction atomically validates the client-observed server tick and
// installs the action. A bot gets one immutable submission per observed tick;
// old replays and future-tick guesses are rejected before touching state.
func (e *GameEngine) SubmitBotAction(botID string, clientTick int, action *Action) error {
	return e.submitBotAction(botID, nil, clientTick, action)
}

// SubmitBotActionForSession additionally binds a network action to the
// BotState that owns the submitting WebSocket. A replaced socket may still
// have one read in flight after reconnect, but it can never mutate the new
// authoritative session.
func (e *GameEngine) SubmitBotActionForSession(botID string, expected *BotState, clientTick int, action *Action) error {
	return e.submitBotAction(botID, expected, clientTick, action)
}

func (e *GameEngine) submitBotAction(botID string, expected *BotState, clientTick int, action *Action) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if action == nil {
		return ErrActionNil
	}
	bot, ok := e.Bots[botID]
	if !ok {
		return ErrActionBotNotFound
	}
	if expected != nil && bot != expected {
		return ErrActionSessionReplaced
	}
	if e.Round.Phase != PhaseActive {
		return ErrActionRoundNotActive
	}
	if !bot.IsAlive {
		return ErrActionBotNotAlive
	}
	if clientTick <= 0 {
		return ErrActionTickStale
	}
	if clientTick > e.TickCount {
		return ErrActionTickFuture
	}
	replayWindow := config.C.TickRate // one second of normal network latency
	if replayWindow < 1 {
		replayWindow = 1
	}
	if e.TickCount-clientTick > replayWindow {
		return ErrActionTickStale
	}
	if bot.HasClientActionTick {
		if clientTick == bot.LastClientActionTick {
			return ErrActionTickDuplicate
		}
		if clientTick < bot.LastClientActionTick {
			return ErrActionTickStale
		}
	}
	if bot.HasAcceptedServerTick && bot.LastAcceptedServerTick == e.TickCount {
		return ErrActionServerTickUsed
	}
	if actionRequiresVisibleTarget(action) && !e.targetVisibleToBot(bot, action.TargetID) {
		return ErrActionTargetNotVisible
	}

	bot.LastClientActionTick = clientTick
	bot.HasClientActionTick = true
	bot.LastAcceptedServerTick = e.TickCount
	bot.HasAcceptedServerTick = true
	e.setBotActionLocked(bot, action)
	return nil
}

func actionRequiresVisibleTarget(action *Action) bool {
	if action == nil || action.TargetID == "" {
		return false
	}
	return action.Type == ActionAttack || action.Type == ActionShove || action.Type == ActionGrapple
}

func (e *GameEngine) targetVisibleToBot(bot *BotState, targetID string) bool {
	target := e.Bots[targetID]
	if bot == nil || target == nil || !target.IsAlive {
		return false
	}
	if e.Bounty != nil && e.Bounty.IsBountyTarget(targetID) {
		return true
	}
	viewRadius := float64(config.C.FogRadius) * config.C.PathfindingCellSize
	return bot.Position.DistanceTo(target.Position) <= viewRadius
}

// SetBotAction is retained for trusted in-process callers that do not speak
// the WebSocket protocol. Network actions must use SubmitBotAction.
func (e *GameEngine) SetBotAction(botID string, action *Action) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if bot, ok := e.Bots[botID]; ok && action != nil {
		e.setBotActionLocked(bot, action)
	}
}

func (e *GameEngine) setBotActionLocked(bot *BotState, action *Action) {
	bot.PendingAction = action
	bot.LastActionTick = e.TickCount
	bot.ReconnectActionGraceUntilTick = 0
	if bot.ActionHistoryMax == 0 {
		bot.ActionHistoryMax = 100
	}
	bot.ActionHistory = append(bot.ActionHistory, action.Type)
	if len(bot.ActionHistory) > bot.ActionHistoryMax {
		bot.ActionHistory = bot.ActionHistory[len(bot.ActionHistory)-bot.ActionHistoryMax:]
	}
}

func (e *GameEngine) recordSuccessfulCombatCommitments() {
	for _, bot := range e.Bots {
		action := bot.PendingAction
		result := bot.LastActionResult
		if action == nil || action.Type != ActionAttack || action.TargetID == "" ||
			bot.Weapon == "staff" || result == nil || result.Action != "attack" || !result.Success {
			continue
		}
		if result.Target != "" && result.Target != action.TargetID {
			continue
		}
		e.AntiTeam.RecordAttack(bot.BotID, action.TargetID)
	}
}

// --------------------------------------------------------------------------
// Spectator management
// --------------------------------------------------------------------------

// AddSpectator registers a spectator connection.
func (e *GameEngine) AddSpectator(conn *SpectatorConn) {
	e.spectatorsMu.Lock()
	defer e.spectatorsMu.Unlock()
	e.addSpectatorLocked(conn)
}

// TryAddSpectator atomically checks the configured capacity and registers the
// connection. Keeping both operations under spectatorsMu prevents concurrent
// upgrades from all observing the same pre-admission count and oversubscribing
// the arena.
func (e *GameEngine) TryAddSpectator(conn *SpectatorConn, maxSpectators int) bool {
	if conn == nil || maxSpectators <= 0 {
		return false
	}
	e.spectatorsMu.Lock()
	defer e.spectatorsMu.Unlock()
	if len(e.Spectators) >= maxSpectators {
		return false
	}
	e.addSpectatorLocked(conn)
	return true
}

func (e *GameEngine) addSpectatorLocked(conn *SpectatorConn) {
	e.Spectators = append(e.Spectators, conn)
	// Make sure the next broadcast is a keyframe so the new spectator gets
	// the static round data (obstacles, map shape) immediately.
	e.forceKeyframe = true
}

// GetTickCount returns the engine's current tick under a read lock.
func (e *GameEngine) GetTickCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.TickCount
}

// GetRoundPhase returns the current round phase under the engine read lock.
func (e *GameEngine) GetRoundPhase() RoundPhase {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.Round.Phase
}

// SpectatorCount returns the current number of connected spectators.
func (e *GameEngine) SpectatorCount() int {
	e.spectatorsMu.RLock()
	defer e.spectatorsMu.RUnlock()
	return len(e.Spectators)
}

// RemoveSpectator unregisters a spectator connection.
func (e *GameEngine) RemoveSpectator(conn *SpectatorConn) {
	e.spectatorsMu.Lock()
	defer e.spectatorsMu.Unlock()

	for i, s := range e.Spectators {
		if s == conn {
			e.Spectators = append(e.Spectators[:i], e.Spectators[i+1:]...)
			return
		}
	}
}

// ListSpectators returns info about all connected spectators.
func (e *GameEngine) ListSpectators() []map[string]interface{} {
	e.spectatorsMu.RLock()
	defer e.spectatorsMu.RUnlock()
	result := make([]map[string]interface{}, 0, len(e.Spectators))
	for i, s := range e.Spectators {
		addr := ""
		if s.Conn != nil {
			addr = s.Conn.RemoteAddr().String()
		}
		result = append(result, map[string]interface{}{
			"index":        i,
			"ip":           s.IP,
			"remote_addr":  addr,
			"connected_at": s.ConnectedAt,
		})
	}
	return result
}

// KickSpectator disconnects the spectator at the given index.
func (e *GameEngine) KickSpectator(index int) bool {
	e.spectatorsMu.Lock()
	defer e.spectatorsMu.Unlock()
	if index < 0 || index >= len(e.Spectators) {
		return false
	}
	s := e.Spectators[index]
	if s.Conn != nil {
		s.Conn.Close()
	}
	s.CloseDone()
	e.Spectators = append(e.Spectators[:index], e.Spectators[index+1:]...)
	return true
}

// GetState returns a read-locked snapshot of the game state for spectators.
func (e *GameEngine) GetState() SpectatorState {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return BuildSpectatorState(e.Bots, e.Arena, e.Pickups, e.KillFeed, e.TickCount, e.Round.StartTick, e.WaitingBots, e.Round.Modifier)
}

// ArenaSnapshot holds read-only arena state for the REST API.
type ArenaSnapshot struct {
	Phase              RoundPhase
	Modifier           RoundModifier
	Tick               int
	BotsConnected      int
	BotsAlive          int
	RoundNumber        int
	RoundTimeRemaining float64
	SafeZoneRadius     float64
	TopBotName         string
}

// GetArenaSnapshot returns a point-in-time snapshot of arena state under the
// read lock. It is safe to call from HTTP handlers.
func (e *GameEngine) GetArenaSnapshot() ArenaSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()

	snap := ArenaSnapshot{
		Phase:              e.Round.Phase,
		Modifier:           e.Round.Modifier,
		Tick:               e.TickCount,
		BotsConnected:      len(e.Bots) + len(e.WaitingBots),
		RoundNumber:        e.Round.RoundNumber,
		RoundTimeRemaining: e.Round.TimeRemaining,
		SafeZoneRadius:     e.Arena.ZoneRadius,
	}

	bestKills := -1
	for _, bot := range e.Bots {
		if bot.IsAlive {
			snap.BotsAlive++
		}
		if bot.RoundKills > bestKills {
			bestKills = bot.RoundKills
			snap.TopBotName = bot.Name
		}
	}

	return snap
}

// GetMapFeatures returns copies of the current teleport pads, hazard zones,
// and capture pads under the read lock. The REST map endpoint uses the atomic
// GetArenaMapSnapshot view instead.
func (e *GameEngine) GetMapFeatures() ([]TeleportPad, []HazardZone, []CapturePad) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	pads := make([]TeleportPad, len(e.TeleportPads))
	copy(pads, e.TeleportPads)
	zones := make([]HazardZone, len(e.HazardZones))
	copy(zones, e.HazardZones)
	capturePads := make([]CapturePad, len(e.CapturePads))
	copy(capturePads, e.CapturePads)
	return pads, zones, capturePads
}

// ArenaMapSnapshot is one atomic REST view of terrain and its round features.
// Pending pre-generated terrain deliberately carries no previous-round
// features or mode; those are not resolved until startRound.
type ArenaMapSnapshot struct {
	Terrain         *TerrainGrid
	MapShape        MapShape
	GameMode        GameMode
	Tick            int
	Modifier        RoundModifier
	FeaturesPending bool
	TeleportPads    []TeleportPad
	HazardZones     []HazardZone
	CapturePads     []CapturePad
}

func (e *GameEngine) GetArenaMapSnapshot() ArenaMapSnapshot {
	e.mu.RLock()
	defer e.mu.RUnlock()

	snapshot := ArenaMapSnapshot{
		Terrain:         ActiveTerrain,
		MapShape:        ActiveMapShape,
		Tick:            e.TickCount,
		Modifier:        e.Round.Modifier,
		FeaturesPending: e.NextTerrain != nil,
	}
	if snapshot.FeaturesPending {
		return snapshot
	}
	snapshot.GameMode = ActiveModeRules.Mode
	snapshot.TeleportPads = append([]TeleportPad(nil), e.TeleportPads...)
	snapshot.HazardZones = append([]HazardZone(nil), e.HazardZones...)
	snapshot.CapturePads = append([]CapturePad(nil), e.CapturePads...)
	return snapshot
}

// GetActiveMap returns the active terrain grid, map shape, and game mode under
// the engine read lock. Callers must use a read-locked engine snapshot instead
// of package globals, which can otherwise expose a torn round transition.
func (e *GameEngine) GetActiveMap() (*TerrainGrid, MapShape, GameMode) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return ActiveTerrain, ActiveMapShape, ActiveModeRules.Mode
}

// ConnectedBotCount returns the number of connected bots (active + waiting)
// under the read lock.
func (e *GameEngine) ConnectedBotCount() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.connectedBotCountLocked()
}

func (e *GameEngine) connectedBotCountLocked() int {
	connected := 0
	for _, bot := range e.Bots {
		if !bot.ReconnectPending {
			connected++
		}
	}
	for _, bot := range e.WaitingBots {
		if !bot.ReconnectPending {
			connected++
		}
	}
	return connected
}

// GetBountyBoard returns a snapshot of the current cross-round bounty board.
func (e *GameEngine) GetBountyBoard() []BountyEntry {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.Bounty.Snapshot()
}

// RestoreBountyBoard replaces the in-memory bounty board from persisted entries.
func (e *GameEngine) RestoreBountyBoard(entries []db.BountyBoardEntry) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Bounty.Restore(entries)
}

func (e *GameEngine) persistBountyBoardAsync() {
	if db.Pool == nil {
		return
	}

	snapshot := e.Bounty.Snapshot()

	rows := make([]db.BountyBoardEntry, 0, len(snapshot))
	for _, entry := range snapshot {
		rows = append(rows, db.BountyBoardEntry{
			BotID:        entry.BotID,
			Name:         entry.Name,
			AvatarColor:  entry.AvatarColor,
			Weapon:       entry.Weapon,
			WinStreak:    entry.WinStreak,
			BountyPoints: entry.BountyPoints,
			Claims:       entry.Claims,
			IsTarget:     entry.IsTarget,
		})
	}
	generation := e.bountyPersistGeneration.Add(1)

	safeGo(func() {
		if err := e.persistBountyBoardSnapshot(
			context.Background(), generation, rows, db.ReplaceBountyBoardEntries,
		); err != nil {
			slog.Error("failed to persist bounty board", "error", err)
		}
	})
}

type bountyBoardSnapshotWriter func(context.Context, []db.BountyBoardEntry) error

func (e *GameEngine) persistBountyBoardSnapshot(
	ctx context.Context,
	generation uint64,
	rows []db.BountyBoardEntry,
	write bountyBoardSnapshotWriter,
) error {
	e.bountyPersistMu.Lock()
	defer e.bountyPersistMu.Unlock()
	if generation != e.bountyPersistGeneration.Load() {
		return nil
	}
	return write(ctx, rows)
}

// --------------------------------------------------------------------------
// Internal helpers
// --------------------------------------------------------------------------

// applyFallbacks assigns AI fallback actions to bots that have no pending
// action and are alive and not stunned.
func (e *GameEngine) applyFallbacks() {
	fallbackIdleTicks := config.C.TickRate * 3
	if fallbackIdleTicks < 1 {
		fallbackIdleTicks = 1
	}
	var nearbyIDs []string
	var nearbyBots []*BotState
	for _, bot := range e.Bots {
		if bot.ReconnectPending {
			bot.PendingAction = nil
			continue
		}
		if bot.PendingAction != nil || !bot.IsAlive || bot.StunTicks > 0 {
			continue
		}
		if bot.ReconnectActionGraceUntilTick > 0 {
			continue
		}
		lastActionTick := bot.LastActionTick
		if lastActionTick == 0 {
			lastActionTick = e.Round.StartTick
		}
		if e.TickCount-lastActionTick > fallbackIdleTicks {
			continue
		}

		viewRadius := float64(config.C.FogRadius) * config.C.PathfindingCellSize
		nearbyIDs = e.Grid.QueryRadiusInto(bot.Position, viewRadius, nearbyIDs[:0])
		nearbyBots = nearbyBots[:0]
		for _, id := range nearbyIDs {
			if id == bot.BotID {
				continue
			}
			if other, ok := e.Bots[id]; ok && other.IsAlive {
				// Team modes: fallback AI only considers enemies as targets.
				if SameTeam(bot, other) {
					continue
				}
				nearbyBots = append(nearbyBots, other)
			}
		}

		fb := GetFallbackAction(bot, nearbyBots, bot.FallbackBehavior, e.Arena)
		if fb != nil {
			bot.PendingAction = fb
		}
	}
}

// processUseItems handles USE_ITEM actions by attempting to collect the
// specified pickup.
func (e *GameEngine) processUseItems() {
	for _, bot := range e.Bots {
		if bot.PendingAction == nil || !bot.IsAlive {
			continue
		}

		switch bot.PendingAction.Type {
		case ActionUseItem:
			if rejectControlledAction(bot, "use_item", false) {
				continue
			}
			ok := CollectByAction(bot, bot.PendingAction.ItemID, &e.Pickups)
			if ok {
				bot.LastActionResult = &ActionResult{
					Action:  "use_item",
					Success: true,
					Message: "item collected",
				}
			} else {
				bot.LastActionResult = &ActionResult{
					Action:  "use_item",
					Success: false,
					Message: "item not found or out of range",
				}
			}

		case ActionPlaceMine:
			if rejectControlledAction(bot, "place_mine", true) {
				continue
			}
			mine := PlaceMine(bot, &e.Landmines, e.TickCount)
			if mine != nil {
				bot.LastActionResult = &ActionResult{
					Action:  "place_mine",
					Success: true,
					Message: "mine placed",
				}
			} else {
				bot.LastActionResult = &ActionResult{
					Action:  "place_mine",
					Success: false,
					Message: "max mines reached",
				}
			}

		case ActionUseGravityWell:
			if rejectControlledAction(bot, "use_gravity_well", true) {
				continue
			}
			if bot.GravityWellCharge <= 0 {
				bot.LastActionResult = &ActionResult{
					Action:  "use_gravity_well",
					Success: false,
					Message: "no gravity well charge",
				}
			} else if bot.PendingAction.TargetPosition == nil {
				bot.LastActionResult = &ActionResult{
					Action:  "use_gravity_well",
					Success: false,
					Message: "target_position required",
				}
			} else {
				targetPos := normalizeActionTargetPosition(*bot.PendingAction.TargetPosition)
				well := CreateGravityWell(bot.BotID, targetPos)
				e.GravityWells = append(e.GravityWells, *well)
				bot.GravityWellCharge--
				bot.LastActionResult = &ActionResult{
					Action:  "use_gravity_well",
					Success: true,
					Message: "gravity well deployed",
				}
			}

		case ActionGrapple:
			e.processGrappleAbility(bot)
		}
	}
}

// processGrappleAbility handles the universal grapple ability (separate from
// the grapple weapon). It can either yank an enemy into close range or latch
// onto an anchor point and pull the user across the arena.
func (e *GameEngine) processGrappleAbility(bot *BotState) {
	offensive := bot != nil && bot.PendingAction != nil && bot.PendingAction.TargetID != ""
	if rejectControlledAction(bot, "grapple", offensive) {
		return
	}
	if bot.GrappleCharges <= 0 {
		bot.LastActionResult = &ActionResult{
			Action:  "grapple",
			Success: false,
			Message: "no grapple charges remaining",
		}
		return
	}
	if bot.GrappleCooldown > 0 {
		bot.LastActionResult = &ActionResult{
			Action:  "grapple",
			Success: false,
			Message: "grapple on cooldown",
		}
		return
	}
	maxRange := config.C.GrappleAbilityRangeTiles
	if maxRange <= 0 {
		maxRange = 12
	}

	targetID := bot.PendingAction.TargetID
	targetPos := bot.PendingAction.TargetPosition
	if targetID == "" && targetPos == nil {
		bot.LastActionResult = &ActionResult{
			Action:  "grapple",
			Success: false,
			Message: "target or target_position required",
		}
		return
	}

	if targetPos != nil {
		normalizedTarget := normalizeActionTargetPosition(*targetPos)
		if !IsInRange(bot.Position, normalizedTarget, maxRange) {
			bot.LastActionResult = &ActionResult{
				Action:  "grapple",
				Success: false,
				Message: "anchor out of range",
			}
			return
		}
		if CombatLineBlocked(bot.Position, normalizedTarget, e.Arena.Obstacles) {
			bot.LastActionResult = &ActionResult{
				Action:  "grapple",
				Success: false,
				Message: "no line of sight",
			}
			return
		}

		from := bot.Position
		landing, ok := findGrappleLandingPosition(bot.Position, normalizedTarget)
		if !ok {
			bot.LastActionResult = &ActionResult{
				Action:  "grapple",
				Success: false,
				Message: "no valid anchor landing",
			}
			return
		}

		bot.Position = landing
		bot.LastValidPosition = landing
		e.Grid.Update(bot.BotID, landing)
		bot.GrappleCharges--
		bot.GrappleCooldown = config.C.GrappleAbilityCooldownSecs * effectCooldownMultiplier(bot)
		e.appendArenaEvents(buildGrappleEvent(bot.BotID, "", from, normalizedTarget, landing, true, e.TickCount))
		bot.LastActionResult = &ActionResult{
			Action:  "grapple",
			Success: true,
			Message: "grapple anchor pull",
		}
		return
	}

	target, ok := e.Bots[targetID]
	if !ok || !target.IsAlive {
		bot.LastActionResult = &ActionResult{
			Action:  "grapple",
			Success: false,
			Target:  targetID,
			Message: "target not found or dead",
		}
		return
	}
	if target == bot {
		bot.LastActionResult = &ActionResult{
			Action:  "grapple",
			Success: false,
			Target:  targetID,
			Message: "cannot grapple self",
		}
		return
	}
	if !ActiveModeRules.CanDamage(bot, target) {
		bot.LastActionResult = &ActionResult{
			Action:  "grapple",
			Success: false,
			Target:  targetID,
			Message: "friendly fire is disabled",
		}
		return
	}
	if !IsInRange(bot.Position, target.Position, maxRange) {
		bot.LastActionResult = &ActionResult{
			Action:  "grapple",
			Success: false,
			Target:  targetID,
			Message: "target out of grapple range",
		}
		return
	}
	if CombatLineBlocked(bot.Position, target.Position, e.Arena.Obstacles) {
		bot.LastActionResult = &ActionResult{
			Action:  "grapple",
			Success: false,
			Target:  targetID,
			Message: "no line of sight",
		}
		return
	}
	if target.InvulnTicks > 0 {
		bot.LastActionResult = &ActionResult{
			Action:  "grapple",
			Success: false,
			Target:  targetID,
			Message: "target dodging",
		}
		return
	}
	damage := config.C.GrappleAbilityDamage
	if damage <= 0 {
		damage = 15
	}
	rawDmg := CalculateDamage(damage, bot.AttackMultiplier, target.DefenseReduction)
	// Keep the universal ability distinct from the grapple weapon so adaptive
	// balance never treats this shared utility damage as weapon output.
	dealt := ApplyDamage(target, bot, rawDmg, "grapple_ability", e.TickCount)

	from := target.Position
	landing, ok := findAdjacentPullPosition(bot.Position, target.Position)
	if ok {
		target.Position = landing
		target.LastValidPosition = landing
		e.Grid.Update(target.BotID, landing)
	}

	stunTicks := config.C.GrappleAbilityStunTicks
	if stunTicks <= 0 {
		stunTicks = 3
	}
	target.StunTicks = stunTicks
	markDisrupted(target, config.C.ShieldDisruptWindowTicks)

	bot.GrappleCharges--
	bot.GrappleCooldown = config.C.GrappleAbilityCooldownSecs * effectCooldownMultiplier(bot)
	e.appendArenaEvents(buildGrappleEvent(bot.BotID, target.BotID, from, target.Position, target.Position, false, e.TickCount))

	bot.LastActionResult = &ActionResult{
		Action:  "grapple",
		Success: true,
		Target:  target.BotID,
		Damage:  dealt,
		Message: "grapple pull",
	}
}

func findAdjacentPullPosition(pullerPos, targetPos Vec2) (Vec2, bool) {
	if ActiveTerrain == nil {
		dir := targetPos.Sub(pullerPos).Normalized()
		if dir.Length() < 1e-9 {
			dir = NewVec2(1, 0)
		}
		return pullerPos.Add(dir.Scale(config.C.PathfindingCellSize)), true
	}

	pullerCell := ActiveTerrain.WorldToGrid(pullerPos)
	targetCell := ActiveTerrain.WorldToGrid(targetPos)
	bestCell := targetCell
	bestDist := 1 << 30
	found := false
	for dx := -1; dx <= 1; dx++ {
		for dy := -1; dy <= 1; dy++ {
			if dx == 0 && dy == 0 {
				continue
			}
			nc := [2]int{pullerCell[0] + dx, pullerCell[1] + dy}
			if ActiveTerrain.IsBlocked(nc[0], nc[1]) {
				continue
			}
			d := GridDistance(targetCell, nc)
			if d < bestDist {
				bestDist = d
				bestCell = nc
				found = true
			}
		}
	}
	if !found {
		return Vec2{}, false
	}
	return ActiveTerrain.GridToWorld(bestCell), true
}

func findGrappleLandingPosition(from, anchor Vec2) (Vec2, bool) {
	if ActiveTerrain == nil {
		return anchor, true
	}

	anchorCell := ActiveTerrain.WorldToGrid(anchor)
	fromCell := ActiveTerrain.WorldToGrid(from)
	bestCell := anchorCell
	bestDist := 1 << 30
	found := false

	tryCell := func(cell [2]int) {
		if ActiveTerrain.IsBlocked(cell[0], cell[1]) {
			return
		}
		d := GridDistance(fromCell, cell)
		if d < bestDist {
			bestDist = d
			bestCell = cell
			found = true
		}
	}

	tryCell(anchorCell)
	if !found {
		for radius := 1; radius <= 2 && !found; radius++ {
			for dx := -radius; dx <= radius; dx++ {
				for dy := -radius; dy <= radius; dy++ {
					if dx != -radius && dx != radius && dy != -radius && dy != radius {
						continue
					}
					tryCell([2]int{anchorCell[0] + dx, anchorCell[1] + dy})
				}
			}
		}
	}

	if !found {
		return Vec2{}, false
	}
	return ActiveTerrain.GridToWorld(bestCell), true
}

// updateStuckDetection advances the per-bot stuck/still counters by comparing
// the current grid cell with the PREVIOUS tick's recorded cell. Comparing
// against LastValidPosition is wrong: every movement path syncs it to the
// current position within the same tick, which made this check read "not
// moved" for every bot on every tick — spear brace was permanently ready and
// moving bots were teleport-nudged sideways every second.
func updateStuckDetection(bot *BotState, cell [2]int) {
	if bot.PrevTickCellSet && cell == bot.PrevTickCell {
		bot.StuckTicks++
		bot.StillTicks++
	} else {
		bot.StuckTicks = 0
		bot.StillTicks = 0
	}
	bot.PrevTickCell = cell
	bot.PrevTickCellSet = true
}

// applyZoneDamage hurts alive bots that are outside the safe zone.
func (e *GameEngine) applyZoneDamage() {
	dmg := config.C.ZoneDamagePerTick * SuddenDeathDamageMultiplier()
	for _, bot := range e.Bots {
		if !bot.IsAlive {
			continue
		}
		if !e.Arena.IsInZone(bot.Position) {
			bot.HP -= dmg
		}
	}
}

// handleKillCredits processes death events: updates killer streaks, ELO, and
// the kill feed.
func (e *GameEngine) handleKillCredits(deaths []DeathEvent) {
	for i := range deaths {
		death := &deaths[i]
		victim, victimOk := e.Bots[death.VictimID]

		var killer *BotState
		if death.KillerID != "" {
			killer = e.Bots[death.KillerID]
		}

		killerName := death.KillerName
		weapon := death.Weapon
		damage := death.Damage

		if killer != nil {
			killer.KillStreak++
			if killer.KillStreak > killer.BestKillStreak {
				killer.BestKillStreak = killer.KillStreak
			}
			killer.RoundKills++
			killerName = killer.Name
			if weapon == "" {
				weapon = killer.Weapon
			}
			if damageSourceMatchesEquippedWeapon(killer, weapon) {
				killer.RoundWeaponKills++
			}
			death.KillerName = killerName
			death.Weapon = weapon

			// Team battle: kills are the team score. (CTF scores captures
			// instead — see UpdateCTFFlags — so don't mix kills in there.)
			if e.ModeRules.HasTeams() && !e.ModeRules.UsesFlags && killer.Team > 0 &&
				(victim == nil || victim.Team != killer.Team) {
				e.TeamScores[killer.Team]++
			}

			if victimOk {
				ApplyEloChange(killer, victim)
				// Bounty bonus for killing the bounty target.
				e.Bounty.OnKill(killer, victim)
				if killer.BountyTokenBonus > 0 {
					killer.Elo = ClampElo(killer.Elo + killer.BountyTokenBonus)
					killer.RoundDamageDealt += float64(killer.BountyTokenBonus)
					killer.BountyTokenBonus = 0
					killer.ActiveEffects = removeEffectByName(killer.ActiveEffects, "bounty_token")
				}
				e.persistBountyBoardAsync()
			}
		} else if victimOk {
			e.Bounty.OnDeath(victim)
			e.persistBountyBoardAsync()
		}

		victimName := ""
		if victimOk {
			victimName = victim.Name
		}

		e.KillFeed.Add(killerName, victimName, weapon, e.TickCount)

		e.KillEvents = append(e.KillEvents, KillEvent{
			KillerID:   death.KillerID,
			VictimID:   death.VictimID,
			VictimName: victimName,
			Weapon:     weapon,
			Damage:     damage,
			KillStreak: 0,
			RoundKills: 0,
		})

		// Fill in kill event stats from the killer if available.
		if killer != nil {
			last := &e.KillEvents[len(e.KillEvents)-1]
			last.KillStreak = killer.KillStreak
			last.RoundKills = killer.RoundKills
		}

		// Log the kill to the database. Waits for the round row to exist:
		// kill_logs.round_id has a foreign key on rounds(id), and a kill in
		// the opening ticks could otherwise race the async CreateRound.
		if killer != nil && victimOk {
			roundID, tick := e.Round.RoundID, e.TickCount
			killerID, victimID, killerHP := killer.BotID, victim.BotID, int(killer.HP)
			ready := e.roundDBReady
			safeGo(func() {
				afterRoundCreated(ready, "kill log", roundID, func() {
					InsertKillLog(context.Background(), roundID, killerID, victimID, weapon, damage, killerHP, tick)
				})
			})
		}

		// Emit game event for dashboard.
		if GameEventHook != nil {
			GameEventHook("kill", map[string]interface{}{
				"killer_id":   death.KillerID,
				"killer_name": killerName,
				"victim_id":   death.VictimID,
				"victim_name": victimName,
				"weapon":      weapon,
				"damage":      damage,
				"tick":        e.TickCount,
			})
		}
	}
}

// checkAFK kicks bots that haven't sent an action within the AFK timeout.
// Removes them directly from e.Bots so they stop receiving tick messages
// immediately rather than waiting for the reader goroutine to notice the
// closed connection.
func (e *GameEngine) checkAFK() {
	c := &config.C
	var toRemove []string
	var snapshots []BotStatsSnapshot
	for _, bot := range e.Bots {
		isAFK := false
		reason := "AFK timeout"
		eventName := "afk_kick"

		// Bot with no websocket connection is a ghost — remove immediately.
		if bot.ReconnectPending {
			graceTicks := int(math.Ceil(c.WSReconnectGraceSecs * float64(c.TickRate)))
			if graceTicks < 1 {
				graceTicks = 1
			}
			if e.TickCount-bot.DisconnectedAtTick < graceTicks {
				continue
			}
			isAFK = true
			reason = "reconnect grace expired"
			eventName = "reconnect_timeout"
		} else if bot.Conn == nil {
			isAFK = true
		} else if !bot.IsAlive {
			// Dead bots can't act — don't kick them for AFK.
			continue
		} else if bot.ReconnectActionGraceUntilTick > 0 {
			if e.TickCount <= bot.ReconnectActionGraceUntilTick {
				continue
			}
			isAFK = true
			reason = "no action after reconnect"
			eventName = "reconnect_action_timeout"
		} else if bot.LastActionTick > 0 && e.TickCount-bot.LastActionTick > c.AFKTimeoutTicks {
			// Standard AFK: had actions before but stopped.
			isAFK = true
		} else if bot.LastActionTick == 0 && e.Round.Phase == PhaseActive &&
			e.TickCount-e.Round.StartTick > c.AFKTimeoutTicks {
			// Never acted since round started.
			isAFK = true
		}

		if isAFK {
			bot.SignalTransportClose(BotTransportCloseCause{
				Source: "server_policy", CloseCode: websocket.ClosePolicyViolation, CloseReason: reason,
			})
			SendKick(bot, reason)
			if bot.Conn != nil {
				conn := bot.Conn
				closeReason := reason
				safeGo(func() {
					_ = conn.WriteControl(
						websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.ClosePolicyViolation, closeReason),
						time.Now().Add(time.Second),
					)
					_ = conn.Close()
				})
			}
			toRemove = append(toRemove, bot.BotID)
			snapshots = append(snapshots, takeBotStatsSnapshot(bot))
			slog.Info("bot removed by activity policy", "bot", bot.Name, "bot_id", bot.BotID, "reason", reason)
			if GameEventHook != nil {
				GameEventHook(eventName, map[string]interface{}{
					"bot_id":   bot.BotID,
					"bot_name": bot.Name,
					"reason":   reason,
				})
			}
		}
	}
	for _, id := range toRemove {
		delete(e.Bots, id)
		e.Grid.Remove(id)
	}
	for _, snapshot := range snapshots {
		snapshot := snapshot
		safeGo(func() { PersistSingleBot(context.Background(), snapshot) })
	}
}

// sendBotTickUpdates sends the per-tick state update to each connected bot.
// Uses fog_radius (grid tiles) to determine entity visibility.
// sendBotTickUpdates builds each connected bot's tick envelope and stages it
// in the outbox. Everything here runs under the engine lock and produces
// value snapshots only; flushTickOutbox marshals and delivers them after the
// lock is released.
func (e *GameEngine) sendBotTickUpdates() {
	fogRadius := config.C.FogRadius
	viewRadius := float64(fogRadius) * config.C.PathfindingCellSize

	// Shared per-tick data, identical for every recipient: safe_zone submap,
	// game-mode fields, service status, and the round extras. Built once here
	// instead of once per bot.
	safeZone := BuildSafeZoneGridView(e.Arena)
	var modeExtra TickMessage
	AddModeTickExtra(&modeExtra, e.ModeRules, e.TeamScores, e.Flags)
	// Maintenance data is repeated only while active. This is a bounded
	// reliability fallback for a direct control message dropped from a slow
	// bot's normal send queue.
	var serviceStatus *ServiceStatus
	if status := e.GetServiceStatus(); status.Maintenance != nil {
		serviceStatus = &status
	}
	suddenDeathActive := e.SuddenDeath.Active
	suddenDeathStall := e.SuddenDeath.StallActive
	bountyTarget := e.Bounty.TargetID
	roundTick := e.TickCount - e.Round.StartTick
	roundModifier := string(e.Round.Modifier)

	// Observer-independent nearby-bot views, built at most once per visible
	// bot per tick. Only has_los/rear_exposed depend on the observer, so each
	// observer gets a cheap struct copy via ObservedBy instead of a full
	// rebuild per observer-target pair.
	baseViews := make(map[string]*BotNearbyView, len(e.Bots))

	// Static-entity grid cells, in the same spirit as baseViews: WorldToGrid
	// is observer-independent, so each entity list's cells are computed once
	// per tick here and the per-bot fog scans below only pay the GridDistance
	// compare (previously every bot re-converted every entity).
	var (
		pickupCells      [][2]int
		teleportPadCells [][2]int
		capturePadCells  [][2]int
		hazardZoneCells  [][2]int
		burnFieldCells   [][2]int
		gravityWellCells [][2]int
	)
	if ActiveTerrain != nil {
		pickupCells = make([][2]int, len(e.Pickups))
		for i := range e.Pickups {
			pickupCells[i] = ActiveTerrain.WorldToGrid(e.Pickups[i].Position)
		}
		teleportPadCells = make([][2]int, len(e.TeleportPads))
		for i := range e.TeleportPads {
			teleportPadCells[i] = ActiveTerrain.WorldToGrid(e.TeleportPads[i].Position)
		}
		capturePadCells = make([][2]int, len(e.CapturePads))
		for i := range e.CapturePads {
			capturePadCells[i] = ActiveTerrain.WorldToGrid(e.CapturePads[i].Position)
		}
		hazardZoneCells = make([][2]int, len(e.HazardZones))
		for i := range e.HazardZones {
			hazardZoneCells[i] = ActiveTerrain.WorldToGrid(e.HazardZones[i].Position)
		}
		burnFieldCells = make([][2]int, len(e.BurnFields))
		for i := range e.BurnFields {
			burnFieldCells[i] = ActiveTerrain.WorldToGrid(e.BurnFields[i].Position)
		}
		gravityWellCells = make([][2]int, len(e.GravityWells))
		for i := range e.GravityWells {
			gravityWellCells[i] = ActiveTerrain.WorldToGrid(e.GravityWells[i].Position)
		}
	}

	// Armed-mine cells for the per-bot proximity count. Only armed mines can
	// count, and only while a terrain grid is active (the previous per-bot
	// loop never counted without one), so both filters apply once here.
	type armedMineCell struct {
		ownerID string
		cell    [2]int
	}
	var armedMineCells []armedMineCell
	if ActiveTerrain != nil {
		for _, mine := range e.Landmines {
			if mine.Armed {
				armedMineCells = append(armedMineCells, armedMineCell{
					ownerID: mine.OwnerID,
					cell:    ActiveTerrain.WorldToGrid(mine.Position),
				})
			}
		}
	}

	var nearbyIDs []string
	for _, bot := range e.Bots {
		if bot.SendChan == nil {
			continue
		}

		yourState := BuildYourState(bot, e.Arena, e.KillFeed, e.TickCount)

		// Cache the bot's grid cell once per tick; every entity-visibility
		// check below reuses it instead of recomputing WorldToGrid.
		var botCell [2]int
		if ActiveTerrain != nil {
			botCell = ActiveTerrain.WorldToGrid(bot.Position)
		}

		// Build nearby entities using fog radius.
		nearbyIDs = e.Grid.QueryRadiusInto(bot.Position, viewRadius, nearbyIDs[:0])
		var nearby []any

		nearbyBotCount := 0
		for _, id := range nearbyIDs {
			if id == bot.BotID {
				continue
			}
			if other, ok := e.Bots[id]; ok {
				base := baseViews[id]
				if base == nil {
					baseView := BuildBotNearbyBaseView(other)
					base = &baseView
					baseViews[id] = base
				}
				// ObservedBy copies the base view per observer — required, as
				// each copy is marshaled independently after unlock. The
				// per-pair LOS raycast is correct behavior and stays.
				nearby = append(nearby, base.ObservedBy(bot.Position))
				nearbyBotCount++
			}
		}

		// Include pickups within fog radius.
		for i := range e.Pickups {
			if ActiveTerrain != nil {
				if GridDistance(botCell, pickupCells[i]) <= fogRadius {
					nearby = append(nearby, BuildPickupNearbyView(e.Pickups[i]))
				}
			} else if bot.Position.DistanceTo(e.Pickups[i].Position) <= viewRadius {
				nearby = append(nearby, BuildPickupNearbyView(e.Pickups[i]))
			}
		}

		// Include teleport pads within fog radius.
		for i := range e.TeleportPads {
			if ActiveTerrain != nil {
				if GridDistance(botCell, teleportPadCells[i]) <= fogRadius {
					nearby = append(nearby, BuildTeleportPadViewForBot(e.TeleportPads[i], e.TickCount, true, bot))
				}
			} else if bot.Position.DistanceTo(e.TeleportPads[i].Position) <= viewRadius {
				nearby = append(nearby, BuildTeleportPadViewForBot(e.TeleportPads[i], e.TickCount, true, bot))
			}
		}

		// Include capture pads within fog radius.
		for i := range e.CapturePads {
			if ActiveTerrain != nil {
				if GridDistance(botCell, capturePadCells[i]) <= fogRadius+3 {
					nearby = append(nearby, BuildCapturePadView(e.CapturePads[i], e.TickCount, true))
				}
			} else if bot.Position.DistanceTo(e.CapturePads[i].Position) <= viewRadius {
				nearby = append(nearby, BuildCapturePadView(e.CapturePads[i], e.TickCount, true))
			}
		}

		// Include hazard zones within fog radius.
		for i := range e.HazardZones {
			if ActiveTerrain != nil {
				if GridDistance(botCell, hazardZoneCells[i]) <= fogRadius+4 {
					nearby = append(nearby, BuildHazardZoneView(e.HazardZones[i], true, e.Round.Modifier))
				}
			} else if bot.Position.DistanceTo(e.HazardZones[i].Position) <= viewRadius {
				nearby = append(nearby, BuildHazardZoneView(e.HazardZones[i], true, e.Round.Modifier))
			}
		}

		// Include staff burn fields within fog radius.
		for i := range e.BurnFields {
			if ActiveTerrain != nil {
				if GridDistance(botCell, burnFieldCells[i]) <= fogRadius+2 {
					nearby = append(nearby, BuildBurnFieldView(e.BurnFields[i], true))
				}
			} else if bot.Position.DistanceTo(e.BurnFields[i].Position) <= viewRadius {
				nearby = append(nearby, BuildBurnFieldView(e.BurnFields[i], true))
			}
		}

		// Include gravity wells within fog radius.
		for i := range e.GravityWells {
			if ActiveTerrain != nil {
				if GridDistance(botCell, gravityWellCells[i]) <= fogRadius {
					nearby = append(nearby, BuildGravityWellView(e.GravityWells[i], true))
				}
			} else if bot.Position.DistanceTo(e.GravityWells[i].Position) <= viewRadius {
				nearby = append(nearby, BuildGravityWellView(e.GravityWells[i], true))
			}
		}

		// Include own landmines (only visible to owner).
		for _, mine := range e.Landmines {
			if mine.OwnerID == bot.BotID {
				nearby = append(nearby, BuildMineView(mine, true))
			}
		}

		// Include bounty target position (visible to all bots regardless of fog).
		if e.Bounty.TargetID != "" && e.Bounty.TargetID != bot.BotID {
			if bountyBot, ok := e.Bots[e.Bounty.TargetID]; ok && bountyBot.IsAlive {
				nearby = append(nearby, BountyTargetView{
					Type:     "bounty_target",
					ID:       bountyBot.BotID,
					BotID:    bountyBot.BotID,
					Name:     bountyBot.Name,
					Position: posToGrid(bountyBot.Position),
				})
			}
		}

		// Build directional hints when no bots are within fog radius.
		var hints []HintView
		if nearbyBotCount == 0 {
			hints = buildHints(bot, e.Bots, e.Pickups)
		}

		// Count armed mines within 3 grid cells of the bot (cells precomputed
		// above; armedMineCells is empty when no terrain grid is active).
		nearbyMineCount := 0
		for _, mine := range armedMineCells {
			if mine.ownerID != bot.BotID && GridDistance(botCell, mine.cell) <= 3 {
				nearbyMineCount++
			}
		}

		// Assemble the envelope with the sudden death, bounty, and game-mode
		// extras (shared per-tick values captured above).
		msg := NewTickMessage(yourState, nearby, e.TickCount, safeZone, hints, fogRadius)
		msg.SuddenDeath = suddenDeathActive
		msg.SuddenDeathStall = suddenDeathStall
		msg.BountyTarget = bountyTarget
		msg.NearbyMines = nearbyMineCount
		msg.RoundTick = roundTick
		msg.RoundModifier = roundModifier
		msg.ServiceStatus = serviceStatus
		msg.GameMode = modeExtra.GameMode
		msg.TeamScores = modeExtra.TeamScores
		msg.Flags = modeExtra.Flags
		// Void tiles within the bot's fog radius (omitted entirely while
		// sudden death is inactive to keep payloads small; present — possibly
		// empty — whenever it is active).
		if suddenDeathActive {
			tiles := e.SuddenDeath.VoidTilesNear(botCell, fogRadius)
			msg.VoidTiles = &tiles
		}

		e.outbox.botTicks = append(e.outbox.botTicks, outboundBotTick{bot: bot, msg: msg})
	}
}

// sendLobbyStateUpdate broadcasts lobby/intermission state to spectators.
// During active rounds, waiting bots are included so the lobby tab stays populated.
func (e *GameEngine) sendLobbyStateUpdate() {
	c := &config.C

	// With nobody to receive it — no spectators AND no waiting bots — skip
	// building and marshaling the lobby state entirely. (An idle server
	// previously paid this at 10 Hz forever. Both recipient groups must be
	// empty: the tail of this function serves waiting bots independently of
	// spectators.)
	e.spectatorsMu.RLock()
	lobbySpectatorCount := len(e.Spectators)
	e.spectatorsMu.RUnlock()
	if lobbySpectatorCount == 0 && len(e.WaitingBots) == 0 {
		return
	}

	// During lobby phase, all bots are in e.Bots. During active/intermission,
	// mid-round joiners sit in e.WaitingBots.
	lobbyBots := e.Bots
	if e.Round.Phase != PhaseLobby {
		lobbyBots = e.WaitingBots
	}

	players := buildLobbyPlayers(lobbyBots)

	var countdown interface{}
	if e.Round.LobbyCountdownTicks > 0 {
		secs := e.Round.LobbyCountdownTicks / c.TickRate
		if secs < 1 {
			secs = 1
		}
		countdown = secs
	}

	staged := &outboundLobbyState{
		state: map[string]interface{}{
			"type":           "lobby_state",
			"tick":           e.TickCount,
			"bots_connected": len(lobbyBots),
			"bots_needed":    c.MinBotsToStart,
			"countdown":      countdown,
			"players":        players,
		},
	}

	// Also stage lobby updates for waiting bots so they know they're queued.
	if len(e.WaitingBots) > 0 {
		staged.waitingConnected = len(lobbyBots)
		staged.waitingMinBots = c.MinBotsToStart
		staged.waitingPlayers = players
		staged.waitingBots = make([]*BotState, 0, len(e.WaitingBots))
		for _, bot := range e.WaitingBots {
			staged.waitingBots = append(staged.waitingBots, bot)
		}
	}

	// Marshal + broadcast happen in flushTickOutbox after the lock drops.
	e.outbox.lobbyState = staged
}

// sendSpectatorUpdate broadcasts the full arena state to all spectators.
// Static round data (obstacles, map shape) is only included on keyframe
// ticks and right after a spectator joins; renderers keep their last
// received copy in between, which cuts steady-state bandwidth noticeably
// (obstacles dominate the static payload, especially on cave maps).
func (e *GameEngine) sendSpectatorUpdate() {
	// With no spectators connected, skip building and marshaling the full
	// arena state (per-bot view maps, pads, zones, mines...) every 100 ms.
	// forceKeyframe is left un-consumed so the first broadcast after a
	// spectator joins still carries the static round data, and the transient
	// event buffer is drained the same way the broadcast path drains it.
	e.spectatorsMu.RLock()
	spectatorCount := len(e.Spectators)
	e.spectatorsMu.RUnlock()
	if spectatorCount == 0 {
		e.RecentEvents = nil
		return
	}

	state := BuildSpectatorState(e.Bots, e.Arena, e.Pickups, e.KillFeed, e.TickCount, e.Round.StartTick, e.WaitingBots, e.Round.Modifier)

	keyframeInterval := config.C.SpectatorKeyframeInterval
	e.spectatorsMu.Lock()
	keyframe := keyframeInterval <= 1 || e.forceKeyframe ||
		(keyframeInterval > 0 && e.TickCount%keyframeInterval == 0)
	e.forceKeyframe = false
	e.spectatorsMu.Unlock()
	if keyframe {
		state.ArenaSize = []float64{e.Arena.Width, e.Arena.Height}
		// Void tiles ride keyframes only (see SpectatorState.VoidTiles).
		// forceKeyframe already guarantees a late-joining spectator gets the
		// current set; new tiles reach spectators within a keyframe interval,
		// which is visually acceptable for a slow-accumulating hazard.
		state.VoidTiles = e.SuddenDeath.GetAllVoidTiles()
	} else {
		state.Obstacles = nil
		state.MaskRects = nil
	}

	// Add new gameplay entities to spectator state.
	for _, pad := range e.TeleportPads {
		state.TeleportPads = append(state.TeleportPads, BuildTeleportPadView(pad, e.TickCount, false))
	}
	for _, pad := range e.CapturePads {
		state.CapturePads = append(state.CapturePads, BuildCapturePadView(pad, e.TickCount, false))
	}
	for _, zone := range e.HazardZones {
		state.HazardZones = append(state.HazardZones, BuildHazardZoneView(zone, false, e.Round.Modifier))
	}
	for _, mine := range e.Landmines {
		state.Landmines = append(state.Landmines, BuildMineView(mine, false))
	}
	for _, well := range e.GravityWells {
		state.GravityWells = append(state.GravityWells, BuildGravityWellView(well, false))
	}
	for _, impact := range e.StaffImpacts {
		state.StaffImpacts = append(state.StaffImpacts, BuildStaffImpactView(impact, false))
	}
	for _, field := range e.BurnFields {
		state.BurnFields = append(state.BurnFields, BuildBurnFieldView(field, false))
	}
	state.SuddenDeath = e.SuddenDeath.Active
	state.SuddenDeathStall = e.SuddenDeath.StallActive
	if e.SuddenDeath.Active {
		state.SuddenDeathMult = SuddenDeathDamageMultiplier()
	}
	state.BountyTarget = e.Bounty.TargetID

	// Game mode metadata.
	state.RoundNumber = e.Round.RoundNumber
	state.GameMode = string(e.ModeRules.Mode)
	state.MapShape = string(ActiveMapShape)
	if e.ModeRules.HasTeams() {
		scores := make(map[string]int, e.ModeRules.TeamCount)
		for team := 1; team <= e.ModeRules.TeamCount; team++ {
			scores[strconv.Itoa(team)] = e.TeamScores[team]
		}
		state.TeamScores = scores
		for _, f := range e.Flags {
			state.Flags = append(state.Flags, BuildFlagView(f))
		}
	}
	if len(e.RecentEvents) > 0 {
		state.Events = append(state.Events, e.RecentEvents...)
	}

	// The snapshot is complete (transient events copied, keyframe flag
	// consumed above); marshal + PreparedMessage + broadcast happen in
	// flushTickOutbox after the lock drops.
	e.outbox.spectator = &state
	e.RecentEvents = nil
}

// sendEventMessages stages buffered death and kill events for the relevant
// bots, then drains the event buffers. Recipient sessions are resolved here,
// under the lock; the events themselves are plain value structs, so marshal
// and delivery run in flushTickOutbox.
func (e *GameEngine) sendEventMessages() {
	for _, ev := range e.DeathEvents {
		if victim, ok := e.Bots[ev.VictimID]; ok {
			e.outbox.deathSends = append(e.outbox.deathSends, outboundDeathMessage{bot: victim, event: ev})
		}
	}
	for _, ev := range e.KillEvents {
		if killer, ok := e.Bots[ev.KillerID]; ok {
			e.outbox.killSends = append(e.outbox.killSends, outboundKillMessage{bot: killer, event: ev})
		}
	}
	// No respawns - dead bots stay dead until next round.

	e.DeathEvents = e.DeathEvents[:0]
	e.KillEvents = e.KillEvents[:0]
}

// buildHints generates directional hints for a bot that has no nearby bots.
// Returns directions to the nearest 3 bots and the nearest pickup of each type.
func buildHints(bot *BotState, allBots map[string]*BotState, pickups []Pickup) []HintView {
	type botDist struct {
		id   string
		dir  Vec2
		dist float64
	}

	// Keep only the nearest three alive bots while scanning. BotID breaks
	// equal-distance ties so map iteration order cannot change hint ordering.
	nearest := make([]botDist, 0, 3)
	for _, other := range allBots {
		if other.BotID == bot.BotID || !other.IsAlive {
			continue
		}
		dir, d := hintVectorInGrid(bot.Position, other.Position)
		candidate := botDist{id: other.BotID, dir: dir, dist: d}

		insertAt := len(nearest)
		for i, current := range nearest {
			if candidate.dist < current.dist || (candidate.dist == current.dist && candidate.id < current.id) {
				insertAt = i
				break
			}
		}
		if insertAt >= 3 {
			continue
		}
		if len(nearest) < 3 {
			nearest = append(nearest, botDist{})
		}
		copy(nearest[insertAt+1:], nearest[insertAt:len(nearest)-1])
		nearest[insertAt] = candidate
	}

	var hints []HintView
	for i := range nearest {
		hints = append(hints, HintView{
			HintType:  "bot",
			Direction: [2]float64{round1(nearest[i].dir.X()), round1(nearest[i].dir.Y())},
			Distance:  round1(nearest[i].dist),
		})
	}

	// Find nearest pickup of each type.
	type pickupDist struct {
		pType PickupType
		dir   Vec2
		dist  float64
	}
	bestPickup := make(map[PickupType]*pickupDist)
	for _, p := range pickups {
		dir, d := hintVectorInGrid(bot.Position, p.Position)
		if existing, ok := bestPickup[p.Type]; !ok || d < existing.dist {
			bestPickup[p.Type] = &pickupDist{pType: p.Type, dir: dir, dist: d}
		}
	}
	for _, pd := range bestPickup {
		hints = append(hints, HintView{
			HintType:   "pickup",
			PickupType: string(pd.pType),
			Direction:  [2]float64{round1(pd.dir.X()), round1(pd.dir.Y())},
			Distance:   round1(pd.dist),
		})
	}

	return hints
}

// hintVectorInGrid converts both endpoints at the public bot-protocol
// boundary. Nearby positions and bot movement are expressed in grid tiles, so
// hint distances must use the same unit rather than leaking world units.
func hintVectorInGrid(from, to Vec2) (Vec2, float64) {
	fromGrid := posToGrid(from)
	toGrid := posToGrid(to)
	delta := NewVec2(float64(toGrid[0]-fromGrid[0]), float64(toGrid[1]-fromGrid[1]))
	return delta.Normalized(), delta.Length()
}

// --------------------------------------------------------------------------
// Admin methods
// --------------------------------------------------------------------------

// Pause pauses the game loop. Tick processing is skipped while paused.
func (e *GameEngine) Pause() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Paused = true
	slog.Info("game paused")
}

// Resume resumes the game loop.
func (e *GameEngine) Resume() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Paused = false
	slog.Info("game resumed")
}

// IsPaused returns whether the engine is paused.
func (e *GameEngine) IsPaused() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.Paused
}

// ResetLeaderboard serializes the database reset with all bot-stat writes and
// reserves a clean persistence baseline without changing the live match score.
// Snapshots captured before a successful reset carry the old epoch and are
// discarded when their background goroutines eventually reach the gate.
func (e *GameEngine) ResetLeaderboard(ctx context.Context, reset func(context.Context) error) error {
	if reset == nil {
		return fmt.Errorf("reset leaderboard: nil reset function")
	}

	// botStatsPersistenceMu is held for the whole operation so the epoch-gate
	// semantics in PersistBotStatsFromSnapshot/PersistRoundBotStats are
	// unchanged. e.mu, however, is released around the DB reset: reset() runs
	// a TRUNCATE whose ACCESS EXCLUSIVE lock can wait behind any concurrent
	// leaderboard SELECT (or a pg_dump), and holding the engine write lock
	// across that round trip froze the entire game world — tick(), every bot
	// action, and every REST read — for an unbounded time.
	botStatsPersistenceMu.Lock()
	defer botStatsPersistenceMu.Unlock()

	e.mu.Lock()
	backups := make(map[*BotState]leaderboardResetBackup, len(e.Bots)+len(e.WaitingBots))
	previousSkippedRound := e.skipLeaderboardRound
	if e.Round.Phase == PhaseActive {
		e.skipLeaderboardRound = e.Round.RoundNumber
	}
	resetBot := func(bot *BotState) {
		if bot == nil {
			return
		}
		if _, seen := backups[bot]; seen {
			return
		}
		backups[bot] = captureLeaderboardResetBackup(bot)
		resetBotLeaderboardState(bot)
	}
	for _, bot := range e.Bots {
		resetBot(bot)
	}
	for _, bot := range e.WaitingBots {
		resetBot(bot)
	}
	e.mu.Unlock()

	if err := reset(ctx); err != nil {
		// Restore ONLY the fields resetBotLeaderboardState touched: ticks
		// advanced live state (position, HP, round score, Grid placement)
		// while the lock was released, so a full-struct restore would revert
		// live gameplay and desync the spatial grid. Elo deltas earned during
		// the failed-reset window are accepted as lost on this admin path.
		e.mu.Lock()
		for bot, previous := range backups {
			previous.restore(bot)
		}
		e.skipLeaderboardRound = previousSkippedRound
		e.mu.Unlock()
		return fmt.Errorf("reset leaderboard: %w", err)
	}

	// Only after a successful TRUNCATE: discarding pending deltas or bumping
	// the epoch before knowing the outcome would throw away stat deltas the
	// DB still holds.
	pendingBotStatsDeltas = make(map[string]db.BotStatsDelta)
	botStatsPersistenceEpoch.Add(1)
	return nil
}

// leaderboardResetBackup holds exactly the fields resetBotLeaderboardState
// mutates, so a failed DB reset can restore them without reverting live
// gameplay state that advanced while the engine lock was released.
type leaderboardResetBackup struct {
	elo                  int
	persistedKills       int
	persistedDeaths      int
	persistedDamageDealt float64
	persistedDamageTaken float64
	persistedDistance    float64
	persistedPickups     int
	rebased              bool
	killBaseline         int
	lifeBaseline         int
}

func captureLeaderboardResetBackup(bot *BotState) leaderboardResetBackup {
	return leaderboardResetBackup{
		elo:                  bot.Elo,
		persistedKills:       bot.PersistedKills,
		persistedDeaths:      bot.PersistedDeaths,
		persistedDamageDealt: bot.PersistedDamageDealt,
		persistedDamageTaken: bot.PersistedDamageTaken,
		persistedDistance:    bot.PersistedDistance,
		persistedPickups:     bot.PersistedPickups,
		rebased:              bot.LeaderboardRebased,
		killBaseline:         bot.LeaderboardKillBaseline,
		lifeBaseline:         bot.LeaderboardLifeBaseline,
	}
}

func (b leaderboardResetBackup) restore(bot *BotState) {
	bot.Elo = b.elo
	bot.PersistedKills = b.persistedKills
	bot.PersistedDeaths = b.persistedDeaths
	bot.PersistedDamageDealt = b.persistedDamageDealt
	bot.PersistedDamageTaken = b.persistedDamageTaken
	bot.PersistedDistance = b.persistedDistance
	bot.PersistedPickups = b.persistedPickups
	bot.LeaderboardRebased = b.rebased
	bot.LeaderboardKillBaseline = b.killBaseline
	bot.LeaderboardLifeBaseline = b.lifeBaseline
}

func resetBotLeaderboardState(bot *BotState) {
	bot.Elo = ClampElo(config.StartingElo())
	// Preserve active-match score and mechanics. Reserving the current
	// cumulative counters makes the next snapshot start at the reset boundary
	// instead of repopulating the newly-truncated leaderboard with old data.
	bot.PersistedKills = bot.RoundKills
	bot.PersistedDeaths = bot.RoundDeaths
	bot.PersistedDamageDealt = bot.RoundDamageDealt
	bot.PersistedDamageTaken = bot.RoundDamageTaken
	bot.PersistedDistance = bot.RoundDistance
	bot.PersistedPickups = bot.RoundPickups
	bot.LeaderboardRebased = true
	bot.LeaderboardKillBaseline = bot.KillStreak
	bot.LeaderboardLifeBaseline = bot.RoundLongestLife
}

// KickBot disconnects a bot by ID. Returns true if found.
func (e *GameEngine) KickBot(botID, reason string) bool {
	e.mu.Lock()
	bot, ok := e.Bots[botID]
	if !ok {
		bot, ok = e.WaitingBots[botID]
	}
	if !ok {
		e.mu.Unlock()
		return false
	}
	delete(e.Bots, botID)
	delete(e.WaitingBots, botID)
	e.Grid.Remove(botID)
	statsSnapshot := takeBotStatsSnapshot(bot)
	closeReason := boundedWebSocketCloseReason(reason)
	bot.SignalTransportClose(BotTransportCloseCause{
		Source: "server_kick", CloseCode: websocket.ClosePolicyViolation, CloseReason: closeReason,
	})
	e.mu.Unlock()

	SendKick(bot, reason)
	if bot.Conn != nil {
		_ = bot.Conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, closeReason),
			time.Now().Add(time.Second),
		)
		_ = bot.Conn.Close()
	}
	safeGo(func() { PersistSingleBot(context.Background(), statsSnapshot) })
	slog.Info("admin kicked bot", "bot_id", botID, "name", bot.Name, "reason", reason)
	return true
}

// BanKey adds an API key ID to the ban list.
func (e *GameEngine) BanKey(apiKeyID string) {
	e.bannedKeysMu.Lock()
	defer e.bannedKeysMu.Unlock()
	e.bannedKeys[apiKeyID] = true
	slog.Info("admin banned key", "api_key_id", apiKeyID)
}

// IsKeyBanned checks if an API key ID is banned.
func (e *GameEngine) IsKeyBanned(apiKeyID string) bool {
	e.bannedKeysMu.RLock()
	defer e.bannedKeysMu.RUnlock()
	return e.bannedKeys[apiKeyID]
}

// GetBotProfile returns detailed behavioral data for profiling a bot.
func (e *GameEngine) GetBotProfile(botID string) (map[string]interface{}, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	bot, ok := e.Bots[botID]
	if !ok {
		bot, ok = e.WaitingBots[botID]
	}
	if !ok {
		return nil, false
	}

	// Compute distances to all other bots for positioning analysis
	var avgEnemyDist float64
	var closestEnemyDist float64 = 99999
	var closestEnemyName string
	enemyCount := 0
	for _, other := range e.Bots {
		if other.BotID == botID || !other.IsAlive {
			continue
		}
		d := bot.Position.DistanceTo(other.Position)
		avgEnemyDist += d
		enemyCount++
		if d < closestEnemyDist {
			closestEnemyDist = d
			closestEnemyName = other.Name
		}
	}
	if enemyCount > 0 {
		avgEnemyDist /= float64(enemyCount)
	}

	// Zone positioning
	var distToZoneCenter float64
	var inZone bool
	if e.Arena != nil {
		distToZoneCenter = bot.Position.DistanceTo(e.Arena.ZoneCenter)
		inZone = distToZoneCenter <= e.Arena.ZoneRadius
	}

	// Compute accuracy
	var accuracy float64
	if bot.RoundShotsFired > 0 {
		accuracy = float64(bot.RoundShotsHit) / float64(bot.RoundShotsFired) * 100
	}

	// Damage per kill
	var dmgPerKill float64
	if bot.RoundKills > 0 {
		dmgPerKill = bot.RoundDamageDealt / float64(bot.RoundKills)
	}

	// Current action
	var currentAction string
	var actionTarget string
	if bot.PendingAction != nil {
		currentAction = string(bot.PendingAction.Type)
		actionTarget = bot.PendingAction.TargetID
	}

	// Ticks alive this life
	ticksAlive := 0
	if bot.IsAlive && bot.RoundLifeStartTick > 0 {
		ticksAlive = e.TickCount - bot.RoundLifeStartTick
	}

	return map[string]interface{}{
		"bot_id":              bot.BotID,
		"name":                bot.Name,
		"weapon":              bot.Weapon,
		"avatar_color":        bot.AvatarColor,
		"stats":               bot.Stats,
		"elo":                 bot.Elo,
		"fallback_behavior":   bot.FallbackBehavior,
		"is_alive":            bot.IsAlive,
		"hp":                  round1(bot.HP),
		"max_hp":              round1(bot.MaxHP),
		"position":            bot.Position,
		"speed":               round1(bot.Speed),
		"frozen":              bot.Frozen,
		"current_action":      currentAction,
		"action_target":       actionTarget,
		"cooldown_remaining":  round1(bot.CooldownRemaining),
		"dodge_cooldown":      bot.DodgeCooldown,
		"invuln_ticks":        bot.InvulnTicks,
		"stun_ticks":          bot.StunTicks,
		"shield_absorb":       round1(bot.ShieldAbsorb),
		"active_effects":      append([]Effect(nil), bot.ActiveEffects...),
		"kill_streak":         bot.KillStreak,
		"round_kills":         bot.RoundKills,
		"round_deaths":        bot.RoundDeaths,
		"round_damage_dealt":  round1(bot.RoundDamageDealt),
		"round_damage_taken":  round1(bot.RoundDamageTaken),
		"round_shots_fired":   bot.RoundShotsFired,
		"round_shots_hit":     bot.RoundShotsHit,
		"round_distance":      round1(bot.RoundDistance),
		"round_pickups":       bot.RoundPickups,
		"accuracy":            round1(accuracy),
		"damage_per_kill":     round1(dmgPerKill),
		"avg_enemy_distance":  round1(avgEnemyDist),
		"closest_enemy_dist":  round1(closestEnemyDist),
		"closest_enemy_name":  closestEnemyName,
		"dist_to_zone_center": round1(distToZoneCenter),
		"in_zone":             inZone,
		"ticks_alive":         ticksAlive,
		"attack_multiplier":   round1(bot.AttackMultiplier),
		"defense_reduction":   round1(bot.DefenseReduction),
		"last_action_tick":    bot.LastActionTick,
		"last_damaged_by":     bot.LastDamagedBy,
	}, true
}

// BanIP adds an IP to the ban list.
func (e *GameEngine) BanIP(ip string) {
	e.bannedIPsMu.Lock()
	defer e.bannedIPsMu.Unlock()
	e.bannedIPs[ip] = true
	slog.Info("admin banned IP", "ip", ip)
}

// UnbanIP removes an IP from the ban list.
func (e *GameEngine) UnbanIP(ip string) {
	e.bannedIPsMu.Lock()
	defer e.bannedIPsMu.Unlock()
	delete(e.bannedIPs, ip)
	slog.Info("admin unbanned IP", "ip", ip)
}

// IsIPBanned checks if an IP is banned.
func (e *GameEngine) IsIPBanned(ip string) bool {
	e.bannedIPsMu.RLock()
	defer e.bannedIPsMu.RUnlock()
	return e.bannedIPs[ip]
}

// GetBannedIPs returns the list of banned IPs.
func (e *GameEngine) GetBannedIPs() []string {
	e.bannedIPsMu.RLock()
	defer e.bannedIPsMu.RUnlock()
	ips := make([]string, 0, len(e.bannedIPs))
	for ip := range e.bannedIPs {
		ips = append(ips, ip)
	}
	return ips
}

// FreezeBot sets a bot to frozen state. Returns true if found.
func (e *GameEngine) FreezeBot(botID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if bot, ok := e.Bots[botID]; ok {
		bot.Frozen = true
		slog.Info("admin froze bot", "bot_id", botID, "name", bot.Name)
		return true
	}
	if bot, ok := e.WaitingBots[botID]; ok {
		bot.Frozen = true
		return true
	}
	return false
}

// UnfreezeBot unfreezes a bot. Returns true if found.
func (e *GameEngine) UnfreezeBot(botID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if bot, ok := e.Bots[botID]; ok {
		bot.Frozen = false
		slog.Info("admin unfroze bot", "bot_id", botID, "name", bot.Name)
		return true
	}
	if bot, ok := e.WaitingBots[botID]; ok {
		bot.Frozen = false
		return true
	}
	return false
}

// KillBot sets a bot's HP to 0 (admin kill). Returns true if found and alive.
func (e *GameEngine) KillBot(botID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	bot, ok := e.Bots[botID]
	if !ok || !bot.IsAlive {
		return false
	}
	bot.HP = 0
	slog.Info("admin killed bot", "bot_id", botID, "name", bot.Name)
	return true
}

// TeleportBot moves a bot to the specified coordinates. Returns true if found.
func (e *GameEngine) TeleportBot(botID string, x, y float64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	bot, ok := e.Bots[botID]
	if !ok {
		return false
	}

	newPos := NewVec2(x, y)
	// Validate destination is not inside a wall.
	if ActiveTerrain != nil {
		cell := ActiveTerrain.WorldToGrid(newPos)
		if ActiveTerrain.IsBlocked(cell[0], cell[1]) {
			slog.Warn("admin teleport blocked: destination is a wall cell", "bot_id", botID, "x", x, "y", y)
			return false
		}
	}

	e.Grid.Remove(botID)
	bot.Position = newPos
	bot.LastValidPosition = newPos
	e.Grid.Insert(botID, bot.Position)
	slog.Info("admin teleported bot", "bot_id", botID, "name", bot.Name, "x", x, "y", y)
	return true
}

// HealBot restores HP to a bot. Returns true if found.
func (e *GameEngine) HealBot(botID string, hp float64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	bot, ok := e.Bots[botID]
	if !ok {
		return false
	}

	bot.HP += hp
	if bot.HP > bot.MaxHP {
		bot.HP = bot.MaxHP
	}
	if !bot.IsAlive && bot.HP > 0 {
		bot.IsAlive = true
	}
	slog.Info("admin healed bot", "bot_id", botID, "name", bot.Name, "hp", hp)
	return true
}

// ForceRestartRound ends the current round and starts a new one.
func (e *GameEngine) ForceRestartRound() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.Round.Phase == PhaseActive {
		e.endRound()
	}
	// Skip intermission, go straight to lobby.
	e.Round.Phase = PhaseLobby
	e.Round.IntermissionTicks = 0
	e.Round.LobbyCountdownTicks = 0
	slog.Info("admin forced round restart")
}

// GetFullGameState returns a detailed snapshot of the entire game state for admin inspection.
func (e *GameEngine) GetFullGameState() map[string]interface{} {
	e.mu.RLock()
	defer e.mu.RUnlock()

	bots := make([]map[string]interface{}, 0, len(e.Bots))
	for _, bot := range e.Bots {
		bots = append(bots, map[string]interface{}{
			"bot_id":      bot.BotID,
			"name":        bot.Name,
			"hp":          round1(bot.HP),
			"max_hp":      round1(bot.MaxHP),
			"position":    bot.Position,
			"weapon":      bot.Weapon,
			"is_alive":    bot.IsAlive,
			"kills":       bot.RoundKills,
			"deaths":      bot.RoundDeaths,
			"elo":         bot.Elo,
			"kill_streak": bot.KillStreak,
			// Snapshot: the tick goroutine mutates ActiveEffects in place
			// (TickEffects decrements/compacts) while HTTP handlers marshal
			// this map after the RLock is released.
			"effects": append([]Effect(nil), bot.ActiveEffects...),
			"stats":   bot.Stats,
			"speed":   round1(bot.Speed),
		})
	}

	phase := "lobby"
	switch e.Round.Phase {
	case PhaseActive:
		phase = "active"
	case PhaseIntermission:
		phase = "intermission"
	}

	ticksElapsed := 0
	if e.Round.Phase == PhaseActive {
		ticksElapsed = e.TickCount - e.Round.StartTick
	}

	return map[string]interface{}{
		"tick":          e.TickCount,
		"paused":        e.Paused,
		"round_number":  e.Round.RoundNumber,
		"round_phase":   phase,
		"round_id":      e.Round.RoundID,
		"ticks_elapsed": ticksElapsed,
		"bots_active":   len(e.Bots),
		"bots_waiting":  len(e.WaitingBots),
		"bots":          bots,
		// Dynamic arena sizing can change dimensions between rounds; admin
		// minimaps should rescale from this instead of assuming a fixed size.
		"arena_size": [2]float64{config.C.ArenaWidth, config.C.ArenaHeight},
		"game_mode":  string(e.Round.Mode),
		"map_shape":  string(ActiveMapShape),
		"pickups":    len(e.Pickups),
		// Snapshot: pickup collection shifts e.Pickups elements in place on
		// the tick goroutine; handlers marshal outside the lock.
		"pickups_list": append([]Pickup(nil), e.Pickups...),
		"projectiles":  len(e.Projectiles),
		"zone": map[string]interface{}{
			"center":        e.Arena.ZoneCenter,
			"radius":        round1(e.Arena.ZoneRadius),
			"target_center": e.Arena.ZoneTargetCenter,
			"target_radius": round1(e.Arena.ZoneTargetRadius),
		},
	}
}

// GetBotDetail returns detailed info about a single bot for admin inspection.
func (e *GameEngine) GetBotDetail(botID string) (map[string]interface{}, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	bot, ok := e.Bots[botID]
	if !ok {
		bot, ok = e.WaitingBots[botID]
	}
	if !ok {
		return nil, false
	}

	connInfo := map[string]interface{}{
		"has_conn":      bot.Conn != nil,
		"has_send_chan": bot.SendChan != nil,
	}
	if bot.Conn != nil {
		connInfo["remote_addr"] = bot.Conn.RemoteAddr().String()
	}

	// Copy the reference-typed fields while the RLock is held: the caller
	// json-marshals the returned map with no lock, racing the tick goroutine
	// otherwise. ActionResult is a flat value struct; Action carries one
	// pointer field that must be duplicated too.
	var lastAction interface{}
	if bot.LastActionResult != nil {
		la := *bot.LastActionResult
		lastAction = &la
	}
	var pendingAction *Action
	if bot.PendingAction != nil {
		pa := *bot.PendingAction
		if pa.TargetPosition != nil {
			tp := *pa.TargetPosition
			pa.TargetPosition = &tp
		}
		pendingAction = &pa
	}

	return map[string]interface{}{
		"bot_id":             bot.BotID,
		"api_key_id":         bot.APIKeyID,
		"name":               bot.Name,
		"avatar_color":       bot.AvatarColor,
		"position":           bot.Position,
		"hp":                 round1(bot.HP),
		"max_hp":             round1(bot.MaxHP),
		"speed":              round1(bot.Speed),
		"weapon":             bot.Weapon,
		"is_alive":           bot.IsAlive,
		"elo":                bot.Elo,
		"stats":              bot.Stats,
		"fallback_behavior":  bot.FallbackBehavior,
		"kill_streak":        bot.KillStreak,
		"attack_multiplier":  round1(bot.AttackMultiplier),
		"defense_reduction":  round1(bot.DefenseReduction),
		"cooldown_remaining": round1(bot.CooldownRemaining),
		"dodge_cooldown":     bot.DodgeCooldown,
		"invuln_ticks":       bot.InvulnTicks,
		"stun_ticks":         bot.StunTicks,
		"frozen":             bot.Frozen,
		"shield_absorb":      round1(bot.ShieldAbsorb),
		"active_effects":     append([]Effect(nil), bot.ActiveEffects...),
		"round_kills":        bot.RoundKills,
		"round_deaths":       bot.RoundDeaths,
		"round_damage_dealt": round1(bot.RoundDamageDealt),
		"round_damage_taken": round1(bot.RoundDamageTaken),
		"round_distance":     round1(bot.RoundDistance),
		"round_shots_fired":  bot.RoundShotsFired,
		"round_shots_hit":    bot.RoundShotsHit,
		"round_pickups":      bot.RoundPickups,
		"last_action_tick":   bot.LastActionTick,
		"last_action_result": lastAction,
		"current_action":     pendingAction,
		"connected_at":       bot.ConnectedAt,
		"action_counts":      botActionCounts(bot),
		"connection":         connInfo,
	}, true
}

// botActionCounts returns a map of action type → count from the bot's action history.
func botActionCounts(bot *BotState) map[string]int {
	counts := make(map[string]int)
	for _, a := range bot.ActionHistory {
		counts[string(a)]++
	}
	return counts
}

// ListAllBots returns summary info for all connected bots.
func (e *GameEngine) ListAllBots() []map[string]interface{} {
	e.mu.RLock()
	defer e.mu.RUnlock()

	result := make([]map[string]interface{}, 0, len(e.Bots)+len(e.WaitingBots))
	for _, bot := range e.Bots {
		entry := map[string]interface{}{
			"bot_id":       bot.BotID,
			"api_key_id":   bot.APIKeyID,
			"name":         bot.Name,
			"hp":           round1(bot.HP),
			"is_alive":     bot.IsAlive,
			"weapon":       bot.Weapon,
			"elo":          bot.Elo,
			"kills":        bot.RoundKills,
			"deaths":       bot.RoundDeaths,
			"status":       "active",
			"connected":    bot.Conn != nil,
			"connected_at": bot.ConnectedAt,
		}
		result = append(result, entry)
	}
	for _, bot := range e.WaitingBots {
		entry := map[string]interface{}{
			"bot_id":       bot.BotID,
			"api_key_id":   bot.APIKeyID,
			"name":         bot.Name,
			"hp":           round1(bot.HP),
			"is_alive":     bot.IsAlive,
			"weapon":       bot.Weapon,
			"elo":          bot.Elo,
			"kills":        bot.RoundKills,
			"deaths":       bot.RoundDeaths,
			"status":       "waiting",
			"connected":    bot.Conn != nil,
			"connected_at": bot.ConnectedAt,
		}
		result = append(result, entry)
	}
	return result
}

// ListConnections returns info about all WebSocket connections (bots + spectators).
func (e *GameEngine) ListConnections() map[string]interface{} {
	e.mu.RLock()
	botConns := make([]map[string]interface{}, 0)
	for _, bot := range e.Bots {
		entry := map[string]interface{}{
			"bot_id": bot.BotID,
			"name":   bot.Name,
			"type":   "bot",
		}
		if bot.Conn != nil {
			entry["remote_addr"] = bot.Conn.RemoteAddr().String()
		}
		botConns = append(botConns, entry)
	}
	for _, bot := range e.WaitingBots {
		entry := map[string]interface{}{
			"bot_id": bot.BotID,
			"name":   bot.Name,
			"type":   "bot_waiting",
		}
		if bot.Conn != nil {
			entry["remote_addr"] = bot.Conn.RemoteAddr().String()
		}
		botConns = append(botConns, entry)
	}
	e.mu.RUnlock()

	e.spectatorsMu.RLock()
	specConns := make([]map[string]interface{}, 0, len(e.Spectators))
	for i, s := range e.Spectators {
		entry := map[string]interface{}{
			"index": i,
			"type":  "spectator",
		}
		if s.Conn != nil {
			entry["remote_addr"] = s.Conn.RemoteAddr().String()
		}
		specConns = append(specConns, entry)
	}
	e.spectatorsMu.RUnlock()

	return map[string]interface{}{
		"bot_connections":       botConns,
		"spectator_connections": specConns,
		"total_bots":            len(botConns),
		"total_spectators":      len(specConns),
	}
}

// BotStatsSnapshot holds a copy of the stats fields needed for persistence,
// avoiding concurrent reads on the live BotState pointers.
type BotStatsSnapshot struct {
	BotID            string
	Elo              int
	KillStreak       int
	BestStreak       int
	KillsDelta       int
	DeathsDelta      int
	DamageDealtDelta int64
	DamageTakenDelta int64
	DistanceDelta    float64
	RoundLongestLife int
	PickupsDelta     int
	TickRate         int
	CapturedAt       time.Time
	PersistenceEpoch uint64
}

// copyBotsForPersist returns a deep copy of bots safe for goroutine use.
// BotState contains slices (ActiveEffects, HitsReceived, etc.) that would
// race if only the pointer were copied.
func (e *GameEngine) copyBotsForPersist() map[string]*BotState {
	cp := make(map[string]*BotState, len(e.Bots))
	for id, bot := range e.Bots {
		b := *bot // value copy of the struct
		// Deep-copy slices to avoid data races.
		if bot.ActiveEffects != nil {
			b.ActiveEffects = make([]Effect, len(bot.ActiveEffects))
			copy(b.ActiveEffects, bot.ActiveEffects)
		}
		if bot.HitsReceived != nil {
			b.HitsReceived = make([]HitRecord, len(bot.HitsReceived))
			copy(b.HitsReceived, bot.HitsReceived)
		}
		if bot.CurrentPath != nil {
			b.CurrentPath = make([]Vec2, len(bot.CurrentPath))
			copy(b.CurrentPath, bot.CurrentPath)
		}
		cp[id] = &b
	}
	return cp
}

// takeBotStatsSnapshot reserves the delta represented by this snapshot before
// a persistence goroutine starts. The caller must ensure the bot is not being
// mutated concurrently (the engine methods call it while holding e.mu).
func takeBotStatsSnapshot(bot *BotState) BotStatsSnapshot {
	currentStreak := bot.KillStreak
	bestStreak := bot.BestKillStreak
	longestLife := bot.RoundLongestLife
	if bot.LeaderboardRebased {
		// Arena rounds currently have one life per bot, so KillStreak grows
		// monotonically for the rest of the straddling round. Rebasing it gives
		// both current and best post-reset streaks without mutating live combat
		// state. ResetRoundStats removes the rebase at the next round boundary.
		currentStreak = max(0, bot.KillStreak-bot.LeaderboardKillBaseline)
		bestStreak = currentStreak
		longestLife = max(0, bot.RoundLongestLife-bot.LeaderboardLifeBaseline)
	}
	snapshot := BotStatsSnapshot{
		BotID:            bot.BotID,
		Elo:              ClampElo(bot.Elo),
		KillStreak:       currentStreak,
		BestStreak:       bestStreak,
		KillsDelta:       max(0, bot.RoundKills-bot.PersistedKills),
		DeathsDelta:      max(0, bot.RoundDeaths-bot.PersistedDeaths),
		DamageDealtDelta: max(0, int64(bot.RoundDamageDealt)-int64(bot.PersistedDamageDealt)),
		DamageTakenDelta: max(0, int64(bot.RoundDamageTaken)-int64(bot.PersistedDamageTaken)),
		DistanceDelta:    math.Max(0, bot.RoundDistance-bot.PersistedDistance),
		RoundLongestLife: longestLife,
		PickupsDelta:     max(0, bot.RoundPickups-bot.PersistedPickups),
		TickRate:         max(1, config.C.TickRate),
		CapturedAt:       time.Now(),
		PersistenceEpoch: botStatsPersistenceEpoch.Load(),
	}

	bot.PersistedKills = bot.RoundKills
	bot.PersistedDeaths = bot.RoundDeaths
	bot.PersistedDamageDealt = bot.RoundDamageDealt
	bot.PersistedDamageTaken = bot.RoundDamageTaken
	bot.PersistedDistance = bot.RoundDistance
	bot.PersistedPickups = bot.RoundPickups
	return snapshot
}

// snapshotBotStats captures disjoint deltas for background persistence.
func (e *GameEngine) snapshotBotStats() []BotStatsSnapshot {
	snaps := make([]BotStatsSnapshot, 0, len(e.Bots))
	for _, bot := range e.Bots {
		snaps = append(snaps, takeBotStatsSnapshot(bot))
	}
	return snaps
}
