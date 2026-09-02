package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"

	"arena-server/internal/config"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool is the global database connection pool.
var Pool *pgxpool.Pool

// ErrNoDatabase is returned by query helpers when the connection pool has
// not been initialised (db.Connect failed or was never called). Returning
// this instead of dereferencing a nil pool prevents the nil-pointer panic
// that took the keyed bot-join path down in the 2026-05-29 incident.
var ErrNoDatabase = errors.New("database connection pool is not initialized")

// ErrRuntimeLeaseHeld means another Arena server process is already serving
// this database. A singleton runtime is required because one engine owns the
// authoritative match clock and performs whole-board snapshot persistence.
var ErrRuntimeLeaseHeld = errors.New("another arena server runtime already holds the database lease")

// This key is a stable namespace reserved for the Arena server runtime. Schema
// migrations use a different transaction-scoped key.
const arenaRuntimeAdvisoryLockID int64 = 2026071102

const (
	runtimeLeaseHeartbeatInterval = time.Second
	runtimeLeaseHeartbeatTimeout  = 2 * time.Second
)

// RuntimeLease owns one dedicated pool connection for the lifetime of a server
// process. PostgreSQL session advisory locks are bound to that connection.
type RuntimeLease struct {
	mu   sync.Mutex
	conn *pgxpool.Conn
}

// AcquireRuntimeLease prevents two game engines from writing rounds and global
// snapshot state to the same database at once.
func AcquireRuntimeLease(ctx context.Context) (*RuntimeLease, error) {
	if Pool == nil {
		return nil, ErrNoDatabase
	}
	conn, err := Pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("AcquireRuntimeLease acquire: %w", err)
	}
	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, arenaRuntimeAdvisoryLockID).Scan(&acquired); err != nil {
		// The query may have reached PostgreSQL before its result failed to scan.
		// Destroy the session so a possibly acquired session lock cannot leak
		// back into the pool.
		raw := conn.Hijack()
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = raw.Close(closeCtx)
		return nil, fmt.Errorf("AcquireRuntimeLease lock: %w", err)
	}
	if !acquired {
		conn.Release()
		return nil, ErrRuntimeLeaseHeld
	}
	return &RuntimeLease{conn: conn}, nil
}

// Close explicitly releases the runtime lease, then closes its dedicated
// PostgreSQL session. It is idempotent; destroying instead of returning the
// connection to the pool guarantees a session-scoped lock cannot leak even if
// the unlock result is lost.
func (lease *RuntimeLease) Close(ctx context.Context) error {
	if lease == nil {
		return nil
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.conn == nil {
		return nil
	}
	var unlocked bool
	unlockErr := lease.conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, arenaRuntimeAdvisoryLockID).Scan(&unlocked)
	raw := lease.conn.Hijack()
	lease.conn = nil
	closeErr := raw.Close(ctx)
	if unlockErr != nil {
		return fmt.Errorf("RuntimeLease unlock: %w", unlockErr)
	}
	if !unlocked {
		return errors.New("RuntimeLease unlock: advisory lock was not held by its dedicated session")
	}
	if closeErr != nil {
		return fmt.Errorf("RuntimeLease close: %w", closeErr)
	}
	return nil
}

// Monitor verifies that the dedicated PostgreSQL session remains usable. A
// session-level advisory lock disappears if that connection dies, even when
// the process can still use other pooled connections; callers must shut the
// runtime down on a non-nil result so another server can never overlap it.
func (lease *RuntimeLease) Monitor(ctx context.Context) error {
	ticker := time.NewTicker(runtimeLeaseHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			checkCtx, cancel := context.WithTimeout(ctx, runtimeLeaseHeartbeatTimeout)
			err := lease.check(checkCtx)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("RuntimeLease heartbeat: %w", err)
			}
		}
	}
}

func (lease *RuntimeLease) check(ctx context.Context) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.conn == nil {
		return errors.New("runtime lease is closed")
	}
	var alive int
	if err := lease.conn.QueryRow(ctx, `SELECT 1`).Scan(&alive); err != nil {
		return err
	}
	if alive != 1 {
		return fmt.Errorf("unexpected heartbeat value %d", alive)
	}
	return nil
}

// Connect establishes the global pgx pool from config.C. It retries on
// failure (config.C.DBConnectAttempts, spaced by DBConnectRetrySeconds) so a
// database that is still coming up after a host or Docker daemon restart does
// not permanently leave the server without persistence -- the failure mode
// behind the 2026-05-29 bot-join outage.
func Connect(ctx context.Context) error {
	delay := time.Duration(config.C.DBConnectRetrySeconds) * time.Second

	pool, err := connectWithRetry(ctx, config.C.DBConnectAttempts, delay, connectOnce)
	if err != nil {
		return err
	}

	Pool = pool
	slog.Info("database connected",
		"host", config.C.DBHost,
		"port", config.C.DBPort,
		"db", config.C.DBName,
	)
	return nil
}

// connectionString builds the PostgreSQL URL from the configuration.
//
// The user and password are escaped as URL components rather than pasted in.
// The old fmt.Sprintf meant a password containing "/", "#", "?" or a space —
// which is most of what `openssl rand -base64` produces, and exactly what
// warnInsecureDefaults asks operators to set — failed to parse, burned every
// connection retry and stopped the server with "invalid port" pointing at the
// wrong thing. sslmode comes from ARENA_DB_SSLMODE so a managed or remote
// database can require TLS; the compose default stays "disable".
func connectionString(cfg config.Config) string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(cfg.DBUser, cfg.DBPassword),
		Host:     net.JoinHostPort(cfg.DBHost, strconv.Itoa(cfg.DBPort)),
		Path:     "/" + cfg.DBName,
		RawQuery: url.Values{"sslmode": {cfg.DBSSLMode}}.Encode(),
	}
	return u.String()
}

// connectOnce performs a single connect-and-ping attempt and returns the
// resulting pool. It is the unit of work retried by connectWithRetry.
func connectOnce(ctx context.Context) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(connectionString(config.C))
	if err != nil {
		return nil, fmt.Errorf("failed to parse connection config: %w", err)
	}
	poolCfg.MaxConns = int32(config.C.DBPoolSize + config.C.DBMaxOverflow)
	poolCfg.MinConns = 5

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return pool, nil
}

// connectWithRetry calls connectFn up to attempts times, waiting delay between
// attempts, until it succeeds or the context is cancelled. It is factored out
// from Connect so the retry policy can be unit-tested without a real database.
func connectWithRetry(
	ctx context.Context,
	attempts int,
	delay time.Duration,
	connectFn func(context.Context) (*pgxpool.Pool, error),
) (*pgxpool.Pool, error) {
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		pool, err := connectFn(ctx)
		if err == nil {
			return pool, nil
		}
		lastErr = err
		slog.Warn("database connect attempt failed",
			"attempt", i+1, "of", attempts, "error", err)
	}
	return nil, lastErr
}

// Close closes the database connection pool.
func Close() {
	if Pool != nil {
		Pool.Close()
		slog.Info("database connection closed")
	}
}
