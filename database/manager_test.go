package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
)

const (
	tenantA = "tenant-a"
)

type stubResourceSource struct {
	configs map[string]*config.DatabaseConfig
}

func (s *stubResourceSource) DBConfig(_ context.Context, key string) (*config.DatabaseConfig, error) {
	if cfg, ok := s.configs[key]; ok {
		return cfg, nil
	}
	return &config.DatabaseConfig{Type: "postgresql", Host: "localhost"}, nil
}

type failingResourceSource struct {
	err error
}

func (f *failingResourceSource) DBConfig(context.Context, string) (*config.DatabaseConfig, error) {
	return nil, f.err
}

type stubStatement struct{}

func (s *stubStatement) Query(_ context.Context, _ ...any) (*sql.Rows, error) { return nil, nil }
func (s *stubStatement) QueryRow(_ context.Context, _ ...any) types.Row       { return nil }
func (s *stubStatement) Exec(_ context.Context, _ ...any) (sql.Result, error) { return nil, nil }
func (s *stubStatement) Close() error                                         { return nil }

type stubTx struct{}

func (s *stubTx) Query(_ context.Context, _ string, _ ...any) (*sql.Rows, error) { return nil, nil }
func (s *stubTx) QueryRow(_ context.Context, _ string, _ ...any) types.Row       { return nil }
func (s *stubTx) Exec(_ context.Context, _ string, _ ...any) (sql.Result, error) { return nil, nil }
func (s *stubTx) Prepare(_ context.Context, _ string) (Statement, error) {
	return &stubStatement{}, nil
}
func (s *stubTx) Commit(_ context.Context) error   { return nil }
func (s *stubTx) Rollback(_ context.Context) error { return nil }

type stubDB struct {
	key      string
	closedMu sync.Mutex
	closed   bool
	closeErr error
	onClosed func(string)
}

func (s *stubDB) Query(_ context.Context, _ string, _ ...any) (*sql.Rows, error) { return nil, nil }
func (s *stubDB) QueryRow(_ context.Context, _ string, _ ...any) types.Row       { return nil }
func (s *stubDB) Exec(_ context.Context, _ string, _ ...any) (sql.Result, error) { return nil, nil }
func (s *stubDB) Prepare(_ context.Context, _ string) (Statement, error) {
	return &stubStatement{}, nil
}
func (s *stubDB) Begin(_ context.Context) (Tx, error)                     { return &stubTx{}, nil }
func (s *stubDB) BeginTx(_ context.Context, _ *sql.TxOptions) (Tx, error) { return &stubTx{}, nil }
func (s *stubDB) Health(_ context.Context) error                          { return nil }
func (s *stubDB) Stats() (map[string]any, error)                          { return map[string]any{"key": s.key}, nil }
func (s *stubDB) Close() error {
	s.closedMu.Lock()
	s.closed = true
	callback := s.onClosed
	key := s.key
	s.closedMu.Unlock()
	if callback != nil {
		callback(key)
	}
	return s.closeErr
}
func (s *stubDB) DatabaseType() string                       { return "stub" }
func (s *stubDB) MigrationTable() string                     { return "schema_migrations" }
func (s *stubDB) CreateMigrationTable(context.Context) error { return nil }

func TestDbManagerReturnsSameInstanceForSameKey(t *testing.T) {
	ctx := context.Background()
	log := newErrorTestLogger()

	connectorCalls := 0
	manager := NewDbManager(&stubResourceSource{configs: map[string]*config.DatabaseConfig{
		tenantA: {Type: "postgresql"},
	}}, log, DbManagerOptions{MaxSize: 5, IdleTTL: time.Minute}, func(cfg *config.DatabaseConfig, _ logger.Logger) (Interface, error) {
		connectorCalls++
		return &stubDB{key: cfg.Database}, nil
	})

	first, _, err := manager.Get(ctx, tenantA)
	require.NoError(t, err)
	second, _, err := manager.Get(ctx, tenantA)
	require.NoError(t, err)
	assert.Same(t, first, second)
	assert.Equal(t, 1, connectorCalls)
	assert.Equal(t, 1, manager.Size())
}

func TestDbManagerCloseClosesAllConnections(t *testing.T) {
	ctx := context.Background()
	log := newErrorTestLogger()

	var mu sync.Mutex
	evicted := []string{}
	connector := func(cfg *config.DatabaseConfig, _ logger.Logger) (Interface, error) {
		return &stubDB{key: cfg.Database, onClosed: func(key string) {
			mu.Lock()
			defer mu.Unlock()
			evicted = append(evicted, key)
		}}, nil
	}

	resource := &stubResourceSource{configs: map[string]*config.DatabaseConfig{
		"tenant-x": {Type: "postgresql", Database: "x"},
		"tenant-y": {Type: "postgresql", Database: "y"},
	}}

	manager := NewDbManager(resource, log, DbManagerOptions{MaxSize: 5, IdleTTL: time.Hour}, connector)
	_, _, err := manager.Get(ctx, "tenant-x")
	require.NoError(t, err)
	_, _, err = manager.Get(ctx, "tenant-y")
	require.NoError(t, err)

	err = manager.Close()
	require.NoError(t, err)
	assert.Equal(t, 0, manager.Size())

	mu.Lock()
	defer mu.Unlock()
	assert.ElementsMatch(t, []string{"x", "y"}, evicted)
}

// TestCreateConnectionReturnsErrorWhenConfigFails proves a config-resolution failure in the
// create callback surfaces through the public Get surface as a wrapped error.
func TestCreateConnectionReturnsErrorWhenConfigFails(t *testing.T) {
	ctx := context.Background()
	configErr := errors.New("config failure")
	manager := NewDbManager(&failingResourceSource{err: configErr}, newErrorTestLogger(), DbManagerOptions{}, nil)

	_, _, err := manager.Get(ctx, "tenant")
	require.Error(t, err)
	assert.ErrorContains(t, err, "failed to get database config")
}

// TestCreateConnectionPropagatesConnectorError proves a connector failure surfaces through
// the public Get surface as a wrapped error.
func TestCreateConnectionPropagatesConnectorError(t *testing.T) {
	ctx := context.Background()
	authErr := errors.New("connector failure")
	resource := &stubResourceSource{configs: map[string]*config.DatabaseConfig{"tenant": {Type: "postgresql"}}}
	connector := func(*config.DatabaseConfig, logger.Logger) (Interface, error) {
		return nil, authErr
	}
	manager := NewDbManager(resource, newErrorTestLogger(), DbManagerOptions{}, connector)

	_, _, err := manager.Get(ctx, "tenant")
	require.Error(t, err)
	assert.ErrorContains(t, err, "failed to create database connection")
}

// TestDbManagerGetAfterCloseReturnsError pins the F22 fix: once Close() has run, Get()
// fails closed (returning the manager's closed error) instead of resurrecting a
// connection on a shut-down manager. The resourcepool closed guard supplies this; before
// the rewire, DbManager.Get would silently create and leak a fresh connection.
func TestDbManagerGetAfterCloseReturnsError(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	closed := map[string]bool{}
	m := NewDbManager(twoTenantSource(), newErrorTestLogger(),
		DbManagerOptions{MaxSize: 5, IdleTTL: time.Minute}, newClosableDB(&mu, closed))
	require.NoError(t, m.Close())

	conn, release, err := m.Get(ctx, "a")
	require.Error(t, err)
	assert.ErrorIs(t, err, errManagerClosed, "Get after Close must fail closed, not resurrect a connection (F22)")
	assert.Nil(t, conn)
	assert.Nil(t, release)
	assert.Equal(t, 0, m.Size(), "no connection may be created on a closed manager")
}

// TestDbManagerCloseAggregatesErrors pins the aggregate Close contract: when MULTIPLE cached
// connections fail to close, Close surfaces EVERY failure (not just the first), under the
// historical "errors closing database connections" prefix. Black-box via Get + Close.
func TestDbManagerCloseAggregatesErrors(t *testing.T) {
	var n atomic.Int32
	connector := func(_ *config.DatabaseConfig, _ logger.Logger) (Interface, error) {
		id := n.Add(1)
		return &stubDB{key: fmt.Sprintf("k%d", id), closeErr: fmt.Errorf("close failure %d", id)}, nil
	}
	src := &stubResourceSource{configs: map[string]*config.DatabaseConfig{
		"a": {Type: "postgresql"},
		"b": {Type: "postgresql"},
	}}
	m := NewDbManager(src, newErrorTestLogger(), DbManagerOptions{MaxSize: 5, IdleTTL: time.Minute}, connector)

	ctx := context.Background()
	_, relA, err := m.Get(ctx, "a")
	require.NoError(t, err)
	relA()
	_, relB, err := m.Get(ctx, "b")
	require.NoError(t, err)
	relB()

	err = m.Close()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "errors closing database connections")
	assert.Contains(t, err.Error(), "close failure 1")
	assert.Contains(t, err.Error(), "close failure 2", "Close must surface ALL connection close errors, not just the first")
}

// TestDbManagerZeroValueMethodsAreSafe pins that a zero-value DbManager (never built via
// NewDbManager — the lightweight stand-in the debug/health endpoint and prewarm paths use) does
// not panic on any of Stats/Close/Get/Size, matching the pre-resourcepool field-based behavior
// (Stats/Size/Close were nil-map-safe; Get is guarded to fail closed rather than panic).
func TestDbManagerZeroValueMethodsAreSafe(t *testing.T) {
	m := &DbManager{}

	stats := m.Stats()
	assert.Equal(t, 0, stats["active_connections"])
	assert.Equal(t, 0, stats["max_connections"])
	assert.Equal(t, 0, stats["idle_ttl_seconds"])
	assert.Empty(t, stats["connections"])

	assert.Equal(t, 0, m.Size(), "zero-value Size must be 0, not panic")

	conn, release, err := m.Get(context.Background(), "any")
	assert.ErrorIs(t, err, errManagerClosed, "zero-value Get must fail closed, not panic")
	assert.Nil(t, conn)
	assert.Nil(t, release)

	assert.NotPanics(t, func() {
		m.StartCleanup(time.Minute)
		m.StopCleanup()
	}, "zero-value StartCleanup/StopCleanup must be no-ops, not panic")

	assert.NoError(t, m.Close(), "closing a never-initialized manager is a no-op")
}

func TestDbManagerStatsEmptyManager(t *testing.T) {
	m := NewDbManager(&stubResourceSource{configs: map[string]*config.DatabaseConfig{}}, newTestLogger(), DbManagerOptions{
		MaxSize: 5,
		IdleTTL: 10 * time.Minute,
	}, func(*config.DatabaseConfig, logger.Logger) (Interface, error) { return nil, errors.New("not used") })

	stats := m.Stats()
	assert.Equal(t, 0, stats["active_connections"])
	assert.Equal(t, 5, stats["max_connections"])
	assert.Equal(t, 600, stats["idle_ttl_seconds"])
	conns, ok := stats["connections"].([]map[string]any)
	require.True(t, ok, "connections key must be []map[string]any")
	assert.Empty(t, conns, "empty manager has no connection entries")
}

// TestDbManagerStatsPopulatedManager drives Stats() through the public Get surface and pins
// the per-connection detail array: one entry per live connection, each with its key, an
// RFC3339 last_used string, and an int idle_duration. This shape feeds the debug/health
// endpoint, so it must be preserved exactly.
func TestDbManagerStatsPopulatedManager(t *testing.T) {
	stubA := &stubDB{key: "a"}
	stubB := &stubDB{key: "b"}
	connector := func(cfg *config.DatabaseConfig, _ logger.Logger) (Interface, error) {
		if cfg.Host == "host-a" {
			return stubA, nil
		}
		return stubB, nil
	}

	src := &stubResourceSource{configs: map[string]*config.DatabaseConfig{
		"a": {Type: "postgresql", Host: "host-a"},
		"b": {Type: "postgresql", Host: "host-b"},
	}}
	m := NewDbManager(src, newTestLogger(), DbManagerOptions{MaxSize: 5, IdleTTL: time.Hour}, connector)
	defer func() { _ = m.Close() }()

	ctx := context.Background()
	_, relA, err := m.Get(ctx, "a")
	require.NoError(t, err)
	defer relA()
	_, relB, err := m.Get(ctx, "b")
	require.NoError(t, err)
	defer relB()

	stats := m.Stats()
	assert.Equal(t, 2, stats["active_connections"])
	assert.Equal(t, 5, stats["max_connections"])
	assert.Equal(t, int(time.Hour.Seconds()), stats["idle_ttl_seconds"])

	conns, ok := stats["connections"].([]map[string]any)
	require.True(t, ok, "connections key must be []map[string]any")
	require.Len(t, conns, 2, "per-connection detail must be surfaced for each live connection")

	keys := make([]any, 0, len(conns))
	for _, c := range conns {
		keys = append(keys, c["key"])
		assert.IsType(t, "", c["last_used"], "last_used must be an RFC3339 string")
		assert.IsType(t, 0, c["idle_duration"], "idle_duration must be an int seconds count")
	}
	assert.ElementsMatch(t, []any{"a", "b"}, keys)
}

func TestStartCleanupIsIdempotent(t *testing.T) {
	m := NewDbManager(&stubResourceSource{}, newTestLogger(), DbManagerOptions{
		MaxSize: 5,
		IdleTTL: time.Hour,
	}, func(*config.DatabaseConfig, logger.Logger) (Interface, error) { return &stubDB{}, nil })
	defer func() { _ = m.Close() }()

	m.StartCleanup(10 * time.Second)
	// Second call must observe an already-running cleanup loop and short-circuit rather
	// than spawning a duplicate goroutine.
	require.NotPanics(t, func() {
		m.StartCleanup(10 * time.Second)
	})

	m.StopCleanup()
	// Second StopCleanup hits the early-return path (no loop running).
	require.NotPanics(t, func() {
		m.StopCleanup()
	})
}

func TestStartCleanupAppliesDefaultIntervalForNonPositive(t *testing.T) {
	m := NewDbManager(&stubResourceSource{}, newTestLogger(), DbManagerOptions{
		MaxSize: 5,
		IdleTTL: time.Hour,
	}, func(*config.DatabaseConfig, logger.Logger) (Interface, error) { return &stubDB{}, nil })
	defer func() { _ = m.Close() }()

	// Zero substitutes the documented 5-min default; we can't inspect the
	// ticker directly so the contract is "no panic + clean stop".
	require.NotPanics(t, func() { m.StartCleanup(0) })
	m.StopCleanup()

	require.NotPanics(t, func() { m.StartCleanup(-5 * time.Second) })
	m.StopCleanup()
}

// --- Black-box helpers for the manager's public Get/Close surface (ADR-032 lease semantics
// are exercised directly in internal/resourcepool). ---

// newClosableDB returns a connector that records each connection's Close() in the
// shared `closed` map under `mu`, keyed by the connection's config Database value.
func newClosableDB(mu *sync.Mutex, closed map[string]bool) Connector {
	return func(cfg *config.DatabaseConfig, _ logger.Logger) (Interface, error) {
		return &stubDB{key: cfg.Database, onClosed: func(key string) {
			mu.Lock()
			closed[key] = true
			mu.Unlock()
		}}, nil
	}
}

func twoTenantSource() *stubResourceSource {
	return &stubResourceSource{configs: map[string]*config.DatabaseConfig{
		"a": {Type: "postgresql", Database: "a"},
		"b": {Type: "postgresql", Database: "b"},
	}}
}

func TestDbManagerGetReturnsNonNilReleaseFunc(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	closed := map[string]bool{}
	m := NewDbManager(twoTenantSource(), newErrorTestLogger(),
		DbManagerOptions{MaxSize: 5, IdleTTL: time.Minute}, newClosableDB(&mu, closed))
	defer func() { _ = m.Close() }()

	conn, release, err := m.Get(ctx, "a")
	require.NoError(t, err)
	require.NotNil(t, conn)
	require.NotNil(t, release, "Get must return a non-nil release so callers can always defer it")

	// Releasing a still-cached (non-evicted) connection must NOT close it.
	release()
	mu.Lock()
	wasClosed := closed["a"]
	mu.Unlock()
	assert.False(t, wasClosed, "releasing a lease on a live cached connection must not close it")
	assert.Equal(t, 1, m.Size())
}

// TestDbManagerDynamicConfigGetsPoolDefaults proves a dynamic DBConfigProvider
// (source.type=dynamic) that returns a zero-value Pool no longer reaches the
// connector unnormalized: the create callback applies the same pool defaults
// config.Validate applies to static config, so the PostgreSQL/Oracle connectors
// never call SetMaxOpenConns(0) (unlimited connections).
func TestDbManagerDynamicConfigGetsPoolDefaults(t *testing.T) {
	ctx := context.Background()
	resource := &stubResourceSource{configs: map[string]*config.DatabaseConfig{
		"tenant": {Type: "postgresql", Host: "localhost"}, // zero-value Pool
	}}

	var captured *config.DatabaseConfig
	connector := func(cfg *config.DatabaseConfig, _ logger.Logger) (Interface, error) {
		captured = cfg
		return &stubDB{}, nil
	}
	manager := NewDbManager(resource, newErrorTestLogger(), DbManagerOptions{}, connector)
	defer func() { _ = manager.Close() }()

	_, release, err := manager.Get(ctx, "tenant")
	require.NoError(t, err)
	release()
	require.NotNil(t, captured)

	assert.Equal(t, int32(25), captured.Pool.Max.Connections, "max connections defaults to 25")
	assert.Equal(t, captured.Pool.Max.Connections, captured.Pool.Idle.Connections, "idle tracks max")
	assert.Equal(t, 5*time.Minute, captured.Pool.Idle.Time, "idle time defaults to 5m")
	assert.Equal(t, 30*time.Minute, captured.Pool.Lifetime.Max, "lifetime max defaults to 30m")
	require.NotNil(t, captured.Pool.KeepAlive.Enabled)
	assert.True(t, *captured.Pool.KeepAlive.Enabled, "keepalive defaults to enabled")
	assert.Equal(t, 60*time.Second, captured.Pool.KeepAlive.Interval, "keepalive interval defaults to 60s")
	assert.Equal(t, "UTC", captured.Timezone, "timezone defaults to UTC")
	assert.Equal(t, 1000, captured.Query.Log.MaxLength, "query log max length defaults to 1000")
	assert.Equal(t, 200*time.Millisecond, captured.Query.Slow.Threshold, "slow query threshold defaults to 200ms")

	// The provider-owned config must stay pristine: defaults are applied to a clone.
	assert.NotSame(t, resource.configs["tenant"], captured)
	assert.Zero(t, resource.configs["tenant"].Pool.Max.Connections, "provider config pool untouched")
	assert.Empty(t, resource.configs["tenant"].Timezone, "provider config timezone untouched")
}

// TestDbManagerDynamicConfigExplicitPoolPreserved proves that pool defaulting
// on the dynamic path only fills zero values — a dynamic config that already
// sets an explicit pool size passes through unchanged.
func TestDbManagerDynamicConfigExplicitPoolPreserved(t *testing.T) {
	ctx := context.Background()
	resource := &stubResourceSource{configs: map[string]*config.DatabaseConfig{
		"tenant": {
			Type: "postgresql",
			Host: "localhost",
			Pool: config.PoolConfig{
				Max: config.PoolMaxConfig{Connections: 40},
			},
		},
	}}

	var captured *config.DatabaseConfig
	connector := func(cfg *config.DatabaseConfig, _ logger.Logger) (Interface, error) {
		captured = cfg
		return &stubDB{}, nil
	}
	manager := NewDbManager(resource, newErrorTestLogger(), DbManagerOptions{}, connector)
	defer func() { _ = manager.Close() }()

	_, release, err := manager.Get(ctx, "tenant")
	require.NoError(t, err)
	release()
	require.NotNil(t, captured)

	assert.Equal(t, int32(40), captured.Pool.Max.Connections, "explicit max connections preserved")
	assert.Equal(t, int32(40), captured.Pool.Idle.Connections, "idle defaults to explicit max")
}

// TestDbManagerDynamicConfigInvalidPoolRejected proves an invalid dynamic pool
// config fails the create callback before the connector is ever invoked, surfacing
// through Get.
func TestDbManagerDynamicConfigInvalidPoolRejected(t *testing.T) {
	ctx := context.Background()
	resource := &stubResourceSource{configs: map[string]*config.DatabaseConfig{
		"tenant": {
			Type: "postgresql",
			Host: "localhost",
			Pool: config.PoolConfig{
				Idle: config.PoolIdleConfig{Time: -1},
			},
		},
	}}

	connectorCalled := false
	connector := func(*config.DatabaseConfig, logger.Logger) (Interface, error) {
		connectorCalled = true
		return &stubDB{}, nil
	}
	manager := NewDbManager(resource, newErrorTestLogger(), DbManagerOptions{}, connector)

	_, _, err := manager.Get(ctx, "tenant")
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to apply pool defaults for key")
	var cfgErr *config.ConfigError
	require.ErrorAs(t, err, &cfgErr, "wraps the underlying config validation error")
	assert.ErrorContains(t, err, "database.pool.idle.time")
	assert.False(t, connectorCalled, "connector must not run with an invalid pool config")
}

// TestDbManagerDynamicConfigConcurrentCreateSharedConfig guards the clone in the create
// callback: every key resolves to the SAME shared provider config pointer, so concurrent
// creates for distinct keys each shallow-clone that one struct simultaneously. Under -race
// this proves the clone never races the shared config, expressed through the public Get
// surface (singleflight would collapse same-key creates, so distinct keys are used).
func TestDbManagerDynamicConfigConcurrentCreateSharedConfig(t *testing.T) {
	ctx := context.Background()
	shared := &config.DatabaseConfig{Type: "postgresql", Host: "localhost"}
	const goroutines = 8
	configs := make(map[string]*config.DatabaseConfig, goroutines)
	for i := range goroutines {
		configs[fmt.Sprintf("tenant-%d", i)] = shared
	}
	resource := &stubResourceSource{configs: configs}
	connector := func(*config.DatabaseConfig, logger.Logger) (Interface, error) {
		return &stubDB{}, nil
	}
	manager := NewDbManager(resource, newErrorTestLogger(), DbManagerOptions{MaxSize: 20, IdleTTL: time.Minute}, connector)
	defer func() { _ = manager.Close() }()

	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	for i := range goroutines {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, release, err := manager.Get(ctx, fmt.Sprintf("tenant-%d", i))
			if release != nil {
				release()
			}
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}
