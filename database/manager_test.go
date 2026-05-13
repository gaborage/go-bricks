package database

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
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

	first, err := manager.Get(ctx, tenantA)
	require.NoError(t, err)
	second, err := manager.Get(ctx, tenantA)
	require.NoError(t, err)
	assert.Same(t, first, second)
	assert.Equal(t, 1, connectorCalls)
	assert.Equal(t, 1, manager.Size())
}

func TestDbManagerSingleflight(t *testing.T) {
	ctx := context.Background()
	log := newErrorTestLogger()

	var mu sync.Mutex
	connectorCalls := 0
	manager := NewDbManager(&stubResourceSource{configs: map[string]*config.DatabaseConfig{
		"tenant-b": {Type: "postgresql"},
	}}, log, DbManagerOptions{MaxSize: 5, IdleTTL: time.Minute}, func(cfg *config.DatabaseConfig, _ logger.Logger) (Interface, error) {
		mu.Lock()
		connectorCalls++
		mu.Unlock()
		return &stubDB{key: cfg.Database}, nil
	})

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := manager.Get(ctx, "tenant-b")
			require.NoError(t, err)
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, connectorCalls)
}

func TestDbManagerEvictsLRU(t *testing.T) {
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
		"a": {Type: "postgresql", Database: "a"},
		"b": {Type: "postgresql", Database: "b"},
		"c": {Type: "postgresql", Database: "c"},
	}}

	manager := NewDbManager(resource, log, DbManagerOptions{MaxSize: 2, IdleTTL: time.Minute}, connector)

	_, err := manager.Get(ctx, "a")
	require.NoError(t, err)
	_, err = manager.Get(ctx, "b")
	require.NoError(t, err)
	_, err = manager.Get(ctx, "c")
	require.NoError(t, err)

	assert.Equal(t, 2, manager.Size())
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, evicted, "a")
}

func TestDbManagerCleanupRemovesIdleConnections(t *testing.T) {
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
		"idle": {Type: "postgresql", Database: "idle"},
	}}

	manager := NewDbManager(resource, log, DbManagerOptions{MaxSize: 2, IdleTTL: 10 * time.Millisecond}, connector)
	_, err := manager.Get(ctx, "idle")
	require.NoError(t, err)

	time.Sleep(20 * time.Millisecond)
	manager.cleanupIdleConnections()

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, evicted, "idle")
	assert.Equal(t, 0, manager.Size())
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
	_, err := manager.Get(ctx, "tenant-x")
	require.NoError(t, err)
	_, err = manager.Get(ctx, "tenant-y")
	require.NoError(t, err)

	err = manager.Close()
	require.NoError(t, err)
	assert.Equal(t, 0, manager.Size())

	mu.Lock()
	defer mu.Unlock()
	assert.ElementsMatch(t, []string{"x", "y"}, evicted)
}

func TestCreateConnectionReturnsErrorWhenConfigFails(t *testing.T) {
	ctx := context.Background()
	configErr := errors.New("config failure")
	manager := NewDbManager(&failingResourceSource{err: configErr}, newErrorTestLogger(), DbManagerOptions{}, nil)

	_, err := manager.createConnection(ctx, "tenant")
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "failed to get database config"))
}

func TestCreateConnectionPropagatesConnectorError(t *testing.T) {
	ctx := context.Background()
	authErr := errors.New("connector failure")
	resource := &stubResourceSource{configs: map[string]*config.DatabaseConfig{"tenant": {Type: "postgresql"}}}
	connector := func(*config.DatabaseConfig, logger.Logger) (Interface, error) {
		return nil, authErr
	}
	manager := NewDbManager(resource, newErrorTestLogger(), DbManagerOptions{}, connector)

	_, err := manager.createConnection(ctx, "tenant")
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "failed to create database connection"))
}

func TestCreateConnectionReturnsExistingInstanceWhenAlreadyCached(t *testing.T) {
	ctx := context.Background()
	resource := &stubResourceSource{configs: map[string]*config.DatabaseConfig{"tenant": {Type: "postgresql"}}}

	connector := func(cfg *config.DatabaseConfig, _ logger.Logger) (Interface, error) {
		return &stubDB{key: cfg.Database}, nil
	}
	manager := NewDbManager(resource, newErrorTestLogger(), DbManagerOptions{MaxSize: 5, IdleTTL: time.Minute}, connector)

	existing := &stubDB{key: "existing"}
	manager.mu.Lock()
	element := manager.lru.PushFront("tenant")
	manager.conns["tenant"] = &dbEntry{conn: existing, element: element, lastUsed: time.Now(), key: "tenant"}
	manager.mu.Unlock()

	conn, err := manager.createConnection(ctx, "tenant")
	require.NoError(t, err)
	assert.Same(t, existing, conn)
}

func TestStartCleanupDoesNotCreateMultipleRoutines(t *testing.T) {
	resource := &stubResourceSource{configs: map[string]*config.DatabaseConfig{"tenant": {Type: "postgresql"}}}
	manager := NewDbManager(resource, newErrorTestLogger(), DbManagerOptions{}, func(*config.DatabaseConfig, logger.Logger) (Interface, error) {
		return &stubDB{}, nil
	})

	manager.StartCleanup(5 * time.Millisecond)
	manager.cleanupMu.Lock()
	first := manager.cleanupCh
	manager.cleanupMu.Unlock()

	manager.StartCleanup(5 * time.Millisecond)
	manager.cleanupMu.Lock()
	second := manager.cleanupCh
	manager.cleanupMu.Unlock()

	assert.Equal(t, first, second)
	manager.StopCleanup()
}

func TestCloseAggregatesErrors(t *testing.T) {
	resource := &stubResourceSource{configs: map[string]*config.DatabaseConfig{}}
	manager := NewDbManager(resource, newErrorTestLogger(), DbManagerOptions{}, nil)

	errA := errors.New("close a")
	errB := errors.New("close b")

	manager.mu.Lock()
	elementA := manager.lru.PushFront("a")
	manager.conns["a"] = &dbEntry{conn: &stubDB{key: "a", closeErr: errA}, element: elementA, lastUsed: time.Now(), key: "a"}
	elementB := manager.lru.PushFront("b")
	manager.conns["b"] = &dbEntry{conn: &stubDB{key: "b", closeErr: errB}, element: elementB, lastUsed: time.Now(), key: "b"}
	manager.mu.Unlock()

	err := manager.Close()
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "close b"))
	assert.True(t, strings.Contains(err.Error(), "close a"))
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
	_, err := m.Get(ctx, "a")
	require.NoError(t, err)
	_, err = m.Get(ctx, "b")
	require.NoError(t, err)

	stats := m.Stats()
	assert.Equal(t, 2, stats["active_connections"])
	conns, ok := stats["connections"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, conns, 2)

	for _, c := range conns {
		assert.Contains(t, []any{"a", "b"}, c["key"])
		assert.IsType(t, "", c["last_used"])
		assert.IsType(t, 0, c["idle_duration"])
	}
}

func TestStartCleanupIsIdempotent(t *testing.T) {
	m := NewDbManager(&stubResourceSource{}, newTestLogger(), DbManagerOptions{
		MaxSize: 5,
		IdleTTL: time.Hour,
	}, func(*config.DatabaseConfig, logger.Logger) (Interface, error) { return &stubDB{}, nil })
	defer func() { _ = m.Close() }()

	m.StartCleanup(10 * time.Second)
	// Second call must observe cleanupCh != nil and short-circuit rather
	// than spawning a duplicate goroutine.
	require.NotPanics(t, func() {
		m.StartCleanup(10 * time.Second)
	})

	m.StopCleanup()
	// Second StopCleanup hits the early-return path (cleanupCh == nil).
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

func TestCleanupIdleConnectionsLogsCloseError(t *testing.T) {
	failingStub := &stubDB{key: "x", closeErr: errors.New("driver fault on close")}
	connector := func(*config.DatabaseConfig, logger.Logger) (Interface, error) { return failingStub, nil }

	m := NewDbManager(&stubResourceSource{}, newTestLogger(), DbManagerOptions{
		MaxSize: 5,
		IdleTTL: time.Hour,
	}, connector)
	defer func() { _ = m.Close() }()

	ctx := context.Background()
	_, err := m.Get(ctx, "x")
	require.NoError(t, err)

	// Backdate lastUsed so the cleanup pass sees the entry as idle. Direct
	// field access under m.mu matches the pattern in
	// TestCloseAggregatesErrors above.
	m.mu.Lock()
	m.conns["x"].lastUsed = time.Now().Add(-2 * time.Hour)
	m.mu.Unlock()

	m.cleanupIdleConnections()

	assert.Equal(t, 0, m.Size(), "idle connection removed from cache despite Close() error")
	failingStub.closedMu.Lock()
	closed := failingStub.closed
	failingStub.closedMu.Unlock()
	assert.True(t, closed, "Close was attempted even though it returned an error")
}
