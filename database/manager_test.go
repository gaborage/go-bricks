package database

import (
	"context"
	"database/sql"
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
	return nil
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
