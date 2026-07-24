package tenantstore

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/gaborage/go-bricks/config"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/multitenant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStore implements TableCreator with a controllable CreateTable outcome.
type fakeStore struct {
	vendor    string
	tableName string
	createErr error
	creates   int
}

func (s *fakeStore) CreateTable(context.Context, dbtypes.Interface) error {
	s.creates++
	return s.createErr
}

// newTestDeps builds Deps backed by the given GetDB; the per-vendor
// constructors record which vendor was invoked into the returned slice.
func newTestDeps(getDB func(context.Context) (dbtypes.Interface, error), autoCreate bool, createErr error) (deps *Deps[*fakeStore], constructorVendors *[]string) {
	vendors := []string{}
	newFor := func(vendor string) func(string) (*fakeStore, error) {
		return func(tableName string) (*fakeStore, error) {
			vendors = append(vendors, vendor)
			return &fakeStore{vendor: vendor, tableName: tableName, createErr: createErr}, nil
		}
	}
	d := &Deps[*fakeStore]{
		Name:            "testmod",
		TableName:       "test_table",
		AutoCreateTable: autoCreate,
		Logger:          logger.New("info", false),
		GetDB:           getDB,
		NewPostgres:     newFor(dbtypes.PostgreSQL),
		NewOracle:       newFor(dbtypes.Oracle),
		WarnMsg:         "test table creation failed",
	}
	return d, &vendors
}

func singleDB(db dbtypes.Interface) func(context.Context) (dbtypes.Interface, error) {
	return func(context.Context) (dbtypes.Interface, error) { return db, nil }
}

func TestCacheGetCreatesStorePerTenant(t *testing.T) {
	tenants := dbtesting.NewTenantDBMap()
	tenants.ForTenantWithVendor("tenant-a", dbtypes.PostgreSQL)
	tenants.ForTenantWithVendor("tenant-b", dbtypes.Oracle)
	d, vendors := newTestDeps(tenants.AsGetDBFunc(), false, nil)
	c := &Cache[*fakeStore]{}

	storeA, err := c.Get(multitenant.SetTenant(t.Context(), "tenant-a"), d)
	require.NoError(t, err)
	storeB, err := c.Get(multitenant.SetTenant(t.Context(), "tenant-b"), d)
	require.NoError(t, err)

	assert.NotSame(t, storeA, storeB, "different tenants receive isolated stores")
	assert.Equal(t, dbtypes.PostgreSQL, storeA.vendor, "postgres tenant's store comes from NewPostgres")
	assert.Equal(t, dbtypes.Oracle, storeB.vendor, "oracle tenant's store comes from NewOracle")
	assert.Equal(t, []string{dbtypes.PostgreSQL, dbtypes.Oracle}, *vendors, "each tenant invokes only its own vendor's constructor")
	assert.Equal(t, "test_table", storeA.tableName, "table name passes through to the constructor")
}

func TestCacheGetReturnsCachedStoreOnHit(t *testing.T) {
	d, vendors := newTestDeps(singleDB(dbtesting.NewTestDB(dbtypes.PostgreSQL)), false, nil)
	c := &Cache[*fakeStore]{}

	first, err := c.Get(t.Context(), d)
	require.NoError(t, err)
	second, err := c.Get(t.Context(), d)
	require.NoError(t, err)

	assert.Same(t, first, second, "the empty-tenant store is reused across calls")
	assert.Len(t, *vendors, 1, "the constructor runs once per tenant")
}

func TestCacheGetOraclePath(t *testing.T) {
	d, vendors := newTestDeps(singleDB(dbtesting.NewTestDB(dbtypes.Oracle)), false, nil)
	c := &Cache[*fakeStore]{}

	store, err := c.Get(t.Context(), d)
	require.NoError(t, err)
	assert.Equal(t, dbtypes.Oracle, store.vendor)
	assert.Equal(t, []string{dbtypes.Oracle}, *vendors, "only NewOracle is invoked for an Oracle DB")
}

func TestCacheGetUnknownVendorError(t *testing.T) {
	d, vendors := newTestDeps(singleDB(dbtesting.NewTestDB("mysql")), false, nil)
	c := &Cache[*fakeStore]{}

	store, err := c.Get(t.Context(), d)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "testmod: unsupported database vendor: mysql")
	assert.Nil(t, store)
	assert.Empty(t, *vendors, "no constructor runs for an unknown vendor")
	_, ok := c.Cached("")
	assert.False(t, ok)
}

func TestCacheGetReturnsGetDBError(t *testing.T) {
	cause := errors.New("connection refused")
	d, _ := newTestDeps(func(context.Context) (dbtypes.Interface, error) { return nil, cause }, false, nil)
	c := &Cache[*fakeStore]{}

	store, err := c.Get(t.Context(), d)
	require.Error(t, err)
	assert.ErrorIs(t, err, cause)
	assert.Contains(t, err.Error(), "testmod: database unavailable")
	assert.Nil(t, store)
	_, ok := c.Cached("")
	assert.False(t, ok, "a failed init caches nothing, so it can retry")
}

func TestCacheGetReturnsConstructorError(t *testing.T) {
	cause := errors.New("bad table name")
	d, _ := newTestDeps(singleDB(dbtesting.NewTestDB(dbtypes.PostgreSQL)), false, nil)
	calls := 0
	d.NewPostgres = func(string) (*fakeStore, error) {
		calls++
		return nil, cause
	}
	c := &Cache[*fakeStore]{}

	_, err := c.Get(t.Context(), d)
	require.ErrorIs(t, err, cause)
	assert.Contains(t, err.Error(), "testmod: failed to create store")

	_, err = c.Get(t.Context(), d)
	require.ErrorIs(t, err, cause)
	assert.Equal(t, 2, calls, "a failed init retries instead of caching the failure")
}

func TestCacheGetAutoCreateTableWarnOnly(t *testing.T) {
	d, _ := newTestDeps(singleDB(dbtesting.NewTestDB(dbtypes.PostgreSQL)), true, errors.New("ORA-00955"))
	c := &Cache[*fakeStore]{}

	store, err := c.Get(t.Context(), d)
	require.NoError(t, err, "a CreateTable failure is warn-only; Get still succeeds")
	assert.Equal(t, 1, store.creates)

	again, err := c.Get(t.Context(), d)
	require.NoError(t, err)
	assert.Same(t, store, again, "the store is cached despite the CreateTable failure")
	assert.Equal(t, 1, store.creates, "one CreateTable attempt per tenant")
}

func TestCacheGetSkipsCreateTableWhenDisabled(t *testing.T) {
	d, _ := newTestDeps(singleDB(dbtesting.NewTestDB(dbtypes.PostgreSQL)), false, nil)
	c := &Cache[*fakeStore]{}

	store, err := c.Get(t.Context(), d)
	require.NoError(t, err)
	assert.Zero(t, store.creates, "CreateTable is not attempted when AutoCreateTable is false")
}

func TestCacheCached(t *testing.T) {
	d, _ := newTestDeps(singleDB(dbtesting.NewTestDB(dbtypes.PostgreSQL)), false, nil)
	c := &Cache[*fakeStore]{}

	_, ok := c.Cached("")
	assert.False(t, ok, "miss before first Get")

	store, err := c.Get(t.Context(), d)
	require.NoError(t, err)

	cached, ok := c.Cached("")
	assert.True(t, ok, "hit after Get")
	assert.Same(t, store, cached)

	_, ok = c.Cached("other-tenant")
	assert.False(t, ok, "miss for a tenant never initialized")
}

func TestCacheGetTenantsInitializeIndependently(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	getDB := func(ctx context.Context) (dbtypes.Interface, error) {
		if tid, _ := multitenant.GetTenant(ctx); tid == "tenant-slow" {
			started <- struct{}{}
			<-release
		}
		return dbtesting.NewTestDB(dbtypes.PostgreSQL), nil
	}
	d, _ := newTestDeps(getDB, false, nil)
	c := &Cache[*fakeStore]{}

	done := make(chan error, 1)
	go func() {
		_, err := c.Get(multitenant.SetTenant(context.Background(), "tenant-slow"), d)
		done <- err
	}()

	<-started // tenant-slow is now blocked inside its init

	// With a cache-wide init lock this Get would deadlock behind tenant-slow;
	// per-tenant locking must let an unrelated tenant proceed immediately.
	_, err := c.Get(multitenant.SetTenant(context.Background(), "tenant-fast"), d)
	require.NoError(t, err, "an unrelated tenant must not block on another tenant's slow init")

	close(release)
	require.NoError(t, <-done, "the slow tenant's init completes once its DB responds")

	_, ok := c.Cached("tenant-slow")
	assert.True(t, ok)
	_, ok = c.Cached("tenant-fast")
	assert.True(t, ok)
}

func TestCacheGetSameTenantSerializesInit(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	getDB := func(context.Context) (dbtypes.Interface, error) {
		entered <- struct{}{}
		<-release
		return dbtesting.NewTestDB(dbtypes.PostgreSQL), nil
	}
	d, vendors := newTestDeps(getDB, false, nil)
	c := &Cache[*fakeStore]{}

	stores := make(chan *fakeStore, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			s, err := c.Get(context.Background(), d)
			stores <- s
			errs <- err
		}()
	}

	// Exactly one goroutine wins the tenant init lock and enters GetDB; hold it
	// there until the other is observably blocked on that lock, so the loser
	// deterministically takes the double-checked cache hit after the handoff.
	<-entered
	waitForGoroutineBlockedOnTenantLock()
	close(release)

	s1, s2 := <-stores, <-stores
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	assert.Same(t, s1, s2, "both concurrent callers receive the same store")
	assert.Len(t, *vendors, 1, "the constructor runs exactly once for a contended tenant")
}

func TestCacheGetSingleKeyIgnoresCtxTenant(t *testing.T) {
	d, vendors := newTestDeps(singleDB(dbtesting.NewTestDB(dbtypes.PostgreSQL)), false, nil)
	d.SingleKey = true
	c := &Cache[*fakeStore]{}

	storeA, err := c.Get(multitenant.SetTenant(context.Background(), "tenant-a"), d)
	require.NoError(t, err)
	storeB, err := c.Get(multitenant.SetTenant(context.Background(), "tenant-b"), d)
	require.NoError(t, err)

	assert.Same(t, storeA, storeB, "SingleKey caches under the empty key regardless of ctx tenant")
	assert.Len(t, *vendors, 1, "GetDB/constructor is consulted exactly once across different ctx tenants")

	cached, ok := c.Cached("")
	assert.True(t, ok, "the store is cached under the empty key")
	assert.Same(t, storeA, cached)
}

func TestSharedResolversRequiredPrefixesModule(t *testing.T) {
	err := SharedResolversRequired("outbox")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outbox: tenancy=shared requires framework-injected shared resolvers")
	assert.Contains(t, err.Error(), "app.RegisterModule")
}

func TestRejectSharedWithStaticTenants(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.Config
		wantErr bool
	}{
		{name: "nil_config", cfg: nil, wantErr: false},
		{name: "multitenant_disabled", cfg: &config.Config{}, wantErr: false},
		{name: "dynamic_source_with_tenants", cfg: &config.Config{
			Multitenant: config.MultitenantConfig{Enabled: true, Tenants: map[string]config.TenantEntry{"a": {}}},
			Source:      config.SourceConfig{Type: config.SourceTypeDynamic},
		}, wantErr: false},
		{name: "static_source_no_tenants", cfg: &config.Config{
			Multitenant: config.MultitenantConfig{Enabled: true},
		}, wantErr: false},
		{name: "static_source_with_tenants", cfg: &config.Config{
			Multitenant: config.MultitenantConfig{Enabled: true, Tenants: map[string]config.TenantEntry{"a": {}}},
		}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RejectSharedWithStaticTenants("inbox", tt.cfg)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "inbox: tenancy=shared cannot be combined with static")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// waitForGoroutineBlockedOnTenantLock spins until some goroutine's stack shows
// it waiting on the per-tenant init mutex inside Cache.Get. Stack-based because
// sync.Mutex exposes no waiter count; converges once the loser reaches the lock
// (the winner is parked pre-store, so the loser's fast-path check always misses).
func waitForGoroutineBlockedOnTenantLock() {
	buf := make([]byte, 1<<20)
	for {
		stacks := string(buf[:runtime.Stack(buf, true)])
		for _, g := range strings.Split(stacks, "\n\n") {
			if strings.Contains(g, "sync.(*Mutex).Lock") && strings.Contains(g, "tenantstore.go") {
				return
			}
		}
		runtime.Gosched()
	}
}
