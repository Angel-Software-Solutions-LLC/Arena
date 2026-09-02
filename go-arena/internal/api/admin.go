package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"arena-server/internal/config"
	"arena-server/internal/db"
	"arena-server/internal/game"
	"arena-server/internal/platform"
	"arena-server/internal/security"
	"arena-server/internal/ws"

	"github.com/go-chi/chi/v5"
)

// AdminHandler holds references needed by admin endpoints.
type AdminHandler struct {
	Engine          *game.GameEngine
	ServiceStatus   *ServiceStatusService
	Shutdown        func()
	platformCatalog demoCatalogAuthority
	// ChatHub, when set, lets chat moderation purge hidden messages from
	// the live ring and connected clients, not just the database.
	ChatHub   *ws.ChatHub
	startTime time.Time
	// resetLeaderboardData is injectable so the destructive endpoint can be
	// contract-tested without connecting to a production-like database.
	resetLeaderboardData func(context.Context) error
	// Cache of DB token hashes to avoid DB hit on every request.
	tokenHashes []string
	tokenMu     sync.RWMutex
	// overrideMu serializes restart-staged game configuration and live weapon
	// tuning. Game configuration is never written into config.C after startup;
	// doing so would race the simulation's many direct reads.
	overrideMu sync.Mutex
	// activeConfig is the immutable startup snapshot shown alongside pending
	// PostgreSQL overrides. gameOverrides contains the desired restart state.
	activeConfig    config.Config
	activeConfigSet bool
	gameOverrides   map[string]interface{}
	// updateMu serializes the check/publish/submit transaction for self-update
	// requests. A second request must not overwrite the first job's notice.
	updateMu sync.Mutex
	// saveAdminOverrides is injectable for transaction-order tests. Production
	// handlers leave it nil and use db.SaveAdminOverrides.
	saveAdminOverrides func(context.Context, string, map[string]interface{}) error
}

func (h *AdminHandler) adminOverridePersistenceAvailable() bool {
	return db.Pool != nil || h.saveAdminOverrides != nil
}

func (h *AdminHandler) persistAdminOverrides(ctx context.Context, scope string, values map[string]interface{}) error {
	if h.saveAdminOverrides != nil {
		return h.saveAdminOverrides(ctx, scope, values)
	}
	return db.SaveAdminOverrides(ctx, scope, values)
}

// NewAdminHandler creates a new AdminHandler.
func NewAdminHandler(engine *game.GameEngine) *AdminHandler {
	h := &AdminHandler{
		Engine:               engine,
		platformCatalog:      platform.NewPostgresAuthority(),
		startTime:            time.Now(),
		resetLeaderboardData: db.ResetLeaderboard,
		activeConfig:         config.C,
		activeConfigSet:      true,
		gameOverrides:        make(map[string]interface{}),
	}
	// Initialize DB table and load token hashes.
	ctx := context.Background()
	if db.Pool != nil {
		if config.ShouldAutoMigrateDatabase() {
			if err := db.EnsureAdminTokensTable(ctx); err != nil {
				slog.Warn("failed to ensure admin_tokens table", "error", err)
			}
			if err := db.EnsureAdminRegistryTables(ctx); err != nil {
				slog.Warn("failed to ensure admin registry tables", "error", err)
			}
		}
		if values, err := db.LoadAdminOverrides(ctx, db.AdminOverrideScopeGameConfig); err != nil {
			slog.Warn("failed to load admin game overrides for pending-state display", "error", err)
		} else {
			h.gameOverrides = values
		}
		h.reloadTokenHashes()
	}
	h.loadAdminRegistries(ctx)
	// Registry loading canonicalizes the configured map pool after registering
	// custom shapes. Capture the active snapshot only after that startup-only
	// normalization has completed.
	h.activeConfig = config.C
	return h
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// constantTimeEqual compares two strings in constant time to avoid leaking
// information about secret tokens via response-time side channels.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (h *AdminHandler) reloadTokenHashes() {
	ctx := context.Background()
	hashes, err := db.GetAllAdminTokenHashes(ctx)
	if err != nil {
		slog.Warn("failed to load admin token hashes", "error", err)
		return
	}
	h.tokenMu.Lock()
	h.tokenHashes = hashes
	h.tokenMu.Unlock()
}

// hasDatabaseTokens reports whether any database-issued admin token is
// loaded, under the same lock reloadTokenHashes writes under. The middleware
// read this slice bare while a concurrent token issue reloaded it, which the
// race detector flags and which could answer "not configured" to a caller
// whose token was merely wrong.
func (h *AdminHandler) hasDatabaseTokens() bool {
	if h == nil {
		return false
	}
	h.tokenMu.RLock()
	defer h.tokenMu.RUnlock()
	return len(h.tokenHashes) > 0
}

// IsValidAdminToken checks if the given token is either the env var token or
// one of the database-stored tokens.
func (h *AdminHandler) IsValidAdminToken(token string) bool {
	if config.C.AdminToken != "" && constantTimeEqual(token, config.C.AdminToken) {
		return true
	}
	hashed := hashToken(token)
	h.tokenMu.RLock()
	defer h.tokenMu.RUnlock()
	for _, th := range h.tokenHashes {
		if constantTimeEqual(th, hashed) {
			return true
		}
	}
	return false
}

// AdminAuthMiddleware checks the X-Admin-Token header against the configured
// admin token. Localhost requests can bypass if ARENA_ADMIN_LOCALHOST_BYPASS is true.
// This is the legacy standalone version for backward compatibility.
var AdminAuthMiddleware = MakeAdminAuthMiddleware(nil)

type adminPrincipalContextKey struct{}

func withAdminPrincipal(ctx context.Context, principal string) context.Context {
	return context.WithValue(ctx, adminPrincipalContextKey{}, principal)
}

func adminPrincipalFromContext(ctx context.Context) string {
	principal, _ := ctx.Value(adminPrincipalContextKey{}).(string)
	return strings.TrimSpace(principal)
}

// MakeAdminAuthMiddleware creates an admin auth middleware that checks both the
// env var token and any dynamically created tokens via the handler. These are
// the machine paths; a human administrator arrives through
// MakeAdminAuthMiddlewareWithPlatformAdmins.
func MakeAdminAuthMiddleware(handler *AdminHandler) func(http.Handler) http.Handler {
	return MakeAdminAuthMiddlewareWithPlatformAdmins(handler, nil)
}

/*
 * MakeAdminAuthMiddlewareWithPlatformAdmins is the whole admin guard: the
 * machine credentials, plus the one thing the customer cookie is allowed to
 * say.
 *
 * That cookie used to be refused here unconditionally, and the reason still
 * holds for every session that does not carry the Angel Accounts
 * platform-administrator claim: a customer session means "signed in", not
 * "trusted". What changed is that Accounts now states, inside a token Arena
 * verifies, which identities administer the platform — so the refusal narrows
 * from "this cookie" to "this cookie without that claim". A session that never
 * carried it, or whose grant has lapsed, is exactly as unwelcome as before;
 * TestCustomerCookieNeverAuthorizesAdmin still holds.
 *
 * There used to be a third way in: an Arena-operated admin SSO application
 * with its own arena_admin_session cookie, admitted by an email allowlist
 * (ARENA_OIDC_ADMIN_EMAILS). It is retired. Two places deciding who
 * administers the platform is one too many, and the second one was a list of
 * addresses maintained by hand in Arena's environment — nothing revoked it
 * when somebody left the desk. The support-desk role in Accounts is now the
 * single source of HUMAN admin authority.
 *
 * What is deliberately NOT retired is everything below that authenticates a
 * machine: ARENA_ADMIN_TOKEN, database-issued admin tokens, and the loopback
 * bypass. Those are automation credentials, not identities, and they are also
 * the break-glass path if Accounts is unreachable.
 */
func MakeAdminAuthMiddlewareWithPlatformAdmins(handler *AdminHandler, customerHandler *CustomerOIDCHandler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cfg := &config.C

			// Check localhost bypass.
			if cfg.AdminLocalhostBypass && isLocalhost(r) {
				next.ServeHTTP(w, r.WithContext(withAdminPrincipal(r.Context(), "localhost-bypass")))
				return
			}

			// Check an Angel Accounts sign-in that carries the verified
			// platform-administrator claim. This is the only human path.
			principal, authorized, denyReason := customerHandler.platformAdminPrincipal(r)
			if authorized {
				next.ServeHTTP(w, r.WithContext(withAdminPrincipal(r.Context(), principal)))
				return
			}

			token := r.Header.Get("X-Admin-Token")
			/*
			 * An administrator's mutation that failed the same-origin or CSRF
			 * check is answered with what actually went wrong — but only when
			 * there is no token to fall back on. A client presenting a token
			 * asked to be authenticated by that token; it must reach the token
			 * paths below exactly as it did before this cookie was consulted.
			 */
			if denyReason != "" && token == "" {
				writeError(w, http.StatusForbidden, denyReason)
				return
			}
			if token == "" {
				// No credential of any kind. Where a human can sign in, say
				// so; otherwise the only answer is the machine header.
				if customerAccountAuthEnabled(customerHandler) {
					writeError(w, http.StatusUnauthorized,
						"not authenticated — sign in with Angel Accounts or provide X-Admin-Token")
					return
				}
				writeError(w, http.StatusUnauthorized, "missing X-Admin-Token header")
				return
			}

			// Check env var token.
			if cfg.AdminToken != "" && constantTimeEqual(token, cfg.AdminToken) {
				next.ServeHTTP(w, r.WithContext(withAdminPrincipal(r.Context(), "admin-token:environment")))
				return
			}

			// Check dynamic tokens via handler.
			if handler != nil && handler.IsValidAdminToken(token) {
				fingerprint := hashToken(token)
				if len(fingerprint) > 12 {
					fingerprint = fingerprint[:12]
				}
				next.ServeHTTP(w, r.WithContext(withAdminPrincipal(r.Context(), "admin-token:"+fingerprint)))
				return
			}

			// If no token configured at all.
			if cfg.AdminToken == "" && !handler.hasDatabaseTokens() {
				writeError(w, http.StatusServiceUnavailable, "admin token not configured")
				return
			}

			writeError(w, http.StatusForbidden, "invalid admin token")
		})
	}
}

// isLocalhost returns true if the request originates from a loopback address.
func isLocalhost(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// Routes registers all admin routes on the given chi.Router.
func (h *AdminHandler) Routes(r chi.Router) {
	// Demo bot management.
	r.Post("/bots/demo-loadout", h.applyDemoLoadout)

	// Debug / inspection.
	r.Get("/debug/connections", h.debugConnections)
	r.Get("/debug/game-state", h.debugGameState)
	r.Get("/debug/bot/{id}", h.debugBot)
	r.Get("/debug/metrics", h.debugMetrics)
	r.Get("/debug/rounds", h.debugRounds)

	// Bot admin.
	r.Post("/bots/{id}/kick", h.kickBot)
	r.Post("/bots/{id}/ban", h.banBot)
	r.Post("/bots/{id}/kill", h.killBot)
	r.Post("/bots/{id}/teleport", h.teleportBot)
	r.Post("/bots/{id}/heal", h.healBot)
	r.Get("/bots", h.listBots)

	// Chat moderation.
	r.Get("/chat/messages", h.listChatMessages)
	r.Post("/chat/messages/{id}/hide", h.hideChatMessage)
	r.Post("/chat/messages/{id}/unhide", h.unhideChatMessage)
	r.Post("/chat/ban", h.setChatBan)
	r.Get("/chat/bans", h.listChatBans)
	r.Get("/chat/ban-log", h.chatBanLog)
	r.Get("/chat/keywords", h.listChatKeywords)
	r.Post("/chat/keywords", h.addChatKeyword)
	r.Delete("/chat/keywords/{id}", h.deleteChatKeyword)
	r.Put("/chat/enabled", h.setChatEnabled)
	r.Get("/chat/overview", h.chatOverview)

	// Game control.
	r.Post("/game/pause", h.gamePause)
	r.Post("/game/resume", h.gameResume)
	r.Post("/game/restart-round", h.gameRestartRound)
	r.Put("/game/config", h.updateGameConfig)
	r.Get("/game/config", h.getGameConfig)
	r.Get("/game/maps", h.getMapSettings)
	r.Put("/game/maps", h.updateMapSettings)
	r.Post("/game/maps/preview", h.previewMap)
	r.Put("/game/maps/custom/{name}", h.upsertCustomMap)
	r.Delete("/game/maps/custom/{name}", h.deleteCustomMap)

	// Public-site content blocks.
	r.Get("/content-blocks", h.listContentBlocks)
	r.Put("/content-blocks/{key}", h.updateContentBlock)

	// Site-wide public broadcast controls.
	r.Get("/broadcasts", h.listBroadcasts)
	r.Post("/broadcasts", h.createBroadcast)
	r.Delete("/broadcasts/{id}", h.clearBroadcast)

	// Data management.
	r.Get("/db/stats", h.dbStats)
	r.Post("/db/reset-leaderboard", h.resetLeaderboard)
	r.Post("/db/cleanup-stale", h.cleanupStale)
	r.Get("/logs", h.getLogs)

	// Spectator management.
	r.Get("/spectators", h.listSpectators)
	r.Post("/spectators/{index}/kick", h.kickSpectator)

	// Bot profiler.
	r.Get("/bots/{id}/profile", h.botProfile)

	// Weapon balance tuning.
	r.Get("/weapons", h.getWeapons)
	r.Put("/weapons/{name}", h.updateWeapon)

	// Freeze / unfreeze.
	r.Post("/bots/{id}/freeze", h.freezeBot)
	r.Post("/bots/{id}/unfreeze", h.unfreezeBot)

	// IP banning.
	r.Get("/ip-bans", h.listIPBans)
	r.Post("/ip-bans", h.addIPBan)
	r.Delete("/ip-bans/{ip}", h.removeIPBan)

	// Anti-cheat analysis.
	r.Get("/anticheat", h.anticheatScan)

	// API key management.
	r.Get("/api-keys", h.listAPIKeys)
	r.Post("/api-keys/{id}/revoke", h.revokeAPIKey)

	// Admin token management.
	r.Get("/admin-tokens", h.listAdminTokens)
	r.Post("/admin-tokens", h.createAdminToken)
	r.Delete("/admin-tokens/{id}", h.deleteAdminToken)

	// Server.
	r.Get("/config", h.getServerConfig)
	r.Get("/health/deep", h.deepHealthCheck)
	r.Post("/server/gc", h.triggerGC)
	r.Post("/server/restart", h.restartServer)

	// Self-update: running vs latest commit, trigger, and progress. See update.go.
	r.Get("/version", adminVersionInfo)
	r.Post("/update", h.triggerUpdate)
	r.Get("/update/status", updateStatus)

	// Demo-bot fleet (external private container). See admin_demobots.go.
	r.Get("/demobots/status", h.demobotsStatus)
	r.Get("/demobots/config", h.demobotsConfig)
	r.Put("/demobots/count", h.demobotsSetCount)
	r.Get("/demobots/version", h.demobotsVersion)
	r.Post("/demobots/update", h.demobotsTriggerUpdate)
}

// ============================================================================
// Debug / inspection
// ============================================================================

func (h *AdminHandler) debugConnections(w http.ResponseWriter, r *http.Request) {
	conns := h.Engine.ListConnections()
	writeJSON(w, http.StatusOK, conns)
}

func (h *AdminHandler) debugGameState(w http.ResponseWriter, r *http.Request) {
	state := h.Engine.GetFullGameState()
	writeJSON(w, http.StatusOK, state)
}

func (h *AdminHandler) debugBot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	detail, found := h.Engine.GetBotDetail(id)
	if !found {
		writeError(w, http.StatusNotFound, fmt.Sprintf("bot %q not found", id))
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *AdminHandler) debugMetrics(w http.ResponseWriter, r *http.Request) {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	uptime := time.Since(h.startTime)

	metrics := map[string]interface{}{
		"goroutines":     runtime.NumGoroutine(),
		"go_version":     runtime.Version(),
		"num_cpu":        runtime.NumCPU(),
		"uptime_seconds": int(uptime.Seconds()),
		"uptime_human":   uptime.Round(time.Second).String(),
		"memory": map[string]interface{}{
			"alloc_mb":       float64(memStats.Alloc) / 1024 / 1024,
			"total_alloc_mb": float64(memStats.TotalAlloc) / 1024 / 1024,
			"sys_mb":         float64(memStats.Sys) / 1024 / 1024,
			"heap_alloc_mb":  float64(memStats.HeapAlloc) / 1024 / 1024,
			"heap_inuse_mb":  float64(memStats.HeapInuse) / 1024 / 1024,
			"heap_objects":   memStats.HeapObjects,
			"stack_inuse_mb": float64(memStats.StackInuse) / 1024 / 1024,
		},
		"gc": map[string]interface{}{
			"num_gc":          memStats.NumGC,
			"last_gc_seconds": float64(memStats.LastGC) / 1e9,
			"pause_total_ms":  float64(memStats.PauseTotalNs) / 1e6,
		},
		"game": map[string]interface{}{
			"tick_count":     h.Engine.GetTickCount(),
			"bots_connected": h.Engine.ConnectedBotCount(),
			"spectators":     h.Engine.SpectatorCount(),
			"tick_rate":      config.C.TickRate,
		},
		"websocket": ws.GetWebSocketAdmissionMetrics(),
	}
	if db.Pool != nil {
		poolStats := db.Pool.Stat()
		metrics["database_pool"] = map[string]interface{}{
			"total_connections":        poolStats.TotalConns(),
			"acquired_connections":     poolStats.AcquiredConns(),
			"idle_connections":         poolStats.IdleConns(),
			"max_connections":          poolStats.MaxConns(),
			"constructing_connections": poolStats.ConstructingConns(),
			"acquire_count":            poolStats.AcquireCount(),
			"canceled_acquire_count":   poolStats.CanceledAcquireCount(),
			"empty_acquire_count":      poolStats.EmptyAcquireCount(),
			"acquire_duration_ms":      float64(poolStats.AcquireDuration()) / float64(time.Millisecond),
		}
	}
	writeJSON(w, http.StatusOK, metrics)
}

func (h *AdminHandler) debugRounds(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit := 20
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}

	if db.Pool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}

	rows, err := db.Pool.Query(r.Context(),
		`SELECT id, round_number, started_at, ended_at, bots_participated, mvp_bot_id, status
		 FROM rounds ORDER BY round_number DESC LIMIT $1`, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to query rounds: "+err.Error())
		return
	}
	defer rows.Close()

	var rounds []map[string]interface{}
	for rows.Next() {
		var rd db.Round
		if err := rows.Scan(&rd.ID, &rd.RoundNumber, &rd.StartedAt, &rd.EndedAt,
			&rd.BotsParticipated, &rd.MVPBotID, &rd.Status); err != nil {
			continue
		}
		entry := map[string]interface{}{
			"id":                rd.ID,
			"round_number":      rd.RoundNumber,
			"started_at":        rd.StartedAt,
			"ended_at":          rd.EndedAt,
			"bots_participated": rd.BotsParticipated,
			"mvp_bot_id":        rd.MVPBotID,
			"status":            rd.Status,
		}
		rounds = append(rounds, entry)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"rounds": rounds,
		"count":  len(rounds),
	})
}

// ============================================================================
// Bot admin
// ============================================================================

func (h *AdminHandler) kickBot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req struct {
		Reason string `json:"reason"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.Reason == "" {
		req.Reason = "kicked by admin"
	}

	if !h.Engine.KickBot(id, req.Reason) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("bot %q not found", id))
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": fmt.Sprintf("bot %q kicked", id),
		"reason":  req.Reason,
	})
}

func (h *AdminHandler) banBot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Get the bot's API key ID before kicking.
	detail, found := h.Engine.GetBotDetail(id)
	if !found {
		writeError(w, http.StatusNotFound, fmt.Sprintf("bot %q not found", id))
		return
	}

	apiKeyID, _ := detail["api_key_id"].(string)
	if apiKeyID != "" {
		h.Engine.BanKey(apiKeyID)
		// Also deactivate in DB.
		if db.Pool != nil {
			if err := db.DeactivateAPIKey(r.Context(), apiKeyID); err != nil {
				slog.Error("failed to deactivate banned key in DB", "error", err)
			}
		}
	}

	h.Engine.KickBot(id, "banned by admin")

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":    fmt.Sprintf("bot %q banned and kicked", id),
		"api_key_id": apiKeyID,
	})
}

func (h *AdminHandler) killBot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.Engine.KillBot(id) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("bot %q not found or not alive", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": fmt.Sprintf("bot %q killed", id),
	})
}

func (h *AdminHandler) teleportBot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate coordinates.
	if req.X < 0 || req.X > config.C.ArenaWidth || req.Y < 0 || req.Y > config.C.ArenaHeight {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("coordinates out of bounds (0-%.0f, 0-%.0f)", config.C.ArenaWidth, config.C.ArenaHeight))
		return
	}

	if !h.Engine.TeleportBot(id, req.X, req.Y) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("bot %q not found", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":  fmt.Sprintf("bot %q teleported", id),
		"position": [2]float64{req.X, req.Y},
	})
}

func (h *AdminHandler) healBot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req struct {
		HP float64 `json:"hp"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.HP <= 0 {
		writeError(w, http.StatusBadRequest, "hp must be positive")
		return
	}

	if !h.Engine.HealBot(id, req.HP) {
		writeError(w, http.StatusNotFound, fmt.Sprintf("bot %q not found", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": fmt.Sprintf("bot %q healed by %.0f HP", id, req.HP),
	})
}

func (h *AdminHandler) listBots(w http.ResponseWriter, r *http.Request) {
	bots := h.Engine.ListAllBots()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"bots":  bots,
		"count": len(bots),
	})
}

// ============================================================================
// Game control
// ============================================================================

func (h *AdminHandler) gamePause(w http.ResponseWriter, r *http.Request) {
	h.Engine.Pause()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": "game paused",
		"paused":  true,
	})
}

func (h *AdminHandler) gameResume(w http.ResponseWriter, r *http.Request) {
	h.Engine.Resume()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": "game resumed",
		"paused":  false,
	})
}

func (h *AdminHandler) gameRestartRound(w http.ResponseWriter, r *http.Request) {
	h.Engine.ForceRestartRound()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": "round restarted",
	})
}

func (h *AdminHandler) getGameConfig(w http.ResponseWriter, r *http.Request) {
	h.overrideMu.Lock()
	defer h.overrideMu.Unlock()
	writeJSON(w, http.StatusOK, h.gameConfigResponseLocked())
}

func (h *AdminHandler) updateGameConfig(w http.ResponseWriter, r *http.Request) {
	if !h.adminOverridePersistenceAvailable() {
		writeError(w, http.StatusServiceUnavailable, "database is required to save configuration")
		return
	}
	var updates map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	h.overrideMu.Lock()
	defer h.overrideMu.Unlock()

	applied, rejected, restartRequired, err := h.stageGameConfigUpdatesLocked(r.Context(), updates)
	if err != nil {
		slog.Error("failed to persist admin game config", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to persist configuration; active settings were not changed")
		return
	}
	if len(applied) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error": "no valid configuration values were supplied", "rejected": rejected,
		})
		return
	}

	slog.Info("admin staged game config", "applied", applied, "rejected", rejected, "restart_required", restartRequired)
	message := "configuration saved; values are already active"
	if restartRequired {
		message = "configuration saved; restart the server to activate it"
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":          message,
		"applied":          applied,
		"rejected":         rejected,
		"persisted":        true,
		"restart_required": restartRequired,
		"activation":       "server_restart",
	})
}

// ============================================================================
// Data management
// ============================================================================

func (h *AdminHandler) dbStats(w http.ResponseWriter, r *http.Request) {
	if db.Pool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}

	poolStats := db.Pool.Stat()
	stats := map[string]interface{}{
		"pool": map[string]interface{}{
			"total_conns":        poolStats.TotalConns(),
			"idle_conns":         poolStats.IdleConns(),
			"acquired_conns":     poolStats.AcquiredConns(),
			"max_conns":          poolStats.MaxConns(),
			"constructing_conns": poolStats.ConstructingConns(),
		},
	}

	// Get table row counts.
	tables := []string{"api_keys", "bots", "bot_stats", "round_bot_stats", "kill_log", "rounds"}
	tableCounts := make(map[string]int)
	for _, table := range tables {
		var count int
		// Using fmt.Sprintf here is safe because table names are hardcoded above.
		err := db.Pool.QueryRow(r.Context(),
			fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count)
		if err != nil {
			tableCounts[table] = -1
		} else {
			tableCounts[table] = count
		}
	}
	stats["table_counts"] = tableCounts

	writeJSON(w, http.StatusOK, stats)
}

func (h *AdminHandler) resetLeaderboard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Confirm != "RESET_ALL_STATS" {
		writeError(w, http.StatusBadRequest, "must send {\"confirm\": \"RESET_ALL_STATS\"} to confirm")
		return
	}

	reset := h.resetLeaderboardData
	if reset == nil {
		reset = db.ResetLeaderboard
	}
	// Bound the TRUNCATE round trip like the sibling admin handlers do: a
	// blocked table lock must not stall persistence (or, worst case, the
	// admin request) indefinitely.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var err error
	if h.Engine != nil {
		err = h.Engine.ResetLeaderboard(ctx, reset)
	} else {
		err = reset(ctx)
	}
	if err != nil {
		if errors.Is(err, db.ErrNoDatabase) {
			writeError(w, http.StatusServiceUnavailable, "database not available")
			return
		}
		slog.Error("admin failed to reset leaderboard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to reset leaderboard")
		return
	}

	slog.Warn("admin reset leaderboard - all-time and time-window stats wiped")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":         "leaderboard reset successfully",
		"cleared_sources": []string{"all_time", "time_windows"},
	})
}

func (h *AdminHandler) cleanupStale(w http.ResponseWriter, r *http.Request) {
	if db.Pool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}

	var req struct {
		Days int `json:"days"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Days <= 0 {
		writeError(w, http.StatusBadRequest, "must provide {\"days\": N} where N > 0")
		return
	}

	cutoff := time.Now().AddDate(0, 0, -req.Days)

	// Deactivate instead of deleting. API keys cascade to bots, bot links, and
	// legacy entitlement evidence; destructive cleanup can therefore erase the
	// only proof a customer has before an old purchase is claimed. Keeping the
	// rows also avoids a delete-vs-link snapshot race. PostgreSQL rechecks this
	// direct UPDATE predicate after row-lock waits, so a concurrently refreshed
	// last_seen value is not incorrectly cleaned up.
	result, err := db.Pool.Exec(r.Context(), `
		UPDATE api_keys
		SET is_active = false
		WHERE is_active = true
		  AND (last_seen < $1 OR (last_seen IS NULL AND created_at < $1))`, cutoff)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to cleanup: "+err.Error())
		return
	}
	deactivated := result.RowsAffected()

	slog.Info("admin deactivated stale bot keys", "days", req.Days, "deactivated", deactivated)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":     fmt.Sprintf("deactivated %d stale keys older than %d days; bot and purchase records were preserved", deactivated, req.Days),
		"removed":     0,
		"deactivated": deactivated,
		"cutoff_date": cutoff.Format(time.RFC3339),
	})
}

func (h *AdminHandler) getLogs(w http.ResponseWriter, r *http.Request) {
	// Read the last N lines from stdout/stderr log.
	// Since Go uses slog which typically writes to stderr, we read from a log
	// file if available. Otherwise, return a message.
	linesStr := r.URL.Query().Get("lines")
	lines := 100
	if linesStr != "" {
		if n, err := strconv.Atoi(linesStr); err == nil && n > 0 && n <= 1000 {
			lines = n
		}
	}

	// Try to read from common log locations.
	logPaths := []string{"/var/log/arena-server.log", "/tmp/arena-server.log"}
	for _, path := range logPaths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		logLines := strings.Split(string(data), "\n")
		start := 0
		if len(logLines) > lines {
			start = len(logLines) - lines
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"source": path,
			"lines":  logLines[start:],
			"count":  len(logLines[start:]),
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": "log file not found; logs are written to stderr (use docker logs or journalctl)",
		"lines":   []string{},
	})
}

// ============================================================================
// Server
// ============================================================================

func (h *AdminHandler) getServerConfig(w http.ResponseWriter, r *http.Request) {
	c := &config.C
	// Return sanitized config (no secrets).
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"server_host":            c.ServerHost,
		"server_port":            c.ServerPort,
		"tick_rate":              c.TickRate,
		"max_bots":               c.MaxBots,
		"max_spectators":         c.MaxSpectators,
		"arena_size":             [2]float64{c.ArenaWidth, c.ArenaHeight},
		"db_host":                c.DBHost,
		"db_port":                c.DBPort,
		"db_name":                c.DBName,
		"redis_host":             c.RedisHost,
		"redis_port":             c.RedisPort,
		"cors_origins":           c.CORSOrigins,
		"elo_k_factor":           c.EloKFactor,
		"elo_starting":           c.EloStarting,
		"admin_localhost_bypass": c.AdminLocalhostBypass,
		"admin_token_set":        c.AdminToken != "",
	})
}

func (h *AdminHandler) deepHealthCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	health := map[string]interface{}{
		"status":        "ok",
		"uptime":        time.Since(h.startTime).Round(time.Second).String(),
		"goroutines":    runtime.NumGoroutine(),
		"heap_alloc_mb": float64(memStats.HeapAlloc) / 1024 / 1024,
		"bots_online":   h.Engine.ConnectedBotCount(),
		"spectators":    h.Engine.SpectatorCount(),
		"paused":        h.Engine.IsPaused(),
	}

	// DB ping.
	if db.Pool != nil {
		if err := db.Pool.Ping(ctx); err != nil {
			health["db"] = "error: " + err.Error()
			health["status"] = "degraded"
		} else {
			health["db"] = "ok"
		}
	} else {
		health["db"] = "not connected"
		health["status"] = "degraded"
	}

	// Redis ping.
	if security.RedisClient != nil {
		if err := security.RedisClient.Ping(ctx).Err(); err != nil {
			health["redis"] = "error: " + err.Error()
			health["status"] = "degraded"
		} else {
			health["redis"] = "ok"
		}
	} else {
		health["redis"] = "not connected"
	}

	writeJSON(w, http.StatusOK, health)
}

func (h *AdminHandler) triggerGC(w http.ResponseWriter, r *http.Request) {
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	runtime.GC()

	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	slog.Info("admin triggered GC",
		"heap_before_mb", float64(before.HeapAlloc)/1024/1024,
		"heap_after_mb", float64(after.HeapAlloc)/1024/1024,
	)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":        "garbage collection triggered",
		"heap_before_mb": float64(before.HeapAlloc) / 1024 / 1024,
		"heap_after_mb":  float64(after.HeapAlloc) / 1024 / 1024,
		"freed_mb":       float64(before.HeapAlloc-after.HeapAlloc) / 1024 / 1024,
	})
}

func (h *AdminHandler) restartServer(w http.ResponseWriter, r *http.Request) {
	slog.Warn("admin triggered server restart")
	if h.ServiceStatus != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		_, err := h.ServiceStatus.SetManualRestart(ctx)
		cancel()
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "could not publish the required restart notice")
			return
		}
	} else if h.Engine != nil {
		// Legacy/test wiring without the durable service still gets a semantic
		// status transition before the process accepts the restart.
		h.Engine.NotifyServiceRestart(60)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": "server restarting — Docker will auto-restart the container",
	})

	// Give the response time to flush, then exit.
	// Docker's restart policy (unless-stopped) will bring the container back up.
	go func() {
		time.Sleep(500 * time.Millisecond)
		slog.Info("shutting down for restart...")
		if h.Shutdown != nil {
			h.Shutdown()
			return
		}
		os.Exit(0)
	}()
}

// ============================================================================
// Helpers
// ============================================================================

func toInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	}
	return 0, false
}

// cloudflareBlockIP creates a Cloudflare IP Access Rule to block the given IP.
func cloudflareBlockIP(ip, note string) error {
	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/firewall/access_rules/rules", config.C.CloudflareZoneID)
	body := map[string]interface{}{
		"mode": "block",
		"configuration": map[string]interface{}{
			"target": "ip",
			"value":  ip,
		},
		"notes": note,
	}
	bodyJSON, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", url, bytes.NewReader(bodyJSON))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+config.C.CloudflareAPIToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cloudflare API returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func toFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// ============================================================================
// API Key Management
// ============================================================================

func (h *AdminHandler) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := db.ListAllAPIKeys(r.Context())
	if err != nil {
		slog.Error("admin listAPIKeys failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list API keys")
		return
	}

	// Enrich with online status from engine
	onlineBots := h.Engine.ListAllBots()
	onlineByAPIKeyID := make(map[string]string) // api_key_id -> remote_addr
	for _, b := range onlineBots {
		botID, _ := b["bot_id"].(string)
		apiKeyID, _ := b["api_key_id"].(string)
		if botID != "" && apiKeyID != "" {
			if detail, ok := h.Engine.GetBotDetail(botID); ok {
				if conn, ok := detail["connection"].(map[string]interface{}); ok {
					if addr, ok := conn["remote_addr"].(string); ok {
						onlineByAPIKeyID[apiKeyID] = addr
					}
				}
			}
		}
	}

	for _, k := range keys {
		keyID, _ := k["key_id"].(string)
		if addr, online := onlineByAPIKeyID[keyID]; online {
			k["is_online"] = true
			k["connected_ip"] = addr
		} else {
			k["is_online"] = false
			k["connected_ip"] = nil
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"api_keys": keys,
		"count":    len(keys),
	})
}

func (h *AdminHandler) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing key id")
		return
	}

	err := db.DeactivateAPIKey(r.Context(), id)
	if err != nil {
		slog.Error("admin revokeAPIKey failed", "error", err, "key_id", id)
		writeError(w, http.StatusInternalServerError, "failed to revoke key")
		return
	}

	// Also ban the key in-engine and kick any connected bot
	h.Engine.BanKey(id)
	for _, b := range h.Engine.ListAllBots() {
		apiKeyID, _ := b["api_key_id"].(string)
		botID, _ := b["bot_id"].(string)
		if apiKeyID == id {
			h.Engine.KickBot(botID, "API key revoked by admin")
			break
		}
	}

	slog.Info("admin revoked API key", "key_id", id)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": "API key revoked",
		"key_id":  id,
	})
}

// ============================================================================
// Admin Token Management
// ============================================================================

func (h *AdminHandler) listAdminTokens(w http.ResponseWriter, r *http.Request) {
	tokens := make([]map[string]interface{}, 0)

	// Primary token from env var (never show full token, just masked)
	if config.C.AdminToken != "" {
		t := config.C.AdminToken
		masked := t[:min(4, len(t))] + "..." + t[max(0, len(t)-4):]
		tokens = append(tokens, map[string]interface{}{
			"id":         "primary",
			"label":      "Primary (env var)",
			"token_hint": masked,
			"created_at": h.startTime,
			"source":     "ARENA_ADMIN_TOKEN",
			"deletable":  false,
		})
	}

	// Database tokens
	if db.Pool != nil {
		dbTokens, err := db.ListAdminTokens(r.Context())
		if err != nil {
			slog.Warn("failed to list admin tokens from DB", "error", err)
		} else {
			for _, t := range dbTokens {
				t["source"] = "database"
				t["deletable"] = true
				tokens = append(tokens, t)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"tokens": tokens,
		"count":  len(tokens),
	})
}

func (h *AdminHandler) createAdminToken(w http.ResponseWriter, r *http.Request) {
	if db.Pool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}

	var req struct {
		Label string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.Label = "Admin Token"
	}
	if req.Label == "" {
		req.Label = "Admin Token"
	}

	// Generate a secure random token
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}
	tokenStr := "arena_admin_" + hex.EncodeToString(tokenBytes)
	tokenHash := hashToken(tokenStr)
	tokenHint := tokenStr[:16] + "..." + tokenStr[len(tokenStr)-4:]

	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate id")
		return
	}
	id := hex.EncodeToString(idBytes)

	if err := db.CreateAdminToken(r.Context(), id, req.Label, tokenHash, tokenHint); err != nil {
		slog.Error("failed to create admin token", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save token")
		return
	}

	// Reload cache
	h.reloadTokenHashes()

	slog.Info("admin created new admin token", "id", id, "label", req.Label)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":         id,
		"label":      req.Label,
		"token":      tokenStr,
		"created_at": time.Now(),
		"message":    "Store this token safely. It will not be shown again.",
	})
}

func (h *AdminHandler) deleteAdminToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" || id == "primary" {
		writeError(w, http.StatusBadRequest, "cannot delete primary token")
		return
	}

	if db.Pool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}

	if err := db.DeleteAdminToken(r.Context(), id); err != nil {
		slog.Error("failed to delete admin token", "error", err, "id", id)
		writeError(w, http.StatusNotFound, "token not found")
		return
	}

	// Reload cache
	h.reloadTokenHashes()

	slog.Info("admin deleted admin token", "id", id)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": "admin token deleted",
		"id":      id,
	})
}

// ============================================================================
// Anti-Cheat Analysis
// ============================================================================

type acFlag struct {
	Severity string `json:"severity"` // "critical", "high", "medium", "low"
	Category string `json:"category"` // "stats", "accuracy", "damage", "speed", "kills", "connection"
	Message  string `json:"message"`
	Value    string `json:"value"`
	Expected string `json:"expected"`
}

func anticheatRiskScore(flags []acFlag) int {
	risk := 0
	for _, f := range flags {
		switch f.Severity {
		case "critical":
			risk += 35
		case "high":
			risk += 18
		case "medium":
			risk += 8
		case "low":
			risk += 3
		}
	}
	if risk > 100 {
		return 100
	}
	return risk
}

func anticheatConfidence(risk int, hardFlags int, totalFlags int) string {
	if hardFlags > 0 || risk >= 70 {
		return "high"
	}
	if risk >= 35 || totalFlags >= 3 {
		return "medium"
	}
	return "review"
}

func defenseReductionOutOfRange(observed, expected float64) bool {
	// Admin detail values are rounded to one decimal, so 0.06 can arrive as 0.1.
	return observed > expected+0.051
}

func damagePerHitOutOfRange(shotsHit int, avgDamage, maxDamageWithBoost float64) (bool, string) {
	if shotsHit < 6 {
		return false, ""
	}
	if avgDamage > maxDamageWithBoost*2.25 {
		return true, "critical"
	}
	if avgDamage > maxDamageWithBoost*1.65 {
		return true, "high"
	}
	return false, ""
}

// ============================================================================
// Spectator Management
// ============================================================================

func (h *AdminHandler) listSpectators(w http.ResponseWriter, r *http.Request) {
	specs := h.Engine.ListSpectators()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"spectators": specs,
		"count":      len(specs),
	})
}

func (h *AdminHandler) kickSpectator(w http.ResponseWriter, r *http.Request) {
	idx, err := strconv.Atoi(chi.URLParam(r, "index"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid index")
		return
	}
	if !h.Engine.KickSpectator(idx) {
		writeError(w, http.StatusNotFound, "spectator not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"message": "spectator kicked"})
}

// ============================================================================
// Bot Behavior Profiler
// ============================================================================

func (h *AdminHandler) botProfile(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Samples parameter — how many snapshots to take (default 30, max 100)
	samplesStr := r.URL.Query().Get("samples")
	samples := 30
	if n, err := strconv.Atoi(samplesStr); err == nil && n > 0 && n <= 100 {
		samples = n
	}

	// Interval in ms between samples (default 100ms = 10 per second)
	intervalStr := r.URL.Query().Get("interval_ms")
	intervalMS := 100
	if n, err := strconv.Atoi(intervalStr); err == nil && n >= 50 && n <= 2000 {
		intervalMS = n
	}

	// Collect snapshots
	type snapshot struct {
		Action           string
		TargetID         string
		Position         [2]float64
		HP               float64
		CooldownLeft     float64
		DodgeCooldown    int
		IsAlive          bool
		ClosestEnemyDist float64
		InZone           bool
		DistToZone       float64
	}
	snapshots := make([]snapshot, 0, samples)

	for i := 0; i < samples; i++ {
		profile, ok := h.Engine.GetBotProfile(id)
		if !ok {
			writeError(w, http.StatusNotFound, "bot not found or disconnected")
			return
		}

		pos := [2]float64{0, 0}
		if p, ok := profile["position"].(game.Vec2); ok {
			pos = [2]float64(p)
		}
		snap := snapshot{
			Action:           fmt.Sprintf("%v", profile["current_action"]),
			TargetID:         fmt.Sprintf("%v", profile["action_target"]),
			HP:               toF(profile["hp"]),
			CooldownLeft:     toF(profile["cooldown_remaining"]),
			DodgeCooldown:    toI(profile["dodge_cooldown"]),
			IsAlive:          profile["is_alive"] == true,
			ClosestEnemyDist: toF(profile["closest_enemy_dist"]),
			InZone:           profile["in_zone"] == true,
			DistToZone:       toF(profile["dist_to_zone_center"]),
			Position:         pos,
		}
		snapshots = append(snapshots, snap)

		if i < samples-1 {
			time.Sleep(time.Duration(intervalMS) * time.Millisecond)
		}
	}

	// Get final profile for static data
	finalProfile, ok := h.Engine.GetBotProfile(id)
	if !ok {
		writeError(w, http.StatusNotFound, "bot disconnected during profiling")
		return
	}

	// Analyze actions
	actionCounts := map[string]int{}
	var totalMoveDist float64
	var zoneTimeIn, zoneTimeOut int
	var avgEnemyDist float64
	var lowHPActions int // actions while HP < 30%
	attackTargets := map[string]int{}

	for i, s := range snapshots {
		a := s.Action
		if a == "" || a == "<nil>" {
			a = "idle"
		}
		actionCounts[a]++

		if s.InZone {
			zoneTimeIn++
		} else {
			zoneTimeOut++
		}
		avgEnemyDist += s.ClosestEnemyDist

		maxHP := toF(finalProfile["max_hp"])
		if maxHP > 0 && s.HP < maxHP*0.3 {
			lowHPActions++
		}

		if a == "attack" && s.TargetID != "" && s.TargetID != "<nil>" {
			attackTargets[s.TargetID]++
		}

		if i > 0 {
			dx := s.Position[0] - snapshots[i-1].Position[0]
			dy := s.Position[1] - snapshots[i-1].Position[1]
			totalMoveDist += (dx*dx + dy*dy) // squared, fine for relative comparison
		}
	}
	if len(snapshots) > 0 {
		avgEnemyDist /= float64(len(snapshots))
	}

	// Determine playstyle
	totalActions := 0
	for _, c := range actionCounts {
		totalActions += c
	}
	pct := func(key string) float64 {
		if totalActions == 0 {
			return 0
		}
		return float64(actionCounts[key]) / float64(totalActions) * 100
	}

	// Behavioral classification
	var playstyle string
	var traits []string

	movePct := pct("move") + pct("move_to")
	attackPct := pct("attack")
	dodgePct := pct("dodge")
	idlePct := pct("idle")

	if attackPct > 50 {
		playstyle = "Aggressive"
		traits = append(traits, "Heavy attacker — spends >50% of ticks attacking")
	} else if attackPct > 30 {
		playstyle = "Balanced"
		traits = append(traits, "Balanced attack/movement ratio")
	} else if movePct > 60 {
		playstyle = "Evasive"
		traits = append(traits, "Highly mobile — spends >60% of ticks moving")
	} else if idlePct > 40 {
		playstyle = "Passive"
		traits = append(traits, "Often idle — may be AFK or waiting for opportunities")
	} else {
		playstyle = "Mixed"
	}

	if dodgePct > 10 {
		traits = append(traits, "Dodge-heavy — uses dodge frequently")
	}
	if avgEnemyDist < 5 {
		traits = append(traits, "Brawler — stays very close to enemies")
	} else if avgEnemyDist > 15 {
		traits = append(traits, "Kiter — maintains distance from enemies")
	}

	accuracy := toF(finalProfile["accuracy"])
	if accuracy > 80 {
		traits = append(traits, fmt.Sprintf("High accuracy (%.0f%%) — precise targeting", accuracy))
	} else if accuracy < 30 && toI(finalProfile["round_shots_fired"]) > 5 {
		traits = append(traits, fmt.Sprintf("Low accuracy (%.0f%%) — spray-and-pray style", accuracy))
	}

	zonePct := float64(zoneTimeIn) / float64(max(zoneTimeIn+zoneTimeOut, 1)) * 100
	if zonePct > 80 {
		traits = append(traits, "Zone-aware — stays inside safe zone")
	} else if zonePct < 30 {
		traits = append(traits, "Zone-ignorant — frequently outside safe zone")
	}

	lowHPPct := float64(lowHPActions) / float64(max(len(snapshots), 1)) * 100
	if lowHPPct > 30 {
		traits = append(traits, "Risk-taker — often fights at low HP")
	}

	if len(attackTargets) == 1 {
		traits = append(traits, "Target-locked — focuses on a single enemy")
	} else if len(attackTargets) > 3 {
		traits = append(traits, "Target-switcher — attacks many different enemies")
	}

	// Top attack target
	var topTarget string
	var topTargetCount int
	for t, c := range attackTargets {
		if c > topTargetCount {
			topTargetCount = c
			topTarget = t
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"bot":              finalProfile,
		"playstyle":        playstyle,
		"traits":           traits,
		"samples":          len(snapshots),
		"interval_ms":      intervalMS,
		"duration_ms":      len(snapshots) * intervalMS,
		"action_breakdown": actionCounts,
		"action_pcts": map[string]interface{}{
			"move":   r1(movePct),
			"attack": r1(attackPct),
			"dodge":  r1(dodgePct),
			"idle":   r1(idlePct),
		},
		"positioning": map[string]interface{}{
			"avg_enemy_distance": r1(avgEnemyDist),
			"zone_time_in_pct":   r1(zonePct),
			"low_hp_time_pct":    r1(lowHPPct),
			"movement_intensity": r1(totalMoveDist),
		},
		"targeting": map[string]interface{}{
			"unique_targets":      len(attackTargets),
			"top_target_id":       topTarget,
			"top_target_attacks":  topTargetCount,
			"target_distribution": attackTargets,
		},
	})
}

func r1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}

func toF(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	}
	return 0
}
func toI(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

// ============================================================================
// Weapon Balance Tuning
// ============================================================================

func (h *AdminHandler) getWeapons(w http.ResponseWriter, r *http.Request) {
	h.overrideMu.Lock()
	defer h.overrideMu.Unlock()
	weapons := make(map[string]interface{})
	overrides := map[string]interface{}{}
	if db.Pool != nil {
		if values, err := db.LoadAdminOverrides(r.Context(), db.AdminOverrideScopeWeapon); err == nil {
			overrides = values
		}
	}
	for _, name := range game.GetAvailableWeapons() {
		wc, _ := game.GetBaseWeaponConfig(name)
		effective := game.GetWeaponConfig(name)
		balance, _ := game.GetWeaponBalanceState(name)
		_, overridden := overrides[name]
		configSource := "built_in"
		if overridden {
			configSource = "database_override"
		}
		weapons[name] = map[string]interface{}{
			"name":               wc.Name,
			"damage":             wc.Damage,
			"range":              wc.Range,
			"cooldown":           wc.Cooldown,
			"special":            wc.Special,
			"param":              wc.Param,
			"effective_damage":   effective.Damage,
			"effective_range":    effective.Range,
			"effective_cooldown": effective.Cooldown,
			"damage_scale":       balance.DamageScale,
			"cooldown_scale":     balance.CooldownScale,
			"adjustment_scale":   balance.AdjustmentScale,
			"rounds_tracked":     balance.RoundsTracked,
			"config_source":      configSource,
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"weapons": weapons,
	})
}

func (h *AdminHandler) updateWeapon(w http.ResponseWriter, r *http.Request) {
	if !h.adminOverridePersistenceAvailable() {
		writeError(w, http.StatusServiceUnavailable, "database is required to save weapon tuning")
		return
	}
	name := chi.URLParam(r, "name")

	var req map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	h.overrideMu.Lock()
	defer h.overrideMu.Unlock()

	wc, ok := game.GetBaseWeaponConfig(name)
	if !ok {
		writeError(w, http.StatusNotFound, "weapon not found: "+name)
		return
	}

	applied := []string{}

	if v, ok := req["damage"]; ok {
		if f, ok := toFloat(v); ok && f >= 0 {
			wc.Damage = int(f)
			applied = append(applied, fmt.Sprintf("damage=%d", wc.Damage))
		}
	}
	if v, ok := req["range"]; ok {
		if f, ok := toFloat(v); ok && f >= 0 {
			wc.Range = f
			wc.GridRange = int(math.Round(f / config.C.PathfindingCellSize))
			applied = append(applied, fmt.Sprintf("range=%.1f", wc.Range))
		}
	}
	if v, ok := req["cooldown"]; ok {
		if f, ok := toFloat(v); ok && f > 0 {
			wc.Cooldown = f
			applied = append(applied, fmt.Sprintf("cooldown=%.2f", wc.Cooldown))
		}
	}
	if v, ok := req["param"]; ok {
		if f, ok := toFloat(v); ok {
			wc.Param = f
			applied = append(applied, fmt.Sprintf("param=%.2f", wc.Param))
		}
	}
	if len(applied) == 0 {
		writeError(w, http.StatusBadRequest, "no valid weapon values were supplied")
		return
	}

	if err := h.persistAdminOverrides(r.Context(), db.AdminOverrideScopeWeapon, map[string]interface{}{
		name: persistedWeaponValue(wc),
	}); err != nil {
		slog.Error("failed to persist weapon tuning", "weapon", name, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to persist weapon tuning; no changes were applied")
		return
	}
	if !game.UpdateBaseWeaponConfig(name, wc) {
		writeError(w, http.StatusInternalServerError, "failed to apply persisted weapon tuning")
		return
	}

	slog.Info("admin updated weapon", "weapon", name, "changes", applied)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":   "weapon updated",
		"weapon":    name,
		"applied":   applied,
		"config":    wc,
		"persisted": true,
	})
}

// ============================================================================
// Freeze / Unfreeze
// ============================================================================

func (h *AdminHandler) freezeBot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.Engine.FreezeBot(id) {
		writeError(w, http.StatusNotFound, "bot not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"message": "bot frozen", "bot_id": id})
}

func (h *AdminHandler) unfreezeBot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.Engine.UnfreezeBot(id) {
		writeError(w, http.StatusNotFound, "bot not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"message": "bot unfrozen", "bot_id": id})
}

// ============================================================================
// IP Banning
// ============================================================================

func (h *AdminHandler) listIPBans(w http.ResponseWriter, r *http.Request) {
	ips := h.Engine.GetBannedIPs()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"banned_ips": ips,
		"count":      len(ips),
	})
}

func (h *AdminHandler) addIPBan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IP string `json:"ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.IP == "" {
		writeError(w, http.StatusBadRequest, "missing ip field")
		return
	}
	h.Engine.BanIP(req.IP)

	// Kick all bots from this IP
	kicked := 0
	for _, b := range h.Engine.ListAllBots() {
		botID, _ := b["bot_id"].(string)
		if botID == "" {
			continue
		}
		if detail, ok := h.Engine.GetBotDetail(botID); ok {
			if conn, ok := detail["connection"].(map[string]interface{}); ok {
				if addr, ok := conn["remote_addr"].(string); ok {
					host, _, _ := net.SplitHostPort(addr)
					if host == req.IP {
						h.Engine.KickBot(botID, "IP banned by admin")
						kicked++
					}
				}
			}
		}
	}

	// Push to Cloudflare if configured
	cfResult := ""
	if config.C.CloudflareAPIToken != "" && config.C.CloudflareZoneID != "" {
		if err := cloudflareBlockIP(req.IP, "Banned via Arena admin dashboard"); err != nil {
			slog.Error("cloudflare IP block failed", "ip", req.IP, "error", err)
			cfResult = "cloudflare push failed: " + err.Error()
		} else {
			cfResult = "pushed to cloudflare"
			slog.Info("IP blocked on cloudflare", "ip", req.IP)
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"message":     "IP banned",
		"ip":          req.IP,
		"bots_kicked": kicked,
		"cloudflare":  cfResult,
	})
}

func (h *AdminHandler) removeIPBan(w http.ResponseWriter, r *http.Request) {
	ip := chi.URLParam(r, "ip")
	if ip == "" {
		writeError(w, http.StatusBadRequest, "missing ip")
		return
	}
	h.Engine.UnbanIP(ip)
	writeJSON(w, http.StatusOK, map[string]interface{}{"message": "IP unbanned", "ip": ip})
}

// ============================================================================
// Anti-Cheat Analysis
// ============================================================================

func (h *AdminHandler) anticheatScan(w http.ResponseWriter, r *http.Request) {
	c := &config.C

	// Weapon max damage lookup: baseDmg * maxAttackMult(2.0)
	weaponMaxDmg := map[string]float64{
		"sword":   21 * 2.0,
		"bow":     16 * 2.0 * (1 + float64(config.C.BowChargeMaxTicks)*config.C.BowChargeDamagePerTick),
		"daggers": 11 * 2.0 * config.C.DaggerBackstabBonusMultiplier,
		"shield":  14 * 2.0 * config.C.ShieldBashBonusMultiplier,
		"spear":   17 * 2.0 * config.C.SpearBraceBonusMultiplier,
		"staff":   17 * 2.0,
		"grapple": 14 * 2.0 * config.C.GrappleSlamBonusMultiplier,
	}
	weaponCooldowns := map[string]float64{
		"sword": 0.55, "bow": 1.05, "daggers": 0.35,
		"shield": 0.8, "spear": 0.75, "staff": 1.65, "grapple": 1.05,
	}

	allBots := h.Engine.ListAllBots()
	type botReport struct {
		BotID       string   `json:"bot_id"`
		Name        string   `json:"name"`
		AvatarColor string   `json:"avatar_color"`
		Weapon      string   `json:"weapon"`
		Elo         int      `json:"elo"`
		Status      string   `json:"status"`
		Flags       []acFlag `json:"flags"`
		FlagCount   int      `json:"flag_count"`
		RiskScore   int      `json:"risk_score"`
		Confidence  string   `json:"confidence"`
		Action      string   `json:"recommended_action"`
	}

	var flaggedBots []botReport
	// Track IPs for multi-account detection
	ipToBots := make(map[string][]string)

	for _, b := range allBots {
		botID, _ := b["bot_id"].(string)
		if botID == "" {
			continue
		}
		detail, ok := h.Engine.GetBotDetail(botID)
		if !ok {
			continue
		}

		var flags []acFlag
		name, _ := detail["name"].(string)
		weapon, _ := detail["weapon"].(string)
		avatarColor, _ := detail["avatar_color"].(string)
		elo, _ := detail["elo"].(int)
		status, _ := b["status"].(string)

		// --- Connection IP tracking ---
		if conn, ok := detail["connection"].(map[string]interface{}); ok {
			if addr, ok := conn["remote_addr"].(string); ok && addr != "" {
				host, _, _ := net.SplitHostPort(addr)
				if host != "" {
					ipToBots[host] = append(ipToBots[host], name+" ("+botID[:8]+")")
				}
			}
		}

		// --- 1. Stat budget violation ---
		if stats, ok := detail["stats"].(map[string]int); ok {
			total := 0
			for _, v := range stats {
				total += v
			}
			if total > c.StatBudget {
				flags = append(flags, acFlag{
					Severity: "critical", Category: "stats",
					Message:  "Stat budget exceeded",
					Value:    fmt.Sprintf("%d points used", total),
					Expected: fmt.Sprintf("<= %d", c.StatBudget),
				})
			}
			for k, v := range stats {
				if v < c.StatMin {
					flags = append(flags, acFlag{
						Severity: "critical", Category: "stats",
						Message: fmt.Sprintf("Stat '%s' below minimum", k),
						Value:   fmt.Sprintf("%d", v), Expected: fmt.Sprintf(">= %d", c.StatMin),
					})
				}
				if v > c.StatMax {
					flags = append(flags, acFlag{
						Severity: "critical", Category: "stats",
						Message: fmt.Sprintf("Stat '%s' above maximum", k),
						Value:   fmt.Sprintf("%d", v), Expected: fmt.Sprintf("<= %d", c.StatMax),
					})
				}
			}
			// Check stat count (must be exactly 4)
			if len(stats) != 4 {
				flags = append(flags, acFlag{
					Severity: "critical", Category: "stats",
					Message: "Wrong number of stat keys",
					Value:   fmt.Sprintf("%d keys", len(stats)), Expected: "4 keys (hp,speed,attack,defense)",
				})
			}
		}

		// --- 2. HP exceeds max for stats ---
		hp, _ := detail["hp"].(float64)
		maxHP, _ := detail["max_hp"].(float64)
		if stats, ok := detail["stats"].(map[string]int); ok {
			expectedMax := c.StatHPBase + float64(stats["hp"])*c.StatHPPerPoint
			if maxHP > expectedMax+0.5 {
				flags = append(flags, acFlag{
					Severity: "critical", Category: "stats",
					Message: "MaxHP exceeds stat-derived maximum",
					Value:   fmt.Sprintf("%.0f", maxHP), Expected: fmt.Sprintf("%.0f", expectedMax),
				})
			}
			if hp > maxHP+0.5 {
				flags = append(flags, acFlag{
					Severity: "high", Category: "stats",
					Message: "Current HP exceeds MaxHP",
					Value:   fmt.Sprintf("%.1f", hp), Expected: fmt.Sprintf("<= %.0f", maxHP),
				})
			}
		}

		// --- 3. Speed exceeds stat-derived max ---
		speed, _ := detail["speed"].(float64)
		if stats, ok := detail["stats"].(map[string]int); ok {
			expectedSpeed := c.StatSpeedBase + float64(stats["speed"])*c.StatSpeedPerPoint
			// Allow 2x for speed boost pickup
			maxPossibleSpeed := expectedSpeed * 2.0
			if speed > maxPossibleSpeed+0.1 {
				flags = append(flags, acFlag{
					Severity: "high", Category: "speed",
					Message: "Movement speed exceeds maximum (even with boost)",
					Value:   fmt.Sprintf("%.1f", speed), Expected: fmt.Sprintf("<= %.1f", maxPossibleSpeed),
				})
			}
		}

		// --- 4. Attack multiplier exceeds stat-derived max ---
		atkMult, _ := detail["attack_multiplier"].(float64)
		if stats, ok := detail["stats"].(map[string]int); ok {
			expectedAtk := c.StatAttackBase + float64(stats["attack"])*c.StatAttackPerPoint
			// Allow 1.5x for damage boost pickup
			maxPossibleAtk := expectedAtk * 1.5
			if atkMult > maxPossibleAtk+0.05 {
				flags = append(flags, acFlag{
					Severity: "high", Category: "damage",
					Message: "Attack multiplier exceeds maximum (even with boost)",
					Value:   fmt.Sprintf("%.2f", atkMult), Expected: fmt.Sprintf("<= %.2f", maxPossibleAtk),
				})
			}
		}

		// --- 5. Defense reduction exceeds stat-derived max ---
		defRed, _ := detail["defense_reduction"].(float64)
		if stats, ok := detail["stats"].(map[string]int); ok {
			expectedDef := float64(stats["defense"]) * c.StatDefensePerPoint
			if defenseReductionOutOfRange(defRed, expectedDef) {
				flags = append(flags, acFlag{
					Severity: "high", Category: "stats",
					Message: "Defense reduction exceeds stat-derived value",
					Value:   fmt.Sprintf("%.2f", defRed), Expected: fmt.Sprintf("<= %.2f", expectedDef),
				})
			}
		}

		// --- 6. Accuracy analysis ---
		shotsFired, _ := detail["round_shots_fired"].(int)
		shotsHit, _ := detail["round_shots_hit"].(int)
		if shotsFired >= 25 {
			accuracy := float64(shotsHit) / float64(shotsFired) * 100.0
			if accuracy > 98.0 {
				flags = append(flags, acFlag{
					Severity: "high", Category: "accuracy",
					Message: fmt.Sprintf("Suspiciously high accuracy (%d/%d shots)", shotsHit, shotsFired),
					Value:   fmt.Sprintf("%.1f%%", accuracy), Expected: "< 98% over 25+ shots",
				})
			} else if accuracy > 92.0 {
				flags = append(flags, acFlag{
					Severity: "medium", Category: "accuracy",
					Message: fmt.Sprintf("Very high accuracy (%d/%d shots)", shotsHit, shotsFired),
					Value:   fmt.Sprintf("%.1f%%", accuracy), Expected: "< 92% over 25+ shots",
				})
			}
		}

		// --- 7. Damage per hit analysis ---
		dmgDealt, _ := detail["round_damage_dealt"].(float64)
		if shotsHit > 0 && weapon != "" {
			avgDmg := dmgDealt / float64(shotsHit)
			maxDmg := weaponMaxDmg[weapon]
			if maxDmg == 0 {
				maxDmg = 50
			}
			// With damage boost (1.5x) the absolute max is higher
			maxDmgWithBoost := maxDmg * 1.5
			if out, severity := damagePerHitOutOfRange(shotsHit, avgDmg, maxDmgWithBoost); out {
				flags = append(flags, acFlag{
					Severity: severity, Category: "damage",
					Message:  "Average damage per hit exceeds weapon maximum",
					Value:    fmt.Sprintf("%.1f per hit", avgDmg),
					Expected: fmt.Sprintf("review above %.1f (%s max with boost, 6+ hits)", maxDmgWithBoost, weapon),
				})
			}
		}

		// --- 8. Kill rate analysis ---
		roundKills, _ := detail["round_kills"].(int)
		if roundKills >= 3 && weapon != "" {
			cooldown := weaponCooldowns[weapon]
			if cooldown == 0 {
				cooldown = 0.5
			}
			// Max theoretical kills per round at weapon cooldown
			roundDuration := c.RoundDuration
			maxKillsTheoretical := int(roundDuration / cooldown)
			// Flag if kills approach theoretical max (>60% of max)
			if roundKills > int(float64(maxKillsTheoretical)*0.6) {
				flags = append(flags, acFlag{
					Severity: "medium", Category: "kills",
					Message:  "Kill count approaching theoretical maximum for weapon cooldown",
					Value:    fmt.Sprintf("%d kills", roundKills),
					Expected: fmt.Sprintf("<< %d theoretical max (%s, %.1fs cd)", maxKillsTheoretical, weapon, cooldown),
				})
			}
		}

		// --- 9. K/D ratio analysis ---
		roundDeaths, _ := detail["round_deaths"].(int)
		if roundKills >= 5 && roundDeaths == 0 {
			flags = append(flags, acFlag{
				Severity: "medium", Category: "kills",
				Message: "High kills with zero deaths this round",
				Value:   fmt.Sprintf("%d kills, 0 deaths", roundKills), Expected: "Some deaths expected",
			})
		}

		// --- 10. Damage taken analysis (invuln exploit) ---
		dmgTaken, _ := detail["round_damage_taken"].(float64)
		if roundKills >= 5 && dmgTaken < 1.0 {
			flags = append(flags, acFlag{
				Severity: "medium", Category: "damage",
				Message:  "Active in combat but almost no damage taken",
				Value:    fmt.Sprintf("%.1f damage taken, %d kills", dmgTaken, roundKills),
				Expected: "Review combat replay before action",
			})
		}

		// --- 11. Impossible shield absorb ---
		shieldAbsorb, _ := detail["shield_absorb"].(float64)
		if shieldAbsorb > c.PickupShieldBubbleHP+1 {
			flags = append(flags, acFlag{
				Severity: "high", Category: "stats",
				Message:  "Shield absorb exceeds pickup shield HP",
				Value:    fmt.Sprintf("%.0f", shieldAbsorb),
				Expected: fmt.Sprintf("<= %.0f", c.PickupShieldBubbleHP),
			})
		}

		// --- 12. Invalid weapon ---
		validWeapons := map[string]bool{"sword": true, "bow": true, "daggers": true, "shield": true, "spear": true, "staff": true, "grapple": true}
		if weapon != "" && !validWeapons[weapon] {
			flags = append(flags, acFlag{
				Severity: "critical", Category: "stats",
				Message: "Unknown/invalid weapon equipped",
				Value:   weapon, Expected: "sword, bow, daggers, shield, spear, staff, or grapple",
			})
		}

		// --- 13. Impossible distance traveled ---
		dist, _ := detail["round_distance"].(float64)
		if stats, ok := detail["stats"].(map[string]int); ok && dist > 0 {
			maxSpeed := (c.StatSpeedBase + float64(stats["speed"])*c.StatSpeedPerPoint) * 2.0 // with speed boost
			tickRate := float64(c.TickRate)
			maxDistPerTick := maxSpeed / tickRate
			maxTicks := c.RoundDuration * tickRate
			maxPossibleDist := maxDistPerTick * maxTicks * 1.1 // 10% margin
			if dist > maxPossibleDist {
				flags = append(flags, acFlag{
					Severity: "high", Category: "speed",
					Message:  "Distance traveled exceeds theoretical maximum",
					Value:    fmt.Sprintf("%.0f units", dist),
					Expected: fmt.Sprintf("<= %.0f units", maxPossibleDist),
				})
			}
		}

		// --- 14. Permanent invulnerability ---
		invuln, _ := detail["invuln_ticks"].(int)
		dodge, _ := detail["dodge_cooldown"].(int)
		if invuln > c.DodgeInvulnTicks+2 {
			flags = append(flags, acFlag{
				Severity: "critical", Category: "stats",
				Message:  "Invulnerability ticks exceed dodge maximum",
				Value:    fmt.Sprintf("%d ticks", invuln),
				Expected: fmt.Sprintf("<= %d ticks", c.DodgeInvulnTicks),
			})
		}
		_ = dodge // dodge cooldown is valid state

		if len(flags) > 0 {
			hardFlags := 0
			for _, f := range flags {
				if f.Severity == "critical" && f.Category == "stats" {
					hardFlags++
				}
			}
			risk := anticheatRiskScore(flags)
			confidence := anticheatConfidence(risk, hardFlags, len(flags))
			action := "watch"
			if confidence == "high" && risk >= 70 {
				action = "review before kick"
			} else if confidence == "medium" {
				action = "compare samples"
			}
			flaggedBots = append(flaggedBots, botReport{
				BotID: botID, Name: name, AvatarColor: avatarColor,
				Weapon: weapon, Elo: elo, Status: status,
				Flags: flags, FlagCount: len(flags), RiskScore: risk,
				Confidence: confidence, Action: action,
			})
		}
	}

	// --- Multi-account IP analysis ---
	var ipFlags []map[string]interface{}
	for ip, bots := range ipToBots {
		if len(bots) > 1 {
			ipFlags = append(ipFlags, map[string]interface{}{
				"ip":        ip,
				"bot_count": len(bots),
				"bots":      bots,
				"severity":  "info",
				"message":   fmt.Sprintf("%d bots connected from same IP; review only, common for demos/NAT", len(bots)),
			})
		}
	}

	// Sort by risk score descending
	for i := 0; i < len(flaggedBots); i++ {
		for j := i + 1; j < len(flaggedBots); j++ {
			if flaggedBots[j].RiskScore > flaggedBots[i].RiskScore {
				flaggedBots[i], flaggedBots[j] = flaggedBots[j], flaggedBots[i]
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"flagged_bots":  flaggedBots,
		"flagged_count": len(flaggedBots),
		"total_scanned": len(allBots),
		"clean_count":   len(allBots) - len(flaggedBots),
		"ip_flags":      ipFlags,
		"scan_time":     time.Now(),
	})
}
