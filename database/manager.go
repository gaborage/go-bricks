package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/resourcepool"
	"github.com/gaborage/go-bricks/logger"
)

// DBConfigProvider provides per-key database configurations.
// This interface abstracts where tenant-specific database configs come from.
type DBConfigProvider interface {
	// DBConfig returns the database configuration for the given key.
	// For single-tenant apps, key will be "". For multi-tenant, key will be the tenant ID.
	// An implementation MUST return either a non-nil configuration or a non-nil error;
	// the manager rejects a (nil, nil) result with ErrNoDatabaseConfig.
	DBConfig(ctx context.Context, key string) (*config.DatabaseConfig, error)
}

// Connector creates database connections from configuration
type Connector func(*config.DatabaseConfig, logger.Logger) (Interface, error)

// ReleaseFunc releases a lease obtained from Get. Callers must invoke it (typically
// deferred) when they are finished with the connection for the current unit of work.
// It is idempotent: calling it more than once is a safe no-op. The connection itself
// is a long-lived, shared pool — Release does NOT close it; it only signals that this
// borrower is done, so a connection evicted while leased can be closed once its last
// lease is released. See ADR-032.
type ReleaseFunc func()

// errManagerClosed is returned by Get after Close has been called, rather than
// resurrecting a connection on a shut-down manager (backlog F22). It is unexported:
// DbManager exposed no closed-state error before the resourcepool rewire, so this
// closes F22 while keeping the public surface unchanged.
var errManagerClosed = errors.New("database: manager closed")

// ErrNoDatabaseConfig is returned when a DBConfigProvider hands back a nil configuration
// with a nil error, a contract violation the manager rejects instead of dereferencing.
var ErrNoDatabaseConfig = errors.New("database: config provider returned no configuration")

// DbManager manages database connections by string keys.
// It provides lazy initialization, LRU eviction, and cleanup for database connections.
// The manager is key-agnostic - it doesn't know about tenants, just manages named connections.
//
// It is a thin adapter over internal/resourcepool.Pool, which owns the ADR-032
// lease/evict/close protocol (seed leases, LRU eviction, idle cleanup, and the
// closed-pool guard). The manager keeps only the database-specific config resolution.
type DbManager struct {
	pool           *resourcepool.Pool[Interface]
	logger         logger.Logger
	resourceSource DBConfigProvider
	connector      Connector // Injected for testability
}

// DbManagerOptions configures the DbManager
type DbManagerOptions struct {
	MaxSize int           // Cached-connection cap; <=0 uses a default (not unlimited).
	IdleTTL time.Duration // Idle-connection lifetime; <=0 uses a default (not disabled).
	// CleanupInterval is how often the idle sweep runs; <=0 uses the documented 5-minute
	// default. The manager starts that sweep itself at construction (ADR-067).
	CleanupInterval time.Duration
}

// NewDbManager creates a new database manager. The idle-cleanup sweep starts here and stops in
// Close, so callers need not drive it (ADR-067); StartCleanup remains available and idempotent.
func NewDbManager(resourceSource DBConfigProvider, log logger.Logger, opts DbManagerOptions, connector Connector) *DbManager {
	if opts.MaxSize <= 0 {
		opts.MaxSize = 100 // sensible default
	}
	if opts.IdleTTL <= 0 {
		opts.IdleTTL = 30 * time.Minute // sensible default
	}
	if opts.CleanupInterval <= 0 {
		opts.CleanupInterval = config.DefaultDatabaseManagerCleanupInterval
	}

	// Default to real connection factory if none provided
	if connector == nil {
		connector = NewConnection
	}

	m := &DbManager{
		logger:         log,
		resourceSource: resourceSource,
		connector:      connector,
		pool: resourcepool.New[Interface](opts.MaxSize, opts.IdleTTL, func(conn Interface) error {
			return conn.Close()
		}),
	}

	resourcepool.WarnIfCleanupIntervalTooLate(log, "database.manager", opts.CleanupInterval, opts.IdleTTL)
	m.pool.StartCleanup(opts.CleanupInterval)

	return m
}

// Get returns a database connection for the given key plus a ReleaseFunc the caller must
// invoke when finished with it for the current unit of work (typically deferred). For
// single-tenant, use key "". For multi-tenant, use the tenant ID. Connections are created
// lazily and cached with LRU eviction; the lease prevents a connection that is evicted
// while in use from being closed under an active caller (the #606 race). Once Close has
// run, Get fails closed rather than resurrecting a connection (F22) — except a caller
// already mid-Get on a fresh connection another borrower holds, who may still receive
// that live handle after Close returns; it closes exactly once, at its final release.
// On error the returned ReleaseFunc is nil — check err first. A provider that breaks the
// DBConfigProvider contract by returning (nil, nil) yields an error a caller can match
// with errors.Is against ErrNoDatabaseConfig.
func (m *DbManager) Get(ctx context.Context, key string) (Interface, ReleaseFunc, error) {
	if m.pool == nil {
		// Zero-value manager (never built via NewDbManager): unusable, fail closed rather
		// than panic — consistent with the Stats()/Close()/Size() zero-value guards.
		return nil, nil, errManagerClosed
	}
	conn, release, err := m.pool.GetOrCreate(ctx, key, func(ctx context.Context) (Interface, error) {
		return m.createConnection(ctx, key)
	})
	if err != nil {
		if errors.Is(err, resourcepool.ErrPoolClosed) {
			return nil, nil, errManagerClosed
		}
		return nil, nil, err
	}
	return conn, ReleaseFunc(release), nil
}

// createConnection resolves the per-key database configuration and opens a new connection.
// It applies the standard pool defaults to a shallow clone of the provider's config so a
// long-lived, shared provider config stays pristine, then hands off to the injected
// connector. It performs only config resolution and connection opening — the pool owns all
// lease/LRU/eviction bookkeeping. Invoked inside the pool's create callback, so singleflight
// guarantees one call per key per creation.
func (m *DbManager) createConnection(ctx context.Context, key string) (Interface, error) {
	dbConfig, err := m.resourceSource.DBConfig(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("failed to get database config for key %s: %w", key, err)
	}
	if dbConfig == nil {
		return nil, fmt.Errorf("%w for key %s", ErrNoDatabaseConfig, key)
	}
	// Shallow-clone before defaulting: providers may return long-lived shared configs.
	cfgCopy := *dbConfig
	// No wrapping: the seam addresses the error to this key's section itself, and a wrap
	// naming the key again would print the same identity twice (ADR-076 addendum).
	if defaultsErr := config.ApplyDatabasePoolDefaultsForKey(&cfgCopy, key); defaultsErr != nil {
		return nil, defaultsErr
	}

	conn, err := m.connector(&cfgCopy, m.logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create database connection for key %s: %w", key, err)
	}

	m.logger.Info().
		Str("key", key).
		Str("db_type", cfgCopy.Type).
		Msg("Created new database connection")

	return conn, nil
}

// StartCleanup starts the background cleanup routine for idle connections. A non-positive
// interval substitutes the documented 5-minute default. The constructor already started a
// sweep, so this is a no-op unless StopCleanup ran first (the pool's loop is single-instance).
func (m *DbManager) StartCleanup(interval time.Duration) {
	if m.pool == nil {
		return // zero-value manager: nothing to run, consistent with the other nil-pool guards
	}
	if interval <= 0 {
		interval = config.DefaultDatabaseManagerCleanupInterval
	}
	m.pool.StartCleanup(interval)
}

// StopCleanup stops the background cleanup routine
func (m *DbManager) StopCleanup() {
	if m.pool == nil {
		return // zero-value manager: nothing to stop
	}
	m.pool.StopCleanup()
}

// Close closes all database connections and stops the idle-cleanup sweep the constructor
// started. A connection still borrowed by in-flight work is closed at its final release
// instead of by this call (wiki/migrations.md C581.3).
func (m *DbManager) Close() error {
	if m.pool == nil {
		return nil // zero-value manager (never built via NewDbManager): nothing to close
	}
	// pool.Close joins every per-connection close error; preserve DbManager's original
	// aggregate contract (all failures surfaced under one prefix), matching the pre-resourcepool
	// "errors closing database connections: %v" wrapping.
	if err := m.pool.Close(); err != nil {
		return fmt.Errorf("errors closing database connections: %w", err)
	}
	return nil
}

// Size returns the number of active connections
func (m *DbManager) Size() int {
	if m.pool == nil {
		return 0 // zero-value manager: preserve the original len(nil-map) == 0 behavior
	}
	return m.pool.Size()
}

// Stats returns statistics about the connection pool
func (m *DbManager) Stats() map[string]any {
	if m.pool == nil {
		// A zero-value DbManager (not built via NewDbManager, e.g. a lightweight test
		// stand-in) reports empty stats rather than panicking, preserving the
		// pre-resourcepool behavior the debug/health endpoint relies on.
		return map[string]any{
			"active_connections": 0,
			"max_connections":    0,
			"idle_ttl_seconds":   0,
			"errors":             0,
			"connections":        []map[string]any{},
		}
	}

	ps := m.pool.Stats()

	stats := map[string]any{
		"active_connections": ps.Size,
		"max_connections":    ps.MaxSize,
		"idle_ttl_seconds":   int(ps.IdleTTL.Seconds()),
		// Pool create/close failures (including a deferred close on a handle still borrowed
		// when Close ran, C581.3) — otherwise unobservable outside this Stats() call.
		"errors": ps.Errors,
	}

	// Rebuild the per-connection detail array from the pool's entry snapshot so the shape
	// (key, RFC3339 last_used, idle_duration seconds) is preserved for Stats() consumers such
	// as the debug/health endpoint.
	snap := m.pool.Snapshot()
	connections := make([]map[string]any, 0, len(snap))
	now := time.Now()
	for _, e := range snap {
		connections = append(connections, map[string]any{
			"key":           e.Key,
			"last_used":     e.LastUsed.Format(time.RFC3339),
			"idle_duration": int(now.Sub(e.LastUsed).Seconds()),
		})
	}
	stats["connections"] = connections

	return stats
}
