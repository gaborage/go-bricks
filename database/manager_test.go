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
	key       string
	closedMu  sync.Mutex
	closed    bool
	closeErr  error
	onClosed  func(string)
	closeHook func() // optional: invoked at the start of Close (e.g. to simulate a slow close)
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
	hook := s.closeHook
	s.closedMu.Unlock()
	if hook != nil {
		hook()
	}
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
			_, _, err := manager.Get(ctx, "tenant-b")
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
	// The LRU victim "a" uses a slow Close to assert eviction detaches bookkeeping
	// under the lock and closes OUTSIDE it: a concurrent Get must not block on it.
	releaseClose := make(chan struct{})
	connector := func(cfg *config.DatabaseConfig, _ logger.Logger) (Interface, error) {
		db := &stubDB{key: cfg.Database, onClosed: func(key string) {
			mu.Lock()
			defer mu.Unlock()
			evicted = append(evicted, key)
		}}
		if cfg.Database == "a" {
			db.closeHook = func() { <-releaseClose }
		}
		return db, nil
	}

	resource := &stubResourceSource{configs: map[string]*config.DatabaseConfig{
		"a": {Type: "postgresql", Database: "a"},
		"b": {Type: "postgresql", Database: "b"},
		"c": {Type: "postgresql", Database: "c"},
	}}

	manager := NewDbManager(resource, log, DbManagerOptions{MaxSize: 2, IdleTTL: time.Minute}, connector)

	_, relA, err := manager.Get(ctx, "a")
	require.NoError(t, err)
	_, _, err = manager.Get(ctx, "b")
	require.NoError(t, err)
	// Release "a"'s lease so eviction may actually close it — a leased connection's
	// close is deferred until its last lease is released (ADR-032).
	relA()

	// Get("c") evicts "a"; its Close blocks until releaseClose, so run it in the
	// background. With close-under-lock this would hold m.mu and stall every Get.
	done := make(chan struct{})
	go func() {
		_, _, gErr := manager.Get(ctx, "c")
		assert.NoError(t, gErr)
		close(done)
	}()

	// Bookkeeping is detached under the lock, so Size drops to 2 even though the
	// victim's Close is still blocked. This would deadlock under close-under-lock.
	assert.Eventually(t, func() bool { return manager.Size() == 2 }, time.Second, 5*time.Millisecond)

	// Release the slow Close and let the eviction finish.
	close(releaseClose)
	<-done

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, evicted, "a")
}

func TestDbManagerCleanupRemovesIdleConnections(t *testing.T) {
	ctx := context.Background()
	log := newErrorTestLogger()

	var mu sync.Mutex
	evicted := []string{}
	// Slow Close on the idle connection proves idle cleanup detaches bookkeeping
	// under the lock and closes OUTSIDE it.
	releaseClose := make(chan struct{})
	connector := func(cfg *config.DatabaseConfig, _ logger.Logger) (Interface, error) {
		return &stubDB{key: cfg.Database, closeHook: func() { <-releaseClose }, onClosed: func(key string) {
			mu.Lock()
			defer mu.Unlock()
			evicted = append(evicted, key)
		}}, nil
	}

	resource := &stubResourceSource{configs: map[string]*config.DatabaseConfig{
		"idle": {Type: "postgresql", Database: "idle"},
	}}

	manager := NewDbManager(resource, log, DbManagerOptions{MaxSize: 2, IdleTTL: 10 * time.Millisecond}, connector)
	_, relIdle, err := manager.Get(ctx, "idle")
	require.NoError(t, err)
	// Release the lease so idle cleanup may actually close it (deferred-until-release, ADR-032).
	relIdle()

	time.Sleep(20 * time.Millisecond)

	// Run cleanup in the background: the idle connection's Close blocks until
	// releaseClose. With close-under-lock this would stall every Size()/Get().
	done := make(chan struct{})
	go func() {
		manager.cleanupIdleConnections()
		close(done)
	}()

	// Bookkeeping is detached under the lock, so Size drops to 0 even while the
	// idle connection's Close is still blocked.
	assert.Eventually(t, func() bool { return manager.Size() == 0 }, time.Second, 5*time.Millisecond)

	// Release the slow Close and let cleanup finish.
	close(releaseClose)
	<-done

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

	entry, err := manager.createConnection(ctx, "tenant")
	require.NoError(t, err)
	assert.Same(t, existing, entry.conn)
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
	_, _, err := m.Get(ctx, "a")
	require.NoError(t, err)
	_, _, err = m.Get(ctx, "b")
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

// TestDbManagerEvictionWithSlowCloseDoesNotBlockConcurrentGet guards the M3 audit
// finding: evictIfNeeded must NOT hold the manager mutex while closing the evicted
// connection. A slow Close() on an evicted tenant's connection must not block a
// concurrent Get() that targets a different, still-cached tenant (head-of-line
// blocking). Mirrors the safe collect-then-close pattern in cache/manager.go.
func TestDbManagerEvictionWithSlowCloseDoesNotBlockConcurrentGet(t *testing.T) {
	ctx := context.Background()
	log := newErrorTestLogger()

	const slowClose = 200 * time.Millisecond
	closeStarted := make(chan struct{})

	connector := func(cfg *config.DatabaseConfig, _ logger.Logger) (Interface, error) {
		db := &stubDB{key: cfg.Database}
		if cfg.Database == "a" {
			// Tenant "a" is the LRU victim. Its Close blocks for slowClose to simulate
			// a stuck connection teardown. Signal once so the test can race a Get.
			var once sync.Once
			db.closeHook = func() {
				once.Do(func() { close(closeStarted) })
				time.Sleep(slowClose)
			}
		}
		return db, nil
	}

	resource := &stubResourceSource{configs: map[string]*config.DatabaseConfig{
		"a": {Type: "postgresql", Database: "a"},
		"b": {Type: "postgresql", Database: "b"},
		"c": {Type: "postgresql", Database: "c"},
	}}

	manager := NewDbManager(resource, log, DbManagerOptions{MaxSize: 2, IdleTTL: time.Minute}, connector)

	_, relA, err := manager.Get(ctx, "a")
	require.NoError(t, err)
	_, _, err = manager.Get(ctx, "b")
	require.NoError(t, err)
	// Release "a"'s lease so the eviction can close it (deferred-until-release, ADR-032).
	relA()

	// Creating "c" evicts the LRU victim "a", whose Close blocks for slowClose.
	go func() {
		_, _, _ = manager.Get(ctx, "c")
	}()

	// Wait until the slow Close has actually begun before measuring.
	select {
	case <-closeStarted:
	case <-time.After(time.Second):
		t.Fatal("eviction Close never started")
	}

	// "b" is still cached: this Get only needs getExisting's lock. If eviction holds
	// the manager mutex across Close (the M3 bug), this blocks for ~slowClose.
	start := time.Now()
	_, _, err = manager.Get(ctx, "b")
	require.NoError(t, err)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, slowClose/2,
		"Get on a cached tenant must not block on another tenant's slow eviction Close (close-under-lock)")
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
	_, relX, err := m.Get(ctx, "x")
	require.NoError(t, err)
	// Release the lease so idle cleanup may close it (deferred-until-release, ADR-032).
	relX()

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

// --- Lease/refcount: eviction-while-in-use race (issue #606, ADR-032) ---

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

func TestDbManagerEvictionWhileLeasedDefersCloseUntilRelease(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	closed := map[string]bool{}
	// MaxSize 1 → getting a second key evicts the first.
	m := NewDbManager(twoTenantSource(), newErrorTestLogger(),
		DbManagerOptions{MaxSize: 1, IdleTTL: time.Minute}, newClosableDB(&mu, closed))
	defer func() { _ = m.Close() }()

	_, releaseA, err := m.Get(ctx, "a")
	require.NoError(t, err)

	// Borrowing "b" evicts "a" from the LRU. "a" is still leased, so it must NOT close.
	_, releaseB, err := m.Get(ctx, "b")
	require.NoError(t, err)
	defer releaseB()

	mu.Lock()
	closedWhileLeased := closed["a"]
	mu.Unlock()
	assert.False(t, closedWhileLeased,
		"an evicted-but-leased connection must not be closed while a lease is held (the #606 race)")

	// Releasing the last lease closes the evicted connection now.
	releaseA()
	mu.Lock()
	closedAfterRelease := closed["a"]
	mu.Unlock()
	assert.True(t, closedAfterRelease,
		"an evicted connection must be closed once its last lease is released")
}

func TestDbManagerTwoLeasesKeepConnectionAliveUntilBothReleased(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	closed := map[string]bool{}
	m := NewDbManager(twoTenantSource(), newErrorTestLogger(),
		DbManagerOptions{MaxSize: 1, IdleTTL: time.Minute}, newClosableDB(&mu, closed))
	defer func() { _ = m.Close() }()

	_, release1, err := m.Get(ctx, "a")
	require.NoError(t, err)
	_, release2, err := m.Get(ctx, "a") // same key, second borrower → refcount 2
	require.NoError(t, err)

	// Evict "a".
	_, releaseB, err := m.Get(ctx, "b")
	require.NoError(t, err)
	defer releaseB()

	release1()
	mu.Lock()
	closedAfterFirst := closed["a"]
	mu.Unlock()
	assert.False(t, closedAfterFirst, "connection must stay open while a second lease is outstanding")

	release2()
	mu.Lock()
	closedAfterSecond := closed["a"]
	mu.Unlock()
	assert.True(t, closedAfterSecond, "connection must close when the final lease is released")
}

func TestDbManagerIdleCleanupWhileLeasedDefersClose(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	closed := map[string]bool{}
	m := NewDbManager(twoTenantSource(), newErrorTestLogger(),
		DbManagerOptions{MaxSize: 5, IdleTTL: time.Nanosecond}, newClosableDB(&mu, closed))
	defer func() { _ = m.Close() }()

	_, releaseA, err := m.Get(ctx, "a")
	require.NoError(t, err)

	time.Sleep(time.Millisecond) // ensure idle threshold passed
	m.cleanupIdleConnections()   // detaches "a" (idle) but it is still leased

	mu.Lock()
	closedWhileLeased := closed["a"]
	mu.Unlock()
	assert.False(t, closedWhileLeased, "idle cleanup must not close a leased connection")

	releaseA()
	mu.Lock()
	closedAfterRelease := closed["a"]
	mu.Unlock()
	assert.True(t, closedAfterRelease, "idle-cleaned connection closes when its last lease is released")
}

func TestDbManagerReleaseIsIdempotent(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	closed := map[string]bool{}
	m := NewDbManager(twoTenantSource(), newErrorTestLogger(),
		DbManagerOptions{MaxSize: 1, IdleTTL: time.Minute}, newClosableDB(&mu, closed))
	defer func() { _ = m.Close() }()

	_, releaseA, err := m.Get(ctx, "a")
	require.NoError(t, err)
	_, releaseB, err := m.Get(ctx, "b") // evict "a"
	require.NoError(t, err)
	defer releaseB()

	assert.NotPanics(t, func() {
		releaseA()
		releaseA() // double release must be a safe no-op (no double close, no negative refcount)
	})
}

// TestDbManagerDynamicConfigGetsPoolDefaults proves a dynamic DBConfigProvider
// (source.type=dynamic) that returns a zero-value Pool no longer reaches the
// connector unnormalized: DbManager.createConnection must apply the same pool
// defaults config.Validate applies to static config, so the PostgreSQL/Oracle
// connectors never call SetMaxOpenConns(0) (unlimited connections).
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

	_, err := manager.createConnection(ctx, "tenant")
	require.NoError(t, err)
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

	_, err := manager.createConnection(ctx, "tenant")
	require.NoError(t, err)
	require.NotNil(t, captured)

	assert.Equal(t, int32(40), captured.Pool.Max.Connections, "explicit max connections preserved")
	assert.Equal(t, int32(40), captured.Pool.Idle.Connections, "idle defaults to explicit max")
}

// TestDbManagerDynamicConfigInvalidPoolRejected proves an invalid dynamic pool
// config fails createConnection before the connector is ever invoked.
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

	_, err := manager.createConnection(ctx, "tenant")
	require.Error(t, err)
	require.ErrorContains(t, err, "failed to apply pool defaults for key")
	var cfgErr *config.ConfigError
	require.ErrorAs(t, err, &cfgErr, "wraps the underlying config validation error")
	assert.ErrorContains(t, err, "database.pool.idle.time")
	assert.False(t, connectorCalled, "connector must not run with an invalid pool config")
}

// TestDbManagerDynamicConfigConcurrentCreateSharedConfig guards the clone in
// createConnection: concurrent creates against a shared provider config must
// not race (make check runs -race).
func TestDbManagerDynamicConfigConcurrentCreateSharedConfig(t *testing.T) {
	ctx := context.Background()
	resource := &stubResourceSource{configs: map[string]*config.DatabaseConfig{
		"tenant": {Type: "postgresql", Host: "localhost"},
	}}
	connector := func(*config.DatabaseConfig, logger.Logger) (Interface, error) {
		return &stubDB{}, nil
	}
	manager := NewDbManager(resource, newErrorTestLogger(), DbManagerOptions{MaxSize: 5, IdleTTL: time.Minute}, connector)

	const goroutines = 8
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := manager.createConnection(ctx, "tenant")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}
