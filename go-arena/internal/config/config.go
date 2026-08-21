package config

import (
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/mail"
	"net/url"
	"os"
	"strings"

	"github.com/kelseyhightower/envconfig"
)

const (
	DefaultShoveRangeTiles         = 1.0
	DefaultShoveKnockbackTiles     = 2.0
	DefaultTerrainMoveCellsPerTick = 0.35
	MaxTerrainMoveCellsPerTick     = 2.0
	DefaultAFKTimeoutSeconds       = 30
	MinimumAFKTimeoutSeconds       = 10
)

type Config struct {
	// Database
	DBHost     string `envconfig:"ARENA_DB_HOST" default:"localhost"`
	DBPort     int    `envconfig:"ARENA_DB_PORT" default:"5432"`
	DBName     string `envconfig:"ARENA_DB_NAME" default:"arena"`
	DBUser     string `envconfig:"ARENA_DB_USER" default:"arena"`
	DBPassword string `envconfig:"ARENA_DB_PASSWORD" default:"arena"`
	// DBRuntimeUser is the least-privilege application role that an owner-run
	// `arena-server migrate` command grants access to. It is normally supplied
	// only to the one-shot migration container.
	DBRuntimeUser string `envconfig:"ARENA_RUNTIME_DB_USER" default:""`

	// Redis
	RedisHost string `envconfig:"ARENA_REDIS_HOST" default:"localhost"`
	RedisPort int    `envconfig:"ARENA_REDIS_PORT" default:"6379"`

	// Server
	ServerHost string `envconfig:"ARENA_HOST" default:"0.0.0.0"`
	ServerPort int    `envconfig:"ARENA_PORT" default:"8000"`

	// Game
	TickRate            int     `envconfig:"ARENA_TICK_RATE" default:"10"`
	MaxBots             int     `envconfig:"ARENA_MAX_BOTS" default:"500"`
	MaxSpectators       int     `envconfig:"ARENA_MAX_SPECTATORS" default:"500"`
	ArenaWidth          float64 `envconfig:"ARENA_WIDTH" default:"2000"`
	ArenaHeight         float64 `envconfig:"ARENA_HEIGHT" default:"2000"`
	BotRadius           float64 `envconfig:"ARENA_BOT_RADIUS" default:"5.0"`
	SpatialCellSize     float64 `envconfig:"ARENA_SPATIAL_CELL_SIZE" default:"100"`
	PathfindingCellSize float64 `envconfig:"ARENA_PATHFINDING_CELL_SIZE" default:"20"`
	FogRadius           int     `envconfig:"ARENA_FOG_RADIUS" default:"7"`

	// Game modes (groundwork). "ffa" (default), "team_battle", "ctf".
	GameModeName        string  `envconfig:"ARENA_GAME_MODE" default:"ffa"`
	TeamCount           int     `envconfig:"ARENA_TEAM_COUNT" default:"2"`
	FriendlyFire        bool    `envconfig:"ARENA_FRIENDLY_FIRE" default:"false"`
	CTFCapturesToWin    int     `envconfig:"ARENA_CTF_CAPTURES_TO_WIN" default:"3"`
	CTFFlagPickupRadius float64 `envconfig:"ARENA_CTF_FLAG_PICKUP_RADIUS" default:"25"`
	CTFFlagReturnSecs   float64 `envconfig:"ARENA_CTF_FLAG_RETURN_SECS" default:"20"`

	// Map generation. "square" (classic), "circle", "hexagon", "diamond",
	// "cross", "caves", "star", "hourglass", "clover", or "random" to roll a shape each round.
	MapShape     string `envconfig:"ARENA_MAP_SHAPE" default:"random"`
	MapShapePool string `envconfig:"ARENA_MAP_SHAPE_POOL" default:"square,circle,hexagon,diamond,cross,caves,donut,islands,rooms,spiral,star,hourglass,clover"`

	// Dynamic arena size: the map grows with the number of bots joining the
	// round, from the base ARENA_WIDTH/HEIGHT at <= ARENA_SIZE_BASE_BOTS up
	// to ARENA_SIZE_MAX_SCALE times the base dimensions at
	// >= ARENA_SIZE_MAX_BOTS. Applied when each round's terrain is generated.
	ArenaSizeDynamic  bool    `envconfig:"ARENA_SIZE_DYNAMIC" default:"true"`
	ArenaSizeBaseBots int     `envconfig:"ARENA_SIZE_BASE_BOTS" default:"12"`
	ArenaSizeMaxBots  int     `envconfig:"ARENA_SIZE_MAX_BOTS" default:"48"`
	ArenaSizeMaxScale float64 `envconfig:"ARENA_SIZE_MAX_SCALE" default:"2.0"`
	// Below ARENA_SIZE_BASE_BOTS the map shrinks linearly down to
	// ARENA_SIZE_MIN_SCALE times the base dimensions at 2 bots (a duel plays
	// on a small arena instead of the full map; GitHub issue #12). Set to 1.0
	// to disable shrinking and keep the pre-delta scale-up-only behavior.
	ArenaSizeMinScale float64 `envconfig:"ARENA_SIZE_MIN_SCALE" default:"0.6"`

	// Spectator keyframes: static round data (obstacles, map shape) is only
	// included every Nth broadcast plus immediately after a spectator joins.
	// 1 = include everything every tick (legacy behaviour).
	SpectatorKeyframeInterval int `envconfig:"ARENA_SPECTATOR_KEYFRAME_INTERVAL" default:"10"`

	// Combat
	StatBudget       int     `envconfig:"ARENA_STAT_BUDGET" default:"20"`
	StatMin          int     `envconfig:"ARENA_STAT_MIN" default:"1"`
	StatMax          int     `envconfig:"ARENA_STAT_MAX" default:"10"`
	RoundDuration    float64 `envconfig:"ARENA_ROUND_DURATION" default:"300"`
	IntermissionTime float64 `envconfig:"ARENA_INTERMISSION_TIME" default:"10"`
	LobbyCountdown   float64 `envconfig:"ARENA_LOBBY_COUNTDOWN" default:"10"`
	MinBotsToStart   int     `envconfig:"ARENA_MIN_BOTS_TO_START" default:"2"`

	// Stat multipliers (for live balance tuning)
	StatHPBase              float64 `envconfig:"ARENA_STAT_HP_BASE" default:"100"`
	StatHPPerPoint          float64 `envconfig:"ARENA_STAT_HP_PER_POINT" default:"10"`
	StatSpeedBase           float64 `envconfig:"ARENA_STAT_SPEED_BASE" default:"3.0"`
	StatSpeedPerPoint       float64 `envconfig:"ARENA_STAT_SPEED_PER_POINT" default:"0.5"`
	TerrainMoveCellsPerTick float64 `envconfig:"ARENA_TERRAIN_MOVE_CELLS_PER_TICK" default:"0.35"`
	StatAttackBase          float64 `envconfig:"ARENA_STAT_ATTACK_BASE" default:"1.0"`
	StatAttackPerPoint      float64 `envconfig:"ARENA_STAT_ATTACK_PER_POINT" default:"0.1"`
	StatDefensePerPoint     float64 `envconfig:"ARENA_STAT_DEFENSE_PER_POINT" default:"0.03"`

	// Dodge
	DodgeSpeedMult     float64 `envconfig:"ARENA_DODGE_SPEED_MULT" default:"2.0"`
	DodgeInvulnTicks   int     `envconfig:"ARENA_DODGE_INVULN_TICKS" default:"3"`
	DodgeCooldownTicks int     `envconfig:"ARENA_DODGE_COOLDOWN_TICKS" default:"30"`

	// Knockback
	KnockbackWallDamage float64 `envconfig:"ARENA_KNOCKBACK_WALL_DAMAGE" default:"5"`

	// Projectiles
	ProjectileSpeed      float64 `envconfig:"ARENA_PROJECTILE_SPEED" default:"240.0"`
	ProjectileHitRadius  float64 `envconfig:"ARENA_PROJECTILE_HIT_RADIUS" default:"1.0"`
	ProjectileMaxAgeSecs float64 `envconfig:"ARENA_PROJECTILE_MAX_AGE_SECS" default:"1.0"`

	// Staff
	StaffDelayTicks            int     `envconfig:"ARENA_STAFF_DELAY_TICKS" default:"3"`
	StaffBurnFieldTicks        int     `envconfig:"ARENA_STAFF_BURN_FIELD_TICKS" default:"12"`
	StaffBurnFieldRadius       int     `envconfig:"ARENA_STAFF_BURN_FIELD_RADIUS" default:"1"`
	StaffBurnFieldTickInterval int     `envconfig:"ARENA_STAFF_BURN_FIELD_TICK_INTERVAL" default:"2"`
	StaffBurnFieldDamage       float64 `envconfig:"ARENA_STAFF_BURN_FIELD_DAMAGE" default:"3"`
	StunDurationTicks          int     `envconfig:"ARENA_STUN_DURATION_TICKS" default:"1"`

	// Weapon signatures
	ShieldBashBonusMultiplier     float64 `envconfig:"ARENA_SHIELD_BASH_BONUS_MULT" default:"1.35"`
	ShieldDisruptWindowTicks      int     `envconfig:"ARENA_SHIELD_DISRUPT_WINDOW_TICKS" default:"10"`
	DaggerBackstabBonusMultiplier float64 `envconfig:"ARENA_DAGGER_BACKSTAB_BONUS_MULT" default:"1.45"`
	DaggerBackstabDotThreshold    float64 `envconfig:"ARENA_DAGGER_BACKSTAB_DOT_THRESHOLD" default:"-0.35"`
	SpearBraceStillTicks          int     `envconfig:"ARENA_SPEAR_BRACE_STILL_TICKS" default:"2"`
	SpearBraceBonusMultiplier     float64 `envconfig:"ARENA_SPEAR_BRACE_BONUS_MULT" default:"1.35"`
	SpearBraceBonusKnockback      int     `envconfig:"ARENA_SPEAR_BRACE_BONUS_KNOCKBACK" default:"1"`
	BowChargeMaxTicks             int     `envconfig:"ARENA_BOW_CHARGE_MAX_TICKS" default:"6"`
	BowChargeReadyTicks           int     `envconfig:"ARENA_BOW_CHARGE_READY_TICKS" default:"2"`
	BowChargeDamagePerTick        float64 `envconfig:"ARENA_BOW_CHARGE_DAMAGE_PER_TICK" default:"0.12"`
	BowChargeSpeedPerTick         float64 `envconfig:"ARENA_BOW_CHARGE_SPEED_PER_TICK" default:"0.08"`
	BowChargeCooldownPerTick      float64 `envconfig:"ARENA_BOW_CHARGE_COOLDOWN_PER_TICK" default:"0.06"`
	GrappleSlamMinRange           int     `envconfig:"ARENA_GRAPPLE_SLAM_MIN_RANGE" default:"3"`
	GrappleSlamBonusMultiplier    float64 `envconfig:"ARENA_GRAPPLE_SLAM_BONUS_MULT" default:"1.4"`
	GrappleSlamStunTicks          int     `envconfig:"ARENA_GRAPPLE_SLAM_STUN_TICKS" default:"2"`

	// Universal Grapple Ability
	GrappleChargesPerRound     int     `envconfig:"ARENA_GRAPPLE_CHARGES_PER_ROUND" default:"2"`
	GrappleAbilityRangeTiles   int     `envconfig:"ARENA_GRAPPLE_RANGE_TILES" default:"12"`
	GrappleAbilityDamage       float64 `envconfig:"ARENA_GRAPPLE_DAMAGE" default:"15"`
	GrappleAbilityCooldownSecs float64 `envconfig:"ARENA_GRAPPLE_COOLDOWN_SECS" default:"4.0"`
	GrappleAbilityStunTicks    int     `envconfig:"ARENA_GRAPPLE_STUN_TICKS" default:"3"`

	// Shove
	ShoveRange     float64 `envconfig:"ARENA_SHOVE_RANGE" default:"1.0"`
	ShoveKnockback float64 `envconfig:"ARENA_SHOVE_KNOCKBACK" default:"2.0"`
	ShoveStunTicks int     `envconfig:"ARENA_SHOVE_STUN_TICKS" default:"2"`
	ShoveCooldown  float64 `envconfig:"ARENA_SHOVE_COOLDOWN" default:"1.5"`

	// Zone
	ZoneInitialRadius  float64 `envconfig:"ARENA_ZONE_INITIAL_RADIUS" default:"1000.0"`
	ZoneCoverMap       bool    `envconfig:"ARENA_ZONE_COVER_MAP" default:"true"`
	ZoneCenterX        float64 `envconfig:"ARENA_ZONE_CENTER_X" default:"1000.0"`
	ZoneCenterY        float64 `envconfig:"ARENA_ZONE_CENTER_Y" default:"1000.0"`
	ZoneShrinkPercent  float64 `envconfig:"ARENA_ZONE_SHRINK_PERCENT" default:"0.15"`
	ZoneShrinkInterval float64 `envconfig:"ARENA_ZONE_SHRINK_INTERVAL_SECS" default:"20"`
	ZoneDamagePerTick  float64 `envconfig:"ARENA_ZONE_DAMAGE_PER_TICK" default:"3"`
	ZoneMinRadius      float64 `envconfig:"ARENA_ZONE_MIN_RADIUS" default:"175.0"`
	ZoneShrinkDelay    float64 `envconfig:"ARENA_ZONE_SHRINK_DELAY_SECS" default:"60"`

	// Obstacles
	ObstacleCountMin int `envconfig:"ARENA_OBSTACLE_COUNT_MIN" default:"20"`
	ObstacleCountMax int `envconfig:"ARENA_OBSTACLE_COUNT_MAX" default:"30"`

	// Pickups
	PickupSpawnIntervalTicks        int     `envconfig:"ARENA_PICKUP_SPAWN_INTERVAL_TICKS" default:"50"`
	PickupMaxActive                 int     `envconfig:"ARENA_PICKUP_MAX_ACTIVE" default:"20"`
	PickupHealthAmount              float64 `envconfig:"ARENA_PICKUP_HEALTH_AMOUNT" default:"30"`
	PickupSpeedBoostMult            float64 `envconfig:"ARENA_PICKUP_SPEED_BOOST_MULT" default:"2.0"`
	PickupSpeedBoostTicks           int     `envconfig:"ARENA_PICKUP_SPEED_BOOST_TICKS" default:"50"`
	PickupDamageBoostMult           float64 `envconfig:"ARENA_PICKUP_DAMAGE_BOOST_MULT" default:"1.5"`
	PickupDamageBoostTicks          int     `envconfig:"ARENA_PICKUP_DAMAGE_BOOST_TICKS" default:"50"`
	PickupShieldBubbleHP            float64 `envconfig:"ARENA_PICKUP_SHIELD_BUBBLE_HP" default:"50"`
	PickupCooldownShardTicks        int     `envconfig:"ARENA_PICKUP_COOLDOWN_SHARD_TICKS" default:"100"`
	PickupCooldownShardMult         float64 `envconfig:"ARENA_PICKUP_COOLDOWN_SHARD_MULT" default:"0.6"`
	PickupBountyTokenPoints         int     `envconfig:"ARENA_PICKUP_BOUNTY_TOKEN_POINTS" default:"18"`
	PickupBountyTokenTicks          int     `envconfig:"ARENA_PICKUP_BOUNTY_TOKEN_TICKS" default:"90"`
	PickupHazardKeyTicks            int     `envconfig:"ARENA_PICKUP_HAZARD_KEY_TICKS" default:"80"`
	PickupOverdriveTicks            int     `envconfig:"ARENA_PICKUP_OVERDRIVE_TICKS" default:"60"`
	PickupOverdriveDamageMult       float64 `envconfig:"ARENA_PICKUP_OVERDRIVE_DAMAGE_MULT" default:"1.25"`
	PickupOverdriveCooldownMult     float64 `envconfig:"ARENA_PICKUP_OVERDRIVE_COOLDOWN_MULT" default:"0.75"`
	PickupGrappleChargeAmount       int     `envconfig:"ARENA_PICKUP_GRAPPLE_CHARGE_AMOUNT" default:"1"`
	PickupRelayBatteryTicks         int     `envconfig:"ARENA_PICKUP_RELAY_BATTERY_TICKS" default:"90"`
	PickupRelayBatteryBonusProgress int     `envconfig:"ARENA_PICKUP_RELAY_BATTERY_BONUS_PROGRESS" default:"1"`
	PickupCollectRadius             float64 `envconfig:"ARENA_PICKUP_COLLECT_RADIUS" default:"2.0"`

	// Network / persistence
	PersistIntervalSecs          float64 `envconfig:"ARENA_PERSIST_INTERVAL_SECS" default:"30"`
	KillFeedSize                 int     `envconfig:"ARENA_KILL_FEED_SIZE" default:"20"`
	WSMessageMaxBytes            int     `envconfig:"ARENA_WS_MESSAGE_MAX_BYTES" default:"1024"`
	WSMaxMessagesPerSec          int     `envconfig:"ARENA_WS_MAX_MESSAGES_PER_SEC" default:"25"`
	ConnectionTimeout            float64 `envconfig:"ARENA_CONNECTION_TIMEOUT" default:"10"`
	HeartbeatInterval            float64 `envconfig:"ARENA_HEARTBEAT_INTERVAL" default:"30"`
	WSConnectRatePerMin          int     `envconfig:"ARENA_WS_CONNECT_RATE_PER_MIN" default:"3"`
	WSSpectatorConnectRatePerMin int     `envconfig:"ARENA_WS_SPECTATOR_CONNECT_RATE_PER_MIN" default:"60"`
	LoadoutTimeoutSecs           float64 `envconfig:"ARENA_LOADOUT_TIMEOUT_SECS" default:"10"`
	WSReconnectGraceSecs         float64 `envconfig:"ARENA_WS_RECONNECT_GRACE_SECS" default:"10"`
	SpectatorBroadcastInterval   int     `envconfig:"ARENA_SPECTATOR_BROADCAST_INTERVAL" default:"1"`
	AFKTimeoutTicks              int     `envconfig:"ARENA_AFK_TIMEOUT_TICKS" default:"300"`

	// Browser error reporting. Public, unauthenticated intake for client-side
	// exceptions (bounded in-memory only, surfaced in the admin Errors tab).
	// Per-IP request budget; 0 or less disables the endpoint's limiter.
	ClientErrorReportRPM int `envconfig:"ARENA_CLIENT_ERROR_REPORT_RPM" default:"30"`

	// Log retention. Applies to log-style data only (kill_log, finished
	// rounds, audit/event tables, stale rate-limit rows) — never to
	// leaderboard, account, or payment tables.
	LogRetentionHours int `envconfig:"ARENA_LOG_RETENTION_HOURS" default:"48"`

	// Nightly restart. The server exits cleanly at the scheduled wall-clock
	// time (after warning connected clients); Docker's restart policy brings
	// it back up. Disable for standalone dev runs without a supervisor.
	NightlyRestartEnabled        bool   `envconfig:"ARENA_NIGHTLY_RESTART_ENABLED" default:"true"`
	NightlyRestartTime           string `envconfig:"ARENA_NIGHTLY_RESTART_TIME" default:"00:00"`
	NightlyRestartTZ             string `envconfig:"ARENA_NIGHTLY_RESTART_TZ" default:"Local"`
	NightlyRestartWarnMinutes    int    `envconfig:"ARENA_NIGHTLY_RESTART_WARN_MINUTES" default:"15"`
	NightlyRestartRoundGraceSecs int    `envconfig:"ARENA_NIGHTLY_RESTART_ROUND_GRACE_SECS" default:"420"`

	// Admin
	AdminKey             string `envconfig:"ARENA_ADMIN_KEY" default:"changeme_admin_key"`
	AdminToken           string `envconfig:"ARENA_ADMIN_TOKEN" default:""`
	AdminLocalhostBypass bool   `envconfig:"ARENA_ADMIN_LOCALHOST_BYPASS" default:"true"`
	AdminRateLimitRPM    int    `envconfig:"ARENA_ADMIN_RATE_LIMIT_RPM" default:"120"`

	// Cloudflare (optional — for IP ban push)
	CloudflareAPIToken string `envconfig:"ARENA_CF_API_TOKEN" default:""`
	CloudflareZoneID   string `envconfig:"ARENA_CF_ZONE_ID" default:""`

	// Self-update (optional). The Admin Panel "Update" action is only enabled
	// when both UpdaterURL and UpdaterSharedSecret are set and point at a running
	// arena-updater sidecar (see docs/build-and-deploy.md).
	UpdaterURL          string `envconfig:"ARENA_UPDATER_URL" default:""`
	UpdaterSharedSecret string `envconfig:"ARENA_UPDATER_SHARED_SECRET" default:""`
	// Optional GitHub token to raise the tarball-fetch / compare rate limit.
	// Arena is a public repo, so this is not required.
	UpdateGitHubToken string `envconfig:"ARENA_UPDATE_GITHUB_TOKEN" default:""`
	// owner/repo and branch the "update to latest" check compares the running
	// build against (production release branch by default).
	UpdateRepo   string `envconfig:"ARENA_UPDATE_REPO" default:"Angel-Software-Solutions-LLC/Arena"`
	UpdateBranch string `envconfig:"ARENA_UPDATE_BRANCH" default:"main"`

	// Demo-bot fleet control (optional). Base URL of the private fleet's
	// control API on the compose network (e.g. http://arena-demobots:9200).
	// The admin panel's Demo Bot Fleet card is disabled when unset. Requests
	// are authenticated with AdminToken, which the fleet shares.
	DemobotsControlURL string `envconfig:"ARENA_DEMOBOTS_CONTROL_URL" default:""`

	// CORS
	CORSOrigins string `envconfig:"ARENA_CORS_ORIGINS" default:"*"`

	// Security headers (HSTS, CSP, X-Frame-Options, etc). Enabled by default;
	// provided as an escape hatch in case a header ever conflicts with a
	// deployment's reverse-proxy setup.
	SecurityHeadersEnabled bool `envconfig:"ARENA_SECURITY_HEADERS_ENABLED" default:"true"`

	// DB Pool
	DBPoolSize    int `envconfig:"ARENA_DB_POOL_SIZE" default:"20"`
	DBMaxOverflow int `envconfig:"ARENA_DB_MAX_OVERFLOW" default:"10"`

	// DB connection resilience
	DBConnectAttempts     int  `envconfig:"ARENA_DB_CONNECT_ATTEMPTS" default:"10"`
	DBConnectRetrySeconds int  `envconfig:"ARENA_DB_CONNECT_RETRY_SECONDS" default:"3"`
	DBOptional            bool `envconfig:"ARENA_DB_OPTIONAL" default:"false"`
	// Managed migrations keep DDL out of the runtime role. When enabled, normal
	// server startup performs only a read-only schema preflight and refuses to
	// start on a stale schema; `arena-server migrate` still applies migrations.
	DBMigrationsManaged bool `envconfig:"ARENA_DB_MIGRATIONS_MANAGED" default:"false"`

	// Rate limiting per endpoint
	RateLimitBotConfigPerMin int `envconfig:"ARENA_RATE_LIMIT_BOT_CONFIG_PER_MIN" default:"120"`

	// Security
	APIKeyPrefix string `envconfig:"ARENA_API_KEY_PREFIX" default:"arena_"`
	// BcryptRounds controls the rollback-compatible bcrypt prefix generated once
	// per API key. Current readers verify the appended digest on the connection
	// hot path and do not pay this cost on each authentication.
	BcryptRounds                int    `envconfig:"ARENA_BCRYPT_ROUNDS" default:"12"`
	TrustedProxyCIDRs           string `envconfig:"ARENA_TRUSTED_PROXY_CIDRS" default:"127.0.0.1/32,::1/128"`
	TrustedCloudflareProxyCIDRs string `envconfig:"ARENA_TRUSTED_CLOUDFLARE_PROXY_CIDRS" default:""`
	RateLimitRegisterRPM        int    `envconfig:"ARENA_RATE_LIMIT_REGISTER_RPM" default:"10"`
	RateLimitRegisterPerHour    int    `envconfig:"ARENA_RATE_LIMIT_REGISTER_PER_HOUR" default:"500"`

	// ELO
	EloKFactor  float64 `envconfig:"ARENA_ELO_K_FACTOR" default:"32"`
	EloStarting int     `envconfig:"ARENA_ELO_STARTING" default:"1000"`
	EloMin      int     `envconfig:"ARENA_ELO_MIN" default:"100"`
	EloMax      int     `envconfig:"ARENA_ELO_MAX" default:"3000"`

	// Bot separation

	// Anti-teaming
	AntiTeamRadius         float64 `envconfig:"ARENA_ANTI_TEAM_RADIUS" default:"30.0"`
	AntiTeamThresholdTicks int     `envconfig:"ARENA_ANTI_TEAM_THRESHOLD_TICKS" default:"50"`
	AntiTeamDamagePerTick  float64 `envconfig:"ARENA_ANTI_TEAM_DAMAGE_PER_TICK" default:"2.0"`

	// Teleport Pads
	TeleportPadPairs         int `envconfig:"ARENA_TELEPORT_PAD_PAIRS" default:"3"`
	TeleportCooldownTicks    int `envconfig:"ARENA_TELEPORT_COOLDOWN_TICKS" default:"300"`
	TeleportCollectRadius    int `envconfig:"ARENA_TELEPORT_COLLECT_RADIUS" default:"0"`
	TeleportPadLockTicks     int `envconfig:"ARENA_TELEPORT_PAD_LOCK_TICKS" default:"300"`
	TeleportHazardGraceTicks int `envconfig:"ARENA_TELEPORT_HAZARD_GRACE_TICKS" default:"2"`

	// Capture Pad objective
	CapturePadCount              int     `envconfig:"ARENA_CAPTURE_PAD_COUNT" default:"1"`
	CapturePadRadius             int     `envconfig:"ARENA_CAPTURE_PAD_RADIUS" default:"2"`
	CapturePadCaptureTicks       int     `envconfig:"ARENA_CAPTURE_PAD_CAPTURE_TICKS" default:"20"`
	CapturePadCooldownTicks      int     `envconfig:"ARENA_CAPTURE_PAD_COOLDOWN_TICKS" default:"120"`
	CapturePadScoreBonus         int     `envconfig:"ARENA_CAPTURE_PAD_SCORE_BONUS" default:"12"`
	CapturePadShieldBonus        float64 `envconfig:"ARENA_CAPTURE_PAD_SHIELD_BONUS" default:"20"`
	CapturePadDamageBoostMult    float64 `envconfig:"ARENA_CAPTURE_PAD_DAMAGE_BOOST_MULT" default:"1.2"`
	CapturePadEffectTicks        int     `envconfig:"ARENA_CAPTURE_PAD_EFFECT_TICKS" default:"80"`
	CapturePadControlPulseTicks  int     `envconfig:"ARENA_CAPTURE_PAD_CONTROL_PULSE_TICKS" default:"15"`
	CapturePadControlPulseScore  int     `envconfig:"ARENA_CAPTURE_PAD_CONTROL_PULSE_SCORE" default:"2"`
	CapturePadControlPulseShield float64 `envconfig:"ARENA_CAPTURE_PAD_CONTROL_PULSE_SHIELD" default:"4"`

	// Environmental Hazards
	HazardZoneCount     int     `envconfig:"ARENA_HAZARD_ZONE_COUNT" default:"6"`
	HazardMinWidth      int     `envconfig:"ARENA_HAZARD_MIN_WIDTH" default:"2"`
	HazardMaxWidth      int     `envconfig:"ARENA_HAZARD_MAX_WIDTH" default:"4"`
	HazardDamagePerTick float64 `envconfig:"ARENA_HAZARD_DAMAGE_PER_TICK" default:"3"`
	HazardPulseOnTicks  int     `envconfig:"ARENA_HAZARD_PULSE_ON_TICKS" default:"30"`
	HazardPulseOffTicks int     `envconfig:"ARENA_HAZARD_PULSE_OFF_TICKS" default:"20"`

	// Sudden Death
	SuddenDeathTilesPerTick int     `envconfig:"ARENA_SUDDEN_DEATH_TILES_PER_TICK" default:"2"`
	SuddenDeathDamage       float64 `envconfig:"ARENA_SUDDEN_DEATH_DAMAGE" default:"999"`
	// All damage is multiplied by this factor while sudden death is active.
	SuddenDeathDamageMult float64 `envconfig:"ARENA_SUDDEN_DEATH_DAMAGE_MULT" default:"2.0"`
	// If no bot deals damage for this many seconds during sudden death, every
	// living bot starts taking stall damage each tick until combat resumes.
	SuddenDeathStallSeconds float64 `envconfig:"ARENA_SUDDEN_DEATH_STALL_SECONDS" default:"20"`
	SuddenDeathStallDamage  float64 `envconfig:"ARENA_SUDDEN_DEATH_STALL_DAMAGE_PER_TICK" default:"2"`
	// Stall damage grows by another SuddenDeathStallDamage step every this
	// many seconds of continued stalling, so no round can drag on forever.
	SuddenDeathStallRampSeconds float64 `envconfig:"ARENA_SUDDEN_DEATH_STALL_RAMP_SECONDS" default:"5"`
	// Once sudden death is active the round no longer ends on the duration
	// timer; it plays out in overtime capped at this many extra seconds.
	SuddenDeathMaxOvertime float64 `envconfig:"ARENA_SUDDEN_DEATH_MAX_OVERTIME" default:"90"`

	// Bounty System
	BountyWinStreakThreshold int `envconfig:"ARENA_BOUNTY_WIN_STREAK" default:"1"`
	BountyBoardBasePoints    int `envconfig:"ARENA_BOUNTY_BOARD_BASE_POINTS" default:"6"`
	BountyBoardStepPoints    int `envconfig:"ARENA_BOUNTY_BOARD_STEP_POINTS" default:"4"`
	BountyBoardMaxPoints     int `envconfig:"ARENA_BOUNTY_BOARD_MAX_POINTS" default:"18"`

	// Occasional special round modifiers
	RoundModifierChance                  float64 `envconfig:"ARENA_ROUND_MODIFIER_CHANCE" default:"0.30"`
	RoundModifierFastZoneDelayMult       float64 `envconfig:"ARENA_ROUND_MOD_FAST_ZONE_DELAY_MULT" default:"0.55"`
	RoundModifierFastZoneIntervalMult    float64 `envconfig:"ARENA_ROUND_MOD_FAST_ZONE_INTERVAL_MULT" default:"0.65"`
	RoundModifierPickupSurgeIntervalMult float64 `envconfig:"ARENA_ROUND_MOD_PICKUP_SURGE_INTERVAL_MULT" default:"0.50"`
	RoundModifierDoubleBountyMult        float64 `envconfig:"ARENA_ROUND_MOD_DOUBLE_BOUNTY_MULT" default:"2.0"`
	RoundModifierTeleportCooldownMult    float64 `envconfig:"ARENA_ROUND_MOD_TELEPORT_COOLDOWN_MULT" default:"0.45"`
	RoundModifierTeleportLockMult        float64 `envconfig:"ARENA_ROUND_MOD_TELEPORT_LOCK_MULT" default:"0.55"`
	RoundModifierHazardStormOnMult       float64 `envconfig:"ARENA_ROUND_MOD_HAZARD_STORM_ON_MULT" default:"1.20"`
	RoundModifierHazardStormOffMult      float64 `envconfig:"ARENA_ROUND_MOD_HAZARD_STORM_OFF_MULT" default:"0.45"`
	RoundModifierHazardStormDamageMult   float64 `envconfig:"ARENA_ROUND_MOD_HAZARD_STORM_DAMAGE_MULT" default:"1.35"`

	// Landmines
	MineMaxPerBot     int     `envconfig:"ARENA_MINE_MAX_PER_BOT" default:"3"`
	MineDamage        float64 `envconfig:"ARENA_MINE_DAMAGE" default:"40"`
	MineBlastRadius   int     `envconfig:"ARENA_MINE_BLAST_RADIUS" default:"1"`
	MineArmDelayTicks int     `envconfig:"ARENA_MINE_ARM_DELAY_TICKS" default:"10"`

	// Gravity Well
	GravityWellDurationTicks int     `envconfig:"ARENA_GRAVITY_WELL_DURATION_TICKS" default:"30"`
	GravityWellPullRadius    int     `envconfig:"ARENA_GRAVITY_WELL_PULL_RADIUS" default:"3"`
	GravityWellPullForce     float64 `envconfig:"ARENA_GRAVITY_WELL_PULL_FORCE" default:"0.5"`

	// Automatic weapon balancing
	WeaponAutoBalanceEnabled           bool    `envconfig:"ARENA_WEAPON_AUTO_BALANCE_ENABLED" default:"true"`
	WeaponAutoBalanceStartStep         float64 `envconfig:"ARENA_WEAPON_AUTO_BALANCE_START_STEP" default:"0.05"`
	WeaponAutoBalanceMinStep           float64 `envconfig:"ARENA_WEAPON_AUTO_BALANCE_MIN_STEP" default:"0.005"`
	WeaponAutoBalanceDecay             float64 `envconfig:"ARENA_WEAPON_AUTO_BALANCE_DECAY" default:"0.94"`
	WeaponAutoBalanceDeadzoneStart     float64 `envconfig:"ARENA_WEAPON_AUTO_BALANCE_DEADZONE_START" default:"0.02"`
	WeaponAutoBalanceDeadzoneMin       float64 `envconfig:"ARENA_WEAPON_AUTO_BALANCE_DEADZONE_MIN" default:"0.003"`
	WeaponAutoBalanceMinDamageScale    float64 `envconfig:"ARENA_WEAPON_AUTO_BALANCE_MIN_DAMAGE_SCALE" default:"0.70"`
	WeaponAutoBalanceMaxDamageScale    float64 `envconfig:"ARENA_WEAPON_AUTO_BALANCE_MAX_DAMAGE_SCALE" default:"1.40"`
	WeaponAutoBalanceMinCooldownScale  float64 `envconfig:"ARENA_WEAPON_AUTO_BALANCE_MIN_COOLDOWN_SCALE" default:"0.75"`
	WeaponAutoBalanceMaxCooldownScale  float64 `envconfig:"ARENA_WEAPON_AUTO_BALANCE_MAX_COOLDOWN_SCALE" default:"1.35"`
	WeaponAutoBalanceDamageWeight      float64 `envconfig:"ARENA_WEAPON_AUTO_BALANCE_DAMAGE_WEIGHT" default:"0.65"`
	WeaponAutoBalanceCooldownWeight    float64 `envconfig:"ARENA_WEAPON_AUTO_BALANCE_COOLDOWN_WEIGHT" default:"0.45"`
	WeaponAutoBalanceMinRounds         int     `envconfig:"ARENA_WEAPON_AUTO_BALANCE_MIN_ROUNDS" default:"6"`
	WeaponAutoBalanceMinBotSamples     int     `envconfig:"ARENA_WEAPON_AUTO_BALANCE_MIN_BOT_SAMPLES" default:"18"`
	WeaponAutoBalanceMinDistinctBots   int     `envconfig:"ARENA_WEAPON_AUTO_BALANCE_MIN_DISTINCT_BOTS" default:"2"`
	WeaponAutoBalanceMinActions        int     `envconfig:"ARENA_WEAPON_AUTO_BALANCE_MIN_ACTIONS" default:"5"`
	WeaponAutoBalanceConfidenceZ       float64 `envconfig:"ARENA_WEAPON_AUTO_BALANCE_CONFIDENCE_Z" default:"1.96"`
	WeaponAutoBalanceMinEffect         float64 `envconfig:"ARENA_WEAPON_AUTO_BALANCE_MIN_EFFECT" default:"0.05"`
	WeaponAutoBalanceMaxEvidenceRounds int     `envconfig:"ARENA_WEAPON_AUTO_BALANCE_MAX_EVIDENCE_ROUNDS" default:"48"`

	// OIDC / SSO (opt-in)
	OIDCEnabled      bool   `envconfig:"ARENA_OIDC_ENABLED" default:"false"`
	OIDCIssuer       string `envconfig:"ARENA_OIDC_ISSUER" default:""`
	OIDCClientID     string `envconfig:"ARENA_OIDC_CLIENT_ID" default:""`
	OIDCClientSecret string `envconfig:"ARENA_OIDC_CLIENT_SECRET" default:""`
	OIDCRedirectURI  string `envconfig:"ARENA_OIDC_REDIRECT_URI" default:""`
	OIDCSessionTTL   int    `envconfig:"ARENA_OIDC_SESSION_TTL_HOURS" default:"8"`
	OIDCAdminEmails  string `envconfig:"ARENA_OIDC_ADMIN_EMAILS" default:""`

	// Customer OIDC is deliberately a separate client/application from admin
	// SSO. A public customer login must never mint an admin-authorized session.
	CustomerOIDCEnabled      bool   `envconfig:"ARENA_CUSTOMER_OIDC_ENABLED" default:"false"`
	CustomerOIDCIssuer       string `envconfig:"ARENA_CUSTOMER_OIDC_ISSUER" default:""`
	CustomerOIDCClientID     string `envconfig:"ARENA_CUSTOMER_OIDC_CLIENT_ID" default:""`
	CustomerOIDCClientSecret string `envconfig:"ARENA_CUSTOMER_OIDC_CLIENT_SECRET" default:""`
	CustomerOIDCRedirectURI  string `envconfig:"ARENA_CUSTOMER_OIDC_REDIRECT_URI" default:""`
	// 720h (30 days) with sliding renewal on use (see customerSessionTTL in
	// customer_oidc.go): a visitor who returns at least once within any
	// 30-day window never has to sign back in, while an abandoned or stolen
	// cookie still lapses.
	CustomerOIDCSessionTTL      int `envconfig:"ARENA_CUSTOMER_OIDC_SESSION_TTL_HOURS" default:"720"`
	CustomerBotLinkRPM          int `envconfig:"ARENA_CUSTOMER_BOT_LINK_RPM" default:"10"`
	CustomerBotLinkPerHour      int `envconfig:"ARENA_CUSTOMER_BOT_LINK_PER_HOUR" default:"10"`
	CustomerAPIKeyMutationRPM   int `envconfig:"ARENA_CUSTOMER_API_KEY_MUTATION_RPM" default:"30"`
	CustomerAPIKeyCreatePerHour int `envconfig:"ARENA_CUSTOMER_API_KEY_CREATE_PER_HOUR" default:"10"`
	CustomerAPIKeyRevokePerHour int `envconfig:"ARENA_CUSTOMER_API_KEY_REVOKE_PER_HOUR" default:"20"`

	// Native customer email auth is an alternative to customer OIDC. It sends
	// one-time passwordless links through the deployment's transactional SMTP
	// service and reuses the same customer session/CSRF boundary as OIDC.
	CustomerEmailAuthEnabled         bool   `envconfig:"ARENA_CUSTOMER_EMAIL_AUTH_ENABLED" default:"false"`
	CustomerEmailSignInURL           string `envconfig:"ARENA_CUSTOMER_EMAIL_SIGN_IN_URL" default:""`
	CustomerEmailTokenTTLMinutes     int    `envconfig:"ARENA_CUSTOMER_EMAIL_TOKEN_TTL_MINUTES" default:"15"`
	CustomerEmailSendCooldownSeconds int    `envconfig:"ARENA_CUSTOMER_EMAIL_SEND_COOLDOWN_SECONDS" default:"60"`
	CustomerEmailSendRPM             int    `envconfig:"ARENA_CUSTOMER_EMAIL_SEND_RPM" default:"5"`
	SMTPHost                         string `envconfig:"ARENA_SMTP_HOST" default:""`
	SMTPPort                         int    `envconfig:"ARENA_SMTP_PORT" default:"465"`
	SMTPTLSMode                      string `envconfig:"ARENA_SMTP_TLS_MODE" default:"implicit"`
	SMTPTLSServerName                string `envconfig:"ARENA_SMTP_TLS_SERVER_NAME" default:""`
	SMTPUsername                     string `envconfig:"ARENA_SMTP_USERNAME" default:""`
	SMTPPassword                     string `envconfig:"ARENA_SMTP_PASSWORD" default:""`
	SMTPFrom                         string `envconfig:"ARENA_SMTP_FROM" default:""`

	// Cosmetics checkout is disabled by default. Enabling it requires the
	// verified customer auth provider, durable database state, and a complete
	// Stripe configuration; ValidateCosmeticsCheckoutConfig enforces that
	// launch boundary before the server starts.
	CosmeticsCheckoutEnabled bool   `envconfig:"ARENA_COSMETICS_CHECKOUT_ENABLED" default:"false"`
	StripeSecretKey          string `envconfig:"ARENA_STRIPE_SECRET_KEY" default:""`
	StripePublishableKey     string `envconfig:"ARENA_STRIPE_PUBLISHABLE_KEY" default:""`
	StripeWebhookSecrets     string `envconfig:"ARENA_STRIPE_WEBHOOK_SECRETS" default:""`
	StripeSuccessURL         string `envconfig:"ARENA_STRIPE_SUCCESS_URL" default:""`
	StripeCancelURL          string `envconfig:"ARENA_STRIPE_CANCEL_URL" default:""`
	StripeReturnURL          string `envconfig:"ARENA_STRIPE_RETURN_URL" default:""`
	StripePortalReturnURL    string `envconfig:"ARENA_STRIPE_PORTAL_RETURN_URL" default:""`
	StripeAutomaticTax       bool   `envconfig:"ARENA_STRIPE_AUTOMATIC_TAX" default:"false"`
	CosmeticsCheckoutRPM     int    `envconfig:"ARENA_COSMETICS_CHECKOUT_RPM" default:"10"`
	CosmeticsAccountReadRPM  int    `envconfig:"ARENA_COSMETICS_ACCOUNT_READ_RPM" default:"60"`

	// Developer lobby chat. Off by default; posting requires a signed-in
	// customer session, so enabling chat without customer auth yields a
	// read-only lobby. ChatAliveLock blocks posting while any bot linked to
	// the poster's account is alive in an active round, so chat cannot be
	// used to coordinate live bots (the spectator stream is delayed for the
	// same reason).
	//
	// ChatEnabled only seeds the chat_runtime_settings DB row the first time
	// the chat schema is created; from then on an admin can turn chat on or
	// off live from the admin panel's Chat Moderation tab (PUT
	// /admin/chat/enabled), with no env var edit or restart required. This
	// field exists so a fresh deployment can opt in from day one without
	// needing that extra click.
	ChatEnabled     bool `envconfig:"ARENA_CHAT_ENABLED" default:"false"`
	ChatMaxClients  int  `envconfig:"ARENA_CHAT_MAX_CLIENTS" default:"200"`
	ChatHistorySize int  `envconfig:"ARENA_CHAT_HISTORY_SIZE" default:"50"`
	ChatMaxBodyLen  int  `envconfig:"ARENA_CHAT_MAX_BODY_LEN" default:"280"`
	ChatPostsPerMin int  `envconfig:"ARENA_CHAT_POSTS_PER_MIN" default:"12"`
	ChatAliveLock   bool `envconfig:"ARENA_CHAT_ALIVE_LOCK" default:"true"`

	// Bot taunts: cosmetic enum-only emotes rendered as speech bubbles in
	// the spectator view. Spectator-only by construction (they ride the
	// delayed arena_state events channel and never enter bot tick payloads),
	// so they cannot become a bot-to-bot signal.
	TauntsEnabled     bool    `envconfig:"ARENA_TAUNTS_ENABLED" default:"false"`
	TauntCooldownSecs float64 `envconfig:"ARENA_TAUNT_COOLDOWN_SECS" default:"5"`
}

var C Config

// ShouldAutoMigrateDatabase reports whether the long-running server process
// owns schema setup. Managed production releases run the same idempotent
// migrations in the updater's one-shot owner container instead, so every
// runtime startup path must use this shared guard before attempting DDL.
func ShouldAutoMigrateDatabase() bool {
	return !C.DBMigrationsManaged
}

const (
	DefaultEloStarting = 1000
	DefaultEloMin      = 100
	DefaultEloMax      = 3000

	DefaultWeaponAutoBalanceMinDamageScale    = 0.70
	DefaultWeaponAutoBalanceMaxDamageScale    = 1.40
	DefaultWeaponAutoBalanceMinCooldownScale  = 0.75
	DefaultWeaponAutoBalanceMaxCooldownScale  = 1.35
	DefaultWeaponAutoBalanceMaxEvidenceRounds = 48
	DefaultWeaponAutoBalanceMinStep           = 0.005
	DefaultWeaponAutoBalanceStartStep         = 0.05

	absoluteWeaponAutoBalanceMinScale       = 0.50
	absoluteWeaponAutoBalanceMaxScale       = 2.00
	absoluteWeaponAutoBalanceMaxRoundWindow = 240
)

// WeaponAutoBalanceStepBounds returns the validated controller sensitivity.
// Keeping this in config lets both startup migration and the runtime controller
// agree on what a fresh algorithm epoch means.
func WeaponAutoBalanceStepBounds() (float64, float64) {
	minStep := C.WeaponAutoBalanceMinStep
	if minStep != minStep || minStep <= 0 || minStep > 0.05 {
		minStep = DefaultWeaponAutoBalanceMinStep
	}
	startStep := C.WeaponAutoBalanceStartStep
	if startStep != startStep || startStep <= 0 || startStep > 0.10 {
		startStep = DefaultWeaponAutoBalanceStartStep
	}
	if startStep < minStep {
		startStep = minStep
	}
	return minStep, startStep
}

func resolveWeaponAutoBalanceSettings(
	minDamage, maxDamage, minCooldown, maxCooldown float64,
	maxEvidenceRounds int,
) (float64, float64, float64, float64, int) {
	validRails := func(minV, maxV float64) bool {
		return minV >= absoluteWeaponAutoBalanceMinScale &&
			maxV <= absoluteWeaponAutoBalanceMaxScale &&
			minV <= 1 && maxV >= 1 && minV < maxV
	}
	if !validRails(minDamage, maxDamage) {
		minDamage = DefaultWeaponAutoBalanceMinDamageScale
		maxDamage = DefaultWeaponAutoBalanceMaxDamageScale
	}
	if !validRails(minCooldown, maxCooldown) {
		minCooldown = DefaultWeaponAutoBalanceMinCooldownScale
		maxCooldown = DefaultWeaponAutoBalanceMaxCooldownScale
	}
	if maxEvidenceRounds < 2 || maxEvidenceRounds > absoluteWeaponAutoBalanceMaxRoundWindow {
		maxEvidenceRounds = DefaultWeaponAutoBalanceMaxEvidenceRounds
	}
	return minDamage, maxDamage, minCooldown, maxCooldown, maxEvidenceRounds
}

// WeaponAutoBalanceDamageBounds returns defensively validated runtime rails.
func WeaponAutoBalanceDamageBounds() (float64, float64) {
	minDamage, maxDamage, _, _, _ := resolveWeaponAutoBalanceSettings(
		C.WeaponAutoBalanceMinDamageScale,
		C.WeaponAutoBalanceMaxDamageScale,
		C.WeaponAutoBalanceMinCooldownScale,
		C.WeaponAutoBalanceMaxCooldownScale,
		C.WeaponAutoBalanceMaxEvidenceRounds,
	)
	return minDamage, maxDamage
}

// WeaponAutoBalanceCooldownBounds returns defensively validated runtime rails.
func WeaponAutoBalanceCooldownBounds() (float64, float64) {
	_, _, minCooldown, maxCooldown, _ := resolveWeaponAutoBalanceSettings(
		C.WeaponAutoBalanceMinDamageScale,
		C.WeaponAutoBalanceMaxDamageScale,
		C.WeaponAutoBalanceMinCooldownScale,
		C.WeaponAutoBalanceMaxCooldownScale,
		C.WeaponAutoBalanceMaxEvidenceRounds,
	)
	return minCooldown, maxCooldown
}

// WeaponAutoBalanceEvidenceLimit bounds how long an inconclusive batch may
// accumulate while never undercutting the configured minimum evidence window.
func WeaponAutoBalanceEvidenceLimit(minRounds int) int {
	_, _, _, _, limit := resolveWeaponAutoBalanceSettings(
		C.WeaponAutoBalanceMinDamageScale,
		C.WeaponAutoBalanceMaxDamageScale,
		C.WeaponAutoBalanceMinCooldownScale,
		C.WeaponAutoBalanceMaxCooldownScale,
		C.WeaponAutoBalanceMaxEvidenceRounds,
	)
	if limit < minRounds {
		return minRounds
	}
	return limit
}

func resolveEloSettings(minElo, maxElo, startingElo int) (int, int, int) {
	if minElo <= 0 || maxElo <= minElo {
		minElo = DefaultEloMin
		maxElo = DefaultEloMax
	}
	if startingElo <= 0 {
		startingElo = DefaultEloStarting
	}
	if startingElo < minElo {
		startingElo = minElo
	} else if startingElo > maxElo {
		startingElo = maxElo
	}
	return minElo, maxElo, startingElo
}

func resolveShoveSettings(rangeTiles, knockbackTiles float64) (float64, float64) {
	if math.IsNaN(rangeTiles) || math.IsInf(rangeTiles, 0) || rangeTiles < 1 {
		rangeTiles = DefaultShoveRangeTiles
	} else {
		rangeTiles = math.Round(rangeTiles)
	}
	if math.IsNaN(knockbackTiles) || math.IsInf(knockbackTiles, 0) || knockbackTiles < 1 {
		knockbackTiles = DefaultShoveKnockbackTiles
	} else {
		knockbackTiles = math.Round(knockbackTiles)
	}
	return rangeTiles, knockbackTiles
}

// EloBounds returns the one validated rating interval used by the runtime
// and persistence repair. It remains defensive for tests or future live
// configuration code that mutates C after startup.
func EloBounds() (int, int) {
	minElo, maxElo, _ := resolveEloSettings(C.EloMin, C.EloMax, C.EloStarting)
	return minElo, maxElo
}

// ClampElo keeps every rating write inside the validated interval.
func ClampElo(elo int) int {
	minElo, maxElo := EloBounds()
	if elo < minElo {
		return minElo
	}
	if elo > maxElo {
		return maxElo
	}
	return elo
}

// StartingElo returns the configured initial rating after fallback and bound
// normalization, including when configuration is mutated after startup.
func StartingElo() int {
	_, _, startingElo := resolveEloSettings(C.EloMin, C.EloMax, C.EloStarting)
	return startingElo
}

// ValidateCosmeticsCheckoutConfig keeps the payment surface fail-closed. It
// intentionally does nothing while checkout is disabled so development and
// existing non-commerce deployments retain their current defaults.
func ValidateCosmeticsCheckoutConfig(cfg Config) error {
	if cfg.CosmeticsAccountReadRPM <= 0 {
		return fmt.Errorf("ARENA_COSMETICS_ACCOUNT_READ_RPM must be positive")
	}
	if !cfg.CosmeticsCheckoutEnabled {
		if len(ParseStripeWebhookSecrets(cfg.StripeWebhookSecrets)) > 0 {
			if stripeAPIKeyMode(cfg.StripeSecretKey, false) == "" {
				return fmt.Errorf("ARENA_STRIPE_SECRET_KEY must be retained as an sk_test, rk_test, sk_live, or rk_live key while Stripe webhooks service existing cosmetic subscriptions")
			}
			if err := validateCosmeticsCheckoutURL("ARENA_STRIPE_PORTAL_RETURN_URL", cfg.StripePortalReturnURL); err != nil {
				return err
			}
		}
		return nil
	}
	oidcReady := cfg.CustomerOIDCEnabled &&
		strings.TrimSpace(cfg.CustomerOIDCIssuer) != "" &&
		strings.TrimSpace(cfg.CustomerOIDCClientID) != "" &&
		strings.TrimSpace(cfg.CustomerOIDCClientSecret) != "" &&
		strings.TrimSpace(cfg.CustomerOIDCRedirectURI) != "" &&
		cfg.CustomerOIDCSessionTTL > 0
	emailReady := cfg.CustomerEmailAuthEnabled && ValidateCustomerEmailAuthConfig(cfg) == nil
	if !oidcReady && !emailReady {
		return fmt.Errorf("cosmetics checkout requires fully configured customer OIDC or native verified-email auth")
	}
	if cfg.DBOptional {
		return fmt.Errorf("cosmetics checkout requires the database; ARENA_DB_OPTIONAL must be false")
	}
	if strings.TrimSpace(cfg.StripeSecretKey) == "" {
		return fmt.Errorf("ARENA_STRIPE_SECRET_KEY is required when cosmetics checkout is enabled")
	}
	secretMode := stripeAPIKeyMode(cfg.StripeSecretKey, false)
	if secretMode == "" {
		return fmt.Errorf("ARENA_STRIPE_SECRET_KEY must be an sk_test, rk_test, sk_live, or rk_live key")
	}
	publishableMode := stripeAPIKeyMode(cfg.StripePublishableKey, true)
	if publishableMode == "" {
		return fmt.Errorf("ARENA_STRIPE_PUBLISHABLE_KEY must be a pk_test or pk_live key")
	}
	if secretMode != publishableMode {
		return fmt.Errorf("ARENA_STRIPE_SECRET_KEY and ARENA_STRIPE_PUBLISHABLE_KEY must use the same Stripe mode")
	}
	if len(ParseStripeWebhookSecrets(cfg.StripeWebhookSecrets)) == 0 {
		return fmt.Errorf("ARENA_STRIPE_WEBHOOK_SECRETS must contain at least one secret")
	}
	if err := validateCosmeticsCheckoutURL("ARENA_STRIPE_SUCCESS_URL", cfg.StripeSuccessURL); err != nil {
		return err
	}
	if err := validateCosmeticsCheckoutURL("ARENA_STRIPE_CANCEL_URL", cfg.StripeCancelURL); err != nil {
		return err
	}
	if err := validateCosmeticsCheckoutURL("ARENA_STRIPE_RETURN_URL", cfg.StripeReturnURL); err != nil {
		return err
	}
	if !strings.Contains(cfg.StripeReturnURL, "{CHECKOUT_SESSION_ID}") {
		return fmt.Errorf("ARENA_STRIPE_RETURN_URL must include {CHECKOUT_SESSION_ID}")
	}
	if err := validateCosmeticsCheckoutURL("ARENA_STRIPE_PORTAL_RETURN_URL", cfg.StripePortalReturnURL); err != nil {
		return err
	}
	if cfg.CosmeticsCheckoutRPM <= 0 {
		return fmt.Errorf("ARENA_COSMETICS_CHECKOUT_RPM must be positive")
	}
	return nil
}

func stripeAPIKeyMode(value string, publishable bool) string {
	value = strings.TrimSpace(value)
	if publishable {
		switch {
		case strings.HasPrefix(value, "pk_test_"):
			return "test"
		case strings.HasPrefix(value, "pk_live_"):
			return "live"
		default:
			return ""
		}
	}
	switch {
	case strings.HasPrefix(value, "sk_test_"), strings.HasPrefix(value, "rk_test_"):
		return "test"
	case strings.HasPrefix(value, "sk_live_"), strings.HasPrefix(value, "rk_live_"):
		return "live"
	default:
		return ""
	}
}

// ValidateCustomerEmailAuthConfig keeps passwordless registration fail-closed.
// The SMTP credential is a send-only app password and transport encryption is
// mandatory, including when the service is reached over a private address.
func ValidateCustomerEmailAuthConfig(cfg Config) error {
	if !cfg.CustomerEmailAuthEnabled {
		return nil
	}
	if cfg.DBOptional {
		return fmt.Errorf("native customer email auth requires the database; ARENA_DB_OPTIONAL must be false")
	}
	if err := validateCustomerEmailSignInURL(cfg.CustomerEmailSignInURL); err != nil {
		return err
	}
	if cfg.CustomerOIDCSessionTTL <= 0 {
		return fmt.Errorf("ARENA_CUSTOMER_OIDC_SESSION_TTL_HOURS must be positive for customer sessions")
	}
	if cfg.CustomerEmailTokenTTLMinutes < 5 || cfg.CustomerEmailTokenTTLMinutes > 60 {
		return fmt.Errorf("ARENA_CUSTOMER_EMAIL_TOKEN_TTL_MINUTES must be between 5 and 60")
	}
	if cfg.CustomerEmailSendCooldownSeconds < 10 || cfg.CustomerEmailSendCooldownSeconds > 3600 {
		return fmt.Errorf("ARENA_CUSTOMER_EMAIL_SEND_COOLDOWN_SECONDS must be between 10 and 3600")
	}
	if cfg.CustomerEmailSendRPM <= 0 || cfg.CustomerEmailSendRPM > 60 {
		return fmt.Errorf("ARENA_CUSTOMER_EMAIL_SEND_RPM must be between 1 and 60")
	}
	if strings.TrimSpace(cfg.SMTPHost) == "" {
		return fmt.Errorf("ARENA_SMTP_HOST is required")
	}
	if cfg.SMTPPort <= 0 || cfg.SMTPPort > 65535 {
		return fmt.Errorf("ARENA_SMTP_PORT must be between 1 and 65535")
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.SMTPTLSMode))
	if mode != "implicit" && mode != "starttls" {
		return fmt.Errorf("ARENA_SMTP_TLS_MODE must be implicit or starttls")
	}
	if strings.TrimSpace(cfg.SMTPTLSServerName) == "" {
		return fmt.Errorf("ARENA_SMTP_TLS_SERVER_NAME is required")
	}
	username, err := mail.ParseAddress(strings.TrimSpace(cfg.SMTPUsername))
	if err != nil || username.Address != strings.TrimSpace(cfg.SMTPUsername) {
		return fmt.Errorf("ARENA_SMTP_USERNAME must be a mailbox address")
	}
	if strings.TrimSpace(cfg.SMTPPassword) == "" {
		return fmt.Errorf("ARENA_SMTP_PASSWORD is required")
	}
	from, err := mail.ParseAddress(strings.TrimSpace(cfg.SMTPFrom))
	if err != nil || from.Address == "" || !strings.EqualFold(from.Address, username.Address) {
		return fmt.Errorf("ARENA_SMTP_FROM must use the authenticated ARENA_SMTP_USERNAME mailbox")
	}
	return nil
}

func validateCustomerEmailSignInURL(raw string) error {
	value := strings.TrimSpace(raw)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("ARENA_CUSTOMER_EMAIL_SIGN_IN_URL must be an absolute HTTPS Dashboard URL")
	}
	cleanPath := strings.TrimSuffix(parsed.EscapedPath(), "/")
	if cleanPath != "/dashboard" && cleanPath != "/arena/dashboard" {
		return fmt.Errorf("ARENA_CUSTOMER_EMAIL_SIGN_IN_URL must point to /dashboard/ or /arena/dashboard/")
	}
	if strings.EqualFold(parsed.Scheme, "https") {
		return nil
	}
	if strings.EqualFold(parsed.Scheme, "http") && isLoopbackCheckoutHost(parsed.Hostname()) {
		return nil
	}
	return fmt.Errorf("ARENA_CUSTOMER_EMAIL_SIGN_IN_URL must use HTTPS (HTTP is allowed only for loopback hosts)")
}

// ParseStripeWebhookSecrets converts the comma-separated rotation list into
// the ordered secrets accepted by the Stripe adapter. Config retains the raw
// string so Config remains comparable for the existing live-staging checks.
func ParseStripeWebhookSecrets(raw string) []string {
	values := strings.Split(raw, ",")
	secrets := make([]string, 0, len(values))
	for _, value := range values {
		if secret := strings.TrimSpace(value); secret != "" {
			secrets = append(secrets, secret)
		}
	}
	return secrets
}

func validateCosmeticsCheckoutURL(name, raw string) error {
	value := strings.TrimSpace(raw)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute HTTPS URL", name)
	}
	if strings.EqualFold(parsed.Scheme, "https") {
		return nil
	}
	if strings.EqualFold(parsed.Scheme, "http") && isLoopbackCheckoutHost(parsed.Hostname()) {
		return nil
	}
	return fmt.Errorf("%s must use HTTPS (HTTP is allowed only for loopback hosts)", name)
}

func isLoopbackCheckoutHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ValidateMovementConfig prevents a malformed floating-point environment
// value from poisoning per-bot movement progress or creating unbounded
// per-tick terrain loops.
func ValidateMovementConfig(cfg Config) error {
	pace := cfg.TerrainMoveCellsPerTick
	if math.IsNaN(pace) || math.IsInf(pace, 0) || pace <= 0 || pace > MaxTerrainMoveCellsPerTick {
		return fmt.Errorf("ARENA_TERRAIN_MOVE_CELLS_PER_TICK must be finite, greater than 0, and at most %.2f", MaxTerrainMoveCellsPerTick)
	}
	return nil
}

// ValidateAFKConfig keeps a legacy tick-based override from turning into an
// unexpectedly short timeout when the server tick rate changes. In particular,
// it fails closed on the historical 30-tick value, which is only three seconds
// at the default 10 Hz cadence.
func ValidateAFKConfig(cfg Config) error {
	if cfg.TickRate <= 0 {
		return fmt.Errorf("ARENA_AFK_TIMEOUT_TICKS requires ARENA_TICK_RATE to be greater than 0")
	}
	minimumTicks := cfg.TickRate * MinimumAFKTimeoutSeconds
	if cfg.AFKTimeoutTicks < minimumTicks {
		return fmt.Errorf("ARENA_AFK_TIMEOUT_TICKS must be at least %d ticks (%d seconds at %d Hz)", minimumTicks, MinimumAFKTimeoutSeconds, cfg.TickRate)
	}
	return nil
}

func Load() {
	if err := envconfig.Process("", &C); err != nil {
		slog.Error("failed to load config", "error", err)
		panic(err)
	}
	// The default is a duration, not a magic tick count. Preserve the legacy
	// ARENA_AFK_TIMEOUT_TICKS override for existing operators, but derive an
	// unset default from the configured cadence so it remains 30 seconds.
	if _, explicitlyConfigured := os.LookupEnv("ARENA_AFK_TIMEOUT_TICKS"); !explicitlyConfigured {
		C.AFKTimeoutTicks = C.TickRate * DefaultAFKTimeoutSeconds
	}
	rawMin, rawMax, rawStarting := C.EloMin, C.EloMax, C.EloStarting
	C.EloMin, C.EloMax, C.EloStarting = resolveEloSettings(rawMin, rawMax, rawStarting)
	if C.EloMin != rawMin || C.EloMax != rawMax || C.EloStarting != rawStarting {
		slog.Warn("normalized invalid Elo configuration",
			"configured_min", rawMin,
			"configured_max", rawMax,
			"configured_starting", rawStarting,
			"effective_min", C.EloMin,
			"effective_max", C.EloMax,
			"effective_starting", C.EloStarting,
		)
	}
	rawShoveRange, rawShoveKnockback := C.ShoveRange, C.ShoveKnockback
	C.ShoveRange, C.ShoveKnockback = resolveShoveSettings(rawShoveRange, rawShoveKnockback)
	if C.ShoveRange != rawShoveRange || C.ShoveKnockback != rawShoveKnockback {
		slog.Warn("normalized shove configuration to whole grid tiles",
			"configured_range", rawShoveRange,
			"configured_knockback", rawShoveKnockback,
			"effective_range", C.ShoveRange,
			"effective_knockback", C.ShoveKnockback,
		)
	}
	rawMinDamage, rawMaxDamage := C.WeaponAutoBalanceMinDamageScale, C.WeaponAutoBalanceMaxDamageScale
	rawMinCooldown, rawMaxCooldown := C.WeaponAutoBalanceMinCooldownScale, C.WeaponAutoBalanceMaxCooldownScale
	rawMaxEvidenceRounds := C.WeaponAutoBalanceMaxEvidenceRounds
	C.WeaponAutoBalanceMinDamageScale,
		C.WeaponAutoBalanceMaxDamageScale,
		C.WeaponAutoBalanceMinCooldownScale,
		C.WeaponAutoBalanceMaxCooldownScale,
		C.WeaponAutoBalanceMaxEvidenceRounds = resolveWeaponAutoBalanceSettings(
		rawMinDamage, rawMaxDamage, rawMinCooldown, rawMaxCooldown, rawMaxEvidenceRounds,
	)
	if C.WeaponAutoBalanceMinDamageScale != rawMinDamage || C.WeaponAutoBalanceMaxDamageScale != rawMaxDamage ||
		C.WeaponAutoBalanceMinCooldownScale != rawMinCooldown || C.WeaponAutoBalanceMaxCooldownScale != rawMaxCooldown ||
		C.WeaponAutoBalanceMaxEvidenceRounds != rawMaxEvidenceRounds {
		slog.Warn("normalized invalid weapon auto-balance configuration",
			"damage_rails", []float64{C.WeaponAutoBalanceMinDamageScale, C.WeaponAutoBalanceMaxDamageScale},
			"cooldown_rails", []float64{C.WeaponAutoBalanceMinCooldownScale, C.WeaponAutoBalanceMaxCooldownScale},
			"max_evidence_rounds", C.WeaponAutoBalanceMaxEvidenceRounds,
		)
	}
	if err := ValidateMovementConfig(C); err != nil {
		slog.Error("invalid movement configuration", "error", err)
		panic(err)
	}
	if err := ValidateAFKConfig(C); err != nil {
		slog.Error("invalid AFK configuration", "error", err)
		panic(err)
	}
	if err := ValidateCustomerEmailAuthConfig(C); err != nil {
		slog.Error("invalid customer email auth configuration", "error", err)
		panic(err)
	}
	if err := ValidateCosmeticsCheckoutConfig(C); err != nil {
		slog.Error("invalid cosmetics checkout configuration", "error", err)
		panic(err)
	}
	slog.Info("config loaded",
		"host", C.ServerHost,
		"port", C.ServerPort,
		"tick_rate", C.TickRate,
		"arena", [2]float64{C.ArenaWidth, C.ArenaHeight},
	)
	warnInsecureDefaults()
}

// warnInsecureDefaults logs loud (but non-fatal) warnings when the server is
// running with default/weak credentials. It never blocks startup — operators
// should be nudged to fix these, not locked out by a config validation bug.
func warnInsecureDefaults() {
	if C.DBPassword == "arena" || C.DBPassword == "changeme_arena_2026" {
		slog.Warn("SECURITY: ARENA_DB_PASSWORD is set to the insecure default " +
			"value — set a strong, unique password before exposing this server")
	}
	if C.AdminToken == "changeme_admin_token" {
		slog.Warn("SECURITY: ARENA_ADMIN_TOKEN is set to the insecure example " +
			"value — set a strong, unique token before exposing this server")
	} else if C.AdminToken == "" {
		if C.AdminLocalhostBypass {
			slog.Warn("SECURITY: ARENA_ADMIN_TOKEN is not set; admin API is only " +
				"reachable via the ARENA_ADMIN_LOCALHOST_BYPASS loopback path (no " +
				"remote admin auth configured)")
		} else {
			slog.Warn("SECURITY: ARENA_ADMIN_TOKEN is not set and " +
				"ARENA_ADMIN_LOCALHOST_BYPASS is disabled — the admin API cannot " +
				"be authenticated at all unless OIDC or a DB-issued token is configured")
		}
	}
}
