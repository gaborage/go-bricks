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

type nilConfigSource struct{}

func (n *nilConfigSource) DBConfig(context.Context, string) (*config.DatabaseConfig, error) {
	return nil, nil
}

// panickingResourceSource panics on its first DBConfig call and resolves normally afterwards, so
// one manager can prove both that a panicking source fails only that call and that the pool stays
// usable for the next one.
type panickingResourceSource struct {
	panicVal any
	calls    atomic.Int32
}

func (p *panickingResourceSource) DBConfig(context.Context, string) (*config.DatabaseConfig, error) {
	if p.calls.Add(1) == 1 {
		panic(p.panicVal)
	}
	return &config.DatabaseConfig{Type: "postgresql", Database: "after-panic"}, nil
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
	_, relX, err := manager.Get(ctx, "tenant-x")
	require.NoError(t, err)
	_, relY, err := manager.Get(ctx, "tenant-y")
	require.NoError(t, err)
	relX()
	relY()

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

// TestDbManagerGetRecoversSourcePanic proves end-to-end that a panic in a consumer-supplied
// DBConfigProvider no longer kills the process: the pool's singleflight guard converts it into a
// type-only error for that Get (ADR-081 — the panic value never reaches the message), and a later
// Get for the same tenant succeeds.
func TestDbManagerGetRecoversSourcePanic(t *testing.T) {
	const marker = "marker-abc123"
	ctx := context.Background()
	connector := func(cfg *config.DatabaseConfig, _ logger.Logger) (Interface, error) {
		return &stubDB{key: cfg.Database}, nil
	}
	manager := NewDbManager(&panickingResourceSource{panicVal: errors.New(marker)}, newErrorTestLogger(), DbManagerOptions{}, connector)
	defer manager.Close()

	_, rel, err := manager.Get(ctx, tenantA)
	require.Error(t, err)
	assert.Nil(t, rel)
	assert.NotContains(t, err.Error(), marker, "the panic value must never reach the error text")
	require.ErrorContains(t, err, "panic during create")
	require.ErrorContains(t, err, "*errors.errorString")

	db, rel2, err := manager.Get(ctx, tenantA)
	require.NoError(t, err)
	require.NotNil(t, rel2)
	defer rel2()
	assert.NotNil(t, db)
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
	assert.Nil(t, conn)
	assert.Nil(t, release)
	assert.Equal(t, 0, m.Size(), "no connection may be created on a closed manager")
	require.ErrorIs(t, err, errManagerClosed, "Get after Close must fail closed, not resurrect a connection (F22)")
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
	assert.Nil(t, conn)
	assert.Nil(t, release)
	assert.NotPanics(t, func() {
		m.StartCleanup(time.Minute)
		m.StopCleanup()
	}, "zero-value StartCleanup/StopCleanup must be no-ops, not panic")

	require.ErrorIs(t, err, errManagerClosed, "zero-value Get must fail closed, not panic")

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

// TestDbManagerStatsSurfacesPoolErrors pins that a deferred-close failure — a connection
// still borrowed when Close runs, closed only at its final release (ADR-032, C581.3) — is
// not silently dropped: PoolStats.Errors must reach Stats()["errors"] so callers can observe
// it, since it is deliberately excluded from Close()'s returned error.
func TestDbManagerStatsSurfacesPoolErrors(t *testing.T) {
	stub := &stubDB{key: "a", closeErr: errors.New("deferred close failure")}
	connector := func(*config.DatabaseConfig, logger.Logger) (Interface, error) { return stub, nil }
	src := &stubResourceSource{configs: map[string]*config.DatabaseConfig{"a": {Type: "postgresql"}}}
	m := NewDbManager(src, newErrorTestLogger(), DbManagerOptions{MaxSize: 5, IdleTTL: time.Hour}, connector)

	ctx := context.Background()
	_, release, err := m.Get(ctx, "a")
	require.NoError(t, err)
	assert.Equal(t, 0, m.Stats()["errors"], "no close attempted yet")

	// Close leaves the still-borrowed connection open (liveLeases > 0); the deferred close
	// attempt — and its failure — only happens once the lease is released.
	require.NoError(t, m.Close(), "Close must not surface a deferred close failure")
	release()

	assert.Equal(t, 1, m.Stats()["errors"], "deferred close failure must be counted and surfaced")
}

// TestNewDbManagerStartsIdleCleanup pins ADR-067 decision 4: the manager starts its own idle
// sweep at construction, exactly as cache.NewCacheManager does. No StartCleanup call appears
// in this test — a swept connection is the proof that the constructor started the loop.
func TestNewDbManagerStartsIdleCleanup(t *testing.T) {
	src := &stubResourceSource{configs: map[string]*config.DatabaseConfig{
		tenantA: {Type: "postgresql", Database: tenantA},
	}}
	m := NewDbManager(src, newErrorTestLogger(), DbManagerOptions{
		MaxSize:         5,
		IdleTTL:         10 * time.Millisecond,
		CleanupInterval: 10 * time.Millisecond,
	}, func(*config.DatabaseConfig, logger.Logger) (Interface, error) { return &stubDB{key: tenantA}, nil })
	defer func() { _ = m.Close() }()

	_, release, err := m.Get(context.Background(), tenantA)
	require.NoError(t, err)
	release()

	assert.Eventually(t, func() bool {
		return m.Stats()["active_connections"] == 0
	}, 2*time.Second, 10*time.Millisecond, "the constructor must start the idle-cleanup sweep")
}

// TestNewDbManagerClosesCleanlyWithALiveCleanupLoop pins the other half of ADR-067 decision 4:
// the sweep the constructor started is stopped by Close (pool.Close joins the loop), so a
// caller that never touches StartCleanup/StopCleanup still shuts down cleanly.
func TestNewDbManagerClosesCleanlyWithALiveCleanupLoop(t *testing.T) {
	m := NewDbManager(&stubResourceSource{}, newErrorTestLogger(), DbManagerOptions{
		MaxSize:         5,
		IdleTTL:         10 * time.Millisecond,
		CleanupInterval: 10 * time.Millisecond,
	}, func(*config.DatabaseConfig, logger.Logger) (Interface, error) { return &stubDB{}, nil })

	require.NoError(t, m.Close(), "Close must stop the constructor-started sweep and report success")
	require.NoError(t, m.Close(), "Close stays idempotent")
}

// TestNewDbManagerWarnsWhenCleanupIntervalIsNotBelowIdleTTL pins that the advisory that used to
// live in App.warnIfCleanupIntervalTooLate now fires from the manager that owns the pool, under
// this manager's keys. The predicate itself is exhausted in
// internal/resourcepool/cleanup_warning_test.go, so only what is manager-specific stays here:
// the non-positive-CleanupInterval default is applied BEFORE the check (a raw 0 would be below
// any TTL and stay silent), and a genuinely faster sweep still says nothing.
func TestNewDbManagerWarnsWhenCleanupIntervalIsNotBelowIdleTTL(t *testing.T) {
	tests := []struct {
		name            string
		cleanupInterval time.Duration
		idleTTL         time.Duration
		wantWarn        bool
	}{
		{name: "interval_below_ttl_silent", cleanupInterval: time.Minute, idleTTL: time.Hour, wantWarn: false},
		{name: "unset_interval_takes_the_default_and_warns_against_a_short_ttl", cleanupInterval: 0, idleTTL: time.Minute, wantWarn: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &warnRecorder{}
			m := NewDbManager(&stubResourceSource{}, rec, DbManagerOptions{
				MaxSize:         5,
				IdleTTL:         tc.idleTTL,
				CleanupInterval: tc.cleanupInterval,
			}, func(*config.DatabaseConfig, logger.Logger) (Interface, error) { return &stubDB{}, nil })
			defer func() { _ = m.Close() }()

			if !tc.wantWarn {
				assert.Empty(t, rec.warns, "a sweep that outpaces the TTL must not WARN")
				return
			}
			require.Len(t, rec.warns, 1, "the advisory must fire exactly once per manager")
			assert.Contains(t, rec.warns[0], "database.manager.cleanupinterval is >= database.manager.idlettl")
		})
	}
}

func TestStartCleanupIsIdempotent(t *testing.T) {
	m := NewDbManager(&stubResourceSource{}, newTestLogger(), DbManagerOptions{
		MaxSize: 5,
		IdleTTL: time.Hour,
	}, func(*config.DatabaseConfig, logger.Logger) (Interface, error) { return &stubDB{}, nil })
	defer func() { _ = m.Close() }()

	// The constructor already started a loop (ADR-067); stop it so the first call below is
	// the one that starts a loop and the second is the one that must short-circuit.
	m.StopCleanup()

	m.StartCleanup(10 * time.Second)
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

	m.StopCleanup() // drop the constructor's loop so these calls are the ones that start one

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

// TestDbManagerDynamicConfigInfersTypeFromConnectionString proves the
// consumer-visible half of ADR-050's seam inference: a dynamic DBConfigProvider
// returning a DSN-only config reaches the connector with Type populated, so the
// tenant dials instead of failing every request on the factory's empty-type
// dispatch. The non-empty db_type this test asserts is the same value [C59.6]
// tells operators to look for in the "Created new database connection" log line.
func TestDbManagerDynamicConfigInfersTypeFromConnectionString(t *testing.T) {
	ctx := context.Background()
	// No credentials: scheme inference reads only the scheme, and the stub connector
	// never authenticates.
	resource := &stubResourceSource{configs: map[string]*config.DatabaseConfig{
		"tenant": {ConnectionString: "postgres://localhost:5432/db"},
	}}
	providerOwned := *resource.configs["tenant"]

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
	assert.Equal(t, "postgresql", captured.Type, "the connector receives the type inferred from the DSN scheme")
	// Compare the whole value, not just Type: normalization must not reach the
	// provider's struct through any field (ConnectionString, Pool, TLS, vendor blocks).
	assert.Equal(t, providerOwned, *resource.configs["tenant"], "the provider-owned config stays untouched")
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
	var cfgErr *config.ConfigError
	require.ErrorAs(t, err, &cfgErr, "the config validation error reaches the caller typed")
	// C60.19: a dynamically-resolved tenant is addressed exactly as a statically-declared
	// one, so a consumer routing on Field cannot tell the two doors apart. The manager no
	// longer wraps, because the key is already inside the field.
	assert.Equal(t, "multitenant.tenants.tenant.database.pool.idle.time", cfgErr.Field)
	assert.NotContains(t, err.Error(), "failed to apply pool defaults for key",
		"the seam addresses the error; a wrap would print the key twice")
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

// TestDbManagerGetRejectsNilConfigFromProvider proves a provider returning (nil, nil)
// surfaces as ErrNoDatabaseConfig at Get instead of dereferencing the nil config.
func TestDbManagerGetRejectsNilConfigFromProvider(t *testing.T) {
	ctx := context.Background()
	manager := NewDbManager(&nilConfigSource{}, newErrorTestLogger(), DbManagerOptions{}, nil)
	defer func() { _ = manager.Close() }()

	var conn Interface
	var release ReleaseFunc
	var err error
	require.NotPanics(t, func() {
		conn, release, err = manager.Get(ctx, tenantA)
	})
	assert.Nil(t, conn)
	assert.Nil(t, release)
	assert.Equal(t, 0, manager.Size())
	require.ErrorIs(t, err, ErrNoDatabaseConfig)
	require.ErrorContains(t, err, tenantA)
}

// togglingConfigSource returns (nil, nil) until ready flips, then a usable config. It tallies
// calls so a test can prove concurrent Gets collapsed onto one of them, and sleeps while not
// ready to widen the singleflight window the followers have to arrive in.
type togglingConfigSource struct {
	ready atomic.Bool
	calls atomic.Int32
	delay time.Duration
}

func (t *togglingConfigSource) DBConfig(context.Context, string) (*config.DatabaseConfig, error) {
	t.calls.Add(1)
	if !t.ready.Load() {
		time.Sleep(t.delay)
		return nil, nil
	}
	return &config.DatabaseConfig{Type: "postgresql", Host: "localhost"}, nil
}

// TestDbManagerGetNilConfigCollapsesAndRecovers proves concurrent Gets collapsed by
// singleflight all receive ErrNoDatabaseConfig without a goroutine dying, and that the
// key stays creatable once the provider starts returning a config.
func TestDbManagerGetNilConfigCollapsesAndRecovers(t *testing.T) {
	ctx := context.Background()
	source := &togglingConfigSource{delay: 20 * time.Millisecond}
	connector := func(*config.DatabaseConfig, logger.Logger) (Interface, error) {
		return &stubDB{}, nil
	}
	manager := NewDbManager(source, newErrorTestLogger(), DbManagerOptions{}, connector)
	defer func() { _ = manager.Close() }()

	const goroutines = 2
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, release, err := manager.Get(ctx, tenantA)
			if release != nil {
				release()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	assert.Equal(t, 0, manager.Size())
	assert.Equal(t, int32(1), source.calls.Load(), "singleflight must collapse the failing resolution to one provider call")
	for err := range errs {
		require.ErrorIs(t, err, ErrNoDatabaseConfig)
	}

	source.ready.Store(true)
	conn, release, err := manager.Get(ctx, tenantA)
	require.NoError(t, err)
	require.NotNil(t, release)
	assert.NotNil(t, conn)
	release()
}
