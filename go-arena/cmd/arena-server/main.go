package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"arena-server/internal/api"
	"arena-server/internal/config"
	"arena-server/internal/db"
	"arena-server/internal/game"
	"arena-server/internal/security"
	"arena-server/internal/ws"

	"github.com/jackc/pgx/v5"
)

type commandMode string

const (
	commandServe   commandMode = "serve"
	commandMigrate commandMode = "migrate"
)

var databaseRolePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]{0,62}$`)

type schemaQueryer interface {
	QueryRow(context.Context, string, ...interface{}) pgx.Row
}

const managedSchemaPreflightQuery = `
	WITH required(table_name, column_name) AS (
		VALUES
			('api_keys', 'id'),
			('bots', 'id'),
			('bot_stats', 'bot_id'),
			('rounds', 'persisted_order'),
			('kill_log', 'id'),
			('rate_limits', 'ip_address'),
			('weapon_balance', 'revision'),
			('weapon_balance', 'algorithm_version'),
			('weapon_balance_history', 'revision'),
			('weapon_balance_history', 'algorithm_version'),
			('bounty_board', 'bot_id'),
			('round_bot_stats', 'round_id'),
			('round_bot_stats', 'weapon'),
			('round_bot_stats', 'longest_life_secs'),
			('round_bot_stats', 'shots_fired'),
			('round_bot_stats', 'shots_hit'),
			('admin_tokens', 'id'),
			('admin_content_blocks', 'key'),
			('custom_map_templates', 'name'),
			('admin_runtime_overrides', 'scope'),
			('service_notice_events', 'id'),
			('cosmetic_categories', 'id'),
			('cosmetic_categories', 'is_builtin'),
			('cosmetic_items', 'id'),
			('cosmetic_items', 'category_id'),
			('cosmetic_items', 'is_builtin'),
			('cosmetic_items', 'sort_order'),
			('cosmetic_packs', 'id'),
			('cosmetic_packs', 'is_builtin'),
			('cosmetic_pack_items', 'pack_id'),
			('cosmetic_catalog_audit', 'id'),
			('cosmetic_entitlements', 'bot_id'),
			('customer_accounts', 'id'),
			('customer_email_verifications', 'email'),
			('customer_email_verifications', 'token_hash'),
			('customer_email_verifications', 'expires_at'),
			('account_bot_links', 'account_id'),
			('account_api_keys', 'account_id'),
			('cosmetic_licenses', 'id'),
			('cosmetic_license_assignments', 'license_id'),
			('bot_cosmetic_loadout', 'license_id'),
			('bot_cosmetic_loadout', 'account_id'),
			('cosmetic_orders', 'account_id'),
			('cosmetic_orders', 'pack_description'),
			('cosmetic_orders', 'expected_subtotal_cents'),
			('cosmetic_orders', 'cumulative_charge_refunded_cents'),
			('cosmetic_orders', 'stripe_checkout_session_id'),
			('cosmetic_orders', 'stripe_payment_intent_id'),
			('cosmetic_order_items', 'item_id'),
			('cosmetic_order_licenses', 'license_id'),
			('cosmetic_payment_events', 'payload_hash'),
			('cosmetic_order_refunds', 'refund_id'),
			('cosmetic_subscriptions', 'id'),
			('cosmetic_subscriptions', 'stripe_subscription_id'),
			('cosmetic_subscriptions', 'last_provider_event_created_at'),
			('cosmetic_subscriptions', 'last_provider_state_observed_at'),
			('cosmetic_subscription_licenses', 'license_id'),
			('cosmetic_subscription_events', 'payload_hash'),
			('cosmetic_admin_memberships', 'id'),
			('cosmetic_admin_memberships', 'account_id'),
			('cosmetic_admin_memberships', 'status'),
			('cosmetic_admin_memberships', 'expires_at'),
			('cosmetic_admin_memberships', 'granted_by'),
			('cosmetic_admin_membership_licenses', 'membership_id'),
			('cosmetic_admin_membership_licenses', 'item_id'),
			('cosmetic_admin_membership_licenses', 'license_id'),
			('platform_account_metadata', 'account_id'),
			('platform_account_metadata', 'status'),
			('platform_account_metadata', 'maximum_agents'),
			('platform_account_metadata', 'revision'),
			('platform_account_metadata', 'created_at'),
			('platform_account_metadata', 'updated_at'),
			('platform_agents', 'agent_id'),
			('platform_agents', 'registration_source'),
			('platform_agents', 'status'),
			('platform_agents', 'revision'),
			('platform_agents', 'created_at'),
			('platform_agents', 'updated_at'),
			('platform_game_profiles', 'profile_id'),
			('platform_game_profiles', 'agent_id'),
			('platform_game_profiles', 'game'),
			('platform_game_profiles', 'status'),
			('platform_game_profiles', 'revision'),
			('platform_game_profiles', 'enrolled_at'),
			('platform_game_profiles', 'updated_at'),
			('platform_changes', 'change_id'),
			('platform_changes', 'subject_kind'),
			('platform_changes', 'subject_id'),
			('platform_changes', 'transition'),
			('platform_changes', 'revision'),
			('platform_changes', 'changed_at'),
			('platform_idempotency_records', 'operation'),
			('platform_idempotency_records', 'idempotency_key'),
			('platform_idempotency_records', 'request_hash'),
			('platform_idempotency_records', 'response'),
			('platform_idempotency_records', 'subject_kind'),
			('platform_idempotency_records', 'subject_id'),
			('platform_idempotency_records', 'revision'),
			('platform_idempotency_records', 'created_at'),
			('platform_agent_link_events', 'event_id'),
			('platform_agent_link_events', 'account_id'),
			('platform_agent_link_events', 'agent_id'),
			('platform_agent_link_events', 'status'),
			('platform_agent_link_events', 'revision'),
			('platform_agent_link_events', 'reason'),
			('platform_agent_link_events', 'occurred_at')
	), missing AS (
		SELECT required.table_name, required.column_name
		FROM required
		LEFT JOIN information_schema.columns AS columns
		  ON columns.table_schema = 'public'
		 AND columns.table_name = required.table_name
		 AND columns.column_name = required.column_name
		WHERE columns.column_name IS NULL
	)
	SELECT COALESCE(string_agg(table_name || '.' || column_name, ', ' ORDER BY table_name, column_name), '')
	FROM missing`

func main() {
	// Load configuration from environment variables.
	config.Load()
	mode, err := parseCommand(os.Args[1:])
	if err != nil {
		slog.Error("invalid command", "error", err)
		os.Exit(2)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if mode == commandMigrate {
		if err := runDatabaseMigrations(ctx); err != nil {
			slog.Error("database migration failed", "error", err)
			os.Exit(1)
		}
		slog.Info("database migration completed")
		return
	}

	// Connect to the database. By default the DB is required (auth and
	// persistence depend on it): if it is still unreachable after retries we
	// exit non-zero so the container restart policy retries from a clean
	// start, rather than silently running in a broken degraded mode where
	// every keyed bot join fails (the 2026-05-29 incident). Set
	// ARENA_DB_OPTIONAL=true to allow running without a database.
	if err := db.Connect(ctx); err != nil {
		if !config.C.DBOptional {
			slog.Error("database required but unavailable after retries; exiting for restart", "error", err)
			os.Exit(1)
		}
		slog.Warn("database not available, running without persistence (ARENA_DB_OPTIONAL=true)", "error", err)
	}
	defer db.Close()
	if db.Pool != nil {
		runtimeLease, err := db.AcquireRuntimeLease(ctx)
		if err != nil {
			slog.Error("failed to acquire the database runtime lease; refusing to start", "error", err)
			os.Exit(1)
		}
		defer func() {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer closeCancel()
			if err := runtimeLease.Close(closeCtx); err != nil {
				slog.Warn("failed to release database runtime lease cleanly", "error", err)
			}
		}()
		slog.Info("acquired database runtime lease")
		go func() {
			if err := runtimeLease.Monitor(ctx); err != nil {
				slog.Error("database runtime lease lost; shutting down", "error", err)
				cancel()
			}
		}()
	}

	// Local/default deployments retain automatic startup migrations. Managed
	// production deployments run migrations through the updater under the DB
	// owner and keep the runtime role DDL-free. Both paths fail closed on a
	// migration error or a stale schema instead of starting partially broken.
	if db.Pool != nil {
		if !config.ShouldAutoMigrateDatabase() {
			slog.Info("database migrations are managed externally; skipping runtime DDL")
		} else if err := db.EnsureCoreSchema(ctx); err != nil {
			slog.Error("failed to migrate database schema; refusing to start", "error", err)
			os.Exit(1)
		}
		if err := verifyManagedSchema(ctx, db.Pool); err != nil {
			slog.Error("database schema preflight failed; refusing to start", "error", err)
			os.Exit(1)
		}
		// No game engine exists yet, so any active row belongs to a previous
		// process that stopped before it could publish a natural round result.
		// Reconcile before this runtime is capable of creating its first round.
		if changed, err := db.InterruptActiveRounds(ctx); err != nil {
			slog.Error("failed to reconcile interrupted rounds; refusing to start", "error", err)
			os.Exit(1)
		} else if changed > 0 {
			slog.Info("reconciled interrupted rounds", "changed_rows", changed)
		}
		minElo, maxElo := config.EloBounds()
		if changed, err := db.NormalizeEloRatings(ctx, minElo, maxElo); err != nil {
			slog.Warn("failed to normalize persisted Elo ratings", "error", err)
		} else if changed > 0 {
			slog.Info("normalized persisted Elo ratings", "changed_rows", changed, "min", minElo, "max", maxElo)
		}
		if err := api.LoadPersistedGameConfigOverrides(ctx); err != nil {
			slog.Warn("failed to load persisted admin game config", "error", err)
		}
	}

	// Initialise Redis for rate limiting. General routes degrade gracefully;
	// email delivery and checkout fail closed until Redis is available.
	if err := security.InitRedis(); err != nil {
		slog.Warn("redis rate limiting initialisation failed", "error", err)
	}

	// Initialise grid-based weapon ranges from config cell size.
	game.InitWeaponRanges(config.C.PathfindingCellSize)
	if db.Pool != nil {
		if err := api.LoadPersistedWeaponOverrides(ctx); err != nil {
			slog.Warn("failed to load persisted admin weapon overrides", "error", err)
		}
	}
	if err := game.LoadWeaponBalance(ctx); err != nil {
		slog.Warn("failed to load weapon balance state", "error", err)
	}

	// Create the game engine. It starts after the router captures the immutable
	// startup configuration used by restart-staged admin settings.
	engine := game.NewGameEngine()
	if db.Pool != nil {
		if entries, err := db.ListBountyBoardEntries(ctx); err != nil {
			slog.Warn("failed to load bounty board state", "error", err)
		} else if len(entries) > 0 {
			engine.RestoreBountyBoard(entries)
			slog.Info("restored bounty board state", "entries", len(entries))
		} else if seed, err := db.GetLatestWinnerBountySeed(
			ctx,
			config.C.BountyWinStreakThreshold,
			config.C.BountyBoardBasePoints,
			config.C.BountyBoardStepPoints,
			config.C.BountyBoardMaxPoints,
		); err != nil {
			slog.Warn("failed to seed bounty board from recent rounds", "error", err)
		} else if seed != nil {
			engine.RestoreBountyBoard([]db.BountyBoardEntry{*seed})
			slog.Info("seeded bounty board from recent winners", "bot", seed.Name, "streak", seed.WinStreak)
		}
	}
	// Build the HTTP router. Demo bots run as an external process (private
	// repo) speaking the public bot protocol; the server has no embedded fleet.
	var routerOpts []api.RouterOption
	routerOpts = append(routerOpts, api.WithShutdown(cancel), api.WithNightlyRestart(ctx))
	router := api.NewRouter(engine, routerOpts...)

	// Wire up event hooks for dashboard logging.
	ws.EventHook = func(action, botName, botID, ip, apiKeyID, errMsg string, details map[string]interface{}) {
		api.EmitConnection(api.GlobalEventBus, action, botName, botID, ip, apiKeyID, errMsg, details)
	}
	ws.WSMessageHook = func(botID, botName, action string, data map[string]interface{}) {
		api.EmitWSMessage(api.GlobalEventBus, botID, botName, action, data)
	}
	game.GameEventHook = func(eventName string, data map[string]interface{}) {
		api.EmitGameEvent(api.GlobalEventBus, eventName, data)
	}
	if db.Pool != nil {
		go api.RunCosmeticAdminMembershipExpiryLoop(ctx, engine)
	}
	go engine.Run(ctx)

	// Start the HTTP server.
	addr := fmt.Sprintf("%s:%d", config.C.ServerHost, config.C.ServerPort)
	slog.Info("starting arena server", "addr", addr)

	srv := &http.Server{
		Addr:    addr,
		Handler: router,
		// ReadHeaderTimeout bounds slow-header (Slowloris-style) connections.
		// It only covers the time to read request headers before the handler
		// runs, so it does not affect long-lived WebSocket connections, which
		// hijack the raw net.Conn and manage their own read/write deadlines.
		ReadHeaderTimeout: 10 * time.Second,
		// IdleTimeout only applies to keep-alive HTTP connections sitting
		// between requests, not to hijacked WebSocket connections.
		IdleTimeout: 120 * time.Second,
	}

	// Start demo bots after server setup.

	// Graceful shutdown on SIGINT / SIGTERM or the authenticated admin restart
	// endpoint. WebSockets are hijacked from net/http, so explicitly notify and
	// close them before asking the HTTP server to drain.
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		slog.Info("shutting down...")
		engine.NotifyServiceRestart(60)
		time.Sleep(350 * time.Millisecond)

		// Stop demo bots first so they disconnect cleanly.
		engine.CloseAllWebSockets("service restart; retry in about 60 seconds")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("HTTP shutdown did not fully drain", "error", err)
		}
	}()

	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
	if ctx.Err() != nil {
		<-shutdownDone
	}
}

func parseCommand(args []string) (commandMode, error) {
	if len(args) == 0 {
		return commandServe, nil
	}
	if len(args) == 1 && args[0] == string(commandMigrate) {
		return commandMigrate, nil
	}
	return "", fmt.Errorf("usage: arena-server [migrate]")
}

func runDatabaseMigrations(ctx context.Context) error {
	if err := db.Connect(ctx); err != nil {
		return fmt.Errorf("connect as migrator: %w", err)
	}
	defer db.Close()
	if db.Pool == nil {
		return fmt.Errorf("connect as migrator returned no database pool")
	}
	if err := db.EnsureCoreSchema(ctx); err != nil {
		return fmt.Errorf("apply core schema: %w", err)
	}

	runtimeUser := config.C.DBRuntimeUser
	if runtimeUser == "" {
		// Direct/local migrations normally use one owner/runtime role. The
		// updater always supplies ARENA_RUNTIME_DB_USER explicitly.
		runtimeUser = config.C.DBUser
	}
	if err := grantRuntimeDatabasePrivileges(ctx, runtimeUser); err != nil {
		return err
	}
	if err := verifyManagedSchema(ctx, db.Pool); err != nil {
		return fmt.Errorf("post-migration schema preflight: %w", err)
	}
	return nil
}

func verifyManagedSchema(ctx context.Context, queryer schemaQueryer) error {
	var missing string
	if err := queryer.QueryRow(ctx, managedSchemaPreflightQuery).Scan(&missing); err != nil {
		return fmt.Errorf("query required schema: %w", err)
	}
	if missing != "" {
		return fmt.Errorf("required database schema is stale or inaccessible; missing: %s", missing)
	}
	return nil
}

func runtimePrivilegeStatements(runtimeUser string) ([]string, error) {
	if !databaseRolePattern.MatchString(runtimeUser) {
		return nil, fmt.Errorf("ARENA_RUNTIME_DB_USER must be a conventional PostgreSQL role name")
	}
	role := pgx.Identifier{runtimeUser}.Sanitize()
	return []string{
		fmt.Sprintf("GRANT USAGE ON SCHEMA public TO %s", role),
		fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %s", role),
		fmt.Sprintf("GRANT TRUNCATE ON TABLE public.bot_stats, public.round_bot_stats TO %s", role),
		fmt.Sprintf("GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %s", role),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s", role),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO %s", role),
	}, nil
}

func grantRuntimeDatabasePrivileges(ctx context.Context, runtimeUser string) error {
	if runtimeUser == config.C.DBUser {
		// The simple local/default deployment uses one owner/runtime role, which
		// already owns every object and needs no grants back to itself.
		return nil
	}
	statements, err := runtimePrivilegeStatements(runtimeUser)
	if err != nil {
		return err
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin runtime privilege grant: %w", err)
	}
	defer tx.Rollback(ctx)

	var roleExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, runtimeUser).Scan(&roleExists); err != nil {
		return fmt.Errorf("look up runtime database role: %w", err)
	}
	if !roleExists {
		return fmt.Errorf("runtime database role %q does not exist", runtimeUser)
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return fmt.Errorf("grant runtime database privileges: %w", err)
		}
	}

	var missingPrivileges string
	if err := tx.QueryRow(ctx, `
		WITH missing AS (
			SELECT 'table ' || quote_ident(schemaname) || '.' || quote_ident(tablename) AS object_name
			FROM pg_tables
			WHERE schemaname = 'public'
			  AND NOT (
				has_table_privilege($1, format('%I.%I', schemaname, tablename), 'SELECT') AND
				has_table_privilege($1, format('%I.%I', schemaname, tablename), 'INSERT') AND
				has_table_privilege($1, format('%I.%I', schemaname, tablename), 'UPDATE') AND
				has_table_privilege($1, format('%I.%I', schemaname, tablename), 'DELETE') AND
				(tablename NOT IN ('bot_stats', 'round_bot_stats') OR
				 has_table_privilege($1, format('%I.%I', schemaname, tablename), 'TRUNCATE'))
			  )
			UNION ALL
			SELECT 'sequence ' || quote_ident(sequence_schema) || '.' || quote_ident(sequence_name)
			FROM information_schema.sequences
			WHERE sequence_schema = 'public'
			  AND NOT (
				has_sequence_privilege($1, format('%I.%I', sequence_schema, sequence_name), 'USAGE') AND
				has_sequence_privilege($1, format('%I.%I', sequence_schema, sequence_name), 'SELECT')
			  )
		)
		SELECT COALESCE(string_agg(object_name, ', ' ORDER BY object_name), '')
		FROM missing`, runtimeUser).Scan(&missingPrivileges); err != nil {
		return fmt.Errorf("verify runtime database privileges: %w", err)
	}
	if missingPrivileges != "" {
		return fmt.Errorf("runtime database role %q lacks required privileges on %s", runtimeUser, missingPrivileges)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit runtime database privileges: %w", err)
	}
	return nil
}
