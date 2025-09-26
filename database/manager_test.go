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
	"github.com/gaborage/go-bricks/logger"
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
func (s *stubStatement) QueryRow(_ context.Context, _ ...any) *sql.Row        { return nil }
func (s *stubStatement) Exec(_ context.Context, _ ...any) (sql.Result, error) { return nil, nil }
func (s *stubStatement) Close() error                                         { return nil }

type stubTx struct{}

func (s *stubTx) Query(_ context.Context, _ string, _ ...any) (*sql.Rows, error) { return nil, nil }
func (s *stubTx) QueryRow(_ context.Context, _ string, _ ...any) *sql.Row        { return nil }
func (s *stubTx) Exec(_ context.Context, _ string, _ ...any) (sql.Result, error) { return nil, nil }
func (s *stubTx) Prepare(_ context.Context, _ string) (Statement, error) {
	return &stubStatement{}, nil
}
func (s *stubTx) Commit() error   { return nil }
func (s *stubTx) Rollback() error { return nil }

type stubDB struct {
	key      string
	closedMu sync.Mutex
	closed   bool
	closeErr error
	onClosed func(string)
}

func (s *stubDB) Query(_ context.Context, _ string, _ ...any) (*sql.Rows, error) { return nil, nil }
func (s *stubDB) QueryRow(_ context.Context, _ string, _ ...any) *sql.Row        { return nil }
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
func (s *stubDB) GetMigrationTable() string                  { return "schema_migrations" }
func (s *stubDB) CreateMigrationTable(context.Context) error { return nil }

func TestDbManagerReturnsSameInstanceForSameKey(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

	connectorCalls := 0
	manager := NewDbManager(&stubResourceSource{configs: map[string]*config.DatabaseConfig{
		"tenant-a": {Type: "postgresql"},
	}}, log, DbManagerOptions{MaxSize: 5, IdleTTL: time.Minute}, func(cfg *config.DatabaseConfig, _ logger.Logger) (Interface, error) {
		connectorCalls++
		return &stubDB{key: cfg.Database}, nil
	})

	first, err := manager.Get(ctx, "tenant-a")
	require.NoError(t, err)
	second, err := manager.Get(ctx, "tenant-a")
	require.NoError(t, err)
	assert.Same(t, first, second)
	assert.Equal(t, 1, connectorCalls)
	assert.Equal(t, 1, manager.Size())
}

func TestDbManagerSingleflight(t *testing.T) {
	ctx := context.Background()
	log := logger.New("error", false)

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
	for i := 0; i < 10; i++ {
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
	log := logger.New("error", false)

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
	log := logger.New("error", false)

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
	log := logger.New("error", false)

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
	manager := NewDbManager(&failingResourceSource{err: configErr}, logger.New("error", false), DbManagerOptions{}, nil)

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
	manager := NewDbManager(resource, logger.New("error", false), DbManagerOptions{}, connector)

	_, err := manager.createConnection(ctx, "tenant")
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "failed to create database connection"))
}

func TestCreateConnectionReturnsExistingInstanceWhenAlreadyCached(t *testing.T) {
	ctx := context.Background()
	resource := &stubResourceSource{configs: map[string]*config.DatabaseConfig{"tenant": {Type: "postgresql"}}}

	closed := make(chan struct{}, 1)
	connector := func(cfg *config.DatabaseConfig, _ logger.Logger) (Interface, error) {
		return &stubDB{key: cfg.Database, onClosed: func(string) { closed <- struct{}{} }}, nil
	}
	manager := NewDbManager(resource, logger.New("error", false), DbManagerOptions{MaxSize: 5, IdleTTL: time.Minute}, connector)

	existing := &stubDB{key: "existing"}
	manager.mu.Lock()
	element := manager.lru.PushFront("tenant")
	manager.conns["tenant"] = &dbEntry{conn: existing, element: element, lastUsed: time.Now(), key: "tenant"}
	manager.mu.Unlock()

	conn, err := manager.createConnection(ctx, "tenant")
	require.NoError(t, err)
	assert.Same(t, existing, conn)

	select {
	case <-closed:
		// expected the new connection to be closed immediately
	case <-time.After(time.Second):
		t.Fatalf("expected replacement connection to be closed")
	}
}

func TestStartCleanupDoesNotCreateMultipleRoutines(t *testing.T) {
	resource := &stubResourceSource{configs: map[string]*config.DatabaseConfig{"tenant": {Type: "postgresql"}}}
	manager := NewDbManager(resource, logger.New("error", false), DbManagerOptions{}, func(*config.DatabaseConfig, logger.Logger) (Interface, error) {
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
	manager := NewDbManager(resource, logger.New("error", false), DbManagerOptions{}, nil)

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
